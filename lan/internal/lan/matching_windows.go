//go:build windows && amd64

package lan

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"strconv"
	"sync"
	"time"
)

const (
	keyMatchFeature = "key-match-v1"
	keyMatchWorkers = 4
	keyMatchLimit   = 128
	keyMatchTimeout = 6 * time.Second
)

var errKeyMismatch = errors.New("密钥不匹配")

func (h *host) matchIdentity() string {
	h.identityOnce.Do(func() { h.identity = randomCode() + randomCode() })
	return h.identity
}

// A probe authenticates only. It must not reserve a host session, initialize
// capture/decoders, release inputs or disturb an existing controller.
func (h *host) acceptMatchProbe(c *tls.Conn, w *wire, m message) {
	proof, err := channelProof(c, h.code, "match-controller")
	if m.Version != protocolVersion || err != nil || !checkProof(m.Proof, proof) {
		_ = w.send(message{Type: "error", Text: "密钥或匹配协议不正确"}, nil)
		return
	}
	h.mu.Lock()
	busy := h.session != nil
	h.mu.Unlock()
	proof, err = channelProof(c, h.code, "match-client")
	if err != nil {
		return
	}
	_ = w.send(message{Type: "match-result", Version: protocolVersion, DeviceID: h.matchIdentity(), Name: h.name, Busy: busy, Proof: proof}, nil)
}

type keyMatchProbe struct {
	id, name, address string
	busy              bool
}

type keyMatchDevice struct {
	id, name  string
	addresses []string
	busy      bool
}

type keyMatchReport struct {
	legacy, failed, skipped int
}

func probeKeyMatch(ctx context.Context, address, code string, local net.IP) (keyMatchProbe, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	c, err := dialTLSLogged(ctx, address, false, local)
	if err != nil {
		return keyMatchProbe{}, err
	}
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	w := &wire{conn: c, timeout: 2 * time.Second}
	proof, err := channelProof(c, code, "match-controller")
	if err != nil {
		return keyMatchProbe{}, err
	}
	if err := w.send(message{Type: "match-probe", Version: protocolVersion, Proof: proof}, nil); err != nil {
		return keyMatchProbe{}, err
	}
	m, body, err := w.read()
	if err != nil {
		return keyMatchProbe{}, err
	}
	if m.Type == "error" {
		return keyMatchProbe{}, errKeyMismatch
	}
	proof, err = channelProof(c, code, "match-client")
	if err != nil || m.Type != "match-result" || m.Version != protocolVersion || len(body) != 0 || len(m.DeviceID) < 12 || len(m.DeviceID) > 128 || !checkProof(m.Proof, proof) {
		return keyMatchProbe{}, errors.New("设备匹配证明无效")
	}
	return keyMatchProbe{id: m.DeviceID, name: m.Name, address: c.RemoteAddr().String(), busy: m.Busy}, nil
}

// Check discovered addresses with bounded concurrency, then merge by identity
// authenticated over TLS. Neither advertised IDs nor hostnames are trusted as
// deduplication keys; a stale address must not hide a reachable matching one.
func findKeyMatches(parent context.Context, devices []device, probe func(context.Context, string) (keyMatchProbe, error)) ([]keyMatchDevice, keyMatchReport) {
	ctx, cancel := context.WithTimeout(parent, keyMatchTimeout)
	defer cancel()
	var report keyMatchReport
	var addresses []string
	seenAddresses := map[string]bool{}
	legacy := map[string]bool{}
	for _, d := range devices {
		ip := net.ParseIP(d.IP)
		if ip == nil || ip.To4() == nil || (!ip.IsGlobalUnicast() && !ip.IsLoopback()) || ip.IsLinkLocalUnicast() || d.Port < 1 || d.Port > 65535 || d.Version != protocolVersion {
			continue
		}
		address := net.JoinHostPort(ip.String(), strconv.Itoa(d.Port))
		if !hasFeature(d.Features, keyMatchFeature) {
			id := d.ID
			if id == "" {
				id = address
			}
			legacy[id] = true
			continue
		}
		if seenAddresses[address] {
			continue
		}
		seenAddresses[address] = true
		if len(addresses) == keyMatchLimit {
			report.skipped++
			continue
		}
		addresses = append(addresses, address)
	}
	report.legacy = len(legacy)
	type result struct {
		peer keyMatchProbe
		err  error
	}
	results := make([]result, len(addresses))
	jobs := make(chan int, len(addresses))
	for i := range addresses {
		jobs <- i
	}
	close(jobs)
	var workers sync.WaitGroup
	for i := 0; i < min(keyMatchWorkers, len(addresses)); i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				if err := ctx.Err(); err != nil {
					results[index].err = err
					continue
				}
				peer, err := probe(ctx, addresses[index])
				results[index] = result{peer, err}
			}
		}()
	}
	workers.Wait()
	var matches []keyMatchDevice
	seenIDs := map[string]int{}
	for _, result := range results {
		if result.err != nil || result.peer.id == "" || result.peer.address == "" {
			report.failed++
			continue
		}
		peer := result.peer
		if index, ok := seenIDs[peer.id]; ok {
			match := &matches[index]
			found := false
			for _, address := range match.addresses {
				found = found || address == peer.address
			}
			if !found {
				match.addresses = append(match.addresses, peer.address)
			}
			match.busy = match.busy || peer.busy
		} else {
			seenIDs[peer.id] = len(matches)
			matches = append(matches, keyMatchDevice{id: peer.id, name: peer.name, addresses: []string{peer.address}, busy: peer.busy})
		}
	}
	return matches, report
}
