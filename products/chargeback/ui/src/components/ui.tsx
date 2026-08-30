import { type ReactNode } from 'react'
import { NavLink } from 'react-router-dom'

export function Notice({ kind, children }: { kind?: 'ok' | 'bad' | 'warn'; children: ReactNode }) {
  return <div className={`notice ${kind ?? ''}`}>{children}</div>
}

export function Badge({ status }: { status: string | null | undefined }) {
  const s = (status ?? '').toLowerCase()
  const cls =
    s === 'active' || s === 'verified' || s === 'issued'
      ? 'ok'
      : s === 'pending' || s === 'draft'
        ? 'warn'
        : s === 'failed' || s === 'suspended'
          ? 'bad'
          : ''
  return <span className={`badge ${cls}`}>{status || '—'}</span>
}

export function Field({
  label,
  error,
  children,
}: {
  label: string
  error?: string
  children: ReactNode
}) {
  return (
    <div className="field">
      <label>{label}</label>
      {children}
      {error ? <div className="err">{error}</div> : null}
    </div>
  )
}

export function Tabs({ base, tabs, current }: { base: string; tabs: string[]; current: string }) {
  return (
    <nav className="tabs">
      {tabs.map((t) => (
        <NavLink key={t} to={`${base}?tab=${t.toLowerCase()}`} className={current === t.toLowerCase() ? 'active' : ''}>
          {t}
        </NavLink>
      ))}
    </nav>
  )
}

export function Empty({ children }: { children: ReactNode }) {
  return <p className="muted">{children}</p>
}

export function Steps({ steps, at }: { steps: string[]; at: number }) {
  return (
    <div className="steps">
      {steps.map((s, i) => (
        <span key={s} className={i < at ? 'done' : i === at ? 'on' : ''}>
          {i + 1}. {s}
        </span>
      ))}
    </div>
  )
}

export function Details({ value }: { value: unknown }) {
  if (value === null || value === undefined || value === '') return <span className="muted">—</span>
  const text = typeof value === 'string' ? value : JSON.stringify(value)
  return <pre className="details">{text}</pre>
}
