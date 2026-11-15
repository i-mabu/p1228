# ===== 1. 環境ファイルの読み込み =====
function Read-P1228EnvironmentFile {
  param([Parameter(Mandatory = $true)][string]$Path)

  if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
    throw "Environment file not found: $Path"
  }

  $values = @{}
  $lineNumber = 0
  foreach ($line in Get-Content -LiteralPath $Path) {
    $lineNumber++
    $trimmed = $line.Trim()
    if (-not $trimmed -or $trimmed.StartsWith('#')) { continue }
    if ($trimmed -notmatch '^([A-Za-z_][A-Za-z0-9_]*)=(.*)$') {
      throw "Invalid environment entry at ${Path}:$lineNumber"
    }

    $name = $Matches[1]
    $value = $Matches[2].Trim()
    if ($value.Length -ge 2) {
      $first = $value.Substring(0, 1)
      $last = $value.Substring($value.Length - 1, 1)
      if (($first -eq '"' -and $last -eq '"') -or ($first -eq "'" -and $last -eq "'")) {
        $value = $value.Substring(1, $value.Length - 2)
      }
    }
    $values[$name] = $value
  }
  return $values
}


# ===== 2. 分離環境での実行 =====
function Invoke-P1228WithIsolatedEnvironment {
  param(
    [Parameter(Mandatory = $true)][System.Collections.IDictionary]$Environment,
    [Parameter(Mandatory = $true)][scriptblock]$Action
  )

  $target = [System.EnvironmentVariableTarget]::Process
  $snapshot = @{}

  try {
    foreach ($name in $Environment.Keys) {
      $key = [string]$name

      $snapshot[$key] = [System.Environment]::GetEnvironmentVariable(
        $key,
        $target
      )

      [System.Environment]::SetEnvironmentVariable(
        $key,
        [string]$Environment[$name],
        $target
      )
    }

    & $Action
  }
  finally {
    foreach ($name in $Environment.Keys) {
      $key = [string]$name

      [System.Environment]::SetEnvironmentVariable(
        $key,
        $snapshot[$key],
        $target
      )
    }
  }
}
