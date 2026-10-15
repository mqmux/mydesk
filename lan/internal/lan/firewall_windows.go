//go:build windows && amd64

package lan

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

func psQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

func showConnectionError(err error) {
	text := "连接未完成：\n" + err.Error() + "\n\n需要配置防火墙时，两端各自运行对应 EXE 的 -firewall setup（需要确认和管理员权限）。\n网卡选择：-list-interfaces / -interface 网卡名称或本机IP。\nVPN 强制隔离不能靠改成 TCP 绕过。\n详细日志在 EXE 旁的 logs 文件夹。"
	prompt, _ := syscall.UTF16PtrFromString(text)
	caption, _ := syscall.UTF16PtrFromString("MyDesk LAN 连接诊断")
	ucall("MessageBoxW", 0, uintptr(unsafe.Pointer(prompt)), uintptr(unsafe.Pointer(caption)), 0x10)
}

func firewallScript(role, path, action string) string {
	hash := sha256.Sum256([]byte(strings.ToLower(path)))
	group := fmt.Sprintf("MyDesk LAN %s %x", role, hash[:6])
	script := "$ErrorActionPreference='Stop'; $program=" + psQuote(path) + "; $group=" + psQuote(group) + "; foreach ($protocol in @('TCP','UDP')) { $name=$group+' '+$protocol; $existing=Get-NetFirewallRule -Name $name -ErrorAction SilentlyContinue; if ($existing) { $filter=$existing | Get-NetFirewallApplicationFilter; if ($filter.Program -ne $program) { throw 'Existing rule belongs to another program' }; $existing | Remove-NetFirewallRule }; "
	if action == "setup" {
		script += "New-NetFirewallRule -Name $name -DisplayName $name -Group $group -Program $program -Direction Inbound -Action Allow -Enabled True -Profile Any -RemoteAddress LocalSubnet -Protocol $protocol -EdgeTraversalPolicy Block | Out-Null; "
	}
	script += "}"
	if action == "setup" {
		script += "; $blocked=Get-NetFirewallApplicationFilter -Program $program -ErrorAction SilentlyContinue | Get-NetFirewallRule | Where-Object { $_.Enabled -eq 'True' -and $_.Direction -eq 'Inbound' -and $_.Action -eq 'Block' }; if ($blocked) { throw ('Allow rules added, but explicit blocking rules still exist: '+(($blocked.Name) -join ', ')) }"
	}
	return script
}

func firewallAction(role, action string) error {
	if action != "setup" && action != "remove" {
		return fmt.Errorf("-firewall 只支持 setup（配置）或 remove（撤销）")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, err = filepath.Abs(exe)
	if err != nil {
		return err
	}
	shell := syscall.NewLazyDLL("shell32.dll")
	admin, _, _ := shell.NewProc("IsUserAnAdmin").Call()
	if admin == 0 {
		verb, _ := syscall.UTF16PtrFromString("runas")
		file, _ := syscall.UTF16PtrFromString(exe)
		args, _ := syscall.UTF16PtrFromString("-firewall " + action)
		// Explicit user command; request UAC instead of silently changing policy.
		r, _, _ := shell.NewProc("ShellExecuteW").Call(0, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(file)), uintptr(unsafe.Pointer(args)), 0, 0)
		if r <= 32 {
			return fmt.Errorf("未获得管理员权限或 UAC 已取消（%d）；防火墙未修改", r)
		}
		fmt.Println("已请求管理员权限；请在弹出的确认框完成操作。")
		return nil
	}
	text := "仅为当前程序添加 TCP/UDP 入站允许规则，来源限本地子网，覆盖专用/公用/域配置文件。\n不会关闭防火墙，也不会修改 VPN 策略。\n\n程序：" + exe + "\n\n确认配置？"
	if action == "remove" {
		text = "只撤销当前路径下此程序创建的 MyDesk LAN 防火墙规则。\n\n程序：" + exe + "\n\n确认撤销？"
	}
	caption, _ := syscall.UTF16PtrFromString("MyDesk LAN 防火墙配置")
	prompt, _ := syscall.UTF16PtrFromString(text)
	r, _, _ := ucall("MessageBoxW", 0, uintptr(unsafe.Pointer(prompt)), uintptr(unsafe.Pointer(caption)), 0x24|0x100)
	if r != 6 {
		fmt.Println("操作已取消；防火墙未修改。")
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := helperCommand(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", firewallScript(role, exe, action))
	output, err := cmd.CombinedOutput()
	result := "已完成。规则只作用于当前程序路径；更换目录后需要重新配置。"
	style := uintptr(0x40)
	if err != nil {
		result = fmt.Sprintf("配置失败：%v\n%s", err, output)
		style = 0x10
	}
	prompt, _ = syscall.UTF16PtrFromString(result)
	ucall("MessageBoxW", 0, uintptr(unsafe.Pointer(prompt)), uintptr(unsafe.Pointer(caption)), style)
	if err != nil {
		return fmt.Errorf("防火墙操作失败：%w；%s", err, output)
	}
	fmt.Println(result)
	return nil
}
