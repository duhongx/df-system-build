# Windows SSH 反向转发工具

Windows 上运行单个 Go 可执行文件，主动连接你可控的 SSH 中转机，将 Windows 能访问的指定服务器端口转发到中转机的回环端口。Mac 再经中转机登录目标服务器，继续读取日志和排查数据库。目标服务器无须安装工具。

```text
Mac ──SSH──> 中转机 127.0.0.1:22022
                    ↑ 已认证的反向 SSH 隧道
                 Windows ──TCP──> 内网服务器:22
```

## 前提

- Windows 能访问目标服务器的 SSH 端口，以及中转机的 SSH 端口。
- Mac 能 SSH 登录中转机。
- 中转机已有 OpenSSH Server，隧道账号允许反向 TCP 转发；Mac 账号允许向中转机 `127.0.0.1:22022` 转发。
- 隧道账号使用 SSH 私钥认证。目标服务器仍单独使用它原有的 SSH 账号及认证方式；工具不保存目标服务器的密码。

本工具实现标准 SSH 反向端口转发，相当于 Windows 上的 `ssh -N -R 127.0.0.1:22022:服务器IP:22 隧道账号@中转机`。它不依赖 ToDesk 的连接，也不能在没有可达中转机的情况下凭空建立网络通路。

## Windows 使用

1. 将 `ssh-tunnel.exe` 和 `tunnel.example.json` 放入一个目录，将后者复制为 `tunnel.json`。
2. 按实际环境填写中转机地址、隧道账号和目标服务器地址。
3. 放入中转机隧道账号的 OpenSSH 私钥 `tunnel_ed25519`，以及包含已核实中转机主机密钥的 `known_hosts`。相对文件路径以配置文件所在目录为准。PuTTY 的 PPK 私钥需要先导出为 OpenSSH 格式。
4. 在 PowerShell 执行：

```powershell
.\ssh-tunnel.exe -config .\tunnel.json
```

Windows 压缩包内也提供 `start-tunnel.cmd`；配置完成后可双击启动，错误信息会保留在窗口中。

出现 `tunnel ready` 表示中转机端口已建立。目标连接会在每次 Mac 访问时由 Windows 发起；如果 Windows 无法访问目标服务器，日志显示 `connect target ...`。Ctrl+C 关闭隧道。

也可以删除 `known_hosts_file`，改用 `relay_host_key_sha256` 填写从可信渠道核实的中转机主机密钥指纹，例如 `SHA256:...`；二者必须且只能配置一项。中转机管理员可在服务器控制台运行 `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub -E sha256` 获取对应公钥的指纹。指纹必须与服务器实际提供的主机密钥一致。

加密私钥通过环境变量 `SSH_TUNNEL_KEY_PASSPHRASE` 提供口令；不将口令放在配置文件或命令参数中。

## Mac 连接

中转机的 22022 端口只监听回环地址，通过中转机的 SSH 通道访问：

```bash
ssh -J mac中转账号@relay.example.com -p 22022 目标服务器账号@127.0.0.1
```

若中转机 SSH 端口是 2222，将跳板参数改为 `-J mac中转账号@relay.example.com:2222`。目标服务器的主机密钥和登录认证仍由本机 SSH 验证。

多个目标服务器分别使用不同的配置文件和回环端口（如 22022、22023），各启动一个工具实例。为区分目标主机，可在 Mac 的 `~/.ssh/config` 中为每个目标设置别名和 `HostKeyAlias`。

## 中转机账号限制示例

由中转机管理员按现有账号规则合并配置；不要直接覆盖现有 `sshd_config`。例如仅允许隧道账号申请指定的回环转发端口：

```text
Match User tunnel
    AllowTcpForwarding remote
    GatewayPorts no
    PermitListen 127.0.0.1:22022
    AllowAgentForwarding no
    X11Forwarding no
    PermitTTY no
```

上述限制只配置转发权限，不等于禁止该账号运行所有命令。Mac 登录中转机应使用另一个允许本地转发的账号。服务端限制、目标服务器认证和工具连接日志共同适用于这条通路。

工具校验中转机主机密钥，拒绝非回环监听地址，最多转发 16 条并发连接。SSH 传输中断后按 2～30 秒退避重连，并每 15 秒探测连接；单次连接、握手和探测期限为 10 秒。重连会中断已有 SSH 会话，需重新登录目标服务器。只要程序退出，中转机转发端口随 SSH 连接关闭而释放。

## 编译与验证

独立 Go 模块，不修改平台后端依赖；需要 Go 1.26 工具链。

```bash
cd tools/ssh-tunnel
go test -race ./...
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o ../../output/ssh-tunnel/ssh-tunnel-windows-amd64.exe .
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags='-s -w' -o ../../output/ssh-tunnel/ssh-tunnel-darwin-arm64 .
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o ../../output/ssh-tunnel/ssh-tunnel-linux-amd64 .
```

参考：[Go SSH 库](https://pkg.go.dev/golang.org/x/crypto/ssh)、[OpenSSH 反向转发](https://man.openbsd.org/ssh.1#R)、[sshd 转发权限](https://man.openbsd.org/sshd_config.5#AllowTcpForwarding)。
