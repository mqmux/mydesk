//go:build windows

package lan

import (
	"errors"
	"fmt"
	"image"
	"log"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

var user32 = syscall.NewLazyDLL("user32.dll")
var gdi32 = syscall.NewLazyDLL("gdi32.dll")
var kernel32 = syscall.NewLazyDLL("kernel32.dll")

//go:uintptrescapes
func ucall(name string, args ...uintptr) (uintptr, uintptr, error) {
	return user32.NewProc(name).Call(args...)
}

//go:uintptrescapes
func gcall(name string, args ...uintptr) (uintptr, uintptr, error) {
	return gdi32.NewProc(name).Call(args...)
}
func utf16(s string) *uint16 { p, _ := syscall.UTF16PtrFromString(s); return p }

type point struct{ X, Y int32 }
type rect struct{ Left, Top, Right, Bottom int32 }
type desktop struct{ left, top, width, height int }
type monitorInfo struct {
	Size          uint32
	Monitor, Work rect
	Flags         uint32
}

func monitors() []desktop {
	var result []desktop
	for _, output := range monitorOutputs() {
		result = append(result, output.area)
	}
	return result
}
func configureDPI() { ucall("SetProcessDpiAwarenessContext", ^uintptr(3)) }

type bitmapHeader struct {
	Size                         uint32
	Width, Height                int32
	Planes, BitCount             uint16
	Compression, SizeImage       uint32
	XPelsPerMeter, YPelsPerMeter int32
	ClrUsed, ClrImportant        uint32
}
type bitmapInfo struct {
	Header bitmapHeader
	Colors [4]byte
}

func bitmapSpec(w, h int) bitmapInfo {
	return bitmapInfo{Header: bitmapHeader{Size: 40, Width: int32(w), Height: -int32(h), Planes: 1, BitCount: 32}}
}

type screenGrabber struct {
	screen, dc, bitmap, old, pixels uintptr
	size                            int
	area                            desktop
}

func newScreenGrabber(d desktop) (*screenGrabber, error) {
	if d.width < 1 || d.height < 1 || d.width*d.height > 16<<20 {
		return nil, errors.New("invalid desktop size")
	}
	s := &screenGrabber{area: d, size: d.width * d.height * 4}
	s.screen, _, _ = ucall("GetDC", 0)
	s.dc, _, _ = gcall("CreateCompatibleDC", s.screen)
	bi := bitmapSpec(d.width, d.height)
	s.bitmap, _, _ = gcall("CreateDIBSection", s.screen, uintptr(unsafe.Pointer(&bi)), 0, uintptr(unsafe.Pointer(&s.pixels)), 0, 0)
	if s.screen == 0 || s.dc == 0 || s.bitmap == 0 {
		s.close()
		return nil, errors.New("cannot create screen capture DIB")
	}
	s.old, _, _ = gcall("SelectObject", s.dc, s.bitmap)
	return s, nil
}
func (s *screenGrabber) capture() ([]byte, error) {
	return s.captureCursor(true)
}
func (s *screenGrabber) captureCursor(show bool) ([]byte, error) {
	r, _, e := gcall("BitBlt", s.dc, 0, 0, uintptr(s.area.width), uintptr(s.area.height), s.screen, uintptr(s.area.left), uintptr(s.area.top), 0x40CC0020)
	if r == 0 {
		return nil, fmt.Errorf("capture failed: %v", e)
	}
	type cursorInfo struct {
		Size, Flags uint32
		Cursor      uintptr
		Position    point
	}
	type iconInfo struct {
		Icon        int32
		HotX, HotY  uint32
		Mask, Color uintptr
	}
	ci := cursorInfo{Size: uint32(unsafe.Sizeof(cursorInfo{}))}
	if ret, _, _ := ucall("GetCursorInfo", uintptr(unsafe.Pointer(&ci))); show && ret != 0 && ci.Flags&1 != 0 {
		var ii iconInfo
		if ret, _, _ := ucall("GetIconInfo", ci.Cursor, uintptr(unsafe.Pointer(&ii))); ret != 0 {
			x, y := int(ci.Position.X)-s.area.left-int(ii.HotX), int(ci.Position.Y)-s.area.top-int(ii.HotY)
			ucall("DrawIconEx", s.dc, uintptr(x), uintptr(y), ci.Cursor, 0, 0, 0, 0, 3)
			if ii.Mask != 0 {
				gcall("DeleteObject", ii.Mask)
			}
			if ii.Color != 0 {
				gcall("DeleteObject", ii.Color)
			}
		}
	}
	gcall("GdiFlush")
	raw := unsafe.Slice((*byte)(unsafe.Pointer(s.pixels)), s.size)
	return raw, nil
}
func (s *screenGrabber) close() {
	if s.dc != 0 && s.old != 0 {
		gcall("SelectObject", s.dc, s.old)
	}
	if s.bitmap != 0 {
		gcall("DeleteObject", s.bitmap)
	}
	if s.dc != 0 {
		gcall("DeleteDC", s.dc)
	}
	if s.screen != 0 {
		ucall("ReleaseDC", 0, s.screen)
	}
	s.bitmap = 0
	s.dc = 0
	s.screen = 0
}
func bgraImage(b []byte, w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i+3 < len(b) && i+3 < len(img.Pix); i += 4 {
		img.Pix[i] = b[i+2]
		img.Pix[i+1] = b[i+1]
		img.Pix[i+2] = b[i]
		img.Pix[i+3] = 255
	}
	return img
}
func imageBGRA(img image.Image, w, h int) []byte {
	b := make([]byte, w*h*4)
	bounds := img.Bounds()
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, bl, _ := img.At(x+bounds.Min.X, y+bounds.Min.Y).RGBA()
			i := (y*w + x) * 4
			b[i] = byte(bl >> 8)
			b[i+1] = byte(g >> 8)
			b[i+2] = byte(r >> 8)
			b[i+3] = 255
		}
	}
	return b
}

type mouseInput struct {
	DX, DY            int32
	Data, Flags, Time uint32
	Extra             uintptr
}
type winInput struct {
	Type  uint32
	Mouse mouseInput
}
type keyInput struct {
	VK, Scan    uint16
	Flags, Time uint32
	Extra       uintptr
}
type inputState struct {
	area          desktop
	keys          map[uint16]inputEvent
	buttons       map[int]bool
	inject        func(winInput) error // Optional test sink; production uses SendInput.
	virtualBounds func() desktop
}

func newInputState(d desktop) *inputState {
	return &inputState{area: d, keys: map[uint16]inputEvent{}, buttons: map[int]bool{}}
}
func (i *inputState) apply(e inputEvent) error {
	if e.Kind == "reset" {
		i.release()
		return nil
	}
	if e.Kind == "key-down" || e.Kind == "key-up" {
		if e.VK == 0 || e.VK > 255 {
			return errors.New("invalid virtual key")
		}
		var flags uint32
		if e.Kind == "key-up" {
			flags |= 2
		}
		if e.Extended {
			flags |= 1
		}
		in := winInput{Type: 1}
		ki := (*keyInput)(unsafe.Pointer(&in.Mouse))
		*ki = keyInput{VK: e.VK, Scan: e.Scan, Flags: flags}
		if err := i.emit(in); err != nil {
			return err
		}
		if e.Kind == "key-up" {
			delete(i.keys, e.VK)
		} else {
			i.keys[e.VK] = e
		}
		return nil
	}
	virtual := virtualDesktopBounds
	if i.virtualBounds != nil {
		virtual = i.virtualBounds
	}
	in, err := mouseEventInput(e, i.area, virtual(), len(i.buttons) != 0)
	if err != nil {
		return err
	}
	// SetCursorPos alone is not an injected pointer-motion event. Shell/XAML
	// sliders, selection controls and the snipping overlay need real moves in
	// the same input stream as DOWN/UP, just as in the original cloud client.
	if err := i.emit(in); err != nil {
		return err
	}
	if e.Kind == "down" {
		i.buttons[e.Button] = true
	} else if e.Kind == "up" {
		delete(i.buttons, e.Button)
	}
	return nil
}
func (i *inputState) release() {
	for _, e := range i.keys {
		e.Kind = "key-up"
		_ = i.apply(e)
	}
	for b := range i.buttons {
		in := winInput{Mouse: mouseInput{Flags: []uint32{4, 16, 64}[b]}}
		if i.emit(in) == nil {
			delete(i.buttons, b)
		}
	}
}

func clipboardRead() (string, error) {
	ret, _, _ := ucall("OpenClipboard", 0)
	if ret == 0 {
		return "", errors.New("clipboard busy")
	}
	defer ucall("CloseClipboard")
	h, _, _ := ucall("GetClipboardData", 13)
	if h == 0 {
		return "", nil
	}
	p, _, _ := kernel32.NewProc("GlobalLock").Call(h)
	if p == 0 {
		return "", errors.New("clipboard lock")
	}
	defer kernel32.NewProc("GlobalUnlock").Call(h)
	size, _, _ := kernel32.NewProc("GlobalSize").Call(h)
	if size > 2<<20 {
		return "", errors.New("clipboard text exceeds 2 MiB")
	}
	chars := unsafe.Slice((*uint16)(unsafe.Pointer(p)), int(size/2))
	return syscall.UTF16ToString(chars), nil
}
func clipboardWrite(text string) error {
	chars, err := syscall.UTF16FromString(text)
	if err != nil {
		return err
	}
	if len(chars) > 1<<20 {
		return errors.New("clipboard text too large")
	}
	ret, _, _ := ucall("OpenClipboard", 0)
	if ret == 0 {
		return errors.New("clipboard busy")
	}
	defer ucall("CloseClipboard")
	h, _, _ := kernel32.NewProc("GlobalAlloc").Call(2, uintptr(len(chars)*2))
	if h == 0 {
		return errors.New("clipboard allocation")
	}
	p, _, _ := kernel32.NewProc("GlobalLock").Call(h)
	if p == 0 {
		kernel32.NewProc("GlobalFree").Call(h)
		return errors.New("clipboard lock")
	}
	copy(unsafe.Slice((*uint16)(unsafe.Pointer(p)), len(chars)), chars)
	kernel32.NewProc("GlobalUnlock").Call(h)
	ucall("EmptyClipboard")
	ret, _, _ = ucall("SetClipboardData", 13, h)
	if ret == 0 {
		kernel32.NewProc("GlobalFree").Call(h)
		return errors.New("clipboard write")
	}
	return nil
}

type winMessage struct {
	HWnd           uintptr
	Message        uint32
	WParam, LParam uintptr
	Time           uint32
	Pt             point
}
type paintStruct struct {
	Hdc                uintptr
	Erase              int32
	Paint              rect
	Restore, IncUpdate int32
	Reserved           [32]byte
}
type wndClass struct {
	Size, Style                        uint32
	Proc                               uintptr
	ClsExtra, WndExtra                 int32
	Instance, Icon, Cursor, Background uintptr
	MenuName, ClassName                *uint16
	IconSm                             uintptr
}
type viewer struct {
	mu                                     sync.Mutex
	hwnd                                   uintptr
	closed                                 bool
	width, height                          int
	windowWidth, windowHeight              int
	smooth, pixel                          bool
	pixels, refine                         []byte
	status                                 string
	onInput                                func(inputEvent)
	onFiles                                func()
	onCommand, onCancelTransfers           func()
	onMonitor                              func()
	onMinimized                            func(bool)
	minimized                              atomic.Bool
	screenGeneration                       uint64
	pendingDesktop                         *desktopResize
	pressed                                map[uint16]bool
	pointer                                pointerDrag
	renderer                               *d2dRenderer
	displayed                              uint64
	lastFrame, lastPaint                   uint64
	capture, hideLocalCursor               atomic.Bool
	fullscreen                             bool
	savedRect                              rect
	savedStyle                             uintptr
	frames                                 displayMailbox
	baseRevision                           uint64
	gpu                                    *d3d11Renderer
	gpuTried, renderSeeded                 bool
	renderBackend, renderName              string
	renderStats                            bool
	renderMetrics                          displayMetrics
	cpuOverlay                             displayOverlay
	compatibility                          atomic.Bool
	compatPixels                           []byte
	keyMode                                atomic.Bool
	f9Held, f9Fired                        bool
	f9Started                              time.Time
	hud, hudText                           uintptr
	hudButtons                             []uintptr
	hudVisible, hudPinned                  bool
	repairs                                refineOverlay
	sharp                                  sharpDisplay
	remoteCursorID                         atomic.Uint32
	remoteCursorKnown, remoteCursorVisible atomic.Bool
	paintPending                           atomic.Bool
	pipeline                               *pipelineMetrics
}

func (v *viewer) buffer() []byte {
	return v.bufferFor(pixelBGRA)
}
func (v *viewer) bufferFor(format frameFormat) []byte {
	v.mu.Lock()
	size := format.size(v.width, v.height)
	v.mu.Unlock()
	return v.frames.buffer(size)
}

func (v *viewer) requestFramePaint(h uintptr) {
	if h != 0 && !v.minimized.Load() && v.paintPending.CompareAndSwap(false, true) {
		if ok, _, _ := ucall("PostMessageW", h, 0x8001, 0, 0); ok == 0 {
			v.paintPending.Store(false)
		}
	}
}

func (v *viewer) setFrame(b []byte) {
	v.setSurface(b, pixelBGRA)
}
func (v *viewer) setSurface(b []byte, format frameFormat) {
	now := time.Now()
	v.mu.Lock()
	if format.size(v.width, v.height) == 0 || len(b) != format.size(v.width, v.height) {
		v.mu.Unlock()
		return
	}
	v.frames.publishFormat(b, format, now)
	v.pixels = b
	v.repairs.invalidateSurface(b, format)
	v.lastFrame = v.frames.sequence
	v.baseRevision++
	h := v.hwnd
	v.mu.Unlock()
	v.requestFramePaint(h)
}
func (v *viewer) setRefine(b []byte) {
	v.mu.Lock()
	v.refine = b
	v.baseRevision++
	h := v.hwnd
	v.mu.Unlock()
	v.requestFramePaint(h)
}
func (v *viewer) setStatus(s string) {
	v.mu.Lock()
	v.status = s
	h := v.hwnd
	v.mu.Unlock()
	if h != 0 {
		ucall("PostMessageW", h, 0x8002, 0, 0)
	}
}
func (v *viewer) snapshotDisplay(full bool) displaySnapshot {
	v.mu.Lock()
	defer v.mu.Unlock()
	s := displaySnapshot{frame: v.frames.acquire(), pixels: v.pixels, baseRevision: v.baseRevision, repairs: v.repairs.snapshot(full)}
	if s.frame != nil {
		s.format = s.frame.format
	}
	if len(v.refine) == v.width*v.height*4 {
		s.pixels, s.wholeRefine, s.format = v.refine, true, pixelBGRA
	}
	return s
}

func (v *viewer) releaseDisplay(s displaySnapshot) {
	v.mu.Lock()
	v.frames.release(s.frame)
	v.mu.Unlock()
}

func (v *viewer) setRenderName(name string) {
	v.mu.Lock()
	v.renderName = name
	v.mu.Unlock()
}

func (v *viewer) paint() {
	var ps paintStruct
	ucall("BeginPaint", v.hwnd, uintptr(unsafe.Pointer(&ps)))
	defer ucall("EndPaint", v.hwnd, uintptr(unsafe.Pointer(&ps)))
	var r rect
	ucall("GetClientRect", v.hwnd, uintptr(unsafe.Pointer(&r)))
	cw, ch := int(r.Right), int(r.Bottom)
	iconic, _, _ := ucall("IsIconic", v.hwnd)
	if cw < 1 || ch < 1 || iconic != 0 {
		ucall("KillTimer", v.hwnd, 10)
		return
	}
	if !v.gpuTried {
		v.gpuTried = true
		if v.renderBackend != "d2d" {
			var err error
			v.gpu, err = newD3D11(v.hwnd, cw, ch, v.width, v.height)
			if err != nil {
				log.Printf("[lan][render] D3D11 unavailable; compatibility renderer: %v", err)
			} else {
				v.setRenderName("D3D11")
				log.Print("[lan][render] D3D11 GPU scaling/sharp + regional lossless layers")
			}
		}
		if v.gpu == nil {
			v.compatibility.Store(true)
		}
	}
	s := v.snapshotDisplay(!v.renderSeeded)
	defer func() { v.releaseDisplay(s) }()
	native := s.frame != nil && s.frame.native != nil && !s.wholeRefine
	if !native && (s.format.size(v.width, v.height) == 0 || len(s.pixels) != s.format.size(v.width, v.height)) {
		return
	}
	v.renderSeeded = true
	x, y, w, h := fitRect(cw, ch, v.width, v.height)
	var times renderTimes
	presented := true
	if v.gpu != nil {
		var err error
		times, presented, err = v.gpu.draw(s, cw, ch, x, y, w, h, v.smooth, v.pixel)
		if err == errGPUNotReady {
			// Snapshot updates must survive a nonblocking cross-device retry.
			v.mu.Lock()
			if s.repairs.mask != nil {
				v.repairs.maskDirty = true
			}
			for _, p := range s.repairs.patches {
				tracker := refineTiles{width: v.width}
				tracker.visit(p.rect, func(i int) {
					if v.repairs.valid[i] {
						v.repairs.pixelDirty[i] = true
					}
				})
			}
			v.mu.Unlock()
			ucall("SetTimer", v.hwnd, 10, 2, 0)
			return
		}
		if err != nil {
			log.Printf("[lan][render] D3D11 failed; compatibility renderer: %v", err)
			v.gpu.close()
			v.gpu = nil
			v.compatibility.Store(true)
			// Replay all valid patches into the independent CPU fallback surface.
			v.releaseDisplay(s)
			s = v.snapshotDisplay(true)
			presented = true
		} else if !presented {
			// A short one-shot timer yields to keyboard/mouse messages instead of
			// blocking on DXGI or spinning posted render messages.
			retry := uintptr(8)
			if v.gpu.occluded {
				retry = 250
			}
			ucall("SetTimer", v.hwnd, 10, retry, 0)
		}
	}
	if v.gpu == nil {
		if native {
			return
		} // The decode worker switches to the existing CPU path.
		times = v.paintCompatibility(s, ps.Hdc, cw, ch, x, y, w, h)
	}
	if presented && s.frame != nil {
		v.mu.Lock()
		if s.frame.sequence != v.lastPaint {
			now := time.Now()
			v.pipeline.observe(3, times.upload)
			v.pipeline.observe(4, now.Sub(s.frame.decoded))
			v.pipeline.recordPresent(now)
			v.renderMetrics.record(s.frame, v.lastPaint, times, now)
			v.displayed++
			v.lastPaint = s.frame.sequence
		}
		v.mu.Unlock()
	}
}

func (v *viewer) paintCompatibility(s displaySnapshot, dc uintptr, cw, ch, x, y, w, h int) renderTimes {
	start := time.Now()
	v.cpuOverlay.update(s.repairs, v.width, v.height)
	b := s.pixels
	if s.format == pixelNV12 {
		v.compatPixels = nv12ToBGRA(v.compatPixels, b, v.width, v.height)
		b = v.compatPixels
	}
	if !s.wholeRefine {
		b = v.cpuOverlay.compose(b, v.width, v.height)
	}
	bw, bh := v.width, v.height
	if !v.smooth && !v.pixel && (w > bw || h > bh) && w*h <= 16<<20 {
		b = v.sharp.prepare(b, bw, bh, w, h)
		bw, bh = w, h
	}
	if v.renderer == nil {
		v.renderer, _ = newD2D(v.hwnd, cw, ch, bw, bh)
		if v.renderer != nil {
			v.setRenderName("Direct2D")
		} else {
			v.setRenderName("GDI")
		}
	}
	if v.renderer != nil {
		if err := v.renderer.draw(b, bw, bh, cw, ch, x, y, w, h, v.smooth, v.pixel); err != nil {
			v.renderer.close()
			v.renderer = nil
		}
	}
	if v.renderer == nil {
		gcall("PatBlt", dc, 0, 0, uintptr(cw), uintptr(ch), 0x42)
		bi := bitmapSpec(bw, bh)
		mode := uintptr(3)
		if !v.pixel && (v.smooth || w < v.width || h < v.height) {
			mode = 4
			gcall("SetBrushOrgEx", dc, 0, 0, 0)
		}
		gcall("SetStretchBltMode", dc, mode)
		gcall("StretchDIBits", dc, uintptr(x), uintptr(y), uintptr(w), uintptr(h), 0, 0, uintptr(bw), uintptr(bh), uintptr(unsafe.Pointer(&b[0])), uintptr(unsafe.Pointer(&bi)), 0, 0xCC0020)
	}
	return renderTimes{submit: time.Since(start)}
}
func fitRect(cw, ch, w, h int) (int, int, int, int) {
	if cw <= 0 || ch <= 0 || w <= 0 || h <= 0 {
		return 0, 0, 0, 0
	}
	dw := cw
	dh := cw * h / w
	if dh > ch {
		dh = ch
		dw = ch * w / h
	}
	return (cw - dw) / 2, (ch - dh) / 2, dw, dh
}
func (v *viewer) mouse(l uintptr) (int, int, bool) {
	var r rect
	ucall("GetClientRect", v.hwnd, uintptr(unsafe.Pointer(&r)))
	ox, oy, w, h := fitRect(int(r.Right), int(r.Bottom), v.width, v.height)
	x, y := int(int16(l&65535)), int(int16((l>>16)&65535))
	if w < 1 || h < 1 {
		return v.pointer.x, v.pointer.y, false
	}
	inside := x >= ox && y >= oy && x < ox+w && y < oy+h
	return max(0, min(v.width-1, (x-ox)*v.width/w)), max(0, min(v.height-1, (y-oy)*v.height/h)), inside
}
func (v *viewer) run() error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	v.capture.Store(true)
	v.pressed = map[uint16]bool{}
	var keyboardHook uintptr
	heldLocal := map[uint32]bool{}
	cb := syscall.NewCallback(func(hwnd uintptr, m uint32, w, l uintptr) uintptr {
		switch m {
		case 0x8001:
			v.paintPending.Store(false)
			ucall("InvalidateRect", hwnd, 0, 0)
			// WM_PAINT is lower priority than posted frame messages. Paint the
			// newest frame now, rather than waiting for that queue to go idle.
			ucall("UpdateWindow", hwnd)
			return 0
		case 0x8002:
			v.mu.Lock()
			s := v.status
			v.mu.Unlock()
			s = v.modeStatus(s)
			ucall("SetWindowTextW", hwnd, uintptr(unsafe.Pointer(utf16(s))))
			if v.hudText != 0 {
				ucall("SetWindowTextW", v.hudText, uintptr(unsafe.Pointer(utf16(s))))
			}
			return 0
		case 0x113:
			if w == 10 {
				ucall("KillTimer", hwnd, 10)
				ucall("InvalidateRect", hwnd, 0, 0)
				ucall("UpdateWindow", hwnd)
				return 0
			}
			if w == 9 {
				v.tickControls(time.Now())
				return 0
			}
		case 0x8004:
			v.resetRemoteInput()
			if v.onFiles != nil {
				v.onFiles()
			}
			return 0
		case 0x8005:
			v.resetRemoteInput()
			if v.onCommand != nil {
				v.onCommand()
			}
			return 0
		case 0x8006:
			v.setFullscreen(!v.fullscreen)
			return 0
		case 0x8007:
			if v.onCancelTransfers != nil {
				v.onCancelTransfers()
			}
			return 0
		case 0x8009:
			v.cancelPointerCapture() // F7 input-mode change, on the viewer thread.
			return 0
		case 0x800A:
			v.resetRemoteInput()
			if v.onMonitor != nil {
				v.onMonitor()
			}
			return 0
		case 0x800B:
			v.applyPendingDesktop()
			return 0
		case 0x215, 0x1F: // WM_CAPTURECHANGED / WM_CANCELMODE
			if v.pointer.buttons != 0 {
				v.resetRemoteInput()
			}
			return 0
		case 0x8008:
			v.refreshLocalCursor()
			return 0
		case 0x20:
			if l&65535 == 1 && w == hwnd {
				var p point
				ucall("GetCursorPos", uintptr(unsafe.Pointer(&p)))
				ucall("ScreenToClient", hwnd, uintptr(unsafe.Pointer(&p)))
				ucall("SetCursor", v.cursorAt(p))
				return 1
			}
		case 0x112:
			if w&0xFFF0 == 0xF030 {
				v.setFullscreen(true)
				return 0
			}
			if w&0xFFF0 == 0xF120 && v.fullscreen {
				v.setFullscreen(false)
				return 0
			}
		case 0xF:
			v.paint()
			return 0
		case 0x14:
			return 1
		case 5:
			v.setMinimized(w == 1) // SIZE_MINIMIZED; losing focus is not minimizing.
			if w == 1 {
				ucall("KillTimer", hwnd, 10)
			}
			v.layoutHUD()
			ucall("InvalidateRect", hwnd, 0, 0)
			return 0
		case 6:
			if w&65535 != 0 {
				break
			}
			fallthrough
		case 8:
			v.f9Held = false
			v.f9Fired = false
			heldLocal = map[uint32]bool{}
			v.resetRemoteInput()
			v.pressed = map[uint16]bool{}
			return 0
		case 0x100, 0x104, 0x101, 0x105:
			if keyboardHook != 0 {
				return 0
			}
			if w == 0x78 {
				v.handleF9(m == 0x100 || m == 0x104, time.Now())
				return 0
			}
			if (m == 0x100 || m == 0x104) && l&(1<<30) == 0 && v.postLocalAction(uint32(w)) {
				heldLocal[uint32(w)] = true
				return 0
			}
			if heldLocal[uint32(w)] {
				if m == 0x101 || m == 0x105 {
					delete(heldLocal, uint32(w))
				}
				return 0
			}
			if !v.capture.Load() {
				break
			}
			kind := "key-down"
			if m == 0x101 || m == 0x105 {
				kind = "key-up"
			}
			v.onInput(inputEvent{Kind: kind, VK: uint16(w), Scan: uint16(l >> 16 & 255), Extended: l&(1<<24) != 0})
			return 0
		case 0x200, 0x201, 0x202, 0x203, 0x204, 0x205, 0x206, 0x207, 0x208, 0x209:
			v.handlePointerMessage(hwnd, m, l)
			return 0
		case 0x20A:
			if v.capture.Load() {
				p := point{int32(int16(l & 65535)), int32(int16(l >> 16 & 65535))}
				ucall("ScreenToClient", hwnd, uintptr(unsafe.Pointer(&p)))
				packed := uintptr(uint16(p.X)) | uintptr(uint16(p.Y))<<16
				x, y, inside := v.mouse(packed)
				if inside {
					v.onInput(inputEvent{Kind: "wheel", X: x, Y: y, Delta: int(int16(w >> 16 & 65535))})
				}
				return 0
			}
		case 0x10:
			ucall("DestroyWindow", hwnd)
			return 0
		case 2:
			v.resetRemoteInput()
			ucall("PostQuitMessage", 0)
			return 0
		}
		ret, _, _ := ucall("DefWindowProcW", hwnd, uintptr(m), w, l)
		return ret
	})
	inst, _, _ := kernel32.NewProc("GetModuleHandleW").Call(0)
	cursor, _, _ := ucall("LoadCursorW", 0, 32512)
	name := utf16(fmt.Sprintf("MyDeskLANViewer_%x", cb))
	wc := wndClass{Size: uint32(unsafe.Sizeof(wndClass{})), Proc: cb, Instance: inst, Cursor: cursor, ClassName: name}
	if ret, _, err := ucall("RegisterClassExW", uintptr(unsafe.Pointer(&wc))); ret == 0 {
		return fmt.Errorf("RegisterClass: %v", err)
	}
	defer ucall("UnregisterClassW", uintptr(unsafe.Pointer(name)), inst)
	x, y, ww, hh := viewerWindowBounds(v.width, v.height, v.windowWidth, v.windowHeight)
	hwnd, _, err := ucall("CreateWindowExW", 0, uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(utf16("MyDesk LAN - connecting"))), 0x02CF0000, uintptr(x), uintptr(y), uintptr(ww), uintptr(hh), 0, 0, inst, 0)
	if hwnd == 0 {
		return err
	}
	darkTitleBar(hwnd)
	v.mu.Lock()
	v.hwnd = hwnd
	v.mu.Unlock()
	cleanupHUD := v.createHUD(inst)
	defer cleanupHUD()
	ucall("SetTimer", hwnd, 9, 50, 0)
	defer ucall("KillTimer", hwnd, 9)
	defer ucall("KillTimer", hwnd, 10)
	type hookKey struct {
		VK, Scan, Flags, Time uint32
		Extra                 uintptr
	}
	hookCallback := syscall.NewCallback(func(code int32, w, l uintptr) uintptr {
		if code >= 0 {
			foreground, _, _ := ucall("GetForegroundWindow")
			k := (*hookKey)(unsafe.Pointer(l))
			// The opening function key may be released after focus moves into an
			// owned dialog. Never retain that key as a repeat on returning here.
			if foreground != hwnd && k.Flags&0x90 == 0x80 {
				delete(heldLocal, k.VK)
			}
			if foreground == hwnd && k.Flags&0x10 == 0 {
				up := k.Flags&0x80 != 0
				if k.VK == 0x78 {
					v.handleF9(!up, time.Now())
					return 1
				}
				if heldLocal[k.VK] {
					if up {
						delete(heldLocal, k.VK)
					}
					return 1
				}
				if !up && v.postLocalAction(k.VK) {
					heldLocal[k.VK] = true
					return 1
				}
				if v.capture.Load() {
					kind := "key-down"
					if up {
						kind = "key-up"
					}
					v.onInput(inputEvent{Kind: kind, VK: uint16(k.VK), Scan: uint16(k.Scan), Extended: k.Flags&1 != 0})
					return 1
				}
			}
		}
		r, _, _ := ucall("CallNextHookEx", keyboardHook, uintptr(code), w, l)
		return r
	})
	keyboardHook, _, _ = ucall("SetWindowsHookExW", 13, hookCallback, inst, 0)
	defer func() {
		if v.gpu != nil {
			v.gpu.close()
		}
		if keyboardHook != 0 {
			ucall("UnhookWindowsHookEx", keyboardHook)
		}
	}()
	ucall("ShowWindow", hwnd, 5)
	defer func() {
		if v.renderer != nil {
			v.renderer.close()
		}
		v.mu.Lock()
		v.hwnd = 0
		v.closed = true
		v.mu.Unlock()
	}()
	var msg winMessage
	for {
		ret, _, _ := ucall("GetMessageW", uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(ret) <= 0 {
			break
		}
		ucall("TranslateMessage", uintptr(unsafe.Pointer(&msg)))
		ucall("DispatchMessageW", uintptr(unsafe.Pointer(&msg)))
	}
	return nil
}
