//go:build windows && amd64

package lan

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"
)

// Actual FFmpeg A/B, with identical raw input and encoder options. Measures
// raw-picture submission to encoded-picture emission, not user-input latency,
// desktop capture, network, rendering or scan-out. Stdin remains open while the
// last-picture count is sampled, so EOF cannot hide next-AUD retention.
func TestEncoderPipeLatencyProbe(t *testing.T) {
	if os.Getenv("MYDESK_LAN_LATENCY_TEST") != "1" {
		t.Skip("encoder latency probe opt-in")
	}
	ff, _ := filepath.Abs(filepath.Join("..", "..", "runtime", "ffmpeg.exe"))
	for _, fps := range []int{60, 120} {
		for _, packetized := range []bool{false, true} {
			t.Run(fmt.Sprintf("%dFPS-packetized%t", fps, packetized), func(t *testing.T) {
				c := videoConfig{fps: fps, bitrate: 80000, h264Quality: 8, area: desktop{width: 320, height: 180}}
				tail := profileArgs(c, "libx264", "test")
				if packetized {
					tail = packetProfileArgs(c, "libx264", "test")
				}
				start := 0
				for tail[start] != "-an" {
					start++
				}
				args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-f", "rawvideo", "-pix_fmt", "bgra", "-video_size", "320x180", "-framerate", fmt.Sprint(fps), "-i", "pipe:0"}
				args = append(args, tail[start:]...)
				ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
				defer cancel()
				cmd := helperCommand(ctx, ff, args...)
				in, err := cmd.StdinPipe()
				if err != nil {
					t.Fatal(err)
				}
				out, err := cmd.StdoutPipe()
				if err != nil {
					t.Fatal(err)
				}
				var stderr tailBuffer
				cmd.Stderr = &stderr
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				var mu sync.Mutex
				const pictures = 90
				times := make([]time.Time, pictures)
				var lags []time.Duration
				count := 0
				done := make(chan error, 1)
				read := readAccessUnits
				if packetized {
					read = readEncoderPackets
				}
				go func() {
					done <- read(out, func(_ []byte, _ bool) error {
						mu.Lock()
						defer mu.Unlock()
						if count >= 25 && count < pictures && !times[count].IsZero() {
							lags = append(lags, time.Since(times[count]))
						}
						count++
						return nil
					})
				}()
				defer func() {
					cancel()
					in.Close()
					_ = cmd.Process.Kill()
					<-done
					_ = cmd.Wait()
				}()
				pixels := make([]byte, 320*180*4)
				for i := 0; i < len(pixels); i += 4 {
					pixels[i], pixels[i+1], pixels[i+2], pixels[i+3] = 80, 100, 120, 255
				}
				began := time.Now()
				for i := range times {
					time.Sleep(max(time.Duration(0), time.Until(began.Add(time.Duration(i)*time.Second/time.Duration(fps)))))
					mu.Lock()
					times[i] = time.Now()
					mu.Unlock()
					pixels[0] = byte(i)
					if err := writeAll(in, pixels); err != nil {
						t.Fatal(err, stderr.String())
					}
				}
				time.Sleep(150 * time.Millisecond)
				mu.Lock()
				defer mu.Unlock()
				if len(lags) == 0 {
					t.Fatal("no measured pictures", count, stderr.String())
				}
				sort.Slice(lags, func(i, j int) bool { return lags[i] < lags[j] })
				t.Logf("open input emitted %d/%d; raw submit -> encoded picture p50=%v p95=%v", count, pictures, lags[len(lags)/2], lags[len(lags)*95/100])
				if packetized && count != pictures {
					t.Fatal("complete encoded picture retained with open input")
				}
			})
		}
	}
}
