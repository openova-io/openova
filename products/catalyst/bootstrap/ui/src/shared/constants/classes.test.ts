/**
 * classes.test.ts — SVC-180: the class labels every screen reads, the
 * grouping order, and the parity of the class views of the left menu
 * (products/catalyst/bootstrap/api/internal/consolemenu) with this file.
 */

import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { ALL_BLUEPRINTS } from './catalog.generated'
import {
  CLASS_LABELS,
  CLASS_ORDER,
  DEFAULT_CLASS,
  PLATFORM_CLASS,
  RESOURCES_CLASS_KIND,
  classOfBlueprint,
  groupByClass,
  isBlueprintClass,
  resourcesClassRoute,
} from './classes'
import { isValidKind } from '@/pages/sovereign/cloud-list/kinds'

describe('classes.ts (SVC-180)', () => {
  it('holds the six classes in the grouping order', () => {
    expect(CLASS_ORDER).toEqual([
      'application',
      'data-service',
      'network-security-service',
      'ai-service',
      'infrastructure',
      'platform-component',
    ])
    expect(DEFAULT_CLASS).toBe('application')
    expect(PLATFORM_CLASS).toBe('platform-component')
  })

  it('knows every class a Blueprint of the generated catalogue says', () => {
    for (const b of ALL_BLUEPRINTS) expect(isBlueprintClass(b.class), b.id).toBe(true)
  })

  it('gives every class a singular and a plural label', () => {
    for (const c of CLASS_ORDER) {
      expect(CLASS_LABELS[c].singular.trim()).not.toBe('')
      expect(CLASS_LABELS[c].plural.trim()).not.toBe('')
    }
    expect(CLASS_LABELS['data-service']).toEqual({ singular: 'Data service', plural: 'Data services' })
  })

  it('isBlueprintClass refuses what is not a class', () => {
    for (const v of ['', 'database', 'Application', undefined, null, 3]) {
      expect(isBlueprintClass(v)).toBe(false)
    }
  })

  it('classOfBlueprint reads the catalogue, with or without bp-, and defaults to application', () => {
    expect(classOfBlueprint('bp-cilium')).toBe('platform-component')
    expect(classOfBlueprint('cilium')).toBe('platform-component')
    expect(classOfBlueprint('bp-postgres')).toBe('data-service')
    expect(classOfBlueprint('bp-no-such-blueprint')).toBe('application')
    expect(classOfBlueprint('')).toBe('application')
    expect(classOfBlueprint(null)).toBe('application')
  })

  it('groupByClass keeps the class order and the order inside a group, and drops empty classes', () => {
    const items = [
      { id: 'a', c: 'platform-component' },
      { id: 'b', c: 'application' },
      { id: 'c', c: 'data-service' },
      { id: 'd', c: 'application' },
    ] as const
    const groups = groupByClass(items, (i) => i.c)
    expect(groups.map((g) => g.cls)).toEqual(['application', 'data-service', 'platform-component'])
    expect(groups[0].items.map((i) => i.id)).toEqual(['b', 'd'])
    expect(groups[0].label.plural).toBe('Applications')
    const noPlatform = groupByClass(items, (i) => i.c, CLASS_ORDER.filter((c) => c !== PLATFORM_CLASS))
    expect(noPlatform.map((g) => g.cls)).toEqual(['application', 'data-service'])
  })

  it('the class view opens a Resources list that exists, filtered by the class', () => {
    expect(isValidKind(RESOURCES_CLASS_KIND)).toBe(true)
    expect(resourcesClassRoute('ai-service')).toBe('/cloud?view=list&kind=helmreleases&class=ai-service')
  })

  it('each class view of the left menu carries the plural label and the route of this file', () => {
    // The console reads the Go list of console pages over the API; this
    // keeps its labels and routes equal to the ones every screen shows.
    const goFile = resolve(__dirname, '../../../../api/internal/consolemenu/consolemenu.go')
    const go = readFileSync(goFile, 'utf8')
    const views = go.split('\n').filter((l) => l.includes('Name: "class-'))
    expect(views.length).toBe(CLASS_ORDER.length)
    for (const c of CLASS_ORDER) {
      const want = `{Name: "class-${c}", Label: "${CLASS_LABELS[c].plural}", Route: "${resourcesClassRoute(c)}",`
      expect(views.some((l) => l.includes(want)), want).toBe(true)
    }
  })
})
