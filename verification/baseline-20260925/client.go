package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"image/png"
	"io"
	"log"
	"math"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"

	"github.com/gorilla/websocket"
	quic "github.com/quic-go/quic-go"
)

// activeHelperSession is set by ensureMFSession and read by encodeJPEGImage.
// It allows JPEG encoding to be offloaded to the mf_helper subprocess.
var activeHelperSession *mfHelperSession
var activeHelperMu sync.Mutex

var (
	user32                            = syscall.NewLazyDLL("user32.dll")
	gdi32                             = syscall.NewLazyDLL("gdi32.dll")
	kernel32                          = syscall.NewLazyDLL("kernel32.dll")
	shell32                           = syscall.NewLazyDLL("shell32.dll")
	procGetModuleHandleW              = kernel32.NewProc("GetModuleHandleW")
	d3d11                             = syscall.NewLazyDLL("d3d11.dll")
	procGetDC                         = user32.NewProc("GetDC")
	procReleaseDC                     = user32.NewProc("ReleaseDC")
	procGetSystemMetrics              = user32.NewProc("GetSystemMetrics")
	procSetProcessDPIAware            = user32.NewProc("SetProcessDPIAware")
	procSetProcessDpiAwarenessContext = user32.NewProc("SetProcessDpiAwarenessContext")
	procSetCursorPos                  = user32.NewProc("SetCursorPos")
	procShowWindow                    = user32.NewProc("ShowWindow")
	procLoadCursorW                   = user32.NewProc("LoadCursorW")
	procGetCursorInfo                 = user32.NewProc("GetCursorInfo")
	procGetIconInfo                   = user32.NewProc("GetIconInfo")
	procEnumDisplayMonitors           = user32.NewProc("EnumDisplayMonitors")
	procGetMonitorInfoW               = user32.NewProc("GetMonitorInfoW")
	procOpenInputDesktop              = user32.NewProc("OpenInputDesktop")
	procCloseDesktop                  = user32.NewProc("CloseDesktop")
	procMouseEvent                    = user32.NewProc("mouse_event")
	procKeybdEvent                    = user32.NewProc("keybd_event")
	procSendInput                     = user32.NewProc("SendInput")
	procMapVirtualKey                 = user32.NewProc("MapVirtualKeyW")
	procVkKeyScan                     = user32.NewProc("VkKeyScanW")
	procOpenClipboard                 = user32.NewProc("OpenClipboard")
	procCloseClipboard                = user32.NewProc("CloseClipboard")
	procEmptyClipboard                = user32.NewProc("EmptyClipboard")
	procGetClipboardData              = user32.NewProc("GetClipboardData")
	procSetClipboardData              = user32.NewProc("SetClipboardData")
	procIsClipboardFormatAvail        = user32.NewProc("IsClipboardFormatAvailable")
	procGetClipboardSequenceNumber    = user32.NewProc("GetClipboardSequenceNumber")
	procRegisterClipboardFormatW      = user32.NewProc("RegisterClipboardFormatW")
	procAddClipboardFormatListener    = user32.NewProc("AddClipboardFormatListener")
	procRemoveClipboardFormatListener = user32.NewProc("RemoveClipboardFormatListener")
	procCreateWindowExW               = user32.NewProc("CreateWindowExW")
	procDefWindowProcW                = user32.NewProc("DefWindowProcW")
	procGetMessageW                   = user32.NewProc("GetMessageW")
	procDispatchMessageW              = user32.NewProc("DispatchMessageW")
	procDestroyWindow                 = user32.NewProc("DestroyWindow")
	procRegisterClassExW              = user32.NewProc("RegisterClassExW")
	procCreateCompatibleDC            = gdi32.NewProc("CreateCompatibleDC")
	procCreateCompatibleBitmap        = gdi32.NewProc("CreateCompatibleBitmap")
	procCreateDIBSection              = gdi32.NewProc("CreateDIBSection")
	procSelectObject                  = gdi32.NewProc("SelectObject")
	procBitBlt                        = gdi32.NewProc("BitBlt")
	procDeleteObject                  = gdi32.NewProc("DeleteObject")
	procDeleteDC                      = gdi32.NewProc("DeleteDC")
	procGetDIBits                     = gdi32.NewProc("GetDIBits")
	procGetObjectW                    = gdi32.NewProc("GetObjectW")
	procGlobalAlloc                   = kernel32.NewProc("GlobalAlloc")
	procGlobalLock                    = kernel32.NewProc("GlobalLock")
	procGlobalUnlock                  = kernel32.NewProc("GlobalUnlock")
	procGlobalFree                    = kernel32.NewProc("GlobalFree")
	procGlobalSize                    = kernel32.NewProc("GlobalSize")
	procGetConsoleWindow              = kernel32.NewProc("GetConsoleWindow")
	procSetConsoleTitleW              = kernel32.NewProc("SetConsoleTitleW")
	procMultiByteToWideChar           = kernel32.NewProc("MultiByteToWideChar")
	procDragQueryFileW                = shell32.NewProc("DragQueryFileW")
	procD3D11CreateDevice             = d3d11.NewProc("D3D11CreateDevice")

	pngClipboardOnce   sync.Once
	pngClipboardFormat uintptr
	clipboardMutex     sync.Mutex

	errDesktopUnavailable = errors.New("desktop unavailable")
)

// buildTime is injected at compile time via -ldflags=-X main.buildTime=...
var buildTime string

const (
	maxClipboardRetries   = 5
	clipboardRetryDelayMS = 50
)

type msg struct {
	hwnd    uintptr
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      point
}

type point struct {
	x int32
	y int32
}

type wndClassEx struct {
	size       uint32
	style      uint32
	wndProc    uintptr
	clsExtra   int32
	wndExtra   int32
	instance   uintptr
	icon       uintptr
	cursor     uintptr
	background uintptr
	menuName   uintptr
	className  uintptr
	iconSm     uintptr
}

const (
	wmDestroy         = 0x0002
	wmClipboardUpdate = 0x031D
	csOwnDC           = 0x0040
	wsOverlapped      = 0x00000000
	wsExToolwindow    = 0x00000080
)

var clipboardWndHwnd uintptr

func tryOpenClipboard() (bool, error) {
	for i := 0; i < maxClipboardRetries; i++ {
		if opened, _, _ := procOpenClipboard.Call(0); opened != 0 {
			return true, nil
		}
		if i < maxClipboardRetries-1 {
			time.Sleep(time.Duration(clipboardRetryDelayMS) * time.Millisecond)
		}
	}
	return false, fmt.Errorf("OpenClipboard failed after %d attempts", maxClipboardRetries)
}

func openClipboardWithRetry() error {
	clipboardMutex.Lock()
	for i := 0; i < maxClipboardRetries; i++ {
		if opened, _, _ := procOpenClipboard.Call(0); opened != 0 {
			return nil
		}
		if i < maxClipboardRetries-1 {
			time.Sleep(time.Duration(clipboardRetryDelayMS) * time.Millisecond)
		}
	}
	clipboardMutex.Unlock()
	return fmt.Errorf("OpenClipboard failed after %d attempts", maxClipboardRetries)
}

func closeClipboardAndUnlock() {
	procCloseClipboard.Call()
	clipboardMutex.Unlock()
}

var clipboardNotifyFunc func()

func clipboardWndProc(hwnd uintptr, message uint32, wParam, lParam uintptr) uintptr {
	switch message {
	case wmDestroy:
		procRemoveClipboardFormatListener.Call(hwnd)
		procDestroyWindow.Call(hwnd)
		return 0
	case wmClipboardUpdate:
		if clipboardNotifyFunc != nil {
			clipboardNotifyFunc()
		}
		return 0
	}
	ret, _, _ := procDefWindowProcW.Call(hwnd, uintptr(message), wParam, lParam)
	return ret
}

func startClipboardListener(notifyFunc func()) error {
	clipboardNotifyFunc = notifyFunc

	className, _ := syscall.UTF16PtrFromString("GoClipboardListener")

	instance, _, _ := procGetModuleHandleW.Call(0)

	class := wndClassEx{
		size:      uint32(unsafe.Sizeof(wndClassEx{})),
		style:     csOwnDC,
		wndProc:   syscall.NewCallback(clipboardWndProc),
		instance:  instance,
		className: uintptr(unsafe.Pointer(className)),
	}

	if _, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&class))); err != nil && err != nil {
		return fmt.Errorf("RegisterClassExW failed: %v", err)
	}

	hwnd, _, err := procCreateWindowExW.Call(
		wsExToolwindow,
		uintptr(unsafe.Pointer(className)),
		0,
		wsOverlapped,
		0, 0, 0, 0,
		0, 0, instance, 0,
	)
	if hwnd == 0 {
		return fmt.Errorf("CreateWindowExW failed: %v", err)
	}

	clipboardWndHwnd = hwnd

	if _, _, err := procAddClipboardFormatListener.Call(hwnd); err != nil && err != nil {
		procDestroyWindow.Call(hwnd)
		clipboardWndHwnd = 0
		return fmt.Errorf("AddClipboardFormatListener failed: %v", err)
	}

	go clipboardMessageLoop()

	return nil
}

func clipboardMessageLoop() {
	var m msg
	for {
		ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(ret) == 0 || int32(ret) == -1 {
			break
		}
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

func stopClipboardListener() {
	if clipboardWndHwnd != 0 {
		procDestroyWindow.Call(clipboardWndHwnd)
		clipboardWndHwnd = 0
	}
}

const (
	smCXScreen        = 0
	smCYScreen        = 1
	smXVirtualScreen  = 76
	smYVirtualScreen  = 77
	smCXVirtualScreen = 78
	smCYVirtualScreen = 79

	srccopy     = 0x00CC0020
	captureBLT  = 0x40000000
	biRGB       = 0
	biBitfields = 3
	dibRGB      = 0

	mouseLeftDown   = 0x0002
	mouseLeftUp     = 0x0004
	mouseRightDown  = 0x0008
	mouseRightUp    = 0x0010
	mouseMiddleDown = 0x0020
	mouseMiddleUp   = 0x0040
	mouseWheel      = 0x0800
	cursorShowing   = 0x00000001
	desktopRead     = 0x00000001

	keyeventfExtendedKey = 0x0001
	keyeventfKeyUp       = 0x0002

	inputMouse    = 0
	inputKeyboard = 1
	inputHardware = 2

	mouseeventfAbsolute   = 0x8000
	mouseeventfMove       = 0x0001
	mouseeventfLeftDown   = 0x0002
	mouseeventfLeftUp     = 0x0004
	mouseeventfRightDown  = 0x0008
	mouseeventfRightUp    = 0x0010
	mouseeventfMiddleDown = 0x0020
	mouseeventfMiddleUp   = 0x0040
	mouseeventfWheel      = 0x0800
	mouseeventfXDown      = 0x0080
	mouseeventfXUp        = 0x0100
	mouseeventfVirtualDesk = 0x4000

	idcArrow    = 32512
	idcIBeam    = 32513
	idcWait     = 32514
	idcCross    = 32515
	idcHand     = 32649
	idcHelp     = 32651
	idcNo       = 32648
	idcSizeAll  = 32646
	idcSizeNS   = 32645
	idcSizeWE   = 32644
	idcSizeNWSE = 32642
	idcSizeNESW = 32643
	idcAppStart = 32650

	cfUnicodeText                        = 13
	cfDIB                                = 8
	cfDIBV5                              = 17
	cfHDROP                              = 15
	cfBitmap                             = 2
	swHide                               = 0
	swShow                               = 5
	gmemMoveable                         = 0x0002
	dpiAwarenessContextPerMonitorAwareV2 = ^uintptr(3)
	maxClipboardImageBytes               = 24 << 20

	vkBack      = 0x08
	vkTab       = 0x09
	vkReturn    = 0x0D
	vkShift     = 0x10
	vkControl   = 0x11
	vkMenu      = 0x12
	vkPause     = 0x13
	vkCapsLock  = 0x14
	vkEscape    = 0x1B
	vkSpace     = 0x20
	vkPageUp    = 0x21
	vkPageDown  = 0x22
	vkEnd       = 0x23
	vkHome      = 0x24
	vkLeft      = 0x25
	vkUp        = 0x26
	vkRight     = 0x27
	vkDown      = 0x28
	vkPrint     = 0x2A
	vkInsert    = 0x2D
	vkDelete    = 0x2E
	vk0         = 0x30
	vkA         = 0x41
	vkLWin      = 0x5B
	vkRWin      = 0x5C
	vkApps      = 0x5D
	vkNumpad0   = 0x60
	vkMultiply  = 0x6A
	vkAdd       = 0x6B
	vkSubtract  = 0x6D
	vkDecimal   = 0x6E
	vkDivide    = 0x6F
	vkF1        = 0x70
	vkF6        = 0x75
	vkNumLock   = 0x90
	vkScroll    = 0x91
	vkLShift    = 0xA0
	vkRShift    = 0xA1
	vkLControl  = 0xA2
	vkRControl  = 0xA3
	vkLMenu     = 0xA4
	vkRMenu     = 0xA5
	vkSemicolon = 0xBA
	vkEqual     = 0xBB
	vkComma     = 0xBC
	vkMinus     = 0xBD
	vkPeriod    = 0xBE
	vkSlash     = 0xBF
	vkBacktick  = 0xC0
	vkLBracket  = 0xDB
	vkBackslash = 0xDC
	vkRBracket  = 0xDD
	vkQuote     = 0xDE

	tilePatchSize     = 96
	maxTilePatchArea  = 65
	maxTileCount      = 80
	rawBGRAMaxPayload = 0

	quicNextProto           = "remote-desktop-quic-v1"
	quicDatagramMagicUplink = 0x52
	quicDatagramHeaderSize  = 13
	quicDatagramChunkSize   = 1120

	d3dDriverTypeHardware        = 1
	d3d11CreateDeviceBGRASupport = 0x20
	d3d11SDKVersion              = 7
	d3d11UsageStaging            = 3
	d3d11CPUAccessRead           = 0x20000
	d3d11MapRead                 = 1
	dxgiFormatB8G8R8A8UNorm      = 87

	dxgiErrorInvalidCall           = 0x887A0001
	dxgiErrorNotFound              = 0x887A0002
	dxgiErrorMoreData              = 0x887A0003
	dxgiErrorUnsupported           = 0x887A0004
	dxgiErrorNotCurrentlyAvailable = 0x887A0022
	dxgiErrorAccessLost            = 0x887A0026
	dxgiErrorWaitTimeout           = 0x887A0027

	idxgiOutputDuplicationAcquireNextFrame   = 8
	idxgiOutputDuplicationGetFrameDirtyRects = 9
	idxgiOutputDuplicationGetFrameMoveRects  = 10
	idxgiOutputDuplicationReleaseFrame       = 14
)

type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

type rgbQuad struct {
	Blue     byte
	Green    byte
	Red      byte
	Reserved byte
}

type bitmapInfo struct {
	Header bitmapInfoHeader
	Colors [1]rgbQuad
}

type winPoint struct {
	X int32
	Y int32
}

type cursorInfo struct {
	Size   uint32
	Flags  uint32
	Cursor uintptr
	Pt     winPoint
}

type dropFiles struct {
	PFiles uint32
	Pt     winPoint
	FNC    int32
	FWide  int32
}

type iconInfo struct {
	FIcon    int32
	XHotspot uint32
	YHotspot uint32
	HbmMask  uintptr
	HbmColor uintptr
}

type monitorInfo struct {
	cbSize    uint32
	rcMonitor winRect
	rcWork    winRect
	dwFlags   uint32
}

type bitmapObject struct {
	Type       int32
	Width      int32
	Height     int32
	WidthBytes int32
	Planes     uint16
	BitsPixel  uint16
	Bits       uintptr
}

type winRect struct {
	Left   int32
	Top    int32
	Right  int32
	Bottom int32
}

type guid struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

type dxgiOutputDesc struct {
	DeviceName         [32]uint16
	DesktopCoordinates winRect
	AttachedToDesktop  int32
	Rotation           uint32
	Monitor            uintptr
}

type dxgiSampleDesc struct {
	Count   uint32
	Quality uint32
}

type d3d11Texture2DDesc struct {
	Width          uint32
	Height         uint32
	MipLevels      uint32
	ArraySize      uint32
	Format         uint32
	SampleDesc     dxgiSampleDesc
	Usage          uint32
	BindFlags      uint32
	CPUAccessFlags uint32
	MiscFlags      uint32
}

type d3d11MappedSubresource struct {
	Data       uintptr
	RowPitch   uint32
	DepthPitch uint32
}

type d3d11Box struct {
	Left   uint32
	Top    uint32
	Front  uint32
	Right  uint32
	Bottom uint32
	Back   uint32
}

type dxgiPoint struct {
	X int32
	Y int32
}

type dxgiRect struct {
	Left   int32
	Top    int32
	Right  int32
	Bottom int32
}

type dxgiMoveRect struct {
	SourcePoint     dxgiPoint
	DestinationRect dxgiRect
}

type dxgiPointerPosition struct {
	Position winPoint
	Visible  int32
}

type dxgiOutduplFrameInfo struct {
	LastPresentTime         int64
	LastMouseUpdateTime     int64
	AccumulatedFrames       uint32
	RectsCoalesced          int32
	ProtectedContentMasked  int32
	PointerPosition         dxgiPointerPosition
	TotalMetadataBufferSize uint32
	PointerShapeBufferSize  uint32
}

type bgraCaptureFrame struct {
	Pixels     []byte
	Metrics    desktopMetrics
	DirtyRects []image.Rectangle
	DirtyKnown bool
	Backend    string
}

var (
	iidIDXGIDevice     = guid{Data1: 0x54ec77fa, Data2: 0x1377, Data3: 0x44e6, Data4: [8]byte{0x8c, 0x32, 0x88, 0xfd, 0x5f, 0x44, 0xc8, 0x4c}}
	iidIDXGIOutput1    = guid{Data1: 0x00cddea8, Data2: 0x939b, Data3: 0x4b83, Data4: [8]byte{0xa3, 0x40, 0xa6, 0x85, 0x22, 0x66, 0x66, 0xcc}}
	iidID3D11Texture2D = guid{Data1: 0x6f15aaf2, Data2: 0xd208, Data3: 0x4e89, Data4: [8]byte{0x9a, 0xb4, 0x48, 0x95, 0x35, 0xd3, 0x4f, 0x9c}}
)

type cursorFingerprint struct {
	Width     int32
	Height    int32
	HotX      uint32
	HotY      uint32
	ColorHash uint64
	MaskHash  uint64
}

var standardCursorKinds = []struct {
	ID   uintptr
	Kind string
}{
	{ID: idcArrow, Kind: "arrow"},
	{ID: idcIBeam, Kind: "text"},
	{ID: idcWait, Kind: "wait"},
	{ID: idcAppStart, Kind: "wait"},
	{ID: idcHand, Kind: "hand"},
	{ID: idcCross, Kind: "cross"},
	{ID: idcSizeWE, Kind: "size-we"},
	{ID: idcSizeNS, Kind: "size-ns"},
	{ID: idcSizeNWSE, Kind: "size-nwse"},
	{ID: idcSizeNESW, Kind: "size-nesw"},
	{ID: idcSizeAll, Kind: "size-all"},
	{ID: idcNo, Kind: "forbidden"},
	{ID: idcHelp, Kind: "help"},
}

var (
	cursorHandleKindCache sync.Map

	standardCursorFingerprintOnce   sync.Once
	standardCursorFingerprintExact  map[string]string
	standardCursorFingerprintBitmap map[string]string
)

type inputEvent struct {
	Seq       int64           `json:"seq,omitempty"`
	DeviceID  string          `json:"deviceId"`
	Type      string          `json:"type"`
	X         int             `json:"x,omitempty"`
	Y         int             `json:"y,omitempty"`
	Button    string          `json:"button,omitempty"`
	Delta     int             `json:"delta,omitempty"`
	Key       string          `json:"key,omitempty"`
	Code      string          `json:"code,omitempty"`
	Text      string          `json:"text,omitempty"`
	VK        int             `json:"vk,omitempty"`
	Extended  bool            `json:"extended,omitempty"`
	Ctrl      bool            `json:"ctrl,omitempty"`
	Alt       bool            `json:"alt,omitempty"`
	Shift     bool            `json:"shift,omitempty"`
	Meta      bool            `json:"meta,omitempty"`
	Repeat    bool            `json:"repeat,omitempty"`
	Clipboard *clipboardState `json:"clipboard,omitempty"`
}

type transferFileRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Size int64  `json:"size"`
}

type transferCommandPayload struct {
	Kind   string            `json:"kind"`
	Target string            `json:"target,omitempty"`
	Files  []transferFileRef `json:"files"`
}

type remoteFileRequest struct {
	ID   string `json:"id"`
	Path string `json:"path,omitempty"`
}

type remoteFileEntry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	IsDir   bool   `json:"isDir"`
	Size    int64  `json:"size"`
	ModTime string `json:"modTime,omitempty"`
}

type remoteFileResult struct {
	ID        string            `json:"id"`
	DeviceID  string            `json:"deviceId"`
	Path      string            `json:"path,omitempty"`
	Parent    string            `json:"parent,omitempty"`
	Entries   []remoteFileEntry `json:"entries,omitempty"`
	File      *transferFileRef  `json:"file,omitempty"`
	Error     string            `json:"error,omitempty"`
	UpdatedAt string            `json:"updatedAt,omitempty"`
}

type remoteCommandRequest struct {
	ID         string `json:"id"`
	Kind       string `json:"kind,omitempty"`
	Command    string `json:"command"`
	Dir        string `json:"dir,omitempty"`
	TimeoutSec int    `json:"timeoutSec"`
}

type remoteCommandResult struct {
	ID         string `json:"id"`
	DeviceID   string `json:"deviceId"`
	Command    string `json:"command,omitempty"`
	Dir        string `json:"dir,omitempty"`
	Output     string `json:"output,omitempty"`
	Error      string `json:"error,omitempty"`
	ExitCode   int    `json:"exitCode,omitempty"`
	TimedOut   bool   `json:"timedOut,omitempty"`
	StartedAt  string `json:"startedAt,omitempty"`
	FinishedAt string `json:"finishedAt,omitempty"`
	UpdatedAt  string `json:"updatedAt,omitempty"`
}

type transferCreateRequest struct {
	DeviceID  string `json:"deviceId"`
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	Direction string `json:"direction"`
}

type transferProgressRequest struct {
	ID             string `json:"id"`
	DeliveredBytes int64  `json:"deliveredBytes"`
	Status         string `json:"status"`
	Error          string `json:"error,omitempty"`
}

type registerRequest struct {
	DeviceID         string `json:"deviceId"`
	Name             string `json:"name"`
	OriginX          int    `json:"originX"`
	OriginY          int    `json:"originY"`
	Width            int    `json:"width"`
	Height           int    `json:"height"`
	CursorX          int    `json:"cursorX"`
	CursorY          int    `json:"cursorY"`
	CursorOn         bool   `json:"cursorVisible"`
	CursorKind       string `json:"cursorKind"`
	CursorImage      []byte `json:"cursorImage,omitempty"`
	CursorImageClear bool   `json:"cursorImageClear,omitempty"`
	CursorImageW     int    `json:"cursorImageW,omitempty"`
	CursorImageH     int    `json:"cursorImageH,omitempty"`
	CursorHotX       int    `json:"cursorHotX,omitempty"`
	CursorHotY       int    `json:"cursorHotY,omitempty"`
}

type commandResponse struct {
	LastSeq          int64        `json:"lastSeq"`
	Commands         []inputEvent `json:"commands"`
	ViewerActive     bool         `json:"viewerActive"`
	ViewerKind       string       `json:"viewerKind,omitempty"`
	SuggestedMode    string       `json:"suggestedMode,omitempty"`
	SuggestedCodec   string       `json:"suggestedCodec,omitempty"`
	SuggestedFPS     int          `json:"suggestedFps,omitempty"`
	SuggestedQuality int          `json:"suggestedQuality,omitempty"`
	SuggestedFullMS  int          `json:"suggestedFullRefreshMs,omitempty"`
	ClipboardSync    *bool        `json:"clipboardSync,omitempty"`
}

type mfProbeResult struct {
	OK          bool   `json:"ok"`
	ArchBits    int    `json:"archBits"`
	MFPlat      bool   `json:"mfplat"`
	MFReadWrite bool   `json:"mfreadwrite"`
	MFH264Enc   bool   `json:"mfh264enc"`
	MFDecoder   bool   `json:"mfdecoder"`
	StartupHR   int    `json:"startupHr"`
	Message     string `json:"message"`
}

type mfSessionResponse struct {
	OK           bool     `json:"ok"`
	Type         string   `json:"type"`
	Role         string   `json:"role,omitempty"`
	Version      string   `json:"version,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
	Encode       []string `json:"encode,omitempty"`
	Decode       []string `json:"decode,omitempty"`
	Message      string   `json:"message,omitempty"`
	Error        string   `json:"error,omitempty"`
	Codec        string   `json:"codec,omitempty"`
	Format       string   `json:"format,omitempty"`
	InputPath    string   `json:"inputPath,omitempty"`
	OutputPath   string   `json:"outputPath,omitempty"`
	Width        int      `json:"width,omitempty"`
	Height       int      `json:"height,omitempty"`
	Stride       int      `json:"stride,omitempty"`
	PayloadSize  int      `json:"payloadSize,omitempty"`
}

type mfSessionRequest struct {
	Type        string `json:"type"`
	Role        string `json:"role,omitempty"`
	Codec       string `json:"codec,omitempty"`
	Format      string `json:"format,omitempty"`
	Width       int    `json:"width,omitempty"`
	Height      int    `json:"height,omitempty"`
	Stride      int    `json:"stride,omitempty"`
	Quality     int    `json:"quality,omitempty"`
	PayloadSize int    `json:"payloadSize,omitempty"`
}

type clipboardState struct {
	DeviceID  string            `json:"deviceId"`
	Kind      string            `json:"kind,omitempty"`
	Text      string            `json:"text,omitempty"`
	Image     []byte            `json:"image,omitempty"`
	ImageType string            `json:"imageType,omitempty"`
	ImageW    int               `json:"imageW,omitempty"`
	ImageH    int               `json:"imageH,omitempty"`
	Files     []transferFileRef `json:"files,omitempty"`
	Paths     []string          `json:"-"`
	Version   int64             `json:"version"`
	UpdatedAt string            `json:"updatedAt"`
	Sync      *bool             `json:"sync,omitempty"`
}

type transferSnapshot struct {
	ID             string `json:"id"`
	DeviceID       string `json:"deviceId"`
	Name           string `json:"name"`
	Direction      string `json:"direction"`
	Size           int64  `json:"size"`
	UploadedBytes  int64  `json:"uploadedBytes"`
	DeliveredBytes int64  `json:"deliveredBytes"`
	Status         string `json:"status"`
	Error          string `json:"error,omitempty"`
	UpdatedAt      string `json:"updatedAt"`
}

type agentPollRequest struct {
	DeviceID         string `json:"deviceId,omitempty"`
	After            int64  `json:"after"`
	CursorX          int    `json:"cursorX"`
	CursorY          int    `json:"cursorY"`
	CursorVisible    bool   `json:"cursorVisible"`
	CursorKind       string `json:"cursorKind"`
	CursorImage      []byte `json:"cursorImage,omitempty"`
	CursorImageClear bool   `json:"cursorImageClear,omitempty"`
	CursorImageW     int    `json:"cursorImageW,omitempty"`
	CursorImageH     int    `json:"cursorImageH,omitempty"`
	CursorHotX       int    `json:"cursorHotX,omitempty"`
	CursorHotY       int    `json:"cursorHotY,omitempty"`
}

type agentStreamMessage struct {
	Type      string            `json:"type"`
	DeviceID  string            `json:"deviceId,omitempty"`
	Role      string            `json:"role,omitempty"`
	Register  *registerRequest  `json:"register,omitempty"`
	Clipboard *clipboardState   `json:"clipboard,omitempty"`
	Poll      *agentPollRequest `json:"poll,omitempty"`
	Commands  *commandResponse  `json:"commands,omitempty"`
	Error     string            `json:"error,omitempty"`
}

type agentFrameEnvelope struct {
	Type        string      `json:"type"`
	DeviceID    string      `json:"deviceId"`
	Mode        string      `json:"mode"`
	Status      string      `json:"status,omitempty"`
	Message     string      `json:"message,omitempty"`
	Codec       string      `json:"codec,omitempty"`
	OriginX     int         `json:"originX"`
	OriginY     int         `json:"originY"`
	Width       int         `json:"width"`
	Height      int         `json:"height"`
	Quality     int         `json:"quality"`
	PatchX      int         `json:"patchX,omitempty"`
	PatchY      int         `json:"patchY,omitempty"`
	PatchW      int         `json:"patchW,omitempty"`
	PatchH      int         `json:"patchH,omitempty"`
	Tiles       []tilePatch `json:"tiles,omitempty"`
	PayloadSize int         `json:"payloadSize"`
}

type wsClientMessage struct {
	Type     string `json:"type"`
	DeviceID string `json:"deviceId,omitempty"`
	Role     string `json:"role,omitempty"`
}

type clientIdentity struct {
	DeviceID string `json:"deviceId"`
	Name     string `json:"name"`
}

type desktopMetrics struct {
	Left   int
	Top    int
	Width  int
	Height int
}

type tilePatch struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	W      int `json:"w"`
	H      int `json:"h"`
	AtlasX int `json:"atlasX"`
	AtlasY int `json:"atlasY"`
}

type cursorSnapshot struct {
	X          int
	Y          int
	Visible    bool
	Kind       string
	Image      []byte
	ImageClear bool
	ImageW     int
	ImageH     int
	HotX       int
	HotY       int
}

type keySpec struct {
	VK       byte
	Extended bool
}

type mouseInput struct {
	DX          int32
	DY          int32
	MouseData   uint32
	DwFlags     uint32
	Time        uint32
	_pad1       uint32
	DwExtraInfo uintptr
}

type input struct {
	Type uint32
	_pad uint32
	MI   mouseInput
}

type inputExecutor struct {
	pressed  map[byte]bool
	originX  int
	originY  int
	desktopW int
	desktopH int
	frameW   int
	frameH   int
}

type agent struct {
	baseURL            string
	deviceID           string
	name               string
	transport          string
	activeTransport    string
	quality            int
	httpClient         *http.Client
	input              *inputExecutor
	originX            int
	originY            int
	lastSeq            int64
	lastShot           time.Time
	lastFullShot       time.Time
	lastImage          *image.RGBA
	lastClipboard      string
	lastClipboardSeq   uint32
	clipboardSync      bool
	clipboardChanged   chan struct{}
	lastErrorLog       time.Time
	viewerActive       bool
	viewerKind         string
	streamMode         string
	streamCodec        string
	streamFPS          int
	streamQuality      int
	streamFullMS       int
	pollInterval       time.Duration
	streamTick         time.Duration
	idleShotTick       time.Duration
	clipboardTick      time.Duration
	inputDelay         time.Duration
	patchBoost         int
	tileBoost          int
	modeOverride       string
	codecOverride      string
	quicConn           *quic.Conn
	quicStream         *quic.Stream
	quicDecoder        *json.Decoder
	quicUploadID       int64
	wsFrameConn        *websocket.Conn
	wsFrameWriteMu     sync.Mutex
	mfHelperPath       string
	mfSession          *mfHelperSession
	lastCodecLog       time.Time
	txStatAt           time.Time
	txStatBytes        int64
	txStatFrames       int
	txStatLast         string
	txStatCodec        string
	txFrameW           int
	txFrameH           int
	txFPS              float64
	txBPS              float64
	txAvgBytes         float64
	lastProfileLog     string
	lastH264At         time.Time
	videoPatchReady    bool
	captureMaxWidth    int
	captureMaxHeight   int
	captureBackend     string
	captureSession     *screenCaptureSession
	dxgiCapture        *dxgiCaptureSession
	dxgiWarned         bool
	dxgiRetryAt        time.Time
	lastFrameBGRA      []byte
	lastFrameMetrics   desktopMetrics
	cursorImageHash    uint64
	cursorImageValid   bool
	monitors           []desktopMetrics
	activeMonitor      int
	monitorsMu         sync.Mutex
	lastVirtualW       int // 缓存上次虚拟屏幕宽度，用于检测分辨率变化
	lastVirtualH       int // 缓存上次虚拟屏幕高度，用于检测分辨率变化
	captureMonitorGen   atomic.Int64 // 捕获时的显示器代数，switchMonitor 会递增
	transferMu         sync.Mutex
	activeTransfers    map[string]struct{}
	remoteFileMu       sync.Mutex
	remoteFileDir      string
	remoteCommandMu    sync.Mutex
	remoteCommandDir   string
	desktopMu          sync.Mutex
	desktopUnavailable bool
	captureFailures    int
	lastDesktopStatus  string
	lastDesktopMessage string
}

type screenCaptureSession struct {
	metrics  desktopMetrics
	screenDC uintptr
	memDC    uintptr
	bitmap   uintptr
	oldObj   uintptr
	dibBits  uintptr
	pixels   []byte
}

type dxgiCaptureSession struct {
	metrics desktopMetrics
	device  uintptr
	context uintptr
	outputs []dxgiOutputCapture
	pixels  []byte
}

type dxgiOutputCapture struct {
	duplication uintptr
	staging     uintptr
	rect        image.Rectangle
	haveFrame   bool
	metadata    []byte
}

type mfHelperSession struct {
	path   string
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	stderr *bytes.Buffer
	mu     sync.Mutex
}

func main() {
	enableDPIAwareness()
	log.SetOutput(io.Discard) // 关闭日志，避免控制台点击卡顿
	serverURL := flag.String("server", "http://127.0.0.1:19998", "server base url")
	deviceID := flag.String("device", "", "device id")
	deviceName := flag.String("name", "", "device display name")
	transport := flag.String("transport", "auto", "transport: auto, quic, http")
	streamMode := flag.String("mode", "auto", "stream mode: auto, video, delta")
	streamCodec := flag.String("codec", "auto", "video codec: auto, jpeg")
	quality := flag.Int("quality", 60, "jpeg quality, 1-100")
	captureWidth := flag.Int("capture-width", 0, "maximum capture width before upload, 0 = native")
	captureHeight := flag.Int("capture-height", 0, "maximum capture height before upload, 0 = native")
	captureBackend := flag.String("capture-backend", "gdi", "screen capture backend: auto, dxgi, gdi")
	pollMS := flag.Int("poll-ms", 25, "command poll interval in ms")
	streamMS := flag.Int("stream-ms", 8, "stream tick interval in ms")
	idleMS := flag.Int("idle-ms", 6000, "idle refresh interval in ms")
	clipboardMS := flag.Int("clipboard-ms", 1200, "clipboard sync interval in ms")
	inputDelayMS := flag.Int("input-delay-ms", 8, "delay after input before capture in ms")
	patchBoost := flag.Int("patch-boost", 8, "extra jpeg quality for patch uploads")
	tileBoost := flag.Int("tile-boost", 6, "extra jpeg quality for tile uploads")
	mfHelper := flag.String("mf-helper", "off", "path to mf_helper.exe, 'auto' = use embedded helper")
	flag.Parse()

	identity, err := loadOrCreateIdentity(strings.TrimSpace(*deviceID), strings.TrimSpace(*deviceName))
	if err != nil {
		log.Printf("load identity failed, fallback identity will be used: %v", err)
		identity = clientIdentity{
			DeviceID: fallbackDeviceID(),
			Name:     fallbackDeviceName(),
		}
	}

	a := &agent{
		baseURL:          strings.TrimRight(*serverURL, "/"),
		deviceID:         identity.DeviceID,
		name:             identity.Name,
		transport:        normalizeTransport(*transport),
		activeTransport:  "http",
		quality:          clamp(*quality, 20, 100),
		httpClient:       newLowLatencyHTTPClient(90 * time.Second),
		input:            newInputExecutor(),
		pollInterval:     time.Duration(clamp(*pollMS, 20, 5000)) * time.Millisecond,
		streamTick:       time.Duration(clamp(*streamMS, 10, 2000)) * time.Millisecond,
		idleShotTick:     time.Duration(clamp(*idleMS, 500, 20000)) * time.Millisecond,
		clipboardTick:    time.Duration(clamp(*clipboardMS, 200, 10000)) * time.Millisecond,
		clipboardSync:    true,
		clipboardChanged: make(chan struct{}, 1),
		inputDelay:       time.Duration(clamp(*inputDelayMS, 0, 2000)) * time.Millisecond,
		patchBoost:       clamp(*patchBoost, 0, 30),
		tileBoost:        clamp(*tileBoost, 0, 30),
		modeOverride:     normalizeStreamMode(*streamMode, "auto"),
		codecOverride:    normalizeFrameCodec(*streamCodec, "auto"),
		captureMaxWidth:  max(0, *captureWidth),
		captureMaxHeight: max(0, *captureHeight),
		captureBackend:   normalizeCaptureBackend(*captureBackend),
		activeTransfers:  make(map[string]struct{}),
	}

	switch strings.TrimSpace(strings.ToLower(*mfHelper)) {
	case "auto":
		a.mfHelperPath = "mf_helper.exe"
	case "off", "":
		// disabled
	default:
		a.mfHelperPath = *mfHelper
	}

	log.Printf("[agent][start] device=%s name=%s server=%s transport=%s mode=%s codec=%s capture=%s mf-helper=%s fallback=off desktop-status=on", a.deviceID, a.name, a.baseURL, a.transport, a.modeOverride, a.codecOverride, a.captureLabel(), mfHelperLabel(a.mfHelperPath))
	backoff := time.Second
	for {
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					log.Printf("[agent][panic] recovered: %v", recovered)
				}
			}()
			if err := a.run(); err != nil {
				log.Printf("[agent][loop] ended: %v", err)
			}
		}()
		log.Printf("[agent][reconnect] in %s", backoff)
		time.Sleep(backoff)
		if backoff < 15*time.Second {
			backoff *= 2
			if backoff > 15*time.Second {
				backoff = 15 * time.Second
			}
		}
	}
}

func (a *agent) run() error {
	if err := a.prepareTransport(); err != nil {
		return err
	}
	defer a.closeQUIC()
	defer a.closeWSUpload()
	defer stopClipboardListener()

	if err := startClipboardListener(func() {
		select {
		case a.clipboardChanged <- struct{}{}:
		default:
		}
	}); err != nil {
		log.Printf("[agent][clipboard] listener start failed: %v", err)
	}

	return a.runLoop()
}

func (a *agent) prepareTransport() error {
	a.activeTransport = "http"
	a.quicUploadID = 0
	a.closeWSUpload()
	switch a.transport {
	case "http":
		return nil
	case "quic":
		if err := a.connectQUIC(); err != nil {
			return err
		}
		a.activeTransport = "quic"
		log.Printf("[agent][stream] connected via QUIC")
		return nil
	default:
		if err := a.connectQUIC(); err != nil {
			log.Printf("[agent][quic] unavailable endpoint=%s, fallback to http/ws: %v (hint: open UDP port on server/security group/firewall)", a.quicEndpoint(), err)
			if wsErr := a.connectWSUpload(); wsErr == nil {
				a.activeTransport = "ws"
				log.Printf("[agent][stream] frame uplink connected via WS")
			} else {
				log.Printf("[agent][ws] frame uplink unavailable, fallback to plain http: %v", wsErr)
			}
			return nil
		}
		a.activeTransport = "quic"
		log.Printf("[agent][stream] connected via QUIC")
		return nil
	}
}

func (a *agent) runLoop() error {
	a.lastImage = nil
	a.lastShot = time.Time{}
	a.lastFullShot = time.Time{}
	a.videoPatchReady = false
	nativeMetrics := a.getActiveMonitor()
	metrics := a.captureMetrics(nativeMetrics)
	a.originX = nativeMetrics.Left
	a.originY = nativeMetrics.Top
	a.input.setDesktop(a.originX, a.originY, nativeMetrics.Width, nativeMetrics.Height, metrics.Width, metrics.Height)
	if err := a.register(metrics, nativeMetrics); err != nil {
		return err
	}
	if metrics.Width != nativeMetrics.Width || metrics.Height != nativeMetrics.Height {
		log.Printf("[agent][register] ok device=%s desktop=%dx%d origin=(%d,%d) native=%dx%d", a.deviceID, metrics.Width, metrics.Height, nativeMetrics.Left, nativeMetrics.Top, nativeMetrics.Width, nativeMetrics.Height)
	} else {
		log.Printf("[agent][register] ok device=%s desktop=%dx%d origin=(%d,%d)", a.deviceID, metrics.Width, metrics.Height, nativeMetrics.Left, nativeMetrics.Top)
	}
	title := fmt.Sprintf("[%s] 已连接 %dx%d %s", a.name, metrics.Width, metrics.Height, buildTime)
	procSetConsoleTitleW.Call(uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr(title))))
	if err := a.captureAndUpload(true); err != nil {
		if !a.noteCaptureFailure(err) {
			log.Printf("[agent][capture] initial failed: %v", err)
		}
	} else {
		a.noteCaptureSuccess()
	}

	var captureMu sync.Mutex
	capturePending := false
	captureForceFull := false
	captureReason := ""
	captureNotify := make(chan struct{}, 1)
	scheduleCapture := func(forceFull bool, reason string) {
		if a.isDesktopUnavailable() && reason != "desktop-probe" {
			return
		}
		captureMu.Lock()
		if forceFull {
			captureForceFull = true
		}
		if reason != "" {
			captureReason = reason
		}
		if !capturePending {
			capturePending = true
			select {
			case captureNotify <- struct{}{}:
			default:
			}
		}
		captureMu.Unlock()
	}
	go func() {
		for range captureNotify {
			captureMu.Lock()
			forceFull := captureForceFull
			reason := captureReason
			capturePending = false
			captureForceFull = false
			captureReason = ""
			captureMu.Unlock()

			if a.isDesktopUnavailable() && reason != "desktop-probe" {
				continue
			}
			if err := a.captureAndUpload(forceFull); err != nil {
				if strings.TrimSpace(reason) == "" {
					reason = "async"
				}
				if a.noteCaptureFailure(err) {
					continue
				}
				a.logErrorf("%s capture failed: %v", reason, err)
			} else {
				a.noteCaptureSuccess()
			}

			captureMu.Lock()
			if capturePending {
				select {
				case captureNotify <- struct{}{}:
				default:
				}
			}
			captureMu.Unlock()
		}
	}()

	if clipboard, err := readClipboardState(); err == nil {
		a.lastClipboard = clipboardSignature(clipboard)
		a.lastClipboardSeq = clipboardSequenceNumber()
	}

	heartbeatTicker := time.NewTicker(5 * time.Second)
	defer heartbeatTicker.Stop()
	desktopProbeTicker := time.NewTicker(2 * time.Second)
	defer desktopProbeTicker.Stop()

	pollTicker := time.NewTicker(a.pollInterval)
	defer pollTicker.Stop()

	streamTicker := time.NewTicker(a.streamTick)
	defer streamTicker.Stop()

	idleShotTicker := time.NewTicker(a.idleShotTick)
	defer idleShotTicker.Stop()

	clipboardTicker := time.NewTicker(a.clipboardTick)
	defer clipboardTicker.Stop()

	for {
		select {
		case <-heartbeatTicker.C:
			nativeMetrics = a.getActiveMonitor()
			metrics = a.captureMetrics(nativeMetrics)
			a.originX = nativeMetrics.Left
			a.originY = nativeMetrics.Top
			a.input.setDesktop(a.originX, a.originY, nativeMetrics.Width, nativeMetrics.Height, metrics.Width, metrics.Height)
			if err := a.register(metrics, nativeMetrics); err != nil {
				a.logErrorf("register failed: %v", err)
			}
		case <-pollTicker.C:
			resp, err := a.fetchCommands()
			if err != nil {
				a.viewerActive = false
				a.viewerKind = ""
				a.streamFPS = 0
				a.streamQuality = 0
				a.streamFullMS = 0
				a.streamCodec = ""
				a.videoPatchReady = false
				a.logErrorf("fetch commands failed: %v", err)
				continue
			}
			a.viewerActive = resp.ViewerActive
			a.viewerKind = strings.TrimSpace(resp.ViewerKind)
			a.streamMode = normalizeStreamMode(resp.SuggestedMode, "")
			a.streamCodec = normalizeFrameCodec(resp.SuggestedCodec, "")
			if a.viewerKind == "controller" && a.streamCodec == "h264-mf" {
				a.lastH264At = time.Now()
			}
			if !a.viewerActive || a.viewerKind != "controller" {
				a.lastH264At = time.Time{}
				a.videoPatchReady = false
			}
			a.streamFPS = resp.SuggestedFPS
			a.streamQuality = resp.SuggestedQuality
			a.streamFullMS = resp.SuggestedFullMS
			if resp.ClipboardSync != nil {
				a.clipboardSync = *resp.ClipboardSync
			}
			a.logProfileDecision()
			if len(resp.Commands) == 0 {
				a.lastSeq = resp.LastSeq
				continue
			}

			needRefresh := false
			for _, cmd := range resp.Commands {
				if cmd.Type == "clipboard_set" {
					if !a.clipboardSync {
						continue
					}
					clipboard := clipboardState{Kind: "text", Text: cmd.Text}
					if cmd.Clipboard != nil {
						clipboard = *cmd.Clipboard
					}
					if err := a.writeClipboardState(clipboard); err != nil {
						log.Printf("apply clipboard command %d failed: %v", cmd.Seq, err)
					} else {
						if localClipboard, err := readClipboardState(); err == nil {
							a.lastClipboard = clipboardSignature(localClipboard)
						} else {
							a.lastClipboard = clipboardSignature(clipboard)
						}
						a.lastClipboardSeq = clipboardSequenceNumber()
					}
					continue
				}
				if cmd.Type == "transfer_files" {
					var payload transferCommandPayload
					if err := json.Unmarshal([]byte(cmd.Text), &payload); err != nil {
						log.Printf("apply transfer command %d failed: %v", cmd.Seq, err)
						continue
					}
					go a.receiveTransferFiles(payload)
					continue
				}
				if cmd.Type == "file_list" {
					go a.handleRemoteFileList(cmd.Text)
					continue
				}
				if cmd.Type == "file_download" {
					go a.handleRemoteFileDownload(cmd.Text)
					continue
				}
				if cmd.Type == "remote_command" {
					go a.handleRemoteCommand(cmd.Text)
					continue
				}
				if cmd.Type == "monitor_switch" {
					go a.switchMonitor()
					continue
				}

				if err := a.input.apply(cmd); err != nil {
					log.Printf("apply command %d (%s) failed: %v", cmd.Seq, cmd.Type, err)
					continue
				}
				if shouldRefresh(cmd.Type) {
					needRefresh = true
				}
			}
			a.lastSeq = resp.LastSeq

			if needRefresh {
				forceFull := a.shouldForceFullCapture(time.Now())
				time.AfterFunc(a.inputDelay, func() {
					scheduleCapture(forceFull, "input")
				})
			}
		case <-streamTicker.C:
			interval, active := a.streamInterval()
			if !active || time.Since(a.lastShot) < interval {
				continue
			}
			forceFull := a.shouldForceFullCapture(time.Now())
			scheduleCapture(forceFull, "stream")
		case <-idleShotTicker.C:
			if a.viewerActive {
				continue
			}
			if time.Since(a.lastShot) > 6*time.Second {
				forceFull := a.shouldForceFullCapture(time.Now())
				scheduleCapture(forceFull, "idle")
			}
		case <-desktopProbeTicker.C:
			if a.isDesktopUnavailable() {
				scheduleCapture(true, "desktop-probe")
			}
		case <-clipboardTicker.C:
			a.syncAgentClipboard()
		case <-a.clipboardChanged:
			a.syncAgentClipboard()
		}
	}
}

func (a *agent) syncAgentClipboard() {
	if !a.clipboardSync {
		return
	}
	seq := clipboardSequenceNumber()
	if seq != 0 && seq == a.lastClipboardSeq {
		return
	}
	clipboard, err := readClipboardState()
	if err != nil {
		a.logErrorf("read clipboard failed: %v", err)
		return
	}
	signature := clipboardSignature(clipboard)
	if signature != a.lastClipboard {
		if clipboard.Kind != "text" && clipboard.Kind != "image" {
			a.lastClipboardSeq = seq
			return
		}
		postState, err := a.prepareClipboardForPost(clipboard)
		if err != nil {
			a.logErrorf("clipboard prepare failed: %v", err)
			return
		}
		if err := a.postClipboard(postState); err != nil {
			a.logErrorf("clipboard sync failed: %v", err)
			return
		}
		a.lastClipboard = signature
		a.lastClipboardSeq = seq
		return
	}
	a.lastClipboardSeq = seq
}

func newInputExecutor() *inputExecutor {
	return &inputExecutor{
		pressed: make(map[byte]bool),
	}
}

func (a *agent) isDesktopUnavailable() bool {
	if a == nil {
		return false
	}
	a.desktopMu.Lock()
	defer a.desktopMu.Unlock()
	return a.desktopUnavailable
}

func (a *agent) noteCaptureSuccess() {
	if a == nil {
		return
	}
	a.desktopMu.Lock()
	wasUnavailable := a.desktopUnavailable
	statusChanged := wasUnavailable || a.lastDesktopStatus != "available"
	a.desktopUnavailable = false
	a.captureFailures = 0
	a.lastDesktopStatus = "available"
	a.lastDesktopMessage = ""
	a.desktopMu.Unlock()
	if statusChanged {
		if err := a.postDesktopStatus(true, ""); err != nil {
			a.logErrorf("desktop status sync failed: %v", err)
		}
		if wasUnavailable {
			log.Printf("[agent][capture] desktop recovered")
		}
	}
}

func (a *agent) noteCaptureFailure(err error) bool {
	if a == nil || err == nil {
		return false
	}
	reason := strings.TrimSpace(err.Error())
	if reason == "" {
		reason = "capture failed"
	}
	desktopErr := errors.Is(err, errDesktopUnavailable)
	a.desktopMu.Lock()
	a.captureFailures++
	shouldMarkUnavailable := desktopErr || a.captureFailures >= 3
	statusChanged := shouldMarkUnavailable && (!a.desktopUnavailable || a.lastDesktopStatus != "unavailable" || a.lastDesktopMessage != reason)
	if shouldMarkUnavailable {
		a.desktopUnavailable = true
		a.lastDesktopStatus = "unavailable"
		a.lastDesktopMessage = reason
	}
	a.desktopMu.Unlock()
	if !shouldMarkUnavailable {
		return false
	}
	a.clearCaptureBases()
	if statusChanged {
		log.Printf("[agent][capture] desktop unavailable: %s", reason)
		if err := a.postDesktopStatus(false, reason); err != nil {
			a.logErrorf("desktop status sync failed: %v", err)
		}
	}
	return true
}

func (a *agent) clearCaptureBases() {
	if a == nil {
		return
	}
	a.lastImage = nil
	a.lastFrameBGRA = nil
	a.lastFrameMetrics = desktopMetrics{}
	a.lastShot = time.Time{}
	a.lastFullShot = time.Time{}
	a.videoPatchReady = false
	if a.captureSession != nil {
		a.captureSession.close()
	}
	if a.dxgiCapture != nil {
		a.dxgiCapture.close()
		a.dxgiCapture = nil
	}
}

func (a *agent) postDesktopStatus(available bool, reason string) error {
	if a == nil {
		return nil
	}
	status := "available"
	if !available {
		status = "unavailable"
	}
	reason = strings.TrimSpace(reason)
	header := agentFrameEnvelope{
		Type:     "desktop_status",
		DeviceID: a.deviceID,
		Status:   status,
		Message:  reason,
	}
	if a.usingQUIC() {
		return a.uploadFrameQUIC(header, nil)
	}
	if a.usingWSUpload() {
		return a.uploadFrameWS(header, nil)
	}
	body, err := json.Marshal(header)
	if err != nil {
		return err
	}
	resp, err := a.httpClient.Post(a.baseURL+"/api/desktop-status", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("desktop status failed: %s", strings.TrimSpace(string(raw)))
	}
	return nil
}

func (i *inputExecutor) setDesktop(originX, originY, desktopW, desktopH, frameW, frameH int) {
	i.originX = originX
	i.originY = originY
	i.desktopW = desktopW
	i.desktopH = desktopH
	i.frameW = frameW
	i.frameH = frameH
}

func (i *inputExecutor) setOrigin(x, y int) {
	i.originX = x
	i.originY = y
}

func (i *inputExecutor) apply(cmd inputEvent) error {
	x, y := i.frameToDesktop(cmd.X, cmd.Y)
	switch cmd.Type {
	case "move":
		return mouseMove(x+i.originX, y+i.originY)
	case "down":
		return mouseButton(x+i.originX, y+i.originY, cmd.Button, true)
	case "up":
		return mouseButton(x+i.originX, y+i.originY, cmd.Button, false)
	case "wheel":
		return mouseWheelEvent(x+i.originX, y+i.originY, cmd.Delta)
	case "keydown":
		return i.key(cmd, true)
	case "keyup":
		return i.key(cmd, false)
	case "reset_keys":
		return i.releaseAll()
	case "refresh":
		return nil
	default:
		return fmt.Errorf("unsupported input type: %s", cmd.Type)
	}
}

func (i *inputExecutor) frameToDesktop(x, y int) (int, int) {
	if i == nil {
		return x, y
	}
	return scalePointBetweenSizes(x, y, i.frameW, i.frameH, i.desktopW, i.desktopH)
}

func (i *inputExecutor) key(cmd inputEvent, down bool) error {
	spec := keySpec{}
	if cmd.VK > 0 {
		spec = keySpec{VK: byte(cmd.VK), Extended: cmd.Extended}
	} else {
		var err error
		spec, err = resolveKey(cmd.Key, cmd.Code)
		if err != nil {
			return err
		}
	}

	if down {
		if i.pressed[spec.VK] && !cmd.Repeat {
			return nil
		}
		return i.send(spec, false)
	}

	if !i.pressed[spec.VK] {
		return nil
	}
	return i.send(spec, true)
}

func (i *inputExecutor) send(spec keySpec, keyUp bool) error {
	scan, _, _ := procMapVirtualKey.Call(uintptr(spec.VK), 0)
	flags := uintptr(0)
	if spec.Extended {
		flags |= keyeventfExtendedKey
	}
	if keyUp {
		flags |= keyeventfKeyUp
	}

	procKeybdEvent.Call(uintptr(spec.VK), scan, flags, 0)
	if keyUp {
		delete(i.pressed, spec.VK)
	} else {
		i.pressed[spec.VK] = true
	}
	return nil
}

func (i *inputExecutor) releaseAll() error {
	for vk := range i.pressed {
		_ = i.send(keySpec{VK: vk, Extended: isExtendedVK(vk)}, true)
	}
	i.pressed = make(map[byte]bool)
	return nil
}

func (a *agent) register(metrics, nativeMetrics desktopMetrics) error {
	cursor := a.currentCursorState(metrics, nativeMetrics, true)
	req := registerRequest{
		DeviceID:   a.deviceID,
		Name:       a.name,
		OriginX:    metrics.Left,
		OriginY:    metrics.Top,
		Width:      metrics.Width,
		Height:     metrics.Height,
		CursorX:    cursor.X,
		CursorY:    cursor.Y,
		CursorOn:   cursor.Visible,
		CursorKind: cursor.Kind,
	}
	if a.usingQUIC() {
		return a.sendAgentStream(agentStreamMessage{
			Type:     "register",
			DeviceID: a.deviceID,
			Register: &req,
		})
	}

	body, err := json.Marshal(req)
	if err != nil {
		return err
	}

	resp, err := a.httpClient.Post(a.baseURL+"/api/register", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("register failed: %s", strings.TrimSpace(string(raw)))
	}
	return nil
}

func (a *agent) postClipboard(payload clipboardState) error {
	if a == nil || !a.clipboardSync {
		return nil
	}
	payload = normalizeClipboardState(payload)
	payload.DeviceID = a.deviceID
	if a.usingQUIC() {
		return a.sendAgentStream(agentStreamMessage{
			Type:      "clipboard",
			DeviceID:  a.deviceID,
			Clipboard: &payload,
		})
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	resp, err := a.httpClient.Post(a.baseURL+"/api/clipboard", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("clipboard upload failed: %s", strings.TrimSpace(string(raw)))
	}
	return nil
}

func (a *agent) prepareClipboardForPost(state clipboardState) (clipboardState, error) {
	state = normalizeClipboardState(state)
	if state.Kind != "text" && state.Kind != "image" {
		return clipboardState{}, fmt.Errorf("only text and image clipboard are supported")
	}
	return state, nil
}

func (a *agent) writeClipboardState(state clipboardState) error {
	if a == nil || !a.clipboardSync {
		return nil
	}
	state = normalizeClipboardState(state)
	if state.Kind != "text" && state.Kind != "image" {
		return nil
	}
	return writeClipboardState(state)
}

func (a *agent) receiveClipboardFiles(files []transferFileRef) ([]string, error) {
	if len(files) == 0 {
		return nil, nil
	}
	targetDir := resolveAgentClipboardCacheDir(a.deviceID)
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(files))
	var firstErr error
	for _, file := range files {
		if strings.TrimSpace(file.ID) == "" {
			continue
		}
		if !a.beginTransfer(file.ID) {
			continue
		}
		path, err := a.receiveTransferFileToPath(file, targetDir)
		a.finishTransfer(file.ID)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			log.Printf("[agent][clipboard] receive %s failed: %v", file.Name, err)
			continue
		}
		paths = append(paths, path)
	}
	if len(paths) == 0 && firstErr != nil {
		return nil, firstErr
	}
	if len(paths) > 0 {
		log.Printf("[agent][clipboard] staged %d file(s) for paste -> %s", len(paths), targetDir)
	}
	return paths, nil
}

func (a *agent) uploadClipboardFiles(paths []string) ([]transferFileRef, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(paths))
	refs := make([]transferFileRef, 0, len(paths))
	var firstErr error
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		cleanPath := filepath.Clean(path)
		if _, ok := seen[strings.ToLower(cleanPath)]; ok {
			continue
		}
		seen[strings.ToLower(cleanPath)] = struct{}{}
		info, err := os.Stat(cleanPath)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			log.Printf("[agent][clipboard] stat file %s failed: %v", cleanPath, err)
			continue
		}
		if info.IsDir() {
			log.Printf("[agent][clipboard] skip directory %s", cleanPath)
			continue
		}
		ref, err := a.uploadLocalFile(cleanPath, info.Size(), "agent_to_controller")
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			log.Printf("[agent][clipboard] upload file %s failed: %v", cleanPath, err)
			continue
		}
		refs = append(refs, ref)
	}
	if len(refs) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return refs, nil
}

func (a *agent) postCursor(cursor cursorSnapshot) error {
	payload := agentPollRequest{
		DeviceID:      a.deviceID,
		CursorX:       cursor.X,
		CursorY:       cursor.Y,
		CursorVisible: cursor.Visible,
		CursorKind:    cursor.Kind,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	resp, err := a.httpClient.Post(a.baseURL+"/api/cursor", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("cursor sync failed: %s", strings.TrimSpace(string(raw)))
	}
	return nil
}

func (a *agent) receiveTransferFiles(payload transferCommandPayload) {
	if len(payload.Files) == 0 {
		return
	}
	targetDir := resolveAgentTransferTargetDir(payload.Target)
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		log.Printf("[agent][transfer] create target dir failed: %v", err)
		return
	}
	for _, file := range payload.Files {
		if strings.TrimSpace(file.ID) == "" {
			continue
		}
		if !a.beginTransfer(file.ID) {
			continue
		}
		if err := a.receiveTransferFile(file, targetDir); err != nil {
			log.Printf("[agent][transfer] %s failed: %v", file.Name, err)
		}
		a.finishTransfer(file.ID)
	}
}

func (a *agent) handleRemoteFileList(raw string) {
	var req remoteFileRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		return
	}
	result := a.listRemoteFiles(req)
	if err := a.postRemoteFileResult(result); err != nil {
		log.Printf("[agent][files] post list result failed: %v", err)
	}
}

func (a *agent) handleRemoteFileDownload(raw string) {
	var req remoteFileRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		return
	}
	result := remoteFileResult{ID: strings.TrimSpace(req.ID), DeviceID: a.deviceID}
	path := resolveAgentFilePath(req.Path)
	info, err := os.Stat(path)
	if err != nil {
		result.Error = err.Error()
		_ = a.postRemoteFileResult(result)
		return
	}
	if info.IsDir() {
		result.Error = "selected path is a directory"
		_ = a.postRemoteFileResult(result)
		return
	}
	posted := false
	ref, err := a.uploadLocalFileNotify(path, info.Size(), "agent_to_controller", func(ref transferFileRef) {
		result.Path = path
		result.File = &ref
		if err := a.postRemoteFileResult(result); err != nil {
			log.Printf("[agent][files] post early download result failed: %v", err)
			return
		}
		posted = true
	})
	if err != nil {
		if !posted {
			result.Error = err.Error()
			_ = a.postRemoteFileResult(result)
		}
		return
	}
	result.Path = path
	result.File = &ref
	if !posted {
		if err := a.postRemoteFileResult(result); err != nil {
			log.Printf("[agent][files] post download result failed: %v", err)
		}
	}
}

func (a *agent) uploadLocalFile(path string, size int64, direction string) (transferFileRef, error) {
	return a.uploadLocalFileNotify(path, size, direction, nil)
}

func (a *agent) uploadLocalFileNotify(path string, size int64, direction string, onCreated func(transferFileRef)) (transferFileRef, error) {
	state, err := a.createTransfer(transferCreateRequest{
		DeviceID:  a.deviceID,
		Name:      filepath.Base(path),
		Size:      size,
		Direction: direction,
	})
	if err != nil {
		return transferFileRef{}, err
	}
	ref := transferFileRef{ID: state.ID, Name: state.Name, Size: state.Size}
	if onCreated != nil {
		onCreated(ref)
	}
	file, err := os.Open(path)
	if err != nil {
		_ = a.cancelTransfer(state.ID)
		return transferFileRef{}, err
	}
	defer file.Close()

	const chunkSize = 256 << 10
	buf := make([]byte, chunkSize)
	var offset int64
	for {
		n, readErr := file.Read(buf)
		if n > 0 {
			if err := a.putTransferChunk(state.ID, offset, buf[:n]); err != nil {
				_ = a.cancelTransfer(state.ID)
				return transferFileRef{}, err
			}
			offset += int64(n)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			_ = a.cancelTransfer(state.ID)
			return transferFileRef{}, readErr
		}
	}
	if err := a.completeTransfer(state.ID); err != nil {
		_ = a.cancelTransfer(state.ID)
		return transferFileRef{}, err
	}
	log.Printf("[agent][transfer] uploaded %s (%s)", filepath.Base(path), formatBytes(size))
	return ref, nil
}

func (a *agent) handleRemoteCommand(raw string) {
	var req remoteCommandRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		return
	}
	req.ID = strings.TrimSpace(req.ID)
	req.Command = strings.TrimSpace(req.Command)
	req.Kind = strings.TrimSpace(strings.ToLower(req.Kind))
	if req.ID == "" {
		return
	}
	result := remoteCommandResult{
		ID:        req.ID,
		DeviceID:  a.deviceID,
		Command:   req.Command,
		StartedAt: time.Now().Format(time.RFC3339),
	}
	if req.Kind == "cwd" {
		result.Dir = a.currentRemoteCommandDir()
		result.Output = result.Dir
		result.FinishedAt = time.Now().Format(time.RFC3339)
		_ = a.postRemoteCommandResult(result)
		return
	}
	if req.Kind == "setcwd" {
		dir, err := a.resolveRemoteCommandDir(req.Dir)
		if err != nil {
			result.Error = err.Error()
		} else {
			a.setRemoteCommandDir(dir)
			result.Dir = dir
			result.Output = "directory updated: " + dir
		}
		result.FinishedAt = time.Now().Format(time.RFC3339)
		_ = a.postRemoteCommandResult(result)
		return
	}
	if req.Command == "" {
		result.Error = "empty command"
		result.FinishedAt = time.Now().Format(time.RFC3339)
		_ = a.postRemoteCommandResult(result)
		return
	}
	if handled := a.handleBuiltinRemoteCommand(&result, req.Command); handled {
		_ = a.postRemoteCommandResult(result)
		return
	}
	dir, err := a.resolveRemoteCommandDir(req.Dir)
	if err != nil {
		result.Error = err.Error()
		result.FinishedAt = time.Now().Format(time.RFC3339)
		_ = a.postRemoteCommandResult(result)
		return
	}
	result.Dir = dir
	cmd := exec.Command("cmd.exe", "/C", req.Command)
	cmd.Dir = dir
	output := &cappedOutputBuffer{limit: 256 << 10}
	cmd.Stdout = output
	cmd.Stderr = output
	if err := cmd.Start(); err != nil {
		result.Error = err.Error()
		result.FinishedAt = time.Now().Format(time.RFC3339)
		_ = a.postRemoteCommandResult(result)
		return
	}
	a.setRemoteCommandDir(dir)
	if req.TimeoutSec <= 0 {
		result.Output = "command started; not waiting for output"
		result.FinishedAt = time.Now().Format(time.RFC3339)
		_ = a.postRemoteCommandResult(result)
		go func() {
			_ = cmd.Wait()
		}()
		return
	}
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()
	select {
	case err := <-done:
		result.Output = output.String()
		result.ExitCode = commandExitCode(err)
		if err != nil {
			result.Error = err.Error()
		}
		result.FinishedAt = time.Now().Format(time.RFC3339)
		_ = a.postRemoteCommandResult(result)
	case <-time.After(time.Duration(req.TimeoutSec) * time.Second):
		result.Output = output.String()
		result.TimedOut = true
		result.Error = fmt.Sprintf("timeout after %ds; command is still running on remote", req.TimeoutSec)
		result.FinishedAt = time.Now().Format(time.RFC3339)
		_ = a.postRemoteCommandResult(result)
	}
}

func (a *agent) handleBuiltinRemoteCommand(result *remoteCommandResult, command string) bool {
	if result == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(command)) {
	case "hide":
		result.Output = setAgentConsoleVisible(false)
		result.FinishedAt = time.Now().Format(time.RFC3339)
		return true
	case "show":
		result.Output = setAgentConsoleVisible(true)
		result.FinishedAt = time.Now().Format(time.RFC3339)
		return true
	default:
		return false
	}
}

func setAgentConsoleVisible(visible bool) string {
	hwnd, _, _ := procGetConsoleWindow.Call()
	if hwnd == 0 {
		return "no console window found"
	}
	cmd := uintptr(swHide)
	label := "hidden"
	if visible {
		cmd = uintptr(swShow)
		label = "shown"
	}
	procShowWindow.Call(hwnd, cmd)
	return "console window " + label
}

type cappedOutputBuffer struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	limit int
}

func (b *cappedOutputBuffer) Write(p []byte) (int, error) {
	if b == nil {
		return len(p), nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.limit <= 0 || b.buf.Len() < b.limit {
		remaining := b.limit - b.buf.Len()
		if b.limit <= 0 || remaining > len(p) {
			remaining = len(p)
		}
		if remaining > 0 {
			_, _ = b.buf.Write(p[:remaining])
		}
	}
	return len(p), nil
}

func (b *cappedOutputBuffer) String() string {
	if b == nil {
		return ""
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	out := decodeConsoleOutput(b.buf.Bytes())
	if b.limit > 0 && b.buf.Len() >= b.limit {
		out += "\r\n[output truncated]"
	}
	return out
}

func decodeConsoleOutput(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	if utf8.Valid(data) {
		return string(data)
	}
	if s, ok := decodeMultiByte(data, 1); ok {
		return s
	}
	if s, ok := decodeMultiByte(data, 0); ok {
		return s
	}
	if s, ok := decodeMultiByte(data, 936); ok {
		return s
	}
	return string(data)
}

func decodeMultiByte(data []byte, codePage uint32) (string, bool) {
	if len(data) == 0 {
		return "", true
	}
	n, _, _ := procMultiByteToWideChar.Call(
		uintptr(codePage),
		0,
		uintptr(unsafe.Pointer(&data[0])),
		uintptr(len(data)),
		0,
		0,
	)
	if n == 0 {
		return "", false
	}
	buf := make([]uint16, int(n))
	n, _, _ = procMultiByteToWideChar.Call(
		uintptr(codePage),
		0,
		uintptr(unsafe.Pointer(&data[0])),
		uintptr(len(data)),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
	)
	if n == 0 {
		return "", false
	}
	return string(utf16.Decode(buf[:int(n)])), true
}

func commandExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func (a *agent) resolveRemoteCommandDir(dir string) (string, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		dir = a.currentRemoteCommandDir()
	}
	if dir == "" {
		dir = defaultAgentCommandDir()
	}
	cleaned := filepath.Clean(dir)
	if !filepath.IsAbs(cleaned) {
		if abs, err := filepath.Abs(cleaned); err == nil {
			cleaned = abs
		}
	}
	st, err := os.Stat(cleaned)
	if err != nil {
		return "", err
	}
	if !st.IsDir() {
		return "", fmt.Errorf("not a directory: %s", cleaned)
	}
	return cleaned, nil
}

func (a *agent) currentRemoteCommandDir() string {
	if a == nil {
		return defaultAgentCommandDir()
	}
	a.remoteCommandMu.Lock()
	defer a.remoteCommandMu.Unlock()
	dir := strings.TrimSpace(a.remoteCommandDir)
	if dir == "" {
		return defaultAgentCommandDir()
	}
	if st, err := os.Stat(dir); err == nil && st.IsDir() {
		return dir
	}
	a.remoteCommandDir = ""
	return defaultAgentCommandDir()
}

func (a *agent) setRemoteCommandDir(dir string) {
	if a == nil {
		return
	}
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return
	}
	a.remoteCommandMu.Lock()
	a.remoteCommandDir = dir
	a.remoteCommandMu.Unlock()
}

func (a *agent) listRemoteFiles(req remoteFileRequest) remoteFileResult {
	result := remoteFileResult{ID: strings.TrimSpace(req.ID), DeviceID: a.deviceID}
	dir := a.resolveRemoteFileListPath(req.Path)
	info, err := os.Stat(dir)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	if !info.IsDir() {
		dir = filepath.Dir(dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	result.Path = dir
	result.Parent = filepath.Dir(dir)
	a.setRemoteFileDir(dir)
	result.Entries = make([]remoteFileEntry, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		name := entry.Name()
		result.Entries = append(result.Entries, remoteFileEntry{
			Name:    name,
			Path:    filepath.Join(dir, name),
			IsDir:   info.IsDir(),
			Size:    info.Size(),
			ModTime: info.ModTime().Format("2006-01-02 15:04:05"),
		})
	}
	sort.Slice(result.Entries, func(i, j int) bool {
		if result.Entries[i].IsDir != result.Entries[j].IsDir {
			return result.Entries[i].IsDir
		}
		return strings.ToLower(result.Entries[i].Name) < strings.ToLower(result.Entries[j].Name)
	})
	return result
}

func (a *agent) resolveRemoteFileListPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		if dir := a.currentRemoteFileDir(); dir != "" {
			return dir
		}
	}
	return resolveAgentFilePath(path)
}

func (a *agent) currentRemoteFileDir() string {
	if a == nil {
		return ""
	}
	a.remoteFileMu.Lock()
	defer a.remoteFileMu.Unlock()
	dir := strings.TrimSpace(a.remoteFileDir)
	if dir == "" {
		return ""
	}
	if st, err := os.Stat(dir); err == nil && st.IsDir() {
		return dir
	}
	a.remoteFileDir = ""
	return ""
}

func (a *agent) setRemoteFileDir(dir string) {
	if a == nil {
		return
	}
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return
	}
	a.remoteFileMu.Lock()
	a.remoteFileDir = dir
	a.remoteFileMu.Unlock()
}

func (a *agent) postRemoteFileResult(result remoteFileResult) error {
	result.DeviceID = a.deviceID
	result.UpdatedAt = time.Now().Format(time.RFC3339)
	body, err := json.Marshal(result)
	if err != nil {
		return err
	}
	resp, err := a.httpClient.Post(a.baseURL+"/api/file/result", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("file result post failed: %s", strings.TrimSpace(string(raw)))
	}
	return nil
}

func (a *agent) postRemoteCommandResult(result remoteCommandResult) error {
	result.DeviceID = a.deviceID
	result.UpdatedAt = time.Now().Format(time.RFC3339)
	body, err := json.Marshal(result)
	if err != nil {
		return err
	}
	resp, err := a.httpClient.Post(a.baseURL+"/api/command/result", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("command result post failed: %s", strings.TrimSpace(string(raw)))
	}
	return nil
}

func (a *agent) beginTransfer(id string) bool {
	a.transferMu.Lock()
	defer a.transferMu.Unlock()
	if _, exists := a.activeTransfers[id]; exists {
		return false
	}
	a.activeTransfers[id] = struct{}{}
	return true
}

func (a *agent) finishTransfer(id string) {
	a.transferMu.Lock()
	delete(a.activeTransfers, id)
	a.transferMu.Unlock()
}

func (a *agent) receiveTransferFile(file transferFileRef, targetDir string) error {
	_, err := a.receiveTransferFileToPath(file, targetDir)
	return err
}

func (a *agent) receiveTransferFileToPath(file transferFileRef, targetDir string) (string, error) {
	name := sanitizeTransferFilename(file.Name)
	if name == "" {
		name = "remote-file.bin"
	}
	targetPath := uniqueTransferPath(filepath.Join(targetDir, name))
	if err := a.postTransferProgress(file.ID, 0, "receiving", ""); err != nil {
		log.Printf("[agent][transfer] progress start failed for %s: %v", file.Name, err)
	}
	dst, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		_ = a.postTransferProgress(file.ID, 0, "failed", err.Error())
		return "", err
	}
	defer dst.Close()

	const chunkSize = 256 << 10
	var offset int64
	for offset < file.Size {
		chunk, done, err := a.fetchTransferChunk(file.ID, offset, chunkSize)
		if err != nil {
			_ = a.postTransferProgress(file.ID, offset, "failed", err.Error())
			return "", err
		}
		if len(chunk) == 0 {
			if done {
				break
			}
			return "", fmt.Errorf("transfer returned empty chunk before completion")
		}
		n, err := dst.Write(chunk)
		if err != nil {
			_ = a.postTransferProgress(file.ID, offset, "failed", err.Error())
			return "", err
		}
		offset += int64(n)
		if err := a.postTransferProgress(file.ID, offset, "receiving", ""); err != nil {
			log.Printf("[agent][transfer] progress update failed for %s: %v", file.Name, err)
		}
	}
	if err := dst.Sync(); err != nil {
		log.Printf("[agent][transfer] sync warning for %s: %v", file.Name, err)
	}
	if err := a.postTransferProgress(file.ID, offset, "completed", ""); err != nil {
		log.Printf("[agent][transfer] progress complete failed for %s: %v", file.Name, err)
	}
	log.Printf("[agent][transfer] saved %s (%s) -> %s", name, formatBytes(file.Size), targetPath)
	return targetPath, nil
}

func (a *agent) fetchTransferChunk(id string, offset int64, size int) ([]byte, bool, error) {
	endpoint := fmt.Sprintf("%s/api/transfer/chunk?id=%s&offset=%d&size=%d", a.baseURL, url.QueryEscape(id), offset, size)
	resp, err := a.httpClient.Get(endpoint)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		data, err := io.ReadAll(resp.Body)
		return data, false, err
	case http.StatusNoContent:
		return nil, true, nil
	default:
		raw, _ := io.ReadAll(resp.Body)
		return nil, false, fmt.Errorf("transfer chunk failed: %s", strings.TrimSpace(string(raw)))
	}
}

func (a *agent) postTransferProgress(id string, deliveredBytes int64, status, transferErr string) error {
	req := transferProgressRequest{
		ID:             id,
		DeliveredBytes: deliveredBytes,
		Status:         status,
		Error:          transferErr,
	}
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	resp, err := a.httpClient.Post(a.baseURL+"/api/transfer/progress", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("transfer progress failed: %s", strings.TrimSpace(string(raw)))
	}
	return nil
}

func (a *agent) createTransfer(req transferCreateRequest) (transferSnapshot, error) {
	var data transferSnapshot
	body, err := json.Marshal(req)
	if err != nil {
		return data, err
	}
	resp, err := a.httpClient.Post(a.baseURL+"/api/transfer/create", "application/json", bytes.NewReader(body))
	if err != nil {
		return data, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return data, fmt.Errorf("transfer create failed: %s", strings.TrimSpace(string(raw)))
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return data, err
	}
	return data, nil
}

func (a *agent) putTransferChunk(id string, offset int64, data []byte) error {
	req, err := http.NewRequest(http.MethodPut, fmt.Sprintf("%s/api/transfer/chunk?id=%s&offset=%d", a.baseURL, url.QueryEscape(id), offset), bytes.NewReader(data))
	if err != nil {
		return err
	}
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("transfer chunk upload failed: %s", strings.TrimSpace(string(raw)))
	}
	return nil
}

func (a *agent) completeTransfer(id string) error {
	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/api/transfer/complete?id=%s", a.baseURL, url.QueryEscape(id)), nil)
	if err != nil {
		return err
	}
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("transfer complete failed: %s", strings.TrimSpace(string(raw)))
	}
	return nil
}

func (a *agent) cancelTransfer(id string) error {
	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/api/transfer/cancel?id=%s", a.baseURL, url.QueryEscape(id)), nil)
	if err != nil {
		return err
	}
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("transfer cancel failed: %s", strings.TrimSpace(string(raw)))
	}
	return nil
}

func resolveAgentTransferTargetDir(target string) string {
	home, _ := os.UserHomeDir()
	rawTarget := strings.TrimSpace(target)
	if rawTarget != "" && filepath.IsAbs(rawTarget) {
		if st, err := os.Stat(rawTarget); err == nil && st.IsDir() {
			return rawTarget
		}
		if err := os.MkdirAll(rawTarget, 0o755); err == nil {
			return rawTarget
		}
	}
	target = strings.TrimSpace(strings.ToLower(target))
	candidates := []string{}
	if target == "downloads" {
		candidates = append(candidates, filepath.Join(home, "Downloads"), filepath.Join(home, "Desktop"))
	} else {
		candidates = append(candidates, filepath.Join(home, "Desktop"), filepath.Join(home, "Downloads"))
	}
	if home != "" {
		candidates = append(candidates, home)
	}
	candidates = append(candidates, os.TempDir())
	for _, dir := range candidates {
		if dir == "" {
			continue
		}
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			return dir
		}
	}
	return os.TempDir()
}

func resolveAgentFilePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return defaultAgentFileDir()
	}
	cleaned := filepath.Clean(path)
	if filepath.IsAbs(cleaned) {
		return cleaned
	}
	if abs, err := filepath.Abs(cleaned); err == nil {
		return abs
	}
	return cleaned
}

func defaultAgentFileDir() string {
	home, _ := os.UserHomeDir()
	candidates := []string{}
	if home != "" {
		candidates = append(candidates, filepath.Join(home, "Desktop"), filepath.Join(home, "Downloads"), home)
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, wd)
	}
	candidates = append(candidates, os.TempDir())
	for _, dir := range candidates {
		if dir == "" {
			continue
		}
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			return dir
		}
	}
	return os.TempDir()
}

func defaultAgentCommandDir() string {
	if wd, err := os.Getwd(); err == nil && strings.TrimSpace(wd) != "" {
		if st, statErr := os.Stat(wd); statErr == nil && st.IsDir() {
			return wd
		}
	}
	return defaultAgentFileDir()
}

func resolveAgentClipboardCacheDir(deviceID string) string {
	root := agentClipboardCacheRoot()
	cleanupOldAgentClipboardCache(root, 24*time.Hour)
	device := safeCachePathSegment(deviceID, "device")
	batch := time.Now().Format("20060102-150405.000000000")
	return filepath.Join(root, device, batch)
}

func agentClipboardCacheRoot() string {
	if dir, err := os.UserCacheDir(); err == nil && strings.TrimSpace(dir) != "" {
		return filepath.Join(dir, "go-remote-desktop", "agent-clipboard")
	}
	return filepath.Join(os.TempDir(), "go-remote-desktop", "agent-clipboard")
}

func agentClipboardPathsInCache(paths []string) bool {
	if len(paths) == 0 {
		return false
	}
	root := agentClipboardCacheRoot()
	for _, path := range paths {
		if !pathWithinDir(path, root) {
			return false
		}
	}
	return true
}

func cleanupOldAgentClipboardCache(root string, maxAge time.Duration) {
	if strings.TrimSpace(root) == "" || maxAge <= 0 {
		return
	}
	cutoff := time.Now().Add(-maxAge)
	devices, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, device := range devices {
		if !device.IsDir() {
			continue
		}
		deviceDir := filepath.Join(root, device.Name())
		batches, err := os.ReadDir(deviceDir)
		if err != nil {
			continue
		}
		for _, batch := range batches {
			if !batch.IsDir() {
				continue
			}
			info, err := batch.Info()
			if err != nil || info.ModTime().After(cutoff) {
				continue
			}
			_ = os.RemoveAll(filepath.Join(deviceDir, batch.Name()))
		}
		if remaining, err := os.ReadDir(deviceDir); err == nil && len(remaining) == 0 {
			_ = os.Remove(deviceDir)
		}
	}
}

func safeCachePathSegment(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		value = fallback
	}
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	segment := strings.Trim(b.String(), " .")
	if segment == "" {
		segment = fallback
	}
	return segment
}

func pathWithinDir(path, dir string) bool {
	path = strings.TrimSpace(path)
	dir = strings.TrimSpace(dir)
	if path == "" || dir == "" {
		return false
	}
	absPath, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return false
	}
	absDir, err := filepath.Abs(filepath.Clean(dir))
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absDir, absPath)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)))
}

func sanitizeTransferFilename(name string) string {
	name = filepath.Base(strings.TrimSpace(name))
	if name == "." || name == "" {
		return ""
	}
	return name
}

func uniqueTransferPath(path string) string {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return path
	}
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(filepath.Base(path), ext)
	dir := filepath.Dir(path)
	for i := 1; i < 1000; i++ {
		candidate := filepath.Join(dir, fmt.Sprintf("%s (%d)%s", base, i, ext))
		if _, err := os.Stat(candidate); errors.Is(err, os.ErrNotExist) {
			return candidate
		}
	}
	return filepath.Join(dir, fmt.Sprintf("%s-%d%s", base, time.Now().Unix(), ext))
}

func (a *agent) fetchCommands() (commandResponse, error) {
	var respData commandResponse
	nativeMetrics := virtualDesktopMetrics()
	metrics := a.captureMetrics(nativeMetrics)
	cursor := a.currentCursorState(metrics, nativeMetrics, false)
	if a.usingQUIC() {
		return a.fetchCommandsQUIC(cursor)
	}
	endpoint := fmt.Sprintf(
		"%s/api/commands?deviceId=%s&after=%d&cursorX=%d&cursorY=%d&cursorVisible=%d&cursorKind=%s",
		a.baseURL,
		url.QueryEscape(a.deviceID),
		a.lastSeq,
		cursor.X,
		cursor.Y,
		boolToInt(cursor.Visible),
		url.QueryEscape(cursor.Kind),
	)

	resp, err := a.httpClient.Get(endpoint)
	if err != nil {
		return respData, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return respData, fmt.Errorf("commands request failed: %s", strings.TrimSpace(string(raw)))
	}

	err = json.NewDecoder(resp.Body).Decode(&respData)
	return respData, err
}

func (a *agent) captureAndUpload(forceFull bool) error {
	if forceFull && a.shouldDeferActiveFull(time.Now()) {
		forceFull = false
	}
	if handled, err := a.captureAndUploadBGRA(forceFull); handled || err != nil {
		return err
	}
	a.clearBGRABase()

	gen := a.captureMonitorGen.Load() // 快照代数，防止旧捕获覆盖 switchMonitor 设定的 origin
	current, nativeMetrics, err := a.captureScreenRGBA()
	if err != nil {
		return err
	}
	metrics := a.captureMetrics(nativeMetrics)
	current = a.scaleCapturedFrame(current, nativeMetrics, metrics)
	baseQuality := a.effectiveQuality()
	fullQuality := a.fullFrameQuality(baseQuality)
	if a.captureMonitorGen.Load() == gen {
		a.originX = nativeMetrics.Left
		a.originY = nativeMetrics.Top
		a.input.setDesktop(a.originX, a.originY, nativeMetrics.Width, nativeMetrics.Height, metrics.Width, metrics.Height)
	}

	if a.effectiveStreamMode() == "video" {
		if !forceFull && a.lastImage != nil && sameBounds(a.lastImage, current) {
			patchRect, changed := diffBounds(a.lastImage, current)
			if !changed {
				return nil
			}
			var preferredTileRects []image.Rectangle
			if shouldProbeTilesForVideo(current.Bounds(), patchRect) {
				preferredTileRects = diffTiles(a.lastImage, current, tilePatchSize)
			}
			if normalizeFrameCodec(a.effectiveFrameCodec(), "") == "h264-mf" && a.shouldUseHybridPatchInVideo(current.Bounds(), patchRect) {
				if a.videoPatchReady {
					if patchRect.Dx() > 0 && patchRect.Dy() > 0 && shouldUsePatchArea(patchRect, current.Bounds(), 78) {
						patchImage := cropRGBA(current, patchRect)
						patchQuality := a.patchQuality(baseQuality+6, current.Bounds(), patchRect)
						if patchData, patchErr := encodeJPEGImage(patchImage, patchQuality); patchErr == nil {
							if uploadErr := a.uploadPatch(patchData, metrics, patchRect, patchQuality); uploadErr == nil {
								a.lastImage = mergeSentPatchIntoBase(a.lastImage, current, patchRect)
								a.lastShot = time.Now()
								a.noteUpload("patch", "jpeg", len(patchData), patchRect.Dx(), patchRect.Dy())
								return nil
							}
						}
					}
					if tileRects := diffTiles(a.lastImage, current, tilePatchSize); a.shouldUseHybridTilesInVideo(current.Bounds(), tileRects) {
						atlasImage, tiles := buildTileAtlas(current, tileRects, tilePatchSize)
						if atlasImage != nil && len(tiles) > 0 {
							tileQuality := a.tilesQuality(baseQuality+4, current.Bounds(), tiles)
							if atlasData, atlasErr := encodeJPEGImage(atlasImage, tileQuality); atlasErr == nil {
								if uploadErr := a.uploadTiles(atlasData, metrics, tiles, tileQuality); uploadErr == nil {
									a.lastImage = mergeSentTilesIntoBase(a.lastImage, current, tiles)
									a.lastShot = time.Now()
									a.noteUpload("tiles", "jpeg", len(atlasData), atlasImage.Bounds().Dx(), atlasImage.Bounds().Dy())
									return nil
								}
							}
						}
					}
				}
				keyQuality := clamp(max(baseQuality+8, 68), 58, 86)
				keyData, keyErr := encodeJPEGImage(current, keyQuality)
				if keyErr == nil {
					if uploadErr := a.uploadFull(keyData, "jpeg", metrics, keyQuality); uploadErr == nil {
						a.lastImage = current
						a.lastShot = time.Now()
						a.lastFullShot = a.lastShot
						a.videoPatchReady = true
						a.noteUpload("full", "jpeg", len(keyData), current.Bounds().Dx(), current.Bounds().Dy())
						return nil
					}
				}
			}
			if a.shouldPreferDeltaInVideo(current.Bounds(), patchRect) {
				if a.shouldPreferTileDelta(current.Bounds(), preferredTileRects) && shouldPreferTilesOverPatch(current.Bounds(), patchRect, preferredTileRects) {
					atlasImage, tiles := buildTileAtlas(current, preferredTileRects, tilePatchSize)
					if atlasImage != nil && len(tiles) > 0 {
						tileQuality := a.tilesQuality(baseQuality, current.Bounds(), tiles)
						if atlasData, atlasErr := encodeJPEGImage(atlasImage, tileQuality); atlasErr == nil {
							if uploadErr := a.uploadTiles(atlasData, metrics, tiles, tileQuality); uploadErr == nil {
								a.lastImage = mergeSentTilesIntoBase(a.lastImage, current, tiles)
								a.lastShot = time.Now()
								a.noteUpload("tiles", "jpeg", len(atlasData), atlasImage.Bounds().Dx(), atlasImage.Bounds().Dy())
								return nil
							}
						}
					}
				}
				if patchRect.Dx() > 0 && patchRect.Dy() > 0 && shouldUsePatchArea(patchRect, current.Bounds(), 72) {
					patchImage := cropRGBA(current, patchRect)
					patchQuality := a.patchQuality(baseQuality, current.Bounds(), patchRect)
					if patchData, patchErr := encodeJPEGImage(patchImage, patchQuality); patchErr == nil {
						if uploadErr := a.uploadPatch(patchData, metrics, patchRect, patchQuality); uploadErr == nil {
							a.lastImage = mergeSentPatchIntoBase(a.lastImage, current, patchRect)
							a.lastShot = time.Now()
							a.noteUpload("patch", "jpeg", len(patchData), patchRect.Dx(), patchRect.Dy())
							return nil
						}
					}
				}
			}
		}
	} else {
		if !fastFrameChanged(a.lastImage, current, 12) {
			return nil
		}
		fullData, codec, frameW, frameH, err := a.encodeVideoFrame(current, fullQuality)
		if err != nil {
			return err
		}
		if err := a.uploadFull(fullData, codec, metrics, fullQuality); err != nil {
			return err
		}

		a.lastImage = current
		a.lastShot = time.Now()
		a.lastFullShot = a.lastShot
		a.videoPatchReady = codec == "jpeg"
		a.noteUpload("video", codec, len(fullData), frameW, frameH)
		return nil
	}

	if !forceFull && a.lastImage != nil && sameBounds(a.lastImage, current) {
		if patchRect, changed := diffBounds(a.lastImage, current); changed {
			if shouldUsePatch(patchRect, current.Bounds()) {
				patchImage := cropRGBA(current, patchRect)
				patchQuality := a.patchQuality(baseQuality, current.Bounds(), patchRect)
				if patchData, patchErr := encodeJPEGImage(patchImage, patchQuality); patchErr == nil {
					if uploadErr := a.uploadPatch(patchData, metrics, patchRect, patchQuality); uploadErr == nil {
						a.lastImage = mergeSentPatchIntoBase(a.lastImage, current, patchRect)
						a.lastShot = time.Now()
						a.noteUpload("patch", "jpeg", len(patchData), patchRect.Dx(), patchRect.Dy())
						return nil
					}
				}
			}

			if tileRects := diffTiles(a.lastImage, current, tilePatchSize); shouldUseTilePatch(tileRects, current.Bounds()) {
				atlasImage, tiles := buildTileAtlas(current, tileRects, tilePatchSize)
				if atlasImage != nil && len(tiles) > 0 {
					tileQuality := a.tilesQuality(baseQuality, current.Bounds(), tiles)
					if atlasData, atlasErr := encodeJPEGImage(atlasImage, tileQuality); atlasErr == nil {
						if uploadErr := a.uploadTiles(atlasData, metrics, tiles, tileQuality); uploadErr == nil {
							a.lastImage = mergeSentTilesIntoBase(a.lastImage, current, tiles)
							a.lastShot = time.Now()
							a.noteUpload("tiles", "jpeg", len(atlasData), atlasImage.Bounds().Dx(), atlasImage.Bounds().Dy())
							return nil
						}
					}
				}
			}
		} else {
			return nil
		}
	}

	fullData, err := encodeJPEGImage(current, fullQuality)
	if err != nil {
		return err
	}
	if err := a.uploadFull(fullData, "jpeg", metrics, fullQuality); err != nil {
		return err
	}

	a.lastImage = current
	a.lastShot = time.Now()
	a.lastFullShot = a.lastShot
	a.videoPatchReady = true
	a.noteUpload("full", "jpeg", len(fullData), current.Bounds().Dx(), current.Bounds().Dy())
	return nil
}

func (a *agent) captureAndUploadBGRA(forceFull bool) (bool, error) {
	if !a.canUseBGRAFastPath() {
		return false, nil
	}
	gen := a.captureMonitorGen.Load() // 快照代数，防止旧捕获覆盖 switchMonitor 设定的 origin
	frame, err := a.captureDesktopBGRA()
	if err != nil {
		if errors.Is(err, errDesktopUnavailable) {
			return true, err
		}
		a.logErrorf("[agent][capture] bgra fast path failed: %v", err)
		return false, nil
	}
	current := frame.Pixels
	nativeMetrics := frame.Metrics
	metrics := a.captureMetrics(nativeMetrics)
	if metrics.Width != nativeMetrics.Width || metrics.Height != nativeMetrics.Height {
		return false, nil
	}
	baseQuality := a.effectiveQuality()
	fullQuality := a.fullFrameQuality(baseQuality)
	if a.captureMonitorGen.Load() == gen {
		a.originX = nativeMetrics.Left
		a.originY = nativeMetrics.Top
		a.input.setDesktop(a.originX, a.originY, nativeMetrics.Width, nativeMetrics.Height, metrics.Width, metrics.Height)
	}

	bounds := image.Rect(0, 0, metrics.Width, metrics.Height)
	sameBase := len(a.lastFrameBGRA) == len(current) && sameDesktopMetrics(a.lastFrameMetrics, metrics)
	if !forceFull && sameBase {
		var patchRect image.Rectangle
		var preferredTileRects []image.Rectangle
		var changed bool
		if frame.DirtyKnown {
			preferredTileRects = normalizeCaptureDirtyRects(frame.DirtyRects, bounds)
			changed = len(preferredTileRects) > 0
			if changed {
				patchRect = unionImageRects(preferredTileRects)
			}
		} else {
			patchRect, changed = diffBoundsBGRA(a.lastFrameBGRA, current, metrics.Width, metrics.Height)
		}
		if !changed {
			return true, nil
		}

		if !frame.DirtyKnown && a.effectiveStreamMode() == "video" && shouldProbeTilesForVideo(bounds, patchRect) {
			preferredTileRects = diffTilesBGRA(a.lastFrameBGRA, current, metrics.Width, metrics.Height, tilePatchSize)
		}

		if a.effectiveStreamMode() == "video" {
			if a.shouldPreferDeltaInVideo(bounds, patchRect) {
				if a.shouldPreferTileDelta(bounds, preferredTileRects) && shouldPreferTilesOverPatch(bounds, patchRect, preferredTileRects) {
					if a.tryUploadBGRATiles(current, metrics, bounds, preferredTileRects, baseQuality) {
						return true, nil
					}
				}
				if patchRect.Dx() > 0 && patchRect.Dy() > 0 && shouldUsePatchArea(patchRect, bounds, 72) {
					if a.tryUploadBGRAPatch(current, metrics, bounds, patchRect, baseQuality) {
						return true, nil
					}
				}
			}
		} else {
			if shouldUsePatch(patchRect, bounds) {
				if a.tryUploadBGRAPatch(current, metrics, bounds, patchRect, baseQuality) {
					return true, nil
				}
			}
			tileRects := diffTilesBGRA(a.lastFrameBGRA, current, metrics.Width, metrics.Height, tilePatchSize)
			if shouldUseTilePatch(tileRects, bounds) {
				if a.tryUploadBGRATiles(current, metrics, bounds, tileRects, baseQuality) {
					return true, nil
				}
			}
		}
	}

	fullImage := bgraToRGBAImage(current, metrics.Width, metrics.Height)
	fullData, err := encodeJPEGImage(fullImage, fullQuality)
	if err != nil {
		return true, err
	}
	if err := a.uploadFull(fullData, "jpeg", metrics, fullQuality); err != nil {
		return true, err
	}
	a.setBGRABase(current, metrics)
	a.lastImage = nil
	a.lastShot = time.Now()
	a.lastFullShot = a.lastShot
	a.videoPatchReady = true
	a.noteUpload("full", "jpeg", len(fullData), metrics.Width, metrics.Height)
	return true, nil
}

func (a *agent) canUseBGRAFastPath() bool {
	if a == nil || a.captureMaxWidth > 0 || a.captureMaxHeight > 0 {
		return false
	}
	if normalizeFrameCodec(a.effectiveFrameCodec(), "") == "h264-mf" {
		return false
	}
	return true
}

func (a *agent) tryUploadBGRAPatch(current []byte, metrics desktopMetrics, bounds, patchRect image.Rectangle, baseQuality int) bool {
	if rawData, rawRect := cropBGRABytes(current, metrics.Width, metrics.Height, patchRect); len(rawData) > 0 && len(rawData) <= rawBGRAMaxPayload {
		if err := a.uploadPatchCodec(rawData, "bgra", metrics, rawRect, 0); err == nil {
			a.updateBGRABaseRegion(current, metrics, rawRect)
			a.lastImage = nil
			a.lastShot = time.Now()
			a.noteUpload("patch", "bgra", len(rawData), rawRect.Dx(), rawRect.Dy())
			return true
		}
	}

	patchImage := cropBGRAtoRGBA(current, metrics.Width, metrics.Height, patchRect)
	if patchImage == nil {
		return false
	}
	patchQuality := a.patchQuality(baseQuality, bounds, patchRect)
	patchData, err := encodeJPEGImage(patchImage, patchQuality)
	if err != nil {
		return false
	}
	if err := a.uploadPatch(patchData, metrics, patchRect, patchQuality); err != nil {
		return false
	}
	a.updateBGRABaseRegion(current, metrics, patchRect)
	a.lastImage = nil
	a.lastShot = time.Now()
	a.noteUpload("patch", "jpeg", len(patchData), patchRect.Dx(), patchRect.Dy())
	return true
}

func (a *agent) tryUploadBGRATiles(current []byte, metrics desktopMetrics, bounds image.Rectangle, rects []image.Rectangle, baseQuality int) bool {
	rawAtlas, rawTiles := buildTileAtlasRawBGRA(current, metrics.Width, metrics.Height, rects, tilePatchSize)
	if len(rawAtlas) > 0 && len(rawAtlas) <= rawBGRAMaxPayload && len(rawTiles) > 0 {
		if err := a.uploadTilesCodec(rawAtlas, "bgra", metrics, rawTiles, 0); err == nil {
			a.updateBGRABaseTiles(current, metrics, rawTiles)
			a.lastImage = nil
			a.lastShot = time.Now()
			a.noteUpload("tiles", "bgra", len(rawAtlas), tileAtlasWidth(rawTiles), tileAtlasHeight(rawTiles))
			return true
		}
	}

	atlasImage, tiles := buildTileAtlasBGRA(current, metrics.Width, metrics.Height, rects, tilePatchSize)
	if atlasImage == nil || len(tiles) == 0 {
		return false
	}
	tileQuality := a.tilesQuality(baseQuality, bounds, tiles)
	atlasData, err := encodeJPEGImage(atlasImage, tileQuality)
	if err != nil {
		return false
	}
	if err := a.uploadTiles(atlasData, metrics, tiles, tileQuality); err != nil {
		return false
	}
	a.updateBGRABaseTiles(current, metrics, tiles)
	a.lastImage = nil
	a.lastShot = time.Now()
	a.noteUpload("tiles", "jpeg", len(atlasData), atlasImage.Bounds().Dx(), atlasImage.Bounds().Dy())
	return true
}

func (a *agent) setBGRABase(current []byte, metrics desktopMetrics) {
	if a == nil || len(current) == 0 {
		return
	}
	a.lastFrameBGRA = append(a.lastFrameBGRA[:0], current...)
	a.lastFrameMetrics = metrics
}

func (a *agent) clearBGRABase() {
	if a == nil {
		return
	}
	a.lastFrameBGRA = nil
	a.lastFrameMetrics = desktopMetrics{}
}

func (a *agent) updateBGRABaseRegion(current []byte, metrics desktopMetrics, rect image.Rectangle) {
	if a == nil || len(a.lastFrameBGRA) != len(current) || !sameDesktopMetrics(a.lastFrameMetrics, metrics) {
		a.setBGRABase(current, metrics)
		return
	}
	copyBGRARegion(a.lastFrameBGRA, current, metrics.Width, metrics.Height, rect)
}

func (a *agent) updateBGRABaseTiles(current []byte, metrics desktopMetrics, tiles []tilePatch) {
	if a == nil || len(a.lastFrameBGRA) != len(current) || !sameDesktopMetrics(a.lastFrameMetrics, metrics) {
		a.setBGRABase(current, metrics)
		return
	}
	for _, tile := range tiles {
		rect := image.Rect(tile.X, tile.Y, tile.X+tile.W, tile.Y+tile.H)
		copyBGRARegion(a.lastFrameBGRA, current, metrics.Width, metrics.Height, rect)
	}
}

func (a *agent) uploadFull(data []byte, codec string, metrics desktopMetrics, quality int) error {
	codec = normalizeFrameCodec(codec, "jpeg")
	if a.usingQUIC() {
		return a.uploadFrameQUIC(agentFrameEnvelope{
			Type:        "frame",
			DeviceID:    a.deviceID,
			Mode:        "full",
			Codec:       codec,
			OriginX:     metrics.Left,
			OriginY:     metrics.Top,
			Width:       metrics.Width,
			Height:      metrics.Height,
			Quality:     quality,
			PayloadSize: len(data),
		}, data)
	}
	if a.usingWSUpload() {
		return a.uploadFrameWS(agentFrameEnvelope{
			Type:        "frame",
			DeviceID:    a.deviceID,
			Mode:        "full",
			Codec:       codec,
			OriginX:     metrics.Left,
			OriginY:     metrics.Top,
			Width:       metrics.Width,
			Height:      metrics.Height,
			Quality:     quality,
			PayloadSize: len(data),
		}, data)
	}
	endpoint := fmt.Sprintf(
		"%s/api/frame?deviceId=%s&mode=full&codec=%s&originX=%d&originY=%d&width=%d&height=%d&quality=%d",
		a.baseURL,
		url.QueryEscape(a.deviceID),
		url.QueryEscape(codec),
		metrics.Left,
		metrics.Top,
		metrics.Width,
		metrics.Height,
		quality,
	)
	return a.uploadFrame(endpoint, data)
}

func (a *agent) uploadPatch(data []byte, metrics desktopMetrics, rect image.Rectangle, quality int) error {
	return a.uploadPatchCodec(data, "jpeg", metrics, rect, quality)
}

func (a *agent) uploadPatchCodec(data []byte, codec string, metrics desktopMetrics, rect image.Rectangle, quality int) error {
	codec = normalizeFrameCodec(codec, "jpeg")
	if a.usingQUIC() {
		return a.uploadFrameQUIC(agentFrameEnvelope{
			Type:        "frame",
			DeviceID:    a.deviceID,
			Mode:        "patch",
			Codec:       codec,
			OriginX:     metrics.Left,
			OriginY:     metrics.Top,
			Width:       metrics.Width,
			Height:      metrics.Height,
			Quality:     quality,
			PatchX:      rect.Min.X,
			PatchY:      rect.Min.Y,
			PatchW:      rect.Dx(),
			PatchH:      rect.Dy(),
			Tiles:       []tilePatch{{X: rect.Min.X, Y: rect.Min.Y, W: rect.Dx(), H: rect.Dy()}},
			PayloadSize: len(data),
		}, data)
	}
	if a.usingWSUpload() {
		return a.uploadFrameWS(agentFrameEnvelope{
			Type:        "frame",
			DeviceID:    a.deviceID,
			Mode:        "patch",
			Codec:       codec,
			OriginX:     metrics.Left,
			OriginY:     metrics.Top,
			Width:       metrics.Width,
			Height:      metrics.Height,
			Quality:     quality,
			PatchX:      rect.Min.X,
			PatchY:      rect.Min.Y,
			PatchW:      rect.Dx(),
			PatchH:      rect.Dy(),
			Tiles:       []tilePatch{{X: rect.Min.X, Y: rect.Min.Y, W: rect.Dx(), H: rect.Dy()}},
			PayloadSize: len(data),
		}, data)
	}
	endpoint := fmt.Sprintf(
		"%s/api/frame?deviceId=%s&mode=patch&codec=%s&originX=%d&originY=%d&width=%d&height=%d&x=%d&y=%d&w=%d&h=%d&quality=%d",
		a.baseURL,
		url.QueryEscape(a.deviceID),
		url.QueryEscape(codec),
		metrics.Left,
		metrics.Top,
		metrics.Width,
		metrics.Height,
		rect.Min.X,
		rect.Min.Y,
		rect.Dx(),
		rect.Dy(),
		quality,
	)
	return a.uploadFrame(endpoint, data)
}

func (a *agent) uploadTiles(atlas []byte, metrics desktopMetrics, tiles []tilePatch, quality int) error {
	return a.uploadTilesCodec(atlas, "jpeg", metrics, tiles, quality)
}

func (a *agent) uploadTilesCodec(atlas []byte, codec string, metrics desktopMetrics, tiles []tilePatch, quality int) error {
	codec = normalizeFrameCodec(codec, "jpeg")
	if a.usingQUIC() {
		return a.uploadFrameQUIC(agentFrameEnvelope{
			Type:        "frame",
			DeviceID:    a.deviceID,
			Mode:        "tiles",
			Codec:       codec,
			OriginX:     metrics.Left,
			OriginY:     metrics.Top,
			Width:       metrics.Width,
			Height:      metrics.Height,
			Quality:     quality,
			Tiles:       cloneTiles(tiles),
			PayloadSize: len(atlas),
		}, atlas)
	}
	if a.usingWSUpload() {
		return a.uploadFrameWS(agentFrameEnvelope{
			Type:        "frame",
			DeviceID:    a.deviceID,
			Mode:        "tiles",
			Codec:       codec,
			OriginX:     metrics.Left,
			OriginY:     metrics.Top,
			Width:       metrics.Width,
			Height:      metrics.Height,
			Quality:     quality,
			Tiles:       cloneTiles(tiles),
			PayloadSize: len(atlas),
		}, atlas)
	}
	endpoint := fmt.Sprintf(
		"%s/api/frame?deviceId=%s&mode=tiles&codec=%s&originX=%d&originY=%d&width=%d&height=%d&quality=%d",
		a.baseURL,
		url.QueryEscape(a.deviceID),
		url.QueryEscape(codec),
		metrics.Left,
		metrics.Top,
		metrics.Width,
		metrics.Height,
		quality,
	)

	meta, err := json.Marshal(tiles)
	if err != nil {
		return err
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	metaPart, err := writer.CreateFormField("tiles")
	if err != nil {
		return err
	}
	if _, err := metaPart.Write(meta); err != nil {
		return err
	}

	atlasName := "tiles.jpg"
	if codec == "bgra" {
		atlasName = "tiles.bgra"
	}
	filePart, err := writer.CreateFormFile("atlas", atlasName)
	if err != nil {
		return err
	}
	if _, err := filePart.Write(atlas); err != nil {
		return err
	}

	if err := writer.Close(); err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body.Bytes()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("tile upload failed: %s", strings.TrimSpace(string(raw)))
	}
	return nil
}

func (a *agent) uploadFrame(endpoint string, data []byte) error {
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "image/jpeg")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("upload failed: %s", strings.TrimSpace(string(raw)))
	}
	return nil
}

func (a *agent) encodeVideoFrame(current *image.RGBA, quality int) ([]byte, string, int, int, error) {
	codec := a.effectiveFrameCodec()
	if codec == "h264-mf" {
		frame := a.prepareAdaptiveVideoFrame(current)
		if data, err := a.tryEncodeH264MF(frame, quality); err != nil {
			a.logCodecFallback(codec, err)
		} else {
			return data, codec, frame.Bounds().Dx(), frame.Bounds().Dy(), nil
		}
	}
	data, err := encodeJPEGImage(current, quality)
	return data, "jpeg", current.Bounds().Dx(), current.Bounds().Dy(), err
}

func (a *agent) tryEncodeH264MF(current *image.RGBA, quality int) ([]byte, error) {
	if a == nil || a.mfSession == nil {
		return nil, fmt.Errorf("mf helper session is unavailable")
	}
	resp, payload, err := a.mfSession.exchange(mfSessionRequest{
		Type:    "encode",
		Codec:   "h264-mf",
		Format:  "rgba",
		Width:   current.Bounds().Dx(),
		Height:  current.Bounds().Dy(),
		Stride:  current.Stride,
		Quality: quality,
	}, current.Pix)
	if err != nil {
		return nil, err
	}
	if !resp.OK {
		if strings.TrimSpace(resp.Error) == "" {
			return nil, fmt.Errorf("h264-mf encode failed")
		}
		return nil, fmt.Errorf(resp.Error)
	}
	if len(payload) == 0 {
		return nil, fmt.Errorf("h264-mf helper returned empty payload")
	}
	return payload, nil
}

func (a *agent) prepareAdaptiveVideoFrame(current *image.RGBA) *image.RGBA {
	if current == nil {
		return current
	}
	scale := a.adaptiveVideoScale(current.Bounds())
	if scale >= 0.995 {
		return current
	}
	srcW := current.Bounds().Dx()
	srcH := current.Bounds().Dy()
	dstW := evenDimension(int(float64(srcW)*scale + 0.5))
	dstH := evenDimension(int(float64(srcH)*scale + 0.5))
	if dstW >= srcW || dstH >= srcH {
		return current
	}
	if dstW < 2 || dstH < 2 {
		return current
	}
	return resizeRGBAFast(current, dstW, dstH)
}

func (a *agent) adaptiveVideoScale(bounds image.Rectangle) float64 {
	if a == nil || bounds.Dx() <= 0 || bounds.Dy() <= 0 {
		return 1
	}
	if a.effectiveStreamMode() != "video" || normalizeFrameCodec(a.effectiveFrameCodec(), "") != "h264-mf" {
		return 1
	}
	if a.viewerKind != "controller" {
		return 1
	}

	pressure := a.uploadPressure()
	targetFrameBytes := a.targetBudgetBPS() * 0.90 / float64(max(a.targetStreamFPS(), 1))
	estimatedFrameBytes := a.txAvgBytes
	if estimatedFrameBytes <= 0 {
		estimatedFrameBytes = float64(bounds.Dx()*bounds.Dy()) * 0.020
	}

	scale := 1.0
	switch {
	case pressure >= 4.00:
		scale = 0.625
	case pressure >= 2.80:
		scale = 0.75
	case pressure >= 2.10:
		scale = 0.875
	}
	if targetFrameBytes > 0 && estimatedFrameBytes > targetFrameBytes {
		dynamicScale := math.Sqrt(targetFrameBytes / estimatedFrameBytes)
		if dynamicScale < scale {
			scale = dynamicScale
		}
	}
	if a.activeTransport != "quic" {
		if a.txFPS > 0 && a.txFPS < 2.0 {
			scale = minFloat(scale, 0.75)
		} else if a.txFPS > 0 && a.txFPS < 3.5 {
			scale = minFloat(scale, 0.875)
		}
	}
	if scale >= 0.995 {
		return 1
	}
	if scale < 0.50 {
		scale = 0.50
	}
	return snapVideoScale(scale)
}

func snapVideoScale(scale float64) float64 {
	steps := []float64{1.0, 0.875, 0.75, 0.625, 0.5}
	for _, step := range steps {
		if scale >= step-0.03 {
			return step
		}
	}
	return 0.5
}

func evenDimension(v int) int {
	if v < 2 {
		return 2
	}
	if v%2 != 0 {
		v--
	}
	if v < 2 {
		return 2
	}
	return v
}

func resizeRGBAFast(src *image.RGBA, dstW, dstH int) *image.RGBA {
	if src == nil {
		return nil
	}
	srcW := src.Bounds().Dx()
	srcH := src.Bounds().Dy()
	if dstW <= 0 || dstH <= 0 || srcW <= 0 || srcH <= 0 {
		return src
	}
	if dstW == srcW && dstH == srcH {
		return src
	}
	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	if dstW >= srcW && dstH >= srcH {
		for y := 0; y < dstH; y++ {
			sy := y * srcH / dstH
			srcRow := sy * src.Stride
			dstRow := y * dst.Stride
			for x := 0; x < dstW; x++ {
				sx := x * srcW / dstW
				srcPix := srcRow + sx*4
				dstPix := dstRow + x*4
				copy(dst.Pix[dstPix:dstPix+4], src.Pix[srcPix:srcPix+4])
			}
		}
		return dst
	}
	for y := 0; y < dstH; y++ {
		dstRow := y * dst.Stride
		fy := (float64(y)+0.5)*float64(srcH)/float64(dstH) - 0.5
		y0 := int(math.Floor(fy))
		if y0 < 0 {
			y0 = 0
		}
		if y0 > srcH-1 {
			y0 = srcH - 1
		}
		y1 := y0 + 1
		if y1 > srcH-1 {
			y1 = srcH - 1
		}
		wy := fy - float64(y0)
		if wy < 0 {
			wy = 0
		}
		if wy > 1 {
			wy = 1
		}
		srcRow0 := y0 * src.Stride
		srcRow1 := y1 * src.Stride
		for x := 0; x < dstW; x++ {
			fx := (float64(x)+0.5)*float64(srcW)/float64(dstW) - 0.5
			x0 := int(math.Floor(fx))
			if x0 < 0 {
				x0 = 0
			}
			if x0 > srcW-1 {
				x0 = srcW - 1
			}
			x1 := x0 + 1
			if x1 > srcW-1 {
				x1 = srcW - 1
			}
			wx := fx - float64(x0)
			if wx < 0 {
				wx = 0
			}
			if wx > 1 {
				wx = 1
			}
			p00 := srcRow0 + x0*4
			p10 := srcRow0 + x1*4
			p01 := srcRow1 + x0*4
			p11 := srcRow1 + x1*4
			dstPix := dstRow + x*4
			for c := 0; c < 4; c++ {
				top := float64(src.Pix[p00+c])*(1-wx) + float64(src.Pix[p10+c])*wx
				bottom := float64(src.Pix[p01+c])*(1-wx) + float64(src.Pix[p11+c])*wx
				value := top*(1-wy) + bottom*wy
				if value < 0 {
					value = 0
				}
				if value > 255 {
					value = 255
				}
				dst.Pix[dstPix+c] = byte(value + 0.5)
			}
		}
	}
	return dst
}

func (a *agent) captureLabel() string {
	if a == nil {
		return "native"
	}
	backend := normalizeCaptureBackend(a.captureBackend)
	if a.captureMaxWidth <= 0 && a.captureMaxHeight <= 0 {
		return backend + "/native"
	}
	switch {
	case a.captureMaxWidth > 0 && a.captureMaxHeight > 0:
		return fmt.Sprintf("%s/max-%dx%d", backend, a.captureMaxWidth, a.captureMaxHeight)
	case a.captureMaxWidth > 0:
		return fmt.Sprintf("%s/max-%dw", backend, a.captureMaxWidth)
	default:
		return fmt.Sprintf("%s/max-%dh", backend, a.captureMaxHeight)
	}
}

func (a *agent) captureMetrics(native desktopMetrics) desktopMetrics {
	if a == nil || native.Width <= 0 || native.Height <= 0 {
		return native
	}
	scale := 1.0
	if a.captureMaxWidth > 0 && native.Width > a.captureMaxWidth {
		scale = minFloat(scale, float64(a.captureMaxWidth)/float64(native.Width))
	}
	if a.captureMaxHeight > 0 && native.Height > a.captureMaxHeight {
		scale = minFloat(scale, float64(a.captureMaxHeight)/float64(native.Height))
	}
	if scale >= 0.995 {
		return native
	}
	metrics := native
	metrics.Width = max(1, int(float64(native.Width)*scale+0.5))
	metrics.Height = max(1, int(float64(native.Height)*scale+0.5))
	return metrics
}

func (a *agent) scaleCapturedFrame(current *image.RGBA, nativeMetrics, frameMetrics desktopMetrics) *image.RGBA {
	if current == nil {
		return nil
	}
	if nativeMetrics.Width == frameMetrics.Width && nativeMetrics.Height == frameMetrics.Height {
		return current
	}
	if frameMetrics.Width <= 0 || frameMetrics.Height <= 0 {
		return current
	}
	return resizeRGBAFast(current, frameMetrics.Width, frameMetrics.Height)
}

func scalePointBetweenSizes(x, y, srcW, srcH, dstW, dstH int) (int, int) {
	return scaleCoordBetweenSizes(x, srcW, dstW), scaleCoordBetweenSizes(y, srcH, dstH)
}

func scaleCoordBetweenSizes(coord, srcSize, dstSize int) int {
	if srcSize <= 0 || dstSize <= 0 {
		return coord
	}
	if srcSize == 1 || dstSize == 1 {
		return 0
	}
	if coord < 0 {
		coord = 0
	}
	if coord > srcSize-1 {
		coord = srcSize - 1
	}
	if srcSize == dstSize {
		return coord
	}
	scaled := int(math.Round(float64(coord) * float64(dstSize-1) / float64(srcSize-1)))
	return clamp(scaled, 0, max(dstSize-1, 0))
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func (a *agent) logCodecFallback(codec string, err error) {
	if a == nil || err == nil {
		return
	}
	if time.Since(a.lastCodecLog) < 5*time.Second {
		return
	}
	a.lastCodecLog = time.Now()
	log.Printf("[agent][codec] requested=%s fallback=jpeg reason=%v", codec, err)
}

func (a *agent) usingQUIC() bool {
	return a.activeTransport == "quic" && a.quicConn != nil && a.quicStream != nil && a.quicDecoder != nil
}

func (a *agent) usingWSUpload() bool {
	return a.activeTransport == "ws" && a.wsFrameConn != nil
}

func (a *agent) connectQUIC() error {
	conn, err := quic.DialAddr(context.Background(), a.quicEndpoint(), &tls.Config{
		InsecureSkipVerify: true,
		NextProtos:         []string{quicNextProto},
	}, &quic.Config{
		EnableDatagrams: true,
		KeepAlivePeriod: 5 * time.Second,
		MaxIdleTimeout:  30 * time.Second,
	})
	if err != nil {
		return err
	}

	state := conn.ConnectionState()
	if !state.SupportsDatagrams.Remote {
		conn.CloseWithError(1, "quic datagrams are not supported")
		return fmt.Errorf("quic datagrams are not supported")
	}

	stream, err := conn.OpenStreamSync(context.Background())
	if err != nil {
		conn.CloseWithError(1, "open stream failed")
		return err
	}

	if err := stream.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		stream.Close()
		conn.CloseWithError(1, "set write deadline failed")
		return err
	}
	if err := json.NewEncoder(stream).Encode(wsClientMessage{
		Type:     "hello",
		DeviceID: a.deviceID,
		Role:     "agent",
	}); err != nil {
		stream.Close()
		conn.CloseWithError(1, "hello failed")
		return err
	}
	if err := stream.SetWriteDeadline(time.Time{}); err != nil {
		stream.Close()
		conn.CloseWithError(1, "clear write deadline failed")
		return err
	}

	a.quicConn = conn
	a.quicStream = stream
	a.quicDecoder = json.NewDecoder(stream)
	return nil
}

func (a *agent) closeQUIC() {
	if a.quicStream != nil {
		_ = a.quicStream.Close()
		a.quicStream = nil
	}
	if a.quicConn != nil {
		_ = a.quicConn.CloseWithError(0, "")
		a.quicConn = nil
	}
	a.quicDecoder = nil
}

func (a *agent) wsUploadURL() string {
	base, err := url.Parse(a.baseURL)
	if err != nil {
		return a.baseURL
	}
	switch strings.ToLower(base.Scheme) {
	case "https":
		base.Scheme = "wss"
	default:
		base.Scheme = "ws"
	}
	base.Path = "/ws/agent"
	base.RawQuery = ""
	base.Fragment = ""
	return base.String()
}

func (a *agent) connectWSUpload() error {
	a.closeWSUpload()
	dialer := websocket.Dialer{
		HandshakeTimeout:  10 * time.Second,
		EnableCompression: false,
		NetDialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			dialer := &net.Dialer{KeepAlive: 30 * time.Second, Timeout: 10 * time.Second}
			conn, err := dialer.DialContext(ctx, network, address)
			if err != nil {
				return nil, err
			}
			configureClientTCP(conn)
			return conn, nil
		},
	}
	conn, _, err := dialer.Dial(a.wsUploadURL(), nil)
	if err != nil {
		return err
	}
	configureClientTCP(conn.UnderlyingConn())
	conn.EnableWriteCompression(false)
	if err := conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		_ = conn.Close()
		return err
	}
	if err := conn.WriteJSON(wsClientMessage{
		Type:     "hello",
		DeviceID: a.deviceID,
		Role:     "agent",
	}); err != nil {
		_ = conn.Close()
		return err
	}
	if err := conn.SetWriteDeadline(time.Time{}); err != nil {
		_ = conn.Close()
		return err
	}
	a.wsFrameConn = conn
	go a.consumeWSUpload()
	return nil
}

func (a *agent) consumeWSUpload() {
	if a == nil || a.wsFrameConn == nil {
		return
	}
	conn := a.wsFrameConn
	conn.SetReadLimit(8 << 10)
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			a.wsFrameWriteMu.Lock()
			if a.wsFrameConn == conn {
				a.wsFrameConn = nil
				a.activeTransport = "http"
			}
			a.wsFrameWriteMu.Unlock()
			_ = conn.Close()
			return
		}
	}
}

func (a *agent) closeWSUpload() {
	a.wsFrameWriteMu.Lock()
	conn := a.wsFrameConn
	a.wsFrameConn = nil
	a.wsFrameWriteMu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
}

func (a *agent) sendAgentStream(msg agentStreamMessage) error {
	if !a.usingQUIC() {
		return fmt.Errorf("quic stream is unavailable")
	}
	if msg.DeviceID == "" {
		msg.DeviceID = a.deviceID
	}
	if err := a.quicStream.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	defer a.quicStream.SetWriteDeadline(time.Time{})
	return json.NewEncoder(a.quicStream).Encode(msg)
}

func (a *agent) fetchCommandsQUIC(cursor cursorSnapshot) (commandResponse, error) {
	var respData commandResponse
	if err := a.sendAgentStream(agentStreamMessage{
		Type:     "poll",
		DeviceID: a.deviceID,
		Poll: &agentPollRequest{
			DeviceID:      a.deviceID,
			After:         a.lastSeq,
			CursorX:       cursor.X,
			CursorY:       cursor.Y,
			CursorVisible: cursor.Visible,
			CursorKind:    cursor.Kind,
		},
	}); err != nil {
		return respData, err
	}

	if err := a.quicStream.SetReadDeadline(time.Now().Add(8 * time.Second)); err != nil {
		return respData, err
	}
	defer a.quicStream.SetReadDeadline(time.Time{})

	for {
		var msg agentStreamMessage
		if err := a.quicDecoder.Decode(&msg); err != nil {
			return respData, err
		}
		switch strings.TrimSpace(strings.ToLower(msg.Type)) {
		case "commands":
			if msg.Commands == nil {
				return respData, fmt.Errorf("quic commands response is empty")
			}
			return *msg.Commands, nil
		case "error":
			return respData, fmt.Errorf("quic agent error: %s", strings.TrimSpace(msg.Error))
		}
	}
}

func (a *agent) uploadFrameQUIC(header agentFrameEnvelope, payload []byte) error {
	if !a.usingQUIC() {
		return fmt.Errorf("quic datagram stream is unavailable")
	}
	packet, err := encodeAgentFramePacket(header, payload)
	if err != nil {
		return err
	}
	a.quicUploadID++
	chunks, err := splitQUICUploadDatagrams(a.quicUploadID, packet)
	if err != nil {
		return err
	}
	for _, chunk := range chunks {
		if err := a.quicConn.SendDatagram(chunk); err != nil {
			return err
		}
	}
	return nil
}

func (a *agent) uploadFrameWS(header agentFrameEnvelope, payload []byte) error {
	if !a.usingWSUpload() {
		return fmt.Errorf("ws frame uplink is unavailable")
	}
	packet, err := encodeAgentFramePacket(header, payload)
	if err != nil {
		return err
	}
	a.wsFrameWriteMu.Lock()
	defer a.wsFrameWriteMu.Unlock()
	if a.wsFrameConn == nil {
		return fmt.Errorf("ws frame uplink is unavailable")
	}
	if err := a.wsFrameConn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	defer a.wsFrameConn.SetWriteDeadline(time.Time{})
	if err := a.wsFrameConn.WriteMessage(websocket.BinaryMessage, packet); err != nil {
		_ = a.wsFrameConn.Close()
		a.wsFrameConn = nil
		a.activeTransport = "http"
		return err
	}
	return nil
}

func encodeAgentFramePacket(header agentFrameEnvelope, payload []byte) ([]byte, error) {
	headerJSON, err := json.Marshal(header)
	if err != nil {
		return nil, err
	}
	packet := make([]byte, 4+len(headerJSON)+len(payload))
	binary.BigEndian.PutUint32(packet[:4], uint32(len(headerJSON)))
	copy(packet[4:], headerJSON)
	copy(packet[4+len(headerJSON):], payload)
	return packet, nil
}

func splitQUICUploadDatagrams(packetID int64, packet []byte) ([][]byte, error) {
	if len(packet) == 0 {
		return nil, nil
	}
	chunkCount := (len(packet) + quicDatagramChunkSize - 1) / quicDatagramChunkSize
	if chunkCount <= 0 || chunkCount > 0xFFFF {
		return nil, fmt.Errorf("frame is too large for quic datagrams")
	}

	chunks := make([][]byte, 0, chunkCount)
	for index := 0; index < chunkCount; index++ {
		start := index * quicDatagramChunkSize
		end := start + quicDatagramChunkSize
		if end > len(packet) {
			end = len(packet)
		}
		datagram := make([]byte, quicDatagramHeaderSize+(end-start))
		datagram[0] = quicDatagramMagicUplink
		binary.BigEndian.PutUint64(datagram[1:9], uint64(packetID))
		binary.BigEndian.PutUint16(datagram[9:11], uint16(index))
		binary.BigEndian.PutUint16(datagram[11:13], uint16(chunkCount))
		copy(datagram[quicDatagramHeaderSize:], packet[start:end])
		chunks = append(chunks, datagram)
	}
	return chunks, nil
}

func (a *agent) quicEndpoint() string {
	base, err := url.Parse(a.baseURL)
	if err != nil {
		return a.baseURL
	}
	return base.Host
}

func readClipboardText() (string, error) {
	ok, _, _ := procIsClipboardFormatAvail.Call(cfUnicodeText)
	if ok == 0 {
		return "", nil
	}
	if err := openClipboardWithRetry(); err != nil {
		return "", err
	}
	defer closeClipboardAndUnlock()

	handle, _, _ := procGetClipboardData.Call(cfUnicodeText)
	if handle == 0 {
		return "", nil
	}

	ptr, _, _ := procGlobalLock.Call(handle)
	if ptr == 0 {
		return "", fmt.Errorf("GlobalLock failed")
	}
	defer procGlobalUnlock.Call(handle)

	chars := make([]uint16, 0, 256)
	for index := uintptr(0); ; index++ {
		ch := *(*uint16)(unsafe.Pointer(ptr + index*2))
		if ch == 0 {
			break
		}
		chars = append(chars, ch)
		if len(chars) > 1<<20 {
			break
		}
	}

	return string(utf16.Decode(chars)), nil
}

func clearClipboard() error {
	if err := openClipboardWithRetry(); err != nil {
		return err
	}
	defer closeClipboardAndUnlock()
	if emptied, _, _ := procEmptyClipboard.Call(); emptied == 0 {
		return fmt.Errorf("EmptyClipboard failed")
	}
	return nil
}

func writeClipboardText(text string) error {
	utf := utf16.Encode([]rune(text + "\x00"))
	size := uintptr(len(utf) * 2)

	if err := openClipboardWithRetry(); err != nil {
		return err
	}
	defer closeClipboardAndUnlock()

	if emptied, _, _ := procEmptyClipboard.Call(); emptied == 0 {
		return fmt.Errorf("EmptyClipboard failed")
	}

	handle, _, _ := procGlobalAlloc.Call(gmemMoveable, size)
	if handle == 0 {
		return fmt.Errorf("GlobalAlloc failed")
	}

	ptr, _, _ := procGlobalLock.Call(handle)
	if ptr == 0 {
		procGlobalFree.Call(handle)
		return fmt.Errorf("GlobalLock failed")
	}

	dst := unsafe.Slice((*uint16)(unsafe.Pointer(ptr)), len(utf))
	copy(dst, utf)
	procGlobalUnlock.Call(handle)

	if set, _, _ := procSetClipboardData.Call(cfUnicodeText, handle); set == 0 {
		procGlobalFree.Call(handle)
		return fmt.Errorf("SetClipboardData failed")
	}
	return nil
}

func readClipboardState() (clipboardState, error) {
	if filesState, ok, err := readClipboardFilesState(); ok || err != nil {
		return filesState, err
	}
	if imageState, ok, err := readClipboardImageState(); ok || err != nil {
		return imageState, err
	}
	text, err := readClipboardText()
	if err != nil {
		return clipboardState{}, err
	}
	return clipboardState{Kind: "text", Text: text}, nil
}

func readClipboardFilesState() (clipboardState, bool, error) {
	ok, _, _ := procIsClipboardFormatAvail.Call(cfHDROP)
	if ok == 0 {
		return clipboardState{}, false, nil
	}
	if err := openClipboardWithRetry(); err != nil {
		return clipboardState{}, true, err
	}
	defer closeClipboardAndUnlock()

	handle, _, _ := procGetClipboardData.Call(cfHDROP)
	if handle == 0 {
		return clipboardState{}, false, nil
	}
	paths := queryClipboardFilePaths(handle)
	if len(paths) == 0 {
		return clipboardState{}, false, nil
	}
	return clipboardState{Kind: "files", Paths: paths}, true, nil
}

func queryClipboardFilePaths(hDrop uintptr) []string {
	count, _, _ := procDragQueryFileW.Call(hDrop, 0xFFFFFFFF, 0, 0)
	if count == 0 {
		return nil
	}
	paths := make([]string, 0, int(count))
	for i := uint32(0); i < uint32(count); i++ {
		length, _, _ := procDragQueryFileW.Call(hDrop, uintptr(i), 0, 0)
		if length == 0 {
			continue
		}
		buf := make([]uint16, int(length)+1)
		procDragQueryFileW.Call(hDrop, uintptr(i), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
		path := strings.TrimSpace(syscall.UTF16ToString(buf))
		if path != "" {
			paths = append(paths, path)
		}
	}
	return paths
}

func writeClipboardState(state clipboardState) error {
	state = normalizeClipboardState(state)
	if state.Kind == "files" {
		return nil
	}
	if state.Kind == "image" && len(state.Image) > 0 {
		return writeClipboardImagePNG(state.Image)
	}
	if state.Kind != "text" {
		return nil
	}
	return writeClipboardText(state.Text)
}

func writeClipboardFiles(paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	utf := make([]uint16, 0, 256)
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
		utf = append(utf, syscall.StringToUTF16(path)...)
	}
	if len(utf) == 0 {
		return nil
	}
	utf = append(utf, 0)

	headerSize := int(unsafe.Sizeof(dropFiles{}))
	size := uintptr(headerSize + len(utf)*2)
	handle, _, _ := procGlobalAlloc.Call(gmemMoveable, size)
	if handle == 0 {
		return fmt.Errorf("GlobalAlloc failed")
	}
	ptr, _, _ := procGlobalLock.Call(handle)
	if ptr == 0 {
		procGlobalFree.Call(handle)
		return fmt.Errorf("GlobalLock failed")
	}
	header := (*dropFiles)(unsafe.Pointer(ptr))
	*header = dropFiles{
		PFiles: uint32(headerSize),
		FWide:  1,
	}
	dst := unsafe.Slice((*uint16)(unsafe.Pointer(ptr+uintptr(headerSize))), len(utf))
	copy(dst, utf)
	procGlobalUnlock.Call(handle)

	if opened, _, _ := procOpenClipboard.Call(0); opened == 0 {
		procGlobalFree.Call(handle)
		return fmt.Errorf("OpenClipboard failed")
	}
	defer procCloseClipboard.Call()
	if emptied, _, _ := procEmptyClipboard.Call(); emptied == 0 {
		procGlobalFree.Call(handle)
		return fmt.Errorf("EmptyClipboard failed")
	}
	if set, _, _ := procSetClipboardData.Call(cfHDROP, handle); set == 0 {
		procGlobalFree.Call(handle)
		return fmt.Errorf("SetClipboardData failed")
	}
	return nil
}

func bitmapToDIB(hBitmap uintptr) ([]byte, error) {
	var bm bitmapObject
	if ret, _, _ := procGetObjectW.Call(hBitmap, unsafe.Sizeof(bm), uintptr(unsafe.Pointer(&bm))); ret == 0 {
		return nil, fmt.Errorf("GetObject failed")
	}

	width := int(bm.Width)
	height := int(bm.Height)
	bitCount := int(bm.BitsPixel)
	if bitCount != 24 && bitCount != 32 {
		return nil, fmt.Errorf("unsupported bitmap bit count: %d", bitCount)
	}

	rowStride := ((width*bitCount + 31) / 32) * 4
	pixelSize := rowStride * height
	dibSize := 40 + pixelSize

	dib := make([]byte, dibSize)

	binary.LittleEndian.PutUint32(dib[0:4], 40)
	binary.LittleEndian.PutUint32(dib[4:8], uint32(width))
	binary.LittleEndian.PutUint32(dib[8:12], uint32(height))
	binary.LittleEndian.PutUint16(dib[12:14], 1)
	binary.LittleEndian.PutUint16(dib[14:16], uint16(bitCount))
	binary.LittleEndian.PutUint32(dib[16:20], biRGB)
	binary.LittleEndian.PutUint32(dib[20:24], uint32(pixelSize))
	binary.LittleEndian.PutUint32(dib[24:28], 0)
	binary.LittleEndian.PutUint32(dib[28:32], 0)
	binary.LittleEndian.PutUint32(dib[32:36], 0)
	binary.LittleEndian.PutUint32(dib[36:40], 0)

	screenDC, _, _ := procGetDC.Call(0)
	if screenDC == 0 {
		return nil, fmt.Errorf("GetDC failed")
	}
	defer procReleaseDC.Call(0, screenDC)

	memDC, _, _ := procCreateCompatibleDC.Call(screenDC)
	if memDC == 0 {
		return nil, fmt.Errorf("CreateCompatibleDC failed")
	}
	defer procDeleteDC.Call(memDC)

	oldObj, _, _ := procSelectObject.Call(memDC, hBitmap)
	if oldObj == 0 {
		return nil, fmt.Errorf("SelectObject failed")
	}
	defer procSelectObject.Call(memDC, oldObj)

	var bmi bitmapInfo
	bmi.Header.Size = uint32(unsafe.Sizeof(bmi.Header))
	bmi.Header.Width = int32(width)
	bmi.Header.Height = int32(height)
	bmi.Header.Planes = 1
	bmi.Header.BitCount = uint16(bitCount)
	bmi.Header.Compression = biRGB

	ret, _, _ := procGetDIBits.Call(
		memDC,
		hBitmap,
		0,
		uintptr(height),
		uintptr(unsafe.Pointer(&dib[40])),
		uintptr(unsafe.Pointer(&bmi)),
		0,
	)
	if ret == 0 {
		return nil, fmt.Errorf("GetDIBits failed")
	}

	return dib, nil
}

func readClipboardImageState() (clipboardState, bool, error) {
	pngFormat := pngClipboardFormatID()
	pngAvailable := uintptr(0)
	if pngFormat != 0 {
		pngAvailable, _, _ = procIsClipboardFormatAvail.Call(pngFormat)
	}
	dibAvailable, _, _ := procIsClipboardFormatAvail.Call(cfDIB)
	dibV5Available, _, _ := procIsClipboardFormatAvail.Call(cfDIBV5)
	bitmapAvailable, _, _ := procIsClipboardFormatAvail.Call(cfBitmap)
	if pngAvailable == 0 && dibAvailable == 0 && dibV5Available == 0 && bitmapAvailable == 0 {
		return clipboardState{}, false, nil
	}
	if err := openClipboardWithRetry(); err != nil {
		return clipboardState{}, true, err
	}
	defer closeClipboardAndUnlock()

	var lastErr error
	if pngAvailable != 0 {
		handle, _, _ := procGetClipboardData.Call(pngFormat)
		if handle != 0 {
			data, err := clipboardGlobalBytes(handle, maxClipboardImageBytes)
			if err != nil {
				return clipboardState{}, true, err
			}
			state, err := clipboardImageStateFromPNG(data)
			if err == nil {
				return state, true, nil
			}
			lastErr = err
		}
	}

	for _, format := range []uintptr{cfDIB, cfDIBV5} {
		available := dibAvailable
		if format == cfDIBV5 {
			available = dibV5Available
		}
		if available == 0 {
			continue
		}
		handle, _, _ := procGetClipboardData.Call(format)
		if handle == 0 {
			continue
		}
		dib, err := clipboardGlobalBytes(handle, 64<<20)
		if err != nil {
			return clipboardState{}, true, err
		}
		state, err := clipboardImageStateFromDIB(dib)
		if err == nil {
			return state, true, nil
		}
		lastErr = err
	}

	if bitmapAvailable != 0 {
		handle, _, _ := procGetClipboardData.Call(cfBitmap)
		if handle != 0 {
			dib, err := bitmapToDIB(handle)
			if err == nil {
				state, err := clipboardImageStateFromDIB(dib)
				if err == nil {
					return state, true, nil
				}
				lastErr = err
			}
		}
	}

	if lastErr != nil {
		return clipboardState{}, true, lastErr
	}
	return clipboardState{}, false, nil
}

func pngClipboardFormatID() uintptr {
	pngClipboardOnce.Do(func() {
		name := syscall.StringToUTF16Ptr("PNG")
		format, _, _ := procRegisterClipboardFormatW.Call(uintptr(unsafe.Pointer(name)))
		pngClipboardFormat = format
	})
	return pngClipboardFormat
}

func clipboardGlobalBytes(handle uintptr, maxSize uintptr) ([]byte, error) {
	size, _, _ := procGlobalSize.Call(handle)
	if size == 0 || (maxSize > 0 && size > maxSize) {
		return nil, fmt.Errorf("clipboard image size is invalid")
	}
	ptr, _, _ := procGlobalLock.Call(handle)
	if ptr == 0 {
		return nil, fmt.Errorf("GlobalLock failed")
	}
	defer procGlobalUnlock.Call(handle)
	return append([]byte(nil), unsafe.Slice((*byte)(unsafe.Pointer(ptr)), int(size))...), nil
}

func clipboardImageStateFromPNG(data []byte) (clipboardState, error) {
	if len(data) == 0 || len(data) > maxClipboardImageBytes {
		return clipboardState{}, fmt.Errorf("clipboard image too large: %s", formatBytes(int64(len(data))))
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return clipboardState{}, err
	}
	return clipboardState{
		Kind:      "image",
		Image:     append([]byte(nil), data...),
		ImageType: "png",
		ImageW:    cfg.Width,
		ImageH:    cfg.Height,
	}, nil
}

func clipboardImageStateFromDIB(dib []byte) (clipboardState, error) {
	img, err := imageFromDIB(dib)
	if err != nil {
		return clipboardState{}, err
	}
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, img); err != nil {
		return clipboardState{}, err
	}
	if pngBuf.Len() > maxClipboardImageBytes {
		return clipboardState{}, fmt.Errorf("clipboard image too large: %s", formatBytes(int64(pngBuf.Len())))
	}
	bounds := img.Bounds()
	return clipboardState{
		Kind:      "image",
		Image:     pngBuf.Bytes(),
		ImageType: "png",
		ImageW:    bounds.Dx(),
		ImageH:    bounds.Dy(),
	}, nil
}

func writeClipboardImagePNG(data []byte) error {
	if len(data) == 0 {
		return writeClipboardText("")
	}
	if len(data) > maxClipboardImageBytes {
		return fmt.Errorf("clipboard image too large: %s", formatBytes(int64(len(data))))
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return err
	}
	dib := dibFromImage(img)
	dibHandle, err := globalAllocBytes(dib)
	if err != nil {
		return err
	}
	pngHandle, err := globalAllocBytes(data)
	if err != nil {
		procGlobalFree.Call(dibHandle)
		return err
	}
	pngFormat := pngClipboardFormatID()

	if opened, _, _ := procOpenClipboard.Call(0); opened == 0 {
		procGlobalFree.Call(dibHandle)
		procGlobalFree.Call(pngHandle)
		return fmt.Errorf("OpenClipboard failed")
	}
	defer procCloseClipboard.Call()
	if emptied, _, _ := procEmptyClipboard.Call(); emptied == 0 {
		procGlobalFree.Call(dibHandle)
		procGlobalFree.Call(pngHandle)
		return fmt.Errorf("EmptyClipboard failed")
	}
	if set, _, _ := procSetClipboardData.Call(cfDIB, dibHandle); set == 0 {
		procGlobalFree.Call(dibHandle)
		procGlobalFree.Call(pngHandle)
		return fmt.Errorf("SetClipboardData failed")
	}
	if pngFormat != 0 {
		if set, _, _ := procSetClipboardData.Call(pngFormat, pngHandle); set != 0 {
			pngHandle = 0
		}
	}
	if pngHandle != 0 {
		procGlobalFree.Call(pngHandle)
	}
	return nil
}

func globalAllocBytes(data []byte) (uintptr, error) {
	handle, _, _ := procGlobalAlloc.Call(gmemMoveable, uintptr(len(data)))
	if handle == 0 {
		return 0, fmt.Errorf("GlobalAlloc failed")
	}
	ptr, _, _ := procGlobalLock.Call(handle)
	if ptr == 0 {
		procGlobalFree.Call(handle)
		return 0, fmt.Errorf("GlobalLock failed")
	}
	copy(unsafe.Slice((*byte)(unsafe.Pointer(ptr)), len(data)), data)
	procGlobalUnlock.Call(handle)
	return handle, nil
}

func imageFromDIB(dib []byte) (*image.RGBA, error) {
	if len(dib) < 40 {
		return nil, fmt.Errorf("clipboard DIB is too small")
	}
	headerSize := int(binary.LittleEndian.Uint32(dib[0:4]))
	if headerSize < 40 || headerSize > len(dib) {
		return nil, fmt.Errorf("clipboard DIB header is invalid")
	}
	width := int(int32(binary.LittleEndian.Uint32(dib[4:8])))
	rawHeight := int(int32(binary.LittleEndian.Uint32(dib[8:12])))
	if width <= 0 || rawHeight == 0 {
		return nil, fmt.Errorf("clipboard DIB dimensions are invalid")
	}
	height := rawHeight
	topDown := false
	if height < 0 {
		height = -height
		topDown = true
	}
	planes := binary.LittleEndian.Uint16(dib[12:14])
	bitCount := int(binary.LittleEndian.Uint16(dib[14:16]))
	compression := binary.LittleEndian.Uint32(dib[16:20])
	if planes != 1 || (bitCount != 24 && bitCount != 32) {
		return nil, fmt.Errorf("clipboard DIB format is unsupported")
	}
	if compression != biRGB && !(bitCount == 32 && compression == biBitfields && dibHasCompatibleBitfields(dib)) {
		return nil, fmt.Errorf("clipboard DIB format is unsupported")
	}
	pixelOffset := headerSize
	rowStride := ((width*bitCount + 31) / 32) * 4
	if pixelOffset+rowStride*height > len(dib) {
		return nil, fmt.Errorf("clipboard DIB pixel data is incomplete")
	}
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	hasAlpha := false
	if bitCount == 32 {
		for y := 0; y < height && !hasAlpha; y++ {
			row := dib[pixelOffset+y*rowStride:]
			for x := 0; x < width; x++ {
				if row[x*4+3] != 0 {
					hasAlpha = true
					break
				}
			}
		}
	}
	for y := 0; y < height; y++ {
		srcY := y
		if !topDown {
			srcY = height - 1 - y
		}
		src := dib[pixelOffset+srcY*rowStride:]
		dst := img.Pix[y*img.Stride:]
		for x := 0; x < width; x++ {
			if bitCount == 32 {
				s := x * 4
				d := x * 4
				dst[d+0] = src[s+2]
				dst[d+1] = src[s+1]
				dst[d+2] = src[s+0]
				if hasAlpha {
					dst[d+3] = src[s+3]
				} else {
					dst[d+3] = 0xFF
				}
			} else {
				s := x * 3
				d := x * 4
				dst[d+0] = src[s+2]
				dst[d+1] = src[s+1]
				dst[d+2] = src[s+0]
				dst[d+3] = 0xFF
			}
		}
	}
	return img, nil
}

func dibHasCompatibleBitfields(dib []byte) bool {
	if len(dib) < 56 {
		return false
	}
	redMask := binary.LittleEndian.Uint32(dib[40:44])
	greenMask := binary.LittleEndian.Uint32(dib[44:48])
	blueMask := binary.LittleEndian.Uint32(dib[48:52])
	alphaMask := binary.LittleEndian.Uint32(dib[52:56])
	return redMask == 0x00ff0000 && greenMask == 0x0000ff00 && blueMask == 0x000000ff && (alphaMask == 0 || alphaMask == 0xff000000)
}

func dibFromImage(img image.Image) []byte {
	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	rowStride := width * 4
	dib := make([]byte, 40+rowStride*height)
	binary.LittleEndian.PutUint32(dib[0:4], 40)
	binary.LittleEndian.PutUint32(dib[4:8], uint32(int32(width)))
	binary.LittleEndian.PutUint32(dib[8:12], uint32(int32(height)))
	binary.LittleEndian.PutUint16(dib[12:14], 1)
	binary.LittleEndian.PutUint16(dib[14:16], 32)
	binary.LittleEndian.PutUint32(dib[16:20], biRGB)
	binary.LittleEndian.PutUint32(dib[20:24], uint32(rowStride*height))
	for y := 0; y < height; y++ {
		dstY := height - 1 - y
		dst := dib[40+dstY*rowStride:]
		for x := 0; x < width; x++ {
			r, g, b, a := img.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
			d := x * 4
			dst[d+0] = byte(b >> 8)
			dst[d+1] = byte(g >> 8)
			dst[d+2] = byte(r >> 8)
			dst[d+3] = byte(a >> 8)
		}
	}
	return dib
}

func normalizeClipboardState(state clipboardState) clipboardState {
	state.Kind = strings.ToLower(strings.TrimSpace(state.Kind))
	if state.Kind == "" {
		if len(state.Image) > 0 {
			state.Kind = "image"
		} else {
			state.Kind = "text"
		}
	}
	if state.Kind == "image" {
		state.Text = ""
		state.Files = nil
		state.Paths = nil
		if state.ImageType == "" {
			state.ImageType = "png"
		} else {
			state.ImageType = strings.ToLower(strings.TrimSpace(state.ImageType))
		}
		return state
	}
	if state.Kind == "files" {
		state.Text = ""
		state.Image = nil
		state.ImageType = ""
		state.ImageW = 0
		state.ImageH = 0
		return state
	}
	state.Kind = "text"
	state.Image = nil
	state.ImageType = ""
	state.ImageW = 0
	state.ImageH = 0
	state.Files = nil
	state.Paths = nil
	return state
}

func clipboardSignature(state clipboardState) string {
	state = normalizeClipboardState(state)
	if state.Kind == "image" {
		sum := sha256.Sum256(state.Image)
		return fmt.Sprintf("image:%s:%dx%d:%x", state.ImageType, state.ImageW, state.ImageH, sum)
	}
	if state.Kind == "files" {
		var b strings.Builder
		b.WriteString("files:")
		for _, path := range state.Paths {
			cleanPath := filepath.Clean(strings.TrimSpace(path))
			if cleanPath == "." || cleanPath == "" {
				continue
			}
			var size int64
			var mod int64
			if info, err := os.Stat(cleanPath); err == nil && !info.IsDir() {
				size = info.Size()
				mod = info.ModTime().UnixNano()
			}
			fmt.Fprintf(&b, "path=%s|%d|%d\n", strings.ToLower(cleanPath), size, mod)
		}
		for _, file := range state.Files {
			fmt.Fprintf(&b, "ref=%s|%s|%d\n", file.ID, file.Name, file.Size)
		}
		return b.String()
	}
	return "text:" + state.Text
}

func clipboardSequenceNumber() uint32 {
	seq, _, _ := procGetClipboardSequenceNumber.Call()
	return uint32(seq)
}

func (a *agent) captureScreenRGBA() (*image.RGBA, desktopMetrics, error) {
	metrics := a.getActiveMonitor()
	if metrics.Width <= 0 || metrics.Height <= 0 {
		return nil, desktopMetrics{}, fmt.Errorf("invalid screen size: %dx%d", metrics.Width, metrics.Height)
	}
	if !inputDesktopAvailable() {
		return nil, metrics, fmt.Errorf("%w: input desktop is not available", errDesktopUnavailable)
	}

	if a != nil {
		if a.captureSession == nil {
			a.captureSession = &screenCaptureSession{}
		}
		img, err := a.captureSession.capture(metrics)
		if err == nil {
			return img, metrics, nil
		}
		a.captureSession.close()
		return nil, metrics, err
	}

	img, err := captureScreenRGBARaw(metrics)
	if err == nil {
		return img, metrics, nil
	}

	return nil, metrics, err
}

func (a *agent) captureScreenBGRA() ([]byte, desktopMetrics, error) {
	frame, err := a.captureDesktopBGRA()
	if err != nil {
		return nil, frame.Metrics, err
	}
	return frame.Pixels, frame.Metrics, nil
}

func (a *agent) captureDesktopBGRA() (bgraCaptureFrame, error) {
	metrics := a.getActiveMonitor()
	if metrics.Width <= 0 || metrics.Height <= 0 {
		return bgraCaptureFrame{}, fmt.Errorf("invalid screen size: %dx%d", metrics.Width, metrics.Height)
	}
	if !inputDesktopAvailable() {
		return bgraCaptureFrame{Metrics: metrics}, fmt.Errorf("%w: input desktop is not available", errDesktopUnavailable)
	}
	if a == nil {
		return bgraCaptureFrame{Metrics: metrics}, fmt.Errorf("agent is nil")
	}
	backend := normalizeCaptureBackend(a.captureBackend)
	canTryDXGI := backend != "gdi" && a.captureMaxWidth <= 0 && a.captureMaxHeight <= 0
	if backend == "auto" && !a.dxgiRetryAt.IsZero() && time.Now().Before(a.dxgiRetryAt) {
		canTryDXGI = false
	}
	if canTryDXGI {
		frame, err := a.captureDXGIBGRA(metrics)
		if err == nil {
			scrubCapturedCursorBGRA(frame.Pixels, metrics)
			a.dxgiRetryAt = time.Time{}
			return frame, nil
		}
		if a.dxgiCapture != nil {
			a.dxgiCapture.close()
			a.dxgiCapture = nil
		}
		if backend == "dxgi" {
			return bgraCaptureFrame{Metrics: metrics}, err
		}
		a.dxgiRetryAt = time.Now().Add(30 * time.Second)
		if !a.dxgiWarned {
			log.Printf("[agent][capture] dxgi unavailable, fallback to gdi: %v", err)
			a.dxgiWarned = true
		}
	}
	if a.captureSession == nil {
		a.captureSession = &screenCaptureSession{}
	}
	pixels, err := a.captureSession.captureBGRA(metrics)
	if err != nil {
		a.captureSession.close()
		return bgraCaptureFrame{Metrics: metrics}, err
	}
	return bgraCaptureFrame{Pixels: pixels, Metrics: metrics, Backend: "gdi"}, nil
}

func (a *agent) captureDXGIBGRA(metrics desktopMetrics) (bgraCaptureFrame, error) {
	if a == nil {
		return bgraCaptureFrame{Metrics: metrics}, fmt.Errorf("agent is nil")
	}
	if a.dxgiCapture == nil || !sameDesktopMetrics(a.dxgiCapture.metrics, metrics) {
		if a.dxgiCapture != nil {
			a.dxgiCapture.close()
		}
		session, err := newDXGICaptureSession(metrics)
		if err != nil {
			return bgraCaptureFrame{Metrics: metrics}, err
		}
		a.dxgiCapture = session
		log.Printf("[agent][capture] backend=dxgi outputs=%d desktop=%dx%d origin=(%d,%d)", len(session.outputs), metrics.Width, metrics.Height, metrics.Left, metrics.Top)
	}
	return a.dxgiCapture.capture(metrics)
}

func scrubCapturedCursorBGRA(frame []byte, metrics desktopMetrics) {
	if metrics.Width <= 0 || metrics.Height <= 0 || len(frame) < metrics.Width*metrics.Height*4 {
		return
	}
	rect, ok := currentCursorFrameRect(metrics, 3)
	if !ok {
		return
	}
	clean, ok := captureScreenRectBGRA(metrics, rect)
	if !ok || len(clean) == 0 {
		return
	}
	copySmallBGRARegion(frame, metrics.Width, metrics.Height, rect, clean)
}

func currentCursorFrameRect(metrics desktopMetrics, padding int) (image.Rectangle, bool) {
	ci := cursorInfo{Size: uint32(unsafe.Sizeof(cursorInfo{}))}
	if ret, _, _ := procGetCursorInfo.Call(uintptr(unsafe.Pointer(&ci))); ret == 0 {
		return image.Rectangle{}, false
	}
	if ci.Flags&cursorShowing == 0 || ci.Cursor == 0 {
		return image.Rectangle{}, false
	}
	width, height, hotX, hotY, ok := cursorBitmapGeometry(ci.Cursor)
	if !ok || width <= 0 || height <= 0 {
		width, height, hotX, hotY = 64, 64, 0, 0
	}
	x := int(ci.Pt.X) - metrics.Left - hotX
	y := int(ci.Pt.Y) - metrics.Top - hotY
	rect := image.Rect(x-padding, y-padding, x+width+padding, y+height+padding)
	rect = rect.Intersect(image.Rect(0, 0, metrics.Width, metrics.Height))
	if rect.Empty() {
		return image.Rectangle{}, false
	}
	return rect, true
}

func cursorBitmapGeometry(handle uintptr) (int, int, int, int, bool) {
	var ii iconInfo
	if ret, _, _ := procGetIconInfo.Call(handle, uintptr(unsafe.Pointer(&ii))); ret == 0 {
		return 0, 0, 0, 0, false
	}
	if ii.HbmMask != 0 {
		defer procDeleteObject.Call(ii.HbmMask)
	}
	if ii.HbmColor != 0 {
		defer procDeleteObject.Call(ii.HbmColor)
	}
	width, height, ok := bitmapHandleSize(ii.HbmColor)
	if !ok {
		width, height, ok = bitmapHandleSize(ii.HbmMask)
		if ok && height > 1 {
			// Monochrome cursor masks store AND/XOR planes stacked vertically.
			height /= 2
		}
	}
	if !ok {
		return 0, 0, 0, 0, false
	}
	return width, height, int(ii.XHotspot), int(ii.YHotspot), true
}

func bitmapHandleSize(bitmap uintptr) (int, int, bool) {
	if bitmap == 0 {
		return 0, 0, false
	}
	var bm bitmapObject
	if ret, _, _ := procGetObjectW.Call(bitmap, unsafe.Sizeof(bm), uintptr(unsafe.Pointer(&bm))); ret == 0 {
		return 0, 0, false
	}
	width := int(bm.Width)
	height := int(bm.Height)
	if height < 0 {
		height = -height
	}
	if width <= 0 || height <= 0 {
		return 0, 0, false
	}
	return width, height, true
}

func captureScreenRectBGRA(metrics desktopMetrics, rect image.Rectangle) ([]byte, bool) {
	rect = rect.Intersect(image.Rect(0, 0, metrics.Width, metrics.Height))
	if rect.Empty() {
		return nil, false
	}
	width, height := rect.Dx(), rect.Dy()
	screenDC, _, _ := procGetDC.Call(0)
	if screenDC == 0 {
		return nil, false
	}
	defer procReleaseDC.Call(0, screenDC)

	memDC, _, _ := procCreateCompatibleDC.Call(screenDC)
	if memDC == 0 {
		return nil, false
	}
	defer procDeleteDC.Call(memDC)

	regionMetrics := desktopMetrics{Width: width, Height: height}
	bitmap, bits := createCaptureDIBSection(screenDC, regionMetrics)
	if bitmap == 0 {
		bitmap, _, _ = procCreateCompatibleBitmap.Call(screenDC, uintptr(width), uintptr(height))
	}
	if bitmap == 0 {
		return nil, false
	}
	defer procDeleteObject.Call(bitmap)

	oldObj, _, _ := procSelectObject.Call(memDC, bitmap)
	if oldObj == 0 {
		return nil, false
	}
	defer procSelectObject.Call(memDC, oldObj)

	ret, _, _ := procBitBlt.Call(
		memDC,
		0,
		0,
		uintptr(width),
		uintptr(height),
		screenDC,
		uintptrSigned(metrics.Left+rect.Min.X),
		uintptrSigned(metrics.Top+rect.Min.Y),
		uintptr(srccopy),
	)
	if ret == 0 {
		return nil, false
	}

	pixels := make([]byte, width*height*4)
	if bits != 0 {
		copy(pixels, unsafe.Slice((*byte)(unsafe.Pointer(bits)), len(pixels)))
		return pixels, true
	}

	var bi bitmapInfo
	bi.Header.Size = uint32(unsafe.Sizeof(bi.Header))
	bi.Header.Width = int32(width)
	bi.Header.Height = -int32(height)
	bi.Header.Planes = 1
	bi.Header.BitCount = 32
	bi.Header.Compression = biRGB
	ret, _, _ = procGetDIBits.Call(
		memDC,
		bitmap,
		0,
		uintptr(height),
		uintptr(unsafe.Pointer(&pixels[0])),
		uintptr(unsafe.Pointer(&bi)),
		dibRGB,
	)
	if ret == 0 {
		return nil, false
	}
	return pixels, true
}

func captureScreenRGBA() (*image.RGBA, desktopMetrics, error) {
	metrics := virtualDesktopMetrics()
	if metrics.Width <= 0 || metrics.Height <= 0 {
		return nil, desktopMetrics{}, fmt.Errorf("invalid screen size: %dx%d", metrics.Width, metrics.Height)
	}
	if !inputDesktopAvailable() {
		return nil, metrics, fmt.Errorf("%w: input desktop is not available", errDesktopUnavailable)
	}

	img, err := captureScreenRGBARaw(metrics)
	if err == nil {
		return img, metrics, nil
	}

	return nil, metrics, err
}

func (s *screenCaptureSession) capture(metrics desktopMetrics) (*image.RGBA, error) {
	if s == nil {
		return captureScreenRGBARaw(metrics)
	}
	pixels, err := s.captureBGRA(metrics)
	if err != nil {
		return nil, err
	}
	return bgraToRGBAImage(pixels, metrics.Width, metrics.Height), nil
}

func (s *screenCaptureSession) captureBGRA(metrics desktopMetrics) ([]byte, error) {
	if s == nil {
		return nil, fmt.Errorf("capture session is nil")
	}
	if !sameDesktopMetrics(s.metrics, metrics) || s.screenDC == 0 || s.memDC == 0 || s.bitmap == 0 || len(s.pixels) != metrics.Width*metrics.Height*4 {
		s.close()
		if err := s.init(metrics); err != nil {
			s.close()
			return nil, err
		}
	}

	ret, _, _ := procBitBlt.Call(
		s.memDC,
		0,
		0,
		uintptr(metrics.Width),
		uintptr(metrics.Height),
		s.screenDC,
		uintptrSigned(metrics.Left),
		uintptrSigned(metrics.Top),
		uintptr(srccopy),
	)
	if ret == 0 {
		return nil, fmt.Errorf("BitBlt failed")
	}

	if s.dibBits == 0 {
		var bi bitmapInfo
		bi.Header.Size = uint32(unsafe.Sizeof(bi.Header))
		bi.Header.Width = int32(metrics.Width)
		bi.Header.Height = -int32(metrics.Height)
		bi.Header.Planes = 1
		bi.Header.BitCount = 32
		bi.Header.Compression = biRGB

		ret, _, _ = procGetDIBits.Call(
			s.memDC,
			s.bitmap,
			0,
			uintptr(metrics.Height),
			uintptr(unsafe.Pointer(&s.pixels[0])),
			uintptr(unsafe.Pointer(&bi)),
			dibRGB,
		)
		if ret == 0 {
			return nil, fmt.Errorf("GetDIBits failed")
		}
	}

	return s.pixels, nil
}

func (s *screenCaptureSession) init(metrics desktopMetrics) error {
	if s == nil {
		return fmt.Errorf("capture session is nil")
	}
	screenDC, _, _ := procGetDC.Call(0)
	if screenDC == 0 {
		return fmt.Errorf("GetDC failed")
	}
	memDC, _, _ := procCreateCompatibleDC.Call(screenDC)
	if memDC == 0 {
		procReleaseDC.Call(0, screenDC)
		return fmt.Errorf("CreateCompatibleDC failed")
	}

	bitmap, bits := createCaptureDIBSection(screenDC, metrics)
	if bitmap == 0 {
		bitmap, _, _ = procCreateCompatibleBitmap.Call(screenDC, uintptr(metrics.Width), uintptr(metrics.Height))
	}
	if bitmap == 0 {
		procDeleteDC.Call(memDC)
		procReleaseDC.Call(0, screenDC)
		return fmt.Errorf("CreateCompatibleBitmap failed")
	}
	oldObj, _, _ := procSelectObject.Call(memDC, bitmap)
	if oldObj == 0 {
		procDeleteObject.Call(bitmap)
		procDeleteDC.Call(memDC)
		procReleaseDC.Call(0, screenDC)
		return fmt.Errorf("SelectObject failed")
	}

	s.metrics = metrics
	s.screenDC = screenDC
	s.memDC = memDC
	s.bitmap = bitmap
	s.oldObj = oldObj
	s.dibBits = bits
	if bits != 0 {
		s.pixels = unsafe.Slice((*byte)(unsafe.Pointer(bits)), metrics.Width*metrics.Height*4)
	} else {
		s.pixels = make([]byte, metrics.Width*metrics.Height*4)
	}
	return nil
}

func createCaptureDIBSection(screenDC uintptr, metrics desktopMetrics) (uintptr, uintptr) {
	if screenDC == 0 || metrics.Width <= 0 || metrics.Height <= 0 {
		return 0, 0
	}
	var bi bitmapInfo
	bi.Header.Size = uint32(unsafe.Sizeof(bi.Header))
	bi.Header.Width = int32(metrics.Width)
	bi.Header.Height = -int32(metrics.Height)
	bi.Header.Planes = 1
	bi.Header.BitCount = 32
	bi.Header.Compression = biRGB
	bi.Header.SizeImage = uint32(metrics.Width * metrics.Height * 4)

	var bits uintptr
	bitmap, _, _ := procCreateDIBSection.Call(
		screenDC,
		uintptr(unsafe.Pointer(&bi)),
		dibRGB,
		uintptr(unsafe.Pointer(&bits)),
		0,
		0,
	)
	if bitmap == 0 || bits == 0 {
		if bitmap != 0 {
			procDeleteObject.Call(bitmap)
		}
		return 0, 0
	}
	return bitmap, bits
}

func (s *screenCaptureSession) close() {
	if s == nil {
		return
	}
	if s.memDC != 0 && s.oldObj != 0 {
		procSelectObject.Call(s.memDC, s.oldObj)
	}
	if s.bitmap != 0 {
		procDeleteObject.Call(s.bitmap)
	}
	if s.memDC != 0 {
		procDeleteDC.Call(s.memDC)
	}
	if s.screenDC != 0 {
		procReleaseDC.Call(0, s.screenDC)
	}
	*s = screenCaptureSession{}
}

func newDXGICaptureSession(metrics desktopMetrics) (*dxgiCaptureSession, error) {
	device, context, err := createD3D11Device()
	if err != nil {
		return nil, err
	}
	session := &dxgiCaptureSession{
		metrics: metrics,
		device:  device,
		context: context,
		pixels:  make([]byte, metrics.Width*metrics.Height*4),
	}
	if err := session.initOutputs(); err != nil {
		session.close()
		return nil, err
	}
	return session, nil
}

func createD3D11Device() (uintptr, uintptr, error) {
	featureLevels := []uint32{0xb000, 0xa100, 0xa000}
	var device uintptr
	var context uintptr
	var selectedLevel uint32
	hr, _, _ := procD3D11CreateDevice.Call(
		0,
		uintptr(d3dDriverTypeHardware),
		0,
		uintptr(d3d11CreateDeviceBGRASupport),
		uintptr(unsafe.Pointer(&featureLevels[0])),
		uintptr(len(featureLevels)),
		uintptr(d3d11SDKVersion),
		uintptr(unsafe.Pointer(&device)),
		uintptr(unsafe.Pointer(&selectedLevel)),
		uintptr(unsafe.Pointer(&context)),
	)
	if hresultFailed(hr) {
		return 0, 0, fmt.Errorf("D3D11CreateDevice failed: %s", formatHRESULT(hr))
	}
	if device == 0 || context == 0 {
		if device != 0 {
			comRelease(device)
		}
		if context != 0 {
			comRelease(context)
		}
		return 0, 0, fmt.Errorf("D3D11CreateDevice returned empty device/context")
	}
	return device, context, nil
}

func (s *dxgiCaptureSession) initOutputs() error {
	if s == nil || s.device == 0 {
		return fmt.Errorf("dxgi session is not initialized")
	}
	dxgiDevice, err := comQueryInterface(s.device, &iidIDXGIDevice)
	if err != nil {
		return fmt.Errorf("QueryInterface IDXGIDevice failed: %w", err)
	}
	defer comRelease(dxgiDevice)

	var adapter uintptr
	hr, _, _ := comCall(dxgiDevice, 7, uintptr(unsafe.Pointer(&adapter)))
	if hresultFailed(hr) || adapter == 0 {
		return fmt.Errorf("IDXGIDevice.GetAdapter failed: %s", formatHRESULT(hr))
	}
	defer comRelease(adapter)

	for index := 0; index < 16; index++ {
		var output uintptr
		hr, _, _ = comCall(adapter, 7, uintptr(index), uintptr(unsafe.Pointer(&output)))
		if uint32(hr) == dxgiErrorNotFound {
			break
		}
		if hresultFailed(hr) || output == 0 {
			continue
		}
		capture, ok := s.initOutput(output)
		comRelease(output)
		if ok {
			s.outputs = append(s.outputs, capture)
		}
	}
	if len(s.outputs) == 0 {
		return fmt.Errorf("no duplicated DXGI outputs found")
	}
	return nil
}

func (s *dxgiCaptureSession) initOutput(output uintptr) (dxgiOutputCapture, bool) {
	var desc dxgiOutputDesc
	hr, _, _ := comCall(output, 7, uintptr(unsafe.Pointer(&desc)))
	if hresultFailed(hr) || desc.AttachedToDesktop == 0 {
		return dxgiOutputCapture{}, false
	}
	rect := image.Rect(
		int(desc.DesktopCoordinates.Left),
		int(desc.DesktopCoordinates.Top),
		int(desc.DesktopCoordinates.Right),
		int(desc.DesktopCoordinates.Bottom),
	)
	if rect.Empty() || !rect.Overlaps(image.Rect(s.metrics.Left, s.metrics.Top, s.metrics.Left+s.metrics.Width, s.metrics.Top+s.metrics.Height)) {
		return dxgiOutputCapture{}, false
	}

	output1, err := comQueryInterface(output, &iidIDXGIOutput1)
	if err != nil {
		return dxgiOutputCapture{}, false
	}
	defer comRelease(output1)

	var duplication uintptr
	hr, _, _ = comCall(output1, 22, s.device, uintptr(unsafe.Pointer(&duplication)))
	if hresultFailed(hr) || duplication == 0 {
		return dxgiOutputCapture{}, false
	}

	staging, err := createStagingTexture2D(s.device, rect.Dx(), rect.Dy())
	if err != nil {
		comRelease(duplication)
		return dxgiOutputCapture{}, false
	}

	return dxgiOutputCapture{
		duplication: duplication,
		staging:     staging,
		rect:        rect,
	}, true
}

func createStagingTexture2D(device uintptr, width, height int) (uintptr, error) {
	if device == 0 || width <= 0 || height <= 0 {
		return 0, fmt.Errorf("invalid staging texture size: %dx%d", width, height)
	}
	desc := d3d11Texture2DDesc{
		Width:          uint32(width),
		Height:         uint32(height),
		MipLevels:      1,
		ArraySize:      1,
		Format:         dxgiFormatB8G8R8A8UNorm,
		SampleDesc:     dxgiSampleDesc{Count: 1},
		Usage:          d3d11UsageStaging,
		CPUAccessFlags: d3d11CPUAccessRead,
	}
	var texture uintptr
	hr, _, _ := comCall(device, 5, uintptr(unsafe.Pointer(&desc)), 0, uintptr(unsafe.Pointer(&texture)))
	if hresultFailed(hr) || texture == 0 {
		return 0, fmt.Errorf("CreateTexture2D staging failed: %s", formatHRESULT(hr))
	}
	return texture, nil
}

func (s *dxgiCaptureSession) capture(metrics desktopMetrics) (bgraCaptureFrame, error) {
	if s == nil || s.context == 0 || len(s.outputs) == 0 {
		return bgraCaptureFrame{Metrics: metrics}, fmt.Errorf("dxgi session is not initialized")
	}
	if !sameDesktopMetrics(s.metrics, metrics) {
		return bgraCaptureFrame{Metrics: metrics}, fmt.Errorf("desktop metrics changed")
	}
	dirtyRects := make([]image.Rectangle, 0, 8)
	pendingFirstFrame := false
	for i := range s.outputs {
		rects, acquired, err := s.outputs[i].capture(s.context, s.pixels, metrics)
		if err != nil {
			return bgraCaptureFrame{Metrics: metrics}, err
		}
		if !s.outputs[i].haveFrame {
			pendingFirstFrame = true
		}
		if acquired {
			dirtyRects = append(dirtyRects, rects...)
		}
	}
	if pendingFirstFrame {
		return bgraCaptureFrame{Metrics: metrics}, fmt.Errorf("dxgi first frame timed out")
	}
	return bgraCaptureFrame{
		Pixels:     s.pixels,
		Metrics:    metrics,
		DirtyRects: normalizeCaptureDirtyRects(dirtyRects, image.Rect(0, 0, metrics.Width, metrics.Height)),
		DirtyKnown: true,
		Backend:    "dxgi",
	}, nil
}

func (o *dxgiOutputCapture) capture(context uintptr, dst []byte, metrics desktopMetrics) ([]image.Rectangle, bool, error) {
	if o == nil || o.duplication == 0 || o.staging == 0 {
		return nil, false, fmt.Errorf("dxgi output is not initialized")
	}
	timeout := uintptr(0)
	if !o.haveFrame {
		timeout = 100
	}
	var info dxgiOutduplFrameInfo
	var resource uintptr
	hr, _, _ := comCall(o.duplication, idxgiOutputDuplicationAcquireNextFrame, timeout, uintptr(unsafe.Pointer(&info)), uintptr(unsafe.Pointer(&resource)))
	if uint32(hr) == dxgiErrorWaitTimeout {
		return nil, false, nil
	}
	if hresultFailed(hr) || resource == 0 {
		return nil, false, fmt.Errorf("AcquireNextFrame failed: %s", formatHRESULT(hr))
	}
	defer comCall(o.duplication, idxgiOutputDuplicationReleaseFrame)
	defer comRelease(resource)

	texture, err := comQueryInterface(resource, &iidID3D11Texture2D)
	if err != nil {
		return nil, false, fmt.Errorf("QueryInterface ID3D11Texture2D failed: %w", err)
	}
	defer comRelease(texture)

	moveRects, dirtyRects, err := o.readFrameMetadata(info.TotalMetadataBufferSize)
	if err != nil {
		return nil, false, err
	}
	outputBounds := image.Rect(0, 0, o.rect.Dx(), o.rect.Dy())
	changedRects := make([]image.Rectangle, 0, len(moveRects)*2+len(dirtyRects)+1)
	if !o.haveFrame || info.RectsCoalesced != 0 {
		full := outputBounds
		copyTextureFull(context, o.staging, texture)
		if err := mapAndCopyDXGIRects(context, o.staging, dst, metrics, o.rect, []image.Rectangle{full}); err != nil {
			return nil, false, err
		}
		o.haveFrame = true
		changedRects = append(changedRects, outputLocalToFrameRect(o.rect, metrics, full))
		return changedRects, true, nil
	}
	if len(moveRects)+len(dirtyRects) == 0 {
		return nil, true, nil
	}

	localDirty := make([]image.Rectangle, 0, len(moveRects)*2+len(dirtyRects))
	addLocalDirty := func(rect image.Rectangle) {
		r := rect.Intersect(outputBounds)
		if r.Empty() {
			return
		}
		localDirty = append(localDirty, r)
		changedRects = append(changedRects, outputLocalToFrameRect(o.rect, metrics, r))
	}
	for _, move := range moveRects {
		dstRect := dxgiRectToImage(move.DestinationRect)
		srcRect := image.Rect(
			int(move.SourcePoint.X),
			int(move.SourcePoint.Y),
			int(move.SourcePoint.X)+dstRect.Dx(),
			int(move.SourcePoint.Y)+dstRect.Dy(),
		)
		// Copy the authoritative pixels for both sides of a move. Relying on
		// move replay alone can leave desktop-icon/window drag trails if DXGI
		// coalesces the uncovered source area separately.
		addLocalDirty(srcRect)
		addLocalDirty(dstRect)
	}

	for _, rect := range dirtyRects {
		addLocalDirty(dxgiRectToImage(rect))
	}
	if len(localDirty) > 0 {
		copyTextureRects(context, o.staging, texture, localDirty)
		if err := mapAndCopyDXGIRects(context, o.staging, dst, metrics, o.rect, localDirty); err != nil {
			return nil, false, err
		}
	}
	o.haveFrame = true
	return changedRects, true, nil
}

func (o *dxgiOutputCapture) readFrameMetadata(totalBytes uint32) ([]dxgiMoveRect, []dxgiRect, error) {
	if o == nil || o.duplication == 0 || totalBytes == 0 {
		return nil, nil, nil
	}
	if len(o.metadata) < int(totalBytes) {
		o.metadata = make([]byte, int(totalBytes))
	}
	var moveBytes uint32
	hr, _, _ := comCall(o.duplication, idxgiOutputDuplicationGetFrameMoveRects, uintptr(len(o.metadata)), uintptr(unsafe.Pointer(&o.metadata[0])), uintptr(unsafe.Pointer(&moveBytes)))
	if uint32(hr) == dxgiErrorMoreData && moveBytes > uint32(len(o.metadata)) {
		o.metadata = make([]byte, int(moveBytes))
		hr, _, _ = comCall(o.duplication, idxgiOutputDuplicationGetFrameMoveRects, uintptr(len(o.metadata)), uintptr(unsafe.Pointer(&o.metadata[0])), uintptr(unsafe.Pointer(&moveBytes)))
	}
	if hresultFailed(hr) && uint32(hr) != dxgiErrorInvalidCall {
		return nil, nil, fmt.Errorf("GetFrameMoveRects failed: %s", formatHRESULT(hr))
	}
	moveCount := int(moveBytes) / int(unsafe.Sizeof(dxgiMoveRect{}))
	moveRects := make([]dxgiMoveRect, 0, moveCount)
	if moveCount > 0 {
		raw := unsafe.Slice((*dxgiMoveRect)(unsafe.Pointer(&o.metadata[0])), moveCount)
		moveRects = append(moveRects, raw...)
	}

	var dirtyBytes uint32
	hr, _, _ = comCall(o.duplication, idxgiOutputDuplicationGetFrameDirtyRects, uintptr(len(o.metadata)), uintptr(unsafe.Pointer(&o.metadata[0])), uintptr(unsafe.Pointer(&dirtyBytes)))
	if uint32(hr) == dxgiErrorMoreData && dirtyBytes > uint32(len(o.metadata)) {
		o.metadata = make([]byte, int(dirtyBytes))
		hr, _, _ = comCall(o.duplication, idxgiOutputDuplicationGetFrameDirtyRects, uintptr(len(o.metadata)), uintptr(unsafe.Pointer(&o.metadata[0])), uintptr(unsafe.Pointer(&dirtyBytes)))
	}
	if hresultFailed(hr) && uint32(hr) != dxgiErrorInvalidCall {
		return nil, nil, fmt.Errorf("GetFrameDirtyRects failed: %s", formatHRESULT(hr))
	}
	dirtyCount := int(dirtyBytes) / int(unsafe.Sizeof(dxgiRect{}))
	dirtyRects := make([]dxgiRect, 0, dirtyCount)
	if dirtyCount > 0 {
		raw := unsafe.Slice((*dxgiRect)(unsafe.Pointer(&o.metadata[0])), dirtyCount)
		dirtyRects = append(dirtyRects, raw...)
	}
	return moveRects, dirtyRects, nil
}

func copyTextureFull(context, dstTexture, srcTexture uintptr) {
	if context == 0 || dstTexture == 0 || srcTexture == 0 {
		return
	}
	comCall(context, 47, dstTexture, srcTexture)
}

func copyTextureRects(context, dstTexture, srcTexture uintptr, rects []image.Rectangle) {
	if context == 0 || dstTexture == 0 || srcTexture == 0 {
		return
	}
	for _, rect := range rects {
		if rect.Empty() {
			continue
		}
		box := d3d11Box{
			Left:   uint32(rect.Min.X),
			Top:    uint32(rect.Min.Y),
			Front:  0,
			Right:  uint32(rect.Max.X),
			Bottom: uint32(rect.Max.Y),
			Back:   1,
		}
		comCall(context, 46, dstTexture, 0, uintptr(rect.Min.X), uintptr(rect.Min.Y), 0, srcTexture, 0, uintptr(unsafe.Pointer(&box)))
	}
}

func mapAndCopyDXGIRects(context, staging uintptr, dst []byte, metrics desktopMetrics, outputRect image.Rectangle, rects []image.Rectangle) error {
	if context == 0 || staging == 0 {
		return fmt.Errorf("dxgi staging texture is not initialized")
	}
	var mapped d3d11MappedSubresource
	hr, _, _ := comCall(context, 14, staging, 0, d3d11MapRead, 0, uintptr(unsafe.Pointer(&mapped)))
	if hresultFailed(hr) || mapped.Data == 0 {
		return fmt.Errorf("D3D11 Map failed: %s", formatHRESULT(hr))
	}
	defer comCall(context, 15, staging, 0)

	outputBounds := image.Rect(0, 0, outputRect.Dx(), outputRect.Dy())
	for _, rect := range rects {
		r := rect.Intersect(outputBounds)
		if r.Empty() {
			continue
		}
		copyMappedDXGIRect(dst, metrics, outputRect, mapped, r)
	}
	return nil
}

func copyMappedDXGIRect(dst []byte, metrics desktopMetrics, outputRect image.Rectangle, mapped d3d11MappedSubresource, rect image.Rectangle) {
	if len(dst) < metrics.Width*metrics.Height*4 || mapped.Data == 0 || mapped.RowPitch == 0 {
		return
	}
	rowPitch := int(mapped.RowPitch)
	copyBytes := rect.Dx() * 4
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		frameX := outputRect.Min.X - metrics.Left + rect.Min.X
		frameY := outputRect.Min.Y - metrics.Top + y
		if frameX < 0 || frameY < 0 || frameX+rect.Dx() > metrics.Width || frameY >= metrics.Height {
			continue
		}
		src := unsafe.Slice((*byte)(unsafe.Pointer(mapped.Data+uintptr(y*rowPitch+rect.Min.X*4))), copyBytes)
		dstOffset := (frameY*metrics.Width + frameX) * 4
		copy(dst[dstOffset:dstOffset+copyBytes], src)
	}
}

func applyBGRAMove(pixels []byte, width, height int, srcPoint image.Point, dstRect image.Rectangle) bool {
	if width <= 0 || height <= 0 || len(pixels) < width*height*4 {
		return false
	}
	bounds := image.Rect(0, 0, width, height)
	dst := dstRect.Intersect(bounds)
	if dst.Empty() {
		return false
	}
	srcX := srcPoint.X + dst.Min.X - dstRect.Min.X
	srcY := srcPoint.Y + dst.Min.Y - dstRect.Min.Y
	if srcX < 0 {
		shift := -srcX
		dst.Min.X += shift
		srcX = 0
	}
	if srcY < 0 {
		shift := -srcY
		dst.Min.Y += shift
		srcY = 0
	}
	if srcX+dst.Dx() > width {
		dst.Max.X -= srcX + dst.Dx() - width
	}
	if srcY+dst.Dy() > height {
		dst.Max.Y -= srcY + dst.Dy() - height
	}
	if dst.Empty() {
		return false
	}
	rowBytes := width * 4
	copyBytes := dst.Dx() * 4
	temp := make([]byte, copyBytes*dst.Dy())
	for y := 0; y < dst.Dy(); y++ {
		srcOffset := (srcY+y)*rowBytes + srcX*4
		copy(temp[y*copyBytes:(y+1)*copyBytes], pixels[srcOffset:srcOffset+copyBytes])
	}
	for y := 0; y < dst.Dy(); y++ {
		dstOffset := (dst.Min.Y+y)*rowBytes + dst.Min.X*4
		copy(pixels[dstOffset:dstOffset+copyBytes], temp[y*copyBytes:(y+1)*copyBytes])
	}
	return true
}

func (s *dxgiCaptureSession) close() {
	if s == nil {
		return
	}
	for i := range s.outputs {
		if s.outputs[i].staging != 0 {
			comRelease(s.outputs[i].staging)
		}
		if s.outputs[i].duplication != 0 {
			comRelease(s.outputs[i].duplication)
		}
	}
	if s.context != 0 {
		comRelease(s.context)
	}
	if s.device != 0 {
		comRelease(s.device)
	}
	*s = dxgiCaptureSession{}
}

func dxgiRectToImage(rect dxgiRect) image.Rectangle {
	return image.Rect(int(rect.Left), int(rect.Top), int(rect.Right), int(rect.Bottom))
}

func outputLocalToFrameRect(outputRect image.Rectangle, metrics desktopMetrics, local image.Rectangle) image.Rectangle {
	return image.Rect(
		outputRect.Min.X-metrics.Left+local.Min.X,
		outputRect.Min.Y-metrics.Top+local.Min.Y,
		outputRect.Min.X-metrics.Left+local.Max.X,
		outputRect.Min.Y-metrics.Top+local.Max.Y,
	)
}

func normalizeCaptureDirtyRects(rects []image.Rectangle, bounds image.Rectangle) []image.Rectangle {
	if len(rects) == 0 {
		return nil
	}
	out := make([]image.Rectangle, 0, len(rects))
	for _, rect := range rects {
		r := rect.Intersect(bounds)
		if !r.Empty() {
			out = append(out, r)
		}
	}
	if len(out) <= maxTileCount {
		return out
	}
	union := unionImageRects(out)
	if union.Empty() {
		return nil
	}
	return []image.Rectangle{union}
}

func unionImageRects(rects []image.Rectangle) image.Rectangle {
	var union image.Rectangle
	for _, rect := range rects {
		if rect.Empty() {
			continue
		}
		if union.Empty() {
			union = rect
		} else {
			union = union.Union(rect)
		}
	}
	return union
}

func comMethod(obj uintptr, index uintptr) uintptr {
	if obj == 0 {
		return 0
	}
	vtbl := *(*uintptr)(unsafe.Pointer(obj))
	return *(*uintptr)(unsafe.Pointer(vtbl + index*unsafe.Sizeof(uintptr(0))))
}

func comCall(obj uintptr, index uintptr, args ...uintptr) (uintptr, uintptr, syscall.Errno) {
	method := comMethod(obj, index)
	if method == 0 {
		return uintptr(dxgiErrorInvalidCall), 0, syscall.EINVAL
	}
	callArgs := make([]uintptr, 0, len(args)+1)
	callArgs = append(callArgs, obj)
	callArgs = append(callArgs, args...)
	r1, r2, err := syscall.SyscallN(method, callArgs...)
	return r1, r2, err
}

func comQueryInterface(obj uintptr, iid *guid) (uintptr, error) {
	if obj == 0 || iid == nil {
		return 0, fmt.Errorf("invalid COM object")
	}
	var out uintptr
	hr, _, _ := comCall(obj, 0, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&out)))
	if hresultFailed(hr) || out == 0 {
		return 0, fmt.Errorf("%s", formatHRESULT(hr))
	}
	return out, nil
}

func comRelease(obj uintptr) {
	if obj != 0 {
		comCall(obj, 2)
	}
}

func hresultFailed(hr uintptr) bool {
	return int32(uint32(hr)) < 0
}

func formatHRESULT(hr uintptr) string {
	switch uint32(hr) {
	case dxgiErrorWaitTimeout:
		return "DXGI_ERROR_WAIT_TIMEOUT"
	case dxgiErrorAccessLost:
		return "DXGI_ERROR_ACCESS_LOST"
	case dxgiErrorNotCurrentlyAvailable:
		return "DXGI_ERROR_NOT_CURRENTLY_AVAILABLE"
	case dxgiErrorUnsupported:
		return "DXGI_ERROR_UNSUPPORTED"
	case dxgiErrorNotFound:
		return "DXGI_ERROR_NOT_FOUND"
	case dxgiErrorMoreData:
		return "DXGI_ERROR_MORE_DATA"
	case dxgiErrorInvalidCall:
		return "DXGI_ERROR_INVALID_CALL"
	default:
		return fmt.Sprintf("HRESULT 0x%08x", uint32(hr))
	}
}

func sameDesktopMetrics(a, b desktopMetrics) bool {
	return a.Left == b.Left && a.Top == b.Top && a.Width == b.Width && a.Height == b.Height
}

func convertBGRAtoRGBA(dst, src []byte) {
	for srcOffset, dstOffset := 0, 0; srcOffset+3 < len(src) && dstOffset+3 < len(dst); srcOffset, dstOffset = srcOffset+4, dstOffset+4 {
		dst[dstOffset] = src[srcOffset+2]
		dst[dstOffset+1] = src[srcOffset+1]
		dst[dstOffset+2] = src[srcOffset]
		dst[dstOffset+3] = 255
	}
}

func bgraToRGBAImage(src []byte, width, height int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, max(1, width), max(1, height)))
	convertBGRAtoRGBA(img.Pix, src)
	return img
}

func diffBoundsBGRA(prev, curr []byte, width, height int) (image.Rectangle, bool) {
	if width <= 0 || height <= 0 || len(curr) < width*height*4 || len(prev) < width*height*4 {
		if width <= 0 || height <= 0 {
			return image.Rectangle{}, false
		}
		return image.Rect(0, 0, width, height), true
	}

	rowBytes := width * 4
	minX, minY := width, height
	maxX, maxY := 0, 0
	changed := false
	for y := 0; y < height; y++ {
		row := y * rowBytes
		if bytes.Equal(curr[row:row+rowBytes], prev[row:row+rowBytes]) {
			continue
		}
		for x := 0; x < width; x++ {
			offset := row + x*4
			if pixelDiffersBGRA(curr[offset:offset+4], prev[offset:offset+4]) {
				if !changed {
					minX, maxX = x, x+1
					minY, maxY = y, y+1
					changed = true
				} else {
					minX = min(minX, x)
					minY = min(minY, y)
					maxX = max(maxX, x+1)
					maxY = max(maxY, y+1)
				}
			}
		}
	}
	if !changed {
		return image.Rectangle{}, false
	}
	margin := 6
	return image.Rect(max(0, minX-margin), max(0, minY-margin), min(width, maxX+margin), min(height, maxY+margin)), true
}

func pixelDiffersBGRA(a, b []byte) bool {
	const tolerance = 12
	diff := abs(int(a[0])-int(b[0])) + abs(int(a[1])-int(b[1])) + abs(int(a[2])-int(b[2]))
	return diff > tolerance
}

func diffTilesBGRA(prev, curr []byte, width, height, tileSize int) []image.Rectangle {
	if width <= 0 || height <= 0 || len(curr) < width*height*4 || len(prev) < width*height*4 {
		return nil
	}
	if tileSize <= 0 {
		tileSize = tilePatchSize
	}

	tiles := make([]image.Rectangle, 0, 8)
	for y := 0; y < height; y += tileSize {
		for x := 0; x < width; x += tileSize {
			rect := image.Rect(x, y, min(width, x+tileSize), min(height, y+tileSize))
			if tileDiffersBGRA(prev, curr, width, height, rect) {
				tiles = append(tiles, rect)
				if len(tiles) > maxTileCount {
					return tiles
				}
			}
		}
	}
	return tiles
}

func tileDiffersBGRA(prev, curr []byte, width, height int, rect image.Rectangle) bool {
	bounds := rect.Intersect(image.Rect(0, 0, width, height))
	if bounds.Empty() {
		return false
	}
	rowBytes := width * 4
	startX := bounds.Min.X * 4
	endX := bounds.Max.X * 4
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		row := y * rowBytes
		if bytes.Equal(curr[row+startX:row+endX], prev[row+startX:row+endX]) {
			continue
		}
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			offset := row + x*4
			if pixelDiffersBGRA(curr[offset:offset+4], prev[offset:offset+4]) {
				return true
			}
		}
	}
	return false
}

func cropBGRAtoRGBA(src []byte, width, height int, rect image.Rectangle) *image.RGBA {
	bounds := rect.Intersect(image.Rect(0, 0, width, height))
	if bounds.Empty() || len(src) < width*height*4 {
		return nil
	}
	dst := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	rowBytes := width * 4
	for y := 0; y < bounds.Dy(); y++ {
		srcRow := (bounds.Min.Y+y)*rowBytes + bounds.Min.X*4
		dstRow := y * dst.Stride
		for x := 0; x < bounds.Dx(); x++ {
			srcIndex := srcRow + x*4
			dstIndex := dstRow + x*4
			dst.Pix[dstIndex] = src[srcIndex+2]
			dst.Pix[dstIndex+1] = src[srcIndex+1]
			dst.Pix[dstIndex+2] = src[srcIndex]
			dst.Pix[dstIndex+3] = 255
		}
	}
	return dst
}

func cropBGRABytes(src []byte, width, height int, rect image.Rectangle) ([]byte, image.Rectangle) {
	bounds := rect.Intersect(image.Rect(0, 0, width, height))
	if bounds.Empty() || width <= 0 || height <= 0 || len(src) < width*height*4 {
		return nil, image.Rectangle{}
	}
	dst := make([]byte, bounds.Dx()*bounds.Dy()*4)
	srcStride := width * 4
	dstStride := bounds.Dx() * 4
	for y := 0; y < bounds.Dy(); y++ {
		srcRow := (bounds.Min.Y+y)*srcStride + bounds.Min.X*4
		dstRow := y * dstStride
		copy(dst[dstRow:dstRow+dstStride], src[srcRow:srcRow+dstStride])
	}
	return dst, bounds
}

func buildTileAtlasBGRA(src []byte, width, height int, rects []image.Rectangle, tileSize int) (*image.RGBA, []tilePatch) {
	if len(rects) == 0 || width <= 0 || height <= 0 || len(src) < width*height*4 {
		return nil, nil
	}
	if tileSize <= 0 {
		tileSize = tilePatchSize
	}
	cols := int(math.Ceil(math.Sqrt(float64(len(rects)))))
	if cols < 1 {
		cols = 1
	}
	rows := int(math.Ceil(float64(len(rects)) / float64(cols)))
	atlas := image.NewRGBA(image.Rect(0, 0, cols*tileSize, rows*tileSize))
	tiles := make([]tilePatch, 0, len(rects))
	for index, rect := range rects {
		bounds := rect.Intersect(image.Rect(0, 0, width, height))
		if bounds.Empty() {
			continue
		}
		col := index % cols
		row := index / cols
		atlasX := col * tileSize
		atlasY := row * tileSize
		copyBGRAtoRGBARegion(atlas, image.Point{X: atlasX, Y: atlasY}, src, width, height, bounds)
		tiles = append(tiles, tilePatch{
			X:      bounds.Min.X,
			Y:      bounds.Min.Y,
			W:      bounds.Dx(),
			H:      bounds.Dy(),
			AtlasX: atlasX,
			AtlasY: atlasY,
		})
	}
	if len(tiles) == 0 {
		return nil, nil
	}
	return atlas, tiles
}

func buildTileAtlasRawBGRA(src []byte, width, height int, rects []image.Rectangle, tileSize int) ([]byte, []tilePatch) {
	if len(rects) == 0 || width <= 0 || height <= 0 || len(src) < width*height*4 {
		return nil, nil
	}
	if tileSize <= 0 {
		tileSize = tilePatchSize
	}
	cols := int(math.Ceil(math.Sqrt(float64(len(rects)))))
	if cols < 1 {
		cols = 1
	}
	tiles := make([]tilePatch, 0, len(rects))
	for index, rect := range rects {
		bounds := rect.Intersect(image.Rect(0, 0, width, height))
		if bounds.Empty() {
			continue
		}
		col := index % cols
		row := index / cols
		tiles = append(tiles, tilePatch{
			X:      bounds.Min.X,
			Y:      bounds.Min.Y,
			W:      bounds.Dx(),
			H:      bounds.Dy(),
			AtlasX: col * tileSize,
			AtlasY: row * tileSize,
		})
	}
	if len(tiles) == 0 {
		return nil, nil
	}

	atlasW := tileAtlasWidth(tiles)
	atlasH := tileAtlasHeight(tiles)
	if atlasW <= 0 || atlasH <= 0 {
		return nil, nil
	}
	atlas := make([]byte, atlasW*atlasH*4)
	srcStride := width * 4
	atlasStride := atlasW * 4
	for _, tile := range tiles {
		for y := 0; y < tile.H; y++ {
			srcRow := (tile.Y+y)*srcStride + tile.X*4
			dstRow := (tile.AtlasY+y)*atlasStride + tile.AtlasX*4
			copy(atlas[dstRow:dstRow+tile.W*4], src[srcRow:srcRow+tile.W*4])
		}
	}
	return atlas, tiles
}

func tileAtlasWidth(tiles []tilePatch) int {
	maxX := 0
	for _, tile := range tiles {
		if tile.W > 0 && tile.AtlasX+tile.W > maxX {
			maxX = tile.AtlasX + tile.W
		}
	}
	return maxX
}

func tileAtlasHeight(tiles []tilePatch) int {
	maxY := 0
	for _, tile := range tiles {
		if tile.H > 0 && tile.AtlasY+tile.H > maxY {
			maxY = tile.AtlasY + tile.H
		}
	}
	return maxY
}

func copyBGRAtoRGBARegion(dst *image.RGBA, dstPoint image.Point, src []byte, width, height int, rect image.Rectangle) {
	if dst == nil || len(src) < width*height*4 {
		return
	}
	bounds := rect.Intersect(image.Rect(0, 0, width, height))
	if bounds.Empty() {
		return
	}
	rowBytes := width * 4
	for y := 0; y < bounds.Dy(); y++ {
		srcRow := (bounds.Min.Y+y)*rowBytes + bounds.Min.X*4
		dstY := dstPoint.Y + y
		if dstY < dst.Bounds().Min.Y || dstY >= dst.Bounds().Max.Y {
			continue
		}
		dstRow := (dstY-dst.Bounds().Min.Y)*dst.Stride + (dstPoint.X-dst.Bounds().Min.X)*4
		for x := 0; x < bounds.Dx(); x++ {
			dstX := dstPoint.X + x
			if dstX < dst.Bounds().Min.X || dstX >= dst.Bounds().Max.X {
				continue
			}
			srcIndex := srcRow + x*4
			dstIndex := dstRow + x*4
			dst.Pix[dstIndex] = src[srcIndex+2]
			dst.Pix[dstIndex+1] = src[srcIndex+1]
			dst.Pix[dstIndex+2] = src[srcIndex]
			dst.Pix[dstIndex+3] = 255
		}
	}
}

func copyBGRARegion(dst, src []byte, width, height int, rect image.Rectangle) {
	if width <= 0 || height <= 0 || len(dst) < width*height*4 || len(src) < width*height*4 {
		return
	}
	bounds := rect.Intersect(image.Rect(0, 0, width, height))
	if bounds.Empty() {
		return
	}
	rowBytes := width * 4
	copyBytes := bounds.Dx() * 4
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		row := y*rowBytes + bounds.Min.X*4
		copy(dst[row:row+copyBytes], src[row:row+copyBytes])
	}
}

func copySmallBGRARegion(dst []byte, frameW, frameH int, dstRect image.Rectangle, src []byte) {
	if frameW <= 0 || frameH <= 0 || len(dst) < frameW*frameH*4 || dstRect.Empty() {
		return
	}
	srcW, srcH := dstRect.Dx(), dstRect.Dy()
	if srcW <= 0 || srcH <= 0 || len(src) < srcW*srcH*4 {
		return
	}
	bounds := dstRect.Intersect(image.Rect(0, 0, frameW, frameH))
	if bounds.Empty() {
		return
	}
	dstRowBytes := frameW * 4
	srcRowBytes := srcW * 4
	copyBytes := bounds.Dx() * 4
	srcOffsetX := bounds.Min.X - dstRect.Min.X
	srcOffsetY := bounds.Min.Y - dstRect.Min.Y
	for y := 0; y < bounds.Dy(); y++ {
		dstRow := (bounds.Min.Y+y)*dstRowBytes + bounds.Min.X*4
		srcRow := (srcOffsetY+y)*srcRowBytes + srcOffsetX*4
		copy(dst[dstRow:dstRow+copyBytes], src[srcRow:srcRow+copyBytes])
	}
}

func captureScreenRGBARaw(metrics desktopMetrics) (*image.RGBA, error) {
	screenDC, _, _ := procGetDC.Call(0)
	if screenDC == 0 {
		return nil, fmt.Errorf("GetDC failed")
	}
	defer procReleaseDC.Call(0, screenDC)

	memDC, _, _ := procCreateCompatibleDC.Call(screenDC)
	if memDC == 0 {
		return nil, fmt.Errorf("CreateCompatibleDC failed")
	}
	defer procDeleteDC.Call(memDC)

	bitmap, _, _ := procCreateCompatibleBitmap.Call(screenDC, uintptr(metrics.Width), uintptr(metrics.Height))
	if bitmap == 0 {
		return nil, fmt.Errorf("CreateCompatibleBitmap failed")
	}
	defer procDeleteObject.Call(bitmap)

	oldObj, _, _ := procSelectObject.Call(memDC, bitmap)
	defer procSelectObject.Call(memDC, oldObj)

	ret, _, _ := procBitBlt.Call(
		memDC,
		0,
		0,
		uintptr(metrics.Width),
		uintptr(metrics.Height),
		screenDC,
		uintptrSigned(metrics.Left),
		uintptrSigned(metrics.Top),
		uintptr(srccopy),
	)
	if ret == 0 {
		return nil, fmt.Errorf("BitBlt failed")
	}

	var bi bitmapInfo
	bi.Header.Size = uint32(unsafe.Sizeof(bi.Header))
	bi.Header.Width = int32(metrics.Width)
	bi.Header.Height = -int32(metrics.Height)
	bi.Header.Planes = 1
	bi.Header.BitCount = 32
	bi.Header.Compression = biRGB

	pixels := make([]byte, metrics.Width*metrics.Height*4)
	ret, _, _ = procGetDIBits.Call(
		memDC,
		bitmap,
		0,
		uintptr(metrics.Height),
		uintptr(unsafe.Pointer(&pixels[0])),
		uintptr(unsafe.Pointer(&bi)),
		dibRGB,
	)
	if ret == 0 {
		return nil, fmt.Errorf("GetDIBits failed")
	}

	img := image.NewRGBA(image.Rect(0, 0, metrics.Width, metrics.Height))
	convertBGRAtoRGBA(img.Pix, pixels)

	return img, nil
}

func (a *agent) currentCursorState(frameMetrics, nativeMetrics desktopMetrics, forceImage bool) cursorSnapshot {
	ci := cursorInfo{Size: uint32(unsafe.Sizeof(cursorInfo{}))}
	if ret, _, _ := procGetCursorInfo.Call(uintptr(unsafe.Pointer(&ci))); ret == 0 {
		return cursorSnapshot{Kind: "arrow"}
	}
	if ci.Flags&cursorShowing == 0 || ci.Cursor == 0 {
		return cursorSnapshot{Kind: "arrow"}
	}
	x := int(ci.Pt.X) - nativeMetrics.Left
	y := int(ci.Pt.Y) - nativeMetrics.Top
	x, y = scalePointBetweenSizes(x, y, nativeMetrics.Width, nativeMetrics.Height, frameMetrics.Width, frameMetrics.Height)
	snap := cursorSnapshot{
		X:       x,
		Y:       y,
		Visible: true,
		Kind:    classifyCursorHandle(ci.Cursor),
	}
	return snap
}

func (a *agent) markCursorImageCleared() bool {
	if a == nil || !a.cursorImageValid {
		return false
	}
	a.cursorImageHash = 0
	a.cursorImageValid = false
	return true
}

func captureCursorPNG(handle uintptr) ([]byte, int, int, int, int, uint64, bool) {
	var ii iconInfo
	if ret, _, _ := procGetIconInfo.Call(handle, uintptr(unsafe.Pointer(&ii))); ret == 0 {
		return nil, 0, 0, 0, 0, 0, false
	}
	if ii.HbmMask != 0 {
		defer procDeleteObject.Call(ii.HbmMask)
	}
	if ii.HbmColor != 0 {
		defer procDeleteObject.Call(ii.HbmColor)
	}
	if ii.HbmColor == 0 {
		return nil, 0, 0, 0, 0, 0, false
	}
	colorPixels, colorW, colorH, ok := readBitmapPixels(ii.HbmColor)
	if !ok || colorW <= 0 || colorH <= 0 {
		return nil, 0, 0, 0, 0, 0, false
	}
	maskPixels, maskW, maskH, maskOK := readBitmapPixels(ii.HbmMask)
	width := int(colorW)
	height := int(colorH)
	rgba := image.NewRGBA(image.Rect(0, 0, width, height))
	hasAlpha := false
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			src := (y*width + x) * 4
			alpha := colorPixels[src+3]
			if alpha != 0 {
				hasAlpha = true
			}
			rgba.Pix[y*rgba.Stride+x*4+0] = colorPixels[src+2]
			rgba.Pix[y*rgba.Stride+x*4+1] = colorPixels[src+1]
			rgba.Pix[y*rgba.Stride+x*4+2] = colorPixels[src+0]
			rgba.Pix[y*rgba.Stride+x*4+3] = alpha
		}
	}
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			dst := y*rgba.Stride + x*4 + 3
			if !hasAlpha {
				rgba.Pix[dst] = 255
			}
			if maskOK && maskW >= int32(width) && maskH >= int32(height) && bitmapMaskTransparent(maskPixels, int(maskW), x, y) {
				rgba.Pix[dst] = 0
			}
		}
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, rgba); err != nil {
		return nil, 0, 0, 0, 0, 0, false
	}
	data := buf.Bytes()
	return data, width, height, int(ii.XHotspot), int(ii.YHotspot), hashBytes64(data), true
}

func bitmapMaskTransparent(pixels []byte, width, x, y int) bool {
	offset := (y*width + x) * 4
	if offset+3 >= len(pixels) {
		return false
	}
	return pixels[offset] > 127 || pixels[offset+1] > 127 || pixels[offset+2] > 127
}

func encodeJPEGImage(img image.Image, quality int) ([]byte, error) {
	// Try hardware-accelerated helper first
	activeHelperMu.Lock()
	session := activeHelperSession
	activeHelperMu.Unlock()
	if session != nil {
		rgba, ok := img.(*image.RGBA)
		if ok && len(rgba.Pix) > 0 {
			resp, payload, err := session.exchange(mfSessionRequest{
				Type:    "encode",
				Codec:   "jpeg",
				Format:  "rgba",
				Width:   rgba.Bounds().Dx(),
				Height:  rgba.Bounds().Dy(),
				Stride:  rgba.Stride,
				Quality: quality,
			}, rgba.Pix)
			if err == nil && resp.OK && len(payload) > 0 {
				return payload, nil
			}
		}
	}
	// Fallback to Go stdlib
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: clamp(quality, 20, 100)}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func shouldRefresh(kind string) bool {
	switch kind {
	case "move", "keydown", "reset_keys", "clipboard_set":
		return false
	default:
		return true
	}
}

func shouldUsePatch(rect, bounds image.Rectangle) bool {
	return shouldUsePatchArea(rect, bounds, 55)
}

func shouldUsePatchArea(rect, bounds image.Rectangle, maxPercent int) bool {
	patchArea := rect.Dx() * rect.Dy()
	fullArea := bounds.Dx() * bounds.Dy()
	if patchArea <= 0 || fullArea <= 0 {
		return false
	}
	return patchArea*100/fullArea <= clamp(maxPercent, 5, 95)
}

func rectArea(rect image.Rectangle) int {
	return max(0, rect.Dx()*rect.Dy())
}

func totalRectArea(rects []image.Rectangle) int {
	total := 0
	for _, rect := range rects {
		total += rectArea(rect)
	}
	return total
}

func shouldProbeTilesForVideo(fullBounds, patchRect image.Rectangle) bool {
	fullW := max(1, fullBounds.Dx())
	fullH := max(1, fullBounds.Dy())
	patchArea := rectArea(patchRect)
	fullArea := max(1, fullW*fullH)
	if patchArea <= 0 {
		return false
	}
	if patchArea*100/fullArea >= 18 {
		return true
	}
	return patchRect.Dx()*100/fullW >= 55 || patchRect.Dy()*100/fullH >= 55
}

func shouldPreferTilesOverPatch(fullBounds, patchRect image.Rectangle, tileRects []image.Rectangle) bool {
	if len(tileRects) < 2 {
		return false
	}
	patchArea := rectArea(patchRect)
	tileArea := totalRectArea(tileRects)
	fullArea := max(1, fullBounds.Dx()*fullBounds.Dy())
	if patchArea <= 0 || tileArea <= 0 {
		return false
	}
	if tileArea*100 <= patchArea*82 {
		return true
	}
	return patchArea*100/fullArea >= 30 && tileArea*100 <= patchArea*92
}

func shouldUseTilePatch(tiles []image.Rectangle, bounds image.Rectangle) bool {
	if len(tiles) < 2 || len(tiles) > maxTileCount {
		return false
	}
	fullArea := bounds.Dx() * bounds.Dy()
	if fullArea <= 0 {
		return false
	}

	tileArea := 0
	for _, rect := range tiles {
		tileArea += rect.Dx() * rect.Dy()
	}
	if tileArea <= 0 {
		return false
	}
	return tileArea*100/fullArea <= maxTilePatchArea
}

func diffBounds(prev, curr *image.RGBA) (image.Rectangle, bool) {
	if prev == nil || curr == nil || !sameBounds(prev, curr) {
		if curr == nil {
			return image.Rectangle{}, false
		}
		return curr.Bounds(), true
	}

	bounds := curr.Bounds()
	minX, minY := bounds.Max.X, bounds.Max.Y
	maxX, maxY := bounds.Min.X, bounds.Min.Y
	changed := false

	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		currRow := (y - bounds.Min.Y) * curr.Stride
		prevRow := (y - bounds.Min.Y) * prev.Stride
		rowWidth := bounds.Dx() * 4
		if rowWidth > 0 && bytes.Equal(curr.Pix[currRow:currRow+rowWidth], prev.Pix[prevRow:prevRow+rowWidth]) {
			continue
		}
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			offset := (x - bounds.Min.X) * 4
			currIndex := currRow + offset
			prevIndex := prevRow + offset
			if pixelDiffers(curr.Pix[currIndex:currIndex+4], prev.Pix[prevIndex:prevIndex+4]) {
				if !changed {
					minX, maxX = x, x+1
					minY, maxY = y, y+1
					changed = true
				} else {
					minX = min(minX, x)
					minY = min(minY, y)
					maxX = max(maxX, x+1)
					maxY = max(maxY, y+1)
				}
			}
		}
	}

	if !changed {
		return image.Rectangle{}, false
	}

	margin := 6
	return image.Rect(
		max(bounds.Min.X, minX-margin),
		max(bounds.Min.Y, minY-margin),
		min(bounds.Max.X, maxX+margin),
		min(bounds.Max.Y, maxY+margin),
	), true
}

func pixelDiffers(a, b []byte) bool {
	const tolerance = 12
	diff := abs(int(a[0])-int(b[0])) + abs(int(a[1])-int(b[1])) + abs(int(a[2])-int(b[2]))
	return diff > tolerance
}

func fastFrameChanged(prev, curr *image.RGBA, step int) bool {
	if prev == nil || curr == nil || !sameBounds(prev, curr) {
		return true
	}
	if step < 2 {
		step = 2
	}

	bounds := curr.Bounds()
	baseX := bounds.Min.X
	baseY := bounds.Min.Y
	for y := bounds.Min.Y; y < bounds.Max.Y; y += step {
		currRow := (y - baseY) * curr.Stride
		prevRow := (y - baseY) * prev.Stride
		for x := bounds.Min.X; x < bounds.Max.X; x += step {
			offset := (x - baseX) * 4
			currIndex := currRow + offset
			prevIndex := prevRow + offset
			if pixelDiffers(curr.Pix[currIndex:currIndex+4], prev.Pix[prevIndex:prevIndex+4]) {
				return true
			}
		}
	}

	checkRows := []int{bounds.Min.Y, bounds.Min.Y + bounds.Dy()/2, bounds.Max.Y - 1}
	for _, y := range checkRows {
		if y < bounds.Min.Y || y >= bounds.Max.Y {
			continue
		}
		currRow := (y - baseY) * curr.Stride
		prevRow := (y - baseY) * prev.Stride
		for x := bounds.Min.X; x < bounds.Max.X; x += max(1, step/2) {
			offset := (x - baseX) * 4
			currIndex := currRow + offset
			prevIndex := prevRow + offset
			if pixelDiffers(curr.Pix[currIndex:currIndex+4], prev.Pix[prevIndex:prevIndex+4]) {
				return true
			}
		}
	}

	return false
}

func diffTiles(prev, curr *image.RGBA, tileSize int) []image.Rectangle {
	if prev == nil || curr == nil || !sameBounds(prev, curr) {
		if curr == nil {
			return nil
		}
		return []image.Rectangle{curr.Bounds()}
	}
	if tileSize <= 0 {
		tileSize = tilePatchSize
	}

	bounds := curr.Bounds()
	tiles := make([]image.Rectangle, 0, 8)
	for y := bounds.Min.Y; y < bounds.Max.Y; y += tileSize {
		for x := bounds.Min.X; x < bounds.Max.X; x += tileSize {
			rect := image.Rect(x, y, min(bounds.Max.X, x+tileSize), min(bounds.Max.Y, y+tileSize))
			if tileDiffers(prev, curr, rect) {
				tiles = append(tiles, rect)
				if len(tiles) > maxTileCount {
					return tiles
				}
			}
		}
	}
	return tiles
}

func tileDiffers(prev, curr *image.RGBA, rect image.Rectangle) bool {
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		currRow := (y - curr.Bounds().Min.Y) * curr.Stride
		prevRow := (y - prev.Bounds().Min.Y) * prev.Stride
		start := (rect.Min.X - curr.Bounds().Min.X) * 4
		end := (rect.Max.X - curr.Bounds().Min.X) * 4
		if end > start && bytes.Equal(curr.Pix[currRow+start:currRow+end], prev.Pix[prevRow+start:prevRow+end]) {
			continue
		}
		for x := rect.Min.X; x < rect.Max.X; x++ {
			offset := (x - curr.Bounds().Min.X) * 4
			currIndex := currRow + offset
			prevIndex := prevRow + offset
			if pixelDiffers(curr.Pix[currIndex:currIndex+4], prev.Pix[prevIndex:prevIndex+4]) {
				return true
			}
		}
	}
	return false
}

func buildTileAtlas(src *image.RGBA, rects []image.Rectangle, tileSize int) (*image.RGBA, []tilePatch) {
	if src == nil || len(rects) == 0 {
		return nil, nil
	}
	if tileSize <= 0 {
		tileSize = tilePatchSize
	}

	cols := int(math.Ceil(math.Sqrt(float64(len(rects)))))
	if cols < 1 {
		cols = 1
	}
	rows := int(math.Ceil(float64(len(rects)) / float64(cols)))
	atlas := image.NewRGBA(image.Rect(0, 0, cols*tileSize, rows*tileSize))
	tiles := make([]tilePatch, 0, len(rects))

	for index, rect := range rects {
		col := index % cols
		row := index / cols
		atlasX := col * tileSize
		atlasY := row * tileSize
		draw.Draw(atlas, image.Rect(atlasX, atlasY, atlasX+rect.Dx(), atlasY+rect.Dy()), src, rect.Min, draw.Src)
		tiles = append(tiles, tilePatch{
			X:      rect.Min.X,
			Y:      rect.Min.Y,
			W:      rect.Dx(),
			H:      rect.Dy(),
			AtlasX: atlasX,
			AtlasY: atlasY,
		})
	}

	return atlas, tiles
}

func cropRGBA(src *image.RGBA, rect image.Rectangle) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, rect.Dx(), rect.Dy()))
	draw.Draw(dst, dst.Bounds(), src, rect.Min, draw.Src)
	return dst
}

func cloneRGBA(src *image.RGBA) *image.RGBA {
	if src == nil {
		return nil
	}
	dst := image.NewRGBA(src.Bounds())
	copy(dst.Pix, src.Pix)
	return dst
}

func mergeSentPatchIntoBase(base, current *image.RGBA, rect image.Rectangle) *image.RGBA {
	if current == nil {
		return base
	}
	if base == nil || !sameBounds(base, current) {
		return cloneRGBA(current)
	}
	patchRect := rect.Intersect(current.Bounds())
	if patchRect.Empty() {
		return base
	}
	draw.Draw(base, patchRect, current, patchRect.Min, draw.Src)
	return base
}

func mergeSentTilesIntoBase(base, current *image.RGBA, tiles []tilePatch) *image.RGBA {
	if current == nil {
		return base
	}
	if base == nil || !sameBounds(base, current) {
		return cloneRGBA(current)
	}
	for _, tile := range tiles {
		rect := image.Rect(tile.X, tile.Y, tile.X+tile.W, tile.Y+tile.H).Intersect(current.Bounds())
		if rect.Empty() {
			continue
		}
		draw.Draw(base, rect, current, rect.Min, draw.Src)
	}
	return base
}

func sameBounds(a, b *image.RGBA) bool {
	if a == nil || b == nil {
		return false
	}
	return a.Bounds().Eq(b.Bounds())
}

func cloneTiles(src []tilePatch) []tilePatch {
	if len(src) == 0 {
		return nil
	}
	dst := make([]tilePatch, len(src))
	copy(dst, src)
	return dst
}

func setCursor(x, y int) error {
	ret, _, _ := procSetCursorPos.Call(uintptr(x), uintptr(y))
	if ret == 0 {
		return fmt.Errorf("SetCursorPos failed")
	}
	return nil
}

func mouseMove(x, y int) error {
	virtualW, _, _ := procGetSystemMetrics.Call(smCXVirtualScreen)
	virtualH, _, _ := procGetSystemMetrics.Call(smCYVirtualScreen)
	if virtualW <= 0 || virtualH <= 0 {
		virtualW, _, _ = procGetSystemMetrics.Call(smCXScreen)
		virtualH, _, _ = procGetSystemMetrics.Call(smCYScreen)
	}

	if virtualW <= 0 {
		virtualW = 1920
	}
	if virtualH <= 0 {
		virtualH = 1080
	}

	dx := int32(x * 65536 / int(virtualW))
	dy := int32(y * 65536 / int(virtualH))

	inp := input{
		Type: inputMouse,
		MI: mouseInput{
			DX:          dx,
			DY:          dy,
			MouseData:   0,
			DwFlags:     mouseeventfMove | mouseeventfAbsolute | mouseeventfVirtualDesk,
			Time:        0,
			DwExtraInfo: 0,
		},
	}

	ret, _, _ := procSendInput.Call(1, uintptr(unsafe.Pointer(&inp)), unsafe.Sizeof(inp))
	if ret == 0 {
		return fmt.Errorf("SendInput failed")
	}
	return nil
}

func mouseButton(x, y int, button string, down bool) error {
	var flags uint32
	switch strings.ToLower(button) {
	case "left":
		if down {
			flags = mouseeventfLeftDown
		} else {
			flags = mouseeventfLeftUp
		}
	case "right":
		if down {
			flags = mouseeventfRightDown
		} else {
			flags = mouseeventfRightUp
		}
	case "middle":
		if down {
			flags = mouseeventfMiddleDown
		} else {
			flags = mouseeventfMiddleUp
		}
	default:
		return fmt.Errorf("unknown mouse button: %s", button)
	}

	virtualW, _, _ := procGetSystemMetrics.Call(smCXVirtualScreen)
	virtualH, _, _ := procGetSystemMetrics.Call(smCYVirtualScreen)
	if virtualW <= 0 || virtualH <= 0 {
		virtualW, _, _ = procGetSystemMetrics.Call(smCXScreen)
		virtualH, _, _ = procGetSystemMetrics.Call(smCYScreen)
	}

	if virtualW <= 0 {
		virtualW = 1920
	}
	if virtualH <= 0 {
		virtualH = 1080
	}

	dx := int32(x * 65536 / int(virtualW))
	dy := int32(y * 65536 / int(virtualH))

	inp := input{
		Type: inputMouse,
		MI: mouseInput{
			DX:          dx,
			DY:          dy,
			MouseData:   0,
			DwFlags:     flags | mouseeventfAbsolute | mouseeventfVirtualDesk,
			Time:        0,
			DwExtraInfo: 0,
		},
	}

	ret, _, _ := procSendInput.Call(1, uintptr(unsafe.Pointer(&inp)), unsafe.Sizeof(inp))
	if ret == 0 {
		return fmt.Errorf("SendInput failed")
	}
	return nil
}

func mouseWheelEvent(x, y int, delta int) error {
	if delta == 0 {
		return nil
	}

	virtualW, _, _ := procGetSystemMetrics.Call(smCXVirtualScreen)
	virtualH, _, _ := procGetSystemMetrics.Call(smCYVirtualScreen)
	if virtualW <= 0 || virtualH <= 0 {
		virtualW, _, _ = procGetSystemMetrics.Call(smCXScreen)
		virtualH, _, _ = procGetSystemMetrics.Call(smCYScreen)
	}

	if virtualW <= 0 {
		virtualW = 1920
	}
	if virtualH <= 0 {
		virtualH = 1080
	}

	dx := int32(x * 65536 / int(virtualW))
	dy := int32(y * 65536 / int(virtualH))
	mouseData := uint32(int32(delta)) // delta 已是 WHEEL_DELTA 单位，直接透传

	inp := input{
		Type: inputMouse,
		MI: mouseInput{
			DX:          dx,
			DY:          dy,
			MouseData:   mouseData,
			DwFlags:     mouseeventfWheel | mouseeventfAbsolute | mouseeventfVirtualDesk,
			Time:        0,
			DwExtraInfo: 0,
		},
	}

	ret, _, _ := procSendInput.Call(1, uintptr(unsafe.Pointer(&inp)), unsafe.Sizeof(inp))
	if ret == 0 {
		return fmt.Errorf("SendInput failed")
	}
	return nil
}

func resolveKey(key, code string) (keySpec, error) {
	if spec, ok := keySpecFromCode(code); ok {
		return spec, nil
	}
	if spec, ok := keySpecFromKey(key); ok {
		return spec, nil
	}

	runes := []rune(key)
	if len(runes) == 1 {
		ret, _, _ := procVkKeyScan.Call(uintptr(runes[0]))
		vk := int16(ret)
		if vk == -1 {
			return keySpec{}, fmt.Errorf("unsupported key: %s", key)
		}
		return keySpec{VK: byte(vk & 0xFF)}, nil
	}

	return keySpec{}, fmt.Errorf("unsupported key: %s (%s)", key, code)
}

func isExtendedVK(vk byte) bool {
	switch vk {
	case vkRight, vkLeft, vkUp, vkDown, vkPageUp, vkPageDown, vkHome, vkEnd, vkInsert, vkDelete,
		vkRControl, vkRMenu, vkLWin, vkRWin, vkApps, vkDivide, vkReturn, vkNumLock, vkPrint:
		return true
	default:
		return false
	}
}

func keySpecFromCode(code string) (keySpec, bool) {
	switch code {
	case "Backspace":
		return keySpec{VK: vkBack}, true
	case "Tab":
		return keySpec{VK: vkTab}, true
	case "Enter":
		return keySpec{VK: vkReturn}, true
	case "NumpadEnter":
		return keySpec{VK: vkReturn, Extended: true}, true
	case "Escape":
		return keySpec{VK: vkEscape}, true
	case "Space":
		return keySpec{VK: vkSpace}, true
	case "PageUp":
		return keySpec{VK: vkPageUp, Extended: true}, true
	case "PageDown":
		return keySpec{VK: vkPageDown, Extended: true}, true
	case "End":
		return keySpec{VK: vkEnd, Extended: true}, true
	case "Home":
		return keySpec{VK: vkHome, Extended: true}, true
	case "ArrowLeft":
		return keySpec{VK: vkLeft, Extended: true}, true
	case "ArrowUp":
		return keySpec{VK: vkUp, Extended: true}, true
	case "ArrowRight":
		return keySpec{VK: vkRight, Extended: true}, true
	case "ArrowDown":
		return keySpec{VK: vkDown, Extended: true}, true
	case "Insert":
		return keySpec{VK: vkInsert, Extended: true}, true
	case "Delete":
		return keySpec{VK: vkDelete, Extended: true}, true
	case "PrintScreen":
		return keySpec{VK: vkPrint, Extended: true}, true
	case "Pause":
		return keySpec{VK: vkPause}, true
	case "CapsLock":
		return keySpec{VK: vkCapsLock}, true
	case "NumLock":
		return keySpec{VK: vkNumLock, Extended: true}, true
	case "ScrollLock":
		return keySpec{VK: vkScroll}, true
	case "ShiftLeft":
		return keySpec{VK: vkLShift}, true
	case "ShiftRight":
		return keySpec{VK: vkRShift}, true
	case "ControlLeft":
		return keySpec{VK: vkLControl}, true
	case "ControlRight":
		return keySpec{VK: vkRControl, Extended: true}, true
	case "AltLeft":
		return keySpec{VK: vkLMenu}, true
	case "AltRight":
		return keySpec{VK: vkRMenu, Extended: true}, true
	case "MetaLeft":
		return keySpec{VK: vkLWin, Extended: true}, true
	case "MetaRight":
		return keySpec{VK: vkRWin, Extended: true}, true
	case "ContextMenu":
		return keySpec{VK: vkApps, Extended: true}, true
	case "Minus":
		return keySpec{VK: vkMinus}, true
	case "Equal":
		return keySpec{VK: vkEqual}, true
	case "BracketLeft":
		return keySpec{VK: vkLBracket}, true
	case "BracketRight":
		return keySpec{VK: vkRBracket}, true
	case "Backslash":
		return keySpec{VK: vkBackslash}, true
	case "Semicolon":
		return keySpec{VK: vkSemicolon}, true
	case "Quote":
		return keySpec{VK: vkQuote}, true
	case "Backquote":
		return keySpec{VK: vkBacktick}, true
	case "Comma":
		return keySpec{VK: vkComma}, true
	case "Period":
		return keySpec{VK: vkPeriod}, true
	case "Slash":
		return keySpec{VK: vkSlash}, true
	case "NumpadMultiply":
		return keySpec{VK: vkMultiply}, true
	case "NumpadAdd":
		return keySpec{VK: vkAdd}, true
	case "NumpadSubtract":
		return keySpec{VK: vkSubtract}, true
	case "NumpadDecimal":
		return keySpec{VK: vkDecimal}, true
	case "NumpadDivide":
		return keySpec{VK: vkDivide, Extended: true}, true
	}

	if strings.HasPrefix(code, "Key") && len(code) == 4 {
		letter := code[3]
		if letter >= 'A' && letter <= 'Z' {
			return keySpec{VK: byte(vkA + letter - 'A')}, true
		}
	}
	if strings.HasPrefix(code, "Digit") && len(code) == 6 {
		digit := code[5]
		if digit >= '0' && digit <= '9' {
			return keySpec{VK: byte(vk0 + digit - '0')}, true
		}
	}
	if strings.HasPrefix(code, "Numpad") && len(code) == 7 {
		digit := code[6]
		if digit >= '0' && digit <= '9' {
			return keySpec{VK: byte(vkNumpad0 + digit - '0')}, true
		}
	}
	if strings.HasPrefix(code, "F") {
		if n, err := strconv.Atoi(strings.TrimPrefix(code, "F")); err == nil && n >= 1 && n <= 24 {
			return keySpec{VK: byte(vkF1 + n - 1)}, true
		}
	}

	return keySpec{}, false
}

func keySpecFromKey(key string) (keySpec, bool) {
	switch strings.ToLower(key) {
	case "enter":
		return keySpec{VK: vkReturn}, true
	case "backspace":
		return keySpec{VK: vkBack}, true
	case "tab":
		return keySpec{VK: vkTab}, true
	case "escape":
		return keySpec{VK: vkEscape}, true
	case " ":
		return keySpec{VK: vkSpace}, true
	case "arrowleft":
		return keySpec{VK: vkLeft, Extended: true}, true
	case "arrowup":
		return keySpec{VK: vkUp, Extended: true}, true
	case "arrowright":
		return keySpec{VK: vkRight, Extended: true}, true
	case "arrowdown":
		return keySpec{VK: vkDown, Extended: true}, true
	case "delete":
		return keySpec{VK: vkDelete, Extended: true}, true
	case "insert":
		return keySpec{VK: vkInsert, Extended: true}, true
	case "home":
		return keySpec{VK: vkHome, Extended: true}, true
	case "end":
		return keySpec{VK: vkEnd, Extended: true}, true
	case "pageup":
		return keySpec{VK: vkPageUp, Extended: true}, true
	case "pagedown":
		return keySpec{VK: vkPageDown, Extended: true}, true
	case "shift":
		return keySpec{VK: vkShift}, true
	case "control":
		return keySpec{VK: vkControl}, true
	case "alt":
		return keySpec{VK: vkMenu}, true
	case "meta":
		return keySpec{VK: vkLWin, Extended: true}, true
	}

	return keySpec{}, false
}

func enumerateMonitors() []desktopMetrics {
	var monitors []desktopMetrics
	callback := syscall.NewCallback(func(hMonitor, hdcMonitor, lprcMonitor, dwData uintptr) uintptr {
		var mi monitorInfo
		mi.cbSize = uint32(unsafe.Sizeof(mi))
		if ret, _, _ := procGetMonitorInfoW.Call(hMonitor, uintptr(unsafe.Pointer(&mi))); ret != 0 {
			monitors = append(monitors, desktopMetrics{
				Left:   int(mi.rcMonitor.Left),
				Top:    int(mi.rcMonitor.Top),
				Width:  int(mi.rcMonitor.Right - mi.rcMonitor.Left),
				Height: int(mi.rcMonitor.Bottom - mi.rcMonitor.Top),
			})
		}
		return 1
	})
	procEnumDisplayMonitors.Call(0, 0, callback, 0)
	if len(monitors) == 0 {
		return []desktopMetrics{virtualDesktopMetrics()}
	}
	return monitors
}

func (a *agent) getActiveMonitor() desktopMetrics {
	if a == nil {
		return virtualDesktopMetrics()
	}
	a.monitorsMu.Lock()
	defer a.monitorsMu.Unlock()

	// 通过 GetSystemMetrics 快速检测虚拟屏幕尺寸是否变化（O(1)）
	vw, _, _ := procGetSystemMetrics.Call(smCXVirtualScreen)
	vh, _, _ := procGetSystemMetrics.Call(smCYVirtualScreen)
	curW := int(vw)
	curH := int(vh)
	if curW > 0 && curH > 0 && (curW != a.lastVirtualW || curH != a.lastVirtualH) {
		a.lastVirtualW = curW
		a.lastVirtualH = curH
		a.monitors = enumerateMonitors() // 分辨率变化时重新枚举所有显示器
	}

	if len(a.monitors) == 0 {
		a.monitors = enumerateMonitors()
	}
	if a.activeMonitor < 0 || a.activeMonitor >= len(a.monitors) {
		a.activeMonitor = 0
	}
	return a.monitors[a.activeMonitor]
}

func (a *agent) switchMonitor() {
	if a == nil {
		return
	}
	a.monitorsMu.Lock()
	a.monitors = enumerateMonitors()
	// 切换显示器后更新虚拟屏幕缓存
	vw, _, _ := procGetSystemMetrics.Call(smCXVirtualScreen)
	vh, _, _ := procGetSystemMetrics.Call(smCYVirtualScreen)
	a.lastVirtualW = int(vw)
	a.lastVirtualH = int(vh)
	count := len(a.monitors)
	if count == 0 {
		a.monitors = []desktopMetrics{virtualDesktopMetrics()}
		count = 1
	}
	a.activeMonitor = (a.activeMonitor + 1) % count
	monitor := a.monitors[a.activeMonitor]
	a.captureMonitorGen.Add(1) // 递增代数，让正在运行的旧捕获跳过 origin 更新
	a.monitorsMu.Unlock()
	log.Printf("[agent][monitor] switched to monitor %d/%d: %dx%d @ (%d,%d)", a.activeMonitor+1, count, monitor.Width, monitor.Height, monitor.Left, monitor.Top)

	// 同步更新 origin 和 input 映射，避免与捕获循环竞争导致鼠标坐标错误
	metrics := a.captureMetrics(monitor)
	a.originX = monitor.Left
	a.originY = monitor.Top
	a.input.setDesktop(a.originX, a.originY, monitor.Width, monitor.Height, metrics.Width, metrics.Height)
	log.Printf("[agent][monitor] origin set to (%d,%d) desktop=%dx%d frame=%dx%d", a.originX, a.originY, monitor.Width, monitor.Height, metrics.Width, metrics.Height)

	// 注册到服务端异步执行，避免阻塞
	go func() {
		if err := a.register(metrics, monitor); err != nil {
			log.Printf("[agent][monitor] register failed: %v", err)
		}
	}()
}

func virtualDesktopMetrics() desktopMetrics {
	left, _, _ := procGetSystemMetrics.Call(smXVirtualScreen)
	top, _, _ := procGetSystemMetrics.Call(smYVirtualScreen)
	width, _, _ := procGetSystemMetrics.Call(smCXVirtualScreen)
	height, _, _ := procGetSystemMetrics.Call(smCYVirtualScreen)

	w := int(width)
	h := int(height)
	if w <= 0 || h <= 0 {
		width, _, _ = procGetSystemMetrics.Call(smCXScreen)
		height, _, _ = procGetSystemMetrics.Call(smCYScreen)
		return desktopMetrics{
			Left:   0,
			Top:    0,
			Width:  int(width),
			Height: int(height),
		}
	}

	return desktopMetrics{
		Left:   int(int32(left)),
		Top:    int(int32(top)),
		Width:  w,
		Height: h,
	}
}

func inputDesktopAvailable() bool {
	desktop, _, _ := procOpenInputDesktop.Call(0, 0, desktopRead)
	if desktop == 0 {
		return false
	}
	procCloseDesktop.Call(desktop)
	return true
}

func defaultDeviceID() string {
	return fallbackDeviceID()
}

func normalizeTransport(value string) string {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case "http":
		return "http"
	case "quic":
		return "quic"
	default:
		return "auto"
	}
}

func normalizeStreamMode(value, fallback string) string {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case "video":
		return "video"
	case "delta":
		return "delta"
	case "auto":
		if fallback == "" {
			return "auto"
		}
		return normalizeStreamMode(fallback, "")
	default:
		if fallback == "" {
			return ""
		}
		return normalizeStreamMode(fallback, "")
	}
}

func normalizeFrameCodec(value, fallback string) string {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case "jpeg", "mjpeg":
		return "jpeg"
	case "bgra", "raw-bgra", "raw":
		return "bgra"
	case "h264", "h264-mf":
		return "jpeg"
	case "auto":
		if fallback == "" {
			return "auto"
		}
		return normalizeFrameCodec(fallback, "")
	default:
		if fallback == "" {
			return ""
		}
		return normalizeFrameCodec(fallback, "")
	}
}

func normalizeCaptureBackend(value string) string {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case "dxgi", "desktop-dup", "desktop-duplication":
		return "dxgi"
	case "gdi", "bitblt":
		return "gdi"
	default:
		return "auto"
	}
}

func probeMFHelper(path string) (mfProbeResult, error) {
	var result mfProbeResult
	cmd := exec.Command(path, "probe")
	cmd.Dir = filepath.Dir(path)
	output, err := cmd.Output()
	if err != nil {
		return result, err
	}
	if err := json.Unmarshal(bytes.TrimSpace(output), &result); err != nil {
		return result, err
	}
	return result, nil
}

func startMFHelperSession(path, role string) (*mfHelperSession, mfSessionResponse, error) {
	var hello mfSessionResponse
	stderr := &bytes.Buffer{}
	cmd := exec.Command(path, "session")
	cmd.Dir = filepath.Dir(path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, hello, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, hello, err
	}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, hello, err
	}

	session := &mfHelperSession{
		path:   path,
		cmd:    cmd,
		stdin:  stdin,
		stdout: bufio.NewReader(stdout),
		stderr: stderr,
	}
	hello, _, err = session.exchange(mfSessionRequest{
		Type: "hello",
		Role: strings.TrimSpace(role),
	}, nil)
	if err != nil {
		session.Close()
		return nil, hello, err
	}
	return session, hello, nil
}

func (s *mfHelperSession) exchange(req mfSessionRequest, payload []byte) (mfSessionResponse, []byte, error) {
	var resp mfSessionResponse
	var body []byte
	s.mu.Lock()
	defer s.mu.Unlock()

	if s == nil || s.cmd == nil || s.stdin == nil || s.stdout == nil {
		return resp, nil, fmt.Errorf("helper session is not running")
	}
	req.PayloadSize = len(payload)
	header, err := json.Marshal(req)
	if err != nil {
		return resp, nil, err
	}
	if len(header) == 0 || len(header) > 1<<20 {
		return resp, nil, fmt.Errorf("helper header is too large")
	}
	prefix := make([]byte, 8)
	binary.BigEndian.PutUint32(prefix[:4], uint32(len(header)))
	binary.BigEndian.PutUint32(prefix[4:8], uint32(len(payload)))
	if _, err := s.stdin.Write(prefix); err != nil {
		return resp, nil, err
	}
	if _, err := s.stdin.Write(header); err != nil {
		return resp, nil, err
	}
	if len(payload) > 0 {
		if _, err := s.stdin.Write(payload); err != nil {
			return resp, nil, err
		}
	}
	if flusher, ok := s.stdin.(interface{ Flush() error }); ok {
		if err := flusher.Flush(); err != nil {
			return resp, nil, err
		}
	}
	if _, err := io.ReadFull(s.stdout, prefix); err != nil {
		return resp, nil, err
	}
	headerLen := int(binary.BigEndian.Uint32(prefix[:4]))
	payloadLen := int(binary.BigEndian.Uint32(prefix[4:8]))
	if headerLen <= 0 || headerLen > 1<<20 {
		return resp, nil, fmt.Errorf("helper response header size is invalid")
	}
	if payloadLen < 0 || payloadLen > 256<<20 {
		return resp, nil, fmt.Errorf("helper response payload size is invalid")
	}
	header = make([]byte, headerLen)
	if _, err := io.ReadFull(s.stdout, header); err != nil {
		return resp, nil, err
	}
	if payloadLen > 0 {
		body = make([]byte, payloadLen)
		if _, err := io.ReadFull(s.stdout, body); err != nil {
			return resp, nil, err
		}
	}
	if err := json.Unmarshal(bytes.TrimSpace(header), &resp); err != nil {
		return resp, nil, err
	}
	if resp.PayloadSize > 0 && len(body) != resp.PayloadSize {
		return resp, nil, fmt.Errorf("helper response payload size mismatch")
	}
	if !resp.OK && strings.TrimSpace(resp.Error) != "" {
		return resp, body, fmt.Errorf(resp.Error)
	}
	return resp, body, nil
}

func (s *mfHelperSession) call(reqType string) (mfSessionResponse, error) {
	resp, _, err := s.exchange(mfSessionRequest{Type: reqType}, nil)
	if err != nil {
		return resp, err
	}
	return resp, nil
}

func (s *mfHelperSession) Close() {
	if s == nil {
		return
	}

	activeHelperMu.Lock()
	if activeHelperSession == s {
		activeHelperSession = nil
	}
	activeHelperMu.Unlock()

	s.mu.Lock()
	cmd := s.cmd
	stdin := s.stdin
	s.cmd = nil
	s.stdin = nil
	s.stdout = nil
	s.mu.Unlock()

	if stdin != nil {
		_ = stdin.Close()
	}
	if cmd == nil {
		return
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		<-done
	}
}

func (a *agent) ensureMFSession(role string) {
	if a == nil || a.mfSession != nil || strings.TrimSpace(a.mfHelperPath) == "" {
		return
	}
	// Resolve helper path: relative paths are checked next to the agent exe
	path, found := resolveHelperPath(a.mfHelperPath)
	if !found {
		log.Printf("[agent][mf] helper not found at %s", a.mfHelperPath)
		return
	}
	session, hello, err := startMFHelperSession(path, role)
	if err != nil {
		log.Printf("[agent][mf] session start failed: %v", err)
		return
	}
	a.mfSession = session
	activeHelperMu.Lock()
	activeHelperSession = session
	activeHelperMu.Unlock()
	log.Printf("[agent][mf] session ready version=%s caps=%s encode=%s decode=%s", strings.TrimSpace(hello.Version), strings.Join(hello.Capabilities, ","), strings.Join(hello.Encode, ","), strings.Join(hello.Decode, ","))
}

func (a *agent) pingMFSession(role string) error {
	if a == nil || strings.TrimSpace(a.mfHelperPath) == "" {
		return nil
	}
	if a.mfSession == nil {
		a.ensureMFSession(role)
		if a.mfSession == nil {
			return fmt.Errorf("helper session unavailable")
		}
	}
	if _, err := a.mfSession.call("ping"); err == nil {
		return nil
	}
	a.mfSession.Close()
	a.mfSession = nil
	a.ensureMFSession(role)
	if a.mfSession == nil {
		return fmt.Errorf("helper session unavailable")
	}
	return nil
}

func resolveHelperPath(raw string) (string, bool) {
	candidate := strings.TrimSpace(raw)
	if candidate == "" {
		return "", false
	}
	if fileExists(candidate) {
		path, err := filepath.Abs(candidate)
		if err != nil {
			return candidate, true
		}
		return path, true
	}
	if exePath, err := os.Executable(); err == nil {
		joined := filepath.Join(filepath.Dir(exePath), candidate)
		if fileExists(joined) {
			return joined, true
		}
	}
	return "", false
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func mfHelperLabel(path string) string {
	if path == "" {
		return "off"
	}
	return path
}

func clamp(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func formatBytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%dB", n)
	}
	value := float64(n)
	units := []string{"KB", "MB", "GB", "TB"}
	for _, unit := range units {
		value /= 1024
		if value < 1024 || unit == units[len(units)-1] {
			return fmt.Sprintf("%.2f%s", value, unit)
		}
	}
	return fmt.Sprintf("%dB", n)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func uintptrSigned(v int) uintptr {
	return uintptr(int32(v))
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func enableDPIAwareness() {
	if procSetProcessDpiAwarenessContext.Find() == nil {
		if result, _, _ := procSetProcessDpiAwarenessContext.Call(dpiAwarenessContextPerMonitorAwareV2); result != 0 {
			return
		}
	}
	procSetProcessDPIAware.Call()
}

func classifyCursorHandle(handle uintptr) string {
	if handle == 0 {
		return "arrow"
	}
	if cached, ok := cursorHandleKindCache.Load(handle); ok {
		return cached.(string)
	}
	for _, entry := range standardCursorKinds {
		cursor, _, _ := procLoadCursorW.Call(0, entry.ID)
		if cursor != 0 && cursor == handle {
			cursorHandleKindCache.Store(handle, entry.Kind)
			return entry.Kind
		}
	}
	if kind, ok := classifyCursorByFingerprint(handle); ok {
		cursorHandleKindCache.Store(handle, kind)
		return kind
	}
	cursorHandleKindCache.Store(handle, "arrow")
	return "arrow"
}

func classifyCursorByFingerprint(handle uintptr) (string, bool) {
	standardCursorFingerprintOnce.Do(initStandardCursorFingerprints)
	fp, ok := fingerprintCursorHandle(handle)
	if !ok {
		return "", false
	}
	if kind, ok := standardCursorFingerprintExact[fp.exactKey()]; ok {
		return kind, true
	}
	if kind, ok := standardCursorFingerprintBitmap[fp.bitmapKey()]; ok {
		return kind, true
	}
	return "", false
}

func initStandardCursorFingerprints() {
	standardCursorFingerprintExact = make(map[string]string, len(standardCursorKinds))
	standardCursorFingerprintBitmap = make(map[string]string, len(standardCursorKinds))
	for _, entry := range standardCursorKinds {
		cursor, _, _ := procLoadCursorW.Call(0, entry.ID)
		if cursor == 0 {
			continue
		}
		fp, ok := fingerprintCursorHandle(cursor)
		if !ok {
			continue
		}
		standardCursorFingerprintExact[fp.exactKey()] = entry.Kind
		if _, exists := standardCursorFingerprintBitmap[fp.bitmapKey()]; !exists {
			standardCursorFingerprintBitmap[fp.bitmapKey()] = entry.Kind
		}
	}
}

func fingerprintCursorHandle(handle uintptr) (cursorFingerprint, bool) {
	var ii iconInfo
	if ret, _, _ := procGetIconInfo.Call(handle, uintptr(unsafe.Pointer(&ii))); ret == 0 {
		return cursorFingerprint{}, false
	}
	if ii.HbmMask != 0 {
		defer procDeleteObject.Call(ii.HbmMask)
	}
	if ii.HbmColor != 0 {
		defer procDeleteObject.Call(ii.HbmColor)
	}
	maskW, maskH, maskHash, ok := fingerprintBitmapHandle(ii.HbmMask)
	if !ok {
		return cursorFingerprint{}, false
	}
	colorW, colorH, colorHash, colorOK := fingerprintBitmapHandle(ii.HbmColor)
	if ii.HbmColor != 0 && !colorOK {
		return cursorFingerprint{}, false
	}
	width := maskW
	height := maskH
	if colorW > 0 && colorH > 0 {
		width = colorW
		height = colorH
	}
	return cursorFingerprint{
		Width:     width,
		Height:    height,
		HotX:      ii.XHotspot,
		HotY:      ii.YHotspot,
		ColorHash: colorHash,
		MaskHash:  maskHash,
	}, true
}

func fingerprintBitmapHandle(bitmap uintptr) (int32, int32, uint64, bool) {
	if bitmap == 0 {
		return 0, 0, 0, true
	}
	data, width, height, ok := readBitmapPixels(bitmap)
	if !ok {
		return 0, 0, 0, false
	}
	return width, height, hashBytes64(data), true
}

func readBitmapPixels(bitmap uintptr) ([]byte, int32, int32, bool) {
	var bm bitmapObject
	if ret, _, _ := procGetObjectW.Call(bitmap, unsafe.Sizeof(bm), uintptr(unsafe.Pointer(&bm))); ret == 0 {
		return nil, 0, 0, false
	}
	width := bm.Width
	height := bm.Height
	if width <= 0 || height == 0 {
		return nil, 0, 0, false
	}
	if height < 0 {
		height = -height
	}
	screenDC, _, _ := procGetDC.Call(0)
	if screenDC == 0 {
		return nil, 0, 0, false
	}
	defer procReleaseDC.Call(0, screenDC)

	var bi bitmapInfo
	bi.Header.Size = uint32(unsafe.Sizeof(bi.Header))
	bi.Header.Width = width
	bi.Header.Height = -height
	bi.Header.Planes = 1
	bi.Header.BitCount = 32
	bi.Header.Compression = biRGB

	pixels := make([]byte, int(width)*int(height)*4)
	ret, _, _ := procGetDIBits.Call(
		screenDC,
		bitmap,
		0,
		uintptr(height),
		uintptr(unsafe.Pointer(&pixels[0])),
		uintptr(unsafe.Pointer(&bi)),
		dibRGB,
	)
	if ret == 0 {
		return nil, 0, 0, false
	}
	return pixels, width, height, true
}

func hashBytes64(data []byte) uint64 {
	const (
		offset64 = 1469598103934665603
		prime64  = 1099511628211
	)
	hash := uint64(offset64)
	for _, b := range data {
		hash ^= uint64(b)
		hash *= prime64
	}
	return hash
}

func (fp cursorFingerprint) exactKey() string {
	return fmt.Sprintf("%d:%d:%d:%d:%016x:%016x", fp.Width, fp.Height, fp.HotX, fp.HotY, fp.ColorHash, fp.MaskHash)
}

func (fp cursorFingerprint) bitmapKey() string {
	return fmt.Sprintf("%d:%d:%016x:%016x", fp.Width, fp.Height, fp.ColorHash, fp.MaskHash)
}

func (a *agent) logErrorf(format string, args ...any) {
	now := time.Now()
	if now.Sub(a.lastErrorLog) < 2*time.Second {
		return
	}
	a.lastErrorLog = now
	log.Printf(format, args...)
}

func (a *agent) noteUpload(mode, codec string, size, frameW, frameH int) {
	if a == nil || size <= 0 {
		return
	}
	now := time.Now()
	if a.txStatAt.IsZero() {
		a.txStatAt = now
	}
	a.txStatBytes += int64(size)
	a.txStatFrames++
	a.txStatLast = mode
	a.txStatCodec = codec
	a.txFrameW = frameW
	a.txFrameH = frameH
	if a.txAvgBytes <= 0 {
		a.txAvgBytes = float64(size)
	} else {
		a.txAvgBytes = a.txAvgBytes*0.82 + float64(size)*0.18
	}
	elapsed := now.Sub(a.txStatAt)
	if elapsed < 2*time.Second {
		return
	}
	a.txFPS = float64(a.txStatFrames) / elapsed.Seconds()
	a.txBPS = float64(a.txStatBytes) / elapsed.Seconds()
	sizeLabel := ""
	if a.txFrameW > 0 && a.txFrameH > 0 {
		if a.txStatLast == "patch" || a.txStatLast == "tiles" {
			sizeLabel = fmt.Sprintf(" rect=%dx%d", a.txFrameW, a.txFrameH)
		} else {
			sizeLabel = fmt.Sprintf(" frame=%dx%d", a.txFrameW, a.txFrameH)
		}
	}
	log.Printf("[agent][tx] view=%s mode=%s codec=%s fps=%.1f rate=%s/s avg=%s pressure=%.2f%s", nonEmpty(a.viewerKind, "idle"), a.effectiveStreamMode(), nonEmpty(a.txStatCodec, codec), a.txFPS, formatBytes(int64(a.txBPS)), formatBytes(int64(a.txAvgBytes)), a.uploadPressure(), sizeLabel)
	a.txStatAt = now
	a.txStatBytes = 0
	a.txStatFrames = 0
	a.txStatLast = ""
}

func (a *agent) logProfileDecision() {
	if a == nil {
		return
	}
	helper := "off"
	if a.mfSession != nil {
		helper = "on"
	}
	selectedCodec := a.effectiveFrameCodec()
	summary := fmt.Sprintf("transport=%s viewer=%s active=%t mode=%s suggestedMode=%s codec=%s suggestedCodec=%s fps=%d q=%d full=%d helper=%s", nonEmpty(a.activeTransport, a.transport), nonEmpty(a.viewerKind, "idle"), a.viewerActive, a.effectiveStreamMode(), nonEmpty(a.streamMode, "-"), selectedCodec, nonEmpty(a.streamCodec, "-"), a.streamFPS, a.streamQuality, a.streamFullMS, helper)
	if summary == a.lastProfileLog {
		return
	}
	a.lastProfileLog = summary
	log.Printf("[agent][profile] %s", summary)
}

func (a *agent) targetBudgetBPS() float64 {
	if a == nil {
		return 500 * 1024
	}
	switch a.viewerKind {
	case "controller":
		return 500 * 1024
	case "web":
		return 128 * 1024
	default:
		if a.viewerActive {
			return 192 * 1024
		}
		return 64 * 1024
	}
}

func (a *agent) targetStreamFPS() int {
	if a == nil {
		return 1
	}
	fps := a.streamFPS
	switch {
	case fps <= 0 && a.viewerKind == "controller":
		fps = 30
	case fps <= 0 && a.viewerActive:
		fps = 4
	case fps <= 0:
		fps = 1
	}
	if a.viewerKind == "controller" && fps < 12 {
		fps = 12
	}
	return clamp(fps, 1, 60)
}

func (a *agent) uploadPressure() float64 {
	if a == nil || a.txAvgBytes <= 0 {
		return 0
	}
	target := a.targetBudgetBPS()
	if target <= 0 {
		return 0
	}
	predicted := a.txAvgBytes * float64(a.targetStreamFPS())
	if a.txBPS > predicted {
		predicted = predicted*0.6 + a.txBPS*0.4
	}
	return predicted / target
}

func (a *agent) shouldPreferDeltaInVideo(fullBounds, patchRect image.Rectangle) bool {
	if normalizeFrameCodec(a.effectiveFrameCodec(), "") == "h264-mf" {
		return false
	}
	fullArea := max(1, fullBounds.Dx()*fullBounds.Dy())
	patchArea := max(0, patchRect.Dx()*patchRect.Dy())
	if patchArea == 0 {
		return false
	}
	ratio := float64(patchArea) / float64(fullArea)
	pressure := a.uploadPressure()
	if a != nil && a.viewerKind == "controller" && a.activeTransport != "quic" {
		switch {
		case pressure >= 1.40:
			return ratio <= 0.85
		case pressure >= 1.00:
			return ratio <= 0.65
		default:
			return ratio <= 0.42
		}
	}
	switch {
	case pressure >= 0.90:
		return ratio <= 0.72
	case pressure >= 0.72:
		return ratio <= 0.42
	default:
		return ratio <= 0.16
	}
}

func (a *agent) shouldUseHybridPatchInVideo(fullBounds, patchRect image.Rectangle) bool {
	if a == nil || normalizeFrameCodec(a.effectiveFrameCodec(), "") != "h264-mf" {
		return false
	}
	fullArea := max(1, fullBounds.Dx()*fullBounds.Dy())
	patchArea := max(0, patchRect.Dx()*patchRect.Dy())
	if patchArea == 0 {
		return false
	}
	ratio := float64(patchArea) / float64(fullArea)
	pressure := a.uploadPressure()
	switch {
	case pressure >= 1.8:
		return ratio <= 0.24
	case pressure >= 1.2:
		return ratio <= 0.16
	default:
		return ratio <= 0.08
	}
}

func (a *agent) shouldUseHybridTilesInVideo(fullBounds image.Rectangle, rects []image.Rectangle) bool {
	if a == nil || normalizeFrameCodec(a.effectiveFrameCodec(), "") != "h264-mf" {
		return false
	}
	if len(rects) < 2 {
		return false
	}
	fullArea := max(1, fullBounds.Dx()*fullBounds.Dy())
	totalArea := 0
	for _, rect := range rects {
		totalArea += max(0, rect.Dx()*rect.Dy())
	}
	if totalArea <= 0 {
		return false
	}
	ratio := float64(totalArea) / float64(fullArea)
	pressure := a.uploadPressure()
	switch {
	case pressure >= 1.8:
		return ratio <= 0.30
	case pressure >= 1.2:
		return ratio <= 0.18
	default:
		return ratio <= 0.10
	}
}

func (a *agent) shouldPreferTileDelta(fullBounds image.Rectangle, rects []image.Rectangle) bool {
	if normalizeFrameCodec(a.effectiveFrameCodec(), "") == "h264-mf" {
		return false
	}
	if len(rects) < 2 {
		return false
	}
	fullArea := max(1, fullBounds.Dx()*fullBounds.Dy())
	totalArea := 0
	for _, rect := range rects {
		totalArea += max(0, rect.Dx()*rect.Dy())
	}
	if totalArea <= 0 {
		return false
	}
	ratio := float64(totalArea) / float64(fullArea)
	pressure := a.uploadPressure()
	if a != nil && a.viewerKind == "controller" && a.activeTransport != "quic" {
		switch {
		case pressure >= 1.40:
			return ratio <= 0.92
		case pressure >= 1.00:
			return ratio <= 0.78
		default:
			return ratio <= 0.55
		}
	}
	switch {
	case pressure >= 0.90:
		return ratio <= 0.80
	case pressure >= 0.72:
		return ratio <= 0.48
	default:
		return ratio <= 0.22
	}
}

func (a *agent) streamInterval() (time.Duration, bool) {
	if !a.viewerActive {
		return 0, false
	}
	fps := a.streamFPS
	if fps <= 0 {
		fps = 6
	}
	if a.viewerKind == "controller" {
		minFPS := 12
		pressure := a.uploadPressure()
		if a.effectiveStreamMode() == "video" && pressure < 0.60 {
			minFPS = 24
		} else if pressure < 0.82 {
			minFPS = 16
		}
		if fps < minFPS {
			fps = minFPS
		}
	}
	if fps > 60 {
		fps = 60
	}
	return time.Second / time.Duration(fps), true
}

func nonEmpty(v, fallback string) string {
	v = strings.TrimSpace(v)
	if v != "" {
		return v
	}
	return fallback
}

func (a *agent) shouldHoldH264() bool {
	if a == nil || a.mfSession == nil || a.viewerKind != "controller" {
		return false
	}
	if normalizeFrameCodec(a.streamCodec, "") == "h264-mf" {
		return true
	}
	return !a.lastH264At.IsZero()
}

func (a *agent) streamFullRefreshInterval() time.Duration {
	if a.streamFullMS > 0 {
		refresh := time.Duration(a.streamFullMS) * time.Millisecond
		if refresh < 800*time.Millisecond {
			return 800 * time.Millisecond
		}
		if refresh > 12*time.Second {
			return 12 * time.Second
		}
		return refresh
	}
	if a.viewerKind == "controller" {
		return 2 * time.Second
	}
	if a.viewerActive {
		return 6 * time.Second
	}
	return 12 * time.Second
}

func (a *agent) shouldForceFullCapture(now time.Time) bool {
	if a == nil || a.lastFullShot.IsZero() {
		return true
	}
	if now.Sub(a.lastFullShot) <= a.streamFullRefreshInterval() {
		return false
	}
	return !a.shouldDeferActiveFull(now)
}

func (a *agent) shouldDeferActiveFull(now time.Time) bool {
	if a == nil || !a.viewerActive || a.viewerKind != "controller" || !a.videoPatchReady {
		return false
	}
	if a.lastShot.IsZero() || now.Sub(a.lastShot) > 900*time.Millisecond {
		return false
	}
	// Full JPEG refresh is expensive on 2K/fullscreen. During smooth incremental
	// motion, postpone it briefly and still force an occasional resync.
	return !a.lastFullShot.IsZero() && now.Sub(a.lastFullShot) < 30*time.Second
}

func (a *agent) effectiveStreamMode() string {
	if mode := normalizeStreamMode(a.modeOverride, ""); mode != "" && mode != "auto" {
		return mode
	}
	if a != nil && a.viewerKind == "controller" && a.activeTransport != "quic" {
		return "video"
	}
	if a != nil && a.shouldHoldH264() {
		return "video"
	}
	if mode := normalizeStreamMode(a.streamMode, ""); mode != "" && mode != "auto" {
		return mode
	}
	if a.viewerKind == "controller" {
		return "video"
	}
	return "delta"
}

func (a *agent) effectiveFrameCodec() string {
	if a == nil {
		return "jpeg"
	}
	if a.effectiveStreamMode() != "video" {
		return "jpeg"
	}
	switch normalizeFrameCodec(a.codecOverride, "auto") {
	case "auto":
		if a.activeTransport != "quic" {
			return "jpeg"
		}
		if a.shouldHoldH264() {
			return "h264-mf"
		}
		return "jpeg"
	case "h264-mf":
		if a.mfSession != nil {
			return "h264-mf"
		}
		return "jpeg"
	default:
		return "jpeg"
	}
}

func (a *agent) effectiveQuality() int {
	pressure := a.uploadPressure()
	if a.streamQuality > 0 {
		q := clamp(a.streamQuality, 20, 100)
		if a.viewerKind == "controller" {
			if a.effectiveStreamMode() == "video" {
				if q >= 95 {
					if pressure >= 1.05 {
						return clamp(max(q-12, 70), 60, 90)
					}
					if pressure >= 0.85 {
						return clamp(max(q-6, 76), 66, 96)
					}
					return clamp(max(q, 82), 70, 100)
				}
				if pressure >= 1.05 {
					return clamp(max(q-10, 42), 40, 82)
				}
				if pressure >= 0.85 {
					return clamp(max(q-6, 46), 42, 86)
				}
				return clamp(max(q, 54), 46, 90)
			}
			if pressure >= 0.90 {
				return clamp(max(q-6, 42), 40, 82)
			}
			return clamp(max(q, 46), 40, 88)
		}
		return clamp(q, 30, 100)
	}
	if a.viewerKind == "controller" {
		if a.effectiveStreamMode() == "video" {
			if pressure >= 1.05 {
				return clamp(max(a.quality-10, 42), 40, 80)
			}
			if pressure >= 0.85 {
				return clamp(max(a.quality-6, 46), 42, 84)
			}
			return clamp(max(a.quality, 54), 46, 88)
		}
		if pressure >= 0.90 {
			return clamp(max(a.quality-6, 42), 40, 80)
		}
		return clamp(max(a.quality, 46), 42, 82)
	}
	if a.viewerActive {
		return clamp(max(a.quality, 50), 50, 85)
	}
	return a.quality
}

func (a *agent) fullFrameQuality(base int) int {
	pressure := a.uploadPressure()
	if a.viewerKind == "controller" {
		if a.effectiveStreamMode() == "video" {
			if a.activeTransport != "quic" {
				if a.streamQuality >= 95 {
					if pressure >= 1.35 {
						return clamp(max(base-8, 72), 62, 88)
					}
					if pressure >= 1.00 {
						return clamp(max(base-5, 78), 70, 94)
					}
					return clamp(max(base, 86), 76, 100)
				}
				if pressure >= 1.35 {
					return clamp(max(base-8, 46), 44, 76)
				}
				if pressure >= 1.00 {
					return clamp(max(base-5, 50), 48, 80)
				}
				return clamp(max(base-2, 54), 50, 82)
			}
			if pressure >= 1.05 {
				return clamp(max(base-6, 42), 40, 82)
			}
			if pressure >= 0.85 {
				return clamp(max(base-3, 46), 44, 86)
			}
			return clamp(max(base, 56), 48, 90)
		}
		if pressure >= 0.90 {
			return clamp(max(base-4, 44), 42, 80)
		}
		return clamp(max(base, 48), 44, 84)
	}
	return clamp(base, 35, 100)
}

func (a *agent) patchQuality(base int, fullBounds, patchRect image.Rectangle) int {
	bonus := a.patchBoost
	fullArea := fullBounds.Dx() * fullBounds.Dy()
	patchArea := patchRect.Dx() * patchRect.Dy()
	if fullArea > 0 {
		ratio := float64(patchArea) / float64(fullArea)
		switch {
		case ratio <= 0.05:
			bonus = a.patchBoost + 6
		case ratio <= 0.15:
			bonus = a.patchBoost + 2
		case ratio <= 0.30:
			bonus = max(0, a.patchBoost-2)
		default:
			bonus = max(0, a.patchBoost-4)
		}
	}
	maxQuality := 88
	if a.streamQuality >= 95 {
		maxQuality = 100
	}
	return clamp(base+bonus, max(base, 50), maxQuality)
}

func (a *agent) tilesQuality(base int, fullBounds image.Rectangle, tiles []tilePatch) int {
	if len(tiles) == 0 {
		return clamp(base+a.tileBoost, max(base, 48), 86)
	}

	totalArea := 0
	for _, tile := range tiles {
		totalArea += max(0, tile.W) * max(0, tile.H)
	}
	fullArea := fullBounds.Dx() * fullBounds.Dy()
	bonus := a.tileBoost
	if fullArea > 0 {
		ratio := float64(totalArea) / float64(fullArea)
		switch {
		case ratio <= 0.08:
			bonus = a.tileBoost + 6
		case ratio <= 0.20:
			bonus = a.tileBoost + 2
		default:
			bonus = max(0, a.tileBoost-2)
		}
	}
	maxQuality := 86
	if a.streamQuality >= 95 {
		maxQuality = 100
	}
	return clamp(base+bonus, max(base, 48), maxQuality)
}

func loadOrCreateIdentity(overrideID, overrideName string) (clientIdentity, error) {
	identity := clientIdentity{}
	path := clientIdentityPath()

	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &identity)
	}

	if strings.TrimSpace(overrideID) != "" {
		identity.DeviceID = strings.TrimSpace(overrideID)
	}
	if strings.TrimSpace(overrideName) != "" {
		identity.Name = strings.TrimSpace(overrideName)
	}

	if strings.TrimSpace(identity.DeviceID) == "" {
		identity.DeviceID = fallbackDeviceID()
	}
	if strings.TrimSpace(identity.Name) == "" {
		identity.Name = fallbackDeviceName()
	}

	data, err := json.MarshalIndent(identity, "", "  ")
	if err != nil {
		return identity, err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return identity, err
	}
	return identity, nil
}

func clientIdentityPath() string {
	exePath, err := os.Executable()
	if err == nil {
		return filepathJoin(dirOf(exePath), "client.identity.json")
	}
	wd, err := os.Getwd()
	if err == nil {
		return filepathJoin(wd, "client.identity.json")
	}
	return "client.identity.json"
}

func fallbackDeviceID() string {
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err == nil {
		return hex.EncodeToString(buf)
	}
	return "agent-fallback"
}

func fallbackDeviceName() string {
	name, err := os.Hostname()
	if err != nil || strings.TrimSpace(name) == "" {
		return "Windows Client"
	}
	return name
}

func newLowLatencyHTTPClient(timeout time.Duration) *http.Client {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ForceAttemptHTTP2:     false,
		DisableCompression:    true,
		MaxIdleConns:          16,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ExpectContinueTimeout: time.Second,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			conn, err := (&net.Dialer{
				Timeout:   5 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext(ctx, network, address)
			if err != nil {
				return nil, err
			}
			configureClientTCP(conn)
			return conn, nil
		},
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
	}
}

func configureClientTCP(conn net.Conn) {
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetNoDelay(true)
		_ = tcp.SetKeepAlive(true)
		_ = tcp.SetKeepAlivePeriod(30 * time.Second)
	}
}

func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '\\' || path[i] == '/' {
			return path[:i]
		}
	}
	return "."
}

func filepathJoin(dir, name string) string {
	if dir == "" || dir == "." {
		return name
	}
	last := dir[len(dir)-1]
	if last == '\\' || last == '/' {
		return dir + name
	}
	return dir + `\` + name
}
