import { useCallback, useEffect, useMemo, useState } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router-dom'
import { API_BASE } from '../api/client'
import type { DimensionValues, ResourceList, ResourceRow } from '../api/types'
import { useSession } from '../auth/session'
import { DataTable, type Column } from '../components/DataTable'
import { DateRange } from '../components/DateRange'
import { Badge, Field, KPI, Notice, PageHeader, Segmented, ShareBar, Skeleton } from '../components/ui'
import { describeWindow } from '../lib/dates'
import { day } from '../lib/format'
import { resourceHref } from '../lib/links'
import { formatMoney } from '../lib/money'
import { PAGE_SIZES, STATUS_OPTIONS, isSort, kindLabel, pageRange, paramsFromResourcesState, resourcesQuery, resourcesStateFromParams, type ResourceStatus, type ResourcesState } from '../lib/resources'
import { customerLens, lensFor, type Lens } from '../lib/scope'
import { useQuery } from '../lib/useQuery'

/**
 * Resources (DESIGN.md §2.3 / §3.4) — the inventory joined with its cost in
 * the window. The list is server-paged and server-sorted; every control is
 * in the URL, so a filtered page is a link. Clicking a row opens the
 * resource's detail (daily cost, SKU lines, attributes, transitions).
 */
export function Resources() {
  const { me } = useSession()
  return <ResourcesBody lens={lensFor(me)} />
}

/** The same list pinned to one customer (customer detail → Resources tab). */
export function CustomerResources({ customerId }: { customerId: string }) {
  return <ResourcesBody lens={customerLens(customerId)} embedded />
}

const SEARCH_DEBOUNCE_MS = 300

export function ResourcesBody({ lens, embedded }: { lens: Lens; embedded?: boolean }) {
  const [params, setParams] = useSearchParams()
  const nav = useNavigate()
  const state = useMemo(() => resourcesStateFromParams(params), [params])
  const setState = useCallback(
    (next: ResourcesState) => {
      const p = paramsFromResourcesState(next)
      const tab = params.get('tab')
      if (tab) p.set('tab', tab)
      setParams(p, { replace: true })
    },
    [params, setParams],
  )

  // Search box: local while typing, committed to the URL 300 ms after the last keystroke.
  const [q, setQ] = useState(state.q)
  useEffect(() => {
    setQ(state.q)
  }, [state.q])
  useEffect(() => {
    if (q === state.q) return
    const t = setTimeout(() => setState({ ...state, q, offset: 0 }), SEARCH_DEBOUNCE_MS)
    return () => clearTimeout(t)
  }, [q, state, setState])

  const query = resourcesQuery(state)
  const list = useQuery<ResourceList>(`${lens.resources}?${query}`)
  const dims = useQuery<DimensionValues>(`${lens.cost('dimensions')}?from=${state.window.from}&to=${state.window.to}`)

  // Status counts under the same filters. When the whole unfiltered list is
  // on one page they come from the rows; otherwise three limit=1 probes ask
  // the server for each status's total.
  const d = list.data
  const complete = d ? state.status === 'all' && d.total <= d.rows.length : false
  const probe = (status: ResourceStatus) => (d && !complete ? `${lens.resources}?${resourcesQuery(state, { status, limit: 1, offset: 0 })}` : null)
  const live = useQuery<ResourceList>(probe('live'))
  const stopped = useQuery<ResourceList>(probe('stopped'))
  const deleted = useQuery<ResourceList>(probe('deleted'))
  const counts = complete && d ? countByStatus(d.rows) : { live: live.data?.total, stopped: stopped.data?.total, deleted: deleted.data?.total }

  const cur = d?.currency ?? d?.rows[0]?.currency ?? ''
  const sum = d?.sum_cost ?? 0
  const total = d?.total ?? 0
  const rows = d?.rows ?? []
  const showCustomer = lens.operator && !lens.customerId
  const filtered = Boolean(state.kind || state.region || state.status !== 'all' || state.q)
  const kinds = optionsWith(dims.data?.dimensions.kind ?? [], state.kind, (k) => kindLabel(k, dims.data))
  const regions = optionsWith(dims.data?.dimensions.region ?? [], state.region, (r) => r)

  const columns: Column<ResourceRow>[] = [
    {
      key: 'name',
      header: 'Name',
      value: (r) => r.name || r.resource_id,
      render: (r) => (
        <>
          {r.name || <span className="muted">(unnamed)</span>}
          <span className="sub mono">{r.resource_id}</span>
        </>
      ),
      total: () => `${total.toLocaleString()} resource${total === 1 ? '' : 's'}`,
    },
    { key: 'kind', header: 'Kind', value: (r) => kindLabel(r.kind, dims.data), render: (r) => <span title={r.kind}>{kindLabel(r.kind, dims.data)}</span> },
    ...(showCustomer
      ? [
          {
            key: 'customer',
            header: 'Customer',
            value: (r: ResourceRow) => r.customer_name || r.customer_id,
            sortable: false,
            render: (r: ResourceRow) => (
              <Link to={`/customers/${r.customer_id}`} onClick={(e) => e.stopPropagation()}>
                {r.customer_name || r.customer_id}
              </Link>
            ),
          },
        ]
      : []),
    { key: 'region', header: 'Region', value: (r) => r.region, sortable: false, className: 'nowrap' },
    { key: 'status', header: 'Status', value: (r) => r.status, sortable: false, render: (r) => <Badge status={r.status} /> },
    {
      key: 'cost',
      header: `Cost · ${describeWindow(state.window)}`,
      value: (r) => r.cost,
      numeric: true,
      render: (r) => (
        <>
          <ShareBar share={sum > 0 ? r.cost / sum : 0} />
          {formatMoney(r.cost, r.currency || cur)}
          {r.lines?.length ? <span className="sub">{r.lines.length} SKU{r.lines.length === 1 ? '' : 's'}</span> : null}
        </>
      ),
      total: () => formatMoney(sum, cur),
    },
    { key: 'first_seen', header: 'First seen', value: (r) => r.first_seen, render: (r) => day(r.first_seen), className: 'nowrap' },
    { key: 'last_seen', header: 'Last seen', value: (r) => r.last_seen, render: (r) => (r.deleted_at ? <span title={`deleted ${day(r.deleted_at)}`}>{day(r.last_seen)}</span> : day(r.last_seen)), className: 'nowrap' },
  ]

  const exportHref = `${API_BASE}${lens.resources}.csv?${query}`
  const exportButton = (
    <a href={exportHref}>
      <button>Export CSV</button>
    </a>
  )
  const detailHref = (r: ResourceRow) => {
    const p = new URLSearchParams({ preset: state.preset })
    if (state.preset === 'custom') {
      p.set('from', state.window.from)
      p.set('to', state.window.to)
    }
    return `${resourceHref(lens, r.source_id, r.resource_id)}?${p.toString()}`
  }
  const page = Math.floor(state.offset / state.limit) + 1
  const pages = Math.max(1, Math.ceil(total / state.limit))
  const clearFilters = () => setState({ ...state, kind: '', region: '', status: 'all', q: '', offset: 0 })

  return (
    <div className="stack">
      {!embedded ? (
        <PageHeader
          title="Resources"
          sub={`${describeWindow(state.window)} · ${d ? `${total.toLocaleString()} resource${total === 1 ? '' : 's'}` : '…'}${filtered ? ' matching the filters' : ''} · costs at list rates`}
          actions={exportButton}
        />
      ) : null}

      <div className="toolbar" role="region" aria-label="Resource filters">
        <DateRange value={{ preset: state.preset, window: state.window, granularity: 'day' }} onChange={(v) => setState({ ...state, preset: v.preset, window: v.window, offset: 0 })} showGranularity={false} />
        <span className="sep" />
        <Field label="Kind">
          <select value={state.kind} onChange={(e) => setState({ ...state, kind: e.target.value, offset: 0 })} aria-label="Kind">
            <option value="">All kinds</option>
            {kinds.map((k) => (
              <option key={k.key} value={k.key}>
                {k.label}
              </option>
            ))}
          </select>
        </Field>
        <Field label="Region">
          <select value={state.region} onChange={(e) => setState({ ...state, region: e.target.value, offset: 0 })} aria-label="Region">
            <option value="">All regions</option>
            {regions.map((r) => (
              <option key={r.key} value={r.key}>
                {r.label}
              </option>
            ))}
          </select>
        </Field>
        <Field label="Status">
          <Segmented<ResourceStatus> value={state.status} options={STATUS_OPTIONS} onChange={(status) => setState({ ...state, status, offset: 0 })} ariaLabel="Status" />
        </Field>
        <Field label="Search">
          <input type="search" value={q} onChange={(e) => setQ(e.target.value)} placeholder="name or resource id" aria-label="Search resources" style={{ width: 220 }} />
        </Field>
        {embedded ? (
          <>
            <span className="grow" />
            {exportButton}
          </>
        ) : null}
      </div>

      {list.error ? <Notice kind="bad">{list.error}</Notice> : null}

      {d ? (
        <div className="kpis">
          <KPI label="Resources" value={total.toLocaleString()} note={filtered ? 'matching the filters' : `seen in ${describeWindow(state.window)}`} />
          <KPI label="Cost in window" value={formatMoney(sum, cur, { compact: true })} note={`${describeWindow(state.window)} · all ${total.toLocaleString()} rows`} hint="Sum over every matching resource, not only this page" />
          <KPI label="Live" value={countText(counts.live)} note="present at the last collection" />
          <KPI label="Stopped" value={countText(counts.stopped)} note="billed as compute while the price book says so" tone={counts.stopped ? 'warn' : undefined} />
          <KPI label="Deleted" value={countText(counts.deleted)} note="gone; billed up to the deletion" />
        </div>
      ) : null}

      <div className="card pad-0">
        {list.loading && !d ? (
          <div style={{ padding: 16 }}>
            <Skeleton lines={6} />
          </div>
        ) : d ? (
          <DataTable
            columns={columns}
            rows={rows}
            rowKey={(r) => `${r.source_id}/${r.resource_id}`}
            sort={{ key: state.sort, dir: state.order }}
            onSortChange={(s) => {
              if (isSort(s.key)) setState({ ...state, sort: s.key, order: s.dir, offset: 0 })
            }}
            onRowClick={(r) => nav(detailHref(r))}
            csvName={`resources-${state.window.from}-page${page}`}
            emptyTitle={filtered ? `No resources match in ${describeWindow(state.window)}` : `No resources seen in ${describeWindow(state.window)}`}
            emptyBody={
              filtered ? (
                <>
                  Nothing has this kind, region, status and search together.{' '}
                  <button className="link small" onClick={clearFilters}>
                    Clear the filters
                  </button>
                </>
              ) : (
                <>Inventory appears after a cost source is verified and has collected at least once{lens.operator ? '; check the customer’s Sources tab' : ''}.</>
              )
            }
            footNote={`${pageRange(state.offset, rows.length, total)} · sorted by ${state.sort.replace('_', ' ')} ${state.order === 'asc' ? '↑' : '↓'} · click a row for daily cost, SKU lines and attributes`}
          />
        ) : null}
      </div>

      {d && total > 0 ? (
        <div className="row between">
          <span className="muted small">
            page {page} of {pages}
          </span>
          <span className="row">
            <select value={state.limit} onChange={(e) => setState({ ...state, limit: Number(e.target.value), offset: 0 })} aria-label="Rows per page" style={{ width: 'auto' }}>
              {PAGE_SIZES.map((n) => (
                <option key={n} value={n}>
                  {n} per page
                </option>
              ))}
            </select>
            <span className="pager">
              <button className="small" onClick={() => setState({ ...state, offset: Math.max(0, state.offset - state.limit) })} disabled={state.offset === 0} aria-label="Previous page">
                ‹
              </button>
              <span>
                {page} / {pages}
              </span>
              <button className="small" onClick={() => setState({ ...state, offset: state.offset + state.limit })} disabled={state.offset + state.limit >= total} aria-label="Next page">
                ›
              </button>
            </span>
          </span>
        </div>
      ) : null}
    </div>
  )
}

function countByStatus(rows: ResourceRow[]): { live: number; stopped: number; deleted: number } {
  const c = { live: 0, stopped: 0, deleted: 0 }
  for (const r of rows) if (r.status in c) c[r.status as keyof typeof c]++
  return c
}

function countText(n: number | undefined): string {
  return n === undefined ? '…' : n.toLocaleString()
}

/** Select options from the dimension values, keeping a URL-supplied value that the window no longer offers. */
function optionsWith(values: Array<{ key: string; label: string }>, current: string, label: (key: string) => string): Array<{ key: string; label: string }> {
  const out = values.map((v) => ({ key: v.key, label: v.label || label(v.key) }))
  if (current && !out.some((o) => o.key === current)) out.unshift({ key: current, label: label(current) })
  return out
}
