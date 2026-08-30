/**
 * JobsGraphView — the Graph half of the /jobs List⇄Graph toggle
 * (Refs #6703), rendered with the CANONICAL natural-view DAG
 * `FlowCanvasOrganic` — the same force-directed job-tree canvas the rest
 * of the operator UX is tuned against (FlowPage.tsx header; the founder
 * ratified it after rejecting the lane-layout scaffolding). It is NOT a
 * bespoke SVG renderer: this view reuses `flowLayoutOrganic` + the fold /
 * depth helpers exactly as FlowPage does, fed from the SAME job-store
 * `Job[]` the /jobs table renders (via `jobsToOrganicInputs`), so list
 * and graph never diverge.
 *
 * Groups (`type:'group'`) render as fold-collapsible bubbles with a
 * child-count badge — that IS the "default gathered relevant groups"
 * view (bootstrap-kit / provisioner / cutover / … each a bubble). Double-
 * click or the disclosure badge folds/unfolds; a node click opens the
 * job's detail page (same chroot/mothership link logic as JobsTable).
 *
 * A `highlightKind` (driven by the graph-view chip strip) dims every
 * bubble that is NOT that kind — highlight, never remove, so dependency
 * edges are never severed mid-chain.
 */

import { useCallback, useMemo, useState } from 'react'
import { useNavigate, useParams } from '@tanstack/react-router'
import { DETECTED_MODE } from '@/shared/lib/detectMode'
import type { Job, JobKind } from '@/lib/jobs.types'
import { flowLayoutOrganic } from '@/lib/flowLayoutOrganic'
import { jobsToOrganicInputs } from '@/lib/jobsToOrganic'
import {
  defaultFoldedAtContainmentDepth,
  descendantCountByGroup,
} from '@/lib/flowStreamToOrganic'
import { FlowCanvasOrganic, type FlowOrganicAction } from './FlowCanvasOrganic'

interface JobsGraphViewProps {
  /** The flat Job tree to draw (groups + leaves) — same list as the table. */
  jobs: readonly Job[]
  /** Deployment id — forwarded for parity; the link builder reads the
   *  route param + DETECTED_MODE, matching JobsTable. */
  deploymentId?: string
  /** Active graph chip — dims every bubble not of this kind. null = no
   *  highlight (all bubbles at full opacity). */
  highlightKind?: JobKind | null
}

/**
 * Default fold depth for the /jobs graph: fold groups at containment
 * depth ≥ 2, so the top-level orchestration units (bootstrap-kit,
 * provisioner, cutover, …) render as gathered bubbles with child-count
 * badges rather than exploding every leaf on first paint.
 */
const DEFAULT_FOLD_DEPTH = 2

const GROUP_NODE_ACTIONS: readonly FlowOrganicAction[] = [
  { id: 'fold', label: 'Fold' },
  { id: 'unfold', label: 'Unfold' },
]

export function JobsGraphView({ jobs, highlightKind }: JobsGraphViewProps) {
  const navigate = useNavigate()
  const params = useParams({ strict: false }) as { deploymentId?: string }
  const isSovereign = DETECTED_MODE.mode === 'sovereign'
  const depId = params.deploymentId ?? ''

  // Fold state — start with the deep groups gathered (folded). Reset only
  // when the set of group ids changes (not on every status tick).
  const groupSignature = useMemo(
    () =>
      jobs
        .filter((j) => j.type === 'group')
        .map((j) => j.id)
        .sort()
        .join('|'),
    [jobs],
  )
  const [folded, setFolded] = useState<Set<string>>(
    () => defaultFoldedAtContainmentDepth(jobs, DEFAULT_FOLD_DEPTH),
  )
  // Re-seed the fold set when the group TOPOLOGY changes (new prov /
  // reseed) — the React-sanctioned "adjust state during render" pattern
  // (store the previous signature in state; a status-only tick keeps the
  // same signature so the operator's manual fold/unfold survives).
  const [prevSig, setPrevSig] = useState(groupSignature)
  if (prevSig !== groupSignature) {
    setPrevSig(groupSignature)
    setFolded(defaultFoldedAtContainmentDepth(jobs, DEFAULT_FOLD_DEPTH))
  }

  const inputs = useMemo(() => jobsToOrganicInputs(jobs), [jobs])

  const layout = useMemo(
    () =>
      flowLayoutOrganic(jobs, {
        hints: inputs.hints,
        regions: inputs.regions,
        families: inputs.families,
        folded,
      }),
    [jobs, inputs, folded],
  )

  const badgeCounts = useMemo(
    () => descendantCountByGroup(jobs, folded),
    [jobs, folded],
  )

  const hasGroups = inputs.groupIds.size > 0

  const toggleFold = useCallback((jobId: string) => {
    setFolded((prev) => {
      const next = new Set(prev)
      if (next.has(jobId)) next.delete(jobId)
      else next.add(jobId)
      return next
    })
  }, [])

  const handleNodeAction = useCallback(
    (jobId: string, actionId: string) => {
      setFolded((prev) => {
        const next = new Set(prev)
        if (actionId === 'fold') next.add(jobId)
        else if (actionId === 'unfold') next.delete(jobId)
        return next
      })
    },
    [],
  )

  // SAME chroot/mothership target logic as JobsTable.useJobLinkBuilder:
  // strip the "<deploymentId>:" (or "<deploymentId>:<region>:") prefix so
  // the id matches jobs.Store's bare jobName key, encode the segment, and
  // stay scoped under /provision/<id>/jobs on the mothership monitor.
  const onJobClick = useCallback(
    (jobId: string) => {
      const bare = jobId.includes(':')
        ? jobId.slice(jobId.indexOf(':') + 1)
        : jobId
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
    <div data-testid="jobs-graph-view" className="jobs-graph-view">
      <style>{JOBS_GRAPH_VIEW_CSS}</style>
      <FlowCanvasOrganic
        layout={layout}
        openJobId={null}
        hostJobId={null}
        highlightFamilyId={highlightKind ?? null}
        onJobClick={(jobId) => onJobClick(jobId)}
        onJobDoubleClick={(jobId) => toggleFold(jobId)}
        onCanvasBackgroundClick={() => {}}
        onFoldToggle={hasGroups ? toggleFold : undefined}
        badgeCounts={badgeCounts}
        nodeActions={hasGroups ? GROUP_NODE_ACTIONS : undefined}
        onNodeAction={hasGroups ? handleNodeAction : undefined}
      />
    </div>
  )
}

const JOBS_GRAPH_VIEW_CSS = `
.jobs-graph-view {
  position: relative;
  width: 100%;
  height: min(72vh, 760px);
  min-height: 420px;
  border: 1px solid var(--color-border);
  border-radius: 10px;
  background: var(--color-bg-2);
  overflow: hidden;
}
`
