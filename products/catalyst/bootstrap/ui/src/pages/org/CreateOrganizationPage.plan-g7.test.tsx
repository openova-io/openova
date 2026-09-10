/**
 * CreateOrganizationPage.plan-g7.test.tsx — UAT row G7 (Refs #4293).
 *
 * Clause: "vcluster dual-door walk — both Org doors land a vcluster-isolation
 * Org."
 *
 * # Where the boundary comes from
 *
 * Every Organization on every plan and of either kind is backed by a dedicated
 * vCluster (founder direction 2026-09-10, Refs #4292 #4539 #6135). The plan
 * the console door sends sizes the ResourceQuota/LimitRange inside that
 * vCluster; it does not select the boundary. So the clause is satisfied from
 * this door by the boundary itself, and these tests pin two things: the door
 * still sends the purchased plan (the quota input the server has accepted
 * since #4292 and the funnel has sent since #4473), and nothing on this page —
 * not the plan, not the kind, not the Advanced panel — can present or send any
 * boundary but `vcluster`.
 *
 * # History
 *
 * While a plan-keyed tier gate existed (free/S → host `<slug>` namespace,
 * m/l/xl/flexi → vCluster), this door carried no `plan_slug`, so every
 * Organization created here was normalised to `s` and authored onto a host
 * namespace — the clause was unsatisfiable from this door by construction, and
 * an operator who opened Advanced and picked `vcluster` got HTTP 422
 * `isolation-plan-conflict` from #6135 because the plan could not deliver it.
 *
 * These tests drive the REAL component and assert on the REAL submit payload
 * (the mocked `createOrganization` is the module boundary, one layer below the
 * page), not on a helper.
 */
import { describe, expect, it, vi, beforeEach } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { CreateOrganizationPage } from './CreateOrganizationPage'
import { createOrganization, type SovereignParentDomain } from './org.api'
import { ORG_PLAN_SLUGS } from '@/lib/organizations.api'

vi.mock('./org.api', async () => {
  const actual = await vi.importActual<typeof import('./org.api')>('./org.api')
  return { ...actual, createOrganization: vi.fn() }
})

const POOL: SovereignParentDomain[] = [
  { name: 'omani.homes', role: 'org-pool', flipStatus: 'ready' },
]

/** Fill the required fields and submit; the create is rejected so the page
 *  stays on the form and the payload is all we read. */
async function submitWith(mutate: () => void) {
  vi.mocked(createOrganization).mockClear()
  vi.mocked(createOrganization).mockRejectedValueOnce(new Error('stop here'))
  render(<CreateOrganizationPage initialParentDomains={POOL} disableFetch />)
  mutate()
  fireEvent.change(screen.getByTestId('org-create-subdomain'), {
    target: { value: 'acme' },
  })
  fireEvent.change(screen.getByTestId('org-create-email'), {
    target: { value: 'admin@acme.com' },
  })
  fireEvent.click(screen.getByTestId('org-create-submit'))
  await waitFor(() => expect(createOrganization).toHaveBeenCalled())
  return vi.mocked(createOrganization).mock.calls.at(-1)![0]
}

describe('UAT row G7 — the console door lands a vcluster-isolation Org on every plan', () => {
  beforeEach(() => {
    // This suite renders the page in every case; without an explicit unmount
    // the previous DOM lingers and every getByTestId resolves to two nodes.
    cleanup()
    vi.mocked(createOrganization).mockReset()
  })

  it('offers exactly the plans the server accepts', () => {
    render(<CreateOrganizationPage initialParentDomains={POOL} disableFetch />)
    const select = screen.getByTestId('create-org-plan-select') as HTMLSelectElement
    const offered = Array.from(select.options).map((o) => o.value)
    // A plan the server does not know is silently coerced to "s" — an option
    // that quietly becomes a different Org is worse than no option.
    expect(offered).toEqual([...ORG_PLAN_SLUGS])
  })

  it('sends the chosen plan, which sizes the quota inside the Org vCluster', async () => {
    const body = await submitWith(() => {
      fireEvent.change(screen.getByTestId('create-org-plan-select'), {
        target: { value: 'm' },
      })
    })
    expect(
      body.plan_slug,
      'the console door dropped the plan again — the server normalises a ' +
        'plan-less create to "s" and the Org lands with the smallest quota ' +
        'instead of the one the operator picked',
    ).toBe('m')
  })

  it('renders the same vcluster boundary whichever plan is chosen', () => {
    render(<CreateOrganizationPage initialParentDomains={POOL} disableFetch />)
    const badge = () =>
      screen.getByTestId('create-org-isolation').getAttribute('data-isolation')

    // Default plan S — a dedicated vCluster like every other plan.
    expect(badge()).toBe('vcluster')

    fireEvent.change(screen.getByTestId('create-org-plan-select'), {
      target: { value: 'm' },
    })
    expect(badge()).toBe('vcluster')

    // CONTROL that shares the suspect property: back down to S on the same
    // form. A page that still keyed the badge off the plan would flip here.
    fireEvent.change(screen.getByTestId('create-org-plan-select'), {
      target: { value: 's' },
    })
    expect(badge()).toBe('vcluster')
  })

  it('the default plan is still S, so an operator who ignores the control gets the old behaviour', async () => {
    const body = await submitWith(() => undefined)
    expect(body.plan_slug).toBe('s')
    // Unchanged from #5857: isolation is not sent as a default; the server
    // stamps the vCluster boundary itself.
    expect('isolation' in body).toBe(false)
  })

  it('a plan plus the explicit isolation assertion AGREE, so #6135 cannot 422 them', async () => {
    const body = await submitWith(() => {
      fireEvent.change(screen.getByTestId('create-org-plan-select'), {
        target: { value: 'xl' },
      })
      fireEvent.click(screen.getByTestId('create-org-advanced-toggle'))
    })
    expect(body.plan_slug).toBe('xl')
    // Opening Advanced sends the one boundary every plan delivers as an
    // explicit assertion, which is the whole contract of #6135's constraint
    // assertion: it can only ever agree.
    expect(body.isolation).toBe('vcluster')
  })

  it('every plan the picker offers renders the vcluster boundary — there is no other', () => {
    render(<CreateOrganizationPage initialParentDomains={POOL} disableFetch />)
    // Vacuity: the sweep is only a guard if the picker offers more than one
    // plan to sweep.
    expect(ORG_PLAN_SLUGS.length).toBeGreaterThan(1)
    for (const plan of ORG_PLAN_SLUGS) {
      fireEvent.change(screen.getByTestId('create-org-plan-select'), {
        target: { value: plan },
      })
      expect(
        screen.getByTestId('create-org-isolation').getAttribute('data-isolation'),
        `plan ${plan} must render a dedicated vCluster`,
      ).toBe('vcluster')
    }
    // And the page offers no way to pick a namespace boundary: the option
    // would promise what no plan delivers (the server refuses it with 422).
    fireEvent.click(screen.getByTestId('create-org-advanced-toggle'))
    expect(screen.queryByTestId('create-org-isolation-select')).toBeNull()
    expect(screen.getByTestId('create-org-advanced').textContent).not.toContain('namespace')
  })
})
