import { useEffect, useMemo, useState } from 'react'
import { llmApi } from '../api/endpoints'

/**
 * LLMEndpointsPage
 * ================
 *
 * Human operator's workspace for the user-registered LLM endpoint
 * catalogue (PR 10 — opencode replacement). Each row represents one
 * `llm_endpoint` DB entry; the backend wires it into the live
 * Registry on create/update so new endpoints are usable by agents
 * without a server restart.
 *
 * Visual model: one card per endpoint, inline expandable editor.
 * Avoids a separate modal because endpoints are a short list (usually
 * 1–5 entries) and side-by-side diffing between endpoints is common.
 */

type ModelInfo = {
  id: string
  name?: string
  context_window?: number
  max_output_tokens?: number
  supports_tools?: boolean
  supports_vision?: boolean
  supports_reasoning?: boolean
  input_price_per_mtok?: number
  output_price_per_mtok?: number
}

type Endpoint = {
  id: string
  name: string
  format: 'openai' | 'anthropic'
  base_url: string
  api_key_redacted: string
  api_key_set: boolean
  models: ModelInfo[]
  default_model: string
  status: 'active' | 'disabled'
  registered: boolean
  created_by: string
  created_at: string
  updated_at: string
}

export default function LLMEndpointsPage() {
  const [endpoints, setEndpoints] = useState<Endpoint[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string>('')
  const [creating, setCreating] = useState(false)
  const [editingID, setEditingID] = useState<string>('')

  const load = async () => {
    setLoading(true)
    setError('')
    try {
      const res = await llmApi.list()
      setEndpoints(res.data?.endpoints || [])
    } catch (e: any) {
      setError(e?.response?.data?.error?.message || e?.message || 'Failed to load')
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => { load() }, [])

  return (
    <div className="h-full flex flex-col space-y-6">
      <div className="flex items-center justify-between shrink-0">
        <div>
          <h1 className="text-2xl font-extrabold text-slate-800">LLM Endpoints</h1>
          <p className="text-slate-500 text-sm font-medium mt-1">
            Register any endpoint that speaks the OpenAI <code>/chat/completions</code> or
            Anthropic <code>/v1/messages</code> protocol. Assigned to agents via role overrides.
          </p>
        </div>
        <div className="flex items-center gap-3">
          <button
            onClick={load}
            className="font-marker text-xs bg-slate-200 text-slate-700 px-4 py-2 rounded-lg shadow hover:bg-slate-300 transition"
          >
            ↻ Refresh
          </button>
          <button
            onClick={() => { setCreating(true); setEditingID('') }}
            className="font-marker text-xs bg-emerald-600 text-white px-4 py-2 rounded-lg shadow hover:bg-emerald-700 transition"
          >
            + Register endpoint
          </button>
        </div>
      </div>

      {error && (
        <div className="rounded-xl bg-rose-100 border border-rose-300 text-rose-800 px-4 py-2 text-sm">
          {error}
        </div>
      )}

      {creating && (
        <EndpointEditor
          mode="create"
          initial={blankEndpoint()}
          onCancel={() => setCreating(false)}
          onSaved={async () => { setCreating(false); await load() }}
          onError={setError}
        />
      )}

      {loading && endpoints.length === 0 && (
        <div className="font-type text-slate-500">Loading…</div>
      )}

      {!loading && endpoints.length === 0 && !creating && !error && (
        <div className="rounded-2xl bg-white/80 border border-slate-200 p-10 text-center">
          <div className="text-4xl mb-3">🔌</div>
          <div className="font-marker text-xl text-slate-700">No endpoints yet</div>
          <p className="text-sm text-slate-500 mt-2">
            Click <b>Register endpoint</b> to add one. You'll need a URL
            (or blank for the provider's default), an API key, and at least one model ID.
          </p>
        </div>
      )}

      <div className="flex-1 overflow-y-auto space-y-4 pb-10">
        {endpoints.map((e) => (
          <EndpointCard
            key={e.id}
            ep={e}
            isEditing={editingID === e.id}
            onEdit={() => setEditingID(e.id)}
            onCancel={() => setEditingID('')}
            onSaved={async () => { setEditingID(''); await load() }}
            onDeleted={async () => { setEditingID(''); await load() }}
            onError={setError}
          />
        ))}
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------
// Card

function EndpointCard({
  ep, isEditing, onEdit, onCancel, onSaved, onDeleted, onError,
}: {
  ep: Endpoint
  isEditing: boolean
  onEdit: () => void
  onCancel: () => void
  onSaved: () => Promise<void>
  onDeleted: () => Promise<void>
  onError: (m: string) => void
}) {
  const [testing, setTesting] = useState(false)
  const [testResult, setTestResult] = useState<string>('')

  const handleTest = async () => {
    setTesting(true)
    setTestResult('')
    try {
      const res = await llmApi.test(ep.id)
      setTestResult(`✓ ${res.data?.usage?.input_tokens ?? '?'} in / ${res.data?.usage?.output_tokens ?? '?'} out · ${res.data?.model}`)
    } catch (e: any) {
      setTestResult(`✗ ${e?.response?.data?.error?.message || e?.message || 'unknown error'}`)
    } finally {
      setTesting(false)
    }
  }

  const handleDelete = async () => {
    const label = ep.status === 'disabled' ? 'permanently delete' : 'disable'
    if (!confirm(`Really ${label} endpoint "${ep.name}"?`)) return
    try {
      await llmApi.del(ep.id)
      await onDeleted()
    } catch (e: any) {
      onError(e?.response?.data?.error?.message || e?.message || 'Delete failed')
    }
  }

  return (
    <div className={`bg-white/90 rounded-2xl border shadow-sm overflow-hidden ${
      ep.status === 'disabled' ? 'border-slate-200 opacity-60' : 'border-slate-200'
    }`}>
      <div className="px-5 py-3 border-b border-slate-100 bg-slate-50/80 flex items-center justify-between gap-4">
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2 flex-wrap">
            <span className="font-marker text-base text-slate-800">{ep.name}</span>
            <FormatBadge format={ep.format} />
            <StatusBadge ep={ep} />
          </div>
          <div className="text-[11px] text-slate-500 mt-0.5 truncate font-mono">
            {ep.base_url || <span className="italic text-slate-400">default URL for {ep.format}</span>}
          </div>
        </div>
        <div className="flex items-center gap-2 shrink-0">
          <button
            onClick={handleTest}
            disabled={testing || !ep.registered}
            className="text-xs font-bold px-3 py-1 rounded-lg bg-blue-600 text-white hover:bg-blue-700 disabled:opacity-50 transition"
            title={!ep.registered ? 'Endpoint not loaded into runtime registry' : 'Dispatch a 1-token probe'}
          >
            {testing ? '…' : 'Test'}
          </button>
          <button
            onClick={isEditing ? onCancel : onEdit}
            className="text-xs font-bold px-3 py-1 rounded-lg bg-slate-700 text-white hover:bg-slate-800 transition"
          >
            {isEditing ? 'Close' : 'Edit'}
          </button>
          <button
            onClick={handleDelete}
            className="text-xs font-bold px-3 py-1 rounded-lg bg-rose-600 text-white hover:bg-rose-700 transition"
            title={ep.status === 'disabled' ? 'Hard delete (row is already disabled)' : 'Soft delete (disable, keep audit row)'}
          >
            {ep.status === 'disabled' ? 'Delete' : 'Disable'}
          </button>
        </div>
      </div>

      <div className="px-5 py-3 flex flex-wrap gap-6 text-xs text-slate-600">
        <div>
          <span className="font-bold text-slate-500 uppercase tracking-wider mr-1">Key</span>
          <span className="font-mono">{ep.api_key_redacted || '(empty)'}</span>
        </div>
        <div>
          <span className="font-bold text-slate-500 uppercase tracking-wider mr-1">Models</span>
          {ep.models.length === 0
            ? <span className="italic text-slate-400">none listed</span>
            : <span className="font-mono">{ep.models.map(m => m.id).join(', ')}</span>}
        </div>
        {ep.default_model && (
          <div>
            <span className="font-bold text-slate-500 uppercase tracking-wider mr-1">Default</span>
            <span className="font-mono">{ep.default_model}</span>
          </div>
        )}
      </div>

      {testResult && (
        <div className={`px-5 py-2 text-xs font-mono border-t ${
          testResult.startsWith('✓')
            ? 'bg-emerald-50 border-emerald-200 text-emerald-800'
            : 'bg-rose-50 border-rose-200 text-rose-800'
        }`}>
          {testResult}
        </div>
      )}

      {isEditing && (
        <EndpointEditor
          mode="edit"
          initial={ep}
          onCancel={onCancel}
          onSaved={onSaved}
          onError={onError}
        />
      )}
    </div>
  )
}

// ---------------------------------------------------------------------
// Editor

function blankEndpoint(): Endpoint {
  return {
    id: '',
    name: '',
    format: 'openai',
    base_url: '',
    api_key_redacted: '',
    api_key_set: false,
    models: [],
    default_model: '',
    status: 'active',
    registered: false,
    created_by: '',
    created_at: '',
    updated_at: '',
  }
}

function EndpointEditor({
  mode, initial, onCancel, onSaved, onError,
}: {
  mode: 'create' | 'edit'
  initial: Endpoint
  onCancel: () => void
  onSaved: () => Promise<void>
  onError: (m: string) => void
}) {
  const [name, setName] = useState(initial.name)
  const [format, setFormat] = useState<'openai' | 'anthropic'>(initial.format)
  const [baseURL, setBaseURL] = useState(initial.base_url)
  const [apiKey, setApiKey] = useState('')
  const [defaultModel, setDefaultModel] = useState(initial.default_model)
  const [modelsText, setModelsText] = useState(
    initial.models.map(m => m.id).join('\n'),
  )
  const [saving, setSaving] = useState(false)

  const parsedModels = useMemo(
    () => modelsText
      .split('\n')
      .map(s => s.trim())
      .filter(Boolean)
      .map(id => ({ id })),
    [modelsText],
  )

  const handleSave = async () => {
    setSaving(true)
    try {
      if (mode === 'create') {
        await llmApi.create({
          name,
          format,
          base_url: baseURL,
          api_key: apiKey,
          models: parsedModels,
          default_model: defaultModel,
        })
      } else {
        // api_key blank on edit = "keep existing"; backend handles.
        await llmApi.update(initial.id, {
          name: name !== initial.name ? name : undefined,
          format: format !== initial.format ? format : undefined,
          base_url: baseURL,
          api_key: apiKey || undefined,
          models: parsedModels,
          default_model: defaultModel,
        })
      }
      await onSaved()
    } catch (e: any) {
      onError(e?.response?.data?.error?.message || e?.message || 'Save failed')
    } finally {
      setSaving(false)
    }
  }

  const formatHints: Record<string, string> = {
    openai:
      'Any endpoint using OpenAI\'s /chat/completions schema. Leave blank for api.openai.com, or paste a /v1 root like https://api.minimaxi.com/v1 , https://api.deepseek.com/v1 , https://openrouter.ai/api/v1 , http://localhost:11434/v1 (Ollama), etc.',
    anthropic:
      'Native Anthropic Messages API. Leave blank for api.anthropic.com, or paste a proxy URL ending at /v1 (AWS Bedrock proxy, self-hosted Litellm, etc.).',
  }

  return (
    <div className="border-t border-slate-200 bg-slate-50/80 px-5 py-5 space-y-4">
      <div className="grid grid-cols-2 gap-4">
        <LabeledInput
          label="Name"
          value={name}
          onChange={setName}
          placeholder="e.g. MiniMax prod"
        />
        <LabeledSelect
          label="Format"
          value={format}
          onChange={(v) => setFormat(v as 'openai' | 'anthropic')}
          options={[
            { value: 'openai', label: 'OpenAI-compatible' },
            { value: 'anthropic', label: 'Anthropic Messages API' },
          ]}
        />
      </div>
      <div>
        <LabeledInput
          label="Base URL"
          value={baseURL}
          onChange={setBaseURL}
          placeholder={format === 'openai' ? 'https://api.minimaxi.com/v1' : 'https://api.anthropic.com/v1'}
        />
        <p className="text-[11px] text-slate-500 mt-1 leading-snug">{formatHints[format]}</p>
      </div>
      <LabeledInput
        label={mode === 'edit' ? `API Key (leave blank to keep ${initial.api_key_redacted || 'existing'})` : 'API Key'}
        value={apiKey}
        onChange={setApiKey}
        placeholder={mode === 'edit' ? '(unchanged)' : 'sk-...'}
        type="password"
      />
      <div>
        <label className="block text-[11px] font-bold text-slate-500 uppercase tracking-wider mb-1">
          Models <span className="text-slate-400 font-normal">(one per line, model ID only)</span>
        </label>
        <textarea
          value={modelsText}
          onChange={(e) => setModelsText(e.target.value)}
          rows={Math.max(3, parsedModels.length + 1)}
          className="w-full px-3 py-2 text-sm font-mono bg-white border border-slate-300 rounded-lg focus:border-blue-500 focus:ring-1 focus:ring-blue-500 outline-none"
          placeholder={format === 'anthropic'
            ? 'claude-opus-4-5-20251015\nclaude-sonnet-4-5-20250929'
            : 'MiniMax-M2.7\ngpt-4o-mini'}
        />
        <p className="text-[11px] text-slate-500 mt-1 leading-snug">
          Use the provider's exact model id. Pricing + capability hints auto-fill from the
          built-in catalogue when known; otherwise the model still works, just without cost stats.
        </p>
      </div>
      {parsedModels.length > 1 && (
        <LabeledSelect
          label="Default model (used when a role override specifies this endpoint but no model)"
          value={defaultModel}
          onChange={setDefaultModel}
          options={[
            { value: '', label: '(none — caller must always specify)' },
            ...parsedModels.map(m => ({ value: m.id, label: m.id })),
          ]}
        />
      )}
      {parsedModels.length === 1 && parsedModels[0].id !== defaultModel && (
        <p className="text-[11px] text-slate-500 italic">
          Single model — will be used as the default automatically.
        </p>
      )}
      <div className="flex justify-end gap-2 pt-2">
        <button
          onClick={onCancel}
          disabled={saving}
          className="font-bold text-xs px-4 py-2 rounded-lg bg-slate-200 text-slate-700 hover:bg-slate-300 disabled:opacity-50 transition"
        >
          Cancel
        </button>
        <button
          onClick={handleSave}
          disabled={saving || !name || (mode === 'create' && !apiKey)}
          className="font-bold text-xs px-4 py-2 rounded-lg bg-emerald-600 text-white hover:bg-emerald-700 disabled:opacity-50 transition"
        >
          {saving ? 'Saving…' : mode === 'create' ? 'Register' : 'Save changes'}
        </button>
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------
// Small UI helpers

function LabeledInput({
  label, value, onChange, placeholder, type = 'text',
}: {
  label: string
  value: string
  onChange: (v: string) => void
  placeholder?: string
  type?: string
}) {
  return (
    <div>
      <label className="block text-[11px] font-bold text-slate-500 uppercase tracking-wider mb-1">
        {label}
      </label>
      <input
        type={type}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
        className="w-full px-3 py-2 text-sm bg-white border border-slate-300 rounded-lg focus:border-blue-500 focus:ring-1 focus:ring-blue-500 outline-none"
      />
    </div>
  )
}

function LabeledSelect({
  label, value, onChange, options,
}: {
  label: string
  value: string
  onChange: (v: string) => void
  options: { value: string; label: string }[]
}) {
  return (
    <div>
      <label className="block text-[11px] font-bold text-slate-500 uppercase tracking-wider mb-1">
        {label}
      </label>
      <select
        value={value}
        onChange={(e) => onChange(e.target.value)}
        className="w-full px-3 py-2 text-sm bg-white border border-slate-300 rounded-lg focus:border-blue-500 focus:ring-1 focus:ring-blue-500 outline-none"
      >
        {options.map(o => (
          <option key={o.value} value={o.value}>{o.label}</option>
        ))}
      </select>
    </div>
  )
}

function FormatBadge({ format }: { format: string }) {
  const cls = format === 'anthropic'
    ? 'bg-amber-100 text-amber-800 border-amber-300'
    : 'bg-sky-100 text-sky-800 border-sky-300'
  return (
    <span className={`text-[10px] font-bold uppercase tracking-wider border rounded px-1.5 py-0.5 ${cls}`}>
      {format}
    </span>
  )
}

function StatusBadge({ ep }: { ep: Endpoint }) {
  if (ep.status === 'disabled') {
    return <span className="text-[10px] font-bold uppercase tracking-wider border rounded px-1.5 py-0.5 bg-slate-100 text-slate-600 border-slate-300">disabled</span>
  }
  if (!ep.registered) {
    return <span className="text-[10px] font-bold uppercase tracking-wider border rounded px-1.5 py-0.5 bg-rose-100 text-rose-800 border-rose-300">not loaded</span>
  }
  return <span className="text-[10px] font-bold uppercase tracking-wider border rounded px-1.5 py-0.5 bg-emerald-100 text-emerald-800 border-emerald-300">live</span>
}
