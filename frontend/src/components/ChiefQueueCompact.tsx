import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { prApi } from '../api/endpoints'

/**
 * ChiefQueueCompact — the "peek at Chief's desk" card that lives on
 * OverviewPage's left column.  Shows the top-3 PRs waiting on a human
 * decision together with each PR's recommended_action, so operators
 * don't have to navigate to ChiefPage just to see if there's anything
 * to triage.  Click-through lands on the full queue tab.
 *
 * Visual: dark surface card, colored row per action (SVG icon + label).
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

type ActionStyle = { label: string; tone: string; iconColor: string; bg: string }

const actions: Record<string, ActionStyle> = {
  auto_advance:      { label: 'auto',    tone: '#6ee7b7', iconColor: '#10b981', bg: 'rgba(16,185,129,0.08)' },
  escalate_to_human: { label: 'human',   tone: '#fcd34d', iconColor: '#f59e0b', bg: 'rgba(245,158,11,0.08)' },
  request_changes:   { label: 'changes', tone: '#fda4af', iconColor: '#f43f5e', bg: 'rgba(244,63,94,0.08)' },
}

function ActionIcon({ action }: { action: string }) {
  const common = { width: 11, height: 11, viewBox: '0 0 24 24', fill: 'none', stroke: 'currentColor', strokeWidth: 2, strokeLinecap: 'round' as const, strokeLinejoin: 'round' as const }
  if (action === 'auto_advance') return (<svg {...common}><path d="m13 2-9 10h7l-1 10 9-10h-7z"/></svg>)
  if (action === 'escalate_to_human') return (<svg {...common}><path d="M12 8v4"/><circle cx="12" cy="16" r="0.5" fill="currentColor"/><circle cx="12" cy="12" r="9"/></svg>)
  if (action === 'request_changes') return (<svg {...common}><polyline points="1 4 1 10 7 10"/><path d="M3.51 15a9 9 0 1 0 2.13-9.36L1 10"/></svg>)
  return (<svg {...common}><circle cx="12" cy="12" r="1.5" fill="currentColor"/></svg>)
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
    <button
      onClick={() => navigate('/chief')}
      className="surface-1 p-4 text-left w-full transition-all hover:border-[var(--border-hover)] hover:-translate-y-0.5"
    >
      <div className="flex items-center justify-between mb-3">
        <div className="flex items-center gap-1.5">
          <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="#a5b4fc" strokeWidth="1.8">
            <path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z"/>
          </svg>
          <h3 className="text-[10px] font-semibold uppercase tracking-[0.08em]" style={{ color: 'var(--text-2)' }}>
            Chief&apos;s Desk
          </h3>
        </div>
        <span
          className="chip font-mono-jb text-[10.5px]"
          style={count > 0
            ? { background: 'rgba(245,158,11,0.08)', borderColor: 'rgba(245,158,11,0.22)', color: '#fcd34d' }
            : undefined}
        >
          <span
            className="w-1.5 h-1.5 rounded-full"
            style={{
              background: count > 0 ? '#f59e0b' : '#3f3f46',
              boxShadow: count > 0 ? '0 0 6px rgba(245,158,11,0.6)' : undefined,
              animation: count > 0 ? 'status-pulse 2s ease-in-out infinite' : undefined,
            }}
          />
          {count} pending
        </span>
      </div>

      {!loaded ? (
        <p className="text-[12px] py-2" style={{ color: 'var(--text-2)', fontStyle: 'italic' }}>Peeking at the desk…</p>
      ) : visible.length === 0 ? (
        <p className="text-[12px] py-2" style={{ color: 'var(--text-2)', fontStyle: 'italic' }}>All quiet — nothing waiting.</p>
      ) : (
        <div className="space-y-1">
          {visible.map((pr) => {
            const act = parseAction(pr.tech_review)
            const style = (act && actions[act])
            return (
              <div
                key={pr.id}
                className="flex items-center gap-2 px-2 py-1.5 rounded-md transition-colors"
                style={style ? { background: style.bg } : { background: 'rgba(255,255,255,0.02)' }}
              >
                {style ? (
                  <span style={{ color: style.iconColor }}><ActionIcon action={act!} /></span>
                ) : (
                  <span style={{ color: 'var(--text-2)' }}><ActionIcon action="" /></span>
                )}
                {style && (
                  <span
                    className="text-[9px] font-semibold uppercase tracking-[0.08em] font-mono-jb"
                    style={{ color: style.tone }}
                  >
                    {style.label}
                  </span>
                )}
                <span className="text-[12px] truncate flex-1" style={{ color: 'var(--text-1)' }} title={pr.title}>
                  {pr.title}
                </span>
              </div>
            )
          })}
          {extra > 0 && (
            <p className="text-[10.5px] pl-2 pt-0.5" style={{ color: 'var(--text-2)' }}>+ {extra} more…</p>
          )}
        </div>
      )}

      <p className="mt-3 text-[10.5px] text-right flex items-center justify-end gap-1" style={{ color: 'var(--text-2)' }}>
        Open queue
        <svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.2" strokeLinecap="round" strokeLinejoin="round"><polyline points="9 18 15 12 9 6"/></svg>
      </p>
    </button>
  )
}
