//go:build windows && amd64

package lan

import (
	"fmt"
	"os"
	"testing"
	"time"
)

func TestViewerFrameLeaseDoesNotBlockDecoder(t *testing.T) {
	v := &viewer{width: 64, height: 64}
	b := v.buffer()
	b[0] = 42
	v.setFrame(b)
	s := v.snapshotDisplay(true)
	defer v.releaseDisplay(s)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			b := v.buffer()
			b[0] = byte(i)
			v.setFrame(b)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("painting lease blocked frame publication")
	}
	if s.pixels[0] != 42 {
		t.Fatal("decoder overwrote borrowed display frame")
	}
	latest := v.snapshotDisplay(false)
	defer v.releaseDisplay(latest)
	if latest.pixels[0] != 99 || latest.frame.sequence != 101 {
		t.Fatal("not displaying latest decoded frame")
	}
}

// Own test window only. No input injection, monitor changes or clipboard
// writes. A 1440p viewport may extend beyond a smaller physical monitor;
// this measures frame submission, not physical display scan-out.
func TestGPU1440Viewport(t *testing.T) {
	if os.Getenv("MYDESK_LAN_UI_TEST") != "1" || os.Getenv("MYDESK_LAN_GPU_TEST") != "1" {
		t.Skip("native 1440p rendering opt-in")
	}
	for _, setting := range []struct {
		backend string
		format  frameFormat
	}{{"auto", pixelBGRA}, {"auto", pixelNV12}, {"d2d", pixelBGRA}} {
		backend, format := setting.backend, setting.format
		t.Run(backend+"-"+format.String(), func(t *testing.T) {
			v := &viewer{width: 1920, height: 1080, renderBackend: backend, onInput: func(inputEvent) {}}
			initial := v.bufferFor(format)
			if format == pixelBGRA {
				for i := 3; i < len(initial); i += 4 {
					initial[i] = 255
				}
			}
			v.setSurface(initial, format)
			done := make(chan error, 1)
			go func() {
				var hwnd uintptr
				deadline := time.Now().Add(3 * time.Second)
				for time.Now().Before(deadline) {
					v.mu.Lock()
					hwnd = v.hwnd
					v.mu.Unlock()
					if hwnd != 0 {
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
				if hwnd == 0 {
					done <- fmt.Errorf("test window not created")
					return
				}
				defer ucall("PostMessageW", hwnd, 0x10, 0, 0)
				// DefWindowProc otherwise constrains overlapped windows to the
				// physical monitor's maximum tracking size on this test machine.
				ucall("SetWindowLongPtrW", hwnd, ^uintptr(15), 0x92000000)
				if ok, _, err := ucall("SetWindowPos", hwnd, 0, 0, 0, 2560, 1440, 0x436); ok == 0 {
					done <- fmt.Errorf("resize test viewport: %v", err)
					return
				}
				v.setRefinePatch(message{Left: 0, Top: 0, Width: 64, Height: 64}, make([]byte, 64*64*4))
				start := time.Now()
				for i := 0; i < 240; i++ {
					time.Sleep(max(time.Duration(0), time.Until(start.Add(time.Duration(i)*time.Second/120))))
					b := v.bufferFor(format)
					if format == pixelNV12 {
						for j := range b {
							b[j] = byte(i + j/256)
						}
					} else {
						for j := 0; j < len(b); j += 4 {
							b[j], b[j+1], b[j+2], b[j+3] = byte(i+j/256), byte(i*3+j/512), byte(i+j/1024), 255
						}
					}
					v.setSurface(b, format)
				}
				time.Sleep(100 * time.Millisecond)
				done <- nil
			}()
			if err := v.run(); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if v.displayed < 30 {
				t.Fatalf("renderer unusable: %d submissions", v.displayed)
			}
			if backend == "auto" && v.gpu == nil {
				t.Fatal("GPU unexpectedly fell back")
			}
			if backend == "auto" && (v.gpu.cw != 2560 || v.gpu.ch != 1440) {
				t.Fatalf("incorrect GPU test viewport: %dx%d", v.gpu.cw, v.gpu.ch)
			}
			if backend == "d2d" && v.renderer == nil {
				t.Fatal("compatibility renderer unavailable")
			}
			if backend == "d2d" && (v.sharp.dw != 2560 || v.sharp.dh != 1440) {
				t.Fatalf("incorrect CPU sharp test viewport: %dx%d", v.sharp.dw, v.sharp.dh)
			}
			n := float64(v.renderMetrics.frames)
			t.Logf("%s 1080p -> 1440p submitted=%d superseded=%d upload avg=%.2fms submit avg=%.2fms decoded-age avg=%.2fms max=%.2fms", v.renderName, v.displayed, v.renderMetrics.skipped, float64(v.renderMetrics.upload)/float64(time.Millisecond)/n, float64(v.renderMetrics.submit)/float64(time.Millisecond)/n, float64(v.renderMetrics.age)/float64(time.Millisecond)/n, float64(v.renderMetrics.peak)/float64(time.Millisecond))
		})
	}
}

func TestGPUFailureReplaysRepairsIntoCompatibilityWindow(t *testing.T) {
	if os.Getenv("MYDESK_LAN_UI_TEST") != "1" || os.Getenv("MYDESK_LAN_GPU_TEST") != "1" {
		t.Skip("native renderer failure opt-in")
	}
	for _, format := range []frameFormat{pixelBGRA, pixelNV12} {
		t.Run(format.String(), func(t *testing.T) { testGPUFailureReplaysRepairs(t, format) })
	}
}

func testGPUFailureReplaysRepairs(t *testing.T, format frameFormat) {
	v := &viewer{width: 320, height: 180, onInput: func(inputEvent) {}}
	v.setSurface(make([]byte, format.size(320, 180)), format)
	v.setRefinePatch(message{Left: 0, Top: 0, Width: 64, Height: 64}, make([]byte, 64*64*4))
	v.onCommand = func() {
		if v.gpu != nil {
			// UI-thread-only injection: fail surface validation before invoking
			// any invalid native GPU operation, then exercise actual fallback.
			v.gpu.sw++
			v.requestFramePaint(v.hwnd)
		}
	}
	done := make(chan error, 1)
	go func() {
		deadline := time.Now().Add(3 * time.Second)
		var hwnd uintptr
		for time.Now().Before(deadline) {
			v.mu.Lock()
			hwnd, ready := v.hwnd, v.displayed > 0
			v.mu.Unlock()
			if hwnd != 0 && ready {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		v.mu.Lock()
		hwnd = v.hwnd
		v.mu.Unlock()
		if hwnd == 0 {
			done <- fmt.Errorf("test window not created")
			return
		}
		defer ucall("PostMessageW", hwnd, 0x10, 0, 0)
		ucall("PostMessageW", hwnd, 0x8005, 0, 0)
		for time.Now().Before(deadline) {
			v.mu.Lock()
			name := v.renderName
			v.mu.Unlock()
			if name == "Direct2D" {
				done <- nil
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		done <- fmt.Errorf("D3D11 failure did not recover via Direct2D")
	}()
	if err := v.run(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if v.gpu != nil || v.renderer == nil || len(v.cpuOverlay.mask) == 0 || !v.cpuOverlay.mask[0] {
		t.Fatal("renderer failure lost the compatibility target or valid repair")
	}
	if !v.compatibility.Load() || (format == pixelNV12 && len(v.compatPixels) != 320*180*4) {
		t.Fatal("NV12 compatibility conversion/output fallback not requested")
	}
}
