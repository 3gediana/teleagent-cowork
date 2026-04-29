package config

import (
	"fmt"
	"log"
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

// Validate sanity-checks the loaded config and returns the first
// problem. Run immediately after Load() in main(); fail-fast on
// error. Catches typos (port=0, empty host, blank DataDir) that
// would otherwise crash much later with an opaque message.
// Optional fields (LLM creds, opencode URL, git repo) are NOT
// validated here — they get checked at use-time with surfaceable
// errors. The point of this is to catch wiring mistakes at boot.
func (c *Config) Validate() error {
	if c == nil {
		return fmt.Errorf("config is nil (Load returned no config?)")
	}
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		return fmt.Errorf("server.port=%d out of range [1, 65535]", c.Server.Port)
	}
	if c.Server.Mode == "" {
		return fmt.Errorf("server.mode is empty (expected 'release', 'test', or 'debug')")
	}
	if c.Database.Host == "" {
		return fmt.Errorf("database.host is empty")
	}
	if c.Database.DBName == "" {
		return fmt.Errorf("database.dbname is empty")
	}
	if c.Database.Port < 1 || c.Database.Port > 65535 {
		return fmt.Errorf("database.port=%d out of range [1, 65535]", c.Database.Port)
	}
	if c.Redis.Host == "" {
		return fmt.Errorf("redis.host is empty")
	}
	if c.Redis.Port < 1 || c.Redis.Port > 65535 {
		return fmt.Errorf("redis.port=%d out of range [1, 65535]", c.Redis.Port)
	}
	if c.DataDir == "" {
		return fmt.Errorf("data_dir is empty (must point to a writable directory)")
	}
	return nil
}

// recognisedEnvVars lists every A3C_/OPENCODE_ env var the backend
// actually reads. Kept in code so LogEffective can iterate it and
// operators see at boot what knobs exist without grepping source.
// Add a row when you add a new os.Getenv call. (Test-only flags
// like A3C_RUN_E2E_TESTS are intentionally omitted from the boot
// log to keep production output focused.)
var recognisedEnvVars = []struct {
	Name string
	Help string
}{
	{"A3C_AUTOPILOT", "1/true/yes/on enables agent pool + auto-audit (default off)"},
	{"A3C_OPENCODE_CMD", "override path to opencode binary for pool spawner"},
	{"A3C_OPENCODE_ARGS", "extra args for spawned opencode subprocesses"},
	{"A3C_EMBEDDER_URL", "bge-base-zh-v1.5 sidecar URL (default http://127.0.0.1:8081)"},
	{"A3C_RETENTION_DAYS", "tool_call_trace / dialogue_message retention in days (default 90)"},
}

// redactCreds returns "(set)" for non-empty values and "(empty)" for
// blanks, hiding the actual secret. Used by LogEffective so we can
// confirm at boot which providers have credentials loaded WITHOUT
// echoing them to disk / journald / wherever stdout goes.
func redactCreds(v string) string {
	if v == "" {
		return "(empty)"
	}
	return "(set)"
}

// LogEffective writes the loaded config + every recognised env var
// to the standard logger, with secrets redacted. Call from main()
// right after Validate() so operators see exactly what booted —
// the most common confusion is "which provider creds are loaded
// but blank?", which this answers in one log line.
func (c *Config) LogEffective() {
	if c == nil {
		return
	}
	log.Printf("[Config] server: port=%d mode=%s", c.Server.Port, c.Server.Mode)
	log.Printf("[Config] database: host=%s port=%d user=%s dbname=%s password=%s",
		c.Database.Host, c.Database.Port, c.Database.User, c.Database.DBName, redactCreds(c.Database.Password))
	log.Printf("[Config] redis: host=%s port=%d db=%d prefix=%q password=%s",
		c.Redis.Host, c.Redis.Port, c.Redis.DB, c.Redis.Prefix, redactCreds(c.Redis.Password))
	log.Printf("[Config] data_dir: %s", c.DataDir)
	if c.Git.RepoPath != "" {
		log.Printf("[Config] git.repo_path: %s", c.Git.RepoPath)
	}
	log.Printf("[Config] opencode: serve_url=%s default_provider=%s default_model=%s",
		c.OpenCode.ServeURL, c.OpenCode.DefaultModelProvider, c.OpenCode.DefaultModelID)

	// LLM provider bootstrap creds. Steady-state these come from the
	// llm_endpoint DB table (operator registers via the dashboard);
	// these YAML slots only matter for the offline smoke harness and
	// for first-boot seeding. Surface the load state regardless so
	// operators can see "minimax key set, openai blank" at a glance.
	log.Printf("[Config] llm.minimax: api_key=%s base_url=%s",
		redactCreds(c.LLM.MiniMax.APIKey), c.LLM.MiniMax.BaseURL)
	log.Printf("[Config] llm.openai: api_key=%s base_url=%s",
		redactCreds(c.LLM.OpenAI.APIKey), c.LLM.OpenAI.BaseURL)
	log.Printf("[Config] llm.anthropic: api_key=%s base_url=%s",
		redactCreds(c.LLM.Anthropic.APIKey), c.LLM.Anthropic.BaseURL)
	log.Printf("[Config] llm.deepseek: api_key=%s base_url=%s",
		redactCreds(c.LLM.DeepSeek.APIKey), c.LLM.DeepSeek.BaseURL)

	// Recognised env vars: show "set" / "unset" + a one-line help.
	// Don't dump the value (could contain a token / path with PII).
	log.Printf("[Config] env vars (set/unset):")
	for _, e := range recognisedEnvVars {
		v := strings.TrimSpace(os.Getenv(e.Name))
		state := "unset"
		if v != "" {
			state = "set"
			// Truthy/numeric flags are safe to display verbatim, so
			// operators can confirm "did A3C_AUTOPILOT actually take?".
			// Anything that LOOKS like a secret stays redacted.
			if isNonSecretValue(e.Name, v) {
				state = "set=" + v
			}
		}
		log.Printf("[Config]   %s [%s] — %s", e.Name, state, e.Help)
	}
}

// isNonSecretValue reports whether the env var's literal value is
// safe to log. Names containing KEY/TOKEN/PASSWORD/SECRET are always
// redacted; the rest are safe (boolean toggles, paths, URLs without
// embedded creds). Conservative: anything we're unsure about stays
// hidden.
func isNonSecretValue(name, value string) bool {
	upper := strings.ToUpper(name)
	for _, marker := range []string{"KEY", "TOKEN", "PASSWORD", "SECRET", "CRED"} {
		if strings.Contains(upper, marker) {
			return false
		}
	}
	// URLs with embedded basic-auth: redact if "user:pass@" pattern is
	// in there. (Operators occasionally inline creds into A3C_EMBEDDER_URL.)
	if strings.Contains(value, "@") && (strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://")) {
		// crude heuristic — if there's a colon before the @ in the
		// authority section, treat as basic-auth and redact.
		schemeEnd := strings.Index(value, "://")
		if schemeEnd > 0 {
			rest := value[schemeEnd+3:]
			at := strings.Index(rest, "@")
			if at > 0 && strings.Contains(rest[:at], ":") {
				return false
			}
		}
	}
	return true
}
