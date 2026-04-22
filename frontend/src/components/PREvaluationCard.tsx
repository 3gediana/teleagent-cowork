import { useState } from 'react'

/**
 * PREvaluationCard renders the Evaluate Agent's `evaluate_output` verdict
 * (stored on the PR row as `tech_review` JSON) in a structured, human-
 * readable form. Evaluate is the platform's primary technical decision-
 * maker after the C' governance refactor — its `result`, `merge_cost_rating`,
 * and especially `recommended_action` directly drive Chief's AutoMode path.
 * Exposing them as raw JSON (the old behaviour) buried the signal that
 * matters most; this card surfaces it.
 *
 * Fallback path: if `tech_review` is missing the new `recommended_action`
 * field (historical PRs), we still render whatever's there as a raw JSON
 * block, so no migration is required.
 *
 * Aesthetic: cabin scrapbook. Parchment background, saddle-brown borders,
 * handwriting fonts, leather-tag action badges.
 */
type TechReviewParsed = {
  result?: 'approved' | 'needs_work' | 'conflicts' | 'high_risk' | string
  merge_cost_rating?: 'low' | 'medium' | 'high' | string
  recommended_action?: 'auto_advance' | 'escalate_to_human' | 'request_changes' | string
  reason?: string
  conflict_files?: string[]
  quality_patterns?: string
  common_mistakes?: string
  [key: string]: unknown
}

type BizReviewParsed = {
  result?: 'approved' | 'needs_changes' | 'rejected' | string
  biz_review?: string
  version_suggestion?: string
  alignment_rationale?: string
  [key: string]: unknown
}

// safeParse keeps the card resilient to malformed / legacy payloads —
// some older PRs stored plain strings, some stored stringified-stringified
// JSON. We try the common shapes and then give up gracefully.
function safeParse<T>(raw: unknown): T | null {
  if (!raw) return null
  if (typeof raw === 'object') return raw as T
  if (typeof raw !== 'string') return null
  try {
    const first = JSON.parse(raw)
    if (typeof first === 'string') {
      // Double-stringified — unwrap once more.
      try { return JSON.parse(first) as T } catch { return null }
    }
    return first as T
  } catch {
    return null
  }
}

// ---- Result chip (leftmost) ----
function ResultChip({ result }: { result?: string }) {
  if (!result) return null
  const r = result.toLowerCase()
  const styles: Record<string, { bg: string; text: string; border: string; label: string; icon: string }> = {
    approved:     { bg: 'bg-emerald-50', text: 'text-emerald-700', border: 'border-emerald-300', label: 'APPROVED',   icon: '✅' },
    needs_work:   { bg: 'bg-rose-50',    text: 'text-rose-700',    border: 'border-rose-300',    label: 'NEEDS WORK', icon: '✍️' },
    conflicts:    { bg: 'bg-amber-50',   text: 'text-amber-700',   border: 'border-amber-400',   label: 'CONFLICTS',  icon: '⚡' },
    high_risk:    { bg: 'bg-red-50',     text: 'text-red-700',     border: 'border-red-400',     label: 'HIGH RISK',  icon: '⚠️' },
  }
  const s = styles[r] || { bg: 'bg-[#8b4513]/10', text: 'text-[#5d4037]', border: 'border-[#8b4513]/30', label: result.toUpperCase(), icon: '•' }
  return (
    <span className={`inline-flex items-center gap-1.5 px-3 py-1 rounded-lg border-2 ${s.bg} ${s.text} ${s.border} font-marker text-[11px] tracking-wider shadow-sm`}>
      <span>{s.icon}</span>
      {s.label}
    </span>
  )
}

// ---- Merge-cost pill (middle) ----
function CostPill({ cost }: { cost?: string }) {
  if (!cost) return null
  const c = cost.toLowerCase()
  const colors: Record<string, string> = {
    low:    'bg-emerald-100/60 text-emerald-700 border-emerald-300/60',
    medium: 'bg-amber-100/60   text-amber-700   border-amber-300/60',
    high:   'bg-rose-100/60    text-rose-700    border-rose-300/60',
  }
  return (
    <span className={`inline-flex items-center gap-1 px-2.5 py-1 rounded-md border text-[10px] font-mono uppercase tracking-widest ${colors[c] || 'bg-[#8b4513]/10 text-[#5d4037] border-[#8b4513]/20'}`}>
      cost&nbsp;<strong className="font-bold">{c || '?'}</strong>
    </span>
  )
}

// ---- Recommended-action leather tag (rightmost — the big signal) ----
function ActionTag({ action }: { action?: string }) {
  if (!action) return null
  const styles: Record<string, { bg: string; text: string; border: string; ring: string; icon: string; label: string; rotate: string }> = {
    auto_advance: {
      bg: 'bg-emerald-600', text: 'text-emerald-50', border: 'border-emerald-700',
      ring: 'ring-2 ring-emerald-400/40 shadow-[0_0_20px_rgba(16,185,129,0.35)]',
      icon: '🚀', label: 'AUTO ADVANCE', rotate: '-rotate-2',
    },
    escalate_to_human: {
      bg: 'bg-amber-100', text: 'text-amber-800', border: 'border-amber-400',
      ring: '', icon: '🖐️', label: 'ESCALATE', rotate: 'rotate-1',
    },
    request_changes: {
      bg: 'bg-rose-100', text: 'text-rose-700', border: 'border-rose-300',
      ring: '', icon: '↩️', label: 'SEND BACK', rotate: '-rotate-1',
    },
  }
  const s = styles[action] || styles.escalate_to_human
  return (
    <span
      className={`inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg border-2 font-marker text-[11px] tracking-wider ${s.bg} ${s.text} ${s.border} ${s.ring} ${s.rotate} shadow-md transform transition-transform hover:rotate-0`}
    >
      <span className="text-sm">{s.icon}</span>
      {s.label}
    </span>
  )
}

// ---- Biz review result chip ----
function BizResultChip({ result }: { result?: string }) {
  if (!result) return null
  const r = result.toLowerCase()
  const styles: Record<string, { bg: string; text: string; border: string; label: string; icon: string }> = {
    approved:      { bg: 'bg-indigo-50', text: 'text-indigo-700', border: 'border-indigo-300', label: 'ALIGNED',       icon: '🎯' },
    needs_changes: { bg: 'bg-amber-50',  text: 'text-amber-700',  border: 'border-amber-300',  label: 'NEEDS CHANGES', icon: '✂️' },
    rejected:      { bg: 'bg-rose-50',   text: 'text-rose-700',   border: 'border-rose-300',   label: 'OFF-DIRECTION', icon: '🚫' },
  }
  const s = styles[r] || { bg: 'bg-[#8b4513]/10', text: 'text-[#5d4037]', border: 'border-[#8b4513]/30', label: result.toUpperCase(), icon: '•' }
  return (
    <span className={`inline-flex items-center gap-1.5 px-3 py-1 rounded-lg border-2 ${s.bg} ${s.text} ${s.border} font-marker text-[11px] tracking-wider shadow-sm`}>
      <span>{s.icon}</span>
      {s.label}
    </span>
  )
}

export function PREvaluationCard({
  techReview,
  bizReview,
}: {
  techReview?: unknown
  bizReview?: unknown
}) {
  const [showRawTech, setShowRawTech] = useState(false)
  const [showRawBiz, setShowRawBiz] = useState(false)

  const tech = safeParse<TechReviewParsed>(techReview)
  const biz = safeParse<BizReviewParsed>(bizReview)

  if (!tech && !biz) return null

  return (
    <div className="space-y-4">
      {/* ---- Tech review block ---- */}
      {tech && (
        <div className="p-4 bg-[#f4ece1]/60 rounded-xl border border-[#8b4513]/15 shadow-inner">
          <div className="flex items-center gap-2 mb-3">
            <span className="text-[9px] font-marker text-[#8b4513]/40 uppercase tracking-widest">Tech Evaluation</span>
            <span className="text-[10px] font-hand text-[#8b4513]/30 italic">— Evaluate Agent</span>
          </div>

          <div className="flex flex-wrap items-center gap-2 mb-3">
            <ResultChip result={tech.result} />
            <CostPill cost={tech.merge_cost_rating} />
            <ActionTag action={tech.recommended_action} />
          </div>

          {tech.reason && (
            <p className="font-hand text-sm text-[#3e2723] bg-white/50 px-3 py-2 rounded-lg border border-[#8b4513]/10 italic mb-2">
              "{tech.reason}"
            </p>
          )}

          {tech.conflict_files && tech.conflict_files.length > 0 && (
            <div className="mb-2">
              <p className="text-[9px] font-marker text-amber-700/70 uppercase tracking-widest mb-1">⚡ Conflict files</p>
              <ul className="text-xs font-mono text-[#5d4037]/80 pl-4 list-disc">
                {tech.conflict_files.map((f) => (
                  <li key={f} className="truncate">{f}</li>
                ))}
              </ul>
            </div>
          )}

          {tech.quality_patterns && (
            <p className="font-hand text-[11px] text-emerald-700/80 mb-1">
              ✨ Pattern worth learning: <span className="italic">{tech.quality_patterns}</span>
            </p>
          )}
          {tech.common_mistakes && (
            <p className="font-hand text-[11px] text-rose-600/80 mb-1">
              ⚠️ Anti-pattern observed: <span className="italic">{tech.common_mistakes}</span>
            </p>
          )}

          {/* Legacy fallback — some PRs pre-date the structured schema */}
          {!tech.recommended_action && !tech.result && (
            <pre className="text-xs text-[#5d4037]/70 font-mono whitespace-pre-wrap max-h-32 overflow-auto bg-white/40 p-2 rounded">
              {typeof techReview === 'string' ? techReview : JSON.stringify(techReview, null, 2)}
            </pre>
          )}

          <button
            onClick={() => setShowRawTech((v) => !v)}
            className="mt-2 text-[10px] font-hand text-[#8b4513]/50 hover:text-[#8b4513] transition-colors"
          >
            {showRawTech ? '▾ Hide raw JSON' : '▸ Show raw JSON'}
          </button>
          {showRawTech && (
            <pre className="mt-2 text-[11px] text-[#5d4037]/60 font-mono whitespace-pre-wrap max-h-48 overflow-auto bg-white/40 p-2 rounded border border-[#8b4513]/10">
              {JSON.stringify(tech, null, 2)}
            </pre>
          )}
        </div>
      )}

      {/* ---- Biz review block ---- */}
      {biz && (
        <div className="p-4 bg-indigo-50/40 rounded-xl border border-indigo-200/60 shadow-inner">
          <div className="flex items-center gap-2 mb-3">
            <span className="text-[9px] font-marker text-indigo-400 uppercase tracking-widest">Business Evaluation</span>
            <span className="text-[10px] font-hand text-indigo-300 italic">— Maintain Agent</span>
          </div>

          <div className="flex flex-wrap items-center gap-2 mb-3">
            <BizResultChip result={biz.result} />
            {biz.version_suggestion && (
              <span className="inline-flex items-center gap-1 px-2.5 py-1 rounded-md border border-indigo-300/60 bg-indigo-100/50 text-indigo-700 text-[10px] font-mono uppercase tracking-widest">
                → <strong>{biz.version_suggestion}</strong>
              </span>
            )}
          </div>

          {biz.biz_review && (
            <p className="font-hand text-sm text-[#3e2723] bg-white/50 px-3 py-2 rounded-lg border border-indigo-100 italic mb-2">
              "{biz.biz_review}"
            </p>
          )}

          {biz.alignment_rationale && (
            <p className="font-hand text-[11px] text-indigo-700/70 mb-1">
              🎯 Alignment lesson: <span className="italic">{biz.alignment_rationale}</span>
            </p>
          )}

          <button
            onClick={() => setShowRawBiz((v) => !v)}
            className="mt-2 text-[10px] font-hand text-indigo-400/70 hover:text-indigo-600 transition-colors"
          >
            {showRawBiz ? '▾ Hide raw JSON' : '▸ Show raw JSON'}
          </button>
          {showRawBiz && (
            <pre className="mt-2 text-[11px] text-indigo-700/60 font-mono whitespace-pre-wrap max-h-48 overflow-auto bg-white/40 p-2 rounded border border-indigo-100">
              {JSON.stringify(biz, null, 2)}
            </pre>
          )}
        </div>
      )}
    </div>
  )
}
