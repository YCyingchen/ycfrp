# YCFRP

**版本：s2609.031** ｜ 内置 **frp 内核 v0.71.0** ｜ 单二进制 FRP 双端管理面板

## 这是什么

YCFRP 把 frp 的 **服务端（frps）** 与 **客户端（frpc）** 管理能力合并进同一个程序，内置 frp 内核，
不依赖任何外部运行时。一套面板即可完成隧道创建、启停、流量监控、日志诊断与多渠道通知。

- **Linux / NAS**：命令行运行，或用本仓库的 `docker-compose.yml` 一条命令部署
- **Windows**：双击即用的图形界面桌面程序（另附控制台版，便于注册为系统服务）
- 面板与内核均为 **中文界面**，占用极低，适合长期常驻

## 快速开始（Docker Compose）

```bash
mkdir -p /opt/ycfrp && cd /opt/ycfrp

# 下载本站提供的 docker-compose.yml 放到当前目录
docker compose up -d
```

启动完成后浏览器打开 `http://<服务器IP>:38080`，默认账号 **admin / admin**（首次登录后请立即修改）。

`docker-compose.yml` 默认使用 `network_mode: host`，这样新增隧道时无需逐个映射端口；
如需桥接网络，把对应远程端口补进 `ports` 列表即可。

## 镜像与标签

| 标签 | 说明 |
| --- | --- |
| `s2609.031` | 当前版本，版本号格式为 `s` + 年份 + 月份 + 当月修订次数 |
| `latest` | 指向最新版本，便于自动升级 |

```bash
docker pull ycyingchen/ycfrp:s2609.031
```

## 数据持久化

面板配置、隧道列表、上传文件与日志统一保存在容器内 `/var/lib/ycfrp`，
compose 文件已将其映射到宿主机的 `./data`，备份该目录即可完整迁移或还原。

```yaml
volumes:
  - ./data:/var/lib/ycfrp
```

## 默认端口

| 端口 | 用途 | 协议 | 备注 |
| --- | --- | --- | --- |
| 38080 | YCFRP 管理面板 | HTTP | 必开 |
| 7000 | frps 与 frpc 通信端口 | TCP | 服务端模式必开 |
| 7500 | frp 内核仪表盘 | HTTP | 可选，建议仅本机访问 |
| 8080 | HTTP 类型隧道虚拟主机 | HTTP | 按需 |
| 8443 | HTTPS 类型隧道虚拟主机 | HTTPS | 按需 |
| 20000-60000 | 隧道映射的远程端口 | TCP/UDP | 按需，可在配置中调整 |

## 获取安装包

Windows 图形界面版与 Linux 静态二进制版（amd64 / arm64 / armv7）可在下载页获取，
页面同时提供完整安装步骤与端口说明。

## 升级

```bash
docker compose pull && docker compose up -d
```

升级前建议先备份 `./data` 目录。
