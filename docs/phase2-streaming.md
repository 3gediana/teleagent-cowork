# Phase 2 — Live SSE Streaming

> Native-runtime sessions now stream tokens to the dashboard the same
> way opencode sessions do, plus four new event types that expose
> runtime internals (turn summaries, errors) the legacy path couldn't.

## Event catalogue

| Event                 | Shape                                                                 | Legacy / Native | Frontend behaviour                                                   |
| --------------------- | --------------------------------------------------------------------- | --------------- | -------------------------------------------------------------------- |
| `CHAT_UPDATE`         | `{role, content, session_id?}`                                        | Both            | Appends (or upserts if `session_id` present) the final agent reply. |
| `TOOL_CALL`           | `{session_id, tool, args}`                                            | Both            | Pushes a "Tool called: X" system message + refreshes state.         |
| `AGENT_TEXT_DELTA`    | `{session_id, iteration, delta}`                                      | Native only     | Typewriter. Accumulates into a message keyed `stream-${session_id}`. |
| `AGENT_TURN`          | `{session_id, iteration, input_tokens, output_tokens, tool_count}`    | Native only     | Kept for dashboard telemetry; no user-facing render yet.            |
| `AGENT_DONE`          | `{session_id, iterations, input_tokens, output_tokens, cost_usd}`     | Native only     | Fires `refreshState()` so lock / task / PR changes surface.         |
| `AGENT_ERROR`         | `{session_id, error}`                                                 | Native only     | Red system message in chat; cleans up streaming placeholder.        |

The `session_id` field on `CHAT_UPDATE` is a native-runtime extension —
opencode doesn't emit it. Frontend code handles both: when present,
the final message replaces the live-streaming one in place (zero
flicker); when absent, it appends a new row as it always has.

## Wire path

```
runner.Run                                    [runner/loop.go]
  emit()   ── StreamEmitter ──>               [runner/stream.go]
                                service.SSEManager.BroadcastToProject
                                               [service/broadcast.go]
       ├── Redis LPush (retention + resume)
       └── in-memory fanout to every SSEClient
                                        ↓
                                EventSource onmessage
                                               [frontend/hooks/useSSE.ts]
```

The callback hand-off (`runner.StreamEmitter = func(...) { SSEManager.BroadcastToProject(...) }`)
lives in `cmd/server/main.go` to keep `runner` free of the
`service` import.

## Backpressure and noise

- `AGENT_TEXT_DELTA` and `AGENT_TURN` fire frequently (≥30 Hz at peak).
  `useSSE.highFreq` suppresses their activity-feed + broadcast-buffer
  entries and their `console.log` output so devtools stays usable.
- `recordToolCallTrace` runs in a goroutine (`go func() { model.DB.Create(...) }()`)
  so a slow DB can't pause the conversation loop.
- SSE client channel is buffered (`cap=10`) and `select { case ... default }`
  drops events when the client is slow — preferable to blocking the
  whole broadcast path. Clients replay on reconnect from Redis.

## Test coverage

5 new tests in `internal/runner/stream_test.go`:

- **TestStream_EmitsChatUpdateAndAgentDoneOnTextOnlyReply** — asserts
  exact event sequencing, payload shape (role/content/session_id/iterations/output_tokens),
  and per-project fanout.
- **TestStream_EmitsToolCallBeforeExecutionAndDoneAfter** — TOOL_CALL
  fires *before* tool Execute runs; AGENT_TURN count matches iterations;
  zero AGENT_ERROR on happy path.
- **TestStream_EmitsAgentErrorOnLivelock** — livelock path emits
  exactly one AGENT_ERROR, zero AGENT_DONE.
- **TestStream_NoProjectIDSilent** — sessions without a ProjectID
  (CLI / test harness) emit zero events and don't crash.
- **TestStream_AgentTurnCarriesIterationAndToolCount** — per-turn
  payload fields are accurate across multi-turn runs.

Total backend test count: **95** (42 LLM + 43 runner + 5 stream +
5 platform-tools bundled into the runner suite).

## Verification commands

```sh
# Backend
cd D:/claude-code/coai2/platform/backend
go test ./...
go test ./internal/runner/... -run TestStream -v

# Frontend
cd D:/claude-code/coai2/frontend
npx tsc --noEmit
```
