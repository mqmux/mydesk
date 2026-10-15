package lan

import (
	"errors"
	"math"
)

// Matrix/range are properties of encoded samples, not display resolution.
// Unspecified H.264 matrix uses the same BT.601 default as the old raw path.
type h264Color struct {
	matrix uint8
	full   bool
}

func copyNV12Rows(dst, src []byte, w, h, stride, storageH int) error {
	if len(dst) != pixelNV12.size(w, h) || len(dst) == 0 || stride < w || stride > 32768 || storageH < h || storageH > 16384 || storageH%2 != 0 || len(src) < stride*storageH*3/2 {
		return errors.New("invalid native NV12 layout")
	}
	for y := 0; y < h; y++ {
		copy(dst[y*w:(y+1)*w], src[y*stride:y*stride+w])
	}
	for y := 0; y < h/2; y++ {
		copy(dst[w*h+y*w:w*h+(y+1)*w], src[stride*storageH+y*stride:stride*storageH+y*stride+w])
	}
	return nil
}

// Keep the viewer's existing BT.601-limited/left-centred NV12 contract. The
// common unspecified/601-limited route is a no-op; no RGB round trip occurs.
type nv12Normalizer struct {
	color h264Color
	ready bool
	coeff [3][3][256]int32
}

func (n *nv12Normalizer) init(c h264Color) {
	n.color, n.ready = c, true
	kr, kb := 0.299, 0.114
	if c.matrix == 1 {
		kr, kb = 0.2126, 0.0722
	}
	kg := 1 - kr - kb
	yScale, cScale, yOffset := 219.0, 224.0, 16.0
	if c.full {
		yScale, cScale, yOffset = 255, 255, 0
	}
	// RGB reconstruction, followed by BT.601 limited-range YUV projection.
	m := [3][3]float64{{1, 0, 2 * (1 - kr)}, {1, -2 * kb * (1 - kb) / kg, -2 * kr * (1 - kr) / kg}, {1, 2 * (1 - kb), 0}}
	out := [3][3]float64{{.299, .587, .114}, {-.299 / (2 * (1 - .114)), -.587 / (2 * (1 - .114)), .5}, {.5, -.587 / (2 * (1 - .299)), -.114 / (2 * (1 - .299))}}
	for row := 0; row < 3; row++ {
		for col := 0; col < 3; col++ {
			factor := 0.0
			for j := 0; j < 3; j++ {
				factor += out[row][j] * m[j][col]
			}
			scale, offset := cScale, 128.0
			if col == 0 {
				scale, offset = yScale, yOffset
			}
			outScale := 224.0
			if row == 0 {
				outScale = 219
			}
			for value := 0; value < 256; value++ {
				n.coeff[row][col][value] = int32(math.Round((float64(value) - offset) * factor * outScale / scale * 65536))
			}
		}
	}
}

func (n *nv12Normalizer) normalize(b []byte, w, h int, c h264Color) {
	if !c.full && c.matrix != 1 {
		return
	}
	if !n.ready || n.color != c {
		n.init(c)
	}
	yuv := func(row, y, u, v int) byte {
		offset := 128
		if row == 0 {
			offset = 16
		}
		value := int((n.coeff[row][0][y]+n.coeff[row][1][u]+n.coeff[row][2][v]+32768)>>16) + offset
		return byte(max(0, min(255, value)))
	}
	base := w * h
	if c.matrix != 1 {
		// Range-only conversion has no cross-plane terms.
		for i := 0; i < base; i++ {
			b[i] = yuv(0, int(b[i]), 128, 128)
		}
	} else {
		cw, ch := w/2, h/2
		for y := 0; y < h; y++ {
			y0 := (2*y - 1) >> 2
			fy := (2*y - 1) & 3
			y1 := min(y0+1, ch-1)
			y0 = max(0, y0)
			for x := 0; x < w; x++ {
				x0, fx := x/2, (x%2)*2
				x1 := min(x0+1, cw-1)
				chroma := func(c int) int {
					a, z := int(b[base+y0*w+x0*2+c]), int(b[base+y0*w+x1*2+c])
					d, e := int(b[base+y1*w+x0*2+c]), int(b[base+y1*w+x1*2+c])
					return (a*(4-fx)*(4-fy) + z*fx*(4-fy) + d*(4-fx)*fy + e*fx*fy + 8) >> 4
				}
				b[y*w+x] = yuv(0, int(b[y*w+x]), chroma(0), chroma(1))
			}
		}
	}
	for i := base; i < len(b); i += 2 {
		u, v := int(b[i]), int(b[i+1])
		b[i], b[i+1] = yuv(1, 16, u, v), yuv(2, 16, u, v)
	}
}

type h264Bits struct {
	b   []byte
	at  int
	bad bool
}

func (r *h264Bits) bits(n int) uint32 {
	if n < 0 || n > 32 || r.at+n > len(r.b)*8 {
		r.bad = true
		return 0
	}
	var v uint32
	for i := 0; i < n; i++ {
		v = v<<1 | uint32(r.b[r.at/8]>>(7-r.at%8)&1)
		r.at++
	}
	return v
}
func (r *h264Bits) ue() uint32 {
	zeros := 0
	for !r.bad && r.bits(1) == 0 {
		zeros++
		if zeros > 24 {
			r.bad = true
			return 0
		}
	}
	return (1 << zeros) - 1 + r.bits(zeros)
}
func (r *h264Bits) se() int {
	v := r.ue()
	if v&1 != 0 {
		return int((v + 1) / 2)
	}
	return -int(v / 2)
}

func parseH264Color(nal []byte) (color h264Color, w, h int, result error) {
	color.matrix = 6
	if len(nal) < 4 || len(nal) > 65536 || nal[0]&31 != 7 {
		return color, 0, 0, errors.New("invalid native H.264 SPS")
	}
	rbsp := make([]byte, 0, len(nal)-1)
	zeros := 0
	for _, b := range nal[1:] {
		if zeros >= 2 && b == 3 {
			zeros = 0
			continue
		}
		rbsp = append(rbsp, b)
		if b == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}
	r := h264Bits{b: rbsp}
	profile := r.bits(8)
	r.bits(8)
	r.bits(8)
	r.ue()
	chroma := uint32(1)
	switch profile {
	case 100, 110, 122, 244, 44, 83, 86, 118, 128, 138, 139, 134, 135:
		chroma = r.ue()
		if chroma != 1 {
			return color, 0, 0, errors.New("MF native route supports 8-bit 4:2:0 only")
		}
		if r.ue() != 0 || r.ue() != 0 {
			return color, 0, 0, errors.New("MF native route requires 8-bit H.264")
		}
		r.bits(1)
		if r.bits(1) != 0 {
			for i := 0; i < 8; i++ {
				if r.bits(1) != 0 {
					last, next := 8, 8
					size := 16
					if i >= 6 {
						size = 64
					}
					for j := 0; j < size; j++ {
						if next != 0 {
							next = (last + r.se() + 256) % 256
						}
						if next != 0 {
							last = next
						}
					}
				}
			}
		}
	}
	r.ue()
	order := r.ue()
	if order == 0 {
		r.ue()
	} else if order == 1 {
		r.bits(1)
		r.se()
		r.se()
		count := r.ue()
		if count > 255 {
			r.bad = true
		} else {
			for i := uint32(0); i < count; i++ {
				r.se()
			}
		}
	} else if order > 2 {
		r.bad = true
	}
	r.ue()
	r.bits(1)
	mbW, mbH := r.ue()+1, r.ue()+1
	frameOnly := r.bits(1)
	if frameOnly == 0 {
		r.bits(1)
	}
	r.bits(1)
	var left, right, top, bottom uint32
	if r.bits(1) != 0 {
		left, right, top, bottom = r.ue(), r.ue(), r.ue(), r.ue()
	}
	w = int(mbW)*16 - int(left+right)*2
	h = int(mbH)*16*int(2-frameOnly) - int(top+bottom)*2*int(2-frameOnly)
	if left != 0 || top != 0 {
		return color, 0, 0, errors.New("MF native route does not support nonzero left/top cropping")
	}
	if frameOnly == 0 {
		return color, 0, 0, errors.New("MF native route requires progressive desktop pictures")
	}
	if r.bits(1) != 0 {
		if r.bits(1) != 0 {
			if r.bits(8) == 255 {
				r.bits(16)
				r.bits(16)
			}
		}
		if r.bits(1) != 0 {
			r.bits(1)
		}
		if r.bits(1) != 0 {
			r.bits(3)
			color.full = r.bits(1) != 0
			if r.bits(1) != 0 {
				r.bits(8)
				r.bits(8)
				matrix := r.bits(8)
				switch matrix {
				case 1:
					color.matrix = 1
				case 2, 5, 6:
					color.matrix = 6
				default:
					return color, 0, 0, errors.New("unsupported native H.264 colour matrix")
				}
			}
		}
		if r.bits(1) != 0 {
			if r.ue() != 0 || r.ue() != 0 {
				return color, 0, 0, errors.New("unsupported native H.264 chroma location")
			}
		}
	}
	if r.bad || w < 1 || h < 1 || mbW > 1024 || mbH > 1024 || pixelNV12.size(w, h) == 0 {
		return color, 0, 0, errors.New("truncated or invalid native H.264 SPS")
	}
	return color, w, h, nil
}
