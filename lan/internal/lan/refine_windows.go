//go:build windows && amd64

package lan

import (
	"bytes"
	"image"
	"image/png"
	"log"
	"runtime"
	"time"
)

func validRefineRect(m message, w, h int) bool {
	return m.Left >= 0 && m.Top >= 0 && m.Left < w && m.Top < h && m.Width > 0 && m.Height > 0 && m.Width <= w-m.Left && m.Height <= h-m.Top && m.Left%64 == 0 && m.Top%64 == 0 && (m.Left+m.Width == w || (m.Left+m.Width)%64 == 0) && (m.Top+m.Height == h || (m.Top+m.Height)%64 == 0)
}
func (v *viewer) setRefinePatch(m message, pixels []byte) {
	v.mu.Lock()
	if m.ScreenGeneration != v.screenGeneration || !validRefineRect(m, v.width, v.height) {
		v.mu.Unlock()
		return
	}
	if len(v.repairs.valid) == 0 {
		v.repairs.reset(v.width, v.height)
	}
	r := image.Rect(m.Left, m.Top, m.Left+m.Width, m.Top+m.Height)
	if v.repairs.patch(r, pixels) && pixels != nil {
		format := pixelBGRA
		if v.frames.latest != nil {
			format = v.frames.latest.format
		}
		v.repairs.rememberSurface(r, v.pixels, format)
	}
	h := v.hwnd
	v.mu.Unlock()
	v.requestFramePaint(h)
}
func (s *hostSession) regionalRefineLoop() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	var grab *screenGrabber
	defer func() {
		if grab != nil {
			grab.close()
		}
	}()
	var capture func() ([]byte, error)
	var generation uint64
	var resumed uint64
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	w, h := 0, 0
	tracker := refineTiles{}
	hidden := s.cursorHidden.Load()
	send := func(kind string, r image.Rectangle, data []byte) bool {
		_, _, current := s.screenConfig()
		if current != generation {
			return true
		} // Retired capture; rebuild next tick.
		err := s.video.send(message{Type: kind, Left: r.Min.X, Top: r.Min.Y, Width: r.Dx(), Height: r.Dy(), ScreenGeneration: generation}, data)
		if err != nil && s.ctx.Err() == nil {
			log.Printf("[lan][refine] %v", err)
		}
		return err == nil
	}
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.refineWake:
		case <-tick.C:
		}
		// Retain the capture/tracker and decoder state while hidden. Video still
		// updates the latest picture; clipboard/files never share this gate.
		if s.minimized.Load() {
			tick.Reset(time.Second)
			continue
		}
		tick.Reset(100 * time.Millisecond)
		now := time.Now()
		cfg, _, current := s.screenConfig()
		if s.screenSwitch && s.screenReady.Load() != current {
			continue
		}
		if capture == nil || generation != current {
			if grab != nil {
				grab.close()
				grab = nil
			}
			capture = cfg.grab
			if capture == nil {
				var err error
				grab, err = newScreenGrabber(cfg.area)
				if err != nil {
					continue
				}
				capture = func() ([]byte, error) { return grab.captureCursor(false) }
			}
			generation = current
			w, h = cfg.area.width, cfg.area.height
			tracker.reset(w, h)
		}
		if next := s.refineResumes.Load(); next != resumed {
			resumed = next
			// Video may have invalidated a repair while hidden even if the
			// desktop has since returned to the same pixels as our last sample.
			// Keep existing visible repairs; revalidate/send after the normal
			// settle time instead of blocking the first restored video picture.
			for i := range tracker.valid {
				tracker.valid[i], tracker.changed[i] = false, now
			}
		}
		nextHidden := s.cursorHidden.Load()
		if hidden != nextHidden {
			hidden = nextHidden
			tracker.reset(w, h)
			if !send("refine-clear-patch", image.Rect(0, 0, w, h), nil) {
				return
			}
		}
		// A separately captured GDI cursor cannot be synchronized with the encoded
		// cursor. Keep repairs off while that optional video cursor is enabled.
		if !hidden {
			continue
		}
		pixels, err := capture()
		if err != nil {
			if len(tracker.previous) > 0 {
				if !send("refine-clear-patch", image.Rect(0, 0, w, h), nil) {
					return
				}
				tracker.reset(w, h)
			}
			continue
		}
		for _, r := range tracker.observe(pixels, now) {
			if !send("refine-clear-patch", r, nil) {
				return
			}
		}
		sent := 0
		for _, ready := range tracker.ready(now) {
			for y := ready.Min.Y; y < ready.Max.Y; y += 1024 {
				for x := ready.Min.X; x < ready.Max.X; x += 1024 {
					if sent >= 32 {
						continue
					}
					r := image.Rect(x, y, min(x+1024, ready.Max.X), min(y+1024, ready.Max.Y))
					var out bytes.Buffer
					enc := png.Encoder{CompressionLevel: png.BestSpeed}
					if err := enc.Encode(&out, bgraRectImage(pixels, w, r)); err != nil {
						continue
					}
					if out.Len() > maxWireBody {
						continue
					}
					if !send("refine-patch", r, out.Bytes()) {
						return
					}
					tracker.visit(r, func(i int) { tracker.valid[i] = true })
					sent++
				}
			}
		}
	}
}
