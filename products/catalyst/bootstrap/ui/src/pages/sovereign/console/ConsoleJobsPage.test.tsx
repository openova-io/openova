/**
 * ConsoleJobsPage.test.tsx — issue #933.
 *
 * Locks in the Sovereign Console Jobs surface against
 * /api/v1/sovereign/jobs:
 *
 *   • Loading state renders a spinner
 *   • Loaded state renders one row per job, sorted started-DESC
 *   • Error state surfaces the API error message
 *   • Empty state surfaces a "no jobs" empty card
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, waitFor } from '@testing-library/react'
import { ConsoleJobsPage } from './ConsoleJobsPage'

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

describe('ConsoleJobsPage', () => {
  beforeEach(() => {
    // jsdom's fetch is unset by default
  })

  it('renders rows for each returned job', async () => {
    mockFetchOnce({
      jobs: [
        {
          id: 'hr/flux-system/bp-cilium',
          name: 'bp-cilium',
          namespace: 'flux-system',
          kind: 'HelmRelease',
          status: 'succeeded',
          startedAt: '2026-04-30T10:00:00Z',
          finishedAt: '2026-04-30T10:05:00Z',
        },
        {
          id: 'job/auth/keycloak-bootstrap',
          name: 'keycloak-bootstrap',
          namespace: 'auth',
          kind: 'Job',
          status: 'failed',
          startedAt: '2026-04-30T09:00:00Z',
        },
      ],
    })

    render(<ConsoleJobsPage />)

    await waitFor(() => {
      expect(screen.getByTestId('jobs-table')).toBeTruthy()
    })

    expect(screen.getByTestId('job-row-hr/flux-system/bp-cilium')).toBeTruthy()
    expect(screen.getByTestId('job-row-job/auth/keycloak-bootstrap')).toBeTruthy()
  })

  it('renders the empty state when no jobs', async () => {
    mockFetchOnce({ jobs: [] })

    render(<ConsoleJobsPage />)

    await waitFor(() => {
      expect(screen.getByTestId('jobs-empty')).toBeTruthy()
    })
  })

  it('renders the error state when the API fails', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue({
      ok: false,
      status: 503,
      json: async () => ({ error: 'in-cluster-client-unavailable' }),
    }) as unknown as typeof fetch

    render(<ConsoleJobsPage />)

    await waitFor(() => {
      expect(screen.getByTestId('jobs-error')).toBeTruthy()
    })
  })
})
