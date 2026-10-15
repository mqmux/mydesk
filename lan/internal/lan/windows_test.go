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

func TestWindowsInputLayout(t *testing.T) {
	var i winInput
	if unsafe.Sizeof(i) != 40 || unsafe.Offsetof(i.Mouse) != 8 {
		t.Fatal("Win64 INPUT ABI mismatch")
	}
}

func TestFFmpegAccessUnits(t *testing.T) {
	if os.Getenv("MYDESK_LAN_MEDIA_TEST") != "1" {
		t.Skip("FFmpeg integration test")
	}
	ff, _ := filepath.Abs(filepath.Join("..", "..", "runtime", "ffmpeg.exe"))
	c := videoConfig{fps: 60, bitrate: 20000, area: desktop{width: 1920, height: 1080}}
	args := profileArgs(c, "libx264", "test")
	args = append(args[:len(args)-1], "-frames:v", "60", "pipe:1")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := helperCommand(ctx, ff, args...)
	data, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	err = readAccessUnits(bytes.NewReader(data), func(data []byte, key bool) error { count++; return nil })
	t.Logf("60 encoder frames produce %d access units", count)
	if err != nil || count != 60 {
		t.Fatalf("invalid AUD splitting: %d %v", count, err)
	}
}

func TestFFmpegEncoderPackets(t *testing.T) {
	if os.Getenv("MYDESK_LAN_MEDIA_TEST") != "1" {
		t.Skip("FFmpeg integration test")
	}
	for _, runtimeName := range []string{"ffmpeg.exe", "ffmpeg-compat.exe"} {
		t.Run(runtimeName, func(t *testing.T) {
			ff, _ := filepath.Abs(filepath.Join("..", "..", "runtime", runtimeName))
			c := videoConfig{fps: 120, bitrate: 80000, h264Quality: 8, area: desktop{width: 1920, height: 1080}}
			args := packetProfileArgs(c, "libx264", "test")
			args = append(args[:len(args)-1], "-frames:v", "150", "pipe:1")
			if runtimeName == "ffmpeg-compat.exe" {
				args = compatibleArgs(args)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := helperCommand(ctx, ff, args...)
			data, err := cmd.Output()
			if err != nil {
				t.Fatal(err)
			}
			count, keys := 0, 0
			err = readEncoderPackets(bytes.NewReader(data), func(data []byte, key bool) error {
				count++
				if key {
					keys++
					if !containsNAL(data, 7) || !containsNAL(data, 8) || !containsNAL(data, 5) {
						t.Fatal("IDR cannot initialize a fresh decoder")
					}
				}
				return nil
			})
			t.Logf("120 FPS Q100 local FLV: %d pictures, %d IDRs", count, keys)
			if err != nil || count != 150 || keys != 2 {
				t.Fatalf("invalid encoder packets: %d/%d %v", count, keys, err)
			}
		})
	}
}

func TestEncoderPacketEnvelopeKeepsQualityAndCapture(t *testing.T) {
	c := videoConfig{fps: 120, bitrate: 80000, h264Quality: 8, hideCursor: true, area: desktop{width: 1920, height: 1080}}
	for _, encoder := range []string{"h264_nvenc", "h264_amf", "h264_qsv", "libx264"} {
		for _, capture := range []string{"ddagrab", "gdi", "test"} {
			old, packet := profileArgs(c, encoder, capture), packetProfileArgs(c, encoder, capture)
			for i := 0; i < len(old)-5; i++ {
				if packet[i] != old[i] {
					t.Fatal("capture/quality/bitrate/GOP/B-frame options changed", encoder, capture)
				}
			}
		}
	}
}

func TestDesktopEncoderCapture(t *testing.T) {
	if os.Getenv("MYDESK_LAN_DESKTOP_TEST") != "1" {
		t.Skip("set MYDESK_LAN_DESKTOP_TEST=1 for actual desktop capture test")
	}
	ff, _ := filepath.Abs(filepath.Join("..", "..", "runtime", "ffmpeg.exe"))
	screens := dxgiOutputs()
	if len(screens) == 0 {
		screens = monitors()
	}
	if len(screens) == 0 {
		t.Fatal("no desktop outputs")
	}
	c := videoConfig{fps: 60, monitor: 0, bitrate: 20000, quality: 90, area: screens[0], ffmpeg: ff, encoder: "auto", capture: "auto", source: "desktop"}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p, err := selectProfile(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	runCtx, stop := context.WithTimeout(ctx, 2*time.Second)
	defer stop()
	frames := 0
	start := time.Now()
	_ = runEncoder(runCtx, c, p, func(data []byte, key bool) error { frames++; return nil })
	elapsed := time.Since(start).Seconds()
	t.Logf("desktop %dx%d %s/%s captured %d units in %.2fs (%.1f FPS including startup)", c.area.width, c.area.height, p.capture, p.name, frames, elapsed, float64(frames)/elapsed)
	if frames < 30 {
		t.Fatal("desktop encoder did not produce usable frames")
	}
}
func TestDirect2DWindow(t *testing.T) {
	if os.Getenv("MYDESK_LAN_UI_TEST") != "1" {
		t.Skip("set MYDESK_LAN_UI_TEST=1 for native rendering smoke test")
	}
	v := &viewer{width: 320, height: 180, onInput: func(inputEvent) {}}
	b := make([]byte, 320*180*4)
	for i := 0; i < len(b); i += 4 {
		b[i] = 40
		b[i+1] = 150
		b[i+2] = 230
		b[i+3] = 255
	}
	v.setFrame(b)
	go func() {
		for i := 0; i < 100; i++ {
			time.Sleep(20 * time.Millisecond)
			v.mu.Lock()
			h := v.hwnd
			v.mu.Unlock()
			if h != 0 {
				time.Sleep(300 * time.Millisecond)
				ucall("PostMessageW", h, 0x10, 0, 0)
				return
			}
		}
	}()
	if err := v.run(); err != nil {
		t.Fatal(err)
	}
	if v.displayed == 0 {
		t.Fatal("window never rendered a frame")
	}
	if v.renderer == nil && v.gpu == nil {
		t.Fatal("GPU display initialization fell back to GDI")
	}
}
func TestSyntheticVideoSession(t *testing.T) {
	if os.Getenv("MYDESK_LAN_MEDIA_TEST") != "1" {
		t.Skip("set MYDESK_LAN_MEDIA_TEST=1 for actual FFmpeg end-to-end test")
	}
	ff, err := filepath.Abs(filepath.Join("..", "..", "runtime", "ffmpeg.exe"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ff); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		fps     int
		tcp     bool
		quality int
	}{{60, false, 0}, {60, true, 0}, {120, false, 0}, {120, true, 0}, {120, false, 100}} {
		tcp, targetFPS := test.tcp, test.fps
		name := "UDP"
		if tcp {
			name = "TCP"
		}
		name = fmt.Sprintf("%s%d", name, targetFPS)
		if test.quality > 0 {
			name += fmt.Sprintf("Q%d", test.quality)
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.WithValue(context.Background(), jpegQualityKey{}, test.quality))
			defer cancel()
			tlsCfg, err := ephemeralTLS()
			if err != nil {
				t.Fatal(err)
			}
			listener, err := tls.Listen("tcp4", "127.0.0.1:0", tlsCfg)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			h := &host{code: "test-pair-code", name: "integration", tls: tlsCfg, cfg: videoConfig{fps: targetFPS, monitor: 0, bitrate: 0, quality: 100, area: desktop{width: 1920, height: 1080}, ffmpeg: ff, encoder: "auto", capture: "auto", source: "test"}}
			go func() {
				for {
					c, err := listener.Accept()
					if err != nil {
						return
					}
					go h.accept(ctx, c.(*tls.Conn), false)
				}
			}()
			s, err := connectController(ctx, cancel, listener.Addr().String(), h.code, targetFPS, ff, os.Getenv("MYDESK_LAN_CPU_DECODE") == "1")
			if err != nil {
				t.Fatal(err)
			}
			defer s.close()
			if s.welcome.FPS != targetFPS || s.welcome.Bitrate != targetFPS*40000/60 {
				t.Fatal("frame/bitrate negotiation mismatch", s.welcome)
			}
			s.view = &viewer{width: s.welcome.Width, height: s.welcome.Height, onInput: func(inputEvent) {}}
			var viewerDone chan error
			if os.Getenv("MYDESK_LAN_UI_TEST") == "1" {
				viewerDone = make(chan error, 1)
				go func() { viewerDone <- s.view.run() }()
				defer func() {
					s.view.mu.Lock()
					hwnd := s.view.hwnd
					s.view.mu.Unlock()
					if hwnd != 0 {
						ucall("PostMessageW", hwnd, 0x10, 0, 0)
					}
					if err := <-viewerDone; err != nil {
						t.Error(err)
					}
				}()
			}
			if err := s.start(tcp, false); err != nil {
				t.Fatal(err)
			}
			time.Sleep(4 * time.Second)
			before := s.decoded.Load()
			s.view.mu.Lock()
			beforeDisplay := s.view.displayed
			s.view.mu.Unlock()
			time.Sleep(3 * time.Second)
			after := s.decoded.Load()
			fps := float64(after-before) / 3
			s.view.mu.Lock()
			displayed := s.view.displayed
			s.view.mu.Unlock()
			t.Logf("%s encoder=%s received=%d decoded=%d displayed=%d lost=%d sustained decode=%.1f FPS", name, s.welcome.Backend, s.received.Load(), after, displayed, s.dropped.Load(), fps)
			if after == 0 || fps < float64(targetFPS)*0.9 {
				t.Fatalf("video pipeline not usable (%.1f FPS)", fps)
			}
			if viewerDone != nil {
				displayFPS := float64(displayed-beforeDisplay) / 3
				t.Logf("sustained native display %.1f FPS", displayFPS)
				if displayFPS < float64(targetFPS)*0.9 {
					t.Fatalf("display did not reach target: %.1f", displayFPS)
				}
			}
			if !tcp && s.transport.Load() {
				t.Fatal("loopback UDP unexpectedly fell back to TCP")
			}
		})
	}
}

func TestAutomaticTCPFallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tlsCfg, err := ephemeralTLS()
	if err != nil {
		t.Fatal(err)
	}
	listener, err := tls.Listen("tcp4", "127.0.0.1:0", tlsCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	h := &host{code: "fallback-test", cfg: videoConfig{fps: 10, quality: 90, bitrate: 20000, area: desktop{width: 320, height: 180}, encoder: "jpeg"}}
	go func() {
		for {
			c, err := listener.Accept()
			if err != nil {
				return
			}
			go h.accept(ctx, c.(*tls.Conn), false)
		}
	}()
	s, err := connectController(ctx, cancel, listener.Addr().String(), h.code, 10, "", false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	s.view = &viewer{width: 320, height: 180, onInput: func(inputEvent) {}}
	if err := s.start(false, false); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	hs := h.session
	h.mu.Unlock()
	blackhole, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer blackhole.Close()
	go func() {
		b := make([]byte, 2048)
		for {
			if _, _, err := blackhole.ReadFromUDP(b); err != nil {
				return
			}
		}
	}()
	hs.mu.Lock()
	hs.peer = blackhole.LocalAddr().(*net.UDPAddr)
	hs.mu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s.transport.Load() && hs.tcp.Load() && s.decoded.Load() >= 1 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("automatic fallback failed: controllerTCP=%t hostTCP=%t decoded=%d", s.transport.Load(), hs.tcp.Load(), s.decoded.Load())
}
func TestPairingRejectsWrongCode(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tlsCfg, err := ephemeralTLS()
	if err != nil {
		t.Fatal(err)
	}
	ln, err := tls.Listen("tcp4", "127.0.0.1:0", tlsCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	h := &host{code: "correct-code"}
	go func() {
		c, err := ln.Accept()
		if err == nil {
			h.accept(ctx, c.(*tls.Conn), false)
		}
	}()
	if _, err := connectController(ctx, cancel, ln.Addr().String(), "wrong-code", 60, "", false); err == nil {
		t.Fatal("wrong pairing code accepted")
	}
}
func TestDiscoveryLoopback(t *testing.T) {
	c, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		t.Fatal(err)
	}
	port := c.LocalAddr().(*net.UDPAddr).Port
	c.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go advertise(ctx, port, "test-device")
	time.Sleep(30 * time.Millisecond)
	devices, err := discover(ctx, port)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range devices {
		if d.Name == "test-device" {
			found = true
		}
	}
	if !found {
		t.Fatal("local discovery failed")
	}
}
