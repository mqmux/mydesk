//go:build windows && amd64

package lan

import (
	"runtime"
	"syscall"
	"unsafe"
)

type displayOutput struct {
	area   desktop
	device string
	index  int
}

// Reuse one callback: syscall.NewCallback allocates slots that are not freed.
// Allocating it on every hot-plug poll would eventually exhaust those slots.
var enumerateMonitorCallback = syscall.NewCallback(func(h, dc, r, p uintptr) uintptr {
	info := struct {
		monitorInfo
		Device [32]uint16
	}{}
	info.Size = uint32(unsafe.Sizeof(info))
	if ok, _, _ := ucall("GetMonitorInfoW", h, uintptr(unsafe.Pointer(&info))); ok != 0 {
		out := (*[]displayOutput)(unsafe.Pointer(p))
		m := info.Monitor
		*out = append(*out, displayOutput{area: desktop{int(m.Left), int(m.Top), int(m.Right - m.Left), int(m.Bottom - m.Top)}, device: syscall.UTF16ToString(info.Device[:]), index: len(*out)})
	}
	return 1
})

func monitorOutputs() []displayOutput {
	var result []displayOutput
	ucall("EnumDisplayMonitors", 0, 0, enumerateMonitorCallback, uintptr(unsafe.Pointer(&result)))
	runtime.KeepAlive(&result)
	return result
}

// Match FFmpeg ddagrab's default D3D11 adapter/output indices. Win32 monitor
// enumeration can differ when virtual displays or multiple adapters exist.
func dxgiOutputs() []desktop {
	var result []desktop
	for _, output := range dxgiDisplayOutputs() {
		result = append(result, output.area)
	}
	return result
}

func dxgiDisplayOutputs() []displayOutput {
	iid := guid{0x770aae78, 0xf26f, 0x4dba, [8]byte{0xa8, 0x29, 0x25, 0x3c, 0x83, 0xd1, 0xb3, 0x87}}
	var factory uintptr
	hr, _, _ := syscall.NewLazyDLL("dxgi.dll").NewProc("CreateDXGIFactory1").Call(uintptr(unsafe.Pointer(&iid)), uintptr(unsafe.Pointer(&factory)))
	if hrError(hr) != nil || factory == 0 {
		return nil
	}
	defer comCall(factory, 2)
	var adapter uintptr
	hr, _, _ = comCall(factory, 12, 0, uintptr(unsafe.Pointer(&adapter)))
	if hrError(hr) != nil || adapter == 0 {
		return nil
	}
	defer comCall(adapter, 2)
	type outputDesc struct {
		Name        [32]uint16
		Coordinates rect
		Attached    int32
		Rotation    uint32
		Monitor     uintptr
	}
	var out []displayOutput
	for i := 0; i < 32; i++ {
		var output uintptr
		hr, _, _ = comCall(adapter, 7, uintptr(i), uintptr(unsafe.Pointer(&output)))
		if hrError(hr) != nil || output == 0 {
			break
		}
		var d outputDesc
		hr, _, _ = comCall(output, 7, uintptr(unsafe.Pointer(&d)))
		comCall(output, 2)
		if hrError(hr) != nil {
			return nil
		}
		r := d.Coordinates
		out = append(out, displayOutput{area: desktop{int(r.Left), int(r.Top), int(r.Right - r.Left), int(r.Bottom - r.Top)}, device: syscall.UTF16ToString(d.Name[:]), index: i})
	}
	return out
}
