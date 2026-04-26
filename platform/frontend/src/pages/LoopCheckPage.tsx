import { useEffect, useMemo, useState } from 'react'
import { useAppStore } from '../stores/appStore'
import {
  loopcheckApi,
  type LoopReport,
  type LoopPillar,
  type LoopCheck,
  type LoopStatus,
} from '../api/endpoints'

/**
 * LoopCheckPage — the health dashboard for self-evolution and
 * automation loops. Consumes /api/v1/loopcheck directly and renders
 * one card per check.
 *
 * Design principles:
 *   - No buttons that mutate state. This is read-only.
 *   - Status badges use the same colour mapping as the CLI so
 *     screenshots transfer cleanly.
 *   - Metrics collapse behind a toggle — most of the time the
 *     summary + last-activity line is enough.
 *   - Auto-polls every 60s (cheap query, safe cadence).
 */

const STATUS_STYLES: Record<
  LoopStatus,
  { bg: string; border: string; text: string; dot: string; label: string }
> = {
  healthy: {
    bg: 'bg-emerald-50',
    border: 'border-emerald-200',
    text: 'text-emerald-700',
    dot: 'bg-emerald-500',
    label: 'Healthy',
  },
  stale: {
    bg: 'bg-amber-50',
    border: 'border-amber-200',
    text: 'text-amber-700',
    dot: 'bg-amber-500',
    label: 'Stale',
  },
  unused: {
    bg: 'bg-slate-50',
    border: 'border-slate-200',
    text: 'text-slate-600',
    dot: 'bg-slate-400',
    label: 'Unused',
  },
  broken: {
    bg: 'bg-rose-50',
    border: 'border-rose-200',
    text: 'text-rose-700',
    dot: 'bg-rose-500',
    label: 'Broken',
  },
}

// Friendly names for the check-id strings coming out of the Go
// package. Keeping the mapping here lets non-developers rename the
// display labels without touching the backend.
const CHECK_LABELS: Record<string, string> = {
  feedback_to_experience: 'Feedback → Experience',
  experience_to_analyze: 'Experience → Analyze',
  skill_to_policy: 'Skill → Policy',
  policy_matching: 'Policy matching',
  artifact_injection: 'Artifact injection',
  refinery_pipeline: 'Refinery pipeline',
  heartbeat: 'Heartbeat',
  timer_activity: 'Timer activity',
  chief_auto_approval: 'Chief auto-approval',
  retry_mechanism: 'Retry mechanism',
  failure_modes: 'Failure modes',
}

export default function LoopCheckPage() {
  const { selectedProjectId } = useAppStore()
  const [report, setReport] = useState<LoopReport | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [scopeGlobal, setScopeGlobal] = useState(false)
  const [windowDays, setWindowDays] = useState(7)

  // One fetch per change of {scope, window, project}. We also kick
  // a poll on a 60s timer so the page stays fresh when left open.
  useEffect(() => {
    let cancelled = false
    const run = async () => {
      setLoading(true)
      setError(null)
      try {
        const scope = scopeGlobal ? '' : selectedProjectId
        const res = await loopcheckApi.get(scope || undefined, windowDays)
        if (cancelled) return
        if (res.success) setReport(res.data)
        else setError('Server reported an unsuccessful response.')
      } catch (e: any) {
        if (cancelled) return
        setError(e?.message || 'Network error fetching loopcheck.')
      } finally {
        if (!cancelled) setLoading(false)
      }
    }
    run()
    const iv = setInterval(run, 60_000)
    return () => {
      cancelled = true
      clearInterval(iv)
    }
  }, [selectedProjectId, scopeGlobal, windowDays])

  return (
    <div className="h-full flex flex-col space-y-6 overflow-auto pr-1">
      <header className="flex items-start justify-between gap-4">
        <div>
          <h1 className="text-2xl font-extrabold text-slate-800">
            Loop Health
          </h1>
          <p className="text-sm text-slate-500 mt-1">
            Are the self-evolution and automation loops actually flowing?
            Read-only diagnostic — mirrors{' '}
            <code className="px-1.5 py-0.5 bg-slate-100 rounded text-xs">
              go run ./experiments/loopcheck
            </code>
            .
          </p>
        </div>
        <Controls
          scopeGlobal={scopeGlobal}
          setScopeGlobal={setScopeGlobal}
          windowDays={windowDays}
          setWindowDays={setWindowDays}
        />
      </header>

      {loading && !report && (
        <div className="flex-1 flex items-center justify-center text-slate-400 text-sm">
          Running checks…
        </div>
      )}

      {error && (
        <div className="bg-rose-50 border border-rose-200 rounded-2xl p-4 text-sm text-rose-700">
          {error}
        </div>
      )}

      {report && (
        <>
          <OverallBanner report={report} loading={loading} />
          <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
            <PillarCard
              title="Self-Evolution"
              subtitle="Feedback → Experience → Analyze → Skill → Policy → Artifact"
              pillar={report.self_evolution}
            />
            <PillarCard
              title="Automation"
              subtitle="Heartbeat · Timers · Chief · Retry · Failure modes"
              pillar={report.automation}
            />
          </div>
        </>
      )}
    </div>
  )
}

function Controls(props: {
  scopeGlobal: boolean
  setScopeGlobal: (v: boolean) => void
  windowDays: number
  setWindowDays: (v: number) => void
}) {
  return (
    <div className="flex items-center gap-3 text-xs text-slate-600">
      <label className="flex items-center gap-2 cursor-pointer select-none">
        <input
          type="checkbox"
          checked={props.scopeGlobal}
          onChange={(e) => props.setScopeGlobal(e.target.checked)}
          className="h-4 w-4 rounded border-slate-300 text-indigo-500 focus:ring-indigo-400"
        />
        <span>Platform-wide</span>
      </label>
      <div className="h-6 w-px bg-slate-200" />
      <div className="flex items-center gap-1">
        <span className="text-slate-500">Window:</span>
        {[1, 7, 30].map((n) => (
          <button
            key={n}
            type="button"
            onClick={() => props.setWindowDays(n)}
            className={`px-2.5 py-1 rounded-md font-medium transition-colors ${
              props.windowDays === n
                ? 'bg-indigo-500 text-white'
                : 'text-slate-500 hover:bg-slate-100'
            }`}
          >
            {n}d
          </button>
        ))}
      </div>
    </div>
  )
}

function OverallBanner({
  report,
  loading,
}: {
  report: LoopReport
  loading: boolean
}) {
  const s = STATUS_STYLES[report.overall_status]
  const generated = new Date(report.generated_at)
  return (
    <div
      className={`rounded-3xl border ${s.border} ${s.bg} px-6 py-5 flex items-center justify-between`}
    >
      <div className="flex items-center gap-4">
        <span
          className={`inline-flex items-center gap-2 px-3 py-1 rounded-full ${s.text} bg-white/60 border ${s.border} font-semibold text-xs uppercase tracking-wider`}
        >
          <span className={`h-2 w-2 rounded-full ${s.dot}`} />
          {s.label}
        </span>
        <div className="text-sm text-slate-700">
          Window <span className="font-semibold">{report.window_days}d</span>,
          scope{' '}
          <span className="font-semibold">
            {report.project_id ? `project ${report.project_id}` : 'platform-wide'}
          </span>
          .
        </div>
      </div>
      <div className="text-xs text-slate-500">
        {loading ? 'Refreshing…' : `as of ${generated.toLocaleTimeString()}`}
      </div>
    </div>
  )
}

function PillarCard({
  title,
  subtitle,
  pillar,
}: {
  title: string
  subtitle: string
  pillar: LoopPillar
}) {
  const s = STATUS_STYLES[pillar.overall_status]
  return (
    <section className="bg-white rounded-3xl border border-slate-200 shadow-sm p-6 space-y-4">
      <header className="flex items-start justify-between">
        <div>
          <h2 className="text-lg font-bold text-slate-800">{title}</h2>
          <p className="text-xs text-slate-500 mt-0.5">{subtitle}</p>
        </div>
        <span
          className={`shrink-0 inline-flex items-center gap-1.5 px-2.5 py-1 rounded-full ${s.text} ${s.bg} border ${s.border} font-semibold text-xs`}
        >
          <span className={`h-1.5 w-1.5 rounded-full ${s.dot}`} />
          {s.label}
        </span>
      </header>

      <ul className="divide-y divide-slate-100">
        {pillar.checks.map((c) => (
          <CheckRow key={c.name} check={c} />
        ))}
      </ul>
    </section>
  )
}

function CheckRow({ check }: { check: LoopCheck }) {
  const [open, setOpen] = useState(false)
  const s = STATUS_STYLES[check.status]
  const label = CHECK_LABELS[check.name] || check.name
  const lastActivity = useMemo(() => humanAgo(check.last_activity), [
    check.last_activity,
  ])

  // Only non-healthy checks auto-expand their metric block on first
  // render; healthy rows stay collapsed because the summary alone
  // tells the full story.
  const hasMetrics = check.metrics && Object.keys(check.metrics).length > 0

  return (
    <li className="py-3">
      <div className="flex items-start gap-3">
        <span
          className={`mt-1 shrink-0 h-2.5 w-2.5 rounded-full ${s.dot}`}
          aria-label={s.label}
        />
        <div className="flex-1 min-w-0">
          <div className="flex items-baseline justify-between gap-3">
            <div className="font-semibold text-sm text-slate-800">{label}</div>
            {lastActivity && (
              <div className="text-xs text-slate-400 shrink-0">
                {lastActivity}
              </div>
            )}
          </div>
          <div className="text-sm text-slate-600 mt-0.5">{check.summary}</div>
          {hasMetrics && (
            <button
              type="button"
              onClick={() => setOpen((v) => !v)}
              className="mt-1.5 text-xs text-indigo-500 hover:text-indigo-700 font-medium"
            >
              {open ? 'Hide metrics' : 'Show metrics'}
            </button>
          )}
          {hasMetrics && open && (
            <dl className="mt-2 grid grid-cols-2 gap-x-6 gap-y-1 text-xs">
              {Object.entries(check.metrics!).map(([k, v]) => (
                <div key={k} className="flex justify-between gap-3">
                  <dt className="text-slate-500 truncate">{k}</dt>
                  <dd className="font-mono text-slate-800 shrink-0">
                    {renderMetric(v)}
                  </dd>
                </div>
              ))}
            </dl>
          )}
        </div>
      </div>
    </li>
  )
}

function renderMetric(v: any): string {
  if (v === null || v === undefined) return '—'
  if (typeof v === 'number') return v.toLocaleString()
  if (typeof v === 'boolean') return v ? 'yes' : 'no'
  if (typeof v === 'string') return v
  try {
    return JSON.stringify(v)
  } catch {
    return String(v)
  }
}

function humanAgo(iso: string | null | undefined): string | null {
  if (!iso) return null
  const d = new Date(iso)
  const delta = Math.max(0, (Date.now() - d.getTime()) / 1000)
  if (delta < 60) return `${Math.round(delta)}s ago`
  if (delta < 3600) return `${Math.round(delta / 60)}m ago`
  if (delta < 86400) return `${Math.round(delta / 3600)}h ago`
  return `${Math.round(delta / 86400)}d ago`
}
