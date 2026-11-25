param([int]$Port=18082,[int]$BackendPort=18086,[switch]$Rebuild)
$ErrorActionPreference='Stop'
$projectDir=Split-Path -Parent $PSScriptRoot
Set-Location -LiteralPath $projectDir
$runtimeDir=Join-Path $projectDir '.work\flow-demo'
$activeFile=Join-Path $runtimeDir 'active.json'
New-Item -ItemType Directory -Path $runtimeDir -Force | Out-Null
if(Test-Path -LiteralPath $activeFile){
    $active=Get-Content -LiteralPath $activeFile -Raw | ConvertFrom-Json
    if(-not $active.stopped -and $active.base -match '^http://localhost:\d+$'){
        $health=$null
        try{$publicUri=[uri]$active.base;$health=Invoke-RestMethod -Uri ("http://127.0.0.1:"+$publicUri.Port+'/api/demo/health') -Headers @{Host=$publicUri.Authority} -TimeoutSec 3}catch{}
        if($health.product -eq 'kubepilot-flow-demo' -and $health.instance -eq $active.instance){
            Write-Output "模拟环境已运行：$($active.base)"
            Write-Output '点击“进入演示工作空间”，再点击“注入故障并发送告警”。'
            exit 0
        }
    }
}
$nodeCommand=Get-Command node -ErrorAction SilentlyContinue
if(-not $nodeCommand){throw '需要 Node.js 24 或更高版本'}
$binaryDir=Join-Path $projectDir 'bin\demo'
$platformBinary=Join-Path $binaryDir 'kubepilot-platform.exe'
$connectorBinary=Join-Path $binaryDir 'kubepilot-connector.exe'
if($Rebuild -or -not(Test-Path -LiteralPath $platformBinary) -or -not(Test-Path -LiteralPath $connectorBinary)){
    $goCommand=Get-Command go -ErrorAction SilentlyContinue
    $goBinary=if($goCommand){$goCommand.Source}else{Join-Path (Split-Path -Parent $projectDir) '.work\dev-tools\go\bin\go.exe'}
    if(-not(Test-Path -LiteralPath $goBinary)){throw '需要 Go 1.24 或更高版本'}
    $env:GOPATH=Join-Path (Split-Path -Parent $projectDir) '.work\dev-tools\gopath'
    $env:GOCACHE=Join-Path (Split-Path -Parent $projectDir) '.work\dev-tools\gocache'
    New-Item -ItemType Directory -Path $binaryDir -Force | Out-Null
    & $goBinary build -o $platformBinary ./cmd/kubepilot-platform
    if($LASTEXITCODE -ne 0){throw '模拟平台构建失败'}
    & $goBinary build -o $connectorBinary ./cmd/kubepilot-connector
    if($LASTEXITCODE -ne 0){throw '模拟执行端构建失败'}
}
if($Rebuild -or -not(Test-Path -LiteralPath 'web\dist\index.html')){
    Push-Location -LiteralPath (Join-Path $projectDir 'web')
    try{
        if(-not(Test-Path -LiteralPath 'node_modules')){& npm.cmd ci --no-audit --no-fund;if($LASTEXITCODE -ne 0){throw '前端依赖安装失败'}}
        & npm.cmd run build
        if($LASTEXITCODE -ne 0){throw '前端构建失败'}
    }finally{Pop-Location}
}
$env:FLOW_DEMO_PORT=[string]$Port
$env:FLOW_DEMO_BACKEND_PORT=[string]$BackendPort
$env:FLOW_DEMO_BIN_DIR=$binaryDir
$env:FLOW_DEMO_ACTIVE_FILE=$activeFile
$runnerScript=Join-Path $projectDir 'web\scripts\flow-demo.mjs'
$runner=Start-Process -FilePath $nodeCommand.Source -ArgumentList @('"'+$runnerScript+'"') -WorkingDirectory $projectDir -WindowStyle Hidden -RedirectStandardOutput (Join-Path $runtimeDir 'runner.stdout.log') -RedirectStandardError (Join-Path $runtimeDir 'runner.stderr.log') -PassThru
$ready=$false
for($attempt=0;$attempt -lt 160;$attempt++){
    $runner.Refresh()
    if($runner.HasExited){throw '模拟环境启动失败，请查看 .work\flow-demo\runner.stderr.log'}
    try{
        $health=Invoke-RestMethod -Uri "http://127.0.0.1:$Port/api/demo/health" -Headers @{Host="localhost:$Port"} -TimeoutSec 3
        if($health.ready -and (Test-Path -LiteralPath $activeFile)){
            $active=Get-Content -LiteralPath $activeFile -Raw | ConvertFrom-Json
            if($health.instance -eq $active.instance){$ready=$true;break}
        }
    }catch{}
    Start-Sleep -Milliseconds 250
}
if(-not $ready){throw '模拟环境未完成启动，请查看 .work\flow-demo\runner.stderr.log'}
Write-Output "全链路模拟：http://localhost:$Port"
Write-Output '进入演示工作空间 → 注入故障并发送告警。无需账号、API Key 或 kubeconfig。'
Write-Output '集群与模型均为模拟；原有 18081 工作空间独立保留。'
Write-Output '停止：.\scripts\stop-flow-demo.ps1'
