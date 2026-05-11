/**
 * flow-bridge — single-responsibility shim translating the legacy
 * Catalyst Job tree into the OpenovaFlow contract
 * (`FlowInstance` + `FlowNode[]` + `Relationship[]`).
 *
 * # Why this file is the ONLY place that knows about Job
 *
 * The OpenovaFlow Foundation refactor (2026-05-11) split visualisation
 * out of Catalyst into the standalone @openova/flow-core + @openova/
 * flow-canvas packages. Those packages know nothing about
 * `parentId`/`dependsOn`/`appId` — they speak FlowNode + Relationship.
 *
 * Agent #2 will ship a proper FlowAdapter against catalyst-api that
 * emits FlowMessage events directly; Agent #3 will replace this bridge
 * with that adapter. Until then, the bridge keeps catalyst-ui working
 * against the existing `/api/v1/deployments/{id}/jobs` endpoint.
 *
 * # Mapping
 *
 *   Job.parentId             → Relationship { fromId:job, toId:parent, type:'contains' }
 *   Job.dependsOn[i]         → Relationship { fromId:deps[i], toId:job, type:'finish-to-start', condition:'on-success' }
 *   Job.appId                → FlowNode.meta.appId (preserved opaque)
 *   Job.id                   → FlowNode.id
 *   Job.status               → FlowNode.status
 *   Job.startedAt / Job.finishedAt (ISO) → unix ms timestamps
 *   Job.type ('install' | 'group')        → encoded via `meta.kind`; group-ness is now
 *                                          inferred from contains-edges anyway, so the
 *                                          bridge only needs to preserve the kind for
 *                                          downstream consumers that still distinguish.
 *
 * # Region/family hints
 *
 * The legacy code computed region+family hints at the layout-input
 * boundary (`useJobHints`). The bridge does NOT compute them — it
 * forwards the upstream hint computation to the new layout's
 * `perNodeHints` slot. Catalyst-ui still owns the `applicationCatalog`
 * + `BLUEPRINT_DEPS` resolution; the bridge is only the shape adapter.
 */

import type {
  FlowInstance,
  FlowNode,
  Relationship,
} from '@openova/flow-core'
import type { Job, JobStatus } from './jobs.types'

/**
 * Translate a `Job[]` into FlowNode[] + Relationship[] for the given
 * flowId. The caller supplies the FlowInstance separately (it carries
 * deployment status + startedAt; those don't come from individual
 * jobs).
 *
 * Determinism: nodes and relationships are emitted in the same order
 * as the input `jobs` array (and inside each job, parent-rel comes
 * before depends-on rels). Stable across reloads.
 */
export function adaptJobsToFlow(
  jobs: readonly Job[],
  flowId: string,
): { nodes: FlowNode[]; relationships: Relationship[] } {
  const nodes: FlowNode[] = []
  const relationships: Relationship[] = []
  const knownIds = new Set<string>()
  for (const j of jobs) knownIds.add(j.id)

  for (const j of jobs) {
    nodes.push({
      id: j.id,
      flowId,
      label: labelFor(j),
      status: j.status,
      startedAt: parseIsoToMs(j.startedAt),
      endedAt: parseIsoToMs(j.finishedAt),
      meta: {
        kind: j.type, // 'install' | 'group'
        appId: j.appId,
        jobName: j.jobName,
        displayName: j.displayName ?? '',
        durationMs: j.durationMs,
        // legacy parentId / dependsOn copied through opaque for
        // anyone who still wants to read them via meta.
        parentId: j.parentId,
        dependsOn: [...j.dependsOn],
      },
    })

    // contains edge — job is contained by its parent.
    if (j.parentId && knownIds.has(j.parentId)) {
      relationships.push({
        fromId: j.id,
        toId: j.parentId,
        type: 'contains',
      })
    }

    // finish-to-start / on-success for each dependsOn entry.
    for (const depId of j.dependsOn) {
      if (depId === j.id) continue
      if (!knownIds.has(depId)) continue
      relationships.push({
        fromId: depId,
        toId: j.id,
        type: 'finish-to-start',
        condition: 'on-success',
      })
    }
  }

  return { nodes, relationships }
}

/**
 * Convenience: build the full triple (FlowInstance + nodes + rels)
 * suitable for passing straight into FlowCanvas / layout().
 *
 *   • `flowId` — typically the deploymentId.
 *   • `definitionId` — optional template id (the upstream
 *                       "applicationCatalogVersion" or similar). The
 *                       UI uses this for "DAG vs DAG-run" disambiguation
 *                       when the catalyst-api begins emitting it.
 */
export function buildFlowFromJobs(args: {
  jobs: readonly Job[]
  flowId: string
  definitionId?: string
  flowStatus?: string
  startedAt?: number
  endedAt?: number
}): { flow: FlowInstance; nodes: FlowNode[]; relationships: Relationship[] } {
  const { jobs, flowId, definitionId, flowStatus, startedAt, endedAt } = args
  const { nodes, relationships } = adaptJobsToFlow(jobs, flowId)
  const rolledStatus = flowStatus ?? rollupStatus(jobs)
  const inferredStartedAt = startedAt ?? earliestStarted(jobs) ?? 0
  return {
    flow: {
      id: flowId,
      definitionId,
      status: rolledStatus,
      startedAt: inferredStartedAt,
      endedAt,
    },
    nodes,
    relationships,
  }
}

/* ────────────────────────────────────────────────────────────────────
 * Helpers
 * ──────────────────────────────────────────────────────────────────── */

function labelFor(j: Job): string {
  if (j.displayName && j.displayName.length > 0) return j.displayName
  return j.jobName.replace(/^install-/, '')
}

function parseIsoToMs(iso: string | null | undefined): number | undefined {
  if (!iso) return undefined
  const t = Date.parse(iso)
  return Number.isFinite(t) ? t : undefined
}

function rollupStatus(jobs: readonly Job[]): JobStatus {
  if (jobs.length === 0) return 'pending'
  const leafJobs = jobs.filter((j) => j.type !== 'group')
  if (leafJobs.length === 0) return 'pending'
  const buckets = new Set<JobStatus>(leafJobs.map((j) => j.status))
  if (buckets.has('failed')) {
    const allTerminal = leafJobs.every((j) => j.status === 'succeeded' || j.status === 'failed')
    return allTerminal ? 'failed' : 'running'
  }
  if (buckets.has('running') || buckets.has('pending')) return 'running'
  return 'succeeded'
}

function earliestStarted(jobs: readonly Job[]): number | null {
  let earliest: number | null = null
  for (const j of jobs) {
    if (!j.startedAt) continue
    const t = Date.parse(j.startedAt)
    if (!Number.isFinite(t)) continue
    if (earliest === null || t < earliest) earliest = t
  }
  return earliest
}
