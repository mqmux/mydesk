//go:build windows && amd64

package lan

import "time"

// Do not advertise warm-idle-v1 from the new controller: a 4.13 host uses it
// to enable irregular update-driven capture, independently of minimize state.
// A new host still accepts the old feature for an older controller's F7/UI.
func backgroundFeatureFor(features []string) string {
	if hasFeature(features, minimizeFeature) {
		return minimizeFeature
	}
	if hasFeature(features, warmIdleFeature) {
		return warmIdleFeature
	}
	return ""
}

func controllerSessionFeatures() []string {
	return []string{screenSwitchFeature, minimizeFeature, directoryFeature, repairResyncFeature}
}

func (s *hostSession) videoKeepaliveLoop() {
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-tick.C:
			if s.tcp.Load() {
				return // Video transport never automatically returns from TCP to UDP.
			}
			if !s.minimized.Load() {
				continue // Foreground continuous video provides its own UDP liveness.
			}
			s.mu.Lock()
			peer := s.peer
			s.mu.Unlock()
			if peer != nil {
				// Empty authenticated datagram: no decoder work and no frame ID consumed.
				// Frame advertises the last picture actually sent, not a queued picture.
				b := s.tx.seal(fragment{kind: packetKeepalive, frame: s.lastSentFrame.Load()})
				// sendFrames leaves a short socket-wide deadline behind. Refresh
				// it even when there have been no pictures for several seconds.
				s.writeAuxUDP(b, peer, true)
			}
		}
	}
}

func (s *hostSession) setViewerMinimized(minimized bool) {
	if s.minimized.Swap(minimized) == minimized {
		return
	}
	if !minimized {
		s.refineResumes.Add(1)
	}
	// Wake the repair sampler on restore; no encoder restart or key request.
	select {
	case s.refineWake <- struct{}{}:
	default:
	}
}

func (s *controllerSession) setViewerMinimized(minimized bool) {
	s.viewerMinimized.Store(minimized)
	s.sendViewerState()
}

func (s *controllerSession) sendViewerState() {
	if backgroundFeatureFor(s.welcome.Features) != "" {
		minimized := s.viewerMinimized.Load()
		s.enqueue(message{Type: "viewer-state", Minimized: &minimized})
	}
}

func (v *viewer) setMinimized(minimized bool) {
	if v.minimized.Swap(minimized) == minimized {
		return
	}
	v.pipeline.resetCadence()
	if v.onMinimized != nil {
		v.onMinimized(minimized)
	}
}
