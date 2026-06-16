/**
 * TopologyTab.test.tsx — #3656 regression lock (founder #6, UI-faithfulness).
 *
 * Founder report (hw150, 2026-06-16): on a bootstrap-kit HelmRelease with NO
 * Application CR (e.g. bp-alloy — the AppDetail hero shows
 * `source: HelmRelease`), the Topology tab's "Live status" panel stuck on
 * "Loading status…" while the browser console accumulated 59+ errors in ~2 min,
 * all `GET /applications/{name}/status?namespace=flux-system → 404`. The status
 * poller assumed an Application CR exists and retried in a tight loop forever.
 *
 * These tests lock the fix:
 *   1. When `isBootstrap`, the status endpoint is NEVER called (no poll, no
 *      404 loop) and the panel renders the calm n/a message.
 *   2. When NOT bootstrap, the status endpoint IS called (normal behaviour
 *      preserved).
 *
 * The fix is GENERIC — it gates on "no Application CR" (the `isBootstrap`
 * signal the parent derives from `apiApp.bootstrap`), never on a blueprint
 * name.
 */
import { describe, it, expect, afterEach, vi } from 'vitest'
import { render, screen, cleanup, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

/* ── Mock the API + infra clients so no network is hit ─────────────────── */

const getApplicationStatus = vi.fn()
const getCatalogItem = vi.fn()
const getHierarchicalInfrastructure = vi.fn()

vi.mock('@/lib/catalog.api', () => ({
  getApplicationStatus: (...a: unknown[]) => getApplicationStatus(...a),
  getCatalogItem: (...a: unknown[]) => getCatalogItem(...a),
}))

vi.mock('@/lib/infrastructure.types', () => ({
  getHierarchicalInfrastructure: (...a: unknown[]) => getHierarchicalInfrastructure(...a),
}))

// Stub the heavy child widgets — the poller behaviour is what we assert,
// not the editor / DR section internals (covered by their own tests).
vi.mock('@/widgets/topology/TopologyEditor', () => ({
  TopologyEditor: () => <div data-testid="stub-topology-editor" />,
}))
vi.mock('@/widgets/continuum/DRSection', () => ({
  DRSection: () => <div data-testid="stub-dr-section" />,
}))

import { TopologyTab } from './TopologyTab'

function withProviders(node: React.ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return <QueryClientProvider client={qc}>{node}</QueryClientProvider>
}

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

describe('TopologyTab — bootstrap HelmRelease status poll (#3656)', () => {
  it('does NOT poll the status endpoint for a bootstrap component (no 404 loop)', async () => {
    getHierarchicalInfrastructure.mockResolvedValue({ topology: { regions: [] } })

    render(
      withProviders(
        <TopologyTab
          sovereignId="test-sov"
          applicationName="bp-alloy"
          namespace="flux-system"
          isBootstrap
        />,
      ),
    )

    // The calm n/a panel renders instead of a perpetual "Loading status…".
    await waitFor(() => {
      expect(screen.getByTestId('topology-tab-status-bootstrap')).toBeTruthy()
    })
    expect(screen.queryByTestId('topology-tab-status-loading')).toBeNull()

    // Crucially: the status endpoint was never called, so it can never 404-loop.
    expect(getApplicationStatus).not.toHaveBeenCalled()
  })

  it('DOES poll the status endpoint for a non-bootstrap app (Application CR exists)', async () => {
    getHierarchicalInfrastructure.mockResolvedValue({ topology: { regions: [] } })
    getApplicationStatus.mockResolvedValue({
      name: 'wordpress',
      namespace: 'qa-omantel',
      spec: { placement: 'single-region', regions: [] },
      status: {},
    })

    render(
      withProviders(
        <TopologyTab
          sovereignId="test-sov"
          applicationName="wordpress"
          namespace="qa-omantel"
        />,
      ),
    )

    await waitFor(() => {
      expect(getApplicationStatus).toHaveBeenCalled()
    })
    // The bootstrap panel must NOT show for a CR-backed app.
    expect(screen.queryByTestId('topology-tab-status-bootstrap')).toBeNull()
  })
})
