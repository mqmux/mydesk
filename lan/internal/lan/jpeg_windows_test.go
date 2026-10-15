//go:build windows && amd64

package lan

import (
	"bytes"
	"context"
	"crypto/tls"
	"image"
	"net"
	"testing"
	"time"
)

func waitJPEG(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("JPEG pipeline did not reach expected state")
}

func TestJPEGSupportsOddDesktopDimensions(t *testing.T) {
	profile, err := selectProfile(context.Background(), videoConfig{encoder: "auto", ffmpeg: "not-invoked", area: desktop{width: 193, height: 131}})
	if err != nil || profile.codec != codecJPEGDelta {
		t.Fatal("odd screen cannot fall back to JPEG")
	}
	encoder := jpegDeltaEncoder{width: 193, height: 131, quality: 90}
	data, key, err := encoder.encode(testDesktop(193, 131), time.Now())
	if err != nil || !key {
		t.Fatal("odd JPEG screen encoding failed")
	}
	if _, err := decodeJPEGDelta(data, 193, 131, true); err != nil {
		t.Fatal(err)
	}
}

func TestJPEGDeltaSessionWithoutRuntime(t *testing.T) {
	for _, tcp := range []bool{false, true} {
		name := "UDP"
		if tcp {
			name = "TCP"
		}
		t.Run(name, func(t *testing.T) {
			// Keep the old JPEG-only decoder-capability negotiation regression;
			// the native-only H.264 negotiation has its own actual media test.
			ctx, cancel := context.WithCancel(context.WithValue(context.Background(), decoderBackendKey{}, "ffmpeg"))
			defer cancel()
			cfg, err := ephemeralTLS()
			if err != nil {
				t.Fatal(err)
			}
			ln, err := tls.Listen("tcp4", "127.0.0.1:0", cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			h := &host{code: "jpeg-test-code", cfg: videoConfig{fps: 30, quality: 90, bitrate: 20000, area: desktop{width: 320, height: 180}, source: "test", encoder: "auto", ffmpeg: "not-installed-for-test"}}
			go func() {
				for {
					c, err := ln.Accept()
					if err != nil {
						return
					}
					go h.accept(ctx, c.(*tls.Conn), false)
				}
			}()
			s, err := connectController(ctx, cancel, ln.Addr().String(), h.code, 30, "", false, net.IPv4(127, 0, 0, 1))
			if err != nil {
				t.Fatal(err)
			}
			defer s.close()
			if s.welcome.Codec != codecJPEGDelta {
				t.Fatal("controller lacking runtime did not negotiate regional JPEG")
			}
			s.view = &viewer{width: 320, height: 180, onInput: func(inputEvent) {}}
			if err := s.start(tcp, false); err != nil {
				t.Fatal(err)
			}
			waitJPEG(t, func() bool { return s.decoded.Load() >= 20 })
			if s.transport.Load() != tcp || s.dropped.Load() != 0 {
				t.Fatalf("transport/loss mismatch: tcp=%t lost=%d", s.transport.Load(), s.dropped.Load())
			}
			s.view.mu.Lock()
			pixels := bytes.Clone(s.view.pixels)
			s.view.mu.Unlock()
			if len(pixels) != 320*180*4 {
				t.Fatal("no composed desktop")
			}
			t.Logf("%s no-runtime JPEG: %d decoded regional updates, %d lost", name, s.decoded.Load(), s.dropped.Load())
		})
	}
}

func TestJPEGIdleDoesNotFallBackTCP(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg, err := ephemeralTLS()
	if err != nil {
		t.Fatal(err)
	}
	ln, err := tls.Listen("tcp4", "127.0.0.1:0", cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	pixels := testDesktop(320, 180)
	h := &host{code: "idle-test-code", cfg: videoConfig{fps: 30, quality: 90, bitrate: 20000, area: desktop{width: 320, height: 180}, encoder: "jpeg", grab: func() ([]byte, error) { return pixels, nil }}}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go h.accept(ctx, c.(*tls.Conn), false)
		}
	}()
	s, err := connectController(ctx, cancel, ln.Addr().String(), h.code, 30, "", false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	s.view = &viewer{width: 320, height: 180, onInput: func(inputEvent) {}}
	if err := s.start(false, false); err != nil {
		t.Fatal(err)
	}
	waitJPEG(t, func() bool { return s.decoded.Load() == 1 })
	time.Sleep(2500 * time.Millisecond)
	if s.transport.Load() || s.decoded.Load() != 1 || s.received.Load() < 5 {
		t.Fatalf("idle heartbeat failed: tcp=%t decoded=%d received=%d", s.transport.Load(), s.decoded.Load(), s.received.Load())
	}
}

func TestJPEGDecoderGapRecovery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &controllerSession{ctx: ctx, cancel: cancel, welcome: message{Width: 192, Height: 128}, frames: make(chan videoFrame, 16), commands: make(chan message, 16), view: &viewer{width: 192, height: 128}}
	done := make(chan struct{})
	go func() { defer close(done); s.decode() }()
	defer func() { cancel(); <-done }()
	e := jpegDeltaEncoder{width: 192, height: 128, quality: 90}
	now := time.Now()
	pixels := testDesktop(192, 128)
	full, _, _ := e.encode(pixels, now)
	paintTestRect(pixels, 192, image.Rect(1, 1, 8, 8))
	delta2, _, _ := e.encode(pixels, now.Add(20*time.Millisecond))
	paintTestRect(pixels, 192, image.Rect(130, 1, 140, 8))
	delta3, _, _ := e.encode(pixels, now.Add(40*time.Millisecond))
	push := func(id, epoch uint64, key bool, data []byte, at time.Time) {
		s.frames <- videoFrame{id: id, epoch: epoch, key: key, data: data, codec: codecJPEGDelta, at: at}
	}
	push(1, 1, true, full, time.Now())
	waitJPEG(t, func() bool { return s.decoded.Load() == 1 })
	push(3, 1, false, delta3, time.Now())
	time.Sleep(20 * time.Millisecond)
	if s.decoded.Load() != 1 {
		t.Fatal("out-of-order patch committed")
	}
	push(2, 1, false, delta2, time.Now())
	waitJPEG(t, func() bool { return s.decoded.Load() == 3 })
	heartbeat, _, _ := e.encode(pixels, now.Add(time.Second))
	push(5, 1, false, heartbeat, time.Now().Add(-150*time.Millisecond)) // update 4 was lost
	push(6, 1, false, heartbeat, time.Now())
	waitJPEG(t, func() bool { return len(s.commands) > 0 })
	if m := <-s.commands; m.Type != "keyframe" {
		t.Fatal("gap did not request full resync")
	}
	if s.decoded.Load() != 3 {
		t.Fatal("dependent patches applied after loss")
	}
	paintTestRect(pixels, 192, image.Rect(70, 70, 90, 90))
	fresh := jpegDeltaEncoder{width: 192, height: 128, quality: 90}
	full, _, _ = fresh.encode(pixels, now)
	push(7, 2, true, full, time.Now())
	waitJPEG(t, func() bool { return s.decoded.Load() == 4 })
	patches, _ := decodeJPEGDelta(full, 192, 128, true)
	expected := make([]byte, len(pixels))
	applyJPEGPatches(expected, 192, patches)
	s.view.mu.Lock()
	equal := bytes.Equal(s.view.pixels, expected)
	s.view.mu.Unlock()
	if !equal {
		t.Fatal("resync retained stale regions")
	}
}
