package service

import (
	"encoding/json"
	"log"

	"github.com/a3c/platform/internal/model"
)

// MarshalToJSON is a helper to marshal any value to a JSON string.
func MarshalToJSON(v interface{}, dest *string) {
	b, err := json.Marshal(v)
	if err != nil {
		log.Printf("[Experience] Failed to marshal: %v", err)
		*dest = "{}"
		return
	}
	*dest = string(b)
}

// CreateExperienceFromAudit creates an Experience from audit observation.
func CreateExperienceFromAudit(projectID, sessionID, agentRole, taskID, patternObserved, suggestionForSubmitter string) {
	exp := model.Experience{
		ID:              model.GenerateID("exp"),
		ProjectID:       projectID,
		SourceType:      "audit_observation",
		SourceID:        sessionID,
		AgentRole:       agentRole,
		TaskID:          taskID,
		PatternObserved: patternObserved,
		DoDifferently:   suggestionForSubmitter,
		Status:          "raw",
	}
	if err := model.DB.Create(&exp).Error; err != nil {
		log.Printf("[Experience] Failed to create audit observation: %v", err)
	}
}

// CreateExperienceFromFix creates an Experience from fix strategy.
func CreateExperienceFromFix(projectID, sessionID, taskID, fixStrategy string, falsePositive bool) {
	outcome := "success"
	if falsePositive {
		outcome = "partial"
	}
	exp := model.Experience{
		ID:            model.GenerateID("exp"),
		ProjectID:     projectID,
		SourceType:    "fix_strategy",
		SourceID:      sessionID,
		AgentRole:     "fix",
		TaskID:        taskID,
		FixStrategy:   fixStrategy,
		FalsePositive: falsePositive,
		Outcome:       outcome,
		Status:        "raw",
	}
	if err := model.DB.Create(&exp).Error; err != nil {
		log.Printf("[Experience] Failed to create fix strategy: %v", err)
	}
}

// CreateExperienceFromEvaluate creates an Experience from evaluate patterns.
func CreateExperienceFromEvaluate(projectID, sessionID, prID, qualityPatterns, commonMistakes string) {
	exp := model.Experience{
		ID:              model.GenerateID("exp"),
		ProjectID:       projectID,
		SourceType:      "eval_pattern",
		SourceID:        sessionID,
		AgentRole:       "evaluate",
		TaskID:          prID,
		QualityPatterns: qualityPatterns,
		Pitfalls:        commonMistakes,
		Status:          "raw",
	}
	if err := model.DB.Create(&exp).Error; err != nil {
		log.Printf("[Experience] Failed to create eval pattern: %v", err)
	}
}

// CreateExperienceFromBizReview creates an Experience from business review rationale.
func CreateExperienceFromBizReview(projectID, sessionID, prID, alignmentRationale string) {
	exp := model.Experience{
		ID:          model.GenerateID("exp"),
		ProjectID:   projectID,
		SourceType:  "maintain_rationale",
		SourceID:    sessionID,
		AgentRole:   "maintain",
		TaskID:      prID,
		KeyInsight:  alignmentRationale,
		Status:      "raw",
	}
	if err := model.DB.Create(&exp).Error; err != nil {
		log.Printf("[Experience] Failed to create biz review rationale: %v", err)
	}
}
