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
import {
  TOPOLOGY_BY_ID,
  type BlueprintTopology,
  type BlueprintTopologyVariant,
} from '@/shared/constants/catalog.generated'

/**
 * #3375 — topology-class vocabulary normaliser.
 *
 * The matrix / blueprint.yaml declare classes with hyphens
 * (`active-hot-standby`); the placement EDITOR + the live Application CR
 * carry the editor vocab (`active-hotstandby`, `single-region`). Map both
 * dialects onto one canonical token so the read-back, the editor's
 * current-mode highlight, and the DR-section gate all agree.
 */
function canonicalTopologyClass(raw: string | null | undefined): string {
  const s = (raw ?? '').trim().toLowerCase()
  switch (s) {
    case 'active-hot-standby':
    case 'active-hotstandby':
    case 'active_hot_standby':
      return 'active-hot-standby'
    case 'active-passive':
    case 'active_passive':
      return 'active-passive'
    case 'active-active':
    case 'active_active':
      return 'active-active'
    case 'singleton':
    case 'single-region':
    case 'single_region':
      return 'singleton'
    default:
      return s || 'singleton'
  }
}

/** Human one-liner per canonical class. */
function describeTopologyClass(klass: string): string {
  switch (klass) {
    case 'active-hot-standby':
      return 'Primary serves; a synchronous hot standby in the second region is ready for near-zero-RTO switchover.'
    case 'active-passive':
      return 'Primary serves; a warm standby in the second region promotes on failure (some replication lag).'
    case 'active-active':
      return 'Both regions serve live traffic; data syncs between them.'
    case 'singleton':
      return 'One instance in one region; no cross-region failover (cluster-local or stateless).'
    default:
      return ''
  }
}

/** Whether a declared class carries a cross-region DR contract. */
function isDRCapableClass(klass: string): boolean {
  return klass === 'active-hot-standby' || klass === 'active-passive' || klass === 'active-active'
}

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
  /**
   * #3656 — true when this app is a bootstrap-kit HelmRelease with NO
   * companion Application CR (the parent AppDetail computes this from
   * `apiApp.bootstrap`; the hero already renders `source: HelmRelease`).
   * The GET /applications/{name}/status endpoint 404s for these — the CR
   * it reads does not exist — so we MUST NOT poll it. Without this gate the
   * Live-status poller hammered the 404 forever (founder #6: a faithful UI
   * never sticks on "Loading…" against a state that can never resolve).
   * Generic by construction — keys on "has Application CR", never a
   * blueprint name.
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
  isBootstrap = false,
}: TopologyTabProps) {
  const qc = useQueryClient()
  const [refreshTick, setRefreshTick] = useState(0)

  // #3656 — bootstrap-kit HelmReleases (bp-alloy/gitea/harbor/openbao/…)
  // have NO Application CR, so the status endpoint 404s forever. Don't poll
  // it for them, and never retry/loop on a 404: a faithful UI renders the
  // calm n/a state below instead of sticking on "Loading…" against a state
  // that can never resolve. `retry: false` + `refetchInterval: false` (when
  // bootstrap) kills the tight 404 loop the founder caught on hw150.
  const statusQ = useQuery({
    queryKey: ['application-status', sovereignId, applicationName, namespace, refreshTick],
    queryFn: () => getApplicationStatus(sovereignId, applicationName, namespace),
    enabled: !initialApp && !isBootstrap && !!sovereignId && !!applicationName,
    refetchInterval: isBootstrap ? false : 10_000,
    retry: false,
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
    const regions = infraQ.data?.topology?.regions ?? []
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

  // Is the Sovereign genuinely multi-region? On a 2-region prov the
  // cross-region default class applies; on a single-region prov every app
  // falls back to its single-region default. Counts distinct *physical*
  // regions from the infra topology (the kom4dc 2-VPC mimic still surfaces
  // two region nodes), and treats the app's own region set as a floor.
  const sovereignRegionCount = useMemo(() => {
    const fromInfra = infraQ.data?.topology?.regions?.length ?? 0
    return Math.max(fromInfra, currentRegions.length)
  }, [infraQ.data, currentRegions])
  const isMultiRegion = sovereignRegionCount > 1

  // #3375 — the app's DECLARED topology, lifted from its blueprint.yaml
  // spec.topology (the same source docs/topology-matrix.md promotes). Keyed
  // on either the `bp-<name>` id or the bare slug — applicationName is the
  // componentId, which is one of those on every route.
  const declaredTopology: BlueprintTopology | undefined = useMemo(() => {
    const key = (applicationName ?? '').trim()
    if (!key) return undefined
    return TOPOLOGY_BY_ID[key] ?? TOPOLOGY_BY_ID[key.replace(/^bp-/, '')] ?? TOPOLOGY_BY_ID[`bp-${key}`]
  }, [applicationName])

  // The class this Sovereign WILL run the app as: the multi-region default
  // on a 2-region prov, else the single-region default — exactly the choice
  // the TopologyPicker + the fan-out controller make. Canonicalised so it
  // matches both dialects.
  const declaredClass = useMemo(() => {
    if (!declaredTopology) return 'singleton'
    const pick = isMultiRegion
      ? declaredTopology.multiRegion ?? declaredTopology.singleRegion ?? declaredTopology.supported[0]
      : declaredTopology.singleRegion ?? declaredTopology.supported[0]
    return canonicalTopologyClass(pick)
  }, [declaredTopology, isMultiRegion])

  // The DR contract variant for the declared class (perTopology entry).
  // perTopology keys use the hyphenated dialect; match canonically.
  const declaredVariant: BlueprintTopologyVariant | undefined = useMemo(() => {
    if (!declaredTopology) return undefined
    for (const [variant, contract] of Object.entries(declaredTopology.perTopology)) {
      if (canonicalTopologyClass(variant) === declaredClass) return contract
    }
    return undefined
  }, [declaredTopology, declaredClass])

  // The live placement the Application CR / status reports (editor dialect).
  const liveClass = useMemo(() => canonicalTopologyClass(currentMode), [currentMode])

  // The class the DR section + region-kill walk operate on. Prefer the live
  // CR placement when it reports a DR-capable class (the CR is the running
  // truth); otherwise fall back to the DECLARED class so bootstrap-kit apps
  // with no Application CR (gitea/harbor/openbao/grafana/keycloak install as
  // bare HelmReleases — getApplicationStatus returns nothing) still surface
  // the DR panel that the matrix asserts. Never invents a class the app
  // doesn't declare.
  const effectiveDRClass = useMemo(() => {
    if (isDRCapableClass(liveClass)) return liveClass
    return declaredClass
  }, [liveClass, declaredClass])
  const showDR = isDRCapableClass(effectiveDRClass)

  // Whether this variant carries an operator-initiated switchover. active-active
  // apps that declare `switchover: none` (e.g. clickhouse/iceberg/vllm — both
  // regions serve, no promotion) get the replication/DR info but NO Switchover
  // button (there's nothing to promote). hot-standby / active-passive always do.
  const switchoverMechanism = declaredVariant?.switchover?.mechanism ?? null
  const hasSwitchover =
    effectiveDRClass === 'active-hot-standby' ||
    effectiveDRClass === 'active-passive' ||
    (!!switchoverMechanism && switchoverMechanism.toLowerCase() !== 'none')

  useEffect(() => {
    // When initialApp updates, just trigger no-op so consumers re-derive.
  }, [initialApp])

  return (
    <div className="topology-tab" data-testid="app-topology-tabpanel">
      {/* #3375 — DECLARED topology read-back. The §1 acceptance: every app
          shows its real declared class + DR mechanism + per-cluster
          placement (from blueprint.yaml spec.topology), not a blank editor.
          Rendered first, above the editor, so the operator reads the
          ground-truth before touching the placement controls. */}
      <DeclaredTopologyPanel
        applicationName={applicationName}
        declaredTopology={declaredTopology}
        declaredClass={declaredClass}
        declaredVariant={declaredVariant}
        liveClass={liveClass}
        isMultiRegion={isMultiRegion}
        sovereignRegionCount={sovereignRegionCount}
      />

      <h3 className="mt-6 mb-2 text-sm font-medium text-[var(--color-text-strong)]">
        Change placement
      </h3>
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
        supportedCanonical={declaredTopology?.supported}
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
        {isBootstrap ? (
          <p className="text-xs text-[var(--color-text-dim)]" data-testid="topology-tab-status-bootstrap">
            n/a — bootstrap component (HelmRelease, no Application CR). Live
            rollout status is tracked via Flux, not the per-region controller.
          </p>
        ) : !app ? (
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
            {effectiveDRClass === 'active-hot-standby' ? '—' : 'n/a (mode)'}
          </div>
          <div data-testid="topology-tab-last-switchover">
            <strong className="text-[var(--color-text)]">Last switchover</strong>:{' '}
            {(app?.status as Record<string, unknown> | undefined)?.lastSwitchover
              ? String((app?.status as Record<string, unknown>).lastSwitchover)
              : '—'}
          </div>
        </div>
      </div>

      {/* EPIC-6 Slice U-DR-1 (#1101) + #3375 — DR section. Renders whenever
          the app's EFFECTIVE class is DR-capable (active-hot-standby /
          active-passive / active-active) — taken from the live Application
          CR when it reports one, else from the DECLARED class so bootstrap-
          kit apps (gitea/harbor/openbao/grafana/keycloak — bare HelmReleases
          with no Application CR) still surface the Switchover control the
          matrix asserts. Composes the live Continuum status, switchover
          button, failback panel, and switchover history. */}
      {showDR ? (
        <DRSection
          sovereignId={sovereignId}
          continuumName={continuumName ?? `dr-${applicationName}`}
          applicationName={applicationName}
          namespace={namespace}
          callerTier={callerTier}
          declaredClass={effectiveDRClass}
          switchoverMechanism={switchoverMechanism}
          hasSwitchover={hasSwitchover}
          disableNetwork={disableNetwork}
        />
      ) : null}
    </div>
  )
}

/* ─── Declared-topology read-back panel (#3375) ──────────────────────── */

interface DeclaredTopologyPanelProps {
  applicationName: string
  declaredTopology: BlueprintTopology | undefined
  declaredClass: string
  declaredVariant: BlueprintTopologyVariant | undefined
  liveClass: string
  isMultiRegion: boolean
  sovereignRegionCount: number
}

/**
 * DeclaredTopologyPanel — the §1 acceptance surface for #3375.
 *
 * Reads back, for THIS app, the declared topology class + DR mechanism +
 * per-cluster placement straight from its blueprint.yaml `spec.topology`
 * (the same source docs/topology-matrix.md promotes). Always rendered — so
 * every app's Topology tab states its real declared posture rather than the
 * generic editor alone. Honest about the live-vs-declared split: when the
 * Sovereign is single-region the cross-region machinery is OFF and the
 * single-region default applies.
 */
function DeclaredTopologyPanel({
  applicationName,
  declaredTopology,
  declaredClass,
  declaredVariant,
  liveClass,
  isMultiRegion,
  sovereignRegionCount,
}: DeclaredTopologyPanelProps) {
  const placement = declaredVariant?.placement
  const replication = declaredVariant?.replication
  const switchover = declaredVariant?.switchover
  const clusters = placement?.clusters ?? []
  const roles = placement?.roles ?? {}

  const classLabel = (k: string): string => {
    switch (k) {
      case 'active-hot-standby':
        return 'active-hot-standby'
      case 'active-passive':
        return 'active-passive'
      case 'active-active':
        return 'active-active'
      default:
        return 'singleton'
    }
  }

  // A "live differs from declared" hint when the running CR reports a class
  // other than what the Sovereign topology implies (e.g. the app was
  // installed single-region on a 2-region prov, or vice-versa).
  const liveDiffers =
    isDRCapableClass(liveClass) && liveClass !== declaredClass

  return (
    <section
      className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] p-4"
      data-testid="topology-tab-declared-panel"
    >
      <div className="mb-2 flex items-baseline justify-between">
        <h3 className="text-sm font-semibold text-[var(--color-text-strong)]">
          Declared topology
        </h3>
        <span
          className={`inline-flex items-center rounded-md px-2.5 py-0.5 text-xs font-semibold ${
            declaredClass === 'singleton'
              ? 'bg-[var(--color-border)]/40 text-[var(--color-text-dim)]'
              : 'bg-[var(--color-accent)]/15 text-[var(--color-accent)]'
          }`}
          data-testid="topology-tab-declared-class"
        >
          {classLabel(declaredClass)}
        </span>
      </div>

      {!declaredTopology ? (
        <p className="text-xs text-[var(--color-text-dim)]" data-testid="topology-tab-declared-none">
          <code className="font-mono">{applicationName}</code> declares no topology contract
          in its Blueprint — it runs as a single instance with no cross-region failover.
        </p>
      ) : (
        <>
          <p className="mb-3 text-xs text-[var(--color-text-dim)]" data-testid="topology-tab-declared-desc">
            {describeTopologyClass(declaredClass)}
          </p>

          <dl className="grid grid-cols-1 gap-x-6 gap-y-1.5 text-xs sm:grid-cols-2">
            <div className="flex justify-between gap-3 sm:block">
              <dt className="text-[var(--color-text-dim)]">Effective class</dt>
              <dd
                className="font-mono text-[var(--color-text)]"
                data-testid="topology-tab-declared-effective"
              >
                {classLabel(declaredClass)}{' '}
                <span className="text-[var(--color-text-dim)]">
                  ({isMultiRegion ? `multi-region · ${sovereignRegionCount} regions` : 'single-region default'})
                </span>
              </dd>
            </div>
            <div className="flex justify-between gap-3 sm:block">
              <dt className="text-[var(--color-text-dim)]">Supported</dt>
              <dd className="font-mono text-[var(--color-text)]" data-testid="topology-tab-declared-supported">
                {declaredTopology.supported.map(classLabel).join(' · ') || '—'}
              </dd>
            </div>
            {placement?.tier ? (
              <div className="flex justify-between gap-3 sm:block">
                <dt className="text-[var(--color-text-dim)]">Tier</dt>
                <dd className="font-mono text-[var(--color-text)]" data-testid="topology-tab-declared-tier">
                  {placement.tier}
                </dd>
              </div>
            ) : null}
            {replication?.backend ? (
              <div className="flex justify-between gap-3 sm:block">
                <dt className="text-[var(--color-text-dim)]">State preservation</dt>
                <dd className="font-mono text-[var(--color-text)]" data-testid="topology-tab-declared-replication">
                  {replication.backend}
                  {replication.mode ? ` · ${replication.mode}` : ''}
                </dd>
              </div>
            ) : null}
            {switchover?.mechanism ? (
              <div className="flex justify-between gap-3 sm:block">
                <dt className="text-[var(--color-text-dim)]">Switchover</dt>
                <dd className="font-mono text-[var(--color-text)]" data-testid="topology-tab-declared-switchover">
                  {switchover.mechanism}
                </dd>
              </div>
            ) : null}
            {switchover && (switchover.rtoSeconds != null || switchover.rpoSeconds != null) ? (
              <div className="flex justify-between gap-3 sm:block">
                <dt className="text-[var(--color-text-dim)]">RTO / RPO</dt>
                <dd className="font-mono text-[var(--color-text)]" data-testid="topology-tab-declared-rto-rpo">
                  {switchover.rtoSeconds != null ? `${switchover.rtoSeconds}s` : '—'} /{' '}
                  {switchover.rpoSeconds != null ? `${switchover.rpoSeconds}s` : '—'}
                </dd>
              </div>
            ) : null}
          </dl>

          {clusters.length > 0 ? (
            <div className="mt-3">
              <p className="mb-1.5 text-[10px] font-semibold uppercase tracking-wide text-[var(--color-text-dim)]">
                Per-cluster placement
              </p>
              <ul className="flex flex-wrap gap-1.5" data-testid="topology-tab-declared-clusters">
                {clusters.map((c) => {
                  const role = roles[c] ?? 'active'
                  return (
                    <li
                      key={c}
                      data-testid={`topology-tab-declared-cluster-${c}`}
                      className={`inline-flex items-center gap-1.5 rounded-md border px-2 py-0.5 text-xs ${
                        role === 'active'
                          ? 'border-green-500/40 bg-green-500/10 text-green-400'
                          : role === 'passive' || role === 'standby'
                            ? 'border-yellow-500/40 bg-yellow-500/10 text-yellow-400'
                            : 'border-[var(--color-border)] text-[var(--color-text-dim)]'
                      }`}
                    >
                      <code className="font-mono">{c}</code>
                      <span className="uppercase text-[10px] font-semibold">{role}</span>
                    </li>
                  )
                })}
              </ul>
            </div>
          ) : null}

          {!isMultiRegion && isDRCapableClass(canonicalTopologyClass(declaredTopology.multiRegion)) ? (
            <p
              className="mt-3 rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-3 py-2 text-[11px] text-[var(--color-text-dim)]"
              data-testid="topology-tab-declared-singleregion-note"
            >
              This Sovereign is single-region, so the cross-region DR machinery is dormant. On a
              2-region Sovereign this app runs as{' '}
              <strong className="text-[var(--color-text)]">
                {classLabel(canonicalTopologyClass(declaredTopology.multiRegion))}
              </strong>{' '}
              and the Switchover panel below activates.
            </p>
          ) : null}

          {liveDiffers ? (
            <p
              className="mt-2 text-[11px] text-yellow-400"
              data-testid="topology-tab-declared-live-differs"
            >
              Live placement on the running instance is{' '}
              <strong>{classLabel(liveClass)}</strong> (differs from the declared default).
            </p>
          ) : null}
        </>
      )}
    </section>
  )
}
