package agent

type ToolDefinition struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Parameters  []ToolParam `json:"parameters"`
	RoleAccess  []Role   `json:"role_access"`
}

type ToolParam struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
}

var PlatformTools = map[string]*ToolDefinition{
	"audit_output": {
		Name:        "audit_output",
		Description: "Output audit result for a code change submission",
		Parameters: []ToolParam{
			{Name: "level", Type: "string", Description: "Audit level: L0 (merge), L1 (fixable), L2 (reject)", Required: true},
			{Name: "issues", Type: "array", Description: "List of issues found (for L1/L2)", Required: false},
			{Name: "reject_reason", Type: "string", Description: "Reason for L2 rejection", Required: false},
		},
		RoleAccess: []Role{RoleAudit1},
	},
	"fix_output": {
		Name:        "fix_output",
		Description: "Output fix verification result",
		Parameters: []ToolParam{
			{Name: "action", Type: "string", Description: "Action: fix, delegate, or reject", Required: true},
			{Name: "fixed", Type: "boolean", Description: "Whether issues were fixed (for action=fix)", Required: false},
			{Name: "delegate_to", Type: "string", Description: "Delegate target: audit_agent_2", Required: false},
			{Name: "reject_reason", Type: "string", Description: "Reason for rejection", Required: false},
		},
		RoleAccess: []Role{RoleFix},
	},
	"audit2_output": {
		Name:        "audit2_output",
		Description: "Output final audit decision after re-review",
		Parameters: []ToolParam{
			{Name: "result", Type: "string", Description: "Final result: merge or reject", Required: true},
			{Name: "reject_reason", Type: "string", Description: "Reason for rejection", Required: false},
		},
		RoleAccess: []Role{RoleAudit2},
	},
	"create_task": {
		Name:        "create_task",
		Description: "Create a new task in the current milestone",
		Parameters: []ToolParam{
			{Name: "name", Type: "string", Description: "Task name", Required: true},
			{Name: "description", Type: "string", Description: "Task description", Required: false},
			{Name: "priority", Type: "string", Description: "Priority: high, medium, or low", Required: false},
		},
		RoleAccess: []Role{RoleMaintain},
	},
	"delete_task": {
		Name:        "delete_task",
		Description: "Delete a task (only for maintain agent)",
		Parameters: []ToolParam{
			{Name: "task_id", Type: "string", Description: "Task ID to delete", Required: true},
		},
		RoleAccess: []Role{RoleMaintain},
	},
	"update_milestone": {
		Name:        "update_milestone",
		Description: "Update the milestone block content",
		Parameters: []ToolParam{
			{Name: "content", Type: "string", Description: "New milestone block content", Required: true},
		},
		RoleAccess: []Role{RoleMaintain},
	},
	"propose_direction": {
		Name:        "propose_direction",
		Description: "Write direction block content (must be confirmed by human in conversation)",
		Parameters: []ToolParam{
			{Name: "content", Type: "string", Description: "Direction block content", Required: true},
		},
		RoleAccess: []Role{RoleMaintain},
	},
	"write_milestone": {
		Name:        "write_milestone",
		Description: "Write milestone block content following the template format",
		Parameters: []ToolParam{
			{Name: "content", Type: "string", Description: "Milestone block content in template format", Required: true},
		},
		RoleAccess: []Role{RoleMaintain},
	},
	"assess_output": {
		Name:        "assess_output",
		Description: "Output project structure assessment result",
		Parameters: []ToolParam{
			{Name: "content", Type: "string", Description: "ASSESS_DOC.md content in template format", Required: true},
		},
		RoleAccess: []Role{RoleAssess},
	},
}

func GetToolsForRole(role Role) []*ToolDefinition {
	tools := make([]*ToolDefinition, 0)
	for _, tool := range PlatformTools {
		for _, r := range tool.RoleAccess {
			if r == role {
				tools = append(tools, tool)
				break
			}
		}
	}
	return tools
}