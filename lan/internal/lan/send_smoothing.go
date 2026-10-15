package lan

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

const videoQueueGrace = 4 * time.Millisecond

var errVideoQueueFull = errors.New("video queue full; reference chain reset")

const (
	sendQueueFull = iota
	sendQueueRecovered
	sendQueueExpired
	sendEncoderStart
	sendRepair
	sendUDPError
	sendUDPRetry
	sendUDPRecovered
	sendUDPFallback
	sendAgedFrame
	sendNACKDropped
	sendCounterCount
)

// Local diagnostic windows only; no encoded contents or input/path data.
type hostSendMetrics struct {
	mu       sync.Mutex
	phase    [4]timingSamples
	counters [sendCounterCount]uint64
}

func (m *hostSendMetrics) add(counter int) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.counters[counter]++
	m.mu.Unlock()
}
func (m *hostSendMetrics) observe(phase int, duration time.Duration) {
	if m == nil || duration < 0 {
		return
	}
	m.mu.Lock()
	s := &m.phase[phase]
	s.values[s.next] = duration
	s.next = (s.next + 1) % len(s.values)
	s.count = min(s.count+1, len(s.values))
	m.mu.Unlock()
}
func (m *hostSendMetrics) summary() string {
	if m == nil {
		return ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	labels := [4]string{"encoder-output-gap", "queue-grace-wait", "send-queue-age", "frame-send-submit"}
	parts := []string{fmt.Sprintf("queue-full/recovered/expired=%d/%d/%d encoder-starts=%d repairs=%d udp-errors/retries/recovered/fallback=%d/%d/%d/%d aged=%d nack-dropped=%d", m.counters[sendQueueFull], m.counters[sendQueueRecovered], m.counters[sendQueueExpired], m.counters[sendEncoderStart], m.counters[sendRepair], m.counters[sendUDPError], m.counters[sendUDPRetry], m.counters[sendUDPRecovered], m.counters[sendUDPFallback], m.counters[sendAgedFrame], m.counters[sendNACKDropped])}
	for i := range m.phase {
		s := &m.phase[i]
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
	m.counters = [sendCounterCount]uint64{}
	return strings.Join(parts, " | ")
}

// Preserve FIFO/reference order and the original two-frame capacity. The
// healthy path is unchanged; only a full queue gets one short bounded wait.
func queueVideoFrame(ctx context.Context, frames chan<- videoFrame, f videoFrame, grace time.Duration, metrics *hostSendMetrics) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case frames <- f:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	metrics.add(sendQueueFull)
	if grace <= 0 {
		metrics.add(sendQueueExpired)
		return errVideoQueueFull
	}
	start := time.Now()
	defer func() { metrics.observe(1, time.Since(start)) }()
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case frames <- f:
		metrics.add(sendQueueRecovered)
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		metrics.add(sendQueueExpired)
		return errVideoQueueFull
	}
}
