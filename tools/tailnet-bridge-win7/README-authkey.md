# Windows 7 32 位：一次性授权密钥版

运行 `tailnet-bridge-10.212.11.18-win7-x86-authkey.exe`，无需安装 Tailscale、OpenSSH 或其他运行环境，不需要配置文件。

1. 先关闭之前运行的桥接程序，再双击此文件。
2. 程序使用内置的一次性 Auth Key 自动加入打包时指定的 Tailscale 网络，无需在 Windows 浏览器登录。
3. 出现 `bridge ready` 后保持窗口开启，将窗口显示的 Tailscale IP 或 SSH 命令提供给 Mac 端。

内置目标是 **10.212.11.18:22**，Tailscale 内的监听端口为 **22022**，设备名为 `windows7-ssh-bridge`。

Auth Key 的有效期限制首次设备入网。本次提供的密钥由用户设置为 2 天有效，需在到期前完成首次入网；程序无法离线核验它在服务端的确切到期时间或已使用状态。成功入网后，设备身份保存在 `%AppData%\df-tailnet-bridge-win7\state`，后续正常启动复用该身份，不会因为首次入网密钥两天后到期而立即掉线。设备自身的密钥到期、删除或撤销仍可能要求重新入网。

同一把一次性密钥只能用于一台新设备。首次成功后不要删除状态目录，也不要将该程序用来给第二台机器入网。若确实需要重新入网，应生成新 Auth Key 并重新打包。若网络启用了设备审批，还需在 Tailscale 管理后台批准新节点。

连接超过 2 分钟会给出超时错误。若未出现 `bridge ready`，保留窗口末尾错误内容以便排查。`-print-config` 仅显示非敏感配置，`-check` 仅检查 Windows 到目标 TCP 端口的连通性，均不会消耗一次性入网密钥。测试编译与文件格式不等于已经验证 Win7 实机入网。

本版本使用 Go 1.20.14 / Tailscale 1.44.3 兼容 Windows 7，旧依赖已停止常规维护，适合此次临时排查。程序内含入网凭据，请仅传给预定的 Windows 机器；不要公开上传程序或复制登录状态目录。

依据：[Tailscale 一次性 Auth Key](https://tailscale.com/docs/features/access-control/auth-keys)。
