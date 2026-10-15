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

// Identical pre-encoded large-motion frames, input kept open after pacing.
// Measures local compressed-input -> raw pipe, not capture/network/scan-out.
func TestNV12DecoderLatencyAB(t *testing.T) {
	if os.Getenv("MYDESK_LAN_LATENCY_TEST") != "1" {
		t.Skip("decoder latency opt-in")
	}
	ff, _ := filepath.Abs(filepath.Join("..", "..", "runtime", "ffmpeg.exe"))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := helperCommand(ctx, ff, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=1920x1080:rate=120", "-frames:v", "150", "-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency", "-crf", "8", "-threads", "4", "-bf", "0", "-g", "120", "-bsf:v", "h264_metadata=aud=insert", "-f", "h264", "pipe:1")
	stream, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var units [][]byte
	if err := readAccessUnits(bytes.NewReader(stream), func(b []byte, _ bool) error { units = append(units, b); return nil }); err != nil {
		t.Fatal(err)
	}
	for _, hardware := range []bool{false, true} {
		for _, format := range []frameFormat{pixelBGRA, pixelNV12} {
			name := format.String() + "-software"
			if hardware {
				name = format.String() + "-hardware"
			}
			t.Run(name, func(t *testing.T) {
				d := &videoDecoder{path: ff, width: 1920, height: 1080, format: format, hardware: hardware, fps: 120}
				// The real viewer recycles raw buffers. Do not attribute one new
				// full-frame allocation per picture to either decoder route.
				scratch := make([]byte, format.size(1920, 1080))
				d.buffer = func() []byte { return scratch }
				var mu sync.Mutex
				sent := make([]time.Time, len(units))
				var ages []time.Duration
				decoded := 0
				completed := make(chan struct{}, 1)
				if err := d.start(ctx, func(b []byte) {
					mu.Lock()
					defer mu.Unlock()
					if decoded < len(sent) && !sent[decoded].IsZero() && decoded >= 30 {
						ages = append(ages, time.Since(sent[decoded]))
					}
					decoded++
					if decoded == len(units) {
						completed <- struct{}{}
					}
				}); err != nil {
					t.Fatal(err)
				}
				defer d.close()
				start := time.Now()
				for i, unit := range units {
					time.Sleep(max(time.Duration(0), time.Until(start.Add(time.Duration(i)*time.Second/120))))
					mu.Lock()
					sent[i] = time.Now()
					mu.Unlock()
					if err := d.writeFrame(unit); err != nil {
						t.Fatal(err, d.stderr.String())
					}
				}
				select {
				case <-completed:
				case <-time.After(2 * time.Second):
					t.Fatal("decoder retained frames with open input", d.stderr.String())
				}
				mu.Lock()
				defer mu.Unlock()
				if len(ages) == 0 {
					t.Fatal("no timed frames")
				}
				sort.Slice(ages, func(i, j int) bool { return ages[i] < ages[j] })
				t.Logf("%d/%d frames, pipe bytes/frame=%d, steady age p50=%v p95=%v max=%v", decoded, len(units), format.size(1920, 1080), ages[len(ages)/2], ages[len(ages)*95/100], ages[len(ages)-1])
			})
		}
	}
}
