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

	// Drop old column if exists
	result := model.DB.Exec("ALTER TABLE agent_session DROP COLUMN open_code_session_id")
	if result.Error != nil {
		fmt.Printf("Drop old column: %v (may not exist)\n", result.Error)
	} else {
		fmt.Println("Dropped old column open_code_session_id")
	}

	// Verify
	type ColInfo struct {
		Field string
	}
	var cols []ColInfo
	model.DB.Raw("SELECT column_name as field FROM information_schema.columns WHERE table_name = 'agent_session' AND table_schema = DATABASE() AND column_name LIKE '%session_id%'").Scan(&cols)
	fmt.Println("Session ID columns:")
	for _, c := range cols {
		fmt.Printf("  %s\n", c.Field)
	}
}
