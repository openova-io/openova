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
import { useFleet } from '@/lib/useFleet'
import { SovereignCard } from '@/widgets/fleet/SovereignCard'

function StatCard({ label, value, sub }: { label: string; value: string | number; sub?: string }) {
  return (
    <Card>
      <CardContent className="pt-5">
        <p className="text-xs text-[oklch(45%_0.01_250)] uppercase tracking-wider font-medium">
          {label}
        </p>
        <p className="mt-1 text-2xl font-bold text-[oklch(92%_0.01_250)] tabular-nums">{value}</p>
        {sub && <p className="mt-0.5 text-xs text-[oklch(40%_0.01_250)]">{sub}</p>}
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
          <h1 className="text-xl font-semibold text-[oklch(92%_0.01_250)]">Sovereign Fleet</h1>
          <p className="mt-1 text-sm text-[oklch(50%_0.01_250)]">
            Manage every OpenOva Sovereign across providers, regions, and Organizations.
          </p>
        </div>
        <div className="flex items-center gap-2">
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
            <p className="mt-1 text-sm text-[oklch(45%_0.01_250)]">
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
        <Server className="h-6 w-6 text-[oklch(40%_0.01_250)]" />
      </div>
      <div>
        <p className="font-medium text-[oklch(75%_0.01_250)]">No Sovereigns provisioned yet</p>
        <p className="mt-1 text-sm text-[oklch(45%_0.01_250)]">
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
