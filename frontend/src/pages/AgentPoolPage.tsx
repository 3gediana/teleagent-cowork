import { useEffect, useState } from 'react'
import { useAppStore } from '../stores/appStore'
import { agentPoolApi, type PoolInstance } from '../api/endpoints'

/**
 * AgentPoolPage — "the pool" is the platform's own stable of client
 * agents. Humans spawn one, the platform stands up an opencode
 * subprocess on the same host, auto-injects skills, and registers it
 * as a normal client agent. From then on it claims tasks like any
 * other agent.
 *
 * Aesthetic: cabin scrapbook, same as the rest. Each instance card
 * is a parchment index-card with a wax seal for status.
 */

const STATUS_STYLES: Record<PoolInstance['status'], { bg: string; text: string; border: string; icon: string; label: string }> = {
  starting: { bg: 'bg-amber-50',   text: 'text-amber-700',   border: 'border-amber-300',   icon: '⏳', label: 'BOOTING' },
  ready:    { bg: 'bg-emerald-50', text: 'text-emerald-700', border: 'border-emerald-400', icon: '✅', label: 'READY' },
  crashed:  { bg: 'bg-rose-50',    text: 'text-rose-700',    border: 'border-rose-300',    icon: '💥', label: 'CRASHED' },
  stopping: { bg: 'bg-[#8b4513]/5',text: 'text-[#8b4513]/60',border: 'border-[#8b4513]/20',icon: '⏹️', label: 'STOPPING' },
  stopped:  { bg: 'bg-[#8b4513]/10',text: 'text-[#5d4037]',   border: 'border-[#8b4513]/30',icon: '💤', label: 'STOPPED' },
}

function StatusTag({ status }: { status: PoolInstance['status'] }) {
  const s = STATUS_STYLES[status] || STATUS_STYLES.stopped
  return (
    <span className={`inline-flex items-center gap-1 px-2.5 py-1 rounded-lg border-2 ${s.bg} ${s.text} ${s.border} font-marker text-[10px] uppercase tracking-wider shadow-sm`}>
      <span>{s.icon}</span>
      {s.label}
    </span>
  )
}

function SpawnModal({ onClose, onSpawned }: { onClose: () => void; onSpawned: (i: PoolInstance) => void }) {
  const selectedProjectId = useAppStore((s) => s.selectedProjectId)
  const [name, setName] = useState('')
  const [roleHint, setRoleHint] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<string | null>(null)

  const handleSpawn = async () => {
    if (!selectedProjectId) return
    setBusy(true); setErr(null)
    try {
      const res = await agentPoolApi.spawn({ project_id: selectedProjectId, role_hint: roleHint || undefined, name: name || undefined })
      if (!res.success) throw new Error(res.error?.message || 'spawn failed')
      onSpawned(res.data)
      onClose()
    } catch (e) {
      setErr(e instanceof Error ? e.message : 'failed')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="fixed inset-0 bg-[#3e2723]/50 backdrop-blur-sm z-50 flex items-center justify-center" onClick={() => !busy && onClose()}>
      <div className="parchment w-[30rem] max-w-[90vw] rounded-3xl border border-[#8b4513]/30 shadow-2xl p-6" onClick={(e) => e.stopPropagation()}>
        <h3 className="font-marker text-xl text-[#5d4037] mb-1">🏠 Spawn Platform Agent</h3>
        <p className="font-hand text-sm text-[#8b4513]/60 mb-4">
          The platform will start an opencode subprocess on this host, auto-inject skills,
          and register it as a client agent. It'll appear in the agent list once ready.
        </p>

        <div className="space-y-3 mb-4">
          <div>
            <label className="block font-marker text-[10px] uppercase tracking-widest text-[#5d4037]/70 mb-1">Display name (optional)</label>
            <input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="platform-worker-1"
              className="w-full bg-white/70 border border-[#8b4513]/20 rounded-lg px-3 py-2 text-sm font-hand text-[#3e2723] placeholder-[#8b4513]/30 shadow-inner focus:ring-2 focus:ring-[#8b4513]/40 outline-none"
            />
          </div>
          <div>
            <label className="block font-marker text-[10px] uppercase tracking-widest text-[#5d4037]/70 mb-1">Role hint (optional)</label>
            <input
              value={roleHint}
              onChange={(e) => setRoleHint(e.target.value)}
              placeholder="any / audit_1 / fix / ..."
              className="w-full bg-white/70 border border-[#8b4513]/20 rounded-lg px-3 py-2 text-sm font-hand text-[#3e2723] placeholder-[#8b4513]/30 shadow-inner focus:ring-2 focus:ring-[#8b4513]/40 outline-none"
            />
            <p className="mt-1 text-[10px] font-hand text-[#8b4513]/50 italic">
              Cosmetic — the platform's task queue determines what the agent actually does.
            </p>
          </div>
        </div>

        {err && (
          <p className="mb-3 text-xs font-hand text-rose-700 bg-rose-50 border border-rose-200 px-3 py-2 rounded-lg">⚠ {err}</p>
        )}

        <div className="flex gap-2">
          <button
            onClick={handleSpawn}
            disabled={busy || !selectedProjectId}
            className="flex-1 px-4 py-2.5 rounded-xl font-marker text-sm bg-[#5d4037] hover:bg-[#4e342e] text-[#efebe9] border-b-4 border-[#3e2723] shadow-md active:scale-95 disabled:opacity-60"
          >
            {busy ? 'Spawning…' : 'Spawn'}
          </button>
          <button
            onClick={() => !busy && onClose()}
            className="px-4 py-2.5 rounded-xl font-marker text-sm bg-[#f4ece1] text-[#8b4513]/70 border border-[#8b4513]/20 hover:bg-[#8b4513]/10"
          >
            Cancel
          </button>
        </div>
      </div>
    </div>
  )
}

function InstanceCard({ inst, onShutdown, onPurge }: {
  inst: PoolInstance
  onShutdown: (id: string) => void
  onPurge: (id: string) => void
}) {
  const running = inst.status === 'ready' || inst.status === 'starting'
  const canPurge = inst.status === 'stopped' || inst.status === 'crashed'

  return (
    <div className="parchment rounded-2xl p-5 border border-[#8b4513]/20 shadow-md hover:shadow-lg transition-shadow relative">
      <div className="flex items-start justify-between gap-3 mb-3">
        <div className="min-w-0">
          <h4 className="font-marker text-base text-[#5d4037] flex items-center gap-2 truncate">
            🏠 {inst.agent_name}
            <span className="text-[10px] font-mono bg-black/5 text-[#8b4513]/50 px-2 py-0.5 rounded">#{inst.id.slice(0, 12)}</span>
          </h4>
          <div className="mt-1 flex items-center gap-3 text-[11px] font-hand text-[#8b4513]/60">
            <span>port <span className="font-mono text-[#5d4037]">{inst.port}</span></span>
            {inst.pid > 0 && <span>pid <span className="font-mono text-[#5d4037]">{inst.pid}</span></span>}
            <span>started {new Date(inst.started_at).toLocaleTimeString()}</span>
          </div>
        </div>
        <StatusTag status={inst.status} />
      </div>

      {inst.skills_injected && inst.skills_injected.length > 0 && (
        <div className="mb-3">
          <p className="text-[9px] font-marker text-[#8b4513]/40 uppercase tracking-widest mb-1">Skills injected ({inst.skills_injected.length})</p>
          <div className="flex flex-wrap gap-1">
            {inst.skills_injected.map((s) => (
              <span key={s} className="inline-flex items-center gap-1 px-2 py-0.5 rounded bg-[#f4ece1] border border-[#8b4513]/15 text-[10px] font-mono text-[#5d4037]">
                📜 {s}
              </span>
            ))}
          </div>
        </div>
      )}

      {inst.last_error && (
        <p className="mb-3 text-[11px] font-hand text-rose-700 bg-rose-50 border border-rose-200 px-2 py-1.5 rounded-lg">
          ⚠ {inst.last_error}
        </p>
      )}

      <div className="flex items-center gap-2">
        {running && (
          <button
            onClick={() => onShutdown(inst.id)}
            className="px-4 py-1.5 rounded-xl font-marker text-xs bg-rose-100 text-rose-700 border-2 border-rose-300 hover:bg-rose-200 transition-colors active:scale-95"
          >
            ⏹ Shutdown
          </button>
        )}
        {canPurge && (
          <button
            onClick={() => onPurge(inst.id)}
            className="px-4 py-1.5 rounded-xl font-marker text-xs bg-[#8b4513]/10 text-[#5d4037] border-2 border-[#8b4513]/20 hover:bg-[#8b4513]/20 transition-colors active:scale-95"
          >
            🗑 Purge
          </button>
        )}
        {inst.project_id && (
          <span className="ml-auto text-[10px] font-hand text-[#8b4513]/40">project <span className="font-mono">{inst.project_id}</span></span>
        )}
      </div>
    </div>
  )
}

export default function AgentPoolPage() {
  const [instances, setInstances] = useState<PoolInstance[]>([])
  const [loading, setLoading] = useState(true)
  const [spawnOpen, setSpawnOpen] = useState(false)

  const load = async () => {
    const res = await agentPoolApi.list()
    if (res.success) setInstances(res.data.instances || [])
    setLoading(false)
  }

  useEffect(() => {
    load()
    const i = setInterval(load, 8000)
    return () => clearInterval(i)
  }, [])

  const handleShutdown = async (id: string) => {
    await agentPoolApi.shutdown(id)
    load()
  }
  const handlePurge = async (id: string) => {
    await agentPoolApi.purge(id)
    load()
  }

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-extrabold text-[#5d4037] font-marker">🏠 Agent Pool</h1>
          <p className="text-[#8b4513]/60 text-sm font-hand mt-1">
            Platform-hosted client agents. The platform spawns these locally, injects skills, and lets them claim tasks like any external agent.
          </p>
        </div>
        <button
          onClick={() => setSpawnOpen(true)}
          className="px-5 py-2.5 rounded-xl font-marker text-sm bg-[#5d4037] hover:bg-[#4e342e] text-[#efebe9] border-b-4 border-[#3e2723] shadow-md active:scale-95"
        >
          + Spawn Agent
        </button>
      </div>

      {loading && instances.length === 0 ? (
        <div className="text-center py-20 text-[#8b4513]/40">
          <p className="text-4xl mb-3 opacity-30">⏳</p>
          <p className="font-marker text-lg">Peeking at the pool...</p>
        </div>
      ) : instances.length === 0 ? (
        <div className="parchment border-2 border-dashed border-[#8b4513]/25 rounded-3xl p-16 text-center">
          <p className="text-6xl mb-5 opacity-40">🏠</p>
          <p className="font-marker text-xl text-[#5d4037]">Pool is empty.</p>
          <p className="font-hand text-sm text-[#8b4513]/50 mt-2">
            Spawn a platform agent to get started. The platform will run an opencode subprocess on this host
            with your active skills pre-loaded.
          </p>
        </div>
      ) : (
        <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
          {instances.map((inst) => (
            <InstanceCard key={inst.id} inst={inst} onShutdown={handleShutdown} onPurge={handlePurge} />
          ))}
        </div>
      )}

      {spawnOpen && (
        <SpawnModal onClose={() => setSpawnOpen(false)} onSpawned={(i) => { setInstances((prev) => [...prev, i]); load() }} />
      )}
    </div>
  )
}
