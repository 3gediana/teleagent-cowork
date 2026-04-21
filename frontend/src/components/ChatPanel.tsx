import { useAppStore } from '../stores/appStore'
import { dashboardApi } from '../api/endpoints'

export function ChatPanel() {
  const {
    chatMessages, inputText, targetBlock, setInputText, addChatMessage, setTargetBlock,
    pendingInput, setPendingInput, selectedProjectId, sessionId
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
    if (sessionId) {
      try {
        await dashboardApi.clearContext(sessionId)
      } catch {}
    }
    useAppStore.getState().clearChat()
    setPendingInput(null)
  }

  return (
    <div className="flex flex-col h-full">
      <div className="flex items-center gap-3 mb-6 px-1 shrink-0">
        <select
          value={targetBlock}
          onChange={(e) => setTargetBlock(e.target.value as any)}
          className="bg-[#f4ece1] border border-[#8b4513]/30 rounded-lg px-3 py-1.5 text-sm font-marker text-[#5d4037] shadow-sm focus:ring-2 focus:ring-[#8b4513] outline-none"
        >
          <option value="direction">Direction</option>
          <option value="milestone">Milestone</option>
          <option value="task">Task</option>
        </select>
        <button
          onClick={handleClearChat}
          className="text-xs font-marker text-[#8b4513]/60 hover:text-rose-700 transition-colors bg-[#f4ece1]/50 border border-[#8b4513]/10 px-3 py-1.5 rounded-lg"
        >
          Clear Desk
        </button>
        <span className="ml-auto text-[10px] font-bold text-[#8b4513]/40 uppercase tracking-widest">
          {chatMessages.length} Messages
        </span>
      </div>

      <div className="flex-1 overflow-y-auto mb-6 space-y-4 custom-scrollbar pr-2">
        {chatMessages.map((m) => (
          <div key={m.id} className={`${m.role === 'human' ? 'text-right' : m.role === 'system' ? 'text-center' : 'text-left'}`}>
            <span
              className={`inline-block max-w-[90%] px-5 py-3 text-sm shadow-md transition-all hover:scale-[1.02] ${
                m.role === 'human'
                  ? 'bg-[#5d4037] text-[#efebe9] rounded-2xl rounded-br-sm font-type'
                  : m.role === 'system'
                  ? 'bg-black/5 text-[#8b4513]/60 border border-black/5 rounded-xl px-6 font-marker italic'
                  : 'parchment text-[#3e2723] rounded-2xl rounded-bl-sm font-hand text-base border border-[#8b4513]/20'
              }`}
            >
              {m.content}
            </span>
          </div>
        ))}
        {chatMessages.length === 0 && (
          <div className="text-center py-12 text-[#8b4513]/30 flex flex-col items-center justify-center h-full">
            <p className="text-5xl mb-4 opacity-40">🖋️</p>
            <p className="font-marker text-lg text-[#8b4513]/50">Start your collaboration</p>
            <p className="font-hand text-sm mt-1">Agent is waiting for your signal...</p>
          </div>
        )}
      </div>

      {pendingInput && (
        <div className="bg-[#8b4513]/10 border border-[#8b4513]/20 rounded-2xl p-5 mb-6 shadow-inner shrink-0 animate-in fade-in slide-in-from-bottom-4">
          <p className="text-xs font-marker text-[#8b4513] mb-3 uppercase tracking-widest">
            Confirm update to <strong className="bg-[#8b4513] text-white px-2 py-0.5 rounded ml-1">{pendingInput.target_block}</strong>?
          </p>
          <div className="font-hand text-base text-[#5d4037] mb-4 bg-white/40 p-4 rounded-xl border border-white/20 italic break-words whitespace-pre-wrap max-h-24 overflow-y-auto custom-scrollbar shadow-inner">
            "{pendingInput.content}"
          </div>
          <div className="flex gap-3">
            <button
              onClick={handleConfirm}
              className="flex-1 btn-cabin px-4 py-2.5 rounded-xl text-xs font-marker"
            >
              Confirm
            </button>
            <button
              onClick={handleCancel}
              className="flex-1 bg-white/50 hover:bg-white/80 text-[#5d4037] border border-[#8b4513]/20 px-4 py-2.5 rounded-xl text-xs font-marker transition-all"
            >
              Cancel
            </button>
          </div>
        </div>
      )}

      <div className="flex gap-3 shrink-0">
        <input
          type="text"
          value={inputText}
          onChange={(e) => setInputText(e.target.value)}
          onKeyDown={(e) => e.key === 'Enter' && handleSend()}
          placeholder="Write your message..."
          className="flex-1 bg-[#f4ece1] border border-[#8b4513]/30 rounded-2xl px-5 py-3.5 text-sm font-hand text-[#3e2723] placeholder-[#8b4513]/30 shadow-inner focus:ring-2 focus:ring-[#8b4513] outline-none transition-all"
        />
        <button
          onClick={handleSend}
          className="btn-cabin px-8 py-3.5 rounded-2xl text-sm font-marker shadow-lg active:scale-95"
        >
          Send
        </button>
      </div>
    </div>
  )
}

