/**
 * UninstallDialog.org-scope.test.tsx — a customer (an Organization session,
 * console.<org>.<pool>) uninstalls and upgrades their OWN Application through
 * /api/v1/org/applications/{name}. The API's OrgScopeGuard refuses the
 * Sovereign routes /api/v1/sovereigns/{id}/applications/{name} for an
 * Organization session, so before this the customer's Uninstall and Upgrade
 * buttons answered "HTTP 403 org-scoped-forbidden".
 *
 * The Organization route resolves the caller's Organization from
 * X-Tenant-Host and answers 400 tenant-required without it
 * (ResolveOrganization). The fetch stub answers the same way, so a request
 * that leaves the header out fails here as it fails against the API.
 */
import { describe, it, expect, afterEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

import { UninstallDialog } from './UninstallDialog'
import { UpgradeDialog } from './UpgradeDialog'
import {
  applicationUrl,
  getApplication,
  getApplicationStatus,
  previewTopologyChange,
  previewUpgrade,
  updateApplication,
} from '@/lib/catalog.api'

function withProviders(node: React.ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return <QueryClientProvider client={qc}>{node}</QueryClientProvider>
}

function stubFetch() {
  const calls: Array<{ url: string; method: string; tenantHost: string | null }> = []
  const fn = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    const tenantHost = new Headers(init?.headers).get('X-Tenant-Host')
    calls.push({ url, method: (init?.method ?? 'GET').toUpperCase(), tenantHost })
    if (url.includes('/v1/org/') && !tenantHost) {
      return new Response(JSON.stringify({ error: 'tenant-required' }), {
        status: 400,
        headers: { 'Content-Type': 'application/json' },
      })
    }
    if (url.endsWith('/v1/catalog/bp-wordpress/versions')) {
      return new Response(JSON.stringify({ versions: [{ version: '1.3.0' }], upgradeMatrix: { '1.0.0': ['1.3.0'] } }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    }
    if (url.includes('/upgrade/preview')) {
      return new Response(
        JSON.stringify({ manifests: [], diff: '', blueprint: { name: 'bp-wordpress', version: '1.3.0' }, warnings: [] }),
        { status: 200, headers: { 'Content-Type': 'application/json' } },
      )
    }
    return new Response(JSON.stringify({ name: 'wp-prod', namespace: 'org-demo', status: 'uninstalling', message: '' }), {
      status: 202,
      headers: { 'Content-Type': 'application/json' },
    })
  })
  vi.stubGlobal('fetch', fn)
  return calls
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('an Organization session manages its own Application through /org/applications', () => {
  it('uninstall sends DELETE /org/applications/{name} with the tenant host, never the Sovereign route', async () => {
    const calls = stubFetch()
    let done = false
    render(
      withProviders(
        <UninstallDialog
          open
          onClose={() => undefined}
          sovereignId="dep-1"
          applicationName="wp-prod"
          namespace="org-demo"
          orgScoped
          onUninstalled={() => {
            done = true
          }}
        />,
      ),
    )
    fireEvent.change(screen.getByTestId('uninstall-dialog-confirm-input'), { target: { value: 'wp-prod' } })
    fireEvent.click(screen.getByTestId('uninstall-dialog-confirm'))
    await waitFor(() => expect(done).toBe(true))
    const del = calls.filter((c) => c.method === 'DELETE')
    expect(del).toHaveLength(1)
    expect(del[0].url).toMatch(/\/v1\/org\/applications\/wp-prod$/)
    expect(del[0].url).not.toContain('/sovereigns/')
    expect(del[0].tenantHost).toBe(window.location.host)
  })

  it('the Upgrade dialog previews and applies through /org/applications/{name} with the tenant host', async () => {
    const calls = stubFetch()
    let upgradedTo = ''
    render(
      withProviders(
        <UpgradeDialog
          open
          onClose={() => undefined}
          sovereignId="dep-1"
          applicationName="wp-prod"
          namespace="org-demo"
          orgScoped
          blueprintName="bp-wordpress"
          currentVersion="1.0.0"
          onUpgraded={(v) => {
            upgradedTo = v
          }}
        />,
      ),
    )
    await screen.findByRole('option', { name: '1.3.0' })
    fireEvent.change(screen.getByTestId('upgrade-dialog-target'), { target: { value: '1.3.0' } })
    fireEvent.click(screen.getByTestId('upgrade-dialog-preview-btn'))
    await screen.findByTestId('upgrade-dialog-preview')
    fireEvent.click(screen.getByTestId('upgrade-dialog-apply-btn'))
    await waitFor(() => expect(upgradedTo).toBe('1.3.0'))
    expect(screen.queryByTestId('upgrade-dialog-error')).toBeNull()

    const app = calls.filter((c) => c.url.includes('/applications/'))
    expect(app.map((c) => `${c.method} ${c.url.replace(/^.*\/v1\//, '/v1/')}`)).toEqual([
      'POST /v1/org/applications/wp-prod/upgrade/preview?targetVersion=1.3.0',
      'PUT /v1/org/applications/wp-prod',
    ])
    expect(app.map((c) => c.tenantHost)).toEqual([window.location.host, window.location.host])
  })

  it('upgrade, previews and reads use the Organization route with the tenant host; a Sovereign session keeps its route', async () => {
    const calls = stubFetch()
    const org = { namespace: 'org-demo', orgScoped: true }
    await updateApplication('dep-1', 'wp-prod', { blueprintRef: { name: 'bp-wordpress', version: '1.3.0' } }, org)
    await getApplicationStatus('dep-1', 'wp-prod', 'org-demo', { orgScoped: true })
    await getApplication('dep-1', 'wp-prod', 'org-demo', { orgScoped: true })
    await previewUpgrade('dep-1', 'wp-prod', '1.3.0', {}, org)
    await previewTopologyChange('dep-1', 'wp-prod', { placement: { mode: 'single-region', regions: ['a'] } }, org)
    expect(calls.map((c) => `${c.method} ${c.url.replace(/^.*\/v1\//, '/v1/')}`)).toEqual([
      'PUT /v1/org/applications/wp-prod',
      'GET /v1/org/applications/wp-prod/status',
      'GET /v1/org/applications/wp-prod',
      'POST /v1/org/applications/wp-prod/upgrade/preview?targetVersion=1.3.0',
      'POST /v1/org/applications/wp-prod/topology/preview',
    ])
    expect(calls.map((c) => c.tenantHost)).toEqual(calls.map(() => window.location.host))

    await getApplicationStatus('dep-1', 'wp-prod', 'acme')
    const sovereign = calls[calls.length - 1]
    expect(sovereign.url).toMatch(/\/v1\/sovereigns\/dep-1\/applications\/wp-prod\/status\?namespace=acme$/)
    expect(sovereign.tenantHost).toBeNull()
    expect(applicationUrl('dep-1', 'wp-prod', '/status', new URLSearchParams({ namespace: 'acme' }))).toMatch(
      /\/v1\/sovereigns\/dep-1\/applications\/wp-prod\/status\?namespace=acme$/,
    )
  })
})
