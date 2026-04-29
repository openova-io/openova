/**
 * applicationCatalog.ts — resolves the set of Applications a Sovereign
 * Admin landing page renders.
 *
 * Inputs:
 *   • BOOTSTRAP_KIT (catalog.generated.ts) — 11 always-installed
 *     Blueprints (cilium, cert-manager, flux, crossplane, sealed-secrets,
 *     spire, nats-jetstream, openbao, keycloak, gitea,
 *     bp-catalyst-platform).
 *   • The wizard store's `selectedComponents` (string[] of bare ids
 *     without the bp- prefix, e.g. "harbor", "kserve", "axon"). Each
 *     gets normalised to its Blueprint id for display alongside the
 *     bootstrap-kit set.
 *
 * Output: a stable, deduplicated, dependency-ordered list of
 * Application descriptors for the AdminPage card grid.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), every value
 * here is computed from the catalog and componentGroups module — there
 * is no hand-maintained id list. Adding a new Blueprint to the catalog
 * makes it eligible for the grid automatically.
 *
 * Per #2 (never compromise), bootstrap-kit Applications and selected
 * Applications use the SAME ApplicationDescriptor shape — the AdminPage
 * doesn't need to special-case "is this a bootstrap component".
 */

import { BOOTSTRAP_KIT } from '@/shared/constants/catalog.generated'
import {
  ALL_COMPONENTS,
  findComponent,
  resolveTransitiveDependencies,
  type ComponentEntry,
} from '@/pages/wizard/steps/componentGroups'
import { normaliseComponentId } from './eventReducer'

export interface ApplicationDescriptor {
  /** Canonical Blueprint id ("bp-<slug>"). */
  id: string
  /** Bare id without bp- prefix — used to look up componentGroups metadata. */
  bareId: string
  /** Display title (componentGroups name, falls back to slug). */
  title: string
  /** One-paragraph description from the catalog (6-10 word target). */
  description: string
  /** Family/group id (pilot, spine, …) for chip palette + sidebar grouping. */
  familyId: string
  /** Display name of the family (PILOT, SPINE, …). */
  familyName: string
  /** Tier — mandatory / recommended / optional. */
  tier: ComponentEntry['tier']
  /** Optional logo url (vendored under public/component-logos/<id>.svg). */
  logoUrl: string | null | undefined
  /** Component-level dependency ids (bare). Surfaced on the Dependencies tab. */
  dependencies: string[]
  /** True when this Application is part of the always-installed bootstrap kit. */
  bootstrapKit: boolean
}

/**
 * Build the set of Applications the AdminPage renders for the given
 * `selectedComponents` list. Always includes the BOOTSTRAP_KIT eleven,
 * then expands the selection (with transitive dependencies) and unions.
 *
 * Order: bootstrap-kit first (in install order), then user selection
 * sorted by family then by name. Stable across renders.
 */
export function resolveApplications(
  selectedComponents: readonly string[],
): ApplicationDescriptor[] {
  const out: ApplicationDescriptor[] = []
  const seen = new Set<string>()

  // 1. Bootstrap kit — always present, in numerical install order.
  for (const b of BOOTSTRAP_KIT) {
    if (seen.has(b.id)) continue
    seen.add(b.id)
    // Look up by bare slug so descriptions / family come from componentGroups
    // when available; fall back to the catalog summary if the component
    // isn't represented in componentGroups (e.g. bp-bp-catalyst-platform).
    const bare = b.slug
    const compEntry = findComponent(bare)
    out.push(makeDescriptor({
      blueprintId: b.id,
      bareId: bare,
      fallbackTitle: b.label,
      compEntry,
      bootstrapKit: true,
    }))
  }

  // 2. User selection — expand with transitive component deps so the
  // grid renders every component the operator's choice actually pulls
  // in. Mandatory components from the catalog are also always added.
  const seedIds = new Set<string>()
  for (const c of ALL_COMPONENTS) {
    if (c.tier === 'mandatory') seedIds.add(c.id)
  }
  for (const id of selectedComponents) seedIds.add(id)

  const expanded = new Set<string>()
  for (const seed of seedIds) {
    expanded.add(seed)
    for (const d of resolveTransitiveDependencies(seed)) expanded.add(d)
  }

  // Stable order: family id then component name.
  const ordered = [...expanded]
    .map((id) => findComponent(id))
    .filter((c): c is ComponentEntry => !!c)
    .sort((a, b) => {
      if (a.product !== b.product) return a.product.localeCompare(b.product)
      return a.name.localeCompare(b.name)
    })

  for (const c of ordered) {
    const blueprintId = normaliseComponentId(c.id)
    if (!blueprintId) continue
    if (seen.has(blueprintId)) continue
    seen.add(blueprintId)
    out.push(makeDescriptor({
      blueprintId,
      bareId: c.id,
      fallbackTitle: c.name,
      compEntry: c,
      bootstrapKit: false,
    }))
  }

  return out
}

interface MakeDescriptorArgs {
  blueprintId: string
  bareId: string
  fallbackTitle: string
  compEntry: ComponentEntry | undefined
  bootstrapKit: boolean
}

function makeDescriptor(args: MakeDescriptorArgs): ApplicationDescriptor {
  const { blueprintId, bareId, fallbackTitle, compEntry, bootstrapKit } = args
  return {
    id: blueprintId,
    bareId,
    title: compEntry?.name ?? fallbackTitle,
    description: compEntry?.desc ?? '',
    familyId: compEntry?.product ?? 'platform',
    familyName: compEntry?.groupName ?? 'Platform',
    tier: compEntry?.tier ?? 'mandatory',
    logoUrl: compEntry?.logoUrl ?? null,
    dependencies: compEntry?.dependencies ?? [],
    bootstrapKit,
  }
}

/**
 * Reverse-lookup — for the ApplicationPage's "depended on by" list.
 * Returns every component id whose `dependencies[]` contains the given
 * id. Sourced from `findDependents()` in componentGroups but lifted to
 * this module so the AdminPage's resolver and the ApplicationPage's
 * panel share one import surface.
 */
export function reverseDependencies(bareId: string): string[] {
  return ALL_COMPONENTS.filter((c) => (c.dependencies ?? []).includes(bareId)).map(
    (c) => c.id,
  )
}

/** Lookup a descriptor inside a list by Blueprint id ("bp-<slug>"). */
export function findApplication(
  apps: readonly ApplicationDescriptor[],
  blueprintId: string,
): ApplicationDescriptor | undefined {
  return apps.find((a) => a.id === blueprintId)
}
