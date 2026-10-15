//go:build windows && amd64

package lan

import (
	"context"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"
)

func TestRemoteCommandAndValidation(t *testing.T) {
	s, _ := auxiliaryFixture(t)
	dir := t.TempDir()
	r, err := s.runCommand(s.ctx, dir, "echo LAN-command-ok & exit /b 7", 10)
	if err != nil || r.Running || r.ExitCode != 7 || !strings.Contains(r.Output, "LAN-command-ok") {
		t.Fatalf("result %+v, %v", r, err)
	}
	if _, err = s.runCommand(s.ctx, dir, "echo invalid", 3601); err == nil {
		t.Fatal("accepted invalid wait")
	}
	if _, err = s.runCommand(s.ctx, dir, "", 10); err == nil {
		t.Fatal("accepted empty command")
	}
	r, err = s.runCommand(s.ctx, dir, "ping -n 3 127.0.0.1 >nul", 1)
	if err != nil || !r.Running || r.PID <= 0 {
		t.Fatalf("timeout must leave finite command running: %+v %v", r, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(commandSlots) > 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if len(commandSlots) != 0 {
		t.Fatal("command slot not released")
	}
}
func TestDisabledClipboardAndInputSettings(t *testing.T) {
	fake := &fakeClipboard{}
	var gate sync.Mutex
	var disabled atomic.Bool
	guard := guardedClipboard{fake, &gate, &disabled}
	disabled.Store(true)
	if _, err := guard.apply(context.Background(), clipState{Kind: "text", Data: []byte("blocked")}, 0); err == nil {
		t.Fatal("disabled clipboard write succeeded")
	}
	if fake.writes != 0 {
		t.Fatal("clipboard written")
	}
	s, _ := auxiliaryFixture(t)
	s.setInputEnabled(false)
	if !s.inputDisabled.Load() || s.view.capture.Load() {
		t.Fatal("watch mode not applied")
	}
	s.setInputEnabled(true)
	if s.inputDisabled.Load() || !s.view.capture.Load() {
		t.Fatal("control not restored")
	}
	s.setClipboardEnabled(false)
	if !s.clipboardDisabled.Load() {
		t.Fatal("clipboard not disabled")
	}
	s.setLocalCursorEnabled(false)
	if !s.view.hideLocalCursor.Load() {
		t.Fatal("local cursor not hidden")
	}
}

func TestRemoteCursorCaptureArguments(t *testing.T) {
	c := videoConfig{fps: 120, bitrate: 80000, area: desktop{width: 1920, height: 1080}, hideCursor: true}
	if args := strings.Join(profileArgs(c, "h264_nvenc", "ddagrab"), " "); !strings.Contains(args, "draw_mouse=0") {
		t.Fatal(args)
	}
	if args := strings.Join(profileArgs(c, "libx264", "gdi"), " "); !strings.Contains(args, "-draw_mouse 0") {
		t.Fatal(args)
	}
	c.hideCursor = false
	if args := strings.Join(profileArgs(c, "h264_nvenc", "ddagrab"), " "); !strings.Contains(args, "draw_mouse=1") {
		t.Fatal(args)
	}
}
func TestFullscreenAndOwnedDialogs(t *testing.T) {
	if os.Getenv("MYDESK_LAN_UI_TEST") != "1" {
		t.Skip("native UI opt-in")
	}
	s, _ := auxiliaryFixture(t)
	v := s.view
	v.onCancelTransfers = s.cancelTransfers
	finished := make(chan error, 1)
	go func() { finished <- v.run() }()
	deadline := time.Now().Add(4 * time.Second)
	var h uintptr
	for time.Now().Before(deadline) {
		h = s.ownerWindow()
		if h != 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if h == 0 {
		t.Fatal("viewer missing")
	}
	defer func() {
		ucall("PostMessageW", h, 0x10, 0, 0)
		select {
		case err := <-finished:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(3 * time.Second):
			t.Error("viewer did not close")
		}
	}()
	var before rect
	ucall("GetWindowRect", h, uintptr(unsafe.Pointer(&before)))
	ucall("SendMessageW", h, 0x112, 0xF030, 0)
	style, _, _ := ucall("GetWindowLongPtrW", h, ^uintptr(15))
	if style&0xC40000 != 0 {
		t.Fatalf("fullscreen still has border: %x", style)
	}
	monitor, _, _ := ucall("MonitorFromWindow", h, 2)
	info := monitorInfo{Size: uint32(unsafe.Sizeof(monitorInfo{}))}
	ucall("GetMonitorInfoW", monitor, uintptr(unsafe.Pointer(&info)))
	var actual rect
	ucall("GetWindowRect", h, uintptr(unsafe.Pointer(&actual)))
	if actual != info.Monitor {
		t.Fatalf("not full monitor: %+v %+v", actual, info.Monitor)
	}
	s.openFileManager()
	s.filesMu.Lock()
	f := s.files
	s.filesMu.Unlock()
	defer f.cancel()
	waitFileUI(t, f, func() bool { return f.progressBar != 0 && !f.busy.Load() })
	op, cancel := context.WithCancel(s.ctx)
	defer cancel()
	f.mu.Lock()
	f.operationCancel = cancel
	f.mu.Unlock()
	ucall("SendMessageW", h, 0x8007, 0, 0)
	if op.Err() == nil || s.inputDisabled.Load() {
		t.Fatal("F10 did not cancel transfer or changed input mode")
	}
	f.mu.Lock()
	f.operationCancel = nil
	f.mu.Unlock()
	if !onFileUI(t, f, func() bool {
		owner, _, _ := ucall("GetWindow", f.hwnd, 4)
		f.progress("test")(50, 100)
		return owner == h
	}) {
		t.Fatal("F8 not owned")
	}
	waitFileUI(t, f, func() bool { pos, _, _ := ucall("SendMessageW", f.progressBar, 0x408, 0, 0); return pos == 500 })
	s.openCommandDialog()
	s.filesMu.Lock()
	d := s.command
	s.filesMu.Unlock()
	defer d.cancel()
	waitFileUI(t, &d.fileManager, func() bool { return d.commandEdit != 0 })
	if !onFileUI(t, &d.fileManager, func() bool {
		owner, _, _ := ucall("GetWindow", d.hwnd, 4)
		ucall("SendMessageW", d.hwnd, 0x111, 20, 0)
		return owner == h && s.inputDisabled.Load() && !v.capture.Load()
	}) {
		t.Fatal("F7 owner/settings")
	}
	if v.hud == 0 || len(v.hudButtons) != 6 {
		t.Fatal("fullscreen toolbar missing")
	}
	ucall("SendMessageW", v.hud, 0x111, 7, 0)
	if !v.hudPinned || !v.hudVisible {
		t.Fatal("toolbar did not pin")
	}
	ucall("SendMessageW", v.hud, 0x111, 7, 0)
	if v.hudPinned {
		t.Fatal("toolbar did not unpin")
	}
	ucall("SendMessageW", v.hud, 0x111, 3, 0)
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		ucall("GetWindowRect", h, uintptr(unsafe.Pointer(&actual)))
		if actual == before {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	ucall("GetWindowRect", h, uintptr(unsafe.Pointer(&actual)))
	if actual != before {
		t.Fatalf("restore changed bounds: %+v %+v", before, actual)
	}
}
