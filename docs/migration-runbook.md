# Native Runtime Migration Runbook

> Operator guide for moving platform agent roles off opencode onto the
> self-built native runtime. One role at a time, with a clear rollback
> path at every step.

## TL;DR checklist per role

```
[ ] Step 1: Run nativesmoke in staging — must be GREEN
[ ] Step 2: Register the target LLM endpoint via the dashboard
[ ] Step 3: Test connection with the Test button (should show token usage)
[ ] Step 4: Pick a non-prod project to shadow-run the role for 24h
[ ] Step 5: Compare opencode vs native for the same task (diff outputs)
[ ] Step 6: Flip the role in Settings → Agent Model Configuration
[ ] Step 7: Monitor dashboard signal-health + duration for 72h
[ ] Step 8: If stable, promote to prod project; else see Rollback
```

Migration order (least → most blast radius):
1. `audit_1` — read-only, deterministic schema, safest to try first.
2. `audit_2` — same as audit_1 but runs less often.
3. `fix` — writes files but only on explicit L1 handoff.
4. `analyze` — heavy reads, no writes.
5. `chief` — heavy reads + long-running, but no writes.
6. `maintain` — writes the milestone / direction blocks.
7. `evaluate` / `merge` — last. These gate PRs; any regression is
   immediately user-facing.

## Step 1 — Pre-flight smoke

From a clean workspace:

```sh
cd platform/backend

# Unit suite (~95 cases)
go test ./internal/llm/... ./internal/runner/...
# Expect:   ok  .../internal/llm (cached)
#           ok  .../internal/runner

# End-to-end smoke with a mock LLM (no external deps)
go run ./cmd/nativesmoke
# Expect:   ✔ ALL GREEN — native runtime is production-ready for audit_1
#           exit code 0
```

If any check fails, **stop**. Do not proceed to register endpoints or
flip overrides. Filing the failure against the runtime takes priority
over the migration timeline.

## Step 2 — Register the target endpoint

1. In the dashboard, open **LLM Endpoints** (left sidebar).
2. Click **+ Register endpoint**.
3. Pick a format:
   - `openai` for MiniMax / DeepSeek / OpenRouter / Ollama / vLLM.
   - `anthropic` for native Anthropic / Bedrock-Litellm proxies.
4. Fill in:
   - **Name** — human label, shown in the role picker.
   - **Base URL** — leave empty for the provider's canonical URL, or
     paste a specific `/v1` root (see inline hint per format).
   - **API Key** — stored server-side, redacted on GET.
   - **Models** — one model id per line. Pricing + capability hints
     auto-fill from the builtin catalogue when known (MiniMax-M2.7,
     Claude Sonnet 4.5, etc.); otherwise the model still works, just
     without cost stats.
5. Save. The row appears with a green **LIVE** badge once the loader
   installed it into the Registry.

## Step 3 — Test connection

Click the blue **Test** button on the endpoint card. The button
dispatches a 1-token probe through the live runtime path:

- **Pass**: `✓ N in / M out · model-id`
- **Fail**: `✗ <provider error verbatim>` — fix BaseURL / key and try
  again. Common causes:
  - Base URL missing `/v1` (OpenAI-compat providers)
  - API key scoped to the wrong org
  - Model id typo (most common; copy from the provider's dashboard)

Do not move on until Test is green.

## Step 4 — Shadow run

Pick a **non-prod project** — ideally a staging clone with the same
workload shape as prod. On that project only:

1. Go to **Settings → Agent Model Configuration**.
2. Find the target role (e.g. audit_1).
3. Click **Change** and pick the new 🔌-tagged model row.
4. Confirm. The next session for that role runs on the native runtime;
   prior sessions are unaffected.

Leave it running for 24 hours with real traffic. During this window:

- Watch `docker logs` / journald for `[Dispatcher] session=...
  role=... → native runner ...` lines. Every session for the migrated
  role should log that prefix.
- Watch `[Dispatcher] ... tokens=X/Y cache=Z/W cost=$... iters=N
  duration=...` at session end. Compare iteration counts against
  opencode's average for the same role — a 3× blowup means the model
  is over-tool-calling, adjust prompt or swap models.
- Verify chat panel: native sessions render a **live typewriter** as
  the model streams. Opencode sessions still render the full reply
  in one paint. This is the most obvious visual differentiator.

## Step 5 — Diff against opencode

Use the bundled `shadowdiff` CLI to compare populations of sessions
across runtimes without hand-eyeballing the DB:

```sh
# Compare the last 20 audit_1 sessions on this project
cd platform/backend
go run ./cmd/shadowdiff --project $PROJECT_ID --role audit_1 --limit 20

# Full-fidelity pair diff between one native session and one opencode
go run ./cmd/shadowdiff --session-a sess_native_abc --session-b sess_legacy_xyz --show-output
```

Population mode prints:
- Per-side session table (id, status, model, duration, first 200 chars of Output).
- Aggregate completion rate + avg `duration_ms`.
- A `⚠` line when native's completion rate is >10pp below opencode's
  (the runbook's red-flag signal). A `✓` when native beats it.

Pair mode surfaces every differing field with a `*` marker, then
prints both `Output` fields and any `InjectedArtifacts` JSON
side-by-side. Use it to diagnose one-off regressions.

Eyeballing that shadowdiff can't do:
- **Write roles (fix/maintain)**: are the resulting edits functionally
  equivalent? Use `git diff` between the two runtime's branches.
- **Tool call shape**: in the dashboard's Activity feed, does the
  native session emit the same TOOL_CALL events opencode did?

Expected differences (all benign):
- Native sessions include `AGENT_TEXT_DELTA` + `AGENT_DONE` events;
  opencode doesn't.
- Native sessions' `CHAT_UPDATE` payload carries `session_id`;
  opencode's doesn't.

Red flags (block migration):
- Native always requires more iterations than opencode for the same
  task (suggests tool schema or prompt regression).
- Native's audit verdicts diverge from opencode's by >10% on a fixed
  task set.

## Step 6 — Flip prod

Once shadow is stable, re-flip the role in the prod project's
Settings page. **That is the entire prod change** — no server restart,
no config deploy.

## Step 7 — Monitor 72h

Check these signals at least once a day for the first three days:

- **Dashboard → Knowledge → Injection Signal Quality card**: the
  role's success/failure rate per signal should not drop > 5pp versus
  the pre-migration baseline. A sharper drop suggests the new model
  is producing lower-quality outputs → the refinery's feedback loop
  is catching it, pay attention.
- **Dashboard → Tag Review**: if the role proposes tags (analyze, fix,
  maintain), verify the proposal cadence is similar. An empty review
  queue means the role stopped proposing; a flood means schema drift.
- **Activity feed**: no `AGENT_ERROR` events unless the provider
  itself is having a bad day (check their status page first).

## Step 8 — Rollback

Native runtime routing is controlled by **one DB column**
(`role_override.model_provider`). Rollback is a one-click operation:

1. In **Settings → Agent Model Configuration**, click **Reset** on the
   migrated role. This clears the override; the role falls back to
   the opencode default immediately.
2. In-flight native sessions finish normally; the next session goes
   through opencode.
3. If you need a hard kill, set the endpoint's status to `disabled`
   from the **LLM Endpoints** page — every session using that
   endpoint fails with a clear `endpoint not registered` error at
   dispatch, and new sessions fall through to opencode.

## Known quirks

- **SQLite smoke only**: `nativesmoke` covers the wire path but not
  Redis-broadcast side effects inside the audit service. AuditLevel
  persistence is exercised in the service unit tests; do not regress
  those while working on the runtime.
- **Prompt templates**: the default `runner.NativePromptBuilder` reuses
  `agent.BuildPrompt`, which loads role templates via `go:embed`. If
  you fork a role's template, rebuild the binary.
- **Parallel tool calls**: opencode processes tool calls in the order
  the model emits them; native does the same today (sequential). If
  you migrate a high-fanout role (analyze, chief) and see wall-clock
  regressions >2×, file a ticket — parallelisation is a known next
  step.

## Verification recipes

```sh
# Is the dispatcher wiring alive?
curl -s localhost:8080/api/v1/llm/endpoints \
  -H "Authorization: Bearer $KEY" | jq '.data.endpoints[] | {id, name, registered}'

# Did the last session route native or opencode?
grep "role=audit_1 →" /var/log/a3c/backend.log | tail

# How long did the last 10 native audit_1 sessions take?
grep "[Dispatcher] session=.* role=audit_1 tokens=" /var/log/a3c/backend.log \
  | tail -10 \
  | sed 's/.*duration=//'
```

## Success criteria for the whole migration

- [ ] Every role routed native for 7 consecutive days.
- [ ] `docker logs` has zero `→ opencode` dispatches outside the
      explicit fallback roles (if any are kept).
- [ ] Injection-signal success rates held steady within ±3pp.
- [ ] One week with no `AGENT_ERROR` events that trace to our code
      (provider outages are acceptable).

Once the above is true, delete `internal/opencode/` and the hooks in
`cmd/server/main.go` (`opencode.InitScheduler`,
`opencode.ToolCallHandler`, `opencode.SessionCompletionHandler`,
`opencode.BroadcastHandler`). That's the Phase 4 close-out.
