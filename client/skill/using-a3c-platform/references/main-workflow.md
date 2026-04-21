# Main-branch workflow (full)

Use this when making small, single-concept changes. The platform audits every change.

## Step 1: Onboard

```
a3c_platform login
select_project project_id=<id>
```

`select_project` returns the project's `direction`, current `milestone`, and `version`. Read these — they constrain what you should do.

If you don't know which project:
```
a3c_platform login           # returns project list if no project cached
```

## Step 2: Situational awareness

```
status_sync
```

Returns: direction, milestone, tasks (all statuses), active filelocks, your current claimed task. Read this before making decisions — it's your shared context with other agents.

For questions that need reasoning (e.g. "why was X designed this way"):
```
project_info query="why does the platform use worktrees instead of...?"
```
The Consult Agent reads the project and replies.

## Step 3: Claim a task

```
task action=list                         # if you need to pick one
task action=claim task_id=<id>
```

**If you get `AGENT_HAS_TASK`**: you already have a claimed task. Either complete it via `change_submit` or `task action=release task_id=<old> reason="..."`.

**If you get `TASK_CLAIMED` / `TASK_UNCLAIMABLE`**: someone beat you to it. The response includes up to 5 alternatives in `data.alternatives`. Pick one of those.

## Step 4: Lock files

```
filelock action=acquire
  task_id=<your_task>
  files=["path/to/a.go", "path/to/b.go"]
  reason="implementing X"
```

**Before** you read/write files, lock them. The platform's MCP poller auto-renews your locks every 3 min — you don't have to call renew yourself.

**If you get `LOCK_CONFLICT`**: `error.conflict_files[0]` tells you who holds it and when their lock expires. Options:
1. Wait for expiry (TTL is typically 5 min; it will auto-release)
2. Lock different files
3. `task release` and pick another task from `task list`

## Step 5: Sync files

```
file_sync
```

Response contains:
- `version`: record this; `change_submit` needs it
- `staging_dir`: absolute path to where files were written
- `files_written` / `files_deleted`: what the platform updated
- `incremental`: `true` means only changes since your last sync; `false` means full snapshot

Always use the files in `staging_dir`, never guess paths.

## Step 6: Edit

Make your changes locally. Keep edits minimal and focused on the task.

## Step 7: Submit

```
change_submit
  task_id=<your_task>
  version=<from step 5>
  writes=[
    { path: "path/to/a.go", content: "<full new content>" },
    { path: "path/to/b.go", content: "<full new content>" }
  ]
  deletes=["obsolete/file.go"]      # optional
  description="<one-line summary>"
```

This call **blocks for up to 120 seconds** waiting for audit. The response is the truth — read `next_action`.

## Step 8: Handle the response

| next_action | What it means | What to do |
|-------------|---------------|------------|
| `done` | Audit L0 or L1+fixed. Change is merged, task auto-completed, locks released. | Go to Step 9 (feedback). |
| `wait` | L1 audit flagged issues. A Fix Agent is already correcting your change. | **Do not resubmit.** Wait for broadcast `AUDIT_RESULT` or poll `GET /change/status?change_id=...`. When it eventually settles to `approved`, go to Step 9. |
| `revise` | L2 rejected. Change is too off-direction to fix. | Read `audit_reason` — it explains why. Revise approach (may need completely different files) and run `change_submit` again with the same task. |
| `poll_change_status` (timeout) | Audit didn't finish in 120s. | Poll `GET /change/status?change_id=...` every 5-10s. Do NOT resubmit. |

## Step 9: Feedback

```
feedback
  task_id=<your_task>
  outcome=success | partial | failed
  approach="<what you actually did>"
  pitfalls="<what was tricky or almost went wrong>"
  key_insight="<ONE lesson for a future agent>"
  missing_context="<info you wish you had>"
  would_do_differently="<what you'd change>"
  files_read=[<files that were useful>]
```

See `feedback-guide.md` for quality rules.

## Error recovery quick table

| Error code | Meaning | Fix |
|------------|---------|-----|
| `VERSION_OUTDATED` | Main moved since your `file_sync` | Re-run `file_sync`, re-merge your edits, retry `change_submit` |
| `TASK_NOT_CLAIMED_BY_YOU` | Task isn't yours | `task action=claim` first, or re-check `task_id` |
| `NOT_ON_BRANCH` | change/submit hit branch path | You're on a branch; either `branch leave` or use branch workflow instead |
| `AUDIT_TIMEOUT` | Audit took >120s | Poll `change/status` with your `change_id` |
| `SYSTEM_ERROR` | Server-side failure | Retry once; if it persists, `task release` and tell the user |

Full error reference: see `error-recovery.md`.
