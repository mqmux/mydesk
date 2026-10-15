package lan

// NV12 output has an explicit local contract: 8-bit BT.601 limited range,
// left/vertically-centred chroma. FFmpeg normalizes tagged input before the
// raw pipe, which otherwise carries no colour metadata. No wire format change.
type frameFormat uint8

const (
	pixelBGRA frameFormat = iota
	pixelNV12
)

func (f frameFormat) size(w, h int) int {
	if w < 1 || h < 1 || w > 16384 || h > 16384 || w*h > 16<<20 {
		return 0
	}
	switch f {
	case pixelBGRA:
		return w * h * 4
	case pixelNV12:
		if w%2 == 0 && h%2 == 0 {
			return w * h * 3 / 2
		}
	}
	return 0
}

func (f frameFormat) String() string {
	if f == pixelNV12 {
		return "NV12"
	}
	return "BGRA"
}

// Match the shader's phase-correct bilinear chroma reconstruction, rather
// than treating each chroma sample as a flat 2x2 block with coloured edges.
func nv12RGB(b []byte, w, h, x, y int) (r, g, blue int) {
	x0, y0 := x/2, (2*y-1)>>2
	fx, fy := (x%2)*2, (2*y-1)&3
	cw, ch := w/2, h/2
	x1, y1 := min(x0+1, cw-1), min(y0+1, ch-1)
	x0, y0 = max(0, x0), max(0, y0)
	base := w * h
	chroma := func(c int) int {
		a := int(b[base+y0*w+x0*2+c])
		z := int(b[base+y0*w+x1*2+c])
		d := int(b[base+y1*w+x0*2+c])
		e := int(b[base+y1*w+x1*2+c])
		return (a*(4-fx)*(4-fy) + z*fx*(4-fy) + d*(4-fx)*fy + e*fx*fy + 8) >> 4
	}
	return yuv601RGB(int(b[y*w+x]), chroma(0), chroma(1))
}

func yuv601RGB(y, u, v int) (r, g, b int) {
	c, d, e := y-16, u-128, v-128
	r = min(255, max(0, (298*c+409*e+128)>>8))
	g = min(255, max(0, (298*c-100*d-208*e+128)>>8))
	b = min(255, max(0, (298*c+516*d+128)>>8))
	return
}

// Used only for a renderer failure while switching the decoder back to the
// old BGRA path. Normal GPU display never creates this full CPU RGB image.
func nv12ToBGRA(dst, src []byte, w, h int) []byte {
	if pixelNV12.size(w, h) == 0 || len(src) != pixelNV12.size(w, h) {
		return nil
	}
	if len(dst) != w*h*4 {
		dst = make([]byte, w*h*4)
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, b := nv12RGB(src, w, h, x, y)
			i := (y*w + x) * 4
			dst[i], dst[i+1], dst[i+2], dst[i+3] = byte(b), byte(g), byte(r), 255
		}
	}
	return dst
}
