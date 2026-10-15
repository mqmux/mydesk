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

// Built-release CLI smoke test: private loopback listener, synthetic JPEG,
// headless master, no real clipboard/input/capture/firewall changes.
func TestKeyMatchCLIHeadlessDiscoveryAndExplicitTarget(t *testing.T) {
	if os.Getenv("MYDESK_LAN_KEY_CLI_TEST") != "1" {
		t.Skip("built key-match CLI opt-in")
	}
	module, _ := filepath.Abs(filepath.Join("..", ".."))
	owned := t.TempDir()
	for _, name := range []string{"lan_client.exe", "lan_controller.exe"} {
		source, err := os.Open(filepath.Join(module, "dist", name))
		if err != nil {
			t.Fatal(err)
		}
		target, err := os.Create(filepath.Join(owned, name))
		if err != nil {
			source.Close()
			t.Fatal(err)
		}
		_, err = io.Copy(target, source)
		source.Close()
		closeErr := target.Close()
		if err != nil || closeErr != nil {
			t.Fatal(err, closeErr)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	reserved, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := reserved.Addr().String()
	port := strconv.Itoa(reserved.Addr().(*net.TCPAddr).Port)
	reserved.Close()
	hostLog := &tailBuffer{}
	client := helperCommand(ctx, filepath.Join(owned, "lan_client.exe"), "-interface", "127.0.0.1", "-port", port, "-code", "owned-cli-key", "-source", "test", "-encoder", "jpeg", "-fps", "15", "-refine=false")
	client.Stdout, client.Stderr = hostLog, hostLog
	if err := client.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); client.Wait() }() // Only this owned test listener.
	ready := false
	until := time.Now().Add(8 * time.Second)
	for time.Now().Before(until) {
		c, err := net.DialTimeout("tcp4", address, 100*time.Millisecond)
		if err == nil {
			c.Close()
			ready = true
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if !ready {
		t.Fatal("owned loopback listener not ready")
	}
	for _, scenario := range []string{"automatic", "explicit", "explicit-refused", "list-no-key"} {
		t.Run(scenario, func(t *testing.T) {
			args := []string{"-interface", "127.0.0.1", "-port", port, "-fps", "15", "-test-seconds", "1"}
			input := "owned-cli-key\n"
			switch scenario {
			case "explicit":
				args = append(args, "-connect", address, "-code", "owned-cli-key")
				input = ""
			case "explicit-refused":
				args = append(args, "-connect", "127.0.0.1:1", "-code", "owned-cli-key")
				input = ""
			case "list-no-key":
				args = append(args, "-list")
				input = ""
			}
			masterLog := &tailBuffer{}
			master := helperCommand(ctx, filepath.Join(owned, "lan_controller.exe"), args...)
			master.Stdin = strings.NewReader(input)
			master.Stdout, master.Stderr = masterLog, masterLog
			err := master.Run()
			output := masterLog.String()
			if scenario == "explicit-refused" {
				if err == nil || strings.Contains(output, "[lan][匹配]") || strings.Contains(output, "[lan][配对] 成功") {
					t.Fatal("explicit refusal fell back to discovery/another host", err)
				}
				return
			}
			if err != nil {
				t.Fatal("owned master failed", scenario, err, output)
			}
			if scenario == "list-no-key" {
				if strings.Contains(output, "连接密钥") || strings.Contains(output, "[lan][配对] 成功") || !strings.Contains(output, address) {
					t.Fatal("list unexpectedly requested a key or paired", output)
				}
				return
			}
			if scenario == "automatic" && (!strings.Contains(output, "连接密钥") || !strings.Contains(output, "唯一匹配设备") || strings.Contains(output, "设备序号或")) {
				t.Fatal("automatic target still requires IP/selection", output)
			}
			if scenario == "explicit" && strings.Contains(output, "[lan][匹配]") {
				t.Fatal("explicit target triggered discovery", output)
			}
			decoded := regexp.MustCompile(`decoded=(\d+)`).FindStringSubmatch(output)
			if len(decoded) != 2 || decoded[1] == "0" || !strings.Contains(output, "[lan][配对] 成功") {
				t.Fatal("no synthetic JPEG received", output)
			}
			// Wait for session cleanup before probing the same owned listener again.
			deadline := time.Now().Add(time.Second)
			for time.Now().Before(deadline) {
				peer, err := probeKeyMatch(ctx, address, "owned-cli-key", nil)
				if err == nil && !peer.busy {
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
			t.Fatal("owned session remained busy after headless controller exit")
		})
	}
}

func TestKeyMatchCLILegacyDirectCompatibility(t *testing.T) {
	if os.Getenv("MYDESK_LAN_KEY_CLI_TEST") != "1" {
		t.Skip("saved 4.19/current CLI compatibility opt-in")
	}
	module, _ := filepath.Abs(filepath.Join("..", ".."))
	current := t.TempDir()
	legacy := t.TempDir()
	archive, err := zip.OpenReader(filepath.Join(module, "..", "mydesk-lan-v4.19-update.zip"))
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	for _, name := range []string{"lan_client.exe", "lan_controller.exe"} {
		source, err := os.Open(filepath.Join(module, "dist", name))
		if err != nil {
			t.Fatal(err)
		}
		target, err := os.Create(filepath.Join(current, name))
		if err != nil {
			source.Close()
			t.Fatal(err)
		}
		_, err = io.Copy(target, source)
		source.Close()
		closeErr := target.Close()
		if err != nil || closeErr != nil {
			t.Fatal(err, closeErr)
		}
		found := false
		for _, entry := range archive.File {
			if entry.Name != name { // Exact allowlist; no arbitrary archive paths.
				continue
			}
			found = true
			r, err := entry.Open()
			if err != nil {
				t.Fatal(err)
			}
			w, err := os.Create(filepath.Join(legacy, name))
			if err != nil {
				r.Close()
				t.Fatal(err)
			}
			_, err = io.Copy(w, r)
			r.Close()
			closeErr := w.Close()
			if err != nil || closeErr != nil {
				t.Fatal(err, closeErr)
			}
		}
		if !found {
			t.Fatal("missing saved-release executable", name)
		}
	}
	for _, scenario := range []struct{ name, host, master string }{
		{"new-master-old-host", legacy, current},
		{"old-master-new-host", current, legacy},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			reserved, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			address := reserved.Addr().String()
			port := strconv.Itoa(reserved.Addr().(*net.TCPAddr).Port)
			reserved.Close()
			hostLog := &tailBuffer{}
			client := helperCommand(ctx, filepath.Join(scenario.host, "lan_client.exe"), "-interface", "127.0.0.1", "-port", port, "-code", "legacy-cli-key", "-source", "test", "-encoder", "jpeg", "-fps", "15", "-refine=false")
			client.Stdout, client.Stderr = hostLog, hostLog
			if err := client.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { cancel(); client.Wait() }()
			ready := false
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				conn, err := net.DialTimeout("tcp4", address, 100*time.Millisecond)
				if err == nil {
					conn.Close()
					ready = true
					break
				}
				time.Sleep(30 * time.Millisecond)
			}
			if !ready {
				t.Fatal("owned legacy/current listener not ready")
			}
			masterLog := &tailBuffer{}
			master := helperCommand(ctx, filepath.Join(scenario.master, "lan_controller.exe"), "-connect", address, "-code", "legacy-cli-key", "-fps", "15", "-test-seconds", "1")
			master.Stdout, master.Stderr = masterLog, masterLog
			if err := master.Run(); err != nil {
				t.Fatal("explicit v6 compatibility failed", err, masterLog.String())
			}
			decoded := regexp.MustCompile(`decoded=(\d+)`).FindStringSubmatch(masterLog.String())
			if len(decoded) != 2 || decoded[1] == "0" || strings.Contains(masterLog.String(), "[lan][匹配]") {
				t.Fatal("explicit compatibility triggered discovery or got no video", masterLog.String())
			}
			deadline = time.Now().Add(time.Second)
			for time.Now().Before(deadline) && !strings.Contains(hostLog.String(), "controller disconnected") {
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}
