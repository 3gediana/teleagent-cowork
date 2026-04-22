package runner

// Tool abstraction for the native agent runtime.
//
// Design follows Claude Code's tool model (see G:\ai\claude-code-source\src):
//   - Each tool is a Go type that knows its own schema + how to execute
//     a single call. The runner owns the dispatch loop, the streaming
//     channel, and state persistence — tools are pure functions from
//     "this invocation's arguments + ambient session context" to "a
//     string (or structured JSON) that becomes the tool_result turn".
//   - Tools self-describe their JSON schema so the runner can hand a
//     list of llm.ToolDef straight to the provider without the tool
//     author duplicating anything.
//   - Concurrency boundaries: each Tool.Execute runs on its own
//     goroutine inside the runner. Tools that mutate shared state (DB,
//     filesystem) must take whatever locks they need — the runner
//     doesn't know what invariants a given tool has to preserve.

import (
	"context"
	"encoding/json"

	"github.com/a3c/platform/internal/agent"
)

// Tool is the contract every native tool implements. Both "platform
// tools" (audit_output, create_task, ...) and "builtin file tools"
// (read, glob, grep, edit) satisfy this interface.
type Tool interface {
	// Name is the id the LLM sees in the tools array. Must be unique
	// within a given role's tool set.
	Name() string

	// IsConcurrencySafe reports whether this tool can run in parallel
	// with siblings inside the same assistant turn. The loop
	// partitions a turn's tool_use blocks into consecutive safe-batches
	// (run concurrently up to a cap) and unsafe-singletons (run
	// serially). Safe = "no mutation of shared state (disk, DB,
	// external service) that another concurrent call could collide
	// with". Pure reads (read/glob/grep) are safe; writes (edit,
	// platform sinks) are not.
	//
	// The signature takes the raw JSON input so tools can decide
	// per-call (e.g. a hypothetical bash tool could mark `ls` safe and
	// `rm` unsafe). For the current tool set nobody uses the input
	// argument — pass raw through.
	IsConcurrencySafe(input json.RawMessage) bool

	// Description is the natural-language help string the model reads
	// to decide when to call the tool. Short + instructive — Claude
	// Code's experience is that 2–3 sentences outperforms a wall of
	// text (model gets overwhelmed).
	Description() string

	// InputSchema returns the JSON Schema for this tool's arguments.
	// Runner forwards this verbatim to llm.ToolDef.Schema. Keeping the
	// schema inline with the code (instead of a separate JSON file)
	// makes refactors atomic.
	InputSchema() map[string]any

	// Execute runs one invocation. input is the raw JSON from the
	// model — already validated against the schema by the runner.
	// ctx provides ambient session state + cancellation; sess gives
	// access to the role, project, and output channels.
	//
	// Returns:
	//   - result: the tool_result payload. String for simple outputs,
	//     structured JSON (marshalled) for richer results. Passed
	//     verbatim into the next turn's tool_result content block.
	//   - isError: true if the tool failed in a way the model should
	//     see (vs. a bug the runner should terminate on). When true,
	//     result should be a human-readable error message.
	//   - fatal: non-nil when execution hit an infrastructure failure
	//     (DB down, permission revoked, etc.) that should stop the
	//     whole session instead of being handed back to the model.
	Execute(ctx context.Context, sess *RunnerSession, input json.RawMessage) (result string, isError bool, fatal error)
}

// RunnerSession is the live state a Tool's Execute method can touch.
// It wraps agent.Session with mutation helpers the runner knows how to
// coordinate (output streaming, session-local journal, etc.).
//
// Kept as a struct rather than an interface because the tool set is
// tightly coupled to the runner anyway — an interface wouldn't buy
// decoupling, only ceremony.
type RunnerSession struct {
	// AgentSession is the persistent agent.Session (same pointer as
	// the dispatcher sees). Tools mutate AgentSession.Output in place
	// for streaming, but must NOT touch Status — the runner owns
	// status transitions.
	AgentSession *agent.Session

	// Journal records every tool call (args + result + elapsed) so the
	// post-run auditor can reconstruct what happened. Opaque to tools;
	// the runner appends.
	Journal []JournalEntry

	// EndpointID identifies the llm.Registry entry routing this run.
	// Tools that want to emit sub-requests to the same LLM (like
	// summarise-large-output) use this to stay on the same endpoint.
	EndpointID string

	// Model is the concrete model id that was chosen for this run
	// (after RoleOverride + registry defaults). Tools MAY call the
	// model themselves for sub-steps, but must pass this value
	// through or they'll route to whatever the registry's fallback is.
	Model string
}

// JournalEntry is one row of the tool-call log. Flattened to flat
// fields (rather than nesting Input/Output) because this is what the
// DB persists and what dashboards render — extra nesting just means
// JSON.stringify + custom views everywhere.
type JournalEntry struct {
	ToolName    string          `json:"tool_name"`
	Input       json.RawMessage `json:"input"`
	Output      string          `json:"output"`
	IsError     bool            `json:"is_error"`
	ElapsedMs   int64           `json:"elapsed_ms"`
}

// Registry is the per-role tool bundle. Built once at session start
// from agent.GetToolsForRole(role) + the role's share of builtin
// tools (read, glob, grep, edit).
//
// The runner looks up Tool by name when processing a tool_use event
// from the LLM; names are case-sensitive because the model echoes
// back exactly what we gave it.
type Registry struct {
	tools map[string]Tool
}

func NewRegistry() *Registry {
	return &Registry{tools: map[string]Tool{}}
}

func (r *Registry) Register(t Tool) {
	r.tools[t.Name()] = t
}

// Get returns the Tool with the given name, or nil if none. Callers
// treat nil as "model hallucinated a tool name" and feed a synthetic
// error back to the model so it can self-correct.
func (r *Registry) Get(name string) Tool {
	return r.tools[name]
}

// List returns every registered tool in deterministic order (sorted
// by name). Used when building the llm.ToolDef list for a request.
func (r *Registry) List() []Tool {
	names := make([]string, 0, len(r.tools))
	for n := range r.tools {
		names = append(names, n)
	}
	// cheap sort avoids importing sort.Strings — the tool set is small
	// so bubble sort is fine and keeps the dep surface minimal.
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j-1] > names[j]; j-- {
			names[j-1], names[j] = names[j], names[j-1]
		}
	}
	out := make([]Tool, 0, len(names))
	for _, n := range names {
		out = append(out, r.tools[n])
	}
	return out
}
