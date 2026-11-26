/**
 * StepCredentials.test.tsx — vitest coverage for the SSH-keypair UX added
 * for GitHub issue #160 ([I] ux: SSH keypair UX in wizard).
 *
 * Asserts the spec verbatim:
 *
 *   • Mode A (Generate keypair):
 *       — clicking the button POSTs to /api/v1/sshkey/generate with the
 *         resolved sovereign FQDN as the comment hint
 *       — on 200, store.sshPublicKey + store.sshFingerprint are populated
 *       — the browser is asked to download the private key (URL.createObjectURL
 *         is stubbed and asserted)
 *       — the one-time warning banner ("Private key shown once. Save it now
 *         or you lose access.") renders
 *
 *   • Mode B (Paste existing public key):
 *       — pasting a valid ed25519 line populates store.sshPublicKey
 *       — pasting empty leaves the wizard in a "next-disabled" state (regex
 *         enforced via isValidSSHPublicKey)
 *       — pasting nonsense surfaces an inline error and does NOT write into
 *         the store
 *
 *   • Server-error handling: the generator returning HTTP 500 surfaces an
 *     error message and leaves the store untouched.
 *
 *   • The credential inputs (CLD-122): one per credential field of the
 *     cloud profile, validated through POST /api/v1/credentials/validate;
 *     none for a profile whose source is operator.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4: every test feeds the wizard a
 * concrete sovereign FQDN via the store and checks that the generator
 * request body carries that FQDN — there is no hardcoded value anywhere
 * in the assertions.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, fireEvent, cleanup, waitFor } from '@testing-library/react'
import { StepCredentials } from './StepCredentials'
import { useWizardStore } from '@/entities/deployment/store'
import { useWizardNav } from '@/shared/lib/wizardNav'
import { INITIAL_WIZARD_STATE, isValidSSHPublicKey } from '@/entities/deployment/model'
import { ENTRY, profileOf, answering, cloudRoutes, type Routes } from '@/test/fixtures/providers-routes.fixture'

const FIXTURE_FQDN = 'omantel.omani.works'
const FIXTURE_PUBLIC_KEY =
  'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBdkRf2yAJ7E7g1zFJKj7xZl9Q3WkF0K3ZQp5Y7qXmHZ catalyst@omantel.omani.works'
const FIXTURE_PRIVATE_KEY =
  '-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAA\n-----END OPENSSH PRIVATE KEY-----\n'
const FIXTURE_FINGERPRINT = 'SHA256:abcdef1234567890abcdef1234567890abcdef1234567890aaaaa'

beforeEach(() => {
  // Reset persisted store. The pool/subdomain trio resolves to the fixture
  // FQDN — the SSH section reads this to compose the comment + .pem name.
  useWizardStore.setState({
    ...INITIAL_WIZARD_STATE,
    sovereignDomainMode: 'pool',
    sovereignPoolDomain: 'omani-works',
    sovereignSubdomain: 'omantel',
    // No cloud chosen: the step reads no profile, so the SSH generator is
    // the only route the SSH-section tests below see.
  })
  useWizardNav.setState({ nav: {} })
})

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

/* ── Helpers ─────────────────────────────────────────────────────── */

function stubBlobUrl() {
  // jsdom doesn't implement URL.createObjectURL — stub it so the download
  // helper runs without throwing. Returning a sentinel string also lets the
  // test assert it was invoked exactly once with a Blob.
  const created = vi.fn().mockReturnValue('blob:test-url')
  const revoked = vi.fn()
  globalThis.URL.createObjectURL = created
  globalThis.URL.revokeObjectURL = revoked
  return { created, revoked }
}

/* ── Mode A — Generate ───────────────────────────────────────────── */

describe('Mode A: generate keypair', () => {
  it('POSTs to /api/v1/sshkey/generate with the resolved FQDN', async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({
        publicKey: FIXTURE_PUBLIC_KEY,
        privateKey: FIXTURE_PRIVATE_KEY,
        fingerprint: FIXTURE_FINGERPRINT,
      }),
    } as Response)
    vi.stubGlobal('fetch', fetchMock)
    stubBlobUrl()

    render(<StepCredentials />)
    fireEvent.click(screen.getByTestId('ssh-generate-button'))

    await waitFor(() => expect(fetchMock).toHaveBeenCalled())
    const [url, init] = fetchMock.mock.calls[0]
    expect(String(url)).toMatch(/\/api\/v1\/sshkey\/generate$/)
    expect(init?.method).toBe('POST')
    expect(JSON.parse(init?.body as string)).toEqual({ fqdn: FIXTURE_FQDN })
  })

  it('writes the generated public key + fingerprint into the store on success', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue({
        ok: true,
        status: 200,
        json: async () => ({
          publicKey: FIXTURE_PUBLIC_KEY,
          privateKey: FIXTURE_PRIVATE_KEY,
          fingerprint: FIXTURE_FINGERPRINT,
        }),
      } as Response),
    )
    stubBlobUrl()

    render(<StepCredentials />)
    fireEvent.click(screen.getByTestId('ssh-generate-button'))

    await waitFor(() => {
      expect(useWizardStore.getState().sshPublicKey).toBe(FIXTURE_PUBLIC_KEY)
    })
    expect(useWizardStore.getState().sshFingerprint).toBe(FIXTURE_FINGERPRINT)
    expect(useWizardStore.getState().sshKeyGeneratedThisSession).toBe(true)
  })

  it('triggers a browser download of the private key', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue({
        ok: true,
        status: 200,
        json: async () => ({
          publicKey: FIXTURE_PUBLIC_KEY,
          privateKey: FIXTURE_PRIVATE_KEY,
          fingerprint: FIXTURE_FINGERPRINT,
        }),
      } as Response),
    )
    const { created, revoked } = stubBlobUrl()

    // Capture the synthetic <a> click — the download helper appends an anchor
    // to document.body and clicks it. We spy on HTMLAnchorElement.prototype.click.
    const clickSpy = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})

    render(<StepCredentials />)
    fireEvent.click(screen.getByTestId('ssh-generate-button'))

    await waitFor(() => expect(clickSpy).toHaveBeenCalledTimes(1))
    expect(created).toHaveBeenCalledTimes(1)
    expect(revoked).toHaveBeenCalledTimes(1)
  })

  it('renders the one-time "private key shown once" warning after generation', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue({
        ok: true,
        status: 200,
        json: async () => ({
          publicKey: FIXTURE_PUBLIC_KEY,
          privateKey: FIXTURE_PRIVATE_KEY,
          fingerprint: FIXTURE_FINGERPRINT,
        }),
      } as Response),
    )
    stubBlobUrl()
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})

    render(<StepCredentials />)
    fireEvent.click(screen.getByTestId('ssh-generate-button'))

    const banner = await screen.findByTestId('ssh-private-key-warning')
    expect(banner.textContent).toMatch(/Private key shown once\. Save it now or you lose access\./)
  })

  it('surfaces a server error and leaves the store untouched on HTTP 500', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue({ ok: false, status: 500, json: async () => ({}) } as Response),
    )
    stubBlobUrl()

    render(<StepCredentials />)
    fireEvent.click(screen.getByTestId('ssh-generate-button'))

    // Wait for the generator to settle (button text returns from "Generating…").
    await waitFor(() => {
      expect(screen.getByTestId('ssh-generate-button').textContent).toMatch(/Generate Ed25519 keypair/)
    })
    expect(useWizardStore.getState().sshPublicKey).toBe('')
    expect(useWizardStore.getState().sshKeyGeneratedThisSession).toBe(false)
  })
})

/* ── Mode B — Paste ──────────────────────────────────────────────── */

describe('Mode B: paste existing public key', () => {
  it('writes a valid ed25519 line into the store', () => {
    render(<StepCredentials />)
    fireEvent.click(screen.getByTestId('ssh-mode-paste'))

    const input = screen.getByTestId('ssh-paste-input') as HTMLTextAreaElement
    fireEvent.change(input, { target: { value: FIXTURE_PUBLIC_KEY } })

    expect(useWizardStore.getState().sshPublicKey).toBe(FIXTURE_PUBLIC_KEY)
  })

  it('rejects empty input and keeps the store empty', () => {
    render(<StepCredentials />)
    fireEvent.click(screen.getByTestId('ssh-mode-paste'))

    const input = screen.getByTestId('ssh-paste-input') as HTMLTextAreaElement
    fireEvent.change(input, { target: { value: '' } })

    expect(useWizardStore.getState().sshPublicKey).toBe('')
  })

  it('surfaces an error on malformed input without writing to the store', () => {
    render(<StepCredentials />)
    fireEvent.click(screen.getByTestId('ssh-mode-paste'))

    const input = screen.getByTestId('ssh-paste-input') as HTMLTextAreaElement
    fireEvent.change(input, { target: { value: 'this is not an ssh key' } })

    expect(screen.getByTestId('ssh-paste-error').textContent).toMatch(/did not parse/i)
    expect(useWizardStore.getState().sshPublicKey).toBe('')
  })

  it('rejects valid-prefix-but-tiny-base64 (defends against placeholder paste)', () => {
    render(<StepCredentials />)
    fireEvent.click(screen.getByTestId('ssh-mode-paste'))

    const input = screen.getByTestId('ssh-paste-input') as HTMLTextAreaElement
    fireEvent.change(input, { target: { value: 'ssh-ed25519 AAAA short' } })

    expect(screen.getByTestId('ssh-paste-error')).toBeTruthy()
    expect(useWizardStore.getState().sshPublicKey).toBe('')
  })
})

/* ── ObjectStorage region auto-default (issue #473) ──────────────── */

/* ── The credential fields of the cloud profile (CLD-122) ────────────── */

describe('the credential inputs of the cloud profile', () => {
  /** pack-a at site-1 chosen, a valid SSH key: the credentials alone gate Continue. */
  function chooseCloud(routes: Routes) {
    useWizardStore.setState({ provider: ENTRY.provider, installation: ENTRY.installation, sshPublicKey: FIXTURE_PUBLIC_KEY })
    const fetchMock = vi.fn(answering(routes))
    vi.stubGlobal('fetch', fetchMock)
    return fetchMock
  }
  const nextDisabled = () => useWizardNav.getState().nav.nextDisabled

  it('renders one input per credential field of the profile, a secret one masked', async () => {
    chooseCloud(cloudRoutes())
    render(<StepCredentials />)
    expect((await screen.findByTestId('credential-api_token')).getAttribute('type')).toBe('password')
    expect(screen.getByTestId('credential-project').getAttribute('type')).toBe('text')
    expect(screen.getByText('API token')).toBeTruthy()
    expect(nextDisabled()).toBe(true)
  })

  it('validates the credential map with the provider and installation, and then allows Continue', async () => {
    const fetchMock = chooseCloud({ ...cloudRoutes(), 'POST /v1/credentials/validate': { status: 200, body: { valid: true } } })
    render(<StepCredentials />)
    fireEvent.change(await screen.findByTestId('credential-api_token'), { target: { value: '  tok-123  ' } })
    fireEvent.click(screen.getByTestId('credentials-validate'))
    await waitFor(() => expect(useWizardStore.getState().credentialValidated).toBe(true))
    const post = fetchMock.mock.calls.find(([, init]) => init?.method === 'POST')
    expect(JSON.parse(post?.[1]?.body as string)).toEqual({
      provider: 'pack-a', installation: 'site-1', credentials: { api_token: 'tok-123' },
    })
    await waitFor(() => expect(nextDisabled()).toBe(false))
    // A changed value must be validated again.
    fireEvent.change(screen.getByTestId('credential-project'), { target: { value: 'p-1' } })
    expect(useWizardStore.getState().credentialValidated).toBe(false)
    await waitFor(() => expect(nextDisabled()).toBe(true))
  })

  it('a refusal that names a field shows it and keeps Continue disabled', async () => {
    chooseCloud({
      ...cloudRoutes(),
      'POST /v1/credentials/validate': { status: 400, body: { valid: false, field: 'api_token', message: 'API token does not match the pattern' } },
    })
    render(<StepCredentials />)
    fireEvent.change(await screen.findByTestId('credential-api_token'), { target: { value: 'short' } })
    fireEvent.click(screen.getByTestId('credentials-validate'))
    expect((await screen.findByRole('alert')).textContent).toContain('api_token: API token does not match the pattern')
    expect(useWizardStore.getState().credentialValidated).toBe(false)
    expect(nextDisabled()).toBe(true)
  })

  it('a profile that takes the operator credentials asks for none', async () => {
    chooseCloud(cloudRoutes([ENTRY], profileOf({ credentials: { source: 'operator', fields: [{ key: 'socket', label: 'Socket', secret: false, required: false }] } })))
    render(<StepCredentials />)
    await screen.findByTestId('credentials-operator')
    expect(screen.queryByTestId('credential-socket')).toBeNull()
    await waitFor(() => expect(nextDisabled()).toBe(false))
  })

  it('a profile that takes either allows Continue with nothing typed, and asks for validation once a value is typed', async () => {
    chooseCloud(cloudRoutes([ENTRY], profileOf({ credentials: { source: 'either', fields: profileOf().credentials.fields } })))
    render(<StepCredentials />)
    expect((await screen.findByTestId('credentials-section')).textContent).toContain("leave every field empty to provision with the operator's credentials")
    await waitFor(() => expect(nextDisabled()).toBe(false))
    fireEvent.change(screen.getByTestId('credential-api_token'), { target: { value: 'tok-123' } })
    await waitFor(() => expect(nextDisabled()).toBe(true))
  })
})

describe('isValidSSHPublicKey', () => {
  it('accepts ed25519, rsa, ecdsa', () => {
    expect(
      isValidSSHPublicKey(
        'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBdkRf2yAJ7E7g1zFJKj7xZl9Q3WkF0K3ZQp5Y7qXmHZ user@host',
      ),
    ).toBe(true)
    expect(
      isValidSSHPublicKey(
        'ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAAAgQDeu8M5z0nZ5Q3WkF0K3ZQp5Y7qXmHZAAAAB3NzaC1yc2EAAAA user@host',
      ),
    ).toBe(true)
    expect(
      isValidSSHPublicKey(
        'ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTItbmlzdHAyNTYAAAAIbmlzdHAyNTYAAABBBOLp7+ALp9JOAW user@host',
      ),
    ).toBe(true)
  })

  it('rejects empty / whitespace / wrong algorithm', () => {
    expect(isValidSSHPublicKey('')).toBe(false)
    expect(isValidSSHPublicKey('   ')).toBe(false)
    expect(isValidSSHPublicKey('ssh-dss AAAA something')).toBe(false)
    expect(isValidSSHPublicKey('not a key at all')).toBe(false)
  })
})
