//go:build windows && amd64

package lan

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"log"
	"runtime"
	"syscall"
	"time"
	"unsafe"
)

type clipState struct {
	Kind     string
	Data     []byte
	Hash     string
	Sequence uint32
}

const maxClipboardText = 2 << 20

var errClipboardTooLarge = errors.New("剪贴板内容大小超过限制")

func clipHash(kind string, data []byte) string {
	h := sha256.New()
	h.Write([]byte(kind))
	h.Write([]byte{0})
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

type clipboardAccess interface {
	snapshot(context.Context) (clipState, error)
	apply(context.Context, clipState, uint32) (uint32, error)
}
type clipJob struct {
	run    func(uintptr) (clipState, error)
	result chan clipResult
}
type clipResult struct {
	state clipState
	err   error
}
type nativeClipboard struct {
	ctx    context.Context
	jobs   chan clipJob
	cached clipState
	known  bool
}

func newNativeClipboard(ctx context.Context) *nativeClipboard {
	n := &nativeClipboard{ctx: ctx, jobs: make(chan clipJob)}
	go n.run()
	return n
}
func (n *nativeClipboard) run() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	cb := syscall.NewCallback(func(h uintptr, m uint32, w, l uintptr) uintptr {
		r, _, _ := ucall("DefWindowProcW", h, uintptr(m), w, l)
		return r
	})
	inst, _, _ := kernel32.NewProc("GetModuleHandleW").Call(0)
	name := utf16(fmt.Sprintf("MyDeskLANClipboard_%x", cb))
	wc := wndClass{Size: uint32(unsafe.Sizeof(wndClass{})), Proc: cb, Instance: inst, ClassName: name}
	ucall("RegisterClassExW", uintptr(unsafe.Pointer(&wc)))
	defer ucall("UnregisterClassW", uintptr(unsafe.Pointer(name)), inst)
	owner, _, _ := ucall("CreateWindowExW", 0, uintptr(unsafe.Pointer(name)), 0, 0, 0, 0, 0, 0, ^uintptr(2), 0, inst, 0)
	defer ucall("DestroyWindow", owner)
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-n.ctx.Done():
			return
		case job := <-n.jobs:
			var state clipState
			var err error
			if owner == 0 {
				err = errors.New("无法创建剪贴板窗口")
			} else {
				state, err = job.run(owner)
			}
			job.result <- clipResult{state, err}
		case <-tick.C:
			var msg winMessage
			for {
				r, _, _ := ucall("PeekMessageW", uintptr(unsafe.Pointer(&msg)), 0, 0, 0, 1)
				if r == 0 {
					break
				}
				ucall("TranslateMessage", uintptr(unsafe.Pointer(&msg)))
				ucall("DispatchMessageW", uintptr(unsafe.Pointer(&msg)))
			}
		}
	}
}
func (n *nativeClipboard) call(ctx context.Context, run func(uintptr) (clipState, error)) (clipState, error) {
	job := clipJob{run: run, result: make(chan clipResult, 1)}
	select {
	case n.jobs <- job:
	case <-ctx.Done():
		return clipState{}, ctx.Err()
	case <-n.ctx.Done():
		return clipState{}, n.ctx.Err()
	}
	select {
	case r := <-job.result:
		return r.state, r.err
	case <-ctx.Done():
		return clipState{}, ctx.Err()
	case <-n.ctx.Done():
		return clipState{}, n.ctx.Err()
	}
}
func clipboardSequence() uint32 { r, _, _ := ucall("GetClipboardSequenceNumber"); return uint32(r) }
func openNativeClipboard(ctx context.Context, owner uintptr) error {
	var lastErr error
	for i := 0; i < 6; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		r, _, callErr := ucall("OpenClipboard", owner)
		if r != 0 {
			return nil
		}
		lastErr = callErr
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
	opened, _, _ := ucall("GetOpenClipboardWindow")
	contentOwner, _, _ := ucall("GetClipboardOwner")
	windowProcess := func(hwnd uintptr) string {
		if hwnd == 0 {
			return "未知/无窗口"
		}
		var pid uint32
		tid, _, _ := ucall("GetWindowThreadProcessId", hwnd, uintptr(unsafe.Pointer(&pid)))
		return fmt.Sprintf("PID=%d TID=%d HWND=0x%x", pid, tid, hwnd)
	}
	return fmt.Errorf("剪贴板被占用，稍后重试（%v；打开者 %s；内容所有者 %s，不一定是占用者）", lastErr, windowProcess(opened), windowProcess(contentOwner))
}
func clipboardGlobal(format uintptr, limit int) ([]byte, error) {
	h, _, _ := ucall("GetClipboardData", format)
	if h == 0 {
		return nil, errors.New("剪贴板格式读取失败")
	}
	size, _, _ := kernel32.NewProc("GlobalSize").Call(h)
	if size == 0 {
		return nil, errors.New("剪贴板内存为空")
	}
	if size > uintptr(limit) {
		return nil, errClipboardTooLarge
	}
	p, _, _ := kernel32.NewProc("GlobalLock").Call(h)
	if p == 0 {
		return nil, errors.New("剪贴板内存被占用")
	}
	defer kernel32.NewProc("GlobalUnlock").Call(h)
	return bytes.Clone(unsafe.Slice((*byte)(unsafe.Pointer(p)), int(size))), nil
}
func clipboardAvailable(format uintptr) bool {
	r, _, _ := ucall("IsClipboardFormatAvailable", format)
	return r != 0
}
func pngClipboardFormat() uintptr {
	r, _, _ := ucall("RegisterClipboardFormatW", uintptr(unsafe.Pointer(utf16("PNG"))))
	return r
}
func validClipboardPNG(data []byte) error {
	if len(data) == 0 || len(data) > maxClipboardPNG {
		return errors.New("截图 PNG 超过 24 MiB 限制")
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return err
	}
	if cfg.Width < 1 || cfg.Height < 1 || cfg.Height > 16<<20 || cfg.Width > (16<<20)/cfg.Height {
		return errors.New("截图超过 1600 万像素限制")
	}
	return nil
}
func clipboardDIBImage(data []byte) (*image.RGBA, error) {
	bad := func() (*image.RGBA, error) { return nil, errors.New("不支持或损坏的剪贴板 DIB") }
	if len(data) < 40 {
		return bad()
	}
	hs := int(binary.LittleEndian.Uint32(data[:4]))
	if hs < 40 || hs > len(data) {
		return bad()
	}
	w, h := int(int32(binary.LittleEndian.Uint32(data[4:8]))), int(int32(binary.LittleEndian.Uint32(data[8:12])))
	top := h < 0
	if top {
		h = -h
	}
	bpp := int(binary.LittleEndian.Uint16(data[14:16]))
	compression := binary.LittleEndian.Uint32(data[16:20])
	if w < 1 || h < 1 || h > 16<<20 || w > (16<<20)/h || binary.LittleEndian.Uint16(data[12:14]) != 1 || (bpp != 24 && bpp != 32) {
		return bad()
	}
	offset := hs
	if compression == 3 {
		if bpp != 32 || len(data) < 52 || binary.LittleEndian.Uint32(data[40:44]) != 0x00ff0000 || binary.LittleEndian.Uint32(data[44:48]) != 0x0000ff00 || binary.LittleEndian.Uint32(data[48:52]) != 0x000000ff {
			return bad()
		}
		if hs == 40 {
			offset += 12
		}
	} else if compression != 0 {
		return bad()
	}
	colors := uint64(binary.LittleEndian.Uint32(data[32:36]))
	if colors > uint64((len(data)-offset)/4) {
		return bad()
	}
	offset += int(colors) * 4
	stride := ((w*bpp + 31) / 32) * 4
	if offset > len(data) || h > (len(data)-offset)/stride {
		return bad()
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		sy := h - 1 - y
		if top {
			sy = y
		}
		src := data[offset+sy*stride:]
		dst := img.Pix[y*img.Stride:]
		for x := 0; x < w; x++ {
			j := x * (bpp / 8)
			k := x * 4
			dst[k], dst[k+1], dst[k+2], dst[k+3] = src[j+2], src[j+1], src[j], 255
		}
	}
	return img, nil
}
func pngToClipboardDIB(data []byte) ([]byte, error) {
	if err := validClipboardPNG(data); err != nil {
		return nil, err
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	bounds := img.Bounds()
	rgba := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	draw.Draw(rgba, rgba.Bounds(), img, bounds.Min, draw.Src)
	w, h := rgba.Bounds().Dx(), rgba.Bounds().Dy()
	dib := make([]byte, 40+w*h*4)
	binary.LittleEndian.PutUint32(dib[:4], 40)
	binary.LittleEndian.PutUint32(dib[4:8], uint32(w))
	binary.LittleEndian.PutUint32(dib[8:12], uint32(h))
	binary.LittleEndian.PutUint16(dib[12:14], 1)
	binary.LittleEndian.PutUint16(dib[14:16], 32)
	binary.LittleEndian.PutUint32(dib[20:24], uint32(w*h*4))
	for y := 0; y < h; y++ {
		src := rgba.Pix[y*rgba.Stride:]
		dst := dib[40+(h-1-y)*w*4:]
		for x := 0; x < w*4; x += 4 {
			dst[x], dst[x+1], dst[x+2], dst[x+3] = src[x+2], src[x+1], src[x], src[x+3]
		}
	}
	return dib, nil
}
func allocateClipboard(data []byte) (uintptr, error) {
	h, _, _ := kernel32.NewProc("GlobalAlloc").Call(2, uintptr(len(data)))
	if h == 0 {
		return 0, errors.New("剪贴板内存分配失败")
	}
	p, _, _ := kernel32.NewProc("GlobalLock").Call(h)
	if p == 0 {
		kernel32.NewProc("GlobalFree").Call(h)
		return 0, errors.New("剪贴板内存锁定失败")
	}
	copy(unsafe.Slice((*byte)(unsafe.Pointer(p)), len(data)), data)
	kernel32.NewProc("GlobalUnlock").Call(h)
	return h, nil
}

// Only clone raw bytes while the system clipboard is open. Conversion, PNG
// encoding and hashing happen after CloseClipboard, on our private buffers.
type clipboardReader interface {
	open(context.Context, uintptr) error
	close() error
	sequence() uint32
	available(uintptr) bool
	global(uintptr, int) ([]byte, error)
	pngFormat() uintptr
}

type winClipboardReader struct{}

func (winClipboardReader) open(ctx context.Context, owner uintptr) error {
	return openNativeClipboard(ctx, owner)
}
func (winClipboardReader) close() error {
	if r, _, err := ucall("CloseClipboard"); r == 0 {
		return fmt.Errorf("关闭剪贴板失败: %w", err)
	}
	return nil
}
func (winClipboardReader) sequence() uint32         { return clipboardSequence() }
func (winClipboardReader) available(f uintptr) bool { return clipboardAvailable(f) }
func (winClipboardReader) global(f uintptr, limit int) ([]byte, error) {
	return clipboardGlobal(f, limit)
}
func (winClipboardReader) pngFormat() uintptr { return pngClipboardFormat() }

func readClipboardRaw(ctx context.Context, owner uintptr, reader clipboardReader, skipPNG, skipDIB bool) (state clipState, err error) {
	if err = reader.open(ctx, owner); err != nil {
		return state, err
	}
	defer func() {
		if closeErr := reader.close(); closeErr != nil {
			err = closeErr
		}
	}()
	state.Kind = "ignore"
	var format uintptr
	limit := maxClipboardText
	if !reader.available(15) {
		pf := reader.pngFormat()
		switch {
		case !skipPNG && pf != 0 && reader.available(pf):
			state.Kind, format, limit = "png", pf, maxClipboardPNG
		case !skipDIB && (reader.available(17) || reader.available(8)):
			state.Kind, format, limit = "dib", 8, 64<<20
			if reader.available(17) {
				format = 17
			}
		case reader.available(13):
			state.Kind, format = "utf16", 13
		}
	}
	if format != 0 {
		state.Data, err = reader.global(format, limit)
	}
	// GetClipboardData may materialize a delayed/synthesized format and
	// legitimately advance the sequence while we hold the clipboard lock.
	state.Sequence = reader.sequence()
	return state, err
}

func decodeClipboardText(data []byte) ([]byte, error) {
	if len(data) > maxClipboardText {
		return nil, errClipboardTooLarge
	}
	if len(data) < 2 || len(data)%2 != 0 {
		return nil, errors.New("损坏的 UTF16 剪贴板文本")
	}
	chars := make([]uint16, 0, len(data)/2)
	terminated := false
	for i := 0; i < len(data); i += 2 {
		ch := binary.LittleEndian.Uint16(data[i:])
		if ch == 0 {
			terminated = true
			break
		}
		chars = append(chars, ch)
	}
	if !terminated {
		return nil, errors.New("剪贴板文本没有结束符")
	}
	out := []byte(syscall.UTF16ToString(chars))
	if len(out) > maxClipboardText {
		return nil, errClipboardTooLarge
	}
	return out, nil
}

func readClipboardSnapshot(ctx context.Context, owner uintptr, reader clipboardReader) (clipState, error) {
	var skipPNG, skipDIB bool
	var firstSequence uint32
	for attempt := 0; attempt < 3; attempt++ {
		state, err := readClipboardRaw(ctx, owner, reader, skipPNG, skipDIB)
		if err != nil && !errors.Is(err, errClipboardTooLarge) {
			return clipState{}, err
		}
		if attempt != 0 && state.Sequence != firstSequence {
			return clipState{}, errors.New("复制内容已变化，重新读取")
		}
		firstSequence = state.Sequence
		if err == nil {
			switch state.Kind {
			case "png":
				err = validClipboardPNG(state.Data)
				state.Kind = "image"
				if err != nil {
					skipPNG = true
					continue
				}
			case "dib":
				var img *image.RGBA
				img, err = clipboardDIBImage(state.Data)
				if err == nil {
					var out bytes.Buffer
					enc := png.Encoder{CompressionLevel: png.BestSpeed}
					err = enc.Encode(&out, img)
					state.Data = out.Bytes()
					if err == nil {
						err = validClipboardPNG(state.Data)
					}
				}
				state.Kind = "image"
				if err != nil {
					skipDIB = true
					continue
				}
			case "utf16":
				state.Data, err = decodeClipboardText(state.Data)
				state.Kind = "text"
			}
		}
		if err != nil {
			if !errors.Is(err, errClipboardTooLarge) {
				return clipState{}, err
			}
			// Unsupported/oversize copies stay local. Cache their sequence so
			// they cannot poison every later poll or erase the local clipboard.
			state.Kind, state.Data = "ignore", nil
		}
		state.Hash = clipHash(state.Kind, state.Data)
		if reader.sequence() != state.Sequence {
			return clipState{}, errors.New("复制内容已变化，重新读取")
		}
		return state, ctx.Err()
	}
	return clipState{}, errors.New("无法读取剪贴板格式")
}

func (n *nativeClipboard) snapshot(ctx context.Context) (clipState, error) {
	return n.call(ctx, func(owner uintptr) (clipState, error) {
		seq := clipboardSequence()
		if n.known && n.cached.Sequence == seq {
			return n.cached, nil
		}
		state, err := readClipboardSnapshot(ctx, owner, winClipboardReader{})
		if err == nil {
			n.cached, n.known = state, true
		}
		return state, err
	})
}
func (n *nativeClipboard) apply(ctx context.Context, state clipState, expected uint32) (uint32, error) {
	result, err := n.call(ctx, func(owner uintptr) (result clipState, err error) {
		var formats []uintptr
		var buffers [][]byte
		switch state.Kind {
		case "text":
			if len(state.Data) > maxClipboardText {
				return clipState{}, errors.New("文本过大")
			}
			chars, err := syscall.UTF16FromString(string(state.Data))
			if err != nil {
				return clipState{}, err
			}
			if len(chars)*2 > maxClipboardText {
				return clipState{}, errors.New("文本 UTF16 超过 2 MiB")
			}
			data := make([]byte, len(chars)*2)
			for i, c := range chars {
				binary.LittleEndian.PutUint16(data[i*2:], c)
			}
			formats = []uintptr{13}
			buffers = [][]byte{data}
		case "image":
			dib, err := pngToClipboardDIB(state.Data)
			if err != nil {
				return clipState{}, err
			}
			pf := pngClipboardFormat()
			if pf == 0 {
				return clipState{}, errors.New("PNG 剪贴板格式注册失败")
			}
			formats = []uintptr{pf, 8}
			buffers = [][]byte{state.Data, dib}
		default:
			return clipState{}, errors.New("不支持的剪贴板类型（不传文件）")
		}
		handles := make([]uintptr, len(buffers))
		defer func() {
			for _, h := range handles {
				if h != 0 {
					kernel32.NewProc("GlobalFree").Call(h)
				}
			}
		}()
		for i, b := range buffers {
			h, err := allocateClipboard(b)
			if err != nil {
				return clipState{}, err
			}
			handles[i] = h
		}
		if err := openNativeClipboard(ctx, owner); err != nil {
			return clipState{}, err
		}
		defer func() {
			if closeErr := (winClipboardReader{}).close(); closeErr != nil {
				err = closeErr
			}
		}()
		if clipboardSequence() != expected {
			return clipState{}, errors.New("本地已有新复制内容，放弃旧同步")
		}
		if err := ctx.Err(); err != nil {
			return clipState{}, err
		}
		n.known = false
		if r, _, _ := ucall("EmptyClipboard"); r == 0 {
			return clipState{}, errors.New("清空剪贴板失败")
		}
		for i, f := range formats {
			r, _, _ := ucall("SetClipboardData", f, handles[i])
			if r == 0 {
				return clipState{}, errors.New("写入剪贴板失败，将重试")
			}
			handles[i] = 0
		}
		return clipState{Sequence: clipboardSequence()}, nil
	})
	return result.Sequence, err
}

func serveClipboard(parent context.Context, w *wire, m message, store clipboardAccess) error {
	ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
	defer cancel()
	switch m.Type {
	case "clip-get":
		state, err := store.snapshot(ctx)
		if err != nil {
			return err
		}
		kind := state.Kind
		if m.Hash == state.Hash || kind == "ignore" {
			return w.send(message{Type: "clip-unchanged", Kind: kind, Sequence: state.Sequence, Hash: state.Hash}, nil)
		}
		if err := w.send(message{Type: "clip", Kind: kind, Size: int64(len(state.Data)), Sequence: state.Sequence, Hash: state.Hash}, nil); err != nil {
			return err
		}
		if err := sendBulk(ctx, w, bytes.NewReader(state.Data), int64(len(state.Data)), nil); err != nil {
			return err
		}
		current, err := store.snapshot(ctx)
		if err != nil {
			return err
		}
		return w.send(message{Type: "clip-current", Sequence: current.Sequence}, nil)
	case "clip-put":
		limit := int64(maxClipboardPNG)
		if m.Kind == "text" {
			limit = 2 << 20
		} else if m.Kind != "image" {
			return errors.New("不支持文件剪贴板")
		}
		var out bytes.Buffer
		if err := receiveBulk(ctx, w, &out, m.Size, limit, nil); err != nil {
			return err
		}
		state := clipState{Kind: m.Kind, Data: out.Bytes(), Hash: m.Hash}
		if clipHash(state.Kind, state.Data) != state.Hash {
			return errors.New("剪贴板内容校验失败")
		}
		if err := w.send(message{Type: "clip-verified"}, nil); err != nil {
			return err
		}
		commit, _, err := w.read()
		if err != nil {
			return err
		}
		if commit.Type != "clip-commit" {
			return errors.New("同步已取消，源复制内容已变化")
		}
		seq, err := store.apply(ctx, state, m.Sequence)
		if err != nil {
			return fmt.Errorf("写入被控端剪贴板: %w", err)
		}
		return w.send(message{Type: "complete", Sequence: seq}, nil)
	default:
		return errors.New("无效剪贴板请求")
	}
}

func (s *controllerSession) getClipboard(ctx context.Context, hash string) (clipState, error) {
	w, close, err := s.openAux(ctx)
	if err != nil {
		return clipState{}, err
	}
	defer close()
	if err := w.send(message{Type: "clip-get", Hash: hash}, nil); err != nil {
		return clipState{}, err
	}
	m, _, err := w.read()
	if err != nil {
		return clipState{}, err
	}
	if m.Type == "error" {
		return clipState{}, errors.New(m.Text)
	}
	state := clipState{Kind: m.Kind, Hash: m.Hash, Sequence: m.Sequence}
	if m.Type == "clip-unchanged" {
		return state, nil
	}
	if m.Type != "clip" {
		return clipState{}, errors.New("无效剪贴板响应")
	}
	limit := int64(maxClipboardPNG)
	if m.Kind == "text" {
		limit = 2 << 20
	} else if m.Kind != "image" {
		return clipState{}, errors.New("不支持剪贴板类型")
	}
	var out bytes.Buffer
	if err := receiveBulk(ctx, w, &out, m.Size, limit, nil); err != nil {
		return clipState{}, err
	}
	state.Data = out.Bytes()
	if clipHash(state.Kind, state.Data) != state.Hash {
		return clipState{}, errors.New("剪贴板摘要不一致")
	}
	current, _, err := w.read()
	if err != nil {
		return clipState{}, err
	}
	if current.Type != "clip-current" || current.Sequence != state.Sequence {
		return clipState{}, errors.New("远端已有新复制内容，重新获取")
	}
	return state, nil
}
func (s *controllerSession) putClipboard(ctx context.Context, state clipState, expected uint32, local clipboardAccess) error {
	w, close, err := s.openAux(ctx)
	if err != nil {
		return err
	}
	defer close()
	if err := w.send(message{Type: "clip-put", Kind: state.Kind, Sequence: expected, Size: int64(len(state.Data)), Hash: state.Hash}, nil); err != nil {
		return err
	}
	if err := sendBulk(ctx, w, bytes.NewReader(state.Data), int64(len(state.Data)), nil); err != nil {
		return err
	}
	m, _, err := w.read()
	if err != nil {
		return err
	}
	if m.Type == "error" {
		return errors.New(m.Text)
	}
	if m.Type != "clip-verified" {
		return errors.New("剪贴板未完成校验")
	}
	current, err := local.snapshot(ctx)
	if err != nil {
		return fmt.Errorf("校验主控端源剪贴板: %w", err)
	}
	if current.Sequence != state.Sequence {
		return errors.New("本地已有新复制内容，取消旧同步")
	}
	if err := w.send(message{Type: "clip-commit"}, nil); err != nil {
		return err
	}
	m, _, err = w.read()
	if err != nil {
		return err
	}
	if m.Type == "error" {
		return errors.New(m.Text)
	}
	if m.Type != "complete" {
		return errors.New("剪贴板未确认完成")
	}
	return nil
}

type clipboardUpload struct {
	source         clipState
	remoteSequence uint32
}

type clipboardSyncState struct {
	localHash, remoteHash string
	localSequence         uint32
	localKnown            bool
	pending               *clipboardUpload
}

func (s *clipboardSyncState) rememberLocal(current clipState) {
	s.localHash, s.localSequence, s.localKnown = current.Hash, current.Sequence, true
}

func syncClipboardRound(ctx context.Context, s *controllerSession, local clipboardAccess, syncState *clipboardSyncState) error {
	remote, err := s.getClipboard(ctx, syncState.remoteHash)
	if err != nil {
		return fmt.Errorf("读取被控端剪贴板: %w", err)
	}
	current, err := local.snapshot(ctx)
	if err != nil {
		return fmt.Errorf("读取主控端剪贴板: %w", err)
	}
	if pending := syncState.pending; pending != nil {
		if current.Hash != pending.source.Hash {
			// A genuinely new local copy replaces the old attempted upload.
			syncState.pending = nil
		} else if remote.Sequence != pending.remoteSequence && remote.Hash != pending.source.Hash {
			// An unsuccessful/uncertain upload must not be retargeted to a newer
			// destination on the next poll. Consume the old source and let the
			// remote copy win, including files/unsupported formats. A lost ack
			// with the source still present remotely is handled below without echo.
			syncState.rememberLocal(current)
			syncState.pending = nil
			if remote.Kind != "ignore" && remote.Hash == syncState.remoteHash {
				// clip-unchanged has no body. A remote re-copy of the previously
				// observed value still supersedes this failed upload; fetch its
				// body on the next poll instead of applying an empty placeholder.
				syncState.remoteHash = ""
				return nil
			}
		}
	}
	// A newly copied local file/unsupported format must not be erased by a
	// remote snapshot from the same polling round. It is not transferred.
	if current.Kind == "ignore" && syncState.localKnown && (current.Sequence != syncState.localSequence || current.Hash != syncState.localHash) {
		syncState.rememberLocal(current)
		syncState.remoteHash = remote.Hash
		return nil
	}
	if current.Kind != "ignore" && current.Hash != syncState.localHash && current.Hash != remote.Hash {
		if syncState.pending == nil {
			syncState.pending = &clipboardUpload{source: current, remoteSequence: remote.Sequence}
		}
		if err := s.putClipboard(ctx, current, syncState.pending.remoteSequence, local); err != nil {
			return fmt.Errorf("主控端→被控端剪贴板同步: %w", err)
		}
		syncState.rememberLocal(current)
		syncState.remoteHash = current.Hash
		syncState.pending = nil
		return nil
	}
	if remote.Kind != "ignore" && remote.Hash != syncState.remoteHash && remote.Hash != current.Hash {
		seq, err := local.apply(ctx, remote, current.Sequence)
		if err != nil {
			return fmt.Errorf("写入主控端剪贴板: %w", err)
		}
		remote.Sequence = seq
		syncState.rememberLocal(remote)
		syncState.remoteHash = remote.Hash
		syncState.pending = nil
		return nil
	}
	syncState.rememberLocal(current)
	syncState.remoteHash = remote.Hash
	syncState.pending = nil
	return nil
}

func (s *controllerSession) imageClipboardLoop() {
	local := s.clip
	if local == nil {
		local = newNativeClipboard(s.ctx)
	}
	local = guardedClipboard{local, &s.clipboardGate, &s.clipboardDisabled}
	var syncState clipboardSyncState
	var lastLog time.Time
	tick := time.NewTicker(600 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-tick.C:
			if s.clipboardDisabled.Load() {
				continue
			}
			ctx, cancel := context.WithTimeout(s.ctx, 2*time.Minute)
			s.clipboardGate.Lock()
			s.clipboardCancel = cancel
			if s.clipboardDisabled.Load() {
				cancel()
			}
			s.clipboardGate.Unlock()
			err := syncClipboardRound(ctx, s, local, &syncState)
			cancel()
			s.clipboardGate.Lock()
			s.clipboardCancel = nil
			s.clipboardGate.Unlock()
			if err != nil && s.ctx.Err() == nil && !s.clipboardDisabled.Load() {
				if time.Since(lastLog) > 5*time.Second {
					log.Printf("[lan][clipboard] %v；后续轮询继续重试", err)
					lastLog = time.Now()
				}
				s.setClipboardNotice("剪贴板重试中（见日志）")
			} else {
				s.setClipboardNotice("")
			}
		}
	}
}
