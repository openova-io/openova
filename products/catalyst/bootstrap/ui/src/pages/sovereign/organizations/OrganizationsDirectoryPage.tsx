/**
 * OrganizationsDirectoryPage — /organizations.
 *
 * The Organizations menu (issue #3378) replaces BSS and the never-built
 * OSS with ONE directory: the parent organization's complete view of
 * everything beneath it. The parent org is the FIRST citizen (#3378 §4),
 * so the menu is never blank even on a sovereign with zero sub-orgs
 * (#3378 §5 empty-state law).
 *
 * Wire path (rides EXISTING endpoints): listOrganizations() composes the
 * parent row from GET /api/v1/sovereign/self + every sub-org row from
 * GET /api/v1/organizations. No new directory endpoint (#3378 §6).
 *
 * Chrome + design tokens inherited from the sibling sovereign surfaces
 * (the legacy roster page / JobsPage / ParentDomainsPage) — no bespoke chrome, no
 * hex colours outside the documented amber/rose/emerald status-pill
 * exception.
 */

import { useMemo, useState } from 'react'
import { Link } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'
import { DETECTED_MODE } from '@/shared/lib/detectMode'
import { PortalShell } from '../PortalShell'
import { ShowbackPanel } from './ShowbackPanel'
import {
  listOrganizations,
  type OrgRow,
  type OrgBillingMode,
  type OrgKind,
  type OrgStatus,
} from '@/lib/organizations.api'

const QUERY_STALE_MS = 30_000

// The Organizations menu's sibling surfaces (issue #3378 §4). The
// directory is the landing; commerce/billing/domains hang off it.
const SECTION_LINKS: readonly { id: string; label: string; to: string }[] = [
  { id: 'commerce-plans', label: 'Commerce · Plans', to: '/organizations/commerce/plans' },
  { id: 'commerce-addons', label: 'Add-ons', to: '/organizations/commerce/addons' },
  { id: 'commerce-bundles', label: 'Bundles', to: '/organizations/commerce/bundles' },
  { id: 'commerce-industries', label: 'Industries', to: '/organizations/commerce/industries' },
  { id: 'commerce-apps', label: 'Apps', to: '/organizations/commerce/apps' },
  // #4196 — Billing is now its own first-class top-level menu in the
  // SovereignSidebar (Vouchers · Orders · Revenue). These quick-links jump
  // straight into it. Vouchers is the Phase-0 sovereign-admin onboarding
  // tool (DoD.md Phase 0), reachable day-one in any billing mode (#4170).
  { id: 'billing', label: 'Billing', to: '/billing/revenue' },
  { id: 'vouchers', label: 'Vouchers', to: '/billing/vouchers' },
  { id: 'domains', label: 'Domains', to: '/organizations/domains' },
]

export interface OrganizationsDirectoryPageProps {
  /** Test seam — bypass the React Query fetcher with synthetic rows. */
  initialOrgsOverride?: readonly OrgRow[]
}

export function OrganizationsDirectoryPage({
  initialOrgsOverride,
}: OrganizationsDirectoryPageProps = {}) {
  const { deploymentId: resolvedId } = useResolvedDeploymentId()
  const deploymentId = resolvedId ?? ''
  const sovereignFQDN = DETECTED_MODE.sovereignFQDN ?? null

  const query = useQuery<OrgRow[]>({
    queryKey: ['organizations-directory', deploymentId],
    queryFn: listOrganizations,
    staleTime: QUERY_STALE_MS,
    enabled: !initialOrgsOverride,
    placeholderData: (prev) => prev,
  })

  const orgs: readonly OrgRow[] = initialOrgsOverride ?? query.data ?? []
  const loading = !initialOrgsOverride && query.isLoading
  const error =
    !initialOrgsOverride && query.isError ? (query.error as Error).message : null

  const [search, setSearch] = useState<string>('')
  const [kindFilter, setKindFilter] = useState<'' | OrgKind>('')

  const visibleOrgs = useMemo<OrgRow[]>(() => {
    const q = search.trim().toLowerCase()
    return orgs.filter((o) => {
      if (kindFilter && o.kind !== kindFilter) return false
      if (q) {
        const hay = `${o.displayName} ${o.slug} ${o.ownerEmail} ${o.kind} ${o.billingMode}`.toLowerCase()
        if (!hay.includes(q)) return false
      }
      return true
    })
  }, [orgs, search, kindFilter])

  return (
    <PortalShell
      deploymentId={deploymentId}
      sovereignFQDN={sovereignFQDN}
      pageTitle="Organizations"
      headerSlotRight={
        <Link
          to={'/organizations/new' as never}
          data-testid="orgs-create-button"
          className="inline-flex items-center gap-1.5 rounded-md border border-[var(--color-accent)] bg-[var(--color-accent)]/10 px-3 py-1.5 text-xs font-medium text-[var(--color-accent)] no-underline transition-colors hover:bg-[var(--color-accent)]/20"
        >
          <svg className="h-3.5 w-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
            <path strokeLinecap="round" strokeLinejoin="round" d="M12 5v14M5 12h14" />
          </svg>
          Create organization
        </Link>
      }
    >
      <div data-testid="organizations-directory-page">
        <p className="mb-3 text-sm text-[var(--color-text-dim)]">
          Every organization on this Sovereign. The parent organization — the
          Sovereign itself — is the first row; each department and customer is
          one Organization with its own identity, RBAC, and cost attribution.
        </p>

        {/* Section nav — the Organizations menu's sibling surfaces
            (commerce / billing / domains). Issue #3378 §4. */}
        <nav
          data-testid="organizations-section-nav"
          className="mb-4 flex flex-wrap gap-1.5 text-xs"
        >
          {SECTION_LINKS.map((s) => (
            <Link
              key={s.to}
              to={s.to as never}
              data-testid={`organizations-nav-${s.id}`}
              className="rounded-md border border-[var(--color-border)] bg-[var(--color-surface)] px-2.5 py-1 text-[var(--color-text-dim)] no-underline transition-colors hover:border-[var(--color-accent)] hover:text-[var(--color-text)]"
            >
              {s.label}
            </Link>
          ))}
        </nav>

        {/* B3 parent self-showback (DoD 3): the sovereign's own per-app
            cost attribution — walkable pre-sub-org. Hidden in the test
            seam path (initialOrgsOverride) to keep the directory's
            empty-state test focused. */}
        {initialOrgsOverride ? null : (
          <div className="mb-4">
            <ShowbackPanel />
          </div>
        )}

        <div className="mb-3 flex flex-wrap items-end gap-3" data-testid="organizations-toolbar">
          <label className="flex flex-1 min-w-[240px] max-w-md flex-col gap-1">
            <span className="text-[0.62rem] uppercase tracking-wide text-[var(--color-text-dim)]">
              Search
            </span>
            <input
              type="search"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              placeholder="Search by name, slug, owner email, kind, or mode…"
              data-testid="organizations-search"
              aria-label="Search organizations"
              className="rounded-md border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-1.5 text-sm text-[var(--color-text)] focus:border-[var(--color-accent)] focus:outline-none"
            />
          </label>
          <label className="flex flex-col gap-1">
            <span className="text-[0.62rem] uppercase tracking-wide text-[var(--color-text-dim)]">
              Kind
            </span>
            <select
              value={kindFilter}
              onChange={(e) => setKindFilter(e.target.value as '' | OrgKind)}
              data-testid="organizations-filter-kind"
              aria-label="Filter by kind"
              className="rounded-md border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-1.5 text-sm text-[var(--color-text)] focus:border-[var(--color-accent)] focus:outline-none"
            >
              <option value="">All</option>
              <option value="internal">Internal</option>
              <option value="customer">Customer</option>
            </select>
          </label>
          <span
            data-testid="organizations-result-count"
            aria-live="polite"
            className="ml-auto pb-1 font-mono text-xs tabular-nums text-[var(--color-text-dim)]"
          >
            {visibleOrgs.length}/{orgs.length}
          </span>
        </div>

        {error ? (
          <div
            data-testid="organizations-error"
            className="mb-3 rounded-md border border-rose-500/40 bg-rose-500/10 px-3 py-2 text-sm text-rose-300"
          >
            {error}
          </div>
        ) : null}

        {loading ? (
          <div data-testid="organizations-loading" className="text-sm text-[var(--color-text-dim)]">
            Loading…
          </div>
        ) : visibleOrgs.length === 0 ? (
          <div
            data-testid="organizations-no-matches"
            className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] px-4 py-8 text-center text-sm text-[var(--color-text-dim)]"
          >
            No organizations match the current filters.
          </div>
        ) : (
          <div className="overflow-x-auto rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)]">
            <table data-testid="organizations-table" className="w-full border-collapse text-sm">
              <thead>
                <tr className="border-b border-[var(--color-border)] text-left text-[0.7rem] uppercase tracking-wide text-[var(--color-text-dim)]">
                  <th className="px-3 py-2 font-semibold">Organization</th>
                  <th className="px-3 py-2 font-semibold">Kind</th>
                  <th className="px-3 py-2 font-semibold">Tier</th>
                  <th className="px-3 py-2 font-semibold">Billing</th>
                  <th className="px-3 py-2 font-semibold">Isolation</th>
                  <th className="px-3 py-2 font-semibold">Status</th>
                </tr>
              </thead>
              <tbody>
                {visibleOrgs.map((o) => (
                  <OrgDirectoryRow key={o.id} org={o} />
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </PortalShell>
  )
}

function OrgDirectoryRow({ org }: { org: OrgRow }) {
  return (
    <tr
      data-testid={`organizations-row-${org.id}`}
      data-kind={org.kind}
      data-parent={org.isParent ? 'true' : 'false'}
      className="border-b border-[var(--color-border)] last:border-b-0 hover:bg-[var(--color-surface-hover)]"
    >
      <td className="px-3 py-2 align-middle">
        <Link
          to={'/organizations/$org' as never}
          params={{ org: org.slug } as never}
          data-testid={`organizations-cell-name-${org.id}`}
          className="flex flex-col no-underline"
        >
          <span className="flex items-center gap-2 text-[var(--color-text-strong)]">
            {org.displayName}
            {org.isParent ? (
              <span
                data-testid={`organizations-parent-badge-${org.id}`}
                className="inline-flex items-center rounded-full border border-[var(--color-accent)]/40 bg-[var(--color-accent)]/10 px-2 py-0.5 text-[9px] font-semibold uppercase tracking-wide text-[var(--color-accent)]"
              >
                Parent
              </span>
            ) : null}
          </span>
          {org.ownerEmail ? (
            <span className="text-[11px] text-[var(--color-text-dim)]">{org.ownerEmail}</span>
          ) : (
            <span className="font-mono text-[11px] text-[var(--color-text-dim)]">{org.slug}</span>
          )}
        </Link>
      </td>
      <td className="px-3 py-2 align-middle">
        <KindBadge kind={org.kind} orgId={org.id} />
      </td>
      <td
        className="px-3 py-2 align-middle text-xs capitalize text-[var(--color-text)]"
        data-testid={`organizations-cell-tier-${org.id}`}
      >
        {org.tier}
      </td>
      <td className="px-3 py-2 align-middle">
        <BillingModeBadge mode={org.billingMode} orgId={org.id} />
      </td>
      <td
        className="px-3 py-2 align-middle text-xs text-[var(--color-text)]"
        data-testid={`organizations-cell-isolation-${org.id}`}
      >
        {org.isolation}
      </td>
      <td className="px-3 py-2 align-middle">
        <StatusBadge status={org.status} orgId={org.id} />
      </td>
    </tr>
  )
}

function KindBadge({ kind, orgId }: { kind: OrgKind; orgId: string }) {
  const cls =
    kind === 'internal'
      ? 'border-sky-500/40 bg-sky-500/10 text-sky-300'
      : 'border-violet-500/40 bg-violet-500/10 text-violet-300'
  return (
    <span
      data-testid={`organizations-cell-kind-${orgId}`}
      className={`inline-flex items-center rounded-full border px-2 py-0.5 text-[10px] font-semibold uppercase tracking-wide ${cls}`}
    >
      {kind}
    </span>
  )
}

/** BillingModeBadge — real | chargeback | showback (#3378 §2.6 / §5). */
const BILLING_TONE: Record<OrgBillingMode, string> = {
  real: 'border-emerald-500/40 bg-emerald-500/10 text-emerald-300',
  chargeback: 'border-amber-500/40 bg-amber-500/10 text-amber-300',
  showback: 'border-[var(--color-border)] bg-[var(--color-bg-2)] text-[var(--color-text-dim)]',
}

function BillingModeBadge({ mode, orgId }: { mode: OrgBillingMode; orgId: string }) {
  return (
    <span
      data-testid={`organizations-cell-billing-${orgId}`}
      data-mode={mode}
      className={`inline-flex items-center rounded-full border px-2 py-0.5 text-[10px] font-semibold uppercase tracking-wide ${BILLING_TONE[mode]}`}
    >
      {mode}
    </span>
  )
}

const STATUS_TONE: Record<OrgStatus, { label: string; className: string }> = {
  active: { label: 'Active', className: 'border-emerald-500/40 bg-emerald-500/10 text-emerald-300' },
  trial: { label: 'Trial', className: 'border-amber-500/40 bg-amber-500/10 text-amber-300' },
  provisioning: { label: 'Provisioning', className: 'border-amber-500/40 bg-amber-500/10 text-amber-300' },
  suspended: { label: 'Suspended', className: 'border-rose-500/40 bg-rose-500/10 text-rose-300' },
  failed: { label: 'Failed', className: 'border-rose-500/40 bg-rose-500/10 text-rose-300' },
  unknown: {
    label: 'Unknown',
    className: 'border-[var(--color-border)] bg-[var(--color-bg-2)] text-[var(--color-text-dim)]',
  },
}

function StatusBadge({ status, orgId }: { status: OrgStatus; orgId: string }) {
  const tone = STATUS_TONE[status]
  return (
    <span
      data-testid={`organizations-cell-status-${orgId}`}
      data-status={status}
      className={`inline-flex items-center rounded-full border px-2 py-0.5 text-[10px] font-semibold uppercase tracking-wide ${tone.className}`}
    >
      {tone.label}
    </span>
  )
}
