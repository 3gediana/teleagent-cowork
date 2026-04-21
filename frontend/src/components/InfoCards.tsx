import { useAppStore } from '../stores/appStore'

interface InfoCardProps {
  title: string
  icon: string
  value: string | null
  onEdit?: () => void
  children?: React.ReactNode
  accentColor?: string
}

export function InfoCard({ title, icon, value, onEdit, accentColor = 'blue' }: InfoCardProps) {
  const accentClasses: Record<string, string> = {
    blue: 'bg-[#bbdefb] rotate-1',
    amber: 'bg-[#fff9c4] -rotate-1',
    purple: 'bg-[#f8bbd0] rotate-1',
    emerald: 'bg-[#c8e6c9] -rotate-1',
    rose: 'bg-[#ffcdd2] rotate-1',
  }

  return (
    <div className={`sticky-note ${accentClasses[accentColor]} p-5 flex flex-col min-h-[200px] max-h-96 shadow-lg`}>
      {/* Tape Effect */}
      <div className="absolute -top-2 left-1/2 -translate-x-1/2 w-16 h-5 bg-white/30 backdrop-blur-[1px] border-x border-black/5 z-10" />
      
      <div className="flex items-center justify-between mb-3 shrink-0">
        <div className="flex items-center gap-2">
          <span className="text-xl">{icon}</span>
          <h3 className="text-xs font-marker text-slate-800/60 uppercase tracking-widest">{title}</h3>
        </div>
        <div className="flex items-center gap-2">
          {onEdit && (
            <button
              onClick={onEdit}
              className="text-[10px] font-marker text-blue-700 hover:text-blue-900 px-2 py-0.5 rounded border border-blue-700/20"
            >
              Edit
            </button>
          )}
        </div>
      </div>
      <div className="overflow-y-auto pr-2 custom-scrollbar flex-1">
        <p className={`font-hand text-base leading-snug whitespace-pre-wrap break-words ${value ? 'text-slate-800' : 'text-slate-500/60 italic'}`}>
          {value || `No ${title.toLowerCase()} set...`}
        </p>
      </div>
    </div>
  )
}

export function VersionCard({ version, onRollback }: { version: string; onRollback: () => void }) {
  return (
    <div className="rounded-xl bg-white shadow-sm border border-slate-200 p-5 flex flex-col max-h-96 justify-center">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-3">
          <span className="text-2xl">🏷️</span>
          <div>
            <h3 className="text-xs font-bold text-slate-500 uppercase tracking-wider mb-1">Version</h3>
            <p className="text-2xl font-bold text-slate-800 font-mono">{version}</p>
          </div>
        </div>
        <button
          onClick={onRollback}
          className="text-xs font-medium text-rose-600 hover:text-rose-700 transition-colors px-3 py-2 rounded-lg border border-rose-200 hover:border-rose-300 hover:bg-rose-50"
        >
          Rollback
        </button>
      </div>
    </div>
  )
}

export function AgentsCard() {
  const project = useAppStore((s) => s.project)
  if (!project || project.agents.length === 0) return null

  return (
    <div className="rounded-xl bg-white shadow-sm border border-slate-200 p-5 flex flex-col max-h-96">
      <div className="flex items-center gap-2 mb-4 shrink-0">
        <span className="text-xl">🤖</span>
        <h3 className="text-sm font-bold text-slate-700 uppercase tracking-wider">Online Agents</h3>
        <span className="ml-auto text-xs font-medium bg-emerald-100 text-emerald-700 px-2.5 py-1 rounded-full">
          {project.agents.length} active
        </span>
      </div>
      <div className="flex flex-wrap gap-2 overflow-y-auto custom-scrollbar content-start">
        {project.agents.map((a) => (
          <div key={a.id} className="flex items-center gap-2 bg-slate-50 border border-slate-100 rounded-lg px-3 py-2 shadow-sm">
            <span className="w-2.5 h-2.5 rounded-full bg-emerald-500 animate-pulse shadow-[0_0_8px_rgba(16,185,129,0.5)]" />
            <span className="text-sm font-medium text-slate-700">{a.name}</span>
            {a.current_task && (
              <span className="text-xs text-slate-500 truncate max-w-32">({a.current_task})</span>
            )}
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
    <div className="rounded-xl bg-amber-50 shadow-sm border border-amber-200 p-4 mb-6">
      <div className="flex items-center gap-2 mb-3">
        <span className="text-xl">🔒</span>
        <h3 className="text-sm font-bold text-amber-900 uppercase tracking-wider">File Locks</h3>
        <span className="ml-auto text-xs font-medium bg-amber-200 text-amber-800 px-2.5 py-1 rounded-full">
          {project.locks.length} locked
        </span>
      </div>
      <div className="space-y-2">
        {project.locks.map((l, i) => (
          <div key={l.lock_id || i} className="bg-white rounded-lg p-3 border border-amber-100 shadow-sm flex flex-col gap-1">
            <div className="flex items-center gap-2">
              <span className="text-sm font-bold text-amber-700">{l.agent_name}</span>
            </div>
            <div className="text-xs text-slate-600 font-mono truncate bg-slate-50 p-1.5 rounded border border-slate-100">
              {l.files.join(', ')}
            </div>
            <div className="text-xs text-slate-500 italic mt-0.5">{l.reason}</div>
          </div>
        ))}
      </div>
    </div>
  )
}
