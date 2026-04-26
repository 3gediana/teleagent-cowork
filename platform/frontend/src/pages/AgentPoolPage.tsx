import { useEffect, useMemo, useState } from 'react'
import { useAppStore } from '../stores/appStore'
import {
  agentPoolApi,
  type PoolInstance,
  type OpencodeProvider,
  type PoolMetrics,
  type PoolEvent,
  type PoolTokenSample,
} from '../api/endpoints'

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
  starting: { bg: 'bg-amber-50',      text: 'text-amber-700',   border: 'border-amber-300',   icon: '⏳', label: 'BOOTING' },
  ready:    { bg: 'bg-emerald-50',    text: 'text-emerald-700', border: 'border-emerald-400', icon: '✅', label: 'READY' },
  crashed:  { bg: 'bg-rose-50',       text: 'text-rose-700',    border: 'border-rose-300',    icon: '💥', label: 'CRASHED' },
  stopping: { bg: 'bg-[#8b4513]/5',   text: 'text-[#8b4513]/60',border: 'border-[#8b4513]/20',icon: '⏹️', label: 'STOPPING' },
  stopped:  { bg: 'bg-[#8b4513]/10',  text: 'text-[#5d4037]',   border: 'border-[#8b4513]/30',icon: '💤', label: 'STOPPED' },
  // 'dormant' = platform idly-reclaimed; opencode subprocess is
  // gone but the Instance + session metadata are preserved so a
  // Wake click rehydrates the same agent. Moon glyph + slate tone
  // to distinguish from the grey 'stopped' (which is terminal).
  dormant:  { bg: 'bg-slate-100',     text: 'text-slate-700',   border: 'border-slate-300',   icon: '🌙', label: 'DORMANT' },
  // 'waking' is the few-second transient while Wake re-spawns
  // opencode. Operators see it briefly, same tone family as
  // 'starting' so the visual rhythm tracks.
  waking:   { bg: 'bg-sky-50',        text: 'text-sky-700',     border: 'border-sky-300',     icon: '☀️', label: 'WAKING' },
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
  // "minimax-coding-plan") served by GET /agentpool/opencode-providers
  // which reads ~/.config/opencode/opencode.json. NOT the LLMEndpoint
  // ids (`llm_xxxx`) which drive the native runner.
  //
  // Required for broadcast injection to work — the backend's
  // PoolBroadcastInjector refuses to post to opencode with empty
  // model fields (opencode silently drops the turn otherwise).
  const [providerID, setProviderID] = useState(() => localStorage.getItem('pool.last_provider') || '')
  const [modelID, setModelID] = useState(() => localStorage.getItem('pool.last_model') || '')
  // Catalogue fetched at modal open. Empty array = fallback to
  // free-text input (fresh installs or missing config.json).
  const [providers, setProviders] = useState<OpencodeProvider[]>([])

  // Remember the last-used pair so the next spawn doesn't make the
  // operator re-type the same strings. Stored in localStorage rather
  // than state so it survives reload.
  useEffect(() => {
    if (providerID) localStorage.setItem('pool.last_provider', providerID)
  }, [providerID])
  useEffect(() => {
    if (modelID) localStorage.setItem('pool.last_model', modelID)
  }, [modelID])

  // Load opencode config catalogue once on mount. Failure is
  // non-fatal — the input stays a free-text field.
  useEffect(() => {
    agentPoolApi.opencodeProviders()
      .then((res) => {
        if (res?.success && res.data?.providers?.length) {
          setProviders(res.data.providers)
          // If the operator has no saved preference, seed with the
          // first provider+model so "just click Spawn" works on a
          // fresh install.
          if (!providerID && res.data.providers[0]) {
            const p0 = res.data.providers[0]
            setProviderID(p0.id)
            if (!modelID && p0.models[0]) setModelID(p0.models[0].id)
          }
        }
      })
      .catch(() => {/* silent fallback */})
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // Model list narrows once a provider is picked. Free-text when the
  // catalogue is missing or the selected provider isn't in it.
  const selectedProvider = useMemo(
    () => providers.find((p) => p.id === providerID),
    [providers, providerID],
  )

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
              {providers.length > 0 ? (
                <select
                  value={providerID}
                  onChange={(e) => setProviderID(e.target.value)}
                  className="w-full bg-white/70 border border-[#8b4513]/20 rounded-lg px-3 py-2 text-sm font-hand text-[#3e2723] shadow-inner focus:ring-2 focus:ring-[#8b4513]/40 outline-none"
                >
                  {providers.map((p) => (
                    <option key={p.id} value={p.id}>{p.name} <span className="text-[#8b4513]/40">— {p.id}</span></option>
                  ))}
                </select>
              ) : (
                <input
                  value={providerID}
                  onChange={(e) => setProviderID(e.target.value)}
                  placeholder="minimax-coding-plan"
                  className="w-full bg-white/70 border border-[#8b4513]/20 rounded-lg px-3 py-2 text-sm font-hand text-[#3e2723] placeholder-[#8b4513]/30 shadow-inner focus:ring-2 focus:ring-[#8b4513]/40 outline-none"
                />
              )}
            </div>
            <div>
              <label className="block font-marker text-[10px] uppercase tracking-widest text-[#5d4037]/70 mb-1">Model *</label>
              {selectedProvider && selectedProvider.models.length > 0 ? (
                <select
                  value={modelID}
                  onChange={(e) => setModelID(e.target.value)}
                  className="w-full bg-white/70 border border-[#8b4513]/20 rounded-lg px-3 py-2 text-sm font-hand text-[#3e2723] shadow-inner focus:ring-2 focus:ring-[#8b4513]/40 outline-none"
                >
                  {selectedProvider.models.map((m) => (
                    <option key={m.id} value={m.id}>{m.name}</option>
                  ))}
                </select>
              ) : (
                <input
                  value={modelID}
                  onChange={(e) => setModelID(e.target.value)}
                  placeholder="MiniMax-M2.7"
                  className="w-full bg-white/70 border border-[#8b4513]/20 rounded-lg px-3 py-2 text-sm font-hand text-[#3e2723] placeholder-[#8b4513]/30 shadow-inner focus:ring-2 focus:ring-[#8b4513]/40 outline-none"
                />
              )}
            </div>
          </div>
          <p className="-mt-1 text-[10px] font-hand text-[#8b4513]/50 italic">
            {providers.length > 0
              ? `${providers.length} provider${providers.length === 1 ? '' : 's'} from ~/.config/opencode/opencode.json — edit that file to add more.`
              : 'Free-text: these must match an entry in ~/.config/opencode/opencode.json. Broadcasts drop silently otherwise.'}
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

// dormantDuration renders "3m", "2h" etc. for how long an agent's
// been asleep. Empty string if dormant_at is missing (legacy rows
// from before Phase 4). Updates only on re-render — caller is
// responsible for polling the list if they want live counters.
function dormantDuration(dormantAt?: string): string {
  if (!dormantAt) return ''
  const ms = Date.now() - new Date(dormantAt).getTime()
  if (!Number.isFinite(ms) || ms < 0) return ''
  const s = Math.floor(ms / 1000)
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h}h`
  return `${Math.floor(h / 24)}d`
}

// eventLabel maps the backend event type to an operator-friendly
// icon + phrase. Unknown types pass through unchanged so
// forward-compatible additions show up in the log without a
// frontend release.
function eventLabel(t: string): { icon: string; phrase: string } {
  switch (t) {
    case 'spawn_ready':
      return { icon: '🟢', phrase: 'spawned' }
    case 'rotate':
      return { icon: '🔁', phrase: 'rotated session' }
    case 'dormancy':
      return { icon: '🌙', phrase: 'entered dormancy' }
    case 'wake':
      return { icon: '☀️', phrase: 'woke up' }
    case 'crash':
      return { icon: '💥', phrase: 'crashed' }
    case 'shutdown':
      return { icon: '⏹', phrase: 'shut down' }
    default:
      return { icon: '•', phrase: t }
  }
}

// formatClock renders a unix-ms timestamp into "HH:MM:SS" — tight
// enough for a row in the event log without wrapping.
function formatClock(ms: number): string {
  const d = new Date(ms)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
}

// TokenSparkline renders the recent token curve as a polyline SVG
// so we don't drag in a chart dependency. Y-axis is normalised
// against the archive threshold (150K) so the line height maps
// directly to "how close we are to rotating". Rotations show as
// vertical dashed markers so the operator can read the history
// without peeking at the event log.
function TokenSparkline({ tokens, events }: { tokens: PoolTokenSample[]; events: PoolEvent[] }) {
  const w = 280
  const h = 48
  const pad = 2

  if (tokens.length < 2) {
    return (
      <div className="h-12 flex items-center justify-center text-[10px] font-hand text-[#8b4513]/40 italic">
        collecting samples…
      </div>
    )
  }
  const xs0 = tokens[0].at_ms
  const xs1 = tokens[tokens.length - 1].at_ms
  const span = Math.max(1, xs1 - xs0)

  const points = tokens.map((s) => {
    const x = pad + ((s.at_ms - xs0) / span) * (w - pad * 2)
    const yNorm = Math.min(1, Math.max(0, s.tokens / ARCHIVE_THRESHOLD_TOKENS))
    const y = h - pad - yNorm * (h - pad * 2)
    return `${x.toFixed(1)},${y.toFixed(1)}`
  }).join(' ')

  // Rotation markers sit at the x of any event with type=rotate
  // whose at_ms falls inside the sample window (older ones drop
  // off the left edge naturally with the data).
  const rotateXs: number[] = []
  for (const e of events) {
    if (e.type !== 'rotate') continue
    if (e.at_ms < xs0 || e.at_ms > xs1) continue
    rotateXs.push(pad + ((e.at_ms - xs0) / span) * (w - pad * 2))
  }

  // Threshold bands: 60% amber, 85% rose.
  const thresholdLine = (pct: number, color: string) => {
    const y = h - pad - pct * (h - pad * 2)
    return <line x1={pad} x2={w - pad} y1={y} y2={y} stroke={color} strokeDasharray="2 3" strokeWidth={0.5} />
  }

  return (
    <svg viewBox={`0 0 ${w} ${h}`} className="w-full h-12" preserveAspectRatio="none">
      {thresholdLine(0.6, '#d97706')}
      {thresholdLine(0.85, '#e11d48')}
      {rotateXs.map((x, i) => (
        <line key={i} x1={x} x2={x} y1={pad} y2={h - pad} stroke="#4338ca" strokeDasharray="1 2" strokeWidth={0.75} opacity={0.7} />
      ))}
      <polyline points={points} fill="none" stroke="#059669" strokeWidth={1.25} strokeLinejoin="round" />
    </svg>
  )
}

// ExpandedMetricsPanel fetches /agentpool/metrics/:id on mount and
// polls at 4s while visible. Unmounts stop the poll. The backend
// keeps rings in-memory so this is the sole way to see them.
function ExpandedMetricsPanel({ instanceID }: { instanceID: string }) {
  const [metrics, setMetrics] = useState<PoolMetrics | null>(null)
  const [err, setErr] = useState<string | null>(null)

  useEffect(() => {
    let stopped = false
    const pull = async () => {
      try {
        const res = await agentPoolApi.metrics(instanceID)
        if (stopped) return
        if (res?.success) {
          setMetrics(res.data)
          setErr(null)
        } else {
          setErr('metrics fetch failed')
        }
      } catch (e) {
        if (!stopped) setErr(e instanceof Error ? e.message : 'network')
      }
    }
    pull()
    const t = setInterval(pull, 4000)
    return () => { stopped = true; clearInterval(t) }
  }, [instanceID])

  if (err) {
    return <p className="text-[10px] font-hand text-rose-700 italic">metrics: {err}</p>
  }
  if (!metrics) {
    return <p className="text-[10px] font-hand text-[#8b4513]/40 italic">loading metrics…</p>
  }

  // Show newest events first, cap at 20 so the card stays compact.
  const recent = [...metrics.events].reverse().slice(0, 20)

  return (
    <div className="mt-3 rounded-lg border border-[#8b4513]/15 bg-[#faf6ee] p-3 space-y-2">
      <div>
        <p className="text-[9px] font-marker uppercase tracking-widest text-[#8b4513]/50 mb-1">
          Token curve <span className="text-[#8b4513]/30">({metrics.tokens.length} samples)</span>
        </p>
        <TokenSparkline tokens={metrics.tokens} events={metrics.events} />
      </div>
      <div>
        <p className="text-[9px] font-marker uppercase tracking-widest text-[#8b4513]/50 mb-1">
          Recent events <span className="text-[#8b4513]/30">({metrics.events.length} total)</span>
        </p>
        {recent.length === 0 ? (
          <p className="text-[10px] font-hand text-[#8b4513]/40 italic">no events yet</p>
        ) : (
          <ul className="space-y-0.5 max-h-40 overflow-y-auto pr-1">
            {recent.map((e, i) => {
              const label = eventLabel(e.type)
              return (
                <li key={i} className="flex items-start gap-2 text-[10px] font-mono text-[#3e2723]">
                  <span className="shrink-0 w-14 text-[#8b4513]/60">{formatClock(e.at_ms)}</span>
                  <span className="shrink-0">{label.icon}</span>
                  <span className="shrink-0 w-20 text-[#5d4037]">{label.phrase}</span>
                  {e.detail && <span className="text-[#8b4513]/70 truncate" title={e.detail}>{e.detail}</span>}
                </li>
              )
            })}
          </ul>
        )}
      </div>
    </div>
  )
}

function InstanceCard({ inst, onShutdown, onSleep, onWake, onPurge, pendingAction }: {
  inst: PoolInstance
  onShutdown: (id: string) => void
  onSleep: (id: string) => void
  onWake: (id: string) => void
  onPurge: (id: string) => void
  pendingAction: string | null
}) {
  const running = inst.status === 'ready' || inst.status === 'starting' || inst.status === 'waking'
  const canPurge = inst.status === 'stopped' || inst.status === 'crashed'
  const canSleep = inst.status === 'ready'
  const canWake = inst.status === 'dormant'
  const busy = pendingAction === inst.id
  // Per-card expand toggle for the metrics panel. Not hoisted to
  // the page — each card polls independently, so one expanded
  // card doesn't drive the whole grid's fetch volume.
  const [expanded, setExpanded] = useState(false)

  return (
    <div className="parchment rounded-2xl p-5 border border-[#8b4513]/20 shadow-md hover:shadow-lg transition-shadow relative">
      <div
        className="flex items-start justify-between gap-3 mb-3 cursor-pointer select-none"
        title={expanded ? 'Collapse metrics' : 'Expand to see token curve + event log'}
        onClick={() => setExpanded((v) => !v)}
      >
        <div className="min-w-0">
          <h4 className="font-marker text-base text-[#5d4037] flex items-center gap-2 truncate">
            🏠 {inst.agent_name}
            <span className="text-[10px] font-mono bg-black/5 text-[#8b4513]/50 px-2 py-0.5 rounded">#{inst.id.slice(0, 12)}</span>
            <span className="text-[10px] text-[#8b4513]/40">{expanded ? '▾' : '▸'}</span>
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

      <div className="flex items-center gap-2 flex-wrap">
        {canSleep && (
          <button
            disabled={busy}
            onClick={() => onSleep(inst.id)}
            title="Archive current session + stop opencode subprocess. Wake to bring back."
            className="px-3 py-1.5 rounded-xl font-marker text-xs bg-slate-100 text-slate-700 border-2 border-slate-300 hover:bg-slate-200 transition-colors active:scale-95 disabled:opacity-50"
          >
            🌙 Sleep
          </button>
        )}
        {canWake && (
          <button
            disabled={busy}
            onClick={() => onWake(inst.id)}
            title="Re-spawn opencode + create a fresh session. Agent identity preserved."
            className="px-3 py-1.5 rounded-xl font-marker text-xs bg-sky-100 text-sky-700 border-2 border-sky-300 hover:bg-sky-200 transition-colors active:scale-95 disabled:opacity-50"
          >
            ☀️ Wake
          </button>
        )}
        {running && (
          <button
            disabled={busy}
            onClick={() => onShutdown(inst.id)}
            className="px-3 py-1.5 rounded-xl font-marker text-xs bg-rose-100 text-rose-700 border-2 border-rose-300 hover:bg-rose-200 transition-colors active:scale-95 disabled:opacity-50"
          >
            ⏹ Shutdown
          </button>
        )}
        {canPurge && (
          <button
            disabled={busy}
            onClick={() => onPurge(inst.id)}
            className="px-3 py-1.5 rounded-xl font-marker text-xs bg-[#8b4513]/10 text-[#5d4037] border-2 border-[#8b4513]/20 hover:bg-[#8b4513]/20 transition-colors active:scale-95 disabled:opacity-50"
          >
            🗑 Purge
          </button>
        )}
        {inst.status === 'dormant' && inst.dormant_at && (
          <span className="text-[10px] font-hand text-slate-500 italic">
            asleep for {dormantDuration(inst.dormant_at)}
          </span>
        )}
        {inst.project_id && (
          <span className="ml-auto text-[10px] font-hand text-[#8b4513]/40">project <span className="font-mono">{inst.project_id}</span></span>
        )}
      </div>

      {expanded && <ExpandedMetricsPanel instanceID={inst.id} />}
    </div>
  )
}

// PoolOverview renders a 4-up stat strip over the instance grid.
// Built directly off the list response so there's no extra round-trip.
function PoolOverview({ instances }: { instances: PoolInstance[] }) {
  // Pre-compute once per render; cheap on any realistic pool size.
  const totals = useMemo(() => {
    let ready = 0, booting = 0, crashed = 0, stopped = 0, dormant = 0, waking = 0
    let tokenSum = 0, rotSum = 0, sessionBound = 0
    for (const i of instances) {
      if (i.status === 'ready') ready++
      else if (i.status === 'starting') booting++
      else if (i.status === 'waking') waking++
      else if (i.status === 'dormant') dormant++
      else if (i.status === 'crashed') crashed++
      else stopped++
      tokenSum += i.last_context_tokens || 0
      rotSum += i.archive_rotation || 0
      if (i.opencode_session_id) sessionBound++
    }
    return { ready, booting, crashed, stopped, dormant, waking, tokenSum, rotSum, sessionBound }
  }, [instances])

  const stats: { label: string; value: string; sub?: string; tone: string }[] = [
    {
      label: 'Running',
      value: String(totals.ready),
      sub: totals.booting + totals.waking > 0
        ? `+${totals.booting + totals.waking} ${totals.waking > 0 ? 'waking' : 'booting'}`
        : totals.stopped > 0 ? `${totals.stopped} stopped` : undefined,
      tone: totals.ready > 0 ? 'text-emerald-700' : 'text-[#8b4513]/60',
    },
    {
      label: 'Dormant',
      value: String(totals.dormant),
      sub: totals.dormant > 0 ? 'asleep; session preserved' : 'no sleepers',
      tone: totals.dormant > 0 ? 'text-slate-700' : 'text-[#8b4513]/40',
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
      sub: totals.crashed > 0
        ? `${totals.crashed} crashed`
        : totals.rotSum > 0 ? 'archive threshold reached' : 'since spawn',
      tone: totals.crashed > 0 ? 'text-rose-700' : totals.rotSum > 0 ? 'text-indigo-700' : 'text-[#8b4513]/60',
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
  // pendingAction holds the id of the instance currently being
  // mutated (sleep/wake/shutdown/purge) so we can disable its card's
  // buttons. One global slot is plenty — operators don't fire two
  // actions at once from the same page.
  const [pendingAction, setPendingAction] = useState<string | null>(null)
  const [actionErr, setActionErr] = useState<string | null>(null)

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

  // runAction is the common wrapper around every lifecycle call. It
  // locks the card for the instance, runs the call, surfaces any
  // backend error on a banner, and reloads regardless — so even a
  // failed Wake refreshes the error text on the card.
  const runAction = async (id: string, kind: string, fn: () => Promise<{ success: boolean; error?: any }>) => {
    if (pendingAction) return
    setPendingAction(id)
    setActionErr(null)
    try {
      const res = await fn()
      if (!res?.success) {
        const msg = res?.error?.message || `${kind} failed`
        setActionErr(`${kind} on ${id.slice(0, 12)}: ${msg}`)
      }
    } catch (e) {
      setActionErr(`${kind}: ${e instanceof Error ? e.message : 'network error'}`)
    } finally {
      setPendingAction(null)
      load()
    }
  }

  const handleShutdown = (id: string) => runAction(id, 'shutdown', () => agentPoolApi.shutdown(id))
  const handleSleep    = (id: string) => runAction(id, 'sleep',    () => agentPoolApi.sleep(id))
  const handleWake     = (id: string) => runAction(id, 'wake',     () => agentPoolApi.wake(id))
  const handlePurge    = (id: string) => runAction(id, 'purge',    () => agentPoolApi.purge(id))

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

      {actionErr && (
        <div className="parchment rounded-xl px-4 py-2 border-2 border-rose-300 bg-rose-50 text-rose-700 font-hand text-sm flex items-center justify-between gap-2">
          <span>⚠ {actionErr}</span>
          <button
            onClick={() => setActionErr(null)}
            className="text-xs font-marker px-2 py-0.5 rounded bg-rose-100 border border-rose-300 hover:bg-rose-200"
          >
            dismiss
          </button>
        </div>
      )}

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
            <InstanceCard
              key={inst.id}
              inst={inst}
              onShutdown={handleShutdown}
              onSleep={handleSleep}
              onWake={handleWake}
              onPurge={handlePurge}
              pendingAction={pendingAction}
            />
          ))}
        </div>
      )}

      {spawnOpen && (
        <SpawnModal onClose={() => setSpawnOpen(false)} onSpawned={(i) => { setInstances((prev) => [...prev, i]); load() }} />
      )}
    </div>
  )
}
