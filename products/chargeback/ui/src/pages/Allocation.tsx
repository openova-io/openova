import { useEffect, useMemo, useRef, useState, type FormEvent } from 'react'
import { api, errorText } from '../api/client'
import type { AllocationResult, AllocationRow, AllocationSettings } from '../api/types'
import { Donut, EmptyChart, colorFor } from '../components/charts'
import { DataTable, type Column } from '../components/DataTable'
import { DateRange, type DateRangeState } from '../components/DateRange'
import { Badge, Field, KPI, Notice, PageHeader, ShareBar, Skeleton } from '../components/ui'
import { basisPreview, n2, pct, reconciles, sumHours } from '../lib/allocation'
import { describeWindow, presetWindow } from '../lib/dates'
import { when } from '../lib/format'
import { formatMoney, formatPct } from '../lib/money'
import { toNumber } from '../lib/num'
import { customerName, useCustomers } from '../lib/useCustomers'
import { useQuery } from '../lib/useQuery'

/**
 * Allocation (DESIGN.md §2.8, ADR-0014 D3 case 3) — how the Sovereign's cloud
 * bill is split across its Organizations, and what each one's margin is.
 *
 * Settings (GET/PUT /allocation/settings) are the basis weights, the
 * overhead policy and the cost pool; the result (GET /allocation) is one row
 * per Organization plus the platform-overhead line, in currency.
 */

interface Draft {
  vcpu: string
  mem_gib: string
  pvc_gb: string
  overhead_policy: string
  pool: string
  manual_amount: string
  currency: string
  sovereign_customer_id: string
}

function draftOf(s: AllocationSettings | null): Draft {
  return {
    vcpu: String(s?.weights?.vcpu ?? 1),
    mem_gib: String(s?.weights?.mem_gib ?? 0),
    pvc_gb: String(s?.weights?.pvc_gb ?? 0),
    overhead_policy: s?.overhead_policy ?? 'separate',
    pool: s?.pool ?? 'sovereign-cost',
    manual_amount: String(s?.manual_amount ?? 0),
    currency: s?.currency ?? 'OMR',
    sovereign_customer_id: s?.sovereign_customer_id ?? '',
  }
}

function sameDraft(a: Draft, b: Draft): boolean {
  return (Object.keys(a) as Array<keyof Draft>).every((k) => a[k] === b[k])
}

/** Older servers return rows + share_total only; `pool` and `totals` are then derived here. */
type AllocationResponse = Partial<AllocationResult> & { rows?: AllocationRow[]; share_total?: number }

export function Allocation() {
  const settings = useQuery<AllocationSettings>('/allocation/settings')
  const { customers } = useCustomers()
  const [range, setRange] = useState<DateRangeState>(() => ({ preset: 'mtd', window: presetWindow('mtd'), granularity: 'day' }))
  const result = useQuery<AllocationResponse>(`/allocation?from=${range.window.from}&to=${range.window.to}`)
  const [draft, setDraft] = useState<Draft>(() => draftOf(null))
  const [saved, setSaved] = useState<Draft>(() => draftOf(null))
  const [saving, setSaving] = useState(false)
  const [saveErr, setSaveErr] = useState('')
  const [flash, setFlash] = useState('')
  const adopt = useRef(false)

  // Server settings become the form unless the operator has unsaved edits;
  // right after a save the reloaded document is adopted unconditionally.
  useEffect(() => {
    if (!settings.data) return
    const d = draftOf(settings.data)
    const take = adopt.current
    adopt.current = false
    setSaved(d)
    setDraft((cur) => (take || sameDraft(cur, saved) ? d : cur))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [settings.data])

  const set = (patch: Partial<Draft>) => setDraft((d) => ({ ...d, ...patch }))
  const dirty = !sameDraft(draft, saved)
  const weights = { vcpu: toNumber(draft.vcpu), mem_gib: toNumber(draft.mem_gib), pvc_gb: toNumber(draft.pvc_gb) }

  const save = async (e: FormEvent) => {
    e.preventDefault()
    setSaveErr('')
    if (weights.vcpu < 0 || weights.mem_gib < 0 || weights.pvc_gb < 0) {
      setSaveErr('Weights cannot be negative')
      return
    }
    if (weights.vcpu + weights.mem_gib + weights.pvc_gb === 0) {
      setSaveErr('At least one weight must be above 0, or every share is 0')
      return
    }
    if (draft.pool === 'manual' && toNumber(draft.manual_amount) <= 0) {
      setSaveErr('A manual pool needs an amount above 0')
      return
    }
    if (draft.pool === 'sovereign-cost' && !draft.sovereign_customer_id) {
      setSaveErr('Choose the Sovereign customer whose rated cloud cost is the pool')
      return
    }
    setSaving(true)
    try {
      await api.put('/allocation/settings', {
        weights,
        overhead_policy: draft.overhead_policy,
        pool: draft.pool,
        manual_amount: toNumber(draft.manual_amount),
        currency: draft.currency.trim().toUpperCase(),
        sovereign_customer_id: draft.sovereign_customer_id || null,
      })
      setFlash('settings saved — result recalculated')
      adopt.current = true
      await Promise.all([settings.reload(), result.reload()])
    } catch (err) {
      setSaveErr(errorText(err))
    } finally {
      setSaving(false)
    }
  }

  const d = result.data
  const rows = useMemo(() => d?.rows ?? [], [d])
  const shareTotal = d?.share_total ?? 0
  const ok = reconciles(rows.length, shareTotal)
  const totals = d?.totals ?? {
    allocated: rows.reduce((n, r) => n + toNumber(r.allocated_cost), 0),
    revenue: rows.reduce((n, r) => n + toNumber(r.rated_revenue), 0),
    margin: rows.reduce((n, r) => n + toNumber(r.margin), 0),
  }
  const pool = d?.pool
  const cur = pool?.currency ?? settings.data?.currency ?? draft.currency
  const money = (v: number | string | null | undefined, compact = false) => formatMoney(toNumber(v), cur, { compact })
  const hours = sumHours(rows)
  const preview = basisPreview(weights, hours)
  // A margin percentage against a sub-unit revenue (an Organization that
  // started minutes ago) prints an absurd number; below 1 currency unit it
  // is not a meaningful ratio.
  const marginPct = totals.revenue >= 1 ? (totals.margin / totals.revenue) * 100 : null
  const poolSource =
    pool?.source === 'manual' || (pool === undefined && draft.pool === 'manual')
      ? 'manual amount'
      : pool?.customer_name || pool?.customer_id
        ? `rated cloud cost of ${pool.customer_name ?? customerName(customers, pool.customer_id)}`
        : 'rated cloud cost of the Sovereign customer'
  const tierLabel = (r: AllocationRow) => (r.tier === 'platform-overhead' ? 'Platform overhead' : 'Organization')

  const columns: Column<AllocationRow>[] = [
    {
      key: 'org',
      header: 'Organization',
      value: (r) => r.customer_name || r.customer_slug,
      render: (r, i = rows.indexOf(r)) => (
        <>
          <span className="swatch" style={{ background: r.tier === 'platform-overhead' ? '#94a3b8' : colorFor(i) }} />
          {r.customer_name || r.customer_slug}
          {r.customer_slug && r.customer_slug !== r.customer_name ? <span className="sub mono">{r.customer_slug}</span> : null}
        </>
      ),
    },
    { key: 'tier', header: 'Tier', value: (r) => tierLabel(r), render: (r) => <Badge status={tierLabel(r)} kind={r.tier === 'platform-overhead' ? undefined : 'info'} /> },
    { key: 'vcpu', header: 'vCPU-h', value: (r) => r.vcpu_hours, numeric: true, render: (r) => n2(r.vcpu_hours), total: (rs) => n2(sumHours(rs).vcpu_hours) },
    { key: 'mem', header: 'GiB-h', value: (r) => r.mem_gib_hours, numeric: true, render: (r) => n2(r.mem_gib_hours), total: (rs) => n2(sumHours(rs).mem_gib_hours) },
    { key: 'pvc', header: 'GB-h', value: (r) => r.pvc_gb_hours, numeric: true, render: (r) => n2(r.pvc_gb_hours), total: (rs) => n2(sumHours(rs).pvc_gb_hours) },
    { key: 'weight', header: 'Weighted basis', value: (r) => r.weight, numeric: true, render: (r) => n2(r.weight), total: (rs) => n2(rs.reduce((n, r) => n + toNumber(r.weight), 0)) },
    {
      key: 'share',
      header: 'Share',
      value: (r) => r.share,
      numeric: true,
      render: (r) => (
        <>
          <ShareBar share={r.share} /> {pct(r.share)}
        </>
      ),
      total: (rs) => pct(rs.reduce((n, r) => n + toNumber(r.share), 0)),
    },
    { key: 'allocated', header: 'Allocated cost', value: (r) => r.allocated_cost, numeric: true, render: (r) => money(r.allocated_cost), total: (rs) => money(rs.reduce((n, r) => n + toNumber(r.allocated_cost), 0)) },
    { key: 'revenue', header: 'Rated revenue', value: (r) => r.rated_revenue, numeric: true, render: (r) => money(r.rated_revenue), total: (rs) => money(rs.reduce((n, r) => n + toNumber(r.rated_revenue), 0)) },
    {
      key: 'margin',
      header: 'Margin',
      value: (r) => r.margin,
      numeric: true,
      render: (r) => <span className={toNumber(r.margin) < 0 ? 'bad' : toNumber(r.margin) > 0 ? 'ok' : ''}>{money(r.margin)}</span>,
      total: (rs) => {
        const m = rs.reduce((n, r) => n + toNumber(r.margin), 0)
        return <span className={m < 0 ? 'bad' : m > 0 ? 'ok' : ''}>{money(m)}</span>
      },
    },
    { key: 'margin_pct', header: 'Margin %', value: (r) => r.margin_pct, numeric: true, render: (r) => (r.margin_pct === null || toNumber(r.rated_revenue) < 1 ? <span className="muted" title="revenue below 1 unit — the ratio is not meaningful">—</span> : formatPct(r.margin_pct)) },
  ]

  return (
    <div className="stack">
      <PageHeader
        title="Allocation"
        sub="The Sovereign's cloud bill split across Organizations by weighted platform consumption, against what each is billed"
        actions={
          <button onClick={() => void result.reload()} disabled={result.loading}>
            Recalculate
          </button>
        }
      />
      {flash ? <Notice kind="ok">{flash}</Notice> : null}

      <form className="card" onSubmit={save}>
        <div className="card-head">
          <h2>Settings</h2>
          <span className="hint">{settings.data?.updated_at ? `last saved ${when(settings.data.updated_at)}` : settings.error ? settings.error : 'defaults until saved'}</span>
        </div>
        {settings.loading && !settings.data ? (
          <Skeleton lines={3} />
        ) : (
          <div className="grid three">
            <div>
              <h3>Basis weights</h3>
              <div className="grid3">
                <Field label="vCPU-hour">
                  <input type="number" step="any" min={0} value={draft.vcpu} onChange={(e) => set({ vcpu: e.target.value })} aria-label="vCPU-hour weight" />
                </Field>
                <Field label="GiB-hour">
                  <input type="number" step="any" min={0} value={draft.mem_gib} onChange={(e) => set({ mem_gib: e.target.value })} aria-label="GiB-hour weight" />
                </Field>
                <Field label="GB-hour">
                  <input type="number" step="any" min={0} value={draft.pvc_gb} onChange={(e) => set({ pvc_gb: e.target.value })} aria-label="GB-hour weight" />
                </Field>
              </div>
              <div className="muted small">
                basis = vCPU-h × {weights.vcpu} + GiB-h × {weights.mem_gib} + GB-h × {weights.pvc_gb}
              </div>
              <div className="muted small tabular">
                {rows.length ? (
                  <>
                    this window: {preview.terms.map((t) => `${n2(t.hours)} × ${t.weight}`).join(' + ')} = <b>{n2(preview.total)}</b>
                  </>
                ) : (
                  'no consumption in this window to preview against'
                )}
              </div>
            </div>
            <div>
              <h3>Platform overhead</h3>
              <label className="check" style={{ marginBottom: 6 }}>
                <input type="radio" name="overhead" checked={draft.overhead_policy === 'separate'} onChange={() => set({ overhead_policy: 'separate' })} /> Keep as a separate line
              </label>
              <label className="check">
                <input type="radio" name="overhead" checked={draft.overhead_policy === 'distribute'} onChange={() => set({ overhead_policy: 'distribute' })} /> Distribute across Organizations by share
              </label>
              <p className="muted small">The control plane's own pods and volumes: shown as a row of their own, or folded into every Organization's allocated cost.</p>
            </div>
            <div>
              <h3>Cost pool</h3>
              <label className="check" style={{ marginBottom: 6 }}>
                <input type="radio" name="pool" checked={draft.pool === 'sovereign-cost'} onChange={() => set({ pool: 'sovereign-cost' })} /> The Sovereign's rated cloud cost for the window
              </label>
              {draft.pool === 'sovereign-cost' ? (
                <Field label="Sovereign customer" help="The customer whose cost sources carry the cloud bill">
                  <select value={draft.sovereign_customer_id} onChange={(e) => set({ sovereign_customer_id: e.target.value })}>
                    <option value="">{pool?.source === 'sovereign-cost' && pool.customer_name ? `auto: ${pool.customer_name} (the only customer with a verified cloud project)` : '— choose —'}</option>
                    {customers.map((c) => (
                      <option key={c.id} value={c.id}>
                        {c.name}
                      </option>
                    ))}
                  </select>
                </Field>
              ) : null}
              <label className="check" style={{ marginBottom: 6 }}>
                <input type="radio" name="pool" checked={draft.pool === 'manual'} onChange={() => set({ pool: 'manual' })} /> A manual amount per window
              </label>
              {draft.pool === 'manual' ? (
                <div className="grid2">
                  <Field label="Amount">
                    <input type="number" step="any" min={0} value={draft.manual_amount} onChange={(e) => set({ manual_amount: e.target.value })} />
                  </Field>
                  <Field label="Currency">
                    <input value={draft.currency} maxLength={3} onChange={(e) => set({ currency: e.target.value.toUpperCase() })} />
                  </Field>
                </div>
              ) : null}
            </div>
          </div>
        )}
        {saveErr ? <Notice kind="bad">{saveErr}</Notice> : null}
        <div className="row end" style={{ marginTop: 8 }}>
          {dirty ? (
            <button type="button" onClick={() => setDraft(saved)} disabled={saving}>
              Revert
            </button>
          ) : null}
          <button className="primary" disabled={saving || !dirty}>
            {saving ? 'Saving…' : 'Save and recalculate'}
          </button>
        </div>
      </form>

      <div className="toolbar" role="region" aria-label="Allocation window">
        <DateRange value={range} onChange={setRange} showGranularity={false} />
      </div>

      {result.error ? <Notice kind="bad">{result.error}</Notice> : null}
      {d && !ok ? (
        <Notice kind="bad">
          Shares total {pct(shareTotal)}, not 100 %. Some consumption is missing from this split — do not use it as a cost basis until it reconciles.
        </Notice>
      ) : null}

      {d ? (
        <div className="kpis">
          <KPI label="Cost pool" value={pool ? money(pool.amount, true) : '—'} note={poolSource} hint={describeWindow(range.window)} />
          <KPI label="Allocated" value={money(totals.allocated, true)} note={`${pct(shareTotal)} of consumption accounted for`} tone={ok ? undefined : 'bad'} />
          <KPI label="Rated revenue" value={money(totals.revenue, true)} note="what the Organizations are billed at their price books" />
          <KPI label="Margin" value={money(totals.margin, true)} note={marginPct === null ? (totals.revenue > 0 ? 'revenue below 1 unit — % not meaningful' : 'no revenue in the window') : `${formatPct(marginPct)} of revenue`} tone={totals.margin < 0 ? 'bad' : totals.margin > 0 ? 'ok' : undefined} />
        </div>
      ) : null}

      <div className="grid side">
        <div className="card pad-0">
          {result.loading && !d ? (
            <div style={{ padding: 16 }}>
              <Skeleton lines={4} />
            </div>
          ) : (
            <DataTable
              columns={columns}
              rows={rows}
              rowKey={(r) => `${r.customer_id}-${r.tier}`}
              defaultSort={{ key: 'allocated', dir: 'desc' }}
              csvName={`allocation-${range.window.from}`}
              emptyTitle={`No allocation for ${describeWindow(range.window)}`}
              emptyBody="No Organization consumed platform resources in this window, or the platform collector has not completed a pass yet."
            />
          )}
        </div>
        <div className="card">
          <div className="card-head">
            <h2>Allocated cost by row</h2>
            <span className="hint">{describeWindow(range.window)}</span>
          </div>
          {rows.some((r) => toNumber(r.allocated_cost) > 0) ? (
            <Donut
              slices={rows.map((r, i) => ({ key: `${r.customer_id}-${r.tier}`, label: r.tier === 'platform-overhead' ? 'Platform overhead' : r.customer_name || r.customer_slug, value: toNumber(r.allocated_cost), color: r.tier === 'platform-overhead' ? '#94a3b8' : colorFor(i) }))}
              format={(v) => money(v, true)}
              caption="allocated"
            />
          ) : (
            <EmptyChart message={d ? 'Nothing allocated: the pool is 0 for this window, or no row has a share.' : 'Waiting for the result.'} />
          )}
        </div>
      </div>

      <div className="card">
        <h2>How this is computed</h2>
        <p style={{ margin: 0 }}>
          Pool = {poolSource} for {describeWindow(range.window)}
          {pool ? ` (${money(pool.amount)})` : ''}. Each Organization's share = its weighted platform consumption (vCPU-h × {weights.vcpu} + GiB-h × {weights.mem_gib} + GB-h × {weights.pvc_gb}) ÷ all consumption
          {draft.overhead_policy === 'distribute' ? ', with the platform overhead distributed by the same shares' : ', with the platform overhead kept as its own row'}. Allocated cost = pool × share. Margin = what the
          Organization is billed (its price book) − its allocated cloud cost.
        </p>
      </div>
    </div>
  )
}
