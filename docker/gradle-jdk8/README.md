# Java 构建执行器镜像

核对日期：2026-09-09。镜像示例名为 `ops-builder-gradle:jdk8`，实际构建以同目录 [Dockerfile](Dockerfile) 为准。

## 构建上下文

当前 Dockerfile 使用镜像仓库中的 `eclipse-temurin:8-jdk` 基础镜像，并复制外部提供的 Gradle、Maven 安装目录及 Gradle 配置。仓库没有附带这些目录，需要先准备：

```text
构建目录/
├── Dockerfile
├── gradle/             包含 bin/gradle、lib/ 等完整发行目录
├── maven/              包含 bin/mvn、lib/、conf/ 等完整发行目录
└── gradle.properties
```

旧方案使用 Gradle 6.8.3 和 Maven 3.5.4；当前 Dockerfile 不下载或锁定这两个版本，实际版本由复制进来的安装目录决定。Maven settings 放入 `maven/conf/settings.xml`，Gradle 初始化脚本放入 `gradle/init.d/`；单独提供 settings.xml／init.gradle 不能满足当前 COPY 指令。

## 构建与验证

在准备好的上下文目录执行：

```bash
docker build -t ops-builder-gradle:jdk8 .
docker run --rm ops-builder-gradle:jdk8 java -version
docker run --rm ops-builder-gradle:jdk8 gradle --version
docker run --rm ops-builder-gradle:jdk8 mvn --version
```

Gradle 安装到 `/opt/gradle`，Maven 安装到 `/opt/maven`，工作目录为 `/workspace`。构建依赖基础镜像和 apt 源可达。

## 平台使用

在“设置 → 编译配置”选择 Java、Docker 模式，指定镜像、命令、制品目录及 CPU／内存限制。以下是手动运行示例；平台实际通过 Docker SDK 启动容器，命令来自配置：

```bash
docker run --rm \
  -v /path/to/source:/workspace \
  -v /opt/gradle-cache:/root/.gradle/caches \
  -v /opt/maven-repo:/root/.m2/repository \
  -w /workspace \
  ops-builder-gradle:jdk8 \
  sh -c "gradle clean build -x test"
```

缓存目录通过编译配置的挂载项维护。此镜像用于业务 Java 应用编译，不是 Go 平台服务镜像。本次文档更新未重新构建该镜像。
