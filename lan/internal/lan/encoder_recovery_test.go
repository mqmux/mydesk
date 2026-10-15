package lan

import (
	"testing"
	"time"
)

func TestEncoderRecoveryCoalescesStartupAndShutdown(t *testing.T) {
	var r encoderRecovery
	generation := r.begin(1)
	if r.repair(1, true) || r.needed(1, generation) {
		t.Fatal("startup key request restarted a fresh encoder")
	}
	r.sentKey(1)
	if !r.repair(1, true) || !r.needed(1, generation) || r.repair(1, true) {
		t.Fatal("repair was lost or not coalesced")
	}
	r.retire(1)
	if r.repair(1, true) || r.repair(1, false) {
		t.Fatal("shutdown request carried into next encoder")
	}
	generation = r.begin(2)
	if r.repair(1, false) || r.needed(2, generation) {
		t.Fatal("old epoch request restarted fresh encoder")
	}
	if !r.repair(2, false) || !r.needed(2, generation) {
		t.Fatal("unsent/aged first IDR could not recover")
	}
}

func TestEncoderRecoveryNeverLosesConfigurationChanges(t *testing.T) {
	var r encoderRecovery
	r.change()
	generation := r.begin(1)
	if r.needed(1, generation) {
		t.Fatal("already applied configuration restarted encoder")
	}
	r.change()
	if !r.needed(1, generation) {
		t.Fatal("startup configuration change was swallowed")
	}
	r.retire(1)
	r.change()
	next := r.begin(2)
	if next != generation+2 || r.needed(2, next) {
		t.Fatal("shutdown configuration generation was lost")
	}
	r.change()
	if !r.needed(2, next) {
		t.Fatal("stale notification could suppress new configuration")
	}
}

func TestEncoderBackoffOnlyRepeatedFailures(t *testing.T) {
	var b encoderBackoff
	for i, want := range []time.Duration{0, 50 * time.Millisecond, 100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond, 800 * time.Millisecond, time.Second, time.Second} {
		if got := b.failure(10 * time.Millisecond); got != want {
			t.Fatalf("failure %d: %v != %v", i, got, want)
		}
	}
	if b.failure(2*time.Second) != 0 {
		t.Fatal("healthy stream retained crash backoff")
	}
}
