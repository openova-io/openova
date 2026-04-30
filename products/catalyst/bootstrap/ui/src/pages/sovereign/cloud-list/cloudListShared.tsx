/**
 * cloudListShared — shared scaffolding for the Cloud per-resource list
 * pages (P3 of #309). Every list page ships:
 *   • <CloudListHeader />  H1 + count badge + tagline + back-link
 *   • <CloudListToolbar /> search + filter pills (kind-appropriate)
 *   • <CloudListTable />   sortable columns, click-row → drawer
 *   • <CloudListDetail />  slide-in drawer rendered into an overlay
 *
 * The shape mirrors JobsTable.tsx (status colour tokens, monospace
 * chips, hover row tint) so the pages read as one consistent surface.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), every label,
 * column id, status string and CSS token comes from a typed input or
 * a CSS variable — there's no inlined provider name or hex colour.
 */

import { useEffect, type ReactNode } from 'react'
import { Link } from '@tanstack/react-router'
import type { TopologyStatus } from '@/lib/infrastructure.types'
import type { SortState } from './sortState'

/* ── Header ──────────────────────────────────────────────────────── */

interface CloudListHeaderProps {
  /** Plural resource name as user-visible — e.g. "Clusters". */
  title: string
  /** Short tagline beneath the title. */
  tagline: string
  /** Total resource count (badge in the title). */
  count: number
  /** Stable deployment id (powers the back-link target). */
  deploymentId: string
  /** Per-page testid prefix, e.g. "cloud-clusters" → "cloud-clusters-page". */
  testId: string
}

export function CloudListHeader({
  title,
  tagline,
  count,
  deploymentId,
  testId,
}: CloudListHeaderProps) {
  return (
    <header
      className="mb-3 flex items-start justify-between gap-4"
      data-testid={`${testId}-header`}
    >
      <div>
        <h1
          className="text-2xl font-bold text-[var(--color-text-strong)]"
          data-testid={`${testId}-title`}
        >
          {title}
          <span
            className="ml-2 inline-flex items-center rounded-full bg-[color-mix(in_srgb,var(--color-border)_60%,transparent)] px-2 py-0.5 align-middle text-xs font-semibold text-[var(--color-text-dim)]"
            data-testid={`${testId}-count`}
          >
            {count}
          </span>
        </h1>
        <p className="mt-1 text-sm text-[var(--color-text-dim)]">{tagline}</p>
      </div>
      <Link
        to={'/provision/$deploymentId/cloud' as never}
        params={{ deploymentId } as never}
        className="text-xs text-[var(--color-text-dim)] hover:text-[var(--color-text)] no-underline"
        data-testid={`${testId}-back`}
      >
        ← Back to Cloud
      </Link>
    </header>
  )
}

/* ── Status pill — same palette as JobsTable / CloudCompute ─────── */

export function StatusPill({ status }: { status: TopologyStatus }) {
  return (
    <span
      data-status={status}
      className="cloud-list-status"
    >
      {status}
    </span>
  )
}

/* ── Filter pill (status / region / etc) ────────────────────────── */

interface FilterPillsProps<T extends string> {
  label: string
  options: readonly T[]
  selected: T | ''
  onChange: (next: T | '') => void
  testId: string
}

export function FilterPills<T extends string>({
  label,
  options,
  selected,
  onChange,
  testId,
}: FilterPillsProps<T>) {
  return (
    <label className="cloud-list-filter-label">
      <span className="cloud-list-filter-caption">{label}</span>
      <select
        value={selected}
        onChange={(e) => onChange((e.target.value as T) || '')}
        className="cloud-list-filter-select"
        data-testid={testId}
        aria-label={`Filter by ${label.toLowerCase()}`}
      >
        <option value="">All</option>
        {options.map((opt) => (
          <option key={opt} value={opt}>
            {opt}
          </option>
        ))}
      </select>
    </label>
  )
}

/* ── Toolbar (search + filters + count) ─────────────────────────── */

interface CloudListToolbarProps {
  /** Per-page testid prefix. */
  testId: string
  /** Current search value (controlled). */
  search: string
  onSearchChange: (next: string) => void
  /** Visible / total count for the live-region announcement. */
  visibleCount: number
  totalCount: number
  /** Render slot for filter pills. */
  filters?: ReactNode
}

export function CloudListToolbar({
  testId,
  search,
  onSearchChange,
  visibleCount,
  totalCount,
  filters,
}: CloudListToolbarProps) {
  return (
    <div className="cloud-list-toolbar" data-testid={`${testId}-toolbar`}>
      <div className="cloud-list-search-wrap">
        <svg className="cloud-list-search-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={2} aria-hidden>
          <circle cx="11" cy="11" r="7" />
          <path d="M21 21l-4.35-4.35" strokeLinecap="round" />
        </svg>
        <input
          type="search"
          placeholder="Search by name…"
          className="cloud-list-search-input"
          value={search}
          onChange={(e) => onSearchChange(e.target.value)}
          data-testid={`${testId}-search`}
          aria-label="Search resources"
        />
      </div>
      <div className="cloud-list-filters">
        {filters}
        <span
          className="cloud-list-result-count"
          data-testid={`${testId}-result-count`}
          aria-live="polite"
        >
          {visibleCount}/{totalCount}
        </span>
      </div>
    </div>
  )
}

/* ── Sortable column header ─────────────────────────────────────── */

interface SortableTHProps {
  column: string
  label: string
  state: SortState
  onChange: (next: SortState) => void
  testId: string
}

export function SortableTH({ column, label, state, onChange, testId }: SortableTHProps) {
  const active = state.column === column
  return (
    <th
      data-col={column}
      className="cloud-list-th cloud-list-th-sortable"
      onClick={() => {
        if (!active) onChange({ column, dir: 'asc' })
        else onChange({ column, dir: state.dir === 'asc' ? 'desc' : 'asc' })
      }}
      data-testid={testId}
      data-sort-active={active ? 'true' : 'false'}
      data-sort-dir={active ? state.dir : ''}
    >
      <span className="cloud-list-th-content">
        {label}
        {active ? (
          <span className="cloud-list-th-arrow" aria-hidden>
            {state.dir === 'asc' ? '↑' : '↓'}
          </span>
        ) : null}
      </span>
    </th>
  )
}

/* ── Pagination ─────────────────────────────────────────────────── */

interface PaginationProps {
  page: number
  pageSize: number
  total: number
  onPageChange: (next: number) => void
  testId: string
}

export function Pagination({ page, pageSize, total, onPageChange, testId }: PaginationProps) {
  const pageCount = Math.max(1, Math.ceil(total / pageSize))
  if (pageCount <= 1) return null
  return (
    <div className="cloud-list-pagination" data-testid={`${testId}-pagination`}>
      <button
        type="button"
        onClick={() => onPageChange(Math.max(0, page - 1))}
        disabled={page <= 0}
        data-testid={`${testId}-pagination-prev`}
      >
        ← Prev
      </button>
      <span className="cloud-list-pagination-label" data-testid={`${testId}-pagination-page`}>
        Page {page + 1} of {pageCount}
      </span>
      <button
        type="button"
        onClick={() => onPageChange(Math.min(pageCount - 1, page + 1))}
        disabled={page >= pageCount - 1}
        data-testid={`${testId}-pagination-next`}
      >
        Next →
      </button>
    </div>
  )
}

/* ── Detail drawer ──────────────────────────────────────────────── */

interface CloudListDetailDrawerProps {
  /** When non-null the drawer is open; render a controlled component. */
  open: boolean
  onClose: () => void
  title: string
  testId: string
  children: ReactNode
}

export function CloudListDetailDrawer({
  open,
  onClose,
  title,
  testId,
  children,
}: CloudListDetailDrawerProps) {
  // Esc closes the drawer.
  useEffect(() => {
    if (!open) return
    function onKey(ev: KeyboardEvent) {
      if (ev.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [open, onClose])

  if (!open) return null
  return (
    <div
      className="cloud-list-drawer-backdrop"
      data-testid={`${testId}-backdrop`}
      onClick={onClose}
      role="presentation"
    >
      <aside
        className="cloud-list-drawer"
        data-testid={testId}
        role="dialog"
        aria-modal="true"
        aria-label={title}
        onClick={(e) => e.stopPropagation()}
      >
        <header className="cloud-list-drawer-header">
          <h2 className="cloud-list-drawer-title">{title}</h2>
          <button
            type="button"
            onClick={onClose}
            className="cloud-list-drawer-close"
            data-testid={`${testId}-close`}
            aria-label="Close detail"
          >
            ×
          </button>
        </header>
        <div className="cloud-list-drawer-body" data-testid={`${testId}-body`}>
          {children}
        </div>
      </aside>
    </div>
  )
}

/* ── Detail rows (key/value pairs) ──────────────────────────────── */

interface DetailRowProps {
  label: string
  value: ReactNode
  mono?: boolean
  testId?: string
}

export function DetailRow({ label, value, mono = false, testId }: DetailRowProps) {
  return (
    <div className="cloud-list-detail-row" data-testid={testId}>
      <span className="cloud-list-detail-row-label">{label}</span>
      <span className={`cloud-list-detail-row-value ${mono ? 'cloud-list-detail-row-mono' : ''}`}>
        {value}
      </span>
    </div>
  )
}

/* ── Empty state ────────────────────────────────────────────────── */

interface EmptyStateProps {
  testId: string
  title: string
  body: ReactNode
}

export function EmptyState({ testId, title, body }: EmptyStateProps) {
  return (
    <div className="cloud-list-empty" data-testid={testId}>
      <p className="cloud-list-empty-title">{title}</p>
      <p className="cloud-list-empty-body">{body}</p>
    </div>
  )
}

