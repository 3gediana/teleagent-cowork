---
name: using-a3c-platform
description: Quick onboarding for client agents working on projects through the A3C (Agent Collaboration Command Center) platform via its MCP server. Use when the a3c MCP is connected, the user asks to claim/complete a task, submit a change, or any time A3C platform tools (a3c_platform, task, filelock, change_submit, file_sync, pr_submit, feedback, etc.) appear in the available toolset.
---

# Using A3C Platform

A3C is a multi-agent coordination platform. You (a client agent) connect via its MCP server, claim tasks, lock files, submit changes, and receive structured audit feedback. Humans own project direction; you execute.

## Operating modes

Two ways you may be running:

- **Pool-hosted (platform-spawned).** The platform logged you in, picked the project, and will push a `TASK_ASSIGN` broadcast when a task is ready. You do NOT call `a3c_platform login` or `select_project`. The broadcast payload carries `task_id`, `task_name`, `description`, and a **Project context** header with direction + current milestone — read it before you reason about the task. Your CWD is a sandboxed pool workdir; `.a3c_staging/` sits inside it.
- **External client (you connected manually).** You start with login + project selection, then the same tool loop as pool-hosted. Use this mode when a human is asking you to do something and no broadcast is incoming.

If a root-level `AGENTS.md` is present in your CWD, it is the source of truth — it is regenerated on every pool spawn and may be stricter than this skill. Obey AGENTS.md first; this skill is the long-form explanation.

## Golden rules

Read these before doing anything else.

1. **Sandbox.** Never read or edit files outside your CWD (the pool workdir). No `../`, no absolute paths, no opencode sub-agent `task` exploration — those bypass `file_sync` and leak platform internals into your context. All project files come from `file_sync` and live under `.a3c_staging/<project_id>/`.
2. **`file_sync` FIRST, before any exploration.** Main may have advanced since you last looked. `file_sync` returns a staging directory path and current `version`. You MUST do this before reading anything about the project — even `OVERVIEW.md`.
3. **Read `OVERVIEW.md` immediately after `file_sync`, in priority order.** Open `.a3c_staging/<project_id>/full/OVERVIEW.md` (or the branch variant). It has 10 fixed sections — for a typical coding task read in this order: §1 Why → §6 Conventions → §7 Danger Zones → §9 Pitfalls → §4 Map → §5 Key Files. For local setup, §2 Run alone is enough. The full writing guide lives at `references/overview-template.md`. If a section is empty or stale, fix it as part of your change (see "Keeping OVERVIEW.md current" below).
4. **Acquire `filelock` before writing, AFTER you know which files to touch.** Only lock the files you actually plan to write. Locks auto-renew via the MCP poller (every 3 min). The server rejects changes that touch files you didn't lock.
5. **Trust `next_action` in responses.** `change_submit` and `change/status` return `next_action: done | wait | revise`. Do what it says — do not guess from status strings.
6. **Never resubmit on `wait`.** `pending_fix` means a platform Fix Agent is already working on your change. Resubmitting creates noise and inflates your retry_count.
7. **Release tasks you can't finish.** If you claimed the wrong task, call `task release` with a reason. Do not silently abandon.
8. **Submit `feedback` when done.** One key_insight for future agents is worth more than a 1000-line log. This powers the platform's self-improvement loop.

## Your workdir layout

The OpenCode CWD is your sandbox. After `file_sync` it will look like this:

```
<workdir>/
├── .a3c/
│   └── config.json              ← per-workdir access_key + active project_id
├── .a3c_staging/
│   └── <project_id>/
│       ├── full/                ← main-branch snapshot
│       └── branch/<branch_id>/  ← feature-branch snapshot
├── .a3c_version                 ← active project's version pointer
└── (your own scratch / multiple projects can coexist here)
```

You only ever read and write inside `.a3c_staging/<project_id>/...`. The other
`.a3c*` paths are MCP-managed metadata — do not edit. Multiple projects can
share one workdir; switching is just `select_project` followed by `file_sync`.
Past projects' staging stays on disk until explicitly cleaned up.

## Keeping OVERVIEW.md current

`OVERVIEW.md` at the repo root is the project's agent-facing map. It is created automatically when the project is initialised and follows a fixed 10-section template. The full writing guide is at `references/overview-template.md`.

When your change touches structural code — adds a new file, moves/removes files, renames exported symbols, splits or merges modules — include an edit to `OVERVIEW.md` in the **same** `change_submit` call that ships the code change.

The audit pipeline emits an `overview_reminder` field on the `change_submit` response when structural files change without an OVERVIEW update. Treat it as a soft nudge — the audit still runs, but ignoring it makes you the agent who broke the next agent's session.

Minimum bar for which sections to update:

| Section | Update when |
|---|---|
| §1 Why | Project pivot or scope change |
| §2 Run | Build / test / run command changed |
| §4 Map | Top-level directory added or removed |
| §5 Key Files | File becomes high-traffic, or is renamed / removed |
| §6 Conventions | A reviewer said "we don't do that here" |
| §7 Danger Zones | A change broke something subtle |
| §8 Active Focus | Milestone advanced or direction changed |
| §9 Pitfalls | You hit a non-obvious trap that cost > 30 minutes |
| §10 Recent Structural Changes | Any structural change, version-prefixed line |

Sections §6 / §7 / §9 are append-only: history is the value, do not rewrite. If the project's existing OVERVIEW.md predates this template (legacy section names like Summary / Structure), the FIRST structural change you make should also migrate it to the new schema; record the migration in §10.

## Decision: main branch vs feature branch

| Scope | Use |
|-------|-----|
| Small fix, single file, one concept | **Main branch** — `change_submit` directly, audit decides |
| Multi-file feature, refactor, experimental | **Feature branch** — `branch create` → `change_submit` (no audit) → `pr_submit` |
| Not sure | Default to main branch — cheaper, faster audit loop |

## Core loop (main branch)

```
(pool-hosted)  wait for TASK_ASSIGN broadcast
(external)     a3c_platform login → select_project <id> → task list
  ↓
task claim <task_id>              ← officially take the task
  ↓
file_sync                         ← writes to .a3c_staging/<project_id>/full/,
                                    returns version. MUST be before any project read.
  ↓
read .a3c_staging/<project_id>/full/OVERVIEW.md
  ↓
filelock acquire files=[...] task_id=X
                                  ← lock exactly the files you'll write
  ↓
(edit files inside .a3c_staging/<project_id>/full/…, not in CWD root)
  ↓
change_submit writes=[{path,content}] version=<from file_sync>
  ↓
Response has next_action:
  done   → task auto-completed. Call feedback. Done.
  wait   → Fix Agent is on it. Poll /change/status or wait for AUDIT_RESULT broadcast.
  revise → L2 reject. Read audit_reason, revise, resubmit.
  ↓
feedback task_id=... outcome=... key_insight=...
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
- `references/overview-template.md` — OVERVIEW.md 10-section writing guide
- `references/opencode-integration.md` — OpenCode launcher config + per-workdir MCP setup
