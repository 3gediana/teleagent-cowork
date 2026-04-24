# bench.ps1 — quality-score harness for a tasq implementation.
#
# Run this from inside a "solo-output" or "team-output" directory that
# contains a tasq/ package and a tests/ folder. It writes a JSON
# report to bench-result.json with the raw numbers, and prints a
# human summary.
#
# You can run this any number of times after the experiment completes
# to sanity-check the numbers. No state mutated outside the output
# directory.

param(
  [string]$OutputDir = (Get-Location).Path,
  [string]$Label     = '(unlabelled)'
)

$ErrorActionPreference = 'Continue'
$PSNativeCommandUseErrorActionPreference = $false

$ExperimentRoot = $PSScriptRoot

function Step($msg) { Write-Host "[$Label] $msg" -ForegroundColor Cyan }

Step "grading $OutputDir"
if (-not (Test-Path (Join-Path $OutputDir 'tasq'))) {
  Write-Host "[$Label][FAIL] no tasq/ package in $OutputDir" -ForegroundColor Red
  $result = @{ label=$Label; dir=$OutputDir; status='missing_package' }
  $result | ConvertTo-Json | Set-Content (Join-Path $OutputDir 'bench-result.json')
  exit 1
}

$result = [ordered]@{
  label       = $Label
  dir         = $OutputDir
  graded_at   = (Get-Date -Format 'o')
  loc         = $null
  test_loc    = $null
  pytest      = @{ ran=$false; passed=0; failed=0; total=0; pct=0.0; stderr='' }
  coverage    = @{ pct=0.0; ran=$false }
  ruff        = @{ issues=-1; ran=$false }
  mypy        = @{ errors=-1; ran=$false }
  scenario    = @{ exit=-1; pass_count=0; fail_count=0; last_fail=''; ran=$false }
  score       = 0.0
}

Push-Location $OutputDir
try {
  # --- 1. LOC ---
  Step "counting lines"
  $srcLoc = 0
  $testLoc = 0
  Get-ChildItem tasq -Recurse -Filter '*.py' -File -ErrorAction SilentlyContinue | ForEach-Object {
    $srcLoc += (Get-Content $_.FullName -ErrorAction SilentlyContinue | Measure-Object -Line).Lines
  }
  if (Test-Path tests) {
    Get-ChildItem tests -Recurse -Filter '*.py' -File -ErrorAction SilentlyContinue | ForEach-Object {
      $testLoc += (Get-Content $_.FullName -ErrorAction SilentlyContinue | Measure-Object -Line).Lines
    }
  }
  $result.loc      = $srcLoc
  $result.test_loc = $testLoc
  Step "  tasq/ $srcLoc LOC, tests/ $testLoc LOC"

  # --- 2. pytest + coverage ---
  Step "pytest --cov"
  $pytestLog = Join-Path $OutputDir 'bench-pytest.log'
  # Use --cov-report=json:coverage.json with json. No -x so every test runs.
  & python -m pytest --cov=tasq --cov-report=term --cov-report=json:coverage.json --tb=short tests/ 2>&1 |
    Tee-Object -FilePath $pytestLog | Out-Null
  $pyExit = $LASTEXITCODE
  $result.pytest.ran = $true

  $tail = Get-Content $pytestLog -Tail 30 -ErrorAction SilentlyContinue
  $summary = $tail | Select-String -Pattern 'passed|failed|error' | Select-Object -Last 1
  if ($summary) {
    $line = $summary.Line
    if ($line -match '(\d+)\s+passed') { $result.pytest.passed = [int]$Matches[1] }
    if ($line -match '(\d+)\s+failed') { $result.pytest.failed = [int]$Matches[1] }
    $result.pytest.total = $result.pytest.passed + $result.pytest.failed
    if ($result.pytest.total -gt 0) {
      $result.pytest.pct = [math]::Round(100.0 * $result.pytest.passed / $result.pytest.total, 1)
    }
  }
  if ($pyExit -ne 0 -and $result.pytest.total -eq 0) {
    $result.pytest.stderr = (Get-Content $pytestLog -Tail 10 -ErrorAction SilentlyContinue) -join "`n"
  }
  Step "  pytest: $($result.pytest.passed)/$($result.pytest.total) passed ($($result.pytest.pct)%)"

  if (Test-Path coverage.json) {
    $cov = Get-Content coverage.json -Raw | ConvertFrom-Json
    $result.coverage.pct = [math]::Round($cov.totals.percent_covered, 1)
    $result.coverage.ran = $true
    Step "  coverage: $($result.coverage.pct)%"
  }

  # --- 3. ruff ---
  Step "ruff check"
  $ruffLog = Join-Path $OutputDir 'bench-ruff.log'
  & python -m ruff check tasq tests 2>&1 | Tee-Object -FilePath $ruffLog | Out-Null
  $ruffLines = Get-Content $ruffLog -ErrorAction SilentlyContinue
  $ruffSummary = $ruffLines | Select-String -Pattern 'Found (\d+) errors?'
  if ($ruffSummary) {
    $result.ruff.issues = [int]$ruffSummary.Matches[0].Groups[1].Value
  } elseif ($ruffLines -contains 'All checks passed!') {
    $result.ruff.issues = 0
  } else {
    # Fallback: count lines that look like diagnostics
    $result.ruff.issues = ($ruffLines | Select-String -Pattern ':\d+:\d+:').Count
  }
  $result.ruff.ran = $true
  Step "  ruff: $($result.ruff.issues) issues"

  # --- 4. mypy ---
  Step "mypy tasq/"
  $mypyLog = Join-Path $OutputDir 'bench-mypy.log'
  & python -m mypy --ignore-missing-imports --no-error-summary tasq 2>&1 | Tee-Object -FilePath $mypyLog | Out-Null
  $mypyLines = Get-Content $mypyLog -ErrorAction SilentlyContinue
  $mypyErrors = ($mypyLines | Select-String -Pattern '\s*error:').Count
  $result.mypy.errors = $mypyErrors
  $result.mypy.ran = $true
  Step "  mypy: $mypyErrors errors"

  # --- 5. scenario ---
  Step "scenarios/smoke.py (end-to-end)"
  $scenarioSrc = Join-Path $ExperimentRoot 'scenarios\smoke.py'
  if (-not (Test-Path $scenarioSrc)) {
    Write-Host "[$Label][WARN] scenario script not found at $scenarioSrc" -ForegroundColor Yellow
  } else {
    $scenarioDir = Join-Path $OutputDir 'scenarios'
    New-Item -ItemType Directory -Force -Path $scenarioDir | Out-Null
    Copy-Item $scenarioSrc (Join-Path $scenarioDir 'smoke.py') -Force
    $scenarioLog = Join-Path $OutputDir 'bench-scenario.log'
    $env:PYTHONPATH = $OutputDir
    & python $scenarioDir\smoke.py 2>&1 | Tee-Object -FilePath $scenarioLog | Out-Null
    $result.scenario.exit = $LASTEXITCODE
    $result.scenario.ran = $true
    $scLines = Get-Content $scenarioLog -ErrorAction SilentlyContinue
    $result.scenario.pass_count = ($scLines | Select-String -Pattern '^\[PASS\]').Count
    $result.scenario.fail_count = ($scLines | Select-String -Pattern '^\[FAIL\]').Count
    $lastFail = $scLines | Select-String -Pattern '^\[FAIL\]' | Select-Object -Last 1
    if ($lastFail) { $result.scenario.last_fail = $lastFail.Line }
    Step "  scenario exit=$($result.scenario.exit) pass=$($result.scenario.pass_count) fail=$($result.scenario.fail_count)"
  }

  # --- 6. composite score ---
  # scoring rubric:
  #   coverage %                          (0-100)
  # + pytest pass %                       (0-100)
  # + 20 if scenario exit==0              (binary)
  # - ruff issues * 0.5                   (clip at -20)
  # - mypy errors * 0.2                   (clip at -10)
  # - (LOC target penalty): 3000..8000 LOC is sweet spot; linear
  #   penalty for under 1500 (incomplete) or over 12000 (bloat).
  $cov_score = $result.coverage.pct
  $pyt_score = $result.pytest.pct
  $sce_score = if ($result.scenario.exit -eq 0) { 20 } else { 0 }
  $ruff_pen = [math]::Min(20, $result.ruff.issues * 0.5)
  if ($result.ruff.issues -lt 0) { $ruff_pen = 20 }  # not run = worst
  $mypy_pen = [math]::Min(10, $result.mypy.errors * 0.2)
  if ($result.mypy.errors -lt 0) { $mypy_pen = 10 }
  $loc = $srcLoc
  $loc_pen = 0.0
  if ($loc -lt 1500) { $loc_pen = (1500 - $loc) * 0.01 }
  elseif ($loc -gt 12000) { $loc_pen = ($loc - 12000) * 0.005 }
  $result.score = [math]::Round($cov_score + $pyt_score + $sce_score - $ruff_pen - $mypy_pen - $loc_pen, 1)

  Step "  --- composite score: $($result.score) ---"
}
finally {
  Pop-Location
}

$result | ConvertTo-Json -Depth 5 | Set-Content (Join-Path $OutputDir 'bench-result.json') -Encoding UTF8
Write-Host "[$Label] wrote bench-result.json"
