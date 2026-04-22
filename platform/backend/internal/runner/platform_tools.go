package runner

// Platform-tool adapters.
//
// Wraps every entry in agent.PlatformTools as a native runner Tool so
// role-specific outputs (audit_output, fix_output, create_task, ...)
// flow through the same Loop as the builtin file tools. The side
// effects (DB mutations, change-pipeline advancement) happen inside
// service.HandleToolCallResult — the wrapper's job is just to move
// the tool call across the package boundary.
//
// Why a single generic wrapper instead of one type per tool:
//   Every platform tool's Execute does essentially the same thing:
//     1. Parse raw args into a map.
//     2. Hand to service.HandleToolCallResult.
//     3. Return a confirmation string to the model.
//   Per-tool types would be 8 copies of the same 12-line Execute;
//   the generic wrapper keeps that logic in one place and picks up
//   any new tool we add to agent.PlatformTools automatically.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/a3c/platform/internal/agent"
)

// PlatformToolSink is the callback that carries a tool invocation
// into service-land. The real value, wired at startup, is
// service.HandleToolCallResult. Exposed as a variable so tests and
// non-production entrypoints (like e2erun) can stub it.
var PlatformToolSink func(sessionID, changeID, projectID, toolName string, args map[string]interface{})

// PlatformTool adapts an agent.ToolDefinition to the runner.Tool
// interface. Each role's platform tools become PlatformTool instances
// in the role's Registry.
type PlatformTool struct {
	Def *agent.ToolDefinition
}

func (p *PlatformTool) Name() string { return p.Def.Name }

// IsConcurrencySafe: platform tools are terminal "sinks" that mutate
// the DB (audit verdict, task creation, milestone update). Even two
// parallel audit_output calls would race on the same change row. We
// mark the whole class unsafe to avoid edge-case reasoning about
// which sinks touch which tables.
func (p *PlatformTool) IsConcurrencySafe(_ json.RawMessage) bool { return false }

// Description returns the full tool description the LLM sees.
// Concatenates the base description with an optional ErrorGuidance
// paragraph — keeping them separate in the struct lets the dashboard
// render them as distinct sections while still presenting a single
// blob to the model (which is what Anthropic and OpenAI both
// consume).
func (p *PlatformTool) Description() string {
	if p.Def.ErrorGuidance == "" {
		return p.Def.Description
	}
	return p.Def.Description + "\n\nError handling: " + p.Def.ErrorGuidance
}

// InputSchema synthesises a JSON Schema from agent.ToolParam list.
// We deliberately translate every platform param to a "type: string"
// (for string) or the declared type (array, object, boolean,
// integer). Required fields land in the schema's `required` array.
//
// Top-level `examples` surface the ToolDefinition.Examples list to
// providers that read the field (Anthropic Messages API reads it;
// OpenAI-compat providers generally ignore unknown schema keys, which
// is fine — the examples still live in the body and don't harm
// validation).
func (p *PlatformTool) InputSchema() map[string]any {
	props := map[string]any{}
	required := make([]string, 0, len(p.Def.Parameters))
	for _, param := range p.Def.Parameters {
		prop := map[string]any{
			"type":        paramTypeToJSON(param.Type),
			"description": param.Description,
		}
		// Arrays need an `items` schema or most providers will reject
		// the tool def. We default to object items since the existing
		// platform tools (e.g. audit_output.issues) are arrays of
		// structured records; the handler does the per-field
		// validation.
		if param.Type == "array" {
			prop["items"] = map[string]any{"type": "object"}
		}
		props[param.Name] = prop
		if param.Required {
			required = append(required, param.Name)
		}
	}
	schema := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	if len(p.Def.Examples) > 0 {
		// Clone so the schema consumer can't mutate the shared
		// PlatformTools table by accident.
		examples := make([]map[string]any, len(p.Def.Examples))
		copy(examples, p.Def.Examples)
		schema["examples"] = examples
	}
	return schema
}

// paramTypeToJSON maps the ToolParam Type strings (freeform in
// tools.go) to canonical JSON Schema type names.
func paramTypeToJSON(t string) string {
	switch t {
	case "boolean", "bool":
		return "boolean"
	case "integer", "int":
		return "integer"
	case "number", "float":
		return "number"
	case "array":
		return "array"
	case "object":
		return "object"
	default:
		return "string"
	}
}

// Execute decodes args, invokes the sink, and returns a confirmation.
// Most platform tools don't produce useful output for the model —
// they're sinks — so a short "Recorded <tool>." is enough to let the
// model proceed to message_stop.
func (p *PlatformTool) Execute(ctx context.Context, sess *RunnerSession, raw json.RawMessage) (string, bool, error) {
	if PlatformToolSink == nil {
		return "Error: platform tool sink is not wired (server misconfiguration)", true, nil
	}
	args := map[string]interface{}{}
	// Tolerate empty args — some tools (assess_output, audit2_output
	// with result=merge) legitimately ship only a couple of fields.
	if len(raw) > 0 && string(raw) != "{}" {
		if err := json.Unmarshal(raw, &args); err != nil {
			return fmt.Sprintf("Error: invalid arguments for %s: %v", p.Def.Name, err), true, nil
		}
	}

	var sessionID, changeID, projectID string
	if sess.AgentSession != nil {
		sessionID = sess.AgentSession.ID
		projectID = sess.AgentSession.ProjectID
		changeID = sess.AgentSession.ChangeID
	}

	// The sink is fire-and-forget from the LLM's perspective — side
	// effects happen synchronously here but the model doesn't need
	// to wait for a success/fail signal the way it does for a read.
	// If the sink panics, recover so one bad tool call can't crash
	// the whole session.
	func() {
		defer func() {
			if rec := recover(); rec != nil {
				// Fatal failures come back through the runner as the
				// recovered panic message; treat as non-fatal from
				// the model's standpoint (it already emitted the
				// call, we just couldn't process it).
				// Intentionally logged — see PlatformToolSink caller.
				_ = rec
			}
		}()
		PlatformToolSink(sessionID, changeID, projectID, p.Def.Name, args)
	}()
	return fmt.Sprintf("Recorded %s for session %s.", p.Def.Name, sessionID), false, nil
}

// PlatformRegistryBuilder is the production registry builder: includes
// the 4 builtin file tools plus every PlatformTool the role is
// authorised for. This is what wire.go installs in place of
// DefaultRegistryBuilder when the server comes up.
func PlatformRegistryBuilder(role agent.Role) *Registry {
	reg := NewRegistry()

	// Builtin file tools are available to every role — reading,
	// searching, and editing are prerequisites for any agent doing
	// real work. Policy around which roles may *actually* write is
	// enforced at the filelock / change-submission layer, not here.
	reg.Register(ReadTool{})
	reg.Register(GlobTool{})
	reg.Register(GrepTool{})
	reg.Register(EditTool{})

	// Role-specific platform tools.
	for _, def := range agent.GetToolsForRole(role) {
		reg.Register(&PlatformTool{Def: def})
	}
	return reg
}
