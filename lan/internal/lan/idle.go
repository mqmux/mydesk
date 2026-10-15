package lan

import "time"

const warmIdleFeature = "warm-idle-v1"
const minimizeFeature = "minimize-only-v1"

// Silence is normal for update-driven capture. Keep UDP reachability separate
// from pictures, but don't let small keepalives hide a blackholed video stream.
// Only the UDP receive goroutine owns this state.
type udpActivity struct {
	lastPacket  time.Time
	video, sent uint64
	waiting     time.Time
}

func (a *udpActivity) picture(id uint64, now time.Time) {
	a.lastPacket = now
	a.video = max(a.video, id)
	if a.video >= a.sent {
		a.waiting = time.Time{}
	}
}

func (a *udpActivity) keepalive(sent uint64, now time.Time) {
	a.lastPacket = now
	a.sent = max(a.sent, sent)
	if a.sent > a.video && a.waiting.IsZero() {
		a.waiting = now
	}
}

func (a *udpActivity) stalled(now time.Time) bool {
	return (!a.lastPacket.IsZero() && now.Sub(a.lastPacket) > 2*time.Second) ||
		(!a.waiting.IsZero() && now.Sub(a.waiting) > 2*time.Second)
}

func udpReadInterval(partial, minimized bool) time.Duration {
	if partial || !minimized {
		return 10 * time.Millisecond // Preserve foreground and fragment maintenance.
	}
	return 250 * time.Millisecond // Incoming datagrams wake ReadFromUDP immediately.
}
