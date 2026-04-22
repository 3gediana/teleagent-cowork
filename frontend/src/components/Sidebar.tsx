import { Link, useLocation } from 'react-router-dom'
import { useAppStore } from '../stores/appStore'

/**
 * Sidebar — Linear-inspired dark navigation rail.
 *
 * Layout: brand block → project switcher → grouped nav (Workspace /
 * Fleet / System) → user footer. Icons are inline SVG (no emoji) to
 * keep the typographic rhythm tight on Windows/macOS/Linux alike.
 *
 * `sidebarCollapsed` still works — collapses to 64px, hides labels,
 * keeps icons centered. Active route receives a 2px inset stripe on
 * the left plus a soft indigo glow, matching Linear.
 */

type IconName =
  | 'overview' | 'tasks' | 'submissions' | 'prs' | 'chief'
  | 'pool' | 'knowledge' | 'tags' | 'activity' | 'llm' | 'settings'
  | 'chevron' | 'more'

function Icon({ name, size = 14 }: { name: IconName; size?: number }) {
  const s = { width: size, height: size, viewBox: '0 0 24 24', fill: 'none', stroke: 'currentColor', strokeWidth: 1.8, strokeLinecap: 'round' as const, strokeLinejoin: 'round' as const }
  switch (name) {
    case 'overview':
      return (<svg {...s}><rect x="3" y="3" width="7" height="9" rx="1"/><rect x="14" y="3" width="7" height="5" rx="1"/><rect x="14" y="12" width="7" height="9" rx="1"/><rect x="3" y="16" width="7" height="5" rx="1"/></svg>)
    case 'tasks':
      return (<svg {...s}><path d="M9 11l3 3L22 4"/><path d="M21 12v7a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11"/></svg>)
    case 'submissions':
      return (<svg {...s}><path d="m13 2-9 10h7l-1 10 9-10h-7z"/></svg>)
    case 'prs':
      return (<svg {...s}><circle cx="6" cy="6" r="2.5"/><circle cx="6" cy="18" r="2.5"/><circle cx="18" cy="18" r="2.5"/><path d="M6 8.5v7"/><path d="M18 8.5V13a3 3 0 0 1-3 3H8.5"/></svg>)
    case 'chief':
      return (<svg {...s}><path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z"/></svg>)
    case 'pool':
      return (<svg {...s}><circle cx="12" cy="12" r="9"/><path d="M12 3v18M3 12h18"/></svg>)
    case 'knowledge':
      return (<svg {...s}><path d="M21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z"/><path d="M3.3 7 12 12l8.7-5"/><path d="M12 22V12"/></svg>)
    case 'tags':
      return (<svg {...s}><path d="M20.59 13.41 13.42 20.58a2 2 0 0 1-2.83 0L2 12V2h10l8.59 8.59a2 2 0 0 1 0 2.82z"/><circle cx="7" cy="7" r="1.5" fill="currentColor"/></svg>)
    case 'activity':
      return (<svg {...s}><path d="M22 12h-4l-3 9L9 3l-3 9H2"/></svg>)
    case 'llm':
      return (<svg {...s}><path d="M20 7h-9"/><path d="M14 17H5"/><circle cx="17" cy="17" r="3"/><circle cx="7" cy="7" r="3"/></svg>)
    case 'settings':
      return (<svg {...s}><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 0 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 1 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 1 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 1 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 1 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z"/></svg>)
    case 'chevron':
      return (<svg {...s}><polyline points="8 9 12 5 16 9"/><polyline points="16 15 12 19 8 15"/></svg>)
    case 'more':
      return (<svg {...s}><circle cx="12" cy="12" r="1"/><circle cx="19" cy="12" r="1"/><circle cx="5" cy="12" r="1"/></svg>)
  }
}

type NavItem = { path: string; label: string; icon: IconName; badge?: string; badgeTone?: 'mute' | 'green' }
type NavGroup = { title: string; items: NavItem[] }

const navGroups: NavGroup[] = [
  {
    title: 'Workspace',
    items: [
      { path: '/',             label: 'Overview',    icon: 'overview' },
      { path: '/tasks',        label: 'Tasks',       icon: 'tasks' },
      { path: '/submissions',  label: 'Submissions', icon: 'submissions' },
      { path: '/prs',          label: 'Pull Requests', icon: 'prs' },
      { path: '/chief',        label: 'Chief Chat',  icon: 'chief' },
    ],
  },
  {
    title: 'Fleet',
    items: [
      { path: '/pool',      label: 'Agent Pool',    icon: 'pool' },
      { path: '/llm',       label: 'LLM Endpoints', icon: 'llm' },
      { path: '/knowledge', label: 'Knowledge',     icon: 'knowledge' },
      { path: '/tags',      label: 'Tag Review',    icon: 'tags' },
    ],
  },
  {
    title: 'System',
    items: [
      { path: '/activity', label: 'Activity', icon: 'activity' },
      { path: '/settings', label: 'Settings', icon: 'settings' },
    ],
  },
]

export function Sidebar() {
  const { project, sidebarCollapsed, toggleSidebar } = useAppStore()
  const location = useLocation()

  const collapsed = sidebarCollapsed
  const width = collapsed ? 'w-16' : 'w-60'

  return (
    <aside className={`${width} bg-[#0d0d0f] border-r border-[#1e1e22] flex flex-col shrink-0 relative transition-all duration-200 z-40`}>
      {/* soft top glow */}
      <div className="absolute inset-x-0 top-0 h-40 pointer-events-none" style={{ background: 'radial-gradient(ellipse 80% 40% at 50% 0%, rgba(99,102,241,0.08), transparent 60%)' }} />

      {/* brand */}
      <div className={`relative z-10 ${collapsed ? 'px-2 py-5' : 'px-5 pt-5 pb-4'} flex items-center ${collapsed ? 'justify-center' : 'justify-between'}`}>
        <div className="flex items-center gap-2.5 min-w-0">
          <div className="w-7 h-7 shrink-0 rounded-[8px] bg-gradient-to-br from-[#7b82e6] to-[#4338ca] flex items-center justify-center shadow-lg shadow-indigo-900/40 ring-1 ring-white/10">
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="white" strokeWidth="2.5"><path d="M6 3 L6 21 M6 12 L18 12 M18 3 L18 21"/></svg>
          </div>
          {!collapsed && (
            <div className="leading-tight min-w-0">
              <div className="text-[13px] font-semibold text-white tracking-tight truncate">A3C Studio</div>
              <div className="text-[11px] text-[#71717a]">Mission Control</div>
            </div>
          )}
        </div>
        {!collapsed && (
          <button
            onClick={toggleSidebar}
            className="text-[#71717a] hover:text-white transition-colors p-1 rounded-md hover:bg-white/5"
            title="Collapse sidebar"
          >
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round"><polyline points="15 18 9 12 15 6"/></svg>
          </button>
        )}
      </div>

      {collapsed && (
        <button
          onClick={toggleSidebar}
          className="mx-auto mb-2 text-[#71717a] hover:text-white transition-colors p-1 rounded-md hover:bg-white/5"
          title="Expand sidebar"
        >
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round"><polyline points="9 18 15 12 9 6"/></svg>
        </button>
      )}

      {/* project switcher — shows picker CTA when no project selected */}
      {!collapsed && (
        <div className="px-3 pb-3 relative z-10">
          {project ? (
            <button className="w-full flex items-center gap-2 px-2.5 py-1.5 rounded-md hover:bg-white/5 transition-colors text-left">
              <div className="w-5 h-5 shrink-0 rounded bg-gradient-to-br from-amber-400 to-amber-600 flex items-center justify-center text-[10px] font-bold text-amber-950 shadow">
                {(project.name?.[0] || 'P').toUpperCase()}
              </div>
              <div className="flex-1 min-w-0">
                <div className="text-[12.5px] font-medium text-[#e8e8ea] truncate">{project.name}</div>
              </div>
              <span className="text-[#3f3f46]"><Icon name="chevron" /></span>
            </button>
          ) : (
            <div
              className="w-full flex items-center gap-2 px-2.5 py-1.5 rounded-md"
              style={{ background: 'rgba(99,102,241,0.08)', border: '1px dashed rgba(99,102,241,0.28)' }}
            >
              <div className="w-5 h-5 shrink-0 rounded flex items-center justify-center text-[10px] font-bold"
                   style={{ background: 'rgba(99,102,241,0.2)', color: '#c7c9f4' }}>
                ?
              </div>
              <div className="flex-1 min-w-0">
                <div className="text-[12px] font-medium truncate" style={{ color: '#c7c9f4' }}>
                  Select a workspace
                </div>
              </div>
            </div>
          )}
        </div>
      )}

      {/* nav — hidden when no project selected to prevent dead clicks */}
      {project && (
        <nav className="sidebar-nav flex-1 px-3 pb-3 space-y-0.5 overflow-y-auto">
          {navGroups.map((group) => (
            <div key={group.title}>
              {!collapsed && (
                <div className="px-2 pt-3 pb-1.5 text-[11px] font-medium text-[#71717a] uppercase tracking-wider">{group.title}</div>
              )}
              {collapsed && <div className="h-3" aria-hidden />}
              {group.items.map((item) => {
                const active = location.pathname === item.path
                return (
                  <Link
                    key={item.path}
                    to={item.path}
                    className={active ? 'active' : ''}
                    title={collapsed ? item.label : undefined}
                    style={collapsed ? { justifyContent: 'center', padding: '8px 0' } : undefined}
                  >
                    <Icon name={item.icon} />
                    {!collapsed && <span>{item.label}</span>}
                  </Link>
                )
              })}
            </div>
          ))}
        </nav>
      )}
      {!project && <div className="flex-1" />}

      {/* footer */}
      {!collapsed ? (
        <div className="px-3 py-3 border-t border-[#1e1e22]">
          <div className="flex items-center gap-2.5">
            <div className="avatar" style={{ width: 28, height: 28, fontSize: 11, background: 'linear-gradient(135deg, #ec4899, #f43f5e)' }}>OP</div>
            <div className="flex-1 min-w-0 leading-tight">
              <div className="text-[12px] text-white truncate font-medium">operator</div>
              <div className="text-[11px] text-[#71717a] truncate">Human operator</div>
            </div>
            <button className="text-[#71717a] hover:text-white p-1 rounded-md hover:bg-white/5">
              <Icon name="more" />
            </button>
          </div>
        </div>
      ) : (
        <div className="py-3 flex items-center justify-center border-t border-[#1e1e22]">
          <div className="avatar" style={{ width: 28, height: 28, fontSize: 11, background: 'linear-gradient(135deg, #ec4899, #f43f5e)' }}>OP</div>
        </div>
      )}
    </aside>
  )
}
