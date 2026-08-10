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

import {
  getApplicationPlacement,
  getApplicationStatus,
  getCatalogItem,
  type CatalogItem,
} from '@/lib/catalog.api'
import {
  getContinuumReplicationStatus,
  lagBucket,
  type ContinuumHealthGate,
  type LagBucket,
} from '@/lib/continuum.api'
import { getHierarchicalInfrastructure } from '@/lib/infrastructure.types'
import { PlacementEditor, type OwnedDependencyInfo } from '@/widgets/topology/PlacementEditor'
import { SwitchoverDialog } from '@/widgets/continuum/SwitchoverDialog'
import {
  type Capability,
  type PlacementTarget,
  type ReconStatus,
  HOST_VCLUSTER,
  PATTERN_NOT_REPORTED,
  derivePattern,
  describePattern,
  normalizeCapability,
  patternLabel,
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
   * True when this app is a bootstrap-kit HelmRelease with NO companion
   * Application CR. Generic by construction — keys on "has Application CR",
   * never a blueprint name.
   *
   * #3656 → #5934: this flag no longer suppresses the status poll. It once
   * did, because GET /applications/{name}/status 404'd for every such
   * component; since #5836 that endpoint answers from the same synthesisers
   * the sibling detail endpoint uses, so the flag now only selects the
   * ABSTENTION COPY shown if the endpoint turns out to have nothing, plus the
   * #4000 empty-namespace placement lookup. It must never again decide WHETHER
   * to ask.
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

/**
 * PlacementSource — where the rendered target list came from (#5568).
 *
 *   runtime  — the /placement endpoint reported `derivedFromRuntime: true`
 *              AND returned targets: a genuine live observation.
 *   declared — the targets came from `spec.placement` / `status.perCluster` /
 *              the legacy mode+regions projection, or from a /placement
 *              response that admitted no runtime was consulted. Desired
 *              state, not observed state.
 *   none     — no targets from any source; the pattern is `not reported`.
 *
 * The distinction is the whole point of the endpoint's `derivedFromRuntime`
 * field, which #5568 found hardcoded `true` server-side. That half is fixed;
 * this type is the consumer that makes the flag mean something — without it
 * a declared 2-region pair and an observed 2-region pair render identically.
 */
type PlacementSource = 'runtime' | 'declared' | 'none'

/** The chip copy per provenance. Never prints a source it cannot back. */
const PLACEMENT_SOURCE_LABEL: Record<PlacementSource, string> = {
  runtime: 'runtime-observed',
  declared: 'declared · not observed',
  none: 'no source',
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
  // #4552 — the armed manual-switchover confirm dialog (opened from the DR
  // panel's "Switch over…" button). Closed by default.
  const [showSwitchover, setShowSwitchover] = useState(false)

  // #3656 → #5934. #3656 disabled this poll for bootstrap-kit components
  // because they have no Application CR and the endpoint 404'd forever. The
  // 404-loop worry was right; "never ask" was too blunt an answer, and it
  // outlived its cause: since #5836 GET /applications/{name}/status falls
  // through to the same synthesisers the sibling detail endpoint uses (the
  // HelmRelease first, then the live runtime), so bp-grafana and bp-keycloak
  // DO have a status to report — the console simply never asked for it, and
  // printed "n/a — bootstrap component" on a page whose own header read Ready
  // (UAT rows 67, 69).
  //
  // So: ask for every component, and keep #3656's actual concern by backing
  // off the moment the endpoint tells us it has nothing (retry:false + the
  // error-aware interval below). One 404 per mount, never a loop.
  const statusEnabled = !initialApp && !!sovereignId && !!applicationName
  const statusQ = useQuery({
    queryKey: ['application-status', sovereignId, applicationName, namespace, refreshTick],
    queryFn: () => getApplicationStatus(sovereignId, applicationName, namespace),
    enabled: statusEnabled,
    refetchInterval: (query) => (query.state.status === 'error' ? false : 10_000),
    retry: false,
  })

  const app: ApplicationStatus | undefined = initialApp ?? statusQ.data

  // Has the status question been ANSWERED yet? Distinct from "is there an
  // answer": the Status panel below must not render its abstention prose while
  // the request is still in flight, or every bootstrap component flashes
  // "no status" before its real one arrives. A disabled query counts as
  // settled — nothing is coming.
  const statusSettled = !statusEnabled || statusQ.isSuccess || statusQ.isError

  // #3982 — the REAL placement, derived from where the component's
  // workloads actually run across BOTH region clusters. This is the
  // authoritative source for the target cards + the derived Pattern; it is
  // what makes grafana/keycloak (running in 2 regions) render 2 targets
  // instead of the old uniform false `singleton`. Generic: works for
  // bootstrap HelmReleases (no Application CR) AND wizard installs, because
  // the backend keys on live Pods. Falls back silently (null) to the legacy
  // spec/status projection below when the data plane is unavailable.
  // #4000: a bootstrap-kit HelmRelease lives in flux-system, but its WORKLOAD
  // Pods run in their own targetNamespace (e.g. bp-alloy → ns `alloy`, see
  // 21-alloy.yaml `targetNamespace: alloy`). AppDetail passes the HR namespace
  // (flux-system) as `namespace`, and the placement endpoint filters Pods by a
  // NON-EMPTY namespace — so the HR namespace excludes EVERY workload Pod → 0
  // targets → a false "No placement targets reported yet" even on a converged
  // multi-region prov (caught live on hw174, where bp-alloy showed singleton/
  // no-targets while the data plane held its region-a+region-b DaemonSet pods).
  // The endpoint documents EMPTY namespace as the safe default for bootstrap
  // components (match across all namespaces); honour that so bp-alloy et al.
  // resolve their real active-active targets. Wizard installs (non-bootstrap)
  // keep their namespace — no regression.
  const placementNamespace = isBootstrap ? undefined : namespace
  const placementQ = useQuery({
    queryKey: ['application-placement', sovereignId, applicationName, placementNamespace, refreshTick],
    queryFn: () => getApplicationPlacement(sovereignId, applicationName, placementNamespace),
    enabled: !disableNetwork && !!sovereignId && !!applicationName,
    refetchInterval: 30_000,
    retry: false,
  })
  const runtimeTargets: PlacementTarget[] = useMemo(() => {
    const t = placementQ.data?.targets
    return Array.isArray(t) ? (t as PlacementTarget[]) : []
  }, [placementQ.data])

  // #5568 — `derivedFromRuntime` is the endpoint's PROVENANCE flag: true only
  // when the live k8s cache was actually queried. Until now NOTHING in the
  // frontend read it (grep: one type declaration in catalog.api.ts and a pile
  // of test mocks, zero production consumers), so the field asserted a
  // discrimination the UI never made — the same "carries zero information"
  // defect the server-side hardcode had, moved one hop downstream.
  //
  // It is load-bearing because the fallback chain below is keyed on
  // EMPTINESS, not on provenance: whenever the runtime reports no targets the
  // panel silently projects `spec.placement` / `status.perCluster` instead and
  // renders the result identically. So a DECLARED intention and an OBSERVED
  // fact reach the same Pattern chip with nothing to tell them apart — which
  // is how a 2-region pair renders over a 1-region runtime (#5513).
  //
  // Null response = the endpoint was unreachable (getApplicationPlacement
  // returns null on !res.ok), which is not a runtime observation either.
  const runtimeWasConsulted = placementQ.data?.derivedFromRuntime === true

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

  // The target list rendered by the Placement panel + fed to derivePattern.
  // Priority (#3982):
  //   1. RUNTIME-derived targets — where the component's workloads ACTUALLY
  //      run, across both region clusters. Authoritative; this is what makes
  //      grafana/keycloak show 2 targets instead of a false `singleton`.
  //   2. spec.placement.targets[] — the #3969 desired-state the operator
  //      explicitly chose in the editor (used pre-rollout / when the data
  //      plane is unavailable).
  //   3. legacy status.targets / mode+regions projection.
  //
  // #5568 — the resolver now also reports WHICH rung it landed on, because the
  // rung is the provenance: rung 1 is observed, rungs 2 and 3 are declared.
  // The panel renders that distinction instead of presenting all three the
  // same way.
  const resolved: { targets: PlacementTarget[]; source: PlacementSource } = useMemo(() => {
    // Rung 1 is only an OBSERVATION when the endpoint says a runtime was
    // actually consulted (#5568). If it reports derivedFromRuntime:false the
    // targets did not come from live state, so they are not treated as such.
    if (runtimeTargets.length > 0 && runtimeWasConsulted) {
      return { targets: runtimeTargets, source: 'runtime' as const }
    }
    const declared = (t: PlacementTarget[]) => ({ targets: t, source: 'declared' as const })
    if (runtimeTargets.length > 0) return declared(runtimeTargets)
    const specPlacement = app?.spec?.placement
    if (specPlacement && typeof specPlacement === 'object') {
      const t = (specPlacement as Record<string, unknown>).targets
      if (Array.isArray(t) && t.length > 0) return declared(t as PlacementTarget[])
    }
    // Legacy fallback: project from status.targets (recon rollup), else
    // from the legacy mode + regions.
    const status = (app?.status ?? {}) as Record<string, unknown>
    // 2b. status.targets — the controller's own placement TARGET LIST.
    //
    //     #5420 put the `status.perCluster` projection ahead of the LEGACY
    //     `mode + regions` fabrication, and that ordering is still right (see
    //     2c below). But the rung it was placed ahead of was not the only one
    //     underneath it: `status.targets` sat below the perCluster rung too,
    //     and the perCluster rung RETURNS. So a perCluster observation that
    //     saw one cluster shadowed a status.targets that named two — and the
    //     panel printed `singleton` over data on the SAME object that said
    //     active-hot-standby (UAT row 63, hw293: one region-A card, no DR
    //     block, while the Status panel beside it read "Standby·Hot is
    //     reconciling").
    //
    //     These two fields are not the same kind of thing:
    //       • status.targets   — the placement, per target, with role AND
    //         standbyType. Written by the controller from the desired
    //         placement.
    //       • status.perCluster — an OBSERVATION of clusters where workloads
    //         were seen. Its role vocabulary is primary/active/singleton only;
    //         it carries no standbyType and no vCluster, and a Standby whose
    //         Pods have not landed yet is simply ABSENT from it.
    //
    //     An incomplete observation must not overwrite the placement it is an
    //     observation OF. So targets is read first, and perCluster remains the
    //     answer whenever targets is absent or empty. Both rungs are labelled
    //     `declared` either way, so this changes WHICH declared source is
    //     rendered, never whether a declared source is passed off as observed.
    const statusTargetsFirst = status.targets
    if (Array.isArray(statusTargetsFirst) && statusTargetsFirst.length > 0) {
      return declared(statusTargetsFirst as PlacementTarget[])
    }
    // 2c. #5420 — EFFECTIVE per-cluster state, ahead of any projection from
    //     the DECLARED posture. `status.perCluster` says where this app's
    //     workloads were actually observed; the legacy `mode + regions`
    //     projection below maps over every DECLARED region and therefore
    //     fabricates a Standby card for a region that may hold nothing.
    //     That is how a single-region app rendered 2 cards, a DR panel and
    //     an ARMED Switchover pointing at a region with no namespace, while
    //     its own DR chip read "NOT LIVE" on the same screen.
    //
    //     Same class as #5515 (empty targets → false `singleton`) and #5422
    //     (absent placement → asserted `singleton`): never let a declared
    //     intention masquerade as an observed fact.
    const perCluster = status.perCluster
    if (Array.isArray(perCluster) && perCluster.length > 0) {
      const effective = perCluster
        .map((entry) => {
          const e = (entry ?? {}) as Record<string, unknown>
          const cluster = typeof e.cluster === 'string' ? e.cluster : ''
          if (!cluster) return null
          const role = (typeof e.role === 'string' ? e.role : '').toLowerCase()
          // `singleton` is the controller's own word for "one place, no
          // standby" — it must NOT become a Standby card.
          const isPrimary = role === 'primary' || role === 'active' || role === 'singleton'
          return {
            region: cluster,
            cluster,
            // #5945 — this used to stamp `vcluster: 'mgmt'` on EVERY
            // per-cluster-derived target. #4325 deleted bp-mgmt-vcluster (and
            // the dmz/rtz siblings), so no post-#4325 Sovereign has an `mgmt`
            // namespace — the 45-namespace census on hw292 shows zero, and the
            // only vClusters are per-Org. The value was pure fabrication:
            // `status.perCluster` reports a CLUSTER, never a vCluster.
            // It was also load-bearing downstream, which is how #5945 was
            // measured: these targets seed the PlacementEditor, whose
            // `optionsForTarget` re-admits whatever the current target names —
            // so the fabricated 'mgmt' both PRESELECTED a retired vCluster and
            // put it back in the select that #5616 had already narrowed to
            // ['host']. Report no vCluster instead; `host` is the renderer's
            // honest default and the one tier the controller can always resolve.
            role: isPrimary ? ('Primary' as const) : ('Standby' as const),
            ...(isPrimary ? {} : { standbyType: 'Hot' as const }),
          } as PlacementTarget
        })
        .filter((t): t is PlacementTarget => t !== null)
      if (effective.length > 0) return declared(effective)
    }
    const statusRegions = (status.regions ?? []) as RegionStatus[]
    // status.placement is an OBJECT on a real (#3373/#3969) controller; only
    // a pre-#3969 controller wrote the legacy posture string here.
    const mode =
      typeof specPlacement === 'string'
        ? specPlacement
        : typeof status.placement === 'string'
          ? status.placement
          : ''
    const regions = Array.isArray(app?.spec?.regions) ? app!.spec!.regions! : statusRegions.map((r) => r.name)
    const legacy = targetsFromLegacy({ mode, regions, statusRegions })
    // Nothing anywhere — an empty list is neither observed nor declared, and
    // derivePattern renders it as `not reported` (#5515). Labelling it
    // "declared" would assert a desired state that was never written.
    return legacy.length > 0 ? declared(legacy) : { targets: legacy, source: 'none' as const }
  }, [app, runtimeTargets, runtimeWasConsulted])

  const targets: PlacementTarget[] = resolved.targets
  const placementSource: PlacementSource = resolved.source

  const pattern = useMemo(() => derivePattern(targets), [targets])

  // ── DR / replication telemetry (#3375 rows 51/52/56) ──────────────
  //
  // A component is DR-capable (has a cross-region replica) when its
  // placement carries at least one Standby target alongside a Primary —
  // i.e. it is NOT a singleton. For those, the live DR telemetry lives on
  // the per-app Continuum CR (`dr-<app>` by the application-controller's
  // convention, core/controllers/application/.../continuum.go
  // ContinuumNameFor) and is surfaced by catalyst-api's
  // /continuum/{name}/replication-status. We render it READ-ONLY: standby
  // region(s) + the live WAL replication lag in seconds. Honestly hidden
  // for singletons (no DR block against a phantom region — row 58).
  const hasStandby = useMemo(
    () => targets.some((t) => t.role === 'Standby'),
    [targets],
  )
  const continuumName = useMemo(() => `dr-${applicationName}`, [applicationName])

  // Poll the DR endpoint for every networked app and gate rendering on a
  // genuine live cross-region result. Three cases the poll covers:
  //   • App-CR apps with a Standby placement target (hasStandby, the #3375 path).
  //   • #4886 — Continuum-backed BOOTSTRAP-HR apps (spine-keycloak/gitea/harbor/
  //     openbao, cnpg-pair). These have NO Application CR and their Pods run
  //     active-active across both regions, so the placement projection can't
  //     advertise a Standby target → hasStandby is false even though the live
  //     `continuums.dr.openova.io` CR carries the real active/standby + lag.
  //   • #4552 — an app whose placement projection reports a FALSE singleton
  //     (e.g. shared-pg, whose CNPG replica half the projection doesn't surface
  //     as a Standby target). hasStandby is false and it is not a bootstrap
  //     component, yet a live 2-region cnpg-pair genuinely backs it. Polling
  //     unconditionally lets the DR panel render it as a pair with the
  //     switchover control instead of a stale singleton.
  // #5514 — the endpoint does NOT 404 for an unbacked app: it answers HTTP 200
  // with the `source:"pending"` fallback envelope. So the panel's CONTENT is
  // gated on `drBacked` (source === "live") below, never on `drQ.isError`.
  // A real transport error does not retry and leaves drStatus undefined, which
  // lands on the same unbacked branch.
  // Bootstrap components query with an EMPTY namespace so the cluster-wide
  // lookup finds the CR in its own namespace (the HR lives in flux-system, the
  // Continuum in the spine's namespace) — mirrors the #4000 placement rule.
  const drNamespace = isBootstrap ? undefined : namespace
  const drQ = useQuery({
    queryKey: ['continuum-replication-status', sovereignId, continuumName, drNamespace, refreshTick],
    queryFn: () => getContinuumReplicationStatus(sovereignId, continuumName, { namespace: drNamespace }),
    enabled: !disableNetwork && !!sovereignId && !!applicationName,
    refetchInterval: 30_000,
    retry: false,
  })
  const drStatus = drQ.data

  // The primary + standby regions for the DR cards. Prefer the live
  // replication-status (authoritative current primary after a switchover);
  // fall back to the placement targets when the endpoint is unavailable.
  const drPrimaryRegion = useMemo(
    () =>
      drStatus?.currentPrimary ||
      drStatus?.primaryRegion ||
      targets.find((t) => t.role === 'Primary')?.region ||
      '',
    [drStatus, targets],
  )
  const drStandbyRegions = useMemo(
    () =>
      Array.from(
        new Set(
          targets
            .filter((t) => t.role === 'Standby' && t.region && t.region !== drPrimaryRegion)
            .map((t) => t.region),
        ),
      ),
    [targets, drPrimaryRegion],
  )

  // Live WAL replication lag in seconds (row 56). Undefined when the DR
  // endpoint hasn't resolved yet — rendered as "—" with a "measuring…"
  // hint, NEVER a hardcoded zero passed off as a real reading.
  const lagSeconds: number | null = useMemo(
    () => (typeof drStatus?.walLagSeconds === 'number' ? drStatus.walLagSeconds : null),
    [drStatus],
  )
  const lagColor: LagBucket = useMemo(() => lagBucket(lagSeconds), [lagSeconds])

  // ── #5508 — the healthGates verdict QUALIFIES the lag reading ─────
  //
  // The API already says when the measurement is unverified (hw291:
  // walLagSeconds:0 alongside streaming-replication=Warn "unverified" +
  // standby-available=Warn "not verifiable") — this tab used to render the
  // number behind a green pill and throw both gates away, making the one
  // surface an operator reads during a DR decision strictly LESS honest
  // than the API behind it. Same defect class as the #5478 controller
  // guard: a control that reports instead of measuring. Two rules:
  //
  //   • degradedGates — every Warn/Fail gate renders as an explicit row
  //     under the lag line, and the lag pill can never render green while
  //     any is present (a Fail forces red, a Warn caps the bucket at
  //     yellow).
  //   • lagUnverified — the gates that qualify the MEASUREMENT chain mark
  //     the number itself meaningless; render "— unverified", never a
  //     numeric zero. Derived structurally, no message-text matching:
  //       - streaming-replication Warn → replication health unverified.
  //       - standby-available    Warn → the standby leg is unverifiable; a
  //         lag against an unverifiable leg is not a measurement (the
  //         reductio on hw291: dr-spine-openbao, a raft store with no
  //         PostgreSQL at all, reported a PostgreSQL lag of 0).
  //       - wal-lag-under-rpo    Warn with lag <= 30 → the backend's
  //         not-reported branch (walLagHealthGate's over-threshold branch
  //         requires lag > 30, and THAT genuine reading must keep
  //         rendering numerically).
  //     A Fail is a VERIFIED fault: the number stays (a real reading of a
  //     broken state) behind a red pill.
  //
  // An absent/empty healthGates list (older backend) leaves every render
  // exactly as before — verified readings are untouched.
  const healthGates: ContinuumHealthGate[] = useMemo(() => {
    const g = drStatus?.healthGates
    return Array.isArray(g) ? g : []
  }, [drStatus])
  const degradedGates = useMemo(
    () =>
      healthGates.filter((g) => {
        const s = (g.status ?? '').toLowerCase()
        return s === 'warn' || s === 'fail'
      }),
    [healthGates],
  )
  const anyGateFail = useMemo(
    () => degradedGates.some((g) => (g.status ?? '').toLowerCase() === 'fail'),
    [degradedGates],
  )
  const lagUnverified = useMemo(
    () =>
      healthGates.some((g) => {
        if ((g.status ?? '').toLowerCase() !== 'warn') return false
        if (g.name === 'streaming-replication' || g.name === 'standby-available') return true
        return g.name === 'wal-lag-under-rpo' && (lagSeconds == null || lagSeconds <= 30)
      }),
    [healthGates, lagSeconds],
  )
  const effectiveLagColor: LagBucket = useMemo(() => {
    if (lagUnverified) return 'unknown'
    if (anyGateFail) return 'red'
    if (degradedGates.length > 0 && lagColor === 'green') return 'yellow'
    return lagColor
  }, [lagUnverified, anyGateFail, degradedGates, lagColor])

  // #4886 / #4552 — the DR section renders for App-CR apps with a Standby
  // placement target (the existing #3375 hasStandby path) AND for ANY app whose
  // LIVE continuum status confirms a genuine cross-region pair: source:"live"
  // plus a standby region distinct from the active/lease-holder region. This
  // covers bootstrap-HR spine apps (#4886) and App-CR apps whose placement
  // projection reported a false singleton (#4552 — shared-pg). An app with no
  // live pair (404 → synthesized/pending shape, or a same-region echo) never
  // renders DR, so we never arm a phantom region.
  const liveStandbyRegion = useMemo(
    () =>
      drStatus?.replicas?.find(
        (rep) => rep.role !== 'primary' && !!rep.region && rep.region !== drPrimaryRegion,
      )?.region || '',
    [drStatus, drPrimaryRegion],
  )
  const hasLiveDR = drStatus?.source === 'live' && !!liveStandbyRegion

  // #5514 — THE DR-BACKING GATE. The replication-status endpoint does NOT 404
  // for an app with no Continuum CR: catalyst-api answers HTTP 200 carrying
  // `pendingReplicationStatus()` (continuum_dr_extras.go) — a fallback envelope
  // with `source:"pending"`, `replicaPromotable:false`, `walLagSeconds:0`, and
  // a `replicas[]` entry synthesized from the Sovereign's CONFIGURED region env
  // (SOVEREIGN_REPLICA_REGION), not from an observed replica. So `drQ.isError`
  // never fires, and every downstream render read that envelope as real: hw291
  // showed `Replication lag 0.0 s`, a "hot replica (follows WAL)" card, and an
  // ARMED "Switch over…" for `uatcorp/uatwalk-ahs-07300830` — an Application
  // declaring active-hot-standby whose standby region has no namespace at all.
  //
  // `source === 'live'` is the backend's own honesty flag and the ONLY value
  // that means "read off the real Continuum + CNPGPair CR status". Nothing
  // numeric and no control may render without it. The server is already honest
  // here; it was the client that ignored the flag.
  const drBacked = drStatus?.source === 'live'

  const showDR = hasStandby || hasLiveDR

  // #4923/#4901 — the EXPLICIT standby-absent condition. The backend verifies
  // the standby leg off the live cnpg cluster-pair and reports the tri-state
  // standbyAvailable verdict: false means the required hot-standby is
  // unreachable (region-kill / outage) and MUST render as a fault — never as
  // a calm "hot replica (follows WAL)" card. undefined = unverifiable
  // (unknown), which renders the normal card without a false health claim.
  //
  // UAT row 62 — READ WHAT THE BACKEND ACTUALLY EMITS. The tri-state bool is
  // `omitempty`, so it is ABSENT from the payload whenever the standby leg is
  // undetermined, and it is not the only place the verdict is published: the
  // same backend upserts a `standby-available` health gate on EVERY branch
  // (Pass / Warn / Fail). Keying the red banner on the bool alone meant a
  // response whose own gate said Fail rendered no banner at all. A Fail gate is
  // a VERIFIED fault by construction (the backend never emits Fail without
  // positive evidence of a lost leg), so it must arm the banner too.
  const standbyGate = useMemo(
    () => healthGates.find((g) => g.name === 'standby-available'),
    [healthGates],
  )
  const standbyGateStatus = (standbyGate?.status ?? '').toLowerCase()
  const standbyAbsent = drStatus?.standbyAvailable === false || standbyGateStatus === 'fail'

  // The standby card's health line. Row 62's clause is "not a static badge",
  // and this line was `drStatus?.streamingState || 'hot replica (follows WAL)'`
  // against a backend that returns `streamingState: ""` for every
  // Continuum-CR-derived reading — so in practice it was a HARDCODED green
  // claim, printed unchanged over a standby the API had just declared
  // unverifiable. Derive it from the evidence instead, and never assert
  // "follows WAL" without a reading that says so.
  const standbyHealthLine = useMemo(() => {
    if (standbyAbsent) return 'standby unreachable — replication interrupted'
    if (drStatus?.streamingState) return drStatus.streamingState
    if (standbyGateStatus === 'pass') return 'standby leg verified reachable'
    if (standbyGateStatus === 'warn') return 'standby state not verified'
    return 'standby state not reported'
  }, [standbyAbsent, drStatus, standbyGateStatus])

  // The switchover target — the standby region we would promote. Prefer the
  // placement Standby target, fall back to the live continuum standby region.
  const switchoverTarget = useMemo(
    () => drStandbyRegions[0] || liveStandbyRegion || '',
    [drStandbyRegions, liveStandbyRegion],
  )

  // #5514 — arming the switchover needs THREE independent facts, not just a
  // region string. `switchoverTarget` alone is a DECLARED region name, which on
  // hw291 pointed at a region with no namespace: a string is not a standby.
  //
  //   1. drBacked            — the reading is live, not the pending envelope.
  //   2. replicaPromotable   — the backend's positive verdict. It DOES return
  //                            this (`false` for the phantom) and it was never
  //                            consulted. `undefined` = unverified, which stays
  //                            armed: the SwitchoverDialog then runs its own
  //                            read-only RPO/health preflight and only arms
  //                            [ Confirm Switchover ] on `promotable`. Only an
  //                            EXPLICIT `false` disarms here.
  //   3. !standbyAbsent      — a VERIFIED-absent standby (#4923/#4901) has no
  //                            leg to promote.
  const replicaPromotable = drStatus?.replicaPromotable
  const switchoverArmed =
    drBacked && !!switchoverTarget && replicaPromotable !== false && !standbyAbsent

  // The plain reason the control is not armed — the operator must never see a
  // dead button with no explanation.
  const switchoverBlockedReason = useMemo(() => {
    if (switchoverArmed) return ''
    if (!switchoverTarget) return 'resolving standby region…'
    if (standbyAbsent) return 'standby unreachable — nothing to promote'
    if (replicaPromotable === false) return 'the replica is not promotable — no caught-up standby to promote'
    return 'no live replication reading — cannot promote'
  }, [switchoverArmed, switchoverTarget, standbyAbsent, replicaPromotable])

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
  //
  // #3969 §7.4: the application-controller writes the ONE recon value to
  // `status.placementRecon` (a string) + a plain `status.placementReason`.
  // That field is distinct from the legacy `status.placement` OBJECT
  // ({vcluster, source, regions}) so the two never collide. We prefer the
  // dedicated recon field; legacy string placement + phase remain a
  // fallback for pre-#3969 controllers. There is NEVER a second
  // contradictory class anywhere here.
  const recon: { status: ReconStatus; reason: string } = useMemo(() => {
    const status = (app?.status ?? {}) as Record<string, unknown>
    // The dedicated recon field wins; its reason is the plain operator
    // string, falling back to the generic status.reason.
    const reason = (status.placementReason as string) || (status.reason as string) || ''
    const recon = typeof status.placementRecon === 'string' ? status.placementRecon : ''
    // The legacy `status.placement` is an object on a real controller; only
    // treat it as a recon string on the (pre-#3969) string form.
    const legacyPlacement = typeof status.placement === 'string' ? status.placement : ''
    const v = (recon || legacyPlacement).toLowerCase()
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

  // #5945 — the vCluster options the placement editor may offer, derived from
  // the OBSERVED topology instead of a hardcoded tier vocabulary. Each live
  // vCluster is identified by its HOST NAMESPACE (`VClusterSpec.namespace`,
  // the real placement identity per #5616 — the per-Org vCluster is *named*
  // "vcluster" but *runs* in the Org namespace, so the display name is not
  // addressable). `host` is always offered: it is the one tier the
  // application-controller resolves without a dedicated namespace.
  //
  // Before this, TopologyTab passed NO vCluster list at all, so the editor fell
  // back to ['host'] and the only other entry it ever showed was whatever the
  // current target named — which the perCluster projection above hardcoded to
  // the #4325-deleted `mgmt`. Net effect measured on hw292: the select offered
  // retired tiers, preselected one of them, and the per-Org vClusters that DO
  // exist were not offered at all.
  const availableVClusters = useMemo(() => {
    const set = new Set<string>([HOST_VCLUSTER])
    for (const r of infraQ.data?.topology?.regions ?? []) {
      const clusters = (r as unknown as { clusters?: Array<{ vclusters?: Array<{ namespace?: string }> }> })
        .clusters ?? []
      for (const c of clusters) {
        for (const vc of c.vclusters ?? []) {
          const ns = (vc.namespace ?? '').trim()
          if (ns) set.add(ns)
        }
      }
    }
    return Array.from(set)
  }, [infraQ.data])

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
          {/* #5515 — an un-derivable pattern renders as prose ("not reported")
              in the DIM colour, never a pattern NAME in the accent colour. The
              pre-fix code printed a confident `singleton` next to this panel's
              own "No placement targets reported yet." note. */}
          <span className="text-xs text-[var(--color-text-dim)]">
            Pattern:{' '}
            <span
              className={
                pattern === PATTERN_NOT_REPORTED
                  ? 'font-semibold text-[var(--color-text-dim)] italic'
                  : 'font-semibold text-[var(--color-accent)]'
              }
              data-testid="topology-tab-pattern"
              title={describePattern(pattern)}
            >
              {patternLabel(pattern)}
            </span>
            {/* #5568 — the PROVENANCE of the targets this pattern was derived
                from. `derivedFromRuntime` exists to make exactly this
                distinction and had no consumer, so a declared 2-region pair
                and an observed one rendered identically. Only the runtime rung
                is allowed to read as observed. */}
            {pattern !== PATTERN_NOT_REPORTED && (
              <>
                {' · '}
                <span
                  className={
                    placementSource === 'runtime'
                      ? 'text-[var(--color-text-dim)]'
                      : 'font-semibold text-yellow-400'
                  }
                  data-testid="topology-tab-placement-source"
                  title={
                    placementSource === 'runtime'
                      ? 'Derived from live runtime state observed in the region clusters.'
                      : 'Projected from the DECLARED placement (spec/status). No live runtime observation backs this — it is desired state, not observed state.'
                  }
                >
                  {PLACEMENT_SOURCE_LABEL[placementSource]}
                </span>
              </>
            )}
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
                        {/* #5945 — the fallback was `mgmt`, a vCluster #4325
                            deleted, so a target that named no vCluster was
                            DISPLAYED as living in one that cannot exist. */}
                        {t.region} · {t.cluster} · {t.vcluster || HOST_VCLUSTER}
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
            availableVClusters={availableVClusters}
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

      {/* ── DR / replication (#3375 rows 51/52/56/57) ────────────────────
          READ-ONLY cross-region DR telemetry. Rendered ONLY for apps whose
          placement carries a Standby target (a live cross-region replica);
          honestly absent for singletons so we never arm a DR block against
          a phantom region (row 58). #4886 also renders it for Continuum-backed
          bootstrap-HR apps off the live continuum status. */}
      {showDR ? (
        <section
          className="mt-4 rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] p-4"
          data-testid="topology-tab-dr-panel"
        >
          <div className="mb-3 flex items-baseline justify-between">
            <h3 className="text-sm font-semibold text-[var(--color-text-strong)]">
              Disaster recovery
            </h3>
            {drStatus ? (
              <span
                className={`rounded px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-wide ${
                  drStatus.source === 'live'
                    ? 'bg-green-500/10 text-green-400'
                    : 'bg-yellow-500/10 text-yellow-400'
                }`}
                data-testid="topology-tab-dr-source"
                title={
                  drStatus.source === 'live'
                    ? 'Read live off the Continuum + CNPGPair CR status.'
                    : 'Fallback shape — the in-cluster client was bootstrapping or the DR CRs were not found. Not a live reading.'
                }
              >
                {drStatus.source === 'live' ? '● live' : '○ not live'}
              </span>
            ) : null}
          </div>

          {!drBacked ? (
            // #5514 — NO LIVE BACKING. Reached on all three unbacked shapes:
            // the endpoint erroring, the `source:"pending"` fallback envelope
            // (the hw291 phantom — HTTP 200, NOT an error, which is why the old
            // `drQ.isError` condition here was dead code), and the still-loading
            // first paint. Renders the honest note and NOTHING else: no standby
            // card, no numeric lag, no switchover control. A DR control must
            // never arm against a region we have not observed.
            <div data-testid="topology-tab-dr-none">
              <p className="text-xs text-[var(--color-text-dim)]">
                {drQ.isLoading
                  ? 'Checking for a cross-region replica…'
                  : 'No cross-region replica reporting yet for this component.'}
              </p>
              {hasStandby && drStatus && !drQ.isLoading ? (
                <p
                  className="mt-2 text-xs text-yellow-400"
                  data-testid="topology-tab-dr-unbacked"
                >
                  This placement DECLARES a standby, but the replication endpoint
                  returned a fallback reading (source: {drStatus.source}) — no live
                  Continuum / cnpg pair backs it. Replication lag and switchover
                  stay unavailable until a real replica reports.
                </p>
              ) : null}
              {/* UAT row 63 — the switchover control must render DISABLED here,
                  not vanish. The clause is "renders an honest no-backing DR
                  section: 'Switchover unavailable' copy, the control DISABLED,
                  and NO synthesized lag / standby / target-region values", and
                  the 2026-08-10 walk could not verify the middle clause because
                  NO switchover element existed on the page at all — an absent
                  control and a disabled one are not the same statement, and only
                  the second one tells the operator that switchover is a thing
                  this app has and cannot currently do.
                  It is rendered inside the !drBacked branch precisely so it can
                  never arm: there is no `onClick`, no target region and no
                  numeric value anywhere in it — the row's third clause holds by
                  construction. */}
              {!drQ.isLoading ? (
                <div
                  className="mt-3 flex items-center gap-2"
                  data-testid="topology-tab-dr-switchover"
                >
                  <button
                    type="button"
                    className="rounded-md border border-[var(--color-border)] px-3 py-1.5 text-xs disabled:cursor-not-allowed disabled:opacity-50"
                    disabled
                    data-testid="topology-tab-dr-switchover-open"
                  >
                    Switch over…
                  </button>
                  <span
                    className="text-[10px] text-[var(--color-text-dim)]"
                    data-testid="topology-tab-dr-switchover-reason"
                  >
                    Switchover unavailable — no live Continuum backs this
                    placement, so there is no standby to promote.
                  </span>
                </div>
              ) : null}
            </div>
          ) : (
            <>
              {/* UAT row 62 — the LIVE Continuum status: reconciled phase +
                  which region holds the witness lease + when that lease
                  expires. The clause is "the live Continuum status (Ready /
                  lease holder / standby) from the live API, not a static
                  badge"; the panel previously rendered none of it, because the
                  replication-status endpoint read `status.leaseHolder`
                  internally and never put it (or `status.phase`) on the wire.
                  Each cell renders ONLY when the API reports it — an absent
                  phase prints nothing rather than a green-looking default. */}
              {drStatus?.phase || drStatus?.leaseHolder ? (
                <div
                  className="mb-3 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs"
                  data-testid="topology-tab-dr-continuum-status"
                >
                  {drStatus?.phase ? (
                    <span
                      className={`inline-flex items-center gap-1.5 rounded-md px-2 py-0.5 font-semibold ${
                        drStatus.phase === 'Healthy'
                          ? 'bg-green-500/10 text-green-400'
                          : drStatus.phase === 'Degraded' || drStatus.phase === 'Failed'
                            ? 'bg-red-500/10 text-red-400'
                            : 'bg-yellow-500/10 text-yellow-400'
                      }`}
                      data-testid="topology-tab-dr-phase"
                      title="The Continuum CR's reconciled phase, read live off the DR API."
                    >
                      ● {drStatus.phase}
                    </span>
                  ) : null}
                  {drStatus?.leaseHolder ? (
                    <span className="text-[var(--color-text-dim)]">
                      lease held by{' '}
                      <span
                        className="font-mono text-[var(--color-text)]"
                        data-testid="topology-tab-dr-lease-holder"
                      >
                        {drStatus.leaseHolder}
                      </span>
                      {drStatus?.leaseExpiresAt ? (
                        <span data-testid="topology-tab-dr-lease-expires">
                          {' '}
                          · renews {drStatus.leaseExpiresAt}
                        </span>
                      ) : null}
                    </span>
                  ) : null}
                </div>
              ) : null}

              <div
                className="flex flex-wrap items-stretch gap-2"
                data-testid="topology-tab-dr-regions"
              >
                <div
                  className="min-w-[11rem] rounded-md border border-green-500/40 bg-green-500/10 px-3 py-2"
                  data-testid="topology-tab-dr-primary"
                >
                  <div className="text-[10px] uppercase tracking-wide text-[var(--color-text-dim)]">
                    Primary region
                  </div>
                  <div className="mt-0.5 font-mono text-xs font-semibold text-green-400">
                    {drPrimaryRegion || '—'}
                  </div>
                  <div className="text-[10px] text-[var(--color-text-dim)]">serves writes</div>
                </div>
                {drStandbyRegions.length > 0 ? (
                  drStandbyRegions.map((region) => (
                    <div
                      key={region}
                      className={`min-w-[11rem] rounded-md border px-3 py-2 ${
                        standbyAbsent
                          ? 'border-red-500/40 bg-red-500/10'
                          : 'border-yellow-500/40 bg-yellow-500/10'
                      }`}
                      data-testid={`topology-tab-dr-standby-${region}`}
                    >
                      <div className="text-[10px] uppercase tracking-wide text-[var(--color-text-dim)]">
                        Standby region
                      </div>
                      <div
                        className={`mt-0.5 font-mono text-xs font-semibold ${
                          standbyAbsent ? 'text-red-400' : 'text-yellow-400'
                        }`}
                      >
                        {region}
                      </div>
                      <div
                        className={`text-[10px] ${standbyAbsent ? 'text-red-400' : 'text-[var(--color-text-dim)]'}`}
                        data-testid={`topology-tab-dr-standby-${region}-health`}
                      >
                        {standbyHealthLine}
                      </div>
                    </div>
                  ))
                ) : (
                  <div
                    className={`min-w-[11rem] rounded-md border px-3 py-2 ${
                      standbyAbsent
                        ? 'border-red-500/40 bg-red-500/10'
                        : 'border-yellow-500/40 bg-yellow-500/10'
                    }`}
                    data-testid="topology-tab-dr-standby"
                  >
                    <div className="text-[10px] uppercase tracking-wide text-[var(--color-text-dim)]">
                      Standby region
                    </div>
                    <div
                      className={`mt-0.5 font-mono text-xs font-semibold ${
                        standbyAbsent ? 'text-red-400' : 'text-yellow-400'
                      }`}
                    >
                      {liveStandbyRegion ||
                        drStatus?.replicas?.find((rep) => rep.role !== 'primary')?.region ||
                        '—'}
                    </div>
                    <div
                      className={`text-[10px] ${standbyAbsent ? 'text-red-400' : 'text-[var(--color-text-dim)]'}`}
                      data-testid="topology-tab-dr-standby-health"
                    >
                      {standbyHealthLine}
                    </div>
                  </div>
                )}
              </div>

              {/* #4923/#4901 — the explicit honest standby-absent condition.
                  Rendered ONLY on a VERIFIED absent standby (backend cross-
                  checked the live cnpg-pair replica half); never inferred
                  from a small lag number or a stored-green CR phase. */}
              {standbyAbsent ? (
                <div
                  className="mt-3 rounded-md border border-red-500/40 bg-red-500/10 px-3 py-2 text-xs text-red-400"
                  data-testid="topology-tab-dr-standby-absent"
                >
                  Standby absent — the required hot-standby is unreachable.
                  Replication has no standby leg to acknowledge commits; RPO=0
                  durability is at risk until the standby recovers.
                </div>
              ) : null}

              {/* Live replication lag in seconds (row 56) — never a hardcoded —. */}
              <div className="mt-3 flex items-center gap-2 text-xs" data-testid="topology-tab-dr-lag">
                <span className="text-[var(--color-text-dim)]">Replication lag</span>
                <span
                  className={`inline-flex items-center gap-1.5 rounded-md px-2 py-0.5 font-semibold tabular-nums ${
                    effectiveLagColor === 'green'
                      ? 'bg-green-500/10 text-green-400'
                      : effectiveLagColor === 'yellow'
                        ? 'bg-yellow-500/10 text-yellow-400'
                        : effectiveLagColor === 'red'
                          ? 'bg-red-500/10 text-red-400'
                          : 'bg-[var(--color-bg)] text-[var(--color-text-dim)]'
                  }`}
                  data-testid="topology-tab-dr-lag-value"
                >
                  {/* #5514 — a lag NUMBER is a claim that a standby is being
                      measured. On a VERIFIED-absent standby (#4901) the backend
                      keeps reporting 0, which reads as perfect health during an
                      outage; render "—" instead of that false zero.
                      #5508 — same for an UNVERIFIED measurement: when the
                      health gates say the reading is unverified, the zero is a
                      report, not a measurement. */}
                  {lagSeconds == null || standbyAbsent || lagUnverified
                    ? '—'
                    : `${lagSeconds.toFixed(1)} s`}
                </span>
                {standbyAbsent ? (
                  <span className="text-[10px] text-red-400">no standby leg to measure</span>
                ) : null}
                {!standbyAbsent && lagUnverified ? (
                  <span
                    className="text-[10px] text-yellow-400"
                    data-testid="topology-tab-dr-lag-unverified"
                  >
                    unverified — the health gates below report no live measurement
                  </span>
                ) : null}
                {lagSeconds == null && drQ.isLoading ? (
                  <span className="text-[10px] text-[var(--color-text-dim)]">measuring…</span>
                ) : null}
                {drStatus?.syncState ? (
                  <span className="text-[10px] text-[var(--color-text-dim)]">
                    · {drStatus.syncState}
                  </span>
                ) : null}
              </div>

              {/* #5508 — every Warn/Fail health gate renders next to the
                  reading it qualifies. The hw291 defect was precisely that the
                  API returned these and the tab surfaced neither. */}
              {degradedGates.length > 0 ? (
                <ul className="mt-2 space-y-0.5" data-testid="topology-tab-dr-gates">
                  {degradedGates.map((g) => {
                    const failed = (g.status ?? '').toLowerCase() === 'fail'
                    return (
                      <li
                        key={g.name}
                        className={`text-[10px] ${failed ? 'text-red-400' : 'text-yellow-400'}`}
                        data-testid={`topology-tab-dr-gate-${g.name}`}
                      >
                        {failed ? '✕' : '⚠'} {g.name}
                        {g.message ? ` — ${g.message}` : ''}
                      </li>
                    )
                  })}
                </ul>
              ) : null}

              {/* Armed manual Switchover (#4552). The button opens a confirm
                  dialog that runs a read-only RPO/health preflight and only
                  arms [ Confirm Switchover ] when the standby is caught up and
                  promotable. The confirm wires to the operator-gated
                  POST .../switchover endpoint (the K-Cont-2 reconciler runs the
                  7-step cordon-before-promote sequence). Hidden when we can't
                  resolve a standby region to promote to. */}
              <div className="mt-4 flex items-center gap-2" data-testid="topology-tab-dr-switchover">
                <button
                  type="button"
                  className="rounded-md border border-[var(--color-border)] px-3 py-1.5 text-xs hover:border-[var(--color-accent)] disabled:cursor-not-allowed disabled:opacity-50"
                  onClick={() => setShowSwitchover(true)}
                  disabled={!switchoverArmed}
                  data-testid="topology-tab-dr-switchover-open"
                >
                  Switch over…
                </button>
                <span
                  className="text-[10px] text-[var(--color-text-dim)]"
                  data-testid="topology-tab-dr-switchover-reason"
                >
                  {switchoverArmed
                    ? `promote standby ${switchoverTarget} — runs an RPO/health preflight before you confirm`
                    : switchoverBlockedReason}
                </span>
              </div>

              {showSwitchover && switchoverArmed ? (
                <SwitchoverDialog
                  sovereignId={sovereignId}
                  continuumName={continuumName}
                  namespace={drNamespace}
                  fromRegion={drPrimaryRegion}
                  toRegion={switchoverTarget}
                  applicationName={applicationName}
                  lagSeconds={lagSeconds}
                  syncState={drStatus?.syncState}
                  disableNetwork={disableNetwork}
                  onClose={() => setShowSwitchover(false)}
                  onConfirmed={() => {
                    setShowSwitchover(false)
                    // Re-poll the DR status so the panel reflects the promoted
                    // primary + fresh lag right after the sequence kicks off.
                    setRefreshTick((t) => t + 1)
                  }}
                />
              ) : null}
            </>
          )}
        </section>
      ) : null}

      {/* ── Panel 2: Status ──────────────────────────────────────────── */}
      <section
        className="mt-4 rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] p-4"
        data-testid="topology-tab-status-panel"
      >
        <h3 className="mb-2 text-sm font-semibold text-[var(--color-text-strong)]">Status</h3>
        {/* #5934 — the ANSWER decides what renders here, never the component's
            CLASS. This branch used to lead on `isBootstrap` and hard-render the
            n/a prose regardless of what the API returned, so the real-status
            branch was unreachable for the ~40 bootstrap-kit components on a
            converged Sovereign — including when a caller had already handed us
            a status via initialApp. That is what made rows 67 and 69 read
            "n/a — bootstrap component" three inches under a header saying
            STATUS Ready.

            The abstention survives, narrowed to the one case that earns it: we
            asked and the endpoint genuinely had nothing (both synthesisers
            missed → 404). "We did not ask" and "there is no answer" must not
            render identically. */}
        {app ? (
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
        ) : isBootstrap && statusSettled ? (
          <p className="text-xs text-[var(--color-text-dim)]" data-testid="topology-tab-status-bootstrap">
            n/a — bootstrap component (HelmRelease, no Application CR). Live rollout status is tracked via Flux.
          </p>
        ) : (
          <p className="text-xs text-[var(--color-text-dim)]" data-testid="topology-tab-status-loading">
            Loading status…
          </p>
        )}
      </section>
    </div>
  )
}
