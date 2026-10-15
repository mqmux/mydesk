package lan

import (
	"bytes"
	"math/rand"
	"strings"
	"testing"
)

func TestSharpCachedRegionsMatchFullRender(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	for _, size := range [][4]int{{67, 71, 133, 147}, {130, 70, 256, 144}, {193, 129, 257, 173}} {
		sw, sh, dw, dh := size[0], size[1], size[2], size[3]
		src := make([]byte, sw*sh*4)
		rng.Read(src)
		cache := sharpDisplay{}
		for i := 0; i < 24; i++ {
			for j := 0; j < 5; j++ {
				src[rng.Intn(len(src))] = byte(rng.Intn(256))
			}
			got := cache.prepare(src, sw, sh, dw, dh)
			fresh := sharpDisplay{}
			want := fresh.prepare(src, sw, sh, dw, dh)
			if !bytes.Equal(got, want) {
				t.Fatalf("cached region diverged %v iteration %d", size, i)
			}
			if !bytes.Equal(cache.prepare(src, sw, sh, dw, dh), got) {
				t.Fatal("static frame changed")
			}
		}
	}
}

func TestExplicitQ100H264Arguments(t *testing.T) {
	c := videoConfig{fps: 120, bitrate: 80000, h264Quality: 8, area: desktop{width: 1920, height: 1080}}
	for _, pair := range [][2]string{{"h264_nvenc", "-cq 8"}, {"libx264", "-crf 8"}} {
		args := strings.Join(profileArgs(c, pair[0], "test"), " ")
		if !strings.Contains(args, pair[1]) || !strings.Contains(args, "-g 120") {
			t.Fatal(args)
		}
	}
}
func TestSharpOriginalKernelAndBounds(t *testing.T) {
	src := make([]byte, 3*3*4)
	for i := range src {
		src[i] = 100
	}
	for k := 0; k < 3; k++ {
		src[4*4+k] = 140
	}
	s := sharpDisplay{}
	got := s.prepare(src, 3, 3, 5, 5)
	// Endpoint-aligned scaling preserves corners; sharpening increases the edge.
	if got[0] != 100 || got[(2*5+2)*4] <= 140 {
		t.Fatal("sharp enhancement or endpoints missing")
	}
	bad := s.prepare(src, 3, 3, 100000, 100000)
	if len(bad) != len(src) {
		t.Fatal("unbounded allocation")
	}
}
func BenchmarkSharp1080To1440Partial(b *testing.B) {
	src := make([]byte, 1920*1080*4)
	s := sharpDisplay{}
	s.prepare(src, 1920, 1080, 2560, 1440)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		src[0] = byte(i)
		s.prepare(src, 1920, 1080, 2560, 1440)
	}
}
func BenchmarkSharp1080To1440Full(b *testing.B) {
	src := make([]byte, 1920*1080*4)
	s := sharpDisplay{}
	s.prepare(src, 1920, 1080, 2560, 1440)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j := 0; j < len(src); j += 64 * 4 {
			src[j] = byte(i)
		}
		s.prepare(src, 1920, 1080, 2560, 1440)
	}
}
