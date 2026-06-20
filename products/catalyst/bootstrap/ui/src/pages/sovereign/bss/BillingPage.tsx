/**
 * BillingPage — /console/bss/billing (native React).
 *
 * Wave 6 PR 2 (2026-05-17): replaces the iframe-wrapped BssSectionShell
 * with a native PortalShell + subscription table that mirrors the
 * design system shared by JobsPage / AppsPage / ParentDomainsPage /
 * SettingsPage and the Wave 6 PR 3 OrdersPage sibling. The iframe is
 * gone — every byte of chrome reads from design tokens so /bss/billing
 * renders as a sibling of /jobs.
 *
 * Layout:
 *   • PortalShell header with "← Back to BSS overview" sub-affordance
 *     (mirrors BssSectionShell + OrdersPage).
 *   • Page header card: title + tagline + "API pending" pill when the
 *     subscriptions endpoint hasn't been wired yet.
 *   • Filter row: search + status dropdown + plan dropdown + "+ New plan"
 *     button (matches the JobsTable toolbar baseline + ParentDomainsPage
 *     CTA placement).
 *   • Table: Org name | Plan | MRR | Last paid | Status pill.
 *   • Empty + loading states match JobsPage / ParentDomainsPage / OrdersPage.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #1 the table chrome renders from
 * mount even while the fetcher resolves so the operator never sees a
 * blank surface. Per #4 every color is a CSS variable from globals.css
 * (the only exception is the amber-* / rose-* / emerald-* Tailwind
 * families, used verbatim as SettingsPage / OrdersPage do for status
 * pills — pendingApi+trial / overdue / paid respectively).
 */

import { useMemo, useState } from 'react'
import { Link } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'
import { PortalShell } from '../PortalShell'
import { useDeploymentEvents } from '../useDeploymentEvents'
import {
  getBillingSubscriptions,
  type Subscription,
  type SubscriptionStatus,
  type SubscriptionsResponse,
} from '@/lib/bss.api'

const QUERY_STALE_MS = 30_000

/* ── Filter catalogue ───────────────────────────────────────────────
 *
 * Single source of truth for the status dropdown. Adding a new status
 * is a one-line change here; the option list + predicate below pick it
 * up automatically (mirrors OrdersPage's STATUS_OPTIONS pattern).
 */

const STATUS_OPTIONS: readonly { value: SubscriptionStatus; label: string }[] = [
  { value: 'paid', label: 'Paid' },
  { value: 'trial', label: 'Trial' },
  { value: 'overdue', label: 'Overdue' },
  { value: 'cancelled', label: 'Cancelled' },
]

export interface BillingPageProps {
  /** Test seam — disables the live SSE attach. */
  disableStream?: boolean
  /** Test seam — bypass the React Query fetcher with synthetic data. */
  initialSubscriptionsOverride?: SubscriptionsResponse
}

export function BillingPage({
  disableStream = false,
  initialSubscriptionsOverride,
}: BillingPageProps = {}) {
  const { deploymentId: resolvedId } = useResolvedDeploymentId()
  const deploymentId = resolvedId ?? ''

  const { snapshot } = useDeploymentEvents({
    deploymentId,
    applicationIds: [],
    disableStream,
  })
  const sovereignFQDN =
    snapshot?.sovereignFQDN ?? snapshot?.result?.sovereignFQDN ?? null

  const query = useQuery<SubscriptionsResponse>({
    queryKey: ['bss-billing-subscriptions', deploymentId],
    queryFn: getBillingSubscriptions,
    staleTime: QUERY_STALE_MS,
    enabled: !initialSubscriptionsOverride,
    placeholderData: (prev) => prev,
  })

  const data = initialSubscriptionsOverride ?? query.data ?? null
  const subscriptions = data?.subscriptions ?? []
  const pendingApi = data?.pendingApi ?? true
  const loading = !initialSubscriptionsOverride && query.isLoading && data === null

  /* ── Toolbar state ──────────────────────────────────────────────── */

  const [search, setSearch] = useState<string>('')
  const [statusFilter, setStatusFilter] = useState<'' | SubscriptionStatus>('')
  const [planFilter, setPlanFilter] = useState<string>('')

  const planOptions = useMemo<string[]>(() => {
    const set = new Set<string>()
    for (const s of subscriptions) {
      if (s.plan) set.add(s.plan)
    }
    return [...set].sort((a, b) => a.localeCompare(b))
  }, [subscriptions])

  const visibleSubscriptions = useMemo<Subscription[]>(() => {
    const q = search.trim().toLowerCase()
    return subscriptions.filter((s) => {
      if (statusFilter && s.status !== statusFilter) return false
      if (planFilter && s.plan !== planFilter) return false
      if (!q) return true
      if (s.tenantOrg.toLowerCase().includes(q)) return true
      if (s.plan.toLowerCase().includes(q)) return true
      if (s.status.toLowerCase().includes(q)) return true
      return false
    })
  }, [subscriptions, search, statusFilter, planFilter])

  return (
    <PortalShell
      deploymentId={deploymentId}
      sovereignFQDN={sovereignFQDN}
      pageTitle="BSS — Billing"
      headerSlotLeft={
        <Link
          to={'/bss' as never}
          className="text-[11px] text-[var(--color-text-dim)] hover:text-[var(--color-text)] no-underline"
          data-testid="sov-bss-back-to-landing-billing"
        >
          ← Back to BSS overview
        </Link>
      }
    >
      <div className="mx-auto max-w-7xl" data-testid="bss-billing-page">
        {/* Page header card — mirrors BssLandingPage revenue card chrome. */}
        <section
          aria-label="BSS billing header"
          data-testid="bss-billing-header"
          data-pending-api={pendingApi ? 'true' : undefined}
          className="rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-2)] p-5"
        >
          <div className="flex items-start justify-between gap-3">
            <div>
              <h2
                data-testid="bss-billing-title"
                className="text-base font-semibold text-[var(--color-text-strong)]"
              >
                Subscriptions
              </h2>
              <p className="mt-0.5 text-xs text-[var(--color-text-dim)]">
                Active organization subscriptions, plan tier, monthly recurring
                revenue, and current payment status.
              </p>
            </div>
            {pendingApi ? (
              <ApiPendingPill testId="bss-billing-pending-api" />
            ) : null}
          </div>
        </section>

        {/* Toolbar — search + filters + primary CTA. */}
        <section
          aria-label="BSS billing filters"
          data-testid="bss-billing-toolbar"
          className="mt-4 flex flex-wrap items-end gap-3"
        >
          <div className="relative min-w-[240px] flex-1 sm:max-w-md">
            <SearchIcon className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-[var(--color-text-dim)]" />
            <input
              type="search"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              placeholder="Search subscriptions by org, plan, or status…"
              data-testid="bss-billing-search"
              aria-label="Search subscriptions"
              className="w-full rounded-md border border-[var(--color-border)] bg-[var(--color-surface)] py-1.5 pl-8 pr-3 text-sm text-[var(--color-text)] outline-none transition-colors focus:border-[var(--color-accent)]"
            />
          </div>

          <label className="flex flex-col gap-1">
            <span className="text-[10px] uppercase tracking-wide text-[var(--color-text-dim)]">
              Status
            </span>
            <select
              value={statusFilter}
              onChange={(e) =>
                setStatusFilter(e.target.value as '' | SubscriptionStatus)
              }
              data-testid="bss-billing-filter-status"
              aria-label="Filter by status"
              className="rounded-md border border-[var(--color-border)] bg-[var(--color-surface)] px-2 py-1.5 text-sm text-[var(--color-text)] outline-none focus:border-[var(--color-accent)]"
            >
              <option value="">All</option>
              {STATUS_OPTIONS.map((o) => (
                <option key={o.value} value={o.value}>
                  {o.label}
                </option>
              ))}
            </select>
          </label>

          <label className="flex flex-col gap-1">
            <span className="text-[10px] uppercase tracking-wide text-[var(--color-text-dim)]">
              Plan
            </span>
            <select
              value={planFilter}
              onChange={(e) => setPlanFilter(e.target.value)}
              data-testid="bss-billing-filter-plan"
              aria-label="Filter by plan"
              className="rounded-md border border-[var(--color-border)] bg-[var(--color-surface)] px-2 py-1.5 text-sm text-[var(--color-text)] outline-none focus:border-[var(--color-accent)]"
            >
              <option value="">All</option>
              {planOptions.map((p) => (
                <option key={p} value={p}>
                  {p}
                </option>
              ))}
            </select>
          </label>

          <span
            data-testid="bss-billing-result-count"
            aria-live="polite"
            className="ml-auto self-end pb-1 text-xs tabular-nums text-[var(--color-text-dim)]"
          >
            {visibleSubscriptions.length}/{subscriptions.length}
          </span>

          <button
            type="button"
            data-testid="bss-billing-new-plan"
            // New-plan creation isn't wired yet; the button renders
            // disabled when pendingApi is true so the operator gets a
            // visible "coming soon" affordance instead of a dead click.
            disabled={pendingApi}
            title={
              pendingApi
                ? 'Billing API not yet wired'
                : 'Create a new subscription plan'
            }
            className="self-end rounded-md bg-[var(--color-accent)] px-3 py-1.5 text-sm font-medium text-white transition-opacity hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50"
          >
            + New plan
          </button>
        </section>

        {/* Table — empty state mirrors ParentDomainsPage / JobsTable. */}
        <section
          aria-label="BSS billing subscriptions"
          className="mt-4 overflow-x-auto rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)]"
        >
          {loading ? (
            <div
              data-testid="bss-billing-loading"
              className="px-4 py-8 text-center text-sm text-[var(--color-text-dim)]"
            >
              Loading…
            </div>
          ) : visibleSubscriptions.length === 0 ? (
            <div
              data-testid="bss-billing-empty"
              className="px-4 py-12 text-center text-sm text-[var(--color-text-dim)]"
            >
              {subscriptions.length === 0
                ? 'No organization subscriptions yet. New organizations appear here once they purchase a plan from the marketplace.'
                : 'No subscriptions match the current filters.'}
            </div>
          ) : (
            <table
              data-testid="bss-billing-table"
              className="w-full border-collapse text-sm"
            >
              <thead>
                <tr className="border-b border-[var(--color-border)] text-left text-[11px] uppercase tracking-wide text-[var(--color-text-dim)]">
                  <th className="px-4 py-2 font-semibold">Org</th>
                  <th className="px-4 py-2 font-semibold">Plan</th>
                  <th className="px-4 py-2 text-right font-semibold">MRR</th>
                  <th className="px-4 py-2 font-semibold">Last paid</th>
                  <th className="px-4 py-2 font-semibold">Status</th>
                </tr>
              </thead>
              <tbody>
                {visibleSubscriptions.map((sub) => (
                  <SubscriptionRow key={sub.id} sub={sub} />
                ))}
              </tbody>
            </table>
          )}
        </section>
      </div>
    </PortalShell>
  )
}

/* ── Row + presentation primitives ───────────────────────────────── */

function SubscriptionRow({ sub }: { sub: Subscription }) {
  return (
    <tr
      data-testid={`bss-billing-row-${sub.id}`}
      data-status={sub.status}
      className="border-b border-[var(--color-border)] transition-colors last:border-b-0 hover:bg-[var(--color-surface-hover)]"
    >
      <td className="px-4 py-2.5 text-[var(--color-text-strong)]">
        {sub.tenantOrg ? (
          <span data-testid={`bss-billing-org-${sub.id}`}>{sub.tenantOrg}</span>
        ) : (
          <span className="text-[var(--color-text-dimmer)]">—</span>
        )}
      </td>
      <td className="px-4 py-2.5 text-[var(--color-text)]">
        {sub.plan ? (
          <span data-testid={`bss-billing-plan-${sub.id}`}>{sub.plan}</span>
        ) : (
          <span className="text-[var(--color-text-dimmer)]">—</span>
        )}
      </td>
      <td
        className="px-4 py-2.5 text-right tabular-nums text-[var(--color-text)]"
        data-testid={`bss-billing-mrr-${sub.id}`}
      >
        {formatCents(sub.mrrCents, sub.currency)}
      </td>
      <td
        className="px-4 py-2.5 text-[var(--color-text-dim)]"
        data-testid={`bss-billing-lastpaid-${sub.id}`}
      >
        {formatLastPaid(sub.lastPaidAt)}
      </td>
      <td className="px-4 py-2.5">
        <StatusPill status={sub.status} testId={`bss-billing-status-${sub.id}`} />
      </td>
    </tr>
  )
}

function StatusPill({
  status,
  testId,
}: {
  status: SubscriptionStatus
  testId: string
}) {
  // Pill tones mirror ParentDomainsPage's FlipStatusBadge + OrdersPage's
  // status pill family:
  //   • emerald = healthy / paid
  //   • amber   = pending / trial
  //   • rose    = destructive / overdue
  //   • slate   = inactive / cancelled (token-driven dim-text family)
  const tone: { cls: string; label: string } = (() => {
    switch (status) {
      case 'paid':
        return {
          cls: 'border-emerald-500/40 bg-emerald-500/10 text-emerald-300',
          label: 'Paid',
        }
      case 'trial':
        return {
          cls: 'border-amber-500/40 bg-amber-500/10 text-amber-300',
          label: 'Trial',
        }
      case 'overdue':
        return {
          cls: 'border-rose-500/40 bg-rose-500/10 text-rose-300',
          label: 'Overdue',
        }
      case 'cancelled':
      default:
        return {
          cls: 'border-[var(--color-border)] bg-[var(--color-bg-2)] text-[var(--color-text-dim)]',
          label: 'Cancelled',
        }
    }
  })()
  return (
    <span
      data-testid={testId}
      data-status={status}
      className={`inline-block rounded-full border px-2 py-0.5 text-[11px] font-semibold uppercase tracking-wide ${tone.cls}`}
    >
      {tone.label}
    </span>
  )
}

function ApiPendingPill({ testId }: { testId: string }) {
  // Tone copied from BssLandingPage / SettingsPage verbatim so the
  // "API pending" affordance reads as one global token in the operator's
  // mental model. Amber is the canonical "wired, not yet returning" tone.
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

function SearchIcon({ className }: { className?: string }) {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={2}
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden
      className={className}
    >
      <circle cx="11" cy="11" r="7" />
      <path d="M21 21l-4.35-4.35" />
    </svg>
  )
}

/** formatCents — render a cents integer as a localized currency string. */
function formatCents(cents: number, currency: string): string {
  const major = cents / 100
  try {
    return new Intl.NumberFormat(undefined, {
      style: 'currency',
      currency,
      maximumFractionDigits: 0,
    }).format(major)
  } catch {
    // Bad currency code shouldn't blow up the row — fall back to USD.
    return new Intl.NumberFormat(undefined, {
      style: 'currency',
      currency: 'USD',
      maximumFractionDigits: 0,
    }).format(major)
  }
}

/** formatLastPaid — short date-only "May 17, 2026" or "—" on empty. */
function formatLastPaid(iso: string): string {
  if (!iso) return '—'
  const t = new Date(iso).getTime()
  if (!Number.isFinite(t) || t <= 0) return '—'
  return new Date(t).toLocaleDateString(undefined, {
    month: 'short',
    day: 'numeric',
    year: 'numeric',
  })
}
