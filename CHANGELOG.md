# CHANGELOG

All bug and UX fixes from the 2026-04-22 hardening pass.

---

## Security (must-have before exposing to network)

- **AuthMiddleware rejects unauthenticated requests** (`middleware/auth.go`)
  Previously a missing `Authorization` header was silently promoted to `agent_id=human`, effectively disabling auth on every protected endpoint (`/task/*`, `/change/*`, `/pr/*`, `/chief/*`, `/policy/*`, …). Now: no Bearer → 401.
- **Register no longer leaks access_key** (`handler/auth.go`)
  `POST /agent/register` with an existing agent name used to return that agent's `access_key` in the response — full account takeover by anyone who knew the name. Now: 409 `AGENT_NAME_TAKEN`.
- **Ownership checks** added where missing:
  - `DELETE /task/:id` (`handler/task.go`)
  - `POST /branch/close` (`handler/branch.go`)
  - `POST /dashboard/confirm` (`handler/dashboard.go`) — only the agent that created the pending input can confirm
- **Human-only endpoints gated by `Agent.IsHuman` flag** (new column):
  - `POST /dashboard/input`, `/dashboard/confirm`, `/dashboard/clear_context`
  - `POST /chief/chat`
  Frontend `/agent/register` defaults to `is_human=true`; MCP registrations default to `false`. Bootstrap on DB migration promotes the oldest agent if no human exists yet.
- **Branch name charset validation** (`handler/branch.go`)
  User-supplied names restricted to `^[A-Za-z0-9][A-Za-z0-9._-]*$`, preventing git flag injection via names like `-rf`.

## Concurrency correctness

- **FileLock `Acquire` made atomic** (`handler/filelock.go`)
  Now a single transaction with `SELECT ... FOR UPDATE` on existing locks. Idempotent: re-acquiring same agent+task+file returns the existing lock instead of creating duplicates.
- **Task `Claim` made atomic** (`handler/task.go`)
  Agent-has-task check and task update now share one transaction. Concurrent claims can no longer both succeed.
- **SSE multi-client support** (`service/broadcast.go`, `handler/sse.go`)
  Each connection gets a unique client ID; previously two tabs on the same project overwrote each other.
- **Redis ack-set TTL** (`service/broadcast.go`)
  `SAdd` now always pairs with `Expire(24h)`. Without this, broadcast ack sets leaked forever.
- **`pendingInputs` thread-safe + 30 min TTL** (`handler/dashboard.go`)
  Map access now guarded by `sync.Mutex`; stale entries garbage-collected.

## Core logic bugs

- **`ExecuteMerge` now clears `agent.current_branch_id`** (`service/branch.go`)
  Previously the cleanup condition was always false (nil check after assigning nil), leaving agents pointing to deleted worktrees.
- **L2 rejection watchdog logic fixed** (`service/audit.go`)
  Was unconditionally resetting the task after 10 min regardless of heartbeats or resubmits. Now: any heartbeat (<2min) or newer submission aborts the watchdog.
- **Policy tag matching uses correct column** (`opencode/scheduler.go`)
  Previously looked up `TaskTag.task_id = session.ChangeID` — wrong entity. Now resolves ChangeID → Change.TaskID → TaskTag.
- **Change diff distinguishes `new` vs `modified`** (`handler/change.go`)
  Audit agent was seeing every write labelled "new".
- **PR evaluate unknown result** (`service/pr_agent.go`)
  When LLM returns something unrecognized, broadcast `PR_NEEDS_WORK` instead of leaving PR stuck at `evaluated`.
- **Chief merge path honors `VersionSuggestion` and broadcasts `PR_MERGED`** (`service/tool_handler.go`)
  AutoMode-merged PRs no longer silently ignore biz-review version suggestions or skip the frontend notification.
- **Feedback `AgentRole` semantic fix** (`handler/feedback.go`)
  Client agent feedback now stored with `AgentRole="client_agent"` and the persona goes into `tags`, so Analyze Agent's role-based grouping isn't polluted.

## MCP client

- **`feedback` tool actually works** (`client/mcp/src/api-client.ts`, `src/index.ts`)
  Added `submitFeedback()` — previously the tool called `api.post(...)` which didn't exist, causing a runtime `TypeError`. Phase 3B's data-collection entry point was silently broken.
- **`task` tool gained `list` and `release` actions** (`src/index.ts`)
  Previously only `claim`. New `release` prevents dead-lock when an agent claims the wrong task.
- **`pr_submit.self_review` is now a structured object** (`src/index.ts`, `handler/pr.go`)
  No more "did I pass a string or an object" confusion. Server accepts both.
- **`poll` body no longer leaks access_key** (`src/api-client.ts`)
  Bearer header is the sole auth; the redundant `{key}` body was harmless but unnecessary disclosure.
- **`file_sync` respects `A3C_STAGING_DIR` / `cwd()`** (`src/index.ts`)
  Previously staged files inside the MCP install dir, where the agent couldn't find them.

## Agent ergonomics

- **Incremental `file_sync`** (`handler/filesync.go`, `src/index.ts`)
  Uses `git diff --name-status <old_tag>..HEAD` to return only changed files + a `deleted` array. Full snapshot fallback when the tag is unknown. Client unlinks deleted files from staging.
- **FileLock auto-renew in Poller** (`client/mcp/src/poller.ts`)
  Calls `/filelock/renew` every 3 min so long-running tasks don't lose locks mid-work.
- **`change.submit` response has `next_action`** (`handler/change.go`)
  `"done"` / `"wait"` / `"revise"` with a human-readable message — agents no longer have to guess from status strings. Timeout response points at a new `GET /change/status?change_id=` poll endpoint.
- **`GET /change/status` endpoint added**
  Single-change status lookup so agents can check on pending audits without scanning the full project list.
- **Branch enter conflict returns structured occupant info** (`service/branch.go`, `handler/branch.go`)
  `BRANCH_OCCUPIED` now includes `occupant.agent_id / name / last_active_unix` and a `hint`.
- **`PR_MERGED` broadcast includes `next_action`** (`handler/pr.go`)
  Agents that were on the merged branch get a clear "pick another branch or return to main" message.
- **SSE resume-from-id** (`service/broadcast.go`, `handler/sse.go`)
  Standard `Last-Event-ID` header / `?last_event_id=` query parameter. Ongoing stream also emits `id:` lines. Browser `EventSource` reconnection is now lossless within retention.
- **Chief chat response shape unified** (`handler/chief.go`)
  New-session and follow-up responses share the same fields (`session_id`, `status`, `agent_response`, `opencode_session_id`, `message`).
- **`/change/list` paginated** (`?limit=&offset=`)
- **`/experience/list` honors `?limit=`**
- **Claim race surfaces alternatives** (`handler/task.go`)
  On `TASK_CLAIMED` / `TASK_UNCLAIMABLE`, response includes up to 5 pending tasks ranked by priority.
- **Heartbeat window widened** (`scheduler.go`, `auth.go`, `sync.go`, `poller.ts`)
  Poller: 3 min. Redis TTL: 7 min. Scheduler checker: 7 min. Tolerates brief network jitter.
- **Chief `GlobalState` bounded**
  Max 30 tasks (pending/claimed prioritized), 20 agents, 30 policies per prompt. No more context-window overflow on long-running projects.

## Misc

- **`AGENT_ONLINE` broadcast sent once, not N-1 times** (`handler/auth.go`)
- **Analyze timer no longer runs on startup** (`service/scheduler.go`)
  And raises the raw-experience threshold from 3 to 10 for better signal quality.
- **Poll message injection coalesced** (`handler/sync.go`)
  At most 5 important events per poll folded into one `[Project Updates]` block to avoid flooding the agent session.
- **Logout releases branch occupancy**
- **Login auto-cleanup releases branch occupancy** (matches scheduler behavior)
- **Pre-existing vet error fixed** (`handler/agent.go`)
  Dead `!= nil` check on a function declaration removed so `go test ./...` passes cleanly.

---

## Breaking changes / migration notes

- **`Agent.IsHuman` column added.** `AutoMigrate` handles schema. The `model/init.go` bootstrap promotes the oldest existing agent to `is_human=true` if no human agents exist yet — your dashboard keeps working after upgrade without manual SQL.
- **Frontend `authApi.register(name, projectId, isHuman?)`** now defaults `isHuman=true`. If you programmatically register agents from the frontend, they will be flagged human.
- **`GET /agent/register` with an existing name** now returns 409 instead of recycling the stored `access_key`. Users must keep their first-issued key.
- **`POST /poll`** no longer reads `{key}` from body. (Server already used Bearer; nothing broke — but clients passing the body field now get ignored.)
- **`POST /pr/submit.self_review`** accepts both object (preferred) and stringified JSON (backward compat). No breakage for existing clients.

---

## Verification

- `go build ./...` ✅
- `go test ./...` ✅
- `npx tsc --noEmit` (client/mcp) ✅
- `npx tsc --noEmit` (frontend) ✅
