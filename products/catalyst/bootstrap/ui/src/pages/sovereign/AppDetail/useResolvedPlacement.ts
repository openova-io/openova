/**
 * useResolvedPlacement — THE placement resolver for the per-app screen
 * (#6192, #3969).
 *
 * One hook, two consumers: the Topology tab's "Pattern:" line and the
 * Overview tab's PLACEMENT cell. Before this file the resolver lived inside
 * TopologyTab.tsx and the Overview printed `apiApp.placement` — the STORED
 * posture off the detail endpoint — so a `keycloak` that genuinely runs in
 * both regions read `singleton` on Overview and `active-active ·
 * runtime-observed` one click away on Topology (hw295 grafana/keycloak;
 * hw307 gitea/harbor/keycloak/openbao, 2026-11-06). #3969 forbids exactly
 * that second contradictory value, and #5422 had already removed the
 * `?? 'singleton'` fail-open from the Overview without removing the second
 * SOURCE. Sharing the resolver is what removes it: both cells now derive
 * from the same targets, so they can only ever disagree if React renders
 * one value twice.
 *
 * Resolution order (#3982 / #5568 / #6061 / #5420 — unchanged, moved):
 *   1. runtime-derived targets from GET …/placement, only when the endpoint
 *      says a runtime was consulted (`derivedFromRuntime: true`);
 *   2. the same targets when it did not (declared, not observed);
 *   3. `spec.placement.targets[]` — the #3969 desired state;
 *   4. `status.targets` — the controller's own placement target list;
 *   5. `status.perCluster` — the controller's per-cluster observation;
 *   6. the legacy mode + regions projection.
 * Each rung is labelled with its provenance; only rung 1 reads as observed.
 */

import { useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'

import { getApplicationPlacement, getApplicationStatus } from '@/lib/catalog.api'
import type { ObjectStatus } from '@/lib/objectStatus'
import {
  type DerivedPattern,
  type PlacementTarget,
  derivePattern,
  targetsFromLegacy,
} from '@/shared/lib/placement'

export interface ApplicationStatus {
  name: string
  namespace: string
  phase?: string
  status?: Record<string, unknown>
  spec?: ApplicationSpec
  /** ADR-0015 D7 — statusOf(object) incl. perRegion[], rendered verbatim. */
  objectStatus?: ObjectStatus
}

export interface ApplicationSpec {
  blueprintRef?: { name?: string; version?: string }
  placement?: string | Record<string, unknown>
  regions?: string[]
  environmentRef?: string
}

export interface RegionStatus {
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
export type PlacementSource = 'runtime' | 'declared' | 'none'

/** The chip copy per provenance. Never prints a source it cannot back. */
export const PLACEMENT_SOURCE_LABEL: Record<PlacementSource, string> = {
  runtime: 'runtime-observed',
  declared: 'declared · not observed',
  none: 'no source',
}

export interface UseResolvedPlacementOptions {
  sovereignId: string
  applicationName: string
  namespace?: string
  /** Test seam — pre-fill data, skip the status poll. */
  initialApp?: ApplicationStatus
  /** Test seam — skip every network call. */
  disableNetwork?: boolean
  /** Bootstrap-kit HelmRelease with no companion CR (see TopologyTab). */
  isBootstrap?: boolean
  /** Bump to refetch both queries (the Topology tab's "Refresh"). */
  refreshTick?: number
}

export interface ResolvedPlacement {
  /** The status response (or the test seam), as the Topology tab reads it. */
  app: ApplicationStatus | undefined
  /** True once the status question has been ANSWERED (success or error). */
  statusSettled: boolean
  /** The resolved target list — what the Placement cards render. */
  targets: PlacementTarget[]
  /** Where `targets` came from. */
  placementSource: PlacementSource
  /** The DERIVED pattern label — the ONLY placement value either tab prints. */
  pattern: DerivedPattern
}

export function useResolvedPlacement({
  sovereignId,
  applicationName,
  namespace,
  initialApp,
  disableNetwork = false,
  isBootstrap = false,
  refreshTick = 0,
}: UseResolvedPlacementOptions): ResolvedPlacement {
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
    //
    // UAT row 16 — read the OBJECT form's `mode` too. `spec.placement` is
    // dual-form (#3373) and the /status endpoint now reports it in the shape
    // the CR holds instead of flattening it to a posture token, which is what
    // makes rung 2 above reachable at all. This rung must not lose the posture
    // in exchange: an object-form CR that carries `{mode, regions}` but no
    // `targets[]` — the shape the "+ New instance" dialog writes — reaches
    // here, and reading only the string form would leave `mode` empty and
    // project an ACTIVE-ACTIVE app as primary+standby. Same dual-form read as
    // declaresStandby below.
    const mode =
      typeof specPlacement === 'string'
        ? specPlacement
        : specPlacement && typeof (specPlacement as Record<string, unknown>).mode === 'string'
          ? ((specPlacement as Record<string, unknown>).mode as string)
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

  return { app, statusSettled, targets, placementSource, pattern }
}
