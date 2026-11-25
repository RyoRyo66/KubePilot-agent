param([string]$LabRoot = 'D:\KubePilotLab')
$ErrorActionPreference = 'Stop'
$toolRoot = Join-Path $LabRoot 'tools'
$kubeconfigPath = Join-Path $LabRoot 'config\kubeconfig'
foreach ($tool in @('kind.exe', 'kubectl.exe')) {
    if (-not (Test-Path -LiteralPath (Join-Path $toolRoot $tool))) { throw ('Missing lab tool: ' + $tool) }
}
if (-not (Test-Path -LiteralPath $kubeconfigPath)) { throw 'Lab kubeconfig not found; create the cluster first.' }
$dockerBins = @(
    (Join-Path $env:LOCALAPPDATA 'Programs\DockerDesktop\resources\bin'),
    (Join-Path $env:ProgramFiles 'Docker\Docker\resources\bin')
)
$dockerBin = $dockerBins | Where-Object { Test-Path -LiteralPath (Join-Path $_ 'docker.exe') } | Select-Object -First 1
$env:PATH = $toolRoot + ';' + $(if ($dockerBin) { $dockerBin + ';' } else { '' }) + $env:PATH
$env:KUBECONFIG = $kubeconfigPath
$env:DOCKER_CONTEXT = 'desktop-linux'
Write-Output ('KubePilot lab environment selected for this terminal: ' + $LabRoot)
Write-Output 'Context: kind-kubepilot-lab; application: http://127.0.0.1:18090'
