@echo off
chcp 936 >nul
title YCFRP - 查看状态
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
YCFRP-console.exe -status
echo.
echo   按任意键关闭本窗口。
pause >nul
