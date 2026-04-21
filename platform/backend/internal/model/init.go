package model

import (
	"context"
	"fmt"
	"log"

	"github.com/redis/go-redis/v9"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"github.com/a3c/platform/internal/config"
)

var (
	DB    *gorm.DB
	RDB   *redis.Client
)

func InitDB(cfg *config.DatabaseConfig) error {
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=True&loc=Local",
		cfg.User, cfg.Password, cfg.Host, cfg.Port, cfg.DBName)

	var err error
	DB, err = gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		return fmt.Errorf("failed to connect database: %w", err)
	}

	// Ensure connection uses utf8mb4
	DB.Exec("SET NAMES utf8mb4 COLLATE utf8mb4_unicode_ci")

	// Drop old indexes if they exist (MySQL doesn't support IF EXISTS for DROP INDEX, so we ignore errors)
	if DB.Migrator().HasTable(&Agent{}) {
		DB.Exec("ALTER TABLE agent DROP INDEX uni_agent_name") // may fail, that's OK
		DB.Exec("ALTER TABLE agent DROP INDEX idx_agent_name") // may fail, that's OK
	}
	// Drop old unique index on role_override if it exists from previous schema
	if DB.Migrator().HasTable(&RoleOverride{}) {
		DB.Exec("ALTER TABLE role_override DROP INDEX uni_role_override_role") // may fail, that's OK
	}

	if err = DB.AutoMigrate(
		&Project{}, &Agent{}, &ContentBlock{},
		&Milestone{}, &MilestoneArchive{},
		&Task{}, &FileLock{}, &Change{},
		&Branch{}, &PullRequest{}, &RoleOverride{},
		&AgentSession{}, &ToolCallTrace{}, &TaskTag{},
	); err != nil {
		log.Printf("[DB] AutoMigrate warning: %v (attempting retry)", err)
		// Retry once - GORM sometimes fails on first pass with index issues
		if err2 := DB.AutoMigrate(&RoleOverride{}); err2 != nil {
			return fmt.Errorf("failed to auto migrate: %w", err)
		}
	}

	// Ensure text columns use utf8mb4 for Chinese characters
	DB.Exec("ALTER TABLE content_block CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci")
	DB.Exec("ALTER TABLE task CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci")
	DB.Exec("ALTER TABLE milestone CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci")
	DB.Exec("ALTER TABLE project CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci")

	log.Println("Database connected successfully")
	return nil
}

func InitRedis(cfg *config.RedisConfig) error {
	RDB = redis.NewClient(&redis.Options{
		Addr:     fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
		Password: cfg.Password,
		DB:       cfg.DB,
	})

	ctx := context.Background()
	if err := RDB.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("failed to connect redis: %w", err)
	}

	log.Println("Redis connected successfully")
	return nil
}