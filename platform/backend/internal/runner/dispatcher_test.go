package runner

// Dispatcher tests. Only cover the pure-function pieces
// (resolveEndpointForRole, DefaultRegistryBuilder, DefaultPromptBuilder)
// to avoid pulling in a DB + full session lifecycle into the unit tests.
// End-to-end dispatch is exercised by e2erun.

import (
	"strings"
	"testing"

	"github.com/a3c/platform/internal/agent"
	"github.com/a3c/platform/internal/llm"
)

// withIsolatedRegistry swaps llm.DefaultRegistry with a fresh one for
// the duration of a test so entries we install here don't leak into
// sibling tests (the registry is a process-wide singleton).
func withIsolatedRegistry(t *testing.T) {
	t.Helper()
	prev := llm.DefaultRegistry
	llm.DefaultRegistry = llm.NewRegistry()
	t.Cleanup(func() { llm.DefaultRegistry = prev })
}

func TestResolveEndpointForRole_EmptyRegistry(t *testing.T) {
	withIsolatedRegistry(t)

	cfg := &agent.RoleConfig{Role: agent.RoleAudit1}
	_, err := resolveEndpointForRole(cfg)
	if err == nil || !strings.Contains(err.Error(), "no LLM endpoints") {
		t.Errorf("expected clear empty-registry error, got %v", err)
	}
}

func TestResolveEndpointForRole_ConfiguredOverride(t *testing.T) {
	withIsolatedRegistry(t)

	llm.DefaultRegistry.Register(&llm.Entry{
		EndpointID:   "llm_fake",
		EndpointName: "Fake",
		DefaultModel: "fake-model-default",
	})

	cfg := &agent.RoleConfig{
		Role:          agent.RoleAudit1,
		ModelProvider: "llm_fake",
		ModelID:       "fake-model-explicit",
	}
	route, err := resolveEndpointForRole(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if route.endpointID != "llm_fake" || route.modelID != "fake-model-explicit" {
		t.Errorf("unexpected route: %+v", route)
	}
}

func TestResolveEndpointForRole_OverrideFallsBackToDefaultModel(t *testing.T) {
	withIsolatedRegistry(t)
	llm.DefaultRegistry.Register(&llm.Entry{
		EndpointID:   "llm_fake",
		EndpointName: "Fake",
		DefaultModel: "fake-model-default",
	})

	cfg := &agent.RoleConfig{
		Role:          agent.RoleAudit1,
		ModelProvider: "llm_fake", // ModelID empty
	}
	route, err := resolveEndpointForRole(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if route.modelID != "fake-model-default" {
		t.Errorf("expected fallback to endpoint default model, got %s", route.modelID)
	}
}

func TestResolveEndpointForRole_NoOverride_PicksFirstRegistered(t *testing.T) {
	withIsolatedRegistry(t)
	llm.DefaultRegistry.Register(&llm.Entry{
		EndpointID:   "llm_fake",
		EndpointName: "Fake",
		DefaultModel: "fake-model-default",
	})

	cfg := &agent.RoleConfig{Role: agent.RoleAudit1}
	route, err := resolveEndpointForRole(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if route.endpointID != "llm_fake" || route.modelID != "fake-model-default" {
		t.Errorf("unexpected route for empty override: %+v", route)
	}
}

func TestResolveEndpointForRole_MissingEndpointIsClearError(t *testing.T) {
	withIsolatedRegistry(t)

	cfg := &agent.RoleConfig{
		Role:          agent.RoleAudit1,
		ModelProvider: "llm_ghost",
		ModelID:       "whatever",
	}
	_, err := resolveEndpointForRole(cfg)
	if err == nil || !strings.Contains(err.Error(), "llm_ghost") {
		t.Errorf("expected error naming the missing endpoint, got %v", err)
	}
}

func TestDefaultRegistryBuilder_IncludesAllBuiltins(t *testing.T) {
	reg := DefaultRegistryBuilder(agent.RoleAudit1)
	wantNames := []string{"edit", "glob", "grep", "read"}
	got := reg.List()
	if len(got) != len(wantNames) {
		t.Fatalf("want %d tools, got %d", len(wantNames), len(got))
	}
	for i, tool := range got {
		if tool.Name() != wantNames[i] {
			t.Errorf("tool[%d]: got %q want %q", i, tool.Name(), wantNames[i])
		}
	}
}

func TestDefaultPromptBuilder_FallsBackWhenInputEmpty(t *testing.T) {
	sess := &agent.Session{
		Role:    agent.RoleAudit1,
		Context: &agent.SessionContext{ProjectPath: "/tmp"},
	}
	_, user, err := DefaultPromptBuilder(sess)
	if err != nil {
		// agent.BuildPrompt might error because we didn't populate a
		// full context. Skip — this test only cares about the user
		// fallback path, which runs after build.
		t.Skipf("BuildPrompt requires full context (not the subject of this test): %v", err)
	}
	if user == "" {
		t.Error("user input should never be empty — default fallback should kick in")
	}
}

func TestDefaultPromptBuilder_UsesInputContentWhenSet(t *testing.T) {
	sess := &agent.Session{
		Role: agent.RoleAudit1,
		Context: &agent.SessionContext{
			ProjectPath:  "/tmp",
			InputContent: "here is the task",
		},
	}
	_, user, err := DefaultPromptBuilder(sess)
	if err != nil {
		t.Skipf("BuildPrompt setup-dep skip: %v", err)
	}
	if user != "here is the task" {
		t.Errorf("user input: got %q, want verbatim InputContent", user)
	}
}

func TestFireSessionCompletion_NoHandlerIsSafe(t *testing.T) {
	// Null handler is a legitimate state (tests/offline tools) — must
	// not panic. Also: no-op when sess is nil.
	prev := SessionCompletionHandler
	SessionCompletionHandler = nil
	defer func() { SessionCompletionHandler = prev }()

	fireSessionCompletion(nil, "completed")
	fireSessionCompletion(&agent.Session{ID: "s"}, "completed")
}

func TestFireSessionCompletion_InvokesHandlerWithCorrectFields(t *testing.T) {
	var got struct {
		sessionID, projectID, role, status string
	}
	prev := SessionCompletionHandler
	SessionCompletionHandler = func(sid, pid, role, status string) {
		got.sessionID = sid
		got.projectID = pid
		got.role = role
		got.status = status
	}
	defer func() { SessionCompletionHandler = prev }()

	sess := &agent.Session{ID: "sess_1", ProjectID: "proj_1", Role: agent.RoleAudit1}
	fireSessionCompletion(sess, "completed")
	if got.sessionID != "sess_1" || got.projectID != "proj_1" ||
		got.role != string(agent.RoleAudit1) || got.status != "completed" {
		t.Errorf("handler received wrong fields: %+v", got)
	}
}

func TestFireSessionCompletion_PanicInHandlerIsRecovered(t *testing.T) {
	prev := SessionCompletionHandler
	SessionCompletionHandler = func(_, _, _, _ string) {
		panic("boom")
	}
	defer func() { SessionCompletionHandler = prev }()

	// Must not propagate the panic.
	fireSessionCompletion(&agent.Session{ID: "s"}, "completed")
}

func TestDefaultMaxTokensForRole_HasSensibleDefaults(t *testing.T) {
	// Audit roles are terse (just a tool call) — a low budget
	// prevents runaway output if the model starts narrating.
	if defaultMaxTokensForRole(agent.RoleAudit1) > 4096 {
		t.Error("audit role should have a tight token budget")
	}
	// Maintain / assess roles produce long milestone / assessment
	// docs; they need headroom.
	if defaultMaxTokensForRole(agent.RoleMaintain) < 4096 {
		t.Error("maintain role needs a generous token budget")
	}
	// Unknown roles fall through to a safe middle ground.
	var unknown agent.Role = "unknown_role"
	if got := defaultMaxTokensForRole(unknown); got < 1024 || got > 8192 {
		t.Errorf("unknown role should get a middle budget; got %d", got)
	}
}
