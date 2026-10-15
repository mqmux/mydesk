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
	"sync"
	"testing"
	"unsafe"
)

func TestResolveDisplayIdentityAndRemoval(t *testing.T) {
	c := videoConfig{display: "DISPLAY2", monitor: 1, area: desktop{left: 1920, width: 1920, height: 1080}, fps: 120, quality: 100}
	a, b := c, c
	a.display, a.monitor, a.area = "DISPLAY1", 1, desktop{width: 1920, height: 1080}
	b.monitor, b.area = 0, desktop{left: -2560, top: -30, width: 2560, height: 1440}
	got, ok := resolveDisplay(c, []videoConfig{b, a})
	if !ok || got.display != c.display || got.area != b.area || got.monitor != 0 {
		t.Fatal("selected device lost on resize/reorder", got)
	}
	got, ok = resolveDisplay(c, []videoConfig{a})
	if !ok || got.area != a.area || got.display != "DISPLAY1" {
		t.Fatal("removed display retained stale bounds")
	}
	if _, ok := resolveDisplay(c, []videoConfig{{}}); ok {
		t.Fatal("invalid/unavailable display accepted")
	}
}

func TestDisplayRefreshGenerationInputsAndSettings(t *testing.T) {
	for _, auto := range []bool{false, true} {
		t.Run(fmt.Sprint(auto), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c := videoConfig{display: "TEST", fps: 120, bitrate: 50000, quality: 100, h264Quality: 8, encoder: "jpeg", source: "test", area: desktop{width: 128, height: 64}}
			next := c
			next.area = desktop{left: -192, top: -40, width: 192, height: 96}
			s := &hostSession{ctx: ctx, cfg: c, profile: encoderProfile{name: "jpeg-delta", capture: "gdi", codec: codecJPEGDelta}, input: newInputState(c.area), screenGeneration: 1, screenSwitch: true, autoBitrate: auto, restart: make(chan struct{}, 1), screenList: func(videoConfig) []videoConfig { return []videoConfig{next} }}
			s.bitrate.Store(50000)
			s.screenReady.Store(1)
			injected := 0
			s.input.inject = func(winInput) error { injected++; return nil }
			s.input.keys[0x10] = inputEvent{VK: 0x10}
			s.input.buttons[1] = true
			changed, err := s.refreshDisplay()
			got, _, generation := s.screenConfig()
			expectedRate := 50000
			if auto {
				expectedRate = automaticBitrate(next)
			}
			if err != nil || !changed || generation != 2 || got.area != next.area || got.bitrate != expectedRate || got.fps != 120 || got.quality != 100 || got.h264Quality != 8 || s.input.area != next.area || injected != 2 || s.screenReady.Load() != 0 || len(s.restart) != 1 {
				t.Fatal("incomplete resolution transition", changed, err, got, generation, injected)
			}
			if err := s.applyRemoteInput(message{Input: inputEvent{Kind: "key-down", VK: 0x41}, ScreenGeneration: 1}); err != nil || injected != 2 {
				t.Fatal("old input passed the new geometry gate")
			}
			s.screenReady.Store(2)
			if err := s.applyRemoteInput(message{Input: inputEvent{Kind: "key-down", VK: 0x41}, ScreenGeneration: 2}); err != nil || injected != 3 {
				t.Fatal("new ready screen input did not resume")
			}
			changed, err = s.refreshDisplay()
			if err != nil || changed || s.screenGeneration != 2 {
				t.Fatal("unchanged screen caused a restart storm")
			}
		})
	}
}

func TestDisplayProbeRevalidatesAndRetainsOnUnavailable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := videoConfig{display: "TEST", fps: 60, encoder: "jpeg", source: "test", area: desktop{width: 64, height: 64}}
	next, later := c, c
	next.area, later.area = desktop{width: 128, height: 64}, desktop{width: 192, height: 96}
	calls := 0
	s := &hostSession{ctx: ctx, cfg: c, profile: encoderProfile{codec: codecJPEGDelta}, input: newInputState(c.area), screenGeneration: 1, restart: make(chan struct{}, 1), screenList: func(videoConfig) []videoConfig {
		calls++
		if calls == 1 {
			return []videoConfig{next}
		}
		return []videoConfig{later}
	}}
	if changed, err := s.refreshDisplay(); err == nil || changed || s.cfg.area != c.area || len(s.restart) != 0 {
		t.Fatal("stale probe committed")
	}
	if changed, err := s.refreshDisplay(); err != nil || !changed || s.cfg.area != later.area {
		t.Fatal("stable retry failed", err)
	}
	s.screenList = func(videoConfig) []videoConfig { return nil }
	if changed, err := s.refreshDisplay(); err == nil || changed || s.cfg.area != later.area {
		t.Fatal("unavailable display replaced working configuration")
	}
}

func TestNativeDisplayEnumerationReusesCallback(t *testing.T) {
	if os.Getenv("MYDESK_LAN_UI_TEST") != "1" {
		t.Skip("read-only Windows display enumeration opt-in")
	}
	if unsafe.Sizeof(struct {
		monitorInfo
		Device [32]uint16
	}{}) != 104 {
		t.Fatal("MONITORINFOEXW ABI")
	}
	for i := 0; i < 1200; i++ {
		outputs := monitorOutputs()
		if len(outputs) == 0 || outputs[0].device == "" || !validDesktop(outputs[0].area) {
			t.Fatal("monitor enumeration failed", i, outputs)
		}
	}
}

func TestPairRefreshesRealDisplayBounds(t *testing.T) {
	if os.Getenv("MYDESK_LAN_UI_TEST") != "1" {
		t.Skip("read-only native display pairing opt-in")
	}
	outputs := monitorOutputs()
	if len(outputs) == 0 {
		t.Skip("no active desktop")
	}
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
	// Read the real display bounds but do not capture, inject input or change
	// any monitor mode. Deliberately seed a stale listener startup rectangle.
	h := &host{code: "real-bounds-test", cfg: videoConfig{source: "desktop", display: outputs[0].device, area: desktop{width: 64, height: 64}, fps: 60, quality: 100, encoder: "jpeg", capture: "auto"}}
	go func() {
		for {
			c, e := ln.Accept()
			if e != nil {
				return
			}
			go h.accept(ctx, c.(*tls.Conn), false)
		}
	}()
	sessionCtx, stop := context.WithCancel(ctx)
	s, err := connectController(sessionCtx, stop, ln.Addr().String(), h.code, 60, "", true, net.IPv4(127, 0, 0, 1))
	if err != nil {
		stop()
		t.Fatal(err)
	}
	defer s.close()
	d := outputs[0].area
	if s.welcome.Width != d.width || s.welcome.Height != d.height || s.welcome.Left != d.left || s.welcome.Top != d.top || h.cfg.area.width != 64 {
		t.Fatal("pairing reused stale listener geometry", s.welcome, d)
	}
	// No start message: there is no real desktop encoder/cursor/input loop.
}

func TestAutomaticResolutionSessionAndReconnect(t *testing.T) {
	for _, kind := range []string{"jpeg-udp", "jpeg-tcp", "gpu-udp", "gpu-tcp"} {
		t.Run(kind, func(t *testing.T) {
			gpu := kind == "gpu-udp" || kind == "gpu-tcp"
			if gpu && (os.Getenv("MYDESK_LAN_GPU_TEST") != "1" || os.Getenv("MYDESK_LAN_MEDIA_TEST") != "1" || os.Getenv("MYDESK_LAN_UI_TEST") != "1") {
				t.Skip("real native GPU/media session opt-in")
			}
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
			var layoutMu sync.Mutex
			area := desktop{width: 320, height: 180}
			layout := func(c videoConfig) []videoConfig {
				layoutMu.Lock()
				defer layoutMu.Unlock()
				c.area, c.display = area, "RESOLUTION_TEST"
				if !gpu {
					pixels := testDesktop(area.width, area.height)
					c.grab = func() ([]byte, error) { return pixels, nil }
				}
				return []videoConfig{c}
			}
			cfg := videoConfig{display: "RESOLUTION_TEST", fps: 60, quality: 100, h264Quality: 8, capture: "auto", source: "test", encoder: "jpeg", area: area}
			if gpu {
				cfg.encoder = "libx264"
				cfg.ffmpeg, _ = filepath.Abs(filepath.Join("..", "..", "runtime", "ffmpeg.exe"))
			}
			clip := &fakeClipboard{}
			clip.copy("ignore", nil)
			h := &host{code: "resolution-test", queueGrace: true, udpSmoothing: true, cfg: cfg, screenList: layout, clipStore: clip}
			go func() {
				for {
					c, err := ln.Accept()
					if err != nil {
						return
					}
					go h.accept(ctx, c.(*tls.Conn), false)
				}
			}()
			tcp := kind == "jpeg-tcp" || kind == "gpu-tcp"
			connect := func() (*controllerSession, func()) {
				parent := ctx
				if gpu {
					parent = context.WithValue(ctx, decoderBackendKey{}, "mf")
				}
				sessionCtx, stop := context.WithCancel(parent)
				s, err := connectController(sessionCtx, stop, ln.Addr().String(), h.code, 60, "", !gpu, net.IPv4(127, 0, 0, 1))
				if err != nil {
					stop()
					t.Fatal(err)
				}
				s.view = &viewer{width: s.welcome.Width, height: s.welcome.Height, screenGeneration: 1, onInput: func(inputEvent) {}}
				if gpu {
					s.gpuDecode = "auto"
				}
				if err := s.start(tcp, false); err != nil {
					s.close()
					t.Fatal(err)
				}
				var done chan error
				if gpu {
					done = make(chan error, 1)
					go func() { done <- s.view.run() }()
				}
				var closeOnce sync.Once
				close := func() {
					closeOnce.Do(func() {
						s.close()
						if done != nil {
							s.view.mu.Lock()
							window := s.view.hwnd
							s.view.mu.Unlock()
							if window != 0 {
								ucall("PostMessageW", window, 0x10, 0, 0)
							}
							<-done
						}
						waitJPEG(t, func() bool { h.mu.Lock(); defer h.mu.Unlock(); return h.session == nil })
					})
				}
				t.Cleanup(close)
				return s, close
			}
			s, closeFirst := connect()
			waitJPEG(t, func() bool { return s.readyScreen.Load() == 1 })
			if gpu {
				waitJPEG(t, func() bool { s.view.mu.Lock(); defer s.view.mu.Unlock(); return s.view.hwnd != 0 })
			}
			originalCtrl, originalVideo := s.ctrl, s.video
			localClip := &fakeClipboard{}
			localClip.copy("ignore", nil)
			var syncState clipboardSyncState
			for i, next := range []desktop{{width: 640, height: 360}, {left: -720, top: -40, width: 720, height: 480}, {width: 320, height: 180}} {
				layoutMu.Lock()
				area = next
				layoutMu.Unlock()
				generation := uint64(i + 2)
				waitJPEG(t, func() bool { return s.readyScreen.Load() == generation })
				desc := s.currentScreen()
				s.view.mu.Lock()
				valid := s.view.width == next.width && s.view.height == next.height && s.view.repairs.count == 0
				frame := s.view.frames.latest
				if gpu {
					valid = valid && frame != nil && frame.native != nil
					if valid {
						fw, fh := frame.native.size()
						valid = fw == next.width && fh == next.height && s.activeDecoder.Load() == 1
					}
				} else {
					valid = valid && len(s.view.pixels) == next.width*next.height*4
				}
				s.view.mu.Unlock()
				if !valid || desc.Width != next.width || desc.Height != next.height || desc.Left != next.left || desc.Top != next.top || s.ctrl != originalCtrl || s.video != originalVideo || s.ctx.Err() != nil {
					t.Fatal("automatic geometry/video transition incomplete", desc, next)
				}
				if _, err := s.remoteListRequest(ctx, "", "file-default"); err != nil {
					t.Fatal("F8 auxiliary disconnected", err)
				}
				if _, err := s.runCommand(ctx, programDirectory(), "echo resize-ok", 1); err != nil {
					t.Fatal("F7 auxiliary disconnected", err)
				}
				want := []byte(fmt.Sprintf("clipboard resize %d", generation))
				localClip.copy("text", want)
				if err := syncClipboardRound(ctx, s, localClip, &syncState); err != nil {
					t.Fatal("clipboard auxiliary disconnected", err)
				}
				got, _ := clip.snapshot(ctx)
				if !bytes.Equal(got.Data, want) {
					t.Fatal("clipboard content lost after resize")
				}
			}
			closeFirst()
			// Host process/config stays alive and still contains its startup size.
			// Change the desktop while disconnected, then reconnect the master.
			layoutMu.Lock()
			area = desktop{width: 640, height: 360}
			layoutMu.Unlock()
			s2, closeSecond := connect()
			defer closeSecond()
			waitJPEG(t, func() bool { return s2.readyScreen.Load() == 1 })
			if h.cfg.area.width != 320 || s2.welcome.Width != 640 || s2.welcome.Height != 360 || s2.ctx.Err() != nil {
				t.Fatal("reconnect copied stale startup resolution", s2.welcome)
			}
			t.Logf("%s: 3 automatic mode/origin changes and offline reconnect; authenticated channels preserved", kind)
		})
	}
}

func TestEncodedScreenDimensionGuard(t *testing.T) {
	if os.Getenv("MYDESK_LAN_MEDIA_TEST") != "1" {
		t.Skip("real H.264 SPS opt-in")
	}
	ff, _ := filepath.Abs(filepath.Join("..", "..", "runtime", "ffmpeg.exe"))
	data, err := helperCommand(context.Background(), ff, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=192x96:rate=60", "-frames:v", "1", "-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency", "-f", "h264", "pipe:1").Output()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte{0, 0, 0, 1}) || !encodedScreenMatches(data, desktop{width: 192, height: 96}) || encodedScreenMatches(data, desktop{width: 128, height: 64}) {
		t.Fatal("new resolution emitted under stale descriptor")
	}
	c := videoConfig{source: "test", area: desktop{width: 192, height: 96}, fps: 60, quality: 100, bitrate: 4000}
	args := packetProfileArgs(c, "libx264", "test")
	args = append(args[:len(args)-1], "-frames:v", "3", "pipe:1")
	packets, err := helperCommand(context.Background(), ff, args...).Output()
	if err != nil || !validEncoderPacketsAt(packets, c.area) || validEncoderPacketsAt(packets, desktop{width: 128, height: 64}) {
		t.Fatal("probe accepted stale capture dimensions", err)
	}
}
