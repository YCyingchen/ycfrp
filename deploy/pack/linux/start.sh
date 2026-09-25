#!/bin/sh
# YCFRP 后台启动脚本
#
# 用法：
#   ./start.sh                     使用默认目录 /var/lib/ycfrp，端口 38080
#   ./start.sh -p 39000            指定面板端口
#   ./start.sh -c /opt/ycfrp/data  指定数据目录
#
# 脚本会调用同目录下的 ycfrp（找不到时退回 /usr/local/bin/ycfrp）。

set -eu

PORT=38080
DATA_DIR=/var/lib/ycfrp

while [ $# -gt 0 ]; do
    case "$1" in
        -p|--port) PORT="$2"; shift 2 ;;
        -c|--config) DATA_DIR="$2"; shift 2 ;;
        -h|--help)
            sed -n '2,11p' "$0" | sed 's/^# \{0,1\}//'
            exit 0
            ;;
        *) echo "未知参数：$1" >&2; exit 2 ;;
    esac
done

# 优先使用脚本同目录的主程序，其次查找 PATH 与安装目录。
SELF_DIR=$(cd "$(dirname "$0")" && pwd)
if [ -x "$SELF_DIR/ycfrp" ]; then
    BIN="$SELF_DIR/ycfrp"
elif command -v ycfrp >/dev/null 2>&1; then
    BIN=$(command -v ycfrp)
elif [ -x /usr/local/bin/ycfrp ]; then
    BIN=/usr/local/bin/ycfrp
else
    echo "未找到 ycfrp 主程序，请把它与脚本放在同一目录。" >&2
    exit 1
fi

# 非 root 用户无法写入 /var/lib 与绑定低位端口，这里给出明确提示。
if [ "$(id -u)" -ne 0 ] && [ "$DATA_DIR" = "/var/lib/ycfrp" ]; then
    echo "当前不是 root 用户，请改用可写的数据目录，例如：" >&2
    echo "  $0 -c \"\$HOME/ycfrp-data\" -p $PORT" >&2
    exit 1
fi

# 已在后台运行时不再重复启动。
if "$BIN" -c "$DATA_DIR" -status 2>/dev/null | grep -q "正在后台运行"; then
    echo "YCFRP 已经在后台运行。"
    "$BIN" -c "$DATA_DIR" -status
    exit 0
fi

echo "正在后台启动 YCFRP……"
# -d 会一直等到面板真正监听端口才返回，所以这里不用再自己轮询。
"$BIN" -d -c "$DATA_DIR" -p "$PORT"

echo
echo "============================================================"
echo "  YCFRP 已在后台运行"
echo "============================================================"
echo
echo "  面板地址：http://127.0.0.1:$PORT"
echo "  默认账号：admin / admin"
echo "  数据目录：$DATA_DIR"
echo "  日志文件：$DATA_DIR/logs"
echo
echo "  查看状态：./status.sh -c $DATA_DIR"
echo "  停止服务：./stop.sh -c $DATA_DIR"
echo
