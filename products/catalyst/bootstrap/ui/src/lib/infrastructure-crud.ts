/**
 * infrastructure-crud.ts — typed client wrappers for every CRUD action
 * on the Sovereign Infrastructure surface (issue #228).
 *
 * Every mutation ends up creating a Job entry on the backend; the
 * Jobs system is owned by a sibling agent. The wrappers below only
 * speak HTTP — Job tracking is plumbed through the response.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #1 (waterfall) — every endpoint named in the spec has a wrapper
 *      shipped today, even if the backend isn't live yet. The
 *      `feature flag` is `import.meta.env.VITE_INFRA_CRUD_LIVE`.
 *   #4 (never hardcode) — endpoints are derived from API_BASE.
 */

import { API_BASE } from '@/shared/config/urls'
import type { CloudProvider } from '@/entities/deployment/model'
import type { IsolationMode } from './infrastructure.types'

/** Every mutation returns a JobRef so the operator can track it from
 *  the Jobs page. `parentId` is the synthesised day-2-mutations group
 *  Job id (issue #351 — replaces the old `batchId`). */
export interface JobRef {
  jobId: string
  parentId: string
  status: 'queued' | 'running' | 'succeeded' | 'failed'
}

/** Cascade preview returned by GET .../delete-preview before the
 *  operator confirms a destructive op. */
export interface CascadePreview {
  affected: { id: string; kind: string; label: string }[]
  estimatedDuration: string
  blockers: string[]
}

/* ── Region ─────────────────────────────────────────────────────── */

export interface AddRegionRequest {
  deploymentId: string
  provider: CloudProvider
  providerRegion: string
  skuCp: string
  skuWorker: string
  workerCount: number
}

export async function addRegion(req: AddRegionRequest): Promise<JobRef> {
  return postJSON(
    `${API_BASE}/v1/deployments/${encodeURIComponent(req.deploymentId)}/infrastructure/regions`,
    {
      provider: req.provider,
      providerRegion: req.providerRegion,
      skuCp: req.skuCp,
      skuWorker: req.skuWorker,
      workerCount: req.workerCount,
    },
  )
}

/** Patch fields the catalyst-api supports updating safely on a Region.
 *  Provider + providerRegion are immutable (cloud-side identity);
 *  skuCp / skuWorker / workerCount are surfaced as Update fields. */
export interface UpdateRegionRequest {
  deploymentId: string
  regionId: string
  skuCp?: string
  skuWorker?: string
  workerCount?: number
}

export async function updateRegion(req: UpdateRegionRequest): Promise<JobRef> {
  const body: Record<string, unknown> = {}
  if (req.skuCp !== undefined) body.skuCp = req.skuCp
  if (req.skuWorker !== undefined) body.skuWorker = req.skuWorker
  if (req.workerCount !== undefined) body.workerCount = req.workerCount
  return patchJSON(
    `${API_BASE}/v1/deployments/${encodeURIComponent(req.deploymentId)}/infrastructure/regions/${encodeURIComponent(req.regionId)}`,
    body,
  )
}

/* ── Cluster ────────────────────────────────────────────────────── */

export interface AddClusterRequest {
  deploymentId: string
  regionId: string
  name: string
  version: string
  controlPlaneSku: string
}

export async function addCluster(req: AddClusterRequest): Promise<JobRef> {
  return postJSON(
    `${API_BASE}/v1/deployments/${encodeURIComponent(req.deploymentId)}/infrastructure/regions/${encodeURIComponent(req.regionId)}/clusters`,
    { name: req.name, version: req.version, controlPlaneSku: req.controlPlaneSku },
  )
}

/** Patch fields supported on a Cluster: name (rename), version (k3s
 *  upgrade), controlPlaneSku (resize CP). */
export interface UpdateClusterRequest {
  deploymentId: string
  clusterId: string
  name?: string
  version?: string
  controlPlaneSku?: string
}

export async function updateCluster(req: UpdateClusterRequest): Promise<JobRef> {
  const body: Record<string, unknown> = {}
  if (req.name !== undefined) body.name = req.name
  if (req.version !== undefined) body.version = req.version
  if (req.controlPlaneSku !== undefined) body.controlPlaneSku = req.controlPlaneSku
  return patchJSON(
    `${API_BASE}/v1/deployments/${encodeURIComponent(req.deploymentId)}/infrastructure/clusters/${encodeURIComponent(req.clusterId)}`,
    body,
  )
}

/* ── vCluster ───────────────────────────────────────────────────── */

export interface AddVClusterRequest {
  deploymentId: string
  clusterId: string
  name: string
  isolationMode: IsolationMode
}

export async function addVCluster(req: AddVClusterRequest): Promise<JobRef> {
  return postJSON(
    `${API_BASE}/v1/deployments/${encodeURIComponent(req.deploymentId)}/infrastructure/clusters/${encodeURIComponent(req.clusterId)}/vclusters`,
    { name: req.name, isolationMode: req.isolationMode },
  )
}

/** Patch fields supported on a vCluster: rename + change isolationMode.
 *  The catalyst-api persists this through a direct CR write on the
 *  Sovereign cluster (vcluster.io/v1alpha1/vclusters), per ADR-0001
 *  §9.2 row B3 (K8s-native CR, not Crossplane XRC). */
export interface UpdateVClusterRequest {
  deploymentId: string
  vclusterId: string
  name?: string
  isolationMode?: IsolationMode
}

export async function updateVCluster(req: UpdateVClusterRequest): Promise<JobRef> {
  const body: Record<string, unknown> = {}
  if (req.name !== undefined) body.name = req.name
  if (req.isolationMode !== undefined) body.isolationMode = req.isolationMode
  return patchJSON(
    `${API_BASE}/v1/deployments/${encodeURIComponent(req.deploymentId)}/infrastructure/vclusters/${encodeURIComponent(req.vclusterId)}`,
    body,
  )
}

/* ── Node Pool ──────────────────────────────────────────────────── */

export interface AddNodePoolRequest {
  deploymentId: string
  clusterId: string
  sku: string
  replicas: number
}

export async function addNodePool(req: AddNodePoolRequest): Promise<JobRef> {
  return postJSON(
    `${API_BASE}/v1/deployments/${encodeURIComponent(req.deploymentId)}/infrastructure/clusters/${encodeURIComponent(req.clusterId)}/pools`,
    { sku: req.sku, replicas: req.replicas },
  )
}

export interface ScalePoolRequest {
  deploymentId: string
  poolId: string
  replicas: number
}

export async function scalePool(req: ScalePoolRequest): Promise<JobRef> {
  return patchJSON(
    `${API_BASE}/v1/deployments/${encodeURIComponent(req.deploymentId)}/infrastructure/pools/${encodeURIComponent(req.poolId)}`,
    { replicas: req.replicas },
  )
}

export interface ChangeSKURequest {
  deploymentId: string
  poolId: string
  newSku: string
}

export async function changePoolSKU(req: ChangeSKURequest): Promise<JobRef> {
  return patchJSON(
    `${API_BASE}/v1/deployments/${encodeURIComponent(req.deploymentId)}/infrastructure/pools/${encodeURIComponent(req.poolId)}`,
    { sku: req.newSku },
  )
}

/** Unified Update for a NodePool — combines replicas + sku into a
 *  single PATCH call. Either field may be omitted to keep it
 *  unchanged. */
export interface UpdateNodePoolRequest {
  deploymentId: string
  poolId: string
  sku?: string
  replicas?: number
}

export async function updateNodePool(req: UpdateNodePoolRequest): Promise<JobRef> {
  const body: Record<string, unknown> = {}
  if (req.sku !== undefined) body.sku = req.sku
  if (req.replicas !== undefined) body.desiredSize = req.replicas
  return patchJSON(
    `${API_BASE}/v1/deployments/${encodeURIComponent(req.deploymentId)}/infrastructure/pools/${encodeURIComponent(req.poolId)}`,
    body,
  )
}

/* ── Worker Node ────────────────────────────────────────────────── */

export interface AddWorkerNodeRequest {
  deploymentId: string
  clusterId: string
  name: string
  sku: string
  role: 'worker' | 'control-plane'
}

export async function addWorkerNode(req: AddWorkerNodeRequest): Promise<JobRef> {
  return postJSON(
    `${API_BASE}/v1/deployments/${encodeURIComponent(req.deploymentId)}/infrastructure/clusters/${encodeURIComponent(req.clusterId)}/nodes`,
    { name: req.name, sku: req.sku, role: req.role },
  )
}

export interface UpdateWorkerNodeRequest {
  deploymentId: string
  nodeId: string
  /** Resize machine type. Triggers a rolling replace. */
  sku?: string
  /** Comma-separated `key=value:effect` taints to apply. */
  taints?: string
  /** Comma-separated `key=value` labels to apply. */
  labels?: string
}

export async function updateWorkerNode(req: UpdateWorkerNodeRequest): Promise<JobRef> {
  const body: Record<string, unknown> = {}
  if (req.sku !== undefined) body.sku = req.sku
  if (req.taints !== undefined) body.taints = req.taints
  if (req.labels !== undefined) body.labels = req.labels
  return patchJSON(
    `${API_BASE}/v1/deployments/${encodeURIComponent(req.deploymentId)}/infrastructure/nodes/${encodeURIComponent(req.nodeId)}`,
    body,
  )
}

/* ── Load Balancer ──────────────────────────────────────────────── */

export interface AddLBRequest {
  deploymentId: string
  regionId: string
  name: string
  listeners: { port: number; protocol: string }[]
}

export async function addLB(req: AddLBRequest): Promise<JobRef> {
  return postJSON(
    `${API_BASE}/v1/deployments/${encodeURIComponent(req.deploymentId)}/infrastructure/loadbalancers`,
    {
      regionId: req.regionId,
      name: req.name,
      listeners: req.listeners,
    },
  )
}

/** Patch fields supported on a LoadBalancer: rename + listener set
 *  rewrite. Region is immutable (cloud-side identity). */
export interface UpdateLBRequest {
  deploymentId: string
  lbId: string
  name?: string
  listeners?: { port: number; protocol: string }[]
}

export async function updateLB(req: UpdateLBRequest): Promise<JobRef> {
  const body: Record<string, unknown> = {}
  if (req.name !== undefined) body.name = req.name
  if (req.listeners !== undefined) body.listeners = req.listeners
  return patchJSON(
    `${API_BASE}/v1/deployments/${encodeURIComponent(req.deploymentId)}/infrastructure/loadbalancers/${encodeURIComponent(req.lbId)}`,
    body,
  )
}

/* ── Network (VPC / DRG) ─────────────────────────────────────────── */

export interface AddNetworkRequest {
  deploymentId: string
  regionId: string
  cidr: string
  name: string
}

export async function addNetwork(req: AddNetworkRequest): Promise<JobRef> {
  return postJSON(
    `${API_BASE}/v1/deployments/${encodeURIComponent(req.deploymentId)}/infrastructure/networks`,
    { regionId: req.regionId, cidr: req.cidr, name: req.name },
  )
}

export interface UpdateNetworkRequest {
  deploymentId: string
  networkId: string
  /** Rename. CIDR is immutable post-create. */
  name?: string
}

export async function updateNetwork(req: UpdateNetworkRequest): Promise<JobRef> {
  const body: Record<string, unknown> = {}
  if (req.name !== undefined) body.name = req.name
  return patchJSON(
    `${API_BASE}/v1/deployments/${encodeURIComponent(req.deploymentId)}/infrastructure/networks/${encodeURIComponent(req.networkId)}`,
    body,
  )
}

/* ── Persistent Volume Claim ─────────────────────────────────────── */

export interface AddPVCRequest {
  deploymentId: string
  name: string
  namespace: string
  capacity: string
  storageClass: string
}

export async function addPVC(req: AddPVCRequest): Promise<JobRef> {
  return postJSON(
    `${API_BASE}/v1/deployments/${encodeURIComponent(req.deploymentId)}/infrastructure/pvcs`,
    {
      name: req.name,
      namespace: req.namespace,
      capacity: req.capacity,
      storageClass: req.storageClass,
    },
  )
}

export interface UpdatePVCRequest {
  deploymentId: string
  pvcId: string
  /** Expand only — Kubernetes PVCs do not support shrink. */
  capacity?: string
}

export async function updatePVC(req: UpdatePVCRequest): Promise<JobRef> {
  const body: Record<string, unknown> = {}
  if (req.capacity !== undefined) body.capacity = req.capacity
  return patchJSON(
    `${API_BASE}/v1/deployments/${encodeURIComponent(req.deploymentId)}/infrastructure/pvcs/${encodeURIComponent(req.pvcId)}`,
    body,
  )
}

/* ── Object Bucket ───────────────────────────────────────────────── */

export interface AddBucketRequest {
  deploymentId: string
  name: string
  capacity: string
  retentionDays: string
}

export async function addBucket(req: AddBucketRequest): Promise<JobRef> {
  return postJSON(
    `${API_BASE}/v1/deployments/${encodeURIComponent(req.deploymentId)}/infrastructure/buckets`,
    { name: req.name, capacity: req.capacity, retentionDays: req.retentionDays },
  )
}

export interface UpdateBucketRequest {
  deploymentId: string
  bucketId: string
  capacity?: string
  retentionDays?: string
}

export async function updateBucket(req: UpdateBucketRequest): Promise<JobRef> {
  const body: Record<string, unknown> = {}
  if (req.capacity !== undefined) body.capacity = req.capacity
  if (req.retentionDays !== undefined) body.retentionDays = req.retentionDays
  return patchJSON(
    `${API_BASE}/v1/deployments/${encodeURIComponent(req.deploymentId)}/infrastructure/buckets/${encodeURIComponent(req.bucketId)}`,
    body,
  )
}

/* ── Cloud Volume (Hetzner-style block volume) ───────────────────── */

export interface AddVolumeRequest {
  deploymentId: string
  regionId: string
  name: string
  capacity: string
}

export async function addVolume(req: AddVolumeRequest): Promise<JobRef> {
  return postJSON(
    `${API_BASE}/v1/deployments/${encodeURIComponent(req.deploymentId)}/infrastructure/volumes`,
    { regionId: req.regionId, name: req.name, capacity: req.capacity },
  )
}

export interface UpdateVolumeRequest {
  deploymentId: string
  volumeId: string
  /** Resize (cloud volumes generally support expand only). */
  capacity?: string
  /** Attach/detach by setting attachedTo to a node id, or '' to detach. */
  attachedTo?: string
}

export async function updateVolume(req: UpdateVolumeRequest): Promise<JobRef> {
  const body: Record<string, unknown> = {}
  if (req.capacity !== undefined) body.capacity = req.capacity
  if (req.attachedTo !== undefined) body.attachedTo = req.attachedTo
  return patchJSON(
    `${API_BASE}/v1/deployments/${encodeURIComponent(req.deploymentId)}/infrastructure/volumes/${encodeURIComponent(req.volumeId)}`,
    body,
  )
}

/* ── Peering ────────────────────────────────────────────────────── */

export interface AddPeeringRequest {
  deploymentId: string
  fromVpcId: string
  toVpcId: string
  subnets: string
}

export async function addPeering(req: AddPeeringRequest): Promise<JobRef> {
  return postJSON(
    `${API_BASE}/v1/deployments/${encodeURIComponent(req.deploymentId)}/infrastructure/peerings`,
    { fromVpcId: req.fromVpcId, toVpcId: req.toVpcId, subnets: req.subnets },
  )
}

/* ── Firewall Rules ─────────────────────────────────────────────── */

export interface FirewallRulePayload {
  protocol: string
  port: string
  source: string
  action: 'allow' | 'deny'
}

export interface AddFirewallRuleRequest {
  deploymentId: string
  firewallId: string
  rule: FirewallRulePayload
}

export async function addFirewallRule(
  req: AddFirewallRuleRequest,
): Promise<JobRef> {
  return postJSON(
    `${API_BASE}/v1/deployments/${encodeURIComponent(req.deploymentId)}/infrastructure/firewalls/${encodeURIComponent(req.firewallId)}/rules`,
    req.rule,
  )
}

/* ── DNS Zone Records ───────────────────────────────────────────── */

export interface DNSRecordPayload {
  name: string
  type: string
  value: string
  ttl: number
}

export interface EditDNSRecordsRequest {
  deploymentId: string
  zoneId: string
  records: DNSRecordPayload[]
}

export async function editDNSRecords(
  req: EditDNSRecordsRequest,
): Promise<JobRef> {
  return patchJSON(
    `${API_BASE}/v1/deployments/${encodeURIComponent(req.deploymentId)}/infrastructure/dns-zones/${encodeURIComponent(req.zoneId)}`,
    { records: req.records },
  )
}

/* ── Node actions (cordon / drain / replace) ────────────────────── */

export type NodeAction = 'cordon' | 'drain' | 'replace'

export interface NodeActionRequest {
  deploymentId: string
  nodeId: string
  action: NodeAction
}

export async function nodeAction(req: NodeActionRequest): Promise<JobRef> {
  return postJSON(
    `${API_BASE}/v1/deployments/${encodeURIComponent(req.deploymentId)}/infrastructure/nodes/${encodeURIComponent(req.nodeId)}/${req.action}`,
    {},
  )
}

/* ── PVC actions (snapshot / expand) ────────────────────────────── */

export type PVCAction = 'snapshot' | 'expand'

export interface PVCActionRequest {
  deploymentId: string
  pvcId: string
  action: PVCAction
  /** Required for `expand` — Kubernetes capacity string (e.g. "20Gi"). */
  newCapacity?: string
}

export async function pvcAction(req: PVCActionRequest): Promise<JobRef> {
  return postJSON(
    `${API_BASE}/v1/deployments/${encodeURIComponent(req.deploymentId)}/infrastructure/pvcs/${encodeURIComponent(req.pvcId)}/${req.action}`,
    req.newCapacity ? { capacity: req.newCapacity } : {},
  )
}

/* ── Cascade-aware delete ───────────────────────────────────────── */

export type DeletableResource =
  | 'regions'
  | 'clusters'
  | 'vclusters'
  | 'pools'
  | 'loadbalancers'
  | 'peerings'
  | 'firewalls'
  | 'dns-zones'
  | 'pvcs'
  | 'volumes'
  | 'buckets'
  | 'networks'
  | 'nodes'

export interface CascadeDeleteRequest {
  deploymentId: string
  resource: DeletableResource
  resourceId: string
}

export async function cascadeDelete(req: CascadeDeleteRequest): Promise<JobRef> {
  const url = `${API_BASE}/v1/deployments/${encodeURIComponent(req.deploymentId)}/infrastructure/${req.resource}/${encodeURIComponent(req.resourceId)}`
  const res = await fetch(url, {
    method: 'DELETE',
    headers: { Accept: 'application/json' },
  })
  if (!res.ok) throw new Error(`delete ${req.resource}/${req.resourceId} failed: ${res.status}`)
  return (await res.json()) as JobRef
}

export async function previewCascadeDelete(
  req: CascadeDeleteRequest,
): Promise<CascadePreview> {
  const url = `${API_BASE}/v1/deployments/${encodeURIComponent(req.deploymentId)}/infrastructure/${req.resource}/${encodeURIComponent(req.resourceId)}/delete-preview`
  const res = await fetch(url, { headers: { Accept: 'application/json' } })
  if (!res.ok) {
    // Best-effort empty preview when the endpoint isn't deployed yet.
    return { affected: [], estimatedDuration: 'unknown', blockers: [] }
  }
  return (await res.json()) as CascadePreview
}

/* ── Internal helpers ───────────────────────────────────────────── */

async function postJSON<TIn, TOut = JobRef>(url: string, body: TIn): Promise<TOut> {
  const res = await fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
    body: JSON.stringify(body),
  })
  if (!res.ok) throw new Error(`POST ${url} failed: ${res.status}`)
  return (await res.json()) as TOut
}

async function patchJSON<TIn, TOut = JobRef>(url: string, body: TIn): Promise<TOut> {
  const res = await fetch(url, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
    body: JSON.stringify(body),
  })
  if (!res.ok) throw new Error(`PATCH ${url} failed: ${res.status}`)
  return (await res.json()) as TOut
}

/** Feature flag — when false, the modals call the wrappers but the
 *  Catalyst API simply records the action as a no-op job. The UI
 *  ships today; the backend lights up later without a frontend
 *  redeploy. */
export const INFRA_CRUD_LIVE: boolean =
  String(import.meta.env.VITE_INFRA_CRUD_LIVE ?? 'false').toLowerCase() === 'true'
