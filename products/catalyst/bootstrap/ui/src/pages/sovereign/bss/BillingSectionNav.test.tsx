/**
 * BillingSectionNav.test.tsx — issue #4196.
 *
 * The Billing menu's in-page section switcher renders the three native
 * sections (Vouchers · Orders · Revenue) and highlights the active one
 * from the URL. Vouchers is the default (it covers both /billing and
 * /billing/vouchers).
 */

import { describe, it, expect, afterEach } from 'vitest'
import { render, screen, cleanup } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createRoute,
  createMemoryHistory,
} from '@tanstack/react-router'
import { BillingSectionNav } from './BillingSectionNav'
import { BILLING_SECTIONS, billingSectionsFor } from './billing-sections'

function renderAt(path: string) {
  const rootRoute = createRootRoute({ component: () => <BillingSectionNav /> })
  // Register the billing leaf paths so the Links resolve cleanly.
  const leaves = ['/billing', '/billing/vouchers', '/billing/orders', '/billing/revenue'].map(
    (p) =>
      createRoute({
        getParentRoute: () => rootRoute,
        path: p,
        component: () => <BillingSectionNav />,
      }),
  )
  const router = createRouter({
    routeTree: rootRoute.addChildren(leaves),
    history: createMemoryHistory({ initialEntries: [path] }),
  })
  // The nav reads the installed-Blueprint set (ADR-0014 D5) through
  // react-query, exactly as it does in production where main.tsx wraps the
  // whole console in a QueryClientProvider. Retries off + a throwaway client
  // per render so a failing fetch resolves immediately to "unknown", which is
  // the degrade-to-native-three path these tests exercise.
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router as never} />
    </QueryClientProvider>,
  )
}

afterEach(() => cleanup())

describe('BillingSectionNav (#4196)', () => {
  it('renders all three billing sections with /billing/* hrefs', async () => {
    renderAt('/billing/vouchers')
    expect(await screen.findByTestId('billing-section-nav')).toBeTruthy()
    // The NATIVE sections keep their /billing/* hrefs and their order.
    // BILLING_SECTIONS also carries application sections (ADR-0014 D5), which
    // render only when their Blueprint is installed — billingSectionsFor([])
    // is exactly the native set, which is what this test is about.
    const native = billingSectionsFor([])
    expect(native.map((s) => s.to)).toEqual([
      '/billing/vouchers',
      '/billing/orders',
      '/billing/revenue',
    ])
    for (const s of native) {
      const link = screen.getByTestId(`billing-section-nav-${s.id}`) as HTMLAnchorElement
      expect(link.getAttribute('href')).toBe(s.to)
    }
  })

  it('marks Vouchers active at /billing/vouchers', async () => {
    renderAt('/billing/vouchers')
    const v = await screen.findByTestId('billing-section-nav-vouchers')
    expect(v.getAttribute('aria-current')).toBe('page')
    expect(screen.getByTestId('billing-section-nav-orders').getAttribute('aria-current')).toBeNull()
  })

  it('marks Orders active at /billing/orders', async () => {
    renderAt('/billing/orders')
    const o = await screen.findByTestId('billing-section-nav-orders')
    expect(o.getAttribute('aria-current')).toBe('page')
  })

  it('marks Revenue active at /billing/revenue', async () => {
    renderAt('/billing/revenue')
    const r = await screen.findByTestId('billing-section-nav-revenue')
    expect(r.getAttribute('aria-current')).toBe('page')
  })
})

/* ──────────────────────────────────────────────────────────────────
 * ADR-0014 D5 — Billing links the Sovereign chargeback placement
 *
 * D5 requires the Sovereign BSS placement of bp-chargeback to be "linked from
 * console.<fqdn>/billing". Walked on hw307 2026-09-03: chargeback was
 * installed via kit slot 13f, Ready, serving its own UI, and reachable from
 * the sidebar rail — but NOTHING in the Billing menu pointed at it. These pin
 * the link AND the condition on it, because a tab that dead-ends on a
 * Sovereign without the Blueprint is worse than no tab.
 * ────────────────────────────────────────────────────────────────── */
describe('billingSectionsFor — ADR-0014 D5 application section', () => {
  it('offers Chargeback only when the Blueprint is installed', () => {
    const withIt = billingSectionsFor(['bp-chargeback']).map((s) => s.id)
    expect(withIt).toContain('chargeback')
    const withoutIt = billingSectionsFor([]).map((s) => s.id)
    expect(withoutIt).not.toContain('chargeback')
  })

  it('degrades to the three native sections when the installed set is unknown', () => {
    // The sidebar-entries read failed or has not resolved. Rendering the
    // native three is the pre-ADR-0014 behaviour; inventing a Chargeback tab
    // would send the operator nowhere.
    expect(billingSectionsFor(undefined).map((s) => s.id)).toEqual([
      'vouchers',
      'orders',
      'revenue',
    ])
  })

  it('never hides a native section, whatever the installed set says', () => {
    for (const set of [[], ['bp-chargeback'], ['bp-agenity']]) {
      const ids = billingSectionsFor(set).map((s) => s.id)
      expect(ids).toEqual(expect.arrayContaining(['vouchers', 'orders', 'revenue']))
    }
  })

  it('points Billing at the Blueprint own declared route, not a parallel one', () => {
    // If these drift, Billing and the sidebar rail send the operator to two
    // different surfaces for the same application.
    const cb = billingSectionsFor(['bp-chargeback']).find((s) => s.id === 'chargeback')
    expect(cb?.to).toBe('/apps/bp-chargeback/dashboard')
    expect(cb?.blueprint).toBe('bp-chargeback')
  })
})
