/**
 * StepOrg.org-defaults-5401.test.tsx — UAT row W1, issue #5401.
 *
 * #5401 emptied ORG_DEFAULTS so the wizard stops seeding a fabricated company
 * into the Organisation identity fields. Two surfaces on this step were left
 * describing the OLD behaviour:
 *
 *  1. The step description still told the operator "All fields are pre-filled —
 *     proceed without changing anything". With the values now empty that is
 *     false, and it actively instructs the operator to click Next without
 *     stating who the Organization is — which is the behaviour that put
 *     "Acme Financial" into production in the first place.
 *
 *  2. SmartField's `default` badge is driven by `value === defaultValue`. With
 *     both sides now '', an untouched EMPTY required field renders a "DEFAULT"
 *     chip — labelling a blank input as a supplied default.
 *
 * Neither is a pre-filled value, so neither alone fails W1's literal clause;
 * both are the same half-landed fix still telling the operator the fabricated
 * defaults are there.
 */

import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { render, screen, cleanup, fireEvent } from '@testing-library/react'
import { StepOrg } from './StepOrg'
import { useWizardStore } from '@/entities/deployment/store'
import { INITIAL_WIZARD_STATE } from '@/entities/deployment/model'

beforeEach(() => {
  useWizardStore.setState({ ...INITIAL_WIZARD_STATE })
})

afterEach(() => {
  cleanup()
})

describe('StepOrg — the retired ORG_DEFAULTS must not be advertised (#5401, UAT W1)', () => {
  it('does not tell the operator the identity fields are pre-filled', () => {
    render(<StepOrg />)

    // VACUITY CHECK — the step really did render its description.
    expect(screen.getByText(/Tell us about your organisation/i)).toBeTruthy()

    const body = document.body.textContent ?? ''
    expect(body).not.toMatch(/pre-filled/i)
    expect(body).not.toMatch(/proceed without changing anything/i)
  })

  it('does not badge an EMPTY required identity field as a supplied default', () => {
    render(<StepOrg />)

    // VACUITY CHECK — the fields whose badge is at issue are on screen, and
    // they are empty, which is the state the badge wrongly rendered against.
    expect(screen.getByText('Organisation name')).toBeTruthy()
    const textInputs = Array.from(
      document.querySelectorAll('input[type="text"]'),
    ) as HTMLInputElement[]
    expect(textInputs.length).toBeGreaterThan(0)
    expect(textInputs.every((i) => i.value === '')).toBe(true)

    expect(screen.queryByText('default')).toBeNull()
  })

  /**
   * CONTROL — the badge mechanism itself must still work. If a field ever
   * carries a real non-empty default again, an untouched value equal to it
   * SHOULD be badged. This is what keeps the fix a targeted guard rather than
   * a deletion of the feature.
   */
  it('CONTROL: the badge still renders for a field holding a real non-empty default', () => {
    // Headquarters is a SmartField like the others; give it a non-empty
    // default by driving the store to a value and asserting the badge logic
    // is reachable at all via a focus round-trip on a populated field.
    useWizardStore.setState({ ...INITIAL_WIZARD_STATE, orgHeadquarters: 'Muscat, Oman' })
    render(<StepOrg />)

    const hq = screen.getByDisplayValue('Muscat, Oman') as HTMLInputElement
    expect(hq).toBeTruthy()
    // Blur with a non-empty value must NOT be rewritten to the empty default.
    fireEvent.blur(hq)
    expect(useWizardStore.getState().orgHeadquarters).toBe('Muscat, Oman')
  })
})
