package service

// Task description → embedding pipeline
// =====================================
//
// A task's (Name + Description) is the canonical "search query" the
// injection selector later uses to find semantically relevant artifacts.
// We embed it as soon as the task is created and store the result on the
// Task row, so at injection time we can skip an expensive round-trip to
// the sidecar for every agent prompt build.
//
// Concurrency / failure model
// ---------------------------
// Embedding is best-effort, asynchronous, and idempotent:
//
//   - The Create handler calls EmbedTaskAsync in a goroutine so the user
//     gets their HTTP response immediately even if the sidecar is slow
//     (the first query for this task would still have to wait, but task
//     creation is the critical path).
//   - A failure is logged, not propagated; BackfillTaskEmbeddings cleans
//     up later.
//   - Idempotent: re-running the same task through the pipeline is a
//     no-op (we re-embed only when text actually changes).
//
// This keeps "the sidecar is down" from taking down the platform.

import (
	"context"
	"log"
	"time"

	"github.com/a3c/platform/internal/model"
)

// taskEmbeddingText is the canonical text fed into the embedder for a
// task. Mirrors the refinery.artifactEmbeddingText format so queries
// (tasks) and documents (artifacts) live in compatible semantic spaces.
// Keeping them symmetric is what lets cosine similarity be meaningful.
func taskEmbeddingText(name, description string) string {
	switch {
	case description != "" && name != "":
		return "[task] " + name + "\n" + description
	case name != "":
		return "[task] " + name
	default:
		return description
	}
}

// EmbedTaskAsync spawns a goroutine to compute and store a task's
// description embedding. The caller gets immediate control back — the
// HTTP handler shouldn't block on the sidecar.
//
// Safe to call any number of times: each call checks whether the current
// stored embedding is already up-to-date for the text, re-embedding only
// if the text changed or no embedding exists yet.
func EmbedTaskAsync(taskID string) {
	go func() {
		if err := embedTaskSync(taskID); err != nil {
			log.Printf("[TaskEmbed] %s: %v", taskID, err)
		}
	}()
}

// embedTaskSync is the synchronous core. Exposed (lowercase so only
// in-package tests can reach it) for deterministic testing.
func embedTaskSync(taskID string) error {
	var task model.Task
	if err := model.DB.Where("id = ?", taskID).First(&task).Error; err != nil {
		return err
	}

	text := taskEmbeddingText(task.Name, task.Description)
	if text == "" {
		// Nothing meaningful to embed — skip. Can happen for tasks
		// created from MCP automation with a stub name.
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	vecs, err := DefaultEmbeddingClient().EmbedDocuments(ctx, []string{text})
	if err != nil {
		return err
	}
	if len(vecs) != 1 || len(vecs[0]) == 0 {
		return nil // no-op rather than error: sidecar reported empty
	}

	now := time.Now()
	return model.DB.Model(&model.Task{}).Where("id = ?", taskID).Updates(map[string]any{
		"description_embedding":     MarshalEmbedding(vecs[0]),
		"description_embedding_dim": len(vecs[0]),
		"description_embedded_at":   &now,
	}).Error
}

// -- Backfill -------------------------------------------------------------

// BackfillTaskEmbeddings finds tasks without an embedding and fills them
// in. Intended to run once at startup (handles rows created while the
// sidecar was down) and periodically thereafter. Caps itself at `limit`
// tasks per run so a large backlog doesn't hammer the sidecar.
//
// Returns the number of tasks successfully embedded in this pass. The
// caller can loop until zero to drain a backlog.
func BackfillTaskEmbeddings(limit int) int {
	if limit <= 0 {
		limit = 100
	}

	var tasks []model.Task
	// Deleted tasks are skipped — no value in embedding tombstones.
	err := model.DB.
		Where("(description_embedding IS NULL OR LENGTH(description_embedding) = 0) AND deleted_at IS NULL").
		Order("created_at DESC").
		Limit(limit).
		Find(&tasks).Error
	if err != nil || len(tasks) == 0 {
		return 0
	}

	done := 0
	for _, t := range tasks {
		if err := embedTaskSync(t.ID); err != nil {
			log.Printf("[TaskEmbed/backfill] %s: %v", t.ID, err)
			continue
		}
		done++
	}
	if done > 0 {
		log.Printf("[TaskEmbed/backfill] embedded %d/%d tasks", done, len(tasks))
	}
	return done
}

// StartTaskEmbeddingBackfillTimer runs BackfillTaskEmbeddings periodically
// in the background. Call once from main.go. The first pass fires ~30s
// after startup so the sidecar has time to warm up.
func StartTaskEmbeddingBackfillTimer() {
	go func() {
		time.Sleep(30 * time.Second)
		BackfillTaskEmbeddings(200)

		ticker := time.NewTicker(15 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			BackfillTaskEmbeddings(200)
		}
	}()
}
