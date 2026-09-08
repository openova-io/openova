import { useState, type FormEvent } from 'react'
import { Navigate, useNavigate } from 'react-router-dom'
import { api } from '../api/client'
import type { Customer } from '../api/types'
import { Field, Modal, Notice } from '../components/ui'
import { BILLING_MODES, CUSTOMER_KINDS } from '../lib/customers'
import { customerBody, emptyCustomerForm, hasErrors, slugify, validateCustomer, type CustomerForm, type Errors } from '../lib/forms'
import { useAction } from '../lib/useAction'

/** /customers/new is kept as a deep link; the form itself is a modal on the list. */
export function CustomerNew() {
  return <Navigate to="/customers?new=1" replace />
}

export function NewCustomerModal({ onClose }: { onClose: () => void }) {
  const nav = useNavigate()
  const [form, setForm] = useState<CustomerForm>(emptyCustomerForm)
  const [slugTouched, setSlugTouched] = useState(false)
  const [errors, setErrors] = useState<Errors<CustomerForm>>({})
  const act = useAction()
  const set = <K extends keyof CustomerForm>(k: K, v: CustomerForm[K]) => setForm((f) => ({ ...f, [k]: v }))

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    const errs = validateCustomer(form)
    setErrors(errs)
    if (hasErrors(errs)) return
    await act.run('created', async () => {
      const c = await api.post<Customer>('/customers', customerBody(form))
      // A customer is defined by where its cost comes from (founder,
      // 2026-09-08), so creation lands on the Sources tab with the
      // add-source modal already open.
      nav(`/customers/${c.id}?tab=sources&add=1`)
    })
  }

  return (
    <Modal
      title="New customer"
      onClose={onClose}
      footer={
        <>
          <button type="button" onClick={onClose} disabled={act.busy}>
            Cancel
          </button>
          <button className="primary" form="new-customer-form" disabled={act.busy}>
            Create and add a source
          </button>
        </>
      }
    >
      <form id="new-customer-form" onSubmit={(e) => void submit(e)}>
        {act.error ? <Notice kind="bad">{act.error}</Notice> : null}
        <div className="grid2">
          <Field label="Name" error={errors.name}>
            <input
              value={form.name}
              onChange={(e) => {
                set('name', e.target.value)
                if (!slugTouched) set('slug', slugify(e.target.value))
              }}
              placeholder="Acme Trading LLC"
              autoFocus
            />
          </Field>
          <Field label="Slug" error={errors.slug} help="Lowercase; used in statement ids and invite links. Cannot change later.">
            <input
              value={form.slug}
              onChange={(e) => {
                setSlugTouched(true)
                set('slug', e.target.value.toLowerCase())
              }}
              className="mono"
              placeholder="acme"
            />
          </Field>
        </div>
        <Field label="Admin email" error={errors.admin_email} help="Receives the invite and signs in as customer-admin.">
          <input type="email" value={form.admin_email} onChange={(e) => set('admin_email', e.target.value)} placeholder="finance@example.com" />
        </Field>
        <div className="grid2">
          <Field label="Kind" error={errors.kind} help={CUSTOMER_KINDS.find((k) => k.value === form.kind)?.help}>
            <select value={form.kind} onChange={(e) => set('kind', e.target.value)}>
              {CUSTOMER_KINDS.map((k) => (
                <option key={k.value} value={k.value}>
                  {k.label}
                </option>
              ))}
            </select>
          </Field>
          {form.kind === 'organization' ? (
            <Field label="Organization slug" error={errors.org_slug} help="The Organization on this Sovereign; optional until it exists.">
              <input value={form.org_slug} onChange={(e) => set('org_slug', e.target.value.toLowerCase())} className="mono" placeholder="acme" />
            </Field>
          ) : (
            <Field label="Billing mode" error={errors.billing_mode} help={BILLING_MODES.find((m) => m.value === form.billing_mode)?.help}>
              <select value={form.billing_mode} onChange={(e) => set('billing_mode', e.target.value)}>
                {BILLING_MODES.map((m) => (
                  <option key={m.value} value={m.value}>
                    {m.label}
                  </option>
                ))}
              </select>
            </Field>
          )}
        </div>
        {form.kind === 'organization' ? (
          <Field label="Billing mode" error={errors.billing_mode} help={BILLING_MODES.find((m) => m.value === form.billing_mode)?.help}>
            <select value={form.billing_mode} onChange={(e) => set('billing_mode', e.target.value)}>
              {BILLING_MODES.map((m) => (
                <option key={m.value} value={m.value}>
                  {m.label}
                </option>
              ))}
            </select>
          </Field>
        ) : null}
        <Field label="Start date" error={errors.start_date} help="Usage before this day is not billed. Empty = from the first collection.">
          <input type="date" value={form.start_date} onChange={(e) => set('start_date', e.target.value)} />
        </Field>
        <p className="muted small">
          Next you define <b>where this customer's cost comes from</b>: Save opens the Sources tab with the add-source form, and the price book is chosen there — one per source.
        </p>
        <p className="muted small">The customer starts <b>pending</b>. Send the invite from its page; activating it links the admin email and turns the customer active.</p>
      </form>
    </Modal>
  )
}
