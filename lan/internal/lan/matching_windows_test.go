//go:build windows && amd64

package lan

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func keyMatchHostFixture(t *testing.T, code, name string) (*host, string, context.Context) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cfg, err := ephemeralTLS()
	if err != nil {
		t.Fatal(err)
	}
	ln, err := tls.Listen("tcp4", "127.0.0.1:0", cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	h := &host{code: code, name: name, cfg: videoConfig{area: desktop{width: 320, height: 180}, encoder: "jpeg", fps: 30, quality: 90}}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go h.accept(ctx, c.(*tls.Conn), false)
		}
	}()
	return h, ln.Addr().String(), ctx
}

func keyMatchAnnouncement(address string) device {
	ip, port, _ := net.SplitHostPort(address)
	number, _ := strconv.Atoi(port)
	return device{ID: "untrusted-discovery-id", Version: protocolVersion, Name: "untrusted-discovery-name", IP: ip, Port: number, Features: []string{keyMatchFeature}}
}

func TestKeyMatchProbeAuthenticatesWithoutStartingOrStealingSession(t *testing.T) {
	h, address, ctx := keyMatchHostFixture(t, "matching-test-secret", "verified-host")
	var captures atomic.Int64
	h.cfg.grab = func() ([]byte, error) {
		captures.Add(1)
		return nil, errors.New("capture must not start for a probe")
	}
	peer, err := probeKeyMatch(ctx, address, h.code, net.IPv4(127, 0, 0, 1))
	if err != nil || peer.id != h.matchIdentity() || peer.name != h.name || peer.address != address || peer.busy {
		t.Fatal("incorrect authenticated match", peer, err)
	}
	h.mu.Lock()
	started := h.session != nil
	h.mu.Unlock()
	if started || captures.Load() != 0 {
		t.Fatal("probe started capture or reserved the device")
	}
	if _, err := probeKeyMatch(ctx, address, "not-the-right-key", nil); !errors.Is(err, errKeyMismatch) {
		t.Fatal("wrong key accepted", err)
	}
	// Occupied sessions are reported, never canceled or replaced by a probe.
	occupiedCtx, occupiedCancel := context.WithCancel(ctx)
	defer occupiedCancel()
	occupied := &hostSession{ctx: occupiedCtx, cancel: occupiedCancel}
	h.mu.Lock()
	h.session = occupied
	h.mu.Unlock()
	for i := 0; i < 5; i++ {
		peer, err := probeKeyMatch(ctx, address, h.code, nil)
		if err != nil || !peer.busy || peer.id != h.matchIdentity() {
			t.Fatal("occupied device missing from matches", err)
		}
	}
	h.mu.Lock()
	unchanged := h.session == occupied
	h.mu.Unlock()
	if !unchanged || occupiedCtx.Err() != nil || captures.Load() != 0 {
		t.Fatal("probe disturbed the active controller")
	}
}

func TestKeyMatchMultipleHostsAndAuthenticatedIdentityDedup(t *testing.T) {
	h1, first, ctx := keyMatchHostFixture(t, "shared-test-key", "same-hostname")
	h2, second, _ := keyMatchHostFixture(t, h1.code, "same-hostname")
	_, wrong, _ := keyMatchHostFixture(t, "different-secret", "not-a-match")
	devices := []device{keyMatchAnnouncement(first), keyMatchAnnouncement(second), keyMatchAnnouncement(wrong), keyMatchAnnouncement(first)}
	// Deliberately identical untrusted advertised IDs/names cannot merge hosts.
	matches, report := findKeyMatches(ctx, devices, func(ctx context.Context, address string) (keyMatchProbe, error) {
		return probeKeyMatch(ctx, address, h1.code, nil)
	})
	if len(matches) != 2 || matches[0].id != h1.matchIdentity() || matches[1].id != h2.matchIdentity() || report.failed != 1 || len(matches[0].addresses) != 1 {
		t.Fatal("matching by advertised metadata or wrong key", matches, report)
	}
	var output bytes.Buffer
	address, code, err := selectControllerTarget(ctx, bufio.NewReader(strings.NewReader("2\n")), &output, "", h1.code, defaultPort,
		func(context.Context, string) ([]keyMatchDevice, keyMatchReport, error) { return matches, report, nil })
	if err != nil || address != second || code != h1.code || !strings.Contains(output.String(), first) || !strings.Contains(output.String(), second) {
		t.Fatal("same-key device selection failed", address, err, output.String())
	}
}

func TestKeyMatchTLSProofRolesAndReplay(t *testing.T) {
	h, address, ctx := keyMatchHostFixture(t, "roles-test-key", "private-host")
	var replay string
	for index, role := range []string{"match-controller", "replay", "controller"} {
		c, err := dialTLSLogged(ctx, address, false)
		if err != nil {
			t.Fatal(err)
		}
		w := &wire{conn: c, timeout: time.Second}
		proof := replay
		if role != "replay" {
			proof, err = channelProof(c, h.code, role)
			if err != nil {
				t.Fatal(err)
			}
		}
		if index == 0 {
			replay = proof
		}
		if err := w.send(message{Type: "match-probe", Version: protocolVersion, Proof: proof}, nil); err != nil {
			t.Fatal(err)
		}
		m, _, err := w.read()
		c.Close()
		if err != nil {
			t.Fatal(err)
		}
		if index == 0 && m.Type != "match-result" {
			t.Fatal("valid matching role rejected")
		}
		if index != 0 && (m.Type != "error" || m.Name != "" || m.DeviceID != "" || m.Busy) {
			t.Fatal("replay/other-role proof accepted or leaked device state", m.Type)
		}
	}
}

func TestKeyMatchMultiAddressFallbackBoundsAndLegacy(t *testing.T) {
	for _, mode := range []string{"merge", "legacy", "limit", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var devices []device
			for index := 0; index < 140; index++ {
				d := keyMatchAnnouncement(fmt.Sprintf("127.0.0.1:%d", 30000+index))
				if mode == "legacy" {
					d.Features = nil
				}
				devices = append(devices, d)
				if mode == "merge" && index == 2 {
					break
				}
			}
			if mode == "cancel" {
				cancel()
			}
			var active, peak, calls atomic.Int64
			matches, report := findKeyMatches(ctx, devices, func(ctx context.Context, address string) (keyMatchProbe, error) {
				calls.Add(1)
				n := active.Add(1)
				defer active.Add(-1)
				for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
				}
				select {
				case <-time.After(time.Millisecond):
				case <-ctx.Done():
					return keyMatchProbe{}, ctx.Err()
				}
				if strings.HasSuffix(address, ":30000") {
					return keyMatchProbe{}, errors.New("stale first address")
				}
				return keyMatchProbe{id: "verified-same-device", name: "verified-name", address: address}, nil
			})
			if peak.Load() > keyMatchWorkers {
				t.Fatal("unbounded probe concurrency", peak.Load())
			}
			switch mode {
			case "merge":
				if len(matches) != 1 || len(matches[0].addresses) != 2 || matches[0].addresses[0] != "127.0.0.1:30001" || report.failed != 1 {
					t.Fatal("address fallback/identity merge failed", matches, report)
				}
			case "legacy":
				if calls.Load() != 0 || len(matches) != 0 || report.legacy != 1 {
					t.Fatal("legacy device was probed/reserved", calls.Load(), report)
				}
			case "limit":
				if calls.Load() != keyMatchLimit || report.skipped != 12 {
					t.Fatal("probe limit not enforced", calls.Load(), report)
				}
			case "cancel":
				if calls.Load() != 0 || len(matches) != 0 {
					t.Fatal("canceled search probed devices")
				}
			}
		})
	}
}

func TestKeyMatchSelectionConsole(t *testing.T) {
	free := keyMatchDevice{id: "free", name: "verified-free", addresses: []string{"192.168.1.5:29860", "192.168.2.5:29860"}}
	busy := keyMatchDevice{id: "busy", name: "verified-busy", addresses: []string{"192.168.1.6:29860"}, busy: true}
	for _, test := range []struct {
		name, input, explicit, want string
		matches                     []keyMatchDevice
		finds                       int
	}{
		{name: "one", want: strings.Join(free.addresses, ","), matches: []keyMatchDevice{free}, finds: 1},
		{name: "many", input: "2\n", want: strings.Join(free.addresses, ","), matches: []keyMatchDevice{busy, free}, finds: 1},
		{name: "busy-cannot-select", input: "1\n2\n", want: strings.Join(free.addresses, ","), matches: []keyMatchDevice{busy, free}, finds: 1},
		{name: "bad-choice", input: "99\ngarbage\n2\n", want: strings.Join(free.addresses, ","), matches: []keyMatchDevice{busy, free}, finds: 1},
		{name: "manual", input: "192.168.1.7:31000\n", want: "192.168.1.7:31000", finds: 1},
		{name: "explicit", explicit: "192.168.1.8", want: "192.168.1.8:29860", finds: 0},
		{name: "only-busy-manual", input: "192.168.1.9\n", want: "192.168.1.9:29860", matches: []keyMatchDevice{busy}, finds: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			calls := 0
			address, code, err := selectControllerTarget(context.Background(), bufio.NewReader(strings.NewReader(test.input)), &output, test.explicit, "do-not-print-this-key", defaultPort,
				func(ctx context.Context, code string) ([]keyMatchDevice, keyMatchReport, error) {
					calls++
					return test.matches, keyMatchReport{}, nil
				})
			if err != nil || address != test.want || code != "do-not-print-this-key" || calls != test.finds || strings.Contains(output.String(), code) {
				t.Fatal("incorrect target/key/output", address, calls, err, output.String())
			}
		})
	}
	t.Run("rescan-change-key", func(t *testing.T) {
		var output bytes.Buffer
		calls := 0
		_, code, err := selectControllerTarget(context.Background(), bufio.NewReader(strings.NewReader("first-secret\nr\nk\nsecond-secret\n")), &output, "", "", defaultPort,
			func(ctx context.Context, code string) ([]keyMatchDevice, keyMatchReport, error) {
				calls++
				if code == "second-secret" {
					return []keyMatchDevice{free}, keyMatchReport{}, nil
				}
				return nil, keyMatchReport{legacy: 1}, nil
			})
		if err != nil || code != "second-secret" || calls != 3 || strings.Contains(output.String(), code) {
			t.Fatal("rescan/key replacement failed", calls, err)
		}
	})
	for _, input := range []string{"q\n", ""} {
		var output bytes.Buffer
		_, _, err := selectControllerTarget(context.Background(), bufio.NewReader(strings.NewReader(input)), &output, "", "valid-secret", defaultPort,
			func(context.Context, string) ([]keyMatchDevice, keyMatchReport, error) {
				return []keyMatchDevice{busy}, keyMatchReport{}, nil
			})
		if err == nil {
			t.Fatal("cancel/EOF selected a device")
		}
	}
	if matchDisplayName("host\x1b\n\r\u202e") != "host" {
		t.Fatal("peer name can inject control sequences")
	}
	for _, bad := range []string{"", ",", "192.168.1.5:0", "192.168.1.5:65536", "192.168.1.5:bad", "bad host"} {
		if _, err := normalizeControllerAddresses(bad, defaultPort); err == nil {
			t.Fatal("invalid explicit address accepted", bad)
		}
	}
}

func TestKeyMatchProbeCancellationClosesUnresponsivePeer(t *testing.T) {
	parent, stop := context.WithCancel(context.Background())
	defer stop()
	cfg, err := ephemeralTLS()
	if err != nil {
		t.Fatal(err)
	}
	listener, err := tls.Listen("tcp4", "127.0.0.1:0", cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		c, err := listener.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_ = c.(*tls.Conn).HandshakeContext(parent)
		<-parent.Done()
	}()
	ctx, cancel := context.WithTimeout(parent, 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = probeKeyMatch(ctx, listener.Addr().String(), "canceled-key", nil)
	if err == nil || time.Since(started) > time.Second {
		t.Fatal("canceled probe kept waiting for its socket timeout", err)
	}
}

func TestKeyMatchRejectsUnauthenticatedReply(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg, err := ephemeralTLS()
	if err != nil {
		t.Fatal(err)
	}
	listener, err := tls.Listen("tcp4", "127.0.0.1:0", cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		c, err := listener.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		w := &wire{conn: c, timeout: time.Second}
		if _, _, err := w.read(); err == nil {
			_ = w.send(message{Type: "match-result", Version: protocolVersion, Name: "forged-name", DeviceID: "forged-device-id", Proof: "not-a-valid-proof"}, nil)
		}
	}()
	if _, err := probeKeyMatch(ctx, listener.Addr().String(), "actual-private-key", nil); err == nil {
		t.Fatal("unverified peer was accepted as a key match")
	}
}

func TestKeyMatchAdvertisementContainsNoKeyAndUsesStableIdentity(t *testing.T) {
	h, address, ctx := keyMatchHostFixture(t, "secret-not-for-broadcast", "announced-host")
	_, portText, _ := net.SplitHostPort(address)
	port, _ := strconv.Atoi(portText)
	advertiseError := make(chan error, 1)
	go func() {
		advertiseError <- advertiseDeviceOn(ctx, port, h.name, net.IPv4(127, 0, 0, 1), h.matchIdentity(), []string{keyMatchFeature})
	}()
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var data []byte
	for i := 0; i < 6; i++ {
		select {
		case err := <-advertiseError:
			t.Fatal("owned discovery socket failed", err)
		default:
		}
		c.SetDeadline(time.Now().Add(500 * time.Millisecond))
		_, _ = c.WriteToUDP([]byte(discoverMagic), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
		buffer := make([]byte, 2048)
		n, _, err := c.ReadFromUDP(buffer)
		if err == nil {
			data = buffer[:n]
			break
		}
	}
	var advertised device
	if json.Unmarshal(data, &advertised) != nil || advertised.ID != h.matchIdentity() || !hasFeature(advertised.Features, keyMatchFeature) || bytes.Contains(data, []byte(h.code)) || bytes.Contains(data, []byte("proof")) {
		t.Fatal("incorrect/private discovery data", string(data))
	}
	peer, err := probeKeyMatch(ctx, address, h.code, nil)
	if err != nil || peer.id != advertised.ID {
		t.Fatal("advertised and authenticated identity differ", err)
	}
}
