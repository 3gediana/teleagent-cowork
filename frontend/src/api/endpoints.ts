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