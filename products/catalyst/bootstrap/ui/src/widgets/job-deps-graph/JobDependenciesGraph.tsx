/**
 * JobDependenciesGraph — pure-SVG topological-layered DAG renderer for
 * the Job-detail Dependencies tab (epic openova-io/openova#204 item 11,
 * sub-ticket #206).
 *
 * Each node is a job (color-coded by status). Edges are `dependsOn`
 * relations rendered as orthogonal "step" polylines. Clicking a node
 * fires the `onNodeClick` callback, which the parent uses to navigate
 * to that job's detail page (TanStack <Link> happens at the call site
 * so this widget stays router-agnostic and trivially testable).
 *
 * RATIONALE (per docs/INVIOLABLE-PRINCIPLES.md):
 *   • #2 (never compromise quality) — no graph lib. The chart is small
 *     (~30 jobs at the upper bound), so a deterministic layered layout
 *     beats `reactflow` in bundle size, test ergonomics, and SSR.
 *   • #4 (never hardcode) — every dimension is derived from `depsLayout`
 *     options or the layout result. The 380px viewport height is a
 *     prop default, not a magic number.
 *
 * Visual contract (locked by JobDependenciesGraph.test.tsx + Playwright):
 *   • SVG has data-testid="jobs-deps-graph".
 *   • Each node group has data-testid="jobs-deps-node-<id>".
 *   • Each edge polyline has data-testid="jobs-deps-edge-<from>-<to>".
 *   • Status colour comes from the `--color-success` / `--color-accent`
 *     / `--color-danger` / `--color-warn` CSS variables — the same
 *     palette as JobCard's status iconography.
 */

import { useId, useMemo } from 'react'
import {
  depsLayout,
  type LayoutInput,
  type LayoutOptions,
} from '@/shared/lib/depsLayout'

/** Same JobUiStatus vocabulary as src/pages/sovereign/jobs.ts. */
export type JobUiStatus = 'pending' | 'running' | 'succeeded' | 'failed'

/**
 * Minimum Job shape this widget needs. The widget intentionally accepts
 * a structural type (NOT the canonical `Job` from jobs.ts) so it works
 * against the evolved-but-not-yet-merged Job model the sibling agents
 * are introducing AND against today's jobs.ts shape.
 */
export interface JobNode {
  id: string
  title: string
  status: JobUiStatus
  dependsOn: readonly string[]
}

export interface JobDependenciesGraphProps {
  /** Jobs to render. Edges with a dep id not in this list are dropped. */
  jobs: readonly JobNode[]
  /** Default 380. Min 350 / max 450 per spec; clamped at render time. */
  height?: number
  /** Optional layout overrides — e.g. wider columns on big graphs. */
  layoutOpts?: LayoutOptions
  /** Click handler — receives the clicked node's id. */
  onNodeClick?: (jobId: string) => void
  /** Optional className passed to the wrapping <div>. */
  className?: string
  /**
   * OPTIONAL highlight/dim overlay (P2, Refs #6703). When provided, every
   * node whose id is in this set (and every edge touching such a node) is
   * rendered at a reduced opacity so the NON-dimmed nodes read as the
   * highlighted set. This is opacity-only — the layout is unchanged, so the
   * existing render + Playwright visual contract is untouched when the prop
   * is absent (the default): no `opacity`/`data-dimmed` attribute is emitted
   * at all unless a dim set is passed. Used by JobsGraphView to implement
   * the graph-view chip HIGHLIGHT (which dims, never removes, so mid-chain
   * `dependsOn` edges survive).
   */
  dimNodeIds?: ReadonlySet<string>
}

/** Opacity applied to a dimmed node/edge when `dimNodeIds` is active. */
const DIM_OPACITY = 0.25

const STATUS_FILL: Record<JobUiStatus, string> = {
  succeeded: 'var(--color-success)',
  running: 'var(--color-accent)',
  failed: 'var(--color-danger)',
  pending: 'var(--color-text-dim)',
}

const STATUS_RING: Record<JobUiStatus, string> = {
  succeeded: 'var(--color-success)',
  running: 'var(--color-accent)',
  failed: 'var(--color-danger)',
  pending: 'var(--color-border-strong)',
}

export function JobDependenciesGraph({
  jobs,
  height = 380,
  layoutOpts,
  onNodeClick,
  className,
  dimNodeIds,
}: JobDependenciesGraphProps) {
  const clamped = Math.max(350, Math.min(450, height))

  // Per-instance marker id — the /jobs graph view (P2) renders one
  // JobDependenciesGraph per orchestration-unit section, so a hardcoded
  // marker id would repeat across the page (invalid duplicate DOM ids;
  // url(#id) resolves only to the first). useId() gives each instance a
  // stable unique id; strip ':' so the SVG url(#...) reference is clean.
  const arrowMarkerId = `jobs-deps-arrow-${useId().replace(/:/g, '')}`

  const layout = useMemo(() => {
    const inputs: LayoutInput[] = jobs.map((j) => ({
      id: j.id,
      dependsOn: j.dependsOn,
    }))
    return depsLayout(inputs, layoutOpts)
  }, [jobs, layoutOpts])

  const byId = useMemo(() => {
    const m = new Map<string, JobNode>()
    for (const j of jobs) m.set(j.id, j)
    return m
  }, [jobs])

  // Node geometry — sourced from depsLayout's internal defaults via the
  // bounding box; we recompute width/height per node visually.
  const nodeWidth = layoutOpts?.nodeWidth ?? 180
  const nodeHeight = layoutOpts?.nodeHeight ?? 56

  if (jobs.length === 0) {
    return (
      <div
        className={className}
        data-testid="jobs-deps-graph-empty"
        style={{ height: clamped }}
      >
        <p className="text-xs text-[var(--color-text-dim)]">
          No dependencies for this job.
        </p>
      </div>
    )
  }

  return (
    <div
      className={
        'relative w-full overflow-auto rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)]' +
        (className ? ' ' + className : '')
      }
      data-testid="jobs-deps-graph-wrapper"
      style={{ height: clamped }}
    >
      <svg
        data-testid="jobs-deps-graph"
        width={layout.width}
        height={layout.height}
        viewBox={`0 0 ${layout.width} ${layout.height}`}
        role="img"
        aria-label="Job dependencies graph"
        style={{ display: 'block', minWidth: '100%' }}
      >
        {/* Edges first so they sit beneath the nodes. */}
        <g data-testid="jobs-deps-edges">
          {layout.edges.map((e) => {
            // An edge dims when either endpoint is dimmed — a highlighted
            // node keeps a bright edge only to another highlighted node.
            const edgeDimmed = dimNodeIds
              ? dimNodeIds.has(e.from) || dimNodeIds.has(e.to)
              : false
            return (
              <polyline
                key={`${e.from}->${e.to}`}
                data-testid={`jobs-deps-edge-${e.from}-${e.to}`}
                data-dimmed={dimNodeIds ? (edgeDimmed ? 'true' : 'false') : undefined}
                points={e.points.map((p) => `${p.x},${p.y}`).join(' ')}
                fill="none"
                stroke="var(--color-border-strong)"
                strokeWidth={1.5}
                opacity={edgeDimmed ? DIM_OPACITY : undefined}
                markerEnd={`url(#${arrowMarkerId})`}
              />
            )
          })}
        </g>

        {/* Arrow marker definition. */}
        <defs>
          <marker
            id={arrowMarkerId}
            viewBox="0 0 10 10"
            refX="9"
            refY="5"
            markerWidth="6"
            markerHeight="6"
            orient="auto-start-reverse"
          >
            <path d="M0,0 L10,5 L0,10 Z" fill="var(--color-border-strong)" />
          </marker>
        </defs>

        {/* Nodes. */}
        <g data-testid="jobs-deps-nodes">
          {layout.nodes.map((n) => {
            const job = byId.get(n.id)!
            const fill = STATUS_FILL[job.status]
            const ring = STATUS_RING[job.status]
            const nodeDimmed = dimNodeIds?.has(n.id) ?? false
            return (
              <g
                key={n.id}
                data-testid={`jobs-deps-node-${n.id}`}
                data-status={job.status}
                data-dimmed={dimNodeIds ? (nodeDimmed ? 'true' : 'false') : undefined}
                opacity={nodeDimmed ? DIM_OPACITY : undefined}
                transform={`translate(${n.x}, ${n.y})`}
                style={{ cursor: onNodeClick ? 'pointer' : 'default' }}
                onClick={() => onNodeClick?.(n.id)}
                tabIndex={onNodeClick ? 0 : -1}
                role={onNodeClick ? 'button' : undefined}
                aria-label={`${job.title} — ${job.status}`}
                onKeyDown={(ev) => {
                  if (!onNodeClick) return
                  if (ev.key === 'Enter' || ev.key === ' ') {
                    ev.preventDefault()
                    onNodeClick(n.id)
                  }
                }}
              >
                <rect
                  width={nodeWidth}
                  height={nodeHeight}
                  rx={10}
                  ry={10}
                  fill="var(--color-bg)"
                  stroke={ring}
                  strokeWidth={1.5}
                />
                {/* Status pip. */}
                <circle cx={14} cy={nodeHeight / 2} r={6} fill={fill} />
                {/* Title — single line, ellipsis via SVG textLength. */}
                <text
                  x={28}
                  y={nodeHeight / 2 - 4}
                  fill="var(--color-text-strong)"
                  fontSize={12}
                  fontWeight={600}
                  dominantBaseline="middle"
                >
                  {truncate(job.title, 22)}
                </text>
                <text
                  x={28}
                  y={nodeHeight / 2 + 12}
                  fill="var(--color-text-dim)"
                  fontSize={10}
                  fontWeight={400}
                  dominantBaseline="middle"
                >
                  {job.status}
                </text>
              </g>
            )
          })}
        </g>
      </svg>
    </div>
  )
}

function truncate(s: string, max: number): string {
  if (s.length <= max) return s
  return s.slice(0, Math.max(0, max - 1)) + '…'
}
