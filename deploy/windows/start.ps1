param(
  [string]$ProjectRoot = (Resolve-Path (Join-Path $PSScriptRoot '../..')).Path
)
$ErrorActionPreference = 'Stop'
Set-Location $ProjectRoot

$environmentHelper = Join-Path $PSScriptRoot 'environment.ps1'
. $environmentHelper
$values = Read-P1228EnvironmentFile (Join-Path $ProjectRoot '.env')

$required = @('P1228_GENERAL_BOT_TOKEN','P1228_READ_ALOUD_A_BOT_TOKEN','P1228_READ_ALOUD_B_BOT_TOKEN',
  'P1228_GENERAL_WORKER_SECRET','P1228_READ_ALOUD_A_WORKER_SECRET','P1228_READ_ALOUD_B_WORKER_SECRET',
  'P1228_GENERAL_APPLICATION_ID','P1228_READ_ALOUD_A_APPLICATION_ID','P1228_READ_ALOUD_B_APPLICATION_ID')
foreach ($name in $required) { if (-not $values[$name]) { throw "Missing $name in .env" } }

New-Item -ItemType Directory -Force -Path '.runtime' | Out-Null
$null = Get-Command go -ErrorAction Stop
$node = (Get-Command node -ErrorAction Stop).Source
$goVersion = (& go version) -join ' '
$nodeVersion = (& node --version) -join ' '
$nodeNumeric = [version]($nodeVersion.TrimStart('v'))
if ($nodeNumeric -lt [version]'24.17.0') { throw "Node.js 24.17.0 or newer is required; found $nodeVersion" }
$goNumeric = [version](((& go version) -split 'go')[-1].Split(' ')[0])
if ($goNumeric -lt [version]'1.23.0') { throw "Go 1.23.0 or newer is required; found $goVersion" }
Write-Host "Building p1228 ($goVersion, $nodeVersion)"

Push-Location 'gateway'
try { & go build -o (Join-Path $ProjectRoot '.runtime/p1228-gateway.exe') ./cmd/gateway; if ($LASTEXITCODE -ne 0) { throw 'Go build failed' } }
finally { Pop-Location }
Push-Location 'workers'
try {
  if (-not (Test-Path 'node_modules')) { & npm ci --no-audit --no-fund; if ($LASTEXITCODE -ne 0) { throw 'npm ci --no-audit --no-fund failed' } }
  & npm run build; if ($LASTEXITCODE -ne 0) { throw 'TypeScript build failed' }
}
finally { Pop-Location }

$logDir = Join-Path $ProjectRoot '.runtime'
$runId = [DateTime]::UtcNow.ToString("yyyyMMdd'T'HHmmssfff'Z'", [Globalization.CultureInfo]::InvariantCulture) + '-' + [guid]::NewGuid().ToString('N')
$gatewayEnvironment = @{}
foreach ($name in $required) { $gatewayEnvironment[$name] = $values[$name] }
$gateway = Invoke-P1228WithIsolatedEnvironment $gatewayEnvironment {
  Start-Process -FilePath (Join-Path $ProjectRoot '.runtime/p1228-gateway.exe') -ArgumentList @('-config', (Join-Path $ProjectRoot 'config/gateway.windows.yaml')) -WorkingDirectory $ProjectRoot -PassThru -RedirectStandardOutput (Join-Path $logDir "gateway-$runId.log") -RedirectStandardError (Join-Path $logDir "gateway-error-$runId.log")
}
Set-Content -Path (Join-Path $logDir 'gateway.pid') -Value $gateway.Id

$healthUrl = 'http://127.0.0.1:8787/internal/v1/health'
$ready = $false
$lastHealthError = 'no response received'
for ($i = 0; $i -lt 30; $i++) {
  Start-Sleep -Seconds 1
  try {
    $health = Invoke-RestMethod -Uri $healthUrl -TimeoutSec 2
    if ($health.status -eq 'ready') { $ready = $true; break }
    $lastHealthError = "unexpected health response: $($health | ConvertTo-Json -Compress)"
  }
  catch { $lastHealthError = $_.Exception.Message }

  if (-not (Get-Process -Id $gateway.Id -ErrorAction SilentlyContinue)) {
    $lastHealthError = "Gateway process $($gateway.Id) exited before becoming healthy"
    break
  }
}
if (-not $ready) {
  $process = Get-CimInstance Win32_Process -Filter "ProcessId = $($gateway.Id)" -ErrorAction SilentlyContinue
  if ($process -and [string]::Equals($process.ExecutablePath, (Join-Path $ProjectRoot '.runtime/p1228-gateway.exe'), [StringComparison]::OrdinalIgnoreCase)) {
    Stop-Process -Id $gateway.Id -Force -ErrorAction SilentlyContinue
  }
  Remove-Item -Path (Join-Path $logDir 'gateway.pid') -Force -ErrorAction SilentlyContinue
  throw "Gateway did not become healthy: $lastHealthError. Check .runtime/gateway.log"
}

$workers = @(
  @{ Name='general'; Bot='general'; Secret=$values['P1228_GENERAL_WORKER_SECRET']; App=$values['P1228_GENERAL_APPLICATION_ID']; Script='dist/general/index.js' },
  @{ Name='read-aloud-a'; Bot='read_aloud_a'; Secret=$values['P1228_READ_ALOUD_A_WORKER_SECRET']; App=$values['P1228_READ_ALOUD_A_APPLICATION_ID']; Script='dist/read-aloud/index.js' },
  @{ Name='read-aloud-b'; Bot='read_aloud_b'; Secret=$values['P1228_READ_ALOUD_B_WORKER_SECRET']; App=$values['P1228_READ_ALOUD_B_APPLICATION_ID']; Script='dist/read-aloud/index.js' }
)
foreach ($worker in $workers) {
  $childEnvironment = @{
    P1228_GATEWAY_URL = 'http://127.0.0.1:8787'
    P1228_WORKER_ID = $worker.Bot
    P1228_BOT_ID = $worker.Bot
    P1228_WORKER_SECRET = $worker.Secret
    P1228_APPLICATION_ID = $worker.App
  }
  foreach ($name in @('P1228_AUDIO_ENGINE_URL', 'P1228_AUDIO_CACHE_DIR', 'P1228_AUDIO_CACHE_MAX_MB', 'P1228_AUDIO_CACHE_TTL_HOURS')) {
    if ($values[$name]) { $childEnvironment[$name] = $values[$name] }
  }
  $stdout = Join-Path $logDir ($worker.Name + "-$runId.log")
  $stderr = Join-Path $logDir ($worker.Name + "-error-$runId.log")
  $proc = Invoke-P1228WithIsolatedEnvironment $childEnvironment {
    Start-Process -FilePath $node -ArgumentList (Join-Path $ProjectRoot ('workers/' + $worker.Script)) -WorkingDirectory (Join-Path $ProjectRoot 'workers') -PassThru -RedirectStandardOutput $stdout -RedirectStandardError $stderr
  }
  Set-Content -Path (Join-Path $logDir ($worker.Name + '.pid')) -Value $proc.Id
}
Write-Host 'p1228 started. Logs: .runtime/*.log'
