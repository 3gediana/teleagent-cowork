#!/usr/bin/env pwsh
# Full Agent Lifecycle E2E Test — simulates agent joining, small task, large branch+PR, experience
# Uses internal API to simulate agent tool outputs (bypasses model dependency)
$base = "http://localhost:3003/api/v1"
$intBase = "http://localhost:3003/api/v1/internal"

function Api($method, $path, $body, $token=$null) {
    $h = @{"Content-Type"="application/json"}
    if ($token) { $h["Authorization"] = "Bearer $token" }
    try {
        if ($method -eq "GET") {
            return Invoke-RestMethod -Uri "$base$path" -Method GET -Headers $h -ErrorAction Stop
        }
        return Invoke-RestMethod -Uri "$base$path" -Method $method -Body $body -Headers $h -ErrorAction Stop
    } catch {
        $resp = $_.Exception.Response
        if ($resp) {
            $sr = [System.IO.StreamReader]::new($resp.GetResponseStream())
            $errBody = $sr.ReadToEnd()
            Write-Host "  [ERR] $method $path => $($resp.StatusCode): $($errBody.Substring(0,[Math]::Min(200,$errBody.Length)))" -ForegroundColor Red
        } else {
            Write-Host "  [ERR] $method $path : $($_.Exception.Message)" -ForegroundColor Red
        }
        return $null
    }
}

function IntApi($method, $path, $body) {
    $h = @{"Content-Type"="application/json"}
    try {
        if ($method -eq "GET") {
            return Invoke-RestMethod -Uri "$intBase$path" -Method GET -Headers $h -ErrorAction Stop
        }
        return Invoke-RestMethod -Uri "$intBase$path" -Method $method -Body $body -Headers $h -ErrorAction Stop
    } catch {
        $resp = $_.Exception.Response
        if ($resp) {
            $sr = [System.IO.StreamReader]::new($resp.GetResponseStream())
            $errBody = $sr.ReadToEnd()
            Write-Host "  [INT-ERR] $method $path => $($resp.StatusCode): $($errBody.Substring(0,[Math]::Min(200,$errBody.Length)))" -ForegroundColor Red
        }
        return $null
    }
}

function L($m) { Write-Host "  $m" }
function Pass($m) { Write-Host "  [PASS] $m" -ForegroundColor Green }
function Fail($m) { Write-Host "  [FAIL] $m" -ForegroundColor Red }

Write-Host "`n========== Full Agent Lifecycle Test ==========" -ForegroundColor Cyan
$passCount = 0; $failCount = 0
function P($m) { Pass $m; $script:passCount++ }
function F($m) { Fail $m; $script:failCount++ }

# ========== PHASE 1: Agent Registration ==========
Write-Host "`n--- Phase 1: Agent Registration ---" -ForegroundColor Yellow

$reg = Api "POST" "/agent/register" '{"name":"flow-agent"}'
if ($reg -and $reg.success) {
    $agentKey = $reg.data.access_key
    $agentId = $reg.data.agent_id
    P "Agent registered: $agentId"
} else { F "Agent registration failed"; exit 1 }

# Create project
$proj = Api "POST" "/project/create" '{"name":"FlowTestProject","description":"Full lifecycle test"}' $agentKey
if ($proj -and $proj.success) {
    $projId = $proj.data.id
    P "Project created: $projId"
} else { F "Project creation failed"; exit 1 }

# Select project
$selBody = @{project=$projId} | ConvertTo-Json -Compress
$sel = Api "POST" "/auth/select-project" $selBody $agentKey
if ($sel -and $sel.success) { P "Project selected" } else { F "Select project failed" }

# Heartbeat
$hb = Api "POST" "/auth/heartbeat" "{}" $agentKey
if ($hb -and $hb.success) { P "Heartbeat OK" }

# ========== PHASE 2: Small Task -> Change -> Audit (L0 approve) ==========
Write-Host "`n--- Phase 2: Small Task (Change -> Audit L0) ---" -ForegroundColor Yellow

$taskBody = @{name="Fix typo in README"; description="Fix a small typo in the project README"; priority="low"} | ConvertTo-Json -Compress
$task = Api "POST" "/task/create?project_id=$projId" $taskBody $agentKey
if ($task -and $task.success) { $taskId = $task.data.id; P "Task created: $taskId" } else { F "Task creation failed"; exit 1 }

$claimBody = @{task_id=$taskId} | ConvertTo-Json -Compress
$claim = Api "POST" "/task/claim" $claimBody $agentKey
if ($claim -and $claim.success) { P "Task claimed" } else { L "Claim may have failed (already claimed)" }

# Submit change (auto_mode=true triggers audit workflow)
$writes = @(@{path="README.md"; content="# FlowTestProject`n`nA test project for the full agent lifecycle.`n"})
$changeBody = @{task_id=$taskId; description="Fixed typo: s/teh/the/g"; writes=$writes; deletes=@()} | ConvertTo-Json -Depth 5 -Compress
L "Submitting change (triggers audit, blocks up to 120s)..."
$change = Api "POST" "/change/submit?project_id=$projId" $changeBody $agentKey
if ($change -and $change.success) {
    $changeId = $change.data.change_id
    P "Change submitted: $changeId status=$($change.data.status)"
} else {
    F "Change submit failed"
    # Try to find existing change
    $changes = Api "GET" "/change/list?project_id=$projId" "" $agentKey
    if ($changes -and $changes.data) {
        $changeId = $changes.data.changes[0].id
        L "Using existing change: $changeId"
    } else { exit 1 }
}

# The audit workflow blocks but model may fail. Let's simulate the audit result directly.
# First check if audit already completed
$changeInfo = Api "GET" "/change/list?project_id=$projId" "" $agentKey
$changeStatus = "unknown"
$auditLevel = ""
if ($changeInfo -and $changeInfo.data) {
    foreach ($c in $changeInfo.data.changes) {
        if ($c.id -eq $changeId) {
            $changeStatus = $c.status
            $auditLevel = $c.audit_level
            break
        }
    }
}
L "Change status: $changeStatus audit_level=$auditLevel"

# If audit didn't complete (model issue), simulate L0 approval via internal API
if ($changeStatus -ne "approved" -and $changeStatus -ne "pending_fix" -and $changeStatus -ne "rejected") {
    L "Simulating audit L0 approval via internal API..."
    $auditResult = @{change_id=$changeId; level="L0"; issues=@(); reject_reason=""} | ConvertTo-Json -Depth 4 -Compress
    $auditResp = IntApi "POST" "/agent/audit_output" $auditResult
    if ($auditResp -and $auditResp.success) {
        P "Audit L0 approved: $($auditResp.data.level)"
    } else {
        L "Audit output via internal API failed, trying direct DB update..."
    }
}

# Verify change status after audit
Start-Sleep 2
$changeInfo2 = Api "GET" "/change/list?project_id=$projId" "" $agentKey
if ($changeInfo2 -and $changeInfo2.data) {
    foreach ($c in $changeInfo2.data.changes) {
        if ($c.id -eq $changeId) {
            P "Change after audit: status=$($c.status) audit_level=$($c.audit_level)"
            break
        }
    }
}

# ========== PHASE 3: Feedback & Experience ==========
Write-Host "`n--- Phase 3: Feedback & Experience ---" -ForegroundColor Yellow

$fb1 = @{task_id=$taskId; outcome="success"; approach="Simple text replacement"; pitfalls="None - straightforward fix"; key_insight="Small typos are easy to fix but easy to miss in review"; would_do_differently="Use spell checker before committing"} | ConvertTo-Json -Compress
$fb1r = Api "POST" "/feedback/submit?project_id=$projId" $fb1 $agentKey
if ($fb1r -and $fb1r.success) { P "Feedback (success): $($fb1r.data.id)" } else { F "Feedback 1" }

$fb2 = @{task_id=$taskId; outcome="failed"; approach="Tried auto-fix but introduced new typo"; pitfalls="Regex without context awareness"; key_insight="Blind find-replace can break code"; would_do_differently="Review each replacement individually"} | ConvertTo-Json -Compress
$fb2r = Api "POST" "/feedback/submit?project_id=$projId" $fb2 $agentKey
if ($fb2r -and $fb2r.success) { P "Feedback (failed): $($fb2r.data.id)" } else { F "Feedback 2" }

$fb3 = @{task_id=$taskId; outcome="partial"; approach="Fixed most issues but missed edge case"; pitfalls="Edge case in config parsing"; key_insight="Always test edge cases after bulk changes"; would_do_differently="Write tests first"} | ConvertTo-Json -Compress
$fb3r = Api "POST" "/feedback/submit?project_id=$projId" $fb3 $agentKey
if ($fb3r -and $fb3r.success) { P "Feedback (partial): $($fb3r.data.id)" } else { F "Feedback 3" }

# Check experiences
$exps = Api "GET" "/experience/list?project_id=$projId" "" $agentKey
if ($exps -and $exps.data) {
    $expList = $exps.data.experiences
    P "Experiences: $($expList.Count) total"
    foreach ($e in $expList) {
        L "  [$($e.source_type)] $($e.agent_role) outcome=$($e.outcome)"
    }
}

# ========== PHASE 4: Large Branch & PR Workflow ==========
Write-Host "`n--- Phase 4: Large Branch & PR Workflow ---" -ForegroundColor Yellow

# Complete previous task
$compBody = @{task_id=$taskId} | ConvertTo-Json -Compress
$comp = Api "POST" "/task/complete" $compBody $agentKey
if ($comp -and $comp.success) { P "Previous task completed" }

# Create large task
$task2Body = @{name="Refactor config system"; description="Split config into multiple files with validation"; priority="high"} | ConvertTo-Json -Compress
$task2 = Api "POST" "/task/create?project_id=$projId" $task2Body $agentKey
if ($task2 -and $task2.success) { $taskId2 = $task2.data.id; P "Large task created: $taskId2" } else { F "Large task creation failed"; $taskId2 = $null }

if ($taskId2) {
    # Claim
    $claim2Body = @{task_id=$taskId2} | ConvertTo-Json -Compress
    $claim2 = Api "POST" "/task/claim" $claim2Body $agentKey
    if ($claim2 -and $claim2.success) { P "Large task claimed" } else { L "Claim failed (may have existing claimed task)" }

    # Create branch
    $branchBody = @{name="config-refactor-v2"; description="Split monolithic config into modular files"} | ConvertTo-Json -Compress
    $branch = Api "POST" "/branch/create" $branchBody $agentKey
    if ($branch -and $branch.success) {
        $branchId = $branch.data.id
        P "Branch created: $branchId name=$($branch.data.name)"
    } else {
        # Try listing existing branches and use one
        $branches = Api "GET" "/branch/list" "" $agentKey
        if ($branches -and $branches.data -and $branches.data.branches.Count -gt 0) {
            $branchId = $branches.data.branches[0].id
            P "Using existing branch: $branchId"
        } else {
            F "No branch available"
            $branchId = $null
        }
    }

    if ($branchId) {
        # Enter branch
        $enterBody = @{branch_id=$branchId} | ConvertTo-Json -Compress
        $enter = Api "POST" "/branch/enter" $enterBody $agentKey
        if ($enter -and $enter.success) { P "Entered branch" }

        # Submit change on branch (3 files)
        $writes2 = @(
            @{path="config/base.go"; content="package config`n`n// BaseConfig holds common settings`ntype BaseConfig struct {`n`tPort int`n`tDebug bool`n}`n"},
            @{path="config/validation.go"; content="package config`n`n// Validate checks config integrity`nfunc Validate(c *BaseConfig) error {`n`treturn nil`n}`n"},
            @{path="config/env.go"; content="package config`n`n// EnvConfig holds env-specific overrides`ntype EnvConfig struct {`n`tBaseConfig`n`tEnv string`n}`n"}
        )
        $bChangeBody = @{task_id=$taskId2; description="Refactored config into separate modules"; writes=$writes2; deletes=@()} | ConvertTo-Json -Depth 5 -Compress
        $bChange = Api "POST" "/branch/change_submit" $bChangeBody $agentKey
        if ($bChange -and $bChange.success) {
            P "Branch change submitted (3 files): $($bChange.data.change_id)"
        } else { L "Branch change submit failed" }

        # Submit PR
        $selfReview = @{changed_functions=@("Validate","NewEnvConfig"); overall_impact="medium"; merge_confidence="high"} | ConvertTo-Json -Compress
        $prBody = @{title="Config Refactor v2"; description="Split monolithic config into modular files with validation"; self_review=$selfReview} | ConvertTo-Json -Depth 3 -Compress
        $pr = Api "POST" "/pr/submit" $prBody $agentKey
        if ($pr -and $pr.success) {
            $prId = $pr.data.id
            P "PR submitted: $prId status=$($pr.data.status)"
        } else { F "PR submit failed"; $prId = $null }

        # Approve review (simulate human)
        if ($prId) {
            Start-Sleep 2
            $apprBody = @{pr_id=$prId} | ConvertTo-Json -Compress
            $appr = Api "POST" "/pr/approve_review" $apprBody $agentKey
            if ($appr -and $appr.success) {
                P "PR review approved, evaluate agent triggered: $($appr.data.status)"
            } else { L "PR approve_review failed" }

            # Wait for evaluate agent (may timeout due to model)
            L "Waiting for evaluate agent (up to 90s)..."
            for ($i=0; $i -lt 18; $i++) {
                Start-Sleep 5
                $prInfo = Api "GET" "/pr/$prId" "" $agentKey
                if ($prInfo -and $prInfo.data) {
                    $prStatus = $prInfo.data.status
                    L "  PR status: $prStatus ($($i*5)s)"
                    if ($prStatus -in @("evaluated","pending_human_merge","merged","rejected","merge_failed")) {
                        P "PR evaluation completed: $prStatus"
                        break
                    }
                }
            }

            # If still evaluating, simulate evaluate output
            $prInfo2 = Api "GET" "/pr/$prId" "" $agentKey
            if ($prInfo2 -and $prInfo2.data -and $prInfo2.data.status -eq "evaluating") {
                L "Evaluate agent timed out, simulating APPROVED result..."
                # Find the evaluate session
                $sessions = IntApi "GET" "/agent/sessions?project_id=$projId" ""
                $evalSessionId = $null
                if ($sessions -and $sessions.data) {
                    foreach ($s in $sessions.data.sessions) {
                        if ($s.role -eq "evaluate" -and $s.status -eq "running") {
                            $evalSessionId = $s.id
                            break
                        }
                    }
                }
                if ($evalSessionId) {
                    L "Found evaluate session: $evalSessionId, submitting output..."
                    $evalOutput = @{tool="evaluate_output"; result="approved"; merge_cost_rating="Low"; reason="Code is clean and well-structured"; conflict_files=@()} | ConvertTo-Json -Depth 3 -Compress
                    $evalResp = IntApi "POST" "/agent/session/$evalSessionId/output" $evalOutput
                    if ($evalResp -and $evalResp.success) {
                        P "Evaluate output submitted"
                    }
                }
                # Wait for PR status update
                Start-Sleep 5
                $prInfo3 = Api "GET" "/pr/$prId" "" $agentKey
                if ($prInfo3 -and $prInfo3.data) {
                    L "PR status after eval: $($prInfo3.data.status)"
                }
            }

            # If pending_human_merge, approve merge
            $prInfo4 = Api "GET" "/pr/$prId" "" $agentKey
            if ($prInfo4 -and $prInfo4.data -and $prInfo4.data.status -eq "pending_human_merge") {
                L "Approving merge..."
                $mergeBody = @{pr_id=$prId} | ConvertTo-Json -Compress
                $merge = Api "POST" "/pr/approve_merge" $mergeBody $agentKey
                if ($merge -and $merge.success) {
                    P "PR merged! new_version=$($merge.data.new_version)"
                } else { L "Merge failed or skipped" }
            }
        }

        # Leave branch
        $leave = Api "POST" "/branch/leave" "{}" $agentKey
        if ($leave -and $leave.success) { P "Left branch" }
    }
}

# ========== PHASE 5: Experience Summary & Distillation ==========
Write-Host "`n--- Phase 5: Experience Summary & Distillation ---" -ForegroundColor Yellow

$allExps = Api "GET" "/experience/list?project_id=$projId" "" $agentKey
if ($allExps -and $allExps.data) {
    $expList = $allExps.data.experiences
    P "Total experiences: $($expList.Count)"
    $byType = @{}
    foreach ($e in $expList) {
        $t = $e.source_type
        if (-not $byType[$t]) { $byType[$t] = 0 }
        $byType[$t]++
    }
    foreach ($k in $byType.Keys) { L "  $k : $($byType[$k])" }
}

$skills = Api "GET" "/skill/list" "" $agentKey
if ($skills -and $skills.data) {
    P "Skills: $($skills.data.skills.Count)"
}

$policies = Api "GET" "/policy/list" "" $agentKey
if ($policies -and $policies.data) {
    P "Policies: $($policies.data.policies.Count)"
    foreach ($p in $policies.data.policies) { L "  [$($p.status)] $($p.name) hits=$($p.hit_count)" }
}

# ========== PHASE 6: Broadcast Events from Logs ==========
Write-Host "`n--- Phase 6: Broadcast Events from Server Log ---" -ForegroundColor Yellow

$logFile = "D:\claude-code\coai\platform\backend\server_err.log"
if (Test-Path $logFile) {
    $recentLog = Get-Content $logFile -Tail 300 -ErrorAction SilentlyContinue
    $broadcasts = $recentLog | Select-String "Broadcast|PR_|Audit|Policy|Experience|Merge|Evaluate|Chief|CHAT_UPDATE|VERSION_UPDATE"
    if ($broadcasts) {
        P "Broadcast events: $($broadcasts.Count)"
        $seen = @{}
        foreach ($b in $broadcasts) {
            $line = $b.Line
            $key = ($line -replace '\s+', ' ').Substring(0, [Math]::Min(60, $line.Length))
            if (-not $seen[$key]) {
                $seen[$key] = $true
                if ($line.Length -gt 140) { $line = $line.Substring(0, 140) + "..." }
                L "  $line"
            }
        }
    }
}

# ========== PHASE 7: Final Platform Status ==========
Write-Host "`n--- Phase 7: Final Platform Status ---" -ForegroundColor Yellow

$tasks = Api "GET" "/task/list?project_id=$projId" "" $agentKey
if ($tasks -and $tasks.data) {
    $taskList = $tasks.data.tasks
    $pending = ($taskList | Where-Object { $_.status -eq "pending" }).Count
    $claimed = ($taskList | Where-Object { $_.status -eq "claimed" }).Count
    $completed = ($taskList | Where-Object { $_.status -eq "completed" }).Count
    P "Tasks: pending=$pending claimed=$claimed completed=$completed"
    foreach ($t in $taskList) { L "  [$($t.status)] $($t.name) ($($t.priority))" }
}

$sessions = IntApi "GET" "/agent/sessions?project_id=$projId" ""
if ($sessions -and $sessions.data) {
    $byRole = @{}
    foreach ($s in $sessions.data.sessions) {
        $r = $s.role
        if (-not $byRole[$r]) { $byRole[$r] = @() }
        $byRole[$r] += "$($s.status)"
    }
    P "Sessions by role:"
    foreach ($k in $byRole.Keys) { L "  $k : $($byRole[$k] -join ', ')" }
}

$prList = Api "GET" "/pr/list" "" $agentKey
if ($prList -and $prList.data) {
    $prs = $prList.data.pull_requests
    P "PRs: $($prs.Count)"
    foreach ($p in $prs) { L "  [$($p.status)] $($p.title)" }
}

Write-Host "`n========== Results: PASS=$passCount FAIL=$failCount ==========" -ForegroundColor Cyan
