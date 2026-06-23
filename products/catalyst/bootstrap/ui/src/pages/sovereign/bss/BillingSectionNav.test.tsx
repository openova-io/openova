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
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createRoute,
  createMemoryHistory,
} from '@tanstack/react-router'
import { BillingSectionNav } from './BillingSectionNav'
import { BILLING_SECTIONS } from './billing-sections'

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
  return render(<RouterProvider router={router as never} />)
}

afterEach(() => cleanup())

describe('BillingSectionNav (#4196)', () => {
  it('renders all three billing sections with /billing/* hrefs', async () => {
    renderAt('/billing/vouchers')
    expect(await screen.findByTestId('billing-section-nav')).toBeTruthy()
    expect(BILLING_SECTIONS.map((s) => s.to)).toEqual([
      '/billing/vouchers',
      '/billing/orders',
      '/billing/revenue',
    ])
    for (const s of BILLING_SECTIONS) {
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
