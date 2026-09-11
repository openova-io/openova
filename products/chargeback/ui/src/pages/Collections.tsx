import { useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { api } from '../api/client'
import type { AgingReport, AgingRow, BillingSettings, CollectionsRun } from '../api/types'
import { DataTable, type Column } from '../components/DataTable'
import { Badge, Confirm, Field, KPI, Notice, PageHeader, Skeleton } from '../components/ui'
import { AGING_BUCKETS, agingKPIs, oldestDueText, overdueShare, reminderScheduleText, type AgingKPIs } from '../lib/account'
import { t } from '../i18n'
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

/** A money formatter bound to a fallback currency, as the page builds one. */
type Money = (v: number | string | null | undefined, cur?: string) => string

/**
 * The three sentences this page composes out of several counts. Each was an
 * inline template with its own `=== 1 ? '' : 's'`; each is now catalogue
 * entries joined with the punctuation that separates them, which is what lets
 * a locale spell the counted nouns its own way without owning the layout.
 */
function subtitle(rep: AgingReport, k: AgingKPIs, schedule: string, settings: BillingSettings | null): string {
  const parts = [t('collections.agingAsOf', { day: day(rep.as_of) }), `${t('collections.openInvoices', { count: k.invoices })} ${t('collections.acrossCustomers', { count: k.customers })}`]
  if (schedule) parts.push(t('collections.remindersAt', { schedule }))
  if (settings?.escalation_days) parts.push(t('collections.escalateAfter', { days: settings.escalation_days, action: t(settings.escalation_action === 'suspend' ? 'collections.actionSuspend' : 'collections.actionNotify') }))
  return parts.join(' · ')
}

function runSummary(run: CollectionsRun): string {
  const parts = [t('collections.reminders', { count: run.reminders }), t('collections.escalations', { count: run.escalations }), t('collections.suspendedCount', { count: run.suspended }), t('collections.resumedCount', { count: run.resumed })]
  if (run.mails) parts.push(t('collections.mailsSent', { count: run.mails }))
  if (run.errors) parts.push(t('collections.runErrors', { count: run.errors }))
  return `${t('collections.evaluated', { count: run.invoices })}: ${parts.join(', ')}.`
}

function resumeBody(row: AgingRow, money: Money): string {
  let held = t('collections.resumeHolds')
  if (row.suspended_at) held += ` ${t('collections.resumeSince', { when: when(row.suspended_at) })}`
  if (row.suspension_source) held += ` ${t('collections.resumeBy', { source: row.suspension_source })}`
  const owing = toNumber(row.overdue) > 0 ? t('collections.resumeStillOverdue', { amount: money(row.overdue, row.currency) }) : t('collections.resumeNothingOverdue')
  return `${held}. ${owing}`
}

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
    const ok = await act.run(t('collections.ranLabel'), async () => setRun(await api.post<CollectionsRun>('/collections/run', {})), rep.reload)
    if (ok) setDialog(null)
  }

  const enforce = async (kind: 'suspend' | 'resume', row: AgingRow) => {
    const ok = await act.run(t(kind === 'suspend' ? 'collections.suspendedLabel' : 'collections.resumedLabel', { customer: row.customer_name }), () => api.post(`/customers/${row.customer_id}/${kind}`, { reason: reason.trim() }), rep.reload)
    if (ok) {
      setDialog(null)
      setReason('')
    }
  }

  const columns: Column<AgingRow>[] = [
    {
      key: 'customer',
      header: t('collections.col.customer'),
      value: (r) => r.customer_name,
      render: (r) => (
        <>
          <Link to={`/customers/${r.customer_id}?tab=account`}>{r.customer_name}</Link>
          <span className="sub">
            <span className="mono">{r.customer_slug}</span> · {t('collections.openInvoices', { count: r.invoices })}
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
      header: t('collections.col.totalOwed'),
      numeric: true,
      value: (r) => toNumber(r.total),
      render: (r) => <b>{money(r.total, r.currency)}</b>,
      total: (rs) => <b>{money(rs.reduce((n, r) => n + toNumber(r.total), 0))}</b>,
    },
    {
      key: 'overdue',
      header: t('collections.col.overdue'),
      numeric: true,
      value: (r) => toNumber(r.overdue),
      render: (r) => (toNumber(r.overdue) > 0 ? <span className="bad">{money(r.overdue, r.currency)}</span> : <span className="ok">{t('collections.nothingOverdue')}</span>),
      total: (rs) => money(rs.reduce((n, r) => n + toNumber(r.overdue), 0)),
    },
    { key: 'oldest', header: t('collections.col.oldestDue'), numeric: true, value: (r) => r.oldest_days, render: (r) => <span className={r.oldest_days > 0 ? 'bad' : 'muted'}>{oldestDueText(r.oldest_days)}</span> },
    {
      key: 'credit',
      header: t('collections.col.creditAvailable'),
      numeric: true,
      value: (r) => toNumber(r.available_credit),
      render: (r) => (toNumber(r.available_credit) > 0 ? <span className="ok">{money(r.available_credit, r.currency)}</span> : <span className="muted">{t('common.none')}</span>),
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
            {open === r.customer_id ? t('collections.hideInvoices') : t('collections.showInvoices')}
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
              {t('collections.resume')}
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
              {t('collections.suspend')}
            </button>
          )}
        </span>
      ),
    },
  ]

  return (
    <div className="stack">
      <PageHeader
        title={t('collections.title')}
        sub={rep.data ? subtitle(rep.data, k, schedule, settings.data) : t('collections.subFallback')}
        actions={
          <>
            <Link to="/billing">
              <button>{t('collections.reminderSchedule')}</button>
            </Link>
            {canCollect ? (
              <button className="primary" onClick={() => setDialog({ kind: 'run' })} disabled={act.busy}>
                {t('collections.runNow')}
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
          <Notice kind="warn">{run.skip_reason ? t('collections.didNotRunBecause', { reason: run.skip_reason }) : t('collections.didNotRun')}</Notice>
        ) : (
          <Notice kind={run.errors ? 'warn' : 'ok'}>{runSummary(run)}</Notice>
        )
      ) : null}
      {external ? <Notice kind="info">{t('collections.externalNotice')}</Notice> : null}

      <div className="kpis">
        <KPI label={t('collections.kpi.totalOwed')} value={money(k.total)} note={t('collections.openInvoices', { count: k.invoices })} />
        <KPI label={t('collections.kpi.overdue')} value={money(k.overdue)} note={share === null ? t('collections.nothingOwed') : t('collections.shareOfOwed', { pct: formatPct(share * 100) })} tone={k.overdue > 0 ? 'bad' : undefined} />
        <KPI label={t('collections.kpi.customersOverdue')} value={k.customersOverdue} note={k.customersOverdue ? t('collections.ofWithOpenInvoice', { count: k.customers }) : t('collections.everyoneWithinTerms')} tone={k.customersOverdue ? 'warn' : undefined} />
        <KPI label={t('collections.kpi.suspended')} value={rows.filter((r) => r.suspended).length} note={t('collections.heldAtPlatform')} tone={rows.some((r) => r.suspended) ? 'bad' : undefined} />
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
            label={t('collections.tableLabel')}
            defaultSort={{ key: 'overdue', dir: 'desc' }}
            csvName="aging"
            selectedKey={open}
            emptyTitle={t('collections.emptyTitle')}
            emptyBody={t('collections.emptyBody')}
            footNote={t('collections.footNote')}
            expanded={(r) =>
              open === r.customer_id ? (
                <table aria-label={t('collections.invoicesOf', { customer: r.customer_name })}>
                  <thead>
                    <tr>
                      <th>{t('collections.col.invoice')}</th>
                      <th>{t('collections.col.due')}</th>
                      <th>{t('collections.col.bucket')}</th>
                      <th>{t('common.status')}</th>
                      <th className="num">{t('collections.col.outstanding')}</th>
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
          title={t('collections.runTitle')}
          confirmLabel={t('collections.runConfirm')}
          busy={act.busy}
          onClose={() => setDialog(null)}
          onConfirm={runNow}
          body={
            <div className="stack tight">
              <p>{schedule ? t('collections.runBodyWithSchedule', { schedule }) : t('collections.runBody')}</p>
              <p className="muted small" style={{ margin: 0 }}>
                {t('collections.runNote')}
              </p>
            </div>
          }
        />
      ) : null}
      {dialog?.kind === 'suspend' || dialog?.kind === 'resume' ? (
        <Confirm
          title={t(dialog.kind === 'suspend' ? 'collections.suspendTitle' : 'collections.resumeTitle', { customer: dialog.row.customer_name })}
          danger={dialog.kind === 'suspend'}
          confirmLabel={t(dialog.kind === 'suspend' ? 'collections.suspend' : 'collections.resume')}
          busy={act.busy}
          onClose={() => setDialog(null)}
          onConfirm={() => enforce(dialog.kind, dialog.row)}
          body={
            <div className="stack tight">
              {dialog.kind === 'suspend' ? (
                <p>{t('collections.suspendBody', { amount: money(dialog.row.overdue, dialog.row.currency), oldest: oldestDueText(dialog.row.oldest_days) })}</p>
              ) : (
                <p>{resumeBody(dialog.row, money)}</p>
              )}
              <Field label={t('collections.reason')} help={t('collections.reasonHelp')}>
                <input value={reason} onChange={(e) => setReason(e.target.value)} placeholder={t(dialog.kind === 'suspend' ? 'collections.reasonSuspendPlaceholder' : 'collections.reasonResumePlaceholder')} autoFocus />
              </Field>
            </div>
          }
        />
      ) : null}
    </div>
  )
}
