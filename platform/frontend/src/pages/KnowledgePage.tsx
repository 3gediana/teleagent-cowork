import { useState, useEffect } from 'react'
import { useAppStore } from '../stores/appStore'
import { refineryApi, metricsApi } from '../api/endpoints'

const KIND_COLORS: Record<string, string> = {
  pattern: '#10b981',
  anti_pattern: '#ef4444',
  tool_recipe: '#3b82f6',
  model_route: '#8b5cf6',
  failure_class: '#f59e0b',
  temporal_rule: '#06b6d4',
}

const KIND_LABELS: Record<string, string> = {
  pattern: 'Pattern',
  anti_pattern: 'Anti-Pattern',
  tool_recipe: 'Tool Recipe',
  model_route: 'Model Route',
  failure_class: 'Failure Class',
  temporal_rule: 'Temporal Rule',
}

const STATUS_COLORS: Record<string, string> = {
  candidate: '#f59e0b',
  active: '#10b981',
  deprecated: '#6b7280',
  rejected: '#ef4444',
}

// InjectionMetrics mirrors the server's shape — see
// @platform/backend/internal/service/injection_metrics.go
type SignalTally = { success: number; failure: number; rate: number }
type InjectionMetrics = {
  total_changes: number
  signals: Record<string, SignalTally>
  generated_at?: string
}

// Colours echo the KIND palette so semantic/importance/tag/recency feel
// visually consistent with the rest of the Refinery surface.
const SIGNAL_COLORS: Record<string, string> = {
  semantic:   '#3b82f6', // blue — the most "intelligent" signal
  tag:        '#10b981', // emerald — matches pattern green
  importance: '#f59e0b', // amber — usage-weighted, the legacy fallback
  recency:    '#06b6d4', // cyan  — time-based
  unknown:    '#6b7280', // slate — legacy flat-id payloads
}

const SIGNAL_LABELS: Record<string, string> = {
  semantic:   'Semantic',
  tag:        'Tag match',
  importance: 'Importance',
  recency:    'Recency',
  unknown:    'Legacy',
}

export default function KnowledgePage() {
  const { selectedProjectId } = useAppStore()
  const [artifacts, setArtifacts] = useState<any[]>([])
  const [counts, setCounts] = useState<{ kind: string; total: number }[]>([])
  const [growth, setGrowth] = useState<{ day: string; kind: string; count: number }[]>([])
  const [runs, setRuns] = useState<any[]>([])
  const [metrics, setMetrics] = useState<InjectionMetrics | null>(null)
  const [filterKind, setFilterKind] = useState<string>('')
  const [running, setRunning] = useState(false)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string>('')
  const [expandedId, setExpandedId] = useState<string | null>(null)

  const loadData = async () => {
    if (!selectedProjectId) return
    setLoading(true)
    setError('')
    try {
      // metrics endpoint is recent (PR 9) and may 4xx on older servers;
      // tolerate that so the rest of the page still renders.
      const [a, g, r, m] = await Promise.all([
        refineryApi.artifacts(selectedProjectId, filterKind || undefined),
        refineryApi.growth(selectedProjectId, 30),
        refineryApi.runs(selectedProjectId),
        metricsApi.injectionSignal(selectedProjectId).catch(() => null),
      ])
      if (a.success) { setArtifacts(a.data.artifacts || []); setCounts(a.data.counts || []) }
      if (g.success) setGrowth(g.data.series || [])
      if (r.success) setRuns(r.data.runs || [])
      if (m && m.success) setMetrics(m.data as InjectionMetrics)
      else setMetrics(null)
    } catch (e: any) {
      setError(e?.message || 'Failed to load data')
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => { loadData() }, [selectedProjectId, filterKind])

  const handleRun = async () => {
    if (!selectedProjectId || running) return
    setRunning(true)
    setError('')
    try {
      const res = await refineryApi.run(selectedProjectId, 24 * 14)
      if (!res.success) { setError('Run failed to start'); return }
      // Poll for completion (async run)
      const runId = res.data.run_id
      for (let i = 0; i < 30; i++) {
        await new Promise(r => setTimeout(r, 2000))
        const rr = await refineryApi.runs(selectedProjectId)
        if (rr.success) {
          const target = rr.data.runs?.find((r: any) => r.id === runId)
          if (target && target.status !== 'pending' && target.status !== 'running') break
        }
      }
      await loadData()
    } catch (e: any) {
      setError(e?.message || 'Run failed')
    } finally {
      setRunning(false)
    }
  }

  const handleStatusChange = async (artifactId: string, newStatus: string) => {
    try {
      await refineryApi.updateStatus(artifactId, newStatus)
      await loadData()
    } catch (e: any) {
      setError(e?.message || 'Status update failed')
    }
  }

  // Build a simple sparkline per kind from growth data
  const growthByKind = new Map<string, { day: string; count: number }[]>()
  for (const g of growth) {
    let arr = growthByKind.get(g.kind)
    if (!arr) { arr = []; growthByKind.set(g.kind, arr) }
    arr.push({ day: g.day, count: g.count })
  }

  const totalArtifacts = counts.reduce((s, c) => s + c.total, 0)

  return (
    <div className="space-y-6">
      {/* Error banner */}
      {error && (
        <div className="bg-red-500/10 border border-red-500/20 rounded-xl px-4 py-3 flex items-center justify-between">
          <span className="text-sm text-red-700">{error}</span>
          <button onClick={() => setError('')} className="text-red-500 hover:text-red-700 text-xs font-bold">✕</button>
        </div>
      )}

      {/* Header */}
      <div className="flex items-center justify-between">
        <div>
          <h2 className="text-2xl font-marker text-[#5d4037]">Knowledge Refinery</h2>
          <p className="text-sm text-[#5d4037]/60 mt-1">Multi-pass distillation of agent activity into reusable knowledge</p>
        </div>
        <button
          onClick={handleRun}
          disabled={running}
          className={`px-5 py-2.5 rounded-xl font-bold text-sm transition-all ${
            running
              ? 'bg-[#8b4513]/30 text-[#5d4037]/40 cursor-wait'
              : 'bg-[#8b4513] text-[#f4ece1] hover:bg-[#6d3410] shadow-md hover:shadow-lg'
          }`}
        >
          {running ? '⟳ Running...' : '▶ Run Refinery'}
        </button>
      </div>

      {/* Kind summary cards */}
      {loading && counts.length === 0 ? (
        <div className="text-center py-12 text-[#5d4037]/30 text-sm animate-pulse">
          Loading knowledge data...
        </div>
      ) : (
      <div className="grid grid-cols-3 md:grid-cols-6 gap-3">
        {counts.map(c => (
          <button
            key={c.kind}
            onClick={() => setFilterKind(filterKind === c.kind ? '' : c.kind)}
            className={`p-4 rounded-xl border-2 transition-all text-center ${
              filterKind === c.kind ? 'border-current scale-105 shadow-lg' : 'border-transparent hover:border-[#8b4513]/20'
            }`}
            style={{ borderColor: filterKind === c.kind ? KIND_COLORS[c.kind] : undefined }}
          >
            <div className="text-2xl font-bold" style={{ color: KIND_COLORS[c.kind] }}>{c.total}</div>
            <div className="text-[10px] font-bold text-[#5d4037]/60 uppercase tracking-wider mt-1">
              {KIND_LABELS[c.kind] || c.kind}
            </div>
          </button>
        ))}
        {counts.length === 0 && (
          <div className="col-span-6 text-center py-8 text-[#5d4037]/40 text-sm">
            No artifacts yet. Run the refinery to distill knowledge from agent activity.
          </div>
        )}
      </div>
      )}

      {/* Growth sparklines with area fill */}
      {growthByKind.size > 0 && (
        <div className="bg-[#f4ece1]/50 rounded-xl p-5 border border-[#8b4513]/10">
          <h3 className="text-sm font-bold text-[#5d4037]/60 uppercase tracking-wider mb-3">Growth (30d)</h3>
          <div className="grid grid-cols-2 md:grid-cols-3 gap-4">
            {Array.from(growthByKind.entries()).map(([kind, series]) => {
              const max = Math.max(...series.map(s => s.count), 1)
              const w = 120, h = 40, padY = 4
              const linePoints = series.map((s, i) => {
                const x = (i / Math.max(series.length - 1, 1)) * w
                const y = padY + (1 - s.count / max) * (h - padY * 2)
                return `${x},${y}`
              }).join(' ')
              // area: close the polygon at the bottom
              const areaPoints = linePoints
                + ` ${w},${h} 0,${h}`
              const latest = series[series.length - 1]?.count || 0
              const first = series[0]?.count || 0
              const trend = latest > first ? '↑' : latest < first ? '↓' : '→'
              return (
                <div key={kind} className="flex items-center gap-3">
                  <svg viewBox={`0 0 ${w} ${h}`} className="w-20 h-10" preserveAspectRatio="none">
                    <polygon fill={KIND_COLORS[kind]} fillOpacity="0.15" points={areaPoints} />
                    <polyline fill="none" stroke={KIND_COLORS[kind]} strokeWidth="2" strokeLinejoin="round" points={linePoints} />
                  </svg>
                  <div>
                    <div className="text-xs font-bold" style={{ color: KIND_COLORS[kind] }}>{KIND_LABELS[kind] || kind}</div>
                    <div className="text-[10px] text-[#5d4037]/50">{latest} latest {trend}</div>
                  </div>
                </div>
              )
            })}
          </div>
        </div>
      )}

      {/* Injection signal metrics — how well does each retrieval reason perform
          across feedback-applied changes? Empty until PR 5/9 data accumulates. */}
      {metrics && metrics.total_changes > 0 && (
        <div className="bg-[#f4ece1]/50 rounded-xl p-5 border border-[#8b4513]/10">
          <div className="flex items-baseline justify-between mb-3">
            <h3 className="text-sm font-bold text-[#5d4037]/60 uppercase tracking-wider">
              Injection Signal Quality
            </h3>
            <span className="text-[10px] text-[#5d4037]/40">
              {metrics.total_changes} audited change{metrics.total_changes === 1 ? '' : 's'}
            </span>
          </div>
          <div className="space-y-2">
            {Object.entries(metrics.signals)
              .map(([signal, tally]) => ({ signal, tally, total: tally.success + tally.failure }))
              // Hide signals with zero settled verdicts so L1-only buckets
              // don't waste row space; they'd show a 0% bar that isn't 0.
              .filter((row) => row.total > 0)
              .sort((a, b) => b.total - a.total)
              .map(({ signal, tally, total }) => {
                const successPct = tally.rate * 100
                const color = SIGNAL_COLORS[signal] || '#6b7280'
                return (
                  <div key={signal} className="flex items-center gap-3">
                    <div className="w-24 text-xs font-bold" style={{ color }}>
                      {SIGNAL_LABELS[signal] || signal}
                    </div>
                    <div className="flex-1 h-6 bg-[#8b4513]/10 rounded-md overflow-hidden relative">
                      <div
                        className="h-full transition-[width] duration-500"
                        style={{ width: `${successPct}%`, background: color, opacity: 0.8 }}
                      />
                      <div className="absolute inset-0 flex items-center justify-between px-2 text-[10px] font-mono font-bold text-[#3b2010]">
                        <span>{tally.success}✓ / {tally.failure}✗</span>
                        <span>{successPct.toFixed(0)}%</span>
                      </div>
                    </div>
                    <div className="w-10 text-right text-[10px] text-[#5d4037]/40 font-mono">
                      n={total}
                    </div>
                  </div>
                )
              })}
          </div>
          <p className="text-[10px] text-[#5d4037]/40 mt-3 leading-relaxed">
            Success rate per retrieval signal that was cited as the dominant reason in an injected hint.
            Higher = that signal's picks were more often audited <span className="font-bold">L0 (accepted)</span>.
          </p>
        </div>
      )}

      {/* Recent runs */}
      {runs.length > 0 && (
        <div className="space-y-2">
          <h3 className="text-sm font-bold text-[#5d4037]/60 uppercase tracking-wider">Recent Runs</h3>
          {runs.slice(0, 5).map(run => (
            <div key={run.id} className="flex items-center gap-4 px-4 py-2.5 bg-[#f4ece1]/30 rounded-lg border border-[#8b4513]/5">
              <span className={`w-2.5 h-2.5 rounded-full ${
                run.status === 'ok' ? 'bg-emerald-500' :
                run.status === 'partial' ? 'bg-amber-500' :
                run.status === 'pending' ? 'bg-blue-400 animate-pulse' :
                'bg-red-500'}`} />
              <span className="text-xs text-[#5d4037]/60 font-mono">{new Date(run.started_at).toLocaleString()}</span>
              <span className="text-xs text-[#5d4037]/40">{run.duration_ms || '...'}ms</span>
              <span className="text-xs font-bold text-[#5d4037]/50 uppercase">{run.trigger}</span>
              <span className={`text-[10px] font-bold uppercase ${
                run.status === 'ok' ? 'text-emerald-600' :
                run.status === 'partial' ? 'text-amber-600' :
                run.status === 'pending' ? 'text-blue-500' :
                'text-red-600'}`}>{run.status}</span>
            </div>
          ))}
        </div>
      )}

      {/* Artifact list */}
      <div className="space-y-2">
        <div className="flex items-center justify-between">
          <h3 className="text-sm font-bold text-[#5d4037]/60 uppercase tracking-wider">
            Artifacts {filterKind && `(${KIND_LABELS[filterKind] || filterKind})`}
          </h3>
          <span className="text-xs text-[#5d4037]/40">{totalArtifacts} total</span>
        </div>

        {artifacts.length === 0 ? (
          <div className="text-center py-12 text-[#5d4037]/30 text-sm">
            {running ? 'Refinery is processing...' : 'No artifacts match the current filter.'}
          </div>
        ) : (
          artifacts.map(art => (
            <div
              key={art.id}
              onClick={() => setExpandedId(expandedId === art.id ? null : art.id)}
              className="bg-[#f4ece1]/30 rounded-lg border border-[#8b4513]/5 px-4 py-3 cursor-pointer hover:bg-[#f4ece1]/60 transition-colors"
            >
              <div className="flex items-center gap-3">
                <span
                  className="w-3 h-3 rounded-full shrink-0"
                  style={{ backgroundColor: KIND_COLORS[art.kind] || '#888' }}
                />
                <span className="text-sm font-bold text-[#5d4037] flex-1 truncate">{art.name}</span>
                <span className="text-[10px] font-bold uppercase tracking-wider px-1.5 py-0.5 rounded"
                  style={{ color: STATUS_COLORS[art.status] || '#888', backgroundColor: (STATUS_COLORS[art.status] || '#888') + '18' }}>
                  {art.status}
                </span>
                <span className="text-[10px] font-bold text-[#5d4037]/40 uppercase tracking-wider">{art.kind}</span>
                <span className="text-[10px] text-[#5d4037]/30 font-mono">v{art.version}</span>
                <span className="text-[10px] text-[#5d4037]/30">
                  {(art.confidence * 100).toFixed(0)}%
                </span>
              </div>
              <p className="text-xs text-[#5d4037]/50 mt-1 line-clamp-2">{art.summary}</p>

              {expandedId === art.id && (
                <div className="mt-3 pt-3 border-t border-[#8b4513]/10 space-y-2">
                  <div className="grid grid-cols-4 gap-2 text-xs">
                    <div className="bg-[#8b4513]/5 rounded px-2 py-1">
                      <span className="text-[#5d4037]/40">Hit</span>
                      <span className="ml-1 font-bold text-[#5d4037]">{art.hit_count}</span>
                    </div>
                    <div className="bg-[#8b4513]/5 rounded px-2 py-1">
                      <span className="text-[#5d4037]/40">Used</span>
                      <span className="ml-1 font-bold text-[#5d4037]">{art.usage_count}</span>
                    </div>
                    <div className="bg-emerald-500/10 rounded px-2 py-1">
                      <span className="text-emerald-700/60">✓</span>
                      <span className="ml-1 font-bold text-emerald-700">{art.success_count}</span>
                    </div>
                    <div className="bg-red-500/10 rounded px-2 py-1">
                      <span className="text-red-700/60">✗</span>
                      <span className="ml-1 font-bold text-red-700">{art.failure_count}</span>
                    </div>
                  </div>
                  <details className="text-xs">
                    <summary className="text-[#5d4037]/40 cursor-pointer hover:text-[#5d4037]/70">Payload</summary>
                    <pre className="mt-1 p-2 bg-[#5d4037]/5 rounded text-[#5d4037]/70 overflow-x-auto whitespace-pre-wrap break-all">
                      {JSON.stringify(JSON.parse(art.payload || '{}'), null, 2)}
                    </pre>
                  </details>
                  <div className="text-[10px] text-[#5d4037]/30">
                    Produced by <span className="font-mono">{art.produced_by}</span> · Created {new Date(art.created_at).toLocaleString()}
                  </div>
                  {/* Status actions */}
                  <div className="flex gap-2 mt-2">
                    {art.status !== 'active' && (
                      <button onClick={e => { e.stopPropagation(); handleStatusChange(art.id, 'active') }}
                        className="px-2 py-0.5 text-[10px] font-bold rounded bg-emerald-500/20 text-emerald-700 hover:bg-emerald-500/30">
                        ✓ Activate
                      </button>
                    )}
                    {art.status !== 'deprecated' && (
                      <button onClick={e => { e.stopPropagation(); handleStatusChange(art.id, 'deprecated') }}
                        className="px-2 py-0.5 text-[10px] font-bold rounded bg-gray-500/20 text-gray-600 hover:bg-gray-500/30">
                        ↓ Deprecate
                      </button>
                    )}
                    {art.status !== 'rejected' && (
                      <button onClick={e => { e.stopPropagation(); handleStatusChange(art.id, 'rejected') }}
                        className="px-2 py-0.5 text-[10px] font-bold rounded bg-red-500/20 text-red-700 hover:bg-red-500/30">
                        ✗ Reject
                      </button>
                    )}
                  </div>
                </div>
              )}
            </div>
          ))
        )}
      </div>
    </div>
  )
}
