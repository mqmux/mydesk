package lan

import "image"

// Immutable updates: receiving patches cannot mutate an in-progress upload.
type overlayUpdate struct {
	revision uint64
	mask     []byte
	patches  []jpegPatch
}

func (o *refineOverlay) snapshot(full bool) overlayUpdate {
	u := overlayUpdate{revision: o.revision}
	if len(o.valid) == 0 {
		return u
	}
	if full || o.maskDirty {
		u.mask = make([]byte, len(o.valid))
		for i, valid := range o.valid {
			if valid {
				u.mask[i] = 255
			}
		}
	}
	dirty := o.pixelDirty
	if full {
		dirty = o.valid
	}
	for _, r := range tileMaskRects(dirty, o.width, o.height) {
		b := make([]byte, r.Dx()*r.Dy()*4)
		for y := r.Min.Y; y < r.Max.Y; y++ {
			copy(b[(y-r.Min.Y)*r.Dx()*4:], o.pixels[(y*o.width+r.Min.X)*4:(y*o.width+r.Max.X)*4])
		}
		u.patches = append(u.patches, jpegPatch{rect: r, pixels: b})
	}
	clear(o.pixelDirty)
	o.maskDirty = false
	return u
}

// UI-owned compatibility compositor; all work happens outside viewer.mu.
type displayOverlay struct {
	pixels, composite []byte
	mask              []bool
}

func (o *displayOverlay) update(u overlayUpdate, w, h int) {
	if u.mask == nil && len(u.patches) == 0 {
		return
	}
	if len(o.pixels) != w*h*4 {
		o.pixels = make([]byte, w*h*4)
		o.mask = make([]bool, ((w+63)/64)*((h+63)/64))
	}
	if u.mask != nil {
		for i := range o.mask {
			o.mask[i] = u.mask[i] != 0
		}
	}
	applyJPEGPatches(o.pixels, w, u.patches)
}

func (o *displayOverlay) compose(video []byte, w, h int) []byte {
	if len(o.mask) == 0 {
		return video
	}
	rects := tileMaskRects(o.mask, w, h)
	if len(rects) == 0 {
		return video
	}
	if len(rects) == 1 && rects[0] == image.Rect(0, 0, w, h) {
		return o.pixels
	}
	if len(o.composite) != len(video) {
		o.composite = make([]byte, len(video))
	}
	copy(o.composite, video)
	for _, r := range rects {
		for y := r.Min.Y; y < r.Max.Y; y++ {
			a, b := (y*w+r.Min.X)*4, (y*w+r.Max.X)*4
			copy(o.composite[a:b], o.pixels[a:b])
		}
	}
	return o.composite
}
