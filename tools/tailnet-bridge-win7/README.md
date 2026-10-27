# Windows 7 32 位兼容版

本版本针对 Windows 7 单独使用 **Go 1.20.14 + Tailscale/tsnet 1.44.3** 构建。普通 Win32 版本虽然是 32 位 PE，但使用新版 Go，最低系统要求仍是 Windows 10，不能在 Win7 加载。

## 使用

下载 `tailnet-bridge-10.212.11.18-win7-x86.exe` 到 Windows 7，双击运行。程序已内置目标 **10.212.11.18:22**，转发端口为 **22022**，不需要 JSON 配置文件、OpenSSH、Go 或 Python。

另有 `tailnet-bridge-10.212.11.18-win7-x86-authkey.exe` 一次性授权密钥版：已内置该次打包提供的 Auth Key，首次启动会自动入网，无需网页登录。使用同一个登录状态目录，首次入网成功后会复用设备身份；不要同时运行两个版本。详见 `README-authkey.md`。

首次运行时打开控制台打印的 Tailscale 认证链接，登录 Mac 所在的网络。可以把认证链接复制到已经登录的 Mac 浏览器完成，不要求 Win7 浏览器支持现代登录页面。若网络启用了设备审批，还需批准这个新节点。

出现 `bridge ready` 后，按控制台打印的地址在 Mac 执行：

```text
ssh -p 22022 Linux服务器账号@程序的TailscaleIP
```

保持程序窗口运行。账号和密码属于目标 Linux；Windows 不需要 SSH 服务。登录状态保存在 `%AppData%\df-tailnet-bridge-win7\state`，与现代版隔离，不应分享该目录。程序的设备名为 `windows7-ssh-bridge`。

`-print-config` 可以只查看内置配置，`-check` 仅检查 Windows 到目标 TCP 端口是否连通，`-licenses` 查看内嵌的第三方许可证。

## 兼容性与限制

- Go 官方将 1.20 作为支持 Windows 7/8 的最后一个系列；1.21 起最低 Windows 10/Server 2016。
- Tailscale 官方 1.44.3 是专门提供给 Win7/8 的旧分支安全更新；该分支已停止常规维护。本程序仅用于明确需要 Win7 的临时排查，不代表具备现代版本的安全更新。
- 成功编译和核验 PE 头不等于已在 Win7 实机验证。旧系统补丁、证书、网络访问，以及当前 tailnet 的最低客户端版本策略，仍可能影响登录和连通。
- TLS 和 SSH 的证书、主机密钥及登录认证仍按正常流程验证，不提供跳过验证选项。

## 开发

这是与现代版隔离的兼容模块，不要使用最新 `go` 自动代替旧工具链构建，否则即使仍是 GOARCH=386，也会重新引入 Win10 最低要求。使用从 Go 官方下载并校验过的 **go1.20.14** 编译器：

```bash
GOROOT=/path/to/go1.20.14 CGO_ENABLED=0 /path/to/go1.20.14/bin/go test ./...
GOROOT=/path/to/go1.20.14 CGO_ENABLED=0 GOOS=windows GOARCH=386 GO386=softfloat /path/to/go1.20.14/bin/go build -trimpath -ldflags='-s -w' -o ../../output/tailnet-bridge/tailnet-bridge-10.212.11.18-win7-x86.exe .
```

显式设置 `GOROOT` 可避免本机已有环境变量把旧版编译器指向新版标准库和链接器。

该模块的转发和配置代码来自现代版，并只作 Go 1.20/旧 tsnet API 兼容调整。`THIRD_PARTY_LICENSES.txt` 包含本模块 Windows 构建实际使用的依赖及 Go 1.20.14 的许可证；更新依赖时需重新生成后再编译并验证 `-licenses`。

构建内置一次性密钥版时，将密钥保存到项目外的私有文件，使用：

```bash
python3 build_authkey.py --go /path/to/go1.20.14/bin/go --key-file /private/path/authkey
```

脚本使用私有临时目录和 Go overlay 注入密钥，不将真实密钥写入源码、命令行参数或配置输出。编译不会向 Tailscale 发起设备入网请求。公用源码中的 `authkey_bundle.go` 始终为空密钥；密钥版程序及其压缩包已从 Git 排除。该版屏蔽网页登录提示，连接等待上限为 2 分钟，失败时需检查网络、密钥是否已使用/过期，以及设备审批。`TS_AUTHKEY`（兼容 `TS_AUTH_KEY`）可在确实需要重新入网时替换内置密钥。

依据：[Go 1.20 系统支持](https://go.dev/doc/go1.20#windows)、[Tailscale 旧版发布记录](https://tailscale.com/changelog#2024-01-08)。
