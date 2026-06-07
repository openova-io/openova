/**
 * EndpointsTab.test.tsx — unit tests for the G117.3 (#2742) editable
 * Endpoints/Ingress tab ported from the dead Svelte console into the
 * production React tree.
 *
 * The catalog.api module is mocked so no network is hit; the tests
 * assert the CRUD wiring (list → New → create PR, Edit → patch PR,
 * Delete → delete PR) and that the mutation body carries the full,
 * correctly-cased fields the Go handler requires (notably `name` on
 * create — precheck.ValidateMutation rejects an empty endpoint name).
 */
import { describe, it, expect, afterEach, beforeEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

import { EndpointsTab } from './EndpointsTab'
import type { ResolvedEndpoint, EndpointPR } from '@/lib/catalog.api'

/* ── Mock the API client ─────────────────────────────────────────────── */

const listEndpoints = vi.fn()
const createAppEndpoint = vi.fn()
const patchAppEndpoint = vi.fn()
const deleteAppEndpoint = vi.fn()

vi.mock('@/lib/catalog.api', () => ({
  listEndpoints: (...a: unknown[]) => listEndpoints(...a),
  createAppEndpoint: (...a: unknown[]) => createAppEndpoint(...a),
  patchAppEndpoint: (...a: unknown[]) => patchAppEndpoint(...a),
  deleteAppEndpoint: (...a: unknown[]) => deleteAppEndpoint(...a),
}))

function withProviders(node: React.ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return <QueryClientProvider client={qc}>{node}</QueryClientProvider>
}

const SAMPLE_EP: ResolvedEndpoint = {
  name: 'web',
  hostnameTemplate: '{AppName}.{OrgSlug}.omani.homes',
  hostname: 'shop.acme.omani.homes',
  port: 443,
  protocol: 'https',
  tls: true,
  visibility: 'public',
  ssoEnabled: true,
  launchDefault: true,
  status: 'Ready',
}

const SAMPLE_PR: EndpointPR = {
  prURL: 'https://gitea.t99.omani.works/acme/iac/pulls/7',
  status: 'open',
  preCheckResults: { kyverno: 'pass', certManager: 'pass', dnsConflict: 'pass' },
}

function renderTab(overrides: Partial<React.ComponentProps<typeof EndpointsTab>> = {}) {
  return render(
    withProviders(
      <EndpointsTab
        applicationName="shop"
        appUID="uid-123"
        externalURL=""
        sovereignFQDN="t99.omani.works"
        {...overrides}
      />,
    ),
  )
}

beforeEach(() => {
  listEndpoints.mockReset().mockResolvedValue({ items: [SAMPLE_EP] })
  createAppEndpoint.mockReset().mockResolvedValue(SAMPLE_PR)
  patchAppEndpoint.mockReset().mockResolvedValue(SAMPLE_PR)
  deleteAppEndpoint.mockReset().mockResolvedValue(SAMPLE_PR)
})
afterEach(() => cleanup())

describe('EndpointsTab', () => {
  it('lists endpoints from the API with name + hostname + sso', async () => {
    renderTab()
    expect(await screen.findByTestId('sov-endpoints-table')).toBeTruthy()
    expect(screen.getByTestId('sov-endpoint-row-shop.acme.omani.homes')).toBeTruthy()
    expect(screen.getByText('web')).toBeTruthy()
    expect(screen.getByText('shop.acme.omani.homes')).toBeTruthy()
    // "+ New endpoint" is available when appUID is present.
    expect(screen.getByTestId('endpoint-new')).toBeTruthy()
  })

  it('opens the create dialog with the PR-consequence warning + all inputs', async () => {
    renderTab()
    await screen.findByTestId('sov-endpoints-table')
    fireEvent.click(screen.getByTestId('endpoint-new'))
    expect(screen.getByTestId('endpoint-dialog')).toBeTruthy()
    expect(screen.getByTestId('endpoint-consequence').textContent).toMatch(/governed Git-IaC pull request/i)
    expect(screen.getByTestId('endpoint-input-name')).toBeTruthy()
    expect(screen.getByTestId('endpoint-input-hostname')).toBeTruthy()
    expect(screen.getByTestId('endpoint-input-protocol')).toBeTruthy()
    expect(screen.getByTestId('endpoint-input-port')).toBeTruthy()
    expect(screen.getByTestId('endpoint-input-visibility')).toBeTruthy()
    expect(screen.getByTestId('endpoint-input-tls')).toBeTruthy()
    expect(screen.getByTestId('endpoint-input-sso')).toBeTruthy()
  })

  it('create submits the full body INCLUDING name and surfaces the PR banner', async () => {
    renderTab()
    await screen.findByTestId('sov-endpoints-table')
    fireEvent.click(screen.getByTestId('endpoint-new'))

    fireEvent.change(screen.getByTestId('endpoint-input-name'), { target: { value: 'api' } })
    fireEvent.change(screen.getByTestId('endpoint-input-hostname'), {
      target: { value: 'api.acme.omani.homes' },
    })
    fireEvent.change(screen.getByTestId('endpoint-input-port'), { target: { value: '8443' } })
    fireEvent.submit(screen.getByTestId('endpoint-save').closest('form')!)

    await waitFor(() => expect(createAppEndpoint).toHaveBeenCalledTimes(1))
    const [uid, body] = createAppEndpoint.mock.calls[0]
    expect(uid).toBe('uid-123')
    // The Go precheck REQUIRES a non-empty name on create.
    expect(body.name).toBe('api')
    expect(body.hostname).toBe('api.acme.omani.homes')
    expect(body.port).toBe(8443)
    expect(body.protocol).toBe('https')
    expect(body.visibility).toBe('public')

    // PR banner appears with the returned prURL.
    const banner = await screen.findByTestId('endpoint-pr-banner')
    expect(banner.textContent).toMatch(/Change submitted as PR/i)
    expect(screen.getByTestId('endpoint-pr-banner-link').getAttribute('href')).toBe(SAMPLE_PR.prURL)
  })

  it('edit opens prefilled, disables the name field, and PATCHes by name', async () => {
    renderTab()
    await screen.findByTestId('sov-endpoints-table')
    fireEvent.click(screen.getByTestId('endpoint-edit-web'))

    const nameInput = screen.getByTestId('endpoint-input-name') as HTMLInputElement
    expect(nameInput.value).toBe('web')
    expect(nameInput.disabled).toBe(true)
    const hostInput = screen.getByTestId('endpoint-input-hostname') as HTMLInputElement
    expect(hostInput.value).toBe('shop.acme.omani.homes')

    fireEvent.change(hostInput, { target: { value: 'shop2.acme.omani.homes' } })
    fireEvent.submit(screen.getByTestId('endpoint-save').closest('form')!)

    await waitFor(() => expect(patchAppEndpoint).toHaveBeenCalledTimes(1))
    const [uid, name, body] = patchAppEndpoint.mock.calls[0]
    expect(uid).toBe('uid-123')
    expect(name).toBe('web')
    expect(body.hostname).toBe('shop2.acme.omani.homes')
    expect(createAppEndpoint).not.toHaveBeenCalled()
  })

  it('delete opens the confirm dialog and calls deleteAppEndpoint', async () => {
    renderTab()
    await screen.findByTestId('sov-endpoints-table')
    fireEvent.click(screen.getByTestId('endpoint-delete-web'))

    expect(screen.getByTestId('endpoint-delete-dialog')).toBeTruthy()
    expect(screen.getByTestId('endpoint-consequence').textContent).toMatch(/removing the\s+endpoint manifest/i)

    fireEvent.click(screen.getByTestId('endpoint-delete-confirm'))
    await waitFor(() => expect(deleteAppEndpoint).toHaveBeenCalledTimes(1))
    expect(deleteAppEndpoint.mock.calls[0]).toEqual(['uid-123', 'web'])
  })

  it('surfaces a mutation error inside the dialog without closing it', async () => {
    createAppEndpoint.mockRejectedValueOnce(new Error('hostname already in use'))
    renderTab()
    await screen.findByTestId('sov-endpoints-table')
    fireEvent.click(screen.getByTestId('endpoint-new'))
    fireEvent.change(screen.getByTestId('endpoint-input-name'), { target: { value: 'api' } })
    fireEvent.change(screen.getByTestId('endpoint-input-hostname'), {
      target: { value: 'dup.acme.omani.homes' },
    })
    fireEvent.submit(screen.getByTestId('endpoint-save').closest('form')!)

    const err = await screen.findByTestId('endpoint-dialog-error')
    expect(err.textContent).toMatch(/hostname already in use/i)
    // Dialog stays open so the operator can correct + retry.
    expect(screen.getByTestId('endpoint-dialog')).toBeTruthy()
  })

  it('falls back to a read-only externalURL row (no Edit/Delete) when the API returns none', async () => {
    listEndpoints.mockResolvedValue({ items: [] })
    renderTab({ externalURL: 'https://shop.acme.omani.homes/' })
    await screen.findByTestId('sov-endpoints-table')
    expect(screen.getByTestId('sov-endpoint-row-shop.acme.omani.homes')).toBeTruthy()
    // Synthesized fallback rows are not editable (no backing manifest).
    expect(screen.queryByTestId('endpoint-edit-shop')).toBeNull()
    expect(screen.queryByTestId('endpoint-delete-shop')).toBeNull()
    // But "New endpoint" is still offered.
    expect(screen.getByTestId('endpoint-new')).toBeTruthy()
  })

  it('shows the bootstrap-kit notice when there is no appUID', async () => {
    renderTab({ appUID: '' })
    expect(await screen.findByTestId('sov-section-endpoint-edit-unavailable')).toBeTruthy()
    expect(screen.queryByTestId('endpoint-new')).toBeNull()
    expect(listEndpoints).not.toHaveBeenCalled()
  })
})
