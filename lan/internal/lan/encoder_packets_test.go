package lan

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
	"time"
)

func packetFixture(t *testing.T) ([]byte, [][]byte) {
	t.Helper()
	units := [][]byte{
		{0, 0, 0, 1, 7, 0x64, 0, 0x1f, 0x55, 0, 0, 0, 1, 8, 0xee, 0x22, 0, 0, 1, 9, 0x10, 0, 0, 1, 5, 0x33, 0x44},
		{0, 0, 0, 1, 9, 0x10, 0, 0, 1, 1, 0x55, 0x66},
	}
	var buf bytes.Buffer
	p := decoderPackets{fps: 120}
	for _, unit := range units {
		if err := p.write(&buf, unit); err != nil {
			t.Fatal(err)
		}
	}
	return buf.Bytes(), units
}

func TestEncoderPacketsPreserveNALs(t *testing.T) {
	stream, units := packetFixture(t)
	var frames [][]byte
	var keys []bool
	err := readEncoderPackets(&shortReader{b: stream}, func(b []byte, key bool) error {
		frames = append(frames, b)
		keys = append(keys, key)
		return nil
	})
	if err != nil || len(frames) != 2 || !keys[0] || keys[1] {
		t.Fatalf("framing/keys: %d %v %v", len(frames), keys, err)
	}
	for i := range units {
		want, got := annexBNALs(units[i]), annexBNALs(frames[i])
		if len(want) != len(got) {
			t.Fatal("NAL count changed")
		}
		for j := range want {
			if !bytes.Equal(want[j], got[j]) {
				t.Fatal("encoded NAL bytes changed")
			}
		}
	}
}

func TestEncoderPacketsEmitWithoutNextPictureOrEOF(t *testing.T) {
	stream, _ := packetFixture(t)
	configEnd := 13 + 15 + (int(stream[14])<<16 | int(stream[15])<<8 | int(stream[16]))
	firstEnd := configEnd + 15 + (int(stream[configEnd+1])<<16 | int(stream[configEnd+2])<<8 | int(stream[configEnd+3]))
	r, w := io.Pipe()
	defer r.Close()
	defer w.Close()
	emitted := make(chan bool, 2)
	done := make(chan error, 1)
	go func() { done <- readEncoderPackets(r, func(_ []byte, key bool) error { emitted <- key; return nil }) }()
	if _, err := w.Write(stream[:firstEnd]); err != nil {
		t.Fatal(err)
	}
	select {
	case key := <-emitted:
		if !key {
			t.Fatal("first picture was not IDR")
		}
	case <-time.After(time.Second):
		t.Fatal("complete first picture waited for next picture/EOF")
	}
	if _, err := w.Write(stream[firstEnd:]); err != nil {
		t.Fatal(err)
	}
	select {
	case key := <-emitted:
		if key {
			t.Fatal("P picture incorrectly marked IDR")
		}
	case <-time.After(time.Second):
		t.Fatal("complete last picture waited for EOF")
	}
	w.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestEncoderAVCRestoresIDRParametersAndLengthSizes(t *testing.T) {
	for _, size := range []int{1, 2, 4} {
		a := encoderAVC{}
		config := []byte{1, 0x64, 0, 0x1f, 0xfc | byte(size-1), 0xe1, 0, 4, 7, 0x64, 0, 0x1f, 1, 0, 2, 8, 0xee}
		if err := a.configure(config); err != nil {
			t.Fatal(err)
		}
		packet := make([]byte, size)
		packet[size-1] = 3
		packet = append(packet, 5, 0xab, 0xcd)
		for i := 0; i < 2; i++ {
			out, key, err := a.picture(packet)
			if err != nil || !key || !containsNAL(out, 7) || !containsNAL(out, 8) || !containsNAL(out, 5) {
				t.Fatalf("IDR could not bootstrap decoder: size=%d %v", size, err)
			}
		}
		packet[size] = 1
		out, key, err := a.picture(packet)
		if err != nil || key || containsNAL(out, 7) || len(annexBNALs(out)) != 1 {
			t.Fatal("P picture altered")
		}
		aud := make([]byte, size)
		aud[size-1] = 2
		aud = append(aud, 9, 0x10)
		packet[size] = 5
		out, key, err = a.picture(append(aud, packet...))
		if err != nil || !key || len(annexBNALs(out)) != 4 {
			t.Fatal("AUD + IDR framing failed", err)
		}
		for i, kind := range []byte{9, 7, 8, 5} {
			if annexBNALs(out)[i][0]&31 != kind {
				t.Fatal("AUD was displaced by restored parameter sets")
			}
		}
	}
}

func TestEncoderPacketsRejectMalformedAndCallbackError(t *testing.T) {
	stream, _ := packetFixture(t)
	for cut := 0; cut < len(stream); cut++ {
		err := readEncoderPackets(bytes.NewReader(stream[:cut]), func([]byte, bool) error { return nil })
		// EOF exactly between tags is valid; partial headers/payloads are not.
		if err == nil && cut != 13 {
			pos := 13
			for pos < cut {
				pos += 15 + (int(stream[pos+1])<<16 | int(stream[pos+2])<<8 | int(stream[pos+3]))
			}
			if pos != cut {
				t.Fatalf("truncation accepted at %d", cut)
			}
		}
	}
	for _, mutate := range []func([]byte){
		func(b []byte) { b[0] = 'X' },
		func(b []byte) { binary.BigEndian.PutUint32(b[5:9], 65537) },
		func(b []byte) { b[12] = 1 },
		func(b []byte) { b[14], b[15], b[16] = 0xff, 0xff, 0xff },
		func(b []byte) { b[21] = 1 },
		func(b []byte) { b[24] = 0x16 },
		func(b []byte) { b[26] = 1 },
		func(b []byte) { b[len(b)-1] ^= 1 },
	} {
		bad := bytes.Clone(stream)
		mutate(bad)
		if err := readEncoderPackets(bytes.NewReader(bad), func([]byte, bool) error { return nil }); err == nil {
			t.Fatal("malformed FLV accepted")
		}
	}
	want := errors.New("stop encoder")
	if err := readEncoderPackets(bytes.NewReader(stream), func([]byte, bool) error { return want }); !errors.Is(err, want) {
		t.Fatal("callback error lost", err)
	}
	a := encoderAVC{}
	if _, _, err := a.picture([]byte{0, 0, 0, 1, 5}); err == nil {
		t.Fatal("picture without configuration accepted")
	}
	config := []byte{1, 100, 0, 31, 0xff, 0xe1, 0, 4, 7, 100, 0, 31, 1, 0, 1, 8}
	if err := a.configure(config); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]byte{{0}, {0, 0, 0, 0}, {0, 0, 0, 8, 5}, {0, 0, 0, 1, 1}, {0, 0, 0, 1, 9}} {
		if _, _, err := a.picture(bad); err == nil {
			t.Fatal("invalid picture accepted")
		}
	}
}

func TestEncoderAVCConfigurationRefreshAndValidation(t *testing.T) {
	config := []byte{1, 100, 0, 31, 0xff, 0xe1, 0, 4, 7, 100, 0, 31, 1, 0, 2, 8, 0xee}
	for cut := 0; cut < len(config); cut++ {
		var a encoderAVC
		if err := a.configure(config[:cut]); err == nil {
			t.Fatalf("truncated avcC accepted at %d", cut)
		}
	}
	for _, pos := range []int{0, 4, 5, 7, 8, 12, 14, 15} {
		bad := bytes.Clone(config)
		bad[pos] = 0
		var a encoderAVC
		if err := a.configure(bad); err == nil {
			t.Fatalf("invalid avcC accepted at %d", pos)
		}
	}
	var a encoderAVC
	if err := a.configure(config); err != nil {
		t.Fatal(err)
	}
	key := []byte{0, 0, 0, 2, 5, 0xab}
	old, _, err := a.picture(key)
	if err != nil {
		t.Fatal(err)
	}
	updated := bytes.Clone(config)
	updated[11], updated[16] = 32, 0xdd
	if err := a.configure(updated); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.picture([]byte{0, 0, 0, 1, 1}); err == nil {
		t.Fatal("configuration refresh accepted dependent picture first")
	}
	got, isKey, err := a.picture(key)
	if err != nil || !isKey || bytes.Equal(old, got) || annexBNALs(got)[0][3] != 32 || annexBNALs(got)[1][1] != 0xdd {
		t.Fatal("updated IDR reused stale SPS/PPS", err)
	}
}
