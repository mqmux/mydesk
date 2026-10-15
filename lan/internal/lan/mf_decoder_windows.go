//go:build windows && amd64

package lan

import (
	"context"
	"errors"
	"fmt"
	"log"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// Native MF supports private shared GPU pictures and the original CPU
// NV12/BGRA compatibility output. No MFT-owned surface is retained by the UI.
var mfplat = syscall.NewLazyDLL("mfplat.dll")
var ole32 = syscall.NewLazyDLL("ole32.dll")

var (
	mfDecoderCLSID = guid{0x62ce7e72, 0x4c71, 0x4d20, [8]byte{0xb1, 0x5d, 0x45, 0x28, 0x31, 0xa8, 0x7d, 0x9d}}
	mfTransformIID = guid{0xbf94c121, 0x5b05, 0x4e6f, [8]byte{0x80, 0, 0xba, 0x59, 0x89, 0x61, 0x41, 0x4d}}
	mf2DBufferIID  = guid{0x7dc9d5f9, 0x9ed9, 0x44ec, [8]byte{0x9b, 0xbf, 6, 0, 0xbb, 0x58, 0x9f, 0xbb}}
	mfMajorType    = guid{0x48eba18e, 0xf8c9, 0x4687, [8]byte{0xbf, 0x11, 0x0a, 0x74, 0xc9, 0xf9, 0x6a, 0x8f}}
	mfSubtype      = guid{0xf7e34c9a, 0x42e8, 0x4714, [8]byte{0xb7, 0x4b, 0xcb, 0x29, 0xd7, 0x2c, 0x35, 0xe5}}
	mfFrameSize    = guid{0x1652c33d, 0xd6b2, 0x4012, [8]byte{0xb8, 0x34, 0x72, 3, 8, 0x49, 0xa3, 0x7d}}
	mfFrameRate    = guid{0xc459a2e8, 0x3d2c, 0x4e44, [8]byte{0xb1, 0x32, 0xfe, 0xe5, 0x15, 0x6c, 0x7b, 0xb0}}
	mfAspect       = guid{0xc6376a1e, 0x8d0a, 0x4027, [8]byte{0xbe, 0x45, 0x6d, 0x9a, 0x0a, 0xd3, 0x9b, 0xb6}}
	mfInterlace    = guid{0xe2724bb8, 0xe676, 0x4806, [8]byte{0xb4, 0xb2, 0xa8, 0xd6, 0xef, 0xb4, 0x4c, 0xcd}}
	mfStride       = guid{0x644b4e48, 0x1e02, 0x4516, [8]byte{0xb0, 0xeb, 0xc0, 0x1c, 0xa9, 0xd4, 0x9a, 0xc6}}
	mfLowLatency   = guid{0x9c27891a, 0xed7a, 0x40e1, [8]byte{0x88, 0xe8, 0xb2, 0x27, 0x27, 0xa0, 0x24, 0xee}}
	mfD3D11Aware   = guid{0x206b4fc8, 0xfcf9, 0x4c51, [8]byte{0xaf, 0xe3, 0x97, 0x64, 0x36, 0x9e, 0x33, 0xa0}}
	mfCleanPoint   = guid{0x9cdf01d8, 0xa0f0, 0x43ba, [8]byte{0xb0, 0x77, 0xea, 0xa0, 0x6c, 0xbd, 0x72, 0x8a}}
	mfVideo        = mfFourCC(0x73646976)
	mfH264         = mfFourCC(0x34363248)
	mfNV12         = mfFourCC(0x3231564e)
)

func mfFourCC(n uint32) guid { return guid{n, 0, 0x10, [8]byte{0x80, 0, 0, 0xaa, 0, 0x38, 0x9b, 0x71}} }

//go:uintptrescapes
func mfCall(name string, args ...uintptr) (uintptr, error) {
	p := mfplat.NewProc(name)
	if err := p.Find(); err != nil {
		return 0, err
	}
	hr, _, _ := p.Call(args...)
	return hr, mfError(name, hr)
}

func mfError(op string, hr uintptr) error {
	if int32(hr) < 0 {
		return fmt.Errorf("%s HRESULT 0x%08x", op, uint32(hr))
	}
	return nil
}

//go:uintptrescapes
func mfCOM(obj uintptr, slot int, args ...uintptr) error {
	hr, _, _ := comCall(obj, slot, args...)
	return mfError(fmt.Sprintf("MF method %d", slot), hr)
}

func mfSet32(obj uintptr, key *guid, value uint32) error {
	return mfCOM(obj, 21, uintptr(unsafe.Pointer(key)), uintptr(value))
}
func mfSet64(obj uintptr, key *guid, value uint64) error {
	return mfCOM(obj, 22, uintptr(unsafe.Pointer(key)), uintptr(value))
}
func mfSetGUID(obj uintptr, key, value *guid) error {
	return mfCOM(obj, 24, uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(value)))
}
func mfGet32(obj uintptr, key *guid, fallback uint32) uint32 {
	var n uint32
	if mfCOM(obj, 7, uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(&n))) != nil {
		return fallback
	}
	return n
}

type mfOutputInfo struct{ Flags, Size, Alignment uint32 }
type mfOutputData struct {
	Stream uint32
	Sample uintptr
	Status uint32
	Events uintptr
}

type mfDecodeRequest struct {
	data  []byte
	reply chan error
}
type nativeDecoder struct {
	width, height, fps int
	format             frameFormat
	hardware           bool
	buffer             func() []byte
	onFrame            func([]byte)
	timing             decoderTiming
	started            time.Time
	ctx                context.Context
	cancel             context.CancelFunc
	requests           chan mfDecodeRequest
	exited             chan struct{}
	errMu              sync.Mutex
	err                error
	onGPUFrame         func(*gpuDisplayFrame)
}

func (d *nativeDecoder) start(parent context.Context, onFrame func([]byte)) error {
	if pixelNV12.size(d.width, d.height) == 0 || d.format.size(d.width, d.height) == 0 || d.width < 48 || d.height < 48 || d.width > 4096 || d.height > 2304 || d.fps < 1 || d.fps > maxFPS {
		return errors.New("MF decoder: unsupported dimensions/FPS")
	}
	d.ctx, d.cancel = context.WithCancel(parent)
	d.onFrame = onFrame
	d.requests = make(chan mfDecodeRequest)
	d.exited = make(chan struct{})
	d.started = time.Now()
	d.timing.init(d.fps, d.started)
	ready := make(chan error, 1)
	go d.run(ready)
	err := <-ready
	if err != nil {
		d.cancel()
		<-d.exited
	}
	return err
}

func (d *nativeDecoder) run(ready chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(d.exited)
	first := true
	n, err := newMFTransform(d.width, d.height, d.fps, d.hardware)
	if err == nil && d.onGPUFrame != nil {
		if !d.hardware {
			err = errors.New("GPU texture output requires hardware decoding")
		} else {
			n.textures, err = newTextureDecoder(n.device, d.width, d.height)
			n.gpuContext = d.ctx
			n.emitGPU = func(frame *gpuDisplayFrame) {
				d.timing.complete(time.Now())
				d.onGPUFrame(frame)
				if first {
					log.Printf("[lan][decoder] MF GPU shared-texture output ready in %d ms; no whole-frame CPU readback", time.Since(d.started).Milliseconds())
					first = false
				}
			}
		}
	}
	ready <- err
	if err != nil {
		if n != nil {
			n.close()
		}
		d.setError(err)
		return
	}
	defer n.close()
	for {
		select {
		case <-d.ctx.Done():
			return
		case request := <-d.requests:
			err := n.decode(request.data, func(nv12 []byte) error {
				var b []byte
				if d.buffer != nil {
					b = d.buffer()
				} else {
					b = make([]byte, d.format.size(d.width, d.height))
				}
				if len(b) != d.format.size(d.width, d.height) {
					return errors.New("MF decoder: invalid viewer buffer")
				}
				if d.format == pixelNV12 {
					copy(b, nv12)
				} else {
					b = nv12ToBGRA(b, nv12, d.width, d.height)
				}
				d.timing.complete(time.Now())
				d.onFrame(b)
				if first {
					log.Printf("[lan][decoder] MF first frame in %d ms including activation (D3D11=%t output=%s)", time.Since(d.started).Milliseconds(), d.hardware, d.format)
					first = false
				}
				return nil
			})
			request.reply <- err
			if err != nil {
				d.setError(err)
				return
			}
		}
	}
}

func (d *nativeDecoder) setError(err error)      { d.errMu.Lock(); d.err = err; d.errMu.Unlock() }
func (d *nativeDecoder) failure() error          { d.errMu.Lock(); defer d.errMu.Unlock(); return d.err }
func (d *nativeDecoder) slow(now time.Time) bool { return d.timing.slow(now) }
func (d *nativeDecoder) writeFrame(data []byte) error {
	if len(data) == 0 || len(data) > maxVideoFrame {
		return errors.New("MF decoder: invalid picture size")
	}
	request := mfDecodeRequest{data: data, reply: make(chan error, 1)}
	d.timing.submit(time.Now())
	select {
	case d.requests <- request:
	case <-d.ctx.Done():
		return d.ctx.Err()
	case <-d.exited:
		return errors.New("MF decoder stopped")
	}
	select {
	case err := <-request.reply:
		return err
	case <-d.ctx.Done():
		return d.ctx.Err()
	case <-d.exited:
		if err := d.failure(); err != nil {
			return err
		}
		return errors.New("MF decoder stopped")
	}
}
func (d *nativeDecoder) close() {
	if d.cancel != nil {
		d.cancel()
		<-d.exited
		d.cancel = nil
	}
}

// MF activation, transform calls and sample releases stay on one MTA worker.
// Private shared display surfaces have separate, thread-safe ownership.
type mfTransform struct {
	transform, device, manager uintptr
	width, height, fps, ow, oh int
	stride                     int
	info                       mfOutputInfo
	frame                      uint64
	nv12, packed               []byte
	color                      h264Color
	converter                  nv12Normalizer
	comStarted, mfStarted      bool
	textures                   *textureDecoder
	gpuContext                 context.Context
	emitGPU                    func(*gpuDisplayFrame)
}

func newMFTransform(w, h, fps int, hardware bool) (_ *mfTransform, result error) {
	n := &mfTransform{width: w, height: h, fps: fps, color: h264Color{matrix: 6}, nv12: make([]byte, pixelNV12.size(w, h))}
	defer func() {
		if result != nil {
			n.close()
		}
	}()
	if err := ole32.NewProc("CoInitializeEx").Find(); err != nil {
		return nil, err
	}
	hr, _, _ := ole32.NewProc("CoInitializeEx").Call(0, 0)
	if err := mfError("CoInitializeEx", hr); err != nil {
		return nil, err
	}
	n.comStarted = true
	if _, err := mfCall("MFStartup", 0x20070, 1); err != nil {
		return nil, err
	}
	n.mfStarted = true
	hr, _, _ = ole32.NewProc("CoCreateInstance").Call(uintptr(unsafe.Pointer(&mfDecoderCLSID)), 0, 1, uintptr(unsafe.Pointer(&mfTransformIID)), uintptr(unsafe.Pointer(&n.transform)))
	if err := mfError("H.264 MFT activation", hr); err != nil {
		return nil, err
	}
	var attrs uintptr
	if err := mfCOM(n.transform, 8, uintptr(unsafe.Pointer(&attrs))); err != nil {
		return nil, err
	}
	defer releaseCOM(&attrs)
	if err := mfSet32(attrs, &mfLowLatency, 1); err != nil {
		return nil, fmt.Errorf("MF low-latency mode: %w", err)
	}
	if hardware {
		if mfGet32(attrs, &mfD3D11Aware, 0) == 0 {
			return nil, errors.New("H.264 MFT is not D3D11 aware")
		}
		if err := n.attachD3D11(); err != nil {
			return nil, err
		}
	}
	var mt uintptr
	if _, err := mfCall("MFCreateMediaType", uintptr(unsafe.Pointer(&mt))); err != nil {
		return nil, err
	}
	defer releaseCOM(&mt)
	for _, e := range []error{mfSetGUID(mt, &mfMajorType, &mfVideo), mfSetGUID(mt, &mfSubtype, &mfH264), mfSet64(mt, &mfFrameSize, uint64(w)<<32|uint64(h)), mfSet64(mt, &mfFrameRate, uint64(fps)<<32|1), mfSet64(mt, &mfAspect, 1<<32|1), mfSet32(mt, &mfInterlace, 2)} {
		if e != nil {
			return nil, e
		}
	}
	if err := mfCOM(n.transform, 15, 0, mt, 0); err != nil {
		return nil, err
	}
	if err := n.outputType(); err != nil {
		return nil, err
	}
	for _, message := range []uintptr{0x10000000, 0x10000003} {
		if err := mfCOM(n.transform, 23, message, 0); err != nil {
			return nil, err
		}
	}
	return n, nil
}

func (n *mfTransform) attachD3D11() error {
	p := syscall.NewLazyDLL("d3d11.dll").NewProc("D3D11CreateDevice")
	if err := p.Find(); err != nil {
		return err
	}
	var context uintptr
	defer releaseCOM(&context)
	var level uint32
	hr, _, _ := p.Call(0, 1, 0, 0x820, 0, 0, 7, uintptr(unsafe.Pointer(&n.device)), uintptr(unsafe.Pointer(&level)), uintptr(unsafe.Pointer(&context)))
	if err := mfError("MF D3D11 device", hr); err != nil {
		return err
	}
	multithreadIID := guid{0x9b7e4e00, 0x342c, 0x4106, [8]byte{0xa1, 0x9f, 0x4f, 0x27, 4, 0xf6, 0x89, 0xf0}}
	var protected uintptr
	if err := mfCOM(n.device, 0, uintptr(unsafe.Pointer(&multithreadIID)), uintptr(unsafe.Pointer(&protected))); err != nil {
		return err
	}
	comCall(protected, 5, 1)
	releaseCOM(&protected)
	var token uint32
	if _, err := mfCall("MFCreateDXGIDeviceManager", uintptr(unsafe.Pointer(&token)), uintptr(unsafe.Pointer(&n.manager))); err != nil {
		return err
	}
	if err := mfCOM(n.manager, 7, n.device, uintptr(token)); err != nil {
		return err
	}
	return mfCOM(n.transform, 23, 2, n.manager)
}

func (n *mfTransform) outputType() error {
	for index := uintptr(0); index < 32; index++ {
		var mt uintptr
		if err := mfCOM(n.transform, 14, 0, index, uintptr(unsafe.Pointer(&mt))); err != nil {
			return err
		}
		var subtype guid
		err := mfCOM(mt, 10, uintptr(unsafe.Pointer(&mfSubtype)), uintptr(unsafe.Pointer(&subtype)))
		if err != nil || subtype != mfNV12 {
			releaseCOM(&mt)
			continue
		}
		var size uint64
		err = mfCOM(mt, 8, uintptr(unsafe.Pointer(&mfFrameSize)), uintptr(unsafe.Pointer(&size)))
		w, h := int(size>>32), int(uint32(size))
		if err != nil || w < n.width || h < n.height || w > n.width+16 || h > n.height+16 || pixelNV12.size(w, h) == 0 {
			releaseCOM(&mt)
			return errors.New("MF decoder output dimensions differ from paired desktop")
		}
		err = mfCOM(n.transform, 16, 0, mt, 0)
		n.stride = int(int32(mfGet32(mt, &mfStride, uint32(w))))
		releaseCOM(&mt)
		if err != nil {
			return err
		}
		n.ow, n.oh = w, h
		if n.stride < w || n.stride > 32768 {
			return errors.New("MF decoder invalid NV12 stride")
		}
		return mfCOM(n.transform, 7, 0, uintptr(unsafe.Pointer(&n.info)))
	}
	return errors.New("MF decoder does not offer NV12")
}

func mfSample(data []byte, size, alignment int) (sample uintptr, result error) {
	if size < 1 || size > 64<<20 {
		return 0, errors.New("MF sample size outside limit")
	}
	if _, err := mfCall("MFCreateSample", uintptr(unsafe.Pointer(&sample))); err != nil {
		return 0, err
	}
	defer func() {
		if result != nil {
			releaseCOM(&sample)
		}
	}()
	var buffer uintptr
	var err error
	if alignment > 1 {
		_, err = mfCall("MFCreateAlignedMemoryBuffer", uintptr(size), uintptr(alignment-1), uintptr(unsafe.Pointer(&buffer)))
	} else {
		_, err = mfCall("MFCreateMemoryBuffer", uintptr(size), uintptr(unsafe.Pointer(&buffer)))
	}
	if err != nil {
		return sample, err
	}
	defer releaseCOM(&buffer)
	if len(data) > 0 {
		var p uintptr
		var capacity uint32
		if err := mfCOM(buffer, 3, uintptr(unsafe.Pointer(&p)), uintptr(unsafe.Pointer(&capacity)), 0); err != nil {
			return sample, err
		}
		if p == 0 || int(capacity) < len(data) {
			mfCOM(buffer, 4)
			return sample, errors.New("MF input buffer too small")
		}
		copy(unsafe.Slice((*byte)(unsafe.Pointer(p)), len(data)), data)
		if err := mfCOM(buffer, 4); err != nil {
			return sample, err
		}
		if err := mfCOM(buffer, 6, uintptr(len(data))); err != nil {
			return sample, err
		}
	}
	return sample, mfCOM(sample, 42, buffer)
}

func (n *mfTransform) decode(data []byte, emit func([]byte) error) error {
	nals := annexBNALs(data)
	picture, key := false, false
	for _, nal := range nals {
		switch nal[0] & 31 {
		case 7:
			color, w, h, err := parseH264Color(nal)
			if err != nil {
				return err
			}
			if w != n.width || h != n.height {
				return errors.New("MF SPS resolution differs from paired desktop")
			}
			n.color = color
		case 1:
			picture = true
		case 5:
			picture = true
			key = true
		}
	}
	if !picture {
		return errors.New("MF access unit has no picture")
	}
	if n.frame == 0 {
		sps, pps := false, false
		for _, nal := range nals {
			sps = sps || nal[0]&31 == 7
			pps = pps || nal[0]&31 == 8
		}
		if !key || !sps || !pps {
			return errors.New("MF decoder requires initial IDR/SPS/PPS")
		}
	}
	sample, err := mfSample(data, len(data), 0)
	if err != nil {
		return err
	}
	defer releaseCOM(&sample)
	if err := mfCOM(sample, 36, uintptr(n.frame*10000000/uint64(n.fps))); err != nil {
		return err
	}
	if err := mfCOM(sample, 38, uintptr(10000000/n.fps)); err != nil {
		return err
	}
	if key {
		if err := mfSet32(sample, &mfCleanPoint, 1); err != nil {
			return err
		}
	}
	hr, _, _ := comCall(n.transform, 24, 0, sample, 0)
	if uint32(hr) == 0xc00d36b5 {
		if err := n.drain(emit); err != nil {
			return err
		}
		hr, _, _ = comCall(n.transform, 24, 0, sample, 0)
	}
	if err := mfError("MF ProcessInput", hr); err != nil {
		return err
	}
	n.frame++
	return n.drain(emit)
}

func (n *mfTransform) drain(emit func([]byte) error) error {
	for count := 0; count < 64; count++ {
		out := mfOutputData{}
		if n.info.Flags&0x300 == 0 {
			var err error
			out.Sample, err = mfSample(nil, max(int(n.info.Size), pixelNV12.size(n.ow, n.oh)), int(n.info.Alignment))
			if err != nil {
				return err
			}
		}
		var status uint32
		hr, _, _ := comCall(n.transform, 25, 0, 1, uintptr(unsafe.Pointer(&out)), uintptr(unsafe.Pointer(&status)))
		releaseCOM(&out.Events)
		if uint32(hr) == 0xc00d6d72 {
			releaseCOM(&out.Sample)
			return nil
		}
		if uint32(hr) == 0xc00d6d61 {
			releaseCOM(&out.Sample)
			if err := n.outputType(); err != nil {
				return err
			}
			continue
		}
		if err := mfError("MF ProcessOutput", hr); err != nil {
			releaseCOM(&out.Sample)
			return err
		}
		if out.Sample == 0 {
			return errors.New("MF output has no sample")
		}
		if n.emitGPU != nil {
			frame, err := n.textures.convert(n.gpuContext, out.Sample, n.color)
			releaseCOM(&out.Sample)
			if err != nil {
				return err
			}
			n.emitGPU(frame)
			continue
		}
		err := n.copyOutput(out.Sample)
		releaseCOM(&out.Sample)
		if err != nil {
			return err
		}
		n.converter.normalize(n.nv12, n.width, n.height, n.color)
		if err := emit(n.nv12); err != nil {
			return err
		}
	}
	return errors.New("MF output did not reach NEED_MORE_INPUT")
}

func (n *mfTransform) copyOutput(sample uintptr) error {
	var buffer uintptr
	if err := mfCOM(sample, 40, 0, uintptr(unsafe.Pointer(&buffer))); err != nil {
		return err
	}
	defer releaseCOM(&buffer)
	var twoD uintptr
	if mfCOM(buffer, 0, uintptr(unsafe.Pointer(&mf2DBufferIID)), uintptr(unsafe.Pointer(&twoD))) == nil {
		defer releaseCOM(&twoD)
		var length uint32
		if err := mfCOM(twoD, 7, uintptr(unsafe.Pointer(&length))); err != nil {
			return err
		}
		if int(length) < pixelNV12.size(n.ow, n.oh) || length > 64<<20 {
			return errors.New("MF contiguous NV12 length invalid")
		}
		if int(length) == len(n.nv12) && n.ow == n.width && n.oh == n.height {
			return mfCOM(twoD, 8, uintptr(unsafe.Pointer(&n.nv12[0])), uintptr(length))
		}
		if len(n.packed) != int(length) {
			n.packed = make([]byte, int(length))
		}
		if err := mfCOM(twoD, 8, uintptr(unsafe.Pointer(&n.packed[0])), uintptr(length)); err != nil {
			return err
		}
		return copyNV12Rows(n.nv12, n.packed, n.width, n.height, n.ow, n.oh)
	}
	var p uintptr
	var length uint32
	if err := mfCOM(buffer, 3, uintptr(unsafe.Pointer(&p)), 0, uintptr(unsafe.Pointer(&length))); err != nil {
		return err
	}
	defer mfCOM(buffer, 4)
	if p == 0 || length > 64<<20 {
		return errors.New("MF NV12 buffer invalid")
	}
	return copyNV12Rows(n.nv12, unsafe.Slice((*byte)(unsafe.Pointer(p)), int(length)), n.width, n.height, n.stride, n.oh)
}

func (n *mfTransform) close() {
	if n.textures != nil {
		n.textures.close()
		n.textures = nil
	}
	if n.transform != 0 {
		mfCOM(n.transform, 23, 0, 0)
		mfCOM(n.transform, 23, 0x10000001, 0)
	}
	releaseCOM(&n.transform)
	releaseCOM(&n.manager)
	releaseCOM(&n.device)
	if n.mfStarted {
		mfCall("MFShutdown")
		n.mfStarted = false
	}
	if n.comStarted {
		ole32.NewProc("CoUninitialize").Call()
		n.comStarted = false
	}
}

var mfProbe struct {
	once sync.Once
	err  error
}

func nativeDecoderAvailable() error {
	mfProbe.once.Do(func() {
		result := make(chan error, 1)
		go func() {
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()
			n, err := newMFTransform(64, 64, 60, false)
			if n != nil {
				n.close()
			}
			result <- err
		}()
		mfProbe.err = <-result
	})
	return mfProbe.err
}
