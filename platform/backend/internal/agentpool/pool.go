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
	// GetAgent fetches the full Agent row. Used by Wake to pull
	// the access_key back for the re-spawned subprocess (agents
	// keep their identity across dormancy). Returns (nil, nil) on
	// a miss so callers can branch without string-matching error
	// text — every caller so far treats "not found" as a benign
	// "instance was purged mid-flight".
	GetAgent(id string) (*model.Agent, error)
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
func (gormStore) GetAgent(id string) (*model.Agent, error) {
	var a model.Agent
	if err := model.DB.First(&a, "id = ?", id).Error; err != nil {
		// "not found" is not an infrastructure error; surface
		// (nil, nil) and let the caller decide what to do.
		if isRecordNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return &a, nil
}

// isRecordNotFound keeps gorm out of the interface boundary —
// dormancy.go only sees (nil, nil) for missing rows.
func isRecordNotFound(err error) bool {
	return err != nil && err.Error() == "record not found"
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

	// OpencodeProviderID + OpencodeModelID name the provider/model
	// this agent uses on the opencode side (e.g. "minimax-coding-plan"
	// / "MiniMax-M2.7"). These are the ids opencode knows about from
	// its own config; the platform's LLMEndpoint table is a parallel
	// concept we don't auto-sync yet. Empty = the opencode serve has
	// to pick a default, which in practice means zero assistant
	// replies (opencode refuses to route without a model) — so
	// callers should always supply these on Spawn.
	OpencodeProviderID string `json:"opencode_provider_id,omitempty"`
	OpencodeModelID    string `json:"opencode_model_id,omitempty"`

	// OpencodeSessionID is the id of the long-running opencode
	// session this pool agent is currently attached to. Created
	// during Spawn (right after the serve goes healthy) and
	// rotated by the archive loop once the context nears its cap.
	// Empty while Status="starting" or if session creation failed.
	OpencodeSessionID string `json:"opencode_session_id,omitempty"`

	// ArchiveRotation counts how many times the session has been
	// rotated for this agent. 0 = still on the initial session. The
	// counter is folded into the title we give each replacement so
	// opencode's session list stays legible to operators reviewing
	// history.
	ArchiveRotation int `json:"archive_rotation"`

	// LastContextTokens is the most recent ContextSize reading the
	// watcher observed on this agent's session. Surfaced so the
	// dashboard can draw a "how full is the context" gauge without
	// re-probing opencode from the browser. Zero until the first
	// watch cycle.
	LastContextTokens int `json:"last_context_tokens"`

	// LastActivityAt is the wall-clock time of the last time this
	// agent *did* something — broadcast injection, context-probe
	// reading new tokens, or manual operator interaction. Drives
	// the idle→dormant transition (see internal/agentpool/
	// dormancy.go). Zero value means "never active yet", which is
	// treated as "just spawned" — the spawn handler stamps it on
	// ready transition so a fresh agent isn't immediately eligible
	// for dormancy.
	LastActivityAt time.Time `json:"last_activity_at,omitempty"`

	// DormantAt is the wall-clock time the agent most recently
	// entered the dormant state. Useful for the dashboard's "asleep
	// for 12m" copy and for debugging "why did this agent not pick
	// up the broadcast I just sent" (the broadcast consumer skips
	// dormant instances — they have to wake first).
	DormantAt time.Time `json:"dormant_at,omitempty"`
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
	// Defaults to 5500-5599. Earlier defaults sat at 4097-4199 "just
	// above opencode's 4096", but Windows reserves 4091-4290 for
	// Hyper-V / WSL dynamic port allocations — any opencode serve
	// we spawn there dies immediately with "Failed to start server
	// on port NNNN". 5500-5599 sits below the ephemeral range and
	// well away from known reservations on both Windows and Linux.
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

	// SkipOpencodeEnvPrep disables the automatic `.opencode/`
	// template hydration that guards against the zod v4 crash.
	// Set this in tests (FakeSpawner never actually runs opencode)
	// and in deployments where operators hand-manage the template.
	SkipOpencodeEnvPrep bool

	// ContextWatchInterval is the period between context-size polls
	// the watcher runs for each ready agent. Zero disables the
	// watcher entirely — useful for tests that want deterministic
	// session state. Default 30s in production (see ApplyDefaults).
	ContextWatchInterval time.Duration

	// ArchiveThresholdTokens is the context size at which the
	// watcher rotates an agent's opencode session. We trigger on
	// input + cache.read tokens specifically (the part that gets
	// replayed on every subsequent turn). Default 150_000 — well
	// below opencode's own 80% auto-compact line so we always
	// archive *before* opencode silently summarises the transcript.
	ArchiveThresholdTokens int

	// IdleTimeout is how long a ready agent must go without any
	// inject / probe / operator activity before the dormancy
	// detector tears its opencode serve down. Session id is
	// preserved on the Instance so wake can rebind; a "stopping
	// point" archive session is created just before termination so
	// the transcript up to dormancy is permanently addressable.
	// Zero disables the detector entirely. Default 30m.
	IdleTimeout time.Duration

	// DormancyCheckInterval is how often the detector scans for
	// eligible idle instances. Much larger than the activity
	// resolution we actually need (agents sit in a conversation
	// for minutes at a time). Zero defaults to IdleTimeout/6 but
	// capped at 5m.
	DormancyCheckInterval time.Duration
}

// ApplyDefaults fills in zeroes with sensible runtime values. Kept
// separate from New() so callers that accept a ManagerConfig from
// outside can normalise it without going through the constructor.
func (c *ManagerConfig) ApplyDefaults() {
	if c.Root == "" {
		c.Root = filepath.Join("data", "pool")
	}
	if c.PortMin == 0 {
		c.PortMin = 5500
	}
	if c.PortMax == 0 {
		c.PortMax = 5599
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
	if c.ArchiveThresholdTokens == 0 {
		c.ArchiveThresholdTokens = 150_000
	}
	if c.IdleTimeout > 0 && c.DormancyCheckInterval == 0 {
		// Scale with IdleTimeout so shorter timeouts still get
		// snappy detection, capped at 5m so an operator-set 24h
		// timeout doesn't end up with a 4h probe lag.
		c.DormancyCheckInterval = c.IdleTimeout / 6
		if c.DormancyCheckInterval > 5*time.Minute {
			c.DormancyCheckInterval = 5 * time.Minute
		}
	}
	// ContextWatchInterval=0 and IdleTimeout=0 are both legitimate
	// "off" signals; don't overwrite them. Respective Start* paths
	// treat zero as "don't run" explicitly. main.go is where the
	// operator's default 30m lives.
}

// SessionCreator abstracts "build a fresh opencode session on the
// agent's serve". Production wires in an adapter around
// opencode.Client; tests substitute a fake that returns a canned id.
//
// Kept as an interface (not a direct dependency on opencode.Client)
// so agentpool stays free of the opencode HTTP package — otherwise
// pool_test.go would need a live opencode to unit-test anything.
type SessionCreator interface {
	// CreateInitialSession is called once per spawn, after the
	// agent's opencode serve has passed /global/health. Returns the
	// session id to persist on the Agent row, or an error. An error
	// is non-fatal for the spawn (agent still becomes "ready"), it
	// just means no session is bound yet.
	CreateInitialSession(ctx context.Context, serveURL, agentName string) (string, error)

	// CreateArchiveSession is called by the context watcher when an
	// agent's current session crosses the archive threshold. The
	// implementation picks a title that distinguishes it from the
	// previous session ("pool:name#2" etc.) so operators browsing
	// opencode's session list can spot rotations. Returns the new
	// session id to bind the agent to.
	CreateArchiveSession(ctx context.Context, serveURL, agentName string, rotation int) (string, error)
}

// ContextProbe reads the current token footprint of an opencode
// session. Separated from SessionCreator because some test
// scenarios only exercise the probe side (no rotation needed).
type ContextProbe interface {
	// ContextSize returns input+cache.read tokens on the latest
	// assistant message in the session, or 0 if none exists yet.
	ContextSize(ctx context.Context, serveURL, sessionID string) (int, error)
}

// ArchiveNotifier pushes a "your session just rotated" signal out
// to the MCP poller running inside the pool agent's subprocess.
// The MCP will see the new session id on its next broadcast poll
// and swap its cached lock. Kept as an interface so tests can
// observe what would have been broadcast without touching Redis.
type ArchiveNotifier interface {
	NotifyArchive(agentID string, oldSessionID, newSessionID string, tokens int, reason string)
}

// Manager owns the running pool. Methods are goroutine-safe.
//
// Thread-safety discipline: mu guards `instances` only. Subprocess
// operations (spawn, wait, kill) run without the lock held; the
// instance struct they mutate is owned by the goroutine that owns
// the subprocess, so no double-locking required.
type Manager struct {
	cfg               ManagerConfig
	spawner           Spawner
	store             Store
	sessionCreator    SessionCreator
	contextProbe      ContextProbe
	archiveNotifier   ArchiveNotifier
	broadcastConsumer BroadcastConsumer
	broadcastInjector BroadcastInjector

	mu        sync.Mutex
	instances map[string]*subprocess

	// lastPort tracks the next port to try — reused from one Spawn
	// to the next so we don't scan the whole range every time.
	lastPort int

	// watchStop, once non-nil, signals the context-watch goroutine
	// to exit. Set by StartContextWatcher / cleared by Stop.
	watchStop chan struct{}

	// broadcastStop is the same story for the directed-broadcast
	// consumer loop. Both are kept nil until the respective
	// Start*() method is called so a Manager built without any
	// background goroutines stays purely synchronous (tests).
	broadcastStop chan struct{}

	// dormancyStop gates the idle→dormant detector goroutine
	// (StartDormancyDetector). Same story as the other two: nil
	// until started, closed on shutdown / replaced on re-start.
	dormancyStop chan struct{}

	// metrics is the per-instance ring-buffer store driving the
	// dashboard's token sparkline + lifecycle event log. See
	// metrics.go. Populated lazily on first write so Manager
	// startup cost stays flat.
	metrics map[string]*instanceMetrics
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
		metrics:   map[string]*instanceMetrics{},
		lastPort:  cfg.PortMin - 1,
	}
}

// WithStore replaces the Store used for Agent-row bookkeeping. Meant
// for tests; returns the manager for chaining.
func (m *Manager) WithStore(s Store) *Manager {
	m.store = s
	return m
}

// WithSessionCreator installs the component that builds the initial
// opencode session after a pool agent boots. Production wires in an
// adapter around opencode.Client (see cmd/server/main.go). Tests
// pass a fake, or leave it nil to skip session creation entirely.
func (m *Manager) WithSessionCreator(sc SessionCreator) *Manager {
	m.sessionCreator = sc
	return m
}

// WithContextProbe installs the component that queries an agent's
// current opencode session for its accumulated token footprint.
// Required for the context watcher to function; nil disables the
// watcher even if an interval was configured.
func (m *Manager) WithContextProbe(cp ContextProbe) *Manager {
	m.contextProbe = cp
	return m
}

// WithArchiveNotifier installs the sink the context watcher calls
// after rotating a session. The MCP poller inside the agent reads
// whichever "you are now on session X" message this emits. nil =
// rotations still happen on the server side, but the MCP never
// learns about them (OK in tests that only want to observe state).
func (m *Manager) WithArchiveNotifier(an ArchiveNotifier) *Manager {
	m.archiveNotifier = an
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
// joins, optional role hint, an optional display name, and the
// opencode provider+model pair this agent should use.
type SpawnRequest struct {
	ProjectID string
	RoleHint  agent.Role // cosmetic; the platform's task queue is what actually drives work
	Name      string     // optional display name; auto-generated if empty

	// OpencodeProviderID / OpencodeModelID are the ids this agent
	// tells opencode to route prompts through. Must match an entry
	// in opencode's own config (global ~/.config/opencode/opencode.json
	// or workspace `.opencode/opencode.json`) — the platform does not
	// auto-sync these with LLMEndpoint rows yet.
	//
	// Empty strings are allowed but mean the initial session is
	// created without a model lock, and the watch loop has no model
	// to use for archive prompts. In practice the caller (dashboard)
	// should always pass both.
	OpencodeProviderID string
	OpencodeModelID    string
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

	// 3a. Prime `.opencode/` inside workDir so opencode serve boots
	// with the expected provider npm packages (`@ai-sdk/openai-
	// compatible` + zod v3) and *without* `@opencode-ai/plugin` —
	// the latter pulls in zod v4, which crashes opencode 1.14.21's
	// resolveTools() and causes every prompt to come back with
	// parts=0. See opencode_env.go for the full rationale.
	//
	// Tests and headless deployments can skip this by setting
	// SkipOpencodeEnvPrep, since FakeSpawner never runs opencode.
	if !m.cfg.SkipOpencodeEnvPrep {
		if err := prepareOpencodeDir(workDir, m.cfg.Root); err != nil {
			return nil, fmt.Errorf("pool: prepare .opencode: %w", err)
		}
	}

	// 3b. Materialise skills on disk so the spawned opencode picks
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

	// 5. Start the subprocess. Each pool agent gets its own
	// opencode serve so it has an isolated MCP process with its
	// own A3C_AGENT_ID and A3C_WORK_DIR. This lets file_sync
	// pull project files into the agent's own workspace, and
	// the MCP poller injects broadcasts into that agent's serve.
	//
	// OPENCODE_SERVE_URL points to the agent's own serve so the
	// A3C MCP poller knows where to inject messages. The provider
	// + model vars are what the MCP needs when it creates a fresh
	// opencode session (initial spawn AND archive rotation) —
	// opencode refuses to route prompts without a model, so we
	// push those as env rather than asking MCP to re-derive them.
	serveURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	spawnReq := SpawnerRequest{
		WorkingDir: workDir,
		Port:       port,
		Env: map[string]string{
			"A3C_PLATFORM_URL":          m.cfg.PlatformURL,
			"A3C_ACCESS_KEY":            accessKey,
			"A3C_PROJECT_ID":            req.ProjectID,
			"A3C_AGENT_ID":              agentID,
			"A3C_INSTANCE_ID":           instanceID,
			"A3C_WORK_DIR":              workDir,
			"A3C_OPENCODE_PROVIDER_ID":  req.OpencodeProviderID,
			"A3C_OPENCODE_MODEL_ID":     req.OpencodeModelID,
			"OPENCODE_SERVE_URL":        serveURL,
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

	instPort := port
	inst := Instance{
		ID:                 instanceID,
		AgentID:            agentID,
		AgentName:          name,
		Role:               string(req.RoleHint),
		ProjectID:          req.ProjectID,
		Port:               instPort,
		PID:                handle.PID(),
		Status:             "starting",
		StartedAt:          time.Now(),
		SkillsInjected:     skills,
		WorkingDir:         workDir,
		OpencodeProviderID: req.OpencodeProviderID,
		OpencodeModelID:    req.OpencodeModelID,
	}
	sp := &subprocess{inst: inst, handle: handle, stopChan: make(chan struct{})}

	m.mu.Lock()
	m.instances[instanceID] = sp
	m.mu.Unlock()

	// 6. Wait for health.
	// Virtual agents always report healthy (they share the operator's
	// opencode serve). Subprocess agents wait for their own health.
	healthy := handle.WaitHealthy(ctx, m.cfg.StartupTimeout)
	if !healthy {
		log.Printf("[Pool] instance %s failed to go healthy in %s; tearing down", instanceID, m.cfg.StartupTimeout)
		_ = m.shutdownLocked(instanceID, fmt.Errorf("health check failed"))
		return nil, fmt.Errorf("pool: instance %s never became healthy", instanceID)
	}

	// 7. Create the initial opencode session. We do this server-side
	// (rather than letting the MCP poll-lock onto whatever session
	// might exist) so the archive loop has a single authoritative
	// place to read and rotate the session id. Failure here doesn't
	// abort the spawn — the agent is still useful to the operator
	// via the UI, and a later broadcast can retry. See SessionCreator
	// for the abstraction boundary (real impl: opencode.Client).
	//
	// Note: this is independent of SkipOpencodeEnvPrep. The env-prep
	// flag exists so tests can skip npm install; session creation
	// runs whenever a SessionCreator is wired, since tests that care
	// about session plumbing inject a fake creator and tests that
	// don't leave it nil.
	var sessionID string
	if m.sessionCreator != nil {
		if sid, err := m.sessionCreator.CreateInitialSession(ctx, serveURL, name); err != nil {
			log.Printf("[Pool] instance %s: create opencode session failed: %v (agent is still running; archive loop will retry)", instanceID, err)
		} else {
			sessionID = sid
		}
	}

	// 8. Flip DB + in-memory state to ready, committing the session
	// id in the same update so operators never see "ready" without a
	// session.
	now := time.Now()
	agentUpdates := map[string]any{
		"status":         "online",
		"last_heartbeat": &now,
	}
	if sessionID != "" {
		agentUpdates["session_id"] = sessionID
	}
	_ = m.store.UpdateAgent(agentID, agentUpdates)
	m.mu.Lock()
	sp.inst.Status = "ready"
	sp.inst.OpencodeSessionID = sessionID
	// Stamp activity on ready transition so a freshly-spawned agent
	// doesn't immediately qualify as idle. Dormancy detector checks
	// Since(LastActivityAt) > IdleTimeout.
	sp.inst.LastActivityAt = time.Now()
	m.mu.Unlock()

	m.recordEvent(instanceID, "spawn_ready", fmt.Sprintf("port=%d session=%s", port, sessionID))

	// 9. Background watcher — flips status to "crashed" if the
	// subprocess exits on its own.
	go m.watch(sp)

	return &sp.inst, nil
}

// watch fires when the subprocess exits. The goroutine runs for the
// lifetime of the subprocess; clean shutdown paths also route through
// here via stopChan. Two subtleties: (a) the watch gets launched once
// per Spawn AND once per Wake so there can be overlapping goroutines
// on the same subprocess struct across a dormancy/wake transition;
// (b) dormancy nils out sp.handle when it tears down the subprocess,
// so a racy launch that reads sp.handle after the nil-out would
// otherwise panic. Snapshot the handle under the lock and bail if
// it's already gone — the previous watcher will have done our job.
func (m *Manager) watch(sp *subprocess) {
	m.mu.Lock()
	handle := sp.handle
	m.mu.Unlock()
	if handle == nil {
		return
	}
	exitCh := handle.Wait()
	select {
	case code := <-exitCh:
		m.mu.Lock()
		// Any "we planned this exit" status (stopped/stopping,
		// dormant/waking) must NOT be overwritten — the exit is
		// the teardown side of a graceful transition that a
		// different goroutine already accounted for. Only genuine
		// surprise exits (ready → dead) get the crashed stamp.
		s := sp.inst.Status
		planned := s == "stopping" || s == "stopped" || s == "dormant" || s == "waking"
		if !planned {
			sp.inst.Status = "crashed"
			sp.inst.LastError = fmt.Sprintf("exited with code %d", code)
			log.Printf("[Pool] instance %s crashed: %s", sp.inst.ID, sp.inst.LastError)
		}
		m.mu.Unlock()
		// Agent DB row: only push offline if this was a crash.
		// Dormancy / planned shutdown already updated the row in
		// their own code paths and re-updating here would clobber
		// whatever state they set.
		if !planned {
			_ = m.store.UpdateAgent(sp.inst.AgentID, map[string]any{"status": "offline"})
			m.recordEvent(sp.inst.ID, "crash", fmt.Sprintf("exit code=%d", code))
		}
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
	handle := sp.handle
	m.mu.Unlock()

	// Flip DB state FIRST — before waiting for the subprocess to exit.
	// Task dispatcher queries `agents WHERE status='online'` on its
	// 15s tick; if we waited for handle.Terminate (up to ShutdownGrace
	// ≈ 10s) before updating the row, dispatcher could still broadcast
	// TASK_ASSIGN to this doomed agent and those broadcasts silently
	// land in Redis nobody drains. Marking offline up-front gives the
	// dispatcher an immediate "don't bother" signal while the OS cleanup
	// is still in flight.
	now := time.Now()
	_ = m.store.UpdateAgent(sp.inst.AgentID, map[string]any{
		"status":         "offline",
		"last_heartbeat": &now,
	})

	// Tear down outside the lock. Guard against nil handle too —
	// dormancy nils it on transition, so a Shutdown called on a
	// dormant instance has nothing to terminate.
	close(sp.stopChan)
	if handle != nil {
		handle.Terminate(m.cfg.ShutdownGrace)
	}

	m.mu.Lock()
	sp.inst.Status = "stopped"
	if cause != nil {
		sp.inst.LastError = cause.Error()
	}
	m.mu.Unlock()
	detail := ""
	if cause != nil {
		detail = cause.Error()
	}
	m.recordEvent(instanceID, "shutdown", detail)
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
	// Drop metrics ring too — no point keeping history around for an
	// id the operator just asked us to forget.
	delete(m.metrics, id)
	// Also drop the orphan agent row so the dashboard stops showing it.
	_ = m.store.DeleteAgent(sp.inst.AgentID)
	return nil
}

// ShutdownAll stops every instance — useful on server shutdown so we
// don't leak subprocesses. Also stops the context watcher AND the
// directed-broadcast consumer so no background goroutine keeps
// hitting Redis or dying opencode serves after we've killed them.
func (m *Manager) ShutdownAll() {
	m.stopContextWatcher()
	m.StopBroadcastConsumer()
	m.StopDormancyDetector()
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
