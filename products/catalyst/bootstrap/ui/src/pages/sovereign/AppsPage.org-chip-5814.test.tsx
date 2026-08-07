import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import {
  Outlet,
  RouterProvider,
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
} from '@tanstack/react-router'

import { AppCard } from './AppsPage'
import type { ApplicationDescriptor } from './applicationCatalog'

/**
 * #5814 (UAT row 15) — a customer-launched app card must be attributed to its
 * Organization on the sovereign grid.
 *
 * The walk on hw292 read the whole page and recorded: 46 cards, all
 * BOOTSTRAP-badged, "no Org grouping, no scope filter", and the string `uatco`
 * absent from document.innerText. The backend half of the fix puts the Org on
 * the wire; this half proves it reaches the operator's eyes.
 *
 * WHY THE SUPPRESSION CASE IS TESTED AS HARD AS THE RENDER CASE. The wire
 * carries an org for EVERY instance, spine included — the spine's is the
 * Sovereign self-org, identical across ~40 cards. Painting it would be
 * technically truthful and practically useless: the one chip that carries
 * information would be lost in forty that do not. So "bootstrap rows show no
 * chip" is a real product requirement, not an implementation detail, and it is
 * the half most likely to be "simplified away" by someone who reads the render
 * condition as a redundant guard.
 */

function descriptor(over: Partial<ApplicationDescriptor> = {}): ApplicationDescriptor {
  return {
    id: 'uatco-agenity',
    bareId: 'agenity',
    title: 'uatco-agenity',
    description: 'Agenity instance',
    familyId: 'platform',
    familyName: 'Agenity',
    tier: 'mandatory',
    logoUrl: null,
    dependencies: [],
    bootstrapKit: false,
    ...over,
  }
}

/**
 * AppCard calls useParams() to build its detail Link, so it only mounts under
 * a router. Same harness shape as AppsPage.open-button.test.tsx.
 */
function renderCard(props: Partial<Parameters<typeof AppCard>[0]> & { app: ApplicationDescriptor }) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const homeRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/',
    component: () => (
      <AppCard
        status="installed"
        isCatalog={false}
        isService={false}
        environment="prod"
        marketplacePublished={null}
        slug="agenity"
        {...props}
      />
    ),
  })
  const appRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/app/$componentId',
    component: () => <div data-testid="app-detail-target" />,
  })
  const router = createRouter({
    routeTree: rootRoute.addChildren([homeRoute, appRoute]),
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  return render(<RouterProvider router={router} />)
}

afterEach(cleanup)

describe('#5814 — Org attribution chip on the sovereign apps grid', () => {
  it('renders the Org slug on a customer-launched instance card', async () => {
    renderCard({ app: descriptor(), org: 'uatco' })

    // findBy*, not getBy*: TanStack Router mounts the route component on a
    // microtask, so a synchronous query sees an empty body and every
    // "element absent" assertion in this file would pass for that reason
    // rather than the intended one.
    const chip = await screen.findByTestId('sov-app-org-uatco-agenity')
    expect(chip.getAttribute('data-org')).toBe('uatco')
    // Assert on the rendered TEXT, not just the attribute: the walk's own
    // method was a case-insensitive scan of document.innerText for "uatco",
    // so the thing under test is what that scan would have found.
    expect(chip.textContent).toContain('uatco')
  })

  it('renders no chip when the Application declares no organizationRef', async () => {
    renderCard({ app: descriptor({ id: 'legacy-app' }) })
    // Wait for the card itself first — otherwise this asserts on an empty
    // body and passes no matter what the chip logic does.
    await screen.findByText('FREE')
    expect(screen.queryByTestId('sov-app-org-legacy-app')).toBeNull()
  })

  it('renders no chip for a bootstrap-kit row even when an org is supplied', async () => {
    // The call site passes `undefined` for bootstrap rows, but a future
    // refactor that forwards `inst.org` unconditionally must still be caught
    // by SOMETHING — so this pins the behaviour at the boundary the operator
    // actually sees rather than at the call site's ternary.
    renderCard({ app: descriptor({ id: 'spine-openbao', bootstrapKit: true }), slug: 'openbao' })
    // Vacuity control FIRST: the card DID render. Without it, a crash or a
    // not-yet-mounted route would satisfy the null assertion below for
    // entirely the wrong reason — which is exactly what happened on the
    // first run of this file.
    expect(await screen.findByText('BOOTSTRAP')).toBeTruthy()
    expect(screen.queryByTestId('sov-app-org-spine-openbao')).toBeNull()
  })
})
