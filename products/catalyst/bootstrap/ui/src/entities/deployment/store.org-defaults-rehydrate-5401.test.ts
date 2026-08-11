/**
 * store.org-defaults-rehydrate-5401.test.ts — UAT rows W1 + W2, issue #5401.
 *
 * #5401 retired the fabricated ORG_DEFAULTS (`Acme Financial` / `acme.io` /
 * `platform@acme.io` / `Frankfurt, Germany` / …) by emptying them in
 * `model.ts`. That fix reaches INITIAL_WIZARD_STATE only.
 *
 * The wizard store is `persist`-wrapped and `partialize()` strips ONLY the
 * four credential fields, so every Organization identity field is written to
 * localStorage under `openova-catalyst-wizard`. `merge()` then returns
 * `{ ...current, ...persisted }` — the persisted payload WINS over
 * INITIAL_WIZARD_STATE.
 *
 * So any browser that ran the wizard against the pre-fix bundle rehydrates the
 * fabricated company verbatim, and the #5401 fix is invisible on exactly the
 * machines most likely to walk these rows (an operator's own browser, which has
 * run the wizard before). Concretely:
 *
 *   W1 — "step 1 does NOT pre-fill a fabricated company into the Organisation
 *         identity fields": `orgName` rehydrates to 'Acme Financial'.
 *   W2 — "the wizard does NOT derive cloud provider/region from a fabricated
 *         value": `orgHeadquarters` rehydrates to 'Frankfurt, Germany', which
 *         StepProvider's `getHqHint()` matches against
 *         `/germany|frankfurt|berlin|munich|hamburg|cologne/i` — deriving
 *         provider 'hetzner' + regions ['fsn1','nbg1','hel1'] AND rendering the
 *         "★ Pre-selected based on HQ" banner, from a fabricated value.
 *
 * The fix is a persist `version` bump + `migrate` that drops the retired
 * identity fields from legacy payloads so INITIAL_WIZARD_STATE's empty values
 * win. An operator's OWN typed identity must survive — that is the control
 * below, and it is what makes this a migration rather than a blanket wipe.
 */

import { describe, it, expect, beforeEach } from 'vitest'
import { useWizardStore } from './store'
import { INITIAL_WIZARD_STATE } from './model'

const PERSIST_KEY = 'openova-catalyst-wizard'

/**
 * The exact ORG_DEFAULTS shipped by the pre-#5401 bundle (image tag fad88bd,
 * `model.ts:320-324` at that commit). This is the payload a returning
 * operator's browser actually holds.
 */
const RETIRED_ORG_DEFAULTS = {
  orgName: 'Acme Financial',
  orgDomain: 'acme.io',
  orgEmail: 'platform@acme.io',
  orgIndustry: 'Financial Services',
  orgSize: '2,000–10,000',
  orgHeadquarters: 'Frankfurt, Germany',
}

/**
 * Seed localStorage with a legacy (pre-version-bump) persist payload.
 *
 * `version: 0` is the REAL shape, verified empirically against zustand 5
 * rather than assumed: the pre-fix persist options declared no `version`, and
 * zustand's default option is `version: 0`, which it writes into every payload.
 * An earlier draft of this test seeded no `version` key at all and the fix
 * appeared not to work — zustand calls `migrate` ONLY when the stored version
 * is `typeof === 'number'` (middleware.js:392), so a version-less payload
 * silently skips migration and every assertion here failed for the wrong
 * reason.
 */
function seedLegacyPayload(state: Record<string, unknown>): void {
  window.localStorage.setItem(PERSIST_KEY, JSON.stringify({ state, version: 0 }))
}

beforeEach(() => {
  window.localStorage.clear()
  useWizardStore.setState({ ...INITIAL_WIZARD_STATE })
  window.localStorage.clear()
})

describe('wizard store — retired ORG_DEFAULTS must not survive rehydration (#5401, UAT W1/W2)', () => {
  it('W1: a legacy payload does NOT rehydrate the fabricated Organisation identity', async () => {
    seedLegacyPayload({
      ...RETIRED_ORG_DEFAULTS,
      // A field from the SAME payload that must survive — this is the vacuity
      // check: if the payload were not being read at all, this assertion fails
      // and the org-field assertions below would be passing on nothing.
      sovereignSubdomain: 'legacy-payload-was-read',
    })

    await useWizardStore.persist.rehydrate()
    const s = useWizardStore.getState()

    // VACUITY CHECK — prove the seeded payload really was rehydrated.
    expect(s.sovereignSubdomain).toBe('legacy-payload-was-read')

    // The actual assertion: no fabricated company comes back.
    expect(s.orgName).toBe('')
    expect(s.orgDomain).toBe('')
    expect(s.orgEmail).toBe('')
    expect(s.orgIndustry).toBe('')
    expect(s.orgSize).toBe('')
  })

  it('W2: a legacy payload does NOT rehydrate an HQ that drives provider/region derivation', async () => {
    seedLegacyPayload({
      ...RETIRED_ORG_DEFAULTS,
      sovereignSubdomain: 'legacy-payload-was-read',
    })

    await useWizardStore.persist.rehydrate()
    const s = useWizardStore.getState()

    expect(s.sovereignSubdomain).toBe('legacy-payload-was-read')
    // 'Frankfurt, Germany' is what StepProvider's getHqHint() matches to
    // derive provider 'hetzner' + regions ['fsn1','nbg1','hel1'].
    expect(s.orgHeadquarters).toBe('')
    expect(/germany|frankfurt|berlin|munich|hamburg|cologne/i.test(s.orgHeadquarters)).toBe(false)
  })

  it("W2: a legacy payload's provider/region DERIVATION from the fabricated HQ is dropped too", async () => {
    // StepProvider's first-visit effect early-returns when regionProviders is
    // already populated, so a derivation computed from the fabricated HQ would
    // be frozen in and never recomputed against the operator's real HQ.
    seedLegacyPayload({
      ...RETIRED_ORG_DEFAULTS,
      sovereignSubdomain: 'legacy-payload-was-read',
      provider: 'hetzner',
      regionProviders: { 0: 'hetzner', 1: 'hetzner' },
      regionCloudRegions: { 0: 'fsn1', 1: 'nbg1' },
    })

    await useWizardStore.persist.rehydrate()
    const s = useWizardStore.getState()

    expect(s.sovereignSubdomain).toBe('legacy-payload-was-read')
    expect(s.regionProviders).toEqual({})
    expect(s.regionCloudRegions).toEqual({})
    expect(s.provider).toBeNull()
  })

  /**
   * FORWARD-COMPAT — a payload already at the new version must be handed
   * through untouched (migrate must not run on it). This one can only be green
   * AFTER the fix: pre-fix the store declares no `version`, so zustand sees a
   * version mismatch with no `migrate` to call and drops the payload entirely.
   * It is therefore NOT a control — the control is the legacy-payload case
   * below, which is green before AND after.
   */
  it('an operator’s OWN typed identity in a current-version payload survives rehydration', async () => {
    window.localStorage.setItem(
      PERSIST_KEY,
      JSON.stringify({
        state: {
          orgName: 'Omantel',
          orgDomain: 'omantel.biz',
          orgEmail: 'platform@omantel.biz',
          orgIndustry: 'Telecommunications',
          orgSize: '10,000+',
          orgHeadquarters: 'Muscat, Oman',
          provider: 'huawei',
          regionProviders: { 0: 'huawei' },
          regionCloudRegions: { 0: 'me-east-1' },
        },
        // Written by the post-fix bundle, so no migration is owed.
        version: 1,
      }),
    )

    await useWizardStore.persist.rehydrate()
    const s = useWizardStore.getState()

    expect(s.orgName).toBe('Omantel')
    expect(s.orgDomain).toBe('omantel.biz')
    expect(s.orgEmail).toBe('platform@omantel.biz')
    expect(s.orgIndustry).toBe('Telecommunications')
    expect(s.orgSize).toBe('10,000+')
    expect(s.orgHeadquarters).toBe('Muscat, Oman')
    expect(s.provider).toBe('huawei')
    expect(s.regionProviders).toEqual({ 0: 'huawei' })
    expect(s.regionCloudRegions).toEqual({ 0: 'me-east-1' })
  })

  /**
   * CONTROL 2 — a LEGACY payload whose org identity is the operator's own
   * (they overwrote the fabricated defaults before the fix landed) must also
   * survive. Only values that are still verbatim-identical to the retired
   * defaults may be dropped.
   */
  it('CONTROL: a legacy payload the operator had already overwritten survives', async () => {
    seedLegacyPayload({
      orgName: 'Omantel',
      orgDomain: 'omantel.biz',
      orgEmail: 'platform@omantel.biz',
      orgIndustry: 'Telecommunications',
      orgSize: '10,000+',
      orgHeadquarters: 'Muscat, Oman',
    })

    await useWizardStore.persist.rehydrate()
    const s = useWizardStore.getState()

    expect(s.orgName).toBe('Omantel')
    expect(s.orgHeadquarters).toBe('Muscat, Oman')
    expect(s.orgIndustry).toBe('Telecommunications')
  })
})
