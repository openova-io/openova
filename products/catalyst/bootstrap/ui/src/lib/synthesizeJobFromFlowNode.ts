/**
 * synthesizeJobFromFlowNode — adapt an OpenovaFlow FlowNode to a flat Job.
 *
 * Why this exists:
 *   JobDetail's primary lookup path is the legacy event-stream reducer +
 *   live-jobs backfill (`useDeploymentEvents` + `useLiveJobsBackfill`).
 *   For Sovereigns whose state lives ONLY in the OpenovaFlow snapshot —
 *   post-flux-only provisions, fresh chroot consoles before the legacy
 *   bridge has emitted any rows — that lookup misses and JobDetail used
 *   to short-circuit to the "Job not found" panel, NEVER mounting the
 *   FlowPage canvas. The canvas, however, has authoritative state for
 *   those node ids (verified live 2026-05-11: GET /v1/flows/<id>/snapshot
 *   returns 2 leaf nodes that GET /v1/deployments/<id>/jobs/<id> 404s on).
 *
 *   This adapter produces a Job stub from a FlowNode so JobDetail can
 *   render its full surface (header, canvas, log pane) using the flow
 *   snapshot as the source of truth. The synthesized Job is
 *   intentionally minimal — it carries only what JobDetail.tsx + the
 *   embedded FlowPage need to mount: `id`, a display label, a status,
 *   and the required-but-empty navigation fields (`appId`, `parentId`,
 *   `dependsOn`, `childIds`).
 *
 * Single source of truth:
 *   The Job shape lives in `@/lib/jobs.types`. We never re-declare it
 *   here — any future field added there is filled in via TypeScript
 *   structural checks at the call site, not by hand at this layer.
 *
 * Inviolable principles cited (docs/INVIOLABLE-PRINCIPLES.md):
 *   #1 (waterfall): target-state fallback, no MVP "show loading" stub.
 *   #2 (no compromise): no field is faked with plausible data; absent
 *     timestamps land as null / 0 so downstream `fmtTime` formatters
 *     render "—" not a hallucinated time.
 *   #4 (never hardcode): the synthesized type is `'install'` (the only
 *     leaf type) — the call site is responsible for not passing a group
 *     node here. The label falls back to the id when `label` is empty.
 */

import type { FlowNode } from '@openova/flow-core'
import type { Job, JobStatus } from './jobs.types'

/**
 * FlowNode.status is a free-form string per `@openova/flow-core` (the
 * adapter contract allows family-specific status vocabularies). For the
 * canvas-bound JobDetail consumer we want the canonical four-bucket
 * JobStatus. Anything outside the four buckets coerces to `'pending'`
 * so the status chip + LogPane tone resolve cleanly.
 */
const JOB_STATUS_SET = new Set<JobStatus>([
  'pending',
  'running',
  'succeeded',
  'failed',
])

function coerceJobStatus(s: string | undefined): JobStatus {
  if (s && (JOB_STATUS_SET as Set<string>).has(s)) return s as JobStatus
  return 'pending'
}

/**
 * Build a flat {@link Job} stub from a FlowNode. The result is safe to
 * pass anywhere a Job is expected; it intentionally has no children and
 * no execution backing, so the LogPane will fall through to its
 * empty-state / global-stream path naturally.
 */
export function synthesizeJobFromFlowNode(n: FlowNode): Job {
  return {
    id: n.id,
    jobName: n.label || n.id,
    displayName: n.label || undefined,
    type: 'install',
    appId: '',
    parentId: '',
    dependsOn: [],
    childIds: [],
    status: coerceJobStatus(n.status),
    startedAt: null,
    finishedAt: null,
    durationMs: 0,
  }
}
