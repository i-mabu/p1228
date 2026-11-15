# ===== 1. 準備 =====
param(
  [string]$ProjectRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
)
$ErrorActionPreference = 'Stop'
Set-Location $ProjectRoot

# ===== 2. Goの整形（gofmt） =====
if (Get-Command gofmt -ErrorAction SilentlyContinue) {
  Push-Location 'gateway'
  try { & gofmt -w .; if ($LASTEXITCODE -ne 0) { throw 'gofmt failed' } }
  finally { Pop-Location }
  Write-Host 'gofmt: done'
} else {
  Write-Warning 'gofmt not found; skipping Go formatting.'
}

# ===== 3. TypeScriptと設定ファイルの整形（Prettier） =====
if (Get-Command npm -ErrorAction SilentlyContinue) {
  if (-not (Test-Path 'workers/node_modules')) {
    Push-Location 'workers'
    try { & npm ci --ignore-scripts --no-audit --no-fund; if ($LASTEXITCODE -ne 0) { throw 'npm ci failed' } }
    finally { Pop-Location }
  }
  Push-Location 'workers'
  try { & npm run format; if ($LASTEXITCODE -ne 0) { throw 'prettier failed' } }
  finally { Pop-Location }
  Write-Host 'prettier: done'
} else {
  Write-Warning 'npm not found; skipping Prettier formatting.'
}

# ===== 4. Shellの整形（shfmt） =====
if (Get-Command shfmt -ErrorAction SilentlyContinue) {
  $shellFiles = @('deploy/install-ubuntu.sh', 'scripts/format.sh', '.githooks/pre-commit') +
    @(Get-ChildItem 'deploy/scripts/*.sh' | ForEach-Object { $_.FullName })
  & shfmt -i 2 -ci -w $shellFiles
  if ($LASTEXITCODE -ne 0) { throw 'shfmt failed' }
  Write-Host 'shfmt: done'
} else {
  Write-Warning 'shfmt not found; skipping Shell formatting. Install shfmt v3.10.0: https://github.com/mvdan/sh/releases'
}

# ===== 5. PowerShellの整形（Invoke-Formatter、手動適用） =====
# ===== PowerShell スクリプトの整形は一括自動適用せず、PSScriptAnalyzer の =====
# ===== Invoke-Formatter で内容を確認してから手動で適用する運用とする。 =====
# ===== 例: Invoke-Formatter -ScriptDefinition (Get-Content -Raw deploy/windows/start.ps1) =====
if (Get-Module -ListAvailable PSScriptAnalyzer) {
  Write-Host 'PSScriptAnalyzer found. To preview PowerShell formatting, run Invoke-Formatter per file (see docs/operations/ci.md).'
} else {
  Write-Host 'PSScriptAnalyzer not installed. Install with: Install-Module PSScriptAnalyzer -Scope CurrentUser'
}

Write-Host 'All formatters finished.'
