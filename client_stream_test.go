package main

import (
	"encoding/json"
	"image"
	"testing"
	"time"
	"unsafe"
)

func TestTCPJPEGQualityIsUniform(t *testing.T) {
	a := &agent{viewerActive: true, viewerKind: "controller", activeTransport: "ws", streamMode: "video", patchBoost: 8, tileBoost: 8, streamFPS: 48, txAvgBytes: 128 * 1024, txBPS: 6 * 1024 * 1024}
	full := image.Rect(0, 0, 1920, 1080)
	patch := image.Rect(0, 0, 64, 64)
	for _, mode := range []string{"video", "delta"} {
		a.streamMode = mode
		for _, quality := range []int{56, 80, 95, 100} {
			a.streamQuality = quality
			base := a.effectiveQuality()
			if base != quality || a.fullFrameQuality(base) != quality || a.patchQuality(base, full, patch) != quality || a.tilesQuality(base, full, []tilePatch{{W: 64, H: 64}}) != quality {
				t.Fatalf("quality differs across updates: mode=%s requested=%d base=%d", mode, quality, base)
			}
		}
	}
}

func TestMouseInputWindowsABI(t *testing.T) {
	var in input
	wantSize, wantOffset := uintptr(28), uintptr(4)
	if unsafe.Sizeof(uintptr(0)) == 8 {
		wantSize, wantOffset = 40, 8
	}
	if unsafe.Sizeof(in) != wantSize || unsafe.Offsetof(in.MI) != wantOffset {
		t.Fatalf("INPUT size=%d offset=%d; want %d/%d", unsafe.Sizeof(in), unsafe.Offsetof(in.MI), wantSize, wantOffset)
	}
}

func TestNegotiatedCaptureBudget(t *testing.T) {
	a := &agent{viewerActive: true, viewerKind: "controller", streamMode: "video", streamFPS: 60}
	if a.targetBudgetBPS() != 500*1024 {
		t.Fatal("old-server fallback changed")
	}
	var response commandResponse
	if err := json.Unmarshal([]byte(`{"suggestedTargetKBps":32768,"suggestedFps":60}`), &response); err != nil {
		t.Fatal(err)
	}
	a.streamTargetKBps.Store(int64(response.SuggestedTargetKBps))
	if a.targetBudgetBPS() != 32768*1024 {
		t.Fatal("LAN budget not used")
	}
	interval, active := a.streamInterval()
	if !active || interval != time.Second/60 {
		t.Fatal("60 fps profile not respected")
	}
	a.streamFPS = 12
	interval, _ = a.streamInterval()
	if interval != time.Second/12 {
		t.Fatal("LAN backpressure floor overridden")
	}
	a.streamTargetKBps.Store(500)
	interval, _ = a.streamInterval()
	if interval != time.Second/24 {
		t.Fatal("cloud minimum-FPS policy changed")
	}
	a.viewerActive = false
	if _, active := a.streamInterval(); active {
		t.Fatal("capture active without viewer")
	}
}

func TestCaptureCadenceDoesNotDrift(t *testing.T) {
	start := time.Unix(100, 0)
	var next time.Time
	count := 0
	// The existing 10 ms ticker must not round a 16.67 ms period up to 20 ms
	// on every frame (50 fps). Preserve the fractional phase for 60 fps.
	for elapsed := time.Duration(0); elapsed < time.Second; elapsed += 10 * time.Millisecond {
		now := start.Add(elapsed)
		if next.IsZero() || !now.Before(next) {
			count++
			next = nextCaptureDeadline(next, now, time.Second/60)
		}
	}
	if count != 60 {
		t.Fatalf("cadence produced %d frames, want 60", count)
	}
	now := start.Add(500 * time.Millisecond)
	next = nextCaptureDeadline(start, now, time.Second/60)
	if !next.After(now) || next.Sub(now) > time.Second/60 {
		t.Fatal("missed deadline creates backlog")
	}
}

func TestWebSocketTransportSelection(t *testing.T) {
	for _, input := range []string{"ws", " websocket ", "WS"} {
		if normalizeTransport(input) != "ws" {
			t.Fatalf("%q not recognized", input)
		}
	}
	if normalizeTransport("http") != "http" || normalizeTransport("auto") != "auto" {
		t.Fatal("existing transports changed")
	}
}
