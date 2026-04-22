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

// TestChiefRoleHasPlatformTools enforces Chief's governance-only scope.
//
// Chief is a low-intervention human proxy: it approves/rejects PRs,
// switches milestones, writes policies, and delegates task/milestone/
// direction edits to Maintain. It MUST NOT carry mutating tools that
// touch the work queue directly (create_task, delete_task,
// update_milestone, write_milestone, propose_direction) — those belong
// to Maintain and letting Chief call them would let a Chief session
// collide with an agent in flight on a claimed task.
func TestChiefRoleHasPlatformTools(t *testing.T) {
	cfg := GetRoleConfig(RoleChief)
	if cfg == nil {
		t.Fatal("Chief role config should exist")
	}

	// The only tools Chief is allowed to carry.
	expectedTools := []string{
		"approve_pr",
		"reject_pr",
		"switch_milestone",
		"create_policy",
		"delegate_to_maintain",
		"chief_output",
	}
	// Tools Chief must NEVER have — regression guard.
	forbiddenTools := []string{
		"create_task",
		"delete_task",
		"update_milestone",
		"write_milestone",
		"propose_direction",
	}

	toolMap := make(map[string]bool)
	for _, tool := range cfg.PlatformTools {
		toolMap[tool] = true
	}

	for _, expected := range expectedTools {
		if !toolMap[expected] {
			t.Errorf("Chief role missing required platform tool: %s", expected)
		}
	}
	for _, forbidden := range forbiddenTools {
		if toolMap[forbidden] {
			t.Errorf("Chief role must NOT carry %s — that would let Chief disrupt in-flight Maintain work. Use delegate_to_maintain instead.", forbidden)
		}
	}

	// Also assert Chief's declared tool list matches the registry's
	// RoleAccess. A tool that's listed on Chief but whose RoleAccess
	// doesn't include RoleChief is a dead reference — the LLM would
	// never see it at inference time.
	for _, tool := range cfg.PlatformTools {
		def, ok := PlatformTools[tool]
		if !ok {
			t.Errorf("Chief lists platform tool %q that is not defined in PlatformTools", tool)
			continue
		}
		granted := false
		for _, r := range def.RoleAccess {
			if r == RoleChief {
				granted = true
				break
			}
		}
		if !granted {
			t.Errorf("Chief lists platform tool %q but the tool's RoleAccess does not include RoleChief", tool)
		}
	}
}

// TestAllRolesDeclaredToolsAreGranted is a drift guard for every role, not
// just Chief. For every tool a role config lists under PlatformTools, the
// tool must (a) exist in the PlatformTools registry and (b) include that
// role in its RoleAccess. Otherwise the LLM at inference time will never
// be shown the tool and the role silently runs without it — the exact
// class of bug that hid in Chief's config for months.
func TestAllRolesDeclaredToolsAreGranted(t *testing.T) {
	for role, cfg := range RoleConfigs {
		for _, tool := range cfg.PlatformTools {
			def, ok := PlatformTools[tool]
			if !ok {
				t.Errorf("role %s lists tool %q that is not registered in PlatformTools", role, tool)
				continue
			}
			granted := false
			for _, r := range def.RoleAccess {
				if r == role {
					granted = true
					break
				}
			}
			if !granted {
				t.Errorf("role %s lists tool %q but its RoleAccess does not include %s (dead reference — LLM will never see this tool)", role, tool, role)
			}
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
