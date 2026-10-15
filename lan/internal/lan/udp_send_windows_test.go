//go:build windows && amd64

package lan

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

type scriptedDatagrams struct {
	mu                   sync.Mutex
	deadline             time.Time
	deadlines            []time.Time
	writes               [][]byte
	calls, deadlineCalls int
	failAt, failures     int
	always               bool
	fault                error
	delayAt              int
	delay                time.Duration
}

func (w *scriptedDatagrams) SetWriteDeadline(deadline time.Time) error {
	w.mu.Lock()
	w.deadline = deadline
	w.deadlines = append(w.deadlines, deadline)
	w.deadlineCalls++
	w.mu.Unlock()
	return nil
}
func (w *scriptedDatagrams) WriteToUDP(b []byte, _ *net.UDPAddr) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls++
	if !time.Now().Before(w.deadline) {
		return 0, context.DeadlineExceeded
	}
	if len(w.writes) == w.failAt && (w.always || w.failures > 0) {
		w.failures--
		return 0, w.fault
	}
	if w.delay > 0 && len(w.writes) == w.delayAt {
		time.Sleep(w.delay)
		if !time.Now().Before(w.deadline) {
			return 0, context.DeadlineExceeded
		}
	}
	w.writes = append(w.writes, append([]byte(nil), b...))
	return len(b), nil
}

func TestUDPBatchTransientRetryDoesNotReplayOrReorder(t *testing.T) {
	packets := make([][]byte, 96)
	for i := range packets {
		packets[i] = []byte{byte(i), 99}
	}
	w := &scriptedDatagrams{failAt: 35, failures: 2, fault: &net.OpError{Op: "write", Err: &os.SyscallError{Syscall: "wsasendto", Err: syscall.Errno(10055)}}}
	metrics := &hostSendMetrics{}
	var gate udpBatchWriter
	batches := 0
	if err := sendUDPFrame(context.Background(), w, &gate, packets, nil, true, metrics, func(time.Time) { batches++ }); err != nil {
		t.Fatal(err)
	}
	if len(w.writes) != len(packets) || w.calls != len(packets)+2 || metrics.counters[sendUDPRetry] != 2 || metrics.counters[sendUDPRecovered] != 1 || batches < 2 {
		t.Fatal("retry/batch accounting", len(w.writes), w.calls, batches, metrics.counters)
	}
	for i := range packets {
		if !bytes.Equal(packets[i], w.writes[i]) {
			t.Fatal("successful datagram replayed or reordered", i)
		}
	}
	if w.deadlineCalls < 3 {
		t.Fatal("not split into batches")
	}
}

func TestUDPBatchesKeepOneFrameDeadline(t *testing.T) {
	packets := make([][]byte, 97)
	for i := range packets {
		packets[i] = []byte{byte(i)}
	}
	w := &scriptedDatagrams{}
	var gate udpBatchWriter
	if err := sendUDPFrame(context.Background(), w, &gate, packets, nil, true, nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(w.deadlines) != 4 {
		t.Fatal("unexpected batch count", len(w.deadlines))
	}
	for _, d := range w.deadlines {
		if !d.Equal(w.deadlines[0]) {
			t.Fatal("batching extended the absolute frame deadline")
		}
	}
}

func TestUDPRecoveredPacketRestoresNormalRemainderBudget(t *testing.T) {
	packets := make([][]byte, 66)
	for i := range packets {
		packets[i] = []byte{byte(i)}
	}
	w := &scriptedDatagrams{failAt: 35, failures: 1, fault: syscall.Errno(10055), delayAt: 36, delay: 8 * time.Millisecond}
	var gate udpBatchWriter
	if err := sendUDPFrame(context.Background(), w, &gate, packets, nil, true, nil, nil); err != nil || len(w.writes) != len(packets) {
		t.Fatal("recovered resource window incorrectly capped healthy remainder", err, len(w.writes))
	}
}

func TestUDPPressureIsBoundedAndFatalErrorsAreNotRetried(t *testing.T) {
	for _, smoothing := range []bool{false, true} {
		w := &scriptedDatagrams{always: true, fault: syscall.Errno(10055)}
		var gate udpBatchWriter
		start := time.Now()
		if err := sendUDPFrame(context.Background(), w, &gate, [][]byte{{1}}, nil, smoothing, nil, nil); err == nil || time.Since(start) >= 100*time.Millisecond || w.calls > udpRetryLimit+1 {
			t.Fatal("unbounded pressure retry", err, w.calls, time.Since(start))
		}
		if !smoothing && w.calls != 1 {
			t.Fatal("disabled switch changed 4.17 fallback")
		}
	}
	for _, fault := range []error{syscall.Errno(10013), syscall.Errno(10051), syscall.Errno(10040), syscall.Errno(10038), syscall.Errno(10004), net.ErrClosed, context.DeadlineExceeded} {
		w := &scriptedDatagrams{always: true, fault: fault}
		var gate udpBatchWriter
		if err := sendUDPFrame(context.Background(), w, &gate, [][]byte{{1}}, nil, true, nil, nil); !errors.Is(err, fault) || w.calls != 1 {
			t.Fatal("fatal/canceled condition retried", fault, err, w.calls)
		}
	}
	if !temporaryUDPSendError(&os.SyscallError{Syscall: "write", Err: syscall.Errno(10035)}) {
		t.Fatal("would-block not recognized by code")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := &scriptedDatagrams{}
	var gate udpBatchWriter
	if err := sendUDPFrame(ctx, w, &gate, [][]byte{{1}}, nil, true, nil, nil); !errors.Is(err, context.Canceled) || w.calls != 0 {
		t.Fatal("canceled video kept writing")
	}
}

func TestNACKQueueDoesNotBlockControlAndIsBounded(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &hostSession{ctx: ctx, udpSmoothing: true, sendMetrics: &hostSendMetrics{}}
	s.udpSender.mu.Lock()
	start := time.Now()
	for i := 0; i < 100; i++ {
		s.retransmit(message{Frame: 1, Missing: []uint16{0}})
	}
	s.udpSender.mu.Unlock()
	if time.Since(start) > 50*time.Millisecond || len(s.nacks) != 8 || s.sendMetrics.counters[sendNACKDropped] != 92 {
		t.Fatal("repair I/O blocked control or queue expanded")
	}
}

func TestQueuedNACKRecoversStaticFrameWithNoFurtherVideo(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sender, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()
	receiver, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()
	p, _ := makePacketCipher(make([]byte, 36))
	f := videoFrame{id: 1, epoch: 1, codec: codecJPEGDelta, key: true, data: bytes.Repeat([]byte{88}, fragmentSize+8)}
	packets := splitFrame(p, f)
	s := &hostSession{ctx: ctx, cancel: cancel, udp: sender, tx: p, udpSmoothing: true, peer: receiver.LocalAddr().(*net.UDPAddr), cache: map[uint64]cachedFrame{1: {packets: packets, at: time.Now()}}, sendMetrics: &hostSendMetrics{}}
	s.epoch.Store(1)
	frames := make(chan videoFrame) // No new pictures arrive to wake a FIFO sender.
	done := make(chan error, 1)
	go func() { done <- s.sendFrames(frames) }()
	defer func() { cancel(); sender.Close(); <-done }()
	s.retransmit(message{Frame: 1, Missing: []uint16{0, 1}})
	receiver.SetReadDeadline(time.Now().Add(time.Second))
	assembler := newAssembler()
	for i := 0; i < 2; i++ {
		b := make([]byte, packetSize)
		n, _, err := receiver.ReadFromUDP(b)
		if err != nil {
			t.Fatal("idle repair not sent", err)
		}
		fragment, err := p.open(b[:n])
		if err != nil {
			t.Fatal(err)
		}
		got, err := assembler.add(fragment, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if i == 1 && (got == nil || !bytes.Equal(got.data, f.data)) {
			t.Fatal("idle last frame not recovered")
		}
	}
}

type pressureSocket struct {
	*net.UDPConn
	fault    error
	always   bool
	failed   atomic.Bool
	attempts atomic.Uint64
}

func (w *pressureSocket) WriteToUDP(b []byte, peer *net.UDPAddr) (int, error) {
	if len(b) > 4 && b[4] == packetVideo && (w.always || !w.failed.Swap(true)) {
		w.attempts.Add(1)
		return 0, &os.SyscallError{Syscall: "wsasendto", Err: w.fault}
	}
	return w.UDPConn.WriteToUDP(b, peer)
}

func TestSmoothedSessionResourceRecoveryAndTCPFallback(t *testing.T) {
	for _, test := range []struct {
		name                         string
		smooth, persistent, fallback bool
		fault                        error
	}{
		{"one-buffer-error", true, false, false, syscall.Errno(10055)},
		{"legacy-one-buffer-error", false, false, true, syscall.Errno(10055)},
		{"persistent-buffer-error", true, true, true, syscall.Errno(10055)},
		{"permission-error", true, true, true, syscall.Errno(10013)},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cfg, err := ephemeralTLS()
			if err != nil {
				t.Fatal(err)
			}
			ln, err := tls.Listen("tcp4", "127.0.0.1:0", cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			var socket *pressureSocket
			h := &host{code: "pressure-test", queueGrace: test.smooth, udpSmoothing: test.smooth, sendStats: true, cfg: videoConfig{source: "test", encoder: "jpeg", quality: 100, fps: 60, area: desktop{width: 320, height: 180}}, udpWriterFactory: func(c *net.UDPConn) datagramWriter {
				socket = &pressureSocket{UDPConn: c, fault: test.fault, always: test.persistent}
				return socket
			}}
			go func() {
				for {
					c, e := ln.Accept()
					if e != nil {
						return
					}
					go h.accept(ctx, c.(*tls.Conn), false)
				}
			}()
			s, err := connectController(ctx, cancel, ln.Addr().String(), h.code, 60, "", true, net.IPv4(127, 0, 0, 1))
			if err != nil {
				t.Fatal(err)
			}
			defer s.close()
			s.view = &viewer{width: 320, height: 180, onInput: func(inputEvent) {}}
			if err := s.start(false, false); err != nil {
				t.Fatal(err)
			}
			waitJPEG(t, func() bool { return s.decoded.Load() > 4 })
			h.mu.Lock()
			hs := h.session
			h.mu.Unlock()
			if s.transport.Load() != test.fallback || hs.tcp.Load() != test.fallback || socket.attempts.Load() == 0 {
				t.Fatal("unexpected transport recovery", s.transport.Load(), socket.attempts.Load())
			}
			if !test.fallback && hs.epoch.Load() != 1 {
				t.Fatal("one transient buffer error restarted encoder", hs.epoch.Load())
			}
			if test.fallback && hs.epoch.Load() < 2 {
				t.Fatal("fallback omitted fresh reference chain")
			}
			if _, err := s.remoteListRequest(ctx, "", "file-default"); err != nil {
				t.Fatal("F8 auxiliary stopped", err)
			}
			if _, err := s.runCommand(ctx, programDirectory(), "echo smooth-ok", 1); err != nil {
				t.Fatal("F7 auxiliary stopped", err)
			}
			t.Logf("%s: socket faults=%d epoch=%d TCP=%t decoded=%d | %s", test.name, socket.attempts.Load(), hs.epoch.Load(), s.transport.Load(), s.decoded.Load(), hs.sendMetrics.summary())
		})
	}
}

func TestHealthyUDPSmallFrameHasNoPacingSleep(t *testing.T) {
	w := &scriptedDatagrams{}
	var gate udpBatchWriter
	start := time.Now()
	if err := sendUDPFrame(context.Background(), w, &gate, [][]byte{{1}, {2}}, nil, true, nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(w.writes) != 2 || w.deadlineCalls != 1 || time.Since(start) > 50*time.Millisecond {
		t.Fatal("small frame paced/delayed")
	}
	t.Log(fmt.Sprintf("two healthy datagrams submitted in %v", time.Since(start)))
}
