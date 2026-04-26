package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// IsAutopilotEnabled reports whether the platform should run in
// "autopilot" mode (server spawns its own LLM agent pool, runs
// dispatcher, and auto-runs audit/fix workflows on every change
// submission).
//
// The default is OFF. The platform's primary value proposition is
// remote multi-developer + AI collaboration — a shared project /
// task / change / review hub where each developer's local AI is
// their own tool, not a server-spawned subprocess. Run B5
// benchmarking confirmed that the auto-pool path adds protocol
// overhead without beating the solo-agent baseline (Run A: 22 min
// for 15 tasks; Run B5: stalled at 7/15 in 14 min). The autopilot
// mode is preserved for legacy demos and for users who explicitly
// want server-managed agents.
//
// Recognised truthy values: "1", "true", "yes", "on" (case-insensitive).
// Anything else (including unset) is false.
func IsAutopilotEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("A3C_AUTOPILOT")))
	switch v {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

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
	//   * experiments/nativesmokereal: real end-to-end smoke test
	//     against the provider, so we verify the runtime without a
	//     dashboard UI.
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

// loaded holds the most recently parsed config. Set by Load on success
// and retrieved via Get() by packages that need a stable reference to
// the active config without re-parsing the YAML (e.g. HTTP handlers
// that can't accept a *Config in their constructor). Never mutate
// through the returned pointer — treat it as read-only.
var loaded *Config

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
			loaded = &cfg
			return loaded
		}
	}

	panic("config file not found in search paths")
}

// Get returns the process-wide Config last loaded by Load. Returns nil
// if Load has never been called successfully (e.g. in unit tests that
// don't bootstrap through main.go). Callers must nil-check.
func Get() *Config {
	return loaded
}
