/**
 * lib/reconciliation.api.ts — client for the #3925 surface-B bounded Flux
 * dependency DAG (GET /api/v1/deployments/{id}/reconciliation).
 *
 * Matches the Go ReconciliationDAG / ReconciliationNode structs in
 * products/catalyst/bootstrap/api/internal/handler/reconciliation_dag.go.
 * The wire vocabulary is the Reconciliation set — NEVER Success/Failed.
 */

import { API_BASE } from '@/shared/config/urls'

/** The node-state vocabulary the FE renders verbatim. */
export type ReconState =
  | 'Reconciled'
  | 'Reconciling'
  | 'Drifted'
  | 'Degraded'
  | 'Suspended'

export type ReconKind = 'HelmRelease' | 'Kustomization'

export interface ReconciliationNode {
  id: string
  label: string
  kind: ReconKind
  state: ReconState
  dependsOn?: string[]
  message?: string
}

export interface ReconciliationDAG {
  nodes: ReconciliationNode[]
  reconciled: number
  total: number
  watching: boolean
  notYetTracked: string[]
}

/**
 * fetchReconciliationDAG retrieves the bounded Flux DAG for a deployment.
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 the URL is built from API_BASE.
 */
export async function fetchReconciliationDAG(
  deploymentId: string,
): Promise<ReconciliationDAG> {
  const res = await fetch(
    `${API_BASE}/v1/deployments/${encodeURIComponent(deploymentId)}/reconciliation`,
    { headers: { Accept: 'application/json' }, credentials: 'include' },
  )
  if (!res.ok) {
    throw new Error(`reconciliation DAG fetch failed: HTTP ${res.status}`)
  }
  return (await res.json()) as ReconciliationDAG
}
