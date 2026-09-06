import { useEffect, useMemo, useState, type FormEvent } from 'react'
import { api, asList, errorText } from '../api/client'
import type { Budget, BudgetStatus } from '../api/types'
import { ProgressBar } from '../components/charts'
import { DataTable, type Column } from '../components/DataTable'
import { Badge, Confirm, Field, Modal, Notice, Skeleton } from '../components/ui'
import { budgetForm, budgetView, sortBudgetViews, type BudgetView } from '../lib/budgets'
import { budgetBody, currentPeriod, emptyBudgetForm, hasErrors, validateBudget, type BudgetForm, type Errors } from '../lib/forms'
import { formatMoney, formatPct } from '../lib/money'
import { useAction } from '../lib/useAction'
import { useQuery } from '../lib/useQuery'

type Dialog = { kind: 'create' } | { kind: 'edit'; budget: Budget } | { kind: 'delete'; budget: Budget } | null

/**
 * Monthly budgets of one customer (#6867, DESIGN.md §3.5) with this month's
 * standing from GET /budgets/{id}/status. Operator: CRUD; customer: read.
 */
export function BudgetsPanel({ customerId, canManage, currency }: { customerId: string; canManage: boolean; currency: string }) {
  const period = currentPeriod()
  const q = useQuery<unknown>(`/customers/${customerId}/budgets`)
  const budgets = useMemo(() => asList<Budget>(q.data, 'budgets'), [q.data])
  const [statuses, setStatuses] = useState<Record<string, { status: BudgetStatus | null; error: string }>>({})
  const act = useAction()
  const [dialog, setDialog] = useState<Dialog>(null)
  const close = () => setDialog(null)

  useEffect(() => {
    let live = true
    if (budgets.length === 0) {
      setStatuses({})
      return
    }
    void Promise.all(
      budgets.map(async (b) => {
        try {
          return [b.id, { status: await api.get<BudgetStatus>(`/budgets/${b.id}/status?period=${period}`), error: '' }] as const
        } catch (e) {
          return [b.id, { status: null, error: errorText(e) }] as const
        }
      }),
    ).then((pairs) => {
      if (live) setStatuses(Object.fromEntries(pairs))
    })
    return () => {
      live = false
    }
  }, [budgets, period])

  const views = useMemo(() => sortBudgetViews(budgets.map((b) => budgetView(b, statuses[b.id]?.status ?? null, statuses[b.id]?.error ?? ''))), [budgets, statuses])
  const money = (v: number | null | undefined, cur: string) => formatMoney(v, cur, { compact: true })

  const columns: Column<BudgetView>[] = [
    {
      key: 'name',
      header: 'Budget',
      value: (v) => v.budget.name,
      render: (v) => (
        <>
          {v.budget.name}
          {!v.budget.active ? <Badge status="inactive" /> : null}
          <span className="sub">
            alerts at {v.budget.thresholds.join(' / ')} % · {v.budget.notify_emails.length ? `${v.budget.notify_emails.length} recipient${v.budget.notify_emails.length === 1 ? '' : 's'}` : 'no recipients'}
          </span>
        </>
      ),
    },
    { key: 'amount', header: 'Monthly amount', value: (v) => v.amount, numeric: true, render: (v) => formatMoney(v.amount, v.budget.currency) },
    {
      key: 'actual',
      header: `Spent · ${period}`,
      value: (v) => v.actual,
      numeric: true,
      render: (v) => (v.status ? <>{money(v.actual, v.budget.currency)} <span className="muted small">{formatPct(v.pctActual, { digits: 0 })}</span></> : <span className="muted" title={v.statusError}>—</span>),
    },
    {
      key: 'forecast',
      header: 'Forecast',
      value: (v) => v.forecast ?? -1,
      numeric: true,
      render: (v) => (v.forecast === null ? <span className="muted">—</span> : <span className={v.pctForecast !== null && v.pctForecast >= 100 ? 'bad' : ''}>{money(v.forecast, v.budget.currency)} <span className="muted small">{formatPct(v.pctForecast, { digits: 0 })}</span></span>),
    },
    {
      key: 'progress',
      header: 'Progress',
      value: (v) => v.pctActual,
      sortable: false,
      width: 260,
      render: (v) => (v.status ? <ProgressBar value={v.actual} max={v.amount} thresholds={v.budget.thresholds} markers={v.forecast !== null ? [{ label: 'forecast', value: v.forecast }] : []} format={(x) => money(x, v.budget.currency)} /> : <span className="muted small">{v.statusError ? `status unavailable: ${v.statusError}` : 'loading…'}</span>),
    },
    {
      key: 'status',
      header: 'Status',
      value: (v) => v.status?.status ?? '',
      render: (v) => (v.status ? <><Badge status={v.status.status} />{v.crossed.length ? <span className="sub">crossed {v.crossed.join(', ')} %</span> : null}</> : <span className="muted">—</span>),
    },
    ...(canManage
      ? [
          {
            key: 'actions',
            header: '',
            value: () => '',
            sortable: false,
            className: 'nowrap',
            render: (v: BudgetView) => (
              <span className="btn-row">
                <button className="link small" disabled={act.busy} onClick={() => setDialog({ kind: 'edit', budget: v.budget })}>
                  Edit
                </button>
                <button className="link small danger" disabled={act.busy} onClick={() => setDialog({ kind: 'delete', budget: v.budget })}>
                  Delete
                </button>
              </span>
            ),
          } as Column<BudgetView>,
        ]
      : []),
  ]

  return (
    <div className="stack">
      <div className="row between">
        <span className="muted small">
          {budgets.length} budget{budgets.length === 1 ? '' : 's'} · {views.filter((v) => v.tone === 'bad').length} exceeded · {views.filter((v) => v.tone === 'warn').length} warning · period {period}
        </span>
        {canManage ? (
          <button className="primary" onClick={() => setDialog({ kind: 'create' })} disabled={act.busy}>
            New budget
          </button>
        ) : null}
      </div>
      {q.error ? <Notice kind="bad">{q.error}</Notice> : null}
      {act.error ? <Notice kind="bad">{act.error}</Notice> : null}
      {act.ok ? <Notice kind="ok">{act.ok}</Notice> : null}
      {q.loading && !q.data ? (
        <Skeleton lines={3} />
      ) : (
        <div className="card pad-0">
          <DataTable
            columns={columns}
            rows={views}
            rowKey={(v) => v.budget.id}
            emptyTitle="No budget for this customer"
            emptyBody={canManage ? 'Set a monthly amount to get alerts at 50 / 80 / 100 % and a forecast against it on the overview.' : 'No monthly cap is set. The operator can add one; spend against it then shows here and on your overview.'}
          />
        </div>
      )}
      <p className="muted small">A budget caps one calendar month. Every hour the actual month-to-date cost and the forecast are compared with it; each threshold mails the recipients once per month when crossed.</p>

      {dialog?.kind === 'create' || dialog?.kind === 'edit' ? <BudgetFormModal customerId={customerId} budget={dialog.kind === 'edit' ? dialog.budget : undefined} currency={currency} onClose={close} onDone={q.reload} /> : null}
      {dialog?.kind === 'delete' ? (
        <Confirm
          title="Delete budget"
          danger
          confirmLabel="Delete"
          busy={act.busy}
          onClose={close}
          onConfirm={async () => {
            const ok = await act.run(`${dialog.budget.name} deleted`, () => api.del(`/budgets/${dialog.budget.id}`), q.reload)
            if (ok) close()
          }}
          body={
            <>
              Delete <b>{dialog.budget.name}</b> ({formatMoney(dialog.budget.amount, dialog.budget.currency)} per month)? Threshold alerts stop and its history is removed. Costs and statements are not affected.
            </>
          }
        />
      ) : null}
    </div>
  )
}

function BudgetFormModal({ customerId, budget, currency, onClose, onDone }: { customerId: string; budget?: Budget; currency: string; onClose: () => void; onDone: () => void | Promise<void> }) {
  const [form, setForm] = useState<BudgetForm>(budget ? budgetForm(budget) : emptyBudgetForm(currency || 'OMR'))
  const [errors, setErrors] = useState<Errors<BudgetForm>>({})
  const act = useAction()
  const set = <K extends keyof BudgetForm>(k: K, v: BudgetForm[K]) => setForm((f) => ({ ...f, [k]: v }))

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    const errs = validateBudget(form)
    setErrors(errs)
    if (hasErrors(errs)) return
    const body = budgetBody(form, customerId)
    const ok = await act.run(budget ? `${form.name.trim()} saved` : `${form.name.trim()} created`, () => (budget ? api.put(`/budgets/${budget.id}`, body) : api.post('/budgets', body)), onDone)
    if (ok) onClose()
  }

  return (
    <Modal
      title={budget ? `Edit budget — ${budget.name}` : 'New monthly budget'}
      onClose={onClose}
      footer={
        <>
          <button type="button" onClick={onClose} disabled={act.busy}>
            Cancel
          </button>
          <button className="primary" form="budget-form" disabled={act.busy}>
            {budget ? 'Save' : 'Create'}
          </button>
        </>
      }
    >
      <form id="budget-form" onSubmit={(e) => void submit(e)}>
        {act.error ? <Notice kind="bad">{act.error}</Notice> : null}
        <Field label="Name" error={errors.name}>
          <input value={form.name} onChange={(e) => set('name', e.target.value)} placeholder="e.g. Production, monthly" autoFocus />
        </Field>
        <div className="grid2">
          <Field label="Monthly amount" error={errors.amount}>
            <input value={form.amount} onChange={(e) => set('amount', e.target.value)} inputMode="decimal" placeholder="3000" />
          </Field>
          <Field label="Currency" error={errors.currency} help="Normally the price-book currency.">
            <input value={form.currency} onChange={(e) => set('currency', e.target.value.toUpperCase())} maxLength={3} className="mono" />
          </Field>
        </div>
        <Field label="Alert thresholds (% of amount)" error={errors.thresholds} help="Comma-separated. Each is mailed once per month when the actual cost crosses it.">
          <input value={form.thresholds} onChange={(e) => set('thresholds', e.target.value)} placeholder="50, 80, 100" />
        </Field>
        <Field label="Notify" error={errors.notify_emails} help="Comma-separated email addresses. Empty = alerts are recorded but nobody is mailed.">
          <input value={form.notify_emails} onChange={(e) => set('notify_emails', e.target.value)} placeholder="finance@example.com, ops@example.com" />
        </Field>
        <label className="check">
          <input type="checkbox" checked={form.active} onChange={(e) => set('active', e.target.checked)} /> Active — evaluated hourly
        </label>
      </form>
    </Modal>
  )
}
