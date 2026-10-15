//go:build windows && amd64

package lan

import (
	"os"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

// Drive only our own viewer's native message handler. The remote input sink
// records events; this test never injects input into Explorer or other apps.
func TestNativePointerCaptureFullscreenAndCancellation(t *testing.T) {
	if os.Getenv("MYDESK_LAN_UI_TEST") != "1" {
		t.Skip("native UI opt-in")
	}
	type received struct {
		event    inputEvent
		captured uintptr
		buttons  uint8
		hud      bool
	}
	events := make(chan received, 64)
	var probeHUD atomic.Bool
	v := &viewer{width: 1920, height: 1080, windowWidth: 960, windowHeight: 600}
	v.keyMode.Store(true)
	v.onInput = func(e inputEvent) {
		if probeHUD.Load() && v.pointer.buttons != 0 {
			v.showHUD(true) // Must not reset the held drag even when requested.
		}
		captured, _, _ := ucall("GetCapture") // Capture belongs to this UI thread.
		events <- received{e, captured, v.pointer.buttons, v.hudVisible}
	}
	done := make(chan error, 1)
	go func() { done <- v.run() }()
	var h uintptr
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		v.mu.Lock()
		h = v.hwnd
		v.mu.Unlock()
		if h != 0 {
			if visible, _, _ := ucall("IsWindowVisible", h); visible != 0 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if h == 0 {
		t.Fatal("viewer not created")
	}
	defer func() {
		ucall("PostMessageW", h, 0x10, 0, 0)
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(3 * time.Second):
			t.Error("viewer did not close")
		}
	}()
	// Disable cursor-based hover polling so the user's actual cursor position
	// cannot influence these synthetic, window-local pointer messages.
	ucall("KillTimer", h, 9)
	// SetCapture/ReleaseCapture can generate real hover messages even without
	// moving the cursor. Filter only untagged pointer messages on our window,
	// keeping the actual viewer handler for every synthetic test event.
	const pointerTag = uintptr(0x5A5A0000)
	previous, _, _ := ucall("GetWindowLongPtrW", h, ^uintptr(3))
	filter := syscall.NewCallback(func(hwnd uintptr, m uint32, w, l uintptr) uintptr {
		if m >= 0x200 && m <= 0x209 && w != pointerTag {
			return 0
		}
		ret, _, _ := ucall("CallWindowProcW", previous, hwnd, uintptr(m), w, l)
		return ret
	})
	ucall("SetWindowLongPtrW", h, ^uintptr(3), filter)
	defer ucall("SetWindowLongPtrW", h, ^uintptr(3), previous)
	ucall("SendMessageW", h, 0x112, 0xF030, 0)
	var bounds rect
	ucall("GetClientRect", h, uintptr(unsafe.Pointer(&bounds)))
	for len(events) > 0 {
		<-events
	}
	v.keyMode.Store(false)
	probeHUD.Store(true)
	send := func(m uint32, x, y int) {
		t.Helper()
		packed := uintptr(uint16(x)) | uintptr(uint16(y))<<16
		ucall("SendMessageW", h, uintptr(m), pointerTag, packed)
	}
	expect := func(kind string, button int, mask uint8, capture bool) received {
		t.Helper()
		select {
		case got := <-events:
			wantCapture := uintptr(0)
			if capture {
				wantCapture = h
			}
			if got.event.Kind != kind || got.event.Button != button || got.buttons != mask || got.captured != wantCapture || got.hud {
				t.Fatalf("got %+v, want %s/%d mask=%d capture=%x without HUD", got, kind, button, mask, wantCapture)
			}
			return got
		case <-time.After(time.Second):
			t.Fatalf("missing %s", kind)
			return received{}
		}
	}
	cx, cy := int(bounds.Right)/2, int(bounds.Bottom)/2
	send(0x201, cx, cy)
	expect("down", 0, 1, true)
	send(0x200, cx, 1) // Drag across the hover-toolbar trigger.
	expect("move", 0, 1, true)
	send(0x200, -20, -20) // Captured motion outside the picture remains valid.
	if e := expect("move", 0, 1, true).event; e.X != 0 || e.Y != 0 {
		t.Fatalf("outside top-left not clamped: %+v", e)
	}
	send(0x204, cx, cy)
	expect("down", 1, 3, true)
	send(0x205, -20, -20)
	expect("up", 1, 1, true) // Right UP must not end the held left drag.
	send(0x202, int(bounds.Right)+20, int(bounds.Bottom)+20)
	if e := expect("up", 0, 0, true).event; e.X != 1919 || e.Y != 1079 {
		t.Fatalf("outside bottom-right not clamped: %+v", e)
	}
	send(0x200, cx, cy)
	expect("move", 0, 0, false) // Matching UP was queued before native release.

	for _, cancelMessage := range []uint32{0x1F, 0x215, 8, 0x8004, 0x8005} {
		send(0x201, cx, cy)
		expect("down", 0, 1, true)
		ucall("SendMessageW", h, uintptr(cancelMessage), 0, 0)
		expect("reset", 0, 0, false)
		send(0x202, cx, cy) // Canceled press must not generate a stray UP.
		send(0x200, cx, cy)
		expect("move", 0, 0, false)
	}
	if len(events) != 0 {
		t.Fatalf("extra reset/UP events: %d", len(events))
	}
}
