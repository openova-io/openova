import { useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { api } from '../api/client'
import type { AgingReport, AgingRow, BillingSettings, CollectionsRun } from '../api/types'
import { DataTable, type Column } from '../components/DataTable'
import { Badge, Confirm, Field, KPI, Notice, PageHeader, Skeleton } from '../components/ui'
import { AGING_BUCKETS, agingKPIs, oldestDueText, overdueShare, reminderScheduleText } from '../lib/account'
import { day, when } from '../lib/format'
import { formatMoney, formatPct } from '../lib/money'
import { toNumber } from '../lib/num'
import { useAction } from '../lib/useAction'
import { useSession } from '../auth/session'
import { can } from '../lib/access'
import { useQuery } from '../lib/useQuery'

/**
 * Collections (DESIGN.md §9.6–§9.7): the aging report per customer, the
 * evaluator run on demand, and the operator's explicit suspend / resume at
 * the platform. With an external billing system the report still reads,
 * but collections are theirs and the run says so.
 */

type Dialog = { kind: 'run' } | { kind: 'suspend' | 'resume'; row: AgingRow } | null

export function Collections() {
  const rep = useQuery<AgingReport>('/collections/aging')
  const settings = useQuery<BillingSettings>('/billing-settings')
  const act = useAction()
  const { me } = useSession()
  // Running collections and suspending or resuming at the platform are
  // billing.collect (DESIGN.md §10.9); a finance-viewer reads the report.
  const canCollect = can(me, 'billing.collect')
  const [dialog, setDialog] = useState<Dialog>(null)
  const [reason, setReason] = useState('')
  const [run, setRun] = useState<CollectionsRun | null>(null)
  const [open, setOpen] = useState<string | null>(null)

  const k = agingKPIs(rep.data)
  const rows = useMemo(() => rep.data?.rows ?? [], [rep.data])
  const invoices = useMemo(() => rep.data?.invoices ?? [], [rep.data])
  const external = (rep.data?.collections_owner ?? settings.data?.commercial_provider) === 'external'
  const share = rep.data ? overdueShare(rep.data) : null
  const schedule = settings.data ? reminderScheduleText(settings.data.reminder_days ?? []) : ''
  const currency = k.currency
  const money = (v: number | string | null | undefined, cur = currency) => formatMoney(toNumber(v), cur)

  const runNow = async () => {
    const ok = await act.run('collections run', async () => setRun(await api.post<CollectionsRun>('/collections/run', {})), rep.reload)
    if (ok) setDialog(null)
  }

  const enforce = async (kind: 'suspend' | 'resume', row: AgingRow) => {
    const ok = await act.run(`${row.customer_name} ${kind === 'suspend' ? 'suspended' : 'resumed'} at the platform`, () => api.post(`/customers/${row.customer_id}/${kind}`, { reason: reason.trim() }), rep.reload)
    if (ok) {
      setDialog(null)
      setReason('')
    }
  }

  const columns: Column<AgingRow>[] = [
    {
      key: 'customer',
      header: 'Customer',
      value: (r) => r.customer_name,
      render: (r) => (
        <>
          <Link to={`/customers/${r.customer_id}?tab=account`}>{r.customer_name}</Link>
          <span className="sub">
            <span className="mono">{r.customer_slug}</span> · {r.invoices} open invoice{r.invoices === 1 ? '' : 's'}
            {r.suspended ? (
              <>
                {' '}
                · <Badge status="suspended" kind="bad" />
              </>
            ) : null}
          </span>
        </>
      ),
    },
    ...AGING_BUCKETS.map(
      (b): Column<AgingRow> => ({
        key: b.value,
        header: b.label,
        numeric: true,
        value: (r) => toNumber(r.buckets?.[b.value]),
        render: (r) => {
          const v = toNumber(r.buckets?.[b.value])
          return v > 0 ? <span className={b.late ? 'bad' : ''}>{money(v, r.currency)}</span> : <span className="muted">—</span>
        },
        total: (rs) => {
          const t = rs.reduce((n, r) => n + toNumber(r.buckets?.[b.value]), 0)
          return t > 0 ? money(t) : ''
        },
      }),
    ),
    {
      key: 'total',
      header: 'Total owed',
      numeric: true,
      value: (r) => toNumber(r.total),
      render: (r) => <b>{money(r.total, r.currency)}</b>,
      total: (rs) => <b>{money(rs.reduce((n, r) => n + toNumber(r.total), 0))}</b>,
    },
    {
      key: 'overdue',
      header: 'Overdue',
      numeric: true,
      value: (r) => toNumber(r.overdue),
      render: (r) => (toNumber(r.overdue) > 0 ? <span className="bad">{money(r.overdue, r.currency)}</span> : <span className="ok">none</span>),
      total: (rs) => money(rs.reduce((n, r) => n + toNumber(r.overdue), 0)),
    },
    { key: 'oldest', header: 'Oldest due', numeric: true, value: (r) => r.oldest_days, render: (r) => <span className={r.oldest_days > 0 ? 'bad' : 'muted'}>{oldestDueText(r.oldest_days)}</span> },
    {
      key: 'credit',
      header: 'Credit available',
      numeric: true,
      value: (r) => toNumber(r.available_credit),
      render: (r) => (toNumber(r.available_credit) > 0 ? <span className="ok">{money(r.available_credit, r.currency)}</span> : <span className="muted">—</span>),
    },
    {
      key: 'actions',
      header: '',
      value: () => '',
      sortable: false,
      className: 'nowrap actions',
      render: (r) => (
        <span className="btn-row">
          <button
            className="small"
            onClick={(e) => {
              e.stopPropagation()
              setOpen(open === r.customer_id ? null : r.customer_id)
            }}
          >
            {open === r.customer_id ? 'Hide invoices' : 'Invoices'}
          </button>
          {!canCollect ? null : r.suspended ? (
            <button
              className="small primary"
              disabled={act.busy}
              onClick={(e) => {
                e.stopPropagation()
                setReason('')
                setDialog({ kind: 'resume', row: r })
              }}
            >
              Resume
            </button>
          ) : (
            <button
              className="small danger"
              disabled={act.busy}
              onClick={(e) => {
                e.stopPropagation()
                setReason('')
                setDialog({ kind: 'suspend', row: r })
              }}
            >
              Suspend
            </button>
          )}
        </span>
      ),
    },
  ]

  return (
    <div className="stack">
      <PageHeader
        title="Collections"
        sub={
          rep.data ? (
            <>
              aging as of {day(rep.data.as_of)} · {k.invoices} open invoice{k.invoices === 1 ? '' : 's'} across {k.customers} customer{k.customers === 1 ? '' : 's'}
              {schedule ? ` · reminders ${schedule}` : ''}
              {settings.data?.escalation_days ? ` · escalate ${settings.data.escalation_days} days after, ${settings.data.escalation_action === 'suspend' ? 'suspend' : 'notify'}` : ''}
            </>
          ) : (
            'the aging report'
          )
        }
        actions={
          <>
            <Link to="/billing">
              <button>Reminder schedule</button>
            </Link>
            {canCollect ? (
              <button className="primary" onClick={() => setDialog({ kind: 'run' })} disabled={act.busy}>
                Run collections now
              </button>
            ) : null}
          </>
        }
      />
      {rep.error ? <Notice kind="bad">{rep.error}</Notice> : null}
      {act.error ? <Notice kind="bad">{act.error}</Notice> : null}
      {act.ok && !run ? <Notice kind="ok">{act.ok}</Notice> : null}
      {run ? (
        run.skipped ? (
          <Notice kind="warn">Collections did not run{run.skip_reason ? ` — ${run.skip_reason}` : ''}.</Notice>
        ) : (
          <Notice kind={run.errors ? 'warn' : 'ok'}>
            Evaluated {run.invoices} open invoice{run.invoices === 1 ? '' : 's'}: {run.reminders} reminder{run.reminders === 1 ? '' : 's'}, {run.escalations} escalation{run.escalations === 1 ? '' : 's'}, {run.suspended} suspended, {run.resumed} resumed
            {run.mails ? `, ${run.mails} mail${run.mails === 1 ? '' : 's'} sent` : ''}
            {run.errors ? `, ${run.errors} error${run.errors === 1 ? '' : 's'}` : ''}.
          </Notice>
        )
      ) : null}
      {external ? <Notice kind="info">This Sovereign invoices through the operator's billing system: collections are theirs. The report still reads from our invoice copy, and suspend / resume here execute an explicit command.</Notice> : null}

      <div className="kpis">
        <KPI label="Total owed" value={money(k.total)} note={`${k.invoices} open invoice${k.invoices === 1 ? '' : 's'}`} />
        <KPI label="Overdue" value={money(k.overdue)} note={share === null ? 'nothing owed' : `${formatPct(share * 100)} of what is owed`} tone={k.overdue > 0 ? 'bad' : undefined} />
        <KPI label="Customers overdue" value={k.customersOverdue} note={k.customersOverdue ? `of ${k.customers} with an open invoice` : 'everyone is within terms'} tone={k.customersOverdue ? 'warn' : undefined} />
        <KPI label="Suspended" value={rows.filter((r) => r.suspended).length} note="held at the platform by this product" tone={rows.some((r) => r.suspended) ? 'bad' : undefined} />
      </div>

      <div className="card pad-0">
        {rep.loading && !rep.data ? (
          <div style={{ padding: 16 }}>
            <Skeleton lines={4} />
          </div>
        ) : (
          <DataTable
            columns={columns}
            rows={rows}
            rowKey={(r) => r.customer_id}
            label="Aging"
            defaultSort={{ key: 'overdue', dir: 'desc' }}
            csvName="aging"
            selectedKey={open}
            emptyTitle="Nothing is owed"
            emptyBody="Every issued invoice is settled. The report fills as invoices are sent and fall due."
            footNote="buckets are days past the due date; current is not yet due"
            expanded={(r) =>
              open === r.customer_id ? (
                <table aria-label={`Open invoices of ${r.customer_name}`}>
                  <thead>
                    <tr>
                      <th>Invoice</th>
                      <th>Due</th>
                      <th>Bucket</th>
                      <th>Status</th>
                      <th className="num">Outstanding</th>
                    </tr>
                  </thead>
                  <tbody>
                    {invoices
                      .filter((i) => i.customer_id === r.customer_id)
                      .map((i) => (
                        <tr key={i.statement_id}>
                          <td>
                            <Link to={`/statements/${i.statement_id}`} className="mono">
                              {i.invoice_number || i.statement_id}
                            </Link>
                          </td>
                          <td>
                            {day(i.due_at)} <span className={i.days_past_due > 0 ? 'sub bad' : 'sub'}>{oldestDueText(i.days_past_due)}</span>
                          </td>
                          <td>{AGING_BUCKETS.find((b) => b.value === i.bucket)?.label ?? i.bucket}</td>
                          <td>
                            <Badge status={i.status} kind={i.days_past_due > 0 ? 'bad' : undefined} />
                          </td>
                          <td className="num">{money(i.outstanding, i.currency)}</td>
                        </tr>
                      ))}
                  </tbody>
                </table>
              ) : null
            }
          />
        )}
      </div>

      {dialog?.kind === 'run' ? (
        <Confirm
          title="Run collections now?"
          confirmLabel="Run collections"
          busy={act.busy}
          onClose={() => setDialog(null)}
          onConfirm={runNow}
          body={
            <div className="stack tight">
              <p>
                One evaluator pass as of today: every open invoice is checked against the reminder schedule{schedule ? ` (${schedule})` : ''} and the escalation rule. Reminders are emailed, escalations executed, and a settled customer this product suspended is resumed.
              </p>
              <p className="muted small" style={{ margin: 0 }}>
                The same pass runs daily; this only brings it forward. A reminder already sent for a step is not sent again.
              </p>
            </div>
          }
        />
      ) : null}
      {dialog?.kind === 'suspend' || dialog?.kind === 'resume' ? (
        <Confirm
          title={`${dialog.kind === 'suspend' ? 'Suspend' : 'Resume'} ${dialog.row.customer_name} at the platform?`}
          danger={dialog.kind === 'suspend'}
          confirmLabel={dialog.kind === 'suspend' ? 'Suspend' : 'Resume'}
          busy={act.busy}
          onClose={() => setDialog(null)}
          onConfirm={() => enforce(dialog.kind, dialog.row)}
          body={
            <div className="stack tight">
              {dialog.kind === 'suspend' ? (
                <p>
                  The Organization is suspended at the platform — its Applications stop — until you resume it. {money(dialog.row.overdue, dialog.row.currency)} is overdue, oldest {oldestDueText(dialog.row.oldest_days)}.
                </p>
              ) : (
                <p>
                  Lifts the suspension this product holds{dialog.row.suspended_at ? ` since ${when(dialog.row.suspended_at)}` : ''}
                  {dialog.row.suspension_source ? ` (by ${dialog.row.suspension_source})` : ''}. {toNumber(dialog.row.overdue) > 0 ? `${money(dialog.row.overdue, dialog.row.currency)} is still overdue.` : 'Nothing is overdue.'}
                </p>
              )}
              <Field label="Reason" help="Kept on the suspension record and shown to the customer.">
                <input value={reason} onChange={(e) => setReason(e.target.value)} placeholder={dialog.kind === 'suspend' ? 'invoice 60 days overdue, no response' : 'payment received'} autoFocus />
              </Field>
            </div>
          }
        />
      ) : null}
    </div>
  )
}
