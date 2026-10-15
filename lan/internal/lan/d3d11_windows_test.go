//go:build windows && amd64

package lan

import (
	"bytes"
	"image"
	"math/rand"
	"os"
	"runtime"
	"testing"
	"time"
	"unsafe"
)

func TestD3D11ABI(t *testing.T) {
	if unsafe.Sizeof(d3dTextureDesc{}) != 44 || unsafe.Sizeof(dxgiSwapDesc{}) != 72 ||
		unsafe.Offsetof(dxgiSwapDesc{}.Window) != 48 || unsafe.Sizeof(displayConstants{}) != 48 ||
		unsafe.Sizeof(d3dMapped{}) != 16 || unsafe.Sizeof(d3dViewport{}) != 24 || unsafe.Sizeof(d3dBufferDesc{}) != 24 {
		t.Fatal("Win64 D3D11 ABI mismatch")
	}
}

// Readback is exclusively a validation/benchmark tool. Production rendering
// never downloads the scaled display texture or waits for GPU completion.
func readGPU(r *d3d11Renderer, source gpuTexture) ([]byte, error) {
	d := d3dTextureDesc{Width: uint32(source.w), Height: uint32(source.h), Mips: 1, ArraySize: 1, Format: 87, Sample: d3dSample{Count: 1}, Usage: 3, CPUAccess: 0x20000}
	var staging uintptr
	hr, _, _ := comCall(r.device, 5, uintptr(unsafe.Pointer(&d)), 0, uintptr(unsafe.Pointer(&staging)))
	if err := d3dError(hr); err != nil {
		return nil, err
	}
	defer releaseCOM(&staging)
	r.unbind()
	comCall(r.context, 47, staging, source.texture)
	var mapped d3dMapped
	hr, _, _ = comCall(r.context, 14, staging, 0, 1, 0, uintptr(unsafe.Pointer(&mapped)))
	if err := d3dError(hr); err != nil {
		return nil, err
	}
	defer comCall(r.context, 15, staging, 0)
	out := make([]byte, source.w*source.h*4)
	for y := 0; y < source.h; y++ {
		copy(out[y*source.w*4:], unsafe.Slice((*byte)(unsafe.Pointer(mapped.Data+uintptr(y)*uintptr(mapped.RowPitch))), source.w*4))
	}
	return out, nil
}

func TestGPUSharpMatchesCPU(t *testing.T) {
	if os.Getenv("MYDESK_LAN_GPU_TEST") != "1" {
		t.Skip("read-only GPU rendering opt-in")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	for _, size := range [][4]int{{67, 71, 133, 147}, {130, 70, 256, 144}, {193, 129, 257, 173}, {31, 19, 32, 20}, {13, 9, 13, 9}} {
		sw, sh, dw, dh := size[0], size[1], size[2], size[3]
		r, err := newD3D11(0, dw, dh, sw, sh)
		if err != nil {
			t.Fatal(err)
		}
		target, err := r.texture(dw, dh, 87, true)
		if err != nil {
			r.close()
			t.Fatal(err)
		}
		src := make([]byte, sw*sh*4)
		rand.New(rand.NewSource(42)).Read(src)
		for i := 3; i < len(src); i += 4 {
			src[i] = 255
		}
		_, err = r.render(displaySnapshot{pixels: src, baseRevision: 1}, target.rtv, 0, 0, dw, dh, false, false)
		if err != nil {
			target.close()
			r.close()
			t.Fatal(err)
		}
		got, err := readGPU(r, target)
		target.close()
		r.close()
		if err != nil {
			t.Fatal(err)
		}
		var cpu sharpDisplay
		want := cpu.prepare(src, sw, sh, dw, dh)
		if !bytes.Equal(got, want) {
			for i := range got {
				if got[i] != want[i] {
					t.Fatalf("GPU original-sharp differs %v at byte %d: %d vs %d", size, i, got[i], want[i])
				}
			}
		}
	}
}

func TestGPURepairLayersAndClear(t *testing.T) {
	if os.Getenv("MYDESK_LAN_GPU_TEST") != "1" {
		t.Skip("read-only GPU rendering opt-in")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	r, err := newD3D11(0, 200, 110, 128, 65)
	if err != nil {
		t.Fatal(err)
	}
	defer r.close()
	target, err := r.texture(200, 110, 87, true)
	if err != nil {
		t.Fatal(err)
	}
	defer target.close()
	video := bytes.Repeat([]byte{30, 40, 50, 255}, 128*65)
	var overlay refineOverlay
	overlay.reset(128, 65)
	for pass := 0; pass < 3; pass++ {
		switch pass {
		case 0:
			overlay.patch(image.Rect(64, 0, 128, 65), bytes.Repeat([]byte{80, 120, 200, 255}, 64*65))
		case 1:
			overlay.patch(image.Rect(64, 0, 128, 65), nil)
		case 2:
			overlay.patch(image.Rect(0, 64, 64, 65), bytes.Repeat([]byte{200, 80, 50, 255}, 64))
		}
		_, err := r.render(displaySnapshot{pixels: video, baseRevision: 1, repairs: overlay.snapshot(pass == 0)}, target.rtv, 0, 0, 200, 110, false, false)
		if err != nil {
			t.Fatal(err)
		}
		got, err := readGPU(r, target)
		if err != nil {
			t.Fatal(err)
		}
		var cpu sharpDisplay
		want := cpu.prepare(overlay.compose(video), 128, 65, 200, 110)
		if !bytes.Equal(got, want) {
			t.Fatalf("GPU repair composition differs, pass %d", pass)
		}
	}
}

func TestGPUOffsetPixelAndWholeRefine(t *testing.T) {
	if os.Getenv("MYDESK_LAN_GPU_TEST") != "1" {
		t.Skip("read-only GPU rendering opt-in")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	r, err := newD3D11(0, 137, 85, 65, 37)
	if err != nil {
		t.Fatal(err)
	}
	defer r.close()
	target, err := r.texture(137, 85, 87, true)
	if err != nil {
		t.Fatal(err)
	}
	defer target.close()
	video := bytes.Repeat([]byte{80, 130, 170, 255}, 65*37)
	var overlay refineOverlay
	overlay.reset(65, 37)
	overlay.patch(image.Rect(0, 0, 65, 37), bytes.Repeat([]byte{140, 80, 30, 255}, 65*37))
	for _, whole := range []bool{false, true} {
		_, err := r.render(displaySnapshot{pixels: video, baseRevision: 1, wholeRefine: whole, repairs: overlay.snapshot(!whole)}, target.rtv, 4, 5, 129, 75, false, false)
		if err != nil {
			t.Fatal(err)
		}
		got, err := readGPU(r, target)
		if err != nil {
			t.Fatal(err)
		}
		source := video
		if !whole {
			source = overlay.compose(video)
		}
		var cpu sharpDisplay
		want := cpu.prepare(source, 65, 37, 129, 75)
		for y := 0; y < 85; y++ {
			for x := 0; x < 137; x++ {
				pixel := got[(y*137+x)*4 : (y*137+x+1)*4]
				if x >= 4 && x < 133 && y >= 5 && y < 80 {
					i := ((y-5)*129 + x - 4) * 4
					if !bytes.Equal(pixel, want[i:i+4]) {
						t.Fatalf("offset/refine pixel differs (%d,%d) whole=%t", x, y, whole)
					}
				} else if !bytes.Equal(pixel, []byte{0, 0, 0, 255}) {
					t.Fatal("letterbox not black")
				}
			}
		}
	}
	// Pixel mode's nearest-neighbour sampling must remain sharp rather than
	// accidentally following the enhancement or smooth path.
	for y := 0; y < 37; y++ {
		for x := 0; x < 65; x++ {
			video[(y*65+x)*4] = byte(x*3 + y*5)
		}
	}
	_, err = r.render(displaySnapshot{pixels: video, baseRevision: 2, wholeRefine: true}, target.rtv, 0, 0, 137, 85, false, true)
	if err != nil {
		t.Fatal(err)
	}
	got, err := readGPU(r, target)
	if err != nil {
		t.Fatal(err)
	}
	for y := 0; y < 85; y++ {
		for x := 0; x < 137; x++ {
			sx, sy := (2*x+1)*65/(2*137), (2*y+1)*37/(2*85)
			if !bytes.Equal(got[(y*137+x)*4:(y*137+x+1)*4], video[(sy*65+sx)*4:(sy*65+sx+1)*4]) {
				t.Fatal("pixel sampling differs")
			}
		}
	}
}

func BenchmarkGPUSharp1080To1440(b *testing.B) {
	benchmarkGPUSharp1080To1440(b, pixelBGRA)
}
func BenchmarkGPUNV12Sharp1080To1440(b *testing.B) {
	benchmarkGPUSharp1080To1440(b, pixelNV12)
}
func benchmarkGPUSharp1080To1440(b *testing.B, format frameFormat) {
	if os.Getenv("MYDESK_LAN_GPU_TEST") != "1" {
		b.Skip("GPU benchmark opt-in")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	r, err := newD3D11(0, 2560, 1440, 1920, 1080)
	if err != nil {
		b.Fatal(err)
	}
	defer r.close()
	target, err := r.texture(2560, 1440, 87, true)
	if err != nil {
		b.Fatal(err)
	}
	defer target.close()
	pixels := make([]byte, format.size(1920, 1080))
	// Reusable staging target makes each timed iteration include completion
	// of GPU work, without charging a CPU copy of the 1440p image.
	d := d3dTextureDesc{Width: 2560, Height: 1440, Mips: 1, ArraySize: 1, Format: 87, Sample: d3dSample{Count: 1}, Usage: 3, CPUAccess: 0x20000}
	var staging uintptr
	hr, _, _ := comCall(r.device, 5, uintptr(unsafe.Pointer(&d)), 0, uintptr(unsafe.Pointer(&staging)))
	if err := d3dError(hr); err != nil {
		b.Fatal(err)
	}
	defer releaseCOM(&staging)
	// Exclude shader compilation, resize-only tables and resource allocation
	// from steady-state timings equally for BGRA and NV12.
	if _, err := r.render(displaySnapshot{pixels: pixels, format: format, baseRevision: 1}, target.rtv, 0, 0, 2560, 1440, false, false); err != nil {
		b.Fatal(err)
	}
	r.unbind()
	comCall(r.context, 47, staging, target.texture)
	var first d3dMapped
	hr, _, _ = comCall(r.context, 14, staging, 0, 1, 0, uintptr(unsafe.Pointer(&first)))
	if err := d3dError(hr); err != nil {
		b.Fatal(err)
	}
	comCall(r.context, 15, staging, 0)
	var submitted time.Duration
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pixels[0] = byte(i)
		times, err := r.render(displaySnapshot{pixels: pixels, format: format, baseRevision: uint64(i + 2)}, target.rtv, 0, 0, 2560, 1440, false, false)
		if err != nil {
			b.Fatal(err)
		}
		submitted += times.upload + times.submit
		r.unbind()
		comCall(r.context, 47, staging, target.texture)
		var mapped d3dMapped
		hr, _, _ = comCall(r.context, 14, staging, 0, 1, 0, uintptr(unsafe.Pointer(&mapped)))
		if err := d3dError(hr); err != nil {
			b.Fatal(err)
		}
		comCall(r.context, 15, staging, 0)
	}
	b.StopTimer()
	b.ReportMetric(float64(submitted.Nanoseconds())/float64(b.N), "CPU-submit-ns/op")
}
