package agent

import "github.com/a3c/platform/internal/model"

type Role string

const (
	RoleAudit1   Role = "audit_1"
	RoleFix      Role = "fix"
	RoleAudit2   Role = "audit_2"
	RoleMaintain Role = "maintain"
	RoleConsult  Role = "consult"
	RoleAssess   Role = "assess"
	RoleEvaluate Role = "evaluate" // PR tech evaluation (diff + dry-run merge + code review)
	RoleMerge    Role = "merge"    // PR merge execution (git merge + conflict resolution)
)

type RoleConfig struct {
	Role           Role     `json:"role"`
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	PromptTemplate string   `json:"prompt_template"`
	PlatformTools  []string `json:"platform_tools"`
	OpenCodeTools  []string `json:"opencode_tools"`
	ModelProvider  string   `json:"model_provider"` // override: e.g. "openai", "anthropic", "minimax-coding-plan"
	ModelID        string   `json:"model_id"`       // override: e.g. "gpt-4o", "claude-sonnet-4-20250514", "MiniMax-M2.7"
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
	RoleEvaluate: {
		Role:           RoleEvaluate,
		Name:           "Evaluate Agent",
		Description:    "Evaluates PRs: diff analysis, dry-run merge conflict detection, code quality review",
		PromptTemplate: "evaluate.md",
		PlatformTools:  []string{"evaluate_output"},
		OpenCodeTools:  []string{"read", "glob"},
	},
	RoleMerge: {
		Role:           RoleMerge,
		Name:           "Merge Agent",
		Description:    "Executes PR merges: git merge, simple conflict resolution, complex conflicts abort",
		PromptTemplate: "merge.md",
		PlatformTools:  []string{"merge_output"},
		OpenCodeTools:  []string{"read", "edit", "glob"},
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

// GetRoleConfigWithOverride returns RoleConfig with DB overrides applied.
// If no override exists, returns the default config (copy).
func GetRoleConfigWithOverride(role Role) *RoleConfig {
	base := GetRoleConfig(role)
	if base == nil {
		return nil
	}
	// Copy to avoid mutating the global
	cfg := *base

	var override model.RoleOverride
	if model.DB.Where("role = ?", string(role)).First(&override).Error == nil {
		if override.ModelProvider != "" {
			cfg.ModelProvider = override.ModelProvider
		}
		if override.ModelID != "" {
			cfg.ModelID = override.ModelID
		}
	}
	return &cfg
}

// SetRoleOverride persists a model override for a role
func SetRoleOverride(role Role, modelProvider, modelID string) error {
	var override model.RoleOverride
	result := model.DB.Where("role = ?", string(role)).First(&override)
	if result.Error != nil {
		// Create new
		override = model.RoleOverride{
			ID:            model.GenerateID("rover"),
			Role:          string(role),
			ModelProvider: modelProvider,
			ModelID:       modelID,
		}
		return model.DB.Create(&override).Error
	}
	return model.DB.Model(&override).Updates(map[string]interface{}{
		"model_provider": modelProvider,
		"model_id":       modelID,
	}).Error
}

// GetAllRoleConfigs returns all role configs with overrides applied
func GetAllRoleConfigs() []*RoleConfig {
	configs := make([]*RoleConfig, 0, len(RoleConfigs))
	for r := range RoleConfigs {
		configs = append(configs, GetRoleConfigWithOverride(r))
	}
	return configs
}