/**
 * reconcilerAdapter.ts — folds the #3925/#3958 reconciliation set
 * (Flux HelmReleases + Kustomizations AND the declarative non-Flux
 * reconcilers: cert-manager / CNPG / External-Secrets / the Catalyst
 * control-plane CRs) into the neutral GraphNode/GraphEdge shape the
 * unified Cloud-graph canvas consumes (#3958).
 *
 * Each reconciler becomes a typed node:
 *   • kind  → ArchNodeType (drives SHAPE via NODE_CATEGORY + BORDER via
 *             NODE_FAMILY)
 *   • state → ArchStatus  (drives FILL via STATUS_FILL)
 *   • spec.dependsOn → 'depends-on' edges (Flux only carries these)
 *
 * The node ids are namespaced `recon:<id>` so they never collide with
 * the cloud-side / k8s-side composite ids (`HelmRelease:foo` vs a future
 * k8s HelmRelease projection). The dependsOn edges rewrite both endpoints
 * through the same namespacing so they resolve.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 — the kind→type, state→status maps
 * are data; no node is fabricated, the adapter is a pure shape transform
 * over what the backend emits.
 */

import type {
  ReconState,
  ReconciliationNode,
} from '@/lib/reconciliation.api'
import type { ArchNodeType, ArchStatus, GraphEdge, GraphNode } from './types'

export interface ReconcilerAdaptResult {
  nodes: GraphNode[]
  edges: GraphEdge[]
}

/** Namespacing prefix so reconciler ids never collide with cloud/k8s ids. */
const RECON_PREFIX = 'recon:'

/**
 * Reconciler kind → ArchNodeType. The wire kinds already match the
 * ArchNodeType names 1:1 EXCEPT `Cluster`: in the reconciliation set
 * `Cluster` is a CNPG postgres Cluster (Data/Storage, indigo CNPG
 * border), NOT the cloud-side k8s Cluster (Scope). We remap it to
 * `Database` so it draws as a ⬢ hexagon with the CNPG family border.
 * Any unknown kind falls back to a control-plane square.
 */
const KIND_TO_ARCHTYPE: Record<string, ArchNodeType> = {
  HelmRelease: 'HelmRelease',
  Kustomization: 'Kustomization',
  Certificate: 'Certificate',
  ExternalSecret: 'ExternalSecret',
  Application: 'Application',
  Environment: 'Environment',
  Organization: 'Organization',
  Continuum: 'Continuum',
  // CNPG Cluster → Database (Data/Storage hexagon, CNPG indigo border).
  Cluster: 'Database',
}

function archTypeForKind(kind: string): ArchNodeType {
  return KIND_TO_ARCHTYPE[kind] ?? 'HelmRelease'
}

/** Reconcile state → ArchStatus (FILL). */
const STATE_TO_STATUS: Record<ReconState, ArchStatus> = {
  Reconciled: 'healthy',
  Reconciling: 'reconciling',
  Drifted: 'drifted',
  Degraded: 'degraded',
  Suspended: 'suspended',
}

function statusForState(state: ReconState): ArchStatus {
  return STATE_TO_STATUS[state] ?? 'unknown'
}

/**
 * reconcilersToGraph — turn the reconciliation node list into the
 * neutral graph shape. Returns empty when the input is null/empty.
 */
export function reconcilersToGraph(
  nodes: ReconciliationNode[] | null | undefined,
): ReconcilerAdaptResult {
  const out: ReconcilerAdaptResult = { nodes: [], edges: [] }
  if (!nodes || nodes.length === 0) return out

  const present = new Set<string>(nodes.map((n) => n.id))

  for (const rn of nodes) {
    const type = archTypeForKind(rn.kind)
    out.nodes.push({
      id: `${RECON_PREFIX}${rn.id}`,
      type,
      label: rn.label || rn.id,
      sublabel: `${rn.kind} · ${rn.state}`,
      status: statusForState(rn.state),
      metadata: {
        kind: rn.kind,
        state: rn.state,
        ...(rn.message ? { message: rn.message } : {}),
      },
    })
    // dependsOn edges — Flux carries these; non-Flux reconcilers are
    // edgeless. Skip deps that aren't in the present set (a deferred /
    // not-yet-tracked reconciler) so we never dangle an edge.
    for (const dep of rn.dependsOn ?? []) {
      if (!present.has(dep)) continue
      out.edges.push({
        id: `${RECON_PREFIX}${dep}->${RECON_PREFIX}${rn.id}`,
        source: `${RECON_PREFIX}${dep}`,
        target: `${RECON_PREFIX}${rn.id}`,
        type: 'depends-on',
      })
    }
  }

  return out
}
