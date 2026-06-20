/**
 * reconciliation.shared.ts — tokens + helpers shared by the Reconciliation
 * page's two views (the GraphCanvas bubble DAG and the scientific
 * ReconciliationTable). One source of truth so the state colour, the
 * Flux/non-Flux filter class, and the attention-ordering never drift apart
 * between the graph and the table.
 *
 * Vocabulary is the Reconciliation set — Reconciled/Reconciling/Drifted/
 * Degraded/Suspended — NEVER Success/Failed (those imply a finite end).
 */

import type { ReconState, ReconciliationNode } from '@/lib/reconciliation.api'

/**
 * Per-state visual tokens. The glyph + colour are the only state signal;
 * the string is the vocabulary word verbatim (the FE never renders
 * Success/Failed). `fg` doubles as the DAG bubble ring colour (passed to
 * GraphCanvas.nodeColorFn) so a node reads the same hue in both views.
 */
export const STATE_TONE: Record<
  ReconState,
  { glyph: string; fg: string; bg: string; border: string; pulse: boolean }
> = {
  Reconciled: { glyph: '◇', fg: '#4ADE80', bg: 'rgba(74,222,128,0.10)', border: 'rgba(74,222,128,0.30)', pulse: false },
  Reconciling: { glyph: '↻', fg: '#38BDF8', bg: 'rgba(56,189,248,0.10)', border: 'rgba(56,189,248,0.30)', pulse: true },
  Drifted: { glyph: '⤳', fg: '#FBBF24', bg: 'rgba(251,191,36,0.10)', border: 'rgba(251,191,36,0.30)', pulse: false },
  Degraded: { glyph: '⚠', fg: '#F87171', bg: 'rgba(248,113,113,0.10)', border: 'rgba(248,113,113,0.30)', pulse: false },
  Suspended: { glyph: '⏸', fg: 'var(--color-text-dim)', bg: 'rgba(148,163,184,0.10)', border: 'rgba(148,163,184,0.30)', pulse: false },
}

/**
 * Default sort priority — the operator wants the reconcilers that need
 * attention surfaced first (mirrors the Jobs table putting running/failed
 * on top). Lower = higher in the table. Degraded (stuck) first, then the
 * self-healing Drifted, then in-flight Reconciling, then the dormant
 * Suspended, then the steady-state Reconciled.
 */
export const STATE_PRIORITY: Record<ReconState, number> = {
  Degraded: 0,
  Drifted: 1,
  Reconciling: 2,
  Suspended: 3,
  Reconciled: 4,
}

/** The two filter pseudo-classes used by the DAG filter dropdown. */
export const FLUX_CLASS = 'Flux'
export const ALL_CLASS = 'All'

/**
 * A node's filter CLASS. Flux = HelmRelease|Kustomization (the only
 * edge-bearing layer, so the DAG defaults to it); every other kind is its
 * own class (Certificate, Cluster, ExternalSecret, Application, …). Keeps
 * the FE kind-agnostic: a new backend kind becomes its own class with no FE
 * change.
 */
export function nodeClass(kind: string): string {
  return kind === 'HelmRelease' || kind === 'Kustomization' ? FLUX_CLASS : kind
}

/* ── Table helpers (kept here, not in the component file, so the table's
 *    react-refresh boundary only exports components) ──────────────────── */

/** The full state vocabulary, in attention order — drives the State filter. */
export const STATE_VALUES: readonly ReconState[] = ['Reconciled', 'Reconciling', 'Drifted', 'Degraded', 'Suspended']

export type ReconSortKey = 'name' | 'kind' | 'class' | 'state'
export type SortDir = 'asc' | 'desc'

/** Case-insensitive substring match across name / kind / state / message /
 *  dependsOn — the same breadth JobsTable's matchJob uses. */
export function matchReconNode(n: ReconciliationNode, query: string): boolean {
  if (!query.trim()) return true
  const q = query.toLowerCase()
  const fields = [n.label, n.id, n.kind, n.state, n.message ?? '']
  for (const f of fields) {
    if (f.toLowerCase().includes(q)) return true
  }
  for (const d of n.dependsOn ?? []) {
    if (d.toLowerCase().includes(q)) return true
  }
  return false
}

/**
 * Comparator factory. Key 'state' orders by STATE_PRIORITY (attention-first);
 * 'name'/'kind'/'class' are lexical. Every comparison tiebreaks on id so the
 * render is deterministic across re-fetches. Pure + exported so the unit test
 * locks the contract.
 */
export function compareReconNodes(a: ReconciliationNode, b: ReconciliationNode, key: ReconSortKey, dir: SortDir): number {
  const sign = dir === 'asc' ? 1 : -1
  let primary = 0
  switch (key) {
    case 'state':
      primary = (STATE_PRIORITY[a.state] ?? 99) - (STATE_PRIORITY[b.state] ?? 99)
      break
    case 'kind':
      primary = a.kind.localeCompare(b.kind)
      break
    case 'class':
      primary = nodeClass(a.kind).localeCompare(nodeClass(b.kind))
      break
    case 'name':
      primary = a.label.localeCompare(b.label)
      break
  }
  if (primary !== 0) return sign * primary
  return a.id.localeCompare(b.id)
}
