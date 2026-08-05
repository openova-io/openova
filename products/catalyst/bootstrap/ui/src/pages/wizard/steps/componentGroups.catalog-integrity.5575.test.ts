// componentGroups.catalog-integrity.5575.test.ts — the fail-CLOSED wizard
// catalog validator (#5575).
//
// #5575 root cause: nothing validated the wizard's hand-maintained component
// catalog against the actual Blueprint sources, and the vitest gate that would
// have caught it was fail-open. Result: 6 offered components resolve to no
// Blueprint (select → nothing deploys), and 8 carry dependency ids that are not
// components. This test is the fail-closed contract:
//
//   1. Every ALL_COMPONENTS id must resolve to a Blueprint source
//      (platform/<id>, products/<id>, a bp-<id> blueprint.yaml, a
//      platform/bp-*-<id> alias, or a bootstrap-kit slot *-<id>.yaml) UNLESS it
//      is in KNOWN_UNBUILT — the explicit, tracked debt list for #5575.
//   2. Every component dependency must reference a real component id UNLESS it
//      is in KNOWN_INFRA_DEPS (infra targets that are dependsOn edges but not
//      selectable components).
//
// Both checks are BIDIRECTIONAL: an entry in an allowlist that no longer
// applies (e.g. someone finally ships bp-envoy) fails too, so the debt list
// self-cleans instead of rotting. A NEW phantom / dangling dep fails CI — the
// fail-open hole #5575 reported is closed.

import { existsSync, readdirSync, readFileSync, statSync } from 'node:fs'
import { resolve, join } from 'node:path'
import { describe, it, expect } from 'vitest'

import { ALL_COMPONENTS } from './componentGroups'

// ── locate the monorepo root (contains platform/ + products/ + clusters/) ────
function repoRoot(): string {
  if (process.env.OPENOVA_REPO_ROOT) return resolve(process.env.OPENOVA_REPO_ROOT)
  let dir = process.cwd()
  for (let i = 0; i < 12; i++) {
    if (
      existsSync(join(dir, 'platform')) &&
      existsSync(join(dir, 'products')) &&
      existsSync(join(dir, 'clusters'))
    )
      return dir
    const up = resolve(dir, '..')
    if (up === dir) break
    dir = up
  }
  throw new Error('could not locate monorepo root (platform/ + products/ + clusters/)')
}

const ROOT = repoRoot()

// The 6 components #5575 found to resolve to no Blueprint. Tracked here as
// EXPLICIT debt (Refs #5575) so they are visible, not hidden — the disposition
// (remove vs. mark coming-soon vs. build the blueprint) is a product decision;
// this list keeps the fail-closed gate green until then and blocks any NEW one.
const KNOWN_UNBUILT: ReadonlySet<string> = new Set([
  'envoy', // tier: mandatory, but the real envoy ships inside bp-cilium (cilium-envoy)
  'frpc',
  'strongswan',
  'superset',
  'ntfy',
  'specter', // componentGroups.ts already comments "there is no bp-specter HelmRelease"
])

// dependsOn targets that are legitimate infra edges but not selectable
// components (e.g. the gateway-api bootstrap-kit slot). Tracked, Refs #5575.
const KNOWN_INFRA_DEPS: ReadonlySet<string> = new Set(['gateway-api'])

function ls(dir: string): string[] {
  try {
    return readdirSync(dir)
  } catch {
    return []
  }
}

// Faithful reimplementation of #5575's resolution query.
function resolvesToBlueprint(id: string): boolean {
  const platform = join(ROOT, 'platform')
  const products = join(ROOT, 'products')
  const kit = join(ROOT, 'clusters', '_template', 'bootstrap-kit')

  // 1. direct platform/<id> or products/<id> directory
  for (const base of [platform, products]) {
    const d = join(base, id)
    if (existsSync(d) && statSync(d).isDirectory()) return true
  }
  // 2. alias dirs: platform/bp-*-<id>, products/*-<id> or products/<id>-* (e.g. vcluster)
  for (const base of [platform, products]) {
    for (const name of ls(base)) {
      if (name === `bp-${id}` || name.endsWith(`-${id}`) || name.startsWith(`${id}-`)) {
        if (statSync(join(base, name)).isDirectory()) return true
      }
    }
  }
  // 3. bootstrap-kit slot NN-<id>.yaml or NN-bp-<id>.yaml
  for (const name of ls(kit)) {
    if (name.endsWith(`-${id}.yaml`) || name.endsWith(`-bp-${id}.yaml`)) return true
  }
  // 4. a bp-<id> reference inside any blueprint.yaml (covers vcluster → bp-*-vcluster)
  for (const base of [platform, products]) {
    for (const name of ls(base)) {
      const bp = join(base, name, 'blueprint.yaml')
      if (existsSync(bp)) {
        const txt = readFileSync(bp, 'utf8')
        if (txt.includes(`bp-${id}`) || new RegExp(`name:\\s*bp-.*${id}`).test(txt)) return true
      }
    }
  }
  return false
}

describe('#5575 wizard component catalog integrity (fail-closed)', () => {
  const ids = ALL_COMPONENTS.map(c => c.id)

  it('every offered component resolves to a Blueprint, except tracked KNOWN_UNBUILT debt', () => {
    const unresolved = ids.filter(id => !resolvesToBlueprint(id)).sort()
    const expected = [...KNOWN_UNBUILT].sort()
    // Bidirectional: no NEW phantom (unresolved ⊆ KNOWN_UNBUILT) AND no stale
    // debt entry (KNOWN_UNBUILT ⊆ unresolved — a built one must leave the list).
    expect(unresolved).toEqual(expected)
  })

  it('every component dependency references a real component or tracked infra dep', () => {
    const idSet = new Set(ids)
    const dangling = new Set<string>()
    for (const c of ALL_COMPONENTS) {
      for (const dep of (c as { dependencies?: string[] }).dependencies ?? []) {
        if (!idSet.has(dep) && !KNOWN_INFRA_DEPS.has(dep)) dangling.add(dep)
      }
    }
    expect([...dangling].sort()).toEqual([])
  })

  it('the debt allowlist is non-empty and every entry is a real offered component (no typos)', () => {
    // Vacuity: if the resolver silently broke and marked everything resolved,
    // the first test would demand an EMPTY KNOWN_UNBUILT — this guards the other
    // direction (the allowlist must reference ids that actually exist).
    expect(KNOWN_UNBUILT.size).toBeGreaterThan(0)
    const idSet = new Set(ids)
    for (const id of KNOWN_UNBUILT) expect(idSet.has(id)).toBe(true)
  })
})
