# 打包 YCFRP 发布产物。
#
# Windows 自带的 bsdtar 不支持 --mode，打出来的 tar.gz 里所有文件都是 664，
# 用户解包后执行 ./ycfrp 会直接 Permission denied。这里改用 .NET 的
# System.Formats.Tar 显式写入 Unix 权限位，保证可执行文件是 755、配置类文件是 644。
param(
    [string]$Version = "s2609.031",
    [string]$Root = (Split-Path -Parent $PSScriptRoot),
    [string]$OutputDir = "dist"
)

$ErrorActionPreference = "Stop"
Add-Type -AssemblyName System.Formats.Tar

$dist = Join-Path $Root $OutputDir
if (-not (Test-Path $dist)) { New-Item -ItemType Directory -Path $dist | Out-Null }

# 每个打包目录里各文件的权限位。
$mode755 = [System.IO.UnixFileMode]::UserRead -bor [System.IO.UnixFileMode]::UserWrite -bor [System.IO.UnixFileMode]::UserExecute -bor
           [System.IO.UnixFileMode]::GroupRead -bor [System.IO.UnixFileMode]::GroupExecute -bor
           [System.IO.UnixFileMode]::OtherRead -bor [System.IO.UnixFileMode]::OtherExecute   # 755
$mode644 = [System.IO.UnixFileMode]::UserRead -bor [System.IO.UnixFileMode]::UserWrite -bor [System.IO.UnixFileMode]::GroupRead -bor [System.IO.UnixFileMode]::OtherRead   # 644

$modes = @{
    "ycfrp"             = $mode755
    "start.sh"          = $mode755
    "stop.sh"           = $mode755
    "status.sh"         = $mode755
    "YCFRP.exe"         = [System.IO.UnixFileMode]::UserRead -bor [System.IO.UnixFileMode]::UserWrite
    "YCFRP-console.exe" = [System.IO.UnixFileMode]::UserRead -bor [System.IO.UnixFileMode]::UserWrite
    "ycfrp.service"     = $mode644
    "docker-compose.yml" = $mode644
    "README-linux.txt"  = $mode644
    "README-windows.txt" = [System.IO.UnixFileMode]::UserRead -bor [System.IO.UnixFileMode]::UserWrite
}

function New-TarGz {
    param([string]$SourceDir, [string]$Target)

    $tmpTar = [System.IO.Path]::ChangeExtension([System.IO.Path]::GetTempFileName(), ".tar")
    try {
        $fs = [System.IO.File]::Create($tmpTar)
        try {
            $tw = [System.Formats.Tar.TarWriter]::new($fs, $false)
            try {
                foreach ($file in (Get-ChildItem -LiteralPath $SourceDir -File | Sort-Object Name)) {
                    $entry = [System.Formats.Tar.PaxTarEntry]::new([System.Formats.Tar.TarEntryType]::RegularFile, $file.Name)
                    $mode = $modes[$file.Name]
                    # 未登记的文件默认 644；shell 脚本一律可执行，避免漏登记后无法运行。
                    if (-not $mode) { $mode = if ($file.Extension -eq '.sh') { $mode755 } else { $mode644 } }
                    $entry.Mode = $mode
                    $entry.Uid = 0
                    $entry.Gid = 0
                    $entry.UserName = "root"
                    $entry.GroupName = "root"
                    $entry.ModificationTime = $file.LastWriteTime
                    $entry.DataStream = [System.IO.File]::OpenRead($file.FullName)
                    try { $tw.WriteEntry($entry) } finally { $entry.DataStream.Dispose() }
                }
            } finally { $tw.Dispose() }
        } finally { $fs.Dispose() }

        # gzip 压缩；.NET 的 GZipStream 不写入原始文件名，满足可复现打包。
        $in = [System.IO.File]::OpenRead($tmpTar)
        try {
            $out = [System.IO.File]::Create($Target)
            try {
                $gz = [System.IO.Compression.GZipStream]::new($out, [System.IO.Compression.CompressionLevel]::Optimal)
                try { $in.CopyTo($gz) } finally { $gz.Dispose() }
            } finally { $out.Dispose() }
        } finally { $in.Dispose() }
    } finally {
        if (Test-Path $tmpTar) { Remove-Item -LiteralPath $tmpTar -Force }
    }
}

function New-Zip {
    param([string]$SourceDir, [string]$Target)

    if (Test-Path $Target) { Remove-Item -LiteralPath $Target -Force }
    $zip = [System.IO.Compression.ZipFile]::Open($Target, [System.IO.Compression.ZipArchiveMode]::Create)
    try {
        foreach ($file in (Get-ChildItem -LiteralPath $SourceDir -File | Sort-Object Name)) {
            [System.IO.Compression.ZipFileExtensions]::CreateEntryFromFile($zip, $file.FullName, $file.Name) | Out-Null
        }
    } finally { $zip.Dispose() }
}

foreach ($arch in @("amd64", "arm64", "armv7")) {
    $src = Join-Path $dist "pkg\linux-$arch"
    if (-not (Test-Path $src)) { throw "缺少打包目录：$src" }
    $target = Join-Path $dist "ycfrp-$Version-linux-$arch.tar.gz"
    New-TarGz -SourceDir $src -Target $target
    Write-Host ("已生成 {0}（{1:N0} 字节）" -f (Split-Path -Leaf $target), (Get-Item $target).Length)
}

$winSrc = Join-Path $dist "pkg\windows"
if (-not (Test-Path $winSrc)) { throw "缺少打包目录：$winSrc" }
$winTarget = Join-Path $dist "ycfrp-$Version-windows-amd64.zip"
New-Zip -SourceDir $winSrc -Target $winTarget
Write-Host ("已生成 {0}（{1:N0} 字节）" -f (Split-Path -Leaf $winTarget), (Get-Item $winTarget).Length)
