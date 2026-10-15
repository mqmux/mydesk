//go:build windows && amd64

package lan

import (
	"image"
	"time"
	"unsafe"
)

const repairResyncFeature = "repair-resync-v1"

func (r *d3d11Renderer) clearNativeState() {
	r.nativeBaseline.close()
	for i := range r.nativeMask {
		r.nativeMask[i].close()
	}
	for _, texture := range r.imports {
		texture.close()
	}
	r.imports = nil
	r.importPool = nil
	r.nativeMaskIndex = 0
	releaseCOM(&r.invalidateGPU)
}

func (r *d3d11Renderer) prepareNativeRepairs() error {
	if r.invalidateGPU != 0 {
		return nil
	}
	var err error
	if r.nativeBaseline, err = r.texture(r.sw, r.sh, 87, false); err != nil {
		return err
	}
	for i := range r.nativeMask {
		if r.nativeMask[i], err = r.texture(r.mask.w, r.mask.h, 61, true); err != nil {
			return err
		}
		zero := [4]float32{}
		comCall(r.context, 50, r.nativeMask[i].rtv, uintptr(unsafe.Pointer(&zero[0])))
	}
	r.invalidateGPU, err = r.shader("InvalidateGPURepairs", "ps_4_0", 15)
	return err
}

func (r *d3d11Renderer) rebaseNativeRepair(rect image.Rectangle) {
	box := d3dBox{Left: uint32(rect.Min.X), Top: uint32(rect.Min.Y), Right: uint32(rect.Max.X), Bottom: uint32(rect.Max.Y), Back: 1}
	comCall(r.context, 46, r.nativeBaseline.texture, 0, uintptr(rect.Min.X), uintptr(rect.Min.Y), 0, r.source[r.sourceIndex].texture, 0, uintptr(unsafe.Pointer(&box)))
	x0, y0, x1, y1 := rect.Min.X/64, rect.Min.Y/64, (rect.Max.X+63)/64, (rect.Max.Y+63)/64
	mask := make([]byte, (x1-x0)*(y1-y0))
	for i := range mask {
		mask[i] = 255
	}
	box = d3dBox{Left: uint32(x0), Top: uint32(y0), Right: uint32(x1), Bottom: uint32(y1), Back: 1}
	comCall(r.context, 48, r.nativeMask[r.nativeMaskIndex].texture, 0, uintptr(unsafe.Pointer(&box)), uintptr(unsafe.Pointer(&mask[0])), uintptr(x1-x0), 0)
}

func (r *d3d11Renderer) checkNativeRepairs() {
	next := 1 - r.nativeMaskIndex
	views := [6]uintptr{r.source[r.sourceIndex].srv, r.nativeBaseline.srv, r.mask.srv, 0, 0, r.nativeMask[r.nativeMaskIndex].srv}
	c := displayConstants{SourceW: uint32(r.sw), SourceH: uint32(r.sh)}
	r.passViews(r.nativeMask[next].rtv, r.invalidateGPU, views, c, 0, 0, r.mask.w, r.mask.h)
	r.nativeMaskIndex = next
}

func (v *viewer) setGPUFrame(frame *gpuDisplayFrame) {
	v.mu.Lock()
	if frame.width != v.width || frame.height != v.height || v.closed {
		v.mu.Unlock()
		frame.release()
		return
	}
	v.frames.publishNative(frame, time.Now())
	v.pixels = nil
	v.lastFrame = v.frames.sequence
	v.baseRevision++
	h := v.hwnd
	v.mu.Unlock()
	v.requestFramePaint(h)
}
