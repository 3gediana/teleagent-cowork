# Error recovery reference

Every A3C error response has `error.code` and `error.message`. Many include actionable data in `error.*` or `data.*`. Look there first.

## Auth / identity

| Code | Meaning | Action |
|------|---------|--------|
| `AUTH_MISSING` | No Authorization header sent | MCP client sends Bearer automatically; if this appears, the MCP didn't initialize properly. Restart. |
| `AUTH_INVALID_KEY` | Access key is wrong or expired | `a3c_platform action=login` with a fresh key |
| `AUTH_ALREADY_ONLINE` | Same agent is online elsewhere | `a3c_platform action=logout` first, or wait 7 min for heartbeat timeout |
| `AGENT_NAME_TAKEN` | You tried to register a name someone else owns | Pick a different name; never expect Register to return an existing key (security fix) |
| `HUMAN_ONLY` | Endpoint is reserved for human dashboard users | You're a client agent; you can't call `/dashboard/*` or `/chief/chat`. Use `project_info` for queries instead. |

## Task lifecycle

| Code | Meaning | Action |
|------|---------|--------|
| `TASK_NOT_FOUND` | task_id doesn't exist | Re-check from `task list` |
| `TASK_CLAIMED` | Someone else just claimed it | Response has `data.alternatives` — pick one |
| `TASK_UNCLAIMABLE` | Task is in a state that can't be claimed (e.g. deleted) | Response has `data.alternatives` |
| `TASK_COMPLETED` | Already done | `task list` for fresh options |
| `AGENT_HAS_TASK` | You have an existing claimed task | Complete it (submit change) or `task release` it |
| `TASK_NOT_CLAIMED_BY_YOU` | You're trying to act on a task you don't own | Claim first, or use your actual `task_id` |

## File locks

| Code | Meaning | Action |
|------|---------|--------|
| `LOCK_CONFLICT` | File is locked by another agent | `error.conflict_files[0]` has `locked_by` + `expires_at`. Wait, work on other files, or release task |
| TTL of 5 min | Locks auto-renew via MCP poller every 3 min | If you see an unexpected expiry, check if MCP is still connected |

## Changes

| Code | Meaning | Action |
|------|---------|--------|
| `VERSION_OUTDATED` | Main advanced since your `file_sync` | Response includes `current_version`. Re-run `file_sync` to sync, re-apply your edits, retry `change_submit` |
| `NO_FILES` | Submit had empty writes and deletes | Add content |
| `CHANGE_NOT_FOUND` | Unknown change_id | Check from `change list` |
| `CHANGE_ALREADY_APPROVED` | Trying to modify an already-reviewed change | Submit a new change instead |
| `AUDIT_TIMEOUT` (response `status=pending`) | Audit didn't finish in 120s | Poll `GET /change/status?change_id=...` every 5-10s until terminal state |

## Change response `next_action`

| Value | Meaning | What to do |
|-------|---------|------------|
| `done` | Approved and merged | Call `feedback`, you're finished |
| `wait` | L1 issues, Fix Agent is handling it | **Do nothing. Do not resubmit.** Poll `change/status` or wait for `AUDIT_RESULT` broadcast |
| `revise` | L2 rejected | Read `audit_reason`, revise approach, submit new change |
| `poll_change_status` | Audit overrun 120s | Poll `change/status` |

## Branches

| Code | Meaning | Action |
|------|---------|--------|
| `NOT_ON_BRANCH` | Called branch-only tool from main | `select_branch` first, or don't use branch tools |
| `BRANCH_NOT_FOUND` | Bad branch_id | `branch list` |
| `BRANCH_OCCUPIED` | Someone's active on this branch | `error.occupant.agent_name` and `last_active_unix` show who and when. `error.hint` suggests next step |
| `BRANCH_CREATE_FAILED` | Usually "max 3 active branches" | Close an existing branch first |
| `BRANCH_CLOSE_FAILED` | Branch already closed/merged | Ignore |
| `INVALID_NAME` | Branch name violates pattern | Use `^[A-Za-z0-9][A-Za-z0-9._-]*$` — no `/`, no spaces, no leading `-` |
| `SYNC_CONFLICTS` | `branch sync_main` hit conflicts | `error.conflict_files` lists them; resolve in staging and `change_submit` |
| `PR_ALREADY_EXISTS` | Branch has an open PR | Close it or ask for re-review — don't open a duplicate |
| `DIFF_FAILED` | PR diff couldn't be computed | Usually: branch has no commits yet. Make at least one `change_submit` |

## PR lifecycle

| Status | Who acts next |
|--------|---------------|
| `pending_human_review` | Human clicks "approve evaluation" (or Chief Agent in AutoMode) |
| `evaluating` | Evaluate Agent is running — wait |
| `evaluated` | Waiting for Maintain Agent biz review |
| `pending_human_merge` | Human clicks merge (or Chief Agent in AutoMode) |
| `merging` | ExecuteMerge is running — wait |
| `merged` | Done. Branch deleted. Watch `PR_MERGED` broadcast for `next_action`. |
| `merge_failed` | Conflicts during final merge. Human must resolve. |
| `rejected` | PR declined. Read `tech_review` / `biz_review` for reason. |

## Permissions

| Code | Meaning | Action |
|------|---------|--------|
| `FORBIDDEN` | Cross-project action (e.g. trying to delete a task in a project you're not in) | Confirm `current_project_id` matches target |
| `HUMAN_ONLY` | Endpoint is human-flag gated | Don't call it as a client agent |

## System

| Code | Meaning | Action |
|------|---------|--------|
| `SYSTEM_ERROR` | Server exception | Retry once. If persists, capture the error and tell the user — don't loop |
| `INVALID_PARAMS` | Request body / query malformed | Read `error.message`, fix call signature |
| `NOT_FOUND` | Generic 404 | Check the ID |

## When you're genuinely stuck

If you've retried twice and something is still broken:
1. Call `task release` on your current task with `reason="<what went wrong>"`
2. Call `feedback` with `outcome=failed` and describe the blocker in `pitfalls` + `missing_context`
3. Surface the problem to the user — don't silently loop
