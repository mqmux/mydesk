package lan

import "testing"

func TestPointerDragContinuesOutsidePictureAndTracksButtons(t *testing.T) {
	var d pointerDrag
	if _, ok := d.event(0x201, 0, 0, false); ok || d.buttons != 0 {
		t.Fatal("background/letterbox press captured")
	}
	if _, ok := d.event(0x202, 0, 0, true); ok {
		t.Fatal("unmatched UP forwarded")
	}
	if e, ok := d.event(0x201, 100, 120, true); !ok || e.Kind != "down" || d.buttons != 1 {
		t.Fatal("drag did not begin")
	}
	if e, ok := d.event(0x200, 0, 0, false); !ok || e.Kind != "move" || d.buttons != 1 {
		t.Fatal("drag froze outside picture")
	}
	if _, ok := d.event(0x204, 20, 30, true); !ok || d.buttons != 3 {
		t.Fatal("second button not tracked")
	}
	if _, ok := d.event(0x205, 0, 0, false); !ok || d.buttons != 1 {
		t.Fatal("right UP lost left drag")
	}
	if e, ok := d.event(0x202, 1919, 1079, false); !ok || e.Kind != "up" || d.buttons != 0 || d.x != 1919 || d.y != 1079 {
		t.Fatal("release outside picture lost or jumped to origin")
	}
	if _, ok := d.event(0x200, 0, 0, false); ok {
		t.Fatal("idle movement outside picture forwarded")
	}
}

func TestPointerDoubleClickIsAnotherDown(t *testing.T) {
	for _, m := range []uint32{0x203, 0x206, 0x209} {
		var d pointerDrag
		e, ok := d.event(m, 1, 2, true)
		if !ok || e.Kind != "down" || d.buttons == 0 {
			t.Fatal("double-click press dropped")
		}
	}
}
