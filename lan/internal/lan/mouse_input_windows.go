//go:build windows

package lan

import (
	"errors"
	"fmt"
	"unsafe"
)

const (
	inputMouseMove       = 0x0001
	inputMouseNoCoalesce = 0x2000
	inputMouseVirtual    = 0x4000
	inputMouseAbsolute   = 0x8000
)

func virtualDesktopBounds() desktop {
	x, _, _ := ucall("GetSystemMetrics", 76)
	y, _, _ := ucall("GetSystemMetrics", 77)
	w, _, _ := ucall("GetSystemMetrics", 78)
	h, _, _ := ucall("GetSystemMetrics", 79)
	return desktop{int(int32(x)), int(int32(y)), int(w), int(h)}
}

func absoluteMouseCoordinate(pixel, origin, extent int) int32 {
	// Target the pixel centre, avoiding the one-pixel rounding error of
	// pixel*65535/(extent-1). Negative virtual-desktop origins are supported.
	p := max(0, min(extent-1, pixel-origin))
	return int32(min(int64(65535), (int64(p)*65536+32768)/int64(extent)))
}

func mouseEventInput(e inputEvent, area, virtual desktop, dragging bool) (winInput, error) {
	if area.width < 1 || area.height < 1 || virtual.width < 1 || virtual.height < 1 {
		return winInput{}, errors.New("invalid mouse desktop bounds")
	}
	flags := uint32(inputMouseMove | inputMouseAbsolute | inputMouseVirtual)
	var data uint32
	switch e.Kind {
	case "move":
		if dragging {
			flags |= inputMouseNoCoalesce
		}
	case "down", "up":
		if e.Button < 0 || e.Button > 2 {
			return winInput{}, errors.New("invalid mouse button")
		}
		if e.Kind == "down" {
			flags |= []uint32{2, 8, 32}[e.Button]
		} else {
			flags |= []uint32{4, 16, 64}[e.Button]
		}
	case "wheel":
		flags |= 0x800
		data = uint32(int32(e.Delta))
	default:
		return winInput{}, errors.New("unknown input event")
	}
	x := max(0, min(area.width-1, e.X)) + area.left
	y := max(0, min(area.height-1, e.Y)) + area.top
	return winInput{Mouse: mouseInput{
		DX:    absoluteMouseCoordinate(x, virtual.left, virtual.width),
		DY:    absoluteMouseCoordinate(y, virtual.top, virtual.height),
		Flags: flags, Data: data,
	}}, nil
}

func (i *inputState) emit(in winInput) error {
	if i.inject != nil {
		return i.inject(in)
	}
	ret, _, err := ucall("SendInput", 1, uintptr(unsafe.Pointer(&in)), unsafe.Sizeof(in))
	if ret != 1 {
		return fmt.Errorf("SendInput accepted %d/1 events (INPUT size=%d): %v", ret, unsafe.Sizeof(in), err)
	}
	return nil
}
