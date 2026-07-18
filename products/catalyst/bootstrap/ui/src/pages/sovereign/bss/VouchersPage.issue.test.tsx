/**
 * VouchersPage.issue.test.tsx — regression lock-in for UAT rows 73/74
 * (issue #5223): the Issue-voucher modal mirrors the SERVER voucher-code
 * strength policy (core/services/shared/voucher/code.go) inline.
 *
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
