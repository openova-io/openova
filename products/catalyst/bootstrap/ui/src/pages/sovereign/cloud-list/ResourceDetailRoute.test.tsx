/**
 * ResourceDetailRoute.test.tsx — locks the SSE wire contract for the
 * resource-detail page's EventsPanel feed (#1099 Slice R4 — Events).
 *
 * Regression context (2026-05-20 trust-recovery audit):
 *   The cloud-list ResourceDetailPage rendered an EventsPanel tab and
 *   wired `allEvents` from the page-level k8sSnapshot — but the
 *   SSE subscription opened by `ResourceDetailRoute` used the DEFAULT
 *   `GRAPH_K8S_KINDS` list, which intentionally omits `event` to keep
 *   the cardinality of the CloudPage canvas snapshot bounded. The
 *   detail page inherited that omission → snapshot never contained
 *   any `event:` keyed entry → `allEvents` was always empty →
 *   EventsPanel always rendered `events-panel-empty`. Anti-pattern
 *   catalogue item from CLAUDE.md §4: "defensive-coding empty-state
 *   hides an upstream that never produced data."
 *
 * This test pins the fix: the resource-detail SSE URL must include
 * `event` in its `kinds=` query parameter, so Events surfaced by the
 * server-side k8scache Factory (already registered per
 * `products/catalyst/bootstrap/api/internal/k8scache/kinds.go:155`)
 * reach the EventsPanel widget.
 *
 * jsdom does not implement EventSource — install a minimal global
 * shim and assert the URL the hook hands to it.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, cleanup } from '@testing-library/react'

interface CapturedES {
  url: string
  close: () => void
}

let activeES: CapturedES | null = null

class FakeEventSource implements CapturedES {
  url: string
  onopen: ((e: Event) => void) | null = null
  onmessage: ((e: MessageEvent) => void) | null = null
  onerror: ((e: Event) => void) | null = null
  closed = false
  constructor(url: string) {
    this.url = url
    // Capture for the test harness via a module-level reference
    // instead of `this` (avoids the no-this-alias lint rule).
    captureES(this)
  }
  close = () => {
    this.closed = true
  }
}

function captureES(es: CapturedES): void {
  activeES = es
}

// TanStack router and the shared deployment-id hook are imported by
// ResourceDetailRoute. The route is normally mounted under
// `RouterProvider`; in jsdom we mock the consumer hooks so the route
// renders standalone and we can assert the SSE URL contract.
vi.mock('@tanstack/react-router', () => ({
  useParams: () => ({
    deploymentId: 'dep-1',
    kind: 'pod',
    ns: 'default',
    name: 'wp-1',
    // 'overview' renders the lightest tab content (no Monaco editor,
    // no Recharts MetricsPanel, no log WebSocket). The SSE URL we
    // assert is opened by the page's `useK8sCacheStream` BEFORE any
    // tab content mounts, so the tab choice does not affect coverage.
    tab: 'overview',
  }),
  useNavigate: () => () => {},
}))

vi.mock('@/shared/lib/useResolvedDeploymentId', () => ({
  useResolvedDeploymentId: () => ({ deploymentId: 'dep-1' }),
}))

vi.mock('@/shared/lib/detectMode', () => ({
  DETECTED_MODE: { mode: 'mothership' },
}))

vi.mock('@/shared/lib/oidc', () => ({
  loadTokens: () => null,
}))

// Short-circuit the resource fetch so jsdom does not hang on a real
// `fetch` call. The SSE URL we assert is opened by the page's
// `useK8sCacheStream` (which uses EventSource, mocked above), so the
// REST fetch is orthogonal to this test.
vi.mock('@/shared/lib/authedFetch', () => ({
  authedFetch: () =>
    Promise.resolve(
      new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } }),
    ),
}))

vi.mock('@/shared/config/urls', () => ({
  API_BASE: '/api',
}))

beforeEach(() => {
  activeES = null
  // @ts-expect-error — install fake on the global
  globalThis.EventSource = FakeEventSource
})

afterEach(() => {
  cleanup()
  activeES = null
  // @ts-expect-error — clean up
  delete globalThis.EventSource
})

describe('ResourceDetailRoute SSE subscription', () => {
  it('subscribes to event kind so EventsPanel sees Events', async () => {
    const { ResourceDetailRoute } = await import('./ResourceDetailRoute')
    render(<ResourceDetailRoute />)
    expect(activeES).not.toBeNull()
    const url = activeES!.url
    // The catalyst-api SSE accepts `?kinds=` as a comma-separated
    // list. Assert `event` is present so the EventsPanel feed lights
    // up; without this the panel renders perpetual empty-state.
    expect(url).toMatch(/kinds=[^&]*event/)
    // Sanity: the canvas kinds (pod, deployment, service) must still
    // be subscribed — we extend, never replace.
    expect(url).toMatch(/kinds=[^&]*pod/)
    expect(url).toMatch(/kinds=[^&]*deployment/)
    expect(url).toMatch(/kinds=[^&]*service/)
  })
})
