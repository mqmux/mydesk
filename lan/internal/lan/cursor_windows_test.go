//go:build windows && amd64

package lan

import (
	"context"
	"net"
	"runtime"
	"testing"
	"time"
	"unsafe"
)

func TestBasicCursorKindsAndCopiedHandles(t *testing.T) {
	classifier := cursorClassifier{}
	for _, entry := range cursorKinds {
		h, _, _ := ucall("LoadCursorW", 0, entry.id)
		if h == 0 {
			t.Fatal("missing system cursor", entry.kind)
		}
		if got := classifier.classify(h); got != entry.kind {
			t.Fatalf("%s classified as %s", entry.kind, got)
		}
		if cursorID(entry.kind) != entry.id {
			t.Fatal("cursor mapping")
		}
	}
	hand, _, _ := ucall("LoadCursorW", 0, 32649)
	copy, _, _ := ucall("CopyIcon", hand)
	if copy == 0 {
		t.Fatal("copy cursor failed")
	}
	defer ucall("DestroyIcon", copy)
	if classifier.classify(copy) != "hand" {
		t.Fatal("application copy of standard cursor not recognized")
	}
	if cursorID("unknown") != 32512 || unsafe.Sizeof(cursorIconInfo{}) != 32 || unsafe.Sizeof(cursorBitmap{}) != 32 || unsafe.Sizeof(remoteCursorInfo{}) != 24 {
		t.Fatal("cursor ABI or unknown fallback")
	}
}
func TestCursorStateIndependentOfVideoRendering(t *testing.T) {
	v := &viewer{}
	s := &controllerSession{ctx: context.Background(), commands: make(chan message, 4), view: v}
	for _, drawRemote := range []bool{false, true} {
		s.setRemoteCursorEnabled(drawRemote)
		v.setCursorState("text", true)
		if v.remoteCursorID.Load() != 32513 || !v.remoteCursorKnown.Load() || !v.remoteCursorVisible.Load() {
			t.Fatal("text cursor state not applied")
		}
		v.setCursorState("hand", false)
		if v.remoteCursorID.Load() != 32649 || v.remoteCursorVisible.Load() {
			t.Fatal("visibility state not applied")
		}
		v.setCursorState("not-a-system-cursor", true)
		if v.remoteCursorID.Load() != 32512 {
			t.Fatal("invalid cursor not normalized")
		}
	}
}

// A private, never-shown window provides real client geometry without moving
// the user's mouse, calling ShowCursor or changing the system clipboard.
func cursorTestViewer(t *testing.T) *viewer {
	t.Helper()
	runtime.LockOSThread()
	inst, _, _ := kernel32.NewProc("GetModuleHandleW").Call(0)
	h, _, err := ucall("CreateWindowExW", 0, uintptr(unsafe.Pointer(utf16("STATIC"))), 0, 0, 0, 0, 640, 480, 0, 0, inst, 0)
	if h == 0 {
		runtime.UnlockOSThread()
		t.Fatal(err)
	}
	t.Cleanup(func() { ucall("DestroyWindow", h); runtime.UnlockOSThread() })
	return &viewer{hwnd: h, width: 1920, height: 1080}
}

func TestF7LocalCursorSurvivesRemoteHidden(t *testing.T) {
	v := cursorTestViewer(t)
	s := &controllerSession{ctx: context.Background(), commands: make(chan message, 8), view: v}
	center := point{320, 240}
	for _, drawRemote := range []bool{false, true} {
		s.setRemoteCursorEnabled(drawRemote)
		s.setLocalCursorEnabled(true)
		for _, entry := range cursorKinds {
			v.setCursorState(entry.kind, false)
			expected, _, _ := ucall("LoadCursorW", 0, entry.id)
			// A mouse/pen suppression flag or a hidden remote cursor must not
			// override the explicit F7 checkbox, even before the first click.
			for _, buttons := range []uint8{0, 1, 0} {
				v.pointer.buttons = buttons
				if got := v.cursorAt(center); got == 0 || got != expected {
					t.Fatalf("local enabled, remote draw=%t hidden=true kind=%s buttons=%d: got=%x want=%x", drawRemote, entry.kind, buttons, got, expected)
				}
			}
			v.setCursorState(entry.kind, true)
			if got := v.cursorAt(center); got != expected {
				t.Fatal("remote visible transition changed local shape", got, expected)
			}
		}
		s.setLocalCursorEnabled(false)
		for _, visible := range []bool{false, true} {
			v.setCursorState("hand", visible)
			if got := v.cursorAt(center); got != 0 {
				t.Fatal("F7 explicit local hide ignored", visible, got)
			}
		}
	}
}

func TestLocalCursorFallbackAndUIRegions(t *testing.T) {
	v := cursorTestViewer(t)
	arrow, _, _ := ucall("LoadCursorW", 0, 32512)
	if v.cursorAt(point{320, 240}) != arrow {
		t.Fatal("missing initial arrow")
	}
	v.setCursorState("unknown-custom-cursor", false)
	if v.cursorAt(point{320, 240}) != arrow {
		t.Fatal("unknown hidden remote cursor removed local fallback")
	}
	v.hideLocalCursor.Store(true)
	if v.cursorAt(point{320, 240}) != 0 {
		t.Fatal("local hide not applied inside picture")
	}
	if v.cursorAt(point{320, 1}) != arrow {
		t.Fatal("letterbox cursor hidden")
	}
	v.width, v.height = 640, 480
	v.hudVisible = true
	if v.cursorAt(point{320, 20}) != arrow {
		t.Fatal("HUD cursor hidden")
	}
}

func TestHiddenCursorControlMessageKeepsLocalPointer(t *testing.T) {
	v := cursorTestViewer(t)
	ctx, cancel := context.WithCancel(context.Background())
	a, b := net.Pipe()
	s := &controllerSession{ctx: ctx, cancel: cancel, view: v, ctrl: &wire{conn: a}, commands: make(chan message, 4)}
	done := make(chan struct{})
	go func() { defer close(done); s.readControl() }()
	defer func() { cancel(); a.Close(); b.Close(); <-done }()
	visible := false
	if err := (&wire{conn: b}).send(message{Type: "cursor-state", CursorKind: "text", CursorVisible: &visible}, nil); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for !v.remoteCursorKnown.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !v.remoteCursorKnown.Load() || v.remoteCursorVisible.Load() {
		t.Fatal("raw visibility metadata was lost")
	}
	expected, _, _ := ucall("LoadCursorW", 0, 32513)
	if v.cursorAt(point{320, 240}) != expected {
		t.Fatal("hidden cursor message hid enabled local pointer")
	}
	s.setLocalCursorEnabled(false)
	if v.cursorAt(point{320, 240}) != 0 {
		t.Fatal("explicit hide checkbox stopped working")
	}
	s.setLocalCursorEnabled(true)
	if v.cursorAt(point{320, 240}) != expected {
		t.Fatal("checking F7 did not restore pointer before click/drag")
	}
	// A private hidden window cannot own the real pointer under the cursor.
	if v.refreshLocalCursor() {
		t.Fatal("cursor refresh escaped a hidden, non-capturing viewer")
	}
}
