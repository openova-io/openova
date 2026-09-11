import { useMemo, useState, type FormEvent } from 'react'
import { Link } from 'react-router-dom'
import { api } from '../api/client'
import type { BillingSettings, TaxCategoryRule, TaxRule, TaxRulesDoc } from '../api/types'
import { useSession } from '../auth/session'
import { DataTable, type Column } from '../components/DataTable'
import { Badge, Confirm, EmptyState, Field, KPI, Modal, Notice, PageHeader, Skeleton } from '../components/ui'
import { can } from '../lib/access'
import { taxRateText } from '../lib/billing'
import { today } from '../lib/format'
import { hasErrors, type Errors } from '../lib/forms'
import {
  TAX_KINDS,
  chargesTax,
  emptyTaxCategoryForm,
  emptyTaxRuleForm,
  isSKUFamily,
  kindTone,
  knownCategories,
  phaseTone,
  rateText,
  rulePhase,
  scopeText,
  taxCategoryBody,
  taxCategoryFormFrom,
  taxKindHelp,
  taxKindLabel,
  taxRuleBody,
  taxRuleFormFrom,
  validateTaxCategory,
  validateTaxRule,
  validityText,
  withKind,
  type TaxCategoryForm,
  type TaxRuleForm,
} from '../lib/tax'
import { useAction } from '../lib/useAction'
import { useQuery } from '../lib/useQuery'

/**
 * Configure → Tax (DESIGN.md §17). One rate on billing settings answers a
 * Sovereign selling one kind of service inside one country. A tax authority
 * asks for more than that: a rate that differs by CATEGORY, a rate that
 * changes ON A DATE, REVERSE CHARGE for a registered business abroad, an
 * exemption CERTIFICATE that expires, and several rates on one invoice with
 * a summary by rate.
 *
 * This page holds the two tables that answer it — the RULES and the SKU-to-
 * CATEGORY placements — beside the Sovereign's own registration, which lives
 * on Billing and is linked, never duplicated: two editors for one field is
 * how the two disagree.
 *
 * Reading is `metering.read` (a rate is not a secret, and every role that
 * reads a bill may see why it was taxed); writing is `settings.manage`.
 */
type Dialog =
  | { kind: 'rule'; rule?: TaxRule }
  | { kind: 'delete-rule'; rule: TaxRule }
  | { kind: 'category'; category?: TaxCategoryRule }
  | { kind: 'delete-category'; category: TaxCategoryRule }
  | null

export function Tax() {
  const { me } = useSession()
  const canManage = can(me, 'settings.manage')
  const rulesQ = useQuery<TaxRulesDoc>('/tax/rules')
  const catsQ = useQuery<{ categories?: TaxCategoryRule[] }>('/tax/categories')
  const settings = useQuery<BillingSettings>('/billing-settings')
  const [dialog, setDialog] = useState<Dialog>(null)
  const act = useAction()
  const close = () => setDialog(null)

  const rules = useMemo(() => rulesQ.data?.rules ?? [], [rulesQ.data])
  const categories = useMemo(() => catsQ.data?.categories ?? [], [catsQ.data])
  const on = today()
  const inForce = rules.filter((r) => rulePhase(r, on) === 'in force')
  const countries = new Set(rules.map((r) => r.country).filter(Boolean))
  const sellerCountry = (settings.data?.tax_country ?? '').trim()
  const categoryNames = useMemo(() => knownCategories(categories, rules), [categories, rules])

  const reload = async () => {
    await Promise.all([rulesQ.reload(), catsQ.reload()])
  }

  const ruleColumns: Column<TaxRule>[] = [
    {
      key: 'name',
      header: 'Rule',
      value: (r) => r.name,
      render: (r) => (
        <span>
          {r.name}
          {r.note ? <span className="sub">{r.note}</span> : <span className="sub muted">no note — nothing is printed on the invoice for this rule</span>}
        </span>
      ),
    },
    { key: 'scope', header: 'Applies to', value: (r) => scopeText(r), render: (r) => <span className="nowrap">{scopeText(r)}</span> },
    { key: 'kind', header: 'Kind', value: (r) => taxKindLabel(r.kind), render: (r) => <Badge status={taxKindLabel(r.kind)} kind={kindTone(r.kind)} /> },
    {
      key: 'rate',
      header: 'Rate',
      value: (r) => Number(r.rate),
      numeric: true,
      render: (r) => (chargesTax(r.kind) ? <span className="nowrap">{rateText(r.rate)}</span> : <span className="muted nowrap">{rateText(r.rate)}</span>),
    },
    {
      key: 'validity',
      header: 'Valid',
      value: (r) => r.effective_from,
      render: (r) => {
        const phase = rulePhase(r, on)
        return (
          <span className="nowrap">
            {validityText(r)}
            <span className="sub">
              <Badge status={phase} kind={phaseTone(phase)} />
            </span>
          </span>
        )
      },
    },
    {
      key: 'actions',
      header: '',
      value: () => '',
      sortable: false,
      className: 'nowrap actions',
      render: (r) =>
        canManage ? (
          <span className="btn-row">
            <button className="link small" disabled={act.busy} onClick={() => setDialog({ kind: 'rule', rule: r })}>
              Edit
            </button>
            <button className="link small danger" disabled={act.busy} onClick={() => setDialog({ kind: 'delete-rule', rule: r })}>
              Delete
            </button>
          </span>
        ) : null,
    },
  ]

  const categoryColumns: Column<TaxCategoryRule>[] = [
    {
      key: 'sku',
      header: 'SKU',
      value: (c) => c.sku,
      render: (c) => (
        <span>
          <span className="mono">{c.sku}</span>
          <span className="sub">{isSKUFamily(c.sku) ? 'every SKU starting with this prefix' : 'this SKU exactly'}</span>
        </span>
      ),
    },
    { key: 'category', header: 'Category', value: (c) => c.category, render: (c) => <span className="mono">{c.category}</span> },
    { key: 'note', header: 'Note', value: (c) => c.note ?? '', render: (c) => (c.note ? <span>{c.note}</span> : <span className="muted">—</span>) },
    {
      key: 'actions',
      header: '',
      value: () => '',
      sortable: false,
      className: 'nowrap actions',
      render: (c) =>
        canManage ? (
          <span className="btn-row">
            <button className="link small" disabled={act.busy} onClick={() => setDialog({ kind: 'category', category: c })}>
              Edit
            </button>
            <button className="link small danger" disabled={act.busy} onClick={() => setDialog({ kind: 'delete-category', category: c })}>
              Delete
            </button>
          </span>
        ) : null,
    },
  ]

  return (
    <div className="stack">
      <PageHeader
        title="Tax"
        sub="What each supply is taxed at, to whom, and from when — by country, region and category of supply, each rule valid over its own dates. An invoice is rated at the rules in force on the day it covers, and prints one summary row per rule that applied."
        actions={
          canManage ? (
            <button className="primary" onClick={() => setDialog({ kind: 'rule' })}>
              New rule
            </button>
          ) : null
        }
      />
      {act.error ? <Notice kind="bad">{act.error}</Notice> : null}
      {act.ok ? <Notice kind="ok">{act.ok}</Notice> : null}
      {rulesQ.error ? <Notice kind="bad">{rulesQ.error}</Notice> : null}
      {catsQ.error ? <Notice kind="bad">{catsQ.error}</Notice> : null}
      {!canManage ? (
        <Notice kind="info">
          Read-only: rules and categories are edited with <code>settings.manage</code> (sovereign-admin). A tax rule changes what every future invoice charges.
        </Notice>
      ) : null}

      <div className="kpis">
        <KPI label="Rules in force" value={inForce.length} note={rules.length === inForce.length ? 'every rule on the book' : `of ${rules.length} on the book`} />
        <KPI label="Countries" value={countries.size} note={countries.size ? [...countries].sort().join(' · ') : 'no rule authored yet'} />
        <KPI label="Categories placed" value={categories.length} note={categories.length ? `${categoryNames.length} categor${categoryNames.length === 1 ? 'y' : 'ies'} in use` : 'every SKU is in the default category'} />
        <KPI
          label="Registered in"
          value={sellerCountry || '—'}
          note={sellerCountry ? 'the seller country every determination turns on' : 'not configured — no cross-border determination is made'}
          tone={sellerCountry ? undefined : 'warn'}
        />
      </div>

      {/* The Sovereign's own identity is Billing's (DESIGN.md §9.4) and is
          linked, not copied: two editors for one field is how the two
          disagree. tax_country is the one field §17 adds, and it is edited
          there beside the registration number it belongs with. */}
      <div className="card">
        <div className="card-head">
          <h2>This Sovereign</h2>
          <span className="hint">
            <Link to="/billing">edit on Billing settings</Link>
          </span>
        </div>
        {settings.error ? (
          <Notice kind="bad">{settings.error}</Notice>
        ) : !settings.data ? (
          <Skeleton lines={2} />
        ) : (
          <>
            <dl className="kv">
              <dt>Registration country</dt>
              <dd>{sellerCountry ? <span className="mono">{sellerCountry}</span> : <span className="warn">not configured</span>}</dd>
              <dt>Registration number</dt>
              <dd>{settings.data.tax_registration_number ? <span className="mono">{settings.data.tax_registration_number}</span> : <span className="muted">none</span>}</dd>
              <dt>Legal name</dt>
              <dd>{settings.data.legal_name || <span className="muted">none</span>}</dd>
              <dt>Address</dt>
              <dd>{settings.data.address || <span className="muted">none</span>}</dd>
              <dt>Default rate</dt>
              <dd>
                {taxRateText(settings.data.tax_rate)} <span className="muted small">· what a supply no rule covers is taxed at</span>
              </dd>
            </dl>
            {!sellerCountry ? (
              <Notice kind="warn">
                Without a registration country no cross-border determination is made, so a registered business abroad is invoiced at the domestic rate rather than under reverse charge.{' '}
                <Link to="/billing">Set it on Billing settings</Link>.
              </Notice>
            ) : null}
          </>
        )}
      </div>

      <div className="card">
        <div className="card-head">
          <h2>Rules</h2>
          <span className="hint">country, then region, then category, then start date</span>
        </div>
        <p className="muted small">
          The most specific rule wins: a region rule beats a country-wide one and an exact category beats the catch-all, and only among equally specific rules does the latest start date win. A rule that is
          not <b>Standard</b> charges nothing — its rate is 0 and its <b>note</b> is the sentence the invoice must carry.
        </p>
        {rulesQ.loading && !rules.length ? (
          <Skeleton lines={4} />
        ) : rules.length === 0 ? (
          <EmptyState title="No tax rules">
            Every supply is taxed at the Sovereign default rate, and a customer's own override or exemption still applies. Add a rule to tax a category differently, to change a rate on a date, or to carry
            the reverse-charge wording a registered business abroad has to be invoiced with.
          </EmptyState>
        ) : (
          <DataTable
            label="Tax rules"
            columns={ruleColumns}
            rows={rules}
            rowKey={(r) => r.id}
            defaultSort={{ key: 'validity', dir: 'desc' }}
            pageSize={20}
            csvName="tax-rules"
            emptyTitle="No tax rules"
          />
        )}
      </div>

      <div className="card">
        <div className="card-head">
          <h2>Categories by SKU</h2>
          {canManage ? (
            <button className="small" onClick={() => setDialog({ kind: 'category' })}>
              Place a SKU
            </button>
          ) : null}
        </div>
        <p className="muted small">
          Which category of supply each meter belongs to, so storage and compute can be taxed differently. A row is an exact SKU (<span className="mono">k8s.vcpu</span>) or a prefix ending in{' '}
          <span className="mono">*</span> (<span className="mono">evs.*</span> — the whole storage family); the longest matching prefix wins, and a SKU matched by nothing is the default category, which is
          the one a rule with an empty category covers.
        </p>
        {catsQ.loading && !categories.length ? (
          <Skeleton lines={3} />
        ) : categories.length === 0 ? (
          <EmptyState title="No SKU is placed in a category">
            Every meter is in the default category, and one rule per country taxes all of them the same. Place a SKU family here to tax it under its own rule.
          </EmptyState>
        ) : (
          <DataTable label="Tax categories" columns={categoryColumns} rows={categories} rowKey={(c) => c.sku} defaultSort={{ key: 'sku', dir: 'asc' }} pageSize={20} csvName="tax-categories" emptyTitle="No SKU is placed in a category" />
        )}
      </div>

      {dialog?.kind === 'rule' ? <TaxRuleModal rule={dialog.rule} categories={categoryNames} defaultCountry={sellerCountry} onClose={close} onDone={reload} /> : null}
      {dialog?.kind === 'category' ? <TaxCategoryModal category={dialog.category} categories={categoryNames} onClose={close} onDone={reload} /> : null}
      {dialog?.kind === 'delete-rule' ? (
        <Confirm
          title={`Delete ${dialog.rule.name}`}
          danger
          confirmLabel="Delete rule"
          busy={act.busy}
          onClose={close}
          onConfirm={async () => {
            const ok = await act.run(`${dialog.rule.name} deleted`, () => api.del(`/tax/rules/${dialog.rule.id}`), reload)
            if (ok) close()
          }}
          body={
            <div className="stack tight">
              <p>
                Future statement runs will not apply <b>{dialog.rule.name}</b> ({scopeText(dialog.rule)}, {rateText(dialog.rule.rate)}). Supplies it covered fall to the next most specific rule, or to the
                Sovereign default rate.
              </p>
              <p className="muted small">Invoices already rated under it keep the tax summary they recorded, naming this rule — an issued invoice is a permanent record, not a live reference.</p>
            </div>
          }
        />
      ) : null}
      {dialog?.kind === 'delete-category' ? (
        <Confirm
          title={`Return ${dialog.category.sku} to the default category`}
          danger
          confirmLabel="Delete placement"
          busy={act.busy}
          onClose={close}
          onConfirm={async () => {
            const ok = await act.run(`${dialog.category.sku} returned to the default category`, () => api.del(`/tax/categories/${encodeURIComponent(dialog.category.sku)}`), reload)
            if (ok) close()
          }}
          body={
            <p>
              <span className="mono">{dialog.category.sku}</span> leaves the category <span className="mono">{dialog.category.category}</span> and is rated by whichever rule covers the default category.
            </p>
          }
        />
      ) : null}
    </div>
  )
}

/** Create or edit one rule. The rate is TYPED as a percentage and travels as a fraction. */
export function TaxRuleModal({
  rule,
  categories,
  defaultCountry,
  onClose,
  onDone,
}: {
  rule?: TaxRule
  categories: string[]
  defaultCountry?: string
  onClose: () => void
  onDone: () => void | Promise<void>
}) {
  const [form, setForm] = useState<TaxRuleForm>(() => (rule ? taxRuleFormFrom(rule) : emptyTaxRuleForm(today(), defaultCountry ?? '')))
  const [errors, setErrors] = useState<Errors<TaxRuleForm>>({})
  const act = useAction()
  const set = <K extends keyof TaxRuleForm>(k: K, v: TaxRuleForm[K]) => setForm((f) => ({ ...f, [k]: v }))
  const standard = chargesTax(form.kind)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    const errs = validateTaxRule(form)
    setErrors(errs)
    if (hasErrors(errs)) return
    const body = taxRuleBody(form)
    const ok = await act.run(
      rule ? `${body.name} saved` : `${body.name} created`,
      () => (rule ? api.put(`/tax/rules/${rule.id}`, body) : api.post('/tax/rules', body)),
      onDone,
    )
    if (ok) onClose()
  }

  return (
    <Modal
      title={rule ? `Edit rule — ${rule.name}` : 'New tax rule'}
      onClose={onClose}
      footer={
        <>
          <button type="button" onClick={onClose} disabled={act.busy}>
            Cancel
          </button>
          <button className="primary" form="tax-rule-form" disabled={act.busy}>
            {rule ? 'Save' : 'Create rule'}
          </button>
        </>
      }
    >
      <form id="tax-rule-form" onSubmit={(e) => void submit(e)}>
        {act.error ? <Notice kind="bad">{act.error}</Notice> : null}
        <Field label="Name" error={errors.name} help="What this rule is called in the rules table and in the tax summary printed on the invoice.">
          <input value={form.name} onChange={(e) => set('name', e.target.value)} placeholder="e.g. Oman VAT standard" autoFocus />
        </Field>
        <div className="grid2">
          <Field label="Country" error={errors.country} help="ISO 3166-1 alpha-2. The buyer's registration country decides; a buyer with none is treated as domestic.">
            <input value={form.country} onChange={(e) => set('country', e.target.value.toUpperCase())} className="mono" maxLength={2} placeholder="OM" aria-label="Country" />
          </Field>
          <Field label="Region" error={errors.region} help="Empty = the whole country. Only where a country taxes by region.">
            <input value={form.region} onChange={(e) => set('region', e.target.value)} placeholder="the whole country" aria-label="Region" />
          </Field>
        </div>
        <Field label="Category" error={errors.category} help="Empty = every category. A named category only covers the SKUs placed in it on this page.">
          <input value={form.category} onChange={(e) => set('category', e.target.value)} list="tax-rule-categories" className="mono" placeholder="every category" aria-label="Category" />
          <datalist id="tax-rule-categories">
            {categories.map((c) => (
              <option key={c} value={c} />
            ))}
          </datalist>
        </Field>
        <div className="grid2">
          <Field label="Kind" error={errors.kind} help={taxKindHelp(form.kind)}>
            <select value={form.kind} onChange={(e) => setForm((f) => withKind(f, e.target.value))} aria-label="Kind">
              {TAX_KINDS.map((k) => (
                <option key={k.value} value={k.value}>
                  {k.label}
                </option>
              ))}
            </select>
          </Field>
          <Field
            label="Rate (%)"
            error={errors.rate}
            help={standard ? 'Applied to the taxable base of this category, after discounts. Type 5 for 5 %.' : `A ${taxKindLabel(form.kind).toLowerCase()} rule charges nothing, so its rate is 0.`}
          >
            <input value={form.rate} onChange={(e) => set('rate', e.target.value)} inputMode="decimal" placeholder="5" disabled={!standard} aria-label="Rate" />
          </Field>
        </div>
        <Field
          label="Note"
          error={errors.note}
          help="The sentence the invoice must carry when this rule applies — the reverse-charge wording, the exemption article, the zero-rating provision. Only the tax authority knows what it must say."
        >
          <input value={form.note} onChange={(e) => set('note', e.target.value)} placeholder={standard ? 'optional' : 'e.g. Reverse charge: the recipient is liable to account for the tax on this supply.'} aria-label="Note" />
        </Field>
        <div className="grid2">
          <Field label="Effective from" error={errors.effective_from} help="A period is rated at the rules in force on the days it covers, whatever today's rules are.">
            <input type="date" value={form.effective_from} onChange={(e) => set('effective_from', e.target.value)} aria-label="Effective from" />
          </Field>
          <Field label="Effective to" error={errors.effective_to} help="Empty = open-ended. The end day is EXCLUDED: a rule ending 2027-01-01 no longer applies on 2027-01-01.">
            <input type="date" value={form.effective_to} onChange={(e) => set('effective_to', e.target.value)} min={form.effective_from || undefined} aria-label="Effective to" />
          </Field>
        </div>
      </form>
    </Modal>
  )
}

/** Place one SKU, or one SKU family, in a category. */
export function TaxCategoryModal({ category, categories, onClose, onDone }: { category?: TaxCategoryRule; categories: string[]; onClose: () => void; onDone: () => void | Promise<void> }) {
  const [form, setForm] = useState<TaxCategoryForm>(() => (category ? taxCategoryFormFrom(category) : emptyTaxCategoryForm()))
  const [errors, setErrors] = useState<Errors<TaxCategoryForm>>({})
  const act = useAction()
  const set = <K extends keyof TaxCategoryForm>(k: K, v: TaxCategoryForm[K]) => setForm((f) => ({ ...f, [k]: v }))

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    const errs = validateTaxCategory(form)
    setErrors(errs)
    if (hasErrors(errs)) return
    const body = taxCategoryBody(form)
    const ok = await act.run(`${body.sku} placed in ${body.category}`, () => api.put('/tax/categories', body), onDone)
    if (ok) onClose()
  }

  return (
    <Modal
      title={category ? `Edit placement — ${category.sku}` : 'Place a SKU in a category'}
      onClose={onClose}
      footer={
        <>
          <button type="button" onClick={onClose} disabled={act.busy}>
            Cancel
          </button>
          <button className="primary" form="tax-category-form" disabled={act.busy}>
            {category ? 'Save' : 'Place SKU'}
          </button>
        </>
      }
    >
      <form id="tax-category-form" onSubmit={(e) => void submit(e)}>
        {act.error ? <Notice kind="bad">{act.error}</Notice> : null}
        <Field
          label="SKU"
          error={errors.sku}
          help="An exact SKU (k8s.vcpu) or a PREFIX ending in * (evs.* — the whole storage family). The longest matching prefix wins, so evs.ssd.* beats evs.*."
        >
          <input value={form.sku} onChange={(e) => set('sku', e.target.value)} className="mono" placeholder="evs.*" disabled={Boolean(category)} autoFocus={!category} aria-label="SKU" />
        </Field>
        <Field label="Category" error={errors.category} help="The category a rule names. A SKU placed in no category is in the default one, which a rule with an empty category covers.">
          <input value={form.category} onChange={(e) => set('category', e.target.value)} list="tax-category-names" className="mono" placeholder="storage" autoFocus={Boolean(category)} aria-label="Category" />
          <datalist id="tax-category-names">
            {categories.map((c) => (
              <option key={c} value={c} />
            ))}
          </datalist>
        </Field>
        <Field label="Note" error={errors.note} help="Why this family sits in this category — read by whoever revisits the placement, never printed on an invoice.">
          <input value={form.note} onChange={(e) => set('note', e.target.value)} placeholder="optional" aria-label="Placement note" />
        </Field>
      </form>
    </Modal>
  )
}
