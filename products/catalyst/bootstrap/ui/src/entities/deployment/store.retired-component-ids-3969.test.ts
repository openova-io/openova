/**
 * store.retired-component-ids-3969.test.ts — UAT row W5 / #6183.
 *
 * Removing a card from `componentGroups.ts` changes the SOURCE catalog. It
 * does not change what is already sitting in an operator's browser: the
 * wizard store is `persist`-wrapped under `openova-catalyst-wizard`, and
 * `partialize()` strips only four credential fields, so a draft written
 * before the removal still names the retired id — in TWO places, not one.
 *
 *   • `selectedComponents: string[]` — the payload the wizard provisions.
 *   • `componentGroups: Record<groupId, string[]>` — the second, older
 *     catalog, which seeds `INITIAL_WIZARD_STATE` and which StepReview
 *     reads for its section header count and its per-component cards.
 *
 * The two are handled by different blocks of `merge()` and only the first
 * one filtered against the catalog. StepReview counts the raw array length
 * (`totalComponents`, StepReview.tsx:627) while resolving each id through
 * `findComponent()` and silently skipping the misses (:641-642) — so a
 * retired id survives there as a count with no card behind it. That is the
 * same counter-versus-cards inconsistency row W5 asserts against, arriving
 * from localStorage instead of from source, and it is live for every id
 * #5575 retired as well as for `specter`.
 *
 * These tests prove the rehydration is safe rather than reasoning about it.
 * `specter` is the subject; `envoy` (retired under #5575) is included so the
 * guard is shown to be about retired ids in general, not about one name.
 */

import { describe, it, expect, beforeEach } from 'vitest'
import { useWizardStore } from './store'
import { INITIAL_WIZARD_STATE } from './model'
import { findComponent, MANDATORY_COMPONENT_IDS } from '@/pages/wizard/steps/componentGroups'

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

  it('selectedComponents drops the retired id and keeps everything else', async () => {
    seedPayload({
      sovereignSubdomain: 'draft-payload-was-read',
      selectedComponents: [...MANDATORY_COMPONENT_IDS, 'grafana', 'litmus', 'specter', 'envoy'].sort(),
    })

    await useWizardStore.persist.rehydrate()
    const s = useWizardStore.getState()

    // VACUITY — prove the seeded payload really was rehydrated. Without
    // this, a merge that discarded the whole payload would pass every
    // assertion below for the wrong reason.
    expect(s.sovereignSubdomain).toBe('draft-payload-was-read')

    expect(s.selectedComponents).not.toContain('specter')
    expect(s.selectedComponents).not.toContain('envoy')
    // The operator's real picks survive — this is a filter, not a wipe.
    expect(s.selectedComponents).toContain('grafana')
    expect(s.selectedComponents).toContain('litmus')
    for (const id of MANDATORY_COMPONENT_IDS) {
      expect(s.selectedComponents).toContain(id)
    }
    // Every surviving id resolves to a card.
    for (const id of s.selectedComponents) {
      expect(findComponent(id), `${id} survived rehydration with no ComponentDef`).toBeTruthy()
    }
  })

  it('componentGroups drops the retired id too, so the StepReview count matches its cards', async () => {
    seedPayload({
      sovereignSubdomain: 'draft-payload-was-read',
      componentGroups: {
        insights: ['grafana', 'opentelemetry', 'specter'],
        spine: ['cilium', 'envoy'],
        // A retired group key must still be dropped whole (pre-existing rule).
        'legacy-group': ['grafana'],
      },
    })

    await useWizardStore.persist.rehydrate()
    const s = useWizardStore.getState()

    expect(s.sovereignSubdomain).toBe('draft-payload-was-read')
    expect(s.componentGroups.insights).toEqual(['grafana', 'opentelemetry'])
    expect(s.componentGroups.spine).toEqual(['cilium'])
    expect(s.componentGroups['legacy-group']).toBeUndefined()

    // The identity StepReview depends on: its header counts the raw array
    // lengths, its cards resolve through findComponent(). Every id must
    // therefore resolve, or the two disagree.
    const ids = Object.values(s.componentGroups).flat()
    expect(ids.length).toBeGreaterThan(0)
    for (const id of ids) {
      expect(findComponent(id), `${id} would be counted with no card behind it`).toBeTruthy()
    }
  })

  it('the pre-existing minio → seaweedfs migration still runs alongside the filter', async () => {
    // The filter was added to the same expression as this rename. Both must
    // survive: the rename runs FIRST, or `minio` would be filtered out as an
    // unknown id and the operator would silently lose their storage pick.
    seedPayload({
      sovereignSubdomain: 'draft-payload-was-read',
      componentGroups: { silo: ['minio', 'velero', 'specter'] },
    })

    await useWizardStore.persist.rehydrate()
    const s = useWizardStore.getState()

    expect(s.sovereignSubdomain).toBe('draft-payload-was-read')
    expect(s.componentGroups.silo).toEqual(['seaweedfs', 'velero'])
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
    expect(s.selectedComponents.length).toBeGreaterThan(0)
    for (const id of MANDATORY_COMPONENT_IDS) {
      expect(s.selectedComponents).toContain(id)
    }
    for (const id of s.selectedComponents) {
      expect(findComponent(id)).toBeTruthy()
    }
  })
})
