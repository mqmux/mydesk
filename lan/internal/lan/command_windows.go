//go:build windows && amd64

package lan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

type commandResult struct {
	Output        string
	ExitCode, PID int
	Running       bool
}
type commandOutput struct {
	sync.Mutex
	text      []byte
	truncated bool
}

func (b *commandOutput) Write(p []byte) (int, error) {
	b.Lock()
	defer b.Unlock()
	n := len(p)
	room := max(0, (2<<20)-len(b.text))
	b.text = append(b.text, p[:min(room, n)]...)
	b.truncated = b.truncated || n > room
	return n, nil
}
func (b *commandOutput) snapshot() string {
	b.Lock()
	defer b.Unlock()
	s := strings.ToValidUTF8(string(b.text), "�")
	if b.truncated {
		s += "\r\n[输出超过 2MB，已截断]"
	}
	return s
}

var commandSlots = make(chan struct{}, 4)

func serveCommand(ctx context.Context, w *wire, m message) error {
	if strings.TrimSpace(m.Text) == "" || len(m.Text) > 32700 || strings.ContainsRune(m.Text, 0) || m.WaitSeconds < 0 || m.WaitSeconds > 3600 {
		return errors.New("命令或等待时间无效（0–3600 秒）")
	}
	path := m.Path
	if path == "" {
		path = programDirectory()
	}
	path, err := safeTransferPath(path)
	if err != nil {
		return err
	}
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return errors.New("工作路径不是目录")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case commandSlots <- struct{}{}:
	default:
		return errors.New("远端已有 4 个运行中的命令")
	}
	cmd := exec.Command("cmd.exe", "/d", "/s", "/c", "chcp 65001 >nul & "+m.Text)
	cmd.Dir = path
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	cmd.WaitDelay = 2 * time.Second
	output := &commandOutput{}
	cmd.Stdout = output
	cmd.Stderr = output
	if err := cmd.Start(); err != nil {
		<-commandSlots
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait(); <-commandSlots }()
	result := commandResult{PID: cmd.Process.Pid, Running: true, ExitCode: -1}
	send := func() error {
		result.Output = output.snapshot()
		data, err := json.Marshal(result)
		if err != nil {
			return err
		}
		return w.send(message{Type: "command-result"}, data)
	}
	if m.WaitSeconds == 0 {
		return send()
	}
	timer := time.NewTimer(time.Duration(m.WaitSeconds) * time.Second)
	defer timer.Stop()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case err := <-done:
			result.Running = false
			result.ExitCode = cmd.ProcessState.ExitCode()
			if err != nil && result.ExitCode < 0 {
				return err
			}
			return send()
		case <-timer.C:
			return send()
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
			if err := w.send(message{Type: "command-wait"}, nil); err != nil {
				return err
			}
		}
	}
}
func (s *controllerSession) runCommand(ctx context.Context, path, text string, wait int) (commandResult, error) {
	w, close, err := s.openAux(ctx)
	if err != nil {
		return commandResult{}, err
	}
	defer close()
	if err = w.send(message{Type: "command-run", Path: path, Text: text, WaitSeconds: wait}, nil); err != nil {
		return commandResult{}, err
	}
	for {
		m, data, err := w.read()
		if err != nil {
			return commandResult{}, err
		}
		switch m.Type {
		case "command-wait":
			continue
		case "error":
			return commandResult{}, errors.New(m.Text)
		case "command-result":
			var result commandResult
			err = json.Unmarshal(data, &result)
			return result, err
		default:
			return commandResult{}, fmt.Errorf("无效命令响应：%s", m.Type)
		}
	}
}
