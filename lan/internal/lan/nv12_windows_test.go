//go:build windows && amd64

package lan

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestNV12GPUConversionSharpRepairsAndBGRA(t *testing.T) {
	if os.Getenv("MYDESK_LAN_GPU_TEST") != "1" {
		t.Skip("read-only GPU opt-in")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	const sw, sh, dw, dh = 130, 66, 259, 135
	r, err := newD3D11(0, dw, dh, sw, sh)
	if err != nil {
		t.Fatal(err)
	}
	defer r.close()
	target, err := r.texture(dw, dh, 87, true)
	if err != nil {
		t.Fatal(err)
	}
	defer target.close()
	video := make([]byte, pixelNV12.size(sw, sh))
	rand.New(rand.NewSource(99)).Read(video)
	var overlay refineOverlay
	overlay.reset(sw, sh)
	for pass := 0; pass < 4; pass++ {
		format, pixels := pixelNV12, video
		switch pass {
		case 1:
			overlay.patch(image.Rect(64, 0, 128, 64), bytes.Repeat([]byte{80, 200, 50, 255}, 64*64))
		case 2:
			overlay.patch(image.Rect(64, 0, 128, 64), nil)
		case 3:
			format, pixels = pixelBGRA, nv12ToBGRA(nil, video, sw, sh)
		}
		_, err = r.render(displaySnapshot{pixels: pixels, format: format, baseRevision: uint64(pass + 1), repairs: overlay.snapshot(pass == 0)}, target.rtv, 0, 0, dw, dh, false, false)
		if err != nil {
			t.Fatal(err)
		}
		got, err := readGPU(r, target)
		if err != nil {
			t.Fatal(err)
		}
		var cpu sharpDisplay
		want := cpu.prepare(overlay.compose(nv12ToBGRA(nil, video, sw, sh)), sw, sh, dw, dh)
		if !bytes.Equal(got, want) {
			for i := range got {
				if got[i] != want[i] {
					t.Fatalf("pass %d byte %d GPU=%d CPU=%d", pass, i, got[i], want[i])
				}
			}
		}
	}
}

func TestDecoderNV12Args(t *testing.T) {
	for _, hardware := range []bool{false, true} {
		a := strings.Join(decoderOutputArgs(hardware, pixelNV12), " ")
		if !strings.Contains(a, "-pix_fmt nv12") || strings.Contains(a, "format=bgra") || !strings.Contains(a, "out_color_matrix=bt601:out_range=tv:out_h_chr_pos=0:out_v_chr_pos=128") || !strings.Contains(a, "-threads:v 1") {
			t.Fatal("wrong output contract", a)
		}
		if strings.Contains(a, "hwdownload") != hardware {
			t.Fatal("wrong hardware download", a)
		}
	}
}

func TestNV12ViewerLeaseAndWholeRefine(t *testing.T) {
	v := &viewer{width: 128, height: 64}
	first := solidNV12(128, 64, 100, 128, 128)
	v.setSurface(first, pixelNV12)
	s := v.snapshotDisplay(true)
	for i := 0; i < 30; i++ {
		b := v.bufferFor(pixelNV12)
		copy(b, first)
		b[0] = byte(i)
		v.setSurface(b, pixelNV12)
	}
	if s.format != pixelNV12 || s.pixels[0] != 100 {
		t.Fatal("NV12 leased frame overwritten")
	}
	v.releaseDisplay(s)
	v.setRefine(make([]byte, 128*64*4))
	s = v.snapshotDisplay(true)
	defer v.releaseDisplay(s)
	if s.format != pixelBGRA || !s.wholeRefine || len(s.pixels) != 128*64*4 {
		t.Fatal("legacy refine treated as YUV")
	}
}

func decodeTestPictures(t *testing.T, ctx context.Context, ff string, stream []byte, w, h int, format frameFormat, hardware bool) []byte {
	t.Helper()
	frames := make(chan []byte, 8)
	d := &videoDecoder{path: ff, width: w, height: h, format: format, hardware: hardware, fps: 60}
	if err := d.start(ctx, func(b []byte) {
		select {
		case frames <- b:
		default:
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer d.close()
	if err := readAccessUnits(bytes.NewReader(stream), func(b []byte, _ bool) error { return d.writeFrame(b) }); err != nil {
		t.Fatal(err, d.stderr.String())
	}
	select {
	case b := <-frames:
		return b
	case err := <-d.done:
		d.cancel()
		d.cancel = nil
		t.Fatal(err, d.stderr.String())
	case <-time.After(4 * time.Second):
		t.Fatal("no decoder output", d.stderr.String())
	}
	return nil
}

func TestNV12DecoderColourMetadataAndRuntime(t *testing.T) {
	if os.Getenv("MYDESK_LAN_MEDIA_TEST") != "1" {
		t.Skip("FFmpeg colour opt-in")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	const w, h = 128, 64
	for _, runtimeName := range []string{"ffmpeg.exe", "ffmpeg-compat.exe"} {
		ff, _ := filepath.Abs(filepath.Join("..", "..", "runtime", runtimeName))
		for _, matrix := range []string{"smpte170m", "bt709"} {
			for _, colorRange := range []string{"tv", "pc"} {
				t.Run(runtimeName+"-"+matrix+"-"+colorRange, func(t *testing.T) {
					input := solidNV12(w, h, 100, 128, 128)
					bars := [][3]byte{{16, 128, 128}, {235, 128, 128}, {81, 90, 240}, {145, 54, 34}, {41, 240, 110}, {126, 128, 128}, {200, 180, 80}, {60, 100, 160}}
					if colorRange == "pc" {
						bars[0][0], bars[1][0] = 0, 255
					}
					for y := 0; y < h; y++ {
						for x := 0; x < w; x++ {
							input[y*w+x] = bars[x/16][0]
						}
					}
					for y := 0; y < h/2; y++ {
						for x := 0; x < w; x += 2 {
							input[w*h+y*w+x], input[w*h+y*w+x+1] = bars[x/16][1], bars[x/16][2]
						}
					}
					args := []string{"-hide_banner", "-loglevel", "error", "-f", "rawvideo", "-pixel_format", "nv12", "-video_size", fmt.Sprintf("%dx%d", w, h), "-framerate", "60", "-colorspace", matrix, "-color_range", colorRange, "-i", "pipe:0", "-frames:v", "4", "-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency", "-qp", "0", "-bf", "0", "-colorspace", matrix, "-color_range", colorRange, "-bsf:v", "h264_metadata=aud=insert", "-f", "h264", "pipe:1"}
					cmd := helperCommand(ctx, ff, args...)
					cmd.Stdin = bytes.NewReader(bytes.Repeat(input, 4))
					var stderr tailBuffer
					cmd.Stderr = &stderr
					stream, err := cmd.Output()
					if err != nil {
						t.Fatal(err, stderr.String())
					}
					bgra := decodeTestPictures(t, ctx, ff, stream, w, h, pixelBGRA, false)
					nv12 := decodeTestPictures(t, ctx, ff, stream, w, h, pixelNV12, false)
					got := nv12ToBGRA(nil, nv12, w, h)
					peak := 0
					// Bar interiors isolate range/matrix from differing chroma
					// upsampling filters. Random-phase GPU equivalence is separate.
					for bar := 0; bar < 8; bar++ {
						for y := 4; y < h-4; y++ {
							for x := bar*16 + 4; x < bar*16+12; x++ {
								for c := 0; c < 3; c++ {
									i := (y*w+x)*4 + c
									peak = max(peak, absInt(int(got[i])-int(bgra[i])))
								}
							}
						}
					}
					if peak > 5 {
						t.Fatalf("colour normalization differs from BGRA by %d", peak)
					}
					for _, x := range []int{8, 24} {
						i := (10*w + x) * 4
						want := 0
						if x == 24 {
							want = 255
						}
						for c := 0; c < 3; c++ {
							if absInt(int(got[i+c])-want) > 2 {
								t.Fatal("black/white range", got[i:i+4])
							}
						}
					}
					t.Logf("normalized NV12/BGRA interior max difference %d", peak)
				})
			}
		}
	}
}

// Exercise the real controller decoder loop, including process replacement
// and keyframe recovery, without an interactive window or network peer.
func TestNV12ControllerCompatibilityRestart(t *testing.T) {
	if os.Getenv("MYDESK_LAN_MEDIA_TEST") != "1" {
		t.Skip("real decoder fallback opt-in")
	}
	ff, _ := filepath.Abs(filepath.Join("..", "..", "runtime", "ffmpeg.exe"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := helperCommand(ctx, ff, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "color=gray:size=128x64:rate=60", "-frames:v", "1", "-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency", "-bf", "0", "-g", "1", "-bsf:v", "h264_metadata=aud=insert", "-f", "h264", "pipe:1")
	unit, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, setting := range []struct{ output, renderer, backend string }{{"auto", "auto", "mf"}, {"bgra", "auto", "mf"}, {"auto", "d2d", "mf"}, {"auto", "auto", "ffmpeg"}, {"bgra", "auto", "ffmpeg"}, {"auto", "d2d", "ffmpeg"}} {
		t.Run(setting.backend+"-"+setting.output+"-"+setting.renderer, func(t *testing.T) {
			child, stop := context.WithCancel(ctx)
			defer stop()
			v := &viewer{width: 128, height: 64, renderBackend: setting.renderer}
			s := &controllerSession{ctx: child, cancel: stop, ffmpeg: ff, software: true, decoder: setting.backend, decodeOutput: setting.output, view: v, welcome: message{Width: 128, Height: 64, FPS: 60}, frames: make(chan videoFrame, 16), commands: make(chan message, 64)}
			done := make(chan struct{})
			go func() { s.decode(); close(done) }()
			defer func() {
				stop()
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Error("decoder not closed")
				}
			}()
			id := uint64(0)
			feed := func() {
				id++
				select {
				case s.frames <- videoFrame{id: id, epoch: 1, codec: codecH264, key: true, data: unit, at: time.Now()}:
				case <-child.Done():
					t.Fatal(child.Err())
				}
			}
			waitFormat := func(want frameFormat) {
				deadline := time.Now().Add(3 * time.Second)
				for time.Now().Before(deadline) {
					feed()
					time.Sleep(15 * time.Millisecond)
					v.mu.Lock()
					f := v.frames.latest
					ok := f != nil && f.format == want
					v.mu.Unlock()
					if ok {
						return
					}
				}
				t.Fatal("no output in format", want)
			}
			if setting.output == "auto" && setting.renderer == "auto" {
				waitFormat(pixelNV12)
				v.setRefinePatch(message{Left: 0, Top: 0, Width: 64, Height: 64}, bytes.Repeat([]byte{100, 100, 100, 255}, 64*64))
				v.compatibility.Store(true)
				waitFormat(pixelBGRA)
				v.mu.Lock()
				valid := v.repairs.valid[0]
				v.mu.Unlock()
				if !valid {
					t.Fatal("restart removed static RGB repair")
				}
			} else {
				waitFormat(pixelBGRA)
			}
			if s.decoded.Load() == 0 {
				t.Fatal("no decoded frames")
			}
		})
	}
}
