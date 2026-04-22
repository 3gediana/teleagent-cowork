# Phase 1 Complete — Native Agent Runtime

> Replaces the opencode agent backend with a self-built Go runtime that
> every platform agent can optionally run on. Opencode remains as an
> unmodified fallback; migration happens one role at a time via the
> dashboard.

## What shipped

### 1. LLM abstraction layer (`internal/llm`)
Unified Provider interface over two wire formats:
- **Anthropic Messages API** (`anthropic.go`) — system prompt caching,
  tool_use with `input_json_delta` reassembly, `thinking` budget
  promotion, stop-reason normalization.
- **OpenAI-compatible** (`openai.go`) — `/chat/completions` with
  tool_call fragment reassembly by index, `reasoning_content` →
  `EvThinkingDelta` passthrough, tool-role message flattening.

Shared infrastructure: SSE decoder tolerant of comments/keepalives +
multi-line data joins + 1 MiB scanner buffer; retry policy with
exponential backoff + jittered caps + Retry-After parsing; cost
computation from token usage × per-model pricing.

Registry keyed by DB endpoint ID so multiple endpoints of the same
format (prod + staging MiniMax, two Anthropic orgs, local Ollama)
coexist. Hot-reload on handler CRUD without server restart.

**42 unit tests green** covering SSE framing, retry decisions, cost
math, adapter request shape (`httptest` servers + canned fixtures),
tool-call reassembly, streaming text deltas, error propagation, and
registry routing.

### 2. DB + HTTP surface (`model.LLMEndpoint`, `/api/v1/llm/endpoints/*`)
- `LLMEndpoint` schema: ID, Name, Format (`anthropic`|`openai`),
  BaseURL, APIKey (redacted on GET), Models JSON, DefaultModel,
  Status, CreatedBy, timestamps.
- CRUD endpoints with human-only mutations (reuses `requireHuman`
  gate); `POST /:id/test` dispatches a 1-token probe and returns
  verbatim provider error on failure.
- Soft delete (disable + registry.Remove) → hard delete on second
  DELETE; preserves audit link from RoleOverride.

### 3. Frontend (`frontend/src/pages/LLMEndpointsPage.tsx`)
Inline-expandable cards per endpoint. Hints the operator on base-URL
formats per provider (api.minimaxi.com/v1, api.deepseek.com/v1,
localhost:11434/v1 for Ollama, etc.). Auto-derives DefaultModel when
only one model is listed. Test button with live usage readout.

`SettingsPage` model picker merges opencode catalogue + user-registered
endpoints into one list, tagging native rows with a 🔌 badge.

### 4. Native runner (`internal/runner`)
- **Tool interface** — `Name/Description/InputSchema/Execute`.
  RunnerSession gives tools access to AgentSession + journal.
- **Loop** — builds messages from system prompt + user turn, streams
  via llm.Registry, reassembles assistant turn (text + tool_use
  blocks), dispatches tool_use via Registry, feeds results back as
  user turn, exits on text-only stop. Caps at `MaxIterations=20` to
  trip runaway loops; hallucinated tools surfaced as synthetic errors
  so the model can self-correct.
- **4 builtin tools** — `read` (offset/limit, 256 KiB cap),
  `glob` (with `**/` recursion, ignores `.git/node_modules/vendor`),
  `grep` (RE2, per-file match cap, binary-file skip, glob filter),
  `edit` (exact-match single replacement, `replace_all` opt-in,
  atomic temp+rename, refuses overwrite on create).
- **Platform tool adapter** — single generic wrapper over every
  `agent.PlatformTools` entry. Auto-derives JSON Schema from
  `ToolParam` list. Routes calls to `service.HandleToolCallResult`
  so native + opencode runtimes produce **identical** DB side
  effects.
- **Dispatcher** — `agent.RegisterDispatcher(runner.Dispatch)` in
  `main.go`. Routes by `RoleConfig.ModelProvider` prefix:
  `"llm_"*` → native, else → opencode fallback.

**43 unit tests green** (sandbox / read / glob / grep / edit /
loop / platform_tools / dispatcher).

### 5. Wiring (`cmd/server/main.go`)
```go
llm.LoadAll()                              // boot endpoints into Registry
runner.OpencodeFallback = opencode.Dispatch
runner.NativeRegistryBuilder = runner.PlatformRegistryBuilder
runner.PlatformToolSink = service.HandleToolCallResult
agent.RegisterDispatcher(runner.Dispatch)
```

## Test counts

| Package                         | Tests |
| ------------------------------- | ----- |
| `internal/llm` (SSE/retry/cost/anthropic/openai/registry) | 42 |
| `internal/runner` (sandbox/builtins/loop/dispatcher/platform) | 43 |
| **Net-new native stack**        | **85** |
| `go test ./...`                 | all green |

## How to migrate a role

1. In the UI, go to **LLM Endpoints** (left sidebar) and register the
   target endpoint (URL + key + model IDs).
2. In **Settings → Agent Model Configuration**, open the role's picker.
   Rows from your new endpoint are tagged 🔌 with the format name.
3. Click the model row. The next session for that role runs on the
   native runtime; subsequent sessions can be flipped back to opencode
   by re-picking a legacy entry.

## Phase 2 — Live SSE streaming (shipped)

See `@docs/phase2-streaming.md` for details. Summary:

- **Backend**: runner emits 6 event types through `StreamEmitter`
  wired to `service.SSEManager.BroadcastToProject`:
  `CHAT_UPDATE` / `TOOL_CALL` (opencode-compatible shapes, no frontend
  change required) + `AGENT_TEXT_DELTA` / `AGENT_TURN` / `AGENT_DONE`
  / `AGENT_ERROR` (native-runtime extensions).
- **Tool trace persistence**: `recordToolCallTrace` writes one
  `ToolCallTrace` row per tool call so observability queries don't
  branch on runtime.
- **Frontend**: `useSSE` listens for all four new event types. Live
  typewriter via `upsertChatMessage` keyed `stream-${session_id}` —
  each delta re-renders the same chat row in place, `CHAT_UPDATE`
  flips the same row to its final content with zero flicker.
  High-frequency events (`AGENT_TEXT_DELTA`, `AGENT_TURN`) bypass the
  activity feed + broadcast buffer so they don't drown out everything
  else.
- **Tests**: 5 new stream tests covering event ordering, payload
  shape, error paths, and silent-mode (no ProjectID). Total backend
  tests now **95 green**.

## Remaining work

- Migrate platform tool schemas to include examples + error-case
  guidance (Claude Code's `prompt.ts` pattern).
- Retire opencode once every role has been verified on the native
  runtime in production (target: 2 weeks of parallel run).
