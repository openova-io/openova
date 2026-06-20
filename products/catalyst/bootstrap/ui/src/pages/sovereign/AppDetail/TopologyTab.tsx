/**
 * TopologyTab — #3969 application-centric placement.
 *
 * EXACTLY TWO panels: { Placement, Status }.
 *
 *  • Placement (read-only) — the desired target cards (region · cluster ·
 *    vCluster, ● PRIMARY / ○ STANDBY·Hot|Cold), a replication arrow between
 *    Primary and Standby, the DERIVED "Pattern:" label, and the owned-deps
 *    "follow this placement" rows. An "Edit placement" affordance opens the
 *    ONE PlacementEditor (also used by the wizard + switchover).
 *  • Status — the single recon chip: ● Reconciled / Reconciling / Degraded
 *    (+ plain reason). A mismatch is NEVER a second contradictory value.
 *
 * DELETED by #3969: the old declared/derived-class machinery
 * (canonicalTopologyClass, the second derived class, DeclaredTopologyPanel,
 * the observed strip, the parroted-class/unbuilt-mandate cells), the entire
 * DR section (DRSection / imperative Switchover verb), and the TOPOLOGY_BY_ID
 * matrix consumption. Switchover = a role-flip edit in the same editor; the
 * "Promote" affordance, if any, is a shortcut that opens the editor with
 * roles pre-flipped — not a separate verb.
 */

import { useMemo, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'

import { getApplicationStatus, getCatalogItem, type CatalogItem } from '@/lib/catalog.api'
import { getHierarchicalInfrastructure } from '@/lib/infrastructure.types'
import { PlacementEditor, type OwnedDependencyInfo } from '@/widgets/topology/PlacementEditor'
import {
  type Capability,
  type PlacementTarget,
  type ReconStatus,
  derivePattern,
  normalizeCapability,
  targetsFromLegacy,
} from '@/shared/lib/placement'

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
  /** Caller's tier — reserved (the editor server-side enforces the role). */
  callerTier?: string
  /**
   * #3656 — true when this app is a bootstrap-kit HelmRelease with NO
   * companion Application CR. The GET /applications/{name}/status endpoint
   * 404s for these, so we MUST NOT poll it (calm n/a status instead of a
   * 404 loop). Generic by construction — keys on "has Application CR", never
   * a blueprint name.
   */
  isBootstrap?: boolean
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
  placement?: string | Record<string, unknown>
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
  isBootstrap = false,
}: TopologyTabProps) {
  const qc = useQueryClient()
  const [editing, setEditing] = useState(false)
  const [refreshTick, setRefreshTick] = useState(0)

  // #3656 — bootstrap-kit HelmReleases have no Application CR; the status
  // endpoint 404s forever, so don't poll it for them.
  const statusQ = useQuery({
    queryKey: ['application-status', sovereignId, applicationName, namespace, refreshTick],
    queryFn: () => getApplicationStatus(sovereignId, applicationName, namespace),
    enabled: !initialApp && !isBootstrap && !!sovereignId && !!applicationName,
    refetchInterval: isBootstrap ? false : 10_000,
    retry: false,
  })

  const app: ApplicationStatus | undefined = initialApp ?? statusQ.data

  // Pull the Blueprint card for placementCapability (gates >1 Primary).
  const blueprintRef = app?.spec?.blueprintRef
  const blueprintQ = useQuery({
    queryKey: ['catalog-item', blueprintRef?.name],
    queryFn: () => getCatalogItem(blueprintRef!.name!),
    enabled: !!blueprintRef?.name,
    staleTime: 60_000,
  })
  const blueprint: CatalogItem | undefined = blueprintQ.data

  const capability: Capability = useMemo(() => {
    const raw = (blueprint as Record<string, unknown> | undefined)?.placementCapability as string | undefined
    return normalizeCapability(raw)
  }, [blueprint])

  // Infra topology — region + cluster option source for the editor.
  const infraQ = useQuery({
    queryKey: ['infrastructure-topology', sovereignId],
    queryFn: () => getHierarchicalInfrastructure(sovereignId),
    enabled: !!sovereignId && !disableNetwork,
    staleTime: 60_000,
  })

  // The desired-state targets: prefer spec.placement.targets[] (#3969
  // canonical); else project the legacy mode/regions or status rollup.
  const targets: PlacementTarget[] = useMemo(() => {
    const specPlacement = app?.spec?.placement
    if (specPlacement && typeof specPlacement === 'object') {
      const t = (specPlacement as Record<string, unknown>).targets
      if (Array.isArray(t) && t.length > 0) return t as PlacementTarget[]
    }
    // Legacy fallback: project from status.targets (recon rollup), else
    // from the legacy mode + regions.
    const status = (app?.status ?? {}) as Record<string, unknown>
    const statusTargets = status.targets
    if (Array.isArray(statusTargets) && statusTargets.length > 0) {
      return statusTargets as PlacementTarget[]
    }
    const statusRegions = (status.regions ?? []) as RegionStatus[]
    const mode = typeof specPlacement === 'string' ? specPlacement : ((status.placement as string) ?? '')
    const regions = Array.isArray(app?.spec?.regions) ? app!.spec!.regions! : statusRegions.map((r) => r.name)
    return targetsFromLegacy({ mode, regions, statusRegions })
  }, [app])

  const pattern = useMemo(() => derivePattern(targets), [targets])

  // Owned dependencies that cascade — read from spec.placement.ownedDependencies
  // when present (override state); the names also come from the blueprint's
  // backingServices. Default follow:true.
  const ownedDeps: OwnedDependencyInfo[] = useMemo(() => {
    const specPlacement = app?.spec?.placement
    const overrides =
      specPlacement && typeof specPlacement === 'object'
        ? ((specPlacement as Record<string, unknown>).ownedDependencies as
            | Array<{ name: string; follow?: boolean }>
            | undefined)
        : undefined
    const fromBlueprint =
      ((blueprint as Record<string, unknown> | undefined)?.backingServices as
        | Array<{ type?: string; mode?: string; instanceRef?: string }>
        | undefined) ?? []
    // Owned (private) backing services follow by default. Derive the
    // instance name the same way the generator does (<consumer>-pg) for
    // private mode; shared deps are not owned and do not appear here.
    const ownedNames = new Set<string>()
    for (const bs of fromBlueprint) {
      const mode = (bs.mode ?? 'private').toLowerCase()
      if (mode === 'private') ownedNames.add(bs.instanceRef || `${applicationName}-pg`)
    }
    // Merge any override state.
    const byName = new Map<string, boolean>()
    for (const n of ownedNames) byName.set(n, true)
    for (const o of overrides ?? []) byName.set(o.name, o.follow !== false)
    return Array.from(byName.entries()).map(([name, follow]) => ({ name, follow }))
  }, [app, blueprint, applicationName])

  // Recon status — read the single status value (no second derived class).
  const recon: { status: ReconStatus; reason: string } = useMemo(() => {
    const status = (app?.status ?? {}) as Record<string, unknown>
    const placement = (status.placement as string) ?? ''
    const reason = (status.reason as string) ?? ''
    // Accept the canonical recon vocabulary verbatim; map the legacy phase
    // strings (Ready/Provisioning/Degraded) onto it.
    const v = placement.toLowerCase()
    if (v === 'reconciled' || v === 'ready') return { status: 'Reconciled', reason }
    if (v === 'degraded') return { status: 'Degraded', reason }
    if (v === 'reconciling' || v === 'provisioning') return { status: 'Reconciling', reason }
    const phase = ((app?.phase ?? status.phase) as string | undefined)?.toLowerCase() ?? ''
    if (phase === 'ready') return { status: 'Reconciled', reason }
    if (phase === 'degraded' || phase === 'failed') return { status: 'Degraded', reason }
    return { status: 'Reconciling', reason }
  }, [app])

  const availableRegions = useMemo(() => {
    const set = new Set<string>(targets.map((t) => t.region))
    for (const r of infraQ.data?.topology?.regions ?? []) if (r.name) set.add(r.name)
    return Array.from(set).filter(Boolean).sort()
  }, [targets, infraQ.data])

  const availableClusters = useMemo(() => {
    const set = new Set<string>(targets.map((t) => t.cluster))
    for (const r of infraQ.data?.topology?.regions ?? []) {
      const clusters = (r as unknown as { clusters?: Array<{ name?: string }> }).clusters ?? []
      for (const c of clusters) if (c.name) set.add(c.name)
      if (r.name) set.add(r.name)
    }
    return Array.from(set).filter(Boolean).sort()
  }, [targets, infraQ.data])

  return (
    <div className="topology-tab" data-testid="app-topology-tabpanel">
      {/* ── Panel 1: Placement ───────────────────────────────────────── */}
      <section
        className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] p-4"
        data-testid="topology-tab-placement-panel"
      >
        <div className="mb-3 flex items-baseline justify-between">
          <h3 className="text-sm font-semibold text-[var(--color-text-strong)]">Placement</h3>
          <span className="text-xs text-[var(--color-text-dim)]">
            Pattern:{' '}
            <span className="font-semibold text-[var(--color-accent)]" data-testid="topology-tab-pattern">
              {pattern}
            </span>
          </span>
        </div>

        {!editing ? (
          <>
            {targets.length === 0 ? (
              <p className="text-xs text-[var(--color-text-dim)]" data-testid="topology-tab-placement-empty">
                No placement targets reported yet.
              </p>
            ) : (
              <div className="flex flex-wrap items-stretch gap-2" data-testid="topology-tab-target-cards">
                {targets.map((t, i) => (
                  <div key={`${t.region}-${i}`} className="flex items-center gap-2">
                    <div
                      className={`min-w-[12rem] rounded-md border px-3 py-2 ${
                        t.role === 'Primary'
                          ? 'border-green-500/40 bg-green-500/10'
                          : 'border-yellow-500/40 bg-yellow-500/10'
                      }`}
                      data-testid={`topology-tab-target-card-${i}`}
                    >
                      <div className="font-mono text-[11px] text-[var(--color-text-dim)]">
                        {t.region} · {t.cluster} · {t.vcluster ?? 'mgmt'}
                      </div>
                      <div
                        className={`mt-1 text-xs font-semibold ${
                          t.role === 'Primary' ? 'text-green-400' : 'text-yellow-400'
                        }`}
                        data-testid={`topology-tab-target-card-${i}-role`}
                      >
                        {t.role === 'Primary' ? '● PRIMARY' : `○ STANDBY · ${t.standbyType ?? 'Hot'}`}
                      </div>
                      <div className="text-[10px] text-[var(--color-text-dim)]">
                        {t.role === 'Primary'
                          ? 'serves writes'
                          : t.standbyType === 'Cold'
                            ? 'snapshot/bucket follower'
                            : 'live replica'}
                      </div>
                    </div>
                    {i < targets.length - 1 ? (
                      <span className="text-[10px] text-[var(--color-text-dim)]" aria-hidden>
                        ──▶
                        <br />
                        repl
                      </span>
                    ) : null}
                  </div>
                ))}
              </div>
            )}

            {ownedDeps.length > 0 ? (
              <div className="mt-4" data-testid="topology-tab-owned-deps">
                <p className="mb-1 text-[10px] font-semibold uppercase tracking-wide text-[var(--color-text-dim)]">
                  Owned dependencies (follow this placement)
                </p>
                <ul className="space-y-1 text-xs">
                  {ownedDeps.map((d) => (
                    <li key={d.name} data-testid={`topology-tab-owned-${d.name}`} className="font-mono">
                      <span className="text-[var(--color-text)]">{d.name}</span>{' '}
                      <span className={d.follow ? 'text-green-400' : 'text-yellow-400'}>
                        {d.follow ? '✓ follows' : 'decoupled (pinned)'}
                      </span>
                    </li>
                  ))}
                </ul>
              </div>
            ) : null}

            <button
              type="button"
              className="mt-4 rounded-md border border-[var(--color-border)] px-3 py-1.5 text-xs hover:border-[var(--color-accent)]"
              onClick={() => setEditing(true)}
              data-testid="topology-tab-edit-placement"
            >
              Edit placement
            </button>
          </>
        ) : (
          <PlacementEditor
            key={applicationName}
            sovereignId={sovereignId}
            applicationName={applicationName}
            namespace={namespace}
            initialTargets={targets}
            capability={capability}
            availableRegions={availableRegions}
            availableClusters={availableClusters}
            ownedDependencies={ownedDeps}
            disableNetwork={disableNetwork}
            onCancel={() => setEditing(false)}
            onApplied={() => {
              setEditing(false)
              setRefreshTick((t) => t + 1)
              void qc.invalidateQueries({ queryKey: ['application-status'] })
            }}
          />
        )}
      </section>

      {/* ── Panel 2: Status ──────────────────────────────────────────── */}
      <section
        className="mt-4 rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] p-4"
        data-testid="topology-tab-status-panel"
      >
        <h3 className="mb-2 text-sm font-semibold text-[var(--color-text-strong)]">Status</h3>
        {isBootstrap ? (
          <p className="text-xs text-[var(--color-text-dim)]" data-testid="topology-tab-status-bootstrap">
            n/a — bootstrap component (HelmRelease, no Application CR). Live rollout status is tracked via Flux.
          </p>
        ) : !app ? (
          <p className="text-xs text-[var(--color-text-dim)]" data-testid="topology-tab-status-loading">
            Loading status…
          </p>
        ) : (
          <div className="flex items-center gap-2 text-xs" data-testid="topology-tab-recon-status">
            <span
              className={`inline-flex items-center gap-1.5 rounded-md px-2.5 py-1 font-semibold ${
                recon.status === 'Reconciled'
                  ? 'bg-green-500/10 text-green-400'
                  : recon.status === 'Degraded'
                    ? 'bg-red-500/10 text-red-400'
                    : 'bg-yellow-500/10 text-yellow-400'
              }`}
            >
              ● {recon.status}
            </span>
            {recon.reason ? (
              <span className="text-[var(--color-text-dim)]" data-testid="topology-tab-recon-reason">
                {recon.reason}
              </span>
            ) : null}
          </div>
        )}
      </section>
    </div>
  )
}
