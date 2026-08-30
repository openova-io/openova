/**
 * JobsGraphView — the Graph half of the /jobs List⇄Graph toggle (P1b,
 * Refs #6703). A thin wrapper that maps the flat Job tree to the pure-SVG
 * layered-DAG renderer `JobDependenciesGraph` and wires node clicks to the
 * per-job detail page using the SAME chroot/mothership path logic the
 * JobsTable rows use (JobsTable.useJobLinkBuilder).
 *
 * Two mappings the widget requires:
 *   • Title — `displayName ?? jobName` (the same label the table row link
 *     renders), so a node reads identically to its table row.
 *   • Status — JobStatus has SEVEN values but JobDependenciesGraph only
 *     knows FOUR (pending | running | succeeded | failed). The HEALTH-axis
 *     statuses are folded onto the one-shot axis: healthy → succeeded,
 *     degraded / failing → failed. (issue #3646 §4c health axis.)
 */

import { useCallback } from 'react'
import { useNavigate, useParams } from '@tanstack/react-router'
import { DETECTED_MODE } from '@/shared/lib/detectMode'
import type { Job, JobStatus } from '@/lib/jobs.types'
import {
  JobDependenciesGraph,
  type JobNode,
  type JobUiStatus,
} from '@/widgets/job-deps-graph/JobDependenciesGraph'

interface JobsGraphViewProps {
  /** The flat Job tree to draw (groups + leaves). */
  jobs: readonly Job[]
  /** Deployment id — forwarded for parity; the link builder reads the
   *  route param + DETECTED_MODE, matching JobsTable. */
  deploymentId?: string
}

/**
 * Fold the 7-value JobStatus onto the 4-value graph vocabulary. The
 * health axis (issue #3646 §4c) collapses to the one-shot axis:
 *   healthy  → succeeded   (running-forever and that's correct)
 *   degraded → failed      (running-forever and that's a hang)
 *   failing  → failed
 */
function toGraphStatus(s: JobStatus): JobUiStatus {
  switch (s) {
    case 'running':
      return 'running'
    case 'succeeded':
    case 'healthy':
      return 'succeeded'
    case 'failed':
    case 'degraded':
    case 'failing':
      return 'failed'
    case 'pending':
    default:
      return 'pending'
  }
}

export function JobsGraphView({ jobs }: JobsGraphViewProps) {
  const navigate = useNavigate()
  const params = useParams({ strict: false }) as { deploymentId?: string }
  const isSovereign = DETECTED_MODE.mode === 'sovereign'
  const depId = params.deploymentId ?? ''

  const nodes: JobNode[] = jobs.map((j) => ({
    id: j.id,
    title: j.displayName ?? j.jobName,
    status: toGraphStatus(j.status),
    dependsOn: j.dependsOn,
  }))

  // SAME chroot/mothership target logic as JobsTable.useJobLinkBuilder:
  // strip the "<deploymentId>:" (or "<deploymentId>:<region>:") prefix so
  // the id matches jobs.Store's bare jobName key, encode the segment (the
  // `/` inside a multi-region bare name must stay path-safe), and stay
  // scoped under /provision/<id>/jobs on the mothership monitor surface.
  const onNodeClick = useCallback(
    (jobId: string) => {
      const bare = jobId.includes(':') ? jobId.slice(jobId.indexOf(':') + 1) : jobId
      const encoded = encodeURIComponent(bare)
      const to =
        isSovereign || !depId
          ? `/jobs/${encoded}`
          : `/provision/${depId}/jobs/${encoded}`
      navigate({ to: to as never })
    },
    [navigate, isSovereign, depId],
  )

  return (
    <div data-testid="jobs-graph-view">
      <JobDependenciesGraph jobs={nodes} onNodeClick={onNodeClick} />
    </div>
  )
}
