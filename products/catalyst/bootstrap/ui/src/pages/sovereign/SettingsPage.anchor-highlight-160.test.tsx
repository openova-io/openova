/**
 * SettingsPage.anchor-highlight-160.test.tsx — UAT row 160 / #3379, second half.
 *
 * The clause asks the `#sovereignty` anchor to "scroll to + HIGHLIGHT the
 * Cluster-sovereignty panel". The hw293-2026-08-10 walk found the scroll
 * working — the panel's bounding-box top moved 3055px -> 896px — and recorded
 * the highlight as absent in the most literal terms available: "the target's
 * class stays 'scroll-mt-20' before and after the click".
 *
 * That residual is not cosmetic on this particular panel. /settings stacks
 * TWELVE sections; arriving at the bottom one with nothing marking it leaves
 * the operator looking at an unannotated card and asking whether the anchor
 * did anything. The cutover is the Pillar-5 trigger, so "did I land on the
 * right thing" is a question worth answering in the UI.
 *
 * THE DISCRIMINATING CONTROL is the third case below. Highlighting whatever is
 * addressed is the feature; highlighting the sovereignty panel unconditionally
 * would satisfy a naive assertion while being a different, worse behaviour. So
 * `#dns` must light the DNS section and leave sovereignty dark, and the
 * no-hash case must leave BOTH dark.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createRoute,
  createMemoryHistory,
  Outlet,
} from '@tanstack/react-router'

import { SettingsPage } from './SettingsPage'
import { useWizardStore } from '@/entities/deployment/store'
import { INITIAL_WIZARD_STATE } from '@/entities/deployment/model'

const DEP = 'dep160'

function renderSettings(hash = '') {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const settingsRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/settings',
    component: () => <SettingsPage disableStream />,
  })
  const catchAll = createRoute({
    getParentRoute: () => rootRoute,
    path: '/$',
    component: () => <div />,
  })
  const router = createRouter({
    routeTree: rootRoute.addChildren([settingsRoute, catchAll]),
    history: createMemoryHistory({
      initialEntries: [`/provision/${DEP}/settings${hash}`],
    }),
  })
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  return render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  useWizardStore.setState({ ...INITIAL_WIZARD_STATE })
  // jsdom ships neither EventSource (SovereigntyCard opens its own stream) nor
  // Element.scrollIntoView. Both are shimmed rather than mocked away, because
  // the page must be allowed to call them.
  if (typeof (globalThis as unknown as { EventSource?: unknown }).EventSource === 'undefined') {
    class StubEventSource {
      url: string
      readyState = 0
      onopen: ((this: EventSource, ev: Event) => unknown) | null = null
      onmessage: ((this: EventSource, ev: MessageEvent) => unknown) | null = null
      onerror: ((this: EventSource, ev: Event) => unknown) | null = null
      static readonly CLOSED = 2
      constructor(url: string) {
        this.url = url
      }
      addEventListener(): void {}
      removeEventListener(): void {}
      close(): void {}
    }
    ;(globalThis as unknown as { EventSource: typeof EventSource }).EventSource =
      StubEventSource as unknown as typeof EventSource
  }
  if (!('scrollIntoView' in Element.prototype)) {
    Object.defineProperty(Element.prototype, 'scrollIntoView', {
      value: vi.fn(),
      writable: true,
    })
  }
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => new Response('{}', { status: 200, headers: { 'content-type': 'application/json' } })),
  )
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

function highlighted(testId: string): string | null {
  return screen.getByTestId(testId).getAttribute('data-anchor-highlighted')
}

describe('SettingsPage — row 160: arriving at an anchor highlights THAT panel', () => {
  it('control — with no hash, nothing is highlighted', async () => {
    renderSettings()
    await screen.findByTestId('settings-sovereignty')
    expect(highlighted('settings-sovereignty')).toBeNull()
    expect(highlighted('settings-section-dns')).toBeNull()
  })

  it('#sovereignty highlights the Cluster-sovereignty panel', async () => {
    renderSettings('#sovereignty')
    const panel = await screen.findByTestId('settings-sovereignty')
    await waitFor(() => expect(panel.getAttribute('data-anchor-highlighted')).toBe('true'))
    // Assert on the VALUE of the class, not merely on the flag: the walk's
    // finding was literally "the class stays scroll-mt-20", so the rendered
    // class must actually gain something a human can see.
    expect(panel.className).not.toBe('scroll-mt-20')
    expect(panel.className).toContain('ring')
  })

  it('#dns highlights DNS and leaves sovereignty dark — the highlight follows the anchor', async () => {
    renderSettings('#dns')
    await screen.findByTestId('settings-sovereignty')
    await waitFor(() => expect(highlighted('settings-section-dns')).toBe('true'))
    expect(highlighted('settings-sovereignty')).toBeNull()
  })
})
