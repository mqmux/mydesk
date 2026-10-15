package lan

import (
	"encoding/binary"
	"errors"
	"io"
)

// The network already delivers complete H.264 pictures. Preserve those
// boundaries in the local FFmpeg pipe instead of making its Annex-B parser
// wait for the following picture to discover the current one's end.
// This framing is local only: no change to encoded pixels or wire protocol.
type decoderPackets struct {
	started bool
	frame   uint64
	fps     int
}

func annexBNALs(data []byte) [][]byte {
	var nals [][]byte
	start := -1
	for i := 0; i+3 < len(data); {
		size := 0
		if data[i] == 0 && data[i+1] == 0 {
			if data[i+2] == 1 {
				size = 3
			} else if data[i+2] == 0 && data[i+3] == 1 {
				size = 4
			}
		}
		if size == 0 {
			i++
			continue
		}
		if start >= 0 && i > start {
			nals = append(nals, data[start:i])
		}
		start = i + size
		i = start
	}
	if start >= 0 && start < len(data) {
		nals = append(nals, data[start:])
	}
	return nals
}

func flvTag(payload []byte, timestamp uint32) []byte {
	n := len(payload)
	out := make([]byte, 11+n+4)
	out[0] = 9
	out[1], out[2], out[3] = byte(n>>16), byte(n>>8), byte(n)
	out[4], out[5], out[6], out[7] = byte(timestamp>>16), byte(timestamp>>8), byte(timestamp), byte(timestamp>>24)
	copy(out[11:], payload)
	binary.BigEndian.PutUint32(out[11+n:], uint32(11+n))
	return out
}

func (p *decoderPackets) write(w io.Writer, data []byte) error {
	if len(data) == 0 || len(data) > maxVideoFrame {
		return errors.New("invalid decoder picture size")
	}
	nals := annexBNALs(data)
	var sps, pps [][]byte
	key := false
	picture := false
	for _, nal := range nals {
		if len(nal) == 0 {
			continue
		}
		switch nal[0] & 31 {
		case 7:
			sps = append(sps, nal)
		case 8:
			pps = append(pps, nal)
		case 5:
			key = true
			picture = true
		case 1:
			picture = true
		}
	}
	if !picture {
		return errors.New("decoder unit has no picture")
	}
	var out []byte
	if !p.started {
		if len(sps) == 0 || len(pps) == 0 || len(sps[0]) < 4 || len(sps) > 31 || len(pps) > 255 {
			return errors.New("decoder requires initial SPS/PPS")
		}
		config := []byte{0x17, 0, 0, 0, 0, 1, sps[0][1], sps[0][2], sps[0][3], 0xff, 0xe0 | byte(len(sps))}
		for _, nal := range sps {
			if len(nal) > 65535 {
				return errors.New("SPS too large")
			}
			config = append(config, byte(len(nal)>>8), byte(len(nal)))
			config = append(config, nal...)
		}
		config = append(config, byte(len(pps)))
		for _, nal := range pps {
			if len(nal) > 65535 {
				return errors.New("PPS too large")
			}
			config = append(config, byte(len(nal)>>8), byte(len(nal)))
			config = append(config, nal...)
		}
		out = append(out, []byte{'F', 'L', 'V', 1, 1, 0, 0, 0, 9, 0, 0, 0, 0}...)
		out = append(out, flvTag(config, 0)...)
	}
	flag := byte(0x27)
	if key {
		flag = 0x17
	}
	payload := []byte{flag, 1, 0, 0, 0}
	for _, nal := range nals {
		var size [4]byte
		binary.BigEndian.PutUint32(size[:], uint32(len(nal)))
		payload = append(payload, size[:]...)
		payload = append(payload, nal...)
	}
	fps := p.fps
	if fps <= 0 {
		fps = 60
	}
	out = append(out, flvTag(payload, uint32(p.frame*1000/uint64(fps)))...)
	if err := writeAll(w, out); err != nil {
		return err
	}
	p.started = true
	p.frame++
	return nil
}
