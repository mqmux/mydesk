package lan

import (
	"sync"
	"time"
)

// Separate configuration changes from reference-chain repair. A new encoder
// already owes an IDR, so key requests during its startup/shutdown are redundant;
// configuration generations must never be lost in that same coalescing window.
type encoderRecovery struct {
	mu                           sync.Mutex
	generation, epoch            uint64
	active, ready, repairPending bool
}

func (r *encoderRecovery) change() {
	r.mu.Lock()
	r.generation++
	r.mu.Unlock()
}
func (r *encoderRecovery) begin(epoch uint64) uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.epoch, r.active, r.ready, r.repairPending = epoch, true, false, false
	return r.generation
}
func (r *encoderRecovery) sentKey(epoch uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active && r.epoch == epoch {
		r.ready = true
	}
}
func (r *encoderRecovery) repair(epoch uint64, external bool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.active || r.epoch != epoch || (external && !r.ready) || r.repairPending {
		return false
	}
	r.repairPending = true
	return true
}
func (r *encoderRecovery) needed(epoch, generation uint64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.active && r.epoch == epoch && (r.generation != generation || r.repairPending)
}
func (r *encoderRecovery) retire(epoch uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.epoch == epoch {
		r.active, r.ready, r.repairPending = false, false, false
	}
}

type encoderBackoff struct{ failures int }

func (b *encoderBackoff) failure(lifetime time.Duration) time.Duration {
	if lifetime >= time.Second {
		b.failures = 0
	}
	b.failures++
	if b.failures == 1 {
		return 0 // First failure/recovery gets an immediate fresh IDR.
	}
	return min(time.Second, 50*time.Millisecond<<min(5, b.failures-2))
}
