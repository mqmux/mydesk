package lan

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultPort     = 29860
	protocolVersion = 6
	buildVersion    = "4.20-key-match"
	maxFPS          = 120
	maxWireBody     = 32 << 20
	packetSize      = 1200
	udpHeaderSize   = 40
	fragmentSize    = packetSize - udpHeaderSize - 16
	maxVideoFrame   = 8 << 20
	maxFragments    = (maxVideoFrame + fragmentSize - 1) / fragmentSize
	codecH264       = 1
	codecJPEG       = 2
	codecJPEGDelta  = 3
	packetVideo     = 1
	packetProbe     = 2
	packetProbeAck  = 3
	packetKeepalive = 4
)

type message struct {
	queued           time.Time  // Local-only, never serialized; no input content in metrics.
	Minimized        *bool      `json:"minimized,omitempty"`
	Features         []string   `json:"features,omitempty"`
	ScreenGeneration uint64     `json:"screenGeneration,omitempty"`
	Monitor          int        `json:"monitor,omitempty"`
	MonitorCount     int        `json:"monitorCount,omitempty"`
	CursorKind       string     `json:"cursorKind,omitempty"`
	CursorVisible    *bool      `json:"cursorVisible,omitempty"`
	InputEnabled     *bool      `json:"inputEnabled,omitempty"`
	ClipboardEnabled *bool      `json:"clipboardEnabled,omitempty"`
	CursorEnabled    *bool      `json:"cursorEnabled,omitempty"`
	WaitSeconds      int        `json:"waitSeconds,omitempty"`
	Path             string     `json:"path,omitempty"`
	Size             int64      `json:"size,omitempty"`
	Offset           int64      `json:"offset,omitempty"`
	Hash             string     `json:"hash,omitempty"`
	Kind             string     `json:"kind,omitempty"`
	Sequence         uint32     `json:"sequence,omitempty"`
	Overwrite        bool       `json:"overwrite,omitempty"`
	Type             string     `json:"type"`
	Version          int        `json:"version,omitempty"`
	Name             string     `json:"name,omitempty"`
	DeviceID         string     `json:"deviceID,omitempty"`
	Busy             bool       `json:"busy,omitempty"`
	Proof            string     `json:"proof,omitempty"`
	FPS              int        `json:"fps,omitempty"`
	Quality          int        `json:"quality,omitempty"`
	Bitrate          int        `json:"bitrate,omitempty"`
	Width            int        `json:"width,omitempty"`
	Height           int        `json:"height,omitempty"`
	Left             int        `json:"left,omitempty"`
	Top              int        `json:"top,omitempty"`
	Codec            byte       `json:"codec,omitempty"`
	Backend          string     `json:"backend,omitempty"`
	UDPPort          int        `json:"udpPort,omitempty"`
	Frame            uint64     `json:"frame,omitempty"`
	Epoch            uint64     `json:"epoch,omitempty"`
	Key              bool       `json:"key,omitempty"`
	Missing          []uint16   `json:"missing,omitempty"`
	Input            inputEvent `json:"input,omitempty"`
	Text             string     `json:"text,omitempty"`
	Transport        string     `json:"transport,omitempty"`
	Received         uint64     `json:"received,omitempty"`
	Dropped          uint64     `json:"dropped,omitempty"`
}

type inputEvent struct {
	Kind     string `json:"kind"`
	X        int    `json:"x,omitempty"`
	Y        int    `json:"y,omitempty"`
	Button   int    `json:"button,omitempty"`
	Delta    int    `json:"delta,omitempty"`
	VK       uint16 `json:"vk,omitempty"`
	Scan     uint16 `json:"scan,omitempty"`
	Extended bool   `json:"extended,omitempty"`
}

type wire struct {
	conn    net.Conn
	mu      sync.Mutex
	timeout time.Duration
}

func (w *wire) send(m message, body []byte) error {
	h, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if len(h) > 65536 || len(body) > maxWireBody {
		return errors.New("message exceeds protocol limit")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	timeout := w.timeout
	if timeout == 0 {
		timeout = 3 * time.Second
	}
	_ = w.conn.SetWriteDeadline(time.Now().Add(timeout))
	var p [8]byte
	binary.BigEndian.PutUint32(p[:4], uint32(len(h)))
	binary.BigEndian.PutUint32(p[4:], uint32(len(body)))
	for _, b := range [][]byte{p[:], h, body} {
		if err := writeAll(w.conn, b); err != nil {
			return err
		}
	}
	return nil
}
func (w *wire) read() (message, []byte, error) {
	if w.timeout > 0 {
		_ = w.conn.SetReadDeadline(time.Now().Add(w.timeout))
	}
	var m message
	var p [8]byte
	if _, err := io.ReadFull(w.conn, p[:]); err != nil {
		return m, nil, err
	}
	h, b := binary.BigEndian.Uint32(p[:4]), binary.BigEndian.Uint32(p[4:])
	if h == 0 || h > 65536 || b > maxWireBody {
		return m, nil, errors.New("invalid message length")
	}
	hd := make([]byte, h)
	if _, err := io.ReadFull(w.conn, hd); err != nil {
		return m, nil, err
	}
	if err := json.Unmarshal(hd, &m); err != nil {
		return m, nil, err
	}
	body := make([]byte, b)
	_, err := io.ReadFull(w.conn, body)
	return m, body, err
}
func writeAll(w io.Writer, b []byte) error {
	for len(b) > 0 {
		n, err := w.Write(b)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		b = b[n:]
	}
	return nil
}

func channelProof(conn *tls.Conn, code, role string) (string, error) {
	state := conn.ConnectionState()
	material, err := state.ExportKeyingMaterial("mydesk-lan-pair-v1", nil, 32)
	if err != nil {
		return "", err
	}
	h := hmac.New(sha256.New, []byte(code))
	h.Write(material)
	h.Write([]byte(role))
	return hex.EncodeToString(h.Sum(nil)), nil
}
func checkProof(got, want string) bool {
	a, err := hex.DecodeString(got)
	if err != nil {
		return false
	}
	b, _ := hex.DecodeString(want)
	return hmac.Equal(a, b)
}

type packetCipher struct {
	aead    cipher.AEAD
	prefix  [4]byte
	counter atomic.Uint64
}

func makePacketCipher(key []byte) (*packetCipher, error) {
	if len(key) != 36 {
		return nil, errors.New("invalid session key")
	}
	b, err := aes.NewCipher(key[:32])
	if err != nil {
		return nil, err
	}
	a, err := cipher.NewGCM(b)
	if err != nil {
		return nil, err
	}
	p := &packetCipher{aead: a}
	copy(p.prefix[:], key[32:])
	return p, nil
}
func sessionCiphers(conn *tls.Conn, server bool) (*packetCipher, *packetCipher, error) {
	s := conn.ConnectionState()
	keys, err := s.ExportKeyingMaterial("mydesk-lan-udp-v1", nil, 72)
	if err != nil {
		return nil, nil, err
	}
	a, err := makePacketCipher(keys[:36])
	if err != nil {
		return nil, nil, err
	}
	b, err := makePacketCipher(keys[36:])
	if server {
		return a, b, err
	}
	return b, a, err
}

type fragment struct {
	kind, codec  byte
	key          bool
	frame, epoch uint64
	index, total uint16
	payload      []byte
}

func (p *packetCipher) seal(f fragment) []byte {
	h := make([]byte, udpHeaderSize)
	copy(h, "LND1")
	h[4] = f.kind
	h[5] = f.codec
	if f.key {
		h[6] = 1
	}
	binary.BigEndian.PutUint64(h[8:16], p.counter.Add(1))
	binary.BigEndian.PutUint64(h[16:24], f.frame)
	binary.BigEndian.PutUint64(h[24:32], f.epoch)
	binary.BigEndian.PutUint16(h[32:34], f.index)
	binary.BigEndian.PutUint16(h[34:36], f.total)
	var nonce [12]byte
	copy(nonce[:4], p.prefix[:])
	copy(nonce[4:], h[8:16])
	return p.aead.Seal(h, nonce[:], f.payload, h)
}
func (p *packetCipher) open(b []byte) (fragment, error) {
	var f fragment
	if len(b) < udpHeaderSize+16 || len(b) > packetSize || string(b[:4]) != "LND1" {
		return f, errors.New("invalid datagram")
	}
	var nonce [12]byte
	copy(nonce[:4], p.prefix[:])
	copy(nonce[4:], b[8:16])
	raw, err := p.aead.Open(nil, nonce[:], b[udpHeaderSize:], b[:udpHeaderSize])
	if err != nil {
		return f, err
	}
	f = fragment{kind: b[4], codec: b[5], key: b[6]&1 != 0, frame: binary.BigEndian.Uint64(b[16:24]), epoch: binary.BigEndian.Uint64(b[24:32]), index: binary.BigEndian.Uint16(b[32:34]), total: binary.BigEndian.Uint16(b[34:36]), payload: raw}
	if f.kind == packetVideo && (f.total == 0 || int(f.total) > maxFragments || f.index >= f.total || len(raw) == 0) {
		return fragment{}, errors.New("invalid fragment count")
	}
	return f, nil
}
func splitFrame(p *packetCipher, f videoFrame) [][]byte {
	n := (len(f.data) + fragmentSize - 1) / fragmentSize
	out := make([][]byte, 0, n)
	for i := 0; i < n; i++ {
		end := (i + 1) * fragmentSize
		if end > len(f.data) {
			end = len(f.data)
		}
		out = append(out, p.seal(fragment{kind: packetVideo, codec: f.codec, key: f.key, frame: f.id, epoch: f.epoch, index: uint16(i), total: uint16(n), payload: f.data[i*fragmentSize : end]}))
	}
	return out
}

type videoFrame struct {
	id, epoch uint64
	codec     byte
	key       bool
	data      []byte
	at        time.Time
}
type partialFrame struct {
	meta            fragment
	parts           [][]byte
	count, bytes    int
	first, lastNack time.Time
}
type assembler struct {
	frames    map[uint64]*partialFrame
	completed map[uint64]time.Time
	bytes     int
}

func newAssembler() *assembler {
	return &assembler{frames: make(map[uint64]*partialFrame), completed: make(map[uint64]time.Time)}
}
func (a *assembler) add(f fragment, now time.Time) (*videoFrame, error) {
	if f.kind != packetVideo {
		return nil, nil
	}
	if _, ok := a.completed[f.frame]; ok {
		return nil, nil
	}
	p := a.frames[f.frame]
	if p == nil {
		if len(a.frames) >= 16 || a.bytes > 24<<20 {
			return nil, errors.New("reassembly window full")
		}
		p = &partialFrame{meta: f, parts: make([][]byte, f.total), first: now}
		a.frames[f.frame] = p
	}
	if p.meta.total != f.total || p.meta.codec != f.codec || p.meta.epoch != f.epoch || p.meta.key != f.key {
		return nil, errors.New("inconsistent fragments")
	}
	if p.parts[f.index] != nil {
		return nil, nil
	}
	if p.bytes+len(f.payload) > maxVideoFrame {
		return nil, errors.New("frame exceeds size limit")
	}
	p.parts[f.index] = f.payload
	p.count++
	p.bytes += len(f.payload)
	a.bytes += len(f.payload)
	if p.count != len(p.parts) {
		return nil, nil
	}
	b := make([]byte, 0, p.bytes)
	for _, part := range p.parts {
		b = append(b, part...)
	}
	a.remove(f.frame)
	a.completed[f.frame] = now
	return &videoFrame{id: f.frame, epoch: f.epoch, codec: f.codec, key: f.key, data: b, at: now}, nil
}
func (a *assembler) remove(id uint64) {
	if p := a.frames[id]; p != nil {
		a.bytes -= p.bytes
		delete(a.frames, id)
	}
}
func (a *assembler) maintenance(now time.Time) ([]message, int) {
	for id, at := range a.completed {
		if now.Sub(at) > time.Second {
			delete(a.completed, id)
		}
	}
	var out []message
	expired := 0
	for id, p := range a.frames {
		age := now.Sub(p.first)
		if age > 100*time.Millisecond {
			a.remove(id)
			a.completed[id] = now
			expired++
			continue
		}
		if age < 12*time.Millisecond || (!p.lastNack.IsZero() && now.Sub(p.lastNack) < 20*time.Millisecond) {
			continue
		}
		missing := make([]uint16, 0, 64)
		for i, b := range p.parts {
			if b == nil {
				missing = append(missing, uint16(i))
				if len(missing) == 256 {
					break
				}
			}
		}
		out = append(out, message{Type: "nack", Frame: id, Missing: missing})
		p.lastNack = now
	}
	return out, expired
}

func randomCode() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func addressWithPort(host string, port int) string {
	if _, _, err := net.SplitHostPort(host); err == nil {
		return host
	}
	return net.JoinHostPort(host, fmt.Sprint(port))
}
