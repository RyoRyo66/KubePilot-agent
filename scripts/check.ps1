$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
Set-Location -LiteralPath $projectRoot
$goCommand = Get-Command go -ErrorAction SilentlyContinue
$goBinary = if ($goCommand) { $goCommand.Source } else { Join-Path (Split-Path -Parent $projectRoot) '.work\dev-tools\go\bin\go.exe' }
$env:GOPATH = Join-Path (Split-Path -Parent $projectRoot) '.work\dev-tools\gopath'
$env:GOCACHE = Join-Path (Split-Path -Parent $projectRoot) '.work\dev-tools\gocache'
if (-not $env:GOPROXY) { $env:GOPROXY = 'https://goproxy.cn,https://proxy.golang.org,direct' }
& $goBinary test ./... -count=1 -cover
if ($LASTEXITCODE -ne 0) { throw 'Go 测试未通过' }
& $goBinary vet ./...
if ($LASTEXITCODE -ne 0) { throw 'Go 静态检查未通过' }
Push-Location -LiteralPath (Join-Path $projectRoot 'web')
try { & npm.cmd run build; if ($LASTEXITCODE -ne 0) { throw '前端类型检查或构建失败' } } finally { Pop-Location }
