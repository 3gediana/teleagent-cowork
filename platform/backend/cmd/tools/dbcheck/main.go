package main

import (
	"fmt"
	"log"

	"github.com/a3c/platform/internal/config"
	"github.com/a3c/platform/internal/model"
)

func main() {
	cfg := config.Load("../../configs/config.yaml")
	if err := model.InitDB(&cfg.Database); err != nil {
		log.Fatal(err)
	}

	// Check agent_session columns
	type ColInfo struct {
		Field string
		Type  string
	}
	var cols []ColInfo
	model.DB.Raw("SELECT column_name as field, data_type as type FROM information_schema.columns WHERE table_name = 'agent_session' AND table_schema = DATABASE() ORDER BY ordinal_position").Scan(&cols)
	fmt.Println("agent_session columns:")
	for _, c := range cols {
		fmt.Printf("  %s (%s)\n", c.Field, c.Type)
	}

	// Check recent sessions
	type Session struct {
		ID                string
		Role              string
		Status            string
		OpenCodeSessionID string
		Output            string
	}
	var sessions []Session
	model.DB.Raw("SELECT id, role, status, opencode_session_id, output FROM agent_session ORDER BY created_at DESC LIMIT 5").Scan(&sessions)
	fmt.Println("\nRecent sessions:")
	for _, s := range sessions {
		fmt.Printf("  %s | role=%s | status=%s | oc_sess=%s | out_len=%d\n", s.ID, s.Role, s.Status, s.OpenCodeSessionID, len(s.Output))
	}

	// Check tool_call_trace columns
	var traceCols []ColInfo
	model.DB.Raw("SELECT column_name as field, data_type as type FROM information_schema.columns WHERE table_name = 'tool_call_trace' AND table_schema = DATABASE() ORDER BY ordinal_position").Scan(&traceCols)
	fmt.Println("\ntool_call_trace columns:")
	for _, c := range traceCols {
		fmt.Printf("  %s (%s)\n", c.Field, c.Type)
	}

	// Count records
	var expCount, polCount, skillCount int64
	model.DB.Model(&model.Experience{}).Count(&expCount)
	model.DB.Model(&model.Policy{}).Count(&polCount)
	model.DB.Model(&model.SkillCandidate{}).Count(&skillCount)
	fmt.Printf("\nExperiences: %d | Policies: %d | Skills: %d\n", expCount, polCount, skillCount)
}
