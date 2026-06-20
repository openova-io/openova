/**
 * cloud-list/kinds.ts — single source of truth for the Cloud parent
 * surface's resource-kind catalogue.
 *
 * Two layers of kinds:
 *   1. Catalyst-projected kinds (Clusters, vClusters, Node Pools, Load
 *      Balancers, Buckets) — composed from the topology API.
 *   2. Native K8s kinds (Pods, Deployments, …) — read live via the
 *      catalyst-api k8scache SSE stream that PR #1062 wired up.
 *
 * Both CloudListView (active-list dispatch) and CloudKindChips (toolbar
 * chip strip) import from here. Per docs/INVIOLABLE-PRINCIPLES.md #4
 * (never hardcode), the kind list, default, storage key and icon glyph
 * shapes are exported as typed constants.
 */

import type { JSX } from 'react'

import { ClustersPage } from '../cloud-compute/ClustersPage'
import { VClustersPage } from '../cloud-compute/VClustersPage'
import { NodePoolsPage } from '../cloud-compute/NodePoolsPage'
import { WorkerNodesPage } from '../cloud-compute/WorkerNodesPage'
import { LoadBalancersPage } from '../cloud-network/LoadBalancersPage'
import { PvcsPage } from '../cloud-storage/PvcsPage'
import { BucketsPage } from '../cloud-storage/BucketsPage'
import { VolumesPage } from '../cloud-storage/VolumesPage'
import {
  PodsListPage,
  DeploymentsListPage,
  StatefulSetsListPage,
  DaemonSetsListPage,
  ReplicaSetsListPage,
  ConfigMapsListPage,
  SecretsListPage,
  NamespacesListPage,
  NodesListPage,
  PersistentVolumesListPage,
  EndpointSlicesListPage,
  ServicesListPage,
  IngressesListPage,
  StorageClassesListPage,
  DnsZonesListPage,
  PolicyReportsListPage,
  ClusterPolicyReportsListPage,
} from './kindsPages'

export type CloudListKind =
  | 'clusters'
  | 'vclusters'
  | 'node-pools'
  | 'worker-nodes'
  | 'load-balancers'
  | 'services'
  | 'ingresses'
  | 'pvcs'
  | 'buckets'
  | 'volumes'
  // Live K8s kinds — backed by the SSE data plane (PR #1062).
  | 'pods'
  | 'deployments'
  | 'statefulsets'
  | 'daemonsets'
  | 'replicasets'
  | 'configmaps'
  | 'secrets'
  | 'namespaces'
  | 'nodes'
  | 'persistentvolumes'
  | 'endpointslices'
  | 'storage-classes'
  | 'dns-zones'
  // Wave-2 Family-E (#1583, C11-005/C11-006) — Kyverno PolicyReport surfaces.
  | 'policyreports'
  | 'clusterpolicyreports'

/**
 * Mapping from CloudListKind id → registry kind name on the
 * /api/v1/sovereigns/{id}/k8s/{kind} surface. Used by chip-count
 * derivation in CloudPage to read live counts from the snapshot.
 *
 * Catalyst-projected kinds (clusters, vclusters, …) are absent — they
 * still come from the topology API.
 */
export const KIND_TO_REGISTRY: Partial<Record<CloudListKind, string>> = {
  pods: 'pod',
  deployments: 'deployment',
  statefulsets: 'statefulset',
  daemonsets: 'daemonset',
  replicasets: 'replicaset',
  services: 'service',
  ingresses: 'ingress',
  configmaps: 'configmap',
  secrets: 'secret',
  namespaces: 'namespace',
  nodes: 'node',
  persistentvolumes: 'persistentvolume',
  endpointslices: 'endpointslice',
  // Wave-2 Family-E (#1583, C11-005/C11-006): Kyverno PolicyReport CRDs.
  policyreports: 'policyreport',
  clusterpolicyreports: 'clusterpolicyreport',
}

export interface CloudKindEntry {
  id: CloudListKind
  label: string
  /** Free-form one-line tagline used elsewhere (legacy tile UI; kept
   *  for back-compat with detail-page subtitles). */
  tagline: string
  /** True when the count is real; false when the underlying informer
   *  isn't wired yet (we render a "—" instead of a number). */
  hasData: boolean
  /** SVG path data on the canonical 24x24 viewBox — Tabler-style. */
  icon: string
  /** Conceptual category (drives the chip-tint colour). */
  category: 'compute' | 'network' | 'storage' | 'workload' | 'config'
  Component: () => JSX.Element
  /** True when this kind is in the default chip strip; false → it lives
   *  in the `+ More` overflow popover. */
  primary: boolean
}

// Tabler / lucide-style outlines on the same 24x24 viewBox the
// sidebar NavIcon uses.
const ICON_CLUSTER =
  'M3 12c0 -4 4 -7 9 -7s9 3 9 7v6m0 0a2 2 0 0 1 -2 2H5a2 2 0 0 1 -2 -2v0M3 18v0M3 8h18'
const ICON_VCLUSTER =
  'M4 7a3 3 0 0 1 3 -3h10a3 3 0 0 1 3 3v10a3 3 0 0 1 -3 3H7a3 3 0 0 1 -3 -3zM8 8h8M8 12h8M8 16h5'
const ICON_NODE_POOL =
  'M5 4h4v4H5zM5 16h4v4H5zM15 4h4v4h-4zM15 16h4v4h-4zM7 8v8M17 8v8M9 6h6M9 18h6'
const ICON_WORKER_NODE =
  'M4 5h16v6H4zM4 13h16v6H4zM7 8h.01M7 16h.01M11 8h6M11 16h6'
const ICON_LB =
  'M12 4v4m0 0a4 4 0 0 0 -4 4v0m4 -4a4 4 0 0 1 4 4v0M4 12h4M16 12h4M6 14a2 2 0 0 0 2 2v0a2 2 0 0 0 2 -2v0a2 2 0 0 0 -2 -2v0a2 2 0 0 0 -2 2zM14 14a2 2 0 0 0 2 2v0a2 2 0 0 0 2 -2v0a2 2 0 0 0 -2 -2v0a2 2 0 0 0 -2 2z'
const ICON_SERVICE = 'M5 7h14M5 12h14M5 17h14M3 7v10M21 7v10'
const ICON_INGRESS = 'M3 12h6M21 12h-6M9 8l4 4 -4 4M15 16l-4 -4 4 -4'
const ICON_DNS =
  'M12 3a9 9 0 0 0 0 18m0 -18a9 9 0 0 1 0 18m0 -18c2 2 3 5 3 9s-1 7 -3 9m0 -18c-2 2 -3 5 -3 9s1 7 3 9M3 12h18'
const ICON_PVC =
  'M5 8a7 3 0 0 0 14 0A7 3 0 0 0 5 8zM5 8v8a7 3 0 0 0 14 0V8M5 12a7 3 0 0 0 14 0'
const ICON_BUCKET =
  'M5 6h14l-1 14a2 2 0 0 1 -2 2H8a2 2 0 0 1 -2 -2zM9 6V4a3 3 0 0 1 6 0v2'
const ICON_VOLUME =
  'M5 4a7 3 0 0 0 14 0A7 3 0 0 0 5 4zM5 4v16a7 3 0 0 0 14 0V4'
const ICON_STORAGE_CLASS = 'M4 6h16M4 12h16M4 18h16M8 6v12M16 6v12'
const ICON_POD =
  'M3 12c0 -1.66 4 -3 9 -3s9 1.34 9 3M3 12c0 1.66 4 3 9 3s9 -1.34 9 -3M3 12v6c0 1.66 4 3 9 3s9 -1.34 9 -3v-6'
const ICON_DEPLOY =
  'M5 4h6v6H5zM13 4h6v6h-6zM5 14h6v6H5zM13 14h6v6h-6z'
const ICON_STS = ICON_DEPLOY
const ICON_DS =
  'M3 6h18M3 12h18M3 18h18M6 6v12M12 6v12M18 6v12'
const ICON_RS = ICON_DEPLOY
const ICON_CFG =
  'M4 7h16M4 12h16M4 17h10M14 17l3 3 3 -5'
const ICON_SECRET =
  'M12 2l9 4v6c0 5 -4 9 -9 10 -5 -1 -9 -5 -9 -10V6z'
const ICON_NS = 'M5 5h14v14H5zM5 9h14M9 5v14'
const ICON_NODE = ICON_WORKER_NODE
const ICON_PV = ICON_PVC
const ICON_EPS =
  'M5 12a7 7 0 0 0 14 0M5 12a7 7 0 0 1 14 0M12 5v14M9 5h6M9 19h6'
// Wave-2 Family-E (C11-005/C11-006): shield-with-checkmark glyph for the
// Kyverno PolicyReport surfaces — same iconography as the Compliance
// dashboard so the operator sees the cross-page family-of-surfaces tie.
const ICON_POLICY_REPORT =
  'M12 3l8 4v5c0 5 -3.5 8 -8 9 -4.5 -1 -8 -4 -8 -9V7zM9 12l2 2 4 -4'

/**
 * Canonical kind catalogue. Order matters — `primary: true` entries
 * render in the toolbar chip strip; everything else lives in the
 * `+ More` popover.
 */
export const KINDS: readonly CloudKindEntry[] = [
  // Primary chips — the 6 most-used surfaces stay inline.
  { id: 'clusters', label: 'Clusters', tagline: 'k3s / k8s control planes', hasData: true, Component: ClustersPage, icon: ICON_CLUSTER, category: 'compute', primary: true },
  { id: 'vclusters', label: 'vClusters', tagline: 'Logical isolation per Organization', hasData: true, Component: VClustersPage, icon: ICON_VCLUSTER, category: 'compute', primary: true },
  { id: 'node-pools', label: 'Node Pools', tagline: 'Worker pools grouped by SKU + role', hasData: true, Component: NodePoolsPage, icon: ICON_NODE_POOL, category: 'compute', primary: true },
  { id: 'pvcs', label: 'PVCs', tagline: 'Persistent volume claims', hasData: true, Component: PvcsPage, icon: ICON_PVC, category: 'storage', primary: true },
  { id: 'load-balancers', label: 'Load Balancers', tagline: 'Cloud-provisioned LBs fronting clusters', hasData: true, Component: LoadBalancersPage, icon: ICON_LB, category: 'network', primary: true },
  { id: 'buckets', label: 'Buckets', tagline: 'S3-compatible (SeaweedFS / provider)', hasData: true, Component: BucketsPage, icon: ICON_BUCKET, category: 'storage', primary: true },

  /* ── Overflow — accessible via the `+ More` popover ───────────── */

  // Compute & infrastructure
  { id: 'worker-nodes', label: 'Worker Nodes', tagline: 'Hetzner Server CRs (Crossplane projection)', hasData: true, Component: WorkerNodesPage, icon: ICON_WORKER_NODE, category: 'compute', primary: false },
  { id: 'nodes', label: 'Nodes', tagline: 'Raw Kubelets reporting to the cluster', hasData: true, Component: NodesListPage, icon: ICON_NODE, category: 'compute', primary: false },
  { id: 'namespaces', label: 'Namespaces', tagline: 'Logical partitions', hasData: true, Component: NamespacesListPage, icon: ICON_NS, category: 'config', primary: false },

  // Workloads
  { id: 'pods', label: 'Pods', tagline: 'Running containers across all namespaces', hasData: true, Component: PodsListPage, icon: ICON_POD, category: 'workload', primary: false },
  { id: 'deployments', label: 'Deployments', tagline: 'Stateless replicated workloads', hasData: true, Component: DeploymentsListPage, icon: ICON_DEPLOY, category: 'workload', primary: false },
  { id: 'statefulsets', label: 'StatefulSets', tagline: 'Ordered, persistent workloads', hasData: true, Component: StatefulSetsListPage, icon: ICON_STS, category: 'workload', primary: false },
  { id: 'daemonsets', label: 'DaemonSets', tagline: 'One pod per node', hasData: true, Component: DaemonSetsListPage, icon: ICON_DS, category: 'workload', primary: false },
  { id: 'replicasets', label: 'ReplicaSets', tagline: 'Owned by Deployments', hasData: true, Component: ReplicaSetsListPage, icon: ICON_RS, category: 'workload', primary: false },

  // Networking
  { id: 'services', label: 'Services', tagline: 'ClusterIP / NodePort / LoadBalancer routes', hasData: true, Component: ServicesListPage, icon: ICON_SERVICE, category: 'network', primary: false },
  { id: 'ingresses', label: 'Ingresses', tagline: 'HTTP routing + TLS terminators', hasData: true, Component: IngressesListPage, icon: ICON_INGRESS, category: 'network', primary: false },
  { id: 'endpointslices', label: 'EndpointSlices', tagline: 'Service backend addresses', hasData: true, Component: EndpointSlicesListPage, icon: ICON_EPS, category: 'network', primary: false },
  { id: 'dns-zones', label: 'DNS Zones', tagline: 'PowerDNS zones (Sovereign-side)', hasData: false, Component: DnsZonesListPage, icon: ICON_DNS, category: 'network', primary: false },

  // Config & storage
  { id: 'configmaps', label: 'ConfigMaps', tagline: 'Key-value config bundles', hasData: true, Component: ConfigMapsListPage, icon: ICON_CFG, category: 'config', primary: false },
  { id: 'secrets', label: 'Secrets', tagline: 'Sensitive bundles (values redacted)', hasData: true, Component: SecretsListPage, icon: ICON_SECRET, category: 'config', primary: false },
  { id: 'volumes', label: 'Volumes', tagline: 'Cloud block volumes', hasData: true, Component: VolumesPage, icon: ICON_VOLUME, category: 'storage', primary: false },
  { id: 'persistentvolumes', label: 'PersistentVolumes', tagline: 'Cluster-scoped backing volumes', hasData: true, Component: PersistentVolumesListPage, icon: ICON_PV, category: 'storage', primary: false },
  { id: 'storage-classes', label: 'Storage Classes', tagline: 'Provisioner + reclaim policy presets', hasData: false, Component: StorageClassesListPage, icon: ICON_STORAGE_CLASS, category: 'storage', primary: false },

  // Wave-2 Family-E (C11-005/C11-006): Kyverno PolicyReport surfaces.
  // Both render in the `+ More` popover (the Compliance dashboard is the
  // primary read-path; these lists are the kubectl-equivalent fallback).
  { id: 'policyreports', label: 'Policy Reports', tagline: 'Per-namespace Kyverno PolicyReport evaluations.', hasData: true, Component: PolicyReportsListPage, icon: ICON_POLICY_REPORT, category: 'config', primary: false },
  { id: 'clusterpolicyreports', label: 'Cluster Policy Reports', tagline: 'Cluster-scoped Kyverno ClusterPolicyReport evaluations.', hasData: true, Component: ClusterPolicyReportsListPage, icon: ICON_POLICY_REPORT, category: 'config', primary: false },
] as const

export const KIND_IDS: readonly CloudListKind[] = KINDS.map((k) => k.id)
export const DEFAULT_KIND: CloudListKind = 'clusters'
export const KIND_STORAGE_KEY = 'sov-cloud-list-kind'

export function isValidKind(value: unknown): value is CloudListKind {
  return typeof value === 'string' && (KIND_IDS as readonly string[]).includes(value)
}

export function readPersistedKind(): CloudListKind | null {
  if (typeof window === 'undefined') return null
  try {
    const raw = window.localStorage.getItem(KIND_STORAGE_KEY)
    return isValidKind(raw) ? raw : null
  } catch {
    return null
  }
}

export function writePersistedKind(kind: CloudListKind): void {
  if (typeof window === 'undefined') return
  try {
    window.localStorage.setItem(KIND_STORAGE_KEY, kind)
  } catch {
    /* noop */
  }
}

export function findKind(id: CloudListKind): CloudKindEntry {
  return KINDS.find((k) => k.id === id) ?? KINDS[0]
}
