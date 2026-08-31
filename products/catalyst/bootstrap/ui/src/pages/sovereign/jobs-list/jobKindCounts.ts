/**
 * jobs-list/jobKindCounts.ts — the per-kind count map behind the /jobs
 * chip badges (P1b, Refs #6703).
 *
 * The jobs analogue of cloud-list/kindCounts.ts. Extracted as PRODUCTION
 * code (not a hand-built literal in a test) so a guard can assert on the
 * VALUE the page actually renders — handing JobKindChips a synthetic
 * `counts` prop would prove nothing about what the operator sees.
 *
 * Semantics:
 *   • Every JobKind key is present and starts at 0.
 *   • Each job is tallied under `jobKind(j)` — the canonical "read the
 *     kind, not the name-prefix" accessor (issue #3646). `group` rows are
 *     EXCLUDED (a synthesised parent is not an engine you filter by), so
 *     the `group` bucket stays 0.
 */

import type { Job, JobKind } from '@/lib/jobs.types'
import { jobKind } from '@/lib/jobs.types'

const ALL_JOB_KINDS: readonly JobKind[] = [
  'install',
  'reconcile',
  'step',
  'mutation',
  'cron',
  'task',
  'reconciler',
  'lifecycle',
  'group',
]

export function deriveJobKindCounts(jobs: readonly Job[]): Record<JobKind, number> {
  const counts = {} as Record<JobKind, number>
  for (const k of ALL_JOB_KINDS) counts[k] = 0
  for (const j of jobs) {
    const k = jobKind(j)
    // Groups are synthesised parents — never a filterable engine.
    if (k === 'group') continue
    counts[k] = (counts[k] ?? 0) + 1
  }
  return counts
}

/**
 * Every kind mapped to `null` — the "not counted yet" state. A `null` count
 * renders as an em-dash "—" in the dropdown / chip strip, NOT as "(0)".
 *
 * Used while the authoritative backend list is still loading: the counts must
 * NEVER be derived from the reducer first-paint (the SSE active-tail), which
 * during a cutover shows re-running OpenTofu and no completed installs — i.e.
 * "OpenTofu (64) / HelmRelease (0)". Showing "—" until the real list lands is
 * honest ("loading"); showing the reducer's tally is a wrong number.
 */
export function nullJobKindCounts(): Record<JobKind, null> {
  const counts = {} as Record<JobKind, null>
  for (const k of ALL_JOB_KINDS) counts[k] = null
  return counts
}
