package lan

import (
	"testing"
	"time"
)

func TestDecoderTimingDetectsInternalBacklog(t *testing.T) {
	now := time.Now()
	var d decoderTiming
	d.init(120, now)
	for i := 0; i < 10; i++ {
		d.submit(now.Add(time.Duration(i) * 8 * time.Millisecond))
	}
	if d.slow(now.Add(200 * time.Millisecond)) {
		t.Fatal("startup penalized")
	}
	if d.slow(now.Add(500 * time.Millisecond)) {
		t.Fatal("one transient sample penalized")
	}
	if !d.slow(now.Add(600 * time.Millisecond)) {
		t.Fatal("internal backlog missed")
	}
}
func TestDecoderTimingLateFramesAndRecovery(t *testing.T) {
	now := time.Now()
	var d decoderTiming
	d.init(120, now.Add(-time.Second))
	for i := 0; i < 6; i++ {
		at := now.Add(time.Duration(i) * 20 * time.Millisecond)
		d.submit(at)
		d.complete(at.Add(50 * time.Millisecond))
	}
	if !d.slow(now.Add(150 * time.Millisecond)) {
		t.Fatal("sustained latency missed")
	}
	d.submit(now.Add(160 * time.Millisecond))
	d.complete(now.Add(165 * time.Millisecond))
	if d.slow(now.Add(165 * time.Millisecond)) {
		t.Fatal("fast decoder penalized")
	}
}

func TestDecoderTimingStartupBurstDoesNotLatchFallback(t *testing.T) {
	now := time.Now()
	var d decoderTiming
	d.init(120, now)
	for i := 0; i < 10; i++ {
		d.submit(now.Add(time.Duration(i) * 8 * time.Millisecond))
	}
	for i := 0; i < 10; i++ {
		d.complete(now.Add(550 * time.Millisecond))
	}
	if d.slow(now.Add(560 * time.Millisecond)) {
		t.Fatal("drained startup pictures forced CPU fallback")
	}
	for i := 0; i < 6; i++ {
		at := now.Add((600 + time.Duration(i)*20) * time.Millisecond)
		d.submit(at)
		d.complete(at.Add(50 * time.Millisecond))
	}
	if !d.slow(now.Add(750 * time.Millisecond)) {
		t.Fatal("genuinely late steady-state frames ignored")
	}
	if d.slow(now.Add(time.Second)) {
		t.Fatal("old lateness stayed latched after idle")
	}
}

func TestDecoderTimingTransientBacklogRecovers(t *testing.T) {
	now := time.Now()
	var d decoderTiming
	d.init(120, now.Add(-time.Second))
	for i := 0; i < 8; i++ {
		d.submit(now)
	}
	if d.slow(now.Add(90 * time.Millisecond)) {
		t.Fatal("transient backlog forced fallback")
	}
	for i := 0; i < 8; i++ {
		d.complete(now.Add(100 * time.Millisecond))
	}
	if d.slow(now.Add(105 * time.Millisecond)) {
		t.Fatal("burst of late completions forced fallback")
	}
	// Complete one fast frame to show recovery rather than persistently late decode.
	d.submit(now.Add(110 * time.Millisecond))
	d.complete(now.Add(115 * time.Millisecond))
	if d.slow(now.Add(200 * time.Millisecond)) {
		t.Fatal("recovered backlog forced fallback")
	}
}

func TestDecoderPressureRequiresRecentSustainedEvents(t *testing.T) {
	now := time.Now()
	var p decoderPressureWindow
	p.init(now)
	for i := 0; i < 100; i++ {
		p.record(now.Add(200 * time.Millisecond))
	}
	if p.slow(now.Add(600 * time.Millisecond)) {
		t.Fatal("startup drops penalized")
	}
	for i := 0; i < 100; i++ {
		p.record(now.Add(600 * time.Millisecond))
	}
	if p.slow(now.Add(700 * time.Millisecond)) {
		t.Fatal("single burst penalized")
	}
	for i := 1; i < 4; i++ {
		p.record(now.Add((600 + time.Duration(i)*50) * time.Millisecond))
	}
	if !p.slow(now.Add(750 * time.Millisecond)) {
		t.Fatal("sustained pressure missed")
	}
	if p.slow(now.Add(2 * time.Second)) {
		t.Fatal("historical drops stayed latched")
	}
	p.init(now.Add(3 * time.Second))
	if p.slow(now.Add(3600 * time.Millisecond)) {
		t.Fatal("new decoder inherited pressure")
	}
}
