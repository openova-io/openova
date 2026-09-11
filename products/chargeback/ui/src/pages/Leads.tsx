import { useMemo } from 'react'
import { asList } from '../api/client'
import type { Estimate } from '../api/types'
import { DataTable, type Column } from '../components/DataTable'
import { KPI, Notice, PageHeader, Skeleton } from '../components/ui'
import { day, when } from '../lib/format'
import { formatMoney } from '../lib/money'
import { useQuery } from '../lib/useQuery'

/**
 * Leads (DESIGN.md §11) — the public estimates a prospect left an address
 * on, newest first. Read-only by design: a lead is a record of what somebody
 * priced for themselves, and the proposals module is what turns one into an
 * offer. `customers.manage` at the Sovereign; the API refuses everyone else.
 */
export function Leads() {
  const leads = useQuery<unknown>('/leads')
  const rows = useMemo(() => asList<Estimate>(leads.data, 'leads'), [leads.data])
  const currency = rows[0]?.currency ?? ''
  const pipeline = rows.reduce((sum, e) => sum + Number(e.monthly ?? 0), 0)
  const live = rows.filter((e) => new Date(e.valid_until).getTime() > Date.now())

  const columns: Column<Estimate>[] = [
    {
      key: 'created',
      header: 'Received',
      value: (r) => r.created_at,
      render: (r) => (
        <>
          {when(r.created_at)}
          <span className="sub">valid until {day(r.valid_until)}</span>
        </>
      ),
    },
    {
      key: 'email',
      header: 'Contact',
      value: (r) => r.contact_email ?? '',
      render: (r) => (r.contact_email ? <a href={`mailto:${r.contact_email}`}>{r.contact_email}</a> : <span className="muted">—</span>),
    },
    {
      key: 'monthly',
      header: 'Per month',
      value: (r) => Number(r.monthly ?? 0),
      numeric: true,
      render: (r) => formatMoney(r.monthly, r.currency),
    },
    {
      key: 'yearly',
      header: 'Per year',
      value: (r) => Number(r.yearly ?? 0),
      numeric: true,
      render: (r) => formatMoney(r.yearly, r.currency),
    },
    {
      key: 'lines',
      header: 'What they priced',
      value: (r) => r.lines?.length ?? 0,
      render: (r) => (
        <span title={(r.lines ?? []).map((l) => `${l.plan ? `${l.plan.toUpperCase()} plan` : l.sku} × ${l.quantity}`).join(', ')}>
          {r.lines?.length ?? 0} line{(r.lines?.length ?? 0) === 1 ? '' : 's'}
          <span className="sub">{(r.lines ?? []).map((l) => (l.plan ? `${l.plan.toUpperCase()} plan` : l.sku)).slice(0, 3).join(' · ')}</span>
        </span>
      ),
    },
    {
      key: 'region',
      header: 'Region',
      value: (r) => r.region ?? '',
      render: (r) => (r.region ? r.region : <span className="muted">any</span>),
    },
    {
      key: 'link',
      header: '',
      className: 'actions',
      value: () => '',
      sortable: false,
      render: (r) => (
        <a href={r.share_url ?? `/estimate/${r.id}`} target="_blank" rel="noreferrer">
          <button className="small">Open estimate</button>
        </a>
      ),
    },
  ]

  return (
    <div className="stack">
      <PageHeader title="Leads" sub="Estimates from the public calculator whose author left an address. Prices quoted are the list prices of the published price book on the day the estimate was made." />
      {leads.error ? <Notice kind="bad">{leads.error}</Notice> : null}

      <div className="kpis">
        <KPI label="Leads" value={rows.length} note={rows.length ? `${live.length} still within their 30-day validity` : 'nobody has asked for an estimate yet'} />
        <KPI label="Priced per month" value={formatMoney(pipeline, currency, { compact: true })} note="the sum of every lead's monthly figure, at list price" />
      </div>

      <div className="card pad-0">
        {leads.loading && !leads.data ? (
          <div style={{ padding: 16 }}>
            <Skeleton lines={3} />
          </div>
        ) : (
          <DataTable
            columns={columns}
            rows={rows}
            rowKey={(r) => r.id}
            defaultSort={{ key: 'created', dir: 'desc' }}
            emptyTitle="No leads yet"
            emptyBody={
              <>
                A lead appears when somebody prices a month on the public calculator and asks for it by email. Publish a price book on <a href="/pricebooks">Price books</a> to open the calculator, then link{' '}
                <a href="/estimate">/estimate</a> from the website.
              </>
            }
          />
        )}
      </div>
    </div>
  )
}
