/**
 * cloudGraphData.ts — the ONE dataset builder for the unified Cloud
 * surface (#3970). Both renderings (graph + list) derive from this single
 * merged `GraphNode[]` so a reconciler that appears in the graph appears
 * in the list, filtered by the SAME active chip-set.
 *
 * This folds the three sources into one neutral graph shape:
 *   • cloud  — the hierarchical infrastructure tree (hierarchyToGraph)
 *   • k8s    — the live k8scache SSE snapshot (k8sToGraph)
 *   • recon  — the /reconciliation set incl. Flux + non-Flux reconcilers
 *              and the Catalyst CRs (reconcilersToGraph)
 *
 * Reconciler ids are namespaced `recon:` so they never collide with the
 * cloud/k8s ids — a plain concat after mergeGraphs is safe.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 — a pure shape transform; no node
 * is fabricated, the same merge the graph canvas already used.
 */

import type { HierarchicalInfrastructure } from '@/lib/infrastructure.types'
import type { ReconciliationNode } from '@/lib/reconciliation.api'
import { hierarchyToGraph } from './adapter'
import { k8sToGraph, mergeGraphs } from './k8sAdapter'
import { reconcilersToGraph } from './reconcilerAdapter'
import type { K8sSnapshot } from './useK8sCacheStream'
import type { GraphEdge, GraphNode } from './types'

export interface CloudGraphData {
  nodes: GraphNode[]
  edges: GraphEdge[]
}

/**
 * buildCloudGraph — merge the cloud tree + k8s snapshot + reconciler set
 * into ONE neutral graph. Pure; safe to memoise on its inputs.
 */
export function buildCloudGraph(
  data: HierarchicalInfrastructure | null,
  k8sSnapshot: K8sSnapshot | null | undefined,
  reconcilers: ReconciliationNode[] | null | undefined,
): CloudGraphData {
  const cloud = hierarchyToGraph(data)
  // k8sToGraph iterates a Map; pass an empty one when no snapshot yet.
  const k8s = k8sToGraph(k8sSnapshot ?? (new Map() as K8sSnapshot))
  const base = mergeGraphs(cloud, k8s)
  const recon = reconcilersToGraph(reconcilers)
  if (recon.nodes.length === 0) return base
  return {
    nodes: [...base.nodes, ...recon.nodes],
    edges: [...base.edges, ...recon.edges],
  }
}
