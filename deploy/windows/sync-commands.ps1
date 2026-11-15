param([string]$ProjectRoot = (Resolve-Path (Join-Path $PSScriptRoot '../..')).Path)
$ErrorActionPreference = 'Stop'
Set-Location $ProjectRoot
$environmentHelper = Join-Path $PSScriptRoot 'environment.ps1'
. $environmentHelper
$values = Read-P1228EnvironmentFile (Join-Path $ProjectRoot '.env')
$node = (Get-Command node -ErrorAction Stop).Source
$items = @(
  @{ Bot='general'; Worker='general'; Secret=$values['P1228_GENERAL_WORKER_SECRET']; App=$values['P1228_GENERAL_APPLICATION_ID'] },
  @{ Bot='read_aloud_a'; Worker='read_aloud_a'; Secret=$values['P1228_READ_ALOUD_A_WORKER_SECRET']; App=$values['P1228_READ_ALOUD_A_APPLICATION_ID'] },
  @{ Bot='read_aloud_b'; Worker='read_aloud_b'; Secret=$values['P1228_READ_ALOUD_B_WORKER_SECRET']; App=$values['P1228_READ_ALOUD_B_APPLICATION_ID'] }
)
$gatewayUrl = if ($values['P1228_GATEWAY_URL']) { $values['P1228_GATEWAY_URL'] } else { 'http://127.0.0.1:8787' }
foreach ($item in $items) {
  if (-not $item.Secret -or -not $item.App) {
    throw "Missing Worker secret or Application ID for $($item.Bot) in .env"
  }
  $childEnvironment = @{
    P1228_GATEWAY_URL = $gatewayUrl
    P1228_WORKER_ID = $item.Worker
    P1228_BOT_ID = $item.Bot
    P1228_WORKER_SECRET = $item.Secret
    P1228_APPLICATION_ID = $item.App
  }
  $exitCode = Invoke-P1228WithIsolatedEnvironment $childEnvironment {
    & $node (Join-Path $ProjectRoot 'workers/dist/sync-commands.js') | Out-Host
    $LASTEXITCODE
  }
  if ($exitCode -ne 0) { throw "Command registration failed for $($item.Bot) (exit $exitCode)" }
}
Write-Host 'Application commands registered.'
