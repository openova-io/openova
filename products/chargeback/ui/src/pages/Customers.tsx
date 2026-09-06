import { useMemo, useState } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router-dom'
import { api, asList } from '../api/client'
import type { Customer, InviteIssued, PriceBook, Summary } from '../api/types'
import { DataTable, type Column } from '../components/DataTable'
import { Badge, Delta, KPI, Notice, PageHeader, Segmented, Skeleton } from '../components/ui'
import { STATUS_FILTERS, customerCounts, filterCustomers, lastStatementText, mtdByCustomer, mtdFor, priceBookName, sourceCounts, sourcesText, type StatusFilter } from '../lib/customers'
import { when } from '../lib/format'
import { formatMoney } from '../lib/money'
import { readKPIs } from '../lib/summary'
import { useAction } from '../lib/useAction'
import { useQuery } from '../lib/useQuery'
import { NewCustomerModal } from './CustomerNew'

/**
 * Customers — the account directory (#6867, DESIGN.md §2.4). One row per
 * customer with its billing setup, collector health and month-to-date cost
 * joined from /cost/summary; every row opens the account view.
 */
export function Customers() {
  const nav = useNavigate()
  const [params, setParams] = useSearchParams()
  const list = useQuery<unknown>('/customers')
  const books = useQuery<unknown>('/pricebooks')
  const sum = useQuery<Summary>('/cost/summary')
  const rows = useMemo(() => asList<Customer>(list.data, 'customers'), [list.data])
  const bookRows = useMemo(() => asList<PriceBook>(books.data, 'pricebooks', 'price_books'), [books.data])
  const mtd = useMemo(() => mtdByCustomer(sum.data), [sum.data])
  const k = sum.data ? readKPIs(sum.data) : null
  const currency = k?.currency ?? ''
  const [q, setQ] = useState('')
  const statusParam = params.get('status')
  const status: StatusFilter = STATUS_FILTERS.some((f) => f.value === statusParam) ? (statusParam as StatusFilter) : 'all'
  const showNew = params.get('new') === '1'
  const setParam = (key: string, value: string) => {
    const p = new URLSearchParams(params)
    if (value) p.set(key, value)
    else p.delete(key)
    setParams(p, { replace: true })
  }
  const visible = useMemo(() => filterCustomers(rows, q, status), [rows, q, status])
  const counts = customerCounts(rows)
  const act = useAction()
  const [invite, setInvite] = useState<{ customer: Customer; issued: InviteIssued } | null>(null)

  const sendInvite = (c: Customer) =>
    act.run(`invite sent to ${c.admin_email}`, async () => {
      setInvite({ customer: c, issued: await api.post<InviteIssued>(`/customers/${c.id}/invite`) })
    })

  const columns: Column<Customer>[] = [
    {
      key: 'name',
      header: 'Customer',
      value: (c) => c.name,
      render: (c) => (
        <>
          {c.name}
          <span className="sub">
            <span className="mono">{c.slug}</span>
            {c.kind === 'organization' ? ' · organization' : ''} · {c.admin_email}
          </span>
        </>
      ),
    },
    {
      key: 'status',
      header: 'Status',
      value: (c) => c.status,
      render: (c) => (
        <>
          <Badge status={c.status} />
          {c.status === 'active' && c.collecting === false ? <span className="sub warn">not collecting</span> : null}
        </>
      ),
    },
    { key: 'billing', header: 'Billing', value: (c) => c.billing_mode },
    {
      key: 'book',
      header: 'Price book',
      value: (c) => priceBookName(bookRows, c.price_book_id) ?? '',
      render: (c) => {
        const name = priceBookName(bookRows, c.price_book_id)
        return name ? <Link to={`/pricebooks/${c.price_book_id}`} onClick={(e) => e.stopPropagation()}>{name}</Link> : <span className="muted" title="Nothing is rated without a price book">none</span>
      },
    },
    {
      key: 'sources',
      header: 'Sources',
      value: (c) => sourceCounts(c).total,
      numeric: true,
      render: (c) => {
        const { verified, total } = sourceCounts(c)
        return <span className={total !== null && verified === 0 && total > 0 ? 'warn' : ''} title="verified / total">{sourcesText(c)}</span>
      },
    },
    {
      key: 'mtd',
      header: 'Month to date',
      value: (c) => mtdFor(mtd, c.id),
      numeric: true,
      render: (c) => {
        const v = mtdFor(mtd, c.id)
        return v === null ? <span className="muted" title="Not among the top customers of the summary — open the account for its cost">—</span> : formatMoney(v, currency)
      },
    },
    { key: 'collected', header: 'Last collected', value: (c) => c.last_collected_at ?? '', render: (c) => <span className={c.last_collected_at ? '' : 'muted'}>{when(c.last_collected_at)}</span> },
    { key: 'statement', header: 'Last statement', value: (c) => c.last_statement_period ?? '', render: (c) => lastStatementText(c) },
    {
      key: 'actions',
      header: '',
      value: () => '',
      sortable: false,
      className: 'nowrap',
      render: (c) => (
        <button
          className="link small"
          disabled={act.busy}
          onClick={(e) => {
            e.stopPropagation()
            void sendInvite(c)
          }}
          title={c.status === 'pending' ? 'Send the activation invite to the admin email' : 'Re-send the sign-in invite'}
        >
          {c.status === 'pending' ? 'Invite' : 'Re-invite'}
        </button>
      ),
    },
  ]

  return (
    <div className="stack">
      <PageHeader
        title="Customers"
        sub={`${counts.total} customer${counts.total === 1 ? '' : 's'} · ${counts.active} active · ${counts.pending} pending · ${counts.suspended} suspended`}
        actions={
          <>
            <Link to="/customers/import">
              <button>Import CSV</button>
            </Link>
            <button className="primary" onClick={() => setParam('new', '1')}>
              New customer
            </button>
          </>
        }
      />
      {list.error ? <Notice kind="bad">{list.error}</Notice> : null}
      {act.error ? <Notice kind="bad">{act.error}</Notice> : null}
      {invite ? (
        <Notice kind="ok">
          Invite for <b>{invite.customer.name}</b> sent to {invite.customer.admin_email}, valid until {when(invite.issued.expires_at)}.
          <br />
          <span className="mono small">{invite.issued.invite_url}</span>
        </Notice>
      ) : null}

      <div className="kpis">
        <KPI label="Active" value={counts.active} note="collected and billed" />
        <KPI label="Pending" value={counts.pending} note={counts.pending ? 'invite not yet activated' : 'every customer is activated'} tone={counts.pending ? 'warn' : undefined} />
        <KPI label="Suspended" value={counts.suspended} note={counts.suspended ? 'not collected, sign-in refused' : 'none'} tone={counts.suspended ? 'bad' : undefined} />
        {sum.error ? (
          <KPI label="Month to date" value="—" note={sum.error} tone="bad" />
        ) : k ? (
          <KPI label="Month to date, all customers" value={formatMoney(k.mtd, currency, { compact: true })} note={<><Delta pct={k.momDeltaPct} /> vs same days last month</>} />
        ) : (
          <KPI label="Month to date" value="…" />
        )}
      </div>

      <div className="toolbar">
        <div className="field grow">
          <label htmlFor="customer-search">Search</label>
          <input id="customer-search" value={q} onChange={(e) => setQ(e.target.value)} placeholder="name, slug or admin email" />
        </div>
        <div className="field">
          <label>Status</label>
          <Segmented<StatusFilter> value={status} options={STATUS_FILTERS} onChange={(v) => setParam('status', v === 'all' ? '' : v)} ariaLabel="Status filter" />
        </div>
      </div>

      {list.loading && !list.data ? (
        <Skeleton lines={5} />
      ) : (
        <div className="card pad-0">
          <DataTable
            columns={columns}
            rows={visible}
            rowKey={(c) => c.id}
            defaultSort={{ key: 'name', dir: 'asc' }}
            onRowClick={(c) => nav(`/customers/${c.id}`)}
            csvName="customers"
            emptyTitle={rows.length === 0 ? 'No customers yet' : 'No customer matches'}
            emptyBody={
              rows.length === 0 ? (
                <>
                  Create the first customer, or <Link to="/customers/import">import a CSV</Link>.
                </>
              ) : (
                <>
                  Nothing matches "{q}"{status !== 'all' ? ` with status ${status}` : ''}.{' '}
                  <button className="link" onClick={() => { setQ(''); setParam('status', '') }}>
                    Clear filters
                  </button>
                </>
              )
            }
            footNote="click a row to open the account"
          />
        </div>
      )}

      {showNew ? <NewCustomerModal books={bookRows} onClose={() => setParam('new', '')} /> : null}
    </div>
  )
}
