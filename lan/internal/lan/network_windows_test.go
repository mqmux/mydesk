//go:build windows && amd64

package lan

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestFirewallScriptScopeAndEscaping(t *testing.T) {
	path := `C:\Users\test's folder\lan_client.exe`
	script := firewallScript("client", path, "setup")
	for _, required := range []string{"test''s folder", "-Program $program", "-RemoteAddress LocalSubnet", "-Profile Any", "@('TCP','UDP')", "Existing rule belongs to another program"} {
		if !strings.Contains(script, required) {
			t.Fatalf("missing firewall safeguard: %s", required)
		}
	}
	for _, forbidden := range []string{"Set-NetFirewallProfile", "DefaultInboundAction", "-Action Block", "-RemoteAddress Any"} {
		if strings.Contains(script, forbidden) {
			t.Fatalf("unsafe script: %s", forbidden)
		}
	}
	if strings.Contains(firewallScript("client", path, "remove"), "New-NetFirewallRule") {
		t.Fatal("remove recreated rule")
	}
	if firewallScript("client", path, "setup") == firewallScript("controller", path, "setup") {
		t.Fatal("roles share rules")
	}
}

func TestFirewallPowerShellSyntaxWithoutApplying(t *testing.T) {
	for _, action := range []string{"setup", "remove"} {
		script := firewallScript("client", `C:\Users\test's folder\lan_client.exe`, action)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		command := "$tokens=$null; $parseErrors=$null; [void][System.Management.Automation.Language.Parser]::ParseInput(" + psQuote(script) + ", [ref]$tokens, [ref]$parseErrors); if ($parseErrors.Count) { $parseErrors | Out-String | Write-Output; exit 1 }"
		output, err := helperCommand(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", command).CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("%s script parse failed (no firewall commands executed): %v %s", action, err, output)
		}
	}
}

func TestExplicitInterfaceDiscovery(t *testing.T) {
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	port := c.LocalAddr().(*net.UDPAddr).Port
	c.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go advertiseOn(ctx, port, "explicit-loopback-test", net.IPv4(127, 0, 0, 1))
	time.Sleep(30 * time.Millisecond)
	devices, err := discoverOn(ctx, port, net.IPv4(127, 0, 0, 1))
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 || devices[0].IP != "127.0.0.1" || devices[0].ID == "" {
		t.Fatalf("explicit discovery mismatch: %+v", devices)
	}
}
func TestInterfaceSelectionAndDiagnostics(t *testing.T) {
	if likelyPhysical("LetsTAP") || likelyPhysical("WireGuard Tunnel") || !likelyPhysical("以太网") {
		t.Fatal("fallback interface classification failed")
	}
	ip, err := resolveInterface("127.0.0.1")
	if err != nil || !ip.IsLoopback() {
		t.Fatal("loopback bind failed")
	}
	if _, err := resolveInterface("nonexistent-interface-for-test"); err == nil {
		t.Fatal("unknown interface accepted")
	}
	err = networkDiagnostic("TCP连接", "192.168.1.6:29860", syscall.Errno(10061))
	if !strings.Contains(err.Error(), "拒绝连接") || !errors.Is(err, syscall.Errno(10061)) {
		t.Fatal("refused diagnostic lost cause")
	}
	err = networkDiagnostic("TCP连接", "192.168.1.6:29860", errConnectTimeout{})
	if !strings.Contains(err.Error(), "VPN") || !strings.Contains(err.Error(), "防火墙") {
		t.Fatal("timeout missing actionable hints")
	}
}
func TestParallelAddressDialAndExplicitBinding(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	config, err := ephemeralTLS()
	if err != nil {
		t.Fatal(err)
	}
	listener, err := tls.Listen("tcp4", "127.0.0.1:0", config)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) { defer conn.Close(); _ = conn.(*tls.Conn).HandshakeContext(ctx); <-ctx.Done() }(conn)
		}
	}()
	conn, err := dialTLS(ctx, "127.0.0.1:1,"+listener.Addr().String(), net.IPv4(127, 0, 0, 1))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if !conn.LocalAddr().(*net.TCPAddr).IP.IsLoopback() || conn.RemoteAddr().String() != listener.Addr().String() {
		t.Fatal("wrong interface/target selected")
	}
}
