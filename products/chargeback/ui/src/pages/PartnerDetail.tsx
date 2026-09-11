import { useMemo, useState, type FormEvent } from 'react'
import { Link, useParams, useSearchParams } from 'react-router-dom'
import { api, asList, errorText } from '../api/client'
import type { Customer, MarginReport, Partner, PriceBook, RetailDocument, RetailOverride, RoleBinding, Statement } from '../api/types'
import { useSession } from '../auth/session'
import { DataTable, type Column } from '../components/DataTable'
import { Badge, Confirm, EmptyState, Field, KPI, Notice, PageHeader, Segmented, Skeleton, Tabs } from '../components/ui'
import { canPartner } from '../lib/access'
import { day, when } from '../lib/format'
import { formatMoney, formatPct } from '../lib/money'
import { toNumber } from '../lib/num'
import { PARTNER_ROLES, roleLabel } from '../lib/access'
import {
  RETAIL_BASES,
  belowBuyOf,
  billToHelp,
  billToLabel,
  emptyOverride,
  marginByCustomer,
  marginTable,
  marginTotals,
  partnerTerms,
  previewRetail,
  validateRetailRule,
  type MarginTableRow,
  type RetailPreviewRow,
} from '../lib/partners'
import { statementPeriod, statementStatus } from '../lib/statements'
import { useAction } from '../lib/useAction'
import { useQuery } from '../lib/useQuery'
import { AccountPanel } from '../panels/AccountPanel'

/**
 * One partner (DESIGN.md §11) — the same bodies under both lenses: the
 * Sovereign opens it from Configure → Partners, a partner owner sees them as
 * its own pages. Every figure is derived: margin is customer net minus
 * partner buy, and a retail price is a base times a markup.
 */

const TABS = ['Overview', 'Customers', 'Statements', 'Margin', 'Retail rule', 'Users', 'Account']

export function PartnerDetail() {
  const { id = '' } = useParams()
  const [params] = useSearchParams()
  const tab = (params.get('tab') ?? 'overview').toLowerCase()
  const q = useQuery<Partner>(`/partners/${id}`)
  const p = q.data

  if (q.error && !p) {
    return (
      <div className="stack">
        <PageHeader title="Partner" crumbs={[{ to: '/partners', label: 'Partners' }, { label: id }]} />
        <Notice kind="bad">{q.error}</Notice>
      </div>
    )
  }
  if (!p) return <Skeleton lines={6} />
  const base = `/partners/${id}`
  return (
    <div className="stack">
      <PageHeader
        crumbs={[{ to: '/partners', label: 'Partners' }, { label: p.name }]}
        title={
          <span className="row" style={{ gap: 10 }}>
            {p.name} <Badge status={p.status} />
          </span>
        }
        sub={
          <>
            <span className="mono">{p.slug}</span> · {partnerTerms(p)}
            {p.contact_email ? ` · ${p.contact_email}` : ''}
          </>
        }
      />
      <PartnerKPIs partner={p} />
      <Tabs base={base} tabs={TABS} current={tab} counts={{ customers: p.customer_count }} />
      {tab === 'overview' ? <PartnerOverview partner={p} /> : null}
      {tab === 'customers' ? <PartnerCustomers partnerId={id} /> : null}
      {tab === 'statements' ? <PartnerStatements partnerId={id} /> : null}
      {tab === 'margin' ? <PartnerMargin partnerId={id} /> : null}
      {tab === 'retail rule' ? <RetailRuleEditor partner={p} onSaved={q.reload} /> : null}
      {tab === 'users' ? <PartnerUsers partnerId={id} /> : null}
      {tab === 'account' ? <PartnerAccount partner={p} /> : null}
      {!TABS.some((t) => t.toLowerCase() === tab) ? <Notice kind="warn">Unknown tab "{tab}".</Notice> : null}
    </div>
  )
}

/** The strip every partner page opens with: what it buys on and what it owes. */
export function PartnerKPIs({ partner }: { partner: Partner }) {
  const balance = toNumber(partner.balance ?? 0)
  return (
    <div className="kpis">
      <KPI label="Billing model" value={billToLabel(partner.bill_to)} note={billToHelp(partner.bill_to)} />
      <KPI
        label="Buy price set by"
        value={partner.tier_name || (partner.commission_pct !== null && partner.commission_pct !== undefined ? `${toNumber(partner.commission_pct)} % commission` : 'no tier')}
        note={partner.tier_name ? 'a tier is a percentage off the list price' : 'without a tier the buy price is the list price'}
      />
      <KPI label="Customers" value={partner.customer_count ?? 0} note="end customers assigned to this partner" />
      <KPI
        label="Account balance"
        value={formatMoney(Math.abs(balance), '')}
        note={balance > 0 ? 'owed by the partner' : balance < 0 ? 'in credit — owed to the partner' : 'settled'}
        tone={balance > 0 ? 'bad' : undefined}
      />
    </div>
  )
}

function PartnerOverview({ partner }: { partner: Partner }) {
  return (
    <div className="card">
      <h2>How this partner is billed</h2>
      {partner.bill_to === 'partner' ? (
        <>
          <p>
            <b>Resell.</b> Each period we invoice <b>{partner.name}</b> a <b>wholesale statement</b>: every one of its customers' lines at the Sovereign list price, less its tier
            {partner.tier_name ? ` (${partner.tier_name})` : ''}, grouped by end customer. Its customers are priced from its own <Link to={`/partners/${partner.id}?tab=retail%20rule`}>retail book</Link>, derived from its rule — the partner bills them itself.
          </p>
          <p className="muted">
            The margin the partner keeps is what its customers pay less what it pays us. It is derived per line and never typed in: change the list price, the tier or the retail rule, and every figure follows.
          </p>
        </>
      ) : (
        <>
          <p>
            <b>Agent.</b> We invoice this partner's end customers ourselves, at our own books — nothing about their bill changes because <b>{partner.name}</b> introduced them. Each period the partner is credited a <b>commission statement</b>: one line per end customer, the customer net less the partner buy
            {partner.tier_name ? ` (its tier ${partner.tier_name} sets that buy price)` : partner.commission_pct !== null && partner.commission_pct !== undefined ? ` (${toNumber(partner.commission_pct)} % of the customer net)` : ''}.
          </p>
          <p className="muted">Issuing it credits the partner's account: money we owe, so the balance reads in credit. Nothing is collected on it.</p>
        </>
      )}
      <p className="muted tiny">
        Its account, invoices, payments and collections are the ones any party has — the partner owns a customer record of its own ({partner.party_customer_id ? <span className="mono">{partner.party_customer_id.slice(0, 8)}</span> : 'its party'}), so there is one ledger, not two.
      </p>
    </div>
  )
}

/** The end customers assigned to the partner. */
export function PartnerCustomers({ partnerId, lensBase = '/customers' }: { partnerId: string; lensBase?: string }) {
  const q = useQuery<unknown>(`/partners/${partnerId}/customers`)
  const rows = useMemo(() => asList<Customer>(q.data, 'customers'), [q.data])
  const columns: Column<Customer>[] = [
    {
      key: 'name',
      header: 'Customer',
      value: (c) => c.name,
      render: (c) => (
        <Link to={`${lensBase}/${c.id}`}>
          {c.name}
          <span className="sub mono">{c.slug}</span>
        </Link>
      ),
    },
    { key: 'status', header: 'Status', value: (c) => c.status, render: (c) => <Badge status={c.status} /> },
    { key: 'admin', header: 'Admin', value: (c) => c.admin_email },
    {
      key: 'balance',
      header: 'Balance',
      value: (c) => toNumber(c.balance ?? 0),
      numeric: true,
      render: (c) => (c.balance === undefined ? <span className="muted">—</span> : formatMoney(toNumber(c.balance), '')),
    },
    { key: 'statement', header: 'Last statement', value: (c) => c.last_statement_period ?? '', render: (c) => c.last_statement_period ?? <span className="muted">none yet</span> },
  ]
  if (q.error) return <Notice kind="bad">{q.error}</Notice>
  if (!q.data) return <Skeleton lines={4} />
  return (
    <div className="card pad-0">
      <DataTable
        label="Partner customers"
        columns={columns}
        rows={rows}
        rowKey={(c) => c.id}
        defaultSort={{ key: 'name', dir: 'asc' }}
        csvName="partner-customers"
        emptyTitle="No customers yet"
        emptyBody="Assign a customer to this partner from the customer's page."
      />
    </div>
  )
}

/** The partner's OWN statements: wholesale (resell) or commission (agent). */
export function PartnerStatements({ partnerId }: { partnerId: string }) {
  const q = useQuery<unknown>(`/partners/${partnerId}/statements`)
  const rows = useMemo(() => asList<Statement>(q.data, 'statements'), [q.data])
  const columns: Column<Statement>[] = [
    { key: 'period', header: 'Period', value: (s) => s.period_start, render: (s) => <Link to={`/statements/${s.id}`}>{statementPeriod(s)}</Link> },
    {
      key: 'kind',
      header: 'Kind',
      value: (s) => s.statement_kind ?? '',
      render: (s) => <Badge status={s.statement_kind === 'commission' ? 'commission' : 'wholesale'} kind="info" />,
    },
    { key: 'status', header: 'Status', value: (s) => statementStatus(s), render: (s) => <Badge status={statementStatus(s)} /> },
    { key: 'invoice', header: 'Invoice', value: (s) => s.invoice_number ?? '', render: (s) => (s.invoice_number ? <span className="mono">{s.invoice_number}</span> : <span className="muted">not issued</span>) },
    { key: 'total', header: 'Total', value: (s) => toNumber(s.total), numeric: true, render: (s) => formatMoney(toNumber(s.total), s.currency) },
    {
      key: 'margin',
      header: 'Margin',
      value: (s) => toNumber(s.margin_total ?? 0),
      numeric: true,
      render: (s) => (s.margin_total === undefined || s.margin_total === null ? <span className="muted">—</span> : formatMoney(toNumber(s.margin_total), s.currency)),
    },
    { key: 'issued', header: 'Issued', value: (s) => s.issued_at ?? '', render: (s) => (s.issued_at ? when(s.issued_at) : <span className="muted">—</span>) },
  ]
  if (q.error) return <Notice kind="bad">{q.error}</Notice>
  if (!q.data) return <Skeleton lines={4} />
  return (
    <div className="card pad-0">
      <DataTable
        label="Partner statements"
        columns={columns}
        rows={rows}
        rowKey={(s) => s.id}
        defaultSort={{ key: 'period', dir: 'desc' }}
        csvName="partner-statements"
        emptyTitle="No partner statement yet"
        emptyBody="A statement run writes one per period, from the lines of this partner's customers."
        footNote="a wholesale statement is what the partner pays us; a commission statement is what we owe the partner"
      />
    </div>
  )
}

const PERIOD_RE = /^\d{4}-(0[1-9]|1[0-2])$/

/** The margin report: per customer per service, net, buy, margin, margin %. */
export function PartnerMargin({ partnerId }: { partnerId: string }) {
  const [params, setParams] = useSearchParams()
  const period = PERIOD_RE.test(params.get('period') ?? '') ? params.get('period')! : new Date().toISOString().slice(0, 7)
  const [group, setGroup] = useState<'service' | 'customer'>('service')
  const q = useQuery<MarginReport>(`/partners/${partnerId}/margin?period=${encodeURIComponent(period)}`)
  const all = useMemo(() => marginTable(q.data), [q.data])
  const rows = group === 'customer' ? marginByCustomer(all) : all
  const totals = marginTotals(all)
  const cur = q.data?.currency ?? ''
  const money = (v: number) => formatMoney(v, cur)

  const columns: Column<MarginTableRow>[] = [
    { key: 'customer', header: 'Customer', value: (r) => r.customer },
    { key: 'service', header: 'Service', value: (r) => r.service, render: (r) => <span className="mono">{r.service}</span> },
    { key: 'net', header: 'Customer net', value: (r) => r.net, numeric: true, render: (r) => money(r.net), total: () => money(totals.net) },
    { key: 'buy', header: 'Partner buy', value: (r) => r.buy, numeric: true, render: (r) => money(r.buy), total: () => money(totals.buy) },
    {
      key: 'margin',
      header: 'Margin',
      value: (r) => r.margin,
      numeric: true,
      render: (r) => <span className={r.margin < 0 ? 'bad' : r.margin > 0 ? 'ok' : ''}>{money(r.margin)}</span>,
      total: () => money(totals.margin),
    },
    {
      key: 'pct',
      header: 'Margin %',
      value: (r) => r.pct ?? 0,
      numeric: true,
      render: (r) => (r.pct === null ? <span className="muted">—</span> : formatPct(r.pct)),
      total: () => (totals.pct === null ? '—' : formatPct(totals.pct)),
    },
  ]

  return (
    <div className="stack">
      <div className="toolbar">
        <Field label="Period">
          <input
            type="month"
            value={period}
            onChange={(e) => {
              const p = new URLSearchParams(params)
              p.set('period', e.target.value)
              setParams(p, { replace: true })
            }}
          />
        </Field>
        <Field label="Group by">
          <Segmented<'service' | 'customer'>
            value={group}
            onChange={setGroup}
            options={[
              { value: 'service', label: 'Customer · service' },
              { value: 'customer', label: 'Customer' },
            ]}
            ariaLabel="Group by"
          />
        </Field>
      </div>
      {q.error ? <Notice kind="bad">{q.error}</Notice> : null}
      <div className="kpis">
        <KPI label="Customer net" value={money(totals.net)} note="what the end customers pay" />
        <KPI label="Partner buy" value={money(totals.buy)} note="what the partner pays us" />
        <KPI label="Margin" value={money(totals.margin)} note="net − buy, derived per line" tone={totals.margin < 0 ? 'bad' : undefined} />
        <KPI label="Margin %" value={totals.pct === null ? '—' : formatPct(totals.pct)} note="of the customer net" />
      </div>
      <div className="card pad-0">
        <DataTable
          label="Margin"
          columns={columns}
          rows={rows}
          rowKey={(r) => `${r.customerId}|${r.service}`}
          defaultSort={{ key: 'margin', dir: 'desc' }}
          csvName={`margin-${period}`}
          emptyTitle="Nothing rated in this period"
          emptyBody="The margin is read from the figures frozen on this partner's customers' statements; run the period first."
          footNote="every figure comes from the statements of the period, never from today's prices"
        />
      </div>
    </div>
  )
}

/** The partner's own account — the party's ledger, through the one panel. */
export function PartnerAccount({ partner }: { partner: Partner }) {
  const { me } = useSession()
  const q = useQuery<Customer>(partner.party_customer_id ? `/customers/${partner.party_customer_id}` : null)
  if (!partner.party_customer_id) return <Notice kind="warn">This partner has no account record.</Notice>
  if (q.error && !q.data) return <Notice kind="bad">{q.error}</Notice>
  if (!q.data) return <Skeleton lines={4} />
  return (
    <div className="stack">
      <Notice kind="info">
        A partner is a party like any other: this is the same ledger a customer has — invoices, payments, credit notes and collections — on <b>{partner.name}</b>'s own account.
      </Notice>
      <AccountPanel
        customerId={partner.party_customer_id}
        customer={q.data}
        canRecord={canPartner(me, 'billing.collect', partner.id)}
        canCheckout={canPartner(me, 'account.topup', partner.id) || canPartner(me, 'billing.collect', partner.id)}
        canApplyCredit={canPartner(me, 'billing.collect', partner.id)}
        onChanged={q.reload}
      />
    </div>
  )
}

/** The partner's users: who signs in as this partner, and as what. */
export function PartnerUsers({ partnerId }: { partnerId: string }) {
  const { me } = useSession()
  const q = useQuery<unknown>(`/partners/${partnerId}/users`)
  const rows = useMemo(() => asList<RoleBinding>(q.data, 'users'), [q.data])
  const canManage = canPartner(me, 'partner.self.manage', partnerId)
  const act = useAction()
  const [email, setEmail] = useState('')
  const [role, setRole] = useState<'partner-owner' | 'partner-viewer'>('partner-viewer')
  const [removing, setRemoving] = useState<RoleBinding | null>(null)

  const add = async (e: FormEvent) => {
    e.preventDefault()
    const v = email.trim().toLowerCase()
    if (!v) return
    const ok = await act.run(`${v} added as ${roleLabel(role)}`, () => api.post(`/partners/${partnerId}/users`, { email: v, role }), q.reload)
    if (ok) setEmail('')
  }

  const columns: Column<RoleBinding>[] = [
    { key: 'email', header: 'Email', value: (u) => u.subject_email, render: (u) => <span className="mono">{u.subject_email}</span> },
    { key: 'role', header: 'Role', value: (u) => String(u.role), render: (u) => <Badge status={roleLabel(String(u.role))} kind={u.role === 'partner-owner' ? 'info' : undefined} /> },
    { key: 'granted', header: 'Granted', value: (u) => u.granted_at ?? '', render: (u) => <span className="muted">{u.granted_by ? `${u.granted_by} · ` : ''}{u.granted_at ? when(u.granted_at) : ''}</span> },
    {
      key: 'actions',
      header: '',
      value: () => '',
      sortable: false,
      className: 'nowrap actions',
      render: (u) =>
        canManage ? (
          <button className="link small danger" disabled={act.busy} onClick={() => setRemoving(u)}>
            Remove
          </button>
        ) : null,
    },
  ]

  return (
    <div className="stack">
      {act.error ? <Notice kind="bad">{act.error}</Notice> : null}
      {act.ok ? <Notice kind="ok">{act.ok}</Notice> : null}
      {q.error ? <Notice kind="bad">{q.error}</Notice> : null}
      <div className="card pad-0">
        <DataTable
          label="Partner users"
          columns={columns}
          rows={rows}
          rowKey={(u) => u.id ?? u.subject_email}
          emptyTitle="No users yet"
          emptyBody="Nobody can sign in as this partner until an address is added here."
        />
      </div>
      {canManage ? (
        <form className="card" onSubmit={(e) => void add(e)} aria-label="Add a partner user">
          <h2>Add a user</h2>
          <div className="row gap">
            <Field label="Email">
              <input value={email} onChange={(e) => setEmail(e.target.value)} placeholder="name@partner.example" />
            </Field>
            <Field label="Role" help={PARTNER_ROLES.find((r) => r.role === role)?.help}>
              <select value={role} onChange={(e) => setRole(e.target.value as 'partner-owner' | 'partner-viewer')}>
                {PARTNER_ROLES.map((r) => (
                  <option key={r.role} value={r.role}>
                    {r.label}
                  </option>
                ))}
              </select>
            </Field>
            <button className="primary" disabled={act.busy || !email.trim()}>
              Add
            </button>
          </div>
        </form>
      ) : null}
      {removing ? (
        <Confirm
          title={`Remove ${removing.subject_email}`}
          danger
          confirmLabel="Remove"
          busy={act.busy}
          onClose={() => setRemoving(null)}
          onConfirm={async () => {
            const ok = await act.run(`${removing.subject_email} removed`, () => api.del(`/partners/${partnerId}/users/${encodeURIComponent(removing.subject_email)}`), q.reload)
            if (ok) setRemoving(null)
          }}
          body={<>They lose access to this partner at their next request. Nothing else about the partner changes.</>}
        />
      ) : null}
    </div>
  )
}

/**
 * The retail rule (DESIGN.md §11.4): a base, a markup and its overrides. The
 * preview shows what the rule makes of today's list prices BEFORE saving;
 * saving re-derives the book on the server and replaces the preview with it,
 * with every line priced below the partner's own buy price called out.
 */
export function RetailRuleEditor({ partner, onSaved }: { partner: Partner; onSaved?: () => void | Promise<void> }) {
  const { me } = useSession()
  const canEdit = canPartner(me, 'partner.self.manage', partner.id)
  const doc = useQuery<RetailDocument>(`/partners/${partner.id}/retail-book`)
  const books = useQuery<unknown>(canPartner(me, 'partners.manage', partner.id) ? '/pricebooks' : null)
  const act = useAction()
  // The form is the EDIT, not the state: until a field is touched the stored
  // rule is what the editor shows, so an arriving document needs no effect
  // and no render-phase state change to seed it.
  const [edit, setEdit] = useState<{ base: 'list' | 'buy'; markup: string; overrides: RetailOverride[] } | null>(null)
  const [saved, setSaved] = useState<RetailDocument | null>(null)
  const rule = doc.data?.retail_rule
  const base = edit?.base ?? (rule?.base === 'list' ? 'list' : 'buy')
  const markup = edit?.markup ?? (rule ? String(toNumber(rule.markup_pct)) : '5')
  const overrides = edit?.overrides ?? (rule?.overrides ?? []).map((o) => ({ ...o }))
  const setBase = (v: 'list' | 'buy') => setEdit({ base: v, markup, overrides })
  const setMarkup = (v: string) => setEdit({ base, markup: v, overrides })
  const setOverrides = (v: RetailOverride[]) => setEdit({ base, markup, overrides: v })

  const current = saved ?? doc.data
  const derived = current?.books ?? []
  // The tier percentage the preview works from: read back from the derived
  // book (list − buy over list) so the preview and the server agree even
  // when a tier holds several rules.
  const listItems = useMemo(() => {
    const all = asList<PriceBook>(books.data, 'pricebooks', 'price_books').filter((b) => !b.derived_from_rule)
    const ids = new Set(derived.map((d) => d.list_book_id))
    const chosen = all.filter((b) => ids.has(b.id))
    return (chosen.length ? chosen : all).flatMap((b) => b.items ?? [])
  }, [books.data, derived])
  const tierPct = useMemo(() => {
    for (const d of derived) {
      for (const bb of d.below_buy ?? []) {
        const list = toNumber(bb.list_unit_price)
        if (list > 0) return ((list - toNumber(bb.buy_unit_price)) / list) * 100
      }
    }
    return 0
  }, [derived])
  const preview: RetailPreviewRow[] = useMemo(
    () => (listItems.length ? previewRetail(listItems, tierPct, { base, markup_pct: markup, overrides }) : []),
    [listItems, tierPct, base, markup, overrides],
  )
  const previewBelow = belowBuyOf(preview)
  const below = current?.below_buy ?? []
  const invalid = validateRetailRule({ base, markup_pct: markup, overrides })

  const save = async () => {
    if (invalid) {
      act.setError(invalid)
      return
    }
    await act.run('retail rule saved — the retail book was re-derived', async () => {
      setSaved(await api.put<RetailDocument>(`/partners/${partner.id}/retail-rule`, { base, markup_pct: markup, overrides }))
      await doc.reload()
      if (onSaved) await onSaved()
    })
  }

  if (partner.bill_to !== 'partner') {
    return (
      <Notice kind="info">
        {doc.data?.note ?? 'This partner is an agent: we invoice its end customers at our own books and credit it a commission, so there is no retail book.'}
      </Notice>
    )
  }

  return (
    <div className="stack">
      {act.error ? <Notice kind="bad">{act.error}</Notice> : null}
      {act.ok ? <Notice kind="ok">{act.ok}</Notice> : null}
      {doc.error ? <Notice kind="bad">{doc.error}</Notice> : null}

      <div className="card">
        <h2>Retail rule</h2>
        <p className="muted">
          The partner's retail book is <b>derived</b>, never typed: a base times a markup, per SKU. Change the list price, the tier or this rule and every retail price follows — so there is one price to keep true, not two.
        </p>
        <div className="row gap">
          <Field label="Base" help={RETAIL_BASES.find((b) => b.value === base)?.help}>
            <Segmented<'list' | 'buy'> value={base} onChange={setBase} options={RETAIL_BASES.map((b) => ({ value: b.value, label: b.label }))} ariaLabel="Retail base" />
          </Field>
          <Field label="Markup %" help="applied to the base; a negative markup sells below it">
            <input value={markup} onChange={(e) => setMarkup(e.target.value)} inputMode="decimal" aria-label="Markup percent" disabled={!canEdit} />
          </Field>
        </div>

        <h3>Overrides</h3>
        <p className="muted tiny">Most specific wins: a SKU beats its service, and a service beats the markup above.</p>
        {overrides.length === 0 ? <p className="muted">No override — the markup above applies to every SKU.</p> : null}
        {overrides.map((o, i) => (
          <div className="row gap" key={i}>
            <Field label="Scope">
              <select
                value={o.scope}
                disabled={!canEdit}
                onChange={(e) => setOverrides(overrides.map((x, k) => (k === i ? { ...x, scope: e.target.value } : x)))}
                aria-label={`Override ${i + 1} scope`}
              >
                <option value="service">Service</option>
                <option value="sku">SKU</option>
              </select>
            </Field>
            <Field label={o.scope === 'sku' ? 'SKU' : 'Service'}>
              <input
                value={o.key}
                disabled={!canEdit}
                placeholder={o.scope === 'sku' ? 'ecs.m7n.xlarge.8' : 'ecs'}
                onChange={(e) => setOverrides(overrides.map((x, k) => (k === i ? { ...x, key: e.target.value } : x)))}
                aria-label={`Override ${i + 1} key`}
              />
            </Field>
            <Field label="Markup %">
              <input
                value={String(o.markup_pct)}
                disabled={!canEdit}
                inputMode="decimal"
                onChange={(e) => setOverrides(overrides.map((x, k) => (k === i ? { ...x, markup_pct: e.target.value } : x)))}
                aria-label={`Override ${i + 1} markup`}
              />
            </Field>
            {canEdit ? (
              <button className="link small danger" onClick={() => setOverrides(overrides.filter((_, k) => k !== i))}>
                Remove
              </button>
            ) : null}
          </div>
        ))}
        {canEdit ? (
          <div className="btn-row">
            <button onClick={() => setOverrides([...overrides, emptyOverride()])}>Add override</button>
            <button className="primary" onClick={() => void save()} disabled={act.busy || Boolean(invalid)}>
              Save and re-derive
            </button>
          </div>
        ) : (
          <Notice kind="info">An owner of this partner edits the rule; you can see what it derives.</Notice>
        )}
        {invalid ? <Notice kind="warn">{invalid}</Notice> : null}
      </div>

      {previewBelow.length > 0 && !act.ok ? (
        <Notice kind="warn">
          {previewBelow.length === 1 ? 'One line' : `${previewBelow.length} lines`} would price <b>below the buy price</b> — the partner would resell at a loss on <span className="mono">{previewBelow.map((b) => b.sku).join(', ')}</span>. Saving is allowed; the prices are the partner's to set.
        </Notice>
      ) : null}
      {below.length > 0 ? (
        <Notice kind="warn">
          The derived book prices {below.length === 1 ? 'one line' : `${below.length} lines`} <b>below the buy price</b>: <span className="mono">{below.map((b) => b.sku).join(', ')}</span>.
        </Notice>
      ) : null}

      {preview.length > 0 ? <RetailPreviewTable rows={preview} title="Preview" note="what this rule makes of today's list prices — saved, the server derives the same book" /> : null}

      {derived.map((d) => (
        <div className="card pad-0" key={d.book.id}>
          <div className="card-head" style={{ padding: '12px 12px 0' }}>
            <h2>{d.book.name}</h2>
            <span className="hint">derived from {d.list_book_name} · read-only — change the rule, the tier or the list book</span>
          </div>
          <table>
            <thead>
              <tr>
                <th>SKU</th>
                <th>Unit</th>
                <th className="num">Retail unit price</th>
              </tr>
            </thead>
            <tbody>
              {(d.book.items ?? []).map((it) => (
                <tr key={it.sku}>
                  <td className="mono">{it.sku}</td>
                  <td>{it.unit}</td>
                  <td className="num">{formatMoney(toNumber(it.unit_price), d.book.currency, { digits: 8 })}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ))}
      {derived.length === 0 && current?.note ? <EmptyState title="No retail book yet">{current.note}</EmptyState> : null}
      {rule?.updated_at ? <p className="muted tiny">Rule last saved {day(rule.updated_at)}.</p> : null}
    </div>
  )
}

function RetailPreviewTable({ rows, title, note }: { rows: RetailPreviewRow[]; title: string; note: string }) {
  return (
    <div className="card pad-0">
      <div className="card-head" style={{ padding: '12px 12px 0' }}>
        <h2>{title}</h2>
        <span className="hint">{note}</span>
      </div>
      <table aria-label="Retail preview">
        <thead>
          <tr>
            <th>SKU</th>
            <th className="num">List</th>
            <th className="num">Buy</th>
            <th className="num">Retail</th>
            <th className="num">Margin per unit</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((r) => (
            <tr key={r.sku} className={r.belowBuy ? 'warn' : undefined}>
              <td className="mono">{r.sku}</td>
              <td className="num">{formatMoney(r.list, '', { digits: 8 })}</td>
              <td className="num">{formatMoney(r.buy, '', { digits: 8 })}</td>
              <td className="num">{formatMoney(r.retail, '', { digits: 8 })}</td>
              <td className={`num ${r.margin < 0 ? 'bad' : r.margin > 0 ? 'ok' : ''}`}>
                {formatMoney(r.margin, '', { digits: 8 })}
                {r.belowBuy ? <span className="sub">below the buy price</span> : null}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

export function errorTextOf(e: unknown): string {
  return errorText(e)
}
