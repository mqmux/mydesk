package lan

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

func TestDecoderPacketsPreserveNALs(t *testing.T) {
	nals := [][]byte{{0x67, 0x64, 0, 0x28, 0x80}, {0x68, 0x80}, {9, 0xf0}, {0x65, 0x88, 0x80}}
	var au []byte
	for i, nal := range nals {
		if i%2 == 0 {
			au = append(au, 0, 0, 0, 1)
		} else {
			au = append(au, 0, 0, 1)
		}
		au = append(au, nal...)
	}
	var out bytes.Buffer
	p := decoderPackets{fps: 120}
	if err := p.write(&out, au); err != nil {
		t.Fatal(err)
	}
	stream := out.Bytes()
	if !bytes.Equal(stream[:13], []byte{'F', 'L', 'V', 1, 1, 0, 0, 0, 9, 0, 0, 0, 0}) {
		t.Fatal("invalid FLV header")
	}
	offset := 13
	for tag := 0; tag < 2; tag++ {
		n := int(stream[offset+1])<<16 | int(stream[offset+2])<<8 | int(stream[offset+3])
		payload := stream[offset+11 : offset+11+n]
		if binary.BigEndian.Uint32(stream[offset+11+n:]) != uint32(n+11) {
			t.Fatal("invalid tag size")
		}
		if tag == 1 {
			if payload[0] != 0x17 || payload[1] != 1 {
				t.Fatal("invalid AVC picture tag")
			}
			q := payload[5:]
			for _, nal := range nals {
				size := int(binary.BigEndian.Uint32(q))
				q = q[4:]
				if size != len(nal) || !bytes.Equal(nal, q[:size]) {
					t.Fatal("NAL bytes changed")
				}
				q = q[size:]
			}
			if len(q) != 0 {
				t.Fatal("extra NAL data")
			}
		}
		offset += 11 + n + 4
	}
	if offset != len(stream) {
		t.Fatal("extra tags")
	}
	out.Reset()
	if err := p.write(&out, []byte{0, 0, 0, 1, 1, 0x80}); err != nil {
		t.Fatal(err)
	}
	if out.Bytes()[0] != 9 || out.Bytes()[6] != 8 || out.Bytes()[11] != 0x27 {
		t.Fatal("header repeated or timestamp/key wrong")
	}
}

type brokenPacketWriter struct{}

func (brokenPacketWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func TestDecoderPacketsRejectInvalidAndPropagateWrite(t *testing.T) {
	for _, b := range [][]byte{nil, {1, 2, 3}, {0, 0, 1, 5, 0x80}, {0, 0, 1, 7, 0x80}} {
		p := decoderPackets{}
		var out bytes.Buffer
		if p.write(&out, b) == nil || out.Len() != 0 || p.started {
			t.Fatal("bad picture accepted")
		}
	}
	au := []byte{0, 0, 1, 0x67, 0x64, 0, 0x28, 0x80, 0, 0, 1, 0x68, 0x80, 0, 0, 1, 0x65, 0x80}
	p := decoderPackets{}
	if !errors.Is(p.write(brokenPacketWriter{}, au), io.ErrClosedPipe) || p.started || p.frame != 0 {
		t.Fatal("write failure lost or state advanced")
	}
}
