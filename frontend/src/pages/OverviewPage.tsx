import { useEffect, useState } from 'react'
import { useAppStore } from '../stores/appStore'
import { useDashboard } from '../hooks/useDashboard'
import { useSSE } from '../hooks/useSSE'
import { InfoCard, VersionCard, AgentsCard, LocksCard } from '../components/InfoCards'
import { ChatPanel } from '../components/ChatPanel'
import { Modal, SuccessResult, Select, Textarea, Button, useModal } from '../components/Modal'
import { versionApi, milestoneApi, taskApi } from '../api/endpoints'
import { TaskKanban } from '../components/TaskKanban'

function RollbackModal({ onClose, projectId }: { onClose: () => void; projectId: string }) {
  const [versions, setVersions] = useState<string[]>([])
  const [selected, setSelected] = useState('')
  const [reason, setReason] = useState('')
  const [loading, setLoading] = useState(false)
  const [result, setResult] = useState<any>(null)

  useEffect(() => {
    versionApi.list(projectId).then((res) => {
      if (res.success) setVersions(res.data.versions || [])
    })
  }, [projectId])

  const handleRollback = async () => {
    if (!selected) return
    setLoading(true)
    const res = await versionApi.rollback(projectId, selected, reason)
    setLoading(false)
    if (res.success) setResult(res.data)
  }

  return (
    <Modal title="Rollback Version" icon="⏪" onClose={onClose}>
      {result ? (
        <SuccessResult title="Rollback Successful!" data={{ version: result.version }} onClose={onClose} />
      ) : (
        <div className="space-y-4">
          <Select label="Version" value={selected} onChange={(e) => setSelected(e.target.value)} options={[
            { value: '', label: 'Select version...' },
            ...versions.map((v) => ({ value: v, label: v })),
          ]} />
          <Textarea label="Reason" value={reason} onChange={(e) => setReason(e.target.value)} placeholder="Optional reason" rows={2} />
          <div className="flex gap-3 pt-2">
            <Button variant="danger" className="flex-1" onClick={handleRollback} disabled={!selected || loading}>
              {loading ? 'Rolling back...' : 'Rollback'}
            </Button>
            <Button variant="secondary" className="flex-1" onClick={onClose}>Cancel</Button>
          </div>
        </div>
      )}
    </Modal>
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

  return (
    <Modal title="Switch Milestone" icon="🏁" onClose={onClose}>
      {result ? (
        <SuccessResult title="Milestone Switched!" data={{ 'New Milestone': result.new_milestone, 'New Version': result.new_version }} onClose={onClose} />
      ) : (
        <div className="space-y-4">
          <p className="text-sm text-slate-500 font-medium">Archive current milestone and start a new development cycle.</p>
          {project && (
            <div className="bg-slate-50 border border-slate-100 rounded-xl p-4 space-y-2 text-sm shadow-inner">
              <p className="flex justify-between"><span className="text-slate-400 font-bold uppercase text-[10px]">Current:</span> <span className="text-slate-800 font-bold">{project.milestone}</span></p>
              <p className="flex justify-between"><span className="text-slate-400 font-bold uppercase text-[10px]">Version:</span> <span className="text-blue-600 font-mono font-bold">{project.version}</span></p>
              <p className="flex justify-between"><span className="text-slate-400 font-bold uppercase text-[10px]">Progress:</span> <span className="text-emerald-600 font-bold">{project.tasks.filter((t) => t.status === 'completed').length} completed</span></p>
            </div>
          )}
          <div className="flex gap-3 pt-2">
            <Button className="flex-1" onClick={handleSwitch} disabled={loading}>{loading ? 'Switching...' : 'Confirm Switch'}</Button>
            <Button variant="secondary" className="flex-1" onClick={onClose}>Cancel</Button>
          </div>
        </div>
      )}
    </Modal>
  )
}

export default function OverviewPage() {
  const { project, selectedProjectId } = useAppStore()
  const { refreshState, isConnected } = useSSE(selectedProjectId)
  
  const rollbackModal = useModal()
  const milestoneSwitchModal = useModal()

  useEffect(() => {
    if (selectedProjectId) {
      refreshState()
      const interval = setInterval(refreshState, 10000)
      return () => clearInterval(interval)
    }
  }, [selectedProjectId])

  const handleClaim = async (taskId: string) => {
    await taskApi.claim(taskId)
    refreshState()
  }

  const handleComplete = async (taskId: string) => {
    await taskApi.complete(taskId)
    refreshState()
  }

  return (
    <div className="flex gap-8 h-[calc(100vh-10rem)] overflow-hidden">
      {/* ... Left Column ... */}
      <div className="w-[300px] flex flex-col gap-6 shrink-0 overflow-y-auto custom-scrollbar pr-2 pb-4">
        {/* ... Direction, Milestone, Version, Agents, Locks ... */}
        <div className="p-1">
          <InfoCard
            title="Direction"
            icon="🎯"
            value={project?.direction ?? null}
            accentColor="blue"
          />
        </div>
        <div className="p-1">
          <InfoCard
            title="Milestone"
            icon="🏁"
            value={project?.milestone ?? null}
            onEdit={() => milestoneSwitchModal.openModal()}
            accentColor="amber"
          />
        </div>
        <div className="px-1">
          <VersionCard version={project?.version || 'v1.0'} onRollback={() => rollbackModal.openModal()} />
        </div>
        <div className="px-1">
          <AgentsCard />
        </div>
        <div className="px-1">
          <LocksCard />
        </div>
      </div>

      {/* Center: The Board */}
      <div className="flex-1 min-w-0 flex flex-col">
        <div className="flex-1 min-h-0 wood-board rounded-[40px] border-[10px] border-[#2d1b0f] shadow-2xl p-6 overflow-hidden">
          <TaskKanban onClaim={handleClaim} onComplete={handleComplete} />
        </div>
      </div>

      {/* Right: The Brain (Chat) */}
      <div className="w-[450px] parchment rounded-[32px] border border-[#8b4513]/20 shadow-2xl p-6 flex flex-col shrink-0 overflow-hidden">
        <div className="flex items-center justify-between mb-6 shrink-0 px-2">
          <h2 className="text-xl font-marker text-[#5d4037] flex items-center gap-2 -rotate-1">
            💬 Maintain Agent
          </h2>
          <div className="flex items-center gap-2">
            <span className={`w-2 h-2 rounded-full ${isConnected ? 'bg-emerald-600 animate-pulse' : 'bg-rose-500'} shadow-[0_0_8px_rgba(16,185,129,0.5)]`} />
            <span className={`text-[10px] font-bold ${isConnected ? 'text-[#8b4513]/40' : 'text-rose-500'} uppercase tracking-widest`}>
              {isConnected ? 'Active' : 'Offline'}
            </span>
          </div>
        </div>
        <div className="flex-1 min-h-0">
          <ChatPanel />
        </div>
      </div>

      {rollbackModal.open && selectedProjectId && <RollbackModal onClose={rollbackModal.closeModal} projectId={selectedProjectId} />}
      {milestoneSwitchModal.open && selectedProjectId && <MilestoneSwitchModal onClose={milestoneSwitchModal.closeModal} projectId={selectedProjectId} />}
    </div>
  )
}
