//go:build windows && amd64

package lan

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

type openFileName struct {
	Size                         uint32
	Owner, Instance              uintptr
	Filter, CustomFilter         *uint16
	MaxCustomFilter, FilterIndex uint32
	File                         *uint16
	MaxFile                      uint32
	FileTitle                    *uint16
	MaxFileTitle                 uint32
	InitialDir, Title            *uint16
	Flags                        uint32
	FileOffset, FileExtension    uint16
	DefaultExtension             *uint16
	CustomData, Hook             uintptr
	Template                     *uint16
	Reserved                     uintptr
	Reserved2, FlagsEx           uint32
}

func chooseLocalFiles(owner uintptr, saveName string) ([]string, error) {
	buffer := make([]uint16, 65536)
	filter := []uint16{'所', '有', '文', '件', 0, '*', '.', '*', 0, 0}
	flags := uint32(0x80000 | 0x8 | 0x800 | 0x1000 | 0x200)
	proc := "GetOpenFileNameW"
	title := "选择上传文件（可多选）"
	if saveName != "" {
		copy(buffer, syscall.StringToUTF16(saveName))
		flags = 0x80000 | 0x8 | 0x800 | 0x2
		proc = "GetSaveFileNameW"
		title = "保存下载文件"
	}
	of := openFileName{Size: uint32(unsafe.Sizeof(openFileName{})), Owner: owner, Filter: &filter[0], File: &buffer[0], MaxFile: uint32(len(buffer)), Flags: flags, Title: utf16(title), InitialDir: utf16(programDirectory())}
	dll := syscall.NewLazyDLL("comdlg32.dll")
	r, _, _ := dll.NewProc(proc).Call(uintptr(unsafe.Pointer(&of)))
	if r == 0 {
		code, _, _ := dll.NewProc("CommDlgExtendedError").Call()
		if code != 0 {
			return nil, fmt.Errorf("文件选择对话框失败：%x", code)
		}
		return nil, nil
	}
	var parts []string
	for i := 0; i < len(buffer) && buffer[i] != 0; {
		j := i
		for j < len(buffer) && buffer[j] != 0 {
			j++
		}
		parts = append(parts, syscall.UTF16ToString(buffer[i:j]))
		i = j + 1
	}
	if len(parts) < 2 {
		return parts, nil
	}
	var paths []string
	for _, name := range parts[1:] {
		paths = append(paths, filepath.Join(parts[0], name))
	}
	return paths, nil
}

type fileManager struct {
	s                         *controllerSession
	ctx                       context.Context
	cancel                    context.CancelFunc
	mu                        sync.Mutex
	hwnd                      uintptr
	operationCancel           context.CancelFunc
	updates                   chan func()
	busy                      atomic.Bool
	listing                   directoryListing
	pathEdit, listBox, status uintptr
	buttons                   map[int]uintptr
	searchEdit, saveDirLabel  uintptr
	entries                   []fileEntry
	downloadDir               string
	darkBrush                 uintptr
	progressBar               uintptr
	listHeader                uintptr
	rows                      [][3]string
	sortColumn                int
	sortDescending            bool
}

func (s *controllerSession) openFileManager() {
	s.filesMu.Lock()
	if s.files != nil && s.files.ctx.Err() == nil {
		f := s.files
		f.mu.Lock()
		h := f.hwnd
		f.mu.Unlock()
		s.filesMu.Unlock()
		f.cancel()
		if h != 0 {
			ucall("PostMessageW", h, 0x10, 0, 0)
		}
		return
	}
	ctx, cancel := context.WithCancel(s.ctx)
	if s.downloadDir == "" {
		s.downloadDir = programDirectory()
	}
	f := &fileManager{s: s, ctx: ctx, cancel: cancel, updates: make(chan func(), 128), buttons: map[int]uintptr{}, downloadDir: s.downloadDir}
	s.files = f
	s.filesMu.Unlock()
	go func() {
		defer cancel()
		if err := f.run(); err != nil {
			log.Printf("[lan][F8] %v", err)
		}
		s.filesMu.Lock()
		if s.files == f {
			s.files = nil
		}
		s.filesMu.Unlock()
	}()
}
func (f *fileManager) post(fn func()) {
	select {
	case f.updates <- fn:
	case <-f.ctx.Done():
		return
	}
	f.mu.Lock()
	h := f.hwnd
	f.mu.Unlock()
	if h != 0 {
		ucall("PostMessageW", h, 0x8003, 0, 0)
	}
}
func (f *fileManager) setText(h uintptr, text string) {
	ucall("SetWindowTextW", h, uintptr(unsafe.Pointer(utf16(text))))
}
func controlText(h uintptr) string {
	n, _, _ := ucall("GetWindowTextLengthW", h)
	if n > 32767 {
		n = 32767
	}
	buf := make([]uint16, n+1)
	ucall("GetWindowTextW", h, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return syscall.UTF16ToString(buf)
}
func (f *fileManager) enable(enabled bool) {
	v := uintptr(0)
	if enabled {
		v = 1
	}
	for id, h := range f.buttons {
		if id != 14 {
			ucall("EnableWindow", h, v)
		}
	}
	ucall("EnableWindow", f.pathEdit, v)
	ucall("EnableWindow", f.listBox, v)
	ucall("EnableWindow", f.searchEdit, v)
	// Disabling the focused edit during a refresh otherwise leaves the dialog
	// without a keyboard target. Keep F8 usable while listing/transferring.
	if !enabled {
		ucall("SetFocus", f.buttons[14])
	}
}
func (f *fileManager) task(work func(context.Context) error) {
	if !f.busy.CompareAndSwap(false, true) {
		return
	}
	f.enable(false)
	ucall("SendMessageW", f.progressBar, 0x402, 0, 0)
	ctx, cancel := context.WithCancel(f.ctx)
	f.mu.Lock()
	f.operationCancel = cancel
	f.mu.Unlock()
	go func() {
		err := work(ctx)
		cancel()
		f.s.setTransferNotice("")
		f.post(func() {
			f.mu.Lock()
			f.operationCancel = nil
			f.mu.Unlock()
			f.busy.Store(false)
			f.enable(true)
			if err != nil {
				f.setText(f.status, "失败/已取消："+err.Error())
				log.Printf("[lan][F8] %v", err)
			}
		})
	}()
}
func (f *fileManager) applyListing(listing directoryListing) {
	if listing.Path != f.listing.Path {
		f.setText(f.searchEdit, "")
	}
	f.listing = listing
	f.setText(f.pathEdit, listing.Path)
	f.filterEntries()
}
func (f *fileManager) filterEntries() {
	selected, hadSelection := f.selected()
	f.entries = filterFileEntries(f.listing, controlText(f.searchEdit))
	sortFileEntries(f.entries, f.sortColumn, f.sortDescending)
	f.rows = make([][3]string, len(f.entries))
	index := -1
	for i, entry := range f.entries {
		f.rows[i] = fileColumns(entry)
		if hadSelection && entry.Path == selected.Path {
			index = i
		}
	}
	ucall("SendMessageW", f.listBox, 0xB, 0, 0)
	ucall("SendMessageW", f.listBox, 0x102F, uintptr(len(f.entries)), 0)
	f.selectFileRow(index)
	ucall("SendMessageW", f.listBox, 0xB, 1, 0)
	ucall("InvalidateRect", f.listBox, 0, 1)
}
func (f *fileManager) refresh(path string) {
	f.refreshRequest(path, "file-list")
}
func (f *fileManager) refreshRequest(path, kind string) {
	f.task(func(ctx context.Context) error {
		listing, err := f.s.remoteListRequest(ctx, strings.TrimSpace(path), kind)
		if err != nil {
			return err
		}
		f.post(func() {
			f.applyListing(listing)
			f.setText(f.status, fmt.Sprintf("%d 项。双击目录进入，双击文件下载；拖入本地文件上传。", len(listing.Entries)))
		})
		return nil
	})
}
func (f *fileManager) selected() (fileEntry, bool) {
	i, _, _ := ucall("SendMessageW", f.listBox, 0x100C, ^uintptr(0), 2)
	if int32(i) < 0 || i >= uintptr(len(f.entries)) {
		return fileEntry{}, false
	}
	return f.entries[int(i)], true
}
func (f *fileManager) progress(label string) func(int64, int64) {
	started := time.Now()
	last := time.Time{}
	return func(n, total int64) {
		if n < total && time.Since(last) < 100*time.Millisecond {
			return
		}
		last = time.Now()
		pct := float64(100)
		if total > 0 {
			pct = float64(n) * 100 / float64(total)
		}
		text := fmt.Sprintf("%s %.1f%% | %.2f / %.2f MB | %s", label, pct, float64(n)/1024/1024, float64(total)/1024/1024, formatRate(float64(n)/max(time.Since(started).Seconds(), 0.001)))
		if n >= total {
			text += " | 正在校验/保存…"
		}
		f.s.setTransferNotice(fmt.Sprintf("文件 %s %.0f%%", label, pct))
		f.post(func() {
			f.setText(f.status, text)
			ucall("SendMessageW", f.progressBar, 0x402, uintptr(min(1000, max(0, int(pct*10)))), 0)
		})
	}
}
func (f *fileManager) confirm(text string) bool {
	r, _, _ := ucall("MessageBoxW", f.hwnd, uintptr(unsafe.Pointer(utf16(text))), uintptr(unsafe.Pointer(utf16("确认覆盖"))), 0x24|0x100)
	return r == 6
}
func (f *fileManager) upload() {
	if f.busy.Load() {
		return
	}
	directory := f.listing.Path
	if directory == "" {
		f.setText(f.status, "请先进入远端目标目录。")
		return
	}
	paths, err := chooseLocalFiles(f.hwnd, "")
	if err != nil {
		f.setText(f.status, err.Error())
		return
	}
	if len(paths) == 0 {
		return
	}
	f.uploadPaths(paths)
}
func (f *fileManager) uploadPaths(paths []string) {
	if f.busy.Load() || len(paths) == 0 {
		return
	}
	directory := f.listing.Path
	if directory == "" {
		f.setText(f.status, "请先进入远端目标目录。")
		return
	}
	overwrites := map[string]bool{}
	for _, path := range paths {
		name := filepath.Base(path)
		for _, entry := range f.listing.Entries {
			if strings.EqualFold(entry.Name, name) {
				if entry.Directory {
					f.setText(f.status, "同名目标是目录，不能覆盖。")
					return
				}
				if !f.confirm("远端文件已存在：\n" + filepath.Join(directory, name) + "\n\n确认覆盖？") {
					return
				}
				overwrites[path] = true
			}
		}
	}
	f.task(func(ctx context.Context) error {
		for _, path := range paths {
			if err := f.s.uploadFile(ctx, path, filepath.Join(directory, filepath.Base(path)), overwrites[path], f.progress("上传 "+filepath.Base(path))); err != nil {
				return err
			}
		}
		listing, err := f.s.remoteList(ctx, directory)
		if err == nil {
			f.post(func() { f.applyListing(listing); f.setText(f.status, "上传完成，SHA256 校验通过。") })
		}
		return err
	})
}
func (f *fileManager) download() {
	if f.busy.Load() {
		return
	}
	entry, ok := f.selected()
	if !ok || entry.Directory {
		f.setText(f.status, "请选择要下载的普通文件。")
		return
	}
	target, err := defaultDownloadTarget(f.downloadDir, entry.Name)
	if err != nil {
		f.setText(f.status, err.Error())
		return
	}
	f.task(func(ctx context.Context) error {
		if err := f.s.downloadFile(ctx, entry.Path, target, false, f.progress("下载 "+entry.Name)); err != nil {
			return err
		}
		f.post(func() { f.setText(f.status, "下载完成，SHA256 校验通过："+target) })
		return nil
	})
}
func (f *fileManager) chooseSaveDir() {
	if dir := chooseDownloadFolder(f.hwnd); dir != "" {
		f.downloadDir = dir
		f.s.filesMu.Lock()
		f.s.downloadDir = dir
		f.s.filesMu.Unlock()
		f.setText(f.saveDirLabel, "保存到："+dir)
	}
}
func (f *fileManager) layout(h uintptr) {
	var r rect
	ucall("GetClientRect", h, uintptr(unsafe.Pointer(&r)))
	w, ht := int(r.Right), int(r.Bottom)
	if f.pathEdit == 0 {
		return
	}
	ucall("MoveWindow", f.pathEdit, 12, 12, uintptr(max(100, w-24)), 28, 1)
	for id, button := range f.buttons {
		ucall("MoveWindow", button, uintptr(12+(id-10)*94), 48, 88, 28, 1)
	}
	ucall("MoveWindow", f.saveDirLabel, 12, 84, uintptr(max(100, w-24)), 24, 1)
	ucall("MoveWindow", f.searchEdit, 12, 112, uintptr(max(100, w-24)), 28, 1)
	ucall("MoveWindow", f.listBox, 12, 148, uintptr(max(100, w-24)), uintptr(max(40, ht-230)), 1)
	ucall("SendMessageW", f.listBox, 0x101E, 0, uintptr(max(140, w-320)))
	ucall("MoveWindow", f.status, 12, uintptr(max(100, ht-74)), uintptr(max(100, w-24)), 32, 1)
	ucall("MoveWindow", f.progressBar, 12, uintptr(max(140, ht-36)), uintptr(max(100, w-24)), 22, 1)
}
func (f *fileManager) run() error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	releaseControls, err := activateListControls()
	if err != nil {
		return fmt.Errorf("初始化文件列表：%w", err)
	}
	defer releaseControls()
	common := struct{ Size, Classes uint32 }{8, 0x21}
	syscall.NewLazyDLL("comctl32.dll").NewProc("InitCommonControlsEx").Call(uintptr(unsafe.Pointer(&common)))
	f.darkBrush, _, _ = gcall("CreateSolidBrush", darkBackground)
	defer gcall("DeleteObject", f.darkBrush)
	cb := syscall.NewCallback(func(h uintptr, m uint32, w, l uintptr) uintptr {
		switch m {
		case 0x14:
			var r rect
			ucall("GetClientRect", h, uintptr(unsafe.Pointer(&r)))
			ucall("FillRect", w, uintptr(unsafe.Pointer(&r)), f.darkBrush)
			return 1
		case 0x133, 0x134, 0x135, 0x136, 0x138:
			return darkControlColors(w, f.darkBrush)
		case 0x2B:
			paintDarkButton((*drawItem)(unsafe.Pointer(l)))
			return 1
		case 0x4E:
			return f.listNotify(l)
		case 0x233:
			paths := droppedFiles(w)
			syscall.NewLazyDLL("shell32.dll").NewProc("DragFinish").Call(w)
			f.uploadPaths(paths)
			return 0
		case 5:
			f.layout(h)
			return 0
		case 0x8003:
			for {
				select {
				case fn := <-f.updates:
					fn()
				default:
					return 0
				}
			}
		case 0x111:
			id, notification := int(w&65535), int((w>>16)&65535)
			if id == 4 && notification == 0x300 {
				f.filterEntries()
				return 0
			}
			if notification == 0 {
				switch id {
				case 10:
					f.refresh(controlText(f.pathEdit))
				case 11:
					p := f.listing.Path
					if p != "" {
						parent := filepath.Dir(p)
						if strings.EqualFold(parent, p) {
							parent = ""
						}
						f.refresh(parent)
					}
				case 12:
					f.upload()
				case 13:
					f.download()
				case 14:
					f.mu.Lock()
					cancel := f.operationCancel
					f.mu.Unlock()
					if cancel != nil {
						cancel()
					}
				case 15:
					f.chooseSaveDir()
				case 16:
					f.refresh("")
				case 17:
					f.s.openCommandDialog()
				}
			}
			return 0
		case 0x10:
			f.cancel()
			ucall("DestroyWindow", h)
			return 0
		case 2:
			ucall("PostQuitMessage", 0)
			return 0
		}
		r, _, _ := ucall("DefWindowProcW", h, uintptr(m), w, l)
		return r
	})
	inst, _, _ := kernel32.NewProc("GetModuleHandleW").Call(0)
	cursor, _, _ := ucall("LoadCursorW", 0, 32512)
	name := utf16(fmt.Sprintf("MyDeskLANFiles_%x", cb))
	wc := wndClass{Size: uint32(unsafe.Sizeof(wndClass{})), Proc: cb, Instance: inst, Cursor: cursor, Background: f.darkBrush, ClassName: name}
	if r, _, e := ucall("RegisterClassExW", uintptr(unsafe.Pointer(&wc))); r == 0 {
		return e
	}
	defer ucall("UnregisterClassW", uintptr(unsafe.Pointer(name)), inst)
	h, _, e := ucall("CreateWindowExW", 0, uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(utf16("MyDesk LAN 文件管理 (F8)"))), 0x2CF0000, 180, 140, 900, 600, f.s.ownerWindow(), 0, inst, 0)
	if h == 0 {
		return e
	}
	darkTitleBar(h)
	f.mu.Lock()
	f.hwnd = h
	f.mu.Unlock()
	defer func() { f.mu.Lock(); f.hwnd = 0; f.mu.Unlock() }()
	font, releaseFont := boldUIFont(h)
	defer releaseFont()
	create := func(class, label string, style uintptr, id int) uintptr {
		control, _, _ := ucall("CreateWindowExW", 0, uintptr(unsafe.Pointer(utf16(class))), uintptr(unsafe.Pointer(utf16(label))), style, 0, 0, 100, 28, h, uintptr(id), inst, 0)
		ucall("SendMessageW", control, 0x30, font, 1)
		darkControl(control)
		return control
	}
	f.pathEdit = create("EDIT", "", 0x50810080, 1)
	f.listBox = create("SysListView32", "", 0x5081100D, 2)
	if f.listBox == 0 {
		ucall("DestroyWindow", h)
		return fmt.Errorf("无法创建文件列表")
	}
	f.initFileList()
	f.status = create("STATIC", "连接远端目录…", 0x50000000, 3)
	f.searchEdit = create("EDIT", "", 0x50810080, 4)
	ucall("SendMessageW", f.searchEdit, 0x1501, 0, uintptr(unsafe.Pointer(utf16("搜索当前目录…"))))
	f.saveDirLabel = create("STATIC", "保存到："+f.downloadDir, 0x50000000, 5)
	f.progressBar = create("msctls_progress32", "", 0x50000001, 6)
	if f.progressBar == 0 {
		ucall("DestroyWindow", h)
		return fmt.Errorf("无法创建传输进度条")
	}
	empty := utf16("")
	syscall.NewLazyDLL("uxtheme.dll").NewProc("SetWindowTheme").Call(f.progressBar, uintptr(unsafe.Pointer(empty)), uintptr(unsafe.Pointer(empty)))
	ucall("SendMessageW", f.progressBar, 0x406, 0, 1000)
	ucall("SendMessageW", f.progressBar, 0x2001, 0, darkBackground)
	ucall("SendMessageW", f.progressBar, 0x409, 0, 0x0080C050)
	for i, label := range []string{"刷新", "上级", "上传", "下载", "取消传输", "保存到…", "磁盘", "命令 F7"} {
		f.buttons[10+i] = create("BUTTON", label, 0x5001000B, 10+i)
	}
	syscall.NewLazyDLL("shell32.dll").NewProc("DragAcceptFiles").Call(h, 1)
	f.layout(h)
	presentOwnedWindow(h, f.pathEdit)
	stop := context.AfterFunc(f.ctx, func() { ucall("PostMessageW", h, 0x10, 0, 0) })
	defer stop()
	f.refreshRequest("", "file-default")
	var msg winMessage
	for {
		r, _, _ := ucall("GetMessageW", uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		if (msg.Message == 0x100 || msg.Message == 0x104) && msg.WParam == 0x77 && msg.LParam&(1<<30) == 0 {
			ucall("PostMessageW", h, 0x10, 0, 0)
			continue
		}
		if msg.Message == 0x100 && msg.LParam&(1<<30) == 0 {
			switch msg.WParam {
			case 0x76:
				f.s.openCommandDialog()
				continue
			case 0x79:
				f.s.cancelTransfers()
				continue
			case 0x7A:
				if owner := f.s.ownerWindow(); owner != 0 {
					ucall("PostMessageW", owner, 0x8006, 0, 0)
				}
				continue
			}
		}
		if msg.Message == 0x100 && (msg.HWnd == f.pathEdit || msg.HWnd == f.searchEdit) {
			if msg.WParam == 13 && msg.HWnd == f.pathEdit {
				f.refresh(controlText(f.pathEdit))
				continue
			}
			ctrl, _, _ := ucall("GetKeyState", 0x11)
			if msg.WParam == 'A' && int16(ctrl) < 0 {
				ucall("SendMessageW", msg.HWnd, 0xB1, 0, ^uintptr(0))
				continue
			}
		}
		if msg.Message == 0x100 && msg.HWnd == f.listBox && msg.WParam == 13 {
			f.activateSelectedFile()
			continue
		}
		if handled, _, _ := ucall("IsDialogMessageW", h, uintptr(unsafe.Pointer(&msg))); handled != 0 {
			continue
		}
		ucall("TranslateMessage", uintptr(unsafe.Pointer(&msg)))
		ucall("DispatchMessageW", uintptr(unsafe.Pointer(&msg)))
	}
	return nil
}
