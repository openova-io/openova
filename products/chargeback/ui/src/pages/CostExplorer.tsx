import { useCallback, useEffect, useMemo, useState } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router-dom'
import { API_BASE, api, asList, errorText } from '../api/client'
import { GROUP_BY_OPTIONS, type DimensionValues, type ExploreResult, type GroupBy, type Metric, type SavedView } from '../api/types'
import { useSession } from '../auth/session'
import { EmptyChart, LineChart, StackedBars, colorFor, seriesFromExplore } from '../components/charts'
import { DataTable, type Column } from '../components/DataTable'
import { DateRange } from '../components/DateRange'
import { DIM_LABEL, FilterChips, filterCount, type Dim } from '../components/FilterChips'
import { Delta, Field, KPI, Modal, Notice, PageHeader, Segmented, ShareBar, Skeleton } from '../components/ui'
import { bucketLabel, describeWindow } from '../lib/dates'
import { apiQuery, drillInto, paramsFromState, stateFromParams, type ChartKind, type ExploreState } from '../lib/exploreState'
import { formatMoney, formatQty } from '../lib/money'
import { customerLens, lensFor, pageHref, type Lens } from '../lib/scope'
import { useQuery } from '../lib/useQuery'

/**
 * Cost explorer (DESIGN.md §2.2) — the AWS Cost Explorer / Azure Cost
 * analysis equivalent. Every control lives in the URL; the table and the
 * chart are two views of the same /cost/explore document.
 */
export function CostExplorer() {
  const { me } = useSession()
  return <ExplorerBody lens={lensFor(me)} />
}

export function CustomerCostExplorer({ customerId }: { customerId: string }) {
  return <ExplorerBody lens={customerLens(customerId)} embedded />
}

type Row = { key: string; label: string; total: number; previous: number; delta_pct: number | null; share: number; resources: number; color: string }

export function ExplorerBody({ lens, embedded }: { lens: Lens; embedded?: boolean }) {
  const [params, setParams] = useSearchParams()
  const nav = useNavigate()
  const state = useMemo(() => stateFromParams(params), [params])
  const setState = useCallback(
    (next: ExploreState) => {
      const p = paramsFromState(next)
      if (params.get('tab')) p.set('tab', params.get('tab')!)
      setParams(p, { replace: true })
    },
    [params, setParams],
  )
  const query = apiQuery(state)
  const res = useQuery<ExploreResult>(`${lens.cost('explore')}?${query}`)
  const dims = useQuery<DimensionValues>(`${lens.cost('dimensions')}?from=${state.window.from}&to=${state.window.to}`)
  const [views, setViews] = useState<SavedView[]>([])
  const [saving, setSaving] = useState(false)
  const [viewName, setViewName] = useState('')
  const [viewErr, setViewErr] = useState('')

  const loadViews = useCallback(async () => {
    try {
      setViews(asList<SavedView>(await api.get<unknown>('/views?page=explore'), 'views'))
    } catch {
      setViews([])
    }
  }, [])
  useEffect(() => {
    void loadViews()
  }, [loadViews])

  const saveView = async () => {
    setViewErr('')
    try {
      await api.post('/views', { name: viewName.trim(), page: 'explore', params: Object.fromEntries(paramsFromState(state).entries()) })
      setSaving(false)
      setViewName('')
      await loadViews()
    } catch (e) {
      setViewErr(errorText(e))
    }
  }
  const openView = (v: SavedView) => {
    const p = new URLSearchParams(v.params as Record<string, string>)
    setParams(p, { replace: false })
  }
  const deleteView = async (v: SavedView) => {
    try {
      await api.del(`/views/${v.id}`)
      await loadViews()
    } catch (e) {
      setViewErr(errorText(e))
    }
  }

  const d = res.data
  const cur = d?.currency ?? ''
  const fmt = (v: number) => (state.metric === 'usage' ? formatQty(v) : formatMoney(v, cur))
  const fmtCompact = (v: number) => (state.metric === 'usage' ? formatQty(v) : formatMoney(v, cur, { compact: true }))
  const rows: Row[] = useMemo(() => {
    if (!d) return []
    const gs = d.groups.map((g, i) => ({ key: g.key, label: g.label, total: g.total, previous: g.previous, delta_pct: g.delta_pct, share: g.share, resources: g.resources, color: colorFor(i) }))
    if (d.other) gs.push({ key: 'other', label: 'Other', total: d.other.total, previous: d.other.previous, delta_pct: d.other.delta_pct, share: d.other.share, resources: d.other.resources, color: colorFor(gs.length) })
    return gs
  }, [d])

  const groupLabel = GROUP_BY_OPTIONS.find((o) => o.value === state.groupBy)?.label ?? state.groupBy
  const columns: Column<Row>[] = [
    {
      key: 'label',
      header: groupLabel,
      value: (r) => r.label,
      render: (r) => (
        <>
          <span className="swatch" style={{ background: r.color }} />
          {r.label}
          {r.key !== r.label && r.key !== 'other' ? <span className="sub mono">{r.key}</span> : null}
        </>
      ),
    },
    { key: 'total', header: describeWindow(state.window), value: (r) => r.total, numeric: true, render: (r) => fmt(r.total), total: (rs) => fmt(rs.reduce((n, r) => n + r.total, 0)) },
    { key: 'previous', header: 'Previous period', value: (r) => r.previous, numeric: true, render: (r) => fmt(r.previous), total: (rs) => fmt(rs.reduce((n, r) => n + r.previous, 0)) },
    { key: 'delta', header: 'Change', value: (r) => r.delta_pct, numeric: true, render: (r) => <Delta pct={r.delta_pct} /> },
    { key: 'share', header: 'Share', value: (r) => r.share, numeric: true, render: (r) => <><ShareBar share={r.share} /> {(r.share * 100).toFixed(1)} %</> },
    { key: 'resources', header: 'Resources', value: (r) => r.resources, numeric: true },
  ]

  const exportHref = `${API_BASE}${lens.cost('export.csv')}?${query}`
  const chartProps = d ? seriesFromExplore(d) : null
  const hasData = d ? d.total.current > 0 || d.total.previous > 0 || d.groups.length > 0 : false
  const labelFor = (dim: Dim, key: string) => dims.data?.dimensions[dim]?.find((v) => v.key === key)?.label ?? key

  return (
    <div className="stack">
      {!embedded ? (
        <PageHeader
          title="Cost explorer"
          sub={`${describeWindow(state.window)} · ${state.granularity === 'day' ? 'daily' : 'monthly'} · by ${groupLabel.toLowerCase()}${filterCount(state.filters) ? ` · ${filterCount(state.filters)} filter${filterCount(state.filters) === 1 ? '' : 's'}` : ''}`}
          actions={
            <>
              {views.length ? (
                <select aria-label="Saved views" value="" onChange={(e) => { const v = views.find((x) => x.id === e.target.value); if (v) openView(v) }} style={{ width: 'auto' }}>
                  <option value="">Saved views…</option>
                  {views.map((v) => (
                    <option key={v.id} value={v.id}>
                      {v.name}
                    </option>
                  ))}
                </select>
              ) : null}
              <button onClick={() => setSaving(true)}>Save view</button>
              <a href={exportHref}>
                <button>Export CSV</button>
              </a>
            </>
          }
        />
      ) : null}

      <div className="toolbar" role="region" aria-label="Explorer controls">
        <DateRange value={{ preset: state.preset, window: state.window, granularity: state.granularity }} onChange={(v) => setState({ ...state, preset: v.preset, window: v.window, granularity: v.granularity })} />
        <span className="sep" />
        <Field label="Group by">
          <select value={state.groupBy} onChange={(e) => setState({ ...state, groupBy: e.target.value as GroupBy, metric: e.target.value === 'sku' ? state.metric : 'cost' })} aria-label="Group by">
            {GROUP_BY_OPTIONS.filter((o) => lens.operator || o.value !== 'customer').map((o) => (
              <option key={o.value} value={o.value}>
                {o.label}
              </option>
            ))}
          </select>
        </Field>
        <Field label="Metric">
          <Segmented<Metric>
            value={state.metric}
            options={[
              { value: 'cost', label: 'Cost' },
              { value: 'usage', label: 'Usage' },
            ]}
            onChange={(metric) => setState({ ...state, metric, groupBy: metric === 'usage' && state.groupBy !== 'sku' && !(state.filters.include.sku?.length === 1) ? 'sku' : state.groupBy })}
            ariaLabel="Metric"
          />
        </Field>
        <Field label="Chart">
          <Segmented<ChartKind>
            value={state.chart}
            options={[
              { value: 'stacked', label: 'Bars' },
              { value: 'line', label: 'Lines' },
              { value: 'area', label: 'Area' },
            ]}
            onChange={(chart) => setState({ ...state, chart })}
            ariaLabel="Chart type"
          />
        </Field>
        <Field label="Show">
          <select value={state.limit} onChange={(e) => setState({ ...state, limit: Number(e.target.value) })} aria-label="Top N">
            {[5, 10, 20, 50, 0].map((n) => (
              <option key={n} value={n}>
                {n === 0 ? 'all groups' : `top ${n}`}
              </option>
            ))}
          </select>
        </Field>
      </div>
      <FilterChips filters={state.filters} onChange={(filters) => setState({ ...state, filters })} dimensions={dims.data} labelFor={labelFor} hideDims={lens.operator ? [] : ['customer']} />

      {res.error ? <Notice kind="bad">{res.error}</Notice> : null}
      {d?.mixed_currency ? <Notice kind="warn">This selection mixes more than one currency; values are shown as {cur}.</Notice> : null}

      {d ? (
        <div className="kpis">
          <KPI label={`Total · ${describeWindow(state.window)}`} value={fmtCompact(d.total.current)} note={<><Delta pct={d.total.delta_pct} /> vs {fmtCompact(d.total.previous)} before</>} />
          <KPI label="Average per bucket" value={fmtCompact(d.buckets.length ? d.total.current / Math.max(1, d.bucket_has_data.filter(Boolean).length) : 0)} note={`${d.bucket_has_data.filter(Boolean).length} of ${d.buckets.length} ${state.granularity === 'day' ? 'days' : 'months'} with data`} />
          {d.forecast ? <KPI label="Forecast month end" value={fmtCompact(d.forecast.month_end)} note={`${d.forecast.method} · ${d.forecast.confidence}`} /> : <KPI label="Groups" value={d.groups.length + (d.other ? 1 : 0)} note={d.other ? `top ${d.groups.length} + other` : 'all shown'} />}
          <KPI label="Resources" value={d.total.resources.toLocaleString()} note="distinct in the window" />
          {d.unpriced.length ? <KPI label="Unpriced SKUs" value={d.unpriced.length} note={d.unpriced.map((u) => u.sku).join(', ')} tone="warn" /> : null}
        </div>
      ) : null}

      <div className="card">
        {res.loading && !d ? (
          <Skeleton lines={5} />
        ) : d && chartProps && hasData ? (
          state.chart === 'stacked' ? (
            <StackedBars {...chartProps} height={300} format={fmt} bucketLabel={bucketLabel} onBarClick={(i, key) => { if (key && key !== 'other' && state.groupBy !== 'none') setState(drillInto(state, key)); else if (state.granularity === 'day') setState({ ...state, preset: 'custom', window: { from: d.buckets[i], to: d.buckets[i] < d.to ? nextDay(d.buckets[i]) : d.to } }) }} />
          ) : (
            <LineChart {...chartProps} height={300} format={fmt} bucketLabel={bucketLabel} area={state.chart === 'area'} />
          )
        ) : d ? (
          <EmptyChart message={`No ${state.metric === 'usage' ? 'usage' : 'priced usage'} for this selection in ${describeWindow(state.window)}.
            ${filterCount(state.filters) ? ' Try removing a filter.' : ''}`} />
        ) : null}
      </div>

      {d ? (
        <div className="card pad-0">
          <DataTable
            columns={columns}
            rows={rows}
            rowKey={(r) => r.key}
            defaultSort={{ key: 'total', dir: 'desc' }}
            csvName={`cost-by-${state.groupBy}-${state.window.from}`}
            onRowClick={state.groupBy !== 'none' ? (r) => { if (r.key === 'other') return; if (state.groupBy === 'resource') nav(pageHref(lens, 'resources', `q=${encodeURIComponent(r.key)}`)); else setState(drillInto(state, r.key)) } : undefined}
            emptyTitle="No groups"
            emptyBody="Nothing matched this selection."
            footNote={state.groupBy !== 'none' ? `click a row to drill into ${DIM_LABEL[state.groupBy as Dim] ?? state.groupBy} → next level` : undefined}
          />
        </div>
      ) : null}

      {d && d.unpriced.length ? (
        <Notice kind="warn">
          Usage without a rate (shown as 0 cost): {d.unpriced.map((u) => `${u.sku} · ${formatQty(u.quantity, u.unit)} · ${u.resources} resource${u.resources === 1 ? '' : 's'}`).join(' — ')}.{' '}
          {lens.operator ? <Link to="/pricebooks">Add the missing rates</Link> : null}
        </Notice>
      ) : null}

      {saving ? (
        <Modal
          title="Save this view"
          onClose={() => setSaving(false)}
          footer={
            <>
              <button onClick={() => setSaving(false)}>Cancel</button>
              <button className="primary" onClick={() => void saveView()} disabled={!viewName.trim()}>
                Save
              </button>
            </>
          }
        >
          <Field label="Name" error={viewErr}>
            <input value={viewName} onChange={(e) => setViewName(e.target.value)} autoFocus placeholder="e.g. Compute by SKU, month to date" />
          </Field>
          <p className="muted small">Saves the period, grouping, filters and chart of this URL. Views are yours; delete from the list below.</p>
          {views.length ? (
            <ul className="small" style={{ margin: '6px 0 0 16px' }}>
              {views.map((v) => (
                <li key={v.id}>
                  {v.name}{' '}
                  <button className="link small danger" onClick={() => void deleteView(v)}>
                    delete
                  </button>
                </li>
              ))}
            </ul>
          ) : null}
        </Modal>
      ) : null}
    </div>
  )
}

function nextDay(d: string): string {
  const t = new Date(d + 'T00:00:00Z')
  t.setUTCDate(t.getUTCDate() + 1)
  return t.toISOString().slice(0, 10)
}
