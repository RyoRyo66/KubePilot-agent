param([string]$LabRoot = 'D:\KubePilotLab')
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'lab-env.ps1') -LabRoot $LabRoot
$dockerCommand = Get-Command docker.exe -ErrorAction Stop
$docker = $dockerCommand.Source
& $docker desktop start
if ($LASTEXITCODE -ne 0) { throw 'Could not start Docker Desktop.' }
$deadline = (Get-Date).AddSeconds(90)
do {
    & $docker --context desktop-linux info --format '{{.ServerVersion}}' 2>$null | Out-Null
    if ($LASTEXITCODE -eq 0) { break }
    if ((Get-Date) -gt $deadline) { throw 'Docker engine did not become ready.' }
    Start-Sleep -Seconds 2
} while ($true)
$running = & $docker --context desktop-linux inspect kubepilot-lab-control-plane --format '{{.State.Running}}'
if ($LASTEXITCODE -ne 0) { throw 'Existing lab container was not found. This script never recreates or deletes a cluster.' }
if ($running.Trim() -ne 'true') {
    & $docker --context desktop-linux start kubepilot-lab-control-plane
    if ($LASTEXITCODE -ne 0) { throw 'Could not start existing lab node.' }
}
$kubectl = Join-Path $LabRoot 'tools\kubectl.exe'
$kubectlArgs = @('--kubeconfig', (Join-Path $LabRoot 'config\kubeconfig'), '--context', 'kind-kubepilot-lab')
$deadline = (Get-Date).AddSeconds(120)
do {
    & $kubectl @kubectlArgs get --raw=/readyz --request-timeout=5s 2>$null | Out-Null
    if ($LASTEXITCODE -eq 0) { break }
    if ((Get-Date) -gt $deadline) { throw 'Kubernetes API did not become ready.' }
    Start-Sleep -Seconds 2
} while ($true)
& $kubectl @kubectlArgs wait --for=condition=Ready node --all --timeout=120s
if ($LASTEXITCODE -ne 0) { throw 'Lab node did not become Ready.' }
& (Join-Path $PSScriptRoot 'check-lab.ps1') -LabRoot $LabRoot
