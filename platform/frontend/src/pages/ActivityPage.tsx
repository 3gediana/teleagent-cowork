import { ActivityStream } from '../components/ActivityStream'
import { BroadcastPanel } from '../components/BroadcastPanel'

export default function ActivityPage() {
  return (
    <div className="h-full flex flex-col space-y-6">
      <h1 className="text-2xl font-extrabold text-slate-800">Activity & Events</h1>

      <div className="flex-1 grid grid-cols-2 gap-8 min-h-0">
        <div className="bg-white rounded-3xl border border-slate-200 shadow-sm p-6 flex flex-col overflow-hidden">
          <ActivityStream />
        </div>
        <div className="bg-white rounded-3xl border border-slate-200 shadow-sm p-6 flex flex-col overflow-hidden">
          <BroadcastPanel />
        </div>
      </div>
    </div>
  )
}
