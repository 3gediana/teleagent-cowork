package agent

import "testing"

func TestGetRoleConfig(t *testing.T) {
	tests := []struct {
		role     Role
		exists   bool
		name     string
	}{
		{RoleChief, true, "Chief Agent"},
		{RoleEvaluate, true, "Evaluate Agent"},
		{RoleMerge, true, "Merge Agent"},
		{RoleAudit1, true, "Audit Agent 1"},
		{RoleFix, true, "Fix Agent"},
		{RoleMaintain, true, "Maintain Agent"},
		{RoleConsult, true, "Consult Agent"},
		{RoleAssess, true, "Assess Agent"},
		{RoleAudit2, true, "Audit Agent 2"},
		{Role("nonexistent"), false, ""},
	}

	for _, tt := range tests {
		t.Run(string(tt.role), func(t *testing.T) {
			cfg := GetRoleConfig(tt.role)
			if tt.exists {
				if cfg == nil {
					t.Fatalf("expected config for role %s", tt.role)
				}
				if cfg.Name != tt.name {
					t.Errorf("expected name=%s, got=%s", tt.name, cfg.Name)
				}
			} else {
				if cfg != nil {
					t.Errorf("expected nil for nonexistent role, got %v", cfg)
				}
			}
		})
	}
}

func TestChiefRoleHasPlatformTools(t *testing.T) {
	cfg := GetRoleConfig(RoleChief)
	if cfg == nil {
		t.Fatal("Chief role config should exist")
	}

	expectedTools := []string{"create_task", "delete_task", "update_milestone", "propose_direction", "write_milestone", "approve_pr", "reject_pr", "switch_milestone", "create_policy", "chief_output"}
	toolMap := make(map[string]bool)
	for _, tool := range cfg.PlatformTools {
		toolMap[tool] = true
	}

	for _, expected := range expectedTools {
		if !toolMap[expected] {
			t.Errorf("Chief role missing platform tool: %s", expected)
		}
	}
}

func TestGetRoleForTrigger(t *testing.T) {
	tests := []struct {
		trigger string
		role    Role
	}{
		{"chief_request", RoleChief},
		{"chief_chat", RoleChief},
		{"chief_decision_pr_review", RoleChief},
		{"chief_decision_pr_merge", RoleChief},
		{"chief_decision_milestone_switch", RoleChief},
		{"pr_evaluate", RoleEvaluate},
		{"pr_merge", RoleMerge},
		{"pr_biz_review", RoleMaintain},
		{"change_submitted", RoleAudit1},
		{"unknown_trigger", RoleMaintain}, // default
	}

	for _, tt := range tests {
		t.Run(tt.trigger, func(t *testing.T) {
			got := GetRoleForTrigger(tt.trigger)
			if got != tt.role {
				t.Errorf("GetRoleForTrigger(%q) = %q, want %q", tt.trigger, got, tt.role)
			}
		})
	}
}
