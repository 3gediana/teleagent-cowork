import { useEffect, useMemo, useState } from 'react'
import { useAppStore } from '../stores/appStore'
import { tagApi } from '../api/endpoints'

/**
 * TagReviewPage
 * =============
 *
 * Human reviewer's workspace for the Tag lifecycle (PR 6):
 *   - Surfaces every proposed TaskTag across the current project's tasks
 *   - Groups by parent task so the reviewer can scan one task at a time
 *   - Confirm / Reject actions flip state via /tag/*
 *
 * Pending tags are the friction point — without human judgement the
 * rule engine's guesses would either rot uncounted or (worse) bias the
 * injection selector with noise. This page is the fastest way to get
 * that judgement in.
 */

type TagRow = {
  id: string
  task_id: string
  tag: string
  dimension: string
  status: string
  source: string
  confidence: number
  evidence?: string
  reviewed_by?: string
  reviewed_at?: string | null
  created_at: string
}

type TaskLite = {
  id: string
  name: string
  status: string
}

type GroupedTask = {
  task: TaskLite
  tags: TagRow[]
}

export default function TagReviewPage() {
  const project = useAppStore((s) => s.project)
  const [groups, setGroups] = useState<GroupedTask[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string>('')
  const [pendingTagId, setPendingTagId] = useState<string>('') // "in-flight" spinner target

  const load = async () => {
    if (!project) return
    setLoading(true)
    setError('')
    try {
      // The server has no "list all tags by project" endpoint yet —
      // we fan out one /tag/list call per task. That's O(N_tasks)
      // requests which is fine at current volumes (<500 tasks). When
      // this page becomes the bottleneck, add a batch endpoint on the
      // server side; for now, keeping the API surface minimal.
      const results = await Promise.all(
        project.tasks.map(async (t: any) => {
          try {
            const res = await tagApi.list(t.id, 'proposed')
            return { task: t, tags: (res?.data?.tags || []) as TagRow[] }
          } catch {
            return { task: t, tags: [] as TagRow[] }
          }
        }),
      )
      // Only show tasks that actually have pending tags — the
      // reviewer doesn't want to scroll past empty cards.
      setGroups(results.filter((g) => g.tags.length > 0))
    } catch (e: any) {
      setError(e?.message || 'Failed to load tags')
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [project?.id])

  const totalPending = useMemo(
    () => groups.reduce((acc, g) => acc + g.tags.length, 0),
    [groups],
  )

  const handleAction = async (tagId: string, action: 'confirm' | 'reject') => {
    setPendingTagId(tagId)
    try {
      if (action === 'confirm') await tagApi.confirm(tagId)
      else await tagApi.reject(tagId)
      await load()
    } catch (e: any) {
      setError(e?.response?.data?.error?.message || e?.message || 'Action failed')
    } finally {
      setPendingTagId('')
    }
  }

  if (!project) {
    return <div className="p-8 font-type text-slate-600">Select a project first.</div>
  }

  return (
    <div className="h-full flex flex-col space-y-6">
      <div className="flex items-center justify-between shrink-0">
        <div>
          <h1 className="text-2xl font-extrabold text-slate-800">Tag Review</h1>
          <p className="text-slate-500 text-sm font-medium mt-1">
            Confirm or reject auto-proposed tags. Each decision shapes how the
            refinery injects experience hints into future claims.
          </p>
        </div>
        <div className="flex items-center gap-3">
          <span className="text-xs font-bold text-slate-500 uppercase tracking-widest">
            {totalPending} pending
          </span>
          <button
            onClick={load}
            className="font-marker text-xs bg-slate-800 text-white px-4 py-2 rounded-lg shadow hover:bg-slate-700 transition"
          >
            ↻ Refresh
          </button>
        </div>
      </div>

      {error && (
        <div className="rounded-xl bg-rose-100 border border-rose-300 text-rose-800 px-4 py-2 text-sm">
          {error}
        </div>
      )}

      {loading && groups.length === 0 && (
        <div className="font-type text-slate-500">Loading…</div>
      )}

      {!loading && groups.length === 0 && !error && (
        <div className="rounded-2xl bg-white/80 border border-slate-200 p-10 text-center">
          <div className="text-4xl mb-3">🎉</div>
          <div className="font-marker text-xl text-slate-700">Queue empty</div>
          <p className="text-sm text-slate-500 mt-2">
            No proposed tags waiting for review. Create new tasks or re-run
            the tag rules to populate this page.
          </p>
        </div>
      )}

      <div className="flex-1 overflow-y-auto space-y-4 pb-10">
        {groups.map((g) => (
          <TaskCard
            key={g.task.id}
            task={g.task}
            tags={g.tags}
            pendingTagId={pendingTagId}
            onAction={handleAction}
          />
        ))}
      </div>
    </div>
  )
}

function TaskCard({
  task,
  tags,
  pendingTagId,
  onAction,
}: {
  task: TaskLite
  tags: TagRow[]
  pendingTagId: string
  onAction: (tagId: string, action: 'confirm' | 'reject') => void
}) {
  return (
    <div className="bg-white/90 rounded-2xl border border-slate-200 shadow-sm overflow-hidden">
      <div className="px-5 py-3 border-b border-slate-100 bg-slate-50/80 flex items-center justify-between">
        <div>
          <div className="font-marker text-base text-slate-800">{task.name}</div>
          <div className="text-[10px] font-bold text-slate-400 uppercase tracking-wider mt-0.5">
            #{task.id.slice(0, 10)} · {task.status}
          </div>
        </div>
        <div className="text-[10px] text-slate-500 font-bold">
          {tags.length} tag{tags.length === 1 ? '' : 's'} pending
        </div>
      </div>
      <div className="divide-y divide-slate-100">
        {tags.map((t) => (
          <TagRowItem
            key={t.id}
            row={t}
            disabled={pendingTagId === t.id}
            onAction={(action) => onAction(t.id, action)}
          />
        ))}
      </div>
    </div>
  )
}

function TagRowItem({
  row,
  disabled,
  onAction,
}: {
  row: TagRow
  disabled: boolean
  onAction: (action: 'confirm' | 'reject') => void
}) {
  const evidenceSummary = useMemo(() => summariseEvidence(row.evidence), [row.evidence])
  return (
    <div className="px-5 py-3 flex items-center gap-4">
      <div className="flex-1 min-w-0">
        <div className="flex items-center gap-2">
          <span className="font-mono text-xs bg-slate-100 text-slate-600 px-2 py-0.5 rounded">
            {row.dimension}
          </span>
          <span className="font-bold text-slate-800">{row.tag}</span>
          <span className="text-[10px] text-slate-400">
            conf {(row.confidence * 100).toFixed(0)}% · source {row.source}
          </span>
        </div>
        {evidenceSummary && (
          <div className="text-[11px] text-slate-500 mt-1 truncate">
            {evidenceSummary}
          </div>
        )}
      </div>
      <div className="flex items-center gap-2 shrink-0">
        <button
          onClick={() => onAction('confirm')}
          disabled={disabled}
          className="text-xs font-bold px-3 py-1 rounded-lg bg-emerald-600 text-white hover:bg-emerald-700 disabled:opacity-50 transition"
        >
          Confirm
        </button>
        <button
          onClick={() => onAction('reject')}
          disabled={disabled}
          className="text-xs font-bold px-3 py-1 rounded-lg bg-rose-600 text-white hover:bg-rose-700 disabled:opacity-50 transition"
        >
          Reject
        </button>
      </div>
    </div>
  )
}

// summariseEvidence renders the Evidence JSON blob as a short human
// line. Falls back gracefully when the payload is unexpected, so a
// malformed evidence field can never break the row's render.
function summariseEvidence(raw?: string): string {
  if (!raw) return ''
  try {
    const obj = JSON.parse(raw) as {
      matched_keywords?: string[]
      qualify_keywords?: string[]
      rule_id?: string
    }
    const parts: string[] = []
    if (obj.rule_id) parts.push(`rule=${obj.rule_id}`)
    if (obj.matched_keywords?.length) parts.push(`match=[${obj.matched_keywords.join(',')}]`)
    return parts.join(' · ')
  } catch {
    return raw.length > 120 ? raw.slice(0, 120) + '…' : raw
  }
}
