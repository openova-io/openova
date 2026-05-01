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
