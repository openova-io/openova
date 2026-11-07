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
 * matrix consumption.
 *
 * DESIRED STATE (2026-11-07): there is NO switchover button or request. The
 * ONLY way to move the writer is to change which target is Primary in the
 * PlacementEditor and Save. The Application controller projects that Primary
 * into the Continuum's spec.desiredPrimaryRegion and the continuum-controller
 * converges (runs the 7-step switchover itself). The DR section shows the
 * declared Primary next to the region actually serving writes, plus the
 * convergence progress / blocker and the last switchover.
 */

import { useMemo, useState } from 'react'
import { useQuery, useQueryClient, type Query } from '@tanstack/react-query'

import { getCatalogItem, type CatalogItem } from '@/lib/catalog.api'
import {
  getContinuumReplicationStatus,
  getSwitchoverPreview,
  lagBucket,
  type ContinuumHealthGate,
  type ContinuumReplicationStatus,
  type LagBucket,
} from '@/lib/continuum.api'
import { getHierarchicalInfrastructure } from '@/lib/infrastructure.types'
import { PlacementEditor, type OwnedDependencyInfo } from '@/widgets/topology/PlacementEditor'
// #3648/#3375 — the ONE canonicaliser for the topology-mode vocabulary, mirrored
// from Go's placement.Canonicalize. Used here (row 63) so a CR declaring
// `active-hotstandby` is read the same as one declaring `active-hot-standby`.
import { canonicalizeMode } from '@/widgets/topology/modes'
import {
  type Capability,
  type PlacementTarget,
  type ReconStatus,
  HOST_VCLUSTER,
  PATTERN_NOT_REPORTED,
  describePattern,
  normalizeCapability,
  patternLabel,
} from '@/shared/lib/placement'
import { drIsConverging, drPollInterval } from './drConvergence'
import {
  PLACEMENT_SOURCE_LABEL,
  useResolvedPlacement,
  type ApplicationStatus,
} from './useResolvedPlacement'

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

// The placement resolver + its wire types live in useResolvedPlacement.ts so
// the Overview tab reads the SAME value this tab prints (#6192).

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
  // The editor's live (unsaved) targets — drives the "what will Save do to
  // the DR writer" preview. Null when the editor is closed.
  const [draftTargets, setDraftTargets] = useState<PlacementTarget[] | null>(null)

  // #6192 — ONE resolver for the whole per-app screen. The status poll, the
  // runtime placement poll and the six-rung resolution chain moved to
  // useResolvedPlacement.ts verbatim; the Overview tab calls the same hook.
  const { app, statusSettled, targets, placementSource, pattern } = useResolvedPlacement({
    sovereignId,
    applicationName,
    namespace,
    initialApp,
    disableNetwork,
    isBootstrap,
    refreshTick,
  })

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

  // UAT row 63 — WHAT THE CR DECLARES, independently of what was observed.
  //
  // `hasStandby` above and `hasLiveDR` below are both derived from OBSERVED
  // state: a projected Standby target, or a live Continuum reading. Neither
  // consults what the Application CR DECLARES. On hw293,
  // `hw293walkone/uat50-ahs-pg` declared `spec.placement.mode:
  // active-hot-standby` with `standbyRegions [<region-b>]` while nothing had
  // observed that standby yet — /placement answered `{"targets":[]}`,
  // `status.targets` was absent, and `status.perCluster` held the single
  // `singleton` entry the controller wrote after fanning the HelmRelease out to
  // region A only. So both observed terms were false, `showDR` was false, and
  // the ENTIRE `topology-tab-dr-*` subtree was absent from the page.
  //
  // That is a verdict published from ABSENT evidence, which is the exact thing
  // row 63 exists to forbid: the page stated "this app has no DR" by omission,
  // when the object said "a standby is declared and nothing has reported on it
  // yet". An absent control and a disabled control are not the same statement —
  // only the second one tells the operator that switchover is a thing this app
  // HAS and cannot currently do.
  //
  // #6061 fixed the rung ABOVE this one (an incomplete `status.perCluster`
  // shadowing a `status.targets` that already named the pair). It does not help
  // an app on which NEITHER field was ever written.
  //
  // Read only the DECLARED posture here — `spec.placement.mode` on the #3969
  // object form, the legacy `spec.placement` string, or the pre-#3969
  // `status.placement` string — and canonicalise it so both spellings of every
  // mode compare correctly. `singleton` and `active-active` declare no standby
  // and must NOT open the section: that keeps row 58 (never arm a DR block
  // against a phantom region) intact, and it is what makes this a
  // discrimination rather than a blanket promotion.
  const declaresStandby = useMemo(() => {
    const specPlacement = app?.spec?.placement
    const status = (app?.status ?? {}) as Record<string, unknown>
    const declaredMode =
      specPlacement && typeof specPlacement === 'object'
        ? (specPlacement as Record<string, unknown>).mode
        : typeof specPlacement === 'string'
          ? specPlacement
          : typeof status.placement === 'string'
            ? status.placement
            : ''
    if (typeof declaredMode !== 'string' || declaredMode === '') return false
    const mode = canonicalizeMode(declaredMode)
    return mode === 'active-hot-standby' || mode === 'active-passive'
  }, [app])

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
    // Desired-state convergence: poll fast (3s) while the controller is
    // converging the writer onto the declared Primary, so the step counter
    // moves live; otherwise the calm 30s cadence.
    refetchInterval: (query: Query<ContinuumReplicationStatus>) => drPollInterval(query.state.data),
    retry: false,
  })
  const drStatus = drQ.data

  // ── Desired vs observed primary (desired-state model) ─────────────
  // `currentPrimary` is the OBSERVED writer (status.primaryRegion);
  // `desiredPrimaryRegion` is what the placement declares. Never print the
  // Continuum's static spec.primaryRegion as "the primary".
  const observedPrimary = drStatus?.observedPrimaryRegion || drStatus?.currentPrimary || ''
  const desiredPrimary = drStatus?.desiredPrimaryRegion || ''
  const converging = drIsConverging(drStatus)
  const lastSwitchover = drStatus?.lastSwitchover

  // The primary + standby regions for the DR cards. Prefer the live
  // replication-status (authoritative current primary after a switchover);
  // fall back to the placement targets when the endpoint is unavailable.
  const drPrimaryRegion = useMemo(
    () =>
      drStatus?.observedPrimaryRegion ||
      drStatus?.currentPrimary ||
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

  // UAT row 63 — the third term is the DECLARATION. `hasStandby` and
  // `hasLiveDR` are both observations; when both are false the honest answer is
  // "nothing has reported", not "this app has no DR". `declaresStandby` opens
  // the section for an app whose CR asks for a standby, and the section's own
  // `!drBacked` branch below then renders the DISABLED control with its reason
  // and NOTHING numeric — so this can never arm a phantom switchover (#5514).
  const showDR = hasStandby || hasLiveDR || declaresStandby

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

  // ── Save preview: "Saving will switch the writer from X to Y" ──────
  // Purely informative — Save stays the action. Fires only while the editor
  // is open, the app is Continuum-backed (live), and the draft's Primary
  // differs from the region serving writes now.
  const draftPrimary = useMemo(
    () => draftTargets?.find((t) => t.role === 'Primary')?.region || '',
    [draftTargets],
  )
  const previewTarget =
    editing && drBacked && !!draftPrimary && !!drPrimaryRegion && draftPrimary !== drPrimaryRegion
      ? draftPrimary
      : ''
  const previewQ = useQuery({
    queryKey: ['continuum-switchover-preview', sovereignId, continuumName, drNamespace, previewTarget],
    queryFn: () =>
      getSwitchoverPreview(sovereignId, continuumName, { targetRegion: previewTarget }, { namespace: drNamespace }),
    enabled: !disableNetwork && !!previewTarget,
    retry: false,
    staleTime: 10_000,
  })

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
            onTargetsChange={setDraftTargets}
            beforeActions={
              previewTarget ? (
                <div
                  className="mb-3 rounded-md border border-[var(--color-accent)]/40 bg-[var(--color-accent)]/10 px-3 py-2 text-xs"
                  data-testid="topology-tab-save-preview"
                >
                  <p className="text-[var(--color-text)]">
                    Saving will switch the writer from{' '}
                    <span className="font-mono font-semibold">{drPrimaryRegion}</span> to{' '}
                    <span className="font-mono font-semibold">{previewTarget}</span>. The DR
                    controller runs the switchover once the placement is saved.
                  </p>
                  {previewQ.isLoading ? (
                    <p className="mt-1 text-[var(--color-text-dim)]" data-testid="topology-tab-save-preview-loading">
                      Checking switchover preconditions…
                    </p>
                  ) : previewQ.isError ? (
                    <p className="mt-1 text-yellow-400" data-testid="topology-tab-save-preview-error">
                      Preconditions could not be checked: {(previewQ.error as Error).message}
                    </p>
                  ) : previewQ.data ? (
                    <div className="mt-1" data-testid="topology-tab-save-preview-checks">
                      {previewQ.data.blockingChecks?.length ? (
                        <ul className="space-y-0.5">
                          {previewQ.data.blockingChecks.map((c) => (
                            <li key={c} className="text-yellow-400">
                              ⚠ {c}
                            </li>
                          ))}
                        </ul>
                      ) : (
                        <p className="text-green-400">✓ all switchover checks pass</p>
                      )}
                      <p className="text-[var(--color-text-dim)]">
                        current lag {Number(previewQ.data.currentLagSec ?? 0).toFixed(1)} s · estimated{' '}
                        {previewQ.data.estimatedDuration}
                      </p>
                    </div>
                  ) : null}
                </div>
              ) : null
            }
            onCancel={() => {
              setEditing(false)
              setDraftTargets(null)
            }}
            onApplied={() => {
              setEditing(false)
              setDraftTargets(null)
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
              {/* UAT row 63 — `declaresStandby` joins `hasStandby` here for the
                  same reason it joins `showDR`: on an app whose standby was
                  DECLARED but never observed, `hasStandby` is false, so without
                  it the operator would be shown a DR section with no statement
                  of why it is there while the Placement panel beside it reads
                  `singleton`. Silence at that point is the same absent-evidence
                  defect one level down. `drStatus` is still required — the note
                  names the reading, so it may not be printed before one
                  arrives. */}
              {(hasStandby || declaresStandby) && drStatus && !drQ.isLoading ? (
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
              {!drQ.isLoading ? (
                <p
                  className="mt-3 text-[10px] text-[var(--color-text-dim)]"
                  data-testid="topology-tab-dr-unavailable"
                >
                  The Primary is moved by editing this app&apos;s placement. No live
                  Continuum backs it yet, so a saved Primary change cannot switch the
                  writer until a real replica reports.
                </p>
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
                className="mb-3 flex flex-wrap items-stretch gap-2"
                data-testid="topology-tab-dr-primaries"
              >
                <div
                  className="min-w-[11rem] rounded-md border border-[var(--color-border)] px-3 py-2"
                  data-testid="topology-tab-dr-desired"
                  title="The Primary region declared in this app's placement. Change it with Edit placement → Save."
                >
                  <div className="text-[10px] uppercase tracking-wide text-[var(--color-text-dim)]">
                    Desired primary
                  </div>
                  <div className="mt-0.5 font-mono text-xs font-semibold text-[var(--color-text)]">
                    {desiredPrimary || '—'}
                  </div>
                  <div className="text-[10px] text-[var(--color-text-dim)]">
                    {desiredPrimary ? 'declared in placement' : 'not declared'}
                  </div>
                </div>
                <div
                  className="min-w-[11rem] rounded-md border border-green-500/40 bg-green-500/10 px-3 py-2"
                  data-testid="topology-tab-dr-observed"
                  title="The region actually serving writes, observed by the DR controller."
                >
                  <div className="text-[10px] uppercase tracking-wide text-[var(--color-text-dim)]">
                    Serving writes
                  </div>
                  <div className="mt-0.5 font-mono text-xs font-semibold text-green-400">
                    {observedPrimary || '—'}
                  </div>
                  <div className="text-[10px] text-[var(--color-text-dim)]">
                    {!desiredPrimary
                      ? 'observed'
                      : converging
                        ? 'converging…'
                        : '✓ matches desired'}
                  </div>
                </div>
              </div>

              {converging ? (
                <div
                  className={`mb-3 rounded-md border px-3 py-2 text-xs ${
                    drStatus?.switchoverInProgress
                      ? 'border-[var(--color-accent)]/40 bg-[var(--color-accent)]/10 text-[var(--color-text)]'
                      : 'border-yellow-500/40 bg-yellow-500/10 text-yellow-400'
                  }`}
                  data-testid="topology-tab-dr-converging"
                >
                  {drStatus?.switchoverInProgress ? (
                    <>
                      Switching <span className="font-mono">{observedPrimary || '—'}</span> →{' '}
                      <span className="font-mono">{desiredPrimary}</span>: {drStatus.step || 'in progress'}
                    </>
                  ) : (
                    <>
                      {drStatus?.convergeMessage ||
                        `Not converged — writes are served from ${observedPrimary || 'an unobserved region'}, the placement declares ${desiredPrimary}.`}
                      {drStatus?.convergeReason ? (
                        <span
                          className="ml-2 font-mono text-[10px]"
                          data-testid="topology-tab-dr-converge-reason"
                        >
                          ({drStatus.convergeReason})
                        </span>
                      ) : null}
                    </>
                  )}
                </div>
              ) : null}

              {lastSwitchover && (lastSwitchover.at || lastSwitchover.result) ? (
                <div
                  className="mb-3 rounded-md border border-[var(--color-border)] px-3 py-2 text-xs"
                  data-testid="topology-tab-dr-last-switchover"
                >
                  <div className="text-[10px] uppercase tracking-wide text-[var(--color-text-dim)]">
                    Last switchover
                  </div>
                  <div className="mt-0.5 flex flex-wrap items-center gap-x-2">
                    <span className="font-mono">
                      {lastSwitchover.from || '—'} → {lastSwitchover.to || '—'}
                    </span>
                    {lastSwitchover.result ? (
                      <span
                        className={`rounded px-1.5 py-0.5 text-[10px] font-semibold ${
                          lastSwitchover.result === 'Success'
                            ? 'bg-green-500/10 text-green-400'
                            : 'bg-red-500/10 text-red-400'
                        }`}
                        data-testid="topology-tab-dr-last-switchover-result"
                      >
                        {lastSwitchover.result}
                      </span>
                    ) : null}
                    {lastSwitchover.at ? (
                      <span className="text-[var(--color-text-dim)]">{lastSwitchover.at}</span>
                    ) : null}
                  </div>
                </div>
              ) : null}

              <div
                className="flex flex-wrap items-stretch gap-2"
                data-testid="topology-tab-dr-regions"
              >
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
