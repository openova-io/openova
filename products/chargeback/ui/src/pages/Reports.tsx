import { useMemo, useState, type FormEvent } from 'react'
import { api, asList, errorText } from '../api/client'
import { REPORT_SECTIONS, type Customer, type ReportDelivery, type ReportPreview, type ReportSchedule, type ReportSendResult, type ReportSection } from '../api/types'
import { DataTable, type Column } from '../components/DataTable'
import { Badge, Confirm, EmptyState, Field, KPI, Modal, Notice, PageHeader, Skeleton } from '../components/ui'
import { when } from '../lib/format'
import { hasErrors, type Errors } from '../lib/forms'
import { CADENCES, WEEKDAYS, cadenceLabel, deliveryStats, emptyReportForm, nextRunText, reportBody, reportForm, sectionLabels, validateReportForm, windowLabel, type ReportForm } from '../lib/reports'
import { operatorLens, type Lens } from '../lib/scope'
import { customerName, useCustomers } from '../lib/useCustomers'
import { useQuery } from '../lib/useQuery'

/**
 * Scheduled cost reports (#6867 follow-up): who gets the plain-text cost
 * report, how often, with which sections. The operator page lists every
 * schedule; the customer lens (/my/reports) lists the customer's own, and a
 * customer-admin may manage those. Preview shows the exact text the next
 * send would mail; Send now mails it today for the cadence's window.
 */

type Dialog =
  | { kind: 'create' }
  | { kind: 'edit'; r: ReportSchedule }
  | { kind: 'delete'; r: ReportSchedule }
  | { kind: 'preview'; r: ReportSchedule }
  | { kind: 'deliveries'; r: ReportSchedule }
  | null

export function Reports() {
  return <ReportsBody lens={operatorLens()} canManage />
}

export function ReportsBody({ lens, canManage }: { lens: Lens; canManage: boolean }) {
  const list = useQuery<unknown>(lens.reports)
  const rows = useMemo(() => asList<ReportSchedule>(list.data, 'schedules'), [list.data])
  const { customers } = useCustomers()
  const [dialog, setDialog] = useState<Dialog>(null)
  const [error, setError] = useState('')
  const [flash, setFlash] = useState('')
  const [busy, setBusy] = useState('')
  const stats = useMemo(() => deliveryStats(rows), [rows])
  const nextDue = useMemo(() => {
    const due = rows.filter((r) => r.active && r.next_at).map((r) => r.next_at)
    due.sort()
    return due[0] ?? null
  }, [rows])
  const scopeOf = (r: ReportSchedule) => (r.customer_id ? customerName(customers, r.customer_id, r.customer_name) : 'All customers')

  const sendNow = async (r: ReportSchedule) => {
    setBusy(r.id)
    setError('')
    setFlash('')
    try {
      const res = await api.post<ReportSendResult>(`/reports/schedules/${r.id}/send`)
      setFlash(`Sent “${res.subject}” to ${res.sent_to.join(', ')}`)
      await list.reload()
    } catch (e) {
      setError(errorText(e))
    } finally {
      setBusy('')
    }
  }
  const remove = async (r: ReportSchedule) => {
    setBusy(r.id)
    setError('')
    try {
      await api.del(`/reports/schedules/${r.id}`)
      setDialog(null)
      setFlash(`${r.name} deleted`)
      await list.reload()
    } catch (e) {
      setError(errorText(e))
      setDialog(null)
    } finally {
      setBusy('')
    }
  }

  const columns: Column<ReportSchedule>[] = [
    {
      key: 'name',
      header: 'Report',
      value: (r) => r.name,
      render: (r) => (
        <>
          <div className="row" style={{ gap: 6 }}>
            <b>{r.name}</b>
            {!r.active ? <Badge status="inactive" /> : null}
          </div>
          {lens.operator ? <div className="muted small">{scopeOf(r)}</div> : null}
        </>
      ),
    },
    {
      key: 'cadence',
      header: 'Cadence',
      value: (r) => `${r.cadence}:${r.day_of_week ?? ''}:${r.day_of_month ?? ''}:${r.hour_utc}`,
      render: (r) => (
        <>
          <div>{cadenceLabel(r)}</div>
          <div className="muted small">covers {windowLabel(r.cadence)}</div>
        </>
      ),
    },
    {
      key: 'recipients',
      header: 'Recipients',
      value: (r) => r.recipients.length,
      render: (r) => (
        <span title={r.recipients.join(', ')}>
          {r.recipients.slice(0, 2).join(', ')}
          {r.recipients.length > 2 ? <span className="muted"> +{r.recipients.length - 2}</span> : null}
        </span>
      ),
    },
    { key: 'sections', header: 'Sections', value: (r) => sectionLabels(r.sections), render: (r) => <span className="small">{sectionLabels(r.sections)}</span> },
    {
      key: 'next_at',
      header: 'Next run',
      value: (r) => r.next_at,
      render: (r) =>
        r.active ? (
          <>
            <div>{when(r.next_at)}</div>
            <div className="muted small">{nextRunText(r.next_at)}</div>
          </>
        ) : (
          <span className="muted">paused</span>
        ),
    },
    {
      key: 'last_sent_at',
      header: 'Last sent',
      value: (r) => r.last_sent_at ?? '',
      render: (r) => (
        <>
          <div>{r.last_sent_at ? when(r.last_sent_at) : <span className="muted">never</span>}</div>
          {r.failed_30d > 0 ? (
            <div className="bad small" title={r.last_error ?? ''}>
              {r.failed_30d} failed in 30 d
            </div>
          ) : r.sent_30d > 0 ? (
            <div className="muted small">{r.sent_30d} sent in 30 d</div>
          ) : null}
        </>
      ),
    },
    {
      key: 'actions',
      header: '',
      value: () => '',
      sortable: false,
      render: (r) => (
        <div className="row" style={{ justifyContent: 'flex-end', flexWrap: 'nowrap' }}>
          <button className="small" onClick={() => setDialog({ kind: 'preview', r })}>
            Preview
          </button>
          <button className="small" onClick={() => setDialog({ kind: 'deliveries', r })}>
            Deliveries
          </button>
          {canManage ? (
            <>
              <button className="small" disabled={busy === r.id} onClick={() => void sendNow(r)}>
                Send now
              </button>
              <button className="small" onClick={() => setDialog({ kind: 'edit', r })}>
                Edit
              </button>
              <button className="small danger" onClick={() => setDialog({ kind: 'delete', r })}>
                Delete
              </button>
            </>
          ) : null}
        </div>
      ),
    },
  ]

  return (
    <div className="stack">
      <PageHeader
        title="Reports"
        sub={
          lens.operator
            ? 'Scheduled cost reports mailed as plain text: yesterday, the last 7 days or last month, with the sections you pick. Checked every 5 minutes.'
            : 'Cost reports for your account, mailed on a schedule. A customer-admin can add or change them; the operator sees them too.'
        }
        actions={
          canManage ? (
            <button className="primary" onClick={() => setDialog({ kind: 'create' })}>
              New schedule
            </button>
          ) : undefined
        }
      />
      {list.error ? <Notice kind="bad">{list.error}</Notice> : null}
      {error ? <Notice kind="bad">{error}</Notice> : null}
      {flash ? <Notice kind="ok">{flash}</Notice> : null}

      <div className="kpis">
        <KPI label="Schedules" value={stats.schedules} note={`${stats.active} active`} />
        <KPI label="Sent" value={stats.sent30} note="deliveries in the last 30 days" />
        <KPI label="Failures" value={stats.failed30} note="deliveries that could not be sent" tone={stats.failed30 ? 'bad' : undefined} />
        <KPI label="Next due" value={nextDue ? nextRunText(nextDue) : '—'} note={nextDue ? when(nextDue) : 'no active schedule'} />
      </div>

      <div className="card">
        {list.loading && !list.data ? (
          <Skeleton lines={4} />
        ) : rows.length === 0 ? (
          <EmptyState title="No scheduled report">
            {canManage
              ? 'Nothing is mailed on a schedule. Create one to receive a daily, weekly or monthly cost report with the total, top services, budgets, anomalies and recommendations.'
              : 'No report is scheduled for this account yet.'}
          </EmptyState>
        ) : (
          <DataTable columns={columns} rows={rows} rowKey={(r) => r.id} defaultSort={{ key: 'next_at', dir: 'asc' }} dense />
        )}
      </div>

      {dialog?.kind === 'create' || dialog?.kind === 'edit' ? (
        <ReportModal
          initial={dialog.kind === 'edit' ? dialog.r : null}
          lens={lens}
          customers={customers}
          onClose={() => setDialog(null)}
          onSaved={(name) => {
            setDialog(null)
            setFlash(`${name} saved`)
            void list.reload()
          }}
        />
      ) : null}
      {dialog?.kind === 'delete' ? (
        <Confirm
          title={`Delete ${dialog.r.name}?`}
          danger
          confirmLabel="Delete schedule"
          busy={busy === dialog.r.id}
          onClose={() => setDialog(null)}
          onConfirm={() => remove(dialog.r)}
          body={
            <p>
              Stops the {dialog.r.cadence} report to {dialog.r.recipients.length} recipient{dialog.r.recipients.length === 1 ? '' : 's'} and removes its delivery log. Past sends stay in the audit trail.
            </p>
          }
        />
      ) : null}
      {dialog?.kind === 'preview' ? <PreviewModal r={dialog.r} onClose={() => setDialog(null)} /> : null}
      {dialog?.kind === 'deliveries' ? <DeliveriesModal r={dialog.r} onClose={() => setDialog(null)} /> : null}
    </div>
  )
}

function PreviewModal({ r, onClose }: { r: ReportSchedule; onClose: () => void }) {
  const q = useQuery<ReportPreview>(`/reports/schedules/${r.id}/preview`)
  return (
    <Modal title={`Preview · ${r.name}`} onClose={onClose} wide footer={<button onClick={onClose}>Close</button>}>
      {q.error ? <Notice kind="bad">{q.error}</Notice> : null}
      {q.loading && !q.data ? (
        <Skeleton lines={6} />
      ) : q.data ? (
        <div className="stack tight">
          <div className="muted small">
            Window {q.data.window_from} → {q.data.window_to} (exclusive) · to {q.data.recipients.join(', ')} · exactly what the next send mails
          </div>
          <div>
            <b>Subject:</b> {q.data.subject}
          </div>
          <pre className="details" style={{ whiteSpace: 'pre', overflowX: 'auto', fontSize: 12, lineHeight: 1.45 }}>
            {q.data.body}
          </pre>
        </div>
      ) : null}
    </Modal>
  )
}

function DeliveriesModal({ r, onClose }: { r: ReportSchedule; onClose: () => void }) {
  const q = useQuery<unknown>(`/reports/schedules/${r.id}/deliveries`)
  const rows = useMemo(() => asList<ReportDelivery>(q.data, 'deliveries'), [q.data])
  return (
    <Modal title={`Deliveries · ${r.name}`} onClose={onClose} wide footer={<button onClick={onClose}>Close</button>}>
      {q.error ? <Notice kind="bad">{q.error}</Notice> : null}
      {q.loading && !q.data ? (
        <Skeleton lines={4} />
      ) : rows.length === 0 ? (
        <EmptyState title="Nothing sent yet">The first delivery is due {r.active ? `${nextRunText(r.next_at)} (${when(r.next_at)})` : 'once the schedule is active'}.</EmptyState>
      ) : (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>Sent</th>
                <th>Window</th>
                <th>Recipients</th>
                <th>Subject</th>
                <th>Result</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((d) => (
                <tr key={d.id}>
                  <td>{when(d.sent_at)}</td>
                  <td className="small">
                    {d.window_from} → {d.window_to}
                  </td>
                  <td className="small" title={d.recipients.join(', ')}>
                    {d.recipients.length}
                  </td>
                  <td className="small">{d.subject || <span className="muted">—</span>}</td>
                  <td>
                    <Badge status={d.ok ? 'sent' : 'failed'} kind={d.ok ? 'ok' : 'bad'} />
                    {d.error ? <div className="bad small">{d.error}</div> : null}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Modal>
  )
}

function ReportModal({ initial, lens, customers, onClose, onSaved }: { initial: ReportSchedule | null; lens: Lens; customers: Customer[]; onClose: () => void; onSaved: (name: string) => void }) {
  const [f, setF] = useState<ReportForm>(() => (initial ? reportForm(initial) : emptyReportForm(lens.customerId ?? '')))
  const [errors, setErrors] = useState<Errors<ReportForm>>({})
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const set = (patch: Partial<ReportForm>) => setF((d) => ({ ...d, ...patch }))
  const toggle = (s: ReportSection) => set({ sections: f.sections.includes(s) ? f.sections.filter((x) => x !== s) : [...f.sections, s] })

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    const errs = validateReportForm(f)
    setErrors(errs)
    if (hasErrors(errs)) return
    setBusy(true)
    setError('')
    const body = reportBody(f, lens.customerId)
    try {
      if (initial) await api.put(`/reports/schedules/${initial.id}`, body)
      else await api.post('/reports/schedules', body)
      onSaved(String(body.name))
    } catch (err) {
      setError(errorText(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      title={initial ? `Edit ${initial.name}` : 'New scheduled report'}
      onClose={onClose}
      footer={
        <>
          <button type="button" onClick={onClose} disabled={busy}>
            Cancel
          </button>
          <button className="primary" form="report-form" disabled={busy}>
            {initial ? 'Save' : 'Create'}
          </button>
        </>
      }
    >
      <form id="report-form" onSubmit={submit} className="stack tight">
        {error ? <Notice kind="bad">{error}</Notice> : null}
        <div className="grid2">
          <Field label="Name" error={errors.name}>
            <input value={f.name} onChange={(e) => set({ name: e.target.value })} autoFocus placeholder="e.g. Weekly ops report" />
          </Field>
          {lens.operator && !lens.customerId ? (
            <Field label="Scope" help="All customers reports the Sovereign-wide total and lists the top customers">
              <select value={f.customer_id} onChange={(e) => set({ customer_id: e.target.value })}>
                <option value="">All customers</option>
                {customers.map((c) => (
                  <option key={c.id} value={c.id}>
                    {c.name}
                  </option>
                ))}
              </select>
            </Field>
          ) : (
            <Field label="Scope">
              <input value={lens.customerId ? customerName(customers, lens.customerId, initial?.customer_name) : 'This account'} disabled />
            </Field>
          )}
          <Field label="Cadence" error={errors.cadence} help={`Covers ${windowLabel(f.cadence)}`}>
            <select value={f.cadence} onChange={(e) => set({ cadence: e.target.value as ReportForm['cadence'] })}>
              {CADENCES.map((c) => (
                <option key={c.value} value={c.value}>
                  {c.label}
                </option>
              ))}
            </select>
          </Field>
          {f.cadence === 'weekly' ? (
            <Field label="Weekday" error={errors.day_of_week}>
              <select value={f.day_of_week} onChange={(e) => set({ day_of_week: e.target.value })}>
                {WEEKDAYS.map((d, i) => (
                  <option key={d} value={String(i)}>
                    {d}
                  </option>
                ))}
              </select>
            </Field>
          ) : f.cadence === 'monthly' ? (
            <Field label="Day of month" error={errors.day_of_month} help="1–28, so every month has it">
              <input type="number" min={1} max={28} value={f.day_of_month} onChange={(e) => set({ day_of_month: e.target.value })} />
            </Field>
          ) : (
            <Field label="Day">
              <input value="every day" disabled />
            </Field>
          )}
          <Field label="Hour (UTC)" error={errors.hour_utc} help="Yesterday's collection is complete by 01:00 UTC">
            <select value={f.hour_utc} onChange={(e) => set({ hour_utc: e.target.value })}>
              {Array.from({ length: 24 }, (_, h) => (
                <option key={h} value={String(h)}>
                  {String(h).padStart(2, '0')}:00
                </option>
              ))}
            </select>
          </Field>
          <label className="check" style={{ alignSelf: 'end', paddingBottom: 8 }}>
            <input type="checkbox" checked={f.active} onChange={(e) => set({ active: e.target.checked })} /> Active — sent on schedule
          </label>
        </div>
        <Field label="Recipients" error={errors.recipients} help="Comma-separated email addresses, up to 20">
          <input value={f.recipients} onChange={(e) => set({ recipients: e.target.value })} placeholder="finance@example.com, ops@example.com" />
        </Field>
        <Field label="Sections" error={errors.sections}>
          <div className="stack tight">
            {REPORT_SECTIONS.filter((s) => s.value !== 'customers' || (lens.operator && !f.customer_id)).map((s) => (
              <label className="check" key={s.value}>
                <input type="checkbox" checked={f.sections.includes(s.value)} onChange={() => toggle(s.value)} /> {s.label}
                <span className="muted small"> — {s.hint}</span>
              </label>
            ))}
          </div>
        </Field>
      </form>
    </Modal>
  )
}
