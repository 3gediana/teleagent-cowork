package refinery

// End-to-end integration tests using an in-memory SQLite database.
//
// These tests exercise the REAL refinery passes against a REAL database
// with REAL tables. They catch bugs the unit tests can't: n-gram mining
// over actual Episodes, artifact upsert, lifecycle promotion, and the
// GlobalPromoter delta-merge logic.
//
// We use modernc.org/sqlite (pure Go, no CGO) so the test runs on any
// platform. A fresh in-memory DB is created per test via setupTestDB.

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/a3c/platform/internal/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// testDBCounter gives each test a unique in-memory database so state does
// not leak between tests (the "cache=shared" DSN would otherwise persist
// seeded rows across all tests in the same process).
var testDBCounter int64

// setupTestDB creates a fresh in-memory SQLite DB, migrates all schemas,
// installs it on the package-level model.DB, and returns a cleanup func
// that restores the previous model.DB. Each test should defer cleanup().
func setupTestDB(t *testing.T) func() {
	t.Helper()
	prev := model.DB
	n := atomic.AddInt64(&testDBCounter, 1)
	// Per-test unique in-memory file with cache=shared so goroutines
	// spawned in the test all see the same virtual database.
	dsn := fmt.Sprintf("file:testdb_%d?mode=memory&cache=shared", n)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&model.AgentSession{},
		&model.ToolCallTrace{},
		&model.TaskTag{},
		&model.Episode{},
		&model.KnowledgeArtifact{},
		&model.RefineryRun{},
		&model.Change{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	model.DB = db
	return func() {
		model.DB = prev
	}
}

// seedCompletedSession creates one AgentSession + linked ToolCallTraces in
// a terminal state, plus an optional Change row carrying the audit verdict.
func seedCompletedSession(t *testing.T, projectID, sessionID, toolSeq, outcome, auditLevel string, files []string, changeID string) {
	t.Helper()
	status := "completed"
	switch outcome {
	case "failure":
		status = "failed"
	case "partial":
		status = "pending_fix"
	}
	if err := model.DB.Create(&model.AgentSession{
		ID:        sessionID,
		Role:      "coder",
		ProjectID: projectID,
		ChangeID:  changeID,
		Status:    status,
		CreatedAt: time.Now().Add(-time.Hour),
	}).Error; err != nil {
		t.Fatalf("seed session: %v", err)
	}
	for i, tool := range strings.Fields(toolSeq) {
		args := map[string]any{}
		if len(files) > 0 {
			args["files"] = files
		}
		b, _ := json.Marshal(args)
		if err := model.DB.Create(&model.ToolCallTrace{
			ID:        sessionID + "_t" + string(rune('0'+i)),
			SessionID: sessionID,
			ProjectID: projectID,
			ToolName:  tool,
			Args:      string(b),
			Success:   outcome == "success",
			CreatedAt: time.Now().Add(-time.Hour).Add(time.Duration(i) * time.Second),
		}).Error; err != nil {
			t.Fatalf("seed trace: %v", err)
		}
	}
	if changeID != "" {
		var lvl *string
		if auditLevel != "" {
			v := auditLevel
			lvl = &v
		}
		if err := model.DB.Create(&model.Change{
			ID:         changeID,
			ProjectID:  projectID,
			AuditLevel: lvl,
		}).Error; err != nil {
			t.Fatalf("seed change: %v", err)
		}
	}
}

// --- Tests ----------------------------------------------------------------

func TestIntegration_EpisodeGrouperHonoursAuditLevel(t *testing.T) {
	defer setupTestDB(t)()

	// Session completed, but audit marked it L2 (auditor-rejected).
	// EpisodeGrouper should classify outcome="failure", not "success".
	seedCompletedSession(t, "p1", "s1", "grep read edit", "success", "L2",
		[]string{"main.go"}, "c1")

	stats, err := EpisodeGrouper{}.Run(&Context{ProjectID: "p1", Now: time.Now(), LookbackHours: 24})
	if err != nil {
		t.Fatalf("pass: %v", err)
	}
	if stats["episodes_created"] != 1 {
		t.Fatalf("expected 1 episode, got %v", stats["episodes_created"])
	}

	var ep model.Episode
	model.DB.Where("session_id = ?", "s1").First(&ep)
	if ep.Outcome != "failure" {
		t.Errorf("expected outcome=failure (L2 override), got %q", ep.Outcome)
	}
	if ep.AuditLevel != "L2" {
		t.Errorf("expected audit_level=L2, got %q", ep.AuditLevel)
	}
}

func TestIntegration_EpisodeGrouperIncrementalUpdate(t *testing.T) {
	defer setupTestDB(t)()

	// First run: change has no audit_level yet → outcome=success.
	seedCompletedSession(t, "p1", "s1", "grep read edit", "success", "",
		[]string{"main.go"}, "c1")

	if _, err := (EpisodeGrouper{}).Run(&Context{ProjectID: "p1", Now: time.Now(), LookbackHours: 24}); err != nil {
		t.Fatalf("first run: %v", err)
	}
	var ep model.Episode
	model.DB.Where("session_id = ?", "s1").First(&ep)
	if ep.Outcome != "success" {
		t.Fatalf("initial expected success, got %q", ep.Outcome)
	}

	// Auditor comes back and marks the change L2 → next run should flip
	// the existing Episode's outcome to failure.
	l2 := "L2"
	model.DB.Model(&model.Change{}).Where("id = ?", "c1").Update("audit_level", &l2)

	stats, _ := (EpisodeGrouper{}).Run(&Context{ProjectID: "p1", Now: time.Now(), LookbackHours: 24})
	if got := stats["episodes_updated"]; got != 1 {
		t.Errorf("expected 1 episode updated, got %v", got)
	}
	model.DB.Where("session_id = ?", "s1").First(&ep)
	if ep.Outcome != "failure" {
		t.Errorf("after L2 audit, expected outcome=failure, got %q", ep.Outcome)
	}
}

func TestIntegration_PatternExtractorDiscriminatesByFileCategory(t *testing.T) {
	defer setupTestDB(t)()

	// Same tool sequence on Go files (all succeed) vs. docs (mix).
	for i := 0; i < 4; i++ {
		seedCompletedSession(t, "p1", "go"+string(rune('0'+i)),
			"grep read edit", "success", "L0",
			[]string{"pkg/a.go"}, "cgo"+string(rune('0'+i)))
	}
	for i := 0; i < 4; i++ {
		seedCompletedSession(t, "p1", "md"+string(rune('0'+i)),
			"grep read edit", "success", "L0",
			[]string{"README.md"}, "cmd"+string(rune('0'+i)))
	}
	_, _ = (EpisodeGrouper{}).Run(&Context{ProjectID: "p1", Now: time.Now(), LookbackHours: 24})
	_, err := (PatternExtractor{}).Run(&Context{ProjectID: "p1", Now: time.Now(), LookbackHours: 24})
	if err != nil {
		t.Fatalf("pattern: %v", err)
	}

	// Expect BOTH a go-categorised pattern AND a docs-categorised one.
	var arts []model.KnowledgeArtifact
	model.DB.Where("project_id = ? AND kind = ?", "p1", "pattern").Find(&arts)
	foundGo, foundDocs := false, false
	for _, a := range arts {
		if strings.Contains(a.Name, "[go]") {
			foundGo = true
		}
		if strings.Contains(a.Name, "[docs]") {
			foundDocs = true
		}
	}
	if !foundGo || !foundDocs {
		t.Errorf("expected patterns split by category; got artifacts: %+v", artifactNames(arts))
	}
}

func TestIntegration_AntiPatternBoostsOnL2(t *testing.T) {
	defer setupTestDB(t)()

	// 10 episodes, 4 failures. "edit edit" appears in 3 failures, 2 with L2.
	for i := 0; i < 6; i++ {
		seedCompletedSession(t, "p1", "ok"+string(rune('0'+i)),
			"grep read edit change_submit", "success", "L0",
			[]string{"main.go"}, "cok"+string(rune('0'+i)))
	}
	for i := 0; i < 2; i++ {
		seedCompletedSession(t, "p1", "l2"+string(rune('0'+i)),
			"edit edit change_submit", "failure", "L2",
			[]string{"main.go"}, "cl2"+string(rune('0'+i)))
	}
	seedCompletedSession(t, "p1", "l1a",
		"edit edit change_submit", "failure", "",
		[]string{"main.go"}, "cl1a")
	seedCompletedSession(t, "p1", "other",
		"grep read read", "failure", "",
		[]string{"main.go"}, "cother")

	_, _ = (EpisodeGrouper{}).Run(&Context{ProjectID: "p1", Now: time.Now(), LookbackHours: 24})
	_, err := (AntiPatternDetector{}).Run(&Context{ProjectID: "p1", Now: time.Now(), LookbackHours: 24})
	if err != nil {
		t.Fatalf("anti-pattern: %v", err)
	}

	var anti model.KnowledgeArtifact
	if err := model.DB.Where("project_id = ? AND kind = ? AND name LIKE ?", "p1", "anti_pattern", "anti: edit%").First(&anti).Error; err != nil {
		t.Fatalf("expected anti-pattern for 'edit edit', got %v", err)
	}

	// Payload should carry l2_count and confidence should be >= raw fail_rate.
	var payload map[string]any
	_ = json.Unmarshal([]byte(anti.Payload), &payload)
	l2 := payload["l2_count"]
	if l2 == nil || int(l2.(float64)) < 2 {
		t.Errorf("expected l2_count>=2, got %v", l2)
	}
	failRate := payload["fail_rate"].(float64)
	if anti.Confidence < failRate {
		t.Errorf("confidence %.2f must be >= fail_rate %.2f (monotone)", anti.Confidence, failRate)
	}
}

func TestIntegration_LifecyclePromotesAndDeprecates(t *testing.T) {
	defer setupTestDB(t)()

	// High-confidence candidate with no usage → promote.
	promo := &model.KnowledgeArtifact{
		ID:         "k1",
		ProjectID:  "p1",
		Kind:       "pattern",
		Name:       "pat: x→y",
		Confidence: 0.9,
		Status:     "candidate",
		Version:    1,
	}
	model.DB.Create(promo)

	// Low-effectiveness active artifact → deprecate.
	dep := &model.KnowledgeArtifact{
		ID:           "k2",
		ProjectID:    "p1",
		Kind:         "pattern",
		Name:         "pat: a→b",
		Confidence:   0.5,
		Status:       "active",
		UsageCount:   12,
		SuccessCount: 2,
		FailureCount: 10,
		Version:      1,
	}
	model.DB.Create(dep)

	promoted, deprecated, err := PromoteAndDeprecateArtifacts("p1")
	if err != nil {
		t.Fatalf("lifecycle: %v", err)
	}
	if promoted != 1 {
		t.Errorf("expected 1 promotion, got %d", promoted)
	}
	if deprecated != 1 {
		t.Errorf("expected 1 deprecation, got %d", deprecated)
	}

	var after1 model.KnowledgeArtifact
	model.DB.Where("id = ?", "k1").First(&after1)
	if after1.Status != "active" {
		t.Errorf("k1 should be active, got %q", after1.Status)
	}
	var after2 model.KnowledgeArtifact
	model.DB.Where("id = ?", "k2").First(&after2)
	if after2.Status != "deprecated" {
		t.Errorf("k2 should be deprecated, got %q", after2.Status)
	}
}

func TestIntegration_GlobalPromoterDoesNotDoubleCount(t *testing.T) {
	defer setupTestDB(t)()

	// Project-scoped artifact that qualifies for global promotion.
	src := &model.KnowledgeArtifact{
		ID:           "src1",
		ProjectID:    "p1",
		Kind:         "pattern",
		Name:         "pat: grep→read",
		Confidence:   0.9,
		Status:       "active",
		UsageCount:   20,
		SuccessCount: 18,
		FailureCount: 2,
		Version:      1,
	}
	model.DB.Create(src)

	// First global run: creates global twin with counts 20/18/2.
	_, err := (GlobalPromoter{}).Run(&Context{ProjectID: "", Now: time.Now(), LookbackHours: 24 * 30})
	if err != nil {
		t.Fatalf("first global run: %v", err)
	}
	var g1 model.KnowledgeArtifact
	model.DB.Where("project_id = '' AND kind = ? AND name = ?", "pattern", "pat: grep→read").First(&g1)
	if g1.UsageCount != 20 || g1.SuccessCount != 18 {
		t.Fatalf("first run expected 20/18, got %d/%d", g1.UsageCount, g1.SuccessCount)
	}

	// Second run with UNCHANGED source counts → global must NOT change.
	_, _ = (GlobalPromoter{}).Run(&Context{ProjectID: "", Now: time.Now(), LookbackHours: 24 * 30})
	model.DB.Where("project_id = '' AND kind = ? AND name = ?", "pattern", "pat: grep→read").First(&g1)
	if g1.UsageCount != 20 || g1.SuccessCount != 18 {
		t.Errorf("after idempotent re-run expected 20/18 (no double-count), got %d/%d",
			g1.UsageCount, g1.SuccessCount)
	}

	// Source grows by 5/4/1 → global should add only the delta.
	model.DB.Model(&model.KnowledgeArtifact{}).Where("id = ?", "src1").Updates(map[string]interface{}{
		"usage_count":   25,
		"success_count": 22,
		"failure_count": 3,
	})
	_, _ = (GlobalPromoter{}).Run(&Context{ProjectID: "", Now: time.Now(), LookbackHours: 24 * 30})
	model.DB.Where("project_id = '' AND kind = ? AND name = ?", "pattern", "pat: grep→read").First(&g1)
	if g1.UsageCount != 25 || g1.SuccessCount != 22 || g1.FailureCount != 3 {
		t.Errorf("after delta merge expected 25/22/3, got %d/%d/%d",
			g1.UsageCount, g1.SuccessCount, g1.FailureCount)
	}
}

func TestIntegration_MetaPassWritesReport(t *testing.T) {
	defer setupTestDB(t)()

	// Seed a couple of artifacts from different producers.
	model.DB.Create(&model.KnowledgeArtifact{
		ID: "a1", ProjectID: "p1", Kind: "pattern", Name: "pat: x→y",
		ProducedBy: "pattern_extractor/v1", Confidence: 0.8, Status: "active",
		UsageCount: 10, SuccessCount: 9, Version: 1,
	})
	model.DB.Create(&model.KnowledgeArtifact{
		ID: "a2", ProjectID: "p1", Kind: "anti_pattern", Name: "anti: p→q",
		ProducedBy: "antipattern_detector/v1", Confidence: 0.9, Status: "active",
		UsageCount: 5, FailureCount: 4, Version: 1,
	})

	_, err := (MetaPass{}).Run(&Context{ProjectID: "p1", Now: time.Now(), LookbackHours: 24 * 365})
	if err != nil {
		t.Fatalf("meta pass: %v", err)
	}

	var report model.KnowledgeArtifact
	if err := model.DB.Where("project_id = ? AND kind = ?", "p1", "pass_report").First(&report).Error; err != nil {
		t.Fatalf("expected pass_report to be created: %v", err)
	}
	if !strings.Contains(report.Summary, "pattern_extractor") {
		t.Errorf("expected summary to mention pattern_extractor producer; got %q", report.Summary)
	}
}

// Helper: extract artifact names for diagnostic output.
func artifactNames(arts []model.KnowledgeArtifact) []string {
	names := make([]string, len(arts))
	for i, a := range arts {
		names[i] = a.Name
	}
	return names
}
