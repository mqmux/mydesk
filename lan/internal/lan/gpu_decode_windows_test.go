//go:build windows && amd64

package lan

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"image"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"
)

func TestGPUSharedTextureABI(t *testing.T) {
	if unsafe.Sizeof(planeViewDesc{}) != 24 {
		t.Fatal("Win64 SRV ABI")
	}
}

func renderNativeTest(r *d3d11Renderer, s displaySnapshot, target gpuTexture, w, h int) error {
	until := time.Now().Add(time.Second)
	for {
		_, err := r.render(s, target.rtv, 0, 0, w, h, false, false)
		if err != errGPUNotReady || time.Now().After(until) {
			return err
		}
		time.Sleep(time.Millisecond)
	}
}

func TestMFSharedGPUTexturesMatchCompatibility(t *testing.T) {
	if os.Getenv("MYDESK_LAN_GPU_TEST") != "1" || os.Getenv("MYDESK_LAN_MEDIA_TEST") != "1" {
		t.Skip("native GPU codec opt-in")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	ff, _ := filepath.Abs(filepath.Join("..", "..", "runtime", "ffmpeg.exe"))
	for _, color := range []struct {
		name                      string
		args                      []string
		w, h, targetW, targetH, n int
	}{{"601", nil, 128, 64, 224, 112, 6}, {"709", []string{"-colorspace", "bt709"}, 128, 64, 224, 112, 6}, {"601-full", []string{"-color_range", "pc"}, 128, 64, 224, 112, 6}, {"709-full", []string{"-colorspace", "bt709", "-color_range", "pc"}, 128, 64, 224, 112, 6}, {"1080-crop-to-1440", []string{"-colorspace", "bt709"}, 1920, 1080, 2560, 1440, 2}} {
		t.Run(color.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			args := []string{"-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", fmt.Sprintf("testsrc2=size=%dx%d:rate=60", color.w, color.h), "-frames:v", fmt.Sprint(color.n), "-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency", "-crf", "8", "-bf", "0"}
			args = append(args, color.args...)
			args = append(args, "-bsf:v", "h264_metadata=aud=insert", "-f", "h264", "pipe:1")
			stream, err := helperCommand(ctx, ff, args...).Output()
			if err != nil {
				t.Fatal(err)
			}
			var units [][]byte
			if err := readAccessUnits(bytes.NewReader(stream), func(b []byte, _ bool) error { units = append(units, b); return nil }); err != nil {
				t.Fatal(err)
			}
			var expected [][]byte
			cpu := &nativeDecoder{width: color.w, height: color.h, fps: 60, hardware: true, format: pixelNV12}
			if err := cpu.start(ctx, func(b []byte) { expected = append(expected, nv12ToBGRA(nil, b, color.w, color.h)) }); err != nil {
				t.Fatal(err)
			}
			for _, unit := range units {
				if err := cpu.writeFrame(unit); err != nil {
					cpu.close()
					t.Fatal(err)
				}
			}
			cpu.close()
			r, err := newD3D11(0, color.targetW, color.targetH, color.w, color.h)
			if err != nil {
				t.Fatal(err)
			}
			defer r.close()
			target, err := r.texture(color.targetW, color.targetH, 87, true)
			if err != nil {
				t.Fatal(err)
			}
			defer target.close()
			count := 0
			var callbackErr error
			gpu := &nativeDecoder{width: color.w, height: color.h, fps: 60, hardware: true, format: pixelNV12, onGPUFrame: func(f *gpuDisplayFrame) {
				defer f.release()
				s := displaySnapshot{frame: &displayFrame{native: f, sequence: uint64(count + 1), format: pixelBGRA}, format: pixelBGRA, baseRevision: uint64(count + 1)}
				if err := renderNativeTest(r, s, target, color.targetW, color.targetH); err != nil {
					callbackErr = err
					return
				}
				got, err := readGPU(r, target)
				if err != nil {
					callbackErr = err
					return
				}
				var sharp sharpDisplay
				if count >= len(expected) {
					callbackErr = fmt.Errorf("unexpected GPU picture %d", count)
					return
				}
				want := sharp.prepare(expected[count], color.w, color.h, color.targetW, color.targetH)
				for i := range got {
					if got[i] != want[i] {
						callbackErr = fmt.Errorf("%s frame %d byte %d: direct=%d CPU=%d", color.name, count, i, got[i], want[i])
						return
					}
				}
				count++
			}}
			if err := gpu.start(ctx, func([]byte) { callbackErr = fmt.Errorf("whole frame returned through CPU path") }); err != nil {
				t.Fatal(err)
			}
			defer gpu.close()
			for _, unit := range units {
				if err := gpu.writeFrame(unit); err != nil {
					t.Fatal(err)
				}
				if callbackErr != nil {
					t.Fatal(callbackErr)
				}
			}
			if count != len(units) || len(r.imports) > 3 {
				t.Fatal("surface pool count", count, len(r.imports))
			}
			t.Logf("%s: %d real GPU pictures, exact CPU colour/sharp match; %d shared surfaces", color.name, count, len(r.imports))
		})
	}
}

func TestGPURepairMotionEligibility(t *testing.T) {
	if os.Getenv("MYDESK_LAN_GPU_TEST") != "1" {
		t.Skip("GPU motion mask opt-in")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	r, err := newD3D11(0, 128, 64, 128, 64)
	if err != nil {
		t.Fatal(err)
	}
	defer r.close()
	if err := r.prepareNativeRepairs(); err != nil {
		t.Fatal(err)
	}
	pixels := bytes.Repeat([]byte{40, 60, 80, 255}, 128*64)
	comCall(r.context, 48, r.source[0].texture, 0, 0, uintptr(unsafe.Pointer(&pixels[0])), 128*4, 0)
	r.sourceIndex = 0
	r.rebaseNativeRepair(image.Rect(0, 0, 128, 64))
	mask := []byte{255, 255}
	comCall(r.context, 48, r.mask.texture, 0, 0, uintptr(unsafe.Pointer(&mask[0])), 2, 0)
	r.hasRepairs = true
	r.checkNativeRepairs()
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			pixels[(y*128+x)*4] = 200
		}
	}
	comCall(r.context, 48, r.source[0].texture, 0, 0, uintptr(unsafe.Pointer(&pixels[0])), 128*4, 0)
	r.checkNativeRepairs()
	// Copy the R8 mask through the source shader to a validation BGRA surface.
	shader, err := r.shader("ReadMotionMask", "ps_4_0", 15)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseCOM(&shader)
	target, err := r.texture(2, 1, 87, true)
	if err != nil {
		t.Fatal(err)
	}
	defer target.close()
	check := func(a, b byte) {
		r.passViews(target.rtv, shader, [6]uintptr{0, 0, 0, 0, 0, r.nativeMask[r.nativeMaskIndex].srv}, displayConstants{}, 0, 0, 2, 1)
		got, err := readGPU(r, target)
		if err != nil || got[0] != a || got[4] != b {
			t.Fatal("motion eligibility", got, err)
		}
	}
	check(0, 255)
	// A new CPU mask for another tile must not revive the first stale repair.
	comCall(r.context, 48, r.mask.texture, 0, 0, uintptr(unsafe.Pointer(&mask[0])), 2, 0)
	r.checkNativeRepairs()
	check(0, 255)
	r.rebaseNativeRepair(image.Rect(0, 0, 64, 64))
	r.checkNativeRepairs()
	check(255, 255)
}

func TestGPUVideoSessionAndMinimize(t *testing.T) {
	if os.Getenv("MYDESK_LAN_MEDIA_TEST") != "1" || os.Getenv("MYDESK_LAN_GPU_TEST") != "1" || os.Getenv("MYDESK_LAN_UI_TEST") != "1" {
		t.Skip("real shared-GPU UI/session opt-in")
	}
	ff, _ := filepath.Abs(filepath.Join("..", "..", "runtime", "ffmpeg.exe"))
	for _, scenario := range []struct{ tcp, smooth bool }{{false, false}, {true, false}, {false, true}, {true, true}} {
		tcp, smooth := scenario.tcp, scenario.smooth
		t.Run(fmt.Sprintf("TCP-%t/Smooth-%t", tcp, smooth), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			tlsConfig, err := ephemeralTLS()
			if err != nil {
				t.Fatal(err)
			}
			ln, err := tls.Listen("tcp4", "127.0.0.1:0", tlsConfig)
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			h := &host{code: "GPU-video-test", queueGrace: smooth, udpSmoothing: smooth, sendStats: true, cfg: videoConfig{fps: 120, quality: 100, area: desktop{width: 1920, height: 1080}, ffmpeg: ff, encoder: "auto", capture: "auto", source: "test"}}
			go func() {
				for {
					c, e := ln.Accept()
					if e != nil {
						return
					}
					go h.accept(ctx, c.(*tls.Conn), false)
				}
			}()
			s, err := connectController(context.WithValue(ctx, decoderBackendKey{}, "mf"), cancel, ln.Addr().String(), h.code, 120, "", false, net.IPv4(127, 0, 0, 1))
			if err != nil {
				t.Fatal(err)
			}
			defer s.close()
			s.gpuDecode = "auto"
			s.pipeline = &pipelineMetrics{}
			s.view = &viewer{width: 1920, height: 1080, pipeline: s.pipeline, onInput: func(inputEvent) {}}
			if err := s.start(tcp, false); err != nil {
				t.Fatal(err)
			}
			viewDone := make(chan error, 1)
			go func() { viewDone <- s.view.run() }()
			defer func() {
				s.view.mu.Lock()
				window := s.view.hwnd
				s.view.mu.Unlock()
				if window != 0 {
					ucall("PostMessageW", window, 0x10, 0, 0)
				}
				<-viewDone
			}()
			waitJPEG(t, func() bool { return s.gpuPictures.Load() > 20 })
			time.Sleep(2 * time.Second)
			before := s.gpuPictures.Load()
			s.view.mu.Lock()
			shown := s.view.displayed
			window := s.view.hwnd
			s.view.mu.Unlock()
			time.Sleep(2 * time.Second)
			s.view.mu.Lock()
			displayed := s.view.displayed
			s.view.mu.Unlock()
			decodedFPS := float64(s.gpuPictures.Load()-before) / 2
			displayFPS := float64(displayed-shown) / 2
			t.Logf("shared GPU 1080p120: decoded=%.1f presented=%.1f dropped=%d | %s", decodedFPS, displayFPS, s.dropped.Load(), s.pipeline.summary())
			if decodedFPS < 108 || displayFPS < 108 {
				t.Fatal("GPU route didn't sustain the unchanged 90%% target", decodedFPS, displayFPS)
			}
			// Owned window only. Minimize/restore does not renew the encoder epoch
			// or replace the native GPU decoder, and decoded pictures continue.
			h.mu.Lock()
			hs := h.session
			h.mu.Unlock()
			epoch := hs.epoch.Load()
			gpuBefore := s.gpuPictures.Load()
			ucall("PostMessageW", window, 0x112, 0xf020, 0)
			waitJPEG(t, func() bool { return s.viewerMinimized.Load() && hs.minimized.Load() })
			time.Sleep(700 * time.Millisecond)
			ucall("PostMessageW", window, 0x112, 0xf120, 0)
			waitJPEG(t, func() bool { return !s.viewerMinimized.Load() && !hs.minimized.Load() })
			if hs.epoch.Load() != epoch || s.gpuPictures.Load() <= gpuBefore || s.activeDecoder.Load() != 1 {
				t.Fatal("minimize restarted or disabled GPU video")
			}
			if tcp {
				// Simulate a display compatibility fallback, not a driver reset or
				// modification to any user's devices. CPU output must resume once.
				s.view.compatibility.Store(true)
				waitJPEG(t, func() bool {
					s.view.mu.Lock()
					defer s.view.mu.Unlock()
					return s.view.frames.latest != nil && s.view.frames.latest.native == nil && len(s.view.pixels) > 0
				})
				if hs.epoch.Load() > epoch+2 || s.ctx.Err() != nil {
					t.Fatal("compatibility fallback restarted repeatedly")
				}
			}
		})
	}
}

func TestGPUDecodeScreenHandoff(t *testing.T) {
	if os.Getenv("MYDESK_LAN_MEDIA_TEST") != "1" || os.Getenv("MYDESK_LAN_GPU_TEST") != "1" {
		t.Skip("native GPU screen handoff opt-in")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	ff, _ := filepath.Abs(filepath.Join("..", "..", "runtime", "ffmpeg.exe"))
	first := message{Type: "screen", ScreenGeneration: 1, Epoch: 1, MonitorCount: 2, Width: 128, Height: 64, FPS: 60, Codec: codecH264, Features: []string{screenSwitchFeature, repairResyncFeature}}
	s := &controllerSession{ctx: ctx, cancel: cancel, welcome: first, frames: make(chan videoFrame, 32), commands: make(chan message, 32), view: &viewer{width: 128, height: 64}, decoder: "mf", gpuDecode: "auto"}
	done := make(chan struct{})
	go func() { defer close(done); s.decode() }()
	defer func() { s.close(); <-done }()
	var id uint64
	var previous *sharedGPUPool
	for i, size := range [][2]int{{128, 64}, {192, 96}, {128, 64}} {
		w, h := size[0], size[1]
		generation := uint64(i + 1)
		data, err := helperCommand(ctx, ff, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", fmt.Sprintf("testsrc2=size=%dx%d:rate=60", w, h), "-frames:v", "6", "-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency", "-crf", "8", "-bf", "0", "-bsf:v", "h264_metadata=aud=insert", "-f", "h264", "pipe:1").Output()
		if err != nil {
			t.Fatal(err)
		}
		desc := first
		desc.Width, desc.Height, desc.Epoch, desc.ScreenGeneration = w, h, generation, generation
		s.registerScreen(desc)
		if err := readAccessUnits(bytes.NewReader(data), func(b []byte, key bool) error {
			id++
			s.frames <- videoFrame{id: id, epoch: generation, codec: codecH264, key: key, data: b, at: time.Now()}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		waitJPEG(t, func() bool { return s.gpuPictures.Load() >= uint64((i+1)*6) })
		s.view.mu.Lock()
		frame := s.view.frames.acquire()
		s.view.mu.Unlock()
		if frame == nil || frame.native == nil || s.readyScreen.Load() != generation {
			t.Fatal("missing GPU screen picture", generation)
		}
		f := frame.native.(*gpuDisplayFrame)
		if f.width != w || f.height != h || f.pool == previous {
			t.Fatal("old screen surface/pool reused")
		}
		if previous != nil {
			previous.mu.Lock()
			closed, available := previous.closed, len(previous.free)
			previous.mu.Unlock()
			if !closed || available != 0 {
				t.Fatal("old decoder pool retained free surfaces")
			}
		}
		previous = f.pool
		s.view.mu.Lock()
		s.view.frames.release(frame)
		s.view.mu.Unlock()
	}
}

type countedNativeSurface struct{ closed *atomic.Int64 }

func (f countedNativeSurface) size() (int, int) { return 2, 2 }
func (f countedNativeSurface) release()         { f.closed.Add(1) }
func TestNativeMailboxLeaseOwnership(t *testing.T) {
	var m displayMailbox
	var releases atomic.Int64
	m.publishNative(countedNativeSurface{&releases}, time.Now())
	lease := m.acquire()
	for i := 0; i < 100; i++ {
		m.publishNative(countedNativeSurface{&releases}, time.Now())
	}
	if releases.Load() != 99 {
		t.Fatal("leased surface was prematurely recycled", releases.Load())
	}
	m.release(lease)
	m.publish(bytes.Repeat([]byte{0}, 16), time.Now())
	if releases.Load() != 101 {
		t.Fatal("GPU ownership leaked after CPU fallback", releases.Load())
	}
}
