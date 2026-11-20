/**
 * StepProvider.test.tsx — the provider step states the region separation of
 * the request before anything is provisioned (NET-213).
 *
 * The rule is the one the writer of the Sovereign object applies to the
 * regions of status.network: site when every region has its own cloud
 * region, none otherwise and for fewer than two regions. With none on two
 * or more regions the step shows the notice of the condition
 * RegionSeparation word for word.
 */

import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { render, screen, fireEvent, cleanup } from '@testing-library/react'
import { StepProvider } from './StepProvider'
import { useWizardStore } from '@/entities/deployment/store'
import { INITIAL_WIZARD_STATE, PROVIDER_REGIONS } from '@/entities/deployment/model'
import type { CloudProvider, TopologyTemplate } from '@/entities/deployment/model'
import { defaultNodeSizeId, defaultWorkerSizeId } from '@/shared/constants/providerSizes'

const NOTICE = 'Regions share one failure domain. Not a disaster-recovery configuration.'

// Any provider of the catalogue with two cloud regions; the rule names none.
const PROVIDER = Object.keys(PROVIDER_REGIONS).find(
  (p) => PROVIDER_REGIONS[p as CloudProvider].length >= 2,
) as CloudProvider
const [FIRST, SECOND] = PROVIDER_REGIONS[PROVIDER]

/** A request of the topology whose regions are in the given cloud regions. */
function request(topology: TopologyTemplate, cloudRegions: string[]) {
  useWizardStore.setState({
    ...INITIAL_WIZARD_STATE,
    topology,
    airgap: false,
    provider: PROVIDER,
    regionProviders: Object.fromEntries(cloudRegions.map((_, i) => [i, PROVIDER])),
    regionCloudRegions: Object.fromEntries(cloudRegions.map((r, i) => [i, r])),
    regionControlPlaneSizes: cloudRegions.map(() => defaultNodeSizeId(PROVIDER)),
    regionWorkerSizes: cloudRegions.map(() => defaultWorkerSizeId(PROVIDER)),
    regionWorkerCounts: cloudRegions.map(() => 2),
  })
}

beforeEach(() => {
  window.localStorage.clear()
  useWizardStore.setState({ ...INITIAL_WIZARD_STATE })
})

afterEach(() => {
  cleanup()
  window.localStorage.clear()
})

describe('StepProvider — the region separation of the request', () => {
  it('two regions in two cloud regions show site and no notice', () => {
    request('dual', [FIRST.id, SECOND.id])
    render(<StepProvider />)
    expect(screen.getByTestId('step-provider-region-separation-value').textContent).toBe('site')
    expect(screen.queryByText(NOTICE)).toBeNull()
  })

  it('two regions in one cloud region show none and the notice word for word', () => {
    request('dual', [FIRST.id, FIRST.id])
    render(<StepProvider />)
    expect(screen.getByTestId('step-provider-region-separation-value').textContent).toBe('none')
    expect(screen.getByTestId('step-provider-region-separation-notice').textContent).toBe(NOTICE)
  })

  it('one region shows neither the separation nor the notice', () => {
    request('solo', [FIRST.id])
    render(<StepProvider />)
    expect(screen.queryByTestId('step-provider-region-separation')).toBeNull()
    expect(screen.queryByText(NOTICE)).toBeNull()
  })

  it('picking the cloud region of the first region for the second turns site into none', () => {
    request('dual', [FIRST.id, SECOND.id])
    render(<StepProvider />)
    expect(screen.getByTestId('step-provider-region-separation-value').textContent).toBe('site')
    // Open the second region's cloud-region list and pick the first region's.
    fireEvent.click(screen.getByText(SECOND.label))
    const options = screen.getAllByText(FIRST.label)
    fireEvent.click(options[options.length - 1])
    expect(useWizardStore.getState().regionCloudRegions[1]).toBe(FIRST.id)
    expect(screen.getByTestId('step-provider-region-separation-value').textContent).toBe('none')
    expect(screen.getByTestId('step-provider-region-separation-notice').textContent).toBe(NOTICE)
  })
})
