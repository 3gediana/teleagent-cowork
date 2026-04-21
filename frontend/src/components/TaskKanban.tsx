import { useAppStore } from '../stores/appStore'

interface TaskKanbanProps {
  onClaim: (taskId: string) => void
  onComplete: (taskId: string) => void
}

export function TaskKanban({ onClaim, onComplete }: TaskKanbanProps) {
  const project = useAppStore((s) => s.project)
  if (!project) return null

  const pendingTasks = project.tasks.filter((t) => t.status === 'pending')
  const claimedTasks = project.tasks.filter((t) => t.status === 'claimed')
  const completedTasks = project.tasks.filter((t) => t.status === 'completed')

  const getPriorityPin = (priority: string) => {
    switch (priority) {
      case 'high': return 'bg-rose-500 ring-2 ring-rose-300'
      case 'medium': return 'bg-amber-500 ring-2 ring-amber-300'
      case 'low': return 'bg-emerald-500 ring-2 ring-emerald-300'
      default: return 'bg-slate-400'
    }
  }

  const getNoteColor = (id: string) => {
    const colors = [
      'bg-[#fff9c4]', // Yellow
      'bg-[#f8bbd0]', // Pink
      'bg-[#bbdefb]', // Blue
      'bg-[#c8e6c9]', // Green
    ]
    const idx = id.split('').reduce((acc, char) => acc + char.charCodeAt(0), 0) % colors.length
    return colors[idx]
  }

  const getRotation = (id: string) => {
    const idx = id.split('').reduce((acc, char) => acc + char.charCodeAt(0), 0) % 7
    return (idx - 3) // -3 to 3 degrees
  }

  const TaskCard = ({ task, showActions }: { task: any; showActions?: 'claim' | 'complete' }) => {
    const rotation = getRotation(task.id)
    const noteColor = getNoteColor(task.id)
    const isTape = task.id.charCodeAt(0) % 2 === 0

    return (
      <div 
        className={`sticky-note ${noteColor} p-6 mb-6 flex flex-col min-h-[160px]`}
        style={{ transform: `rotate(${rotation}deg)` }}
      >
        {/* Pin or Tape */}
        {isTape ? (
          <div className="absolute -top-3 left-1/2 -translate-x-1/2 w-20 h-6 bg-white/40 backdrop-blur-[1px] border-x border-black/5 z-10" />
        ) : (
          <div className={`absolute top-2 left-1/2 -translate-x-1/2 w-3 h-3 rounded-full shadow-md z-10 ${getPriorityPin(task.priority)}`} />
        )}

        <div className="flex-1">
          <div className="flex items-start justify-between gap-2 mb-2">
            <h4 className="font-hand font-bold text-lg text-slate-800 leading-tight break-words">{task.name}</h4>
          </div>
          {task.description && (
            <div className="font-hand text-sm text-slate-700/80 mb-4 line-clamp-3 overflow-hidden">
              {task.description}
            </div>
          )}
        </div>

        <div className="mt-auto border-t border-black/5 pt-3 flex items-center justify-between">
          <div className="flex flex-col">
            <span className="text-[9px] font-bold text-black/30 uppercase tracking-tighter">#{task.id.slice(0, 8)}</span>
            {task.assignee_name && (
              <span className="font-marker text-[11px] text-blue-700 mt-0.5">@{task.assignee_name}</span>
            )}
          </div>
          
          {showActions && (
            <div className="flex gap-2">
              {showActions === 'claim' && (
                <button
                  onClick={() => onClaim(task.id)}
                  className="font-marker text-[10px] bg-blue-600 hover:bg-blue-700 text-white px-3 py-1 rounded shadow-sm transition-all active:scale-95"
                >
                  Claim It
                </button>
              )}
              {showActions === 'complete' && (
                <button
                  onClick={() => onComplete(task.id)}
                  className="font-marker text-[10px] bg-emerald-600 hover:bg-emerald-700 text-white px-3 py-1 rounded shadow-sm transition-all active:scale-95"
                >
                  Done!
                </button>
              )}
            </div>
          )}
        </div>
      </div>
    )
  }

  return (
    <div className="grid grid-cols-3 gap-10 h-full p-4 overflow-y-auto custom-scrollbar">
      {/* Pending Column */}
      <div className="flex flex-col">
        <div className="font-marker text-2xl text-white/80 text-center mb-8 border-b-2 border-dashed border-white/20 pb-4 tracking-widest">
          TODO
        </div>
        <div className="space-y-2">
          {pendingTasks.map((t) => <TaskCard key={t.id} task={t} showActions="claim" />)}
        </div>
      </div>

      {/* In Progress Column */}
      <div className="flex flex-col">
        <div className="font-marker text-2xl text-blue-300 text-center mb-8 border-b-2 border-dashed border-blue-300/20 pb-4 tracking-widest">
          DOING
        </div>
        <div className="space-y-2">
          {claimedTasks.map((t) => <TaskCard key={t.id} task={t} showActions="complete" />)}
        </div>
      </div>

      {/* Completed Column */}
      <div className="flex flex-col">
        <div className="font-marker text-2xl text-emerald-300 text-center mb-8 border-b-2 border-dashed border-emerald-300/20 pb-4 tracking-widest">
          DONE
        </div>
        <div className="space-y-2">
          {completedTasks.map((t) => <TaskCard key={t.id} task={t} />)}
        </div>
      </div>
    </div>
  )
}
