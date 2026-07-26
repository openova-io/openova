/**
 * ProvisionPage.sse-url.test.tsx — integration test that locks in the
 * post-#749/#754 contract: when the route param is a 16-char hex
 * deployment id, the SSE EventSource URL the page opens carries the
 * SAME 16 chars verbatim. No truncation, no encoding mangling, no
 * accidental slicing — even on the "deployment is unknown to the
 * backend" code path that originally surfaced the bug.
 *
 * The test mounts the canonical `ProvisionPage` re-export (which is
 * AppsPage under the hood) inside a memory router, stubs `fetch` so
 * the history-replay path is silent, and installs a recording
 * EventSource shim so we can read the exact URL the hook constructed.
 */

import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { render, cleanup, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createRoute,
  createMemoryHistory,
  Outlet,
} from '@tanstack/react-router'
import { ProvisionPage } from './ProvisionPage'
import { useWizardStore } from '@/entities/deployment/store'
import { INITIAL_WIZARD_STATE } from '@/entities/deployment/model'
import { NotificationProvider } from '@/shared/ui/notifications'
import { API_BASE } from '@/shared/config/urls'

interface RecordedES {
  url: string
  onopen: ((e: Event) => void) | null
  onmessage: ((e: MessageEvent) => void) | null
  onerror: ((e: Event) => void) | null
  addEventListener: (type: string, listener: EventListener) => void
  removeEventListener: (type: string, listener: EventListener) => void
  close: () => void
  readyState: number
}

const constructed: RecordedES[] = []

class RecordingEventSource implements RecordedES {
  url: string
  onopen: ((e: Event) => void) | null = null
  onmessage: ((e: MessageEvent) => void) | null = null
  onerror: ((e: Event) => void) | null = null
  readyState = 0
  static readonly CONNECTING = 0
  static readonly OPEN = 1
  static readonly CLOSED = 2

  constructor(url: string) {
    this.url = url
    constructed.push(this)
  }
  addEventListener() {
    /* no-op for this test — we only inspect the URL */
  }
  removeEventListener() {
    /* no-op */
  }
  close() {
    this.readyState = 2
  }
}

function renderProvision(deploymentId: string) {
  const rootRoute = createRootRoute({
    component: () => (
      <NotificationProvider>
        <Outlet />
      </NotificationProvider>
    ),
  })
  const provisionRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId',
    component: () => <ProvisionPage />,
  })
  const wizardRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/wizard',
    component: () => <div data-testid="wizard-target" />,
  })
  const tree = rootRoute.addChildren([provisionRoute, wizardRoute])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({ initialEntries: [`/provision/${deploymentId}`] }),
  })
  // ProvisionPage (== AppsPage) mounts PortalShell, whose ReadinessChip
  // (#3935) and useResolvedDeploymentId are TanStack-Query consumers.
  // src/main.tsx wraps the app in a QueryClientProvider; the harness must
  // do the same or the shell throws before the SSE effect ever fires.
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  })
  return render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  useWizardStore.setState({ ...INITIAL_WIZARD_STATE })
  constructed.length = 0
  // Stub fetch so the history-replay GET resolves with a no-op body.
  globalThis.fetch = (() =>
    Promise.resolve({
      ok: true,
      json: () => Promise.resolve({ events: [], state: undefined, done: false }),
    } as unknown as Response)) as typeof fetch
  // @ts-expect-error — install our recording EventSource on the global
  globalThis.EventSource = RecordingEventSource
})

afterEach(() => {
  cleanup()
  constructed.length = 0
  // @ts-expect-error — clean up
  delete globalThis.EventSource
})

describe('ProvisionPage — SSE URL preserves the full 16-char deployment id', () => {
  it('opens EventSource at the un-truncated path for the canonical bug-report id', async () => {
    // The exact id from issue #749's live evidence.
    const FULL_ID = 'eeb34ecd1414a505'
    expect(FULL_ID.length).toBe(16)

    renderProvision(FULL_ID)

    // Wait for useDeploymentEvents' SSE-effect to fire and the
    // recording EventSource to be constructed. The route mount +
    // PortalShell render needs a few macrotasks; waitFor polls.
    await waitFor(() => {
      expect(constructed.length).toBeGreaterThanOrEqual(1)
    })
    const url = constructed[0]!.url

    // Must end in `/v1/deployments/<FULL_ID>/logs` — the exact URL
    // shape the catalyst-api SSE endpoint serves.
    expect(url).toBe(`${API_BASE}/v1/deployments/${FULL_ID}/logs`)
    // Defence-in-depth: the URL must contain the FULL id as a
    // contiguous substring. No truncation, no slicing.
    expect(url).toContain(FULL_ID)
    // And the urlencoded 15-char prefix must NOT be present as the
    // sole id substring before /logs (the original bug shape).
    const TRUNCATED = FULL_ID.slice(0, 15)
    expect(url).not.toMatch(new RegExp(`/v1/deployments/${TRUNCATED}/logs(\\?|$|/)`))
  })

  it('opens EventSource at the un-truncated path for an arbitrary 16-char hex id', async () => {
    const FULL_ID = '0123456789abcdef'
    renderProvision(FULL_ID)

    await waitFor(() => {
      expect(constructed.length).toBeGreaterThanOrEqual(1)
    })
    expect(constructed[0]!.url).toBe(`${API_BASE}/v1/deployments/${FULL_ID}/logs`)
  })

  it('preserves trailing hex characters on the failure-edge id (last char critical)', async () => {
    // Specifically chosen because the original truncation dropped the
    // final character — make sure trailing 0..f all survive.
    for (const last of ['0', '5', '9', 'a', 'f']) {
      const FULL_ID = `aaaaaaaaaaaaaaa${last}`
      expect(FULL_ID.length).toBe(16)
      constructed.length = 0
      const { unmount } = renderProvision(FULL_ID)
      await waitFor(() => {
        expect(constructed.length).toBeGreaterThanOrEqual(1)
      })
      expect(constructed[0]!.url).toBe(
        `${API_BASE}/v1/deployments/${FULL_ID}/logs`,
      )
      unmount()
    }
  })
})
