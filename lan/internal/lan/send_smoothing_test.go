package lan

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestQueueGracePreservesFIFOAndBoundsPressure(t *testing.T) {
	for _, grace := range []time.Duration{0, videoQueueGrace} {
		frames := make(chan videoFrame, 2)
		metrics := &hostSendMetrics{}
		for i := uint64(1); i <= 2; i++ {
			if err := queueVideoFrame(context.Background(), frames, videoFrame{id: i, epoch: 1}, grace, metrics); err != nil {
				t.Fatal(err)
			}
		}
		start := time.Now()
		err := queueVideoFrame(context.Background(), frames, videoFrame{id: 3, epoch: 1}, grace, metrics)
		if !errors.Is(err, errVideoQueueFull) || len(frames) != 2 || time.Since(start) > 100*time.Millisecond {
			t.Fatal("persistent pressure changed queue capacity or reference safety", err, time.Since(start))
		}
		if (<-frames).id != 1 || (<-frames).id != 2 || metrics.counters[sendQueueExpired] != 1 {
			t.Fatal("FIFO/reference order changed")
		}
	}
	frames := make(chan videoFrame, 2)
	frames <- videoFrame{id: 1}
	frames <- videoFrame{id: 2}
	metrics := &hostSendMetrics{}
	freed := make(chan struct{})
	go func() { time.Sleep(time.Millisecond); <-frames; close(freed) }()
	if err := queueVideoFrame(context.Background(), frames, videoFrame{id: 3}, videoQueueGrace, metrics); err != nil {
		t.Fatal("short stall forced a reference reset", err)
	}
	<-freed
	if (<-frames).id != 2 || (<-frames).id != 3 || metrics.counters[sendQueueRecovered] != 1 {
		t.Fatal("short-stall recovery lost/reordered a reference")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := queueVideoFrame(ctx, frames, videoFrame{id: 4}, videoQueueGrace, nil); !errors.Is(err, context.Canceled) || len(frames) != 0 {
		t.Fatal("canceled encoder enqueued another picture")
	}
}

func TestSendMetricsBoundedAndReset(t *testing.T) {
	var disabled *hostSendMetrics
	disabled.add(sendQueueFull)
	disabled.observe(0, time.Millisecond)
	if disabled.summary() != "" {
		t.Fatal("disabled logging")
	}
	m := &hostSendMetrics{}
	m.add(sendQueueFull)
	m.add(sendQueueRecovered)
	for i := 1; i <= 100; i++ {
		m.observe(2, time.Duration(i)*time.Millisecond)
	}
	first := m.summary()
	if !strings.Contains(first, "queue-full/recovered/expired=1/1/0") || !strings.Contains(first, "p50/p95/p99=50.00/95.00/99.00ms") {
		t.Fatal(first)
	}
	for i := 0; i < 10000; i++ {
		m.observe(0, time.Millisecond)
	}
	if got := m.summary(); !strings.Contains(got, "n=512") || !strings.Contains(got, "queue-full/recovered/expired=0/0/0") {
		t.Fatal(got)
	}
}

func TestPresentCadenceExcludesMinimizeAndScreenReset(t *testing.T) {
	p := &pipelineMetrics{}
	start := time.Now()
	p.recordPresent(start)
	p.recordPresent(start.Add(8 * time.Millisecond))
	p.recordPresent(start.Add(16 * time.Millisecond))
	p.resetCadence()
	p.recordPresent(start.Add(time.Hour))
	p.recordPresent(start.Add(time.Hour + 8*time.Millisecond))
	if got := p.summary(); !strings.Contains(got, "present-interval n=3 p50/p95/p99=8.00/8.00/8.00ms") {
		t.Fatal(got)
	}
}
