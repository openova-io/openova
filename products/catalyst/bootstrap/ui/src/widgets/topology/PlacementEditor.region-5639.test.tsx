/**
 * PlacementEditor.region-5639.test.tsx — #5639 guard, BOTH directions.
 *
 * THE DEFECT THIS PINS. hw292 (dep 1c56518035a83e03) ran a per-Org
 * `bp-postgres` whose CNPG Cluster carried
 *
 *     openova.io/region: ""
 *     nodeAffinity ... values: [""]
 *
 * for 2d9h in `Setting up primary`, 0 ready, while the HelmRelease reported
 * install succeeded. No node can ever carry the empty-string region, so that
 * pod is unschedulable FOREVER — not slow, broken.
 *
 * #5641 fixed the CHART (a Helm `required` on `topology.primary.region`) and
 * the per-Org producer. This file pins the OTHER producer of the identical
 * shape, which no prior PR touched: the operator-facing placement editor.
 *
 * TWO STACKED DEFECTS, and the second is the load-bearing one:
 *
 *  1. DERIVATION — the Region <select> renders its options as
 *     `[t.region, ...availableRegions].filter(Boolean)`. When the infra
 *     topology query returns nothing (it is `enabled: !!sovereignId`, it
 *     401s without a session, and it throws on any non-2xx) AND the
 *     Application has no targets yet, `availableRegions` is `[]` and the
 *     seeded target is `region: availableRegions[0] ?? ''`. Both halves are
 *     falsy, so the control renders ZERO options: a blank box with no
 *     explanation of why it is blank.
 *
 *  2. VALIDATION — `validatePlacement` checked role, standbyType and the
 *     Primary count, and NEVER the region. So the empty selector was
 *     SUBMITTABLE: Apply was enabled, and it PUT
 *     `targets: [{ region: '', ... }]` — a placement with no region, which
 *     is exactly the live hw292 shape. Fixing only (1) leaves this armed for
 *     every future case where the list is legitimately empty.
 *
 * THE NEGATIVE CONTROL IS NOT OPTIONAL. A "fix" that simply refused to apply
 * any placement would satisfy the two positive assertions above. So the
 * singleton-on-a-single-region-Sovereign case below MUST stay green: it is
 * the control proving the gate rejects the empty region specifically and not
 * placement editing in general.
 *
 * Confirmed RED against the pre-fix tree — see the PR body for real output.
 */
import { describe, it, expect, afterEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'

import { PlacementEditor } from './PlacementEditor'
import type { PlacementTarget } from '@/shared/lib/placement'

// Capture what actually reaches the wire. A "the button was disabled"
// assertion alone cannot tell an empty region from a real one; this can.
const updateApplicationMock = vi.fn<(...args: unknown[]) => Promise<unknown>>(() =>
  Promise.resolve({}),
)
vi.mock('@/lib/catalog.api', () => ({
  updateApplication: (...args: unknown[]) => updateApplicationMock(...args),
}))

/** The body PlacementEditor PUTs — third positional arg of updateApplication. */
type AppliedBody = { placement: { targets: Array<{ region: string }> } }
const appliedBody = (call: number): AppliedBody =>
  updateApplicationMock.mock.calls[call][2] as AppliedBody

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

function renderEditor(overrides: Partial<React.ComponentProps<typeof PlacementEditor>> = {}) {
  return render(
    <PlacementEditor
      sovereignId="s"
      applicationName="postgres"
      initialTargets={[]}
      capability="primary+standby"
      availableRegions={[]}
      availableClusters={[]}
      {...overrides}
    />,
  )
}

const applyBtn = () => screen.getByTestId('placement-editor-apply') as HTMLButtonElement

describe('#5639 (1) — an unresolvable region list is VISIBLE, not a blank box', () => {
  it('renders a legible empty-state instead of a <select> with zero options', () => {
    renderEditor()
    const sel = screen.getByTestId('placement-editor-target-0-region') as HTMLSelectElement
    const options = Array.from(sel.querySelectorAll('option'))

    // Pre-fix this array was EMPTY — the operator saw a blank control and
    // had no way to tell "no regions reported" from "still loading".
    expect(options.length).toBeGreaterThan(0)
    expect(screen.getByTestId('placement-editor-target-0-region-empty')).toBeTruthy()
    expect(sel.textContent?.toLowerCase()).toContain('no regions reported')
  })

  it('the empty-state is ABSENT when regions ARE reported (non-vacuity)', () => {
    renderEditor({ availableRegions: ['hw-me-east-215-a-rtz-prod'] })
    // A guard that always rendered the empty-state would pass the test above
    // on every input. This is the direction that catches it.
    expect(screen.queryByTestId('placement-editor-target-0-region-empty')).toBeNull()
  })
})

describe('#5639 (2) — an empty region CANNOT be submitted', () => {
  it('disables Apply and names the reason when the seeded target has no region', () => {
    renderEditor()
    // Pre-fix: validatePlacement returned null, so Apply was ENABLED.
    expect(applyBtn().disabled).toBe(true)

    const reason = screen.getByTestId('placement-editor-validation').textContent ?? ''
    // "Legible" means it names the field and the consequence — not a code.
    expect(reason.toLowerCase()).toContain('region')
    expect(reason).toMatch(/target 1/i)
  })

  it('no empty-region placement reaches the wire even if Apply is clicked', async () => {
    renderEditor()
    fireEvent.click(applyBtn())
    // Pre-fix this PUT `targets: [{ region: '', ... }]` — the live hw292
    // shape, produced from the console.
    await waitFor(() => expect(updateApplicationMock).not.toHaveBeenCalled())
  })

  it('a Standby added with no region left to assign is caught too', () => {
    // The second half of the same expression: addTarget() falls back to
    // `availableRegions[1] ?? availableRegions[0] ?? ''`, so a 2-target
    // active-hot-standby on a topology that reports ONE region silently gave
    // the standby an empty (pre-fix) region.
    renderEditor({
      availableRegions: ['hw-me-east-215-a-rtz-prod'],
      availableClusters: ['hw-me-east-215-a-rtz-prod'],
      initialTargets: [
        { region: 'hw-me-east-215-a-rtz-prod', cluster: 'c-a', vcluster: 'mgmt', role: 'Primary' },
        { region: '', cluster: '', vcluster: 'mgmt', role: 'Standby', standbyType: 'Hot' },
      ] as PlacementTarget[],
    })
    expect(applyBtn().disabled).toBe(true)
    expect((screen.getByTestId('placement-editor-validation').textContent ?? '')).toMatch(/target 2/i)
  })
})

describe('#5639 negative control — placements that DO carry a region still apply', () => {
  it('singleton on a SINGLE-region Sovereign is still applicable', async () => {
    // The case the fix must not break: one region reported, one Primary
    // target, no standby. If this ever goes red the gate has become a
    // blanket block and the two assertions above are satisfied by breaking
    // placement editing outright.
    renderEditor({
      availableRegions: ['hw-me-east-215-a-rtz-prod'],
      availableClusters: ['hw-me-east-215-a-rtz-prod'],
    })
    expect(screen.getByTestId('placement-editor-derived-pattern').textContent).toContain('singleton')
    expect(applyBtn().disabled).toBe(false)

    fireEvent.click(applyBtn())
    await waitFor(() => expect(updateApplicationMock).toHaveBeenCalledTimes(1))

    // Assert on the VALUE that reached the wire, not merely that a call
    // happened — an empty region would also have produced one call.
    expect(appliedBody(0).placement.targets[0].region).toBe('hw-me-east-215-a-rtz-prod')
  })

  it('a two-region active-hot-standby still applies with both real regions', async () => {
    renderEditor({
      availableRegions: ['hw-me-east-215-a-rtz-prod', 'hw-me-east-215-b-rtz-prod'],
      availableClusters: ['c-a', 'c-b'],
      initialTargets: [
        { region: 'hw-me-east-215-a-rtz-prod', cluster: 'c-a', vcluster: 'mgmt', role: 'Primary' },
        {
          region: 'hw-me-east-215-b-rtz-prod',
          cluster: 'c-b',
          vcluster: 'mgmt',
          role: 'Standby',
          standbyType: 'Hot',
        },
      ] as PlacementTarget[],
    })
    expect(screen.getByTestId('placement-editor-derived-pattern').textContent).toContain(
      'active-hot-standby',
    )
    expect(applyBtn().disabled).toBe(false)

    fireEvent.click(applyBtn())
    await waitFor(() => expect(updateApplicationMock).toHaveBeenCalledTimes(1))
    expect(appliedBody(0).placement.targets.map((t) => t.region)).toEqual([
      'hw-me-east-215-a-rtz-prod',
      'hw-me-east-215-b-rtz-prod',
    ])
  })
})
