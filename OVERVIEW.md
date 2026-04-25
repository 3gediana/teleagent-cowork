# Project Overview

> Living map of this codebase. Every agent reads it at session start.
> Update it in the SAME `change_submit` that introduces structural changes
> (new files, moved/renamed/removed files, new exported symbols, modules).
> Section headings are stable — they are part of the agent protocol.
> Read order for a typical task: §1 → §6 → §7 → §9 → §4 → §5.

---

## 1. Why  *(2–5 lines, never empty)*

A3C is a multi-agent coordination platform. Human operators set project
direction; AI agents (local-via-MCP, or optionally platform-hosted) claim
tasks, submit changes, and receive structured audit feedback. Default mode
is collaboration-hub: the platform manages state, review, and broadcasts —
execution lives in agents.

---

## 2. Run  *(commands only, no prose)*

```
# Backend (Go)
cd platform/backend && go run ./cmd/server          # :8080

# Frontend (React + Vite)
cd frontend && npm install && npm run dev           # :5173

# MCP client (TypeScript) — for external agents
cd client/mcp && npm install && npm run build && node dist/index.js

# Tests
cd platform/backend && go test ./internal/...
cd client/mcp && npm run build
```

---

## 3. Stack  *(one-liners)*

- **Language / runtime**: Go 1.21 (backend), Node 20+ (MCP client), React 18 (frontend)
- **Main framework**: Gin + GORM (backend), Vite + Zustand (frontend), `@modelcontextprotocol/sdk` (client)
- **Storage**: SQLite (dev) / Postgres (prod), Redis (broadcasts + queues)
- **Other**: opencode subprocesses for optional platform-hosted pool (gated by `A3C_AUTOPILOT`)

---

## 4. Map  *(top 2 levels max, one line each)*

```
platform/backend/
  cmd/server/         # entry point + boot wiring
  internal/
    handler/          # HTTP endpoints, one file per resource
    service/          # business logic (audit, broadcast, git, refinery, scheduler)
    model/            # GORM models + DB schema
    agentpool/        # platform-hosted opencode pool (autopilot only)
    config/           # env + yaml loader, IsAutopilotEnabled()
    repo/             # ContentBlock + project metadata helpers
client/mcp/           # external MCP server (TypeScript)
client/skill/         # agent skill protocols (markdown contracts)
frontend/src/
  pages/              # one route = one page
  components/         # shared UI
  api/                # typed backend API wrappers
docs/dev/             # internal architecture notes
bench/                # benchmark scenarios + outputs
```

---

## 5. Key Files  *(what FUTURE agents will open, not what you just touched)*

| File | What it does | When you'll touch it |
|---|---|---|
| `platform/backend/cmd/server/main.go` | server boot, autopilot gate, route table | startup wiring |
| `platform/backend/internal/handler/change.go` | change submit / review / approve | any change-flow work |
| `platform/backend/internal/service/audit.go` | audit workflow + `ApproveAndCommitChange` | approval logic |
| `platform/backend/internal/service/broadcast.go` | SSE manager + `BroadcastDirected` | realtime / events |
| `platform/backend/internal/service/git.go` | repo init + `initialOverviewTemplate` (`//go:embed`) | per-project repo, OVERVIEW seed |
| `platform/backend/internal/agentpool/pool.go` | pool manager + spawn lifecycle | autopilot / pool features |
| `platform/backend/internal/config/config.go` | env + yaml + `IsAutopilotEnabled()` | any new env-driven flag |
| `client/mcp/src/index.ts` | MCP tool definitions (agent contract) | client-facing protocol |
| `client/mcp/src/config.ts` | `workdirRoot()` + per-workdir config | workdir / config behaviour |
| `client/skill/using-a3c-platform/SKILL.md` | client-agent contract | agent protocol changes |

---

## 6. Conventions  *(append-only)*

- Every HTTP handler returns `{success, data, error}` envelope — never raw payload
- Broadcasts MUST be idempotent; consumers ack via `messageID`
- Never call `os.Getenv` outside `internal/config` — go through `config.Get()` / `config.IsAutopilotEnabled()`
- Pool / autopilot features are gated behind `A3C_AUTOPILOT=1` env (default OFF)
- Frontend API calls go through `src/api/endpoints.ts`, not raw `fetch`
- Tests for handlers live next to them: `xxx.go` ↔ `xxx_test.go`
- DB migrations are append-only; never edit a past migration
- Templates that ship to projects (e.g. `initial_overview.md`) live in `service/templates/` and are loaded via `//go:embed`

---

## 7. Danger Zones  *(append-only)*

- `internal/service/audit.go::approveChange` — git commit + version bump in one transaction; partial failure leaves the project in a corrupt state
- `internal/agentpool/dormancy.go` — wake/sleep state machine, race-prone; check `IsAutopilotEnabled` before adding new auto-wake paths
- `internal/service/broadcast.go::BroadcastDirected` — auto-wake is gated behind autopilot; new callers must keep that gate
- `internal/handler/change.go` — manual review path (`pending_human_confirm`) is critical for collaboration-hub mode; do not collapse it back into auto-audit
- `internal/handler/agentpool.go::Spawn` / `Wake` — both must reject when autopilot is off (otherwise zombie pool agents)
- DB schema: append-only migrations; never edit a past migration

---

## 8. Active Focus  *(5 lines max, refresh on milestone switch)*

- **Milestone**: Collaboration Hub (autopilot OFF by default)
- **Now**: OVERVIEW protocol v2 (10-section schema); per-workdir MCP config (`A3C_HOME` / `workdirRoot()`)
- **Next**: OpenCode workdir-level MCP wiring; team async catch-up; @mention via directed broadcast
- **Out of scope**: Multi-tenant; SSO; native-runner refactor

---

## 9. Pitfalls  *(append-only)*

- `BroadcastDirected` used to auto-wake dormant pool agents even with autopilot off — fixed via `config.IsAutopilotEnabled()` gate in `service/broadcast.go`
- `/agentpool/wake` used to bypass the autopilot gate — now rejected in `handler/agentpool.go`
- `change_submit` with project `auto_mode=true` used to silently auto-audit even when autopilot was off — now forced to manual review (`pending_human_confirm`) when autopilot off
- MCP client config used to live only in `~/.a3c/config.json` (global): switching workdirs cross-polluted `project_id`. Now per-workdir at `<workdir>/.a3c/config.json` with home as read-only fallback
- `OVERVIEW.md` reminder is a soft nudge on `change_submit` — agents who ignore it accumulate stale maps for everyone else; staleness compounds across sessions
- Go raw strings cannot contain backticks, so the OVERVIEW template body lives in `service/templates/initial_overview.md` (embedded), not inline

---

## 10. Recent Structural Changes  *(append at top, date- or version-prefixed)*

- 2026-04-25: OpenCode integration verified — `opencode.json` setup guide at `client/skill/using-a3c-platform/references/opencode-integration.md`. Two deployment options documented (cwd-based default + explicit `A3C_HOME` env). Per-workdir config isolation manually verified across two workdirs (no cross-pollution; home config untouched).
- 2026-04-25: Per-workdir MCP config — `<workdir>/.a3c/config.json` is primary; `workdirRoot()` resolves via `A3C_HOME` env or `process.cwd()`. Home `~/.a3c/config.json` is read-only fallback for first-run UX. Adds `client/mcp/src/config.ts::workdirRoot` export.
- 2026-04-25: OVERVIEW template v2 — 10-section schema embedded via `//go:embed templates/initial_overview.md` in `service/git.go`. Writing guide added at `client/skill/using-a3c-platform/references/overview-template.md`. SKILL.md updated with priority read order and section-update matrix.
- 2026-04-25: Agent pool / autopilot gated behind `A3C_AUTOPILOT` env (default off). `BroadcastDirected` auto-wake, `/agentpool/spawn`, `/agentpool/wake`, and pool-only background loops (`StartContextWatcher`, `StartBroadcastConsumer`, `StartDormancyDetector`, `StartTaskDispatcher`) all respect the gate.
- 2026-04-25: `service.ApproveAndCommitChange` exported as wrapper around `approveChange`; `change/review` handler reworked to accept both `pending` and `pending_human_confirm`, delegate side-effects, and always broadcast `AUDIT_RESULT` to submitter.
