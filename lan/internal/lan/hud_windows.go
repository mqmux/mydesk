//go:build windows && amd64

package lan

import (
	"fmt"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

func (v *viewer) modeStatus(status string) string {
	mode := "Func"
	if v.keyMode.Load() {
		mode = "Key"
	}
	return "[Mode: " + mode + "] " + status
}
func (v *viewer) handleF9(down bool, now time.Time) {
	if down {
		if !v.f9Held {
			v.f9Held = true
			v.f9Fired = false
			v.f9Started = now
		}
		return
	}
	v.checkF9(now)
	if v.f9Held && !v.f9Fired && v.capture.Load() && v.onInput != nil {
		v.onInput(inputEvent{Kind: "key-down", VK: 0x78})
		v.onInput(inputEvent{Kind: "key-up", VK: 0x78})
	}
	v.f9Held = false
	v.f9Fired = false
}
func (v *viewer) tickControls(now time.Time) {
	foreground, _, _ := ucall("GetForegroundWindow")
	if foreground != v.hwnd {
		v.f9Held = false
		v.f9Fired = false
	}
	v.checkF9(now)
	if v.pointer.buttons != 0 {
		return // Do not pop up/reset the toolbar while a remote drag is held.
	}
	if !v.fullscreen || v.keyMode.Load() {
		v.showHUD(false)
		return
	}
	var p point
	ucall("GetCursorPos", uintptr(unsafe.Pointer(&p)))
	// Focus determines keyboard routing, not hover visibility. Check the real
	// hit window so an unrelated window covering us cannot summon the toolbar.
	hit, _, _ := ucall("WindowFromPoint", uintptr(uint32(p.X))|uintptr(uint32(p.Y))<<32)
	root, _, _ := ucall("GetAncestor", hit, 2)
	ucall("ScreenToClient", v.hwnd, uintptr(unsafe.Pointer(&p)))
	var r rect
	ucall("GetClientRect", v.hwnd, uintptr(unsafe.Pointer(&r)))
	v.updateHUDHover(int(p.X), int(p.Y), int(r.Right), root == v.hwnd)
}
func (v *viewer) updateHUDHover(x, y, w int, overViewer bool) {
	v.showHUD(v.fullscreen && !v.keyMode.Load() && (v.hudPinned || (overViewer && wantHUD(x, y, w, v.hudVisible))))
}
func wantHUD(x, y, w int, visible bool) bool {
	return x >= 0 && x < w && y >= 0 && ((visible && y < 44) || y < 4)
}
func (v *viewer) checkF9(now time.Time) {
	if v.f9Held && !v.f9Fired && now.Sub(v.f9Started) >= 3*time.Second {
		v.f9Fired = true
		v.keyMode.Store(!v.keyMode.Load())
		v.resetRemoteInput()
		ucall("PostMessageW", v.hwnd, 0x8002, 0, 0)
	}
}
func (v *viewer) showHUD(show bool) {
	if v.hud == 0 || v.hudVisible == show || (show && v.pointer.buttons != 0) {
		return
	}
	v.hudVisible = show
	flag := uintptr(0)
	if show {
		v.resetRemoteInput()
		flag = 4
	}
	ucall("ShowWindow", v.hud, flag)
	ucall("InvalidateRect", v.hwnd, 0, 0)
}
func (v *viewer) layoutHUD() {
	if v.hud == 0 {
		return
	}
	var r rect
	ucall("GetClientRect", v.hwnd, uintptr(unsafe.Pointer(&r)))
	w := int(r.Right)
	ucall("SetWindowPos", v.hud, 0, 0, 0, uintptr(w), 40, 0x10)
	ucall("MoveWindow", v.hudText, 8, 7, uintptr(max(40, w-522)), 26, 1)
	for i, h := range v.hudButtons {
		ucall("MoveWindow", h, uintptr(max(48, w-508)+i*84), 5, 80, 30, 1)
	}
}
func (v *viewer) createHUD(inst uintptr) func() {
	brush, _, _ := gcall("CreateSolidBrush", darkBackground)
	cb := syscall.NewCallback(func(h uintptr, m uint32, w, l uintptr) uintptr {
		switch m {
		case 0x14:
			var r rect
			ucall("GetClientRect", h, uintptr(unsafe.Pointer(&r)))
			ucall("FillRect", w, uintptr(unsafe.Pointer(&r)), brush)
			return 1
		case 0x138:
			return darkControlColors(w, brush)
		case 0x2B:
			paintDarkButton((*drawItem)(unsafe.Pointer(l)))
			return 1
		case 0x111:
			switch w & 65535 {
			case 1:
				ucall("PostMessageW", v.hwnd, 0x8005, 0, 0)
			case 2:
				ucall("PostMessageW", v.hwnd, 0x8004, 0, 0)
			case 3:
				ucall("PostMessageW", v.hwnd, 0x8006, 0, 0)
			case 4:
				ucall("ShowWindow", v.hwnd, 6)
			case 5:
				ucall("PostMessageW", v.hwnd, 0x10, 0, 0)
			case 7:
				v.hudPinned = !v.hudPinned
				label := "固定"
				if v.hudPinned {
					label = "取消固定"
				}
				ucall("SetWindowTextW", v.hudButtons[5], uintptr(unsafe.Pointer(utf16(label))))
				v.tickControls(time.Now())
			}
			return 0
		}
		r, _, _ := ucall("DefWindowProcW", h, uintptr(m), w, l)
		return r
	})
	name := utf16(fmt.Sprintf("MyDeskLANHUD_%x", cb))
	cursor, _, _ := ucall("LoadCursorW", 0, 32512)
	wc := wndClass{Size: uint32(unsafe.Sizeof(wndClass{})), Proc: cb, Instance: inst, Cursor: cursor, Background: brush, ClassName: name}
	registered, _, _ := ucall("RegisterClassExW", uintptr(unsafe.Pointer(&wc)))
	if registered == 0 {
		gcall("DeleteObject", brush)
		return func() {}
	}
	v.hud, _, _ = ucall("CreateWindowExW", 0, uintptr(unsafe.Pointer(name)), 0, 0x42000000, 0, 0, 100, 40, v.hwnd, 0, inst, 0)
	font, releaseFont := boldUIFont(v.hud)
	create := func(class, label string, style uintptr, id int) uintptr {
		h, _, _ := ucall("CreateWindowExW", 0, uintptr(unsafe.Pointer(utf16(class))), uintptr(unsafe.Pointer(utf16(label))), style, 0, 0, 80, 30, v.hud, uintptr(id), inst, 0)
		ucall("SendMessageW", h, 0x30, font, 1)
		return h
	}
	v.hudText = create("STATIC", v.modeStatus("MyDesk LAN"), 0x50000000, 6)
	for i, label := range strings.Split("命令 F7|文件 F8|退出全屏|最小化|关闭|固定", "|") {
		id := i + 1
		if i == 5 {
			id = 7
		}
		v.hudButtons = append(v.hudButtons, create("BUTTON", label, 0x5000000B, id))
	}
	v.layoutHUD()
	return func() {
		if v.hud != 0 {
			ucall("DestroyWindow", v.hud)
			v.hud = 0
		}
		ucall("UnregisterClassW", uintptr(unsafe.Pointer(name)), inst)
		releaseFont()
		gcall("DeleteObject", brush)
	}
}
