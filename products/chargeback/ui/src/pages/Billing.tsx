import { useState, type FormEvent } from 'react'
import { Link } from 'react-router-dom'
import { api } from '../api/client'
import type { BillingSettings } from '../api/types'
import { Field, Notice, PageHeader, Skeleton } from '../components/ui'
import { parseReminderDays, reminderScheduleText } from '../lib/account'
import { ESCALATION_ACTIONS, billingBody, billingFormFrom, validateBilling, type BillingForm } from '../lib/billing'
import { when } from '../lib/format'
import { hasErrors, type Errors } from '../lib/forms'
import { useAction } from '../lib/useAction'
import { useQuery } from '../lib/useQuery'

/**
 * Billing settings (DESIGN.md §8 + §9): how invoices and credit notes are
 * numbered, the Sovereign's tax identity and default rate, and the
 * collections schedule. One PUT /billing-settings carries only what changed;
 * the discount combination rule keeps its own card on the Discounts page.
 */
export function Billing() {
  const settings = useQuery<BillingSettings>('/billing-settings')
  const [edits, setEdits] = useState<Partial<BillingForm>>({})
  const [errors, setErrors] = useState<Errors<BillingForm>>({})
  const act = useAction()

  if (settings.error && !settings.data) {
    return (
      <div className="stack">
        <PageHeader title="Billing" />
        <Notice kind="bad">{settings.error}</Notice>
      </div>
    )
  }
  if (!settings.data) return <Skeleton lines={6} />
  const saved = settings.data
  const form: BillingForm = { ...billingFormFrom(saved), ...edits }
  const set = <K extends keyof BillingForm>(k: K, v: BillingForm[K]) => setEdits((e) => ({ ...e, [k]: v }))
  const body = billingBody(saved, form)
  const dirty = body !== null
  const days = parseReminderDays(form.reminder_days)
  const external = saved.commercial_provider === 'external'

  const save = async (e: FormEvent) => {
    e.preventDefault()
    const errs = validateBilling(form)
    setErrors(errs)
    if (hasErrors(errs)) return
    if (!body) {
      act.setError('Nothing changed.')
      return
    }
    const changed = Object.keys(body).filter((k) => k !== 'discount_rule')
    await act.run(`saved ${changed.map((k) => k.replace(/_/g, ' ')).join(', ')}`, () => api.put('/billing-settings', body), async () => {
      await settings.reload()
      setEdits({})
    })
  }

  return (
    <div className="stack" style={{ maxWidth: 820 }}>
      <PageHeader title="Billing" sub={<>{saved.updated_at ? `last changed ${when(saved.updated_at)}` : 'never changed'} · invoicing {external ? "through the operator's billing system" : 'from this Sovereign'}</>} />
      {act.error ? <Notice kind="bad">{act.error}</Notice> : null}
      {act.ok ? <Notice kind="ok">{act.ok}</Notice> : null}
      {external ? <Notice kind="info">The operator's billing system numbers invoices and collects. The prefixes and the collections schedule below apply to what this product keeps on its side; the tax identity is still printed on every rated bill it exports.</Notice> : null}

      <form className="stack" onSubmit={(e) => void save(e)}>
        <div className="card">
          <h2>Numbering</h2>
          <p className="muted small">
            Invoice numbers are gapless per calendar year and assigned at issue; credit notes are numbered the same way under their own prefix, so one is never mistaken for the other. Changing a prefix affects the next number issued, never an issued document.
          </p>
          <div className="grid2">
            <Field label="Invoice prefix" error={errors.invoice_prefix} help="Upper-case letters, digits or dashes; INV gives INV-2026-00001.">
              <input value={form.invoice_prefix} onChange={(e) => set('invoice_prefix', e.target.value.toUpperCase())} className="mono" placeholder="INV" />
            </Field>
            <Field label="Credit-note prefix" error={errors.credit_note_prefix} help="Must differ from the invoice prefix; CN gives CN-2026-00001.">
              <input value={form.credit_note_prefix} onChange={(e) => set('credit_note_prefix', e.target.value.toUpperCase())} className="mono" placeholder="CN" />
            </Field>
          </div>
        </div>

        <div className="card">
          <h2>Tax</h2>
          <p className="muted small">The default rate every statement is taxed at unless the customer carries an override or an exemption, and the seller identity frozen onto every invoice at issue (DESIGN.md §9.4).</p>
          <div className="grid2">
            <Field label="Tax rate (%)" error={errors.tax_rate} help="Applied to the net subtotal. 0 for no tax.">
              <input value={form.tax_rate} onChange={(e) => set('tax_rate', e.target.value)} inputMode="decimal" placeholder="5" aria-label="Tax rate" />
            </Field>
            <Field label="Tax registration number" error={errors.tax_registration_number} help="The Sovereign's own registration, printed on every invoice.">
              <input value={form.tax_registration_number} onChange={(e) => set('tax_registration_number', e.target.value)} className="mono" placeholder="OM1234567890" />
            </Field>
          </div>
          <div className="grid2">
            <Field label="Legal name" error={errors.legal_name} help="The seller as it appears on the invoice.">
              <input value={form.legal_name} onChange={(e) => set('legal_name', e.target.value)} />
            </Field>
            <Field label="Address" error={errors.address}>
              <input value={form.address} onChange={(e) => set('address', e.target.value)} />
            </Field>
          </div>
        </div>

        <div className="card">
          <div className="card-head">
            <h2>Collections</h2>
            <span className="hint">
              <Link to="/collections">open the aging report</Link>
            </span>
          </div>
          <p className="muted small">When an invoice is reminded about, measured from its due date, and what happens once it is escalated. The daily pass and Run collections now both follow this (DESIGN.md §9.6).</p>
          <Field label="Reminder days" error={errors.reminder_days} help={days.error ? 'Comma-separated whole days; negative is before the due date.' : `Reads: ${reminderScheduleText(days.values)}.`}>
            <input value={form.reminder_days} onChange={(e) => set('reminder_days', e.target.value)} className="mono" placeholder="-3, 0, 7, 14, 30" aria-label="Reminder days" />
          </Field>
          <div className="grid2">
            <Field label="Escalate after (days)" error={errors.escalation_days} help="Days past the due date before the escalation action runs. 0 turns escalation off.">
              <input value={form.escalation_days} onChange={(e) => set('escalation_days', e.target.value)} inputMode="numeric" placeholder="45" aria-label="Escalation days" />
            </Field>
            <Field label="Escalation action" error={errors.escalation_action} help={ESCALATION_ACTIONS.find((a) => a.value === form.escalation_action)?.help ?? 'What happens on the escalation day.'}>
              <select value={form.escalation_action} onChange={(e) => set('escalation_action', e.target.value)} aria-label="Escalation action">
                <option value="">choose…</option>
                {ESCALATION_ACTIONS.map((a) => (
                  <option key={a.value} value={a.value}>
                    {a.label}
                  </option>
                ))}
              </select>
            </Field>
          </div>
        </div>

        <div className="row between">
          <span className="muted small">{dirty ? `${Object.keys(body).length - 1} field${Object.keys(body).length === 2 ? '' : 's'} changed` : 'No changes'}</span>
          <span className="btn-row">
            <button
              type="button"
              onClick={() => {
                setEdits({})
                setErrors({})
                act.clear()
              }}
              disabled={!dirty || act.busy}
            >
              Reset
            </button>
            <button className="primary" disabled={act.busy || !dirty}>
              Save
            </button>
          </span>
        </div>
      </form>
    </div>
  )
}
