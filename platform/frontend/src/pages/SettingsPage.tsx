import { useState, useEffect } from 'react'
import { projectApi, authApi, roleApi, providerApi, llmApi } from '../api/endpoints'
import { Modal, Input, Select, Button, SuccessResult, useModal, Textarea } from '../components/Modal'
import { useAppStore } from '../stores/appStore'

function RegisterModal({ onClose }: { onClose: () => void }) {
  const [name, setName] = useState('')
  const [projectId, setProjectId] = useState('')
  const [result, setResult] = useState<any>(null)
  const [projects, setProjects] = useState<any[]>([])

  useEffect(() => {
    projectApi.list().then((res) => {
      if (res.success) setProjects(res.data || [])
    })
  }, [])

  const handleRegister = async () => {
    if (!name.trim()) return
    const res = await authApi.register(name, projectId || undefined)
    if (res.success) setResult(res.data)
  }

  return (
    <Modal title="Register Agent" icon="🤖" onClose={onClose}>
      {result ? (
        <div className="text-center py-2">
          <div className="text-5xl mb-4">🎉</div>
          <h3 className="text-lg font-extrabold text-emerald-600 mb-5">Agent Registered!</h3>
          <div className="text-left bg-slate-50 border border-slate-100 rounded-xl p-5 mb-6 space-y-3 shadow-inner">
            <p className="text-sm flex flex-col gap-1">
              <span className="text-slate-500 font-bold uppercase tracking-wider text-xs">Agent ID</span>
              <span className="text-slate-800 font-mono bg-white p-2 rounded border border-slate-200 shadow-sm">{result.agent_id}</span>
            </p>
            <div className="bg-amber-50 border border-amber-200 rounded-xl p-4">
              <p className="text-xs text-amber-700 font-extrabold uppercase tracking-widest mb-2">Access Key (CRITICAL)</p>
              <p className="text-sm text-amber-900 font-mono break-all bg-white/50 p-2 rounded border border-amber-100 select-all">{result.access_key}</p>
              <p className="text-[10px] text-amber-600 mt-2 font-medium">⚠️ Copy this key now. You will not be able to see it again.</p>
            </div>
          </div>
          <Button className="w-full" onClick={onClose}>Done</Button>
        </div>
      ) : (
        <div className="space-y-4">
          <Input label="Agent Name" value={name} onChange={(e) => setName(e.target.value)} placeholder="e.g. CodeArchitect" />
          {projects.length > 0 && (
            <Select label="Assign to Project" value={projectId} onChange={(e) => setProjectId(e.target.value)} options={[
              { value: '', label: 'No initial assignment' },
              ...projects.map((p) => ({ value: p.id, label: p.name })),
            ]} />
          )}
          <div className="flex gap-3 pt-2">
            <Button className="flex-1" onClick={handleRegister}>Register Agent</Button>
            <Button variant="secondary" className="flex-1" onClick={onClose}>Cancel</Button>
          </div>
        </div>
      )}
    </Modal>
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
    if (res.success) setResult(res.data)
  }

  return (
    <Modal title="New Project" icon="📁" onClose={onClose}>
      {result ? (
        <SuccessResult title="Project Created!" data={{ id: result.id, name: result.name, status: result.status }} onClose={onClose} />
      ) : (
        <div className="space-y-4">
          <Input label="Project Name" value={name} onChange={(e) => setName(e.target.value)} placeholder="e.g. Project Apollo" />
          <Textarea label="Short Description" value={desc} onChange={(e) => setDesc(e.target.value)} placeholder="What is this project about?" rows={2} />
          <div className="p-4 bg-slate-50 rounded-xl border border-slate-100">
            <label className="flex items-center gap-3 text-sm font-bold text-slate-700 cursor-pointer">
              <input type="checkbox" checked={importExisting} onChange={(e) => setImportExisting(e.target.checked)} className="w-4 h-4 text-blue-600 rounded border-slate-300 focus:ring-blue-500" />
              Import existing source code
            </label>
            {importExisting && (
              <div className="mt-4 animate-in fade-in slide-in-from-top-2">
                <Input label="GitHub Repository URL" value={githubRepo} onChange={(e) => setGithubRepo(e.target.value)} placeholder="https://github.com/user/repo" />
              </div>
            )}
          </div>
          <div className="flex gap-3 pt-2">
            <Button className="flex-1" onClick={handleCreate}>Create Project</Button>
            <Button variant="secondary" className="flex-1" onClick={onClose}>Cancel</Button>
          </div>
        </div>
      )}
    </Modal>
  )
}

export default function SettingsPage() {
  const { setSelectedProjectId } = useAppStore()
  const registerModal = useModal()
  const createProjectModal = useModal()
  const [projects, setProjects] = useState<any[]>([])
  const [roles, setRoles] = useState<any[]>([])
  const [modelOptions, setModelOptions] = useState<any[]>([])
  const [saving, setSaving] = useState<string | null>(null)
  const [searchOpen, setSearchOpen] = useState<string | null>(null) // role being searched
  const [searchQuery, setSearchQuery] = useState('')

  useEffect(() => {
    projectApi.list().then((res) => {
      if (res.success) setProjects(res.data || [])
    })
    roleApi.list().then((res) => {
      if (res.success) setRoles(res.data || [])
    })
    // Merge two sources of truth:
    //   1. opencode-hosted provider catalogue (`providerApi.list()`) —
    //      the legacy path, still serves every agent until Phase 1
    //      native runtime migration completes.
    //   2. user-registered LLM endpoints (`llmApi.list()`) — the new
    //      path from PR 10. These already live in the native Registry;
    //      assigning one to a role persists (provider=endpoint_id,
    //      model=model_id), and the backend dispatcher routes to the
    //      native adapter when provider_id starts with "llm_".
    // Both feeds produce the same ModelOption row shape, so the
    // existing picker UI works unchanged. The `source` tag lets us
    // badge rows so operators can tell the two apart.
    Promise.all([
      providerApi.list().then((res) => (res.success && res.data ? res.data.models || [] : [])),
      llmApi.list().then((res) => {
        if (!res.success) return []
        const out: any[] = []
        for (const ep of res.data?.endpoints || []) {
          if (ep.status !== 'active') continue
          for (const m of ep.models || []) {
            out.push({
              provider_id: ep.id,
              provider_name: ep.name,
              model_id: m.id,
              model_name: m.name || m.id,
              format: ep.format,
              source: 'llm',
            })
          }
        }
        return out
      }).catch(() => []),
    ]).then(([opencode, llm]) => {
      // Prepend user endpoints so they surface first — operators who
      // registered their own endpoint usually want to use it.
      setModelOptions([...llm, ...opencode.map((o: any) => ({ ...o, source: 'opencode' }))])
    })
  }, [])

  const handleModelChange = async (role: string, modelProvider: string, modelId: string) => {
    setSaving(role)
    try {
      await roleApi.updateModel(role, modelProvider, modelId)
      setRoles(prev => prev.map(r => r.role === role ? { ...r, model_provider: modelProvider, model_id: modelId } : r))
    } finally {
      setSaving(null)
    }
  }

  return (
    <div className="max-w-4xl space-y-10">
      <h1 className="text-2xl font-extrabold text-slate-800">Workspace Settings</h1>

      <section>
        <h2 className="text-lg font-bold text-slate-700 mb-4 flex items-center gap-2">
           <span className="p-1.5 bg-purple-100 text-purple-600 rounded-lg">🧠</span> Agent Model Configuration
        </h2>
        <div className="bg-white rounded-3xl border border-slate-200 shadow-sm overflow-hidden">
          <div className="p-6 border-b border-slate-100 space-y-1">
            <p className="text-slate-500 text-sm">Choose which LLM model each platform agent uses. Overrides are persisted across restarts.</p>
            <p className="text-slate-400 text-xs">
              Models tagged <span className="text-[10px] font-bold uppercase tracking-wider bg-emerald-100 text-emerald-700 border border-emerald-300 rounded px-1 py-[1px]">🔌 native</span>
              {' '}come from your{' '}
              <a href="/llm" className="text-blue-600 hover:underline">user-registered endpoints</a>; the rest are served via the opencode provider catalogue.
            </p>
          </div>
          <div className="divide-y divide-slate-50">
            {roles.map(r => {
              const currentModel = r.model_provider && r.model_id
                ? `${r.model_provider}/${r.model_id}`
                : ''
              const isOpen = searchOpen === r.role
              const q = searchQuery.toLowerCase()
              const filtered = q
                ? modelOptions.filter((m: any) =>
                    m.model_name.toLowerCase().includes(q) ||
                    m.model_id.toLowerCase().includes(q) ||
                    m.provider_name.toLowerCase().includes(q) ||
                    m.provider_id.toLowerCase().includes(q)
                  ).slice(0, 50)
                : modelOptions.slice(0, 30)
              return (
                <div key={r.role} className="p-4">
                  <div className="flex items-center justify-between gap-4">
                    <div className="min-w-0 flex-1">
                      <p className="font-bold text-slate-800">{r.name}</p>
                      <p className="text-xs text-slate-400 mt-0.5">{r.description}</p>
                    </div>
                    <div className="flex items-center gap-2">
                      {currentModel ? (
                        <span className="text-xs font-mono bg-slate-100 text-slate-600 px-2 py-1 rounded-lg">{currentModel}</span>
                      ) : (
                        <span className="text-xs text-slate-400 italic">Default</span>
                      )}
                      <button
                        className="text-xs px-3 py-1.5 rounded-lg border border-slate-200 hover:bg-slate-50 text-slate-600 font-medium transition-colors"
                        onClick={() => { setSearchOpen(isOpen ? null : r.role); setSearchQuery('') }}
                      >
                        {isOpen ? 'Close' : 'Change'}
                      </button>
                      {currentModel && (
                        <button
                          className="text-xs px-2 py-1.5 rounded-lg border border-red-200 hover:bg-red-50 text-red-500 font-medium transition-colors"
                          disabled={saving === r.role}
                          onClick={() => handleModelChange(r.role, '', '')}
                        >
                          Reset
                        </button>
                      )}
                      {saving === r.role && <span className="text-xs text-blue-500 animate-pulse">Saving...</span>}
                    </div>
                  </div>
                  {isOpen && (
                    <div className="mt-3 border border-slate-200 rounded-xl overflow-hidden">
                      <input
                        className="w-full px-3 py-2 text-sm border-b border-slate-100 focus:outline-none focus:ring-1 focus:ring-blue-500"
                        placeholder="Search models (e.g. claude, gpt-4, MiniMax)..."
                        value={searchQuery}
                        onChange={(e: any) => setSearchQuery(e.target.value)}
                        autoFocus
                      />
                      <div className="max-h-60 overflow-y-auto">
                        {filtered.length === 0 ? (
                          <div className="px-3 py-4 text-center text-slate-400 text-sm">No models found</div>
                        ) : (
                          filtered.map((m: any) => {
                            const val = `${m.provider_id}/${m.model_id}`
                            const isActive = val === currentModel
                            return (
                              <button
                                key={val}
                                className={`w-full text-left px-3 py-2 text-sm hover:bg-blue-50 transition-colors flex items-center justify-between gap-2 ${isActive ? 'bg-blue-50 text-blue-700' : 'text-slate-700'}`}
                                onClick={() => {
                                  handleModelChange(r.role, m.provider_id, m.model_id)
                                  setSearchOpen(null)
                                  setSearchQuery('')
                                }}
                              >
                                <span className="truncate flex items-center gap-2">
                                  {m.source === 'llm' && (
                                    <span
                                      className="text-[9px] font-bold uppercase tracking-wider bg-emerald-100 text-emerald-700 border border-emerald-300 rounded px-1 py-[1px]"
                                      title={`User-registered ${m.format} endpoint — routed through native runtime`}
                                    >
                                      🔌 {m.format}
                                    </span>
                                  )}
                                  <span className="font-medium">{m.provider_name}</span>
                                  <span className="text-slate-400 mx-1">/</span>
                                  <span>{m.model_name}</span>
                                </span>
                                {isActive && <span className="text-blue-500 text-xs">✓ Active</span>}
                              </button>
                            )
                          })
                        )}
                      </div>
                    </div>
                  )}
                </div>
              )
            })}
            {roles.length === 0 && (
              <div className="p-6 text-center text-slate-400 text-sm">Loading agent roles...</div>
            )}
          </div>
        </div>
      </section>

      <section>
        <h2 className="text-lg font-bold text-slate-700 mb-4 flex items-center gap-2">
           <span className="p-1.5 bg-blue-100 text-blue-600 rounded-lg">🤖</span> Agent Management
        </h2>
        <div className="bg-white rounded-3xl border border-slate-200 shadow-sm p-6">
           <p className="text-slate-500 text-sm mb-6">Register new agents to collaborate on projects. Each agent will receive a unique access key.</p>
           <Button onClick={() => registerModal.openModal()}>+ Register New Agent</Button>
        </div>
      </section>

      <section>
        <h2 className="text-lg font-bold text-slate-700 mb-4 flex items-center gap-2">
           <span className="p-1.5 bg-amber-100 text-amber-600 rounded-lg">📁</span> Project Management
        </h2>
        <div className="bg-white rounded-3xl border border-slate-200 shadow-sm overflow-hidden">
           <div className="p-6 border-b border-slate-100 flex items-center justify-between">
              <p className="text-slate-500 text-sm">Create or switch between active projects.</p>
              <Button onClick={() => createProjectModal.openModal()}>+ Create Project</Button>
           </div>
           <div className="divide-y divide-slate-50">
              {projects.map(p => (
                <div key={p.id} className="p-4 flex items-center justify-between hover:bg-slate-50 transition-colors">
                   <div>
                      <p className="font-bold text-slate-800">{p.name}</p>
                      <p className="text-xs text-slate-400 font-mono mt-0.5">{p.id}</p>
                   </div>
                   <Button variant="secondary" onClick={() => { setSelectedProjectId(p.id); window.location.href = '/' }}>Switch to Project</Button>
                </div>
              ))}
           </div>
        </div>
      </section>

      {registerModal.open && <RegisterModal onClose={registerModal.closeModal} />}
      {createProjectModal.open && <CreateProjectModal onClose={createProjectModal.closeModal} />}
    </div>
  )
}
