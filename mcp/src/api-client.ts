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

  async login(key: string, project?: string) {
    const { data } = await this.client.post('/api/v1/auth/login', { key, project })
    return data
  }

  async logout() {
    const { data } = await this.client.post('/api/v1/auth/logout', {})
    return data
  }

  async heartbeat() {
    const { data } = await this.client.post('/api/v1/auth/heartbeat', {})
    return data
  }

  async createTask(name: string, description: string, priority: string, milestoneId?: string) {
    const { data } = await this.client.post('/api/v1/task/create', {
      name, description, priority, milestone_id: milestoneId,
    }, { params: { project_id: this.project } })
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

  async listTasks(projectId: string) {
    const { data } = await this.client.get('/api/v1/task/list', { params: { project_id: projectId } })
    return data
  }

  async deleteTask(taskId: string) {
    const { data } = await this.client.delete(`/api/v1/task/${taskId}`)
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

  async renewLock() {
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
  }) {
    const { data } = await this.client.post('/api/v1/change/submit', changeData, {
      params: { project_id: this.project },
    })
    return data
  }

  async listChanges(status?: string) {
    const params: any = { project_id: this.project }
    if (status) params.status = status
    const { data } = await this.client.get('/api/v1/change/list', { params })
    return data
  }

  async reviewChange(changeId: string, level: string, approved: boolean, reason: string) {
    const { data } = await this.client.post('/api/v1/change/review', {
      change_id: changeId, level, approved, reason,
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

  async milestoneSwitch() {
    const { data } = await this.client.post('/api/v1/milestone/switch', {}, {
      params: { project_id: this.project },
    })
    return data
  }

  async milestoneArchives() {
    const { data } = await this.client.get('/api/v1/milestone/archives', {
      params: { project_id: this.project },
    })
    return data
  }

  async versionRollback(version: string, reason?: string) {
    const { data } = await this.client.post('/api/v1/version/rollback', {
      version, reason,
    }, { params: { project_id: this.project } })
    return data
  }

  async versionList() {
    const { data } = await this.client.get('/api/v1/version/list', {
      params: { project_id: this.project },
    })
    return data
  }
}