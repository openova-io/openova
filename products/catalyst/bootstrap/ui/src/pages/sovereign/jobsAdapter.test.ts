/**
 * jobsAdapter — bug #476 regression.
 *
 * The synthesised group slugs (`GROUP_PHASE_0`, `GROUP_CLUSTER_BOOTSTRAP`,
 * `GROUP_APPLICATIONS`) MUST NOT collide with any id `deriveJobs()`
 * emits. When they did (`cluster-bootstrap` group slug == `cluster-
 * bootstrap` leaf id), the downstream layout's parent-chain walk hit
 * a self-reference and the browser hung.
 *
 * This test runs `deriveJobs() → adaptDerivedJobsToFlat()` against the
 * fresh-wizard application set and asserts the slug constants don't
 * appear in the leaf-id set.
 */

import { describe, it, expect } from 'vitest'
import { adaptDerivedJobsToFlat, GROUP_PHASE_0, GROUP_CLUSTER_BOOTSTRAP, GROUP_APPLICATIONS } from './jobsAdapter'
import { deriveJobs } from './jobs'
import { resolveApplications } from './applicationCatalog'
import { buildInitialState } from './eventReducer'
import { INITIAL_WIZARD_STATE } from '@/entities/deployment/model'

describe('jobsAdapter — group-slug ↔ leaf-id collision protection (bug #476)', () => {
  it('GROUP_CLUSTER_BOOTSTRAP slug does NOT match the bare cluster-bootstrap leaf id', () => {
    // The cluster-bootstrap leaf has id='cluster-bootstrap' verbatim
    // (jobs.ts line 210). The group slug must not.
    expect(GROUP_CLUSTER_BOOTSTRAP).not.toBe('cluster-bootstrap')
  })

  it('no group slug collides with any leaf id produced from the default wizard state', () => {
    const apps = resolveApplications(INITIAL_WIZARD_STATE.selectedComponents)
    const state = buildInitialState(apps.map((a) => a.id))
    const derived = deriveJobs(state, apps)
    const flat = adaptDerivedJobsToFlat(derived)

    const leafIds = new Set(flat.filter((j) => j.type !== 'group').map((j) => j.id))
    const groupSlugs: string[] = [GROUP_PHASE_0, GROUP_CLUSTER_BOOTSTRAP, GROUP_APPLICATIONS]
    for (const slug of groupSlugs) {
      expect(leafIds.has(slug)).toBe(false)
    }
  })

  it('emits at least one leaf with id=cluster-bootstrap (sanity — the bare leaf is preserved)', () => {
    const apps = resolveApplications(INITIAL_WIZARD_STATE.selectedComponents)
    const state = buildInitialState(apps.map((a) => a.id))
    const derived = deriveJobs(state, apps)
    const flat = adaptDerivedJobsToFlat(derived)
    const found = flat.find((j) => j.id === 'cluster-bootstrap' && j.type !== 'group')
    expect(found).toBeTruthy()
    // Its parentId must point at the renamed group slug, NOT at itself.
    expect(found!.parentId).toBe(GROUP_CLUSTER_BOOTSTRAP)
    expect(found!.parentId).not.toBe(found!.id)
  })
})

/* ─────────────────────────────────────────────────────────────────
 * Issue #474 — JobsTable rows must NOT render dead phase slugs
 *
 * Phase-8a-preflight first live provision (febeeb888debf477) failed
 * at tofu plan, so the catalyst-api recorded zero jobs. The wizard
 * renders synthetic phase rows from the local event stream regardless
 * (per INVIOLABLE-PRINCIPLES.md #1 — first paint = full list). Pre-fix
 * the synthetic row IDs collided with bare phase slugs (e.g. id was
 * `infrastructure` not `infrastructure:tofu-init`), so clicking a row
 * navigated to /jobs/infrastructure which JobDetail's local jobsById
 * lookup couldn't resolve → "Job not found" panel.
 *
 * The cumulative resolution: PR #480 (rename cluster-bootstrap group
 * slug to phase-1-bootstrap), PR #498 (catalyst-ui fetch via API_BASE),
 * and the synthesised IDs in jobs.ts using the `infrastructure:tofu-*`
 * prefix. This test locks in the contract: every synthesised row's id
 * is one of the well-known forms JobDetail's jobsById accepts.
 * ───────────────────────────────────────────────────────────────── */
describe('jobsAdapter — every synthesised row is a resolvable JobDetail target (issue #474)', () => {
  it('no synthesised row id is a bare phase slug like "infrastructure" or "phase"', () => {
    const apps = resolveApplications(INITIAL_WIZARD_STATE.selectedComponents)
    const state = buildInitialState(apps.map((a) => a.id))
    const derived = deriveJobs(state, apps)
    const flat = adaptDerivedJobsToFlat(derived)
    const FORBIDDEN_BARE_SLUGS = ['infrastructure', 'phase', 'phase-0', 'phase-1', 'cluster']
    for (const j of flat) {
      expect(FORBIDDEN_BARE_SLUGS.includes(j.id)).toBe(false)
    }
  })

  it('every row’s id is non-empty and either a known group slug, a tofu phase id, the bootstrap leaf, or an application id', () => {
    const apps = resolveApplications(INITIAL_WIZARD_STATE.selectedComponents)
    const state = buildInitialState(apps.map((a) => a.id))
    const derived = deriveJobs(state, apps)
    const flat = adaptDerivedJobsToFlat(derived)
    const groupSlugs = new Set([GROUP_PHASE_0, GROUP_CLUSTER_BOOTSTRAP, GROUP_APPLICATIONS])
    const tofuPhases = new Set([
      'infrastructure:tofu-init',
      'infrastructure:tofu-plan',
      'infrastructure:tofu-apply',
      'infrastructure:tofu-output',
    ])
    const appIds = new Set(apps.map((a) => a.id))
    for (const j of flat) {
      expect(j.id).not.toBe('')
      const ok =
        groupSlugs.has(j.id) ||
        tofuPhases.has(j.id) ||
        j.id === 'cluster-bootstrap' ||
        appIds.has(j.id)
      expect(ok, `row id ${j.id} doesn't match any known shape`).toBe(true)
    }
  })

  it('no row id contains an unencoded "/" that would break the /jobs/$jobId route param', () => {
    const apps = resolveApplications(INITIAL_WIZARD_STATE.selectedComponents)
    const state = buildInitialState(apps.map((a) => a.id))
    const derived = deriveJobs(state, apps)
    const flat = adaptDerivedJobsToFlat(derived)
    for (const j of flat) {
      expect(j.id).not.toMatch(/\//)
    }
  })

  it('every leaf row’s parentId resolves to a row already in the flat list (no orphans)', () => {
    const apps = resolveApplications(INITIAL_WIZARD_STATE.selectedComponents)
    const state = buildInitialState(apps.map((a) => a.id))
    const derived = deriveJobs(state, apps)
    const flat = adaptDerivedJobsToFlat(derived)
    const allIds = new Set(flat.map((j) => j.id))
    for (const j of flat) {
      if (j.type === 'group') continue
      // Either the leaf has no parent, or the parent must exist in the
      // same flat list. An orphan leaf would render as un-clickable.
      if (j.parentId !== '') {
        expect(allIds.has(j.parentId), `leaf ${j.id} parent ${j.parentId} not in flat list`).toBe(true)
      }
    }
  })
})
