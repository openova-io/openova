import { useEffect, useMemo, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { asList } from '../api/client'
import type { Anomaly, Summary } from '../api/types'
import { useSession } from '../auth/session'
import { RankedBars } from '../components/charts'
import { DataTable, type Column } from '../components/DataTable'
import { DateRange, type DateRangeState } from '../components/DateRange'
import { KPI, Notice, PageHeader, Skeleton } from '../components/ui'
import { anomalyExplorerParams, anomalyKPIs, anomalyKey, anomalyRule, driverRows, keysForDay, sortAnomalies } from '../lib/anomalies'
import { describeWindow, windowFromParams } from '../lib/dates'
import { day } from '../lib/format'
import { explorerHref } from '../lib/links'
import { formatMoney, formatQty } from '../lib/money'
import { customerLens, lensFor, type Lens } from '../lib/scope'
import { useQuery } from '../lib/useQuery'

/**
 * Anomalies (DESIGN.md §3.6) — days whose cost for a customer and service
 * broke out of its 14-day baseline. A row opens to the SKUs and resources
 * whose Δ explains the spike and links to the explorer pinned to that day.
 */
export function Anomalies() {
  const { me } = useSession()
  return <AnomaliesBody lens={lensFor(me)} />
}

/** The same list pinned to one customer (customer detail → Anomalies). */
export function CustomerAnomalies({ customerId }: { customerId: string }) {
  return <AnomaliesBody lens={customerLens(customerId)} embedded />
}

export function AnomaliesBody({ lens, embedded }: { lens: Lens; embedded?: boolean }) {
  const [params, setParams] = useSearchParams()
  const { window, preset } = useMemo(() => windowFromParams(params, '30d'), [params])
  const dayParam = params.get('day')
  const setWindow = (v: DateRangeState) => {
    const p = new URLSearchParams({ preset: v.preset })
    if (v.preset === 'custom') {
      p.set('from', v.window.from)
      p.set('to', v.window.to)
    }
    const tab = params.get('tab')
    if (tab) p.set('tab', tab)
    setParams(p, { replace: true })
  }

  const res = useQuery<unknown>(`${lens.anomalies}?from=${window.from}&to=${window.to}`)
  const rows = useMemo(() => sortAnomalies(asList<Anomaly>(res.data, 'anomalies')), [res.data])
  const envelope = res.data && typeof res.data === 'object' && !Array.isArray(res.data) ? (res.data as { currency?: string }) : null
  // The rows carry no currency; the summary document is the cheapest place that does.
  const sum = useQuery<Summary>(res.data && !envelope?.currency ? lens.cost('summary') : null)
  const currency = envelope?.currency ?? sum.data?.currency ?? ''
  const money = (v: number | null | undefined, compact = false) => formatMoney(v, currency, { compact })
  const kpis = useMemo(() => anomalyKPIs(rows), [rows])
  const showCustomer = lens.operator && !lens.customerId

  const [open, setOpen] = useState<Set<string>>(() => new Set())
  useEffect(() => {
    if (dayParam) setOpen(new Set(keysForDay(rows, dayParam)))
  }, [dayParam, rows])
  const toggle = (a: Anomaly) =>
    setOpen((s) => {
      const n = new Set(s)
      const k = anomalyKey(a)
      if (n.has(k)) n.delete(k)
      else n.add(k)
      return n
    })

  const columns: Column<Anomaly>[] = [
    {
      key: 'day',
      header: 'Day',
      value: (a) => a.day,
      className: 'nowrap',
      render: (a) => (
        <>
          <span className="caret" aria-hidden>
            {open.has(anomalyKey(a)) ? '▾' : '▸'}
          </span>
          {day(a.day)}
        </>
      ),
    },
    {
      key: 'where',
      header: 'Where',
      value: (a) => `${a.label}${showCustomer ? ` (${a.customer_name})` : ''}`,
      render: (a) => (
        <>
          {a.label}
          {showCustomer ? <span className="sub">{a.customer_name || a.customer_id}</span> : null}
        </>
      ),
    },
    { key: 'expected', header: 'Expected', value: (a) => a.expected, numeric: true, render: (a) => money(a.expected) },
    { key: 'actual', header: 'Actual', value: (a) => a.actual, numeric: true, render: (a) => money(a.actual) },
    { key: 'impact', header: 'Impact', value: (a) => a.impact, numeric: true, render: (a) => <span className="bad">+{money(a.impact)}</span>, total: (as) => <span className="bad">+{money(as.reduce((n, a) => n + a.impact, 0))}</span> },
    { key: 'score', header: 'Score', value: (a) => a.score, numeric: true, render: (a) => <span title="standard deviations above the 14-day mean">{formatQty(a.score)} σ</span> },
  ]

  const expanded = (a: Anomaly) => {
    if (!open.has(anomalyKey(a))) return null
    const drivers = driverRows(a.drivers)
    return (
      <div className="stack tight">
        <div className="row between">
          <b className="small">What moved on {day(a.day)}</b>
          <Link to={explorerHref(lens, anomalyExplorerParams(a, showCustomer))} className="small" onClick={(e) => e.stopPropagation()}>
            Open in explorer →
          </Link>
        </div>
        {drivers.length ? (
          <RankedBars rows={drivers} format={(v) => `${v > 0 ? '+' : ''}${money(v)}`} />
        ) : (
          <p className="muted small">No driver breakdown was sent for this day — the explorer link shows the day by SKU.</p>
        )}
      </div>
    )
  }

  return (
    <div className="stack">
      {!embedded ? <PageHeader title="Anomalies" sub={`${describeWindow(window)} · ${res.data ? `${kpis.count} spike${kpis.count === 1 ? '' : 's'}` : '…'} · daily cost vs a 14-day baseline`} /> : null}

      <div className="toolbar" role="region" aria-label="Window">
        <DateRange value={{ preset, window, granularity: 'day' }} onChange={setWindow} showGranularity={false} />
      </div>

      {res.error ? <Notice kind="bad">{res.error}</Notice> : null}

      {res.data ? (
        <div className="kpis">
          <KPI label="Anomalies found" value={kpis.count} note={describeWindow(window)} tone={kpis.count ? 'warn' : 'ok'} />
          <KPI label="Total impact" value={money(kpis.totalImpact, true)} note="cost above the expected baseline" />
          <KPI label="Biggest single day" value={kpis.biggestDay ? money(kpis.biggestDay.impact, true) : '—'} note={kpis.biggestDay ? `${day(kpis.biggestDay.day)} · all spikes that day` : 'no spike'} />
        </div>
      ) : null}

      <div className="card pad-0">
        {res.loading && !res.data ? (
          <div style={{ padding: 16 }}>
            <Skeleton lines={5} />
          </div>
        ) : res.data ? (
          <DataTable
            columns={columns}
            rows={rows}
            rowKey={anomalyKey}
            onRowClick={toggle}
            expanded={expanded}
            csvName={`anomalies-${window.from}`}
            emptyTitle={`No anomalies in ${describeWindow(window)}`}
            emptyBody={<>{anomalyRule(currency)}.</>}
            footNote="click a row for the SKUs and resources behind the spike"
          />
        ) : null}
      </div>

      <p className="muted tiny">
        A day is flagged when its cost for one customer and service is at least 3σ above the trailing 14-day mean, at least 1.3× that mean, and at least 1 {currency || 'currency unit'} over it. Expected is the baseline mean; impact is actual minus expected.
      </p>
    </div>
  )
}
