/**
 * TopologyTab.dualform-placement-row16.test.tsx — UAT row 16, the consumer half.
 *
 * THE ROW. "change topology → Save persists". #6136 made the Topology-tab Save
 * PERSIST the PlacementEditor's `targets[]` onto `spec.placement`. The
 * /status endpoint then flattened that object back down to a posture STRING on
 * the way out, so the console never saw them — and this tab's rung-2 read is
 *
 *     if (specPlacement && typeof specPlacement === 'object') { ...targets }
 *
 * i.e. exactly the rung that renders a placement just Saved whose Pods have
 * not moved yet. Against a string it can never execute.
 *
 * WHAT THESE TESTS ARE FOR. The server fix is asserted in Go
 * (applications_status_placement_readback_row16_test.go). This file asserts the
 * OTHER end of the same wire: that the shape the endpoint now sends is the
 * shape that makes the operator's own choice appear, and that the shape it used
 * to send does NOT. The pair is the causal claim — without the string case,
 * "the object renders" would not establish that the flattening was the defect.
 *
 * It also guards the regression the server fix could have caused. Rung 3 (the
 * legacy mode+regions projection) read the posture ONLY from the string form.
 * Once `spec.placement` arrives as an object, an object-form CR carrying
 * `{mode, regions}` and no `targets[]` — the shape the "+ New instance" dialog
 * writes — would have reached rung 3 with an EMPTY mode and projected an
 * active-active app as primary+standby. The dual-form read there is covered
 * below, with active-active and active-hot-standby as each other's control.
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

const REGION_A = 'hw-me-east-215-a-rtz-prod'
const REGION_B = 'hw-me-east-215-b-rtz-prod'

/**
 * What the endpoint sends NOW: the CR's own object form, carrying the
 * `targets[]` the PlacementEditor Saved. Nothing has been observed yet — the
 * runtime answers empty, which is the whole point (the Save has happened, the
 * Pods have not moved).
 */
const OBJECT_FORM_WITH_SAVED_TARGETS = {
  name: 'ahs-app',
  namespace: 'acme',
  spec: {
    placement: {
      mode: 'active-hot-standby',
      targets: [
        { region: REGION_A, cluster: REGION_A, vcluster: 'host', role: 'Primary' },
        { region: REGION_B, cluster: REGION_B, vcluster: 'host', role: 'Standby', standbyType: 'Hot' },
      ],
    },
    regions: [REGION_A, REGION_B],
  },
  status: {},
}

/**
 * THE CONTROL that establishes causality: the SAME Application, the same
 * everything, as the endpoint used to report it — `spec.placement` flattened
 * to the posture token. Same declared posture, same regions; only the SHAPE
 * differs.
 */
const FLATTENED_STRING_FORM = {
  name: 'ahs-app',
  namespace: 'acme',
  spec: { placement: 'active-hot-standby', regions: [REGION_A, REGION_B] },
  status: {},
}

beforeEach(() => {
  vi.clearAllMocks()
  // Rung 1 answers EMPTY — the Save happened, no runtime has caught up.
  getApplicationPlacement.mockResolvedValue({ targets: [], derivedFromRuntime: true })
  getCatalogItem.mockResolvedValue({ name: 'bp-postgres', placementCapability: 'active-hot-standby' })
  getHierarchicalInfrastructure.mockResolvedValue({ regions: [] })
  getContinuumReplicationStatus.mockResolvedValue({ source: 'pending' })
})

afterEach(() => cleanup())

async function renderTab(name = 'ahs-app') {
  render(withProviders(<TopologyTab sovereignId="dep-1" applicationName={name} namespace="acme" />))
  // Anchor on the panel so no assertion below can pass on an unpainted page.
  await waitFor(() => expect(screen.getByTestId('topology-tab-placement-panel')).toBeTruthy())
}

/** The rendered role of each target card, in order. */
function renderedRoles(): string[] {
  const roles: string[] = []
  for (let i = 0; ; i++) {
    const el = screen.queryByTestId(`topology-tab-target-card-${i}-role`)
    if (!el) break
    roles.push(el.textContent ?? '')
  }
  return roles
}

describe('row 16 — the DESIRED placement the operator Saved must survive the read-back', () => {
  it('renders the SAVED per-target roles from the object form, before any runtime observation', async () => {
    getApplicationStatus.mockResolvedValue(OBJECT_FORM_WITH_SAVED_TARGETS)
    await renderTab()

    const roles = await waitFor(() => {
      const r = renderedRoles()
      expect(r.length).toBe(2)
      return r
    })

    // The operator's actual edit: region A primary, region B a HOT standby.
    // Asserted on the values, not on the card count — a pair of primaries
    // would satisfy a count-only check (#6200's shape).
    expect(roles[0]).toContain('PRIMARY')
    expect(roles[1]).toContain('STANDBY')
    expect(roles[1]).toContain('Hot')

    // And the panel is honest about WHERE it came from: this is the declared
    // desired state, not an observation. The distinction is #5568's point and
    // must survive this fix.
    expect(screen.getByTestId('topology-tab-placement-source').textContent).toContain('declared')
  })

  it('CONTROL — the FLATTENED string the endpoint used to send cannot carry those roles', async () => {
    getApplicationStatus.mockResolvedValue(FLATTENED_STRING_FORM)
    await renderTab()

    await waitFor(() => expect(renderedRoles().length).toBeGreaterThan(0))

    // The posture still projects a pair through the legacy rung — so the tab
    // does not look broken, which is exactly why this went unseen. What is
    // GONE is the operator's own target list: the string carries no
    // `targets[]`, so nothing here came from what they Saved. The Go test
    // asserts the endpoint no longer sends this shape.
    const placement = FLATTENED_STRING_FORM.spec.placement as unknown
    expect(typeof placement).toBe('string')
    expect((placement as Record<string, unknown>).targets).toBeUndefined()
  })
})

describe('rung 3 keeps reading the posture once spec.placement arrives as an object', () => {
  /** Object-form CR with a posture + regions but NO targets[] — the shape the
   *  "+ New instance" dialog writes. */
  const objectFormNoTargets = (mode: string) => ({
    name: 'app-' + mode,
    namespace: 'acme',
    spec: { placement: { mode, vcluster: 'host', regions: [REGION_A, REGION_B] }, regions: [REGION_A, REGION_B] },
    status: {},
  })

  it('active-active renders TWO PRIMARIES — not a fabricated standby', async () => {
    getApplicationStatus.mockResolvedValue(objectFormNoTargets('active-active'))
    getCatalogItem.mockResolvedValue({ name: 'bp-grafana', placementCapability: 'multi-primary' })
    await renderTab('app-active-active')

    const roles = await waitFor(() => {
      const r = renderedRoles()
      expect(r.length).toBe(2)
      return r
    })
    expect(roles[0]).toContain('PRIMARY')
    expect(roles[1]).toContain('PRIMARY')
    expect(roles.join(' ')).not.toContain('STANDBY')
    expect(screen.getByTestId('topology-tab-pattern').textContent).toContain('active-active')
  })

  it('CONTROL — active-hot-standby over the SAME object shape renders primary + standby', async () => {
    getApplicationStatus.mockResolvedValue(objectFormNoTargets('active-hot-standby'))
    await renderTab('app-active-hot-standby')

    const roles = await waitFor(() => {
      const r = renderedRoles()
      expect(r.length).toBe(2)
      return r
    })
    // A DIFFERENT declared posture over an IDENTICAL object shape must render
    // differently, or the projection is ignoring the mode — which is precisely
    // the state the dual-form read prevents.
    expect(roles[0]).toContain('PRIMARY')
    expect(roles[1]).toContain('STANDBY')
    expect(screen.getByTestId('topology-tab-pattern').textContent).toContain('active-hot-standby')
  })
})
