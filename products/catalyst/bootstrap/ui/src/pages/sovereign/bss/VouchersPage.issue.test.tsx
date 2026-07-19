/**
 * VouchersPage.issue.test.tsx — regression lock-in for UAT rows 72/73/74
 * (issue #5223): the Issue-voucher modal mirrors the SERVER voucher-code
 * strength policy (core/services/shared/voucher/code.go) inline, and
 * renders the plan-tier field the row-72 criterion names.
 *
 *   • Row 72 — the issuance form renders code + credit OMR + PLAN TIER +
 *     description. The plan-tier select is fed by the live commerce plans
 *     list (never hardcoded); the chosen slug submits as `plan_tier`, and
 *     "Any plan (credit only)" omits the field. The table's PLAN column
 *     renders the persisted tier.
 *   • Row 73 — a weak custom code (`1234`) must be rejected INLINE with the
 *     min-12-chars message; before this fix it sailed to the server and the
 *     operator only saw the opaque post-submit 400.
 *   • Row 74 — a BLANK code is the server's auto-generate path
 *     (`VCH-XXXXXXXXXXXX`, ~60 bits); before this fix the client-side
 *     "Voucher code is required." block made that path UI-unreachable.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

// ── Mocks (declared before importing the page) ─────────────────────────
vi.mock('@/shared/lib/useResolvedDeploymentId', () => ({
  useResolvedDeploymentId: () => ({ deploymentId: 'dep-test-1', loading: false }),
}))

vi.mock('../useDeploymentEvents', () => ({
  useDeploymentEvents: () => ({ snapshot: null }),
}))

vi.mock('../PortalShell', () => ({
  PortalShell: ({ children }: { children?: React.ReactNode }) => (
    <div data-testid="portal-shell-stub">{children}</div>
  ),
}))

const issueSpy = vi.fn((req: Record<string, unknown>) =>
  Promise.resolve({
    code: (req.code as string) ?? 'VCH-AUTOGEN12345',
    credit_omr: req.credit_omr as number,
    active: true,
    times_redeemed: 0,
    max_redemptions: 0,
    created_at: '2026-07-19T00:00:00Z',
  }),
)
vi.mock('@/lib/bss.api', () => ({
  issueVoucher: (req: Record<string, unknown>) => issueSpy(req),
  listVouchers: () => Promise.resolve([]),
  revokeVoucher: () => Promise.resolve(),
  voucherStatus: () => 'active',
}))

// Row 72 — the Plan-tier select is fed by the commerce plans list. Two
// generic tiers + one product-scoped tier (which the #3156 guard must
// exclude from the picker).
vi.mock('@/lib/commerce.api', () => ({
  listPlans: () =>
    Promise.resolve([
      { slug: 'm', name: 'M', price_omr: 9, sort_order: 2, features: [] },
      { slug: 's', name: 'S', price_omr: 5, sort_order: 1, features: [] },
      {
        slug: 'agenity-s',
        name: 'Agenity S',
        price_omr: 3,
        sort_order: 9,
        product_slug: 'agenity',
        features: [],
      },
    ]),
}))

import { VouchersPage } from './VouchersPage'
import {
  validateVoucherCodeStrength,
  VOUCHER_CODE_MIN_LEN,
  VOUCHER_CODE_MIN_DISTINCT,
} from './voucherCodePolicy'

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <VouchersPage disableStream disableFetch initialItemsOverride={[]} />
    </QueryClientProvider>,
  )
}

function openIssueModal() {
  fireEvent.click(screen.getByTestId('bss-vouchers-issue-cta'))
  expect(screen.getByTestId('bss-issue-voucher-modal')).toBeTruthy()
}

function setCredit(value: string) {
  fireEvent.change(screen.getByTestId('bss-issue-voucher-credit'), {
    target: { value },
  })
}

beforeEach(() => {
  issueSpy.mockClear()
})

afterEach(() => {
  cleanup()
})

describe('validateVoucherCodeStrength — the inline mirror of the server policy', () => {
  it('accepts empty (the auto-generate path)', () => {
    expect(validateVoucherCodeStrength('')).toBeNull()
    expect(validateVoucherCodeStrength('   ')).toBeNull()
  })

  it('rejects a short code with the min-length message (row 73)', () => {
    const msg = validateVoucherCodeStrength('1234')
    expect(msg).toContain(`at least ${VOUCHER_CODE_MIN_LEN} characters`)
  })

  it('measures the hyphen-stripped body, matching the server', () => {
    // 11 body chars split by hyphens — still too short.
    expect(validateVoucherCodeStrength('ABC-DEF-2026')).not.toBeNull()
    // 12 body chars with >= 6 distinct — acceptable.
    expect(validateVoucherCodeStrength('LAUNCH-2026X-Y')).toBeNull()
  })

  it('rejects a low-distinct-chars code (ABABABABABAB shape)', () => {
    const msg = validateVoucherCodeStrength('ABABABABABAB')
    expect(msg).toContain(`${VOUCHER_CODE_MIN_DISTINCT} distinct`)
  })

  it('accepts a strong custom code', () => {
    expect(validateVoucherCodeStrength('LAUNCH2026PROMO')).toBeNull()
  })
})

describe('IssueVoucherModal — rows 73/74', () => {
  it('row 73: a weak code gets an INLINE min-12-char rejection and never reaches the API', async () => {
    renderPage()
    openIssueModal()
    fireEvent.change(screen.getByTestId('bss-issue-voucher-code'), {
      target: { value: '1234' },
    })
    setCredit('5')
    // The live hint appears while typing…
    expect(
      screen.getByTestId('bss-issue-voucher-code-hint').textContent,
    ).toContain(`at least ${VOUCHER_CODE_MIN_LEN} characters`)
    // …and the same rule gates submit.
    fireEvent.click(screen.getByTestId('bss-issue-voucher-submit'))
    expect(
      screen.getByTestId('bss-issue-voucher-error').textContent,
    ).toContain(`at least ${VOUCHER_CODE_MIN_LEN} characters`)
    expect(issueSpy).not.toHaveBeenCalled()
  })

  it('row 74: a BLANK code submits (auto-generate path) with the code field omitted', async () => {
    renderPage()
    openIssueModal()
    setCredit('10')
    fireEvent.click(screen.getByTestId('bss-issue-voucher-submit'))
    await waitFor(() => expect(issueSpy).toHaveBeenCalledTimes(1))
    const req = issueSpy.mock.calls[0][0]
    expect('code' in req).toBe(false)
    expect(req.credit_omr).toBe(10)
  })

  it('a strong custom code still submits uppercased', async () => {
    renderPage()
    openIssueModal()
    fireEvent.change(screen.getByTestId('bss-issue-voucher-code'), {
      target: { value: 'launch2026promo' },
    })
    setCredit('7')
    fireEvent.click(screen.getByTestId('bss-issue-voucher-submit'))
    await waitFor(() => expect(issueSpy).toHaveBeenCalledTimes(1))
    expect(issueSpy.mock.calls[0][0].code).toBe('LAUNCH2026PROMO')
  })
})

describe('IssueVoucherModal — row 72 plan tier', () => {
  it('renders the Plan-tier select fed by the commerce plans list, sorted, generic-only', async () => {
    renderPage()
    openIssueModal()
    const select = screen.getByTestId(
      'bss-issue-voucher-plan-tier',
    ) as HTMLSelectElement
    // Fetched options land async — wait for S + M.
    await waitFor(() => expect(select.options.length).toBe(3))
    expect(select.options[0].value).toBe('') // Any plan (credit only)
    expect(select.options[1].value).toBe('s') // sort_order wins over fetch order
    expect(select.options[2].value).toBe('m')
    // #3156 guard — the product-scoped plan never enters the picker.
    expect(
      Array.from(select.options).some((o) => o.value === 'agenity-s'),
    ).toBe(false)
  })

  it('submits the chosen plan slug as plan_tier', async () => {
    renderPage()
    openIssueModal()
    const select = screen.getByTestId('bss-issue-voucher-plan-tier')
    await waitFor(() =>
      expect((select as HTMLSelectElement).options.length).toBe(3),
    )
    fireEvent.change(select, { target: { value: 'm' } })
    setCredit('20')
    fireEvent.click(screen.getByTestId('bss-issue-voucher-submit'))
    await waitFor(() => expect(issueSpy).toHaveBeenCalledTimes(1))
    expect(issueSpy.mock.calls[0][0].plan_tier).toBe('m')
  })

  it('omits plan_tier entirely for "Any plan (credit only)"', async () => {
    renderPage()
    openIssueModal()
    setCredit('5')
    fireEvent.click(screen.getByTestId('bss-issue-voucher-submit'))
    await waitFor(() => expect(issueSpy).toHaveBeenCalledTimes(1))
    expect('plan_tier' in issueSpy.mock.calls[0][0]).toBe(false)
  })
})

describe('Voucher table — row 72 PLAN column', () => {
  it('renders the persisted plan tier in the PLAN column and the drawer', () => {
    const voucher = {
      code: 'VCH-PLANM12345',
      credit_omr: 20,
      description: 'plan-tier row',
      plan_tier: 'm',
      active: true,
      max_redemptions: 1,
      times_redeemed: 0,
      created_at: '2026-07-19T00:00:00Z',
    }
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    render(
      <QueryClientProvider client={qc}>
        <VouchersPage
          disableStream
          disableFetch
          initialItemsOverride={[voucher]}
        />
      </QueryClientProvider>,
    )
    expect(
      screen.getByTestId('bss-voucher-plan-VCH-PLANM12345').textContent,
    ).toBe('m')
    fireEvent.click(screen.getByTestId('bss-voucher-toggle-VCH-PLANM12345'))
    expect(
      screen.getByTestId('bss-voucher-drawer-plan-VCH-PLANM12345').textContent,
    ).toBe('m')
  })

  it('renders an em-dash in PLAN and "Any plan" in the drawer for a credit-only voucher', () => {
    const voucher = {
      code: 'VCH-CREDITONLY1',
      credit_omr: 5,
      description: '',
      active: true,
      max_redemptions: 0,
      times_redeemed: 0,
      created_at: '2026-07-19T00:00:00Z',
    }
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    render(
      <QueryClientProvider client={qc}>
        <VouchersPage
          disableStream
          disableFetch
          initialItemsOverride={[voucher]}
        />
      </QueryClientProvider>,
    )
    expect(
      screen.getByTestId('bss-voucher-plan-VCH-CREDITONLY1').textContent,
    ).toBe('—')
    fireEvent.click(screen.getByTestId('bss-voucher-toggle-VCH-CREDITONLY1'))
    expect(
      screen.getByTestId('bss-voucher-drawer-plan-VCH-CREDITONLY1').textContent,
    ).toBe('Any plan (credit only)')
  })
})
