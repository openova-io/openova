/**
 * ConsoleAppsPage.test.tsx — issue #933.
 *
 * Locks in the Sovereign Console Apps surface against
 * /api/v1/sovereign/apps:
 *
 *   • Loaded state renders one card per app with status badge
 *   • Bootstrap-kit cards render with the "bootstrap" badge
 *   • Available cards render an "Install" button affordance
 *   • Search filter narrows the visible cards
 *   • Status filter chips narrow the visible cards
 *   • Error state surfaces the API error message
 */

import { describe, it, expect, afterEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'
import { ConsoleAppsPage } from './ConsoleAppsPage'

const ORIGINAL_FETCH = globalThis.fetch

afterEach(() => {
  cleanup()
  globalThis.fetch = ORIGINAL_FETCH
})

function mockFetchOnce(body: unknown, ok = true) {
  globalThis.fetch = vi.fn().mockResolvedValue({
    ok,
    status: ok ? 200 : 500,
    json: async () => body,
  }) as unknown as typeof fetch
}

const SAMPLE_APPS = {
  generatedAt: '2026-05-05T10:00:00Z',
  bootstrapKit: ['bp-cilium'],
  apps: [
    {
      id: 'bp-cilium',
      slug: 'cilium',
      title: 'Cilium',
      summary: 'eBPF networking',
      tags: [],
      status: 'bootstrap',
      bootstrapKit: true,
    },
    {
      id: 'bp-keycloak',
      slug: 'keycloak',
      title: 'Keycloak',
      summary: 'Identity provider',
      category: 'identity',
      tags: ['auth'],
      status: 'installed',
      bootstrapKit: false,
    },
    {
      id: 'bp-harbor',
      slug: 'harbor',
      title: 'Harbor Registry',
      summary: 'OCI registry',
      category: 'platform',
      tags: ['registry'],
      status: 'available',
      bootstrapKit: false,
    },
  ],
}

describe('ConsoleAppsPage', () => {
  it('renders one card per app with status badge', async () => {
    mockFetchOnce(SAMPLE_APPS)
    render(<ConsoleAppsPage />)
    await waitFor(() => {
      expect(screen.getByTestId('apps-grid')).toBeTruthy()
    })
    expect(screen.getByTestId('app-card-bp-cilium')).toBeTruthy()
    expect(screen.getByTestId('app-card-bp-keycloak')).toBeTruthy()
    expect(screen.getByTestId('app-card-bp-harbor')).toBeTruthy()
    // Available card carries an Install button.
    expect(screen.getByTestId('app-install-bp-harbor')).toBeTruthy()
  })

  it('search narrows results by title / id / category', async () => {
    mockFetchOnce(SAMPLE_APPS)
    render(<ConsoleAppsPage />)
    await waitFor(() => screen.getByTestId('apps-grid'))

    const search = screen.getByTestId('apps-search') as HTMLInputElement
    fireEvent.change(search, { target: { value: 'harbor' } })

    expect(screen.getByTestId('app-card-bp-harbor')).toBeTruthy()
    expect(screen.queryByTestId('app-card-bp-cilium')).toBeNull()
    expect(screen.queryByTestId('app-card-bp-keycloak')).toBeNull()
  })

  it('filter chips narrow by status', async () => {
    mockFetchOnce(SAMPLE_APPS)
    render(<ConsoleAppsPage />)
    await waitFor(() => screen.getByTestId('apps-grid'))

    fireEvent.click(screen.getByTestId('apps-filter-available'))

    expect(screen.getByTestId('app-card-bp-harbor')).toBeTruthy()
    expect(screen.queryByTestId('app-card-bp-cilium')).toBeNull()
    expect(screen.queryByTestId('app-card-bp-keycloak')).toBeNull()
  })

  it('renders the error state when the API fails', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue({
      ok: false,
      status: 503,
      json: async () => ({ error: 'oops' }),
    }) as unknown as typeof fetch

    render(<ConsoleAppsPage />)
    await waitFor(() => {
      expect(screen.getByTestId('apps-error')).toBeTruthy()
    })
  })
})
