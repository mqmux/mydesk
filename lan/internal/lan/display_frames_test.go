package lan

import (
	"bytes"
	"image"
	"sync"
	"testing"
	"time"
)

func TestDisplayMailboxLatestAndOwnership(t *testing.T) {
	var m displayMailbox
	first := []byte{1, 2, 3, 4}
	m.publish(first, time.Now())
	lease := m.acquire()
	for i := 0; i < 30; i++ {
		b := m.buffer(4)
		b[0] = byte(i + 10)
		m.publish(b, time.Now())
	}
	if !bytes.Equal(lease.pixels, first) || first[0] != 1 {
		t.Fatal("painting frame recycled while still leased")
	}
	latest := m.acquire()
	if latest.sequence != 31 || latest.pixels[0] != 39 {
		t.Fatal("mailbox retained a stale FIFO picture")
	}
	m.release(lease)
	m.release(latest)
	if latest.readers != 0 || lease.readers != 0 || len(m.buffers()) > 3 {
		t.Fatal("invalid reference count or unbounded buffer cache")
	}
}

func TestDisplayMailboxConcurrentBufferReuse(t *testing.T) {
	var m displayMailbox
	var mu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			b := m.buffer(32)
			for j := range b {
				b[j] = byte(i)
			}
			mu.Lock()
			m.publish(b, time.Now())
			mu.Unlock()
		}
	}()
	for i := 0; i < 1000; i++ {
		mu.Lock()
		f := m.acquire()
		mu.Unlock()
		if f != nil {
			for _, x := range f.pixels {
				if x != f.pixels[0] {
					t.Error("leased pixels changed concurrently")
				}
			}
		}
		mu.Lock()
		m.release(f)
		mu.Unlock()
	}
	wg.Wait()
}

func TestDisplayOverlayImmutableAndMotion(t *testing.T) {
	var source refineOverlay
	source.reset(128, 65)
	video := bytes.Repeat([]byte{100, 100, 100, 255}, 128*65)
	patch := bytes.Repeat([]byte{150, 150, 150, 255}, 128*65)
	source.patch(image.Rect(0, 0, 128, 65), patch)
	source.rememberVideo(image.Rect(0, 0, 128, 65), video)
	first := source.snapshot(true)
	var display displayOverlay
	display.update(first, 128, 65)
	if !bytes.Equal(display.compose(video, 128, 65), source.compose(video)) {
		t.Fatal("repair composition differs")
	}
	// Network updates must not overwrite an immutable upload snapshot.
	source.patch(image.Rect(0, 0, 64, 64), bytes.Repeat([]byte{200, 200, 200, 255}, 64*64))
	if first.patches[0].pixels[0] != 150 {
		t.Fatal("upload snapshot overwritten")
	}
	display.update(source.snapshot(false), 128, 65)
	if display.compose(video, 128, 65)[0] != 200 {
		t.Fatal("updated patch not visible")
	}
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			video[(y*128+x)*4] = 240
		}
	}
	source.invalidateMotion(video)
	update := source.snapshot(false)
	if update.mask == nil || len(update.patches) != 0 {
		t.Fatal("motion should update mask, not copy repair image")
	}
	display.update(update, 128, 65)
	got := display.compose(video, 128, 65)
	if got[0] != 240 || got[64*4] != 150 {
		t.Fatal("motion hidden or unrelated repair lost")
	}
	if !bytes.Equal(got, source.compose(video)) {
		t.Fatal("fallback differs from original compositor")
	}
	quiet := source.snapshot(false)
	if quiet.mask != nil || len(quiet.patches) != 0 {
		t.Fatal("unchanged repair texture unnecessarily uploaded")
	}
	// A device recreation requires a full replay despite drained dirty bits.
	var recreated displayOverlay
	recreated.update(source.snapshot(true), 128, 65)
	if !bytes.Equal(recreated.compose(video, 128, 65), got) {
		t.Fatal("device recreation lost repairs")
	}
}

func TestDisplayMetricsDistinguishSupersededFrames(t *testing.T) {
	now := time.Now()
	var m displayMetrics
	m.record(&displayFrame{sequence: 5, decoded: now.Add(-2 * time.Millisecond)}, 2, renderTimes{upload: time.Millisecond}, now)
	if m.frames != 1 || m.skipped != 2 || m.age != 2*time.Millisecond {
		t.Fatal(m)
	}
}
