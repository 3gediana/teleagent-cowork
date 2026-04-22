import { useEffect, useState } from 'react'
import { useAppStore } from '../stores/appStore'
import { projectApi } from '../api/endpoints'

/**
 * WorkspacePickerPage — the pre-selection state rendered INSIDE the
 * app shell.  Replaces the old full-screen LoginPanel which was just a
 * project list dressed up as a login gate; the project switcher now
 * lives on the same dark canvas as the rest of the app, keeping visual
 * continuity from first-contact onwards.
 *
 * Visual: medium-density grid of workspace cards with subtle hover
 * lift, an "open" state on click that transitions into the normal
 * Overview view.  The last card is a "Create workspace" CTA.
 */

type Project = {
  id: string
  name: string
  description?: string
  direction?: string | null
  milestone?: string | null
  version?: string | null
  agents_count?: number
  tasks_count?: number
}

function avatarGradient(id: string): string {
  const palette = [
    'linear-gradient(135deg, #6366f1, #4338ca)',
    'linear-gradient(135deg, #10b981, #059669)',
    'linear-gradient(135deg, #f59e0b, #d97706)',
    'linear-gradient(135deg, #ec4899, #be185d)',
    'linear-gradient(135deg, #06b6d4, #0e7490)',
    'linear-gradient(135deg, #a855f7, #7e22ce)',
  ]
  let h = 0
  for (let i = 0; i < id.length; i++) h = ((h << 5) - h) + id.charCodeAt(i) | 0
  return palette[Math.abs(h) % palette.length]
}

function IconArrow() {
  return (
    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <line x1="5" y1="12" x2="19" y2="12"/>
      <polyline points="12 5 19 12 12 19"/>
    </svg>
  )
}

function IconPlus() {
  return (
    <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round">
      <path d="M12 5v14M5 12h14"/>
    </svg>
  )
}

export default function WorkspacePickerPage() {
  const setSelectedProjectId = useAppStore((s) => s.setSelectedProjectId)
  const [projects, setProjects] = useState<Project[]>([])
  const [loading, setLoading] = useState(true)
  const [hoverId, setHoverId] = useState<string | null>(null)

  useEffect(() => {
    projectApi.list().then((res) => {
      if (res.success) setProjects((res.data as Project[]) || [])
      setLoading(false)
    }).catch(() => setLoading(false))
  }, [])

  const select = (id: string) => {
    setSelectedProjectId(id)
  }

  return (
    <div className="min-h-full flex flex-col">
      {/* hero strip */}
      <div className="px-10 pt-10 pb-6 border-b border-[#1e1e22] relative overflow-hidden">
        {/* subtle ambient gradient */}
        <div className="absolute top-0 left-1/3 w-[600px] h-[200px] pointer-events-none"
             style={{ background: 'radial-gradient(ellipse, rgba(99,102,241,0.1), transparent 70%)', filter: 'blur(60px)' }} />

        <div className="relative">
          <div className="flex items-center gap-2 mb-2">
            <span className="chip chip-blue font-mono-jb text-[10.5px]">
              <span className="status-dot" style={{ width: 5, height: 5 }} />
              operator signed in
            </span>
          </div>
          <h1 className="text-[26px] font-semibold tracking-tight text-white leading-tight">
            Choose a workspace
          </h1>
          <p className="text-[13px] mt-1.5 max-w-[560px]" style={{ color: 'var(--text-1)' }}>
            Every workspace has its own agents, tasks, and policies. Pick one to open Mission Control,
            or create a new workspace to bootstrap a fresh project.
          </p>
        </div>
      </div>

      {/* grid */}
      <div className="flex-1 px-10 py-8">
        {loading ? (
          <GridSkeleton />
        ) : (
          <>
            {projects.length === 0 ? (
              <EmptyState />
            ) : (
              <>
                <div className="flex items-center justify-between mb-4">
                  <div className="text-[11px] font-semibold uppercase tracking-[0.08em]" style={{ color: 'var(--text-2)' }}>
                    {projects.length} {projects.length === 1 ? 'workspace' : 'workspaces'}
                  </div>
                </div>
                <div className="grid grid-cols-[repeat(auto-fill,minmax(280px,1fr))] gap-3">
                  {projects.map((p) => {
                    const active = hoverId === p.id
                    return (
                      <button
                        key={p.id}
                        onClick={() => select(p.id)}
                        onMouseEnter={() => setHoverId(p.id)}
                        onMouseLeave={() => setHoverId(null)}
                        className="surface-1 p-4 text-left group transition-all relative"
                        style={{
                          borderColor: active ? 'var(--accent)' : undefined,
                          transform: active ? 'translateY(-2px)' : undefined,
                          boxShadow: active
                            ? '0 8px 24px -8px rgba(99,102,241,0.4), 0 0 0 1px rgba(99,102,241,0.3) inset'
                            : undefined,
                        }}
                      >
                        {/* top row: avatar + arrow */}
                        <div className="flex items-start justify-between mb-3">
                          <div
                            className="w-9 h-9 rounded-[10px] flex items-center justify-center text-[14px] font-bold text-white shrink-0"
                            style={{ background: avatarGradient(p.id), boxShadow: '0 2px 8px rgba(0,0,0,0.3), 0 0 0 1px rgba(255,255,255,0.08) inset' }}
                          >
                            {(p.name || 'P').slice(0, 1).toUpperCase()}
                          </div>
                          <span
                            className="opacity-0 group-hover:opacity-100 transition-opacity"
                            style={{ color: 'var(--accent)' }}
                          >
                            <IconArrow />
                          </span>
                        </div>

                        {/* name */}
                        <div className="text-[14px] font-semibold tracking-tight text-white truncate" title={p.name}>
                          {p.name}
                        </div>

                        {/* description */}
                        <div
                          className="mt-1 text-[12px] leading-snug line-clamp-2 min-h-[32px]"
                          style={{ color: 'var(--text-1)' }}
                        >
                          {p.description || (
                            <span className="italic" style={{ color: 'var(--text-2)' }}>
                              No description
                            </span>
                          )}
                        </div>

                        {/* meta row */}
                        <div className="mt-3 pt-3 border-t border-[#27272a] flex items-center gap-3 text-[10.5px] font-mono-jb" style={{ color: 'var(--text-2)' }}>
                          {p.version && (
                            <span className="flex items-center gap-1">
                              <svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><path d="M3 12h18"/><path d="M12 3v18"/></svg>
                              {p.version}
                            </span>
                          )}
                          {p.milestone && (
                            <span className="flex items-center gap-1 truncate" title={p.milestone}>
                              <svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><path d="M4 4v17"/><path d="M4 4h13l-3 5 3 5H4"/></svg>
                              <span className="truncate">{p.milestone}</span>
                            </span>
                          )}
                          <span className="ml-auto font-mono-jb text-[10px] opacity-70">{p.id.slice(0, 6)}</span>
                        </div>
                      </button>
                    )
                  })}

                  {/* Create new card */}
                  <button
                    className="relative rounded-[10px] border border-dashed p-4 text-left transition-all group flex flex-col items-center justify-center min-h-[148px]"
                    style={{ borderColor: 'var(--border)', color: 'var(--text-2)' }}
                    onMouseEnter={(e) => { e.currentTarget.style.borderColor = 'var(--accent)'; e.currentTarget.style.color = 'var(--accent)' }}
                    onMouseLeave={(e) => { e.currentTarget.style.borderColor = 'var(--border)'; e.currentTarget.style.color = 'var(--text-2)' }}
                  >
                    <span className="w-10 h-10 rounded-full flex items-center justify-center mb-2" style={{ background: 'rgba(99,102,241,0.08)', border: '1px solid rgba(99,102,241,0.2)' }}>
                      <IconPlus />
                    </span>
                    <span className="text-[13px] font-medium">Create workspace</span>
                    <span className="text-[11px] mt-0.5" style={{ color: 'var(--text-2)' }}>
                      Start a new project
                    </span>
                  </button>
                </div>
              </>
            )}
          </>
        )}
      </div>

      {/* footer hint */}
      <div className="px-10 py-4 border-t border-[#1e1e22] flex items-center justify-between text-[11.5px]" style={{ color: 'var(--text-2)' }}>
        <span>Tip: press <kbd className="px-1.5 py-0.5 mx-0.5 rounded bg-white/5 text-[10px] font-mono-jb border border-[#2a2a2e]">↑↓</kbd> to navigate, <kbd className="px-1.5 py-0.5 mx-0.5 rounded bg-white/5 text-[10px] font-mono-jb border border-[#2a2a2e]">Enter</kbd> to open</span>
        <a href="#" className="hover:text-white transition-colors">Docs</a>
      </div>
    </div>
  )
}

function GridSkeleton() {
  return (
    <div className="grid grid-cols-[repeat(auto-fill,minmax(280px,1fr))] gap-3">
      {Array.from({ length: 6 }).map((_, i) => (
        <div key={i} className="surface-1 p-4 animate-pulse">
          <div className="flex items-start justify-between mb-3">
            <div className="w-9 h-9 rounded-[10px] bg-white/5" />
          </div>
          <div className="h-4 bg-white/5 rounded mb-2 w-3/4" />
          <div className="h-3 bg-white/5 rounded mb-1 w-full" />
          <div className="h-3 bg-white/5 rounded w-2/3" />
        </div>
      ))}
    </div>
  )
}

function EmptyState() {
  return (
    <div className="flex flex-col items-center justify-center py-20 text-center">
      <div className="w-14 h-14 rounded-full flex items-center justify-center mb-4"
           style={{ background: 'rgba(99,102,241,0.08)', border: '1px solid rgba(99,102,241,0.2)' }}>
        <svg width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="#a5b4fc" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round">
          <rect x="3" y="3" width="18" height="18" rx="2"/>
          <path d="M3 9h18"/>
          <path d="M9 21V9"/>
        </svg>
      </div>
      <h3 className="text-[14px] font-semibold text-white tracking-tight mb-1">No workspaces yet</h3>
      <p className="text-[12.5px] max-w-[360px]" style={{ color: 'var(--text-1)' }}>
        Create your first workspace to start coordinating agents, tasks, and reviews from one place.
      </p>
      <button className="mt-4 inline-flex items-center gap-1.5 px-3.5 py-2 rounded-md text-[12.5px] font-medium transition-all"
              style={{ background: 'linear-gradient(135deg, #6366f1, #4f46e5)', color: 'white', boxShadow: '0 4px 12px -4px rgba(99,102,241,0.5)' }}>
        <IconPlus />
        Create workspace
      </button>
    </div>
  )
}
