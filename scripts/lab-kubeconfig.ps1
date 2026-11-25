param([string]$Context = 'kind-kubepilot-lab')
$ErrorActionPreference = 'Stop'
if ($Context -ne 'kind-kubepilot-lab') { throw '只为本项目的 kind 实验集群生成配置。' }
$projectRoot = Split-Path -Parent $PSScriptRoot
New-Item -ItemType Directory -Path (Join-Path $projectRoot 'data') -Force | Out-Null
# 仅提取集群连接信息，不提取管理员用户的证书或私钥。
$clusterJson = & kubectl --context $Context config view --minify --raw -o 'jsonpath={.clusters[0].cluster}'
if ($LASTEXITCODE -ne 0) { throw '无法读取实验集群连接信息' }
$cluster = $clusterJson | ConvertFrom-Json
$serviceToken = & kubectl --context $Context --namespace kube-agent-lab create token kubepilot --duration=24h
if ($LASTEXITCODE -ne 0 -or -not $serviceToken) { throw '无法为实验 ServiceAccount 创建令牌，请先应用 rbac-readonly.yaml' }
$configuration = @{
    apiVersion = 'v1'
    kind = 'Config'
    clusters = @(@{ name = 'kubepilot-lab'; cluster = $cluster })
    users = @(@{ name = 'kubepilot'; user = @{ token = $serviceToken.Trim() } })
    contexts = @(@{ name = 'kubepilot-lab-reader'; context = @{ cluster = 'kubepilot-lab'; user = 'kubepilot'; namespace = 'kube-agent-lab' } })
    'current-context' = 'kubepilot-lab-reader'
}
$destination = Join-Path $projectRoot 'data\kubepilot.kubeconfig'
$configuration | ConvertTo-Json -Depth 12 | Set-Content -LiteralPath $destination -Encoding UTF8
$serviceToken = $null
$configuration = $null
Write-Output "已保存限定命名空间身份的 kubeconfig：$destination"
Write-Output '令牌有效期 24 小时；本文件包含凭证，请勿提交到版本控制。'
