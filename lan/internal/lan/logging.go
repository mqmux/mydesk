package lan

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"time"
)

func setupLogging(role string) func() {
	exe, err := os.Executable()
	if err != nil {
		return func() {}
	}
	dir := filepath.Join(filepath.Dir(exe), "logs")
	if os.MkdirAll(dir, 0755) != nil {
		return func() {}
	}
	f, err := os.OpenFile(filepath.Join(dir, role+"-"+time.Now().Format("20060102-150405")+".log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return func() {}
	}
	log.SetOutput(io.MultiWriter(os.Stderr, f))
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
	return func() { f.Close() }
}
