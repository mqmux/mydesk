//go:build windows && amd64

package lan

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"
)

// Identical pre-encoded large-motion pictures, paced with input kept open.
// Measures compressed-picture admission -> CPU NV12, not desktop/network/UI.
func TestNativeMFDecoderLatencyAB(t *testing.T) {
	if os.Getenv("MYDESK_LAN_MF_LATENCY_TEST") != "1" {
		t.Skip("native/FFmpeg decoder timing opt-in")
	}
	ff, _ := filepath.Abs(filepath.Join("..", "..", "runtime", "ffmpeg.exe"))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := helperCommand(ctx, ff, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=1920x1080:rate=60", "-frames:v", "90", "-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency", "-crf", "8", "-bf", "0", "-g", "60", "-bsf:v", "h264_metadata=aud=insert", "-f", "h264", "pipe:1")
	stream, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var units [][]byte
	if err := readAccessUnits(bytes.NewReader(stream), func(b []byte, _ bool) error { units = append(units, bytes.Clone(b)); return nil }); err != nil {
		t.Fatal(err)
	}
	for _, setting := range []struct {
		backend string
		hw      bool
	}{{"mf", false}, {"ffmpeg", false}, {"mf", true}, {"ffmpeg", true}} {
		name := setting.backend + "-software"
		if setting.hw {
			name = setting.backend + "-d3d11"
		}
		t.Run(name, func(t *testing.T) {
			var mu sync.Mutex
			times := make([]time.Time, len(units))
			var delays []time.Duration
			count := 0
			completed := make(chan struct{}, 1)
			started := time.Now()
			var first time.Duration
			buffer := make([]byte, pixelNV12.size(1920, 1080))
			d, err := startPictureDecoder(ctx, setting.backend, ff, setting.hw, pixelNV12, 1920, 1080, 60, func() []byte { return buffer }, func([]byte) {
				mu.Lock()
				defer mu.Unlock()
				if count == 0 {
					first = time.Since(started)
				}
				if count >= 20 && count < len(times) {
					delays = append(delays, time.Since(times[count]))
				}
				count++
				if count == len(units) {
					completed <- struct{}{}
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			defer d.close()
			feedStart := time.Now()
			for i, unit := range units {
				time.Sleep(max(time.Duration(0), time.Until(feedStart.Add(time.Duration(i)*time.Second/60))))
				mu.Lock()
				times[i] = time.Now()
				mu.Unlock()
				if err := d.writeFrame(unit); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case <-completed:
			case <-time.After(3 * time.Second):
				t.Fatalf("%d/%d pictures before close", count, len(units))
			}
			mu.Lock()
			defer mu.Unlock()
			sort.Slice(delays, func(i, j int) bool { return delays[i] < delays[j] })
			t.Logf("first including activation=%.2fms; compressed admission -> CPU NV12 median=%.2fms p95=%.2fms; pictures=%d/%d", float64(first)/float64(time.Millisecond), float64(delays[len(delays)/2])/float64(time.Millisecond), float64(delays[(len(delays)-1)*95/100])/float64(time.Millisecond), count, len(units))
		})
	}
}
