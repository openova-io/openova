import { useMemo, useState } from 'react'
import { API_BASE } from '../api/client'
import type { JournalLine, JournalResponse } from '../api/types'
import { DataTable, type Column } from '../components/DataTable'
import { Badge, KPI, Notice, PageHeader, Skeleton } from '../components/ui'
import { accountTotals, balanceOf, eventLabel, periodLabel, recentPeriods, statusTone, type AccountTotal } from '../lib/finance'
import { day } from '../lib/format'
import { formatMoney } from '../lib/money'
import { toNumber } from '../lib/num'
import { useQuery } from '../lib/useQuery'

/**
 * Finance → Journal (DESIGN.md §18.2): a period's double-entry lines with
 * the account codes THE OPERATOR mapped, the balance check shown as a figure
 * rather than asserted, and the CSV a finance system imports.
 *
 * A closed period is served from the journal the close froze, so what is on
 * this page is what the books were signed off on.
 */
export function FinanceJournal() {
  const periods = useMemo(() => recentPeriods(), [])
  const [period, setPeriod] = useState(periods[1] ?? periods[0])
  const j = useQuery<JournalResponse>(`/finance/journal?period=${period}`)
  const batch = j.data?.journal ?? null
  const balance = balanceOf(batch)
  const lines = useMemo(() => batch?.lines ?? [], [batch])
  const totals = useMemo(() => accountTotals(lines), [lines])
  const currency = balance.currency || lines[0]?.currency || ''
  const money = (v: number | string | null | undefined, cur = currency) => formatMoney(toNumber(v), cur)
  const status = j.data?.status ?? 'open'

  const columns: Column<JournalLine>[] = [
    { key: 'seq', header: '#', numeric: true, width: 56, value: (l) => l.seq },
    { key: 'date', header: 'Date', value: (l) => l.date, render: (l) => <span className="mono">{l.date}</span> },
    { key: 'event', header: 'Event', value: (l) => l.event, render: (l) => eventLabel(l.event) },
    {
      key: 'account',
      header: 'Account',
      value: (l) => l.account_code,
      render: (l) => (
        <>
          <span className="mono">{l.account_code}</span>
          <span className="sub">
            {l.account_name || l.account_key} · <span className="mono">{l.account_key}</span>
          </span>
        </>
      ),
    },
    { key: 'debit', header: 'Debit', numeric: true, value: (l) => toNumber(l.debit), render: (l) => (toNumber(l.debit) ? money(l.debit, l.currency) : <span className="muted">—</span>), total: (rs) => money(rs.reduce((n, l) => n + toNumber(l.debit), 0)) },
    { key: 'credit', header: 'Credit', numeric: true, value: (l) => toNumber(l.credit), render: (l) => (toNumber(l.credit) ? money(l.credit, l.currency) : <span className="muted">—</span>), total: (rs) => money(rs.reduce((n, l) => n + toNumber(l.credit), 0)) },
    { key: 'customer', header: 'Customer', value: (l) => l.customer_name ?? '', render: (l) => (l.customer_name ? <>{l.customer_name}</> : <span className="muted">—</span>) },
    {
      key: 'source',
      header: 'From',
      value: (l) => `${l.source_kind}:${l.source_id}`,
      render: (l) => (
        <>
          {l.reference || l.source_kind}
          <span className="sub">
            {l.source_kind} <span className="mono">{l.source_id}</span>
          </span>
        </>
      ),
    },
  ]

  const accountColumns: Column<AccountTotal>[] = [
    { key: 'code', header: 'Account', value: (a) => a.code, render: (a) => <span className="mono">{a.code}</span> },
    { key: 'name', header: 'Name', value: (a) => a.name || a.key, render: (a) => <>{a.name || a.key}</> },
    { key: 'debit', header: 'Debit', numeric: true, value: (a) => a.debit, render: (a) => (a.debit ? money(a.debit) : <span className="muted">—</span>), total: (rs) => money(rs.reduce((n, a) => n + a.debit, 0)) },
    { key: 'credit', header: 'Credit', numeric: true, value: (a) => a.credit, render: (a) => (a.credit ? money(a.credit) : <span className="muted">—</span>), total: (rs) => money(rs.reduce((n, a) => n + a.credit, 0)) },
    { key: 'lines', header: 'Lines', numeric: true, value: (a) => a.lines },
  ]

  return (
    <div className="stack">
      <PageHeader
        title="Journal"
        sub={
          <>
            every financial event of {periodLabel(period)} as double-entry lines against the accounts you mapped
            {status !== 'open' ? (
              <>
                {' '}
                · <Badge status={status} kind={statusTone(status)} />
              </>
            ) : null}
          </>
        }
        actions={
          <span className="btn-row">
            <label className="inline">
              <span className="muted">Period</span>{' '}
              <select value={period} onChange={(e) => setPeriod(e.target.value)} aria-label="Period">
                {periods.map((p) => (
                  <option key={p} value={p}>
                    {periodLabel(p)}
                  </option>
                ))}
              </select>
            </label>
            <a className="button" href={`${API_BASE}/finance/journal?period=${period}&format=csv`}>
              Download CSV
            </a>
          </span>
        }
      />

      {j.error ? <Notice kind="bad">{j.error}</Notice> : null}

      <div className="kpis">
        <KPI label="Total debits" value={money(balance.debit)} note={`${balance.lines} line${balance.lines === 1 ? '' : 's'}`} />
        <KPI label="Total credits" value={money(balance.credit)} />
        <KPI
          label="Balance check"
          value={money(balance.difference)}
          tone={balance.balanced ? 'ok' : 'bad'}
          note={balance.balanced ? 'debits equal credits' : 'the journal does not balance'}
          hint="the difference between the two sides; an export is refused unless it is zero"
        />
        <KPI label="Accounts posted to" value={totals.length} note={status === 'closed' ? 'frozen at the close' : 'derived from the ledger'} />
      </div>

      {batch?.unmapped_account_keys?.length ? <Notice kind="warn">No account is mapped for {batch.unmapped_account_keys.join(', ')} — map it on the Account mapping page, or the journal cannot balance.</Notice> : null}

      {j.loading && !batch ? (
        <Skeleton lines={6} />
      ) : (
        <>
          <section className="card">
            <h2>By account</h2>
            <p className="muted">What a finance system posts for {periodLabel(period)}.</p>
            <DataTable label="Journal by account" columns={accountColumns} rows={totals} rowKey={(a) => `${a.code}-${a.key}`} defaultSort={{ key: 'code', dir: 'asc' }} emptyTitle="Nothing was posted in this period" emptyBody={<>No invoice, payment, credit note or settlement fee fell in {periodLabel(period)}.</>} csvName={`journal-${period}-accounts`} />
          </section>

          <section className="card">
            <h2>Lines</h2>
            <p className="muted">Every line carries the statement, payment or credit note it came from, so any figure traces back.</p>
            <DataTable
              label="Journal"
              columns={columns}
              rows={lines}
              rowKey={(l) => String(l.seq)}
              defaultSort={{ key: 'seq', dir: 'asc' }}
              pageSize={100}
              emptyTitle="Nothing was posted in this period"
              emptyBody={<>Rate and issue a statement, or record a payment, and it appears here.</>}
              csvName={`journal-${period}`}
              footNote={j.data?.period_state?.closed_at ? <>closed {day(j.data.period_state.closed_at)} by {j.data.period_state.closed_by}</> : undefined}
            />
          </section>
        </>
      )}
    </div>
  )
}
