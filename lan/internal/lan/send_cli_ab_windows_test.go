//go:build windows && amd64

package lan

import (
	"archive/zip"
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Diagnostic A/B only: own loopback child processes, synthetic video,
// headless receive and no clipboard/input/system monitor modifications.
func TestSendCLIBaselineHeadlessMFReadbackAB(t *testing.T) {
	if os.Getenv("MYDESK_LAN_CLI_AB_TEST") != "1" {
		t.Skip("saved-release CLI comparison opt-in")
	}
	module, _ := filepath.Abs(filepath.Join("..", ".."))
	old := t.TempDir()
	archive, err := zip.OpenReader(filepath.Join(module, "..", "mydesk-lan-v4.17-update.zip"))
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	for _, entry := range archive.File {
		if entry.Name != "lan_client.exe" && entry.Name != "lan_controller.exe" {
			continue
		}
		r, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		w, err := os.Create(filepath.Join(old, entry.Name))
		if err != nil {
			r.Close()
			t.Fatal(err)
		}
		_, copyErr := io.Copy(w, r)
		r.Close()
		closeErr := w.Close()
		if copyErr != nil || closeErr != nil {
			t.Fatal(copyErr, closeErr)
		}
	}
	pattern := regexp.MustCompile(`received=(\d+) decoded=(\d+) dropped=(\d+)`)
	for _, scenario := range []struct {
		name, folder string
		disabled     bool
	}{{"4.17", old, false}, {"4.18-off", filepath.Join(module, "dist"), true}, {"4.18-on", filepath.Join(module, "dist"), false}} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			reserved, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := reserved.Addr().(*net.TCPAddr).Port
			addr := reserved.Addr().String()
			reserved.Close()
			args := []string{"-interface", "127.0.0.1", "-port", strconv.Itoa(port), "-code", "loopback-ab-code", "-source", "test", "-fps", "60", "-ffmpeg", filepath.Join(module, "runtime", "ffmpeg.exe")}
			if scenario.disabled {
				args = append(args, "-queue-grace=false", "-udp-smoothing=false")
			}
			hostLog := &tailBuffer{}
			host := helperCommand(ctx, filepath.Join(scenario.folder, "lan_client.exe"), args...)
			host.Stdout, host.Stderr = hostLog, hostLog
			if err := host.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { cancel(); host.Wait() }() // Kill only this owned listener after master/session cleanup.
			until := time.Now().Add(10 * time.Second)
			ready := false
			for time.Now().Before(until) {
				c, err := net.DialTimeout("tcp4", addr, 100*time.Millisecond)
				if err == nil {
					c.Close()
					ready = true
					break
				}
				time.Sleep(50 * time.Millisecond)
			}
			if !ready {
				t.Fatal("owned listener failed to start")
			}
			masterLog := &tailBuffer{}
			master := helperCommand(ctx, filepath.Join(scenario.folder, "lan_controller.exe"), "-connect", addr, "-code", "loopback-ab-code", "-fps", "60", "-decoder", "mf", "-gpu-decode", "off", "-test-seconds", "4")
			master.Stdout, master.Stderr = masterLog, masterLog
			if err := master.Run(); err != nil {
				t.Fatal("owned headless master failed", err)
			}
			m := pattern.FindStringSubmatch(masterLog.String())
			if len(m) != 4 {
				t.Fatal("missing headless sample")
			}
			decoded, _ := strconv.Atoi(m[2])
			t.Logf("%s saved/current EXEs, 1080p60 MF CPU-NV12 readback, first 4s including activation: received=%s decoded=%s (%.2f/s) dropped=%s", scenario.name, m[1], m[2], float64(decoded)/4, m[3])
			if decoded == 0 {
				t.Fatal("no decoded baseline pictures")
			}
			// Let the master close its session and host stop its own encoder
			// before ending the owned listener process. No user process is killed.
			until = time.Now().Add(time.Second)
			for time.Now().Before(until) && !strings.Contains(hostLog.String(), "controller disconnected") {
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}
