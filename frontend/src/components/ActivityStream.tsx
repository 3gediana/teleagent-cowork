import { useAppStore, ActivityItem } from '../stores/appStore'

export function ActivityStream() {
  const activities = useAppStore((s) => s.activities)

  const formatTime = (ts: number) => {
    const diff = Date.now() - ts
    if (diff < 60000) return 'Just now'
    if (diff < 3600000) return `${Math.floor(diff / 60000)}m ago`
    if (diff < 86400000) return `${Math.floor(diff / 3600000)}h ago`
    return new Date(ts).toLocaleDateString()
  }

  const getIcon = (action: string) => {
    if (action.includes('claimed')) return '👆'
    if (action.includes('completed')) return '✅'
    if (action.includes('locked')) return '🔒'
    if (action.includes('unlocked')) return '🔓'
    if (action.includes('joined')) return '👋'
    if (action.includes('left')) return '👋'
    if (action.includes('version')) return '📦'
    if (action.includes('audit')) return '🔍'
    if (action.includes('direction')) return '🎯'
    if (action.includes('milestone')) return '🏁'
    return '⚡'
  }

  return (
    <div className="h-full flex flex-col">
      <div className="flex items-center justify-between mb-4 shrink-0">
        <h3 className="text-sm font-bold text-slate-700 uppercase tracking-wider flex items-center gap-2">
          <span className="text-lg">📊</span>
          Activity Stream
        </h3>
        <div className="w-2.5 h-2.5 rounded-full bg-emerald-500 animate-pulse shadow-[0_0_8px_rgba(16,185,129,0.5)]" />
      </div>
      <div className="flex-1 overflow-y-auto space-y-2 custom-scrollbar pr-1">
        {activities.length === 0 ? (
          <div className="text-center py-8 text-slate-400">
            <p className="text-3xl mb-2">📭</p>
            <p className="text-sm font-medium">No recent activity</p>
          </div>
        ) : (
          activities.map((a: ActivityItem) => (
            <div key={a.id} className="flex items-start gap-3 p-3 rounded-lg bg-slate-50 border border-slate-100 hover:border-blue-200 hover:shadow-sm transition-all">
              <span className="text-lg">{getIcon(a.action)}</span>
              <div className="flex-1 min-w-0">
                <p className="text-sm text-slate-700">
                  <span className="font-bold text-blue-700">{a.agentName}</span>
                  <span className="text-slate-600"> {a.action}</span>
                </p>
                {a.target && (
                  <p className="text-xs text-slate-500 truncate mt-0.5">{a.target}</p>
                )}
              </div>
              <span className="text-xs font-medium text-slate-400 whitespace-nowrap">{formatTime(a.timestamp)}</span>
            </div>
          ))
        )}
      </div>
    </div>
  )
}
