/**
 * CrossSovereignView — aggregated table of every Application across
 * every visible Sovereign (EPIC-6 Slice U-Fleet-3, #1101).
 *
 * Columns:
 *   - Application (name + blueprint chip)
 *   - Sovereign FQDN
 *   - Org slug
 *   - Regions (chip per region)
 *   - Topology (single-region | active-active | active-hotstandby)
 *   - DR posture (— | DR active | DR alert | Misconfigured)
 *   - Status (CR phase passthrough)
 *
 * Filters (all server-side per the brief):
 *   - Org filter (text)
 *   - Topology mode (select)
 *   - DR posture (select)
 *
 * Each row clickable → opens that App's AppDetail in the relevant
 * Sovereign (via `sovereignChrootURL(sov, { appName })`).
 *
 * Per the brief out-of-scope notes:
 *   - No cross-Sovereign Application reuse logic (Applications stay
 *     within one Sovereign by design)
 *   - No fleet-level RBAC/install actions (read-only fleet view)
 */

import { useMemo, useState } from 'react'
import { motion } from 'framer-motion'
import { ArrowLeft, Layers, AlertCircle, RefreshCw, Search } from 'lucide-react'
import { Link } from '@tanstack/react-router'
import { Button } from '@/shared/ui/button'
import { Card, CardContent } from '@/shared/ui/card'
import { Badge } from '@/shared/ui/badge'
import { Input } from '@/shared/ui/input'
import {
  type ApplicationRow,
  type DRPosture,
  type TopologyMode,
  drPostureBadgeColor,
  sovereignChrootURL,
} from '@/lib/fleet.api'
import { useFleetApplications } from '@/lib/useFleet'

const TOPOLOGY_OPTIONS: Array<{ value: '' | TopologyMode; label: string }> = [
  { value: '', label: 'All topologies' },
  { value: 'single-region', label: 'Single region' },
  { value: 'active-active', label: 'Active-Active' },
  { value: 'active-hotstandby', label: 'Active-Hotstandby' },
]

const DR_OPTIONS: Array<{ value: '' | DRPosture; label: string }> = [
  { value: '', label: 'All DR postures' },
  { value: '—', label: '— (no DR)' },
  { value: 'DR active', label: 'DR active' },
  { value: 'DR alert', label: 'DR alert' },
  { value: 'Misconfigured', label: 'Misconfigured' },
]

export function CrossSovereignView() {
  const [orgFilter, setOrgFilter] = useState('')
  const [topology, setTopology] = useState<'' | TopologyMode>('')
  const [dr, setDR] = useState<'' | DRPosture>('')

  const filters = useMemo(
    () => ({
      org: orgFilter || undefined,
      topology: topology || undefined,
      drPosture: dr || undefined,
    }),
    [orgFilter, topology, dr],
  )

  const query = useFleetApplications(filters)
  const rows = query.data?.applications ?? []

  return (
    <div
      className="flex flex-col gap-6 p-8 max-w-7xl mx-auto"
      data-testid="cross-sov-page"
    >
      {/* Header */}
      <motion.div
        initial={{ opacity: 0, y: 12 }}
        animate={{ opacity: 1, y: 0 }}
        transition={{ duration: 0.3 }}
        className="flex items-start justify-between gap-4 flex-wrap"
      >
        <div>
          <Link to="/dashboard" className="text-xs text-[var(--color-text-dim)] hover:underline flex items-center gap-1">
            <ArrowLeft className="h-3 w-3" />
            Back to Sovereign Fleet
          </Link>
          <h1 className="text-xl font-semibold text-[var(--color-text-strong)] mt-1">
            Applications across the fleet
          </h1>
          <p className="mt-1 text-sm text-[var(--color-text-dim)]">
            Every Application in every visible Sovereign — filter by Org, topology, or DR posture.
          </p>
        </div>
        <Badge variant="default" data-testid="cross-sov-total">
          <Layers className="h-3 w-3" />
          {query.data?.total ?? 0} applications
        </Badge>
      </motion.div>

      {/* Filters */}
      <motion.div
        initial={{ opacity: 0, y: 12 }}
        animate={{ opacity: 1, y: 0 }}
        transition={{ duration: 0.3, delay: 0.05 }}
        className="grid grid-cols-1 md:grid-cols-3 gap-3"
      >
        <div className="relative">
          <Search className="absolute left-3 top-1/2 -translate-y-1/2 h-4 w-4 text-[var(--color-text-dimmer)]" />
          <Input
            data-testid="cross-sov-filter-org"
            placeholder="Filter by Org slug…"
            value={orgFilter}
            onChange={(e) => setOrgFilter(e.target.value)}
            className="pl-9"
          />
        </div>
        <select
          data-testid="cross-sov-filter-topology"
          aria-label="Filter by topology"
          value={topology}
          onChange={(e) => setTopology(e.target.value as '' | TopologyMode)}
          className="rounded-[--radius-md] border border-[--color-surface-border] bg-[--color-surface-2] px-3 py-2 text-sm text-[var(--color-text)]"
        >
          {TOPOLOGY_OPTIONS.map((o) => (
            <option key={o.value} value={o.value}>
              {o.label}
            </option>
          ))}
        </select>
        <select
          data-testid="cross-sov-filter-dr"
          aria-label="Filter by DR posture"
          value={dr}
          onChange={(e) => setDR(e.target.value as '' | DRPosture)}
          className="rounded-[--radius-md] border border-[--color-surface-border] bg-[--color-surface-2] px-3 py-2 text-sm text-[var(--color-text)]"
        >
          {DR_OPTIONS.map((o) => (
            <option key={o.value} value={o.value}>
              {o.label}
            </option>
          ))}
        </select>
      </motion.div>

      {/* Table */}
      {query.isError ? (
        <div
          data-testid="cross-sov-error"
          className="flex flex-col items-center justify-center gap-4 py-16 text-center"
        >
          <AlertCircle className="h-8 w-8 text-[--color-error]" />
          <div>
            <p className="font-medium text-[oklch(75%_0.01_250)]">Failed to load applications</p>
            <p className="mt-1 text-sm text-[var(--color-text-dimmer)]">
              {query.error instanceof Error ? query.error.message : 'Unknown error'}
            </p>
          </div>
          <Button variant="secondary" onClick={() => query.refetch()}>
            <RefreshCw className="h-4 w-4" />
            Retry
          </Button>
        </div>
      ) : rows.length === 0 && !query.isLoading ? (
        <Card>
          <CardContent className="flex flex-col items-center gap-2 py-12 text-center">
            <Layers className="h-6 w-6 text-[var(--color-text-dimmer)]" />
            <p className="text-sm text-[var(--color-text-dim)]" data-testid="cross-sov-empty">
              No Applications match the current filters.
            </p>
          </CardContent>
        </Card>
      ) : (
        <Card>
          <CardContent className="p-0">
            <div className="overflow-x-auto">
              <table
                data-testid="cross-sov-table"
                className="min-w-full divide-y divide-[--color-surface-border]"
              >
                <thead>
                  <tr className="text-left text-[10px] uppercase tracking-wider text-[var(--color-text-dim)]">
                    <Th>Application</Th>
                    <Th>Sovereign</Th>
                    <Th>Org</Th>
                    <Th>Regions</Th>
                    <Th>Topology</Th>
                    <Th>DR posture</Th>
                    <Th>Status</Th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-[--color-surface-border]">
                  {rows.map((row) => (
                    <Row key={`${row.sovereign.id}/${row.namespace ?? ''}/${row.app.name}`} row={row} />
                  ))}
                </tbody>
              </table>
            </div>
          </CardContent>
        </Card>
      )}
    </div>
  )
}

function Th({ children }: { children: React.ReactNode }) {
  return <th className="px-3 py-2 font-medium">{children}</th>
}

function Row({ row }: { row: ApplicationRow }) {
  const handleClick = () => {
    if (typeof window === 'undefined') return
    window.location.href = sovereignChrootURL(row.sovereign, {
      appName: row.app.name,
      namespace: row.namespace,
    })
  }
  return (
    <tr
      className="cursor-pointer hover:bg-[--color-surface-2] transition-colors"
      onClick={handleClick}
      data-testid={`cross-sov-row-${row.sovereign.id}-${row.app.name}`}
    >
      <td className="px-3 py-3 text-sm text-[var(--color-text-strong)] font-mono">
        <div className="flex flex-col gap-0.5">
          <span>{row.app.name}</span>
          {row.app.blueprint && (
            <span className="text-[10px] text-[var(--color-text-dimmer)]">
              {row.app.blueprint}
              {row.app.version ? ` @ ${row.app.version}` : ''}
            </span>
          )}
        </div>
      </td>
      <td className="px-3 py-3 text-sm text-[oklch(80%_0.01_250)] font-mono">
        {row.sovereign.fqdn || row.sovereign.id}
      </td>
      <td className="px-3 py-3 text-sm text-[oklch(80%_0.01_250)]">{row.org || '—'}</td>
      <td className="px-3 py-3 text-sm">
        <div className="flex flex-wrap gap-1">
          {row.regions.length === 0 ? (
            <span className="text-[var(--color-text-dimmer)]">—</span>
          ) : (
            row.regions.map((r) => (
              <Badge key={r} variant="default">
                {r}
              </Badge>
            ))
          )}
        </div>
      </td>
      <td className="px-3 py-3 text-sm text-[oklch(80%_0.01_250)]">{row.topology}</td>
      <td className="px-3 py-3 text-sm">
        <span
          className={
            'inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-xs font-medium border ' +
            drPostureBadgeColor(row.drPosture)
          }
          data-testid={`cross-sov-dr-${row.app.name}`}
        >
          {row.drPosture}
        </span>
      </td>
      <td className="px-3 py-3 text-sm text-[oklch(70%_0.01_250)]">{row.status || '—'}</td>
    </tr>
  )
}
