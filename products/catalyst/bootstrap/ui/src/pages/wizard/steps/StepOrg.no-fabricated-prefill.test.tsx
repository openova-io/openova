/**
 * StepOrg.no-fabricated-prefill.test.tsx — UAT row W1, issues #5401 / #6194.
 *
 * WHAT THIS ROW ASSERTS: "Deployment wizard step 1 does NOT pre-fill a
 * fabricated company into the Organisation identity fields."
 *
 * WHY A SECOND TEST EXISTS. `store.org-defaults-rehydrate-5401.test.ts`
 * already covers the *store* half — a legacy persist payload must not bring
 * `Acme Financial` back. It passes, and it is right. But it asserts on store
 * state, and the field the operator actually reads is a `<select>`, whose
 * rendered value is decided by the DOM, not by the store:
 *
 *   `ORG_DEFAULTS.industry` is `''`, so React renders `<select value="">`
 *   over an option list that contained no `''` entry. Per HTML's "ask for a
 *   reset" algorithm (select, no `multiple`, display size 1, nothing
 *   selected ⇒ select the first non-disabled option) the browser selects
 *   INDUSTRIES[0] — `Financial Services` — which is verbatim
 *   RETIRED_ORG_DEFAULTS_5401.orgIndustry.
 *
 * Measured live on hw296 (console.hw296.omani.works/wizard, catalyst-ui
 * :031db43, fresh browser context, localStorage empty): the two text inputs
 * read `value:"" valueLen:0`, and the Industry select read
 * `value:"Financial Services" valueLen:18 selectedIndex:0
 * hasBlankOption:false`. The store said `''` the whole time — so the field
 * shown to the operator and the value the wizard would submit disagreed.
 *
 * A store-level assertion structurally cannot see that. This test renders the
 * step and reads the DOM.
 */

import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { render, screen, fireEvent, cleanup } from '@testing-library/react'
import { StepOrg } from './StepOrg'
import { useWizardStore } from '@/entities/deployment/store'
import { INITIAL_WIZARD_STATE, RETIRED_ORG_DEFAULTS_5401 } from '@/entities/deployment/model'

beforeEach(() => {
  window.localStorage.clear()
  useWizardStore.setState({ ...INITIAL_WIZARD_STATE })
})

afterEach(() => {
  cleanup()
  window.localStorage.clear()
})

function selects(): HTMLSelectElement[] {
  return Array.from(document.querySelectorAll('select'))
}
function textInputs(): HTMLInputElement[] {
  return Array.from(document.querySelectorAll('input[type="text"]'))
}

describe('StepOrg — no fabricated company on a fresh wizard (UAT W1)', () => {
  it('every Organisation identity field is genuinely empty, selects included', () => {
    render(<StepOrg />)

    // VACUITY CONTROL — the step really rendered its identity fields. Without
    // this, an empty query would satisfy every assertion below.
    expect(textInputs().length).toBe(2)
    expect(selects().length).toBe(2)

    for (const el of textInputs()) {
      expect(el.value).toBe('')
      expect(el.value.length).toBe(0)
    }
    for (const el of selects()) {
      // The regression: `selectedIndex` fell through to 0 over a list whose
      // first entry was a real profile value.
      expect(el.value).toBe('')
      expect(el.value.length).toBe(0)
      expect(el.options[0]!.value).toBe('')
    }
  })

  it('no retired fabricated identity value is rendered as a VALUE anywhere on the step', () => {
    render(<StepOrg />)

    const rendered = [
      ...textInputs().map((el) => el.value),
      ...selects().map((el) => el.value),
    ]
    for (const fabricated of Object.values(RETIRED_ORG_DEFAULTS_5401)) {
      expect(rendered).not.toContain(fabricated)
    }

    // DISCRIMINATING CONTROL — `Financial Services` is still OFFERED as an
    // option (this is a real industry an operator may pick), so the assertion
    // above is about the field's VALUE, not about the string's absence from
    // the catalog. If this control ever fails, the test above has become
    // vacuous for the wrong reason.
    const industry = selects()[0]!
    const optionTexts = Array.from(industry.options).map((o) => o.text)
    expect(optionTexts).toContain(RETIRED_ORG_DEFAULTS_5401.orgIndustry)
  })

  it('the select is live — picking a value sets it, so the empty read is not a dead element', () => {
    render(<StepOrg />)
    const industry = selects()[0]!

    fireEvent.change(industry, { target: { value: 'Banking' } })

    expect(industry.value).toBe('Banking')
    expect(useWizardStore.getState().orgIndustry).toBe('Banking')
  })

  it('an empty field is not labelled DEFAULT, and the step does not claim pre-filled values', () => {
    render(<StepOrg />)

    // #6194 — the step's copy told the operator "All fields are pre-filled —
    // proceed without changing anything" while Organisation name was blank.
    const text = document.body.textContent ?? ''
    expect(text).not.toMatch(/All fields are pre-filled/i)
    expect(text).not.toMatch(/proceed without changing anything/i)
    expect(text).not.toMatch(/Fields marked default are pre-filled/i)

    // The DEFAULT chip fired on `'' === ''` once ORG_DEFAULTS was emptied,
    // badging a blank required field as though it already held a value.
    expect(screen.queryByText('default')).toBeNull()
  })
})
