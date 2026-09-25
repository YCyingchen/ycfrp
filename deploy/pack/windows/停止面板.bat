@echo off
chcp 936 >nul
title YCFRP - 停止面板
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
echo   正在停止 YCFRP……
echo.

YCFRP-console.exe -stop

echo.
echo   按任意键关闭本窗口。
pause >nul
