/**
 * InstallPage.placement.test.tsx — #3375 §3(d) lock-in.
 *
 * The install-flow placement picker must offer the SAME canonical 4-class
 * set the post-create TopologyEditor offers (single-region / active-active
 * / active-hotstandby / active-passive). It previously hardcoded only the
 * first three, so a Blueprint that supports `active-passive` could be
 * selected on the editor but never AT INSTALL. This test asserts all four
 * options render + are selectable.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent } from '@testing-library/react'

// Mock the deployment-id resolver + catalog hooks so the page renders the
// form scaffold (which carries the placement <select>) without a router
// or network.
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

// useNavigate is only invoked on status-modal dismiss; stub it so the
// page mounts cleanly.
vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => () => Promise.resolve(undefined),
}))

// InstallForm renders the RJSF form; stub it to a no-op so the test
// focuses on the placement scaffold the page owns.
vi.mock('@/widgets/install/InstallForm', () => ({
  InstallForm: () => null,
}))

import { InstallPage } from './InstallPage'

describe('InstallPage placement picker (#3375 §3d)', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })
  afterEach(() => cleanup())

  it('offers all four canonical placement modes including active-passive', () => {
    render(<InstallPage preselectedBlueprint="bp-grafana" />)

    const select = screen.getByTestId('install-page-placement') as HTMLSelectElement
    const optionValues = Array.from(select.options).map((o) => o.value)

    expect(optionValues).toContain('single-region')
    expect(optionValues).toContain('active-active')
    expect(optionValues).toContain('active-hotstandby')
    // The #3375 §3(d) fix — previously absent.
    expect(optionValues).toContain('active-passive')
  })

  it('lets active-passive be selected', () => {
    render(<InstallPage preselectedBlueprint="bp-grafana" />)

    const select = screen.getByTestId('install-page-placement') as HTMLSelectElement
    fireEvent.change(select, { target: { value: 'active-passive' } })
    expect(select.value).toBe('active-passive')
  })
})
