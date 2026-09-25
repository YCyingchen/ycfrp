#!/usr/bin/env python3
"""用飞牛官方 fnpack 工具打包 YCFRP 的 fpk 应用包。

用法：
  python scripts/build-fpk.py <linux-amd64二进制> <输出.fpk> [版本号] [fnpack路径]

流程：用 fnpack create 生成标准项目 → 填入 manifest/脚本 → 放二进制 → fnpack build。
"""
import os, sys, shutil, subprocess, tempfile

FNPACK_DEFAULT = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "tools", "fnpack.exe")

MANIFEST_TMPL = """appname      = ycfrp
version      = {version}
display_name = YCFRP
desc         = FRP 双端管理面板，内置 frps / frpc 内核，隧道管理、流量监控、中文日志诊断与多渠道通知。
platform     = {platform}
source       = thirdparty
maintainer   = YCyingchen
maintainer_url = https://ycfrp.yc1.cc.cd
distributor  = YCyingchen
distributor_url = https://ycfrp.yc1.cc.cd
os_min_version = 1.2.0401
service_port = 38080
checkport    = false
ctl_stop     = true
desktop_uidir = ui
desktop_applaunchname = ycfrp.main
"""

# 桌面入口配置：iframe 内嵌打开 YCFRP 面板（38080 端口）
UI_CONFIG = """{
  ".url": {
    "ycfrp.main": {
      "title": "YCFRP 面板",
      "icon": "images/icon_{0}.png",
      "type": "iframe",
      "protocol": "http",
      "port": "38080",
      "url": "/",
      "allUsers": true
    }
  }
}
"""

CMD_MAIN = """#!/bin/bash

LOG_FILE="${TRIM_PKGVAR}/info.log"
PID_FILE="${TRIM_PKGVAR}/app.pid"

CMD="${TRIM_APPDEST}/ycfrp -c ${TRIM_PKGVAR}/data -p 38080"

log_msg() {
    echo "$(date '+%Y-%m-%d %H:%M:%S') - $1" >> ${LOG_FILE}
}

start_process() {
    if status; then
        return 0
    fi
    mkdir -p "${TRIM_PKGVAR}/data" >/dev/null 2>&1 || true
    log_msg "Starting YCFRP ..."
    setsid bash -c "${CMD}" >> ${LOG_FILE} 2>&1 &
    printf "%s" "$!" > ${PID_FILE}
    log_msg "started pid=$!"
    return 0
}

stop_process() {
    log_msg "Stopping YCFRP ..."
    if [ -r "${PID_FILE}" ]; then
        pid=$(head -n 1 "${PID_FILE}" | tr -d '[:space:]')
        log_msg "pid=${pid}"
        if ! check_process "${pid}"; then
            rm -f "${PID_FILE}"
            return
        fi
        kill -TERM ${pid} >> ${LOG_FILE} 2>&1
        local count=0
        while check_process "${pid}" && [ $count -lt 10 ]; do
            sleep 1
            count=$((count + 1))
        done
        if check_process "${pid}"; then
            kill -KILL "${pid}"
            sleep 1
            rm -f "${PID_FILE}"
        else
            log_msg "process killed"
            rm -f "${PID_FILE}"
        fi
    fi
    return 0
}

check_process() {
    local pid=$1
    if kill -0 "${pid}" 2>/dev/null; then
        return 0
    else
        return 1
    fi
}

status() {
    if [ -f "${PID_FILE}" ]; then
        pid=$(head -n 1 "${PID_FILE}" | tr -d '[:space:]')
        if check_process "${pid}"; then
            return 0
        else
            rm -f "${PID_FILE}"
        fi
    fi
    return 1
}

case $1 in
start)
    start_process
    ;;
stop)
    stop_process
    ;;
status)
    if status; then
        exit 0
    else
        exit 3
    fi
    ;;
*)
    exit 1
    ;;
esac
"""

PRIVILEGE = '{\n    "defaults":\n    {\n        "run-as": "root"\n    }\n}\n'


def main():
    if len(sys.argv) < 3:
        print(__doc__)
        sys.exit(1)
    bin_path = sys.argv[1]
    out_path = sys.argv[2]
    version = sys.argv[3] if len(sys.argv) > 3 else "0.30.0"
    fnpack = sys.argv[4] if len(sys.argv) > 4 else FNPACK_DEFAULT
    platform = sys.argv[5] if len(sys.argv) > 5 else "x86"  # x86 或 arm

    workdir = tempfile.mkdtemp(prefix="ycfrp-fpk-")
    # fnpack create 在当前目录创建 <appname> 子目录
    r = subprocess.run([fnpack, "create", "ycfrp", "--without-ui", "true"],
                       cwd=workdir, capture_output=True, text=True, encoding="utf-8", errors="ignore")
    if r.returncode != 0:
        print("fnpack create 失败:", r.stderr or r.stdout)
        sys.exit(1)

    proj = os.path.join(workdir, "ycfrp")
    if not os.path.isdir(proj):
        print("未找到生成的项目目录")
        sys.exit(1)

    # 写 manifest
    with open(os.path.join(proj, "manifest"), "w", encoding="utf-8", newline="\n") as f:
        f.write(MANIFEST_TMPL.format(version=version, platform=platform))

    # 写 cmd/main
    with open(os.path.join(proj, "cmd", "main"), "w", encoding="utf-8", newline="\n") as f:
        f.write(CMD_MAIN)

    # 写 privilege
    with open(os.path.join(proj, "config", "privilege"), "w", encoding="utf-8", newline="\n") as f:
        f.write(PRIVILEGE)

    # 放二进制
    shutil.copy2(bin_path, os.path.join(proj, "app", "ycfrp"))

    # 桌面 UI 入口：app/ui/config + images 图标
    ui_dir = os.path.join(proj, "app", "ui")
    os.makedirs(os.path.join(ui_dir, "images"), exist_ok=True)
    with open(os.path.join(ui_dir, "config"), "w", encoding="utf-8", newline="\n") as f:
        f.write(UI_CONFIG)
    # 生成 64/256 尺寸图标到 ui/images
    from PIL import Image, ImageDraw
    for size, name in ((64, "icon_64.png"), (256, "icon_256.png")):
        img = Image.new("RGBA", (size, size), (0, 0, 0, 0))
        d = ImageDraw.Draw(img)
        for y in range(size):
            t = y / size
            d.line([(0, y), (size, y)], fill=(int(0x4f + 0x2d * t), int(0x8c - 0x30 * t), 0xff, 255))
        mask = Image.new("L", (size, size), 0)
        ImageDraw.Draw(mask).rounded_rectangle([0, 0, size - 1, size - 1], radius=size // 5, fill=255)
        img.putalpha(mask)
        try:
            from PIL import ImageFont
            font = ImageFont.truetype("C:/Windows/Fonts/segoeuib.ttf", int(size * 0.38))
        except Exception:
            font = ImageFont.load_default()
        d.text((size * 0.22, size * 0.22), "YC", font=font, fill=(255, 255, 255, 255))
        img.save(os.path.join(ui_dir, "images", name))

    # 清理 .DS_Store
    for p in (os.path.join(proj, "cmd", ".DS_Store"), os.path.join(proj, "config", ".DS_Store")):
        if os.path.exists(p):
            os.remove(p)

    # fnpack build
    r = subprocess.run([fnpack, "build"], cwd=proj, capture_output=True, text=True, encoding="utf-8", errors="ignore")
    if r.returncode != 0:
        print("fnpack build 失败:", r.stderr or r.stdout)
        sys.exit(1)

    src = os.path.join(proj, "ycfrp.fpk")
    if not os.path.exists(src):
        print("未找到 ycfrp.fpk 产物")
        sys.exit(1)

    shutil.copy2(src, out_path)
    shutil.rmtree(workdir, ignore_errors=True)
    print(f"fpk done: {out_path} ({os.path.getsize(out_path)} bytes)")


if __name__ == "__main__":
    main()
