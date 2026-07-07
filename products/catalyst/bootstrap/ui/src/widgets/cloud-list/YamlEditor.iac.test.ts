/**
 * YamlEditor.iac.test.ts — #4843 regression: the "Edit IaC" editor must seed
 * COMMITTABLE YAML from a live Blueprint CR.
 *
 * Root cause (UAT hw228 rows 148/154): the live CR carries
 * `metadata.managedFields` whose entries hold empty `fieldsV1: {}` maps.
 * objectToYAML() rendered an empty MAP as a bare `{}` pushed onto its own
 * UNINDENTED line, producing invalid YAML — `PUT /catalog/{bp}/iac` then 400'd
 * (`yaml: line N: could not find expected ':'`). Two fixes, both asserted here:
 *   1. objectToYAML inlines empty objects (`k: {}`) like it already did empty
 *      arrays (`k: []`).
 *   2. stripServerFields removes managedFields/status/runtime metadata from the
 *      seed (matching the server-side application_iac_git.go sanitiser).
 */

import { describe, it, expect } from 'vitest'
import { load as parseYAML } from 'js-yaml'

import { objectToYAML, stripServerFields } from './YamlEditor'
import type { K8sObject } from '@/widgets/architecture-graph/useK8sCacheStream'

describe('#4843 objectToYAML empty-container handling', () => {
  it('inlines an empty object as `k: {}` (not a bare {} on its own line)', () => {
    const out = objectToYAML({ metadata: { fieldsV1: {} } })
    // The empty map must be inline; a bare `{}` at column 0 is the bug.
    expect(out).not.toMatch(/^\{\}$/m)
    expect(out).toContain('fieldsV1: {}')
    // …and the whole thing must be valid YAML that round-trips.
    expect(() => parseYAML(out)).not.toThrow()
  })

  it('still inlines an empty array as `k: []`', () => {
    expect(objectToYAML({ items: [] })).toContain('items: []')
  })

  it('keeps non-empty nested objects on indented lines', () => {
    const out = objectToYAML({ spec: { version: '1.2.3' } })
    expect(out).toContain('spec:\n  version: 1.2.3')
    expect(() => parseYAML(out)).not.toThrow()
  })
})

describe('#4843 stripServerFields', () => {
  const liveCR: K8sObject = {
    apiVersion: 'catalog.openova.io/v1alpha1',
    kind: 'Blueprint',
    metadata: {
      name: 'bp-wordpress',
      resourceVersion: '918273',
      uid: 'abc-123',
      generation: 4,
      creationTimestamp: '2026-07-06T00:00:00Z',
      managedFields: [
        { manager: 'flux', operation: 'Apply', fieldsType: 'FieldsV1', fieldsV1: {} },
      ],
    },
    spec: { version: '0.4.12', manifests: { chart: 'bp-wordpress-tenant' } },
    status: { phase: 'Ready' },
  } as unknown as K8sObject

  it('drops managedFields/status/runtime metadata but keeps the authored contract', () => {
    const clean = stripServerFields(liveCR) as Record<string, unknown>
    const meta = clean.metadata as Record<string, unknown>
    expect(meta.managedFields).toBeUndefined()
    expect(meta.resourceVersion).toBeUndefined()
    expect(meta.uid).toBeUndefined()
    expect(meta.generation).toBeUndefined()
    expect(meta.creationTimestamp).toBeUndefined()
    expect(clean.status).toBeUndefined()
    // User-authored fields survive.
    expect(meta.name).toBe('bp-wordpress')
    expect(clean.spec).toEqual({ version: '0.4.12', manifests: { chart: 'bp-wordpress-tenant' } })
  })

  it('does not mutate the source object (live query cache safety)', () => {
    stripServerFields(liveCR)
    expect((liveCR.metadata as Record<string, unknown>).managedFields).toBeDefined()
    expect((liveCR as Record<string, unknown>).status).toBeDefined()
  })

  it('end-to-end: a live CR with managedFields seeds COMMITTABLE YAML', () => {
    // This is the exact hw228 148/154 failure path: seed the editor the way
    // YamlEditor does (stripServerFields → objectToYAML) and prove it parses.
    const seed = objectToYAML(stripServerFields(liveCR))
    expect(seed).not.toMatch(/^\{\}$/m)
    const parsed = parseYAML(seed) as Record<string, unknown>
    expect(parsed.kind).toBe('Blueprint')
    expect((parsed.metadata as Record<string, unknown>).name).toBe('bp-wordpress')
    expect((parsed.spec as Record<string, unknown>).version).toBe('0.4.12')
    expect((parsed.metadata as Record<string, unknown>).managedFields).toBeUndefined()
  })
})
