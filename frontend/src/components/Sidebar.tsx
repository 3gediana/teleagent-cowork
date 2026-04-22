import { Link, useLocation } from 'react-router-dom'
import { useAppStore } from '../stores/appStore'

const navItems = [
  { path: '/', label: 'Overview', icon: '🏠' },
  { path: '/tasks', label: 'Tasks', icon: '📋' },
  { path: '/submissions', label: 'Submissions', icon: '🚀' },
  { path: '/prs', label: 'PRs', icon: '🔀' },
  { path: '/chief', label: 'Chief', icon: '🤖' },
  { path: '/pool', label: 'Agent Pool', icon: '🏡' },
  { path: '/knowledge', label: 'Knowledge', icon: '🧠' },
  { path: '/tags', label: 'Tag Review', icon: '🏷️' },
  { path: '/activity', label: 'Activity', icon: '📊' },
  { path: '/llm', label: 'LLM Endpoints', icon: '🔌' },
  { path: '/settings', label: 'Settings', icon: '⚙️' },
]

export function Sidebar() {
  const { project, sidebarCollapsed, toggleSidebar } = useAppStore()
  const location = useLocation()

  return (
    <aside
      className={`parchment border-r border-[#8b4513]/20 transition-all duration-300 flex flex-col shrink-0 z-40 ${
        sidebarCollapsed ? 'w-20' : 'w-64'
      }`}
    >
      <div className="p-6 border-b border-[#8b4513]/10 flex items-center justify-between">
        {!sidebarCollapsed && (
          <h1 className="text-3xl font-marker text-[#5d4037] tracking-tight drop-shadow-sm -rotate-2">A3C</h1>
        )}
        <button
          onClick={toggleSidebar}
          className="p-1.5 rounded-lg hover:bg-[#8b4513]/10 text-[#8b4513]/50 transition-colors"
        >
          {sidebarCollapsed ? '➡️' : '⬅️'}
        </button>
      </div>

      <nav className="flex-1 p-4 space-y-2">
        {navItems.map((item) => (
          <Link
            key={item.path}
            to={item.path}
            className={`flex items-center gap-3 px-4 py-3 rounded-xl transition-all ${
              location.pathname === item.path
                ? 'bg-[#8b4513] text-[#f4ece1] font-bold shadow-md scale-105'
                : 'text-[#5d4037]/70 hover:bg-[#8b4513]/10 hover:text-[#5d4037] font-medium'
            }`}
          >
            <span className="text-xl">{item.icon}</span>
            {!sidebarCollapsed && <span className="font-type text-sm">{item.label}</span>}
          </Link>
        ))}
      </nav>

      {!sidebarCollapsed && project && (
        <div className="p-5 m-4 bg-[#5d4037] rounded-2xl border border-black/20 shadow-xl relative overflow-hidden group">
          <div className="absolute inset-0 opacity-10 bg-[url('https://www.transparenttextures.com/patterns/leather.png')] pointer-events-none" />
          <p className="text-[9px] font-bold text-[#efebe9]/50 uppercase tracking-[0.2em] mb-2 relative">Active Project</p>
          <p className="text-sm font-marker text-[#efebe9] truncate relative">{project.name}</p>
          <div className="flex items-center gap-2 mt-3 relative">
            <span className="w-2 h-2 rounded-full bg-emerald-500 shadow-[0_0_8px_rgba(16,185,129,0.5)]" />
            <span className="text-[10px] font-bold text-[#efebe9]/60 font-mono tracking-tighter">VER {project.version}</span>
          </div>
        </div>
      )}
    </aside>
  )
}
