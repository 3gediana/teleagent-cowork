import { useState, useEffect } from 'react'
import { useAppStore } from '../stores/appStore'
import { projectApi } from '../api/endpoints'

export default function LoginPanel({ onLogin }: { onLogin: () => void }) {
  const [projects, setProjects] = useState<any[]>([])
  const [selected, setSelected] = useState('')

  useEffect(() => {
    projectApi.list().then((res) => {
      if (res.success) setProjects(res.data || [])
    })
  }, [])

  const handleEnter = () => {
    if (selected) {
      useAppStore.getState().setSelectedProjectId(selected)
      onLogin()
    }
  }

  return (
    <div className="min-h-screen bg-gradient-to-br from-slate-50 via-white to-blue-50 flex items-center justify-center p-8">
      <div className="bg-white rounded-3xl shadow-xl border border-slate-200 p-10 w-[32rem]">
        <div className="text-center mb-8">
          <h1 className="text-4xl font-extrabold bg-gradient-to-r from-blue-600 to-blue-800 bg-clip-text text-transparent mb-2">A3C Dashboard</h1>
          <p className="text-slate-500 font-medium">Autonomous AI Coding Coordination</p>
        </div>
        {projects.length > 0 ? (
          <div className="space-y-2 mb-6">
            {projects.map((p) => (
              <button
                key={p.id}
                onClick={() => setSelected(p.id)}
                className={`w-full text-left rounded-xl px-4 py-3 transition-all border ${
                  selected === p.id
                    ? 'bg-blue-50 border-blue-400 text-blue-800 shadow-sm'
                    : 'bg-white border-slate-200 text-slate-700 hover:bg-slate-50 hover:border-slate-300'
                }`}
              >
                <span className="font-bold">{p.name}</span>
                {p.description && <p className="text-xs text-slate-500 mt-1">{p.description}</p>}
              </button>
            ))}
          </div>
        ) : (
          <p className="text-center text-slate-500 mb-6 font-medium italic">No projects found.</p>
        )}
        <button
          onClick={handleEnter}
          disabled={!selected}
          className="w-full bg-blue-600 hover:bg-blue-700 text-white py-4 rounded-xl font-bold shadow-md transition-all disabled:opacity-50 disabled:cursor-not-allowed active:scale-95"
        >
          Enter Workspace
        </button>
      </div>
    </div>
  )
}
