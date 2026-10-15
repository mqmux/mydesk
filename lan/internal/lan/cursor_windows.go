//go:build windows && amd64

package lan

import (
	"crypto/sha256"
	"fmt"
	"runtime"
	"time"
	"unsafe"
)

var cursorKinds = []struct {
	id   uintptr
	kind string
}{{32512, "arrow"}, {32513, "text"}, {32514, "wait"}, {32650, "app-start"}, {32649, "hand"}, {32515, "cross"}, {32644, "size-we"}, {32645, "size-ns"}, {32642, "size-nwse"}, {32643, "size-nesw"}, {32646, "size-all"}, {32648, "forbidden"}, {32651, "help"}, {32516, "up-arrow"}}

func cursorID(kind string) uintptr {
	for _, c := range cursorKinds {
		if c.kind == kind {
			return c.id
		}
	}
	return 32512
}

type cursorIconInfo struct {
	Icon        uint32
	HotX, HotY  uint32
	Mask, Color uintptr
}

type remoteCursorInfo struct {
	Size, Flags uint32
	Cursor      uintptr
	Position    point
}
type cursorBitmap struct {
	Type, Width, Height, Stride int32
	Planes, Bits                uint16
	Data                        uintptr
}

func cursorFingerprint(handle uintptr) string {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	var ii cursorIconInfo
	if ok, _, _ := ucall("GetIconInfo", handle, uintptr(unsafe.Pointer(&ii))); ok == 0 {
		return ""
	}
	if ii.Mask != 0 {
		defer gcall("DeleteObject", ii.Mask)
	}
	if ii.Color != 0 {
		defer gcall("DeleteObject", ii.Color)
	}
	hash := sha256.New()
	fmt.Fprintf(hash, "%d:%d:", ii.HotX, ii.HotY)
	dc, _, _ := ucall("GetDC", 0)
	if dc == 0 {
		return ""
	}
	defer ucall("ReleaseDC", 0, dc)
	for _, bitmap := range []uintptr{ii.Mask, ii.Color} {
		if bitmap == 0 {
			hash.Write([]byte{0})
			continue
		}
		var bm cursorBitmap
		if ok, _, _ := gcall("GetObjectW", bitmap, unsafe.Sizeof(bm), uintptr(unsafe.Pointer(&bm))); ok == 0 || bm.Width < 1 || bm.Width > 256 || bm.Height < 1 || bm.Height > 512 {
			return ""
		}
		bi := bitmapSpec(int(bm.Width), int(bm.Height))
		pixels := make([]byte, int(bm.Width)*int(bm.Height)*4)
		if rows, _, _ := gcall("GetDIBits", dc, bitmap, 0, uintptr(bm.Height), uintptr(unsafe.Pointer(&pixels[0])), uintptr(unsafe.Pointer(&bi)), 0); rows != uintptr(bm.Height) {
			return ""
		}
		fmt.Fprintf(hash, "%d:%d:", bm.Width, bm.Height)
		hash.Write(pixels)
	}
	return fmt.Sprintf("%x", hash.Sum(nil))
}

type cursorClassifier struct {
	handles      map[uintptr]string
	fingerprints map[string]string
}

func (c *cursorClassifier) classify(handle uintptr) string {
	if handle == 0 {
		return "arrow"
	}
	if c.handles == nil {
		c.handles = map[uintptr]string{}
		c.fingerprints = map[string]string{}
		for _, entry := range cursorKinds {
			h, _, _ := ucall("LoadCursorW", 0, entry.id)
			if h != 0 {
				c.handles[h] = entry.kind
				if fp := cursorFingerprint(h); fp != "" {
					c.fingerprints[fp] = entry.kind
				}
			}
		}
	}
	if kind, ok := c.handles[handle]; ok {
		return kind
	}
	kind := "arrow"
	if fp := cursorFingerprint(handle); fp != "" {
		if known, ok := c.fingerprints[fp]; ok {
			kind = known
		}
	}
	if len(c.handles) < 128 {
		c.handles[handle] = kind
	}
	return kind
}
func (s *hostSession) cursorLoop() {
	ticker := time.NewTicker(33 * time.Millisecond)
	defer ticker.Stop()
	classifier := cursorClassifier{}
	lastKind := ""
	lastVisible := false
	lastSent := time.Time{}
	for {
		select {
		case <-s.ctx.Done():
			return
		case now := <-ticker.C:
			ci := remoteCursorInfo{Size: uint32(unsafe.Sizeof(remoteCursorInfo{}))}
			if ok, _, _ := ucall("GetCursorInfo", uintptr(unsafe.Pointer(&ci))); ok == 0 {
				continue
			}
			visible := ci.Flags&1 != 0
			kind := classifier.classify(ci.Cursor)
			cfg, _, _ := s.screenConfig()
			a := cfg.area
			if ci.Position.X < int32(a.left) || ci.Position.X >= int32(a.left+a.width) || ci.Position.Y < int32(a.top) || ci.Position.Y >= int32(a.top+a.height) {
				kind = "arrow"
				visible = true
			}
			if kind == lastKind && visible == lastVisible && now.Sub(lastSent) < 2*time.Second {
				continue
			}
			if err := s.ctrl.send(message{Type: "cursor-state", CursorKind: kind, CursorVisible: &visible}, nil); err != nil {
				return
			}
			lastKind = kind
			lastVisible = visible
			lastSent = now
		}
	}
}
func (v *viewer) setCursorState(kind string, visible bool) {
	v.remoteCursorID.Store(uint32(cursorID(kind)))
	v.remoteCursorVisible.Store(visible)
	v.remoteCursorKnown.Store(true)
	if h := func() uintptr { v.mu.Lock(); defer v.mu.Unlock(); return v.hwnd }(); h != 0 {
		ucall("PostMessageW", h, 0x8008, 0, 0)
	}
}
func (v *viewer) cursorAt(p point) uintptr {
	var r rect
	ucall("GetClientRect", v.hwnd, uintptr(unsafe.Pointer(&r)))
	x, y, w, h := fitRect(int(r.Right), int(r.Bottom), v.width, v.height)
	if int(p.X) < x || int(p.X) >= x+w || int(p.Y) < y || int(p.Y) >= y+h || (v.hudVisible && p.Y < 40) {
		cursor, _, _ := ucall("LoadCursorW", 0, 32512)
		return cursor
	}
	// F7 is an explicit local-display policy, not a mirror of the host's
	// hardware cursor visibility. A host without a mouse can report hidden
	// (or touch/pen suppressed) even while remote input/shape updates work.
	// Preserve its visibility metadata, but never hide a checked local pointer.
	if v.hideLocalCursor.Load() {
		return 0
	}
	id := uintptr(v.remoteCursorID.Load())
	if id == 0 {
		id = 32512
	}
	cursor, _, _ := ucall("LoadCursorW", 0, id)
	return cursor
}

// Called only on the viewer thread. A capture can suppress WM_SETCURSOR, so
// refresh shapes/options during drags as well. Do not change the cursor over
// an unrelated foreground window, a dialog or an obscuring window.
func (v *viewer) refreshLocalCursor() bool {
	if v.hwnd == 0 {
		return false
	}
	var p point
	if ok, _, _ := ucall("GetCursorPos", uintptr(unsafe.Pointer(&p))); ok == 0 {
		return false
	}
	captured, _, _ := ucall("GetCapture")
	if captured != v.hwnd {
		hit, _, _ := ucall("WindowFromPoint", uintptr(uint32(p.X))|uintptr(uint32(p.Y))<<32)
		root, _, _ := ucall("GetAncestor", hit, 2)
		// Our own child controls have their own cursor policy (HUD/buttons).
		if root != v.hwnd || hit != v.hwnd {
			return false
		}
	}
	ucall("ScreenToClient", v.hwnd, uintptr(unsafe.Pointer(&p)))
	var r rect
	ucall("GetClientRect", v.hwnd, uintptr(unsafe.Pointer(&r)))
	if captured != v.hwnd && (p.X < 0 || p.Y < 0 || p.X >= r.Right || p.Y >= r.Bottom) {
		return false
	}
	ucall("SetCursor", v.cursorAt(p))
	return true
}
