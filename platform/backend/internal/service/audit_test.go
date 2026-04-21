package service

import "testing"

func TestClassifyFailureMode(t *testing.T) {
	tests := []struct {
		name     string
		issues   []AuditIssue
		expected string
	}{
		{
			name:     "no issues",
			issues:   []AuditIssue{},
			expected: "",
		},
		{
			name: "wrong_assumption",
			issues: []AuditIssue{
				{Type: "wrong_assumption", File: "main.go", Detail: "assumed X but Y"},
			},
			expected: "wrong_assumption",
		},
		{
			name: "missing_context",
			issues: []AuditIssue{
				{Type: "missing_context", File: "util.go", Detail: "lacked context"},
			},
			expected: "missing_context",
		},
		{
			name: "tool_misuse",
			issues: []AuditIssue{
				{Type: "tool_misuse", File: "handler.go", Detail: "used wrong tool"},
			},
			expected: "tool_misuse",
		},
		{
			name: "over_edit",
			issues: []AuditIssue{
				{Type: "over_edit", File: "model.go", Detail: "edited too much"},
			},
			expected: "over_edit",
		},
		{
			name: "invalid_output",
			issues: []AuditIssue{
				{Type: "invalid_output", File: "api.go", Detail: "output invalid"},
			},
			expected: "invalid_output",
		},
		{
			name: "unknown type returns empty",
			issues: []AuditIssue{
				{Type: "style_issue", File: "ui.tsx", Detail: "bad style"},
			},
			expected: "",
		},
		{
			name: "first match wins",
			issues: []AuditIssue{
				{Type: "style_issue", File: "ui.tsx", Detail: "bad style"},
				{Type: "wrong_assumption", File: "main.go", Detail: "assumed X"},
				{Type: "missing_context", File: "util.go", Detail: "lacked context"},
			},
			expected: "wrong_assumption",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyFailureMode(tt.issues)
			if got != tt.expected {
				t.Errorf("classifyFailureMode() = %q, want %q", got, tt.expected)
			}
		})
	}
}
