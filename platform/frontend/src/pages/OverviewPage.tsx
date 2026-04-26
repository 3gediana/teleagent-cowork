import { useEffect, useState } from 'react'
import { useAppStore } from '../stores/appStore'
import { useDashboard } from '../hooks/useDashboard'
import { useSSE } from '../hooks/useSSE'
import { InfoCard, VersionCard, AgentsCard, LocksCard } from '../components/InfoCards'
import { ChatPanel } from '../components/ChatPanel'
import { ChiefQueueCompact } from '../components/ChiefQueueCompact'
import { Modal, SuccessResult, Select, Textarea, Button, useModal } from '../components/Modal'
import { versionApi, milestoneApi, taskApi } from '../api/endpoints'
import { TaskKanban } from '../components/TaskKanban'

/**
 * OverviewPage — the flagship screen.
 *
 * Layout: three columns inside the dark shell.
 *   · Left  (280px): stacked project-context cards — Direction,
 *                    Milestone, Version, Agents, Locks, Chief queue.
 *   · Center (flex): the dark felt board framed by a 1px metallic
 *                    bevel, holding the TaskKanban.
 *   · Right (420px): the Maintain Agent chat surface.
 */

function IconTarget() {
  return (
    <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8">
      <circle cx="12" cy="12" r="9"/><circle cx="12" cy="12" r="5"/><circle cx="12" cy="12" r="1.5" fill="currentColor"/>
    </svg>
  )
}
function IconFlag() {
  return (
    <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8">
      <path d="M4 4v17"/><path d="M4 4h13l-3 5 3 5H4"/>
    </svg>
  )
}

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
    <div className="flex gap-5 h-[calc(100vh-3.5rem)] overflow-hidden p-5">
      {/* ═══ LEFT — project context ═══ */}
      <div className="w-[280px] flex flex-col gap-3 shrink-0 overflow-y-auto custom-scrollbar pr-1 pb-2">
        <InfoCard
          title="Direction"
          icon={<IconTarget />}
          value={project?.direction ?? null}
          accentColor="blue"
        />
        <InfoCard
          title="Milestone"
          icon={<IconFlag />}
          value={project?.milestone ?? null}
          onEdit={() => milestoneSwitchModal.openModal()}
          accentColor="amber"
        />
        <VersionCard version={project?.version || 'v1.0'} onRollback={() => rollbackModal.openModal()} />
        <AgentsCard />
        <LocksCard />
        <ChiefQueueCompact />
      </div>

      {/* ═══ CENTER — the felt board ═══ */}
      <div className="flex-1 min-w-0 flex flex-col">
        <div className="board-frame h-full">
          <div className="felt-board h-full rounded-[13px] overflow-hidden">
            <TaskKanban onClaim={handleClaim} onComplete={handleComplete} />
          </div>
        </div>
      </div>

      {/* ═══ RIGHT — Maintain chat ═══ */}
      <div className="w-[420px] surface-1 flex flex-col shrink-0 overflow-hidden">
        <div className="px-4 py-3 flex items-center justify-between shrink-0 border-b border-[#27272a]">
          <div className="flex items-center gap-2">
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="#a5b4fc" strokeWidth="1.8">
              <path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z"/>
            </svg>
            <h2 className="text-[13px] font-semibold text-white tracking-tight">Maintain Agent</h2>
          </div>
          <div className={`chip ${isConnected ? 'chip-green' : 'chip-rose'}`}>
            <span
              className="w-1.5 h-1.5 rounded-full"
              style={{
                background: isConnected ? '#10b981' : '#f43f5e',
                boxShadow: isConnected ? '0 0 6px #10b981' : '0 0 6px #f43f5e',
                animation: isConnected ? 'status-pulse 2s ease-in-out infinite' : undefined,
              }}
            />
            <span className="text-[10.5px] font-medium uppercase tracking-[0.08em]">
              {isConnected ? 'Live' : 'Offline'}
            </span>
          </div>
        </div>
        <div className="flex-1 min-h-0 p-3">
          <ChatPanel />
        </div>
      </div>

      {rollbackModal.open && selectedProjectId && <RollbackModal onClose={rollbackModal.closeModal} projectId={selectedProjectId} />}
      {milestoneSwitchModal.open && selectedProjectId && <MilestoneSwitchModal onClose={milestoneSwitchModal.closeModal} projectId={selectedProjectId} />}
    </div>
  )
}
