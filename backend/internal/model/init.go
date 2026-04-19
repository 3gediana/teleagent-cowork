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

	if err = DB.AutoMigrate(
		&Project{}, &Agent{}, &ContentBlock{},
		&Milestone{}, &MilestoneArchive{},
		&Task{}, &FileLock{}, &Change{},
	); err != nil {
		return fmt.Errorf("failed to auto migrate: %w", err)
	}

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