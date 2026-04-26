# Full E2E demo: reset DB → register → LLM endpoint → project → direction → chief → 3 pool agents
param(
    [string]$Base = 'http://localhost:3003/api/v1'
)

$ErrorActionPreference = 'Continue'

function Req($method, $path, $body, $headers) {
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
        $body2 = ''
        if ($resp) {
            try { $body2 = (New-Object IO.StreamReader ($resp.GetResponseStream())).ReadToEnd() } catch {}
        }
        return @{ ok=$false; status=$code; body=$body2; err=$_.Exception.Message }
    }
}

function Auth($key) { @{ Authorization = "Bearer $key" } }

# ── Step 0: Reset DB + restart server ──
Write-Host "=== Step 0: Reset DB + restart ===" -ForegroundColor Cyan
D:\mysql\bin\mysql.exe -u root --password= -e "DROP DATABASE IF EXISTS a3c; CREATE DATABASE a3c CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;" 2>$null
powershell -File D:\claude-code\coai2\platform\backend\scripts\dev-server.ps1 restart
Start-Sleep 2

# ── Step 1: Register alice ──
Write-Host "=== Step 1: Register alice ===" -ForegroundColor Cyan
$reg1 = Req POST '/agent/register' @{ name='alice'; is_human=$true } $null
if (-not $reg1.ok) { Write-Host "FAIL: register alice - $($reg1.body)"; exit 1 }
$aliceKey = $reg1.data.data.access_key
$aliceID  = $reg1.data.data.agent_id
Write-Host "  alice registered: id=$aliceID key=$aliceKey"

# ── Step 2: Register LLM endpoint ──
Write-Host "=== Step 2: Register MiniMax-M2.7 LLM endpoint ===" -ForegroundColor Cyan
$llm = Req POST '/llm/endpoints' @{
    name='minimax-m2.7'; format='openai_compatible';
    base_url='https://api.minimaxi.com/v1';
    api_key='sk-cp-MJO5k-R6779F_QbYUyqDCXaYO2ofUTmjQo62GH29erJQ02bS7M5uEWzyHGzrgon6ZAT1GzdeagJy4JBYqecRdV1WbOHfOlUnx21avs6B_wf4-omQgV8OEjE';
    models=@(@{ id='MiniMax-M2.7'; name='MiniMax-M2.7'; context_window=204800; max_output_tokens=131072; supports_tools=$true; supports_reasoning=$true });
    default_model='MiniMax-M2.7'
} (Auth $aliceKey)
if (-not $llm.ok) { Write-Host "FAIL: register LLM - $($llm.body)"; exit 1 }
Write-Host "  LLM endpoint registered: id=$($llm.data.data.id)"

# ── Step 3: Create project with complex direction ──
Write-Host "=== Step 3: Create project ===" -ForegroundColor Cyan
$create = Req POST '/project/create' @{ name='TaskFlow-Pro'; description='Enterprise project management platform with AI-powered task decomposition, real-time collaboration, and automated quality assurance' } (Auth $aliceKey)
if (-not $create.ok) { Write-Host "FAIL: create project - $($create.body)"; exit 1 }
$projID = $create.data.data.id
Write-Host "  project created: id=$projID"

# Select project
$select = Req POST '/auth/select-project' @{ project=$projID } (Auth $aliceKey)
Write-Host "  project selected: status=$($select.status)"

# ── Step 4: Set a complex direction ──
Write-Host "=== Step 4: Set project direction ===" -ForegroundColor Cyan
$direction = @"
# TaskFlow-Pro — Enterprise Project Management Platform

## Vision
Build a full-featured enterprise project management platform with AI-powered capabilities. The system must support multi-team collaboration, intelligent task decomposition, real-time progress tracking, and automated quality assurance pipelines.

## Core Modules (6 modules, ~10000 lines target)

### Module 1: User & Team Management (`users/`)
- User model with role-based access control (Admin, Manager, Developer, Viewer)
- Team CRUD with member invitation and role assignment
- Authentication with JWT tokens, session management, refresh tokens
- User profile with avatar, preferences, notification settings
- Team activity log and audit trail
- **Estimated: ~1500 lines**

### Module 2: Project & Milestone Management (`projects/`)
- Project CRUD with templates (Kanban, Scrum, Waterfall)
- Milestone tracking with Gantt-chart-style timeline
- Project settings: labels, priorities, custom fields
- Project archive/restore and import/export
- Project health dashboard (velocity, burndown, bottleneck detection)
- **Estimated: ~2000 lines**

### Module 3: Task Intelligence Engine (`tasks/`)
- Task CRUD with subtasks, dependencies, and linked issues
- AI-powered task decomposition: given a high-level goal, auto-split into subtasks with effort estimates
- Smart assignment: match tasks to team members based on skills and workload
- Task time tracking with automatic status transitions
- Recurring tasks and task templates
- **Estimated: ~2000 lines**

### Module 4: Real-time Collaboration (`collaboration/`)
- WebSocket-based real-time updates for task boards
- Comment threads with @mentions and rich text
- File attachments with preview and version history
- Shared document editing (CRDT-based)
- Notification system: in-app, email digest, webhook integrations
- **Estimated: ~2000 lines**

### Module 5: Quality Assurance Pipeline (`qa/`)
- Automated code review checklist generation from task descriptions
- Test case auto-generation from acceptance criteria
- QA gate: tasks cannot close until all generated tests pass
- Bug tracking linked to tasks with severity/priority matrix
- Quality metrics dashboard (defect density, test coverage, review turnaround)
- **Estimated: ~1500 lines**

### Module 6: Analytics & Reporting (`analytics/`)
- Team velocity charts and sprint retrospectives
- Custom report builder with drag-and-drop widgets
- Export reports to PDF/Excel
- AI-powered insights: predict delays, suggest rebalancing
- API for external BI tool integration
- **Estimated: ~1000 lines**

## Architecture Requirements
- Backend: Go with Gin framework, MySQL for persistence, Redis for caching/pubsub
- Frontend: React + TypeScript + TailwindCSS + shadcn/ui
- Real-time: WebSocket with Redis pub/sub backend
- AI: OpenAI-compatible API for task decomposition and insights
- Testing: Unit tests for each module, integration tests for cross-module flows

## Milestone Plan
- **Milestone 1**: User & Team + Project foundations (backend models, CRUD APIs, basic UI)
- **Milestone 2**: Task Intelligence + Collaboration (AI decomposition, real-time board, comments)
- **Milestone 3**: QA Pipeline + Analytics (auto test gen, quality gates, reports dashboard)
- **Milestone 4**: Polish & Integration (end-to-end flows, performance, deployment config)
"@

# Set direction via dashboard/input + confirm
$dirInput = Req POST "/dashboard/input?project_id=$projID" @{
    target_block='direction'; content=$direction
} (Auth $aliceKey)
if ($dirInput.ok) {
    $inputID = $dirInput.data.data.input_id
    Write-Host "  direction input created: input_id=$inputID"
    # Confirm it
    $dirConfirm = Req POST "/dashboard/confirm?project_id=$projID" @{
        input_id=$inputID; confirmed=$true
    } (Auth $aliceKey)
    Write-Host "  direction confirmed: status=$($dirConfirm.status)"
} else {
    Write-Host "  FAIL: direction input - status=$($dirInput.status) body=$($dirInput.body)" -ForegroundColor Red
}

# ── Step 5: Chat with Chief to expand direction ──
Write-Host "=== Step 5: Chief chat — expand direction ===" -ForegroundColor Cyan
$chief1 = Req POST "/chief/chat?project_id=$projID" @{
    message="Please review the project direction and produce a detailed implementation plan for Milestone 1. Break it down into specific development tasks with file-level specifications. Each task should be independently assignable to a developer."
} (Auth $aliceKey)
Write-Host "  chief chat response: status=$($chief1.status)"
if ($chief1.ok) {
    Write-Host "  chief status: $($chief1.data.data.status)"
}

# Wait for chief to process
Write-Host "  Waiting 30s for Chief to process..." -ForegroundColor DarkGray
Start-Sleep 30

# Check chief sessions
$chiefSessions = Req GET "/chief/sessions?project_id=$projID&role=chief" $null (Auth $aliceKey)
if ($chiefSessions.ok -and $chiefSessions.data.data.sessions) {
    $sessions = @($chiefSessions.data.data.sessions)
    Write-Host "  chief sessions: $($sessions.Count) found"
    foreach ($s in $sessions) {
        Write-Host "    session=$($s.id) status=$($s.status) role=$($s.role)" -ForegroundColor DarkGray
    }
}

# ── Step 6: Direct Maintain to create tasks from direction ──
Write-Host "=== Step 6: Maintain — create tasks from direction ===" -ForegroundColor Cyan
$maintain1 = Req POST "/dashboard/input?project_id=$projID" @{
    target_block='task'
    content="Based on the project direction, create development tasks for Milestone 1 (User & Team + Project foundations). Create at least 8 tasks covering: 1) User model & auth API with RBAC and JWT, 2) Team CRUD & member management, 3) User profile & preferences, 4) Project CRUD with templates, 5) Milestone tracking & timeline, 6) Project health dashboard, 7) Database migrations & seed data, 8) API routing & middleware setup, 9) Frontend project structure & shared components, 10) Integration tests for auth & project flows. Use the create_task tool for each task."
} (Auth $aliceKey)
Write-Host "  maintain input: status=$($maintain1.status)"
if ($maintain1.ok) {
    Write-Host "  maintain processing: $($maintain1.data.data.status)"
}

# Wait for maintain to create tasks
Write-Host "  Waiting 45s for Maintain to create tasks..." -ForegroundColor DarkGray
Start-Sleep 45

# Check tasks
$taskList = Req GET "/task/list?project_id=$projID" $null (Auth $aliceKey)
$tasks = @()
if ($taskList.ok -and $taskList.data.data) {
    $tasks = @($taskList.data.data)
    Write-Host "  tasks found: $($tasks.Count)"
    foreach ($t in $tasks) {
        Write-Host "    [$($t.status)] $($t.name) (id=$($t.id))" -ForegroundColor DarkGray
    }
}

# If Maintain didn't create tasks, create them manually as fallback
if ($tasks.Count -eq 0) {
    Write-Host "  No tasks from Maintain, creating manually..." -ForegroundColor Yellow
    $manualTasks = @(
        @{ name='User model & auth API'; description='Implement User model with RBAC, JWT auth endpoints (register, login, refresh, logout), session management. Include Admin/Manager/Developer/Viewer roles. Files: users/model.go, users/handler.go, users/auth.go, users/middleware.go'; priority='high' },
        @{ name='Team CRUD & member management'; description='Implement Team model, CRUD APIs, member invitation with role assignment, team activity log. Files: teams/model.go, teams/handler.go, teams/service.go, teams/activity.go'; priority='high' },
        @{ name='User profile & preferences'; description='User profile with avatar upload, notification preferences, theme settings. Profile update API, avatar storage. Files: users/profile.go, users/preferences.go, users/avatar.go'; priority='medium' },
        @{ name='Project CRUD with templates'; description='Project model with Kanban/Scrum/Waterfall templates, CRUD APIs, project settings (labels, priorities, custom fields). Files: projects/model.go, projects/handler.go, projects/templates.go, projects/settings.go'; priority='high' },
        @{ name='Milestone tracking & timeline'; description='Milestone model with timeline, Gantt-chart data API, milestone CRUD, progress calculation. Files: projects/milestone.go, projects/timeline.go, projects/gantt.go'; priority='high' },
        @{ name='Project health dashboard'; description='Velocity calculation, burndown chart data, bottleneck detection API. Dashboard aggregation queries. Files: projects/dashboard.go, projects/velocity.go, projects/burndown.go'; priority='medium' },
        @{ name='Database migrations & seed data'; description='All database schema migrations, seed data for development, migration runner. Files: migrations/001_users.go, migrations/002_teams.go, migrations/003_projects.go, migrations/004_milestones.go, migrations/runner.go'; priority='high' },
        @{ name='API routing & middleware setup'; description='Gin router setup, CORS middleware, auth middleware, rate limiting, request logging, error handling. Files: server/main.go, server/router.go, server/middleware.go, server/errors.go'; priority='high' },
        @{ name='Frontend project structure & shared components'; description='React app setup with TypeScript, TailwindCSS, shadcn/ui. Shared components: Layout, Sidebar, Header, DataTable, FormFields. Files: frontend/src/App.tsx, frontend/src/components/Layout.tsx, frontend/src/components/DataTable.tsx, frontend/src/lib/api.ts'; priority='high' },
        @{ name='Integration tests for auth & project flows'; description='End-to-end integration tests covering user registration, login, team creation, project CRUD, and milestone management. Files: tests/integration/auth_test.go, tests/integration/project_test.go, tests/integration/team_test.go'; priority='medium' }
    )
    foreach ($mt in $manualTasks) {
        $t = Req POST "/task/create?project_id=$projID" $mt (Auth $aliceKey)
        if ($t.ok) {
            Write-Host "    [created] $($mt.name) id=$($t.data.data.id)" -ForegroundColor Green
        } else {
            Write-Host "    [FAIL] $($mt.name) status=$($t.status)" -ForegroundColor Red
        }
    }
    # Refresh task list
    $taskList = Req GET "/task/list?project_id=$projID" $null (Auth $aliceKey)
    if ($taskList.ok -and $taskList.data.data) { $tasks = @($taskList.data.data) }
}

# ── Step 7: Spawn 3 pool agents ──
Write-Host "=== Step 7: Spawn 3 pool agents ===" -ForegroundColor Cyan
$poolIDs = @()
for ($i = 0; $i -lt 3; $i++) {
    $spawn = Req POST '/agentpool/spawn' @{ project_id=$projID } (Auth $aliceKey)
    if ($spawn.ok) {
        $poolID = $spawn.data.data.id
        $poolAgent = $spawn.data.data.agent_name
        $poolPort = $spawn.data.data.port
        $poolIDs += $poolID
        Write-Host "  agent $i spawned: id=$poolID name=$poolAgent port=$poolPort" -ForegroundColor Green
    } else {
        Write-Host "  agent $i spawn FAILED: status=$($spawn.status) body=$($spawn.body)" -ForegroundColor Red
    }
    Start-Sleep 2
}

# ── Step 8: Monitor progress ──
Write-Host ""
Write-Host "=== Step 8: Monitor progress ===" -ForegroundColor Cyan
Write-Host "  Project ID: $projID"
Write-Host "  Pool agents: $($poolIDs.Count) spawned"
Write-Host "  Tasks: $($tasks.Count) total"
Write-Host ""
Write-Host "Use these commands to monitor:" -ForegroundColor Yellow
Write-Host "  Tasks:    GET /api/v1/task/list?project_id=$projID  (Auth $aliceKey)"
Write-Host "  Sessions: GET /api/v1/chief/sessions?project_id=$projID  (Auth $aliceKey)"
Write-Host "  Pool:     GET /api/v1/agentpool/list  (Auth $aliceKey)"
Write-Host "  Changes:  GET /api/v1/change/list?project_id=$projID  (Auth $aliceKey)"
Write-Host ""
Write-Host "Alice key: $aliceKey"
Write-Host "Project ID: $projID"
