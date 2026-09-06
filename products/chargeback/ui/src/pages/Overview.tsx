import { Link, useNavigate } from 'react-router-dom'
import { useSession } from '../auth/session'
import type { ExploreResult, Summary } from '../api/types'
import { exploreQuery } from '../api/types'
import { Donut, EmptyChart, ProgressBar, RankedBars, StackedBars, seriesFromExplore } from '../components/charts'
import { Badge, Delta, KPI, Notice, PageHeader, Skeleton } from '../components/ui'
import { bucketLabel, describeWindow, presetWindow } from '../lib/dates'
import { formatMoney, formatPct } from '../lib/money'
import { customerLens, lensFor, pageHref, type Lens } from '../lib/scope'
import { readGroups, readKPIs } from '../lib/summary'
import { useQuery } from '../lib/useQuery'
import { day, when } from '../lib/format'

/**
 * Overview — the landing page (DESIGN.md §2.1). Every number comes from
 * /cost/summary (readers in lib/summary.ts, pinned to the Go fixture) and the
 * 30-day daily chart from /cost/explore grouped by service.
 */
export function Overview() {
  const { me } = useSession()
  return <OverviewBody lens={lensFor(me)} title="Overview" />
}

/** The same page pinned to one customer (customer detail → Overview tab). */
export function CustomerOverview({ customerId }: { customerId: string }) {
  return <OverviewBody lens={customerLens(customerId)} embedded />
}

export function OverviewBody({ lens, title, embedded }: { lens: Lens; title?: string; embedded?: boolean }) {
  const nav = useNavigate()
  const sum = useQuery<Summary>(lens.cost('summary'))
  const win = presetWindow('30d')
  const daily = useQuery<ExploreResult>(`${lens.cost('explore')}?${exploreQuery({ from: win.from, to: win.to, granularity: 'day', group_by: 'kind', limit: 6 })}`)

  if (sum.error) return <Notice kind="bad">{sum.error}</Notice>
  if (!sum.data) return <Skeleton lines={6} />
  const s = sum.data
  const k = readKPIs(s)
  const byCustomer = readGroups(s.by_customer)
  const byKind = readGroups(s.by_kind)
  // Sovereign-wide (operator, not pinned to one customer): show the customer split.
  const wide = lens.operator && !lens.customerId
  const cur = k.currency
  const money = (v: number | null | undefined, compact = false) => formatMoney(v, cur, { compact })
  const explorerHref = (q: string) => pageHref(lens, 'explore', q)

  return (
    <div className="stack">
      {!embedded ? (
        <PageHeader
          title={title ?? 'Overview'}
          sub={
            <>
              {describeWindow({ from: s.mtd.from, to: s.mtd.to > s.mtd.from ? s.mtd.to : s.mtd.from })} to date · {k.resourcesLive.toLocaleString()} live resources · collected{' '}
              {k.lastCollectedAt ? when(k.lastCollectedAt) : 'never'}
            </>
          }
          actions={
            <>
              <Link to={explorerHref('preset=mtd&group_by=kind')}>
                <button>Open in cost explorer</button>
              </Link>
              {lens.operator ? (
                <Link to="/statements">
                  <button className="primary">Statements</button>
                </Link>
              ) : null}
            </>
          }
        />
      ) : null}

      {s.mixed_currency ? <Notice kind="warn">Customers bill in more than one currency; totals below mix currencies and are shown in {cur}.</Notice> : null}
      {k.unpricedCount > 0 ? (
        <Notice kind="warn">
          {k.unpricedCount} SKU{k.unpricedCount === 1 ? '' : 's'} in use carry no rate in the price book, so their cost shows as 0:{' '}
          <span className="mono">{s.unpriced_skus.map((u) => u.sku).join(', ')}</span>.{' '}
          {lens.operator ? <Link to="/pricebooks">Add rates</Link> : null}
        </Notice>
      ) : null}

      {embedded ? null : (
        <div className="kpis">
          <KPI label="Month to date" value={money(k.mtd, true)} note={<><Delta pct={k.momDeltaPct} /> vs same days last month ({money(k.prevMTD, true)})</>} hint="Cost of the current calendar month so far" />
          <KPI
            label="Forecast month end"
            value={k.forecastMonthEnd === null ? '—' : money(k.forecastMonthEnd, true)}
            note={k.forecastMethod ? `${k.forecastMethod} · ${k.forecastConfidence} confidence` : 'needs one complete day'}
            tone={k.forecastConfidence === 'low' ? 'warn' : undefined}
            hint="Complete days so far + daily run rate × days left"
          />
          <KPI label={`Last month (${k.lastMonthPeriod})`} value={money(k.lastMonth, true)} note="full calendar month" />
          <KPI label="Average per day" value={money(k.avgDaily, true)} note={`over ${s.last_30d?.days_with_data ?? 0} days with data of the last 30`} />
          <KPI label="Live resources" value={k.resourcesLive.toLocaleString()} note={`${k.sourcesVerified} source${k.sourcesVerified === 1 ? '' : 's'} verified${k.sourcesFailed ? ` · ${k.sourcesFailed} failed` : ''}`} tone={k.sourcesFailed ? 'bad' : undefined} />
          {wide ? (
            <KPI label="Customers" value={k.customersActive} note={`${k.customersPending} pending · ${k.customersSuspended} suspended`} />
          ) : (
            <KPI label="Statements" value={k.draftStatements + k.issuedStatements} note={`${k.draftStatements} draft · ${k.issuedStatements} issued`} />
          )}
        </div>
      )}

      <div className="grid side">
        <div className="card">
          <div className="card-head">
            <h2>Daily cost, last 30 days</h2>
            <span className="hint">
              stacked by service · <Link to={explorerHref('preset=30d&group_by=kind')}>explore</Link>
            </span>
          </div>
          {daily.error ? (
            <Notice kind="bad">{daily.error}</Notice>
          ) : daily.data ? (
            daily.data.total.current > 0 || daily.data.total.previous > 0 ? (
              <StackedBars
                {...seriesFromExplore(daily.data)}
                height={240}
                format={(v) => money(v)}
                bucketLabel={bucketLabel}
                onBarClick={(i) => void nav(explorerHref(`preset=custom&from=${daily.data!.buckets[i]}&to=${daily.data!.buckets[i]}&group_by=kind`))}
              />
            ) : (
              <EmptyChart message={'No priced usage in the last 30 days.'} />
            )
          ) : (
            <Skeleton lines={4} />
          )}
        </div>
        <div className="card">
          <div className="card-head">
            <h2>{wide ? 'Cost by customer' : 'Cost by service'}</h2>
            <span className="hint">month to date</span>
          </div>
          {wide ? (
            byCustomer.length ? (
              <Donut slices={byCustomer.map((g) => ({ key: g.key, label: g.label, value: g.value }))} format={(v) => money(v, true)} caption="Month to date" onSliceClick={(key) => { if (key !== 'other') nav(`/customers/${key}`) }} />
            ) : (
              <EmptyChart message={'No customer has priced usage this month.'} />
            )
          ) : byKind.length ? (
            <Donut slices={byKind.map((g) => ({ key: g.key, label: g.label, value: g.value }))} format={(v) => money(v, true)} caption="Month to date" />
          ) : (
            <EmptyChart message={'No priced usage this month.'} />
          )}
        </div>
      </div>

      <div className="grid two">
        <div className="card">
          <div className="card-head">
            <h2>Cost by service</h2>
            <span className="hint">
              month to date vs previous · <Link to={explorerHref('preset=mtd&group_by=kind')}>explore</Link>
            </span>
          </div>
          {byKind.length ? (
            <RankedBars rows={byKind.map((g) => ({ key: g.key, label: g.label, value: g.value, share: g.share, delta_pct: g.delta_pct }))} format={(v) => money(v)} onClick={(key) => nav(explorerHref(`preset=mtd&group_by=sku&kind=${encodeURIComponent(key)}`))} />
          ) : (
            <EmptyChart message={'Nothing priced this month.'} />
          )}
        </div>
        {wide ? (
          <div className="card">
            <div className="card-head">
              <h2>Top customers</h2>
              <span className="hint">month to date</span>
            </div>
            {byCustomer.length ? (
              <RankedBars rows={byCustomer.map((g) => ({ key: g.key, label: g.label, value: g.value, share: g.share, delta_pct: g.delta_pct }))} format={(v) => money(v)} onClick={(key) => key !== 'other' && nav(`/customers/${key}`)} />
            ) : (
              <EmptyChart message={'No customer has priced usage this month.'} />
            )}
          </div>
        ) : (
          <div className="card">
            <div className="card-head">
              <h2>Statements</h2>
              <Link to={pageHref(lens, 'statements')} className="hint">
                all
              </Link>
            </div>
            <StatementsMini s={s} route={lens.route} />
          </div>
        )}
      </div>

      <div className="grid two">
        <div className="card">
          <div className="card-head">
            <h2>Budgets</h2>
            <Link to={pageHref(lens, 'budgets')} className="hint">
              manage
            </Link>
          </div>
          {s.budgets.length === 0 ? (
            <div className="empty">
              <b>No budget yet</b>
              <Link to={pageHref(lens, 'budgets')}>Set a monthly budget</Link> to get alerts at 50 / 80 / 100 %.
            </div>
          ) : (
            <div className="strip">
              {s.budgets.map((b) => (
                <div className="strip-row" key={b.id}>
                  <div>
                    <div>{b.name}</div>
                    <div className="muted small">{b.customer_name ?? (b.customer_id ? b.customer_id : 'all customers')}</div>
                  </div>
                  <ProgressBar value={b.actual} max={b.amount} thresholds={b.thresholds.map((t) => t.pct)} markers={b.forecast !== null && b.forecast !== undefined ? [{ label: 'forecast', value: b.forecast }] : []} format={(v) => money(v, true)} />
                  <Badge status={b.status} />
                </div>
              ))}
            </div>
          )}
        </div>
        <div className="card">
          <div className="card-head">
            <h2>Anomalies, last 7 days</h2>
            <Link to={pageHref(lens, 'anomalies')} className="hint">
              all
            </Link>
          </div>
          {s.anomalies.length === 0 ? (
            <div className="empty">
              <b>No anomalies</b>
              Daily cost stayed within 3σ of its 14-day baseline for every service.
            </div>
          ) : (
            <table>
              <thead>
                <tr>
                  <th>Day</th>
                  <th>Where</th>
                  <th className="num">Expected</th>
                  <th className="num">Actual</th>
                  <th className="num">Impact</th>
                </tr>
              </thead>
              <tbody>
                {s.anomalies.map((a, i) => (
                  <tr key={i} className="clickable" onClick={() => nav(pageHref(lens, 'anomalies', `day=${a.day}`))}>
                    <td className="nowrap">{day(a.day)}</td>
                    <td>
                      {a.label}
                      {wide ? <span className="sub">{a.customer_name}</span> : null}
                    </td>
                    <td className="num">{money(a.expected)}</td>
                    <td className="num">{money(a.actual)}</td>
                    <td className="num bad">+{money(a.impact)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      </div>

      {wide ? (
        <div className="card">
          <div className="card-head">
            <h2>Recent statements</h2>
            <span className="hint">
              {k.draftStatements} draft · {k.issuedStatements} issued · <Link to="/statements">all</Link>
            </span>
          </div>
          <StatementsMini s={s} route="" />
        </div>
      ) : null}
      <p className="muted tiny">
        Month-to-date change {formatPct(k.momDeltaPct, { sign: true })} compares the same number of days of last month. Costs are list-price rates from each customer's price book; discounts apply on statements.
      </p>
    </div>
  )
}

function StatementsMini({ s, route }: { s: Summary; route: string }) {
  const rows = s.statements?.latest ?? []
  if (rows.length === 0) return <div className="empty">No statements yet. Run a period from Statements.</div>
  return (
    <table>
      <thead>
        <tr>
          {route === '' ? <th>Customer</th> : null}
          <th>Period</th>
          <th>Status</th>
          <th className="num">Total</th>
        </tr>
      </thead>
      <tbody>
        {rows.map((st) => (
          <tr key={st.id}>
            {route === '' ? <td>{st.customer_name ?? st.customer_id}</td> : null}
            <td>
              <Link to={`/statements/${st.id}`}>
                {day(st.period_start)} → {day(st.period_end)}
              </Link>
            </td>
            <td>
              <Badge status={st.status} />
            </td>
            <td className="num">{formatMoney(Number(st.total), st.currency)}</td>
          </tr>
        ))}
      </tbody>
    </table>
  )
}
