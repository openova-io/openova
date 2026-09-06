import { useMemo } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { asList } from '../api/client'
import type { Recommendation } from '../api/types'
import { useSession } from '../auth/session'
import { DataTable, type Column } from '../components/DataTable'
import { Badge, EmptyState, KPI, Notice, PageHeader, Skeleton } from '../components/ui'
import { formatMoney } from '../lib/money'
import {
  SEVERITIES,
  actionFor,
  filterFromParams,
  filterRecommendations,
  groupRecommendations,
  paramsFromFilter,
  severityCounts,
  totalSaving,
  typeMeta,
  type RecommendationFilter,
  type Severity,
} from '../lib/recommendations'
import { customerLens, lensFor, type Lens } from '../lib/scope'
import { useQuery } from '../lib/useQuery'

/**
 * Recommendations (DESIGN.md §3.7) — what the rules found and what it is
 * worth per month. One card per rule type, each stating the rule in one
 * line, with a link to the place where the row gets fixed.
 */
export function Recommendations() {
  const { me } = useSession()
  return <RecommendationsBody lens={lensFor(me)} />
}

/** The same list pinned to one customer (customer detail → Recommendations). */
export function CustomerRecommendations({ customerId }: { customerId: string }) {
  return <RecommendationsBody lens={customerLens(customerId)} embedded />
}

export function RecommendationsBody({ lens, embedded }: { lens: Lens; embedded?: boolean }) {
  const [params, setParams] = useSearchParams()
  const filter = useMemo(() => filterFromParams(params), [params])
  const setFilter = (f: RecommendationFilter) => {
    const p = paramsFromFilter(f)
    const tab = params.get('tab')
    if (tab) p.set('tab', tab)
    setParams(p, { replace: true })
  }
  const res = useQuery<unknown>(lens.recommendations)
  const all = useMemo(() => asList<Recommendation>(res.data, 'recommendations'), [res.data])
  const envelope = res.data && typeof res.data === 'object' && !Array.isArray(res.data) ? (res.data as { total_monthly_saving?: number; currency?: string }) : null
  const currency = envelope?.currency ?? all.find((r) => r.currency)?.currency ?? ''
  const saving = typeof envelope?.total_monthly_saving === 'number' ? envelope.total_monthly_saving : totalSaving(all)
  const counts = useMemo(() => severityCounts(all), [all])
  const types = useMemo(() => Array.from(new Set(all.map((r) => r.type))), [all])
  const rows = useMemo(() => filterRecommendations(all, filter), [all, filter])
  const groups = useMemo(() => groupRecommendations(rows), [rows])
  const showCustomer = lens.operator && !lens.customerId
  const filtered = filter.severities.length > 0 || filter.types.length > 0
  const money = (v: number | null | undefined, compact = false) => formatMoney(v, currency, { compact })

  const toggleSeverity = (s: Severity) => setFilter({ ...filter, severities: filter.severities.includes(s) ? filter.severities.filter((x) => x !== s) : [...filter.severities, s] })
  const toggleType = (t: string) => setFilter({ ...filter, types: filter.types.includes(t) ? filter.types.filter((x) => x !== t) : [...filter.types, t] })

  const columns: Column<Recommendation>[] = [
    { key: 'severity', header: 'Severity', value: (r) => r.severity, render: (r) => <Badge status={r.severity} />, width: 90 },
    ...(showCustomer
      ? [
          {
            key: 'customer',
            header: 'Customer',
            value: (r: Recommendation) => r.customer_name || r.customer_id,
            render: (r: Recommendation) => <Link to={`/customers/${r.customer_id}`}>{r.customer_name || r.customer_id}</Link>,
          },
        ]
      : []),
    {
      key: 'resource',
      header: 'Resource',
      value: (r) => r.resource_name || r.resource_id || '',
      render: (r) =>
        r.resource_id ? (
          <>
            {r.resource_name || <span className="muted">(unnamed)</span>}
            <span className="sub mono">{r.resource_id}</span>
          </>
        ) : (
          <span className="muted">—</span>
        ),
    },
    { key: 'title', header: 'Finding', value: (r) => r.title, render: (r) => <b style={{ fontWeight: 600 }}>{r.title}</b> },
    { key: 'detail', header: 'Detail', value: (r) => r.detail, render: (r) => <span className="muted small">{r.detail}</span> },
    { key: 'saving', header: 'Monthly saving', value: (r) => r.monthly_saving, numeric: true, render: (r) => (r.monthly_saving > 0 ? money(r.monthly_saving) : <span className="muted">—</span>), total: (rs) => (totalSaving(rs) > 0 ? money(totalSaving(rs)) : <span className="muted">—</span>) },
    {
      key: 'action',
      header: 'Action',
      value: (r) => actionFor(r, lens)?.to ?? '',
      sortable: false,
      render: (r) => {
        const a = actionFor(r, lens)
        return a ? <Link to={a.to}>{a.label} →</Link> : <span className="muted small">ask your operator</span>
      },
    },
  ]

  return (
    <div className="stack">
      {!embedded ? <PageHeader title="Recommendations" sub={res.data ? `${all.length} finding${all.length === 1 ? '' : 's'} · ${money(saving, true)} per month at price-book rates` : 'checking the rules…'} /> : null}

      {res.error ? <Notice kind="bad">{res.error}</Notice> : null}

      {res.data ? (
        <div className="kpis">
          <KPI label="Potential monthly saving" value={money(saving, true)} note={`${all.length} recommendation${all.length === 1 ? '' : 's'} · rate × 730 h`} tone={saving > 0 ? 'ok' : undefined} hint="Sum of every recommendation's monthly saving at the customer's price-book rates" />
          <KPI label="High" value={counts.high} note="act this week" tone={counts.high ? 'bad' : undefined} />
          <KPI label="Medium" value={counts.medium} note="worth a look" tone={counts.medium ? 'warn' : undefined} />
          <KPI label="Low" value={counts.low} note="small or uncertain saving" />
        </div>
      ) : null}

      {res.data && all.length ? (
        <div className="chips" role="group" aria-label="Filters">
          <span className="muted small">Severity:</span>
          {SEVERITIES.map((s) => (
            <button key={s} type="button" className={`chip toggle ${filter.severities.includes(s) ? 'on' : ''}`} onClick={() => toggleSeverity(s)} aria-pressed={filter.severities.includes(s)}>
              {s[0].toUpperCase() + s.slice(1)} <span className="n">{counts[s]}</span>
            </button>
          ))}
          <span className="muted small" style={{ marginLeft: 8 }}>
            Type:
          </span>
          {types.map((t) => (
            <button key={t} type="button" className={`chip toggle ${filter.types.includes(t) ? 'on' : ''}`} onClick={() => toggleType(t)} aria-pressed={filter.types.includes(t)}>
              {typeMeta(t).label} <span className="n">{all.filter((r) => r.type === t).length}</span>
            </button>
          ))}
          {filtered ? (
            <button type="button" className="link small" onClick={() => setFilter({ severities: [], types: [] })}>
              Clear
            </button>
          ) : null}
        </div>
      ) : null}

      {res.loading && !res.data ? (
        <div className="card">
          <Skeleton lines={6} />
        </div>
      ) : res.data && all.length === 0 ? (
        <div className="card">
          <EmptyState title="No recommendations">
            Every rule passed: no stopped instance is still billed, no volume is unattached, no elastic IP is unbound, no server ran under 10 % CPU for a week, every SKU in use is priced, every source is fresh
            {lens.operator ? ' and every customer has a price book' : ''}.
          </EmptyState>
        </div>
      ) : res.data && rows.length === 0 ? (
        <div className="card">
          <EmptyState title="Nothing matches these chips">
            {all.length} recommendation{all.length === 1 ? '' : 's'} hidden by the severity and type filters.{' '}
            <button type="button" className="link small" onClick={() => setFilter({ severities: [], types: [] })}>
              Clear the filters
            </button>
          </EmptyState>
        </div>
      ) : (
        groups.map((g) => (
          <div className="card pad-0" key={g.type}>
            <div style={{ padding: '12px 16px 0' }}>
              <div className="card-head" style={{ marginBottom: 2 }}>
                <h2>
                  {g.meta.label} <span className="muted" style={{ fontWeight: 400 }}>· {g.rows.length}</span>
                </h2>
                <span className="hint">{g.saving > 0 ? `${money(g.saving)} / month` : 'no direct saving — a correctness fix'}</span>
              </div>
              <p className="muted small" style={{ margin: '0 0 8px' }}>
                {g.meta.rule}
              </p>
            </div>
            <DataTable columns={columns} rows={g.rows} rowKey={(r) => r.id} csvName={`recommendations-${g.type}`} emptyTitle="Nothing here" />
          </div>
        ))
      )}

      <p className="muted tiny">Savings are the resource's current price-book rate × 730 hours. Fixing a finding removes it on the next collection; nothing here changes a resource by itself.</p>
    </div>
  )
}
