/**
 * presets.ts — the LENSES for the unified Cloud surface (#3958 / #3970).
 *
 * #3970 corrects the lens model. A lens is NOT a filter layer that hides
 * node types underneath a still-full chip strip. **A lens IS a named,
 * predefined chip-set, nothing more.** Selecting a lens sets the active
 * chip-set to *exactly* that lens's chips; the chip strip then renders
 * ONLY those chips (non-member chips are removed from the strip, not
 * greyed). `+ Add` grows the active set (active = lensChips ∪ additions);
 * removing a chip shrinks it. There is no separate hidden layer.
 *
 * Two flavours, per the founder-signed design:
 *   • Domain lenses  — Cloud (default) / Runtime / Reconciliation /
 *                      Networking / Data / Security. Membership cut by
 *                      CATEGORY (and, for Cloud, ALSO by family).
 *   • Family lenses  — Flux / Crossplane / cert-manager / CNPG /
 *                      External-Secrets / Cilium / Catalyst. Membership
 *                      cut by FAMILY.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 — the lens membership is DERIVED
 * from NODE_CATEGORY / NODE_FAMILY at runtime, never hand-listed at a
 * call site. `lensChips(lens)` is the single resolver the page and the
 * list both consume.
 */

import {
  ALL_NODE_TYPES,
  NODE_CATEGORY,
  NODE_FAMILY,
  type ArchNodeType,
  type NodeCategory,
  type NodeFamily,
} from './types'

export type LensId =
  // Domain lenses
  | 'cloud'
  | 'runtime'
  | 'reconciliation'
  | 'networking'
  | 'data'
  | 'security'
  // Family lenses
  | 'flux'
  | 'crossplane'
  | 'certManager'
  | 'cnpg'
  | 'externalSecrets'
  | 'cilium'
  | 'catalyst'

/**
 * Back-compat alias — some call sites historically imported `PresetId`.
 * A lens IS the preset; keep the alias so the rename is non-breaking.
 */
export type PresetId = LensId

export interface Lens {
  id: LensId
  label: string
  /** 'domain' lenses cut by category; 'family' lenses cut by family. */
  group: 'domain' | 'family'
  /** Membership categories. A type is a member when its category is in
   *  this set (AND, when `families` is also set, its family is in that
   *  set too). Omitted = category is not a constraint. */
  categories?: NodeCategory[]
  /** Membership families. A type is a member when its family is in this
   *  set (AND any `categories` constraint). Omitted = family is not a
   *  constraint. */
  families?: NodeFamily[]
}

/** Back-compat alias — a lens IS a preset. */
export type Preset = Lens

/**
 * The lens table — single source of truth for the dropdown. Membership
 * is DERIVED from these category/family rules via `lensChips` below; the
 * chips are never hand-listed.
 */
export const LENSES: Record<LensId, Lens> = {
  // ── Domain lenses ────────────────────────────────────────────────
  // Cloud is the DEFAULT lens (#3970): the cloud-provider infrastructure
  // picture — scope/compute/network nodes of the cloud family.
  cloud: {
    id: 'cloud',
    label: 'Cloud',
    group: 'domain',
    categories: ['scope', 'compute', 'network'],
    families: ['cloud'],
  },
  runtime: {
    id: 'runtime',
    label: 'Runtime',
    group: 'domain',
    categories: ['compute', 'scope'],
  },
  reconciliation: {
    id: 'reconciliation',
    label: 'Reconciliation',
    group: 'domain',
    categories: ['control'],
  },
  networking: {
    id: 'networking',
    label: 'Networking',
    group: 'domain',
    categories: ['network'],
  },
  data: {
    id: 'data',
    label: 'Data',
    group: 'domain',
    categories: ['data'],
  },
  security: {
    id: 'security',
    label: 'Security',
    group: 'domain',
    categories: ['config'],
  },
  // ── Family lenses (cut by family) ────────────────────────────────
  flux: { id: 'flux', label: 'Flux', group: 'family', families: ['flux'] },
  crossplane: {
    id: 'crossplane',
    label: 'Crossplane',
    group: 'family',
    families: ['crossplane'],
  },
  certManager: {
    id: 'certManager',
    label: 'cert-manager',
    group: 'family',
    families: ['certManager'],
  },
  cnpg: { id: 'cnpg', label: 'CNPG', group: 'family', families: ['cnpg'] },
  externalSecrets: {
    id: 'externalSecrets',
    label: 'External-Secrets',
    group: 'family',
    families: ['externalSecrets'],
  },
  cilium: { id: 'cilium', label: 'Cilium', group: 'family', families: ['cilium'] },
  catalyst: {
    id: 'catalyst',
    label: 'Catalyst',
    group: 'family',
    families: ['catalyst'],
  },
}

/** Back-compat alias — `LENSES` is the lens/preset table. */
export const PRESETS = LENSES

/** The default lens the surface opens on (#3970). */
export const DEFAULT_LENS_ID: LensId = 'cloud'

/** Ordered list for the dropdown — domain lenses first, then family. */
export const LENS_ORDER: LensId[] = [
  'cloud',
  'runtime',
  'reconciliation',
  'networking',
  'data',
  'security',
  'flux',
  'crossplane',
  'certManager',
  'cnpg',
  'externalSecrets',
  'cilium',
  'catalyst',
]

/** Back-compat alias. */
export const PRESET_ORDER = LENS_ORDER

/**
 * lensChips — the explicit set of ArchNodeTypes a lens SHOWS. A type is
 * a member when its category passes the lens's category constraint AND
 * its family passes the lens's family constraint. Constraints that are
 * omitted are not applied. This is the chip-set the strip renders and
 * the dataset both the graph and the list filter by (#3970).
 *
 * Derived purely from NODE_CATEGORY / NODE_FAMILY — never hand-listed
 * (Principle #4). A lens with neither constraint would match every type;
 * no such lens exists in the table (the old unconstrained 'all' lens is
 * deleted — the default is now Cloud).
 */
export function lensChips(lens: Lens): Set<ArchNodeType> {
  const catAllow = lens.categories ? new Set<NodeCategory>(lens.categories) : null
  const famAllow = lens.families ? new Set<NodeFamily>(lens.families) : null
  const out = new Set<ArchNodeType>()
  for (const t of ALL_NODE_TYPES) {
    if (catAllow && !catAllow.has(NODE_CATEGORY[t])) continue
    if (famAllow && !famAllow.has(NODE_FAMILY[t])) continue
    out.add(t)
  }
  return out
}

/**
 * setsEqual — small helper used by the page to decide whether the active
 * chip-set is still the pure lens (no `Custom` marker) or has diverged.
 */
export function setsEqual<T>(a: ReadonlySet<T>, b: ReadonlySet<T>): boolean {
  if (a.size !== b.size) return false
  for (const v of a) if (!b.has(v)) return false
  return true
}
