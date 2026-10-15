//go:build windows && amd64

package lan

import (
	"syscall"
	"unsafe"
)

const darkBackground = 0x00111820
const darkText = 0x00F0F6FC

// Each window owns its font until its child controls have been destroyed.
func boldUIFont(h uintptr) (uintptr, func()) {
	stock, _, _ := gcall("GetStockObject", 17)
	var lf uiLogFont
	gcall("GetObjectW", stock, unsafe.Sizeof(lf), uintptr(unsafe.Pointer(&lf)))
	lf.Weight = 600
	font, _, _ := gcall("CreateFontIndirectW", uintptr(unsafe.Pointer(&lf)))
	if font == 0 {
		font, _, _ = gcall("GetStockObject", 17)
		return font, func() {}
	}
	return font, func() { gcall("DeleteObject", font) }
}

type uiLogFont struct {
	Height, Width, Escapement, Orientation, Weight int32
	Style                                          [8]byte
	Face                                           [32]uint16
}

func darkTitleBar(h uintptr) {
	proc := syscall.NewLazyDLL("dwmapi.dll").NewProc("DwmSetWindowAttribute")
	if proc.Find() != nil {
		return
	}
	on := int32(1)
	for _, attribute := range []uintptr{20, 19} {
		proc.Call(h, attribute, uintptr(unsafe.Pointer(&on)), 4)
	}
}
func darkControl(h uintptr) {
	proc := syscall.NewLazyDLL("uxtheme.dll").NewProc("SetWindowTheme")
	if proc.Find() == nil {
		proc.Call(h, uintptr(unsafe.Pointer(utf16("DarkMode_Explorer"))), 0)
	}
}
func darkControlColors(dc, brush uintptr) uintptr {
	// EDIT (including read-only multiline output) scrolls by moving pixels.
	// Transparent glyph backgrounds leave the old line underneath the new one.
	gcall("SetBkMode", dc, 2)
	gcall("SetBkColor", dc, darkBackground)
	gcall("SetTextColor", dc, darkText)
	return brush
}

type drawItem struct {
	Type, ID, ItemID, Action, State uint32
	Window, DC                      uintptr
	Rect                            rect
	Data                            uintptr
}

func paintDarkButton(item *drawItem) {
	font, _, _ := ucall("SendMessageW", item.Window, 0x31, 0, 0)
	if font != 0 {
		old, _, _ := gcall("SelectObject", item.DC, font)
		defer gcall("SelectObject", item.DC, old)
	}
	color := uintptr(darkBackground)
	if item.State&1 != 0 {
		color = 0x00304050
	}
	brush, _, _ := gcall("CreateSolidBrush", color)
	defer gcall("DeleteObject", brush)
	ucall("FillRect", item.DC, uintptr(unsafe.Pointer(&item.Rect)), brush)
	border, _, _ := gcall("CreateSolidBrush", 0x00505C68)
	defer gcall("DeleteObject", border)
	ucall("FrameRect", item.DC, uintptr(unsafe.Pointer(&item.Rect)), border)
	textColor := uintptr(darkText)
	if item.State&4 != 0 {
		textColor = 0x00708090
	}
	gcall("SetTextColor", item.DC, textColor)
	gcall("SetBkMode", item.DC, 1)
	text := utf16(controlText(item.Window))
	ucall("DrawTextW", item.DC, uintptr(unsafe.Pointer(text)), ^uintptr(0), uintptr(unsafe.Pointer(&item.Rect)), 0x25)
	if item.State&0x10 != 0 {
		r := item.Rect
		r.Left += 3
		r.Top += 3
		r.Right -= 3
		r.Bottom -= 3
		ucall("DrawFocusRect", item.DC, uintptr(unsafe.Pointer(&r)))
	}
}

func viewerWindowBounds(sw, sh, requestedW, requestedH int) (x, y, w, h int) {
	work := rect{0, 0, 1920, 1080}
	monitor, _, _ := ucall("MonitorFromPoint", 0, 2)
	info := monitorInfo{Size: uint32(unsafe.Sizeof(monitorInfo{}))}
	if ret, _, _ := ucall("GetMonitorInfoW", monitor, uintptr(unsafe.Pointer(&info))); ret != 0 {
		work = info.Work
	}
	cw, ch := sw, sh
	if requestedW > 0 {
		cw = requestedW
	}
	if requestedH > 0 {
		ch = requestedH
	}
	r := rect{Right: int32(cw), Bottom: int32(ch)}
	ucall("AdjustWindowRectEx", uintptr(unsafe.Pointer(&r)), 0xCF0000, 0, 0)
	ncW, ncH := int(r.Right-r.Left)-cw, int(r.Bottom-r.Top)-ch
	availableW, availableH := max(1, int(work.Right-work.Left)-ncW), max(1, int(work.Bottom-work.Top)-ncH)
	if cw > availableW || ch > availableH {
		_, _, cw, ch = fitRect(availableW, availableH, cw, ch)
	}
	w, h = max(1, cw)+ncW, max(1, ch)+ncH
	x, y = int(work.Left)+(int(work.Right-work.Left)-w)/2, int(work.Top)+(int(work.Bottom-work.Top)-h)/2
	return
}
