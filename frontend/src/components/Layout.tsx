import { Sidebar } from './Sidebar'
import { useAppStore } from '../stores/appStore'
import { AutoModeSwitch } from './AutoModeSwitch'

export function Layout({ children }: { children: React.ReactNode }) {
  const { project } = useAppStore()

  return (
    <div className="flex h-screen overflow-hidden">
      <Sidebar />
      <main className="flex-1 flex flex-col min-w-0">
        <header className="parchment px-8 py-3 flex items-center justify-between border-b border-[#8b4513]/20 shrink-0 z-30">
          <div className="flex items-center gap-3">
             <span className="text-[#8b4513]/30 text-xl font-thin">/</span>
             <span className="font-marker text-lg text-[#5d4037]">{project?.name || 'Loading...'}</span>
          </div>
          <div className="flex items-center gap-3">
             <AutoModeSwitch />
             <div className="flex items-center gap-2 px-3 py-1.5 bg-[#8b4513]/10 border border-[#8b4513]/20 rounded-lg shadow-inner">
                <span className="w-2.5 h-2.5 rounded-full bg-emerald-600 animate-pulse shadow-[0_0_8px_rgba(16,185,129,0.5)]" />
                <span className="text-xs font-bold text-[#5d4037] uppercase tracking-widest">{project?.agents.length || 0} Agents Online</span>
             </div>
          </div>
        </header>
        <div className="flex-1 overflow-y-auto p-8 custom-scrollbar">
          {children}
        </div>
      </main>
    </div>
  )
}
