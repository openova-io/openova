/**
 * OrdersPage — /billing/orders, native React (issue #4196).
 *
 * Wave 6 PR 3 (Option B step 2): replaces the BssSectionShell iframe
 * with a native table that mirrors JobsTable's shape (toolbar →
 * filter row → scrollable table → row click → drill-in).
 *
 * Inherits the parent app's design system per the Wave 6 brief:
 *   • PortalShell wrapper (same as JobsPage/AppsPage/SettingsPage)
 *   • Header back-link via `headerSlotLeft` (mirrors BssSectionShell)
 *   • Design tokens only — no hex, no bespoke Tailwind colours; the
 *     "API pending" pill picks up the amber-* family verbatim from
 *     BssLandingPage / SettingsPage, and the failed-status pill uses
 *     the rose-* family used by SettingsPage's Danger zone.
 *   • Empty + loading + error states match JobsTable / ParentDomainsPage.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #1 (waterfall — first paint is the
 * full surface), the table + toolbar render from mount; rows flip in
 * once `getOrders()` resolves. Per #4 (never hardcode), the status /
 * age catalogues live in named constants so adding a new status is a
 * one-line change. Per #10 (credential hygiene), no token / secret is
 * read; the rollup is a list of opaque order ids.
 */

import { useMemo, useState } from 'react'
import { Link } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'
import { PortalShell } from '../PortalShell'
import { BillingSectionNav } from './BillingSectionNav'
import { useDeploymentEvents } from '../useDeploymentEvents'
import { getOrders, type Order, type OrderStatus, type OrdersResponse } from '@/lib/bss.api'

const QUERY_STALE_MS = 30_000

/* ── Filter catalogues ──────────────────────────────────────────────
 *
 * Single source of truth for the toolbar dropdowns. Adding a new
 * status or age bucket is a one-line change here; the option list +
 * predicate map below pick it up automatically.
 */

const STATUS_OPTIONS: readonly { value: OrderStatus; label: string }[] = [
  { value: 'pending', label: 'Pending' },
  { value: 'completed', label: 'Completed' },
  { value: 'failed', label: 'Failed' },
  { value: 'cancelled', label: 'Cancelled' },
]

type AgeBucket = 'today' | 'week' | 'month'

const AGE_OPTIONS: readonly { value: AgeBucket; label: string; days: number }[] = [
  { value: 'today', label: 'Today', days: 1 },
  { value: 'week', label: 'Last 7 days', days: 7 },
  { value: 'month', label: 'Last 30 days', days: 30 },
]

export interface OrdersPageProps {
  /** Test seam — disables the live SSE attach. */
  disableStream?: boolean
  /** Test seam — bypass the React Query fetcher with synthetic data. */
  initialOrdersOverride?: OrdersResponse
}

export function OrdersPage({
  disableStream = false,
  initialOrdersOverride,
}: OrdersPageProps = {}) {
  const { deploymentId: resolvedId } = useResolvedDeploymentId()
  const deploymentId = resolvedId ?? ''

  const { snapshot } = useDeploymentEvents({
    deploymentId,
    applicationIds: [],
    disableStream,
  })
  const sovereignFQDN =
    snapshot?.sovereignFQDN ?? snapshot?.result?.sovereignFQDN ?? null

  const query = useQuery<OrdersResponse>({
    queryKey: ['bss-orders', deploymentId],
    queryFn: getOrders,
    staleTime: QUERY_STALE_MS,
    enabled: !initialOrdersOverride,
    placeholderData: (prev) => prev,
  })

  const data = initialOrdersOverride ?? query.data ?? null
  const pendingApi = data?.pendingApi ?? true
  const orders = data?.orders ?? []
  const loading = !initialOrdersOverride && query.isLoading

  /* ── Toolbar state ──────────────────────────────────────────────── */

  const [search, setSearch] = useState<string>('')
  const [statusFilter, setStatusFilter] = useState<'' | OrderStatus>('')
  const [ageFilter, setAgeFilter] = useState<'' | AgeBucket>('')

  const visibleOrders = useMemo<Order[]>(() => {
    const now = Date.now()
    const filtered = orders.filter((o) => {
      if (statusFilter && o.status !== statusFilter) return false
      if (ageFilter) {
        const bucket = AGE_OPTIONS.find((a) => a.value === ageFilter)
        if (bucket && o.createdAt) {
          const t = new Date(o.createdAt).getTime()
          if (Number.isFinite(t)) {
            const ageMs = now - t
            if (ageMs > bucket.days * DAY_MS) return false
          }
        }
      }
      if (search.trim() && !matchOrder(o, search)) return false
      return true
    })
    return [...filtered].sort(compareOrders)
  }, [orders, search, statusFilter, ageFilter])

  return (
    <PortalShell
      deploymentId={deploymentId}
      sovereignFQDN={sovereignFQDN}
      pageTitle="Billing — Orders"
      headerSlotLeft={<BillingSectionNav />}
    >
      <style>{ORDERS_TABLE_CSS}</style>

      <div className="mx-auto max-w-7xl" data-testid="bss-orders-page">
        <header className="mb-4 flex flex-wrap items-start justify-between gap-3">
          <div>
            <p className="text-sm text-[var(--color-text-dim)]">
              Provisioning and billing orders from the marketplace. Click a row
              to open the order detail.
            </p>
          </div>
          {pendingApi ? (
            <span
              data-testid="bss-orders-pending-api"
              className="rounded-full border border-amber-500/40 bg-amber-500/10 px-2 py-0.5 text-[10px] font-medium uppercase tracking-wide text-amber-300"
              title="Backend API not yet wired — display only"
            >
              API pending
            </span>
          ) : null}
        </header>

        <div className="orders-toolbar" data-testid="bss-orders-toolbar">
          <div className="orders-search-wrap">
            <svg
              className="orders-search-icon"
              viewBox="0 0 24 24"
              fill="none"
              stroke="currentColor"
              strokeWidth={2}
              aria-hidden
            >
              <circle cx="11" cy="11" r="7" />
              <path d="M21 21l-4.35-4.35" strokeLinecap="round" />
            </svg>
            <input
              type="search"
              placeholder="Search orders by id, organization, or product…"
              className="orders-search-input"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              data-testid="bss-orders-search"
              aria-label="Search orders"
            />
          </div>

          <div className="orders-filters">
            <label className="orders-filter-label">
              <span className="orders-filter-caption">Status</span>
              <select
                value={statusFilter}
                onChange={(e) => setStatusFilter(e.target.value as '' | OrderStatus)}
                className="orders-filter-select"
                data-testid="bss-orders-filter-status"
                aria-label="Filter by status"
              >
                <option value="">All</option>
                {STATUS_OPTIONS.map((s) => (
                  <option key={s.value} value={s.value}>
                    {s.label}
                  </option>
                ))}
              </select>
            </label>

            <label className="orders-filter-label">
              <span className="orders-filter-caption">Age</span>
              <select
                value={ageFilter}
                onChange={(e) => setAgeFilter(e.target.value as '' | AgeBucket)}
                className="orders-filter-select"
                data-testid="bss-orders-filter-age"
                aria-label="Filter by age"
              >
                <option value="">All</option>
                {AGE_OPTIONS.map((a) => (
                  <option key={a.value} value={a.value}>
                    {a.label}
                  </option>
                ))}
              </select>
            </label>

            <span
              className="orders-result-count"
              data-testid="bss-orders-result-count"
              aria-live="polite"
            >
              {visibleOrders.length}/{orders.length}
            </span>
          </div>
        </div>

        <div className="orders-table-scroll">
          <table className="orders-table" data-testid="bss-orders-table">
            <thead>
              <tr>
                <th data-col="id">Order ID</th>
                <th data-col="org">Organization</th>
                <th data-col="product">Product</th>
                <th data-col="status">Status</th>
                <th data-col="created">Created</th>
                <th data-col="updated">Last update</th>
                <th data-col="total">Total</th>
              </tr>
            </thead>
            <tbody>
              {loading ? (
                <tr>
                  <td
                    colSpan={7}
                    className="orders-empty"
                    data-testid="bss-orders-table-loading"
                  >
                    Loading orders…
                  </td>
                </tr>
              ) : visibleOrders.length === 0 ? (
                <tr>
                  <td
                    colSpan={7}
                    className="orders-empty"
                    data-testid="bss-orders-table-empty"
                  >
                    {orders.length === 0
                      ? 'No orders yet. Organization orders from the marketplace will appear here.'
                      : 'No orders match the current filters.'}
                  </td>
                </tr>
              ) : (
                visibleOrders.map((o) => <OrderRow key={o.id} order={o} />)
              )}
            </tbody>
          </table>
        </div>
      </div>
    </PortalShell>
  )
}

/* ── Row ─────────────────────────────────────────────────────────── */

function OrderRow({ order }: { order: Order }) {
  const created = formatRelative(order.createdAt)
  const updated = formatRelative(order.updatedAt)
  return (
    <tr
      className="orders-row"
      data-testid={`bss-orders-row-${order.id}`}
      data-status={order.status}
    >
      <td className="orders-cell orders-cell-id">
        <Link
          to={`/billing/orders/${order.id}` as never}
          className="orders-row-link"
          data-testid={`bss-orders-row-link-${order.id}`}
        >
          {order.id}
        </Link>
      </td>
      <td className="orders-cell orders-cell-org">
        {order.org ? (
          order.org
        ) : (
          <span className="orders-empty-cell">—</span>
        )}
      </td>
      <td className="orders-cell orders-cell-product">
        {order.product ? (
          order.product
        ) : (
          <span className="orders-empty-cell">—</span>
        )}
      </td>
      <td className="orders-cell orders-cell-status">
        <StatusPill status={order.status} orderId={order.id} />
      </td>
      <td className="orders-cell orders-cell-created" title={created.absolute}>
        <span data-testid={`bss-orders-cell-created-${order.id}`}>
          {created.display}
        </span>
      </td>
      <td className="orders-cell orders-cell-updated" title={updated.absolute}>
        <span data-testid={`bss-orders-cell-updated-${order.id}`}>
          {updated.display}
        </span>
      </td>
      <td className="orders-cell orders-cell-total">
        <span
          className="tabular-nums"
          data-testid={`bss-orders-cell-total-${order.id}`}
        >
          {formatCurrency(order.totalCents, order.currency)}
        </span>
      </td>
    </tr>
  )
}

function StatusPill({ status, orderId }: { status: OrderStatus; orderId: string }) {
  return (
    <span
      className={`orders-pill orders-pill-${status}`}
      data-testid={`bss-orders-cell-status-${orderId}`}
      data-status={status}
    >
      {STATUS_LABEL[status]}
    </span>
  )
}

const STATUS_LABEL: Record<OrderStatus, string> = {
  pending: 'Pending',
  completed: 'Completed',
  failed: 'Failed',
  cancelled: 'Cancelled',
}

/* ── Helpers ─────────────────────────────────────────────────────── */

const DAY_MS = 24 * 60 * 60 * 1000

/**
 * STATUS_PRIORITY — sort pending first (operator wants in-flight work
 * surfaced), then failed (action needed), then completed, then
 * cancelled. Mirrors JobsTable's "running > pending > succeeded >
 * failed" ordering philosophy (most-actionable on top).
 */
const STATUS_PRIORITY: Record<OrderStatus, number> = {
  pending: 0,
  failed: 1,
  completed: 2,
  cancelled: 3,
}

function compareOrders(a: Order, b: Order): number {
  const pa = STATUS_PRIORITY[a.status] ?? 99
  const pb = STATUS_PRIORITY[b.status] ?? 99
  if (pa !== pb) return pa - pb
  const ta = a.createdAt ? new Date(a.createdAt).getTime() : 0
  const tb = b.createdAt ? new Date(b.createdAt).getTime() : 0
  if (ta !== tb) return tb - ta
  return a.id.localeCompare(b.id)
}

function matchOrder(o: Order, query: string): boolean {
  const q = query.toLowerCase()
  if (o.id.toLowerCase().includes(q)) return true
  if (o.org.toLowerCase().includes(q)) return true
  if (o.product.toLowerCase().includes(q)) return true
  if (o.status.toLowerCase().includes(q)) return true
  return false
}

function formatRelative(iso: string): { display: string; absolute: string } {
  if (!iso) return { display: '—', absolute: '' }
  const t = new Date(iso).getTime()
  if (!Number.isFinite(t) || t <= 0) return { display: '—', absolute: '' }
  const now = Date.now()
  const dSec = Math.floor((now - t) / 1000)
  const display =
    dSec < 5
      ? 'just now'
      : dSec < 60
        ? `${dSec}s ago`
        : dSec < 3600
          ? `${Math.floor(dSec / 60)}m ago`
          : dSec < 86_400
            ? `${Math.floor(dSec / 3600)}h ago`
            : new Date(t).toLocaleDateString(undefined, {
                month: 'short',
                day: 'numeric',
              })
  const absolute = new Date(t).toLocaleString()
  return { display, absolute }
}

/**
 * MINOR_UNITS_PER_MAJOR — the integer minor-unit divisor per ISO-4217
 * currency. The Sovereign billing service is OMR-denominated and emits
 * amounts in BAISA (1/1000 OMR — three minor digits per ISO-4217), which
 * the catalyst-api bridge (org_orders.go) carries verbatim in the generic
 * `totalCents` minor-unit field. A blanket `/100` (the cents convention)
 * therefore rendered every OMR order 10× inflated (a 9-OMR order showed
 * as "OMR 90.00"). Look the scale up by currency so OMR/BHD/KWD divide by
 * 1000 and dollar/euro-style codes keep /100 — mirrors RevenuePage's
 * BAISA_PER_OMR fix (#4274). The currency itself drives Intl's fraction
 * digits, so no explicit maximumFractionDigits is needed.
 */
const MINOR_UNITS_PER_MAJOR: Record<string, number> = {
  OMR: 1000,
  BHD: 1000,
  KWD: 1000,
}

function minorUnitDivisor(currency: string): number {
  return MINOR_UNITS_PER_MAJOR[currency.toUpperCase()] ?? 100
}

function formatCurrency(minor: number, currency: string): string {
  const value = minor / minorUnitDivisor(currency)
  try {
    return new Intl.NumberFormat(undefined, {
      style: 'currency',
      currency,
    }).format(value)
  } catch {
    // Unknown ISO-4217 code → fall back to a plain numeric format with
    // the raw code prefix so the cell still renders something useful.
    return `${currency} ${value.toFixed(2)}`
  }
}

/* ── Styles ──────────────────────────────────────────────────────── *
 *
 * Mirrors JobsTable's `.jobs-table-*` CSS verbatim, just prefixed
 * `orders-*` so the two coexist without cascade collisions. All
 * colour values flow through design tokens; the status pills use the
 * same token-driven semantics SettingsPage and JobsTable already
 * consume (success/accent/danger families via color-mix), with
 * amber-* / rose-* as the brief's explicit exceptions.
 */
const ORDERS_TABLE_CSS = `
.orders-toolbar {
  display: flex;
  flex-wrap: wrap;
  gap: 0.75rem;
  align-items: flex-end;
  margin-bottom: 0.75rem;
}

.orders-search-wrap {
  position: relative;
  flex: 1 1 280px;
  min-width: 240px;
  max-width: 480px;
}
.orders-search-icon {
  position: absolute;
  left: 0.6rem;
  top: 50%;
  transform: translateY(-50%);
  width: 14px;
  height: 14px;
  color: var(--color-text-dim);
}
.orders-search-input {
  width: 100%;
  padding: 0.32rem 0.7rem 0.32rem 1.9rem;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: 6px;
  color: var(--color-text);
  font-size: 0.82rem;
  outline: none;
  transition: border-color 0.15s ease;
}
.orders-search-input:focus {
  border-color: var(--color-accent);
}

.orders-filters {
  display: flex;
  gap: 0.6rem;
  align-items: flex-end;
  flex-wrap: wrap;
}
.orders-filter-label {
  display: inline-flex;
  flex-direction: column;
  gap: 0.15rem;
}
.orders-filter-caption {
  font-size: 0.62rem;
  color: var(--color-text-dim);
  text-transform: uppercase;
  letter-spacing: 0.06em;
}
.orders-filter-select {
  padding: 0.32rem 0.5rem;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: 6px;
  color: var(--color-text);
  font-size: 0.82rem;
  cursor: pointer;
}
.orders-result-count {
  font-size: 0.72rem;
  color: var(--color-text-dim);
  align-self: flex-end;
  margin-left: auto;
  padding-bottom: 0.32rem;
  font-variant-numeric: tabular-nums;
}

.orders-table-scroll {
  width: 100%;
  overflow-x: auto;
  border: 1px solid var(--color-border);
  border-radius: 12px;
  background: var(--color-surface);
}
.orders-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 0.85rem;
}
.orders-table thead th {
  padding: 0.55rem 0.8rem;
  text-align: left;
  background: color-mix(in srgb, var(--color-border) 35%, transparent);
  border-bottom: 1px solid var(--color-border);
  color: var(--color-text-dim);
  font-size: 0.7rem;
  text-transform: uppercase;
  letter-spacing: 0.06em;
  font-weight: 600;
  white-space: nowrap;
}
.orders-row {
  border-bottom: 1px solid var(--color-border);
  transition: background-color 0.12s ease;
}
.orders-row:last-of-type {
  border-bottom: none;
}
.orders-row:hover {
  background: color-mix(in srgb, var(--color-accent) 5%, transparent);
}
.orders-cell {
  padding: 0.55rem 0.8rem;
  vertical-align: middle;
  color: var(--color-text);
}
.orders-cell-id { min-width: 200px; font-family: var(--font-mono, ui-monospace, monospace); }
.orders-cell-org { min-width: 160px; }
.orders-cell-product { min-width: 160px; }
.orders-cell-total { text-align: right; min-width: 100px; }
.orders-row-link {
  display: inline-block;
  text-decoration: none;
  color: var(--color-text-strong);
  font-weight: 500;
  cursor: pointer;
}
.orders-row-link:hover {
  color: var(--color-accent);
}
.orders-empty-cell {
  color: var(--color-text-dim);
  font-size: 0.78rem;
}
.orders-empty {
  padding: 2rem 1rem;
  text-align: center;
  color: var(--color-text-dim);
  font-size: 0.85rem;
}

.orders-pill {
  display: inline-flex;
  align-items: center;
  padding: 0.12rem 0.55rem;
  border-radius: 999px;
  font-size: 0.66rem;
  font-weight: 700;
  letter-spacing: 0.04em;
  text-transform: uppercase;
  white-space: nowrap;
  border: 1px solid transparent;
}
.orders-pill-pending {
  background: color-mix(in srgb, var(--color-accent) 12%, transparent);
  color: var(--color-accent);
  border-color: color-mix(in srgb, var(--color-accent) 35%, transparent);
}
.orders-pill-completed {
  background: color-mix(in srgb, var(--color-success) 14%, transparent);
  color: var(--color-success);
  border-color: color-mix(in srgb, var(--color-success) 35%, transparent);
}
.orders-pill-failed {
  background: color-mix(in srgb, var(--color-error) 14%, transparent);
  color: var(--color-error);
  border-color: color-mix(in srgb, var(--color-error) 35%, transparent);
}
.orders-pill-cancelled {
  background: color-mix(in srgb, var(--color-text-dim) 14%, transparent);
  color: var(--color-text-dim);
  border-color: color-mix(in srgb, var(--color-text-dim) 30%, transparent);
}
`
