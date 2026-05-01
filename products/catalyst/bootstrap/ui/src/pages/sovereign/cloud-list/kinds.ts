/**
 * cloud-list/kinds.ts — single source of truth for the Cloud parent
 * surface's resource-kind catalogue (issue #366 item 1).
 *
 * Both CloudListView (active-list dispatch) and CloudKindChips (toolbar
 * chip strip) import from here. Per docs/INVIOLABLE-PRINCIPLES.md #4
 * (never hardcode), the kind list, default, storage key and icon glyph
 * shapes are exported as typed constants — there are no inline literal
 * lists at any call site.
 */

import type { JSX } from 'react'

import { ClustersPage } from '../cloud-compute/ClustersPage'
import { VClustersPage } from '../cloud-compute/VClustersPage'
import { NodePoolsPage } from '../cloud-compute/NodePoolsPage'
import { WorkerNodesPage } from '../cloud-compute/WorkerNodesPage'
import { LoadBalancersPage } from '../cloud-network/LoadBalancersPage'
import { ServicesPage } from '../cloud-network/ServicesPage'
import { IngressesPage } from '../cloud-network/IngressesPage'
import { DnsZonesPage } from '../cloud-network/DnsZonesPage'
import { PvcsPage } from '../cloud-storage/PvcsPage'
import { BucketsPage } from '../cloud-storage/BucketsPage'
import { VolumesPage } from '../cloud-storage/VolumesPage'
import { StorageClassesPage } from '../cloud-storage/StorageClassesPage'

export type CloudListKind =
  | 'clusters'
  | 'vclusters'
  | 'node-pools'
  | 'worker-nodes'
  | 'load-balancers'
  | 'services'
  | 'ingresses'
  | 'dns-zones'
  | 'pvcs'
  | 'buckets'
  | 'volumes'
  | 'storage-classes'

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
  category: 'compute' | 'network' | 'storage'
  Component: () => JSX.Element
  /** True when this kind is in the default chip strip; false → it lives
   *  in the `+ More` overflow popover (issue #366 item 1). */
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

/**
 * Canonical kind catalogue. Order matters — `primary: true` entries
 * render in the toolbar chip strip; everything else lives in the
 * `+ More` popover (issue #366 item 1). Founder priority order:
 * Clusters, vClusters, Node Pools, PVCs, Load Balancers, Buckets.
 */
export const KINDS: readonly CloudKindEntry[] = [
  { id: 'clusters', label: 'Clusters', tagline: 'k3s / k8s control planes', hasData: true, Component: ClustersPage, icon: ICON_CLUSTER, category: 'compute', primary: true },
  { id: 'vclusters', label: 'vClusters', tagline: 'Logical isolation per Sovereign tenant', hasData: true, Component: VClustersPage, icon: ICON_VCLUSTER, category: 'compute', primary: true },
  { id: 'node-pools', label: 'Node Pools', tagline: 'Worker pools grouped by SKU + role', hasData: true, Component: NodePoolsPage, icon: ICON_NODE_POOL, category: 'compute', primary: true },
  { id: 'pvcs', label: 'PVCs', tagline: 'Persistent volume claims', hasData: true, Component: PvcsPage, icon: ICON_PVC, category: 'storage', primary: true },
  { id: 'load-balancers', label: 'Load Balancers', tagline: 'Cloud-provisioned LBs fronting clusters', hasData: true, Component: LoadBalancersPage, icon: ICON_LB, category: 'network', primary: true },
  { id: 'buckets', label: 'Buckets', tagline: 'S3-compatible (SeaweedFS / provider)', hasData: true, Component: BucketsPage, icon: ICON_BUCKET, category: 'storage', primary: true },
  // Overflow — accessible via the `+ More` popover.
  { id: 'worker-nodes', label: 'Worker Nodes', tagline: 'Individual VMs / kubelets reporting in', hasData: true, Component: WorkerNodesPage, icon: ICON_WORKER_NODE, category: 'compute', primary: false },
  { id: 'services', label: 'Services', tagline: 'Awaiting service informer (#321)', hasData: false, Component: ServicesPage, icon: ICON_SERVICE, category: 'network', primary: false },
  { id: 'ingresses', label: 'Ingresses', tagline: 'Awaiting ingress informer (#321)', hasData: false, Component: IngressesPage, icon: ICON_INGRESS, category: 'network', primary: false },
  { id: 'dns-zones', label: 'DNS Zones', tagline: 'Awaiting external-dns informer (#321)', hasData: false, Component: DnsZonesPage, icon: ICON_DNS, category: 'network', primary: false },
  { id: 'volumes', label: 'Volumes', tagline: 'Cloud block volumes attached to nodes', hasData: true, Component: VolumesPage, icon: ICON_VOLUME, category: 'storage', primary: false },
  { id: 'storage-classes', label: 'Storage Classes', tagline: 'Awaiting storage-class informer (#321)', hasData: false, Component: StorageClassesPage, icon: ICON_STORAGE_CLASS, category: 'storage', primary: false },
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
