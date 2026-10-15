//go:build windows && amd64

package lan

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

const bulkChunk = 128 << 10
const maxClipboardPNG = 24 << 20

type measuredConn struct {
	net.Conn
	rx, tx atomic.Uint64
}

func (c *measuredConn) Read(b []byte) (int, error) {
	n, e := c.Conn.Read(b)
	c.rx.Add(uint64(n))
	return n, e
}
func (c *measuredConn) Write(b []byte) (int, error) {
	n, e := c.Conn.Write(b)
	c.tx.Add(uint64(n))
	return n, e
}
func meter(c net.Conn) *measuredConn {
	if t, ok := c.(*tls.Conn); ok {
		m, _ := t.NetConn().(*measuredConn)
		return m
	}
	return nil
}

func (s *controllerSession) openAux(ctx context.Context) (*wire, func(), error) {
	c, err := dialTLSLogged(ctx, s.ctrl.conn.RemoteAddr().String(), false, s.ctrl.conn.LocalAddr().(*net.TCPAddr).IP)
	if err != nil {
		return nil, nil, err
	}
	m := meter(c)
	s.auxMu.Lock()
	if s.auxMeters == nil {
		s.auxMeters = map[*measuredConn]bool{}
	}
	if m != nil {
		s.auxMeters[m] = true
	}
	s.auxMu.Unlock()
	stop := context.AfterFunc(ctx, func() { c.Close() })
	stopSession := context.AfterFunc(s.ctx, func() { c.Close() })
	close := func() {
		stop()
		stopSession()
		c.Close()
		s.auxMu.Lock()
		if m != nil && s.auxMeters[m] {
			s.auxRx += m.rx.Load()
			s.auxTx += m.tx.Load()
			delete(s.auxMeters, m)
		}
		s.auxMu.Unlock()
	}
	w := &wire{conn: c, timeout: 30 * time.Second}
	if err = w.send(message{Type: "attach-aux", Version: protocolVersion, Proof: mustProof(c, s.welcome.Text, "aux")}, nil); err == nil {
		var reply message
		reply, _, err = w.read()
		if err == nil && reply.Type != "attached" {
			err = errors.New("辅助传输认证失败")
		}
	}
	if err != nil {
		close()
		return nil, nil, err
	}
	return w, close, nil
}

func (h *host) acceptAux(c *tls.Conn, w *wire, m message) {
	h.mu.Lock()
	s := h.session
	h.mu.Unlock()
	if s == nil || m.Version != protocolVersion || !checkProof(m.Proof, mustProof(c, s.token, "aux")) {
		return
	}
	if !c.RemoteAddr().(*net.TCPAddr).IP.Equal(s.ctrl.conn.RemoteAddr().(*net.TCPAddr).IP) {
		return
	}
	if s.auxActive.Add(1) > 6 {
		s.auxActive.Add(-1)
		return
	}
	defer s.auxActive.Add(-1)
	stop := context.AfterFunc(s.ctx, func() { c.Close() })
	defer stop()
	w.timeout = 30 * time.Second
	if err := w.send(message{Type: "attached"}, nil); err != nil {
		return
	}
	request, _, err := w.read()
	if err != nil {
		return
	}
	if strings.HasPrefix(request.Type, "clip-") {
		s.clipOnce.Do(func() {
			if h.clipStore != nil {
				s.clip = h.clipStore
			} else {
				s.clip = newNativeClipboard(s.ctx)
			}
		})
		err = serveClipboard(s.ctx, w, request, guardedClipboard{s.clip, &s.clipboardGate, &s.clipboardDisabled})
	} else if request.Type == "command-run" {
		if s.directories != nil {
			if request.Path == "" {
				request.Path = programDirectory()
			}
			if _, validateErr := validRemoteDirectory(request.Path); validateErr == nil {
				if saveErr := s.directories.remember(request.Path, true); saveErr != nil {
					log.Printf("[lan][路径] %v", saveErr)
				}
			}
		}
		err = serveCommand(s.ctx, w, request)
	} else if s.directories != nil && (request.Type == "file-default" || request.Type == "file-list" || request.Type == "command-default" || request.Type == "command-directory") {
		err = s.serveDirectoryRequest(s.ctx, w, request)
	} else {
		err = serveFiles(s.ctx, w, request)
	}
	if err != nil && s.ctx.Err() == nil {
		_ = w.send(message{Type: "error", Text: err.Error()}, nil)
	}
}

type fileEntry struct {
	Name      string
	Path      string
	Directory bool
	Size      int64
	Modified  int64
}
type directoryListing struct {
	Path    string
	Entries []fileEntry
}

func safeTransferPath(path string) (string, error) {
	if path == "" || strings.IndexByte(path, 0) >= 0 || strings.HasPrefix(path, `\\?\`) || strings.HasPrefix(path, `\\.\`) {
		return "", errors.New("无效文件路径")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if strings.Contains(strings.TrimPrefix(abs, filepath.VolumeName(abs)), ":") {
		return "", errors.New("不支持设备路径或备用数据流")
	}
	return filepath.Clean(abs), nil
}

func listRemoteDirectory(path string) (directoryListing, error) {
	if path == "" {
		var out directoryListing
		mask, _, _ := kernel32.NewProc("GetLogicalDrives").Call()
		for i := 0; i < 26; i++ {
			if mask&(1<<i) != 0 {
				p := fmt.Sprintf("%c:\\", 'A'+i)
				out.Entries = append(out.Entries, fileEntry{Name: p, Path: p, Directory: true})
			}
		}
		return out, nil
	}
	path, err := safeTransferPath(path)
	if err != nil {
		return directoryListing{}, err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return directoryListing{}, err
	}
	if len(entries) > 10000 {
		return directoryListing{}, errors.New("目录超过 10000 项，请进入子目录后浏览")
	}
	out := directoryListing{Path: path}
	for _, e := range entries {
		if e.Type()&os.ModeSymlink != 0 {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			continue
		}
		out.Entries = append(out.Entries, fileEntry{Name: e.Name(), Path: filepath.Join(path, e.Name()), Directory: info.IsDir(), Size: info.Size(), Modified: info.ModTime().Unix()})
	}
	sort.Slice(out.Entries, func(i, j int) bool {
		if out.Entries[i].Directory != out.Entries[j].Directory {
			return out.Entries[i].Directory
		}
		return strings.ToLower(out.Entries[i].Name) < strings.ToLower(out.Entries[j].Name)
	})
	return out, nil
}

func sendBulk(ctx context.Context, w *wire, r io.Reader, size int64, progress func(int64), beforeDone ...func() error) error {
	hash := sha256.New()
	buffer := make([]byte, bulkChunk)
	var sent int64
	for sent < size {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := io.ReadFull(r, buffer[:min(int64(len(buffer)), size-sent)])
		if err != nil {
			return err
		}
		hash.Write(buffer[:n])
		if err := w.send(message{Type: "chunk", Offset: sent}, buffer[:n]); err != nil {
			return err
		}
		sent += int64(n)
		if progress != nil {
			progress(sent)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, check := range beforeDone {
		if err := check(); err != nil {
			return err
		}
	}
	return w.send(message{Type: "done", Size: sent, Hash: hex.EncodeToString(hash.Sum(nil))}, nil)
}

func fileUnchanged(f *os.File, original os.FileInfo) func() error {
	return func() error {
		current, err := f.Stat()
		if err != nil {
			return err
		}
		if current.Size() != original.Size() || !current.ModTime().Equal(original.ModTime()) {
			return errors.New("源文件在传输过程中变化，请重试")
		}
		return nil
	}
}

func receiveBulk(ctx context.Context, w *wire, dst io.Writer, size, limit int64, progress func(int64)) error {
	if size < 0 || size > limit {
		return errors.New("传输大小超过限制")
	}
	hash := sha256.New()
	var received int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		m, data, err := w.read()
		if err != nil {
			return err
		}
		if m.Type == "error" {
			return errors.New(m.Text)
		}
		if m.Type == "done" {
			if len(data) != 0 || received != size || m.Size != size || m.Hash != hex.EncodeToString(hash.Sum(nil)) {
				return errors.New("传输不完整或 SHA256 校验失败")
			}
			return nil
		}
		if m.Type != "chunk" || m.Offset != received || len(data) == 0 || len(data) > bulkChunk || int64(len(data)) > size-received {
			return errors.New("无效传输分块")
		}
		if err := writeAll(dst, data); err != nil {
			return err
		}
		hash.Write(data)
		received += int64(len(data))
		if progress != nil {
			progress(received)
		}
	}
}

func replaceReceivedFile(temp, target string, overwrite bool) error {
	a, err := syscall.UTF16PtrFromString(temp)
	if err != nil {
		return err
	}
	b, err := syscall.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	flags := uintptr(8)
	if overwrite {
		flags |= 1
	}
	r, _, err := kernel32.NewProc("MoveFileExW").Call(uintptr(unsafe.Pointer(a)), uintptr(unsafe.Pointer(b)), flags)
	if r == 0 {
		return fmt.Errorf("保存文件失败（目标可能已存在或被占用）：%w", err)
	}
	return nil
}

func receiveFile(ctx context.Context, w *wire, target string, size int64, overwrite bool, progress func(int64)) error {
	target, err := safeTransferPath(target)
	if err != nil {
		return err
	}
	if st, err := os.Lstat(target); err == nil {
		if st.IsDir() || !overwrite {
			return errors.New("目标已存在；需明确确认覆盖")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(target), ".mydesk-lan-*.part")
	if err != nil {
		return err
	}
	name := f.Name()
	defer func() { f.Close(); os.Remove(name) }()
	if err := receiveBulk(ctx, w, f, size, 1<<40, progress); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return replaceReceivedFile(name, target, overwrite)
}

func serveFiles(ctx context.Context, w *wire, m message) error {
	switch m.Type {
	case "file-default":
		listing, err := listRemoteDirectory(programDirectory())
		if err != nil {
			return err
		}
		data, err := json.Marshal(listing)
		if err != nil {
			return err
		}
		return w.send(message{Type: "listing"}, data)
	case "file-list":
		listing, err := listRemoteDirectory(m.Path)
		if err != nil {
			return err
		}
		data, err := json.Marshal(listing)
		if err != nil {
			return err
		}
		return w.send(message{Type: "listing"}, data)
	case "file-download":
		path, err := safeTransferPath(m.Path)
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("仅支持普通文件下载")
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		info, err = f.Stat()
		if err != nil {
			return err
		}
		if err := w.send(message{Type: "file", Size: info.Size(), Name: info.Name()}, nil); err != nil {
			return err
		}
		return sendBulk(ctx, w, f, info.Size(), nil, fileUnchanged(f, info))
	case "file-upload":
		path, err := safeTransferPath(m.Path)
		if err != nil {
			return err
		}
		if st, err := os.Lstat(path); err == nil && (st.IsDir() || !m.Overwrite) {
			return errors.New("目标已存在，请确认覆盖后重试")
		}
		if m.Size < 0 || m.Size > 1<<40 {
			return errors.New("无效上传大小")
		}
		if err := w.send(message{Type: "ready"}, nil); err != nil {
			return err
		}
		if err := receiveFile(ctx, w, path, m.Size, m.Overwrite, nil); err != nil {
			return err
		}
		return w.send(message{Type: "complete"}, nil)
	default:
		return errors.New("不支持的文件请求")
	}
}

func (s *controllerSession) remoteList(ctx context.Context, path string) (directoryListing, error) {
	return s.remoteListRequest(ctx, path, "file-list")
}
func (s *controllerSession) remoteListRequest(ctx context.Context, path, kind string) (directoryListing, error) {
	w, close, err := s.openAux(ctx)
	if err != nil {
		return directoryListing{}, err
	}
	defer close()
	if err := w.send(message{Type: kind, Path: path}, nil); err != nil {
		return directoryListing{}, err
	}
	m, data, err := w.read()
	if err != nil {
		return directoryListing{}, err
	}
	if m.Type == "error" {
		return directoryListing{}, errors.New(m.Text)
	}
	if m.Type != "listing" {
		return directoryListing{}, errors.New("无效目录响应")
	}
	var result directoryListing
	err = json.Unmarshal(data, &result)
	return result, err
}

func (s *controllerSession) downloadFile(ctx context.Context, remote, local string, overwrite bool, progress func(int64, int64)) error {
	w, close, err := s.openAux(ctx)
	if err != nil {
		return err
	}
	defer close()
	if err := w.send(message{Type: "file-download", Path: remote}, nil); err != nil {
		return err
	}
	m, _, err := w.read()
	if err != nil {
		return err
	}
	if m.Type == "error" {
		return errors.New(m.Text)
	}
	if m.Type != "file" {
		return errors.New("无效下载响应")
	}
	return receiveFile(ctx, w, local, m.Size, overwrite, func(n int64) {
		if progress != nil {
			progress(n, m.Size)
		}
	})
}

func (s *controllerSession) uploadFile(ctx context.Context, local, remote string, overwrite bool, progress func(int64, int64)) error {
	info, err := os.Lstat(local)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("仅支持普通文件上传")
	}
	f, err := os.Open(local)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil {
		return err
	}
	w, close, err := s.openAux(ctx)
	if err != nil {
		return err
	}
	defer close()
	if err := w.send(message{Type: "file-upload", Path: remote, Size: info.Size(), Overwrite: overwrite}, nil); err != nil {
		return err
	}
	m, _, err := w.read()
	if err != nil {
		return err
	}
	if m.Type == "error" {
		return errors.New(m.Text)
	}
	if m.Type != "ready" {
		return errors.New("无效上传响应")
	}
	if err := sendBulk(ctx, w, f, info.Size(), func(n int64) {
		if progress != nil {
			progress(n, info.Size())
		}
	}, fileUnchanged(f, info)); err != nil {
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
		return errors.New("上传未确认完成")
	}
	return nil
}

func formatRate(bytes float64) string {
	if bytes >= 1024*1024 {
		return fmt.Sprintf("%.2f MB/s", bytes/1024/1024)
	}
	return fmt.Sprintf("%.1f KB/s", bytes/1024)
}
