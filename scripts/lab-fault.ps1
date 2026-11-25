param(
    [Parameter(Mandatory=$true)][ValidateSet('image','selector','probe','crash','oom','pending','restore')][string]$Fault,
    [string]$Context = 'kind-kubepilot-lab'
)
$ErrorActionPreference = 'Stop'
if ($Context -ne 'kind-kubepilot-lab') { throw '此脚本只作用于 kind-kubepilot-lab 实验集群。' }
$projectRoot = Split-Path -Parent $PSScriptRoot
$arguments = @('--context', $Context, '--namespace', 'kube-agent-lab')
switch ($Fault) {
    'image' { & kubectl @arguments set image deployment/demo-web web=nginx:kubepilot-nonexistent-tag }
    'selector' { & kubectl @arguments patch service/demo-web --type=json --patch-file (Join-Path $projectRoot 'experiments\faults\selector.json') }
    'probe' { & kubectl @arguments patch deployment/demo-web --type=json --patch-file (Join-Path $projectRoot 'experiments\faults\probe.json') }
    'restore' {
        & kubectl @arguments apply -f (Join-Path $projectRoot 'deploy\lab.yaml')
        if ($LASTEXITCODE -ne 0) { throw '恢复实验应用失败' }
        & kubectl @arguments delete deployment/fault-worker --ignore-not-found=true
    }
    default { & kubectl @arguments apply -f (Join-Path $projectRoot ("experiments\faults\$Fault.yaml")) }
}
if ($LASTEXITCODE -ne 0) { throw '故障操作失败' }
Write-Output "已执行 $Fault；context=$Context；namespace=kube-agent-lab"
Write-Output '请通过事件、应用访问及排障界面验证实际结果。'
