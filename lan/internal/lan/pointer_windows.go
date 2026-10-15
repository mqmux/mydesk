//go:build windows && amd64

package lan

func (v *viewer) cancelPointerCapture() {
	// Clear first: ReleaseCapture synchronously sends WM_CAPTURECHANGED.
	v.pointer.buttons = 0
	if captured, _, _ := ucall("GetCapture"); captured == v.hwnd && v.hwnd != 0 {
		ucall("ReleaseCapture")
	}
}

func (v *viewer) resetRemoteInput() {
	v.cancelPointerCapture()
	if v.onInput != nil {
		v.onInput(inputEvent{Kind: "reset"})
	}
}

func (v *viewer) handlePointerMessage(hwnd uintptr, m uint32, l uintptr) {
	if !v.capture.Load() {
		v.cancelPointerCapture()
		return
	}
	// Hover controls may appear on an idle pointer, never in the middle of
	// selecting text, dragging a slider or drawing a snipping rectangle.
	if v.fullscreen && !v.keyMode.Load() && v.pointer.buttons == 0 {
		y := int(int16(l >> 16))
		if y >= 0 && ((v.hudVisible && y < 44) || y < 4) {
			v.showHUD(true)
			return
		}
	}
	x, y, inside := v.mouse(l)
	before := v.pointer.buttons
	e, send := v.pointer.event(m, x, y, inside)
	if !send {
		return
	}
	if e.Kind == "down" && before == 0 {
		ucall("SetCapture", hwnd)
	}
	if v.onInput != nil {
		v.onInput(e)
	}
	if before != 0 || v.pointer.buttons != 0 {
		v.refreshLocalCursor()
	}
	// All buttons share one native capture. Releasing right/middle must not
	// release capture while left is still dragging. UP is queued before release.
	if e.Kind == "up" && v.pointer.buttons == 0 {
		v.cancelPointerCapture()
	}
}
