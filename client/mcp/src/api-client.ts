import axios, { AxiosInstance } from 'axios'

export class ApiClient {
  private client: AxiosInstance
  private accessKey: string
  private project: string

  constructor(baseUrl: string, accessKey: string, project: string) {
    this.accessKey = accessKey
    this.project = project
    this.client = axios.create({
      baseURL: baseUrl,
      headers: { Authorization: `Bearer ${accessKey}` },
      timeout: 30000,
    })
  }

  get projectId() { return this.project }
  get currentAccessKey() { return this.accessKey }

  setAccessKey(key: string) {
    this.accessKey = key
    this.client.defaults.headers.Authorization = `Bearer ${key}`
  }

  setProject(project: string) {
    this.project = project
  }

  async selectProject(projectId: string) {
    const { data } = await this.client.post('/api/v1/auth/select-project', { project: projectId })
    return data
  }

  async login(key: string, project?: string) {
    const { data } = await this.client.post('/api/v1/auth/login', { key, project })
    return data
  }

  async logout(key: string) {
    const { data } = await this.client.post('/api/v1/auth/logout', { key })
    return data
  }

  async heartbeat() {
    const { data } = await this.client.post('/api/v1/auth/heartbeat', {})
    return data
  }

  async claimTask(taskId: string) {
    const { data } = await this.client.post('/api/v1/task/claim', { task_id: taskId })
    return data
  }

  async completeTask(taskId: string) {
    const { data } = await this.client.post('/api/v1/task/complete', { task_id: taskId })
    return data
  }

  async releaseTask(taskId: string, reason?: string) {
    const { data } = await this.client.post('/api/v1/task/release', { task_id: taskId, reason: reason || '' })
    return data
  }

  async listTasks(projectId: string) {
    const { data } = await this.client.get('/api/v1/task/list', { params: { project_id: projectId } })
    return data
  }

  async acquireLock(taskId: string, files: string[], reason: string) {
    const { data } = await this.client.post('/api/v1/filelock/acquire', {
      task_id: taskId, files, reason,
    }, { params: { project_id: this.project } })
    return data
  }

  async releaseLock(files?: string[]) {
    const { data } = await this.client.post('/api/v1/filelock/release', { files: files || [] },
      { params: { project_id: this.project } })
    return data
  }

  async checkLocks(files: string[]) {
    const { data } = await this.client.post('/api/v1/filelock/check', { files },
      { params: { project_id: this.project } })
    return data
  }

  async renewLocks() {
    if (!this.project) return { success: true, data: { renewed: [] } }
    const { data } = await this.client.post('/api/v1/filelock/renew', {},
      { params: { project_id: this.project } })
    return data
  }

  async submitChange(changeData: {
    task_id: string
    description?: string
    version: string
    writes: (string | { path: string; content: string })[]
    deletes?: string[]
    /**
     * Optional list of KnowledgeArtifact IDs the client received on
     * task.claim and was guided by while producing this change. Enables
     * the server-side feedback loop: HandleChangeAudit uses this list to
     * bump success_count / failure_count on the exact artifacts whose
     * advice was acted upon. Safe to omit.
     */
    injected_artifact_ids?: string[]
    /**
     * Richer alternative to injected_artifact_ids. Each entry preserves
     * the selector's reason + score at claim time, letting the server
     * compute per-reason success rates (semantic vs importance vs
     * recency) over time. Server prefers this field when both are sent.
     */
    injected_refs?: { id: string; reason?: string; score?: number }[]
  }) {
    // Use longer timeout for audit workflow (2 minutes)
    const { data } = await this.client.post('/api/v1/change/submit', changeData, {
      params: { project_id: this.project },
      timeout: 120000,
    })
    return data
  }

  async syncFiles(version = '') {
    const { data } = await this.client.post('/api/v1/file/sync', { version })
    return data
  }

  async syncStatus() {
    const { data } = await this.client.get('/api/v1/status/sync')
    return data
  }

  async projectInfo(query: string) {
    const { data } = await this.client.post('/api/v1/project/info', { query })
    return data
  }

  // poll asks the platform for buffered broadcasts + directed messages.
  // ackedDirectedIds is a list of header.messageID values for directed
  // messages this client has fully processed (injected into the
  // OpenCode session). The server LREMs matching entries from the
  // directed queue so they aren't redelivered. Empty / omitted on
  // first call; populated on subsequent calls by the poller's
  // ackProvider hook.
  async poll(ackedDirectedIds: string[] = []) {
    const body: Record<string, unknown> = {}
    if (ackedDirectedIds.length > 0) {
      body.acked_directed_ids = ackedDirectedIds
    }
    const { data } = await this.client.post('/api/v1/poll', body)
    return data
  }

  // Branch APIs
  async createBranch(name: string) {
    const { data } = await this.client.post('/api/v1/branch/create', { name })
    return data
  }

  async enterBranch(branchId: string) {
    const { data } = await this.client.post('/api/v1/branch/enter', { branch_id: branchId })
    return data
  }

  async leaveBranch() {
    const { data } = await this.client.post('/api/v1/branch/leave', {})
    return data
  }

  async listBranches() {
    const { data } = await this.client.get('/api/v1/branch/list')
    return data
  }

  async closeBranch(branchId: string) {
    const { data } = await this.client.post('/api/v1/branch/close', { branch_id: branchId })
    return data
  }

  async syncMain() {
    const { data } = await this.client.post('/api/v1/branch/sync_main', {})
    return data
  }

  // PR APIs
  async submitPR(prData: {
    title: string
    description?: string
    self_review: string | object
  }) {
    const { data } = await this.client.post('/api/v1/pr/submit', prData, {
      timeout: 60000,
    })
    return data
  }

  async listPRs() {
    const { data } = await this.client.get('/api/v1/pr/list')
    return data
  }

  async getPR(prId: string) {
    const { data } = await this.client.get(`/api/v1/pr/${prId}`)
    return data
  }

  // Feedback (Phase 3B): submit task completion experience.
  async submitFeedback(feedback: {
    task_id: string
    outcome: string
    approach?: string
    pitfalls?: string
    key_insight?: string
    missing_context?: string
    would_do_differently?: string
    files_read?: string[]
  }) {
    const { data } = await this.client.post('/api/v1/feedback/submit', feedback, {
      params: { project_id: this.project },
    })
    return data
  }
}
