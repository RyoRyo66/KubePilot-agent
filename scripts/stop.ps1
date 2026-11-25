$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
$pidFile = Join-Path $projectRoot 'data\dev-server.pid'
if (-not (Test-Path -LiteralPath $pidFile)) { Write-Output '没有当前项目的运行记录。'; exit 0 }
$serverId = [int](Get-Content -LiteralPath $pidFile -Raw)
$serverProcess = Get-Process -Id $serverId -ErrorAction SilentlyContinue
$expected = Join-Path $projectRoot 'bin\kubepilot.exe'
if ($serverProcess -and $serverProcess.Path -eq $expected) { Stop-Process -Id $serverId; Write-Output 'KubePilot 已停止。' }
elseif ($serverProcess) { throw 'PID 对应的程序不是本项目，已拒绝停止。' }
Remove-Item -LiteralPath $pidFile
