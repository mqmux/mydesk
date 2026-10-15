//go:build windows && amd64

package lan

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"strings"
	"sync"
	"time"
)

const directoryFeature = "directory-memory-v1"

type remoteDirectories struct {
	mu            sync.Mutex
	File, Command string
	Drives        bool
}

// One store per host process, shared across reconnects but never persisted.
// Empty values resolve to the program directory after every host restart.
func hostDirectoryMemory() *remoteDirectories {
	return &remoteDirectories{}
}

func validRemoteDirectory(path string) (string, error) {
	if len(path) > 32767 {
		return "", errors.New("目录路径过长")
	}
	path, err := safeTransferPath(strings.TrimSpace(path))
	if err != nil {
		return "", err
	}
	st, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !st.IsDir() {
		return "", errors.New("工作路径不是目录")
	}
	return path, nil
}

func (p *remoteDirectories) directory(command bool) string {
	if p == nil {
		return programDirectory()
	}
	p.mu.Lock()
	if !command && p.Drives {
		p.mu.Unlock()
		return ""
	}
	path := p.File
	if command {
		path = p.Command
	}
	p.mu.Unlock()
	if path, err := validRemoteDirectory(path); err == nil {
		return path
	}
	return programDirectory()
}

func (p *remoteDirectories) remember(path string, command bool) error {
	if p == nil {
		return nil
	}
	drives := !command && path == ""
	var err error
	if !drives {
		path, err = validRemoteDirectory(path)
		if err != nil {
			return err
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if command {
		p.Command = path
	} else {
		p.File, p.Drives = path, drives
	}
	return nil
}

func (s *hostSession) serveDirectoryRequest(ctx context.Context, w *wire, m message) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if m.Type == "command-directory" {
		if err := s.directories.remember(m.Path, true); err != nil {
			return err
		}
		return w.send(message{Type: "directory-saved"}, nil)
	}
	command := m.Type == "command-default"
	path := m.Path
	if m.Type == "file-default" || command {
		path = s.directories.directory(command)
	}
	listing, err := listRemoteDirectory(path)
	if err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if !command {
		if err := s.directories.remember(listing.Path, false); err != nil {
			log.Printf("[lan][路径] %v", err)
		}
	}
	b, err := json.Marshal(listing)
	if err != nil {
		return err
	}
	return w.send(message{Type: "listing"}, b)
}

func (s *controllerSession) saveCommandDirectory(ctx context.Context, path string) error {
	w, close, err := s.openAux(ctx)
	if err != nil {
		return err
	}
	defer close()
	if err := w.send(message{Type: "command-directory", Path: path}, nil); err != nil {
		return err
	}
	m, _, err := w.read()
	if err != nil {
		return err
	}
	if m.Type == "error" {
		return errors.New(m.Text)
	}
	if m.Type != "directory-saved" {
		return errors.New("目录保存响应无效")
	}
	return nil
}

func (s *controllerSession) queueCommandDirectory(path string) {
	if !hasFeature(s.welcome.Features, directoryFeature) || strings.TrimSpace(path) == "" {
		return
	}
	s.directoryMu.Lock()
	s.commandDirectory = strings.TrimSpace(path)
	s.directoryMu.Unlock()
}

func (s *controllerSession) flushCommandDirectory(parent context.Context) {
	if !hasFeature(s.welcome.Features, directoryFeature) {
		return
	}
	s.directorySaveMu.Lock()
	defer s.directorySaveMu.Unlock()
	s.directoryMu.Lock()
	path := s.commandDirectory
	dirty := path != "" && path != s.savedCommandDirectory
	s.directoryMu.Unlock()
	if !dirty || parent.Err() != nil {
		return
	}
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	if err := s.saveCommandDirectory(ctx, path); err != nil {
		if parent.Err() == nil {
			log.Printf("[lan][F7路径] %v", err)
		}
		return
	}
	s.directoryMu.Lock()
	s.savedCommandDirectory = path
	s.directoryMu.Unlock()
}
