import { useState } from 'react'
import { Link, useParams, useSearchParams } from 'react-router-dom'
import { api, asList } from '../api/client'
import type { CostSource, Customer, CustomerUser, InviteIssued, PriceBook, Summary } from '../api/types'
import { Badge, Confirm, Delta, KPI, Notice, PageHeader, Skeleton, Tabs } from '../components/ui'
import { suspensionText } from '../lib/account'
import { commercialLabel, planLabel, priceBookCurrency } from '../lib/customers'
import { sourcesByLayerText } from '../lib/layers'
import { day, when } from '../lib/format'
import { formatMoney } from '../lib/money'
import { customerLens } from '../lib/scope'
import { readKPIs } from '../lib/summary'
import { useAction } from '../lib/useAction'
import { useQuery } from '../lib/useQuery'
import { AccountPanel } from '../panels/AccountPanel'
import { AuditPanel } from '../panels/AuditPanel'
import { BudgetsPanel } from '../panels/BudgetsPanel'
import { DiscountsPanel } from '../panels/DiscountsPanel'
import { DeleteCustomerConfirm, SettingsPanel } from '../panels/SettingsPanel'
import { SourcesPanel } from '../panels/SourcesPanel'
import { StatementsPanel } from '../panels/StatementsPanel'
import { UsersPanel } from '../panels/UsersPanel'
import { CustomerCostExplorer } from './CostExplorer'
import { CustomerOverview } from './Overview'
import { ResourcesBody } from './Resources'

const TABS = ['Overview', 'Account', 'Cost', 'Resources', 'Statements', 'Discounts', 'Budgets', 'Sources', 'Users', 'Settings', 'Audit']

/**
 * Customer detail — the account view (#6867, DESIGN.md §2.4): header with
 * the billing setup and the lifecycle actions, a KPI strip from the
 * customer's cost summary, and one tab per concern. The tab lives in the
 * URL (?tab=) so every view is a link.
 */
export function CustomerDetail() {
  const { id = '' } = useParams()
  const [params] = useSearchParams()
  const tab = (params.get('tab') ?? 'overview').toLowerCase()
  const cust = useQuery<Customer>(`/customers/${id}`)
  const src = useQuery<unknown>(`/customers/${id}/sources`)
  const usr = useQuery<unknown>(`/customers/${id}/users`)
  const sum = useQuery<Summary>(`/customers/${id}/cost/summary`)
  const books = useQuery<unknown>('/pricebooks')
  const act = useAction()
  const [dialog, setDialog] = useState<'suspend' | 'resume' | 'delete' | null>(null)
  const [invite, setInvite] = useState<InviteIssued | null>(null)

  if (cust.error && !cust.data) {
    return (
      <div className="stack">
        <PageHeader title="Customer" crumbs={[{ to: '/customers', label: 'Customers' }, { label: id }]} />
        <Notice kind="bad">{cust.error}</Notice>
      </div>
    )
  }
  if (!cust.data) return <Skeleton lines={6} />
  const c = cust.data
  const sources = asList<CostSource>(src.data, 'sources')
  const users = asList<CustomerUser>(usr.data, 'users')
  const bookRows = asList<PriceBook>(books.data, 'pricebooks', 'price_books')
  // The book belongs to the source (DESIGN.md §2): a source with none rates
  // its usage to zero, and that is what the header warns about.
  const unbooked = sources.filter((s) => !s.internal && !s.price_book_id)
  const k = sum.data ? readKPIs(sum.data) : null
  const currency = k?.currency || priceBookCurrency(bookRows, sources.find((s) => s.price_book_id)?.price_book_id) || ''
  const money = (v: number | null | undefined) => formatMoney(v, currency, { compact: true })
  const base = `/customers/${id}`
  const verified = sources.filter((s) => s.status === 'verified').length

  const setStatus = async (status: 'active' | 'suspended') => {
    const ok = await act.run(`${c.name} is now ${status}`, () => api.patch(`/customers/${id}`, { status }), cust.reload)
    if (ok) setDialog(null)
  }
  const sendInvite = () =>
    act.run(`invite sent to ${c.admin_email}`, async () => {
      setInvite(await api.post<InviteIssued>(`/customers/${id}/invite`))
    })

  return (
    <div className="stack">
      <PageHeader
        crumbs={[{ to: '/customers', label: 'Customers' }, { label: c.name }]}
        title={
          <span className="row" style={{ gap: 10 }}>
            {c.name} <Badge status={c.status} />
          </span>
        }
        sub={
          <>
            <span className="mono">{c.slug}</span> · {c.kind === 'organization' ? 'Organization' : 'external'} · {commercialLabel(c)}
            {planLabel(c.plan_slug) ? ` · ${planLabel(c.plan_slug)}` : ''} ·{' '}
            {/* The price book is a property of each SOURCE (DESIGN.md §2), so
                the header counts the sources per layer and links to the tab
                where their books are assigned. */}
            <Link to={`${base}?tab=sources`} className={unbooked.length ? 'warn' : undefined}>
              {sourcesByLayerText(c)} source{sources.length === 1 ? '' : 's'}
            </Link>
            {c.start_date ? ` · from ${day(c.start_date)}` : ''} · {c.admin_email}
          </>
        }
        actions={
          <>
            <button onClick={() => void sendInvite()} disabled={act.busy} title={c.status === 'pending' ? 'Send the activation invite' : 'Re-send the sign-in invite'}>
              {c.status === 'pending' ? 'Invite' : 'Re-invite'}
            </button>
            {c.status === 'suspended' ? (
              <button onClick={() => setDialog('resume')} disabled={act.busy}>
                Resume
              </button>
            ) : (
              <button onClick={() => setDialog('suspend')} disabled={act.busy}>
                Suspend
              </button>
            )}
            <Link to={`${base}?tab=settings`}>
              <button>Edit</button>
            </Link>
            <button className="danger" onClick={() => setDialog('delete')} disabled={act.busy}>
              Delete
            </button>
          </>
        }
      />

      {act.error ? <Notice kind="bad">{act.error}</Notice> : null}
      {act.ok ? <Notice kind="ok">{act.ok}</Notice> : null}
      {invite ? (
        <Notice kind="ok">
          Invite sent to {c.admin_email}, valid until {when(invite.expires_at)}. <span className="mono small">{invite.invite_url}</span>
        </Notice>
      ) : null}
      {c.platform_suspended_at ? (
        <Notice kind="bad">
          {c.name} is {suspensionText({ suspended_at: c.platform_suspended_at, source: c.suspension_source ?? '', reason: c.suspension_reason })}.{' '}
          <Link to="/collections">Open Collections</Link> to resume once settled.
        </Notice>
      ) : null}
      {c.status === 'pending' ? (
        <Notice kind="info">
          Pending — nothing is collected until the admin activates the invite. {verified === 0 && src.data ? 'No source is verified yet either.' : ''}
        </Notice>
      ) : c.status === 'active' && src.data && verified === 0 ? (
        <Notice kind="warn">
          Active but no verified cost source — nothing is collected.{' '}
          <Link to={`${base}?tab=sources`}>
            {sources.length ? 'Verify a source' : 'Add a source'}
          </Link>
          .
        </Notice>
      ) : null}
      {unbooked.length > 0 && c.status !== 'suspended' ? (
        <Notice kind="warn">
          {unbooked.length === 1 ? 'One source has' : `${unbooked.length} sources have`} no price book —{' '}
          <span className="mono">{unbooked.map((s) => s.project_id || s.kind).join(', ')}</span>: their usage is collected but every cost shows as 0.{' '}
          <Link to={`${base}?tab=sources`}>Assign a rate card</Link>.
        </Notice>
      ) : null}

      {sum.error ? (
        <Notice kind="bad">Cost summary unavailable: {sum.error}</Notice>
      ) : k ? (
        <div className="kpis">
          <KPI label="Month to date" value={money(k.mtd)} note={<><Delta pct={k.momDeltaPct} /> vs same days last month ({money(k.prevMTD)})</>} hint="Cost of the current calendar month so far" />
          <KPI label="Forecast month end" value={k.forecastMonthEnd === null ? '—' : money(k.forecastMonthEnd)} note={k.forecastMethod ? `${k.forecastMethod} · ${k.forecastConfidence} confidence` : 'needs one complete day'} tone={k.forecastConfidence === 'low' ? 'warn' : undefined} />
          <KPI label={`Last month (${k.lastMonthPeriod || '—'})`} value={money(k.lastMonth)} note="full calendar month" />
          <KPI label="Live resources" value={k.resourcesLive.toLocaleString()} note={`${k.sourcesVerified} verified source${k.sourcesVerified === 1 ? '' : 's'}${k.sourcesFailed ? ` · ${k.sourcesFailed} failed` : ''}`} tone={k.sourcesFailed ? 'bad' : undefined} />
          <KPI label="Statements" value={k.draftStatements + k.issuedStatements} note={`${k.draftStatements} draft · ${k.issuedStatements} issued`} tone={k.draftStatements ? 'warn' : undefined} />
        </div>
      ) : (
        <Skeleton lines={2} />
      )}

      <Tabs base={base} tabs={TABS} current={tab} counts={{ sources: src.data ? sources.length : undefined, users: usr.data ? users.length : undefined, statements: k ? k.draftStatements + k.issuedStatements : undefined }} />

      {tab === 'overview' ? <CustomerOverview customerId={id} /> : null}
      {tab === 'account' ? <AccountPanel customerId={id} customer={c} currency={currency} onChanged={cust.reload} /> : null}
      {tab === 'cost' ? <CustomerCostExplorer customerId={id} /> : null}
      {tab === 'resources' ? <ResourcesBody lens={customerLens(id)} /> : null}
      {tab === 'statements' ? <StatementsPanel customerId={id} canIssue /> : null}
      {tab === 'discounts' ? <DiscountsPanel customerId={id} canManage currency={currency} /> : null}
      {tab === 'budgets' ? <BudgetsPanel customerId={id} canManage currency={currency} /> : null}
      {tab === 'sources' ? (
        <SourcesPanel
          customerId={id}
          sources={sources}
          books={bookRows}
          canManage
          canRotate
          // The new-customer flow lands here with the add-source modal open:
          // defining a customer means defining where its cost comes from.
          autoAdd={params.get('add') === '1'}
          onChanged={async () => {
            await Promise.all([src.reload(), cust.reload()])
          }}
          loading={src.loading}
        />
      ) : null}
      {tab === 'users' ? <UsersPanel customerId={id} users={users} adminEmail={c.admin_email} onChanged={usr.reload} /> : null}
      {tab === 'settings' ? (
        <SettingsPanel
          key={c.id}
          customer={c}
          onSaved={(next) => {
            cust.setData({ ...c, ...next })
            return sum.reload()
          }}
        />
      ) : null}
      {tab === 'audit' ? <AuditPanel customerId={id} /> : null}
      {!TABS.some((t) => t.toLowerCase() === tab) ? <Notice kind="warn">Unknown tab "{tab}".</Notice> : null}

      {dialog === 'suspend' ? (
        <Confirm
          title={`Suspend ${c.name}`}
          danger
          confirmLabel="Suspend"
          busy={act.busy}
          onClose={() => setDialog(null)}
          onConfirm={() => setStatus('suspended')}
          body={<>Collection stops and sign-in is refused for every user of {c.name}. Statements, usage and sources are kept; Resume restores everything.</>}
        />
      ) : null}
      {dialog === 'resume' ? (
        <Confirm title={`Resume ${c.name}`} confirmLabel="Resume" busy={act.busy} onClose={() => setDialog(null)} onConfirm={() => setStatus('active')} body={<>Collection resumes on the next hourly run and users can sign in again.</>} />
      ) : null}
      {dialog === 'delete' ? <DeleteCustomerConfirm customer={c} onClose={() => setDialog(null)} /> : null}
    </div>
  )
}
