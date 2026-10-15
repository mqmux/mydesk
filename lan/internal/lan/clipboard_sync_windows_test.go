//go:build windows && amd64

package lan

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"syscall"
	"testing"
)

// Inject transfer/acknowledgment faults without touching the system clipboard.
type clipboardSnapshotFault struct {
	*fakeClipboard
	snapshots int
	failAt    int
}

func (f *clipboardSnapshotFault) snapshot(ctx context.Context) (clipState, error) {
	f.snapshots++
	if f.snapshots == f.failAt {
		return clipState{}, errors.New("injected source check failure during long transfer")
	}
	return f.fakeClipboard.snapshot(ctx)
}

type clipboardLostAck struct {
	*fakeClipboard
	failOnce bool
}

func (f *clipboardLostAck) apply(ctx context.Context, state clipState, expected uint32) (uint32, error) {
	seq, err := f.fakeClipboard.apply(ctx, state, expected)
	if err == nil && f.failOnce {
		f.failOnce = false
		return 0, errors.New("injected lost completion after clipboard commit")
	}
	return seq, err
}

func TestClipboardFailedLongUploadPreservesNewRemoteCopy(t *testing.T) {
	s, remote := auxiliaryFixture(t)
	local := &clipboardSnapshotFault{fakeClipboard: &fakeClipboard{}, failAt: 2}
	local.copy("text", bytes.Repeat([]byte("长文本\r\n"), 50000))
	var syncState clipboardSyncState
	if err := syncClipboardRound(s.ctx, s, local, &syncState); err == nil {
		t.Fatal("missing injected source failure")
	}
	want := []byte("被控端自己新复制的内容")
	remote.copy("text", want)
	for i := 0; i < 3; i++ {
		if err := syncClipboardRound(s.ctx, s, local, &syncState); err != nil {
			t.Fatal(err)
		}
		got, _ := remote.snapshot(s.ctx)
		master, _ := local.fakeClipboard.snapshot(s.ctx)
		if !bytes.Equal(got.Data, want) || !bytes.Equal(master.Data, want) || remote.writes != 0 {
			t.Fatal("old master text overwrote the new remote copy after a failed upload")
		}
	}
}

func TestClipboardLostLongTextAckPreservesNewRemoteCopy(t *testing.T) {
	remote := &clipboardLostAck{fakeClipboard: &fakeClipboard{}, failOnce: true}
	remote.copy("ignore", nil)
	s := auxiliaryFixtureWithClipboard(t, remote)
	local := &fakeClipboard{}
	local.copy("text", bytes.Repeat([]byte("long initial clipboard\n"), 24000))
	var syncState clipboardSyncState
	if err := syncClipboardRound(s.ctx, s, local, &syncState); err == nil {
		t.Fatal("missing injected acknowledgment failure")
	}
	if remote.writes != 1 {
		t.Fatal("long text did not commit before the lost acknowledgment")
	}
	want := []byte("copy locally on the remote after commit")
	remote.copy("text", want)
	if err := syncClipboardRound(s.ctx, s, local, &syncState); err != nil {
		t.Fatal(err)
	}
	got, _ := remote.snapshot(s.ctx)
	master, _ := local.snapshot(s.ctx)
	if !bytes.Equal(got.Data, want) || !bytes.Equal(master.Data, want) || remote.writes != 1 {
		t.Fatal("unacknowledged old master text was resent over a new remote copy")
	}
}

func TestClipboardPendingUploadRecovery(t *testing.T) {
	for _, action := range []string{"retry", "lost-ack", "new-local", "remote-file", "remote-recopy", "source-rendered"} {
		t.Run(action, func(t *testing.T) {
			remote := &clipboardLostAck{fakeClipboard: &fakeClipboard{}, failOnce: action == "lost-ack"}
			remote.copy("text", []byte("previous remote copy"))
			s := auxiliaryFixtureWithClipboard(t, remote)
			local := &clipboardSnapshotFault{fakeClipboard: &fakeClipboard{}, failAt: 2}
			if action == "lost-ack" {
				local.failAt = 0
			}
			oldRemote, _ := remote.snapshot(s.ctx)
			var syncState clipboardSyncState
			syncState.remoteHash = oldRemote.Hash
			local.copy("text", bytes.Repeat([]byte("long pending clipboard\n"), 24000))
			if err := syncClipboardRound(s.ctx, s, local, &syncState); err == nil || syncState.pending == nil {
				t.Fatal("pending transfer was not retained")
			}
			want, _ := local.fakeClipboard.snapshot(s.ctx)
			switch action {
			case "new-local":
				local.copy("text", []byte("a genuinely newer master copy"))
				want, _ = local.fakeClipboard.snapshot(s.ctx)
			case "remote-file":
				remote.copy("ignore", nil)
				want, _ = remote.snapshot(s.ctx)
			case "remote-recopy":
				remote.copy("text", []byte("previous remote copy"))
				want, _ = remote.snapshot(s.ctx)
			case "source-rendered":
				local.copy("text", want.Data) // Windows may materialize another format with the same text.
				remote.copy("text", []byte("new remote copy during failed transfer"))
				want, _ = remote.snapshot(s.ctx)
			}
			for i := 0; i < 3; i++ {
				if err := syncClipboardRound(s.ctx, s, local, &syncState); err != nil {
					t.Fatal(err)
				}
			}
			got, _ := remote.snapshot(s.ctx)
			if got.Kind != want.Kind || !bytes.Equal(got.Data, want.Data) || syncState.pending != nil {
				t.Fatal("pending upload did not resolve safely", action, got.Kind)
			}
			writes := 1
			if action == "remote-file" || action == "remote-recopy" || action == "source-rendered" {
				writes = 0
			}
			if remote.writes != writes {
				t.Fatal("repeated old upload / clipboard echo", remote.writes)
			}
		})
	}
}

func TestClipboardNewIgnoredLocalCopyIsNotErased(t *testing.T) {
	s, remote := auxiliaryFixture(t)
	local := &fakeClipboard{}
	local.copy("ignore", nil)
	var syncState clipboardSyncState
	if err := syncClipboardRound(s.ctx, s, local, &syncState); err != nil {
		t.Fatal(err)
	}
	// Two unsupported/file copies share the same hash but not the same OS sequence.
	local.copy("ignore", nil)
	remote.copy("text", []byte("new remote text while a local file was copied"))
	if err := syncClipboardRound(s.ctx, s, local, &syncState); err != nil {
		t.Fatal(err)
	}
	got, _ := local.snapshot(s.ctx)
	if got.Kind != "ignore" || local.writes != 0 {
		t.Fatal("local file/oversized copy was overwritten")
	}
}

type fakeClipboardReader struct {
	formats     map[uintptr][]byte
	seq         uint32
	opened      bool
	opens       int
	closes      int
	synthesized bool
	changeClose bool
	readError   error
	closeError  error
}

func (r *fakeClipboardReader) open(ctx context.Context, owner uintptr) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.opened {
		return errors.New("clipboard already open")
	}
	r.opened = true
	r.opens++
	return nil
}
func (r *fakeClipboardReader) close() error {
	r.opened = false
	r.closes++
	if r.changeClose {
		r.seq++
	}
	return r.closeError
}
func (r *fakeClipboardReader) sequence() uint32 { return r.seq }
func (r *fakeClipboardReader) available(format uintptr) bool {
	_, ok := r.formats[format]
	return ok
}
func (r *fakeClipboardReader) global(format uintptr, limit int) ([]byte, error) {
	if !r.opened {
		return nil, errors.New("read after clipboard close")
	}
	if r.synthesized {
		r.synthesized = false
		r.seq++
	}
	if r.readError != nil {
		return nil, r.readError
	}
	data := r.formats[format]
	if len(data) > limit {
		return nil, errClipboardTooLarge
	}
	return bytes.Clone(data), nil
}
func (r *fakeClipboardReader) pngFormat() uintptr { return 100 }

func clipboardUTF16Bytes(t *testing.T, text string) []byte {
	t.Helper()
	chars, err := syscall.UTF16FromString(text)
	if err != nil {
		t.Fatal(err)
	}
	data := make([]byte, len(chars)*2)
	for i, ch := range chars {
		binary.LittleEndian.PutUint16(data[i*2:], ch)
	}
	return data
}

func TestClipboardRawReadClosesAndAcceptsRenderedSequence(t *testing.T) {
	text := string(bytes.Repeat([]byte("文"), 168499)) + "\r\nemoji: 🙂"
	for _, mode := range []string{"normal", "rendered", "changed", "busy", "close-failure"} {
		t.Run(mode, func(t *testing.T) {
			r := &fakeClipboardReader{formats: map[uintptr][]byte{13: clipboardUTF16Bytes(t, text)}, seq: 10}
			r.synthesized = mode == "rendered"
			r.changeClose = mode == "changed"
			if mode == "busy" {
				r.readError = errors.New("injected GlobalLock failure")
			}
			if mode == "close-failure" {
				r.closeError = errors.New("injected CloseClipboard failure")
			}
			state, err := readClipboardSnapshot(context.Background(), 1, r)
			if r.opened || r.opens != 1 || r.closes != 1 {
				t.Fatal("clipboard lock leaked", r.opens, r.closes)
			}
			if mode == "changed" || mode == "busy" || mode == "close-failure" {
				if err == nil {
					t.Fatal("failure or stale data was accepted")
				}
				return
			}
			if err != nil || state.Kind != "text" || string(state.Data) != text || state.Sequence != r.seq {
				t.Fatal("long UTF16 text / rendered sequence lost", err)
			}
		})
	}
}

func TestClipboardOversizedTextStaysLocalAndNextCopyRecovers(t *testing.T) {
	for _, text := range []string{
		string(bytes.Repeat([]byte("x"), maxClipboardText/2)), // UTF16 incl. NUL exceeds limit.
		string(bytes.Repeat([]byte("文"), 750000)),             // UTF16 fits; UTF8 transport does not.
	} {
		r := &fakeClipboardReader{formats: map[uintptr][]byte{13: clipboardUTF16Bytes(t, text)}, seq: 3}
		state, err := readClipboardSnapshot(context.Background(), 1, r)
		if err != nil || state.Kind != "ignore" || len(state.Data) != 0 || r.opened {
			t.Fatal("oversized text poisons polling", err)
		}
		r.seq++
		r.formats[13] = clipboardUTF16Bytes(t, "subsequent short local copy")
		state, err = readClipboardSnapshot(context.Background(), 1, r)
		if err != nil || string(state.Data) != "subsequent short local copy" || r.opens != r.closes {
			t.Fatal("short copy after oversize failed", err)
		}
	}
	for _, malformed := range [][]byte{{1}, {1, 2}, {1, 2, 3}} {
		if _, err := decodeClipboardText(malformed); err == nil {
			t.Fatal("unterminated/odd UTF16 accepted")
		}
	}
}

func TestClipboardImageFallbackClosesBetweenFormats(t *testing.T) {
	data := noisyClipboardPNG(t)
	dib, err := pngToClipboardDIB(data)
	if err != nil {
		t.Fatal(err)
	}
	r := &fakeClipboardReader{formats: map[uintptr][]byte{100: []byte("invalid PNG"), 8: dib}, seq: 5}
	state, err := readClipboardSnapshot(context.Background(), 1, r)
	if err != nil || state.Kind != "image" || validClipboardPNG(state.Data) != nil || r.opened || r.opens != 2 || r.closes != 2 {
		t.Fatal("PNG -> DIB fallback leaked clipboard or failed", err)
	}
}
