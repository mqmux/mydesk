package lan

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

type decoderObservationKey struct{}
type pipelineMetrics struct {
	mu          sync.Mutex
	phase       [6]timingSamples
	lastPresent time.Time
}
type timingSamples struct {
	values [512]time.Duration
	count  int
	next   int
}

func (p *pipelineMetrics) observe(stage int, duration time.Duration) {
	if p == nil || stage < 0 || stage >= len(p.phase) || duration < 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	s := &p.phase[stage]
	s.values[s.next] = duration
	s.next = (s.next + 1) % len(s.values)
	s.count = min(s.count+1, len(s.values))
}
func (p *pipelineMetrics) summary() string {
	if p == nil {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	labels := [6]string{"input-queue", "receive-queue", "decode-submit/output", "GPU-upload/copy-submit", "ready-to-present-submit", "present-interval"}
	var parts []string
	for i := range p.phase {
		s := &p.phase[i]
		if s.count == 0 {
			continue
		}
		values := append([]time.Duration(nil), s.values[:s.count]...)
		sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
		q := func(percent int) float64 {
			return float64(values[min(len(values)-1, (len(values)*percent+99)/100-1)]) / float64(time.Millisecond)
		}
		parts = append(parts, fmt.Sprintf("%s n=%d p50/p95/p99=%.2f/%.2f/%.2fms", labels[i], s.count, q(50), q(95), q(99)))
		*s = timingSamples{}
	}
	return strings.Join(parts, " | ")
}

func (p *pipelineMetrics) recordPresent(now time.Time) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.lastPresent.IsZero() && !now.Before(p.lastPresent) {
		s := &p.phase[5]
		s.values[s.next] = now.Sub(p.lastPresent)
		s.next = (s.next + 1) % len(s.values)
		s.count = min(s.count+1, len(s.values))
	}
	p.lastPresent = now
}
func (p *pipelineMetrics) resetCadence() {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.lastPresent = time.Time{}
	p.mu.Unlock()
}
func decoderObserver(ctx context.Context) func(time.Duration) {
	observe, _ := ctx.Value(decoderObservationKey{}).(func(time.Duration))
	return observe
}
