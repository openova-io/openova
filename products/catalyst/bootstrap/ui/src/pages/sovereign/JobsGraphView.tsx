/**
 * JobsGraphView — the Graph half of the /jobs List⇄Graph toggle (P1b +
 * P2, Refs #6703). Groups the flat Job tree into orchestration-unit
 * SECTIONS ("default gathered relevant groups" — founder verbatim) and
 * renders one pure-SVG layered DAG (`JobDependenciesGraph`) per section.
 * Node clicks route to the per-job detail page using the SAME
 * chroot/mothership path logic the JobsTable rows use.
 *
 * # Why grouped (P2)
 *
 * P1b mapped ALL jobs flat into ONE graph. The backend emits synthesised
 * `type:'group'` parent rows (Bootstrap / Provision / Cutover / Handover /
 * Apps / Reconcilers); flattened, those became edgeless floating nodes and
 * the cross-group `dependsOn` chains crossed the whole canvas. P2 instead:
 *
 *   • One SECTION per group row (`job.type === 'group'`) that has ≥1 child
 *     — title = the group's `displayName ?? jobName`, members = every job
 *     whose `parentId === group.id`. Sections are DERIVED from the group
 *     rows actually present (their displayName is read, never hardcoded).
 *   • A "Standalone tasks" section for leaf jobs with no parent (or a
 *     parentId that matches no present group row).
 *   • Each section lays out INDEPENDENTLY over its own members, so a
 *     section's `dependsOn` edges connect only that section's steps (they
 *     already form a linear chain per group); cross-group deps naturally
 *     drop out (depsLayout ignores unknown ids).
 *
 * # Two per-node mappings the widget requires
 *   • Title — `displayName ?? jobName` (identical to the table-row link).
 *   • Status — JobStatus has SEVEN values but JobDependenciesGraph only
 *     knows FOUR. `toGraphStatus` folds the health axis onto the one-shot
 *     axis: healthy → succeeded, degraded / failing → failed (§3646 §4c).
 *
 * # Chip highlight (P2)
 *
 * The graph-view chip strip lives in the JobsPage toolbar and HIGHLIGHTS
 * (never filters) — removing nodes would sever mid-chain `dependsOn`
 * edges. `highlightKind` (owned by JobsPage as local state, so it never
 * fights the list view's `?kind=` filter) selects a JobKind; every node of
 * a DIFFERENT kind is added to `dimNodeIds` and rendered at reduced opacity
 * by the widget. `null` (the default on entering graph view) dims nothing.
 */

import { useCallback, useMemo } from 'react'
import { useNavigate, useParams } from '@tanstack/react-router'
import { DETECTED_MODE } from '@/shared/lib/detectMode'
import type { Job, JobStatus } from '@/lib/jobs.types'
import { jobKind } from '@/lib/jobs.types'
import type { JobChipKind } from './jobs-list/jobKinds'
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
  /**
   * Graph-view chip highlight (P2). When set, nodes NOT of this kind are
   * dimmed (opacity-only); the node set is never reduced. `null`/undefined
   * = no highlight. Owned by JobsPage so the list filter and the graph
   * highlight stay independent surfaces.
   */
  highlightKind?: JobChipKind | null
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

/**
 * Preferred top-to-bottom section order. Keyed by group `jobName` slug.
 * This is an ORDERING preference only — NOT a label source (titles come
 * from each group's displayName). Covers the canonical backend group slugs
 * AND the reducer first-paint slugs (jobsAdapter) so both order sensibly;
 * unknown groups sort after all known ones (stable by input order), and the
 * standalone section is always last.
 */
const SECTION_ORDER: Record<string, number> = {
  // Canonical backend groups (founder order: provision → bootstrap → apps
  // → reconcilers → handover → cutover).
  provisioner: 10,
  'bootstrap-kit': 20,
  apps: 30,
  reconcilers: 40,
  handover: 50,
  cutover: 60,
  // Reducer first-paint groups (jobsAdapter GROUPS) — same conceptual order.
  'phase-0-infra': 10,
  'phase-1-bootstrap': 20,
  applications: 30,
}
const UNKNOWN_GROUP_RANK = 900
const STANDALONE_RANK = 1000

/** Stable slug for a section testid — slugify the group jobName. */
function sectionSlug(raw: string): string {
  const s = raw
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
  return s.length > 0 ? s : 'group'
}

interface GraphSection {
  /** Stable slug for the section testid. */
  slug: string
  /** Header title — the group displayName, or "Standalone tasks". */
  title: string
  /** The section's member jobs (leaves only — the group row is the header). */
  members: Job[]
  /** Sort rank (lower renders first). */
  rank: number
}

const STANDALONE_SLUG = 'standalone'
const STANDALONE_TITLE = 'Standalone tasks'

/**
 * Partition the flat job list into ordered sections. One section per group
 * row with ≥1 member; a trailing "Standalone tasks" section for parentless
 * leaves (or leaves whose parentId matches no present group row).
 */
function partitionSections(jobs: readonly Job[]): GraphSection[] {
  const groupRows = jobs.filter((j) => j.type === 'group')
  const groupIds = new Set(groupRows.map((g) => g.id))

  // Bucket every non-group leaf under its parent group id (or standalone).
  const membersByGroup = new Map<string, Job[]>()
  const standalone: Job[] = []
  for (const j of jobs) {
    if (j.type === 'group') continue
    if (j.parentId && groupIds.has(j.parentId)) {
      const arr = membersByGroup.get(j.parentId) ?? []
      arr.push(j)
      membersByGroup.set(j.parentId, arr)
    } else {
      standalone.push(j)
    }
  }

  const sections: GraphSection[] = []
  groupRows.forEach((g, idx) => {
    const members = membersByGroup.get(g.id) ?? []
    if (members.length === 0) return // skip empty groups
    const knownRank = SECTION_ORDER[g.jobName]
    // Unknown groups keep input order via a fractional tiebreak on idx.
    const rank = knownRank ?? UNKNOWN_GROUP_RANK + idx
    sections.push({
      slug: sectionSlug(g.jobName),
      title: g.displayName ?? g.jobName,
      members,
      rank,
    })
  })

  if (standalone.length > 0) {
    sections.push({
      slug: STANDALONE_SLUG,
      title: STANDALONE_TITLE,
      members: standalone,
      rank: STANDALONE_RANK,
    })
  }

  // Stable sort by rank, then by input-derived order already encoded above.
  return sections.sort((a, b) => a.rank - b.rank)
}

export function JobsGraphView({ jobs, highlightKind }: JobsGraphViewProps) {
  const navigate = useNavigate()
  const params = useParams({ strict: false }) as { deploymentId?: string }
  const isSovereign = DETECTED_MODE.mode === 'sovereign'
  const depId = params.deploymentId ?? ''

  const sections = useMemo(() => partitionSections(jobs), [jobs])

  // Dim set (P2 highlight): ids of every job NOT of the highlighted kind.
  // Undefined when no highlight is active, so the widget renders unchanged.
  const dimNodeIds = useMemo<ReadonlySet<string> | undefined>(() => {
    if (!highlightKind) return undefined
    const dim = new Set<string>()
    for (const j of jobs) {
      if (j.type === 'group') continue
      if (jobKind(j) !== highlightKind) dim.add(j.id)
    }
    return dim
  }, [jobs, highlightKind])

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
      <style>{JOBS_GRAPH_VIEW_CSS}</style>
      {sections.length === 0 ? (
        <div
          data-testid="jobs-graph-empty"
          className="jobs-graph-empty"
          role="status"
        >
          No jobs to graph yet.
        </div>
      ) : (
        sections.map((section) => {
          const nodes: JobNode[] = section.members.map((j) => ({
            id: j.id,
            title: j.displayName ?? j.jobName,
            status: toGraphStatus(j.status),
            dependsOn: j.dependsOn,
          }))
          return (
            <section
              key={section.slug}
              data-testid={`jobs-graph-section-${section.slug}`}
              className="jobs-graph-section"
            >
              <header className="jobs-graph-section-head">
                <span className="jobs-graph-section-title">{section.title}</span>
                <span
                  className="jobs-graph-section-count"
                  data-testid={`jobs-graph-section-${section.slug}-count`}
                >
                  {section.members.length}
                </span>
              </header>
              <JobDependenciesGraph
                jobs={nodes}
                onNodeClick={onNodeClick}
                dimNodeIds={dimNodeIds}
              />
            </section>
          )
        })
      )}
    </div>
  )
}

const JOBS_GRAPH_VIEW_CSS = `
.jobs-graph-section { margin-bottom: 1.25rem; }
.jobs-graph-section:last-child { margin-bottom: 0; }
.jobs-graph-section-head {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  margin-bottom: 0.4rem;
}
.jobs-graph-section-title {
  font-size: 0.82rem;
  font-weight: 600;
  color: var(--color-text-strong);
}
.jobs-graph-section-count {
  font-size: 0.68rem;
  font-weight: 600;
  padding: 0.04rem 0.4rem;
  border-radius: 999px;
  background: color-mix(in srgb, var(--color-border) 60%, transparent);
  color: var(--color-text-dim);
  font-variant-numeric: tabular-nums;
}
.jobs-graph-empty {
  padding: 1.5rem;
  border: 1px dashed var(--color-border);
  border-radius: 12px;
  color: var(--color-text-dim);
  font-size: 0.85rem;
  text-align: center;
}
`
