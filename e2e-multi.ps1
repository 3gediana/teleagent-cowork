# e2e-multi.ps1 — 3-agent parallel exercise.
#
# Goal: spawn 3 independent pool agents, hand each an independent
# Python module task, verify:
#   (a) all three work in parallel (distinct pids, ports, workdirs)
#   (b) broadcast routing doesn't cross-talk (each agent sees only
#       its own task)
#   (c) each agent actually writes files in its own workdir and
#       runs pytest on them (via opencode's bash + filesystem tools)
#   (d) the three workdirs stay disjoint
#
# Auditing is out-of-scope — pool agents are workers. Downstream
# change/review workflow would pick up the produced code via the
# usual A3C pipeline (StartAuditWorkflow etc.).

param(
  [string]$baseURL    = 'http://127.0.0.1:3003/api/v1',
  [string]$providerID = 'minimax-coding-plan',
  [string]$modelID    = 'MiniMax-M2.7',
  [int]   $replyWait  = 240
)

$ErrorActionPreference = 'Continue'
$PSNativeCommandUseErrorActionPreference = $false

function Log($m){ Write-Host "[e2e] $m" -ForegroundColor Cyan }
function Ok($m) { Write-Host "[ok]  $m" -ForegroundColor Green }
function Fail($m){ Write-Host "[FAIL] $m" -ForegroundColor Red; exit 1 }

# --- 0. DB cleanup + credentials ---
docker exec a3c-mysql mysql -uroot -proot123 -Da3c -e "DELETE FROM agent WHERE is_human=0;" 2>$null | Out-Null

$humanKey = docker exec a3c-mysql mysql -uroot -proot123 -Da3c -e "SELECT access_key FROM agent WHERE is_human=1 ORDER BY created_at LIMIT 1\G" 2>$null `
  | Where-Object { $_ -match 'access_key' } | ForEach-Object { ($_ -split ':')[1].Trim() }
$projID = docker exec a3c-mysql mysql -uroot -proot123 -Da3c -e "SELECT id FROM project ORDER BY created_at LIMIT 1\G" 2>$null `
  | Where-Object { $_ -match 'id:' } | ForEach-Object { ($_ -split ':')[1].Trim() }
if (-not $humanKey) { Fail "no human agent"     }
if (-not $projID)   { Fail "no project"          }
Ok "humanKey=$($humanKey.Substring(0,8))... project=$projID"

# --- 1. Define the three tasks ---
# Each task is pure-python, self-contained, with a clear directive
# to (a) write module (b) write pytest (c) run tests. Prompts are
# intentionally directive — the LLM is a worker here, not a planner.
$tasks = @(
  @{
    role = 'util-string'
    spec = @"
Create a Python module in the current directory called string_utils.py
with these three functions:

  - reverse_words(s: str) -> str: reverse the word order in s.
      Example: reverse_words("hello world foo") == "foo world hello"
  - count_vowels(s: str) -> int: count a/e/i/o/u (case-insensitive).
      Example: count_vowels("Hello World") == 3
  - is_palindrome(s: str) -> bool: true iff s equals its reverse
    ignoring case and non-alphanumerics.
      Example: is_palindrome("A man a plan a canal Panama") == True

Then create test_string_utils.py with pytest tests covering each
function with at least two cases including an edge case (empty
string).

Finally run `python -m pytest -x` in the current directory and show
the output. If tests fail fix them until they pass.
"@
  },
  @{
    role = 'util-math'
    spec = @"
Create a Python module in the current directory called math_utils.py
with these three functions:

  - gcd(a: int, b: int) -> int: greatest common divisor (euclid).
  - lcm(a: int, b: int) -> int: least common multiple.
  - is_prime(n: int) -> bool: primality test for n >= 0.

Then create test_math_utils.py with pytest tests covering each
function (at least two cases each including n=0 or n=1 edge cases).

Finally run `python -m pytest -x` in the current directory and show
the output. If tests fail fix them until they pass.
"@
  },
  @{
    role = 'util-datetime'
    spec = @"
Create a Python module in the current directory called datetime_utils.py
(use only Python stdlib — datetime module) with these three functions:

  - parse_iso(s: str) -> datetime: accept "YYYY-MM-DD" or
    "YYYY-MM-DDTHH:MM:SS" and return a datetime.
  - days_between(a: date|datetime, b: date|datetime) -> int: return
    abs(b - a).days.
  - is_weekend(d: date|datetime) -> bool: Sat/Sun check.

Then create test_datetime_utils.py with pytest tests covering each
function (at least two cases each).

Finally run `python -m pytest -x` in the current directory and show
the output. If tests fail fix them until they pass.
"@
  }
)

# --- 2. Spawn all three in rapid succession ---
$instances = @()
foreach ($t in $tasks) {
  $body = @{
    project_id           = $projID
    role_hint            = $t.role
    opencode_provider_id = $providerID
    opencode_model_id    = $modelID
  } | ConvertTo-Json
  Log "spawn $($t.role)"
  $r = Invoke-WebRequest -Uri "$baseURL/agentpool/spawn" -Method POST `
    -ContentType 'application/json' -Body $body `
    -Headers @{Authorization="Bearer $humanKey"} `
    -UseBasicParsing -TimeoutSec 90
  $d = ($r.Content | ConvertFrom-Json).data
  if (-not $d -or $d.status -ne 'ready') { Fail "spawn $($t.role) failed: $($r.Content)" }
  Ok "  $($t.role) instance=$($d.id) port=$($d.port) pid=$($d.pid) session=$($d.opencode_session_id)"
  $instances += @{
    role         = $t.role
    instance_id  = $d.id
    agent_id     = $d.agent_id
    port         = $d.port
    session_id   = $d.opencode_session_id
    working_dir  = $d.working_dir
    spec         = $t.spec
  }
}

# --- 3. Broadcast each task to its own agent ---
$i = 0
foreach ($inst in $instances) {
  $i++
  $msgID = "multi_e2e_$(Get-Random)"
  $payload = @{
    header  = @{ type = 'TASK_ASSIGN'; messageID = $msgID; timestamp = [long](Get-Date -UFormat %s); target = $inst.agent_id }
    payload = @{ task_id = "multi_task_$i"; description = $inst.spec }
  } | ConvertTo-Json -Depth 6 -Compress
  $payloadPath = "D:\claude-code\coai2\multi-payload-$i.json"
  # CRITICAL: PowerShell's -Encoding UTF8 writes a BOM (EF BB BF) which
  # Go's json.Unmarshal rejects — the consumer then silently drops the
  # event (FetchEvents skips unparseable entries and returns len=0).
  # Write raw UTF-8 without BOM via .NET.
  [System.IO.File]::WriteAllBytes($payloadPath, [System.Text.UTF8Encoding]::new($false).GetBytes($payload))
  docker cp $payloadPath "a3c-redis:/tmp/multi-payload-$i.json" | Out-Null
  docker exec a3c-redis sh -c "cat /tmp/multi-payload-$i.json | redis-cli -x RPUSH a3c:directed:$($inst.agent_id)" | Out-Null
  Log "pushed $($inst.role) / task_$i"
}

# --- 4. Wait for all three agents to finish their work ---
# "Finished" = assistant has written at least one text part AND the
# task file exists on disk. Timeout is generous because MiniMax-M2.7
# tends to think-long on code-writing tasks.
Log "waiting up to $replyWait s for all three agents to write files + reply..."
$deadline = (Get-Date).AddSeconds($replyWait)
$completed = @{}

while ((Get-Date) -lt $deadline -and $completed.Count -lt $instances.Count) {
  Start-Sleep 5
  foreach ($inst in $instances) {
    if ($completed.ContainsKey($inst.instance_id)) { continue }

    # Check on-disk artefacts (the real deliverable).
    $wd = $inst.working_dir
    $moduleName = switch ($inst.role) {
      'util-string'   { 'string_utils.py' }
      'util-math'     { 'math_utils.py' }
      'util-datetime' { 'datetime_utils.py' }
    }
    $testName = switch ($inst.role) {
      'util-string'   { 'test_string_utils.py' }
      'util-math'     { 'test_math_utils.py' }
      'util-datetime' { 'test_datetime_utils.py' }
    }
    $moduleOk = Test-Path (Join-Path $wd $moduleName)
    $testOk   = Test-Path (Join-Path $wd $testName)

    # Check assistant replied.
    $assistantOk = $false
    try {
      $msgs = (Invoke-WebRequest "http://127.0.0.1:$($inst.port)/session/$($inst.session_id)/message" -UseBasicParsing -TimeoutSec 5).Content | ConvertFrom-Json
      foreach ($m in $msgs) {
        if ($m.info.role -eq 'assistant' -and $m.parts.Count -gt 0) { $assistantOk = $true }
      }
    } catch {}

    if ($moduleOk -and $testOk -and $assistantOk) {
      $completed[$inst.instance_id] = $true
      Ok "$($inst.role) done — $moduleName + $testName on disk"
    }
  }
  $remaining = $instances.Count - $completed.Count
  if ($remaining -gt 0) {
    Write-Host "  … $remaining still working" -ForegroundColor DarkGray
  }
}

if ($completed.Count -lt $instances.Count) {
  Write-Host "[WARN] $($instances.Count - $completed.Count) agent(s) did not finish in $replyWait s" -ForegroundColor Yellow
}

# --- 5. Verify no cross-talk: each workdir should contain only its own module ---
Log "verifying workdir isolation"
$crossTalk = $false
foreach ($inst in $instances) {
  $wd = $inst.working_dir
  if (-not (Test-Path $wd)) { Write-Host "  [miss] $($inst.role) workdir absent"; continue }
  $pys = Get-ChildItem $wd -Filter '*.py' -ErrorAction SilentlyContinue | Where-Object { -not $_.PSIsContainer } | ForEach-Object { $_.Name }
  $ownPrefix = switch ($inst.role) {
    'util-string'   { 'string_utils' }
    'util-math'     { 'math_utils' }
    'util-datetime' { 'datetime_utils' }
  }
  foreach ($f in $pys) {
    if ($f -notmatch "^(test_)?$ownPrefix(\.py|_.*\.py)$") {
      Write-Host "  [crosstalk] $($inst.role) workdir contains $f (not its module!)" -ForegroundColor Red
      $crossTalk = $true
    }
  }
  Ok "$($inst.role): $($pys -join ', ')"
}
if ($crossTalk) { Fail "cross-talk detected between workdirs" }
Ok "all workdirs disjoint — no cross-talk"

# --- 6. Pytest verification: run each agent's tests locally in its workdir ---
Log "running each workdir's pytest from the host"
$pythonExe = if (Get-Command python -ErrorAction SilentlyContinue) { 'python' } else { 'py' }
$allPytestOk = $true
foreach ($inst in $instances) {
  $wd = $inst.working_dir
  if (-not (Test-Path $wd)) { $allPytestOk = $false; continue }
  $out = & $pythonExe -m pytest -x $wd 2>&1 | Out-String
  if ($LASTEXITCODE -eq 0) {
    Ok "$($inst.role) pytest PASS"
  } else {
    Write-Host "  [$($inst.role) pytest output]" -ForegroundColor Yellow
    $out -split "`n" | Select-Object -Last 15 | ForEach-Object { "    $_" }
    $allPytestOk = $false
  }
}

# --- 7. Metrics snapshot for each ---
Log "metric rings per instance"
foreach ($inst in $instances) {
  $m = (Invoke-WebRequest "$baseURL/agentpool/metrics/$($inst.instance_id)" -Headers @{Authorization="Bearer $humanKey"} -UseBasicParsing).Content | ConvertFrom-Json
  $evts = $m.data.events
  $tks = $m.data.tokens
  Write-Host ""
  Write-Host "  === $($inst.role) / $($inst.instance_id) ==="
  Write-Host "    events: $($evts.Count), tokens samples: $($tks.Count)"
  foreach ($e in $evts) {
    $ts = [DateTimeOffset]::FromUnixTimeMilliseconds($e.at_ms).LocalDateTime.ToString('HH:mm:ss')
    Write-Host "    $ts $($e.type.PadRight(14)) $($e.detail)"
  }
  if ($tks.Count -gt 0) {
    $last = $tks[-1]
    Write-Host "    last token sample: $($last.tokens) tokens"
  }
}

Write-Host ""
if ($completed.Count -eq $instances.Count -and -not $crossTalk -and $allPytestOk) {
  Write-Host "========== 3-AGENT PARALLEL E2E OK ==========" -ForegroundColor Green
} else {
  Write-Host "========== PARTIAL =========" -ForegroundColor Yellow
  Write-Host "  completed=$($completed.Count)/$($instances.Count) crossTalk=$crossTalk pytestOk=$allPytestOk"
}
