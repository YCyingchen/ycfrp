# 一键构建 YCFRP 发布产物：交叉编译 → 组装 dist/pkg → 写出安装包。
#
# Windows 的 .bat 脚本必须用 GBK(936) 编码，否则中文在 cmd 里会变乱码；
# 仓库里统一保存为 UTF-8，这里在组装阶段转码，避免编辑器来回切换编码。
param(
    [string]$Root = (Split-Path -Parent $PSScriptRoot)
)

$ErrorActionPreference = 'Stop'
$root = [System.IO.Path]::GetFullPath($Root)
$dist = Join-Path $root 'dist'
$pkg = Join-Path $dist 'pkg'
$packSrc = Join-Path $root 'deploy\pack'

if (-not (Test-Path -LiteralPath $dist)) { New-Item -ItemType Directory -Path $dist | Out-Null }

# ---------- 1. 交叉编译 ----------
$env:Path = "C:\Program Files\Go\bin;" + $env:Path
$env:CGO_ENABLED = '0'
$ld = '-s -w'

function Build-Target {
    param([string]$GOOS, [string]$GOARCH, [string]$GOARM, [string]$Out, [string]$Pkg, [string]$Ldflags)

    $env:GOOS = $GOOS
    $env:GOARCH = $GOARCH
    if ($GOARM) { $env:GOARM = $GOARM } else { Remove-Item Env:GOARM -ErrorAction SilentlyContinue }

    $target = Join-Path $dist $Out
    & go build -trimpath -ldflags $Ldflags -o $target $Pkg
    if ($LASTEXITCODE -ne 0) { throw "编译失败：$GOOS/$GOARCH $Pkg" }
    Write-Host ("  已编译 {0,-34} {1:N0} 字节" -f $Out, (Get-Item -LiteralPath $target).Length)
}

Write-Host '=== 1. 交叉编译 ==='
Build-Target -GOOS 'linux' -GOARCH 'amd64' -Out 'ycfrp_linux_amd64' -Pkg './cmd/ycfrp' -Ldflags $ld
Build-Target -GOOS 'linux' -GOARCH 'arm64' -Out 'ycfrp_linux_arm64' -Pkg './cmd/ycfrp' -Ldflags $ld
Build-Target -GOOS 'linux' -GOARCH 'arm' -GOARM '7' -Out 'ycfrp_linux_armv7' -Pkg './cmd/ycfrp' -Ldflags $ld
Build-Target -GOOS 'windows' -GOARCH 'amd64' -Out 'YCFRP_windows_amd64_gui.exe' -Pkg './cmd/ycfrp-gui' -Ldflags '-s -w -H windowsgui'
Build-Target -GOOS 'windows' -GOARCH 'amd64' -Out 'YCFRP_windows_amd64_console.exe' -Pkg './cmd/ycfrp' -Ldflags $ld

# ---------- 2. 组装 dist/pkg ----------
function Copy-Into {
    param([string]$Source, [string]$DestDir, [string]$NewName)
    if (-not (Test-Path -LiteralPath $DestDir)) { New-Item -ItemType Directory -Path $DestDir -Force | Out-Null }
    $name = if ($NewName) { $NewName } else { Split-Path -Leaf $Source }
    Copy-Item -LiteralPath $Source -Destination (Join-Path $DestDir $name) -Force
}

# Windows 的 bat 统一转成 GBK + CRLF：批处理按本机 ANSI 代码页解析 UTF-8 会乱码，
# 而 LF-only 换行会让 cmd 解析 if/(...) 区块出错（表现为“文件明明在却报缺失”）。
function Write-Gbk {
    param([string]$Source, [string]$Dest)
    $text = [System.IO.File]::ReadAllText($Source, [System.Text.Encoding]::UTF8)
    $text = (($text -replace "`r`n", "`n") -replace "`n", "`r`n")
    $gbk = [System.Text.Encoding]::GetEncoding(936)
    [System.IO.File]::WriteAllText($Dest, $text, $gbk)
}

Write-Host ''
Write-Host '=== 2. 组装 dist/pkg ==='

foreach ($arch in @('amd64', 'arm64', 'armv7')) {
    $dir = Join-Path $pkg "linux-$arch"
    if (Test-Path -LiteralPath $dir) { Remove-Item -LiteralPath $dir -Recurse -Force }
    New-Item -ItemType Directory -Path $dir -Force | Out-Null

    Copy-Into -Source (Join-Path $dist "ycfrp_linux_$arch") -DestDir $dir -NewName 'ycfrp'
    Copy-Into -Source (Join-Path $packSrc 'linux\start.sh') -DestDir $dir
    Copy-Into -Source (Join-Path $packSrc 'linux\stop.sh') -DestDir $dir
    Copy-Into -Source (Join-Path $packSrc 'linux\status.sh') -DestDir $dir
    Copy-Into -Source (Join-Path $packSrc 'README-linux.txt') -DestDir $dir
    Copy-Into -Source (Join-Path $packSrc 'ycfrp.service') -DestDir $dir
    Copy-Into -Source (Join-Path $packSrc 'docker-compose.yml') -DestDir $dir
    Write-Host "  已组装 linux-$arch"
}

$winDir = Join-Path $pkg 'windows'
if (Test-Path -LiteralPath $winDir) { Remove-Item -LiteralPath $winDir -Recurse -Force }
New-Item -ItemType Directory -Path $winDir -Force | Out-Null

Copy-Into -Source (Join-Path $dist 'YCFRP_windows_amd64_gui.exe') -DestDir $winDir -NewName 'YCFRP.exe'
Copy-Into -Source (Join-Path $dist 'YCFRP_windows_amd64_console.exe') -DestDir $winDir -NewName 'YCFRP-console.exe'
Copy-Into -Source (Join-Path $packSrc 'README-windows.txt') -DestDir $winDir
foreach ($bat in (Get-ChildItem -LiteralPath (Join-Path $packSrc 'windows') -Filter '*.bat' -File | Sort-Object Name)) {
    Write-Gbk -Source $bat.FullName -Dest (Join-Path $winDir $bat.Name)
}
Write-Host '  已组装 windows'
Write-Host ''
Write-Host '组装完成，接下来执行 scripts\pack.ps1 生成安装包。'
