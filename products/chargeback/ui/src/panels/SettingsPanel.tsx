import { useEffect, useState, type FormEvent } from 'react'
import { useNavigate } from 'react-router-dom'
import { api } from '../api/client'
import type { Customer, PriceBook } from '../api/types'
import { Badge, Confirm, Field, Notice } from '../components/ui'
import { BILLING_MODES, CUSTOMER_KINDS, customerPatch, settingsFrom, type CustomerSettings } from '../lib/customers'
import { hasErrors, validateSettings, type Errors } from '../lib/forms'
import { useAction } from '../lib/useAction'

/**
 * Every field PATCH /customers/{id} accepts, plus the delete at the bottom
 * (#6867). Only changed fields are sent, so a save never rewrites a value
 * the operator did not touch.
 */
export function SettingsPanel({ customer, books, onSaved }: { customer: Customer; books: PriceBook[]; onSaved: (c: Customer) => void | Promise<void> }) {
  const [form, setForm] = useState<CustomerSettings>(() => settingsFrom(customer))
  const [errors, setErrors] = useState<Errors<CustomerSettings>>({})
  const [deleting, setDeleting] = useState(false)
  const act = useAction()
  // The document changed underneath (our own save, or Suspend/Resume in the
  // header): show what is stored now. State (and the saved notice) survive.
  useEffect(() => {
    setForm(settingsFrom(customer))
  }, [customer])
  const set = <K extends keyof CustomerSettings>(k: K, v: CustomerSettings[K]) => setForm((f) => ({ ...f, [k]: v }))
  const patch = customerPatch(customer, form)
  const dirty = Object.keys(patch).length > 0
  const kind = CUSTOMER_KINDS.find((k) => k.value === (customer.kind ?? 'external'))

  const save = async (e: FormEvent) => {
    e.preventDefault()
    const errs = validateSettings(form)
    setErrors(errs)
    if (hasErrors(errs)) return
    if (!dirty) {
      act.setError('Nothing changed.')
      return
    }
    await act.run(`saved ${Object.keys(patch).map((k) => k.replace('_', ' ')).join(', ')}`, async () => {
      const c = await api.patch<Customer>(`/customers/${customer.id}`, patch)
      await onSaved(c)
    })
  }

  return (
    <div className="stack" style={{ maxWidth: 760 }}>
      <form className="card" onSubmit={(e) => void save(e)}>
        <h2>Customer settings</h2>
        {act.error ? <Notice kind="bad">{act.error}</Notice> : null}
        {act.ok ? <Notice kind="ok">{act.ok}</Notice> : null}
        <div className="grid2">
          <Field label="Name" error={errors.name}>
            <input value={form.name} onChange={(e) => set('name', e.target.value)} />
          </Field>
          <Field label="Slug" help="Fixed at creation — it is in every statement id and invite link.">
            <input value={customer.slug} disabled className="mono" />
          </Field>
        </div>
        <div className="grid2">
          <Field label="Admin email" error={errors.admin_email} help="Signs in as customer-admin and receives invites.">
            <input type="email" value={form.admin_email} onChange={(e) => set('admin_email', e.target.value)} />
          </Field>
          <Field label="Status" error={errors.status} help={form.status === 'active' ? 'Collected and billed.' : form.status === 'suspended' ? 'Not collected; sign-in refused.' : 'Waiting for the invite to be activated.'}>
            <select value={form.status} onChange={(e) => set('status', e.target.value)}>
              <option value="pending">pending</option>
              <option value="active">active</option>
              <option value="suspended">suspended</option>
            </select>
          </Field>
        </div>
        <div className="grid2">
          <Field label="Billing mode" error={errors.billing_mode} help={BILLING_MODES.find((m) => m.value === form.billing_mode)?.help}>
            <select value={form.billing_mode} onChange={(e) => set('billing_mode', e.target.value)}>
              {BILLING_MODES.map((m) => (
                <option key={m.value} value={m.value}>
                  {m.label}
                </option>
              ))}
            </select>
          </Field>
          <Field label="Price book" error={errors.price_book_id} help={form.price_book_id ? undefined : 'Without a price book nothing is rated — every cost shows as 0.'}>
            <select value={form.price_book_id} onChange={(e) => set('price_book_id', e.target.value)}>
              <option value="">— none —</option>
              {books.map((b) => (
                <option key={b.id} value={b.id}>
                  {b.name} ({b.currency})
                </option>
              ))}
            </select>
          </Field>
        </div>
        <div className="grid2">
          <Field label="Start date" error={errors.start_date} help="Usage before this day is not billed.">
            <input type="date" value={form.start_date} onChange={(e) => set('start_date', e.target.value)} />
          </Field>
          <Field label="Kind" help={kind?.help}>
            <input value={kind?.label ?? customer.kind ?? 'external'} disabled />
          </Field>
        </div>
        {(customer.kind ?? 'external') === 'organization' ? (
          <Field label="Organization slug" error={errors.org_slug} help="The Organization on this Sovereign whose allocated usage is billed to this customer.">
            <input value={form.org_slug} onChange={(e) => set('org_slug', e.target.value)} className="mono" />
          </Field>
        ) : null}
        <div className="row between">
          <span className="muted small">{dirty ? `${Object.keys(patch).length} field${Object.keys(patch).length === 1 ? '' : 's'} changed` : 'No changes'}</span>
          <span className="btn-row">
            <button type="button" onClick={() => { setForm(settingsFrom(customer)); setErrors({}); act.clear() }} disabled={!dirty || act.busy}>
              Reset
            </button>
            <button className="primary" disabled={act.busy || !dirty}>
              Save
            </button>
          </span>
        </div>
      </form>

      <div className="card" style={{ borderColor: 'var(--bad-line)' }}>
        <div className="card-head">
          <h2>Delete customer</h2>
          <Badge status={customer.status} />
        </div>
        <p className="muted small">Removes the customer, its cost sources and credentials, all collected usage and every draft statement. Refused while an issued statement exists — an issued bill is a permanent record; suspend the customer instead.</p>
        <button className="danger" onClick={() => setDeleting(true)}>
          Delete {customer.name}…
        </button>
      </div>
      {deleting ? <DeleteCustomerConfirm customer={customer} onClose={() => setDeleting(false)} /> : null}
    </div>
  )
}

/** The delete dialog, shared by the header action and the settings tab. */
export function DeleteCustomerConfirm({ customer, onClose }: { customer: Customer; onClose: () => void }) {
  const nav = useNavigate()
  const act = useAction()
  return (
    <Confirm
      title={`Delete ${customer.name}`}
      danger
      confirmLabel="Delete customer"
      busy={act.busy}
      onClose={onClose}
      onConfirm={async () => {
        const ok = await act.run('deleted', () => api.del(`/customers/${customer.id}`))
        if (ok) nav('/customers', { replace: true })
      }}
      body={
        <div className="stack tight">
          {act.error ? <Notice kind="bad">{act.error}</Notice> : null}
          <p>
            This removes <b>{customer.name}</b> (<span className="mono">{customer.slug}</span>) together with its cost sources and credentials, all collected usage, and every draft statement. It cannot be undone.
          </p>
          <p className="muted small">The server refuses while an issued statement exists for this customer — issued bills are permanent records. In that case suspend the customer instead.</p>
        </div>
      }
    />
  )
}
