# 把安装包与说明文件同步到静态下载页目录 dist\site，并打印清单与哈希。
#
# 用户用 Lucky 直接把 dist\site 当静态站点根目录，因此下载页引用的每个文件
# 都必须真实存在于该目录，否则会出现 404。
param(
    [string]$Version = "s2609.031",
    [string]$Root = (Split-Path -Parent $PSScriptRoot)
)

$ErrorActionPreference = 'Stop'
$dist = Join-Path $Root 'dist'
$site = Join-Path $dist 'site'
$pack = Join-Path $Root 'deploy\pack'

if (-not (Test-Path -LiteralPath $site)) { New-Item -ItemType Directory -Path $site -Force | Out-Null }

# 安装包直接放在下载页同目录，与 index.html 里的 href 保持一致。
$artifacts = @(
    "ycfrp-$Version-windows-amd64.zip",
    "ycfrp-$Version-linux-amd64.tar.gz",
    "ycfrp-$Version-linux-arm64.tar.gz",
    "ycfrp-$Version-linux-armv7.tar.gz"
)
foreach ($name in $artifacts) {
    $src = Join-Path $dist $name
    if (-not (Test-Path -LiteralPath $src)) { throw "缺少安装包：$src（请先执行 make-release.ps1 与 pack.ps1）" }
    Copy-Item -LiteralPath $src -Destination (Join-Path $site $name) -Force
}

# 说明文件与 compose 从打包源目录同步，保证与安装包内保持一致。
foreach ($name in @('README-windows.txt', 'README-linux.txt', 'docker-compose.yml')) {
    Copy-Item -LiteralPath (Join-Path $pack $name) -Destination (Join-Path $site $name) -Force
}

Write-Output '=== dist\site 清单 ==='
Get-ChildItem -LiteralPath $site -File | Sort-Object Name | ForEach-Object {
    $h = (Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash.ToLower()
    Write-Output ('{0,-40} {1,12:N0}  {2}' -f $_.Name, $_.Length, $h)
}
