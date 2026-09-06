import { useEffect, useMemo, useState, type FormEvent } from 'react'
import { api, asList, errorText } from '../api/client'
import type { Budget, BudgetStatus, Customer } from '../api/types'
import { ProgressBar } from '../components/charts'
import { Badge, Confirm, EmptyState, Field, KPI, Modal, Notice, PageHeader, Skeleton } from '../components/ui'
import { parseEmails, parseThresholds } from '../lib/budgets'
import { presetWindow, describeWindow } from '../lib/dates'
import { when } from '../lib/format'
import { formatMoney, formatPct } from '../lib/money'
import { toNumber } from '../lib/num'
import { customerName, useCustomers } from '../lib/useCustomers'
import { useQuery } from '../lib/useQuery'

/**
 * Budgets (DESIGN.md §2.7, API §3.5): a monthly amount per scope with alert
 * thresholds. Status for the current month comes from /budgets/{id}/status,
 * fetched together once the list is known.
 */

interface Row {
  budget: Budget
  status: BudgetStatus | null
  statusError: string
}
type Dialog = { kind: 'create' } | { kind: 'edit'; b: Budget } | { kind: 'delete'; b: Budget } | null

export function Budgets() {
  const list = useQuery<unknown>('/budgets')
  const { customers } = useCustomers()
  const budgets = useMemo(() => asList<Budget>(list.data, 'budgets'), [list.data])
  const [statuses, setStatuses] = useState<Record<string, { s: BudgetStatus | null; e: string }>>({})
  const [statusesFor, setStatusesFor] = useState('')
  const [dialog, setDialog] = useState<Dialog>(null)
  const [error, setError] = useState('')
  const [flash, setFlash] = useState('')
  const [busy, setBusy] = useState(false)
  const month = useMemo(() => presetWindow('mtd'), [])

  useEffect(() => {
    let cancelled = false
    const key = budgets.map((b) => `${b.id}:${b.updated_at ?? ''}`).join('|')
    setStatusesFor('')
    void Promise.all(
      budgets.map((b) =>
        api
          .get<BudgetStatus>(`/budgets/${b.id}/status`)
          .then((s) => [b.id, { s, e: '' }] as const)
          .catch((e: unknown) => [b.id, { s: null, e: errorText(e) }] as const),
      ),
    ).then((pairs) => {
      if (cancelled) return
      setStatuses(Object.fromEntries(pairs))
      setStatusesFor(key)
    })
    return () => {
      cancelled = true
    }
  }, [budgets])

  const rows: Row[] = budgets.map((b) => ({ budget: b, status: statuses[b.id]?.s ?? null, statusError: statuses[b.id]?.e ?? '' }))
  const ready = statusesFor !== '' || budgets.length === 0
  const byStatus = (s: string) => rows.filter((r) => r.status?.status === s).length
  const scopeOf = (b: Budget) => (b.customer_id ? customerName(customers, b.customer_id, b.customer_name) : 'All customers')

  const remove = async (b: Budget) => {
    setBusy(true)
    setError('')
    try {
      await api.del(`/budgets/${b.id}`)
      setDialog(null)
      setFlash(`${b.name} deleted`)
      await list.reload()
    } catch (e) {
      setError(errorText(e))
      setDialog(null)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="stack">
      <PageHeader
        title="Budgets"
        sub={`${describeWindow(month)} to date · evaluated hourly; each threshold alerts once per month to the notify addresses`}
        actions={
          <button className="primary" onClick={() => setDialog({ kind: 'create' })}>
            New budget
          </button>
        }
      />
      {list.error ? <Notice kind="bad">{list.error}</Notice> : null}
      {error ? <Notice kind="bad">{error}</Notice> : null}
      {flash ? <Notice kind="ok">{flash}</Notice> : null}

      <div className="kpis">
        <KPI label="Budgets" value={budgets.length} note={`${budgets.filter((b) => b.active).length} active`} />
        <KPI label="Exceeded" value={ready ? byStatus('exceeded') : '…'} note="actual spend is over the amount" tone={byStatus('exceeded') ? 'bad' : undefined} />
        <KPI label="Warning" value={ready ? byStatus('warning') : '…'} note="a threshold was crossed, or the forecast overruns" tone={byStatus('warning') ? 'warn' : undefined} />
        <KPI label="On track" value={ready ? byStatus('ok') : '…'} note="under every threshold" tone={ready && byStatus('ok') === rows.length && rows.length ? 'ok' : undefined} />
      </div>

      <div className="card">
        {list.loading && !list.data ? (
          <Skeleton lines={4} />
        ) : rows.length === 0 ? (
          <EmptyState title="No budget yet">
            Nothing watches spend against a limit. Set a monthly amount for all customers or one customer; alerts fire at the thresholds you list (50 / 80 / 100 % by default).
          </EmptyState>
        ) : (
          <div className="strip">
            {rows.map(({ budget: b, status: st, statusError }) => {
              const crossed = st?.thresholds.filter((t) => t.crossed) ?? []
              return (
                <div className="strip-row" key={b.id}>
                  <div>
                    <div className="row" style={{ gap: 6 }}>
                      <b>{b.name}</b>
                      {!b.active ? <Badge status="inactive" /> : null}
                    </div>
                    <div className="muted small">
                      {scopeOf(b)} · {formatMoney(b.amount, b.currency)} / month
                    </div>
                    <div className="muted small">
                      thresholds {b.thresholds.map((t) => `${t} %`).join(' · ')}
                      {b.notify_emails.length ? ` · mails ${b.notify_emails.length} address${b.notify_emails.length === 1 ? '' : 'es'}` : ' · no notification address'}
                    </div>
                  </div>
                  <div>
                    {st ? (
                      <>
                        <ProgressBar
                          value={st.actual}
                          max={toNumber(st.amount) || toNumber(b.amount)}
                          thresholds={b.thresholds}
                          markers={st.forecast !== null && st.forecast !== undefined ? [{ label: 'forecast', value: st.forecast }] : []}
                          format={(v) => formatMoney(v, b.currency, { compact: true })}
                        />
                        <div className="muted small">
                          {formatMoney(st.actual, b.currency)} spent ({formatPct(st.pct_actual, { digits: 0 })})
                          {st.forecast !== null && st.forecast !== undefined ? ` · forecast ${formatMoney(st.forecast, b.currency)} (${formatPct(st.pct_forecast, { digits: 0 })})` : ' · no forecast yet'}
                          {crossed.length ? ` · crossed ${crossed.map((t) => `${t.pct} %${t.alerted_at ? ` at ${when(t.alerted_at)}` : ''}`).join(', ')}` : ' · no threshold crossed'}
                        </div>
                      </>
                    ) : statusError ? (
                      <span className="bad small">{statusError}</span>
                    ) : (
                      <Skeleton lines={1} />
                    )}
                  </div>
                  <div className="row" style={{ justifyContent: 'flex-end' }}>
                    <Badge status={st ? st.status : b.active ? 'pending' : 'inactive'} />
                    <button className="small" onClick={() => setDialog({ kind: 'edit', b })}>
                      Edit
                    </button>
                    <button className="small danger" onClick={() => setDialog({ kind: 'delete', b })}>
                      Delete
                    </button>
                  </div>
                </div>
              )
            })}
          </div>
        )}
      </div>

      {dialog?.kind === 'create' || dialog?.kind === 'edit' ? (
        <BudgetModal
          initial={dialog.kind === 'edit' ? dialog.b : null}
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
          title={`Delete ${dialog.b.name}?`}
          danger
          confirmLabel="Delete budget"
          busy={busy}
          onClose={() => setDialog(null)}
          onConfirm={() => remove(dialog.b)}
          body={<p>Stops the hourly evaluation and its alerts for {scopeOf(dialog.b)}. Past alert records are kept in the audit trail.</p>}
        />
      ) : null}
    </div>
  )
}

interface Draft {
  name: string
  customer_id: string
  amount: string
  currency: string
  thresholds: string
  notify_emails: string
  active: boolean
}

function draftOf(b: Budget | null): Draft {
  return {
    name: b?.name ?? '',
    customer_id: b?.customer_id ?? '',
    amount: b ? String(toNumber(b.amount)) : '',
    currency: b?.currency ?? 'OMR',
    thresholds: (b?.thresholds ?? [50, 80, 100]).join(', '),
    notify_emails: (b?.notify_emails ?? []).join(', '),
    active: b?.active ?? true,
  }
}

function BudgetModal({ initial, customers, onClose, onSaved }: { initial: Budget | null; customers: Customer[]; onClose: () => void; onSaved: (name: string) => void }) {
  const [draft, setDraft] = useState<Draft>(() => draftOf(initial))
  const [errors, setErrors] = useState<Partial<Record<keyof Draft, string>>>({})
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const set = (patch: Partial<Draft>) => setDraft((d) => ({ ...d, ...patch }))

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    const errs: Partial<Record<keyof Draft, string>> = {}
    if (!draft.name.trim()) errs.name = 'Name is required'
    const amount = Number(draft.amount)
    if (draft.amount.trim() === '' || !Number.isFinite(amount) || amount <= 0) errs.amount = 'Amount must be above 0'
    if (!/^[A-Za-z]{3}$/.test(draft.currency.trim())) errs.currency = 'Three-letter ISO code, e.g. OMR'
    const th = parseThresholds(draft.thresholds)
    if (th.error) errs.thresholds = th.error
    const em = parseEmails(draft.notify_emails)
    if (em.error) errs.notify_emails = em.error
    setErrors(errs)
    if (Object.keys(errs).length) return
    setBusy(true)
    setError('')
    const body = {
      name: draft.name.trim(),
      customer_id: draft.customer_id || null,
      amount,
      currency: draft.currency.trim().toUpperCase(),
      period: 'monthly',
      thresholds: th.values,
      notify_emails: em.values,
      active: draft.active,
    }
    try {
      if (initial) await api.put(`/budgets/${initial.id}`, body)
      else await api.post('/budgets', body)
      onSaved(body.name)
    } catch (err) {
      setError(errorText(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      title={initial ? `Edit ${initial.name}` : 'New budget'}
      onClose={onClose}
      footer={
        <>
          <button type="button" onClick={onClose} disabled={busy}>
            Cancel
          </button>
          <button className="primary" form="budget-form" disabled={busy}>
            {initial ? 'Save' : 'Create'}
          </button>
        </>
      }
    >
      <form id="budget-form" onSubmit={submit} className="stack tight">
        {error ? <Notice kind="bad">{error}</Notice> : null}
        <div className="grid2">
          <Field label="Name" error={errors.name}>
            <input value={draft.name} onChange={(e) => set({ name: e.target.value })} autoFocus placeholder="e.g. Compute ceiling" />
          </Field>
          <Field label="Scope" help="All customers watches the Sovereign-wide total">
            <select value={draft.customer_id} onChange={(e) => set({ customer_id: e.target.value })}>
              <option value="">All customers</option>
              {customers.map((c) => (
                <option key={c.id} value={c.id}>
                  {c.name}
                </option>
              ))}
            </select>
          </Field>
          <Field label="Monthly amount" error={errors.amount}>
            <input type="number" step="any" min={0} value={draft.amount} onChange={(e) => set({ amount: e.target.value })} />
          </Field>
          <Field label="Currency" error={errors.currency} help="Compared against cost in this currency">
            <input value={draft.currency} maxLength={3} onChange={(e) => set({ currency: e.target.value.toUpperCase() })} />
          </Field>
        </div>
        <Field label="Alert thresholds (% of amount)" error={errors.thresholds} help="Comma-separated whole percentages, ascending; each alerts once per month">
          <input value={draft.thresholds} onChange={(e) => set({ thresholds: e.target.value })} placeholder="50, 80, 100" />
        </Field>
        <Field label="Notify" error={errors.notify_emails} help="Comma-separated email addresses; empty = record the crossing only">
          <input value={draft.notify_emails} onChange={(e) => set({ notify_emails: e.target.value })} placeholder="finance@example.com, ops@example.com" />
        </Field>
        <label className="check">
          <input type="checkbox" checked={draft.active} onChange={(e) => set({ active: e.target.checked })} /> Active — evaluated every hour
        </label>
      </form>
    </Modal>
  )
}
