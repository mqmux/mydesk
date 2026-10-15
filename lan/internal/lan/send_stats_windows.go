//go:build windows && amd64

package lan

import (
	"log"
	"time"
)

func (s *hostSession) sendMetricsLoop() {
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-tick.C:
			log.Printf("[lan][send/local] %s", s.sendMetrics.summary())
		}
	}
}
