import { useState } from 'react'

interface ModalProps {
  title: string
  icon?: string
  children: React.ReactNode
  onClose: () => void
}

export function Modal({ title, icon, children, onClose }: ModalProps) {
  return (
    <div className="fixed inset-0 bg-slate-900/40 backdrop-blur-sm flex items-center justify-center z-50 transition-opacity" onClick={onClose}>
      <div
        className="bg-white rounded-2xl shadow-2xl border border-slate-200 w-[28rem] max-h-[80vh] overflow-hidden flex flex-col"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center justify-between px-6 py-4 border-b border-slate-100 bg-slate-50 shrink-0">
          <h3 className="text-lg font-bold text-slate-800 flex items-center gap-2">
            {icon && <span className="text-xl">{icon}</span>}
            {title}
          </h3>
          <button onClick={onClose} className="text-slate-400 hover:text-slate-600 hover:bg-slate-200 transition-colors text-xl w-8 h-8 rounded-full flex items-center justify-center">
            ×
          </button>
        </div>
        <div className="p-6 overflow-y-auto custom-scrollbar">{children}</div>
      </div>
    </div>
  )
}

export function Input({ label, ...props }: { label?: string } & React.InputHTMLAttributes<HTMLInputElement>) {
  return (
    <div className="mb-4">
      {label && <label className="block text-xs font-bold text-slate-600 mb-1.5 uppercase tracking-wider">{label}</label>}
      <input
        className="w-full bg-white border border-slate-300 rounded-lg px-3 py-2.5 text-sm text-slate-800 placeholder-slate-400 shadow-sm focus:ring-2 focus:ring-blue-500 focus:border-blue-500 outline-none transition-shadow"
        {...props}
      />
    </div>
  )
}

export function Textarea({ label, ...props }: { label?: string } & React.TextareaHTMLAttributes<HTMLTextAreaElement>) {
  return (
    <div className="mb-4">
      {label && <label className="block text-xs font-bold text-slate-600 mb-1.5 uppercase tracking-wider">{label}</label>}
      <textarea
        className="w-full bg-white border border-slate-300 rounded-lg px-3 py-2.5 text-sm text-slate-800 placeholder-slate-400 shadow-sm focus:ring-2 focus:ring-blue-500 focus:border-blue-500 outline-none transition-shadow resize-y min-h-[80px]"
        {...props}
      />
    </div>
  )
}

export function Select({ label, options, ...props }: { label?: string; options: { value: string; label: string }[] } & React.SelectHTMLAttributes<HTMLSelectElement>) {
  return (
    <div className="mb-4">
      {label && <label className="block text-xs font-bold text-slate-600 mb-1.5 uppercase tracking-wider">{label}</label>}
      <select
        className="w-full bg-white border border-slate-300 rounded-lg px-3 py-2.5 text-sm font-medium text-slate-700 shadow-sm focus:ring-2 focus:ring-blue-500 focus:border-blue-500 outline-none transition-shadow cursor-pointer"
        {...props}
      >
        {options.map((o) => (
          <option key={o.value} value={o.value}>{o.label}</option>
        ))}
      </select>
    </div>
  )
}

export function Button({ variant = 'primary', children, className = '', ...props }: { variant?: 'primary' | 'secondary' | 'danger', className?: string } & React.ButtonHTMLAttributes<HTMLButtonElement>) {
  const base = 'px-4 py-2.5 rounded-lg text-sm font-bold transition-all disabled:opacity-50 active:scale-95 shadow-sm'
  const variants = {
    primary: 'bg-blue-600 hover:bg-blue-700 text-white',
    secondary: 'bg-white hover:bg-slate-50 text-slate-700 border border-slate-300',
    danger: 'bg-rose-600 hover:bg-rose-700 text-white',
  }
  return <button className={`${base} ${variants[variant]} ${className}`} {...props}>{children}</button>
}

export function SuccessResult({ title, data, onClose }: { title: string; data: Record<string, any>; onClose: () => void }) {
  return (
    <div className="text-center py-2">
      <div className="text-5xl mb-4">✅</div>
      <h3 className="text-lg font-extrabold text-emerald-600 mb-5">{title}</h3>
      <div className="text-left bg-slate-50 border border-slate-100 rounded-xl p-5 mb-6 space-y-3 shadow-inner">
        {Object.entries(data).map(([k, v]) => (
          <div key={k} className="text-sm flex flex-col gap-1">
            <span className="text-slate-500 font-bold uppercase tracking-wider text-xs">{k}</span>
            <span className="text-slate-800 font-mono bg-white p-2 rounded border border-slate-200 break-all shadow-sm">{String(v)}</span>
          </div>
        ))}
      </div>
      <Button onClick={onClose} className="w-full">Done</Button>
    </div>
  )
}

export function useModal() {
  const [open, setOpen] = useState(false)
  return { open, openModal: () => setOpen(true), closeModal: () => setOpen(false) }
}
