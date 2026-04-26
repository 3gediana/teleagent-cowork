# run-experiment.ps1 — drive both the SOLO and TEAM tasq builds
# against the live pool manager backend + real MiniMax-M2.7, then
# grade the outputs via bench.ps1.
#
# Usage:
#   .\run-experiment.ps1                 # runs both arms
#   .\run-experiment.ps1 -Arm solo       # only solo
#   .\run-experiment.ps1 -Arm team       # only team
#   .\run-experiment.ps1 -Grade          # skip agent runs, just grade
#                                        # whatever's in the output dirs

param(
  [ValidateSet('solo','team','both')] [string]$Arm = 'both',
  [switch]$Grade,
  [int]$WaitSeconds = 1800     # max wait for an agent to finish
)

$ErrorActionPreference = 'Continue'
$PSNativeCommandUseErrorActionPreference = $false

$ExperimentRoot = $PSScriptRoot
$SoloOutput = Join-Path $ExperimentRoot 'solo-output'
$TeamOutput = Join-Path $ExperimentRoot 'team-output'
$BaseURL = 'http://127.0.0.1:3003/api/v1'
$ProviderID = 'minimax-coding-plan'
$ModelID = 'MiniMax-M2.7'

function Log($m){ Write-Host "[exp] $m" -ForegroundColor Cyan }
function Ok($m){  Write-Host "[ok]  $m" -ForegroundColor Green }
function Fail($m){ Write-Host "[FAIL] $m" -ForegroundColor Red; exit 1 }

# ---------- DB creds ----------
function Get-HumanKey {
  docker exec a3c-mysql mysql -uroot -proot123 -Da3c -e "SELECT access_key FROM agent WHERE is_human=1 ORDER BY created_at LIMIT 1\G" 2>$null `
    | Where-Object { $_ -match 'access_key' } | ForEach-Object { ($_ -split ':')[1].Trim() }
}

function Get-ProjectID {
  docker exec a3c-mysql mysql -uroot -proot123 -Da3c -e "SELECT id FROM project ORDER BY created_at LIMIT 1\G" 2>$null `
    | Where-Object { $_ -match 'id:' } | ForEach-Object { ($_ -split ':')[1].Trim() }
}

# ---------- Push a task payload via Redis (BOM-free!) ----------
function Push-Broadcast {
  param([string]$AgentID, [string]$TaskID, [string]$Description)
  $msgID = "exp_$(Get-Random)"
  $envelope = @{
    header  = @{ type = 'TASK_ASSIGN'; messageID = $msgID; timestamp = [long](Get-Date -UFormat %s); target = $AgentID }
    payload = @{ task_id = $TaskID; description = $Description }
  } | ConvertTo-Json -Depth 6 -Compress
  $path = Join-Path $env:TEMP "exp-payload-$TaskID.json"
  [System.IO.File]::WriteAllBytes($path, [System.Text.UTF8Encoding]::new($false).GetBytes($envelope))
  docker cp $path "a3c-redis:/tmp/exp-$TaskID.json" | Out-Null
  docker exec a3c-redis sh -c "cat /tmp/exp-$TaskID.json | redis-cli -x RPUSH a3c:directed:$AgentID" | Out-Null
}

# ---------- Spawn one agent, return {instance_id, agent_id, port, session_id, working_dir} ----------
function Spawn-Pool {
  param([string]$Role, [string]$HumanKey, [string]$ProjectID)
  $body = @{
    project_id           = $ProjectID
    role_hint            = $Role
    opencode_provider_id = $ProviderID
    opencode_model_id    = $ModelID
  } | ConvertTo-Json
  $r = Invoke-WebRequest -Uri "$BaseURL/agentpool/spawn" -Method POST `
    -ContentType 'application/json' -Body $body `
    -Headers @{Authorization="Bearer $HumanKey"} `
    -UseBasicParsing -TimeoutSec 120
  $d = ($r.Content | ConvertFrom-Json).data
  if (-not $d -or $d.status -ne 'ready') { Fail "spawn($Role) failed: $($r.Content)" }
  return $d
}

# ---------- Poll an opencode session until idle or timeout ----------
# "Idle" heuristic: no new assistant message for 45 seconds AND
# at least one assistant reply has been received. Returns final
# message count.
function Wait-SessionIdle {
  param([string]$Port, [string]$SessionID, [int]$MaxSeconds = 1800, [string]$Label='agent')
  $deadline = (Get-Date).AddSeconds($MaxSeconds)
  $lastCount = 0
  $lastChangeAt = Get-Date
  while ((Get-Date) -lt $deadline) {
    Start-Sleep 15
    try {
      $raw = curl.exe -sS "http://127.0.0.1:$Port/session/$SessionID/message" 2>$null | Out-String
      $msgs = $raw | ConvertFrom-Json
      $count = $msgs.Count
    } catch {
      $count = $lastCount
    }
    if ($count -gt $lastCount) {
      $lastCount = $count
      $lastChangeAt = Get-Date
      Write-Host "[$Label] messages=$count" -ForegroundColor DarkGray
    }
    $idleFor = ((Get-Date) - $lastChangeAt).TotalSeconds
    $hasAsst = ($msgs | Where-Object { $_.info.role -eq 'assistant' }).Count -gt 0
    if ($hasAsst -and $idleFor -gt 45) {
      Write-Host "[$Label] idle for ${idleFor}s, considering done" -ForegroundColor DarkGray
      return $count
    }
  }
  Write-Host "[$Label] hit MaxSeconds=$MaxSeconds, returning with count=$lastCount" -ForegroundColor Yellow
  return $lastCount
}

# ---------- copy workdir output (tasq/ + tests/) to OutputDir ----------
function Collect-Output {
  param([string]$WorkingDir, [string]$OutputDir)
  if (Test-Path $OutputDir) { Remove-Item -Recurse -Force $OutputDir }
  New-Item -ItemType Directory -Force -Path $OutputDir | Out-Null
  foreach ($sub in @('tasq','tests')) {
    $src = Join-Path $WorkingDir $sub
    if (Test-Path $src) {
      Copy-Item $src -Destination (Join-Path $OutputDir $sub) -Recurse -Force
    }
  }
  # Also pull any top-level .py / .toml / setup files that might be relevant
  Get-ChildItem $WorkingDir -File -ErrorAction SilentlyContinue |
    Where-Object { $_.Extension -in '.py','.toml','.cfg','.md' -and $_.Name -notmatch '^\.' } |
    ForEach-Object { Copy-Item $_.FullName (Join-Path $OutputDir $_.Name) -Force }
}

# =================================================================
# ARM: solo
# =================================================================
function Run-Solo {
  param([string]$HumanKey, [string]$ProjectID)
  Log "=== SOLO arm ==="
  $startAt = Get-Date

  # Clean prior pool agents
  docker exec a3c-mysql mysql -uroot -proot123 -Da3c -e "DELETE FROM agent WHERE is_human=0;" 2>$null | Out-Null

  $inst = Spawn-Pool 'tasq-solo' $HumanKey $ProjectID
  Log "solo instance: $($inst.id) port=$($inst.port) wd=$($inst.working_dir)"
  $wd = $inst.working_dir

  # Copy the SPEC into the agent's workdir so it can read it
  Copy-Item (Join-Path $ExperimentRoot 'SPEC.md') (Join-Path $wd 'SPEC.md') -Force

  $prompt = @"
You are implementing the entire `tasq` CLI described in SPEC.md (in
your current working directory).

READ SPEC.md FIRST. Then implement EVERY module it lists:
  tasq/db.py, tasq/models.py, tasq/config.py, tasq/cli/__init__.py,
  tasq/cli/tasks.py, tasq/cli/projects.py, tasq/cli/tags.py,
  tasq/cli/reports.py, tasq/cli/io.py, tasq/cli/shell.py,
  tasq/formatters.py, tasq/__main__.py

Plus a test file per module under tests/. Aim for 3000-8000 lines
of source + tests to adequately cover the feature surface.

Your process:
  1. Read SPEC.md carefully.
  2. Create the package skeleton (all the files listed above).
  3. Implement each module from the bottom up: db first, then models,
     then config, then cli/*, then formatters, then shell, then __main__.
  4. Write a pytest test for each module as you go.
  5. After every 2-3 modules, run `python -m pytest tests/ -x` to catch
     regressions early.
  6. When done, run `python -m tasq --help` and make sure it works.
  7. Finally, print a short status summary: module count, LOC, tests passing.

Use the write + bash tools liberally. Do NOT ask clarifying questions —
default to the obvious interpretation when SPEC.md is vague. Keep
going until the whole CLI works end-to-end. The grading harness will
run scenarios/smoke.py which exercises the full command surface.
"@

  Log "pushing solo task..."
  Push-Broadcast $inst.agent_id 'tasq-solo' $prompt

  Log "waiting for solo agent (max $WaitSeconds s)..."
  $msgCount = Wait-SessionIdle -Port $inst.port -SessionID $inst.opencode_session_id `
    -MaxSeconds $WaitSeconds -Label 'solo'

  $elapsed = ((Get-Date) - $startAt).TotalSeconds
  Log "solo done, elapsed=$([math]::Round($elapsed,0))s, final messages=$msgCount"

  # Collect
  Collect-Output $wd $SoloOutput
  @{
    arm = 'solo'
    elapsed_seconds = [math]::Round($elapsed, 1)
    messages = $msgCount
    instance_id = $inst.id
    agent_id = $inst.agent_id
    session_id = $inst.opencode_session_id
    port = $inst.port
    working_dir = $wd
  } | ConvertTo-Json | Set-Content (Join-Path $SoloOutput 'run-meta.json') -Encoding UTF8
  Ok "solo output at $SoloOutput"
}

# =================================================================
# ARM: team
# =================================================================
# 3 agents, each writes a disjoint subset of the modules. Then we
# merge their workdirs into $TeamOutput.
#
# Module assignment:
#   agent-A (foundation):  tasq/db.py, tasq/models.py, tasq/config.py,
#                          tasq/formatters.py, tasq/__main__.py
#                          + tests for those 5
#   agent-B (cli-core):    tasq/cli/__init__.py, tasq/cli/tasks.py,
#                          tasq/cli/projects.py, tasq/cli/tags.py
#                          + tests for those 4
#   agent-C (cli-advanced):tasq/cli/reports.py, tasq/cli/io.py,
#                          tasq/cli/shell.py
#                          + tests for those 3
#
# Each agent gets the FULL SPEC.md (contract is shared) but is told
# explicitly which files to write. All three write to their own
# workdir; merge step copies files into team-output/ without
# overwriting — if two agents happen to write the same file, the
# first one wins and a warning is logged.
function Run-Team {
  param([string]$HumanKey, [string]$ProjectID)
  Log "=== TEAM arm ==="
  $startAt = Get-Date

  docker exec a3c-mysql mysql -uroot -proot123 -Da3c -e "DELETE FROM agent WHERE is_human=0;" 2>$null | Out-Null

  $roles = @(
    @{ role='tasq-foundation'; files = @(
        'tasq/db.py','tasq/models.py','tasq/config.py',
        'tasq/formatters.py','tasq/__main__.py',
        'tests/test_db.py','tests/test_models.py','tests/test_config.py',
        'tests/test_formatters.py'
    )};
    @{ role='tasq-cli-core'; files = @(
        'tasq/cli/__init__.py','tasq/cli/tasks.py',
        'tasq/cli/projects.py','tasq/cli/tags.py',
        'tests/test_cli_tasks.py','tests/test_cli_projects.py',
        'tests/test_cli_tags.py'
    )};
    @{ role='tasq-cli-advanced'; files = @(
        'tasq/cli/reports.py','tasq/cli/io.py','tasq/cli/shell.py',
        'tests/test_cli_reports.py','tests/test_cli_io.py',
        'tests/test_cli_shell.py'
    )}
  )

  # Spawn all three
  $instances = @()
  foreach ($r in $roles) {
    Log "spawn $($r.role)"
    $inst = Spawn-Pool $r.role $HumanKey $ProjectID
    Copy-Item (Join-Path $ExperimentRoot 'SPEC.md') (Join-Path $inst.working_dir 'SPEC.md') -Force
    $instances += [pscustomobject]@{
      role = $r.role
      files = $r.files
      instance = $inst
    }
    Ok "  spawned $($inst.id) port=$($inst.port)"
  }

  # Push each its scoped task
  foreach ($ent in $instances) {
    $fileList = ($ent.files -join "`n  ")
    $prompt = @"
You are part of a 3-agent team implementing the `tasq` CLI.
READ SPEC.md in your current working directory FIRST — it's the
shared contract all three of you follow.

Your responsibility: ONLY these files.

  $fileList

Do NOT write any other .py files. Another agent is handling them.
The DB schema, the Task dataclass contract, and the Store API are
spelled out in SPEC.md; implement exactly to that contract so your
code composes with the others' code.

Your process:
  1. Read SPEC.md.
  2. Skim `tasq/` for stubs the other agents might have left (they
     may have already created empty files you need to flesh out —
     do so instead of re-creating).
  3. Implement each of your assigned files. Match the Store API
     and Task dataclass contract from SPEC.md exactly.
  4. Write pytest tests for each module you own.
  5. For files you own that depend on other agents' files: import
     with the assumption those files will exist at run time. Use
     small mocks/fixtures in your tests if you need to isolate.
  6. Run `python -m pytest tests/test_<your_module>.py -x` and make
     sure your own tests pass.
  7. When done, print a short summary of what you wrote.

Do not rewrite another agent's files. Do not ask clarifying
questions — pick the obvious interpretation.
"@
    Log "pushing $($ent.role) task..."
    Push-Broadcast $ent.instance.agent_id $ent.role $prompt
  }

  # Wait for all three in parallel
  Log "waiting for team (max $WaitSeconds s)..."
  $doneByRole = @{}
  $deadline = (Get-Date).AddSeconds($WaitSeconds)
  $firstIdleAt = @{}
  while ((Get-Date) -lt $deadline -and $doneByRole.Count -lt 3) {
    Start-Sleep 20
    foreach ($ent in $instances) {
      if ($doneByRole.ContainsKey($ent.role)) { continue }
      try {
        $raw = curl.exe -sS "http://127.0.0.1:$($ent.instance.port)/session/$($ent.instance.opencode_session_id)/message" 2>$null | Out-String
        $msgs = $raw | ConvertFrom-Json
        $count = $msgs.Count
      } catch { continue }
      # Count how many FILES from the agent's list exist now
      $writeDoneFiles = 0
      foreach ($f in $ent.files) {
        if (Test-Path (Join-Path $ent.instance.working_dir $f)) { $writeDoneFiles++ }
      }
      $fractionDone = if ($ent.files.Count -gt 0) { $writeDoneFiles / $ent.files.Count } else { 0 }
      Write-Host "[$($ent.role)] msgs=$count files=$writeDoneFiles/$($ent.files.Count)" -ForegroundColor DarkGray
      $hasAsst = ($msgs | Where-Object { $_.info.role -eq 'assistant' }).Count -gt 0
      # Done if: all files written, assistant replied, and no new messages in the last 40s
      if ($hasAsst -and $fractionDone -ge 0.75) {
        if (-not $firstIdleAt.ContainsKey($ent.role)) {
          $firstIdleAt[$ent.role] = @{ at=Get-Date; lastCount=$count }
        } else {
          $prev = $firstIdleAt[$ent.role]
          if ($count -gt $prev.lastCount) {
            $firstIdleAt[$ent.role] = @{ at=Get-Date; lastCount=$count }
          } elseif (((Get-Date) - $prev.at).TotalSeconds -gt 40) {
            $doneByRole[$ent.role] = $true
            Ok "$($ent.role) done (files=$writeDoneFiles/$($ent.files.Count), msgs=$count)"
          }
        }
      }
    }
  }

  $elapsed = ((Get-Date) - $startAt).TotalSeconds
  Log "team phase done, elapsed=$([math]::Round($elapsed,0))s, done=$($doneByRole.Count)/3"

  # Merge outputs into one TeamOutput
  if (Test-Path $TeamOutput) { Remove-Item -Recurse -Force $TeamOutput }
  New-Item -ItemType Directory -Force -Path $TeamOutput | Out-Null
  $conflicts = @()
  foreach ($ent in $instances) {
    foreach ($rel in $ent.files) {
      $src = Join-Path $ent.instance.working_dir $rel
      if (-not (Test-Path $src)) { continue }
      $dst = Join-Path $TeamOutput $rel
      $dstDir = Split-Path $dst -Parent
      if (-not (Test-Path $dstDir)) { New-Item -ItemType Directory -Force -Path $dstDir | Out-Null }
      if (Test-Path $dst) {
        $conflicts += "$rel (from $($ent.role) collided with existing)"
      } else {
        Copy-Item $src $dst -Force
      }
    }
  }
  # __init__.py may be missing in subpackages — make sure cli/ has one
  $cliInit = Join-Path $TeamOutput 'tasq\cli\__init__.py'
  if (-not (Test-Path $cliInit)) {
    New-Item -ItemType Directory -Force -Path (Split-Path $cliInit -Parent) | Out-Null
    "# auto-created by run-experiment.ps1 because agent did not write it" | Set-Content $cliInit -Encoding UTF8
  }
  $pkgInit = Join-Path $TeamOutput 'tasq\__init__.py'
  if (-not (Test-Path $pkgInit)) {
    "" | Set-Content $pkgInit -Encoding UTF8
  }

  @{
    arm = 'team'
    elapsed_seconds = [math]::Round($elapsed, 1)
    done_agents = $doneByRole.Count
    conflicts = $conflicts
    instances = ($instances | ForEach-Object { @{
      role=$_.role; instance_id=$_.instance.id; agent_id=$_.instance.agent_id;
      port=$_.instance.port; session_id=$_.instance.opencode_session_id;
      working_dir=$_.instance.working_dir
    }})
  } | ConvertTo-Json -Depth 6 | Set-Content (Join-Path $TeamOutput 'run-meta.json') -Encoding UTF8
  Ok "team output at $TeamOutput"
}

# =================================================================
# MAIN
# =================================================================
if (-not $Grade) {
  $humanKey = Get-HumanKey
  $projID = Get-ProjectID
  if (-not $humanKey -or -not $projID) { Fail "missing humanKey or projectID" }
  Log "humanKey=$($humanKey.Substring(0,8))... project=$projID"

  if ($Arm -in 'solo','both') { Run-Solo $humanKey $projID }
  if ($Arm -in 'team','both') { Run-Team $humanKey $projID }
}

# ---------- Grade both ----------
Log "=== GRADING ==="
foreach ($out in @($SoloOutput, $TeamOutput)) {
  if (-not (Test-Path $out)) { continue }
  $label = Split-Path $out -Leaf
  Log "grading $label"
  & (Join-Path $ExperimentRoot 'bench.ps1') -OutputDir $out -Label $label
}

# ---------- Compare ----------
$compareMd = Join-Path $ExperimentRoot 'compare.md'
$soloRes = $null; $teamRes = $null
if (Test-Path (Join-Path $SoloOutput 'bench-result.json')) {
  $soloRes = Get-Content (Join-Path $SoloOutput 'bench-result.json') -Raw | ConvertFrom-Json
}
if (Test-Path (Join-Path $TeamOutput 'bench-result.json')) {
  $teamRes = Get-Content (Join-Path $TeamOutput 'bench-result.json') -Raw | ConvertFrom-Json
}
$soloMeta = $null; $teamMeta = $null
if (Test-Path (Join-Path $SoloOutput 'run-meta.json')) {
  $soloMeta = Get-Content (Join-Path $SoloOutput 'run-meta.json') -Raw | ConvertFrom-Json
}
if (Test-Path (Join-Path $TeamOutput 'run-meta.json')) {
  $teamMeta = Get-Content (Join-Path $TeamOutput 'run-meta.json') -Raw | ConvertFrom-Json
}

function val($obj, [string]$path, $default='n/a') {
  try {
    $parts = $path -split '\.'
    $cur = $obj
    foreach ($p in $parts) { $cur = $cur.$p }
    if ($null -eq $cur) { return $default }
    return $cur
  } catch { return $default }
}

$md = @()
$md += "# Solo vs Team-3 agent pool benchmark"
$md += ""
$md += "Comparison of one pool agent doing the whole `tasq` CLI vs three pool agents"
$md += "dividing the modules among themselves. Both arms ran against the same"
$md += "SPEC.md, with the same MiniMax-M2.7 provider, on the same harness."
$md += ""
$md += "## Summary table"
$md += ""
$md += "| Metric | Solo | Team-3 |"
$md += "|---|---|---|"
$md += "| Elapsed wall-clock (s) | $(val $soloMeta 'elapsed_seconds') | $(val $teamMeta 'elapsed_seconds') |"
$md += "| Source LOC (tasq/) | $(val $soloRes 'loc') | $(val $teamRes 'loc') |"
$md += "| Test LOC (tests/) | $(val $soloRes 'test_loc') | $(val $teamRes 'test_loc') |"
$md += "| pytest passed | $(val $soloRes 'pytest.passed')/$(val $soloRes 'pytest.total') | $(val $teamRes 'pytest.passed')/$(val $teamRes 'pytest.total') |"
$md += "| pytest pass %  | $(val $soloRes 'pytest.pct')% | $(val $teamRes 'pytest.pct')% |"
$md += "| Coverage %     | $(val $soloRes 'coverage.pct')% | $(val $teamRes 'coverage.pct')% |"
$md += "| ruff issues    | $(val $soloRes 'ruff.issues') | $(val $teamRes 'ruff.issues') |"
$md += "| mypy errors    | $(val $soloRes 'mypy.errors') | $(val $teamRes 'mypy.errors') |"
$md += "| Scenario exit  | $(val $soloRes 'scenario.exit') | $(val $teamRes 'scenario.exit') |"
$md += "| Scenario [PASS]s | $(val $soloRes 'scenario.pass_count') | $(val $teamRes 'scenario.pass_count') |"
$md += "| **Composite score** | **$(val $soloRes 'score')** | **$(val $teamRes 'score')** |"
$md += ""
$md += "## Raw artifacts"
$md += ""
$md += "- Solo output:  ``$SoloOutput``"
$md += "- Team output:  ``$TeamOutput``"
$md += "- Run the harness yourself: ``.\bench.ps1 -OutputDir <path> -Label <name>``"
$md += "- Replay the scenario: ``python scenarios\smoke.py`` (from inside the output dir)"
$md += ""
$md += "## Score formula"
$md += ""
$md += "    score = coverage% + pytest_pass% + (20 if scenario_ok else 0)"
$md += "          - min(20, ruff_issues * 0.5)"
$md += "          - min(10, mypy_errors * 0.2)"
$md += "          - loc_penalty   (if <1500 or >12000 source LOC)"
$md += ""
$md += "Generated at $(Get-Date -Format 'o')"

$md -join "`r`n" | Set-Content $compareMd -Encoding UTF8
Ok "wrote compare.md"

Write-Host ""
Write-Host "========================================"
Write-Host "Experiment complete. See:"
Write-Host "  $compareMd"
Write-Host "========================================"
