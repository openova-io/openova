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
 *  the Jobs page. */
export interface JobRef {
  jobId: string
  batchId: string
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
