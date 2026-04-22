package runner

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/a3c/platform/internal/agent"
)

// installFakeSink swaps PlatformToolSink with a recorder for the
// duration of the calling test. The returned pointer's Calls slice
// captures every invocation in order.
func installFakeSink(t *testing.T) *sinkRecorder {
	t.Helper()
	prev := PlatformToolSink
	rec := &sinkRecorder{}
	PlatformToolSink = rec.Record
	t.Cleanup(func() { PlatformToolSink = prev })
	return rec
}

type sinkRecorder struct {
	mu    sync.Mutex
	Calls []sinkCall
}

type sinkCall struct {
	SessionID string
	ChangeID  string
	ProjectID string
	ToolName  string
	Args      map[string]interface{}
}

func (r *sinkRecorder) Record(sessionID, changeID, projectID, toolName string, args map[string]interface{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Calls = append(r.Calls, sinkCall{sessionID, changeID, projectID, toolName, args})
}

func TestPlatformTool_ExposesSchemaFromDefinition(t *testing.T) {
	def := agent.PlatformTools["create_task"]
	if def == nil {
		t.Skip("create_task definition not found")
	}
	tool := &PlatformTool{Def: def}
	schema := tool.InputSchema()
	if schema["type"] != "object" {
		t.Errorf("root schema type: %v", schema["type"])
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("properties missing")
	}
	nameProp, ok := props["name"].(map[string]any)
	if !ok {
		t.Fatalf("name property missing: %+v", props)
	}
	if nameProp["type"] != "string" {
		t.Errorf("name type: %v", nameProp["type"])
	}
	// Required field for create_task is "name".
	required, _ := schema["required"].([]string)
	if len(required) == 0 || required[0] != "name" {
		t.Errorf("required list: %v", required)
	}
}

func TestPlatformTool_ArraySchemaHasItems(t *testing.T) {
	// audit_output.issues is an array parameter — must emit "items"
	// or providers (particularly OpenAI-compatible) reject the def.
	def := agent.PlatformTools["audit_output"]
	tool := &PlatformTool{Def: def}
	schema := tool.InputSchema()
	props := schema["properties"].(map[string]any)
	issues, ok := props["issues"].(map[string]any)
	if !ok {
		t.Fatalf("issues prop missing: %+v", props)
	}
	if issues["type"] != "array" {
		t.Errorf("issues type: %v", issues["type"])
	}
	if _, hasItems := issues["items"]; !hasItems {
		t.Error("array prop must declare an items schema")
	}
}

func TestPlatformTool_InvokesSink(t *testing.T) {
	rec := installFakeSink(t)
	def := &agent.ToolDefinition{
		Name:        "test_tool",
		Description: "fake",
		Parameters:  []agent.ToolParam{{Name: "x", Type: "string", Required: true}},
	}
	tool := &PlatformTool{Def: def}
	sess := &RunnerSession{
		AgentSession: &agent.Session{
			ID:        "sess1",
			ProjectID: "proj1",
			ChangeID:  "chg1",
		},
	}
	result, isErr, fatal := tool.Execute(context.Background(), sess,
		json.RawMessage(`{"x":"hello"}`))
	if fatal != nil || isErr {
		t.Fatalf("unexpected: isErr=%v fatal=%v result=%q", isErr, fatal, result)
	}
	if len(rec.Calls) != 1 {
		t.Fatalf("expected 1 sink invocation, got %d", len(rec.Calls))
	}
	call := rec.Calls[0]
	if call.SessionID != "sess1" || call.ProjectID != "proj1" || call.ChangeID != "chg1" || call.ToolName != "test_tool" {
		t.Errorf("sink call wrong: %+v", call)
	}
	if call.Args["x"] != "hello" {
		t.Errorf("args: %+v", call.Args)
	}
	if !strings.Contains(result, "Recorded") {
		t.Errorf("result missing 'Recorded': %q", result)
	}
}

func TestPlatformTool_RejectsBadJSON(t *testing.T) {
	installFakeSink(t)
	tool := &PlatformTool{Def: &agent.ToolDefinition{Name: "x"}}
	result, isErr, _ := tool.Execute(context.Background(), &RunnerSession{AgentSession: &agent.Session{}},
		json.RawMessage(`{not valid json`))
	if !isErr {
		t.Error("bad JSON should surface is_error")
	}
	if !strings.Contains(result, "invalid arguments") {
		t.Errorf("unhelpful error: %q", result)
	}
}

func TestPlatformTool_ErrorsWhenSinkMissing(t *testing.T) {
	prev := PlatformToolSink
	PlatformToolSink = nil
	defer func() { PlatformToolSink = prev }()

	tool := &PlatformTool{Def: &agent.ToolDefinition{Name: "x"}}
	result, isErr, _ := tool.Execute(context.Background(), &RunnerSession{AgentSession: &agent.Session{}},
		json.RawMessage(`{}`))
	if !isErr {
		t.Error("missing sink should error")
	}
	if !strings.Contains(result, "sink is not wired") {
		t.Errorf("error message unclear: %q", result)
	}
}

func TestPlatformRegistryBuilder_IncludesBuiltinsAndRoleTools(t *testing.T) {
	reg := PlatformRegistryBuilder(agent.RoleAudit1)
	names := map[string]bool{}
	for _, t := range reg.List() {
		names[t.Name()] = true
	}
	// Builtins: always present.
	for _, b := range []string{"read", "glob", "grep", "edit"} {
		if !names[b] {
			t.Errorf("builtin %q missing for audit_1", b)
		}
	}
	// audit_1 role-specific tool.
	if !names["audit_output"] {
		t.Error("audit_output should be registered for audit_1 role")
	}
	// Maintain-only tool should NOT appear on audit_1.
	if names["create_task"] {
		t.Error("create_task is maintain-only; leaked onto audit_1")
	}
}

func TestPlatformRegistryBuilder_MaintainRoleGetsWritingTools(t *testing.T) {
	reg := PlatformRegistryBuilder(agent.RoleMaintain)
	names := map[string]bool{}
	for _, t := range reg.List() {
		names[t.Name()] = true
	}
	for _, expected := range []string{"create_task", "update_milestone", "propose_direction"} {
		if !names[expected] {
			t.Errorf("maintain role missing %q", expected)
		}
	}
}

func TestPlatformTool_ExamplesLandInSchema(t *testing.T) {
	// Every platform tool ships with >=1 example after the Phase 3
	// schema enhancement. Regression guard: if someone adds a new
	// tool without examples, this fails loudly.
	for name, def := range agent.PlatformTools {
		if len(def.Examples) == 0 {
			t.Errorf("tool %q has no examples; add at least one for small-model reliability", name)
			continue
		}
		tool := &PlatformTool{Def: def}
		schema := tool.InputSchema()
		got, ok := schema["examples"].([]map[string]any)
		if !ok {
			t.Errorf("tool %q schema.examples wrong type: %T", name, schema["examples"])
			continue
		}
		if len(got) != len(def.Examples) {
			t.Errorf("tool %q schema examples count: got %d, want %d", name, len(got), len(def.Examples))
		}
	}
}

func TestPlatformTool_DescriptionIncludesErrorGuidance(t *testing.T) {
	// audit_output's ErrorGuidance tells the model what to do when
	// the diff is unrelated. The LLM must see this content in the
	// description it receives.
	def := agent.PlatformTools["audit_output"]
	if def.ErrorGuidance == "" {
		t.Skip("audit_output has no error guidance (schema regressed?)")
	}
	tool := &PlatformTool{Def: def}
	desc := tool.Description()
	if !strings.Contains(desc, "Error handling:") {
		t.Errorf("description missing error-handling preamble: %q", desc)
	}
	if !strings.Contains(desc, def.ErrorGuidance) {
		t.Errorf("description didn't include ErrorGuidance text")
	}
}

func TestPlatformTool_DescriptionWithoutErrorGuidance(t *testing.T) {
	// audit2_output currently has no ErrorGuidance; description
	// should be returned verbatim without the "Error handling:" tail.
	def := agent.PlatformTools["audit2_output"]
	tool := &PlatformTool{Def: def}
	desc := tool.Description()
	if strings.Contains(desc, "Error handling:") {
		t.Errorf("tool with no ErrorGuidance should not emit the preamble: %q", desc)
	}
	if desc != def.Description {
		t.Errorf("description should match Def.Description exactly; got %q", desc)
	}
}

func TestPlatformTool_ExamplesAreCloned(t *testing.T) {
	// Mutating the returned schema must not mutate the shared
	// PlatformTools table. Defensive copy paranoia.
	def := agent.PlatformTools["audit_output"]
	tool := &PlatformTool{Def: def}
	schema := tool.InputSchema()
	examples := schema["examples"].([]map[string]any)
	originalLen := len(def.Examples)

	// Try to corrupt via the returned slice.
	examples[0] = map[string]any{"level": "CORRUPTED"}

	if len(def.Examples) != originalLen {
		t.Errorf("PlatformTools mutated via schema slice — defensive copy failed")
	}
	if v, _ := def.Examples[0]["level"].(string); v == "CORRUPTED" {
		t.Error("first example was mutated through the returned schema")
	}
}
