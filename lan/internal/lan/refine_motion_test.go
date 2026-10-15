package lan

import (
	"bytes"
	"image"
	"testing"
)

func TestRefineDoesNotHideNewVideo(t *testing.T) {
	var o refineOverlay
	o.reset(128, 64)
	video := bytes.Repeat([]byte{100, 100, 100, 255}, 128*64)
	r := image.Rect(0, 0, 128, 64)
	repair := bytes.Repeat([]byte{110, 110, 110, 255}, 128*64)
	o.patch(r, repair)
	o.rememberVideo(r, video)
	// Small codec fluctuations must not discard still-screen sharpness.
	noisy := bytes.Clone(video)
	for i := 0; i < len(noisy); i += 4 {
		noisy[i] += 5
	}
	o.invalidateMotion(noisy)
	if o.count != 2 {
		t.Fatal("codec noise discarded repairs")
	}
	// A newly decoded scrolling tile becomes visible immediately, while
	// the unchanged tile retains its lossless repair.
	moving := bytes.Clone(video)
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			moving[(y*128+x)*4] = 200
		}
	}
	o.invalidateMotion(moving)
	composed := o.compose(moving)
	if o.count != 1 || composed[0] != 200 || composed[64*4] != 110 {
		t.Fatal("motion hidden or unchanged repair lost")
	}
	// A later stable-screen repair restores quality with a fresh baseline.
	o.patch(image.Rect(0, 0, 64, 64), bytes.Repeat([]byte{205, 100, 100, 255}, 64*64))
	o.rememberVideo(image.Rect(0, 0, 64, 64), moving)
	o.invalidateMotion(moving)
	if o.count != 2 || o.compose(moving)[0] != 205 {
		t.Fatal("fresh repair discarded")
	}
}

func BenchmarkRefineMotionStatic1080p(b *testing.B) {
	var o refineOverlay
	o.reset(1920, 1080)
	video := make([]byte, 1920*1080*4)
	r := image.Rect(0, 0, 1920, 1080)
	o.patch(r, video)
	o.rememberVideo(r, video)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		o.invalidateMotion(video)
	}
}
