package lan

import (
	"encoding/binary"
	"errors"
	"io"
)

// FLV is only the local encoder pipe envelope. Each AVC tag is one complete
// picture; its NAL bytes are converted back to the existing Annex-B wire format
// immediately, without waiting for the next picture's AUD or for EOF.
func readEncoderPackets(r io.Reader, emit func([]byte, bool) error) error {
	var header [9]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return err
	}
	offset := binary.BigEndian.Uint32(header[5:])
	if string(header[:3]) != "FLV" || header[3] != 1 || header[4]&1 == 0 || offset < 9 || offset > 65536 {
		return errors.New("invalid encoder FLV header")
	}
	if _, err := io.CopyN(io.Discard, r, int64(offset-9)); err != nil {
		return err
	}
	var previous [4]byte
	if _, err := io.ReadFull(r, previous[:]); err != nil {
		return err
	}
	if binary.BigEndian.Uint32(previous[:]) != 0 {
		return errors.New("invalid initial FLV tag size")
	}
	var avc encoderAVC
	for {
		var tag [11]byte
		if _, err := io.ReadFull(r, tag[:]); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		size := int(tag[1])<<16 | int(tag[2])<<8 | int(tag[3])
		if size > maxVideoFrame || tag[8] != 0 || tag[9] != 0 || tag[10] != 0 || (tag[0] != 9 && tag[0] != 18) {
			return errors.New("invalid encoder FLV tag")
		}
		payload := make([]byte, size)
		if _, err := io.ReadFull(r, payload); err != nil {
			return err
		}
		if _, err := io.ReadFull(r, previous[:]); err != nil {
			return err
		}
		if binary.BigEndian.Uint32(previous[:]) != uint32(11+size) {
			return errors.New("invalid encoder FLV tag size")
		}
		if tag[0] == 18 { // Bounded optional script metadata, never a picture.
			continue
		}
		if len(payload) < 5 || payload[0]&15 != 7 || payload[0]>>4 < 1 || payload[0]>>4 > 2 {
			return errors.New("encoder pipe requires AVC video")
		}
		// No B frames: network/decoder timestamps are synthesized in display
		// order. Refuse a reordered stream rather than silently lose its PTS.
		if payload[2] != 0 || payload[3] != 0 || payload[4] != 0 {
			return errors.New("reordered encoder pictures are unsupported")
		}
		switch payload[1] {
		case 0:
			if err := avc.configure(payload[5:]); err != nil {
				return err
			}
		case 1:
			data, key, err := avc.picture(payload[5:])
			if err != nil {
				return err
			}
			if err := emit(data, key); err != nil {
				return err
			}
		case 2: // Optional muxer end-of-sequence, not a frame.
		default:
			return errors.New("invalid encoder AVC packet type")
		}
	}
}

type encoderAVC struct {
	lengthSize int
	parameters []byte
	started    bool
}

func (a *encoderAVC) configure(b []byte) error {
	if len(b) < 7 || b[0] != 1 || b[4]&0xfc != 0xfc || b[5]&0xe0 != 0xe0 {
		return errors.New("invalid encoder AVC configuration")
	}
	lengthSize := int(b[4]&3) + 1
	if lengthSize == 3 {
		return errors.New("invalid AVC NAL length size")
	}
	pos := 6
	var parameters []byte
	read := func(count int, kind byte) error {
		if count == 0 {
			return errors.New("missing encoder SPS/PPS")
		}
		for i := 0; i < count; i++ {
			if pos+2 > len(b) {
				return io.ErrUnexpectedEOF
			}
			n := int(binary.BigEndian.Uint16(b[pos:]))
			pos += 2
			if n == 0 || n > len(b)-pos || b[pos]&31 != kind || (kind == 7 && n < 4) {
				return errors.New("invalid encoder parameter set")
			}
			parameters = append(parameters, 0, 0, 0, 1)
			parameters = append(parameters, b[pos:pos+n]...)
			pos += n
		}
		return nil
	}
	if err := read(int(b[5]&31), 7); err != nil {
		return err
	}
	if pos >= len(b) {
		return io.ErrUnexpectedEOF
	}
	count := int(b[pos])
	pos++
	if err := read(count, 8); err != nil {
		return err
	}
	// avcC may have optional high-profile chroma/bit-depth extensions after
	// PPS. The active SPS already carries those; retain its original bytes.
	a.lengthSize, a.parameters, a.started = lengthSize, parameters, false
	return nil
}

func (a *encoderAVC) picture(b []byte) ([]byte, bool, error) {
	if a.lengthSize == 0 {
		return nil, false, errors.New("encoder picture precedes AVC configuration")
	}
	out := make([]byte, 0, min(maxVideoFrame, len(b)+len(a.parameters)))
	key, picture, sps, pps := false, false, false, false
	audEnd := 0
	for len(b) > 0 {
		if len(b) < a.lengthSize {
			return nil, false, io.ErrUnexpectedEOF
		}
		n := 0
		for _, v := range b[:a.lengthSize] {
			n = n<<8 | int(v)
		}
		b = b[a.lengthSize:]
		if n == 0 || n > len(b) || len(out)+4+n > maxVideoFrame {
			return nil, false, errors.New("invalid encoder NAL size")
		}
		switch b[0] & 31 {
		case 5:
			key, picture = true, true
		case 1:
			picture = true
		case 7:
			sps = true
		case 8:
			pps = true
		case 9:
			if len(out) == 0 {
				audEnd = 4 + n
			}
		}
		out = append(out, 0, 0, 0, 1)
		out = append(out, b[:n]...)
		b = b[n:]
	}
	if !picture || (!a.started && !key) {
		return nil, false, errors.New("encoder requires an initial IDR picture")
	}
	if key && (!sps || !pps) {
		// FLV can move the encoder's SPS/PPS into avcC. Every network IDR
		// must still bootstrap a new controller decoder/reference chain.
		if len(out)+len(a.parameters) > maxVideoFrame {
			return nil, false, errors.New("encoder picture too large")
		}
		// When present, AUD must remain the first NAL of the access unit.
		withParameters := make([]byte, 0, len(out)+len(a.parameters))
		withParameters = append(withParameters, out[:audEnd]...)
		withParameters = append(withParameters, a.parameters...)
		out = append(withParameters, out[audEnd:]...)
	}
	a.started = true
	return out, key, nil
}
