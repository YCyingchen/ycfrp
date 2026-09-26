# YCFRP 官方镜像：内置 frps / frpc 内核的 FRP 双端管理面板。
# 构建上下文需包含仓库根目录，构建时先放入对应架构的静态二进制：
#   docker build -f deploy/Dockerfile -t ycyingchen/ycfrp:s2609.034 .

FROM debian:12-slim

LABEL org.opencontainers.image.title="YCFRP" \
      org.opencontainers.image.description="FRP 双端管理面板，内置 frps / frpc 内核" \
      org.opencontainers.image.version="s2609.034" \
      org.opencontainers.image.source="https://hub.docker.com/r/ycyingchen/ycfrp"

# 面板与内核依赖的系统证书及时区数据，安装后清空 apt 缓存以压缩体积。
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends ca-certificates tzdata libcap2-bin; \
    ln -snf /usr/share/zoneinfo/Asia/Shanghai /etc/localtime; \
    echo "Asia/Shanghai" > /etc/timezone; \
    rm -rf /var/lib/apt/lists/*

ENV TZ=Asia/Shanghai

# TARGETARCH / TARGETVARIANT 由 buildx 按目标平台自动注入，用于挑选对应架构的产物。
# 这里特意不给 ARG 默认值：一旦写成 ARG TARGETARCH=amd64，默认值会盖掉平台注入值，
# 导致三个架构全部装进 amd64 二进制。空值时在脚本里回退到 amd64。
# 产物通过 bind 挂载读取，不写入镜像层，避免把三个架构的二进制都塞进镜像。
ARG TARGETARCH
ARG TARGETVARIANT
RUN --mount=type=bind,source=dist,target=/tmp/ycfrp-bins \
    set -eux; \
    ARCH="${TARGETARCH:-amd64}"; \
    if [ "$ARCH" = "arm" ]; then \
        BIN=/tmp/ycfrp-bins/ycfrp-linux-armv7; \
        WANT=40; \
    elif [ "$ARCH" = "arm64" ]; then \
        BIN=/tmp/ycfrp-bins/ycfrp-linux-arm64; \
        WANT=183; \
    else \
        BIN=/tmp/ycfrp-bins/ycfrp-linux-amd64; \
        WANT=62; \
    fi; \
    test -x "$BIN"; \
    install -m 0755 "$BIN" /usr/local/bin/ycfrp; \
    HAVE=$(od -An -tu1 -j18 -N1 /usr/local/bin/ycfrp | tr -d ' '); \
    echo "目标架构=$ARCH$TARGETVARIANT 期望机器码=$WANT 实际机器码=$HAVE"; \
    test "$HAVE" = "$WANT"
# 交叉架构下不执行二进制校验：qemu 模拟运行 Go 静态产物可能出现伪失败，
# 上面的 ELF 机器码比对已能确保装入的是对应架构的产物。

# 以非 root 用户运行：创建专用账号并让其拥有数据目录；用文件能力
# cap_net_bind_service 允许绑定 frps 的 7000/7500 低位端口（客户端无需 root）。
RUN set -eux; \
    groupadd -g 1000 ycfrp; \
    useradd -u 1000 -g ycfrp -d /var/lib/ycfrp -s /usr/sbin/nologin ycfrp; \
    mkdir -p /var/lib/ycfrp; \
    chown -R ycfrp:ycfrp /var/lib/ycfrp; \
    setcap 'cap_net_bind_service=+ep' /usr/local/bin/ycfrp

# 配置、日志与隧道数据统一存放在数据卷中，便于容器重建后保留。
VOLUME ["/var/lib/ycfrp"]

# 38080 面板，7000 frps 通信，7500 内核仪表盘，8080/8443 虚拟主机。
EXPOSE 38080 7000 7500 8080 8443

USER ycfrp

ENTRYPOINT ["/usr/local/bin/ycfrp"]
CMD ["-c", "/var/lib/ycfrp", "-p", "38080"]
