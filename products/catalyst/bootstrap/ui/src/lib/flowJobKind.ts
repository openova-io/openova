/**
 * flowJobKind — derive a chip "kind" from an openova-flow node so the
 * /jobs graph chip strip can FILTER the canvas by kind.
 *
 * The flow stream's nodes do not carry a `kind` field (unlike the job-store
 * rows), so the kind is inferred from the node id prefix (authoritative:
 * `install-*`, `reconcile-*`, `cutover-*`, `task-*`, `cron-*`, `tofu-*`, …)
 * with the human label as a fallback. The result aligns with the /jobs
 * JobKind chip catalogue (jobs-list/jobKinds.ts) so a chip and the nodes it
 * governs use the same identifier.
 */

import type { Job } from './jobs.types'

/** The chip kinds the /jobs graph can filter by (JobKind minus `group`). */
export type GraphKind =
  | 'install'
  | 'reconcile'
  | 'reconciler'
  | 'cron'
  | 'step'
  | 'task'
  | 'mutation'
  | 'lifecycle'

/** Ordered id-prefix → GraphKind rules. `reconciler` before `reconcile`,
 *  since "reconciler" starts with "reconcile". */
const PREFIX_RULES: ReadonlyArray<[RegExp, GraphKind]> = [
  [/(^|[:-])reconciler(s)?[-:]/, 'reconciler'],
  [/(^|[:-])reconcile[-:]/, 'reconcile'],
  [/(^|[:-])install[-:]/, 'install'],
  [/(^|[:-])cutover[-:]/, 'step'],
  [/(^|[:-])cron[-:]/, 'cron'],
  [/(^|[:-])task[-:]/, 'task'],
  [/(^|[:-])(tofu|terraform|provisioner)[-:]/, 'lifecycle'],
  [/(^|[:-])(mutation|crossplane|xrc)[-:]/, 'mutation'],
]

const LABEL_RULES: ReadonlyArray<[RegExp, GraphKind]> = [
  [/^reconcile\b/i, 'reconcile'],
  [/^install\b/i, 'install'],
  [/^cutover\b/i, 'step'],
  [/^terraform\b|^tofu\b|^provision/i, 'lifecycle'],
  [/\(task\)/i, 'task'],
  [/\(step\)/i, 'step'],
  [/\bcron\b/i, 'cron'],
]

/**
 * Best-effort chip kind for a flow node. Falls back to `task` (a generic
 * standalone job) when nothing matches, so an unknown node is never
 * silently hidden by every kind-filter.
 */
export function flowJobKind(job: Pick<Job, 'id' | 'jobName' | 'displayName'>): GraphKind {
  const id = (job.id ?? '').toLowerCase()
  for (const [re, kind] of PREFIX_RULES) {
    if (re.test(id)) return kind
  }
  const label = job.displayName ?? job.jobName ?? ''
  for (const [re, kind] of LABEL_RULES) {
    if (re.test(label)) return kind
  }
  return 'task'
}
