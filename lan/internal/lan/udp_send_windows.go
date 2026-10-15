//go:build windows && amd64

package lan

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"runtime"
	"sync"
	"syscall"
	"time"
)

const (
	udpBatchPackets = 32
	udpFrameBudget  = 60 * time.Millisecond // Same absolute frame budget as 4.17.
	udpRetryBudget  = 4 * time.Millisecond
	udpRetryLimit   = 3
)

type datagramWriter interface {
	WriteToUDP([]byte, *net.UDPAddr) (int, error)
	SetWriteDeadline(time.Time) error
}

// Serialize short batches and deadline changes, not an entire frame. Probe
// and background writes cannot extend a video's socket-wide deadline.
type udpBatchWriter struct{ mu sync.Mutex }

func (w *udpBatchWriter) batch(ctx context.Context, out datagramWriter, packets [][]byte, peer *net.UDPAddr, deadline time.Time) (sent int, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err = ctx.Err(); err != nil {
		return
	}
	if !time.Now().Before(deadline) {
		return 0, context.DeadlineExceeded
	}
	if err = out.SetWriteDeadline(deadline); err != nil {
		return
	}
	for _, p := range packets {
		if err = ctx.Err(); err != nil {
			return
		}
		if !time.Now().Before(deadline) {
			return sent, context.DeadlineExceeded
		}
		var n int
		n, err = out.WriteToUDP(p, peer)
		if err != nil {
			return
		}
		if n != len(p) {
			return sent, io.ErrShortWrite
		}
		sent++
	}
	return
}

func temporaryUDPSendError(err error) bool {
	// Explicit Winsock resource-pressure codes only, including wrapped errors.
	// Permission, unreachable network, closed socket, oversize and timeout are
	// NOT silently retried. No English/localized error-string matching.
	return errors.Is(err, syscall.Errno(10055)) || errors.Is(err, syscall.Errno(10035))
}

func sendUDPFrame(ctx context.Context, out datagramWriter, gate *udpBatchWriter, packets [][]byte, peer *net.UDPAddr, smoothing bool, metrics *hostSendMetrics, betweenBatches func(time.Time)) error {
	deadline := time.Now().Add(udpFrameBudget)
	if !smoothing {
		if err := out.SetWriteDeadline(deadline); err != nil {
			return err
		}
		for _, p := range packets {
			if err := ctx.Err(); err != nil {
				return err
			}
			if _, err := out.WriteToUDP(p, peer); err != nil {
				metrics.add(sendUDPError)
				return err
			}
		}
		return nil
	}
	var retryUntil time.Time
	var pressureErr error
	retries := 0
	retrying := false
	for offset := 0; offset < len(packets); {
		limit := deadline
		if retrying && retryUntil.Before(limit) {
			limit = retryUntil
		}
		end := min(offset+udpBatchPackets, len(packets))
		if retrying {
			end = offset + 1
		} // Only the failed datagram owes the short retry deadline.
		n, err := gate.batch(ctx, out, packets[offset:end], peer, limit)
		offset += n
		if err != nil {
			if ctx.Err() == nil {
				metrics.add(sendUDPError)
			}
			if retrying && errors.Is(err, context.DeadlineExceeded) && pressureErr != nil && ctx.Err() == nil {
				return fmt.Errorf("UDP resource-pressure retry budget exhausted: %w", pressureErr)
			}
			if !temporaryUDPSendError(err) || retries >= udpRetryLimit || ctx.Err() != nil {
				return err
			}
			pressureErr = err
			if retryUntil.IsZero() {
				retryUntil = time.Now().Add(udpRetryBudget)
			}
			if !time.Now().Before(retryUntil) || !time.Now().Before(deadline) {
				return err
			}
			retries++
			retrying = true
			metrics.add(sendUDPRetry)
			// Release the batch lock BEFORE allowing the socket queue to drain.
			delay := min(time.Millisecond, min(time.Until(retryUntil), time.Until(deadline)))
			if delay <= 0 {
				return err
			}
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
			continue // Retry only the failed datagram, never replay successful ones.
		}
		// Recovery budget applies to pressure/retries, not the remainder of a
		// healthy large frame. Later pressure shares the same retry-count limit.
		retrying = false
		if offset < len(packets) {
			if betweenBatches != nil {
				betweenBatches(deadline)
			}
			runtime.Gosched() // No fixed per-frame/per-batch sleep on the healthy path.
		}
	}
	if retries > 0 {
		metrics.add(sendUDPRecovered)
	}
	return nil
}

func (s *hostSession) udpOutput() datagramWriter {
	if s.udpTestWriter != nil {
		return s.udpTestWriter
	}
	return s.udp
}

func (s *hostSession) writeAuxUDP(b []byte, peer *net.UDPAddr, refresh bool) {
	if !s.udpSmoothing {
		if refresh {
			_ = s.udp.SetWriteDeadline(time.Now().Add(udpFrameBudget))
		}
		_, _ = s.udp.WriteToUDP(b, peer)
		return
	}
	if !s.udpSender.mu.TryLock() {
		return
	} // Liveness probes retry; video itself keeps active sessions alive.
	defer s.udpSender.mu.Unlock()
	_ = s.udpOutput().SetWriteDeadline(time.Now().Add(udpFrameBudget))
	_, _ = s.udpOutput().WriteToUDP(b, peer)
}

func (s *hostSession) initNACKQueue() chan message {
	s.nackOnce.Do(func() { s.nacks = make(chan message, 8) })
	return s.nacks
}

// The sender, not the control-reader/input goroutine, performs UDP repair I/O.
// Also selected while JPEG is idle: a missing last/static frame can recover.
func (s *hostSession) sendQueuedNACK(m message, videoDeadline time.Time) {
	if s.tcp.Load() {
		return
	}
	s.mu.Lock()
	c, ok := s.cache[m.Frame]
	peer := s.peer
	s.mu.Unlock()
	if !ok || peer == nil || time.Since(c.at) >= 100*time.Millisecond {
		return
	}
	deadline := minTime(c.at.Add(100*time.Millisecond), time.Now().Add(2*time.Millisecond))
	if !videoDeadline.IsZero() {
		deadline = minTime(deadline, videoDeadline)
	}
	var batch [][]byte
	for _, index := range m.Missing {
		if int(index) >= len(c.packets) {
			continue
		}
		batch = append(batch, c.packets[index])
		if len(batch) == udpBatchPackets {
			if _, err := s.udpSender.batch(s.ctx, s.udpOutput(), batch, peer, deadline); err != nil {
				return
			}
			batch = batch[:0]
		}
	}
	if len(batch) > 0 {
		_, _ = s.udpSender.batch(s.ctx, s.udpOutput(), batch, peer, deadline)
	}
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
