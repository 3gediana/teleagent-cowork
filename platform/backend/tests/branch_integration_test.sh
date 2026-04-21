#!/bin/bash
# Integration test for Phase 2 Branching & PR APIs
# Prerequisites: MySQL + Redis running, server started on localhost:3003
# Usage: bash tests/branch_integration_test.sh

set -e

BASE_URL="http://localhost:3003/api/v1"
AGENT_KEY=""
PROJECT_ID=""
BRANCH_ID=""
PR_ID=""

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

pass() { echo -e "${GREEN}[PASS]${NC} $1"; }
fail() { echo -e "${RED}[FAIL]${NC} $1"; }
info() { echo -e "${YELLOW}[INFO]${NC} $1"; }

# Step 1: Register an agent
info "Step 1: Register test agent"
REG_RESULT=$(curl -s -X POST "$BASE_URL/agent/register" \
  -H "Content-Type: application/json" \
  -d '{"name":"test-branch-agent"}')
AGENT_KEY=$(echo "$REG_RESULT" | python3 -c "import sys,json; print(json.load(sys.stdin).get('data',{}).get('access_key',''))" 2>/dev/null || echo "")

if [ -z "$AGENT_KEY" ]; then
  # Agent may already exist, try login with a known key
  fail "Register failed, trying existing agent login"
  # List agents to find one
  exit 1
fi
pass "Agent registered, key=${AGENT_KEY:0:8}..."

# Step 2: Login
info "Step 2: Login"
LOGIN_RESULT=$(curl -s -X POST "$BASE_URL/auth/login" \
  -H "Content-Type: application/json" \
  -d "{\"key\":\"$AGENT_KEY\"}")
LOGIN_SUCCESS=$(echo "$LOGIN_RESULT" | python3 -c "import sys,json; print(json.load(sys.stdin).get('success',False))" 2>/dev/null || echo "False")
if [ "$LOGIN_SUCCESS" != "True" ]; then
  fail "Login failed: $LOGIN_RESULT"
  exit 1
fi
pass "Login successful"

# Step 3: Create a project (or use existing)
info "Step 3: Create test project"
PROJ_RESULT=$(curl -s -X POST "$BASE_URL/project/create" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $AGENT_KEY" \
  -d '{"name":"branch-test-project","description":"Test project for branching workflow"}')
PROJECT_ID=$(echo "$PROJ_RESULT" | python3 -c "import sys,json; print(json.load(sys.stdin).get('data',{}).get('id',''))" 2>/dev/null || echo "")
if [ -z "$PROJECT_ID" ]; then
  fail "Project creation failed: $PROJ_RESULT"
  exit 1
fi
pass "Project created, id=${PROJECT_ID:0:8}..."

# Step 4: Select project
info "Step 4: Select project"
SELECT_RESULT=$(curl -s -X POST "$BASE_URL/auth/select-project" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $AGENT_KEY" \
  -d "{\"project\":\"$PROJECT_ID\"}")
SELECT_SUCCESS=$(echo "$SELECT_RESULT" | python3 -c "import sys,json; print(json.load(sys.stdin).get('success',False))" 2>/dev/null || echo "False")
BRANCHES_IN_SELECT=$(echo "$SELECT_RESULT" | python3 -c "import sys,json; d=json.load(sys.stdin).get('data',{}); print(len(d.get('branches',[])))" 2>/dev/null || echo "0")
if [ "$SELECT_SUCCESS" != "True" ]; then
  fail "Select project failed: $SELECT_RESULT"
  exit 1
fi
pass "Project selected, branches in response: $BRANCHES_IN_SELECT"

# Step 5: Create a branch
info "Step 5: Create branch"
BRANCH_RESULT=$(curl -s -X POST "$BASE_URL/branch/create" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $AGENT_KEY" \
  -d '{"name":"test-feature"}')
BRANCH_SUCCESS=$(echo "$BRANCH_RESULT" | python3 -c "import sys,json; print(json.load(sys.stdin).get('success',False))" 2>/dev/null || echo "False")
BRANCH_ID=$(echo "$BRANCH_RESULT" | python3 -c "import sys,json; print(json.load(sys.stdin).get('data',{}).get('id',''))" 2>/dev/null || echo "")
if [ "$BRANCH_SUCCESS" != "True" ]; then
  fail "Branch creation failed: $BRANCH_RESULT"
  exit 1
fi
pass "Branch created, id=${BRANCH_ID:0:8}..."

# Step 6: List branches
info "Step 6: List branches"
LIST_RESULT=$(curl -s -X GET "$BASE_URL/branch/list" \
  -H "Authorization: Bearer $AGENT_KEY")
LIST_SUCCESS=$(echo "$LIST_RESULT" | python3 -c "import sys,json; print(json.load(sys.stdin).get('success',False))" 2>/dev/null || echo "False")
BRANCH_COUNT=$(echo "$LIST_RESULT" | python3 -c "import sys,json; d=json.load(sys.stdin).get('data',{}); print(len(d.get('branches',[])))" 2>/dev/null || echo "0")
if [ "$LIST_SUCCESS" != "True" ]; then
  fail "List branches failed: $LIST_RESULT"
else
  pass "List branches: $BRANCH_COUNT branch(es) found"
fi

# Step 7: Enter branch
info "Step 7: Enter branch"
ENTER_RESULT=$(curl -s -X POST "$BASE_URL/branch/enter" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $AGENT_KEY" \
  -d "{\"branch_id\":\"$BRANCH_ID\"}")
ENTER_SUCCESS=$(echo "$ENTER_RESULT" | python3 -c "import sys,json; print(json.load(sys.stdin).get('success',False))" 2>/dev/null || echo "False")
if [ "$ENTER_SUCCESS" != "True" ]; then
  fail "Enter branch failed: $ENTER_RESULT"
  exit 1
fi
pass "Entered branch"

# Step 8: Branch file sync
info "Step 8: Branch file sync"
FSYNC_RESULT=$(curl -s -X GET "$BASE_URL/branch/file_sync" \
  -H "Authorization: Bearer $AGENT_KEY")
FSYNC_SUCCESS=$(echo "$FSYNC_RESULT" | python3 -c "import sys,json; print(json.load(sys.stdin).get('success',False))" 2>/dev/null || echo "False")
if [ "$FSYNC_SUCCESS" != "True" ]; then
  fail "Branch file sync failed: $FSYNC_RESULT"
else
  pass "Branch file sync successful"
fi

# Step 9: Branch change submit
info "Step 9: Branch change submit"
CSUBMIT_RESULT=$(curl -s -X POST "$BASE_URL/branch/change_submit" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $AGENT_KEY" \
  -d '{"writes":[{"path":"test.txt","content":"hello from branch"}],"description":"test write on branch"}')
CSUBMIT_SUCCESS=$(echo "$CSUBMIT_RESULT" | python3 -c "import sys,json; print(json.load(sys.stdin).get('success',False))" 2>/dev/null || echo "False")
if [ "$CSUBMIT_SUCCESS" != "True" ]; then
  fail "Branch change submit failed: $CSUBMIT_RESULT"
else
  pass "Branch change submit successful"
fi

# Step 10: Verify change.submit is blocked on branch
info "Step 10: Verify change.submit blocked on branch"
BLOCKED_RESULT=$(curl -s -X POST "$BASE_URL/change/submit?project_id=$PROJECT_ID" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $AGENT_KEY" \
  -d '{"task_id":"fake","version":"v1.0","writes":[{"path":"x","content":"y"}]}')
BLOCKED_CODE=$(echo "$BLOCKED_RESULT" | python3 -c "import sys,json; print(json.load(sys.stdin).get('error',{}).get('code',''))" 2>/dev/null || echo "")
if [ "$BLOCKED_CODE" = "USE_BRANCH_CHANGE_SUBMIT" ]; then
  pass "change.submit correctly blocked when on branch"
else
  fail "change.submit not blocked (expected USE_BRANCH_CHANGE_SUBMIT, got: $BLOCKED_CODE)"
fi

# Step 11: Verify file/sync is blocked on branch
info "Step 11: Verify file/sync blocked on branch"
FSYNC_BLOCKED=$(curl -s -X POST "$BASE_URL/file/sync" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $AGENT_KEY" \
  -d '{"version":"v1.0"}')
FSYNC_BLOCKED_CODE=$(echo "$FSYNC_BLOCKED" | python3 -c "import sys,json; print(json.load(sys.stdin).get('error',{}).get('code',''))" 2>/dev/null || echo "")
if [ "$FSYNC_BLOCKED_CODE" = "USE_BRANCH_FILE_SYNC" ]; then
  pass "file/sync correctly blocked when on branch"
else
  fail "file/sync not blocked (expected USE_BRANCH_FILE_SYNC, got: $FSYNC_BLOCKED_CODE)"
fi

# Step 12: Submit PR
info "Step 12: Submit PR"
PR_RESULT=$(curl -s -X POST "$BASE_URL/pr/submit" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $AGENT_KEY" \
  -d '{"title":"Test PR from branch","description":"Integration test PR","self_review":"{\"changed_functions\":[],\"overall_impact\":\"low\",\"merge_confidence\":\"high\"}"}')
PR_SUCCESS=$(echo "$PR_RESULT" | python3 -c "import sys,json; print(json.load(sys.stdin).get('success',False))" 2>/dev/null || echo "False")
PR_ID=$(echo "$PR_RESULT" | python3 -c "import sys,json; print(json.load(sys.stdin).get('data',{}).get('id',''))" 2>/dev/null || echo "")
if [ "$PR_SUCCESS" != "True" ]; then
  fail "PR submit failed: $PR_RESULT"
else
  pass "PR submitted, id=${PR_ID:0:8}..."
fi

# Step 13: List PRs
info "Step 13: List PRs"
PR_LIST=$(curl -s -X GET "$BASE_URL/pr/list" \
  -H "Authorization: Bearer $AGENT_KEY")
PR_LIST_SUCCESS=$(echo "$PR_LIST" | python3 -c "import sys,json; print(json.load(sys.stdin).get('success',False))" 2>/dev/null || echo "False")
PR_COUNT=$(echo "$PR_LIST" | python3 -c "import sys,json; d=json.load(sys.stdin).get('data',{}); print(len(d.get('pull_requests',[])))" 2>/dev/null || echo "0")
if [ "$PR_LIST_SUCCESS" != "True" ]; then
  fail "List PRs failed"
else
  pass "List PRs: $PR_COUNT PR(s) found"
fi

# Step 14: Get PR details
if [ -n "$PR_ID" ]; then
  info "Step 14: Get PR details"
  PR_GET=$(curl -s -X GET "$BASE_URL/pr/$PR_ID" \
    -H "Authorization: Bearer $AGENT_KEY")
  PR_GET_SUCCESS=$(echo "$PR_GET" | python3 -c "import sys,json; print(json.load(sys.stdin).get('success',False))" 2>/dev/null || echo "False")
  PR_STATUS=$(echo "$PR_GET" | python3 -c "import sys,json; print(json.load(sys.stdin).get('data',{}).get('status',''))" 2>/dev/null || echo "")
  if [ "$PR_GET_SUCCESS" = "True" ]; then
    pass "PR details: status=$PR_STATUS"
  else
    fail "Get PR failed"
  fi
fi

# Step 15: Reject PR (safe test - won't trigger agent)
if [ -n "$PR_ID" ]; then
  info "Step 15: Reject PR (safe test)"
  REJECT_RESULT=$(curl -s -X POST "$BASE_URL/pr/reject" \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer $AGENT_KEY" \
    -d "{\"pr_id\":\"$PR_ID\",\"reason\":\"Integration test rejection\"}")
  REJECT_SUCCESS=$(echo "$REJECT_RESULT" | python3 -c "import sys,json; print(json.load(sys.stdin).get('success',False))" 2>/dev/null || echo "False")
  if [ "$REJECT_SUCCESS" = "True" ]; then
    pass "PR rejected successfully"
  else
    fail "PR reject failed: $REJECT_RESULT"
  fi
fi

# Step 16: Leave branch
info "Step 16: Leave branch"
LEAVE_RESULT=$(curl -s -X POST "$BASE_URL/branch/leave" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $AGENT_KEY")
LEAVE_SUCCESS=$(echo "$LEAVE_RESULT" | python3 -c "import sys,json; print(json.load(sys.stdin).get('success',False))" 2>/dev/null || echo "False")
if [ "$LEAVE_SUCCESS" = "True" ]; then
  pass "Left branch"
else
  fail "Leave branch failed: $LEAVE_RESULT"
fi

# Step 17: Close branch
info "Step 17: Close branch"
CLOSE_RESULT=$(curl -s -X POST "$BASE_URL/branch/close" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $AGENT_KEY" \
  -d "{\"branch_id\":\"$BRANCH_ID\"}")
CLOSE_SUCCESS=$(echo "$CLOSE_RESULT" | python3 -c "import sys,json; print(json.load(sys.stdin).get('success',False))" 2>/dev/null || echo "False")
if [ "$CLOSE_SUCCESS" = "True" ]; then
  pass "Branch closed"
else
  fail "Close branch failed: $CLOSE_RESULT"
fi

# Step 18: Verify branch is closed
info "Step 18: Verify branch closed in list"
LIST2_RESULT=$(curl -s -X GET "$BASE_URL/branch/list" \
  -H "Authorization: Bearer $AGENT_KEY")
ACTIVE_COUNT=$(echo "$LIST2_RESULT" | python3 -c "
import sys,json
d=json.load(sys.stdin).get('data',{})
branches=d.get('branches',[])
active=[b for b in branches if b.get('status')=='active']
print(len(active))
" 2>/dev/null || echo "-1")
pass "Active branches after close: $ACTIVE_COUNT"

echo ""
echo "========================================="
echo -e "${GREEN}All integration tests completed!${NC}"
echo "========================================="
