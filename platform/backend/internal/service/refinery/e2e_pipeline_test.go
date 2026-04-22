package refinery

// Full end-to-end test: seed realistic sessions → run complete default
// pipeline → verify each pass contributed sensible artifacts → simulate
// feedback bumps → verify lifecycle promotion. This is the closest we
// get without a real MySQL server.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/a3c/platform/internal/model"
)

func TestIntegration_FullPipelineEndToEnd(t *testing.T) {
	defer setupTestDB(t)()

	projectID := "p1"

	// Seed realistic activity: 8 successful sessions with "grep read edit"
	// on Go files, 3 failed sessions with "edit edit" (one is L2).
	type seed struct {
		id         string
		toolSeq    string
		outcome    string
		auditLevel string
		taskTag    string
	}
	seeds := []seed{
		{"s1", "grep read edit change_submit", "success", "L0", "bugfix"},
		{"s2", "grep read edit change_submit", "success", "L0", "bugfix"},
		{"s3", "grep read edit change_submit", "success", "L0", "bugfix"},
		{"s4", "grep read edit change_submit", "success", "L0", "bugfix"},
		{"s5", "grep read edit change_submit", "success", "L0", "feature"},
		{"s6", "grep read edit change_submit", "success", "L0", "feature"},
		{"s7", "grep read edit change_submit", "success", "L0", "feature"},
		{"s8", "grep read edit change_submit", "success", "L0", "feature"},
		{"f1", "edit edit edit change_submit", "failure", "L2", "bugfix"},
		{"f2", "edit edit edit change_submit", "failure", "L2", "bugfix"},
		{"f3", "edit edit edit change_submit", "failure", "L1", "feature"},
	}

	for _, s := range seeds {
		chID := "c_" + s.id
		seedCompletedSession(t, projectID, s.id, s.toolSeq,
			s.outcome, s.auditLevel, []string{"main.go"}, chID)
		// Link a task so ToolRecipeMiner has tag data to bucket by.
		taskID := "t_" + s.id
		changeUpdate := map[string]interface{}{"task_id": &taskID}
		model.DB.Model(&model.Change{}).Where("id = ?", chID).Updates(changeUpdate)
		model.DB.Create(&model.TaskTag{
			ID: "tt_" + s.id, TaskID: taskID, Tag: s.taskTag, Source: "test",
		})
	}

	// Execute the full pipeline.
	r := New()
	run, err := r.Run(projectID, 24, "e2e-test")
	if err != nil {
		t.Logf("pipeline returned: err=%v status=%s stats=%s", err, run.Status, run.PassStats)
	}

	// EpisodeGrouper: should have produced 11 episodes.
	var epCount int64
	model.DB.Model(&model.Episode{}).Where("project_id = ?", projectID).Count(&epCount)
	if epCount != 11 {
		t.Errorf("expected 11 episodes, got %d", epCount)
	}

	// PatternExtractor: the 8-success n-gram "grep read edit" should be a
	// high-confidence pattern.
	var patterns []model.KnowledgeArtifact
	model.DB.Where("project_id = ? AND kind = ?", projectID, "pattern").Find(&patterns)
	if len(patterns) == 0 {
		t.Fatalf("expected at least one pattern artifact; got 0")
	}
	foundGrepRead := false
	for _, p := range patterns {
		if strings.Contains(p.Name, "grep→read→edit") {
			foundGrepRead = true
			if p.Confidence < 0.7 {
				t.Errorf("'grep read edit' pattern should have high confidence, got %.2f", p.Confidence)
			}
		}
	}
	if !foundGrepRead {
		t.Errorf("expected 'grep→read→edit' pattern; got %v", artifactNames(patterns))
	}

	// AntiPatternDetector: "edit edit" should be flagged.
	var antis []model.KnowledgeArtifact
	model.DB.Where("project_id = ? AND kind = ?", projectID, "anti_pattern").Find(&antis)
	if len(antis) == 0 {
		t.Errorf("expected at least one anti-pattern; got 0")
	}
	foundEditEdit := false
	for _, a := range antis {
		if strings.Contains(a.Name, "edit→edit") {
			foundEditEdit = true
			var payload map[string]any
			_ = json.Unmarshal([]byte(a.Payload), &payload)
			if payload["l2_count"] == nil || payload["l2_count"].(float64) < 2 {
				t.Errorf("expected l2_count>=2 on edit→edit anti-pattern, got %v", payload["l2_count"])
			}
		}
	}
	if !foundEditEdit {
		t.Errorf("expected 'edit→edit' anti-pattern; got %v", artifactNames(antis))
	}

	// ToolRecipeMiner: should have produced recipes bucketed by tag.
	var recipes []model.KnowledgeArtifact
	model.DB.Where("project_id = ? AND kind = ?", projectID, "tool_recipe").Find(&recipes)
	if len(recipes) == 0 {
		t.Errorf("expected at least one tool_recipe; got 0")
	}

	// MetaPass: one pass_report per project.
	var reports int64
	model.DB.Model(&model.KnowledgeArtifact{}).Where("project_id = ? AND kind = ?", projectID, "pass_report").Count(&reports)
	if reports != 1 {
		t.Errorf("expected exactly 1 pass_report, got %d", reports)
	}

	// Lifecycle should have been applied automatically. High-confidence
	// patterns with zero usage are promoted straight to active.
	var activePatterns int64
	model.DB.Model(&model.KnowledgeArtifact{}).
		Where("project_id = ? AND kind = ? AND status = ?", projectID, "pattern", "active").
		Count(&activePatterns)
	if activePatterns == 0 {
		t.Errorf("expected at least one pattern promoted to active by lifecycle")
	}

	// Now simulate feedback: pretend 5 future sessions all cited one
	// pattern and all succeeded. usage_count and success_count should
	// bump via direct DB update (the HandleSessionCompletion integration
	// test covers the wiring separately).
	var p model.KnowledgeArtifact
	if err := model.DB.Where("project_id = ? AND kind = ? AND name LIKE ?",
		projectID, "pattern", "%grep→read→edit%").First(&p).Error; err != nil {
		t.Fatalf("pattern missing: %v", err)
	}
	origUsage := p.UsageCount
	model.DB.Model(&model.KnowledgeArtifact{}).Where("id = ?", p.ID).
		Updates(map[string]interface{}{
			"usage_count":   origUsage + 15,
			"success_count": 14,
			"failure_count": 1,
		})
	// A re-run of lifecycle shouldn't deprecate a 14/15 = 93% success rate.
	_, _, _ = PromoteAndDeprecateArtifacts(projectID)
	model.DB.Where("id = ?", p.ID).First(&p)
	if p.Status == "deprecated" {
		t.Errorf("high-success-rate pattern should not be deprecated, got status=%q", p.Status)
	}

	// Flip the scenario: 10 failures out of 12 uses → should deprecate.
	model.DB.Model(&model.KnowledgeArtifact{}).Where("id = ?", p.ID).
		Updates(map[string]interface{}{
			"usage_count":   12,
			"success_count": 2,
			"failure_count": 10,
			"status":        "active",
		})
	_, _, _ = PromoteAndDeprecateArtifacts(projectID)
	model.DB.Where("id = ?", p.ID).First(&p)
	if p.Status != "deprecated" {
		t.Errorf("failing pattern should be deprecated, got status=%q", p.Status)
	}
}

// Smoke test: running the pipeline on an empty database should not
// panic and should produce a run row with status=ok (or partial).
func TestIntegration_PipelineOnEmptyDB(t *testing.T) {
	defer setupTestDB(t)()

	r := New()
	run, err := r.Run("p-empty", 24, "smoke")
	if err != nil {
		t.Fatalf("empty-db run failed: %v", err)
	}
	if run.Status == "" {
		t.Error("expected run status to be set")
	}

	// No episodes, no artifacts, but the run row itself must exist.
	var n int64
	model.DB.Model(&model.RefineryRun{}).Count(&n)
	if n != 1 {
		t.Errorf("expected 1 run row, got %d", n)
	}
	_ = time.Now // keep import
}
