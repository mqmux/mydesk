package lan

import (
	"bytes"
	"image"
	"testing"
	"time"
)

func TestRefineRetainsUnchangedTiles(t *testing.T) {
	w, h := 130, 70
	src := make([]byte, w*h*4)
	for i := range src {
		src[i] = 111
	}
	now := time.Now()
	tracker := refineTiles{}
	tracker.reset(w, h)
	tracker.observe(src, now)
	if len(tracker.ready(now.Add(399*time.Millisecond))) != 0 {
		t.Fatal("repair before stability")
	}
	ready := tracker.ready(now.Add(400 * time.Millisecond))
	if len(ready) != 1 || ready[0] != image.Rect(0, 0, w, h) {
		t.Fatal(ready)
	}
	overlay := refineOverlay{}
	overlay.reset(w, h)
	if !overlay.patch(ready[0], src) {
		t.Fatal("full repair failed")
	}
	tracker.visit(ready[0], func(i int) { tracker.valid[i] = true })
	next := bytes.Clone(src)
	next[0] = 200
	dirty := tracker.observe(next, now.Add(time.Second))
	if len(dirty) != 1 || dirty[0] != image.Rect(0, 0, 64, 64) {
		t.Fatal(dirty)
	}
	overlay.patch(dirty[0], nil)
	video := make([]byte, len(src))
	result := overlay.compose(video)
	if result[0] != 0 || result[64*4] != 111 || result[(69*w+129)*4] != 111 {
		t.Fatal("unchanged repair discarded")
	}
	if len(tracker.ready(now.Add(1399*time.Millisecond))) != 0 {
		t.Fatal("dirty tile repaired too early")
	}
	ready = tracker.ready(now.Add(1400 * time.Millisecond))
	if len(ready) != 1 || ready[0] != dirty[0] {
		t.Fatal(ready)
	}
	patch := make([]byte, 64*64*4)
	patch[0] = 200
	if !overlay.patch(ready[0], patch) || overlay.compose(video)[0] != 200 {
		t.Fatal("repaired patch missing")
	}
	for _, r := range []image.Rectangle{image.Rect(-64, 0, 0, 64), image.Rect(1, 0, 65, 64), image.Rect(0, 0, 192, 64), image.Rect(0, 0, 64, 65)} {
		if overlay.patch(r, nil) {
			t.Fatal("accepted unaligned/out-of-bounds rectangle", r)
		}
	}
}
