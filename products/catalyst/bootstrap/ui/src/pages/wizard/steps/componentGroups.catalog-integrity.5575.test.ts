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
import { DEFAULT_COMPONENT_GROUPS, getProfileDefaults } from '../../../entities/deployment/model'

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

// Tracked, non-mandatory debt: an offered component whose Blueprint is not
// built YET. The map value is the reason, so the list can be argued with
// rather than grown by reflex.
//
// UAT row W5 promoted this row to a determinate FAIL on 2026-08-10 with a
// precise complaint about this very list: it held SIX ids, one of them
// (`envoy`) at `tier: 'mandatory'`, and the first test below is written as
// `unresolved === [...KNOWN_UNBUILT]`. A debt list that can absorb a
// MANDATORY entry makes that test unable to fail for the case that matters
// most — the operator cannot deselect a mandatory card, so every single
// deployment emitted an id nothing could install, and the suite stayed
// green. Five of the six are now REMOVED from the catalog outright
// (componentGroups.ts carries the reasoning per family). `specter` stays
// because it is not a phantom third-party card: it is an OpenOva product
// with copy in the marketplace, an entry in the deployment store and a
// documented `familyRequires: ['cortex']` cascade. Deleting it would be a
// product decision; declaring it as bounded, non-mandatory debt is not.
//
// The bound is enforced, not merely written down — see the mandatory-tier
// test below. That is the rule this row was actually failing on.
const KNOWN_UNBUILT: ReadonlyMap<string, string> = new Map([
  [
    'specter',
    'OpenOva AIOps component; componentGroups.ts already records "there is ' +
      'no bp-specter HelmRelease". Optional tier, so an operator can decline ' +
      'it. Refs #5575.',
  ],
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
    const expected = [...KNOWN_UNBUILT.keys()].sort()
    // Bidirectional: no NEW phantom (unresolved ⊆ KNOWN_UNBUILT) AND no stale
    // debt entry (KNOWN_UNBUILT ⊆ unresolved — a built one must leave the list).
    expect(unresolved).toEqual(expected)
  })

  it('no MANDATORY component may be tracked as unbuilt debt (UAT row W5)', () => {
    // The rule the row was failing on, now enforced rather than described.
    // `mandatory` means the operator cannot deselect the card, so a mandatory
    // id with no Blueprint behind it is emitted by EVERY deployment this
    // wizard produces. There is no tier of debt that makes that acceptable,
    // and allowing it is what let the suite stay green while `envoy` shipped
    // as an uninstallable mandatory card.
    const byId = new Map(ALL_COMPONENTS.map(c => [c.id, c]))
    const mandatoryDebt = [...KNOWN_UNBUILT.keys()]
      .filter(id => byId.get(id)?.tier === 'mandatory')
      .sort()
    expect(mandatoryDebt).toEqual([])
  })

  it('every debt entry states a reason (the list is argued with, not grown)', () => {
    for (const [id, reason] of KNOWN_UNBUILT) {
      expect(reason.trim().length, `${id} carries no reason`).toBeGreaterThan(20)
    }
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

  it('every debt entry is a real offered component (no typos)', () => {
    const idSet = new Set(ids)
    for (const id of KNOWN_UNBUILT.keys()) expect(idSet.has(id)).toBe(true)
  })

  it('the DEFAULT/profile selection tables only name real components', () => {
    // The second catalog. `DEFAULT_COMPONENT_GROUPS` (entities/deployment/
    // model.ts) seeds INITIAL_WIZARD_STATE.componentGroups, and
    // getProfileDefaults() adds to it per industry/compliance — neither was
    // ever cross-checked against componentGroups.ts. That is how `envoy`,
    // `frpc` and `strongswan` stayed PRE-SELECTED on a fresh wizard run
    // even though they resolved to no Blueprint: removing their cards would
    // not have removed them from the default selection. An id here that has
    // no ComponentDef is invisible in the UI and still shipped in the
    // payload, which is strictly worse than a visible phantom card.
    const idSet = new Set(ids)
    const profiles: Array<[string, string[], string]> = [
      ['finance', ['PCI DSS'], '50,000'],
      ['healthcare', ['HIPAA'], '500'],
      ['software', ['SOC 2'], '100,000'],
      ['retail', [], '200'],
      ['', [], ''],
    ]
    const seen = new Set<string>()
    for (const list of Object.values(DEFAULT_COMPONENT_GROUPS)) for (const id of list) seen.add(id)
    for (const [ind, comp, size] of profiles) {
      for (const list of Object.values(getProfileDefaults(ind, comp, size))) {
        for (const id of list) seen.add(id)
      }
    }
    const unknown = [...seen].filter(id => !idSet.has(id)).sort()
    expect(unknown).toEqual([])
    // Vacuity: the sweep must actually have collected ids.
    expect(seen.size).toBeGreaterThan(20)
  })

  it('the resolver discriminates — it is not answering "yes" to everything', () => {
    // The vacuity guard, and it had to be rewritten. It used to be
    // `KNOWN_UNBUILT.size > 0`: a proxy that only works while debt exists,
    // and therefore an argument FOR keeping phantoms in the catalog. It also
    // never tested the resolver at all — a resolver that returned `true`
    // unconditionally would still have passed it.
    //
    // This asks the resolver directly, both ways: a control id that certainly
    // has Blueprint sources must resolve, and an id that certainly has none
    // must not. If either direction breaks, the whole file is measuring
    // nothing, and it says so here rather than reporting a clean catalog.
    expect(resolvesToBlueprint('keycloak')).toBe(true)
    expect(resolvesToBlueprint('definitely-not-a-blueprint-xyzzy')).toBe(false)
  })
})
