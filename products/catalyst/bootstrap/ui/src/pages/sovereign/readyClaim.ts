/**
 * readyClaim.ts — may this page say "Sovereign is ready"?
 *
 * The ready banners of the Apps page and the Jobs page used to follow one
 * signal: the hand-over event. On 15 November 2026 a two-region deployment
 * fired its hand-over while the follower region's releases were still
 * installing or failed, so the parent's console said "Sovereign is ready"
 * above rows that read Running, and the Jobs page counted down to a redirect
 * over them (#7902 defects 6 and 8).
 *
 * The API no longer fires the hand-over before every region is installed.
 * This check is the console's own half: it reads the SAME record and the SAME
 * rows the page renders, so the banner and the page under it cannot disagree
 * whatever a record says (a record written by an older API can still carry a
 * hand-over next to a region that is short).
 *
 * The claim holds unless one of these says otherwise:
 *   1. the record's per-region census has a region with releases that are
 *      not installed (`regions[].hrReady < hrTotal`);
 *   2. the record flags a follower region as behind (`secondaryDegraded`);
 *   3. a release install row of the given list is running, pending or failed.
 *
 * Absent data is not evidence: no census and no rows means nothing
 * contradicts the hand-over, and the claim holds.
 *
 * NON-COMPONENT module by design (react-refresh/only-export-components).
 */

import { jobKind, type Job } from '@/lib/jobs.types'
import type { DeploymentSnapshot } from './useDeploymentEvents'

export interface ReadyClaim {
  /** True when nothing on the page contradicts "ready". */
  holds: boolean
  /** One plain sentence per contradiction; empty when the claim holds. */
  reasons: string[]
}

/** Install-row statuses that contradict "ready". `dormant` (a suspended
 *  release) and the terminal/health statuses do not. */
const UNFINISHED_INSTALL: ReadonlySet<string> = new Set(['running', 'pending', 'failed'])

export function readyClaim(
  snapshot: DeploymentSnapshot | null | undefined,
  jobs?: readonly Job[],
): ReadyClaim {
  const reasons: string[] = []

  const regions = snapshot?.regions ?? []
  const short = regions.filter((r) => (r.hrTotal ?? 0) > 0 && (r.hrReady ?? 0) < (r.hrTotal ?? 0))
  for (const r of short) {
    reasons.push(`Region ${r.region}: ${r.hrReady} of ${r.hrTotal} releases installed`)
  }
  if (snapshot?.secondaryDegraded === true && short.length === 0) {
    reasons.push('A follower region is behind the primary region')
  }

  if (jobs && jobs.length > 0) {
    const unfinished = jobs.filter(
      (j) => j.type !== 'group' && jobKind(j) === 'install' && UNFINISHED_INSTALL.has(j.status),
    )
    if (unfinished.length > 0) {
      const failed = unfinished.filter((j) => j.status === 'failed').length
      const inFlight = unfinished.length - failed
      const parts: string[] = []
      if (inFlight > 0) parts.push(`${inFlight} still installing`)
      if (failed > 0) parts.push(`${failed} failed`)
      reasons.push(`Release installs: ${parts.join(', ')}`)
    }
  }

  return { holds: reasons.length === 0, reasons }
}
