/**
 * JobsFlowView — Flow tab on the Jobs page (founder-locked spec).
 *
 * Renders a two-level Sugiyama layered DAG:
 *   • Outer: batches as meta-stages, left → right.
 *   • Inner: jobs within each batch, left → right.
 *
 * Layout is delegated to `lib/pipelineLayout.ts` (pure function); this
 * component only owns the SVG rendering + click/collapse interactions.
 *
 * Visual contract (per founder spec):
 *   • Batch swimlane: card with name header, mini progress bar, count,
 *     collapse toggle, status-tinted background.
 *   • Job card: name, status badge (pulsing dot if running), duration,
 *     appId chip. Click → /provision/$id/jobs/$jobId.
 *   • Within-batch edge: thin gray straight (span 1) or smooth bezier
 *     (span ≥ 2).
 *   • Cross-batch edge: bold colored arrow at swimlane boundary;
 *     dashed red when source batch has failures.
 *   • Collapsed batch: shrinks to a single supernode with progress bar.
 *   • Default zoom: in-flight batches expanded; all-succeeded collapsed.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #1 (waterfall) — full target shape ships in this component.
 *   #2 (no compromise) — no graph library, pure SVG + computed bezier.
 *   #4 (never hardcode) — every dimension lives in `pipelineLayout.ts`
 *      DEFAULT_GEOMETRY; this component only references the result.
 */

import { useMemo, useState, useCallback } from 'react'
import { useNavigate } from '@tanstack/react-router'
import type { Job } from '@/lib/jobs.types'
import {
  pipelineLayout,
  defaultCollapsedBatchIds,
  edgeToPath,
  type FlowBatchLane,
  type FlowEdge,
  type FlowNode,
} from '@/lib/pipelineLayout'

/* ──────────────────────────────────────────────────────────────────
 * Component
 * ────────────────────────────────────────────────────────────────── */

interface JobsFlowViewProps {
  /** Job list. Backend populates; UI sorts/filters in place. */
  jobs: readonly Job[]
  /** Stable deployment id — embedded in per-job link target. */
  deploymentId: string
}

export function JobsFlowView({ jobs, deploymentId }: JobsFlowViewProps) {
  // Default collapse policy is computed once per `jobs` reference; the
  // user override is a Set keyed by batchId. Each toggle replaces the
  // override with a fresh Set so React re-renders.
  const initialCollapsed = useMemo(() => defaultCollapsedBatchIds(jobs), [jobs])
  const [overrideSet, setOverrideSet] = useState<Set<string>>(new Set())
  const [overrideMode, setOverrideMode] = useState<'replace' | 'merge'>('merge')

  const collapsedBatchIds = useMemo<Set<string>>(() => {
    if (overrideMode === 'replace') return overrideSet
    // merge mode: start from defaults, then toggle entries the user
    // explicitly flipped.
    const out = new Set(initialCollapsed)
    for (const id of overrideSet) {
      if (out.has(id)) out.delete(id)
      else out.add(id)
    }
    return out
  }, [initialCollapsed, overrideSet, overrideMode])

  const layout = useMemo(
    () => pipelineLayout(jobs, { collapsedBatchIds }),
    [jobs, collapsedBatchIds],
  )

  const navigate = useNavigate()

  const onToggleBatch = useCallback((batchId: string) => {
    setOverrideMode('merge')
    setOverrideSet((prev) => {
      const next = new Set(prev)
      if (next.has(batchId)) next.delete(batchId)
      else next.add(batchId)
      return next
    })
  }, [])

  const onJobClick = useCallback(
    (jobId: string) => {
      navigate({
        to: '/provision/$deploymentId/jobs/$jobId' as never,
        params: { deploymentId, jobId } as never,
      })
    },
    [navigate, deploymentId],
  )

  if (jobs.length === 0) {
    return (
      <div
        data-testid="jobs-flow-empty"
        className="rounded-xl border border-dashed border-[var(--color-border)] p-8 text-center text-sm text-[var(--color-text-dim)]"
      >
        No jobs to render in the dependency graph.
      </div>
    )
  }

  return (
    <div className="jobs-flow-wrap" data-testid="jobs-flow-wrap">
      <style>{JOBS_FLOW_CSS}</style>
      <div className="jobs-flow-scroll">
        <svg
          width={layout.width}
          height={layout.height}
          viewBox={`0 0 ${layout.width} ${layout.height}`}
          className="jobs-flow-svg"
          data-testid="jobs-flow-svg"
          role="img"
          aria-label="Job dependency flow"
        >
          <defs>
            <marker
              id="flow-arrow"
              viewBox="0 0 10 10"
              refX="9"
              refY="5"
              markerWidth="6"
              markerHeight="6"
              orient="auto-start-reverse"
            >
              <path d="M0,0 L10,5 L0,10 Z" fill="var(--color-text-dim)" />
            </marker>
            <marker
              id="flow-arrow-meta"
              viewBox="0 0 10 10"
              refX="9"
              refY="5"
              markerWidth="7"
              markerHeight="7"
              orient="auto-start-reverse"
            >
              <path d="M0,0 L10,5 L0,10 Z" fill="#38BDF8" />
            </marker>
            <marker
              id="flow-arrow-blocked"
              viewBox="0 0 10 10"
              refX="9"
              refY="5"
              markerWidth="7"
              markerHeight="7"
              orient="auto-start-reverse"
            >
              <path d="M0,0 L10,5 L0,10 Z" fill="#F87171" />
            </marker>
          </defs>

          {/* 1. Batch swimlanes (drawn first so jobs sit on top) */}
          {layout.batches.map((b) => (
            <BatchSwimlane
              key={b.batchId}
              lane={b}
              onToggle={() => onToggleBatch(b.batchId)}
            />
          ))}

          {/* 2. Edges */}
          {layout.edges.map((e, i) => (
            <FlowEdgePath key={`${e.fromId}-${e.toId}-${i}`} edge={e} />
          ))}

          {/* 3. Job nodes (cards) */}
          {layout.nodes.map((n) =>
            n.kind === 'job' ? (
              <JobCardNode key={n.id} node={n} onClick={() => onJobClick(n.id)} />
            ) : null,
          )}
        </svg>
      </div>
    </div>
  )
}

/* ──────────────────────────────────────────────────────────────────
 * Sub-components
 * ────────────────────────────────────────────────────────────────── */

interface BatchSwimlaneProps {
  lane: FlowBatchLane
  onToggle: () => void
}

function BatchSwimlane({ lane, onToggle }: BatchSwimlaneProps) {
  const tone = LANE_TONE[lane.status]
  const HEADER_HEIGHT = 36
  const progressPct =
    lane.total === 0 ? 0 : Math.round((lane.finished / lane.total) * 100)

  return (
    <g data-testid={`flow-batch-${lane.batchId}`} data-collapsed={lane.collapsed}>
      <rect
        x={lane.x}
        y={lane.y}
        width={lane.width}
        height={lane.height}
        rx={12}
        ry={12}
        fill={tone.bg}
        stroke={tone.border}
        strokeWidth={1.5}
      />
      {/* Header strip */}
      <rect
        x={lane.x}
        y={lane.y}
        width={lane.width}
        height={HEADER_HEIGHT}
        rx={12}
        ry={12}
        fill={tone.headerBg}
        stroke="none"
      />
      <foreignObject
        x={lane.x + 8}
        y={lane.y + 4}
        width={lane.width - 16}
        height={HEADER_HEIGHT - 8}
      >
        <div className="flow-batch-header">
          <button
            type="button"
            className="flow-batch-toggle"
            data-testid={`flow-batch-toggle-${lane.batchId}`}
            onClick={onToggle}
            aria-label={lane.collapsed ? 'Expand batch' : 'Collapse batch'}
          >
            <svg viewBox="0 0 12 12" width="10" height="10" aria-hidden>
              {lane.collapsed ? (
                <path d="M3 2 L9 6 L3 10 Z" fill="currentColor" />
              ) : (
                <path d="M2 4 L10 4 L6 10 Z" fill="currentColor" />
              )}
            </svg>
          </button>
          <span
            className="flow-batch-name"
            data-testid={`flow-batch-name-${lane.batchId}`}
            title={lane.batchId}
          >
            {lane.batchId}
          </span>
          <span className="flow-batch-count" data-testid={`flow-batch-count-${lane.batchId}`}>
            {lane.finished}/{lane.total}
          </span>
        </div>
      </foreignObject>

      {/* Mini progress bar */}
      <rect
        x={lane.x + 8}
        y={lane.y + HEADER_HEIGHT - 4}
        width={lane.width - 16}
        height={3}
        rx={1.5}
        ry={1.5}
        fill="rgba(148,163,184,0.20)"
      />
      <rect
        x={lane.x + 8}
        y={lane.y + HEADER_HEIGHT - 4}
        width={Math.max(0, ((lane.width - 16) * progressPct) / 100)}
        height={3}
        rx={1.5}
        ry={1.5}
        fill={tone.progress}
        data-testid={`flow-batch-progress-${lane.batchId}`}
      />

      {/* Collapsed body — render the supernode glyph in the middle */}
      {lane.collapsed ? (
        <foreignObject
          x={lane.x + 8}
          y={lane.y + HEADER_HEIGHT + 4}
          width={lane.width - 16}
          height={lane.height - HEADER_HEIGHT - 8}
        >
          <div
            className="flow-batch-collapsed"
            data-testid={`flow-batch-supernode-${lane.batchId}`}
          >
            <span className="flow-batch-collapsed-pct">{progressPct}%</span>
            <span className="flow-batch-collapsed-meta">
              {lane.total} {lane.total === 1 ? 'job' : 'jobs'}
            </span>
          </div>
        </foreignObject>
      ) : null}
    </g>
  )
}

interface JobCardNodeProps {
  node: FlowNode
  onClick: () => void
}

function JobCardNode({ node, onClick }: JobCardNodeProps) {
  if (!node.job) return null
  const j = node.job
  const tone = JOB_STATUS_TONE[j.status]
  return (
    <g
      data-testid={`flow-job-${j.id}`}
      data-status={j.status}
      onClick={onClick}
      style={{ cursor: 'pointer' }}
    >
      <rect
        x={node.x}
        y={node.y}
        width={node.width}
        height={node.height}
        rx={10}
        ry={10}
        fill={tone.bg}
        stroke={tone.border}
        strokeWidth={1.25}
        className="flow-job-rect"
      />
      <foreignObject x={node.x} y={node.y} width={node.width} height={node.height}>
        <div className="flow-job-card">
          <div className="flow-job-row">
            <span className="flow-job-name" title={j.jobName}>
              {j.jobName}
            </span>
            <span
              className={`flow-job-badge flow-job-badge-${j.status}`}
              data-testid={`flow-job-badge-${j.id}`}
            >
              {j.status === 'running' ? <span className="flow-job-pulse" aria-hidden /> : null}
              {tone.label}
            </span>
          </div>
          <div className="flow-job-meta">
            <span className="flow-job-app" title={j.appId}>
              {j.appId}
            </span>
            <span className="flow-job-duration">{formatDuration(j.durationMs)}</span>
          </div>
        </div>
      </foreignObject>
    </g>
  )
}

interface FlowEdgePathProps {
  edge: FlowEdge
}

function FlowEdgePath({ edge }: FlowEdgePathProps) {
  const d = edgeToPath(edge.points)
  if (!d) return null
  let stroke = 'var(--color-text-dim)'
  let strokeWidth = 1.25
  let dash: string | undefined
  let marker = 'url(#flow-arrow)'
  let opacity = 0.6
  if (edge.kind === 'meta' || edge.kind === 'cross-batch-job') {
    stroke = '#38BDF8'
    strokeWidth = 1.75
    marker = 'url(#flow-arrow-meta)'
    opacity = 0.85
  }
  if (edge.blocked) {
    stroke = '#F87171'
    dash = '4 4'
    marker = 'url(#flow-arrow-blocked)'
    opacity = 0.95
  }
  return (
    <path
      d={d}
      fill="none"
      stroke={stroke}
      strokeWidth={strokeWidth}
      strokeDasharray={dash}
      opacity={opacity}
      markerEnd={marker}
      data-testid={`flow-edge-${edge.fromId}-${edge.toId}`}
      data-kind={edge.kind}
      data-blocked={edge.blocked ? 'true' : 'false'}
    >
      {edge.tooltip ? <title>{edge.tooltip}</title> : null}
    </path>
  )
}

/* ──────────────────────────────────────────────────────────────────
 * Helpers + tone tables
 * ────────────────────────────────────────────────────────────────── */

function formatDuration(ms: number): string {
  if (!Number.isFinite(ms) || ms <= 0) return '—'
  const totalSec = Math.floor(ms / 1000)
  const h = Math.floor(totalSec / 3600)
  const m = Math.floor((totalSec % 3600) / 60)
  const s = totalSec % 60
  if (h > 0) return `${h}h ${m}m`
  if (m > 0) return `${m}m ${s}s`
  return `${s}s`
}

interface LaneTone {
  bg: string
  border: string
  headerBg: string
  progress: string
}

const LANE_TONE: Record<FlowBatchLane['status'], LaneTone> = {
  succeeded: {
    bg: 'rgba(74,222,128,0.06)',
    border: 'rgba(74,222,128,0.45)',
    headerBg: 'rgba(74,222,128,0.16)',
    progress: '#4ADE80',
  },
  running: {
    bg: 'rgba(56,189,248,0.06)',
    border: 'rgba(56,189,248,0.45)',
    headerBg: 'rgba(56,189,248,0.18)',
    progress: '#38BDF8',
  },
  failed: {
    bg: 'rgba(248,113,113,0.06)',
    border: 'rgba(248,113,113,0.55)',
    headerBg: 'rgba(248,113,113,0.18)',
    progress: '#F87171',
  },
  pending: {
    bg: 'rgba(148,163,184,0.04)',
    border: 'rgba(148,163,184,0.35)',
    headerBg: 'rgba(148,163,184,0.14)',
    progress: '#94A3B8',
  },
  mixed: {
    bg: 'rgba(245,158,11,0.06)',
    border: 'rgba(245,158,11,0.45)',
    headerBg: 'rgba(245,158,11,0.16)',
    progress: '#F59E0B',
  },
}

const JOB_STATUS_TONE: Record<
  Job['status'],
  { bg: string; border: string; label: string }
> = {
  pending:   { bg: 'rgba(15,23,42,0.55)',     border: 'rgba(148,163,184,0.30)', label: 'Pending'   },
  running:   { bg: 'rgba(15,23,42,0.55)',     border: 'rgba(56,189,248,0.55)',  label: 'Running'   },
  succeeded: { bg: 'rgba(15,23,42,0.55)',     border: 'rgba(74,222,128,0.55)',  label: 'Succeeded' },
  failed:    { bg: 'rgba(15,23,42,0.55)',     border: 'rgba(248,113,113,0.55)', label: 'Failed'    },
}

/* ──────────────────────────────────────────────────────────────────
 * Styles — co-located with the component so we don't grow another
 * CSS module. Mirrors JobsTable's strategy.
 * ────────────────────────────────────────────────────────────────── */

const JOBS_FLOW_CSS = `
.jobs-flow-wrap { width: 100%; }

.jobs-flow-scroll {
  width: 100%;
  overflow-x: auto;
  overflow-y: auto;
  border: 1px solid var(--color-border);
  border-radius: 12px;
  background: var(--color-surface);
  max-height: 78vh;
}
.jobs-flow-svg {
  display: block;
  font-family: var(--font-sans, system-ui);
}

.flow-batch-header {
  display: flex;
  align-items: center;
  gap: 0.45rem;
  height: 100%;
  color: var(--color-text);
  font-size: 0.78rem;
  font-weight: 600;
}
.flow-batch-toggle {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 16px;
  height: 16px;
  padding: 0;
  border: none;
  background: transparent;
  color: var(--color-text-dim);
  cursor: pointer;
  border-radius: 4px;
}
.flow-batch-toggle:hover { color: var(--color-text); background: rgba(148,163,184,0.10); }
.flow-batch-name {
  flex: 1 1 auto;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  font-family: var(--font-mono, ui-monospace, monospace);
  letter-spacing: 0.02em;
}
.flow-batch-count {
  font-size: 0.7rem;
  color: var(--color-text-dim);
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}

.flow-batch-collapsed {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  height: 100%;
  gap: 0.15rem;
}
.flow-batch-collapsed-pct {
  font-size: 1.1rem;
  font-weight: 700;
  color: var(--color-text-strong);
  font-variant-numeric: tabular-nums;
}
.flow-batch-collapsed-meta {
  font-size: 0.7rem;
  color: var(--color-text-dim);
  font-variant-numeric: tabular-nums;
}

.flow-job-card {
  display: flex;
  flex-direction: column;
  justify-content: space-between;
  height: 100%;
  width: 100%;
  padding: 0.45rem 0.55rem;
  box-sizing: border-box;
  pointer-events: none;
}
.flow-job-row {
  display: flex;
  align-items: center;
  gap: 0.4rem;
  justify-content: space-between;
}
.flow-job-name {
  flex: 1 1 auto;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  font-size: 0.78rem;
  font-weight: 600;
  color: var(--color-text-strong);
}
.flow-job-meta {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 0.4rem;
  font-size: 0.66rem;
  color: var(--color-text-dim);
  font-family: var(--font-mono, ui-monospace, monospace);
}
.flow-job-app {
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  color: #38BDF8;
}
.flow-job-duration {
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}

.flow-job-badge {
  display: inline-flex;
  align-items: center;
  gap: 0.25rem;
  padding: 0.08rem 0.4rem;
  border-radius: 999px;
  font-size: 0.58rem;
  font-weight: 700;
  letter-spacing: 0.04em;
  text-transform: uppercase;
  white-space: nowrap;
}
.flow-job-badge-pending   { background: rgba(148,163,184,0.10); color: var(--color-text-dim); }
.flow-job-badge-running   { background: rgba(56,189,248,0.10);  color: #38BDF8; }
.flow-job-badge-succeeded { background: rgba(74,222,128,0.10);  color: #4ADE80; }
.flow-job-badge-failed    { background: rgba(248,113,113,0.10); color: #F87171; }

.flow-job-pulse {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: currentColor;
  animation: flow-pulse 1.6s ease-in-out infinite;
}
@keyframes flow-pulse { 0%,100%{opacity:1;transform:scale(1)} 50%{opacity:.4;transform:scale(.6)} }

.flow-job-rect:hover { filter: brightness(1.08); }
`
