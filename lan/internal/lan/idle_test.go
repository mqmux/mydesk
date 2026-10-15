package lan

import (
	"testing"
	"time"
)

func TestUDPIdleActivityAndVideoBlackhole(t *testing.T) {
	now := time.Unix(100, 0)
	a := udpActivity{lastPacket: now}
	a.picture(10, now)
	for i := 1; i <= 120; i++ {
		at := now.Add(time.Duration(i) * time.Second)
		a.keepalive(10, at)
		if a.stalled(at) {
			t.Fatal("unchanged desktop mistaken for UDP failure", i)
		}
	}
	now = now.Add(120 * time.Second)
	if !a.stalled(now.Add(2100 * time.Millisecond)) {
		t.Fatal("missing keepalives did not time out")
	}
	// Heartbeats arrive, but newly advertised video does not. Repeated
	// advertisements must not move the start of this timeout forward.
	a.keepalive(11, now)
	a.keepalive(12, now.Add(time.Second))
	a.keepalive(13, now.Add(2100*time.Millisecond))
	if !a.stalled(now.Add(2100 * time.Millisecond)) {
		t.Fatal("keepalives masked selectively lost video")
	}
	a.picture(13, now.Add(2200*time.Millisecond))
	if a.stalled(now.Add(2200 * time.Millisecond)) {
		t.Fatal("actual video progress did not clear timeout")
	}
	// A delayed old heartbeat must not reintroduce an outstanding picture.
	a.keepalive(11, now.Add(2300*time.Millisecond))
	if a.stalled(now.Add(2300 * time.Millisecond)) {
		t.Fatal("reordered old heartbeat caused a false stall")
	}
}

func TestUDPIdleReadInterval(t *testing.T) {
	if udpReadInterval(false, false) != 10*time.Millisecond || udpReadInterval(false, true) != 250*time.Millisecond || udpReadInterval(true, false) != 10*time.Millisecond || udpReadInterval(true, true) != 10*time.Millisecond {
		t.Fatal("idle polling or fragment repair interval changed unexpectedly")
	}
	p, err := makePacketCipher(make([]byte, 36))
	if err != nil {
		t.Fatal(err)
	}
	b := p.seal(fragment{kind: packetKeepalive, frame: 42})
	f, err := p.open(b)
	if err != nil || len(b) != 56 || f.kind != packetKeepalive || f.frame != 42 || len(f.payload) != 0 {
		t.Fatal("keepalive is not a tiny authenticated non-video packet", err)
	}
	b[len(b)-1] ^= 1
	if _, err := p.open(b); err == nil {
		t.Fatal("unauthenticated keepalive accepted")
	}
}
