param([string]$LabRoot = 'D:\KubePilotLab')
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'lab-env.ps1') -LabRoot $LabRoot
$kubectl = Join-Path $LabRoot 'tools\kubectl.exe'
$configPath = Join-Path $LabRoot 'config\kubeconfig'
$logRoot = Join-Path $LabRoot 'logs'
New-Item -ItemType Directory -Path $logRoot -Force | Out-Null
$services = @(
    @{Name='prometheus';Local=19090;Remote=9090;Check='/api/v1/status/buildinfo'},
    @{Name='alertmanager';Local=19093;Remote=9093;Check='/api/v2/status'}
)
foreach ($service in $services) {
    $url = 'http://127.0.0.1:' + $service.Local
    $listener = Get-NetTCPConnection -LocalPort $service.Local -State Listen -ErrorAction SilentlyContinue
    if ($listener) {
        $existing = Invoke-RestMethod -Uri ($url + $service.Check) -TimeoutSec 3
        if (($service.Name -eq 'prometheus' -and $existing.status -ne 'success') -or ($service.Name -eq 'alertmanager' -and -not $existing.versionInfo.version)) { throw ('Port is occupied by another service: ' + $service.Local) }
        Write-Output ('Already available: ' + $service.Name + ' ' + $url)
        continue
    }
    $processArguments = @('--kubeconfig', $configPath, '--context', 'kind-kubepilot-lab', '-n', 'kubepilot-monitoring', 'port-forward', ('service/' + $service.Name), ($service.Local.ToString() + ':' + $service.Remote), '--address', '127.0.0.1')
    $forward = Start-Process -FilePath $kubectl -ArgumentList $processArguments -WindowStyle Hidden -PassThru -RedirectStandardOutput (Join-Path $logRoot ($service.Name + '-port-forward.stdout.log')) -RedirectStandardError (Join-Path $logRoot ($service.Name + '-port-forward.stderr.log'))
    $forward.Id | Set-Content -LiteralPath (Join-Path $logRoot ($service.Name + '-port-forward.pid'))
    $ready = $false
    for ($attempt=0; $attempt -lt 30; $attempt++) {
        $forward.Refresh()
        if ($forward.HasExited) { throw ('Port forwarding exited: ' + $service.Name) }
        try {
            $response = Invoke-WebRequest -Uri ($url + '/-/ready') -UseBasicParsing -TimeoutSec 2
            if ($response.StatusCode -eq 200) { $ready=$true; break }
        } catch { }
        Start-Sleep -Milliseconds 500
    }
    if (-not $ready) { throw ('Monitoring endpoint not ready: ' + $service.Name) }
    Write-Output ('Available: ' + $service.Name + ' ' + $url)
}
