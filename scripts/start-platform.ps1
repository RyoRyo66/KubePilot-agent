param([int]$Port = 18081, [switch]$SkipBuild)
$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
Set-Location -LiteralPath $projectRoot
$binary = Join-Path $projectRoot 'bin\kubepilot-platform.exe'
$pidFile = Join-Path $projectRoot 'data\platform-server.pid'
$goCommand = Get-Command go -ErrorAction SilentlyContinue
$goBinary = if ($goCommand) { $goCommand.Source } else { Join-Path (Split-Path -Parent $projectRoot) '.work\dev-tools\go\bin\go.exe' }
New-Item -ItemType Directory -Path 'bin','data' -Force | Out-Null
if (Test-Path -LiteralPath $pidFile) {
    $existingId = [int](Get-Content -LiteralPath $pidFile -Raw)
    $existing = Get-Process -Id $existingId -ErrorAction SilentlyContinue
    if ($existing -and $existing.Path -eq $binary) {
        $reply = Invoke-RestMethod -Uri "http://127.0.0.1:$Port/api/platform/ping" -TimeoutSec 3
        if ($reply.product -eq 'kubepilot-platform') { Write-Output "平台已运行：http://127.0.0.1:$Port"; exit 0 }
        throw '已有平台进程，但指定端口未通过检查。请先确认实际端口。'
    }
}
if (-not $SkipBuild) {
    if (-not (Test-Path -LiteralPath $goBinary)) { throw '需要 Go 1.24 或更高版本' }
    $env:GOPATH = Join-Path (Split-Path -Parent $projectRoot) '.work\dev-tools\gopath'
    $env:GOCACHE = Join-Path (Split-Path -Parent $projectRoot) '.work\dev-tools\gocache'
    Push-Location -LiteralPath (Join-Path $projectRoot 'web')
    try {
        if (-not (Test-Path -LiteralPath 'node_modules')) { & npm.cmd ci --no-audit --no-fund; if ($LASTEXITCODE -ne 0) { throw '前端依赖安装失败' } }
        & npm.cmd run build
        if ($LASTEXITCODE -ne 0) { throw '前端构建失败' }
    } finally { Pop-Location }
    & $goBinary build -o $binary ./cmd/kubepilot-platform
    if ($LASTEXITCODE -ne 0) { throw '平台构建失败' }
    & $goBinary build -o (Join-Path $projectRoot 'bin\kubepilot-connector.exe') ./cmd/kubepilot-connector
    if ($LASTEXITCODE -ne 0) { throw 'Connector 构建失败' }
}
if (-not (Test-Path -LiteralPath $binary)) { throw '请先构建平台' }
$env:PLATFORM_ADDR = "127.0.0.1:$Port"
if (-not $env:PLATFORM_DATA_DIR) { $env:PLATFORM_DATA_DIR = Join-Path $projectRoot 'data\platform' }
$env:PLATFORM_WEB_DIR = Join-Path $projectRoot 'web\dist'
$server = Start-Process -FilePath $binary -WorkingDirectory $projectRoot -WindowStyle Hidden -RedirectStandardOutput (Join-Path $projectRoot 'data\platform.stdout.log') -RedirectStandardError (Join-Path $projectRoot 'data\platform.stderr.log') -PassThru
$server.Id | Set-Content -LiteralPath $pidFile
$healthy = $false
for ($attempt = 0; $attempt -lt 30; $attempt++) {
    $server.Refresh()
    if ($server.HasExited) { throw '平台启动失败，请查看 data\platform.stderr.log' }
    try {
        $reply = Invoke-RestMethod -Uri "http://127.0.0.1:$Port/api/platform/ping" -TimeoutSec 2
        if ($reply.product -eq 'kubepilot-platform') { $healthy = $true; break }
    } catch { }
    Start-Sleep -Milliseconds 200
}
if (-not $healthy) { throw '平台未通过启动检查，请查看日志' }
Write-Output "B/S 平台已启动：http://127.0.0.1:$Port"
Write-Output '默认仅供本机预览；集群接入部署方式见 docs\PLATFORM.md。'
Write-Output '停止：.\scripts\stop-platform.ps1'
