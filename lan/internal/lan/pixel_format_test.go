package lan

import (
	"bytes"
	"image"
	"testing"
)

func solidNV12(w, h int, y, u, v byte) []byte {
	b := make([]byte, pixelNV12.size(w, h))
	for i := 0; i < w*h; i++ {
		b[i] = y
	}
	for i := w * h; i < len(b); i += 2 {
		b[i], b[i+1] = u, v
	}
	return b
}

func TestNV12DimensionsAndColours(t *testing.T) {
	if pixelNV12.size(1920, 1080) != 3110400 || pixelBGRA.size(1920, 1080) != 8294400 {
		t.Fatal("wrong plane sizes")
	}
	for _, size := range [][2]int{{1, 2}, {2, 1}, {0, 0}, {16384, 16384}, {-2, 8}} {
		if pixelNV12.size(size[0], size[1]) != 0 {
			t.Fatal("accepted invalid NV12 dimensions", size)
		}
	}
	for _, c := range []struct {
		y, u, v byte
		rgb     [3]byte
	}{
		{16, 128, 128, [3]byte{0, 0, 0}}, {235, 128, 128, [3]byte{255, 255, 255}},
		{126, 128, 128, [3]byte{128, 128, 128}}, {81, 90, 240, [3]byte{255, 0, 0}},
		{145, 54, 34, [3]byte{0, 255, 1}}, {41, 240, 110, [3]byte{0, 0, 255}},
	} {
		got := nv12ToBGRA(nil, solidNV12(8, 4, c.y, c.u, c.v), 8, 4)
		want := []byte{c.rgb[2], c.rgb[1], c.rgb[0], 255}
		for i := 0; i < len(got); i += 4 {
			if !bytes.Equal(got[i:i+4], want) {
				t.Fatalf("YUV %d,%d,%d = %v, want %v", c.y, c.u, c.v, got[i:i+4], want)
			}
		}
	}
}

func TestNV12RepairMotionNoiseAndFormatTransition(t *testing.T) {
	const w, h = 130, 66 // Includes partial right/bottom tiles.
	base := solidNV12(w, h, 110, 128, 128)
	var o refineOverlay
	o.reset(w, h)
	r := image.Rect(0, 0, w, h)
	o.patch(r, bytes.Repeat([]byte{180, 180, 180, 255}, w*h))
	o.rememberSurface(r, base, pixelNV12)
	noise := bytes.Clone(base)
	for i := range noise {
		noise[i]++
	}
	o.invalidateSurface(noise, pixelNV12)
	if o.count != len(o.valid) {
		t.Fatal("small codec noise removed static repairs")
	}
	moving := bytes.Clone(base)
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			moving[y*w+x] = 200
		}
	}
	// Chroma-only motion on a different tile must not leave stale RGB repair.
	for y := 4; y < 28; y++ {
		for x := 64; x < 128; x += 2 {
			moving[w*h+y*w+x] = 200
		}
	}
	o.invalidateSurface(moving, pixelNV12)
	if o.valid[0] || o.valid[1] || !o.valid[2] || !o.valid[3] || o.count != 4 {
		t.Fatalf("motion invalidated wrong tiles: %v", o.valid)
	}
	// A conversion-format fallback retains untouched lossless tiles.
	bgra := nv12ToBGRA(nil, moving, w, h)
	o.invalidateSurface(bgra, pixelBGRA)
	if o.count != 4 || o.baselineFormat != pixelBGRA {
		t.Fatal("format transition lost unchanged repairs")
	}
	// Sparse baseline refresh does not rebase other already repaired tiles.
	o.patch(image.Rect(0, 0, 64, 64), bytes.Repeat([]byte{190, 190, 190, 255}, 64*64))
	o.rememberSurface(image.Rect(0, 0, 64, 64), bgra, pixelBGRA)
	bgra[(64*w+2)*4] = 255
	bgra[(64*w+4)*4] = 255
	bgra[(64*w+6)*4] = 255
	bgra[(64*w+8)*4] = 255
	o.invalidateSurface(bgra, pixelBGRA)
	if o.valid[3] || !o.valid[0] {
		t.Fatal("motion after format transition not handled")
	}
}

func BenchmarkNV12StaticRepairs1080(b *testing.B) {
	const w, h = 1920, 1080
	video := solidNV12(w, h, 110, 128, 128)
	var o refineOverlay
	o.reset(w, h)
	r := image.Rect(0, 0, w, h)
	o.patch(r, make([]byte, w*h*4))
	o.rememberSurface(r, video, pixelNV12)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		o.invalidateSurface(video, pixelNV12)
	}
}

func TestNV12RepairHaloIndependentAcrossTileRefresh(t *testing.T) {
	const w, h = 128, 128
	video := solidNV12(w, h, 110, 128, 128)
	var o refineOverlay
	o.reset(w, h)
	o.patch(image.Rect(0, 0, w, h), make([]byte, w*h*4))
	o.rememberSurface(image.Rect(0, 0, w, h), video, pixelNV12)
	// The row immediately ABOVE a fresh lower patch has changed. Its halo
	// must be refreshed, without erasing the upper patch's motion baseline.
	for x := 0; x < w; x += 2 {
		video[w*h+31*w+x] = 240
	}
	o.rememberSurface(image.Rect(0, 64, w, h), video, pixelNV12)
	o.invalidateSurface(video, pixelNV12)
	if o.valid[0] || o.valid[1] || !o.valid[2] || !o.valid[3] {
		t.Fatal("fresh halo or neighbouring motion history lost", o.valid)
	}
	// A subsequent RGB fallback must use the same tile-specific halo.
	o.invalidateSurface(nv12ToBGRA(nil, video, w, h), pixelBGRA)
	if !o.valid[2] || !o.valid[3] {
		t.Fatal("format fallback lost fresh tile halo")
	}
}
