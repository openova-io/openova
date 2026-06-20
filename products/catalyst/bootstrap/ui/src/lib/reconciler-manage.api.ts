/**
 * lib/reconciler-manage.api.ts — client for the #3996 lightweight
 * ArgoCD/Flux MANAGEMENT surface on the Reconciliation lens.
 *
 * Matches the Go handlers in
 * products/catalyst/bootstrap/api/internal/handler/reconcilers.go:
 *
 *   GET  /api/v1/deployments/{id}/reconcilers
 *   GET  /api/v1/deployments/{id}/reconcilers/{kind}/{ns}/{name}/logs
 *   POST /api/v1/deployments/{id}/reconcilers/{kind}/{ns}/{name}/{action}
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 the URL is built from API_BASE.
 * Mutating calls go through authedFetch so the operator's session/Bearer
 * is attached (the backend gates them behind the operator-tier RBAC).
 */

import { API_BASE } from '@/shared/config/urls'
import { authedFetch } from '@/shared/lib/authedFetch'

/** The reconcile state vocabulary the management list emits (never Success/Failed). */
export type ManageState = 'Reconciled' | 'Reconciling' | 'Degraded' | 'Suspended'

/** A manageable Flux reconciler row (mirror of helmwatch.ManagedReconciler). */
export interface ManagedReconciler {
  kind: string
  name: string
  namespace: string
  state: ManageState
  message?: string
  revision?: string
  suspended: boolean
  lastReconcile?: string
  controller: string
}

export interface ReconcilerList {
  reconcilers: ManagedReconciler[]
  reconciled: number
  total: number
}

/** One controller-log line for the drill-in viewer. */
export interface ReconcilerLogLine {
  lineNumber: number
  message: string
}

export interface ReconcilerLogs {
  controller: string
  object: string
  lines: ReconcilerLogLine[]
  total: number
}

/** The three Flux-native actions the surface can trigger. */
export type ReconcilerActionKind = 'reconcile' | 'suspend' | 'resume'

/** The Flux kinds that are MANAGEABLE (have reconcile/suspend/resume + logs).
 *  The declarative Catalyst CRs are list-only, so the FE only shows the
 *  management affordances for these. */
export const MANAGEABLE_KINDS: ReadonlySet<string> = new Set([
  'HelmRelease',
  'Kustomization',
  'GitRepository',
  'OCIRepository',
  'HelmRepository',
  'HelmChart',
])

/** isManageableKind — true when a kind supports the Flux management actions. */
export function isManageableKind(kind: string): boolean {
  return MANAGEABLE_KINDS.has(kind)
}

/** fetchReconcilers lists every manageable Flux reconciler with live status. */
export async function fetchReconcilers(deploymentId: string): Promise<ReconcilerList> {
  const res = await authedFetch(
    `${API_BASE}/v1/deployments/${encodeURIComponent(deploymentId)}/reconcilers`,
    { headers: { Accept: 'application/json' } },
  )
  if (!res.ok) {
    throw new Error(`reconcilers list failed: HTTP ${res.status}`)
  }
  return (await res.json()) as ReconcilerList
}

/** fetchReconcilerLogs pages the owning controller's logs filtered to one object. */
export async function fetchReconcilerLogs(
  deploymentId: string,
  kind: string,
  namespace: string,
  name: string,
  tailLines = 200,
): Promise<ReconcilerLogs> {
  const base = `${API_BASE}/v1/deployments/${encodeURIComponent(deploymentId)}/reconcilers`
  const path = `${base}/${encodeURIComponent(kind)}/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}/logs`
  const res = await authedFetch(`${path}?tailLines=${tailLines}`, {
    headers: { Accept: 'application/json' },
  })
  if (!res.ok) {
    throw new Error(`reconciler logs failed: HTTP ${res.status}`)
  }
  return (await res.json()) as ReconcilerLogs
}

export interface ReconcilerActionResult {
  kind: string
  namespace: string
  name: string
  action: ReconcilerActionKind
  requestedAt: string
  requestedBy: string
}

/** triggerReconcilerAction fires reconcile / suspend / resume on one object. */
export async function triggerReconcilerAction(
  deploymentId: string,
  kind: string,
  namespace: string,
  name: string,
  action: ReconcilerActionKind,
): Promise<ReconcilerActionResult> {
  const base = `${API_BASE}/v1/deployments/${encodeURIComponent(deploymentId)}/reconcilers`
  const path = `${base}/${encodeURIComponent(kind)}/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}/${action}`
  const res = await authedFetch(path, { method: 'POST' })
  if (res.status === 403) {
    throw new Error('Not permitted — managing a reconciler requires operator tier or higher.')
  }
  if (!res.ok) {
    throw new Error(`reconciler ${action} failed: HTTP ${res.status}`)
  }
  return (await res.json()) as ReconcilerActionResult
}
