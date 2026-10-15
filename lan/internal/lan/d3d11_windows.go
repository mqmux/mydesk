//go:build windows && amd64

package lan

import (
	_ "embed"
	"fmt"
	"runtime"
	"syscall"
	"time"
	"unsafe"
)

//go:embed display.hlsl
var displayShader string

type d3dSample struct{ Count, Quality uint32 }
type d3dTextureDesc struct {
	Width, Height, Mips, ArraySize, Format uint32
	Sample                                 d3dSample
	Usage, Bind, CPUAccess, Misc           uint32
}
type d3dBufferDesc struct{ Size, Usage, Bind, CPUAccess, Misc, Stride uint32 }
type d3dViewport struct{ X, Y, Width, Height, MinDepth, MaxDepth float32 }
type d3dBox struct{ Left, Top, Front, Right, Bottom, Back uint32 }
type d3dMapped struct {
	Data                 uintptr
	RowPitch, DepthPitch uint32
}
type dxgiMode struct{ Width, Height, RateNum, RateDen, Format, Scanline, Scaling uint32 }
type dxgiSwapDesc struct {
	Mode          dxgiMode
	Sample        d3dSample
	Usage, Count  uint32
	Window        uintptr
	Windowed      int32
	Effect, Flags uint32
}
type displayConstants struct {
	SourceW, SourceH, TargetW, TargetH uint32
	Mode, Repairs, Sharp, Reserved     uint32
	X, Y, PadX, PadY                   uint32
}

type gpuTexture struct {
	texture, srv, rtv uintptr
	w, h              int
}

func releaseCOM(p *uintptr) {
	if *p != 0 {
		comCall(*p, 2)
		*p = 0
	}
}
func (t *gpuTexture) close() {
	releaseCOM(&t.rtv)
	releaseCOM(&t.srv)
	releaseCOM(&t.texture)
}
func d3dError(hr uintptr) error {
	if int32(hr) < 0 {
		return fmt.Errorf("D3D11 HRESULT 0x%08x", uint32(hr))
	}
	return nil
}

// Only accessed by the viewer's pinned UI thread. This stage deliberately
// accepts either BGRA or normalized NV12, converting NV12 at source resolution
// on the GPU. No CUDA, CGo or extra distributed DLL required.
type d3d11Renderer struct {
	device, context, swap, target     uintptr
	vertex, scale, enhance, constants uintptr
	convertNV12                       uintptr
	source                            [3]gpuTexture
	luma, chroma                      [3]gpuTexture
	repair, mask, scaled              gpuTexture
	coordinates                       [2]gpuTexture
	hasRepairs, occluded              bool
	sourceIndex, sw, sh, cw, ch       int
	baseRevision                      uint64
	sourceFormat                      frameFormat
	importPool                        *sharedGPUPool
	imports                           map[uintptr]*importedGPUTexture
	nativeBaseline                    gpuTexture
	nativeMask                        [2]gpuTexture
	nativeMaskIndex                   int
	invalidateGPU                     uintptr
}

func newD3D11(hwnd uintptr, cw, ch, sw, sh int) (_ *d3d11Renderer, err error) {
	r := &d3d11Renderer{sw: sw, sh: sh, cw: cw, ch: ch}
	defer func() {
		if err != nil {
			r.close()
		}
	}()
	proc := syscall.NewLazyDLL("d3d11.dll").NewProc("D3D11CreateDeviceAndSwapChain")
	if err = proc.Find(); err != nil {
		return nil, err
	}
	desc := dxgiSwapDesc{Mode: dxgiMode{Width: uint32(cw), Height: uint32(ch), Format: 87}, Sample: d3dSample{Count: 1}, Usage: 0x20, Count: 2, Window: hwnd, Windowed: 1, Effect: 3}
	var descPtr *dxgiSwapDesc
	var swapPtr *uintptr
	if hwnd != 0 {
		descPtr, swapPtr = &desc, &r.swap
	}
	levels := [3]uint32{0xb000, 0xa100, 0xa000}
	hr, _, _ := proc.Call(0, 1, 0, 0x20, uintptr(unsafe.Pointer(&levels[0])), uintptr(len(levels)), 7, uintptr(unsafe.Pointer(descPtr)), uintptr(unsafe.Pointer(swapPtr)), uintptr(unsafe.Pointer(&r.device)), 0, uintptr(unsafe.Pointer(&r.context)))
	if err = d3dError(hr); err != nil {
		return nil, err
	}
	// Bound GPU presentation queue depth. This does not discard codec frames.
	iidDevice1 := guid{0x77db970f, 0x6276, 0x48ba, [8]byte{0xba, 0xa1, 0x07, 0x01, 0x43, 0xb4, 0x39, 0x2c}}
	var dxgi uintptr
	hr, _, _ = comCall(r.device, 0, uintptr(unsafe.Pointer(&iidDevice1)), uintptr(unsafe.Pointer(&dxgi)))
	if d3dError(hr) == nil {
		comCall(dxgi, 12, 1) // IDXGIDevice1::SetMaximumFrameLatency
		releaseCOM(&dxgi)
	}
	if r.swap != 0 {
		// Own F11 fullscreen policy; DXGI must not intercept Alt+Enter.
		iidFactory := guid{0x7b7166ec, 0x21c7, 0x44ae, [8]byte{0xb2, 0x1a, 0xc9, 0xae, 0x32, 0x1a, 0xe3, 0x69}}
		var factory uintptr
		hr, _, _ = comCall(r.swap, 6, uintptr(unsafe.Pointer(&iidFactory)), uintptr(unsafe.Pointer(&factory)))
		if d3dError(hr) == nil {
			comCall(factory, 8, hwnd, 2)
			releaseCOM(&factory)
		}
		if err = r.backBuffer(); err != nil {
			return nil, err
		}
	}
	if r.vertex, err = r.shader("Vertex", "vs_4_0", 12); err != nil {
		return nil, err
	}
	if r.scale, err = r.shader("Scale", "ps_4_0", 15); err != nil {
		return nil, err
	}
	if r.enhance, err = r.shader("Enhance", "ps_4_0", 15); err != nil {
		return nil, err
	}
	bd := d3dBufferDesc{Size: uint32(unsafe.Sizeof(displayConstants{})), Bind: 4}
	hr, _, _ = comCall(r.device, 3, uintptr(unsafe.Pointer(&bd)), 0, uintptr(unsafe.Pointer(&r.constants)))
	if err = d3dError(hr); err != nil {
		return nil, err
	}
	for i := range r.source {
		if r.source[i], err = r.texture(sw, sh, 87, false); err != nil {
			return nil, err
		}
	}
	if r.repair, err = r.texture(sw, sh, 87, false); err != nil {
		return nil, err
	}
	if r.mask, err = r.texture((sw+63)/64, (sh+63)/64, 61, false); err != nil {
		return nil, err
	}
	zeros := make([]byte, r.mask.w*r.mask.h)
	comCall(r.context, 48, r.mask.texture, 0, 0, uintptr(unsafe.Pointer(&zeros[0])), uintptr(r.mask.w), 0)
	runtime.KeepAlive(zeros)
	return r, nil
}

func (r *d3d11Renderer) shader(entry, profile string, slot int) (uintptr, error) {
	proc := syscall.NewLazyDLL("d3dcompiler_47.dll").NewProc("D3DCompile")
	if err := proc.Find(); err != nil {
		return 0, err
	}
	source := []byte(displayShader)
	e, _ := syscall.BytePtrFromString(entry)
	p, _ := syscall.BytePtrFromString(profile)
	var blob, diagnostics uintptr
	hr, _, _ := proc.Call(uintptr(unsafe.Pointer(&source[0])), uintptr(len(source)), 0, 0, 0, uintptr(unsafe.Pointer(e)), uintptr(unsafe.Pointer(p)), 0, 0, uintptr(unsafe.Pointer(&blob)), uintptr(unsafe.Pointer(&diagnostics)))
	runtime.KeepAlive(source)
	defer releaseCOM(&blob)
	defer releaseCOM(&diagnostics)
	if err := d3dError(hr); err != nil {
		if diagnostics != 0 {
			data, _, _ := comCall(diagnostics, 3)
			size, _, _ := comCall(diagnostics, 4)
			return 0, fmt.Errorf("%s: %w: %s", entry, err, unsafe.Slice((*byte)(unsafe.Pointer(data)), int(size)))
		}
		return 0, err
	}
	data, _, _ := comCall(blob, 3)
	size, _, _ := comCall(blob, 4)
	var shader uintptr
	hr, _, _ = comCall(r.device, slot, data, size, 0, uintptr(unsafe.Pointer(&shader)))
	return shader, d3dError(hr)
}

func (r *d3d11Renderer) texture(w, h int, format uint32, target bool) (t gpuTexture, err error) {
	if w < 1 || h < 1 || w > 16384 || h > 16384 || w*h > 16<<20 {
		return t, fmt.Errorf("invalid GPU texture %dx%d", w, h)
	}
	t.w, t.h = w, h
	d := d3dTextureDesc{Width: uint32(w), Height: uint32(h), Mips: 1, ArraySize: 1, Format: format, Sample: d3dSample{Count: 1}, Bind: 8}
	if target {
		d.Bind |= 0x20
	}
	hr, _, _ := comCall(r.device, 5, uintptr(unsafe.Pointer(&d)), 0, uintptr(unsafe.Pointer(&t.texture)))
	if err = d3dError(hr); err == nil {
		hr, _, _ = comCall(r.device, 7, t.texture, 0, uintptr(unsafe.Pointer(&t.srv)))
		err = d3dError(hr)
	}
	if err == nil && target {
		hr, _, _ = comCall(r.device, 9, t.texture, 0, uintptr(unsafe.Pointer(&t.rtv)))
		err = d3dError(hr)
	}
	if err != nil {
		t.close()
	}
	return
}

func (r *d3d11Renderer) backBuffer() error {
	iid := guid{0x6f15aaf2, 0xd208, 0x4e89, [8]byte{0x9a, 0xb4, 0x48, 0x95, 0x35, 0xd3, 0x4f, 0x9c}}
	var texture uintptr
	hr, _, _ := comCall(r.swap, 9, 0, uintptr(unsafe.Pointer(&iid)), uintptr(unsafe.Pointer(&texture)))
	if err := d3dError(hr); err != nil {
		return err
	}
	defer releaseCOM(&texture)
	hr, _, _ = comCall(r.device, 9, texture, 0, uintptr(unsafe.Pointer(&r.target)))
	return d3dError(hr)
}

func (r *d3d11Renderer) unbind() {
	views := [6]uintptr{}
	comCall(r.context, 8, 0, 6, uintptr(unsafe.Pointer(&views[0])))
	comCall(r.context, 33, 0, 0, 0)
}

func (r *d3d11Renderer) resize(cw, ch int) error {
	if cw == r.cw && ch == r.ch {
		return nil
	}
	r.unbind()
	releaseCOM(&r.target)
	hr, _, _ := comCall(r.swap, 13, 0, uintptr(cw), uintptr(ch), 0, 0)
	if err := d3dError(hr); err != nil {
		return err
	}
	r.cw, r.ch = cw, ch
	return r.backBuffer()
}

func (r *d3d11Renderer) pass(target, shader uintptr, views [5]uintptr, c displayConstants, x, y, w, h int) {
	r.passViews(target, shader, [6]uintptr{views[0], views[1], views[2], views[3], views[4]}, c, x, y, w, h)
}
func (r *d3d11Renderer) passViews(target, shader uintptr, views [6]uintptr, c displayConstants, x, y, w, h int) {
	r.unbind()
	comCall(r.context, 48, r.constants, 0, 0, uintptr(unsafe.Pointer(&c)), 0, 0)
	comCall(r.context, 16, 0, 1, uintptr(unsafe.Pointer(&r.constants)))
	comCall(r.context, 11, r.vertex, 0, 0)
	comCall(r.context, 9, shader, 0, 0)
	comCall(r.context, 24, 4) // triangle list; SV_VertexID needs no vertex buffer
	comCall(r.context, 8, 0, 6, uintptr(unsafe.Pointer(&views[0])))
	comCall(r.context, 33, 1, uintptr(unsafe.Pointer(&target)), 0)
	viewport := d3dViewport{X: float32(x), Y: float32(y), Width: float32(w), Height: float32(h), MaxDepth: 1}
	comCall(r.context, 44, 1, uintptr(unsafe.Pointer(&viewport)))
	comCall(r.context, 13, 3, 0)
	runtime.KeepAlive(c)
}

func (r *d3d11Renderer) render(s displaySnapshot, target uintptr, x, y, w, h int, smooth, pixel bool) (renderTimes, error) {
	var times renderTimes
	var native *gpuDisplayFrame
	if s.frame != nil && !s.wholeRefine {
		native, _ = s.frame.native.(*gpuDisplayFrame)
	}
	if (native == nil && (s.format.size(r.sw, r.sh) == 0 || len(s.pixels) != s.format.size(r.sw, r.sh))) || w < 1 || h < 1 {
		return times, fmt.Errorf("invalid display surface")
	}
	start := time.Now()
	r.unbind()
	changedVideo := r.baseRevision != s.baseRevision || r.baseRevision == 0 || r.sourceFormat != s.format
	if native != nil {
		if native.width != r.sw || native.height != r.sh {
			return times, fmt.Errorf("GPU frame dimensions differ from display")
		}
		if err := r.prepareNativeRepairs(); err != nil {
			return times, err
		}
	} else if r.importPool != nil {
		r.clearNativeState()
	}
	if changedVideo {
		r.sourceIndex = (r.sourceIndex + 1) % len(r.source)
		if native != nil {
			if err := r.importFrame(native, r.source[r.sourceIndex].texture); err != nil {
				return times, err
			}
		} else if s.format == pixelNV12 {
			if err := r.prepareNV12(); err != nil {
				return times, err
			}
			comCall(r.context, 48, r.luma[r.sourceIndex].texture, 0, 0, uintptr(unsafe.Pointer(&s.pixels[0])), uintptr(r.sw), 0)
			comCall(r.context, 48, r.chroma[r.sourceIndex].texture, 0, 0, uintptr(unsafe.Pointer(&s.pixels[r.sw*r.sh])), uintptr(r.sw), 0)
		} else {
			comCall(r.context, 48, r.source[r.sourceIndex].texture, 0, 0, uintptr(unsafe.Pointer(&s.pixels[0])), uintptr(r.sw*4), 0)
		}
		r.sourceFormat = s.format
		if s.format == pixelNV12 {
			c := displayConstants{SourceW: uint32(r.sw), SourceH: uint32(r.sh)}
			r.pass(r.source[r.sourceIndex].rtv, r.convertNV12, [5]uintptr{r.luma[r.sourceIndex].srv, r.chroma[r.sourceIndex].srv}, c, 0, 0, r.sw, r.sh)
		}
		r.baseRevision = s.baseRevision
	}
	if s.repairs.mask != nil {
		if len(s.repairs.mask) != r.mask.w*r.mask.h {
			return times, fmt.Errorf("invalid repair mask")
		}
		comCall(r.context, 48, r.mask.texture, 0, 0, uintptr(unsafe.Pointer(&s.repairs.mask[0])), uintptr(r.mask.w), 0)
		r.hasRepairs = false
		for _, valid := range s.repairs.mask {
			if valid != 0 {
				r.hasRepairs = true
				break
			}
		}
	}
	for _, p := range s.repairs.patches {
		box := d3dBox{Left: uint32(p.rect.Min.X), Top: uint32(p.rect.Min.Y), Right: uint32(p.rect.Max.X), Bottom: uint32(p.rect.Max.Y), Back: 1}
		comCall(r.context, 48, r.repair.texture, 0, uintptr(unsafe.Pointer(&box)), uintptr(unsafe.Pointer(&p.pixels[0])), uintptr(p.rect.Dx()*4), 0)
		if native != nil {
			r.rebaseNativeRepair(p.rect)
		}
	}
	if native != nil && r.hasRepairs && (changedVideo || s.repairs.mask != nil || len(s.repairs.patches) > 0) {
		r.checkNativeRepairs()
	}
	times.upload = time.Since(start)
	start = time.Now()
	black := [4]float32{0, 0, 0, 1}
	comCall(r.context, 50, target, uintptr(unsafe.Pointer(&black[0])))
	c := displayConstants{SourceW: uint32(r.sw), SourceH: uint32(r.sh), TargetW: uint32(w), TargetH: uint32(h), Mode: 2, Repairs: 1, X: uint32(x), Y: uint32(y)}
	if native != nil {
		c.Reserved = 1
	}
	if s.wholeRefine || !r.hasRepairs {
		c.Repairs = 0
	}
	if pixel || (!smooth && w <= r.sw && h <= r.sh && w == r.sw && h == r.sh) {
		c.Mode = 0
	}
	views := [6]uintptr{r.source[r.sourceIndex].srv, r.repair.srv, r.mask.srv}
	if native != nil {
		views[5] = r.nativeMask[r.nativeMaskIndex].srv
	}
	if !smooth && !pixel && (w > r.sw || h > r.sh) {
		if r.scaled.w != w || r.scaled.h != h {
			r.scaled.close()
			var err error
			if r.scaled, err = r.texture(w, h, 87, true); err != nil {
				return times, err
			}
			for axis, length := range [2]int{w, h} {
				r.coordinates[axis].close()
				if r.coordinates[axis], err = r.texture(length, 1, 42, false); err != nil {
					return times, err
				}
				source := [2]int{r.sw, r.sh}[axis]
				positions := make([]uint32, length)
				for i := range positions {
					positions[i] = uint32(i * (source - 1) * 256 / max(1, length-1))
				}
				comCall(r.context, 48, r.coordinates[axis].texture, 0, 0, uintptr(unsafe.Pointer(&positions[0])), uintptr(length*4), 0)
				runtime.KeepAlive(positions)
			}
		}
		views[3], views[4] = r.coordinates[0].srv, r.coordinates[1].srv
		c.Mode, c.X, c.Y = 1, 0, 0
		r.passViews(r.scaled.rtv, r.scale, views, c, 0, 0, w, h)
		c.X, c.Y, c.Repairs = uint32(x), uint32(y), 0
		if w*100 > r.sw*108 || h*100 > r.sh*108 {
			c.Sharp = 1
		}
		r.pass(target, r.enhance, [5]uintptr{r.scaled.srv, 0, 0, 0, 0}, c, x, y, w, h)
	} else {
		r.passViews(target, r.scale, views, c, x, y, w, h)
	}
	times.submit = time.Since(start)
	runtime.KeepAlive(s)
	hr, _, _ := comCall(r.device, 39) // GetDeviceRemovedReason
	return times, d3dError(hr)
}

func (r *d3d11Renderer) prepareNV12() error {
	if r.convertNV12 != 0 {
		return nil
	}
	var err error
	shader, err := r.shader("ConvertNV12", "ps_4_0", 15)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			releaseCOM(&shader)
		}
	}()
	for i := range r.source {
		if r.luma[i], err = r.texture(r.sw, r.sh, 61, false); err != nil {
			return err
		}
		if r.chroma[i], err = r.texture(r.sw/2, r.sh/2, 49, false); err != nil {
			return err
		}
		// BGRA cache becomes a native-size conversion target; only GPU writes
		// it in NV12 mode. Keep the rotating three-surface ownership scheme.
		r.source[i].close()
		if r.source[i], err = r.texture(r.sw, r.sh, 87, true); err != nil {
			return err
		}
	}
	r.convertNV12 = shader
	return nil
}

func (r *d3d11Renderer) draw(s displaySnapshot, cw, ch, x, y, w, h int, smooth, pixel bool) (renderTimes, bool, error) {
	if err := r.resize(cw, ch); err != nil {
		return renderTimes{}, false, err
	}
	times, err := r.render(s, r.target, x, y, w, h, smooth, pixel)
	if err != nil {
		return times, false, err
	}
	start := time.Now()
	// Never block the input/UI thread on a full presentation queue. The next
	// retry renders the latest decoded picture, not a FIFO of stale pictures.
	hr, _, _ := comCall(r.swap, 8, 0, 8) // Present(0, DXGI_PRESENT_DO_NOT_WAIT)
	times.submit += time.Since(start)
	r.occluded = uint32(hr) == 0x087a0001
	if uint32(hr) == 0x887a000a || uint32(hr) == 0x087a0001 {
		return times, false, nil
	}
	return times, d3dError(hr) == nil, d3dError(hr)
}

func (r *d3d11Renderer) close() {
	if r.context != 0 {
		comCall(r.context, 110)
	} // ClearState
	for i := range r.source {
		r.source[i].close()
		r.luma[i].close()
		r.chroma[i].close()
	}
	r.repair.close()
	r.mask.close()
	r.scaled.close()
	r.clearNativeState()
	for i := range r.coordinates {
		r.coordinates[i].close()
	}
	for _, p := range []*uintptr{&r.convertNV12, &r.constants, &r.enhance, &r.scale, &r.vertex, &r.target, &r.swap, &r.context, &r.device} {
		releaseCOM(p)
	}
}
