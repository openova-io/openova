/**
 * classes.ts — the Blueprint classes (spec.class) as the console shows them.
 *
 * Every screen that names a class reads its label here: the Applications
 * page and the Catalog page group their cards by class, and Resources groups
 * and filters the rows that carry a Blueprint by class. The class of a
 * Blueprint comes from the build-time catalogue (catalog.generated.ts, written
 * by scripts/generate-catalog.py from each blueprint.yaml); a Blueprint the
 * catalogue does not know is an application, the default class.
 *
 * The class views of Resources are menu candidates of the source page
 * (products/catalyst/bootstrap/api/internal/consolemenu). Their labels and
 * routes there must equal CLASS_LABELS[c].plural and resourcesClassRoute(c);
 * classes.test.ts reads that Go list and fails when the two disagree.
 */

import { BLUEPRINT_BY_ID, type BlueprintClass } from './catalog.generated'

export type { BlueprintClass }

export interface ClassLabel {
  /** One thing of the class: "Data service". */
  singular: string
  /** The class as a group: "Data services". */
  plural: string
}

/** The six classes, in the order every screen groups them. */
export const CLASS_ORDER: readonly BlueprintClass[] = [
  'application',
  'data-service',
  'network-security-service',
  'ai-service',
  'infrastructure',
  'platform-component',
]

/** The screen labels of each class. */
export const CLASS_LABELS: Readonly<Record<BlueprintClass, ClassLabel>> = {
  application: { singular: 'Application', plural: 'Applications' },
  'data-service': { singular: 'Data service', plural: 'Data services' },
  'network-security-service': {
    singular: 'Network and security service',
    plural: 'Network and security services',
  },
  'ai-service': { singular: 'AI service', plural: 'AI services' },
  infrastructure: { singular: 'Infrastructure', plural: 'Infrastructure' },
  'platform-component': { singular: 'Platform component', plural: 'Platform components' },
}

/** The class of a Blueprint the catalogue does not know. */
export const DEFAULT_CLASS: BlueprintClass = 'application'

/** The class whose rows the Applications page leaves to Resources. */
export const PLATFORM_CLASS: BlueprintClass = 'platform-component'

export function isBlueprintClass(value: unknown): value is BlueprintClass {
  return typeof value === 'string' && (CLASS_ORDER as readonly string[]).includes(value)
}

/** The class of a Blueprint, from its id with or without the bp- prefix. */
export function classOfBlueprint(id: string | null | undefined): BlueprintClass {
  if (!id) return DEFAULT_CLASS
  const full = id.startsWith('bp-') ? id : `bp-${id}`
  return BLUEPRINT_BY_ID[full]?.class ?? DEFAULT_CLASS
}

export interface ClassGroup<T> {
  cls: BlueprintClass
  label: ClassLabel
  items: T[]
}

/**
 * groupByClass — one group per class that has an item, in CLASS_ORDER. The
 * items of a group keep their order. A class missing from `order` drops its
 * items.
 */
export function groupByClass<T>(
  items: readonly T[],
  classOf: (item: T) => BlueprintClass,
  order: readonly BlueprintClass[] = CLASS_ORDER,
): ClassGroup<T>[] {
  const byClass = new Map<BlueprintClass, T[]>()
  for (const item of items) {
    const cls = classOf(item)
    const list = byClass.get(cls)
    if (list) list.push(item)
    else byClass.set(cls, [item])
  }
  const out: ClassGroup<T>[] = []
  for (const cls of order) {
    const list = byClass.get(cls)
    if (list && list.length > 0) out.push({ cls, label: CLASS_LABELS[cls], items: list })
  }
  return out
}

/** The search parameter of Resources that holds the class filter. */
export const RESOURCES_CLASS_PARAM = 'class'

/** The Resources list that a class view opens: one row per installed Blueprint. */
export const RESOURCES_CLASS_KIND = 'helmreleases'

/** The address of the class view of Resources for one class. */
export function resourcesClassRoute(cls: BlueprintClass): string {
  return `/cloud?view=list&kind=${RESOURCES_CLASS_KIND}&${RESOURCES_CLASS_PARAM}=${cls}`
}
