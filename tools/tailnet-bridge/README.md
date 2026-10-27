# Windows 免安装 Tailnet SSH 转发

单个 Go 程序内置官方 `tailscale.com/tsnet` 用户态组网库，作为独立设备加入 Mac 所在的 Tailscale 网络，再使用 Windows 自己的网络连接指定 Linux SSH 服务器。

```text
Mac（已登录 Tailscale）
  → Windows 便携程序的 Tailscale IP:22022
  → Windows 实际网卡
  → 内网 Linux IP:22
```

Windows 不需要安装 Tailscale 客户端、OpenSSH、Python 或 Go；不创建系统 VPN 网卡、系统服务或系统路由。目标 Linux 保留已有 SSH 服务和认证方式。Tailscale 的协调及必要时的中继使用其服务基础设施，不要求自备公网中转机。受管理电脑的应用执行和网络策略仍然适用。

## Windows 双击直接运行

下载 `tailnet-bridge-10.212.11.18.exe`，放到 Windows 任意普通目录后双击即可。不需要配置文件、启动脚本或安装程序。

32 位 Windows 使用 `tailnet-bridge-10.212.11.18-win32.exe`；两种架构内置相同的目标地址和转发端口。

上述现代版均要求 Windows 10 / Windows Server 2016 或更高版本，32 位不代表兼容 Win7。Windows 7 请使用单独的 `tailnet-bridge-10.212.11.18-win7-x86.exe` 兼容版本，说明见相邻的 `tailnet-bridge-win7` 目录。

内置目标为 **10.212.11.18:22**，Tailscale 内的转发端口为 **22022**，设备名为 `windows-ssh-bridge`。首次运行会打印 Tailscale 登录链接；在 Windows 浏览器打开，登录 Mac 所在的同一个 Tailscale 网络。显示 `bridge ready` 后保持窗口运行，使用程序打印的 SSH 命令从 Mac 连接。

登录身份自动保存在 Windows 当前用户的 `%AppData%\df-tailnet-bridge\state`，与 exe 放置位置无关。后续运行复用该身份；是否需要重新认证取决于网络的认证策略。这个目录不是外部配置文件，不能随工具分享。

无参数启动始终使用内置目标，不读取目录中遗留的 `bridge.json`。双击启动出错时会保留控制台窗口，按回车才关闭。可在命令行使用 `-print-config` 查看内置配置，或 `-licenses` 查看已嵌入的第三方许可证。

## 可选：使用配置文件指定其他目标

需要其他目标时，仍可以用下面的配置方式；仅在显式传入 `-config bridge.json` 时才读取配置文件。

1. 解压 Windows 压缩包到可写目录。
2. 双击 `start-bridge.cmd`。第一次会创建 `bridge.json` 并停止，要求填写实际目标。
3. 编辑 `bridge.json`：

```json
{
  "hostname": "windows-ssh-bridge",
  "target_address": "192.168.1.150:22",
  "listen_port": 22022,
  "state_directory": "state"
}
```

- `hostname`：这个便携程序在 Tailscale 中的设备名称，多台 Windows 使用不同名称。
- `target_address`：Windows 上原有 SSH 工具实际连接的服务器 IP 和 SSH 端口。示例 IP 必须按实际情况修改。
- `listen_port`：程序在 Tailscale 网络中提供的转发端口，通常保留 22022。
- `state_directory`：节点身份和登录状态保存目录，相对于配置文件所在目录。保留此目录可以复用登录状态；不要分享其中的文件。

4. 可先在 PowerShell 检查 Windows 到目标端口是否连通（不加入 Tailscale、不验证 SSH 密码）：

```powershell
.\tailnet-bridge.exe -config .\bridge.json -check
```

5. 再次双击 `start-bridge.cmd`。首次联网时控制台显示 Tailscale 登录链接；在 Windows 浏览器打开它，使用 Mac 已加入的同一个 Tailscale 网络登录。若该网络启用了设备审批，需要管理员批准这个新节点。
6. 显示 `bridge ready` 后保持窗口运行，记下程序打印的 Mac SSH 命令。Ctrl+C 退出。

不需要填写 `relay_address`、中转机账号、SSH 私钥或 `known_hosts`；这是与旧版 `ssh-tunnel` 不同的组网实现。程序在 tailnet 中接收原始 TCP 流量，再通过 Windows 原生网络连接固定目标，不提供任意目标 SOCKS 代理。

## Mac 连接

以程序实际打印的 IP 为准：

```bash
ssh -p 22022 目标Linux账号@Windows程序的TailscaleIP
```

此处的账号、密码或私钥属于目标 Linux，不是 Windows 账号。首次出现的 SSH 主机密钥也属于目标 Linux。Windows 未安装 SSH 服务并不影响这条通路。

这不是 Tailscale SSH 身份代管，而是通过 Tailscale 网络访问 Linux 原有 SSH。Tailscale 网络策略必须允许 Mac 到新节点的 TCP 22022；程序不自动修改网络策略、不自动批准节点。程序只提供一个指定目标的转发，最多 16 个并发连接。它可供同一 tailnet 中获准访问该端口的设备连接。

## 排查

- `target ... is not reachable` / `connect target`：Windows 到内网服务器 IP/端口不通，先检查实际地址和服务器 SSH 服务。
- 一直等待登录：打开程序打印的认证 URL，确认登录到了 Mac 所在的网络，并检查是否待设备审批。
- `bridge ready` 但 Mac 无法连接：检查 Mac 是否仍连接 Tailscale、使用的是否为程序打印的新节点 IP，以及网络策略是否允许 TCP 22022。
- 能看到 SSH 登录提示但认证失败：检查目标 Linux 的账号、密码或本地 SSH 私钥，与 Windows 是否安装 OpenSSH 无关。
- 退出后不可达：程序必须保持运行，已建立的 SSH 会话会随程序退出而断开。

## 开发验证

独立模块，不修改平台后端依赖。构建需要模块指定的 Go 工具链，可通过 Go 的自动工具链选择取得；运行可执行文件不需要 Go。

```bash
go test -race ./...
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o ../../output/tailnet-bridge/tailnet-bridge-windows-amd64.exe .
CGO_ENABLED=0 GOOS=windows GOARCH=386 GO386=sse2 go build -trimpath -ldflags='-s -w' -o ../../output/tailnet-bridge/tailnet-bridge-10.212.11.18-win32.exe .
```

本地测试覆盖配置解析、TCP 双向转发、半关闭以及关闭时释放连接；不使用真实 Tailscale 身份。真实 tailnet 登录、跨 NAT 通信和远程 Windows 运行需在实际环境中验证。

依据：[官方 tsnet 介绍](https://tailscale.com/docs/features/tsnet)、[官方 tsnet API](https://tailscale.com/docs/reference/tsnet-server-api)、[连接类型及中继](https://tailscale.com/docs/reference/connection-types)。
