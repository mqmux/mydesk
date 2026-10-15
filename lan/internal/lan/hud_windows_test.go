//go:build windows && amd64

package lan

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"net"
	"sync"
	"testing"
	"time"
)

func TestF9LongAndShortPressModes(t *testing.T) {
	var events []inputEvent
	v := &viewer{onInput: func(e inputEvent) { events = append(events, e) }}
	v.capture.Store(true)
	now := time.Now()
	v.handleF9(true, now)
	v.handleF9(false, now.Add(time.Second))
	if len(events) != 2 || events[0].VK != 0x78 || events[1].Kind != "key-up" {
		t.Fatal(events)
	}
	events = nil
	v.handleF9(true, now)
	v.checkF9(now.Add(2999 * time.Millisecond))
	if v.keyMode.Load() {
		t.Fatal("F9 toggled too early")
	}
	v.checkF9(now.Add(3 * time.Second))
	if !v.keyMode.Load() {
		t.Fatal("long F9 did not enter Key mode")
	}
	events = nil
	v.handleF9(false, now.Add(3*time.Second))
	if len(events) != 0 {
		t.Fatal("long press sent F9")
	}
	for _, key := range []uint32{0x75, 0x76, 0x77, 0x79, 0x7A, 27} {
		if v.postLocalAction(key) {
			t.Fatal("Key mode intercepted ordinary key", key)
		}
	}
	if v.modeStatus("test") != "[Mode: Key] test" {
		t.Fatal("mode title")
	}
	v.handleF9(true, now.Add(4*time.Second))
	v.checkF9(now.Add(7 * time.Second))
	v.handleF9(false, now.Add(8*time.Second))
	if v.keyMode.Load() {
		t.Fatal("long F9 did not restore Func")
	}
	for _, row := range []struct {
		x, y          int
		visible, want bool
	}{{0, 0, false, true}, {100, 3, false, true}, {100, 5, false, false}, {100, 30, true, true}, {100, 44, true, false}, {-1, 0, false, false}} {
		if wantHUD(row.x, row.y, 1920, row.visible) != row.want {
			t.Fatal(row)
		}
	}
}
func TestRegionalRefineWireAndCursorPolicy(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	var mu sync.Mutex
	pixels := make([]byte, 128*128*4)
	for i := range pixels {
		pixels[i] = 80
	}
	s := &hostSession{ctx: ctx, video: &wire{conn: a, timeout: 3 * time.Second}, cfg: videoConfig{area: desktop{width: 128, height: 128}, grab: func() ([]byte, error) { mu.Lock(); defer mu.Unlock(); return bytes.Clone(pixels), nil }}}
	s.cursorHidden.Store(true)
	done := make(chan struct{})
	go func() { defer close(done); s.regionalRefineLoop() }()
	defer func() {
		cancel()
		a.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("refine did not stop")
		}
	}()
	w := &wire{conn: b, timeout: 3 * time.Second}
	m, data, err := w.read()
	if err != nil || m.Type != "refine-patch" || m.Width != 128 {
		t.Fatal(m, err)
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width != 128 {
		t.Fatal(cfg, err)
	}
	mu.Lock()
	pixels[0] = 200
	mu.Unlock()
	m, _, err = w.read()
	if err != nil || m.Type != "refine-clear-patch" || m.Width != 64 || m.Height != 64 {
		t.Fatal("full repair discarded on small change", m, err)
	}
	m, data, err = w.read()
	if err != nil || m.Type != "refine-patch" || m.Width != 64 {
		t.Fatal(m, err)
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil || img.Bounds() != image.Rect(0, 0, 64, 64) {
		t.Fatal(err)
	}
	s.cursorHidden.Store(false)
	m, _, err = w.read()
	if err != nil || m.Type != "refine-clear-patch" || m.Width != 128 {
		t.Fatal("cursor mode must clear independent GDI repair", m, err)
	}
}
