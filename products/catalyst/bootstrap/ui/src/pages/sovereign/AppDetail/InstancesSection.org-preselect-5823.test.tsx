/**
 * InstancesSection.org-preselect-5823.test.tsx — UAT row 218.
 *
 * The row asserts the install wizard "opens with the Org's Environment
 * pre-selected as the target". It never did. `org` initialised to `''` and
 * nothing ever set it, so the select sat on "Select an organization…" and the
 * derived `Environment: <org>-prod` line — which only renders once org is
 * chosen — was absent. The hw291 walk measured exactly that: *"the Organization
 * select is NOT pre-selected (value="")"*.
 *
 * WHY THE RULE IS "EXACTLY ONE CANDIDATE" AND NOT "ORG SESSION". Keying off the
 * session scope would need plumbing this dialog does not have, and would still
 * be wrong for a sovereign-admin running a single-Org Sovereign. Counting
 * candidates answers both cases from data already fetched.
 *
 * PARENTS ARE EXCLUDED, and that is the half most likely to be "simplified"
 * away by someone who reads the filter as redundant. The parent row is the
 * Sovereign self-org — the platform's own control-plane Organization.
 * Defaulting a customer install into it would be worse than asking, so a
 * console with only the self-org resolves to ZERO candidates and stays
 * unselected. That is the honest outcome: there is no customer Org yet.
 */

import { describe, it, expect, afterEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createRoute,
  createMemoryHistory,
  Outlet,
} from '@tanstack/react-router'

import type { OrgRow } from '@/lib/organizations.api'

const listOrganizations = vi.fn<() => Promise<OrgRow[]>>()
const getApplicationInstances = vi.fn()

vi.mock('@/lib/organizations.api', async (orig) => ({
  ...(await orig<Record<string, unknown>>()),
  listOrganizations: () => listOrganizations(),
}))

vi.mock('@/lib/catalog.api', async (orig) => ({
  ...(await orig<Record<string, unknown>>()),
  getApplicationInstances: (...a: unknown[]) => getApplicationInstances(...a),
}))

const { InstancesSection } = await import('./InstancesSection')

function orgRow(over: Partial<OrgRow>): OrgRow {
  return {
    id: over.slug ?? 'x',
    slug: 'x',
    displayName: 'X',
    kind: 'customer',
    tier: 'org',
    billingMode: 'real',
    isolation: 'vcluster',
    status: 'active',
    isParent: false,
    consoleHost: '',
    ownerEmail: '',
    ...over,
  } as OrgRow
}

function renderSection() {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const home = createRoute({
    getParentRoute: () => rootRoute,
    path: '/',
    component: () => <InstancesSection blueprint="bp-wordpress" />,
  })
  const appRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/app/$componentId',
    component: () => <div />,
  })
  const router = createRouter({
    routeTree: rootRoute.addChildren([home, appRoute]),
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  // staleTime 0 so a forced refetch actually re-runs the queryFn. The
  // component sets 60s; overriding it here is what makes the
  // "refetch must not clobber the operator" case OBSERVABLE at all — see
  // the note on that test.
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0, staleTime: 0 } },
  })
  render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
  return qc
}

async function openDialog() {
  const qc = renderSection()
  const btn = await screen.findByTestId('btn-new-instance')
  fireEvent.click(btn)
  await screen.findByTestId('select-instance-org')
  return qc
}

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

describe('#5823 — the install wizard pre-selects the only Organization it could be', () => {
  it('pre-selects the sole customer Org and shows the derived Environment', async () => {
    getApplicationInstances.mockResolvedValue({ items: [] })
    listOrganizations.mockResolvedValue([
      orgRow({ slug: 'hw292-omani-works', displayName: 'hw292.omani.works', isParent: true }),
      orgRow({ slug: 'uatco', displayName: 'UAT Co' }),
    ])

    await openDialog()
    const sel = () => screen.getByTestId('select-instance-org') as HTMLSelectElement
    await waitFor(() => expect(sel().value).toBe('uatco'))

    // The row's actual clause is about the ENVIRONMENT being pre-selected as
    // the target. That line only renders once org is set, so it — not the
    // select's value — is what the row asks for.
    const env = await screen.findByTestId('instance-environment-derived')
    expect(env.textContent).toContain('uatco-prod')
  })

  it('leaves the selection blank when several Orgs could be meant', async () => {
    getApplicationInstances.mockResolvedValue({ items: [] })
    listOrganizations.mockResolvedValue([
      orgRow({ slug: 'hw292-omani-works', isParent: true }),
      orgRow({ slug: 'uatco' }),
      orgRow({ slug: 'acme' }),
    ])

    await openDialog()
    const sel = () => screen.getByTestId('select-instance-org') as HTMLSelectElement
    // Wait for the options to land, so the effect has had its data and this
    // asserts on a resolved list rather than on a pending query.
    await waitFor(() => expect(sel().querySelectorAll('option').length).toBeGreaterThan(3))
    expect(sel().value).toBe('')
    expect(screen.queryByTestId('instance-environment-derived')).toBeNull()
  })

  it('never defaults into the parent self-org when it is the only row', async () => {
    // A Sovereign with no customer Organization yet. Pre-selecting the
    // platform's own control-plane Org would silently target a customer
    // install at the control plane — worse than asking.
    getApplicationInstances.mockResolvedValue({ items: [] })
    listOrganizations.mockResolvedValue([
      orgRow({ slug: 'hw292-omani-works', displayName: 'hw292.omani.works', isParent: true }),
    ])

    await openDialog()
    const sel = () => screen.getByTestId('select-instance-org') as HTMLSelectElement
    await waitFor(() => expect(sel().querySelectorAll('option').length).toBe(2))
    expect(
      sel().value,
      'the parent self-org was auto-selected — a customer install would land on the control plane',
    ).toBe('')
  })

  /*
   * NOT TESTED, DELIBERATELY: "a refetch must not overwrite the operator's
   * choice" (the `!org &&` half of the effect's condition).
   *
   * The guard is correct and stays in the code, but it is UNOBSERVABLE through
   * this component and I could not write an honest test for it. Every reachable
   * scenario collapses:
   *
   *   - two candidates -> no pre-select fires at all, so the guard is inert;
   *   - one candidate  -> the select has exactly one option, so the operator
   *     cannot have chosen anything else for the guard to protect;
   *   - two candidates, operator picks A, list collapses to {B} -> A is removed
   *     from the option list, so the select resets to B by DOM semantics
   *     whether or not the guard exists. That is what a first cut of this test
   *     actually measured, and it "failed" for a reason unrelated to the guard.
   *
   * An earlier version slept 30ms and asserted the value was unchanged. It
   * passed against BOTH the guarded and the unguarded implementation — the orgs
   * query carries staleTime 60s and no interval, so no refetch ever happened.
   * A green check that cannot go red is worse than an absent one: it reads as
   * coverage. Removed rather than left in place, and recorded here so the next
   * author does not mistake the gap for an oversight.
   */
})
