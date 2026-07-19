/**
 * VouchersPage — native /billing/vouchers surface (issue #4196).
 *
 * Wave 6 PR 5 (2026-05-17): drops the BssSectionShell iframe wrapper
 * and renders the voucher list + Issue modal as a NATIVE React surface
 * sharing the same PortalShell chrome as BssLandingPage (Wave 6 PR 1),
 * JobsPage, AppsPage, SettingsPage. Per the founder ruling on Wave 6
 * sub-agent UI work — inherit the "big picture", no bespoke chrome, no
 * hex colours, no new card components.
 *
 * Layout:
 *   • Header: tagline + filter row (search + status select + Issue CTA)
 *   • Table: Code | Recipient (—) | Plan | Value | Status pill |
 *            Issued | Expires (—) | Redeemed by (— or N/N)
 *   • Issue modal: code, credit_omr, plan_tier, description,
 *     max_redemptions, recipient_email —
 *     POSTs /api/v1/org/billing/vouchers/issue
 *
 * The "Plan" column is LIVE as of #5223 (UAT row 72): the Issue modal
 * carries a Plan-tier select fed by the commerce plans list (the same
 * catalog the funnel's plan step renders), the billing service persists
 * `plan_tier` on the promo_codes row, and the table + drawer render it.
 * An empty plan tier = credit-only voucher usable with any plan.
 *
 * The "Recipient" and "Expires" columns remain `—` placeholders because
 * the backend (PromoCode store row, see
 * core/services/billing/handlers/vouchers.go) does NOT yet persist the
 * recipient email (it is request-only per the issueVoucherRequest
 * comment) or an expiry-at field. The columns are present in the
 * target-state per Wave 6 brief; flipping them from `—` to live values
 * is a future BE schema extension, not a UI change. Per
 * INVIOLABLE-PRINCIPLES.md #1 (waterfall — first paint is the full
 * surface) we render the full target-state column set from mount.
 *
 * The Revoke action lives in the row drill-in (drawer), NOT inline,
 * matching the founder ruling that destructive lives inside the
 * drill-in and never on list rows.
 */

import { useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'
import { PortalShell } from '../PortalShell'
import { BillingSectionNav } from './BillingSectionNav'
import { useDeploymentEvents } from '../useDeploymentEvents'
// Rows 73/74 (issue #5223) — the inline mirror of the server voucher-code
// strength policy (core/services/shared/voucher/code.go).
import {
  validateVoucherCodeStrength,
  VOUCHER_CODE_MIN_DISTINCT,
  VOUCHER_CODE_MIN_LEN,
} from './voucherCodePolicy'
import {
  issueVoucher,
  listVouchers,
  revokeVoucher,
  voucherStatus,
  type IssueVoucherRequest,
  type Voucher,
  type VoucherStatus,
} from '@/lib/bss.api'
// #5223 (UAT row 72) — the Plan-tier select in the Issue modal is fed by
// the SAME commerce plans list the funnel's plan step + the Commerce
// editor render (catalyst-api /api/v1/org/commerce/plans read-proxy).
import { listPlans, type CommercePlan } from '@/lib/commerce.api'

const QUERY_STALE_MS = 15_000

type StatusFilter = 'all' | VoucherStatus

export interface VouchersPageProps {
  /** Test seam — disables the live SSE attach. */
  disableStream?: boolean
  /** Test seam — bypass the React Query fetcher with synthetic rows. */
  initialItemsOverride?: Voucher[]
  /** Test seam — disables the React Query fetch (renders the supplied
   *  initialItemsOverride only, no network). */
  disableFetch?: boolean
}

export function VouchersPage({
  disableStream = false,
  initialItemsOverride,
  disableFetch = false,
}: VouchersPageProps = {}) {
  const { deploymentId: resolvedId } = useResolvedDeploymentId()
  const deploymentId = resolvedId ?? ''

  const { snapshot } = useDeploymentEvents({
    deploymentId,
    applicationIds: [],
    disableStream,
  })
  const sovereignFQDN =
    snapshot?.sovereignFQDN ?? snapshot?.result?.sovereignFQDN ?? null

  const query = useQuery<Voucher[]>({
    queryKey: ['bss-vouchers', deploymentId],
    queryFn: listVouchers,
    staleTime: QUERY_STALE_MS,
    enabled: !disableFetch && !initialItemsOverride,
    placeholderData: (prev) => prev,
  })

  const items = initialItemsOverride ?? query.data ?? []
  const loading = !initialItemsOverride && query.isLoading
  const fetchError =
    !initialItemsOverride && query.isError
      ? (query.error as Error | undefined)?.message ?? 'Failed to load vouchers'
      : null

  const [search, setSearch] = useState('')
  const [statusFilter, setStatusFilter] = useState<StatusFilter>('all')
  const [showIssueModal, setShowIssueModal] = useState(false)
  const [expanded, setExpanded] = useState<string | null>(null)
  const [rowError, setRowError] = useState<string | null>(null)

  const filtered = useMemo(() => {
    const q = search.trim().toLowerCase()
    return items.filter((v) => {
      if (statusFilter !== 'all' && voucherStatus(v) !== statusFilter) {
        return false
      }
      if (!q) return true
      return (
        v.code.toLowerCase().includes(q) ||
        (v.description ?? '').toLowerCase().includes(q)
      )
    })
  }, [items, search, statusFilter])

  async function onRevoke(v: Voucher) {
    if (
      !confirm(
        `Revoke voucher "${v.code}"? Past redemptions remain attributed; only NEW redemptions will be blocked.`,
      )
    ) {
      return
    }
    try {
      setRowError(null)
      await revokeVoucher(v.code)
      void query.refetch()
      setExpanded(null)
    } catch (err) {
      setRowError((err as Error).message)
    }
  }

  return (
    <PortalShell
      deploymentId={deploymentId}
      sovereignFQDN={sovereignFQDN}
      pageTitle="Billing — Vouchers"
      headerSlotLeft={<BillingSectionNav />}
    >
      <div className="mx-auto max-w-7xl" data-testid="bss-vouchers-page">
        <header className="mb-4">
          <p className="text-sm text-[var(--color-text-dim)]">
            Issue prepaid codes for Organization signup or upgrades. Past
            redemptions are preserved on revoke; only new redemptions are
            blocked.
          </p>
        </header>

        <div className="mb-4 flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
          <div className="flex flex-1 flex-col gap-3 sm:flex-row sm:items-center">
            <input
              type="search"
              data-testid="bss-vouchers-search"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              placeholder="Search code or description…"
              className="w-full max-w-xs rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] px-3 py-1.5 text-sm text-[var(--color-text)] focus:border-[var(--color-accent)] focus:outline-none"
            />
            <select
              data-testid="bss-vouchers-status-filter"
              value={statusFilter}
              onChange={(e) => setStatusFilter(e.target.value as StatusFilter)}
              className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] px-3 py-1.5 text-sm text-[var(--color-text)] focus:border-[var(--color-accent)] focus:outline-none"
            >
              <option value="all">All statuses</option>
              <option value="active">Active</option>
              <option value="inactive">Inactive</option>
              <option value="exhausted">Exhausted</option>
              <option value="revoked">Revoked</option>
            </select>
          </div>
          <button
            type="button"
            data-testid="bss-vouchers-issue-cta"
            onClick={() => setShowIssueModal(true)}
            className="rounded-md bg-[var(--color-accent)] px-3 py-1.5 text-sm font-medium text-white hover:opacity-90"
          >
            + Issue voucher
          </button>
        </div>

        {fetchError ? (
          <div
            data-testid="bss-vouchers-error"
            className="mb-3 rounded-md border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-300"
          >
            {fetchError}
          </div>
        ) : null}

        {rowError ? (
          <div
            data-testid="bss-vouchers-row-error"
            className="mb-3 rounded-md border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-300"
          >
            {rowError}
          </div>
        ) : null}

        {loading ? (
          <div
            data-testid="bss-vouchers-loading"
            className="text-sm text-[var(--color-text-dim)]"
          >
            Loading…
          </div>
        ) : filtered.length === 0 ? (
          <div
            data-testid="bss-vouchers-empty"
            className="rounded-md border border-[var(--color-border)] px-4 py-8 text-center text-sm text-[var(--color-text-dim)]"
          >
            {items.length === 0
              ? 'No vouchers issued yet. Click "+ Issue voucher" to mint a prepaid code.'
              : 'No vouchers match the current filters.'}
          </div>
        ) : (
          <table
            data-testid="bss-vouchers-table"
            className="w-full border-collapse text-sm"
          >
            <thead>
              <tr className="border-b border-[var(--color-border)] text-left text-xs uppercase text-[var(--color-text-dim)]">
                <th className="py-2 pr-3">Code</th>
                <th className="py-2 pr-3">Recipient email</th>
                <th className="py-2 pr-3">Plan</th>
                <th className="py-2 pr-3 text-right">Value</th>
                <th className="py-2 pr-3">Status</th>
                <th className="py-2 pr-3">Issued</th>
                <th className="py-2 pr-3">Expires</th>
                <th className="py-2 pr-3">Redeemed by</th>
              </tr>
            </thead>
            <tbody>
              {filtered.map((v) => (
                <VoucherRow
                  key={v.code}
                  voucher={v}
                  sovereignFQDN={sovereignFQDN}
                  expanded={expanded === v.code}
                  onToggle={() =>
                    setExpanded(expanded === v.code ? null : v.code)
                  }
                  onRevoke={() => onRevoke(v)}
                />
              ))}
            </tbody>
          </table>
        )}

        {showIssueModal && (
          <IssueVoucherModal
            onClose={() => setShowIssueModal(false)}
            onIssued={() => {
              setShowIssueModal(false)
              void query.refetch()
            }}
          />
        )}
      </div>
    </PortalShell>
  )
}

interface VoucherRowProps {
  voucher: Voucher
  /** Live Sovereign FQDN from the deployment snapshot — builds the
   *  canonical redeem URL in the drawer (docs/DOD.md §Domains-canon:
   *  `https://marketplace.<sovereign-fqdn>/redeem/?code=<CODE>`). */
  sovereignFQDN: string | null
  expanded: boolean
  onToggle: () => void
  onRevoke: () => void
}

function VoucherRow({
  voucher,
  sovereignFQDN,
  expanded,
  onToggle,
  onRevoke,
}: VoucherRowProps) {
  const status = voucherStatus(voucher)
  const issued = formatDate(voucher.created_at)
  const redeemed =
    voucher.max_redemptions > 0
      ? `${voucher.times_redeemed}/${voucher.max_redemptions}`
      : voucher.times_redeemed > 0
        ? String(voucher.times_redeemed)
        : '—'
  return (
    <>
      <tr
        data-testid={`bss-voucher-row-${voucher.code}`}
        className="border-b border-[var(--color-border)] hover:bg-[var(--color-bg-2)]"
      >
        <td className="py-2 pr-3">
          <button
            type="button"
            data-testid={`bss-voucher-toggle-${voucher.code}`}
            onClick={onToggle}
            className="flex items-center gap-1 font-mono text-[var(--color-text)] hover:underline"
          >
            <span aria-hidden>{expanded ? '▾' : '▸'}</span>
            {voucher.code}
          </button>
        </td>
        {/* Recipient + Expires are target-state columns; the BE PromoCode
            row does not persist them yet (recipient is request-only,
            expiry is a future schema extension). Render `—` until the BE
            catches up. Plan is LIVE (#5223 UAT row 72). */}
        <td className="py-2 pr-3 text-xs text-[var(--color-text-dim)]">—</td>
        <td
          data-testid={`bss-voucher-plan-${voucher.code}`}
          className={
            voucher.plan_tier
              ? 'py-2 pr-3 text-xs uppercase text-[var(--color-text)]'
              : 'py-2 pr-3 text-xs text-[var(--color-text-dim)]'
          }
        >
          {voucher.plan_tier ? voucher.plan_tier : '—'}
        </td>
        <td className="py-2 pr-3 text-right tabular-nums text-[var(--color-text)]">
          {voucher.credit_omr} OMR
        </td>
        <td className="py-2 pr-3">
          <StatusPill status={status} testCode={voucher.code} />
        </td>
        <td className="py-2 pr-3 text-xs text-[var(--color-text-dim)]">
          {issued}
        </td>
        <td className="py-2 pr-3 text-xs text-[var(--color-text-dim)]">—</td>
        <td className="py-2 pr-3 text-xs text-[var(--color-text-dim)]">
          {redeemed}
        </td>
      </tr>
      {expanded && (
        <tr data-testid={`bss-voucher-drawer-${voucher.code}`}>
          <td
            colSpan={8}
            className="bg-[var(--color-bg-2)] border-b border-[var(--color-border)]"
          >
            <VoucherDrawer
              voucher={voucher}
              sovereignFQDN={sovereignFQDN}
              onRevoke={onRevoke}
            />
          </td>
        </tr>
      )}
    </>
  )
}

interface VoucherDrawerProps {
  voucher: Voucher
  sovereignFQDN: string | null
  onRevoke: () => void
}

function VoucherDrawer({ voucher, sovereignFQDN, onRevoke }: VoucherDrawerProps) {
  const status = voucherStatus(voucher)
  // docs/DOD.md §Domains-canon — the customer-facing redeem URL lives on
  // the marketplace host of THIS Sovereign. Derived from the live
  // deployment snapshot's FQDN, never hardcoded; `—` until the snapshot
  // arrives.
  const redeemURL = sovereignFQDN
    ? `https://marketplace.${sovereignFQDN}/redeem/?code=${encodeURIComponent(voucher.code)}`
    : null
  return (
    <div className="px-4 py-4">
      <dl className="grid grid-cols-1 gap-3 text-xs sm:grid-cols-2">
        <Field label="Code">
          <span className="font-mono text-sm text-[var(--color-text-strong)]">
            {voucher.code}
          </span>
        </Field>
        <Field label="Credit">
          <span className="tabular-nums text-sm text-[var(--color-text-strong)]">
            {voucher.credit_omr} OMR
          </span>
        </Field>
        <Field label="Plan tier">
          <span
            data-testid={`bss-voucher-drawer-plan-${voucher.code}`}
            className={
              voucher.plan_tier
                ? 'text-sm uppercase text-[var(--color-text)]'
                : 'text-sm text-[var(--color-text-dim)]'
            }
          >
            {voucher.plan_tier ? voucher.plan_tier : 'Any plan (credit only)'}
          </span>
        </Field>
        <Field label="Description">
          <span className="text-sm text-[var(--color-text)]">
            {voucher.description || '—'}
          </span>
        </Field>
        <Field label="Redeem URL">
          {redeemURL ? (
            <a
              data-testid={`bss-voucher-redeem-url-${voucher.code}`}
              href={redeemURL}
              target="_blank"
              rel="noreferrer"
              className="break-all font-mono text-xs text-[var(--color-accent)] hover:underline"
            >
              {redeemURL}
            </a>
          ) : (
            <span className="text-sm text-[var(--color-text-dim)]">—</span>
          )}
        </Field>
        <Field label="Status">
          <StatusPill status={status} testCode={`${voucher.code}-drawer`} />
        </Field>
        <Field label="Redemptions">
          <span className="tabular-nums text-sm text-[var(--color-text)]">
            {voucher.max_redemptions > 0
              ? `${voucher.times_redeemed} / ${voucher.max_redemptions}`
              : `${voucher.times_redeemed} (unlimited)`}
          </span>
        </Field>
        <Field label="Issued">
          <span className="text-sm text-[var(--color-text)]">
            {formatDate(voucher.created_at)}
          </span>
        </Field>
      </dl>
      {status !== 'revoked' && (
        <div className="mt-4 flex justify-end">
          <button
            type="button"
            data-testid={`bss-voucher-revoke-${voucher.code}`}
            onClick={onRevoke}
            className="rounded-md border border-rose-500/60 bg-rose-500/5 px-3 py-1.5 text-xs font-medium text-rose-300 hover:bg-rose-500/15"
          >
            Revoke voucher
          </button>
        </div>
      )}
    </div>
  )
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex flex-col gap-1">
      <dt className="text-[10px] font-medium uppercase tracking-wide text-[var(--color-text-dim)]">
        {label}
      </dt>
      <dd>{children}</dd>
    </div>
  )
}

function StatusPill({
  status,
  testCode,
}: {
  status: VoucherStatus
  testCode: string
}) {
  // Tones mirror ParentDomainsPage's FlipStatusBadge family — emerald /
  // amber / rose / dim — keeping the BSS surface visually coherent with
  // the rest of the Sovereign Console.
  const cls =
    status === 'active'
      ? 'bg-emerald-500/15 text-emerald-300'
      : status === 'inactive'
        ? 'bg-zinc-500/15 text-zinc-300'
        : status === 'exhausted'
          ? 'bg-amber-500/15 text-amber-300'
          : 'bg-rose-500/15 text-rose-300'
  const label =
    status === 'active'
      ? 'Active'
      : status === 'inactive'
        ? 'Inactive'
        : status === 'exhausted'
          ? 'Exhausted'
          : 'Revoked'
  return (
    <span
      data-testid={`bss-voucher-status-${testCode}`}
      className={`inline-block rounded-full px-2 py-0.5 text-[11px] font-semibold ${cls}`}
    >
      {label}
    </span>
  )
}

function formatDate(iso: string): string {
  if (!iso || iso.startsWith('0001')) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '—'
  return d.toLocaleDateString()
}


interface IssueVoucherModalProps {
  onClose: () => void
  onIssued: (voucher: Voucher) => void
}

function IssueVoucherModal({ onClose, onIssued }: IssueVoucherModalProps) {
  // Mirrors ParentDomainsPage's AddDomainModal chrome verbatim (form
  // panel layout, label + input + helper text rhythm, Cancel/Submit
  // footer alignment, accent submit button) so the BSS modal reads as
  // a sibling of the admin Parent Domains modal.
  const [code, setCode] = useState('')
  const [creditOmr, setCreditOmr] = useState<number | ''>('')
  const [planTier, setPlanTier] = useState('')
  const [description, setDescription] = useState('')
  const [maxRedemptions, setMaxRedemptions] = useState<number | ''>('')
  const [recipient, setRecipient] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  // #5223 (UAT row 72) — plan tiers come from the live commerce plans
  // list (same catalog the funnel plan step renders), never hardcoded.
  // Product-scoped plans (product_slug set) are excluded per the #3156
  // generic-picker guard. A failed fetch degrades to the credit-only
  // option — issuing stays possible without the catalog.
  const plansQuery = useQuery<CommercePlan[]>({
    queryKey: ['commerce-plans'],
    queryFn: listPlans,
    staleTime: 5 * 60_000,
    retry: false,
  })
  const planOptions = (plansQuery.data ?? [])
    .filter((p) => !p.product_slug)
    .sort((a, b) => a.sort_order - b.sort_order)

  // Rows 73/74 — live inline strength hint while the operator types a
  // custom code (empty = the auto-generate path, no hint). The SAME rule
  // gates submit, so the hint is never advisory-only.
  const liveCodeHint = validateVoucherCodeStrength(code)

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault()
    // Rows 73/74 — mirror the server strength policy inline. An EMPTY
    // code is VALID (the server auto-generates a high-entropy one); a
    // supplied code must clear the min-length + distinct-chars minimum.
    const codeError = validateVoucherCodeStrength(code)
    if (codeError) {
      setError(codeError)
      return
    }
    if (typeof creditOmr !== 'number' || creditOmr <= 0) {
      setError('Credit value must be a positive integer (OMR).')
      return
    }
    setSubmitting(true)
    setError(null)
    try {
      const req: IssueVoucherRequest = {
        credit_omr: creditOmr,
        active: true,
      }
      // Omit `code` entirely when blank — the billing service's
      // IssueVoucher then auto-generates `VCH-XXXXXXXXXXXX` (~60 bits,
      // crypto/rand). The catalyst-api proxy skips edge strength checks
      // for exactly this empty-code path (#4914).
      if (code.trim()) req.code = code.trim().toUpperCase()
      // #5223 (UAT row 72) — omit `plan_tier` when "Any plan" is chosen
      // so the row persists as a credit-only voucher.
      if (planTier) req.plan_tier = planTier
      if (description.trim()) req.description = description.trim()
      if (typeof maxRedemptions === 'number' && maxRedemptions > 0) {
        req.max_redemptions = maxRedemptions
      }
      if (recipient.trim()) req.recipient_email = recipient.trim()
      const created = await issueVoucher(req)
      onIssued(created)
    } catch (err) {
      setError((err as Error).message)
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <div
      data-testid="bss-issue-voucher-modal"
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/60"
      onClick={onClose}
      role="presentation"
    >
      <form
        onSubmit={onSubmit}
        onClick={(e) => e.stopPropagation()}
        className="w-full max-w-md rounded-lg border border-[var(--color-border)] bg-[var(--color-bg)] p-6 shadow-xl"
      >
        <h2 className="mb-1 text-lg font-semibold text-[var(--color-text-strong)]">
          Issue voucher
        </h2>
        <p className="mb-4 text-xs text-[var(--color-text-dim)]">
          Mints a prepaid code redeemable at Organization signup or upgrade. Codes
          are uppercased server-side. Re-issuing the same code resurrects
          a previously revoked voucher (preserving its redemption history)
          and re-fires the recipient email if supplied.
        </p>

        {error && (
          <div
            data-testid="bss-issue-voucher-error"
            className="mb-3 rounded-md border border-red-500/40 bg-red-500/10 px-3 py-2 text-xs text-red-300"
          >
            {error}
          </div>
        )}

        <label className="mb-3 block">
          <div className="mb-1 text-xs font-medium text-[var(--color-text-dim)]">
            Code (optional — leave blank to auto-generate)
          </div>
          <input
            data-testid="bss-issue-voucher-code"
            type="text"
            value={code}
            onChange={(e) => setCode(e.target.value)}
            placeholder="Blank auto-generates VCH-…"
            className="w-full rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] px-3 py-2 text-sm font-mono uppercase text-[var(--color-text)] focus:border-[var(--color-accent)] focus:outline-none"
            autoFocus
            autoComplete="off"
          />
          {liveCodeHint ? (
            <div
              data-testid="bss-issue-voucher-code-hint"
              className="mt-1 text-[10px] text-amber-300"
            >
              {liveCodeHint}
            </div>
          ) : (
            <div className="mt-1 text-[10px] text-[var(--color-text-dim)]">
              A custom code needs ≥ {VOUCHER_CODE_MIN_LEN} characters with ≥{' '}
              {VOUCHER_CODE_MIN_DISTINCT} distinct characters. Blank issues a
              high-entropy VCH-… code.
            </div>
          )}
        </label>

        <label className="mb-3 block">
          <div className="mb-1 text-xs font-medium text-[var(--color-text-dim)]">
            Credit (OMR)
          </div>
          <input
            data-testid="bss-issue-voucher-credit"
            type="number"
            min={1}
            step={1}
            value={creditOmr}
            onChange={(e) =>
              setCreditOmr(e.target.value === '' ? '' : Number(e.target.value))
            }
            placeholder="50"
            className="w-full rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] px-3 py-2 text-sm tabular-nums text-[var(--color-text)] focus:border-[var(--color-accent)] focus:outline-none"
          />
        </label>

        <label className="mb-3 block">
          <div className="mb-1 text-xs font-medium text-[var(--color-text-dim)]">
            Plan tier
          </div>
          <select
            data-testid="bss-issue-voucher-plan-tier"
            value={planTier}
            onChange={(e) => setPlanTier(e.target.value)}
            className="w-full rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] px-3 py-2 text-sm text-[var(--color-text)] focus:border-[var(--color-accent)] focus:outline-none"
          >
            <option value="">Any plan (credit only)</option>
            {planOptions.map((p) => (
              <option key={p.slug} value={p.slug}>
                {p.name} — {p.price_omr} OMR/mo
              </option>
            ))}
          </select>
          <div className="mt-1 text-[10px] text-[var(--color-text-dim)]">
            {plansQuery.isError
              ? 'Plan catalog unavailable — issuing as a credit-only voucher still works.'
              : 'The plan this voucher targets. The credit is applied at checkout either way; the tier is shown to the operator on the voucher list.'}
          </div>
        </label>

        <label className="mb-3 block">
          <div className="mb-1 text-xs font-medium text-[var(--color-text-dim)]">
            Description (optional)
          </div>
          <input
            data-testid="bss-issue-voucher-description"
            type="text"
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            placeholder="Launch promotion — first 100 organizations"
            className="w-full rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] px-3 py-2 text-sm text-[var(--color-text)] focus:border-[var(--color-accent)] focus:outline-none"
          />
        </label>

        <label className="mb-3 block">
          <div className="mb-1 text-xs font-medium text-[var(--color-text-dim)]">
            Max redemptions (optional)
          </div>
          <input
            data-testid="bss-issue-voucher-max-redemptions"
            type="number"
            min={0}
            step={1}
            value={maxRedemptions}
            onChange={(e) =>
              setMaxRedemptions(
                e.target.value === '' ? '' : Number(e.target.value),
              )
            }
            placeholder="Leave blank for unlimited"
            className="w-full rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] px-3 py-2 text-sm tabular-nums text-[var(--color-text)] focus:border-[var(--color-accent)] focus:outline-none"
          />
        </label>

        <label className="mb-4 block">
          <div className="mb-1 text-xs font-medium text-[var(--color-text-dim)]">
            Recipient email (optional)
          </div>
          <input
            data-testid="bss-issue-voucher-recipient"
            type="email"
            value={recipient}
            onChange={(e) => setRecipient(e.target.value)}
            placeholder="founder@acme.example"
            className="w-full rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] px-3 py-2 text-sm text-[var(--color-text)] focus:border-[var(--color-accent)] focus:outline-none"
            autoComplete="off"
          />
          <div className="mt-1 text-[10px] text-[var(--color-text-dim)]">
            When supplied, fires a one-shot "voucher-issued" email via the
            notification service. The address is NOT persisted on the
            voucher row.
          </div>
        </label>

        <div className="flex items-center justify-end gap-2">
          <button
            type="button"
            onClick={onClose}
            className="rounded-md px-3 py-1.5 text-sm text-[var(--color-text-dim)] hover:bg-[var(--color-bg-2)]"
          >
            Cancel
          </button>
          <button
            type="submit"
            data-testid="bss-issue-voucher-submit"
            disabled={submitting}
            className="rounded-md bg-[var(--color-accent)] px-4 py-1.5 text-sm font-medium text-white hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50"
          >
            {submitting ? 'Issuing…' : 'Issue voucher'}
          </button>
        </div>
      </form>
    </div>
  )
}
