/**
 * TopologyTab — per-Application topology editor + live status panel,
 * embedded in the AppDetail page (EPIC-2 #1097 slice T).
 *
 * Renders, per the brief:
 *
 *  • Mode picker + region multi-select (TopologyEditor widget)
 *  • Live status panel — per-region rollout state (read from
 *    Application.status.regions[]); replication lag for
 *    active-hotstandby (Continuum CR; "—" when absent); last
 *    switchover event (read-only).
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 every URL derives from the
 * catalog.api helpers (API_BASE-rooted). Status data uses the same
 * GET /applications/{name}/status endpoint slice I shipped.
 *
 * Sub-page tab pattern per the seam map (slice U / ComplianceTab,
 * slice U5 / MembersTab): tab files live in a sibling directory next
 * to the page.
 */

import { useEffect, useMemo, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'

import {
  getApplicationStatus,
  getCatalogItem,
  type CatalogItem,
} from '@/lib/catalog.api'
import { getHierarchicalInfrastructure } from '@/lib/infrastructure.types'
import { TopologyEditor } from '@/widgets/topology/TopologyEditor'
import { DRSection } from '@/widgets/continuum/DRSection'

export interface TopologyTabProps {
  /** Sovereign id (deploymentId on chroot, mother). */
  sovereignId: string
  /** Application name. */
  applicationName: string
  /** Org namespace. */
  namespace?: string
  /** Test seam — pre-fill data, skip network. */
  initialApp?: ApplicationStatus
  /** Test seam — bypass apply network call. */
  disableNetwork?: boolean
  /** Caller's tier (slice U-DR-1) — controls render of switchover/failback buttons. */
  callerTier?: string
  /** Continuum CR name (slice U-DR-1) — when omitted, derived as `dr-<applicationName>`. */
  continuumName?: string
}

interface ApplicationStatus {
  name: string
  namespace: string
  phase?: string
  status?: Record<string, unknown>
  spec?: ApplicationSpec
}

interface ApplicationSpec {
  blueprintRef?: { name?: string; version?: string }
  placement?: string
  regions?: string[]
  environmentRef?: string
}

interface RegionStatus {
  name: string
  role?: string
  replicas?: number
  ready?: number
  lastTransitionTime?: string
}

export function TopologyTab({
  sovereignId,
  applicationName,
  namespace,
  initialApp,
  disableNetwork = false,
  callerTier,
  continuumName,
}: TopologyTabProps) {
  const qc = useQueryClient()
  const [refreshTick, setRefreshTick] = useState(0)

  const statusQ = useQuery({
    queryKey: ['application-status', sovereignId, applicationName, namespace, refreshTick],
    queryFn: () => getApplicationStatus(sovereignId, applicationName, namespace),
    enabled: !initialApp && !!sovereignId && !!applicationName,
    refetchInterval: 10_000,
  })

  // Resolve the spec view of the Application — this comes from the
  // status endpoint AS WELL as the spec block (catalyst-api returns
  // both for completeness on the same payload — see slice I's
  // ApplicationStatusResponse). For tests / disableNetwork paths the
  // initialApp prop is the source.
  const app: ApplicationStatus | undefined = initialApp ?? statusQ.data

  const currentMode = useMemo(() => {
    if (!app) return 'single-region'
    const fromSpec = (app.spec?.placement ?? '').trim()
    if (fromSpec) return fromSpec
    const fromStatus = (app.status as Record<string, unknown> | undefined)?.placement as string | undefined
    return fromStatus ?? 'single-region'
  }, [app])

  const currentRegions = useMemo(() => {
    if (!app) return [] as string[]
    const fromSpec = app.spec?.regions
    if (Array.isArray(fromSpec)) return fromSpec
    const statusRegions = ((app.status as Record<string, unknown> | undefined)?.regions ?? []) as RegionStatus[]
    return statusRegions.map((r) => r.name).filter(Boolean)
  }, [app])

  const regionStatuses: RegionStatus[] = useMemo(() => {
    const statusRegions = ((app?.status as Record<string, unknown> | undefined)?.regions ?? []) as RegionStatus[]
    return statusRegions
  }, [app])

  // Pull the Blueprint card so the placementSchema modes constraint
  // applies. The status endpoint includes blueprintRef on the spec; we
  // call /catalog/{name} to read placementSchema.modes.
  const blueprintRef = app?.spec?.blueprintRef
  const blueprintQ = useQuery({
    queryKey: ['catalog-item', blueprintRef?.name],
    queryFn: () => getCatalogItem(blueprintRef!.name!),
    enabled: !!blueprintRef?.name,
    staleTime: 60_000,
  })
  const blueprint: CatalogItem | undefined = blueprintQ.data

  // G83 #2630: availableRegions sourced from the Sovereign's own
  // infrastructure topology (deployment.request.regions[].cloudRegion)
  // instead of the prior hardcoded Hetzner placeholder list. On a
  // multi-region HCS Sovereign (e.g. hw86) this surfaces the real
  // me-east-215-a/b codes; on a Hetzner Sovereign it surfaces the
  // hz-* codes the operator actually provisioned.
  const infraQ = useQuery({
    queryKey: ['infrastructure-topology', sovereignId],
    queryFn: () => getHierarchicalInfrastructure(sovereignId),
    enabled: !!sovereignId && !disableNetwork,
    staleTime: 60_000,
  })

  const availableRegions = useMemo(() => {
    const set = new Set<string>(currentRegions)
    const regions = infraQ.data?.regions ?? []
    for (const r of regions) {
      // RegionSpec carries both `name` (the catalyst region id e.g.
      // hw-me-east-215-a-rtz-prod) and `providerRegion` (the cloud
      // code e.g. me-east-215-a). The TopologyEditor consumes the
      // catalyst id since Application.spec.regions stores those, so
      // surface `name` here.
      if (r.name) set.add(r.name)
    }
    return Array.from(set).sort()
  }, [currentRegions, infraQ.data])

  useEffect(() => {
    // When initialApp updates, just trigger no-op so consumers re-derive.
  }, [initialApp])

  return (
    <div className="topology-tab" data-testid="app-topology-tabpanel">
      <p className="mb-3 text-xs text-[var(--color-text-dim)]">
        Edit the placement mode and region set for{' '}
        <code className="font-mono text-[var(--color-text)]">{applicationName}</code>. Apply
        commits the change to the Application CR; the application-controller fans out
        per-region HelmReleases and reconciles the rollout.
      </p>

      <TopologyEditor
        sovereignId={sovereignId}
        applicationName={applicationName}
        currentMode={currentMode}
        currentRegions={currentRegions}
        availableRegions={availableRegions}
        namespace={namespace}
        blueprint={blueprint}
        disableNetwork={disableNetwork}
        onApplied={() => {
          setRefreshTick((t) => t + 1)
          void qc.invalidateQueries({ queryKey: ['application-status'] })
        }}
      />

      <h3 className="mt-6 mb-2 text-sm font-medium text-[var(--color-text-strong)]">
        Live status
      </h3>

      <div
        className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] p-3"
        data-testid="topology-tab-status-panel"
      >
        {!app ? (
          <p className="text-xs text-[var(--color-text-dim)]" data-testid="topology-tab-status-loading">
            Loading status…
          </p>
        ) : regionStatuses.length === 0 ? (
          <p className="text-xs text-[var(--color-text-dim)]" data-testid="topology-tab-status-empty">
            No per-region status yet — the controller has not reported a rollout state.
          </p>
        ) : (
          <ul className="space-y-1.5" role="list">
            {regionStatuses.map((r) => (
              <li
                key={r.name}
                data-testid={`topology-tab-region-${r.name}`}
                className="grid grid-cols-[8rem_4rem_5rem_1fr] items-center gap-2 text-xs"
              >
                <code className="font-mono text-[var(--color-text)]">{r.name}</code>
                <span
                  className={`inline-flex items-center justify-center rounded-md px-2 py-0.5 text-[10px] font-semibold uppercase ${
                    r.role === 'primary'
                      ? 'bg-green-500/10 text-green-400'
                      : r.role === 'standby'
                        ? 'bg-yellow-500/10 text-yellow-400'
                        : 'bg-[var(--color-border)]/40 text-[var(--color-text-dim)]'
                  }`}
                >
                  {r.role ?? 'active'}
                </span>
                <span className="font-mono text-[var(--color-text-dim)]">
                  {r.ready ?? 0}/{r.replicas ?? '?'}
                </span>
                <span className="text-[var(--color-text-dim)]">
                  {r.lastTransitionTime ? new Date(r.lastTransitionTime).toLocaleString() : '—'}
                </span>
              </li>
            ))}
          </ul>
        )}
        <div className="mt-3 grid grid-cols-2 gap-3 text-xs text-[var(--color-text-dim)]">
          <div data-testid="topology-tab-replication-lag">
            <strong className="text-[var(--color-text)]">Replication lag</strong>:{' '}
            {currentMode === 'active-hotstandby' ? '—' : 'n/a (mode)'}
          </div>
          <div data-testid="topology-tab-last-switchover">
            <strong className="text-[var(--color-text)]">Last switchover</strong>:{' '}
            {(app?.status as Record<string, unknown> | undefined)?.lastSwitchover
              ? String((app?.status as Record<string, unknown>).lastSwitchover)
              : '—'}
          </div>
        </div>
      </div>

      {/* EPIC-6 Slice U-DR-1 (#1101) — DR section. Visible only when
          the Application's placement is active-hotstandby. Composes the
          live Continuum status, switchover button, failback panel, and
          switchover history. */}
      {currentMode === 'active-hotstandby' ? (
        <DRSection
          sovereignId={sovereignId}
          continuumName={continuumName ?? `dr-${applicationName}`}
          applicationName={applicationName}
          namespace={namespace}
          callerTier={callerTier}
          disableNetwork={disableNetwork}
        />
      ) : null}
    </div>
  )
}
