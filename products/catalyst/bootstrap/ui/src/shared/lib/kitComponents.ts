/**
 * kitComponents — the component metadata the console reads, from generated files only: the catalogue
 * (catalog.generated.ts) and the kit rings and vendored logos (componentGroups.generated.ts, tools/guards/cmd/kit-rings).
 * No hand list, no product family: a component is a catalogue Blueprint, named by its slug (its name without bp-).
 */
import { ALL_BLUEPRINTS, BLUEPRINT_BY_ID } from '@/shared/constants/catalog.generated'
import { RINGS, COMPONENT_LOGOS, type KitRing } from '@/pages/wizard/steps/componentGroups.generated'
import { path as basePath } from '@/shared/config/urls'
import { depsFor } from '@/data/blueprintDeps'

export type Tier = 'mandatory' | 'recommended' | 'optional'

/** id: the slug ("harbor"); name, desc: card.title, card.summary; logoUrl: the vendored logo, null for the letter-mark. */
export interface ComponentEntry {
  id: string
  name: string
  desc: string
  /** The kit ring and its title; both undefined outside the kit, so AppDetail shows no family chip (ADR-0015 D4). */
  product?: string
  groupName?: string
  tier: Tier // 'mandatory' for a kit release (every ring is installed), else 'optional'
  logoUrl: string | null
  dependencies: string[] // slugs it depends on: the kit's dependsOn (blueprintDeps) and the card's depends
}

const RING_OF = new Map<string, KitRing>()
for (const ring of RINGS) for (const r of ring.releases) RING_OF.set(r.blueprint, ring)

const slug = (id: string): string => id.replace(/^bp-/, '')

/** The slugs of every kit release, sorted: the wizard installs every ring, so this is the selection. */
export const KIT_COMPONENT_IDS: readonly string[] = [...RING_OF.keys()].map(slug).sort()

function entry(id: string): ComponentEntry | undefined {
  const b = BLUEPRINT_BY_ID[`bp-${id}`]
  if (!b) return undefined
  const ring = RING_OF.get(b.id)
  const logo = COMPONENT_LOGOS[id]
  return {
    id,
    name: b.title,
    desc: b.summary,
    product: ring?.name,
    groupName: ring?.title,
    tier: ring ? 'mandatory' : 'optional',
    logoUrl: logo ? basePath(`component-logos/${logo}`) : null,
    dependencies: [...new Set([...depsFor(id), ...b.depends.map(slug)])].filter(
      (d) => d !== id && !!BLUEPRINT_BY_ID[`bp-${d}`],
    ),
  }
}

/** Every catalogue Blueprint as a component entry. */
export const ALL_COMPONENTS: readonly ComponentEntry[] = ALL_BLUEPRINTS.map((b) => entry(b.slug)).filter(
  (c): c is ComponentEntry => !!c,
)

export function findComponent(id: string): ComponentEntry | undefined {
  return entry(id)
}

/** Every slug reachable through dependencies, without id itself. */
export function resolveTransitiveDependencies(id: string): string[] {
  const seen = new Set<string>()
  const queue = [...(entry(id)?.dependencies ?? [])]
  while (queue.length > 0) {
    const d = queue.shift()!
    if (d === id || seen.has(d)) continue
    seen.add(d)
    queue.push(...(entry(d)?.dependencies ?? []))
  }
  return [...seen]
}
