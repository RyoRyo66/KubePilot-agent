param([string]$LabRoot = 'D:\KubePilotLab')
$ErrorActionPreference = 'Stop'
& (Join-Path $PSScriptRoot 'start-lab.ps1') -LabRoot $LabRoot
& (Join-Path $PSScriptRoot 'start-platform.ps1') -SkipBuild
& (Join-Path $PSScriptRoot 'start-monitoring-ports.ps1') -LabRoot $LabRoot
Write-Output 'Platform: http://127.0.0.1:18081'
Write-Output 'Monitoring and Connector use their existing Kubernetes deployments and persistent volumes.'
