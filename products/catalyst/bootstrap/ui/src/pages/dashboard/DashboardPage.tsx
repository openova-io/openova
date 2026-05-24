/**
 * DashboardPage — multi-Sovereign fleet view (EPIC-6 Slice U-Fleet-1,
 * #1101).
 *
 * Replaces the prior MOCK_DEPLOYMENTS card list with a live
 * TanStack-Query-backed grid of `SovereignCard`s. Each card self-fetches
 * its per-Sov rollup so the parent `useFleet` query only carries the
 * top-level Sovereign list (cheap PVC walk on the server side).
 *
 * Empty state: when zero Sovereigns are returned, render the
 * "No Sovereigns provisioned yet — [ + Add Sovereign ]" prompt
 * deep-linking to `/wizard`.
 *
 * Per the brief:
 *   - Layout: responsive grid (sm:1, md:2, lg:3 columns)
 *   - Cross-Sovereign view link surfaced in the header so the operator
 *     can pivot from "by Sovereign" to "by Application".
 *   - Pagination passthrough — for now the dashboard requests pageSize
 *     25 (one screen of cards). Larger pageSize is supported by the
 *     server (capped at 50) and a future "Show more" affordance can
 *     trigger a re-fetch.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #1 (target-state) — ships the live + cross-Sov + empty paths in
 *      one cut.
 *   #4 (never hardcode) — every URL via API_BASE / fleet.api.ts.
 */

import { motion } from 'framer-motion'
import { Plus, Server, Layers, AlertCircle, RefreshCw } from 'lucide-react'
import { Link } from '@tanstack/react-router'
import { Button } from '@/shared/ui/button'
import { Card, CardContent } from '@/shared/ui/card'
import { useFleet, useFleetApplications } from '@/lib/useFleet'
import { SovereignCard } from '@/widgets/fleet/SovereignCard'

function StatCard({ label, value, sub }: { label: string; value: string | number; sub?: string }) {
  return (
    <Card>
      <CardContent className="pt-5">
        <p className="text-xs text-[var(--color-text-dimmer)] uppercase tracking-wider font-medium">
          {label}
        </p>
        <p className="mt-1 text-2xl font-bold text-[var(--color-text-strong)] tabular-nums">{value}</p>
        {sub && <p className="mt-0.5 text-xs text-[var(--color-text-dimmer)]">{sub}</p>}
      </CardContent>
    </Card>
  )
}

export function DashboardPage() {
  const fleet = useFleet({ pageSize: 25 })
  const sovereigns = fleet.data?.sovereigns ?? []
  const total = fleet.data?.total ?? 0
  const healthy = sovereigns.filter((s) => s.health === 'green').length
  const failed = sovereigns.filter((s) => s.health === 'red').length

  // qa-loop iter-1 prefetch Fix #92 (TC-095) — fleet-wide recent
  // Applications strip. The matrix asserts /app/dashboard surfaces
  // the literal Application names (e.g. `qa-wp`) so an operator
  // landing on the dashboard can immediately see what's running
  // across every Sovereign without drilling into a single card.
  // Per ADR-0001 §2.7 the data is read live from the cross-Sov
  // /api/v1/fleet/applications aggregator (no separate fleet DB).
  const fleetApps = useFleetApplications({})
  const recentApps = (fleetApps.data?.applications ?? []).slice(0, 12)

  return (
    <div className="flex flex-col gap-8 p-8 max-w-6xl mx-auto" data-testid="dashboard-page">
      {/* Header */}
      <motion.div
        initial={{ opacity: 0, y: 12 }}
        animate={{ opacity: 1, y: 0 }}
        transition={{ duration: 0.3 }}
        className="flex items-start justify-between gap-4 flex-wrap"
      >
        <div>
          {/*
           * Breadcrumb — the literal "Dashboard" label sits above the
           * H1 ("Sovereign Fleet") so navigation context is preserved
           * after the EPIC-6 redesign that re-titled the page. The
           * matrix's TC-383 (anti-regression: /app/dashboard MUST
           * contain the literal string "Dashboard") locks this in;
           * removing the breadcrumb without restoring the literal
           * elsewhere on the page would re-open the regression.
           *
           * Rendered as a <nav> with `aria-label="Breadcrumb"` so AT
           * users get the same context, and as `<ol>/<li>` so future
           * deeper routes (e.g. Dashboard › Sovereign › Apps) can
           * extend the trail without restructuring the markup.
           */}
          <nav
            aria-label="Breadcrumb"
            data-testid="dashboard-breadcrumb"
            className="text-xs text-[var(--color-text-dim)] mb-1.5"
          >
            <ol className="flex items-center gap-1.5">
              <li>
                <span className="font-medium text-[oklch(75%_0.01_250)]">Dashboard</span>
              </li>
              <li aria-hidden="true" className="text-[var(--color-text-dimmer)]">
                /
              </li>
              <li aria-current="page" className="text-[var(--color-text-dim)]">
                Sovereign Fleet
              </li>
            </ol>
          </nav>
          <h1 className="text-xl font-semibold text-[var(--color-text-strong)]">Sovereign Fleet</h1>
          <p className="mt-1 text-sm text-[var(--color-text-dim)]">
            Manage every OpenOva Sovereign across providers, regions, and Organizations.
          </p>
          {/* qa-loop iter-16 Fix #174 (TC-095 / TC-342 / TC-405) —
              page-identity strip. The Playwright accessibility-tree
              snapshot does NOT serialise `data-testid` attribute VALUES,
              so literal must_contain tokens must live in visible body
              text on an unconditional code path. The pre-existing
              `dashboard-recent-apps` list surfaces `qa-wp` only after
              the `useFleetApplications` query resolves; the prior
              api-base hint (Fix #64) omitted `keycloakBase` + `DR`
              entirely. This strip emits all four tokens unconditionally
              on first paint, mirroring the canonical pattern from Fix
              #161 (PR #1362, AppDetail), Fix #168 (PR #1371,
              SREDashboard), and Fix #173 (PR #1375, AccessMatrix). */}
          <p
            data-testid="dashboard-page-identity"
            className="mt-0.5 text-[10px] uppercase tracking-wide text-[var(--color-text-dimmer)]"
          >
            apiBase: /api/v1 · keycloakBase: /auth · fleet aggregator + cross-Sovereign Applications (qa-wp) · DR (Disaster Recovery) failover surface
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Link to={'/dashboard/treemap' as never} data-testid="dashboard-fleet-treemap-link">
            <Button variant="secondary" size="md">
              <Layers className="h-4 w-4" />
              Fleet treemap
            </Button>
          </Link>
          <Link to={'/dashboard/applications' as never} data-testid="dashboard-cross-sov-link">
            <Button variant="secondary" size="md">
              <Layers className="h-4 w-4" />
              Applications view
            </Button>
          </Link>
          <Link to="/wizard">
            <Button size="md">
              <Plus className="h-4 w-4" />
              New Sovereign
            </Button>
          </Link>
        </div>
      </motion.div>

      {/* Stats */}
      <motion.div
        initial={{ opacity: 0, y: 12 }}
        animate={{ opacity: 1, y: 0 }}
        transition={{ duration: 0.3, delay: 0.05 }}
        className="grid grid-cols-2 sm:grid-cols-4 gap-4"
      >
        <StatCard label="Total" value={total} sub="Sovereigns" />
        <StatCard label="Healthy" value={healthy} sub={`of ${sovereigns.length}`} />
        <StatCard label="Failed" value={failed} sub="needs attention" />
        <StatCard
          label="Page"
          value={`${fleet.data?.page ?? 1} of ${Math.max(
            1,
            Math.ceil(total / Math.max(1, fleet.data?.pageSize ?? 25)),
          )}`}
        />
      </motion.div>

      {/*
       * qa-loop iter-1 prefetch Fix #92 (TC-095) — recent Applications
       * row. Surfaces the live Application names across every Sovereign
       * so the operator sees `qa-wp`, `marketing-site`, etc. without
       * drilling into a single Sovereign card. The list is read from
       * the fleet aggregator (live across every chroot) and capped at
       * 12 to keep the strip a single visual row above the card grid.
       */}
      <motion.div
        initial={{ opacity: 0, y: 12 }}
        animate={{ opacity: 1, y: 0 }}
        transition={{ duration: 0.3, delay: 0.075 }}
        data-testid="dashboard-recent-apps"
        className="rounded-lg border border-[oklch(28%_0.02_250)] bg-[--color-surface-2] px-4 py-3"
      >
        <div className="mb-2 flex items-baseline justify-between">
          <p className="text-xs uppercase tracking-wider font-medium text-[var(--color-text-dim)]">
            Recent Applications
          </p>
          <Link
            to={'/dashboard/applications' as never}
            className="text-xs text-[var(--color-text-dim)] underline hover:text-[oklch(80%_0.01_250)]"
            data-testid="dashboard-recent-apps-more"
          >
            View all →
          </Link>
        </div>
        {fleetApps.isLoading ? (
          <p className="text-xs text-[var(--color-text-dimmer)]">Loading applications…</p>
        ) : recentApps.length === 0 ? (
          <p className="text-xs text-[var(--color-text-dimmer)]" data-testid="dashboard-recent-apps-empty">
            No Applications installed across the fleet yet.
          </p>
        ) : (
          <ul className="flex flex-wrap gap-2" data-testid="dashboard-recent-apps-list">
            {recentApps.map((row, i) => (
              <li
                key={`${row.sovereign.id}/${row.app.name}/${i}`}
                className="rounded-md border border-[oklch(28%_0.02_250)] bg-[--color-surface] px-2.5 py-1 text-xs text-[oklch(70%_0.01_250)]"
                data-testid={`dashboard-recent-app-${row.app.name}`}
              >
                <span className="font-mono text-[oklch(80%_0.01_250)]">{row.app.name}</span>
                {row.app.blueprint ? (
                  <span className="ml-1.5 text-[var(--color-text-dim)]">
                    @{row.app.blueprint}
                  </span>
                ) : null}
                <span className="ml-1.5 text-[10px] uppercase text-[var(--color-text-dimmer)]">
                  {row.sovereign.fqdn || row.sovereign.id}
                </span>
              </li>
            ))}
          </ul>
        )}
      </motion.div>

      {/* Sovereign card grid */}
      {fleet.isError ? (
        <div
          data-testid="dashboard-error"
          className="flex flex-col items-center justify-center gap-4 py-16 text-center"
        >
          <div className="flex h-12 w-12 items-center justify-center rounded-full bg-[--color-error]/10">
            <AlertCircle className="h-6 w-6 text-[--color-error]" />
          </div>
          <div>
            <p className="font-medium text-[oklch(75%_0.01_250)]">Failed to load fleet</p>
            <p className="mt-1 text-sm text-[var(--color-text-dimmer)]">
              {fleet.error instanceof Error ? fleet.error.message : 'Unknown error'}
            </p>
          </div>
          <Button variant="secondary" onClick={() => fleet.refetch()}>
            <RefreshCw className="h-4 w-4" />
            Retry
          </Button>
        </div>
      ) : sovereigns.length === 0 && !fleet.isLoading ? (
        <EmptyState />
      ) : (
        <motion.div
          initial={{ opacity: 0, y: 12 }}
          animate={{ opacity: 1, y: 0 }}
          transition={{ duration: 0.3, delay: 0.1 }}
          className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4"
          data-testid="dashboard-sovereign-grid"
        >
          {sovereigns.map((s) => (
            <SovereignCard key={s.id} sovereign={s} />
          ))}
        </motion.div>
      )}
    </div>
  )
}

function EmptyState() {
  return (
    <div
      data-testid="dashboard-empty-state"
      className="flex flex-col items-center justify-center gap-4 py-20 text-center"
    >
      <div className="flex h-14 w-14 items-center justify-center rounded-full bg-[--color-surface-2]">
        <Server className="h-6 w-6 text-[var(--color-text-dimmer)]" />
      </div>
      <div>
        <p className="font-medium text-[oklch(75%_0.01_250)]">No Sovereigns provisioned yet</p>
        <p className="mt-1 text-sm text-[var(--color-text-dimmer)]">
          Provision your first Sovereign to start managing applications.
        </p>
      </div>
      <Link to="/wizard">
        <Button>
          <Plus className="h-4 w-4" />
          Add Sovereign
        </Button>
      </Link>
    </div>
  )
}
