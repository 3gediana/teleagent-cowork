import { useAppStore, BroadcastEvent } from '../stores/appStore'

export function BroadcastPanel() {
  const events = useAppStore((s) => s.broadcastEvents)
  const clearEvents = useAppStore((s) => s.clearBroadcastEvents)

  const formatTime = (ts: number) => {
    const date = new Date(ts)
    return date.toLocaleTimeString('en-US', { hour: '2-digit', minute: '2-digit', second: '2-digit' })
  }

  const getTypeColor = (type: string) => {
    if (type.includes('VERSION')) return 'text-purple-700 bg-purple-100 border border-purple-200'
    if (type.includes('MILESTONE')) return 'text-amber-700 bg-amber-100 border border-amber-200'
    if (type.includes('DIRECTION')) return 'text-cyan-700 bg-cyan-100 border border-cyan-200'
    if (type.includes('AUDIT')) return 'text-rose-700 bg-rose-100 border border-rose-200'
    if (type.includes('TASK')) return 'text-emerald-700 bg-emerald-100 border border-emerald-200'
    if (type.includes('FILE')) return 'text-orange-700 bg-orange-100 border border-orange-200'
    return 'text-slate-700 bg-slate-100 border border-slate-200'
  }

  return (
    <div className="h-full flex flex-col">
      <div className="flex items-center justify-between mb-4 shrink-0">
        <h3 className="text-sm font-bold text-slate-700 uppercase tracking-wider flex items-center gap-2">
          <span className="text-lg">📡</span>
          SSE Events
        </h3>
        <div className="flex items-center gap-2">
          <span className="text-xs font-bold bg-slate-100 text-slate-600 px-2.5 py-1 rounded-full">{events.length}</span>
          {events.length > 0 && (
            <button
              onClick={clearEvents}
              className="text-xs font-medium text-slate-400 hover:text-slate-600 transition-colors"
            >
              Clear
            </button>
          )}
        </div>
      </div>
      <div className="flex-1 overflow-y-auto space-y-2 custom-scrollbar pr-1">
        {events.length === 0 ? (
          <div className="text-center py-8 text-slate-400">
            <p className="text-3xl mb-2">🔌</p>
            <p className="text-sm font-medium">Waiting for events...</p>
          </div>
        ) : (
          events.map((e: BroadcastEvent) => (
            <div key={e.id} className="p-3 rounded-lg bg-slate-50 border border-slate-200 shadow-sm">
              <div className="flex items-center justify-between mb-2">
                <span className={`text-xs px-2 py-0.5 rounded font-mono font-bold ${getTypeColor(e.type)}`}>
                  {e.type}
                </span>
                <span className="text-xs font-medium text-slate-400 font-mono">{formatTime(e.timestamp)}</span>
              </div>
              {Object.keys(e.payload).length > 0 && (
                <pre className="text-xs text-slate-600 mt-1 overflow-x-auto p-2 bg-white border border-slate-100 rounded custom-scrollbar">
                  {JSON.stringify(e.payload, null, 2)}
                </pre>
              )}
            </div>
          ))
        )}
      </div>
    </div>
  )
}
