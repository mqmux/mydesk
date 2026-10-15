package lan

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"
)

func testCipher(t *testing.T) *packetCipher {
	t.Helper()
	key := make([]byte, 36)
	_, _ = rand.Read(key)
	p, err := makePacketCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func TestUDPReorderDuplicatesAndRetransmission(t *testing.T) {
	p := testCipher(t)
	data := make([]byte, 1024*256)
	_, _ = rand.Read(data)
	f := videoFrame{id: 42, epoch: 3, codec: codecH264, key: true, data: data}
	parts := splitFrame(p, f)
	a := newAssembler()
	now := time.Now()
	var result *videoFrame
	for i := len(parts) - 1; i >= 0; i-- {
		if i == 7 {
			continue
		}
		if len(parts[i]) > packetSize {
			t.Fatal("datagram exceeds MTU budget")
		}
		fragment, err := p.open(parts[i])
		if err != nil {
			t.Fatal(err)
		}
		got, err := a.add(fragment, now)
		if err != nil {
			t.Fatal(err)
		}
		if got != nil {
			result = got
		}
		_, _ = a.add(fragment, now)
	}
	if result != nil {
		t.Fatal("incomplete frame was released")
	}
	requests, expired := a.maintenance(now.Add(15 * time.Millisecond))
	if expired != 0 || len(requests) != 1 || len(requests[0].Missing) != 1 || requests[0].Missing[0] != 7 {
		t.Fatalf("incorrect missing-fragment feedback: %+v", requests)
	}
	fragment, _ := p.open(parts[7])
	result, err := a.add(fragment, now.Add(20*time.Millisecond))
	if err != nil || result == nil || !bytes.Equal(result.data, data) || result.epoch != 3 || !result.key {
		t.Fatal("reassembled frame differs")
	}
	if a.bytes != 0 || len(a.frames) != 0 {
		t.Fatal("assembler retained completed frame")
	}
	for _, part := range parts {
		fragment, _ := p.open(part)
		got, err := a.add(fragment, now.Add(30*time.Millisecond))
		if err != nil || got != nil {
			t.Fatal("late retransmission resurrected completed frame")
		}
	}
	if len(a.frames) != 0 {
		t.Fatal("duplicate created phantom partial frame")
	}
}
func TestUDPAuthenticationAndLimits(t *testing.T) {
	p := testCipher(t)
	packet := p.seal(fragment{kind: packetVideo, codec: codecJPEG, frame: 1, total: 1, payload: []byte("jpeg")})
	packet[16] ^= 1
	if _, err := p.open(packet); err == nil {
		t.Fatal("tampered header accepted")
	}
	packet = p.seal(fragment{kind: packetVideo, total: 65535, payload: []byte("x")})
	if _, err := p.open(packet); err == nil {
		t.Fatal("unbounded fragment allocation")
	}
	a := newAssembler()
	f := fragment{kind: packetVideo, frame: 1, total: 2, index: 0, payload: []byte("x")}
	_, _ = a.add(f, time.Now())
	_, expired := a.maintenance(time.Now().Add(101 * time.Millisecond))
	if expired != 1 || a.bytes != 0 {
		t.Fatal("expired frame retained")
	}
}
func TestH264ReferenceRecovery(t *testing.T) {
	o := newFrameOrder()
	mk := func(id, epoch uint64, key bool) videoFrame {
		return videoFrame{id: id, epoch: epoch, key: key, codec: codecH264}
	}
	if len(o.push(mk(1, 1, false))) != 0 {
		t.Fatal("P-frame decoded without IDR")
	}
	if len(o.push(mk(2, 1, true))) != 1 {
		t.Fatal("IDR not accepted")
	}
	if len(o.push(mk(4, 1, false))) != 0 {
		t.Fatal("out-of-order dependent frame released")
	}
	got := o.push(mk(3, 1, false))
	if len(got) != 2 || got[0].id != 3 || got[1].id != 4 {
		t.Fatal("ordered chain not restored")
	}
	o.reset()
	if len(o.push(mk(5, 1, false))) != 0 {
		t.Fatal("lost reference not enforced")
	}
	if len(o.push(mk(8, 1, true))) != 1 {
		t.Fatal("IDR did not recover gap")
	}
	if len(o.push(mk(9, 2, true))) != 1 {
		t.Fatal("encoder epoch reset did not recover")
	}
	if len(o.push(mk(10, 1, true))) != 0 {
		t.Fatal("old encoder epoch accepted")
	}
}
func TestAnnexBFragmentedReader(t *testing.T) {
	stream := []byte{0, 0, 0, 1, 9, 0x10, 0, 0, 1, 7, 0x11, 0, 0, 1, 5, 0x22, 0, 0, 0, 1, 9, 0x10, 0, 0, 1, 1, 0x33}
	var frames [][]byte
	var keys []bool
	err := readAccessUnits(&shortReader{b: stream}, func(b []byte, key bool) error { frames = append(frames, b); keys = append(keys, key); return nil })
	if err != nil || len(frames) != 2 || !keys[0] || keys[1] || !bytes.Equal(bytes.Join(frames, nil), stream) {
		t.Fatalf("access-unit parsing failed %v %d", err, len(frames))
	}
}

type shortReader struct{ b []byte }

func (r *shortReader) Read(p []byte) (int, error) {
	if len(r.b) == 0 {
		return 0, io.EOF
	}
	n := min(3, len(p))
	if n > len(r.b) {
		n = len(r.b)
	}
	copy(p, r.b[:n])
	r.b = r.b[n:]
	return n, nil
}
func TestWireRejectsOversizeBeforeAllocation(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	go func() {
		var head [8]byte
		binary.BigEndian.PutUint32(head[:4], 100)
		binary.BigEndian.PutUint32(head[4:], maxWireBody+1)
		_, _ = a.Write(head[:])
	}()
	if _, _, err := (&wire{conn: b}).read(); err == nil {
		t.Fatal("oversize body accepted")
	}
}
func TestUDPLoopbackEncryptedFrame(t *testing.T) {
	server, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client, err := net.DialUDP("udp4", nil, server.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	p := testCipher(t)
	data := bytes.Repeat([]byte("screen-pixels"), 10000)
	f := videoFrame{id: 1, epoch: 1, codec: codecJPEG, key: true, data: data}
	parts := splitFrame(p, f)
	go func() {
		for _, part := range parts {
			_, _ = client.Write(part)
			time.Sleep(50 * time.Microsecond)
		}
	}()
	a := newAssembler()
	_ = server.SetReadDeadline(time.Now().Add(3 * time.Second))
	b := make([]byte, packetSize+1)
	for {
		n, _, err := server.ReadFromUDP(b)
		if err != nil {
			t.Fatal(err)
		}
		part, err := p.open(b[:n])
		if err != nil {
			t.Fatal(err)
		}
		complete, err := a.add(part, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if complete != nil {
			if !bytes.Equal(data, complete.data) {
				t.Fatal("loopback payload corruption")
			}
			break
		}
	}
}
