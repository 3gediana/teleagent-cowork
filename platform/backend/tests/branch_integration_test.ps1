# Integration test for Phase 2 Branching & PR APIs
# Prerequisites: Server running on localhost:3003

$Base = "http://localhost:3003/api/v1"
$pass = 0
$fail = 0

function Test-Step($name, $result) {
    if ($result.success -eq $true) {
        Write-Host "[PASS] $name" -ForegroundColor Green
        $script:pass++
    } else {
        Write-Host "[FAIL] $name" -ForegroundColor Red
        Write-Host "  Response: $($result | ConvertTo-Json -Depth 3)" -ForegroundColor DarkGray
        $script:fail++
    }
}

function Invoke-Api($method, $path, $body, $key) {
    $headers = @{ "Content-Type" = "application/json" }
    if ($key) { $headers["Authorization"] = "Bearer $key" }
    $uri = "$Base$path"
    try {
        if ($method -eq "GET") {
            $resp = Invoke-WebRequest -Uri $uri -Method GET -Headers $headers -UseBasicParsing
        } else {
            $resp = Invoke-WebRequest -Uri $uri -Method POST -Headers $headers -Body ($body | ConvertTo-Json -Depth 5) -UseBasicParsing
        }
        return ($resp.Content | ConvertFrom-Json)
    } catch {
        return @{ success = $false; error = $_.Exception.Message }
    }
}

# Step 1: Register agent
Write-Host "`n=== Step 1: Register test agent ===" -ForegroundColor Cyan
$reg = Invoke-Api "POST" "/agent/register" @{ name = "branch-test-agent-$(Get-Random)" }
Test-Step "Register agent" $reg
$agentKey = $reg.data.access_key
$agentId = $reg.data.id

# Step 2: Login
Write-Host "`n=== Step 2: Login ===" -ForegroundColor Cyan
$login = Invoke-Api "POST" "/auth/login" @{ key = $agentKey }
Test-Step "Login" $login

# Step 3: Create project
Write-Host "`n=== Step 3: Create test project ===" -ForegroundColor Cyan
$proj = Invoke-Api "POST" "/project/create" @{ name = "branch-test-$(Get-Random)"; description = "Branch integration test" } $agentKey
Test-Step "Create project" $proj
$projectId = $proj.data.id

# Step 4: Select project (should return branches list)
Write-Host "`n=== Step 4: Select project ===" -ForegroundColor Cyan
$select = Invoke-Api "POST" "/auth/select-project" @{ project = $projectId } $agentKey
Test-Step "Select project" $select
$branchCount = 0
if ($select.data.branches) { $branchCount = $select.data.branches.Count }
Write-Host "  Branches in select_project response: $branchCount"

# Step 5: Create branch
Write-Host "`n=== Step 5: Create branch ===" -ForegroundColor Cyan
$branch = Invoke-Api "POST" "/branch/create" @{ name = "test-feature" } $agentKey
Test-Step "Create branch" $branch
$branchId = $branch.data.id
Write-Host "  Branch ID: $branchId"

# Step 6: List branches
Write-Host "`n=== Step 6: List branches ===" -ForegroundColor Cyan
$list = Invoke-Api "GET" "/branch/list" $null $agentKey
Test-Step "List branches" $list
Write-Host "  Branch count: $($list.data.branches.Count)"

# Step 7: Enter branch
Write-Host "`n=== Step 7: Enter branch ===" -ForegroundColor Cyan
$enter = Invoke-Api "POST" "/branch/enter" @{ branch_id = $branchId } $agentKey
Test-Step "Enter branch" $enter

# Step 8: Branch file sync
Write-Host "`n=== Step 8: Branch file sync ===" -ForegroundColor Cyan
$fsync = Invoke-Api "GET" "/branch/file_sync" $null $agentKey
Test-Step "Branch file sync" $fsync

# Step 9: Branch change submit
Write-Host "`n=== Step 9: Branch change submit ===" -ForegroundColor Cyan
$csubmit = Invoke-Api "POST" "/branch/change_submit" @{
    writes = @(@{ path = "test.txt"; content = "hello from branch" })
    description = "test write on branch"
} $agentKey
Test-Step "Branch change submit" $csubmit

# Step 10: Verify change.submit blocked on branch
Write-Host "`n=== Step 10: Verify change.submit blocked ===" -ForegroundColor Cyan
$blocked = Invoke-Api "POST" "/change/submit?project_id=$projectId" @{
    task_id = "fake"
    version = "v1.0"
    writes = @(@{ path = "x"; content = "y" })
} $agentKey
$blockedCode = $blocked.error.code
if ($blockedCode -eq "USE_BRANCH_CHANGE_SUBMIT") {
    Write-Host "[PASS] change.submit correctly blocked when on branch" -ForegroundColor Green
    $script:pass++
} else {
    Write-Host "[FAIL] change.submit not blocked (got: $blockedCode)" -ForegroundColor Red
    $script:fail++
}

# Step 11: Verify file/sync blocked on branch
Write-Host "`n=== Step 11: Verify file/sync blocked ===" -ForegroundColor Cyan
$fsyncBlocked = Invoke-Api "POST" "/file/sync" @{ version = "v1.0" } $agentKey
$fsyncCode = $fsyncBlocked.error.code
if ($fsyncCode -eq "USE_BRANCH_FILE_SYNC") {
    Write-Host "[PASS] file/sync correctly blocked when on branch" -ForegroundColor Green
    $script:pass++
} else {
    Write-Host "[FAIL] file/sync not blocked (got: $fsyncCode)" -ForegroundColor Red
    $script:fail++
}

# Step 12: Submit PR
Write-Host "`n=== Step 12: Submit PR ===" -ForegroundColor Cyan
$pr = Invoke-Api "POST" "/pr/submit" @{
    title = "Test PR from branch"
    description = "Integration test PR"
    self_review = '{"changed_functions":[],"overall_impact":"low","merge_confidence":"high"}'
} $agentKey
Test-Step "Submit PR" $pr
$prId = $pr.data.id
Write-Host "  PR ID: $prId, Status: $($pr.data.status)"

# Step 13: List PRs
Write-Host "`n=== Step 13: List PRs ===" -ForegroundColor Cyan
$prList = Invoke-Api "GET" "/pr/list" $null $agentKey
Test-Step "List PRs" $prList
Write-Host "  PR count: $($prList.data.pull_requests.Count)"

# Step 14: Get PR details
if ($prId) {
    Write-Host "`n=== Step 14: Get PR details ===" -ForegroundColor Cyan
    try {
        $headers = @{ "Authorization" = "Bearer $agentKey"; "Content-Type" = "application/json" }
        $prGet = (Invoke-WebRequest -Uri "$Base/pr/$prId" -Headers $headers -UseBasicParsing).Content | ConvertFrom-Json
        Test-Step "Get PR details" $prGet
        Write-Host "  PR status: $($prGet.data.status)"
    } catch {
        Write-Host "[FAIL] Get PR: $($_.Exception.Message)" -ForegroundColor Red
        $script:fail++
    }
}

# Step 15: Reject PR (safe - won't trigger agents)
if ($prId) {
    Write-Host "`n=== Step 15: Reject PR ===" -ForegroundColor Cyan
    $reject = Invoke-Api "POST" "/pr/reject" @{ pr_id = $prId; reason = "Integration test rejection" } $agentKey
    Test-Step "Reject PR" $reject
}

# Step 16: Leave branch
Write-Host "`n=== Step 16: Leave branch ===" -ForegroundColor Cyan
$leave = Invoke-Api "POST" "/branch/leave" @{} $agentKey
Test-Step "Leave branch" $leave

# Step 17: Close branch
Write-Host "`n=== Step 17: Close branch ===" -ForegroundColor Cyan
$close = Invoke-Api "POST" "/branch/close" @{ branch_id = $branchId } $agentKey
Test-Step "Close branch" $close

# Step 18: Verify branch closed
Write-Host "`n=== Step 18: Verify branch closed ===" -ForegroundColor Cyan
$list2 = Invoke-Api "GET" "/branch/list" $null $agentKey
$activeCount = ($list2.data.branches | Where-Object { $_.status -eq "active" }).Count
Write-Host "  Active branches after close: $activeCount"
if ($activeCount -eq 0) {
    Write-Host "[PASS] No active branches remaining" -ForegroundColor Green
    $script:pass++
} else {
    Write-Host "[WARN] $activeCount active branches still exist (may be from other tests)" -ForegroundColor Yellow
}

# Summary
Write-Host "`n========================================="
Write-Host "Results: $pass passed, $fail failed" -ForegroundColor $(if ($fail -eq 0) { "Green" } else { "Red" })
Write-Host "========================================="
