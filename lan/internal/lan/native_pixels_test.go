package lan

import (
	"bytes"
	"testing"
)

func TestNativeNV12StrideCropAndBounds(t *testing.T) {
	const w, h, stride, storageH = 128, 66, 144, 80
	src := bytes.Repeat([]byte{0xcc}, stride*storageH*3/2)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			src[y*stride+x] = byte(y + x)
		}
	}
	for y := 0; y < h/2; y++ {
		for x := 0; x < w; x++ {
			src[stride*storageH+y*stride+x] = byte(100 + y + x)
		}
	}
	dst := make([]byte, pixelNV12.size(w, h))
	if err := copyNV12Rows(dst, src, w, h, stride, storageH); err != nil {
		t.Fatal(err)
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if dst[y*w+x] != byte(y+x) {
				t.Fatal("luma padding/crop")
			}
		}
	}
	for y := 0; y < h/2; y++ {
		for x := 0; x < w; x++ {
			if dst[w*h+y*w+x] != byte(100+y+x) {
				t.Fatal("chroma plane starts at visible height instead of storage height")
			}
		}
	}
	for _, test := range []struct {
		data []byte
		s, h int
	}{{src, 127, 80}, {src, 144, 64}, {src[:100], 144, 80}, {src, 144, 79}} {
		if copyNV12Rows(dst, test.data, w, h, test.s, test.h) == nil {
			t.Fatal("invalid native layout accepted")
		}
	}
}

func TestNativeColourNormalizerContract(t *testing.T) {
	for _, c := range []h264Color{{matrix: 6}, {matrix: 6, full: true}, {matrix: 1}, {matrix: 1, full: true}} {
		b := bytes.Repeat([]byte{128}, 128*64*3/2)
		black, white := byte(16), byte(235)
		if c.full {
			black, white = 0, 255
		}
		for y := 0; y < 64; y++ {
			for x := 0; x < 128; x++ {
				if x < 64 {
					b[y*128+x] = black
				} else {
					b[y*128+x] = white
				}
			}
		}
		before := bytes.Clone(b)
		var n nv12Normalizer
		n.normalize(b, 128, 64, c)
		if !c.full && c.matrix == 6 && !bytes.Equal(before, b) {
			t.Fatal("common normalized planes changed")
		}
		for _, point := range []struct{ x, want int }{{20, 0}, {100, 255}} {
			r, g, blue := nv12RGB(b, 128, 64, point.x, 32)
			if absInt(r-point.want) > 1 || absInt(g-point.want) > 1 || absInt(blue-point.want) > 1 {
				t.Fatal("range/neutral colour changed", c, r, g, blue)
			}
		}
	}
	for _, nal := range [][]byte{nil, {7}, {7, 0, 0, 0}, {0x67, 100, 0, 31}, {1, 2, 3, 4}, bytes.Repeat([]byte{0}, 65537)} {
		if _, _, _, err := parseH264Color(nal); err == nil {
			t.Fatal("invalid/truncated SPS accepted")
		}
	}
}
