/**
 * UninstallDialog.org-scope.test.tsx — a customer (an Organization session,
 * console.<org>.<pool>) uninstalls and upgrades their OWN Application through
 * /api/v1/org/applications/{name}. The API's OrgScopeGuard refuses the
 * Sovereign routes /api/v1/sovereigns/{id}/applications/{name} for an
 * Organization session, so before this the customer's Uninstall and Upgrade
 * buttons answered "HTTP 403 org-scoped-forbidden".
 */
import { describe, it, expect, afterEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

import { UninstallDialog } from './UninstallDialog'
import { applicationUrl, getApplicationStatus, updateApplication } from '@/lib/catalog.api'

function withProviders(node: React.ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return <QueryClientProvider client={qc}>{node}</QueryClientProvider>
}

function stubFetch() {
  const calls: Array<{ url: string; method: string }> = []
  const fn = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ url: String(input), method: (init?.method ?? 'GET').toUpperCase() })
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
  it('uninstall sends DELETE /org/applications/{name}, never the Sovereign route', async () => {
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
  })

  it('upgrade and status read use the Organization route; a Sovereign session keeps its route', async () => {
    const calls = stubFetch()
    await updateApplication('dep-1', 'wp-prod', { blueprintRef: { name: 'bp-wordpress', version: '1.3.0' } }, { namespace: 'org-demo', orgScoped: true })
    await getApplicationStatus('dep-1', 'wp-prod', 'org-demo', { orgScoped: true })
    expect(calls.map((c) => `${c.method} ${c.url.replace(/^.*\/v1\//, '/v1/')}`)).toEqual([
      'PUT /v1/org/applications/wp-prod',
      'GET /v1/org/applications/wp-prod/status',
    ])
    expect(applicationUrl('dep-1', 'wp-prod', '/status', new URLSearchParams({ namespace: 'acme' }))).toMatch(
      /\/v1\/sovereigns\/dep-1\/applications\/wp-prod\/status\?namespace=acme$/,
    )
  })
})
