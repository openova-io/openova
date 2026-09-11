import { useMemo, useState, type FormEvent } from 'react'
import { API_BASE, api, asList } from '../api/client'
import type { CostCentre, CostCentreLine, CostCentreReport, CostCentreResource, CostCentreRule, CostCentreRulesDoc, CostCentresDoc } from '../api/types'
import { DataTable, type Column } from '../components/DataTable'
import { Badge, Confirm, EmptyState, Field, KPI, Modal, Notice, ShareBar, Skeleton } from '../components/ui'
import {
  DEFAULT_PRIORITY,
  breakdownAgrees,
  centreLabel,
  costCentreBody,
  costCentreForm,
  costCentreRuleBody,
  emptyCostCentreForm,
  emptyCostCentreRuleForm,
  isUnassigned,
  ruleMatchText,
  shareOf,
  sourceText,
  validateCostCentre,
  validateCostCentreRule,
  type CostCentreForm,
  type CostCentreRuleForm,
} from '../lib/costcentres'
import { currentPeriod, hasErrors, type Errors } from '../lib/forms'
import { formatMoney, formatPct } from '../lib/money'
import { toNumber } from '../lib/num'
import { useAction } from '../lib/useAction'
import { useQuery } from '../lib/useQuery'

/**
 * Cost centres of one customer (DESIGN.md §19).
 *
 * A cost centre is a LABEL on spend, never a sub-account: it has no ledger,
 * no invoice and no users, and the price waterfall does not know it exists.
 * The page therefore has three parts and no fourth — the centres, the rules
 * that attribute usage to them from the tags the collector already records,
 * and the period read by centre, which on a rated period is the invoice's
 * OWN figures apportioned so the rows add up to it exactly.
 *
 * Reading needs metering.read on the customer (an owner reads its own);
 * every edit needs customers.manage, where a customer's budgets and report
 * schedules already sit.
 */
type Dialog =
  | { kind: 'centre'; centre?: CostCentre }
  | { kind: 'delete-centre'; centre: CostCentre }
  | { kind: 'rule'; rule?: CostCentreRule }
  | { kind: 'override'; row?: CostCentreResource }
  | { kind: 'delete-rule'; rule: CostCentreRule }
  | { kind: 'delete-override'; row: CostCentreResource }
  | null

export function CostCentresPanel({ customerId, canManage, currency }: { customerId: string; canManage: boolean; currency: string }) {
  const [period, setPeriod] = useState(currentPeriod())
  const centresQ = useQuery<CostCentresDoc>(`/customers/${customerId}/cost-centres`)
  const rulesQ = useQuery<CostCentreRulesDoc>(`/customers/${customerId}/cost-centres/rules`)
  const overridesQ = useQuery<unknown>(`/customers/${customerId}/cost-centres/resources`)
  const reportQ = useQuery<CostCentreReport>(`/customers/${customerId}/cost-centres/report?period=${period}`)
  const act = useAction()
  const [dialog, setDialog] = useState<Dialog>(null)
  const close = () => setDialog(null)

  const centres = useMemo(() => centresQ.data?.cost_centres ?? [], [centresQ.data])
  const rules = useMemo(() => rulesQ.data?.rules ?? [], [rulesQ.data])
  const overrides = useMemo(() => asList<CostCentreResource>(overridesQ.data, 'resources'), [overridesQ.data])
  const doc = reportQ.data ?? null
  const cur = doc?.currency || currency
  const lines = doc?.lines ?? []
  const totalValue = toNumber(doc?.totals?.total)
  const unassigned = lines.find((l) => isUnassigned(l.code)) ?? null
  const money = (v: number | string | null | undefined) => formatMoney(v ?? 0, cur)

  const reloadConfig = async () => {
    await Promise.all([centresQ.reload(), rulesQ.reload(), overridesQ.reload(), reportQ.reload()])
  }

  const centreColumns: Column<CostCentre>[] = [
    {
      key: 'code',
      header: 'Code',
      value: (c) => c.code,
      render: (c) => (
        <span>
          <span className="mono">{c.code}</span>
          {!c.active ? <Badge status="inactive" /> : null}
          {c.name ? <span className="sub">{c.name}</span> : <span className="sub muted">no name</span>}
        </span>
      ),
    },
    {
      key: 'rules',
      header: 'Rules',
      value: (c) => c.rules,
      numeric: true,
      render: (c) => (c.rules ? c.rules : <span className="muted">—</span>),
    },
    {
      key: 'resources',
      header: 'Overrides',
      value: (c) => c.resources,
      numeric: true,
      render: (c) => (c.resources ? c.resources : <span className="muted">—</span>),
    },
    ...(canManage
      ? [
          {
            key: 'actions',
            header: '',
            value: () => '',
            sortable: false,
            className: 'nowrap actions',
            render: (c: CostCentre) => (
              <span className="btn-row">
                <button className="link small" disabled={act.busy} onClick={() => setDialog({ kind: 'centre', centre: c })}>
                  Edit
                </button>
                <button className="link small danger" disabled={act.busy} onClick={() => setDialog({ kind: 'delete-centre', centre: c })}>
                  Delete
                </button>
              </span>
            ),
          } as Column<CostCentre>,
        ]
      : []),
  ]

  const ruleColumns: Column<CostCentreRule>[] = [
    { key: 'priority', header: 'Order', value: (r) => r.priority, numeric: true, render: (r) => <span className="mono">{r.priority}</span> },
    {
      key: 'match',
      header: 'When the tag says',
      value: (r) => ruleMatchText(r),
      render: (r) => <span className="mono">{ruleMatchText(r)}</span>,
    },
    {
      key: 'centre',
      header: 'Cost centre',
      value: (r) => r.code,
      render: (r) => (
        <span>
          <span className="mono">{r.code}</span>
          {r.name ? <span className="sub">{r.name}</span> : null}
        </span>
      ),
    },
    ...(canManage
      ? [
          {
            key: 'actions',
            header: '',
            value: () => '',
            sortable: false,
            className: 'nowrap actions',
            render: (r: CostCentreRule) => (
              <span className="btn-row">
                <button className="link small" disabled={act.busy} onClick={() => setDialog({ kind: 'rule', rule: r })}>
                  Edit
                </button>
                <button className="link small danger" disabled={act.busy} onClick={() => setDialog({ kind: 'delete-rule', rule: r })}>
                  Delete
                </button>
              </span>
            ),
          } as Column<CostCentreRule>,
        ]
      : []),
  ]

  const overrideColumns: Column<CostCentreResource>[] = [
    { key: 'resource', header: 'Resource', value: (o) => o.resource_id, render: (o) => <span className="mono">{o.resource_id}</span> },
    {
      key: 'centre',
      header: 'Cost centre',
      value: (o) => o.code,
      render: (o) => (
        <span>
          <span className="mono">{o.code}</span>
          {o.name ? <span className="sub">{o.name}</span> : null}
        </span>
      ),
    },
    { key: 'set_by', header: 'Set by', value: (o) => o.set_by ?? '', render: (o) => (o.set_by ? o.set_by : <span className="muted">—</span>) },
    ...(canManage
      ? [
          {
            key: 'actions',
            header: '',
            value: () => '',
            sortable: false,
            className: 'nowrap actions',
            render: (o: CostCentreResource) => (
              <span className="btn-row">
                <button className="link small" disabled={act.busy} onClick={() => setDialog({ kind: 'override', row: o })}>
                  Edit
                </button>
                <button className="link small danger" disabled={act.busy} onClick={() => setDialog({ kind: 'delete-override', row: o })}>
                  Clear
                </button>
              </span>
            ),
          } as Column<CostCentreResource>,
        ]
      : []),
  ]

  const breakdownColumns: Column<CostCentreLine>[] = [
    {
      key: 'code',
      header: 'Cost centre',
      value: (l) => l.code,
      render: (l) => (
        <span>
          <span className="mono">{l.code}</span>
          {isUnassigned(l.code) ? <span className="sub muted">nothing attributed it — tag the resources or add a rule</span> : l.name ? <span className="sub">{l.name}</span> : null}
        </span>
      ),
      total: () => 'Total',
    },
    {
      key: 'share',
      header: 'Share',
      value: (l) => shareOf(l, totalValue),
      numeric: true,
      sortable: false,
      width: 140,
      render: (l) => (
        <span className="nowrap">
          <ShareBar share={shareOf(l, totalValue) / 100} />
          <span className="muted small"> {formatPct(shareOf(l, totalValue), { digits: 0 })}</span>
        </span>
      ),
    },
    { key: 'usage', header: 'Usage', value: (l) => toNumber(l.usage), numeric: true, render: (l) => money(l.usage), total: () => money(doc?.totals?.usage) },
    { key: 'list', header: 'List', value: (l) => toNumber(l.list), numeric: true, render: (l) => money(l.list), total: () => money(doc?.totals?.list) },
    { key: 'discount', header: 'Discount', value: (l) => toNumber(l.discount), numeric: true, render: (l) => money(l.discount), total: () => money(doc?.totals?.discount) },
    { key: 'net', header: 'Net', value: (l) => toNumber(l.net), numeric: true, render: (l) => money(l.net), total: () => money(doc?.totals?.net) },
    { key: 'tax', header: 'Tax', value: (l) => toNumber(l.tax), numeric: true, render: (l) => money(l.tax), total: () => money(doc?.totals?.tax) },
    { key: 'total', header: 'Total', value: (l) => toNumber(l.total), numeric: true, render: (l) => <b>{money(l.total)}</b>, total: () => <b>{money(doc?.totals?.total)}</b> },
  ]

  const agrees = breakdownAgrees(doc)
  const exportHref = `${API_BASE}/customers/${customerId}/cost-centres/report.csv?period=${period}`

  return (
    <div className="stack">
      {centresQ.error ? <Notice kind="bad">{centresQ.error}</Notice> : null}
      {rulesQ.error ? <Notice kind="bad">{rulesQ.error}</Notice> : null}
      {act.error ? <Notice kind="bad">{act.error}</Notice> : null}
      {act.ok ? <Notice kind="ok">{act.ok}</Notice> : null}
      {!canManage ? (
        <Notice kind="info">
          Read-only: cost centres, their rules and the per-resource overrides are edited with <code>customers.manage</code>, like this customer’s budgets and report schedules.
        </Notice>
      ) : null}

      <div className="kpis">
        <KPI label="Cost centres" value={centres.length} note={centres.filter((c) => !c.active).length ? `${centres.filter((c) => !c.active).length} inactive` : 'all active'} />
        <KPI label="Rules" value={rules.length} note={rules.length ? 'matched in order, lowest first' : 'nothing is attributed by tag yet'} />
        <KPI label="Overrides" value={overrides.length} note="resources pinned to a centre by hand" />
        <KPI
          label={`Unassigned · ${period}`}
          value={money(unassigned?.total ?? 0)}
          tone={unassigned && toNumber(unassigned.total) > 0 ? 'warn' : undefined}
          note={unassigned && toNumber(unassigned.total) > 0 ? `${formatPct(shareOf(unassigned, totalValue), { digits: 0 })} of the period` : 'everything is attributed'}
        />
      </div>

      {/* The period, read by cost centre. On a rated period these are the
          statement's own figures apportioned, so they add up to the invoice
          exactly; before the run they are the period's usage alone. */}
      <div className="card pad-0">
        <div className="card-head" style={{ padding: '12px 12px 0' }}>
          <h2>By cost centre</h2>
          <span className="btn-row">
            <input type="month" value={period} onChange={(e) => setPeriod(e.target.value || currentPeriod())} aria-label="Period" />
            <a className="btn small" href={exportHref}>
              Download CSV
            </a>
          </span>
        </div>
        {reportQ.error ? <Notice kind="bad">{reportQ.error}</Notice> : null}
        {reportQ.loading && !doc ? (
          <Skeleton lines={3} />
        ) : lines.length === 0 ? (
          <EmptyState title={`No usage in ${period}`}>Nothing was collected for this customer in the period, so there is nothing to attribute.</EmptyState>
        ) : (
          <DataTable columns={breakdownColumns} rows={lines} rowKey={(l) => l.code} defaultSort={{ key: 'total', dir: 'desc' }} />
        )}
        <p className={`${agrees ? 'muted' : 'bad'} small`} style={{ padding: '0 12px 12px', margin: 0 }}>
          {sourceText(doc)}
          {doc?.invoice
            ? agrees
              ? ` The rows add up to the invoice exactly: ${money(doc.invoice.subtotal)} net, ${money(doc.invoice.tax)} tax, ${money(doc.invoice.total)} total. A cost centre attributes what is already charged — it never changes it.`
              : ` The rows add up to ${money(doc.totals.total)} and the invoice carries ${money(doc.invoice.total)} — the breakdown and the invoice disagree.`
            : ''}
        </p>
      </div>

      {/* The centres themselves. Flat: a cost centre has no parent, because
          a hierarchy of parties is what this design deliberately is not. */}
      <div className="card pad-0">
        <div className="card-head" style={{ padding: '12px 12px 0' }}>
          <h2>Cost centres</h2>
          {canManage ? (
            <button className="primary" disabled={act.busy} onClick={() => setDialog({ kind: 'centre' })}>
              New cost centre
            </button>
          ) : null}
        </div>
        {centresQ.loading && !centresQ.data ? (
          <Skeleton lines={3} />
        ) : (
          <DataTable
            columns={centreColumns}
            rows={centres}
            rowKey={(c) => c.id}
            emptyTitle="No cost centre for this customer"
            emptyBody={canManage ? 'Add the codes this customer books its spend against, then a rule per tag value. Everything unmatched stays visible under Unassigned.' : 'The operator has not set any up. Until then every figure reads as Unassigned.'}
          />
        )}
        <p className="muted small" style={{ padding: '0 12px 12px', margin: 0 }}>
          A cost centre is a label on this customer’s spend — not a sub-account. It has no invoice, no ledger and no users of its own, and it never changes what is charged.
        </p>
      </div>

      {/* The rules. One tag value names one centre; the per-resource
          override below beats every rule. */}
      <div className="card pad-0">
        <div className="card-head" style={{ padding: '12px 12px 0' }}>
          <h2>Rules</h2>
          {canManage ? (
            <button disabled={act.busy || centres.length === 0} title={centres.length === 0 ? 'Add a cost centre first' : undefined} onClick={() => setDialog({ kind: 'rule' })}>
              New rule
            </button>
          ) : null}
        </div>
        {rulesQ.loading && !rulesQ.data ? (
          <Skeleton lines={3} />
        ) : (
          <DataTable
            columns={ruleColumns}
            rows={rules}
            rowKey={(r) => r.id}
            emptyTitle="No rule yet"
            emptyBody="A rule reads a tag the collector already records — the cloud resource tags and a pod’s own labels — and books the resource to a cost centre."
          />
        )}
        <p className="muted small" style={{ padding: '0 12px 12px', margin: 0 }}>
          A resource is matched against the rules in this order: the lowest number first, then the tag key, then the value. One tag value names one cost centre, so re-pointing a value edits its rule rather than adding a second.
        </p>
      </div>

      {/* The per-resource override. */}
      <div className="card pad-0">
        <div className="card-head" style={{ padding: '12px 12px 0' }}>
          <h2>Per-resource overrides</h2>
          {canManage ? (
            <button disabled={act.busy || centres.length === 0} title={centres.length === 0 ? 'Add a cost centre first' : undefined} onClick={() => setDialog({ kind: 'override' })}>
              Pin a resource
            </button>
          ) : null}
        </div>
        {overridesQ.error ? <Notice kind="bad">{overridesQ.error}</Notice> : null}
        <DataTable
          columns={overrideColumns}
          rows={overrides}
          rowKey={(o) => o.resource_id}
          emptyTitle="No override"
          emptyBody="A resource pinned to a cost centre by hand is listed here. It beats every rule — the answer for the resource nobody tagged and the one tagged wrongly."
        />
      </div>

      {dialog?.kind === 'centre' ? <CentreModal customerId={customerId} centre={dialog.centre} onClose={close} onDone={reloadConfig} /> : null}
      {dialog?.kind === 'rule' ? <RuleModal customerId={customerId} rule={dialog.rule} centres={centres} onClose={close} onDone={reloadConfig} /> : null}
      {dialog?.kind === 'override' ? <OverrideModal customerId={customerId} row={dialog.row} centres={centres} onClose={close} onDone={reloadConfig} /> : null}
      {dialog?.kind === 'delete-centre' ? (
        <Confirm
          title="Delete cost centre"
          danger
          confirmLabel="Delete"
          busy={act.busy}
          onClose={close}
          onConfirm={async () => {
            const ok = await act.run(`${dialog.centre.code} deleted`, () => api.del(`/cost-centres/${dialog.centre.id}`), reloadConfig)
            if (ok) close()
          }}
          body={
            <>
              Delete <b>{centreLabel(dialog.centre)}</b>? Its {dialog.centre.rules} rule{dialog.centre.rules === 1 ? '' : 's'} and {dialog.centre.resources} override
              {dialog.centre.resources === 1 ? '' : 's'} go with it, and that usage reads as Unassigned from now on. Invoices already issued keep the breakdown they were issued with.
            </>
          }
        />
      ) : null}
      {dialog?.kind === 'delete-rule' ? (
        <Confirm
          title="Delete rule"
          danger
          confirmLabel="Delete"
          busy={act.busy}
          onClose={close}
          onConfirm={async () => {
            const ok = await act.run('Rule deleted', () => api.del(`/cost-centres/rules/${dialog.rule.id}`), reloadConfig)
            if (ok) close()
          }}
          body={
            <>
              Stop attributing <b>{ruleMatchText(dialog.rule)}</b> to <b>{dialog.rule.code}</b>? Usage that matched it reads as Unassigned until another rule or an override names it.
            </>
          }
        />
      ) : null}
      {dialog?.kind === 'delete-override' ? (
        <Confirm
          title="Clear override"
          danger
          confirmLabel="Clear"
          busy={act.busy}
          onClose={close}
          onConfirm={async () => {
            const ok = await act.run('Override cleared', () => api.del(`/customers/${customerId}/cost-centres/resources/${encodeURIComponent(dialog.row.resource_id)}`), reloadConfig)
            if (ok) close()
          }}
          body={
            <>
              Clear the override on <b>{dialog.row.resource_id}</b>? It falls back to the rules, and to Unassigned if none matches.
            </>
          }
        />
      ) : null}
    </div>
  )
}

function CentreModal({ customerId, centre, onClose, onDone }: { customerId: string; centre?: CostCentre; onClose: () => void; onDone: () => void | Promise<void> }) {
  const [form, setForm] = useState<CostCentreForm>(centre ? costCentreForm(centre) : emptyCostCentreForm())
  const [errors, setErrors] = useState<Errors<CostCentreForm>>({})
  const act = useAction()
  const set = <K extends keyof CostCentreForm>(k: K, v: CostCentreForm[K]) => setForm((f) => ({ ...f, [k]: v }))

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    const errs = validateCostCentre(form)
    setErrors(errs)
    if (hasErrors(errs)) return
    const body = costCentreBody(form)
    const ok = await act.run(centre ? `${body.code} saved` : `${body.code} created`, () => (centre ? api.put(`/cost-centres/${centre.id}`, body) : api.post(`/customers/${customerId}/cost-centres`, body)), onDone)
    if (ok) onClose()
  }

  return (
    <Modal
      title={centre ? `Edit cost centre — ${centre.code}` : 'New cost centre'}
      onClose={onClose}
      footer={
        <>
          <button type="button" onClick={onClose} disabled={act.busy}>
            Cancel
          </button>
          <button className="primary" form="cost-centre-form" disabled={act.busy}>
            {centre ? 'Save' : 'Create'}
          </button>
        </>
      }
    >
      <form id="cost-centre-form" onSubmit={(e) => void submit(e)}>
        {act.error ? <Notice kind="bad">{act.error}</Notice> : null}
        <Field label="Code" error={errors.code} help="What a tag value matches and what a report and an invoice breakdown print. Unique within this customer.">
          <input value={form.code} onChange={(e) => set('code', e.target.value)} placeholder="e.g. CC-1001" className="mono" autoFocus />
        </Field>
        <Field label="Name" error={errors.name} help="For people. Optional.">
          <input value={form.name} onChange={(e) => set('name', e.target.value)} placeholder="e.g. Platform engineering" />
        </Field>
        <label className="check">
          <input type="checkbox" checked={form.active} onChange={(e) => set('active', e.target.checked)} /> Active — may be chosen for new rules and overrides
        </label>
        <p className="muted small">
          Deactivating a centre keeps every figure it already carries: a report does not move because a label was retired. It only stops new rules and overrides pointing at it.
        </p>
      </form>
    </Modal>
  )
}

function RuleModal({ customerId, rule, centres, onClose, onDone }: { customerId: string; rule?: CostCentreRule; centres: CostCentre[]; onClose: () => void; onDone: () => void | Promise<void> }) {
  const active = centres.filter((c) => c.active || c.id === rule?.cost_centre_id)
  const [form, setForm] = useState<CostCentreRuleForm>(
    rule
      ? { cost_centre_id: rule.cost_centre_id, tag_key: rule.tag_key, tag_value: rule.tag_value, priority: String(rule.priority) }
      : emptyCostCentreRuleForm(active[0]?.id ?? ''),
  )
  const [errors, setErrors] = useState<Errors<CostCentreRuleForm>>({})
  const act = useAction()
  const set = <K extends keyof CostCentreRuleForm>(k: K, v: CostCentreRuleForm[K]) => setForm((f) => ({ ...f, [k]: v }))

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    const errs = validateCostCentreRule(form)
    setErrors(errs)
    if (hasErrors(errs)) return
    const body = costCentreRuleBody(form)
    const ok = await act.run(`${body.tag_key} = ${body.tag_value} saved`, () => api.put(`/customers/${customerId}/cost-centres/rules`, body), onDone)
    if (ok) onClose()
  }

  return (
    <Modal
      title={rule ? `Edit rule — ${ruleMatchText(rule)}` : 'New rule'}
      onClose={onClose}
      footer={
        <>
          <button type="button" onClick={onClose} disabled={act.busy}>
            Cancel
          </button>
          <button className="primary" form="cost-centre-rule-form" disabled={act.busy}>
            Save
          </button>
        </>
      }
    >
      <form id="cost-centre-rule-form" onSubmit={(e) => void submit(e)}>
        {act.error ? <Notice kind="bad">{act.error}</Notice> : null}
        <div className="grid2">
          <Field label="Tag key" error={errors.tag_key} help="A tag the collector already records on the resource.">
            <input value={form.tag_key} onChange={(e) => set('tag_key', e.target.value)} placeholder="e.g. cost-centre" className="mono" autoFocus />
          </Field>
          <Field label="Tag value" error={errors.tag_value} help="The exact value, case-sensitive. Empty matches a tag present with no value.">
            <input value={form.tag_value} onChange={(e) => set('tag_value', e.target.value)} placeholder="e.g. CC-1001" className="mono" />
          </Field>
        </div>
        <Field label="Cost centre" error={errors.cost_centre_id} help="Where usage carrying that tag value is booked.">
          <select value={form.cost_centre_id} onChange={(e) => set('cost_centre_id', e.target.value)}>
            <option value="">Choose…</option>
            {active.map((c) => (
              <option key={c.id} value={c.id}>
                {c.code}
                {c.name ? ` — ${c.name}` : ''}
              </option>
            ))}
          </select>
        </Field>
        <Field label="Order" error={errors.priority} help={`Lowest first when two rules on different tag keys both match. ${DEFAULT_PRIORITY} is the default.`}>
          <input value={form.priority} onChange={(e) => set('priority', e.target.value)} inputMode="numeric" className="mono" />
        </Field>
      </form>
    </Modal>
  )
}

function OverrideModal({
  customerId,
  row,
  centres,
  onClose,
  onDone,
}: {
  customerId: string
  row?: CostCentreResource
  centres: CostCentre[]
  onClose: () => void
  onDone: () => void | Promise<void>
}) {
  const active = centres.filter((c) => c.active || c.id === row?.cost_centre_id)
  const [resourceId, setResourceId] = useState(row?.resource_id ?? '')
  const [centreId, setCentreId] = useState(row?.cost_centre_id ?? active[0]?.id ?? '')
  const [errors, setErrors] = useState<{ resource_id?: string; cost_centre_id?: string }>({})
  const act = useAction()

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    const errs: { resource_id?: string; cost_centre_id?: string } = {}
    if (!resourceId.trim()) errs.resource_id = 'The resource id as the collector records it.'
    if (!centreId) errs.cost_centre_id = 'Choose the cost centre this resource belongs to.'
    setErrors(errs)
    if (errs.resource_id || errs.cost_centre_id) return
    const ok = await act.run(
      `${resourceId.trim()} pinned`,
      () => api.put(`/customers/${customerId}/cost-centres/resources/${encodeURIComponent(resourceId.trim())}`, { cost_centre_id: centreId }),
      onDone,
    )
    if (ok) onClose()
  }

  return (
    <Modal
      title={row ? `Edit override — ${row.resource_id}` : 'Pin a resource to a cost centre'}
      onClose={onClose}
      footer={
        <>
          <button type="button" onClick={onClose} disabled={act.busy}>
            Cancel
          </button>
          <button className="primary" form="cost-centre-override-form" disabled={act.busy}>
            Save
          </button>
        </>
      }
    >
      <form id="cost-centre-override-form" onSubmit={(e) => void submit(e)}>
        {act.error ? <Notice kind="bad">{act.error}</Notice> : null}
        <Field label="Resource" error={errors.resource_id} help="The resource id as the collector records it — the id on the Resources page.">
          <input value={resourceId} onChange={(e) => setResourceId(e.target.value)} readOnly={Boolean(row)} className="mono" autoFocus={!row} placeholder="e.g. 7f3c…-srv-1" />
        </Field>
        <Field label="Cost centre" error={errors.cost_centre_id}>
          <select value={centreId} onChange={(e) => setCentreId(e.target.value)}>
            <option value="">Choose…</option>
            {active.map((c) => (
              <option key={c.id} value={c.id}>
                {c.code}
                {c.name ? ` — ${c.name}` : ''}
              </option>
            ))}
          </select>
        </Field>
        <p className="muted small">An override beats every rule, and keeps beating it when the tag changes. Clear it to hand the resource back to the rules.</p>
      </form>
    </Modal>
  )
}
