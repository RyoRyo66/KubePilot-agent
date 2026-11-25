param(
    [Parameter(Mandatory = $true)][string]$BaseURL,
    [Parameter(Mandatory = $true)][string]$Model,
    [switch]$SkipBuild
)
$ErrorActionPreference = 'Stop'
$modelUri = [Uri]$BaseURL
if (-not $modelUri.IsAbsoluteUri -or $modelUri.Scheme -notin @('http', 'https') -or $modelUri.UserInfo -or $modelUri.Query) { throw '请填写不含凭证或查询参数的 API 基地址。' }
if (-not $env:AGENT_LLM_API_KEY) {
    $secureModelKey = Read-Host '输入模型 API Key（隐藏输入，不写入文件）' -AsSecureString
    $keyPointer = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($secureModelKey)
    try { $env:AGENT_LLM_API_KEY = [Runtime.InteropServices.Marshal]::PtrToStringBSTR($keyPointer) }
    finally { [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($keyPointer); $secureModelKey.Dispose() }
}
$env:AGENT_LLM_BASE_URL = $BaseURL.TrimEnd('/')
$env:AGENT_LLM_MODEL = $Model
& (Join-Path $PSScriptRoot 'stop.ps1')
& (Join-Path $PSScriptRoot 'start.ps1') -SkipBuild:$SkipBuild
