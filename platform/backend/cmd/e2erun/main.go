// Command e2erun — hands-on end-to-end walkthrough of the self-evolution loop.
//
// Drives the library layer (no HTTP server, no MySQL, no opencode LLM) to
// show every stage of the pipeline with banner-separated output. Every
// step prints the business action it performed and the resulting DB
// state so you can judge correctness by eye.
//
// Stages
// ------
//   0. Bootstrap          — SQLite in-memory + bge sidecar wiring
//   1. Agents & project   — 1 human PM + 2 MCP client agents
//   2. Historical seed    — 10 past sessions across auth / i18n / schema
//   3. Refinery pass #1   — distill history into KnowledgeArtifacts,
//                           each with a real 768-dim bge embedding
//   4. New task arrives   — 陌生任务"修复用户登录 401", embedded on the fly
//   5. MCP client.claim   — BuildTaskClaimHints returns recipes/patterns/
//                           anti-patterns bundled for the coder agent
//   6. Chief injection    — SelectArtifactsForInjection with commander
//                           budget, formatted like the chief prompt
//   7. Feedback loop      — HandleSessionCompletion bumps counters on
//                           the exact artifacts a chief session used
//   8. Analyze injection  — widest budget, mimics the analyze prompt
//   9. Refinery pass #2   — lifecycle react to the new feedback
//  10. Final health       — per-kind artifact tallies
//
// What this does NOT do
// ---------------------
//   * real opencode serve / LLM calls
//   * real MySQL (we use SQLite for reproducibility + speed)
//   * real SSE / Gin routes (we call service-layer functions directly)
//
// Prereqs: platform/embedder/app.py running on :3011 (set A3C_EMBEDDER_URL
// to override).
//
// Run:   cd platform/backend && go run ./cmd/e2erun

package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/a3c/platform/internal/model"
	"github.com/a3c/platform/internal/service"
	"github.com/a3c/platform/internal/service/refinery"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func init() { log.SetFlags(0) }

// --- Stage orchestration -------------------------------------------------

func main() {
	banner(0, "Bootstrap — SQLite in-memory + embedder sidecar")
	db := mustOpenDB()
	model.DB = db
	service.InstallEmbedderIntoRefinery(nil)

	h, err := service.DefaultEmbeddingClient().Health(ctxT(5 * time.Second))
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nembedder unreachable: %v\n"+
			"  start it first: cd platform/embedder && python app.py\n", err)
		os.Exit(1)
	}
	fmt.Printf("  embedder ok — model=%s dim=%d device=%s\n", h.Model, h.Dim, h.Device)

	banner(1, "Seed agents — 1 human PM + 2 MCP client agents")
	projectID := "proj_demo"
	humanID := "agent_human"
	clientA := "agent_alice"
	clientB := "agent_bob"
	seedProject(db, projectID, humanID, clientA, clientB)
	dumpAgents(db, projectID)

	banner(2, "Seed history — 10 past sessions (auth / frontend / schema)")
	seedHistoricalActivity(db, projectID)
	dumpHistoricalStats(db, projectID)

	banner(3, "Refinery pass #1 — distill history into KnowledgeArtifacts")
	r := refinery.New()
	if _, err := r.Run(projectID, 24*30, "e2e-run-1"); err != nil {
		log.Fatalf("refinery: %v", err)
	}
	dumpArtifacts(db, projectID)
	dumpArtifactSummaries(projectID)

	banner(4, "New task arrives — 陌生任务:\"修复用户登录 401 认证失败\"")
	taskID := createAndEmbedTask(db, projectID, humanID,
		"修复用户登录 401 认证失败",
		"生产环境上用户反映登录服务端返回 401,影响大,需要尽快定位")
	dumpTaskEmbedding(db, taskID)

	banner(5, "MCP client.claim — agent alice picks up the task")
	showClaimHints(projectID, taskID, clientA)

	banner(6, "Chief Agent — what gets injected into its prompt")
	showChiefInjection(db, projectID)

	banner(7, "Session outcome SUCCESS — feedback hook fires")
	demonstrateFeedbackLoop(db, projectID)

	banner(8, "Analyze Agent — widest budget, relevance-ranked")
	showAnalyzeInjection(db, projectID)

	banner(9, "Refinery pass #2 — lifecycle reacts to new feedback")
	before := snapshotArtifacts(db, projectID)
	if _, err := r.Run(projectID, 24*30, "e2e-run-2"); err != nil {
		log.Fatalf("refinery #2: %v", err)
	}
	after := snapshotArtifacts(db, projectID)
	diffArtifacts(before, after)

	banner(10, "Final artifact health")
	dumpFinalHealth(db, projectID)

	banner(11, "Client self-evolution loop — claim → submit → audit → bump")
	demonstrateClientFeedbackLoop(db, projectID, clientA)

	banner(12, "Tag lifecycle — rules propose, human confirms, selector notices")
	demonstrateTagLifecycle(db, projectID, humanID)

	fmt.Println("\n✔ done.  re-run with a fresh DB to see deterministic results.")
}

// --- Stage helpers -------------------------------------------------------

func seedProject(db *gorm.DB, projectID, humanID, clientA, clientB string) {
	if err := db.Create(&model.Project{ID: projectID, Name: "Demo App", Status: "ready"}).Error; err != nil {
		log.Fatalf("seed project: %v", err)
	}
	for _, a := range []model.Agent{
		{ID: humanID, Name: "alice_pm", Status: "online",
			CurrentProjectID: ptrStr(projectID), AccessKey: "hk_demo", IsHuman: true},
		{ID: clientA, Name: "alice_coder", Status: "online",
			CurrentProjectID: ptrStr(projectID), AccessKey: "ck_alice"},
		{ID: clientB, Name: "bob_coder", Status: "online",
			CurrentProjectID: ptrStr(projectID), AccessKey: "ck_bob"},
	} {
		a := a
		if err := db.Create(&a).Error; err != nil {
			log.Fatalf("seed agent %s: %v", a.ID, err)
		}
	}
}

func dumpAgents(db *gorm.DB, projectID string) {
	var agents []model.Agent
	db.Where("current_project_id = ?", projectID).Find(&agents)
	for _, a := range agents {
		role := "client"
		if a.IsHuman {
			role = "human"
		}
		fmt.Printf("  - %-14s [%s/%s] id=%s\n", a.Name, role, a.Status, a.ID)
	}
}

// seedHistoricalActivity lays down 12 completed sessions + tool traces +
// changes + tasks across three topics. Each session links to a Task via
// Change.TaskID so Episode.TaskID gets populated — which lets the topic
// context extractor pull in the (Chinese) task titles.
func seedHistoricalActivity(db *gorm.DB, projectID string) {
	type act struct {
		id, toolSeq, outcome, audit string
		files                       []string
		taskName                    string // Chinese human-written title
	}
	activities := []act{
		// AUTH: canonical grep→read→edit (support≥3, auth-exclusive files).
		// s05 is the "edit edit edit" failure that anchors the anti-pattern.
		{"s01", "grep read edit change_submit", "success", "L0", []string{"internal/middleware/auth.go"}, "修复 JWT 中间件校验缺失"},
		{"s02", "grep read edit change_submit", "success", "L0", []string{"internal/middleware/auth.go"}, "鉴权中间件支持刷新令牌"},
		{"s03", "grep read edit change_submit", "success", "L0", []string{"internal/handler/auth.go"}, "登录接口加入速率限制"},
		{"s04", "grep read edit change_submit", "success", "L0", []string{"internal/handler/auth.go"}, "修复注销接口会话未清理"},
		{"s05", "edit edit edit change_submit", "failure", "L2", []string{"internal/middleware/auth.go"}, "紧急修复 401 误判"},

		// FRONTEND i18n: distinct sequence "read glob edit change_submit"
		// (support=3, frontend-exclusive files). Creates a recipe with
		// different topic keywords (i18n, components) than auth.
		{"s06", "read glob edit change_submit", "success", "L0", []string{"frontend/src/i18n/zh.json"}, "补充中文翻译缺失键"},
		{"s07", "read glob edit change_submit", "success", "L0", []string{"frontend/src/i18n/en.json"}, "对齐英文翻译文案"},
		{"s08", "read glob edit change_submit", "success", "L0", []string{"frontend/src/components/LangSwitch.tsx"}, "添加语言切换按钮"},

		// SCHEMA MIGRATION: yet another distinct sequence with its own
		// file vocabulary ("model", "migration"). Three successes so
		// it clears patternMinSupport=3 independently. Plus one failure
		// on the same files so the anti-pattern picks up schema context.
		{"s09", "grep migrate backfill change_submit", "success", "L0", []string{"internal/model/user.go", "internal/migration/20260415_add_last_login.sql"}, "用户表增加 last_login 字段"},
		{"s10", "grep migrate backfill change_submit", "success", "L0", []string{"internal/model/order.go", "internal/migration/20260416_order_refund.sql"}, "订单表支持退款状态"},
		{"s11", "grep migrate backfill change_submit", "success", "L0", []string{"internal/model/invoice.go", "internal/migration/20260417_invoice_status.sql"}, "发票表迁移加状态列"},
		{"s12", "edit edit change_submit", "failure", "L2", []string{"internal/model/user.go"}, "紧急回退用户表字段"},
	}

	for _, a := range activities {
		taskID := "task_" + a.id
		changeID := "chg_" + a.id
		status := "completed"
		if a.outcome == "failure" {
			status = "failed"
		}

		// Task row — carries the Chinese human-written title that the
		// topic extractor will pull into artifact summaries.
		if err := db.Create(&model.Task{
			ID: taskID, ProjectID: projectID, Name: a.taskName,
			Priority: "medium", Status: "completed", CreatedBy: "agent_human",
			CreatedAt: time.Now().Add(-24 * time.Hour),
		}).Error; err != nil {
			log.Fatalf("seed task: %v", err)
		}
		// Simulate a realistic history where tasks have been reviewed:
		// run the rule engine and promote its proposals to "confirmed"
		// so ToolRecipeMiner can bucket by concrete tags (bugfix /
		// feature / ...) instead of the _untagged catch-all. This also
		// lets Stage 12's selector rerun actually move its tag_score.
		service.ProposeAndPersistTagsForTask(taskID, a.taskName, "")
		var proposedTags []model.TaskTag
		db.Where("task_id = ? AND status = ?", taskID, "proposed").Find(&proposedTags)
		for _, tg := range proposedTags {
			_ = service.ConfirmTag(tg.ID, "agent_human", "auto-confirm for demo seed")
		}

		if err := db.Create(&model.AgentSession{
			ID: a.id, Role: "coder", ProjectID: projectID,
			ChangeID: changeID, Status: status,
			CreatedAt: time.Now().Add(-24 * time.Hour),
		}).Error; err != nil {
			log.Fatalf("seed session: %v", err)
		}
		for i, tool := range strings.Fields(a.toolSeq) {
			filesJSON := "{}"
			if len(a.files) > 0 {
				filesJSON = fmt.Sprintf(`{"files":["%s"]}`, strings.Join(a.files, `","`))
			}
			if err := db.Create(&model.ToolCallTrace{
				ID: fmt.Sprintf("%s_t%d", a.id, i), SessionID: a.id, ProjectID: projectID,
				ToolName: tool, Args: filesJSON, Success: a.outcome == "success",
				CreatedAt: time.Now().Add(-24 * time.Hour).Add(time.Duration(i) * time.Second),
			}).Error; err != nil {
				log.Fatalf("seed trace: %v", err)
			}
		}
		var lvl *string
		if a.audit != "" {
			v := a.audit
			lvl = &v
		}
		// Change links to Task — EpisodeGrouper propagates that to
		// Episode.TaskID, and topic_context.collectTaskNames pulls it.
		if err := db.Create(&model.Change{
			ID: changeID, ProjectID: projectID,
			TaskID:     &taskID,
			AuditLevel: lvl,
		}).Error; err != nil {
			log.Fatalf("seed change: %v", err)
		}
	}
}

func dumpHistoricalStats(db *gorm.DB, projectID string) {
	var total, completed, failed int64
	db.Model(&model.AgentSession{}).Where("project_id = ?", projectID).Count(&total)
	db.Model(&model.AgentSession{}).Where("project_id = ? AND status = ?", projectID, "completed").Count(&completed)
	db.Model(&model.AgentSession{}).Where("project_id = ? AND status = ?", projectID, "failed").Count(&failed)
	fmt.Printf("  sessions: %d total — %d completed, %d failed\n", total, completed, failed)
	var n int64
	db.Model(&model.ToolCallTrace{}).Where("project_id = ?", projectID).Count(&n)
	fmt.Printf("  tool traces: %d\n", n)
}

func dumpArtifacts(db *gorm.DB, projectID string) {
	var arts []model.KnowledgeArtifact
	db.Where("project_id = ?", projectID).Order("kind, confidence DESC").Find(&arts)
	fmt.Printf("  produced %d artifacts:\n", len(arts))
	for _, a := range arts {
		emb := "no-emb"
		if a.EmbeddingDim > 0 {
			emb = fmt.Sprintf("emb=%dd", a.EmbeddingDim)
		}
		fmt.Printf("    - [%-12s] %-42s conf=%.2f  status=%-10s  %s\n",
			a.Kind, truncate(a.Name, 42), a.Confidence, a.Status, emb)
	}
}

func createAndEmbedTask(db *gorm.DB, projectID, humanID, name, desc string) string {
	taskID := fmt.Sprintf("task_demo_%d", time.Now().UnixNano())
	if err := db.Create(&model.Task{
		ID: taskID, ProjectID: projectID, Name: name, Description: desc,
		Priority: "high", Status: "pending", CreatedBy: humanID,
	}).Error; err != nil {
		log.Fatalf("create task: %v", err)
	}
	// Synchronous embed so the following stages see the result immediately
	// (in production, EmbedTaskAsync fires in a goroutine).
	vecs, err := service.DefaultEmbeddingClient().EmbedDocuments(
		ctxT(15*time.Second), []string{"[task] " + name + "\n" + desc})
	if err != nil {
		log.Fatalf("embed task: %v", err)
	}
	now := time.Now()
	db.Model(&model.Task{}).Where("id = ?", taskID).Updates(map[string]any{
		"description_embedding":     service.MarshalEmbedding(vecs[0]),
		"description_embedding_dim": len(vecs[0]),
		"description_embedded_at":   &now,
	})
	return taskID
}

func dumpTaskEmbedding(db *gorm.DB, taskID string) {
	var t model.Task
	db.Where("id = ?", taskID).First(&t)
	status := "MISSING"
	if t.DescriptionEmbeddingDim > 0 {
		status = fmt.Sprintf("%d-dim, embedded_at=%s",
			t.DescriptionEmbeddingDim, t.DescriptionEmbeddedAt.Format("15:04:05"))
	}
	fmt.Printf("  task id=%s\n  title=%q\n  embedding: %s\n", t.ID, t.Name, status)
}

// showClaimHints simulates an MCP client agent calling task.claim and
// prints exactly what it will receive (including the injection reasons
// and the ids that travel back on change.submit for feedback accounting).
func showClaimHints(projectID, taskID, clientAgent string) {
	fmt.Printf("  (MCP client %s would POST /task/claim with task_id=%s)\n", clientAgent, taskID)
	hints, err := service.BuildTaskClaimHints(context.Background(), taskID)
	if err != nil {
		fmt.Printf("  error: %v\n", err)
		return
	}
	fmt.Printf("  selected %d of %d candidates (query_had_embedding=%v)\n",
		hints.Meta.Selected, hints.Meta.CandidatePool, hints.Meta.QueryHadEmbedding)

	printHintGroup := func(label string, items []service.HintItem) {
		if len(items) == 0 {
			return
		}
		fmt.Printf("\n  %s:\n", label)
		for _, h := range items {
			fmt.Printf("    - %-40s score=%.2f\n", truncate(h.Name, 40), h.Score)
			fmt.Printf("      reason: %s\n", h.Reason)
			fmt.Printf("      summary: %s\n", truncate(h.Summary, 100))
		}
	}
	printHintGroup("🛠 Recipes",      hints.Recipes)
	printHintGroup("✓ Patterns",       hints.Patterns)
	printHintGroup("⚠ Anti-patterns", hints.AntiPatterns)

	fmt.Printf("\n  injected_ids (carried back on change.submit for feedback):\n")
	fmt.Printf("    %s\n", strings.Join(hints.InjectedIDs, ", "))
}

func showChiefInjection(db *gorm.DB, projectID string) {
	var tasks []model.Task
	db.Where("project_id = ?", projectID).Find(&tasks)
	if len(tasks) == 0 {
		fmt.Println("  (no tasks to build a query from)")
		return
	}
	qparts := []string{tasks[0].Name}
	for i := 1; i < len(tasks) && i < 5; i++ {
		qparts = append(qparts, tasks[i].Name)
	}
	qtext := strings.Join(qparts, "\n")

	r := service.SelectArtifactsForInjection(context.Background(), service.ArtifactQuery{
		ProjectID: projectID, Audience: service.AudienceCommander, QueryText: qtext,
	})
	fmt.Printf("  query text (project work surface):\n    %q\n", truncate(qtext, 90))
	fmt.Printf("  injected into chief prompt (%d items, budget=commander):\n", len(r))
	for i, ia := range r {
		fmt.Printf("    #%d  score=%.2f  [%s] %s\n",
			i+1, ia.Score, ia.Artifact.Kind, truncate(ia.Artifact.Name, 55))
	}
}

func demonstrateFeedbackLoop(db *gorm.DB, projectID string) {
	var injected []model.KnowledgeArtifact
	db.Where("project_id = ? AND status IN ?", projectID, []string{"active", "candidate"}).
		Order("confidence DESC").Limit(3).Find(&injected)
	if len(injected) == 0 {
		fmt.Println("  (no artifacts for feedback demo)")
		return
	}

	ids := make([]string, 0, len(injected))
	for _, a := range injected {
		ids = append(ids, a.ID)
	}
	sessionID := "chief_demo"
	injectedJSON := fmt.Sprintf(`["%s"]`, strings.Join(ids, `","`))
	if err := db.Create(&model.AgentSession{
		ID: sessionID, Role: "chief", ProjectID: projectID,
		Status: "completed", InjectedArtifacts: injectedJSON, CreatedAt: time.Now(),
	}).Error; err != nil {
		log.Fatalf("seed chief session: %v", err)
	}

	fmt.Println("  before feedback:")
	for _, a := range injected {
		fmt.Printf("    - %-45s usage=%d success=%d failure=%d\n",
			truncate(a.Name, 45), a.UsageCount, a.SuccessCount, a.FailureCount)
	}

	service.HandleSessionCompletion(sessionID, projectID, "chief", "completed")

	fmt.Println("  after HandleSessionCompletion(\"completed\"):")
	var refreshed []model.KnowledgeArtifact
	db.Where("id IN ?", ids).Order("id").Find(&refreshed)
	for _, a := range refreshed {
		fmt.Printf("    - %-45s usage=%d success=%d failure=%d\n",
			truncate(a.Name, 45), a.UsageCount, a.SuccessCount, a.FailureCount)
	}
}

func showAnalyzeInjection(db *gorm.DB, projectID string) {
	var tasks []model.Task
	db.Where("project_id = ?", projectID).Find(&tasks)
	parts := make([]string, 0, len(tasks))
	for _, t := range tasks {
		parts = append(parts, t.Name)
	}
	qtext := strings.Join(parts, "\n")

	r := service.SelectArtifactsForInjection(context.Background(), service.ArtifactQuery{
		ProjectID: projectID, Audience: service.AudienceAnalyzer, QueryText: qtext,
	})
	fmt.Printf("  injected into analyze prompt (%d items, budget=analyzer):\n", len(r))
	for i, ia := range r {
		if i >= 10 {
			fmt.Printf("    ...(%d more)\n", len(r)-10)
			break
		}
		fmt.Printf("    #%d  score=%.2f  [%s] %s\n",
			i+1, ia.Score, ia.Artifact.Kind, truncate(ia.Artifact.Name, 55))
	}
}

type snap struct {
	id, name, kind, status       string
	confidence                   float64
	usage, success, failureCount int
}

func snapshotArtifacts(db *gorm.DB, projectID string) map[string]snap {
	var arts []model.KnowledgeArtifact
	db.Where("project_id = ?", projectID).Find(&arts)
	m := map[string]snap{}
	for _, a := range arts {
		m[a.ID] = snap{a.ID, a.Name, a.Kind, a.Status, a.Confidence,
			a.UsageCount, a.SuccessCount, a.FailureCount}
	}
	return m
}

func diffArtifacts(before, after map[string]snap) {
	changed := 0
	for id, a := range after {
		b, ok := before[id]
		if !ok {
			fmt.Printf("  + NEW   [%s] %s (status=%s)\n", a.kind, truncate(a.name, 55), a.status)
			changed++
			continue
		}
		if b.status != a.status {
			fmt.Printf("  ~ LIFE  [%s] %-40s  %s → %s\n", a.kind, truncate(a.name, 40), b.status, a.status)
			changed++
		}
		if b.usage != a.usage || b.success != a.success {
			fmt.Printf("  ~ FEED  [%s] %-40s  usage %d→%d / success %d→%d\n",
				a.kind, truncate(a.name, 40), b.usage, a.usage, b.success, a.success)
			changed++
		}
	}
	if changed == 0 {
		fmt.Println("  no changes")
	}
}

func dumpFinalHealth(db *gorm.DB, projectID string) {
	var arts []model.KnowledgeArtifact
	db.Where("project_id = ?", projectID).Find(&arts)
	type row struct{ total, active, candidate, deprecated int }
	stats := map[string]*row{}
	withEmb := 0
	for _, a := range arts {
		r := stats[a.Kind]
		if r == nil {
			r = &row{}
			stats[a.Kind] = r
		}
		r.total++
		switch a.Status {
		case "active":
			r.active++
		case "candidate":
			r.candidate++
		case "deprecated":
			r.deprecated++
		}
		if a.EmbeddingDim > 0 {
			withEmb++
		}
	}
	fmt.Printf("  artifact tally:\n")
	for kind, r := range stats {
		fmt.Printf("    %-14s total=%d  active=%d  candidate=%d  deprecated=%d\n",
			kind, r.total, r.active, r.candidate, r.deprecated)
	}
	fmt.Printf("  embedded: %d / %d artifacts\n", withEmb, len(arts))
}

// demonstrateClientFeedbackLoop shows the full "client agent
// self-evolution" circuit end-to-end in one panel:
//
//   1. Agent calls task.claim  → hints bundle returned, usage++
//   2. Agent submits change    → Change row carries injected_ids
//   3. Audit reaches a verdict → HandleChangeAudit fires
//   4. Artifact counters move  → success or failure bumped
//
// This is the loop that lets refinery's lifecycle rules reason about
// "does this pattern actually help MCP clients?" — until this stage
// the only feedback source was Chief/Analyze sessions.
func demonstrateClientFeedbackLoop(db *gorm.DB, projectID, clientAgent string) {
	// Step 1: create a fresh task, embed it, then pull the claim hints
	// (same path a real MCP agent would take).
	taskID := createAndEmbedTask(db, projectID, "agent_human",
		"再次修复登录 401 回归", "前一次修复后又冒出来了,定位根因")
	fmt.Printf("  1. %s claims task %q\n", clientAgent, taskID)

	hints, err := service.BuildTaskClaimHints(ctxT(5*time.Second), taskID)
	if err != nil || hints == nil || len(hints.InjectedIDs) == 0 {
		fmt.Println("     (no hints produced — client receives task only)")
		return
	}
	fmt.Printf("     got %d hints, injected_ids=%s\n",
		len(hints.InjectedIDs), hints.InjectedIDs)

	// Snapshot counters BEFORE the audit verdict so we can show the diff.
	beforeCounts := map[string]int{}
	var arts []model.KnowledgeArtifact
	db.Where("id IN ?", hints.InjectedIDs).Find(&arts)
	for _, a := range arts {
		beforeCounts[a.ID] = a.SuccessCount
	}

	// Step 2: simulate a change.submit that carries the injected_ids
	// through. Real clients go through the HTTP handler; we call the
	// library layer directly for a deterministic demo.
	changeID := "chg_demo_fb"
	injectedJSON := `["` + stringsJoin(hints.InjectedIDs, `","`) + `"]`
	if err := db.Create(&model.Change{
		ID: changeID, ProjectID: projectID, AgentID: clientAgent,
		TaskID:            &taskID,
		Version:           "v1",
		Status:            "pending",
		InjectedArtifacts: injectedJSON,
	}).Error; err != nil {
		log.Fatalf("seed change: %v", err)
	}
	fmt.Printf("  2. change %s submitted with injected_artifacts=%d ids\n",
		changeID, len(hints.InjectedIDs))

	// Step 3: audit reaches an L0 verdict → feedback fires.
	fmt.Println("  3. audit verdict L0 (clean change) — HandleChangeAudit fires")
	service.HandleChangeAudit(changeID, "L0")

	// Step 4: inspect counter movement.
	fmt.Println("  4. counters after:")
	var after []model.KnowledgeArtifact
	db.Where("id IN ?", hints.InjectedIDs).Order("id").Find(&after)
	for _, a := range after {
		delta := a.SuccessCount - beforeCounts[a.ID]
		tag := " "
		if delta > 0 {
			tag = "↑"
		}
		fmt.Printf("     %s [%s] %-45s success=%d (was %d) %s\n",
			tag, a.Kind, truncate(a.Name, 45), a.SuccessCount,
			beforeCounts[a.ID], ifDelta(delta))
	}

	// Idempotency proof: calling the hook again must be a no-op.
	service.HandleChangeAudit(changeID, "L0")
	var verify model.KnowledgeArtifact
	db.Where("id = ?", hints.InjectedIDs[0]).First(&verify)
	fmt.Printf("     (idempotency check: second call left success_count at %d)\n",
		verify.SuccessCount)

	var ch model.Change
	db.Where("id = ?", changeID).First(&ch)
	fmt.Printf("  Change.feedback_applied = %v — future re-audits will be no-ops\n",
		ch.FeedbackApplied)
}

func ifDelta(d int) string {
	if d > 0 {
		return fmt.Sprintf("(+%d)", d)
	}
	return ""
}

// stringsJoin lives here so we don't import "strings" just for one call.
func stringsJoin(parts []string, sep string) string {
	return strings.Join(parts, sep)
}

// demonstrateTagLifecycle walks end-to-end through the PR 6 tag machinery:
//   1. Rule engine proposes tags for a brand-new task
//   2. We show both confirmed tags (from history) and proposed tags for
//      the new task side by side
//   3. Simulate a human confirming one, rejecting another
//   4. Re-run the selector for the task and show how the tag score
//      shifted on the top hint
//
// The goal is that a reviewer can eyeball "before vs after" and see
// that the lifecycle actually moves the ranking — otherwise why bother.
func demonstrateTagLifecycle(db *gorm.DB, projectID, humanID string) {
	// Make a fresh task with obvious keyword triggers so the rule engine
	// has real material to work on.
	taskID := createAndEmbedTask(db, projectID, humanID,
		"修复 JWT 鉴权中间件 crash",
		"登录接口周期性 crash,怀疑是中间件 bug, 需要定位并修复")
	fmt.Printf("  task: %s\n", taskID)

	// Fire the rule engine (handler path in prod; library call here).
	service.ProposeAndPersistTagsForTask(taskID,
		"修复 JWT 鉴权中间件 crash",
		"登录接口周期性 crash,怀疑是中间件 bug, 需要定位并修复")

	fmt.Println("\n  Rule engine proposed:")
	var proposed []model.TaskTag
	db.Where("task_id = ? AND status = ?", taskID, "proposed").Find(&proposed)
	for _, t := range proposed {
		fmt.Printf("    - [%-8s] %-10s conf=%.2f  source=%s\n",
			t.Dimension, t.Tag, t.Confidence, t.Source)
	}

	// Capture the hint scores BEFORE any human review.
	beforeHints, err := service.BuildTaskClaimHints(ctxT(5*time.Second), taskID)
	if err != nil || beforeHints == nil {
		fmt.Printf("  (could not build before-hints: %v)\n", err)
		return
	}
	fmt.Println("\n  Before any human review — top hint tag score:")
	if len(beforeHints.Recipes) > 0 || len(beforeHints.Patterns) > 0 || len(beforeHints.AntiPatterns) > 0 {
		showTopHintScore("    before", beforeHints)
	} else {
		fmt.Println("    (no hints produced yet)")
	}

	// Human confirms the bugfix tag, rejects any 'refactor' misfire (if
	// the rules emitted one — they shouldn't on this text but the call
	// is safe regardless).
	if len(proposed) == 0 {
		fmt.Println("\n  (no proposed tags — skipping confirm step)")
		return
	}
	// Pick a plausible bugfix tag to confirm; reject everything else
	// so the selector ignores rule noise.
	var confirmedTagID string
	for _, t := range proposed {
		if t.Tag == "bugfix" {
			if err := service.ConfirmTag(t.ID, humanID, "correct — is a crash"); err != nil {
				log.Printf("ConfirmTag: %v", err)
			}
			confirmedTagID = t.ID
			fmt.Printf("\n  human confirmed: [%s] %s (tag_id=%s)\n", t.Dimension, t.Tag, t.ID)
			break
		}
	}
	// Reject the rest to model a real review pass.
	for _, t := range proposed {
		if t.ID == confirmedTagID {
			continue
		}
		if err := service.RejectTag(t.ID, humanID, "not relevant"); err != nil {
			log.Printf("RejectTag: %v", err)
		}
		fmt.Printf("  human rejected:  [%s] %s\n", t.Dimension, t.Tag)
	}

	// After the review, re-score.
	afterHints, err := service.BuildTaskClaimHints(ctxT(5*time.Second), taskID)
	if err != nil || afterHints == nil {
		fmt.Printf("  (could not build after-hints: %v)\n", err)
		return
	}
	fmt.Println("\n  After human review — top hint tag score:")
	showTopHintScore("    after ", afterHints)
}

// showTopHintScore prints the first hint across recipes/patterns/anti-
// patterns (whichever has content) with its score and reason so we can
// compare runs.
func showTopHintScore(label string, h *service.TaskClaimHints) {
	var top *service.HintItem
	var kind string
	if len(h.Recipes) > 0 {
		top = &h.Recipes[0]
		kind = "recipe"
	} else if len(h.Patterns) > 0 {
		top = &h.Patterns[0]
		kind = "pattern"
	} else if len(h.AntiPatterns) > 0 {
		top = &h.AntiPatterns[0]
		kind = "anti_pattern"
	}
	if top == nil {
		fmt.Printf("%s  (no hints)\n", label)
		return
	}
	fmt.Printf("%s  [%s] %s  score=%.3f\n", label, kind, truncate(top.Name, 50), top.Score)
	fmt.Printf("%s  reason: %s\n", label, top.Reason)
}

// --- Misc helpers --------------------------------------------------------

func mustOpenDB() *gorm.DB {
	db, err := gorm.Open(sqlite.Open("file:e2erun?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		log.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&model.Project{}, &model.Agent{}, &model.Task{},
		&model.AgentSession{}, &model.ToolCallTrace{},
		&model.Change{}, &model.Episode{},
		&model.KnowledgeArtifact{}, &model.RefineryRun{},
		&model.TaskTag{},
	); err != nil {
		log.Fatalf("migrate: %v", err)
	}
	return db
}

func banner(n int, title string) {
	line := strings.Repeat("─", 72)
	fmt.Printf("\n%s\n  Stage %d · %s\n%s\n", line, n, title, line)
}

func ctxT(d time.Duration) context.Context {
	ctx, _ := context.WithTimeout(context.Background(), d)
	return ctx
}

func ptrStr(s string) *string { return &s }

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
