import { useEffect, type ReactNode } from 'react'
import { Link, NavLink } from 'react-router-dom'
import { formatPct } from '../lib/money'

export function Notice({ kind, children }: { kind?: 'ok' | 'bad' | 'warn' | 'info'; children: ReactNode }) {
  return <div className={`notice ${kind ?? ''}`}>{children}</div>
}

export function Badge({ status, kind }: { status: string | null | undefined; kind?: 'ok' | 'warn' | 'bad' | 'info' }) {
  const s = (status ?? '').toLowerCase()
  const cls =
    kind ??
    (s === 'active' || s === 'verified' || s === 'issued' || s === 'live' || s === 'ok'
      ? 'ok'
      : s === 'pending' || s === 'draft' || s === 'warning' || s === 'stopped' || s === 'medium'
        ? 'warn'
        : s === 'failed' || s === 'suspended' || s === 'exceeded' || s === 'deleted' || s === 'high'
          ? 'bad'
          : '')
  return <span className={`badge ${cls}`}>{status || '—'}</span>
}

export function Field({ label, error, help, children }: { label: string; error?: string; help?: string; children: ReactNode }) {
  return (
    <div className="field">
      <label>{label}</label>
      {children}
      {help && !error ? <div className="help">{help}</div> : null}
      {error ? <div className="err">{error}</div> : null}
    </div>
  )
}

export function Tabs({
  base,
  tabs,
  current,
  counts,
}: {
  base: string
  tabs: string[]
  current: string
  counts?: Record<string, number | undefined>
}) {
  return (
    <nav className="tabs">
      {tabs.map((t) => {
        const key = t.toLowerCase()
        const n = counts?.[key]
        return (
          <NavLink key={t} to={`${base}?tab=${key}`} className={current === key ? 'active' : ''}>
            {t}
            {typeof n === 'number' ? <span className="count">{n}</span> : null}
          </NavLink>
        )
      })}
    </nav>
  )
}

export function Empty({ children }: { children: ReactNode }) {
  return <p className="muted">{children}</p>
}

/** A stated absence: what is missing and, when known, why / what to do. */
export function EmptyState({ title, children }: { title: string; children?: ReactNode }) {
  return (
    <div className="empty">
      <b>{title}</b>
      {children}
    </div>
  )
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

/** Page title row with optional breadcrumbs, subtitle and right-hand actions. */
export function PageHeader({
  title,
  sub,
  crumbs,
  actions,
}: {
  title: ReactNode
  sub?: ReactNode
  crumbs?: Array<{ to?: string; label: string }>
  actions?: ReactNode
}) {
  return (
    <div className="page-head">
      <div>
        {crumbs && crumbs.length ? (
          <div className="crumbs">
            {crumbs.map((c, i) => (
              <span key={i}>
                {i > 0 ? ' / ' : ''}
                {c.to ? <Link to={c.to}>{c.label}</Link> : c.label}
              </span>
            ))}
          </div>
        ) : null}
        <h1>{title}</h1>
        {sub ? <div className="sub">{sub}</div> : null}
      </div>
      {actions ? <div className="actions">{actions}</div> : null}
    </div>
  )
}

/** ▲ +5.8 % / ▼ −3.1 % chip. Cost going UP is red; null renders "—". */
export function Delta({ pct, invert }: { pct: number | null | undefined; invert?: boolean }) {
  if (pct === null || pct === undefined || !Number.isFinite(pct)) return <span className="delta flat">—</span>
  const up = pct > 0.05
  const down = pct < -0.05
  const cls = up ? (invert ? 'down' : 'up') : down ? (invert ? 'up' : 'down') : 'flat'
  return (
    <span className={`delta ${cls}`} title="change versus the previous period of the same length">
      {up ? '▲' : down ? '▼' : '•'} {formatPct(pct, { sign: true })}
    </span>
  )
}

/** KPI card: label, big value, optional unit and footnote/delta. */
export function KPI({
  label,
  value,
  unit,
  note,
  tone,
  hint,
}: {
  label: ReactNode
  value: ReactNode
  unit?: string
  note?: ReactNode
  tone?: 'ok' | 'warn' | 'bad'
  hint?: string
}) {
  return (
    <div className={`kpi ${tone ?? ''}`} title={hint}>
      <div className="k">
        <span>{label}</span>
      </div>
      <div className="v">
        {value}
        {unit ? <span className="unit">{unit}</span> : null}
      </div>
      {note ? <div className="d">{note}</div> : null}
    </div>
  )
}

/** Modal dialog; closes on Escape and backdrop click. */
export function Modal({
  title,
  onClose,
  children,
  wide,
  footer,
}: {
  title: ReactNode
  onClose: () => void
  children: ReactNode
  wide?: boolean
  footer?: ReactNode
}) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])
  return (
    <div className="modal-back" onMouseDown={(e) => e.target === e.currentTarget && onClose()} role="presentation">
      <div className={`modal ${wide ? 'wide' : ''}`} role="dialog" aria-modal="true">
        <h2>{title}</h2>
        {children}
        {footer ? <div className="foot">{footer}</div> : null}
      </div>
    </div>
  )
}

/** Yes/no confirmation with the consequence spelled out. */
export function Confirm({
  title,
  body,
  confirmLabel,
  danger,
  onConfirm,
  onClose,
  busy,
}: {
  title: string
  body: ReactNode
  confirmLabel?: string
  danger?: boolean
  onConfirm: () => void | Promise<void>
  onClose: () => void
  busy?: boolean
}) {
  return (
    <Modal
      title={title}
      onClose={onClose}
      footer={
        <>
          <button onClick={onClose} disabled={busy}>
            Cancel
          </button>
          <button className={danger ? 'danger' : 'primary'} onClick={() => void onConfirm()} disabled={busy}>
            {confirmLabel ?? 'Confirm'}
          </button>
        </>
      }
    >
      <div>{body}</div>
    </Modal>
  )
}

/** A horizontal share bar for table cells (0..1). */
export function ShareBar({ share, width = 72 }: { share: number; width?: number }) {
  const pct = Math.max(0, Math.min(1, share || 0)) * 100
  return (
    <span className="share-bar" style={{ width }} aria-hidden>
      <i style={{ width: `${pct}%` }} />
    </span>
  )
}

/** Segmented control. */
export function Segmented<T extends string>({
  value,
  options,
  onChange,
  ariaLabel,
}: {
  value: T
  options: ReadonlyArray<{ value: T; label: string }>
  onChange: (v: T) => void
  ariaLabel?: string
}) {
  return (
    <div className="seg" role="group" aria-label={ariaLabel}>
      {options.map((o) => (
        <button key={o.value} type="button" className={o.value === value ? 'on' : ''} onClick={() => onChange(o.value)} aria-pressed={o.value === value}>
          {o.label}
        </button>
      ))}
    </div>
  )
}

export function Skeleton({ lines = 3 }: { lines?: number }) {
  return (
    <div className="stack tight" aria-busy>
      {Array.from({ length: lines }, (_, i) => (
        <div key={i} className="skeleton" style={{ width: `${90 - i * 12}%` }} />
      ))}
    </div>
  )
}
