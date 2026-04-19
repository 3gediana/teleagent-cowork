package agent

type Role string

const (
	RoleAudit1   Role = "audit_1"
	RoleFix      Role = "fix"
	RoleAudit2   Role = "audit_2"
	RoleMaintain Role = "maintain"
	RoleConsult  Role = "consult"
	RoleAssess   Role = "assess"
)

type RoleConfig struct {
	Role           Role     `json:"role"`
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	PromptTemplate string   `json:"prompt_template"`
	PlatformTools  []string `json:"platform_tools"`
	OpenCodeTools  []string `json:"opencode_tools"`
}

var RoleConfigs = map[Role]*RoleConfig{
	RoleAudit1: {
		Role:           RoleAudit1,
		Name:           "Audit Agent 1",
		Description:    "Reviews code submissions, judges conflict level (L0/L1/L2)",
		PromptTemplate: "audit_1.md",
		PlatformTools:  []string{"audit_output"},
		OpenCodeTools:  []string{"read", "glob"},
	},
	RoleFix: {
		Role:           RoleFix,
		Name:           "Fix Agent",
		Description:    "Verifies L1 issues flagged by Audit Agent 1, fixes or delegates",
		PromptTemplate: "fix.md",
		PlatformTools:  []string{"fix_output"},
		OpenCodeTools:  []string{"read", "edit", "glob"},
	},
	RoleAudit2: {
		Role:           RoleAudit2,
		Name:           "Audit Agent 2",
		Description:    "Re-audits when Fix Agent delegates (suspected false positive)",
		PromptTemplate: "audit_2.md",
		PlatformTools:  []string{"audit2_output"},
		OpenCodeTools:  []string{"read", "glob"},
	},
	RoleMaintain: {
		Role:           RoleMaintain,
		Name:           "Maintain Agent",
		Description:    "Manages project execution path, creates tasks, updates milestones",
		PromptTemplate: "maintain.md",
		PlatformTools:  []string{"create_task", "delete_task", "update_milestone", "propose_direction", "write_milestone"},
		OpenCodeTools:  []string{"read", "edit", "glob"},
	},
	RoleConsult: {
		Role:           RoleConsult,
		Name:           "Consult Agent",
		Description:    "Answers project status questions, read-only access",
		PromptTemplate: "consult.md",
		PlatformTools:  []string{},
		OpenCodeTools:  []string{"read", "glob"},
	},
	RoleAssess: {
		Role:           RoleAssess,
		Name:           "Assess Agent",
		Description:    "Analyzes imported project structure, outputs ASSESS_DOC.md",
		PromptTemplate: "assess.md",
		PlatformTools:  []string{"assess_output"},
		OpenCodeTools:  []string{"read", "glob"},
	},
}

func GetRoleConfig(role Role) *RoleConfig {
	if cfg, ok := RoleConfigs[role]; ok {
		return cfg
	}
	return nil
}

func GetAvailableRoles() []Role {
	roles := make([]Role, 0, len(RoleConfigs))
	for r := range RoleConfigs {
		roles = append(roles, r)
	}
	return roles
}