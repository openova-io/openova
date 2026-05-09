/**
 * KCUserPicker.test.tsx — DOM tests for the reusable Keycloak user
 * picker widget (EPIC-3 #1098 slice U2).
 *
 * The widget consumes TanStack Query so the tests stand up a fresh
 * QueryClient per case; the searchKCUsers fetch is mocked via
 * `vi.mock('@/pages/admin/rbac/rbac.api')` so the test runs without
 * a network.
 */

import { describe, it, expect, afterEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent, waitFor, act } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

import { KCUserPicker } from './KCUserPicker'
import type { KCUser } from '@/pages/admin/rbac/rbac.api'

vi.mock('@/pages/admin/rbac/rbac.api', async () => {
  const actual = await vi.importActual<typeof import('@/pages/admin/rbac/rbac.api')>(
    '@/pages/admin/rbac/rbac.api',
  )
  return {
    ...actual,
    searchKCUsers: vi.fn(),
  }
})

const { searchKCUsers } = await import('@/pages/admin/rbac/rbac.api')
const mockedSearch = searchKCUsers as unknown as ReturnType<typeof vi.fn>

afterEach(() => {
  cleanup()
  mockedSearch.mockReset()
})

function renderPicker(props: Partial<React.ComponentProps<typeof KCUserPicker>> = {}) {
  const onSelect = props.onSelect ?? vi.fn()
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: 0, gcTime: 0 } },
  })
  return {
    onSelect,
    ...render(
      <QueryClientProvider client={qc}>
        <KCUserPicker
          sovereignId="dep-1"
          onSelect={onSelect}
          noDebounce
          {...props}
        />
      </QueryClientProvider>,
    ),
  }
}

describe('KCUserPicker', () => {
  it('renders the input', () => {
    renderPicker()
    expect(screen.getByTestId('kc-user-picker-input')).toBeTruthy()
  })

  it('does NOT fire search until the query is at least 2 chars', async () => {
    mockedSearch.mockResolvedValue([])
    renderPicker()
    fireEvent.change(screen.getByTestId('kc-user-picker-input'), { target: { value: 'a' } })
    // Wait a tick to let the effect run.
    await act(async () => {
      await new Promise((r) => setTimeout(r, 10))
    })
    expect(mockedSearch).not.toHaveBeenCalled()
  })

  it('fires search and renders results', async () => {
    const users: KCUser[] = [
      { id: 'u1', username: 'alice', email: 'alice@acme.com', source: 'keycloak' },
      { id: 'u2', username: 'bob.fed', source: 'azure_ad_federated' },
    ]
    mockedSearch.mockResolvedValue(users)
    renderPicker()
    fireEvent.change(screen.getByTestId('kc-user-picker-input'), { target: { value: 'al' } })
    await waitFor(() => {
      expect(mockedSearch).toHaveBeenCalledWith('dep-1', 'al', 20)
    })
    await waitFor(() => {
      expect(screen.getByTestId('kc-user-picker-result-u1')).toBeTruthy()
    })
    expect(screen.getByTestId('kc-user-picker-result-u2')).toBeTruthy()
    // Federation badge for u2.
    expect(screen.getByTestId('kc-user-picker-badge-azure')).toBeTruthy()
  })

  it('selecting a result fires onSelect', async () => {
    const users: KCUser[] = [
      { id: 'u1', username: 'alice', email: 'alice@acme.com', source: 'keycloak' },
    ]
    mockedSearch.mockResolvedValue(users)
    const { onSelect } = renderPicker()
    fireEvent.change(screen.getByTestId('kc-user-picker-input'), { target: { value: 'al' } })
    await waitFor(() => {
      expect(screen.getByTestId('kc-user-picker-result-u1')).toBeTruthy()
    })
    fireEvent.click(screen.getByTestId('kc-user-picker-result-u1'))
    expect(onSelect).toHaveBeenCalledWith(users[0])
  })

  it('renders empty state for zero results', async () => {
    mockedSearch.mockResolvedValue([])
    renderPicker()
    fireEvent.change(screen.getByTestId('kc-user-picker-input'), { target: { value: 'zz' } })
    await waitFor(() => {
      expect(screen.getByTestId('kc-user-picker-empty')).toBeTruthy()
    })
  })
})
