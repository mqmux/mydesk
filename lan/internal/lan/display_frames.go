package lan

import (
	"sync"
	"time"
)

// Only already decoded, unpresented pictures may be replaced here. Codec
// reference frames still pass through the decoder in their original order.
type displayFrame struct {
	native   nativeDisplaySurface
	pixels   []byte
	format   frameFormat
	sequence uint64
	decoded  time.Time
	readers  int
}

type nativeDisplaySurface interface {
	size() (int, int)
	release()
}

type displayMailbox struct {
	latest   *displayFrame
	sequence uint64
	once     sync.Once
	free     chan []byte
}

func (m *displayMailbox) buffers() chan []byte {
	m.once.Do(func() { m.free = make(chan []byte, 3) })
	return m.free
}

func (m *displayMailbox) buffer(size int) []byte {
	select {
	case b := <-m.buffers():
		if len(b) == size {
			return b
		}
	default:
	}
	return make([]byte, size)
}

func (m *displayMailbox) recycle(b []byte) {
	if b == nil {
		return
	}
	select {
	case m.buffers() <- b:
	default:
	}
}

// The owner's mutex protects publish/acquire/release. Upload, scaling and
// presentation happen outside that lock. Leased pictures cannot be recycled.
func (m *displayMailbox) publish(b []byte, now time.Time) {
	m.publishFormat(b, pixelBGRA, now)
}

func (m *displayMailbox) publishFormat(b []byte, format frameFormat, now time.Time) {
	old := m.latest
	m.sequence++
	m.latest = &displayFrame{pixels: b, format: format, sequence: m.sequence, decoded: now}
	if old != nil && old.readers == 0 {
		m.recycleFrame(old)
	}
}

func (m *displayMailbox) publishNative(surface nativeDisplaySurface, now time.Time) {
	old := m.latest
	m.sequence++
	m.latest = &displayFrame{native: surface, format: pixelBGRA, sequence: m.sequence, decoded: now}
	if old != nil && old.readers == 0 {
		m.recycleFrame(old)
	}
}

func (m *displayMailbox) recycleFrame(f *displayFrame) {
	if f.native != nil {
		f.native.release()
		f.native = nil
	} else {
		m.recycle(f.pixels)
	}
}

func (m *displayMailbox) acquire() *displayFrame {
	f := m.latest
	if f != nil {
		f.readers++
	}
	return f
}

func (m *displayMailbox) release(f *displayFrame) {
	if f == nil {
		return
	}
	f.readers--
	if f.readers == 0 && f != m.latest {
		m.recycleFrame(f)
	}
}

type displaySnapshot struct {
	frame        *displayFrame
	pixels       []byte
	format       frameFormat
	baseRevision uint64
	wholeRefine  bool
	repairs      overlayUpdate
}

type renderTimes struct{ upload, submit time.Duration }

// CPU-side upload/submission and local decoded-to-submit age, not GPU
// execution time or input-to-photon latency.
type displayMetrics struct {
	frames, skipped           uint64
	upload, submit, age, peak time.Duration
}

func (m *displayMetrics) record(f *displayFrame, previous uint64, times renderTimes, now time.Time) {
	if f == nil || f.sequence <= previous {
		return
	}
	m.frames++
	if f.sequence > previous+1 {
		m.skipped += f.sequence - previous - 1
	}
	m.upload += times.upload
	m.submit += times.submit
	age := max(time.Duration(0), now.Sub(f.decoded))
	m.age += age
	m.peak = max(m.peak, age)
}
