# ops-builder-gradle:jdk8 执行器镜像

## 构建步骤（在 192.168.1.10 服务器上执行）

```bash
# 1. 创建构建目录
mkdir -p /tmp/ops-builder-gradle && cd /tmp/ops-builder-gradle

# 2. 将 Dockerfile 上传到服务器（或从 git 拉取）
# 如果已经 clone 了项目：
cp /path/to/df-build-system/docker/gradle-jdk8/Dockerfile .

# 3. 拷贝服务器上现有的配置文件
cp /usr/local/src/maven/conf/settings.xml ./settings.xml
cp /usr/local/src/gradle/init.d/init.gradle ./init.gradle
cp /root/.gradle/gradle.properties ./gradle.properties

# 4. 构建镜像
docker build -t ops-builder-gradle:jdk8 .

# 5. 验证
docker run --rm ops-builder-gradle:jdk8 gradle --version
docker run --rm ops-builder-gradle:jdk8 mvn --version
docker run --rm ops-builder-gradle:jdk8 java -version
```

## 目录结构

构建前确保当前目录有这 3 个文件：
```
/tmp/ops-builder-gradle/
├── Dockerfile        ← 本文件同目录
├── settings.xml      ← 从 /usr/local/src/maven/conf/settings.xml 拷贝
├── init.gradle       ← 从 /usr/local/src/gradle/init.d/init.gradle 拷贝
└── gradle.properties ← 从 /root/.gradle/gradle.properties 拷贝
```

## 镜像内容

| 组件 | 版本 | 路径 |
|---|---|---|
| OpenJDK | 1.8 | /usr/local/openjdk-8 |
| Gradle | 6.8.3 | /opt/gradle |
| Maven | 3.5.4 | /opt/maven |
| settings.xml | - | /opt/maven/conf/ + /root/.m2/ |
| init.gradle | - | /opt/gradle/init.d/ + /root/.gradle/init.d/ |

## 使用方式

系统构建时会自动以如下方式启动容器：
```bash
docker run --rm \
  -v /path/to/source:/workspace \
  -w /workspace \
  ops-builder-gradle:jdk8 \
  sh -c "gradle clean build -x test"
```

## 缓存加速（可选）

挂载宿主机的 Gradle/Maven 缓存目录，避免每次重新下载依赖：
```bash
docker run --rm \
  -v /path/to/source:/workspace \
  -v /opt/gradle-cache:/root/.gradle/caches \
  -v /opt/maven-repo:/root/.m2/repository \
  -w /workspace \
  ops-builder-gradle:jdk8 \
  sh -c "gradle clean build -x test"
```

首次构建后缓存会留在宿主机，后续构建直接复用。
