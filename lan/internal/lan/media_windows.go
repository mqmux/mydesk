//go:build windows && amd64

package lan

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

type videoConfig struct {
	h264Quality                      int
	hideCursor                       bool
	fps, monitor, bitrate, quality   int
	area                             desktop
	ffmpeg, encoder, capture, source string
	display                          string // Win32 device name, stable across geometry changes.
	// Optional capture injection for deterministic, input-free transport tests.
	grab func() ([]byte, error)
}

type jpegQualityKey struct{}

func negotiatedFPS(limit, requested int) int {
	limit = max(1, min(maxFPS, limit))
	if requested > 0 {
		return min(limit, requested)
	}
	return limit
}
func automaticBitrate(c videoConfig) int {
	// Keep the per-frame budget when raising FPS; 1080p60=40 Mbps,
	// 1080p120=80 Mbps. This is an encoder budget, not measured bandwidth.
	n := int64(40000) * int64(c.area.width) * int64(c.area.height) * int64(c.fps) / (1920 * 1080 * 60)
	return int(max(int64(4000), min(int64(200000), n)))
}

type encoderProfile struct {
	name, capture string
	path          string
	args          []string
	codec         byte
	packetized    bool
}

func decoderCapabilities(ffmpeg string) string {
	if ffmpeg == "" {
		return "jpeg-only"
	}
	return "h264,jpeg-delta"
}

func findFFmpeg(explicit string) string {
	if explicit != "" {
		if st, err := os.Stat(explicit); err == nil && !st.IsDir() {
			p, _ := filepath.Abs(explicit)
			return p
		}
		return ""
	}
	exe, _ := os.Executable()
	for _, p := range []string{filepath.Join(filepath.Dir(exe), "ffmpeg.exe"), filepath.Join(filepath.Dir(exe), "runtime", "ffmpeg.exe"), filepath.Join("runtime", "ffmpeg.exe")} {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			abs, _ := filepath.Abs(p)
			return abs
		}
	}
	p, _ := exec.LookPath("ffmpeg.exe")
	return p
}
func helperCommand(ctx context.Context, path string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd
}

type tailBuffer struct {
	mu sync.Mutex
	b  []byte
}

func (t *tailBuffer) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, b...)
	if len(t.b) > 8192 {
		t.b = bytes.Clone(t.b[len(t.b)-8192:])
	}
	return len(b), nil
}
func (t *tailBuffer) String() string { t.mu.Lock(); defer t.mu.Unlock(); return string(t.b) }

func profileArgs(c videoConfig, encoder, capture string) []string {
	cursor := 1
	if c.hideCursor {
		cursor = 0
	}
	a := []string{"-hide_banner", "-loglevel", "warning", "-nostdin"}
	switch capture {
	case "test":
		a = append(a, "-re", "-readrate_initial_burst", "0.001", "-readrate_catchup", "1", "-f", "lavfi", "-i", fmt.Sprintf("testsrc2=size=%dx%d:rate=%d", c.area.width, c.area.height, c.fps))
	case "ddagrab":
		// Continuous cadence is intentional, including an unchanged desktop.
		// Minimize only gates painting/repair work, never the encoder clock.
		a = append(a, "-f", "lavfi", "-i", fmt.Sprintf("ddagrab=output_idx=%d:framerate=%d:draw_mouse=%d:dup_frames=1", c.monitor, c.fps, cursor))
		if encoder == "libx264" || encoder == "h264_qsv" {
			a = append(a, "-vf", "hwdownload,format=bgra,format=nv12")
		}
	default:
		a = append(a, "-f", "gdigrab", "-framerate", fmt.Sprint(c.fps), "-offset_x", fmt.Sprint(c.area.left), "-offset_y", fmt.Sprint(c.area.top), "-video_size", fmt.Sprintf("%dx%d", c.area.width, c.area.height), "-draw_mouse", fmt.Sprint(cursor), "-i", "desktop")
	}
	a = append(a, "-an", "-c:v", encoder, "-g", fmt.Sprint(c.fps), "-bf", "0")
	bitrate := fmt.Sprintf("%dk", c.bitrate)
	cq := c.h264Quality
	if cq == 0 {
		cq = 14
	}
	switch encoder {
	case "h264_nvenc":
		a = append(a, "-preset", "p1", "-tune", "ull", "-zerolatency", "1", "-delay", "0", "-rc", "vbr", "-cq", fmt.Sprint(cq), "-b:v", bitrate, "-maxrate", fmt.Sprintf("%dk", c.bitrate*2), "-bufsize", fmt.Sprintf("%dk", c.bitrate/4), "-rc-lookahead", "0")
	case "h264_amf":
		a = append(a, "-usage", "ultralowlatency", "-quality", "speed", "-b:v", bitrate)
	case "h264_qsv":
		a = append(a, "-preset", "veryfast", "-look_ahead", "0", "-async_depth", "1", "-b:v", bitrate)
	default:
		a = append(a, "-preset", "ultrafast", "-tune", "zerolatency", "-crf", fmt.Sprint(cq), "-maxrate", fmt.Sprintf("%dk", c.bitrate*2), "-bufsize", fmt.Sprintf("%dk", c.bitrate/4), "-pix_fmt", "yuv420p", "-threads", "4", "-x264-params", fmt.Sprintf("repeat-headers=1:scenecut=0:keyint=%d:min-keyint=%d", c.fps, c.fps))
	}
	return append(a, "-bsf:v", "h264_metadata=aud=insert", "-fps_mode", "passthrough", "-f", "h264", "-flush_packets", "1", "pipe:1")
}
func selectProfile(ctx context.Context, c videoConfig) (encoderProfile, error) {
	if c.encoder == "jpeg" || c.ffmpeg == "" {
		return encoderProfile{name: "jpeg-delta", capture: "gdi", codec: codecJPEGDelta}, nil
	}
	if c.area.width%2 != 0 || c.area.height%2 != 0 {
		if c.encoder == "auto" {
			log.Print("[lan][encoder] 屏幕尺寸不是偶数，使用区域 JPEG")
			return encoderProfile{name: "jpeg-delta", capture: "gdi", codec: codecJPEGDelta}, nil
		}
		return encoderProfile{}, errors.New("requested H.264 encoder requires an even screen width and height; use -encoder jpeg")
	}
	encoders := []string{"h264_nvenc", "h264_amf", "h264_qsv", "libx264"}
	if c.encoder != "auto" {
		encoders = []string{c.encoder}
	}
	captures := []string{"ddagrab", "gdi"}
	if c.capture != "auto" {
		captures = []string{c.capture}
	}
	if c.source == "test" {
		captures = []string{"test"}
	}
	for _, capture := range captures {
		for _, encoder := range encoders {
			args := packetProfileArgs(c, encoder, capture)
			args = append(args[:len(args)-1], "-frames:v", "3", "pipe:1")
			probeCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
			cmd := helperCommand(probeCtx, c.ffmpeg, args...)
			var stderr tailBuffer
			cmd.Stderr = &stderr
			data, err := cmd.Output()
			cancel()
			if err == nil && validEncoderPacketsAt(data, c.area) {
				log.Printf("[lan][encoder] %s / %s", capture, encoder)
				return encoderProfile{name: encoder, capture: capture, path: c.ffmpeg, args: packetProfileArgs(c, encoder, capture), codec: codecH264, packetized: true}, nil
			}
			if encoder == "h264_nvenc" && strings.Contains(stderr.String(), "Driver does not support") {
				compat := findFFmpeg(filepath.Join(filepath.Dir(c.ffmpeg), "ffmpeg-compat.exe"))
				if compat != "" {
					probeCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
					cmd := helperCommand(probeCtx, compat, compatibleArgs(args)...)
					cmd.Stderr = &tailBuffer{}
					data, err := cmd.Output()
					cancel()
					if err == nil && validEncoderPacketsAt(data, c.area) {
						log.Printf("[lan][encoder] %s / %s (driver-compatible runtime)", capture, encoder)
						return encoderProfile{name: encoder, capture: capture, path: compat, args: compatibleArgs(packetProfileArgs(c, encoder, capture)), codec: codecH264, packetized: true}, nil
					}
				}
			}
			log.Printf("[lan][probe] %s/%s unavailable: %s", capture, encoder, strings.TrimSpace(stderr.String()))
		}
	}
	if c.encoder != "auto" {
		return encoderProfile{}, errors.New("requested H.264 encoder failed; see probe log")
	}
	log.Printf("[lan][encoder] fallback regional JPEG/GDI (no FFmpeg required)")
	return encoderProfile{name: "jpeg-delta", capture: "gdi", codec: codecJPEGDelta}, nil
}
func compatibleArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == "-readrate_catchup" {
			i++
			continue
		}
		out = append(out, args[i])
	}
	return out
}

func packetProfileArgs(c videoConfig, encoder, capture string) []string {
	a := profileArgs(c, encoder, capture)
	// Change only the local output envelope, not the capture/encoder settings.
	return append(a[:len(a)-5], "-f", "flv", "-flvflags", "no_metadata+no_duration_filesize+no_sequence_end", "-flush_packets", "1", "pipe:1")
}

func validEncoderPackets(data []byte) bool {
	frames, keys := 0, 0
	err := readEncoderPackets(bytes.NewReader(data), func(_ []byte, key bool) error {
		frames++
		if key {
			keys++
		}
		return nil
	})
	return err == nil && frames == 3 && keys > 0
}

func runEncoder(ctx context.Context, c videoConfig, p encoderProfile, emit func([]byte, bool) error) error {
	if p.codec == codecJPEG || p.codec == codecJPEGDelta {
		return runJPEG(ctx, c, emit)
	}
	path := p.path
	if path == "" {
		path = c.ffmpeg
	}
	cmd := helperCommand(ctx, path, p.args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr tailBuffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	if p.packetized {
		err = readEncoderPackets(stdout, emit)
	} else {
		err = readAccessUnits(stdout, emit)
	}
	if err != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return err
	}
	if waitErr != nil {
		return fmt.Errorf("encoder exited: %w: %s", waitErr, stderr.String())
	}
	return io.EOF
}
func runJPEG(ctx context.Context, c videoConfig, emit func([]byte, bool) error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	capture := c.grab
	if capture == nil && c.source == "test" {
		pixels := make([]byte, c.area.width*c.area.height*4)
		base := make([]byte, len(pixels))
		for i := 0; i < len(base); i += 4 {
			base[i], base[i+1], base[i+2], base[i+3] = 30, 80, 130, 255
		}
		frame := 0
		capture = func() ([]byte, error) {
			copy(pixels, base)
			x := (frame * 4) % max(1, c.area.width-32)
			y := min(100, max(0, c.area.height-32))
			r := image.Rect(x, y, min(x+32, c.area.width), min(y+32, c.area.height))
			for row := r.Min.Y; row < r.Max.Y; row++ {
				for col := r.Min.X; col < r.Max.X; col++ {
					i := (row*c.area.width + col) * 4
					pixels[i], pixels[i+1], pixels[i+2] = 200, 170, 20
				}
			}
			frame++
			return pixels, nil
		}
	}
	if capture == nil {
		s, err := newScreenGrabber(c.area)
		if err != nil {
			return err
		}
		defer s.close()
		capture = func() ([]byte, error) { return s.captureCursor(!c.hideCursor) }
	}
	tick := time.NewTicker(time.Second / time.Duration(c.fps))
	defer tick.Stop()
	encoder := jpegDeltaEncoder{width: c.area.width, height: c.area.height, quality: c.quality}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
			b, err := capture()
			if err != nil {
				return err
			}
			data, key, err := encoder.encode(b, time.Now())
			if err != nil {
				return err
			}
			if data == nil {
				continue
			}
			if err := emit(data, key); err != nil {
				return err
			}
		}
	}
}

func decoderArgs(hardware bool) []string {
	return decoderOutputArgs(hardware, pixelBGRA)
}
func decoderOutputArgs(hardware bool, format frameFormat) []string {
	a := []string{"-hide_banner", "-loglevel", "warning", "-nostdin", "-probesize", "32", "-analyzeduration", "1", "-fpsprobesize", "0", "-fflags", "+discardcorrupt", "-flags", "low_delay", "-threads", "1"}
	if hardware {
		a = append(a, "-hwaccel", "d3d11va", "-hwaccel_output_format", "d3d11")
	}
	a = append(a, "-f", "flv", "-i", "pipe:0", "-an")
	filter := ""
	if hardware {
		filter = "hwdownload,format=nv12"
	}
	if format == pixelNV12 {
		if filter != "" {
			filter += ","
		}
		// Rawvideo has no frame metadata. Normalize range/matrix/chroma phase
		// explicitly, honoring the decoder's input metadata (not guessing from
		// resolution). Numeric chroma positions also work with the old runtime.
		filter += "scale=out_color_matrix=bt601:out_range=tv:out_h_chr_pos=0:out_v_chr_pos=128,format=nv12"
	} else if hardware {
		filter += ",format=bgra"
	}
	if filter != "" {
		a = append(a, "-vf", filter)
	}
	return append(a, "-pix_fmt", strings.ToLower(format.String()), "-fps_mode", "passthrough", "-f", "rawvideo", "-threads:v", "1", "-avioflags", "direct", "-flush_packets", "1", "pipe:1")
}

type videoDecoder struct {
	path          string
	width, height int
	hardware      bool
	format        frameFormat
	cancel        context.CancelFunc
	cmd           *exec.Cmd
	stdin         io.WriteCloser
	done          chan error
	stderr        *tailBuffer
	buffer        func() []byte
	fps           int
	packets       decoderPackets
	timing        decoderTiming
}

func (d *videoDecoder) start(parent context.Context, onFrame func([]byte)) error {
	return d.startWithArgs(parent, decoderOutputArgs(d.hardware, d.format), onFrame)
}

func (d *videoDecoder) startWithArgs(parent context.Context, args []string, onFrame func([]byte)) error {
	frameSize := d.format.size(d.width, d.height)
	if frameSize == 0 {
		return errors.New("invalid decoder output dimensions/format")
	}
	d.packets = decoderPackets{fps: d.fps}
	d.timing.init(d.fps, time.Now())
	ctx, cancel := context.WithCancel(parent)
	cmd := helperCommand(ctx, d.path, args...)
	in, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return err
	}
	// The default Windows anonymous pipe has a 4 KiB buffer. At 1080p60
	// BGRA this otherwise causes over 100,000 pipe operations per second.
	var readHandle, writeHandle syscall.Handle
	sa := syscall.SecurityAttributes{Length: uint32(unsafe.Sizeof(syscall.SecurityAttributes{})), InheritHandle: 1}
	err = syscall.CreatePipe(&readHandle, &writeHandle, &sa, 1<<20)
	if err != nil {
		cancel()
		return err
	}
	out := os.NewFile(uintptr(readHandle), "lan-decoder-read")
	writePipe := os.NewFile(uintptr(writeHandle), "lan-decoder-write")
	cmd.Stdout = writePipe
	stderr := &tailBuffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		out.Close()
		writePipe.Close()
		cancel()
		return err
	}
	writePipe.Close()
	d.cancel = cancel
	d.cmd = cmd
	d.stdin = in
	d.stderr = stderr
	d.done = make(chan error, 1)
	started := time.Now()
	go func() {
		defer out.Close()
		var result error
		first := true
		for {
			var b []byte
			if d.buffer != nil {
				b = d.buffer()
			} else {
				b = make([]byte, frameSize)
			}
			if len(b) != frameSize {
				result = errors.New("invalid decoder buffer size")
				break
			}
			if _, err := io.ReadFull(out, b); err != nil {
				result = err
				break
			}
			d.timing.complete(time.Now())
			onFrame(b)
			if first {
				log.Printf("[lan][decoder] first frame in %d ms (hardware=%t output=%s)", time.Since(started).Milliseconds(), d.hardware, d.format)
				first = false
			}
		}
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		d.done <- result
	}()
	return nil
}

func (d *videoDecoder) writeFrame(data []byte) error {
	d.timing.submit(time.Now())
	return d.packets.write(d.stdin, data)
}
func (d *videoDecoder) close() {
	if d.cancel != nil {
		d.cancel()
		_ = d.stdin.Close()
		<-d.done
		d.cancel = nil
	}
}
