//go:build windows && amd64

package lan

import (
	"context"
	"log"
	"time"
)

const screenSwitchFeature = "screen-switch-v1"

func hasFeature(features []string, feature string) bool {
	for _, f := range features {
		if f == feature {
			return true
		}
	}
	return false
}

func validDesktop(d desktop) bool {
	return d.width > 0 && d.height > 0 && int64(d.width)*int64(d.height) <= 16<<20
}

// Adapter-0 outputs retain their existing ddagrab output_idx mapping. Extra
// Win32 displays (other GPUs/virtual adapters) use GDI capture by desktop
// coordinates, not a guessed DXGI index. Encoding quality/FPS stay unchanged.
func screenTargets(c videoConfig) []videoConfig {
	if c.source == "test" {
		return []videoConfig{c}
	}
	var result []videoConfig
	windows := monitorOutputs()
	for _, output := range dxgiDisplayOutputs() {
		area := output.area
		// Win32 supplies the current desktop geometry even if DXGI's previous
		// mode rectangle has not caught up yet. Keep its actual output index.
		for _, current := range windows {
			if current.device == output.device {
				area = current.area
				break
			}
		}
		if !validDesktop(area) {
			continue
		}
		next := c
		next.monitor, next.area, next.display = output.index, area, output.device
		result = append(result, next)
	}
	for _, output := range windows {
		area := output.area
		if !validDesktop(area) {
			continue
		}
		duplicate := false
		for _, existing := range result {
			if existing.area == area {
				duplicate = true
				break
			}
		}
		if !duplicate {
			next := c
			next.area, next.capture, next.display = area, "gdi", output.device
			result = append(result, next)
		}
	}
	return result
}

func screenPosition(c videoConfig) (int, int) {
	targets := screenTargets(c)
	for i, target := range targets {
		if (c.display != "" && c.display == target.display) || (c.display == "" && c.area == target.area) {
			return i, len(targets)
		}
	}
	return 0, max(1, len(targets))
}

func (s *hostSession) screenConfig() (videoConfig, encoderProfile, uint64) {
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	return s.cfg, s.profile, s.screenGeneration
}

func screenDescriptor(c videoConfig, p encoderProfile, generation, epoch uint64) message {
	index, count := screenPosition(c)
	d := c.area
	return message{Type: "screen", Epoch: epoch, ScreenGeneration: generation, Monitor: index, MonitorCount: count,
		Width: d.width, Height: d.height, Left: d.left, Top: d.top, FPS: c.fps, Quality: c.quality,
		Bitrate: c.bitrate, Codec: p.codec, Backend: p.capture + "/" + p.name}
}

func (s *hostSession) describeScreen(c videoConfig, p encoderProfile, generation, epoch uint64) message {
	desc := screenDescriptor(c, p, generation, epoch)
	if s.screenList != nil {
		targets := s.screenList(c)
		desc.MonitorCount = max(1, len(targets))
		for i, target := range targets {
			if (c.display != "" && c.display == target.display) || (c.display == "" && target.area == c.area) {
				desc.Monitor = i
				break
			}
		}
	}
	return desc
}

func (s *hostSession) nextMonitor() {
	s.displayMu.Lock()
	defer s.displayMu.Unlock()
	c, _, _ := s.screenConfig()
	if s.capturePreference != "" {
		c.capture = s.capturePreference
	}
	// Release held inputs before probing, including on failed/no-op switches.
	s.releaseInputs()
	targets := s.displayTargets(c)
	if len(targets) < 2 {
		_ = s.ctrl.send(message{Type: "screen-status", Text: "被控端当前只有一块可用屏幕"}, nil)
		return
	}
	index := -1
	for i, target := range targets {
		if (c.display != "" && c.display == target.display) || (c.display == "" && target.area == c.area) {
			index = i
			break
		}
	}
	next := targets[(index+1)%len(targets)]
	if !validDesktop(next.area) {
		_ = s.ctrl.send(message{Type: "screen-status", Text: "目标屏幕不可用，保留当前屏幕"}, nil)
		return
	}
	if s.autoBitrate {
		next.bitrate = automaticBitrate(next)
	} else {
		next.bitrate = int(s.bitrate.Load())
	}
	next.hideCursor = s.cursorHidden.Load()
	// Probe while the old stream is still running. Failed probes do not change
	// the capture rectangle, input origin or video generation.
	probe, stop := context.WithTimeout(s.ctx, 12*time.Second)
	profile, err := selectProfile(probe, next)
	stop()
	if err != nil || s.ctx.Err() != nil {
		if s.ctx.Err() == nil {
			log.Printf("[lan][F6] 切屏失败：%v", err)
			_ = s.ctrl.send(message{Type: "screen-status", Text: "切屏失败，保留当前屏幕：" + err.Error()}, nil)
		}
		return
	}
	if fresh, ok := resolveDisplay(next, s.displayTargets(c)); !ok || !sameCaptureDisplay(next, fresh) {
		_ = s.ctrl.send(message{Type: "screen-status", Text: "显示配置仍在变化，请稍后重试切屏"}, nil)
		return
	}
	generation := s.commitDisplay(next, profile)
	log.Printf("[lan][F6] 屏幕 %d/%d | %dx%d @ (%d,%d) | %s/%s | generation=%d", (index+1)%len(targets)+1, len(targets), next.area.width, next.area.height, next.area.left, next.area.top, profile.capture, profile.name, generation)
}

func validScreenDescriptor(m message) bool {
	return m.Type == "screen" && m.Epoch > 0 && m.ScreenGeneration > 0 &&
		validDesktop(desktop{width: m.Width, height: m.Height}) && m.FPS > 0 && m.FPS <= maxFPS &&
		(m.Codec == codecH264 || m.Codec == codecJPEG || m.Codec == codecJPEGDelta) &&
		m.MonitorCount > 0 && m.Monitor >= 0 && m.Monitor < m.MonitorCount
}

func (s *controllerSession) currentScreen() message {
	s.screenMu.Lock()
	defer s.screenMu.Unlock()
	if s.screen.Width == 0 {
		return s.welcome
	}
	return s.screen
}

func (s *controllerSession) registerScreen(m message) {
	s.screenMu.Lock()
	defer s.screenMu.Unlock()
	if s.epochScreens == nil {
		s.epochScreens = make(map[uint64]message)
	}
	if m.Epoch > s.screenLatest {
		s.screenLatest = m.Epoch
	}
	if m.Epoch < s.screenLatest && s.screenLatest-m.Epoch >= 8 {
		return
	}
	s.epochScreens[m.Epoch] = m
	for epoch := range s.epochScreens {
		if epoch < s.screenLatest && s.screenLatest-epoch >= 8 {
			delete(s.epochScreens, epoch)
		}
	}
	if s.screenChanged != nil {
		close(s.screenChanged)
	}
	s.screenChanged = make(chan struct{})
}

func (s *controllerSession) screenForEpoch(epoch uint64) (message, bool) {
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		s.screenMu.Lock()
		m, ok := s.epochScreens[epoch]
		stale := s.screen.Epoch > epoch || (!ok && s.screenLatest > epoch)
		if s.screenChanged == nil {
			s.screenChanged = make(chan struct{})
		}
		changed := s.screenChanged
		s.screenMu.Unlock()
		if ok || stale {
			return m, ok && !stale
		}
		select {
		case <-s.ctx.Done():
			return message{}, false
		case <-changed:
		case <-timer.C:
			s.requestKey()
			return message{}, false
		}
	}
}

func (s *controllerSession) setMonitorNotice(text string) {
	s.noticeMu.Lock()
	s.monitorNotice = text
	s.noticeMu.Unlock()
}

func (s *controllerSession) nextScreen() {
	if !hasFeature(s.welcome.Features, screenSwitchFeature) {
		s.setMonitorNotice("F6 切屏需要更新 LAN 被控端")
		return
	}
	if s.monitorPending.Load() {
		return
	} // No probe/restart storm on key repeats.
	s.pendingFrom.Store(s.currentScreen().ScreenGeneration)
	if !s.monitorPending.CompareAndSwap(false, true) {
		return
	}
	s.setMonitorNotice("正在切换屏幕…")
	s.monitorStarted.Store(time.Now().UnixNano())
	s.enqueue(message{Type: "monitor-next"})
}

func (s *controllerSession) enqueueInput(e inputEvent) {
	if e.Kind != "reset" && s.monitorPending.Load() {
		return
	}
	s.enqueue(message{Type: "input", Input: e, ScreenGeneration: s.currentScreen().ScreenGeneration})
}

func (s *controllerSession) screenPictureReady(generation uint64) {
	if generation == 0 {
		return
	}
	// Old decoder callbacks have finished before resizeDesktop. Acknowledge
	// only an actually published picture, not metadata or decoder startup.
	if s.readyScreen.Load() >= generation {
		return
	}
	if old := s.readyScreen.Swap(generation); old >= generation {
		return
	}
	if s.monitorPending.Load() && generation > s.pendingFrom.Load() {
		s.monitorPending.Store(false)
		s.setMonitorNotice("")
	}
	s.enqueue(message{Type: "screen-ready", ScreenGeneration: generation})
}

type desktopResize struct {
	width, height int
	generation    uint64
	done          chan struct{}
}

func (v *viewer) resizeDesktop(ctx context.Context, w, h int, generation uint64) bool {
	r := &desktopResize{width: w, height: h, generation: generation, done: make(chan struct{})}
	v.mu.Lock()
	if v.closed {
		v.mu.Unlock()
		return false
	}
	v.pendingDesktop = r
	hwnd := v.hwnd
	v.mu.Unlock()
	if hwnd == 0 {
		v.applyPendingDesktop()
	} else {
		if ok, _, _ := ucall("PostMessageW", hwnd, 0x800B, 0, 0); ok == 0 {
			return false
		}
	}
	select {
	case <-r.done:
		return true
	case <-ctx.Done():
		return false
	}
}

// UI-thread only when the viewer is open: COM renderers must be disposed on
// their creating thread, not from the network/decoder worker.
func (v *viewer) applyPendingDesktop() {
	v.mu.Lock()
	r := v.pendingDesktop
	if r == nil {
		v.mu.Unlock()
		return
	}
	v.pendingDesktop = nil
	old := v.frames.latest
	v.frames.latest = nil
	if old != nil && old.readers == 0 {
		v.frames.recycleFrame(old)
	}
	v.width, v.height, v.screenGeneration = r.width, r.height, r.generation
	v.pixels, v.refine = nil, nil
	v.repairs.reset(r.width, r.height)
	v.baseRevision++
	v.mu.Unlock()
	v.pipeline.resetCadence()
	if v.gpu != nil {
		v.gpu.close()
		v.gpu = nil
	}
	if v.renderer != nil {
		v.renderer.close()
		v.renderer = nil
	}
	v.gpuTried, v.renderSeeded = false, false
	v.sharp = sharpDisplay{}
	v.cpuOverlay = displayOverlay{}
	v.compatPixels = nil
	// Renderer initialization on the next frame rediscovers compatibility;
	// do not accidentally force the new H.264 screen down the BGRA path.
	v.compatibility.Store(false)
	v.remoteCursorKnown.Store(false)
	if v.hwnd != 0 {
		ucall("InvalidateRect", v.hwnd, 0, 1)
	}
	close(r.done)
}
