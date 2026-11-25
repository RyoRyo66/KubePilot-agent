$ErrorActionPreference='Stop'
$projectDir=Split-Path -Parent $PSScriptRoot
$activeFile=Join-Path $projectDir '.work\flow-demo\active.json'
if(-not(Test-Path -LiteralPath $activeFile)){Write-Output '没有模拟环境运行记录。';exit 0}
$active=Get-Content -LiteralPath $activeFile -Raw | ConvertFrom-Json
if($active.stopped){Write-Output '模拟环境已停止。';exit 0}
if($active.base -notmatch '^http://localhost:\d+$'){throw '模拟地址无效，拒绝操作'}
$publicUri=[uri]$active.base
$controlBase='http://127.0.0.1:'+$publicUri.Port
$health=Invoke-RestMethod -Uri ($controlBase+'/api/demo/health') -Headers @{Host=$publicUri.Authority} -TimeoutSec 3
if($health.product -ne 'kubepilot-flow-demo' -or $health.instance -ne $active.instance){throw '端口属于其他实例，拒绝停止'}
$headers=@{Authorization='Bearer '+$active.control;'X-Requested-With'='KubePilot';Host=$publicUri.Authority}
$null=Invoke-RestMethod -Method Post -Uri ($controlBase+'/api/demo/shutdown') -Headers $headers -ContentType 'application/json' -Body '{}' -TimeoutSec 5
for($i=0;$i -lt 80;$i++){
    $latest=Get-Content -LiteralPath $activeFile -Raw | ConvertFrom-Json
    if($latest.stopped -and $latest.instance -eq $active.instance){Write-Output '模拟环境及所属执行端已停止，演示报告已保留。';exit 0}
    Start-Sleep -Milliseconds 200
}
throw '停止尚未完成，请查看模拟环境日志；未操作其他服务进程'
