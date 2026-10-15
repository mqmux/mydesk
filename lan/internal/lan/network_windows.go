//go:build windows && amd64

package lan

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

type localNIC struct {
	name     string
	index    int
	ip       net.IP
	network  *net.IPNet
	physical bool
}

var physicalOnce sync.Once
var physicalIndices map[int]bool

func getPhysicalIndices() map[int]bool {
	physicalOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		cmd := helperCommand(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "$ErrorActionPreference='Stop'; ConvertTo-Json -Compress -InputObject @((Get-NetAdapter -Physical -IncludeHidden).ifIndex)")
		data, err := cmd.Output()
		var indices []int
		if err == nil && json.Unmarshal(data, &indices) == nil {
			physicalIndices = map[int]bool{}
			for _, i := range indices {
				physicalIndices[i] = true
			}
		}
	})
	return physicalIndices
}

func likelyPhysical(name string) bool {
	name = strings.ToLower(name)
	for _, s := range []string{"vpn", "tap", "tun", "virtual", "vethernet", "loopback", "wireguard", "tailscale", "zerotier", "wintun", "本地连接*"} {
		if strings.Contains(name, s) {
			return false
		}
	}
	return true
}

func localNICs() []localNIC {
	physical := getPhysicalIndices()
	ifaces, _ := net.Interfaces()
	var out []localNIC
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			ip, network, err := net.ParseCIDR(a.String())
			if err != nil || ip.To4() == nil || ip.IsLinkLocalUnicast() || !ip.IsGlobalUnicast() {
				continue
			}
			p := likelyPhysical(iface.Name)
			if physical != nil {
				p = physical[iface.Index]
			}
			out = append(out, localNIC{iface.Name, iface.Index, ip.To4(), network, p})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].physical != out[j].physical {
			return out[i].physical
		}
		if out[i].ip.IsPrivate() != out[j].ip.IsPrivate() {
			return out[i].ip.IsPrivate()
		}
		return out[i].index < out[j].index
	})
	return out
}

func printInterfaces() {
	fmt.Println("网卡 / IPv4（物理网卡优先；-interface 可填网卡名称或本机 IP）")
	for _, n := range localNICs() {
		kind := "VPN/虚拟"
		if n.physical {
			kind = "物理"
		}
		fmt.Printf("  %s  %s  [%s]\n", n.name, n.ip, kind)
	}
}

func resolveInterface(value string) (net.IP, error) {
	if value == "" {
		return nil, nil
	}
	if ip := net.ParseIP(value); ip != nil && ip.IsLoopback() && ip.To4() != nil {
		return ip.To4(), nil
	}
	for _, n := range localNICs() {
		if strings.EqualFold(value, n.name) || value == n.ip.String() {
			return n.ip, nil
		}
	}
	return nil, fmt.Errorf("找不到已连接的 IPv4 网卡 %q；请用 -list-interfaces 查看", value)
}

func candidateLocalIPs(remote, explicit net.IP) []net.IP {
	if explicit != nil {
		return []net.IP{explicit}
	}
	if remote == nil || remote.IsLoopback() {
		return []net.IP{nil}
	}
	var out []net.IP
	for _, n := range localNICs() {
		if n.network.Contains(remote) {
			out = append(out, n.ip)
		}
	}
	return append(out, nil) // Keep normal OS routing as the final fallback.
}

func networkDiagnostic(stage, addr string, err error) error {
	if err == nil {
		return nil
	}
	if stage == "TCP/TLS连接" {
		return fmt.Errorf("[%s] %s：%w", stage, addr, err)
	}
	hint := "检查被控程序日志、IP/端口和网络可达性"
	var ne net.Error
	switch {
	case errors.Is(err, syscall.Errno(10061)):
		hint = "目标拒绝连接：检查被控端是否运行 lan_client.exe、是否监听正确端口"
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()):
		hint = "连接超时：检查被控端程序防火墙规则、VPN 局域网访问/分流、网卡选择和路由器客户端隔离"
	case stage == "配对":
		hint = "检查配对码；两端必须使用同一版 LAN 程序"
	case stage == "TLS握手":
		hint = "TCP 已连通，但 TLS 握手失败：确认目标是 LAN 被控端而不是其他服务"
	case stage == "视频连接":
		hint = "配对后的视频通道失败：检查被控端是否退出及程序防火墙规则"
	}
	return fmt.Errorf("[%s] %s：%s；原始错误：%w", stage, addr, hint, err)
}

func dialTLS(ctx context.Context, addr string, selected ...net.IP) (*tls.Conn, error) {
	return dialTLSLogged(ctx, addr, true, selected...)
}
func dialTLSLogged(ctx context.Context, addr string, verbose bool, selected ...net.IP) (*tls.Conn, error) {
	var explicit net.IP
	if len(selected) > 0 {
		explicit = selected[0]
	}
	type attempt struct {
		address string
		local   net.IP
		delay   time.Duration
	}
	var attempts []attempt
	for addressIndex, address := range strings.Split(addr, ",") {
		address = strings.TrimSpace(address)
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return nil, networkDiagnostic("TCP连接", address, err)
		}
		for i, ip := range candidateLocalIPs(net.ParseIP(host), explicit) {
			attempts = append(attempts, attempt{address, ip, time.Duration(addressIndex)*150*time.Millisecond + time.Duration(i)*200*time.Millisecond})
		}
	}
	raceCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	type result struct {
		conn *tls.Conn
		err  error
	}
	results := make(chan result)
	for _, a := range attempts {
		go func(a attempt) {
			if a.delay > 0 {
				timer := time.NewTimer(a.delay)
				defer timer.Stop()
				select {
				case <-timer.C:
				case <-raceCtx.Done():
					return
				}
			}
			d := net.Dialer{}
			if a.local != nil {
				d.LocalAddr = &net.TCPAddr{IP: a.local}
			}
			raw, err := d.DialContext(raceCtx, "tcp4", a.address)
			var conn *tls.Conn
			if err != nil {
				err = networkDiagnostic("TCP连接", a.address, err)
			} else {
				conn = tls.Client(&measuredConn{Conn: raw}, &tls.Config{MinVersion: tls.VersionTLS13, InsecureSkipVerify: true})
				if e := conn.HandshakeContext(raceCtx); e != nil {
					conn.Close()
					conn = nil
					err = networkDiagnostic("TLS握手", a.address, e)
				}
			}
			select {
			case results <- result{conn, err}:
			case <-raceCtx.Done():
				if conn != nil {
					conn.Close()
				}
			}
		}(a)
	}
	var failures []error
	for range attempts {
		select {
		case r := <-results:
			if r.err == nil {
				if verbose {
					log.Printf("[lan][连接] TCP/TLS 成功 %s -> %s", r.conn.LocalAddr(), r.conn.RemoteAddr())
				}
				return r.conn, nil
			}
			failures = append(failures, r.err)
		case <-raceCtx.Done():
			if len(failures) > 0 {
				return nil, errors.Join(append(failures, networkDiagnostic("TCP连接", addr, raceCtx.Err()))...)
			}
			return nil, networkDiagnostic("TCP连接", addr, &net.OpError{Op: "dial", Net: "tcp", Err: errConnectTimeout{}})
		}
	}
	return nil, errors.Join(failures...)
}

type errConnectTimeout struct{}

func (errConnectTimeout) Error() string   { return "5 秒内没有建立 TCP/TLS 连接" }
func (errConnectTimeout) Timeout() bool   { return true }
func (errConnectTimeout) Temporary() bool { return true }
