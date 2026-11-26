/**
 * StepProvider.test.tsx — the provider step renders the clouds, regions,
 * zones and flavors the providers routes serve (CLD-122), and states the
 * region separation of the request before anything is provisioned (NET-213).
 *
 * The separation rule is the one the writer of the Sovereign object applies
 * to the regions of status.network: site when every region has its own cloud
 * region, none otherwise and for fewer than two regions. With none on two or
 * more regions the step shows the notice of the condition RegionSeparation
 * word for word.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, fireEvent, cleanup, waitFor } from '@testing-library/react'
import { StepProvider } from './StepProvider'
import { useWizardStore } from '@/entities/deployment/store'
import { useWizardNav } from '@/shared/lib/wizardNav'
import { INITIAL_WIZARD_STATE } from '@/entities/deployment/model'
import type { TopologyTemplate } from '@/entities/deployment/model'
import { ENTRY, OTHER_ENTRY, profileOf, answering, cloudRoutes, type Routes } from '@/test/fixtures/providers-routes.fixture'

vi.mock('@/shared/lib/useSession', () => ({ useSession: () => ({ signedIn: false, refetch: vi.fn() }) }))
vi.mock('@/widgets/auth/PinSignInModal', () => ({ PinSignInModal: () => null }))

const NOTICE = 'Regions share one failure domain. Not a disaster-recovery configuration.'
const PROFILE = profileOf()
const [EAST, WEST] = PROFILE.regions as [typeof PROFILE.regions[number], typeof PROFILE.regions[number]]

function serve(routes: Routes) {
  vi.stubGlobal('fetch', vi.fn(answering(routes)))
}

const nextDisabled = () => useWizardNav.getState().nav.nextDisabled

/** A request of the topology on pack-a at site-1 whose regions are in the given cloud regions. */
function request(topology: TopologyTemplate, cloudRegions: string[]) {
  useWizardStore.setState({
    ...INITIAL_WIZARD_STATE,
    topology,
    airgap: false,
    provider: ENTRY.provider,
    installation: ENTRY.installation,
    regionCloudRegions: Object.fromEntries(cloudRegions.map((r, i) => [i, r])),
    regionZones: Object.fromEntries(cloudRegions.map((r, i) => [i, r === EAST.code ? EAST.defaultZone : WEST.defaultZone])),
    regionControlPlaneSizes: cloudRegions.map(() => 'm.small'),
    regionWorkerSizes: cloudRegions.map(() => 'w.medium'),
    regionWorkerCounts: cloudRegions.map(() => 2),
  })
}

beforeEach(() => {
  window.localStorage.clear()
  useWizardStore.setState({ ...INITIAL_WIZARD_STATE })
  useWizardNav.setState({ nav: {} })
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  window.localStorage.clear()
})

describe('StepProvider — the clouds, regions, zones and flavors of the profile', () => {
  it('shows one card per listed cloud and chooses the first', async () => {
    serve(cloudRoutes([ENTRY, OTHER_ENTRY]))
    render(<StepProvider />)
    expect((await screen.findByTestId('provider-card-pack-a/site-1')).textContent).toContain('Pack A at Site 1')
    expect(screen.getByTestId('provider-card-pack-b').textContent).toContain("The operator's credentials")
    await waitFor(() => expect(useWizardStore.getState().provider).toBe('pack-a'))
    expect(useWizardStore.getState().installation).toBe('site-1')
  })

  it('fills each region with a region of the profile, its default zone and the default flavors offered there', async () => {
    serve(cloudRoutes())
    render(<StepProvider />)
    await waitFor(() => expect(useWizardStore.getState().regionCloudRegions).toEqual({ 0: 'east-1', 1: 'west-1' }))
    const s = useWizardStore.getState()
    expect(s.regionZones).toEqual({ 0: 'east-1b', 1: 'west-1a' })
    // m.large, the default, is offered in east-1 only: west-1 takes the first control-plane flavor it offers.
    expect(s.regionControlPlaneSizes).toEqual(['m.large', 'm.small'])
    expect(s.regionWorkerSizes).toEqual(['w.medium', 'w.medium'])
    expect(s.regionWorkerCounts).toEqual([2, 2])
    await waitFor(() => expect(nextDisabled()).toBe(false))
    // The display prices: 40 + 2 × 20 in east-1, 10 + 2 × 20 in west-1.
    expect(screen.getByTestId('step-provider-total').textContent).toContain('130.00 EUR/month')
  })

  it('another region keeps a flavor it offers and replaces one it does not', async () => {
    serve(cloudRoutes())
    request('solo', [EAST.code])
    useWizardStore.setState({ regionControlPlaneSizes: ['m.large'] })
    render(<StepProvider />)
    fireEvent.click(await screen.findByText(EAST.label))
    const options = screen.getAllByText(WEST.label)
    fireEvent.click(options[options.length - 1]!)
    const s = useWizardStore.getState()
    expect(s.regionCloudRegions[0]).toBe('west-1')
    expect(s.regionZones[0]).toBe('west-1a')
    expect(s.regionControlPlaneSizes[0]).toBe('m.small')
    expect(s.regionWorkerSizes[0]).toBe('w.medium')
  })

  it('a list refused for want of a session asks to sign in and is not read as no cloud', async () => {
    serve({ 'GET /v1/providers': { status: 401, body: { error: 'unauthorized' } } })
    render(<StepProvider />)
    await screen.findByTestId('step-provider-sign-in')
    expect(screen.queryByTestId('step-provider-no-cloud')).toBeNull()
    expect(nextDisabled()).toBe(true)
  })

  it('an empty list says that no cloud is offered and keeps Continue disabled', async () => {
    serve({ 'GET /v1/providers': { status: 200, body: { providers: [] } } })
    render(<StepProvider />)
    await screen.findByTestId('step-provider-no-cloud')
    expect(useWizardStore.getState().provider).toBeNull()
    expect(nextDisabled()).toBe(true)
  })

  it('a profile without multiRegion refuses two regions and takes one', async () => {
    serve(cloudRoutes([ENTRY], profileOf({ capabilities: { multiRegion: false, multiCloudRegion: false } })))
    render(<StepProvider />)
    expect((await screen.findByTestId('step-provider-one-region')).textContent).toContain('Pack A at Site 1 allows one region')
    expect(nextDisabled()).toBe(true)
    cleanup()
    useWizardStore.setState({ topology: 'solo' })
    render(<StepProvider />)
    await waitFor(() => expect(nextDisabled()).toBe(false))
    expect(screen.queryByTestId('step-provider-one-region')).toBeNull()
  })
})

describe('StepProvider — the region separation of the request', () => {
  it('two regions in two cloud regions show site and no notice', () => {
    serve(cloudRoutes())
    request('dual', [EAST.code, WEST.code])
    render(<StepProvider />)
    expect(screen.getByTestId('step-provider-region-separation-value').textContent).toBe('site')
    expect(screen.queryByText(NOTICE)).toBeNull()
  })

  it('two regions in one cloud region show none and the notice word for word', () => {
    serve(cloudRoutes())
    request('dual', [EAST.code, EAST.code])
    render(<StepProvider />)
    expect(screen.getByTestId('step-provider-region-separation-value').textContent).toBe('none')
    expect(screen.getByTestId('step-provider-region-separation-notice').textContent).toBe(NOTICE)
  })

  it('one region shows neither the separation nor the notice', () => {
    serve(cloudRoutes())
    request('solo', [EAST.code])
    render(<StepProvider />)
    expect(screen.queryByTestId('step-provider-region-separation')).toBeNull()
    expect(screen.queryByText(NOTICE)).toBeNull()
  })

  it('picking the cloud region of the first region for the second turns site into none', async () => {
    serve(cloudRoutes())
    request('dual', [EAST.code, WEST.code])
    render(<StepProvider />)
    expect(screen.getByTestId('step-provider-region-separation-value').textContent).toBe('site')
    // Open the second region's cloud-region list and pick the first region's.
    fireEvent.click(await screen.findByText(WEST.label))
    const options = screen.getAllByText(EAST.label)
    fireEvent.click(options[options.length - 1]!)
    expect(useWizardStore.getState().regionCloudRegions[1]).toBe(EAST.code)
    expect(screen.getByTestId('step-provider-region-separation-value').textContent).toBe('none')
    expect(screen.getByTestId('step-provider-region-separation-notice').textContent).toBe(NOTICE)
  })
})
