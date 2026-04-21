import { useState, useEffect, useRef } from 'react'
import { useAppStore } from '../stores/appStore'
import { chiefApi } from '../api/endpoints'

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

type TabType = 'chat' | 'policies' | 'sessions'

export default function ChiefPage() {
  const { selectedProjectId } = useAppStore()
  const [tab, setTab] = useState<TabType>('chat')
  const [messages, setMessages] = useState<ChiefMessage[]>([])
  const [inputText, setInputText] = useState('')
  const [policies, setPolicies] = useState<Policy[]>([])
  const [sessions, setSessions] = useState<Session[]>([])
  const [expandedSession, setExpandedSession] = useState<string | null>(null)
  const [traces, setTraces] = useState<any[]>([])
  const [loading, setLoading] = useState(false)
  const chatEndRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (tab === 'policies') loadPolicies()
    if (tab === 'sessions' && selectedProjectId) loadSessions()
  }, [tab, selectedProjectId])

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
        {(['chat', 'policies', 'sessions'] as TabType[]).map((t) => (
          <button
            key={t}
            onClick={() => setTab(t)}
            className={`px-5 py-2 rounded-xl text-sm font-marker transition-all ${
              tab === t
                ? 'bg-[#5d4037] text-[#efebe9] shadow-md scale-105'
                : 'bg-[#f4ece1] text-[#5d4037]/70 border border-[#8b4513]/20 hover:bg-[#8b4513]/10'
            }`}
          >
            {t === 'chat' ? '💬 Chat' : t === 'policies' ? '📜 Policies' : '📋 Sessions'}
          </button>
        ))}
        <span className="ml-auto text-[10px] font-bold text-[#8b4513]/40 uppercase tracking-widest">
          Chief Agent
        </span>
      </div>

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
    </div>
  )
}
