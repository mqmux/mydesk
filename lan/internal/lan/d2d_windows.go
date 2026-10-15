//go:build windows && amd64

package lan

import (
	"fmt"
	"math"
	"syscall"
	"unsafe"
)

type guid struct {
	A    uint32
	B, C uint16
	D    [8]byte
}
type pixelFormat struct{ Format, Alpha uint32 }
type targetProps struct {
	Type         uint32
	Pixel        pixelFormat
	DPIX, DPIY   float32
	Usage, Level uint32
}
type hwndProps struct {
	Hwnd                   uintptr
	Width, Height, Options uint32
}
type bitmapProps struct {
	Pixel      pixelFormat
	DPIX, DPIY float32
}
type floatRect struct{ Left, Top, Right, Bottom float32 }
type d2dRenderer struct {
	factory, target, bitmap uintptr
	w, h                    int
	cw, ch                  int
}

//go:uintptrescapes
func comCall(obj uintptr, index int, args ...uintptr) (uintptr, uintptr, syscall.Errno) {
	vt := *(*uintptr)(unsafe.Pointer(obj))
	fn := *(*uintptr)(unsafe.Pointer(vt + uintptr(index)*unsafe.Sizeof(uintptr(0))))
	return syscall.SyscallN(fn, append([]uintptr{obj}, args...)...)
}
func hrError(ret uintptr) error {
	if int32(ret) < 0 {
		return fmt.Errorf("Direct2D HRESULT 0x%08x", uint32(ret))
	}
	return nil
}
func newD2D(hwnd uintptr, cw, ch, w, h int) (*d2dRenderer, error) {
	r := &d2dRenderer{w: w, h: h, cw: cw, ch: ch}
	iid := guid{0x06152247, 0x6f50, 0x465a, [8]byte{0x92, 0x45, 0x11, 0x8b, 0xfd, 0x3b, 0x60, 0x07}}
	hr, _, _ := syscall.NewLazyDLL("d2d1.dll").NewProc("D2D1CreateFactory").Call(0, uintptr(unsafe.Pointer(&iid)), 0, uintptr(unsafe.Pointer(&r.factory)))
	if err := hrError(hr); err != nil {
		return nil, err
	}
	props := targetProps{Pixel: pixelFormat{87, 3}, DPIX: 96, DPIY: 96}
	hp := hwndProps{Hwnd: hwnd, Width: uint32(cw), Height: uint32(ch), Options: 2}
	hr, _, _ = comCall(r.factory, 14, uintptr(unsafe.Pointer(&props)), uintptr(unsafe.Pointer(&hp)), uintptr(unsafe.Pointer(&r.target)))
	if err := hrError(hr); err != nil {
		r.close()
		return nil, err
	}
	bp := bitmapProps{Pixel: pixelFormat{87, 3}, DPIX: 96, DPIY: 96}
	size := uintptr(uint32(w)) | uintptr(uint32(h))<<32
	hr, _, _ = comCall(r.target, 4, size, 0, uintptr(w*4), uintptr(unsafe.Pointer(&bp)), uintptr(unsafe.Pointer(&r.bitmap)))
	if err := hrError(hr); err != nil {
		r.close()
		return nil, err
	}
	return r, nil
}
func (r *d2dRenderer) draw(b []byte, bw, bh, cw, ch, x, y, w, h int, smooth, pixel bool) error {
	if cw < 1 || ch < 1 {
		return nil
	}
	size := [2]uint32{uint32(cw), uint32(ch)}
	var hr uintptr
	if bw != r.w || bh != r.h {
		var bitmap uintptr
		bp := bitmapProps{Pixel: pixelFormat{87, 3}, DPIX: 96, DPIY: 96}
		hr, _, _ = comCall(r.target, 4, uintptr(uint32(bw))|uintptr(uint32(bh))<<32, 0, uintptr(bw*4), uintptr(unsafe.Pointer(&bp)), uintptr(unsafe.Pointer(&bitmap)))
		if err := hrError(hr); err != nil {
			return err
		}
		comCall(r.bitmap, 2)
		r.bitmap = bitmap
		r.w = bw
		r.h = bh
	}
	if cw != r.cw || ch != r.ch {
		hr, _, _ = comCall(r.target, 58, uintptr(unsafe.Pointer(&size)))
		if err := hrError(hr); err != nil {
			return err
		}
		r.cw, r.ch = cw, ch
	}
	hr, _, _ = comCall(r.bitmap, 10, 0, uintptr(unsafe.Pointer(&b[0])), uintptr(r.w*4))
	if err := hrError(hr); err != nil {
		return err
	}
	comCall(r.target, 48)
	black := [4]float32{0, 0, 0, 1}
	comCall(r.target, 47, uintptr(unsafe.Pointer(&black)))
	dest := floatRect{float32(x), float32(y), float32(x + w), float32(y + h)}
	interpolation := uintptr(0)
	if !pixel && (smooth || w < r.w || h < r.h) {
		interpolation = 1
	}
	comCall(r.target, 26, r.bitmap, uintptr(unsafe.Pointer(&dest)), uintptr(math.Float32bits(1)), interpolation, 0)
	hr, _, _ = comCall(r.target, 49, 0, 0)
	return hrError(hr)
}
func (r *d2dRenderer) close() {
	for _, p := range []uintptr{r.bitmap, r.target, r.factory} {
		if p != 0 {
			comCall(p, 2)
		}
	}
	r.bitmap = 0
	r.target = 0
	r.factory = 0
}
