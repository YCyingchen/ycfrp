#!/bin/sh
# YCFRP 状态查询脚本。参数与 start.sh 一致。

set -eu

DATA_DIR=/var/lib/ycfrp

while [ $# -gt 0 ]; do
    case "$1" in
        -c|--config) DATA_DIR="$2"; shift 2 ;;
        -p|--port) shift 2 ;;
        -h|--help)
            sed -n '2,3p' "$0" | sed 's/^# \{0,1\}//'
            exit 0
            ;;
        *) echo "未知参数：$1" >&2; exit 2 ;;
    esac
done

SELF_DIR=$(cd "$(dirname "$0")" && pwd)
if [ -x "$SELF_DIR/ycfrp" ]; then
    BIN="$SELF_DIR/ycfrp"
elif command -v ycfrp >/dev/null 2>&1; then
    BIN=$(command -v ycfrp)
elif [ -x /usr/local/bin/ycfrp ]; then
    BIN=/usr/local/bin/ycfrp
else
    echo "未找到 ycfrp 主程序。" >&2
    exit 1
fi

"$BIN" -status -c "$DATA_DIR"
