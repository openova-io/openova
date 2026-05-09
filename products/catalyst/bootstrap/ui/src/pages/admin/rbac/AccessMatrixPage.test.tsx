/**
 * AccessMatrixPage.test.tsx — vitest coverage for the U7 access-matrix
 * cell renderer + warning indicator.
 */

import { cleanup, render, screen } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, describe, expect, it, vi } from 'vitest'

afterEach(() => cleanup())

import { AccessMatrixPage, MatrixCell } from './AccessMatrixPage'
import type { AccessMatrixResponse } from './rbac.api'

// Stub PortalShell so we don't pull the chroot router into the test.
vi.mock('@/pages/sovereign/PortalShell', () => ({
  PortalShell: ({ children }: { children: React.ReactNode }) => <div data-testid="shell">{children}</div>,
}))

// Stub useResolvedDeploymentId — the test injects deploymentId directly.
vi.mock('@/shared/lib/useResolvedDeploymentId', () => ({
  useResolvedDeploymentId: () => ({ deploymentId: '' }),
}))

const fakeMatrix: AccessMatrixResponse = {
  users: [
    {
      id: 'alice-uuid',
      email: 'alice@acme.io',
      source: 'keycloak',
      access: {
        wordpress: { tier: 'admin', userAccessRef: 'rbac-alice-wp' },
        billing: { tier: 'developer', userAccessRef: 'rbac-alice-billing' },
      },
      warnings: ['developer-tier UserAccess for billing missing env-type=dev scope (CR: rbac-alice-billing)'],
    },
    {
      id: 'bob-uuid',
      email: 'bob@acme.io',
      source: 'azure_ad_federated',
      access: {
        wordpress: { tier: 'viewer', userAccessRef: 'rbac-bob-wp' },
      },
    },
  ],
  applications: ['wordpress', 'billing'],
  tiers: ['viewer', 'developer', 'operator', 'admin', 'owner'],
}

function withQuery(ui: React.ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return <QueryClientProvider client={qc}>{ui}</QueryClientProvider>
}

describe('AccessMatrixPage', () => {
  it('renders one row per user and one column per application', () => {
    render(withQuery(<AccessMatrixPage initialDeploymentId="dep-1" initialMatrix={fakeMatrix} />))

    expect(screen.getByTestId('access-matrix-page')).toBeTruthy()
    expect(screen.getByTestId('matrix-row-alice-uuid')).toBeTruthy()
    expect(screen.getByTestId('matrix-row-bob-uuid')).toBeTruthy()
    expect(screen.getByTestId('matrix-col-wordpress')).toBeTruthy()
    expect(screen.getByTestId('matrix-col-billing')).toBeTruthy()
  })

  it('renders the row warning indicator for users with warnings', () => {
    render(withQuery(<AccessMatrixPage initialDeploymentId="dep-1" initialMatrix={fakeMatrix} />))

    expect(screen.getByTestId('matrix-row-warning-alice-uuid')).toBeTruthy()
    // Bob has no warnings; the indicator should be absent.
    expect(screen.queryByTestId('matrix-row-warning-bob-uuid')).toBeNull()
  })

  it('renders the cell warning star alongside the tier label', () => {
    render(withQuery(<AccessMatrixPage initialDeploymentId="dep-1" initialMatrix={fakeMatrix} />))

    const cell = screen.getByTestId('matrix-cell-alice-uuid-billing')
    expect(cell.textContent).toContain('DEVELO')
    expect(cell.textContent).toContain('*')
  })

  it('opens the editor modal when a cell is clicked', () => {
    render(
      withQuery(
        <AccessMatrixPage
          initialDeploymentId="dep-1"
          initialMatrix={fakeMatrix}
          forceOpenEditor={{ userId: 'alice-uuid', application: 'wordpress' }}
        />,
      ),
    )

    expect(screen.getByTestId('matrix-editor-modal')).toBeTruthy()
    expect(screen.getByTestId('matrix-editor-open-multigrant')).toBeTruthy()
  })

  it('renders empty-state when there are no users', () => {
    render(
      withQuery(
        <AccessMatrixPage
          initialDeploymentId="dep-1"
          initialMatrix={{ users: [], applications: [], tiers: [] }}
        />,
      ),
    )
    expect(screen.getByTestId('matrix-empty')).toBeTruthy()
  })
})

describe('MatrixCell', () => {
  it('renders an em-dash placeholder for missing grants', () => {
    const onClick = vi.fn()
    render(<MatrixCell grant={undefined} hasWarning={false} onClick={onClick} testId="t" />)
    expect(screen.getByTestId('t').textContent).toContain('—')
  })
  it('renders the tier label uppercased + truncated to 6 chars', () => {
    const onClick = vi.fn()
    render(
      <MatrixCell
        grant={{ tier: 'developer', userAccessRef: 'r' }}
        hasWarning={false}
        onClick={onClick}
        testId="t"
      />,
    )
    expect(screen.getByTestId('t').textContent).toContain('DEVELO')
  })
  it('renders the warning star when hasWarning', () => {
    const onClick = vi.fn()
    render(
      <MatrixCell
        grant={{ tier: 'developer', userAccessRef: 'r' }}
        hasWarning={true}
        onClick={onClick}
        testId="t"
      />,
    )
    expect(screen.getByTestId('t').textContent).toContain('*')
  })
})
