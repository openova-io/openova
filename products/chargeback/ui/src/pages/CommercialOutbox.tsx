import { useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { api } from '../api/client'
import type { OutboxDocument, OutboxEntry } from '../api/types'
import { useSession } from '../auth/session'
import { DataTable, type Column } from '../components/DataTable'
import { Badge, Confirm, Field, FormRow, KPI, Notice, PageHeader, Segmented, Skeleton } from '../components/ui'
import { can } from '../lib/access'
import { periodLabel, recentPeriods } from '../lib/finance'
import { when } from '../lib/format'
import { useAction } from '../lib/useAction'
import { useQuery } from '../lib/useQuery'

/**
 * Finance → Outbox (DESIGN.md §8.10, #6946): what is queued for the
 * operator's billing system, what is stuck and why, and the two things an
 * operator does about it — retry one document now, and queue the month's
 * journal onto the same lane the bills leave by.
 *
 * A row that failed says so IN ITS OWN WORDS: `last_error` is the far end's
 * answer, verbatim. Retry makes the row due now and pushes it at once; a
 * failure on that push lands on the row again rather than as a failed
 * request, because the retry did happen.
 *
 * Reading is metering.read (every Sovereign role); Retry is billing.issue —
 * the permission that raised the bill; the journal export is the finance
 * handover's audit.read + metering.read, like the journal page itself.
 */

const DOC_LABEL: Record<string, string> = {
  invoice: 'Invoice',
  'rated-usage': 'Rated usage',
  payment: 'Payment',
  journal: 'Journal',
  enforcement: 'Enforcement',
}

function docLabel(kind: string): string {
  return DOC_LABEL[kind] ?? kind
}

/** delivered · failed (a recorded error, waiting for its next attempt) · queued (never refused yet). */
function rowState(e: OutboxEntry): 'delivered' | 'failed' | 'queued' {
  if (e.delivered_at) return 'delivered'
  return e.last_error ? 'failed' : 'queued'
}

export function CommercialOutbox() {
  const { me } = useSession()
  const canRetry = can(me, 'billing.issue')
  const canExport = can(me, 'audit.read') && can(me, 'metering.read')
  const [show, setShow] = useState<'pending' | 'all'>('pending')
  const q = useQuery<OutboxDocument>(`/commercial/outbox?limit=500${show === 'all' ? '&all=1' : ''}`, [show])
  const act = useAction()
  const periods = useMemo(() => recentPeriods(), [])
  const [exporting, setExporting] = useState<string | null>(null)
  const [retrying, setRetrying] = useState<OutboxEntry | null>(null)

  const doc = q.data
  const rows = doc?.entries ?? []
  const external = doc?.commercial_provider === 'external'

  const columns: Column<OutboxEntry>[] = [
    { key: 'id', header: '#', numeric: true, width: 56, value: (e) => e.id },
    { key: 'type', header: 'Document', value: (e) => docLabel(e.doc_type), render: (e) => <Badge status={docLabel(e.doc_type)} kind="info" /> },
    {
      key: 'about',
      header: 'About',
      value: (e) => e.customer_name || e.idempotency_key,
      render: (e) => (
        <>
          {e.customer_id ? <Link to={`/customers/${e.customer_id}`}>{e.customer_name || e.customer_id}</Link> : <span className="muted">the Sovereign</span>}
          <span className="sub">
            {e.statement_id ? (
              <Link to={`/statements/${e.statement_id}`} className="mono">
                {e.statement_id.slice(0, 8)}
              </Link>
            ) : (
              <span className="mono">{e.idempotency_key}</span>
            )}
          </span>
        </>
      ),
    },
    {
      key: 'state',
      header: 'Status',
      value: (e) => rowState(e),
      render: (e) => {
        const st = rowState(e)
        return (
          <>
            <Badge status={st} kind={st === 'delivered' ? 'ok' : st === 'failed' ? 'bad' : 'warn'} />
            <span className="sub">{st === 'delivered' ? `delivered ${when(e.delivered_at)}${e.external_ref ? ` · ref ${e.external_ref}` : ''}` : `next attempt ${when(e.next_attempt_at)}`}</span>
          </>
        )
      },
    },
    { key: 'attempts', header: 'Attempts', numeric: true, value: (e) => e.attempts },
    {
      key: 'error',
      header: 'Last error',
      value: (e) => e.last_error ?? '',
      sortable: false,
      render: (e) => (e.last_error ? <span className="small bad">{e.last_error}</span> : <span className="muted">—</span>),
    },
    { key: 'created', header: 'Queued', value: (e) => e.created_at, render: (e) => <span className="nowrap">{when(e.created_at)}</span> },
    {
      key: 'actions',
      header: '',
      value: () => '',
      sortable: false,
      className: 'nowrap actions',
      render: (e) =>
        canRetry && !e.delivered_at ? (
          <button className="small" disabled={act.busy} title="Make this document due now and push it at once; the outcome lands on the row" onClick={() => setRetrying(e)}>
            Retry
          </button>
        ) : null,
    },
  ]

  return (
    <div className="stack">
      <PageHeader
        title="Outbox"
        sub={external ? "what is queued for the operator's billing system, and what it refused" : 'invoices are raised and collected here, so what is queued is the journal you export — and what the exporter answered'}
        actions={
          canExport ? (
            <button className="small primary" disabled={act.busy} title="Queue the period's journal as a document on this outbox" onClick={() => setExporting(periods[1] ?? periods[0])}>
              Export journal
            </button>
          ) : null
        }
      />
      {q.error ? <Notice kind="bad">{q.error}</Notice> : null}
      {act.error ? <Notice kind="bad">{act.error}</Notice> : null}
      {act.ok ? <Notice kind="ok">{act.ok}</Notice> : null}

      <div className="kpis">
        <KPI label="Queued" value={doc ? doc.pending : '…'} note="not yet delivered" tone={doc?.pending ? 'warn' : undefined} />
        <KPI label="Failed" value={doc ? doc.failed : '…'} note={doc?.failed ? 'refused at least once — the row says why' : 'nothing has been refused'} tone={doc?.failed ? 'bad' : undefined} />
        <KPI label="Billing system" value={doc ? (external ? 'external' : 'this product') : '…'} note={external ? 'the system of record for invoices' : 'invoices are raised and collected here'} />
      </div>

      {!canRetry ? (
        <Notice kind="info">
          Read-only: retrying a document is <code>billing.issue</code>, the permission that raised the bill.
        </Notice>
      ) : null}

      <div className="card pad-0">
        <div className="card-head" style={{ padding: '12px 12px 0' }}>
          <h2>Documents</h2>
          <Segmented
            value={show}
            ariaLabel="Outbox filter"
            options={[
              { value: 'pending', label: 'Not delivered' },
              { value: 'all', label: 'All' },
            ]}
            onChange={setShow}
          />
        </div>
        {q.loading && !doc ? (
          <div style={{ padding: 16 }}>
            <Skeleton lines={4} />
          </div>
        ) : (
          <DataTable
            label="Commercial outbox"
            columns={columns}
            rows={rows}
            rowKey={(e) => String(e.id)}
            pageSize={50}
            csvName="commercial-outbox"
            emptyTitle={show === 'pending' ? 'Nothing is waiting' : 'Nothing has been queued'}
            emptyBody={show === 'pending' ? 'Every queued document has been delivered.' : 'Issuing an invoice in external mode, a settled checkout and a journal export each queue a document here.'}
          />
        )}
      </div>

      {retrying ? (
        <Confirm
          title={`Retry ${docLabel(retrying.doc_type).toLowerCase()} #${retrying.id}`}
          confirmLabel="Retry now"
          busy={act.busy}
          onClose={() => setRetrying(null)}
          onConfirm={async () => {
            const ok = await act.run(`document #${retrying.id} was pushed again — its row shows the outcome`, () => api.post<OutboxEntry>(`/commercial/outbox/${retrying.id}/retry`, {}), async () => {
              setRetrying(null)
              await q.reload()
            })
            if (!ok) setRetrying(null)
          }}
          body={
            <p>
              The document is made due now and pushed at once{retrying.attempts ? ` (${retrying.attempts} attempt${retrying.attempts === 1 ? '' : 's'} so far)` : ''}. Delivering it twice is one document at the far end — it is keyed on{' '}
              <span className="mono">{retrying.idempotency_key}</span>. If the billing system refuses it again, the refusal lands on the row.
            </p>
          }
        />
      ) : null}
      {exporting !== null ? (
        <Confirm
          title="Export the journal"
          confirmLabel="Queue the export"
          busy={act.busy}
          onClose={() => setExporting(null)}
          onConfirm={async () => {
            const period = exporting
            const ok = await act.run(`the journal of ${periodLabel(period)} is queued on the outbox`, () => api.post(`/finance/journal/export`, { period }), async () => {
              setExporting(null)
              await q.reload()
            })
            if (!ok) setExporting(null)
          }}
          body={
            <div className="stack tight">
              <p>
                Queues the period&apos;s double-entry journal as a document on this outbox, the same lane the bills leave by. A journal that does not balance is refused before it is queued; exporting the same period twice is one document at the far end.
              </p>
              <FormRow>
                <Field label="Period">
                  <select aria-label="Journal period" value={exporting} onChange={(e) => setExporting(e.target.value)}>
                    {periods.map((p) => (
                      <option key={p} value={p}>
                        {periodLabel(p)}
                      </option>
                    ))}
                  </select>
                </Field>
              </FormRow>
            </div>
          }
        />
      ) : null}
    </div>
  )
}
