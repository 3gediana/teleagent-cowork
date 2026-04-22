import { useMemo, useState } from 'react'
import { useAppStore } from '../stores/appStore'
import { policyApi, prApi, projectApi } from '../api/endpoints'

/**
 * AutoModeSwitch — the cabin-flavoured leather toggle that lives in
 * the top header. Flipping it on tells Chief to apply its AutoMode
 * decision rules without waiting for a human; flipping it off parks
 * every PR at pending_human_* until a human clicks Approve/Reject.
 *
 * Flipping always routes through a confirmation modal that tells the
 * operator *what will change right now*: how many PRs are waiting,
 * how many active policies Chief will consult, and the invariants
 * Chief still honours (no direct task mutation, always escalate on
 * high_risk). This follows the design doc's "no silent state change"
 * rule — a switch with a ghost behind it is worth the extra click.
 */
export function AutoModeSwitch() {
  const autoMode = useAppStore((s) => s.autoMode)
  const selectedProjectId = useAppStore((s) => s.selectedProjectId)
  const setAutoMode = useAppStore((s) => s.setAutoMode)
  const [modalOpen, setModalOpen] = useState(false)
  const [policyCount, setPolicyCount] = useState<number | null>(null)
  const [pendingCount, setPendingCount] = useState<number | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const target = useMemo(() => !autoMode, [autoMode])

  const openConfirm = async () => {
    if (!selectedProjectId) return
    setError(null)
    setModalOpen(true)
    setPolicyCount(null)
    setPendingCount(null)
    // Fetch context cheaply; safe to await in parallel.
    const [pol, prs] = await Promise.all([
      policyApi.list('active').catch(() => null),
      prApi.list().catch(() => null),
    ])
    if (pol?.success) setPolicyCount((pol.data.policies || []).length)
    else setPolicyCount(0)
    if (prs?.success) {
      const all = prs.data?.pull_requests || []
      setPendingCount(all.filter((p: { status: string }) => p.status === 'pending_human_review' || p.status === 'pending_human_merge').length)
    } else setPendingCount(0)
  }

  const confirm = async () => {
    if (!selectedProjectId) return
    setBusy(true)
    setError(null)
    // Optimistic UI: flip the store, then call the server.
    const prev = autoMode
    setAutoMode(target)
    try {
      const res = await projectApi.setAutoMode(selectedProjectId, target)
      if (!res.success) throw new Error('server refused')
      setModalOpen(false)
    } catch (e) {
      setAutoMode(prev) // rollback
      setError(e instanceof Error ? e.message : 'failed')
    } finally {
      setBusy(false)
    }
  }

  // The switch itself: a leather track with a brass rivet as the knob.
  const trackBase = 'relative inline-flex items-center w-[46px] h-[22px] rounded-full transition-colors duration-200 shadow-inner border'
  const trackOn  = 'bg-emerald-600 border-emerald-700 ring-2 ring-emerald-400/30 shadow-[0_0_12px_rgba(16,185,129,0.35)]'
  const trackOff = 'bg-[#8b4513]/30 border-[#8b4513]/40'

  const knobBase = 'absolute top-[2px] w-[16px] h-[16px] rounded-full transition-all duration-200 shadow-md'
  const knobOn   = 'left-[26px] bg-gradient-to-b from-[#f4ece1] to-[#d7c6a8] border border-[#efebe9]'
  const knobOff  = 'left-[2px] bg-gradient-to-b from-[#efebe9] to-[#c8b695] border border-[#8b4513]/30'

  return (
    <>
      <button
        type="button"
        role="switch"
        aria-checked={autoMode}
        aria-label={`AutoMode ${autoMode ? 'on' : 'off'}`}
        onClick={openConfirm}
        disabled={!selectedProjectId}
        className="group inline-flex items-center gap-2.5 px-3 py-1.5 rounded-xl bg-[#8b4513]/10 border border-[#8b4513]/20 hover:bg-[#8b4513]/15 transition-colors disabled:opacity-50"
      >
        <span className="flex flex-col items-start">
          <span className="text-[9px] font-marker uppercase tracking-[0.2em] text-[#5d4037]/70 leading-tight">AutoMode</span>
          <span className={`text-[10px] font-bold leading-tight ${autoMode ? 'text-emerald-700' : 'text-[#8b4513]/50'}`}>
            {autoMode ? 'Active · Chief decides' : 'Manual · Chief waits'}
          </span>
        </span>
        <span className={`${trackBase} ${autoMode ? trackOn : trackOff}`}>
          <span className={`${knobBase} ${autoMode ? knobOn : knobOff}`} />
        </span>
      </button>

      {modalOpen && (
        <div
          className="fixed inset-0 bg-[#3e2723]/50 backdrop-blur-sm z-50 flex items-center justify-center"
          onClick={() => !busy && setModalOpen(false)}
        >
          <div
            className="parchment w-[32rem] max-w-[90vw] rounded-3xl border border-[#8b4513]/30 shadow-2xl p-6"
            onClick={(e) => e.stopPropagation()}
          >
            <h3 className="font-marker text-xl text-[#5d4037] mb-1">
              {target ? '🚀 Enable AutoMode?' : '🖐️ Disable AutoMode?'}
            </h3>
            <p className="font-hand text-sm text-[#8b4513]/60 mb-4">
              {target
                ? 'Chief will start auto-deciding based on Evaluate\'s verdict and your active policies.'
                : 'Chief will stop auto-deciding. Any in-flight Chief sessions finish, then everything waits on you.'}
            </p>

            {target && (
              <div className="mb-4 p-3 rounded-xl bg-white/50 border border-[#8b4513]/15 text-xs font-hand text-[#5d4037] space-y-1.5">
                <p><strong className="font-marker text-[11px] tracking-wider text-[#5d4037]/80">CHIEF WILL:</strong></p>
                <ul className="pl-4 space-y-0.5 list-['✓'] marker:text-emerald-600">
                  <li className="pl-2">Approve PRs where Evaluate says <code className="font-mono text-[10px] bg-emerald-50 px-1 rounded">auto_advance</code>&nbsp;and no policy requires a human.</li>
                  <li className="pl-2">Reject PRs where the matching policy says <code className="font-mono text-[10px] bg-rose-50 px-1 rounded">require_human</code>&nbsp;(with the policy name as the reason).</li>
                  <li className="pl-2">Report status summaries via <code className="font-mono text-[10px] bg-[#8b4513]/10 px-1 rounded">chief_output</code>.</li>
                </ul>
                <p className="pt-1"><strong className="font-marker text-[11px] tracking-wider text-rose-700/80">CHIEF WILL NOT:</strong></p>
                <ul className="pl-4 space-y-0.5 list-['×'] marker:text-rose-600">
                  <li className="pl-2">Edit tasks or milestones directly — delegates to Maintain.</li>
                  <li className="pl-2">Merge anything Evaluate flagged <code className="font-mono text-[10px] bg-red-50 px-1 rounded">high_risk</code> — always escalates.</li>
                </ul>
              </div>
            )}

            <div className="mb-4 p-3 rounded-xl bg-[#f4ece1]/70 border border-[#8b4513]/10 text-xs font-hand text-[#5d4037]/80 flex items-center justify-between">
              <span>
                {pendingCount == null ? 'Checking queue…' : `${pendingCount} PR${pendingCount === 1 ? '' : 's'} waiting`}
              </span>
              <span>
                {policyCount == null ? 'Loading policies…' : `${policyCount} active polic${policyCount === 1 ? 'y' : 'ies'}`}
              </span>
            </div>

            {error && (
              <p className="mb-3 text-xs font-hand text-rose-700 bg-rose-50 border border-rose-200 px-3 py-2 rounded-lg">
                ⚠ {error}
              </p>
            )}

            <div className="flex gap-2">
              <button
                onClick={confirm}
                disabled={busy}
                className={`flex-1 px-4 py-2.5 rounded-xl font-marker text-sm border-b-4 transition-all active:scale-95 ${
                  target
                    ? 'bg-emerald-600 hover:bg-emerald-700 text-emerald-50 border-emerald-800 shadow-md'
                    : 'bg-[#5d4037] hover:bg-[#4e342e] text-[#efebe9] border-[#3e2723] shadow-md'
                } disabled:opacity-60`}
              >
                {busy ? 'Saving…' : target ? 'Enable AutoMode' : 'Disable AutoMode'}
              </button>
              <button
                onClick={() => !busy && setModalOpen(false)}
                disabled={busy}
                className="px-4 py-2.5 rounded-xl font-marker text-sm bg-[#f4ece1] text-[#8b4513]/70 border border-[#8b4513]/20 hover:bg-[#8b4513]/10 transition-colors disabled:opacity-50"
              >
                Cancel
              </button>
            </div>
          </div>
        </div>
      )}
    </>
  )
}
