/**
 * StepOrg.time-zone.test.tsx — INF-054.
 *
 * Step 1 asks for the Sovereign's own time zone and proposes the browser's:
 * the Sovereign restarts its nodes between 01:00 and 05:00 in that zone by
 * default. The proposal is what the field shows while the operator has not
 * picked one (StepReview submits the same), and a pick is kept in the store.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, fireEvent, cleanup } from '@testing-library/react'
import { StepOrg } from './StepOrg'
import { useWizardStore } from '@/entities/deployment/store'
import { INITIAL_WIZARD_STATE, browserTimeZone } from '@/entities/deployment/model'

/** Make the browser report `zone` as its time zone (undefined: no zone). The
 *  runner itself runs in UTC, the same as the fallback, so a test that read
 *  the runner's zone could not tell the browser's zone from the fallback. */
function browserReports(zone: string | undefined) {
  const real = Intl.DateTimeFormat.prototype.resolvedOptions
  vi.spyOn(Intl.DateTimeFormat.prototype, 'resolvedOptions').mockImplementation(function (this: Intl.DateTimeFormat) {
    return { ...real.call(this), timeZone: zone as string }
  })
}

beforeEach(() => {
  window.localStorage.clear()
  useWizardStore.setState({ ...INITIAL_WIZARD_STATE })
})

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  window.localStorage.clear()
})

function zoneSelect(): HTMLSelectElement {
  const found = document.querySelectorAll<HTMLSelectElement>('select[name="sovereignTimeZone"]')
  expect(found.length).toBe(1)
  return found[0]!
}

describe('StepOrg — the time zone of the Sovereign (INF-054)', () => {
  it('a fresh wizard stores no zone and shows the browser zone as the proposal', () => {
    browserReports('Pacific/Auckland')
    render(<StepOrg />)
    const zone = zoneSelect()
    expect(useWizardStore.getState().sovereignTimeZone).toBe('')
    expect(zone.value).toBe('Pacific/Auckland')
    expect(document.body.textContent).toContain('between 01:00 and 05:00 in this zone')
  })

  it('the proposal is UTC when the browser reports no zone', () => {
    browserReports(undefined)
    render(<StepOrg />)
    expect(useWizardStore.getState().sovereignTimeZone).toBe('')
    expect(zoneSelect().value).toBe('UTC')
  })

  it('a browser zone that the list does not offer is not proposed: the field and the request get UTC', () => {
    // A name the list does not hold could be refused by the API on submit, on
    // a field the person never touched.
    browserReports('Mars/Olympus_Mons')
    render(<StepOrg />)
    const zone = zoneSelect()
    expect(Array.from(zone.options).map((o) => o.value)).not.toContain('Mars/Olympus_Mons')
    expect(zone.value).toBe('UTC')
    expect(useWizardStore.getState().sovereignTimeZone).toBe('')
    expect(browserTimeZone()).toBe('UTC')
  })

  it('the list holds UTC and the IANA zones, and a pick is stored and shown', () => {
    render(<StepOrg />)
    const zone = zoneSelect()
    const values = Array.from(zone.options).map((o) => o.value)
    expect(values).toContain('UTC')
    expect(values).toContain('Asia/Muscat')

    fireEvent.change(zone, { target: { value: 'Asia/Muscat' } })

    expect(useWizardStore.getState().sovereignTimeZone).toBe('Asia/Muscat')
    expect(zoneSelect().value).toBe('Asia/Muscat')
  })
})
