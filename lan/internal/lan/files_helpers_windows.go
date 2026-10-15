//go:build windows && amd64

package lan

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

func programDirectory() string {
	if exe, err := os.Executable(); err == nil {
		return filepath.Dir(exe)
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return os.TempDir()
}
func filterFileEntries(listing directoryListing, filter string) []fileEntry {
	filter = strings.ToLower(strings.TrimSpace(filter))
	var entries []fileEntry
	if listing.Path != "" {
		parent := filepath.Dir(listing.Path)
		if strings.EqualFold(parent, listing.Path) {
			parent = ""
		}
		entries = append(entries, fileEntry{Name: "..", Path: parent, Directory: true})
	}
	for _, entry := range listing.Entries {
		if filter == "" || strings.Contains(strings.ToLower(entry.Name), filter) || strings.Contains(strings.ToLower(entry.Path), filter) {
			entries = append(entries, entry)
		}
	}
	return entries
}
func defaultDownloadTarget(dir, name string) (string, error) {
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name || strings.ContainsAny(name, "/\\:") {
		return "", errors.New("无效下载文件名")
	}
	path, err := safeTransferPath(filepath.Join(dir, name))
	if err != nil {
		return "", err
	}
	for i := 0; i < 10000; i++ {
		candidate := path
		if i > 0 {
			ext := filepath.Ext(name)
			candidate = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(name, ext), i, ext))
		}
		if _, err := os.Lstat(candidate); os.IsNotExist(err) {
			return candidate, nil
		} else if err != nil {
			return "", err
		}
	}
	return "", errors.New("同名文件过多，请选择其他目录")
}
func droppedFiles(drop uintptr) []string {
	proc := syscall.NewLazyDLL("shell32.dll").NewProc("DragQueryFileW")
	n, _, _ := proc.Call(drop, 0xffffffff, 0, 0)
	if n > 10000 {
		return nil
	}
	var paths []string
	for i := uintptr(0); i < n; i++ {
		size, _, _ := proc.Call(drop, i, 0, 0)
		if size == 0 || size > 32767 {
			continue
		}
		buf := make([]uint16, size+1)
		proc.Call(drop, i, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
		paths = append(paths, syscall.UTF16ToString(buf))
	}
	return paths
}

type browseInfo struct {
	Owner, Root     uintptr
	Display, Title  *uint16
	Flags           uint32
	Callback, Param uintptr
	Image           int32
}

func chooseDownloadFolder(owner uintptr) string {
	ole := syscall.NewLazyDLL("ole32.dll")
	hr, _, _ := ole.NewProc("OleInitialize").Call(0)
	if int32(hr) >= 0 {
		defer ole.NewProc("OleUninitialize").Call()
	}
	display := make([]uint16, 32768)
	info := browseInfo{Owner: owner, Display: &display[0], Title: utf16("选择默认下载目录"), Flags: 0x41}
	shell := syscall.NewLazyDLL("shell32.dll")
	pidl, _, _ := shell.NewProc("SHBrowseForFolderW").Call(uintptr(unsafe.Pointer(&info)))
	if pidl == 0 {
		return ""
	}
	defer ole.NewProc("CoTaskMemFree").Call(pidl)
	buf := make([]uint16, 32768)
	r, _, _ := shell.NewProc("SHGetPathFromIDListW").Call(pidl, uintptr(unsafe.Pointer(&buf[0])))
	if r == 0 {
		return ""
	}
	return syscall.UTF16ToString(buf)
}
