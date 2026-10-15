//go:build windows && amd64

package lan

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"
)

// Retain 4.4 options for a reproducible A/B comparison.
func legacyDecoderArgs(hardware bool) []string {
	a := []string{"-hide_banner", "-loglevel", "warning", "-nostdin", "-probesize", "32", "-analyzeduration", "1", "-fpsprobesize", "0", "-avioflags", "direct", "-fflags", "+discardcorrupt", "-flags", "low_delay", "-threads", "1"}
	if hardware {
		a = append(a, "-hwaccel", "d3d11va", "-hwaccel_output_format", "d3d11")
	}
	a = append(a, "-f", "h264", "-i", "pipe:0", "-an")
	if hardware {
		a = append(a, "-vf", "hwdownload,format=nv12,format=bgra")
	}
	return append(a, "-pix_fmt", "bgra", "-fps_mode", "passthrough", "-f", "rawvideo", "-avioflags", "direct", "-flush_packets", "1", "pipe:1")
}

// No desktop capture or input injection. Keep stdin open to expose retained
// frames. This measures pipe-to-BGRA, not capture/network/render/scan-out.
func TestDecoderLatencyProbe(t *testing.T) {
	if os.Getenv("MYDESK_LAN_LATENCY_TEST") != "1" {
		t.Skip("latency probe opt-in")
	}
	ff, _ := filepath.Abs(filepath.Join("..", "..", "runtime", "ffmpeg.exe"))
	ctx, cancel := context.WithTimeout(context.Background(), 55*time.Second)
	defer cancel()
	for _, cfg := range []struct{ w, h, fps int }{{320, 180, 60}, {1920, 1080, 60}, {1920, 1080, 120}} {
		cmd := helperCommand(ctx, ff, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", fmt.Sprintf("color=c=gray:s=%dx%d:r=%d", cfg.w, cfg.h, cfg.fps), "-frames:v", "150", "-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency", "-bf", "0", "-g", "60", "-bsf:v", "h264_metadata=aud=insert", "-f", "h264", "pipe:1")
		stream, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		var units [][]byte
		if err := readAccessUnits(bytes.NewReader(stream), func(b []byte, _ bool) error { units = append(units, b); return nil }); err != nil {
			t.Fatal(err)
		}
		for _, hardware := range []bool{false, true} {
			for _, legacy := range []bool{true, false} {
				t.Run(fmt.Sprintf("%dx%d-%dFPS-hw%t-legacy%t", cfg.w, cfg.h, cfg.fps, hardware, legacy), func(t *testing.T) {
					d := &videoDecoder{path: ff, width: cfg.w, height: cfg.h, hardware: hardware, fps: cfg.fps}
					args := decoderArgs(hardware)
					if legacy {
						args = legacyDecoderArgs(hardware)
					}
					var mu sync.Mutex
					times := make([]time.Time, len(units))
					var lags []time.Duration
					decoded := 0
					if err := d.startWithArgs(ctx, args, func(_ []byte) {
						mu.Lock()
						defer mu.Unlock()
						if decoded < len(times) && !times[decoded].IsZero() && decoded >= 30 {
							lags = append(lags, time.Since(times[decoded]))
						}
						decoded++
					}); err != nil {
						t.Fatal(err)
					}
					defer d.close()
					start := time.Now()
					for i, unit := range units {
						time.Sleep(max(time.Duration(0), time.Until(start.Add(time.Duration(i)*time.Second/time.Duration(cfg.fps)))))
						mu.Lock()
						times[i] = time.Now()
						mu.Unlock()
						if legacy {
							_, err = d.stdin.Write(unit)
						} else {
							err = d.writeFrame(unit)
						}
						if err != nil {
							t.Fatal(err, d.stderr.String())
						}
					}
					time.Sleep(150 * time.Millisecond)
					mu.Lock()
					defer mu.Unlock()
					if len(lags) == 0 {
						t.Fatal("no measured frames", decoded, d.stderr.String())
					}
					sort.Slice(lags, func(i, j int) bool { return lags[i] < lags[j] })
					t.Logf("decoded=%d/%d steady latency p50=%v p95=%v p99=%v", decoded, len(units), lags[len(lags)/2], lags[len(lags)*95/100], lags[len(lags)*99/100])
					if !legacy && decoded != len(units) {
						t.Fatalf("complete frames retained with open input: %d/%d", decoded, len(units))
					}
				})
			}
		}
	}
}
