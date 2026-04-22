package service

// Tag lifecycle operations
// ========================
//
// ProposeAndPersistTagsForTask   — rule engine output → DB rows with
//                                  Status=proposed. Safe to call on any
//                                  task, any number of times (dedup by
//                                  (TaskID, Dimension, Tag)).
// ConfirmTag / RejectTag / SupersedeTag — reviewer actions that flip
//                                  a tag's Status without deleting
//                                  anything (audit trail stays).
//
// All of these are idempotent and tolerant of missing rows: callers
// (handler, scheduler, Analyze Agent) don't need to lock or guard.

import (
	"encoding/json"
	"log"
	"strings"
	"time"

	"github.com/a3c/platform/internal/model"
)

// ProposeAndPersistTagsForTask runs the tag rule engine against a
// task's name + description, then writes every proposal to TaskTag as a
// proposed row (idempotent on the (TaskID, Dimension, Tag) natural key).
//
// Safe to invoke repeatedly: if a proposal already exists in any
// lifecycle state (confirmed, rejected, superseded), we leave it alone
// and never re-propose — that's what the rejected state is for ("this
// rule keeps trying to tag bugfix but we disagreed").
func ProposeAndPersistTagsForTask(taskID, name, description string) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[TagRules] recovered from panic on task=%s: %v", taskID, r)
		}
	}()

	if taskID == "" {
		return
	}

	proposals := ProposeTagsFromText(name, description)
	if len(proposals) == 0 {
		return
	}

	// Short-circuit: pull the set of (Dimension, Tag) pairs that are
	// already on the task in any state. We won't overwrite them. The
	// rejected state deliberately blocks the rule from re-proposing,
	// which is the primary mechanism for retiring a bad rule without
	// editing code.
	var existing []model.TaskTag
	model.DB.Where("task_id = ?", taskID).Find(&existing)
	present := make(map[string]bool, len(existing))
	for _, t := range existing {
		present[t.Dimension+"/"+t.Tag] = true
	}

	now := time.Now()
	for _, p := range proposals {
		key := p.Dimension + "/" + p.Tag
		if present[key] {
			continue
		}
		row := &model.TaskTag{
			ID:         model.GenerateID("ttag"),
			TaskID:     taskID,
			Tag:        p.Tag,
			Dimension:  p.Dimension,
			Source:     p.Source,
			Status:     "proposed",
			Confidence: p.Confidence,
			Evidence:   EvidenceJSON(p.Evidence),
			CreatedAt:  now,
			UpdatedAt:  now,
		}
		if err := model.DB.Create(row).Error; err != nil {
			log.Printf("[TagRules] failed to persist %s/%s on task %s: %v",
				p.Dimension, p.Tag, taskID, err)
			continue
		}
	}
	log.Printf("[TagRules] task=%s proposed=%d (skipped_existing=%d)",
		taskID, len(proposals)-countPresent(proposals, present), countPresent(proposals, present))
}

func countPresent(proposals []TagProposal, present map[string]bool) int {
	n := 0
	for _, p := range proposals {
		if present[p.Dimension+"/"+p.Tag] {
			n++
		}
	}
	return n
}

// ConfirmTag marks a proposed / superseded tag as confirmed by
// `reviewerID` with an optional note (appended to Evidence). Safe to
// call on an already-confirmed tag (no-op, idempotent).
func ConfirmTag(tagID, reviewerID, note string) error {
	return transitionTag(tagID, "confirmed", reviewerID, note, "")
}

// RejectTag marks the tag rejected. The tag row stays in the DB so we
// remember not to re-propose it (and so we can measure rule accuracy).
func RejectTag(tagID, reviewerID, note string) error {
	return transitionTag(tagID, "rejected", reviewerID, note, "")
}

// SupersedeTag marks `oldTagID` as superseded by `newTagID`. Intended
// for "the rule said 'bugfix' but actually it's 'security+bugfix'"
// workflows — the reviewer creates the new tag first, then calls this.
func SupersedeTag(oldTagID, newTagID, reviewerID string) error {
	return transitionTag(oldTagID, "superseded", reviewerID, "", newTagID)
}

// AnalyzeReviewTag is the Analyze-Agent-facing wrapper around ConfirmTag /
// RejectTag that refuses to overwrite a decision a human has already
// made on the same tag. Human authority is the stronger signal; Analyze
// gets to review rule-proposed tags but never to clobber direct human
// intervention.
//
// action ∈ {"confirm", "reject"}; anything else is a no-op.
func AnalyzeReviewTag(tagID, action, note string) error {
	var t model.TaskTag
	if err := model.DB.Where("id = ?", tagID).First(&t).Error; err != nil {
		return err
	}
	// Skip if a human already weighed in on this tag — refuse silently
	// (return nil) rather than error, so an Analyze run can iterate a
	// bulk review list without tripping on any individual human-touched
	// item.
	if strings.HasPrefix(t.ReviewedBy, "human:") {
		log.Printf("[AnalyzeReview] skipping tag %s: already reviewed by %s", tagID, t.ReviewedBy)
		return nil
	}
	reviewer := "analyze/v1"
	switch action {
	case "confirm":
		return ConfirmTag(tagID, reviewer, note)
	case "reject":
		return RejectTag(tagID, reviewer, note)
	default:
		log.Printf("[AnalyzeReview] unknown action %q for tag %s — skipping", action, tagID)
		return nil
	}
}

func transitionTag(tagID, toStatus, reviewerID, note, supersededBy string) error {
	var t model.TaskTag
	if err := model.DB.Where("id = ?", tagID).First(&t).Error; err != nil {
		return err
	}
	if t.Status == toStatus {
		return nil // idempotent
	}

	updates := map[string]any{
		"status":      toStatus,
		"reviewed_by": reviewerID,
		"updated_at":  time.Now(),
	}
	now := time.Now()
	updates["reviewed_at"] = &now

	// Bump confidence to full when a human confirms; reset when
	// rejected so downstream scoring respects the verdict.
	switch toStatus {
	case "confirmed":
		updates["confidence"] = 1.0
	case "rejected":
		updates["confidence"] = 0.0
	case "superseded":
		updates["superseded_by"] = supersededBy
	}

	if note != "" {
		// Append note to Evidence as a "review_note" field. Parse-and-
		// re-marshal so we don't corrupt existing structured data.
		updatedEv := appendEvidenceNote(t.Evidence, reviewerID, note)
		updates["evidence"] = updatedEv
	}

	return model.DB.Model(&model.TaskTag{}).Where("id = ?", tagID).Updates(updates).Error
}

// appendEvidenceNote adds a reviewer note to a tag's existing evidence
// JSON (or seeds a fresh blob if none). Handles malformed existing
// evidence by wrapping it as a legacy field so nothing gets lost.
func appendEvidenceNote(existing, reviewer, note string) string {
	ev := decodeEvidenceOrEmpty(existing)
	// Append a single note; for now we keep only the latest (a review
	// history would be another column, not an ever-growing JSON blob).
	ev["review_note"] = map[string]string{
		"by":   reviewer,
		"note": note,
	}
	return EvidenceJSON(ev)
}

func decodeEvidenceOrEmpty(existing string) map[string]any {
	if existing == "" {
		return map[string]any{}
	}
	// Use EvidenceJSON's inverse — a simple json decode. If it fails,
	// preserve the original string under "legacy_evidence" so nothing
	// is lost.
	var parsed map[string]any
	if err := json.Unmarshal([]byte(existing), &parsed); err == nil && parsed != nil {
		return parsed
	}
	return map[string]any{"legacy_evidence": existing}
}
