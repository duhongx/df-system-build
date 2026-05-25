# Vue 前端应用编译构建流程

## 应用分类

| 类型 | 角色 | 示例 | 镜像 | K8s 资源 |
|------|------|------|------|---------|
| 主应用 | main | web-main | web-main:{tag} | 共用 Deployment/Service/ConfigMap/Ingress |
| 子应用 | sub | web-menzhenysz(04)、web-yewugymk(gymk) | web-main:{tag}（共用） | 共用 web-main 的资源 |
| 独立应用 | standalone | web-opm、web-cdr | {appName}:{tag} | 独立 Deployment/Service/ConfigMap/Ingress |

---

## 独立应用流程（web-opm、web-cdr）

流程简单，和 Java 微服务类似：

```
编译/上传 → 得到 {appName}.zip
→ 解压 dist/ 到 docker build context
→ 用 dockerfile-web 模板构建独立镜像 {appName}:{tag}
→ 推送镜像
→ 更新独立的 Deployment
```

---

## 主应用 + 子应用流程

### 场景 A：制品上传 — 首次部署（web-main Pod 不存在）

**前提：** 用户批量上传了 web-main.zip + 所有子应用 zip

```
1. 识别上传文件中的 web-main.zip（主应用）
2. 识别其他 zip 为子应用（根据应用管理中 appCode 匹配文件名）
3. 创建 html/ 目录
4. 解压 web-main.zip → html/（主应用文件：index.html、assets/ 等）
5. 创建 html/apps/ 目录
6. 遍历子应用 zip，解压到 html/apps/{appCode}/
   - 04.zip → html/apps/04/
   - gymk.zip → html/apps/gymk/
   - ...
7. 生成 Dockerfile（FROM nginx → COPY html/ /usr/share/nginx/html/）
8. 构建镜像 web-main:{branch}-{timestamp}
9. 推送镜像
10. 创建 K8s Deployment + Service + ConfigMap + Ingress
```

### 场景 B：拉取代码 — 更新主应用（web-main Pod 已存在）

```
1. git clone + 编译 → 得到 web-main.zip
2. 获取构建锁（防止并发冲突）
3. 从运行中的 web-main Pod 拷贝 /usr/share/nginx/html → 本地 html/
4. 备份 html/apps/ 目录（子应用不动）
5. 清空 html/ 目录
6. 解压 web-main.zip → html/（新的主应用文件）
7. 恢复 html/apps/（子应用原封不动放回来）
8. 生成 Dockerfile
9. 构建镜像 web-main:{branch}-{timestamp}
10. 推送镜像
11. 更新 K8s Deployment 镜像
```

### 场景 C：拉取代码 — 更新子应用（web-main Pod 已存在）

```
1. git clone + 编译 → 得到 {appCode}.zip（如 04.zip）
2. 获取构建锁
3. 从运行中的 web-main Pod 拷贝 /usr/share/nginx/html → 本地 html/
4. 删除 html/apps/{appCode}/ 目录（只清理这一个子应用）
5. 解压 {appCode}.zip → html/apps/{appCode}/（新的子应用文件）
6. 其他文件（主应用 + 其他子应用）保持不变
7. 生成 Dockerfile
8. 构建镜像 web-main:{branch}-{timestamp}
9. 推送镜像
10. 更新 K8s Deployment 镜像
```

### 场景 D：制品上传 — 更新（web-main Pod 已存在）

流程和场景 B/C 完全一致，区别只是第 1 步：
- 编译模式：源码编译 → 得到 zip
- 制品上传模式：用户直接上传 zip

后续步骤（从 Pod 拷贝 → 替换 → 构建镜像 → 推送 → 部署）完全相同。

---

## 制品命名规则

| 角色 | ArtifactName | 说明 |
|------|-------------|------|
| main | `web-main.zip` | 固定名 |
| sub | `{appCode}.zip` | 如 `04.zip`、`gymk.zip` |
| standalone | `{appName}.zip` | 如 `web-cdr.zip` |

---

## K8s 资源对应关系

| 角色 | Deployment | Service | ConfigMap | Ingress |
|------|-----------|---------|-----------|---------|
| main | web-main | web-main | configmap-web-main | ✓ |
| sub | web-main（共用） | web-main（共用） | 共用 | 共用 |
| standalone | {appName} | {appName} | configmap-{appName} | ✓ |

---

## 与 Python 老代码的对比

### 核心差异：真相源不同

| | Python 老代码 | df-build-system |
|---|---|---|
| html 真相源 | 服务器本地磁盘 `workspace/web-main/dockerfile/dist/` | K8s Pod 内 `/usr/share/nginx/html` |
| 优点 | 简单直接，不依赖 Pod 状态 | 不需要在 build server 上维护持久目录 |
| 缺点 | build server 磁盘是单点 | Pod 异常时拷贝会失败 |

### 流程对比

| 操作 | Python 老代码 | df-build-system |
|------|-------------|-----------------|
| 更新主应用 | find 删除非 apps 文件 → cp 新文件 | 从 Pod 拷贝 → 备份 apps → 清空 → 解压 → 恢复 apps |
| 更新子应用 | rm -rf apps/{code}/* → cp 新文件 | 从 Pod 拷贝 → 删除 apps/{code} → 解压 |
| 独立应用 | 独立 dist 目录 + 独立镜像 | 独立 Deployment + 独立镜像 |
| 首次部署 | 不存在（本地 dist 持久化） | bundleSubApps 从 source 目录查找子应用 zip |
| 并发控制 | 无（单线程脚本） | sync.Mutex 构建锁（per namespace） |
| 编译命令 | 硬编码在 Python 中 | 从 BuildConfig 数据库读取 |
| 镜像构建 | docker CLI | Docker SDK |

### 结论

核心流程一致，没有逻辑错误。主要差异在于真相源位置和并发控制。新代码更适合 K8s 环境。
