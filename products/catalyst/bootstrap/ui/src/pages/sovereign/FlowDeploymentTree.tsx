/**
 * FlowDeploymentTree — left-side static breakdown of jobs by region
 * and family for the Flow canvas.
 *
 * Mirrors the left "DEPLOYMENT PROGRESS" panel in
 * `marketing/mockups/provision-mockup-v4.png`. Per the operator's
 * directive ("NO accordions anywhere"), every region + group is
 * rendered as a STATIC tree (no expand/collapse interactions); the
 * canvas itself is the interactive surface.
 *
 * Composition: receives a list of FlowGroupRow records computed by
 * FlowPage from the flat job list. No data-fetching here.
 */

import type { JobStatus } from '@/lib/jobs.types'

export interface FlowGroupRow {
  /** Unique row id — `${regionId}:${familyId}`. */
  id: string
  /** Region this row belongs to. */
  regionId: string
  /** Region display label (the first row in a region carries the title). */
  regionLabel: string
  /** Region meta line (e.g. "Hetzner · Primary"). */
  regionMeta: string
  /** True for the first row in each region — drives the region header. */
  isRegionHeader: boolean
  /** Family id (`spine`, `pilot`, …). */
  familyId: string
  /** Family display label ("SPINE"). */
  familyLabel: string
  /** Family colour hex. */
  familyColor: string
  /** Family description (e.g. "Networking & Mesh"). */
  familyDesc: string
  /** Job summary lines inside this group. */
  rows: Array<{
    jobId: string
    label: string
    status: JobStatus
    durationLabel: string
  }>
}

export interface FlowDeploymentTreeProps {
  groups: readonly FlowGroupRow[]
  selectedJobId: string | null
  onSelectJob: (jobId: string) => void
  /** Total counts shown in the top "DEPLOYMENT PROGRESS" header. */
  totals: { finished: number; total: number }
}

const STATUS_DOT_BG: Record<JobStatus, string> = {
  succeeded: '#4ADE80',
  running: '#38BDF8',
  failed: '#F87171',
  pending: 'rgba(148,163,184,0.45)',
}

export function FlowDeploymentTree({ groups, selectedJobId, onSelectJob, totals }: FlowDeploymentTreeProps) {
  const pct = totals.total === 0 ? 0 : Math.round((totals.finished / totals.total) * 100)
  return (
    <aside
      className="flow-deployment-tree"
      data-testid="flow-deployment-tree"
      aria-label="Deployment progress tree"
    >
      <div className="flow-tree-header">
        <span className="flow-tree-header-label">Deployment Progress</span>
        <span className="flow-tree-header-count" data-testid="flow-tree-progress-count">
          {totals.finished}/{totals.total}
        </span>
      </div>
      <div className="flow-tree-progress-bar">
        <div
          className="flow-tree-progress-fill"
          style={{ width: `${pct}%` }}
          data-testid="flow-tree-progress-fill"
        />
      </div>
      <div className="flow-tree-body">
        {groups.length === 0 ? (
          <div className="flow-tree-empty">
            No jobs in this deployment.
          </div>
        ) : (
          renderRegionWrappedGroups(groups, selectedJobId, onSelectJob)
        )}
      </div>
    </aside>
  )
}

/**
 * renderRegionWrappedGroups — wraps each region's rows in a single
 * <div data-testid="flow-tree-region-<id>"> so multi-region layouts
 * are visually + semantically grouped (forcing-function: tested by
 * FlowDeploymentTree.test.tsx — at least 2 wrappers when given 2
 * regions in the fixture).
 *
 * Inline component because exporting more than React components from
 * this file would trip the react-refresh fast-refresh warning, and
 * the data-shape is FlowGroupRow which lives here.
 */
function renderRegionWrappedGroups(
  groups: readonly FlowGroupRow[],
  selectedJobId: string | null,
  onSelectJob: (jobId: string) => void,
) {
  // Bucket consecutive groups by regionId. Order is preserved (it's
  // the order buildFlowGroupRows emits — top→bottom in caller's region
  // order).
  const buckets: Array<{ regionId: string; rows: FlowGroupRow[] }> = []
  for (const g of groups) {
    const last = buckets[buckets.length - 1]
    if (last && last.regionId === g.regionId) {
      last.rows.push(g)
    } else {
      buckets.push({ regionId: g.regionId, rows: [g] })
    }
  }
  return buckets.map(({ regionId, rows }) => (
    <div
      key={regionId}
      className="flow-tree-region"
      data-testid={`flow-tree-region-${regionId}`}
      data-region-id={regionId}
    >
      {rows.map((g) => (
        <div
          key={g.id}
          className="flow-tree-group"
          data-testid={`flow-tree-group-${g.regionId}-${g.familyId}`}
        >
          {g.isRegionHeader ? (
            <div className="flow-tree-region-header">
              <span className="flow-tree-region-dot" />
              <div className="flow-tree-region-meta">
                <span
                  className="flow-tree-region-name"
                  data-testid={`flow-tree-region-name-${g.regionId}`}
                >
                  {g.regionLabel}
                </span>
                <span className="flow-tree-region-sub">{g.regionMeta}</span>
              </div>
            </div>
          ) : null}
          <div className="flow-tree-family-row">
            <span
              className="flow-tree-family-dot"
              style={{ background: g.familyColor }}
            />
            <span
              className="flow-tree-family-name"
              data-testid={`flow-tree-family-name-${g.familyId}`}
              style={{ color: g.familyColor }}
            >
              {g.familyLabel}
            </span>
            <span className="flow-tree-family-desc">{g.familyDesc}</span>
            <span className="flow-tree-family-count">{g.rows.length}</span>
          </div>
          {g.rows.map((row) => (
            <button
              key={row.jobId}
              type="button"
              onClick={() => onSelectJob(row.jobId)}
              className={`flow-tree-job-row${selectedJobId === row.jobId ? ' is-selected' : ''}`}
              data-testid={`flow-tree-job-${row.jobId}`}
              data-selected={selectedJobId === row.jobId ? 'true' : 'false'}
            >
              <span
                className="flow-tree-job-dot"
                style={{ background: STATUS_DOT_BG[row.status] }}
              />
              <span className="flow-tree-job-name">{row.label}</span>
              {row.durationLabel ? (
                <span className="flow-tree-job-duration">{row.durationLabel}</span>
              ) : null}
            </button>
          ))}
        </div>
      ))}
    </div>
  ))
}

/* See `flowDeploymentTreeData.ts` for the FlowGroupRow builder helper.
 * Kept in a separate module so this file only exports React components,
 * avoiding the react-refresh fast-refresh warning. */
