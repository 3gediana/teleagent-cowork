import { Sidebar } from './Sidebar'
import { useAppStore } from '../stores/appStore'
import { AutoModeSwitch } from './AutoModeSwitch'

/**
 * Layout — top-level shell with sidebar + header + content region.
 *
 * Header layout (left → right):
 *   workspace ▸ project ▸ view   ·   chips (milestone, version)
 *   ──────────────────────────────────────────────────────────────
 *   QuickFind · AutoMode · online count · notifications
 *
 * The content region is left un-padded so pages can decide their own
 * paddings — Overview wants a full-bleed felt board, while list pages
 * want a comfortable 32px inset.
 */
export function Layout({ children }: { children: React.ReactNode }) {
  const { project } = useAppStore()
  const agentsOnline = project?.agents.length ?? 0

  return (
    <div className="flex h-screen overflow-hidden bg-[#09090b]">
      <Sidebar />

      <main className="flex-1 flex flex-col min-w-0 relative">
        {/* ambient glow behind the content — subtle, not gaudy */}
        <div className="pointer-events-none absolute top-0 right-[15%] w-[600px] h-[400px]" style={{ background: 'radial-gradient(ellipse, rgba(99,102,241,0.07), transparent 70%)', filter: 'blur(80px)' }} />
        <div className="pointer-events-none absolute bottom-0 left-[5%] w-[500px] h-[350px]" style={{ background: 'radial-gradient(ellipse, rgba(34,211,238,0.04), transparent 70%)', filter: 'blur(80px)' }} />

        <header className="relative z-20 px-6 h-14 flex items-center justify-between border-b border-[#1e1e22] bg-[#09090b]/80 backdrop-blur-xl">
          {/* breadcrumb — degrades to a neutral label when no workspace is picked */}
          <div className="flex items-center gap-2 text-[13px] min-w-0">
            {project ? (
              <>
                <span className="text-[#71717a]">Workspace</span>
                <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" className="text-[#3f3f46] shrink-0">
                  <polyline points="9 18 15 12 9 6" />
                </svg>
                <span className="text-white font-medium truncate max-w-[200px]" title={project.name}>
                  {project.name}
                </span>
                {project.milestone && (
                  <span className="mx-1 chip chip-blue font-mono-jb text-[11px]" title="Current milestone">
                    <svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.2"><path d="M4 4v17"/><path d="M4 4h13l-3 5 3 5H4"/></svg>
                    {project.milestone}
                  </span>
                )}
                {project.version && (
                  <span className="chip font-mono-jb text-[11px]" title="Current version">{project.version}</span>
                )}
              </>
            ) : (
              <span className="text-[#71717a]">Mission Control</span>
            )}
          </div>

          <div className="flex items-center gap-2">
            {/* quick find */}
            <button className="chip gap-2 pl-2.5 pr-1.5 hidden md:inline-flex">
              <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><circle cx="11" cy="11" r="8"/><path d="m21 21-4.3-4.3"/></svg>
              <span>Quick find…</span>
              <kbd className="ml-2 px-1.5 py-0.5 rounded bg-white/5 text-[10px] font-mono-jb border border-[#2a2a2e]">⌘K</kbd>
            </button>

            {/* auto mode + online agents — hidden when no workspace */}
            {project && (
              <>
                <AutoModeSwitch />
                <div className="chip chip-green">
                  <span className="status-dot" />
                  <span>{agentsOnline} {agentsOnline === 1 ? 'agent' : 'agents'} online</span>
                </div>
              </>
            )}

            {/* notifications placeholder */}
            <button className="chip relative gap-1.5 px-2" title="Notifications">
              <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><path d="M6 8a6 6 0 0 1 12 0c0 7 3 9 3 9H3s3-2 3-9"/><path d="M10.3 21a1.94 1.94 0 0 0 3.4 0"/></svg>
            </button>
          </div>
        </header>

        <div className="flex-1 overflow-y-auto custom-scrollbar relative z-10">
          {children}
        </div>
      </main>
    </div>
  )
}
