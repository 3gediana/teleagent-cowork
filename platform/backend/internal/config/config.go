package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server   ServerConfig   `yaml:"server"`
	Database DatabaseConfig `yaml:"database"`
	Redis    RedisConfig    `yaml:"redis"`
	Git      GitConfig      `yaml:"git"`
	OpenCode OpenCodeConfig `yaml:"opencode"`
	// LLM is the native-runtime provider-credential bag. Each subkey
	// corresponds to one provider ever seen in production. Adding a
	// key here is a bootstrap convenience — the same credentials
	// normally live in the llm_endpoint DB table (registered via the
	// dashboard). The config slots are used by:
	//   * cmd/nativesmoke-real: real end-to-end smoke test against the
	//     provider, so we verify the runtime without a dashboard UI.
	//   * First-boot bootstrap on a fresh DB (future work).
	LLM     LLMConfig `yaml:"llm"`
	DataDir string    `yaml:"data_dir"`
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
	ServeURL             string `yaml:"serve_url"`
	ProjectPath          string `yaml:"project_path"`
	DefaultModelProvider string `yaml:"default_model_provider"`
	DefaultModelID       string `yaml:"default_model_id"`
}

// LLMConfig holds bootstrap credentials for providers the native
// runtime targets. In steady-state these live in the llm_endpoint
// table and are edited via the dashboard; the config.yaml slots are
// for the offline smoke-test harness and for first-boot seeding.
//
// Never hard-code keys in source — they belong in config.yaml (which
// the repo .gitignore excludes) or in the environment. The OpenAI
// and Anthropic struct shapes mirror MiniMax so adding a new
// OpenAI-compatible provider is a one-line YAML change + a one-line
// Go struct add here.
type LLMConfig struct {
	MiniMax   ProviderCreds `yaml:"minimax"`
	OpenAI    ProviderCreds `yaml:"openai"`
	Anthropic ProviderCreds `yaml:"anthropic"`
	DeepSeek  ProviderCreds `yaml:"deepseek"`
}

// ProviderCreds is the shared shape for any LLM provider's bootstrap
// entry. Leaving APIKey empty is legitimate — downstream code that
// requires it must surface a clear error.
type ProviderCreds struct {
	APIKey   string `yaml:"api_key"`
	BaseURL  string `yaml:"base_url,omitempty"`
	Model    string `yaml:"model,omitempty"` // optional default model id
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
			// Resolve data_dir relative to config file if not absolute
			if cfg.DataDir != "" && !filepath.IsAbs(cfg.DataDir) {
				cfg.DataDir = filepath.Join(filepath.Dir(p), "..", cfg.DataDir)
			}
			return &cfg
		}
	}

	panic("config file not found in search paths")
}
