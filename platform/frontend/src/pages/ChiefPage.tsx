import { useState, useEffect, useRef } from 'react'
import { useAppStore } from '../stores/appStore'
import { chiefApi, experienceApi, skillApi, policyApi, prApi } from '../api/endpoints'
import { ChiefQueuePanel } from '../components/ChiefQueuePanel'

interface ChiefMessage {
  id: string
  role: 'human' | 'chief' | 'system'
  content: string
  timestamp: number
}

interface Policy {
  id: string
  name: string
  match_condition: string
  actions: string
  priority: number
  status: string
  source: string
  created_at: string
}

interface Session {
  id: string
  role: string
  project_id: string
  status: string
  trigger_reason: string
  duration_ms: number
  retry_count: number
  created_at: string
  completed_at: string | null
}

interface Experience {
  id: string
  project_id: string
  source_type: string
  source_id: string
  agent_role: string
  task_id: string
  outcome: string
  approach: string
  pitfalls: string
  key_insight: string
  missing_context: string
  do_differently: string
  pattern_observed: string
  fix_strategy: string
  false_positive: boolean
  status: string
  created_at: string
}

type TabType = 'chat' | 'queue' | 'policies' | 'sessions' | 'experience' | 'skills'

function getTabLabel(t: string): string {
  const labels: Record<string, string> = { chat: 'Chat', queue: 'Queue', policies: 'Policies', sessions: 'Sessions', experience: 'Exp', skills: 'Skills' }
  return labels[t] || t
}

export default function ChiefPage() {
  const { selectedProjectId } = useAppStore()
  const [tab, setTab] = useState<TabType>('chat')
  const [messages, setMessages] = useState<ChiefMessage[]>([])
  const [inputText, setInputText] = useState('')
  const [policies, setPolicies] = useState<Policy[]>([])
  const [sessions, setSessions] = useState<Session[]>([])
  const [expandedSession, setExpandedSession] = useState<string | null>(null)
  const [traces, setTraces] = useState<any[]>([])
  const [experiences, setExperiences] = useState<Experience[]>([])
  const [expStatusFilter, setExpStatusFilter] = useState('raw')
  const [skills, setSkills] = useState<any[]>([])
  const [skillStatusFilter, setSkillStatusFilter] = useState('candidate')
  const [loading, setLoading] = useState(false)
  // pendingCount powers the little number on the Queue tab header so
  // operators spot incoming decisions without having to click into
  // the tab first. Refreshed whenever the Chief page mounts.
  const [pendingCount, setPendingCount] = useState(0)
  const chatEndRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (tab === 'policies') loadPolicies()
    if (tab === 'sessions' && selectedProjectId) loadSessions()
    if (tab === 'experience' && selectedProjectId) loadExperiences()
    if (tab === 'skills') loadSkills()
  }, [tab, selectedProjectId])

  // Pending-count refresher — fires on mount and every 15s while the
  // Chief page is open. Cheap: single /pr/list call, the server is
  // already caching it for the PR page.
  useEffect(() => {
    const refresh = async () => {
      const res = await prApi.list()
      if (res.success) {
        const prs = res.data?.pull_requests || []
        setPendingCount(prs.filter((p: any) => p.status === 'pending_human_review' || p.status === 'pending_human_merge').length)
      }
    }
    refresh()
    const i = setInterval(refresh, 15000)
    return () => clearInterval(i)
  }, [selectedProjectId])

  useEffect(() => {
    chatEndRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [messages])

  const loadPolicies = async () => {
    const res = await chiefApi.policies()
    if (res.success) setPolicies(res.data.policies || [])
  }

  const loadSessions = async () => {
    if (!selectedProjectId) return
    const res = await chiefApi.sessions(selectedProjectId)
    if (res.success) setSessions(res.data.sessions || [])
  }

  const loadTraces = async (sessionId: string) => {
    const res = await chiefApi.traces(sessionId)
    if (res.success) setTraces(res.data.traces || [])
  }

  const loadExperiences = async () => {
    if (!selectedProjectId) return
    const res = await experienceApi.list(selectedProjectId, expStatusFilter)
    if (res.success) setExperiences(res.data.experiences || [])
  }

  const loadSkills = async () => {
    const res = await skillApi.list(skillStatusFilter)
    if (res.success) setSkills(res.data.skills || [])
  }

  const handleApproveSkill = async (id: string) => {
    const res = await skillApi.approve(id)
    if (res.success) loadSkills()
  }

  const handleRejectSkill = async (id: string) => {
    const res = await skillApi.reject(id)
    if (res.success) loadSkills()
  }

  const handleActivatePolicy = async (id: string) => {
    const res = await policyApi.activate(id)
    if (res.success) loadPolicies()
  }

  const handleDeactivatePolicy = async (id: string) => {
    const res = await policyApi.deactivate(id)
    if (res.success) loadPolicies()
  }

  const handleSend = async () => {
    if (!inputText.trim() || !selectedProjectId) return
    const msg = inputText.trim()
    setInputText('')

    const humanMsg: ChiefMessage = { id: Date.now().toString(), role: 'human', content: msg, timestamp: Date.now() }
    setMessages((prev) => [...prev, humanMsg])
    setLoading(true)

    try {
      const res = await chiefApi.chat(selectedProjectId, msg)
      if (res.success) {
        const data = res.data
        const chiefContent = data.agent_response || data.message || 'Chief Agent is processing...'
        const chiefMsg: ChiefMessage = {
          id: (Date.now() + 1).toString(),
          role: 'chief',
          content: chiefContent,
          timestamp: Date.now(),
        }
        setMessages((prev) => [...prev, chiefMsg])
      }
    } catch {
      const errMsg: ChiefMessage = {
        id: (Date.now() + 1).toString(),
        role: 'system',
        content: 'Failed to reach Chief Agent.',
        timestamp: Date.now(),
      }
      setMessages((prev) => [...prev, errMsg])
    }
    setLoading(false)
  }

  const toggleSessionExpand = (sessionId: string) => {
    if (expandedSession === sessionId) {
      setExpandedSession(null)
      setTraces([])
    } else {
      setExpandedSession(sessionId)
      loadTraces(sessionId)
    }
  }

  const statusColor = (status: string) => {
    switch (status) {
      case 'completed': return 'bg-emerald-100 text-emerald-700 border-emerald-200'
      case 'running': return 'bg-blue-100 text-blue-700 border-blue-200'
      case 'failed': return 'bg-rose-100 text-rose-700 border-rose-200'
      case 'pending': return 'bg-amber-100 text-amber-700 border-amber-200'
      default: return 'bg-gray-100 text-gray-600 border-gray-200'
    }
  }

  const sourceIcon = (source: string) => {
    switch (source) {
      case 'human': return '👤'
      case 'chief': return '🤖'
      case 'analyze': return '📊'
      default: return '📝'
    }
  }

  return (
    <div className="h-full flex flex-col">
      {/* Tab Header */}
      <div className="flex items-center gap-2 mb-6 px-1 shrink-0">
        {(['chat', 'queue', 'policies', 'sessions', 'experience', 'skills'] as TabType[]).map((t) => {
          const isActive = tab === t
          const showCount = t === 'queue' && pendingCount > 0
          return (
            <button
              key={t}
              onClick={() => setTab(t)}
              className={'px-5 py-2 rounded-xl text-sm font-marker transition-all inline-flex items-center gap-2 ' + (isActive ? 'bg-[#5d4037] text-[#efebe9] shadow-md scale-105' : 'bg-[#f4ece1] text-[#5d4037]/70 border border-[#8b4513]/20 hover:bg-[#8b4513]/10')}
            >
              {getTabLabel(t)}
              {showCount && (
                <span className={`inline-flex items-center justify-center min-w-[18px] h-[18px] px-1 rounded-full text-[10px] font-mono ${
                  isActive
                    ? 'bg-emerald-400/80 text-[#3e2723]'
                    : 'bg-amber-500 text-white shadow-[0_0_8px_rgba(245,158,11,0.6)] animate-pulse'
                }`}>
                  {pendingCount}
                </span>
              )}
            </button>
          )
        })}
        <span className="ml-auto text-[10px] font-bold text-[#8b4513]/40 uppercase tracking-widest">
          Chief Agent
        </span>
      </div>

      {/* Queue Tab — the new "Chief's desk" of decisions waiting */}
      {tab === 'queue' && (
        <div className="flex-1 overflow-y-auto custom-scrollbar pr-2">
          <ChiefQueuePanel />
        </div>
      )}

      {/* Chat Tab */}
      {tab === 'chat' && (
        <div className="flex-1 flex flex-col min-h-0">
          <div className="flex-1 overflow-y-auto mb-4 space-y-4 custom-scrollbar pr-2">
            {messages.map((m) => (
              <div key={m.id} className={`${m.role === 'human' ? 'text-right' : m.role === 'system' ? 'text-center' : 'text-left'}`}>
                <span
                  className={`inline-block max-w-[90%] px-5 py-3 text-sm shadow-md transition-all hover:scale-[1.02] ${
                    m.role === 'human'
                      ? 'bg-[#5d4037] text-[#efebe9] rounded-2xl rounded-br-sm font-type'
                      : m.role === 'system'
                      ? 'bg-black/5 text-[#8b4513]/60 border border-black/5 rounded-xl px-6 font-marker italic'
                      : 'bg-[#f4ece1] text-[#3e2723] rounded-2xl rounded-bl-sm font-hand text-base border border-[#8b4513]/20'
                  }`}
                >
                  {m.role === 'chief' && (
                    <span className="text-[10px] font-marker text-[#8b4513]/50 block mb-1">🤖 Chief</span>
                  )}
                  {m.content}
                </span>
              </div>
            ))}
            {loading && (
              <div className="text-left">
                <span className="inline-block px-5 py-3 bg-[#f4ece1] text-[#3e2723] rounded-2xl rounded-bl-sm font-hand text-sm border border-[#8b4513]/20 animate-pulse">
                  🤖 Chief is thinking...
                </span>
              </div>
            )}
            {messages.length === 0 && !loading && (
              <div className="text-center py-12 text-[#8b4513]/30 flex flex-col items-center justify-center h-full">
                <p className="text-5xl mb-4 opacity-40">🤖</p>
                <p className="font-marker text-lg text-[#8b4513]/50">Talk to your Chief Agent</p>
                <p className="font-hand text-sm mt-1">Ask anything about the project, give instructions, or set policies...</p>
              </div>
            )}
            <div ref={chatEndRef} />
          </div>

          <div className="flex gap-3 shrink-0">
            <input
              type="text"
              value={inputText}
              onChange={(e) => setInputText(e.target.value)}
              onKeyDown={(e) => e.key === 'Enter' && !loading && handleSend()}
              placeholder="Ask Chief Agent anything..."
              className="flex-1 bg-[#f4ece1] border border-[#8b4513]/30 rounded-2xl px-5 py-3.5 text-sm font-hand text-[#3e2723] placeholder-[#8b4513]/30 shadow-inner focus:ring-2 focus:ring-[#8b4513] outline-none transition-all"
              disabled={loading}
            />
            <button
              onClick={handleSend}
              disabled={loading}
              className="btn-cabin px-8 py-3.5 rounded-2xl text-sm font-marker shadow-lg active:scale-95 disabled:opacity-50"
            >
              Send
            </button>
          </div>
        </div>
      )}

      {/* Policies Tab */}
      {tab === 'policies' && (
        <div className="flex-1 overflow-y-auto custom-scrollbar pr-2">
          {policies.length === 0 ? (
            <div className="text-center py-16 text-[#8b4513]/30">
              <p className="text-4xl mb-3 opacity-40">📜</p>
              <p className="font-marker text-lg text-[#8b4513]/50">No policies yet</p>
              <p className="font-hand text-sm mt-1">Tell Chief Agent your rules in chat, and it will create policies for you.</p>
            </div>
          ) : (
            <div className="space-y-3">
              {policies.map((p) => {
                let matchObj: any = {}
                let actionsObj: any = {}
                try { matchObj = JSON.parse(p.match_condition) } catch {}
                try { actionsObj = JSON.parse(p.actions) } catch {}

                return (
                  <div key={p.id} className="parchment border border-[#8b4513]/20 rounded-2xl p-5 shadow-sm hover:shadow-md transition-all">
                    <div className="flex items-center gap-3 mb-3">
                      <span className="text-lg">{sourceIcon(p.source)}</span>
                      <span className="font-marker text-sm text-[#5d4037] flex-1">{p.name}</span>
                      <span className="text-[10px] font-bold bg-[#8b4513]/10 text-[#8b4513]/60 px-2 py-0.5 rounded font-mono">
                        P{p.priority}
                      </span>
                      <span className={`text-[10px] font-bold px-2 py-0.5 rounded border ${p.status === 'active' ? 'bg-emerald-50 text-emerald-600 border-emerald-200' : 'bg-gray-50 text-gray-500 border-gray-200'}`}>
                        {p.status}
                      </span>
                    </div>
                    <div className="grid grid-cols-2 gap-3">
                      <div>
                        <p className="text-[9px] font-bold text-[#8b4513]/40 uppercase tracking-widest mb-1">Match</p>
                        <pre className="text-xs font-mono text-[#5d4037]/70 bg-white/40 p-2 rounded-lg border border-[#8b4513]/10 overflow-x-auto">
                          {JSON.stringify(matchObj, null, 2)}
                        </pre>
                      </div>
                      <div>
                        <p className="text-[9px] font-bold text-[#8b4513]/40 uppercase tracking-widest mb-1">Actions</p>
                        <pre className="text-xs font-mono text-[#5d4037]/70 bg-white/40 p-2 rounded-lg border border-[#8b4513]/10 overflow-x-auto">
                          {JSON.stringify(actionsObj, null, 2)}
                        </pre>
                      </div>
                    </div>
                    {p.status === 'candidate' && (
                      <div className="flex gap-2 mt-3">
                        <button
                          onClick={() => handleActivatePolicy(p.id)}
                          className="px-3 py-1.5 text-xs font-marker bg-emerald-600 text-white rounded-lg hover:bg-emerald-700 transition-all"
                        >
                          Activate
                        </button>
                        <button
                          onClick={() => handleDeactivatePolicy(p.id)}
                          className="px-3 py-1.5 text-xs font-marker bg-gray-400 text-white rounded-lg hover:bg-gray-500 transition-all"
                        >
                          Deprecate
                        </button>
                      </div>
                    )}
                    {p.status === 'active' && (
                      <div className="mt-3">
                        <button
                          onClick={() => handleDeactivatePolicy(p.id)}
                          className="px-3 py-1.5 text-xs font-marker bg-rose-500 text-white rounded-lg hover:bg-rose-600 transition-all"
                        >
                          Deactivate
                        </button>
                      </div>
                    )}
                  </div>
                )
              })}
            </div>
          )}
        </div>
      )}

      {/* Sessions Tab */}
      {tab === 'sessions' && (
        <div className="flex-1 overflow-y-auto custom-scrollbar pr-2">
          {sessions.length === 0 ? (
            <div className="text-center py-16 text-[#8b4513]/30">
              <p className="text-4xl mb-3 opacity-40">📋</p>
              <p className="font-marker text-lg text-[#8b4513]/50">No sessions yet</p>
              <p className="font-hand text-sm mt-1">Agent sessions will appear here after they run.</p>
            </div>
          ) : (
            <div className="space-y-3">
              {sessions.map((s) => (
                <div key={s.id}>
                  <div
                    onClick={() => toggleSessionExpand(s.id)}
                    className="parchment border border-[#8b4513]/20 rounded-2xl p-4 shadow-sm hover:shadow-md transition-all cursor-pointer"
                  >
                    <div className="flex items-center gap-3">
                      <span className={`text-[10px] font-bold px-2 py-0.5 rounded border ${statusColor(s.status)}`}>
                        {s.status}
                      </span>
                      <span className="font-marker text-sm text-[#5d4037] flex-1">{s.role}</span>
                      <span className="text-[10px] font-mono text-[#8b4513]/40">{s.trigger_reason}</span>
                      {s.duration_ms > 0 && (
                        <span className="text-[10px] font-mono text-[#8b4513]/40">{(s.duration_ms / 1000).toFixed(1)}s</span>
                      )}
                      {s.retry_count > 0 && (
                        <span className="text-[10px] font-bold bg-amber-50 text-amber-600 px-2 py-0.5 rounded border border-amber-200">
                          retry:{s.retry_count}
                        </span>
                      )}
                      <span className="text-lg text-[#8b4513]/30">{expandedSession === s.id ? '▾' : '▸'}</span>
                    </div>
                    <div className="text-[10px] font-mono text-[#8b4513]/30 mt-1">
                      {new Date(s.created_at).toLocaleString()}
                    </div>
                  </div>

                  {expandedSession === s.id && traces.length > 0 && (
                    <div className="ml-6 mt-2 space-y-2">
                      {traces.map((t) => (
                        <div key={t.id} className="bg-white/40 border border-[#8b4513]/10 rounded-xl p-3 text-xs">
                          <div className="flex items-center gap-2 mb-1">
                            <span className={`font-bold ${t.success ? 'text-emerald-600' : 'text-rose-600'}`}>
                              {t.success ? '✓' : '✗'}
                            </span>
                            <span className="font-mono font-bold text-[#5d4037]">{t.tool_name}</span>
                            <span className="ml-auto text-[#8b4513]/30 font-mono">
                              {new Date(t.created_at).toLocaleTimeString()}
                            </span>
                          </div>
                          {t.args && (
                            <pre className="font-mono text-[10px] text-[#8b4513]/50 bg-[#f4ece1]/50 p-1.5 rounded overflow-x-auto">
                              {typeof t.args === 'string' ? t.args : JSON.stringify(t.args, null, 2)}
                            </pre>
                          )}
                          {t.result_summary && (
                            <p className="font-hand text-[11px] text-[#5d4037]/60 mt-1">{t.result_summary}</p>
                          )}
                        </div>
                      ))}
                    </div>
                  )}
                </div>
              ))}
            </div>
          )}
        </div>
      )}

      {/* Experience Tab */}
      {tab === 'experience' && (
        <div className="flex-1 overflow-y-auto custom-scrollbar pr-2">
          <div className="flex items-center gap-2 mb-4">
            {['raw', 'distilled', 'skill', ''].map((s) => (
              <button
                key={s}
                onClick={() => { setExpStatusFilter(s); }}
                className={`px-3 py-1.5 rounded-lg text-xs font-marker transition-all ${
                  expStatusFilter === s
                    ? 'bg-[#5d4037] text-[#efebe9] shadow-sm'
                    : 'bg-[#f4ece1] text-[#5d4037]/60 border border-[#8b4513]/10 hover:bg-[#8b4513]/10'
                }`}
              >
                {s === '' ? 'All' : s.charAt(0).toUpperCase() + s.slice(1)}
              </button>
            ))}
          </div>

          {experiences.length === 0 ? (
            <div className="text-center py-16 text-[#8b4513]/30">
              <p className="text-4xl mb-3 opacity-40">🧠</p>
              <p className="font-marker text-lg text-[#8b4513]/50">No experiences yet</p>
              <p className="font-hand text-sm mt-1">Agent feedback and audit observations will appear here.</p>
            </div>
          ) : (
            <div className="space-y-3">
              {experiences.map((exp) => {
                const sourceIcon: Record<string, string> = {
                  agent_feedback: '💬',
                  audit_observation: '🔍',
                  fix_strategy: '🔧',
                  eval_pattern: '📊',
                  maintain_rationale: '📝',
                }
                const outcomeColor: Record<string, string> = {
                  success: 'text-emerald-600',
                  partial: 'text-amber-600',
                  failed: 'text-rose-600',
                }
                return (
                  <div key={exp.id} className="parchment border border-[#8b4513]/20 rounded-2xl p-4 shadow-sm hover:shadow-md transition-all">
                    <div className="flex items-center gap-2 mb-2">
                      <span className="text-lg">{sourceIcon[exp.source_type] || '📝'}</span>
                      <span className="text-[10px] font-bold bg-[#8b4513]/10 text-[#8b4513]/60 px-2 py-0.5 rounded font-mono">
                        {exp.source_type}
                      </span>
                      <span className="text-[10px] font-mono text-[#8b4513]/30">{exp.agent_role}</span>
                      {exp.outcome && (
                        <span className={`text-[10px] font-bold ${outcomeColor[exp.outcome] || 'text-gray-500'}`}>
                          {exp.outcome}
                        </span>
                      )}
                      <span className={`ml-auto text-[10px] font-bold px-2 py-0.5 rounded border ${
                        exp.status === 'raw' ? 'bg-amber-50 text-amber-600 border-amber-200' :
                        exp.status === 'distilled' ? 'bg-blue-50 text-blue-600 border-blue-200' :
                        exp.status === 'skill' ? 'bg-emerald-50 text-emerald-600 border-emerald-200' :
                        'bg-gray-50 text-gray-500 border-gray-200'
                      }`}>
                        {exp.status}
                      </span>
                    </div>
                    {exp.key_insight && (
                      <p className="font-hand text-sm text-[#5d4037] mb-2 bg-white/40 p-2 rounded-lg border border-[#8b4513]/10">
                        💡 {exp.key_insight}
                      </p>
                    )}
                    {exp.pattern_observed && (
                      <p className="font-hand text-xs text-[#8b4513]/60 mb-1">
                        🔍 Pattern: {exp.pattern_observed}
                      </p>
                    )}
                    {exp.do_differently && (
                      <p className="font-hand text-xs text-[#8b4513]/60 mb-1">
                        🔄 Do differently: {exp.do_differently}
                      </p>
                    )}
                    {exp.pitfalls && (
                      <p className="font-hand text-xs text-[#8b4513]/60 mb-1">
                        ⚠️ Pitfalls: {exp.pitfalls}
                      </p>
                    )}
                    {exp.fix_strategy && (
                      <p className="font-hand text-xs text-[#8b4513]/60 mb-1">
                        🔧 Fix strategy: {exp.fix_strategy}
                      </p>
                    )}
                    {exp.false_positive && (
                      <span className="text-[10px] font-bold bg-rose-50 text-rose-600 px-2 py-0.5 rounded border border-rose-200">
                        False Positive
                      </span>
                    )}
                    <div className="text-[10px] font-mono text-[#8b4513]/30 mt-2">
                      {new Date(exp.created_at).toLocaleString()}
                    </div>
                  </div>
                )
              })}
            </div>
          )}
        </div>
      )}

      {/* Skills Tab */}
      {tab === 'skills' && (
        <div className="flex-1 overflow-y-auto custom-scrollbar pr-2">
          <div className="flex items-center gap-2 mb-4">
            {['candidate', 'active', 'deprecated', ''].map((s) => (
              <button
                key={s}
                onClick={() => { setSkillStatusFilter(s); }}
                className={'px-3 py-1.5 rounded-lg text-xs font-marker transition-all ' + (skillStatusFilter === s ? 'bg-[#5d4037] text-[#efebe9] shadow-sm' : 'bg-[#f4ece1] text-[#5d4037]/60 border border-[#8b4513]/10 hover:bg-[#8b4513]/10')}
              >
                {s === '' ? 'All' : s.charAt(0).toUpperCase() + s.slice(1)}
              </button>
            ))}
          </div>

          {skills.length === 0 ? (
            <div className="text-center py-16 text-[#8b4513]/30">
              <p className="text-4xl mb-3 opacity-40">⚡</p>
              <p className="font-marker text-lg text-[#8b4513]/50">No skills yet</p>
              <p className="font-hand text-sm mt-1">Analyze Agent will distill experiences into skills.</p>
            </div>
          ) : (
            <div className="space-y-3">
              {skills.map((sk: any) => {
                const typeIcon: Record<string, string> = { process: '⚙️', prompt: '💬', routing: '🔀', guard: '🛡️' }
                return (
                  <div key={sk.id} className="parchment border border-[#8b4513]/20 rounded-2xl p-4 shadow-sm hover:shadow-md transition-all">
                    <div className="flex items-center gap-2 mb-2">
                      <span className="text-lg">{typeIcon[sk.type] || '📝'}</span>
                      <span className="font-marker font-bold text-[#5d4037]">{sk.name}</span>
                      <span className="text-[10px] font-bold bg-[#8b4513]/10 text-[#8b4513]/60 px-2 py-0.5 rounded font-mono">
                        {sk.type}
                      </span>
                      <span className={'ml-auto text-[10px] font-bold px-2 py-0.5 rounded border ' + (
                        sk.status === 'candidate' ? 'bg-amber-50 text-amber-600 border-amber-200' :
                        sk.status === 'active' ? 'bg-emerald-50 text-emerald-600 border-emerald-200' :
                        sk.status === 'rejected' ? 'bg-rose-50 text-rose-600 border-rose-200' :
                        'bg-gray-50 text-gray-500 border-gray-200'
                      )}>
                        {sk.status}
                      </span>
                    </div>
                    {sk.action && (
                      <p className="font-hand text-sm text-[#5d4037] mb-1 bg-white/40 p-2 rounded-lg border border-[#8b4513]/10">
                        Action: {sk.action}
                      </p>
                    )}
                    {sk.precondition && (
                      <p className="font-hand text-xs text-[#8b4513]/60 mb-1">
                        Precondition: {sk.precondition}
                      </p>
                    )}
                    {sk.prohibition && (
                      <p className="font-hand text-xs text-rose-500/70 mb-1">
                        Prohibition: {sk.prohibition}
                      </p>
                    )}
                    {sk.evidence && (
                      <p className="font-hand text-xs text-[#8b4513]/40 mb-1">
                        Evidence: {sk.evidence}
                      </p>
                    )}
                    {sk.status === 'candidate' && (
                      <div className="flex gap-2 mt-3">
                        <button
                          onClick={() => handleApproveSkill(sk.id)}
                          className="px-3 py-1.5 text-xs font-marker bg-emerald-600 text-white rounded-lg hover:bg-emerald-700 transition-all"
                        >
                          Approve
                        </button>
                        <button
                          onClick={() => handleRejectSkill(sk.id)}
                          className="px-3 py-1.5 text-xs font-marker bg-rose-500 text-white rounded-lg hover:bg-rose-600 transition-all"
                        >
                          Reject
                        </button>
                      </div>
                    )}
                  </div>
                )
              })}
            </div>
          )}
        </div>
      )}
    </div>
  )
}
