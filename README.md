# YCFRP

**YCFRP** 是一款内置 frp 内核的 FRP 双端（frps / frpc）管理面板，单二进制、无外部运行时依赖。

把 frp 的服务端与客户端管理合并进同一个程序：多实例、隧道管理、流量监控、中文日志诊断、多渠道通知、自定义界面，一个面板全搞定。

[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)

## 特性

- **双端一体**：同一套面板可同时托管多个 frps 服务端与多个 frpc 客户端，各自独立连接、独立隧道、独立启停
- **隧道管理**：隧道按实例归属，支持粘贴 frpc 配置批量导入、frps 导出标准 frpc 配置
- **流量监控**：同时采集本机网卡速率与各隧道的上下行流量、连接数，支持月度配额提醒
- **中文日志**：内核日志统一转成中文，自动匹配常见故障并给出成因与处理建议，支持一键导出
- **多渠道通知**：QQ 官方机器人、OneBot、企业微信、钉钉、飞书、Server 酱、Bark、Telegram 与通用 Webhook
- **界面自定义**：主题色、壁纸、站点名称、多套界面样式、导航布局、毛玻璃效果均可自定义
- **多端部署**：Linux（Docker / 二进制）、Windows（图形界面 exe / 控制台 exe）、飞牛 fnOS（fpk 应用包）

## 快速开始

### 从源码构建

要求 Go 1.25+（无需 CGO）。

```bash
go build ./...
go vet ./internal/...
go test ./internal/... -count=1
```

构建面板二进制：

```bash
# 控制台版（Linux / Windows 通用，跨平台交叉编译）
go build -trimpath -ldflags "-s -w" -o ycfrp ./cmd/ycfrp

# Windows 图形界面版（仅 Windows）
go build -trimpath -ldflags "-s -w -H windowsgui" -o YCFRP.exe ./cmd/ycfrp-gui
```

### 运行

```bash
# 前台运行
./ycfrp -c /var/lib/ycfrp -p 38080

# 后台守护
./ycfrp -d -c /var/lib/ycfrp -p 38080
```

浏览器访问 `http://<服务器IP>:38080`，默认账号 `admin / admin`（首次登录后请立即修改）。

### 命令行参数

| 参数 | 说明 |
| --- | --- |
| `-c` / `--config` | 数据目录（配置、日志、隧道列表） |
| `-p` / `--port` | 面板监听端口 |
| `-d` / `--daemon` | 后台运行 |
| `-stop` | 停止后台运行的面板 |
| `-status` | 查看后台运行状态 |
| `-open` | 用默认浏览器打开面板 |
| `-v` / `--version` | 显示版本信息 |
| `--reset-password` | 重置登录账号为 admin/admin |

### Docker 部署

按你的设备架构选择镜像与 compose 文件。

| 架构 | 镜像标签 | compose 文件 |
| --- | --- | --- |
| x86 / amd64 | `ycyingchen/ycfrp:amd64-latest` | `docker-compose.yml`（仓库根目录） |
| ARM64（树莓派 4/5 等） | `ycyingchen/ycfrp:arm64-latest` | `deploy/pack/docker-compose-arm64.yml` |
| ARMv7（32 位 ARM） | `ycyingchen/ycfrp:armv7-latest` | `deploy/pack/docker-compose-armv7.yml` |

```bash
mkdir -p /opt/ycfrp && cd /opt/ycfrp
# 把对应架构的 compose 文件重命名为 docker-compose.yml 放到当前目录
docker compose up -d
docker compose logs -f
```

**x86 / amd64** 的 `docker-compose.yml` 完整内容（ARM 版仅 `image` 标签不同）：

```yaml
services:
  ycfrp:
    image: ycyingchen/ycfrp:amd64-latest   # ARM64 改 arm64-latest，ARMv7 改 armv7-latest
    container_name: ycfrp
    restart: unless-stopped
    environment:
      - TZ=Asia/Shanghai
    volumes:
      # 配置、隧道列表与日志都保存在这里，备份该目录即可完整迁移。
      - ./data:/var/lib/ycfrp
      # 挂载宿主机 docker.sock，让容器内的面板能自助拉取/载入镜像并重启自身容器。
      - /var/run/docker.sock:/var/run/docker.sock
    # frps 需要监听通信端口以及每条隧道映射的远程端口（默认 20000-60000 区间），
    # 使用宿主机网络可省去逐一映射端口，也是 frps 服务端的推荐方式。
    network_mode: host
```

**直接拉取镜像（不用 compose）：**

```bash
# x86 / amd64
docker pull ycyingchen/ycfrp:amd64-latest

# ARM64
docker pull ycyingchen/ycfrp:arm64-latest

# ARMv7（32 位）
docker pull ycyingchen/ycfrp:armv7-latest
```

> **说明**：`network_mode: host` 让新增隧道无需逐个映射端口；如需桥接网络，删除该行并在 `ports` 里补齐 38080 / 7000 / 7500 / 8080 / 8443 等端口。

### 飞牛 fnOS 部署

下载 fpk 应用包，在飞牛应用中心「手动安装」即可，桌面图标内嵌打开面板。详见 [docs/fpk-guide.md](docs/fpk-guide.md)。

## 相关链接

- **GitHub 仓库**：<https://github.com/YCyingchen/ycfrp>
- **下载页**：<https://ycfrp.yc1.cc.cd>
- **Docker Hub**：<https://hub.docker.com/r/ycyingchen/ycfrp>
- **frp 内核**：<https://github.com/fatedier/frp>

## 目录结构

| 路径 | 说明 |
| --- | --- |
| `cmd/ycfrp` | 主程序入口（控制台版，跨平台） |
| `cmd/ycfrp-gui` | Windows 图形界面入口（WebView2 桌面窗口） |
| `internal/api` | HTTP 接口层，面板所有 REST 接口与静态资源服务 |
| `internal/frp` | frp 内核封装：实例生命周期、状态采集、仪表盘对接 |
| `internal/app` | 应用装配：配置、隧道、实例、通知、监控的串联 |
| `internal/config` | 配置模型与持久化 |
| `internal/logx` | 日志系统与内核日志中文化翻译 |
| `internal/monitor` | 主机资源与网卡流量采集 |
| `internal/notify` | 多渠道通知分发 |
| `internal/qqbot` | QQ 官方机器人网关 |
| `internal/updater` | 自更新（下载/校验/助手进程替换） |
| `internal/webui/static` | 面板前端页面，编译时嵌入二进制 |
| `deploy` | Dockerfile、compose 文件、安装说明 |
| `docs` | 使用文档（fpk 教程等） |
| `scripts` | 构建、打包、fpk 生成脚本 |
| `third_party/frp` | frp v0.71.0 内核源码（含本项目的本地补丁，见下文） |

## 关于 third_party/frp

本项目的 frp 内核基于 [fatedier/frp](https://github.com/fatedier/frp) v0.71.0（Apache 2.0），
通过 `go.mod` 的 `replace` 指向 `third_party/frp` 本地副本。

本地副本相对上游的改动：修复了 `server.Service.Close()` 不关闭 HTTP 虚拟主机监听端口
（默认 8080）的问题——上游该端口在 Close 后不释放，导致面板应用新配置、原地重启内核时
端口被旧实例占用而启动失败。

## 默认端口

| 端口 | 用途 |
| --- | --- |
| 38080 | 管理面板 |
| 7000 | frps 与 frpc 通信端口 |
| 7500 | frp 内核仪表盘（默认仅本机） |
| 8080 / 8443 | HTTP / HTTPS 类型隧道的虚拟主机端口 |
| 20000-60000 | 隧道映射的远程端口区间（可配置） |

## 安装包与下载

安装包通过官方下载页分发，不在本仓库内（见 .gitignore）。

## License

[Apache License 2.0](LICENSE)。frp 内核版权归 [fatedier/frp](https://github.com/fatedier/frp) 及其贡献者所有。
