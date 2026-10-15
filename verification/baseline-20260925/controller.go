package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"image/png"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/gorilla/websocket"
	quic "github.com/quic-go/quic-go"
)

var (
	ctrlUser32                        = syscall.NewLazyDLL("user32.dll")
	ctrlGdi32                         = syscall.NewLazyDLL("gdi32.dll")
	ctrlKernel32                      = syscall.NewLazyDLL("kernel32.dll")
	ctrlImm32                         = syscall.NewLazyDLL("imm32.dll")
	ctrlDwmapi                        = syscall.NewLazyDLL("dwmapi.dll")
	ctrlShell32                       = syscall.NewLazyDLL("shell32.dll")
	ctrlUxTheme                       = syscall.NewLazyDLL("uxtheme.dll")
	ctrlOle32                         = syscall.NewLazyDLL("ole32.dll")
	procRegisterClassExW              = ctrlUser32.NewProc("RegisterClassExW")
	procCreateWindowExW               = ctrlUser32.NewProc("CreateWindowExW")
	procDefWindowProcW                = ctrlUser32.NewProc("DefWindowProcW")
	procDestroyWindow                 = ctrlUser32.NewProc("DestroyWindow")
	procDispatchMessageW              = ctrlUser32.NewProc("DispatchMessageW")
	procGetMessageW                   = ctrlUser32.NewProc("GetMessageW")
	procTranslateMessage              = ctrlUser32.NewProc("TranslateMessage")
	procPostQuitMessage               = ctrlUser32.NewProc("PostQuitMessage")
	procShowWindow                    = ctrlUser32.NewProc("ShowWindow")
	procUpdateWindow                  = ctrlUser32.NewProc("UpdateWindow")
	procLoadCursorW                   = ctrlUser32.NewProc("LoadCursorW")
	procGetIconInfo                   = ctrlUser32.NewProc("GetIconInfo")
	procCreateIconIndirect            = ctrlUser32.NewProc("CreateIconIndirect")
	procDestroyIcon                   = ctrlUser32.NewProc("DestroyIcon")
	procDrawIconEx                    = ctrlUser32.NewProc("DrawIconEx")
	procBeginPaint                    = ctrlUser32.NewProc("BeginPaint")
	procEndPaint                      = ctrlUser32.NewProc("EndPaint")
	procGetClientRect                 = ctrlUser32.NewProc("GetClientRect")
	procInvalidateRect                = ctrlUser32.NewProc("InvalidateRect")
	procSetWindowTextW                = ctrlUser32.NewProc("SetWindowTextW")
	procGetWindowTextW                = ctrlUser32.NewProc("GetWindowTextW")
	procGetWindowTextLengthW          = ctrlUser32.NewProc("GetWindowTextLengthW")
	procSendMessageW                  = ctrlUser32.NewProc("SendMessageW")
	procGetWindowRect                 = ctrlUser32.NewProc("GetWindowRect")
	procAdjustWindowRectEx            = ctrlUser32.NewProc("AdjustWindowRectEx")
	procGetDC                         = ctrlUser32.NewProc("GetDC")
	procReleaseDC                     = ctrlUser32.NewProc("ReleaseDC")
	procGetCursorPos                  = ctrlUser32.NewProc("GetCursorPos")
	procScreenToClient                = ctrlUser32.NewProc("ScreenToClient")
	procSetCursor                     = ctrlUser32.NewProc("SetCursor")
	procSetCapture                    = ctrlUser32.NewProc("SetCapture")
	procReleaseCapture                = ctrlUser32.NewProc("ReleaseCapture")
	procSetWindowLongW                = ctrlUser32.NewProc("SetWindowLongW")
	procGetWindowLongW                = ctrlUser32.NewProc("GetWindowLongW")
	procSetWindowLongPtrW             = ctrlUser32.NewProc("SetWindowLongPtrW")
	procGetWindowLongPtrW             = ctrlUser32.NewProc("GetWindowLongPtrW")
	procSetWindowPos                  = ctrlUser32.NewProc("SetWindowPos")
	procMonitorFromWindow             = ctrlUser32.NewProc("MonitorFromWindow")
	procGetMonitorInfoW               = ctrlUser32.NewProc("GetMonitorInfoW")
	procFillRect                      = ctrlUser32.NewProc("FillRect")
	procOpenClipboard                 = ctrlUser32.NewProc("OpenClipboard")
	procCloseClipboard                = ctrlUser32.NewProc("CloseClipboard")
	procEmptyClipboard                = ctrlUser32.NewProc("EmptyClipboard")
	procGetClipboardData              = ctrlUser32.NewProc("GetClipboardData")
	procSetClipboardData              = ctrlUser32.NewProc("SetClipboardData")
	procIsClipboardFormatAvail        = ctrlUser32.NewProc("IsClipboardFormatAvailable")
	procGetClipboardSequenceNumber    = ctrlUser32.NewProc("GetClipboardSequenceNumber")
	procRegisterClipboardFormatW      = ctrlUser32.NewProc("RegisterClipboardFormatW")
	procAddClipboardFormatListener    = ctrlUser32.NewProc("AddClipboardFormatListener")
	procRemoveClipboardFormatListener = ctrlUser32.NewProc("RemoveClipboardFormatListener")
	procSetProcessDPIAware            = ctrlUser32.NewProc("SetProcessDPIAware")
	procSetProcessDpiAwarenessContext = ctrlUser32.NewProc("SetProcessDpiAwarenessContext")
	procSetWindowsHookExW             = ctrlUser32.NewProc("SetWindowsHookExW")
	procCallNextHookEx                = ctrlUser32.NewProc("CallNextHookEx")
	procUnhookWindowsHookEx           = ctrlUser32.NewProc("UnhookWindowsHookEx")
	procGetForegroundWindow           = ctrlUser32.NewProc("GetForegroundWindow")
	procDragAcceptFiles               = ctrlShell32.NewProc("DragAcceptFiles")
	procDragQueryFileW                = ctrlShell32.NewProc("DragQueryFileW")
	procDragFinish                    = ctrlShell32.NewProc("DragFinish")
	procSHBrowseForFolderW            = ctrlShell32.NewProc("SHBrowseForFolderW")
	procSHGetPathFromIDListW          = ctrlShell32.NewProc("SHGetPathFromIDListW")
	procSetWindowTheme                = ctrlUxTheme.NewProc("SetWindowTheme")
	procOleInitialize                 = ctrlOle32.NewProc("OleInitialize")
	procOleUninitialize               = ctrlOle32.NewProc("OleUninitialize")
	procCoTaskMemFree                 = ctrlOle32.NewProc("CoTaskMemFree")
	procGetModuleHandleW              = ctrlKernel32.NewProc("GetModuleHandleW")
	procGlobalAlloc                   = ctrlKernel32.NewProc("GlobalAlloc")
	procGlobalLock                    = ctrlKernel32.NewProc("GlobalLock")
	procGlobalUnlock                  = ctrlKernel32.NewProc("GlobalUnlock")
	procGlobalFree                    = ctrlKernel32.NewProc("GlobalFree")
	procGlobalSize                    = ctrlKernel32.NewProc("GlobalSize")
	procCreateCompatibleDC            = ctrlGdi32.NewProc("CreateCompatibleDC")
	procCreateCompatibleBitmap        = ctrlGdi32.NewProc("CreateCompatibleBitmap")
	procCreateBitmap                  = ctrlGdi32.NewProc("CreateBitmap")
	procCreateDIBSection              = ctrlGdi32.NewProc("CreateDIBSection")
	procSelectObject                  = ctrlGdi32.NewProc("SelectObject")
	procDeleteDC                      = ctrlGdi32.NewProc("DeleteDC")
	procBitBlt                        = ctrlGdi32.NewProc("BitBlt")
	procSetStretchBltMode             = ctrlGdi32.NewProc("SetStretchBltMode")
	procSetBrushOrgEx                 = ctrlGdi32.NewProc("SetBrushOrgEx")
	procSaveDC                        = ctrlGdi32.NewProc("SaveDC")
	procRestoreDC                     = ctrlGdi32.NewProc("RestoreDC")
	procIntersectClipRect             = ctrlGdi32.NewProc("IntersectClipRect")
	procCreateSolidBrush              = ctrlGdi32.NewProc("CreateSolidBrush")
	procDeleteObject                  = ctrlGdi32.NewProc("DeleteObject")
	procPatBlt                        = ctrlGdi32.NewProc("PatBlt")
	procSetBkMode                     = ctrlGdi32.NewProc("SetBkMode")
	procSetBkColor                    = ctrlGdi32.NewProc("SetBkColor")
	procSetTextColor                  = ctrlGdi32.NewProc("SetTextColor")
	procTextOutW                      = ctrlGdi32.NewProc("TextOutW")
	procStretchDIBits                 = ctrlGdi32.NewProc("StretchDIBits")
	procGetDIBits                     = ctrlGdi32.NewProc("GetDIBits")
	procGetObjectW                    = ctrlGdi32.NewProc("GetObjectW")
	procImmAssociateContextEx         = ctrlImm32.NewProc("ImmAssociateContextEx")
	procDwmSetWindowAttribute         = ctrlDwmapi.NewProc("DwmSetWindowAttribute")

	controllerPNGClipboardOnce   sync.Once
	controllerPNGClipboardFormat uintptr
	controllerClipboardMutex     sync.Mutex
)

const (
	maxClipboardRetries   = 5
	clipboardRetryDelayMS = 50
)

func controllerOpenClipboardWithRetry() error {
	controllerClipboardMutex.Lock()
	for i := 0; i < maxClipboardRetries; i++ {
		if opened, _, _ := procOpenClipboard.Call(0); opened != 0 {
			return nil
		}
		if i < maxClipboardRetries-1 {
			time.Sleep(time.Duration(clipboardRetryDelayMS) * time.Millisecond)
		}
	}
	controllerClipboardMutex.Unlock()
	return fmt.Errorf("OpenClipboard failed after %d attempts", maxClipboardRetries)
}

func controllerCloseClipboardAndUnlock() {
	procCloseClipboard.Call()
	controllerClipboardMutex.Unlock()
}

const (
	csVRedraw = 0x0001
	csHRedraw = 0x0002
	csDblClks = 0x0008

	cwUseDefault = 0x80000000

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

	swShow     = 5
	swMinimize = 6

	cfUnicodeText          = 13
	cfDIB                  = 8
	cfDIBV5                = 17
	cfHDROP                = 15
	cfBitmap               = 2
	gmemMoveable           = 0x0002
	maxClipboardImageBytes = 24 << 20

	wsOverlappedWindow = 0x00CF0000
	wsPopup            = 0x80000000
	wsVisible          = 0x10000000
	wsChild            = 0x40000000
	wsThickFrame       = 0x00040000
	wsMinimizeBox      = 0x00020000
	wsMaximizeBox      = 0x00010000
	wsBorder           = 0x00800000
	wsVScroll          = 0x00200000
	wsClipChildren     = 0x02000000
	wsClipSiblings     = 0x04000000
	wsHScroll          = 0x00100000
	esMultiline        = 0x0004
	esAutoVScroll      = 0x0040
	esAutoHScroll      = 0x0080
	esReadOnly         = 0x0800
	esWantReturn       = 0x1000
	lbsNotify          = 0x0001
	lbsHasStrings      = 0x0040

	htCaption             = 2
	wmNCHitTest           = 0x0084
	wmDestroy             = 0x0002
	wmClose               = 0x0010
	wmSize                = 0x0005
	wmSetFocus            = 0x0007
	wmPaint               = 0x000F
	wmEraseBkgnd          = 0x0014
	wmSetCursor           = 0x0020
	wmCommand             = 0x0111
	wmSizing              = 0x0214
	wmCtlColorEdit        = 0x0133
	wmCtlColorListBox     = 0x0134
	wmCtlColorBtn         = 0x0135
	wmCtlColorScrollbar   = 0x0137
	wmCtlColorStatic      = 0x0138
	wmKillFocus           = 0x0008
	wmSysCommand          = 0x0112
	wmImeSetContext       = 0x0281
	wmImeStartComposition = 0x010D
	wmImeEndComposition   = 0x010E
	wmClipboardUpdate     = 0x031D
	wmImeComposition      = 0x010F
	wmMouseMove           = 0x0200
	wmLButtonDown         = 0x0201
	wmLButtonDblClk       = 0x0203
	wmLButtonUp           = 0x0202
	wmRButtonDown         = 0x0204
	wmRButtonUp           = 0x0205
	wmRButtonDblClk       = 0x0206
	wmMButtonDown         = 0x0207
	wmMButtonUp           = 0x0208
	wmMouseWheel          = 0x020A
	wmDropFiles           = 0x0233
	wmCaptureChanged      = 0x0215
	wmKeyDown             = 0x0100
	wmChar                = 0x0102
	emSetSel              = 0x00B1
	wmKeyUp               = 0x0101
	wmSysKeyDown          = 0x0104
	wmSysKeyUp            = 0x0105
	vkReturn              = 0x0D
	vkUp                  = 0x26
	vkDown                = 0x28
	vkF6                  = 0x75
	vkF7                  = 0x76
	vkF8                  = 0x77
	vkF9                  = 0x78
	vkF10                 = 0x79
	lbAddString           = 0x0180
	lbResetContent        = 0x0184
	lbGetCurSel           = 0x0188
	enChange              = 0x0300
	lbnDblClk             = 2
	sizeMinimized         = 1
	wmszLeft              = 1
	wmszRight             = 2
	wmszTop               = 3
	wmszTopLeft           = 4
	wmszTopRight          = 5
	wmszBottom            = 6
	wmszBottomLeft        = 7
	wmszBottomRight       = 8

	whKeyboardLL  = 13
	llkhfExtended = 0x01
	htClient      = 1

	scMaximize = 0xF030
	scRestore  = 0xF120

	biRGB                                      = 0
	biBitfields                                = 3
	dibRGBColors                               = 0
	srccopy                                    = 0x00CC0020
	blackness                                  = 0x00000042
	colorOnColor                               = 3
	halftone                                   = 4
	transparent                                = 1
	opaque                                     = 2
	diNormal                                   = 0x0003
	blendToleranceMS                           = 20
	controllerBGColor                          = 0x00151F2F
	quicNextProto                              = "remote-desktop-quic-v1"
	quicDatagramMagic                          = 0x51
	quicHeaderSize                             = 13
	gwlStyle                             int32 = -16
	swpNoZOrder                                = 0x0004
	swpFrameChanged                            = 0x0020
	swpShowWindow                              = 0x0040
	monitorDefaultToNearest                    = 0x00000002
	dwmwaUseImmersiveDarkMode                  = 20
	dwmwaUseImmersiveDarkModeLegacy            = 19
	topRevealY                                 = 2
	topOverlayH                                = 40
	topOverlayPad                              = 12
	topOverlayButtonW                          = 108
	topOverlayCloseW                           = 72
	dpiAwarenessContextPerMonitorAwareV2       = ^uintptr(3)
	remoteFilePathID                           = 4101
	remoteFileGoID                             = 4102
	remoteFileListID                           = 4103
	remoteFileStatusID                         = 4104
	remoteFileSaveToID                         = 4105
	remoteFileSaveDirID                        = 4106
	remoteFileCommandID                        = 4107
	remoteFileSearchID                         = 4108
	remoteCommandDirID                         = 4201
	remoteCommandRunID                         = 4202
	remoteCommandTextID                        = 4203
	remoteCommandTimeoutID                     = 4204
	remoteCommandOutputID                      = 4205
	remoteCommandStatusID                      = 4206
	remoteCommandClipboardID                   = 4207
	remoteCommandInputID                       = 4208
	defaultRemoteCommandTimeoutSec             = 30
	maxWinPath                                 = 260
	bifReturnOnlyFSDirs                        = 0x0001
	bifNewDialogStyle                          = 0x0040
	bsAutoCheckbox                             = 0x00000003
	bmGetCheck                                 = 0x00F0
	bmSetCheck                                 = 0x00F1
	bstChecked                                 = 0x0001
)

type point struct {
	X int32
	Y int32
}

type dropFiles struct {
	PFiles uint32
	Pt     point
	FNC    int32
	FWide  int32
}

type rect struct {
	Left   int32
	Top    int32
	Right  int32
	Bottom int32
}

type monitorInfo struct {
	Size    uint32
	Monitor rect
	Work    rect
	Flags   uint32
}

type msg struct {
	HWnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
}

type paintStruct struct {
	Hdc         uintptr
	Erase       int32
	RcPaint     rect
	Restore     int32
	IncUpdate   int32
	RgbReserved [32]byte
}

type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   *uint16
	ClassName  *uint16
	IconSm     uintptr
}

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

type iconInfo struct {
	FIcon    int32
	XHotspot uint32
	YHotspot uint32
	HbmMask  uintptr
	HbmColor uintptr
}

type bitmapInfo struct {
	Header bitmapInfoHeader
	Colors [1]rgbQuad
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

type kbdLLHookStruct struct {
	VkCode      uint32
	ScanCode    uint32
	Flags       uint32
	Time        uint32
	DwExtraInfo uintptr
}

type controllerInput struct {
	DeviceID      string          `json:"deviceId"`
	Type          string          `json:"type"`
	X             int             `json:"x,omitempty"`
	Y             int             `json:"y,omitempty"`
	Button        string          `json:"button,omitempty"`
	Delta         int             `json:"delta,omitempty"`
	VK            int             `json:"vk,omitempty"`
	Extended      bool            `json:"extended,omitempty"`
	Repeat        bool            `json:"repeat,omitempty"`
	Text          string          `json:"text,omitempty"`
	Clipboard     *clipboardState `json:"clipboard,omitempty"`
	ClipboardSync *bool           `json:"clipboardSync,omitempty"`
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

type remoteFileDialog struct {
	app           *controllerApp
	mode          string
	hwnd          uintptr
	pathEdit      uintptr
	goButton      uintptr
	saveButton    uintptr
	commandButton uintptr
	saveDirLabel  uintptr
	searchLabel   uintptr
	searchEdit    uintptr
	listBox       uintptr
	statusLabel   uintptr
	darkBrush     uintptr
	path          string
	parent        string
	downloadDir   string
	statusText    string
	allEntries    []remoteFileEntry
	entries       []remoteFileEntry
	transferIDs   []string
	mu            sync.Mutex
}

type remoteCommandDialog struct {
	app            *controllerApp
	hwnd           uintptr
	dirEdit        uintptr
	commandEdit    uintptr
	timeoutEdit    uintptr
	waitLabel      uintptr
	runButton      uintptr
	clipboardCheck uintptr
	inputCheck     uintptr
	outputEdit     uintptr
	statusLabel    uintptr
	darkBrush      uintptr
	statusText     string
	waiting        bool
	enterHeld      bool
	upHeld         bool
	downHeld       bool
	history        []string
	historyPos     int
	lastOpID       string
	mu             sync.Mutex
}

type browseInfo struct {
	HwndOwner      uintptr
	PidlRoot       uintptr
	PszDisplayName *uint16
	LpszTitle      *uint16
	UlFlags        uint32
	Lpfn           uintptr
	LParam         uintptr
	IImage         int32
}

type transferListResponse struct {
	Transfers []transferSnapshot `json:"transfers"`
}

type controllerTransferState struct {
	ID                    string
	Name                  string
	Direction             string
	Size                  int64
	UploadedBytes         int64
	DeliveredBytes        int64
	Status                string
	Error                 string
	StartedAt             time.Time
	UpdatedAt             time.Time
	LastBytes             int64
	LastSampleAt          time.Time
	RateBPS               float64
	UploadedLastBytes     int64
	UploadedLastSampleAt  time.Time
	UploadedRateBPS       float64
	DeliveredLastBytes    int64
	DeliveredLastSampleAt time.Time
	DeliveredRateBPS      float64
	Cancel                context.CancelFunc
}

type transferProgressView struct {
	Line            string
	Status          string
	ServerLabel     string
	EndpointLabel   string
	ServerPercent   int
	EndpointPercent int
	ServerRateBPS   float64
	EndpointRateBPS float64
}

type tilePatch struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	W      int `json:"w"`
	H      int `json:"h"`
	AtlasX int `json:"atlasX"`
	AtlasY int `json:"atlasY"`
}

type controllerState struct {
	DeviceID         string      `json:"deviceId"`
	Name             string      `json:"name"`
	Alias            string      `json:"alias,omitempty"`
	OriginX          int         `json:"originX"`
	OriginY          int         `json:"originY"`
	CursorX          int         `json:"cursorX"`
	CursorY          int         `json:"cursorY"`
	CursorVisible    bool        `json:"cursorVisible"`
	CursorKind       string      `json:"cursorKind"`
	CursorImage      []byte      `json:"cursorImage,omitempty"`
	CursorImageClear bool        `json:"cursorImageClear,omitempty"`
	CursorImageW     int         `json:"cursorImageW,omitempty"`
	CursorImageH     int         `json:"cursorImageH,omitempty"`
	CursorHotX       int         `json:"cursorHotX,omitempty"`
	CursorHotY       int         `json:"cursorHotY,omitempty"`
	CursorVersion    int64       `json:"cursorVersion,omitempty"`
	Width            int         `json:"width"`
	Height           int         `json:"height"`
	FrameID          int64       `json:"frameId"`
	UpdateKind       string      `json:"updateKind"`
	HasPatch         bool        `json:"hasPatch"`
	PatchFrameID     int64       `json:"patchFrameId"`
	PatchBaseFrameID int64       `json:"patchBaseFrameId"`
	PatchX           int         `json:"patchX"`
	PatchY           int         `json:"patchY"`
	PatchW           int         `json:"patchW"`
	PatchH           int         `json:"patchH"`
	HasTiles         bool        `json:"hasTiles"`
	TilesFrameID     int64       `json:"tilesFrameId"`
	TilesBaseFrameID int64       `json:"tilesBaseFrameId"`
	Tiles            []tilePatch `json:"tiles"`
	Online           bool        `json:"online"`
	HasFrame         bool        `json:"hasFrame"`
}

type wsFrameEnvelope struct {
	Type        string      `json:"type"`
	DeviceID    string      `json:"deviceId"`
	Name        string      `json:"name"`
	Alias       string      `json:"alias,omitempty"`
	FrameID     int64       `json:"frameId"`
	UpdateKind  string      `json:"updateKind"`
	Status      string      `json:"status,omitempty"`
	Message     string      `json:"message,omitempty"`
	BaseFrameID int64       `json:"baseFrameId"`
	Width       int         `json:"width"`
	Height      int         `json:"height"`
	OriginX     int         `json:"originX"`
	OriginY     int         `json:"originY"`
	CursorX     int         `json:"cursorX"`
	CursorY     int         `json:"cursorY"`
	CursorOn    bool        `json:"cursorVisible"`
	CursorKind  string      `json:"cursorKind"`
	CursorImage []byte      `json:"cursorImage,omitempty"`
	CursorClear bool        `json:"cursorImageClear,omitempty"`
	CursorImgW  int         `json:"cursorImageW,omitempty"`
	CursorImgH  int         `json:"cursorImageH,omitempty"`
	CursorHotX  int         `json:"cursorHotX,omitempty"`
	CursorHotY  int         `json:"cursorHotY,omitempty"`
	CursorVer   int64       `json:"cursorVersion,omitempty"`
	Codec       string      `json:"codec,omitempty"`
	Quality     int         `json:"quality,omitempty"`
	PatchX      int         `json:"patchX"`
	PatchY      int         `json:"patchY"`
	PatchW      int         `json:"patchW"`
	PatchH      int         `json:"patchH"`
	Tiles       []tilePatch `json:"tiles"`
	PayloadSize int         `json:"payloadSize"`
}

type wsClientMessage struct {
	Type       string           `json:"type"`
	FrameID    int64            `json:"frameId,omitempty"`
	DeviceID   string           `json:"deviceId,omitempty"`
	Key        string           `json:"key,omitempty"`
	Role       string           `json:"role,omitempty"`
	Codec      string           `json:"codec,omitempty"`
	Mode       string           `json:"mode,omitempty"`
	FPS        int              `json:"fps,omitempty"`
	Quality    int              `json:"quality,omitempty"`
	FullMS     int              `json:"fullMs,omitempty"`
	TargetKBps int              `json:"targetKBps,omitempty"`
	Paused     *bool            `json:"paused,omitempty"`
	Input      *controllerInput `json:"input,omitempty"`
}

type queuedFramePacket struct {
	data        []byte
	incremental bool
}

type devicesResponse struct {
	Devices []struct {
		DeviceID string `json:"deviceId"`
		Name     string `json:"name"`
		Alias    string `json:"alias,omitempty"`
		Online   bool   `json:"online"`
		Width    int    `json:"width"`
		Height   int    `json:"height"`
	} `json:"devices"`
}

type deviceAliasRequest struct {
	DeviceID string `json:"deviceId"`
	Alias    string `json:"alias"`
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

type mfHelperSession struct {
	path   string
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	stderr *bytes.Buffer
	mu     sync.Mutex
}

type controllerApp struct {
	serverURL         string
	deviceID          string
	deviceName        string
	deviceAlias       string
	adminKey          string
	transport         string
	displayMode       string
	stretchMode       string
	streamModePref    string
	streamFPSPref     int
	streamQualityPref int
	streamFullMSPref  int
	targetKBpsPref    int
	http              *http.Client
	hwnd              uintptr
	arrowCursor       uintptr
	windowW           int
	windowH           int

	mu                    sync.RWMutex
	image                 *image.RGBA
	imageBGRA             []byte
	scaledBGRA            []byte
	scaledW               int
	scaledH               int
	scaledFrameID         int64
	scaledMode            string
	displayFrameID        int64
	remoteW               int
	remoteH               int
	aspectFitDone         bool
	desktopStatus         string
	desktopMessage        string
	originX               int
	originY               int
	remoteCursorX         int
	remoteCursorY         int
	remoteCursorOn        bool
	remoteCursorKind      string
	remoteCursorImage     uintptr
	remoteCursorKey       string
	remoteFileDownloadDir string
	remoteCommandTimeout  int
	title                 string
	lastMoveAt            time.Time
	lastInputAt           time.Time
	focused               bool
	keyboardCapture       bool
	keyboardHook          uintptr
	keysDown              map[int]bool
	streamConnected       bool
	streamLabel           string
	viewerPaused          bool
	viewerPauseDirty      bool
	immersive             bool
	overlayVisible        bool
	statsVisible          bool
	statsToggleHeld       bool
	statsToggleTime       time.Time
	statsToggleID         int
	statsToggleFired      bool
	transferCancelHeld    bool
	mouseLeftDown         bool
	dragLastRemoteX       int
	dragLastRemoteY       int
	savedRect             rect
	savedStyle            uintptr
	wsWriteMu             sync.Mutex
	streamStateNotify     chan struct{}
	wsInputMu             sync.Mutex
	wsInputNotify         chan struct{}
	wsInputQueue          []controllerInput
	wsMovePending         *controllerInput
	moveInterval          time.Duration
	pingInterval          time.Duration
	clipboardTick         time.Duration
	transferTick          time.Duration
	lastClipboard         string
	clipboardVersion      int64
	clipboardDeviceID     string
	clipboardSeq          uint32
	clipboardSync         bool
	clipboardSyncKnown    bool
	clipboardChanged      chan struct{}
	transfers             map[string]*controllerTransferState
	fileDialogs           map[uintptr]*remoteFileDialog
	commandDialogs        map[uintptr]*remoteCommandDialog
	rxLogAt               time.Time
	rxLogBytes            int64
	rxLogFrames           int
	rxLogLast             string
	statsUIAt             time.Time
	lastFrameCodec        string
	lastFrameKind         string
	lastFrameBytes        int
	lastFrameQuality      int
	rxStatsAt             time.Time
	rxStatsFrames         int
	rxFPS                 float64
	rxBPS                 float64
	decodeStatsAt         time.Time
	decodeStatsFrames     int
	decodeFPS             float64
	paintStatsAt          time.Time
	paintStatsFrames      int
	paintFPS              float64
	droppedFrames         int64
	mfHelperPath          string
	mfSession             *mfHelperSession
	paintBuffer           paintBackBuffer
}

type paintBackBuffer struct {
	width  int
	height int
	memDC  uintptr
	bitmap uintptr
	oldObj uintptr
	bits   uintptr
}

var globalController *controllerApp
var controllerKeyboardHookProc = syscall.NewCallback(controllerLowLevelKeyboardProc)
var remoteFileDialogClassOnce sync.Once
var remoteFileDialogClassErr error
var remoteCommandDialogClassOnce sync.Once
var remoteCommandDialogClassErr error

var controllerStandardCursors = []struct {
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

func normalizeStretchMode(mode string) string {
	mode = strings.TrimSpace(strings.ToLower(mode))
	switch mode {
	case "halftone", "smooth", "sharp", "pixel", "crisp", "ultra":
		return mode
	case "xsharp", "x-sharp", "extra-sharp":
		return "ultra"
	case "fastsharp", "fast-sharp":
		return "crisp"
	case "nearest":
		return "pixel"
	case "color":
		return "color"
	default:
		return "color"
	}
}

func shouldPreScaleStretch(mode string) bool {
	switch normalizeStretchMode(mode) {
	case "sharp", "ultra", "smooth", "pixel":
		return true
	default:
		return false
	}
}

func normalizeOptionalStreamMode(mode string) string {
	mode = strings.TrimSpace(strings.ToLower(mode))
	switch mode {
	case "", "auto":
		return ""
	case "video", "delta":
		return mode
	default:
		return ""
	}
}

const defaultControllerServerURL = "http://127.0.0.1:19998"

func installControllerFlagUsage() {
	flag.Usage = func() {
		out := flag.CommandLine.Output()
		fmt.Fprintf(out, "用法: %s [参数]\n\n", os.Args[0])
		fmt.Fprintln(out, "常用示例:")
		fmt.Fprintf(out, "  %s -s http://服务器:19997 -k 管理Key -l\n", os.Args[0])
		fmt.Fprintf(out, "  %s -s http://服务器:19997 -k 管理Key -d 设备ID\n\n", os.Args[0])
		fmt.Fprintf(out, "  %s -s http://服务器:19997 -k 管理Key -d 设备ID -id 设备别名\n\n", os.Args[0])
		fmt.Fprintln(out, "参数:")
		fmt.Fprintf(out, "  -server, -s string              服务端地址，默认 %q\n", defaultControllerServerURL)
		fmt.Fprintln(out, "  -device, -d string              远程设备 ID；不填时自动选择第一个在线设备")
		fmt.Fprintln(out, "  -id, -alias string              设置设备别名后退出；配合 -device 使用，只有一台在线设备时可省略 -device")
		fmt.Fprintln(out, "  -key, -k string                 管理 Key")
		fmt.Fprintln(out, "  -list-devices, -l               列出在线/离线设备后退出")
		fmt.Fprintln(out, "  -transport, -t string           传输方式: auto, quic, ws，默认 auto")
		fmt.Fprintln(out, "  -display, -D string             显示模式: fit, fill, native，默认 fit")
		fmt.Fprintln(out, "  -stretch, -r string             缩放/清晰度模式: color, crisp, sharp, ultra, smooth, pixel, halftone，默认 ultra")
		fmt.Fprintln(out, "  -stream-mode, -m string         远程画面模式: auto, video, delta，默认 auto")
		fmt.Fprintln(out, "  -stream-fps, -f int             期望远程帧率；0 表示使用服务端默认")
		fmt.Fprintln(out, "  -stream-quality, -q int         期望 JPEG 质量；0 表示使用服务端默认")
		fmt.Fprintln(out, "  -stream-full-ms, -F int         期望全帧刷新间隔，毫秒；0 表示使用服务端默认")
		fmt.Fprintln(out, "  -stream-target-kbps, -b int     期望远程目标带宽，KB/s；0 表示使用服务端默认")
		fmt.Fprintln(out, "  -stats, -g                      显示左下角状态 HUD，默认 true；关闭用 -stats=false 或 -g=false")
		fmt.Fprintln(out, "  -width, -w int                  初始窗口宽度，默认 1600")
		fmt.Fprintln(out, "  -height, -H int                 初始窗口高度，默认 900")
		fmt.Fprintln(out, "  -move-ms, -v int                本地鼠标移动合并间隔，毫秒，默认 6")
		fmt.Fprintln(out, "  -ping-ms, -p int                控制端心跳间隔，毫秒，默认 900")
		fmt.Fprintln(out, "  -clipboard-ms, -c int           剪贴板同步间隔，毫秒，默认 1200")
	}
}

func main() {
	runtime.LockOSThread()
	enableDPIAwareness()

	installControllerFlagUsage()
	var serverURL, deviceID, deviceAlias, adminKey, transport, displayMode, stretchMode, streamMode string
	var listDevices, statsVisible bool
	var streamFPS, streamQuality, streamFullMS, targetKBps, windowW, windowH, moveMS, pingMS, clipboardMS int
	flag.StringVar(&serverURL, "server", defaultControllerServerURL, "服务端地址")
	flag.StringVar(&serverURL, "s", defaultControllerServerURL, "服务端地址简写")
	flag.StringVar(&deviceID, "device", "", "远程设备 ID")
	flag.StringVar(&deviceID, "d", "", "远程设备 ID 简写")
	flag.StringVar(&deviceAlias, "id", "", "设备别名")
	flag.StringVar(&deviceAlias, "alias", "", "设备别名")
	flag.StringVar(&adminKey, "key", "", "管理 Key")
	flag.StringVar(&adminKey, "k", "", "管理 Key 简写")
	flag.BoolVar(&listDevices, "list-devices", false, "列出设备后退出")
	flag.BoolVar(&listDevices, "l", false, "列出设备后退出简写")
	flag.StringVar(&transport, "transport", "auto", "传输方式: auto, quic, ws")
	flag.StringVar(&transport, "t", "auto", "传输方式简写")
	flag.StringVar(&displayMode, "display", "fit", "显示模式: fit, fill, native")
	flag.StringVar(&displayMode, "D", "fit", "显示模式简写")
	flag.StringVar(&stretchMode, "stretch", "ultra", "缩放/清晰度模式")
	flag.StringVar(&stretchMode, "r", "ultra", "缩放/清晰度模式简写")
	flag.StringVar(&streamMode, "stream-mode", "auto", "远程画面模式: auto, video, delta")
	flag.StringVar(&streamMode, "m", "auto", "远程画面模式简写")
	flag.IntVar(&streamFPS, "stream-fps", 0, "期望远程帧率，0=服务端默认")
	flag.IntVar(&streamFPS, "f", 0, "期望远程帧率简写")
	flag.IntVar(&streamQuality, "stream-quality", 0, "期望 JPEG 质量，0=服务端默认")
	flag.IntVar(&streamQuality, "q", 0, "期望 JPEG 质量简写")
	flag.IntVar(&streamFullMS, "stream-full-ms", 0, "期望全帧刷新间隔，毫秒，0=服务端默认")
	flag.IntVar(&streamFullMS, "F", 0, "期望全帧刷新间隔简写")
	flag.IntVar(&targetKBps, "stream-target-kbps", 0, "期望远程目标带宽，KB/s，0=服务端默认")
	flag.IntVar(&targetKBps, "b", 0, "期望远程目标带宽简写")
	flag.BoolVar(&statsVisible, "stats", true, "显示左下角状态 HUD")
	flag.BoolVar(&statsVisible, "g", true, "显示左下角状态 HUD 简写")
	flag.IntVar(&windowW, "width", 1600, "初始窗口宽度")
	flag.IntVar(&windowW, "w", 1600, "初始窗口宽度简写")
	flag.IntVar(&windowH, "height", 900, "初始窗口高度")
	flag.IntVar(&windowH, "H", 900, "初始窗口高度简写")
	flag.IntVar(&moveMS, "move-ms", 6, "本地鼠标移动合并间隔，毫秒")
	flag.IntVar(&moveMS, "v", 6, "本地鼠标移动合并间隔简写")
	flag.IntVar(&pingMS, "ping-ms", 900, "控制端心跳间隔，毫秒")
	flag.IntVar(&pingMS, "p", 900, "控制端心跳间隔简写")
	flag.IntVar(&clipboardMS, "clipboard-ms", 1200, "剪贴板同步间隔，毫秒")
	flag.IntVar(&clipboardMS, "c", 1200, "剪贴板同步间隔简写")
	flag.Parse()

	app := &controllerApp{
		serverURL:            strings.TrimRight(serverURL, "/"),
		deviceID:             strings.TrimSpace(deviceID),
		deviceAlias:          strings.TrimSpace(deviceAlias),
		adminKey:             strings.TrimSpace(adminKey),
		transport:            strings.TrimSpace(strings.ToLower(transport)),
		displayMode:          strings.TrimSpace(strings.ToLower(displayMode)),
		stretchMode:          strings.TrimSpace(strings.ToLower(stretchMode)),
		streamModePref:       normalizeOptionalStreamMode(streamMode),
		streamFPSPref:        clampInt(streamFPS, 0, 60),
		streamQualityPref:    clampInt(streamQuality, 0, 100),
		streamFullMSPref:     clampInt(streamFullMS, 0, 20000),
		targetKBpsPref:       clampInt(targetKBps, 0, 4096),
		http:                 newControllerHTTPClient(90 * time.Second),
		windowW:              clampInt(windowW, 640, 3840),
		windowH:              clampInt(windowH, 480, 2160),
		title:                "Mydesk",
		keyboardCapture:      true,
		keysDown:             make(map[int]bool),
		statsVisible:         statsVisible,
		viewerPauseDirty:     true,
		streamStateNotify:    make(chan struct{}, 1),
		wsInputNotify:        make(chan struct{}, 1),
		moveInterval:         time.Duration(clampInt(moveMS, 0, 1000)) * time.Millisecond,
		pingInterval:         time.Duration(clampInt(pingMS, 500, 15000)) * time.Millisecond,
		clipboardTick:        time.Duration(clampInt(clipboardMS, 200, 10000)) * time.Millisecond,
		clipboardSync:        true,
		clipboardSyncKnown:   true,
		clipboardChanged:     make(chan struct{}, 1),
		transferTick:         700 * time.Millisecond,
		remoteCommandTimeout: defaultRemoteCommandTimeoutSec,
		transfers:            make(map[string]*controllerTransferState),
		fileDialogs:          make(map[uintptr]*remoteFileDialog),
		commandDialogs:       make(map[uintptr]*remoteCommandDialog),
	}
	if app.displayMode != "fill" && app.displayMode != "native" {
		app.displayMode = "fit"
	}
	app.stretchMode = normalizeStretchMode(app.stretchMode)
	logStreamMode := app.streamModePref
	if logStreamMode == "" {
		logStreamMode = "auto"
	}
	globalController = app
	// log.Printf("[controller][start] server=%s device=%s transport=%s display=%s stretch=%s codec=%s streamMode=%s streamFPS=%d streamQuality=%d streamFullMS=%d targetKBps=%d", app.serverURL, app.deviceID, app.transport, app.displayMode, app.stretchMode, app.preferredCodec(), logStreamMode, app.streamFPSPref, app.streamQualityPref, app.streamFullMSPref, app.targetKBpsPref)
	shouldExit, err := app.prepareStartupDeviceSelection(listDevices)
	if err != nil {
		log.Fatal(err)
	}
	if app.deviceAlias != "" {
		if app.adminKey == "" {
			log.Fatal("missing admin key: use -key to set device alias")
		}
		if app.deviceID == "" {
			log.Fatal("missing device id: use -device <id> to set alias when zero or multiple devices are online")
		}
		if err := app.setDeviceAlias(app.deviceID, app.deviceAlias); err != nil {
			log.Fatal(err)
		}
		log.Printf("[controller][devices] alias set device=%s alias=%s", app.deviceID, app.deviceAlias)
		return
	}
	if shouldExit {
		return
	}
	if app.deviceID == "" && app.adminKey != "" && app.transport != "ws" {
		log.Printf("[controller][quic] admin-key device discovery still uses websocket; direct QUIC requires -device")
	}
	if err := app.createWindow(); err != nil {
		log.Fatal(err)
	}

	go app.streamLoop()
	go app.clipboardLoop()
	go app.transferLoop()

	if err := app.messageLoop(); err != nil {
		log.Fatal(err)
	}
}

func (a *controllerApp) createWindow() error {
	instance, _, _ := procGetModuleHandleW.Call(0)
	className := syscall.StringToUTF16Ptr("GoRemoteControllerWindow")
	cursor, _, _ := procLoadCursorW.Call(0, idcArrow)

	wc := wndClassEx{
		Size:      uint32(unsafe.Sizeof(wndClassEx{})),
		Style:     csHRedraw | csVRedraw | csDblClks,
		WndProc:   syscall.NewCallback(controllerWndProc),
		Instance:  instance,
		Cursor:    cursor,
		ClassName: className,
	}

	atom, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	if atom == 0 {
		return fmt.Errorf("RegisterClassExW failed: %v", err)
	}

	title := syscall.StringToUTF16Ptr(a.title)
	style := uintptr(wsOverlappedWindow | wsVisible)
	clientW := maxInt(1, a.windowW)
	clientH := maxInt(1, a.windowH)
	if remoteW, remoteH, ok := a.aspectRemoteSize(); ok {
		clientW, clientH = aspectClientSizeWithin(clientW, clientH, remoteW, remoteH)
	}
	outerW, outerH := adjustedWindowSizeForClient(clientW, clientH, style, 0)
	hwnd, _, err := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(title)),
		style,
		cwUseDefault,
		cwUseDefault,
		uintptr(outerW),
		uintptr(outerH),
		0,
		0,
		instance,
		0,
	)
	if hwnd == 0 {
		return fmt.Errorf("CreateWindowExW failed: %v", err)
	}

	a.hwnd = hwnd
	a.arrowCursor = cursor
	applyDarkTitleBar(hwnd)
	disableIME(hwnd)
	procShowWindow.Call(hwnd, swShow)
	procUpdateWindow.Call(hwnd)
	procAddClipboardFormatListener.Call(hwnd)
	if err := a.installKeyboardHook(); err != nil {
		return err
	}
	return nil
}

func adjustedWindowSizeForClient(clientW, clientH int, style, exStyle uintptr) (int, int) {
	clientW = maxInt(1, clientW)
	clientH = maxInt(1, clientH)
	r := rect{Right: int32(clientW), Bottom: int32(clientH)}
	if ok, _, _ := procAdjustWindowRectEx.Call(uintptr(unsafe.Pointer(&r)), style, 0, exStyle); ok != 0 {
		outerW := rectWidth(r)
		outerH := rectHeight(r)
		if outerW > 0 && outerH > 0 {
			return outerW, outerH
		}
	}
	return clientW, clientH
}

func aspectClientSizeWithin(clientW, clientH, remoteW, remoteH int) (int, int) {
	clientW = maxInt(1, clientW)
	clientH = maxInt(1, clientH)
	if remoteW <= 0 || remoteH <= 0 {
		return clientW, clientH
	}
	if int64(clientW)*int64(remoteH) > int64(clientH)*int64(remoteW) {
		clientW = aspectWidthForHeight(clientH, remoteW, remoteH)
	} else {
		clientH = aspectHeightForWidth(clientW, remoteW, remoteH)
	}
	return maxInt(1, clientW), maxInt(1, clientH)
}

// fitRemoteInBounds 将远程分辨率适配到可用区域内，不放大只缩小：
// 远程分辨率能放下就用原始大小（1:1），放不下才等比缩小。
func fitRemoteInBounds(availW, availH, remoteW, remoteH int) (int, int) {
	availW = maxInt(1, availW)
	availH = maxInt(1, availH)
	if remoteW <= 0 || remoteH <= 0 {
		return availW, availH
	}
	targetW := remoteW
	targetH := remoteH
	if targetW > availW {
		targetW = availW
		targetH = int((int64(targetW)*int64(remoteH) + int64(remoteW)/2) / int64(remoteW))
	}
	if targetH > availH {
		targetH = availH
		targetW = int((int64(targetH)*int64(remoteW) + int64(remoteH)/2) / int64(remoteH))
	}
	return maxInt(1, targetW), maxInt(1, targetH)
}

func aspectWidthForHeight(height, remoteW, remoteH int) int {
	if height <= 0 || remoteW <= 0 || remoteH <= 0 {
		return maxInt(1, height)
	}
	return maxInt(1, int((int64(height)*int64(remoteW)+int64(remoteH)/2)/int64(remoteH)))
}

func aspectHeightForWidth(width, remoteW, remoteH int) int {
	if width <= 0 || remoteW <= 0 || remoteH <= 0 {
		return maxInt(1, width)
	}
	return maxInt(1, int((int64(width)*int64(remoteH)+int64(remoteW)/2)/int64(remoteW)))
}

func (a *controllerApp) aspectRemoteSize() (int, int, bool) {
	if a == nil {
		return 0, 0, false
	}
	a.mu.RLock()
	remoteW := a.remoteW
	remoteH := a.remoteH
	immersive := a.immersive
	a.mu.RUnlock()
	return remoteW, remoteH, !immersive && remoteW > 0 && remoteH > 0
}

func (a *controllerApp) windowFrameDelta(hwnd uintptr) (int, int) {
	if hwnd == 0 {
		return 0, 0
	}
	var window, client rect
	if ok, _, _ := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&window))); ok == 0 {
		return 0, 0
	}
	if ok, _, _ := procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&client))); ok == 0 {
		return 0, 0
	}
	deltaW := rectWidth(window) - rectWidth(client)
	deltaH := rectHeight(window) - rectHeight(client)
	if deltaW < 0 {
		deltaW = 0
	}
	if deltaH < 0 {
		deltaH = 0
	}
	return deltaW, deltaH
}

func (a *controllerApp) handleSizing(hwnd uintptr, edge uintptr, lParam uintptr) bool {
	if hwnd == 0 || lParam == 0 {
		return false
	}
	remoteW, remoteH, ok := a.aspectRemoteSize()
	if !ok {
		return false
	}
	r := (*rect)(unsafe.Pointer(lParam))
	frameW, frameH := a.windowFrameDelta(hwnd)
	outerW := maxInt(1, int(r.Right-r.Left))
	outerH := maxInt(1, int(r.Bottom-r.Top))
	clientW := maxInt(1, outerW-frameW)
	clientH := maxInt(1, outerH-frameH)

	switch edge {
	case wmszTop, wmszBottom:
		clientW = aspectWidthForHeight(clientH, remoteW, remoteH)
	case wmszLeft, wmszRight:
		clientH = aspectHeightForWidth(clientW, remoteW, remoteH)
	default:
		fitW, fitH := aspectClientSizeWithin(clientW, clientH, remoteW, remoteH)
		clientW, clientH = fitW, fitH
	}
	clientW, clientH = enforceAspectMinimum(clientW, clientH, remoteW, remoteH)
	outerW = clientW + frameW
	outerH = clientH + frameH

	left := int(r.Left)
	right := int(r.Right)
	top := int(r.Top)
	bottom := int(r.Bottom)
	horizontalMoving := edge == wmszLeft || edge == wmszRight || edge == wmszTopLeft || edge == wmszTopRight || edge == wmszBottomLeft || edge == wmszBottomRight
	verticalMoving := edge == wmszTop || edge == wmszBottom || edge == wmszTopLeft || edge == wmszTopRight || edge == wmszBottomLeft || edge == wmszBottomRight

	if edge == wmszLeft || edge == wmszTopLeft || edge == wmszBottomLeft {
		left = right - outerW
	} else if edge == wmszRight || edge == wmszTopRight || edge == wmszBottomRight {
		right = left + outerW
	} else if !horizontalMoving {
		center := (left + right) / 2
		left = center - outerW/2
		right = left + outerW
	}

	if edge == wmszTop || edge == wmszTopLeft || edge == wmszTopRight {
		top = bottom - outerH
	} else if edge == wmszBottom || edge == wmszBottomLeft || edge == wmszBottomRight {
		bottom = top + outerH
	} else if !verticalMoving {
		center := (top + bottom) / 2
		top = center - outerH/2
		bottom = top + outerH
	}

	r.Left = int32(left)
	r.Right = int32(right)
	r.Top = int32(top)
	r.Bottom = int32(bottom)
	return true
}

func enforceAspectMinimum(clientW, clientH, remoteW, remoteH int) (int, int) {
	const minClientW = 320
	const minClientH = 180
	if remoteW <= 0 || remoteH <= 0 {
		return maxInt(minClientW, clientW), maxInt(minClientH, clientH)
	}
	if clientW < minClientW {
		clientW = minClientW
		clientH = aspectHeightForWidth(clientW, remoteW, remoteH)
	}
	if clientH < minClientH {
		clientH = minClientH
		clientW = aspectWidthForHeight(clientH, remoteW, remoteH)
	}
	return clientW, clientH
}

func (a *controllerApp) updateRemoteSizeLocked(newW, newH int) {
	if newW <= 0 || newH <= 0 {
		return
	}
	if newW != a.remoteW || newH != a.remoteH {
		a.aspectFitDone = false
	}
	a.remoteW = newW
	a.remoteH = newH
}

func (a *controllerApp) fitWindowToRemoteAspectOnce() {
	if a == nil {
		return
	}
	a.mu.RLock()
	done := a.aspectFitDone
	a.mu.RUnlock()
	if done {
		return
	}
	if a.fitWindowToRemoteAspect() {
		a.mu.Lock()
		a.aspectFitDone = true
		a.mu.Unlock()
	}
}

func (a *controllerApp) fitWindowToRemoteAspect() bool {
	if a == nil || a.hwnd == 0 {
		return false
	}
	remoteW, remoteH, ok := a.aspectRemoteSize()
	if !ok {
		return false
	}
	var client, window rect
	if ok, _, _ := procGetClientRect.Call(a.hwnd, uintptr(unsafe.Pointer(&client))); ok == 0 {
		return false
	}
	if ok, _, _ := procGetWindowRect.Call(a.hwnd, uintptr(unsafe.Pointer(&window))); ok == 0 {
		return false
	}
	clientW := rectWidth(client)
	clientH := rectHeight(client)
	if clientW <= 0 || clientH <= 0 {
		return false
	}
	frameW := rectWidth(window) - clientW
	frameH := rectHeight(window) - clientH
	// 获取显示器工作区，计算可用 client 区域（工作区减去窗口边框）
	// 目标窗口大小根据远程分辨率 fit 到可用区域内，而非保持当前窗口面积
	availW := clientW
	availH := clientH
	monitor, _, _ := procMonitorFromWindow.Call(a.hwnd, monitorDefaultToNearest)
	if monitor != 0 {
		mi := monitorInfo{Size: uint32(unsafe.Sizeof(monitorInfo{}))}
		if ok2, _, _ := procGetMonitorInfoW.Call(monitor, uintptr(unsafe.Pointer(&mi))); ok2 != 0 {
			workW := int(mi.Work.Right) - int(mi.Work.Left)
			workH := int(mi.Work.Bottom) - int(mi.Work.Top)
			if workW > frameW && workH > frameH {
				availW = workW - frameW
				availH = workH - frameH
			}
		}
	}
	targetClientW, targetClientH := fitRemoteInBounds(availW, availH, remoteW, remoteH)
	if absInt(targetClientW-clientW) <= 1 && absInt(targetClientH-clientH) <= 1 {
		return true
	}
	targetOuterW := maxInt(1, targetClientW+maxInt(0, frameW))
	targetOuterH := maxInt(1, targetClientH+maxInt(0, frameH))
	centerX := (int(window.Left) + int(window.Right)) / 2
	centerY := (int(window.Top) + int(window.Bottom)) / 2
	left := centerX - targetOuterW/2
	top := centerY - targetOuterH/2
	left, top = keepWindowOnWorkArea(a.hwnd, left, top, targetOuterW, targetOuterH)
	procSetWindowPos.Call(
		a.hwnd,
		0,
		uintptrSigned(left),
		uintptrSigned(top),
		uintptr(targetOuterW),
		uintptr(targetOuterH),
		swpNoZOrder,
	)
	return true
}

func keepWindowOnWorkArea(hwnd uintptr, left, top, width, height int) (int, int) {
	monitor, _, _ := procMonitorFromWindow.Call(hwnd, monitorDefaultToNearest)
	if monitor == 0 {
		return left, top
	}
	mi := monitorInfo{Size: uint32(unsafe.Sizeof(monitorInfo{}))}
	if ok, _, _ := procGetMonitorInfoW.Call(monitor, uintptr(unsafe.Pointer(&mi))); ok == 0 {
		return left, top
	}
	workLeft := int(mi.Work.Left)
	workTop := int(mi.Work.Top)
	workRight := int(mi.Work.Right)
	workBottom := int(mi.Work.Bottom)
	if width > workRight-workLeft {
		left = workLeft
	} else {
		if left < workLeft {
			left = workLeft
		}
		if left+width > workRight {
			left = workRight - width
		}
	}
	if height > workBottom-workTop {
		top = workTop
	} else {
		if top < workTop {
			top = workTop
		}
		if top+height > workBottom {
			top = workBottom - height
		}
	}
	return left, top
}

func (a *controllerApp) messageLoop() error {
	var m msg
	for {
		ret, _, err := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		switch int32(ret) {
		case -1:
			return fmt.Errorf("GetMessageW failed: %v", err)
		case 0:
			return nil
		default:
			if a.handleDialogSelectAll(&m) {
				continue
			}
			procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
			procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
		}
	}
}

// TranslateMessage produces character 1 for Ctrl+A. Standard EDIT controls
// need explicit select-all handling; consume the character to avoid a beep.
func (a *controllerApp) handleDialogSelectAll(m *msg) bool {
	if m.Message != wmChar || m.WParam != 1 || m.HWnd == 0 {
		return false
	}
	a.mu.RLock()
	isEdit := false
	for _, d := range a.commandDialogs {
		if d != nil && (m.HWnd == d.dirEdit || m.HWnd == d.commandEdit || m.HWnd == d.timeoutEdit || m.HWnd == d.outputEdit) {
			isEdit = true
			break
		}
	}
	if !isEdit {
		for _, d := range a.fileDialogs {
			if d != nil && (m.HWnd == d.pathEdit || m.HWnd == d.searchEdit) {
				isEdit = true
				break
			}
		}
	}
	a.mu.RUnlock()
	if isEdit {
		procSendMessageW.Call(m.HWnd, emSetSel, 0, ^uintptr(0))
	}
	return isEdit
}

func controllerWndProc(hwnd uintptr, message uint32, wParam, lParam uintptr) uintptr {
	app := globalController
	if app == nil {
		ret, _, _ := procDefWindowProcW.Call(hwnd, uintptr(message), wParam, lParam)
		return ret
	}
	if dialog := app.lookupRemoteFileDialog(hwnd); dialog != nil {
		return dialog.wndProc(message, wParam, lParam)
	}
	if dialog := app.lookupRemoteCommandDialog(hwnd); dialog != nil {
		return dialog.wndProc(message, wParam, lParam)
	}

	switch message {
	case wmNCHitTest:
		ret, _, _ := procDefWindowProcW.Call(hwnd, uintptr(message), wParam, lParam)
		return ret
	case wmPaint:
		app.paint(hwnd)
		return 0
	case wmSizing:
		if app.handleSizing(hwnd, wParam, lParam) {
			return 1
		}
		ret, _, _ := procDefWindowProcW.Call(hwnd, uintptr(message), wParam, lParam)
		return ret
	case wmSize:
		if uint32(wParam) == sizeMinimized {
			app.cancelRemoteLeftDrag(true)
		}
		app.setViewerPaused(uint32(wParam) == sizeMinimized)
		ret, _, _ := procDefWindowProcW.Call(hwnd, uintptr(message), wParam, lParam)
		return ret
	case wmEraseBkgnd:
		return 1
	case wmSetCursor:
		if app.handleSetCursor(hwnd, lowWord(lParam)) {
			return 1
		}
		ret, _, _ := procDefWindowProcW.Call(hwnd, uintptr(message), wParam, lParam)
		return ret
	case wmDestroy:
		app.uninstallKeyboardHook()
		app.paintBuffer.close()
		app.sendInputAsync(controllerInput{Type: "reset_keys"})
		procRemoveClipboardFormatListener.Call(hwnd)
		procPostQuitMessage.Call(0)
		return 0
	case wmClipboardUpdate:
		select {
		case app.clipboardChanged <- struct{}{}:
		default:
		}
		return 0
	case wmSetFocus:
		app.setFocusState(true)
		disableIME(hwnd)
		return 0
	case wmKillFocus:
		app.setFocusState(false)
		return 0
	case wmCaptureChanged:
		app.cancelRemoteLeftDrag(true)
		return 0
	case wmSysCommand:
		switch wParam & 0xFFF0 {
		case scMaximize:
			app.setImmersive(true)
			return 0
		case scRestore:
			if app.isImmersive() {
				app.setImmersive(false)
				return 0
			}
		}
		ret, _, _ := procDefWindowProcW.Call(hwnd, uintptr(message), wParam, lParam)
		return ret
	case wmImeSetContext, wmImeStartComposition, wmImeEndComposition, wmImeComposition:
		return 0
	case wmMouseMove:
		clientX, clientY := signedLowWord(lParam), highWord(lParam)
		if app.isImmersive() {
			app.updateOverlayVisibility(clientY)
			if !app.mouseLeftDown && app.overlayConsumes(clientX, clientY) {
				return 0
			}
		}
		now := time.Now()
		if now.Sub(app.lastMoveAt) >= app.moveInterval {
			if x, y, ok := app.remotePoint(hwnd, clientX, clientY); ok && app.inputEnabled() {
				app.lastMoveAt = now
				app.noteRemoteDragMove(x, y)
				app.sendInputAsync(controllerInput{Type: "move", X: x, Y: y})
			}
		}
		return 0
	case wmLButtonDown, wmLButtonDblClk, wmLButtonUp, wmRButtonDown, wmRButtonDblClk, wmRButtonUp, wmMButtonDown, wmMButtonUp:
		clientX, clientY := signedLowWord(lParam), highWord(lParam)
		if app.isImmersive() && message == wmLButtonDown && app.handleOverlayClick(hwnd, clientX, clientY) {
			return 0
		}
		if app.isImmersive() && !app.mouseLeftDown && app.overlayConsumes(clientX, clientY) {
			return 0
		}
		app.handleMouseButton(hwnd, message, clientX, clientY)
		return 0
	case wmMouseWheel:
		pt := point{X: int32(lowWord(lParam)), Y: int32(highWord(lParam))}
		procScreenToClient.Call(hwnd, uintptr(unsafe.Pointer(&pt)))
		if x, y, ok := app.remotePoint(hwnd, int(pt.X), int(pt.Y)); ok && app.inputEnabled() {
			delta := int(int16(highWord(wParam)))
			app.sendInputAsync(controllerInput{Type: "wheel", Delta: delta, X: x, Y: y})
		}
		return 0
	case wmKeyDown, wmSysKeyDown:
		if app.handleLocalHotkey(int(wParam), true) {
			return 0
		}
		if !app.inputEnabled() {
			return 0
		}
		vk := int(wParam)
		repeat := app.noteKeyEvent(vk, true, keyRepeat(lParam))
		app.sendInputAsync(controllerInput{
			Type:     "keydown",
			VK:       vk,
			Extended: keyExtended(vk, lParam),
			Repeat:   repeat,
		})
		return 0
	case wmKeyUp, wmSysKeyUp:
		if app.handleLocalHotkey(int(wParam), false) {
			return 0
		}
		if !app.inputEnabled() {
			return 0
		}
		vk := int(wParam)
		app.noteKeyEvent(vk, false, false)
		app.sendInputAsync(controllerInput{
			Type:     "keyup",
			VK:       vk,
			Extended: keyExtended(vk, lParam),
		})
		return 0
	default:
		ret, _, _ := procDefWindowProcW.Call(hwnd, uintptr(message), wParam, lParam)
		return ret
	}
}

func (a *controllerApp) lookupRemoteFileDialog(hwnd uintptr) *remoteFileDialog {
	if a == nil || hwnd == 0 {
		return nil
	}
	a.mu.RLock()
	dialog := a.fileDialogs[hwnd]
	a.mu.RUnlock()
	return dialog
}

func (a *controllerApp) lookupRemoteCommandDialog(hwnd uintptr) *remoteCommandDialog {
	if a == nil || hwnd == 0 {
		return nil
	}
	a.mu.RLock()
	dialog := a.commandDialogs[hwnd]
	a.mu.RUnlock()
	return dialog
}

func (a *controllerApp) activeRemoteCommandDialog() *remoteCommandDialog {
	if a == nil {
		return nil
	}
	foreground, _, _ := procGetForegroundWindow.Call()
	if foreground == 0 {
		return nil
	}
	return a.lookupRemoteCommandDialog(foreground)
}

func (a *controllerApp) hotkeyWindowActive() bool {
	if a == nil {
		return false
	}
	foreground, _, _ := procGetForegroundWindow.Call()
	if foreground == 0 {
		return false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if foreground == a.hwnd {
		return true
	}
	if _, ok := a.fileDialogs[foreground]; ok {
		return true
	}
	if _, ok := a.commandDialogs[foreground]; ok {
		return true
	}
	return false
}

func (a *controllerApp) savedRemoteCommandTimeoutSec() int {
	if a == nil {
		return defaultRemoteCommandTimeoutSec
	}
	a.mu.RLock()
	timeoutSec := a.remoteCommandTimeout
	a.mu.RUnlock()
	return clampInt(timeoutSec, 0, 3600)
}

func (a *controllerApp) saveRemoteCommandTimeoutSec(timeoutSec int) {
	if a == nil {
		return
	}
	timeoutSec = clampInt(timeoutSec, 0, 3600)
	a.mu.Lock()
	a.remoteCommandTimeout = timeoutSec
	a.mu.Unlock()
}

func (a *controllerApp) openRemoteFileDialog(mode string) {
	if a == nil || a.hwnd == 0 {
		return
	}
	mode = "files"
	if strings.TrimSpace(a.deviceID) == "" {
		log.Printf("[controller][files] no device selected")
		return
	}
	a.mu.RLock()
	for _, dialog := range a.fileDialogs {
		if dialog != nil && dialog.mode == mode && dialog.hwnd != 0 {
			hwnd := dialog.hwnd
			a.mu.RUnlock()
			procShowWindow.Call(hwnd, swShow)
			return
		}
	}
	a.mu.RUnlock()
	if err := registerRemoteFileDialogClass(); err != nil {
		log.Printf("[controller][files] register dialog failed: %v", err)
		return
	}

	instance, _, _ := procGetModuleHandleW.Call(0)
	className := syscall.StringToUTF16Ptr("GoRemoteFileDialogWindow")
	titleText := "Remote File Manager (F8)"
	hwnd, _, err := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr(titleText))),
		wsOverlappedWindow|wsVisible|wsClipChildren,
		cwUseDefault,
		cwUseDefault,
		760,
		560,
		a.hwnd,
		0,
		instance,
		0,
	)
	if hwnd == 0 {
		log.Printf("[controller][files] create dialog failed: %v", err)
		return
	}
	applyDarkTitleBar(hwnd)
	dialog := &remoteFileDialog{app: a, mode: mode, hwnd: hwnd, downloadDir: a.currentRemoteFileDownloadDir()}
	a.mu.Lock()
	a.fileDialogs[hwnd] = dialog
	a.mu.Unlock()
	dialog.createControls()
	procDragAcceptFiles.Call(hwnd, 1)
	dialog.refresh("")
}

func (a *controllerApp) toggleRemoteFileDialog(mode string) {
	if a == nil {
		return
	}
	mode = "files"
	a.mu.RLock()
	for _, dialog := range a.fileDialogs {
		if dialog != nil && dialog.mode == mode && dialog.hwnd != 0 {
			hwnd := dialog.hwnd
			a.mu.RUnlock()
			procDestroyWindow.Call(hwnd)
			return
		}
	}
	a.mu.RUnlock()
	a.openRemoteFileDialog(mode)
}

func (a *controllerApp) openRemoteCommandDialog() {
	if a == nil || a.hwnd == 0 {
		return
	}
	if strings.TrimSpace(a.deviceID) == "" {
		log.Printf("[controller][command] no device selected")
		return
	}
	a.mu.RLock()
	for _, dialog := range a.commandDialogs {
		if dialog != nil && dialog.hwnd != 0 {
			hwnd := dialog.hwnd
			a.mu.RUnlock()
			procShowWindow.Call(hwnd, swShow)
			return
		}
	}
	a.mu.RUnlock()
	if err := registerRemoteCommandDialogClass(); err != nil {
		log.Printf("[controller][command] register dialog failed: %v", err)
		return
	}
	instance, _, _ := procGetModuleHandleW.Call(0)
	className := syscall.StringToUTF16Ptr("GoRemoteCommandDialogWindow")
	hwnd, _, err := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr("Remote Command (F7)"))),
		wsOverlappedWindow|wsVisible|wsClipChildren,
		cwUseDefault,
		cwUseDefault,
		780,
		560,
		a.hwnd,
		0,
		instance,
		0,
	)
	if hwnd == 0 {
		log.Printf("[controller][command] create dialog failed: %v", err)
		return
	}
	applyDarkTitleBar(hwnd)
	dialog := &remoteCommandDialog{app: a, hwnd: hwnd}
	a.mu.Lock()
	a.commandDialogs[hwnd] = dialog
	a.mu.Unlock()
	dialog.createControls()
	dialog.refreshDir()
}

func (a *controllerApp) toggleRemoteCommandDialog() {
	if a == nil {
		return
	}
	a.mu.RLock()
	for _, dialog := range a.commandDialogs {
		if dialog != nil && dialog.hwnd != 0 {
			hwnd := dialog.hwnd
			a.mu.RUnlock()
			procDestroyWindow.Call(hwnd)
			return
		}
	}
	a.mu.RUnlock()
	a.openRemoteCommandDialog()
}

func registerRemoteFileDialogClass() error {
	remoteFileDialogClassOnce.Do(func() {
		instance, _, _ := procGetModuleHandleW.Call(0)
		cursor, _, _ := procLoadCursorW.Call(0, idcArrow)
		className := syscall.StringToUTF16Ptr("GoRemoteFileDialogWindow")
		wc := wndClassEx{
			Size:      uint32(unsafe.Sizeof(wndClassEx{})),
			Style:     csDblClks,
			WndProc:   syscall.NewCallback(controllerWndProc),
			Instance:  instance,
			Cursor:    cursor,
			ClassName: className,
		}
		atom, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
		if atom == 0 {
			remoteFileDialogClassErr = fmt.Errorf("RegisterClassExW failed: %v", err)
		}
	})
	return remoteFileDialogClassErr
}

func registerRemoteCommandDialogClass() error {
	remoteCommandDialogClassOnce.Do(func() {
		instance, _, _ := procGetModuleHandleW.Call(0)
		cursor, _, _ := procLoadCursorW.Call(0, idcArrow)
		className := syscall.StringToUTF16Ptr("GoRemoteCommandDialogWindow")
		wc := wndClassEx{
			Size:      uint32(unsafe.Sizeof(wndClassEx{})),
			Style:     csDblClks,
			WndProc:   syscall.NewCallback(controllerWndProc),
			Instance:  instance,
			Cursor:    cursor,
			ClassName: className,
		}
		atom, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
		if atom == 0 {
			remoteCommandDialogClassErr = fmt.Errorf("RegisterClassExW failed: %v", err)
		}
	})
	return remoteCommandDialogClassErr
}

func (d *remoteCommandDialog) createControls() {
	if d == nil || d.hwnd == 0 {
		return
	}
	d.darkBrush, _, _ = procCreateSolidBrush.Call(0x00111820)
	createChildControl(d.hwnd, "STATIC", "Directory", wsChild|wsVisible|wsClipSiblings, 16, 16, 80, 22, 0)
	d.dirEdit = createChildControl(d.hwnd, "EDIT", "", wsChild|wsVisible|wsBorder|wsClipSiblings|esAutoHScroll, 104, 14, 500, 26, remoteCommandDirID)
	d.waitLabel = createChildControl(d.hwnd, "STATIC", "Wait(s)", wsChild|wsVisible|wsClipSiblings, 616, 16, 56, 22, 0)
	d.timeoutEdit = createChildControl(d.hwnd, "EDIT", strconv.Itoa(d.savedTimeoutSec()), wsChild|wsVisible|wsBorder|wsClipSiblings|esAutoHScroll, 674, 14, 50, 26, remoteCommandTimeoutID)
	d.runButton = createChildControl(d.hwnd, "BUTTON", "Run", wsChild|wsVisible|wsClipSiblings, 16, 86, 86, 30, remoteCommandRunID)
	d.clipboardCheck = createChildControl(d.hwnd, "BUTTON", "Sync Clipboard", wsChild|wsVisible|wsClipSiblings|bsAutoCheckbox, 112, 88, 150, 26, remoteCommandClipboardID)
	d.setClipboardCheckbox(d.app.isClipboardSyncEnabled())
	d.inputCheck = createChildControl(d.hwnd, "BUTTON", "Sync Input", wsChild|wsVisible|wsClipSiblings|bsAutoCheckbox, 272, 88, 150, 26, remoteCommandInputID)
	d.setInputCheckbox(d.app.keyboardCapture)
	createChildControl(d.hwnd, "STATIC", "Command", wsChild|wsVisible|wsClipSiblings, 16, 52, 80, 22, 0)
	d.commandEdit = createChildControl(d.hwnd, "EDIT", "", wsChild|wsVisible|wsBorder|wsClipSiblings|esAutoHScroll, 104, 50, 620, 28, remoteCommandTextID)
	d.outputEdit = createChildControl(d.hwnd, "EDIT", "", wsChild|wsVisible|wsBorder|wsVScroll|wsHScroll|wsClipSiblings|esMultiline|esReadOnly|esAutoVScroll|esAutoHScroll|esWantReturn, 16, 128, 708, 340, remoteCommandOutputID)
	d.statusLabel = createChildControl(d.hwnd, "STATIC", "", wsChild|wsVisible|wsClipSiblings, 112, 90, 612, 38, remoteCommandStatusID)
	d.applyDarkThemeToControls()
	d.layout()
	d.setStatus("F7 remote command. Wait 0 = start only, do not wait for output.")
	d.setOutputText("Command output will appear here.")
	go d.refreshClipboardSync()
}

func (d *remoteCommandDialog) applyDarkThemeToControls() {
	if d == nil {
		return
	}
	if procSetWindowTheme.Find() != nil {
		return
	}
	for _, hwnd := range []uintptr{d.dirEdit, d.commandEdit, d.timeoutEdit, d.waitLabel, d.runButton, d.clipboardCheck, d.outputEdit, d.statusLabel} {
		if hwnd == 0 {
			continue
		}
		procSetWindowTheme.Call(hwnd, uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr("DarkMode_Explorer"))), 0)
	}
}

func (d *remoteCommandDialog) layout() {
	if d == nil || d.hwnd == 0 {
		return
	}
	var client rect
	procGetClientRect.Call(d.hwnd, uintptr(unsafe.Pointer(&client)))
	width := maxInt(420, int(client.Right-client.Left))
	height := maxInt(320, int(client.Bottom-client.Top))
	margin := 16
	labelW := 80
	waitLabelW := 56
	waitW := 54
	gap := 8
	lineH := 28
	right := width - margin
	dirEditW := maxInt(140, width-margin*2-labelW-waitLabelW-waitW-gap*4)
	procSetWindowPos.Call(d.dirEdit, 0, uintptr(margin+labelW+gap), uintptr(14), uintptr(dirEditW), uintptr(lineH), swpNoZOrder)
	waitLabelX := margin + labelW + gap + dirEditW + gap
	procSetWindowPos.Call(d.waitLabel, 0, uintptr(waitLabelX), uintptr(16), uintptr(waitLabelW), uintptr(22), swpNoZOrder)
	procSetWindowPos.Call(d.timeoutEdit, 0, uintptr(waitLabelX+waitLabelW+gap), uintptr(14), uintptr(waitW), uintptr(lineH), swpNoZOrder)
	cmdW := maxInt(160, right-(margin+labelW+gap))
	procSetWindowPos.Call(d.commandEdit, 0, uintptr(margin+labelW+gap), uintptr(50), uintptr(cmdW), uintptr(lineH), swpNoZOrder)
	procSetWindowPos.Call(d.runButton, 0, uintptr(margin), uintptr(86), uintptr(86), uintptr(30), swpNoZOrder)
	procSetWindowPos.Call(d.clipboardCheck, 0, uintptr(margin+104), uintptr(88), uintptr(150), uintptr(26), swpNoZOrder)
	statusX := margin + 264
	procSetWindowPos.Call(d.statusLabel, 0, uintptr(statusX), uintptr(90), uintptr(maxInt(160, width-margin-statusX)), uintptr(38), swpNoZOrder)
	outputTop := 136
	outputH := maxInt(120, height-outputTop-margin)
	procSetWindowPos.Call(d.outputEdit, 0, uintptr(margin), uintptr(outputTop), uintptr(width-margin*2), uintptr(outputH), swpNoZOrder)
}

func (d *remoteCommandDialog) wndProc(message uint32, wParam, lParam uintptr) uintptr {
	switch message {
	case wmClose:
		procDestroyWindow.Call(d.hwnd)
		return 0
	case wmDestroy:
		d.saveTimeoutFromEdit()
		if d.darkBrush != 0 {
			procDeleteObject.Call(d.darkBrush)
			d.darkBrush = 0
		}
		if d.app != nil {
			d.app.mu.Lock()
			delete(d.app.commandDialogs, d.hwnd)
			d.app.mu.Unlock()
		}
		return 0
	case wmSize:
		d.layout()
		d.invalidate()
		return 0
	case wmEraseBkgnd:
		return 1
	case wmKeyDown, wmSysKeyDown:
		if d.app != nil {
			if d.app.handleLocalHotkey(int(wParam), true) {
				return 0
			}
			if d.app.shouldCaptureKeyboard() {
				vk := int(wParam)
				repeat := d.app.noteKeyEvent(vk, true, keyRepeat(lParam))
				d.app.sendInputAsync(controllerInput{
					Type:     "keydown",
					VK:       vk,
					Extended: keyExtended(vk, lParam),
					Repeat:   repeat,
				})
			}
		}
		ret, _, _ := procDefWindowProcW.Call(d.hwnd, uintptr(message), wParam, lParam)
		return ret
	case wmKeyUp, wmSysKeyUp:
		if d.app != nil {
			if d.app.handleLocalHotkey(int(wParam), false) {
				return 0
			}
			if d.app.shouldCaptureKeyboard() {
				vk := int(wParam)
				d.app.noteKeyEvent(vk, false, false)
				d.app.sendInputAsync(controllerInput{
					Type:     "keyup",
					VK:       vk,
					Extended: keyExtended(vk, lParam),
				})
			}
		}
		ret, _, _ := procDefWindowProcW.Call(d.hwnd, uintptr(message), wParam, lParam)
		return ret
	case wmCommand:
		switch lowWord(wParam) {
		case remoteCommandRunID:
			d.runCommand()
			return 0
		case remoteCommandClipboardID:
			d.toggleClipboardSync()
			return 0
		case remoteCommandInputID:
			d.toggleInputSync()
			return 0
		}
	case wmCtlColorEdit, wmCtlColorListBox, wmCtlColorBtn, wmCtlColorScrollbar, wmCtlColorStatic:
		return d.applyDarkControlColors(wParam)
	case wmPaint:
		d.paint()
		return 0
	}
	ret, _, _ := procDefWindowProcW.Call(d.hwnd, uintptr(message), wParam, lParam)
	return ret
}

func (d *remoteCommandDialog) applyDarkControlColors(hdc uintptr) uintptr {
	if d == nil || hdc == 0 {
		return 0
	}
	if d.darkBrush == 0 {
		d.darkBrush, _, _ = procCreateSolidBrush.Call(0x00111820)
	}
	procSetBkMode.Call(hdc, opaque)
	procSetBkColor.Call(hdc, 0x00111820)
	procSetTextColor.Call(hdc, 0x00F0F6FC)
	return d.darkBrush
}

func (d *remoteCommandDialog) setClipboardCheckbox(enabled bool) {
	if d == nil || d.clipboardCheck == 0 {
		return
	}
	check := uintptr(0)
	if enabled {
		check = bstChecked
	}
	procSendMessageW.Call(d.clipboardCheck, bmSetCheck, check, 0)
}

func (d *remoteCommandDialog) clipboardCheckboxEnabled() bool {
	if d == nil || d.clipboardCheck == 0 {
		return true
	}
	checked, _, _ := procSendMessageW.Call(d.clipboardCheck, bmGetCheck, 0, 0)
	return checked == bstChecked
}

func (d *remoteCommandDialog) setInputCheckbox(enabled bool) {
	if d == nil || d.inputCheck == 0 {
		return
	}
	check := uintptr(0)
	if enabled {
		check = bstChecked
	}
	procSendMessageW.Call(d.inputCheck, bmSetCheck, check, 0)
}

func (d *remoteCommandDialog) inputCheckboxEnabled() bool {
	if d == nil || d.inputCheck == 0 {
		return true
	}
	checked, _, _ := procSendMessageW.Call(d.inputCheck, bmGetCheck, 0, 0)
	return checked == bstChecked
}

func (d *remoteCommandDialog) toggleClipboardSync() {
	if d == nil || d.app == nil {
		return
	}
	enabled := d.clipboardCheckboxEnabled()
	d.app.setClipboardSyncLocal(enabled)
	if enabled {
		d.setStatus("Clipboard sync enabled.")
	} else {
		d.setStatus("Clipboard sync disabled. Remote clipboard will not be touched.")
	}
	go func() {
		if err := d.app.setRemoteClipboardSync(enabled); err != nil {
			d.setStatus("Clipboard sync update failed: " + err.Error())
			return
		}
		if enabled {
			d.setStatus("Clipboard sync enabled.")
		} else {
			d.setStatus("Clipboard sync disabled.")
		}
	}()
}

func (d *remoteCommandDialog) toggleInputSync() {
	if d == nil || d.app == nil {
		return
	}
	enabled := d.inputCheckboxEnabled()
	d.app.mu.Lock()
	d.app.keyboardCapture = enabled
	d.app.mu.Unlock()
	if enabled {
		d.setStatus("Input sync enabled.")
	} else {
		d.setStatus("Input sync disabled. Mouse and keyboard will not be sent")
	}
}

func (d *remoteCommandDialog) refreshClipboardSync() {
	if d == nil || d.app == nil {
		return
	}
	deviceID := strings.TrimSpace(d.app.currentDeviceID())
	if deviceID == "" {
		return
	}
	state, err := d.app.fetchRemoteClipboard(deviceID, -1)
	if err != nil || state.Sync == nil {
		return
	}
	enabled := *state.Sync
	d.app.setClipboardSyncLocal(enabled)
	d.setClipboardCheckbox(enabled)
}

func (d *remoteCommandDialog) refreshDir() {
	if d == nil || d.app == nil {
		return
	}
	opID := newRemoteFileOpID()
	req := remoteCommandRequest{ID: opID, Kind: "cwd"}
	raw, _ := json.Marshal(req)
	d.app.sendInputAsync(controllerInput{Type: "remote_command", Text: string(raw)})
	go func() {
		result, err := d.app.waitRemoteCommandResult(opID, 10*time.Second)
		if err != nil {
			d.setStatus("Directory query failed: " + err.Error())
			return
		}
		if strings.TrimSpace(result.Dir) != "" {
			setWindowText(d.dirEdit, result.Dir)
		}
		d.setStatus("Ready. Directory is kept on client exits.")
	}()
}

func (d *remoteCommandDialog) savedTimeoutSec() int {
	if d == nil || d.app == nil {
		return defaultRemoteCommandTimeoutSec
	}
	return d.app.savedRemoteCommandTimeoutSec()
}

func (d *remoteCommandDialog) saveTimeoutFromEdit() int {
	timeoutSec := d.savedTimeoutSec()
	if d != nil && d.timeoutEdit != 0 {
		timeoutText := strings.TrimSpace(getWindowText(d.timeoutEdit))
		if timeoutText != "" {
			if n, err := strconv.Atoi(timeoutText); err == nil {
				timeoutSec = clampInt(n, 0, 3600)
			}
		}
		setWindowText(d.timeoutEdit, strconv.Itoa(timeoutSec))
	}
	if d != nil && d.app != nil {
		d.app.saveRemoteCommandTimeoutSec(timeoutSec)
	}
	return timeoutSec
}

func (d *remoteCommandDialog) runCommand() {
	if d == nil || d.app == nil {
		return
	}
	command := strings.TrimSpace(getWindowText(d.commandEdit))
	timeoutSec := d.saveTimeoutFromEdit()
	opID := newRemoteFileOpID()
	kind := "exec"
	waitFor := time.Duration(timeoutSec+5) * time.Second
	requestTimeoutSec := timeoutSec
	if command == "" {
		kind = "setcwd"
		requestTimeoutSec = 10
		waitFor = 10 * time.Second
	}
	if kind == "exec" {
		d.addHistory(command)
		setWindowText(d.commandEdit, "")
	}
	req := remoteCommandRequest{
		ID:         opID,
		Kind:       kind,
		Command:    command,
		Dir:        strings.TrimSpace(getWindowText(d.dirEdit)),
		TimeoutSec: requestTimeoutSec,
	}
	raw, _ := json.Marshal(req)
	d.app.sendInputAsync(controllerInput{Type: "remote_command", Text: string(raw)})
	if kind == "setcwd" {
		d.setStatus("Updating remote command directory...")
		d.setOutputText("Updating directory...")
		go func() {
			result, err := d.app.waitRemoteCommandResult(opID, waitFor)
			if err != nil {
				d.setStatus("Directory update failed: " + err.Error())
				return
			}
			d.applyResult(result)
		}()
		return
	}
	if timeoutSec == 0 {
		d.setStatus("Command sent. Not waiting for output.")
		d.setOutputText("Command sent without waiting.\r\nRemote process may continue running.")
		return
	}
	d.mu.Lock()
	d.waiting = true
	d.lastOpID = opID
	d.mu.Unlock()
	d.setStatus(fmt.Sprintf("Running, waiting up to %ds...", timeoutSec))
	d.setOutputText("Running...")
	go func() {
		result, err := d.app.waitRemoteCommandResult(opID, waitFor)
		d.mu.Lock()
		d.waiting = false
		d.mu.Unlock()
		if err != nil {
			d.setStatus("Command result failed: " + err.Error())
			return
		}
		d.applyResult(result)
	}()
}

func (d *remoteCommandDialog) handleEnterKey(keyDown bool) bool {
	if d == nil {
		return false
	}
	d.mu.Lock()
	held := d.enterHeld
	if keyDown && !held {
		d.enterHeld = true
	}
	if !keyDown {
		d.enterHeld = false
	}
	shouldRun := keyDown && !held
	d.mu.Unlock()
	if shouldRun {
		d.runCommand()
	}
	return true
}

func (d *remoteCommandDialog) handleHistoryKey(vk int, keyDown bool) bool {
	if d == nil {
		return false
	}
	if vk != vkUp && vk != vkDown {
		return false
	}
	d.mu.Lock()
	held := d.upHeld
	if vk == vkDown {
		held = d.downHeld
	}
	if keyDown && !held {
		if vk == vkUp {
			d.upHeld = true
		} else {
			d.downHeld = true
		}
	}
	if !keyDown {
		if vk == vkUp {
			d.upHeld = false
		} else {
			d.downHeld = false
		}
	}
	shouldMove := keyDown && !held
	d.mu.Unlock()
	if shouldMove {
		d.moveHistory(vk == vkUp)
	}
	return true
}

func (d *remoteCommandDialog) addHistory(command string) {
	command = strings.TrimSpace(command)
	if d == nil || command == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.history) == 0 || d.history[len(d.history)-1] != command {
		d.history = append(d.history, command)
		if len(d.history) > 100 {
			d.history = append([]string(nil), d.history[len(d.history)-100:]...)
		}
	}
	d.historyPos = len(d.history)
}

func (d *remoteCommandDialog) moveHistory(up bool) {
	if d == nil || d.commandEdit == 0 {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.history) == 0 {
		return
	}
	if d.historyPos < 0 || d.historyPos > len(d.history) {
		d.historyPos = len(d.history)
	}
	if up {
		if d.historyPos > 0 {
			d.historyPos--
		}
	} else if d.historyPos < len(d.history) {
		d.historyPos++
	}
	if d.historyPos >= len(d.history) {
		setWindowText(d.commandEdit, "")
		return
	}
	setWindowText(d.commandEdit, d.history[d.historyPos])
}

func (d *remoteCommandDialog) applyResult(result remoteCommandResult) {
	if d == nil {
		return
	}
	if strings.TrimSpace(result.Dir) != "" {
		setWindowText(d.dirEdit, result.Dir)
	}
	lines := []string{}
	if result.Dir != "" {
		lines = append(lines, "Directory: "+result.Dir)
	}
	lines = append(lines, fmt.Sprintf("ExitCode: %d", result.ExitCode))
	if result.TimedOut {
		lines = append(lines, "Timed out: command is still running remotely.")
	}
	if strings.TrimSpace(result.Error) != "" {
		lines = append(lines, "Error: "+result.Error)
	}
	output := strings.TrimRight(result.Output, "\r\n")
	if output == "" {
		output = "(no output)"
	}
	lines = append(lines, "", output)
	d.setOutputText(strings.Join(lines, "\r\n"))
	if result.TimedOut {
		d.setStatus("Timeout reached. Remote command was not killed.")
	} else if strings.TrimSpace(result.Error) != "" {
		d.setStatus("Command finished with error.")
	} else {
		d.setStatus("Command finished.")
	}
}

func (d *remoteCommandDialog) setOutputText(text string) {
	if d == nil || d.outputEdit == 0 {
		return
	}
	setWindowText(d.outputEdit, text)
	procInvalidateRect.Call(d.outputEdit, 0, 1)
	procUpdateWindow.Call(d.outputEdit)
}

func (d *remoteCommandDialog) setStatus(text string) {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.statusText = text
	d.mu.Unlock()
	if d.statusLabel != 0 {
		setWindowText(d.statusLabel, text)
	}
	d.invalidate()
}

func (d *remoteCommandDialog) invalidate() {
	if d == nil || d.hwnd == 0 {
		return
	}
	procInvalidateRect.Call(d.hwnd, 0, 0)
}

func (d *remoteCommandDialog) paint() {
	if d == nil || d.hwnd == 0 {
		return
	}
	var ps paintStruct
	hdc, _, _ := procBeginPaint.Call(d.hwnd, uintptr(unsafe.Pointer(&ps)))
	if hdc != 0 {
		var rc rect
		procGetClientRect.Call(d.hwnd, uintptr(unsafe.Pointer(&rc)))
		brush, _, _ := procCreateSolidBrush.Call(0x00111820)
		if brush != 0 {
			procFillRect.Call(hdc, uintptr(unsafe.Pointer(&rc)), brush)
			procDeleteObject.Call(brush)
		}
	}
	procEndPaint.Call(d.hwnd, uintptr(unsafe.Pointer(&ps)))
}

func (d *remoteFileDialog) createControls() {
	if d == nil || d.hwnd == 0 {
		return
	}
	d.darkBrush, _, _ = procCreateSolidBrush.Call(0x00111820)
	d.pathEdit = createChildControl(d.hwnd, "EDIT", "", wsChild|wsVisible|wsBorder|wsClipSiblings|esAutoHScroll, 16, 16, 460, 26, remoteFilePathID)
	d.goButton = createChildControl(d.hwnd, "BUTTON", "Go", wsChild|wsVisible|wsClipSiblings, 486, 15, 76, 28, remoteFileGoID)
	d.saveButton = createChildControl(d.hwnd, "BUTTON", "Save to...", wsChild|wsVisible|wsClipSiblings, 572, 15, 116, 28, remoteFileSaveToID)
	d.commandButton = createChildControl(d.hwnd, "BUTTON", "Command", wsChild|wsVisible|wsClipSiblings, 0, 15, 96, 28, remoteFileCommandID)
	d.saveDirLabel = createChildControl(d.hwnd, "STATIC", "", wsChild|wsVisible|wsClipSiblings, 16, 52, 704, 42, remoteFileSaveDirID)
	d.searchLabel = createChildControl(d.hwnd, "STATIC", "Search", wsChild|wsVisible|wsClipSiblings, 16, 96, 62, 22, 0)
	d.searchEdit = createChildControl(d.hwnd, "EDIT", "", wsChild|wsVisible|wsBorder|wsClipSiblings|esAutoHScroll, 82, 92, 638, 26, remoteFileSearchID)
	d.listBox = createChildControl(d.hwnd, "LISTBOX", "", wsChild|wsVisible|wsBorder|wsVScroll|wsClipSiblings|lbsNotify|lbsHasStrings, 16, 128, 704, 272, remoteFileListID)
	d.statusLabel = createChildControl(d.hwnd, "STATIC", "", wsChild|wsVisible|wsClipSiblings, 16, 410, 704, 42, remoteFileStatusID)
	d.applyDarkThemeToControls()
	d.layout()
	d.updateSaveDirLabel()
	d.setStatus("Double-click folders to enter, files to download.\r\nDrag local files here to upload.")
}

func (d *remoteFileDialog) applyDarkThemeToControls() {
	if d == nil {
		return
	}
	if procSetWindowTheme.Find() != nil {
		return
	}
	for _, hwnd := range []uintptr{d.pathEdit, d.goButton, d.saveButton, d.commandButton, d.saveDirLabel, d.searchLabel, d.searchEdit, d.listBox, d.statusLabel} {
		if hwnd == 0 {
			continue
		}
		procSetWindowTheme.Call(hwnd, uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr("DarkMode_Explorer"))), 0)
	}
}

func (d *remoteFileDialog) layout() {
	if d == nil || d.hwnd == 0 {
		return
	}
	var client rect
	procGetClientRect.Call(d.hwnd, uintptr(unsafe.Pointer(&client)))
	width := maxInt(360, int(client.Right-client.Left))
	height := maxInt(260, int(client.Bottom-client.Top))
	margin := 16
	gap := 10
	goButtonW := 76
	saveButtonW := 116
	commandButtonW := 96
	pathH := 28
	saveDirH := 44
	searchH := 28
	statusH := 46
	footerH := 116
	saveDirTop := margin + pathH + 10
	searchTop := saveDirTop + saveDirH + 8
	listTop := searchTop + searchH + 8
	statusTop := height - footerH - statusH
	listH := maxInt(40, statusTop-listTop-8)
	pathW := maxInt(120, width-margin*2-goButtonW-saveButtonW-commandButtonW-gap*3)
	if d.pathEdit != 0 {
		procSetWindowPos.Call(d.pathEdit, 0, uintptr(margin), uintptr(margin), uintptr(pathW), uintptr(pathH), swpNoZOrder)
	}
	goX := margin + pathW + gap
	if d.goButton != 0 {
		procSetWindowPos.Call(d.goButton, 0, uintptr(goX), uintptr(margin), uintptr(goButtonW), uintptr(pathH), swpNoZOrder)
	}
	saveX := goX + goButtonW + gap
	if d.saveButton != 0 {
		procSetWindowPos.Call(d.saveButton, 0, uintptr(saveX), uintptr(margin), uintptr(saveButtonW), uintptr(pathH), swpNoZOrder)
	}
	commandX := saveX + saveButtonW + gap
	if d.commandButton != 0 {
		procSetWindowPos.Call(d.commandButton, 0, uintptr(commandX), uintptr(margin), uintptr(commandButtonW), uintptr(pathH), swpNoZOrder)
	}
	if d.saveDirLabel != 0 {
		procSetWindowPos.Call(d.saveDirLabel, 0, uintptr(margin), uintptr(saveDirTop), uintptr(width-margin*2), uintptr(saveDirH), swpNoZOrder)
	}
	if d.searchLabel != 0 {
		procSetWindowPos.Call(d.searchLabel, 0, uintptr(margin), uintptr(searchTop+2), uintptr(62), uintptr(22), swpNoZOrder)
	}
	if d.searchEdit != 0 {
		procSetWindowPos.Call(d.searchEdit, 0, uintptr(margin+66), uintptr(searchTop-2), uintptr(maxInt(120, width-margin*2-66)), uintptr(searchH), swpNoZOrder)
	}
	if d.listBox != 0 {
		procSetWindowPos.Call(d.listBox, 0, uintptr(margin), uintptr(listTop), uintptr(width-margin*2), uintptr(listH), swpNoZOrder)
	}
	if d.statusLabel != 0 {
		procSetWindowPos.Call(d.statusLabel, 0, uintptr(margin), uintptr(statusTop), uintptr(width-margin*2), uintptr(statusH), swpNoZOrder)
	}
}

func createChildControl(parent uintptr, className, text string, style uintptr, x, y, width, height, id int) uintptr {
	instance, _, _ := procGetModuleHandleW.Call(0)
	hwnd, _, _ := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr(className))),
		uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr(text))),
		style,
		uintptr(x),
		uintptr(y),
		uintptr(width),
		uintptr(height),
		parent,
		uintptr(id),
		instance,
		0,
	)
	return hwnd
}

func (d *remoteFileDialog) wndProc(message uint32, wParam, lParam uintptr) uintptr {
	switch message {
	case wmClose:
		procDestroyWindow.Call(d.hwnd)
		return 0
	case wmDestroy:
		if d.darkBrush != 0 {
			procDeleteObject.Call(d.darkBrush)
			d.darkBrush = 0
		}
		if d.app != nil {
			d.app.mu.Lock()
			delete(d.app.fileDialogs, d.hwnd)
			d.app.mu.Unlock()
		}
		return 0
	case wmSize:
		d.layout()
		d.invalidate()
		return 0
	case wmEraseBkgnd:
		return 1
	case wmCommand:
		id := lowWord(wParam)
		code := highWord(wParam)
		if id == remoteFileGoID {
			d.refresh(getWindowText(d.pathEdit))
			return 0
		}
		if id == remoteFileSaveToID {
			d.chooseDownloadDir()
			return 0
		}
		if id == remoteFileCommandID {
			if d.app != nil {
				d.app.toggleRemoteCommandDialog()
			}
			return 0
		}
		if id == remoteFileSearchID && code == enChange {
			d.applySearchFilter()
			return 0
		}
		if id == remoteFileListID && code == lbnDblClk {
			d.openSelected()
			return 0
		}
	case wmDropFiles:
		paths := queryDroppedFiles(wParam)
		procDragFinish.Call(wParam)
		if len(paths) > 0 {
			go d.uploadLocalFiles(paths)
		}
		return 0
	case wmCtlColorEdit, wmCtlColorListBox, wmCtlColorBtn, wmCtlColorScrollbar, wmCtlColorStatic:
		return d.applyDarkControlColors(wParam)
	case wmPaint:
		d.paint()
		return 0
	}
	ret, _, _ := procDefWindowProcW.Call(d.hwnd, uintptr(message), wParam, lParam)
	return ret
}

func (d *remoteFileDialog) applyDarkControlColors(hdc uintptr) uintptr {
	if d == nil || hdc == 0 {
		return 0
	}
	if d.darkBrush == 0 {
		d.darkBrush, _, _ = procCreateSolidBrush.Call(0x00111820)
	}
	procSetBkMode.Call(hdc, transparent)
	procSetBkColor.Call(hdc, 0x00111820)
	procSetTextColor.Call(hdc, 0x00F0F6FC)
	return d.darkBrush
}

func (d *remoteFileDialog) refresh(path string) {
	if d == nil || d.app == nil {
		return
	}
	path = strings.TrimSpace(path)
	d.setStatus("Loading remote directory...")
	opID := newRemoteFileOpID()
	req := remoteFileRequest{ID: opID, Path: path}
	raw, _ := json.Marshal(req)
	d.app.sendInputAsync(controllerInput{Type: "file_list", Text: string(raw)})
	go func() {
		result, err := d.app.waitRemoteFileResult(opID, 20*time.Second)
		if err != nil {
			d.setStatus("List failed: " + err.Error())
			return
		}
		if strings.TrimSpace(result.Error) != "" {
			d.setStatus("List failed: " + result.Error)
			return
		}
		d.applyList(result)
	}()
}

func (d *remoteFileDialog) applyList(result remoteFileResult) {
	if d == nil {
		return
	}
	path := strings.TrimSpace(result.Path)
	parent := strings.TrimSpace(result.Parent)
	previousPath := ""
	filter := ""
	d.mu.Lock()
	previousPath = d.path
	d.mu.Unlock()
	if d.searchEdit != 0 && path == previousPath {
		filter = getWindowText(d.searchEdit)
	} else if d.searchEdit != 0 {
		setWindowText(d.searchEdit, "")
	}
	entries := make([]remoteFileEntry, 0, len(result.Entries)+1)
	if parent != "" && parent != path {
		entries = append(entries, remoteFileEntry{Name: "..", Path: parent, IsDir: true})
	}
	entries = append(entries, result.Entries...)
	d.mu.Lock()
	d.path = path
	d.parent = parent
	d.allEntries = entries
	d.mu.Unlock()
	setWindowText(d.pathEdit, path)
	d.updateSaveDirLabel()
	d.renderFilteredEntries(filter)
}

func (d *remoteFileDialog) applySearchFilter() {
	if d == nil {
		return
	}
	d.renderFilteredEntries(getWindowText(d.searchEdit))
}

func (d *remoteFileDialog) renderFilteredEntries(filter string) {
	if d == nil {
		return
	}
	filter = strings.TrimSpace(filter)
	d.mu.Lock()
	allEntries := append([]remoteFileEntry(nil), d.allEntries...)
	entries := filterRemoteFileEntries(allEntries, filter)
	d.entries = entries
	d.mu.Unlock()

	if d.listBox != 0 {
		procSendMessageW.Call(d.listBox, lbResetContent, 0, 0)
		for _, entry := range entries {
			line := formatRemoteFileEntry(entry)
			procSendMessageW.Call(d.listBox, lbAddString, 0, uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr(line))))
		}
	}
	if filter == "" {
		d.setStatus(fmt.Sprintf("%d item(s). Double-click folders to enter, files to download.\r\nDrag local files here to upload.", len(entries)))
		return
	}
	d.setStatus(fmt.Sprintf("%d/%d match(es) for %q in current folder.\r\nDouble-click folders to enter, files to download.", len(entries), len(allEntries), filter))
}

func filterRemoteFileEntries(entries []remoteFileEntry, filter string) []remoteFileEntry {
	filter = strings.ToLower(strings.TrimSpace(filter))
	if filter == "" {
		return append([]remoteFileEntry(nil), entries...)
	}
	filtered := make([]remoteFileEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Name == ".." || remoteFileEntryMatchesFilter(entry, filter) {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

func remoteFileEntryMatchesFilter(entry remoteFileEntry, filter string) bool {
	name := strings.ToLower(strings.TrimSpace(entry.Name))
	path := strings.ToLower(strings.TrimSpace(entry.Path))
	return strings.Contains(name, filter) || strings.Contains(path, filter)
}

func (d *remoteFileDialog) openSelected() {
	if d == nil {
		return
	}
	idx, _, _ := procSendMessageW.Call(d.listBox, lbGetCurSel, 0, 0)
	index := int(int32(idx))
	d.mu.Lock()
	if index < 0 || index >= len(d.entries) {
		d.mu.Unlock()
		return
	}
	entry := d.entries[index]
	d.mu.Unlock()
	if entry.IsDir {
		d.refresh(entry.Path)
		return
	}
	go d.downloadRemoteFile(entry)
}

func (d *remoteFileDialog) downloadRemoteFile(entry remoteFileEntry) {
	if d == nil || d.app == nil {
		return
	}
	d.setStatus("Preparing download: " + entry.Name)
	opID := newRemoteFileOpID()
	req := remoteFileRequest{ID: opID, Path: entry.Path}
	raw, _ := json.Marshal(req)
	d.app.sendInputAsync(controllerInput{Type: "file_download", Text: string(raw)})
	result, err := d.app.waitRemoteFileResult(opID, 60*time.Second)
	if err != nil {
		d.setStatus("Download failed: " + err.Error())
		return
	}
	if result.Error != "" {
		d.setStatus("Download failed: " + result.Error)
		return
	}
	if result.File == nil {
		d.setStatus("Download failed: no transfer returned")
		return
	}
	d.addTransferID(result.File.ID)
	targetDir := d.currentDownloadDir()
	if strings.TrimSpace(targetDir) == "" {
		targetDir = controllerProgramRunDir()
	}
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		d.setStatus("Download failed: " + err.Error())
		return
	}
	path, err := d.app.downloadTransferFile(*result.File, targetDir)
	if err != nil {
		d.setStatus("Download failed: " + err.Error())
		return
	}
	d.setStatus("Downloaded: " + path)
}

func (d *remoteFileDialog) currentDownloadDir() string {
	if d == nil {
		return controllerProgramRunDir()
	}
	d.mu.Lock()
	dir := strings.TrimSpace(d.downloadDir)
	d.mu.Unlock()
	if dir == "" {
		dir = controllerProgramRunDir()
	}
	return dir
}

func (d *remoteFileDialog) updateSaveDirLabel() {
	if d == nil || d.saveDirLabel == 0 {
		return
	}
	setWindowText(d.saveDirLabel, "Save to: "+wrapDisplayPath(d.currentDownloadDir()))
}

func (a *controllerApp) currentRemoteFileDownloadDir() string {
	if a == nil {
		return controllerProgramRunDir()
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	dir := strings.TrimSpace(a.remoteFileDownloadDir)
	if dir == "" {
		dir = controllerProgramRunDir()
		a.remoteFileDownloadDir = dir
	}
	return dir
}

func (a *controllerApp) setRemoteFileDownloadDir(dir string) {
	if a == nil {
		return
	}
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return
	}
	a.mu.Lock()
	a.remoteFileDownloadDir = dir
	a.mu.Unlock()
}

func (d *remoteFileDialog) chooseDownloadDir() {
	if d == nil {
		return
	}
	dir, ok := browseForFolder(d.hwnd, "Choose download folder")
	if !ok {
		return
	}
	d.mu.Lock()
	d.downloadDir = dir
	d.mu.Unlock()
	if d.app != nil {
		d.app.setRemoteFileDownloadDir(dir)
	}
	d.updateSaveDirLabel()
	d.setStatus("Download folder updated.\r\nDouble-click a remote file to download here.")
}

func (d *remoteFileDialog) uploadLocalFiles(paths []string) {
	if d == nil || d.app == nil || len(paths) == 0 {
		return
	}
	d.mu.Lock()
	target := d.path
	d.mu.Unlock()
	if strings.TrimSpace(target) == "" {
		target = getWindowText(d.pathEdit)
	}
	d.setStatus(fmt.Sprintf("Uploading %d file(s)...", len(paths)))
	d.app.mu.RLock()
	deviceID := d.app.deviceID
	d.app.mu.RUnlock()
	started := 0
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			continue
		}
		_, err = d.app.uploadLocalFileNotify(deviceID, path, info.Size(), func(ref transferFileRef) {
			d.addTransferID(ref.ID)
			payload := transferCommandPayload{Kind: "dialog-upload", Target: target, Files: []transferFileRef{ref}}
			raw, err := json.Marshal(payload)
			if err != nil {
				d.setStatus("Upload failed: " + err.Error())
				return
			}
			d.app.sendInputAsync(controllerInput{Type: "transfer_files", Text: string(raw)})
			started++
		})
		if err != nil {
			d.setStatus("Upload failed: " + err.Error())
			continue
		}
	}
	if started == 0 {
		d.setStatus("No uploadable files.")
		return
	}
	d.setStatus(fmt.Sprintf("Streaming %d file(s) to remote agent...", started))
}

func (d *remoteFileDialog) addTransferID(id string) {
	id = strings.TrimSpace(id)
	if d == nil || id == "" {
		return
	}
	d.mu.Lock()
	for _, existing := range d.transferIDs {
		if existing == id {
			d.mu.Unlock()
			d.invalidate()
			return
		}
	}
	d.transferIDs = append(d.transferIDs, id)
	d.mu.Unlock()
	d.invalidate()
}

func (d *remoteFileDialog) setStatus(text string) {
	if d == nil {
		return
	}
	setWindowText(d.statusLabel, text)
	d.invalidate()
}

func (d *remoteFileDialog) invalidate() {
	if d != nil && d.hwnd != 0 {
		procInvalidateRect.Call(d.hwnd, 0, 0)
	}
}

func (d *remoteFileDialog) paint() {
	var ps paintStruct
	hdc, _, _ := procBeginPaint.Call(d.hwnd, uintptr(unsafe.Pointer(&ps)))
	defer procEndPaint.Call(d.hwnd, uintptr(unsafe.Pointer(&ps)))
	if hdc == 0 {
		return
	}
	var client rect
	procGetClientRect.Call(d.hwnd, uintptr(unsafe.Pointer(&client)))
	brush, _, _ := procCreateSolidBrush.Call(0x000A0F16)
	if brush != 0 {
		procFillRect.Call(hdc, uintptr(unsafe.Pointer(&client)), brush)
		procDeleteObject.Call(brush)
	}
	width := maxInt(360, int(client.Right-client.Left))
	height := maxInt(260, int(client.Bottom-client.Top))
	view := d.progressView()
	if view.Line == "" {
		drawText(hdc, 16, height-58, "F7 opens remote command. F8 opens this file manager. F9 hides HUD and disables hotkeys.", 0x00DDE6EE)
		return
	}
	drawText(hdc, 16, height-94, view.Line, 0x00DDE6EE)
	drawTransferProgressBar(hdc, 16, height-64, width-32, view.ServerLabel, view.ServerPercent, view.ServerRateBPS, view.Status)
	drawTransferProgressBar(hdc, 16, height-36, width-32, view.EndpointLabel, view.EndpointPercent, view.EndpointRateBPS, view.Status)
}

func drawTransferProgressBar(hdc uintptr, x, y, width int, label string, percent int, rateBPS float64, status string) {
	percent = clampInt(percent, 0, 100)
	label = strings.TrimSpace(label)
	if label == "" {
		label = "stage"
	}
	rateText := ""
	if !isTransferTerminal(status) && percent < 100 {
		rateText = " --/s"
		if rateBPS > 0 {
			rateText = " " + formatBytes(int64(rateBPS)) + "/s"
		}
	}
	text := fmt.Sprintf("%s %d%%%s", label, percent, rateText)
	barLeft := x + 170
	if width < 290 {
		barLeft = x
	}
	bar := rect{Left: int32(barLeft), Top: int32(y), Right: int32(maxInt(barLeft+1, x+width)), Bottom: int32(y + 14)}
	drawFilledRect(hdc, bar, 0x0026313F)
	if percent > 0 {
		fill := bar
		fill.Right = fill.Left + int32((int(bar.Right-bar.Left)*percent)/100)
		drawFilledRect(hdc, fill, transferProgressColorForPercent(status, percent))
	}
	drawText(hdc, x, y-2, text, 0x00B8C4D0)
}

func (d *remoteFileDialog) progressView() transferProgressView {
	if d == nil || d.app == nil {
		return transferProgressView{}
	}
	d.mu.Lock()
	ids := append([]string(nil), d.transferIDs...)
	d.mu.Unlock()
	d.app.mu.RLock()
	defer d.app.mu.RUnlock()
	var best *controllerTransferState
	for _, id := range ids {
		if state := d.app.transfers[id]; state != nil {
			if best == nil || state.UpdatedAt.After(best.UpdatedAt) {
				best = state
			}
		}
	}
	if best == nil {
		return transferProgressView{}
	}
	return formatTransferProgressView(best, 32)
}

func transferProgressColor(status string) uint32 {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed":
		return 0x0068D391
	case "failed", "canceled":
		return 0x004859F2
	default:
		return 0x0000B7FF
	}
}

func transferProgressColorForPercent(status string, percent int) uint32 {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "failed", "canceled":
		return 0x004859F2
	}
	if percent >= 100 {
		return 0x0068D391
	}
	return 0x0000B7FF
}

func transferPrimaryProgressBytes(state *controllerTransferState) int64 {
	if state == nil {
		return 0
	}
	total := maxInt64(state.Size, 0)
	if strings.EqualFold(state.Status, "completed") && total > 0 {
		return total
	}
	direction := strings.TrimSpace(strings.ToLower(state.Direction))
	switch direction {
	case "controller_to_agent", "agent_to_controller":
		if strings.EqualFold(state.Status, "receiving") || state.DeliveredBytes > 0 {
			return maxInt64(state.DeliveredBytes, 0)
		}
		return maxInt64(state.UploadedBytes, 0)
	default:
		return maxInt64(maxInt64(state.UploadedBytes, state.DeliveredBytes), 0)
	}
}

func updateTransferRateSample(now time.Time, bytes int64, lastBytes *int64, lastSampleAt *time.Time, rateBPS *float64) {
	if lastBytes == nil || lastSampleAt == nil || rateBPS == nil {
		return
	}
	bytes = maxInt64(bytes, 0)
	if lastSampleAt.IsZero() || bytes < *lastBytes {
		*lastBytes = bytes
		*lastSampleAt = now
		return
	}
	elapsed := now.Sub(*lastSampleAt)
	if elapsed < 200*time.Millisecond {
		return
	}
	delta := bytes - *lastBytes
	if delta > 0 {
		*rateBPS = float64(delta) / elapsed.Seconds()
		*lastBytes = bytes
		*lastSampleAt = now
		return
	}
	if elapsed > 2*time.Second {
		*rateBPS = 0
		*lastBytes = bytes
		*lastSampleAt = now
	}
}

func updateTransferStageRates(state *controllerTransferState, now time.Time) {
	if state == nil {
		return
	}
	updateTransferRateSample(now, state.UploadedBytes, &state.UploadedLastBytes, &state.UploadedLastSampleAt, &state.UploadedRateBPS)
	updateTransferRateSample(now, state.DeliveredBytes, &state.DeliveredLastBytes, &state.DeliveredLastSampleAt, &state.DeliveredRateBPS)
	state.RateBPS = transferActiveRateBPS(state)
	state.LastBytes = transferPrimaryProgressBytes(state)
	state.LastSampleAt = now
}

func transferActiveRateBPS(state *controllerTransferState) float64 {
	if state == nil || isTransferTerminal(state.Status) {
		return 0
	}
	direction := strings.TrimSpace(strings.ToLower(state.Direction))
	status := strings.TrimSpace(strings.ToLower(state.Status))
	switch direction {
	case "controller_to_agent":
		if status == "uploading" || (state.DeliveredBytes <= 0 && state.UploadedBytes < state.Size) {
			return state.UploadedRateBPS
		}
		if state.DeliveredRateBPS > 0 || state.DeliveredBytes > 0 || status == "receiving" {
			return state.DeliveredRateBPS
		}
		return state.UploadedRateBPS
	case "agent_to_controller":
		if status == "receiving" || state.DeliveredBytes > 0 {
			return state.DeliveredRateBPS
		}
		return state.UploadedRateBPS
	default:
		if state.DeliveredRateBPS > 0 {
			return state.DeliveredRateBPS
		}
		return state.UploadedRateBPS
	}
}

func formatTransferProgressLine(state *controllerTransferState, maxNameLen int) (string, int) {
	view := formatTransferProgressView(state, maxNameLen)
	if view.Line == "" {
		return "", 0
	}
	primary := transferPrimaryProgressBytes(state)
	total := maxInt64(state.Size, 0)
	return view.Line, transferPercent(primary, total, state.Status)
}

func appendTransferETA(line string, state *controllerTransferState, primary, total int64) string {
	if state == nil || isTransferTerminal(state.Status) || total <= primary {
		return line
	}
	rate := transferActiveRateBPS(state)
	if rate <= 0 {
		return line
	}
	eta := time.Duration(float64(total-primary)/rate) * time.Second
	return line + "  ETA " + formatDurationShort(eta)
}

func formatTransferProgressView(state *controllerTransferState, maxNameLen int) transferProgressView {
	if state == nil {
		return transferProgressView{}
	}
	total := maxInt64(state.Size, 0)
	primary := transferPrimaryProgressBytes(state)
	uploaded := minInt64(maxInt64(state.UploadedBytes, 0), total)
	delivered := minInt64(maxInt64(state.DeliveredBytes, 0), total)
	if total <= 0 {
		uploaded = maxInt64(state.UploadedBytes, 0)
		delivered = maxInt64(state.DeliveredBytes, 0)
	}
	if strings.EqualFold(state.Status, "completed") && total > 0 {
		primary = total
		uploaded = total
		delivered = total
	}
	status := strings.ToUpper(nonEmpty(state.Status, "?"))
	name := trimLabel(state.Name, maxNameLen)
	direction := strings.TrimSpace(strings.ToLower(state.Direction))
	view := transferProgressView{
		Status:          state.Status,
		ServerLabel:     "server",
		EndpointLabel:   "endpoint",
		ServerPercent:   transferPercent(uploaded, total, state.Status),
		EndpointPercent: transferPercent(delivered, total, state.Status),
		ServerRateBPS:   state.UploadedRateBPS,
		EndpointRateBPS: state.DeliveredRateBPS,
	}
	switch direction {
	case "controller_to_agent":
		view.EndpointLabel = "remote"
		view.Line = fmt.Sprintf("%s  SEND %s  server %s/%s  remote %s/%s",
			status,
			name,
			formatBytes(uploaded),
			formatBytes(total),
			formatBytes(delivered),
			formatBytes(total),
		)
	case "agent_to_controller":
		view.EndpointLabel = "local"
		view.Line = fmt.Sprintf("%s  GET %s  server %s/%s  local %s/%s",
			status,
			name,
			formatBytes(uploaded),
			formatBytes(total),
			formatBytes(delivered),
			formatBytes(total),
		)
	default:
		view.Line = fmt.Sprintf("%s  %s  %s/%s", status, name, formatBytes(primary), formatBytes(total))
	}
	if state.Error != "" && isTransferTerminal(state.Status) {
		view.Line += "  " + trimLabel(state.Error, 36)
	}
	view.Line = appendTransferETA(view.Line, state, primary, total)
	return view
}

func transferPercent(value, total int64, status string) int {
	if total > 0 {
		return int(minInt64(maxInt64(value, 0), total) * 100 / total)
	}
	if strings.EqualFold(status, "completed") {
		return 100
	}
	return 0
}

func (a *controllerApp) waitRemoteFileResult(id string, timeout time.Duration) (remoteFileResult, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		result, ok, err := a.fetchRemoteFileResult(id)
		if err != nil {
			return result, err
		}
		if ok {
			return result, nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return remoteFileResult{}, fmt.Errorf("timeout waiting remote file result")
}

func (a *controllerApp) fetchRemoteFileResult(id string) (remoteFileResult, bool, error) {
	var result remoteFileResult
	endpoint := fmt.Sprintf("%s/api/file/result?id=%s", a.serverURL, url.QueryEscape(id))
	resp, err := a.http.Get(a.withAdminKey(endpoint))
	if err != nil {
		return result, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return result, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return result, false, fmt.Errorf("file result request failed: %s", strings.TrimSpace(string(raw)))
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return result, false, err
	}
	return result, true, nil
}

func (a *controllerApp) waitRemoteCommandResult(id string, timeout time.Duration) (remoteCommandResult, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		result, ok, err := a.fetchRemoteCommandResult(id)
		if err != nil {
			return result, err
		}
		if ok {
			return result, nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return remoteCommandResult{}, fmt.Errorf("timeout waiting remote command result")
}

func (a *controllerApp) fetchRemoteCommandResult(id string) (remoteCommandResult, bool, error) {
	var result remoteCommandResult
	endpoint := fmt.Sprintf("%s/api/command/result?id=%s", a.serverURL, url.QueryEscape(id))
	resp, err := a.http.Get(a.withAdminKey(endpoint))
	if err != nil {
		return result, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return result, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return result, false, fmt.Errorf("command result request failed: %s", strings.TrimSpace(string(raw)))
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return result, false, err
	}
	return result, true, nil
}

func formatRemoteFileEntry(entry remoteFileEntry) string {
	if entry.IsDir {
		if entry.Name == ".." {
			return "DIR   ..  (parent)"
		}
		return "DIR   " + entry.Name
	}
	return fmt.Sprintf("FILE  %-36s  %10s  %s", trimLabel(entry.Name, 36), formatBytes(entry.Size), entry.ModTime)
}

func newRemoteFileOpID() string {
	return fmt.Sprintf("file-%d", time.Now().UnixNano())
}

func setWindowText(hwnd uintptr, text string) {
	if hwnd == 0 {
		return
	}
	procSetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr(text))))
}

func getWindowText(hwnd uintptr) string {
	if hwnd == 0 {
		return ""
	}
	n, _, _ := procGetWindowTextLengthW.Call(hwnd)
	if n == 0 {
		return ""
	}
	buf := make([]uint16, int(n)+1)
	procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return syscall.UTF16ToString(buf)
}

func formatDurationShort(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	seconds := int(d.Seconds() + 0.5)
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	minutes := seconds / 60
	seconds %= 60
	if minutes < 60 {
		return fmt.Sprintf("%dm%02ds", minutes, seconds)
	}
	hours := minutes / 60
	minutes %= 60
	return fmt.Sprintf("%dh%02dm", hours, minutes)
}

func (a *controllerApp) streamLoop() {
	for {
		if a.deviceID == "" {
			deviceID, err := a.pickDevice()
			if err == nil && deviceID != "" {
				a.deviceID = deviceID
				a.updateTitle()
			} else {
				time.Sleep(1200 * time.Millisecond)
				continue
			}
		}

		if a.deviceID != "" {
			if err := a.runPreferredStream(); err != nil {
				log.Printf("frame stream disconnected: %v", err)
			}
			a.setStreamConnected(false, "")
			time.Sleep(900 * time.Millisecond)
		}
	}
}

func (a *controllerApp) runPreferredStream() error {
	switch a.transport {
	case "ws":
		return a.runFrameStream()
	case "quic":
		return a.runQUICStream()
	default:
		if err := a.runQUICStream(); err != nil {
			log.Printf("quic stream unavailable endpoint=%s, fallback to websocket: %v (hint: open UDP port on server/security group/firewall)", a.quicEndpoint(), err)
			return a.runFrameStream()
		}
		return nil
	}
}

func (a *controllerApp) prepareStartupDeviceSelection(listOnly bool) (bool, error) {
	if a == nil {
		return false, nil
	}
	if a.adminKey == "" {
		if listOnly {
			return false, fmt.Errorf("missing admin key: use -key to list devices")
		}
		return false, nil
	}
	if strings.TrimSpace(a.deviceID) != "" {
		if listOnly {
			data, err := a.fetchDevices()
			if err != nil {
				return false, err
			}
			a.logDeviceList(data)
			return true, nil
		}
		return false, nil
	}
	data, err := a.fetchDevices()
	if err != nil {
		return false, err
	}
	if listOnly {
		a.logDeviceList(data)
		return true, nil
	}
	onlineCount := 0
	firstOnlineID := ""
	firstOnlineName := ""
	firstOnlineW := 0
	firstOnlineH := 0
	for _, device := range data.Devices {
		if !device.Online {
			continue
		}
		onlineCount++
		if firstOnlineID == "" {
			firstOnlineID = device.DeviceID
			firstOnlineName = displayDeviceName(device.Alias, device.Name)
			firstOnlineW = device.Width
			firstOnlineH = device.Height
		}
	}
	if onlineCount > 1 {
		a.logDeviceList(data)
		// log.Printf("[controller][devices] multiple online devices detected; rerun with -device <id>")
		return true, nil
	}
	if onlineCount == 1 {
		a.deviceID = firstOnlineID
		a.deviceName = firstOnlineName
		if firstOnlineW > 0 && firstOnlineH > 0 {
			a.remoteW = firstOnlineW
			a.remoteH = firstOnlineH
			a.aspectFitDone = true
		}
		return false, nil
	}
	return false, nil
}

func (a *controllerApp) fetchDevices() (devicesResponse, error) {
	var data devicesResponse
	resp, err := a.http.Get(a.withAdminKey(a.serverURL + "/api/devices"))
	if err != nil {
		return data, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return data, fmt.Errorf("devices request failed: %s", strings.TrimSpace(string(raw)))
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return data, err
	}
	return data, nil
}

func (a *controllerApp) setDeviceAlias(deviceID, alias string) error {
	if a == nil {
		return fmt.Errorf("controller is not initialized")
	}
	req := deviceAliasRequest{
		DeviceID: strings.TrimSpace(deviceID),
		Alias:    strings.TrimSpace(alias),
	}
	if req.DeviceID == "" {
		return fmt.Errorf("device id is required")
	}
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	resp, err := a.http.Post(a.withAdminKey(a.serverURL+"/api/devices"), "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("set device alias failed: %s", strings.TrimSpace(string(raw)))
	}
	return nil
}

func (a *controllerApp) logDeviceList(data devicesResponse) {
	if len(data.Devices) == 0 {
		log.Printf("[controller][devices] no devices found")
		return
	}
	// log.Printf("[controller][devices] available devices:")
	for index, device := range data.Devices {
		status := "offline"
		if device.Online {
			status = "online"
		}
		name := strings.TrimSpace(device.Name)
		if name == "" {
			name = "-"
		}
		alias := strings.TrimSpace(device.Alias)
		if alias == "" {
			log.Printf("[controller][devices] %d. [%s] device=%s name=%s", index+1, status, strings.TrimSpace(device.DeviceID), name)
			continue
		}
		log.Printf("[controller][devices] %d. [%s] device=%s alias=%s name=%s", index+1, status, strings.TrimSpace(device.DeviceID), alias, name)
	}
}

func displayDeviceName(alias, name string) string {
	if alias = strings.TrimSpace(alias); alias != "" {
		return alias
	}
	return strings.TrimSpace(name)
}

func (a *controllerApp) pickDevice() (string, error) {
	if a.adminKey == "" {
		return "", fmt.Errorf("missing admin key: use -device to connect a known device directly")
	}
	data, err := a.fetchDevices()
	if err != nil {
		return "", err
	}
	for _, device := range data.Devices {
		if device.Online {
			a.deviceName = displayDeviceName(device.Alias, device.Name)
			return device.DeviceID, nil
		}
	}
	if len(data.Devices) > 0 {
		a.deviceName = displayDeviceName(data.Devices[0].Alias, data.Devices[0].Name)
		return data.Devices[0].DeviceID, nil
	}
	return "", nil
}

func (a *controllerApp) applyPatch(patch *image.RGBA, state controllerState) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.image == nil {
		return
	}
	patchRect := image.Rect(state.PatchX, state.PatchY, state.PatchX+state.PatchW, state.PatchY+state.PatchH)
	draw.Draw(a.image, patchRect, patch, patch.Bounds().Min, draw.Src)
	if len(a.imageBGRA) != len(a.image.Pix) {
		a.imageBGRA = rgbaToBGRA(a.image)
	} else {
		nextBGRA := append([]byte(nil), a.imageBGRA...)
		updateBGRARegion(nextBGRA, a.image, patchRect)
		a.imageBGRA = nextBGRA
	}
	a.clearScaledCacheLocked()
	a.displayFrameID = state.FrameID
	a.updateRemoteSizeLocked(state.Width, state.Height)
	a.originX = state.OriginX
	a.originY = state.OriginY
	a.remoteCursorX = state.CursorX
	a.remoteCursorY = state.CursorY
	a.remoteCursorOn = state.CursorVisible
	a.remoteCursorKind = normalizeCursorKind(state.CursorKind)
	a.invalidateRemoteRectLocked(patchRect)
}

func (a *controllerApp) applyTiles(atlas *image.RGBA, state controllerState) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.image == nil || atlas == nil {
		return
	}

	for _, tile := range state.Tiles {
		if tile.W <= 0 || tile.H <= 0 {
			continue
		}
		draw.Draw(
			a.image,
			image.Rect(tile.X, tile.Y, tile.X+tile.W, tile.Y+tile.H),
			atlas,
			image.Point{X: tile.AtlasX, Y: tile.AtlasY},
			draw.Src,
		)
	}
	if len(a.imageBGRA) != len(a.image.Pix) {
		a.imageBGRA = rgbaToBGRA(a.image)
	} else {
		nextBGRA := append([]byte(nil), a.imageBGRA...)
		for _, tile := range state.Tiles {
			if tile.W <= 0 || tile.H <= 0 {
				continue
			}
			updateBGRARegion(nextBGRA, a.image, image.Rect(tile.X, tile.Y, tile.X+tile.W, tile.Y+tile.H))
		}
		a.imageBGRA = nextBGRA
	}
	a.clearScaledCacheLocked()

	a.displayFrameID = state.FrameID
	a.updateRemoteSizeLocked(state.Width, state.Height)
	a.originX = state.OriginX
	a.originY = state.OriginY
	a.remoteCursorX = state.CursorX
	a.remoteCursorY = state.CursorY
	a.remoteCursorOn = state.CursorVisible
	a.remoteCursorKind = normalizeCursorKind(state.CursorKind)
	a.invalidateRemoteRectLocked(tileUnion(state.Tiles))
}

func (a *controllerApp) applyBGRAPatch(raw []byte, state controllerState) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.image == nil || validateBGRAPayload(raw, state.PatchW, state.PatchH) != nil {
		return
	}
	patchRect := image.Rect(state.PatchX, state.PatchY, state.PatchX+state.PatchW, state.PatchY+state.PatchH)
	copyBGRAToRGBARegion(a.image, patchRect, raw, state.PatchW, state.PatchH, image.Point{})
	if len(a.imageBGRA) != len(a.image.Pix) {
		a.imageBGRA = rgbaToBGRA(a.image)
	} else {
		nextBGRA := append([]byte(nil), a.imageBGRA...)
		copyBGRAToBGRAFrame(nextBGRA, a.image.Bounds().Dx(), a.image.Bounds().Dy(), patchRect, raw, state.PatchW, state.PatchH, image.Point{})
		a.imageBGRA = nextBGRA
	}
	a.clearScaledCacheLocked()
	a.displayFrameID = state.FrameID
	a.updateRemoteSizeLocked(state.Width, state.Height)
	a.originX = state.OriginX
	a.originY = state.OriginY
	a.remoteCursorX = state.CursorX
	a.remoteCursorY = state.CursorY
	a.remoteCursorOn = state.CursorVisible
	a.remoteCursorKind = normalizeCursorKind(state.CursorKind)
	a.invalidateRemoteRectLocked(patchRect)
}

func (a *controllerApp) applyBGRATiles(atlas []byte, atlasW, atlasH int, state controllerState) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.image == nil || validateBGRAPayload(atlas, atlasW, atlasH) != nil {
		return
	}
	if len(a.imageBGRA) != len(a.image.Pix) {
		a.imageBGRA = rgbaToBGRA(a.image)
	}
	nextBGRA := append([]byte(nil), a.imageBGRA...)
	for _, tile := range state.Tiles {
		if tile.W <= 0 || tile.H <= 0 || tile.AtlasX < 0 || tile.AtlasY < 0 || tile.AtlasX+tile.W > atlasW || tile.AtlasY+tile.H > atlasH {
			continue
		}
		dstRect := image.Rect(tile.X, tile.Y, tile.X+tile.W, tile.Y+tile.H)
		srcPoint := image.Point{X: tile.AtlasX, Y: tile.AtlasY}
		copyBGRAToRGBARegion(a.image, dstRect, atlas, atlasW, atlasH, srcPoint)
		copyBGRAToBGRAFrame(nextBGRA, a.image.Bounds().Dx(), a.image.Bounds().Dy(), dstRect, atlas, atlasW, atlasH, srcPoint)
	}
	a.imageBGRA = nextBGRA
	a.clearScaledCacheLocked()

	a.displayFrameID = state.FrameID
	a.updateRemoteSizeLocked(state.Width, state.Height)
	a.originX = state.OriginX
	a.originY = state.OriginY
	a.remoteCursorX = state.CursorX
	a.remoteCursorY = state.CursorY
	a.remoteCursorOn = state.CursorVisible
	a.remoteCursorKind = normalizeCursorKind(state.CursorKind)
	a.invalidateRemoteRectLocked(tileUnion(state.Tiles))
}

func (a *controllerApp) applyCursorFromHeader(header wsFrameEnvelope) {
	a.mu.Lock()
	a.remoteCursorX = header.CursorX
	a.remoteCursorY = header.CursorY
	a.remoteCursorOn = header.CursorOn
	a.remoteCursorKind = normalizeCursorKind(header.CursorKind)
	a.destroyRemoteCursorLocked()
	a.mu.Unlock()
	if a.hwnd != 0 {
		a.handleSetCursor(a.hwnd, htClient)
	}
}

func (a *controllerApp) applyDesktopStatus(header wsFrameEnvelope) {
	status := strings.TrimSpace(strings.ToLower(header.Status))
	if status != "unavailable" {
		status = "available"
	}
	a.mu.Lock()
	a.deviceName = displayDeviceName(header.Alias, header.Name)
	a.updateRemoteSizeLocked(header.Width, header.Height)
	if status == "unavailable" {
		a.desktopStatus = "unavailable"
		a.desktopMessage = strings.TrimSpace(header.Message)
	} else {
		a.desktopStatus = ""
		a.desktopMessage = ""
	}
	a.mu.Unlock()
	a.invalidate()
}

func (a *controllerApp) setDesktopAvailable() {
	if a == nil {
		return
	}
	a.mu.Lock()
	changed := a.desktopStatus != "" || a.desktopMessage != ""
	a.desktopStatus = ""
	a.desktopMessage = ""
	a.mu.Unlock()
	if changed {
		a.invalidate()
	}
}

func (a *controllerApp) destroyRemoteCursorLocked() {
	if a.remoteCursorImage != 0 {
		procDestroyIcon.Call(a.remoteCursorImage)
		a.remoteCursorImage = 0
	}
	a.remoteCursorKey = ""
}

func (a *controllerApp) paint(hwnd uintptr) {
	var ps paintStruct
	hdc, _, _ := procBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
	defer procEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
	if hdc == 0 {
		return
	}

	var client rect
	procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&client)))
	width := int(client.Right - client.Left)
	height := int(client.Bottom - client.Top)
	if width <= 0 || height <= 0 {
		return
	}
	dirty := intersectRect(ps.RcPaint, rect{Right: int32(width), Bottom: int32(height)})
	if emptyRect(dirty) {
		return
	}

	a.mu.RLock()
	img := a.image
	frameID := a.displayFrameID
	bgra := a.imageBGRA
	remoteW := a.remoteW
	remoteH := a.remoteH
	displayMode := a.displayMode
	stretchMode := a.stretchMode
	desktopUnavailable := a.desktopStatus == "unavailable"
	desktopMessage := a.desktopMessage
	a.mu.RUnlock()
	if img == nil || len(bgra) == 0 {
		paintBlackRect(hdc, dirty)
		if desktopUnavailable {
			a.paintDesktopUnavailable(hdc, width, height, desktopMessage)
		}
		return
	}

	drawX, drawY, drawW, drawH, ok := controllerDisplayRect(width, height, remoteW, remoteH, displayMode)
	if !ok || drawW <= 0 || drawH <= 0 {
		return
	}

	targetDC := hdc
	if a.paintBuffer.ensure(hdc, width, height) {
		targetDC = a.paintBuffer.memDC
		defer blitPaintRegion(hdc, targetDC, dirty)
	}

	paintLetterboxDirty(targetDC, width, height, drawX, drawY, drawW, drawH, dirty)
	srcW := img.Bounds().Dx()
	srcH := img.Bounds().Dy()
	srcBuf := bgra
	displayDirty := intersectRect(dirty, rect{
		Left:   int32(drawX),
		Top:    int32(drawY),
		Right:  int32(drawX + drawW),
		Bottom: int32(drawY + drawH),
	})
	upscaling := drawW > srcW || drawH > srcH
	fullDisplayPaint := coversRect(dirty, rect{
		Left:   int32(drawX),
		Top:    int32(drawY),
		Right:  int32(drawX + drawW),
		Bottom: int32(drawY + drawH),
	})
	if shouldPreScaleStretch(stretchMode) && drawW > 0 && drawH > 0 && upscaling && fullDisplayPaint {
		srcBuf = a.scaledFrameBGRA(frameID, stretchMode, bgra, srcW, srcH, drawW, drawH)
		srcW = drawW
		srcH = drawH
	}
	useHalftone := (stretchMode == "halftone" || stretchMode == "crisp" || (!upscaling && stretchMode != "pixel")) && (drawW != srcW || drawH != srcH)
	if !fullDisplayPaint && stretchMode != "pixel" && (drawW != srcW || drawH != srcH) {
		useHalftone = true
	}
	if useHalftone {
		procSetStretchBltMode.Call(targetDC, halftone)
		procSetBrushOrgEx.Call(targetDC, 0, 0, 0)
	} else {
		procSetStretchBltMode.Call(targetDC, colorOnColor)
	}
	bi := bitmapInfo{
		Header: bitmapInfoHeader{
			Size:        uint32(unsafe.Sizeof(bitmapInfoHeader{})),
			Width:       int32(srcW),
			Height:      -int32(srcH),
			Planes:      1,
			BitCount:    32,
			Compression: biRGB,
		},
	}

	if fullDisplayPaint {
		procStretchDIBits.Call(
			targetDC,
			uintptr(drawX),
			uintptr(drawY),
			uintptr(drawW),
			uintptr(drawH),
			0,
			0,
			uintptr(srcW),
			uintptr(srcH),
			uintptr(unsafe.Pointer(&srcBuf[0])),
			uintptr(unsafe.Pointer(&bi)),
			dibRGBColors,
			srccopy,
		)
		if stretchMode == "crisp" && upscaling && targetDC == a.paintBuffer.memDC {
			a.paintBuffer.sharpenRect(drawX, drawY, drawW, drawH, 52, 6)
		}
	} else if !emptyRect(displayDirty) && useGlobalClippedStretch(stretchMode) {
		stretchDIBitsClipped(targetDC, displayDirty, drawX, drawY, drawW, drawH, srcW, srcH, srcBuf, &bi)
		if upscaling && targetDC == a.paintBuffer.memDC {
			switch stretchMode {
			case "ultra":
				a.paintBuffer.sharpenRect(int(displayDirty.Left), int(displayDirty.Top), rectWidth(displayDirty), rectHeight(displayDirty), 96, 3)
			case "sharp":
				a.paintBuffer.sharpenRect(int(displayDirty.Left), int(displayDirty.Top), rectWidth(displayDirty), rectHeight(displayDirty), 52, 6)
			}
		}
	} else if !emptyRect(displayDirty) {
		source := clientRectToSourceRect(displayDirty, drawX, drawY, drawW, drawH, srcW, srcH)
		if !source.Empty() {
			regionBuf, regionW, regionH := cropBGRARegion(srcBuf, srcW, srcH, source)
			if len(regionBuf) == 0 || regionW <= 0 || regionH <= 0 {
				return
			}
			regionBI := bitmapInfo{
				Header: bitmapInfoHeader{
					Size:        uint32(unsafe.Sizeof(bitmapInfoHeader{})),
					Width:       int32(regionW),
					Height:      -int32(regionH),
					Planes:      1,
					BitCount:    32,
					Compression: biRGB,
				},
			}
			procStretchDIBits.Call(
				targetDC,
				uintptr(displayDirty.Left),
				uintptr(displayDirty.Top),
				uintptr(rectWidth(displayDirty)),
				uintptr(rectHeight(displayDirty)),
				0,
				0,
				uintptr(regionW),
				uintptr(regionH),
				uintptr(unsafe.Pointer(&regionBuf[0])),
				uintptr(unsafe.Pointer(&regionBI)),
				dibRGBColors,
				srccopy,
			)
			if upscaling && targetDC == a.paintBuffer.memDC {
				switch stretchMode {
				case "ultra":
					a.paintBuffer.sharpenRect(int(displayDirty.Left), int(displayDirty.Top), rectWidth(displayDirty), rectHeight(displayDirty), 96, 3)
				case "sharp", "crisp":
					a.paintBuffer.sharpenRect(int(displayDirty.Left), int(displayDirty.Top), rectWidth(displayDirty), rectHeight(displayDirty), 52, 6)
				}
			}
		}
	}
	if a.isImmersive() {
		a.paintOverlay(targetDC, width)
	}
	if desktopUnavailable {
		a.paintDesktopUnavailable(targetDC, width, height, desktopMessage)
	}
	a.notePaintFrame()
}

func (a *controllerApp) paintDesktopUnavailable(hdc uintptr, clientW, clientH int, message string) {
	if hdc == 0 || clientW <= 0 || clientH <= 0 {
		return
	}
	title := "等待桌面恢复"
	detail := strings.TrimSpace(message)
	if detail == "" {
		detail = "被控端桌面暂不可用，恢复后会自动继续捕获"
	}
	panelW := minInt(maxInt(360, len([]rune(detail))*8+40), maxInt(220, clientW-48))
	panelH := 86
	left := maxInt(12, (clientW-panelW)/2)
	top := maxInt(12, (clientH-panelH)/2)
	panel := rect{Left: int32(left), Top: int32(top), Right: int32(left + panelW), Bottom: int32(top + panelH)}
	drawFilledRect(hdc, panel, 0x00111820)
	drawText(hdc, left+20, top+18, title, 0x00F0F6FC)
	drawText(hdc, left+20, top+48, trimLabel(detail, maxInt(20, (panelW-40)/8)), 0x00B8C4D0)
}

func (a *controllerApp) remotePoint(hwnd uintptr, clientX, clientY int) (int, int, bool) {
	a.mu.RLock()
	remoteW := a.remoteW
	remoteH := a.remoteH
	displayMode := a.displayMode
	a.mu.RUnlock()
	if remoteW <= 0 || remoteH <= 0 {
		return 0, 0, false
	}

	var rc rect
	procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&rc)))
	clientW := int(rc.Right - rc.Left)
	clientH := int(rc.Bottom - rc.Top)
	if clientW <= 0 || clientH <= 0 {
		return 0, 0, false
	}

	drawX, drawY, drawW, drawH, ok := controllerDisplayRect(clientW, clientH, remoteW, remoteH, displayMode)
	if !ok || drawW <= 0 || drawH <= 0 {
		return 0, 0, false
	}
	if clientX < drawX || clientY < drawY || clientX >= drawX+drawW || clientY >= drawY+drawH {
		return 0, 0, false
	}

	x := clampInt((clientX-drawX)*remoteW/drawW, 0, remoteW-1)
	y := clampInt((clientY-drawY)*remoteH/drawH, 0, remoteH-1)
	return x, y, true
}

func (a *controllerApp) handleMouseButton(hwnd uintptr, message uint32, clientX, clientY int) {
	button := "left"
	switch message {
	case wmRButtonDown, wmRButtonDblClk, wmRButtonUp:
		button = "right"
	case wmMButtonDown, wmMButtonUp:
		button = "middle"
	}
	eventType := "down"
	isUp := message == wmLButtonUp || message == wmRButtonUp || message == wmMButtonUp
	if isUp {
		eventType = "up"
	}

	x, y, ok := a.remotePoint(hwnd, clientX, clientY)
	if message == wmLButtonDown || message == wmLButtonDblClk {
		if ok {
			a.beginRemoteLeftDrag(hwnd, clientX, clientY, x, y)
		}
	} else if message == wmLButtonUp {
		defer a.endRemoteLeftDrag()
		if !ok && a.mouseLeftDown {
			x, y, ok = a.dragLastRemoteX, a.dragLastRemoteY, true
		}
	}

	if ok && a.inputEnabled() {
		a.sendInputAsync(controllerInput{Type: eventType, Button: button, X: x, Y: y})
	}
}

func (a *controllerApp) beginRemoteLeftDrag(hwnd uintptr, clientX, clientY, remoteX, remoteY int) {
	a.mouseLeftDown = true
	a.dragLastRemoteX = remoteX
	a.dragLastRemoteY = remoteY
	procSetCapture.Call(hwnd)
}

func (a *controllerApp) noteRemoteDragMove(remoteX, remoteY int) {
	if !a.mouseLeftDown {
		return
	}
	a.dragLastRemoteX = remoteX
	a.dragLastRemoteY = remoteY
}

func (a *controllerApp) endRemoteLeftDrag() {
	a.resetMouseDragState()
	procReleaseCapture.Call()
}

func (a *controllerApp) cancelRemoteLeftDrag(sendRemoteUp bool) {
	if a == nil || !a.mouseLeftDown {
		return
	}
	x, y := a.dragLastRemoteX, a.dragLastRemoteY
	a.resetMouseDragState()
	procReleaseCapture.Call()
	if sendRemoteUp {
		a.sendInputAsync(controllerInput{Type: "up", Button: "left", X: x, Y: y})
	}
}

func (a *controllerApp) resetMouseDragState() {
	a.mouseLeftDown = false
}

func (a *controllerApp) sendInputAsync(input controllerInput) {
	if a.deviceID == "" {
		return
	}
	input.DeviceID = a.deviceID
	a.mu.Lock()
	a.lastInputAt = time.Now()
	a.mu.Unlock()

	if a.enqueueWSInput(input) {
		return
	}
	a.postInputHTTP(input)
}

func (a *controllerApp) invalidate() {
	if a.hwnd != 0 {
		procInvalidateRect.Call(a.hwnd, 0, 0)
	}
}

func (a *controllerApp) invalidateRemoteFileDialogs() {
	if a == nil {
		return
	}
	a.mu.RLock()
	dialogs := make([]*remoteFileDialog, 0, len(a.fileDialogs))
	for _, dialog := range a.fileDialogs {
		if dialog != nil {
			dialogs = append(dialogs, dialog)
		}
	}
	a.mu.RUnlock()
	for _, dialog := range dialogs {
		dialog.invalidate()
	}
}

func (a *controllerApp) invalidateRemoteRectLocked(remote image.Rectangle) {
	if a == nil || a.hwnd == 0 || remote.Empty() {
		return
	}
	if a.remoteW <= 0 || a.remoteH <= 0 {
		if a != nil {
			a.invalidate()
		}
		return
	}
	var client rect
	procGetClientRect.Call(a.hwnd, uintptr(unsafe.Pointer(&client)))
	clientW := int(client.Right - client.Left)
	clientH := int(client.Bottom - client.Top)
	drawX, drawY, drawW, drawH, ok := controllerDisplayRect(clientW, clientH, a.remoteW, a.remoteH, a.displayMode)
	if !ok || drawW <= 0 || drawH <= 0 {
		a.invalidate()
		return
	}
	dirty := remoteRectToClientRect(remote, drawX, drawY, drawW, drawH, a.remoteW, a.remoteH, clientW, clientH)
	if dirty.Left >= dirty.Right || dirty.Top >= dirty.Bottom {
		return
	}
	procInvalidateRect.Call(a.hwnd, uintptr(unsafe.Pointer(&dirty)), 0)
}

func remoteRectToClientRect(remote image.Rectangle, drawX, drawY, drawW, drawH, remoteW, remoteH, clientW, clientH int) rect {
	if remoteW <= 0 || remoteH <= 0 || drawW <= 0 || drawH <= 0 {
		return rect{}
	}
	remote = remote.Intersect(image.Rect(0, 0, remoteW, remoteH))
	if remote.Empty() {
		return rect{}
	}
	left := drawX + remote.Min.X*drawW/remoteW
	top := drawY + remote.Min.Y*drawH/remoteH
	right := drawX + (remote.Max.X*drawW+remoteW-1)/remoteW
	bottom := drawY + (remote.Max.Y*drawH+remoteH-1)/remoteH

	// Scaling filters can touch neighboring pixels, so pad slightly to clear ghosts.
	pad := 3
	left = maxInt(0, left-pad)
	top = maxInt(0, top-pad)
	right = minInt(clientW, right+pad)
	bottom = minInt(clientH, bottom+pad)
	return rect{Left: int32(left), Top: int32(top), Right: int32(right), Bottom: int32(bottom)}
}

func tileUnion(tiles []tilePatch) image.Rectangle {
	union := image.Rectangle{}
	for _, tile := range tiles {
		if tile.W <= 0 || tile.H <= 0 {
			continue
		}
		r := image.Rect(tile.X, tile.Y, tile.X+tile.W, tile.Y+tile.H)
		if union.Empty() {
			union = r
		} else {
			union = union.Union(r)
		}
	}
	return union
}

func (a *controllerApp) notifyViewerStateSender() {
	if a == nil || a.streamStateNotify == nil {
		return
	}
	select {
	case a.streamStateNotify <- struct{}{}:
	default:
	}
}

func (a *controllerApp) setViewerPaused(paused bool) {
	if a == nil {
		return
	}
	a.mu.Lock()
	changed := a.viewerPaused != paused
	if changed {
		a.viewerPaused = paused
		a.viewerPauseDirty = true
	}
	a.mu.Unlock()
	if !changed {
		return
	}
	if paused {
		log.Printf("[controller][stream] minimized, frame flow paused")
	} else {
		log.Printf("[controller][stream] restored, frame flow resumed")
	}
	a.notifyViewerStateSender()
}

func (a *controllerApp) viewerPauseState() (bool, bool) {
	if a == nil {
		return false, false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.viewerPaused, a.viewerPauseDirty
}

func (a *controllerApp) markViewerPauseSynced(paused bool) {
	if a == nil {
		return
	}
	a.mu.Lock()
	if a.viewerPaused == paused {
		a.viewerPauseDirty = false
	}
	a.mu.Unlock()
}

func (a *controllerApp) statsHUDVisible() bool {
	if a == nil {
		return false
	}
	a.mu.RLock()
	visible := a.statsVisible
	a.mu.RUnlock()
	return visible
}

func (a *controllerApp) syncViewerState(send func(wsClientMessage) error, force bool) error {
	paused, dirty := a.viewerPauseState()
	if !force && !dirty {
		return nil
	}
	msg := a.streamPreferenceMessage("viewer_state")
	msg.Paused = boolPtr(paused)
	if err := send(msg); err != nil {
		return err
	}
	a.markViewerPauseSynced(paused)
	return nil
}

func (a *controllerApp) streamPreferenceMessage(msgType string) wsClientMessage {
	msg := wsClientMessage{Type: msgType}
	if a == nil {
		return msg
	}
	if mode := strings.TrimSpace(a.streamModePref); mode != "" {
		msg.Mode = mode
	}
	if a.streamFPSPref > 0 {
		msg.FPS = a.streamFPSPref
	}
	if a.streamQualityPref > 0 {
		msg.Quality = a.streamQualityPref
	}
	if a.streamFullMSPref > 0 {
		msg.FullMS = a.streamFullMSPref
	}
	if a.targetKBpsPref > 0 {
		msg.TargetKBps = a.targetKBpsPref
	}
	return msg
}

func (a *controllerApp) handleLocalHotkey(vk int, keyDown bool) bool {
	if !a.hotkeyWindowActive() {
		return false
	}
	switch vk {
	case vkF6:
		if keyDown && a.statsHUDVisible() {
			a.sendInputAsync(controllerInput{Type: "monitor_switch"})
		}
		return a.statsHUDVisible()
	case vkF7:
		if keyDown && a.statsHUDVisible() {
			a.toggleRemoteCommandDialog()
		}
		return a.statsHUDVisible()
	case vkF8:
		if keyDown && a.statsHUDVisible() {
			a.toggleRemoteFileDialog("files")
		}
		return a.statsHUDVisible()
	case vkF9:
		if keyDown {
			a.mu.Lock()
			if !a.statsToggleHeld {
				a.statsToggleHeld = true
				a.statsToggleID++
				toggleID := a.statsToggleID
				a.statsToggleFired = false
				a.mu.Unlock()
				go func(id int) {
					time.Sleep(3000 * time.Millisecond)
					a.mu.Lock()
					if a.statsToggleHeld && a.statsToggleID == id && !a.statsToggleFired {
						a.statsToggleFired = true
						a.statsVisible = !a.statsVisible
						if !a.statsVisible {
							a.overlayVisible = false
						}
						a.mu.Unlock()
						a.invalidate()
						a.updateTitle()
					} else {
						a.mu.Unlock()
					}
				}(toggleID)
			} else {
				a.mu.Unlock()
			}
		} else {
			// keyUp
			a.mu.Lock()
			if a.statsToggleFired {
				// 已经切换过了，不发送
				a.statsToggleFired = false
				a.statsToggleHeld = false
				a.mu.Unlock()
			} else if a.statsToggleHeld {
				a.statsToggleHeld = false
				a.mu.Unlock()
				if a.inputEnabled() {
					a.sendInputAsync(controllerInput{Type: "keydown", VK: int(vkF9)})
					a.sendInputAsync(controllerInput{Type: "keyup", VK: int(vkF9)})
				}
			} else {
				a.mu.Unlock()
			}
		}
		return true
	case vkF10:
		if keyDown && a.statsHUDVisible() {
			a.mu.Lock()
			held := a.transferCancelHeld
			if !held {
				a.transferCancelHeld = true
			}
			a.mu.Unlock()
			if !held {
				go a.cancelActiveTransfers()
			}
		} else if !keyDown {
			a.mu.Lock()
			a.transferCancelHeld = false
			a.mu.Unlock()
		}
		return a.statsHUDVisible()
	default:
		return false
	}
}

func (a *controllerApp) updateTitle() {
	if a.hwnd == 0 {
		return
	}
	a.mu.RLock()
	deviceID := a.deviceID
	deviceName := a.deviceName
	capture := a.keyboardCapture
	streamConnected := a.streamConnected
	streamLabel := a.streamLabel
	immersive := a.immersive
	rxFPS := a.rxFPS
	rxBPS := a.rxBPS
	decodeFPS := a.decodeFPS
	paintFPS := a.paintFPS
	dropped := a.droppedFrames
	lastCodec := a.lastFrameCodec
	lastQuality := a.lastFrameQuality
	a.mu.RUnlock()
	title := a.title
	if deviceName != "" {
		title = fmt.Sprintf("%s", deviceName)
	} else if deviceID != "" {
		title = "connected"
	}
	if streamConnected {
		if streamLabel == "" {
			streamLabel = "STREAM"
		}
		title += fmt.Sprintf(" [%s: ON]", streamLabel)
	} else {
		title += " [STREAM: OFF]"
	}
	if capture {
		title += " [Keyboard: HOOK]"
	} else {
		title += " [Keyboard: BYPASS]"
	}
	if immersive {
		title += " [Fullscreen]"
	}
	if streamConnected {
		if lastCodec == "" {
			lastCodec = "?"
		}
		title += fmt.Sprintf(" [RX %.1ffps %s/s | DEC %.1f | PAINT %.1f | Q %s | DROP %d | %s]", rxFPS, formatBytes(int64(rxBPS)), decodeFPS, paintFPS, formatQuality(lastQuality), dropped, strings.ToUpper(lastCodec))
	}
	a.mu.RLock()
	statsVisible := a.statsVisible
	a.mu.RUnlock()
	if statsVisible {
		title += " [Mode: Func]"
	} else {
		title += " [Mode: Key]"
	}
	procSetWindowTextW.Call(a.hwnd, uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr(title))))
}

func (a *controllerApp) setStreamConnected(connected bool, label string) {
	a.mu.Lock()
	previous := a.streamConnected
	prevLabel := a.streamLabel
	a.streamConnected = connected
	a.streamLabel = label
	a.mu.Unlock()
	if !connected {
		a.clearPendingWSInputs()
	}
	a.updateTitle()
	if connected && (!previous || prevLabel != label) {
		log.Printf("[controller][stream] connected via %s", label)
	}
	if !connected && previous {
		log.Printf("[controller][stream] disconnected")
	}
}

func (a *controllerApp) preferredCodec() string {
	return "jpeg"
}

func (a *controllerApp) currentDeviceID() string {
	if a == nil {
		return ""
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return strings.TrimSpace(a.deviceID)
}

func (a *controllerApp) isClipboardSyncEnabled() bool {
	if a == nil {
		return true
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if !a.clipboardSyncKnown {
		return true
	}
	return a.clipboardSync
}

func (a *controllerApp) setClipboardSyncLocal(enabled bool) {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.clipboardSync = enabled
	a.clipboardSyncKnown = true
	if !enabled {
		a.lastClipboard = ""
		a.clipboardVersion = 0
		a.clipboardSeq = 0
	}
	a.mu.Unlock()
}

func (a *controllerApp) setRemoteClipboardSync(enabled bool) error {
	if a == nil {
		return fmt.Errorf("controller is not ready")
	}
	deviceID := a.currentDeviceID()
	if deviceID == "" {
		return fmt.Errorf("device id is empty")
	}
	input := controllerInput{
		DeviceID:      deviceID,
		Type:          "clipboard_toggle",
		ClipboardSync: &enabled,
	}
	body, err := json.Marshal(input)
	if err != nil {
		return err
	}
	resp, err := a.http.Post(a.withAdminKey(a.serverURL+"/api/input"), "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("server returned %s", resp.Status)
	}
	a.setClipboardSyncLocal(enabled)
	return nil
}

func (a *controllerApp) withAdminKey(endpoint string) string {
	if a.adminKey == "" {
		return endpoint
	}
	separator := "?"
	if strings.Contains(endpoint, "?") {
		separator = "&"
	}
	return endpoint + separator + "key=" + url.QueryEscape(a.adminKey)
}

func (a *controllerApp) installKeyboardHook() error {
	instance, _, _ := procGetModuleHandleW.Call(0)
	handle, _, err := procSetWindowsHookExW.Call(
		whKeyboardLL,
		controllerKeyboardHookProc,
		instance,
		0,
	)
	if handle == 0 {
		return fmt.Errorf("SetWindowsHookExW failed: %v", err)
	}
	a.mu.Lock()
	a.keyboardHook = handle
	a.mu.Unlock()
	a.updateTitle()
	return nil
}

func (a *controllerApp) uninstallKeyboardHook() {
	a.mu.Lock()
	handle := a.keyboardHook
	a.keyboardHook = 0
	a.mu.Unlock()
	if handle != 0 {
		procUnhookWindowsHookEx.Call(handle)
	}
}

func (a *controllerApp) setFocusState(focused bool) {
	a.mu.Lock()
	a.focused = focused
	if !focused {
		a.overlayVisible = false
		a.keysDown = make(map[int]bool)
	}
	a.mu.Unlock()
	if !focused {
		a.cancelRemoteLeftDrag(true)
		a.sendInputAsync(controllerInput{Type: "reset_keys"})
	} else if a.hwnd != 0 {
		disableIME(a.hwnd)
	}
	a.updateTitle()
}

func (a *controllerApp) windowActive() bool {
	a.mu.RLock()
	hwnd := a.hwnd
	focused := a.focused
	a.mu.RUnlock()
	if !focused || hwnd == 0 {
		return false
	}
	foreground, _, _ := procGetForegroundWindow.Call()
	return foreground == hwnd
}

func (a *controllerApp) shouldCaptureKeyboard() bool {
	a.mu.RLock()
	enabled := a.keyboardCapture
	a.mu.RUnlock()
	if !enabled {
		return false
	}
	return a.windowActive()
}

func (a *controllerApp) inputEnabled() bool {
	a.mu.RLock()
	enabled := a.keyboardCapture
	a.mu.RUnlock()
	return enabled
}

func (a *controllerApp) noteKeyEvent(vk int, down bool, repeatHint bool) bool {
	if a == nil || vk <= 0 {
		return repeatHint
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.keysDown == nil {
		a.keysDown = make(map[int]bool)
	}
	if !down {
		delete(a.keysDown, vk)
		return false
	}
	repeat := repeatHint || a.keysDown[vk]
	a.keysDown[vk] = true
	return repeat
}

func (a *controllerApp) isImmersive() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.immersive
}

func (a *controllerApp) setImmersive(enabled bool) {
	if a.hwnd == 0 {
		return
	}

	a.mu.RLock()
	current := a.immersive
	a.mu.RUnlock()
	if current == enabled {
		return
	}

	if enabled {
		var currentRect rect
		procGetWindowRect.Call(a.hwnd, uintptr(unsafe.Pointer(&currentRect)))
		style := getWindowStyle(a.hwnd)
		monitor, _, _ := procMonitorFromWindow.Call(a.hwnd, monitorDefaultToNearest)
		if monitor == 0 {
			return
		}
		mi := monitorInfo{Size: uint32(unsafe.Sizeof(monitorInfo{}))}
		if ok, _, _ := procGetMonitorInfoW.Call(monitor, uintptr(unsafe.Pointer(&mi))); ok == 0 {
			return
		}

		a.mu.Lock()
		a.savedRect = currentRect
		a.savedStyle = style
		a.immersive = true
		a.overlayVisible = false
		a.mu.Unlock()

		setWindowStyle(a.hwnd, wsPopup|wsVisible)
		procSetWindowPos.Call(
			a.hwnd,
			0,
			uintptr(mi.Monitor.Left),
			uintptr(mi.Monitor.Top),
			uintptr(mi.Monitor.Right-mi.Monitor.Left),
			uintptr(mi.Monitor.Bottom-mi.Monitor.Top),
			swpNoZOrder|swpFrameChanged|swpShowWindow,
		)
	} else {
		a.mu.RLock()
		restoreRect := a.savedRect
		restoreStyle := a.savedStyle
		a.mu.RUnlock()
		if restoreStyle == 0 {
			restoreStyle = wsOverlappedWindow | wsVisible
		}
		setWindowStyle(a.hwnd, restoreStyle)
		procSetWindowPos.Call(
			a.hwnd,
			0,
			uintptr(restoreRect.Left),
			uintptr(restoreRect.Top),
			uintptr(restoreRect.Right-restoreRect.Left),
			uintptr(restoreRect.Bottom-restoreRect.Top),
			swpNoZOrder|swpFrameChanged|swpShowWindow,
		)
		a.mu.Lock()
		a.immersive = false
		a.overlayVisible = false
		a.mu.Unlock()
	}

	applyDarkTitleBar(a.hwnd)
	a.invalidate()
	a.updateTitle()
}

func (a *controllerApp) updateOverlayVisibility(clientY int) {
	a.mu.Lock()
	if !a.statsVisible {
		if a.overlayVisible {
			a.overlayVisible = false
			a.mu.Unlock()
			a.invalidate()
			return
		}
		a.mu.Unlock()
		return
	}
	visible := a.overlayVisible
	next := visible
	if visible {
		next = clientY >= 0 && clientY <= topOverlayH
	} else {
		next = clientY >= 0 && clientY <= topRevealY
	}
	if next == visible {
		a.mu.Unlock()
		return
	}
	a.overlayVisible = next
	a.mu.Unlock()
	a.invalidate()
}

func (a *controllerApp) overlayConsumes(clientX, clientY int) bool {
	a.mu.RLock()
	visible := a.overlayVisible
	statsVisible := a.statsVisible
	a.mu.RUnlock()
	return statsVisible && visible && clientY >= 0 && clientY <= topOverlayH && clientX >= 0
}

func (a *controllerApp) handleOverlayClick(hwnd uintptr, clientX, clientY int) bool {
	if !a.overlayConsumes(clientX, clientY) {
		return false
	}
	restoreRect, closeRect := a.overlayButtonRects(hwnd)
	switch {
	case pointInRect(clientX, clientY, closeRect):
		procDestroyWindow.Call(hwnd)
	case pointInRect(clientX, clientY, restoreRect):
		if a.isImmersive() {
			a.setImmersive(false)
		} else {
			a.setImmersive(true)
		}
	}
	return true
}

func (a *controllerApp) handleSetCursor(hwnd uintptr, hitTest int) bool {
	if hitTest != htClient {
		return false
	}
	var pt point
	if ret, _, _ := procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt))); ret == 0 {
		return false
	}
	procScreenToClient.Call(hwnd, uintptr(unsafe.Pointer(&pt)))
	clientX := int(pt.X)
	clientY := int(pt.Y)
	if a.overlayConsumes(clientX, clientY) {
		procSetCursor.Call(a.arrowCursor)
		return true
	}
	a.mu.RLock()
	remoteCursorOn := a.remoteCursorOn
	remoteCursorKind := a.remoteCursorKind
	a.mu.RUnlock()
	if remoteCursorOn {
		if cursor := loadSystemCursorHandle(normalizeCursorKind(remoteCursorKind)); cursor != 0 {
			procSetCursor.Call(cursor)
			return true
		}
	}
	if a.arrowCursor != 0 {
		procSetCursor.Call(a.arrowCursor)
		return true
	}
	return false
}

func (a *controllerApp) runFrameStream() error {
	endpoint := a.wsEndpoint()
	dialer := websocket.Dialer{
		Proxy:             http.ProxyFromEnvironment,
		HandshakeTimeout:  8 * time.Second,
		EnableCompression: false,
		NetDialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			conn, err := (&net.Dialer{
				Timeout:   5 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext(ctx, network, address)
			if err != nil {
				return nil, err
			}
			configureControllerTCP(conn)
			return conn, nil
		},
	}
	conn, resp, err := dialer.Dial(endpoint, nil)
	if err != nil {
		if resp != nil {
			raw, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return fmt.Errorf("websocket dial failed: %s", strings.TrimSpace(string(raw)))
		}
		return err
	}
	defer conn.Close()
	configureControllerTCP(conn.UnderlyingConn())
	conn.EnableWriteCompression(false)

	a.setStreamConnected(true, "WS")
	hello := a.streamPreferenceMessage("hello")
	hello.Role = "controller"
	hello.Codec = a.preferredCodec()
	if err := a.sendWSControl(conn, hello); err != nil {
		return err
	}
	if err := a.sendWSControl(conn, wsClientMessage{Type: "input", Input: &controllerInput{Type: "reset_keys"}}); err != nil {
		return err
	}
	if err := a.syncViewerState(func(msg wsClientMessage) error {
		return a.sendWSControl(conn, msg)
	}, true); err != nil {
		return err
	}

	frames := make(chan queuedFramePacket, 1)
	errCh := make(chan error, 2)
	stopInputs := make(chan struct{})
	stopDecode := make(chan struct{})
	defer close(stopInputs)
	defer close(stopDecode)
	go func() {
		for {
			messageType, data, err := conn.ReadMessage()
			if err != nil {
				errCh <- err
				return
			}
			if messageType != websocket.BinaryMessage {
				continue
			}
			if enqueueFramePacket(frames, data) {
				a.noteFrameDrop()
			}
		}
	}()
	go func() {
		errCh <- a.runStreamInputPump(stopInputs, func(msg wsClientMessage) error {
			return a.sendWSControl(conn, msg)
		})
	}()
	go a.runDecodeWorker(stopDecode, frames, func(msg wsClientMessage) error {
		return a.sendWSControl(conn, msg)
	}, errCh)

	pingTicker := time.NewTicker(a.pingInterval)
	defer pingTicker.Stop()

	for {
		select {
		case <-a.streamStateNotify:
			if err := a.syncViewerState(func(msg wsClientMessage) error {
				return a.sendWSControl(conn, msg)
			}, false); err != nil {
				return err
			}
		case <-pingTicker.C:
			if err := a.sendWSControl(conn, wsClientMessage{Type: "viewer_ping"}); err != nil {
				return err
			}
			if err := a.syncViewerState(func(msg wsClientMessage) error {
				return a.sendWSControl(conn, msg)
			}, false); err != nil {
				return err
			}
		case err := <-errCh:
			return err
		}
	}
}

func (a *controllerApp) runQUICStream() error {
	endpoint := a.quicEndpoint()
	conn, err := quic.DialAddr(context.Background(), endpoint, &tls.Config{
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
	defer conn.CloseWithError(0, "")

	state := conn.ConnectionState()
	if !state.SupportsDatagrams.Remote {
		return fmt.Errorf("quic datagrams are not supported")
	}

	stream, err := conn.OpenStreamSync(context.Background())
	if err != nil {
		return err
	}
	defer stream.Close()

	sendControl := func(msg wsClientMessage) error {
		return a.sendQUICControl(stream, msg)
	}

	hello := a.streamPreferenceMessage("hello")
	hello.DeviceID = a.deviceID
	hello.Key = a.adminKey
	hello.Role = "controller"
	hello.Codec = a.preferredCodec()
	if err := sendControl(hello); err != nil {
		return err
	}

	a.setStreamConnected(true, "QUIC")
	if err := sendControl(wsClientMessage{Type: "input", Input: &controllerInput{Type: "reset_keys"}}); err != nil {
		return err
	}
	if err := a.syncViewerState(sendControl, true); err != nil {
		return err
	}

	frames := make(chan queuedFramePacket, 1)
	errCh := make(chan error, 2)
	stopInputs := make(chan struct{})
	stopDecode := make(chan struct{})
	defer close(stopInputs)
	defer close(stopDecode)

	go func() {
		assembler := quicFrameAssembler{}
		for {
			data, err := conn.ReceiveDatagram(context.Background())
			if err != nil {
				errCh <- err
				return
			}
			packet, ok, err := assembler.Push(data)
			if err != nil {
				continue
			}
			if ok {
				if enqueueFramePacket(frames, packet) {
					a.noteFrameDrop()
				}
			}
		}
	}()

	go func() {
		errCh <- a.runStreamInputPump(stopInputs, sendControl)
	}()
	go a.runDecodeWorker(stopDecode, frames, sendControl, errCh)

	pingTicker := time.NewTicker(a.pingInterval)
	defer pingTicker.Stop()

	for {
		select {
		case <-a.streamStateNotify:
			if err := a.syncViewerState(sendControl, false); err != nil {
				return err
			}
		case <-pingTicker.C:
			if err := sendControl(wsClientMessage{Type: "viewer_ping"}); err != nil {
				return err
			}
			if err := a.syncViewerState(sendControl, false); err != nil {
				return err
			}
		case err := <-errCh:
			return err
		}
	}
}

func (a *controllerApp) handleFramePacket(data []byte, sendControl func(wsClientMessage) error) error {
	header, payload, err := decodeWSFramePacket(data)
	if err != nil {
		return err
	}
	if header.Type == "cursor" {
		a.applyCursorFromHeader(header)
		return nil
	}
	if header.Type == "desktop_status" {
		a.applyDesktopStatus(header)
		a.fitWindowToRemoteAspectOnce()
		return nil
	}
	if header.Type != "frame" {
		return fmt.Errorf("unexpected frame message type: %s", header.Type)
	}
	codec := normalizeFrameCodec(header.Codec)

	a.mu.RLock()
	prevFrameID := a.displayFrameID
	currentImage := a.image
	a.mu.RUnlock()

	state := controllerState{
		DeviceID:         header.DeviceID,
		Name:             header.Name,
		Alias:            header.Alias,
		OriginX:          header.OriginX,
		OriginY:          header.OriginY,
		CursorX:          header.CursorX,
		CursorY:          header.CursorY,
		CursorVisible:    header.CursorOn,
		CursorKind:       header.CursorKind,
		CursorImage:      header.CursorImage,
		CursorImageClear: header.CursorClear,
		CursorImageW:     header.CursorImgW,
		CursorImageH:     header.CursorImgH,
		CursorHotX:       header.CursorHotX,
		CursorHotY:       header.CursorHotY,
		CursorVersion:    header.CursorVer,
		Width:            header.Width,
		Height:           header.Height,
		FrameID:          header.FrameID,
		UpdateKind:       header.UpdateKind,
		HasPatch:         header.UpdateKind == "patch",
		PatchFrameID:     header.FrameID,
		PatchBaseFrameID: header.BaseFrameID,
		PatchX:           header.PatchX,
		PatchY:           header.PatchY,
		PatchW:           header.PatchW,
		PatchH:           header.PatchH,
		HasTiles:         header.UpdateKind == "tiles",
		TilesFrameID:     header.FrameID,
		TilesBaseFrameID: header.BaseFrameID,
		Tiles:            header.Tiles,
		Online:           true,
		HasFrame:         true,
	}

	switch header.UpdateKind {
	case "patch":
		if currentImage == nil || prevFrameID != header.BaseFrameID {
			return fmt.Errorf("patch base mismatch: have %d want %d", prevFrameID, header.BaseFrameID)
		}
		if codec == "bgra" {
			if err := validateBGRAPayload(payload, header.PatchW, header.PatchH); err != nil {
				return err
			}
			a.mu.Lock()
			a.deviceName = displayDeviceName(header.Alias, header.Name)
			a.updateRemoteSizeLocked(header.Width, header.Height)
			a.originX = header.OriginX
			a.originY = header.OriginY
			a.remoteCursorX = header.CursorX
			a.remoteCursorY = header.CursorY
			a.remoteCursorOn = header.CursorOn
			a.remoteCursorKind = normalizeCursorKind(header.CursorKind)
			a.mu.Unlock()
			a.applyBGRAPatch(payload, state)
			a.fitWindowToRemoteAspectOnce()
			break
		}
		patchImage, err := decodeIncrementalFrame(codec, payload, header.PatchW, header.PatchH)
		if err != nil {
			return err
		}
		a.mu.Lock()
		a.deviceName = displayDeviceName(header.Alias, header.Name)
		a.updateRemoteSizeLocked(header.Width, header.Height)
		a.originX = header.OriginX
		a.originY = header.OriginY
		a.remoteCursorX = header.CursorX
		a.remoteCursorY = header.CursorY
		a.remoteCursorOn = header.CursorOn
		a.remoteCursorKind = normalizeCursorKind(header.CursorKind)
		a.mu.Unlock()
		a.applyPatch(patchImage, state)
		a.fitWindowToRemoteAspectOnce()
	case "tiles":
		if currentImage == nil || prevFrameID != header.BaseFrameID {
			return fmt.Errorf("tiles base mismatch: have %d want %d", prevFrameID, header.BaseFrameID)
		}
		atlasW, atlasH := tileAtlasSize(header.Tiles)
		if atlasW <= 0 || atlasH <= 0 {
			return fmt.Errorf("tile atlas metadata is invalid")
		}
		if codec == "bgra" {
			if err := validateBGRAPayload(payload, atlasW, atlasH); err != nil {
				return err
			}
			a.mu.Lock()
			a.deviceName = displayDeviceName(header.Alias, header.Name)
			a.updateRemoteSizeLocked(header.Width, header.Height)
			a.originX = header.OriginX
			a.originY = header.OriginY
			a.remoteCursorX = header.CursorX
			a.remoteCursorY = header.CursorY
			a.remoteCursorOn = header.CursorOn
			a.remoteCursorKind = normalizeCursorKind(header.CursorKind)
			a.mu.Unlock()
			a.applyBGRATiles(payload, atlasW, atlasH, state)
			a.fitWindowToRemoteAspectOnce()
			break
		}
		atlasImage, err := decodeIncrementalFrame(codec, payload, atlasW, atlasH)
		if err != nil {
			return err
		}
		a.mu.Lock()
		a.deviceName = displayDeviceName(header.Alias, header.Name)
		a.updateRemoteSizeLocked(header.Width, header.Height)
		a.originX = header.OriginX
		a.originY = header.OriginY
		a.remoteCursorX = header.CursorX
		a.remoteCursorY = header.CursorY
		a.remoteCursorOn = header.CursorOn
		a.remoteCursorKind = normalizeCursorKind(header.CursorKind)
		a.mu.Unlock()
		a.applyTiles(atlasImage, state)
		a.fitWindowToRemoteAspectOnce()
	default:
		fullImage, err := a.decodeFrameByCodec(codec, payload)
		if err != nil {
			return err
		}
		a.mu.Lock()
		a.image = fullImage
		a.imageBGRA = rgbaToBGRA(fullImage)
		a.clearScaledCacheLocked()
		a.displayFrameID = header.FrameID
		a.deviceName = displayDeviceName(header.Alias, header.Name)
		a.updateRemoteSizeLocked(header.Width, header.Height)
		a.originX = header.OriginX
		a.originY = header.OriginY
		a.remoteCursorX = header.CursorX
		a.remoteCursorY = header.CursorY
		a.remoteCursorOn = header.CursorOn
		a.remoteCursorKind = normalizeCursorKind(header.CursorKind)
		a.mu.Unlock()
		a.invalidate()
	}

	a.setDesktopAvailable()
	a.fitWindowToRemoteAspectOnce()
	a.applyCursorFromHeader(header)
	a.noteFrameRX(header.UpdateKind+"/"+codec, len(data), header.Quality)
	return sendControl(wsClientMessage{Type: "ack", FrameID: header.FrameID})
}

func (a *controllerApp) runDecodeWorker(stop <-chan struct{}, frames <-chan queuedFramePacket, sendControl func(wsClientMessage) error, errCh chan<- error) {
	for {
		select {
		case <-stop:
			return
		case packet := <-frames:
			data := packet.data
			if len(data) == 0 {
				continue
			}
			if err := a.handleFramePacket(data, sendControl); err != nil {
				log.Printf("frame decode failed: %v", err)
				if sendErr := sendControl(wsClientMessage{Type: "resync"}); sendErr != nil {
					select {
					case errCh <- sendErr:
					default:
					}
					return
				}
			} else {
				a.noteDecodeFrame()
			}
		}
	}
}

func (a *controllerApp) noteFrameRX(updateKind string, size int, quality int) {
	if size <= 0 {
		return
	}
	now := time.Now()
	a.mu.Lock()
	if a.rxLogAt.IsZero() {
		a.rxLogAt = now
	}
	if a.rxStatsAt.IsZero() {
		a.rxStatsAt = now
	}
	a.rxLogBytes += int64(size)
	a.rxLogFrames++
	a.rxLogLast = updateKind
	a.lastFrameKind = updateKind
	a.lastFrameBytes = size
	if quality > 0 {
		a.lastFrameQuality = quality
	}
	if parts := strings.Split(updateKind, "/"); len(parts) > 1 {
		a.lastFrameCodec = parts[len(parts)-1]
	}
	a.rxStatsFrames++
	elapsedStats := now.Sub(a.rxStatsAt)
	if elapsedStats >= time.Second {
		a.rxFPS = float64(a.rxStatsFrames) / elapsedStats.Seconds()
		a.rxStatsAt = now
		a.rxStatsFrames = 0
	}
	streamLabel := a.streamLabel
	elapsedLog := now.Sub(a.rxLogAt)
	shouldLog := elapsedLog >= 2*time.Second
	bytes := a.rxLogBytes
	frames := a.rxLogFrames
	last := a.rxLogLast
	rate := a.rxBPS
	decodeFPS := a.decodeFPS
	paintFPS := a.paintFPS
	dropped := a.droppedFrames
	lastQuality := a.lastFrameQuality
	if shouldLog {
		rate = float64(bytes) / maxFloat(elapsedLog.Seconds(), 0.001)
		a.rxBPS = rate
		a.rxLogAt = now
		a.rxLogBytes = 0
		a.rxLogFrames = 0
		a.rxLogLast = ""
	}
	a.mu.Unlock()

	a.maybeRefreshStatsUI(now)

	if shouldLog {
		if streamLabel == "" {
			streamLabel = "STREAM"
		}
		log.Printf("[controller][rx/%s] frames=%d bytes=%s rate=%s/s last=%s q=%s dec=%.1f paint=%.1f drop=%d", strings.ToLower(streamLabel), frames, formatBytes(bytes), formatBytes(int64(rate)), last, formatQuality(lastQuality), decodeFPS, paintFPS, dropped)
	}
}

func (a *controllerApp) noteDecodeFrame() {
	now := time.Now()
	a.mu.Lock()
	if a.decodeStatsAt.IsZero() {
		a.decodeStatsAt = now
	}
	a.decodeStatsFrames++
	elapsed := now.Sub(a.decodeStatsAt)
	if elapsed >= time.Second {
		a.decodeFPS = float64(a.decodeStatsFrames) / elapsed.Seconds()
		a.decodeStatsAt = now
		a.decodeStatsFrames = 0
	}
	a.mu.Unlock()
	a.maybeRefreshStatsUI(now)
}

func (a *controllerApp) notePaintFrame() {
	now := time.Now()
	a.mu.Lock()
	if a.paintStatsAt.IsZero() {
		a.paintStatsAt = now
	}
	a.paintStatsFrames++
	elapsed := now.Sub(a.paintStatsAt)
	if elapsed >= time.Second {
		a.paintFPS = float64(a.paintStatsFrames) / elapsed.Seconds()
		a.paintStatsAt = now
		a.paintStatsFrames = 0
	}
	a.mu.Unlock()
}

func (a *controllerApp) noteFrameDrop() {
	now := time.Now()
	a.mu.Lock()
	a.droppedFrames++
	a.mu.Unlock()
	a.maybeRefreshStatsUI(now)
}

func (a *controllerApp) maybeRefreshStatsUI(now time.Time) {
	if a == nil || a.hwnd == 0 {
		return
	}
	a.mu.Lock()
	if !a.statsUIAt.IsZero() && now.Sub(a.statsUIAt) < 500*time.Millisecond {
		a.mu.Unlock()
		return
	}
	a.statsUIAt = now
	a.mu.Unlock()
	a.updateTitle()
	a.invalidateStatsHUD()
}

func (a *controllerApp) invalidateStatsHUD() {
	if a == nil || a.hwnd == 0 {
		return
	}
	a.mu.RLock()
	statsVisible := a.statsVisible
	remoteW := a.remoteW
	remoteH := a.remoteH
	displayMode := a.displayMode
	a.mu.RUnlock()
	if !statsVisible {
		return
	}
	var client rect
	procGetClientRect.Call(a.hwnd, uintptr(unsafe.Pointer(&client)))
	clientW := int(client.Right - client.Left)
	clientH := int(client.Bottom - client.Top)
	drawX, drawY, drawW, drawH, ok := controllerDisplayRect(clientW, clientH, remoteW, remoteH, displayMode)
	if !ok {
		drawX, drawY, drawW, drawH = 0, 0, 0, 0
	}
	panel := statsHUDRect(clientW, clientH, drawX, drawY, drawW, drawH)
	if emptyRect(panel) {
		return
	}
	procInvalidateRect.Call(a.hwnd, uintptr(unsafe.Pointer(&panel)), 0)
}

func decodeWSFramePacket(data []byte) (wsFrameEnvelope, []byte, error) {
	var header wsFrameEnvelope
	if len(data) < 4 {
		return header, nil, fmt.Errorf("frame packet is too short")
	}
	headerLen := int(binary.BigEndian.Uint32(data[:4]))
	if headerLen <= 0 || 4+headerLen > len(data) {
		return header, nil, fmt.Errorf("frame header size is invalid")
	}
	if err := json.Unmarshal(data[4:4+headerLen], &header); err != nil {
		return header, nil, err
	}
	payload := append([]byte(nil), data[4+headerLen:]...)
	if header.PayloadSize > 0 && len(payload) != header.PayloadSize {
		return header, nil, fmt.Errorf("frame payload size mismatch")
	}
	return header, payload, nil
}

func decodeJPEGFrame(data []byte) (*image.RGBA, error) {
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	bounds := img.Bounds()
	rgba := image.NewRGBA(bounds)
	draw.Draw(rgba, bounds, img, bounds.Min, draw.Src)
	return rgba, nil
}

func decodeIncrementalFrame(codec string, data []byte, width, height int) (*image.RGBA, error) {
	switch normalizeFrameCodec(codec) {
	case "bgra":
		return decodeBGRAFrame(data, width, height)
	default:
		return decodeJPEGFrame(data)
	}
}

func decodeBGRAFrame(data []byte, width, height int) (*image.RGBA, error) {
	if err := validateBGRAPayload(data, width, height); err != nil {
		return nil, err
	}
	rgba := image.NewRGBA(image.Rect(0, 0, width, height))
	for srcOffset, dstOffset := 0, 0; srcOffset+3 < len(data) && dstOffset+3 < len(rgba.Pix); srcOffset, dstOffset = srcOffset+4, dstOffset+4 {
		rgba.Pix[dstOffset] = data[srcOffset+2]
		rgba.Pix[dstOffset+1] = data[srcOffset+1]
		rgba.Pix[dstOffset+2] = data[srcOffset]
		rgba.Pix[dstOffset+3] = 255
	}
	return rgba, nil
}

func validateBGRAPayload(data []byte, width, height int) error {
	if width <= 0 || height <= 0 {
		return fmt.Errorf("invalid bgra dimensions")
	}
	expected := width * height * 4
	if len(data) != expected {
		return fmt.Errorf("bgra payload size mismatch: got %d want %d", len(data), expected)
	}
	return nil
}

func tileAtlasSize(tiles []tilePatch) (int, int) {
	maxX, maxY := 0, 0
	for _, tile := range tiles {
		if tile.W > 0 && tile.AtlasX+tile.W > maxX {
			maxX = tile.AtlasX + tile.W
		}
		if tile.H > 0 && tile.AtlasY+tile.H > maxY {
			maxY = tile.AtlasY + tile.H
		}
	}
	return maxX, maxY
}

func (a *controllerApp) decodeFrameByCodec(codec string, payload []byte) (*image.RGBA, error) {
	switch normalizeFrameCodec(codec) {
	case "jpeg":
		return decodeJPEGFrame(payload)
	case "h264":
		return a.tryDecodeH264MF(payload)
	default:
		return nil, fmt.Errorf("unsupported frame codec: %s", codec)
	}
}

func (a *controllerApp) tryDecodeH264MF(payload []byte) (*image.RGBA, error) {
	if a == nil || a.mfSession == nil {
		return nil, fmt.Errorf("h264-mf helper session is unavailable")
	}
	resp, raw, err := a.mfSession.exchange(mfSessionRequest{
		Type:        "decode",
		Codec:       "h264-mf",
		PayloadSize: len(payload),
	}, payload)
	if err != nil {
		return nil, err
	}
	if !resp.OK {
		if strings.TrimSpace(resp.Error) == "" {
			return nil, fmt.Errorf("h264-mf decode failed")
		}
		return nil, fmt.Errorf(resp.Error)
	}
	if resp.Width <= 0 || resp.Height <= 0 {
		return nil, fmt.Errorf("h264-mf helper returned invalid frame size")
	}
	stride := resp.Stride
	if stride <= 0 {
		stride = resp.Width * 4
	}
	expected := stride * resp.Height
	if len(raw) < expected {
		return nil, fmt.Errorf("h264-mf helper returned truncated rgba payload")
	}
	rgba := image.NewRGBA(image.Rect(0, 0, resp.Width, resp.Height))
	for y := 0; y < resp.Height; y++ {
		copy(rgba.Pix[y*rgba.Stride:y*rgba.Stride+resp.Width*4], raw[y*stride:y*stride+resp.Width*4])
	}
	return rgba, nil
}

func normalizeFrameCodec(value string) string {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case "jpeg", "mjpeg":
		return "jpeg"
	case "bgra", "raw-bgra", "raw":
		return "bgra"
	case "h264", "h264-mf":
		return "h264"
	default:
		return "jpeg"
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

func (a *controllerApp) ensureMFSession(role string) {
	if a == nil || a.mfSession != nil || strings.TrimSpace(a.mfHelperPath) == "" {
		return
	}
	session, hello, err := startMFHelperSession(a.mfHelperPath, role)
	if err != nil {
		log.Printf("[controller][mf] session start failed: %v", err)
		return
	}
	a.mfSession = session
	log.Printf("[controller][mf] session ready version=%s caps=%s encode=%s decode=%s", strings.TrimSpace(hello.Version), strings.Join(hello.Capabilities, ","), strings.Join(hello.Encode, ","), strings.Join(hello.Decode, ","))
}

func (a *controllerApp) pingMFSession(role string) error {
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

func (a *controllerApp) sendWSControl(conn *websocket.Conn, msg wsClientMessage) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	a.wsWriteMu.Lock()
	defer a.wsWriteMu.Unlock()
	if err := conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	return conn.WriteMessage(websocket.TextMessage, body)
}

func (a *controllerApp) sendQUICControl(stream *quic.Stream, msg wsClientMessage) error {
	a.wsWriteMu.Lock()
	defer a.wsWriteMu.Unlock()

	if err := stream.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	defer stream.SetWriteDeadline(time.Time{})
	return json.NewEncoder(stream).Encode(msg)
}

func (a *controllerApp) enqueueWSInput(input controllerInput) bool {
	a.mu.RLock()
	connected := a.streamConnected
	a.mu.RUnlock()
	if !connected {
		return false
	}

	inputCopy := input
	a.wsInputMu.Lock()
	if inputCopy.Type == "move" {
		a.wsMovePending = &inputCopy
	} else {
		a.wsInputQueue = append(a.wsInputQueue, inputCopy)
		if len(a.wsInputQueue) > 256 {
			a.wsInputQueue = append([]controllerInput(nil), a.wsInputQueue[len(a.wsInputQueue)-128:]...)
		}
	}
	a.wsInputMu.Unlock()
	a.notifyWSInputSender()
	return true
}

func (a *controllerApp) notifyWSInputSender() {
	select {
	case a.wsInputNotify <- struct{}{}:
	default:
	}
}

func (a *controllerApp) nextPendingWSInput() (controllerInput, bool) {
	a.wsInputMu.Lock()
	defer a.wsInputMu.Unlock()

	if len(a.wsInputQueue) > 0 {
		input := a.wsInputQueue[0]
		copy(a.wsInputQueue, a.wsInputQueue[1:])
		a.wsInputQueue = a.wsInputQueue[:len(a.wsInputQueue)-1]
		return input, true
	}
	if a.wsMovePending != nil {
		input := *a.wsMovePending
		a.wsMovePending = nil
		return input, true
	}
	return controllerInput{}, false
}

func (a *controllerApp) clearPendingWSInputs() {
	a.wsInputMu.Lock()
	a.wsInputQueue = nil
	a.wsMovePending = nil
	a.wsInputMu.Unlock()
}

func (a *controllerApp) runStreamInputPump(stop <-chan struct{}, send func(wsClientMessage) error) error {
	for {
		select {
		case <-stop:
			return nil
		case <-a.wsInputNotify:
		}

		for {
			input, ok := a.nextPendingWSInput()
			if !ok {
				break
			}
			if err := send(wsClientMessage{Type: "input", Input: &input}); err != nil {
				return err
			}
		}
	}
}

func (a *controllerApp) postInputHTTP(input controllerInput) {
	go func() {
		body, err := json.Marshal(input)
		if err != nil {
			return
		}
		resp, err := a.http.Post(a.withAdminKey(a.serverURL+"/api/input"), "application/json", bytes.NewReader(body))
		if err != nil {
			return
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()
}

func (a *controllerApp) postClipboardInputHTTP(deviceID string, state clipboardState) error {
	if !a.isClipboardSyncEnabled() {
		return nil
	}
	state = normalizeClipboardState(state)
	if state.Kind != "text" && state.Kind != "image" {
		return nil
	}
	input := controllerInput{
		DeviceID:  strings.TrimSpace(deviceID),
		Type:      "clipboard_set",
		Text:      state.Text,
		Clipboard: &state,
	}
	if input.DeviceID == "" {
		return fmt.Errorf("device id is empty")
	}
	body, err := json.Marshal(input)
	if err != nil {
		return err
	}
	resp, err := a.http.Post(a.withAdminKey(a.serverURL+"/api/input"), "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("server returned %s", resp.Status)
	}
	return nil
}

func (a *controllerApp) clipboardLoop() {
	if a == nil {
		return
	}
	if clipboard, err := readControllerClipboardState(); err == nil {
		a.mu.Lock()
		a.lastClipboard = clipboardSignature(clipboard)
		a.clipboardSeq = clipboardSequenceNumber()
		a.mu.Unlock()
	}
	ticker := time.NewTicker(a.clipboardTick)
	defer ticker.Stop()
	for {
		select {
		case <-a.clipboardChanged:
			a.syncClipboardTick()
		case <-ticker.C:
			a.syncClipboardTick()
		}
	}
}

func (a *controllerApp) syncClipboardTick() {
	if a == nil {
		return
	}
	a.mu.RLock()
	deviceID := strings.TrimSpace(a.deviceID)
	paused := a.viewerPaused
	clipboardSync := a.clipboardSync
	lastDeviceID := a.clipboardDeviceID
	lastClipboard := a.lastClipboard
	lastVersion := a.clipboardVersion
	lastSeq := a.clipboardSeq
	a.mu.RUnlock()
	if deviceID == "" || paused || !clipboardSync {
		return
	}
	if deviceID != lastDeviceID {
		a.mu.Lock()
		a.clipboardDeviceID = deviceID
		a.clipboardVersion = 0
		a.mu.Unlock()
		lastVersion = 0
	}

	seq := clipboardSequenceNumber()
	if seq == 0 || seq != lastSeq {
		localClipboard, err := readControllerClipboardState()
		if err == nil {
			if localClipboard.Kind != "text" && localClipboard.Kind != "image" {
				a.mu.Lock()
				a.clipboardSeq = seq
				a.mu.Unlock()
				return
			}
			localSignature := clipboardSignature(localClipboard)
			if localSignature != lastClipboard {
				a.noteClipboardHUD("send", localClipboard)
				if err := a.postClipboardInputHTTP(deviceID, localClipboard); err != nil {
					log.Printf("[controller][clipboard] sync failed: %v", err)
					return
				}
				a.mu.Lock()
				a.lastClipboard = localSignature
				a.clipboardSeq = seq
				a.mu.Unlock()
				return
			}
			a.mu.Lock()
			a.clipboardSeq = seq
			a.mu.Unlock()
		}
	}

	remote, err := a.fetchRemoteClipboard(deviceID, lastVersion)
	if err != nil {
		return
	}
	if remote.Sync != nil {
		a.setClipboardSyncLocal(*remote.Sync)
		if !*remote.Sync {
			return
		}
	}
	remoteSignature := clipboardSignature(remote)
	if remote.Version <= lastVersion && remoteSignature == lastClipboard {
		a.mu.Lock()
		if remote.Version > a.clipboardVersion {
			a.clipboardVersion = remote.Version
		}
		a.mu.Unlock()
		return
	}
	if remote.Kind != "text" && remote.Kind != "image" {
		a.mu.Lock()
		a.lastClipboard = remoteSignature
		a.clipboardVersion = remote.Version
		a.mu.Unlock()
		return
	}
	if len(remote.Text) == 0 && len(remote.Image) == 0 {
		a.mu.Lock()
		a.lastClipboard = remoteSignature
		a.clipboardVersion = remote.Version
		a.mu.Unlock()
		return
	}
	if err := writeControllerClipboardState(remote); err != nil {
		return
	}
	// 读取回本地实际内容，避免Windows重编码导致签名循环
	localClipboard, err := readControllerClipboardState()
	if err != nil {
		return
	}
	localSignature := clipboardSignature(localClipboard)
	a.mu.Lock()
	a.lastClipboard = localSignature
	a.clipboardVersion = remote.Version
	a.clipboardSeq = clipboardSequenceNumber()
	a.mu.Unlock()
}

func (a *controllerApp) noteClipboardHUD(direction string, state clipboardState) {
	if a == nil {
		return
	}
	state = normalizeClipboardState(state)
	if state.Kind != "image" || len(state.Image) == 0 {
		return
	}
	label := "Clipboard image"
	if state.ImageW > 0 && state.ImageH > 0 {
		label = fmt.Sprintf("Clipboard image %dx%d", state.ImageW, state.ImageH)
	}
	now := time.Now()
	id := "clipboard-" + strings.TrimSpace(strings.ToLower(direction))
	a.mu.Lock()
	a.transfers[id] = &controllerTransferState{
		ID:            id,
		Name:          label,
		Direction:     "clipboard_" + direction,
		Size:          int64(len(state.Image)),
		UploadedBytes: int64(len(state.Image)),
		Status:        direction,
		StartedAt:     now,
		UpdatedAt:     now,
	}
	a.mu.Unlock()
	a.invalidate()
	a.invalidateRemoteFileDialogs()
}

func (a *controllerApp) prepareClipboardForSend(deviceID string, state clipboardState) (clipboardState, error) {
	state = normalizeClipboardState(state)
	if state.Kind != "files" || len(state.Files) > 0 {
		return state, nil
	}
	refs, err := a.uploadClipboardFiles(deviceID, state.Paths)
	if err != nil {
		return state, err
	}
	state.Files = refs
	state.Paths = nil
	return state, nil
}

func (a *controllerApp) uploadClipboardFiles(deviceID string, paths []string) ([]transferFileRef, error) {
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
		key := strings.ToLower(cleanPath)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		info, err := os.Stat(cleanPath)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			log.Printf("[controller][clipboard] stat %s failed: %v", cleanPath, err)
			continue
		}
		if info.IsDir() {
			log.Printf("[controller][clipboard] skip directory %s", cleanPath)
			continue
		}
		ref, err := a.uploadLocalFile(deviceID, cleanPath, info.Size())
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			log.Printf("[controller][clipboard] upload %s failed: %v", cleanPath, err)
			continue
		}
		refs = append(refs, ref)
	}
	if len(refs) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return refs, nil
}

func (a *controllerApp) fetchRemoteClipboard(deviceID string, afterVersion int64) (clipboardState, error) {
	var data clipboardState
	endpoint := fmt.Sprintf("%s/api/clipboard?deviceId=%s&after=%d", a.serverURL, url.QueryEscape(deviceID), afterVersion)
	resp, err := a.http.Get(a.withAdminKey(endpoint))
	if err != nil {
		return data, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return data, fmt.Errorf("clipboard request failed: %s", strings.TrimSpace(string(raw)))
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return data, err
	}
	return data, nil
}

func (a *controllerApp) transferLoop() {
	ticker := time.NewTicker(a.transferTick)
	defer ticker.Stop()
	for range ticker.C {
		a.syncTransfers()
	}
}

func (a *controllerApp) syncTransfers() {
	a.mu.RLock()
	deviceID := a.deviceID
	a.mu.RUnlock()
	if strings.TrimSpace(deviceID) == "" {
		return
	}
	resp, err := a.fetchTransfers(deviceID)
	if err != nil {
		return
	}
	now := time.Now()
	a.mu.Lock()
	for _, snap := range resp.Transfers {
		snapStatus := strings.TrimSpace(strings.ToLower(snap.Status))
		state, ok := a.transfers[snap.ID]
		if !ok {
			if isTransferTerminal(snapStatus) {
				continue
			}
			state = &controllerTransferState{
				ID:        snap.ID,
				Name:      snap.Name,
				Direction: snap.Direction,
				StartedAt: now,
			}
			a.transfers[snap.ID] = state
		}
		wasTerminal := isTransferTerminal(state.Status)
		state.Name = snap.Name
		state.Direction = snap.Direction
		state.Size = snap.Size
		state.UploadedBytes = maxInt64(state.UploadedBytes, snap.UploadedBytes)
		state.DeliveredBytes = maxInt64(state.DeliveredBytes, snap.DeliveredBytes)
		state.Status = snapStatus
		state.Error = snap.Error
		updateTransferStageRates(state, now)
		if isTransferTerminal(state.Status) {
			state.Cancel = nil
			if !wasTerminal {
				state.UpdatedAt = now
			}
			continue
		}
		state.UpdatedAt = now
	}
	for id, state := range a.transfers {
		if state == nil {
			delete(a.transfers, id)
			continue
		}
		if strings.HasPrefix(state.Direction, "clipboard_") {
			if now.Sub(state.UpdatedAt) > 5*time.Second {
				delete(a.transfers, id)
			}
			continue
		}
		if !isTransferTerminal(state.Status) {
			continue
		}
		if now.Sub(state.UpdatedAt) > 20*time.Second {
			delete(a.transfers, id)
		}
	}
	a.mu.Unlock()
	a.invalidate()
	a.invalidateRemoteFileDialogs()
}

func (a *controllerApp) fetchTransfers(deviceID string) (transferListResponse, error) {
	var data transferListResponse
	endpoint := fmt.Sprintf("%s/api/transfers?deviceId=%s", a.serverURL, url.QueryEscape(deviceID))
	resp, err := a.http.Get(a.withAdminKey(endpoint))
	if err != nil {
		return data, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return data, fmt.Errorf("transfer list failed: %s", strings.TrimSpace(string(raw)))
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return data, err
	}
	return data, nil
}

func (a *controllerApp) handleDropFiles(hDrop uintptr) {
	paths := queryDroppedFiles(hDrop)
	procDragFinish.Call(hDrop)
	if len(paths) == 0 {
		return
	}
	go a.uploadDroppedFiles(paths)
}

func (a *controllerApp) uploadDroppedFiles(paths []string) {
	a.mu.RLock()
	deviceID := a.deviceID
	a.mu.RUnlock()
	if strings.TrimSpace(deviceID) == "" {
		log.Printf("[controller][transfer] drop ignored: no device selected")
		return
	}
	started := 0
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			log.Printf("[controller][transfer] stat %s failed: %v", path, err)
			continue
		}
		if info.IsDir() {
			log.Printf("[controller][transfer] skip directory %s", path)
			continue
		}
		_, err = a.uploadLocalFileNotify(deviceID, path, info.Size(), func(ref transferFileRef) {
			payload := transferCommandPayload{Kind: "drop-upload", Target: "desktop", Files: []transferFileRef{ref}}
			raw, err := json.Marshal(payload)
			if err != nil {
				log.Printf("[controller][transfer] manifest encode failed: %v", err)
				return
			}
			a.sendInputAsync(controllerInput{Type: "transfer_files", Text: string(raw)})
			started++
		})
		if err != nil {
			log.Printf("[controller][transfer] upload %s failed: %v", path, err)
			continue
		}
	}
	if started == 0 {
		return
	}
}

func (a *controllerApp) uploadLocalFile(deviceID, path string, size int64) (transferFileRef, error) {
	return a.uploadLocalFileNotify(deviceID, path, size, nil)
}

func (a *controllerApp) uploadLocalFileNotify(deviceID, path string, size int64, onCreated func(transferFileRef)) (transferFileRef, error) {
	createReq := transferCreateRequest{
		DeviceID:  deviceID,
		Name:      filepath.Base(path),
		Size:      size,
		Direction: "controller_to_agent",
	}
	state, err := a.createTransfer(createReq)
	if err != nil {
		return transferFileRef{}, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.mu.Lock()
	a.transfers[state.ID] = &controllerTransferState{
		ID:            state.ID,
		Name:          state.Name,
		Direction:     state.Direction,
		Size:          state.Size,
		UploadedBytes: 0,
		Status:        "uploading",
		StartedAt:     time.Now(),
		UpdatedAt:     time.Now(),
		Cancel:        cancel,
	}
	a.mu.Unlock()
	ref := transferFileRef{ID: state.ID, Name: state.Name, Size: state.Size}
	if onCreated != nil {
		onCreated(ref)
	}
	defer func() {
		a.mu.Lock()
		if current := a.transfers[state.ID]; current != nil {
			current.Cancel = nil
		}
		a.mu.Unlock()
	}()

	file, err := os.Open(path)
	if err != nil {
		_ = a.cancelTransfer(state.ID)
		cancel()
		return transferFileRef{}, err
	}
	defer file.Close()

	const chunkSize = 256 << 10
	buf := make([]byte, chunkSize)
	var offset int64
	for {
		if err := ctx.Err(); err != nil {
			_ = a.cancelTransfer(state.ID)
			return transferFileRef{}, err
		}
		n, readErr := file.Read(buf)
		if n > 0 {
			if err := a.putTransferChunk(state.ID, offset, buf[:n]); err != nil {
				_ = a.cancelTransfer(state.ID)
				return transferFileRef{}, err
			}
			offset += int64(n)
			a.mu.Lock()
			if current := a.transfers[state.ID]; current != nil {
				now := time.Now()
				current.UploadedBytes = offset
				current.Status = "uploading"
				updateTransferStageRates(current, now)
				current.UpdatedAt = now
			}
			a.mu.Unlock()
			a.invalidate()
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
		return transferFileRef{}, err
	}
	a.mu.Lock()
	if current := a.transfers[state.ID]; current != nil {
		current.UploadedBytes = state.Size
		if current.DeliveredBytes <= 0 {
			current.DeliveredLastBytes = 0
			current.DeliveredLastSampleAt = time.Time{}
			current.DeliveredRateBPS = 0
			current.Status = "ready"
		} else if current.DeliveredBytes >= state.Size && state.Size > 0 {
			current.Status = "completed"
		} else {
			current.Status = "receiving"
		}
		updateTransferStageRates(current, time.Now())
		current.UpdatedAt = time.Now()
	}
	a.mu.Unlock()
	a.invalidate()
	return ref, nil
}

func (a *controllerApp) receiveClipboardFiles(state clipboardState) error {
	state = normalizeClipboardState(state)
	if state.Kind != "files" || len(state.Files) == 0 {
		return nil
	}
	if err := clearControllerClipboard(); err != nil {
		log.Printf("[controller][clipboard] clear stale clipboard failed: %v", err)
	}
	targetDir := resolveControllerClipboardCacheDir(state.DeviceID)
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return err
	}
	paths := make([]string, 0, len(state.Files))
	var firstErr error
	for _, file := range state.Files {
		if strings.TrimSpace(file.ID) == "" {
			continue
		}
		path, err := a.downloadTransferFile(file, targetDir)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			log.Printf("[controller][clipboard] download %s failed: %v", file.Name, err)
			continue
		}
		paths = append(paths, path)
	}
	if len(paths) == 0 {
		return firstErr
	}
	if err := writeControllerClipboardFiles(paths); err != nil {
		return err
	}
	log.Printf("[controller][clipboard] staged %d file(s) for paste -> %s", len(paths), targetDir)
	return nil
}

func (a *controllerApp) downloadTransferFile(file transferFileRef, targetDir string) (string, error) {
	name := sanitizeTransferFilename(file.Name)
	if name == "" {
		name = "remote-file.bin"
	}
	targetPath := uniqueTransferPath(filepath.Join(targetDir, name))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	now := time.Now()
	a.mu.Lock()
	current := a.transfers[file.ID]
	if current == nil {
		current = &controllerTransferState{
			ID:        file.ID,
			StartedAt: now,
		}
		a.transfers[file.ID] = current
	}
	current.Name = name
	current.Direction = "agent_to_controller"
	current.Size = file.Size
	if current.Status == "" {
		current.Status = "waiting"
	}
	current.UpdatedAt = now
	current.Cancel = cancel
	a.mu.Unlock()
	a.invalidate()
	defer func() {
		a.mu.Lock()
		if current := a.transfers[file.ID]; current != nil {
			current.Cancel = nil
		}
		a.mu.Unlock()
	}()

	if err := a.postTransferProgress(file.ID, 0, "receiving", ""); err != nil {
		log.Printf("[controller][transfer] progress start failed for %s: %v", file.Name, err)
	}
	dst, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		_ = a.postTransferProgress(file.ID, 0, "failed", err.Error())
		cancel()
		return "", err
	}
	defer dst.Close()

	const chunkSize = 256 << 10
	var offset int64
	for offset < file.Size {
		if err := ctx.Err(); err != nil {
			_ = a.postTransferProgress(file.ID, offset, "canceled", "canceled")
			return "", err
		}
		chunk, done, err := a.fetchTransferChunk(ctx, file.ID, offset, chunkSize)
		if err != nil {
			_ = a.postTransferProgress(file.ID, offset, "failed", err.Error())
			return "", err
		}
		if len(chunk) == 0 {
			if done {
				break
			}
			err := fmt.Errorf("transfer returned empty chunk before completion")
			_ = a.postTransferProgress(file.ID, offset, "failed", err.Error())
			return "", err
		}
		n, err := dst.Write(chunk)
		if err != nil {
			_ = a.postTransferProgress(file.ID, offset, "failed", err.Error())
			return "", err
		}
		offset += int64(n)
		a.updateDownloadTransferProgress(file, offset)
		if err := a.postTransferProgress(file.ID, offset, "receiving", ""); err != nil {
			log.Printf("[controller][transfer] progress update failed for %s: %v", file.Name, err)
		}
	}
	if err := dst.Sync(); err != nil {
		log.Printf("[controller][transfer] sync warning for %s: %v", file.Name, err)
	}
	a.updateDownloadTransferProgress(file, offset)
	if err := a.postTransferProgress(file.ID, offset, "completed", ""); err != nil {
		log.Printf("[controller][transfer] progress complete failed for %s: %v", file.Name, err)
	}
	a.mu.Lock()
	if current := a.transfers[file.ID]; current != nil {
		current.Status = "completed"
		current.DeliveredBytes = offset
		current.UpdatedAt = time.Now()
	}
	a.mu.Unlock()
	a.invalidate()
	return targetPath, nil
}

func (a *controllerApp) updateDownloadTransferProgress(file transferFileRef, offset int64) {
	now := time.Now()
	a.mu.Lock()
	if current := a.transfers[file.ID]; current != nil {
		current.DeliveredBytes = offset
		current.Status = "receiving"
		updateTransferStageRates(current, now)
		current.UpdatedAt = now
	}
	a.mu.Unlock()
	a.invalidate()
}

func (a *controllerApp) waitTransferServerReady(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("empty transfer id")
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		a.syncTransfers()
		a.mu.RLock()
		state := a.transfers[id]
		var status, errText string
		if state != nil {
			status = strings.TrimSpace(strings.ToLower(state.Status))
			errText = strings.TrimSpace(state.Error)
		}
		a.mu.RUnlock()
		if state != nil {
			switch status {
			case "failed", "canceled":
				if errText == "" {
					errText = status
				}
				return fmt.Errorf("transfer %s: %s", status, errText)
			case "ready", "receiving", "completed":
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (a *controllerApp) createTransfer(req transferCreateRequest) (transferSnapshot, error) {
	var data transferSnapshot
	body, err := json.Marshal(req)
	if err != nil {
		return data, err
	}
	resp, err := a.http.Post(a.withAdminKey(a.serverURL+"/api/transfer/create"), "application/json", bytes.NewReader(body))
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

func (a *controllerApp) putTransferChunk(id string, offset int64, data []byte) error {
	req, err := http.NewRequest(http.MethodPut, a.withAdminKey(fmt.Sprintf("%s/api/transfer/chunk?id=%s&offset=%d", a.serverURL, url.QueryEscape(id), offset)), bytes.NewReader(data))
	if err != nil {
		return err
	}
	resp, err := a.http.Do(req)
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

func (a *controllerApp) completeTransfer(id string) error {
	req, err := http.NewRequest(http.MethodPost, a.withAdminKey(fmt.Sprintf("%s/api/transfer/complete?id=%s", a.serverURL, url.QueryEscape(id))), nil)
	if err != nil {
		return err
	}
	resp, err := a.http.Do(req)
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

func (a *controllerApp) fetchTransferChunk(ctx context.Context, id string, offset int64, size int) ([]byte, bool, error) {
	endpoint := fmt.Sprintf("%s/api/transfer/chunk?id=%s&offset=%d&size=%d", a.serverURL, url.QueryEscape(id), offset, size)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.withAdminKey(endpoint), nil)
	if err != nil {
		return nil, false, err
	}
	resp, err := a.http.Do(req)
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

func (a *controllerApp) postTransferProgress(id string, deliveredBytes int64, status, transferErr string) error {
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
	resp, err := a.http.Post(a.withAdminKey(a.serverURL+"/api/transfer/progress"), "application/json", bytes.NewReader(body))
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

func (a *controllerApp) cancelTransfer(id string) error {
	req, err := http.NewRequest(http.MethodPost, a.withAdminKey(fmt.Sprintf("%s/api/transfer/cancel?id=%s", a.serverURL, url.QueryEscape(id))), nil)
	if err != nil {
		return err
	}
	resp, err := a.http.Do(req)
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

func (a *controllerApp) cancelActiveTransfers() {
	a.mu.RLock()
	ids := make([]string, 0, len(a.transfers))
	cancels := make([]context.CancelFunc, 0, len(a.transfers))
	for id, state := range a.transfers {
		if state == nil || isTransferTerminal(state.Status) || strings.HasPrefix(state.Direction, "clipboard_") {
			continue
		}
		ids = append(ids, id)
		if state.Cancel != nil {
			cancels = append(cancels, state.Cancel)
		}
	}
	a.mu.RUnlock()
	for _, cancel := range cancels {
		cancel()
	}
	for _, id := range ids {
		if err := a.cancelTransfer(id); err != nil {
			log.Printf("[controller][transfer] cancel %s failed: %v", id, err)
		}
	}
}

func queryDroppedFiles(hDrop uintptr) []string {
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
		paths = append(paths, syscall.UTF16ToString(buf))
	}
	return paths
}

func browseForFolder(owner uintptr, title string) (string, bool) {
	if procSHBrowseForFolderW.Find() != nil || procSHGetPathFromIDListW.Find() != nil {
		return "", false
	}
	if procOleInitialize.Find() == nil {
		procOleInitialize.Call(0)
		defer procOleUninitialize.Call()
	}
	display := make([]uint16, maxWinPath)
	bi := browseInfo{
		HwndOwner:      owner,
		PszDisplayName: &display[0],
		LpszTitle:      syscall.StringToUTF16Ptr(title),
		UlFlags:        bifReturnOnlyFSDirs | bifNewDialogStyle,
	}
	pidl, _, _ := procSHBrowseForFolderW.Call(uintptr(unsafe.Pointer(&bi)))
	if pidl == 0 {
		return "", false
	}
	if procCoTaskMemFree.Find() == nil {
		defer procCoTaskMemFree.Call(pidl)
	}
	pathBuf := make([]uint16, maxWinPath)
	ret, _, _ := procSHGetPathFromIDListW.Call(pidl, uintptr(unsafe.Pointer(&pathBuf[0])))
	if ret == 0 {
		return "", false
	}
	path := strings.TrimSpace(syscall.UTF16ToString(pathBuf))
	if path == "" {
		return "", false
	}
	return path, true
}

func wrapDisplayPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return path
	}
	var b strings.Builder
	for _, r := range path {
		b.WriteRune(r)
		switch r {
		case '\\', '/', ':':
			b.WriteByte(' ')
		}
	}
	return b.String()
}

func controllerProgramRunDir() string {
	if dir, err := os.Getwd(); err == nil {
		dir = strings.TrimSpace(dir)
		if dir != "" {
			if st, statErr := os.Stat(dir); statErr == nil && st.IsDir() {
				return dir
			}
		}
	}
	if exe, err := os.Executable(); err == nil {
		dir := strings.TrimSpace(filepath.Dir(exe))
		if dir != "" {
			if st, statErr := os.Stat(dir); statErr == nil && st.IsDir() {
				return dir
			}
		}
	}
	return resolveControllerTransferTargetDir("downloads")
}

func resolveControllerTransferTargetDir(target string) string {
	home, _ := os.UserHomeDir()
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

func resolveControllerClipboardCacheDir(deviceID string) string {
	root := controllerClipboardCacheRoot()
	cleanupOldControllerClipboardCache(root, 24*time.Hour)
	device := safeCachePathSegment(deviceID, "device")
	batch := time.Now().Format("20060102-150405.000000000")
	return filepath.Join(root, device, batch)
}

func controllerClipboardCacheRoot() string {
	if dir, err := os.UserCacheDir(); err == nil && strings.TrimSpace(dir) != "" {
		return filepath.Join(dir, "go-remote-desktop", "controller-clipboard")
	}
	return filepath.Join(os.TempDir(), "go-remote-desktop", "controller-clipboard")
}

func controllerClipboardPathsInCache(paths []string) bool {
	if len(paths) == 0 {
		return false
	}
	root := controllerClipboardCacheRoot()
	for _, path := range paths {
		if !pathWithinDir(path, root) {
			return false
		}
	}
	return true
}

func cleanupOldControllerClipboardCache(root string, maxAge time.Duration) {
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
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return path
	}
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(filepath.Base(path), ext)
	dir := filepath.Dir(path)
	for i := 1; i < 1000; i++ {
		candidate := filepath.Join(dir, fmt.Sprintf("%s (%d)%s", base, i, ext))
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
	return filepath.Join(dir, fmt.Sprintf("%s-%d%s", base, time.Now().Unix(), ext))
}

func isTransferTerminal(status string) bool {
	switch strings.TrimSpace(strings.ToLower(status)) {
	case "completed", "failed", "canceled":
		return true
	default:
		return false
	}
}

func (a *controllerApp) wsEndpoint() string {
	base, err := url.Parse(a.serverURL)
	if err != nil {
		return a.serverURL
	}
	switch strings.ToLower(base.Scheme) {
	case "https":
		base.Scheme = "wss"
	default:
		base.Scheme = "ws"
	}
	base.Path = "/ws/controller"
	query := base.Query()
	query.Set("deviceId", a.deviceID)
	if a.adminKey != "" {
		query.Set("key", a.adminKey)
	}
	base.RawQuery = query.Encode()
	return base.String()
}

func (a *controllerApp) quicEndpoint() string {
	base, err := url.Parse(a.serverURL)
	if err != nil {
		return a.serverURL
	}
	return base.Host
}

func newControllerHTTPClient(timeout time.Duration) *http.Client {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ForceAttemptHTTP2:     false,
		DisableCompression:    true,
		MaxIdleConns:          8,
		MaxIdleConnsPerHost:   8,
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
			configureControllerTCP(conn)
			return conn, nil
		},
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
	}
}

func configureControllerTCP(conn net.Conn) {
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetNoDelay(true)
		_ = tcp.SetKeepAlive(true)
		_ = tcp.SetKeepAlivePeriod(30 * time.Second)
	}
}

func classifyQueuedFramePacket(data []byte) queuedFramePacket {
	item := queuedFramePacket{data: append([]byte(nil), data...)}
	header, _, err := decodeWSFramePacket(data)
	if err != nil {
		return item
	}
	switch header.UpdateKind {
	case "patch", "tiles":
		item.incremental = true
	}
	return item
}

func enqueueFramePacket(ch chan queuedFramePacket, data []byte) bool {
	item := classifyQueuedFramePacket(data)
	if item.incremental {
		ch <- item
		return false
	}
	select {
	case ch <- item:
		return false
	default:
	}
	select {
	case queued := <-ch:
		if queued.incremental {
			ch <- queued
			ch <- item
			return false
		}
		ch <- item
		return true
	default:
	}
	ch <- item
	return false
}

type quicFrameAssembler struct {
	frameID     int64
	chunkCount  int
	chunks      [][]byte
	received    []bool
	receivedCnt int
}

func (a *quicFrameAssembler) Push(datagram []byte) ([]byte, bool, error) {
	if len(datagram) < quicHeaderSize || datagram[0] != quicDatagramMagic {
		return nil, false, fmt.Errorf("invalid quic datagram")
	}

	frameID := int64(binary.BigEndian.Uint64(datagram[1:9]))
	chunkIndex := int(binary.BigEndian.Uint16(datagram[9:11]))
	chunkCount := int(binary.BigEndian.Uint16(datagram[11:13]))
	if chunkCount <= 0 || chunkIndex < 0 || chunkIndex >= chunkCount {
		return nil, false, fmt.Errorf("invalid quic chunk index")
	}

	if frameID < a.frameID {
		return nil, false, nil
	}
	if frameID != a.frameID || chunkCount != a.chunkCount {
		a.frameID = frameID
		a.chunkCount = chunkCount
		a.chunks = make([][]byte, chunkCount)
		a.received = make([]bool, chunkCount)
		a.receivedCnt = 0
	}

	if !a.received[chunkIndex] {
		a.received[chunkIndex] = true
		a.chunks[chunkIndex] = append([]byte(nil), datagram[quicHeaderSize:]...)
		a.receivedCnt++
	}
	if a.receivedCnt != a.chunkCount {
		return nil, false, nil
	}

	totalSize := 0
	for _, chunk := range a.chunks {
		totalSize += len(chunk)
	}
	packet := make([]byte, 0, totalSize)
	for _, chunk := range a.chunks {
		packet = append(packet, chunk...)
	}

	a.frameID = 0
	a.chunkCount = 0
	a.chunks = nil
	a.received = nil
	a.receivedCnt = 0
	return packet, true, nil
}

func controllerLowLevelKeyboardProc(nCode, wParam, lParam uintptr) uintptr {
	if int32(nCode) < 0 {
		ret, _, _ := procCallNextHookEx.Call(0, nCode, wParam, lParam)
		return ret
	}

	app := globalController
	if app == nil || lParam == 0 {
		ret, _, _ := procCallNextHookEx.Call(0, nCode, wParam, lParam)
		return ret
	}

	msg := uint32(wParam)
	if msg != wmKeyDown && msg != wmKeyUp && msg != wmSysKeyDown && msg != wmSysKeyUp {
		ret, _, _ := procCallNextHookEx.Call(0, nCode, wParam, lParam)
		return ret
	}

	keyInfo := (*kbdLLHookStruct)(unsafe.Pointer(lParam))
	vk := int(keyInfo.VkCode)
	keyDown := msg == wmKeyDown || msg == wmSysKeyDown

	if app.handleLocalHotkey(vk, keyDown) {
		return 1
	}

	capture := app.shouldCaptureKeyboard()

	if !capture {
		if vk == vkUp || vk == vkDown {
			if dialog := app.activeRemoteCommandDialog(); dialog != nil && dialog.handleHistoryKey(vk, keyDown) {
				return 1
			}
		}
		if vk == vkReturn {
			if dialog := app.activeRemoteCommandDialog(); dialog != nil && dialog.handleEnterKey(keyDown) {
				return 1
			}
		}
		ret, _, _ := procCallNextHookEx.Call(0, nCode, wParam, lParam)
		return ret
	}

	repeat := app.noteKeyEvent(vk, keyDown, false)
	eventType := "keyup"
	if keyDown {
		eventType = "keydown"
	}
	app.sendInputAsync(controllerInput{
		Type:     eventType,
		VK:       vk,
		Extended: keyInfo.Flags&llkhfExtended != 0 || keyExtended(vk, 0),
		Repeat:   repeat,
	})
	return 1
}

func rgbaToBGRA(img *image.RGBA) []byte {
	if img == nil {
		return nil
	}
	buf := make([]byte, len(img.Pix))
	for i := 0; i < len(img.Pix); i += 4 {
		buf[i] = img.Pix[i+2]
		buf[i+1] = img.Pix[i+1]
		buf[i+2] = img.Pix[i]
		buf[i+3] = 255
	}
	return buf
}

func updateBGRARegion(dst []byte, img *image.RGBA, rect image.Rectangle) {
	if img == nil || len(dst) != len(img.Pix) {
		return
	}
	bounds := rect.Intersect(img.Bounds())
	if bounds.Empty() {
		return
	}
	base := img.Bounds().Min
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		srcRow := (y-base.Y)*img.Stride + (bounds.Min.X-base.X)*4
		dstRow := srcRow
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			dst[dstRow] = img.Pix[srcRow+2]
			dst[dstRow+1] = img.Pix[srcRow+1]
			dst[dstRow+2] = img.Pix[srcRow]
			dst[dstRow+3] = 255
			srcRow += 4
			dstRow += 4
		}
	}
}

func copyBGRAToRGBARegion(dst *image.RGBA, dstRect image.Rectangle, src []byte, srcW, srcH int, srcPoint image.Point) {
	if dst == nil || validateBGRAPayload(src, srcW, srcH) != nil {
		return
	}
	bounds := dstRect.Intersect(dst.Bounds())
	if bounds.Empty() {
		return
	}
	dstBase := dst.Bounds().Min
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		srcY := srcPoint.Y + (y - dstRect.Min.Y)
		if srcY < 0 || srcY >= srcH {
			continue
		}
		dstOffset := (y-dstBase.Y)*dst.Stride + (bounds.Min.X-dstBase.X)*4
		srcOffset := srcY*srcW*4 + (srcPoint.X+(bounds.Min.X-dstRect.Min.X))*4
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			srcX := srcPoint.X + (x - dstRect.Min.X)
			if srcX < 0 || srcX >= srcW {
				dstOffset += 4
				srcOffset += 4
				continue
			}
			dst.Pix[dstOffset] = src[srcOffset+2]
			dst.Pix[dstOffset+1] = src[srcOffset+1]
			dst.Pix[dstOffset+2] = src[srcOffset]
			dst.Pix[dstOffset+3] = 255
			dstOffset += 4
			srcOffset += 4
		}
	}
}

func copyBGRAToBGRAFrame(dst []byte, frameW, frameH int, dstRect image.Rectangle, src []byte, srcW, srcH int, srcPoint image.Point) {
	if frameW <= 0 || frameH <= 0 || len(dst) != frameW*frameH*4 || validateBGRAPayload(src, srcW, srcH) != nil {
		return
	}
	bounds := dstRect.Intersect(image.Rect(0, 0, frameW, frameH))
	if bounds.Empty() {
		return
	}
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		srcY := srcPoint.Y + (y - dstRect.Min.Y)
		if srcY < 0 || srcY >= srcH {
			continue
		}
		srcX := srcPoint.X + (bounds.Min.X - dstRect.Min.X)
		dstX := bounds.Min.X
		width := bounds.Dx()
		if srcX < 0 {
			dstX -= srcX
			width += srcX
			srcX = 0
		}
		if srcX+width > srcW {
			width = srcW - srcX
		}
		if width <= 0 {
			continue
		}
		dstOffset := y*frameW*4 + dstX*4
		srcOffset := srcY*srcW*4 + srcX*4
		copy(dst[dstOffset:dstOffset+width*4], src[srcOffset:srcOffset+width*4])
	}
}

func scaleBGRANearest(src []byte, srcW, srcH, dstW, dstH int) []byte {
	if len(src) == 0 || srcW <= 0 || srcH <= 0 || dstW <= 0 || dstH <= 0 {
		return nil
	}
	dst := make([]byte, dstW*dstH*4)
	for y := 0; y < dstH; y++ {
		srcY := y * srcH / dstH
		srcRow := srcY * srcW * 4
		dstRow := y * dstW * 4
		for x := 0; x < dstW; x++ {
			srcX := x * srcW / dstW
			srcOffset := srcRow + srcX*4
			dstOffset := dstRow + x*4
			copy(dst[dstOffset:dstOffset+4], src[srcOffset:srcOffset+4])
		}
	}
	return dst
}

func scaleBGRABilinear(src []byte, srcW, srcH, dstW, dstH int) []byte {
	if len(src) == 0 || srcW <= 0 || srcH <= 0 || dstW <= 0 || dstH <= 0 {
		return nil
	}
	if srcW == dstW && srcH == dstH {
		return bytes.Clone(src)
	}
	dst := make([]byte, dstW*dstH*4)
	var xDen, yDen int
	if dstW > 1 {
		xDen = dstW - 1
	}
	if dstH > 1 {
		yDen = dstH - 1
	}
	for y := 0; y < dstH; y++ {
		var yFixed int
		if yDen > 0 {
			yFixed = (y * (srcH - 1) * 256) / yDen
		}
		y0 := yFixed / 256
		if y0 >= srcH {
			y0 = srcH - 1
		}
		y1 := minInt(y0+1, srcH-1)
		fy := yFixed - y0*256
		wy0 := 256 - fy
		row0 := y0 * srcW * 4
		row1 := y1 * srcW * 4
		dstRow := y * dstW * 4
		for x := 0; x < dstW; x++ {
			var xFixed int
			if xDen > 0 {
				xFixed = (x * (srcW - 1) * 256) / xDen
			}
			x0 := xFixed / 256
			if x0 >= srcW {
				x0 = srcW - 1
			}
			x1 := minInt(x0+1, srcW-1)
			fx := xFixed - x0*256
			wx0 := 256 - fx
			p00 := row0 + x0*4
			p10 := row0 + x1*4
			p01 := row1 + x0*4
			p11 := row1 + x1*4
			out := dstRow + x*4
			for c := 0; c < 4; c++ {
				value := int(src[p00+c])*wx0*wy0 +
					int(src[p10+c])*fx*wy0 +
					int(src[p01+c])*wx0*fy +
					int(src[p11+c])*fx*fy
				dst[out+c] = byte((value + 32768) >> 16)
			}
		}
	}
	return dst
}

func sharpenBGRA(src []byte, width, height, strength, threshold int) []byte {
	if len(src) == 0 || width < 3 || height < 3 || strength <= 0 {
		return src
	}
	dst := bytes.Clone(src)
	rowStride := width * 4
	for y := 1; y < height-1; y++ {
		row := y * rowStride
		for x := 1; x < width-1; x++ {
			idx := row + x*4
			for c := 0; c < 3; c++ {
				cur := int(src[idx+c])
				edge := cur*4 -
					int(src[idx-4+c]) -
					int(src[idx+4+c]) -
					int(src[idx-rowStride+c]) -
					int(src[idx+rowStride+c])
				if edge > -threshold && edge < threshold {
					continue
				}
				value := cur + (edge*strength)/256
				if value < 0 {
					value = 0
				} else if value > 255 {
					value = 255
				}
				dst[idx+c] = byte(value)
			}
			dst[idx+3] = 255
		}
	}
	return dst
}

func lowWord(v uintptr) int {
	return int(uint16(v & 0xFFFF))
}

func signedLowWord(v uintptr) int {
	return int(int16(v & 0xFFFF))
}

func highWord(v uintptr) int {
	return int(int16((v >> 16) & 0xFFFF))
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func keyExtended(vk int, lParam uintptr) bool {
	if ((lParam >> 24) & 1) == 1 {
		return true
	}
	switch vk {
	case 0x25, 0x26, 0x27, 0x28, 0x21, 0x22, 0x23, 0x24, 0x2D, 0x2E, 0xA3, 0xA5, 0x5B, 0x5C, 0x5D, 0x6F:
		return true
	default:
		return false
	}
}

func keyRepeat(lParam uintptr) bool {
	return lParam != 0 && ((lParam>>30)&1) == 1
}

func enableDPIAwareness() {
	if procSetProcessDpiAwarenessContext.Find() == nil {
		if result, _, _ := procSetProcessDpiAwarenessContext.Call(dpiAwarenessContextPerMonitorAwareV2); result != 0 {
			return
		}
	}
	procSetProcessDPIAware.Call()
}

func (a *controllerApp) clearScaledCacheLocked() {
	a.scaledBGRA = nil
	a.scaledW = 0
	a.scaledH = 0
	a.scaledFrameID = 0
	a.scaledMode = ""
}

func (b *paintBackBuffer) ensure(hdc uintptr, width, height int) bool {
	if b == nil || hdc == 0 || width <= 0 || height <= 0 {
		return false
	}
	if b.memDC != 0 && b.bitmap != 0 && b.width == width && b.height == height {
		return true
	}
	b.close()
	memDC, _, _ := procCreateCompatibleDC.Call(hdc)
	if memDC == 0 {
		return false
	}

	var bi bitmapInfo
	bi.Header.Size = uint32(unsafe.Sizeof(bitmapInfoHeader{}))
	bi.Header.Width = int32(width)
	bi.Header.Height = -int32(height)
	bi.Header.Planes = 1
	bi.Header.BitCount = 32
	bi.Header.Compression = biRGB
	bi.Header.SizeImage = uint32(width * height * 4)

	var bits uintptr
	bitmap, _, _ := procCreateDIBSection.Call(
		hdc,
		uintptr(unsafe.Pointer(&bi)),
		dibRGBColors,
		uintptr(unsafe.Pointer(&bits)),
		0,
		0,
	)
	if bitmap == 0 {
		procDeleteDC.Call(memDC)
		return false
	}
	oldObj, _, _ := procSelectObject.Call(memDC, bitmap)
	if oldObj == 0 {
		procDeleteObject.Call(bitmap)
		procDeleteDC.Call(memDC)
		return false
	}
	b.width = width
	b.height = height
	b.memDC = memDC
	b.bitmap = bitmap
	b.oldObj = oldObj
	b.bits = bits
	return true
}

func (b *paintBackBuffer) close() {
	if b == nil {
		return
	}
	if b.memDC != 0 && b.oldObj != 0 {
		procSelectObject.Call(b.memDC, b.oldObj)
	}
	if b.bitmap != 0 {
		procDeleteObject.Call(b.bitmap)
	}
	if b.memDC != 0 {
		procDeleteDC.Call(b.memDC)
	}
	*b = paintBackBuffer{}
}

func (b *paintBackBuffer) sharpenRect(x, y, width, height, strength, threshold int) {
	if b == nil || b.bits == 0 || b.width <= 0 || b.height <= 0 || width < 3 || height < 3 || strength <= 0 {
		return
	}
	if x < 0 {
		width += x
		x = 0
	}
	if y < 0 {
		height += y
		y = 0
	}
	if x+width > b.width {
		width = b.width - x
	}
	if y+height > b.height {
		height = b.height - y
	}
	if width < 3 || height < 3 {
		return
	}

	frame := unsafe.Slice((*byte)(unsafe.Pointer(b.bits)), b.width*b.height*4)
	roiStride := width * 4
	roi := make([]byte, roiStride*height)
	frameStride := b.width * 4
	for row := 0; row < height; row++ {
		srcStart := (y+row)*frameStride + x*4
		copy(roi[row*roiStride:(row+1)*roiStride], frame[srcStart:srcStart+roiStride])
	}

	for row := 1; row < height-1; row++ {
		srcRow := row * roiStride
		dstRow := (y+row)*frameStride + x*4
		for col := 1; col < width-1; col++ {
			srcIdx := srcRow + col*4
			dstIdx := dstRow + col*4
			for c := 0; c < 3; c++ {
				cur := int(roi[srcIdx+c])
				edge := cur*4 -
					int(roi[srcIdx-4+c]) -
					int(roi[srcIdx+4+c]) -
					int(roi[srcIdx-roiStride+c]) -
					int(roi[srcIdx+roiStride+c])
				if edge > -threshold && edge < threshold {
					continue
				}
				value := cur + (edge*strength)/256
				if value < 0 {
					value = 0
				} else if value > 255 {
					value = 255
				}
				frame[dstIdx+c] = byte(value)
			}
			frame[dstIdx+3] = 255
		}
	}
}

func (a *controllerApp) scaledFrameBGRA(frameID int64, mode string, src []byte, srcW, srcH, dstW, dstH int) []byte {
	a.mu.RLock()
	if frameID == a.scaledFrameID && mode == a.scaledMode && dstW == a.scaledW && dstH == a.scaledH && len(a.scaledBGRA) > 0 {
		buf := a.scaledBGRA
		a.mu.RUnlock()
		return buf
	}
	a.mu.RUnlock()

	mode = normalizeStretchMode(mode)
	var scaled []byte
	switch mode {
	case "pixel":
		scaled = scaleBGRANearest(src, srcW, srcH, dstW, dstH)
	case "smooth":
		scaled = scaleBGRABilinear(src, srcW, srcH, dstW, dstH)
	case "ultra":
		scaled = scaleBGRABilinear(src, srcW, srcH, dstW, dstH)
		scaleX := float64(dstW) / float64(maxInt(srcW, 1))
		scaleY := float64(dstH) / float64(maxInt(srcH, 1))
		if scaleX > 1.02 || scaleY > 1.02 {
			scaled = sharpenBGRA(scaled, dstW, dstH, 96, 3)
		}
	default:
		scaled = scaleBGRABilinear(src, srcW, srcH, dstW, dstH)
		scaleX := float64(dstW) / float64(maxInt(srcW, 1))
		scaleY := float64(dstH) / float64(maxInt(srcH, 1))
		if scaleX > 1.08 || scaleY > 1.08 {
			scaled = sharpenBGRA(scaled, dstW, dstH, 56, 6)
		}
	}
	if len(scaled) == 0 {
		return src
	}

	a.mu.Lock()
	a.scaledBGRA = scaled
	a.scaledW = dstW
	a.scaledH = dstH
	a.scaledFrameID = frameID
	a.scaledMode = mode
	a.mu.Unlock()
	return scaled
}

func controllerDisplayRect(clientW, clientH, remoteW, remoteH int, mode string) (int, int, int, int, bool) {
	if clientW <= 0 || clientH <= 0 || remoteW <= 0 || remoteH <= 0 {
		return 0, 0, 0, 0, false
	}

	switch mode {
	case "fill":
		return 0, 0, clientW, clientH, true
	case "native":
		drawW := remoteW
		drawH := remoteH
		if drawW > clientW {
			drawW = clientW
		}
		if drawH > clientH {
			drawH = clientH
		}
		drawX := (clientW - drawW) / 2
		drawY := (clientH - drawH) / 2
		return drawX, drawY, drawW, drawH, true
	default:
		if remoteW > 0 && remoteH > 0 {
			intScale := minInt(clientW/remoteW, clientH/remoteH)
			if intScale >= 2 {
				drawW := remoteW * intScale
				drawH := remoteH * intScale
				drawX := (clientW - drawW) / 2
				drawY := (clientH - drawH) / 2
				return drawX, drawY, drawW, drawH, true
			}
		}
		scaleX := float64(clientW) / float64(remoteW)
		scaleY := float64(clientH) / float64(remoteH)
		scale := scaleX
		if scaleY < scale {
			scale = scaleY
		}
		if scale <= 0 {
			return 0, 0, 0, 0, false
		}
		drawW := int(float64(remoteW)*scale + 0.5)
		drawH := int(float64(remoteH)*scale + 0.5)
		if drawW <= 0 {
			drawW = 1
		}
		if drawH <= 0 {
			drawH = 1
		}
		drawX := (clientW - drawW) / 2
		drawY := (clientH - drawH) / 2
		return drawX, drawY, drawW, drawH, true
	}
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

func formatQuality(q int) string {
	if q <= 0 {
		return "-"
	}
	return strconv.Itoa(q)
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func nonEmpty(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

func trimLabel(v string, maxRunes int) string {
	runes := []rune(strings.TrimSpace(v))
	if len(runes) <= maxRunes || maxRunes < 4 {
		return string(runes)
	}
	return string(runes[:maxRunes-3]) + "..."
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func (a *controllerApp) shouldHideLocalCursor(hwnd uintptr, clientX, clientY int) bool {
	if !a.windowActive() {
		return false
	}

	a.mu.RLock()
	hasImage := a.image != nil
	remoteW := a.remoteW
	remoteH := a.remoteH
	displayMode := a.displayMode
	overlayVisible := a.overlayVisible
	a.mu.RUnlock()
	if !hasImage || remoteW <= 0 || remoteH <= 0 {
		return false
	}
	if overlayVisible && clientY >= 0 && clientY <= topOverlayH {
		return false
	}

	var client rect
	procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&client)))
	clientW := int(client.Right - client.Left)
	clientH := int(client.Bottom - client.Top)
	drawX, drawY, drawW, drawH, ok := controllerDisplayRect(clientW, clientH, remoteW, remoteH, displayMode)
	if !ok {
		return false
	}
	return clientX >= drawX && clientY >= drawY && clientX < drawX+drawW && clientY < drawY+drawH
}

func (a *controllerApp) paintOverlay(hdc uintptr, clientW int) {
	a.mu.RLock()
	visible := a.overlayVisible
	statsVisible := a.statsVisible
	deviceName := a.deviceName
	immersive := a.immersive
	a.mu.RUnlock()
	if !statsVisible || !visible || clientW <= 0 {
		return
	}

	barRect := rect{Left: 0, Top: 0, Right: int32(clientW), Bottom: topOverlayH}
	restoreRect, closeRect := a.overlayButtonRectsFromWidth(clientW)
	drawFilledRect(hdc, barRect, 0x00111820)
	drawFilledRect(hdc, restoreRect, 0x00212B36)
	drawFilledRect(hdc, closeRect, 0x00332229)
	drawText(hdc, topOverlayPad, 12, overlayLabel(deviceName), 0x00F0F6FC)
	if immersive {
		drawText(hdc, int(restoreRect.Left)+14, 12, "Windowed", 0x00DDE6EE)
	} else {
		drawText(hdc, int(restoreRect.Left)+14, 12, "Maximize", 0x00DDE6EE)
	}
	drawText(hdc, int(closeRect.Left)+18, 12, "Close", 0x00FFD7D5)
}

func (a *controllerApp) paintStatsHUD(hdc uintptr, clientW, clientH, drawX, drawY, drawW, drawH int) {
	if clientW <= 0 || clientH <= 0 {
		return
	}
	a.mu.RLock()
	streamConnected := a.streamConnected
	streamLabel := a.streamLabel
	statsVisible := a.statsVisible
	rxFPS := a.rxFPS
	rxBPS := a.rxBPS
	decodeFPS := a.decodeFPS
	paintFPS := a.paintFPS
	dropped := a.droppedFrames
	lastCodec := a.lastFrameCodec
	lastKind := a.lastFrameKind
	lastBytes := a.lastFrameBytes
	lastQuality := a.lastFrameQuality
	a.mu.RUnlock()
	if !streamConnected || !statsVisible {
		return
	}
	if streamLabel == "" {
		streamLabel = "STREAM"
	}
	if lastCodec == "" {
		lastCodec = "?"
	}
	line1 := fmt.Sprintf("%s  RX %.1f  %s/s  DEC %.1f  PAINT %.1f", strings.ToUpper(streamLabel), rxFPS, formatBytes(int64(rxBPS)), decodeFPS, paintFPS)
	line2 := fmt.Sprintf("%s  %s  Q %s  drop=%d", strings.ToUpper(lastCodec), formatBytes(int64(lastBytes)), formatQuality(lastQuality), dropped)
	if lastKind != "" {
		line2 = fmt.Sprintf("%s  %s  Q %s  drop=%d", lastKind, formatBytes(int64(lastBytes)), formatQuality(lastQuality), dropped)
	}
	panel := statsHUDRect(clientW, clientH, drawX, drawY, drawW, drawH)
	if emptyRect(panel) {
		return
	}
	drawFilledRect(hdc, panel, 0x00111820)
	drawText(hdc, int(panel.Left)+12, int(panel.Top)+10, line1, 0x00F0F6FC)
	drawText(hdc, int(panel.Left)+12, int(panel.Top)+32, line2, 0x00B8C4D0)
}

func statsHUDRect(clientW, clientH, drawX, drawY, drawW, drawH int) rect {
	if clientW <= 24 || clientH <= 24 {
		return rect{}
	}
	panelHeight := 56
	panelLeft := 12
	panelTop := maxInt(clientH-(panelHeight+12), 12)
	if drawW > 0 && drawH > 0 {
		panelLeft = maxInt(drawX+12, 12)
		panelTop = maxInt(drawY+drawH-(panelHeight+12), 12)
	}
	return rect{
		Left:   int32(panelLeft),
		Top:    int32(panelTop),
		Right:  int32(minInt(clientW-12, panelLeft+348)),
		Bottom: int32(minInt(clientH-12, panelTop+panelHeight)),
	}
}

func (a *controllerApp) currentTransferHUDLineLocked() string {
	now := time.Now()
	var bestActive *controllerTransferState
	var bestRecentDone *controllerTransferState
	for _, state := range a.transfers {
		if state == nil {
			continue
		}
		if isTransferTerminal(state.Status) {
			if now.Sub(state.UpdatedAt) > 5*time.Second {
				continue
			}
			if bestRecentDone == nil || state.UpdatedAt.After(bestRecentDone.UpdatedAt) {
				bestRecentDone = state
			}
			continue
		}
		if bestActive == nil {
			bestActive = state
			continue
		}
		if state.UpdatedAt.After(bestActive.UpdatedAt) {
			bestActive = state
		}
	}
	best := bestActive
	if best == nil {
		best = bestRecentDone
	}
	if best == nil {
		return ""
	}
	label := "FILE"
	if strings.HasPrefix(best.Direction, "clipboard_") {
		label = "CLIP"
	}
	line, percent := formatTransferProgressLine(best, 22)
	line = fmt.Sprintf("%s %d%%  %s", label, percent, line)
	if label == "FILE" && !isTransferTerminal(best.Status) {
		line += "  F10 cancel"
	}
	return line
}

func (a *controllerApp) overlayButtonRects(hwnd uintptr) (rect, rect) {
	var client rect
	procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&client)))
	return a.overlayButtonRectsFromWidth(int(client.Right - client.Left))
}

func (a *controllerApp) overlayButtonRectsFromWidth(clientW int) (rect, rect) {
	closeRect := rect{
		Left:   int32(maxInt(topOverlayPad, clientW-topOverlayPad-topOverlayCloseW)),
		Top:    6,
		Right:  int32(maxInt(topOverlayPad+topOverlayCloseW, clientW-topOverlayPad)),
		Bottom: topOverlayH - 6,
	}
	restoreRight := int(closeRect.Left) - 8
	restoreRect := rect{
		Left:   int32(maxInt(topOverlayPad, restoreRight-topOverlayButtonW)),
		Top:    6,
		Right:  int32(maxInt(topOverlayPad+topOverlayButtonW, restoreRight)),
		Bottom: topOverlayH - 6,
	}
	return restoreRect, closeRect
}

func overlayLabel(deviceName string) string {
	if strings.TrimSpace(deviceName) != "" {
		return deviceName
	}
	return "Mydesk"
}

func pointInRect(x, y int, r rect) bool {
	return x >= int(r.Left) && x < int(r.Right) && y >= int(r.Top) && y < int(r.Bottom)
}

func drawFilledRect(hdc uintptr, r rect, color uint32) {
	brush, _, _ := procCreateSolidBrush.Call(uintptr(color))
	if brush == 0 {
		return
	}
	defer procDeleteObject.Call(brush)
	procFillRect.Call(hdc, uintptr(unsafe.Pointer(&r)), brush)
}

func drawText(hdc uintptr, x, y int, text string, color uint32) {
	utf16Text := syscall.StringToUTF16(text)
	if len(utf16Text) == 0 {
		return
	}
	procSetBkMode.Call(hdc, transparent)
	procSetTextColor.Call(hdc, uintptr(color))
	procTextOutW.Call(
		hdc,
		uintptr(x),
		uintptr(y),
		uintptr(unsafe.Pointer(&utf16Text[0])),
		uintptr(len(utf16Text)-1),
	)
}

func (a *controllerApp) paintRemoteCursor(hdc uintptr, drawX, drawY, drawW, drawH, remoteW, remoteH, cursorX, cursorY int, cursorKind string) {
	if remoteW <= 0 || remoteH <= 0 || drawW <= 0 || drawH <= 0 {
		return
	}
	cx := drawX + clampInt(cursorX*drawW/maxInt(remoteW, 1), 0, maxInt(drawW-1, 0))
	cy := drawY + clampInt(cursorY*drawH/maxInt(remoteH, 1), 0, maxInt(drawH-1, 0))
	if cursor := loadSystemCursorHandle(normalizeCursorKind(cursorKind)); cursor != 0 {
		if dx, dy, ok := drawCursorIcon(hdc, cursor, cx, cy); ok {
			_ = dx
			_ = dy
			return
		}
	}
	drawFilledRect(hdc, rect{Left: int32(cx), Top: int32(cy), Right: int32(cx + 18), Bottom: int32(cy + 3)}, 0x00000000)
	drawFilledRect(hdc, rect{Left: int32(cx), Top: int32(cy), Right: int32(cx + 3), Bottom: int32(cy + 18)}, 0x00000000)
	drawFilledRect(hdc, rect{Left: int32(cx + 1), Top: int32(cy + 1), Right: int32(cx + 17), Bottom: int32(cy + 2)}, 0x00FFFFFF)
	drawFilledRect(hdc, rect{Left: int32(cx + 1), Top: int32(cy + 1), Right: int32(cx + 2), Bottom: int32(cy + 17)}, 0x00FFFFFF)
	drawFilledRect(hdc, rect{Left: int32(cx + 2), Top: int32(cy + 2), Right: int32(cx + 6), Bottom: int32(cy + 6)}, 0x00FFFFFF)
}

func drawCursorIcon(hdc, cursor uintptr, x, y int) (int, int, bool) {
	var ii iconInfo
	if ret, _, _ := procGetIconInfo.Call(cursor, uintptr(unsafe.Pointer(&ii))); ret == 0 {
		return 0, 0, false
	}
	if ii.HbmMask != 0 {
		defer procDeleteObject.Call(ii.HbmMask)
	}
	if ii.HbmColor != 0 {
		defer procDeleteObject.Call(ii.HbmColor)
	}
	drawX := x - int(ii.XHotspot)
	drawY := y - int(ii.YHotspot)
	ret, _, _ := procDrawIconEx.Call(hdc, uintptrSigned(drawX), uintptrSigned(drawY), cursor, 0, 0, 0, 0, diNormal)
	return drawX, drawY, ret != 0
}

func loadSystemCursorHandle(kind string) uintptr {
	for _, entry := range controllerStandardCursors {
		if entry.Kind != kind {
			continue
		}
		cursor, _, _ := procLoadCursorW.Call(0, entry.ID)
		return cursor
	}
	cursor, _, _ := procLoadCursorW.Call(0, idcArrow)
	return cursor
}

func createCursorFromPNG(data []byte, hotX, hotY int) (uintptr, error) {
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return 0, err
	}
	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()
	if width <= 0 || height <= 0 || width > 256 || height > 256 {
		return 0, fmt.Errorf("invalid cursor image size %dx%d", width, height)
	}
	rgba := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(rgba, rgba.Bounds(), img, bounds.Min, draw.Src)

	colorBits := make([]byte, width*height*4)
	maskStride := ((width + 15) / 16) * 2
	maskBits := make([]byte, maskStride*height)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			src := y*rgba.Stride + x*4
			dst := (y*width + x) * 4
			alpha := rgba.Pix[src+3]
			colorBits[dst+0] = uint8(uint16(rgba.Pix[src+2]) * uint16(alpha) / 255)
			colorBits[dst+1] = uint8(uint16(rgba.Pix[src+1]) * uint16(alpha) / 255)
			colorBits[dst+2] = uint8(uint16(rgba.Pix[src+0]) * uint16(alpha) / 255)
			colorBits[dst+3] = alpha
			if alpha < 128 {
				maskBits[y*maskStride+x/8] |= 0x80 >> uint(x%8)
			}
		}
	}

	colorBitmap, _, _ := procCreateBitmap.Call(uintptr(width), uintptr(height), 1, 32, uintptr(unsafe.Pointer(&colorBits[0])))
	if colorBitmap == 0 {
		return 0, fmt.Errorf("CreateBitmap color failed")
	}
	defer procDeleteObject.Call(colorBitmap)

	maskBitmap, _, _ := procCreateBitmap.Call(uintptr(width), uintptr(height), 1, 1, uintptr(unsafe.Pointer(&maskBits[0])))
	if maskBitmap == 0 {
		return 0, fmt.Errorf("CreateBitmap mask failed")
	}
	defer procDeleteObject.Call(maskBitmap)

	hotX = clampInt(hotX, 0, maxInt(width-1, 0))
	hotY = clampInt(hotY, 0, maxInt(height-1, 0))
	info := iconInfo{
		FIcon:    0,
		XHotspot: uint32(hotX),
		YHotspot: uint32(hotY),
		HbmMask:  maskBitmap,
		HbmColor: colorBitmap,
	}
	cursor, _, _ := procCreateIconIndirect.Call(uintptr(unsafe.Pointer(&info)))
	if cursor == 0 {
		return 0, fmt.Errorf("CreateIconIndirect failed")
	}
	return cursor, nil
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

func applyDarkTitleBar(hwnd uintptr) {
	dark := int32(1)
	if procDwmSetWindowAttribute.Find() != nil {
		return
	}
	procDwmSetWindowAttribute.Call(hwnd, dwmwaUseImmersiveDarkMode, uintptr(unsafe.Pointer(&dark)), unsafe.Sizeof(dark))
	procDwmSetWindowAttribute.Call(hwnd, dwmwaUseImmersiveDarkModeLegacy, uintptr(unsafe.Pointer(&dark)), unsafe.Sizeof(dark))
}

func disableIME(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	if procImmAssociateContextEx.Find() != nil {
		return
	}
	procImmAssociateContextEx.Call(hwnd, 0, 0)
}

func readControllerClipboardText() (string, error) {
	ok, _, _ := procIsClipboardFormatAvail.Call(cfUnicodeText)
	if ok == 0 {
		return "", nil
	}
	if err := controllerOpenClipboardWithRetry(); err != nil {
		return "", err
	}
	defer controllerCloseClipboardAndUnlock()

	handle, _, _ := procGetClipboardData.Call(cfUnicodeText)
	if handle == 0 {
		return "", nil
	}
	ptr, _, _ := procGlobalLock.Call(handle)
	if ptr == 0 {
		return "", fmt.Errorf("GlobalLock failed")
	}
	defer procGlobalUnlock.Call(handle)

	data := unsafe.Slice((*uint16)(unsafe.Pointer(ptr)), 1<<20)
	size := 0
	for size < len(data) && data[size] != 0 {
		size++
	}
	return syscall.UTF16ToString(data[:size]), nil
}

func clearControllerClipboard() error {
	if err := controllerOpenClipboardWithRetry(); err != nil {
		return err
	}
	defer controllerCloseClipboardAndUnlock()
	if emptied, _, _ := procEmptyClipboard.Call(); emptied == 0 {
		return fmt.Errorf("EmptyClipboard failed")
	}
	return nil
}

func writeControllerClipboardText(text string) error {
	utf := syscall.StringToUTF16(text)
	size := uintptr(len(utf) * 2)

	if err := controllerOpenClipboardWithRetry(); err != nil {
		return err
	}
	defer controllerCloseClipboardAndUnlock()

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

func readControllerClipboardState() (clipboardState, error) {
	if filesState, ok, err := readControllerClipboardFilesState(); ok || err != nil {
		return filesState, err
	}
	if imageState, ok, err := readControllerClipboardImageState(); ok || err != nil {
		return imageState, err
	}
	text, err := readControllerClipboardText()
	if err != nil {
		return clipboardState{}, err
	}
	return clipboardState{Kind: "text", Text: text}, nil
}

func readControllerClipboardFilesState() (clipboardState, bool, error) {
	ok, _, _ := procIsClipboardFormatAvail.Call(cfHDROP)
	if ok == 0 {
		return clipboardState{}, false, nil
	}
	if err := controllerOpenClipboardWithRetry(); err != nil {
		return clipboardState{}, true, err
	}
	defer controllerCloseClipboardAndUnlock()
	handle, _, _ := procGetClipboardData.Call(cfHDROP)
	if handle == 0 {
		return clipboardState{}, false, nil
	}
	paths := queryDroppedFiles(handle)
	if len(paths) == 0 {
		return clipboardState{}, false, nil
	}
	return clipboardState{Kind: "files", Paths: paths}, true, nil
}

func writeControllerClipboardState(state clipboardState) error {
	state = normalizeClipboardState(state)
	if state.Kind == "files" {
		return nil
	}
	if state.Kind == "image" && len(state.Image) > 0 {
		return writeControllerClipboardImagePNG(state.Image)
	}
	if state.Kind != "text" && state.Kind != "image" {
		return nil
	}
	return writeControllerClipboardText(state.Text)
}

func controllerBitmapToDIB(hBitmap uintptr) ([]byte, error) {
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

func readControllerClipboardImageState() (clipboardState, bool, error) {
	pngFormat := controllerPNGClipboardFormatID()
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
	if err := controllerOpenClipboardWithRetry(); err != nil {
		return clipboardState{}, true, err
	}
	defer controllerCloseClipboardAndUnlock()

	var lastErr error
	if pngAvailable != 0 {
		handle, _, _ := procGetClipboardData.Call(pngFormat)
		if handle != 0 {
			data, err := controllerClipboardGlobalBytes(handle, maxClipboardImageBytes)
			if err != nil {
				return clipboardState{}, true, err
			}
			state, err := controllerClipboardImageStateFromPNG(data)
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
		dib, err := controllerClipboardGlobalBytes(handle, 64<<20)
		if err != nil {
			return clipboardState{}, true, err
		}
		state, err := controllerClipboardImageStateFromDIB(dib)
		if err == nil {
			return state, true, nil
		}
		lastErr = err
	}

	if bitmapAvailable != 0 {
		handle, _, _ := procGetClipboardData.Call(cfBitmap)
		if handle != 0 {
			dib, err := controllerBitmapToDIB(handle)
			if err == nil {
				state, err := controllerClipboardImageStateFromDIB(dib)
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

func controllerPNGClipboardFormatID() uintptr {
	controllerPNGClipboardOnce.Do(func() {
		name := syscall.StringToUTF16Ptr("PNG")
		format, _, _ := procRegisterClipboardFormatW.Call(uintptr(unsafe.Pointer(name)))
		controllerPNGClipboardFormat = format
	})
	return controllerPNGClipboardFormat
}

func controllerClipboardGlobalBytes(handle uintptr, maxSize uintptr) ([]byte, error) {
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

func controllerClipboardImageStateFromPNG(data []byte) (clipboardState, error) {
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

func controllerClipboardImageStateFromDIB(dib []byte) (clipboardState, error) {
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

func writeControllerClipboardImagePNG(data []byte) error {
	if len(data) == 0 {
		return writeControllerClipboardText("")
	}
	if len(data) > maxClipboardImageBytes {
		return fmt.Errorf("clipboard image too large: %s", formatBytes(int64(len(data))))
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return err
	}
	dib := dibFromImage(img)
	dibHandle, err := controllerGlobalAllocBytes(dib)
	if err != nil {
		return err
	}
	pngHandle, err := controllerGlobalAllocBytes(data)
	if err != nil {
		procGlobalFree.Call(dibHandle)
		return err
	}
	pngFormat := controllerPNGClipboardFormatID()

	if err := controllerOpenClipboardWithRetry(); err != nil {
		procGlobalFree.Call(dibHandle)
		procGlobalFree.Call(pngHandle)
		return err
	}
	defer controllerCloseClipboardAndUnlock()
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

func controllerGlobalAllocBytes(data []byte) (uintptr, error) {
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

func writeControllerClipboardFiles(paths []string) error {
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

	if err := controllerOpenClipboardWithRetry(); err != nil {
		procGlobalFree.Call(handle)
		return err
	}
	defer controllerCloseClipboardAndUnlock()
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

func paintLetterbox(hdc uintptr, clientW, clientH, drawX, drawY, drawW, drawH int) {
	if drawX > 0 {
		procPatBlt.Call(hdc, 0, 0, uintptr(drawX), uintptr(clientH), blackness)
	}
	rightGap := clientW - (drawX + drawW)
	if rightGap > 0 {
		procPatBlt.Call(hdc, uintptr(drawX+drawW), 0, uintptr(rightGap), uintptr(clientH), blackness)
	}
	if drawY > 0 {
		procPatBlt.Call(hdc, uintptr(drawX), 0, uintptr(drawW), uintptr(drawY), blackness)
	}
	bottomGap := clientH - (drawY + drawH)
	if bottomGap > 0 {
		procPatBlt.Call(hdc, uintptr(drawX), uintptr(drawY+drawH), uintptr(drawW), uintptr(bottomGap), blackness)
	}
}

func paintLetterboxDirty(hdc uintptr, clientW, clientH, drawX, drawY, drawW, drawH int, dirty rect) {
	if emptyRect(dirty) {
		return
	}
	paintRectIntersection(hdc, rect{Right: int32(drawX), Bottom: int32(clientH)}, dirty)
	paintRectIntersection(hdc, rect{Left: int32(drawX + drawW), Right: int32(clientW), Bottom: int32(clientH)}, dirty)
	paintRectIntersection(hdc, rect{Left: int32(drawX), Right: int32(drawX + drawW), Bottom: int32(drawY)}, dirty)
	paintRectIntersection(hdc, rect{Left: int32(drawX), Top: int32(drawY + drawH), Right: int32(drawX + drawW), Bottom: int32(clientH)}, dirty)
}

func paintRectIntersection(hdc uintptr, area, dirty rect) {
	r := intersectRect(area, dirty)
	if emptyRect(r) {
		return
	}
	paintBlackRect(hdc, r)
}

func paintBlackRect(hdc uintptr, r rect) {
	if hdc == 0 || emptyRect(r) {
		return
	}
	procPatBlt.Call(hdc, uintptr(r.Left), uintptr(r.Top), uintptr(rectWidth(r)), uintptr(rectHeight(r)), blackness)
}

func blitPaintRegion(dstDC, srcDC uintptr, r rect) {
	if emptyRect(r) {
		return
	}
	procBitBlt.Call(dstDC, uintptr(r.Left), uintptr(r.Top), uintptr(rectWidth(r)), uintptr(rectHeight(r)), srcDC, uintptr(r.Left), uintptr(r.Top), srccopy)
}

func useGlobalClippedStretch(stretchMode string) bool {
	switch normalizeStretchMode(stretchMode) {
	case "sharp", "ultra":
		return true
	default:
		return false
	}
}

func stretchDIBitsClipped(hdc uintptr, clip rect, drawX, drawY, drawW, drawH, srcW, srcH int, srcBuf []byte, bi *bitmapInfo) {
	if hdc == 0 || emptyRect(clip) || drawW <= 0 || drawH <= 0 || srcW <= 0 || srcH <= 0 || len(srcBuf) == 0 || bi == nil {
		return
	}
	saved, _, _ := procSaveDC.Call(hdc)
	if saved != 0 {
		defer procRestoreDC.Call(hdc, saved)
	}
	procIntersectClipRect.Call(hdc, uintptr(clip.Left), uintptr(clip.Top), uintptr(clip.Right), uintptr(clip.Bottom))
	procStretchDIBits.Call(
		hdc,
		uintptr(drawX),
		uintptr(drawY),
		uintptr(drawW),
		uintptr(drawH),
		0,
		0,
		uintptr(srcW),
		uintptr(srcH),
		uintptr(unsafe.Pointer(&srcBuf[0])),
		uintptr(unsafe.Pointer(bi)),
		dibRGBColors,
		srccopy,
	)
}

func clientRectToSourceRect(client rect, drawX, drawY, drawW, drawH, srcW, srcH int) image.Rectangle {
	if emptyRect(client) || drawW <= 0 || drawH <= 0 || srcW <= 0 || srcH <= 0 {
		return image.Rectangle{}
	}
	left := clampInt((int(client.Left)-drawX)*srcW/drawW, 0, srcW)
	top := clampInt((int(client.Top)-drawY)*srcH/drawH, 0, srcH)
	right := clampInt(((int(client.Right)-drawX)*srcW+drawW-1)/drawW, 0, srcW)
	bottom := clampInt(((int(client.Bottom)-drawY)*srcH+drawH-1)/drawH, 0, srcH)
	if right <= left {
		right = minInt(srcW, left+1)
	}
	if bottom <= top {
		bottom = minInt(srcH, top+1)
	}
	return image.Rect(left, top, right, bottom)
}

func cropBGRARegion(src []byte, srcW, srcH int, r image.Rectangle) ([]byte, int, int) {
	if srcW <= 0 || srcH <= 0 || len(src) < srcW*srcH*4 {
		return nil, 0, 0
	}
	r = r.Intersect(image.Rect(0, 0, srcW, srcH))
	if r.Empty() {
		return nil, 0, 0
	}
	w := r.Dx()
	h := r.Dy()
	if r.Min.X == 0 && r.Min.Y == 0 && w == srcW && h == srcH {
		return src, srcW, srcH
	}
	dst := make([]byte, w*h*4)
	srcStride := srcW * 4
	dstStride := w * 4
	for y := 0; y < h; y++ {
		srcStart := (r.Min.Y+y)*srcStride + r.Min.X*4
		copy(dst[y*dstStride:(y+1)*dstStride], src[srcStart:srcStart+dstStride])
	}
	return dst, w, h
}

func intersectRect(a, b rect) rect {
	r := rect{
		Left:   int32(maxInt(int(a.Left), int(b.Left))),
		Top:    int32(maxInt(int(a.Top), int(b.Top))),
		Right:  int32(minInt(int(a.Right), int(b.Right))),
		Bottom: int32(minInt(int(a.Bottom), int(b.Bottom))),
	}
	if emptyRect(r) {
		return rect{}
	}
	return r
}

func coversRect(outer, inner rect) bool {
	return !emptyRect(inner) &&
		outer.Left <= inner.Left &&
		outer.Top <= inner.Top &&
		outer.Right >= inner.Right &&
		outer.Bottom >= inner.Bottom
}

func emptyRect(r rect) bool {
	return r.Left >= r.Right || r.Top >= r.Bottom
}

func rectWidth(r rect) int {
	return int(r.Right - r.Left)
}

func rectHeight(r rect) int {
	return int(r.Bottom - r.Top)
}

func clampInt(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func boolPtr(v bool) *bool {
	return &v
}

func windowLongIndex(v int32) uintptr {
	return uintptr(uint32(v))
}

func uintptrSigned(v int) uintptr {
	return uintptr(int32(v))
}

func normalizeCursorKind(kind string) string {
	switch strings.TrimSpace(strings.ToLower(kind)) {
	case "text", "wait", "hand", "cross", "size-we", "size-ns", "size-nwse", "size-nesw", "size-all", "forbidden", "help":
		return strings.TrimSpace(strings.ToLower(kind))
	default:
		return "arrow"
	}
}

func getWindowStyle(hwnd uintptr) uintptr {
	index := windowLongIndex(gwlStyle)
	if procGetWindowLongPtrW.Find() == nil {
		style, _, _ := procGetWindowLongPtrW.Call(hwnd, index)
		return style
	}
	if procGetWindowLongW.Find() == nil {
		style, _, _ := procGetWindowLongW.Call(hwnd, index)
		return uintptr(uint32(style))
	}
	return wsPopup | wsThickFrame | wsVisible
}

func setWindowStyle(hwnd, style uintptr) {
	index := windowLongIndex(gwlStyle)
	if procSetWindowLongPtrW.Find() == nil {
		procSetWindowLongPtrW.Call(hwnd, index, style)
		return
	}
	if procSetWindowLongW.Find() == nil {
		procSetWindowLongW.Call(hwnd, index, uintptr(uint32(style)))
	}
}
