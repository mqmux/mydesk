//go:build windows && amd64

package lan

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"time"
)

type host struct {
	cfg                                 videoConfig
	code, name                          string
	identity                            string
	identityOnce                        sync.Once
	tls                                 *tls.Config
	mu                                  sync.Mutex
	session                             *hostSession
	clipStore                           clipboardAccess
	screenList                          func(videoConfig) []videoConfig // Optional, input-free multi-display test source.
	directories                         *remoteDirectories
	queueGrace, udpSmoothing, sendStats bool
	udpWriterFactory                    func(*net.UDPConn) datagramWriter // Optional, input-free socket fault fixture.
}
type cachedFrame struct {
	packets [][]byte
	at      time.Time
	bytes   int
}
type hostSession struct {
	ctx                                            context.Context
	cancel                                         context.CancelFunc
	ctrl, video                                    *wire
	token                                          string
	videoReady                                     chan struct{}
	udp                                            *net.UDPConn
	tx, rx                                         *packetCipher
	mu                                             sync.Mutex
	peer                                           *net.UDPAddr
	cache                                          map[uint64]cachedFrame
	cacheBytes                                     int
	tcp                                            atomic.Bool
	profile                                        encoderProfile
	cfg                                            videoConfig
	restart                                        chan struct{}
	recovery                                       encoderRecovery
	frameID, epoch, sent, bytes                    atomic.Uint64
	input                                          *inputState
	refineClear                                    atomic.Uint64
	bitrate                                        atomic.Int64
	auxActive                                      atomic.Int64
	clipOnce                                       sync.Once
	clip                                           clipboardAccess
	inputDisabled, clipboardDisabled, cursorHidden atomic.Bool
	clipboardGate                                  sync.Mutex
	configMu                                       sync.RWMutex
	displayMu, inputMu                             sync.Mutex
	screenGeneration                               uint64
	screenSwitch                                   bool
	autoBitrate                                    bool
	screenReady                                    atomic.Uint64
	screenList                                     func(videoConfig) []videoConfig // deterministic test displays
	capturePreference                              string
	warmIdle                                       bool
	minimized                                      atomic.Bool
	refineWake                                     chan struct{}
	refineResumes                                  atomic.Uint64
	lastSentFrame                                  atomic.Uint64
	directories                                    *remoteDirectories
	queueGrace, udpSmoothing                       bool
	sendMetrics                                    *hostSendMetrics
	udpSender                                      udpBatchWriter
	udpTestWriter                                  datagramWriter // Optional deterministic socket-fault tests.
	nackOnce                                       sync.Once
	nacks                                          chan message
}

func ClientMain() {
	defer setupLogging("client")()
	configureDPI()
	port := flag.Int("port", defaultPort, "LAN discovery/control TCP+UDP port")
	code := flag.String("code", "", "pairing code; default is random per launch")
	fps := flag.Int("fps", maxFPS, "target frames per second (1-120)")
	monitor := flag.Int("monitor", 0, "screen / DXGI output index (0-based)")
	ff := flag.String("ffmpeg", "", "path to ffmpeg.exe")
	encoder := flag.String("encoder", "auto", "auto, h264_nvenc, h264_amf, h264_qsv, libx264, jpeg")
	capture := flag.String("capture", "auto", "auto, ddagrab, gdi")
	bitrate := flag.Int("bitrate", 0, "H.264 target kilobits/s; 0 auto scales with resolution and FPS")
	quality := flag.Int("quality", 100, "JPEG fallback quality (20-100)")
	refine := flag.Bool("refine", true, "lossless still-screen repair")
	queueGrace := flag.Bool("queue-grace", true, "brief bounded full-video-queue wait; false restores 4.17 immediate reset")
	udpSmoothing := flag.Bool("udp-smoothing", true, "bounded UDP batches/resource-pressure retries; false restores 4.17 sender")
	sendStats := flag.Bool("send-stats", false, "log local encoder-output/queue/send timing and recovery counters every 5 seconds")
	// Accept old launch scripts, but do not let them re-enable update-driven
	// foreground cadence, which regressed dragging responsiveness.
	flag.Bool("idle-video", false, "deprecated, ignored: video always uses continuous cadence")
	source := flag.String("source", "desktop", "desktop or test (synthetic 1080p source)")
	iface := flag.String("interface", "", "listen/discover on this interface name or local IPv4")
	interfaces := flag.Bool("list-interfaces", false, "list usable IPv4 interfaces and exit")
	firewall := flag.String("firewall", "", "setup or remove LAN firewall rules (confirmation/UAC required)")
	version := flag.Bool("version", false, "print LAN build/protocol version and exit")
	flag.Parse()
	if *version {
		fmt.Printf("MyDesk LAN client %s / protocol v%d\n", buildVersion, protocolVersion)
		return
	}
	if *firewall != "" {
		if err := firewallAction("client", *firewall); err != nil {
			log.Fatal(err)
		}
		return
	}
	if *interfaces {
		printInterfaces()
		return
	}
	localIP, err := resolveInterface(*iface)
	if err != nil {
		log.Fatal(err)
	}
	if !map[string]bool{"auto": true, "h264_nvenc": true, "h264_amf": true, "h264_qsv": true, "libx264": true, "jpeg": true}[*encoder] || !map[string]bool{"auto": true, "ddagrab": true, "gdi": true}[*capture] || !map[string]bool{"desktop": true, "test": true}[*source] {
		log.Fatal("invalid encoder/capture/source option")
	}
	if *ff != "" && findFFmpeg(*ff) == "" {
		log.Fatal("specified ffmpeg executable does not exist")
	}
	if *fps < 1 || *fps > maxFPS || *port < 1 || *port > 65535 || (*bitrate != 0 && *bitrate < 1000) || *bitrate > 200000 || *quality < 20 || *quality > 100 {
		log.Fatal("invalid fps/port/bitrate/quality")
	}
	screens := monitorOutputs()
	if *capture != "gdi" {
		if outputs := dxgiDisplayOutputs(); len(outputs) > 0 {
			screens = outputs
		}
	}
	if *monitor < 0 || *monitor >= len(screens) {
		log.Fatalf("monitor index invalid; %d outputs", len(screens))
	}
	area := screens[*monitor].area
	if *source == "test" {
		area = desktop{width: 1920, height: 1080}
	}
	if area.width < 1 || area.height < 1 || area.width*area.height > 16<<20 {
		log.Fatal("selected output is unavailable or too large")
	}
	if *code == "" {
		*code = randomCode()
	}
	if len(*code) < 6 {
		log.Fatal("pairing code must contain at least 6 characters")
	}
	tlsCfg, err := ephemeralTLS()
	if err != nil {
		log.Fatal(err)
	}
	name, _ := os.Hostname()
	h := &host{code: *code, name: name, tls: tlsCfg, cfg: videoConfig{fps: *fps, monitor: *monitor, bitrate: *bitrate, quality: *quality, area: area, ffmpeg: findFFmpeg(*ff), encoder: *encoder, capture: *capture, source: *source}}
	h.cfg.display = screens[*monitor].device
	h.queueGrace, h.udpSmoothing, h.sendStats = *queueGrace, *udpSmoothing, *sendStats
	h.directories = hostDirectoryMemory()
	listenIP := "0.0.0.0"
	if localIP != nil {
		listenIP = localIP.String()
	}
	ln, err := tls.Listen("tcp4", net.JoinHostPort(listenIP, fmt.Sprint(*port)), tlsCfg)
	if err != nil {
		log.Fatal(err)
	}
	defer ln.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go func() {
		<-ctx.Done()
		ln.Close()
		h.mu.Lock()
		if h.session != nil {
			h.session.cancel()
		}
		h.mu.Unlock()
	}()
	go func() {
		if err := advertiseDeviceOn(ctx, *port, name, localIP, h.matchIdentity(), []string{keyMatchFeature}); err != nil && ctx.Err() == nil {
			log.Printf("discovery unavailable: %v; direct IP still works", err)
		}
	}()
	log.Printf("MyDesk LAN client %s / protocol v%d | %s | screen %dx%d | target %d FPS", buildVersion, protocolVersion, name, area.width, area.height, *fps)
	log.Printf("PAIRING CODE: %s", *code)
	log.Printf("[lan][监听] TCP 已就绪 %s；若连接超时，请运行 -firewall setup，或检查 VPN 允许局域网访问", ln.Addr())
	for _, n := range localNICs() {
		if localIP != nil && !localIP.Equal(n.ip) {
			continue
		}
		kind := "VPN/虚拟"
		if n.physical {
			kind = "物理"
		}
		log.Printf("Connect: %s:%d [%s / %s]", n.ip, *port, n.name, kind)
	}
	if localIP != nil && localIP.IsLoopback() {
		log.Printf("Connect: %s:%d [仅本机]", localIP, *port)
	}
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Print(err)
			continue
		}
		go h.accept(ctx, c.(*tls.Conn), *refine)
	}
}
func (h *host) accept(parent context.Context, c *tls.Conn, refine bool) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(8 * time.Second))
	if err := c.HandshakeContext(parent); err != nil {
		return
	}
	w := &wire{conn: c}
	m, _, err := w.read()
	if err != nil {
		return
	}
	if m.Type == "match-probe" {
		h.acceptMatchProbe(c, w, m)
		return
	}
	if m.Type == "attach-aux" {
		h.acceptAux(c, w, m)
		return
	}
	if m.Type == "attach" {
		h.attach(c, w, m)
		return
	}
	if m.Type != "pair" || m.Version != protocolVersion {
		_ = w.send(message{Type: "error", Text: "LAN 协议版本不匹配，请同时更新主控端和被控端"}, nil)
		return
	}
	want, err := channelProof(c, h.code, "controller")
	if err != nil || !checkProof(m.Proof, want) {
		_ = w.send(message{Type: "error", Text: "pairing code incorrect"}, nil)
		return
	}
	h.mu.Lock()
	if h.session != nil {
		h.mu.Unlock()
		_ = w.send(message{Type: "error", Text: "device already has an active controller"}, nil)
		return
	}
	ctx, cancel := context.WithCancel(parent)
	s := &hostSession{ctx: ctx, cancel: cancel, ctrl: w, token: randomCode() + randomCode(), videoReady: make(chan struct{}), cache: map[uint64]cachedFrame{}, restart: make(chan struct{}, 1), refineWake: make(chan struct{}, 1), cfg: h.cfg, input: newInputState(h.cfg.area), screenList: h.screenList}
	s.capturePreference = h.cfg.capture
	s.queueGrace, s.udpSmoothing = h.queueGrace, h.udpSmoothing
	if h.sendStats {
		s.sendMetrics = &hostSendMetrics{}
	}
	// The listener's startup rectangle is only a preference, not live metrics.
	// Always resolve the selected device again, including after an offline change.
	if (s.cfg.source == "desktop" && s.cfg.grab == nil) || s.screenList != nil {
		if fresh, ok := resolveDisplay(s.cfg, s.displayTargets(s.cfg)); ok {
			s.cfg = fresh
			s.input.area = fresh.area
		} else {
			cancel()
			h.mu.Unlock()
			_ = w.send(message{Type: "error", Text: displayUnavailableError(s.cfg).Error()}, nil)
			return
		}
	}
	s.cursorHidden.Store(true)
	s.cfg.hideCursor = true
	s.screenSwitch = hasFeature(m.Features, screenSwitchFeature)
	s.warmIdle = backgroundFeatureFor(m.Features) != ""
	if h.directories != nil && hasFeature(m.Features, directoryFeature) {
		s.directories = h.directories
	}
	if s.screenSwitch {
		s.screenGeneration = 1
	}
	h.session = s
	h.mu.Unlock()
	defer func() {
		cancel()
		s.releaseInputs()
		if s.udp != nil {
			s.udp.Close()
		}
		s.mu.Lock()
		if s.video != nil {
			s.video.conn.Close()
		}
		s.mu.Unlock()
		h.mu.Lock()
		if h.session == s {
			h.session = nil
		}
		h.mu.Unlock()
		log.Print("[lan] controller disconnected; keys/buttons released")
	}()
	proof, _ := channelProof(c, h.code, "client")
	if err := w.send(message{Type: "paired", Proof: proof}, nil); err != nil {
		return
	}
	_ = c.SetDeadline(time.Time{})
	go func() { <-ctx.Done(); c.Close() }()
	s.tx, s.rx, err = sessionCiphers(c, true)
	if err != nil {
		return
	}
	s.udp, err = net.ListenUDP("udp4", &net.UDPAddr{IP: c.LocalAddr().(*net.TCPAddr).IP})
	if err != nil {
		return
	}
	_ = s.udp.SetWriteBuffer(4 << 20)
	_ = s.udp.SetReadBuffer(4 << 20)
	if h.udpWriterFactory != nil {
		s.udpTestWriter = h.udpWriterFactory(s.udp)
	}
	s.cfg.fps = negotiatedFPS(s.cfg.fps, m.FPS)
	s.autoBitrate = s.cfg.bitrate == 0
	if s.autoBitrate {
		s.cfg.bitrate = automaticBitrate(s.cfg)
	}
	s.bitrate.Store(int64(s.cfg.bitrate))
	if m.Quality > 0 && m.Quality <= 100 {
		s.cfg.quality = max(20, m.Quality)
		s.cfg.h264Quality = max(8, 32-s.cfg.quality*24/100)
	}
	if m.Backend == "jpeg-only" && s.cfg.encoder == "auto" {
		s.cfg.encoder = "jpeg"
		log.Print("[lan][协商] 主控没有 FFmpeg，选择区域 JPEG，无需外部解码器")
	}
	s.profile, err = selectProfile(ctx, s.cfg)
	if err != nil {
		_ = w.send(message{Type: "error", Text: err.Error()}, nil)
		return
	}
	if m.Quality > 0 {
		cq := s.cfg.h264Quality
		if cq == 0 {
			cq = 14
		}
		log.Printf("[lan][画质] requested q=%d | JPEG Q%d | NVENC CQ / x264 CRF %d（H.264 仍为有损）", m.Quality, s.cfg.quality, cq)
	}
	d := s.cfg.area
	reply := message{Type: "welcome", Version: protocolVersion, Width: d.width, Height: d.height, Left: d.left, Top: d.top, FPS: s.cfg.fps, Quality: s.cfg.quality, Bitrate: s.cfg.bitrate, Codec: s.profile.codec, Backend: s.profile.capture + "/" + s.profile.name, UDPPort: s.udp.LocalAddr().(*net.UDPAddr).Port, Text: s.token}
	if s.screenSwitch {
		reply.Features = []string{screenSwitchFeature}
		reply.ScreenGeneration = s.screenGeneration
		descriptor := s.describeScreen(s.cfg, s.profile, s.screenGeneration, 0)
		reply.Monitor, reply.MonitorCount = descriptor.Monitor, descriptor.MonitorCount
	}
	if s.warmIdle {
		reply.Features = append(reply.Features, backgroundFeatureFor(m.Features))
	}
	if s.directories != nil {
		reply.Features = append(reply.Features, directoryFeature)
	}
	if hasFeature(m.Features, repairResyncFeature) {
		reply.Features = append(reply.Features, repairResyncFeature)
	}
	if err := w.send(reply, nil); err != nil {
		return
	}
	go s.readUDP(c.RemoteAddr().(*net.TCPAddr).IP)
	start, _, err := w.read()
	if err != nil || start.Type != "start" {
		return
	}
	s.tcp.Store(start.Transport == "tcp")
	select {
	case <-s.videoReady:
	case <-time.After(5 * time.Second):
		return
	case <-ctx.Done():
		return
	}
	log.Printf("[lan] connected transport=%s encoder=%s", start.Transport, s.profile.name)
	log.Printf("[lan][send] queue-grace=%t udp-smoothing=%t（画质/FPS 不变）", s.queueGrace, s.udpSmoothing)
	if s.cfg.source == "desktop" {
		go s.cursorLoop()
	}
	if refine && s.cfg.source == "desktop" {
		go s.refineLoop()
	}
	go s.stream()
	if s.warmIdle {
		go s.videoKeepaliveLoop()
	}
	if s.screenSwitch && (s.cfg.source == "desktop" || s.screenList != nil) {
		go s.watchDisplays()
	}
	go s.stats()
	if s.sendMetrics != nil {
		go s.sendMetricsLoop()
	}
	for {
		m, _, err := w.read()
		if err != nil {
			return
		}
		switch m.Type {
		case "refine-resync":
			_, _, generation := s.screenConfig()
			if m.ScreenGeneration == generation {
				s.refineResumes.Add(1)
				select {
				case s.refineWake <- struct{}{}:
				default:
				}
			}
		case "viewer-state":
			if s.warmIdle && m.Minimized != nil {
				s.setViewerMinimized(*m.Minimized)
			}
		case "monitor-next":
			if s.screenSwitch {
				s.nextMonitor()
			}
		case "screen-ready":
			_, _, generation := s.screenConfig()
			if m.ScreenGeneration == generation {
				s.screenReady.Store(generation)
			}
		case "input":
			if err := s.applyRemoteInput(m); err != nil {
				log.Printf("[lan][input] %v", err)
			}
		case "nack":
			s.retransmit(m)
		case "keyframe":
			s.requestRepair(s.epoch.Load(), true)
		case "transport":
			if m.Transport == "tcp" && !s.tcp.Swap(true) {
				log.Print("[lan] UDP timed out; video downgraded to TCP")
				s.requestRestart()
			}
		case "feedback":
			if m.Dropped > 3 && m.Dropped > m.Received/20 {
				current := s.bitrate.Load()
				next := current * 85 / 100
				if next < 4000 {
					next = 4000
				}
				if next < current {
					s.bitrate.Store(next)
					log.Printf("[lan][rate] congestion: %d -> %d kbps", current, next)
					s.requestRestart()
				} else {
					s.requestRepair(s.epoch.Load(), true)
				}
			}
		case "ping":
			_ = w.send(message{Type: "pong", Frame: m.Frame}, nil)
		case "options":
			if m.InputEnabled != nil {
				s.inputDisabled.Store(!*m.InputEnabled)
				if !*m.InputEnabled {
					s.releaseInputs()
				}
			}
			if m.ClipboardEnabled != nil {
				s.clipboardGate.Lock()
				s.clipboardDisabled.Store(!*m.ClipboardEnabled)
				s.clipboardGate.Unlock()
			}
			if m.CursorEnabled != nil && s.cursorHidden.Swap(!*m.CursorEnabled) != !*m.CursorEnabled {
				s.refineClear.Add(1)
				s.requestRestart()
			}
		}
	}
}
func (h *host) attach(c *tls.Conn, w *wire, m message) {
	h.mu.Lock()
	s := h.session
	h.mu.Unlock()
	if s == nil || !checkProof(m.Proof, mustProof(c, s.token, "video")) {
		return
	}
	s.mu.Lock()
	if s.video != nil {
		s.mu.Unlock()
		return
	}
	s.video = w
	close(s.videoReady)
	s.mu.Unlock()
	_ = c.SetDeadline(time.Time{})
	_ = w.send(message{Type: "attached"}, nil)
	<-s.ctx.Done()
}
func mustProof(c *tls.Conn, code, role string) string { p, _ := channelProof(c, code, role); return p }
func (s *hostSession) readUDP(ip net.IP) {
	b := make([]byte, packetSize+1)
	for {
		n, addr, err := s.udp.ReadFromUDP(b)
		if err != nil {
			return
		}
		if !addr.IP.Equal(ip) {
			continue
		}
		f, err := s.rx.open(b[:n])
		if err != nil || f.kind != packetProbe {
			continue
		}
		s.mu.Lock()
		s.peer = addr
		s.mu.Unlock()
		ack := s.tx.seal(fragment{kind: packetProbeAck})
		s.writeAuxUDP(ack, addr, false)
	}
}
func (s *hostSession) requestRestart() {
	s.recovery.change()
	s.signalRestart()
}
func (s *hostSession) requestRepair(epoch uint64, external bool) {
	if s.recovery.repair(epoch, external) {
		s.sendMetrics.add(sendRepair)
		s.signalRestart()
	}
}
func (s *hostSession) signalRestart() {
	select {
	case s.restart <- struct{}{}:
	default:
	}
}

func (s *hostSession) switchToTCP(reason string) {
	if !s.tcp.Swap(true) {
		s.sendMetrics.add(sendUDPFallback)
		log.Printf("[lan] video fallback to TCP: %s", reason)
		_ = s.ctrl.send(message{Type: "transport", Transport: "tcp"}, nil)
		s.requestRestart()
	}
}
func (s *hostSession) stream() {
	frames := make(chan videoFrame, 2)
	go func() {
		if err := s.sendFrames(frames); err != nil && s.ctx.Err() == nil {
			log.Printf("[lan][send] %v", err)
			s.cancel()
		}
	}()
	defer close(frames)
	var backoff encoderBackoff
	initial, _, _ := s.screenConfig()
	dynamicDisplay := s.screenSwitch && (s.screenList != nil || initial.source == "desktop")
	for s.ctx.Err() == nil {
		if dynamicDisplay {
			// Capture can fail before the periodic OS poll notices a mode change.
			// Re-resolve before retrying, rather than restarting stale dimensions.
			_, _ = s.refreshDisplay()
		}
		epoch := s.epoch.Add(1)
		generation := s.recovery.begin(epoch)
		cfg, profile, screenGeneration := s.screenConfig()
		cfg.hideCursor = s.cursorHidden.Load()
		cfg.bitrate = int(s.bitrate.Load())
		if s.screenSwitch {
			desc := s.describeScreen(cfg, profile, screenGeneration, epoch)
			if err := s.video.send(desc, nil); err != nil {
				s.cancel()
				return
			}
		}
		if profile.codec == codecH264 {
			if profile.packetized {
				profile.args = packetProfileArgs(cfg, profile.name, profile.capture)
			} else {
				profile.args = profileArgs(cfg, profile.name, profile.capture)
			}
			if profile.path != cfg.ffmpeg {
				profile.args = compatibleArgs(profile.args)
			}
		}
		ctx, cancel := context.WithCancel(s.ctx)
		started := time.Now()
		s.sendMetrics.add(sendEncoderStart)
		finished := make(chan error, 1)
		go func() {
			var lastOutput time.Time
			finished <- runEncoder(ctx, cfg, profile, func(data []byte, key bool) error {
				if key && profile.codec == codecH264 && !encodedScreenMatches(data, cfg.area) {
					return errors.New("capture resolution changed; waiting for display refresh")
				}
				if len(data) > maxVideoFrame {
					return errors.New("encoded frame too large")
				}
				f := videoFrame{id: s.frameID.Add(1), epoch: epoch, codec: profile.codec, key: key, data: data, at: time.Now()}
				if !lastOutput.IsZero() {
					s.sendMetrics.observe(0, f.at.Sub(lastOutput))
				}
				lastOutput = f.at
				grace := time.Duration(0)
				if s.queueGrace {
					grace = videoQueueGrace
				}
				return queueVideoFrame(ctx, frames, f, grace, s.sendMetrics)
			})
		}()
		var delay time.Duration
	waitEncoder:
		for {
			select {
			case <-s.ctx.Done():
				s.recovery.retire(epoch)
				cancel()
				<-finished
				return
			case <-s.restart:
				if !s.recovery.needed(epoch, generation) {
					continue // Stale wake-up, not a new reason to restart.
				}
				s.recovery.retire(epoch)
				cancel()
				<-finished
				break waitEncoder
			case err := <-finished:
				s.recovery.retire(epoch)
				if s.ctx.Err() == nil {
					log.Printf("[lan][encoder] restarting: %v", err)
				}
				delay = backoff.failure(time.Since(started))
				break waitEncoder
			}
		}
		cancel()
		if delay > 0 {
			select {
			case <-s.ctx.Done():
				return
			case <-time.After(delay):
			}
		}
	}
}
func (s *hostSession) sendFrames(frames <-chan videoFrame) error {
	var nacks <-chan message
	if s.udpSmoothing {
		nacks = s.initNACKQueue()
	}
	for {
		if err := s.ctx.Err(); err != nil {
			return err
		}
		var f videoFrame
		var ok bool
		// Prefer available video over a flood of loss notifications. Repairs
		// still run between UDP batches and while the video/JPEG source is idle.
		select {
		case f, ok = <-frames:
		default:
			select {
			case f, ok = <-frames:
			case m := <-nacks:
				s.sendQueuedNACK(m, time.Time{})
				continue
			case <-s.ctx.Done():
				return s.ctx.Err()
			}
		}
		if !ok {
			return nil
		}
		if f.epoch != s.epoch.Load() {
			continue
		}
		age := time.Since(f.at)
		s.sendMetrics.observe(2, age)
		if age > 100*time.Millisecond {
			s.sendMetrics.add(sendAgedFrame)
			s.requestRepair(f.epoch, false)
			continue
		}
		started := time.Now()
		if s.tcp.Load() {
			if err := s.video.send(message{Type: "video", Frame: f.id, Epoch: f.epoch, Codec: f.codec, Key: f.key}, f.data); err != nil {
				return err
			}
		} else {
			packets := splitFrame(s.tx, f)
			s.mu.Lock()
			addr := s.peer
			now := time.Now()
			for id, c := range s.cache {
				if now.Sub(c.at) > 120*time.Millisecond {
					s.cacheBytes -= c.bytes
					delete(s.cache, id)
				}
			}
			if s.cacheBytes+len(f.data) < 16<<20 {
				s.cache[f.id] = cachedFrame{packets: packets, at: now, bytes: len(f.data)}
				s.cacheBytes += len(f.data)
			}
			s.mu.Unlock()
			if addr == nil {
				s.switchToTCP("UDP peer unavailable")
				continue
			}
			err := sendUDPFrame(s.ctx, s.udpOutput(), &s.udpSender, packets, addr, s.udpSmoothing, s.sendMetrics, func(deadline time.Time) {
				select {
				case m := <-nacks:
					s.sendQueuedNACK(m, deadline)
				default:
				}
			})
			if err != nil {
				if s.ctx.Err() != nil {
					return s.ctx.Err()
				}
				s.sendMetrics.observe(3, time.Since(started))
				s.switchToTCP(err.Error())
				continue
			}
		}
		s.sendMetrics.observe(3, time.Since(started))
		if f.key {
			s.recovery.sentKey(f.epoch)
		}
		s.lastSentFrame.Store(f.id)
		s.sent.Add(1)
		s.bytes.Add(uint64(len(f.data)))
	}
}
func (s *hostSession) retransmit(m message) {
	if s.tcp.Load() || len(m.Missing) > 256 {
		return
	}
	if s.udpSmoothing {
		select {
		case s.initNACKQueue() <- m:
		default:
			s.sendMetrics.add(sendNACKDropped)
		}
		return
	}
	s.mu.Lock()
	c, ok := s.cache[m.Frame]
	addr := s.peer
	s.mu.Unlock()
	if !ok || addr == nil || time.Since(c.at) > 100*time.Millisecond {
		return
	}
	for _, i := range m.Missing {
		if int(i) < len(c.packets) {
			_, _ = s.udp.WriteToUDP(c.packets[i], addr)
		}
	}
}
func (s *hostSession) stats() {
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	var frames, bytes uint64
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-tick.C:
			n, b := s.sent.Load(), s.bytes.Load()
			transport := "udp"
			if s.tcp.Load() {
				transport = "tcp"
			}
			_, profile, _ := s.screenConfig()
			log.Printf("[lan][tx] transport=%s fps=%.1f rate=%.2f MB/s encoder=%s", transport, float64(n-frames)/2, float64(b-bytes)/2/1024/1024, profile.name)
			frames, bytes = n, b
		}
	}
}

func (s *hostSession) refineLoop() {
	s.regionalRefineLoop()
}
