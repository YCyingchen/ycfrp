# 用本机 Edge 无头模式把静态下载页渲染成整页截图，便于核对排版
param(
    [string]$Html = '',
    [string]$Out = "$env:TEMP\ycfrp-download-preview.png",
    [int]$Width = 1440,
    [int]$Height = 2600
)

$Root = Split-Path -Parent $PSScriptRoot
if (-not $Html) { $Html = Join-Path $Root 'dist/site/index.html' }

$edge = 'C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe'
if (-not (Test-Path -LiteralPath $edge)) {
    throw "未找到 Edge：$edge"
}

if (Test-Path -LiteralPath $Out) { Remove-Item -LiteralPath $Out -Force }

$profileDir = Join-Path $env:TEMP 'ycfrp-edge-profile'
$url = ([System.Uri]([System.IO.Path]::GetFullPath($Html))).AbsoluteUri

& $edge --headless=new --disable-gpu --hide-scrollbars --no-first-run `
    --user-data-dir="$profileDir" `
    --screenshot="$Out" --window-size="$Width,$Height" $url 2>$null | Out-Null

Start-Sleep -Seconds 2
if (Test-Path -LiteralPath $Out) {
    Get-Item -LiteralPath $Out | Select-Object Length, FullName | Format-List
}
else {
    Write-Host '截图失败'
}
