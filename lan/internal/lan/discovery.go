package lan

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"net"
	"sort"
	"time"
)

type device struct {
	ID        string   `json:"id,omitempty"`
	Addresses []string `json:"addresses,omitempty"`
	Version   int      `json:"version"`
	Name      string   `json:"name"`
	Port      int      `json:"port"`
	Features  []string `json:"features,omitempty"`
	IP        string   `json:"-"`
}

const discoverMagic = "MYDESK-LAN-DISCOVER-1"

func advertise(ctx context.Context, port int, name string) error {
	return advertiseOn(ctx, port, name, nil)
}
func advertiseOn(ctx context.Context, port int, name string, local net.IP) error {
	return advertiseDeviceOn(ctx, port, name, local, randomCode(), nil)
}

func advertiseDeviceOn(ctx context.Context, port int, name string, local net.IP, id string, features []string) error {
	// Broadcast receive needs an all-address socket; the advertised TCP
	// addresses and source-subnet filter enforce explicit interface selection.
	c, err := net.ListenUDP("udp4", &net.UDPAddr{Port: port})
	if err != nil {
		return err
	}
	go func() { <-ctx.Done(); c.Close() }()
	b := make([]byte, 512)
	var addresses []string
	var selectedNetwork *net.IPNet
	if local != nil {
		addresses = append(addresses, local.String())
	}
	for _, nic := range localNICs() {
		if local == nil {
			addresses = append(addresses, nic.ip.String())
		} else if local.Equal(nic.ip) {
			selectedNetwork = nic.network
		}
	}
	for {
		n, addr, err := c.ReadFromUDP(b)
		if err != nil {
			return err
		}
		if string(b[:n]) != discoverMagic {
			continue
		}
		if local != nil {
			if local.IsLoopback() && !addr.IP.IsLoopback() {
				continue
			}
			if !local.IsLoopback() && (selectedNetwork == nil || !selectedNetwork.Contains(addr.IP)) {
				continue
			}
		}
		ips := append([]string(nil), addresses...)
		if local == nil && addr.IP.IsLoopback() {
			ips = append(ips, "127.0.0.1")
		}
		response, _ := json.Marshal(device{ID: id, Addresses: ips, Version: protocolVersion, Name: name, Port: port, Features: features})
		_, _ = c.WriteToUDP(response, addr)
	}
}
func discover(ctx context.Context, port int) ([]device, error) {
	return discoverOn(ctx, port, nil)
}
func discoverOn(ctx context.Context, port int, selected net.IP) ([]device, error) {
	nics := localNICs()
	if selected == nil || selected.IsLoopback() {
		ip := net.IPv4(127, 0, 0, 1).To4()
		if selected != nil {
			ip = selected.To4()
		}
		nics = append(nics, localNIC{name: "loopback", ip: ip, network: &net.IPNet{IP: net.IPv4(127, 0, 0, 0), Mask: net.CIDRMask(8, 32)}})
	}
	type found struct {
		devices []device
		err     error
	}
	results := make(chan found, len(nics))
	count := 0
	for _, nic := range nics {
		if selected != nil && !selected.Equal(nic.ip) {
			continue
		}
		count++
		go func(nic localNIC) {
			c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: nic.ip})
			if err != nil {
				results <- found{err: err}
				return
			}
			defer c.Close()
			stop := context.AfterFunc(ctx, func() { c.Close() })
			defer stop()
			_ = c.SetReadDeadline(time.Now().Add(1500 * time.Millisecond))
			target := net.IPv4(127, 0, 0, 1)
			if !nic.ip.IsLoopback() {
				target = make(net.IP, 4)
				for i := 0; i < 4; i++ {
					target[i] = nic.ip[i] | ^nic.network.Mask[i]
				}
			}
			if _, err = c.WriteToUDP([]byte(discoverMagic), &net.UDPAddr{IP: target, Port: port}); err != nil {
				results <- found{err: err}
				return
			}
			var devices []device
			b := make([]byte, 2048)
			for {
				n, addr, err := c.ReadFromUDP(b)
				if err != nil {
					break
				}
				var d device
				if json.Unmarshal(b[:n], &d) != nil || d.Port < 1 || d.Port > 65535 {
					continue
				}
				if d.Version != protocolVersion {
					log.Printf("[lan][发现] %s 使用旧协议 v%d；请更新两端程序", addr.IP, d.Version)
					continue
				}
				ips := append([]string{addr.IP.String()}, d.Addresses...)
				if len(ips) > 17 {
					ips = ips[:17]
				}
				for _, value := range ips {
					ip := net.ParseIP(value)
					if ip == nil || ip.To4() == nil || (!ip.IsGlobalUnicast() && !ip.IsLoopback()) || ip.IsLinkLocalUnicast() || (ip.IsLoopback() && !addr.IP.IsLoopback()) {
						continue
					}
					d.IP = ip.String()
					devices = append(devices, d)
				}
			}
			results <- found{devices: devices}
		}(nic)
	}
	seen := map[string]device{}
	for i := 0; i < count; i++ {
		r := <-results
		if r.err != nil {
			log.Printf("[lan][发现] 某网卡发现失败：%v", r.err)
		}
		for _, d := range r.devices {
			seen[fmt.Sprintf("%s:%d", d.IP, d.Port)] = d
		}
	}
	out := make([]device, 0, len(seen))
	for _, d := range seen {
		out = append(out, d)
	}
	priority := func(ip string) int {
		if net.ParseIP(ip).IsLoopback() {
			return 2
		}
		for _, n := range nics {
			if n.physical && n.network.Contains(net.ParseIP(ip)) {
				return 0
			}
		}
		return 1
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := priority(out[i].IP), priority(out[j].IP)
		if a != b {
			return a < b
		}
		return out[i].IP < out[j].IP
	})
	if len(out) == 0 {
		log.Print("[lan][发现] 没发现设备：广播可能被防火墙/VPN/AP 隔离；可用 -connect 被控IP 直接尝试 TCP")
	}
	return out, nil
}
func ephemeralTLS() (*tls.Config, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	t := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "mydesk-lan"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, t, t, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}, nil
}
