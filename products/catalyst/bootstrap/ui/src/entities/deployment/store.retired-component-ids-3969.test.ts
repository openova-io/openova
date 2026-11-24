/**
 * store.retired-component-ids-3969.test.ts — UAT row W5 / #6183.
 *
 * Removing a card from `componentGroups.ts` changes the SOURCE catalog. It
 * does not change what is already sitting in an operator's browser: the
 * wizard store is `persist`-wrapped under `openova-catalyst-wizard`, and
 * `partialize()` strips only four credential fields, so a draft written
 * before the removal still names the retired id: in `selectedComponents`
 * (the payload the wizard provisions) and in `componentGroups`, the family
 * map that the store no longer has (SVC-230). `merge()` sets the selection
 * to every kit release and drops the family map.
 *
 * These tests prove the rehydration is safe rather than reasoning about it.
 * `specter` is the subject; `envoy` (retired under #5575) is included so the
 * guard is shown to be about retired ids in general, not about one name.
 */

import { describe, it, expect, beforeEach } from 'vitest'
import { useWizardStore } from './store'
import { INITIAL_WIZARD_STATE } from './model'
import { findComponent, KIT_COMPONENT_IDS } from '@/shared/lib/kitComponents'

const PERSIST_KEY = 'openova-catalyst-wizard'
const CURRENT_VERSION = 1

/**
 * Seed a persist payload at the CURRENT version, so `migrate` does not run
 * and the assertions land on `merge()` — the path every returning operator
 * takes. (Verified empirically in store.org-defaults-rehydrate-5401.test.ts:
 * zustand only calls `migrate` when the stored version is a number and
 * differs, so a version-less payload behaves differently again.)
 */
function seedPayload(state: Record<string, unknown>): void {
  window.localStorage.setItem(
    PERSIST_KEY,
    JSON.stringify({ state, version: CURRENT_VERSION }),
  )
}

beforeEach(() => {
  window.localStorage.clear()
  useWizardStore.setState({ ...INITIAL_WIZARD_STATE })
  window.localStorage.clear()
})

describe('wizard store — a draft naming a retired component id rehydrates cleanly (#3969 / #6183)', () => {
  it('the premise holds: specter and envoy have no ComponentDef', () => {
    // If either ever comes back, the tests below stop testing a retired id
    // and this says so instead of quietly passing.
    expect(findComponent('specter')).toBeUndefined()
    expect(findComponent('envoy')).toBeUndefined()
    // …and the lookup itself works, so `undefined` means absent, not broken.
    expect(findComponent('grafana')).toBeTruthy()
  })

  it('selectedComponents rehydrates to every kit release, whatever the draft named', async () => {
    seedPayload({
      sovereignSubdomain: 'draft-payload-was-read',
      selectedComponents: ['grafana', 'litmus', 'specter', 'envoy'],
    })

    await useWizardStore.persist.rehydrate()
    const s = useWizardStore.getState()

    // VACUITY — prove the seeded payload really was rehydrated. Without
    // this, a merge that discarded the whole payload would pass every
    // assertion below for the wrong reason.
    expect(s.sovereignSubdomain).toBe('draft-payload-was-read')

    expect(KIT_COMPONENT_IDS.length).toBeGreaterThan(0)
    expect(s.selectedComponents).toEqual([...KIT_COMPONENT_IDS])
    for (const id of s.selectedComponents) {
      expect(findComponent(id), `${id} is a kit release with no Blueprint`).toBeTruthy()
    }
  })

  it('a persisted product-family map (componentGroups) is dropped; minio is not carried, seaweedfs is a kit release', async () => {
    seedPayload({
      sovereignSubdomain: 'draft-payload-was-read',
      componentGroups: { insights: ['grafana', 'specter'], silo: ['minio', 'velero'], 'legacy-group': ['envoy'] },
      selectedComponents: ['minio'],
    })

    await useWizardStore.persist.rehydrate()
    const s = useWizardStore.getState()

    expect(s.sovereignSubdomain).toBe('draft-payload-was-read')
    expect(Object.keys(s)).not.toContain('componentGroups')
    expect(s.selectedComponents).not.toContain('minio')
    expect(s.selectedComponents).toContain('seaweedfs')
  })

  it('a draft that names ONLY retired ids does not crash and falls back to the defaults', async () => {
    // The degenerate case — the filter empties the array. `merge()`'s
    // first-run fallback must then seed the default selection rather than
    // hand the wizard an empty payload.
    seedPayload({
      sovereignSubdomain: 'draft-payload-was-read',
      selectedComponents: ['specter', 'envoy', 'frpc', 'strongswan', 'superset', 'ntfy'],
    })

    await useWizardStore.persist.rehydrate()
    const s = useWizardStore.getState()

    expect(s.sovereignSubdomain).toBe('draft-payload-was-read')
    expect(s.selectedComponents).toEqual([...KIT_COMPONENT_IDS])
  })
})
