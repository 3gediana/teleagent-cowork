import { useState, useEffect } from 'react'
import { useAppStore } from '../stores/appStore'
import { useDashboard } from '../hooks/useDashboard'
import { useSSE } from '../hooks/useSSE'
import { authApi, dashboardApi, projectApi, taskApi, changeApi, milestoneApi, versionApi } from '../api/endpoints'

function DirectionCard() {
  const project = useAppStore((s) => s.project)
  if (!project) return null
  return (
    <div className="bg-white rounded-lg shadow p-4 mb-4">
      <h3 className="text-sm font-semibold text-gray-500 uppercase mb-2">Direction</h3>
      <p className="text-sm text-gray-700 whitespace-pre-wrap">
        {project.direction || 'No direction set'}
      </p>
    </div>
  )
}

function MilestoneCard({ onSwitch }: { onSwitch: () => void }) {
  const project = useAppStore((s) => s.project)
  if (!project) return null
  const pending = project.tasks.filter((t) => t.status === 'pending').length
  const claimed = project.tasks.filter((t) => t.status === 'claimed').length
  const completed = project.tasks.filter((t) => t.status === 'completed').length
  const allDone = project.tasks.length > 0 && project.tasks.every((t) => t.status === 'completed' || t.status === 'deleted')
  return (
    <div className="bg-white rounded-lg shadow p-4 mb-4">
      <div className="flex items-center justify-between mb-2">
        <h3 className="text-sm font-semibold text-gray-500 uppercase">Milestone</h3>
        {allDone && (
          <button onClick={onSwitch} className="text-xs bg-green-500 text-white px-2 py-1 rounded hover:bg-green-600">
            Switch Milestone
          </button>
        )}
      </div>
      <p className="font-medium text-gray-900 mb-3">{project.milestone || 'No milestone'}</p>
      <div className="space-y-1">
        {project.tasks.map((t) => (
          <div key={t.id} className="flex items-center gap-2 text-sm">
            <span>{t.status === 'completed' ? '\u2705' : t.status === 'claimed' ? '\uD83D\uDD35' : '\u2B1C'}</span>
            <span className={t.status === 'completed' ? 'line-through text-gray-400' : 'text-gray-700'}>
              {t.name}
            </span>
            {t.priority === 'high' && <span className="text-xs text-red-500 font-medium">HIGH</span>}
            {t.assignee_name && <span className="text-xs text-gray-400">({t.assignee_name})</span>}
          </div>
        ))}
      </div>
      <div className="mt-3 text-xs text-gray-400">
        {completed} done / {claimed} active / {pending} pending
      </div>
    </div>
  )
}

function LocksCard() {
  const project = useAppStore((s) => s.project)
  if (!project || project.locks.length === 0) return null
  return (
    <div className="bg-white rounded-lg shadow p-4 mb-4">
      <h3 className="text-sm font-semibold text-gray-500 uppercase mb-2">File Locks</h3>
      {project.locks.map((l, i) => (
        <div key={l.lock_id || i} className="text-sm mb-1.5">
          <span className="text-gray-900 font-medium">{l.agent_name}</span>
          <span className="text-gray-500">: {l.files.join(', ')}</span>
          <div className="text-xs text-gray-400">reason: {l.reason}</div>
        </div>
      ))}
    </div>
  )
}

function VersionCard({ onRollback }: { onRollback: () => void }) {
  const project = useAppStore((s) => s.project)
  if (!project) return null
  return (
    <div className="bg-white rounded-lg shadow p-4 mb-4">
      <div className="flex items-center justify-between mb-2">
        <h3 className="text-sm font-semibold text-gray-500 uppercase">Version</h3>
        <button onClick={onRollback} className="text-xs text-blue-500 hover:text-blue-600">Rollback</button>
      </div>
      <p className="text-lg font-mono font-bold text-gray-900">{project.version}</p>
    </div>
  )
}

function AgentsCard() {
  const project = useAppStore((s) => s.project)
  if (!project || project.agents.length === 0) return null
  return (
    <div className="bg-white rounded-lg shadow p-4 mb-4">
      <h3 className="text-sm font-semibold text-gray-500 uppercase mb-2">Online Agents</h3>
      {project.agents.map((a) => (
        <div key={a.id} className="flex items-center gap-2 text-sm">
          <span className="w-2 h-2 bg-green-400 rounded-full"></span>
          <span>{a.name}</span>
          {a.current_task && <span className="text-xs text-gray-400">({a.current_task})</span>}
        </div>
      ))}
    </div>
  )
}

function CreateTaskModal({ onClose, projectId }: { onClose: () => void; projectId: string }) {
  const [name, setName] = useState('')
  const [desc, setDesc] = useState('')
  const [priority, setPriority] = useState('medium')
  const [result, setResult] = useState<any>(null)
  const { refreshState } = useDashboard()

  const handleCreate = async () => {
    if (!name.trim()) return
    const res = await taskApi.create(projectId, name, desc, priority)
    if (res.success) {
      setResult(res.data)
      refreshState()
    }
  }

  return (
    <div className="fixed inset-0 bg-black/50 flex items-center justify-center z-50">
      <div className="bg-white rounded-lg shadow-xl p-6 w-96">
        {result ? (
          <>
            <h3 className="font-bold text-lg mb-4 text-green-600">Task Created!</h3>
            <div className="space-y-2 text-sm">
              <p><strong>ID:</strong> {result.id}</p>
              <p><strong>Name:</strong> {result.name}</p>
              <p><strong>Status:</strong> {result.status}</p>
            </div>
            <button onClick={onClose} className="mt-4 w-full bg-blue-500 text-white py-2 rounded hover:bg-blue-600">
              Close
            </button>
          </>
        ) : (
          <>
            <h3 className="font-bold text-lg mb-4">Create Task</h3>
            <input
              type="text"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="Task name"
              className="w-full border rounded px-3 py-2 text-sm mb-3"
            />
            <textarea
              value={desc}
              onChange={(e) => setDesc(e.target.value)}
              placeholder="Description (optional)"
              className="w-full border rounded px-3 py-2 text-sm mb-3 h-20 resize-none"
            />
            <select value={priority} onChange={(e) => setPriority(e.target.value)}
              className="w-full border rounded px-3 py-2 text-sm mb-4">
              <option value="high">High</option>
              <option value="medium">Medium</option>
              <option value="low">Low</option>
            </select>
            <div className="flex gap-2">
              <button onClick={handleCreate} className="flex-1 bg-blue-500 text-white py-2 rounded text-sm hover:bg-blue-600">
                Create
              </button>
              <button onClick={onClose} className="flex-1 border py-2 rounded text-sm hover:bg-gray-50">
                Cancel
              </button>
            </div>
          </>
        )}
      </div>
    </div>
  )
}

function ChangesPanel({ projectId }: { projectId: string }) {
  const [changes, setChanges] = useState<any[]>([])
  const [filter, setFilter] = useState('pending')

  useEffect(() => {
    const loadChanges = async () => {
      const res = await changeApi.list(projectId, filter)
      if (res.success) {
        setChanges(res.data.changes || [])
      }
    }
    loadChanges()
    const interval = setInterval(loadChanges, 15000)
    return () => clearInterval(interval)
  }, [projectId, filter])

  const handleReview = async (changeId: string, approved: boolean) => {
    const level = approved ? 'L0' : 'L2'
    const reason = approved ? 'Approved via dashboard' : 'Rejected via dashboard'
    const res = await changeApi.review(changeId, level, approved, reason)
    if (res.success) {
      setChanges(changes.filter((c) => c.id !== changeId))
    }
  }

  return (
    <div className="bg-white rounded-lg shadow p-4 mb-4">
      <div className="flex items-center justify-between mb-3">
        <h3 className="text-sm font-semibold text-gray-500 uppercase">Changes</h3>
        <select value={filter} onChange={(e) => setFilter(e.target.value)} className="border rounded px-2 py-1 text-xs">
          <option value="pending">Pending</option>
          <option value="approved">Approved</option>
          <option value="rejected">Rejected</option>
          <option value="">All</option>
        </select>
      </div>
      {changes.length === 0 ? (
        <p className="text-sm text-gray-400">No changes</p>
      ) : (
        <div className="space-y-2">
          {changes.map((ch) => (
            <div key={ch.id} className="border rounded p-2 text-sm">
              <div className="flex items-center justify-between">
                <span className="font-medium">{ch.description || ch.id}</span>
                <span className={`text-xs px-1.5 py-0.5 rounded ${
                  ch.status === 'approved' ? 'bg-green-100 text-green-700' :
                  ch.status === 'rejected' ? 'bg-red-100 text-red-700' :
                  'bg-yellow-100 text-yellow-700'
                }`}>{ch.status}</span>
              </div>
              <div className="text-xs text-gray-400 mt-1">
                v{ch.version} by {ch.agent_id}
              </div>
              {ch.status === 'pending' && (
                <div className="flex gap-2 mt-2">
                  <button onClick={() => handleReview(ch.id, true)} className="text-xs bg-green-500 text-white px-2 py-1 rounded hover:bg-green-600">
                    Approve
                  </button>
                  <button onClick={() => handleReview(ch.id, false)} className="text-xs bg-red-500 text-white px-2 py-1 rounded hover:bg-red-600">
                    Reject
                  </button>
                </div>
              )}
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

function TaskActions({ projectId: _projectId }: { projectId: string }) {
  const [expanded, setExpanded] = useState(false)
  const project = useAppStore((s) => s.project)
  const { refreshState } = useDashboard()

  const handleClaim = async (taskId: string) => {
    await taskApi.claim(taskId)
    refreshState()
  }

  const handleComplete = async (taskId: string) => {
    await taskApi.complete(taskId)
    refreshState()
  }

  if (!project) return null
  const pendingTasks = project.tasks.filter((t) => t.status === 'pending')
  const myTasks = project.tasks.filter((t) => t.status === 'claimed')

  return (
    <div className="bg-white rounded-lg shadow p-4 mb-4">
      <div className="flex items-center justify-between mb-2 cursor-pointer" onClick={() => setExpanded(!expanded)}>
        <h3 className="text-sm font-semibold text-gray-500 uppercase">Quick Actions</h3>
        <span className="text-gray-400">{expanded ? '\u25B2' : '\u25BC'}</span>
      </div>
      {expanded && (
        <div className="space-y-2">
          {pendingTasks.length > 0 && (
            <div>
              <p className="text-xs text-gray-500 mb-1">Claim a task:</p>
              {pendingTasks.slice(0, 5).map((t) => (
                <div key={t.id} className="flex items-center justify-between text-sm mb-1">
                  <span className="truncate">{t.name}</span>
                  <button onClick={() => handleClaim(t.id)} className="text-xs bg-blue-500 text-white px-2 py-1 rounded hover:bg-blue-600">
                    Claim
                  </button>
                </div>
              ))}
            </div>
          )}
          {myTasks.length > 0 && (
            <div>
              <p className="text-xs text-gray-500 mb-1">Complete a task:</p>
              {myTasks.map((t) => (
                <div key={t.id} className="flex items-center justify-between text-sm mb-1">
                  <span className="truncate">{t.name}</span>
                  <button onClick={() => handleComplete(t.id)} className="text-xs bg-green-500 text-white px-2 py-1 rounded hover:bg-green-600">
                    Done
                  </button>
                </div>
              ))}
            </div>
          )}
        </div>
      )}
    </div>
  )
}

function RollbackModal({ onClose, projectId }: { onClose: () => void; projectId: string }) {
  const [versions, setVersions] = useState<string[]>([])
  const [selectedVersion, setSelectedVersion] = useState('')
  const [reason, setReason] = useState('')
  const [loading, setLoading] = useState(false)
  const [result, setResult] = useState<any>(null)

  useEffect(() => {
    const loadVersions = async () => {
      const res = await versionApi.list(projectId)
      if (res.success) {
        setVersions(res.data.versions || [])
      }
    }
    loadVersions()
  }, [projectId])

  const handleRollback = async () => {
    if (!selectedVersion) return
    setLoading(true)
    const res = await versionApi.rollback(projectId, selectedVersion, reason)
    setLoading(false)
    if (res.success) {
      setResult(res.data)
    }
  }

  return (
    <div className="fixed inset-0 bg-black/50 flex items-center justify-center z-50">
      <div className="bg-white rounded-lg shadow-xl p-6 w-96">
        {result ? (
          <>
            <h3 className="font-bold text-lg mb-4 text-green-600">Rollback Successful!</h3>
            <div className="space-y-2 text-sm">
              <p><strong>Version:</strong> {result.version}</p>
            </div>
            <button onClick={onClose} className="mt-4 w-full bg-blue-500 text-white py-2 rounded hover:bg-blue-600">
              Close
            </button>
          </>
        ) : (
          <>
            <h3 className="font-bold text-lg mb-4">Rollback Version</h3>
            {versions.length === 0 ? (
              <p className="text-sm text-gray-400 mb-4">No versions available for rollback</p>
            ) : (
              <select value={selectedVersion} onChange={(e) => setSelectedVersion(e.target.value)}
                className="w-full border rounded px-3 py-2 text-sm mb-3">
                <option value="">Select version...</option>
                {versions.map((v) => (
                  <option key={v} value={v}>{v}</option>
                ))}
              </select>
            )}
            <textarea
              value={reason}
              onChange={(e) => setReason(e.target.value)}
              placeholder="Reason for rollback (optional)"
              className="w-full border rounded px-3 py-2 text-sm mb-3 h-20 resize-none"
            />
            <div className="flex gap-2">
              <button onClick={handleRollback} disabled={!selectedVersion || loading}
                className="flex-1 bg-red-500 text-white py-2 rounded text-sm hover:bg-red-600 disabled:bg-gray-300">
                {loading ? 'Rolling back...' : 'Rollback'}
              </button>
              <button onClick={onClose} className="flex-1 border py-2 rounded text-sm hover:bg-gray-50">
                Cancel
              </button>
            </div>
          </>
        )}
      </div>
    </div>
  )
}

function MilestoneSwitchModal({ onClose, projectId }: { onClose: () => void; projectId: string }) {
  const [loading, setLoading] = useState(false)
  const [result, setResult] = useState<any>(null)
  const project = useAppStore((s) => s.project)
  const { refreshState } = useDashboard()

  const handleSwitch = async () => {
    setLoading(true)
    const res = await milestoneApi.switch(projectId)
    setLoading(false)
    if (res.success) {
      setResult(res.data)
      refreshState()
    }
  }

  if (result) {
    return (
      <div className="fixed inset-0 bg-black/50 flex items-center justify-center z-50">
        <div className="bg-white rounded-lg shadow-xl p-6 w-96">
          <h3 className="font-bold text-lg mb-4 text-green-600">Milestone Switched!</h3>
          <div className="space-y-2 text-sm">
            <p><strong>New Milestone:</strong> {result.new_milestone}</p>
            <p><strong>New Version:</strong> {result.new_version}</p>
          </div>
          <button onClick={onClose} className="mt-4 w-full bg-blue-500 text-white py-2 rounded hover:bg-blue-600">
            Close
          </button>
        </div>
      </div>
    )
  }

  return (
    <div className="fixed inset-0 bg-black/50 flex items-center justify-center z-50">
      <div className="bg-white rounded-lg shadow-xl p-6 w-96">
        <h3 className="font-bold text-lg mb-4">Switch Milestone</h3>
        <p className="text-sm text-gray-600 mb-4">
          This will archive the current milestone and create a new one. All completed tasks will be archived.
        </p>
        {project && (
          <div className="text-sm mb-4">
            <p><strong>Current:</strong> {project.milestone}</p>
            <p><strong>Version:</strong> {project.version}</p>
            <p><strong>Tasks completed:</strong> {project.tasks.filter((t) => t.status === 'completed').length}</p>
          </div>
        )}
        <div className="flex gap-2">
          <button onClick={handleSwitch} disabled={loading}
            className="flex-1 bg-green-500 text-white py-2 rounded text-sm hover:bg-green-600 disabled:bg-gray-300">
            {loading ? 'Switching...' : 'Confirm Switch'}
          </button>
          <button onClick={onClose} className="flex-1 border py-2 rounded text-sm hover:bg-gray-50">
            Cancel
          </button>
        </div>
      </div>
    </div>
  )
}

function ChatPanel() {
  const { chatMessages, inputText, targetBlock, setInputText, addChatMessage, setTargetBlock, pendingInput, setPendingInput, selectedProjectId, sessionId } = useAppStore()

  const handleSend = async () => {
    if (!inputText.trim() || !selectedProjectId) return
    const msg = inputText.trim()
    setInputText('')
    addChatMessage({ id: Date.now().toString(), role: 'human', content: msg, timestamp: Date.now() })

    try {
      const res = await dashboardApi.input(selectedProjectId, targetBlock, msg)
      if (res.success) {
        const data = res.data
        if (data.requires_confirm || data.status === 'pending_confirmation') {
          addChatMessage({
            id: (Date.now() + 1).toString(),
            role: 'agent',
            content: `Input received. Please confirm to update the ${targetBlock} block.`,
            timestamp: Date.now(),
          })
          setPendingInput({
            input_id: data.input_id,
            target_block: targetBlock,
            content: msg,
            requires_confirm: data.requires_confirm || false,
          })
        } else if (data.session_active) {
          addChatMessage({
            id: (Date.now() + 1).toString(),
            role: 'agent',
            content: 'Task creation request sent to maintain agent.',
            timestamp: Date.now(),
          })
        } else {
          addChatMessage({
            id: (Date.now() + 1).toString(),
            role: 'agent',
            content: 'Input submitted successfully.',
            timestamp: Date.now(),
          })
        }
      }
    } catch {
      addChatMessage({
        id: (Date.now() + 1).toString(),
        role: 'agent',
        content: 'Failed to submit input.',
        timestamp: Date.now(),
      })
    }
  }

  const handleConfirm = async () => {
    if (!pendingInput || !selectedProjectId) return
    try {
      const res = await dashboardApi.confirm(selectedProjectId, pendingInput.input_id, true)
      if (res.success) {
        addChatMessage({
          id: Date.now().toString(),
          role: 'system',
          content: `${pendingInput.target_block} block updated and confirmed.`,
          timestamp: Date.now(),
        })
      }
    } catch {
      addChatMessage({
        id: Date.now().toString(),
        role: 'system',
        content: 'Failed to confirm update.',
        timestamp: Date.now(),
      })
    }
    setPendingInput(null)
  }

  const handleCancel = async () => {
    if (!pendingInput || !selectedProjectId) return
    try {
      await dashboardApi.confirm(selectedProjectId, pendingInput.input_id, false)
    } catch {}
    addChatMessage({
      id: Date.now().toString(),
      role: 'system',
      content: 'Update cancelled.',
      timestamp: Date.now(),
    })
    setPendingInput(null)
  }

  const handleClearChat = async () => {
    if (sessionId) {
      try {
        await dashboardApi.clearContext(sessionId)
      } catch {}
    }
    useAppStore.getState().clearChat()
    setPendingInput(null)
  }

  return (
    <div className="flex flex-col h-full">
      <div className="flex items-center gap-3 mb-4">
        <select
          value={targetBlock}
          onChange={(e) => setTargetBlock(e.target.value as any)}
          className="border rounded px-3 py-1.5 text-sm"
        >
          <option value="direction">Direction</option>
          <option value="milestone">Milestone</option>
          <option value="task">Task</option>
        </select>
        <button
          onClick={handleClearChat}
          className="text-xs text-gray-400 hover:text-gray-600"
        >
          Clear context
        </button>
      </div>

      <div className="flex-1 overflow-y-auto mb-4 space-y-3">
        {chatMessages.map((m) => (
          <div key={m.id} className={`text-sm ${m.role === 'human' ? 'text-right' : m.role === 'system' ? 'text-center' : 'text-left'}`}>
            <span
              className={`inline-block max-w-[80%] px-3 py-2 rounded-lg ${
                m.role === 'human' ? 'bg-blue-500 text-white' :
                m.role === 'system' ? 'bg-yellow-50 text-yellow-700 border border-yellow-200' :
                'bg-gray-100 text-gray-700'
              }`}
            >
              {m.content}
            </span>
          </div>
        ))}
        {chatMessages.length === 0 && (
          <div className="text-center text-gray-400 text-sm py-8">
            Select a target block and start a conversation with the maintain agent.
          </div>
        )}
      </div>

      {pendingInput && (
        <div className="bg-blue-50 border border-blue-200 rounded-lg p-3 mb-3">
          <p className="text-sm text-blue-700 mb-2">Confirm update to <strong>{pendingInput.target_block}</strong> block?</p>
          <p className="text-xs text-blue-600 mb-2">{pendingInput.content}</p>
          <div className="flex gap-2">
            <button onClick={handleConfirm} className="bg-blue-500 text-white px-3 py-1 rounded text-xs hover:bg-blue-600">
              Confirm Update
            </button>
            <button onClick={handleCancel} className="border px-3 py-1 rounded text-xs hover:bg-gray-50">
              Cancel
            </button>
          </div>
        </div>
      )}

      <div className="flex gap-2">
        <input
          type="text"
          value={inputText}
          onChange={(e) => setInputText(e.target.value)}
          onKeyDown={(e) => e.key === 'Enter' && handleSend()}
          placeholder="Type a message..."
          className="flex-1 border rounded px-3 py-2 text-sm"
        />
        <button
          onClick={handleSend}
          className="bg-blue-500 text-white px-4 py-2 rounded text-sm hover:bg-blue-600"
        >
          Send
        </button>
      </div>
    </div>
  )
}

function RegisterModal({ onClose }: { onClose: () => void }) {
  const [name, setName] = useState('')
  const [projectId, setProjectId] = useState('')
  const [result, setResult] = useState<any>(null)
  const [projects, setProjects] = useState<any[]>([])

  useEffect(() => {
    const loadProjects = async () => {
      const res = await projectApi.list()
      if (res.success) {
        setProjects(res.data || [])
      }
    }
    loadProjects()
  }, [])

  const handleRegister = async () => {
    if (!name.trim()) return
    const res = await authApi.register(name, projectId || undefined)
    if (res.success) {
      setResult(res.data)
    }
  }

  return (
    <div className="fixed inset-0 bg-black/50 flex items-center justify-center z-50">
      <div className="bg-white rounded-lg shadow-xl p-6 w-96">
        {result ? (
          <>
            <h3 className="font-bold text-lg mb-4 text-green-600">Agent Registered!</h3>
            <div className="space-y-2 text-sm">
              <p><strong>Agent ID:</strong> {result.agent_id}</p>
              <p><strong>Name:</strong> {result.name}</p>
              <div className="bg-yellow-50 border border-yellow-300 p-3 rounded">
                <p className="font-bold text-yellow-800">Access Key (save this!):</p>
                <p className="font-mono text-yellow-900 break-all">{result.access_key}</p>
              </div>
            </div>
            <button onClick={onClose} className="mt-4 w-full bg-blue-500 text-white py-2 rounded hover:bg-blue-600">
              Close
            </button>
          </>
        ) : (
          <>
            <h3 className="font-bold text-lg mb-4">Register New Agent</h3>
            <input
              type="text"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="Agent name"
              className="w-full border rounded px-3 py-2 text-sm mb-3"
            />
            {projects.length > 0 && (
              <select value={projectId} onChange={(e) => setProjectId(e.target.value)}
                className="w-full border rounded px-3 py-2 text-sm mb-4">
                <option value="">No project (assign later)</option>
                {projects.map((p) => (
                  <option key={p.id} value={p.id}>{p.name}</option>
                ))}
              </select>
            )}
            <div className="flex gap-2">
              <button onClick={handleRegister} className="flex-1 bg-blue-500 text-white py-2 rounded text-sm hover:bg-blue-600">
                Register
              </button>
              <button onClick={onClose} className="flex-1 border py-2 rounded text-sm hover:bg-gray-50">
                Cancel
              </button>
            </div>
          </>
        )}
      </div>
    </div>
  )
}

function CreateProjectModal({ onClose }: { onClose: () => void }) {
  const [name, setName] = useState('')
  const [desc, setDesc] = useState('')
  const [githubRepo, setGithubRepo] = useState('')
  const [importExisting, setImportExisting] = useState(false)
  const [result, setResult] = useState<any>(null)

  const handleCreate = async () => {
    if (!name.trim()) return
    const res = await projectApi.create(name, desc, githubRepo || undefined, importExisting)
    if (res.success) {
      setResult(res.data)
    }
  }

  return (
    <div className="fixed inset-0 bg-black/50 flex items-center justify-center z-50">
      <div className="bg-white rounded-lg shadow-xl p-6 w-96">
        {result ? (
          <>
            <h3 className="font-bold text-lg mb-4 text-green-600">Project Created!</h3>
            <div className="space-y-2 text-sm">
              <p><strong>ID:</strong> {result.id}</p>
              <p><strong>Name:</strong> {result.name}</p>
              <p><strong>Status:</strong> {result.status}</p>
              {importExisting && <p className="text-yellow-600">Assessment will run automatically.</p>}
            </div>
            <button onClick={onClose} className="mt-4 w-full bg-blue-500 text-white py-2 rounded hover:bg-blue-600">
              Close
            </button>
          </>
        ) : (
          <>
            <h3 className="font-bold text-lg mb-4">Create New Project</h3>
            <input
              type="text"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="Project name"
              className="w-full border rounded px-3 py-2 text-sm mb-3"
            />
            <textarea
              value={desc}
              onChange={(e) => setDesc(e.target.value)}
              placeholder="Description (optional)"
              className="w-full border rounded px-3 py-2 text-sm mb-3 h-20 resize-none"
            />
            <div className="flex items-center gap-2 mb-3">
              <input
                type="checkbox"
                id="importExisting"
                checked={importExisting}
                onChange={(e) => setImportExisting(e.target.checked)}
                className="rounded"
              />
              <label htmlFor="importExisting" className="text-sm text-gray-600">Import existing project</label>
            </div>
            {importExisting && (
              <input
                type="text"
                value={githubRepo}
                onChange={(e) => setGithubRepo(e.target.value)}
                placeholder="GitHub repository URL"
                className="w-full border rounded px-3 py-2 text-sm mb-3"
              />
            )}
            <div className="flex gap-2">
              <button onClick={handleCreate} className="flex-1 bg-blue-500 text-white py-2 rounded text-sm hover:bg-blue-600">
                Create
              </button>
              <button onClick={onClose} className="flex-1 border py-2 rounded text-sm hover:bg-gray-50">
                Cancel
              </button>
            </div>
          </>
        )}
      </div>
    </div>
  )
}

function LoginPanel({ onLogin }: { onLogin: (project: any) => void }) {
  const [key, setKey] = useState('')
  const [projects, setProjects] = useState<any[]>([])
  const [selectedProject, setSelectedProject] = useState('')

  const handleLogin = async () => {
    if (!key.trim()) return
    const res = await authApi.login(key, selectedProject || undefined)
    if (res.success) {
      localStorage.setItem('a3c_access_key', key)
      useAppStore.getState().setAccessKey(key)
      if (res.data.projects) {
        setProjects(res.data.projects)
        if (res.data.projects.length === 1) {
          setSelectedProject(res.data.projects[0].id)
        }
      }
      if (res.data.project_context) {
        onLogin(res.data)
      }
    }
  }

  return (
    <div className="min-h-screen bg-gray-50 flex items-center justify-center">
      <div className="bg-white rounded-lg shadow-lg p-8 w-96">
        <h1 className="text-2xl font-bold mb-6 text-center">A3C Dashboard</h1>
        <input
          type="text"
          value={key}
          onChange={(e) => setKey(e.target.value)}
          placeholder="Access Key"
          className="w-full border rounded px-3 py-2 text-sm mb-3"
        />
        <button
          onClick={handleLogin}
          className="w-full bg-blue-500 text-white py-2 rounded text-sm hover:bg-blue-600 mb-3"
        >
          Login
        </button>
        {projects.length > 0 && !selectedProject && (
          <div className="space-y-2">
            <p className="text-sm text-gray-600">Select a project:</p>
            {projects.map((p) => (
              <button
                key={p.id}
                onClick={() => setSelectedProject(p.id)}
                className="w-full text-left border rounded px-3 py-2 text-sm hover:bg-gray-50"
              >
                {p.name}
              </button>
            ))}
          </div>
        )}
      </div>
    </div>
  )
}

export default function Dashboard() {
  const { project, accessKey, selectedProjectId } = useAppStore()
  const { refreshState } = useDashboard()
  const [showRegister, setShowRegister] = useState(false)
  const [showCreateProject, setShowCreateProject] = useState(false)
  const [showCreateTask, setShowCreateTask] = useState(false)
  const [showRollback, setShowRollback] = useState(false)
  const [showMilestoneSwitch, setShowMilestoneSwitch] = useState(false)
  const [loggedIn, setLoggedIn] = useState(false)
  useSSE(selectedProjectId)

  useEffect(() => {
    if (selectedProjectId && loggedIn) {
      refreshState()
      const interval = setInterval(refreshState, 10000)
      return () => clearInterval(interval)
    }
  }, [selectedProjectId, loggedIn])

  if (!accessKey || !loggedIn) {
    return <LoginPanel onLogin={() => setLoggedIn(true)} />
  }

  if (!selectedProjectId) {
    return (
      <div className="min-h-screen bg-gray-50 p-8">
        <div className="max-w-2xl mx-auto">
          <h1 className="text-2xl font-bold mb-6">Select or Create a Project</h1>
          <button
            onClick={() => setShowCreateProject(true)}
            className="bg-blue-500 text-white px-4 py-2 rounded text-sm hover:bg-blue-600"
          >
            Create Project
          </button>
          {showCreateProject && <CreateProjectModal onClose={() => setShowCreateProject(false)} />}
        </div>
      </div>
    )
  }

  return (
    <div className="min-h-screen bg-gray-100">
      <header className="bg-white shadow">
        <div className="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-4 flex items-center justify-between">
          <h1 className="text-xl font-bold text-gray-900">A3C Dashboard</h1>
          <div className="flex items-center gap-4">
            {project?.agents.map((a) => (
              <span key={a.id} className="flex items-center gap-1 text-sm text-gray-600">
                <span className="w-2 h-2 bg-green-400 rounded-full"></span>
                {a.name}
              </span>
            ))}
            <button
              onClick={() => setShowRegister(true)}
              className="text-sm text-blue-500 hover:text-blue-600"
            >
              + Agent
            </button>
            <button
              onClick={() => setShowCreateTask(true)}
              className="text-sm text-blue-500 hover:text-blue-600"
            >
              + Task
            </button>
            <button
              onClick={() => {
                localStorage.removeItem('a3c_access_key')
                useAppStore.getState().setAccessKey(null)
                setLoggedIn(false)
              }}
              className="text-sm text-gray-400 hover:text-gray-600"
            >
              Logout
            </button>
          </div>
        </div>
      </header>

      <div className="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-6">
        <div className="grid grid-cols-1 lg:grid-cols-5 gap-6">
          <div className="lg:col-span-2 space-y-0">
            <DirectionCard />
            <MilestoneCard onSwitch={() => setShowMilestoneSwitch(true)} />
            <LocksCard />
            <VersionCard onRollback={() => setShowRollback(true)} />
            <AgentsCard />
            <TaskActions projectId={selectedProjectId} />
            <ChangesPanel projectId={selectedProjectId} />
          </div>
          <div className="lg:col-span-3">
            <div className="bg-white rounded-lg shadow p-4 h-[calc(100vh-12rem)] flex flex-col">
              <ChatPanel />
            </div>
          </div>
        </div>
      </div>

      {showRegister && <RegisterModal onClose={() => setShowRegister(false)} />}
      {showCreateTask && selectedProjectId && <CreateTaskModal onClose={() => setShowCreateTask(false)} projectId={selectedProjectId} />}
      {showRollback && selectedProjectId && <RollbackModal onClose={() => setShowRollback(false)} projectId={selectedProjectId} />}
      {showMilestoneSwitch && selectedProjectId && <MilestoneSwitchModal onClose={() => setShowMilestoneSwitch(false)} projectId={selectedProjectId} />}
    </div>
  )
}