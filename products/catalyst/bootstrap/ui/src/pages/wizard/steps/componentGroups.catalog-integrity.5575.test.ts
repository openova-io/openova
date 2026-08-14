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
// IT IS NOW EMPTY, and that is the point of UAT row W5 (#3969).
//
// History, because an empty allowlist invites someone to re-open it. The row
// promoted to a determinate FAIL on 2026-08-10 against this very list: it
// held SIX ids, one of them (`envoy`) at `tier: 'mandatory'`. The first test
// below reads `unresolved === [...KNOWN_UNBUILT]`, so a debt list able to
// absorb a MANDATORY entry made that test unable to fail for the case that
// mattered most — the operator cannot deselect a mandatory card, so every
// deployment emitted an id nothing could install while the suite stayed
// green. Five of the six were removed from the catalog outright.
//
// The sixth, `specter`, was kept on the argument that it is an OpenOva
// product rather than a phantom third-party card. That argument does not
// survive contact with what the card DID: at `tier: 'optional'` with
// `familyRequires: ['cortex']`, ticking a card that deploys nothing
// cascaded NINE real CORTEX components into the operator's footprint. And
// the two ways out are not symmetric — removal empties this map with no
// guard change, whereas disabling the card would require teaching the test
// to skip disabled cards, i.e. weakening a guard so a row can pass. It was
// removed under #6183; building the real thing is #6318.
//
// Adding an entry here is therefore a deliberate act with two costs, both
// enforced below rather than merely written down: it must carry a reason,
// and it may never name a mandatory-tier component.
const KNOWN_UNBUILT: ReadonlyMap<string, string> = new Map([])

// dependsOn targets that are legitimate infra edges but not selectable
// components (e.g. the gateway-api bootstrap-kit slot). Tracked, Refs #5575.
const KNOWN_INFRA_DEPS: ReadonlySet<string> = new Set(['gateway-api'])

/* ── The three debt-list rules, as predicates over an arbitrary map ───
 *
 * They take the map as a PARAMETER rather than closing over KNOWN_UNBUILT
 * for one reason: KNOWN_UNBUILT is empty now, so a rule written as a loop
 * over it cannot fail — `for (const x of []) expect(…)` is green whatever
 * the rule says. That is the dominant defect class in this repo's guards
 * (a check whose subject cannot fail), and emptying the list is exactly
 * the moment it would have been introduced.
 *
 * Each rule is therefore asserted TWICE below: once against the real map
 * (must be clean) and once against a synthetic map built to violate it
 * (must be caught). The second call is what proves the first one measured
 * anything.
 */

type DebtMap = ReadonlyMap<string, string>

/** Debt ids whose catalog tier is `mandatory` — never allowed. */
function mandatoryDebt(debt: DebtMap): string[] {
  const byId = new Map(ALL_COMPONENTS.map(c => [c.id, c]))
  return [...debt.keys()].filter(id => byId.get(id)?.tier === 'mandatory').sort()
}

/** Debt ids whose reason is missing or too thin to argue with. */
function unreasonedDebt(debt: DebtMap): string[] {
  return [...debt.entries()]
    .filter(([, reason]) => reason.trim().length <= 20)
    .map(([id]) => id)
    .sort()
}

/** Debt ids that are not offered components at all (typos, stale names). */
function debtIdsNotInCatalog(debt: DebtMap): string[] {
  const idSet = new Set(ALL_COMPONENTS.map(c => c.id))
  return [...debt.keys()].filter(id => !idSet.has(id)).sort()
}

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

  it('the debt list is EMPTY — every offered component resolves (UAT row W5)', () => {
    // Stated as its own assertion rather than left implicit in the
    // bidirectional test above, because "unresolved equals the allowlist" is
    // satisfied by ANY consistent pair. This is the row's actual clause:
    // every component id resolves to a real Blueprint, with no exception
    // carried on the side. Refs #3969.
    expect([...KNOWN_UNBUILT.keys()]).toEqual([])
    expect(ids.filter(id => !resolvesToBlueprint(id))).toEqual([])
    // …and the sweep really examined the catalog.
    expect(ids.length).toBeGreaterThan(50)
  })

  it('no MANDATORY component may be tracked as unbuilt debt (UAT row W5)', () => {
    // The rule the row was failing on, enforced rather than described.
    // `mandatory` means the operator cannot deselect the card, so a mandatory
    // id with no Blueprint behind it is emitted by EVERY deployment this
    // wizard produces. There is no tier of debt that makes that acceptable,
    // and allowing it is what let the suite stay green while `envoy` shipped
    // as an uninstallable mandatory card.
    expect(mandatoryDebt(KNOWN_UNBUILT)).toEqual([])

    // The rule can still FIRE now that the real list is empty. `flux` is
    // mandatory in the live catalog, so a debt map naming it must be caught.
    const mandatoryId = ALL_COMPONENTS.find(c => c.tier === 'mandatory')!.id
    expect(
      mandatoryDebt(new Map([[mandatoryId, 'synthetic entry, long enough to pass the reason rule']])),
    ).toEqual([mandatoryId])
  })

  it('every debt entry states a reason (the list is argued with, not grown)', () => {
    expect(unreasonedDebt(KNOWN_UNBUILT)).toEqual([])

    // Fires on a real violation — a one-word reason is not an argument.
    expect(unreasonedDebt(new Map([['grafana', 'later']]))).toEqual(['grafana'])
    // …and does NOT fire on a reason that genuinely says something.
    expect(
      unreasonedDebt(new Map([['grafana', 'a reason long enough to be an actual argument']])),
    ).toEqual([])
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
    expect(debtIdsNotInCatalog(KNOWN_UNBUILT)).toEqual([])

    // Fires on an id that is not in the catalog — the shape a rename or a
    // typo leaves behind, and the shape `specter` itself now has.
    expect(debtIdsNotInCatalog(new Map([['specter', 'removed under #6183']]))).toEqual(['specter'])
    // …and passes a real one, so it is not simply rejecting everything.
    expect(debtIdsNotInCatalog(new Map([['grafana', 'a real catalog id']]))).toEqual([])
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
