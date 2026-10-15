//go:build windows && amd64

package lan

import (
	"context"
	"fmt"
	"log"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

type commandDialog struct {
	fileManager
	commandEdit, waitEdit, output uintptr
	history                       []string
	historyIndex                  int
	loadingDirectory              bool
	directoryEdited               bool
}

// The classic EDIT scroll path blits existing glyphs. Repaint the read-only
// output once AFTER scrolling (not on video frames), including thumb dragging,
// wheel, keyboard and EM_LINESCROLL, so the exposed lines have opaque coverage.
func stabilizeOutputScroll(h uintptr) {
	var previous uintptr
	callback := syscall.NewCallback(func(hwnd uintptr, m uint32, w, l uintptr) uintptr {
		result, _, _ := ucall("CallWindowProcW", previous, hwnd, uintptr(m), w, l)
		if m == 0x114 || m == 0x115 || m == 0x20A || m == 0x20E || m == 0xB5 || m == 0xB6 || m == 0x100 {
			ucall("RedrawWindow", hwnd, 0, 0, 0x105) // INVALIDATE | ERASE | UPDATENOW
		}
		return result
	})
	previous, _, _ = ucall("SetWindowLongPtrW", h, ^uintptr(3), callback)
}

func (s *controllerSession) openCommandDialog() {
	s.filesMu.Lock()
	if s.command != nil && s.command.ctx.Err() == nil {
		d := s.command
		d.mu.Lock()
		h := d.hwnd
		d.mu.Unlock()
		s.filesMu.Unlock()
		d.cancel()
		if h != 0 {
			ucall("PostMessageW", h, 0x10, 0, 0)
		}
		return
	}
	ctx, cancel := context.WithCancel(s.ctx)
	d := &commandDialog{fileManager: fileManager{s: s, ctx: ctx, cancel: cancel, updates: make(chan func(), 128), buttons: map[int]uintptr{}}}
	s.command = d
	s.filesMu.Unlock()
	go func() {
		defer cancel()
		if err := d.run(); err != nil {
			log.Printf("[lan][F7] %v", err)
		}
		s.filesMu.Lock()
		if s.command == d {
			s.command = nil
		}
		s.filesMu.Unlock()
	}()
}
func (d *commandDialog) settings() {
	labels := []string{"同步键鼠（关闭为看板）", "同步剪贴板", "显示远端画面鼠标", "显示主控本地鼠标"}
	enabled := []bool{!d.s.inputDisabled.Load(), !d.s.clipboardDisabled.Load(), !d.s.cursorHidden.Load(), !d.s.localCursorHidden.Load()}
	for i, label := range labels {
		prefix := "[ ] "
		if enabled[i] {
			prefix = "[✓] "
		}
		d.setText(d.buttons[20+i], prefix+label)
	}
}
func (d *commandDialog) execute() {
	if d.busy.Load() {
		return
	}
	text := strings.TrimSpace(controlText(d.commandEdit))
	wait, err := strconv.Atoi(strings.TrimSpace(controlText(d.waitEdit)))
	if err != nil || wait < 0 || wait > 3600 || text == "" {
		d.setText(d.status, "请输入命令，等待时间为 0–3600 秒。")
		return
	}
	path := controlText(d.pathEdit)
	d.s.queueCommandDirectory(path)
	d.busy.Store(true)
	ucall("EnableWindow", d.buttons[10], 0)
	d.history = append(d.history, text)
	d.historyIndex = len(d.history)
	d.setText(d.output, "")
	d.setText(d.status, "正在远端执行…（关闭窗口只停止等待，不终止命令）")
	go func() {
		result, err := d.s.runCommand(d.ctx, path, text, wait)
		d.post(func() {
			d.busy.Store(false)
			ucall("EnableWindow", d.buttons[10], 1)
			if err != nil {
				d.setText(d.status, "执行失败："+err.Error())
				return
			}
			d.setText(d.output, strings.ReplaceAll(strings.ReplaceAll(result.Output, "\r\n", "\n"), "\n", "\r\n"))
			if result.Running {
				d.setText(d.status, fmt.Sprintf("命令仍在远端运行（PID %d）。等待结束不会终止命令。", result.PID))
			} else {
				d.setText(d.status, fmt.Sprintf("执行完成，退出码 %d。", result.ExitCode))
			}
		})
	}()
}
func (d *commandDialog) layout(h uintptr) {
	var r rect
	ucall("GetClientRect", h, uintptr(unsafe.Pointer(&r)))
	w, ht := int(r.Right), int(r.Bottom)
	if d.pathEdit == 0 {
		return
	}
	move := func(h uintptr, x, y, w, ht int) {
		ucall("MoveWindow", h, uintptr(x), uintptr(y), uintptr(max(20, w)), uintptr(max(20, ht)), 1)
	}
	move(d.pathEdit, 12, 12, w-160, 28)
	move(d.waitEdit, w-136, 12, 124, 28)
	move(d.commandEdit, 12, 48, w-124, 28)
	move(d.buttons[10], w-100, 48, 88, 28)
	for i := 0; i < 4; i++ {
		move(d.buttons[20+i], 12+(i%2)*(w-24)/2, 88+(i/2)*36, (w-32)/2, 28)
	}
	move(d.buttons[11], 12, 162, 130, 28)
	move(d.buttons[12], 152, 162, 150, 28)
	move(d.output, 12, 202, w-24, ht-256)
	move(d.status, 12, ht-44, w-24, 36)
}
func (d *commandDialog) run() error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	d.darkBrush, _, _ = gcall("CreateSolidBrush", darkBackground)
	defer gcall("DeleteObject", d.darkBrush)
	cb := syscall.NewCallback(func(h uintptr, m uint32, w, l uintptr) uintptr {
		switch m {
		case 0x14:
			var r rect
			ucall("GetClientRect", h, uintptr(unsafe.Pointer(&r)))
			ucall("FillRect", w, uintptr(unsafe.Pointer(&r)), d.darkBrush)
			return 1
		case 0x133, 0x134, 0x135, 0x136, 0x138:
			return darkControlColors(w, d.darkBrush)
		case 0x2B:
			paintDarkButton((*drawItem)(unsafe.Pointer(l)))
			return 1
		case 5:
			d.layout(h)
			return 0
		case 0x8003:
			for {
				select {
				case fn := <-d.updates:
					fn()
				default:
					return 0
				}
			}
		case 0x111:
			if w&65535 == 1 && d.pathEdit != 0 && !d.loadingDirectory {
				if (w>>16)&65535 == 0x300 {
					d.directoryEdited = true
					d.s.queueCommandDirectory(controlText(d.pathEdit))
				}
				if (w>>16)&65535 == 0x200 {
					go d.s.flushCommandDirectory(d.s.ctx)
				}
			}
			if (w>>16)&65535 != 0 {
				return 0
			}
			switch int(w & 65535) {
			case 10:
				d.execute()
			case 11:
				d.s.openFileManager()
			case 12:
				if owner := d.s.ownerWindow(); owner != 0 {
					ucall("PostMessageW", owner, 0x8006, 0, 0)
				}
			case 20:
				d.s.setInputEnabled(d.s.inputDisabled.Load())
			case 21:
				d.s.setClipboardEnabled(d.s.clipboardDisabled.Load())
			case 22:
				d.s.setRemoteCursorEnabled(d.s.cursorHidden.Load())
			case 23:
				d.s.setLocalCursorEnabled(d.s.localCursorHidden.Load())
			}
			d.settings()
			return 0
		case 0x10:
			d.s.queueCommandDirectory(controlText(d.pathEdit))
			go d.s.flushCommandDirectory(d.s.ctx)
			d.cancel()
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
	name := utf16(fmt.Sprintf("MyDeskLANCommand_%x", cb))
	wc := wndClass{Size: uint32(unsafe.Sizeof(wndClass{})), Proc: cb, Instance: inst, Cursor: cursor, Background: d.darkBrush, ClassName: name}
	if r, _, err := ucall("RegisterClassExW", uintptr(unsafe.Pointer(&wc))); r == 0 {
		return err
	}
	defer ucall("UnregisterClassW", uintptr(unsafe.Pointer(name)), inst)
	h, _, err := ucall("CreateWindowExW", 0, uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(utf16("MyDesk LAN 命令与设置 (F7) — 等待秒数，0=只启动"))), 0x2CF0000, 200, 160, 840, 600, d.s.ownerWindow(), 0, inst, 0)
	if h == 0 {
		return err
	}
	d.mu.Lock()
	d.hwnd = h
	d.mu.Unlock()
	defer func() { d.mu.Lock(); d.hwnd = 0; d.mu.Unlock() }()
	darkTitleBar(h)
	font, releaseFont := boldUIFont(h)
	defer releaseFont()
	create := func(class, label string, style uintptr, id int) uintptr {
		c, _, _ := ucall("CreateWindowExW", 0, uintptr(unsafe.Pointer(utf16(class))), uintptr(unsafe.Pointer(utf16(label))), style, 0, 0, 100, 28, h, uintptr(id), inst, 0)
		ucall("SendMessageW", c, 0x30, font, 1)
		darkControl(c)
		return c
	}
	d.pathEdit = create("EDIT", "", 0x50810080, 1)
	d.waitEdit = create("EDIT", "10", 0x50812080, 2)
	d.commandEdit = create("EDIT", "", 0x50810080, 3)
	d.output = create("EDIT", "", 0x50B108C4, 4)
	stabilizeOutputScroll(d.output)
	d.status = create("STATIC", "仅在你点击执行后运行命令；取消键鼠同步即看板模式。", 0x50000000, 5)
	for id, label := range map[int]string{10: "执行", 11: "文件管理 F8", 12: "切换全屏 F11", 20: "", 21: "", 22: "", 23: ""} {
		d.buttons[id] = create("BUTTON", label, 0x5001000B, id)
	}
	d.settings()
	d.layout(h)
	presentOwnedWindow(h, d.commandEdit)
	stop := context.AfterFunc(d.ctx, func() { ucall("PostMessageW", h, 0x10, 0, 0) })
	defer stop()
	go func() {
		kind := "file-default"
		if hasFeature(d.s.welcome.Features, directoryFeature) {
			kind = "command-default"
		}
		listing, err := d.s.remoteListRequest(d.ctx, "", kind)
		if err == nil {
			d.post(func() {
				if !d.directoryEdited && controlText(d.pathEdit) == "" {
					d.loadingDirectory = true
					d.setText(d.pathEdit, listing.Path)
					d.loadingDirectory = false
				}
			})
		}
	}()
	var msg winMessage
	for {
		r, _, _ := ucall("GetMessageW", uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		if msg.Message == 0x100 {
			if msg.LParam&(1<<30) == 0 {
				switch msg.WParam {
				case 0x76:
					ucall("PostMessageW", h, 0x10, 0, 0)
					continue
				case 0x77:
					d.s.openFileManager()
					continue
				case 0x79:
					d.s.cancelTransfers()
					continue
				case 0x7A:
					if owner := d.s.ownerWindow(); owner != 0 {
						ucall("PostMessageW", owner, 0x8006, 0, 0)
					}
					continue
				}
			}
			if msg.HWnd == d.commandEdit {
				if msg.WParam == 13 {
					d.execute()
					continue
				}
				if msg.WParam == 0x26 || msg.WParam == 0x28 {
					if msg.WParam == 0x26 {
						d.historyIndex = max(0, d.historyIndex-1)
					} else {
						d.historyIndex = min(len(d.history), d.historyIndex+1)
					}
					text := ""
					if d.historyIndex < len(d.history) {
						text = d.history[d.historyIndex]
					}
					d.setText(d.commandEdit, text)
					continue
				}
			}
			if msg.HWnd == d.pathEdit || msg.HWnd == d.waitEdit || msg.HWnd == d.commandEdit || msg.HWnd == d.output {
				ctrl, _, _ := ucall("GetKeyState", 0x11)
				if msg.WParam == 'A' && int16(ctrl) < 0 {
					ucall("SendMessageW", msg.HWnd, 0xB1, 0, ^uintptr(0))
					continue
				}
			}
		}
		if handled, _, _ := ucall("IsDialogMessageW", h, uintptr(unsafe.Pointer(&msg))); handled != 0 {
			continue
		}
		ucall("TranslateMessage", uintptr(unsafe.Pointer(&msg)))
		ucall("DispatchMessageW", uintptr(unsafe.Pointer(&msg)))
	}
	return nil
}
