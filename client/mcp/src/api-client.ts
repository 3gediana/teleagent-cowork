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

  async submitChange(changeData: {
    task_id: string
    description?: string
    version: string
    writes: (string | { path: string; content: string })[]
    deletes?: string[]
  }) {
    // Use longer timeout for audit workflow (2 minutes)
    const { data } = await this.client.post('/api/v1/change/submit', changeData, {
      params: { project_id: this.project },
      timeout: 120000,
    })
    return data
  }

  async syncFiles(version: string) {
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

  async poll() {
    const { data } = await this.client.post('/api/v1/poll', { key: this.accessKey })
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
    self_review: string
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
}
