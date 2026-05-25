# ============================================================
# DF Build System - Gradle JDK8 执行器镜像
# 镜像名: ops-builder-gradle:jdk8
#
# 包含: OpenJDK 8 + Gradle 6.8.3 + Maven 3.5.4
# 用途: Java 后端项目编译（gradle clean build -x test）
#
# 构建方式（在服务器上执行）:
#   1. 将此目录上传到服务器
#   2. 将服务器上的配置文件拷贝进来:
#      cp /usr/local/src/maven/conf/settings.xml ./settings.xml
#      cp /usr/local/src/gradle/init.d/init.gradle ./init.gradle
#   3. docker build -t ops-builder-gradle:jdk8 .
# ============================================================

FROM swr.cn-north-4.myhuaweicloud.com/ddn-k8s/docker.io/eclipse-temurin:8-jdk

RUN apt-get update && apt-get install -y --no-install-recommends \
    git \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

ENV GRADLE_HOME=/opt/gradle
ENV MAVEN_HOME=/opt/maven
ENV PATH="/opt/gradle/bin:/opt/maven/bin:${PATH}"

COPY gradle/ ${GRADLE_HOME}/
COPY maven/ ${MAVEN_HOME}/

RUN mkdir -p /root/.gradle
COPY gradle.properties /root/.gradle/gradle.properties

RUN chmod +x ${GRADLE_HOME}/bin/gradle \
    && chmod +x ${MAVEN_HOME}/bin/mvn \
    && java -version \
    && gradle --version \
    && mvn --version

WORKDIR /workspace

CMD ["bash"]
