# platformlive — End-to-End Live Integration Test

A one-shot Go binary that boots the real HTTP server in-process against
real MySQL + Redis + a real LLM provider, then drives the full platform
pipeline from **human login → project completion** over plain HTTP and
SSE — exactly the surface an MCP client or the dashboard UI would use.

Unlike the unit tests this is **not** a mock: every agent turn is a
real model call, every tool invocation touches real files in the
project's repo directory, and every state transition is observed
through the public API.

## What it covers

| Phase | Exercises |
|-------|-----------|
| 0  | Bootstrap: MySQL schema reset, Redis connect, LLM endpoint registration, role overrides pinned, HTTP server wired. |
| 1  | Operator register + login + create project + select. |
| 2  | SSE subscription opens (every broadcast event is logged live). |
| 3  | `/dashboard/input` + `/dashboard/confirm` for direction. |
| 4  | 2 multi-round Chief chat turns — verifies `DialogueMessage` persistence and that turn N+1 sees turn N's assistant reply. |
| 5  | Dashboard task input → Maintain agent creates milestone + tasks (real LLM + real file-tool calls). |
| 6  | Register + login a client worker agent. |
| 7  | Client `task list` / `task claim` / `file sync` / `filelock acquire` / `change submit`. |
| 8  | Audit pipeline: `audit_1` → (optionally `fix` + `audit_2`) → terminal verdict. |
| 9  | `branch create` + `branch enter` + `branch change_submit` + `pr submit`. |
| 10 | Operator `approve_review` → Evaluate agent + Chief `pr_review` decision + Maintain `biz_review`. |
| 11 | Operator `approve_merge` → Merge agent runs. |
| 12 | Chief follow-up turn — verifies dialogue continuity across the PR pipeline. |
| 13 | Final DB-state report + event histogram + Chief transcript + operator logout. |

## Prerequisites

- MySQL and Redis running (see `configs/config.yaml`).
- A populated provider block under `llm.*` in `configs/config.yaml`
  (`minimax` by default — pass `-provider openai|anthropic|deepseek` to
  pick a different one).
- Port **13003** free on localhost (override with `-port`).

## Run

```powershell
# 1. Reset the live-test database (safe: it is a dedicated DB, not the
#    one the normal server uses).
D:\mysql\bin\mysql.exe -uroot -e "DROP DATABASE IF EXISTS a3c_live; CREATE DATABASE a3c_live CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;"

# 2. Run from platform/backend.
cd platform\backend
go run ./cmd/platformlive
```

Typical runtime on MiniMax-M2.7: **3–6 minutes** end-to-end.

## Reading the output

Stdout interleaves three streams, each prefixed so they are easy to
separate:

- `────── Phase N: ...` — scenario milestones.
- `  ✔ <name>` / `  ✗ <name>` — individual assertions (34 total).
- `    📡 <EVENT_TYPE>` — live SSE broadcasts from the platform
  (agent turns, tool calls, chat updates, PR transitions, version
  bumps). `AGENT_TEXT_DELTA` is counted in the final histogram but
  not printed per-token — it would otherwise drown everything else.

The final banner shows one of:

```
✅ ALL 34 CHECKS PASSED  (elapsed: 3m22s)
```

or, on regression:

```
❌ 32 passed, 2 FAILED  (elapsed: 3m40s)
     - <name> <extra info>
     - ...
```

with the list of failed checks repeated for easy triage.

## Design notes

- **In-process server.** `main.go` wires the same `handler.*` / `middleware.*`
  chain that `cmd/server/main.go` does, minus the long-running timers
  (maintain timer, refinery timer). This keeps the test self-contained
  in a single `go run` — no separate server to start/stop/kill.
- **Clean slate.** The bootstrap phase `DELETE`s every table in
  `a3c_live`, so re-runs are deterministic without having to drop the
  DB manually (but dropping is still recommended on schema changes).
- **Real LLM cost.** Each full run consumes roughly 100k–150k tokens
  across ~10 sessions (chief × 3, maintain × 2, audit × 1–2, fix × 0–1,
  evaluate × 1, biz-review × 1, merge × 1). On MiniMax-M2.7 that's
  about **$0.05–$0.10 per run** — cheap enough to iterate on freely,
  but not free.
- **Graceful degradation.** If Maintain fails to produce a task on a
  given run (real LLMs are non-deterministic), the test creates a
  fallback task via `/task/create` so downstream phases still execute.
  The missing-task case is still flagged as a failed check so the
  regression shows up in the final tally.

## Troubleshooting

| Symptom | Likely cause |
|---------|--------------|
| `llm.<provider>.api_key is empty` | Provider creds missing from `configs/config.yaml`. |
| `SSE connect error: ... 401` | `AuthMiddleware` rejected the subscription — the bearer header logic changed; re-check `startSSE`. |
| Audit never reaches terminal status | Usually an agent exception in the runner; check stderr for panics and `agent_session.status='failed'` rows. |
| `chief dialogue has ≥4 turns ... got 3` | `UpdateSessionOutput` is flipping status to `completed` before `fireSessionCompletion` runs — a regression of the race this test was designed to catch. |
| `Invalid JSON text: "The document is empty."` in audit logs | A JSON-typed column is being written with `""` instead of `"[]"` / `"{}"`. Check `internal/handler/change.go` defaults. |

## Files

- [`main.go`](./main.go) — the entire test harness (server bootstrap,
  HTTP client, SSE subscriber, scenario driver, reporter).
