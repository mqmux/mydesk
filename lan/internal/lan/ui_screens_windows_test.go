//go:build windows && amd64

package lan

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"image/png"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
	"unsafe"
)

func TestFileListABIAndSort(t *testing.T) {
	if unsafe.Sizeof(notifyHeader{}) != 24 || unsafe.Sizeof(listItem{}) != 88 || unsafe.Offsetof(listItem{}.Text) != 24 || unsafe.Sizeof(listColumn{}) != 56 || unsafe.Sizeof(customDraw{}) != 80 || unsafe.Sizeof(listNotification{}) != 64 {
		t.Fatal("Win64 list-view ABI mismatch")
	}
	entries := []fileEntry{{Name: "long name.txt", Path: "long", Size: 2048}, {Name: "folder", Path: "folder", Directory: true}, {Name: "a.bin", Path: "a", Size: 12}}
	sortFileEntries(entries, 1, true)
	if entries[0].Path != "folder" || entries[1].Path != "long" || entries[2].Path != "a" {
		t.Fatal(entries)
	}
	if row := fileColumns(entries[0]); row[0] != `folder\` || row[1] != "" {
		t.Fatal(row)
	}
	if row := fileColumns(entries[1]); row[0] != "long name.txt" || row[1] != "2.0 KB" {
		t.Fatal(row)
	}
	sortFileEntries(entries, 0, false)
	if entries[1].Path != "a" {
		t.Fatal(entries)
	}
}

func TestNativeHUDHoverWithoutActivation(t *testing.T) {
	if os.Getenv("MYDESK_LAN_UI_TEST") != "1" {
		t.Skip("native UI opt-in")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	inst, _, _ := kernel32.NewProc("GetModuleHandleW").Call(0)
	h, _, _ := ucall("CreateWindowExW", 0, uintptr(unsafe.Pointer(utf16("STATIC"))), 0, 0xCF0000, 0, 0, 640, 480, 0, 0, inst, 0)
	if h == 0 {
		t.Fatal("test parent missing")
	}
	defer ucall("DestroyWindow", h)
	resets := 0
	v := &viewer{hwnd: h, fullscreen: true, onInput: func(e inputEvent) {
		if e.Kind == "reset" {
			resets++
		}
	}}
	cleanup := v.createHUD(inst)
	defer cleanup()
	foreground, _, _ := ucall("GetForegroundWindow")
	if foreground == h {
		t.Fatal("hidden test window unexpectedly focused")
	}
	for i := 0; i < 100; i++ {
		v.updateHUDHover(100, 1, 640, true)
	}
	if !v.hudVisible || resets != 1 {
		t.Fatal("hover toolbar oscillated while unfocused", resets)
	}
	after, _, _ := ucall("GetForegroundWindow")
	if after != foreground {
		t.Fatal("hover stole focus")
	}
	v.updateHUDHover(100, 1, 640, false)
	if v.hudVisible {
		t.Fatal("toolbar showing through overlaid window")
	}
	v.hudPinned = true
	v.updateHUDHover(100, 100, 640, false)
	if !v.hudVisible {
		t.Fatal("pinned toolbar disappeared")
	}
	v.keyMode.Store(true)
	v.updateHUDHover(100, 1, 640, true)
	if v.hudVisible {
		t.Fatal("Key mode kept function toolbar")
	}
}

func TestMonitorMetadataWaitAndStaleRejection(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &controllerSession{ctx: ctx, frames: make(chan videoFrame, 32), commands: make(chan message, 16)}
	desc := message{Type: "screen", ScreenGeneration: 2, Epoch: 8, Monitor: 1, MonitorCount: 2, Width: 128, Height: 64, Left: -128, FPS: 120, Codec: codecJPEGDelta}
	if !validScreenDescriptor(desc) {
		t.Fatal("valid negative-origin screen rejected")
	}
	done := make(chan message, 1)
	go func() {
		m, ok := s.screenForEpoch(8)
		if ok {
			done <- m
		}
	}()
	select {
	case <-done:
		t.Fatal("decoded before TCP metadata")
	case <-time.After(20 * time.Millisecond):
	}
	s.registerScreen(desc)
	select {
	case got := <-done:
		if got.Epoch != desc.Epoch || got.Width != desc.Width || got.Left != desc.Left {
			t.Fatal(got)
		}
	case <-time.After(time.Second):
		t.Fatal("metadata did not wake decoder")
	}
	s.screenMu.Lock()
	s.screen = desc
	s.screenMu.Unlock()
	if _, ok := s.screenForEpoch(7); ok {
		t.Fatal("retired UDP epoch accepted")
	}
	for epoch := uint64(9); epoch < 40; epoch++ {
		desc.Epoch = epoch
		s.registerScreen(desc)
	}
	if len(s.epochScreens) > 8 {
		t.Fatal("unbounded metadata history")
	}
	bad := desc
	bad.Width = 1 << 30
	if validScreenDescriptor(bad) {
		t.Fatal("oversized desktop accepted")
	}
}

func TestMonitorSwitchResetAndPNGGeneration(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := videoConfig{fps: 120, quality: 100, h264Quality: 8, bitrate: 80000, encoder: "jpeg", source: "test", area: desktop{width: 64, height: 64}}
	next := cfg
	next.area = desktop{left: -128, top: -20, width: 128, height: 64}
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	s := &hostSession{ctx: ctx, ctrl: &wire{conn: a}, cfg: cfg, profile: encoderProfile{codec: codecJPEGDelta}, input: newInputState(cfg.area), restart: make(chan struct{}, 1), screenGeneration: 1, screenSwitch: true, autoBitrate: true, screenList: func(videoConfig) []videoConfig { return []videoConfig{cfg, next} }}
	s.bitrate.Store(80000)
	var injected []winInput
	s.input.inject = func(i winInput) error { injected = append(injected, i); return nil }
	s.input.keys[0x10] = inputEvent{VK: 0x10}
	s.input.buttons[1] = true
	s.screenReady.Store(1)
	s.nextMonitor()
	c, p, g := s.screenConfig()
	if g != 2 || c.area != next.area || p.codec != codecJPEGDelta || c.fps != 120 || c.quality != 100 || c.h264Quality != 8 || c.bitrate != automaticBitrate(next) {
		t.Fatal(c, p, g)
	}
	if len(injected) != 2 || len(s.input.keys) != 0 || len(s.input.buttons) != 0 || s.input.area != next.area || s.screenReady.Load() == g {
		t.Fatal("old inputs were not released / screen resumed before first picture")
	}
	v := &viewer{width: 64, height: 64, screenGeneration: 1}
	v.setFrame(testDesktop(64, 64))
	v.setRefinePatch(message{Width: 64, Height: 64, ScreenGeneration: 1}, testDesktop(64, 64))
	if !v.resizeDesktop(ctx, 128, 64, 2) {
		t.Fatal("headless resize failed")
	}
	if v.pixels != nil || v.refine != nil || v.repairs.count != 0 || v.frames.latest != nil {
		t.Fatal("old screen surfaces leaked across switch")
	}
	v.setRefinePatch(message{Width: 64, Height: 64, ScreenGeneration: 1}, testDesktop(64, 64))
	if v.repairs.count != 0 {
		t.Fatal("old same-position repair accepted")
	}
	v.setRefinePatch(message{Width: 64, Height: 64, ScreenGeneration: 2}, testDesktop(64, 64))
	if v.repairs.count != 1 {
		t.Fatal("new screen repair missing")
	}
	// Legacy peers do not understand F6; normal video is not reconnected.
	ctrl := &controllerSession{ctx: ctx, commands: make(chan message, 16)}
	ctrl.nextScreen()
	if len(ctrl.commands) != 0 || ctrl.monitorPending.Load() {
		t.Fatal("legacy peer sent unsupported command")
	}
}

func TestControllerJPEGMonitorHandoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := message{Type: "screen", ScreenGeneration: 1, Epoch: 1, MonitorCount: 2, Width: 64, Height: 64, FPS: 120, Codec: codecJPEGDelta}
	s := &controllerSession{ctx: ctx, cancel: cancel, welcome: first, frames: make(chan videoFrame, 32), commands: make(chan message, 32), view: &viewer{width: 64, height: 64}, software: true}
	s.welcome.Features = []string{screenSwitchFeature}
	s.registerScreen(first)
	done := make(chan struct{})
	go func() { defer close(done); s.decode() }()
	defer func() { cancel(); <-done }()
	feed := func(desc message, id uint64) {
		s.registerScreen(desc)
		e := jpegDeltaEncoder{width: desc.Width, height: desc.Height, quality: 100}
		data, key, err := e.encode(testDesktop(desc.Width, desc.Height), time.Now())
		if err != nil {
			t.Fatal(err)
		}
		s.frames <- videoFrame{id: id, epoch: desc.Epoch, codec: codecJPEGDelta, key: key, data: data, at: time.Now()}
		limit := time.Now().Add(time.Second)
		for s.readyScreen.Load() != desc.ScreenGeneration && time.Now().Before(limit) {
			time.Sleep(time.Millisecond)
		}
		if s.readyScreen.Load() != desc.ScreenGeneration {
			t.Fatal("first picture not acknowledged")
		}
		s.view.mu.Lock()
		defer s.view.mu.Unlock()
		if s.view.width != desc.Width || s.view.height != desc.Height || len(s.view.pixels) != desc.Width*desc.Height*4 {
			t.Fatal("new resolution not installed")
		}
	}
	feed(first, 1)
	s.nextScreen()
	s.enqueueInput(inputEvent{Kind: "move", X: 30, Y: 30})
	for len(s.commands) > 0 {
		m := <-s.commands
		if m.Type == "input" {
			t.Fatal("input permitted during switch")
		}
	}
	next := first
	next.Epoch = 2
	next.ScreenGeneration = 2
	next.Width = 128
	next.Height = 96
	next.Left = -128
	next.Top = -96
	next.Monitor = 1
	feed(next, 2)
	if s.monitorPending.Load() {
		t.Fatal("input did not resume after first picture")
	}
	s.enqueueInput(inputEvent{Kind: "move", X: 20, Y: 40})
	found := false
	for len(s.commands) > 0 {
		m := <-s.commands
		if m.Type == "input" {
			found = true
			if m.ScreenGeneration != 2 {
				t.Fatal("old input origin")
			}
		}
	}
	if !found {
		t.Fatal("input suppressed after handoff")
	}
	feedBack := first
	feedBack.ScreenGeneration = 3
	feedBack.Epoch = 3
	feed(feedBack, 3)
}

func TestMonitorNetworkHandoffKeepsAuxiliary(t *testing.T) {
	for _, tcp := range []bool{false, true} {
		t.Run(fmt.Sprintf("TCP-%t", tcp), func(t *testing.T) {
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
			targets := func(c videoConfig) []videoConfig {
				a, b := c, c
				a.area = desktop{width: 64, height: 64}
				b.area = desktop{left: -128, top: -40, width: 128, height: 96}
				a.grab = func() ([]byte, error) { return testDesktop(64, 64), nil }
				b.grab = func() ([]byte, error) { return testDesktop(128, 96), nil }
				return []videoConfig{a, b}
			}
			clip := &fakeClipboard{}
			clip.copy("ignore", nil)
			cfg := videoConfig{fps: 60, quality: 100, encoder: "jpeg", source: "test", capture: "auto"}
			cfg = targets(cfg)[0]
			h := &host{code: "monitor-net-test", queueGrace: true, udpSmoothing: true, cfg: cfg, clipStore: clip, screenList: targets}
			go func() {
				for {
					c, e := ln.Accept()
					if e != nil {
						return
					}
					go h.accept(ctx, c.(*tls.Conn), true)
				}
			}()
			s, err := connectController(ctx, cancel, ln.Addr().String(), h.code, 60, "", true, net.IPv4(127, 0, 0, 1))
			if err != nil {
				t.Fatal(err)
			}
			defer s.close()
			s.view = &viewer{width: 64, height: 64}
			if err := s.start(tcp, false); err != nil {
				t.Fatal(err)
			}
			waitReady := func(generation uint64) {
				limit := time.Now().Add(3 * time.Second)
				for time.Now().Before(limit) {
					if s.readyScreen.Load() == generation && !s.monitorPending.Load() {
						return
					}
					if ctx.Err() != nil {
						break
					}
					time.Sleep(5 * time.Millisecond)
				}
				t.Fatal("screen handoff not ready", generation, s.currentScreen())
			}
			waitReady(1)
			localClip := &fakeClipboard{}
			localClip.copy("ignore", nil)
			var syncState clipboardSyncState
			if err := syncClipboardRound(ctx, s, localClip, &syncState); err != nil {
				t.Fatal(err)
			}
			originalCtrl, originalVideo := s.ctrl, s.video
			for generation := uint64(2); generation <= 4; generation++ {
				s.nextScreen()
				s.nextScreen()
				waitReady(generation)
				desc := s.currentScreen()
				expected := 64
				if generation%2 == 0 {
					expected = 128
				}
				if desc.Width != expected || desc.MonitorCount != 2 || s.transport.Load() != tcp || s.ctrl != originalCtrl || s.video != originalVideo {
					t.Fatal("changed transport/session or wrong display", desc)
				}
				list, err := s.remoteListRequest(ctx, "", "file-default")
				if err != nil || list.Path != programDirectory() {
					t.Fatal("F8 auxiliary interrupted", err, list.Path)
				}
				text := []byte(fmt.Sprintf("clipboard after monitor %d", generation))
				localClip.copy("text", text)
				if err := syncClipboardRound(ctx, s, localClip, &syncState); err != nil {
					t.Fatal("clipboard interrupted", err)
				}
				state, _ := clip.snapshot(ctx)
				if !bytes.Equal(state.Data, text) {
					t.Fatal("clipboard data mismatch")
				}
			}
		})
	}
}

func TestControllerH264MonitorHandoff(t *testing.T) {
	if os.Getenv("MYDESK_LAN_MEDIA_TEST") != "1" {
		t.Skip("native/FFmpeg decode opt-in")
	}
	ff, _ := filepath.Abs(filepath.Join("..", "..", "runtime", "ffmpeg.exe"))
	for _, backend := range []string{"mf", "ffmpeg"} {
		t.Run(backend, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			first := message{Type: "screen", ScreenGeneration: 1, Epoch: 1, MonitorCount: 2, Width: 128, Height: 64, FPS: 60, Codec: codecH264, Features: []string{screenSwitchFeature}}
			s := &controllerSession{ctx: ctx, cancel: cancel, welcome: first, frames: make(chan videoFrame, 32), commands: make(chan message, 32), view: &viewer{width: 128, height: 64}, software: true, decoder: backend, ffmpeg: ff}
			done := make(chan struct{})
			go func() { defer close(done); s.decode() }()
			defer func() { cancel(); <-done }()
			var id uint64
			for i, size := range [][2]int{{128, 64}, {192, 96}, {128, 64}} {
				w, h := size[0], size[1]
				generation := uint64(i + 1)
				cmd := helperCommand(ctx, ff, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", fmt.Sprintf("testsrc2=size=%dx%d:rate=60", w, h), "-frames:v", "6", "-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency", "-crf", "8", "-bf", "0", "-bsf:v", "h264_metadata=aud=insert", "-f", "h264", "pipe:1")
				data, err := cmd.Output()
				if err != nil {
					t.Fatal(err)
				}
				desc := first
				desc.Width = w
				desc.Height = h
				desc.ScreenGeneration = generation
				desc.Epoch = generation
				s.registerScreen(desc)
				if err := readAccessUnits(bytes.NewReader(data), func(unit []byte, key bool) error {
					id++
					s.frames <- videoFrame{id: id, epoch: generation, codec: codecH264, key: key, data: unit, at: time.Now()}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				limit := time.Now().Add(3 * time.Second)
				for s.decoded.Load() < uint64((i+1)*6) && time.Now().Before(limit) {
					time.Sleep(5 * time.Millisecond)
				}
				if s.decoded.Load() != uint64((i+1)*6) || s.readyScreen.Load() != generation {
					t.Fatal("resolution handoff lost decoder output", s.decoded.Load(), generation)
				}
				s.view.mu.Lock()
				bytes := len(s.view.pixels)
				format := s.view.frames.latest.format
				s.view.mu.Unlock()
				if bytes != format.size(w, h) {
					t.Fatal("old-size decoder callback survived", bytes, w, h)
				}
			}
		})
	}
}

func TestMonitorUnavailableKeepsOldConfig(t *testing.T) {
	for _, count := range []int{1, 2} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			a, b := net.Pipe()
			defer a.Close()
			defer b.Close()
			c := videoConfig{area: desktop{width: 64, height: 64}, fps: 120, quality: 100, encoder: "jpeg", source: "test"}
			s := &hostSession{ctx: ctx, ctrl: &wire{conn: a}, cfg: c, input: newInputState(c.area), screenGeneration: 1, restart: make(chan struct{}, 1), screenList: func(videoConfig) []videoConfig {
				if count == 1 {
					return []videoConfig{c}
				}
				bad := c
				bad.area = desktop{left: -100, width: 0, height: 1}
				return []videoConfig{c, bad}
			}}
			done := make(chan struct{})
			go func() { defer close(done); s.nextMonitor() }()
			m, _, err := (&wire{conn: b, timeout: time.Second}).read()
			if err != nil || m.Type != "screen-status" || m.Text == "" {
				t.Fatal(m, err)
			}
			<-done
			got, _, generation := s.screenConfig()
			if got.area != c.area || generation != 1 || len(s.restart) != 0 {
				t.Fatal("failed/no-op switch changed working stream")
			}
		})
	}
}

func TestNativeViewerMonitorResize(t *testing.T) {
	if os.Getenv("MYDESK_LAN_UI_TEST") != "1" {
		t.Skip("native UI opt-in")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	v := &viewer{width: 128, height: 64, windowWidth: 640, windowHeight: 360, onInput: func(inputEvent) {}}
	done := make(chan error, 1)
	go func() { done <- v.run() }()
	var hwnd uintptr
	limit := time.Now().Add(3 * time.Second)
	for time.Now().Before(limit) {
		v.mu.Lock()
		hwnd = v.hwnd
		v.mu.Unlock()
		if hwnd != 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if hwnd == 0 {
		t.Fatal("viewer missing")
	}
	defer func() {
		ucall("PostMessageW", hwnd, 0x10, 0, 0)
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(2 * time.Second):
			t.Error("viewer shutdown failed")
		}
	}()
	// All resize disposal/recreation is executed by the real viewer UI thread.
	for i, size := range [][2]int{{128, 64}, {192, 96}, {128, 64}} {
		generation := uint64(i + 1)
		w, h := size[0], size[1]
		if !v.resizeDesktop(ctx, w, h, generation) {
			t.Fatal("UI-thread resize did not complete")
		}
		v.setFrame(testDesktop(w, h))
		v.setRefinePatch(message{ScreenGeneration: generation, Width: 64, Height: 64}, testDesktop(64, 64))
		ucall("SendMessageW", hwnd, 0x8001, 0, 0)
		v.mu.Lock()
		width, height, count := v.width, v.height, v.repairs.count
		v.mu.Unlock()
		if width != w || height != h || count != 1 {
			t.Fatal("viewer retained old geometry/repairs")
		}
	}
}

func ownControlPixels(h uintptr) ([]byte, error) {
	var r rect
	ucall("GetClientRect", h, uintptr(unsafe.Pointer(&r)))
	g, err := newScreenGrabber(desktop{width: int(r.Right), height: int(r.Bottom)})
	if err != nil {
		return nil, err
	}
	defer g.close()
	dc, _, _ := ucall("GetDC", h)
	defer ucall("ReleaseDC", h, dc)
	if ok, _, e := gcall("BitBlt", g.dc, 0, 0, uintptr(r.Right), uintptr(r.Bottom), dc, 0, 0, 0x00CC0020); ok == 0 {
		return nil, e
	}
	gcall("GdiFlush")
	pixels := unsafe.Slice((*byte)(unsafe.Pointer(g.pixels)), g.size)
	// The capture buffer is a DIB allocation freed by close, not a Go slice.
	return bytes.Clone(pixels), nil
}

func captureOwnUI(t *testing.T, h uintptr, name string) {
	t.Helper()
	dir := os.Getenv("MYDESK_LAN_UI_CAPTURE_DIR")
	if dir == "" {
		return
	}
	var r rect
	ucall("GetClientRect", h, uintptr(unsafe.Pointer(&r)))
	g, err := newScreenGrabber(desktop{width: int(r.Right), height: int(r.Bottom)})
	if err != nil {
		t.Error(err)
		return
	}
	defer g.close()
	if ok, _, _ := ucall("PrintWindow", h, g.dc, 1); ok == 0 {
		t.Error("own-window capture failed")
		return
	}
	gcall("GdiFlush")
	pixels := bytes.Clone(unsafe.Slice((*byte)(unsafe.Pointer(g.pixels)), g.size))
	f, err := os.Create(filepath.Join(dir, name+".png"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, bgraImage(pixels, int(r.Right), int(r.Bottom))); err != nil {
		t.Fatal(err)
	}
}

func TestNativeF7ScrollAndF8Columns(t *testing.T) {
	if os.Getenv("MYDESK_LAN_UI_TEST") != "1" {
		t.Skip("native UI opt-in")
	}
	s, _ := auxiliaryFixture(t)
	s.openCommandDialog()
	var d *commandDialog
	limit := time.Now().Add(3 * time.Second)
	for time.Now().Before(limit) {
		s.filesMu.Lock()
		d = s.command
		s.filesMu.Unlock()
		if d != nil {
			d.mu.Lock()
			h := d.hwnd
			d.mu.Unlock()
			if h != 0 {
				break
			}
		}
		time.Sleep(time.Millisecond)
	}
	if d == nil {
		t.Fatal("F7 missing")
	}
	t.Cleanup(d.cancel)
	// Window-open animations are composited asynchronously by DWM. Measure
	// scroll pixels after that animation, not a half-transparent opening frame.
	time.Sleep(350 * time.Millisecond)
	if !onFileUI(t, &d.fileManager, func() bool {
		text := ""
		for i := 0; i < 200; i++ {
			text += fmt.Sprintf("%03d   文件目录 dir output — abcdefghijklmnopqrstuvwxyz 0123456789\r\n", i)
		}
		d.setText(d.output, text)
		dc, _, _ := ucall("GetDC", d.output)
		defer ucall("ReleaseDC", d.output, dc)
		ucall("SendMessageW", d.hwnd, 0x138, dc, d.output)
		mode, _, _ := gcall("GetBkMode", dc)
		style, _, _ := ucall("GetWindowLongPtrW", d.hwnd, ^uintptr(15))
		return mode == 2 && style&0x2000000 != 0
	}) {
		t.Fatal("output remains transparent / parent paints over controls")
	}
	if !onFileUI(t, &d.fileManager, func() bool {
		for _, lines := range []int{1, 8, 17, -10, 24, -3} {
			ucall("SendMessageW", d.output, 0xB6, 0, uintptr(lines))
			ucall("UpdateWindow", d.output)
			before, err := ownControlPixels(d.output)
			if err != nil {
				return false
			}
			ucall("InvalidateRect", d.output, 0, 1)
			ucall("UpdateWindow", d.output)
			after, err := ownControlPixels(d.output)
			if err != nil || !bytes.Equal(before, after) {
				var r rect
				ucall("GetClientRect", d.output, uintptr(unsafe.Pointer(&r)))
				count := 0
				var first int
				for i := range before {
					if before[i] != after[i] {
						if count == 0 {
							first = i
						}
						count++
					}
				}
				t.Logf("scroll %d differs at x=%d y=%d (%d bytes)", lines, (first/4)%int(r.Right), (first/4)/int(r.Right), count)
				if dir := os.Getenv("MYDESK_LAN_UI_CAPTURE_DIR"); dir != "" {
					for name, pixels := range map[string][]byte{"before": before, "after": after} {
						file, e := os.Create(filepath.Join(dir, name+".png"))
						if e == nil {
							png.Encode(file, bgraImage(pixels, int(r.Right), int(r.Bottom)))
							file.Close()
						}
					}
				}
				return false
			}
		}
		captureOwnUI(t, d.hwnd, "f7-scroll")
		return true
	}) {
		t.Fatal("F7 scrolled glyphs differ from clean redraw")
	}
	d.cancel()
	limit = time.Now().Add(3 * time.Second)
	for time.Now().Before(limit) {
		d.mu.Lock()
		closed := d.hwnd == 0
		d.mu.Unlock()
		if closed {
			break
		}
		time.Sleep(time.Millisecond)
	}
	s.openFileManager()
	var f *fileManager
	limit = time.Now().Add(3 * time.Second)
	for time.Now().Before(limit) {
		s.filesMu.Lock()
		f = s.files
		s.filesMu.Unlock()
		if f != nil {
			f.mu.Lock()
			h := f.hwnd
			f.mu.Unlock()
			if h != 0 {
				break
			}
		}
		time.Sleep(time.Millisecond)
	}
	if f == nil {
		t.Fatal("F8 missing")
	}
	t.Cleanup(f.cancel)
	waitFileUI(t, f, func() bool { return !f.busy.Load() })
	onFileUI(t, f, func() bool { presentOwnedWindow(f.hwnd, f.listBox); return true })
	time.Sleep(350 * time.Millisecond)
	if !onFileUI(t, f, func() bool {
		listing := directoryListing{Path: `C:\远端演示`}
		for i := 0; i < 10000; i++ {
			name := fmt.Sprintf("%05d.txt", i)
			if i == 7 {
				name = "这是一个比较长的文件名，用来验证三列对齐与完整名称提示.txt"
			}
			listing.Entries = append(listing.Entries, fileEntry{Name: name, Path: filepath.Join(listing.Path, name), Size: int64(i) * 1048576, Modified: 1760000000 + int64(i)})
		}
		listing.Entries = append(listing.Entries, fileEntry{Name: "目录", Path: `C:\远端演示\目录`, Directory: true})
		f.applyListing(listing)
		count, _, _ := ucall("SendMessageW", f.listHeader, 0x1200, 0, 0)
		styles, _, _ := ucall("SendMessageW", f.listBox, 0x1037, 0, 0)
		if count != 3 || styles&0x10000 == 0 || len(f.rows) != len(f.entries) {
			return false
		}
		f.selectFileRow(8)
		entry, ok := f.selected()
		if !ok {
			return false
		}
		n := listNotification{Header: notifyHeader{Window: f.listBox, ID: 2, Code: -108}, SubItem: 1}
		f.listNotify(uintptr(unsafe.Pointer(&n)))
		selected, ok := f.selected()
		if !ok || selected.Path != entry.Path {
			return false
		}
		ucall("SendMessageW", f.listBox, 0x1014, 0, 240)
		ucall("UpdateWindow", f.listBox)
		captureOwnUI(t, f.hwnd, "f8-columns")
		return true
	}) {
		t.Fatal("columns/double buffer/selection integrity")
	}
}
