/**
 * StepReview.profile-body.test.tsx — the wizard walked from the provider
 * step to the launch against the providers routes of one pack with an
 * installation (CLD-122): the clouds, regions, zones, flavors and
 * credential inputs are those the routes serve, and the body posted to
 * POST /api/v1/deployments is the create body that provisioner.Request of
 * catalyst-api reads — provider, installation, the credential map and per region
 * provider, cloudRegion, zone and flavors — with none of the legacy
 * credential or singular sizing fields.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, fireEvent, cleanup, waitFor } from '@testing-library/react'
import { StepProvider } from './StepProvider'
import { StepCredentials } from './StepCredentials'
import { StepReview } from './StepReview'
import { useWizardStore } from '@/entities/deployment/store'
import { useWizardNav } from '@/shared/lib/wizardNav'
import { INITIAL_WIZARD_STATE } from '@/entities/deployment/model'
import { answering, cloudRoutes, routeKey } from '@/test/fixtures/providers-routes.fixture'

vi.mock('@tanstack/react-router', () => ({ useRouter: () => ({ navigate: vi.fn() }) }))
vi.mock('@/shared/lib/useSession', () => ({
  useSession: () => ({ signedIn: true, email: 'operator@example.test', loading: false, refetch: vi.fn() }),
}))
vi.mock('@/widgets/auth/PinSignInModal', () => ({ PinSignInModal: () => null }))

const SSH_KEY = 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBdkRf2yAJ7E7g1zFJKj7xZl9Q3WkF0K3ZQp5Y7qXmHZ op@example.test'

const fetchSpy = vi.fn(answering({
  ...cloudRoutes(),
  'POST /v1/credentials/validate': { status: 200, body: { valid: true } },
  'POST /v1/deployments': { status: 200, body: { id: 'dep-test-0001' } },
}))

const callsTo = (key: string) => fetchSpy.mock.calls.filter(([url, init]) => routeKey(url, init) === key)

beforeEach(() => {
  window.localStorage.clear()
  vi.stubGlobal('fetch', fetchSpy)
  vi.stubGlobal('alert', vi.fn())
  fetchSpy.mockClear()
  useWizardNav.setState({ nav: {} })
  useWizardStore.setState({
    ...INITIAL_WIZARD_STATE,
    orgName: 'Profile Body Organisation',
    topology: 'dual',
    sovereignDomainMode: 'pool',
    sovereignPoolDomain: 'omani-works',
    sovereignSubdomain: 'profilebody',
  })
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('the wizard against the providers routes of one pack with an installation', () => {
  it('posts the create body of the profile it rendered', async () => {
    // Step 3: the one listed cloud is chosen; region 1 takes the other zone of its region.
    render(<StepProvider />)
    await waitFor(() => expect(useWizardStore.getState().regionCloudRegions).toEqual({ 0: 'east-1', 1: 'west-1' }))
    fireEvent.click(screen.getByText('east-1b'))
    fireEvent.click(screen.getByText('east-1a'))
    expect(useWizardStore.getState().regionZones[0]).toBe('east-1a')
    await waitFor(() => expect(useWizardNav.getState().nav.nextDisabled).toBe(false))
    cleanup()

    // Step 7: the credential fields of the profile, validated.
    render(<StepCredentials />)
    fireEvent.change(await screen.findByTestId('credential-api_token'), { target: { value: 'tok-123' } })
    fireEvent.change(screen.getByTestId('credential-project'), { target: { value: 'p-1' } })
    fireEvent.click(screen.getByTestId('credentials-validate'))
    await waitFor(() => expect(useWizardStore.getState().credentialValidated).toBe(true))
    useWizardStore.getState().setSshPublicKey(SSH_KEY)
    await waitFor(() => expect(useWizardNav.getState().nav.nextDisabled).toBe(false))
    cleanup()

    // Step 8: the review shows the profile's labels and masks the secret field; Launch posts.
    useWizardNav.setState({ nav: {} })
    render(<StepReview />)
    await waitFor(() => expect(screen.getByTestId('review-region-0').textContent).toContain('Pack A at Site 1'))
    expect(screen.getByTestId('review-region-0').textContent).toContain('East')
    const shown = screen.getByTestId('review-section-credentials').textContent ?? ''
    expect(shown).toContain('p-1')
    expect(shown).not.toContain('tok-123')
    await waitFor(() => expect(useWizardNav.getState().nav.onNext).toBeTypeOf('function'))
    await (useWizardNav.getState().nav.onNext!() as unknown as Promise<void>)
    await waitFor(() => expect(callsTo('POST /v1/deployments')).toHaveLength(1))

    const body = JSON.parse(callsTo('POST /v1/deployments')[0]![1]!.body as string) as Record<string, unknown>
    expect(body.provider).toBe('pack-a')
    expect(body.installation).toBe('site-1')
    expect(body.credentials).toEqual({ api_token: 'tok-123', project: 'p-1' })
    expect(body.regions).toEqual([
      { provider: 'pack-a', cloudRegion: 'east-1', zone: 'east-1a', controlPlaneSize: 'm.large', workerSize: 'w.medium', workerCount: 2 },
      { provider: 'pack-a', cloudRegion: 'west-1', zone: 'west-1a', controlPlaneSize: 'm.small', workerSize: 'w.medium', workerCount: 2 },
    ])
    expect(body.sshPublicKey).toBe(SSH_KEY)
    const legacy = ['region', 'controlPlaneSize', 'workerSize', 'workerCount', 'objectStorageRegion', 'objectStorageAccessKey', 'objectStorageSecretKey']
    expect(legacy.filter((k) => k in body)).toEqual([])
    expect(Object.keys(body).filter((k) => /token|project/i.test(k))).toEqual([])
    // The validation carried the same map, provider and installation.
    expect(JSON.parse(callsTo('POST /v1/credentials/validate')[0]![1]!.body as string)).toEqual({
      provider: 'pack-a', installation: 'site-1', credentials: { api_token: 'tok-123', project: 'p-1' },
    })
  })
})
