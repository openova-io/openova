/**
 * CreateOrganizationPage.plan-g7.test.tsx — UAT row G7 (Refs #4293).
 *
 * Clause: "vcluster dual-door walk — both Org doors land a vcluster-isolation
 * Org."
 *
 * # What was actually missing
 *
 * Not a precondition — a FIELD. An Organization's boundary primitive is decided
 * by `boundaryIsVcluster(planSlug)` alone (#4292, the tier gate at
 * core/controllers/organization/internal/gitops/manifests.go:151): free/S share
 * the host `<slug>` namespace, m/l/xl/flexi get a dedicated Org-vCluster. The
 * server has accepted `plan_slug` on this door since #4292
 * (organization_provisioning.go:290, normalised against catalogPlanSlugs:361),
 * and the FUNNEL door has sent one since #4473
 * (core/services/provisioning/handlers/organization_create.go:140).
 *
 * The CONSOLE door never sent it. `OrgCreateRequest` had no such member, so
 * every Organization created here arrived plan-less, was normalised to `s`, and
 * was authored onto a host namespace. "Both doors land a vcluster-isolation
 * Org" was unsatisfiable from this door by construction — and an operator who
 * opened Advanced and picked `vcluster` got HTTP 422 `isolation-plan-conflict`
 * from #6135, because the plan could not deliver what the label claimed.
 *
 * These tests drive the REAL component and assert on the REAL submit payload
 * (the mocked `createOrganization` is the module boundary, one layer below the
 * page), not on a helper.
 */
import { describe, expect, it, vi, beforeEach } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { CreateOrganizationPage } from './CreateOrganizationPage'
import { createOrganization, type SovereignParentDomain } from './org.api'
import { ORG_PLAN_SLUGS, isolationForPlan } from '@/lib/organizations.api'

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

describe('UAT row G7 — the console door can order a vcluster-isolation Org', () => {
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

  it('sends the chosen paid plan, which is what makes the Org vcluster-backed', async () => {
    const body = await submitWith(() => {
      fireEvent.change(screen.getByTestId('create-org-plan-select'), {
        target: { value: 'm' },
      })
    })
    expect(
      body.plan_slug,
      'the console door dropped the plan again — the server normalises a ' +
        'plan-less create to "s" and boundaryIsVcluster("s") is false, so the ' +
        'Org is authored onto the host namespace and G7 cannot pass from this door',
    ).toBe('m')
  })

  it('renders the boundary the chosen plan will actually deliver', () => {
    render(<CreateOrganizationPage initialParentDomains={POOL} disableFetch />)
    const badge = () =>
      screen.getByTestId('create-org-isolation').getAttribute('data-isolation')

    // Default plan S — host namespace.
    expect(badge()).toBe('namespace')

    fireEvent.change(screen.getByTestId('create-org-plan-select'), {
      target: { value: 'm' },
    })
    expect(badge()).toBe('vcluster')

    // CONTROL that shares the suspect property: back down to S on the same
    // form. A page that simply started printing 'vcluster' once a plan control
    // existed would pass the assertion above and fail this one.
    fireEvent.change(screen.getByTestId('create-org-plan-select'), {
      target: { value: 's' },
    })
    expect(badge()).toBe('namespace')
  })

  it('the default plan is still S, so an operator who ignores the control gets the old behaviour', async () => {
    const body = await submitWith(() => undefined)
    expect(body.plan_slug).toBe('s')
    // Unchanged from #5857: isolation is not sent as a default, so the server
    // derives it from the tier gate.
    expect('isolation' in body).toBe(false)
  })

  it('a paid plan plus an explicit isolation assertion AGREE, so #6135 cannot 422 them', async () => {
    const body = await submitWith(() => {
      fireEvent.change(screen.getByTestId('create-org-plan-select'), {
        target: { value: 'xl' },
      })
      fireEvent.click(screen.getByTestId('create-org-advanced-toggle'))
    })
    expect(body.plan_slug).toBe('xl')
    // The advanced panel opens showing the plan-derived value, so submitting it
    // asserts the boundary the plan delivers rather than contradicting it —
    // which is the whole contract of #6135's constraint assertion.
    expect(body.isolation).toBe('vcluster')
  })

  it('every plan the picker offers maps to a boundary, and the paid ones map to vcluster', () => {
    // Vacuity control for isolationForPlan: if it ever returned one constant,
    // the badge assertions above would still pass for one of the two cases and
    // silently stop discriminating.
    const mapped = ORG_PLAN_SLUGS.map((p) => isolationForPlan(p))
    expect(new Set(mapped).size, 'the tier gate collapsed to a single answer').toBe(2)
    expect(isolationForPlan('s')).toBe('namespace')
    for (const paid of ['m', 'l', 'xl', 'flexi']) {
      expect(isolationForPlan(paid), `plan ${paid} must deliver a vCluster`).toBe('vcluster')
    }
    // Mirrors the server's `case "", "s", "free"` arm verbatim.
    expect(isolationForPlan('')).toBe('namespace')
    expect(isolationForPlan('free')).toBe('namespace')
    expect(isolationForPlan('M')).toBe('vcluster')
  })
})
