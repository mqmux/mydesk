//go:build windows && amd64

package lan

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"unsafe"
)

var texture2DIID = guid{0x6f15aaf2, 0xd208, 0x4e89, [8]byte{0x9a, 0xb4, 0x48, 0x95, 0x35, 0xd3, 0x4f, 0x9c}}
var dxgiBufferIID = guid{0xe7174cfa, 0x1c9e, 0x48b1, [8]byte{0x88, 0x66, 0x62, 0x62, 0x26, 0xbf, 0xc2, 0x58}}
var dxgiResourceIID = guid{0x035f3ab4, 0x482e, 0x4e50, [8]byte{0xb4, 0x1f, 0x8a, 0x7f, 0x8b, 0xd8, 0x96, 0x0b}}
var keyedMutexIID = guid{0x9d8e1289, 0xd7b3, 0x465f, [8]byte{0x81, 0x26, 0x25, 0x0e, 0x34, 0x9a, 0xf8, 0x5d}}
var errGPUNotReady = errors.New("GPU surface is not ready")

type planeViewDesc struct{ Format, Dimension, Mip, Levels, Pad0, Pad1 uint32 }
type sharedGPUSlot struct {
	image         gpuTexture
	mutex, handle uintptr
	key           uint64
}

func (s *sharedGPUSlot) close() { releaseCOM(&s.mutex); s.image.close() }

// No MFT-owned sample is retained by the UI. Three private shared RGB surfaces
// avoid starving the decoder's output/reference pool. Keyed mutexes fence GPU
// access across the two D3D11 devices without a whole-picture CPU readback.
type sharedGPUPool struct {
	mu     sync.Mutex
	free   chan *sharedGPUSlot
	closed bool
}

func (p *sharedGPUPool) give(s *sharedGPUSlot) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		s.close()
		return
	}
	p.free <- s
}
func (p *sharedGPUPool) close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.closed = true
	for len(p.free) > 0 {
		(<-p.free).close()
	}
}

type gpuDisplayFrame struct {
	pool          *sharedGPUPool
	slot          *sharedGPUSlot
	width, height int
	once          sync.Once
}

func (f *gpuDisplayFrame) size() (int, int) { return f.width, f.height }
func (f *gpuDisplayFrame) release()         { f.once.Do(func() { f.pool.give(f.slot) }) }

type textureDecoder struct {
	r                                 d3d11Renderer
	input                             uintptr
	y, uv, coefficients               gpuTexture
	pool                              *sharedGPUPool
	width, height, storageW, storageH int
	color                             h264Color
	normalizer                        nv12Normalizer
}

func newTextureDecoder(device uintptr, w, h int) (_ *textureDecoder, err error) {
	d := &textureDecoder{width: w, height: h, pool: &sharedGPUPool{free: make(chan *sharedGPUSlot, 3)}}
	defer func() {
		if err != nil {
			d.close()
		}
	}()
	d.r.device = device
	comCall(device, 1)
	comCall(device, 40, uintptr(unsafe.Pointer(&d.r.context)))
	if d.r.context == 0 {
		return nil, errors.New("GPU decoder has no immediate context")
	}
	if d.r.vertex, err = d.r.shader("Vertex", "vs_4_0", 12); err != nil {
		return nil, err
	}
	if d.r.convertNV12, err = d.r.shader("ConvertDirectNV12", "ps_4_0", 15); err != nil {
		return nil, err
	}
	bd := d3dBufferDesc{Size: uint32(unsafe.Sizeof(displayConstants{})), Bind: 4}
	hr, _, _ := comCall(device, 3, uintptr(unsafe.Pointer(&bd)), 0, uintptr(unsafe.Pointer(&d.r.constants)))
	if err = d3dError(hr); err != nil {
		return nil, err
	}
	if d.coefficients, err = d.r.texture(2304, 1, 42, false); err != nil {
		return nil, err
	}
	for i := 0; i < 3; i++ {
		s := &sharedGPUSlot{}
		desc := d3dTextureDesc{Width: uint32(w), Height: uint32(h), Mips: 1, ArraySize: 1, Format: 87, Sample: d3dSample{Count: 1}, Bind: 0x28, Misc: 0x100}
		hr, _, _ = comCall(device, 5, uintptr(unsafe.Pointer(&desc)), 0, uintptr(unsafe.Pointer(&s.image.texture)))
		if err = d3dError(hr); err != nil {
			s.close()
			return nil, err
		}
		s.image.w, s.image.h = w, h
		hr, _, _ = comCall(device, 9, s.image.texture, 0, uintptr(unsafe.Pointer(&s.image.rtv)))
		if err = d3dError(hr); err != nil {
			s.close()
			return nil, err
		}
		if err = mfCOM(s.image.texture, 0, uintptr(unsafe.Pointer(&keyedMutexIID)), uintptr(unsafe.Pointer(&s.mutex))); err != nil {
			s.close()
			return nil, err
		}
		var resource uintptr
		if err = mfCOM(s.image.texture, 0, uintptr(unsafe.Pointer(&dxgiResourceIID)), uintptr(unsafe.Pointer(&resource))); err != nil {
			s.close()
			return nil, err
		}
		err = mfCOM(resource, 8, uintptr(unsafe.Pointer(&s.handle)))
		releaseCOM(&resource)
		if err != nil || s.handle == 0 {
			s.close()
			return nil, errors.New("GPU texture cannot be shared")
		}
		d.pool.free <- s
	}
	return d, nil
}

func (d *textureDecoder) prepareInput(w, h int) error {
	if d.input != 0 && d.storageW == w && d.storageH == h {
		return nil
	}
	releaseCOM(&d.y.srv)
	releaseCOM(&d.uv.srv)
	releaseCOM(&d.input)
	desc := d3dTextureDesc{Width: uint32(w), Height: uint32(h), Mips: 1, ArraySize: 1, Format: 103, Sample: d3dSample{Count: 1}, Bind: 8}
	hr, _, _ := comCall(d.r.device, 5, uintptr(unsafe.Pointer(&desc)), 0, uintptr(unsafe.Pointer(&d.input)))
	if err := d3dError(hr); err != nil {
		return err
	}
	for _, v := range []struct {
		format uint32
		dst    *uintptr
	}{{61, &d.y.srv}, {49, &d.uv.srv}} {
		view := planeViewDesc{Format: v.format, Dimension: 4, Levels: 1}
		hr, _, _ := comCall(d.r.device, 7, d.input, uintptr(unsafe.Pointer(&view)), uintptr(unsafe.Pointer(v.dst)))
		if err := d3dError(hr); err != nil {
			return err
		}
	}
	d.storageW, d.storageH = w, h
	return nil
}

func acquireSurface(mutex uintptr, key uint64, timeout uint32) error {
	hr, _, _ := comCall(mutex, 8, uintptr(key), uintptr(timeout))
	if uint32(hr) == 0x102 {
		return errGPUNotReady
	}
	if hr != 0 {
		return fmt.Errorf("GPU keyed mutex 0x%08x", uint32(hr))
	}
	return nil
}
func releaseSurface(mutex uintptr, key uint64) error {
	hr, _, _ := comCall(mutex, 9, uintptr(key))
	return d3dError(hr)
}

func (d *textureDecoder) convert(ctx context.Context, sample uintptr, color h264Color) (_ *gpuDisplayFrame, err error) {
	var buffer, dxgi, source uintptr
	if err = mfCOM(sample, 40, 0, uintptr(unsafe.Pointer(&buffer))); err != nil {
		return nil, err
	}
	defer releaseCOM(&buffer)
	if err = mfCOM(buffer, 0, uintptr(unsafe.Pointer(&dxgiBufferIID)), uintptr(unsafe.Pointer(&dxgi))); err != nil {
		return nil, fmt.Errorf("MF output is not a GPU texture: %w", err)
	}
	defer releaseCOM(&dxgi)
	if err = mfCOM(dxgi, 3, uintptr(unsafe.Pointer(&texture2DIID)), uintptr(unsafe.Pointer(&source))); err != nil {
		return nil, err
	}
	defer releaseCOM(&source)
	var sub uint32
	if err = mfCOM(dxgi, 4, uintptr(unsafe.Pointer(&sub))); err != nil {
		return nil, err
	}
	var desc d3dTextureDesc
	comCall(source, 10, uintptr(unsafe.Pointer(&desc)))
	if desc.Format != 103 || int(desc.Width) < d.width || int(desc.Height) < d.height || desc.Width > uint32(d.width+16) || desc.Height > uint32(d.height+16) || desc.Mips != 1 || sub >= desc.ArraySize || desc.Sample.Count != 1 {
		return nil, errors.New("MF GPU texture has an unsupported layout")
	}
	if err = d.prepareInput(int(desc.Width), int(desc.Height)); err != nil {
		return nil, err
	}
	var slot *sharedGPUSlot
	select {
	case slot = <-d.pool.free:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() {
		if err != nil {
			d.pool.give(slot)
		}
	}()
	if err = acquireSurface(slot.mutex, slot.key, 100); err != nil {
		return nil, err
	}
	d.r.unbind()
	comCall(d.r.context, 46, d.input, 0, 0, 0, 0, source, uintptr(sub), 0)
	c := displayConstants{SourceW: uint32(d.width), SourceH: uint32(d.height)}
	if color.full || color.matrix == 1 {
		if !d.normalizer.ready || d.normalizer.color != color {
			d.normalizer.init(color)
			comCall(d.r.context, 48, d.coefficients.texture, 0, 0, uintptr(unsafe.Pointer(&d.normalizer.coeff[0][0][0])), 2304*4, 0)
		}
		c.Reserved = 1
	}
	d.r.pass(slot.image.rtv, d.r.convertNV12, [5]uintptr{d.y.srv, d.uv.srv, 0, d.coefficients.srv}, c, 0, 0, d.width, d.height)
	d.r.unbind()
	comCall(d.r.context, 111) // Flush producer commands before cross-device access.
	if err = releaseSurface(slot.mutex, 1); err != nil {
		return nil, err
	}
	slot.key = 1
	hr, _, _ := comCall(d.r.device, 39)
	if err = d3dError(hr); err != nil {
		return nil, err
	}
	runtime.KeepAlive(d)
	return &gpuDisplayFrame{pool: d.pool, slot: slot, width: d.width, height: d.height}, nil
}

func (d *textureDecoder) close() {
	if d.pool != nil {
		d.pool.close()
	}
	d.y.close()
	d.uv.close()
	d.coefficients.close()
	releaseCOM(&d.input)
	for _, p := range []*uintptr{&d.r.convertNV12, &d.r.vertex, &d.r.constants, &d.r.context, &d.r.device} {
		releaseCOM(p)
	}
}

type importedGPUTexture struct{ texture, mutex uintptr }

func (t *importedGPUTexture) close() { releaseCOM(&t.mutex); releaseCOM(&t.texture) }

func (r *d3d11Renderer) importFrame(f *gpuDisplayFrame, target uintptr) error {
	if r.importPool != f.pool {
		for _, texture := range r.imports {
			texture.close()
		}
		r.imports = make(map[uintptr]*importedGPUTexture)
		r.importPool = f.pool
	}
	texture := r.imports[f.slot.handle]
	if texture == nil {
		texture = &importedGPUTexture{}
		hr, _, _ := comCall(r.device, 28, f.slot.handle, uintptr(unsafe.Pointer(&texture2DIID)), uintptr(unsafe.Pointer(&texture.texture)))
		if err := d3dError(hr); err != nil {
			return err
		}
		if err := mfCOM(texture.texture, 0, uintptr(unsafe.Pointer(&keyedMutexIID)), uintptr(unsafe.Pointer(&texture.mutex))); err != nil {
			texture.close()
			return err
		}
		r.imports[f.slot.handle] = texture
	}
	if err := acquireSurface(texture.mutex, f.slot.key, 0); err != nil {
		return err
	}
	comCall(r.context, 47, target, texture.texture)
	comCall(r.context, 111)
	if err := releaseSurface(texture.mutex, 0); err != nil {
		return err
	}
	f.slot.key = 0
	return nil
}
