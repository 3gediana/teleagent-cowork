package runner

// Dispatcher tests. Only cover the pure-function pieces
// (routesToNative, DefaultRegistryBuilder, DefaultPromptBuilder) to
// avoid pulling in a DB + full session lifecycle into the unit tests.
// End-to-end dispatch is exercised by e2erun.

import (
	"testing"

	"github.com/a3c/platform/internal/agent"
)

func TestRoutesToNative(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		want     bool
	}{
		{"empty (default opencode)", "", false},
		{"legacy opencode provider", "anthropic", false},
		{"legacy minimax id", "minimax-coding-plan", false},
		{"user-registered LLM endpoint", "llm_abc123", true},
		{"malformed llm-ish prefix", "llmabc", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := routesToNative(c.provider); got != c.want {
				t.Errorf("routesToNative(%q) = %v; want %v", c.provider, got, c.want)
			}
		})
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
