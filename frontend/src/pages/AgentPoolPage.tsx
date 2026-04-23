import { useEffect, useMemo, useState } from 'react'
import { useAppStore } from '../stores/appStore'
import { agentPoolApi, type PoolInstance } from '../api/endpoints'

// ARCHIVE_THRESHOLD_TOKENS mirrors agentpool.ManagerConfig.ApplyDefaults
// (150_000). Not worth round-tripping — the backend never surfaces it
// and it only moves every few releases. If operators tune it
// (ManagerConfig.ArchiveThresholdTokens != default) the progress bar
// will over-estimate remaining headroom, which is a safer failure
// mode than under-estimating.
const ARCHIVE_THRESHOLD_TOKENS = 150_000

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
  // Opencode provider/model pair the pool agent will pin on its
  // session. These are *opencode's* provider ids (e.g.
  // "minimax-coding-plan"), NOT the platform's LLMEndpoint ids
  // (`llm_xxxx`). The two systems are parallel: LLMEndpoint drives
  // the native runner / dialogue pages; pool agents speak directly
  // to opencode's own config in ~/.config/opencode/opencode.json.
  //
  // Required for broadcast injection to work — the backend's
  // PoolBroadcastInjector refuses to post to opencode with empty
  // model fields (opencode silently drops the turn otherwise).
  const [providerID, setProviderID] = useState(() => localStorage.getItem('pool.last_provider') || '')
  const [modelID, setModelID] = useState(() => localStorage.getItem('pool.last_model') || '')

  // Remember the last-used pair so the next spawn doesn't make the
  // operator re-type the same strings. Stored in localStorage rather
  // than state so it survives reload.
  useEffect(() => {
    if (providerID) localStorage.setItem('pool.last_provider', providerID)
  }, [providerID])
  useEffect(() => {
    if (modelID) localStorage.setItem('pool.last_model', modelID)
  }, [modelID])

  const handleSpawn = async () => {
    if (!selectedProjectId) return
    setBusy(true); setErr(null)
    try {
      const res = await agentPoolApi.spawn({
        project_id: selectedProjectId,
        role_hint: roleHint || undefined,
        name: name || undefined,
        opencode_provider_id: providerID || undefined,
        opencode_model_id: modelID || undefined,
      })
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
      <div className="parchment w-[32rem] max-w-[90vw] rounded-3xl border border-[#8b4513]/30 shadow-2xl p-6" onClick={(e) => e.stopPropagation()}>
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

          <div className="grid grid-cols-2 gap-3">
            <div>
              <label className="block font-marker text-[10px] uppercase tracking-widest text-[#5d4037]/70 mb-1">Opencode provider *</label>
              <input
                list="pool-provider-suggestions"
                value={providerID}
                onChange={(e) => setProviderID(e.target.value)}
                placeholder="minimax-coding-plan"
                className="w-full bg-white/70 border border-[#8b4513]/20 rounded-lg px-3 py-2 text-sm font-hand text-[#3e2723] placeholder-[#8b4513]/30 shadow-inner focus:ring-2 focus:ring-[#8b4513]/40 outline-none"
              />
              <datalist id="pool-provider-suggestions">
                <option value="minimax-coding-plan" />
                <option value="anthropic" />
                <option value="openai" />
                <option value="deepseek" />
              </datalist>
            </div>
            <div>
              <label className="block font-marker text-[10px] uppercase tracking-widest text-[#5d4037]/70 mb-1">Model *</label>
              <input
                list="pool-model-suggestions"
                value={modelID}
                onChange={(e) => setModelID(e.target.value)}
                placeholder="MiniMax-M2.7"
                className="w-full bg-white/70 border border-[#8b4513]/20 rounded-lg px-3 py-2 text-sm font-hand text-[#3e2723] placeholder-[#8b4513]/30 shadow-inner focus:ring-2 focus:ring-[#8b4513]/40 outline-none"
              />
              <datalist id="pool-model-suggestions">
                <option value="MiniMax-M2.7" />
                <option value="claude-sonnet-4-5-20250929" />
                <option value="gpt-4o-mini" />
                <option value="deepseek-chat" />
              </datalist>
            </div>
          </div>
          <p className="-mt-1 text-[10px] font-hand text-[#8b4513]/50 italic">
            Required — these must match an entry in opencode's own config (~/.config/opencode/opencode.json). Platform broadcasts drop silently otherwise.
          </p>

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
            disabled={busy || !selectedProjectId || !providerID || !modelID}
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

// SessionHealthPanel visualises the pool agent's opencode session
// state: the session id (truncated + copyable), how full its context
// is relative to the archive threshold, and how many times the
// watcher has already rotated. Only renders when the backend has
// actually bound a session to the instance — an empty session id
// means either "still booting" or "session_creator errored", both
// of which the caller's last_error row already covers.
function SessionHealthPanel({ inst }: { inst: PoolInstance }) {
  if (!inst.opencode_session_id) return null
  const tokens = inst.last_context_tokens || 0
  const pct = Math.min(100, Math.round((tokens / ARCHIVE_THRESHOLD_TOKENS) * 100))
  // Color thresholds match operator intuition: green <60%, amber
  // 60-85%, red >85%. Rotation triggers at 100% so red means "one
  // tick away from a rotation" rather than "broken".
  const barColor = pct >= 85 ? 'bg-rose-500' : pct >= 60 ? 'bg-amber-500' : 'bg-emerald-500'
  const rotations = inst.archive_rotation || 0
  return (
    <div className="mb-3 rounded-lg bg-[#8b4513]/5 border border-[#8b4513]/15 px-3 py-2">
      <div className="flex items-center justify-between gap-2 mb-1.5">
        <div className="min-w-0">
          <p className="text-[9px] font-marker text-[#8b4513]/50 uppercase tracking-widest leading-tight">Opencode session</p>
          <p className="text-[10px] font-mono text-[#5d4037] truncate">{inst.opencode_session_id}</p>
        </div>
        {rotations > 0 && (
          <span
            title={`${rotations} archive rotation${rotations === 1 ? '' : 's'} since spawn`}
            className="shrink-0 inline-flex items-center gap-1 px-1.5 py-0.5 rounded bg-indigo-50 text-indigo-700 border border-indigo-200 text-[10px] font-mono"
          >
            ♻ {rotations}
          </span>
        )}
      </div>
      <div className="flex items-center gap-2">
        <div className="flex-1 h-1.5 bg-[#8b4513]/10 rounded-full overflow-hidden">
          <div className={`h-full ${barColor} transition-[width] duration-500`} style={{ width: `${pct}%` }} />
        </div>
        <span className="shrink-0 font-mono text-[10px] text-[#5d4037]/70 tabular-nums">
          {tokens.toLocaleString()} / {(ARCHIVE_THRESHOLD_TOKENS / 1000).toFixed(0)}K
        </span>
      </div>
      {inst.opencode_model_id && (
        <p className="mt-1 text-[9px] font-hand text-[#8b4513]/50">
          model <span className="font-mono text-[#5d4037]/80">{inst.opencode_provider_id}/{inst.opencode_model_id}</span>
        </p>
      )}
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

      <SessionHealthPanel inst={inst} />

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

// PoolOverview renders a 4-up stat strip over the instance grid.
// Built directly off the list response so there's no extra round-trip.
function PoolOverview({ instances }: { instances: PoolInstance[] }) {
  // Pre-compute once per render; cheap on any realistic pool size.
  const totals = useMemo(() => {
    let ready = 0, booting = 0, crashed = 0, stopped = 0
    let tokenSum = 0, rotSum = 0, sessionBound = 0
    for (const i of instances) {
      if (i.status === 'ready') ready++
      else if (i.status === 'starting') booting++
      else if (i.status === 'crashed') crashed++
      else stopped++
      tokenSum += i.last_context_tokens || 0
      rotSum += i.archive_rotation || 0
      if (i.opencode_session_id) sessionBound++
    }
    return { ready, booting, crashed, stopped, tokenSum, rotSum, sessionBound }
  }, [instances])

  const stats: { label: string; value: string; sub?: string; tone: string }[] = [
    {
      label: 'Running',
      value: String(totals.ready),
      sub: totals.booting > 0 ? `+${totals.booting} booting` : totals.stopped > 0 ? `${totals.stopped} stopped` : undefined,
      tone: totals.ready > 0 ? 'text-emerald-700' : 'text-[#8b4513]/60',
    },
    {
      label: 'Crashed',
      value: String(totals.crashed),
      tone: totals.crashed > 0 ? 'text-rose-700' : 'text-[#8b4513]/40',
    },
    {
      label: 'Tokens in flight',
      value: totals.tokenSum.toLocaleString(),
      sub: `across ${totals.sessionBound} session${totals.sessionBound === 1 ? '' : 's'}`,
      tone: 'text-[#5d4037]',
    },
    {
      label: 'Rotations',
      value: String(totals.rotSum),
      sub: totals.rotSum > 0 ? 'archive threshold reached' : 'since spawn',
      tone: totals.rotSum > 0 ? 'text-indigo-700' : 'text-[#8b4513]/60',
    },
  ]

  return (
    <div className="grid grid-cols-2 md:grid-cols-4 gap-3">
      {stats.map((s) => (
        <div key={s.label} className="parchment rounded-xl px-4 py-3 border border-[#8b4513]/15 shadow-sm">
          <p className="text-[9px] font-marker uppercase tracking-widest text-[#8b4513]/50">{s.label}</p>
          <p className={`mt-0.5 font-marker text-2xl leading-tight ${s.tone}`}>{s.value}</p>
          {s.sub && <p className="mt-0.5 text-[10px] font-hand text-[#8b4513]/50">{s.sub}</p>}
        </div>
      ))}
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
    // Tighter refresh (4s) than the original 8s so the token
    // progress bar feels live — the context watcher polls every 30s
    // upstream, so 4s is as fast as the data actually changes.
    const i = setInterval(load, 4000)
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

      {instances.length > 0 && <PoolOverview instances={instances} />}

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
