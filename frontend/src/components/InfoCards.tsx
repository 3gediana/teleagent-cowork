import { useAppStore } from '../stores/appStore'

/**
 * Project-context cards for the Overview left column.
 *
 * Two shapes:
 *   - cream paper (InfoCard): narrative text — Direction, Milestone.
 *     Picks up the same stripe/pin language as TaskKanban so the
 *     sidebar feels like "notes pinned beside the board".
 *   - dark surface (VersionCard / AgentsCard / LocksCard): data-heavy
 *     cards with mono numbers and colored accents.
 */

type InfoCardProps = {
  title: string
  icon: React.ReactNode
  value: string | null
  onEdit?: () => void
  accentColor?: 'blue' | 'amber' | 'purple' | 'emerald' | 'rose'
}

/**
 * InfoCard now renders as a dark surface with a 3px vertical color
 * stripe on the left (Linear pattern). Cream paper cards are reserved
 * for the felt board — putting them in the dark sidebar makes the two
 * brightest elements on the page fight for attention.
 */
const accentStripe: Record<NonNullable<InfoCardProps['accentColor']>, string> = {
  blue:    'linear-gradient(180deg, #6366f1 0%, #4f46e5 100%)',
  amber:   'linear-gradient(180deg, #f59e0b 0%, #d97706 100%)',
  emerald: 'linear-gradient(180deg, #10b981 0%, #059669 100%)',
  rose:    'linear-gradient(180deg, #ef4444 0%, #dc2626 100%)',
  purple:  'linear-gradient(180deg, #a855f7 0%, #7e22ce 100%)',
}

const accentIcon: Record<NonNullable<InfoCardProps['accentColor']>, string> = {
  blue:    '#a5b4fc',
  amber:   '#fcd34d',
  emerald: '#6ee7b7',
  rose:    '#fda4af',
  purple:  '#d8b4fe',
}

export function InfoCard({ title, icon, value, onEdit, accentColor = 'blue' }: InfoCardProps) {
  return (
    <div className="surface-1 relative overflow-hidden" style={{ minHeight: 88 }}>
      {/* left stripe */}
      <span
        aria-hidden
        className="absolute left-0 top-0 bottom-0 w-[3px]"
        style={{ background: accentStripe[accentColor] }}
      />

      <div className="p-3.5 pl-[15px]">
        {/* header */}
        <div className="flex items-center justify-between mb-1.5">
          <div className="flex items-center gap-1.5">
            <span style={{ color: accentIcon[accentColor] }}>{icon}</span>
            <h3
              className="text-[10px] font-semibold uppercase tracking-[0.08em]"
              style={{ color: 'var(--text-2)' }}
            >
              {title}
            </h3>
          </div>
          {onEdit && (
            <button
              onClick={onEdit}
              className="text-[10.5px] font-medium px-1.5 py-0.5 rounded transition-colors"
              style={{ color: 'var(--text-2)' }}
              onMouseEnter={(e) => { e.currentTarget.style.background = 'rgba(255,255,255,0.06)'; e.currentTarget.style.color = 'var(--text-0)' }}
              onMouseLeave={(e) => { e.currentTarget.style.background = 'transparent'; e.currentTarget.style.color = 'var(--text-2)' }}
            >
              Edit
            </button>
          )}
        </div>

        {/* value */}
        <div className="max-h-40 overflow-y-auto custom-scrollbar pr-1">
          <p
            className="text-[12.5px] leading-relaxed whitespace-pre-wrap break-words"
            style={{
              color: value ? 'var(--text-0)' : 'var(--text-2)',
              fontStyle: value ? undefined : 'italic',
            }}
          >
            {value || `No ${title.toLowerCase()} set`}
          </p>
        </div>
      </div>
    </div>
  )
}

export function VersionCard({ version, onRollback }: { version: string; onRollback: () => void }) {
  return (
    <div className="surface-1 p-4">
      <div className="flex items-center justify-between">
        <div>
          <div className="text-[10px] font-medium uppercase tracking-[0.08em]" style={{ color: 'var(--text-2)' }}>
            Version
          </div>
          <div className="mt-1 text-[22px] font-semibold font-mono-jb tracking-tight" style={{ color: 'var(--text-0)' }}>
            {version}
          </div>
        </div>
        <button
          onClick={onRollback}
          className="text-[11px] font-medium px-2.5 py-1.5 rounded-md transition-colors"
          style={{
            background: 'rgba(244, 63, 94, 0.08)',
            border: '1px solid rgba(244, 63, 94, 0.22)',
            color: '#fda4af',
          }}
          onMouseEnter={(e) => { e.currentTarget.style.background = 'rgba(244, 63, 94, 0.14)' }}
          onMouseLeave={(e) => { e.currentTarget.style.background = 'rgba(244, 63, 94, 0.08)' }}
        >
          Rollback
        </button>
      </div>
    </div>
  )
}

function avatarGradient(name: string): string {
  const palette = [
    'linear-gradient(135deg, #10b981, #059669)',
    'linear-gradient(135deg, #6366f1, #4338ca)',
    'linear-gradient(135deg, #f59e0b, #d97706)',
    'linear-gradient(135deg, #ec4899, #be185d)',
    'linear-gradient(135deg, #06b6d4, #0e7490)',
    'linear-gradient(135deg, #a855f7, #7e22ce)',
  ]
  let h = 0
  for (let i = 0; i < name.length; i++) h = ((h << 5) - h) + name.charCodeAt(i) | 0
  return palette[Math.abs(h) % palette.length]
}

function initials(name: string): string {
  if (!name) return '??'
  const parts = name.split(/[-_\s]+/).filter(Boolean)
  if (parts.length >= 2) return (parts[0][0] + parts[1][0]).toUpperCase()
  return name.slice(0, 2).toUpperCase()
}

export function AgentsCard() {
  const project = useAppStore((s) => s.project)
  if (!project || project.agents.length === 0) return null

  return (
    <div className="surface-1 p-4 flex flex-col max-h-80">
      <div className="flex items-center justify-between mb-3 shrink-0">
        <div className="flex items-center gap-1.5">
          <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" style={{ color: 'var(--text-2)' }}>
            <circle cx="12" cy="12" r="9"/><path d="M12 3v18M3 12h18"/>
          </svg>
          <h3 className="text-[10px] font-semibold uppercase tracking-[0.08em]" style={{ color: 'var(--text-2)' }}>
            Agents online
          </h3>
        </div>
        <span className="chip chip-green font-mono-jb text-[10.5px]">
          <span className="status-dot" style={{ width: 5, height: 5 }} />
          {project.agents.length}
        </span>
      </div>

      <div className="space-y-1.5 overflow-y-auto custom-scrollbar pr-1">
        {project.agents.map((a) => (
          <div
            key={a.id}
            className="flex items-center gap-2 px-2 py-1.5 rounded-md transition-colors"
            style={{ background: 'rgba(255,255,255,0.02)', border: '1px solid var(--border)' }}
          >
            <div
              className="avatar"
              style={{ width: 22, height: 22, fontSize: 9.5, background: avatarGradient(a.name) }}
            >
              {initials(a.name)}
            </div>
            <div className="flex-1 min-w-0 leading-tight">
              <div className="text-[12px] font-medium truncate" style={{ color: 'var(--text-0)' }}>
                {a.name}
              </div>
              {a.current_task && (
                <div className="text-[10.5px] font-mono-jb truncate" style={{ color: 'var(--text-2)' }}>
                  {a.current_task}
                </div>
              )}
            </div>
            {a.is_platform_hosted && (
              <span
                title="Spawned by the platform agent pool"
                className="chip chip-amber font-mono-jb text-[9.5px] px-1.5 py-0"
                style={{ padding: '1px 5px' }}
              >
                hosted
              </span>
            )}
            <span className="w-1.5 h-1.5 rounded-full shrink-0" style={{ background: '#10b981', boxShadow: '0 0 5px #10b981' }} />
          </div>
        ))}
      </div>
    </div>
  )
}

export function LocksCard() {
  const project = useAppStore((s) => s.project)
  if (!project || project.locks.length === 0) return null

  return (
    <div
      className="p-4 rounded-[10px] border"
      style={{
        background: 'linear-gradient(180deg, rgba(245,158,11,0.06), rgba(245,158,11,0.02))',
        borderColor: 'rgba(245,158,11,0.22)',
      }}
    >
      <div className="flex items-center justify-between mb-3">
        <div className="flex items-center gap-1.5">
          <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="#fcd34d" strokeWidth="1.8">
            <rect x="3" y="11" width="18" height="11" rx="2"/>
            <path d="M7 11V7a5 5 0 0 1 10 0v4"/>
          </svg>
          <h3 className="text-[10px] font-semibold uppercase tracking-[0.08em]" style={{ color: '#fcd34d' }}>
            File locks
          </h3>
        </div>
        <span className="chip chip-amber font-mono-jb text-[10.5px]">{project.locks.length}</span>
      </div>

      <div className="space-y-2">
        {project.locks.map((l, i) => (
          <div
            key={l.lock_id || i}
            className="p-2.5 rounded-md border"
            style={{ background: 'rgba(0,0,0,0.25)', borderColor: 'var(--border)' }}
          >
            <div className="flex items-center gap-2 mb-1">
              <span className="text-[11.5px] font-semibold" style={{ color: '#fcd34d' }}>
                {l.agent_name}
              </span>
            </div>
            <div
              className="text-[10.5px] font-mono-jb truncate px-1.5 py-1 rounded"
              style={{ background: 'rgba(0,0,0,0.35)', color: 'var(--text-1)' }}
              title={l.files.join(', ')}
            >
              {l.files.join(', ')}
            </div>
            {l.reason && (
              <div className="text-[10.5px] italic mt-1" style={{ color: 'var(--text-2)' }}>
                {l.reason}
              </div>
            )}
          </div>
        ))}
      </div>
    </div>
  )
}
