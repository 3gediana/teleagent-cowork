import { create } from 'zustand'

interface ProjectState {
  id: string
  name: string
  direction: string | null
  milestone: string | null
  milestoneId: string | null
  version: string
  tasks: Task[]
  locks: Lock[]
  agents: Agent[]
}

interface Task {
  id: string
  name: string
  description: string
  status: string
  assignee_id?: string | null
  assignee_name?: string | null
  priority: string
  milestone_id?: string | null
}

interface Lock {
  lock_id?: string
  task_id: string
  agent_name: string
  files: string[]
  reason: string
  acquired_at: string
  expires_at: string
}

interface Agent {
  id: string
  name: string
  status: string
  current_task: string | null
}

interface ChatMessage {
  id: string
  role: 'human' | 'agent' | 'system'
  content: string
  timestamp: number
}

interface PendingInput {
  input_id: string
  target_block: string
  content: string
  requires_confirm?: boolean
}

interface AppState {
  project: ProjectState | null
  selectedProjectId: string | null
  targetBlock: 'direction' | 'milestone' | 'task'
  chatMessages: ChatMessage[]
  inputText: string
  loading: boolean
  accessKey: string | null
  pendingInput: PendingInput | null
  sessionId: string | null

  setProject: (p: ProjectState) => void
  setTargetBlock: (b: 'direction' | 'milestone' | 'task') => void
  addChatMessage: (m: ChatMessage) => void
  clearChat: () => void
  setInputText: (t: string) => void
  setLoading: (l: boolean) => void
  setAccessKey: (k: string | null) => void
  setSelectedProjectId: (id: string | null) => void
  setPendingInput: (p: PendingInput | null) => void
  setSessionId: (id: string | null) => void
}

export const useAppStore = create<AppState>((set) => ({
  project: null,
  selectedProjectId: null,
  targetBlock: 'direction',
  chatMessages: [],
  inputText: '',
  loading: false,
  accessKey: localStorage.getItem('a3c_access_key'),
  pendingInput: null,
  sessionId: null,

  setProject: (p) => set({ project: p }),
  setTargetBlock: (b) => set({ targetBlock: b }),
  addChatMessage: (m) => set((s) => ({ chatMessages: [...s.chatMessages, m] })),
  clearChat: () => set({ chatMessages: [] }),
  setInputText: (t) => set({ inputText: t }),
  setLoading: (l) => set({ loading: l }),
  setAccessKey: (k) => {
    if (k) localStorage.setItem('a3c_access_key', k)
    else localStorage.removeItem('a3c_access_key')
    set({ accessKey: k })
  },
  setSelectedProjectId: (id) => set({ selectedProjectId: id }),
  setPendingInput: (p) => set({ pendingInput: p }),
  setSessionId: (id) => set({ sessionId: id }),
}))