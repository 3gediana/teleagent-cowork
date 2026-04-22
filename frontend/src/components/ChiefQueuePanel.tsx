import { useEffect, useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { prApi } from '../api/endpoints'
import { PREvaluationCard } from './PREvaluationCard'
import { usePolicyMatcher, type PolicyMatchContext } from '../hooks/usePolicyMatcher'
import { useAppStore } from '../stores/appStore'

/**
 * ChiefQueuePanel is the "Chief's desk" view — every PR currently
 * blocked on a human (pending_human_review / pending_human_merge),
 * paired with Evaluate's verdict and the policy Chief *would* match
 * against if AutoMode were on.
 *
 * This is the first UI where the C' governance refactor becomes
 * legible: you see Evaluate making the technical call, the policy that
 * encodes the human's risk preference, and the action Chief would take.
 * Mirrors the cabin aesthetic used everywhere else.
 */

type PR = {
  id: string
  project_id: string
  branch_id: string
  title: string
  description?: string
  status: string
  submitter_id?: string
  self_review?: string
  tech_review?: string
  biz_review?: string
  diff_stat?: string
  version_suggestion?: string
  created_at: string
}

type TechLite = {
  result?: string
  merge_cost_rating?: 'low' | 'medium' | 'high' | string
  recommended_action?: 'auto_advance' | 'escalate_to_human' | 'request_changes' | string
}

function parseTech(raw?: string): TechLite | null {
  if (!raw) return null
  try {
    const v = JSON.parse(raw)
    if (typeof v === 'string') {
      try { return JSON.parse(v) } catch { return null }
    }
    return v
  } catch { return null }
}

function filePathsFromDiffStat(raw?: string): string[] {
  // diff_stat is stored as JSON: [{"path": "foo.go", ...}, ...].
  // Some historical rows stored plain text — be defensive.
  if (!raw) return []
  try {
    const parsed = JSON.parse(raw)
    if (Array.isArray(parsed)) {
      return parsed
        .map((e) => (typeof e === 'object' && e && 'path' in e ? String((e as { path: unknown }).path) : ''))
        .filter(Boolean)
    }
  } catch { /* ignore */ }
  return []
}

function timeAgo(ts: string): string {
  const d = new Date(ts).getTime()
  const now = Date.now()
  const s = Math.max(0, Math.floor((now - d) / 1000))
  if (s < 60) return `${s}s ago`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m ago`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h}h ago`
  return new Date(ts).toLocaleDateString()
}

function ActionPreview({
  matched,
  recommendedAction,
}: {
  matched: ReturnType<ReturnType<typeof usePolicyMatcher>['match']>
  recommendedAction?: string
}) {
  // The two layers feeding Chief's decision: Evaluate's recommendation
  // and the user-authored policy override. Show both; the human reads
  // top-to-bottom and understands why Chief will act the way it will.
  const autoModeHint = recommendedAction === 'auto_advance' && (!matched || (matched.actions.auto_approve === true))
    ? { label: 'AutoMode → auto-approve', color: 'text-emerald-700', icon: '🚀' }
    : recommendedAction === 'escalate_to_human' || matched?.actions.require_human === true
    ? { label: 'AutoMode → wait for human', color: 'text-amber-700', icon: '🖐️' }
    : recommendedAction === 'request_changes'
    ? { label: 'AutoMode → send back', color: 'text-rose-700', icon: '↩️' }
    : { label: 'AutoMode → undecided', color: 'text-[#8b4513]/60', icon: '❔' }

  return (
    <div className="flex flex-wrap items-center gap-3 text-[11px] font-hand">
      {matched ? (
        <span className="inline-flex items-center gap-1 px-2 py-1 rounded-lg bg-[#5d4037]/5 border border-[#8b4513]/20">
          📜 Policy&nbsp;
          <strong className="font-marker text-[#5d4037]">{matched.policy.name}</strong>
          <span className="font-mono text-[10px] text-[#8b4513]/60">(P{matched.policy.priority})</span>
        </span>
      ) : (
        <span className="italic text-[#8b4513]/40">No matching policy</span>
      )}
      <span className={`inline-flex items-center gap-1 ${autoModeHint.color}`}>
        <span>{autoModeHint.icon}</span>
        <strong className="font-marker tracking-wider text-[11px]">{autoModeHint.label}</strong>
      </span>
    </div>
  )
}

export function ChiefQueuePanel() {
  const [prs, setPRs] = useState<PR[]>([])
  const [loading, setLoading] = useState(true)
  const [actionLoading, setActionLoading] = useState<string | null>(null)
  const [rejectingId, setRejectingId] = useState<string | null>(null)
  const [rejectReason, setRejectReason] = useState('')
  const { match: matchPolicy, loading: policiesLoading } = usePolicyMatcher()
  const autoMode = useAppStore((s) => s.autoMode)

  const load = async () => {
    setLoading(true)
    const res = await prApi.list()
    if (res.success) setPRs(res.data?.pull_requests || [])
    setLoading(false)
  }

  useEffect(() => {
    load()
    const i = setInterval(load, 10000)
    return () => clearInterval(i)
  }, [])

  const pending = useMemo(
    () => prs.filter((pr) => pr.status === 'pending_human_review' || pr.status === 'pending_human_merge'),
    [prs],
  )

  const handleApprove = async (pr: PR) => {
    setActionLoading(pr.id)
    if (pr.status === 'pending_human_review') await prApi.approveReview(pr.id)
    else if (pr.status === 'pending_human_merge') await prApi.approveMerge(pr.id)
    await load()
    setActionLoading(null)
  }

  const handleReject = async (pr: PR) => {
    setActionLoading(pr.id)
    await prApi.reject(pr.id, rejectReason || undefined)
    setRejectingId(null)
    setRejectReason('')
    await load()
    setActionLoading(null)
  }

  if (loading && pending.length === 0) {
    return (
      <div className="text-center py-16 text-[#8b4513]/40">
        <p className="text-4xl mb-3 opacity-30">⏳</p>
        <p className="font-marker text-lg">Looking at the desk...</p>
      </div>
    )
  }

  if (pending.length === 0) {
    return (
      <div className="text-center py-20 text-[#8b4513]/30">
        <p className="text-5xl mb-4 opacity-40">☕</p>
        <p className="font-marker text-lg text-[#8b4513]/60">All quiet.</p>
        <p className="font-hand text-sm mt-1 text-[#8b4513]/50">Chief's got nothing on the desk.</p>
      </div>
    )
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between mb-2 px-1">
        <div>
          <h3 className="font-marker text-lg text-[#5d4037]">🗂️ Chief's Desk</h3>
          <p className="font-hand text-xs text-[#8b4513]/50 mt-0.5">
            {pending.length} PR{pending.length === 1 ? '' : 's'} waiting on a decision.
          </p>
        </div>
        <span className={`inline-flex items-center gap-2 px-3 py-1.5 rounded-lg border text-[10px] font-marker uppercase tracking-widest ${
          autoMode
            ? 'bg-emerald-600 text-emerald-50 border-emerald-700 shadow-[0_0_12px_rgba(16,185,129,0.4)]'
            : 'bg-[#8b4513]/10 text-[#8b4513]/60 border-[#8b4513]/20'
        }`}>
          <span className={`w-1.5 h-1.5 rounded-full ${autoMode ? 'bg-emerald-200 animate-pulse' : 'bg-[#8b4513]/40'}`} />
          AutoMode {autoMode ? 'ON' : 'OFF'}
        </span>
      </div>

      {pending.map((pr) => {
        const tech = parseTech(pr.tech_review)
        const filePaths = filePathsFromDiffStat(pr.diff_stat)
        const ctx: PolicyMatchContext = {
          scope: pr.status === 'pending_human_merge' ? 'pr_merge' : 'pr_review',
          file_count: filePaths.length || undefined,
          file_paths: filePaths.length > 0 ? filePaths : undefined,
          merge_cost: tech?.merge_cost_rating as 'low' | 'medium' | 'high' | undefined,
          submitter: pr.submitter_id,
        }
        const matched = policiesLoading ? null : matchPolicy(ctx)
        const isRejecting = rejectingId === pr.id
        const stageLabel = pr.status === 'pending_human_review' ? 'Awaiting Review' : 'Awaiting Merge'

        return (
          <div key={pr.id} className="parchment rounded-2xl p-5 border border-[#8b4513]/20 shadow-md hover:shadow-lg transition-shadow">
            <div className="flex items-start justify-between gap-3 mb-3">
              <div className="min-w-0">
                <h4 className="font-marker text-base text-[#5d4037] truncate">
                  {pr.title}
                  <span className="ml-2 text-[10px] font-mono bg-black/5 text-[#8b4513]/50 px-2 py-0.5 rounded align-middle">
                    #{pr.id.slice(0, 8)}
                  </span>
                </h4>
                <p className="font-hand text-xs text-[#8b4513]/50 mt-1">
                  Filed {timeAgo(pr.created_at)}
                  {pr.submitter_id && <> by <span className="font-mono">@{pr.submitter_id.slice(0, 8)}</span></>}
                  {filePaths.length > 0 && <> · {filePaths.length} file{filePaths.length === 1 ? '' : 's'}</>}
                </p>
              </div>
              <span className="inline-flex items-center gap-1 px-2.5 py-1 rounded-lg border-2 bg-amber-50 text-amber-700 border-amber-300 font-marker text-[10px] uppercase tracking-wider whitespace-nowrap shrink-0 rotate-1">
                ⏳ {stageLabel}
              </span>
            </div>

            {/* Structured evaluation summary */}
            {(pr.tech_review || pr.biz_review) && (
              <div className="mb-3">
                <PREvaluationCard techReview={pr.tech_review} bizReview={pr.biz_review} />
              </div>
            )}

            {/* Policy + AutoMode preview */}
            <div className="mb-3 px-3 py-2 rounded-lg bg-[#8b4513]/5 border border-[#8b4513]/10">
              <ActionPreview matched={matched} recommendedAction={tech?.recommended_action} />
            </div>

            {/* Action row */}
            {!isRejecting ? (
              <div className="flex items-center gap-2 flex-wrap">
                <button
                  onClick={() => handleApprove(pr)}
                  disabled={actionLoading === pr.id}
                  className="px-4 py-2 bg-[#5d4037] text-[#efebe9] font-marker text-xs rounded-xl shadow-md hover:bg-[#4e342e] hover:-translate-y-0.5 transition-all active:scale-95 disabled:opacity-50 border-b-4 border-[#3e2723]"
                >
                  ✓ {pr.status === 'pending_human_review' ? 'Approve Review' : 'Approve Merge'}
                </button>
                <button
                  onClick={() => { setRejectingId(pr.id); setRejectReason('') }}
                  disabled={actionLoading === pr.id}
                  className="px-4 py-2 bg-rose-100 text-rose-700 font-marker text-xs rounded-xl border-2 border-rose-300 hover:bg-rose-200 hover:-translate-y-0.5 transition-all active:scale-95 disabled:opacity-50"
                >
                  ↩ Reject
                </button>
                <Link
                  to={`/prs`}
                  className="ml-auto text-[11px] font-hand text-[#8b4513]/50 hover:text-[#5d4037] underline decoration-dotted"
                >
                  View full PR →
                </Link>
              </div>
            ) : (
              <div className="space-y-2 p-3 rounded-xl bg-rose-50/60 border border-rose-200">
                <textarea
                  value={rejectReason}
                  onChange={(e) => setRejectReason(e.target.value)}
                  placeholder="Why are you rejecting this PR? (shown to the submitter)"
                  className="w-full bg-white/80 border border-rose-200 rounded-lg px-3 py-2 text-xs font-hand text-[#3e2723] placeholder-rose-300 shadow-inner focus:ring-2 focus:ring-rose-400 outline-none resize-y min-h-[60px]"
                  rows={2}
                />
                <div className="flex items-center gap-2">
                  <button
                    onClick={() => handleReject(pr)}
                    disabled={actionLoading === pr.id}
                    className="px-4 py-1.5 bg-rose-500 text-white font-marker text-xs rounded-lg shadow hover:bg-rose-600 transition-colors active:scale-95 disabled:opacity-50"
                  >
                    Confirm Reject
                  </button>
                  <button
                    onClick={() => { setRejectingId(null); setRejectReason('') }}
                    className="px-4 py-1.5 bg-white text-[#8b4513]/70 font-marker text-xs rounded-lg border border-[#8b4513]/20 hover:bg-[#f4ece1] transition-colors"
                  >
                    Cancel
                  </button>
                </div>
              </div>
            )}
          </div>
        )
      })}
    </div>
  )
}
