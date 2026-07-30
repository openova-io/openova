/**
 * placement.ts — #3969 canonical application-centric placement model (FE).
 *
 * ONE desired state = the Application's Placement: a list of TARGETS, each
 * { region × cluster × vCluster × data-role Primary|Standby }, a Standby
 * carrying a type Hot|Cold. The Blueprint's `capability`
 * (multi-primary | primary+standby) GATES whether >1 target may be
 * Primary. The PATTERN name (singleton / active-passive / active-hot-standby
 * / active-active) is a DERIVED, read-only label — never stored, never
 * selected. Status is the recon vocabulary (Reconciled/Reconciling/
 * Degraded) — never a second contradictory value.
 *
 * This module is the SINGLE source of truth for the placement vocabulary,
 * derivation, and labels — it replaces the three overlapping normalisers
 * (TopologyTab.canonicalTopologyClass, TopologyEditor.canonicalizeMode,
 * the old DR class gates) and the deleted second-derived-class + DR
 * machinery. Mirrors the Go single source (v1alpha1/placement_target.go).
 *
 * Ref #3969.
 */

export type DataRole = 'Primary' | 'Standby'
export type StandbyType = 'Hot' | 'Cold'
export type Capability = 'multi-primary' | 'primary+standby'
export type Pattern = 'singleton' | 'active-passive' | 'active-hot-standby' | 'active-active'

/**
 * The token for "no pattern could be derived" (#5515). NOT a pattern — the
 * explicit absence of one.
 */
export const PATTERN_NOT_REPORTED = 'not-reported'

/**
 * DerivedPattern — what `derivePattern` can honestly return: one of the four
 * real patterns, or `not-reported` when the target list carries no derivable
 * placement at all (#5515).
 *
 * `not-reported` exists because `singleton` is a CONFIDENT CLAIM — "there is
 * exactly one Primary, in one region, and no cross-region failover is wanted".
 * Returning it for an EMPTY target list turns "we know nothing" into "we know
 * this is fine", which is the DR-truthfulness defect #5515 caught live on
 * hw291 (`cilium` rendered `Pattern: singleton` directly beside its own text
 * "No placement targets reported yet.").
 */
export type DerivedPattern = Pattern | typeof PATTERN_NOT_REPORTED

/** One desired target. A Standby carries a standbyType. */
export interface PlacementTarget {
  region: string
  cluster: string
  vcluster?: string
  role: DataRole
  /** Required iff role === 'Standby'; forbidden iff role === 'Primary'. */
  standbyType?: StandbyType
}

/** Per-owned-dependency cascade override. Absent ⇒ follow by default. */
export interface OwnedDependencyOverride {
  name: string
  follow: boolean
}

/** The Application's desired-state placement. */
export interface Placement {
  targets: PlacementTarget[]
  ownedDependencies?: OwnedDependencyOverride[]
}

/** The single recon status value. */
export type ReconStatus = 'Reconciled' | 'Reconciling' | 'Degraded'

/**
 * derivePattern — the ONLY place patterns come from (#3969 §7.3). Computed
 * from targets + roles + standby types for DISPLAY ONLY; never stored.
 *
 *   no Primary target        -> not-reported   (#5515 — nothing to derive)
 *   primaries ≥ 2            -> active-active
 *   standbys  == 0           -> singleton
 *   any standby is Hot       -> active-hot-standby
 *   else                     -> active-passive
 *
 * #5515 — the no-Primary guard MUST come first and MUST NOT fall through to
 * `singleton`. Every one of the four patterns asserts a live Primary; with
 * zero Primary targets there is no pattern, only missing data. The three
 * inputs that land here:
 *
 *   • `[]` — the /placement endpoint reported `targets: []` (the live hw291
 *     `cilium` case). Pre-fix this hit `standbys === 0` and returned the
 *     confident `singleton`.
 *   • a list whose roles are all unrecognised (neither Primary nor Standby) —
 *     same counters, same false `singleton`.
 *   • a Standby-only list — asserting an "active-*" pattern with no active.
 *     `validatePlacement` already rejects this as `NoPrimary`; the derived
 *     label must not contradict that verdict.
 */
export function derivePattern(targets: PlacementTarget[]): DerivedPattern {
  let primaries = 0
  let standbys = 0
  let anyHot = false
  for (const t of targets ?? []) {
    if (t.role === 'Primary') primaries++
    else if (t.role === 'Standby') {
      standbys++
      if (t.standbyType === 'Hot') anyHot = true
    }
  }
  // #5515 — no Primary ⇒ no pattern. Never fail open into `singleton`.
  if (primaries === 0) return PATTERN_NOT_REPORTED
  if (primaries >= 2) return 'active-active'
  if (standbys === 0) return 'singleton'
  if (anyHot) return 'active-hot-standby'
  return 'active-passive'
}

/**
 * Human one-liner per derived pattern (display only).
 *
 * Exhaustive over DerivedPattern with NO trailing return — so adding a member
 * to the union without describing it fails `tsc` here (TS2366) instead of
 * silently returning undefined into the UI.
 */
export function describePattern(pattern: DerivedPattern): string {
  switch (pattern) {
    case PATTERN_NOT_REPORTED:
      return 'No placement targets reported — the pattern cannot be derived yet.'
    case 'singleton':
      return 'One Primary in one region; no cross-region failover.'
    case 'active-passive':
      return 'One Primary; a Cold standby in the second region rebuilds on failover.'
    case 'active-hot-standby':
      return 'One Primary; a live Hot standby in the second region promotes in seconds.'
    case 'active-active':
      return 'Two or more Primary targets serve live traffic; data syncs between them.'
  }
}

/**
 * patternLabel — the display string for a derived pattern. Renders the
 * un-derivable case as prose ("not reported") so no surface ever prints a
 * pattern NAME for data it does not have (#5515).
 */
export function patternLabel(pattern: DerivedPattern): string {
  return pattern === PATTERN_NOT_REPORTED ? 'not reported' : pattern
}

/** Normalize a Blueprint capability string to the canonical enum (default primary+standby). */
export function normalizeCapability(raw: string | null | undefined): Capability {
  return (raw ?? '').trim().toLowerCase() === 'multi-primary' ? 'multi-primary' : 'primary+standby'
}

/**
 * The canonical reason emitted when a placement declares >1 Primary but the
 * capability is primary+standby (#3969 §7.3, DoD item 5).
 */
export const MULTI_PRIMARY_NOT_SUPPORTED = 'MultiPrimaryNotSupported'

/** Count Primary targets. */
export function primaryCount(targets: PlacementTarget[]): number {
  return targets.filter((t) => t.role === 'Primary').length
}

/**
 * Whether a 2nd Primary may be selected given the capability — the editor
 * disables the Primary radio for an additional target when this is false
 * (#3969 §9, DoD item 5). A target that is ALREADY Primary is always
 * allowed to stay Primary.
 */
export function canAddPrimary(targets: PlacementTarget[], capability: Capability): boolean {
  if (capability === 'multi-primary') return true
  return primaryCount(targets) < 1
}

/**
 * validatePlacement — the FE gate, mirroring Go ValidatePlacement.
 * Returns null when valid, else a stable reason + plain message. NEVER
 * throws.
 */
export function validatePlacement(
  targets: PlacementTarget[],
  capability: Capability,
): { reason: string; message: string } | null {
  let primaries = 0
  for (let i = 0; i < targets.length; i++) {
    const t = targets[i]
    if (t.role !== 'Primary' && t.role !== 'Standby') {
      return { reason: 'InvalidRole', message: `target ${i + 1} role "${t.role}" is not Primary|Standby` }
    }
    if (t.role === 'Primary') {
      primaries++
      if (t.standbyType) {
        return { reason: 'PrimaryHasStandbyType', message: `target ${i + 1} is Primary but carries a standby type` }
      }
    } else if (t.role === 'Standby') {
      if (t.standbyType !== 'Hot' && t.standbyType !== 'Cold') {
        return { reason: 'StandbyMissingType', message: `target ${i + 1} is Standby but has no Hot|Cold type` }
      }
    }
  }
  if (targets.length > 0 && primaries === 0) {
    return { reason: 'NoPrimary', message: 'placement declares no Primary target' }
  }
  if (primaries >= 2 && capability !== 'multi-primary') {
    return {
      reason: MULTI_PRIMARY_NOT_SUPPORTED,
      message: 'multi-Primary not supported by this blueprint (only one Primary allowed)',
    }
  }
  return null
}

/**
 * targetsFromLegacy — best-effort projection of the legacy placement shape
 * (the `mode` posture string + a `regions[]` list, or the per-region
 * `status.regions[]` rollup with roles) onto the #3969 target list, so an
 * Application written before this ticket still renders meaningfully. The
 * canonical desired state is `spec.placement.targets[]`; this is the
 * fallback when only the legacy shape is present.
 */
export function targetsFromLegacy(args: {
  mode?: string | null
  regions?: string[]
  clusters?: string[]
  vcluster?: string
  statusRegions?: Array<{ name: string; role?: string }>
}): PlacementTarget[] {
  const vcluster = args.vcluster || 'mgmt'

  // Prefer the observed per-region rollup when present (it carries roles).
  if (args.statusRegions && args.statusRegions.length > 0) {
    return args.statusRegions.map((r, i) => {
      const isPrimary = (r.role ?? '').toLowerCase() === 'primary' || (r.role ?? '').toLowerCase() === 'active' || i === 0
      return {
        region: r.name,
        cluster: args.clusters?.[i] ?? r.name,
        vcluster,
        role: isPrimary ? ('Primary' as DataRole) : ('Standby' as DataRole),
        ...(isPrimary ? {} : { standbyType: 'Hot' as StandbyType }),
      }
    })
  }

  const regions = args.regions ?? []
  const mode = (args.mode ?? '').trim().toLowerCase()
  const isHot = mode === 'active-hot-standby' || mode === 'active-hotstandby'
  const isActiveActive = mode === 'active-active'
  const isPassive = mode === 'active-passive'

  if (regions.length === 0) return []

  return regions.map((region, i) => {
    if (i === 0) {
      return { region, cluster: args.clusters?.[i] ?? region, vcluster, role: 'Primary' as DataRole }
    }
    if (isActiveActive) {
      return { region, cluster: args.clusters?.[i] ?? region, vcluster, role: 'Primary' as DataRole }
    }
    const standbyType: StandbyType = isPassive ? 'Cold' : isHot ? 'Hot' : 'Hot'
    return { region, cluster: args.clusters?.[i] ?? region, vcluster, role: 'Standby' as DataRole, standbyType }
  })
}
