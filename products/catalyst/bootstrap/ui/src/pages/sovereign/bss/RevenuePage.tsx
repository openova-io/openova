/**
 * RevenuePage — native /billing/revenue (issue #4196).
 *
 * Wave 6 PR 4 (2026-05-17 founder UX review): drops the BssSectionShell
 * iframe wrapper from PR 1 (#1606) and renders a NATIVE React surface
 * that inherits the same PortalShell chrome + design tokens as Dashboard
 * / Apps / Jobs / Settings / BssLandingPage. Per the founder ruling,
 * sub-agent UI work must inherit the "big picture" — no bespoke chrome,
 * no hex colours, no new card components.
 *
 * Layout (PortalShell body):
 *   • Back-to-overview crumb (headerSlotLeft, matches BssSectionShell).
 *   • KPI strip — 4 cards (Last 30d revenue / MoM delta / Top organization /
 *     Top plan) using the same KpiCard chrome as BssLandingPage.
 *   • Revenue trend — full-width line chart of daily revenue for the
 *     trailing 30 days. Uses Recharts (already a top-level dep, see
 *     widgets/cloud-list/MetricsPanel.tsx + widgets/compliance/
 *     ComplianceTreemap.tsx) so we don't introduce a new chart lib.
 *   • Breakdown table — Plan | Organizations | MRR | YoY %, sortable by any
 *     column. Mirrors the ParentDomainsPage table chrome (border-
 *     collapse, var(--color-border) row dividers, uppercase dimmed
 *     header cells).
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #1 (waterfall — first paint is the
 * full surface), every card / chart / table renders from mount; values
 * flip from "—" placeholders to live numbers as getRevenue() resolves.
 * Per #4 (never hardcode), every column + chart datum comes from the
 * typed BssRevenue payload, never an inline literal.
 */

import { useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import {
  Area,
  AreaChart,
  CartesianGrid,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from 'recharts'

import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'
import { PortalShell } from '../PortalShell'
import { BillingSectionNav } from './BillingSectionNav'
import { useDeploymentEvents } from '../useDeploymentEvents'
import { getRevenue, type BssRevenue, type PlanBreakdown } from '@/lib/bss.api'

const QUERY_STALE_MS = 30_000

/** Chart geometry. Height fixed so the surface matches the KPI strip
 *  card heights on stacked viewports; width is ResponsiveContainer-fed.
 *  Stroke / fill colours come from CSS tokens at render time. */
const CHART_HEIGHT_PX = 220

/** Column ids for the breakdown table. Used as the sortable-column key
 *  + the test seam attribute on the th. */
type SortKey = 'plan' | 'orgs' | 'mrr' | 'yoy'
type SortDir = 'asc' | 'desc'

export interface RevenuePageProps {
  /** Test seam — disables the live SSE attach. */
  disableStream?: boolean
  /** Test seam — bypass the React Query fetcher with synthetic data. */
  initialRevenueOverride?: BssRevenue
}

export function RevenuePage({
  disableStream = false,
  initialRevenueOverride,
}: RevenuePageProps = {}) {
  const { deploymentId: resolvedId } = useResolvedDeploymentId()
  const deploymentId = resolvedId ?? ''

  const { snapshot } = useDeploymentEvents({
    deploymentId,
    applicationIds: [],
    disableStream,
  })
  const sovereignFQDN =
    snapshot?.sovereignFQDN ?? snapshot?.result?.sovereignFQDN ?? null

  const query = useQuery<BssRevenue>({
    queryKey: ['bss-revenue', deploymentId],
    queryFn: getRevenue,
    staleTime: QUERY_STALE_MS,
    enabled: !initialRevenueOverride,
    placeholderData: (prev) => prev,
  })

  const revenue = initialRevenueOverride ?? query.data ?? null
  const pendingApi = revenue?.pendingApi ?? true
  const loaded = revenue !== null

  return (
    <PortalShell
      deploymentId={deploymentId}
      sovereignFQDN={sovereignFQDN}
      pageTitle="Billing — Revenue"
      headerSlotLeft={<BillingSectionNav />}
    >
      <div className="mx-auto max-w-7xl" data-testid="bss-revenue-page">
        {/* KPI strip — 4 headline metrics */}
        <section
          aria-label="Revenue key metrics"
          data-testid="bss-revenue-kpi-strip"
          className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4"
        >
          <KpiCard
            id="last30d"
            title="Revenue — last 30 days"
            value={loaded ? formatCents(revenue!.kpi.last30dCents) : '—'}
            pendingApi={pendingApi}
          />
          <KpiCard
            id="mom"
            title="Month-over-month"
            value={loaded ? formatDeltaPct(revenue!.kpi.momPct) : '—'}
            delta={loaded ? revenue!.kpi.momPct : null}
            pendingApi={pendingApi}
          />
          <KpiCard
            id="top-org"
            title="Top organization"
            value={loaded && revenue!.kpi.topOrg ? revenue!.kpi.topOrg : '—'}
            footer={loaded && !revenue!.kpi.topOrg ? 'No organizations yet' : undefined}
            pendingApi={pendingApi}
          />
          <KpiCard
            id="top-plan"
            title="Top plan"
            value={loaded && revenue!.kpi.topPlan ? revenue!.kpi.topPlan : '—'}
            footer={loaded && !revenue!.kpi.topPlan ? 'No plans active' : undefined}
            pendingApi={pendingApi}
          />
        </section>

        {/* Revenue trend chart — full width below KPI strip */}
        <section
          aria-label="Revenue trend"
          data-testid="bss-revenue-trend-card"
          data-pending-api={pendingApi ? 'true' : undefined}
          className="mt-6 rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-2)] p-5"
        >
          <header className="mb-4 flex items-start justify-between gap-3">
            <div>
              <h2
                data-testid="bss-revenue-trend-title"
                className="text-base font-semibold text-[var(--color-text-strong)]"
              >
                Revenue — daily, last 30 days
              </h2>
              <p className="mt-0.5 text-xs text-[var(--color-text-dim)]">
                Trailing 30-day revenue from active subscriptions and
                one-shot orders.
              </p>
            </div>
            {pendingApi ? (
              <ApiPendingPill testId="bss-revenue-trend-pending-api" />
            ) : null}
          </header>
          <RevenueTrendChart points={loaded ? revenue!.sparkline : []} />
        </section>

        {/* Plan breakdown table */}
        <section
          aria-label="Revenue breakdown by plan"
          data-testid="bss-revenue-breakdown-card"
          data-pending-api={pendingApi ? 'true' : undefined}
          className="mt-6 rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-2)] p-5"
        >
          <header className="mb-4 flex items-start justify-between gap-3">
            <div>
              <h2
                data-testid="bss-revenue-breakdown-title"
                className="text-base font-semibold text-[var(--color-text-strong)]"
              >
                Breakdown by plan
              </h2>
              <p className="mt-0.5 text-xs text-[var(--color-text-dim)]">
                MRR contribution, active organization count, and
                year-over-year delta per subscription plan. Click any
                column header to sort.
              </p>
            </div>
            {pendingApi ? (
              <ApiPendingPill testId="bss-revenue-breakdown-pending-api" />
            ) : null}
          </header>
          <BreakdownTable
            rows={loaded ? revenue!.breakdown : []}
            loaded={loaded}
          />
        </section>
      </div>
    </PortalShell>
  )
}

/* ── Presentation primitives ────────────────────────────────────────
 *
 * KpiCard / ApiPendingPill / DeltaChip chrome mirrors BssLandingPage
 * verbatim so the Revenue page reads as a sibling of the BSS landing.
 * If the landing's KpiCard ever evolves, the cards here should follow.
 */

interface KpiCardProps {
  id: string
  title: string
  value: string
  delta?: number | null
  footer?: string
  pendingApi?: boolean
}

function KpiCard({ id, title, value, delta, footer, pendingApi }: KpiCardProps) {
  return (
    <article
      data-testid={`bss-revenue-kpi-${id}`}
      data-pending-api={pendingApi ? 'true' : undefined}
      className="flex flex-col gap-2 rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-2)] p-5"
    >
      <header className="flex items-start justify-between gap-2">
        <h2
          className="text-xs font-medium uppercase tracking-wide text-[var(--color-text-dim)]"
          data-testid={`bss-revenue-kpi-${id}-title`}
        >
          {title}
        </h2>
        {pendingApi ? (
          <ApiPendingPill testId={`bss-revenue-kpi-${id}-pending-api`} />
        ) : null}
      </header>
      <div
        className="truncate text-2xl font-semibold tabular-nums text-[var(--color-text-strong)]"
        data-testid={`bss-revenue-kpi-${id}-value`}
        title={value}
      >
        {value}
      </div>
      {delta !== undefined ? (
        <DeltaChip deltaPct={delta} testId={`bss-revenue-kpi-${id}-delta`} />
      ) : null}
      {footer ? (
        <p
          className="text-xs text-[var(--color-text-dim)]"
          data-testid={`bss-revenue-kpi-${id}-footer`}
        >
          {footer}
        </p>
      ) : null}
    </article>
  )
}

function ApiPendingPill({ testId }: { testId: string }) {
  // Tone mirrors SettingsPage's pending-api pill verbatim so the BSS
  // revenue page reads as a sibling of /settings + the BSS landing.
  return (
    <span
      data-testid={testId}
      className="rounded-full border border-amber-500/40 bg-amber-500/10 px-2 py-0.5 text-[10px] font-medium uppercase tracking-wide text-amber-300"
      title="Backend API not yet wired — display only"
    >
      API pending
    </span>
  )
}

function DeltaChip({
  deltaPct,
  testId,
}: {
  deltaPct: number | null | undefined
  testId: string
}) {
  if (deltaPct === null || deltaPct === undefined) {
    return (
      <span
        data-testid={testId}
        className="text-xs text-[var(--color-text-dimmer)]"
      >
        —
      </span>
    )
  }
  const positive = deltaPct >= 0
  const sign = positive ? '+' : ''
  // Same token-driven success / danger semantics SettingsPage's tone
  // classes use — positive picks the accent, negative picks rose-300
  // (matches SettingsPage DangerRow rose-* exception).
  const cls = positive
    ? 'text-[var(--color-accent)]'
    : 'text-rose-300'
  return (
    <span
      data-testid={testId}
      className={`text-xs font-medium tabular-nums ${cls}`}
    >
      {sign}
      {deltaPct.toFixed(1)}%
    </span>
  )
}

/* ── Revenue trend chart ────────────────────────────────────────────
 *
 * Recharts AreaChart fed by the sparkline array. Stroke / fill /
 * tooltip surfaces use CSS tokens so the chart picks up theme changes
 * without a re-render. Empty arrays render a "No revenue yet" empty
 * state with the same dashed-border chrome as the BssLandingPage
 * sparkline empty state.
 */

interface RevenueTrendChartProps {
  points: number[]
}

interface ChartPoint {
  x: number
  /** dayLabel — relative day offset from today (e.g. "D-29" → today).
   *  Used as the XAxis label without pulling in dayjs. */
  dayLabel: string
  /** Baisa → OMR major-unit value plotted on the Y axis; Recharts can't
   *  format mid-tip so the axis/tooltip formatters re-derive baisa. */
  dollars: number
  /** Original baisa, retained for the tooltip formatter. */
  cents: number
}

function RevenueTrendChart({ points }: RevenueTrendChartProps) {
  const chartData = useMemo<ChartPoint[]>(() => {
    if (points.length === 0) return []
    const lastIdx = points.length - 1
    return points.map((cents, i) => ({
      x: i,
      // D-29 is the oldest, D-0 is today. Reads naturally for operators
      // scanning the X axis.
      dayLabel: `D-${lastIdx - i}`,
      // Plot the OMR major-unit value (baisa / 1000) so the Y axis reads
      // in whole OMR; the formatters below re-scale to baisa.
      dollars: cents / BAISA_PER_OMR,
      cents,
    }))
  }, [points])

  if (chartData.length === 0) {
    return (
      <div
        data-testid="bss-revenue-trend-empty"
        data-empty="true"
        className="flex items-center justify-center rounded-md border border-dashed border-[var(--color-border)] text-xs uppercase tracking-wide text-[var(--color-text-dimmer)]"
        style={{ height: CHART_HEIGHT_PX }}
      >
        No revenue yet
      </div>
    )
  }

  return (
    <div
      data-testid="bss-revenue-trend-chart"
      style={{ height: CHART_HEIGHT_PX }}
    >
      <ResponsiveContainer width="100%" height="100%">
        <AreaChart
          data={chartData}
          margin={{ top: 8, right: 16, left: 8, bottom: 0 }}
        >
          <CartesianGrid
            stroke="var(--color-border)"
            strokeDasharray="3 3"
            vertical={false}
          />
          <XAxis
            dataKey="dayLabel"
            stroke="var(--color-text-dim)"
            tick={{ fill: 'var(--color-text-dim)', fontSize: 11 }}
            tickLine={false}
            axisLine={{ stroke: 'var(--color-border)' }}
            interval="preserveStartEnd"
            minTickGap={24}
          />
          <YAxis
            stroke="var(--color-text-dim)"
            tick={{ fill: 'var(--color-text-dim)', fontSize: 11 }}
            tickLine={false}
            axisLine={{ stroke: 'var(--color-border)' }}
            tickFormatter={(v) => formatCompactCents(Number(v) * BAISA_PER_OMR)}
            width={56}
          />
          <Tooltip
            contentStyle={{
              background: 'var(--color-bg-2)',
              border: '1px solid var(--color-border)',
              borderRadius: 6,
              color: 'var(--color-text)',
              fontSize: 12,
            }}
            labelStyle={{ color: 'var(--color-text-dim)' }}
            formatter={(value) => [formatCents(Number(value) * BAISA_PER_OMR), 'Revenue']}
            labelFormatter={(label) => String(label ?? '')}
            cursor={{ stroke: 'var(--color-border-strong)', strokeWidth: 1 }}
          />
          <Area
            type="monotone"
            dataKey="dollars"
            stroke="var(--color-accent)"
            strokeWidth={1.5}
            fill="var(--color-accent)"
            fillOpacity={0.15}
            isAnimationActive={false}
          />
        </AreaChart>
      </ResponsiveContainer>
    </div>
  )
}

/* ── Breakdown table ────────────────────────────────────────────────
 *
 * Sortable table. Default sort = MRR DESC so the highest-grossing plan
 * floats to the top, mirroring the GitHub Billing dashboard's plan
 * table. Click a column header to toggle direction; clicking a
 * different header switches the active sort key + resets to the
 * column-appropriate direction (string asc / numeric desc).
 */

interface BreakdownTableProps {
  rows: readonly PlanBreakdown[]
  loaded: boolean
}

function BreakdownTable({ rows, loaded }: BreakdownTableProps) {
  const [sortKey, setSortKey] = useState<SortKey>('mrr')
  const [sortDir, setSortDir] = useState<SortDir>('desc')

  const sortedRows = useMemo<PlanBreakdown[]>(() => {
    const arr = [...rows]
    arr.sort((a, b) => {
      const cmp = compareRows(a, b, sortKey)
      return sortDir === 'asc' ? cmp : -cmp
    })
    return arr
  }, [rows, sortKey, sortDir])

  function onHeaderClick(next: SortKey) {
    if (next === sortKey) {
      setSortDir((d) => (d === 'asc' ? 'desc' : 'asc'))
      return
    }
    setSortKey(next)
    // String columns default to asc (A-Z reads naturally); numeric
    // columns default to desc (highest first).
    setSortDir(next === 'plan' ? 'asc' : 'desc')
  }

  if (!loaded) {
    return (
      <div
        data-testid="bss-revenue-breakdown-loading"
        className="text-sm text-[var(--color-text-dim)]"
      >
        Loading…
      </div>
    )
  }

  if (sortedRows.length === 0) {
    return (
      <div
        data-testid="bss-revenue-breakdown-empty"
        className="rounded-md border border-[var(--color-border)] px-4 py-8 text-center text-sm text-[var(--color-text-dim)]"
      >
        No plan revenue yet. Once organizations subscribe to a plan, MRR
        contribution per plan will appear here.
      </div>
    )
  }

  return (
    <table
      data-testid="bss-revenue-breakdown-table"
      className="w-full border-collapse text-sm"
    >
      <thead>
        <tr className="border-b border-[var(--color-border)] text-left text-xs uppercase text-[var(--color-text-dim)]">
          <SortableTh
            label="Plan"
            col="plan"
            activeKey={sortKey}
            activeDir={sortDir}
            onClick={onHeaderClick}
            className="py-2 pr-3"
          />
          <SortableTh
            label="Organizations"
            col="orgs"
            activeKey={sortKey}
            activeDir={sortDir}
            onClick={onHeaderClick}
            className="py-2 pr-3 text-right"
          />
          <SortableTh
            label="MRR"
            col="mrr"
            activeKey={sortKey}
            activeDir={sortDir}
            onClick={onHeaderClick}
            className="py-2 pr-3 text-right"
          />
          <SortableTh
            label="YoY"
            col="yoy"
            activeKey={sortKey}
            activeDir={sortDir}
            onClick={onHeaderClick}
            className="py-2 pr-3 text-right"
          />
        </tr>
      </thead>
      <tbody>
        {sortedRows.map((row) => (
          <tr
            key={row.id}
            data-testid={`bss-revenue-breakdown-row-${row.id}`}
            className="border-b border-[var(--color-border)] hover:bg-[var(--color-surface-hover)]"
          >
            <td
              className="py-2 pr-3 text-[var(--color-text)]"
              data-testid={`bss-revenue-breakdown-plan-${row.id}`}
            >
              {row.plan}
            </td>
            <td
              className="py-2 pr-3 text-right tabular-nums text-[var(--color-text)]"
              data-testid={`bss-revenue-breakdown-orgs-${row.id}`}
            >
              {row.orgs}
            </td>
            <td
              className="py-2 pr-3 text-right tabular-nums text-[var(--color-text-strong)]"
              data-testid={`bss-revenue-breakdown-mrr-${row.id}`}
            >
              {formatCents(row.mrrCents)}
            </td>
            <td
              className="py-2 pr-3 text-right tabular-nums"
              data-testid={`bss-revenue-breakdown-yoy-${row.id}`}
            >
              <DeltaChip
                deltaPct={row.yoyPct}
                testId={`bss-revenue-breakdown-yoy-chip-${row.id}`}
              />
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  )
}

interface SortableThProps {
  label: string
  col: SortKey
  activeKey: SortKey
  activeDir: SortDir
  onClick: (col: SortKey) => void
  className?: string
}

function SortableTh({
  label,
  col,
  activeKey,
  activeDir,
  onClick,
  className,
}: SortableThProps) {
  const active = activeKey === col
  // Caret glyph mirrors the ParentDomainsPage row-toggle glyphs.
  const caret = active ? (activeDir === 'asc' ? '▲' : '▼') : ''
  return (
    <th
      className={className}
      data-testid={`bss-revenue-breakdown-th-${col}`}
      data-active={active ? 'true' : undefined}
      data-dir={active ? activeDir : undefined}
      aria-sort={
        active ? (activeDir === 'asc' ? 'ascending' : 'descending') : 'none'
      }
    >
      <button
        type="button"
        onClick={() => onClick(col)}
        className="inline-flex items-center gap-1 text-xs uppercase text-[var(--color-text-dim)] hover:text-[var(--color-text)]"
      >
        <span>{label}</span>
        <span aria-hidden className="text-[10px]">
          {caret || '·'}
        </span>
      </button>
    </th>
  )
}

/* ── Pure helpers ───────────────────────────────────────────────────
 *
 * Kept module-private (NOT exported) so react-refresh's
 * only-export-components rule stays happy. Unit tests can import
 * RevenuePage and re-derive against the same shapes; the helpers are
 * trivial enough that test-via-component covers them.
 */

/** compareRows — column-aware comparator. Numeric columns sort by
 *  numeric value (null → treated as min so null floats to the bottom
 *  on desc); string columns sort lexically with a stable id tiebreak. */
function compareRows(
  a: PlanBreakdown,
  b: PlanBreakdown,
  key: SortKey,
): number {
  switch (key) {
    case 'plan':
      return a.plan.localeCompare(b.plan) || a.id.localeCompare(b.id)
    case 'orgs':
      return a.orgs - b.orgs || a.id.localeCompare(b.id)
    case 'mrr':
      return a.mrrCents - b.mrrCents || a.id.localeCompare(b.id)
    case 'yoy': {
      const av = a.yoyPct ?? Number.NEGATIVE_INFINITY
      const bv = b.yoyPct ?? Number.NEGATIVE_INFINITY
      return av - bv || a.id.localeCompare(b.id)
    }
  }
}

/**
 * BAISA_PER_OMR — the Sovereign billing service is OMR-denominated and
 * emits amounts in baisa (1/1000 OMR), the smallest currency unit. The
 * BssRevenue minor-unit fields (mrrCents / last30dCents) carry baisa
 * verbatim from the catalyst-api bridge (#4274 — org_billing_revenue.go
 * maps store.Order.amount_baisa). The "cents" name is the generic FE
 * minor-unit label kept for type stability; the divisor + currency below
 * are what make it render as real OMR rather than 10×-inflated USD.
 */
const BAISA_PER_OMR = 1000

/** formatCents — render a baisa integer as a localised OMR string. */
function formatCents(cents: number): string {
  const omr = cents / BAISA_PER_OMR
  return new Intl.NumberFormat(undefined, {
    style: 'currency',
    currency: 'OMR',
    maximumFractionDigits: 0,
  }).format(omr)
}

/** formatCompactCents — Y-axis short form ("OMR 1.2K", "OMR 3M"). */
function formatCompactCents(cents: number): string {
  const omr = cents / BAISA_PER_OMR
  return new Intl.NumberFormat(undefined, {
    style: 'currency',
    currency: 'OMR',
    notation: 'compact',
    maximumFractionDigits: 1,
  }).format(omr)
}

/** formatDeltaPct — header KPI uses the raw signed-percentage string. */
function formatDeltaPct(pct: number | null): string {
  if (pct === null) return '—'
  const sign = pct >= 0 ? '+' : ''
  return `${sign}${pct.toFixed(1)}%`
}
