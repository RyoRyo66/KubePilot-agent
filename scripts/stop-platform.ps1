$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
$pidFile = Join-Path $projectRoot 'data\platform-server.pid'
$binary = Join-Path $projectRoot 'bin\kubepilot-platform.exe'
if (-not (Test-Path -LiteralPath $pidFile)) { Write-Output '没有本脚本启动的平台记录。'; exit 0 }
$serverId = [int](Get-Content -LiteralPath $pidFile -Raw)
$server = Get-Process -Id $serverId -ErrorAction SilentlyContinue
if ($server) {
    if ($server.Path -ne $binary) { throw 'PID 已被其他程序使用，拒绝停止。' }
    Stop-Process -Id $serverId -Force
}
Remove-Item -LiteralPath $pidFile
Write-Output '已停止本地平台。接入配置和记录保留在 data\platform。'
