import { useDashboard } from '../hooks/useDashboard'
import { TaskKanban } from '../components/TaskKanban'
import { taskApi } from '../api/endpoints'

export default function TaskPage() {
  const { refreshState } = useDashboard()

  const handleClaim = async (taskId: string) => {
    await taskApi.claim(taskId)
    refreshState()
  }

  const handleComplete = async (taskId: string) => {
    await taskApi.complete(taskId)
    refreshState()
  }

  return (
    <div className="h-full flex flex-col space-y-6">
      <div className="flex items-center justify-between shrink-0">
        <div>
          <h1 className="text-2xl font-extrabold text-slate-800">Task Board</h1>
          <p className="text-slate-500 text-sm font-medium mt-1">AI Agent collaboration workspace</p>
        </div>
      </div>

      <div className="flex-1 min-h-0 wood-board rounded-[40px] border-[12px] border-[#2d1b0f] shadow-2xl p-10 overflow-hidden">
        <TaskKanban onClaim={handleClaim} onComplete={handleComplete} />
      </div>
    </div>
  )
}
