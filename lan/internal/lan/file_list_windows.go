//go:build windows && amd64

package lan

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// Activate comctl32 v6 on the pinned F8 UI thread. No manifest or DLL has to
// accompany the portable executable; keep the private manifest until release.
func activateListControls() (func(), error) {
	f, err := os.CreateTemp("", "mydesk-lan-controls-*.manifest")
	if err != nil {
		return nil, err
	}
	path := f.Name()
	const manifest = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<assembly xmlns="urn:schemas-microsoft-com:asm.v1" manifestVersion="1.0">
<assemblyIdentity type="win32" name="MyDesk.LAN.Controls" version="1.0.0.0" processorArchitecture="amd64"/>
<dependency><dependentAssembly><assemblyIdentity type="win32" name="Microsoft.Windows.Common-Controls" version="6.0.0.0" processorArchitecture="amd64" publicKeyToken="6595b64144ccf1df" language="*"/></dependentAssembly></dependency>
</assembly>`
	_, err = f.WriteString(manifest)
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(path)
		return nil, err
	}
	type actContext struct {
		Size, Flags                      uint32
		Source                           *uint16
		Architecture, Language           uint16
		Directory, Resource, Application *uint16
		Module                           uintptr
	}
	a := actContext{Size: uint32(unsafe.Sizeof(actContext{})), Source: utf16(path)}
	h, _, e := kernel32.NewProc("CreateActCtxW").Call(uintptr(unsafe.Pointer(&a)))
	if h == ^uintptr(0) {
		os.Remove(path)
		return nil, e
	}
	var cookie uintptr
	ok, _, e := kernel32.NewProc("ActivateActCtx").Call(h, uintptr(unsafe.Pointer(&cookie)))
	if ok == 0 {
		kernel32.NewProc("ReleaseActCtx").Call(h)
		os.Remove(path)
		return nil, e
	}
	return func() {
		kernel32.NewProc("DeactivateActCtx").Call(0, cookie)
		kernel32.NewProc("ReleaseActCtx").Call(h)
		os.Remove(path)
	}, nil
}

type notifyHeader struct {
	Window, ID uintptr
	Code       int32
}
type listItem struct {
	Mask                         uint32
	Item, SubItem                int32
	State, StateMask             uint32
	Text                         *uint16
	TextMax, Image               int32
	Param                        uintptr
	Indent, GroupID              int32
	Columns                      uint32
	ColumnIndices, ColumnFormats uintptr
	Group                        int32
}
type listDisplayInfo struct {
	Header notifyHeader
	Item   listItem
}
type listColumn struct {
	Mask                                                uint32
	Format, Width                                       int32
	Text                                                *uint16
	TextMax, SubItem, Image, Order, Min, Default, Ideal int32
}
type listNotification struct {
	Header                      notifyHeader
	Item, SubItem               int32
	NewState, OldState, Changed uint32
	Action                      point
	Param                       uintptr
}
type customDraw struct {
	Header notifyHeader
	Stage  uint32
	DC     uintptr
	Rect   rect
	Item   uintptr
	State  uint32
	Param  uintptr
}

func fileColumns(e fileEntry) [3]string {
	name := e.Name
	size := ""
	if e.Directory {
		name = strings.TrimRight(name, `\/`) + `\`
	} else {
		size = fmt.Sprintf("%d B", e.Size)
		for i, unit := range []string{"KB", "MB", "GB", "TB"} {
			divisor := float64(uint64(1) << (10 * (i + 1)))
			if float64(e.Size) < divisor {
				break
			}
			size = fmt.Sprintf("%.1f %s", float64(e.Size)/divisor, unit)
		}
	}
	modified := ""
	if e.Modified > 0 {
		modified = time.Unix(e.Modified, 0).Format("2006-01-02 15:04")
	}
	return [3]string{name, size, modified}
}

func sortFileEntries(entries []fileEntry, column int, descending bool) {
	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if (a.Name == "..") != (b.Name == "..") {
			return a.Name == ".."
		}
		if a.Directory != b.Directory {
			return a.Directory
		}
		comparison := 0
		switch column {
		case 1:
			if a.Size < b.Size {
				comparison = -1
			} else if a.Size > b.Size {
				comparison = 1
			}
		case 2:
			if a.Modified < b.Modified {
				comparison = -1
			} else if a.Modified > b.Modified {
				comparison = 1
			}
		}
		if comparison == 0 {
			comparison = strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
		}
		if comparison == 0 {
			comparison = strings.Compare(a.Path, b.Path)
		}
		if descending {
			return comparison > 0
		}
		return comparison < 0
	})
}

func (f *fileManager) initFileList() {
	ucall("SendMessageW", f.listBox, 0x2005, 1, 0) // CCM_SETUNICODEFORMAT
	const styles = 0x10000 | 0x20 | 0x4000         // DOUBLEBUFFER, FULLROWSELECT, LABELTIP
	ucall("SendMessageW", f.listBox, 0x1036, styles, styles)
	ucall("SendMessageW", f.listBox, 0x1001, 0, darkBackground)
	ucall("SendMessageW", f.listBox, 0x1024, 0, darkText)
	ucall("SendMessageW", f.listBox, 0x1026, 0, darkBackground)
	for i, name := range []string{"文件名", "大小", "修改时间"} {
		col := listColumn{Mask: 0xF, Width: 150, Text: utf16(name), SubItem: int32(i)}
		if i == 1 {
			col.Format = 1
			col.Width = 110
		}
		ucall("SendMessageW", f.listBox, 0x1061, uintptr(i), uintptr(unsafe.Pointer(&col)))
	}
	f.listHeader, _, _ = ucall("SendMessageW", f.listBox, 0x101F, 0, 0)
	// Header notifications go to its immediate parent (the list view), not
	// directly to the F8 window. Forward only header custom-draw notifications.
	var previous uintptr
	callback := syscall.NewCallback(func(h uintptr, m uint32, w, l uintptr) uintptr {
		if m == 0x4E && l != 0 {
			n := (*notifyHeader)(unsafe.Pointer(l))
			if n.Window == f.listHeader && n.Code == -12 {
				return f.listNotify(l)
			}
		}
		r, _, _ := ucall("CallWindowProcW", previous, h, uintptr(m), w, l)
		return r
	})
	previous, _, _ = ucall("SetWindowLongPtrW", f.listBox, ^uintptr(3), callback)
	var headerPrevious uintptr
	headerCallback := syscall.NewCallback(func(h uintptr, m uint32, w, l uintptr) uintptr {
		if m == 0x14 {
			var r rect
			ucall("GetClientRect", h, uintptr(unsafe.Pointer(&r)))
			ucall("FillRect", w, uintptr(unsafe.Pointer(&r)), f.darkBrush)
			return 1
		}
		r, _, _ := ucall("CallWindowProcW", headerPrevious, h, uintptr(m), w, l)
		return r
	})
	headerPrevious, _, _ = ucall("SetWindowLongPtrW", f.listHeader, ^uintptr(3), headerCallback)
	// Draw the header with the same colors/font as F7/F8, not an OS light theme.
	empty := utf16("")
	syscall.NewLazyDLL("uxtheme.dll").NewProc("SetWindowTheme").Call(f.listHeader, uintptr(unsafe.Pointer(empty)), uintptr(unsafe.Pointer(empty)))
}

func (f *fileManager) selectFileRow(index int) {
	item := listItem{StateMask: 3}
	ucall("SendMessageW", f.listBox, 0x102B, ^uintptr(0), uintptr(unsafe.Pointer(&item)))
	if index >= 0 && index < len(f.entries) {
		item.State = 3
		ucall("SendMessageW", f.listBox, 0x102B, uintptr(index), uintptr(unsafe.Pointer(&item)))
		ucall("SendMessageW", f.listBox, 0x1013, uintptr(index), 0)
	}
}

func (f *fileManager) activateSelectedFile() {
	if f.busy.Load() {
		return
	}
	if e, ok := f.selected(); ok {
		if e.Directory {
			f.refresh(e.Path)
		} else {
			f.download()
		}
	}
}

func (f *fileManager) listNotify(l uintptr) uintptr {
	if l == 0 {
		return 0
	}
	h := (*notifyHeader)(unsafe.Pointer(l))
	if h.Window == f.listHeader && h.Code == -12 {
		d := (*customDraw)(unsafe.Pointer(l))
		if d.Stage == 1 {
			return 0x30 // ITEMDRAW | POSTPAINT
		}
		if d.Stage == 2 {
			var r, last rect
			ucall("GetClientRect", f.listHeader, uintptr(unsafe.Pointer(&r)))
			if ok, _, _ := ucall("SendMessageW", f.listHeader, 0x1207, 2, uintptr(unsafe.Pointer(&last))); ok != 0 && last.Right < r.Right {
				r.Left = last.Right
				ucall("FillRect", d.DC, uintptr(unsafe.Pointer(&r)), f.darkBrush)
			}
			return 0
		}
		if d.Stage == 0x10001 {
			ucall("FillRect", d.DC, uintptr(unsafe.Pointer(&d.Rect)), f.darkBrush)
			index := int(d.Item)
			if index < 0 || index > 2 {
				return 4
			}
			text := []string{"文件名", "大小", "修改时间"}[index]
			if index == f.sortColumn {
				if f.sortDescending {
					text += " ▼"
				} else {
					text += " ▲"
				}
			}
			gcall("SetTextColor", d.DC, darkText)
			gcall("SetBkMode", d.DC, 2)
			gcall("SetBkColor", d.DC, darkBackground)
			r := d.Rect
			r.Left += 8
			r.Right -= 8
			ucall("DrawTextW", d.DC, uintptr(unsafe.Pointer(utf16(text))), ^uintptr(0), uintptr(unsafe.Pointer(&r)), 0x8024)
			return 4 // CDRF_SKIPDEFAULT
		}
	}
	if h.Window != f.listBox {
		return 0
	}
	switch h.Code {
	case -177: // LVN_GETDISPINFOW: read from the local cache, never network/filesystem.
		d := (*listDisplayInfo)(unsafe.Pointer(l))
		if d.Item.Mask&1 != 0 && d.Item.Text != nil && d.Item.TextMax > 0 && d.Item.Item >= 0 && int(d.Item.Item) < len(f.rows) && d.Item.SubItem >= 0 && d.Item.SubItem < 3 {
			buf := unsafe.Slice(d.Item.Text, int(d.Item.TextMax))
			text := syscall.StringToUTF16(f.rows[d.Item.Item][d.Item.SubItem])
			n := copy(buf[:len(buf)-1], text[:len(text)-1])
			buf[n] = 0
		}
	case -108: // LVN_COLUMNCLICK
		column := int((*listNotification)(unsafe.Pointer(l)).SubItem)
		if column >= 0 && column < 3 {
			if f.sortColumn == column {
				f.sortDescending = !f.sortDescending
			} else {
				f.sortColumn, f.sortDescending = column, false
			}
			f.filterEntries()
			ucall("InvalidateRect", f.listHeader, 0, 1)
		}
	case -3: // NM_DBLCLK. Ignore the blank area, even if a row remains selected.
		if (*listNotification)(unsafe.Pointer(l)).Item >= 0 {
			f.activateSelectedFile()
		}
	}
	return 0
}
