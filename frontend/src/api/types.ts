export interface ApiProject {
  id: string
  name: string
  description?: string
  status: string
  github_repo?: string
}

export interface ApiTask {
  id: string
  name: string
  description: string
  status: string
  assignee_id?: string | null
  assignee_name?: string | null
  priority: string
  milestone_id?: string | null
}

export interface ApiLock {
  lock_id?: string
  task_id: string
  agent_name: string
  files: string[]
  reason: string
  acquired_at: string
  expires_at: string
}

export interface ApiAgent {
  id: string
  name: string
  status: string
  current_task: string | null
}

export interface ApiChange {
  id: string
  task_id?: string | null
  agent_id: string
  version: string
  description: string
  status: string
  audit_level?: string | null
  reviewed_at?: string | null
  created_at: string
}

export interface DashboardState {
  direction?: string
  milestone?: string
  milestone_id?: string
  version: string
  tasks: ApiTask[]
  locks: ApiLock[]
  agents: ApiAgent[]
}