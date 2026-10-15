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
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

func TestContinuousCaptureKeepsQualityAndFPS(t *testing.T) {
	c := videoConfig{fps: 120, bitrate: 80000, h264Quality: 8, hideCursor: true, area: desktop{width: 1920, height: 1080}}
	for _, encoder := range []string{"h264_nvenc", "h264_amf", "h264_qsv", "libx264"} {
		for _, capture := range []string{"ddagrab", "gdi", "test"} {
			before := strings.Join(packetProfileArgs(c, encoder, capture), " ")
			if capture == "ddagrab" && (!strings.Contains(before, "framerate=120") || !strings.Contains(before, "dup_frames=1")) {
				t.Fatal("continuous frame cadence was not restored", before)
			}
			s := &hostSession{cfg: c}
			for _, minimized := range []bool{true, false, true, false} {
				s.setViewerMinimized(minimized)
				cfg, _, _ := s.screenConfig()
				if after := strings.Join(packetProfileArgs(cfg, encoder, capture), " "); after != before {
					t.Fatal("minimize changed capture/quality/bitrate/FPS or restarted cadence", encoder, capture)
				}
			}
		}
	}
}

func TestWarmIdleFeatureNegotiation(t *testing.T) {
	for _, feature := range []string{"", warmIdleFeature, minimizeFeature} {
		t.Run("feature-"+feature, func(t *testing.T) {
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
			h := &host{code: "idle-feature-test", cfg: videoConfig{encoder: "jpeg", fps: 60, bitrate: 40000, quality: 100, area: desktop{width: 64, height: 64}}}
			go func() {
				c, e := ln.Accept()
				if e == nil {
					h.accept(ctx, c.(*tls.Conn), false)
				}
			}()
			c, err := dialTLS(ctx, ln.Addr().String(), net.IPv4(127, 0, 0, 1))
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			c.SetDeadline(time.Now().Add(3 * time.Second))
			w := &wire{conn: c}
			features := []string{screenSwitchFeature}
			if feature != "" {
				features = append(features, feature)
			}
			if err := w.send(message{Type: "pair", Version: protocolVersion, Proof: mustProof(c, h.code, "controller"), FPS: 60, Features: features}, nil); err != nil {
				t.Fatal(err)
			}
			if m, _, err := w.read(); err != nil || m.Type != "paired" {
				t.Fatal("pair", m, err)
			}
			welcome, _, err := w.read()
			if err != nil || welcome.Type != "welcome" || backgroundFeatureFor(welcome.Features) != feature {
				t.Fatal("welcome", welcome, err)
			}
			h.mu.Lock()
			s := h.session
			h.mu.Unlock()
			if s == nil || s.warmIdle != (feature != "") || !strings.Contains(strings.Join(packetProfileArgs(s.cfg, "libx264", "ddagrab"), " "), "dup_frames=1") {
				t.Fatal("a peer feature re-enabled irregular capture")
			}
		})
	}
}

func TestMinimizeOnlyFeatureDoesNotEnableLegacyIdleCapture(t *testing.T) {
	features := controllerSessionFeatures()
	if !hasFeature(features, minimizeFeature) || !hasFeature(features, screenSwitchFeature) || hasFeature(features, warmIdleFeature) {
		t.Fatal("new controller enables legacy 4.13 update-driven capture", features)
	}
	if backgroundFeatureFor([]string{warmIdleFeature, minimizeFeature}) != minimizeFeature {
		t.Fatal("minimize-only feature not preferred")
	}
}

func TestWarmIdleUDPKeepaliveAndResume(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	cipher, _ := makePacketCipher(make([]byte, 36))
	s := &controllerSession{ctx: ctx, cancel: cancel, udp: client, rx: cipher, commands: make(chan message, 16), frames: make(chan videoFrame, 16), welcome: message{Features: []string{warmIdleFeature}}}
	s.lastUDP.Store(uint64(time.Now().UnixNano()))
	done := make(chan struct{})
	go func() { defer close(done); s.readUDP(server.LocalAddr().(*net.UDPAddr)) }()
	defer func() { cancel(); client.Close(); <-done }()
	send := func(f fragment) {
		t.Helper()
		if _, err := server.WriteToUDP(cipher.seal(f), client.LocalAddr().(*net.UDPAddr)); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 27; i++ {
		send(fragment{kind: packetKeepalive})
		time.Sleep(100 * time.Millisecond)
	}
	if s.transport.Load() || s.received.Load() != 0 || len(s.frames) != 0 {
		t.Fatal("idle keepalive triggered fallback or unnecessary decode")
	}
	started := time.Now()
	send(fragment{kind: packetVideo, frame: 1, epoch: 1, total: 1, codec: codecH264, key: true, payload: []byte{1, 2, 3}})
	select {
	case f := <-s.frames:
		if f.id != 1 || time.Since(started) > 200*time.Millisecond {
			t.Fatal("idle polling delayed new video", time.Since(started))
		}
	case <-time.After(time.Second):
		t.Fatal("no picture after idle")
	}
	// The receiver must still fall back when only the tiny keepalive makes it
	// through and all newly advertised video datagrams are blackholed.
	for i := 0; i < 26 && !s.transport.Load(); i++ {
		send(fragment{kind: packetKeepalive, frame: 2})
		time.Sleep(100 * time.Millisecond)
	}
	if !s.transport.Load() {
		t.Fatal("video blackhole was hidden by healthy keepalives")
	}
}

func TestWarmIdleHostKeepaliveRefreshesExpiredDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	cipher, _ := makePacketCipher(make([]byte, 36))
	s := &hostSession{ctx: ctx, cancel: cancel, udp: server, tx: cipher, peer: client.LocalAddr().(*net.UDPAddr)}
	s.minimized.Store(true)
	s.lastSentFrame.Store(17)
	server.SetWriteDeadline(time.Now().Add(-time.Second))
	done := make(chan struct{})
	go func() { defer close(done); s.videoKeepaliveLoop() }()
	defer func() { cancel(); <-done }()
	b := make([]byte, packetSize)
	client.SetReadDeadline(time.Now().Add(1500 * time.Millisecond))
	n, _, err := client.ReadFromUDP(b)
	if err != nil {
		t.Fatal("keepalive inherited expired video deadline", err)
	}
	f, err := cipher.open(b[:n])
	if err != nil || f.kind != packetKeepalive || f.frame != 17 || s.frameID.Load() != 0 || s.sent.Load() != 0 {
		t.Fatal("keepalive consumed video sequence", f, err)
	}
	s.setViewerMinimized(false)
	client.SetReadDeadline(time.Now().Add(650 * time.Millisecond))
	if _, _, err := client.ReadFromUDP(b); err == nil {
		t.Fatal("foreground still sends extra keepalives")
	} else if timeout, ok := err.(net.Error); !ok || !timeout.Timeout() {
		t.Fatal("unexpected receive failure", err)
	}
	s.tcp.Store(true)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("TCP kept UDP heartbeat polling alive")
	}
}

func TestWarmIdleRefineMinimizeAndImmediateRestore(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	readerDone := make(chan struct{})
	var patches atomic.Uint64
	go func() {
		defer close(readerDone)
		w := &wire{conn: b}
		for {
			if m, _, err := w.read(); err != nil {
				return
			} else if m.Type == "refine-patch" {
				patches.Add(1)
			}
		}
	}()
	var captures atomic.Uint64
	sampled := make(chan time.Time, 32)
	s := &hostSession{ctx: ctx, cancel: cancel, video: &wire{conn: a}, restart: make(chan struct{}, 1), refineWake: make(chan struct{}, 1), cfg: videoConfig{area: desktop{width: 64, height: 64}, grab: func() ([]byte, error) {
		captures.Add(1)
		select {
		case sampled <- time.Now():
		default:
		}
		return testDesktop(64, 64), nil
	}}}
	s.cursorHidden.Store(true)
	done := make(chan struct{})
	go func() { defer close(done); s.regionalRefineLoop() }()
	defer func() { cancel(); a.Close(); <-done; b.Close(); <-readerDone }()
	waitJPEG(t, func() bool { return captures.Load() >= 5 })
	waitJPEG(t, func() bool { return patches.Load() > 0 })
	s.setViewerMinimized(true)
	time.Sleep(150 * time.Millisecond) // Allow a sampling operation already in flight.
	before := captures.Load()
	beforePatches := patches.Load()
	time.Sleep(1100 * time.Millisecond)
	if captures.Load() != before {
		t.Fatal("background repair kept capturing", before, captures.Load())
	}
	for len(sampled) > 0 {
		<-sampled
	}
	start := time.Now()
	s.setViewerMinimized(false)
	select {
	case at := <-sampled:
		if at.Sub(start) > 200*time.Millisecond {
			t.Fatal("restore waited for background polling interval", at.Sub(start))
		}
	case <-time.After(time.Second):
		t.Fatal("repair did not resume")
	}
	waitJPEG(t, func() bool { return patches.Load() > beforePatches })
	if s.epoch.Load() != 0 || len(s.restart) != 0 || s.clipboardDisabled.Load() || s.inputDisabled.Load() {
		t.Fatal("minimize changed encoder or non-video features")
	}
}

func TestWarmIdleViewerStateCompatibilityAndLatestFrame(t *testing.T) {
	s := &controllerSession{ctx: context.Background(), commands: make(chan message, 8)}
	s.setViewerMinimized(true)
	if len(s.commands) != 0 {
		t.Fatal("sent unsupported state to old client")
	}
	s.welcome.Features = []string{warmIdleFeature}
	s.setViewerMinimized(false)
	m := <-s.commands
	if m.Type != "viewer-state" || m.Minimized == nil || *m.Minimized {
		t.Fatal(m)
	}
	transitions := 0
	v := &viewer{width: 2, height: 2, onMinimized: func(bool) { transitions++ }}
	for i := 0; i < 30; i++ {
		v.setMinimized(true)
		v.setFrame(bytes.Repeat([]byte{byte(i)}, 16))
	}
	v.setMinimized(false)
	if transitions != 2 || v.frames.latest.pixels[0] != 29 || v.frames.sequence != 30 {
		t.Fatal("restore lost latest image or repeated transition", transitions)
	}
}

func TestNativeWarmIdleDoesNotPostBackgroundPaint(t *testing.T) {
	if os.Getenv("MYDESK_LAN_UI_TEST") != "1" {
		t.Skip("native UI opt-in")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	inst, _, _ := kernel32.NewProc("GetModuleHandleW").Call(0)
	h, _, _ := ucall("CreateWindowExW", 0, uintptr(unsafe.Pointer(utf16("STATIC"))), 0, 0xCF0000, 0, 0, 64, 64, 0, 0, inst, 0)
	if h == 0 {
		t.Fatal("test window missing")
	}
	defer ucall("DestroyWindow", h)
	v := &viewer{hwnd: h, width: 2, height: 2}
	v.setMinimized(true)
	for i := 0; i < 100; i++ {
		v.setFrame(bytes.Repeat([]byte{byte(i)}, 16))
	}
	if v.paintPending.Load() {
		t.Fatal("posted background render work")
	}
	v.setMinimized(false)
	v.requestFramePaint(h)
	if !v.paintPending.Load() || v.frames.latest.pixels[0] != 99 {
		t.Fatal("latest frame not immediately paintable")
	}
}

func TestWarmIdleH264DecoderStaysReady(t *testing.T) {
	if os.Getenv("MYDESK_LAN_MEDIA_TEST") != "1" {
		t.Skip("real decoder opt-in")
	}
	ff, _ := filepath.Abs(filepath.Join("..", "..", "runtime", "ffmpeg.exe"))
	c := videoConfig{fps: 120, bitrate: 80000, h264Quality: 8, area: desktop{width: 128, height: 64}}
	args := packetProfileArgs(c, "libx264", "test")
	args = append(args[:len(args)-1], "-frames:v", "12", "pipe:1")
	data, err := helperCommand(context.Background(), ff, args...).Output()
	if err != nil {
		t.Fatal(err)
	}
	var frames []videoFrame
	if err := readEncoderPackets(bytes.NewReader(data), func(b []byte, key bool) error {
		frames = append(frames, videoFrame{id: uint64(len(frames) + 1), epoch: 1, codec: codecH264, key: key, data: b})
		return nil
	}); err != nil || len(frames) != 12 {
		t.Fatal("fixture", err)
	}
	for _, cfg := range []struct {
		backend  string
		software bool
	}{{"mf", true}, {"ffmpeg", true}, {"mf", false}, {"ffmpeg", false}} {
		t.Run(fmt.Sprintf("%s-software-%t", cfg.backend, cfg.software), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s := &controllerSession{ctx: ctx, cancel: cancel, welcome: message{Width: 128, Height: 64, FPS: 120}, frames: make(chan videoFrame, 32), commands: make(chan message, 32), view: &viewer{width: 128, height: 64}, software: cfg.software, decoder: cfg.backend, ffmpeg: ff}
			done := make(chan struct{})
			go func() { defer close(done); s.decode() }()
			defer func() { cancel(); <-done }()
			first := frames[0]
			first.at = time.Now()
			s.deliver(first)
			waitJPEG(t, func() bool { return s.decoded.Load() == 1 })
			for _, f := range frames[1:6] {
				f.at = time.Now()
				s.deliver(f)
			}
			waitJPEG(t, func() bool { return s.decoded.Load() == 6 })
			s.view.setMinimized(true)
			time.Sleep(2600 * time.Millisecond)
			before := s.decoded.Load()
			s.view.setMinimized(false)
			began := time.Now()
			f := frames[6] // P picture: succeeds only with the retained reference chain.
			f.at = began
			s.deliver(f)
			waitJPEG(t, func() bool { return s.decoded.Load() == before+1 })
			elapsed := time.Since(began)
			if elapsed > 250*time.Millisecond || s.ctx.Err() != nil || s.dropped.Load() != 0 || len(s.commands) != 0 {
				t.Fatal("decoder restarted, buffered or requested recovery after idle", elapsed, len(s.commands))
			}
			t.Logf("same-epoch P picture after 2.6s idle/minimize: %v; decoder retained", elapsed)
		})
	}
}

func TestWarmIdleMinimizeKeepsAuxiliary(t *testing.T) {
	s, remote := auxiliaryFixture(t)
	waitJPEG(t, func() bool { return s.decoded.Load() > 0 })
	before := s.currentScreen()
	ctrl, video := s.ctrl, s.video
	s.view.setMinimized(true)
	local := &fakeClipboard{}
	local.copy("ignore", nil)
	var syncState clipboardSyncState
	if err := syncClipboardRound(s.ctx, s, local, &syncState); err != nil {
		t.Fatal(err)
	}
	local.copy("text", []byte("clipboard while minimized"))
	if err := syncClipboardRound(s.ctx, s, local, &syncState); err != nil {
		t.Fatal("clipboard paused with viewer", err)
	}
	got, _ := remote.snapshot(s.ctx)
	if string(got.Data) != "clipboard while minimized" {
		t.Fatal("clipboard mismatch")
	}
	dir := t.TempDir()
	source, target, download := filepath.Join(dir, "source.bin"), filepath.Join(dir, "remote.bin"), filepath.Join(dir, "download.bin")
	payload := bytes.Repeat([]byte("background file"), 8192)
	if err := os.WriteFile(source, payload, 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.uploadFile(s.ctx, source, target, false, nil); err != nil {
		t.Fatal("background upload", err)
	}
	if err := s.downloadFile(s.ctx, target, download, false, nil); err != nil {
		t.Fatal("background download", err)
	}
	data, err := os.ReadFile(download)
	if err != nil || !bytes.Equal(data, payload) {
		t.Fatal("background file corrupt", err)
	}
	r, err := s.runCommand(s.ctx, dir, "echo warm-idle-command", 10)
	if err != nil || !strings.Contains(r.Output, "warm-idle-command") {
		t.Fatal("background command", err, r)
	}
	s.view.setMinimized(false)
	// Commands and file traffic have traversed the same authenticated session;
	// minimizing/restoring must not renew the encoder epoch or screen origin.
	if s.ctx.Err() != nil || s.currentScreen().Epoch != before.Epoch || s.ctrl != ctrl || s.video != video || s.clipboardDisabled.Load() || s.inputDisabled.Load() {
		t.Fatal("restoring changed session or unrelated options")
	}
}

// Passive desktop A/B. No mouse/keyboard injection, foreground changes or
// desktop manipulation. Results depend on what the user's desktop is doing;
// report CPU seconds and picture counts, never assert a savings percentage.
func TestWarmIdleDesktopResourceProbe(t *testing.T) {
	if os.Getenv("MYDESK_LAN_IDLE_TEST") != "1" {
		t.Skip("passive desktop resource probe opt-in")
	}
	outputs := dxgiOutputs()
	if len(outputs) == 0 {
		t.Fatal("no DXGI outputs")
	}
	ff, _ := filepath.Abs(filepath.Join("..", "..", "runtime", "ffmpeg.exe"))
	for _, idle := range []bool{false, true} {
		t.Run(fmt.Sprintf("idle-%t", idle), func(t *testing.T) {
			c := videoConfig{fps: 120, monitor: 0, hideCursor: true, h264Quality: 8, bitrate: 80000, area: outputs[0]}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			args := packetProfileArgs(c, "libx264", "ddagrab")
			if idle {
				// Historical 4.13 comparison only; production has no such mode.
				for i := range args {
					args[i] = strings.Replace(args[i], "dup_frames=1", "dup_frames=0", 1)
				}
			}
			cmd := helperCommand(ctx, ff, args...)
			out, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			var stderr tailBuffer
			cmd.Stderr = &stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			handle, err := syscall.OpenProcess(0x1000, false, uint32(cmd.Process.Pid))
			if err != nil {
				cancel()
				cmd.Wait()
				t.Fatal(err)
			}
			defer syscall.CloseHandle(handle)
			cpu := func() uint64 {
				var created, exited, kernel, user syscall.Filetime
				ok, _, e := kernel32.NewProc("GetProcessTimes").Call(uintptr(handle), uintptr(unsafe.Pointer(&created)), uintptr(unsafe.Pointer(&exited)), uintptr(unsafe.Pointer(&kernel)), uintptr(unsafe.Pointer(&user)))
				if ok == 0 {
					t.Fatal(e)
				}
				return (uint64(kernel.HighDateTime)<<32 | uint64(kernel.LowDateTime)) + (uint64(user.HighDateTime)<<32 | uint64(user.LowDateTime))
			}
			var frames, bytes atomic.Uint64
			first := make(chan struct{}, 1)
			done := make(chan error, 1)
			go func() {
				done <- readEncoderPackets(out, func(data []byte, key bool) error {
					frames.Add(1)
					bytes.Add(uint64(len(data)))
					select {
					case first <- struct{}{}:
					default:
					}
					return nil
				})
			}()
			defer func() { cancel(); <-done; cmd.Wait() }()
			select {
			case <-first:
			case err := <-done:
				// Put it back so the balanced shutdown still joins the reader.
				done <- err
				t.Fatal("desktop encoder", err, stderr.String())
			case <-time.After(5 * time.Second):
				t.Fatal("no initial desktop picture", stderr.String())
			}
			time.Sleep(500 * time.Millisecond)
			beforeCPU, beforeFrames, beforeBytes := cpu(), frames.Load(), bytes.Load()
			time.Sleep(3 * time.Second)
			t.Logf("desktop %dx%d Q100 120-target libx264: pictures=%d, encoded=%d bytes, encoder CPU=%.3fs / 3s (sum of thread time)", c.area.width, c.area.height, frames.Load()-beforeFrames, bytes.Load()-beforeBytes, float64(cpu()-beforeCPU)/1e7)
		})
	}
}
