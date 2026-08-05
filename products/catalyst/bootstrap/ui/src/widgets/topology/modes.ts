/**
 * modes.ts — THE canonical topology-mode vocabulary for the sovereign-admin
 * console.
 *
 * Extracted from the former `TopologyEditor.tsx` (#5609). That file exported
 * two unrelated things: (a) this vocabulary, which production imports, and
 * (b) a `TopologyEditor` React component that **nothing in production ever
 * mounted**. The component was superseded by `PlacementEditor` when #3969
 * collapsed placement editing onto one editor with "NO mode picker, NO
 * separate DR surface" — but the component and its 7 render tests were left
 * behind, so the suite looked like it covered a mode picker the operator
 * could never reach. The component is gone; the vocabulary lives here.
 *
 * The ONLY production surface that selects a topology mode is the create
 * dialog (`NewInstanceDialog` in pages/sovereign/AppDetail/InstancesSection),
 * which gates its options on the Blueprint's `spec.topology.supported`.
 */

import type { BlueprintTopology } from '@/shared/constants/catalog.generated'

/**
 * ALL_MODES — the topology-mode option set for the placement picker.
 * Exported (#3599, EPIC #3597) so the create-from-catalog placement step
 * reuses the SAME option set the catalog placementSchema serves.
 *
 * ONE canonical vocabulary (#3375 DoD-1). The four canonical classes —
 * `singleton` / `active-active` / `active-hot-standby` / `active-passive` —
 * are the exact set the catalog placementSchema serves, the Application CR
 * stores, and the application-controller's resolver accepts. canonicalizeMode
 * folds any legacy value the server returns, so loading an older CR keeps
 * working. This set only makes every canonical class REPRESENTABLE; the
 * per-Blueprint `topology.supported` gate decides which are SELECTABLE.
 */
export const ALL_MODES = ['singleton', 'active-active', 'active-hot-standby', 'active-passive'] as const

export type TopologyMode = (typeof ALL_MODES)[number]

/**
 * canonicalizeMode (#3648, #3375 DoD-1) folds BOTH the legacy editor
 * dialect (single-region / active-hotstandby) AND the canonical
 * vocabulary (singleton / active-active / active-hot-standby /
 * active-passive) onto the ONE canonical token, so any value the server
 * returns (an older CR, a legacy POST) compares correctly against the
 * canonical ALL_MODES / SupportedTopologies. Mirrors the Go single
 * source of truth (placement.Canonicalize) — the CI drift test asserts
 * the FE + Go + catalyst-api agree.
 */
export function canonicalizeMode(raw: string): string {
  switch (raw.trim().toLowerCase()) {
    case 'active-hot-standby':
    case 'active-hotstandby':
      return 'active-hot-standby'
    case 'active-passive':
      return 'active-passive'
    case 'active-active':
      return 'active-active'
    case 'singleton':
    case 'single-region':
      return 'singleton'
    default:
      return raw.trim().toLowerCase()
  }
}

/**
 * describeMode — one-line human description per topology mode. Exported
 * (#3599) so the create-flow placement step shows the same helper text
 * the post-create editor shows for each mode. Canonicalises first so it
 * describes both the canonical tokens and any legacy spelling
 * (#3375 DoD-1).
 */
export function describeMode(mode: string): string {
  switch (canonicalizeMode(mode)) {
    case 'singleton':
      return 'one cluster; lowest cost; no failover'
    case 'active-active':
      return 'every region serves traffic; horizontal scaling'
    case 'active-hot-standby':
      return 'primary serves; standby ready for switchover'
    case 'active-passive':
      // Generic FALLBACK only — used when an app declares no per-component
      // topology matrix. Most active-passive apps in the catalog replicate
      // (raft/streaming/cnpg), so the matrix-derived describer below
      // ALWAYS wins for them. Kept deliberately mechanism-neutral.
      return 'primary serves; standby in the second region promotes on failover'
    default:
      return ''
  }
}

/**
 * describeModeForComponent (#3905) — the per-mode helper text the placement
 * picker actually renders. It DERIVES the description from the SAME
 * per-component `BlueprintTopology` matrix the declared-topology CARD reads
 * (the component's `perTopology[variant]` contract: replication backend/mode
 * + switchover mechanism + RTO/RPO), so the card and the create dialog can
 * NEVER contradict for any component.
 *
 * The root bug this fixes: `describeMode('active-passive')` returned a
 * GENERIC, hardcoded, component-agnostic "…(backup-restore)" string for
 * EVERY component, while the card derived openbao's real `raft · async`
 * warm-standby semantics from the matrix. Now the picker reads the matrix
 * too, so openbao's active-passive option shows raft/async/raft-transition,
 * not backup-restore.
 *
 * Falls back to the generic `describeMode` one-liner when the component
 * declares no matrix entry for the mode (the singleton class, or an app with
 * no `spec.topology` block at all).
 */
export function describeModeForComponent(mode: string, topology?: BlueprintTopology): string {
  const canon = canonicalizeMode(mode)
  const generic = describeMode(mode)
  if (!topology) return generic

  // perTopology keys use the matrix dialect (active-hot-standby /
  // active-passive / active-active / singleton); match canonically so the
  // canonical token resolves the right contract.
  let variant: BlueprintTopology['perTopology'][string] | undefined
  for (const [key, contract] of Object.entries(topology.perTopology)) {
    if (canonicalizeMode(key) === canon) {
      variant = contract
      break
    }
  }
  if (!variant) return generic

  // singleton carries no DR contract — the generic one-liner is correct and
  // there is nothing matrix-specific to add.
  if (canon === 'singleton') return generic

  const repl = variant.replication
  const sw = variant.switchover
  const parts: string[] = []

  // Lead with the operative posture per class, then append the component's
  // concrete replication + switchover contract from the matrix.
  if (canon === 'active-active') {
    parts.push('every region serves traffic')
  } else {
    // active-hot-standby / active-passive: one region serves, the other is
    // the standby. The replication mode (sync vs async) is what separates a
    // hot standby from a warm one — and it comes from the matrix, never a
    // hardcoded "backup-restore".
    parts.push('primary serves; second-region standby promotes on failover')
  }

  if (repl?.backend) {
    const mode = repl.mode ? `${repl.backend} · ${repl.mode}` : repl.backend
    parts.push(`${mode} replication`)
  }
  if (sw?.mechanism && sw.mechanism.toLowerCase() !== 'none') {
    const rto = sw.rtoSeconds != null ? `RTO ${sw.rtoSeconds}s` : null
    const rpo = sw.rpoSeconds != null ? `RPO ${sw.rpoSeconds}s` : null
    const slo = [rto, rpo].filter(Boolean).join(' / ')
    parts.push(slo ? `${sw.mechanism} switchover (${slo})` : `${sw.mechanism} switchover`)
  }

  return parts.join('; ')
}
