param([string]$LabRoot = 'D:\KubePilotLab')
$ErrorActionPreference = 'Stop'
$kubectl = Join-Path $LabRoot 'tools\kubectl.exe'
$kubeconfigPath = Join-Path $LabRoot 'config\kubeconfig'
$kubectlArgs = @('--kubeconfig', $kubeconfigPath, '--context', 'kind-kubepilot-lab')
$nodesJson = & $kubectl @kubectlArgs get nodes -o json
if ($LASTEXITCODE -ne 0) { throw 'Node query failed.' }
$nodes = @((($nodesJson -join "`n") | ConvertFrom-Json).items)
if ($nodes.Count -ne 1) { throw 'Expected exactly one lab node.' }
$ready = @($nodes[0].status.conditions | Where-Object { $_.type -eq 'Ready' -and $_.status -eq 'True' })
if ($ready.Count -ne 1) { throw 'Lab node is not Ready.' }
& $kubectl @kubectlArgs get nodes -o wide
if ($LASTEXITCODE -ne 0) { throw 'Node query failed.' }
& $kubectl @kubectlArgs -n kube-system rollout status deployment/coredns --timeout=45s
if ($LASTEXITCODE -ne 0) { throw 'Cluster DNS is not ready.' }
& $kubectl @kubectlArgs -n local-path-storage rollout status deployment/local-path-provisioner --timeout=45s
if ($LASTEXITCODE -ne 0) { throw 'Local storage provisioner is not ready.' }
& $kubectl @kubectlArgs -n kube-agent-lab rollout status deployment/demo-web --timeout=45s
if ($LASTEXITCODE -ne 0) { throw 'Demo deployment is not ready.' }
$deadline = (Get-Date).AddSeconds(60)
$announcedWait = $false
do {
    # After node startup, API status can still describe the previous containers.
    # Wait for live DNS/HTTP responses instead of accepting that cached status.
    $serviceReply = & $kubectl @kubectlArgs --request-timeout=5s -n kube-agent-lab exec deployment/demo-web -- wget -T 5 -q -O - http://demo-web.kube-agent-lab.svc.cluster.local/ 2>$null
    $serviceReady = $LASTEXITCODE -eq 0 -and ($serviceReply -join "`n") -match 'Welcome to nginx'
    $hostReady = $false
    try {
        $reply = Invoke-WebRequest -Uri 'http://127.0.0.1:18090/' -UseBasicParsing -TimeoutSec 5
        $hostReady = $reply.StatusCode -eq 200 -and $reply.Content -match 'Welcome to nginx'
    } catch { $hostReady = $false }
    if ($serviceReady -and $hostReady) { break }
    if ((Get-Date) -ge $deadline) { throw ('Live application check timed out. Service DNS/HTTP=' + $serviceReady + '; localhost HTTP=' + $hostReady) }
    if (-not $announcedWait) { Write-Output 'Waiting for live Service and localhost HTTP responses...'; $announcedWait = $true }
    Start-Sleep -Seconds 3
} while ($true)
& $kubectl @kubectlArgs -n kube-agent-lab get pods,svc,endpointslices
if ($LASTEXITCODE -ne 0) { throw 'Resource query failed.' }
Write-Output 'PASS: one Ready node, DNS, storage provisioner, in-cluster Service and localhost HTTP 200.'
