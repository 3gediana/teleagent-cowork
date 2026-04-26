import { useAppStore } from '../stores/appStore'

/**
 * TaskKanban — three-column board of cream paper cards pinned onto a
 * dark felt surface.  Priority is communicated via the 3px colored
 * strip on the left of each card (Linear/Height pattern), not by card
 * background tint, so high card counts don't turn the board into a
 * rainbow.
 *
 * Columns auto-scroll vertically so the board gracefully handles large
 * task volumes.  Each card rotation is deterministic (< 1°) per ID —
 * only enough to hint "physical" without looking drunk.
 */

interface TaskKanbanProps {
  onClaim: (taskId: string) => void
  onComplete: (taskId: string) => void
}

type TaskLike = {
  id: string
  name: string
  description: string
  status: string
  assignee_id?: string | null
  assignee_name?: string | null
  priority: string
}

function priorityStripe(p: string): string {
  switch (p) {
    case 'high':   return 'stripe-high'
    case 'medium': return 'stripe-med'
    case 'low':    return 'stripe-low'
    default:       return 'stripe-med'
  }
}

function priorityLabel(p: string): { cls: string; text: string } {
  switch (p) {
    case 'high':   return { cls: 'prio-high', text: 'High' }
    case 'medium': return { cls: 'prio-med',  text: 'Medium' }
    case 'low':    return { cls: 'prio-low',  text: 'Low' }
    default:       return { cls: '',           text: p || 'Task' }
  }
}

/** deterministic hash -> [-0.4, 0.4] degrees rotation */
function microRotation(id: string): number {
  let h = 0
  for (let i = 0; i < id.length; i++) h = ((h << 5) - h) + id.charCodeAt(i) | 0
  return (((Math.abs(h) % 9) - 4) / 10) // -0.4 .. 0.4
}

/** deterministic pin color choice (brass vs silver) per task */
function pinClass(id: string): string {
  let h = 0
  for (let i = 0; i < id.length; i++) h = ((h << 5) - h) + id.charCodeAt(i) | 0
  return (Math.abs(h) % 2 === 0) ? 'pin' : 'pin pin-silver'
}

/** compact assignee avatar with deterministic gradient */
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

function TaskCard({
  task,
  showAction,
  onClaim,
  onComplete,
}: {
  task: TaskLike
  showAction?: 'claim' | 'complete' | null
  onClaim: (id: string) => void
  onComplete: (id: string) => void
}) {
  const stripe = task.status === 'claimed' ? 'stripe-active'
    : task.status === 'completed' ? 'stripe-done'
    : priorityStripe(task.priority)
  const lbl = priorityLabel(task.priority)
  const rot = microRotation(task.id)
  const pin = pinClass(task.id)

  return (
    <div
      className={`card-paper ${stripe}`}
      style={{ transform: `rotate(${rot}deg)` }}
    >
      <span className={pin} aria-hidden />

      {/* header row */}
      <div className="relative z-[2] flex items-start justify-between gap-2 mb-2">
        {lbl.text ? <span className={`prio-label ${lbl.cls}`}>{lbl.text}</span> : <span />}
        <span className="font-mono-jb text-[10.5px]" style={{ color: 'var(--paper-ink-mute)' }}>
          {task.id.slice(0, 8)}
        </span>
      </div>

      {/* title */}
      <h3 className="relative z-[2] text-[14px] font-semibold leading-snug tracking-tight" style={{ color: 'var(--paper-ink)' }}>
        {task.name}
      </h3>

      {/* assignee block if any */}
      {task.assignee_name && (
        <div className="relative z-[2] mt-3 flex items-center gap-2 rounded-md px-2 py-1.5 border"
             style={{ background: 'rgba(0,0,0,0.04)', borderColor: 'rgba(0,0,0,0.06)' }}>
          <div className="avatar" style={{ width: 22, height: 22, fontSize: 9.5, background: avatarGradient(task.assignee_name) }}>
            {initials(task.assignee_name)}
          </div>
          <div className="flex-1 min-w-0 leading-tight">
            <div className="text-[11.5px] font-semibold truncate" style={{ color: 'var(--paper-ink)' }}>
              {task.assignee_name}
            </div>
            <div className="text-[10.5px] font-mono-jb" style={{ color: 'var(--paper-ink-soft)' }}>
              {task.status}
            </div>
          </div>
          {task.status === 'claimed' && (
            <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="var(--paper-ink-soft)" strokeWidth="2.5" strokeLinecap="round" className="animate-spin" style={{ animationDuration: '1.5s' }}>
              <path d="M21 12a9 9 0 1 1-6.219-8.56" />
            </svg>
          )}
        </div>
      )}

      {/* description */}
      {task.description && (
        <p className="relative z-[2] text-[12px] mt-2 leading-relaxed line-clamp-3" style={{ color: 'var(--paper-ink-soft)' }}>
          {task.description}
        </p>
      )}

      {/* footer */}
      <div
        className="relative z-[2] mt-3 pt-2.5 flex items-center justify-between border-t"
        style={{ borderColor: 'rgba(0,0,0,0.08)' }}
      >
        <div className="flex items-center gap-2 text-[11px] font-mono-jb" style={{ color: 'var(--paper-ink-mute)' }}>
          {!task.assignee_name && <span>unassigned</span>}
        </div>
        {showAction === 'claim' && (
          <button
            onClick={() => onClaim(task.id)}
            className="text-[11px] font-medium px-2.5 py-1 rounded transition-colors"
            style={{ background: 'var(--paper-ink)', color: 'white' }}
            onMouseEnter={(e) => (e.currentTarget.style.background = '#000')}
            onMouseLeave={(e) => (e.currentTarget.style.background = 'var(--paper-ink)')}
          >
            Claim
          </button>
        )}
        {showAction === 'complete' && (
          <button
            onClick={() => onComplete(task.id)}
            className="text-[11px] font-medium px-2.5 py-1 rounded transition-colors"
            style={{ background: '#047857', color: 'white' }}
            onMouseEnter={(e) => (e.currentTarget.style.background = '#065f46')}
            onMouseLeave={(e) => (e.currentTarget.style.background = '#047857')}
          >
            Mark done
          </button>
        )}
      </div>
    </div>
  )
}

function ColumnHeader({
  title,
  count,
  dot,
  badgeClass,
}: {
  title: string
  count: number
  dot: string
  badgeClass: string
}) {
  return (
    <div className="col-header">
      <div className="flex items-center gap-2">
        <span className="w-1.5 h-1.5 rounded-full" style={{ background: dot, boxShadow: `0 0 6px ${dot}` }} />
        <span className="text-[12px] font-semibold text-white">{title}</span>
        <span className={`text-[11px] font-mono-jb px-1.5 rounded ${badgeClass}`}>{count}</span>
      </div>
      <button className="text-[#71717a] hover:text-white p-0.5 rounded hover:bg-white/5" title="Add task">
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round">
          <path d="M12 5v14M5 12h14" />
        </svg>
      </button>
    </div>
  )
}

export function TaskKanban({ onClaim, onComplete }: TaskKanbanProps) {
  const project = useAppStore((s) => s.project)
  if (!project) return null

  const pending   = project.tasks.filter((t) => t.status === 'pending')
  const claimed   = project.tasks.filter((t) => t.status === 'claimed')
  const completed = project.tasks.filter((t) => t.status === 'completed')

  return (
    <div className="h-full flex flex-col">
      {/* column headers row */}
      <div className="grid grid-cols-3 gap-5 px-5 pt-5 pb-3 relative z-[1]">
        <ColumnHeader title="Backlog"    count={pending.length}   dot="#71717a" badgeClass="text-[#a1a1aa] bg-white/5" />
        <ColumnHeader title="In Progress" count={claimed.length}  dot="#6366f1" badgeClass="text-indigo-300 bg-indigo-500/15" />
        <ColumnHeader title="Done"        count={completed.length} dot="#10b981" badgeClass="text-emerald-300 bg-emerald-500/15" />
      </div>

      {/* card columns, scrollable */}
      <div className="flex-1 grid grid-cols-3 gap-5 px-5 pb-5 min-h-0 relative z-[1]">
        <Column>
          {pending.map((t) => (
            <TaskCard key={t.id} task={t} showAction="claim" onClaim={onClaim} onComplete={onComplete} />
          ))}
          {pending.length === 0 && <EmptyHint text="No open tasks" />}
        </Column>

        <Column>
          {claimed.map((t) => (
            <TaskCard key={t.id} task={t} showAction="complete" onClaim={onClaim} onComplete={onComplete} />
          ))}
          {claimed.length === 0 && <EmptyHint text="No active tasks" />}
        </Column>

        <Column>
          {completed.map((t) => (
            <TaskCard key={t.id} task={t} showAction={null} onClaim={onClaim} onComplete={onComplete} />
          ))}
          {completed.length === 0 && <EmptyHint text="Nothing shipped yet" />}
        </Column>
      </div>
    </div>
  )
}

function Column({ children }: { children: React.ReactNode }) {
  return (
    <div className="overflow-y-auto custom-scrollbar space-y-4 px-1 pt-1 pb-4 pr-2">
      {children}
    </div>
  )
}

function EmptyHint({ text }: { text: string }) {
  return (
    <div className="border-2 border-dashed border-white/8 rounded-md h-16 flex items-center justify-center">
      <span className="text-white/20 text-[12px]">{text}</span>
    </div>
  )
}
