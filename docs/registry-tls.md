# 镜像仓库 HTTPS 与私有 CA

镜像仓库设置支持填写 `https://registry.example.com:5000`。不带协议的旧地址继续按 HTTP 处理，以兼容现有 Nexus 配置。

如果仓库证书由系统已信任的 CA 签发，不需要额外填写证书。使用私有 CA 或自签证书时，在“镜像仓库设置 → 服务器证书路径”填写平台后端主机上的 CA 文件绝对路径并点击“读取”，也可以直接粘贴 PEM 格式的 CA 证书链。读取的文件必须仅包含 CA 证书，大小不能超过 256 KB。此配置用于平台后端的连接测试和镜像标签查询。

读取接口只接受普通文件，并校验证书 PEM、CA 属性和文件大小；路径必须是运行平台后端的服务器上的绝对路径。

Docker 构建、推送由 Docker Engine 发起网络连接。Linux Docker Engine 还需在仓库主机对应的证书目录安装同一 CA，例如：

```text
/etc/docker/certs.d/registry.example.com:5000/ca.crt
```

使用默认 HTTPS 端口 443 时，目录名不带端口。若 Docker Engine 在其他主机或以远程方式运行，应在实际运行 Engine 的主机配置证书。

Kubernetes 镜像拉取由集群节点的容器运行时发起连接，需在每个相关节点配置并信任该 CA。具体配置位置取决于节点使用的 Docker、containerd 或其他运行时。`imagePullSecrets` 配置的是仓库账号凭据，不会安装 CA 证书。

目前设置页的 CA 字段配置的是**服务端证书信任**。若仓库要求双向 TLS（mTLS）并要求客户端出示证书，还需分别配置后端 HTTP 客户端、Docker Engine 和 Kubernetes 节点的客户端证书及私钥；平台设置页目前不管理这组客户端凭据。
