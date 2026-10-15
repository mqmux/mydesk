//go:build windows && amd64

package lan

import (
	"testing"
	"unsafe"
)

func TestBoldUIFont(t *testing.T) {
	font, release := boldUIFont(0)
	defer release()
	var lf struct {
		Height, Width, Escapement, Orientation, Weight int32
		Style                                          [8]byte
		Face                                           [32]uint16
	}
	n, _, _ := gcall("GetObjectW", font, unsafe.Sizeof(lf), uintptr(unsafe.Pointer(&lf)))
	var original uiLogFont
	stock, _, _ := gcall("GetStockObject", 17)
	gcall("GetObjectW", stock, unsafe.Sizeof(original), uintptr(unsafe.Pointer(&original)))
	if n != unsafe.Sizeof(lf) || lf.Weight != 600 || lf.Height != original.Height || lf.Face != original.Face {
		t.Fatalf("unexpected font: size=%d height=%d weight=%d", n, lf.Height, lf.Weight)
	}
}
