/**
 * SovereignSidebar.sovereignty-160.test.tsx — UAT row 160 / #3379.
 *
 * THE CLAUSE: "Console nav + Settings sidebar expose a dedicated Sovereignty
 * anchor (#sovereignty) that scrolls to + highlights the Cluster-sovereignty
 * panel — the cutover trigger is a first-class surface."
 *
 * The hw293-2026-08-10 walk found HALF of it holding. The Settings table of
 * contents did expose `Sovereignty` -> `#sovereignty`, and clicking it really
 * scrolled (the panel's bounding-box top moved 3055px -> 896px). The CONSOLE
 * NAV did not: enumerating `sov-console-nav` on /dashboard returned exactly
 * eleven entries — Dashboard, Cloud, Apps, Catalog, Agenity, Jobs, Compliance,
 * Users, Organizations, Billing, Settings — and none of them was Sovereignty.
 * `/sovereignty` on the same origin rendered "Not Found". So the cutover
 * trigger — the whole of Pillar 5 — was reachable only by scrolling to the
 * bottom of Settings, which is the opposite of a first-class surface.
 *
 * THE CONTROL MATTERS MORE THAN THE ASSERTION HERE, because the failing half
 * is an ABSENCE and a zero is exactly what a broken selector also returns. The
 * walk handled this by finding `Sovereignty::#sovereignty` in the Settings TOC
 * with the SAME text-and-href enumeration that returned zero for the nav; this
 * file does the same thing in miniature. `enumerateNav()` is asserted to return
 * the known-good entries first, so a zero for Sovereignty is a real null.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createRoute,
  createMemoryHistory,
  Outlet,
} from '@tanstack/react-router'

const scopeMock = vi.fn()
vi.mock('@/shared/lib/useConsoleScope', () => ({
  useConsoleScope: () => scopeMock(),
}))
vi.mock('@/lib/console-ui.api', () => ({
  getSidebarEntries: async () => [],
}))
vi.mock('@/shared/lib/useResolvedDeploymentId', () => ({
  useResolvedDeploymentId: () => ({ deploymentId: '' }),
}))

import { SovereignSidebar } from './SovereignSidebar'

function renderSidebar(at = '/dashboard') {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const catchAll = createRoute({
    getParentRoute: () => rootRoute,
    path: '/$',
    component: () => <SovereignSidebar sovereignFQDN="hw293.omantel.biz" />,
  })
  const router = createRouter({
    routeTree: rootRoute.addChildren([catchAll]),
    history: createMemoryHistory({ initialEntries: [at] }),
  })
  return render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router as never} />
    </QueryClientProvider>,
  )
}

/** The walk's enumeration, in miniature: every nav entry as (text, href). */
function enumerateNav(): Array<{ text: string; href: string }> {
  const nav = screen.getByTestId('sov-console-nav')
  return Array.from(nav.querySelectorAll('a')).map((a) => ({
    text: (a.textContent ?? '').trim(),
    href: a.getAttribute('href') ?? '',
  }))
}

describe('SovereignSidebar — row 160: Sovereignty is a first-class console-nav surface', () => {
  beforeEach(() => {
    scopeMock.mockReset()
    scopeMock.mockReturnValue({ orgScoped: false, org: null, loading: false })
  })
  afterEach(() => cleanup())

  it('the enumeration itself works — control, so a zero below is a real null', async () => {
    renderSidebar()
    await screen.findByTestId('sov-console-nav-dashboard')
    const entries = enumerateNav()
    // Not a length assertion: adding a nav entry is legitimate and must not
    // break the control. What the control proves is that this selector CAN see
    // labels and hrefs, which is the only way a zero for Sovereignty means
    // anything.
    expect(entries.length).toBeGreaterThanOrEqual(11)
    expect(entries.map((e) => e.text)).toContain('Settings')
    expect(entries.find((e) => e.text === 'Settings')?.href).toBe('/settings')
    expect(entries.map((e) => e.text)).toContain('Jobs')
  })

  it('the console nav exposes a Sovereignty entry whose href carries the #sovereignty anchor', async () => {
    renderSidebar()
    await screen.findByTestId('sov-console-nav-dashboard')
    const entries = enumerateNav()
    const sovereignty = entries.filter((e) => e.text === 'Sovereignty')
    expect(
      sovereignty.length,
      `console nav exposed ${entries.length} entries and none was Sovereignty: ` +
        entries.map((e) => e.text).join(', '),
    ).toBe(1)
    // Assert on the VALUE of the href, not merely that an entry exists — an
    // entry pointing at /settings with no anchor lands the operator at the top
    // of an eleven-section page, which is the state the walk already recorded
    // as failing.
    expect(sovereignty[0].href).toBe('/settings#sovereignty')
  })

  it('has its own testid so a walker can address it directly', async () => {
    renderSidebar()
    expect(await screen.findByTestId('sov-console-nav-sovereignty')).toBeTruthy()
  })

  it('highlights on /settings#sovereignty and NOT on plain /settings', async () => {
    // The nav highlight is what makes a surface first-class rather than a
    // shortcut: arriving at the panel must light its own entry, and arriving at
    // Settings proper must not. Pinning BOTH directions is what stops the fix
    // degrading into "Sovereignty is always lit".
    const plain = renderSidebar('/settings')
    await screen.findByTestId('sov-console-nav-settings')
    expect(screen.getByTestId('sov-console-nav-settings').getAttribute('aria-current')).toBe('page')
    expect(screen.getByTestId('sov-console-nav-sovereignty').getAttribute('aria-current')).toBeNull()
    plain.unmount()

    renderSidebar('/settings#sovereignty')
    await screen.findByTestId('sov-console-nav-sovereignty')
    expect(screen.getByTestId('sov-console-nav-sovereignty').getAttribute('aria-current')).toBe('page')
    expect(screen.getByTestId('sov-console-nav-settings').getAttribute('aria-current')).toBeNull()
  })

  it('stays hidden on an Org-scoped console — the cutover is a sovereign-admin act', async () => {
    // #4110: an Org-scoped customer session sees only its own estate. The
    // cutover severs the whole Sovereign from the mothership, so exposing its
    // trigger to a customer would be a worse defect than the one being fixed.
    scopeMock.mockReturnValue({ orgScoped: true, org: 'walkone', loading: false })
    renderSidebar()
    await screen.findByTestId('sov-console-nav-apps')
    expect(screen.queryByTestId('sov-console-nav-sovereignty')).toBeNull()
  })
})
