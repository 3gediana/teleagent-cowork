# Re-collect solo + merge team + grade + compare. Standalone, idempotent.

param()

$ErrorActionPreference = 'Continue'
$PSNativeCommandUseErrorActionPreference = $false

$Root = $PSScriptRoot
$SoloOutput = Join-Path $Root 'solo-output'
$TeamOutput = Join-Path $Root 'team-output'

function Log($m){ Write-Host "[collect] $m" -ForegroundColor Cyan }

# ---------- Refresh SOLO ----------
# Solo workdir = pool_d3efa2d8 (tasq-solo role). Copy full tasq/ + tests/.
$soloWd = 'D:\claude-code\coai2\platform\data\pool\pool_d3efa2d8'
Log "re-collecting SOLO from $soloWd"
if (Test-Path $SoloOutput) { Remove-Item -Recurse -Force $SoloOutput }
New-Item -ItemType Directory -Force -Path $SoloOutput | Out-Null
foreach ($sub in @('tasq','tests')) {
  $src = Join-Path $soloWd $sub
  if (Test-Path $src) {
    Copy-Item $src (Join-Path $SoloOutput $sub) -Recurse -Force
    Log "  copied $sub/"
  } else {
    Log "  no $sub/ found"
  }
}
# Top-level scripts / toml
Get-ChildItem $soloWd -File -ErrorAction SilentlyContinue |
  Where-Object { $_.Extension -in '.py','.toml','.md' -and $_.Name -notlike '.*' -and $_.Name -ne 'SPEC.md' } |
  ForEach-Object { Copy-Item $_.FullName (Join-Path $SoloOutput $_.Name) -Force }
@{
  arm = 'solo'
  working_dir = $soloWd
  collected_at = (Get-Date -Format 'o')
} | ConvertTo-Json | Set-Content (Join-Path $SoloOutput 'run-meta.json') -Encoding UTF8

# ---------- Merge TEAM ----------
Log "merging TEAM outputs"
$teamInstances = @(
  @{ role='tasq-foundation';   wd='D:\claude-code\coai2\platform\data\pool\pool_44cd9882' },
  @{ role='tasq-cli-core';     wd='D:\claude-code\coai2\platform\data\pool\pool_5fc59cb8' },
  @{ role='tasq-cli-advanced'; wd='D:\claude-code\coai2\platform\data\pool\pool_f5b2fd43' }
)
if (Test-Path $TeamOutput) { Remove-Item -Recurse -Force $TeamOutput }
New-Item -ItemType Directory -Force -Path $TeamOutput | Out-Null
$conflicts = @()
foreach ($inst in $teamInstances) {
  if (-not (Test-Path $inst.wd)) { continue }
  foreach ($sub in @('tasq','tests')) {
    $src = Join-Path $inst.wd $sub
    if (-not (Test-Path $src)) { continue }
    Get-ChildItem $src -File -Recurse | Where-Object { $_.Extension -eq '.py' } | ForEach-Object {
      $rel = $_.FullName.Substring((Join-Path $inst.wd '').Length).TrimStart('\')
      $dst = Join-Path $TeamOutput $rel
      $dstDir = Split-Path $dst -Parent
      if (-not (Test-Path $dstDir)) { New-Item -ItemType Directory -Force -Path $dstDir | Out-Null }
      if (Test-Path $dst) {
        $conflicts += "$rel (existing copy kept; $($inst.role)'s version skipped)"
      } else {
        Copy-Item $_.FullName $dst -Force
      }
    }
  }
  Log "  merged $($inst.role)"
}
# Ensure package init files exist
foreach ($init in @('tasq\__init__.py','tasq\cli\__init__.py')) {
  $p = Join-Path $TeamOutput $init
  $dir = Split-Path $p -Parent
  if (-not (Test-Path $dir)) { New-Item -ItemType Directory -Force -Path $dir | Out-Null }
  if (-not (Test-Path $p)) {
    "# auto-created by collect-and-grade.ps1" | Set-Content $p -Encoding UTF8
    Log "  synthesised $init"
  }
}
@{
  arm = 'team'
  instances = $teamInstances
  conflicts = $conflicts
  collected_at = (Get-Date -Format 'o')
} | ConvertTo-Json -Depth 6 | Set-Content (Join-Path $TeamOutput 'run-meta.json') -Encoding UTF8

# ---------- Grade both ----------
Log "=== GRADE: solo ==="
& (Join-Path $Root 'bench.ps1') -OutputDir $SoloOutput -Label 'solo'
Log "=== GRADE: team ==="
& (Join-Path $Root 'bench.ps1') -OutputDir $TeamOutput -Label 'team'

Log "collect-and-grade complete"
