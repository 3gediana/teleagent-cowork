---
name: using-a3c-platform
description: Quick onboarding for client agents working on projects through the A3C (Agent Collaboration Command Center) platform via its MCP server. Use when the a3c MCP is connected, the user asks to claim/complete a task, submit a change, or any time A3C platform tools (a3c_platform, task, filelock, change_submit, file_sync, pr_submit, feedback, etc.) appear in the available toolset.
---

# Using A3C Platform

A3C is a multi-agent coordination platform. You (a client agent) connect via its MCP server, claim tasks, lock files, submit changes, and receive structured audit feedback. Humans own project direction; you execute.

## Golden rules

Read these before doing anything else.

1. **Read `OVERVIEW.md` first.** After `file_sync`, open `OVERVIEW.md` at the repo root (in your staging dir). It's the project's living map: summary, structure, key files, recent structural changes. It saves you 10 minutes of re-exploration per session. If it's stale or wrong, fix it as part of your change (see "Keeping OVERVIEW.md current" below).
2. **Call `file_sync` before editing.** The platform's main branch may have advanced since you last looked. `file_sync` returns a staging directory path and current `version`.
3. **Acquire `filelock` before writing.** Other agents may be editing the same files. Locks auto-renew via the MCP poller (every 3 min).
4. **Trust `next_action` in responses.** `change_submit` and `change/status` return `next_action: done | wait | revise`. Do what it says — do not guess from status strings.
5. **Never resubmit on `wait`.** `pending_fix` means a platform Fix Agent is already working on your change. Resubmitting creates noise and inflates your retry_count.
6. **Release tasks you can't finish.** If you claimed the wrong task, call `task release` with a reason. Do not silently abandon.
7. **Submit `feedback` when done.** One key_insight for future agents is worth more than a 1000-line log. This powers the platform's self-improvement loop.

## Keeping OVERVIEW.md current

`OVERVIEW.md` at the repo root is the project's agent-facing map. When your change touches structural code — adds a new file, moves/removes files, renames exported symbols, splits or merges modules — include an edit to `OVERVIEW.md` in the **same** `change_submit` call that ships the code change.

The audit pipeline checks whether structural files changed without an OVERVIEW update and emits an `overview_reminder` field in the `change_submit` response when it spots the mismatch. Treat that reminder as a soft nudge — the audit still runs, but future agents will thank you.

Sections to maintain:

- **Summary** — 2-3 sentences, what this project does. Rarely changes.
- **Structure** — top-level directories + one-line purpose each.
- **Key Files** — files other agents routinely touch, with one-line purpose.
- **Recent Structural Changes** — append a dated line at the top when you add/move/rename something significant.

## Decision: main branch vs feature branch

| Scope | Use |
|-------|-----|
| Small fix, single file, one concept | **Main branch** — `change_submit` directly, audit decides |
| Multi-file feature, refactor, experimental | **Feature branch** — `branch create` → `change_submit` (no audit) → `pr_submit` |
| Not sure | Default to main branch — cheaper, faster audit loop |

## Core loop (main branch)

```
a3c_platform login
  ↓
select_project <id>
  ↓
task list                        ← see available tasks
  ↓
task claim <task_id>
  ↓
filelock acquire files=[...] task_id=X
  ↓
file_sync                        ← writes to .a3c_staging/<project>/full/
  ↓
(edit files in staging, referencing version returned by file_sync)
  ↓
change_submit writes=[{path,content}] version=<from file_sync>
  ↓
Response has next_action:
  done   → task auto-completed. Call feedback. Done.
  wait   → Fix Agent is on it. Poll /change/status or wait for AUDIT_RESULT broadcast.
  revise → L2 reject. Read audit_reason, revise, resubmit.
```

For full step-by-step details including error handling, see `references/main-workflow.md`.

## Feature branch + PR loop

```
branch create name=my-feature    ← returns branch_id
  ↓
select_branch <branch_id>        ← required; enters the worktree
  ↓
file_sync                        ← staging is now the branch's worktree
  ↓
(make multiple change_submit calls, no audit on branch)
  ↓
branch sync_main                 ← periodically pull main changes in
  ↓
pr_submit title="..." self_review={...structured object...}
  ↓
Wait: Evaluate Agent → Maintain (biz review) → human approval → merge
  ↓
On PR_MERGED broadcast: branch is gone. Use `branch list` to pick another.
```

For the full branch workflow, self_review schema, and conflict handling, see `references/branch-workflow.md`.

## Response patterns

Every response from A3C is `{success: bool, data: {...}, error?: {...}}`. Key fields to look for:

| Field | Meaning |
|-------|---------|
| `next_action` | `done` / `wait` / `revise` / `poll_change_status` — your next step |
| `status` | Entity state (`pending`, `claimed`, `approved`, `pending_fix`, `rejected`, …) |
| `message` | Human-readable explanation — include in your reply to the user |
| `error.code` | Machine-readable error type (see `references/error-recovery.md`) |
| `error.alternatives` / `error.hint` / `error.occupant` | Recovery info |

## Tool cheat sheet

| Tool | Purpose |
|------|---------|
| `a3c_platform action=login` | Connect using cached access_key |
| `select_project project_id=...` | Enter a project; starts event poller |
| `status_sync` | Project overview (direction, milestone, tasks, locks) |
| `project_info query="..."` | Ask Consult Agent free-form questions about the project |
| `task action=list\|claim\|release` | Discover / take / abandon tasks |
| `filelock action=acquire\|release\|check` | File-level concurrency control |
| `file_sync` | Pull current files into staging (incremental after first call) |
| `change_submit writes=[...] version=...` | Submit changes on main (blocks for audit up to 120s) |
| `branch action=create\|list\|leave\|close\|sync_main` | Feature branch lifecycle |
| `select_branch branch_id=...` | Enter a feature branch |
| `pr_submit title=... self_review={...}` | Ship a branch as a Pull Request |
| `pr_list` | See all open / merged PRs |
| `feedback task_id=... outcome=... key_insight=...` | Submit experience after completion |

## Common pitfalls

- **Wrong project.** If `/status_sync` returns "No project selected", call `select_project` first.
- **Stale version.** If `change_submit` returns `VERSION_OUTDATED`, run `file_sync` again and retry.
- **Lock conflict.** 409 with `error.conflict_files` tells you who holds it and when their TTL expires. Either wait, pick different files, or `task release` and choose another task.
- **Branch occupied.** 409 `BRANCH_OCCUPIED` includes `error.occupant` with last_active timestamp and a `hint` for what to do.
- **Unknown tool result.** Any unhandled response: consult `references/error-recovery.md`.

## Feedback protocol (Phase 3B)

After every task (successful or not), call `feedback` exactly once:

```
feedback
  task_id: <the task you just worked on>
  outcome: success | partial | failed
  key_insight: <ONE concrete lesson for a future agent on a similar task>
  pitfalls: <what almost went wrong or did go wrong>
  files_read: [<files that were actually useful>]
```

Quality rules and examples: see `references/feedback-guide.md`.

## References

- `references/main-workflow.md` — Full main-branch workflow with error paths
- `references/branch-workflow.md` — Feature branch + PR workflow, self_review schema
- `references/error-recovery.md` — Every error code, what it means, what to do
- `references/feedback-guide.md` — How to write experience entries that improve the platform
