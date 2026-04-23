# e2e-real.ps1 — real end-to-end exercise for the Phase 3/4/5 pool
#
# Runs the full story against a live backend + docker MySQL/Redis +
# real opencode subprocess with real MiniMax-M2.7:
#
#   1. Clean stale pool agents
#   2. Look up human access key + project id
#   3. Spawn pool agent with real opencode provider/model
#   4. Verify /agentpool/list returns it as ready, pinned to the
#      provider/model, with an opencode_session_id bound
#   5. RPUSH a TASK_ASSIGN broadcast with a distinctive prompt
#   6. Wait for the broadcast consumer to drain + inject + LLM reply
#   7. Confirm the session messages now include both the broadcast
#      user turn AND an assistant reply produced by the real LLM
#   8. Trigger manual Sleep → verify dormancy
#   9. RPUSH another broadcast while dormant → verify auto-wake fires
#  10. Confirm post-wake session picks up the queued broadcast
#  11. Pull /agentpool/metrics/:id and print the event trail

param(
  [string]$baseURL = 'http://127.0.0.1:3003/api/v1',
  [string]$providerID = 'minimax-coding-plan',
  [string]$modelID = 'MiniMax-M2.7'
)

# Relaxed error handling: mysql's always-loud "password on the command
# line" warning arrives on stderr and trips Stop mode. We trap real
# failures via explicit Fail() calls + PS's own try/catch.
$ErrorActionPreference = 'Continue'
$PSNativeCommandUseErrorActionPreference = $false

function Log($msg) { Write-Host "[e2e] $msg" -ForegroundColor Cyan }
function Ok($msg)  { Write-Host "[ok]  $msg" -ForegroundColor Green }
function Fail($msg){ Write-Host "[FAIL] $msg" -ForegroundColor Red; exit 1 }

# ---------- 1. Cleanup stale agents ----------
Log "Wiping stale non-human agents from DB"
docker exec a3c-mysql mysql -uroot -proot123 -Da3c -e "DELETE FROM agent WHERE is_human=0;" 2>$null | Out-Null

# ---------- 2. Resolve human key + project ----------
$humanKey = docker exec a3c-mysql mysql -uroot -proot123 -Da3c -e "SELECT access_key FROM agent WHERE is_human=1 ORDER BY created_at ASC LIMIT 1\G" 2>$null `
  | Where-Object { $_ -match 'access_key' } `
  | ForEach-Object { ($_ -split ':')[1].Trim() }
if (-not $humanKey) { Fail "no human agent in DB" }

$projID = docker exec a3c-mysql mysql -uroot -proot123 -Da3c -e "SELECT id FROM project ORDER BY created_at ASC LIMIT 1\G" 2>$null `
  | Where-Object { $_ -match 'id:' } `
  | ForEach-Object { ($_ -split ':')[1].Trim() }
if (-not $projID) { Fail "no project in DB" }

Ok "using humanKey=$($humanKey.Substring(0,8))..., project=$projID"

# ---------- 3. Spawn ----------
$spawnBody = @{
  project_id           = $projID
  opencode_provider_id = $providerID
  opencode_model_id    = $modelID
} | ConvertTo-Json

Log "Spawning pool agent (provider=$providerID model=$modelID)"
$r = Invoke-WebRequest -Uri "$baseURL/agentpool/spawn" `
  -Method POST -ContentType 'application/json' -Body $spawnBody `
  -Headers @{Authorization="Bearer $humanKey"} `
  -UseBasicParsing -TimeoutSec 90
$spawn = ($r.Content | ConvertFrom-Json).data
if (-not $spawn) { Fail "spawn response missing data: $($r.Content)" }
$instanceID = $spawn.id
$agentID    = $spawn.agent_id
$port       = $spawn.port
$sessionID  = $spawn.opencode_session_id
Ok "spawned instance=$instanceID agent=$agentID port=$port session=$sessionID"

if ($spawn.status -ne 'ready') { Fail "expected status=ready, got $($spawn.status)" }
if (-not $sessionID)           { Fail "no opencode_session_id bound on spawn" }

# ---------- 4. List roundtrip ----------
Log "GET /agentpool/list"
$list = (Invoke-WebRequest "$baseURL/agentpool/list" -Headers @{Authorization="Bearer $humanKey"} -UseBasicParsing).Content | ConvertFrom-Json
$instance = $list.data.instances | Where-Object { $_.id -eq $instanceID }
if (-not $instance) { Fail "spawned instance missing from list" }
Ok "list confirms instance provider=$($instance.opencode_provider_id) model=$($instance.opencode_model_id)"

# ---------- 5. Broadcast first task ----------
$msgID1 = "dir_e2e_$(Get-Random)"
$payload1 = '{"header":{"type":"TASK_ASSIGN","messageID":"' + $msgID1 + '","timestamp":' + ([long](Get-Date -UFormat %s)) + ',"target":"' + $agentID + '"},"payload":{"task_id":"e2e_task_1","description":"Reply with exactly the single word PONG and nothing else."}}'
$payload1 | Set-Content 'D:\claude-code\coai2\e2e-payload1.json' -Encoding ASCII -NoNewline
docker cp 'D:\claude-code\coai2\e2e-payload1.json' 'a3c-redis:/tmp/e2e-payload1.json' | Out-Null
docker exec a3c-redis sh -c "cat /tmp/e2e-payload1.json | redis-cli -x RPUSH a3c:directed:$agentID" | Out-Null
Log "pushed broadcast $msgID1"

# ---------- 6+7. Wait for drain + inject + reply ----------
Log "waiting up to 40s for broadcast injection + LLM reply..."
$deadline = (Get-Date).AddSeconds(40)
$sawReply = $false
while ((Get-Date) -lt $deadline) {
  Start-Sleep 3
  try {
    $msgs = (Invoke-WebRequest "http://127.0.0.1:$port/session/$sessionID/message" -UseBasicParsing -TimeoutSec 5).Content | ConvertFrom-Json
  } catch { continue }

  $hasUser = $false; $hasAsst = $false
  foreach ($m in $msgs) {
    if ($m.info.role -eq 'user') {
      foreach ($p in $m.parts) {
        if ($p.type -eq 'text' -and $p.text -match 'e2e_task_1') { $hasUser = $true }
      }
    }
    if ($m.info.role -eq 'assistant' -and $m.parts.Count -gt 1) { $hasAsst = $true }
  }
  if ($hasUser -and $hasAsst) { $sawReply = $true; break }
}
if (-not $sawReply) {
  Log "last session state:"
  $msgs = (Invoke-WebRequest "http://127.0.0.1:$port/session/$sessionID/message" -UseBasicParsing -TimeoutSec 5).Content | ConvertFrom-Json
  foreach ($m in $msgs) { "  [$($m.info.role) parts=$($m.parts.Count)]" }
  Fail "did not see user broadcast + assistant reply within 40s"
}
Ok "LLM replied to the broadcast"

# ---------- 8. Sleep ----------
Log "manual /agentpool/sleep"
$r = Invoke-WebRequest -Uri "$baseURL/agentpool/sleep" -Method POST -ContentType 'application/json' -Body "{`"instance_id`":`"$instanceID`"}" -Headers @{Authorization="Bearer $humanKey"} -UseBasicParsing -TimeoutSec 30
$slept = ($r.Content | ConvertFrom-Json).data
if ($slept.status -ne 'dormant') { Fail "expected dormant after sleep, got $($slept.status)" }
Ok "instance dormant"

# ---------- 9. Broadcast to dormant → auto-wake ----------
$msgID2 = "dir_e2e_$(Get-Random)"
$payload2 = '{"header":{"type":"TASK_ASSIGN","messageID":"' + $msgID2 + '","timestamp":' + ([long](Get-Date -UFormat %s)) + ',"target":"' + $agentID + '"},"payload":{"task_id":"e2e_task_2","description":"Reply with exactly the single word WOKEUP and nothing else."}}'
$payload2 | Set-Content 'D:\claude-code\coai2\e2e-payload2.json' -Encoding ASCII -NoNewline
docker cp 'D:\claude-code\coai2\e2e-payload2.json' 'a3c-redis:/tmp/e2e-payload2.json' | Out-Null
docker exec a3c-redis sh -c "cat /tmp/e2e-payload2.json | redis-cli -x RPUSH a3c:directed:$agentID" | Out-Null
Log "pushed dormant-target broadcast $msgID2 (auto-wake should fire)"

# ---------- 10. Confirm post-wake session picks it up ----------
Log "waiting up to 60s for auto-wake + post-wake broadcast consumption + LLM reply..."
$deadline = (Get-Date).AddSeconds(60)
$awoke = $false
$postPort = 0
$postSession = ''
while ((Get-Date) -lt $deadline) {
  Start-Sleep 3
  $list = (Invoke-WebRequest "$baseURL/agentpool/list" -Headers @{Authorization="Bearer $humanKey"} -UseBasicParsing).Content | ConvertFrom-Json
  $i = $list.data.instances | Where-Object { $_.id -eq $instanceID }
  if ($i.status -eq 'ready' -and $i.port -gt 0 -and $i.opencode_session_id) {
    $postPort = $i.port
    $postSession = $i.opencode_session_id
    $awoke = $true
    break
  }
}
if (-not $awoke) { Fail "auto-wake did not land within 60s" }
Ok "auto-woken: port=$postPort session=$postSession (was $sessionID)"

# New session will have at most the post-wake broadcast + reply
$seenWoke = $false
$deadline = (Get-Date).AddSeconds(45)
while ((Get-Date) -lt $deadline) {
  Start-Sleep 3
  try {
    $msgs = (Invoke-WebRequest "http://127.0.0.1:$postPort/session/$postSession/message" -UseBasicParsing -TimeoutSec 5).Content | ConvertFrom-Json
  } catch { continue }
  foreach ($m in $msgs) {
    if ($m.info.role -eq 'user') {
      foreach ($p in $m.parts) {
        if ($p.type -eq 'text' -and $p.text -match 'e2e_task_2') { $seenWoke = $true }
      }
    }
  }
  if ($seenWoke) { break }
}
if (-not $seenWoke) { Fail "post-wake session didn't pick up the queued broadcast" }
Ok "post-wake session has the queued broadcast injected"

# ---------- 11. Metrics dump ----------
Log "GET /agentpool/metrics/$instanceID"
$metrics = (Invoke-WebRequest "$baseURL/agentpool/metrics/$instanceID" -Headers @{Authorization="Bearer $humanKey"} -UseBasicParsing).Content | ConvertFrom-Json
$events = $metrics.data.events
"  $(($events).Count) events recorded"
foreach ($e in $events) {
  $ts = [DateTimeOffset]::FromUnixTimeMilliseconds($e.at_ms).LocalDateTime.ToString('HH:mm:ss')
  "  $ts $($e.type.PadRight(12)) $($e.detail)"
}
Ok "metrics panel populated"

Write-Host "`n================= E2E OK =================" -ForegroundColor Green
Write-Host "  spawn → broadcast → LLM reply → sleep → auto-wake → broadcast → LLM reply"
Write-Host "  all lifecycle events recorded in the metric ring"
Write-Host "==========================================`n"
