//go:build windows && amd64

package lan

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"unicode"
)

var errTargetSelectionCanceled = errors.New("已取消连接")

type keyMatchFinder func(context.Context, string) ([]keyMatchDevice, keyMatchReport, error)

func controllerInputLine(reader *bufio.Reader) (string, error) {
	line, err := reader.ReadString('\n')
	if err != nil && (err != io.EOF || len(line) == 0) {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func promptControllerKey(reader *bufio.Reader, out io.Writer) (string, error) {
	for {
		fmt.Fprint(out, "连接密钥（被控端 Pairing code）: ")
		code, err := controllerInputLine(reader)
		if err != nil {
			return "", fmt.Errorf("未输入连接密钥（也可用 -code 指定）: %w", err)
		}
		if len(code) >= 6 {
			return code, nil
		}
		fmt.Fprintln(out, "密钥至少需要 6 个字符，请重新输入。")
	}
}

func normalizeControllerAddresses(address string, defaultPort int) (string, error) {
	var addresses []string
	for _, value := range strings.Split(address, ",") {
		value = strings.TrimSpace(value)
		if value == "" {
			return "", errors.New("IP/地址不能为空")
		}
		normalized := addressWithPort(value, defaultPort)
		host, port, err := net.SplitHostPort(normalized)
		number, portErr := strconv.Atoi(port)
		if err != nil || host == "" || portErr != nil || number < 1 || number > 65535 || strings.ContainsAny(host, " \t\r\n") {
			return "", errors.New("无效的 IP/地址或端口")
		}
		addresses = append(addresses, normalized)
	}
	return strings.Join(addresses, ","), nil
}

// Console output from discovered peers must stay on one line and cannot
// inject terminal control sequences. This does not modify the actual address.
func matchDisplayName(name string) string {
	var out []rune
	for _, ch := range name {
		if !unicode.IsControl(ch) && !unicode.Is(unicode.Cf, ch) {
			out = append(out, ch)
		}
		if len(out) == 80 {
			break
		}
	}
	if len(out) == 0 {
		return "未命名设备"
	}
	return string(out)
}

func printKeyMatch(out io.Writer, index int, peer keyMatchDevice) {
	status := "空闲"
	if peer.busy {
		status = "占用（不可连接）"
	}
	fmt.Fprintf(out, "%d. %s | %s | %s\n", index+1, matchDisplayName(peer.name), strings.Join(peer.addresses, " / "), status)
}

func selectControllerTarget(ctx context.Context, reader *bufio.Reader, out io.Writer, address, code string, port int, find keyMatchFinder) (string, string, error) {
	if len(code) > 0 && len(code) < 6 {
		return "", "", errors.New("连接密钥至少需要 6 个字符")
	}
	if code == "" {
		var err error
		code, err = promptControllerKey(reader, out)
		if err != nil {
			return "", "", err
		}
	}
	if strings.TrimSpace(address) != "" {
		normalized, err := normalizeControllerAddresses(address, port)
		return normalized, code, err // Explicit target never triggers discovery/fallback.
	}
	for {
		if err := ctx.Err(); err != nil {
			return "", "", err
		}
		fmt.Fprintln(out, "[lan][匹配] 正在发现设备并验证密钥…")
		matches, report, err := find(ctx, code)
		if ctx.Err() != nil {
			return "", "", ctx.Err()
		}
		if err != nil {
			fmt.Fprintln(out, "[lan][匹配] 搜索未完成，请检查网卡、防火墙或 VPN；也可指定 IP。")
			matches = nil
		}
		if report.legacy > 0 {
			fmt.Fprintf(out, "[lan][匹配] %d 台旧版被控不支持自动密钥匹配，请更新被控或指定 IP 连接。\n", report.legacy)
		}
		if report.skipped > 0 {
			fmt.Fprintln(out, "[lan][匹配] 发现地址过多，部分地址未检查；可重新搜索或指定 IP。")
		}
		if len(matches) == 1 && !matches[0].busy {
			fmt.Fprintln(out, "[lan][匹配] 找到唯一匹配设备，直接连接：")
			printKeyMatch(out, 0, matches[0])
			return strings.Join(matches[0].addresses, ","), code, nil
		}
		if len(matches) == 0 {
			fmt.Fprintln(out, "[lan][匹配] 未找到匹配密钥的设备。请确认被控已启动、密钥正确，且广播/TCP 未被阻止。")
		} else {
			fmt.Fprintln(out, "[lan][匹配] 匹配设备（同一台的多个 IP 已合并）：")
			for index, peer := range matches {
				printKeyMatch(out, index, peer)
			}
		}
		for {
			fmt.Fprint(out, "设备序号或 IP[:端口]；r 重新搜索，k 更换密钥，q 退出: ")
			choice, err := controllerInputLine(reader)
			if err != nil {
				return "", "", fmt.Errorf("未选择设备（可用 -connect IP -code 密钥）: %w", err)
			}
			switch strings.ToLower(choice) {
			case "q":
				return "", "", errTargetSelectionCanceled
			case "r", "":
			case "k":
				code, err = promptControllerKey(reader, out)
				if err != nil {
					return "", "", err
				}
			default:
				if index, err := strconv.Atoi(choice); err == nil {
					if index < 1 || index > len(matches) {
						fmt.Fprintln(out, "设备序号无效，请重新选择。")
						continue
					}
					peer := matches[index-1]
					if peer.busy {
						fmt.Fprintln(out, "该设备已有主控连接，不会抢占；可选择其他设备或重新搜索。")
						continue
					}
					return strings.Join(peer.addresses, ","), code, nil
				}
				normalized, err := normalizeControllerAddresses(choice, port)
				host, _, _ := net.SplitHostPort(normalized)
				if err != nil || net.ParseIP(host) == nil {
					fmt.Fprintln(out, "请输入有效序号、IP[:端口] 或 r/k/q。")
					continue
				}
				return normalized, code, nil
			}
			break // r, blank or a changed key starts a fresh search.
		}
	}
}
