param([int]$Port = 18080, [switch]$SkipBuild)
$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
Set-Location -LiteralPath $projectRoot
$goCommand = Get-Command go -ErrorAction SilentlyContinue
$goBinary = if ($goCommand) { $goCommand.Source } else { Join-Path (Split-Path -Parent $projectRoot) '.work\dev-tools\go\bin\go.exe' }
if (-not (Test-Path -LiteralPath $goBinary)) { throw '需要 Go 1.24 或更高版本，请安装后重新运行。' }
$env:GOPATH = Join-Path (Split-Path -Parent $projectRoot) '.work\dev-tools\gopath'
$env:GOCACHE = Join-Path (Split-Path -Parent $projectRoot) '.work\dev-tools\gocache'
if (-not $env:GOPROXY) { $env:GOPROXY = 'https://goproxy.cn,https://proxy.golang.org,direct' }
$env:npm_config_cache = Join-Path (Split-Path -Parent $projectRoot) '.work\dev-tools\npm-cache'
New-Item -ItemType Directory -Path 'bin','data' -Force | Out-Null
$binary = Join-Path $projectRoot 'bin\kubepilot.exe'
$pidFile = Join-Path $projectRoot 'data\dev-server.pid'
if (Test-Path -LiteralPath $pidFile) {
    $serverId = [int](Get-Content -LiteralPath $pidFile -Raw)
    $existing = Get-Process -Id $serverId -ErrorAction SilentlyContinue
    if ($existing -and $existing.Path -eq $binary) { Write-Output "服务已运行：http://127.0.0.1:$Port（PID $serverId）"; exit 0 }
}
if (-not $SkipBuild) {
    Push-Location -LiteralPath (Join-Path $projectRoot 'web')
    try {
        if (-not (Test-Path -LiteralPath 'node_modules')) { & npm.cmd ci --no-audit --no-fund; if ($LASTEXITCODE -ne 0) { throw '前端依赖安装失败' } }
        & npm.cmd run build
        if ($LASTEXITCODE -ne 0) { throw '前端构建失败' }
    } finally { Pop-Location }
    & $goBinary build -o $binary ./cmd/kubepilot
    if ($LASTEXITCODE -ne 0) { throw '后端构建失败' }
}
if (-not (Test-Path -LiteralPath $binary)) { throw '缺少可执行文件，请先构建' }
if (-not $env:AGENT_MODE) { $env:AGENT_MODE = 'demo' }
$env:AGENT_ADDR = "127.0.0.1:$Port"
$serverProcess = Start-Process -FilePath $binary -WorkingDirectory $projectRoot -WindowStyle Hidden -RedirectStandardOutput (Join-Path $projectRoot 'data\server.stdout.log') -RedirectStandardError (Join-Path $projectRoot 'data\server.stderr.log') -PassThru
$serverProcess.Id | Set-Content -LiteralPath $pidFile
$healthHeaders = @{}
if ($env:AGENT_API_TOKEN) { $healthHeaders['Authorization'] = 'Bearer ' + $env:AGENT_API_TOKEN }
$healthy = $false
for ($attempt = 0; $attempt -lt 25; $attempt++) {
    $serverProcess.Refresh()
    if ($serverProcess.HasExited) { throw '服务启动失败，请查看 data\server.stderr.log' }
    try {
        $health = Invoke-RestMethod -Uri "http://127.0.0.1:$Port/api/health" -Headers $healthHeaders -TimeoutSec 2
        if ($health.status -eq 'ok' -and $health.version -eq '0.1.0') { $healthy = $true; break }
    } catch { }
    Start-Sleep -Milliseconds 200
}
if (-not $healthy) { throw '服务健康检查尚未通过，请查看日志；可使用 stop.ps1 停止。' }
Write-Output "KubePilot 已启动：http://127.0.0.1:$Port"
Write-Output "环境：$env:AGENT_MODE；日志：data\server.stderr.log"
Write-Output '停止命令：.\scripts\stop.ps1'
