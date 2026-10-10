import { Fragment, useEffect, useMemo, useState, type FormEvent } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { api, asList, errorText } from '../api/client'
import type { Contract, ContractItem, ContractPeriods, CreditNote, CustomerSKU, Statement } from '../api/types'
import { Badge, Confirm, EmptyState, Field, FormRow, KPI, Modal, Notice, PageHeader, ShareBar, Skeleton } from '../components/ui'
import { useSession } from '../auth/session'
import { can } from '../lib/access'
import { CONTRACT_STATUSES, committedUnitPrice, contractFloor, contractItemText, daysToEnd, isCommitmentKind, quantityReading, renewalDue, termEnd, termText } from '../lib/contracts'
import { day, today, when } from '../lib/format'
import { formatMoney, formatNumber } from '../lib/money'
import { toNumber } from '../lib/num'
import { useQuery } from '../lib/useQuery'

/**
 * One contract (DESIGN.md §15.9): the term and the monthly floor, the
 * committed-use, allowance and spend-commitment lines the rating engine
 * applies, and the SLA credit issued against a named statement.
 *
 * The lines are a table with EDIT and DELETE on every row and one dialog per
 * line (#6946): "Add committed use", "Add allowance" and "Add spend
 * commitment" each open a form for ONE line, and a SKU is chosen from what
 * the customer's price books price, never typed. Under every time-integrated
 * quantity sits its human reading — "≈ 8 servers all month" beneath
 * "5,952 instance-hour" — because the raw figure is what the engine uses and
 * nobody reads it.
 *
 * Beneath the lines sit the RATED PERIODS (DESIGN.md §15.10): what the
 * contract did — one row per statement rated under it with the floor in
 * force and the true-up it produced, and under each row how much of every
 * allowance was used and how much of every committed head the usage filled.
 *
 * Everything writable here is `customers.manage`, except the SLA credit,
 * which is `billing.issue` — it issues a real credit note. A principal
 * without them reads the page.
 */
export function ContractDetail() {
  const { id = '' } = useParams()
  const nav = useNavigate()
  const { me } = useSession()
  const q = useQuery<Contract>(`/contracts/${id}`)
  const periods = useQuery<ContractPeriods>(`/contracts/${id}/periods`)
  const c = q.data
  const canManage = can(me, 'customers.manage', c?.customer_id ?? null)
  const canIssue = can(me, 'billing.issue', c?.customer_id ?? null)
  const [dialog, setDialog] = useState<'edit' | 'sla' | 'delete' | null>(null)
  const [line, setLine] = useState<LineDialog | null>(null)
  const [flash, setFlash] = useState('')

  if (q.error && !c) return <Notice kind="bad">{q.error}</Notice>
  if (!c) return <Skeleton lines={6} />

  const items = asList<ContractItem>(c.items ?? [], 'items')
  const commitments = items.filter((it) => isCommitmentKind(it.kind))
  const allowances = items.filter((it) => it.kind === 'allowance')
  const floor = contractFloor(c)
  const on = today()
  const days = daysToEnd(c, on)
  const due = renewalDue(c, on)
  const saved = async (what: string) => {
    setLine(null)
    setFlash(what)
    await q.reload()
  }

  return (
    <div className="stack">
      <PageHeader
        crumbs={[{ to: '/contracts', label: 'Contracts' }, { label: c.name }]}
        title={c.name}
        sub={
          <>
            <Badge status={c.status} kind={c.status === 'active' ? 'ok' : c.status === 'draft' ? 'warn' : 'bad'} /> <Link to={`/customers/${c.customer_id}?tab=contract`}>{c.customer_name}</Link> ·{' '}
            {termText(c)} · {c.currency}
            {c.po_reference ? <> · PO {c.po_reference}</> : null}
            {c.signed_at ? <> · signed {day(c.signed_at)}</> : null}
            {(c.renewal_count ?? 0) > 0 ? (
              <>
                {' '}
                · renewed {c.renewal_count} time{c.renewal_count === 1 ? '' : 's'}
                {c.renewed_at ? ` (last ${when(c.renewed_at)})` : ''}
              </>
            ) : null}
          </>
        }
        actions={
          <>
            {canManage ? <button onClick={() => setDialog('edit')}>Edit</button> : null}
            {canIssue ? <button onClick={() => setDialog('sla')}>Issue SLA credit</button> : null}
            {canManage ? (
              <button className="danger" onClick={() => setDialog('delete')}>
                Delete
              </button>
            ) : null}
          </>
        }
      />
      {flash ? <Notice kind="ok">{flash}</Notice> : null}

      <div className="kpis">
        <KPI
          label="Monthly floor"
          value={floor.amount > 0 ? formatMoney(floor.amount, c.currency) : '—'}
          note={
            floor.source === 'none'
              ? 'no minimum and no spend commitment: every period is invoiced at what it rated'
              : floor.source === 'minimum'
                ? 'the contract’s minimum; a period whose net falls below it carries a true-up line'
                : floor.source === 'spend'
                  ? 'the spend commitment’s amount; a period whose net falls below it carries a true-up line'
                  : `the larger of the minimum (${formatMoney(floor.minimum, c.currency)}) and the spend commitment (${formatMoney(floor.spend, c.currency)}); the two do not add up`
          }
        />
        <KPI
          label="Committed lines"
          value={commitments.length}
          note={commitments.length ? (commitments.some((it) => it.kind === 'spend') ? 'a quantity at a negotiated rate, or an amount per period for a percentage off' : 'a quantity per period at a negotiated rate') : 'nothing committed'}
        />
        <KPI label="Allowance lines" value={allowances.length} note={allowances.length ? 'on top of what the plan already includes' : 'the plan’s allowances only'} />
        <KPI
          label={c.auto_renew ? 'Renews' : 'Ends'}
          value={c.auto_renew ? c.renewal_date || c.ends_on : c.ends_on}
          note={
            c.status !== 'active'
              ? `the contract is ${c.status}`
              : days === null
                ? ''
                : days >= 0
                  ? `${days} day${days === 1 ? '' : 's'} away · notice from ${c.notice_from}`
                  : `${-days} day${days === -1 ? '' : 's'} past the end date`
          }
          tone={due ? 'warn' : undefined}
        />
      </div>

      {due ? (
        <Notice kind="warn">
          Inside the renewal notice window ({c.renewal_notice_days} days). {c.auto_renew ? `It renews for another ${c.term_months} months on ${c.renewal_date} unless it is changed.` : 'It EXPIRES at the end date unless it is renewed.'}
        </Notice>
      ) : null}

      <div className="card">
        <div className="card-head">
          <h2>Committed use and allowances</h2>
          <span className="hint">applied before the discounts, by the one rating engine</span>
          {canManage ? (
            <span className="btn-row">
              <button type="button" className="small primary" onClick={() => setLine({ kind: 'commitment', item: null })}>
                Add committed use
              </button>
              <button type="button" className="small primary" onClick={() => setLine({ kind: 'allowance', item: null })}>
                Add allowance
              </button>
              <button type="button" className="small primary" disabled={items.some((it) => it.kind === 'spend')} title={items.some((it) => it.kind === 'spend') ? 'the contract already carries a spend commitment; edit that row' : undefined} onClick={() => setLine({ kind: 'spend', item: null })}>
                Add spend commitment
              </button>
            </span>
          ) : null}
        </div>
        {items.length === 0 ? (
          <EmptyState title="No lines">
            A committed-use line prices a quantity of a SKU per period at a negotiated rate, with anything above it at list. An allowance line includes a quantity on top of whatever the plan already
            includes. A spend commitment is an amount per period, whatever is used, for a percentage off everything.
          </EmptyState>
        ) : (
          <div className="table-wrap">
            <table aria-label="Contract lines">
              <thead>
                <tr>
                  <th>Kind</th>
                  <th>SKU</th>
                  <th className="num">Quantity</th>
                  <th>What it does</th>
                  {canManage ? <th className="actions" /> : null}
                </tr>
              </thead>
              <tbody>
                {items.map((it, i) => {
                  const reading = quantityReading(it.quantity, it.unit)
                  const label = it.kind === 'spend' ? 'spend commitment' : it.kind === 'commitment' ? `${it.sku} commitment` : `${it.sku} allowance`
                  return (
                    <tr key={it.id ?? `${it.kind}-${it.sku}-${i}`} data-line-kind={it.kind}>
                      <td>
                        <Badge status={it.kind === 'commitment' ? 'committed' : it.kind === 'spend' ? 'spend' : 'allowance'} kind={it.kind === 'commitment' ? 'info' : it.kind === 'spend' ? 'ok' : undefined} />
                      </td>
                      <td className="mono nowrap">{it.kind === 'spend' ? <span className="muted">whole bill</span> : it.sku}</td>
                      <td className="num nowrap">
                        {it.kind === 'spend' ? (
                          <>
                            {formatMoney(toNumber(it.amount), c.currency)}
                            <span className="sub">per period</span>
                          </>
                        ) : (
                          <>
                            {toNumber(it.quantity).toLocaleString(undefined, { maximumFractionDigits: 6 })}
                            {it.unit ? <span className="sub">{it.unit}</span> : null}
                            {reading ? (
                              <div className="muted small" data-reading>
                                {reading}
                              </div>
                            ) : null}
                          </>
                        )}
                      </td>
                      <td>{contractItemText(it, c.currency)}</td>
                      {canManage ? (
                        <td className="actions">
                          <span className="btn-row">
                            <button type="button" className="small" aria-label={`Edit ${label}`} onClick={() => setLine({ kind: it.kind === 'spend' ? 'spend' : it.kind === 'allowance' ? 'allowance' : 'commitment', item: it })}>
                              Edit
                            </button>
                            <button type="button" className="small danger" aria-label={`Delete ${label}`} onClick={() => setLine({ kind: 'delete', item: it })}>
                              Delete
                            </button>
                          </span>
                        </td>
                      ) : null}
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        )}
      </div>

      <RatedPeriods contract={c} doc={periods.data} error={periods.error} />

      {c.notes ? (
        <div className="card">
          <div className="card-head">
            <h2>Notes</h2>
          </div>
          <p className="muted small" style={{ margin: 0 }}>
            {c.notes}
          </p>
        </div>
      ) : null}

      {dialog === 'edit' ? (
        <EditContractModal
          contract={c}
          onClose={() => setDialog(null)}
          onSaved={async () => {
            setDialog(null)
            setFlash('contract saved')
            await q.reload()
          }}
        />
      ) : null}
      {line && line.kind !== 'delete' ? <LineEditor contract={c} kind={line.kind} item={line.item} onClose={() => setLine(null)} onSaved={() => saved(line.item ? 'line saved' : 'line added')} /> : null}
      {line && line.kind === 'delete' && line.item ? <DeleteLineConfirm contract={c} item={line.item} onClose={() => setLine(null)} onDeleted={() => saved('line removed')} /> : null}
      {dialog === 'sla' ? (
        <SLACreditModal
          contract={c}
          onClose={() => setDialog(null)}
          onIssued={(n) => {
            setDialog(null)
            setFlash(`credit note ${n.number} issued for ${formatMoney(toNumber(n.total), n.currency)}`)
          }}
        />
      ) : null}
      {dialog === 'delete' ? (
        <DeleteContractConfirm
          contract={c}
          onClose={() => setDialog(null)}
          onDeleted={() => nav('/contracts')}
        />
      ) : null}
    </div>
  )
}

/**
 * What the contract did (DESIGN.md §15.10): one row per statement rated
 * under it, newest first — the period, the statement it produced, its
 * status, the subtotal, the floor in force and the true-up — and, opened
 * from the row, the consumption of every line as "used / included" with a
 * bar, plus the discounts that applied. The newest period opens by itself:
 * the founder's "the contract page shows no effect" is answered on arrival,
 * not after a click. Nothing here is editable — a period is what a run did.
 */
export function RatedPeriods({ contract, doc, error }: { contract: Contract; doc: ContractPeriods | null; error: string }) {
  const rows = doc?.periods ?? []
  // undefined = nothing chosen yet, so the newest period is the open one.
  const [chosen, setChosen] = useState<string | null | undefined>(undefined)
  const openID = chosen === undefined ? (rows[0]?.statement_id ?? null) : chosen
  const cur = contract.currency
  const money = (v: number | string | null | undefined) => formatMoney(toNumber(v), cur)
  return (
    <div className="card">
      <div className="card-head">
        <h2>Rated periods</h2>
        <span className="hint">what the contract did — one row per statement rated under it, newest first</span>
      </div>
      {error ? (
        <Notice kind="bad">{error}</Notice>
      ) : !doc ? (
        <Skeleton lines={3} />
      ) : rows.length === 0 ? (
        <EmptyState title="No statement has been rated under this contract yet">
          A statement run for a period inside the term rates it under these lines; the period then appears here with how much of each allowance and commitment it used, the floor in force and any true-up.
        </EmptyState>
      ) : (
        <div className="table-wrap">
          <table aria-label="Rated periods">
            <thead>
              <tr>
                <th>Period</th>
                <th>Statement</th>
                <th>Status</th>
                <th className="num">Subtotal</th>
                <th className="num">Floor</th>
                <th className="num">True-up</th>
                <th className="actions" />
              </tr>
            </thead>
            <tbody>
              {rows.map((p) => {
                const open = openID === p.statement_id
                const trueUp = toNumber(p.true_up)
                const hasFloor = p.floor !== null && p.floor !== undefined && toNumber(p.floor) > 0
                const detail = p.allowances.length + p.commitments.length + p.discounts.length > 0
                return (
                  <Fragment key={p.statement_id}>
                    <tr data-period={p.period} className={open ? 'open' : undefined}>
                      <td className="mono nowrap">
                        {p.period}
                        <span className="sub">
                          {p.period_start} to {p.period_end}
                        </span>
                      </td>
                      <td>
                        <Link to={`/statements/${p.statement_id}`}>{p.invoice_number || 'draft statement'}</Link>
                      </td>
                      <td>
                        <Badge status={p.status} kind={p.status === 'overdue' || p.status === 'cancelled' ? 'bad' : p.status === 'paid' ? 'ok' : p.status === 'draft' ? 'warn' : undefined} />
                      </td>
                      <td className="num nowrap">
                        {money(p.subtotal)}
                        {toNumber(p.discount_total) > 0 ? <span className="sub">after {money(p.discount_total)} of discounts</span> : null}
                      </td>
                      <td className="num nowrap">{hasFloor ? money(p.floor) : <span className="muted">—</span>}</td>
                      <td className="num nowrap" data-true-up={trueUp > 0 ? 'yes' : 'no'}>
                        {trueUp > 0 ? (
                          <>
                            {money(p.true_up)}
                            <span className="sub">net {money(p.net)} brought to the floor</span>
                          </>
                        ) : hasFloor ? (
                          <span className="muted">
                            none
                            <span className="sub">net {money(p.net)} met the floor</span>
                          </span>
                        ) : (
                          <span className="muted">—</span>
                        )}
                      </td>
                      <td className="actions">
                        {detail ? (
                          <button type="button" className="small" aria-expanded={open} aria-label={`${open ? 'Hide' : 'Show'} consumption for ${p.period}`} onClick={() => setChosen(open ? null : p.statement_id)}>
                            {open ? 'Hide' : 'Consumption'}
                          </button>
                        ) : null}
                      </td>
                    </tr>
                    {open && detail ? (
                      <tr className="expand" data-period-detail={p.period}>
                        <td colSpan={7}>
                          <div className="consumption">
                            {p.allowances.map((a) => (
                              <Consumption key={`a-${a.sku}`} label={`${a.sku} allowance`} used={a.used} included={a.included} unit={a.unit} excess={a.excess} excessWord="above it, priced by the plan" />
                            ))}
                            {p.commitments.map((m) => (
                              <Consumption
                                key={`c-${m.sku}`}
                                label={`${m.sku} commitment`}
                                used={m.delivered}
                                included={m.committed}
                                unit={m.unit}
                                excess={m.excess}
                                excessWord="above it at list"
                                shortfall={m.shortfall}
                                amount={money(m.amount)}
                              />
                            ))}
                            {p.discounts.map((d, i) => (
                              <div className="consumption-row" key={d.discount_id ?? `d-${i}`} data-discount={d.from_contract ? 'contract' : 'other'}>
                                <span className="nowrap">{d.from_contract ? 'spend commitment' : 'discount'}</span>
                                <span />
                                <span className="muted small">{d.name}</span>
                                <span className="num nowrap">− {money(d.amount)}</span>
                              </div>
                            ))}
                          </div>
                        </td>
                      </tr>
                    ) : null}
                  </Fragment>
                )
              })}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}

/**
 * One line's consumption in a period: "used / included" with a thin bar,
 * the share in words, what went above it, and — on a commitment — what the
 * usage did not reach and what the SKU rated to.
 */
function Consumption({ label, used, included, unit, excess, excessWord, shortfall, amount }: { label: string; used: number | string; included: number | string; unit?: string; excess: number | string; excessWord: string; shortfall?: number | string; amount?: string }) {
  const u = toNumber(used)
  const inc = toNumber(included)
  const over = toNumber(excess)
  const short = toNumber(shortfall)
  const share = inc > 0 ? u / inc : 0
  const pct = Math.round(share * 1000) / 10
  const u2 = unit ? ` ${unit}` : ''
  return (
    <div className="consumption-row" data-consumption={label}>
      <span className="mono nowrap">{label}</span>
      <ShareBar share={share} width={120} />
      <span className="nowrap">
        <span data-used>
          {formatNumber(u, 3)} / {formatNumber(inc, 3)}
          {u2}
        </span>
        <span className="sub">
          {pct.toLocaleString('en-US')} % used
          {over > 0 ? `, ${formatNumber(over, 3)}${u2} ${excessWord}` : ''}
          {short > 0 ? `, ${formatNumber(short, 3)}${u2} committed but not used` : ''}
        </span>
      </span>
      {amount ? <span className="num nowrap">{amount}</span> : <span />}
    </div>
  )
}

/** Edit the header. The end date follows the term unless it is typed. */
function EditContractModal({ contract, onClose, onSaved }: { contract: Contract; onClose: () => void; onSaved: () => void | Promise<void> }) {
  const [form, setForm] = useState({
    name: contract.name,
    starts_on: contract.starts_on,
    term_months: String(contract.term_months),
    ends_on: contract.ends_on,
    minimum_commitment: toNumber(contract.minimum_commitment) > 0 ? String(toNumber(contract.minimum_commitment)) : '',
    status: contract.status,
    auto_renew: contract.auto_renew,
    renewal_notice_days: String(contract.renewal_notice_days),
    po_reference: contract.po_reference ?? '',
    notes: contract.notes ?? '',
  })
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const set = (patch: Partial<typeof form>) => setForm((f) => ({ ...f, ...patch }))
  const derived = termEnd(form.starts_on, Number(form.term_months))
  // The term moved: follow it, so the end date never contradicts the term.
  useEffect(() => {
    if (derived) setForm((f) => (f.ends_on === derived ? f : { ...f, ends_on: derived }))
  }, [derived])

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError('')
    try {
      await api.patch(`/contracts/${contract.id}`, {
        name: form.name.trim(),
        starts_on: form.starts_on,
        term_months: Number(form.term_months) || contract.term_months,
        ends_on: form.ends_on,
        // An explicit null CLEARS the minimum; omitting the key would keep it.
        minimum_commitment: form.minimum_commitment.trim() === '' ? null : form.minimum_commitment.trim(),
        status: form.status,
        auto_renew: form.auto_renew,
        renewal_notice_days: Number(form.renewal_notice_days) || 0,
        po_reference: form.po_reference.trim(),
        notes: form.notes.trim(),
      })
      await onSaved()
    } catch (err) {
      setError(errorText(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      title={`Edit ${contract.name}`}
      wide
      onClose={onClose}
      footer={
        <>
          <button type="button" onClick={onClose} disabled={busy}>
            Cancel
          </button>
          <button className="primary" form="edit-contract" disabled={busy}>
            {busy ? 'Saving…' : 'Save'}
          </button>
        </>
      }
    >
      <form id="edit-contract" onSubmit={(e) => void submit(e)} className="stack tight">
        {error ? <Notice kind="bad">{error}</Notice> : null}
        <div className="grid2">
          <Field label="Name">
            <input value={form.name} onChange={(e) => set({ name: e.target.value })} />
          </Field>
          <Field label="Status" help="Only an ACTIVE contract covering the first day of a period rates that period.">
            <select value={form.status} onChange={(e) => set({ status: e.target.value })} aria-label="Status">
              {CONTRACT_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {s}
                </option>
              ))}
            </select>
          </Field>
          <Field label="Starts on">
            <input type="date" value={form.starts_on} onChange={(e) => set({ starts_on: e.target.value })} />
          </Field>
          <Field label="Term (months)" help={derived ? `Ends ${derived}` : undefined}>
            <input type="number" min={1} step={1} value={form.term_months} onChange={(e) => set({ term_months: e.target.value })} />
          </Field>
          <Field label="Ends on" help="Follows the term; type it to override.">
            <input type="date" value={form.ends_on} onChange={(e) => set({ ends_on: e.target.value })} />
          </Field>
          <Field label={`Monthly minimum (${contract.currency})`} help="Empty clears it: every period is then invoiced at what it rated.">
            <input type="number" step="any" min={0} value={form.minimum_commitment} onChange={(e) => set({ minimum_commitment: e.target.value })} placeholder="none" />
          </Field>
          <Field label="Renewal notice (days)">
            <input type="number" min={0} step={1} value={form.renewal_notice_days} onChange={(e) => set({ renewal_notice_days: e.target.value })} />
          </Field>
          <Field label="Purchase order">
            <input value={form.po_reference} onChange={(e) => set({ po_reference: e.target.value })} />
          </Field>
          <Field label="Auto-renew">
            <label className="check">
              <input type="checkbox" checked={form.auto_renew} onChange={(e) => set({ auto_renew: e.target.checked })} aria-label="Auto-renew" /> Renew for another term at the end date
            </label>
          </Field>
          <div style={{ gridColumn: '1 / -1' }}>
            <Field label="Notes">
              <textarea rows={3} value={form.notes} onChange={(e) => set({ notes: e.target.value })} />
            </Field>
          </div>
        </div>
      </form>
    </Modal>
  )
}

type LineKind = 'commitment' | 'allowance' | 'spend'

/** What the line dialogs open on: a new line of a kind, an existing line to edit, or one to delete. */
type LineDialog = { kind: LineKind; item: ContractItem | null } | { kind: 'delete'; item: ContractItem }

type LineForm = { sku: string; unit: string; quantity: string; rate: 'pct' | 'price'; committed_price: string; discount_pct: string; amount: string; rollover: boolean }

function formOf(kind: LineKind, it: ContractItem | null): LineForm {
  const hasPrice = it?.committed_price !== null && it?.committed_price !== undefined && it?.committed_price !== ''
  return {
    sku: it?.sku ?? '',
    unit: it?.unit ?? '',
    quantity: it && kind !== 'spend' ? String(toNumber(it.quantity)) : '',
    rate: hasPrice ? 'price' : 'pct',
    committed_price: hasPrice ? String(toNumber(it?.committed_price)) : '',
    discount_pct: it && toNumber(it.discount_pct) > 0 ? String(toNumber(it.discount_pct)) : '',
    amount: it && toNumber(it.amount) > 0 ? String(toNumber(it.amount)) : '',
    rollover: Boolean(it?.rollover),
  }
}

/**
 * ONE line, added or edited in a dialog (#6946). The SKU is a select over
 * what the customer's price books price (`GET /customers/{id}/skus`) — never
 * a text box: a typed SKU is a typo the engine finds no rate for. Choosing
 * one fills the unit and shows the list price; the committed price or the
 * percentage then shows the unit price it results in, live, and the human
 * reading of the quantity updates under the field as it is typed. A spend
 * commitment has no SKU and no quantity: an amount per period and the
 * percentage it buys.
 */
export function LineEditor({ contract, kind, item, onClose, onSaved }: { contract: Contract; kind: LineKind; item: ContractItem | null; onClose: () => void; onSaved: () => void | Promise<void> }) {
  const skus = useQuery<{ skus: CustomerSKU[] }>(kind === 'spend' ? null : `/customers/${contract.customer_id}/skus`)
  const options = useMemo(() => {
    const list = [...(skus.data?.skus ?? [])].sort((a, b) => a.sku.localeCompare(b.sku))
    // A line whose SKU the books no longer price stays selectable, so an
    // edit of its quantity does not first demand a different SKU.
    if (item?.sku && !list.some((o) => o.sku === item.sku)) list.unshift({ sku: item.sku, unit: item.unit ?? '', unit_price: '', price_book_id: '', price_book_name: 'not priced by the customer’s books', currency: contract.currency })
    return list
  }, [skus.data, item, contract.currency])
  const [form, setForm] = useState<LineForm>(() => formOf(kind, item))
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const set = (patch: Partial<LineForm>) => setForm((f) => ({ ...f, ...patch }))
  const chosen = options.find((o) => o.sku === form.sku)
  const listPrice = chosen && chosen.unit_price !== '' ? toNumber(chosen.unit_price) : null
  const resulting = kind === 'commitment' ? committedUnitPrice(listPrice, { committed_price: form.rate === 'price' ? form.committed_price : null, discount_pct: form.rate === 'pct' ? form.discount_pct : null }) : null
  const reading = kind === 'spend' ? '' : quantityReading(form.quantity, form.unit)
  const title = item ? (kind === 'spend' ? 'Edit the spend commitment' : `Edit ${item.sku} ${kind === 'commitment' ? 'commitment' : 'allowance'}`) : kind === 'commitment' ? 'Add committed use' : kind === 'allowance' ? 'Add allowance' : 'Add spend commitment'

  const problem = useMemo(() => {
    if (kind === 'spend') {
      if (!(Number(form.amount) > 0)) return 'A spend commitment needs an amount per period above zero.'
      if (form.discount_pct.trim() === '' || !(Number(form.discount_pct) >= 0) || Number(form.discount_pct) >= 100) return 'The percentage off is from 0 up to, not including, 100.'
      return ''
    }
    if (!form.sku) return 'Choose the SKU the line is for.'
    if (form.quantity.trim() === '' || !(Number(form.quantity) >= 0)) return 'A line needs a quantity per period.'
    if (kind === 'commitment' && form.rate === 'price' && form.committed_price.trim() === '') return 'A committed-use line needs a committed price — a commitment at list is not a commitment.'
    if (kind === 'commitment' && form.rate === 'pct' && form.discount_pct.trim() === '') return 'A committed-use line needs a percentage off list — a commitment at list is not a commitment.'
    return ''
  }, [form, kind])

  const chooseSku = (sku: string) => {
    const o = options.find((x) => x.sku === sku)
    set({ sku, unit: o?.unit ?? form.unit })
  }

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (problem) return
    setBusy(true)
    setError('')
    const body: Record<string, unknown> =
      kind === 'spend'
        ? { kind, sku: '', quantity: '0', amount: form.amount.trim(), discount_pct: form.discount_pct.trim(), committed_price: null, rollover: false }
        : {
            kind,
            sku: form.sku,
            unit: form.unit.trim(),
            quantity: form.quantity.trim(),
            committed_price: kind === 'commitment' && form.rate === 'price' ? form.committed_price.trim() : null,
            discount_pct: kind === 'commitment' && form.rate === 'pct' ? form.discount_pct.trim() : null,
            amount: null,
            rollover: kind === 'allowance' ? form.rollover : false,
          }
    try {
      if (item?.id) await api.patch(`/contracts/${contract.id}/items/${item.id}`, body)
      else await api.post(`/contracts/${contract.id}/items`, body)
      await onSaved()
    } catch (err) {
      setError(errorText(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      title={title}
      onClose={onClose}
      footer={
        <>
          <button type="button" onClick={onClose} disabled={busy}>
            Cancel
          </button>
          <button className="primary" form="contract-line" disabled={busy || Boolean(problem)}>
            {busy ? 'Saving…' : item ? 'Save line' : 'Add line'}
          </button>
        </>
      }
    >
      <form id="contract-line" onSubmit={(e) => void submit(e)} className="stack tight" aria-label={title}>
        {error ? <Notice kind="bad">{error}</Notice> : null}
        {kind === 'spend' ? (
          <>
            <p className="muted small" style={{ margin: 0 }}>
              An amount the customer pays every period whether it uses anything or not, in return for a percentage off everything on the bill. A period rated below the amount is trued up to it; the
              percentage comes off before the true-up, through the same discount engine as any other discount.
            </p>
            <FormRow>
              <Field label={`Amount per period (${contract.currency})`} help="The floor the period is trued up to.">
                <input autoFocus type="number" step="any" min={0} value={form.amount} onChange={(e) => set({ amount: e.target.value })} aria-label="Amount per period" placeholder="1000" />
              </Field>
              <Field label="Discount (%)" help="Off every line of the period, on top of any other discount that applies.">
                <input type="number" step="any" min={0} max={99.9999} value={form.discount_pct} onChange={(e) => set({ discount_pct: e.target.value })} aria-label="Spend discount percent" placeholder="50" />
              </Field>
            </FormRow>
          </>
        ) : (
          <>
            <FormRow>
              <Field label="SKU" help={skus.error ? skus.error : chosen?.price_book_name ? `priced by ${chosen.price_book_name}` : skus.data && options.length === 0 ? 'none of this customer’s sources has a price book yet' : 'what the customer’s price books price'}>
                <select value={form.sku} onChange={(e) => chooseSku(e.target.value)} aria-label="SKU" disabled={!skus.data && !item}>
                  <option value="">{skus.data || item ? 'Choose a SKU…' : 'Loading…'}</option>
                  {options.map((o) => (
                    <option key={o.sku} value={o.sku}>
                      {o.description ? `${o.sku} — ${o.description}` : o.sku}
                    </option>
                  ))}
                </select>
              </Field>
              <Field label="Unit" help="From the price book.">
                <input value={form.unit} readOnly aria-label="Unit" placeholder="—" />
              </Field>
              <Field label={`List price (${contract.currency})`} help={chosen ? `per ${chosen.unit || 'unit'}` : undefined}>
                <input value={listPrice === null ? '' : String(listPrice)} readOnly aria-label="List price" placeholder="—" />
              </Field>
            </FormRow>
            <FormRow>
              <Field label="Quantity per period" help={reading || (form.unit ? `in ${form.unit}` : undefined)}>
                <input autoFocus type="number" step="any" min={0} value={form.quantity} onChange={(e) => set({ quantity: e.target.value })} aria-label="Quantity per period" />
              </Field>
              {kind === 'commitment' ? (
                <>
                  <Field label="Rate">
                    <select value={form.rate} onChange={(e) => set({ rate: e.target.value === 'price' ? 'price' : 'pct' })} aria-label="Rate kind">
                      <option value="pct">Percent off list</option>
                      <option value="price">Committed unit price</option>
                    </select>
                  </Field>
                  {form.rate === 'pct' ? (
                    <Field label="Discount (%)" help={resulting !== null ? `→ ${resulting} ${contract.currency} per ${form.unit || 'unit'}` : listPrice === null ? 'choose a priced SKU to see the resulting rate' : undefined}>
                      <input type="number" step="any" min={0} max={100} value={form.discount_pct} onChange={(e) => set({ discount_pct: e.target.value })} aria-label="Discount percent" />
                    </Field>
                  ) : (
                    <Field label={`Committed price (${contract.currency})`} help={resulting !== null && listPrice !== null ? `${listPrice > 0 ? Math.round((1 - resulting / listPrice) * 1000) / 10 : 0} % off the list price` : 'the negotiated unit price'}>
                      <input type="number" step="any" min={0} value={form.committed_price} onChange={(e) => set({ committed_price: e.target.value })} aria-label="Committed price" />
                    </Field>
                  )}
                </>
              ) : (
                <Field label="Carry over">
                  <label className="check">
                    <input type="checkbox" checked={form.rollover} onChange={(e) => set({ rollover: e.target.checked })} aria-label="Carry over" /> Carry the unused part into the next period
                  </label>
                </Field>
              )}
            </FormRow>
          </>
        )}
        <div className="help">
          {contractItemText(
            kind === 'spend'
              ? { kind, sku: '', quantity: '0', amount: form.amount || '0', discount_pct: form.discount_pct || '0' }
              : { kind, sku: form.sku || '<sku>', unit: form.unit, quantity: form.quantity || '0', committed_price: form.rate === 'price' ? form.committed_price : null, discount_pct: form.rate === 'pct' ? form.discount_pct : null, rollover: form.rollover },
            contract.currency,
          )}
        </div>
        {problem ? <Notice kind="bad">{problem}</Notice> : null}
      </form>
    </Modal>
  )
}

/** Remove ONE line. Nothing already invoiced changes; the next run rates without it. */
function DeleteLineConfirm({ contract, item, onClose, onDeleted }: { contract: Contract; item: ContractItem; onClose: () => void; onDeleted: () => void | Promise<void> }) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const what = item.kind === 'spend' ? 'the spend commitment' : `the ${item.sku} ${item.kind === 'commitment' ? 'commitment' : 'allowance'}`
  return (
    <Confirm
      title={`Remove ${what}?`}
      danger
      busy={busy}
      confirmLabel="Remove line"
      onClose={onClose}
      onConfirm={async () => {
        setBusy(true)
        setError('')
        try {
          await api.del(`/contracts/${contract.id}/items/${item.id}`)
          await onDeleted()
        } catch (e) {
          setError(errorText(e))
        } finally {
          setBusy(false)
        }
      }}
      body={
        <div className="stack tight">
          <p>
            {contractItemText(item, contract.currency)} Periods already rated under it keep their statements and their numbers; from the next run {contract.customer_name} is rated without this line.
          </p>
          {error ? <Notice kind="bad">{error}</Notice> : null}
        </div>
      }
    />
  )
}

/**
 * An SLA credit: a percentage of a named statement's charges, issued as a
 * real credit note with the measured availability recorded on it.
 */
function SLACreditModal({ contract, onClose, onIssued }: { contract: Contract; onClose: () => void; onIssued: (n: CreditNote) => void }) {
  const statements = useQuery<unknown>(`/statements?customer_id=${contract.customer_id}`)
  const rows = useMemo(() => asList<Statement>(statements.data, 'statements').filter((s) => s.status !== 'draft'), [statements.data])
  const [form, setForm] = useState({ statement_id: '', pct: '', availability: '', reason: '' })
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const set = (patch: Partial<typeof form>) => setForm((f) => ({ ...f, ...patch }))
  const chosen = rows.find((s) => s.id === form.statement_id)
  const estimate = chosen && Number(form.pct) > 0 ? (toNumber(chosen.total) * Number(form.pct)) / 100 : null

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!form.statement_id) {
      setError('Choose the invoice this credit is against — an SLA credit is always issued against a named statement')
      return
    }
    setBusy(true)
    setError('')
    try {
      const note = await api.post<CreditNote>(`/contracts/${contract.id}/sla-credit`, {
        statement_id: form.statement_id,
        pct: form.pct.trim(),
        measured_availability: form.availability.trim(),
        reason: form.reason.trim(),
      })
      onIssued(note)
    } catch (err) {
      setError(errorText(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      title="Issue an SLA credit"
      onClose={onClose}
      footer={
        <>
          <button type="button" onClick={onClose} disabled={busy}>
            Cancel
          </button>
          <button className="primary" form="sla-credit" disabled={busy}>
            {busy ? 'Issuing…' : 'Issue credit note'}
          </button>
        </>
      }
    >
      <form id="sla-credit" onSubmit={(e) => void submit(e)} className="stack tight">
        {error ? <Notice kind="bad">{error}</Notice> : null}
        <p className="muted small" style={{ margin: 0 }}>
          A credit note against one invoice, numbered and posted to the ledger like any other, recording the percentage owed and the availability actually measured. It reduces what is outstanding
          first; anything beyond that becomes credit on the account.
        </p>
        <Field label="Invoice">
          <select value={form.statement_id} onChange={(e) => set({ statement_id: e.target.value })} aria-label="Invoice">
            <option value="">Choose an issued invoice…</option>
            {rows.map((s) => (
              <option key={s.id} value={s.id}>
                {s.invoice_number || s.period_start} · {s.period_start} to {s.period_end} · {formatMoney(toNumber(s.total), s.currency)}
              </option>
            ))}
          </select>
        </Field>
        <div className="grid2">
          <Field label="SLA credit (%)" help={estimate !== null ? `${formatMoney(estimate, chosen?.currency ?? contract.currency)} of that invoice` : 'a percentage of the invoice total'}>
            <input type="number" step="any" min={0} max={100} value={form.pct} onChange={(e) => set({ pct: e.target.value })} aria-label="SLA credit percent" placeholder="e.g. 10" />
          </Field>
          <Field label="Measured availability (%)" help="Recorded on the credit note, so the document says what it answers.">
            <input type="number" step="any" min={0} max={100} value={form.availability} onChange={(e) => set({ availability: e.target.value })} aria-label="Measured availability" placeholder="e.g. 99.2" />
          </Field>
        </div>
        <Field label="Reason" help="Printed on the credit note and kept on the record. Left empty, the contract, the percentage and the availability are written for you.">
          <input value={form.reason} onChange={(e) => set({ reason: e.target.value })} placeholder="availability 99.2 % against the 99.9 % commitment for August" />
        </Field>
      </form>
    </Modal>
  )
}

function DeleteContractConfirm({ contract, onClose, onDeleted }: { contract: Contract; onClose: () => void; onDeleted: () => void }) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  return (
    <Confirm
      title={`Delete ${contract.name}?`}
      danger
      busy={busy}
      confirmLabel="Delete contract"
      onClose={onClose}
      onConfirm={async () => {
        setBusy(true)
        setError('')
        try {
          await api.del(`/contracts/${contract.id}`)
          onDeleted()
        } catch (e) {
          setError(errorText(e))
        } finally {
          setBusy(false)
        }
      }}
      body={
        <div className="stack tight">
          <p>
            Removes the agreement and its lines. Periods already rated under it keep their statements and their numbers — they simply stop naming a contract. From the next run {contract.customer_name}{' '}
            is rated at its price books alone, with no commitment and no minimum.
          </p>
          {error ? <Notice kind="bad">{error}</Notice> : null}
        </div>
      }
    />
  )
}
