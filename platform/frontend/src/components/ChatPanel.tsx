import { useState } from 'react'
import { useAppStore } from '../stores/appStore'
import { dashboardApi, changeApi, projectApi } from '../api/endpoints'

/**
 * ChatPanel — the right-column conversation surface with the Maintain
 * agent (Overview page).
 *
 * Visual: dark surface, bubbles use the same cream paper-card look as
 * the kanban, so the conversation reads like "notes the agent sent
 * across the desk".  Human messages are an indigo-tinted dark pill on
 * the right, system messages are muted inline chips.
 */

function IconPen() {
  return (
    <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round">
      <path d="M12 20h9"/>
      <path d="M16.5 3.5a2.121 2.121 0 1 1 3 3L7 19l-4 1 1-4L16.5 3.5z"/>
    </svg>
  )
}

function IconSend() {
  return (
    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <path d="m22 2-7 20-4-9-9-4 20-7z"/>
    </svg>
  )
}

export function ChatPanel() {
  const {
    chatMessages, inputText, targetBlock, setInputText, addChatMessage, setTargetBlock,
    pendingInput, setPendingInput, selectedProjectId,
    autoMode, setAutoMode, pendingChanges, removePendingChange
  } = useAppStore()

  const handleSend = async () => {
    if (!inputText.trim() || !selectedProjectId) return
    const msg = inputText.trim()
    setInputText('')
    addChatMessage({ id: Date.now().toString(), role: 'human', content: msg, timestamp: Date.now() })

    try {
      const res = await dashboardApi.input(selectedProjectId, targetBlock, msg)
      if (res.success) {
        const data = res.data
        if (data.requires_confirm || data.status === 'pending_confirmation') {
          addChatMessage({
            id: (Date.now() + 1).toString(),
            role: 'agent',
            content: `Input received. Please confirm to update the ${targetBlock} block.`,
            timestamp: Date.now(),
          })
          setPendingInput({
            input_id: data.input_id,
            target_block: targetBlock,
            content: msg,
            requires_confirm: data.requires_confirm || false,
          })
        } else if (data.agent_response) {
          addChatMessage({
            id: (Date.now() + 1).toString(),
            role: 'agent',
            content: data.agent_response,
            timestamp: Date.now(),
          })
        } else if (data.session_active) {
          addChatMessage({
            id: (Date.now() + 1).toString(),
            role: 'agent',
            content: 'Task creation request sent to maintain agent.',
            timestamp: Date.now(),
          })
        } else {
          addChatMessage({
            id: (Date.now() + 1).toString(),
            role: 'agent',
            content: 'Input submitted successfully.',
            timestamp: Date.now(),
          })
        }
      }
    } catch {
      addChatMessage({
        id: (Date.now() + 1).toString(),
        role: 'agent',
        content: 'Failed to submit input.',
        timestamp: Date.now(),
      })
    }
  }

  const handleConfirm = async () => {
    if (!pendingInput || !selectedProjectId) return
    try {
      const res = await dashboardApi.confirm(selectedProjectId, pendingInput.input_id, true)
      if (res.success) {
        addChatMessage({
          id: Date.now().toString(),
          role: 'system',
          content: `${pendingInput.target_block} block updated and confirmed.`,
          timestamp: Date.now(),
        })
        if (res.data?.context_cleared) {
          addChatMessage({
            id: (Date.now() + 1).toString(),
            role: 'system',
            content: 'Session context cleared.',
            timestamp: Date.now(),
          })
        }
      }
    } catch {
      addChatMessage({
        id: Date.now().toString(),
        role: 'system',
        content: 'Failed to confirm update.',
        timestamp: Date.now(),
      })
    }
    setPendingInput(null)
  }

  const handleCancel = async () => {
    if (!pendingInput || !selectedProjectId) return
    try {
      await dashboardApi.confirm(selectedProjectId, pendingInput.input_id, false)
    } catch {}
    addChatMessage({
      id: Date.now().toString(),
      role: 'system',
      content: 'Update cancelled.',
      timestamp: Date.now(),
    })
    setPendingInput(null)
  }

  const handleClearChat = async () => {
    if (selectedProjectId) {
      try {
        await dashboardApi.clearContext(selectedProjectId)
      } catch {}
    }
    useAppStore.getState().clearChat()
    setPendingInput(null)
  }

  // Inline reject form state — only one change can be in "writing reject
  // reason" mode at a time. Matches the visual model of the pending card
  // expanding in place rather than opening a modal.
  const [rejectingId, setRejectingId] = useState<string | null>(null)
  const [rejectReason, setRejectReason] = useState('')

  // submitVerdict is the single backend call for both approve and reject.
  // We omit `level` so the server applies its defaults (L0 on approve,
  // L2 on reject). The same endpoint handles both `pending` (legacy
  // autopilot) and `pending_human_confirm` (collaboration-hub) source
  // statuses, so this works regardless of A3C_AUTOPILOT.
  const submitVerdict = async (changeId: string, approved: boolean, reason: string) => {
    try {
      const res = await changeApi.review(changeId, '', approved, reason)
      if (res.success) {
        removePendingChange(changeId)
        addChatMessage({
          id: Date.now().toString(),
          role: 'system',
          content: approved
            ? `Change ${changeId.slice(0, 8)} approved and committed.`
            : `Change ${changeId.slice(0, 8)} rejected.`,
          timestamp: Date.now(),
        })
      } else {
        addChatMessage({
          id: Date.now().toString(),
          role: 'system',
          content: `Failed to ${approved ? 'approve' : 'reject'} change.`,
          timestamp: Date.now(),
        })
      }
    } catch (e: any) {
      addChatMessage({
        id: Date.now().toString(),
        role: 'system',
        content: `Failed to ${approved ? 'approve' : 'reject'} change: ${e?.response?.data?.error?.message || 'unknown error'}.`,
        timestamp: Date.now(),
      })
    }
  }

  const handleApproveChange = async (changeId: string) => {
    await submitVerdict(changeId, true, 'Approved')
  }

  const handleStartReject = (changeId: string) => {
    setRejectingId(changeId)
    setRejectReason('')
  }

  const handleCancelReject = () => {
    setRejectingId(null)
    setRejectReason('')
  }

  const handleSubmitReject = async (changeId: string) => {
    const reason = rejectReason.trim()
    if (!reason) return
    await submitVerdict(changeId, false, reason)
    setRejectingId(null)
    setRejectReason('')
  }

  const handleToggleAutoMode = async () => {
    if (!selectedProjectId) return
    const newMode = !autoMode
    try {
      const res = await projectApi.setAutoMode(selectedProjectId, newMode)
      if (res.success) setAutoMode(newMode)
    } catch {}
  }

  const inputClass = 'flex-1 rounded-lg px-3 py-2 text-[13px] outline-none transition-colors'
  const inputStyle: React.CSSProperties = {
    background: 'var(--surface-2)',
    border: '1px solid var(--border)',
    color: 'var(--text-0)',
  }

  return (
    <div className="flex flex-col h-full">
      {/* toolbar */}
      <div className="flex items-center gap-2 mb-3 shrink-0">
        <select
          value={targetBlock}
          onChange={(e) => setTargetBlock(e.target.value as any)}
          className="chip pr-7 appearance-none bg-no-repeat bg-right"
          style={{
            backgroundImage: "url(\"data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='12' height='12' viewBox='0 0 24 24' fill='none' stroke='%2371717a' stroke-width='2'%3E%3Cpolyline points='6 9 12 15 18 9'/%3E%3C/svg%3E\")",
            backgroundPosition: 'right 6px center',
            paddingRight: 24,
          }}
        >
          <option value="direction">Direction</option>
          <option value="milestone">Milestone</option>
          <option value="task">Task</option>
        </select>
        <button
          onClick={handleToggleAutoMode}
          className={`chip ${autoMode ? 'chip-green' : 'chip-amber'}`}
          title={autoMode ? 'Auto mode: changes auto-sent to audit' : 'Manual mode: changes require your approval before audit'}
        >
          {autoMode ? 'Auto' : 'Manual'}
        </button>
        <button
          onClick={handleClearChat}
          className="chip ml-auto"
          title="Clear conversation"
        >
          <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><path d="M3 6h18"/><path d="M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/><path d="M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6"/></svg>
          Clear
        </button>
        <span className="chip font-mono-jb text-[10.5px]">{chatMessages.length}</span>
      </div>

      {/* messages */}
      <div className="flex-1 overflow-y-auto custom-scrollbar space-y-3 pr-1">
        {chatMessages.map((m) => {
          if (m.role === 'human') {
            return (
              <div key={m.id} className="flex justify-end">
                <div
                  className="max-w-[85%] px-3.5 py-2 rounded-2xl rounded-br-md text-[13px] leading-relaxed break-words"
                  style={{
                    background: 'linear-gradient(135deg, #6366f1, #4f46e5)',
                    color: '#fafafa',
                    boxShadow: '0 4px 12px -4px rgba(99,102,241,0.5)',
                  }}
                >
                  {m.content}
                </div>
              </div>
            )
          }
          if (m.role === 'system') {
            return (
              <div key={m.id} className="flex justify-center">
                <span className="chip font-mono-jb text-[10.5px] italic">{m.content}</span>
              </div>
            )
          }
          // agent — cream paper bubble
          return (
            <div key={m.id} className="flex justify-start">
              <div
                className="max-w-[85%] card-paper rounded-2xl rounded-bl-md text-[13px] leading-relaxed break-words"
                style={{ padding: '10px 14px', borderRadius: '14px 14px 14px 4px' }}
              >
                <span className="relative z-[2] whitespace-pre-wrap">{m.content}</span>
              </div>
            </div>
          )
        })}

        {chatMessages.length === 0 && (
          <div className="h-full flex flex-col items-center justify-center py-12" style={{ color: 'var(--text-2)' }}>
            <div className="mb-3 opacity-50"><IconPen /></div>
            <p className="text-[13px] font-medium" style={{ color: 'var(--text-1)' }}>Start your collaboration</p>
            <p className="text-[11.5px] mt-1">Agent is waiting for your signal…</p>
          </div>
        )}
      </div>

      {/* pending input confirm */}
      {pendingInput && (
        <div
          className="mt-3 mb-3 p-3 rounded-lg shrink-0 animate-fade-in"
          style={{
            background: 'rgba(99, 102, 241, 0.06)',
            border: '1px solid rgba(99, 102, 241, 0.2)',
          }}
        >
          <div className="flex items-center gap-2 text-[10.5px] uppercase tracking-[0.08em] font-semibold mb-2" style={{ color: '#a5b4fc' }}>
            <span>Confirm update to</span>
            <span className="chip chip-blue font-mono-jb text-[10px]">{pendingInput.target_block}</span>
          </div>
          <div
            className="text-[12.5px] px-3 py-2 rounded-md mb-3 max-h-24 overflow-y-auto custom-scrollbar whitespace-pre-wrap break-words italic"
            style={{ background: 'rgba(0,0,0,0.25)', color: 'var(--text-1)' }}
          >
            {pendingInput.content}
          </div>
          <div className="flex gap-2">
            <button
              onClick={handleConfirm}
              className="flex-1 text-[12px] font-medium px-3 py-1.5 rounded-md transition-colors"
              style={{ background: '#6366f1', color: 'white' }}
              onMouseEnter={(e) => { e.currentTarget.style.background = '#4f46e5' }}
              onMouseLeave={(e) => { e.currentTarget.style.background = '#6366f1' }}
            >
              Confirm
            </button>
            <button
              onClick={handleCancel}
              className="flex-1 chip justify-center"
            >
              Cancel
            </button>
          </div>
        </div>
      )}

      {/* pending changes */}
      {pendingChanges.length > 0 && (
        <div
          className="mt-3 mb-3 p-3 rounded-lg shrink-0"
          style={{
            background: 'rgba(245, 158, 11, 0.06)',
            border: '1px solid rgba(245, 158, 11, 0.2)',
          }}
        >
          <p className="text-[10.5px] uppercase tracking-[0.08em] font-semibold mb-2" style={{ color: '#fcd34d' }}>
            Pending Human Confirmation
          </p>
          {pendingChanges.map((c) => {
            const isRejecting = rejectingId === c.change_id
            return (
              <div key={c.change_id} className="mb-2 last:mb-0">
                <div className="flex items-center gap-2">
                  <span className="text-[12.5px] flex-1 truncate" style={{ color: 'var(--text-0)' }}>
                    {c.description || c.change_id}
                  </span>
                  {!isRejecting && (
                    <>
                      <button
                        onClick={() => handleApproveChange(c.change_id)}
                        className="text-[11px] font-medium px-2.5 py-1 rounded transition-colors"
                        style={{ background: '#10b981', color: 'white' }}
                        onMouseEnter={(e) => { e.currentTarget.style.background = '#059669' }}
                        onMouseLeave={(e) => { e.currentTarget.style.background = '#10b981' }}
                      >
                        Approve
                      </button>
                      <button
                        onClick={() => handleStartReject(c.change_id)}
                        className="text-[11px] font-medium px-2.5 py-1 rounded transition-colors"
                        style={{ background: '#dc2626', color: 'white' }}
                        onMouseEnter={(e) => { e.currentTarget.style.background = '#b91c1c' }}
                        onMouseLeave={(e) => { e.currentTarget.style.background = '#dc2626' }}
                      >
                        Reject
                      </button>
                    </>
                  )}
                </div>
                {isRejecting && (
                  <div className="mt-2 flex flex-col gap-2">
                    <textarea
                      autoFocus
                      value={rejectReason}
                      onChange={(e) => setRejectReason(e.target.value)}
                      placeholder="Why are you rejecting? The submitter will see this verbatim."
                      rows={3}
                      className="text-[12px] px-2.5 py-2 rounded resize-y outline-none"
                      style={{
                        background: 'rgba(0,0,0,0.25)',
                        border: '1px solid var(--border)',
                        color: 'var(--text-0)',
                      }}
                    />
                    <div className="flex gap-2 justify-end">
                      <button
                        onClick={handleCancelReject}
                        className="text-[11px] font-medium px-2.5 py-1 rounded chip"
                      >
                        Cancel
                      </button>
                      <button
                        onClick={() => handleSubmitReject(c.change_id)}
                        disabled={!rejectReason.trim()}
                        className="text-[11px] font-medium px-2.5 py-1 rounded transition-colors disabled:opacity-40 disabled:cursor-not-allowed"
                        style={{ background: '#dc2626', color: 'white' }}
                        onMouseEnter={(e) => { if (!e.currentTarget.disabled) e.currentTarget.style.background = '#b91c1c' }}
                        onMouseLeave={(e) => { if (!e.currentTarget.disabled) e.currentTarget.style.background = '#dc2626' }}
                      >
                        Submit Rejection
                      </button>
                    </div>
                  </div>
                )}
              </div>
            )
          })}
        </div>
      )}

      {/* composer */}
      <div className="mt-3 flex gap-2 shrink-0">
        <input
          type="text"
          value={inputText}
          onChange={(e) => setInputText(e.target.value)}
          onKeyDown={(e) => e.key === 'Enter' && handleSend()}
          placeholder="Write your message…"
          className={inputClass}
          style={inputStyle}
          onFocus={(e) => (e.currentTarget.style.borderColor = 'var(--accent)')}
          onBlur={(e) => (e.currentTarget.style.borderColor = 'var(--border)')}
        />
        <button
          onClick={handleSend}
          disabled={!inputText.trim()}
          className="inline-flex items-center gap-1.5 px-4 py-2 rounded-lg text-[13px] font-medium transition-all disabled:opacity-40 disabled:cursor-not-allowed"
          style={{
            background: 'linear-gradient(135deg, #6366f1, #4f46e5)',
            color: 'white',
            boxShadow: '0 4px 12px -4px rgba(99, 102, 241, 0.5)',
          }}
        >
          <IconSend />
          Send
        </button>
      </div>
    </div>
  )
}
