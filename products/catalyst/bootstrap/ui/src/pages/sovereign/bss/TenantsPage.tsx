/**
 * TenantsPage — /console/bss/tenants.
 *
 * Wave 6 PR 6 (2026-05-17 founder UX review): native React replacement
 * for the iframe-wrapped BssSectionShell. Inherits PortalShell chrome +
 * design tokens from sibling surfaces (JobsPage / AppsPage /
 * ParentDomainsPage) — no bespoke chrome, no hex colours outside the
 * documented amber/rose/emerald exceptions for status pills.
 *
 * Wire path:
 *
 *   browser ──/api/v1/organizations──▶ catalyst-api (HandleListOrganizations)
 *
 * Wave 6 PR 1 (#1606, 393116355d6e30b1382e843142ad3bbc15d25f51) landed
 * the BSS landing + per-section iframe shell; this PR drops the iframe
 * for /bss/tenants and replaces it with a filterable table of tenant
 * organisations sourced from the existing Organization provisioning store.
 *
 * Layout (PortalShell body):
 *   • Header band: "Tenants" via PortalShell + tagline beneath
 *   • Filter row: search, status / plan / region dropdowns
 *   • Table: Org | Subdomain | Plan | Status | Region | Provisioned |
 *            Last active
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #1 (waterfall) every row renders
 * from mount — loading + empty states match JobsPage / ParentDomainsPage
 * shape so the surface reads as a sibling.
 */

import { useMemo, useState } from 'react'
import { Link } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'
import { PortalShell } from '../PortalShell'
import { useDeploymentEvents } from '../useDeploymentEvents'
import { listTenants, type Tenant, type TenantStatus } from '@/lib/bss.api'

const QUERY_STALE_MS = 30_000

const STATUS_VALUES: readonly TenantStatus[] = [
  'active',
  'trial',
  'provisioning',
  'suspended',
  'failed',
]

export interface TenantsPageProps {
  /** Test seam — disables the live SSE attach. */
  disableStream?: boolean
  /** Test seam — bypass the React Query fetcher with synthetic data. */
  initialTenantsOverride?: readonly Tenant[]
}

export function TenantsPage({
  disableStream = false,
  initialTenantsOverride,
}: TenantsPageProps = {}) {
  const { deploymentId: resolvedId } = useResolvedDeploymentId()
  const deploymentId = resolvedId ?? ''

  const { snapshot } = useDeploymentEvents({
    deploymentId,
    applicationIds: [],
    disableStream,
  })
  const sovereignFQDN =
    snapshot?.sovereignFQDN ?? snapshot?.result?.sovereignFQDN ?? null

  const query = useQuery<Tenant[]>({
    queryKey: ['bss-tenants', deploymentId],
    queryFn: listTenants,
    staleTime: QUERY_STALE_MS,
    enabled: !initialTenantsOverride,
    placeholderData: (prev) => prev,
  })

  const tenants: readonly Tenant[] =
    initialTenantsOverride ?? query.data ?? []
  const loading = !initialTenantsOverride && query.isLoading
  const error =
    !initialTenantsOverride && query.isError ? (query.error as Error).message : null

  const [search, setSearch] = useState<string>('')
  const [statusFilter, setStatusFilter] = useState<'' | TenantStatus>('')
  const [planFilter, setPlanFilter] = useState<string>('')
  const [regionFilter, setRegionFilter] = useState<string>('')

  const planOptions = useMemo<string[]>(() => {
    const set = new Set<string>()
    for (const t of tenants) if (t.plan) set.add(t.plan)
    return [...set].sort((a, b) => a.localeCompare(b))
  }, [tenants])

  const regionOptions = useMemo<string[]>(() => {
    const set = new Set<string>()
    for (const t of tenants) if (t.region) set.add(t.region)
    return [...set].sort((a, b) => a.localeCompare(b))
  }, [tenants])

  const visibleTenants = useMemo<Tenant[]>(() => {
    const q = search.trim().toLowerCase()
    return tenants.filter((t) => {
      if (statusFilter && t.status !== statusFilter) return false
      if (planFilter && t.plan !== planFilter) return false
      if (regionFilter && t.region !== regionFilter) return false
      if (q) {
        const hay =
          `${t.orgName} ${t.consoleHost} ${t.subdomain} ${t.ownerEmail} ${t.status}`.toLowerCase()
        if (!hay.includes(q)) return false
      }
      return true
    })
  }, [tenants, search, statusFilter, planFilter, regionFilter])

  return (
    <PortalShell
      deploymentId={deploymentId}
      sovereignFQDN={sovereignFQDN}
      pageTitle="Organizations"
      headerSlotLeft={
        <Link
          to={'/bss' as never}
          className="text-[11px] text-[var(--color-text-dim)] hover:text-[var(--color-text)] no-underline"
          data-testid="sov-bss-back-to-landing-tenants"
        >
          ← Back to BSS overview
        </Link>
      }
    >
      <div data-testid="bss-tenants-page">
        <p className="mb-4 text-sm text-[var(--color-text-dim)]">
          Organizations provisioned via marketplace signup. Each row is
          one Organization CR + per-Organization vCluster on this Sovereign.
        </p>

        {/* Filter row — mirrors JobsTable.toolbar shape but uses the
            tenant axes (status / plan / region). Plan + Region filters
            hide themselves when only zero or one option appears so a
            fresh Sovereign with no tenants doesn't show stub dropdowns. */}
        <div
          className="mb-3 flex flex-wrap items-end gap-3"
          data-testid="bss-tenants-toolbar"
        >
          <label className="flex flex-1 min-w-[240px] max-w-md flex-col gap-1">
            <span className="text-[0.62rem] uppercase tracking-wide text-[var(--color-text-dim)]">
              Search
            </span>
            <input
              type="search"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              placeholder="Search by org, subdomain, owner email, or status…"
              data-testid="bss-tenants-search"
              aria-label="Search organizations"
              className="rounded-md border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-1.5 text-sm text-[var(--color-text)] focus:border-[var(--color-accent)] focus:outline-none"
            />
          </label>
          <label className="flex flex-col gap-1">
            <span className="text-[0.62rem] uppercase tracking-wide text-[var(--color-text-dim)]">
              Status
            </span>
            <select
              value={statusFilter}
              onChange={(e) => setStatusFilter(e.target.value as '' | TenantStatus)}
              data-testid="bss-tenants-filter-status"
              aria-label="Filter by status"
              className="rounded-md border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-1.5 text-sm text-[var(--color-text)] focus:border-[var(--color-accent)] focus:outline-none"
            >
              <option value="">All</option>
              {STATUS_VALUES.map((s) => (
                <option key={s} value={s}>
                  {STATUS_TONE[s].label}
                </option>
              ))}
            </select>
          </label>
          {planOptions.length > 0 ? (
            <label className="flex flex-col gap-1">
              <span className="text-[0.62rem] uppercase tracking-wide text-[var(--color-text-dim)]">
                Plan
              </span>
              <select
                value={planFilter}
                onChange={(e) => setPlanFilter(e.target.value)}
                data-testid="bss-tenants-filter-plan"
                aria-label="Filter by plan"
                className="rounded-md border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-1.5 text-sm text-[var(--color-text)] focus:border-[var(--color-accent)] focus:outline-none"
              >
                <option value="">All</option>
                {planOptions.map((p) => (
                  <option key={p} value={p}>
                    {p}
                  </option>
                ))}
              </select>
            </label>
          ) : null}
          {regionOptions.length > 0 ? (
            <label className="flex flex-col gap-1">
              <span className="text-[0.62rem] uppercase tracking-wide text-[var(--color-text-dim)]">
                Region
              </span>
              <select
                value={regionFilter}
                onChange={(e) => setRegionFilter(e.target.value)}
                data-testid="bss-tenants-filter-region"
                aria-label="Filter by region"
                className="rounded-md border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-1.5 text-sm text-[var(--color-text)] focus:border-[var(--color-accent)] focus:outline-none"
              >
                <option value="">All</option>
                {regionOptions.map((r) => (
                  <option key={r} value={r}>
                    {r}
                  </option>
                ))}
              </select>
            </label>
          ) : null}
          <span
            data-testid="bss-tenants-result-count"
            aria-live="polite"
            className="ml-auto pb-1 font-mono text-xs tabular-nums text-[var(--color-text-dim)]"
          >
            {visibleTenants.length}/{tenants.length}
          </span>
        </div>

        {error ? (
          <div
            data-testid="bss-tenants-error"
            className="mb-3 rounded-md border border-rose-500/40 bg-rose-500/10 px-3 py-2 text-sm text-rose-300"
          >
            {error}
          </div>
        ) : null}

        {loading ? (
          <div
            data-testid="bss-tenants-loading"
            className="text-sm text-[var(--color-text-dim)]"
          >
            Loading…
          </div>
        ) : tenants.length === 0 ? (
          <div
            data-testid="bss-tenants-empty"
            className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] px-4 py-8 text-center text-sm text-[var(--color-text-dim)]"
          >
            No organizations provisioned yet. Marketplace signups land here once
            the organization-provisioning pipeline (vCluster + Keycloak + DNS +
            certs) reaches the registered state.
          </div>
        ) : visibleTenants.length === 0 ? (
          <div
            data-testid="bss-tenants-no-matches"
            className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] px-4 py-8 text-center text-sm text-[var(--color-text-dim)]"
          >
            No organizations match the current filters.
          </div>
        ) : (
          <div className="overflow-x-auto rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)]">
            <table
              data-testid="bss-tenants-table"
              className="w-full border-collapse text-sm"
            >
              <thead>
                <tr className="border-b border-[var(--color-border)] text-left text-[0.7rem] uppercase tracking-wide text-[var(--color-text-dim)]">
                  <th className="px-3 py-2 font-semibold">Org</th>
                  <th className="px-3 py-2 font-semibold">Subdomain</th>
                  <th className="px-3 py-2 font-semibold">Plan</th>
                  <th className="px-3 py-2 font-semibold">Status</th>
                  <th className="px-3 py-2 font-semibold">Region</th>
                  <th className="px-3 py-2 font-semibold">Provisioned</th>
                  <th className="px-3 py-2 font-semibold">Last active</th>
                </tr>
              </thead>
              <tbody>
                {visibleTenants.map((t) => (
                  <TenantRow key={t.id || t.consoleHost} tenant={t} />
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </PortalShell>
  )
}

interface TenantRowProps {
  tenant: Tenant
}

/**
 * TenantRow — a single row. A future tenant-detail page is out-of-scope
 * for this PR; the row is non-navigable today so we don't ship a dead-
 * end click target. The Org cell carries the `data-href-todo` attribute
 * for the next Wave 6 PR to wire a Link without re-doing the row.
 */
function TenantRow({ tenant }: TenantRowProps) {
  const subdomainLabel =
    tenant.subdomain && tenant.parentDomain
      ? `${tenant.subdomain}.${tenant.parentDomain}`
      : tenant.consoleHost || tenant.subdomain || '—'
  return (
    <tr
      data-testid={`bss-tenants-row-${tenant.id}`}
      data-status={tenant.status}
      className="border-b border-[var(--color-border)] last:border-b-0 hover:bg-[var(--color-surface-hover)]"
    >
      <td className="px-3 py-2 align-middle">
        <div
          data-testid={`bss-tenants-cell-org-${tenant.id}`}
          data-href-todo={tenant.id ? `/bss/tenants/${tenant.id}` : undefined}
          className="flex flex-col"
        >
          <span className="text-[var(--color-text-strong)]">
            {tenant.orgName}
          </span>
          {tenant.ownerEmail ? (
            <span className="text-[11px] text-[var(--color-text-dim)]">
              {tenant.ownerEmail}
            </span>
          ) : null}
        </div>
      </td>
      <td
        className="px-3 py-2 align-middle font-mono text-xs text-[var(--color-text)]"
        data-testid={`bss-tenants-cell-subdomain-${tenant.id}`}
      >
        {subdomainLabel}
      </td>
      <td
        className="px-3 py-2 align-middle text-xs text-[var(--color-text)]"
        data-testid={`bss-tenants-cell-plan-${tenant.id}`}
      >
        {tenant.plan ?? '—'}
      </td>
      <td className="px-3 py-2 align-middle">
        <StatusPill status={tenant.status} tenantId={tenant.id} title={tenant.lastError} />
      </td>
      <td
        className="px-3 py-2 align-middle text-xs text-[var(--color-text)]"
        data-testid={`bss-tenants-cell-region-${tenant.id}`}
      >
        {tenant.region ?? '—'}
      </td>
      <td
        className="px-3 py-2 align-middle text-xs text-[var(--color-text-dim)]"
        data-testid={`bss-tenants-cell-provisioned-${tenant.id}`}
        title={tenant.createdAt}
      >
        {formatDate(tenant.createdAt)}
      </td>
      <td
        className="px-3 py-2 align-middle text-xs text-[var(--color-text-dim)]"
        data-testid={`bss-tenants-cell-last-active-${tenant.id}`}
        title={tenant.updatedAt}
      >
        {formatRelative(tenant.updatedAt)}
      </td>
    </tr>
  )
}

/** Status pill — tone palette mirrors JobsTable badges + ParentDomains
 *  flip-status badges (amber for in-flight, rose for failure, emerald
 *  for happy). These three families are the documented design-system
 *  exception to "tokens only". */
const STATUS_TONE: Record<TenantStatus, { label: string; className: string }> = {
  active: {
    label: 'Active',
    className: 'border-emerald-500/40 bg-emerald-500/10 text-emerald-300',
  },
  trial: {
    label: 'Trial',
    className: 'border-amber-500/40 bg-amber-500/10 text-amber-300',
  },
  provisioning: {
    label: 'Provisioning',
    className: 'border-amber-500/40 bg-amber-500/10 text-amber-300',
  },
  suspended: {
    label: 'Suspended',
    className: 'border-rose-500/40 bg-rose-500/10 text-rose-300',
  },
  failed: {
    label: 'Failed',
    className: 'border-rose-500/40 bg-rose-500/10 text-rose-300',
  },
  unknown: {
    label: 'Unknown',
    className:
      'border-[var(--color-border)] bg-[var(--color-bg-2)] text-[var(--color-text-dim)]',
  },
}

interface StatusPillProps {
  status: TenantStatus
  tenantId: string
  title?: string
}

function StatusPill({ status, tenantId, title }: StatusPillProps) {
  const tone = STATUS_TONE[status]
  return (
    <span
      data-testid={`bss-tenants-cell-status-${tenantId}`}
      data-status={status}
      title={title || tone.label}
      className={`inline-flex items-center rounded-full border px-2 py-0.5 text-[10px] font-semibold uppercase tracking-wide ${tone.className}`}
    >
      {tone.label}
    </span>
  )
}

/** formatDate — `2026-05-17` style for the Provisioned column. Returns
 *  "—" for the Go zero-time sentinel and for unparseable input. */
function formatDate(iso: string): string {
  if (!iso || iso.startsWith('0001')) return '—'
  const t = new Date(iso).getTime()
  if (!Number.isFinite(t) || t <= 0) return '—'
  return new Date(t).toLocaleDateString()
}

/** formatRelative — short "5m ago" style for the Last-active column.
 *  Falls back to the absolute date past 24h, mirroring JobsTable's
 *  formatRelative shape so the two surfaces read consistently. */
function formatRelative(iso: string): string {
  if (!iso || iso.startsWith('0001')) return '—'
  const t = new Date(iso).getTime()
  if (!Number.isFinite(t) || t <= 0) return '—'
  const dSec = Math.floor((Date.now() - t) / 1000)
  if (dSec < 5) return 'just now'
  if (dSec < 60) return `${dSec}s ago`
  if (dSec < 3600) return `${Math.floor(dSec / 60)}m ago`
  if (dSec < 86_400) return `${Math.floor(dSec / 3600)}h ago`
  return new Date(t).toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
}
