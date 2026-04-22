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

export interface BroadcastEvent {
  id: string
  type: string
  payload: Record<string, unknown>
  timestamp: number
}

export interface ActivityItem {
  id: string
  agentName: string
  action: string
  target?: string
  timestamp: number
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

interface PendingChange {
  change_id: string
  agent_id: string
  task_id: string
  description: string
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
  autoMode: boolean
  pendingChanges: PendingChange[]
  broadcastEvents: BroadcastEvent[]
  activities: ActivityItem[]
  sidebarCollapsed: boolean

  setProject: (p: ProjectState) => void
  setTargetBlock: (b: 'direction' | 'milestone' | 'task') => void
  addChatMessage: (m: ChatMessage) => void
  // upsertChatMessage replaces a message with the same id if one
  // exists, or appends if not. Powers live typewriter streaming for
  // native-runtime sessions (AGENT_TEXT_DELTA → repeated upsert with
  // the same `stream-${session_id}` key). Opencode sessions never
  // use this — their CHAT_UPDATE adds a fresh message via
  // addChatMessage as before.
  upsertChatMessage: (m: ChatMessage) => void
  // removeChatMessage drops a message by id. Used to tear down the
  // streaming placeholder when a terminal CHAT_UPDATE lands (the
  // finalised message is added via addChatMessage; we don't want
  // duplicates).
  removeChatMessage: (id: string) => void
  clearChat: () => void
  setInputText: (t: string) => void
  setLoading: (l: boolean) => void
  setAccessKey: (k: string | null) => void
  setSelectedProjectId: (id: string | null) => void
  setPendingInput: (p: PendingInput | null) => void
  setSessionId: (id: string | null) => void
  setAutoMode: (m: boolean) => void
  addPendingChange: (c: PendingChange) => void
  removePendingChange: (id: string) => void
  clearPendingChanges: () => void
  addBroadcastEvent: (e: BroadcastEvent) => void
  clearBroadcastEvents: () => void
  addActivity: (a: ActivityItem) => void
  toggleSidebar: () => void
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
  autoMode: true,
  pendingChanges: [],
  broadcastEvents: [],
  activities: [],
  sidebarCollapsed: false,

  setProject: (p) => set({ project: p }),
  setTargetBlock: (b) => set({ targetBlock: b }),
  addChatMessage: (m) => set((s) => ({ chatMessages: [...s.chatMessages, m] })),
  upsertChatMessage: (m) => set((s) => {
    const idx = s.chatMessages.findIndex((x) => x.id === m.id)
    if (idx === -1) return { chatMessages: [...s.chatMessages, m] }
    const next = s.chatMessages.slice()
    next[idx] = m
    return { chatMessages: next }
  }),
  removeChatMessage: (id) => set((s) => ({
    chatMessages: s.chatMessages.filter((x) => x.id !== id),
  })),
  clearChat: () => set({ chatMessages: [], pendingChanges: [] }),
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
  setAutoMode: (m) => set({ autoMode: m }),
  addPendingChange: (c) => set((s) => ({ pendingChanges: [...s.pendingChanges, c] })),
  removePendingChange: (id) => set((s) => ({ pendingChanges: s.pendingChanges.filter(c => c.change_id !== id) })),
  clearPendingChanges: () => set({ pendingChanges: [] }),
  addBroadcastEvent: (e) => set((s) => ({ broadcastEvents: [e, ...s.broadcastEvents].slice(0, 50) })),
  clearBroadcastEvents: () => set({ broadcastEvents: [] }),
  addActivity: (a) => set((s) => ({ activities: [a, ...s.activities].slice(0, 100) })),
  toggleSidebar: () => set((s) => ({ sidebarCollapsed: !s.sidebarCollapsed })),
}))
