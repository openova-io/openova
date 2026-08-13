/**
 * InstallPage.regions-row60.test.tsx — UAT row 60.
 *
 * THE DEFECT. `composeInstallRequest` hardcoded the submitted placement as
 *
 *     placement: { mode: placementMode, regions: [region] }
 *
 * over a single `Region` input. So picking `active-hot-standby` in the picker
 * submitted a ONE-region hot-standby, every time. The Application CR then
 * declared a DR posture nothing downstream could back: placement.Resolve puts
 * regions[0] Primary and iterates regions[1..] for the standbys, so a
 * one-region list yields zero standbys, and buildContinuumPlan skips the
 * Continuum CR precisely because `len(standbys) == 0` — leaving the Topology
 * tab's Switchover with nothing to arm against. Refs #6033.
 *
 * WHY THE EXISTING TEST DID NOT CATCH IT. `InstallPage.placement.test.tsx`
 * asserts that the picker OFFERS all four canonical modes and that one can be
 * SELECTED. Both were true the whole time. Neither looks at the request the
 * page actually sends, and the request was where the choice was being
 * discarded — the picker could not fail.
 *
 * So this file asserts the REQUEST BODY, and includes the control that makes
 * the assertion mean something: a DIFFERENT topology must submit a
 * DIFFERENTLY-shaped regions[], or the test would also pass for a page that
 * hardcodes two regions for everything.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent } from '@testing-library/react'

vi.mock('@/shared/lib/useResolvedDeploymentId', () => ({
  useResolvedDeploymentId: () => ({ deploymentId: 'dep-test' }),
}))

const grafanaCard = {
  name: 'bp-grafana',
  version: '1.0.6',
  card: { title: 'Grafana', summary: 'Dashboards', description: 'Dashboards' },
  source: 'gitea',
}

vi.mock('@/lib/useCatalog', () => ({
  useCatalog: () => ({ data: [grafanaCard], isLoading: false, isError: false }),
  useCatalogItemVersion: () => ({ data: { ...grafanaCard, raw: { spec: { configSchema: {} } } }, isLoading: false }),
}))

vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => () => Promise.resolve(undefined),
}))

// Capture the install body on the wire. This is the seam the defect lived in,
// so it is the seam the test observes.
const installApplication = vi.fn(async () => ({ name: 'grafana-prod' }))
const previewApplication = vi.fn(async () => ({ blueprint: { name: 'bp-grafana', version: '1.0.6' }, manifests: [] }))
vi.mock('@/lib/catalog.api', () => ({
  installApplication: (...args: unknown[]) => installApplication(...(args as [])),
  previewApplication: (...args: unknown[]) => previewApplication(...(args as [])),
}))

// The RJSF form is not under test; expose a bare submit button that calls the
// page's own onSubmit with empty parameters.
vi.mock('@/widgets/install/InstallForm', () => ({
  InstallForm: ({
    onSubmit,
    onPreview,
  }: {
    onSubmit: (p: Record<string, unknown>) => void
    onPreview: (p: Record<string, unknown>) => void
  }) => (
    <>
      <button type="button" data-testid="stub-install-submit" onClick={() => onSubmit({})}>
        submit
      </button>
      <button type="button" data-testid="stub-install-preview" onClick={() => onPreview({})}>
        preview
      </button>
    </>
  ),
}))

vi.mock('@/widgets/code/CodeView', () => ({ CodeView: () => null }))

import { InstallPage } from './InstallPage'

/** The `placement` object of the most recent installApplication call. */
function submittedPlacement(): { mode: string; regions: string[] } {
  const call = installApplication.mock.calls.at(-1) as unknown as [string, { placement: { mode: string; regions: string[] } }]
  expect(call, 'installApplication was never called').toBeTruthy()
  return call[1].placement
}

describe('InstallPage placement regions (UAT row 60)', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })
  afterEach(() => cleanup())

  it('submits BOTH regions when the chosen topology places a standby', () => {
    render(<InstallPage preselectedBlueprint="bp-grafana" />)

    fireEvent.change(screen.getByTestId('install-page-placement'), {
      target: { value: 'active-hot-standby' },
    })
    fireEvent.change(screen.getByTestId('install-page-region'), {
      target: { value: 'me-east-215-a' },
    })
    // The control the page did not have. Its ABSENCE is the defect: there was
    // nowhere to name the standby region, so there could never be one.
    fireEvent.change(screen.getByTestId('install-page-standby-region'), {
      target: { value: 'me-east-215-b' },
    })

    fireEvent.click(screen.getByTestId('stub-install-submit'))

    const placement = submittedPlacement()
    expect(placement.mode).toBe('active-hot-standby')
    // regions[0] is the primary and regions[1..] the standbys — the ordering
    // placement.Resolve reads, so the ORDER is asserted, not just membership.
    expect(placement.regions).toEqual(['me-east-215-a', 'me-east-215-b'])
  })

  it('CONTROL — a singleton still submits exactly ONE region', () => {
    render(<InstallPage preselectedBlueprint="bp-grafana" />)

    fireEvent.change(screen.getByTestId('install-page-placement'), {
      target: { value: 'singleton' },
    })
    fireEvent.change(screen.getByTestId('install-page-region'), {
      target: { value: 'me-east-215-a' },
    })
    // The standby control must not even be rendered for a one-region posture.
    expect(screen.queryByTestId('install-page-standby-region')).toBeNull()

    fireEvent.click(screen.getByTestId('stub-install-submit'))

    const placement = submittedPlacement()
    expect(placement.mode).toBe('singleton')
    expect(placement.regions).toEqual(['me-east-215-a'])
  })

  it('refuses to submit a multi-region posture with no second region, and says why', () => {
    render(<InstallPage preselectedBlueprint="bp-grafana" />)

    fireEvent.change(screen.getByTestId('install-page-placement'), {
      target: { value: 'active-hot-standby' },
    })
    fireEvent.change(screen.getByTestId('install-page-region'), {
      target: { value: 'me-east-215-a' },
    })
    // Standby left blank — the exact state the page used to submit happily.

    expect(screen.getByTestId('install-page-placement-regions-validation').textContent)
      .toContain('needs a second region')

    fireEvent.click(screen.getByTestId('stub-install-submit'))
    expect(installApplication).not.toHaveBeenCalled()
    expect(screen.getByTestId('install-page-error').textContent).toContain('needs a second region')
  })

  it('refuses a standby region equal to the primary — two names for one place', () => {
    render(<InstallPage preselectedBlueprint="bp-grafana" />)

    fireEvent.change(screen.getByTestId('install-page-placement'), {
      target: { value: 'active-passive' },
    })
    fireEvent.change(screen.getByTestId('install-page-region'), {
      target: { value: 'me-east-215-a' },
    })
    fireEvent.change(screen.getByTestId('install-page-standby-region'), {
      target: { value: 'me-east-215-a' },
    })

    expect(screen.getByTestId('install-page-placement-regions-validation').textContent)
      .toContain('must differ')

    fireEvent.click(screen.getByTestId('stub-install-submit'))
    expect(installApplication).not.toHaveBeenCalled()
  })

  it('the PREVIEW door carries the same regions as the install door', () => {
    render(<InstallPage preselectedBlueprint="bp-grafana" />)

    fireEvent.change(screen.getByTestId('install-page-placement'), {
      target: { value: 'active-active' },
    })
    fireEvent.change(screen.getByTestId('install-page-region'), {
      target: { value: 'me-east-215-a' },
    })
    fireEvent.change(screen.getByTestId('install-page-standby-region'), {
      target: { value: 'me-east-215-b' },
    })

    fireEvent.click(screen.getByTestId('stub-install-preview'))

    const call = previewApplication.mock.calls.at(-1) as unknown as [
      string,
      { placement: { mode: string; regions: string[] } },
    ]
    expect(call, 'previewApplication was never called').toBeTruthy()
    expect(call[1].placement.regions).toEqual(['me-east-215-a', 'me-east-215-b'])
    // A preview that renders a different region set than the install would
    // submit is a preview of something else.
    fireEvent.click(screen.getByTestId('stub-install-submit'))
    expect(submittedPlacement().regions).toEqual(call[1].placement.regions)
  })
})
