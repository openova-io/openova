import { useMemo, useState, type FormEvent } from 'react'
import { Link } from 'react-router-dom'
import { api, asList } from '../api/client'
import type { DimensionValues, Discount } from '../api/types'
import { DataTable, type Column } from '../components/DataTable'
import { Badge, Confirm, Field, Modal, Notice, Segmented, Skeleton } from '../components/ui'
import { presetWindow } from '../lib/dates'
import { discountPhase, discountScopeText, discountValueText, discountWindowText, isGlobalDiscount, phaseTone, sortDiscounts } from '../lib/discounts'
import { discountBody, emptyDiscountForm, hasErrors, validateDiscount, type DiscountForm, type Errors } from '../lib/forms'
import { useAction } from '../lib/useAction'
import { useQuery } from '../lib/useQuery'

type Dialog = { kind: 'create' } | { kind: 'edit'; discount: Discount } | { kind: 'delete'; discount: Discount } | null

/**
 * Discounts that apply to one customer (#6867): its own, editable by the
 * operator, plus the global campaigns (customer_id null) shown read-only
 * with a link to the Discounts page that owns them.
 */
export function DiscountsPanel({ customerId, canManage, currency }: { customerId: string; canManage: boolean; currency: string }) {
  const q = useQuery<unknown>(`/customers/${customerId}/discounts`)
  const rows = useMemo(() => sortDiscounts(asList<Discount>(q.data, 'discounts')), [q.data])
  const win = presetWindow('30d')
  const dims = useQuery<DimensionValues>(canManage ? `/customers/${customerId}/cost/dimensions?from=${win.from}&to=${win.to}` : null)
  const skus = dims.data?.dimensions?.sku ?? []
  const act = useAction()
  const [dialog, setDialog] = useState<Dialog>(null)
  const close = () => setDialog(null)
  const own = rows.filter((d) => !isGlobalDiscount(d))
  const global = rows.filter(isGlobalDiscount)

  const columns: Column<Discount>[] = [
    {
      key: 'name',
      header: 'Discount',
      value: (d) => d.name,
      render: (d) => (
        <>
          {d.name}
          <span className="sub">{d.sku ? <>SKU <span className="mono">{d.sku}</span></> : 'every SKU'}</span>
        </>
      ),
    },
    {
      key: 'scope',
      header: 'Scope',
      value: (d) => (isGlobalDiscount(d) ? 'all customers' : 'this customer'),
      render: (d) => (isGlobalDiscount(d) ? <Badge status="all customers" kind="info" /> : <span className="muted">{discountScopeText(d)}</span>),
    },
    { key: 'value', header: 'Value', value: (d) => Number(d.value), numeric: true, render: (d) => <>{discountValueText(d, currency)} <span className="muted small">{d.kind === 'percent' ? 'off' : 'per statement'}</span></> },
    { key: 'window', header: 'Window', value: (d) => d.starts_at ?? '', render: (d) => <span className="nowrap">{discountWindowText(d)}</span> },
    {
      key: 'phase',
      header: 'Status',
      value: (d) => discountPhase(d),
      render: (d) => {
        const p = discountPhase(d)
        return <Badge status={p} kind={phaseTone(p)} />
      },
    },
    {
      key: 'actions',
      header: '',
      value: () => '',
      sortable: false,
      className: 'nowrap actions',
      render: (d) =>
        isGlobalDiscount(d) ? (
          canManage ? (
            <Link to="/discounts" className="small">
              manage campaigns
            </Link>
          ) : null
        ) : canManage ? (
          <span className="btn-row">
            <button className="link small" disabled={act.busy} onClick={() => setDialog({ kind: 'edit', discount: d })}>
              Edit
            </button>
            <button className="link small" disabled={act.busy} onClick={() => void act.run(`${d.name} ${d.active ? 'deactivated' : 'activated'}`, () => api.patch(`/discounts/${d.id}`, { active: !d.active }), q.reload)}>
              {d.active ? 'Deactivate' : 'Activate'}
            </button>
            <button className="link small danger" disabled={act.busy} onClick={() => setDialog({ kind: 'delete', discount: d })}>
              Delete
            </button>
          </span>
        ) : null,
    },
  ]

  return (
    <div className="stack">
      <div className="row between">
        <span className="muted small">
          {own.length} for this customer · {global.length} campaign{global.length === 1 ? '' : 's'} for all customers · {rows.filter((d) => discountPhase(d) === 'live').length} live today
        </span>
        {canManage ? (
          <button className="primary" onClick={() => setDialog({ kind: 'create' })} disabled={act.busy}>
            New discount
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
            rows={rows}
            rowKey={(d) => d.id}
            emptyTitle="No discounts apply"
            emptyBody={
              canManage ? (
                <>
                  This customer pays list price. Create a discount here, or run a campaign for every customer from <Link to="/discounts">Discounts</Link>.
                </>
              ) : (
                'You are billed at the list-price rates of your price book.'
              )
            }
          />
        </div>
      )}
      <p className="muted small">
        Discounts take money off the list subtotal when a statement is run; issued statements keep the amounts they recorded. A percent discount applies to matching lines; a fixed discount is taken once per statement.
        {canManage ? ' Deactivate to pause a discount without losing its history.' : ' Discounts are granted by the operator.'}
      </p>

      {dialog?.kind === 'create' || dialog?.kind === 'edit' ? (
        <DiscountFormModal customerId={customerId} discount={dialog.kind === 'edit' ? dialog.discount : undefined} currency={currency} skus={skus.map((s) => s.key)} onClose={close} onDone={q.reload} />
      ) : null}
      {dialog?.kind === 'delete' ? (
        <Confirm
          title="Delete discount"
          danger
          confirmLabel="Delete"
          busy={act.busy}
          onClose={close}
          onConfirm={async () => {
            const ok = await act.run(`${dialog.discount.name} deleted`, () => api.del(`/discounts/${dialog.discount.id}`), q.reload)
            if (ok) close()
          }}
          body={
            <>
              Delete <b>{dialog.discount.name}</b> ({discountValueText(dialog.discount, currency)})? Future statement runs will not apply it; statements already issued keep the amount they recorded. To pause it and keep it listed, use Deactivate instead.
            </>
          }
        />
      ) : null}
    </div>
  )
}

function formFrom(d: Discount): DiscountForm {
  return {
    name: d.name,
    kind: d.kind === 'fixed' ? 'fixed' : 'percent',
    value: String(d.value ?? ''),
    sku: d.sku ?? '',
    starts_at: d.starts_at ? d.starts_at.slice(0, 10) : '',
    ends_at: d.ends_at ? d.ends_at.slice(0, 10) : '',
    active: d.active,
  }
}

function DiscountFormModal({
  customerId,
  discount,
  currency,
  skus,
  onClose,
  onDone,
}: {
  customerId: string
  discount?: Discount
  currency: string
  skus: string[]
  onClose: () => void
  onDone: () => void | Promise<void>
}) {
  const [form, setForm] = useState<DiscountForm>(discount ? formFrom(discount) : emptyDiscountForm())
  const [errors, setErrors] = useState<Errors<DiscountForm>>({})
  const act = useAction()
  const set = <K extends keyof DiscountForm>(k: K, v: DiscountForm[K]) => setForm((f) => ({ ...f, [k]: v }))

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    const errs = validateDiscount(form)
    setErrors(errs)
    if (hasErrors(errs)) return
    const body = discountBody(form, customerId)
    const ok = await act.run(discount ? `${form.name.trim()} saved` : `${form.name.trim()} created`, () => (discount ? api.put(`/discounts/${discount.id}`, body) : api.post(`/customers/${customerId}/discounts`, body)), onDone)
    if (ok) onClose()
  }

  return (
    <Modal
      title={discount ? `Edit discount — ${discount.name}` : 'New discount'}
      onClose={onClose}
      footer={
        <>
          <button type="button" onClick={onClose} disabled={act.busy}>
            Cancel
          </button>
          <button className="primary" form="discount-form" disabled={act.busy}>
            {discount ? 'Save' : 'Create'}
          </button>
        </>
      }
    >
      <form id="discount-form" onSubmit={(e) => void submit(e)}>
        {act.error ? <Notice kind="bad">{act.error}</Notice> : null}
        <Field label="Name" error={errors.name}>
          <input value={form.name} onChange={(e) => set('name', e.target.value)} placeholder="e.g. Launch offer" autoFocus />
        </Field>
        <div className="grid2">
          <Field label="Kind" error={errors.kind}>
            <Segmented<'percent' | 'fixed'>
              value={form.kind}
              options={[
                { value: 'percent', label: '% off' },
                { value: 'fixed', label: `Fixed ${currency}` },
              ]}
              onChange={(kind) => set('kind', kind)}
              ariaLabel="Discount kind"
            />
          </Field>
          <Field label={form.kind === 'percent' ? 'Percent (0–100)' : `Amount (${currency})`} error={errors.value} help={form.kind === 'percent' ? 'Applied to each matching line.' : 'Taken once per statement.'}>
            <input value={form.value} onChange={(e) => set('value', e.target.value)} inputMode="decimal" placeholder={form.kind === 'percent' ? '10' : '25.000'} />
          </Field>
        </div>
        <Field label="SKU scope" error={errors.sku} help="Leave empty to apply to every SKU. Suggestions are the SKUs this customer used in the last 30 days.">
          <input value={form.sku} onChange={(e) => set('sku', e.target.value)} list="discount-skus" className="mono" placeholder="every SKU" />
          <datalist id="discount-skus">
            {skus.map((s) => (
              <option key={s} value={s} />
            ))}
          </datalist>
        </Field>
        <div className="grid2">
          <Field label="Starts" error={errors.starts_at} help="Empty = already started.">
            <input type="date" value={form.starts_at} onChange={(e) => set('starts_at', e.target.value)} />
          </Field>
          <Field label="Ends" error={errors.ends_at} help="Empty = no end. The end day is excluded.">
            <input type="date" value={form.ends_at} onChange={(e) => set('ends_at', e.target.value)} min={form.starts_at || undefined} />
          </Field>
        </div>
        <label className="check">
          <input type="checkbox" checked={form.active} onChange={(e) => set('active', e.target.checked)} /> Active — applies on the next statement run
        </label>
      </form>
    </Modal>
  )
}
