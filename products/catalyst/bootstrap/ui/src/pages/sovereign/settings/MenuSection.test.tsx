/**
 * MenuSection.test.tsx — EPIC #6723 lane C: Settings → Menu.
 *
 * Drives the section against a mocked console-ui API:
 *   - the table lists every merged entry with its source badge, toggle,
 *     label / route / parent / order controls (data-testid on each);
 *   - Save is disabled until something changes and is valid; the PUT body
 *     carries ONLY rows that differ from their defaults, and only the
 *     fields that differ (a stored mapping, not a dump of the table);
 *   - the state machine goes idle → saving → applied and invalidates the
 *     sidebar query so the rail re-renders;
 *   - client-side validation mirrors the server (label ≤ 40, route shape,
 *     order 0–100) and a server 400 lists its problems;
 *   - Reset restores a row to its Blueprint / Application default;
 *   - an Org-scoped console gets the fixed-menu note instead of the table.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent, waitFor, within } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { SidebarEntry, SidebarOverride } from '@/lib/console-ui.api'

// vi.mock factories run when the mocked module is first imported, which is
// before this file's body executes — every value a factory touches has to
// come from vi.hoisted().
const { scopeMock, getSidebarMenu, getSidebarOverrides, putSidebarOverrides, SidebarApiError } = vi.hoisted(() => {
  class SidebarApiError extends Error {
    status: number
    problems: string[]
    constructor(status: number, message: string, problems: string[] = []) {
      super(message)
      this.name = 'SidebarApiError'
      this.status = status
      this.problems = problems
    }
  }
  return {
    scopeMock: vi.fn(),
    getSidebarMenu: vi.fn(),
    getSidebarOverrides: vi.fn(),
    putSidebarOverrides: vi.fn(),
    SidebarApiError,
  }
})

vi.mock('@/shared/lib/useConsoleScope', () => ({
  useConsoleScope: () => scopeMock(),
}))
vi.mock('@/shared/lib/useResolvedDeploymentId', () => ({
  useResolvedDeploymentId: () => ({ deploymentId: 'hw310.omani.works' }),
}))
vi.mock('@/shared/lib/detectMode', () => ({
  DETECTED_MODE: { mode: 'sovereign', sovereignFQDN: 'hw310.omani.works' },
}))
vi.mock('@/lib/console-ui.api', () => ({
  getSidebarMenu,
  getSidebarOverrides,
  putSidebarOverrides,
  SidebarApiError,
}))

import { MenuSection } from './MenuSection'
import { overridesFromRows, rowProblem } from './menuMapping'

const PARENTS = ['dashboard', 'cloud', 'apps', 'catalog', 'jobs', 'compliance', 'users', 'organizations', 'billing']

function entries(): SidebarEntry[] {
  return [
    {
      id: 'bp-agenity',
      label: 'Agenity',
      route: '/apps/bp-agenity/dashboard',
      order: 40,
      source: 'blueprint',
      enabled: true,
      defaultLabel: 'Agenity',
      defaultRoute: '/apps/bp-agenity/dashboard',
      defaultOrder: 40,
      defaultEnabled: true,
    },
    {
      id: 'app:grafana',
      label: 'grafana',
      route: '/app/grafana',
      order: 50,
      source: 'application',
      enabled: false,
      defaultLabel: 'grafana',
      defaultRoute: '/app/grafana',
      defaultOrder: 50,
      defaultEnabled: false,
    },
  ]
}

let qc: QueryClient

function renderSection() {
  qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  return render(
    <QueryClientProvider client={qc}>
      <MenuSection />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  scopeMock.mockReset()
  scopeMock.mockReturnValue({ orgScoped: false, org: null, loading: false })
  getSidebarMenu.mockReset()
  getSidebarMenu.mockResolvedValue({ entries: entries(), parents: PARENTS })
  getSidebarOverrides.mockReset()
  getSidebarOverrides.mockResolvedValue({
    entries: [],
    parents: PARENTS,
    allowedHosts: ['hw310.omani.works', 'omani.homes'],
    namespace: 'catalyst-system',
    name: 'console-ui-sidebar',
  })
  putSidebarOverrides.mockReset()
  putSidebarOverrides.mockResolvedValue({
    entries: [],
    appliedAt: '2026-08-31T10:00:00Z',
    namespace: 'catalyst-system',
    name: 'console-ui-sidebar',
  })
})

afterEach(() => cleanup())

describe('MenuSection — table', () => {
  it('lists every merged entry with source, toggle, label, route, parent and order controls', async () => {
    renderSection()
    expect(await screen.findByTestId('settings-menu-table')).toBeTruthy()
    for (const id of ['bp-agenity', 'app:grafana']) {
      const row = screen.getByTestId(`settings-menu-row-${id}`)
      expect(within(row).getByTestId(`settings-menu-enabled-${id}`)).toBeTruthy()
      expect(within(row).getByTestId(`settings-menu-label-${id}`)).toBeTruthy()
      expect(within(row).getByTestId(`settings-menu-route-${id}`)).toBeTruthy()
      expect(within(row).getByTestId(`settings-menu-parent-${id}`)).toBeTruthy()
      expect(within(row).getByTestId(`settings-menu-order-${id}`)).toBeTruthy()
      expect(within(row).getByTestId(`settings-menu-reset-${id}`)).toBeTruthy()
    }
    expect(screen.getByTestId('settings-menu-source-bp-agenity').textContent).toBe('blueprint')
    expect(screen.getByTestId('settings-menu-source-app:grafana').textContent).toBe('application')
    expect(screen.getByTestId('settings-menu-row-bp-agenity').getAttribute('data-enabled')).toBe('true')
    expect(screen.getByTestId('settings-menu-row-app:grafana').getAttribute('data-enabled')).toBe('false')
    // Parent dropdown = Top level + every mappable FLAT_NAV item.
    const parentSelect = screen.getByTestId('settings-menu-parent-app:grafana') as HTMLSelectElement
    const values = Array.from(parentSelect.options).map((o) => o.value)
    expect(values).toEqual(['', ...PARENTS])
    // Allowed hosts are surfaced for the https:// hint.
    expect(screen.getByTestId('settings-menu-allowed-hosts').textContent).toContain('omani.homes')
    // Nothing changed yet → idle, Save disabled.
    expect(screen.getByTestId('settings-menu-status-idle')).toBeTruthy()
    expect((screen.getByTestId('settings-menu-save') as HTMLButtonElement).disabled).toBe(true)
  })

  it('enables an Application, nests it under Resources, saves ONLY the changed rows and applies', async () => {
    renderSection()
    await screen.findByTestId('settings-menu-table')

    fireEvent.click(screen.getByTestId('settings-menu-enabled-app:grafana'))
    fireEvent.change(screen.getByTestId('settings-menu-parent-app:grafana'), { target: { value: 'cloud' } })
    fireEvent.change(screen.getByTestId('settings-menu-label-app:grafana'), { target: { value: 'Observability' } })
    fireEvent.change(screen.getByTestId('settings-menu-order-app:grafana'), { target: { value: '5' } })
    fireEvent.change(screen.getByTestId('settings-menu-route-app:grafana'), {
      target: { value: 'https://grafana.hw310.omani.works/' },
    })
    expect(screen.getByTestId('settings-menu-row-app:grafana').getAttribute('data-overridden')).toBe('true')

    const save = screen.getByTestId('settings-menu-save') as HTMLButtonElement
    expect(save.disabled).toBe(false)
    const invalidate = vi.spyOn(qc, 'invalidateQueries')
    fireEvent.click(save)

    await waitFor(() => expect(screen.getByTestId('settings-menu-status-applied')).toBeTruthy())
    expect(putSidebarOverrides).toHaveBeenCalledTimes(1)
    const [sovereignId, body] = putSidebarOverrides.mock.calls[0] as [string, SidebarOverride[]]
    expect(sovereignId).toBe('hw310.omani.works')
    // Agenity is untouched → not in the body; grafana carries only what changed.
    expect(body).toEqual([
      { id: 'app:grafana', enabled: true, label: 'Observability', route: 'https://grafana.hw310.omani.works/', order: 5, parent: 'cloud' },
    ])
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ['console-ui-sidebar-entries'] })
    // The merged view is re-read after save.
    await waitFor(() => expect(getSidebarMenu).toHaveBeenCalledTimes(2))
  })

  it('disabling a Blueprint entry stores an enabled=false override and nothing else', async () => {
    renderSection()
    await screen.findByTestId('settings-menu-table')
    fireEvent.click(screen.getByTestId('settings-menu-enabled-bp-agenity'))
    fireEvent.click(screen.getByTestId('settings-menu-save'))
    await waitFor(() => expect(putSidebarOverrides).toHaveBeenCalledTimes(1))
    expect(putSidebarOverrides.mock.calls[0][1]).toEqual([{ id: 'bp-agenity', enabled: false }])
  })

  it('validates like the server: long label, bad route, out-of-range order block Save', async () => {
    renderSection()
    await screen.findByTestId('settings-menu-table')
    const save = screen.getByTestId('settings-menu-save') as HTMLButtonElement

    fireEvent.change(screen.getByTestId('settings-menu-label-bp-agenity'), {
      target: { value: 'x'.repeat(41) },
    })
    expect(screen.getByTestId('settings-menu-row-error-bp-agenity').textContent).toContain('40 characters')
    expect(save.disabled).toBe(true)
    fireEvent.change(screen.getByTestId('settings-menu-label-bp-agenity'), { target: { value: 'Agents' } })
    expect(screen.queryByTestId('settings-menu-row-error-bp-agenity')).toBeNull()
    expect(save.disabled).toBe(false)

    fireEvent.change(screen.getByTestId('settings-menu-route-bp-agenity'), { target: { value: 'https://evil.example/' } })
    expect(screen.getByTestId('settings-menu-row-error-bp-agenity').textContent).toContain('parent domains')
    expect(save.disabled).toBe(true)
    fireEvent.change(screen.getByTestId('settings-menu-route-bp-agenity'), { target: { value: 'apps/no-slash' } })
    expect(screen.getByTestId('settings-menu-row-error-bp-agenity').textContent).toContain('must start with /')
    fireEvent.change(screen.getByTestId('settings-menu-route-bp-agenity'), { target: { value: '/apps/bp-agenity/dashboard' } })
    expect(screen.queryByTestId('settings-menu-row-error-bp-agenity')).toBeNull()

    fireEvent.change(screen.getByTestId('settings-menu-order-bp-agenity'), { target: { value: '101' } })
    expect(screen.getByTestId('settings-menu-row-error-bp-agenity').textContent).toContain('between 0 and 100')
    expect(save.disabled).toBe(true)
    fireEvent.change(screen.getByTestId('settings-menu-order-bp-agenity'), { target: { value: '0' } })
    expect(screen.queryByTestId('settings-menu-row-error-bp-agenity')).toBeNull()
    expect(save.disabled).toBe(false)
  })

  it('surfaces a server 400 with its problem list', async () => {
    putSidebarOverrides.mockRejectedValueOnce(
      new SidebarApiError(400, 'invalid-sidebar-overrides', ['entries[0](bp-agenity).order: must be between 0 and 100 (got 500)']),
    )
    renderSection()
    await screen.findByTestId('settings-menu-table')
    fireEvent.click(screen.getByTestId('settings-menu-enabled-bp-agenity'))
    fireEvent.click(screen.getByTestId('settings-menu-save'))
    const err = await screen.findByTestId('settings-menu-status-error')
    expect(err.textContent).toContain('Save failed (400)')
    expect(screen.getByTestId('settings-menu-status-problems').textContent).toContain('between 0 and 100')
  })

  it('Reset restores a row to its default and Discard drops every pending change', async () => {
    renderSection()
    await screen.findByTestId('settings-menu-table')
    const reset = screen.getByTestId('settings-menu-reset-app:grafana') as HTMLButtonElement
    expect(reset.disabled).toBe(true)
    fireEvent.click(screen.getByTestId('settings-menu-enabled-app:grafana'))
    fireEvent.change(screen.getByTestId('settings-menu-parent-app:grafana'), { target: { value: 'apps' } })
    expect(reset.disabled).toBe(false)
    fireEvent.click(reset)
    expect(screen.getByTestId('settings-menu-row-app:grafana').getAttribute('data-enabled')).toBe('false')
    expect((screen.getByTestId('settings-menu-parent-app:grafana') as HTMLSelectElement).value).toBe('')

    fireEvent.change(screen.getByTestId('settings-menu-label-bp-agenity'), { target: { value: 'Agents' } })
    expect((screen.getByTestId('settings-menu-discard') as HTMLButtonElement).disabled).toBe(false)
    fireEvent.click(screen.getByTestId('settings-menu-discard'))
    expect((screen.getByTestId('settings-menu-label-bp-agenity') as HTMLInputElement).value).toBe('Agenity')
    expect((screen.getByTestId('settings-menu-save') as HTMLButtonElement).disabled).toBe(true)
  })

  it('filters rows by id, label or route', async () => {
    renderSection()
    await screen.findByTestId('settings-menu-table')
    fireEvent.change(screen.getByTestId('settings-menu-filter'), { target: { value: 'graf' } })
    expect(screen.queryByTestId('settings-menu-row-bp-agenity')).toBeNull()
    expect(screen.getByTestId('settings-menu-row-app:grafana')).toBeTruthy()
    fireEvent.change(screen.getByTestId('settings-menu-filter'), { target: { value: 'zzz' } })
    expect(screen.getByTestId('settings-menu-filter-empty')).toBeTruthy()
  })

  it('goes read-only when the overrides store refuses the session (403)', async () => {
    getSidebarOverrides.mockRejectedValueOnce(new SidebarApiError(403, 'the Sovereign console menu requires tier-admin or higher'))
    renderSection()
    expect((await screen.findByTestId('settings-menu-readonly')).textContent).toContain('tier-admin')
    expect((screen.getByTestId('settings-menu-enabled-bp-agenity') as HTMLButtonElement).disabled).toBe(true)
    expect((screen.getByTestId('settings-menu-save') as HTMLButtonElement).disabled).toBe(true)
  })

  it('shows the empty state when the Sovereign has no candidate entries', async () => {
    getSidebarMenu.mockResolvedValueOnce({ entries: [], parents: PARENTS })
    renderSection()
    expect(await screen.findByTestId('settings-menu-empty')).toBeTruthy()
    expect(screen.queryByTestId('settings-menu-table')).toBeNull()
  })

  it('an Org-scoped console gets the fixed-menu note, not the table', async () => {
    scopeMock.mockReturnValue({ orgScoped: true, org: 'acme', loading: false })
    renderSection()
    expect(await screen.findByTestId('settings-menu-org-scoped')).toBeTruthy()
    expect(screen.queryByTestId('settings-menu-table')).toBeNull()
  })
})

describe('MenuSection — pure helpers', () => {
  const base = {
    id: 'app:x',
    source: 'application' as const,
    enabled: false,
    label: 'x',
    route: '/app/x',
    order: 50,
    parent: '',
    defaultEnabled: false,
    defaultLabel: 'x',
    defaultRoute: '/app/x',
    defaultOrder: 50,
  }

  it('overridesFromRows emits only rows and fields that differ from the defaults', () => {
    expect(overridesFromRows([base])).toEqual([])
    expect(overridesFromRows([{ ...base, enabled: true }])).toEqual([{ id: 'app:x', enabled: true }])
    expect(overridesFromRows([{ ...base, label: '  Renamed  ' }])).toEqual([{ id: 'app:x', enabled: false, label: 'Renamed' }])
    expect(overridesFromRows([{ ...base, order: 0 }])).toEqual([{ id: 'app:x', enabled: false, order: 0 }])
    expect(overridesFromRows([{ ...base, parent: 'cloud' }])).toEqual([{ id: 'app:x', enabled: false, parent: 'cloud' }])
  })

  it('rowProblem mirrors the server rules', () => {
    const hosts = ['hw310.omani.works']
    expect(rowProblem(base, hosts)).toBe('')
    expect(rowProblem({ ...base, label: '' }, hosts)).toContain('required')
    expect(rowProblem({ ...base, label: 'y'.repeat(41) }, hosts)).toContain('40')
    expect(rowProblem({ ...base, route: '' }, hosts)).toContain('required')
    expect(rowProblem({ ...base, route: '//x' }, hosts)).toContain('//')
    expect(rowProblem({ ...base, route: '/a b' }, hosts)).toContain('spaces')
    expect(rowProblem({ ...base, route: 'http://a.hw310.omani.works' }, hosts)).toContain('must start with /')
    expect(rowProblem({ ...base, route: 'https://a.hw310.omani.works/x' }, hosts)).toBe('')
    expect(rowProblem({ ...base, route: 'https://a.example/x' }, hosts)).toContain('parent domains')
    // Unknown host set (older API) → the server is the judge; no client refusal.
    expect(rowProblem({ ...base, route: 'https://a.example/x' }, [])).toBe('')
    expect(rowProblem({ ...base, order: 101 }, hosts)).toContain('between 0 and 100')
    expect(rowProblem({ ...base, order: Number.NaN }, hosts)).toContain('whole number')
    expect(rowProblem({ ...base, order: 100 }, hosts)).toBe('')
  })
})
