import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { prApi } from '../api/endpoints'

/**
 * ChiefQueueCompact — the 200px "peek at Chief's desk" card that lives
 * on OverviewPage's left column. Shows the top-3 PRs waiting on a
 * decision with their recommended_action glyph, so operators don't
 * have to navigate to ChiefPage just to check if there's anything
 * waiting.
 *
 * Click-through lands on the full queue tab.
 */

type PR = {
  id: string
  title: string
  status: string
  tech_review?: string
}

function parseAction(raw?: string): string | undefined {
  if (!raw) return undefined
  try {
    const v = JSON.parse(raw)
    if (typeof v === 'string') {
      try { return JSON.parse(v).recommended_action } catch { return undefined }
    }
    return v?.recommended_action
  } catch { return undefined }
}

const actionGlyphs: Record<string, { icon: string; label: string; color: string }> = {
  auto_advance:      { icon: '🚀', label: 'auto',    color: 'text-emerald-700' },
  escalate_to_human: { icon: '🖐️', label: 'human',   color: 'text-amber-700' },
  request_changes:   { icon: '↩️', label: 'changes', color: 'text-rose-600' },
}

export function ChiefQueueCompact() {
  const [pending, setPending] = useState<PR[]>([])
  const [loaded, setLoaded] = useState(false)
  const navigate = useNavigate()

  const load = async () => {
    const res = await prApi.list()
    if (res.success) {
      const prs = (res.data?.pull_requests || []) as PR[]
      setPending(prs.filter((p) => p.status === 'pending_human_review' || p.status === 'pending_human_merge'))
    }
    setLoaded(true)
  }

  useEffect(() => {
    load()
    const i = setInterval(load, 15000)
    return () => clearInterval(i)
  }, [])

  const visible = pending.slice(0, 3)
  const extra = Math.max(0, pending.length - visible.length)
  const count = pending.length

  return (
    <div
      onClick={() => navigate('/chief')}
      className="parchment border border-[#8b4513]/20 rounded-2xl p-4 shadow-md cursor-pointer hover:shadow-lg hover:-translate-y-0.5 transition-all"
    >
      <div className="flex items-center justify-between mb-2">
        <div className="flex items-center gap-2">
          <span className="text-lg">🤖</span>
          <span className="font-marker text-sm text-[#5d4037]">Chief's Desk</span>
        </div>
        <span className={`inline-flex items-center gap-1 text-[10px] font-marker uppercase tracking-widest ${
          count > 0 ? 'text-amber-700' : 'text-[#8b4513]/40'
        }`}>
          <span className={`w-1.5 h-1.5 rounded-full ${count > 0 ? 'bg-amber-500 animate-pulse shadow-[0_0_6px_rgba(245,158,11,0.6)]' : 'bg-[#8b4513]/30'}`} />
          {count} pending
        </span>
      </div>

      {!loaded ? (
        <p className="font-hand text-xs text-[#8b4513]/40 italic py-2">Peeking at the desk...</p>
      ) : visible.length === 0 ? (
        <p className="font-hand text-xs text-[#8b4513]/50 italic py-2">☕ All quiet — nothing waiting.</p>
      ) : (
        <div className="space-y-1.5">
          {visible.map((pr) => {
            const act = parseAction(pr.tech_review)
            const g = (act && actionGlyphs[act]) || { icon: '•', label: '', color: 'text-[#8b4513]/50' }
            return (
              <div key={pr.id} className="flex items-center gap-2 px-2 py-1 rounded-lg hover:bg-[#8b4513]/5">
                <span className={`text-sm ${g.color}`}>{g.icon}</span>
                {g.label && (
                  <span className={`text-[9px] font-marker uppercase tracking-wider ${g.color}`}>{g.label}</span>
                )}
                <span className="font-hand text-xs text-[#5d4037] truncate flex-1">{pr.title}</span>
              </div>
            )
          })}
          {extra > 0 && (
            <p className="text-[10px] font-hand text-[#8b4513]/40 italic pl-2">+ {extra} more…</p>
          )}
        </div>
      )}

      <p className="mt-3 text-[10px] font-hand text-[#8b4513]/40 text-right">Open queue →</p>
    </div>
  )
}
