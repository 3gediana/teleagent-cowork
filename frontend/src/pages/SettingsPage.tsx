import { useState, useEffect } from 'react'
import { projectApi, authApi } from '../api/endpoints'
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

  useEffect(() => {
    projectApi.list().then((res) => {
      if (res.success) setProjects(res.data || [])
    })
  }, [])

  return (
    <div className="max-w-4xl space-y-10">
      <h1 className="text-2xl font-extrabold text-slate-800">Workspace Settings</h1>

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
