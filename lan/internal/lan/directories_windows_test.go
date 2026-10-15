//go:build windows && amd64

package lan

import (
	"context"
	"crypto/tls"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestRemoteDirectoriesMemorySeparateAndReset(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "浏览目录"), filepath.Join(dir, "执行目录")
	for _, p := range []string{a, b} {
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	p := hostDirectoryMemory()
	if p.directory(false) != programDirectory() || p.directory(true) != programDirectory() {
		t.Fatal("first-use default")
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(command bool) {
			defer wg.Done()
			path := a
			if command {
				path = b
			}
			if err := p.remember(path, command); err != nil {
				t.Error(err)
			}
		}(i%2 == 0)
	}
	wg.Wait()
	if p.directory(false) != a || p.directory(true) != b {
		t.Fatal("preferences mixed or lost", p.File, p.Command)
	}
	if err := p.remember(filepath.Join(dir, "missing"), true); err == nil {
		t.Fatal("invalid directory saved")
	}
	if err := os.Remove(a); err != nil {
		t.Fatal(err)
	}
	if p.directory(false) != programDirectory() || p.File != a {
		t.Fatal("missing directory didn't safely fall back")
	}
	if err := os.Mkdir(a, 0700); err != nil {
		t.Fatal(err)
	}
	if p.directory(false) != a {
		t.Fatal("saved removable directory was erased")
	}
	if err := p.remember("", false); err != nil {
		t.Fatal(err)
	}
	if p.directory(false) != "" || p.directory(true) != b {
		t.Fatal("drive overview was not remembered separately")
	}
	q := hostDirectoryMemory()
	if q.directory(false) != programDirectory() || q.directory(true) != programDirectory() || q.Drives {
		t.Fatal("restarted host retained previous process paths")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 2 {
		t.Fatal("unexpected file created by directory memory", entries, err)
	}
}

func TestRemoteDirectoriesReconnectAndCommandEditWithoutExecution(t *testing.T) {
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
	dir := t.TempDir()
	fileDir, cmdDir, editedDir := filepath.Join(dir, "F8"), filepath.Join(dir, "F7"), filepath.Join(dir, "F7-edit-only")
	for _, p := range []string{fileDir, cmdDir, editedDir} {
		os.Mkdir(p, 0700)
	}
	pixels := testDesktop(64, 64)
	h := &host{code: "directory-memory-test", directories: hostDirectoryMemory(), cfg: videoConfig{fps: 30, quality: 100, encoder: "jpeg", area: desktop{width: 64, height: 64}, grab: func() ([]byte, error) { return pixels, nil }}}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go h.accept(ctx, c.(*tls.Conn), false)
		}
	}()
	connect := func() *controllerSession {
		t.Helper()
		sessionCtx, stop := context.WithCancel(ctx)
		s, err := connectController(sessionCtx, stop, ln.Addr().String(), h.code, 30, "", false, net.IPv4(127, 0, 0, 1))
		if err != nil {
			stop()
			t.Fatal(err)
		}
		s.view = &viewer{width: 64, height: 64}
		s.downloadDir = filepath.Join(dir, "unchanged-local-downloads")
		if err := s.start(true, false); err != nil {
			s.close()
			t.Fatal(err)
		}
		return s
	}
	s := connect()
	defer s.close()
	if !hasFeature(s.welcome.Features, directoryFeature) {
		t.Fatal("directory feature missing")
	}
	if _, err := s.remoteList(ctx, fileDir); err != nil {
		t.Fatal(err)
	}
	if err := s.saveCommandDirectory(ctx, cmdDir); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"file-default", "command-default"} {
		list, err := s.remoteListRequest(ctx, "", kind)
		expected := fileDir
		if kind == "command-default" {
			expected = cmdDir
		}
		if err != nil || list.Path != expected {
			t.Fatal(kind, list.Path, err)
		}
	}
	// Native opt-in drives only our own F7 edit control, without executing a
	// command or injecting real input. The default test covers the same queue.
	if os.Getenv("MYDESK_LAN_UI_TEST") == "1" {
		s.openCommandDialog()
		var d *commandDialog
		waitJPEG(t, func() bool {
			s.filesMu.Lock()
			d = s.command
			s.filesMu.Unlock()
			if d == nil {
				return false
			}
			d.mu.Lock()
			defer d.mu.Unlock()
			return d.hwnd != 0
		})
		t.Cleanup(d.cancel)
		if !onFileUI(t, &d.fileManager, func() bool {
			d.setText(d.pathEdit, editedDir)
			return d.directoryEdited
		}) {
			t.Fatal("F7 path edit wasn't queued")
		}
	} else {
		s.queueCommandDirectory(editedDir)
	}
	s.close()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		gone := h.session == nil
		h.mu.Unlock()
		if gone {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	// Reconnect to the SAME host process: keep its existing in-memory store.
	s2 := connect()
	defer s2.close()
	for _, kind := range []string{"file-default", "command-default"} {
		list, err := s2.remoteListRequest(ctx, "", kind)
		expected := fileDir
		if kind == "command-default" {
			expected = editedDir
		}
		if err != nil || list.Path != expected {
			t.Fatal("reconnect lost path", kind, list.Path, err)
		}
	}
	if s2.downloadDir != filepath.Join(dir, "unchanged-local-downloads") {
		t.Fatal("local save path changed")
	}
	s2.close()
	waitJPEG(t, func() bool { h.mu.Lock(); defer h.mu.Unlock(); return h.session == nil })
	// ClientMain constructs this fresh store on every host process restart.
	h.mu.Lock()
	h.directories = hostDirectoryMemory()
	h.mu.Unlock()
	s3 := connect()
	defer s3.close()
	for _, kind := range []string{"file-default", "command-default"} {
		list, err := s3.remoteListRequest(ctx, "", kind)
		if err != nil || list.Path != programDirectory() {
			t.Fatal("restarted host didn't restore program directory", kind, list.Path, err)
		}
	}
}
