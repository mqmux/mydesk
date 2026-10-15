//go:build windows && amd64

package lan

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"image/jpeg"
	"image/png"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type controllerSession struct {
	ctx                                                               context.Context
	cancel                                                            context.CancelFunc
	ctrl, video                                                       *wire
	udp                                                               *net.UDPConn
	tx, rx                                                            *packetCipher
	view                                                              *viewer
	welcome                                                           message
	ffmpeg                                                            string
	software                                                          bool
	decodeOutput                                                      string
	decoder                                                           string
	activeDecoder                                                     atomic.Uint32 // 1 MF, 2 FFmpeg; diagnostics/tests, not advertised hardware capability.
	transport                                                         atomic.Bool
	lastUDP, received, dropped, decoded                               atomic.Uint64
	frames                                                            chan videoFrame
	commands                                                          chan message
	lastKeyRequest                                                    atomic.Int64
	decodePressure                                                    atomic.Uint64
	pressureWindow                                                    decoderPressureWindow
	warmupUntil                                                       atomic.Int64
	auxMu                                                             sync.Mutex
	auxMeters                                                         map[*measuredConn]bool
	auxRx, auxTx                                                      uint64
	udpRx, udpTx                                                      atomic.Uint64
	rttMicros                                                         atomic.Int64
	clip                                                              clipboardAccess
	filesMu                                                           sync.Mutex
	files                                                             *fileManager
	downloadDir                                                       string
	command                                                           *commandDialog
	inputDisabled, clipboardDisabled, cursorHidden, localCursorHidden atomic.Bool
	clipboardGate                                                     sync.Mutex
	clipboardCancel                                                   context.CancelFunc
	noticeMu                                                          sync.Mutex
	transferNotice, clipboardNotice                                   string
	screenMu                                                          sync.Mutex
	screen                                                            message
	epochScreens                                                      map[uint64]message
	screenChanged                                                     chan struct{}
	screenLatest                                                      uint64
	readyScreen                                                       atomic.Uint64
	monitorPending                                                    atomic.Bool
	pendingFrom                                                       atomic.Uint64
	monitorStarted                                                    atomic.Int64
	monitorNotice                                                     string
	viewerMinimized                                                   atomic.Bool
	directoryMu, directorySaveMu                                      sync.Mutex
	commandDirectory, savedCommandDirectory                           string
	gpuDecode                                                         string
	pipeline                                                          *pipelineMetrics
	gpuPictures                                                       atomic.Uint64
}

func ControllerMain() {
	defer setupLogging("controller")()
	configureDPI()
	address := flag.String("connect", "", "client IP[:port]; empty automatically discovers and matches the pairing key")
	code := flag.String("code", "", "pairing code shown on client")
	port := flag.Int("port", defaultPort, "discovery/default control port")
	fps := flag.Int("fps", maxFPS, "requested FPS (1-120)")
	quality := flag.Int("q", 0, "quality override; 100 = JPEG Q100 / NVENC CQ8 / x264 CRF8; 0 keeps defaults")
	windowW := flag.Int("w", 0, "initial window content width; 0 native desktop size")
	windowH := flag.Int("H", 0, "initial window content height; 0 native desktop size")
	stretch := flag.String("r", "sharp", "display scaling: sharp/pixel (crisp) or smooth")
	ff := flag.String("ffmpeg", "", "path to ffmpeg.exe")
	transport := flag.String("transport", "auto", "auto or tcp (force video fallback)")
	software := flag.Bool("software-decode", false, "force CPU H.264 decoding")
	decoder := flag.String("decoder", "auto", "H.264 decoder: auto (Windows MF then FFmpeg), mf or ffmpeg")
	decodeOutput := flag.String("decode-output", "auto", "local H.264 output: auto (NV12 GPU, BGRA fallback), nv12 or bgra (4.6 comparison)")
	gpuDecode := flag.String("gpu-decode", "auto", "native GPU texture output: auto/on/off; off retains CPU-NV12 readback")
	renderer := flag.String("renderer", "auto", "display backend: auto (D3D11 GPU, compatibility fallback) or d2d (previous CPU sharp path)")
	renderStats := flag.Bool("render-stats", false, "log display upload/submission and local frame age every 5 seconds")
	latencyStats := flag.Bool("latency-stats", false, "log bounded local pipeline p50/p95/p99 every 5 seconds; not input-to-photon latency")
	list := flag.Bool("list", false, "discover devices and exit")
	seconds := flag.Int("test-seconds", 0, "headless receive/decode test duration; no input injection")
	iface := flag.String("interface", "", "local interface name or IPv4; default prefers physical LAN interfaces")
	interfaces := flag.Bool("list-interfaces", false, "list usable IPv4 interfaces and exit")
	firewall := flag.String("firewall", "", "setup or remove application-scoped LAN firewall rules (confirmation/UAC required)")
	version := flag.Bool("version", false, "print LAN build/protocol version and exit")
	flag.Parse()
	if *gpuDecode != "auto" && *gpuDecode != "on" && *gpuDecode != "off" {
		log.Fatal("gpu-decode must be auto/on/off")
	}
	if *version {
		fmt.Printf("MyDesk LAN controller %s / protocol v%d\n", controllerBuildVersion, protocolVersion)
		return
	}
	if *firewall != "" {
		if err := firewallAction("controller", *firewall); err != nil {
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
	if *ff != "" && findFFmpeg(*ff) == "" {
		log.Fatal("specified ffmpeg executable does not exist")
	}
	if *fps < 1 || *fps > maxFPS || *port < 1 || *port > 65535 || *quality < 0 || *quality > 100 || *windowW < 0 || *windowH < 0 || *windowW > 16384 || *windowH > 16384 || (*stretch != "sharp" && *stretch != "pixel" && *stretch != "smooth") {
		log.Fatal("invalid fps/port/quality/window/scaling setting")
	}
	if *transport != "auto" && *transport != "tcp" {
		log.Fatal("transport must be auto or tcp")
	}
	if *renderer != "auto" && *renderer != "d2d" {
		log.Fatal("renderer must be auto or d2d")
	}
	if *decodeOutput != "auto" && *decodeOutput != "nv12" && *decodeOutput != "bgra" {
		log.Fatal("decode-output must be auto, nv12 or bgra")
	}
	if *decoder != "auto" && *decoder != "mf" && *decoder != "ffmpeg" {
		log.Fatal("decoder must be auto, mf or ffmpeg")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	log.Printf("MyDesk LAN controller %s / protocol v%d", controllerBuildVersion, protocolVersion)
	defer stop()
	reader := bufio.NewReader(os.Stdin)
	if *list {
		log.Print("[lan][发现] 正在通过本地网卡搜索设备…")
		devices, err := discoverOn(ctx, *port, localIP)
		if err != nil {
			log.Fatal(err)
		}
		for i, d := range devices {
			fmt.Printf("%d. %s  %s:%d\n", i+1, d.Name, d.IP, d.Port)
		}
		return
	}
	addressValue, selectedCode, err := selectControllerTarget(ctx, reader, os.Stdout, *address, *code, *port, func(ctx context.Context, code string) ([]keyMatchDevice, keyMatchReport, error) {
		devices, err := discoverOn(ctx, *port, localIP)
		if err != nil {
			return nil, keyMatchReport{}, err
		}
		matches, report := findKeyMatches(ctx, devices, func(ctx context.Context, address string) (keyMatchProbe, error) {
			return probeKeyMatch(ctx, address, code, localIP)
		})
		return matches, report, nil
	})
	if err != nil {
		if errors.Is(err, errTargetSelectionCanceled) {
			return
		}
		log.Fatal(err)
	}
	sessionCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	decodeCtx := context.WithValue(sessionCtx, decoderBackendKey{}, *decoder)
	s, err := connectController(context.WithValue(decodeCtx, jpegQualityKey{}, *quality), cancel, addressValue, selectedCode, *fps, findFFmpeg(*ff), *software, localIP)
	if err != nil {
		if *seconds == 0 {
			showConnectionError(err)
		}
		log.Fatal(err)
	}
	defer s.close()
	s.decodeOutput = *decodeOutput
	s.gpuDecode = *gpuDecode
	if *latencyStats {
		s.pipeline = &pipelineMetrics{}
	}
	s.view = &viewer{width: s.welcome.Width, height: s.welcome.Height, screenGeneration: s.welcome.ScreenGeneration, windowWidth: *windowW, windowHeight: *windowH, smooth: *stretch == "smooth", pixel: *stretch == "pixel", renderBackend: *renderer, renderStats: *renderStats, onFiles: s.openFileManager, onCommand: s.openCommandDialog, onCancelTransfers: s.cancelTransfers, onMonitor: s.nextScreen, onInput: func(e inputEvent) {
		s.enqueueInput(e)
		if e.Kind != "move" {
			// Source-side tile invalidation retains unrelated lossless regions.
		}
	}}
	s.view.pipeline = s.pipeline
	if err := s.start(*transport == "tcp", *seconds == 0); err != nil {
		if *seconds == 0 {
			showConnectionError(err)
		}
		log.Fatal(err)
	}
	log.Printf("[lan] %dx%d target=%d backend=%s | Mode: Func | F6 screens F7 settings F8 files F10 cancel | hold F9 3s for Key mode", s.welcome.Width, s.welcome.Height, s.welcome.FPS, s.welcome.Backend)
	if *seconds > 0 {
		select {
		case <-time.After(time.Duration(*seconds) * time.Second):
		case <-s.ctx.Done():
		}
		log.Printf("[lan][test] received=%d decoded=%d dropped=%d", s.received.Load(), s.decoded.Load(), s.dropped.Load())
		return
	}
	go func() {
		<-s.ctx.Done()
		s.view.mu.Lock()
		h := s.view.hwnd
		s.view.mu.Unlock()
		if h != 0 {
			ucall("PostMessageW", h, 0x10, 0, 0)
		}
	}()
	if err := s.view.run(); err != nil {
		log.Printf("[lan][主控] 显示窗口错误：%v", err)
	}
	log.Print("[lan][主控] 画面窗口已关闭，结束会话")
}
func connectController(ctx context.Context, cancel context.CancelFunc, addr, code string, fps int, ffmpeg string, software bool, local ...net.IP) (_ *controllerSession, resultErr error) {
	stage := "TCP/TLS连接"
	defer func() {
		if resultErr != nil {
			resultErr = networkDiagnostic(stage, addr, resultErr)
		}
	}()
	log.Printf("[lan][连接] 尝试 %s（物理局域网优先；TCP 配对 -> UDP 视频探测）", addr)
	c, err := dialTLS(ctx, addr, local...)
	if err != nil {
		return nil, err
	}
	ok := false
	stage = "配对"
	defer func() {
		if !ok {
			c.Close()
		}
	}()
	_ = c.SetDeadline(time.Now().Add(45 * time.Second))
	w := &wire{conn: c}
	proof, err := channelProof(c, code, "controller")
	if err != nil {
		return nil, err
	}
	quality, _ := ctx.Value(jpegQualityKey{}).(int)
	stage = "解码器探测"
	requestedDecoder, capabilities, err := controllerDecoderBackend(ctx, ffmpeg)
	if err != nil {
		return nil, err
	}
	stage = "配对"
	if err := w.send(message{Type: "pair", Version: protocolVersion, Proof: proof, FPS: fps, Quality: quality, Backend: capabilities, Features: controllerSessionFeatures()}, nil); err != nil {
		return nil, err
	}
	m, _, err := w.read()
	if err != nil {
		return nil, err
	}
	if m.Type == "error" {
		return nil, errors.New(m.Text)
	}
	if m.Type != "paired" || !checkProof(m.Proof, mustProof(c, code, "client")) {
		return nil, errors.New("client pairing proof invalid")
	}
	log.Print("[lan][配对] 成功；等待被控端选择采集/编码器…")
	stage = "编码协商"
	m, _, err = w.read()
	if err != nil {
		return nil, err
	}
	if m.Type == "error" {
		return nil, errors.New(m.Text)
	}
	if m.Type != "welcome" || m.Version != protocolVersion || m.Width < 1 || m.Height < 1 || m.Width*m.Height > 16<<20 || m.UDPPort < 1 || m.UDPPort > 65535 || (m.Codec != codecH264 && m.Codec != codecJPEG && m.Codec != codecJPEGDelta) {
		return nil, errors.New("invalid welcome")
	}
	if m.Codec == codecH264 && !strings.Contains(capabilities, "h264") {
		return nil, errors.New("H.264 decoder unavailable: Windows MF or ffmpeg.exe is required")
	}
	_ = c.SetDeadline(time.Time{})
	// At 120 FPS, 16 pictures cover only 133 ms, shorter than some D3D11VA
	// process initializations. Extra capacity is startup-only; deliver still
	// limits normal streaming to four queued pictures.
	s := &controllerSession{ctx: ctx, cancel: cancel, ctrl: w, welcome: m, ffmpeg: ffmpeg, software: software, decoder: requestedDecoder, frames: make(chan videoFrame, 32), commands: make(chan message, 256)}
	s.cursorHidden.Store(true)
	s.tx, s.rx, err = sessionCiphers(c, false)
	if err != nil {
		return nil, err
	}
	boundIP := c.LocalAddr().(*net.TCPAddr).IP
	s.udp, err = net.ListenUDP("udp4", &net.UDPAddr{IP: boundIP})
	if err != nil {
		return nil, err
	}
	_ = s.udp.SetReadBuffer(8 << 20)
	_ = s.udp.SetWriteBuffer(1 << 20)
	stage = "视频连接"
	video, err := dialTLS(ctx, c.RemoteAddr().String(), boundIP)
	if err != nil {
		s.udp.Close()
		return nil, err
	}
	s.video = &wire{conn: video}
	_ = video.SetDeadline(time.Now().Add(5 * time.Second))
	if err := s.video.send(message{Type: "attach", Proof: mustProof(video, m.Text, "video")}, nil); err != nil {
		s.close()
		return nil, err
	}
	attached, _, err := s.video.read()
	if err != nil || attached.Type != "attached" {
		s.close()
		return nil, errors.New("video connection authentication failed")
	}
	_ = video.SetDeadline(time.Time{})
	ok = true
	return s, nil
}
func (s *controllerSession) start(forceTCP, clipboard bool) error {
	if s.view != nil {
		s.view.mu.Lock()
		// Establish the initial generation before starting UI/decoder workers;
		// the first descriptor is not a monitor change or a GPU reset.
		s.view.screenGeneration = s.welcome.ScreenGeneration
		s.view.onMinimized = s.setViewerMinimized
		s.view.mu.Unlock()
	}
	remote := s.ctrl.conn.RemoteAddr().(*net.TCPAddr)
	addr := &net.UDPAddr{IP: remote.IP, Port: s.welcome.UDPPort}
	if !forceTCP {
		log.Print("[lan][UDP探测] TCP 配对已完成；正在检查 UDP 视频通道…")
		deadline := time.Now().Add(1600 * time.Millisecond)
		b := make([]byte, packetSize+1)
		for time.Now().Before(deadline) {
			probe := s.tx.seal(fragment{kind: packetProbe})
			sent, _ := s.udp.WriteToUDP(probe, addr)
			s.udpTx.Add(uint64(sent))
			_ = s.udp.SetReadDeadline(time.Now().Add(180 * time.Millisecond))
			n, peer, err := s.udp.ReadFromUDP(b)
			if err != nil {
				continue
			}
			if !peer.IP.Equal(addr.IP) || peer.Port != addr.Port {
				continue
			}
			f, err := s.rx.open(b[:n])
			s.udpRx.Add(uint64(n))
			if err == nil && f.kind == packetProbeAck {
				s.lastUDP.Store(uint64(time.Now().UnixNano()))
				break
			}
		}
	}
	tcp := forceTCP || s.lastUDP.Load() == 0
	if forceTCP {
		log.Print("[lan][传输] 已按参数强制使用 TCP 视频（仍保留原编码器）")
	} else if tcp {
		log.Print("[lan][UDP探测] 未收到响应，已自动降级 TCP；检查两端 UDP 防火墙规则/VPN 策略")
	} else {
		log.Printf("[lan][UDP探测] 成功 %s -> %s", s.udp.LocalAddr(), addr)
	}
	s.transport.Store(tcp)
	mode := "udp"
	if tcp {
		mode = "tcp"
	}
	if err := s.ctrl.send(message{Type: "start", Transport: mode}, nil); err != nil {
		return err
	}
	log.Printf("[lan][transport] %s", mode)
	go s.sendCommands()
	go s.readControl()
	go s.readVideo()
	go s.readUDP(addr)
	go s.decode()
	go s.stats()
	if clipboard {
		go s.imageClipboardLoop()
	}
	go func() { <-s.ctx.Done(); s.ctrl.conn.Close(); s.video.conn.Close(); s.udp.Close() }()
	return nil
}
func (s *controllerSession) close() {
	// Flush an edited F7 path before canceling the session/auxiliary transports.
	if s.ctx != nil && s.ctx.Err() == nil {
		ctx, stop := context.WithTimeout(s.ctx, time.Second)
		s.flushCommandDirectory(ctx)
		stop()
	}
	s.cancel()
	if s.view != nil {
		s.view.mu.Lock()
		s.view.closed = true
		old := s.view.frames.latest
		s.view.frames.latest = nil
		if old != nil && old.readers == 0 {
			s.view.frames.recycleFrame(old)
		}
		s.view.mu.Unlock()
	}
	if s.ctrl != nil {
		s.ctrl.conn.Close()
	}
	if s.video != nil {
		s.video.conn.Close()
	}
	if s.udp != nil {
		s.udp.Close()
	}
}
func (s *controllerSession) enqueue(m message) {
	if s.pipeline != nil && m.Type == "input" {
		m.queued = time.Now()
	}
	select {
	case s.commands <- m:
	case <-s.ctx.Done():
	default:
		if m.Type == "input" && m.Input.Kind != "move" {
			log.Print("[lan][input] queue overflow; resetting remote keys")
			s.cancel()
		}
	}
}
func (s *controllerSession) sendCommands() {
	for {
		select {
		case <-s.ctx.Done():
			return
		case m := <-s.commands:
			if !m.queued.IsZero() {
				s.pipeline.observe(0, time.Since(m.queued))
			}
			if err := s.ctrl.send(m, nil); err != nil {
				s.cancel()
				return
			}
		}
	}
}
func (s *controllerSession) readControl() {
	hiddenCursorNoticed := false
	for {
		m, _, err := s.ctrl.read()
		if err != nil {
			if s.ctx.Err() == nil {
				log.Printf("[lan][断开] 控制通道：%v", err)
			}
			s.cancel()
			return
		}
		if m.Type == "pong" {
			delay := time.Now().UnixNano() - int64(m.Frame)
			if delay > 0 && delay < int64(10*time.Second) {
				s.rttMicros.Store(delay / 1000)
			}
		}
		if m.Type == "cursor-state" && m.CursorVisible != nil {
			s.view.setCursorState(m.CursorKind, *m.CursorVisible)
			if !*m.CursorVisible && !s.localCursorHidden.Load() && !hiddenCursorNoticed {
				hiddenCursorNoticed = true
				log.Print("[lan][鼠标] 被控上报鼠标不可见（可能无实体鼠标或系统隐藏）；主控按 F7 设置继续显示指针")
			}
		}
		if m.Type == "screen-status" {
			s.monitorPending.Store(false)
			s.setMonitorNotice(m.Text)
		}
		if m.Type == "transport" && m.Transport == "tcp" {
			s.transport.Store(true)
			log.Print("[lan] client switched video to TCP")
		}
	}
}
func (s *controllerSession) readVideo() {
	for {
		m, b, err := s.video.read()
		if err != nil {
			if s.ctx.Err() == nil {
				log.Printf("[lan][断开] 视频通道：%v", err)
			}
			s.cancel()
			return
		}
		switch m.Type {
		case "screen":
			if !hasFeature(s.welcome.Features, screenSwitchFeature) || !validScreenDescriptor(m) {
				s.cancel()
				return
			}
			s.registerScreen(m)
		case "refine-patch", "refine-clear-patch":
			screen := s.currentScreen()
			if m.ScreenGeneration != screen.ScreenGeneration || !validRefineRect(m, screen.Width, screen.Height) {
				continue
			}
			var pixels []byte
			if m.Type == "refine-patch" {
				cfg, err := png.DecodeConfig(bytes.NewReader(b))
				if err != nil || cfg.Width != m.Width || cfg.Height != m.Height {
					continue
				}
				img, err := png.Decode(bytes.NewReader(b))
				if err != nil {
					continue
				}
				pixels = imageBGRA(img, cfg.Width, cfg.Height)
			}
			s.view.setRefinePatch(m, pixels)
		case "video":
			s.deliver(videoFrame{id: m.Frame, epoch: m.Epoch, codec: m.Codec, key: m.Key, data: b, at: time.Now()})
		case "refine":
			screen := s.currentScreen()
			cfg, err := png.DecodeConfig(bytes.NewReader(b))
			if err != nil || cfg.Width != screen.Width || cfg.Height != screen.Height {
				continue
			}
			img, err := png.Decode(bytes.NewReader(b))
			if err == nil {
				s.view.setRefine(imageBGRA(img, cfg.Width, cfg.Height))
			}
		case "refine-clear":
			s.view.setRefine(nil)
		}
	}
}
func (s *controllerSession) requestKey() {
	now := time.Now().UnixNano()
	old := s.lastKeyRequest.Load()
	if now-old < int64(500*time.Millisecond) {
		return
	}
	if s.lastKeyRequest.CompareAndSwap(old, now) {
		s.enqueue(message{Type: "keyframe"})
	}
}
func (s *controllerSession) deliver(f videoFrame) {
	// GPU decoder initialization can take ~200 ms. Give only that startup
	// phase a larger window; normal streaming keeps at most four queued frames.
	if s.decoded.Load() > 0 && time.Now().UnixNano() > s.warmupUntil.Load() && len(s.frames) >= 4 {
		s.decodePressure.Add(1)
		s.pressureWindow.record(time.Now())
		s.dropped.Add(1)
		s.requestKey()
		return
	}
	select {
	case s.frames <- f:
		s.received.Add(1)
	default:
		s.decodePressure.Add(1)
		s.pressureWindow.record(time.Now())
		s.dropped.Add(1)
		s.requestKey()
	}
}
func (s *controllerSession) readUDP(addr *net.UDPAddr) {
	a := newAssembler()
	activity := udpActivity{lastPacket: time.Unix(0, int64(s.lastUDP.Load()))}
	if s.lastUDP.Load() == 0 {
		activity.lastPacket = time.Time{}
	}
	b := make([]byte, packetSize+1)
	for {
		if s.transport.Load() || s.ctx.Err() != nil {
			return
		}
		_ = s.udp.SetReadDeadline(time.Now().Add(udpReadInterval(len(a.frames) > 0, s.viewerMinimized.Load())))
		n, peer, err := s.udp.ReadFromUDP(b)
		now := time.Now()
		if s.transport.Load() && len(a.frames) > 0 {
			a = newAssembler()
		}
		if err == nil && peer.IP.Equal(addr.IP) && peer.Port == addr.Port && !s.transport.Load() {
			s.udpRx.Add(uint64(n))
			f, e := s.rx.open(b[:n])
			if e == nil && f.kind == packetVideo {
				s.lastUDP.Store(uint64(now.UnixNano()))
				activity.picture(f.frame, now)
				complete, e := a.add(f, now)
				if e == nil && complete != nil {
					s.deliver(*complete)
				}
			} else if e == nil && f.kind == packetKeepalive && backgroundFeatureFor(s.welcome.Features) != "" && len(f.payload) == 0 && f.total == 0 && f.index == 0 {
				s.lastUDP.Store(uint64(now.UnixNano()))
				activity.keepalive(f.frame, now)
			}
		} else if err != nil && s.ctx.Err() != nil {
			return
		}
		requests, expired := a.maintenance(now)
		for _, m := range requests {
			s.enqueue(m)
		}
		if expired > 0 {
			s.dropped.Add(uint64(expired))
			s.requestKey()
		}
		if !s.transport.Load() && activity.stalled(now) {
			if !s.transport.Swap(true) {
				s.enqueue(message{Type: "transport", Transport: "tcp"})
				log.Print("[lan] UDP stalled; switching video to TCP")
			}
		}
	}
}
func (s *controllerSession) decode() {
	decodeContext := s.ctx
	if s.pipeline != nil {
		decodeContext = context.WithValue(s.ctx, decoderObservationKey{}, func(duration time.Duration) { s.pipeline.observe(2, duration) })
	}
	order := newFrameOrder()
	var decoder pictureDecoder
	var epoch uint64
	var jpegCanvas []byte
	screen := s.currentScreen()
	hardware := !s.software
	backend := "mf"
	if s.decoder == "ffmpeg" || nativeDecoderAvailable() != nil {
		backend = "ffmpeg"
	}
	output := pixelBGRA
	if s.decodeOutput != "bgra" && s.view.renderBackend != "d2d" && pixelNV12.size(screen.Width, screen.Height) != 0 {
		output = pixelNV12
	}
	defer func() {
		if decoder != nil {
			decoder.close()
		}
	}()
	lastProgress := time.Now()
	var framesAtStart uint64
	var nativePressure nativeQueuePressure
	nativePressure.reset(time.Now(), s.decodePressure.Load())
	directAllowed := s.gpuDecode != "" && s.gpuDecode != "off" && !s.software && s.decodeOutput != "bgra" && s.view.renderBackend != "d2d" && (s.gpuDecode == "on" || hasFeature(s.welcome.Features, repairResyncFeature))
	directActive := false
	recoverDecoder := func(reason error) bool {
		if decoder != nil {
			decoder.close()
			decoder = nil
		}
		if s.ctx.Err() != nil {
			return false
		}
		if directActive {
			log.Printf("[lan][decoder] GPU texture route failed: %v; keeping MF with CPU-NV12 compatibility output", reason)
			directAllowed, directActive = false, false
			order.reset()
			s.view.mu.Lock()
			s.view.repairs.reset(s.view.width, s.view.height)
			s.view.mu.Unlock()
			s.enqueue(message{Type: "refine-resync", ScreenGeneration: screen.ScreenGeneration})
			s.requestKey()
			return true
		}
		next, nextHardware, nextOutput, ok := nextDecoder(backend, hardware, output, s.decoder, s.ffmpeg)
		if s.software {
			nextHardware = false // Explicit CPU-only mode also survives backend fallback.
		}
		if !ok {
			log.Printf("[lan][decoder] %s failed: %v; no further decoder fallback (add ffmpeg.exe or use -decoder ffmpeg for diagnosis)", backend, reason)
			s.cancel()
			return false
		}
		log.Printf("[lan][decoder] %s failed: %v; switching to %s (hardware=%t output=%s)", backend, reason, next, nextHardware, nextOutput)
		backend, hardware, output = next, nextHardware, nextOutput
		nativePressure.reset(time.Now(), s.decodePressure.Load())
		order.reset()
		s.requestKey()
		return true
	}
	for {
		select {
		case <-s.ctx.Done():
			return
		case f := <-s.frames:
			if hasFeature(s.welcome.Features, screenSwitchFeature) && f.epoch != screen.Epoch {
				next, ok := s.screenForEpoch(f.epoch)
				if !ok || next.ScreenGeneration < screen.ScreenGeneration || next.Epoch < screen.Epoch {
					continue
				}
				s.view.mu.Lock()
				viewGeneration := s.view.screenGeneration
				s.view.mu.Unlock()
				if next.ScreenGeneration != screen.ScreenGeneration || viewGeneration != next.ScreenGeneration {
					if decoder != nil {
						decoder.close()
						decoder = nil
					}
					order.reset()
					jpegCanvas = nil
					if !s.view.resizeDesktop(s.ctx, next.Width, next.Height, next.ScreenGeneration) {
						return
					}
					if output == pixelNV12 && pixelNV12.size(next.Width, next.Height) == 0 {
						output = pixelBGRA
					}
				}
				screen = next
				s.screenMu.Lock()
				s.screen = next
				s.screenMu.Unlock()
			}
			if f.codec == codecH264 && backend == "mf" && decoder != nil && nativePressure.record(time.Now(), s.decodePressure.Load()) {
				if _, _, _, canRecover := nextDecoder(backend, hardware, output, s.decoder, s.ffmpeg); canRecover {
					if !recoverDecoder(errors.New("repeated recent native decoder queue overflows across recovery epochs")) {
						return
					}
				} else {
					// Explicit/native-only software mode has no faster fallback to
					// try. Do not disconnect a usable stream just for low throughput.
					nativePressure.reset(time.Now(), s.decodePressure.Load())
				}
			}
			if f.codec == codecH264 && output == pixelNV12 && s.view.compatibility.Load() {
				if directActive {
					s.view.mu.Lock()
					s.view.repairs.reset(s.view.width, s.view.height)
					s.view.mu.Unlock()
				}
				directAllowed = false
				directActive = false
				s.enqueue(message{Type: "refine-resync", ScreenGeneration: screen.ScreenGeneration})
				log.Print("[lan][decoder] compatibility renderer; switching output to BGRA")
				output = pixelBGRA
				if decoder != nil {
					decoder.close()
					decoder = nil
				}
				order.reset()
				s.requestKey()
			}
			if f.codec == codecH264 && hardware && decoder != nil && f.epoch == epoch && (s.pressureWindow.slow(time.Now()) || decoder.slow(time.Now())) {
				if !recoverDecoder(errors.New("hardware decode backlog/latency")) {
					return
				}
			}
			if f.codec == codecJPEG {
				if len(order.push(f)) == 0 {
					continue
				}
				cfg, err := jpeg.DecodeConfig(bytes.NewReader(f.data))
				if err != nil || cfg.Width != screen.Width || cfg.Height != screen.Height {
					continue
				}
				img, err := jpeg.Decode(bytes.NewReader(f.data))
				if err == nil {
					s.decoded.Add(1)
					s.view.setFrame(imageBGRA(img, cfg.Width, cfg.Height))
					s.screenPictureReady(screen.ScreenGeneration)
				}
				continue
			}
			if len(order.pending) > 0 {
				oldest := time.Now()
				for _, p := range order.pending {
					if p.at.Before(oldest) {
						oldest = p.at
					}
				}
				if time.Since(oldest) > 100*time.Millisecond {
					order.reset()
					s.requestKey()
				}
			}
			ready := order.push(f)
			if f.codec == codecJPEGDelta && order.needsKey {
				s.requestKey()
			}
			for _, frame := range ready {
				if !frame.at.IsZero() {
					s.pipeline.observe(1, time.Since(frame.at))
				}
				if frame.codec == codecJPEGDelta {
					patches, err := decodeJPEGDelta(frame.data, screen.Width, screen.Height, frame.key)
					if err != nil {
						log.Printf("[lan][jpeg] %v; requesting full resync", err)
						order.reset()
						s.requestKey()
						break
					}
					if jpegCanvas == nil {
						jpegCanvas = make([]byte, screen.Width*screen.Height*4)
					}
					if len(patches) > 0 {
						applyJPEGPatches(jpegCanvas, screen.Width, patches)
						pixels := s.view.buffer()
						copy(pixels, jpegCanvas)
						s.decoded.Add(1)
						s.view.setFrame(pixels)
						s.screenPictureReady(screen.ScreenGeneration)
					}
					continue
				}
				if decoder == nil || epoch != frame.epoch {
					now := time.Now()
					s.warmupUntil.Store(now.Add(500 * time.Millisecond).UnixNano())
					s.pressureWindow.init(now)
					if decoder != nil {
						decoder.close()
					}
					format := output // immutable for this process/callback
					var err error
					generation := screen.ScreenGeneration
					var gpu []func(*gpuDisplayFrame)
					directActive = directAllowed && backend == "mf" && hardware
					if directActive {
						gpu = append(gpu, func(frame *gpuDisplayFrame) {
							s.gpuPictures.Add(1)
							s.decoded.Add(1)
							s.view.setGPUFrame(frame)
							s.screenPictureReady(generation)
						})
					}
					decoder, err = startPictureDecoder(decodeContext, backend, s.ffmpeg, hardware, format, screen.Width, screen.Height, screen.FPS, func() []byte { return s.view.bufferFor(format) }, func(b []byte) { s.decoded.Add(1); s.view.setSurface(b, format); s.screenPictureReady(generation) }, gpu...)
					if err != nil {
						if !recoverDecoder(err) {
							return
						}
						break
					}
					if backend == "mf" {
						s.activeDecoder.Store(1)
					} else {
						s.activeDecoder.Store(2)
					}
					epoch = frame.epoch
					lastProgress = time.Now()
					framesAtStart = s.decoded.Load()
				}
				if err := decoder.failure(); err != nil {
					if !recoverDecoder(err) {
						return
					}
					break
				}
				if err := decoder.writeFrame(frame.data); err != nil {
					if !recoverDecoder(err) {
						return
					}
					break
				}
				if time.Since(lastProgress) > 2*time.Second && s.decoded.Load() == framesAtStart {
					if !recoverDecoder(errors.New("no output for two seconds")) {
						return
					}
					break
				}
			}
		}
	}
}
func (s *controllerSession) stats() {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	var received, displayed, dropped uint64
	var pressure uint64
	rx, tx := s.networkTotals()
	last := time.Now()
	lastRenderLog := last
	lastPipelineLog := last
	for {
		select {
		case <-s.ctx.Done():
			return
		case now := <-tick.C:
			if s.pipeline != nil && now.Sub(lastPipelineLog) >= 5*time.Second {
				if summary := s.pipeline.summary(); summary != "" {
					log.Printf("[lan][latency/local] %s", summary)
				}
				lastPipelineLog = now
			}
			if s.monitorPending.Load() && now.UnixNano()-s.monitorStarted.Load() > int64(20*time.Second) && s.monitorPending.Swap(false) {
				s.setMonitorNotice("切屏未收到新画面，可再按 F6 重试")
			}
			elapsed := now.Sub(last).Seconds()
			last = now
			nrx, ntx := s.networkTotals()
			r, loss := s.received.Load(), s.dropped.Load()
			s.view.mu.Lock()
			p := s.view.displayed
			var render displayMetrics
			backend := s.view.renderName
			if s.view.frames.latest != nil {
				backend += "/" + s.view.frames.latest.format.String()
			}
			if s.view.renderStats && now.Sub(lastRenderLog) >= 5*time.Second {
				render, s.view.renderMetrics = s.view.renderMetrics, displayMetrics{}
				lastRenderLog = now
			}
			s.view.mu.Unlock()
			if render.frames > 0 {
				n := float64(render.frames)
				log.Printf("[lan][render] %s upload=%.2fms submit=%.2fms decoded-age avg=%.2fms max=%.2fms superseded=%d", backend, float64(render.upload)/float64(time.Millisecond)/n, float64(render.submit)/float64(time.Millisecond)/n, float64(render.age)/float64(time.Millisecond)/n, float64(render.peak)/float64(time.Millisecond), render.skipped)
			}
			transport := "UDP"
			if s.transport.Load() {
				transport = "TCP"
			}
			rtt := "--"
			if n := s.rttMicros.Load(); n > 0 {
				rtt = fmt.Sprintf("%.1f ms", float64(n)/1000)
			}
			mode := "控制"
			if s.inputDisabled.Load() {
				mode = "看板"
			}
			keys := "F6切屏 F7设置 F8文件 F10取消 F11全屏 | 长按F9普通按键"
			if s.view.keyMode.Load() {
				keys = "普通按键 | 长按F9功能模式"
			}
			screen := s.currentScreen()
			status := fmt.Sprintf("MyDesk LAN | %s %s | %.1f/%d FPS | ↓ %s ↑ %s | RTT %s | %s %s", transport, screen.Backend, float64(p-displayed)/elapsed, screen.FPS, formatRate(float64(nrx-rx)/elapsed), formatRate(float64(ntx-tx)/elapsed), rtt, mode, keys)
			if screen.MonitorCount > 0 {
				status += fmt.Sprintf(" | 屏幕 %d/%d %dx%d", screen.Monitor+1, screen.MonitorCount, screen.Width, screen.Height)
			}
			s.noticeMu.Lock()
			monitorNotice := s.monitorNotice
			s.noticeMu.Unlock()
			if monitorNotice != "" {
				status += " | " + monitorNotice
			}
			if notice := s.notices(); notice != "" {
				status += " | " + notice
			}
			s.view.setStatus(status)
			currentPressure := s.decodePressure.Load()
			s.enqueue(message{Type: "feedback", Received: r - received, Dropped: networkDrops(loss-dropped, currentPressure-pressure)})
			pressure = currentPressure
			s.enqueue(message{Type: "ping", Frame: uint64(time.Now().UnixNano())})
			// Reassert coalescible UI state in case a busy control queue dropped it.
			s.sendViewerState()
			rx, tx = nrx, ntx
			received, displayed, dropped = r, p, loss
		}
	}
}

func networkDrops(total, decode uint64) uint64 {
	if decode >= total {
		return 0
	}
	return total - decode
}
