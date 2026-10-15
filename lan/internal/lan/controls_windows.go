//go:build windows && amd64

package lan

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"unsafe"
)

func (s *controllerSession) ownerWindow() uintptr {
	if s.view == nil {
		return 0
	}
	s.view.mu.Lock()
	defer s.view.mu.Unlock()
	return s.view.hwnd
}
func presentOwnedWindow(h, focus uintptr) {
	ucall("ShowWindow", h, 5)
	ucall("SetWindowPos", h, 0, 0, 0, 0, 0, 0x43)
	ucall("BringWindowToTop", h)
	ucall("SetForegroundWindow", h)
	ucall("SetActiveWindow", h)
	if focus != 0 {
		ucall("SetFocus", focus)
	}
}
func (s *controllerSession) cancelTransfers() {
	s.filesMu.Lock()
	f := s.files
	s.filesMu.Unlock()
	if f == nil {
		return
	}
	f.mu.Lock()
	cancel := f.operationCancel
	f.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}
func (s *controllerSession) setInputEnabled(enabled bool) {
	s.inputDisabled.Store(!enabled)
	if s.view != nil {
		s.view.capture.Store(enabled)
		if s.view.onInput != nil {
			s.view.onInput(inputEvent{Kind: "reset"})
		}
		if h := s.ownerWindow(); h != 0 {
			ucall("PostMessageW", h, 0x8009, 0, 0)
		}
	}
	s.enqueue(message{Type: "options", InputEnabled: &enabled})
}
func (s *controllerSession) setClipboardEnabled(enabled bool) {
	s.clipboardGate.Lock()
	s.clipboardDisabled.Store(!enabled)
	if !enabled && s.clipboardCancel != nil {
		s.clipboardCancel()
	}
	s.clipboardGate.Unlock()
	s.enqueue(message{Type: "options", ClipboardEnabled: &enabled})
	if !enabled {
		s.setClipboardNotice("")
	}
}
func (s *controllerSession) setRemoteCursorEnabled(enabled bool) {
	s.cursorHidden.Store(!enabled)
	s.enqueue(message{Type: "options", CursorEnabled: &enabled})
}
func (s *controllerSession) setLocalCursorEnabled(enabled bool) {
	s.localCursorHidden.Store(!enabled)
	if s.view != nil {
		s.view.hideLocalCursor.Store(!enabled)
		if h := s.ownerWindow(); h != 0 {
			ucall("PostMessageW", h, 0x8008, 0, 0)
		}
	}
}

type guardedClipboard struct {
	store    clipboardAccess
	gate     *sync.Mutex
	disabled *atomic.Bool
}

func (g guardedClipboard) snapshot(ctx context.Context) (clipState, error) {
	if g.disabled.Load() {
		return clipState{}, errors.New("剪贴板同步已关闭")
	}
	return g.store.snapshot(ctx)
}
func (g guardedClipboard) apply(ctx context.Context, state clipState, expected uint32) (uint32, error) {
	g.gate.Lock()
	defer g.gate.Unlock()
	if g.disabled.Load() {
		return 0, errors.New("剪贴板同步已关闭")
	}
	return g.store.apply(ctx, state, expected)
}

func (v *viewer) postLocalAction(vk uint32) bool {
	if v.keyMode.Load() {
		return false
	}
	var message uintptr
	switch vk {
	case 0x75:
		message = 0x800A
	case 0x76:
		message = 0x8005
	case 0x77:
		message = 0x8004
	case 0x79:
		message = 0x8007
	case 0x7A:
		message = 0x8006
	case 27:
		if !v.fullscreen {
			return false
		}
		message = 0x8006
	default:
		return false
	}
	ucall("PostMessageW", v.hwnd, message, 0, 0)
	return true
}
func (v *viewer) setFullscreen(enabled bool) {
	if v.fullscreen == enabled {
		return
	}
	if enabled {
		ucall("GetWindowRect", v.hwnd, uintptr(unsafe.Pointer(&v.savedRect)))
		v.savedStyle, _, _ = ucall("GetWindowLongPtrW", v.hwnd, ^uintptr(15))
		monitor, _, _ := ucall("MonitorFromWindow", v.hwnd, 2)
		info := monitorInfo{Size: uint32(unsafe.Sizeof(monitorInfo{}))}
		if ok, _, _ := ucall("GetMonitorInfoW", monitor, uintptr(unsafe.Pointer(&info))); ok == 0 {
			return
		}
		v.fullscreen = true
		ucall("SetWindowLongPtrW", v.hwnd, ^uintptr(15), 0x92000000)
		r := info.Monitor
		ucall("SetWindowPos", v.hwnd, 0, uintptr(r.Left), uintptr(r.Top), uintptr(r.Right-r.Left), uintptr(r.Bottom-r.Top), 0x64)
	} else {
		v.fullscreen = false
		ucall("SetWindowLongPtrW", v.hwnd, ^uintptr(15), v.savedStyle)
		r := v.savedRect
		ucall("SetWindowPos", v.hwnd, 0, uintptr(r.Left), uintptr(r.Top), uintptr(r.Right-r.Left), uintptr(r.Bottom-r.Top), 0x64)
	}
	if !enabled {
		v.showHUD(false)
	}
	v.layoutHUD()
	darkTitleBar(v.hwnd)
	ucall("InvalidateRect", v.hwnd, 0, 0)
}
