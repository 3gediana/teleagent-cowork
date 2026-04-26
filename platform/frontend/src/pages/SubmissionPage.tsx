import { useState, useEffect } from 'react'
import { useAppStore } from '../stores/appStore'
import { changeApi } from '../api/endpoints'

export default function SubmissionPage() {
  const { selectedProjectId } = useAppStore()
  const [changes, setChanges] = useState<any[]>([])
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    if (selectedProjectId) {
      changeApi.list(selectedProjectId).then((res) => {
        if (res.success) setChanges(res.data || [])
        setLoading(false)
      })
    }
  }, [selectedProjectId])

  if (loading) return <div className="flex items-center justify-center h-64"><p className="text-[#8b4513] font-bold font-marker animate-pulse">Consulting the archives...</p></div>

  return (
    <div className="space-y-8">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-extrabold text-[#5d4037]">Submissions History</h1>
          <p className="text-[#8b4513]/60 text-sm font-hand mt-1">Audit logs of all agent contributions</p>
        </div>
        <div className="flex items-center gap-2">
           <span className="text-xs font-marker bg-[#8b4513]/10 text-[#8b4513] px-4 py-2 rounded-full border border-[#8b4513]/20">{changes.length} Total Logs</span>
        </div>
      </div>

      <div className="grid grid-cols-1 gap-6">
        {changes.length === 0 ? (
          <div className="parchment border-2 border-dashed border-[#8b4513]/20 rounded-3xl p-20 text-center">
            <p className="text-6xl mb-6 opacity-30">📜</p>
            <p className="text-[#5d4037] font-marker text-xl">The archives are empty.</p>
            <p className="text-[#8b4513]/40 font-hand mt-2">No agent submissions recorded yet.</p>
          </div>
        ) : (
          changes.map((c) => (
            <div key={c.id} className="parchment rounded-2xl p-8 hover:shadow-xl transition-all border border-[#8b4513]/10 relative group">
              <div className="absolute top-4 right-4 rotate-3 opacity-20 group-hover:opacity-40 transition-opacity">
                 <span className="text-4xl">✒️</span>
              </div>
              <div className="flex items-start justify-between mb-6">
                <div className="flex items-center gap-4">
                  <div className="bg-[#5d4037] text-[#efebe9] p-3 rounded-xl shadow-lg -rotate-2">
                    <span className="text-2xl">📦</span>
                  </div>
                  <div>
                    <h3 className="font-marker text-lg text-[#5d4037] flex items-center gap-3">
                      {c.description || 'Manifest Change'}
                      <span className="text-[10px] font-mono bg-black/5 text-[#8b4513]/50 px-2 py-1 rounded">#{c.id.slice(0, 8)}</span>
                    </h3>
                    <p className="text-xs font-hand text-[#8b4513]/60 mt-1">
                      Filed by <span className="text-[#8b4513] font-bold">@{c.agent_id}</span> • {new Date(c.created_at).toLocaleString()}
                    </p>
                  </div>
                </div>
                <div className="flex flex-col items-end gap-2">
                  <span className={`text-[10px] font-marker uppercase px-3 py-1 rounded-lg border-2 ${
                    c.status === 'approved' ? 'bg-emerald-50 text-emerald-700 border-emerald-200 rotate-1' :
                    c.status === 'rejected' ? 'bg-rose-50 text-rose-700 border-rose-200 -rotate-1' :
                    'bg-amber-50 text-amber-700 border-amber-200 rotate-1'
                  }`}>
                    {c.status}
                  </span>
                </div>
              </div>

              <div className="grid grid-cols-3 gap-6 mb-4">
                <div className="bg-[#8b4513]/5 rounded-xl p-4 border border-[#8b4513]/10">
                   <p className="text-[9px] font-marker text-[#8b4513]/40 uppercase tracking-widest mb-2">Files Changed</p>
                   <p className="text-xl font-type text-[#5d4037]">{JSON.parse(c.modified_files || '[]').length + JSON.parse(c.new_files || '[]').length}</p>
                </div>
                <div className="bg-[#8b4513]/5 rounded-xl p-4 border border-[#8b4513]/10">
                   <p className="text-[9px] font-marker text-[#8b4513]/40 uppercase tracking-widest mb-2">Version</p>
                   <p className="text-xl font-type text-blue-800">{c.version}</p>
                </div>
                <div className="bg-[#8b4513]/5 rounded-xl p-4 border border-[#8b4513]/10">
                   <p className="text-[9px] font-marker text-[#8b4513]/40 uppercase tracking-widest mb-2">Reference ID</p>
                   <p className="text-xl font-type text-[#5d4037]">{c.task_id?.slice(0, 8) || 'GLOBAL'}</p>
                </div>
              </div>

              {c.audit_reason && (
                 <div className="mt-6 p-5 bg-[#5d4037]/5 border-l-4 border-[#5d4037] rounded-r-xl text-sm text-[#5d4037] font-hand italic shadow-inner">
                    <span className="font-marker not-italic block mb-2 uppercase text-[10px] opacity-40">Audit Review:</span>
                    "{c.audit_reason}"
                 </div>
              )}
            </div>
          ))
        )}
      </div>
    </div>
  )
}
