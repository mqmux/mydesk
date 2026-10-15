package lan

import (
	"strings"
	"testing"
	"time"
)

func TestPipelineTimingBoundedQuantilesAndReset(t *testing.T) {
	var p *pipelineMetrics
	p.observe(0, time.Second)
	if p.summary() != "" {
		t.Fatal("disabled metrics")
	}
	p = &pipelineMetrics{}
	for i := 1; i <= 100; i++ {
		p.observe(1, time.Duration(i)*time.Millisecond)
	}
	if got := p.summary(); !strings.Contains(got, "p50/p95/p99=50.00/95.00/99.00ms") {
		t.Fatal(got)
	}
	if p.summary() != "" {
		t.Fatal("metrics not reset")
	}
	for i := 0; i < 10000; i++ {
		p.observe(2, time.Millisecond)
	}
	if got := p.summary(); !strings.Contains(got, "n=512") {
		t.Fatal("unbounded metrics", got)
	}
}
