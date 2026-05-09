/**
 * CuratePage.test.tsx — unit tests for EPIC-2 slice P (#1097)
 * Curate (sovereign-admin) page.
 */
import { describe, it, expect, afterEach, vi } from 'vitest'
import { render, screen, cleanup } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

vi.mock('@/shared/lib/useResolvedDeploymentId', () => ({
  useResolvedDeploymentId: () => ({ deploymentId: 'dep-1', isLoading: false }),
}))

import { CuratePage } from './CuratePage'

function withQuery(node: React.ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return <QueryClientProvider client={qc}>{node}</QueryClientProvider>
}

afterEach(() => cleanup())

describe('CuratePage', () => {
  it('shows empty state when initialCuratable is empty', () => {
    render(withQuery(<CuratePage disableNetwork initialCuratable={[]} initialOrgs={['acme']} />))
    expect(screen.getByTestId('curate-page-empty')).toBeTruthy()
  })

  it('lists candidates from initialCuratable', () => {
    render(
      withQuery(
        <CuratePage
          disableNetwork
          initialOrgs={['acme']}
          initialCuratable={[
            { org: 'acme', name: 'bp-foo', version: '1.0.0', title: 'Foo' },
            { org: 'acme', name: 'bp-bar', version: '2.0.0', title: 'Bar' },
          ]}
        />,
      ),
    )
    expect(screen.getByTestId('curate-page-row-acme-bp-foo')).toBeTruthy()
    expect(screen.getByTestId('curate-page-row-acme-bp-bar')).toBeTruthy()
  })
})
