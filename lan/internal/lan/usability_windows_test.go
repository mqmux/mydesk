//go:build windows && amd64

package lan

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"
)

func TestLANFileDefaultsSearchAndCollision(t *testing.T) {
	s, _ := auxiliaryFixture(t)
	listing, err := s.remoteListRequest(s.ctx, "", "file-default")
	if err != nil || listing.Path != programDirectory() {
		t.Fatal("remote default differs from executable directory", listing.Path, err)
	}
	listing = directoryListing{Path: `C:\Files`, Entries: []fileEntry{{Name: "屏幕.PNG", Path: `C:\Files\屏幕.PNG`}, {Name: "notes.txt", Path: `C:\Files\notes.txt`}}}
	entries := filterFileEntries(listing, "png")
	if len(entries) != 2 || entries[0].Name != ".." || entries[1].Name != "屏幕.PNG" {
		t.Fatal("search/index mapping mismatch", entries)
	}
	if len(filterFileEntries(listing, "missing")) != 1 {
		t.Fatal("search should preserve parent navigation")
	}
	dir := t.TempDir()
	name := filepath.Join(dir, "report.txt")
	if err := os.WriteFile(name, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	target, err := defaultDownloadTarget(dir, "report.txt")
	if err != nil || filepath.Base(target) != "report (1).txt" {
		t.Fatal("same-name download not preserved", target, err)
	}
	for _, name := range []string{"..", `..\bad`, `C:\bad`, "bad:stream"} {
		if _, err := defaultDownloadTarget(dir, name); err == nil {
			t.Fatal("unsafe download name accepted", name)
		}
	}
	if unsafe.Sizeof(drawItem{}) != 64 || unsafe.Offsetof(drawItem{}.DC) != 32 || unsafe.Sizeof(browseInfo{}) != 64 {
		t.Fatal("Win32 draw/folder dialog ABI mismatch")
	}
}
func TestLAN120FPSQualityBudget(t *testing.T) {
	for _, pair := range [][3]int{{120, 120, 120}, {120, 60, 60}, {60, 120, 60}, {120, 999, 120}, {0, 0, 1}} {
		if got := negotiatedFPS(pair[0], pair[1]); got != pair[2] {
			t.Fatal("negotiation", pair, got)
		}
	}
	c := videoConfig{fps: 120, area: desktop{width: 1920, height: 1080}}
	if automaticBitrate(c) != 80000 {
		t.Fatal("120 FPS per-frame budget reduced")
	}
	c.fps = 60
	if automaticBitrate(c) != 40000 {
		t.Fatal("60 FPS budget")
	}
	for _, encoder := range []string{"h264_nvenc", "libx264"} {
		c.fps = 120
		c.bitrate = automaticBitrate(c)
		args := strings.Join(profileArgs(c, encoder, "test"), " ")
		if !strings.Contains(args, "rate=120") || !strings.Contains(args, "-g 120") || !strings.Contains(args, "14") {
			t.Fatal("high FPS quality arguments missing", args)
		}
	}
	x, y, w, h := viewerWindowBounds(1920, 1080, 0, 0)
	if w < 320 || h < 240 || x < 0 || y < 0 {
		t.Fatal("native-sized window bounds", x, y, w, h)
	}
	if networkDrops(19, 19) != 0 || networkDrops(7, 3) != 4 {
		t.Fatal("decoder overload incorrectly reduces network quality budget")
	}
}

func onFileUI(t *testing.T, f *fileManager, run func() bool) bool {
	t.Helper()
	result := make(chan bool, 1)
	f.post(func() { result <- run() })
	select {
	case ok := <-result:
		return ok
	case <-time.After(3 * time.Second):
		t.Fatal("F8 UI callback timed out")
		return false
	}
}
func waitFileUI(t *testing.T, f *fileManager, run func() bool) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if onFileUI(t, f, run) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("F8 UI state timed out")
}
func TestNativeF8DoubleClickDownloadAndUpload(t *testing.T) {
	if os.Getenv("MYDESK_LAN_UI_TEST") == "" {
		t.Skip("native UI opt-in")
	}
	s, _ := auxiliaryFixture(t)
	remote, local := t.TempDir(), t.TempDir()
	data := []byte("file manager default download test")
	if err := os.WriteFile(filepath.Join(remote, "画面.txt"), data, 0600); err != nil {
		t.Fatal(err)
	}
	s.filesMu.Lock()
	s.downloadDir = local
	s.filesMu.Unlock()
	s.openFileManager()
	var f *fileManager
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
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
		time.Sleep(10 * time.Millisecond)
	}
	if f == nil {
		t.Fatal("no F8 window")
	}
	t.Cleanup(f.cancel)
	waitFileUI(t, f, func() bool { return !f.busy.Load() && f.listing.Path == programDirectory() })
	onFileUI(t, f, func() bool { f.refresh(remote); return true })
	waitFileUI(t, f, func() bool { return !f.busy.Load() && f.listing.Path == remote })
	onFileUI(t, f, func() bool {
		f.setText(f.searchEdit, "txt")
		f.selectFileRow(1)
		n := listNotification{Header: notifyHeader{Window: f.listBox, ID: 2, Code: -3}, Item: 1}
		ucall("SendMessageW", f.hwnd, 0x4E, 2, uintptr(unsafe.Pointer(&n)))
		return true
	})
	waitFileUI(t, f, func() bool { return !f.busy.Load() && strings.Contains(controlText(f.status), "下载完成") })
	got, err := os.ReadFile(filepath.Join(local, "画面.txt"))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal("double click download failed", err)
	}
	onFileUI(t, f, func() bool { f.download(); return true })
	waitFileUI(t, f, func() bool { return !f.busy.Load() })
	if _, err := os.Stat(filepath.Join(local, "画面 (1).txt")); err != nil {
		t.Fatal("duplicate download missing", err)
	}
	upload := filepath.Join(local, "upload.txt")
	if err := os.WriteFile(upload, data, 0600); err != nil {
		t.Fatal(err)
	}
	onFileUI(t, f, func() bool { f.uploadPaths([]string{upload}); return true })
	waitFileUI(t, f, func() bool { return !f.busy.Load() && strings.Contains(controlText(f.status), "上传完成") })
	got, err = os.ReadFile(filepath.Join(remote, "upload.txt"))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal("UI upload failed", err)
	}
	// Exercise the actual WM_CTLCOLOR handler without changing system colors.
	if !onFileUI(t, f, func() bool {
		dc, _, _ := ucall("GetDC", f.pathEdit)
		defer ucall("ReleaseDC", f.pathEdit, dc)
		brush, _, _ := ucall("SendMessageW", f.hwnd, 0x133, dc, f.pathEdit)
		color, _, _ := gcall("GetBkColor", dc)
		return brush == f.darkBrush && color == darkBackground
	}) {
		t.Fatal("dark edit colors missing")
	}
	// Test the actual queued shortcut, not only the session toggle method.
	onFileUI(t, f, func() bool {
		f.task(func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() })
		return true
	})
	f.mu.Lock()
	closeTarget := f.buttons[14]
	f.mu.Unlock()
	ucall("PostMessageW", closeTarget, 0x100, 0x77, 0)
	select {
	case <-f.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("F8 toggle did not close")
	}
}
