import { useEffect, useMemo, useState, type FormEvent } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { api, asList, errorText } from '../api/client'
import type { Contract, ContractItem, CreditNote, Statement } from '../api/types'
import { Badge, Confirm, EmptyState, Field, KPI, Modal, Notice, PageHeader, Skeleton } from '../components/ui'
import { useSession } from '../auth/session'
import { can } from '../lib/access'
import { CONTRACT_STATUSES, contractItemText, daysToEnd, renewalDue, termEnd, termText } from '../lib/contracts'
import { day, today, when } from '../lib/format'
import { formatMoney } from '../lib/money'
import { toNumber } from '../lib/num'
import { useQuery } from '../lib/useQuery'

/**
 * One contract (DESIGN.md §15.9): the term and the monthly minimum, the
 * committed-use and allowance lines the rating engine applies, and the SLA
 * credit issued against a named statement.
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
  const c = q.data
  const canManage = can(me, 'customers.manage', c?.customer_id ?? null)
  const canIssue = can(me, 'billing.issue', c?.customer_id ?? null)
  const [dialog, setDialog] = useState<'edit' | 'items' | 'sla' | 'delete' | null>(null)
  const [flash, setFlash] = useState('')

  if (q.error && !c) return <Notice kind="bad">{q.error}</Notice>
  if (!c) return <Skeleton lines={6} />

  const items = asList<ContractItem>(c.items ?? [], 'items')
  const commitments = items.filter((it) => it.kind === 'commitment')
  const allowances = items.filter((it) => it.kind === 'allowance')
  const on = today()
  const days = daysToEnd(c, on)
  const due = renewalDue(c, on)

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
            {canManage ? <button onClick={() => setDialog('items')}>Edit lines</button> : null}
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
          label="Monthly minimum"
          value={toNumber(c.minimum_commitment) > 0 ? formatMoney(toNumber(c.minimum_commitment), c.currency) : '—'}
          note={toNumber(c.minimum_commitment) > 0 ? 'a period whose net falls below it carries a true-up line' : 'no minimum: every period is invoiced at what it rated'}
        />
        <KPI label="Committed lines" value={commitments.length} note={commitments.length ? 'a quantity per period at a negotiated rate' : 'nothing committed'} />
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
        </div>
        {items.length === 0 ? (
          <EmptyState title="No lines">
            A committed-use line prices a quantity of a SKU per period at a negotiated rate, with anything above it at list. An allowance line includes a quantity on top of whatever the plan already
            includes.
          </EmptyState>
        ) : (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Kind</th>
                  <th>SKU</th>
                  <th className="num">Quantity</th>
                  <th>What it does</th>
                </tr>
              </thead>
              <tbody>
                {items.map((it, i) => (
                  <tr key={it.id ?? `${it.kind}-${it.sku}-${i}`}>
                    <td>
                      <Badge status={it.kind === 'commitment' ? 'committed' : 'allowance'} kind={it.kind === 'commitment' ? 'info' : undefined} />
                    </td>
                    <td className="mono nowrap">{it.sku}</td>
                    <td className="num nowrap">
                      {toNumber(it.quantity).toLocaleString(undefined, { maximumFractionDigits: 6 })}
                      {it.unit ? <span className="sub">{it.unit}</span> : null}
                    </td>
                    <td>{contractItemText(it, c.currency)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>

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
      {dialog === 'items' ? (
        <EditItemsModal
          contract={c}
          onClose={() => setDialog(null)}
          onSaved={async () => {
            setDialog(null)
            setFlash('lines saved')
            await q.reload()
          }}
        />
      ) : null}
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

type ItemDraft = { kind: 'commitment' | 'allowance'; sku: string; unit: string; quantity: string; rate: 'price' | 'pct'; committed_price: string; discount_pct: string; rollover: boolean }

function draftOf(it: ContractItem): ItemDraft {
  const hasPrice = it.committed_price !== null && it.committed_price !== undefined && it.committed_price !== ''
  return {
    kind: it.kind === 'allowance' ? 'allowance' : 'commitment',
    sku: it.sku,
    unit: it.unit ?? '',
    quantity: String(toNumber(it.quantity)),
    rate: hasPrice ? 'price' : 'pct',
    committed_price: hasPrice ? String(toNumber(it.committed_price)) : '',
    discount_pct: toNumber(it.discount_pct) > 0 ? String(toNumber(it.discount_pct)) : '',
    rollover: Boolean(it.rollover),
  }
}

/**
 * Replace the contract's lines. The list sent IS the whole list — the server
 * takes it as a replacement — so the editor shows every line at once rather
 * than pretending each row is saved on its own.
 */
function EditItemsModal({ contract, onClose, onSaved }: { contract: Contract; onClose: () => void; onSaved: () => void | Promise<void> }) {
  const [rows, setRows] = useState<ItemDraft[]>(() => (contract.items ?? []).map(draftOf))
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const set = (i: number, patch: Partial<ItemDraft>) => setRows((rs) => rs.map((r, k) => (k === i ? { ...r, ...patch } : r)))
  const add = (kind: 'commitment' | 'allowance') =>
    setRows((rs) => [...rs, { kind, sku: '', unit: '', quantity: '', rate: 'pct', committed_price: '', discount_pct: '', rollover: false }])
  const remove = (i: number) => setRows((rs) => rs.filter((_, k) => k !== i))

  const problem = useMemo(() => {
    for (const [i, r] of rows.entries()) {
      if (!r.sku.trim()) return `Line ${i + 1} needs a SKU.`
      if (r.quantity.trim() === '' || !(Number(r.quantity) >= 0)) return `Line ${i + 1} needs a quantity.`
      if (r.kind === 'commitment' && r.rate === 'price' && r.committed_price.trim() === '') return `Line ${i + 1}: a committed-use line needs a committed price — a commitment at list is not a commitment.`
      if (r.kind === 'commitment' && r.rate === 'pct' && r.discount_pct.trim() === '') return `Line ${i + 1}: a committed-use line needs a discount percentage — a commitment at list is not a commitment.`
    }
    const seen = new Set<string>()
    for (const r of rows) {
      const key = `${r.kind}:${r.sku.trim()}`
      if (seen.has(key)) return `${r.sku.trim()} appears twice as a ${r.kind} line.`
      seen.add(key)
    }
    return ''
  }, [rows])

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (problem) return
    setBusy(true)
    setError('')
    try {
      await api.put(`/contracts/${contract.id}/items`, {
        items: rows.map((r) => ({
          kind: r.kind,
          sku: r.sku.trim(),
          unit: r.unit.trim(),
          quantity: r.quantity.trim(),
          committed_price: r.kind === 'commitment' && r.rate === 'price' ? r.committed_price.trim() : null,
          discount_pct: r.kind === 'commitment' && r.rate === 'pct' ? r.discount_pct.trim() : null,
          rollover: r.kind === 'allowance' ? r.rollover : false,
        })),
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
      title={`Lines of ${contract.name}`}
      wide
      onClose={onClose}
      footer={
        <>
          <button type="button" onClick={onClose} disabled={busy}>
            Cancel
          </button>
          <button className="primary" form="contract-items" disabled={busy || Boolean(problem)}>
            {busy ? 'Saving…' : 'Save lines'}
          </button>
        </>
      }
    >
      <form id="contract-items" onSubmit={(e) => void submit(e)} className="stack tight">
        {error ? <Notice kind="bad">{error}</Notice> : null}
        <p className="muted small" style={{ margin: 0 }}>
          The list below is the whole list: a line removed here is removed from the contract. Committed use reprices the head of a period&rsquo;s volume; an allowance takes a quantity off it before
          anything is priced at all.
        </p>
        {rows.length === 0 ? <EmptyState title="No lines yet">Add a committed-use line or an allowance.</EmptyState> : null}
        {rows.map((r, i) => (
          <div key={i} className="card flat stack tight">
            <div className="row between">
              <b>
                {r.kind === 'commitment' ? 'Committed use' : 'Allowance'} · line {i + 1}
              </b>
              <button type="button" className="link small danger" onClick={() => remove(i)}>
                Remove
              </button>
            </div>
            <div className="grid3">
              <Field label="SKU">
                <input className="mono" value={r.sku} onChange={(e) => set(i, { sku: e.target.value })} placeholder="ecs.m7n.2xlarge.8" aria-label={`Line ${i + 1} SKU`} />
              </Field>
              <Field label="Unit">
                <input value={r.unit} onChange={(e) => set(i, { unit: e.target.value })} placeholder="instance-hour" aria-label={`Line ${i + 1} unit`} />
              </Field>
              <Field label="Quantity per period">
                <input type="number" step="any" min={0} value={r.quantity} onChange={(e) => set(i, { quantity: e.target.value })} aria-label={`Line ${i + 1} quantity`} />
              </Field>
              {r.kind === 'commitment' ? (
                <>
                  <Field label="Rate">
                    <select value={r.rate} onChange={(e) => set(i, { rate: e.target.value === 'price' ? 'price' : 'pct' })} aria-label={`Line ${i + 1} rate kind`}>
                      <option value="pct">Percent off list</option>
                      <option value="price">Committed unit price</option>
                    </select>
                  </Field>
                  {r.rate === 'pct' ? (
                    <Field label="Discount (%)">
                      <input type="number" step="any" min={0} max={100} value={r.discount_pct} onChange={(e) => set(i, { discount_pct: e.target.value })} aria-label={`Line ${i + 1} discount percent`} />
                    </Field>
                  ) : (
                    <Field label={`Committed price (${contract.currency})`}>
                      <input type="number" step="any" min={0} value={r.committed_price} onChange={(e) => set(i, { committed_price: e.target.value })} aria-label={`Line ${i + 1} committed price`} />
                    </Field>
                  )}
                </>
              ) : (
                <Field label="Carry over">
                  <label className="check">
                    <input type="checkbox" checked={r.rollover} onChange={(e) => set(i, { rollover: e.target.checked })} aria-label={`Line ${i + 1} rollover`} /> Carry the unused part into the next period
                  </label>
                </Field>
              )}
            </div>
            <div className="help">{contractItemText({ kind: r.kind, sku: r.sku || '<sku>', unit: r.unit, quantity: r.quantity || '0', committed_price: r.rate === 'price' ? r.committed_price : null, discount_pct: r.rate === 'pct' ? r.discount_pct : null, rollover: r.rollover }, contract.currency)}</div>
          </div>
        ))}
        {problem ? <Notice kind="bad">{problem}</Notice> : null}
        <div className="row">
          <button type="button" onClick={() => add('commitment')}>
            Add committed use
          </button>
          <button type="button" onClick={() => add('allowance')}>
            Add allowance
          </button>
        </div>
      </form>
    </Modal>
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
