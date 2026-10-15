//go:build windows && amd64

package lan

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type decoderBackendKey struct{}

const controllerBuildVersion = "4.20-key-match"

type pictureDecoder interface {
	writeFrame([]byte) error
	close()
	slow(time.Time) bool
	failure() error
}

type ffmpegDecoder struct {
	decoder *videoDecoder
	err     error
}

func (d *ffmpegDecoder) writeFrame(b []byte) error { return d.decoder.writeFrame(b) }
func (d *ffmpegDecoder) close()                    { d.decoder.close() }
func (d *ffmpegDecoder) slow(now time.Time) bool   { return d.decoder.timing.slow(now) }
func (d *ffmpegDecoder) failure() error {
	if d.err != nil {
		return d.err
	}
	select {
	case err := <-d.decoder.done:
		if err == nil {
			err = errors.New("FFmpeg decoder stopped")
		}
		d.err = fmt.Errorf("%w: %s", err, d.decoder.stderr.String())
		d.decoder.cancel()
		d.decoder.cancel = nil
		d.decoder.stdin.Close()
	default:
	}
	return d.err
}

func startPictureDecoder(ctx context.Context, backend, path string, hardware bool, format frameFormat, w, h, fps int, buffer func() []byte, onFrame func([]byte), gpu ...func(*gpuDisplayFrame)) (pictureDecoder, error) {
	if backend == "mf" {
		d := &nativeDecoder{width: w, height: h, fps: fps, format: format, hardware: hardware, buffer: buffer}
		d.timing.onComplete = decoderObserver(ctx)
		if len(gpu) > 0 {
			d.onGPUFrame = gpu[0]
		}
		if err := d.start(ctx, onFrame); err != nil {
			return nil, err
		}
		return d, nil
	}
	if path == "" {
		return nil, errors.New("FFmpeg decoder unavailable; place ffmpeg.exe beside the controller")
	}
	d := &videoDecoder{path: path, width: w, height: h, fps: fps, format: format, hardware: hardware, buffer: buffer}
	d.timing.onComplete = decoderObserver(ctx)
	if err := d.start(ctx, onFrame); err != nil {
		return nil, err
	}
	return &ffmpegDecoder{decoder: d}, nil
}

func controllerDecoderBackend(ctx context.Context, ffmpeg string) (string, string, error) {
	requested, _ := ctx.Value(decoderBackendKey{}).(string)
	if requested == "" {
		requested = "auto"
	}
	if requested != "auto" && requested != "mf" && requested != "ffmpeg" {
		return "", "", errors.New("decoder must be auto, mf or ffmpeg")
	}
	if requested != "ffmpeg" {
		if err := nativeDecoderAvailable(); err == nil {
			return requested, "h264,jpeg-delta", nil
		} else if requested == "mf" {
			return "", "", fmt.Errorf("Windows native H.264 decoder unavailable: %w", err)
		}
	}
	return requested, decoderCapabilities(ffmpeg), nil
}

// Persistent fallback policy: an epoch/configuration restart must not select
// the failing backend again. Explicit mf/ffmpeg remains useful for honest A/B.
func nextDecoder(backend string, hardware bool, format frameFormat, requested, ffmpeg string) (string, bool, frameFormat, bool) {
	if backend == "mf" {
		if requested != "mf" && ffmpeg != "" {
			return "ffmpeg", true, format, true
		}
		if hardware {
			return backend, false, format, true
		}
		return backend, hardware, format, false
	}
	if hardware {
		return backend, false, format, true
	}
	if format == pixelNV12 {
		return backend, false, pixelBGRA, true
	}
	return backend, hardware, format, false
}

// Native CPU readback can be slower than the existing FFmpeg route on some
// GPUs. Local queue overflows across encoder epochs must not be hidden by
// resetting the usual startup window on every keyframe recovery. One burst
// counts once; three separated recent bursts justify a backend fallback.
type nativeQueuePressure struct {
	started time.Time
	last    uint64
	times   [3]time.Time
	next    int
}

func (p *nativeQueuePressure) reset(now time.Time, total uint64) {
	*p = nativeQueuePressure{started: now, last: total}
}
func (p *nativeQueuePressure) record(now time.Time, total uint64) bool {
	if total == p.last {
		return false
	}
	p.last = total
	if now.Sub(p.started) < 500*time.Millisecond {
		return false
	}
	if p.next > 0 && now.Sub(p.times[(p.next-1)%3]) < 100*time.Millisecond {
		return false
	}
	p.times[p.next%3] = now
	p.next++
	return p.next >= 3 && now.Sub(p.times[p.next%3]) <= 2*time.Second
}
