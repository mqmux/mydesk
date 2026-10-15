package lan

import (
	"bytes"
	"encoding/binary"
	"image"
	"testing"
	"time"
)

func testDesktop(w, h int) []byte {
	b := make([]byte, w*h*4)
	for i := 0; i < len(b); i += 4 {
		b[i], b[i+1], b[i+2], b[i+3] = 30, 80, 130, 255
	}
	return b
}
func paintTestRect(b []byte, w int, r image.Rectangle) {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			i := (y*w + x) * 4
			b[i], b[i+1], b[i+2] = 200, 170, 20
		}
	}
}
func TestJPEGRegionalEncodingAndStaticPreservation(t *testing.T) {
	const w, h = 640, 360
	b := testDesktop(w, h)
	e := jpegDeltaEncoder{width: w, height: h, quality: 90}
	now := time.Now()
	full, key, err := e.encode(b, now)
	if err != nil || !key {
		t.Fatalf("first frame must be full: %v", err)
	}
	patches, err := decodeJPEGDelta(full, w, h, true)
	if err != nil {
		t.Fatal(err)
	}
	canvas := make([]byte, len(b))
	applyJPEGPatches(canvas, w, patches)
	original := bytes.Clone(canvas)
	if data, _, err := e.encode(b, now.Add(20*time.Millisecond)); err != nil || data != nil {
		t.Fatal("unchanged screen sent an unnecessary frame")
	}
	paintTestRect(b, w, image.Rect(4, 4, 12, 12))
	paintTestRect(b, w, image.Rect(580, 320, 590, 330))
	delta, key, err := e.encode(b, now.Add(40*time.Millisecond))
	if err != nil || key {
		t.Fatalf("small changes became full frame: %v", err)
	}
	patches, err = decodeJPEGDelta(delta, w, h, false)
	if err != nil {
		t.Fatal(err)
	}
	area := 0
	for _, p := range patches {
		area += p.rect.Dx() * p.rect.Dy()
	}
	if len(patches) != 2 || area > w*h/10 || len(delta) >= len(full) {
		t.Fatalf("not regional: %d patches, %d pixels, %d/%d bytes", len(patches), area, len(delta), len(full))
	}
	applyJPEGPatches(canvas, w, patches)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			changed := false
			for _, p := range patches {
				if image.Pt(x, y).In(p.rect) {
					changed = true
				}
			}
			if !changed {
				i := (y*w + x) * 4
				if !bytes.Equal(canvas[i:i+4], original[i:i+4]) {
					t.Fatal("untouched background was re-encoded")
				}
			}
		}
	}
	heartbeat, key, err := e.encode(b, now.Add(600*time.Millisecond))
	if err != nil || key || len(heartbeat) != 16 {
		t.Fatalf("static keepalive is not small: %d %v", len(heartbeat), err)
	}
	if p, err := decodeJPEGDelta(heartbeat, w, h, false); err != nil || len(p) != 0 {
		t.Fatal("invalid keepalive")
	}
}
func TestJPEGRectMergeAndFullFallback(t *testing.T) {
	w, h := 192, 130
	b := testDesktop(w, h)
	previous := bytes.Clone(b)
	paintTestRect(b, w, image.Rect(1, 1, 100, 120))
	rects := changedJPEGRects(b, previous, w, h)
	if len(rects) != 1 || rects[0] != image.Rect(0, 0, 128, 128) {
		t.Fatalf("tile merge failed: %v", rects)
	}
	e := jpegDeltaEncoder{width: w, height: h, quality: 90}
	now := time.Now()
	_, _, _ = e.encode(previous, now)
	paintTestRect(b, w, image.Rect(0, 0, w, h))
	data, key, err := e.encode(b, now.Add(20*time.Millisecond))
	if err != nil || !key {
		t.Fatal("large change did not become full frame")
	}
	patches, err := decodeJPEGDelta(data, w, h, true)
	if err != nil || len(patches) != 1 || patches[0].rect != image.Rect(0, 0, w, h) {
		t.Fatal("full fallback invalid")
	}
}
func TestJPEGDeltaValidationAndReferenceRecovery(t *testing.T) {
	e := jpegDeltaEncoder{width: 128, height: 128, quality: 90}
	now := time.Now()
	b := testDesktop(128, 128)
	data, _, err := e.encode(b, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]byte{data[:15], append(bytes.Clone(data), 0), bytes.Clone(data)} {
		if len(bad) == len(data) {
			binary.BigEndian.PutUint32(bad[16:20], 129)
		}
		if _, err := decodeJPEGDelta(bad, 128, 128, true); err == nil {
			t.Fatal("malformed update accepted")
		}
	}
	paintTestRect(b, 128, image.Rect(1, 1, 2, 2))
	delta, key, _ := e.encode(b, now.Add(20*time.Millisecond))
	if key {
		t.Fatal("expected delta")
	}
	if _, err := decodeJPEGDelta(delta, 128, 128, true); err == nil {
		t.Fatal("partial frame accepted as full resync")
	}
	o := newFrameOrder()
	frame := func(id, epoch uint64, key bool) videoFrame {
		return videoFrame{id: id, epoch: epoch, key: key, codec: codecJPEGDelta}
	}
	if len(o.push(frame(2, 1, false))) != 0 {
		t.Fatal("delta accepted without baseline")
	}
	if len(o.push(frame(3, 1, true))) != 1 || len(o.push(frame(5, 1, false))) != 0 {
		t.Fatal("ordering/reference failed")
	}
	if ready := o.push(frame(4, 1, false)); len(ready) != 2 {
		t.Fatal("reordered patches lost")
	}
	o.reset()
	if len(o.push(frame(6, 1, false))) != 0 {
		t.Fatal("lost reference not enforced")
	}
	if len(o.push(frame(8, 2, true))) != 1 || len(o.push(frame(9, 1, false))) != 0 {
		t.Fatal("epoch resync failed")
	}
}

func BenchmarkJPEGSmallChange1080p(b *testing.B) {
	const w, h = 1920, 1080
	pixels := testDesktop(w, h)
	e := jpegDeltaEncoder{width: w, height: h, quality: 90}
	now := time.Now()
	_, _, _ = e.encode(pixels, now)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pixels[(500*w+500)*4] ^= 255
		if _, _, err := e.encode(pixels, now.Add(time.Duration(i%100+1)*time.Millisecond)); err != nil {
			b.Fatal(err)
		}
	}
}
