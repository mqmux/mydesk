//go:build windows && amd64

package lan

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"image"
	"image/png"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
	"unsafe"
)

type fakeClipboard struct {
	mu     sync.Mutex
	state  clipState
	fail   bool
	writes int
}

func (f *fakeClipboard) setFailure(value bool) { f.mu.Lock(); f.fail = value; f.mu.Unlock() }

func (f *fakeClipboard) copy(kind string, data []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state = clipState{Kind: kind, Data: bytes.Clone(data), Hash: clipHash(kind, data), Sequence: f.state.Sequence + 1}
}
func (f *fakeClipboard) snapshot(ctx context.Context) (clipState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return clipState{}, errors.New("busy")
	}
	return f.state, ctx.Err()
}
func (f *fakeClipboard) apply(ctx context.Context, s clipState, expected uint32) (uint32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if f.fail || expected != f.state.Sequence {
		return 0, errors.New("busy or stale")
	}
	s.Sequence = f.state.Sequence + 1
	f.state = s
	f.writes++
	return s.Sequence, nil
}

func auxiliaryFixture(t *testing.T) (*controllerSession, *fakeClipboard) {
	clip := &fakeClipboard{}
	clip.copy("ignore", nil)
	return auxiliaryFixtureWithClipboard(t, clip), clip
}

func auxiliaryFixtureWithClipboard(t *testing.T, clip clipboardAccess) *controllerSession {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	config, err := ephemeralTLS()
	if err != nil {
		t.Fatal(err)
	}
	ln, err := tls.Listen("tcp4", "127.0.0.1:0", config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	pixels := testDesktop(320, 180)
	h := &host{code: "aux-test", clipStore: clip, cfg: videoConfig{fps: 30, quality: 90, bitrate: 20000, area: desktop{width: 320, height: 180}, encoder: "jpeg", grab: func() ([]byte, error) { return pixels, nil }}}
	go func() {
		for {
			c, e := ln.Accept()
			if e != nil {
				return
			}
			go h.accept(ctx, c.(*tls.Conn), false)
		}
	}()
	s, err := connectController(ctx, cancel, ln.Addr().String(), h.code, 30, "", false, net.IPv4(127, 0, 0, 1))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.close)
	s.view = &viewer{width: 320, height: 180, onInput: func(inputEvent) {}}
	if err := s.start(true, false); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAuxiliaryFilesAndCounters(t *testing.T) {
	s, _ := auxiliaryFixture(t)
	dir := t.TempDir()
	source := filepath.Join(dir, "源文件.bin")
	target := filepath.Join(dir, "远端文件.bin")
	download := filepath.Join(dir, "下载.bin")
	data := bytes.Repeat([]byte("verified file payload\x00"), 80000)
	if err := os.WriteFile(source, data, 0600); err != nil {
		t.Fatal(err)
	}
	beforeRx, beforeTx := s.networkTotals()
	if err := s.uploadFile(s.ctx, source, target, false, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.uploadFile(s.ctx, source, target, false, nil); err == nil {
		t.Fatal("unconfirmed overwrite accepted")
	}
	if err := s.uploadFile(s.ctx, source, target, true, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.downloadFile(s.ctx, target, download, false, nil); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(download)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal("file mismatch", err)
	}
	listing, err := s.remoteList(s.ctx, dir)
	if err != nil || len(listing.Entries) != 3 {
		t.Fatal("listing mismatch", err, listing)
	}
	if _, err := s.remoteList(s.ctx, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.downloadFile(s.ctx, target, download, false, nil); err == nil {
		t.Fatal("download overwrite accepted")
	}
	if err := os.WriteFile(source, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.uploadFile(s.ctx, source, target, true, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.downloadFile(s.ctx, target, download, true, nil); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(download)
	if err != nil || st.Size() != 0 {
		t.Fatal("empty file failed", err)
	}
	afterRx, afterTx := s.networkTotals()
	if afterRx-beforeRx < uint64(len(data)) || afterTx-beforeTx < 2*uint64(len(data)) {
		t.Fatal("bulk traffic missing from counters")
	}
	s.auxMu.Lock()
	n := len(s.auxMeters)
	s.auxMu.Unlock()
	if n != 0 {
		t.Fatal("closed auxiliary meters leaked")
	}
}

func TestReceiveFileRejectsCorruptionAndCleansTemp(t *testing.T) {
	for _, kind := range []string{"hash", "offset", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, "keep.txt")
			if err := os.WriteFile(target, []byte("original"), 0600); err != nil {
				t.Fatal(err)
			}
			a, b := net.Pipe()
			defer a.Close()
			defer b.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			w := &wire{conn: a, timeout: time.Second}
			go func() {
				sender := &wire{conn: b}
				offset := int64(0)
				if kind == "offset" {
					offset = 1
				}
				_ = sender.send(message{Type: "chunk", Offset: offset}, []byte("abc"))
				if kind == "cancel" {
					cancel()
					b.Close()
				} else {
					_ = sender.send(message{Type: "done", Size: 3, Hash: "wrong"}, nil)
				}
			}()
			if err := receiveFile(ctx, w, target, 3, true, nil); err == nil {
				t.Fatal("invalid transfer committed")
			}
			got, err := os.ReadFile(target)
			if err != nil || string(got) != "original" {
				t.Fatal("original damaged")
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 1 {
				t.Fatal("temporary file leaked", err)
			}
		})
	}
}

func noisyClipboardPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 1024, 768))
	seed := uint32(79)
	for i := 0; i < len(img.Pix); i += 4 {
		for j := 0; j < 3; j++ {
			seed = seed*1664525 + 1013904223
			img.Pix[i+j] = byte(seed >> 24)
		}
		img.Pix[i+3] = 255
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		t.Fatal(err)
	}
	if out.Len() < bulkChunk*8 {
		t.Fatal("image not large enough")
	}
	return out.Bytes()
}

func TestClipboardImageTextEchoRecoveryAndStaleWrites(t *testing.T) {
	s, remote := auxiliaryFixture(t)
	local := &fakeClipboard{}
	local.copy("ignore", nil)
	var syncState clipboardSyncState
	if err := syncClipboardRound(s.ctx, s, local, &syncState); err != nil {
		t.Fatal(err)
	}
	data := noisyClipboardPNG(t)
	local.copy("image", data)
	if err := syncClipboardRound(s.ctx, s, local, &syncState); err != nil {
		t.Fatal(err)
	}
	state, _ := remote.snapshot(s.ctx)
	if state.Kind != "image" || !bytes.Equal(state.Data, data) {
		t.Fatal("large image upload mismatch")
	}
	if err := syncClipboardRound(s.ctx, s, local, &syncState); err != nil {
		t.Fatal(err)
	}
	if remote.writes != 1 || local.writes != 0 {
		t.Fatal("clipboard echo")
	}
	remote.copy("text", []byte("远端文字\nhello"))
	if err := syncClipboardRound(s.ctx, s, local, &syncState); err != nil {
		t.Fatal(err)
	}
	state, _ = local.snapshot(s.ctx)
	if string(state.Data) != "远端文字\nhello" {
		t.Fatal("text download mismatch")
	}
	remote.copy("image", data)
	if err := syncClipboardRound(s.ctx, s, local, &syncState); err != nil {
		t.Fatal(err)
	}
	state, _ = local.snapshot(s.ctx)
	if !bytes.Equal(state.Data, data) {
		t.Fatal("large image download mismatch")
	}
	remote.setFailure(true)
	local.copy("text", []byte("failure then text"))
	oldLH := syncState.localHash
	if err := syncClipboardRound(s.ctx, s, local, &syncState); err == nil || syncState.localHash != oldLH {
		t.Fatal("failed clipboard advanced synchronization state")
	}
	remote.setFailure(false)
	if err := syncClipboardRound(s.ctx, s, local, &syncState); err != nil {
		t.Fatal(err)
	}
	state, _ = remote.snapshot(s.ctx)
	if string(state.Data) != "failure then text" {
		t.Fatal("text did not recover")
	}
	oldRemote := state.Sequence
	remote.copy("text", []byte("new remote"))
	source, _ := local.snapshot(s.ctx)
	if err := s.putClipboard(s.ctx, source, oldRemote, local); err == nil {
		t.Fatal("stale destination overwritten")
	}
	state, _ = remote.snapshot(s.ctx)
	if string(state.Data) != "new remote" {
		t.Fatal("new remote clipboard lost")
	}
	remote.copy("ignore", nil)
	local.copy("ignore", nil)
	if err := syncClipboardRound(s.ctx, s, local, &syncState); err != nil {
		t.Fatal(err)
	}
	local.copy("image", data)
	stale, _ := local.snapshot(s.ctx)
	local.copy("text", []byte("new local"))
	dest, _ := remote.snapshot(s.ctx)
	if err := s.putClipboard(s.ctx, stale, dest.Sequence, local); err == nil {
		t.Fatal("stale source committed")
	}
	state, _ = remote.snapshot(s.ctx)
	if state.Kind != "ignore" {
		t.Fatal("cancelled source changed remote")
	}
}

func TestClipboardDIBBoundsAndConversion(t *testing.T) {
	data := noisyClipboardPNG(t)
	dib, err := pngToClipboardDIB(data)
	if err != nil {
		t.Fatal(err)
	}
	img, err := clipboardDIBImage(dib)
	if err != nil || img.Bounds() != image.Rect(0, 0, 1024, 768) {
		t.Fatal("DIB conversion", err)
	}
	original, err := png.Decode(bytes.NewReader(data))
	if err != nil || img.At(500, 200) != original.At(500, 200) {
		t.Fatal("DIB pixel mismatch", err)
	}
	if _, err := clipboardDIBImage(dib[:100]); err == nil {
		t.Fatal("truncated DIB accepted")
	}
	binary.LittleEndian.PutUint32(dib[4:8], 0x7fffffff)
	if _, err := clipboardDIBImage(dib); err == nil {
		t.Fatal("oversized DIB accepted")
	}
	if unsafe.Sizeof(openFileName{}) != 152 {
		t.Fatal("OPENFILENAME x64 ABI mismatch")
	}
	for _, path := range []string{"", `C:\bad:stream`, `\\.\PhysicalDrive0`, `\\?\C:\a`} {
		if _, err := safeTransferPath(path); err == nil {
			t.Fatal("unsafe path accepted", path)
		}
	}
}

func TestAuxiliaryCancellationSourceChangeAndAuthentication(t *testing.T) {
	s, _ := auxiliaryFixture(t)
	dir := t.TempDir()
	source := filepath.Join(dir, "source.bin")
	target := filepath.Join(dir, "target.bin")
	if err := os.WriteFile(source, bytes.Repeat([]byte{42}, 2<<20), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(s.ctx)
	if err := s.uploadFile(ctx, source, target, false, func(n, total int64) {
		if n >= bulkChunk {
			cancel()
		}
	}); err == nil {
		t.Fatal("cancelled upload succeeded")
	}
	cancel()
	waitJPEG(t, func() bool { entries, err := os.ReadDir(dir); return err == nil && len(entries) == 1 })
	changed := false
	if err := s.uploadFile(s.ctx, source, target, false, func(n, total int64) {
		if !changed && n == total {
			changed = true
			stamp := time.Now().Add(time.Minute)
			if err := os.Chtimes(source, stamp, stamp); err != nil {
				t.Error(err)
			}
		}
	}); err == nil {
		t.Fatal("modified source committed")
	}
	waitJPEG(t, func() bool { entries, err := os.ReadDir(dir); return err == nil && len(entries) == 1 })
	c, err := dialTLSLogged(s.ctx, s.ctrl.conn.RemoteAddr().String(), false, net.IPv4(127, 0, 0, 1))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	w := &wire{conn: c, timeout: time.Second}
	if err := w.send(message{Type: "attach-aux", Version: protocolVersion, Proof: "wrong"}, nil); err != nil {
		t.Fatal(err)
	}
	if m, _, err := w.read(); err == nil && m.Type == "attached" {
		t.Fatal("unauthenticated auxiliary channel accepted")
	}
}

func TestNativeClipboardOwnerWithoutTouchingClipboard(t *testing.T) {
	if os.Getenv("MYDESK_LAN_UI_TEST") == "" {
		t.Skip("native UI opt-in")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	n := newNativeClipboard(ctx)
	_, err := n.call(ctx, func(owner uintptr) (clipState, error) {
		if owner == 0 {
			return clipState{}, errors.New("no owner")
		}
		return clipState{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestFileManagerNativeWindow(t *testing.T) {
	if os.Getenv("MYDESK_LAN_UI_TEST") == "" {
		t.Skip("native UI opt-in")
	}
	s, _ := auxiliaryFixture(t)
	s.openFileManager()
	t.Cleanup(func() {
		s.filesMu.Lock()
		if s.files != nil {
			s.files.cancel()
		}
		s.filesMu.Unlock()
	})
	result := make(chan bool, 1)
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		s.filesMu.Lock()
		f := s.files
		s.filesMu.Unlock()
		if f != nil {
			f.mu.Lock()
			h := f.hwnd
			f.mu.Unlock()
			if h != 0 {
				f.post(func() {
					result <- f.pathEdit != 0 && f.listBox != 0 && f.searchEdit != 0 && f.saveDirLabel != 0 && f.progressBar != 0 && f.darkBrush != 0 && len(f.buttons) == 8 && f.downloadDir == programDirectory()
				})
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case ok := <-result:
		if !ok {
			t.Fatal("F8 controls missing")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("F8 window failed")
	}
}
