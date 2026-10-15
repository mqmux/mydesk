package lan

import (
	"bytes"
	"errors"
	"io"
)

// FFmpeg inserts AUDs, so a frame may contain SPS/PPS, SEI and multiple slices.
// Bound the parser even when a helper emits a malformed stream.
func readAccessUnits(r io.Reader, emit func([]byte, bool) error) error {
	buf := make([]byte, 0, 256*1024)
	chunk := make([]byte, 64*1024)
	for {
		n, err := r.Read(chunk)
		if n > 0 {
			buf = append(buf, chunk[:n]...)
		}
		for {
			first, next := -1, -1
			for i := 0; i+4 < len(buf); i++ {
				start := 0
				if buf[i] == 0 && buf[i+1] == 0 {
					if buf[i+2] == 1 {
						start = i + 3
					} else if i+4 < len(buf) && buf[i+2] == 0 && buf[i+3] == 1 {
						start = i + 4
					}
				}
				if start > 0 && buf[start]&31 == 9 {
					if first < 0 {
						first = i
					} else {
						next = i
						break
					}
					i = start
				}
			}
			if next < 0 {
				break
			}
			frame := bytes.Clone(buf[:next])
			if e := emit(frame, containsNAL(frame, 5)); e != nil {
				return e
			}
			buf = append(buf[:0], buf[next:]...)
		}
		if len(buf) > maxVideoFrame {
			return errors.New("H.264 access unit exceeds limit")
		}
		if err != nil {
			if err == io.EOF && len(buf) > 0 {
				return emit(bytes.Clone(buf), containsNAL(buf, 5))
			}
			return err
		}
	}
}
func containsNAL(b []byte, kind byte) bool {
	for i := 0; i+3 < len(b); i++ {
		if b[i] == 0 && b[i+1] == 0 {
			j := i + 2
			if b[j] == 0 {
				j++
			}
			if j+1 < len(b) && b[j] == 1 && b[j+1]&31 == kind {
				return true
			}
		}
	}
	return false
}

// Preserve inter-frame dependencies. Once a deadline or queue loses a frame,
// only an IDR can restart decoding. Frame IDs are global across encoder resets.
type frameOrder struct {
	last, epoch uint64
	needsKey    bool
	pending     map[uint64]videoFrame
}

func newFrameOrder() *frameOrder {
	return &frameOrder{needsKey: true, pending: map[uint64]videoFrame{}}
}
func (o *frameOrder) reset() { o.pending = map[uint64]videoFrame{}; o.needsKey = true }
func (o *frameOrder) push(f videoFrame) []videoFrame {
	if f.epoch < o.epoch || f.id <= o.last {
		return nil
	}
	if f.epoch > o.epoch {
		o.epoch = f.epoch
		o.reset()
	}
	if f.codec == codecJPEG {
		o.last = f.id
		return []videoFrame{f}
	}
	if f.key && (o.needsKey || f.id > o.last+1) {
		o.pending = map[uint64]videoFrame{}
		o.last = f.id
		o.needsKey = false
		return []videoFrame{f}
	}
	if o.needsKey {
		return nil
	}
	if len(o.pending) >= 16 {
		o.reset()
		return nil
	}
	o.pending[f.id] = f
	var out []videoFrame
	for {
		n, ok := o.pending[o.last+1]
		if !ok {
			break
		}
		delete(o.pending, o.last+1)
		o.last = n.id
		out = append(out, n)
	}
	return out
}
