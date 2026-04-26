import api from './client'
import type { DashboardState } from './types'

export const dashboardApi = {
  getState: (projectId: string) =>
    api.get('/dashboard/state', { params: { project_id: projectId } }) as Promise<{ success: boolean; data: DashboardState }>,

  input: (projectId: string, targetBlock: string, content: string) =>
    api.post('/dashboard/input', { target_block: targetBlock, content }, { params: { project_id: projectId } }) as Promise<{ success: boolean; data: any }>,

  confirm: (projectId: string, inputId: string, confirmed: boolean) =>
    api.post('/dashboard/confirm', { input_id: inputId, confirmed }, { params: { project_id: projectId } }) as Promise<{ success: boolean; data: any }>,

  clearContext: (projectId: string) =>
    api.post('/dashboard/clear_context', {}, { params: { project_id: projectId } }) as Promise<{ success: boolean; data: any }>,

  getMessages: (projectId: string) =>
    api.get('/dashboard/messages', { params: { project_id: projectId } }) as Promise<{ success: boolean; data: any }>,
}

export const authApi = {
  login: (key: string, project?: string) =>
    api.post('/auth/login', { key, project }) as Promise<{ success: boolean; data: any }>,

  register: (name: string, projectId?: string, isHuman: boolean = true) =>
    api.post('/agent/register', { name, project_id: projectId, is_human: isHuman }) as Promise<{ success: boolean; data: any }>,

  logout: () =>
    api.post('/auth/logout', {}) as Promise<{ success: boolean; data: any }>,
}

export const projectApi = {
  list: () =>
    api.get('/project/list') as Promise<{ success: boolean; data: any[] }>,

  create: (name: string, description?: string, githubRepo?: string, importExisting?: boolean) =>
    api.post('/project/create', { name, description, github_repo: githubRepo, import_existing: importExisting || false }) as Promise<{ success: boolean; data: any }>,

  get: (id: string) =>
    api.get(`/project/${id}`) as Promise<{ success: boolean; data: any }>,

  setAutoMode: (projectId: string, autoMode: boolean) =>
    api.post('/project/auto_mode', { auto_mode: autoMode }, { params: { project_id: projectId } }) as Promise<{ success: boolean; data: any }>,
}

export const taskApi = {
  list: (projectId: string) =>
    api.get('/task/list', { params: { project_id: projectId } }) as Promise<{ success: boolean; data: any }>,

  create: (projectId: string, name: string, description: string, priority: string, milestoneId?: string) =>
    api.post('/task/create', { name, description, priority, milestone_id: milestoneId }, { params: { project_id: projectId } }) as Promise<{ success: boolean; data: any }>,

  claim: (taskId: string) =>
    api.post('/task/claim', { task_id: taskId }) as Promise<{ success: boolean; data: any }>,

  complete: (taskId: string) =>
    api.post('/task/complete', { task_id: taskId }) as Promise<{ success: boolean; data: any }>,
}

// tagApi wraps the /tag/* endpoints added in PR 6 so the review UI can
// confirm / reject / supersede proposed tags without a page reload.
// All mutation endpoints are human-only on the server; the UI still
// shows the buttons for agents so the failure message comes from the
// platform, not the frontend — surface the real error rather than
// hiding the affordance.
export const tagApi = {
  list: (taskId: string, status?: string) =>
    api.get('/tag/list', { params: { task_id: taskId, status } }) as Promise<{ success: boolean; data: any }>,

  confirm: (tagId: string, note?: string) =>
    api.post('/tag/confirm', { tag_id: tagId, note }) as Promise<{ success: boolean; data: any }>,

  reject: (tagId: string, note?: string) =>
    api.post('/tag/reject', { tag_id: tagId, note }) as Promise<{ success: boolean; data: any }>,

  supersede: (oldTagId: string, newTagId: string) =>
    api.post('/tag/supersede', { old_tag_id: oldTagId, new_tag_id: newTagId }) as Promise<{ success: boolean; data: any }>,
}

// metricsApi exposes read-only analytics derived from change-audit
// feedback. Today only the injection-signal rollup (PR 9) is live; as
// more metrics land, group them into this same client so the
// KnowledgePage / dashboard can hydrate one surface at a time.
export const metricsApi = {
  injectionSignal: (projectId: string, limit?: number) =>
    api.get('/metrics/injection-signal', { params: { project_id: projectId, limit } }) as Promise<{ success: boolean; data: any }>,
}

// llmApi manages user-registered LLM endpoints (the replacement for
// the opencode-hosted provider catalogue). Any authenticated agent
// can List/Get; only humans may Create/Update/Delete/Test (server
// enforces via IsHuman gate — UI just surfaces whatever error the
// backend returns).
//
// Shape of an endpoint row (wire shape from the server):
//   { id, name, format: 'openai'|'anthropic', base_url,
//     api_key_redacted, api_key_set, models: ModelInfo[],
//     default_model, status, registered, created_at, updated_at }
export const llmApi = {
  list: () =>
    api.get('/llm/endpoints') as Promise<{ success: boolean; data: { endpoints: any[] } }>,

  get: (id: string) =>
    api.get(`/llm/endpoints/${id}`) as Promise<{ success: boolean; data: any }>,

  create: (payload: {
    name: string
    format: 'openai' | 'anthropic'
    base_url?: string
    api_key: string
    models?: any[]
    default_model?: string
    status?: 'active' | 'disabled'
  }) =>
    api.post('/llm/endpoints', payload) as Promise<{ success: boolean; data: any; warning?: string }>,

  // On update, omit api_key to keep the existing secret (GET returns a
  // redacted value, so the UI can't round-trip it safely). Send an
  // empty base_url explicitly to reset to the provider's canonical URL.
  update: (id: string, payload: {
    name?: string
    format?: 'openai' | 'anthropic'
    base_url?: string
    api_key?: string
    models?: any[]
    default_model?: string
    status?: 'active' | 'disabled'
  }) =>
    api.put(`/llm/endpoints/${id}`, payload) as Promise<{ success: boolean; data: any; warning?: string }>,

  // Delete is soft on the first call (status→disabled) and hard on the
  // second. UI can distinguish via the `deleted` field ("soft" vs "hard")
  // in the response if it wants to render a confirmation flow.
  del: (id: string) =>
    api.delete(`/llm/endpoints/${id}`) as Promise<{ success: boolean; data: { deleted: 'soft' | 'hard' } }>,

  // Dispatches a 1-token probe request. Intended for the "Test connection"
  // button in the endpoint editor — returns the provider error verbatim.
  test: (id: string, model?: string) =>
    api.post(`/llm/endpoints/${id}/test`, {}, { params: model ? { model } : {} }) as Promise<{ success: boolean; data: any }>,
}

export const changeApi = {
  list: (projectId: string, status?: string) =>
    api.get('/change/list', { params: { project_id: projectId, status } }) as Promise<{ success: boolean; data: any }>,

  review: (changeId: string, level: string, approved: boolean, reason: string) =>
    api.post('/change/review', { change_id: changeId, level, approved, reason }) as Promise<{ success: boolean; data: any }>,

  approveForReview: (changeId: string) =>
    api.post('/change/approve_for_review', { change_id: changeId }) as Promise<{ success: boolean; data: any }>,
}

export const milestoneApi = {
  switch: (projectId: string) =>
    api.post('/milestone/switch', {}, { params: { project_id: projectId } }) as Promise<{ success: boolean; data: any }>,

  archives: (projectId: string) =>
    api.get('/milestone/archives', { params: { project_id: projectId } }) as Promise<{ success: boolean; data: any }>,
}

export const versionApi = {
  rollback: (projectId: string, version: string, reason?: string) =>
    api.post('/version/rollback', { version, reason }, { params: { project_id: projectId } }) as Promise<{ success: boolean; data: any }>,

  list: (projectId: string) =>
    api.get('/version/list', { params: { project_id: projectId } }) as Promise<{ success: boolean; data: any }>,
}

export const branchApi = {
  list: () =>
    api.get('/branch/list') as Promise<{ success: boolean; data: { branches: any[] } }>,

  create: (name: string) =>
    api.post('/branch/create', { name }) as Promise<{ success: boolean; data: any }>,

  close: (branchId: string) =>
    api.post('/branch/close', { branch_id: branchId }) as Promise<{ success: boolean; data: any }>,
}

export const prApi = {
  list: () =>
    api.get('/pr/list') as Promise<{ success: boolean; data: { pull_requests: any[] } }>,

  get: (prId: string) =>
    api.get(`/pr/${prId}`) as Promise<{ success: boolean; data: any }>,

  approveReview: (prId: string) =>
    api.post('/pr/approve_review', { pr_id: prId }) as Promise<{ success: boolean; data: any }>,

  approveMerge: (prId: string, version?: string) =>
    api.post('/pr/approve_merge', { pr_id: prId, version }) as Promise<{ success: boolean; data: any }>,

  reject: (prId: string, reason?: string) =>
    api.post('/pr/reject', { pr_id: prId, reason }) as Promise<{ success: boolean; data: any }>,
}

export const roleApi = {
  list: () =>
    api.get('/role/list') as Promise<{ success: boolean; data: any[] }>,

  updateModel: (role: string, modelProvider: string, modelId: string) =>
    api.post('/role/update_model', { role, model_provider: modelProvider, model_id: modelId }) as Promise<{ success: boolean; data: any }>,
}

export const providerApi = {
  list: () =>
    api.get('/opencode/providers') as Promise<{ success: boolean; data: { providers: any[]; models: any[]; default: Record<string, string> } }>,
}

export const chiefApi = {
  chat: (projectId: string, message: string) =>
    api.post('/chief/chat', { message }, { params: { project_id: projectId } }) as Promise<{ success: boolean; data: any }>,

  sessions: (projectId: string, role?: string) =>
    api.get('/chief/sessions', { params: { project_id: projectId, role } }) as Promise<{ success: boolean; data: { sessions: any[] } }>,

  traces: (sessionId: string) =>
    api.get('/chief/traces', { params: { session_id: sessionId } }) as Promise<{ success: boolean; data: { traces: any[] } }>,

  policies: (status?: string) =>
    api.get('/chief/policies', { params: { status } }) as Promise<{ success: boolean; data: { policies: any[] } }>,
}

export const experienceApi = {
  list: (projectId: string, status?: string, sourceType?: string) =>
    api.get('/experience/list', { params: { project_id: projectId, status, source_type: sourceType } }) as Promise<{ success: boolean; data: { experiences: any[] } }>,
}

export const skillApi = {
  list: (status?: string) =>
    api.get('/skill/list', { params: { status } }) as Promise<{ success: boolean; data: { skills: any[] } }>,
  get: (id: string) =>
    api.get('/skill/' + id) as Promise<{ success: boolean; data: any }>,
  approve: (id: string) =>
    api.post('/skill/' + id + '/approve') as Promise<{ success: boolean; data: any }>,
  reject: (id: string) =>
    api.post('/skill/' + id + '/reject') as Promise<{ success: boolean; data: any }>,
}

export const policyApi = {
  list: (status?: string) =>
    api.get('/policy/list', { params: { status } }) as Promise<{ success: boolean; data: { policies: any[] } }>,
  get: (id: string) =>
    api.get('/policy/' + id) as Promise<{ success: boolean; data: any }>,
  activate: (id: string) =>
    api.post('/policy/' + id + '/activate') as Promise<{ success: boolean; data: any }>,
  deactivate: (id: string) =>
    api.post('/policy/' + id + '/deactivate') as Promise<{ success: boolean; data: any }>,
}

// agentPoolApi drives the platform-hosted agent pool — the subsystem
// that lets the platform spawn its own opencode subprocesses on the
// host, auto-inject skills, and treat them like normal client
// agents. See @platform/backend/internal/agentpool/pool.go for the
// backend design. Only humans can spawn/shutdown/purge (server
// enforces via IsHuman gate); List is open to any authenticated agent
// so the dashboard can render the pool state on login.
export type PoolInstance = {
  id: string
  agent_id: string
  agent_name: string
  role?: string
  project_id?: string
  port: number
  pid: number
  status: 'starting' | 'ready' | 'crashed' | 'stopping' | 'stopped' | 'dormant' | 'waking'
  started_at: string
  skills_injected?: string[]
  working_dir?: string
  last_error?: string
  // Opencode session plumbing — populated when the platform pairs
  // a subprocess with a provider/model and the session-creator
  // step succeeds on spawn. See @platform/backend/internal/agentpool/pool.go.
  opencode_provider_id?: string
  opencode_model_id?: string
  opencode_session_id?: string
  // Context-watch bookkeeping: rotation count for how many times
  // the background watcher has rolled the session to a new one
  // before the threshold, and the last token footprint the probe
  // observed. Both drive the "session health" panel on the card.
  archive_rotation?: number
  last_context_tokens?: number
  // Dormancy lifecycle timestamps. LastActivityAt is the "is this
  // agent asleep yet" signal the backend's detector keys on;
  // DormantAt is set when the transition actually happens. Both
  // omitted for an agent that's never gone through the cycle.
  last_activity_at?: string
  dormant_at?: string
}

// OpencodeProvider is one entry from ~/.config/opencode/opencode.json
// surfaced via /agentpool/opencode-providers. IDs are the strings
// opencode itself accepts in a session body, so the spawn modal
// can feed them straight through without translation.
export interface OpencodeProvider {
  id: string
  name: string
  npm?: string
  models: OpencodeProviderModel[]
}

export interface OpencodeProviderModel {
  id: string
  name: string
  context?: number
  output?: number
}

// PoolTokenSample / PoolEvent mirror the ring-buffer shape in
// internal/agentpool/metrics.go. at_ms is unix milliseconds.
export interface PoolTokenSample {
  at_ms: number
  tokens: number
  session_id?: string
}

export interface PoolEvent {
  at_ms: number
  type: 'spawn_ready' | 'rotate' | 'dormancy' | 'wake' | 'crash' | 'shutdown' | string
  detail?: string
}

export interface PoolMetrics {
  tokens: PoolTokenSample[]
  events: PoolEvent[]
}

export const agentPoolApi = {
  list: () =>
    api.get('/agentpool/list') as Promise<{ success: boolean; data: { instances: PoolInstance[] } }>,

  // Opencode config.json provider catalogue — NOT the LLMEndpoint
  // rows returned by /opencode/providers. See the comment on
  // PoolInstance.opencode_provider_id for why these are different.
  opencodeProviders: () =>
    api.get('/agentpool/opencode-providers') as Promise<{ success: boolean; data: { providers: OpencodeProvider[] } }>,

  // Per-instance metric rings: token curve + lifecycle event log.
  // Safe to poll at the dashboard's refresh cadence; entirely
  // in-memory on the backend.
  metrics: (instanceId: string) =>
    api.get(`/agentpool/metrics/${instanceId}`) as Promise<{ success: boolean; data: PoolMetrics }>,

  spawn: (payload: {
    project_id?: string
    role_hint?: string
    name?: string
    opencode_provider_id?: string
    opencode_model_id?: string
  }) =>
    api.post('/agentpool/spawn', payload) as Promise<{ success: boolean; data: PoolInstance; error?: any }>,

  shutdown: (instanceId: string) =>
    api.post('/agentpool/shutdown', { instance_id: instanceId }) as Promise<{ success: boolean; data: any }>,

  // Dormancy manual controls — mirror what the background detector
  // does on idle-timeout. Human-gated on the server (IsHuman).
  sleep: (instanceId: string) =>
    api.post('/agentpool/sleep', { instance_id: instanceId }) as Promise<{ success: boolean; data: any; error?: any }>,

  wake: (instanceId: string) =>
    api.post('/agentpool/wake', { instance_id: instanceId }) as Promise<{ success: boolean; data: PoolInstance; error?: any }>,

  purge: (instanceId: string) =>
    api.post('/agentpool/purge', { instance_id: instanceId }) as Promise<{ success: boolean; data: any }>,
}

export const refineryApi = {
  run: (projectId: string, lookbackHours?: number) =>
    api.post('/refinery/run', { project_id: projectId, lookback_hours: lookbackHours }) as Promise<{ success: boolean; data: { run_id: string; status: string } }>,

  runs: (projectId: string, limit = 20) =>
    api.get('/refinery/runs', { params: { project_id: projectId, limit } }) as Promise<{ success: boolean; data: { runs: any[] } }>,
  artifacts: (projectId: string, kind?: string, status?: string, limit = 200) =>
    api.get('/refinery/artifacts', { params: { project_id: projectId, kind, status, limit } }) as Promise<{ success: boolean; data: { artifacts: any[]; counts: { kind: string; total: number }[] } }>,
  growth: (projectId: string, days = 30) =>
    api.get('/refinery/growth', { params: { project_id: projectId, days } }) as Promise<{ success: boolean; data: { series: { day: string; kind: string; count: number }[]; days: number } }>,
  updateStatus: (artifactId: string, status: string) =>
    api.put('/refinery/artifacts/' + artifactId + '/status', { status }) as Promise<{ success: boolean; data: { id: string; status: string } }>,
}

// LoopCheck — mirrors internal/service/loopcheck. See the Go doc
// comments there for what each field means; the types here are
// kept minimal so the page can render whatever the server ships
// without a schema-contract dance.
export type LoopStatus = 'healthy' | 'stale' | 'unused' | 'broken'

export interface LoopCheck {
  name: string
  status: LoopStatus
  summary: string
  metrics?: Record<string, any>
  last_activity?: string | null
}

export interface LoopPillar {
  name: string
  overall_status: LoopStatus
  checks: LoopCheck[]
}

export interface LoopReport {
  generated_at: string
  window_days: number
  project_id?: string
  self_evolution: LoopPillar
  automation: LoopPillar
  overall_status: LoopStatus
}

export const loopcheckApi = {
  get: (projectId?: string, windowDays = 7) =>
    api.get('/loopcheck', {
      params: { project_id: projectId || undefined, window_days: windowDays },
    }) as Promise<{ success: boolean; data: LoopReport }>,
}