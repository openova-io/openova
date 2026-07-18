/**
 * reconcilerDrill.ts — coordinate helpers for the reconciler-node drill-in
 * (UAT row 193, issue #5223). Split from ReconcilerDrillPanel.tsx so the
 * component file only exports components (react-refresh rule).
 */

import type { GraphNode } from './types'

/** Node-id namespace the reconciler adapter stamps (reconcilerAdapter.ts). */
export const RECON_NODE_PREFIX = 'recon:'

/** isReconcilerNode — true when a graph node came from the reconciler set. */
export function isReconcilerNode(node: Pick<GraphNode, 'id'>): boolean {
  return node.id.startsWith(RECON_NODE_PREFIX)
}

export interface ReconcilerCoords {
  kind: string
  namespace: string | null
  name: string
}

/**
 * reconcilerCoordsForNode — derive the `{kind, ns, name}` coordinate from a
 * reconciler graph node. The DAG feed's id dialects
 * (handler/reconciliation_dag.go):
 *   • HelmRelease    — `bp-<app>`                      (no ns on the wire)
 *   • Kustomization  — `kustomization/<tier>`          (no ns on the wire)
 *   • declarative    — `<kindlower>/<ns>/<name>`       (ns inline; empty for
 *                       cluster-scoped kinds, e.g. `organization//acme`)
 * The REAL Kind casing rides in node.metadata.kind (reconcilerAdapter).
 */
export function reconcilerCoordsForNode(node: GraphNode): ReconcilerCoords {
  const rawId = node.id.slice(RECON_NODE_PREFIX.length)
  const kind = node.metadata?.kind ?? ''
  if (rawId.startsWith('kustomization/')) {
    return {
      kind: kind || 'Kustomization',
      namespace: null,
      name: rawId.slice('kustomization/'.length),
    }
  }
  const parts = rawId.split('/')
  if (parts.length === 3) {
    // Declarative `<kindlower>/<ns>/<name>` — ns may be empty (cluster-scoped).
    return { kind: kind || parts[0], namespace: parts[1] || null, name: parts[2] }
  }
  return { kind: kind || 'HelmRelease', namespace: null, name: rawId }
}
