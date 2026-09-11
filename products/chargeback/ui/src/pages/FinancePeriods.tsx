import { useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { api } from '../api/client'
import type { FinancePeriod, FinancePeriodDetail } from '../api/types'
import { DataTable, type Column } from '../components/DataTable'
import { useSession } from '../auth/session'
import { Badge, Confirm, Field, KPI, Notice, PageHeader, Skeleton } from '../components/ui'
import { can } from '../lib/access'
import { blockerSummary, closeState, periodLabel, recentPeriods, statusTone } from '../lib/finance'
import { day, when } from '../lib/format'
import { formatMoney } from '../lib/money'
import { toNumber } from '../lib/num'
import { useAction } from '../lib/useAction'
import { useQuery } from '../lib/useQuery'

/**
 * Finance → Period close (DESIGN.md §18.4): the status of each month, what
 * stands between the current one and its close, and the close and reopen.
 *
 * A close is refused while a statement in the period is still a draft or an
 * invoice in it is disputed — and the page says which, so the operator reads
 * the answer before pressing the button rather than after. A reopen needs
 * settings.manage and a reason, and is audited.
 */
export function FinancePeriods() {
  const periods = useMemo(() => recentPeriods(), [])
  const [period, setPeriod] = useState(periods[1] ?? periods[0])
  const detail = useQuery<FinancePeriodDetail>(`/finance/periods/${period}`)
  const closed = useQuery<{ periods: FinancePeriod[] }>('/finance/periods')
  const act = useAction()
  const { me } = useSession()
  const mayClose = can(me, 'settings.manage')
  const [dialog, setDialog] = useState<'close' | 'reopen' | null>(null)
  const [reason, setReason] = useState('')

  const state = closeState(detail.data)
  const blockers = detail.data?.blockers ?? []
  const status = detail.data?.period?.status ?? 'open'
  const currency = ''

  const reload = async () => {
    await detail.reload()
    await closed.reload()
  }

  const doClose = async () => {
    const done = await act.run(`${periodLabel(period)} closed`, () => api.post(`/finance/periods/${period}/close`, {}), reload)
    if (done) setDialog(null)
  }

  const doReopen = async () => {
    const done = await act.run(`${periodLabel(period)} reopened`, () => api.post(`/finance/periods/${period}/reopen`, { reason: reason.trim() }), reload)
    if (done) {
      setDialog(null)
      setReason('')
    }
  }

  const blockerColumns: Column<(typeof blockers)[number]>[] = [
    { key: 'kind', header: 'What', value: (b) => b.kind, render: (b) => <Badge status={b.kind === 'draft-statement' ? 'draft statement' : 'open dispute'} kind={b.kind === 'draft-statement' ? 'warn' : 'bad'} /> },
    { key: 'customer', header: 'Customer', value: (b) => b.customer_name ?? '', render: (b) => (b.customer_id ? <Link to={`/customers/${b.customer_id}`}>{b.customer_name}</Link> : <>{b.customer_name || '—'}</>) },
    { key: 'invoice', header: 'Invoice', value: (b) => b.invoice_number ?? '', render: (b) => (b.invoice_number ? <span className="mono">{b.invoice_number}</span> : <span className="muted">not numbered</span>) },
    { key: 'detail', header: 'Why it blocks the close', value: (b) => b.detail ?? '', render: (b) => <span className="sub">{b.detail}</span> },
  ]

  const periodColumns: Column<FinancePeriod>[] = [
    { key: 'period', header: 'Period', value: (p) => p.period, render: (p) => <>{periodLabel(p.period)}<span className="sub mono">{p.period}</span></> },
    { key: 'status', header: 'Status', value: (p) => p.status, render: (p) => <Badge status={p.status} kind={statusTone(p.status)} /> },
    { key: 'closed', header: 'Closed', value: (p) => p.closed_at ?? '', render: (p) => (p.closed_at ? <>{day(p.closed_at)}<span className="sub">{p.closed_by}</span></> : <span className="muted">—</span>) },
    { key: 'lines', header: 'Lines', numeric: true, value: (p) => p.lines ?? 0 },
    { key: 'debit', header: 'Debits', numeric: true, value: (p) => toNumber(p.total_debit), render: (p) => formatMoney(toNumber(p.total_debit), currency) },
    { key: 'credit', header: 'Credits', numeric: true, value: (p) => toNumber(p.total_credit), render: (p) => formatMoney(toNumber(p.total_credit), currency) },
    { key: 'reopened', header: 'Reopened', value: (p) => p.reopened_at ?? '', render: (p) => (p.reopened_at ? <>{day(p.reopened_at)}<span className="sub">{p.reopened_by}: {p.reopen_reason}</span></> : <span className="muted">—</span>) },
  ]

  return (
    <div className="stack">
      <PageHeader
        title="Period close"
        sub={<>once a month is closed, no statement in it may be issued, re-run, cancelled or credited</>}
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
            {mayClose && status !== 'closed' ? (
              <button className="primary" disabled={!state.can || act.busy} title={state.can ? undefined : state.why} onClick={() => setDialog('close')}>
                Close {periodLabel(period)}
              </button>
            ) : null}
            {mayClose && status === 'closed' ? (
              <button
                className="danger"
                disabled={act.busy}
                onClick={() => {
                  setReason('')
                  setDialog('reopen')
                }}
              >
                Reopen
              </button>
            ) : null}
          </span>
        }
      />

      {act.error ? <Notice kind="bad">{act.error}</Notice> : null}
      {act.ok ? <Notice kind="ok">{act.ok}</Notice> : null}
      {detail.error ? <Notice kind="bad">{detail.error}</Notice> : null}

      {detail.loading && !detail.data ? (
        <Skeleton lines={5} />
      ) : (
        <>
          <div className="kpis">
            <KPI label="Status" value={<Badge status={status} kind={statusTone(status)} />} note={status === 'closed' ? `by ${detail.data?.period?.closed_by ?? 'an operator'}` : state.why} />
            <KPI label="Debits" value={formatMoney(toNumber(detail.data?.total_debit ?? detail.data?.period?.total_debit), currency)} note={`${detail.data?.lines ?? detail.data?.period?.lines ?? 0} journal line${(detail.data?.lines ?? detail.data?.period?.lines ?? 0) === 1 ? '' : 's'}`} />
            <KPI label="Credits" value={formatMoney(toNumber(detail.data?.total_credit ?? detail.data?.period?.total_credit), currency)} />
            <KPI label="Blocking the close" value={blockers.length} tone={blockers.length ? 'bad' : 'ok'} note={blockerSummary(blockers) || 'nothing is outstanding'} />
          </div>

          <section className="card">
            <h2>What blocks the close</h2>
            {blockers.length === 0 ? (
              <p className="ok">
                Nothing. Every statement in {periodLabel(period)} is decided and no invoice in it is disputed. <Link to="/finance/journal">Read the journal</Link> before closing.
              </p>
            ) : (
              <DataTable label="What blocks the close" columns={blockerColumns} rows={blockers} rowKey={(b) => `${b.kind}-${b.id}`} emptyTitle="Nothing blocks the close" />
            )}
            {detail.data?.balance_error ? <Notice kind="bad">{detail.data.balance_error}</Notice> : null}
          </section>

          {status === 'reopened' && detail.data?.period?.reopen_reason ? (
            <Notice kind="warn">
              Reopened {when(detail.data.period.reopened_at)} by {detail.data.period.reopened_by}: {detail.data.period.reopen_reason}
            </Notice>
          ) : null}
        </>
      )}

      <section className="card">
        <h2>Closed periods</h2>
        {closed.error ? <Notice kind="bad">{closed.error}</Notice> : null}
        <DataTable label="Periods" columns={periodColumns} rows={closed.data?.periods ?? []} rowKey={(p) => p.period} defaultSort={{ key: 'period', dir: 'desc' }} onRowClick={(p) => setPeriod(p.period)} selectedKey={period} emptyTitle="No period has been closed" emptyBody={<>Close a month above and it appears here with the journal it was closed on.</>} />
      </section>

      {dialog === 'close' ? (
        <Confirm
          title={`Close ${periodLabel(period)}`}
          danger={false}
          confirmLabel="Close the period"
          busy={act.busy}
          onClose={() => setDialog(null)}
          onConfirm={doClose}
          body={
            <>
              <p>
                The journal for {periodLabel(period)} is frozen as it stands and the month stops moving: no statement in it may be issued, re-run, cancelled or credited, and the rating run refuses the period.
              </p>
              <p className="muted">Reopening it later needs a reason and is audited.</p>
            </>
          }
        />
      ) : null}

      {dialog === 'reopen' ? (
        <Confirm
          title={`Reopen ${periodLabel(period)}`}
          danger
          confirmLabel="Reopen the period"
          busy={act.busy || !reason.trim()}
          onClose={() => setDialog(null)}
          onConfirm={doReopen}
          body={
            <>
              <p>The books for {periodLabel(period)} were closed by {detail.data?.period?.closed_by ?? 'an operator'}. Reopening lets statements in it change again.</p>
              <Field label="Why" help="recorded with your name in the audit trail">
                <input value={reason} onChange={(e) => setReason(e.target.value)} placeholder="the August storage meter was wrong" />
              </Field>
            </>
          }
        />
      ) : null}
    </div>
  )
}
