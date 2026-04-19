package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server   ServerConfig    `yaml:"server"`
	Database DatabaseConfig   `yaml:"database"`
	Redis    RedisConfig      `yaml:"redis"`
	Git      GitConfig        `yaml:"git"`
	OpenCode OpenCodeConfig   `yaml:"opencode"`
}

type ServerConfig struct {
	Port int    `yaml:"port"`
	Mode string `yaml:"mode"`
}

type DatabaseConfig struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
	DBName   string `yaml:"dbname"`
}

type RedisConfig struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	Password string `yaml:"password"`
	DB       int    `yaml:"db"`
	Prefix   string `yaml:"prefix"`
}

type GitConfig struct {
	RepoPath string `yaml:"repo_path"` 
}

type OpenCodeConfig struct {
	ServeURL          string `yaml:"serve_url"`
	ProjectPath       string `yaml:"project_path"`
	DefaultModelProvider string `yaml:"default_model_provider"`
	DefaultModelID    string `yaml:"default_model_id"`
}

func Load(path string) *Config {
	searchPaths := []string{
		path,
		"configs/config.yaml",
		"../configs/config.yaml",
		"../../configs/config.yaml",
	}
	
	for _, p := range searchPaths {
		if p == "" {
			continue
		}
		data, err := os.ReadFile(p)
		if err == nil {
			var cfg Config
			if err := yaml.Unmarshal(data, &cfg); err != nil {
				panic(fmt.Sprintf("failed to parse config file %s: %v", p, err))
			}
			return &cfg
		}
	}
	
	panic("config file not found in search paths")
}