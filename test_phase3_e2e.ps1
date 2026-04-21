# Phase 3 E2E Test Script
# Tests Phase 3A (Chief, Heartbeat, Retry, FailureMode) + Phase 3B (Experience, Analyze, Skill, PolicyEngine)

$BASE = "http://localhost:3003/api/v1"
$PASS = 0
$FAIL = 0
$RESULTS = @()

function Test-Endpoint {
    param([string]$Name, [string]$Method, [string]$Path, [object]$Body, [int]$ExpectStatus, [string]$CheckField)
    
    $url = "$BASE$Path"
    try {
        if ($Method -eq "GET") {
            $resp = Invoke-RestMethod -Uri $url -Method GET -Headers $headers -ErrorAction Stop
        } else {
            $resp = Invoke-RestMethod -Uri $url -Method POST -Headers $headers -Body ($Body | ConvertTo-Json -Depth 10) -ContentType "application/json" -ErrorAction Stop
        }
        
        $ok = $true
        if ($CheckField) {
            $val = $resp
            foreach ($part in $CheckField.Split('.')) {
                $val = $val.$part
            }
            if (-not $val) { $ok = $false }
        }
        
        if ($ok) {
            $script:PASS++
            $script:RESULTS += "[PASS] $Name"
        } else {
            $script:FAIL++
            $script:RESULTS += "[FAIL] $Name (check field '$CheckField' missing)"
        }
        return $resp
    } catch {
        $script:FAIL++
        $script:RESULTS += "[FAIL] $Name ($($_.Exception.Message))"
        return $null
    }
}

Write-Host "`n========== Phase 3 E2E Test ==========" -ForegroundColor Cyan

# Step 1: Register an agent and login
Write-Host "`n--- Step 1: Auth ---" -ForegroundColor Yellow
$regResp = Test-Endpoint "Agent Register" "POST" "/agent/register" @{ name = "test_e2e_agent"; description = "E2E test agent" } 200 "success"
if (-not $regResp) {
    Write-Host "Cannot register, aborting" -ForegroundColor Red
    exit 1
}

# Extract access_key from response
$accessKey = ""
if ($regResp.data -is [System.Collections.IDictionary]) {
    $accessKey = $regResp.data["access_key"]
} elseif ($regResp.data.access_key) {
    $accessKey = $regResp.data.access_key
}
if (-not $accessKey) {
    Write-Host "No access_key in register response, aborting" -ForegroundColor Red
    Write-Host "Response: $($regResp | ConvertTo-Json -Depth 5)" -ForegroundColor DarkGray
    exit 1
}
Write-Host "  Access key: $accessKey" -ForegroundColor DarkGray

# Auth uses access_key as Bearer token
$headers = @{ Authorization = "Bearer $accessKey" }

# Logout first in case agent is already online
try { Invoke-RestMethod -Uri "$BASE/auth/logout" -Method POST -Headers $headers -Body (@{key=$accessKey} | ConvertTo-Json -Compress) -ContentType "application/json" -ErrorAction SilentlyContinue | Out-Null } catch {}

# Login to set agent online
$loginResp = Test-Endpoint "Agent Login" "POST" "/auth/login" @{ key = $accessKey; project = "" } 200 "success"
if (-not $loginResp) {
    Write-Host "Cannot login, aborting" -ForegroundColor Red
    exit 1
}

# Auth uses access_key as Bearer token (already set above)
Write-Host "  Auth header set" -ForegroundColor DarkGray

# Step 2: Create project
Write-Host "`n--- Step 2: Project Setup ---" -ForegroundColor Yellow
$projResp = Test-Endpoint "Create Project" "POST" "/project/create" @{ name = "Phase3 E2E Test"; description = "Testing Phase 3 features"; direction = "Build a self-evolving agent platform" } 200 "success"
$projectID = ""
if ($projResp -and $projResp.data) {
    $projectID = $projResp.data.id
    if (-not $projectID) {
        $projectID = $projResp.data["id"]
    }
}
if (-not $projectID) {
    $listResp = Test-Endpoint "List Projects" "GET" "/project/list" $null 200 "success"
    if ($listResp -and $listResp.data) {
        $projs = $null
        if ($listResp.data -is [System.Collections.IDictionary]) {
            $projs = $listResp.data["projects"]
        } else {
            $projs = $listResp.data.projects
        }
        if ($projs -and $projs.Count -gt 0) {
            $projectID = $projs[0].id
        }
    }
}
Write-Host "  Project ID: $projectID"

# Step 3: Select project
Test-Endpoint "Select Project" "POST" "/auth/select-project" @{ project = $projectID } 200 "success" | Out-Null

# Step 4: Phase 3A - Chief Agent APIs
Write-Host "`n--- Step 3: Phase 3A - Chief Agent ---" -ForegroundColor Yellow
Test-Endpoint "Chief Chat" "POST" "/chief/chat?project_id=$projectID" @{ message = "What is the current project status?" } 200 "success" | Out-Null
Test-Endpoint "Chief Sessions" "GET" "/chief/sessions?project_id=$projectID" $null 200 "success" | Out-Null
Test-Endpoint "Chief Policies" "GET" "/chief/policies" $null 200 "success" | Out-Null

# Step 5: Phase 3B - Experience & Feedback
Write-Host "`n--- Step 4: Phase 3B - Experience & Feedback ---" -ForegroundColor Yellow
Test-Endpoint "Feedback Submit" "POST" "/feedback/submit?project_id=$projectID" @{ task_id = "task_test_1"; outcome = "success"; approach = "Read existing code first, then modify"; key_insight = "Always check for existing utility functions before writing new ones"; pitfalls = "Missed an existing helper that did the same thing"; would_do_differently = "Search codebase more thoroughly before implementing" } 200 "success" | Out-Null

# Submit another feedback for distillation
Test-Endpoint "Feedback Submit 2" "POST" "/feedback/submit?project_id=$projectID" @{ task_id = "task_test_2"; outcome = "failed"; approach = "Directly edited the file"; pitfalls = "Broke existing functionality"; key_insight = "Multi-file changes need PR flow"; would_do_differently = "Use PR flow for multi-file changes" } 200 "success" | Out-Null

Test-Endpoint "Feedback Submit 3" "POST" "/feedback/submit?project_id=$projectID" @{ task_id = "task_test_3"; outcome = "partial"; approach = "Incremental changes"; pitfalls = "Missed edge case"; key_insight = "Always add tests for edge cases" } 200 "success" | Out-Null

# Step 6: Experience List API
Write-Host "`n--- Step 5: Experience Query ---" -ForegroundColor Yellow
$expResp = Test-Endpoint "Experience List (raw)" "GET" "/experience/list?project_id=$projectID&status=raw" $null 200 "data"
if ($expResp -and $expResp.data.experiences) {
    $expCount = $expResp.data.experiences.Count
    $script:RESULTS[-1] = "[PASS] Experience List (raw) - found $expCount records"
    $firstExpID = $expResp.data.experiences[0].id
} else {
    $firstExpID = $null
}

Test-Endpoint "Experience List (all)" "GET" "/experience/list?project_id=$projectID" $null 200 "data" | Out-Null

# Step 7: Phase 3B - Skill & Policy CRUD
Write-Host "`n--- Step 6: Skill & Policy CRUD ---" -ForegroundColor Yellow
Test-Endpoint "Skill List" "GET" "/skill/list?status=candidate" $null 200 "success" | Out-Null
Test-Endpoint "Skill List (all)" "GET" "/skill/list" $null 200 "success" | Out-Null
Test-Endpoint "Policy List" "GET" "/policy/list?status=active" $null 200 "success" | Out-Null
Test-Endpoint "Policy List (all)" "GET" "/policy/list" $null 200 "success" | Out-Null

# Step 8: Create a policy manually (via Chief's create_policy)
Write-Host "`n--- Step 7: Policy Creation ---" -ForegroundColor Yellow
$policyResp = Test-Endpoint "Create Policy via Chief" "POST" "/chief/chat?project_id=$projectID" @{ message = "Please create a policy: when a task has tag 'multi_file', require PR flow with guard prompt 'Multi-file changes must go through PR review'" } 200 "data"

# Step 9: Audit observation experience (simulating M19)
Write-Host "`n--- Step 8: Audit Observation Experience ---" -ForegroundColor Yellow
# This would normally come from internal agent tool call, but we verify the API exists
Test-Endpoint "Experience List (audit)" "GET" "/experience/list?project_id=$projectID&source_type=audit_observation" $null 200 "data" | Out-Null

# Step 10: Policy activate/deactivate
Write-Host "`n--- Step 9: Policy Lifecycle ---" -ForegroundColor Yellow
$policyListResp = Test-Endpoint "Policy List (all statuses)" "GET" "/policy/list" $null 200 "success"
if ($policyListResp -and $policyListResp.data.policies.Count -gt 0) {
    $testPolicyID = $policyListResp.data.policies[0].id
    $testPolicyStatus = $policyListResp.data.policies[0].status
    Write-Host "  Test policy: $testPolicyID (status=$testPolicyStatus)"
    
    if ($testPolicyStatus -eq "candidate") {
        Test-Endpoint "Activate Policy" "POST" "/policy/$testPolicyID/activate" $null 200 "success" | Out-Null
        Test-Endpoint "Deactivate Policy" "POST" "/policy/$testPolicyID/deactivate" $null 200 "success" | Out-Null
        # Reactivate for further tests
        Test-Endpoint "Reactivate Policy" "POST" "/policy/$testPolicyID/activate" $null 200 "success" | Out-Null
    } elseif ($testPolicyStatus -eq "active") {
        Test-Endpoint "Deactivate Policy" "POST" "/policy/$testPolicyID/deactivate" $null 200 "success" | Out-Null
        Test-Endpoint "Reactivate Policy" "POST" "/policy/$testPolicyID/activate" $null 200 "success" | Out-Null
    }
} else {
    Write-Host "  No policies found to test lifecycle" -ForegroundColor DarkGray
}

# Step 11: Skill approve/reject
Write-Host "`n--- Step 10: Skill Lifecycle ---" -ForegroundColor Yellow
$skillListResp = Test-Endpoint "Skill List (candidate)" "GET" "/skill/list?status=candidate" $null 200 "success"
if ($skillListResp -and $skillListResp.data.skills.Count -gt 0) {
    $testSkillID = $skillListResp.data.skills[0].id
    Write-Host "  Test skill: $testSkillID"
    Test-Endpoint "Approve Skill" "POST" "/skill/$testSkillID/approve" $null 200 "success" | Out-Null
} else {
    Write-Host "  No candidate skills found (Analyze Agent hasn't run yet)" -ForegroundColor DarkGray
}

# Step 12: Verify data models
Write-Host "`n--- Step 11: Data Model Verification ---" -ForegroundColor Yellow
Test-Endpoint "Chief Traces (empty)" "GET" "/chief/traces?session_id=nonexistent" $null 200 "success" | Out-Null

# Results
Write-Host "`n========== Results ==========" -ForegroundColor Cyan
foreach ($r in $RESULTS) {
    if ($r.StartsWith("[PASS]")) {
        Write-Host "  $r" -ForegroundColor Green
    } else {
        Write-Host "  $r" -ForegroundColor Red
    }
}

Write-Host "`n  Total: $($PASS + $($FAIL)) | PASS: $PASS | FAIL: $FAIL" -ForegroundColor $(if ($FAIL -eq 0) { "Green" } else { "Red" })
Write-Host "==========================================`n" -ForegroundColor Cyan
