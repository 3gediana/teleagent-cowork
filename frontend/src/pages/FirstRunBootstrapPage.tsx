import { useEffect, useState } from 'react'
import { useAppStore } from '../stores/appStore'
import { authApi } from '../api/endpoints'

/**
 * FirstRunBootstrapPage — shown whenever the dashboard boots without a
 * cached access_key.  Before the auth tightening in
 * "auth: guard /project/* + record creator" this screen didn't exist
 * because /project/list was world-readable, so an empty DB still
 * rendered the workspace picker.  After the fix the picker requests
 * would 401 and the user hit a dead end.
 *
 * The panel covers two cases:
 *   1. Very first boot on an empty DB → offer to register the operator
 *      as the first human.  The server allows is_human=true
 *      unconditionally while no human exists yet.
 *   2. Subsequent visits from a fresh browser (new laptop, incognito,
 *      cleared storage) → paste the access_key handed out during
 *      bootstrap.
 *
 * On success we write the key into localStorage via the app store;
 * the axios interceptor in api/client.ts picks it up on the next
 * request, and App.tsx re-renders into the workspace picker.
 */

type Mode = 'choose' | 'register' | 'paste'

export default function FirstRunBootstrapPage() {
  const setAccessKey = useAppStore((s) => s.setAccessKey)
  const [mode, setMode] = useState<Mode>('choose')
  const [name, setName] = useState('operator')
  const [key, setKey] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [issuedKey, setIssuedKey] = useState<string | null>(null)

  // If the user hits register and the DB already has a human, the
  // server returns HUMAN_APPROVAL_REQUIRED and we steer them to the
  // paste mode instead with a friendlier explanation.
  useEffect(() => { setError(null) }, [mode])

  const register = async () => {
    setBusy(true)
    setError(null)
    try {
      const res = await authApi.register(name.trim() || 'operator', undefined, true)
      if (!res.success) throw new Error('unexpected response')
      const k = (res.data as any)?.access_key
      if (!k) throw new Error('server returned no access_key')
      setIssuedKey(k)
    } catch (e: any) {
      const code = e?.response?.data?.error?.code
      if (code === 'HUMAN_APPROVAL_REQUIRED') {
        setMode('paste')
        setError('Bootstrap is already done on this backend. Paste an existing human\u2019s access key instead.')
      } else if (code === 'AGENT_NAME_TAKEN') {
        setError('That name is already registered \u2014 pick another.')
      } else {
        setError(e?.response?.data?.error?.message || e?.message || 'register failed')
      }
    } finally {
      setBusy(false)
    }
  }

  const confirmIssued = () => {
    if (!issuedKey) return
    setAccessKey(issuedKey)
    // No reload needed; App.tsx reacts to accessKey in the store.
  }

  const paste = async () => {
    const trimmed = key.trim()
    if (!trimmed) { setError('Access key is required'); return }
    setBusy(true)
    setError(null)
    try {
      // /auth/login also validates the key server-side, so we don't
      // accept random strings into localStorage and wedge the app.
      const res = await authApi.login(trimmed)
      if (!res.success) throw new Error('invalid key')
      setAccessKey(trimmed)
    } catch (e: any) {
      setError(e?.response?.data?.error?.message || e?.message || 'login failed')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="min-h-screen flex items-center justify-center px-6 py-10" style={{ background: 'radial-gradient(ellipse at top, rgba(99,102,241,0.08), transparent 60%), var(--surface-0, #09090b)' }}>
      <div className="w-full max-w-[460px] surface-1 p-7">
        <div className="flex items-center gap-2 mb-4">
          <span className="chip chip-blue font-mono-jb text-[10.5px]">
            <span className="status-dot" style={{ width: 5, height: 5 }} />
            first-run bootstrap
          </span>
        </div>
        <h1 className="text-[22px] font-semibold tracking-tight text-white leading-tight">
          Sign in to A3C
        </h1>
        <p className="text-[13px] mt-1.5" style={{ color: 'var(--text-1)' }}>
          No access key found in this browser. Register as the first operator, or paste a key you already have.
        </p>

        {mode === 'choose' && (
          <div className="mt-6 grid gap-2.5">
            <button
              onClick={() => setMode('register')}
              className="px-4 py-2.5 rounded-md text-[13px] font-medium text-white transition-all"
              style={{ background: 'linear-gradient(135deg, #6366f1, #4f46e5)', boxShadow: '0 4px 12px -4px rgba(99,102,241,0.5)' }}
            >
              Register as first operator
            </button>
            <button
              onClick={() => setMode('paste')}
              className="px-4 py-2.5 rounded-md text-[13px] font-medium transition-all"
              style={{ background: 'rgba(255,255,255,0.04)', border: '1px solid var(--border, #27272a)', color: 'var(--text-0)' }}
            >
              I already have an access key
            </button>
          </div>
        )}

        {mode === 'register' && !issuedKey && (
          <div className="mt-6 grid gap-3">
            <label className="grid gap-1.5">
              <span className="text-[11.5px] font-semibold uppercase tracking-wider" style={{ color: 'var(--text-2)' }}>Operator name</span>
              <input
                value={name}
                onChange={(e) => setName(e.target.value)}
                disabled={busy}
                className="px-3 py-2 rounded-md text-[13px] font-mono-jb text-white outline-none"
                style={{ background: 'rgba(255,255,255,0.04)', border: '1px solid var(--border, #27272a)' }}
              />
            </label>
            {error && <div className="text-[12px]" style={{ color: '#fda4af' }}>{error}</div>}
            <div className="flex gap-2">
              <button onClick={() => setMode('choose')} disabled={busy} className="px-3 py-2 rounded-md text-[12.5px]" style={{ color: 'var(--text-1)' }}>Back</button>
              <button
                onClick={register}
                disabled={busy}
                className="flex-1 px-4 py-2 rounded-md text-[13px] font-medium text-white transition-all"
                style={{ background: 'linear-gradient(135deg, #6366f1, #4f46e5)', opacity: busy ? 0.6 : 1 }}
              >
                {busy ? 'Registering\u2026' : 'Register'}
              </button>
            </div>
          </div>
        )}

        {mode === 'register' && issuedKey && (
          <div className="mt-6 grid gap-3">
            <div className="text-[12px]" style={{ color: 'var(--text-1)' }}>
              Copy and save this access key somewhere safe. You will need it to sign in from other browsers.
            </div>
            <div className="px-3 py-2.5 rounded-md font-mono-jb text-[12.5px] text-white break-all" style={{ background: 'rgba(255,255,255,0.04)', border: '1px solid var(--border, #27272a)' }}>
              {issuedKey}
            </div>
            <button
              onClick={confirmIssued}
              className="px-4 py-2 rounded-md text-[13px] font-medium text-white"
              style={{ background: 'linear-gradient(135deg, #6366f1, #4f46e5)', boxShadow: '0 4px 12px -4px rgba(99,102,241,0.5)' }}
            >
              Saved it \u2014 continue
            </button>
          </div>
        )}

        {mode === 'paste' && (
          <div className="mt-6 grid gap-3">
            <label className="grid gap-1.5">
              <span className="text-[11.5px] font-semibold uppercase tracking-wider" style={{ color: 'var(--text-2)' }}>Access key</span>
              <input
                value={key}
                onChange={(e) => setKey(e.target.value)}
                disabled={busy}
                placeholder="paste the access_key returned from /agent/register"
                className="px-3 py-2 rounded-md text-[13px] font-mono-jb text-white outline-none"
                style={{ background: 'rgba(255,255,255,0.04)', border: '1px solid var(--border, #27272a)' }}
              />
            </label>
            {error && <div className="text-[12px]" style={{ color: '#fda4af' }}>{error}</div>}
            <div className="flex gap-2">
              <button onClick={() => setMode('choose')} disabled={busy} className="px-3 py-2 rounded-md text-[12.5px]" style={{ color: 'var(--text-1)' }}>Back</button>
              <button
                onClick={paste}
                disabled={busy}
                className="flex-1 px-4 py-2 rounded-md text-[13px] font-medium text-white transition-all"
                style={{ background: 'linear-gradient(135deg, #6366f1, #4f46e5)', opacity: busy ? 0.6 : 1 }}
              >
                {busy ? 'Verifying\u2026' : 'Sign in'}
              </button>
            </div>
          </div>
        )}

        <div className="mt-6 pt-5 border-t border-[#1e1e22] text-[11.5px] leading-relaxed" style={{ color: 'var(--text-2)' }}>
          You can always retrieve the key by hitting <code className="font-mono-jb">POST /api/v1/agent/register</code> directly with <code className="font-mono-jb">is_human=true</code>. The server accepts that only while no human exists yet.
        </div>
      </div>
    </div>
  )
}
