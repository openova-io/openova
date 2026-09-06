import { useMemo, useState } from 'react'
import { Link, useParams, useSearchParams } from 'react-router-dom'
import type { ResourceDetail as ResourceDetailDoc, ResourceLine } from '../api/types'
import { useSession } from '../auth/session'
import { EmptyChart, LineChart, colorFor } from '../components/charts'
import { DataTable, type Column } from '../components/DataTable'
import { DateRange, type DateRangeState } from '../components/DateRange'
import { Badge, EmptyState, KPI, Notice, PageHeader, Skeleton } from '../components/ui'
import { bucketLabel, describeWindow, windowFromParams } from '../lib/dates'
import { day, when } from '../lib/format'
import { explorerHref, resourcesHref } from '../lib/links'
import { formatMoney, formatQty } from '../lib/money'
import { flattenAttrs, kindLabel, transitionRows, unitCost } from '../lib/resources'
import { lensFor, type Lens } from '../lib/scope'
import { useQuery } from '../lib/useQuery'

/**
 * Resource detail (DESIGN.md §2.3 drill-in): daily cost in the window, the
 * SKU lines that make it up, the collector's attributes, the lifecycle
 * transitions, and the most recent usage records behind the numbers.
 */
export function ResourceDetail() {
  const { me } = useSession()
  const { sourceId = '', resourceId = '' } = useParams()
  return <ResourceDetailBody lens={lensFor(me)} sourceId={sourceId} resourceId={resourceId} />
}

export function ResourceDetailBody({ lens, sourceId, resourceId }: { lens: Lens; sourceId: string; resourceId: string }) {
  const [params, setParams] = useSearchParams()
  const { window, preset } = useMemo(() => windowFromParams(params, '30d'), [params])
  const setWindow = (v: DateRangeState) => {
    const p = new URLSearchParams({ preset: v.preset })
    if (v.preset === 'custom') {
      p.set('from', v.window.from)
      p.set('to', v.window.to)
    }
    setParams(p, { replace: true })
  }
  const path = `${lens.resources}/${encodeURIComponent(sourceId)}/${encodeURIComponent(resourceId)}?from=${window.from}&to=${window.to}`
  const res = useQuery<ResourceDetailDoc>(path)
  const [showRecords, setShowRecords] = useState(false)

  const d = res.data
  const cur = d?.currency ?? ''
  const money = (v: number | null | undefined, compact = false) => formatMoney(v, cur, { compact })
  const listHref = resourcesHref(lens)
  const explorerParams = new URLSearchParams({ preset, group_by: 'sku', resource: resourceId })
  if (preset === 'custom') {
    explorerParams.set('from', window.from)
    explorerParams.set('to', window.to)
  }

  const daily = d?.daily ?? []
  const daysWithData = daily.filter((x) => x.has_data).length
  const lines = d?.lines ?? []
  const attrs = useMemo(() => flattenAttrs(d?.attrs), [d])
  const transitions = useMemo(() => transitionRows(d?.transitions ?? (Array.isArray(d?.attrs?.transitions) ? (d.attrs.transitions as Array<Record<string, unknown>>) : null)), [d])
  const records = d?.records_recent ?? []
  const recordKeys = useMemo(() => unionKeys(records), [records])

  const lineColumns: Column<ResourceLine>[] = [
    { key: 'sku', header: 'SKU', value: (l) => l.sku, render: (l) => <span className="mono">{l.sku}</span> },
    { key: 'unit', header: 'Unit', value: (l) => l.unit },
    { key: 'quantity', header: 'Quantity', value: (l) => l.quantity, numeric: true, render: (l) => formatQty(l.quantity) },
    { key: 'unit_cost', header: 'Unit cost', value: (l) => unitCost(l.cost, l.quantity), numeric: true, render: (l) => formatMoney(unitCost(l.cost, l.quantity), cur, { digits: 6 }) },
    { key: 'cost', header: 'Cost', value: (l) => l.cost, numeric: true, render: (l) => money(l.cost), total: (ls) => money(ls.reduce((n, l) => n + l.cost, 0)) },
  ]

  if (res.error) {
    return (
      <div className="stack">
        <PageHeader title="Resource" crumbs={[{ to: listHref, label: 'Resources' }, { label: resourceId }]} />
        <Notice kind="bad">{res.error}</Notice>
        <p className="muted small">
          The resource may belong to another customer or to a source that was removed. <Link to={listHref}>Back to the list</Link>.
        </p>
      </div>
    )
  }
  if (!d) return <Skeleton lines={8} />

  return (
    <div className="stack">
      <PageHeader
        crumbs={[{ to: listHref, label: 'Resources' }, { label: d.name || d.resource_id }]}
        title={
          <>
            {d.name || <span className="muted">(unnamed)</span>} <Badge status={d.status} />
          </>
        }
        sub={
          <>
            {kindLabel(d.kind)} · {d.region || 'no region'}
            {lens.operator ? (
              <>
                {' '}
                · <Link to={`/customers/${d.customer_id}`}>{d.customer_name || d.customer_id}</Link>
              </>
            ) : null}{' '}
            · first seen {day(d.first_seen)} · last seen {day(d.last_seen)}
            {d.deleted_at ? <> · deleted {day(d.deleted_at)}</> : null} · <span className="mono">{d.resource_id}</span>
          </>
        }
        actions={
          <Link to={explorerHref(lens, explorerParams)}>
            <button>Explore this resource</button>
          </Link>
        }
      />

      <div className="toolbar" role="region" aria-label="Window">
        <DateRange value={{ preset, window, granularity: 'day' }} onChange={setWindow} showGranularity={false} />
      </div>

      <div className="kpis">
        <KPI label={`Cost · ${describeWindow(window)}`} value={money(d.cost, true)} note="list rates from the customer's price book" />
        <KPI label="Average per day" value={money(daysWithData ? d.cost / daysWithData : 0, true)} note={`over ${daysWithData} of ${daily.length} day${daily.length === 1 ? '' : 's'} with usage`} />
        <KPI label="SKU lines" value={lines.length} note={lines.length ? lines.map((l) => l.sku).slice(0, 3).join(', ') + (lines.length > 3 ? '…' : '') : 'no priced usage in the window'} tone={lines.length === 0 ? 'warn' : undefined} />
        <KPI label="Status" value={<Badge status={d.status} />} note={transitions.length ? `${transitions.length} transition${transitions.length === 1 ? '' : 's'} recorded` : 'no transitions recorded'} />
      </div>

      <div className="card">
        <div className="card-head">
          <h2>Daily cost</h2>
          <span className="hint">{describeWindow(window)} · missing days had no usage record</span>
        </div>
        {daysWithData > 0 ? (
          <LineChart buckets={daily.map((x) => x.day)} series={[{ key: 'cost', label: 'Cost', values: daily.map((x) => x.cost), color: colorFor(0) }]} missing={daily.map((x) => !x.has_data)} height={220} area format={(v) => money(v)} bucketLabel={bucketLabel} legend={false} />
        ) : (
          <EmptyChart message={`No priced usage for this resource in ${describeWindow(window)}. Widen the window, or check the SKU lines below for usage the price book does not rate.`} />
        )}
      </div>

      <div className="grid side">
        <div className="card pad-0">
          <div className="card-head" style={{ padding: '12px 16px 0' }}>
            <h2>SKU lines</h2>
            <span className="hint">unit cost = cost ÷ quantity in the window</span>
          </div>
          <DataTable columns={lineColumns} rows={lines} rowKey={(l) => `${l.sku}|${l.unit}`} defaultSort={{ key: 'cost', dir: 'desc' }} csvName={`resource-${d.resource_id}-lines`} emptyTitle="No SKU lines" emptyBody={`Nothing was metered for this resource in ${describeWindow(window)}.`} />
        </div>
        <div className="card">
          <div className="card-head">
            <h2>Attributes</h2>
            <span className="hint">as collected</span>
          </div>
          {attrs.length ? (
            <dl className="kv">
              {attrs.map((a) => (
                <div key={a.key} style={{ display: 'contents' }}>
                  <dt>{a.key}</dt>
                  <dd className={/id|flavor|image|cidr|ip/i.test(a.key) ? 'mono' : ''}>{a.value}</dd>
                </div>
              ))}
            </dl>
          ) : (
            <EmptyState title="No attributes">The collector recorded nothing beyond the identity of this resource.</EmptyState>
          )}
        </div>
      </div>

      {transitions.length ? (
        <div className="card pad-0">
          <div className="card-head" style={{ padding: '12px 16px 0' }}>
            <h2>Transitions</h2>
            <span className="hint">status and flavor changes the collector observed · newest first</span>
          </div>
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>At</th>
                  <th>Status</th>
                  <th>Flavor</th>
                  <th>Source</th>
                </tr>
              </thead>
              <tbody>
                {transitions.map((t, i) => (
                  <tr key={`${t.at}-${i}`}>
                    <td className="nowrap">{when(t.at)}</td>
                    <td>{t.status ? <Badge status={t.status} kind={t.status === 'running' || t.status === 'active' ? 'ok' : t.status === 'stopped' ? 'warn' : t.status === 'deleted' ? 'bad' : undefined} /> : <span className="muted">unchanged</span>}</td>
                    <td className="mono">{t.flavor || <span className="muted">unchanged</span>}</td>
                    <td className="muted small">{t.source || '—'}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      ) : null}

      <div className="card">
        <div className="card-head">
          <h2>Recent records</h2>
          <span className="hint">
            {records.length ? (
              <button className="link small" onClick={() => setShowRecords((v) => !v)} aria-expanded={showRecords}>
                {showRecords ? 'Hide' : `Show ${records.length} record${records.length === 1 ? '' : 's'}`}
              </button>
            ) : (
              'none in the window'
            )}
          </span>
        </div>
        {records.length === 0 ? (
          <p className="muted small">No usage records were written for this resource in {describeWindow(window)}.</p>
        ) : showRecords ? (
          <div className="table-wrap">
            <table className="records-table">
              <thead>
                <tr>
                  {recordKeys.map((k) => (
                    <th key={k}>{k.replace(/_/g, ' ')}</th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {records.map((r, i) => (
                  <tr key={i}>
                    {recordKeys.map((k) => (
                      <td key={k} className={typeof r[k] === 'number' ? 'num' : ''}>
                        {cell(r[k])}
                      </td>
                    ))}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <p className="muted small">The raw usage rows the cost above is computed from — collapsed; open them to audit a number.</p>
        )}
      </div>
    </div>
  )
}

/** Column set of a list of loose records: every key, in order of first appearance. */
function unionKeys(records: Array<Record<string, unknown>>): string[] {
  const keys: string[] = []
  for (const r of records) for (const k of Object.keys(r)) if (!keys.includes(k)) keys.push(k)
  return keys
}

function cell(v: unknown): string {
  if (v === null || v === undefined || v === '') return '—'
  if (typeof v === 'number') return formatQty(v)
  if (typeof v === 'object') return JSON.stringify(v)
  const s = String(v)
  return /^\d{4}-\d{2}-\d{2}T/.test(s) ? when(s) : s
}
