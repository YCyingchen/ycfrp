@echo off
chcp 936 >nul
title YCFRP - 后台启动
cd /d "%~dp0"

if not exist "YCFRP-console.exe" (
    echo.
    echo   [错误] 当前目录缺少 YCFRP-console.exe
    echo          请先把压缩包完整解压，再运行本脚本。
    echo.
    pause
    exit /b 1
)

echo.
echo   正在后台启动 YCFRP，请稍候……
echo.

rem -d 表示后台运行：进程会脱离本窗口，关掉窗口也不会停止面板。
rem 启动命令会一直等到面板真正监听端口才返回，因此无需再额外等待。
YCFRP-console.exe -d -p 38080

echo.
echo   ============================================================
echo     YCFRP 已在后台运行
echo   ============================================================
echo.
echo     面板地址：http://127.0.0.1:38080
echo     默认账号：admin / admin
echo.
echo     关闭本窗口不影响面板运行。
echo     打开面板：双击「打开面板.bat」
echo     查看状态：双击「查看状态.bat」
echo     停止面板：双击「停止面板.bat」
echo.

start "" "http://127.0.0.1:38080"

echo   已在浏览器中打开面板。
echo   按任意键关闭本窗口。
pause >nul
