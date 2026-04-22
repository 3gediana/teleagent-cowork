// Package agentpool manages platform-hosted client agents.
//
// The user asked for a mode where the platform itself can spawn,
// inject skills into, and orchestrate client agents on the same
// machine it runs on. Externally-registered agents (people running
// Claude Code / Codex from their own machines) keep working exactly
// as before — platform-hosted agents are an additional path, not a
// replacement.
//
// Lifecycle:
//
//	Spawn(role, project):
//	  1. Pick an unused high port.
//	  2. Create a working directory per agent: <root>/pool/<instance-id>/
//	  3. Write a minimal opencode.json + materialise active skills.
//	  4. Register the agent row in the DB with IsPlatformHosted=true
//	     so authentication, SSE, dashboard all treat it like any
//	     other client.
//	  5. Start the opencode subprocess (or the user-configured
//	     equivalent via ManagerConfig.Command).
//	  6. Wait for /global/health via the opencode.Client.
//	  7. Return Instance{} with PID, port, agent_id, access_key.
//
//	Shutdown(instance_id):
//	  1. Send TERM; wait up to ShutdownGrace for clean exit.
//	  2. Fall back to KILL.
//	  3. Mark agent offline. Delete DB row (pool rows are ephemeral).
//	  4. Optionally clean up the working dir. (Default: keep for
//	     post-mortem; operator deletes.)
//
//	Heartbeat:
//	  Monitor exit code; if the subprocess dies unexpectedly, flip
//	  status=offline and emit a POOL_AGENT_CRASHED event. Do NOT
//	  auto-respawn — the pool is a supervised primitive, not a
//	  restart loop (that's a policy knob for the caller).
//
// Design notes
//
//   - The spawner is abstracted behind the `Spawner` interface so
//     tests can swap in a fake without shelling out. Production uses
//     an OS-exec spawner; tests use a fake that returns a canned
//     instance.
//
//   - Skills are materialised as files on disk rather than injected
//     via MCP calls. opencode reads skills from its config directory
//     at startup; the cleanest way to "inject" is to write files
//     before the subprocess boots. Each skill becomes a subfolder
//     with a SKILL.md; the list is pulled from model.SkillCandidate
//     where status='active', so the same lifecycle humans see in
//     ChiefPage drives what auto-hosted agents get.
//
//   - Access keys for pool agents are generated, stored on the Agent
//     row, and passed via env var (A3C_ACCESS_KEY). The subprocess
//     uses them to authenticate the MCP bridge back to the platform.
package agentpool

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"sync"
	"time"

	"github.com/a3c/platform/internal/agent"
	"github.com/a3c/platform/internal/model"
)

// Store abstracts the DB writes the pool needs. Production uses the
// GORM-backed `gormStore` (var `DefaultStore`); tests pass a
// MemStore to avoid spinning up MySQL.
//
// Scope is intentionally tiny — just what's needed to keep Agent
// rows in sync with pool lifecycle. Anything richer goes through
// model.DB directly from the caller.
type Store interface {
	CreateAgent(a *model.Agent) error
	UpdateAgent(id string, updates map[string]any) error
	DeleteAgent(id string) error
}

// gormStore is the production implementation. Simply delegates to
// model.DB. Separate type (vs inlining in Manager) so the test fake
// can satisfy the same interface.
type gormStore struct{}

func (gormStore) CreateAgent(a *model.Agent) error {
	return model.DB.Create(a).Error
}
func (gormStore) UpdateAgent(id string, updates map[string]any) error {
	return model.DB.Model(&model.Agent{}).Where("id = ?", id).Updates(updates).Error
}
func (gormStore) DeleteAgent(id string) error {
	return model.DB.Delete(&model.Agent{}, "id = ?", id).Error
}

// DefaultStore is the zero-dependency production store — what
// cmd/server wires in. Exported so alternative mains can swap it.
var DefaultStore Store = gormStore{}

// Instance is the externally visible pool record — enough for a
// caller to identify / act on an instance without reaching into the
// manager's internals. Anything more detailed is private to the pkg.
type Instance struct {
	ID              string    `json:"id"`
	AgentID         string    `json:"agent_id"`
	AgentName       string    `json:"agent_name"`
	Role            string    `json:"role"` // advisory hint; client agents pick tasks, don't get assigned a role
	ProjectID       string    `json:"project_id"`
	Port            int       `json:"port"`
	PID             int       `json:"pid"`
	Status          string    `json:"status"` // starting | ready | crashed | stopping | stopped
	StartedAt       time.Time `json:"started_at"`
	SkillsInjected  []string  `json:"skills_injected"`
	WorkingDir      string    `json:"working_dir"`
	LastError       string    `json:"last_error,omitempty"`
}

// ManagerConfig is the runtime knobs. Zero values are sensible, but
// production should set Root explicitly to put pool state on the
// same volume as the rest of the platform data.
type ManagerConfig struct {
	// Root is the filesystem location for per-instance working
	// directories. Defaults to "./data/pool" relative to cwd.
	Root string

	// Command + Args spawn the client subprocess. The env will be
	// extended with A3C_PLATFORM_URL, A3C_ACCESS_KEY, A3C_PROJECT_ID.
	// Empty Command uses the built-in opencode-serve spawner. Allows
	// operators to substitute e.g. their own wrapper script.
	Command string
	Args    []string

	// PortRange the pool picks from when assigning a subprocess port.
	// Defaults to 47000-47999.
	PortMin int
	PortMax int

	// StartupTimeout is how long to wait for /global/health to go
	// green before declaring the instance failed. Default 30s.
	StartupTimeout time.Duration

	// ShutdownGrace is how long to wait after TERM before KILLing.
	// Default 10s.
	ShutdownGrace time.Duration

	// PlatformURL is the base URL the spawned agent should call to
	// reach this platform. Defaults to http://localhost:8080 — good
	// for the same-machine case. Override in Docker deployments.
	PlatformURL string
}

// ApplyDefaults fills in zeroes with sensible runtime values. Kept
// separate from New() so callers that accept a ManagerConfig from
// outside can normalise it without going through the constructor.
func (c *ManagerConfig) ApplyDefaults() {
	if c.Root == "" {
		c.Root = filepath.Join("data", "pool")
	}
	if c.PortMin == 0 {
		c.PortMin = 47000
	}
	if c.PortMax == 0 {
		c.PortMax = 47999
	}
	if c.StartupTimeout == 0 {
		c.StartupTimeout = 30 * time.Second
	}
	if c.ShutdownGrace == 0 {
		c.ShutdownGrace = 10 * time.Second
	}
	if c.PlatformURL == "" {
		c.PlatformURL = "http://localhost:8080"
	}
}

// Manager owns the running pool. Methods are goroutine-safe.
//
// Thread-safety discipline: mu guards `instances` only. Subprocess
// operations (spawn, wait, kill) run without the lock held; the
// instance struct they mutate is owned by the goroutine that owns
// the subprocess, so no double-locking required.
type Manager struct {
	cfg     ManagerConfig
	spawner Spawner
	store   Store

	mu        sync.Mutex
	instances map[string]*subprocess

	// lastPort tracks the next port to try — reused from one Spawn
	// to the next so we don't scan the whole range every time.
	lastPort int
}

// NewManager builds a Manager with the given config. Spawner may be
// nil, in which case the default OS-exec spawner is used. Tests pass
// a FakeSpawner to avoid actually starting subprocesses.
//
// Store defaults to DefaultStore (GORM). Tests should use WithStore
// to inject a MemStore.
func NewManager(cfg ManagerConfig, spawner Spawner) *Manager {
	cfg.ApplyDefaults()
	if spawner == nil {
		spawner = &execSpawner{}
	}
	return &Manager{
		cfg:       cfg,
		spawner:   spawner,
		store:     DefaultStore,
		instances: map[string]*subprocess{},
		lastPort:  cfg.PortMin - 1,
	}
}

// WithStore replaces the Store used for Agent-row bookkeeping. Meant
// for tests; returns the manager for chaining.
func (m *Manager) WithStore(s Store) *Manager {
	m.store = s
	return m
}

// subprocess is the private per-instance state. Wraps the public
// Instance with the cmd handle and the cancellation channel that
// shuts down the health-watch goroutine.
type subprocess struct {
	inst     Instance
	handle   SpawnerHandle
	stopChan chan struct{}
}

// SpawnRequest bundles the per-spawn inputs: which project the agent
// joins, optional role hint, an optional display name.
type SpawnRequest struct {
	ProjectID string
	RoleHint  agent.Role // cosmetic; the platform's task queue is what actually drives work
	Name      string     // optional display name; auto-generated if empty
}

// Spawn brings up a new platform-hosted agent. Synchronous: returns
// only after the subprocess is either healthy or has failed.
func (m *Manager) Spawn(ctx context.Context, req SpawnRequest) (*Instance, error) {
	// 1. Pick a port.
	port, err := m.pickPort()
	if err != nil {
		return nil, fmt.Errorf("pool: pick port: %w", err)
	}

	// 2. Generate identifiers.
	instanceID := model.GenerateID("pool")
	agentID := model.GenerateID("agent")
	accessKey := model.GenerateKey()
	name := req.Name
	if name == "" {
		name = fmt.Sprintf("platform-%s", instanceID[len("pool_"):])
	}

	workDir := filepath.Join(m.cfg.Root, instanceID)

	// 3. Materialise skills on disk so the spawned opencode picks
	// them up at startup. Skills are read from the SkillCandidate
	// table where status='active' — the same lifecycle humans see
	// in ChiefPage > Skills.
	skills, err := materialiseSkills(workDir)
	if err != nil {
		return nil, fmt.Errorf("pool: write skills: %w", err)
	}

	// 4. Register the agent row. MUST happen before we start the
	// subprocess so the subprocess's auth calls succeed immediately.
	dbAgent := model.Agent{
		ID:               agentID,
		Name:             name,
		AccessKey:        accessKey,
		Status:           "offline", // flipped to online once health check passes
		IsHuman:          false,
		IsPlatformHosted: true,
		PoolInstanceID:   instanceID,
	}
	if req.ProjectID != "" {
		dbAgent.CurrentProjectID = &req.ProjectID
	}
	if err := m.store.CreateAgent(&dbAgent); err != nil {
		return nil, fmt.Errorf("pool: register agent row: %w", err)
	}

	// 5. Start the subprocess. Env vars wire authentication + URL.
	spawnReq := SpawnerRequest{
		WorkingDir: workDir,
		Port:       port,
		Env: map[string]string{
			"A3C_PLATFORM_URL": m.cfg.PlatformURL,
			"A3C_ACCESS_KEY":   accessKey,
			"A3C_PROJECT_ID":   req.ProjectID,
			"A3C_AGENT_ID":     agentID,
			"A3C_INSTANCE_ID":  instanceID,
		},
		Command: m.cfg.Command,
		Args:    m.cfg.Args,
	}
	handle, err := m.spawner.Spawn(ctx, spawnReq)
	if err != nil {
		// Best-effort cleanup: drop the orphan agent row.
		_ = m.store.DeleteAgent(agentID)
		return nil, fmt.Errorf("pool: spawn subprocess: %w", err)
	}

	inst := Instance{
		ID:             instanceID,
		AgentID:        agentID,
		AgentName:      name,
		Role:           string(req.RoleHint),
		ProjectID:      req.ProjectID,
		Port:           port,
		PID:            handle.PID(),
		Status:         "starting",
		StartedAt:      time.Now(),
		SkillsInjected: skills,
		WorkingDir:     workDir,
	}
	sp := &subprocess{inst: inst, handle: handle, stopChan: make(chan struct{})}

	m.mu.Lock()
	m.instances[instanceID] = sp
	m.mu.Unlock()

	// 6. Wait for health.
	healthy := handle.WaitHealthy(ctx, m.cfg.StartupTimeout)
	if !healthy {
		log.Printf("[Pool] instance %s failed to go healthy in %s; tearing down", instanceID, m.cfg.StartupTimeout)
		_ = m.shutdownLocked(instanceID, fmt.Errorf("health check failed"))
		return nil, fmt.Errorf("pool: instance %s never became healthy", instanceID)
	}

	// 7. Flip DB + in-memory state to ready.
	now := time.Now()
	_ = m.store.UpdateAgent(agentID, map[string]any{
		"status":         "online",
		"last_heartbeat": &now,
	})
	m.mu.Lock()
	sp.inst.Status = "ready"
	m.mu.Unlock()

	// 8. Background watcher — flips status to "crashed" if the
	// subprocess exits on its own.
	go m.watch(sp)

	return &sp.inst, nil
}

// watch fires when the subprocess exits. The goroutine runs for the
// lifetime of the subprocess; clean shutdown paths also route through
// here via stopChan.
func (m *Manager) watch(sp *subprocess) {
	exitCh := sp.handle.Wait()
	select {
	case code := <-exitCh:
		m.mu.Lock()
		// If the instance is already marked "stopped"/"stopping", a
		// planned shutdown ate the exit — nothing to report.
		if sp.inst.Status != "stopping" && sp.inst.Status != "stopped" {
			sp.inst.Status = "crashed"
			sp.inst.LastError = fmt.Sprintf("exited with code %d", code)
			log.Printf("[Pool] instance %s crashed: %s", sp.inst.ID, sp.inst.LastError)
		}
		m.mu.Unlock()
		// Mark the agent offline so task queue / dashboard reflect.
		_ = m.store.UpdateAgent(sp.inst.AgentID, map[string]any{"status": "offline"})
	case <-sp.stopChan:
		// Explicit shutdown path handled elsewhere.
	}
}

// Shutdown stops a pool instance. Idempotent — calling twice is a
// no-op. Returns after the subprocess is gone or we've given up.
func (m *Manager) Shutdown(instanceID string) error {
	return m.shutdownLocked(instanceID, nil)
}

func (m *Manager) shutdownLocked(instanceID string, cause error) error {
	m.mu.Lock()
	sp, ok := m.instances[instanceID]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("pool: instance %s not found", instanceID)
	}
	if sp.inst.Status == "stopped" || sp.inst.Status == "stopping" {
		m.mu.Unlock()
		return nil // already going / gone
	}
	sp.inst.Status = "stopping"
	m.mu.Unlock()

	// Tear down outside the lock.
	close(sp.stopChan)
	sp.handle.Terminate(m.cfg.ShutdownGrace)

	// Flip DB state.
	now := time.Now()
	_ = m.store.UpdateAgent(sp.inst.AgentID, map[string]any{
		"status":         "offline",
		"last_heartbeat": &now,
	})

	m.mu.Lock()
	sp.inst.Status = "stopped"
	if cause != nil {
		sp.inst.LastError = cause.Error()
	}
	m.mu.Unlock()
	return nil
}

// List returns a snapshot copy of every known instance. Callers MUST
// NOT mutate the returned slice; it's safe to read concurrently.
func (m *Manager) List() []Instance {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Instance, 0, len(m.instances))
	for _, sp := range m.instances {
		out = append(out, sp.inst)
	}
	return out
}

// Get returns a single instance by id. Zero-value Instance + false
// when the id is unknown.
func (m *Manager) Get(id string) (Instance, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sp, ok := m.instances[id]
	if !ok {
		return Instance{}, false
	}
	return sp.inst, true
}

// Purge removes a stopped/crashed instance record from memory. Does
// nothing if the instance is still running — caller must Shutdown
// first.
func (m *Manager) Purge(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	sp, ok := m.instances[id]
	if !ok {
		return fmt.Errorf("pool: instance %s not found", id)
	}
	if sp.inst.Status != "stopped" && sp.inst.Status != "crashed" {
		return fmt.Errorf("pool: instance %s is %s — shut down first", id, sp.inst.Status)
	}
	delete(m.instances, id)
	// Also drop the orphan agent row so the dashboard stops showing it.
	_ = m.store.DeleteAgent(sp.inst.AgentID)
	return nil
}

// ShutdownAll stops every instance — useful on server shutdown so we
// don't leak subprocesses.
func (m *Manager) ShutdownAll() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.instances))
	for id := range m.instances {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		_ = m.Shutdown(id)
	}
}

// pickPort finds the next unused port in the configured range. Cheap:
// the pool is small (single-digit instances typical).
func (m *Manager) pickPort() (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	used := map[int]bool{}
	for _, sp := range m.instances {
		used[sp.inst.Port] = true
	}
	for i := 0; i < (m.cfg.PortMax - m.cfg.PortMin + 1); i++ {
		candidate := m.lastPort + 1
		if candidate > m.cfg.PortMax {
			candidate = m.cfg.PortMin
		}
		m.lastPort = candidate
		if !used[candidate] {
			return candidate, nil
		}
	}
	return 0, fmt.Errorf("no free ports in [%d,%d]", m.cfg.PortMin, m.cfg.PortMax)
}

// Default is the process-wide manager, wired by cmd/server. Handlers
// use it via GetDefault() so tests can inject their own manager.
var (
	defaultMu      sync.RWMutex
	defaultManager *Manager
)

// SetDefault registers the manager used by HTTP handlers. Called from
// cmd/server once config is loaded.
func SetDefault(m *Manager) {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	defaultManager = m
}

// GetDefault returns the registered manager. nil = not yet wired;
// handlers should 503 in that case.
func GetDefault() *Manager {
	defaultMu.RLock()
	defer defaultMu.RUnlock()
	return defaultManager
}
