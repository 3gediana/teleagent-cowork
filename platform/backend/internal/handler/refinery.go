package handler

import (
	"log"
	"sort"
	"strconv"
	"time"

	"github.com/a3c/platform/internal/model"
	"github.com/a3c/platform/internal/service/refinery"
	"github.com/gin-gonic/gin"
)

// RefineryHandler exposes the Refinery pipeline over HTTP so the frontend
// (and humans with curl) can trigger runs, inspect run history, and browse
// produced knowledge artifacts.
type RefineryHandler struct {
	r *refinery.Refinery
}

func NewRefineryHandler() *RefineryHandler {
	return &RefineryHandler{r: refinery.New()}
}

// Run triggers an on-demand refinery run for a project.
// POST /refinery/run  {project_id, lookback_hours?}
// Human-only (to prevent agents from spamming expensive analysis).
// The run executes asynchronously — this endpoint returns the run ID immediately.
func (h *RefineryHandler) Run(c *gin.Context) {
	if !callerIsHuman(c) {
		c.JSON(403, gin.H{"success": false, "error": gin.H{"code": "HUMAN_ONLY", "message": "refinery run is human-only"}})
		return
	}
	var req struct {
		ProjectID     string `json:"project_id"`
		LookbackHours int    `json:"lookback_hours"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": err.Error()}})
		return
	}
	if req.ProjectID == "" {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": "project_id is required"}})
		return
	}
	if req.LookbackHours <= 0 {
		req.LookbackHours = 24 * 14 // default: last 14 days
	}

	// Create a stub run record so we can return the ID immediately
	run := &model.RefineryRun{
		ID:        model.GenerateID("rrun"),
		ProjectID: req.ProjectID,
		Trigger:   "manual",
		StartedAt: time.Now(),
		Status:    "pending",
		PassStats: "{}",
	}
	if err := model.DB.Create(run).Error; err != nil {
		c.JSON(500, gin.H{"success": false, "error": gin.H{"code": "REFINERY_ERROR", "message": err.Error()}})
		return
	}

	// Execute the actual refinery run in a goroutine. We pass the stub's
	// ID through RunWithID so the pipeline updates the same row instead
	// of creating a duplicate RefineryRun record.
	go func() {
		if _, err := h.r.RunWithID(run.ID, req.ProjectID, req.LookbackHours, "manual"); err != nil {
			log.Printf("[Refinery] Async run %s failed: %v", run.ID, err)
			// Ensure the stub ends up in an error state even if Run
			// bailed before persisting final status.
			model.DB.Model(&model.RefineryRun{}).Where("id = ? AND status IN ?", run.ID, []string{"pending", "running"}).
				Updates(map[string]interface{}{
					"status": "error",
					"error":  err.Error(),
				})
		}
	}()

	c.JSON(202, gin.H{"success": true, "data": gin.H{"run_id": run.ID, "status": "pending"}})
}

// Runs returns the recent refinery runs for a project.
// GET /refinery/runs?project_id=...&limit=20
func (h *RefineryHandler) Runs(c *gin.Context) {
	projectID := c.Query("project_id")
	if projectID == "" {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": "project_id is required"}})
		return
	}
	limit := 20
	if l := c.Query("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	var runs []model.RefineryRun
	model.DB.Where("project_id = ?", projectID).Order("started_at DESC").Limit(limit).Find(&runs)
	c.JSON(200, gin.H{"success": true, "data": gin.H{"runs": runs}})
}

// Artifacts lists KnowledgeArtifacts for a project, optionally filtered by kind/status.
// Global (project_id="") artifacts are included — every project should see
// cross-project knowledge.
// GET /refinery/artifacts?project_id=...&kind=pattern&status=candidate&limit=100
func (h *RefineryHandler) Artifacts(c *gin.Context) {
	projectID := c.Query("project_id")
	if projectID == "" {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": "project_id is required"}})
		return
	}
	// Include project-scoped + global artifacts.
	q := model.DB.Model(&model.KnowledgeArtifact{}).
		Where("project_id = ? OR project_id = ''", projectID)
	if k := c.Query("kind"); k != "" {
		q = q.Where("kind = ?", k)
	}
	if s := c.Query("status"); s != "" {
		q = q.Where("status = ?", s)
	}
	limit := 100
	if l := c.Query("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}
	var arts []model.KnowledgeArtifact
	q.Order("updated_at DESC").Limit(limit).Find(&arts)

	// Summary counts (scoped to project + global). Not filtered by kind/status
	// so the dashboard cards always show the full picture.
	type kindCount struct {
		Kind  string `json:"kind"`
		Total int64  `json:"total"`
	}
	var counts []kindCount
	cq := model.DB.Model(&model.KnowledgeArtifact{}).Select("kind, COUNT(*) as total").
		Where("project_id = ? OR project_id = ''", projectID).Group("kind")
	cq.Scan(&counts)

	c.JSON(200, gin.H{"success": true, "data": gin.H{
		"artifacts": arts,
		"counts":    counts,
	}})
}

// Growth returns a time-series of artifact counts per kind for charting.
// GET /refinery/growth?project_id=...&days=30
func (h *RefineryHandler) Growth(c *gin.Context) {
	projectID := c.Query("project_id")
	if projectID == "" {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": "project_id is required"}})
		return
	}
	days := 30
	if d := c.Query("days"); d != "" {
		if n, err := strconv.Atoi(d); err == nil && n > 0 && n <= 365 {
			days = n
		}
	}

	// Load artifacts created within the lookback window (project + global)
	// and aggregate in Go to avoid MySQL/SQLite date function incompatibilities.
	since := time.Now().AddDate(0, 0, -days)
	q := model.DB.Model(&model.KnowledgeArtifact{}).
		Where("created_at >= ?", since).
		Where("project_id = ? OR project_id = ''", projectID)
	var arts []model.KnowledgeArtifact
	q.Find(&arts)

	// Aggregate: day → kind → count
	type dayKind struct {
		Day   string `json:"day"`
		Kind  string `json:"kind"`
		Count int    `json:"count"`
	}
	acc := map[string]map[string]int{} // "2006-01-02" → kind → count
	for _, a := range arts {
		dayStr := a.CreatedAt.Format("2006-01-02")
		if acc[dayStr] == nil {
			acc[dayStr] = map[string]int{}
		}
		acc[dayStr][a.Kind]++
	}

	// Flatten and sort
	var rows []dayKind
	for day, kinds := range acc {
		for kind, count := range kinds {
			rows = append(rows, dayKind{Day: day, Kind: kind, Count: count})
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Day < rows[j].Day })

	c.JSON(200, gin.H{"success": true, "data": gin.H{"series": rows, "days": days}})
}

// UpdateArtifactStatus changes the status of a knowledge artifact.
// PUT /refinery/artifacts/:id/status  {status: "active"|"deprecated"|"rejected"}
// Human-only.
func (h *RefineryHandler) UpdateArtifactStatus(c *gin.Context) {
	if !callerIsHuman(c) {
		c.JSON(403, gin.H{"success": false, "error": gin.H{"code": "HUMAN_ONLY", "message": "artifact status changes are human-only"}})
		return
	}
	artifactID := c.Param("id")
	var req struct {
		Status string `json:"status" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": err.Error()}})
		return
	}
	allowed := map[string]bool{"active": true, "deprecated": true, "rejected": true, "candidate": true}
	if !allowed[req.Status] {
		c.JSON(400, gin.H{"success": false, "error": gin.H{"code": "INVALID_PARAMS", "message": "status must be one of: active, deprecated, rejected, candidate"}})
		return
	}
	result := model.DB.Model(&model.KnowledgeArtifact{}).Where("id = ?", artifactID).
		Updates(map[string]interface{}{"status": req.Status, "updated_at": time.Now()})
	if result.RowsAffected == 0 {
		c.JSON(404, gin.H{"success": false, "error": gin.H{"code": "NOT_FOUND", "message": "artifact not found"}})
		return
	}
	c.JSON(200, gin.H{"success": true, "data": gin.H{"id": artifactID, "status": req.Status}})
}
