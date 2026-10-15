//go:build windows && amd64

package lan

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
	"unsafe"
)

func TestMFABIAndFallbackPolicy(t *testing.T) {
	if unsafe.Sizeof(guid{}) != 16 || unsafe.Sizeof(mfOutputInfo{}) != 12 || unsafe.Sizeof(mfOutputData{}) != 32 || unsafe.Offsetof(mfOutputData{}.Sample) != 8 || unsafe.Offsetof(mfOutputData{}.Events) != 24 {
		t.Fatal("Media Foundation x64 ABI")
	}
	for _, test := range []struct {
		backend, requested, path string
		hardware                 bool
		format                   frameFormat
		want                     string
		hw                       bool
		fmt                      frameFormat
		ok                       bool
	}{
		{"mf", "auto", "runtime", true, pixelNV12, "ffmpeg", true, pixelNV12, true},
		{"mf", "mf", "runtime", true, pixelNV12, "mf", false, pixelNV12, true},
		{"mf", "auto", "", true, pixelNV12, "mf", false, pixelNV12, true},
		{"mf", "auto", "runtime", false, pixelNV12, "ffmpeg", true, pixelNV12, true},
		{"mf", "mf", "runtime", false, pixelNV12, "mf", false, pixelNV12, false},
		{"mf", "auto", "", false, pixelNV12, "mf", false, pixelNV12, false},
		{"ffmpeg", "ffmpeg", "runtime", true, pixelNV12, "ffmpeg", false, pixelNV12, true},
		{"ffmpeg", "auto", "runtime", false, pixelNV12, "ffmpeg", false, pixelBGRA, true},
		{"ffmpeg", "auto", "runtime", false, pixelBGRA, "ffmpeg", false, pixelBGRA, false},
	} {
		b, hw, f, ok := nextDecoder(test.backend, test.hardware, test.format, test.requested, test.path)
		if b != test.want || hw != test.hw || f != test.fmt || ok != test.ok {
			t.Fatal(test, b, hw, f, ok)
		}
	}
}

func TestNativeQueuePressureRecentNotLifetime(t *testing.T) {
	now := time.Now()
	var p nativeQueuePressure
	p.reset(now, 0)
	if p.record(now.Add(10*time.Millisecond), 100) {
		t.Fatal("startup burst triggered fallback")
	}
	if p.record(now.Add(time.Second), 101) || p.record(now.Add(1020*time.Millisecond), 150) || p.record(now.Add(1100*time.Millisecond), 150) {
		t.Fatal("one burst/stale count triggered fallback")
	}
	if p.record(now.Add(1500*time.Millisecond), 151) {
		t.Fatal("two bursts triggered fallback")
	}
	if !p.record(now.Add(2*time.Second), 152) {
		t.Fatal("recent repeated overflows did not survive recovery epochs")
	}
	p.reset(now, 0)
	for i := 1; i <= 5; i++ {
		if p.record(now.Add(time.Duration(i)*3*time.Second), uint64(i)) {
			t.Fatal("lifetime sparse losses triggered fallback")
		}
	}
}

func TestNativeMFDecode(t *testing.T) {
	if os.Getenv("MYDESK_LAN_MEDIA_TEST") != "1" {
		t.Skip("native codec/media opt-in")
	}
	if err := nativeDecoderAvailable(); err != nil {
		t.Fatal(err)
	}
	ff, _ := filepath.Abs(filepath.Join("..", "..", "runtime", "ffmpeg.exe"))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := helperCommand(ctx, ff, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=128x64:rate=60", "-frames:v", "6", "-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency", "-crf", "8", "-bf", "0", "-bsf:v", "h264_metadata=aud=insert", "-f", "h264", "pipe:1")
	stream, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, hw := range []bool{false, true} {
		t.Run(fmt.Sprintf("hardware-%t", hw), func(t *testing.T) {
			var count int
			d := &nativeDecoder{width: 128, height: 64, fps: 60, hardware: hw, format: pixelNV12}
			if err := d.start(ctx, func(b []byte) {
				if len(b) != 128*64*3/2 {
					t.Error("wrong plane size")
				}
				count++
			}); err != nil {
				t.Fatal(err)
			}
			defer d.close()
			if err := readAccessUnits(bytes.NewReader(stream), func(b []byte, _ bool) error { return d.writeFrame(b) }); err != nil {
				t.Fatal(err)
			}
			if count != 6 {
				t.Fatalf("got %d/6 pictures before closing input", count)
			}
		})
	}
}

func TestNativeMFColoursAnd1080Crop(t *testing.T) {
	if os.Getenv("MYDESK_LAN_MEDIA_TEST") != "1" {
		t.Skip("native codec/media opt-in")
	}
	ff, _ := filepath.Abs(filepath.Join("..", "..", "runtime", "ffmpeg.exe"))
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	for _, setting := range []struct {
		w, h              int
		matrix, rangeName string
	}{{128, 64, "smpte170m", "tv"}, {128, 64, "smpte170m", "pc"}, {128, 64, "bt709", "tv"}, {128, 64, "bt709", "pc"}, {1920, 1080, "smpte170m", "tv"}} {
		t.Run(fmt.Sprintf("%dx%d-%s-%s", setting.w, setting.h, setting.matrix, setting.rangeName), func(t *testing.T) {
			w, h := setting.w, setting.h
			input := solidNV12(w, h, 100, 128, 128)
			bars := [][3]byte{{16, 128, 128}, {235, 128, 128}, {81, 90, 240}, {145, 54, 34}, {41, 240, 110}, {126, 128, 128}, {200, 180, 80}, {60, 100, 160}}
			if setting.rangeName == "pc" {
				bars[0][0], bars[1][0] = 0, 255
			}
			for y := 0; y < h; y++ {
				for x := 0; x < w; x++ {
					input[y*w+x] = bars[x*8/w][0]
				}
			}
			for y := 0; y < h/2; y++ {
				for x := 0; x < w; x += 2 {
					input[w*h+y*w+x], input[w*h+y*w+x+1] = bars[x*8/w][1], bars[x*8/w][2]
				}
			}
			cmd := helperCommand(ctx, ff, "-hide_banner", "-loglevel", "error", "-f", "rawvideo", "-pixel_format", "nv12", "-video_size", fmt.Sprintf("%dx%d", w, h), "-framerate", "60", "-colorspace", setting.matrix, "-color_range", setting.rangeName, "-i", "pipe:0", "-frames:v", "4", "-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency", "-crf", "8", "-bf", "0", "-colorspace", setting.matrix, "-color_range", setting.rangeName, "-bsf:v", "h264_metadata=aud=insert", "-f", "h264", "pipe:1")
			cmd.Stdin = bytes.NewReader(bytes.Repeat(input, 4))
			var stderr tailBuffer
			cmd.Stderr = &stderr
			stream, err := cmd.Output()
			if err != nil {
				t.Fatal(err, stderr.String())
			}
			want := decodeTestPictures(t, ctx, ff, stream, w, h, pixelBGRA, false)
			for _, hardware := range []bool{false, true} {
				t.Run(fmt.Sprintf("hw-%t", hardware), func(t *testing.T) {
					var got []byte
					count := 0
					d := &nativeDecoder{width: w, height: h, fps: 60, hardware: hardware, format: pixelNV12}
					if err := d.start(ctx, func(b []byte) {
						if got == nil {
							got = nv12ToBGRA(nil, b, w, h)
						}
						count++
					}); err != nil {
						t.Fatal(err)
					}
					defer d.close()
					if err := readAccessUnits(bytes.NewReader(stream), func(b []byte, _ bool) error { return d.writeFrame(b) }); err != nil {
						t.Fatal(err)
					}
					if count != 4 || len(got) != len(want) {
						t.Fatalf("frames=%d bytes=%d/%d", count, len(got), len(want))
					}
					peak := 0
					for bar := 0; bar < 8; bar++ {
						for _, y := range []int{8, h / 2, h - 8} {
							for x := bar*w/8 + w/32; x < (bar+1)*w/8-w/32; x++ {
								for c := 0; c < 3; c++ {
									i := (y*w+x)*4 + c
									peak = max(peak, absInt(int(got[i])-int(want[i])))
								}
							}
						}
					}
					if peak > 5 {
						t.Fatalf("native colour/crop differs from FFmpeg BGRA by %d", peak)
					}
					t.Logf("native/FFmpeg bar interior max channel difference %d", peak)
				})
			}
		})
	}
}

func TestNativeMFSessionWithoutControllerRuntime(t *testing.T) {
	if os.Getenv("MYDESK_LAN_MEDIA_TEST") != "1" {
		t.Skip("native network/media opt-in")
	}
	ff, _ := filepath.Abs(filepath.Join("..", "..", "runtime", "ffmpeg.exe"))
	for _, tcp := range []bool{false, true} {
		t.Run(fmt.Sprintf("TCP-%t", tcp), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.WithValue(context.Background(), decoderBackendKey{}, "mf"))
			defer cancel()
			tlsConfig, err := ephemeralTLS()
			if err != nil {
				t.Fatal(err)
			}
			listener, err := tls.Listen("tcp4", "127.0.0.1:0", tlsConfig)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			h := &host{code: "native-no-runtime", cfg: videoConfig{fps: 60, quality: 100, area: desktop{width: 1920, height: 1080}, ffmpeg: ff, source: "test", capture: "auto", encoder: "auto"}}
			go func() {
				for {
					c, e := listener.Accept()
					if e != nil {
						return
					}
					go h.accept(ctx, c.(*tls.Conn), false)
				}
			}()
			s, err := connectController(ctx, cancel, listener.Addr().String(), h.code, 60, "", false, net.IPv4(127, 0, 0, 1))
			if err != nil {
				t.Fatal(err)
			}
			defer s.close()
			if s.welcome.Codec != codecH264 || s.ffmpeg != "" {
				t.Fatal("native-only negotiation still requires external controller runtime", s.welcome)
			}
			s.view = &viewer{width: 1920, height: 1080, onInput: func(inputEvent) {}}
			if err := s.start(tcp, false); err != nil {
				t.Fatal(err)
			}
			time.Sleep(1500 * time.Millisecond)
			before := s.decoded.Load()
			time.Sleep(2 * time.Second)
			fps := float64(s.decoded.Load()-before) / 2
			if s.activeDecoder.Load() != 1 || fps < 54 || s.transport.Load() != tcp {
				t.Fatalf("native-only backend=%d fps=%.1f tcp=%t", s.activeDecoder.Load(), fps, s.transport.Load())
			}
			t.Logf("native-only controller 1080p60 %.1f FPS, loss=%d", fps, s.dropped.Load())
		})
	}
}

func TestNativeDecoderFailureFallsBackAndStaysOnFFmpeg(t *testing.T) {
	if os.Getenv("MYDESK_LAN_MEDIA_TEST") != "1" {
		t.Skip("actual decoder recovery opt-in")
	}
	ff, _ := filepath.Abs(filepath.Join("..", "..", "runtime", "ffmpeg.exe"))
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	// Matrix 9 is deliberately outside the native normalization contract;
	// FFmpeg supports it. This exercises actual decoder-loop error recovery.
	cmd := helperCommand(ctx, ff, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "color=gray:size=128x64:rate=60", "-frames:v", "1", "-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency", "-crf", "8", "-bf", "0", "-g", "1", "-colorspace", "bt2020nc", "-bsf:v", "h264_metadata=aud=insert", "-f", "h264", "pipe:1")
	unit, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	v := &viewer{width: 128, height: 64}
	s := &controllerSession{ctx: ctx, cancel: cancel, ffmpeg: ff, software: true, decoder: "auto", view: v, welcome: message{Width: 128, Height: 64, FPS: 60}, frames: make(chan videoFrame, 16), commands: make(chan message, 64)}
	done := make(chan struct{})
	go func() { s.decode(); close(done) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("decoder worker not closed")
		}
	}()
	deadline := time.Now().Add(4 * time.Second)
	id := uint64(0)
	for time.Now().Before(deadline) {
		id++
		s.frames <- videoFrame{id: id, epoch: 1, codec: codecH264, key: true, data: unit, at: time.Now()}
		time.Sleep(15 * time.Millisecond)
		if s.decoded.Load() > 0 {
			break
		}
	}
	if s.decoded.Load() == 0 || s.activeDecoder.Load() != 2 {
		t.Fatalf("failed MF did not recover through FFmpeg: decoded=%d backend=%d", s.decoded.Load(), s.activeDecoder.Load())
	}
	// A later encoder restart must retain the recovered backend.
	before := s.decoded.Load()
	id++
	s.frames <- videoFrame{id: id, epoch: 2, codec: codecH264, key: true, data: unit, at: time.Now()}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && s.decoded.Load() == before {
		id++
		s.frames <- videoFrame{id: id, epoch: 2, codec: codecH264, key: true, data: unit, at: time.Now()}
		time.Sleep(15 * time.Millisecond)
	}
	if s.decoded.Load() == before || s.activeDecoder.Load() != 2 {
		t.Fatal("recovery epoch reselected failed MF backend")
	}
	if len(s.commands) == 0 {
		t.Fatal("failed decoder did not request a fresh reference chain")
	}
}
