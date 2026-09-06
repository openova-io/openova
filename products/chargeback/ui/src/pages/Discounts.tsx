import { useMemo, useState, type FormEvent } from 'react'
import { api, asList, errorText } from '../api/client'
import { exploreQuery, type Customer, type Discount, type ExploreResult } from '../api/types'
import { DataTable, type Column } from '../components/DataTable'
import { Badge, Confirm, Field, KPI, Modal, Notice, PageHeader, Segmented, Skeleton } from '../components/ui'
import { describeWindow, presetWindow } from '../lib/dates'
import { discountPreview, discountState, validateDiscount, type DiscountState } from '../lib/discounts'
import { day } from '../lib/format'
import { formatMoney, formatPct } from '../lib/money'
import { toNumber } from '../lib/num'
import { customerName, useCustomers } from '../lib/useCustomers'
import { useQuery } from '../lib/useQuery'

/**
 * Discounts (DESIGN.md §2.6) — every negotiated discount and campaign in one
 * place. A discount never changes the price book: statements show the list
 * subtotal and what came off it, so the saving stays visible.
 */

type Row = Discount & { state: DiscountState }
type StateFilter = 'all' | DiscountState
type Dialog = { kind: 'create' } | { kind: 'edit'; d: Discount } | { kind: 'delete'; d: Discount } | null

const STATE_TONE: Record<DiscountState, 'ok' | 'info' | 'warn' | 'bad' | undefined> = { active: 'ok', scheduled: 'info', ended: undefined, inactive: undefined }

function valueText(d: Discount): string {
  const v = toNumber(d.value)
  return d.kind === 'percent' ? formatPct(v, { digits: v % 1 ? 2 : 0 }) : formatMoney(v, '')
}

export function Discounts() {
  const list = useQuery<unknown>('/discounts')
  const { customers } = useCustomers()
  const [filter, setFilter] = useState<StateFilter>('all')
  const [scope, setScope] = useState('')
  const [dialog, setDialog] = useState<Dialog>(null)
  const [error, setError] = useState('')
  const [flash, setFlash] = useState('')
  const [busyId, setBusyId] = useState<string | null>(null)

  const all: Row[] = useMemo(() => asList<Discount>(list.data, 'discounts').map((d) => ({ ...d, state: discountState(d) })), [list.data])
  const rows = all.filter((r) => (filter === 'all' || r.state === filter) && (scope === '' || (scope === 'global' ? r.customer_id === null : r.customer_id === scope)))
  const count = (s: DiscountState) => all.filter((r) => r.state === s).length

  const toggle = async (r: Row) => {
    setBusyId(r.id)
    setError('')
    try {
      await api.patch(`/discounts/${r.id}`, { active: !r.active })
      await list.reload()
    } catch (e) {
      setError(errorText(e))
    } finally {
      setBusyId(null)
    }
  }
  const remove = async (d: Discount) => {
    setBusyId(d.id)
    setError('')
    try {
      await api.del(`/discounts/${d.id}`)
      setDialog(null)
      setFlash(`${d.name} deleted`)
      await list.reload()
    } catch (e) {
      setError(errorText(e))
      setDialog(null)
    } finally {
      setBusyId(null)
    }
  }

  const columns: Column<Row>[] = [
    {
      key: 'scope',
      header: 'Scope',
      value: (r) => (r.customer_id ? customerName(customers, r.customer_id, r.customer_name) : ''),
      render: (r) => (r.customer_id ? customerName(customers, r.customer_id, r.customer_name) : <Badge status="All customers" kind="info" />),
    },
    { key: 'name', header: 'Name', value: (r) => r.name },
    { key: 'kind', header: 'Kind', value: (r) => r.kind, render: (r) => (r.kind === 'percent' ? 'percent off' : r.kind === 'fixed' ? 'fixed amount' : r.kind) },
    { key: 'value', header: 'Value', value: (r) => toNumber(r.value), numeric: true, render: (r) => valueText(r) },
    { key: 'sku', header: 'Applies to', value: (r) => r.sku || '', render: (r) => (r.sku ? <span className="mono">{r.sku}</span> : <span className="muted">whole bill</span>) },
    {
      key: 'window',
      header: 'Campaign window',
      value: (r) => r.starts_at ?? '',
      render: (r) => (r.starts_at || r.ends_at ? `${r.starts_at ? day(r.starts_at) : '…'} → ${r.ends_at ? day(r.ends_at) : 'open'}` : <span className="muted">always</span>),
    },
    { key: 'state', header: 'Status', value: (r) => r.state, render: (r) => <Badge status={r.state} kind={STATE_TONE[r.state]} /> },
    {
      key: 'active',
      header: 'Active',
      value: (r) => (r.active ? 1 : 0),
      render: (r) => (
        <label className="check" title={r.active ? 'Switch off — stops applying from the next statement run' : 'Switch on'}>
          <input type="checkbox" checked={r.active} disabled={busyId === r.id} onChange={() => void toggle(r)} aria-label={`${r.name} active`} />
        </label>
      ),
    },
    {
      key: 'actions',
      className: 'actions',
      header: '',
      value: () => '',
      sortable: false,
      render: (r) => (
        <span className="btn-row">
          <button className="small" onClick={() => setDialog({ kind: 'edit', d: r })}>
            Edit
          </button>
          <button className="small danger" onClick={() => setDialog({ kind: 'delete', d: r })}>
            Delete
          </button>
        </span>
      ),
    },
  ]

  return (
    <div className="stack">
      <PageHeader
        title="Discounts"
        sub="Negotiated percentages and time-boxed campaigns, applied at statement time on top of list prices"
        actions={
          <button className="primary" onClick={() => setDialog({ kind: 'create' })}>
            New discount
          </button>
        }
      />
      {list.error ? <Notice kind="bad">{list.error}</Notice> : null}
      {error ? <Notice kind="bad">{error}</Notice> : null}
      {flash ? <Notice kind="ok">{flash}</Notice> : null}

      <div className="kpis">
        <KPI label="Discounts" value={all.length} note={`${all.filter((r) => r.customer_id === null).length} for all customers`} />
        <KPI label="Active now" value={count('active')} note="applied on the next statement run" tone={count('active') ? 'ok' : undefined} />
        <KPI label="Scheduled" value={count('scheduled')} note="campaign starts later" />
        <KPI label="Ended or off" value={count('ended') + count('inactive')} note={`${count('ended')} ended · ${count('inactive')} switched off`} />
      </div>

      <div className="toolbar">
        <Field label="Status">
          <Segmented<StateFilter>
            value={filter}
            onChange={setFilter}
            options={[
              { value: 'all', label: 'All' },
              { value: 'active', label: 'Active' },
              { value: 'scheduled', label: 'Scheduled' },
              { value: 'ended', label: 'Ended' },
              { value: 'inactive', label: 'Off' },
            ]}
            ariaLabel="Status"
          />
        </Field>
        <Field label="Scope">
          <select value={scope} onChange={(e) => setScope(e.target.value)} aria-label="Scope">
            <option value="">every scope</option>
            <option value="global">all-customer campaigns</option>
            {customers.map((c) => (
              <option key={c.id} value={c.id}>
                {c.name}
              </option>
            ))}
          </select>
        </Field>
      </div>

      <div className="card pad-0">
        {list.loading && !list.data ? (
          <div style={{ padding: 16 }}>
            <Skeleton lines={4} />
          </div>
        ) : (
          <DataTable
            columns={columns}
            rows={rows}
            rowKey={(r) => r.id}
            defaultSort={{ key: 'name', dir: 'asc' }}
            emptyTitle={all.length ? 'No discount matches the filter' : 'No discounts'}
            emptyBody={all.length ? 'Widen the status or scope filter.' : 'Every customer pays list price from its price book. Create a discount to take a percentage or a fixed amount off a SKU or the whole bill.'}
          />
        )}
      </div>

      {dialog?.kind === 'create' || dialog?.kind === 'edit' ? (
        <DiscountModal
          initial={dialog.kind === 'edit' ? dialog.d : null}
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
          title={`Delete ${dialog.d.name}?`}
          danger
          confirmLabel="Delete discount"
          busy={busyId === dialog.d.id}
          onClose={() => setDialog(null)}
          onConfirm={() => remove(dialog.d)}
          body={<p>Draft statements re-rated after this will no longer carry it. Issued statements keep the discount they were issued with.</p>}
        />
      ) : null}
    </div>
  )
}

interface Draft {
  customer_id: string
  name: string
  kind: 'percent' | 'fixed'
  value: string
  sku: string
  starts_at: string
  ends_at: string
  active: boolean
}

function draftOf(d: Discount | null): Draft {
  return {
    customer_id: d?.customer_id ?? '',
    name: d?.name ?? '',
    kind: d?.kind === 'fixed' ? 'fixed' : 'percent',
    value: d ? String(toNumber(d.value)) : '',
    sku: d?.sku ?? '',
    starts_at: d?.starts_at ? d.starts_at.slice(0, 10) : '',
    ends_at: d?.ends_at ? d.ends_at.slice(0, 10) : '',
    active: d?.active ?? true,
  }
}

/** Create / edit with a live month-to-date preview of the effect for the chosen scope. */
function DiscountModal({ initial, customers, onClose, onSaved }: { initial: Discount | null; customers: Customer[]; onClose: () => void; onSaved: (name: string) => void }) {
  const [draft, setDraft] = useState<Draft>(() => draftOf(initial))
  const [errors, setErrors] = useState<Partial<Record<keyof Draft, string>>>({})
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const set = (patch: Partial<Draft>) => setDraft((d) => ({ ...d, ...patch }))

  // Preview: this month's list-price cost by SKU for the scope, then the
  // discount arithmetic client-side (lib/discounts, unit-tested).
  const win = useMemo(() => presetWindow('mtd'), [])
  const scopePath = draft.customer_id ? `/customers/${draft.customer_id}` : ''
  const preview = useQuery<ExploreResult>(`${scopePath}/cost/explore?${exploreQuery({ from: win.from, to: win.to, granularity: 'month', group_by: 'sku', limit: 0 })}`)
  const groups = useMemo(() => [...(preview.data?.groups ?? []), ...(preview.data?.other ? [preview.data.other] : [])].map((g) => ({ key: g.key, total: g.total })), [preview.data])
  const effect = preview.data ? discountPreview({ kind: draft.kind, value: Number(draft.value), sku: draft.sku }, groups, preview.data.total.current) : null
  const cur = preview.data?.currency ?? ''

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    const errs = validateDiscount(draft)
    setErrors(errs)
    if (Object.keys(errs).length) return
    setBusy(true)
    setError('')
    const body = {
      customer_id: draft.customer_id || null,
      name: draft.name.trim(),
      kind: draft.kind,
      value: String(Number(draft.value)),
      sku: draft.sku.trim(),
      starts_at: draft.starts_at || null,
      ends_at: draft.ends_at || null,
      active: draft.active,
    }
    try {
      if (initial) await api.put(`/discounts/${initial.id}`, body)
      else await api.post('/discounts', body)
      onSaved(body.name)
    } catch (err) {
      setError(errorText(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      title={initial ? `Edit ${initial.name}` : 'New discount'}
      wide
      onClose={onClose}
      footer={
        <>
          <button type="button" onClick={onClose} disabled={busy}>
            Cancel
          </button>
          <button className="primary" form="discount-form" disabled={busy}>
            {initial ? 'Save' : 'Create'}
          </button>
        </>
      }
    >
      <form id="discount-form" onSubmit={submit} className="grid side" style={{ gap: 18 }}>
        <div>
          {error ? <Notice kind="bad">{error}</Notice> : null}
          <div className="grid2">
            <Field label="Scope" help="A campaign for all customers applies to every statement">
              <select value={draft.customer_id} onChange={(e) => set({ customer_id: e.target.value })}>
                <option value="">All customers</option>
                {customers.map((c) => (
                  <option key={c.id} value={c.id}>
                    {c.name}
                  </option>
                ))}
              </select>
            </Field>
            <Field label="Name" error={errors.name}>
              <input value={draft.name} onChange={(e) => set({ name: e.target.value })} autoFocus placeholder="e.g. Launch campaign, Volume tier 2" />
            </Field>
            <Field label="Kind" error={errors.kind}>
              <Segmented<'percent' | 'fixed'>
                value={draft.kind}
                onChange={(kind) => set({ kind })}
                options={[
                  { value: 'percent', label: 'Percent off' },
                  { value: 'fixed', label: 'Fixed amount' },
                ]}
                ariaLabel="Kind"
              />
            </Field>
            <Field label={draft.kind === 'percent' ? 'Percent (0–100)' : `Amount${cur ? ` (${cur})` : ''}`} error={errors.value}>
              <input type="number" step="any" min={0} max={draft.kind === 'percent' ? 100 : undefined} value={draft.value} onChange={(e) => set({ value: e.target.value })} />
            </Field>
            <Field label="SKU" help="Leave empty for the whole bill; one SKU narrows it to that meter">
              <input className="mono" list="discount-sku-options" value={draft.sku} onChange={(e) => set({ sku: e.target.value })} placeholder="whole bill" />
              <datalist id="discount-sku-options">
                {groups.map((g) => (
                  <option key={g.key} value={g.key} />
                ))}
              </datalist>
            </Field>
            <div />
            <Field label="Starts" help="Empty = immediately">
              <input type="date" value={draft.starts_at} onChange={(e) => set({ starts_at: e.target.value })} />
            </Field>
            <Field label="Ends" error={errors.ends_at} help="Empty = open-ended">
              <input type="date" value={draft.ends_at} min={draft.starts_at || undefined} onChange={(e) => set({ ends_at: e.target.value })} />
            </Field>
          </div>
          <label className="check">
            <input type="checkbox" checked={draft.active} onChange={(e) => set({ active: e.target.checked })} /> Active
          </label>
        </div>
        <div className="card flat" style={{ background: 'var(--panel-2)' }}>
          <h3>Effect this month — estimate</h3>
          {preview.error ? (
            <p className="muted small">Preview unavailable: {preview.error}</p>
          ) : !preview.data || !effect ? (
            <Skeleton lines={2} />
          ) : preview.data.total.current <= 0 ? (
            <p className="muted small">{draft.customer_id ? customerName(customers, draft.customer_id) : 'The customers'} had no priced usage in {describeWindow(win)}, so there is nothing to discount yet.</p>
          ) : (
            <>
              <div className="kpi" style={{ boxShadow: 'none' }}>
                <div className="k">
                  <span>would take off</span>
                </div>
                <div className="v">{formatMoney(effect.saving, cur)}</div>
                <div className="d">
                  of {formatMoney(effect.base, cur)} {draft.sku.trim() ? `on ${draft.sku.trim()}` : 'on the whole bill'} · {describeWindow(win)}
                </div>
              </div>
              {draft.sku.trim() && !effect.matched ? (
                <p className="warn small">
                  No usage of <span className="mono">{draft.sku.trim()}</span> this month for this scope.
                </p>
              ) : null}
              <p className="muted tiny" style={{ marginBottom: 0 }}>
                List prices, month to date ({formatMoney(preview.data.total.current, cur)} total), before other discounts and tax. The statement run applies the exact figure.
              </p>
            </>
          )}
        </div>
      </form>
    </Modal>
  )
}
