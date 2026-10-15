package lan

import (
	"bytes"
	"image"
	"runtime"
	"sync"
)

// Original sharp uses endpoint-aligned bilinear scaling, then a five-point
// edge enhancement (strength 52, threshold 6). Cache unchanged regions so
// high-rate duplicate desktop frames do not repeat this CPU work.
type sharpDisplay struct {
	sw, sh, dw, dh           int
	previous, scaled, output []byte
	xs, ys                   []int
}

func parallelRects(rects []image.Rectangle, fn func(image.Rectangle)) {
	jobs := make(chan image.Rectangle, 128)
	var wg sync.WaitGroup
	for i := 0; i < min(4, runtime.GOMAXPROCS(0)); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for r := range jobs {
				fn(r)
			}
		}()
	}
	for _, r := range rects {
		for y := r.Min.Y; y < r.Max.Y; y += 32 {
			jobs <- image.Rect(r.Min.X, y, r.Max.X, min(y+32, r.Max.Y))
		}
	}
	close(jobs)
	wg.Wait()
}
func (s *sharpDisplay) prepare(src []byte, sw, sh, dw, dh int) []byte {
	if sw < 1 || sh < 1 || dw < 1 || dh < 1 || len(src) != sw*sh*4 || dw*dh > 16<<20 {
		return src
	}
	reset := s.sw != sw || s.sh != sh || s.dw != dw || s.dh != dh
	var dirty []image.Rectangle
	if reset {
		s.sw = sw
		s.sh = sh
		s.dw = dw
		s.dh = dh
		s.previous = bytes.Clone(src)
		s.scaled = make([]byte, dw*dh*4)
		s.output = make([]byte, dw*dh*4)
		s.xs = make([]int, dw)
		s.ys = make([]int, dh)
		for x := range s.xs {
			if dw > 1 {
				s.xs[x] = x * (sw - 1) * 256 / (dw - 1)
			}
		}
		for y := range s.ys {
			if dh > 1 {
				s.ys[y] = y * (sh - 1) * 256 / (dh - 1)
			}
		}
		dirty = []image.Rectangle{image.Rect(0, 0, dw, dh)}
	} else {
		changes := changedJPEGRects(src, s.previous, sw, sh)
		if len(changes) == 0 {
			return s.output
		}
		copy(s.previous, src)
		cols := (dw + 63) / 64
		mask := make([]bool, cols*((dh+63)/64))
		for _, r := range changes {
			x0 := max(0, (r.Min.X-2)*(dw-1)/max(1, sw-1)-2)
			y0 := max(0, (r.Min.Y-2)*(dh-1)/max(1, sh-1)-2)
			x1 := min(dw, ((r.Max.X+2)*(dw-1)+max(1, sw-1)-1)/max(1, sw-1)+3)
			y1 := min(dh, ((r.Max.Y+2)*(dh-1)+max(1, sh-1)-1)/max(1, sh-1)+3)
			for y := y0 / 64; y < (y1+63)/64; y++ {
				for x := x0 / 64; x < (x1+63)/64; x++ {
					mask[y*cols+x] = true
				}
			}
		}
		dirty = tileMaskRects(mask, dw, dh)
	}
	parallelRects(dirty, func(r image.Rectangle) {
		for y := r.Min.Y; y < r.Max.Y; y++ {
			yf := s.ys[y]
			y0 := yf / 256
			y1 := min(y0+1, sh-1)
			fy := yf % 256
			wy := 256 - fy
			for x := r.Min.X; x < r.Max.X; x++ {
				xf := s.xs[x]
				x0 := xf / 256
				x1 := min(x0+1, sw-1)
				fx := xf % 256
				wx := 256 - fx
				a, b, c, d := (y0*sw+x0)*4, (y0*sw+x1)*4, (y1*sw+x0)*4, (y1*sw+x1)*4
				out := (y*dw + x) * 4
				for k := 0; k < 4; k++ {
					value := int(src[a+k])*wx*wy + int(src[b+k])*fx*wy + int(src[c+k])*wx*fy + int(src[d+k])*fx*fy
					s.scaled[out+k] = byte((value + 32768) >> 16)
				}
			}
		}
	})
	sharpen := dw*100 > sw*108 || dh*100 > sh*108
	parallelRects(dirty, func(r image.Rectangle) {
		for y := r.Min.Y; y < r.Max.Y; y++ {
			a, b := (y*dw+r.Min.X)*4, (y*dw+r.Max.X)*4
			copy(s.output[a:b], s.scaled[a:b])
			if !sharpen || y == 0 || y == dh-1 {
				continue
			}
			for x := max(1, r.Min.X); x < min(dw-1, r.Max.X); x++ {
				i := (y*dw + x) * 4
				for k := 0; k < 3; k++ {
					cur := int(s.scaled[i+k])
					edge := cur*4 - int(s.scaled[i-4+k]) - int(s.scaled[i+4+k]) - int(s.scaled[i-dw*4+k]) - int(s.scaled[i+dw*4+k])
					if edge > -6 && edge < 6 {
						continue
					}
					s.output[i+k] = byte(min(255, max(0, cur+edge*52/256)))
				}
				s.output[i+3] = 255
			}
		}
	})
	return s.output
}
