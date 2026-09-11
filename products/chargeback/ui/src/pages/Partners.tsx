import { useMemo, useState, type FormEvent } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { api, asList } from '../api/client'
import type { Discount, Partner, PartnerTier } from '../api/types'
import { useSession } from '../auth/session'
import { DataTable, type Column } from '../components/DataTable'
import { Badge, Confirm, Field, KPI, Modal, Notice, PageHeader, Segmented, Skeleton } from '../components/ui'
import { can } from '../lib/access'
import { formatMoney, formatPct } from '../lib/money'
import { toNumber } from '../lib/num'
import { BILL_TO, billToHelp, billToLabel, partnerTerms } from '../lib/partners'
import { useAction } from '../lib/useAction'
import { useQuery } from '../lib/useQuery'

/**
 * Configure → Partners (DESIGN.md §11) — the resellers and agents this
 * Sovereign sells through, the tiers that set what they pay, and the
 * discounts that make a tier. A tier is a percentage off the ONE list price:
 * there is no second rate card and no markup typed per SKU.
 */

type Dialog = { kind: 'partner'; p?: Partner } | { kind: 'tier'; t?: PartnerTier } | { kind: 'discounts'; t: PartnerTier } | null

export function Partners() {
  const nav = useNavigate()
  const { me } = useSession()
  const canManage = can(me, 'partners.manage')
  const list = useQuery<unknown>('/partners')
  const tiers = useQuery<unknown>('/partners/tiers')
  const rows = useMemo(() => asList<Partner>(list.data, 'partners'), [list.data])
  const tierRows = useMemo(() => asList<PartnerTier>(tiers.data, 'tiers'), [tiers.data])
  const [dialog, setDialog] = useState<Dialog>(null)
  const [flash, setFlash] = useState('')

  const resell = rows.filter((p) => p.bill_to === 'partner').length
  const owed = rows.reduce((n, p) => n + Math.max(0, toNumber(p.balance ?? 0)), 0)
  const credit = rows.reduce((n, p) => n + Math.max(0, -toNumber(p.balance ?? 0)), 0)

  const columns: Column<Partner>[] = [
    {
      key: 'name',
      header: 'Partner',
      value: (p) => p.name,
      render: (p) => (
        <>
          {p.name}
          <span className="sub">
            <span className="mono">{p.slug}</span>
            {p.contact_email ? ` · ${p.contact_email}` : ''}
          </span>
        </>
      ),
    },
    { key: 'status', header: 'Status', value: (p) => p.status, render: (p) => <Badge status={p.status} /> },
    {
      key: 'model',
      header: 'Billing model',
      value: (p) => billToLabel(p.bill_to),
      render: (p) => (
        <>
          <span title={billToHelp(p.bill_to)}>{billToLabel(p.bill_to)}</span>
          <span className="sub">{p.bill_to === 'partner' ? 'invoiced the wholesale statement' : 'credited a commission'}</span>
        </>
      ),
    },
    {
      key: 'tier',
      header: 'Buy price',
      value: (p) => p.tier_name ?? '',
      render: (p) =>
        p.tier_name ? (
          <>
            {p.tier_name}
            <span className="sub">a percentage off list</span>
          </>
        ) : p.commission_pct !== null && p.commission_pct !== undefined ? (
          <>
            {formatPct(toNumber(p.commission_pct), { digits: 0 })} commission
            <span className="sub">of the customer net</span>
          </>
        ) : (
          <span className="muted">no tier — buy is list</span>
        ),
    },
    { key: 'customers', header: 'Customers', value: (p) => p.customer_count ?? 0, numeric: true },
    {
      key: 'retail',
      header: 'Retail book',
      value: (p) => (p.has_retail_rule ? 1 : 0),
      render: (p) =>
        p.bill_to !== 'partner' ? (
          <span className="muted" title="an agent's customers are billed by us, at our own books">
            —
          </span>
        ) : p.has_retail_rule ? (
          <Badge status="derived" kind="ok" />
        ) : (
          <span className="warn" title="its customers are shown the list price until a rule is set">
            no rule
          </span>
        ),
    },
    {
      key: 'balance',
      header: 'Balance',
      value: (p) => toNumber(p.balance ?? 0),
      numeric: true,
      render: (p) => {
        const b = toNumber(p.balance ?? 0)
        if (b === 0) return <span className="muted">settled</span>
        return (
          <>
            <span className={b > 0 ? 'bad' : 'ok'}>{formatMoney(Math.abs(b), '')}</span>
            <span className="sub">{b > 0 ? 'owes us' : 'we owe'}</span>
          </>
        )
      },
    },
  ]

  return (
    <div className="stack">
      <PageHeader
        title="Partners"
        sub="Resellers and agents. One list price per SKU; a tier is a percentage off it, and the margin is derived — never typed."
        actions={
          canManage ? (
            <>
              <button onClick={() => setDialog({ kind: 'tier' })}>New tier</button>
              <button className="primary" onClick={() => setDialog({ kind: 'partner' })}>
                New partner
              </button>
            </>
          ) : null
        }
      />
      {list.error ? <Notice kind="bad">{list.error}</Notice> : null}
      {flash ? <Notice kind="ok">{flash}</Notice> : null}

      <div className="kpis">
        <KPI label="Partners" value={rows.length} note={`${resell} resell · ${rows.length - resell} agent`} />
        <KPI label="Tiers" value={tierRows.length} note="each a percentage off the list price" />
        <KPI label="Owed by partners" value={formatMoney(owed, '')} note="wholesale statements outstanding" tone={owed > 0 ? 'warn' : undefined} />
        <KPI label="Owed to partners" value={formatMoney(credit, '')} note="commission credited and not yet paid out" />
      </div>

      {list.loading && !list.data ? (
        <Skeleton lines={4} />
      ) : (
        <div className="card pad-0">
          <DataTable
            label="Partners"
            columns={columns}
            rows={rows}
            rowKey={(p) => p.id}
            defaultSort={{ key: 'name', dir: 'asc' }}
            onRowClick={(p) => nav(`/partners/${p.id}`)}
            csvName="partners"
            emptyTitle="No partners yet"
            emptyBody="A partner buys at a tier price and either bills its own customers or earns a commission on ours."
            footNote="click a row to open the partner"
          />
        </div>
      )}

      <TiersCard tiers={tierRows} error={tiers.error} canManage={canManage} onEdit={(t) => setDialog({ kind: 'tier', t })} onDiscounts={(t) => setDialog({ kind: 'discounts', t })} />

      {dialog?.kind === 'partner' ? (
        <PartnerModal
          partner={dialog.p}
          tiers={tierRows}
          onClose={() => setDialog(null)}
          onSaved={async (name) => {
            setDialog(null)
            setFlash(`${name} saved`)
            await list.reload()
          }}
        />
      ) : null}
      {dialog?.kind === 'tier' ? (
        <TierModal
          tier={dialog.t}
          onClose={() => setDialog(null)}
          onSaved={async (name) => {
            setDialog(null)
            setFlash(`${name} saved`)
            await tiers.reload()
          }}
        />
      ) : null}
      {dialog?.kind === 'discounts' ? (
        <TierDiscountsModal
          tier={dialog.t}
          onClose={() => setDialog(null)}
          onSaved={async (name) => {
            setDialog(null)
            setFlash(`${name} discounts saved — every partner on the tier was re-derived`)
            await Promise.all([tiers.reload(), list.reload()])
          }}
        />
      ) : null}
    </div>
  )
}

function TiersCard({
  tiers,
  error,
  canManage,
  onEdit,
  onDiscounts,
}: {
  tiers: PartnerTier[]
  error?: string
  canManage: boolean
  onEdit: (t: PartnerTier) => void
  onDiscounts: (t: PartnerTier) => void
}) {
  if (error) return <Notice kind="bad">{error}</Notice>
  return (
    <div className="card pad-0">
      <div className="card-head" style={{ padding: '12px 12px 0' }}>
        <h2>Tiers</h2>
        <span className="hint">a tier is a set of percent discounts off the list price — the same engine, and the same combination rule, as a customer's discounts</span>
      </div>
      {tiers.length === 0 ? (
        <div style={{ padding: 16 }}>
          <p className="muted">No tier yet. A partner without one buys at the list price.</p>
        </div>
      ) : (
        <table aria-label="Partner tiers">
          <thead>
            <tr>
              <th>Tier</th>
              <th>Discounts off list</th>
              <th className="num">Partners</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {tiers.map((t) => (
              <tr key={t.id}>
                <td>
                  {t.name}
                  {t.description ? <span className="sub">{t.description}</span> : null}
                </td>
                <td>
                  {(t.discounts ?? []).length === 0 ? (
                    <span className="warn">none — this tier takes nothing off list</span>
                  ) : (
                    (t.discounts ?? []).map((d: Discount) => (
                      <span key={d.id} className="chip" title={d.sku ? `only ${d.sku}` : 'every SKU'}>
                        {formatPct(toNumber(d.value), { digits: toNumber(d.value) % 1 ? 2 : 0 })} {d.sku ? <span className="mono">{d.sku}</span> : 'all SKUs'}
                      </span>
                    ))
                  )}
                </td>
                <td className="num">{t.partners ?? 0}</td>
                <td className="nowrap actions">
                  {canManage ? (
                    <span className="btn-row">
                      <button className="small" onClick={() => onEdit(t)}>
                        Rename
                      </button>
                      <button className="small" onClick={() => onDiscounts(t)}>
                        Discounts
                      </button>
                    </span>
                  ) : null}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  )
}

function PartnerModal({ partner, tiers, onClose, onSaved }: { partner?: Partner; tiers: PartnerTier[]; onClose: () => void; onSaved: (name: string) => void | Promise<void> }) {
  const editing = Boolean(partner)
  const [slug, setSlug] = useState(partner?.slug ?? '')
  const [name, setName] = useState(partner?.name ?? '')
  const [email, setEmail] = useState(partner?.contact_email ?? '')
  const [billTo, setBillTo] = useState<'partner' | 'customer'>((partner?.bill_to as 'partner' | 'customer') ?? 'partner')
  const [tierId, setTierId] = useState(partner?.tier_id ?? '')
  const [commission, setCommission] = useState(partner?.commission_pct === null || partner?.commission_pct === undefined ? '' : String(toNumber(partner.commission_pct)))
  const act = useAction()

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    const body: Record<string, unknown> = { name: name.trim(), bill_to: billTo, tier_id: tierId, contact_email: email.trim().toLowerCase(), commission_pct: commission.trim() }
    if (!editing) body.slug = slug.trim().toLowerCase()
    const ok = await act.run(`${name} saved`, () => (editing ? api.patch(`/partners/${partner!.id}`, body) : api.post('/partners', body)))
    if (ok) await onSaved(name.trim())
  }

  return (
    <Modal
      title={editing ? `Edit ${partner!.name}` : 'New partner'}
      onClose={onClose}
      footer={
        <>
          <button onClick={onClose} disabled={act.busy}>
            Cancel
          </button>
          <button className="primary" onClick={(e) => void submit(e)} disabled={act.busy || !name.trim() || (!editing && !slug.trim())}>
            {editing ? 'Save' : 'Create partner'}
          </button>
        </>
      }
    >
      <form onSubmit={(e) => void submit(e)} aria-label="Partner">
        {act.error ? <Notice kind="bad">{act.error}</Notice> : null}
        {!editing ? (
          <Field label="Slug" help="lowercase letters, digits and dashes; it names the partner's own account too">
            <input value={slug} onChange={(e) => setSlug(e.target.value)} placeholder="resell-co" />
          </Field>
        ) : null}
        <Field label="Name">
          <input value={name} onChange={(e) => setName(e.target.value)} placeholder="Resell Co" />
        </Field>
        <Field label="Contact email" help="granted partner-owner on this partner: it signs in and sees its own customers, statements, account and margin">
          <input value={email} onChange={(e) => setEmail(e.target.value)} placeholder="ap@resell.example" />
        </Field>
        <Field label="Billing model" help={billToHelp(billTo)}>
          <Segmented<'partner' | 'customer'> value={billTo} onChange={setBillTo} options={BILL_TO.map((b) => ({ value: b.value, label: b.label }))} ariaLabel="Billing model" />
        </Field>
        <Field label="Tier" help="the percentage off list that sets what this partner pays us">
          <select value={tierId} onChange={(e) => setTierId(e.target.value)} aria-label="Tier">
            <option value="">no tier — the partner buys at list</option>
            {tiers.map((t) => (
              <option key={t.id} value={t.id}>
                {t.name}
              </option>
            ))}
          </select>
        </Field>
        {billTo === 'customer' ? (
          <Field label="Commission %" help="used when the partner has no tier: a percentage of what the end customer pays">
            <input value={commission} onChange={(e) => setCommission(e.target.value)} inputMode="decimal" placeholder="20" />
          </Field>
        ) : null}
        <p className="muted tiny">
          {billTo === 'partner'
            ? 'Resell: each period we invoice this partner its customers’ usage at the buy price, and it bills them from its own retail book.'
            : 'Agent: we invoice its end customers at our books and credit the partner the difference between what they pay and its buy price.'}
        </p>
      </form>
    </Modal>
  )
}

function TierModal({ tier, onClose, onSaved }: { tier?: PartnerTier; onClose: () => void; onSaved: (name: string) => void | Promise<void> }) {
  const [name, setName] = useState(tier?.name ?? '')
  const [description, setDescription] = useState(tier?.description ?? '')
  const act = useAction()
  const submit = async () => {
    const ok = await act.run(`${name} saved`, () => api.post('/partners/tiers', { name: name.trim(), description: description.trim() }))
    if (ok) await onSaved(name.trim())
  }
  return (
    <Modal
      title={tier ? `Rename ${tier.name}` : 'New tier'}
      onClose={onClose}
      footer={
        <>
          <button onClick={onClose} disabled={act.busy}>
            Cancel
          </button>
          <button className="primary" onClick={() => void submit()} disabled={act.busy || !name.trim()}>
            Save
          </button>
        </>
      }
    >
      <form aria-label="Tier" onSubmit={(e) => e.preventDefault()}>
        {act.error ? <Notice kind="bad">{act.error}</Notice> : null}
        <Field label="Name">
          <input value={name} onChange={(e) => setName(e.target.value)} placeholder="Gold" />
        </Field>
        <Field label="Description">
          <input value={description} onChange={(e) => setDescription(e.target.value)} placeholder="30 % off list" />
        </Field>
        <p className="muted tiny">Add the discounts that make the tier next: each is a percentage off the list price, for every SKU or for one.</p>
      </form>
    </Modal>
  )
}

interface TierDiscountDraft {
  name: string
  value: string
  sku: string
}

function TierDiscountsModal({ tier, onClose, onSaved }: { tier: PartnerTier; onClose: () => void; onSaved: (name: string) => void | Promise<void> }) {
  const [rows, setRows] = useState<TierDiscountDraft[]>(
    (tier.discounts ?? []).map((d) => ({ name: d.name, value: String(toNumber(d.value)), sku: d.sku ?? '' })),
  )
  const act = useAction()
  const save = async () => {
    const body = { discounts: rows.filter((r) => r.name.trim()).map((r) => ({ name: r.name.trim(), kind: 'percent', value: r.value.trim(), sku: r.sku.trim() })) }
    const ok = await act.run(`${tier.name} saved`, () => api.put(`/partners/tiers/${tier.id}/discounts`, body))
    if (ok) await onSaved(tier.name)
  }
  return (
    <Modal
      title={`${tier.name} — discounts off list`}
      wide
      onClose={onClose}
      footer={
        <>
          <button onClick={onClose} disabled={act.busy}>
            Cancel
          </button>
          <button className="primary" onClick={() => void save()} disabled={act.busy}>
            Save and re-derive
          </button>
        </>
      }
    >
      <form aria-label="Tier discounts" onSubmit={(e) => e.preventDefault()}>
        {act.error ? <Notice kind="bad">{act.error}</Notice> : null}
        <p className="muted">
          Each row is a percentage off the list price. Two rows that both apply to a SKU are combined by the Sovereign's <Link to="/discounts">combination rule</Link> — the same rule that combines a customer's discounts.
        </p>
        {rows.map((r, i) => (
          <div className="row gap" key={i}>
            <Field label="Name">
              <input value={r.name} onChange={(e) => setRows(rows.map((x, k) => (k === i ? { ...x, name: e.target.value } : x)))} placeholder="Gold 30 %" aria-label={`Discount ${i + 1} name`} />
            </Field>
            <Field label="Percent off">
              <input value={r.value} onChange={(e) => setRows(rows.map((x, k) => (k === i ? { ...x, value: e.target.value } : x)))} inputMode="decimal" aria-label={`Discount ${i + 1} percent`} />
            </Field>
            <Field label="SKU" help="empty applies to every SKU">
              <input value={r.sku} onChange={(e) => setRows(rows.map((x, k) => (k === i ? { ...x, sku: e.target.value } : x)))} placeholder="all SKUs" aria-label={`Discount ${i + 1} sku`} />
            </Field>
            <button className="link small danger" onClick={() => setRows(rows.filter((_, k) => k !== i))}>
              Remove
            </button>
          </div>
        ))}
        <button onClick={() => setRows([...rows, { name: '', value: '', sku: '' }])}>Add discount</button>
        <p className="muted tiny">Saving re-derives the retail book of every resell partner on this tier: their buy price just moved.</p>
      </form>
    </Modal>
  )
}

/** The partner assignment on a customer's page (DESIGN.md §11.3). */
export function CustomerPartnerCard({ customer, canManage, onChanged }: { customer: { id: string; name: string; partner_id?: string | null; partner_name?: string }; canManage: boolean; onChanged: () => void | Promise<void> }) {
  const partners = useQuery<unknown>(canManage ? '/partners' : null)
  const rows = useMemo(() => asList<Partner>(partners.data, 'partners'), [partners.data])
  const [choice, setChoice] = useState<string | null>(null)
  const act = useAction()
  const current = rows.find((p) => p.id === customer.partner_id)
  const next = choice === null ? (customer.partner_id ?? '') : choice
  const assign = async () => {
    const ok = await act.run(next ? 'partner assigned' : 'the customer is direct again', () => api.patch(`/customers/${customer.id}`, { partner_id: next }), onChanged)
    if (ok) setChoice(null)
  }

  return (
    <div className="card">
      <h2>Partner</h2>
      {act.error ? <Notice kind="bad">{act.error}</Notice> : null}
      {act.ok ? <Notice kind="ok">{act.ok}</Notice> : null}
      {customer.partner_id ? (
        <p>
          {customer.name} buys through <Link to={`/partners/${customer.partner_id}`}>{customer.partner_name || current?.name || 'its partner'}</Link>
          {current ? ` · ${partnerTerms(current)}` : ''}.
          {current?.bill_to === 'partner' ? ' It is priced from the partner’s retail book, and the partner is invoiced for its usage at the buy price.' : ' We invoice this customer at our own books, and the partner is credited its commission.'}
        </p>
      ) : (
        <p className="muted">{customer.name} buys direct — no partner.</p>
      )}
      {canManage ? (
        <div className="row gap">
          <Field label="Partner" help="one partner per customer; clearing it makes the customer direct again">
            <select value={next} onChange={(e) => setChoice(e.target.value)} aria-label="Partner">
              <option value="">direct — no partner</option>
              {rows.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name} ({billToLabel(p.bill_to)})
                </option>
              ))}
            </select>
          </Field>
          <button className="primary" onClick={() => void assign()} disabled={act.busy || next === (customer.partner_id ?? '')}>
            Save
          </button>
        </div>
      ) : null}
      <p className="muted tiny">Changing this changes which partner is invoiced for this customer's usage from the next statement run; statements already rated keep the partner they were rated under.</p>
    </div>
  )
}

/** A confirm used when a partner is switched off; kept beside its page. */
export function SuspendPartnerConfirm({ partner, onClose, onDone }: { partner: Partner; onClose: () => void; onDone: () => void | Promise<void> }) {
  const act = useAction()
  return (
    <Confirm
      title={`Suspend ${partner.name}`}
      danger
      confirmLabel="Suspend"
      busy={act.busy}
      onClose={onClose}
      onConfirm={async () => {
        const ok = await act.run('suspended', () => api.patch(`/partners/${partner.id}`, { status: 'suspended' }), onDone)
        if (ok) onClose()
      }}
      body={<>Its users can no longer sign in. Its customers keep being collected and billed; nothing about their statements changes.</>}
    />
  )
}
