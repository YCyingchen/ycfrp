# 生成下载页的版本清单 version.json，供面板「检查更新」读取。
#
# 面板按 version.json 里的 platforms 字段挑选当前平台的安装包，因此这里的
# 文件名必须与 sync-site.ps1 同步到 dist\site 的安装包完全一致。
param(
    [string]$Version = "s2609.031",
    [string]$Kernel = "0.71.0",
    [string]$SiteBase = "https://ycfrp.yc1.cc.cd",
    [string]$Root = (Split-Path -Parent $PSScriptRoot),
    [string]$Notes = ""
)

$ErrorActionPreference = 'Stop'
$dist = Join-Path $Root 'dist'
$site = Join-Path $dist 'site'

# 更新日志：供下载页面与面板展示。新的版本追加在数组最前面。
# kind 取值：add 新增 / fix 修复 / change 调整。
$changelogPath = Join-Path $Root 'deploy\changelog.json'
$changelog = @()
if (Test-Path -LiteralPath $changelogPath) {
    $raw = [System.IO.File]::ReadAllText($changelogPath, [System.Text.Encoding]::UTF8)
    $changelog = $raw | ConvertFrom-Json
}

$platforms = [ordered]@{
    'windows-amd64' = "ycfrp-$Version-windows-amd64.zip"
    'linux-amd64'   = "ycfrp-$Version-linux-amd64.tar.gz"
    'linux-arm64'   = "ycfrp-$Version-linux-arm64.tar.gz"
    'linux-armv7'   = "ycfrp-$Version-linux-armv7.tar.gz"
}

# 只登记真实存在的安装包，避免清单里出现 404 地址。
$urls = [ordered]@{}
$hashes = [ordered]@{}
foreach ($pk in $platforms.Keys) {
    $file = Join-Path $site $platforms[$pk]
    if (-not (Test-Path -LiteralPath $file)) {
        $file = Join-Path $dist $platforms[$pk]
    }
    if (Test-Path -LiteralPath $file) {
        $urls[$pk] = "$SiteBase/" + $platforms[$pk]
        $hashes[$pk] = (Get-FileHash -LiteralPath $file -Algorithm SHA256).Hash.ToLower()
    }
}

if ($urls.Count -eq 0) { throw "没有找到任何安装包，请先执行 make-release.ps1 / pack.ps1 / sync-site.ps1" }

$manifest = [ordered]@{
    version    = $Version
    kernel     = $Kernel
    releasedAt = (Get-Date -Format 'yyyy-MM-dd HH:mm:ss')
    notes      = $Notes
    platforms  = $urls
    sha256     = $hashes
    changelog  = $changelog
}

$json = $manifest | ConvertTo-Json -Depth 4
$out = Join-Path $site 'version.json'
# PowerShell 的 ConvertTo-Json 会单独序列化管道传入的数组，把 changelog 包成
# {"value":[...]}，与期望的数组结构不符。用 StringBuilder 手工拼 JSON 更稳妥。
$sb = [System.Text.StringBuilder]::new()
[void]$sb.AppendLine('{')
[void]$sb.AppendLine('  "version": ' + ($manifest.version | ConvertTo-Json) + ',')
[void]$sb.AppendLine('  "kernel": ' + ($manifest.kernel | ConvertTo-Json) + ',')
[void]$sb.AppendLine('  "releasedAt": ' + ($manifest.releasedAt | ConvertTo-Json) + ',')
[void]$sb.AppendLine('  "notes": ' + ($manifest.notes | ConvertTo-Json) + ',')
[void]$sb.AppendLine('  "platforms": ' + ($urls | ConvertTo-Json -Depth 3) + ',')
[void]$sb.AppendLine('  "sha256": ' + ($hashes | ConvertTo-Json -Depth 3) + ',')
# changelog 用 -AsArray 保证永远是数组，不会因单元素被压成对象。
[void]$sb.AppendLine('  "changelog": ' + (@($changelog) | ConvertTo-Json -Depth 4 -AsArray))
[void]$sb.AppendLine('}')
[System.IO.File]::WriteAllText($out, $sb.ToString(), (New-Object System.Text.UTF8Encoding($false)))

Write-Output "已生成 $out"
Write-Output "changelog 条数：$(@($changelog).Count)"
