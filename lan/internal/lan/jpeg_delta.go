package lan

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/draw"
	"image/jpeg"
	"runtime"
	"sync"
	"time"
)

const jpegTileSize = 64

// The entire update is one sequenced frame: missing any fragment invalidates
// the update, rather than silently leaving stale tiles in the desktop.
type jpegDeltaEncoder struct {
	width, height, quality int
	previous               []byte
	lastFull, lastEmit     time.Time
}

func changedJPEGRects(current, previous []byte, w, h int) []image.Rectangle {
	var rects []image.Rectangle
	active := map[[2]int]int{}
	for y := 0; y < h; y += jpegTileSize {
		bottom := min(y+jpegTileSize, h)
		next := map[[2]int]int{}
		for x := 0; x < w; {
			start := x
			for x < w {
				right := min(x+jpegTileSize, w)
				changed := false
				for row := y; row < bottom; row++ {
					a, b := (row*w+x)*4, (row*w+right)*4
					if !bytes.Equal(current[a:b], previous[a:b]) {
						changed = true
						break
					}
				}
				if !changed {
					break
				}
				x = right
			}
			if x > start {
				span := [2]int{start, x}
				if i, ok := active[span]; ok {
					rects[i].Max.Y = bottom
					next[span] = i
				} else {
					next[span] = len(rects)
					rects = append(rects, image.Rect(start, y, x, bottom))
				}
			}
			x = min(x+jpegTileSize, w)
		}
		active = next
	}
	return rects
}

func bgraRectImage(pixels []byte, w int, r image.Rectangle) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	for y := 0; y < r.Dy(); y++ {
		src := pixels[((r.Min.Y+y)*w+r.Min.X)*4:]
		dst := img.Pix[y*img.Stride:]
		for x := 0; x < r.Dx()*4; x += 4 {
			dst[x], dst[x+1], dst[x+2], dst[x+3] = src[x+2], src[x+1], src[x], 255
		}
	}
	return img
}

func (e *jpegDeltaEncoder) encode(pixels []byte, now time.Time) ([]byte, bool, error) {
	if e.width < 1 || e.height < 1 || e.width*e.height > 16<<20 || len(pixels) != e.width*e.height*4 {
		return nil, false, errors.New("invalid JPEG capture dimensions")
	}
	full := len(e.previous) != len(pixels)
	var rects []image.Rectangle
	if !full {
		rects = changedJPEGRects(pixels, e.previous, e.width, e.height)
	}
	if !full && len(rects) == 0 {
		if now.Sub(e.lastEmit) < 500*time.Millisecond {
			return nil, false, nil
		}
	} else if !full {
		area := 0
		for _, r := range rects {
			area += r.Dx() * r.Dy()
		}
		full = area*100 >= e.width*e.height*60 || len(rects) > 64 || now.Sub(e.lastFull) >= 3*time.Second
	}
	if full {
		rects = []image.Rectangle{image.Rect(0, 0, e.width, e.height)}
	}
	encoded := make([][]byte, len(rects))
	var encodingErr error
	var mu sync.Mutex
	jobs := make(chan int, len(rects))
	for i := range rects {
		jobs <- i
	}
	close(jobs)
	var workers sync.WaitGroup
	for i := 0; i < min(len(rects), min(runtime.GOMAXPROCS(0), 4)); i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := range jobs {
				var out bytes.Buffer
				err := jpeg.Encode(&out, bgraRectImage(pixels, e.width, rects[i]), &jpeg.Options{Quality: e.quality})
				if err != nil {
					mu.Lock()
					encodingErr = err
					mu.Unlock()
				} else {
					encoded[i] = out.Bytes()
				}
			}
		}()
	}
	workers.Wait()
	if encodingErr != nil {
		return nil, false, encodingErr
	}
	var out bytes.Buffer
	out.WriteString("JDP1")
	_ = binary.Write(&out, binary.BigEndian, uint32(e.width))
	_ = binary.Write(&out, binary.BigEndian, uint32(e.height))
	_ = binary.Write(&out, binary.BigEndian, uint32(len(rects)))
	for i, r := range rects {
		for _, n := range []int{r.Min.X, r.Min.Y, r.Dx(), r.Dy(), len(encoded[i])} {
			_ = binary.Write(&out, binary.BigEndian, uint32(n))
		}
		out.Write(encoded[i])
	}
	if out.Len() > maxVideoFrame {
		return nil, false, errors.New("JPEG update exceeds frame limit")
	}
	if e.previous == nil {
		e.previous = make([]byte, len(pixels))
	}
	for _, r := range rects {
		for y := r.Min.Y; y < r.Max.Y; y++ {
			a, b := (y*e.width+r.Min.X)*4, (y*e.width+r.Max.X)*4
			copy(e.previous[a:b], pixels[a:b])
		}
	}
	if full {
		e.lastFull = now
	}
	e.lastEmit = now
	return out.Bytes(), full, nil
}

type jpegPatch struct {
	rect   image.Rectangle
	pixels []byte
}

// Validate/decode every patch before committing any of them to the canvas.
func decodeJPEGDelta(data []byte, w, h int, key bool) ([]jpegPatch, error) {
	bad := func() ([]jpegPatch, error) { return nil, errors.New("invalid JPEG regional update") }
	if len(data) < 16 || len(data) > maxVideoFrame || string(data[:4]) != "JDP1" || w < 1 || h < 1 || w*h > 16<<20 {
		return bad()
	}
	if uint64(binary.BigEndian.Uint32(data[4:8])) != uint64(w) || uint64(binary.BigEndian.Uint32(data[8:12])) != uint64(h) {
		return bad()
	}
	count := int(binary.BigEndian.Uint32(data[12:16]))
	if count > 64 || (key && count != 1) {
		return bad()
	}
	data = data[16:]
	patches := make([]jpegPatch, 0, count)
	area := 0
	for i := 0; i < count; i++ {
		if len(data) < 20 {
			return bad()
		}
		x, y, pw, ph, n := int(binary.BigEndian.Uint32(data[:4])), int(binary.BigEndian.Uint32(data[4:8])), int(binary.BigEndian.Uint32(data[8:12])), int(binary.BigEndian.Uint32(data[12:16])), int(binary.BigEndian.Uint32(data[16:20]))
		data = data[20:]
		if x > w || y > h || pw < 1 || ph < 1 || pw > w-x || ph > h-y || n < 1 || n > len(data) {
			return bad()
		}
		r := image.Rect(x, y, x+pw, y+ph)
		if key && r != image.Rect(0, 0, w, h) {
			return bad()
		}
		area += pw * ph
		if area > w*h {
			return bad()
		}
		for _, p := range patches {
			if r.Overlaps(p.rect) {
				return bad()
			}
		}
		cfg, err := jpeg.DecodeConfig(bytes.NewReader(data[:n]))
		if err != nil || cfg.Width != pw || cfg.Height != ph {
			return bad()
		}
		img, err := jpeg.Decode(bytes.NewReader(data[:n]))
		if err != nil {
			return nil, err
		}
		rgba := image.NewRGBA(image.Rect(0, 0, pw, ph))
		draw.Draw(rgba, rgba.Bounds(), img, img.Bounds().Min, draw.Src)
		for j := 0; j < len(rgba.Pix); j += 4 {
			rgba.Pix[j], rgba.Pix[j+2] = rgba.Pix[j+2], rgba.Pix[j]
		}
		patches = append(patches, jpegPatch{rect: r, pixels: rgba.Pix})
		data = data[n:]
	}
	if len(data) != 0 {
		return bad()
	}
	return patches, nil
}

func applyJPEGPatches(canvas []byte, w int, patches []jpegPatch) {
	for _, p := range patches {
		for y := 0; y < p.rect.Dy(); y++ {
			dst := ((p.rect.Min.Y+y)*w + p.rect.Min.X) * 4
			src := y * p.rect.Dx() * 4
			copy(canvas[dst:dst+p.rect.Dx()*4], p.pixels[src:src+p.rect.Dx()*4])
		}
	}
}
