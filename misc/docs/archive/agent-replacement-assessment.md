# Replacing opencode with a Native Go Agent Runtime

> **Audience**: the person deciding whether this migration is worth doing.
> **Scope**: Phase-by-phase evaluation grounded in three real codebases we
> have on disk — `coai` (our current platform), `opencode` (our current
> dependency), and the full `claude-code-source` reconstruction at
> `G:\ai\claude-code-source`.

## TL;DR

| | |
|---|---|
| **Feasible?** | Yes. ~1 500–2 000 LoC of new Go for the MVP. |
| **Net complexity change** | Down. One fewer long-lived process + no cross-language ABI gymnastics. |
| **Highest-risk role to migrate** | `RoleFix` / `RoleMerge` (they use opencode's bundled `read`/`edit`/`glob`). |
| **Lowest-risk starting point** | `RoleAnalyze` (only calls our own `analyze_output`). |
| **Claim the migration pays off** | At Phase 3 (all non-file-editing roles running native). ~1 week solo. |
| **Claude Code source useful?** | Very — it's the reference implementation of a modern tool-calling agent runtime. But don't port it; study it. |

---

## 1 · What opencode currently does for us

Reading `internal/opencode/client.go` + `scheduler.go` end-to-end:

| Responsibility | Where it lives |
|---|---|
| Spawn `opencode serve` subprocess on a random port | `InitScheduler`, `findFreePort` |
| Pipe provider API keys via `OPENCODE_CONFIG_CONTENT` | `loadPureConfig` |
| Create / delete / fetch opencode sessions | `client.CreateSession`, `GetMessages`, `DeleteSession` |
| Send a message + agent + model triple and wait | `sendServeMessage`, `pollServeResponse` |
| Parse opencode's streaming output for platform tool calls | `processServeToolCalls`, `processToolCall` (whitelist of 17 names) |
| Drive the **multi-turn tool loop** | opencode internally — we're a consumer |
| Built-in `read` / `edit` / `glob` file tools | opencode |
| Multi-provider routing (Anthropic / MiniMax / OpenAI / Gemini / xAI) | opencode |
| Retry policy | `maybeRetry` + opencode's own |
| Per-role model override | `RoleOverride` table passed to opencode via `model=provider/id` |

That's the entire dependency surface. **Everything else** — role definitions, prompt templates, tool handler dispatch, session persistence in `AgentSession`, audit verdict handling, knowledge refinery — is ours already and wouldn't move.

## 2 · What Claude Code teaches us

`G:\ai\claude-code-source\src\` is a full reconstruction of the Claude
Code CLI. Ignore the TUI bits (Ink, React, Bun). Three modules matter for
our question:

### 2.1 `Tool.ts` — the tool abstraction shape

Reduced to essence, every tool is:

```ts
type Tool<Input, Output> = {
  name: string
  aliases?: string[]
  searchHint?: string                 // for ToolSearch deferred loading
  inputSchema: z.ZodType<Input>        // Zod → JSON Schema on the wire
  outputSchema?: z.ZodType<Output>
  isEnabled(): boolean
  isReadOnly(input): boolean
  isDestructive?(input): boolean
  isConcurrencySafe(input): boolean
  validateInput?(input, ctx): ValidationResult    // pre-check
  checkPermissions(input, ctx): PermissionResult  // gate
  call(input, ctx, canUseTool, parent, onProgress): Promise<ToolResult>
  description(input, opts): string     // LLM-facing blurb
  prompt(opts): string                 // system-prompt contribution
}
```

Our Go `ToolCallHandler` is one function handling 17 names via `switch`.
Claude Code's is 50+ tools each as a typed object. The Claude Code shape
is worth copying because:

- Explicit `isReadOnly` / `isDestructive` flags drive the permission
  engine without hand-coded logic per call site
- `validateInput` vs `checkPermissions` separation — syntactic vs policy
- `call` receives `onProgress` so streaming tool execution (e.g. a long
  bash) can push updates without ending the turn

### 2.2 `QueryEngine.ts` + `query.ts` — the agent loop

Claude Code's main loop (paraphrased):

```
initial user message
  → assemble system prompt + message history + tool definitions
  → call Anthropic streaming API
  → as chunks arrive:
      - content_block_delta → append text
      - tool_use content block → buffer args
  → on message_stop:
      - if stop_reason == "tool_use": for each tool_use block,
          run canUseTool → tool.checkPermissions → tool.call
          append tool_result; goto "call Anthropic"
      - if stop_reason == "end_turn": return to user
```

The loop is ~150 lines of actual logic plus heavy error handling.
Non-trivial but bounded. **This is exactly what opencode does for us
today, and it's what we'd replace.**

### 2.3 `services/api/claude.ts` — the raw Anthropic interface

Claude Code uses `@anthropic-ai/sdk` directly. No abstraction above it.
The streaming API returns server-sent events; the SDK parses them into
typed events (`message_start`, `content_block_start`, `content_block_delta`,
`content_block_stop`, `message_delta`, `message_stop`).

The Go Anthropic SDK (`github.com/anthropics/anthropic-sdk-go`) exposes
the same events. The work is manageable.

## 3 · What we'd build

### Package shape

```
platform/backend/internal/llm/
├── provider.go       — Provider interface (Chat, ChatStream)
├── anthropic.go      — Anthropic adapter
├── minimax.go        — MiniMax adapter (we use MiniMax-M2.7 today)
├── types.go          — Message, ToolDef, ToolCall, ToolResult, StreamEvent
└── testing.go        — in-memory fake for tests

platform/backend/internal/agent/runner/
├── runner.go         — the tool loop (replaces runAgentViaServe)
├── tools.go          — register role-specific tool set from RoleConfig
├── bultin/           — Go-native replacements for opencode's read/edit/glob
│   ├── read.go
│   ├── edit.go
│   └── glob.go
└── runner_test.go
```

### LoC estimate

| Module | LoC |
|---|---:|
| `llm/anthropic.go` (streaming + tool-use parsing) | ~400 |
| `llm/minimax.go` (OpenAI-compatible API) | ~200 |
| `llm/provider.go` + `types.go` | ~150 |
| `agent/runner/runner.go` (tool loop + abort) | ~350 |
| `agent/runner/tools.go` (registry binding) | ~150 |
| `agent/runner/builtin/{read,edit,glob}.go` | ~400 |
| Streaming → SSE broadcast glue | ~100 |
| Config loader (provider API keys) | ~50 |
| Tests | ~400 |
| **Total MVP** | **~2 200 LoC** |

For comparison: our CHANGELOG hardening pass touched ~2 000 LoC in one
commit. This is the same order of magnitude.

### Interface compatibility

The `Scheduler.Dispatch(session *agent.Session)` signature is stable
enough to keep. Underneath, the runner picks between:

```go
// internal/agent/runner/runner.go
func Dispatch(sess *agent.Session) error {
    if config.UseNativeRuntime(sess.Role) {
        return NativeDispatch(sess)
    }
    return opencode.DefaultScheduler.Dispatch(sess)
}
```

This is the migration shape — env-flag-gated per role, side-by-side for
as long as needed.

## 4 · Staged migration plan

Each phase is independently shippable and reversible.

### Phase 0 — Build `llm/anthropic.go` (1–2 days)

- Wrap `github.com/anthropics/anthropic-sdk-go`
- Expose `ChatStream(ctx, req) → <-chan StreamEvent`
- Unit test against recorded fixtures
- **No integration with existing agents yet**

### Phase 1 — Build `runner.go` + migrate `RoleAnalyze` (2–3 days)

- Analyze is the lowest-risk role: only calls our own `analyze_output`
  tool, no file editing, runs async on a schedule
- Add env flag `A3C_NATIVE_ROLES=analyze`
- Shadow-run for 3–5 Analyze cycles and diff outputs vs opencode
- **Rollback = unset env flag**

### Phase 2 — Migrate read-only roles (2–3 days)

`RoleConsult`, `RoleAssess`, `RoleAudit1`, `RoleAudit2`, `RoleEvaluate`, `RoleChief`.
These need `read` + `glob` — both trivial Go.

At this point 6 of 11 roles are native; opencode still handles the 5
file-editing roles.

### Phase 3 — Migrate `RoleMaintain` (1 day)

Calls `edit` but its own platform tools dominate the turn. Test ground for `edit`.

### Phase 4 — Migrate coder roles (2–3 days)

`RoleFix`, `RoleMerge`. Real `edit` usage, higher risk of regressions.
Add integration tests against `test_full_flow.ps1` before cut-over.

### Phase 5 — Remove opencode (half day)

- Delete `internal/opencode/`
- Remove `pure-opencode.json` + `.opencode/`
- Drop `opencode` subprocess from `start.ps1`
- Update CHANGELOG

**Total: ~1.5 – 2 weeks full-time, or ~3–4 weeks at evening pace.**

## 5 · What we gain

1. **One fewer process** — backend + embedder instead of backend + opencode + embedder. `start.ps1` drops a step.
2. **No more opencode upgrade pain** — CHANGELOG already records a zod-v3-vs-v4 crash that had to be worked around (`.opencode` trickery, serveWorkDir). That class of bug vanishes.
3. **Direct streaming into SSE** — today opencode output is polled every N seconds by `pollServeResponse`. Native streaming lets LLM tokens hit the frontend as they're generated, without the polling latency.
4. **Honest tool dispatch** — currently `processServeToolCalls` reverse-engineers opencode's stream format. Native, we own the parse.
5. **Tighter cost tracking** — usage tokens per-role, per-provider, per-session become first-class metrics (PR 9's injection-signal metrics can be extended to cost-per-signal).
6. **No opencode install requirement** — new contributors can `go run` the backend without installing opencode/bun.

## 6 · What we lose (honestly)

1. **Free multi-provider matrix** — opencode ships with Anthropic / MiniMax / OpenAI / Gemini / xAI / Bedrock / Vertex / Groq configs. We'd rebuild whichever ones we need. Today we use 1 (minimax-coding-plan). Rebuilding that one is trivial.
2. **opencode's MCP client** — if we ever wired external MCP servers through opencode we'd need to replace with something like `github.com/metoro-io/mcp-golang`. Grep confirms we don't rely on this today.
3. **Their auth / credential UX** — opencode handles OAuth for Bedrock/Vertex. We don't use those yet; bridge later if needed.

## 7 · Concrete next step

The diff between doing this and not doing it is the existence of one file:

```
platform/backend/internal/llm/anthropic.go
```

If that file exists with a passing `TestChatStream_ReturnsToolUseBlocks`
test against a recorded Anthropic response, the entire migration is
de-risked. Everything downstream is mechanical.

**Recommendation**: spend 1 day writing `anthropic.go` + test fixtures
inside `coai2`. If that's clean, continue to Phase 1 in `coai2`. If
either phase reveals a dealbreaker (streaming semantics, tool-use
latency, rate-limit handling), we abort with zero damage to `coai`.

## 8 · Appendix: reading Claude Code responsibly

Do:
- Read `Tool.ts` for the tool abstraction shape — copy it
- Read `QueryEngine.ts` for the loop skeleton — paraphrase it
- Read `tools/FileReadTool/FileReadTool.ts` for a concrete tool example

Don't:
- Port the TUI (`ink.ts`, `components/*`) — we have our own frontend
- Port their plugin system — out of scope
- Port their 50 tools wholesale — we have 17 platform tools + 3 file tools, that's the target surface
- Port their hook system yet — defer until we have concrete need

`main.tsx` (808 KB) is a bundle artifact. Read only the per-module
sources in `src/`. The real logic density is under ~50 files.
