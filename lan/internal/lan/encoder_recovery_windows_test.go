//go:build windows && amd64

package lan

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestHostStreamRecoveryWithoutFixedPause(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	a, b := net.Pipe()
	pixels := testDesktop(64, 64)
	s := &hostSession{ctx: ctx, cancel: cancel, video: &wire{conn: a}, restart: make(chan struct{}, 1), profile: encoderProfile{codec: codecJPEGDelta}, cfg: videoConfig{fps: 120, quality: 100, area: desktop{width: 64, height: 64}, grab: func() ([]byte, error) { return pixels, nil }}}
	s.tcp.Store(true)
	s.bitrate.Store(80000)
	done := make(chan struct{})
	go func() { defer close(done); s.stream() }()
	defer func() { cancel(); a.Close(); b.Close(); <-done }()
	reader := &wire{conn: b, timeout: time.Second}
	first, _, err := reader.read()
	if err != nil || !first.Key || first.Epoch != 1 {
		t.Fatal("missing initial full reference", first, err)
	}
	// sendFrames marks readiness just after writing the first picture.
	deadline := time.Now().Add(time.Second)
	for s.sent.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if s.sent.Load() == 0 {
		t.Fatal("initial key was not acknowledged by sender")
	}
	start := time.Now()
	for i := 0; i < 100; i++ {
		s.requestRepair(1, true)
	}
	var recovered message
	for recovered.Epoch != 2 {
		recovered, _, err = reader.read()
		if err != nil {
			t.Fatal(err)
		}
	}
	lag := time.Since(start)
	if !recovered.Key || lag >= 125*time.Millisecond {
		t.Fatalf("recovery kept fixed pause or lost full reference: key=%t lag=%v", recovered.Key, lag)
	}
	// Stale queued notifications from the same repair must not restart again.
	time.Sleep(60 * time.Millisecond)
	if s.epoch.Load() != 2 {
		t.Fatal("coalesced repair requests restarted fresh encoder", s.epoch.Load())
	}
	// An actual configuration change remains effective after coalescing.
	s.cursorHidden.Store(true)
	s.requestRestart()
	for recovered.Epoch != 3 {
		recovered, _, err = reader.read()
		if err != nil {
			t.Fatal(err)
		}
	}
	if !recovered.Key {
		t.Fatal("configuration restart did not produce full reference")
	}
	t.Logf("JPEG host repair request -> fresh full reference %v; 100 requests coalesced; later configuration applied", lag)
}

func TestControllerQueueAllowsStartupButBoundsSteadyState(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &controllerSession{ctx: ctx, cancel: cancel, frames: make(chan videoFrame, 32), commands: make(chan message, 8)}
	s.warmupUntil.Store(time.Now().Add(time.Second).UnixNano())
	s.decoded.Store(1)
	for i := 0; i < 24; i++ {
		s.deliver(videoFrame{id: uint64(i + 1)})
	}
	if len(s.frames) != 24 || s.dropped.Load() != 0 || len(s.commands) != 0 {
		t.Fatal("normal hardware startup forced reference-chain recovery")
	}
	for len(s.frames) > 0 {
		<-s.frames
	}
	s.warmupUntil.Store(0)
	for i := 0; i < 5; i++ {
		s.deliver(videoFrame{id: uint64(i + 25)})
	}
	if len(s.frames) != 4 || s.dropped.Load() != 1 || s.decodePressure.Load() != 1 || len(s.commands) != 1 {
		t.Fatal("startup capacity increased steady-state queue latency")
	}
}
