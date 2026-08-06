/**
 * PlacementEditor.vcluster-5616.test.tsx — #5616 guard, BOTH directions.
 *
 * THE DEFECT THIS PINS. `placement.vcluster` carries a TIER KEY. The
 * application-controller resolves that key to a HOST NAMESPACE through
 * its VClusterPlacements map (namespace == tier name by default) and
 * addresses the per-cluster HelmRelease there. `clusters/_template/
 * bootstrap-kit/` installs NO mgmt / dmz / rtz vCluster and creates no
 * such namespace — verified live on hw292 2026-08-04: 53 namespaces,
 * none of mgmt/dmz/rtz. So those three keys can only ever produce
 *
 *     Application uatco/uatco-agenity  phase=Degraded
 *       upsert per-cluster HelmRelease rtz/uatco-agenity-rtz-a:
 *         namespaces "rtz" not found
 *
 * PR #5622 fixed the CREATE dialog (AppDetail/InstancesSection.tsx). It
 * did not touch THIS component, which is a different door in the SAME
 * shipped tree — and a worse one: the create dialog at least required
 * the operator to pick a dead option, whereas this editor SEEDED a fresh
 * target with `DEFAULT_VCLUSTERS[1]` — literally `'mgmt'` — so an
 * operator who never opened the vCluster control still committed a dead
 * tier the moment they pressed Apply.
 *
 * WHAT THE GUARD ASSERTS. Not button state, not the option list alone:
 * the VALUE that reaches the wire. `updateApplication` is mocked and the
 * PUT body is read back, because "the dropdown looked right" is exactly
 * the evidence that let #5616 ship in the first place.
 *
 * THE CONTROLS ARE LOAD-BEARING. A "fix" that hardcoded every target to
 * `host` would satisfy the first assertion. So:
 *   - an operator-chosen tier must still reach the wire unchanged, and
 *   - an Application ALREADY placed on `mgmt` must keep `mgmt` visible
 *     and selected, or the editor would silently rewrite a real
 *     placement on the next Apply.
 *
 * Confirmed RED against the pre-fix tree — real output in the PR body.
 */
import { describe, it, expect, afterEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'

import { PlacementEditor } from './PlacementEditor'

const updateApplicationMock = vi.fn<(...args: unknown[]) => Promise<unknown>>(() =>
  Promise.resolve({}),
)
vi.mock('@/lib/catalog.api', () => ({
  updateApplication: (...args: unknown[]) => updateApplicationMock(...args),
}))

/** The body PlacementEditor PUTs — third positional arg of updateApplication. */
type AppliedBody = {
  placement: { targets: Array<{ region: string; cluster: string; vcluster?: string }> }
}
const appliedBody = (call = 0): AppliedBody =>
  updateApplicationMock.mock.calls[call][2] as AppliedBody

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

const REGION = 'hw-me-east-215-a-rtz-prod'

function renderEditor(overrides: Partial<React.ComponentProps<typeof PlacementEditor>> = {}) {
  return render(
    <PlacementEditor
      sovereignId="s"
      applicationName="agenity"
      initialTargets={[]}
      capability="primary+standby"
      availableRegions={[REGION]}
      availableClusters={[REGION]}
      {...overrides}
    />,
  )
}

const tierSelect = (i = 0) =>
  screen.getByTestId(`placement-editor-target-${i}-vcluster`) as HTMLSelectElement
const optionValues = (sel: HTMLSelectElement) =>
  Array.from(sel.querySelectorAll('option')).map((o) => o.value)

describe('#5616 — an untouched placement must not default to a dead tier', () => {
  it('seeds a fresh target on host, not mgmt', () => {
    renderEditor()
    // Pre-fix: DEFAULT_VCLUSTERS[1] === 'mgmt'.
    expect(tierSelect().value).toBe('host')
  })

  it('the tier that reaches the wire on an untouched Apply is host', async () => {
    renderEditor()
    fireEvent.click(screen.getByTestId('placement-editor-apply'))
    await waitFor(() => expect(updateApplicationMock).toHaveBeenCalledTimes(1))
    // THE assertion: pre-fix this was `"mgmt"`, and the controller then
    // addressed the HelmRelease into a namespace that does not exist.
    expect(appliedBody().placement.targets[0].vcluster).toBe('host')
  })

  it('a Standby added after the Primary inherits host, not mgmt', async () => {
    renderEditor({ availableRegions: [REGION, 'hw-me-east-215-b-rtz-prod'] })
    fireEvent.click(screen.getByTestId('placement-editor-add-target'))
    fireEvent.click(screen.getByTestId('placement-editor-apply'))
    await waitFor(() => expect(updateApplicationMock).toHaveBeenCalledTimes(1))
    for (const t of appliedBody().placement.targets) {
      expect(t.vcluster).toBe('host')
    }
  })

  it('does not offer a fabricated tier the Sovereign never installed', () => {
    renderEditor()
    // Pre-fix the fallback list was ['host','mgmt','dmz','rtz'] — four
    // options, three of which could only ever Degrade.
    const values = optionValues(tierSelect())
    expect(values).toContain('host')
    expect(values).not.toContain('dmz')
    expect(values).not.toContain('rtz')
  })
})

describe('#5616 controls — the gate must not flatten real placements', () => {
  it('CONTROL: a tier the Sovereign DOES report is offered and reaches the wire', async () => {
    // A fix that hardcoded `host` everywhere would pass every assertion
    // above. This is the direction that catches it.
    renderEditor({ availableVClusters: ['host', 'mgmt'] })
    fireEvent.change(tierSelect(), { target: { value: 'mgmt' } })
    fireEvent.click(screen.getByTestId('placement-editor-apply'))
    await waitFor(() => expect(updateApplicationMock).toHaveBeenCalledTimes(1))
    expect(appliedBody().placement.targets[0].vcluster).toBe('mgmt')
  })

  it('CONTROL: an Application already placed on mgmt keeps mgmt selected and selectable', () => {
    renderEditor({
      initialTargets: [{ region: REGION, cluster: 'c-a', vcluster: 'mgmt', role: 'Primary' }],
    })
    // The editor must never hide a placement the Application actually has —
    // shrinking the option list would silently rewrite it on the next Apply.
    expect(tierSelect().value).toBe('mgmt')
    expect(optionValues(tierSelect())).toContain('mgmt')
  })

  it('CONTROL: an existing placement round-trips unchanged through Apply', async () => {
    renderEditor({
      initialTargets: [{ region: REGION, cluster: 'c-a', vcluster: 'mgmt', role: 'Primary' }],
    })
    fireEvent.click(screen.getByTestId('placement-editor-apply'))
    await waitFor(() => expect(updateApplicationMock).toHaveBeenCalledTimes(1))
    expect(appliedBody().placement.targets[0].vcluster).toBe('mgmt')
  })
})
