# ===== 1. 初期化と停止対象の定義 =====
param([string]$ProjectRoot = (Resolve-Path (Join-Path $PSScriptRoot '../..')).Path)
$ErrorActionPreference = 'Stop'
$runtime = Join-Path $ProjectRoot '.runtime'
if (-not (Test-Path $runtime)) { Write-Host 'No runtime directory.'; exit 0 }

$nodeCommand = Get-Command node -ErrorAction SilentlyContinue
$expected = @{
  'gateway' = @{ Executable = (Join-Path $runtime 'p1228-gateway.exe'); Script = $null }
  'general' = @{ Executable = if ($nodeCommand) { $nodeCommand.Source } else { $null }; Script = (Join-Path $ProjectRoot 'workers/dist/general/index.js') }
  'read-aloud-a' = @{ Executable = if ($nodeCommand) { $nodeCommand.Source } else { $null }; Script = (Join-Path $ProjectRoot 'workers/dist/read-aloud/index.js') }
  'read-aloud-b' = @{ Executable = if ($nodeCommand) { $nodeCommand.Source } else { $null }; Script = (Join-Path $ProjectRoot 'workers/dist/read-aloud/index.js') }
}

# ===== 2. プロセスの停止処理 =====
Get-ChildItem -Path $runtime -Filter '*.pid' | Sort-Object Name -Descending | ForEach-Object {
  $target = $expected[$_.BaseName]
  if (-not $target) {
    Write-Warning "Ignoring unknown PID file: $($_.Name)"
    return
  }

  $raw = (Get-Content $_.FullName -Raw).Trim()
  if ($raw -notmatch '^\d+$') {
    Write-Warning "Invalid PID file; leaving it for inspection: $($_.Name)"
    return
  }

  $processId = [int]$raw
  $process = Get-CimInstance Win32_Process -Filter "ProcessId = $processId" -ErrorAction Stop
  if (-not $process) {
    Remove-Item $_.FullName -Force
    Write-Host "Removed stale PID file: $($_.Name)"
    return
  }

  $matchesTarget = $false
  if ($target.Executable -and $process.ExecutablePath -and
      [string]::Equals($process.ExecutablePath, $target.Executable, [StringComparison]::OrdinalIgnoreCase)) {
    $matchesTarget = if ($target.Script) {
      $process.CommandLine -and $process.CommandLine.IndexOf($target.Script, [StringComparison]::OrdinalIgnoreCase) -ge 0
    } else {
      $true
    }
  }
  if (-not $matchesTarget) {
    Write-Warning "PID $processId no longer matches $($_.BaseName); leaving process and PID file untouched."
    return
  }

  Stop-Process -Id $processId -Force -ErrorAction Stop
  Remove-Item $_.FullName -Force
  Write-Host "Stopped $($_.BaseName) ($processId)"
}

# ===== 3. 終了確認 =====
$remainingPidFiles = @(Get-ChildItem -LiteralPath $runtime -Filter '*.pid' -File)
if ($remainingPidFiles.Count -gt 0) {
  Write-Warning 'Some processes may still be running.'
  exit 1
}

# Runtime logs stay in .runtime/; there is no remote log archive anymore.
