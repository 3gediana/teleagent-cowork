import { useState, useEffect } from 'react'
import { prApi, branchApi } from '../api/endpoints'
import { PREvaluationCard } from '../components/PREvaluationCard'

const statusConfig: Record<string, { bg: string; text: string; border: string; rotate: string; icon: string }> = {
  pending_human_review: { bg: 'bg-amber-50', text: 'text-amber-700', border: 'border-amber-200', rotate: 'rotate-1', icon: '⏳' },
  evaluating: { bg: 'bg-blue-50', text: 'text-blue-700', border: 'border-blue-200', rotate: '', icon: '🔍' },
  evaluated: { bg: 'bg-indigo-50', text: 'text-indigo-700', border: 'border-indigo-200', rotate: '', icon: '📊' },
  pending_human_merge: { bg: 'bg-emerald-50', text: 'text-emerald-700', border: 'border-emerald-200', rotate: 'rotate-1', icon: '✅' },
  merged: { bg: 'bg-green-50', text: 'text-green-700', border: 'border-green-200', rotate: '', icon: '🎉' },
  rejected: { bg: 'bg-rose-50', text: 'text-rose-700', border: 'border-rose-200', rotate: '-rotate-1', icon: '❌' },
  merge_failed: { bg: 'bg-red-50', text: 'text-red-700', border: 'border-red-200', rotate: '-rotate-1', icon: '💥' },
}

const statusLabel: Record<string, string> = {
  pending_human_review: 'Awaiting Review',
  evaluating: 'Under Evaluation',
  evaluated: 'Evaluated',
  pending_human_merge: 'Awaiting Merge',
  merged: 'Merged',
  rejected: 'Rejected',
  merge_failed: 'Merge Failed',
}

export default function PRPage() {
  const [prs, setPRs] = useState<any[]>([])
  const [branches, setBranches] = useState<any[]>([])
  const [loading, setLoading] = useState(true)
  const [actionLoading, setActionLoading] = useState(false)

  useEffect(() => {
    loadData()
  }, [])

  const loadData = async () => {
    setLoading(true)
    const [prRes, brRes] = await Promise.all([prApi.list(), branchApi.list()])
    if (prRes.success) setPRs(prRes.data?.pull_requests || [])
    if (brRes.success) setBranches(brRes.data?.branches || [])
    setLoading(false)
  }

  const handleApproveReview = async (prId: string) => {
    setActionLoading(true)
    await prApi.approveReview(prId)
    await loadData()
    setActionLoading(false)
  }

  const handleApproveMerge = async (prId: string) => {
    setActionLoading(true)
    await prApi.approveMerge(prId)
    await loadData()
    setActionLoading(false)
  }

  const handleReject = async (prId: string) => {
    setActionLoading(true)
    await prApi.reject(prId)
    await loadData()
    setActionLoading(false)
  }

  if (loading) return <div className="flex items-center justify-center h-64"><p className="text-[#8b4513] font-bold font-marker animate-pulse">Scanning the branch network...</p></div>

  return (
    <div className="space-y-8">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-extrabold text-[#5d4037]">Branches & Pull Requests</h1>
          <p className="text-[#8b4513]/60 text-sm font-hand mt-1">Manage feature branches and review merge requests</p>
        </div>
        <div className="flex items-center gap-2">
          <span className="text-xs font-marker bg-[#8b4513]/10 text-[#8b4513] px-4 py-2 rounded-full border border-[#8b4513]/20">{branches.filter(b => b.status === 'active').length} Active Branches</span>
          <span className="text-xs font-marker bg-amber-100 text-amber-800 px-4 py-2 rounded-full border border-amber-200">{prs.filter(p => p.status === 'pending_human_review').length} Pending Review</span>
        </div>
      </div>

      {/* Branches Section */}
      <div>
        <h2 className="text-lg font-marker text-[#5d4037] mb-4 flex items-center gap-2">
          <span className="text-2xl">🌿</span> Active Branches
        </h2>
        <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
          {branches.filter(b => b.status === 'active').length === 0 ? (
            <div className="parchment border-2 border-dashed border-[#8b4513]/20 rounded-2xl p-10 text-center col-span-full">
              <p className="text-4xl mb-3 opacity-30">🌱</p>
              <p className="text-[#5d4037] font-marker">No active branches</p>
              <p className="text-[#8b4513]/40 font-hand text-sm mt-1">Agents can create branches via MCP tools</p>
            </div>
          ) : (
            branches.filter(b => b.status === 'active').map((b) => (
              <div key={b.id} className="parchment rounded-2xl p-5 border border-[#8b4513]/10 hover:shadow-lg transition-all">
                <div className="flex items-center justify-between mb-3">
                  <h3 className="font-marker text-sm text-[#5d4037] truncate">{b.name}</h3>
                  <span className="text-[10px] font-mono bg-emerald-50 text-emerald-700 px-2 py-1 rounded border border-emerald-200">active</span>
                </div>
                <div className="text-xs text-[#8b4513]/60 font-hand space-y-1">
                  <p>Base: <span className="font-mono text-[#5d4037]">{b.base_version}</span></p>
                  <p>Occupant: <span className="font-bold text-[#5d4037]">{b.occupied_by || 'Vacant'}</span></p>
                </div>
              </div>
            ))
          )}
        </div>
      </div>

      {/* PRs Section */}
      <div>
        <h2 className="text-lg font-marker text-[#5d4037] mb-4 flex items-center gap-2">
          <span className="text-2xl">🔀</span> Pull Requests
        </h2>
        <div className="grid grid-cols-1 gap-6">
          {prs.length === 0 ? (
            <div className="parchment border-2 border-dashed border-[#8b4513]/20 rounded-3xl p-20 text-center">
              <p className="text-6xl mb-6 opacity-30">📬</p>
              <p className="text-[#5d4037] font-marker text-xl">No pull requests yet</p>
              <p className="text-[#8b4513]/40 font-hand mt-2">Agents can submit PRs from their branches</p>
            </div>
          ) : (
            prs.map((pr) => {
              const sc = statusConfig[pr.status] || statusConfig.pending_human_review
              return (
                <div key={pr.id} className="parchment rounded-2xl p-8 hover:shadow-xl transition-all border border-[#8b4513]/10 relative group">
                  <div className="absolute top-4 right-4 rotate-3 opacity-20 group-hover:opacity-40 transition-opacity">
                    <span className="text-4xl">{sc.icon}</span>
                  </div>
                  <div className="flex items-start justify-between mb-6">
                    <div className="flex items-center gap-4">
                      <div className="bg-[#5d4037] text-[#efebe9] p-3 rounded-xl shadow-lg -rotate-2">
                        <span className="text-2xl">🔀</span>
                      </div>
                      <div>
                        <h3 className="font-marker text-lg text-[#5d4037] flex items-center gap-3">
                          {pr.title}
                          <span className="text-[10px] font-mono bg-black/5 text-[#8b4513]/50 px-2 py-1 rounded">#{pr.id.slice(0, 8)}</span>
                        </h3>
                        <p className="text-xs font-hand text-[#8b4513]/60 mt-1">
                          Branch: <span className="font-mono">{pr.branch_id?.slice(0, 8)}</span> • Filed by <span className="text-[#8b4513] font-bold">@{pr.submitter_id?.slice(0, 8)}</span> • {new Date(pr.created_at).toLocaleString()}
                        </p>
                      </div>
                    </div>
                    <span className={`text-[10px] font-marker uppercase px-3 py-1 rounded-lg border-2 ${sc.bg} ${sc.text} ${sc.border} ${sc.rotate}`}>
                      {statusLabel[pr.status] || pr.status}
                    </span>
                  </div>

                  {pr.description && (
                    <p className="text-sm text-[#5d4037]/70 font-hand mb-4 italic">"{pr.description}"</p>
                  )}

                  {/* Self Review */}
                  {pr.self_review && (
                    <div className="mb-4 p-4 bg-[#8b4513]/5 rounded-xl border border-[#8b4513]/10">
                      <p className="text-[9px] font-marker text-[#8b4513]/40 uppercase tracking-widest mb-2">Self Review</p>
                      <pre className="text-xs text-[#5d4037] font-mono whitespace-pre-wrap max-h-32 overflow-auto">{(() => { try { return JSON.stringify(JSON.parse(pr.self_review), null, 2) } catch { return pr.self_review } })()}</pre>
                    </div>
                  )}

                  {/* Evaluate + Maintain verdicts, structured so the humans
                      actually see Evaluate's recommended_action (the field
                      that drives Chief's AutoMode path). */}
                  {(pr.tech_review || pr.biz_review) && (
                    <div className="mb-4">
                      <PREvaluationCard techReview={pr.tech_review} bizReview={pr.biz_review} />
                    </div>
                  )}

                  {/* Diff Stats */}
                  {pr.diff_stat && (
                    <div className="mb-4 p-4 bg-[#8b4513]/5 rounded-xl border border-[#8b4513]/10">
                      <p className="text-[9px] font-marker text-[#8b4513]/40 uppercase tracking-widest mb-2">Diff Statistics</p>
                      <pre className="text-xs text-[#5d4037] font-mono whitespace-pre-wrap max-h-24 overflow-auto">{pr.diff_stat}</pre>
                    </div>
                  )}

                  {/* Action Buttons */}
                  <div className="flex items-center gap-3 mt-4">
                    {pr.status === 'pending_human_review' && (
                      <>
                        <button onClick={() => handleApproveReview(pr.id)} disabled={actionLoading} className="px-5 py-2 bg-emerald-600 text-white font-marker text-sm rounded-xl hover:bg-emerald-700 transition-colors shadow-md disabled:opacity-50">
                          👁️ Approve Evaluation
                        </button>
                        <button onClick={() => handleReject(pr.id)} disabled={actionLoading} className="px-5 py-2 bg-rose-500 text-white font-marker text-sm rounded-xl hover:bg-rose-600 transition-colors shadow-md disabled:opacity-50">
                          ✕ Reject
                        </button>
                      </>
                    )}
                    {pr.status === 'pending_human_merge' && (
                      <>
                        <button onClick={() => handleApproveMerge(pr.id)} disabled={actionLoading} className="px-5 py-2 bg-[#5d4037] text-white font-marker text-sm rounded-xl hover:bg-[#4e342e] transition-colors shadow-md disabled:opacity-50">
                          🔀 Confirm Merge
                        </button>
                        <button onClick={() => handleReject(pr.id)} disabled={actionLoading} className="px-5 py-2 bg-rose-500 text-white font-marker text-sm rounded-xl hover:bg-rose-600 transition-colors shadow-md disabled:opacity-50">
                          ✕ Reject
                        </button>
                      </>
                    )}
                    {(pr.status === 'evaluating' || pr.status === 'evaluated') && (
                      <span className="text-xs font-hand text-[#8b4513]/50 italic">Processing... check back soon</span>
                    )}
                    {pr.status === 'merged' && (
                      <span className="text-xs font-marker text-emerald-600">✓ Successfully merged</span>
                    )}
                    {pr.status === 'merge_failed' && (
                      <span className="text-xs font-marker text-rose-600">✗ Merge failed — requires manual resolution</span>
                    )}
                  </div>
                </div>
              )
            })
          )}
        </div>
      </div>
    </div>
  )
}
