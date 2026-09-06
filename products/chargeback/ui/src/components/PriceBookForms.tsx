import { useState, type FormEvent } from 'react'
import { api, errorText } from '../api/client'
import type { PriceBook } from '../api/types'
import { Confirm, Field, Modal, Notice } from './ui'

// Price-book settings, clone and delete dialogs (DESIGN.md §2.5), shared by
// the list page and the detail page so the two never drift.

/** How a stopped instance is billed — the book's `bill_stopped` policy. */
export const BILL_STOPPED_OPTIONS: ReadonlyArray<{ value: string; label: string }> = [
  { value: 'compute', label: 'Compute — billed as if running' },
  { value: 'storage-only', label: 'Storage only — disks billed, compute not' },
  { value: 'none', label: 'None — nothing billed while stopped' },
]

export function billStoppedLabel(v: string | null | undefined): string {
  switch (v) {
    case 'compute':
      return 'billed as running'
    case 'storage-only':
      return 'storage only'
    case 'none':
      return 'not billed'
    default:
      return v || '—'
  }
}

/** Form state for the settings a book carries besides its items — strings, as typed. */
export interface BookSettings {
  name: string
  currency: string
  annual_divisor: string
  bill_stopped: string
  effective_from: string
}

export function settingsFrom(b?: PriceBook | null): BookSettings {
  return {
    name: b?.name ?? '',
    currency: b?.currency ?? 'OMR',
    annual_divisor: String(b?.annual_divisor ?? 8760),
    bill_stopped: b?.bill_stopped ?? 'compute',
    effective_from: b?.effective_from ? b.effective_from.slice(0, 10) : '',
  }
}

export interface BookSettingsBody {
  name: string
  currency: string
  annual_divisor: number
  bill_stopped: string
  effective_from: string
}

export function settingsBody(s: BookSettings): BookSettingsBody {
  return {
    name: s.name.trim(),
    currency: s.currency.trim().toUpperCase(),
    annual_divisor: Number(s.annual_divisor),
    bill_stopped: s.bill_stopped,
    effective_from: s.effective_from || '',
  }
}

export type SettingsErrors = Partial<Record<keyof BookSettings, string>>

export function validateSettings(s: BookSettings): SettingsErrors {
  const e: SettingsErrors = {}
  if (!s.name.trim()) e.name = 'Name is required'
  if (!/^[A-Za-z]{3}$/.test(s.currency.trim())) e.currency = 'Three-letter ISO code, e.g. OMR'
  const d = Number(s.annual_divisor)
  if (!Number.isInteger(d) || d < 1) e.annual_divisor = 'Whole number of hours per year, e.g. 8760'
  if (s.effective_from && !/^\d{4}-\d{2}-\d{2}$/.test(s.effective_from)) e.effective_from = 'YYYY-MM-DD'
  return e
}

export function BookSettingsFields({ value, onChange, errors }: { value: BookSettings; onChange: (v: BookSettings) => void; errors: SettingsErrors }) {
  const set = (patch: Partial<BookSettings>) => onChange({ ...value, ...patch })
  return (
    <div className="grid2">
      <Field label="Name" error={errors.name}>
        <input value={value.name} onChange={(e) => set({ name: e.target.value })} autoFocus placeholder="e.g. Standard 2026" />
      </Field>
      <Field label="Currency" error={errors.currency} help="Statements for customers on this book are issued in it">
        <input value={value.currency} maxLength={3} onChange={(e) => set({ currency: e.target.value.toUpperCase() })} />
      </Field>
      <Field label="Annual divisor" error={errors.annual_divisor} help="Hours per year an annual list price is divided by; 8760 = 365 days">
        <input type="number" min={1} step={1} value={value.annual_divisor} onChange={(e) => set({ annual_divisor: e.target.value })} />
      </Field>
      <Field label="Stopped compute" help="What a stopped instance is billed">
        <select value={value.bill_stopped} onChange={(e) => set({ bill_stopped: e.target.value })}>
          {BILL_STOPPED_OPTIONS.map((o) => (
            <option key={o.value} value={o.value}>
              {o.label}
            </option>
          ))}
        </select>
      </Field>
      <Field label="Effective from" error={errors.effective_from} help="Leave empty to apply to every period">
        <input type="date" value={value.effective_from} onChange={(e) => set({ effective_from: e.target.value })} />
      </Field>
    </div>
  )
}

/** Create / edit-settings dialog. The caller performs the request; a thrown error is shown verbatim. */
export function BookSettingsModal({
  title,
  initial,
  submitLabel,
  onClose,
  onSubmit,
}: {
  title: string
  initial: BookSettings
  submitLabel: string
  onClose: () => void
  onSubmit: (body: BookSettingsBody) => Promise<void>
}) {
  const [value, setValue] = useState(initial)
  const [errors, setErrors] = useState<SettingsErrors>({})
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    const errs = validateSettings(value)
    setErrors(errs)
    if (Object.keys(errs).length) return
    setBusy(true)
    setError('')
    try {
      await onSubmit(settingsBody(value))
    } catch (err) {
      setError(errorText(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      title={title}
      onClose={onClose}
      footer={
        <>
          <button type="button" onClick={onClose} disabled={busy}>
            Cancel
          </button>
          <button className="primary" form="book-settings-form" disabled={busy}>
            {submitLabel}
          </button>
        </>
      }
    >
      <form id="book-settings-form" onSubmit={submit} className="stack tight">
        {error ? <Notice kind="bad">{error}</Notice> : null}
        <BookSettingsFields value={value} onChange={setValue} errors={errors} />
      </form>
    </Modal>
  )
}

/** Clone a book under a new name (per-account pricing). */
export function CloneBookModal({ book, onClose, onCloned }: { book: PriceBook; onClose: () => void; onCloned: (copy: PriceBook | null) => void }) {
  const [name, setName] = useState(`${book.name} (copy)`)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const clone = async (e: FormEvent) => {
    e.preventDefault()
    if (!name.trim()) {
      setError('Name is required')
      return
    }
    setBusy(true)
    setError('')
    try {
      const copy = await api.post<PriceBook | null>(`/pricebooks/${book.id}/clone`, { name: name.trim() })
      onCloned(copy && typeof copy === 'object' && typeof copy.id === 'string' ? copy : null)
    } catch (err) {
      setError(errorText(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      title={`Clone ${book.name}`}
      onClose={onClose}
      footer={
        <>
          <button type="button" onClick={onClose} disabled={busy}>
            Cancel
          </button>
          <button className="primary" form="clone-book-form" disabled={busy}>
            Clone
          </button>
        </>
      }
    >
      <form id="clone-book-form" onSubmit={clone}>
        <p className="muted small">Copies the settings and every item into a new book, so one customer can be priced differently without touching the shared list. Customers stay on the original.</p>
        <Field label="Name of the copy" error={error}>
          <input value={name} onChange={(e) => setName(e.target.value)} autoFocus />
        </Field>
      </form>
    </Modal>
  )
}

/** Delete a book; the server refuses (409) while customers are assigned, and the message is shown as sent. */
export function DeleteBookConfirm({ book, assigned, onClose, onDeleted }: { book: PriceBook; assigned: number; onClose: () => void; onDeleted: () => void }) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const del = async () => {
    setBusy(true)
    setError('')
    try {
      await api.del(`/pricebooks/${book.id}`)
      onDeleted()
    } catch (err) {
      setError(errorText(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Confirm
      title={`Delete ${book.name}?`}
      danger
      busy={busy}
      confirmLabel="Delete price book"
      onClose={onClose}
      onConfirm={del}
      body={
        <div className="stack tight">
          <p>Removes the book and every item in it. Statements already issued keep their rated lines.</p>
          {assigned > 0 ? (
            <Notice kind="warn">
              {assigned} customer{assigned === 1 ? ' is' : 's are'} on this book — the server refuses the delete until they are moved to another book.
            </Notice>
          ) : null}
          {error ? <Notice kind="bad">{error}</Notice> : null}
        </div>
      }
    />
  )
}
