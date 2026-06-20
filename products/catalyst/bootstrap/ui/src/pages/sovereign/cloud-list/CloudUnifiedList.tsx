/**
 * CloudUnifiedList — the LIST rendering of the ONE Cloud dataset (#3970).
 *
 * The list and the graph are TWO renderings of the SAME dataset. This
 * table derives its rows from the same merged `GraphNode[]` the graph
 * canvas uses (cloud + k8s + reconcilers, via `buildCloudGraph`) and
 * filters them by the SAME active chip-set (the shared lens state). So a
 * reconciler that shows in the graph — HelmRelease / Kustomization /
 * Certificate / ExternalSecret / Database (CNPG) / the Catalyst CRs
 * (Application / Environment / Organization / Continuum / UserAccess) —
 * shows here too, and picking a lens filters BOTH identically.
 *
 * Columns: NAME · KIND · FAMILY · STATUS (the locked grammar reads the
 * same channels — family = border colour, status = fill).
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 — every label/colour comes from the
 * type/family/status maps in widgets/architecture-graph/types.ts.
 */

import { useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useCloud } from '../CloudPage'
import { fetchReconciliationDAG, type ReconciliationDAG } from '@/lib/reconciliation.api'
import { buildCloudGraph } from '@/widgets/architecture-graph/cloudGraphData'
import type { K8sSnapshot } from '@/widgets/architecture-graph'
import { LENSES, LENS_ORDER, type LensId } from '@/widgets/architecture-graph/presets'
import {
  useCloudLensOptional,
  useCloudLensState,
} from '@/widgets/architecture-graph/useCloudLens'
import {
  ALL_NODE_TYPES,
  CATEGORY_LABEL,
  FAMILY_BORDER,
  FAMILY_LABEL,
  NODE_CATEGORY,
  NODE_FAMILY,
  NODE_FILL,
  STATUS_FILL,
  type ArchNodeType,
  type ArchStatus,
} from '@/widgets/architecture-graph'

const RECON_POLL_MS = 4_000

/** Human-readable status labels for the STATUS column. */
const STATUS_LABEL: Record<ArchStatus, string> = {
  healthy: 'Reconciled',
  reconciling: 'Reconciling',
  drifted: 'Drifted',
  degraded: 'Degraded',
  failed: 'Failed',
  suspended: 'Suspended',
  unknown: 'Unknown',
}

export function CloudUnifiedList() {
  const { deploymentId, data, isLoading, k8sSnapshot, k8sRevision } = useCloud()
  const lens = useCloudLens()

  // Reconciler set — same fetch/cadence as the graph (Architecture.tsx).
  // React Query dedupes on the shared queryKey so this doesn't double-poll.
  const reconQuery = useQuery<ReconciliationDAG>({
    queryKey: ['reconciliation-dag', deploymentId],
    queryFn: () => fetchReconciliationDAG(deploymentId),
    enabled: !!deploymentId,
    refetchInterval: RECON_POLL_MS,
    staleTime: RECON_POLL_MS,
    placeholderData: (prev) => prev,
  })

  // THE ONE dataset — identical to the graph's allNodes.
  const allNodes = useMemo(
    () =>
      buildCloudGraph(
        data,
        (k8sSnapshot as unknown as K8sSnapshot | null) ?? null,
        reconQuery.data?.nodes ?? null,
      ).nodes,
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [data, k8sRevision, reconQuery.data],
  )

  // Rows = the dataset filtered by the SAME active chip-set as the graph,
  // sorted by kind then name so the table reads stably.
  const rows = useMemo(() => {
    const out = allNodes.filter((n) => lens.activeTypes.has(n.type))
    out.sort((a, b) =>
      a.type === b.type ? a.label.localeCompare(b.label) : a.type.localeCompare(b.type),
    )
    return out
  }, [allNodes, lens.activeTypes])

  // Per-type totals over the WHOLE dataset (for the +Add menu counts).
  const typeTotals = useMemo(() => {
    const m = new Map<ArchNodeType, number>()
    for (const n of allNodes) m.set(n.type, (m.get(n.type) ?? 0) + 1)
    return m
  }, [allNodes])

  return (
    <div data-testid="cloud-unified-list">
      <style>{CLOUD_UNIFIED_LIST_CSS}</style>

      {/* Lens dropdown + chip strip — shares the SAME lens state as the
          graph (CloudLensProvider), so switching here moves the graph too
          and vice-versa. */}
      <div className="cloud-ul-controls" data-testid="cloud-unified-list-controls">
        <label className="cloud-ul-lens-label" htmlFor="cloud-ul-lens">
          Lens
          <select
            id="cloud-ul-lens"
            data-testid="cloud-unified-list-lens"
            value={lens.lensId}
            onChange={(e) => lens.selectLens(e.target.value as LensId)}
            className="cloud-ul-lens-select"
          >
            <optgroup label="Domain">
              {LENS_ORDER.filter((id) => LENSES[id].group === 'domain').map((id) => (
                <option key={id} value={id}>
                  {LENSES[id].label}
                </option>
              ))}
            </optgroup>
            <optgroup label="Family">
              {LENS_ORDER.filter((id) => LENSES[id].group === 'family').map((id) => (
                <option key={id} value={id}>
                  {LENSES[id].label}
                </option>
              ))}
            </optgroup>
          </select>
          {lens.isCustom && (
            <span data-testid="cloud-unified-list-lens-custom" className="cloud-ul-custom">
              Custom
            </span>
          )}
        </label>

        <div className="cloud-ul-chips" data-testid="cloud-unified-list-chips" role="list">
          {ALL_NODE_TYPES.filter((t) => lens.activeTypes.has(t)).map((t) => (
            <span
              key={t}
              role="listitem"
              data-testid={`cloud-unified-list-chip-${t}`}
              className="cloud-ul-chip"
              style={{ borderColor: FAMILY_BORDER[NODE_FAMILY[t]] }}
            >
              <span className="cloud-ul-chip-dot" style={{ background: NODE_FILL[t] }} aria-hidden />
              {t}
              <span className="cloud-ul-chip-count">{typeTotals.get(t) ?? 0}</span>
              <button
                type="button"
                aria-label={`Remove ${t} chip`}
                data-testid={`cloud-unified-list-chip-${t}-remove`}
                className="cloud-ul-chip-x"
                onClick={() => lens.removeChip(t)}
              >
                ×
              </button>
            </span>
          ))}
          <AddChipMenu
            inactiveTypes={ALL_NODE_TYPES.filter((t) => !lens.activeTypes.has(t))}
            typeTotals={typeTotals}
            onAdd={lens.addChip}
          />
        </div>
      </div>

      {/* The table — one row per resource in the active chip-set. */}
      <div className="cloud-ul-table-wrap">
        <table className="cloud-ul-table" data-testid="cloud-unified-list-table">
          <thead>
            <tr>
              <th>Name</th>
              <th>Kind</th>
              <th>Family</th>
              <th>Status</th>
            </tr>
          </thead>
          <tbody>
            {isLoading && !data && rows.length === 0 ? (
              <tr>
                <td colSpan={4} className="cloud-ul-empty" data-testid="cloud-unified-list-loading">
                  Loading resources…
                </td>
              </tr>
            ) : rows.length === 0 ? (
              <tr>
                <td colSpan={4} className="cloud-ul-empty" data-testid="cloud-unified-list-empty">
                  No resources match the {LENSES[lens.lensId].label} lens.
                </td>
              </tr>
            ) : (
              rows.map((n) => {
                const fam = NODE_FAMILY[n.type]
                const status = n.status ?? 'unknown'
                return (
                  <tr key={n.id} data-testid={`cloud-unified-list-row-${n.type}`} data-kind={n.type}>
                    <td className="cloud-ul-name">
                      <span className="cloud-ul-name-label">{n.label}</span>
                      {n.sublabel && <span className="cloud-ul-name-sub">{n.sublabel}</span>}
                    </td>
                    <td>
                      <span
                        className="cloud-ul-kind"
                        title={`${CATEGORY_LABEL[NODE_CATEGORY[n.type]]}`}
                      >
                        {n.type}
                      </span>
                    </td>
                    <td>
                      <span
                        className="cloud-ul-family"
                        style={{ color: FAMILY_BORDER[fam] }}
                        data-family={fam}
                      >
                        {FAMILY_LABEL[fam]}
                      </span>
                    </td>
                    <td>
                      <span className="cloud-ul-status" data-status={status}>
                        <span
                          className="cloud-ul-status-dot"
                          style={{ background: STATUS_FILL[status] }}
                          aria-hidden
                        />
                        {STATUS_LABEL[status]}
                      </span>
                    </td>
                  </tr>
                )
              })
            )}
          </tbody>
        </table>
      </div>
    </div>
  )
}

/* ── + Add chip menu ─────────────────────────────────────────────── */

interface AddChipMenuProps {
  inactiveTypes: ArchNodeType[]
  typeTotals: Map<ArchNodeType, number>
  onAdd: (t: ArchNodeType) => void
}

function AddChipMenu({ inactiveTypes, typeTotals, onAdd }: AddChipMenuProps) {
  const disabled = inactiveTypes.length === 0
  return (
    <details className="cloud-ul-add" data-testid="cloud-unified-list-add">
      <summary
        className="cloud-ul-add-summary"
        data-testid="cloud-unified-list-add-button"
        aria-disabled={disabled}
      >
        + Add
      </summary>
      {!disabled && (
        <div className="cloud-ul-add-menu" data-testid="cloud-unified-list-add-menu">
          {inactiveTypes.map((t) => (
            <button
              key={t}
              type="button"
              data-testid={`cloud-unified-list-add-item-${t}`}
              className="cloud-ul-add-item"
              onClick={(e) => {
                onAdd(t)
                // Close the <details> popover after a pick.
                ;(e.currentTarget.closest('details') as HTMLDetailsElement | null)?.removeAttribute(
                  'open',
                )
              }}
            >
              <span
                className="cloud-ul-chip-dot"
                style={{ background: NODE_FILL[t] }}
                aria-hidden
              />
              {t}
              <span className="cloud-ul-chip-count">{typeTotals.get(t) ?? 0}</span>
            </button>
          ))}
        </div>
      )}
    </details>
  )
}

/* ── Shared lens hook (non-throwing) ─────────────────────────────── */

/**
 * useCloudLens — the list always runs inside CloudLensProvider, but fall
 * back to a private instance for isolated tests so the component never
 * throws when mounted standalone.
 */
function useCloudLens() {
  const shared = useCloudLensOptional()
  const priv = useCloudLensState()
  return shared ?? priv
}

const CLOUD_UNIFIED_LIST_CSS = `
.cloud-ul-controls {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 0.6rem;
  margin-bottom: 0.6rem;
  padding: 0.5rem 0.65rem;
  border: 1px solid var(--color-border);
  border-radius: 8px;
  background: var(--color-bg-2);
}
.cloud-ul-lens-label {
  display: inline-flex;
  align-items: center;
  gap: 0.4rem;
  font-size: 0.75rem;
  color: var(--color-text-dim);
}
.cloud-ul-lens-select {
  border: 1px solid var(--color-border);
  border-radius: 6px;
  background: var(--color-bg);
  color: var(--color-text);
  padding: 0.2rem 0.4rem;
  font-size: 0.75rem;
}
.cloud-ul-custom {
  border: 1px solid var(--color-border);
  border-radius: 6px;
  background: var(--color-bg);
  color: var(--color-text-dim);
  font-size: 0.62rem;
  font-weight: 600;
  padding: 0.06rem 0.3rem;
}
.cloud-ul-chips {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 0.35rem;
}
.cloud-ul-chip {
  display: inline-flex;
  align-items: center;
  gap: 0.3rem;
  border: 1px solid var(--color-border);
  border-radius: 999px;
  background: var(--color-bg);
  color: var(--color-text);
  font-size: 0.72rem;
  padding: 0.1rem 0.45rem;
}
.cloud-ul-chip-dot { width: 0.55rem; height: 0.55rem; border-radius: 999px; }
.cloud-ul-chip-count { color: var(--color-text-dim); font-variant-numeric: tabular-nums; }
.cloud-ul-chip-x {
  border: 0;
  background: transparent;
  color: var(--color-text-dim);
  cursor: pointer;
  line-height: 1;
  padding: 0 0.1rem;
}
.cloud-ul-chip-x:hover { color: var(--color-danger); }
.cloud-ul-add { position: relative; }
.cloud-ul-add-summary {
  list-style: none;
  cursor: pointer;
  border: 1px dashed var(--color-border);
  border-radius: 999px;
  padding: 0.1rem 0.5rem;
  font-size: 0.72rem;
  color: var(--color-text);
}
.cloud-ul-add-summary::-webkit-details-marker { display: none; }
.cloud-ul-add-summary[aria-disabled="true"] { opacity: 0.5; cursor: not-allowed; }
.cloud-ul-add-menu {
  position: absolute;
  z-index: 30;
  margin-top: 0.25rem;
  max-height: 16rem;
  overflow-y: auto;
  display: flex;
  flex-direction: column;
  gap: 0.1rem;
  min-width: 12rem;
  padding: 0.3rem;
  border: 1px solid var(--color-border);
  border-radius: 8px;
  background: var(--color-bg-2);
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.35);
}
.cloud-ul-add-item {
  display: inline-flex;
  align-items: center;
  gap: 0.4rem;
  border: 0;
  background: transparent;
  border-radius: 6px;
  color: var(--color-text);
  font-size: 0.76rem;
  padding: 0.25rem 0.4rem;
  cursor: pointer;
  text-align: left;
}
.cloud-ul-add-item:hover { background: var(--color-bg); }
.cloud-ul-add-item .cloud-ul-chip-count { margin-left: auto; }

.cloud-ul-table-wrap { overflow-x: auto; border: 1px solid var(--color-border); border-radius: 8px; }
.cloud-ul-table { width: 100%; border-collapse: collapse; font-size: 0.8rem; }
.cloud-ul-table thead th {
  text-align: left;
  padding: 0.5rem 0.75rem;
  font-size: 0.68rem;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.03em;
  color: var(--color-text-dim);
  background: var(--color-bg-2);
  border-bottom: 1px solid var(--color-border);
}
.cloud-ul-table tbody td {
  padding: 0.45rem 0.75rem;
  border-bottom: 1px solid color-mix(in srgb, var(--color-border) 55%, transparent);
  color: var(--color-text);
  vertical-align: top;
}
.cloud-ul-table tbody tr:hover { background: var(--color-surface-hover); }
.cloud-ul-name { display: flex; flex-direction: column; gap: 0.1rem; }
.cloud-ul-name-label { font-weight: 600; }
.cloud-ul-name-sub { font-size: 0.68rem; color: var(--color-text-dim); }
.cloud-ul-kind { font-weight: 500; }
.cloud-ul-family { font-weight: 600; font-size: 0.74rem; }
.cloud-ul-status { display: inline-flex; align-items: center; gap: 0.35rem; }
.cloud-ul-status-dot { width: 0.55rem; height: 0.55rem; border-radius: 999px; }
.cloud-ul-empty { padding: 2rem 1rem; text-align: center; color: var(--color-text-dim); }
`
