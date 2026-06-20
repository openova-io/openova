/**
 * ReconciliationTable — the List-tab view of the Reconciliation spine.
 *
 * A scientific, filterable, SORTABLE table on par with the Jobs view's
 * JobsTable (founder 2026-06-20: "it must be similar to jobs view … and it
 * must contain non-flux items as well"). Unlike the bounded-Flux DAG, this
 * table shows EVERY reconciler class — the Flux HelmReleases + Kustomizations
 * AND the declarative non-Flux reconcilers (cert-manager Certificates, CNPG
 * Clusters, External-Secrets ExternalSecrets, the Catalyst control-plane CRs).
 *
 * Layout, top-down (mirrors JobsTable):
 *   • Toolbar: a free-text search (matches name / kind / state / message /
 *     dependsOn) + per-column filter dropdowns (Class / Kind / State) + a
 *     live result count.
 *   • <table> with sortable column headers (Name / Kind / Class / State —
 *     click to toggle asc/desc; aria-sort reflects the active key). Default
 *     sort is attention-first by STATE_PRIORITY (Degraded → … → Reconciled),
 *     then id.
 *
 * Vocabulary is the Reconciliation set — never Success/Failed.
 */

import { useMemo, useState } from 'react'
import type { ReconState, ReconciliationNode } from '@/lib/reconciliation.api'
import {
  STATE_TONE,
  STATE_VALUES,
  FLUX_CLASS,
  nodeClass,
  matchReconNode,
  compareReconNodes,
  type ReconSortKey,
  type SortDir,
} from './reconciliation.shared'

/* ──────────────────────────────────────────────────────────────────
 * Component
 * ────────────────────────────────────────────────────────────────── */

export function ReconciliationTable({ nodes }: { nodes: ReconciliationNode[] }) {
  const [search, setSearch] = useState('')
  const [classFilter, setClassFilter] = useState('')
  const [kindFilter, setKindFilter] = useState('')
  const [stateFilter, setStateFilter] = useState<'' | ReconState>('')
  const [sortKey, setSortKey] = useState<ReconSortKey>('state')
  const [sortDir, setSortDir] = useState<SortDir>('asc')

  const classOptions = useMemo(() => {
    const present = new Set<string>()
    for (const n of nodes) present.add(nodeClass(n.kind))
    // Flux first (the meaningful grouping), then the rest sorted.
    const rest = [...present].filter((c) => c !== FLUX_CLASS).sort((a, b) => a.localeCompare(b))
    return present.has(FLUX_CLASS) ? [FLUX_CLASS, ...rest] : rest
  }, [nodes])

  const kindOptions = useMemo(() => {
    const present = new Set<string>()
    for (const n of nodes) present.add(n.kind)
    return [...present].sort((a, b) => a.localeCompare(b))
  }, [nodes])

  const stateOptions = useMemo(() => {
    const present = new Set<ReconState>()
    for (const n of nodes) present.add(n.state)
    return STATE_VALUES.filter((s) => present.has(s))
  }, [nodes])

  const visible = useMemo(() => {
    const filtered = nodes.filter((n) => {
      if (classFilter && nodeClass(n.kind) !== classFilter) return false
      if (kindFilter && n.kind !== kindFilter) return false
      if (stateFilter && n.state !== stateFilter) return false
      if (!matchReconNode(n, search)) return false
      return true
    })
    return [...filtered].sort((a, b) => compareReconNodes(a, b, sortKey, sortDir))
  }, [nodes, search, classFilter, kindFilter, stateFilter, sortKey, sortDir])

  const toggleSort = (key: ReconSortKey) => {
    if (sortKey === key) {
      setSortDir((d) => (d === 'asc' ? 'desc' : 'asc'))
    } else {
      setSortKey(key)
      setSortDir('asc')
    }
  }
  const ariaSort = (key: ReconSortKey): 'ascending' | 'descending' | 'none' =>
    sortKey === key ? (sortDir === 'asc' ? 'ascending' : 'descending') : 'none'
  const sortGlyph = (key: ReconSortKey) => (sortKey === key ? (sortDir === 'asc' ? '▲' : '▼') : '⇅')

  return (
    <div className="recon-table-wrap" data-testid="reconciliation-table">
      <style>{RECON_TABLE_CSS}</style>

      <div className="recon-toolbar" data-testid="reconciliation-table-toolbar">
        <div className="recon-search-wrap">
          <svg className="recon-search-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={2} aria-hidden>
            <circle cx="11" cy="11" r="7" />
            <path d="M21 21l-4.35-4.35" strokeLinecap="round" />
          </svg>
          <input
            type="search"
            placeholder="Search reconcilers by name, kind, state, dependency, or message…"
            className="recon-search-input"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            data-testid="reconciliation-table-search"
            aria-label="Search reconcilers"
          />
        </div>

        <div className="recon-filters">
          <label className="recon-filter-label">
            <span className="recon-filter-caption">Class</span>
            <select
              value={classFilter}
              onChange={(e) => setClassFilter(e.target.value)}
              className="recon-filter-select"
              data-testid="reconciliation-table-filter-class"
              aria-label="Filter by class"
            >
              <option value="">All</option>
              {classOptions.map((c) => (
                <option key={c} value={c}>
                  {c}
                </option>
              ))}
            </select>
          </label>

          <label className="recon-filter-label">
            <span className="recon-filter-caption">Kind</span>
            <select
              value={kindFilter}
              onChange={(e) => setKindFilter(e.target.value)}
              className="recon-filter-select"
              data-testid="reconciliation-table-filter-kind"
              aria-label="Filter by kind"
            >
              <option value="">All</option>
              {kindOptions.map((k) => (
                <option key={k} value={k}>
                  {k}
                </option>
              ))}
            </select>
          </label>

          <label className="recon-filter-label">
            <span className="recon-filter-caption">State</span>
            <select
              value={stateFilter}
              onChange={(e) => setStateFilter(e.target.value as '' | ReconState)}
              className="recon-filter-select"
              data-testid="reconciliation-table-filter-state"
              aria-label="Filter by state"
            >
              <option value="">All</option>
              {stateOptions.map((s) => (
                <option key={s} value={s}>
                  {s}
                </option>
              ))}
            </select>
          </label>

          <span className="recon-result-count" data-testid="reconciliation-table-count" aria-live="polite">
            {visible.length}/{nodes.length}
          </span>
        </div>
      </div>

      <div className="recon-table-scroll">
        <table className="recon-table" data-testid="reconciliation-table-grid">
          <thead>
            <tr>
              <SortableTh label="Name" col="name" ariaSort={ariaSort} sortGlyph={sortGlyph} onSort={toggleSort} />
              <SortableTh label="Kind" col="kind" ariaSort={ariaSort} sortGlyph={sortGlyph} onSort={toggleSort} />
              <SortableTh label="Class" col="class" ariaSort={ariaSort} sortGlyph={sortGlyph} onSort={toggleSort} />
              <SortableTh label="State" col="state" ariaSort={ariaSort} sortGlyph={sortGlyph} onSort={toggleSort} />
              <th data-col="deps">Depends on</th>
              <th data-col="message">Message</th>
            </tr>
          </thead>
          <tbody>
            {visible.length === 0 ? (
              <tr>
                <td colSpan={6} className="recon-empty" data-testid="reconciliation-table-empty">
                  No reconcilers match the current filters.
                </td>
              </tr>
            ) : (
              visible.map((n) => <ReconciliationRow key={n.id} node={n} />)
            )}
          </tbody>
        </table>
      </div>
    </div>
  )
}

function SortableTh({
  label,
  col,
  ariaSort,
  sortGlyph,
  onSort,
}: {
  label: string
  col: ReconSortKey
  ariaSort: (k: ReconSortKey) => 'ascending' | 'descending' | 'none'
  sortGlyph: (k: ReconSortKey) => string
  onSort: (k: ReconSortKey) => void
}) {
  return (
    <th data-col={col} aria-sort={ariaSort(col)}>
      <button
        type="button"
        className="recon-sort-btn"
        onClick={() => onSort(col)}
        data-testid={`reconciliation-table-sort-${col}`}
        aria-label={`Sort by ${label}`}
      >
        {label}
        <span className="recon-sort-glyph" aria-hidden>
          {sortGlyph(col)}
        </span>
      </button>
    </th>
  )
}

function ReconciliationRow({ node }: { node: ReconciliationNode }) {
  const tone = STATE_TONE[node.state]
  const cls = nodeClass(node.kind)
  return (
    <tr className="recon-row" data-testid="reconciliation-table-row" data-node-id={node.id} data-state={node.state} data-kind={node.kind}>
      <td className="recon-cell recon-cell-name">
        <span className="recon-name" title={node.id}>
          {node.label}
        </span>
      </td>
      <td className="recon-cell">
        <span className="recon-chip recon-chip-kind" title={node.kind}>
          {node.kind}
        </span>
      </td>
      <td className="recon-cell">
        <span className={`recon-chip recon-chip-class${cls === FLUX_CLASS ? ' recon-chip-flux' : ''}`} title={cls}>
          {cls}
        </span>
      </td>
      <td className="recon-cell">
        <span
          className="recon-badge"
          data-testid="reconciliation-table-state"
          style={{ background: tone.bg, borderColor: tone.border, color: tone.fg }}
        >
          <span className={tone.pulse ? 'recon-badge-pulse' : undefined} aria-hidden>
            {tone.glyph}
          </span>
          {node.state}
        </span>
      </td>
      <td className="recon-cell recon-cell-deps">
        {node.dependsOn && node.dependsOn.length > 0 ? (
          <div className="recon-chip-row">
            {node.dependsOn.map((d) => (
              <span key={d} className="recon-chip recon-chip-dep" title={d}>
                {d}
              </span>
            ))}
          </div>
        ) : (
          <span className="recon-empty-cell">—</span>
        )}
      </td>
      <td className="recon-cell recon-cell-message">
        {node.message ? (
          <span className="recon-message" title={node.message}>
            {node.message}
          </span>
        ) : (
          <span className="recon-empty-cell">—</span>
        )}
      </td>
    </tr>
  )
}

/* ──────────────────────────────────────────────────────────────────
 * Styles — mirror the Jobs table's tokens so the two scientific tables
 * read as one family (founder: "similar to jobs view").
 * ────────────────────────────────────────────────────────────────── */

const RECON_TABLE_CSS = `
.recon-table-wrap { width: 100%; }
.recon-toolbar { display: flex; flex-wrap: wrap; gap: 0.75rem; align-items: flex-end; margin-bottom: 0.75rem; }
.recon-search-wrap { position: relative; flex: 1 1 280px; min-width: 240px; max-width: 480px; }
.recon-search-icon { position: absolute; left: 0.6rem; top: 50%; transform: translateY(-50%); width: 14px; height: 14px; color: var(--color-text-dim); }
.recon-search-input {
  width: 100%; padding: 0.32rem 0.7rem 0.32rem 1.9rem;
  background: var(--color-surface); border: 1px solid var(--color-border);
  border-radius: 6px; color: var(--color-text); font-size: 0.82rem; outline: none;
  transition: border-color 0.15s ease;
}
.recon-search-input:focus { border-color: var(--color-accent); }
.recon-filters { display: flex; gap: 0.6rem; align-items: flex-end; flex-wrap: wrap; }
.recon-filter-label { display: inline-flex; flex-direction: column; gap: 0.15rem; }
.recon-filter-caption { font-size: 0.62rem; color: var(--color-text-dim); text-transform: uppercase; letter-spacing: 0.06em; }
.recon-filter-select {
  padding: 0.32rem 0.5rem; background: var(--color-surface); border: 1px solid var(--color-border);
  border-radius: 6px; color: var(--color-text); font-size: 0.82rem; cursor: pointer;
}
.recon-result-count { font-size: 0.72rem; color: var(--color-text-dim); align-self: flex-end; margin-left: auto; padding-bottom: 0.32rem; font-variant-numeric: tabular-nums; }

.recon-table-scroll { width: 100%; overflow-x: auto; border: 1px solid var(--color-border); border-radius: 12px; background: var(--color-surface); }
.recon-table { width: 100%; border-collapse: collapse; font-size: 0.85rem; }
.recon-table thead th {
  padding: 0; text-align: left;
  background: color-mix(in srgb, var(--color-border) 35%, transparent);
  border-bottom: 1px solid var(--color-border);
  color: var(--color-text-dim); font-size: 0.7rem; text-transform: uppercase;
  letter-spacing: 0.06em; font-weight: 600; white-space: nowrap;
}
.recon-table thead th[data-col="deps"], .recon-table thead th[data-col="message"] { padding: 0.55rem 0.8rem; }
.recon-sort-btn {
  display: inline-flex; align-items: center; gap: 0.35rem; width: 100%;
  padding: 0.55rem 0.8rem; background: transparent; border: none; cursor: pointer;
  color: inherit; font: inherit; text-transform: inherit; letter-spacing: inherit; font-weight: inherit;
}
.recon-sort-btn:hover { color: var(--color-text); }
.recon-sort-glyph { font-size: 0.6rem; opacity: 0.7; }
.recon-row { border-bottom: 1px solid var(--color-border); transition: background-color 0.12s ease; }
.recon-row:last-of-type { border-bottom: none; }
.recon-row:hover { background: color-mix(in srgb, var(--color-accent) 5%, transparent); }
.recon-cell { padding: 0.5rem 0.8rem; vertical-align: middle; color: var(--color-text); }
.recon-cell-name { min-width: 200px; max-width: 340px; }
.recon-cell-message { min-width: 200px; max-width: 420px; }
.recon-cell-deps { min-width: 140px; }
.recon-name { font-family: var(--font-mono, ui-monospace, monospace); color: var(--color-text-strong); font-weight: 500; }
.recon-message { color: var(--color-text-dim); font-size: 0.78rem; display: inline-block; max-width: 100%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.recon-empty-cell { color: var(--color-text-dim); font-size: 0.78rem; }
.recon-empty { padding: 2rem 1rem; text-align: center; color: var(--color-text-dim); font-size: 0.85rem; }

.recon-chip {
  display: inline-flex; align-items: center; padding: 0.12rem 0.55rem; border-radius: 999px;
  font-size: 0.7rem; font-weight: 500; font-family: var(--font-mono, ui-monospace, monospace);
  letter-spacing: 0.02em; white-space: nowrap; background: var(--color-surface);
  border: 1px solid var(--color-border); color: var(--color-text-dim);
  max-width: 14rem; overflow: hidden; text-overflow: ellipsis;
}
.recon-chip-kind { color: #C084FC; border-color: rgba(192,132,252,0.25); }
.recon-chip-class { color: var(--color-text-dim); }
.recon-chip-flux { color: #38BDF8; border-color: rgba(56,189,248,0.30); }
.recon-chip-dep { color: var(--color-text-dim); }
.recon-chip-row { display: flex; flex-wrap: wrap; gap: 0.25rem; }

.recon-badge {
  display: inline-flex; align-items: center; gap: 0.35rem; padding: 0.12rem 0.55rem;
  border-radius: 999px; font-size: 0.66rem; font-weight: 700; letter-spacing: 0.04em;
  text-transform: uppercase; white-space: nowrap; border: 1px solid transparent;
}
.recon-badge-pulse { animation: recon-badge-pulse 2s ease-in-out infinite; display: inline-block; }
@keyframes recon-badge-pulse { 0%,100% { opacity: 1; transform: scale(1); } 50% { opacity: 0.4; transform: scale(0.7); } }
`
