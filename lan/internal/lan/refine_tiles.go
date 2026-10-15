package lan

import (
	"bytes"
	"image"
	"time"
)

// Keep lossless repairs for unchanged tiles instead of reverting the whole
// desktop to compressed video whenever one small region changes.
type refineTiles struct {
	width, height int
	previous      []byte
	changed       []time.Time
	valid         []bool
}

func (t *refineTiles) reset(w, h int) {
	t.width = w
	t.height = h
	n := ((w + 63) / 64) * ((h + 63) / 64)
	t.previous = nil
	t.changed = make([]time.Time, n)
	t.valid = make([]bool, n)
}
func (t *refineTiles) visit(r image.Rectangle, fn func(int)) {
	cols := (t.width + 63) / 64
	for y := r.Min.Y / 64; y < (r.Max.Y+63)/64; y++ {
		for x := r.Min.X / 64; x < (r.Max.X+63)/64; x++ {
			fn(y*cols + x)
		}
	}
}
func (t *refineTiles) observe(pixels []byte, now time.Time) []image.Rectangle {
	if len(t.previous) != len(pixels) {
		t.previous = bytes.Clone(pixels)
		for i := range t.changed {
			t.changed[i] = now
			t.valid[i] = false
		}
		return nil
	}
	rects := changedJPEGRects(pixels, t.previous, t.width, t.height)
	for _, r := range rects {
		t.visit(r, func(i int) { t.changed[i] = now; t.valid[i] = false })
	}
	copy(t.previous, pixels)
	return rects
}
func tileMaskRects(mask []bool, w, h int) []image.Rectangle {
	cols := (w + 63) / 64
	var rects []image.Rectangle
	active := map[[2]int]int{}
	for y := 0; y < h; y += 64 {
		next := map[[2]int]int{}
		for x := 0; x < w; {
			start := x
			for x < w && mask[(y/64)*cols+x/64] {
				x = min(x+64, w)
			}
			if x > start {
				span := [2]int{start, x}
				if i, ok := active[span]; ok {
					rects[i].Max.Y = min(y+64, h)
					next[span] = i
				} else {
					next[span] = len(rects)
					rects = append(rects, image.Rect(start, y, x, min(y+64, h)))
				}
			}
			x = min(x+64, w)
		}
		active = next
	}
	return rects
}
func (t *refineTiles) ready(now time.Time) []image.Rectangle {
	mask := make([]bool, len(t.valid))
	for i := range mask {
		mask[i] = !t.valid[i] && !t.changed[i].IsZero() && now.Sub(t.changed[i]) >= 400*time.Millisecond
	}
	return tileMaskRects(mask, t.width, t.height)
}

type refineOverlay struct {
	width, height     int
	pixels, composite []byte
	valid             []bool
	count             int
	videoBaseline     []byte
	baselineFormat    frameFormat
	chromaHalo        []byte // Independent preceding UV row for each tile.
	revision          uint64
	pixelDirty        []bool
	maskDirty         bool
}

func (o *refineOverlay) reset(w, h int) {
	o.width = w
	o.height = h
	o.pixels = make([]byte, w*h*4)
	o.valid = make([]bool, ((w+63)/64)*((h+63)/64))
	o.count = 0
	o.videoBaseline = nil
	o.chromaHalo = nil
	o.pixelDirty = make([]bool, len(o.valid))
	o.maskDirty = true
	o.revision++
}

// Ignore small codec noise, but invalidate stale repairs on decoded motion
// without waiting for the reliable repair channel's change notification.
func (o *refineOverlay) rememberVideo(r image.Rectangle, video []byte) {
	o.rememberSurface(r, video, pixelBGRA)
}
func (o *refineOverlay) rememberSurface(r image.Rectangle, video []byte, format frameFormat) {
	if format == pixelNV12 {
		if format.size(o.width, o.height) == 0 || len(video) != format.size(o.width, o.height) {
			return
		}
		if len(o.videoBaseline) != len(video) || o.baselineFormat != format {
			o.videoBaseline = bytes.Clone(video)
			o.baselineFormat = format
			o.rememberChromaHalo(image.Rect(0, 0, o.width, o.height), video)
		}
		for y := r.Min.Y; y < r.Max.Y; y++ {
			a, b := y*o.width+r.Min.X, y*o.width+r.Max.X
			copy(o.videoBaseline[a:b], video[a:b])
		}
		base := o.width * o.height
		for y := r.Min.Y / 2; y < (r.Max.Y+1)/2; y++ {
			a, b := base+y*o.width+r.Min.X, base+y*o.width+r.Max.X
			copy(o.videoBaseline[a:b], video[a:b])
		}
		o.rememberChromaHalo(r, video)
		return
	}
	if len(video) != len(o.pixels) {
		return
	}
	if len(o.videoBaseline) != len(video) || o.baselineFormat != format {
		o.videoBaseline = bytes.Clone(video)
		o.baselineFormat = format
	}
	for y := r.Min.Y; y < r.Max.Y; y++ {
		a, b := (y*o.width+r.Min.X)*4, (y*o.width+r.Max.X)*4
		copy(o.videoBaseline[a:b], video[a:b])
	}
}
func (o *refineOverlay) invalidateSurface(video []byte, format frameFormat) {
	if o.count == 0 || len(video) != format.size(o.width, o.height) || len(o.videoBaseline) == 0 {
		return
	}
	if format == pixelBGRA && o.baselineFormat == pixelBGRA {
		o.invalidateMotion(video)
		return
	}
	before := o.count
	cols := (o.width + 63) / 64
	for i, valid := range o.valid {
		if !valid {
			continue
		}
		x0, y0 := (i%cols)*64, (i/cols)*64
		changed := 0
		for y := y0; y < min(y0+64, o.height) && changed < 4; y += 2 {
			if format == pixelNV12 && o.baselineFormat == pixelNV12 {
				a, b := y*o.width+x0, y*o.width+min(x0+64, o.width)
				uv := o.width*o.height + (y/2)*o.width
				previousUV := max(o.width*o.height, uv-o.width)
				previousRow := o.videoBaseline[previousUV+x0 : previousUV+min(x0+64, o.width)]
				if y == y0 {
					previousRow = o.chromaHalo[i*64 : i*64+min(64, o.width-x0)]
				}
				if bytes.Equal(video[a:b], o.videoBaseline[a:b]) && bytes.Equal(video[uv+x0:uv+min(x0+64, o.width)], o.videoBaseline[uv+x0:uv+min(x0+64, o.width)]) && bytes.Equal(video[previousUV+x0:previousUV+min(x0+64, o.width)], previousRow) {
					continue
				}
			}
			for x := x0; x < min(x0+64, o.width) && changed < 4; x += 2 {
				if format == pixelNV12 && o.baselineFormat == pixelNV12 {
					p, uv := y*o.width+x, o.width*o.height+(y/2)*o.width+x
					// Compare in the native planes, including chroma-only motion.
					// RGB-equivalent deltas retain the old 24 / 4-sample tolerance;
					// no whole-frame conversion/copy is needed for static repairs.
					c := 298 * (int(video[p]) - int(o.videoBaseline[p]))
					previousUV := max(o.width*o.height+x, uv-o.width)
					// The existing sampling grid is even/even, so horizontal
					// chroma is exact and vertical chroma uses weights 1/4, 3/4.
					// Include the preceding UV row even at a tile boundary.
					chromaDelta := func(channel int) int {
						a := (int(video[previousUV+channel]) + 3*int(video[uv+channel]) + 2) >> 2
						previous := int(o.videoBaseline[previousUV+channel])
						if y == y0 {
							previous = int(o.chromaHalo[i*64+x-x0+channel])
						}
						b := (previous + 3*int(o.videoBaseline[uv+channel]) + 2) >> 2
						return a - b
					}
					d, e := chromaDelta(0), chromaDelta(1)
					if absInt(c+409*e) > 24*256 || absInt(c-100*d-208*e) > 24*256 || absInt(c+516*d) > 24*256 {
						changed++
					}
				} else {
					// Rare format transition (e.g. GPU failure): preserve static
					// repairs, but still invalidate moving regions on this frame.
					r, g, b := surfaceRGB(video, format, o.width, o.height, x, y)
					r0, g0, b0 := o.baselineRGB(x, y, i)
					if absInt(r-r0) > 24 || absInt(g-g0) > 24 || absInt(b-b0) > 24 {
						changed++
					}
				}
			}
		}
		if changed >= 4 {
			o.valid[i] = false
			o.count--
		}
	}
	if before != o.count {
		o.maskDirty = true
		o.revision++
	}
	if format != o.baselineFormat {
		o.videoBaseline = bytes.Clone(video)
		o.baselineFormat = format
		if format == pixelNV12 {
			o.rememberChromaHalo(image.Rect(0, 0, o.width, o.height), video)
		}
	}
}

// Chroma reconstruction crosses the preceding tile row. Keep that tiny halo
// per tile so refreshing one patch neither leaves its halo stale nor rebases
// a neighbouring patch's motion history. At 1080p this is only ~33 KiB.
func (o *refineOverlay) rememberChromaHalo(r image.Rectangle, video []byte) {
	if len(o.chromaHalo) != len(o.valid)*64 {
		o.chromaHalo = make([]byte, len(o.valid)*64)
	}
	cols := (o.width + 63) / 64
	t := refineTiles{width: o.width}
	t.visit(r, func(i int) {
		x, y := (i%cols)*64, (i/cols)*64
		a := o.width*o.height + max(0, y/2-1)*o.width + x
		copy(o.chromaHalo[i*64:i*64+min(64, o.width-x)], video[a:a+min(64, o.width-x)])
	})
}
func (o *refineOverlay) baselineRGB(x, y, tile int) (int, int, int) {
	if o.baselineFormat == pixelNV12 && y%64 == 0 {
		uv := o.width*o.height + (y/2)*o.width + x
		halo := tile*64 + x%64
		u := (int(o.chromaHalo[halo]) + 3*int(o.videoBaseline[uv]) + 2) >> 2
		v := (int(o.chromaHalo[halo+1]) + 3*int(o.videoBaseline[uv+1]) + 2) >> 2
		return yuv601RGB(int(o.videoBaseline[y*o.width+x]), u, v)
	}
	return surfaceRGB(o.videoBaseline, o.baselineFormat, o.width, o.height, x, y)
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
func surfaceRGB(b []byte, f frameFormat, w, h, x, y int) (int, int, int) {
	if f == pixelNV12 {
		return nv12RGB(b, w, h, x, y)
	}
	i := (y*w + x) * 4
	return int(b[i+2]), int(b[i+1]), int(b[i])
}
func (o *refineOverlay) invalidateMotion(video []byte) {
	if o.count == 0 || len(video) != len(o.videoBaseline) {
		return
	}
	before := o.count
	cols := (o.width + 63) / 64
	for i, valid := range o.valid {
		if !valid {
			continue
		}
		x0, y0 := (i%cols)*64, (i/cols)*64
		changed := 0
		for y := y0; y < min(y0+64, o.height) && changed < 4; y += 2 {
			a, b := (y*o.width+x0)*4, (y*o.width+min(x0+64, o.width))*4
			if bytes.Equal(video[a:b], o.videoBaseline[a:b]) {
				continue
			}
			for x := x0; x < min(x0+64, o.width) && changed < 4; x += 2 {
				p := (y*o.width + x) * 4
				for c := 0; c < 3; c++ {
					d := int(video[p+c]) - int(o.videoBaseline[p+c])
					if d > 24 || d < -24 {
						changed++
						break
					}
				}
			}
		}
		if changed >= 4 {
			o.valid[i] = false
			o.count--
		}
	}
	if before != o.count {
		o.maskDirty = true
		o.revision++
	}
}
func (o *refineOverlay) patch(r image.Rectangle, pixels []byte) bool {
	if r.Empty() || r.Min.X < 0 || r.Min.Y < 0 || r.Max.X > o.width || r.Max.Y > o.height || r.Min.X%64 != 0 || r.Min.Y%64 != 0 || (r.Max.X != o.width && r.Max.X%64 != 0) || (r.Max.Y != o.height && r.Max.Y%64 != 0) {
		return false
	}
	if pixels != nil && len(pixels) != r.Dx()*r.Dy()*4 {
		return false
	}
	if pixels != nil {
		applyJPEGPatches(o.pixels, o.width, []jpegPatch{{rect: r, pixels: pixels}})
	}
	t := refineTiles{width: o.width}
	t.visit(r, func(i int) {
		valid := pixels != nil
		if o.valid[i] != valid {
			if valid {
				o.count++
			} else {
				o.count--
			}
			o.valid[i] = valid
		}
		if pixels != nil {
			o.pixelDirty[i] = true
		}
	})
	o.maskDirty = true
	o.revision++
	return true
}
func (o *refineOverlay) compose(video []byte) []byte {
	if o.count == 0 || len(video) != len(o.pixels) {
		return video
	}
	if o.count == len(o.valid) {
		return o.pixels
	}
	if len(o.composite) != len(video) {
		o.composite = make([]byte, len(video))
	}
	copy(o.composite, video)
	for _, r := range tileMaskRects(o.valid, o.width, o.height) {
		for y := r.Min.Y; y < r.Max.Y; y++ {
			a, b := (y*o.width+r.Min.X)*4, (y*o.width+r.Max.X)*4
			copy(o.composite[a:b], o.pixels[a:b])
		}
	}
	return o.composite
}
