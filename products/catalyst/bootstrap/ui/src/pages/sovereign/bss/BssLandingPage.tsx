/**
 * BssLandingPage — native /bss landing.
 *
 * Wave 6 PR 1 (2026-05-17 founder UX review): replaces Family F's
 * iframe BssLayout root with a NATIVE React surface that uses the same
 * PortalShell chrome + design tokens as Dashboard / Apps / Jobs /
 * Settings. Per the founder ruling, sub-agent UI work must inherit the
 * "big picture" — no bespoke chrome, no hex colours, no new card
 * components.
 *
 * Layout (PortalShell body):
 *   • KPI strip — 4 cards (Billing MRR / Orders pending / Vouchers
 *     active / Organizations active) using the SettingsPage SectionCard
 *     chrome (var(--color-bg-2) on rounded border, h2 + tagline).
 *   • Revenue card — 30-day revenue + inline SVG sparkline, full
 *     width below the KPI strip.
 *   • Section nav grid — 5 cards (Billing / Orders / Revenue /
 *     Vouchers / Organizations) linking to /bss/<section>, mirroring the
 *     AppsPage `.apps-grid` minmax(360px, 1fr) auto-fit pattern.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #1 (waterfall — first paint is the
 * full surface), every card renders from mount; the KPI values flip
 * from "—" placeholders to live numbers as `getBssOverview()` resolves.
 * Per #4 (never hardcode), the section catalogue is derived from
 * `BSS_TABS` (re-exported from the soon-to-be-deleted BssLayout was
 * the prior source-of-truth; the catalogue is now inlined here).
 */

import { useMemo } from 'react'
import { Link } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'
import { PortalShell } from '../PortalShell'
import { useDeploymentEvents } from '../useDeploymentEvents'
import { getBssOverview, type BssOverview } from '@/lib/bss.api'

/* ── Section catalogue ──────────────────────────────────────────────
 *
 * Drives both the section-nav grid below the KPI strip and (when a
 * per-section page wants to render it) the sub-nav crumbs. Single
 * source of truth so the landing and per-section pages cannot drift.
 */

export interface BssSection {
  id: 'billing' | 'orders' | 'revenue' | 'vouchers' | 'tenants'
  label: string
  description: string
  to: string
}

export const BSS_SECTIONS: readonly BssSection[] = [
  {
    id: 'billing',
    label: 'Billing',
    description: 'Subscription billing, invoices, payment history.',
    to: '/bss/billing',
  },
  {
    id: 'orders',
    label: 'Orders',
    description: 'New organization orders, provisioning queue, fulfilment.',
    to: '/bss/orders',
  },
  {
    id: 'revenue',
    label: 'Revenue',
    description: 'Period revenue, growth trend, CSV export.',
    to: '/bss/revenue',
  },
  {
    id: 'vouchers',
    label: 'Vouchers',
    description: 'Issue, list, revoke trial / discount vouchers.',
    to: '/bss/vouchers',
  },
  {
    id: 'tenants',
    label: 'Organizations',
    description: 'Active organizations and lifecycle controls.',
    to: '/bss/tenants',
  },
] as const

const QUERY_STALE_MS = 30_000

export interface BssLandingPageProps {
  /** Test seam — disables the live SSE attach. */
  disableStream?: boolean
  /** Test seam — bypass the React Query fetcher with synthetic data. */
  initialOverviewOverride?: BssOverview
}

export function BssLandingPage({
  disableStream = false,
  initialOverviewOverride,
}: BssLandingPageProps = {}) {
  const { deploymentId: resolvedId } = useResolvedDeploymentId()
  const deploymentId = resolvedId ?? ''

  const { snapshot } = useDeploymentEvents({
    deploymentId,
    applicationIds: [],
    disableStream,
  })
  const sovereignFQDN =
    snapshot?.sovereignFQDN ?? snapshot?.result?.sovereignFQDN ?? null

  const query = useQuery<BssOverview>({
    queryKey: ['bss-overview', deploymentId],
    queryFn: getBssOverview,
    staleTime: QUERY_STALE_MS,
    enabled: !initialOverviewOverride,
    placeholderData: (prev) => prev,
  })

  const overview = initialOverviewOverride ?? query.data ?? null
  const pendingApi = overview?.pendingApi ?? true
  const loaded = overview !== null

  return (
    <PortalShell
      deploymentId={deploymentId}
      sovereignFQDN={sovereignFQDN}
      pageTitle="BSS — Business Support Systems"
    >
      <div className="mx-auto max-w-7xl" data-testid="bss-landing-page">
        {/* KPI strip — 4 headline metrics */}
        <section
          aria-label="BSS key metrics"
          data-testid="bss-kpi-strip"
          className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4"
        >
          <KpiCard
            id="mrr"
            title="Monthly recurring revenue"
            value={loaded ? formatCents(overview!.billing.mrrCents) : '—'}
            delta={loaded ? overview!.billing.deltaPct : null}
            pendingApi={pendingApi}
          />
          <KpiCard
            id="orders"
            title="Orders pending"
            value={loaded ? String(overview!.orders.pending) : '—'}
            footer={
              loaded && overview!.orders.oldestDays !== null
                ? `Oldest: ${overview!.orders.oldestDays}d`
                : 'Queue empty'
            }
            pendingApi={pendingApi}
          />
          <KpiCard
            id="vouchers"
            title="Vouchers active"
            value={loaded ? String(overview!.vouchers.active) : '—'}
            footer={
              loaded && overview!.vouchers.redeemRate !== null
                ? `Redeem rate: ${overview!.vouchers.redeemRate.toFixed(0)}%`
                : 'No issuance yet'
            }
            pendingApi={pendingApi}
          />
          <KpiCard
            id="tenants"
            title="Organizations active"
            value={loaded ? String(overview!.tenants.active) : '—'}
            footer={
              loaded
                ? `+${overview!.tenants.newThisWeek} this week`
                : undefined
            }
            pendingApi={pendingApi}
          />
        </section>

        {/* Revenue card — 30-day spend + inline sparkline */}
        <section
          aria-label="BSS revenue trend"
          data-testid="bss-revenue-card"
          data-pending-api={pendingApi ? 'true' : undefined}
          className="mt-6 rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-2)] p-5"
        >
          <header className="mb-4 flex items-start justify-between gap-3">
            <div>
              <h2
                data-testid="bss-revenue-title"
                className="text-base font-semibold text-[var(--color-text-strong)]"
              >
                Revenue — last 30 days
              </h2>
              <p className="mt-0.5 text-xs text-[var(--color-text-dim)]">
                Trailing 30-day revenue from active subscriptions and one-shot
                orders.
              </p>
            </div>
            {pendingApi ? <ApiPendingPill testId="bss-revenue-pending-api" /> : null}
          </header>
          <div className="flex flex-col gap-4 sm:flex-row sm:items-end sm:justify-between">
            <div className="flex flex-col">
              <span
                className="text-3xl font-semibold tabular-nums text-[var(--color-text-strong)]"
                data-testid="bss-revenue-amount"
              >
                {loaded ? formatCents(overview!.revenue.last30dCents) : '—'}
              </span>
              <DeltaChip
                deltaPct={loaded ? overview!.revenue.deltaPct : null}
                testId="bss-revenue-delta"
              />
            </div>
            <Sparkline
              points={loaded ? overview!.revenue.sparkline : []}
              testId="bss-revenue-sparkline"
            />
          </div>
        </section>

        {/* Section nav grid — links into the per-section pages */}
        <section
          aria-label="BSS sections"
          data-testid="bss-section-grid"
          className="mt-6 grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3"
        >
          {BSS_SECTIONS.map((s) => (
            <Link
              key={s.id}
              to={s.to as never}
              data-testid={`bss-section-link-${s.id}`}
              className="group flex flex-col gap-2 rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-2)] p-5 no-underline transition-colors hover:border-[var(--color-accent)] hover:bg-[var(--color-surface-hover)]"
            >
              <span className="text-base font-semibold text-[var(--color-text-strong)] group-hover:text-[var(--color-accent)]">
                {s.label}
              </span>
              <span className="text-xs text-[var(--color-text-dim)]">
                {s.description}
              </span>
              <span className="mt-2 text-xs text-[var(--color-text-dim)] group-hover:text-[var(--color-accent)]">
                Open {s.label.toLowerCase()} →
              </span>
            </Link>
          ))}
        </section>
      </div>
    </PortalShell>
  )
}

/* ── Presentation primitives ─────────────────────────────────────────
 *
 * Card chrome mirrors SettingsPage's SectionCard: rounded-xl,
 * var(--color-bg-2) on var(--color-border), 5-unit padding, h2 +
 * tagline header. KPI cards use a tighter footer line for the
 * delta / context label.
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
      data-testid={`bss-kpi-${id}`}
      data-pending-api={pendingApi ? 'true' : undefined}
      className="flex flex-col gap-2 rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-2)] p-5"
    >
      <header className="flex items-start justify-between gap-2">
        <h2
          className="text-xs font-medium uppercase tracking-wide text-[var(--color-text-dim)]"
          data-testid={`bss-kpi-${id}-title`}
        >
          {title}
        </h2>
        {pendingApi ? <ApiPendingPill testId={`bss-kpi-${id}-pending-api`} /> : null}
      </header>
      <div
        className="text-2xl font-semibold tabular-nums text-[var(--color-text-strong)]"
        data-testid={`bss-kpi-${id}-value`}
      >
        {value}
      </div>
      {delta !== undefined ? (
        <DeltaChip deltaPct={delta} testId={`bss-kpi-${id}-delta`} />
      ) : null}
      {footer ? (
        <p
          className="text-xs text-[var(--color-text-dim)]"
          data-testid={`bss-kpi-${id}-footer`}
        >
          {footer}
        </p>
      ) : null}
    </article>
  )
}

function ApiPendingPill({ testId }: { testId: string }) {
  // Tone mirrors SettingsPage's pending-api pill verbatim so the BSS
  // landing reads as a sibling of /settings.
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
  // Use the same token-driven success / danger semantics SettingsPage's
  // tone classes consume so a positive delta picks up the accent and a
  // negative delta picks up the rose family used by the Danger zone.
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

interface SparklineProps {
  points: number[]
  testId: string
}

const SPARK_W = 220
const SPARK_H = 48

function Sparkline({ points, testId }: SparklineProps) {
  const path = useMemo(() => buildSparkPath(points, SPARK_W, SPARK_H), [points])
  if (path === null) {
    return (
      <div
        data-testid={testId}
        data-empty="true"
        className="flex h-12 w-[220px] items-center justify-center rounded-md border border-dashed border-[var(--color-border)] text-[10px] uppercase tracking-wide text-[var(--color-text-dimmer)]"
      >
        No data
      </div>
    )
  }
  return (
    <svg
      data-testid={testId}
      role="img"
      aria-label="30-day revenue sparkline"
      width={SPARK_W}
      height={SPARK_H}
      viewBox={`0 0 ${SPARK_W} ${SPARK_H}`}
      className="overflow-visible"
    >
      <path
        d={path}
        fill="none"
        stroke="var(--color-accent)"
        strokeWidth={1.5}
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  )
}

function buildSparkPath(points: number[], w: number, h: number): string | null {
  if (points.length === 0) return null
  if (points.length === 1) {
    const y = h / 2
    return `M0 ${y} L${w} ${y}`
  }
  const min = Math.min(...points)
  const max = Math.max(...points)
  const range = max - min || 1
  const stepX = w / (points.length - 1)
  let d = ''
  for (let i = 0; i < points.length; i += 1) {
    const x = i * stepX
    const y = h - ((points[i]! - min) / range) * h
    d += `${i === 0 ? 'M' : 'L'}${x.toFixed(2)} ${y.toFixed(2)} `
  }
  return d.trim()
}

/** formatCents — render a cents integer as a localized currency string. */
function formatCents(cents: number): string {
  const dollars = cents / 100
  return new Intl.NumberFormat(undefined, {
    style: 'currency',
    currency: 'USD',
    maximumFractionDigits: 0,
  }).format(dollars)
}
