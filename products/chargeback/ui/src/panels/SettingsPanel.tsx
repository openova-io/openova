import { useEffect, useState, type FormEvent } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { api } from '../api/client'
import type { Customer } from '../api/types'
import { Badge, Confirm, Field, Notice } from '../components/ui'
import { CHARGING_OPTIONS, CUSTOMER_KINDS, GATEWAYS, PAYMENT_METHODS, PAYMENT_MODELS, customerPatch, fieldLabel, settingsFrom, type CustomerSettings } from '../lib/customers'
import { today } from '../lib/format'
import { hasErrors, validateSettings, type Errors } from '../lib/forms'
import { certificateState, certificateText, dayOf } from '../lib/tax'
import { useAction } from '../lib/useAction'
import { useQuery } from '../lib/useQuery'

/**
 * Every field PATCH /customers/{id} accepts, plus the delete at the bottom
 * (#6867). Only changed fields are sent, so a save never rewrites a value
 * the operator did not touch.
 */
export function SettingsPanel({ customer, onSaved }: { customer: Customer; onSaved: (c: Customer) => void | Promise<void> }) {
  const [form, setForm] = useState<CustomerSettings>(() => settingsFrom(customer))
  const [errors, setErrors] = useState<Errors<CustomerSettings>>({})
  const [deleting, setDeleting] = useState(false)
  const act = useAction()
  // DESIGN.md §8.10 — WHO invoices on this Sovereign. When the operator's own
  // billing system does, the commercial fields are theirs and are read-only
  // here; the billing-account id stays ours to fill in.
  const settings = useQuery<{ commercial_provider?: string }>('/billing-settings')
  const external = settings.data?.commercial_provider === 'external'
  // The document changed underneath (our own save, or Suspend/Resume in the
  // header): show what is stored now. State (and the saved notice) survive.
  useEffect(() => {
    setForm(settingsFrom(customer))
  }, [customer])
  const set = <K extends keyof CustomerSettings>(k: K, v: CustomerSettings[K]) => setForm((f) => ({ ...f, [k]: v }))
  const patch = customerPatch(customer, form)
  const dirty = Object.keys(patch).length > 0
  const kind = CUSTOMER_KINDS.find((k) => k.value === (customer.kind ?? 'external'))
  // DESIGN.md §17 — the exemption certificate as it STANDS (the saved
  // document), and as the form would leave it. An expired certificate is not
  // an exemption: the rating engine falls back to the standard rate, and an
  // issuer that is not told quietly carries the liability.
  const on = today()
  const savedExpiry = dayOf(customer.tax_exemption_expires_on)
  const savedCertificateNumber = (customer.tax_exemption_number ?? '').trim()
  const certificate = certificateState(customer, on)
  const formCertificate = certificateState({ tax_exempt: form.tax_exempt, tax_exemption_number: form.tax_exemption_number, tax_exemption_expires_on: form.tax_exemption_expires_on }, on)

  const save = async (e: FormEvent) => {
    e.preventDefault()
    const errs = validateSettings(form)
    setErrors(errs)
    if (hasErrors(errs)) return
    if (!dirty) {
      act.setError('Nothing changed.')
      return
    }
    await act.run(`saved ${Object.keys(patch).map(fieldLabel).join(', ')}`, async () => {
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
        {/* DESIGN.md §8 — the commercial model. Three questions, three
            controls: is anything collected, when is it paid, and how does
            the money move. The deprecated billing_mode is derived from them
            server-side and is neither shown nor sent. */}
        <h3 className="section">How this customer is charged</h3>
        {external ? (
          <Notice kind="warn">
            This Sovereign invoices through the operator's billing system. Charging, payment model, method and terms are owned there and are read-only here &mdash; what we do is rate the usage
            and hand it over.
          </Notice>
        ) : null}
        <div className="grid2">
          <Field label="Charging" error={errors.charging} help={CHARGING_OPTIONS.find((o) => o.value === form.charging)?.help}>
            <select value={form.charging} onChange={(e) => set('charging', e.target.value)} disabled={external}>
              {CHARGING_OPTIONS.map((o) => (
                <option key={o.value} value={o.value}>
                  {o.label}
                </option>
              ))}
            </select>
          </Field>
          <Field label="Start date" error={errors.start_date} help="Usage before this day is not billed.">
            <input type="date" value={form.start_date} onChange={(e) => set('start_date', e.target.value)} />
          </Field>
        </div>
        {form.charging === 'billed' ? (
          <>
            <div className="grid2">
              <Field label="Payment model" error={errors.payment_model} help={PAYMENT_MODELS.find((o) => o.value === form.payment_model)?.help ?? 'When the customer pays, relative to the usage.'}>
                <select value={form.payment_model} onChange={(e) => set('payment_model', e.target.value)} disabled={external}>
                  <option value="">choose…</option>
                  {PAYMENT_MODELS.map((o) => (
                    <option key={o.value} value={o.value}>
                      {o.label}
                    </option>
                  ))}
                </select>
              </Field>
              <Field label="Payment method" error={errors.payment_method} help={PAYMENT_METHODS.find((o) => o.value === form.payment_method)?.help ?? 'How the money actually moves.'}>
                <select
                  value={form.payment_method}
                  disabled={external}
                  onChange={(e) => {
                    set('payment_method', e.target.value)
                    if (e.target.value !== 'gateway') set('gateway_name', '')
                    else if (!form.gateway_name) set('gateway_name', GATEWAYS[0]?.value ?? '')
                  }}
                >
                  <option value="">choose…</option>
                  {PAYMENT_METHODS.map((o) => (
                    <option key={o.value} value={o.value}>
                      {o.label}
                    </option>
                  ))}
                </select>
              </Field>
            </div>
            {form.payment_method === 'gateway' ? (
              <Field label="Payment gateway" error={errors.gateway_name} help="Which gateway collects. Adding another is a deployment change, not a customer one.">
                <select value={form.gateway_name} onChange={(e) => set('gateway_name', e.target.value)} disabled={external}>
                  <option value="">choose…</option>
                  {GATEWAYS.map((g) => (
                    <option key={g.value} value={g.value}>
                      {g.label}
                    </option>
                  ))}
                </select>
              </Field>
            ) : (
              <div className="grid2">
                <Field label="Purchase order" error={errors.po_reference} help="Quoted on every invoice issued to this customer; a statement can override it before it is issued.">
                  <input value={form.po_reference} onChange={(e) => set('po_reference', e.target.value)} className="mono" placeholder="PO-4471" disabled={external} />
                </Field>
                <Field label="Payment terms" error={errors.payment_terms_days} help={form.payment_terms_days === '0' ? 'Due on receipt.' : 'Days from the issue date to the due date. 30 is the default.'}>
                  <input type="number" min={0} max={365} value={form.payment_terms_days} onChange={(e) => set('payment_terms_days', e.target.value)} placeholder="30" disabled={external} />
                </Field>
              </div>
            )}
          </>
        ) : (
          <p className="muted small" style={{ marginTop: 0 }}>
            Statements are still rated and shown, and nothing is ever collected. Switch charging to billed to choose how it is paid.
          </p>
        )}
        {/* DESIGN.md §9.5 — account credit. Applying credit is explicit
            unless the operator switches it on here; the prepaid wallet adds
            suspend-at-zero. */}
        {form.charging === 'billed' ? (
          <>
            <h3 className="section">Account credit</h3>
            <label className="check">
              <input type="checkbox" checked={form.auto_apply_credit} onChange={(e) => set('auto_apply_credit', e.target.checked)} /> Auto-apply credit to new invoices
            </label>
            <div className="help">Available credit is applied when an invoice is issued. Off, it stays on account until applied from the Account tab.</div>
            {form.payment_model === 'prepaid' ? (
              <>
                <label className="check">
                  <input type="checkbox" checked={form.suspend_at_zero} onChange={(e) => set('suspend_at_zero', e.target.checked)} /> Suspend when balance reaches zero
                </label>
                <div className="help">A prepaid wallet that runs out suspends the Organization at the platform; a top-up resumes it.</div>
              </>
            ) : null}
          </>
        ) : null}
        {/* DESIGN.md §9.4 + §17 — the tax profile. WHERE the customer is
            registered and whether it is a registered BUSINESS is what the
            rules on Configure → Tax resolve against; the exemption
            CERTIFICATE is what makes tax_exempt auditable, and an expired
            one is not an exemption. */}
        <h3 className="section">Tax</h3>
        {certificate === 'expired' ? (
          <Notice kind="bad">
            The exemption certificate {savedCertificateNumber ? <span className="mono">{savedCertificateNumber}</span> : null} {certificateText('expired', savedExpiry)}.
          </Notice>
        ) : null}
        <div className="grid2">
          <Field
            label="Tax country"
            error={errors.tax_country}
            help="ISO 3166-1 alpha-2, where this customer is registered. Empty is treated as domestic. It is what decides which rule applies and whether the supply is cross-border."
          >
            <input value={form.tax_country} onChange={(e) => set('tax_country', e.target.value.toUpperCase())} className="mono" maxLength={2} placeholder="domestic" aria-label="Tax country" />
          </Field>
          <Field label="Tax region" error={errors.tax_region} help="Only where a country taxes by region; empty is the whole country.">
            <input value={form.tax_region} onChange={(e) => set('tax_region', e.target.value)} placeholder="the whole country" aria-label="Tax region" />
          </Field>
        </div>
        <label className="check">
          <input type="checkbox" checked={form.tax_business} onChange={(e) => set('tax_business', e.target.checked)} aria-label="Registered business" /> Registered business
        </label>
        <div className="help">
          A business buyer, not a consumer. Reverse charge applies to a registered business in another country and never to a consumer, so this — not the registration number — is what makes it apply.
        </div>
        <label className="check">
          <input type="checkbox" checked={form.tax_exempt} onChange={(e) => set('tax_exempt', e.target.checked)} /> Tax exempt
        </label>
        {form.tax_exempt ? (
          <>
            <Field label="Exemption reason" error={errors.tax_exempt_reason} help="Printed on every invoice in place of the tax line.">
              <input value={form.tax_exempt_reason} onChange={(e) => set('tax_exempt_reason', e.target.value)} placeholder="government entity" />
            </Field>
            <div className="grid2">
              <Field label="Certificate number" error={errors.tax_exemption_number} help="The certificate the exemption rests on, recorded so it can be produced.">
                <input value={form.tax_exemption_number} onChange={(e) => set('tax_exemption_number', e.target.value)} className="mono" placeholder="unnumbered" aria-label="Certificate number" />
              </Field>
              <Field
                label="Certificate expires"
                error={errors.tax_exemption_expires_on}
                help={
                  formCertificate === 'expired'
                    ? `Expired: ${certificateText('expired', form.tax_exemption_expires_on)}.`
                    : formCertificate === 'valid'
                      ? `The certificate is ${certificateText('valid', form.tax_exemption_expires_on)} — it still applies on that day, and the standard rate applies from the day after.`
                      : 'Empty = the exemption does not lapse. From the day named, the rating engine falls back to the standard rate.'
                }
              >
                <input type="date" value={form.tax_exemption_expires_on} onChange={(e) => set('tax_exemption_expires_on', e.target.value)} aria-label="Certificate expires" />
              </Field>
            </div>
            <Field label="Certificate scan" error={errors.tax_exemption_scan_ref} help="Where the scanned certificate is filed — a document reference, not the document itself.">
              <input value={form.tax_exemption_scan_ref} onChange={(e) => set('tax_exemption_scan_ref', e.target.value)} className="mono" placeholder="optional" aria-label="Certificate scan" />
            </Field>
          </>
        ) : null}
        <div className="grid2">
          <Field label="Tax rate override (%)" error={errors.tax_rate} help="Leave empty for the Sovereign default rate from Billing settings. It replaces the RATE of whatever rule applied, never what the supply is.">
            <input value={form.tax_rate} onChange={(e) => set('tax_rate', e.target.value)} inputMode="decimal" placeholder="default" disabled={form.tax_exempt} />
          </Field>
          <Field label="Tax registration number" error={errors.tax_registration_number} help="The customer's registration, printed on its invoices.">
            <input value={form.tax_registration_number} onChange={(e) => set('tax_registration_number', e.target.value)} className="mono" placeholder="OM1234567890" />
          </Field>
        </div>
        <div className="help">
          Which rate a supply carries is decided by the rules on <Link to="/tax">Configure &rarr; Tax</Link>, against the country, region and category above.
        </div>
        <div className="grid2">
          <Field label="Kind" help={kind?.help}>
            <input value={kind?.label ?? customer.kind ?? 'external'} disabled />
          </Field>
          {/* DESIGN.md §2: a price book is assigned to each SOURCE, not to
              the customer — a cloud source takes a cloud book, a platform
              source a platform book. */}
          <Field label="Price books" help="Assigned per cost source: a cloud source takes a cloud book, a platform source a platform book.">
            <Link to={`/customers/${customer.id}?tab=sources`}>Assign them on the Sources tab</Link>
          </Field>
        </div>
        {external ? (
          <Field label="Billing account id" error={errors.external_account_id} help="This customer's account in the operator's billing system; every rated bill we export is attributed to it.">
            <input value={form.external_account_id} onChange={(e) => set('external_account_id', e.target.value)} className="mono" placeholder="BA-99001" />
          </Field>
        ) : null}
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
