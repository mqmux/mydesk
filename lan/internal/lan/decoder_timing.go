package lan

import (
	"sync"
	"time"
)

// Count pictures across the process boundary, not just the Go receive queue:
// small compressed pictures can accumulate inside FFmpeg without filling it.
type decoderTiming struct {
	mu                              sync.Mutex
	started                         time.Time
	times                           [128]time.Time
	submitted, completed            uint64
	late                            int
	budget                          time.Duration
	lateAt, lateSince, backlogSince time.Time
	onComplete                      func(time.Duration)
}

func (d *decoderTiming) init(fps int, now time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.started = now
	d.times = [128]time.Time{}
	d.submitted, d.completed, d.late = 0, 0, 0
	d.lateAt, d.lateSince, d.backlogSince = time.Time{}, time.Time{}, time.Time{}
	d.budget = max(40*time.Millisecond, 2*time.Second/time.Duration(max(1, fps)))
}
func (d *decoderTiming) submit(now time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.times[d.submitted%128] = now
	d.submitted++
}
func (d *decoderTiming) complete(now time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.completed >= d.submitted {
		return
	}
	when := d.times[d.completed%128]
	if when.Before(d.started.Add(500 * time.Millisecond)) {
		// A drained startup burst is not evidence of slow steady-state decode.
		d.late = 0
		d.lateSince = time.Time{}
	} else if !when.IsZero() && now.Sub(when) > d.budget {
		if d.late == 0 {
			d.lateSince = now
		}
		d.late++
		d.lateAt = now
	} else {
		d.late = 0
		d.lateSince = time.Time{}
	}
	d.completed++
	if d.onComplete != nil && !when.IsZero() {
		d.onComplete(max(time.Duration(0), now.Sub(when)))
	}
}
func (d *decoderTiming) slow(now time.Time) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if now.Sub(d.started) < 500*time.Millisecond {
		return false
	}
	pending := d.submitted - d.completed
	if pending >= 128 || (d.late >= 6 && now.Sub(d.lateSince) >= 100*time.Millisecond && now.Sub(d.lateAt) < 200*time.Millisecond) {
		return true
	}
	if pending > 4 && now.Sub(d.times[d.completed%128]) > 80*time.Millisecond {
		if d.backlogSince.IsZero() {
			d.backlogSince = now
		}
		return now.Sub(d.backlogSince) >= 100*time.Millisecond
	}
	d.backlogSince = time.Time{}
	return false
}

// Cumulative drop counters are still used for statistics; hardware fallback
// instead needs recent, sustained pressure, not three drops over a whole call.
type decoderPressureWindow struct {
	mu    sync.Mutex
	until time.Time
	times [16]time.Time
	next  uint64
}

func (p *decoderPressureWindow) init(now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.until = now.Add(500 * time.Millisecond)
	p.times, p.next = [16]time.Time{}, 0
}
func (p *decoderPressureWindow) record(now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.until.IsZero() || now.Before(p.until) {
		return
	}
	if p.next > 0 && now.Sub(p.times[(p.next-1)%16]) < 50*time.Millisecond {
		return // A single burst counts once, not once per dropped picture.
	}
	p.times[p.next%16] = now
	p.next++
}
func (p *decoderPressureWindow) slow(now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	count := 0
	oldest := now
	for _, at := range p.times {
		if !at.IsZero() && !at.After(now) && now.Sub(at) <= 500*time.Millisecond {
			count++
			if at.Before(oldest) {
				oldest = at
			}
		}
	}
	return count >= 4 && now.Sub(oldest) >= 150*time.Millisecond
}
