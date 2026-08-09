/**
 * TopologyTab.bootstrapStatus-5934.test.tsx — #5934 (UAT rows 67, 69).
 *
 * THE DEFECT THESE GUARDS PIN. The app-detail Status panel printed the literal
 * string "n/a — bootstrap component (HelmRelease, no Application CR)" for every
 * bootstrap-kit component, on a page whose own header and Overview read STATUS
 * Ready. One page contradicting itself, three inches apart.
 *
 * Two lines caused it, and only fixing BOTH moves the rows:
 *   • the status poll was disabled outright when `isBootstrap`, so the console
 *     never asked; and
 *   • the Status panel branched on `isBootstrap` FIRST, hard-rendering the n/a
 *     string regardless of what the API returned — so even an un-gated poll
 *     would have been ignored.
 *
 * WHY THIS IS NOT DEPLOY-GATED. #5836 already landed the API half:
 * HandleApplicationStatus now falls through to statusFromSynthesised (HR first,
 * then runtime) instead of hard-404ing for a component with no Application CR.
 * That fix is on main. But a rolled image changes nothing while the front end
 * refuses to ask and refuses to listen — which is exactly why these rows were
 * about to be mis-blamed on delivery.
 *
 * WHAT THE FALLBACK STILL MEANS. The n/a prose is kept, narrowed to its honest
 * case: the endpoint was asked and genuinely had nothing (synthesis missed →
 * 404). "We did not ask" and "there is no answer" must not render identically.
 */
import { describe, it, expect, afterEach, beforeEach, vi } from 'vitest'
import { render, screen, cleanup, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

const getApplicationStatus = vi.fn()
const getCatalogItem = vi.fn()
const getApplicationPlacement = vi.fn()
const getHierarchicalInfrastructure = vi.fn()
const getContinuumReplicationStatus = vi.fn()

vi.mock('@/lib/catalog.api', () => ({
  getApplicationStatus: (...a: unknown[]) => getApplicationStatus(...a),
  getCatalogItem: (...a: unknown[]) => getCatalogItem(...a),
  getApplicationPlacement: (...a: unknown[]) => getApplicationPlacement(...a),
}))

vi.mock('@/lib/continuum.api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/continuum.api')>()
  return {
    ...actual,
    getContinuumReplicationStatus: (...a: unknown[]) => getContinuumReplicationStatus(...a),
  }
})

vi.mock('@/lib/infrastructure.types', () => ({
  getHierarchicalInfrastructure: (...a: unknown[]) => getHierarchicalInfrastructure(...a),
}))

vi.mock('@/widgets/topology/PlacementEditor', () => ({
  PlacementEditor: () => <div data-testid="stub-placement-editor" />,
}))

import { TopologyTab } from './TopologyTab'

function withProviders(node: React.ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return <QueryClientProvider client={qc}>{node}</QueryClientProvider>
}

/** The literal string the panel used to hard-render for every bootstrap component. */
const NA_PROSE = /n\/a — bootstrap component/

beforeEach(() => {
  getApplicationPlacement.mockResolvedValue({ targets: [], derivedFromRuntime: true })
  getHierarchicalInfrastructure.mockResolvedValue({ topology: { regions: [] } })
  getContinuumReplicationStatus.mockRejectedValue(new Error('continuum replication-status: HTTP 404'))
})

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

describe('TopologyTab — bootstrap component status (#5934, UAT rows 67 + 69)', () => {
  // THE ROW-67/69 GUARD. bp-grafana is a bootstrap-kit HelmRelease with no
  // Application CR. Since #5836 its /status answers Ready (synthesised from the
  // HR). The panel must show THAT, not the hardcoded abstention.
  it('renders the REAL status the API returns for a bootstrap component, not the hardcoded n/a string', async () => {
    getApplicationStatus.mockResolvedValue({
      name: 'bp-grafana',
      namespace: 'flux-system',
      phase: 'Ready',
      status: {},
    })

    render(
      withProviders(
        <TopologyTab sovereignId="test-sov" applicationName="bp-grafana" namespace="flux-system" isBootstrap />,
      ),
    )

    // POSITIVE FIRST. TanStack mounts on a microtask, so asserting an element is
    // ABSENT before proving the subject rendered passes for the wrong reason —
    // the whole panel is empty at that instant. Wait for the real chip, then the
    // negative assertions mean something.
    await waitFor(() => {
      expect(screen.getByTestId('topology-tab-recon-status')).toBeTruthy()
    })
    expect(screen.getByTestId('topology-tab-recon-status').textContent).toContain('Reconciled')
    expect(screen.queryByTestId('topology-tab-status-bootstrap')).toBeNull()
    expect(screen.queryByText(NA_PROSE)).toBeNull()
  })

  // The poll gate itself. Kept separate from the render assertion above because
  // they were two DIFFERENT lines: un-gating the poll without un-gating the
  // render still prints n/a, and un-gating the render without the poll leaves
  // the panel with nothing to render.
  it('POLLS the status endpoint for a bootstrap component (#5836 made it answer)', async () => {
    getApplicationStatus.mockResolvedValue({
      name: 'bp-keycloak',
      namespace: 'flux-system',
      phase: 'Ready',
      status: {},
    })

    render(
      withProviders(
        <TopologyTab sovereignId="test-sov" applicationName="bp-keycloak" namespace="flux-system" isBootstrap />,
      ),
    )

    await waitFor(() => {
      expect(getApplicationStatus).toHaveBeenCalled()
    })
    expect(getApplicationStatus).toHaveBeenCalledWith('test-sov', 'bp-keycloak', 'flux-system')
  })

  // A Degraded component must read Degraded. A fix that pinned the chip to a
  // constant "Reconciled" would satisfy the first guard and be a worse lie than
  // the n/a string it replaced.
  it('a Degraded bootstrap component reads Degraded, not a constant green chip', async () => {
    getApplicationStatus.mockResolvedValue({
      name: 'bp-grafana',
      namespace: 'flux-system',
      phase: 'Degraded',
      status: { placementReason: 'HelmRelease not ready: upgrade retries exhausted' },
    })

    render(
      withProviders(
        <TopologyTab sovereignId="test-sov" applicationName="bp-grafana" namespace="flux-system" isBootstrap />,
      ),
    )

    await waitFor(() => {
      expect(screen.getByTestId('topology-tab-recon-status').textContent).toContain('Degraded')
    })
    expect(screen.getByTestId('topology-tab-recon-reason').textContent).toContain('upgrade retries exhausted')
  })

  // THE FALLBACK, narrowed to its honest case. When /status genuinely has
  // nothing (synthesis missed both the HR and the runtime paths → 404), the
  // panel abstains rather than inventing a status. This is the ONLY case the
  // n/a prose may still appear in.
  it('falls back to the n/a prose when the endpoint genuinely has no answer (404)', async () => {
    getApplicationStatus.mockRejectedValue(new Error('application status: HTTP 404 application-not-found'))

    render(
      withProviders(
        <TopologyTab sovereignId="test-sov" applicationName="bp-nothing" namespace="flux-system" isBootstrap />,
      ),
    )

    await waitFor(() => {
      expect(screen.getByTestId('topology-tab-status-bootstrap')).toBeTruthy()
    })
    expect(screen.queryByTestId('topology-tab-recon-status')).toBeNull()
  })

  // #3656's original concern was a 404 LOOP, and it is still valid — it was the
  // right worry answered with too blunt an instrument (never ask at all). Once
  // the endpoint has said it has nothing, stop re-asking; while it answers, keep
  // polling like any other app.
  it('stops re-polling once the endpoint has answered 404 (the #3656 loop concern, kept)', async () => {
    getApplicationStatus.mockRejectedValue(new Error('application status: HTTP 404 application-not-found'))

    render(
      withProviders(
        <TopologyTab sovereignId="test-sov" applicationName="bp-nothing" namespace="flux-system" isBootstrap />,
      ),
    )

    await waitFor(() => {
      expect(screen.getByTestId('topology-tab-status-bootstrap')).toBeTruthy()
    })
    const callsAfterFirstAnswer = getApplicationStatus.mock.calls.length
    await new Promise((r) => setTimeout(r, 50))
    expect(getApplicationStatus.mock.calls.length).toBe(callsAfterFirstAnswer)
  })

  // CONTROL — the non-bootstrap path already worked and must be undisturbed.
  it('CONTROL: a non-bootstrap app still polls and still renders its recon chip', async () => {
    getApplicationStatus.mockResolvedValue({
      name: 'wordpress',
      namespace: 'qa-omantel',
      phase: 'Ready',
      spec: { placement: 'single-region', regions: [] },
      status: {},
    })

    render(withProviders(<TopologyTab sovereignId="test-sov" applicationName="wordpress" namespace="qa-omantel" />))

    await waitFor(() => {
      expect(screen.getByTestId('topology-tab-recon-status').textContent).toContain('Reconciled')
    })
    expect(getApplicationStatus).toHaveBeenCalledWith('test-sov', 'wordpress', 'qa-omantel')
    expect(screen.queryByTestId('topology-tab-status-bootstrap')).toBeNull()
  })

  // CONTROL — the pre-filled seam (initialApp) must not start a network poll.
  it('CONTROL: initialApp still short-circuits the poll entirely', async () => {
    render(
      withProviders(
        <TopologyTab
          sovereignId="test-sov"
          applicationName="bp-grafana"
          namespace="flux-system"
          isBootstrap
          initialApp={{ name: 'bp-grafana', namespace: 'flux-system', phase: 'Ready', status: {} }}
        />,
      ),
    )

    await waitFor(() => {
      expect(screen.getByTestId('topology-tab-recon-status')).toBeTruthy()
    })
    expect(getApplicationStatus).not.toHaveBeenCalled()
  })
})
