package model

import (
	"encoding/json"
	"testing"
)

func TestPolicyModel(t *testing.T) {
	p := Policy{
		ID:             GenerateID("pol"),
		Name:           "Large PR requires human",
		MatchCondition: `{"scope":"pr_review","file_count_gt":5}`,
		Actions:        `{"require_human":true,"warn":"大改动需人类确认"}`,
		Priority:       10,
		Status:         "active",
		Source:         "human",
	}

	if p.ID == "" {
		t.Fatal("Policy ID should not be empty")
	}
	if p.Status != "active" {
		t.Fatalf("expected active, got %s", p.Status)
	}

	// Validate JSON fields
	var mc map[string]interface{}
	if err := json.Unmarshal([]byte(p.MatchCondition), &mc); err != nil {
		t.Fatalf("MatchCondition should be valid JSON: %v", err)
	}
	if mc["scope"] != "pr_review" {
		t.Fatalf("expected scope=pr_review, got %v", mc["scope"])
	}

	var act map[string]interface{}
	if err := json.Unmarshal([]byte(p.Actions), &act); err != nil {
		t.Fatalf("Actions should be valid JSON: %v", err)
	}
	if requireHuman, _ := act["require_human"].(bool); !requireHuman {
		t.Fatal("expected require_human=true")
	}
}

func TestAgentSessionModel(t *testing.T) {
	s := AgentSession{
		ID:            GenerateID("session"),
		Role:          "chief",
		ProjectID:     "proj_test",
		Status:        "pending",
		RetryCount:    0,
		DurationMs:    0,
	}

	if s.TableName() != "agent_session" {
		t.Fatalf("expected table agent_session, got %s", s.TableName())
	}
	if s.Role != "chief" {
		t.Fatalf("expected role=chief, got %s", s.Role)
	}
}

func TestToolCallTraceModel(t *testing.T) {
	tc := ToolCallTrace{
		ID:        GenerateID("tct"),
		SessionID: "session_test",
		ToolName:  "approve_pr",
		Success:   true,
	}

	if tc.TableName() != "tool_call_trace" {
		t.Fatalf("expected table tool_call_trace, got %s", tc.TableName())
	}
	if tc.ToolName != "approve_pr" {
		t.Fatalf("expected approve_pr, got %s", tc.ToolName)
	}
}

func TestTaskTagModel(t *testing.T) {
	tt := TaskTag{
		ID:     GenerateID("ttag"),
		TaskID: "task_test",
		Tag:    "frontend",
		Source: "human",
	}

	if tt.TableName() != "task_tag" {
		t.Fatalf("expected table task_tag, got %s", tt.TableName())
	}
	if tt.Source != "human" {
		t.Fatalf("expected source=human, got %s", tt.Source)
	}
}

func TestChangeModelFailureMode(t *testing.T) {
	c := Change{
		ID:          GenerateID("chg"),
		ProjectID:   "proj_test",
		FailureMode: "wrong_assumption",
		RetryCount:  2,
	}

	if c.FailureMode != "wrong_assumption" {
		t.Fatalf("expected wrong_assumption, got %s", c.FailureMode)
	}
	if c.RetryCount != 2 {
		t.Fatalf("expected retry_count=2, got %d", c.RetryCount)
	}
}

func TestGenerateID(t *testing.T) {
	id1 := GenerateID("pol")
	id2 := GenerateID("pol")

	if id1 == id2 {
		t.Fatal("GenerateID should produce unique IDs")
	}
	if len(id1) < 5 {
		t.Fatal("GenerateID should produce IDs with reasonable length")
	}
}
