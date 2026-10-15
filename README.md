# MyDesk

MyDesk 是一套 Go 编写的 Windows 远程桌面项目，仓库包含原有的服务器中继版本，以及独立的局域网直连版本。

## 两种连接方式

| 版本 | 入口 | 连接方式 |
| --- | --- | --- |
| 服务器版 | `client.go`、`controller.go`、`server.go` | 被控端和主控端经服务器建立远程会话；支持的传输和画面参数以程序 `-h` 帮助为准。 |
| 局域网版 | `lan/cmd/client`、`lan/cmd/controller` | 主控端与被控端在局域网内直连；TCP/TLS 用于配对和控制，优先 UDP 传视频，UDP 不通时可回退 TCP。无需运行服务器版。 |

局域网版有独立的协议、配置和构建目录，不会替换服务器版。当前连接入口和运行说明见 [局域网版文档](lan/README.md)，第三方运行时与许可信息见 [第三方组件说明](lan/THIRD_PARTY.md)。

## 源码结构

- `client.go`：服务器版被控端
- `controller.go`：服务器版主控端
- `server.go`：服务器版服务端
- `lan/`：局域网直连客户端、主控端、测试、构建脚本和文档
- `verification/baseline-20260925/`：用于对照验证的旧版源码快照

仓库不包含编译好的 EXE、FFmpeg 运行时和发行 ZIP。机器专属的 `client.identity.json` 也不纳入版本管理。

## 构建

需要安装 Go。根目录模块声明 Go 1.24，并指定 Go 1.25.7 工具链。Windows 下可从项目根目录构建服务器版：

```bat
compiled.cmd client.go
compiled.cmd controller.go
compiled.cmd server.go
```

服务器版默认连接本机 `127.0.0.1:19998`。部署到其他机器时，可通过 `MYDESK_SERVER_URL` 环境变量设置自己的服务端地址，或在启动客户端/主控端时使用 `-server`（主控端也支持 `-s`）；仓库不保存部署者的公网 IP 或域名。

局域网版构建脚本位于 `lan`：

```powershell
cd lan
.\build.ps1
```

输出位于 `lan/dist/`。局域网版所需 FFmpeg 运行时安装、启动参数、防火墙设置和功能说明请查看 [lan/README.md](lan/README.md)。
