/**
 * presets.ts — cross-cutting LENSES for the unified Cloud-graph (#3958).
 *
 * A preset is a saved triple of {categories (shapes), families (borders),
 * edgeKinds}. Selecting one narrows the canvas to nodes whose CATEGORY is
 * in the preset's category set AND whose FAMILY is in the preset's family
 * set — layered ON TOP of the per-type chip toggles (the chip toggles
 * stay the fine-grained control; the preset is the coarse lens).
 *
 * Two flavours, per the founder-signed design:
 *   • Domain lenses  — All / Runtime / Cloud / Reconciliation /
 *                      Networking / Data / Security. Cut by CATEGORY
 *                      (every family allowed).
 *   • Family lenses  — Flux / Crossplane / cert-manager / CNPG /
 *                      External-Secrets / Cilium / Catalyst CRs. Cut by
 *                      FAMILY (every category allowed).
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 — the preset table is data; the
 * page reads it, no hardcoded lens logic at the call site.
 */

import {
  ALL_CATEGORIES,
  ALL_FAMILIES,
  ALL_NODE_TYPES,
  NODE_CATEGORY,
  NODE_FAMILY,
  type ArchEdgeType,
  type ArchNodeType,
  type NodeCategory,
  type NodeFamily,
} from './types'

export type PresetId =
  // Domain lenses
  | 'all'
  | 'runtime'
  | 'cloud'
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

export interface Preset {
  id: PresetId
  label: string
  /** 'domain' lenses cut by category; 'family' lenses cut by family. */
  group: 'domain' | 'family'
  /** Active categories (shapes). Empty/omitted = all categories. */
  categories?: NodeCategory[]
  /** Active families (borders). Empty/omitted = all families. */
  families?: NodeFamily[]
  /** Active edge kinds. Omitted = all edge kinds. */
  edgeKinds?: ArchEdgeType[]
}

/** The preset table — single source of truth for the dropdown. */
export const PRESETS: Record<PresetId, Preset> = {
  // ── Domain lenses (cut by category) ──────────────────────────────
  all: { id: 'all', label: 'All', group: 'domain' },
  runtime: {
    id: 'runtime',
    label: 'Runtime',
    group: 'domain',
    categories: ['compute', 'scope'],
  },
  cloud: {
    id: 'cloud',
    label: 'Cloud',
    group: 'domain',
    categories: ['scope', 'compute', 'network'],
    families: ['cloud'],
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
    label: 'Catalyst CRs',
    group: 'family',
    families: ['catalyst'],
  },
}

/** Ordered list for the dropdown — domain lenses first, then family. */
export const PRESET_ORDER: PresetId[] = [
  'all',
  'runtime',
  'cloud',
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

/**
 * presetHiddenTypes — the set of ArchNodeTypes a preset HIDES. A type is
 * hidden when its category isn't in the preset's category allow-list OR
 * its family isn't in the preset's family allow-list. The 'all' preset
 * (and any preset with neither constraint) hides nothing. This composes
 * with the per-type chip `hiddenTypes` via set-union downstream.
 */
export function presetHiddenTypes(preset: Preset): Set<ArchNodeType> {
  const hidden = new Set<ArchNodeType>()
  const catAllow = new Set<NodeCategory>(preset.categories ?? ALL_CATEGORIES)
  const famAllow = new Set<NodeFamily>(preset.families ?? ALL_FAMILIES)
  const allCats = !preset.categories || preset.categories.length === 0
  const allFams = !preset.families || preset.families.length === 0
  if (allCats && allFams) return hidden // 'all' / unconstrained → hide nothing
  for (const t of ALL_NODE_TYPES) {
    const cat = NODE_CATEGORY[t]
    const fam = NODE_FAMILY[t]
    if (!catAllow.has(cat) || !famAllow.has(fam)) hidden.add(t)
  }
  return hidden
}
