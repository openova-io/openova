/**
 * JobsGraphView — the Graph half of the /jobs List⇄Graph toggle
 * (Refs #6703), rendered with the CANONICAL natural-view DAG stack the
 * rest of the operator UX is tuned against: the SAME data path FlowPage
 * uses — `useFlowStream` → `flowStreamToOrganic` → `flowLayoutOrganic` →
 * `FlowCanvasOrganic`. This is NOT a bespoke renderer and NOT the flat
 * job-store: the openova-flow stream carries the real `contains` grouping
 * AND `finish-to-start` dependency edges, so the graph shows the actual
 * dependency DAG (bootstrap-kit → its installs, the cutover chain, the
 * Terraform provisioning chain, …) instead of a disconnected grid.
 *
 * Two /jobs-specific behaviours layered on the shared stack:
 *   • Label — the flow labels the install nodes "Install <component>";
 *     the leading verb is stripped so the node reads by COMPONENT
 *     ("Orgdb Rtz A"), matching the founder's component-first rule (the
 *     same fix P1a made for the dashboard treemap).
 *   • Chip filter — `visibleKinds` (driven by the graph chip strip) FILTERS
 *     the graph: a kind whose chip is removed/deselected has its nodes
 *     dropped from the canvas entirely (group containers are kept so the
 *     tree stays readable). Removing the "HelmRelease" chip really removes
 *     the 137 install nodes.
 */

import { useCallback, useMemo, useState } from 'react'
import { useNavigate, useParams } from '@tanstack/react-router'
import { useWizardStore } from '@/entities/deployment/store'
import { DETECTED_MODE } from '@/shared/lib/detectMode'
import type { Job } from '@/lib/jobs.types'
import { flowLayoutOrganic } from '@/lib/flowLayoutOrganic'
import { useFlowStream } from '@/lib/openflow-adapter-sse'
import {
  defaultFoldedAtContainmentDepth,
  descendantCountByGroup,
  flowStreamToOrganic,
} from '@/lib/flowStreamToOrganic'
import { type GraphKind } from '@/lib/flowJobKind'
import { addPhaseBarrierDeps } from '@/lib/phaseBarriers'
import { selectGraphJobs } from '@/lib/graphJobFilter'
import { FlowCanvasOrganic, type FlowOrganicAction } from './FlowCanvasOrganic'

interface JobsGraphViewProps {
  /** Deployment id — forwarded to the flow stream; on the Sovereign chroot
   *  the route param is empty and the stream resolves the implicit id. */
  deploymentId?: string
  /** Kinds whose chips are currently VISIBLE in the strip. A leaf node
   *  whose kind is absent from this set is filtered OUT of the graph.
   *  undefined = no filter (show everything). */
  visibleKinds?: ReadonlySet<GraphKind>
  /** Test seam — disable the live SSE attach (renders the empty state). */
  disableStream?: boolean
}

/** Default = UNFOLDED (founder call): every group is expanded so the FULL
 *  dependency mesh is drawn — all 236 helm→helm `spec.dependsOn` edges plus
 *  the 6 helm→cutover edges that are otherwise collapsed inside a "112 jobs"
 *  bubble. Folding a group becomes a manual action (double-click / the
 *  disclosure badge) to simplify on demand. `'all'` → defaultFolded returns
 *  the empty set (nothing folded). */
const DEFAULT_FOLD_DEPTH: number | 'all' = 'all'

const GROUP_NODE_ACTIONS: readonly FlowOrganicAction[] = [
  { id: 'fold', label: 'Fold' },
  { id: 'unfold', label: 'Unfold' },
]

/** Strip a leading "Install " / "Reconcile " verb so a node reads by its
 *  component (the founder's component-first rule; matches the P1a treemap
 *  fix). Applied only to the /jobs graph — FlowPage/JobDetail unchanged. */
function componentLabel(name: string): string {
  return name.replace(/^(install|reconcile)\s+/i, '').trim() || name
}

export function JobsGraphView({
  deploymentId,
  visibleKinds,
  disableStream = false,
}: JobsGraphViewProps) {
  const navigate = useNavigate()
  const params = useParams({ strict: false }) as { deploymentId?: string }
  const store = useWizardStore()
  const isSovereign = DETECTED_MODE.mode === 'sovereign'
  const depId = params.deploymentId ?? deploymentId ?? ''

  // ── The mature data path: live openova-flow stream → organic inputs ──
  const stream = useFlowStream({ deploymentId: depId, disableStream })

  const adapter = useMemo(
    () =>
      flowStreamToOrganic({
        nodes: [...stream.nodes.values()],
        relationships: [...stream.relationships.values()],
        wizardRegions: store.regions,
      }),
    [stream.nodes, stream.relationships, store.regions],
  )

  // Childless phase containers: a flow node whose family is 'group' but which
  // is NOT a `contains` parent (an empty "Reconcilers" / "Handover" / "Apps"
  // phase) is typed as a leaf by the adapter. Collect their ids so
  // selectGraphJobs can drop them — else they render as stray, often "Failed",
  // nodes with no chip to filter them.
  const emptyContainerIds = useMemo(() => {
    const s = new Set<string>()
    for (const [id, h] of adapter.hints) {
      if (h.familyId === 'group' && !adapter.groupIds.has(id)) s.add(id)
    }
    return s
  }, [adapter.hints, adapter.groupIds])

  // Phase-barrier edges (#6727): the stream's finish-to-start edges are
  // almost entirely SAME-type (236 helm→helm spec.dependsOn, cutover
  // step→step, tofu→tofu). The coupling BETWEEN engine types lives only in
  // the phase order (Provision→Bootstrap→Cutover…) carried by the
  // group→group spine, so without this a HelmRelease renders as an island
  // with no visible link to provisioning. addPhaseBarrierDeps connects each
  // downstream phase's ROOT leaves to the upstream phase's SINK leaves, so
  // the whole graph is one connected DAG — nothing floats — while leaving
  // the transitive closure implicit (no all-pairs hairball).
  const barrieredJobs = useMemo(
    () => addPhaseBarrierDeps(adapter.jobs),
    [adapter.jobs],
  )

  // Finite-scope + chip filter, then component-first labels.
  // `selectGraphJobs` (a) drops the open-ended reconcilers the /jobs LIST
  // also excludes (jobs.FilterFiniteJobs) — the flow stream carries the full
  // DAG incl. ~22 reconcile-* nodes that have no chip, so leaving them in made
  // the graph ignore the chip selection and kept showing them after "remove
  // all chips"; and (b) keeps a group only if it still has a kept leaf
  // descendant. Relabel strips the leading verb so nodes read by component.
  const jobs = useMemo<Job[]>(() => {
    const relabel = (j: Job): Job => ({
      ...j,
      displayName: componentLabel(j.displayName ?? j.jobName),
      jobName: componentLabel(j.jobName),
    })
    return selectGraphJobs(barrieredJobs, visibleKinds, emptyContainerIds).map(
      relabel,
    )
  }, [barrieredJobs, visibleKinds, emptyContainerIds])

  // Fold state — re-seed on topology (group-set) change, keep manual folds
  // across status ticks (React-sanctioned adjust-state-during-render).
  const groupSignature = useMemo(
    () =>
      jobs
        .filter((j) => j.type === 'group')
        .map((j) => j.id)
        .sort()
        .join('|'),
    [jobs],
  )
  const [folded, setFolded] = useState<Set<string>>(() =>
    defaultFoldedAtContainmentDepth(adapter.jobs, DEFAULT_FOLD_DEPTH),
  )
  const [prevSig, setPrevSig] = useState(groupSignature)
  if (prevSig !== groupSignature) {
    setPrevSig(groupSignature)
    setFolded(defaultFoldedAtContainmentDepth(adapter.jobs, DEFAULT_FOLD_DEPTH))
  }

  const layout = useMemo(
    () =>
      flowLayoutOrganic(jobs, {
        hints: adapter.hints,
        regions: adapter.regions,
        families: adapter.families,
        folded,
      }),
    [jobs, adapter.hints, adapter.regions, adapter.families, folded],
  )

  const badgeCounts = useMemo(
    () => descendantCountByGroup(jobs, folded),
    [jobs, folded],
  )

  const hasGroups = useMemo(
    () => jobs.some((j) => j.type === 'group'),
    [jobs],
  )

  const toggleFold = useCallback((jobId: string) => {
    setFolded((prev) => {
      const next = new Set(prev)
      if (next.has(jobId)) next.delete(jobId)
      else next.add(jobId)
      return next
    })
  }, [])

  const handleNodeAction = useCallback((jobId: string, actionId: string) => {
    setFolded((prev) => {
      const next = new Set(prev)
      if (actionId === 'fold') next.add(jobId)
      else if (actionId === 'unfold') next.delete(jobId)
      return next
    })
  }, [])

  // Node click → the job's detail page (same chroot/mothership link logic
  // as JobsTable): strip the "<deploymentId>:" prefix, encode the segment.
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

  const empty = layout.nodes.length === 0

  return (
    <div data-testid="jobs-graph-view" className="jobs-graph-view">
      <style>{JOBS_GRAPH_VIEW_CSS}</style>
      {empty ? (
        <div className="jobs-graph-empty" data-testid="jobs-graph-empty">
          {stream.streamStatus === 'connecting'
            ? 'Loading the job graph…'
            : visibleKinds && visibleKinds.size === 0
              ? 'No kinds selected — add a chip to show its nodes.'
              : 'No jobs to graph yet.'}
        </div>
      ) : (
        <FlowCanvasOrganic
          layout={layout}
          openJobId={null}
          hostJobId={null}
          embedded
          onJobClick={(jobId) => onJobClick(jobId)}
          onJobDoubleClick={(jobId) => toggleFold(jobId)}
          onCanvasBackgroundClick={() => {}}
          onFoldToggle={hasGroups ? toggleFold : undefined}
          badgeCounts={badgeCounts}
          nodeActions={hasGroups ? GROUP_NODE_ACTIONS : undefined}
          onNodeAction={hasGroups ? handleNodeAction : undefined}
        />
      )}
    </div>
  )
}

const JOBS_GRAPH_VIEW_CSS = `
.jobs-graph-view {
  position: relative;
  width: 100%;
  height: min(74vh, 820px);
  min-height: 440px;
  border: 1px solid var(--color-border);
  border-radius: 10px;
  background: var(--color-bg-2);
  overflow: hidden;
}
.jobs-graph-empty {
  display: flex;
  align-items: center;
  justify-content: center;
  height: 100%;
  color: var(--color-text-dim);
  font-size: 0.9rem;
}
`
