/**
 * CreateOrganizationPage.test.tsx — multi-domain Organization onboarding
 * coverage (issue #828, parent epic #825).
 *
 *   • Heading + form renders
 *   • Parent-domain dropdown lists every org-pool entry
 *   • NS-flip-pending entries are disabled in the dropdown
 *   • Console URL preview updates as fields change (free-subdomain)
 *   • Switching to BYO mode hides the dropdown + shows the BYO field
 *   • Empty pool surfaces the "no parents available" placeholder
 *   • Submit-failure panel renders "create organization:" with no
 *     banned org-rename residue (UAT row 214, issue #5100 — supersedes
 *     the PR #5203 guard that targeted the pre-rename symbol/testids)
 */

import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from '@testing-library/react'
import { CreateOrganizationPage } from './CreateOrganizationPage'
import type { SovereignParentDomain } from './org.api'

vi.mock('./org.api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./org.api')>()
  return { ...actual, createOrganization: vi.fn() }
})
import { createOrganization } from './org.api'

afterEach(() => cleanup())

const POOL: SovereignParentDomain[] = [
  { name: 'omani.works', role: 'org-pool', flipStatus: 'ready' },
  { name: 'omani.trade', role: 'org-pool', flipStatus: 'ready' },
  { name: 'pending.example', role: 'org-pool', flipStatus: 'flipping' },
]

describe('CreateOrganizationPage', () => {
  it('renders heading + form', () => {
    render(<CreateOrganizationPage initialParentDomains={POOL} disableFetch />)
    // Issue #3378: the page is the Organizations internal door now.
    expect(screen.getByTestId('create-org-title').textContent).toBe('Create organization')
    expect(screen.getByTestId('org-create-form')).toBeTruthy()
    expect(screen.getByTestId('org-create-submit')).toBeTruthy()
  })

  /* ── Organizations internal door (issue #3378 B1, DoD-4) ── */

  /* UAT row G7 — this test USED TO assert `data-isolation === 'vcluster'` on
   * first render, and it was protecting a defect rather than behaviour.
   *
   * The form's own #5857 comment already recorded why 'vcluster' is wrong here:
   * the boundary is authored by `boundaryIsVcluster(planSlug)` alone, the form
   * sent no plan, the server normalised it to `s`, and the org-controller
   * therefore built a host `<slug>` namespace. The badge asserted 'vcluster'
   * over a namespace-backed Org — "the BACKING was always right, only the label
   * ignored the tier", which is the exact mislabel isolationForTier was written
   * to remove. Pinning it kept the page advertising a boundary it could not
   * order, and made the honest value look like the regression.
   *
   * The kind-derived BILLING default is real behaviour and stays pinned.
   * Isolation now follows the plan, so it is asserted against the default plan
   * below and re-asserted for a paid tier in CreateOrganizationPage.plan-g7.test.tsx.
   */
  it('defaults to customer kind with real billing and the default plan S boundary', () => {
    render(<CreateOrganizationPage initialParentDomains={POOL} disableFetch />)
    expect(
      screen.getByTestId('create-org-kind-customer').getAttribute('aria-pressed'),
    ).toBe('true')
    expect(screen.getByTestId('create-org-billing-mode').getAttribute('data-mode')).toBe('real')
    // Plan defaults to S, and S shares the host namespace — so this is what the
    // create will actually deliver.
    expect(
      (screen.getByTestId('create-org-plan-select') as HTMLSelectElement).value,
    ).toBe('s')
    expect(screen.getByTestId('create-org-isolation').getAttribute('data-isolation')).toBe('namespace')
  })

  it('selecting Internal renders showback + namespace defaults', () => {
    render(<CreateOrganizationPage initialParentDomains={POOL} disableFetch />)
    fireEvent.click(screen.getByTestId('create-org-kind-internal'))
    expect(
      screen.getByTestId('create-org-kind-internal').getAttribute('aria-pressed'),
    ).toBe('true')
    expect(screen.getByTestId('create-org-billing-mode').getAttribute('data-mode')).toBe('showback')
    expect(screen.getByTestId('create-org-isolation').getAttribute('data-isolation')).toBe('namespace')
  })

  it('the advanced override is visible and can change billing/isolation', () => {
    render(<CreateOrganizationPage initialParentDomains={POOL} disableFetch />)
    fireEvent.click(screen.getByTestId('create-org-kind-internal'))
    // advanced panel hidden until toggled
    expect(screen.queryByTestId('create-org-advanced')).toBeNull()
    fireEvent.click(screen.getByTestId('create-org-advanced-toggle'))
    expect(screen.getByTestId('create-org-advanced')).toBeTruthy()
    // override billing to chargeback — the badge reflects it
    const billingSel = screen.getByTestId('create-org-billing-select') as HTMLSelectElement
    fireEvent.change(billingSel, { target: { value: 'chargeback' } })
    expect(screen.getByTestId('create-org-billing-mode').getAttribute('data-mode')).toBe('chargeback')
  })

  it('renders parent-domain dropdown with every org-pool entry', () => {
    render(<CreateOrganizationPage initialParentDomains={POOL} disableFetch />)
    expect(
      screen.getByTestId('org-create-parent-option-omani.works'),
    ).toBeTruthy()
    expect(
      screen.getByTestId('org-create-parent-option-omani.trade'),
    ).toBeTruthy()
    expect(
      screen.getByTestId('org-create-parent-option-pending.example'),
    ).toBeTruthy()
  })

  it('disables NS-flip-pending entries in the parent dropdown', () => {
    render(<CreateOrganizationPage initialParentDomains={POOL} disableFetch />)
    const pending = screen.getByTestId(
      'org-create-parent-option-pending.example',
    ) as HTMLOptionElement
    expect(pending.disabled).toBe(true)
    const ready = screen.getByTestId(
      'org-create-parent-option-omani.works',
    ) as HTMLOptionElement
    expect(ready.disabled).toBe(false)
  })

  it('updates console URL preview as the operator types the slug', () => {
    render(<CreateOrganizationPage initialParentDomains={POOL} disableFetch />)
    const slug = screen.getByTestId('org-create-subdomain') as HTMLInputElement
    fireEvent.change(slug, { target: { value: 'acme' } })
    const preview = screen.getByTestId('org-create-url-preview')
    // The default-selected parent should be the first ns_flip_ready entry.
    expect(preview.textContent).toBe('console.acme.omani.works')
  })

  it('preview reflects parent dropdown changes', () => {
    render(<CreateOrganizationPage initialParentDomains={POOL} disableFetch />)
    const slug = screen.getByTestId('org-create-subdomain') as HTMLInputElement
    fireEvent.change(slug, { target: { value: 'acme' } })
    const select = screen.getByTestId(
      'org-create-parent-select',
    ) as HTMLSelectElement
    fireEvent.change(select, { target: { value: 'omani.trade' } })
    expect(
      screen.getByTestId('org-create-url-preview').textContent,
    ).toBe('console.acme.omani.trade')
  })

  it('switching to BYO mode hides the parent dropdown + shows the BYO field', () => {
    render(<CreateOrganizationPage initialParentDomains={POOL} disableFetch />)
    expect(screen.queryByTestId('org-create-parent-row')).toBeTruthy()

    const byoRadio = screen
      .getByTestId('org-create-mode-byo')
      .querySelector('input[type="radio"]') as HTMLInputElement
    fireEvent.click(byoRadio)

    expect(screen.queryByTestId('org-create-parent-row')).toBeNull()
    expect(screen.getByTestId('org-create-byo')).toBeTruthy()

    // Type the BYO domain — preview reflects it.
    const byo = screen.getByTestId('org-create-byo') as HTMLInputElement
    fireEvent.change(byo, { target: { value: 'acme.com' } })
    expect(
      screen.getByTestId('org-create-url-preview').textContent,
    ).toBe('console.acme.com')
  })

  it('empty pool renders the no-parents placeholder', () => {
    render(<CreateOrganizationPage initialParentDomains={[]} disableFetch />)
    const select = screen.getByTestId(
      'org-create-parent-select',
    ) as HTMLSelectElement
    expect(select.disabled).toBe(true)
    expect(select.textContent).toContain('No pool parents available')
  })

  /* ── #5857 (UAT row G7): isolation is an OVERRIDE, never a default ──
   *
   * This form has no plan input, so the server normalises planSlug to "s" and
   * the GitOps renderer derives the boundary from planSlug ALONE
   * (BoundaryIsVcluster("s") === false → the host `<slug>` namespace). It never
   * reads the record's Isolation.
   *
   * resolveOrgShape lets a valid explicit `isolation` bypass the tier gate, so
   * sending the kind default — 'vcluster' for every customer — stamped every
   * Door A Org `vcluster` while it was namespace-backed. That is precisely the
   * mislabel isolationForTier was written to remove, re-entering through the
   * override branch.
   *
   * The first assertion is the one that matters: the DEFAULT path must not send
   * the field at all. The second proves the override still works, so the fix is
   * not "stop sending isolation ever".
   */
  it('does NOT send isolation when the operator has not opened the advanced override', async () => {
    // mockClear + calls.at(-1): vi mocks ACCUMULATE across tests in this file,
    // so calls[0] is the first submit ever made, not this test's. Reading it
    // made the override test assert on the DEFAULT test's payload and fail for
    // a reason that had nothing to do with the code under test.
    // Rejecting keeps the component on the error path, which renders cleanly —
    // a partial success record blows up the created-view.
    vi.mocked(createOrganization).mockClear()
    vi.mocked(createOrganization).mockRejectedValueOnce(new Error('stop here'))
    render(<CreateOrganizationPage initialParentDomains={POOL} disableFetch />)

    // Sanity: the badge shows the boundary the DEFAULT PLAN delivers. This
    // line used to assert 'vcluster' — the kind default — which is the mislabel
    // described above rather than a property worth preserving (UAT row G7).
    expect(
      screen.getByTestId('create-org-isolation').getAttribute('data-isolation'),
    ).toBe('namespace')

    fireEvent.change(screen.getByTestId('org-create-subdomain'), {
      target: { value: 'acme' },
    })
    fireEvent.change(screen.getByTestId('org-create-email'), {
      target: { value: 'admin@acme.com' },
    })
    fireEvent.click(screen.getByTestId('org-create-submit'))

    await waitFor(() => expect(createOrganization).toHaveBeenCalled())
    const body = vi.mocked(createOrganization).mock.calls.at(-1)![0]
    expect(
      'isolation' in body,
      'the form sent isolation as a DEFAULT — that bypasses the server tier gate ' +
        '(resolveOrgShape) and stamps the Org "vcluster" while the GitOps renderer ' +
        'backs it with a host namespace, because it derives the boundary from ' +
        'planSlug alone (#5857, UAT row G7).',
    ).toBe(false)
  })

  it('DOES send isolation when the operator explicitly overrides it', async () => {
    vi.mocked(createOrganization).mockClear()
    vi.mocked(createOrganization).mockRejectedValueOnce(new Error('stop here'))
    render(<CreateOrganizationPage initialParentDomains={POOL} disableFetch />)

    fireEvent.click(screen.getByTestId('create-org-advanced-toggle'))
    fireEvent.change(screen.getByTestId('create-org-isolation-select'), {
      target: { value: 'namespace' },
    })
    fireEvent.change(screen.getByTestId('org-create-subdomain'), {
      target: { value: 'acme' },
    })
    fireEvent.change(screen.getByTestId('org-create-email'), {
      target: { value: 'admin@acme.com' },
    })
    fireEvent.click(screen.getByTestId('org-create-submit'))

    await waitFor(() => expect(createOrganization).toHaveBeenCalled())
    const body = vi.mocked(createOrganization).mock.calls.at(-1)![0]
    expect(
      body.isolation,
      'a deliberate operator override was dropped — the fix must suppress the ' +
        'DEFAULT, not the explicit choice',
    ).toBe('namespace')
  })

  /* ── Row 214 regression guard (issue #5100; supersedes PR #5203) ──
     createOrganization's rejection message is rendered VERBATIM in the
     submit-error panel (`err.message`). Lock the rendered copy so it
     can't silently regress back to the banned org-rename term. */
  it('submit failure renders "create organization:" with no banned residue', async () => {
    vi.mocked(createOrganization).mockRejectedValueOnce(
      new Error('create organization: HTTP 500 upstream failure'),
    )
    render(<CreateOrganizationPage initialParentDomains={POOL} disableFetch />)

    fireEvent.change(screen.getByTestId('org-create-subdomain'), {
      target: { value: 'acme' },
    })
    fireEvent.change(screen.getByTestId('org-create-email'), {
      target: { value: 'admin@acme.com' },
    })
    fireEvent.click(screen.getByTestId('org-create-submit'))

    await waitFor(() => {
      expect(
        screen.getByTestId('org-create-submit-error').textContent,
      ).toContain('create organization: HTTP 500')
    })
    expect(
      screen.getByTestId('org-create-submit-error').textContent?.toLowerCase(),
    ).not.toContain('tenant')
  })
})
