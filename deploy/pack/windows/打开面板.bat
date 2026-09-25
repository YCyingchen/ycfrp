@echo off
chcp 936 >nul
title YCFRP - 打开面板
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
echo   正在用默认浏览器打开面板……
echo.

rem 面板端口从数据目录的配置里读取，改过端口也能正确打开。
YCFRP-console.exe -open

echo   如果浏览器没有自动弹出，请手动访问 http://127.0.0.1:38080
echo.
echo   按任意键关闭本窗口。
pause >nul
