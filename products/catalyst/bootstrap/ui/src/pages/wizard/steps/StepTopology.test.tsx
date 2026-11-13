/**
 * StepTopology.test.tsx — the region-count step (ADR-0019).
 *
 * A Sovereign region is one cluster, so the step offers exactly two
 * choices, "One region" and "Two or more regions". Each says what the
 * provisioner builds. The choice is stored as a TopologyTemplate id and
 * its one effect is TOPOLOGY_REGION_COUNT. A stored id of a retired
 * template becomes the default choice, two regions.
 */

import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { render, screen, fireEvent, cleanup } from '@testing-library/react'
import { StepTopology } from './StepTopology'
import { useWizardStore } from '@/entities/deployment/store'
import {
  INITIAL_WIZARD_STATE,
  TOPOLOGY_REGION_COUNT,
  TOPOLOGY_REGION_LABELS,
  WIZARD_PERSIST_VERSION,
} from '@/entities/deployment/model'

const CHOICE_TEXT =
  'One cluster per region. The edge, the platform and Organizations are separated by network policy.'

beforeEach(() => {
  window.localStorage.clear()
  useWizardStore.setState({ ...INITIAL_WIZARD_STATE })
})

afterEach(() => {
  cleanup()
  window.localStorage.clear()
})

describe('StepTopology — two region-count choices', () => {
  it('renders exactly two choices, each with the text of what is built', () => {
    render(<StepTopology />)
    const choices = screen.getAllByRole('radio')
    expect(choices).toHaveLength(2)
    expect(choices[0].textContent).toContain('One region')
    expect(choices[1].textContent).toContain('Two or more regions')
    expect(screen.getAllByText(CHOICE_TEXT)).toHaveLength(2)
  })

  it('names no zone, no virtual cluster per zone and no retired template', () => {
    const { container } = render(<StepTopology />)
    // The shared step shell carries a <style> element; its CSS is not text.
    container.querySelectorAll('style').forEach((el) => el.remove())
    expect(container.textContent ?? '').not.toMatch(/citadel|zoned|compact|triangle|DMZ|RTZ|MGMT|vCluster/i)
  })

  it('the default choice is two regions', () => {
    render(<StepTopology />)
    expect(INITIAL_WIZARD_STATE.topology).toBe('dual')
    expect(screen.getByTestId('topology-choice-dual').getAttribute('aria-checked')).toBe('true')
    expect(screen.getByTestId('topology-choice-solo').getAttribute('aria-checked')).toBe('false')
  })

  it('a click stores the choice, and the region count is its only effect', () => {
    render(<StepTopology />)
    fireEvent.click(screen.getByTestId('topology-choice-solo'))
    expect(useWizardStore.getState().topology).toBe('solo')
    expect(TOPOLOGY_REGION_COUNT).toEqual({ solo: 1, dual: 2 })
    expect(TOPOLOGY_REGION_LABELS).toEqual({ solo: ['Region 1'], dual: ['Region 1', 'Region 2'] })
  })
})

describe('wizard store — a stored retired template', () => {
  it('becomes the two-region choice, with the per-region choices cleared', async () => {
    window.localStorage.setItem(
      'openova-catalyst-wizard',
      JSON.stringify({
        state: {
          topology: 'citadel',
          regionProviders: { 0: 'aws', 1: 'aws', 2: 'aws', 3: 'aws' },
          sovereignSubdomain: 'stored-payload-was-read',
        },
        version: WIZARD_PERSIST_VERSION,
      }),
    )
    await useWizardStore.persist.rehydrate()
    const s = useWizardStore.getState()
    expect(s.sovereignSubdomain).toBe('stored-payload-was-read')
    expect(s.topology).toBe('dual')
    expect(s.regionProviders).toEqual({})
  })
})
