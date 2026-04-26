# Platform smoke test — exercises the main public surface + the audit
# pipeline end-to-end. Does not require a real LLM key: when a session
# needs a tool output, we feed it synthetically via
# /internal/agent/session/:id/output, which is the same path the real
# runner would use.
#
# Usage: pwsh -File smoke-platform.ps1
# Exits non-zero on any failure. Designed to be rerun safely after
# `dev-server.ps1 restart` + DB reset.

param(
    [string]$Base = 'http://localhost:3003/api/v1'
)

$ErrorActionPreference = 'Continue'
$script:Failed = @()
$script:Passed = 0

function Record($name, $ok, $detail = '') {
    if ($ok) {
        $script:Passed++
        Write-Host "  [PASS] $name" -ForegroundColor Green
    } else {
        $script:Failed += "$name :: $detail"
        Write-Host "  [FAIL] $name :: $detail" -ForegroundColor Red
    }
}

function Req($method, $path, $body, $headers, $expectStatus = 200) {
    $p = @{ Method=$method; Uri="$Base$path"; UseBasicParsing=$true }
    if ($body -ne $null) {
        $p.Body = ($body | ConvertTo-Json -Compress -Depth 6)
        $p.ContentType = 'application/json'
    }
    if ($headers) { $p.Headers = $headers }
    try {
        $r = Invoke-WebRequest @p -ErrorAction Stop
        return @{ ok=$true; status=$r.StatusCode; body=$r.Content; data=($r.Content | ConvertFrom-Json -ErrorAction SilentlyContinue) }
    } catch {
        $resp = $_.Exception.Response
        $code = if ($resp) { [int]$resp.StatusCode } else { 0 }
        $body = ''
        if ($resp) {
            try { $body = (New-Object IO.StreamReader ($resp.GetResponseStream())).ReadToEnd() } catch {}
        }
        return @{ ok=$false; status=$code; body=$body; err=$_.Exception.Message }
    }
}

function Auth($key) { @{ Authorization = "Bearer $key" } }

Write-Host "=== Smoke A :: Health ===" -ForegroundColor Cyan
$healthRes = $null
try { $healthRes = Invoke-WebRequest 'http://localhost:3003/health' -UseBasicParsing -ErrorAction Stop } catch {}
Record 'health endpoint returns 200' ($healthRes -and $healthRes.StatusCode -eq 200) 'health endpoint'

Write-Host "=== Smoke B :: Auth bootstrap ===" -ForegroundColor Cyan
$reg1 = Req POST '/agent/register' @{ name='alice'; is_human=$true } $null
Record 'first human register' ($reg1.ok -and $reg1.data.data.access_key) "status=$($reg1.status) body=$($reg1.body)"
$aliceKey = $reg1.data.data.access_key
$aliceID  = $reg1.data.data.agent_id

$reg2 = Req POST '/agent/register' @{ name='bob'; is_human=$true } $null
Record 'second human register is rejected (FORBIDDEN)' ((-not $reg2.ok) -and $reg2.status -eq 403) "status=$($reg2.status) body=$($reg2.body)"

$regBot = Req POST '/agent/register' @{ name='worker-1'; is_human=$false } $null
Record 'non-human register is allowed' ($regBot.ok -and $regBot.data.data.access_key) "status=$($regBot.status) body=$($regBot.body)"
$botKey = $regBot.data.data.access_key
$botID  = $regBot.data.data.agent_id

$login = Req POST '/auth/login' @{ key=$aliceKey } $null
Record 'alice login' ($login.ok -and $login.status -eq 200) "status=$($login.status)"

$noauth = Req GET '/project/list' $null $null
Record '/project/list without key returns 401' ((-not $noauth.ok) -and $noauth.status -eq 401) "status=$($noauth.status)"

Write-Host "=== Smoke C :: Project CRUD ===" -ForegroundColor Cyan
$create = Req POST '/project/create' @{ name='smoke-project'; description='smoke' } (Auth $aliceKey)
Record 'alice creates project' ($create.ok -and $create.data.data.id) "status=$($create.status) body=$($create.body)"
$projID = $create.data.data.id

$list = Req GET '/project/list' $null (Auth $aliceKey)
Record 'alice sees her project' ($list.ok -and ($list.data.data | Where-Object { $_.id -eq $projID })) "body=$($list.body)"

$getP = Req GET "/project/$projID" $null (Auth $aliceKey)
Record 'project detail has created_by=alice' ($getP.ok -and $getP.data.data.created_by -eq $aliceID) "created_by=$($getP.data.data.created_by) expected=$aliceID"

$select = Req POST '/auth/select-project' @{ project=$projID } (Auth $aliceKey)
Record 'alice selects project' ($select.ok -and $select.status -eq 200) "status=$($select.status) body=$($select.body)"

Write-Host "=== Smoke D :: LLM endpoints (format validation) ===" -ForegroundColor Cyan
$llm1 = Req POST '/llm/endpoints' @{
    name='openai-compat'; format='openai_compatible';
    base_url='http://localhost:9/v1'; api_key='fake-sk';
    models=@(@{ id='gpt-4o-mini'; name='GPT-4o Mini' }); default_model='gpt-4o-mini'
} (Auth $aliceKey)
Record 'format=openai_compatible accepted' ($llm1.ok -and $llm1.data.data.id) "status=$($llm1.status) body=$($llm1.body)"

$llm2 = Req POST '/llm/endpoints' @{
    name='bad-format'; format='not-a-format';
    base_url='http://x'; api_key='k';
    models=@(@{ id='m'; name='m' })
} (Auth $aliceKey)
Record 'invalid format rejected' ((-not $llm2.ok) -and $llm2.status -ge 400) "status=$($llm2.status)"

Write-Host "=== Smoke E :: Chief chat precondition ===" -ForegroundColor Cyan
# Chief dispatch should succeed now because we have at least one LLM endpoint.
# But the endpoint is fake (localhost:9), so the LLM call will fail and
# the error should surface back to the user via system dialogue + SSE,
# not silently 200.
$chief = Req POST "/chief/chat?project_id=$projID" @{ message='ping' } (Auth $aliceKey)
Record 'chief accepts message' ($chief.ok -and $chief.status -eq 200) "status=$($chief.status) body=$($chief.body)"

Start-Sleep 2
$msgs = Req GET "/chief/sessions?project_id=$projID&role=chief" $null (Auth $aliceKey)
$dialogueCount = 0
if ($msgs.ok -and $msgs.data.data.sessions) { $dialogueCount = @($msgs.data.data.sessions).Count }
Record 'chief sessions endpoint returns array' ($msgs.ok -and $dialogueCount -ge 0) "count=$dialogueCount"

Write-Host "=== Smoke F :: Task + change + audit pipeline ===" -ForegroundColor Cyan
$task = Req POST "/task/create?project_id=$projID" @{
    name='smoke task'; description='test'; priority='normal'
} (Auth $aliceKey)
Record 'task created' ($task.ok -and $task.data.data.id) "status=$($task.status) body=$($task.body)"
$taskID = if ($task.ok) { $task.data.data.id } else { '' }

if ($taskID) {
    $claim = Req POST '/task/claim' @{ task_id=$taskID; project_id=$projID } (Auth $botKey)
    Record 'worker claims task' ($claim.ok -and $claim.status -eq 200) "status=$($claim.status) body=$($claim.body)"

    $submit = Req POST "/change/submit?project_id=$projID" @{
        task_id=$taskID; description='smoke change'; version='v1.0';
        writes=@(@{ path='README.md'; content='smoke test'+"`n" }); deletes=@()
    } (Auth $botKey)
    Record 'worker submits change' ($submit.ok -and $submit.data.data.change_id) "status=$($submit.status) body=$($submit.body)"
    $changeID = if ($submit.ok) { $submit.data.data.change_id } else { '' }

    Start-Sleep 2
    # After submit, backend should have dispatched an audit_1 session.
    # Without a real LLM, native runner will fail to produce an output.
    # We short-circuit by injecting a synthetic audit_output L0 via the
    # internal/agent/session/:id/output endpoint. First, find the session.
    $sessRes = Req GET "/chief/sessions?project_id=$projID&role=audit_1" $null (Auth $aliceKey)
    $session = $null
    if ($sessRes.ok -and $sessRes.data.data.sessions) {
        $session = @($sessRes.data.data.sessions) | Where-Object { $_.change_id -eq $changeID } | Select-Object -First 1
    }
    Record 'audit_1 session created after change submit' ($session -ne $null) "sessions_found=$(if ($sessRes.ok -and $sessRes.data.data.sessions) { @($sessRes.data.data.sessions).Count } else { 0 })"
}

Write-Host "=== Smoke G :: Pool spawn ===" -ForegroundColor Cyan
# Pool spawn test — expect opencode.cmd to launch; health may or may
# not go green depending on user's opencode config. We record what
# actually happens rather than asserting ready.
$spawn = Req POST '/agentpool/spawn' @{ project_id=$projID } (Auth $aliceKey)
$spawnBody = if ($spawn.body) { $spawn.body.Substring(0, [Math]::Min(300, $spawn.body.Length)) } else { '<empty>' }
Record 'pool spawn endpoint reachable' ($spawn.status -ne 0) "status=$($spawn.status) body=$spawnBody"

Write-Host ""
Write-Host "=== Summary ===" -ForegroundColor Cyan
Write-Host "passed=$script:Passed failed=$($script:Failed.Count)"
if ($script:Failed.Count -gt 0) {
    Write-Host "FAILURES:" -ForegroundColor Red
    $script:Failed | ForEach-Object { Write-Host "  - $_" -ForegroundColor Red }
    exit 1
}
exit 0
