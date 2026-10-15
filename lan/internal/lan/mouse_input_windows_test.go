//go:build windows && amd64

package lan

import (
	"context"
	"errors"
	"net"
	"testing"
	"unsafe"
)

func TestAbsoluteMouseCoordinatesRoundTrip(t *testing.T) {
	for _, width := range []int{1, 1920, 2560, 3840, 7680, 32768} {
		for _, origin := range []int{-3840, 0, 1920} {
			for pixel := 0; pixel < width; pixel++ {
				encoded := absoluteMouseCoordinate(pixel+origin, origin, width)
				if got := int(int64(encoded)*int64(width)/65536) + origin; got != pixel+origin {
					t.Fatalf("pixel mismatch: width=%d origin=%d pixel=%d encoded=%d decoded=%d", width, origin, pixel, encoded, got)
				}
			}
		}
	}
}

func TestMouseInjectionFlagsVirtualOriginsAndWheel(t *testing.T) {
	area := desktop{-1920, -200, 1920, 1080}
	virtual := desktop{-1920, -200, 4480, 1640}
	for _, e := range []inputEvent{
		{Kind: "move", X: 12, Y: 34},
		{Kind: "down", X: 100, Y: 200, Button: 0},
		{Kind: "up", X: 300, Y: 400, Button: 0},
		{Kind: "down", X: 0, Y: 0, Button: 1},
		{Kind: "up", X: 1919, Y: 1079, Button: 1},
		{Kind: "down", X: 25, Y: 40, Button: 2},
		{Kind: "up", X: 25, Y: 40, Button: 2},
		{Kind: "wheel", X: 400, Y: 450, Delta: -120},
		{Kind: "move", X: -100, Y: 5000},
	} {
		in, err := mouseEventInput(e, area, virtual, true)
		if err != nil {
			t.Fatal(err)
		}
		flags := uint32(inputMouseMove | inputMouseVirtual | inputMouseAbsolute)
		if in.Type != 0 || in.Mouse.Flags&flags != flags {
			t.Fatal("event does not enter the absolute mouse-motion input stream", e, in)
		}
		x := int(int64(in.Mouse.DX)*int64(virtual.width)/65536) + virtual.left
		y := int(int64(in.Mouse.DY)*int64(virtual.height)/65536) + virtual.top
		if x != max(0, min(area.width-1, e.X))+area.left || y != max(0, min(area.height-1, e.Y))+area.top {
			t.Fatal("negative-origin or selected-monitor mapping failed", e, x, y)
		}
		if e.Kind == "move" && in.Mouse.Flags&inputMouseNoCoalesce == 0 {
			t.Fatal("held drag moves can be coalesced")
		}
		if e.Kind == "wheel" && (in.Mouse.Flags&0x800 == 0 || int32(in.Mouse.Data) != -120) {
			t.Fatal("wheel units/sign changed")
		}
	}
	for _, e := range []inputEvent{{Kind: "down", Button: -1}, {Kind: "up", Button: 3}, {Kind: "unknown"}} {
		if _, err := mouseEventInput(e, area, virtual, false); err == nil {
			t.Fatal("invalid mouse event accepted")
		}
	}
	if _, err := mouseEventInput(inputEvent{Kind: "move"}, area, desktop{}, false); err == nil {
		t.Fatal("invalid virtual desktop accepted")
	}
}

func TestMouseDragInjectionSequenceAndReset(t *testing.T) {
	i := newInputState(desktop{width: 1920, height: 1080})
	i.virtualBounds = func() desktop { return i.area }
	var got []winInput
	i.inject = func(in winInput) error { got = append(got, in); return nil }
	events := []inputEvent{{Kind: "down", X: 80, Y: 90}, {Kind: "move", X: 150, Y: 90}, {Kind: "move", X: 200, Y: 90}, {Kind: "up", X: 200, Y: 90}}
	for _, e := range events {
		if err := i.apply(e); err != nil {
			t.Fatal(err)
		}
	}
	if len(got) != 4 || len(i.buttons) != 0 {
		t.Fatal("pointer move never reached injection sink or release lost")
	}
	for n, flag := range []uint32{2, inputMouseNoCoalesce, inputMouseNoCoalesce, 4} {
		if got[n].Mouse.Flags&flag == 0 || got[n].Mouse.Flags&inputMouseMove == 0 {
			t.Fatal("DOWN/MOVE/UP stream broken", got)
		}
	}
	got = nil
	if err := i.apply(inputEvent{Kind: "down", X: 300, Y: 400}); err != nil {
		t.Fatal(err)
	}
	if err := i.apply(inputEvent{Kind: "key-down", VK: 0x10}); err != nil {
		t.Fatal(err)
	}
	if err := i.apply(inputEvent{Kind: "reset"}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 || len(i.keys) != 0 || len(i.buttons) != 0 || got[3].Mouse.Flags != 4 || got[3].Mouse.DX != 0 || got[3].Mouse.DY != 0 {
		t.Fatal("reset did not release without moving pointer", got)
	}
}

func TestInputFailureDoesNotLoseHeldState(t *testing.T) {
	i := newInputState(desktop{width: 1920, height: 1080})
	i.virtualBounds = func() desktop { return i.area }
	fail := false
	i.inject = func(winInput) error {
		if fail {
			return errors.New("blocked")
		}
		return nil
	}
	for _, e := range []inputEvent{{Kind: "down"}, {Kind: "key-down", VK: 0x5b}} {
		if err := i.apply(e); err != nil {
			t.Fatal(err)
		}
	}
	fail = true
	for _, e := range []inputEvent{{Kind: "up"}, {Kind: "key-up", VK: 0x5b}, {Kind: "down", Button: 1}} {
		if i.apply(e) == nil {
			t.Fatal("injection failure hidden")
		}
	}
	if len(i.buttons) != 1 || !i.buttons[0] || len(i.keys) != 1 {
		t.Fatal("failed UP discarded held state or failed DOWN retained")
	}
	fail = false
	i.release()
	if len(i.buttons) != 0 || len(i.keys) != 0 {
		t.Fatal("held input could not be retried/released")
	}
}

func TestShortcutAndDragControlWirePreservesOrder(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	defer cancel()
	s := &controllerSession{ctx: ctx, cancel: cancel, ctrl: &wire{conn: a}, commands: make(chan message, 16)}
	done := make(chan struct{})
	go func() { defer close(done); s.sendCommands() }()
	defer func() { cancel(); a.Close(); <-done }()
	events := []inputEvent{
		{Kind: "key-down", VK: 0x10}, {Kind: "key-down", VK: 0x5b}, {Kind: "key-down", VK: 'S'},
		{Kind: "key-up", VK: 'S'}, {Kind: "key-up", VK: 0x5b}, {Kind: "key-up", VK: 0x10},
		{Kind: "down", X: 100, Y: 100}, {Kind: "move", X: 200, Y: 180}, {Kind: "move", X: 500, Y: 400}, {Kind: "up", X: 500, Y: 400},
	}
	for _, e := range events {
		s.enqueue(message{Type: "input", Input: e})
	}
	i := newInputState(desktop{width: 1920, height: 1080})
	i.virtualBounds = func() desktop { return i.area }
	var got []winInput
	i.inject = func(in winInput) error { got = append(got, in); return nil }
	w := &wire{conn: b}
	for _, e := range events {
		m, _, err := w.read()
		if err != nil || m.Type != "input" || m.Input != e {
			t.Fatal("input control sequence changed", m, err)
		}
		if err := i.apply(m.Input); err != nil {
			t.Fatal(err)
		}
	}
	if len(got) != len(events) || len(i.keys) != 0 || len(i.buttons) != 0 {
		t.Fatal("shortcut or drag input lost")
	}
	for n := 0; n < 6; n++ {
		ki := (*keyInput)(unsafe.Pointer(&got[n].Mouse))
		if got[n].Type != 1 || ki.VK != events[n].VK || (ki.Flags&2 != 0) != (events[n].Kind == "key-up") {
			t.Fatal("Win+Shift+S key order/release changed")
		}
	}
}
