//go:build windows && amd64

package lan

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"time"
)

func (s *hostSession) displayTargets(c videoConfig) []videoConfig {
	if s.capturePreference != "" {
		c.capture = s.capturePreference
	}
	if s.screenList != nil {
		return s.screenList(c)
	}
	return screenTargets(c)
}

// Match the same physical Windows device even if its rectangle/index changed.
// Without an identity (legacy/test config), prefer its rectangle then index.
// A removed device falls back to the first available display, not stale bounds.
func resolveDisplay(c videoConfig, targets []videoConfig) (videoConfig, bool) {
	for _, target := range targets {
		if c.display != "" && target.display == c.display && validDesktop(target.area) {
			return target, true
		}
	}
	if c.display == "" {
		for _, target := range targets {
			if target.area == c.area && validDesktop(target.area) {
				return target, true
			}
		}
		for _, target := range targets {
			if target.monitor == c.monitor && validDesktop(target.area) {
				return target, true
			}
		}
	}
	for _, target := range targets {
		if validDesktop(target.area) {
			return target, true
		}
	}
	return c, false
}

func sameCaptureDisplay(a, b videoConfig) bool {
	return a.area == b.area && a.monitor == b.monitor && a.display == b.display && a.capture == b.capture
}

func (s *hostSession) releaseInputs() {
	s.inputMu.Lock()
	defer s.inputMu.Unlock()
	if s.input != nil {
		s.input.release()
	}
}

func (s *hostSession) applyRemoteInput(m message) error {
	s.inputMu.Lock()
	defer s.inputMu.Unlock()
	if s.screenSwitch && m.Input.Kind != "reset" {
		_, _, generation := s.screenConfig()
		if m.ScreenGeneration != generation || s.screenReady.Load() != generation {
			return nil
		}
	}
	if s.inputDisabled.Load() && m.Input.Kind != "reset" {
		return nil
	}
	if s.input == nil {
		return errors.New("input state unavailable")
	}
	err := s.input.apply(m.Input)
	if m.Input.Kind != "move" {
		s.refineClear.Add(1)
	}
	return err
}

// Called with displayMu held. Input checking/applying and changing its origin
// share inputMu so no old-coordinate input can cross the generation transition.
func (s *hostSession) commitDisplay(next videoConfig, profile encoderProfile) uint64 {
	s.inputMu.Lock()
	defer s.inputMu.Unlock()
	if s.input != nil {
		s.input.release()
		s.input.area = next.area
	}
	s.configMu.Lock()
	s.cfg, s.profile = next, profile
	s.screenGeneration++
	generation := s.screenGeneration
	s.screenReady.Store(0)
	s.bitrate.Store(int64(next.bitrate))
	s.configMu.Unlock()
	s.refineClear.Add(1)
	s.requestRestart()
	return generation
}

// Try the proven encoder/runtime first. Resolution changes should not repeat
// every unavailable vendor probe or discard a working driver-compatible NVENC.
func probeDisplayProfile(ctx context.Context, c videoConfig, p encoderProfile) (encoderProfile, error) {
	if p.codec != codecH264 || c.encoder == "jpeg" || c.ffmpeg == "" || c.area.width%2 != 0 || c.area.height%2 != 0 {
		return selectProfile(ctx, c)
	}
	capture := p.capture
	if c.capture != "auto" && c.source != "test" {
		capture = c.capture
	}
	if c.source == "test" {
		capture = "test"
	}
	args := packetProfileArgs(c, p.name, capture)
	path := p.path
	if path == "" {
		path = c.ffmpeg
	}
	if path != c.ffmpeg {
		args = compatibleArgs(args)
	}
	probeArgs := append(append([]string(nil), args[:len(args)-1]...), "-frames:v", "3", "pipe:1")
	probe, stop := context.WithTimeout(ctx, 4*time.Second)
	cmd := helperCommand(probe, path, probeArgs...)
	cmd.Stderr = &tailBuffer{}
	data, err := cmd.Output()
	stop()
	if err == nil && validEncoderPacketsAt(data, c.area) {
		p.capture, p.path, p.args, p.packetized = capture, path, args, true
		return p, nil
	}
	if ctx.Err() != nil {
		return encoderProfile{}, ctx.Err()
	}
	return selectProfile(ctx, c)
}

func (s *hostSession) refreshDisplay() (bool, error) {
	s.displayMu.Lock()
	defer s.displayMu.Unlock()
	if s.ctx.Err() != nil {
		return false, s.ctx.Err()
	}
	c, previous, _ := s.screenConfig()
	next, ok := resolveDisplay(c, s.displayTargets(c))
	if !ok {
		return false, errors.New("暂时没有可用显示器，等待显示配置恢复")
	}
	if sameCaptureDisplay(c, next) {
		return false, nil
	}
	if s.autoBitrate {
		next.bitrate = automaticBitrate(next)
	} else {
		next.bitrate = int(s.bitrate.Load())
	}
	next.hideCursor = s.cursorHidden.Load()
	probe, stop := context.WithTimeout(s.ctx, 12*time.Second)
	profile, err := probeDisplayProfile(probe, next, previous)
	stop()
	if err != nil {
		return false, err
	}
	// A second mode change/removal during probing must not commit a stale size.
	fresh, ok := resolveDisplay(c, s.displayTargets(c))
	if s.ctx.Err() != nil {
		return false, s.ctx.Err()
	}
	if !ok || !sameCaptureDisplay(next, fresh) {
		return false, errors.New("显示配置仍在变化，等待稳定后重试")
	}
	generation := s.commitDisplay(next, profile)
	log.Printf("[lan][显示变化] %dx%d @ (%d,%d) -> %dx%d @ (%d,%d) | %s/%s | generation=%d", c.area.width, c.area.height, c.area.left, c.area.top, next.area.width, next.area.height, next.area.left, next.area.top, profile.capture, profile.name, generation)
	return true, nil
}

func sameDisplayOutputs(a, b []displayOutput) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (s *hostSession) watchDisplays() {
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	var observed, handled []displayOutput
	var retry, lastLog time.Time
	for {
		select {
		case <-s.ctx.Done():
			return
		case now := <-tick.C:
			if now.Before(retry) {
				continue
			}
			if s.screenList == nil {
				outputs := monitorOutputs() // Cheap Win32 poll; DXGI only on change.
				if !sameDisplayOutputs(outputs, observed) {
					observed = outputs
					continue // Two consecutive snapshots debounce display transitions.
				}
				if len(outputs) > 0 && sameDisplayOutputs(outputs, handled) {
					continue
				}
			}
			_, err := s.refreshDisplay()
			if err != nil {
				retry = now.Add(time.Second)
				if s.ctx.Err() == nil && now.Sub(lastLog) >= 5*time.Second {
					log.Printf("[lan][显示变化] %v", err)
					lastLog = now
				}
			} else {
				handled = observed
			}
		}
	}
}

// ddagrab can start producing a new SPS before the OS change is committed.
// Never send that differently-sized IDR under the old screen descriptor. Only
// key pictures need this check; ordinary P pictures incur no new SPS parsing.
func encodedScreenMatches(data []byte, area desktop) bool {
	for _, nal := range annexBNALs(data) {
		if nal[0]&31 == 7 {
			_, w, h, err := parseH264Color(nal)
			if err == nil && (w != area.width || h != area.height) {
				return false
			}
		}
	}
	return true
}

func validEncoderPacketsAt(data []byte, area desktop) bool {
	frames, keys := 0, 0
	err := readEncoderPackets(bytes.NewReader(data), func(b []byte, key bool) error {
		frames++
		if key {
			keys++
			if !encodedScreenMatches(b, area) {
				return errors.New("encoder probe resolution differs from current display")
			}
		}
		return nil
	})
	return err == nil && frames == 3 && keys > 0
}

func displayUnavailableError(c videoConfig) error {
	return fmt.Errorf("屏幕 %d 当前不可用，请检查被控显示配置", c.monitor+1)
}
