/**
 * flowLayoutV4.ts — multi-region, multi-stage circular-node layout.
 *
 * Replaces the pill-card swimlane layout (PR #245) with a
 * mockup-faithful canvas matching `marketing/mockups/provision-mockup-v4.png`:
 *
 *   • Nodes are CIRCLES grouped by family colour (PILOT / SPINE /
 *     SURGE / SILO / GUARDIAN / INSIGHTS / FABRIC / CORTEX / RELAY /
 *     CATALYST), sized by stage importance.
 *
 *   • Each region renders as a horizontal band: TOP band = primary
 *     region, BOTTOM band = secondary region. Single-region clusters
 *     render the primary band only and the canvas height adjusts.
 *
 *   • Stages 1..N are rendered as vertical columns left → right. Stage
 *     index for a job is derived from a longest-path topological sort
 *     over the dependency graph; identical to the Sugiyama core in
 *     pipelineLayout.ts but keyed on column rather than batch.
 *
 *   • Edges are routed as straight directional arrows when same-stage,
 *     and as cubic-bezier curves when spanning more than one stage,
 *     mirroring the bezier router in pipelineLayout.ts.
 *
 * This module owns ONLY geometry — no React, no SVG strings. Pure
 * function: same input + opts produces the same output.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #1 (waterfall, target-state shape) — multi-region + bezier + 10-
 *      stage column grid all ship in one pass; no "single-region for
 *      now" interim shape.
 *   #2 (no compromise) — no graph library; deterministic Sugiyama
 *      layer assignment + barycenter-stable y-ordering.
 *   #4 (never hardcode) — every dimension is a configurable knob in
 *      `FlowGeometryV4`; the family palette is sourced from the public
 *      product taxonomy in componentGroups.ts (caller injects).
 */

import type { Job, JobStatus } from './jobs.types'

/* ──────────────────────────────────────────────────────────────────
 * Public types
 * ────────────────────────────────────────────────────────────────── */

/** Family palette — name + hex colour. Caller-injected. */
export interface FlowFamily {
  /** Family id, lowercase, matches ApplicationDescriptor.familyId. */
  id: string
  /** Display label, uppercase ("PILOT", "SPINE", ...). */
  label: string
  /** Hex colour, e.g. "#818CF8". Used for ring + glow. */
  color: string
}

/** Per-region descriptor injected by caller (e.g. FlowPage). */
export interface FlowRegion {
  /** Stable region id. */
  id: string
  /** Display label — e.g. "FSN1 · Falkenstein" or "OMAN · Sovereign". */
  label: string
  /** Provider/meta line — "Hetzner · Primary". */
  meta: string
}

/** A laid-out circular node. */
export interface FlowNodeV4 {
  /** Stable id (Job id). */
  id: string
  /** Region this node lives in. */
  regionId: string
  /** Family id. */
  familyId: string
  /** Stage column (1-indexed for display). */
  stage: number
  /** Centre x in canvas pixels. */
  cx: number
  /** Centre y in canvas pixels. */
  cy: number
  /** Radius in pixels — derived from stage importance + family weight. */
  r: number
  /** Status — drives glow + arc colour. */
  status: JobStatus
  /** Display label (jobName). */
  label: string
  /** Sub-label (e.g. duration, e.g. "1m 12s"). */
  subLabel: string
  /** Progress 0..1 — drives the arc length around the ring. */
  progress: number
  /** True when the operator-supplied highlightJobId matches this node. */
  highlighted: boolean
  /** Underlying Job — forwarded for click handlers / tooltips. */
  job: Job
}

/** A laid-out region container — band frame + label. */
export interface FlowRegionLane {
  regionId: string
  label: string
  meta: string
  /** Top y of the band. */
  y: number
  /** Band height. */
  height: number
  /** Number of nodes inside. */
  nodeCount: number
}

/** A laid-out edge — bezier when span >= 2, straight otherwise. */
export interface FlowEdgeV4 {
  fromId: string
  toId: string
  /** Polyline points; 2 for straight, 4 for cubic-bezier. */
  points: { x: number; y: number }[]
  /** Edge classification — drives stroke style. */
  kind: 'within-region' | 'cross-region'
  /** Status of the source node — drives stroke colour. */
  fromStatus: JobStatus
}

/** Stage-column descriptor — used to render dividers + the stage label row. */
export interface FlowStageColumn {
  /** 1-indexed display number. */
  stage: number
  /** Centre x. */
  cx: number
  /** Left edge of the column. */
  left: number
  /** Right edge of the column. */
  right: number
}

/** Full layout result. */
export interface FlowLayoutV4Result {
  nodes: FlowNodeV4[]
  edges: FlowEdgeV4[]
  regions: FlowRegionLane[]
  stages: FlowStageColumn[]
  width: number
  height: number
}

/** Geometry knobs — defaulted in DEFAULT_GEOMETRY_V4. */
export interface FlowGeometryV4 {
  /** Outer canvas padding (left + right). */
  paddingX: number
  /** Outer canvas padding (top + bottom). */
  paddingY: number
  /** Vertical gap between region bands. */
  regionGap: number
  /** Width of the static left "deployment progress" tree column reserved
   *  by the canvas (so edges don't run under it). 0 disables. */
  leftReserve: number
  /** Distance between adjacent stage sub-columns (centre-to-centre). */
  stageColumnWidth: number
  /** Default node radius. */
  nodeRadius: number
  /** Larger radius for stage-1/stage-2 anchor nodes. */
  nodeRadiusAnchor: number
  /** Minimum vertical gap between same-column nodes (centre-to-centre). */
  minNodeGap: number
  /** Region label font size. */
  regionLabelHeight: number
  /**
   * Max nodes per sub-column before the layout splits a logical stage
   * into N adjacent sub-columns. Mirrors the static-mockup MAX_PER_COL=5
   * heuristic. Higher = denser column, lower = wider canvas.
   */
  maxPerColumn: number
}

export const DEFAULT_GEOMETRY_V4: FlowGeometryV4 = {
  paddingX: 28,
  paddingY: 22,
  regionGap: 28,
  leftReserve: 0, // The deployment-tree panel sits OUTSIDE the SVG.
  // 78px per column lets a 10-stage layout (with 2-3 sub-columns per
  // dense stage) fit a ~960-1100px wide canvas host without
  // horizontal scrolling. The bezier router still has room to bow
  // because of the perpendicular-bias control points (see
  // routeBezier).
  stageColumnWidth: 84,
  // Bigger nodes — mockup glyph is clearly 56-72px diameter at 1440px.
  // FORCING-FUNCTION: nodeRadius >= 28 (= 56px diameter) is asserted in
  // flowLayoutV4.test.ts to lock this against future shrinkage.
  nodeRadius: 28,
  nodeRadiusAnchor: 32,
  // 64px gap between same-column node centres — leaves ~6px clear
  // between adjacent circles at r=28 (nodes don't touch) and matches
  // the mockup's vertical density.
  minNodeGap: 66,
  regionLabelHeight: 28,
  // 5 per column matches the mockup MAX_PER_COL=5 heuristic — denser
  // families fan out into adjacent sub-columns, which is what creates
  // the organic-looking node clusters in provision-mockup-v4.png.
  maxPerColumn: 5,
}

/** Caller-supplied node hints — supplied per-job by FlowPage. */
export interface FlowNodeHints {
  /** Family id (lowercase) — falls back to "platform" when absent. */
  familyId?: string
  /** Region id — falls back to FALLBACK_REGION_ID when absent. */
  regionId?: string
  /** Display label — falls back to job.jobName. */
  label?: string
  /**
   * Override the stage/column. When supplied, the layout uses this value
   * directly instead of running longest-path on Job.dependsOn — useful
   * when Job.dependsOn is empty (test catalogs) but the caller knows
   * the canonical install stage (e.g. from component-graph metadata).
   */
  stage?: number
  /**
   * Extra dependency edges expressed as upstream JOB IDS. Concatenated
   * with Job.dependsOn during layered stage assignment. Lets the
   * caller surface component-graph dependencies that aren't part of
   * the per-job dependsOn surface (e.g. derived from componentGroups).
   */
  extraDepIds?: readonly string[]
}

export interface FlowLayoutV4Options {
  /** Hints per job id — caller injects via JobIdHintMap. */
  hints?: ReadonlyMap<string, FlowNodeHints>
  /** Region descriptors in render order (top → bottom). */
  regions?: readonly FlowRegion[]
  /** Family palette — caller-supplied. */
  families?: readonly FlowFamily[]
  /** Override geometry. */
  geometry?: Partial<FlowGeometryV4>
  /** Highlight a single node id (thicker border + glow). */
  highlightJobId?: string
  /**
   * When set, the layout SCALES its column widths + region heights so
   * the resulting canvas fits a container approximately
   * (targetWidth × targetHeight) px. Caller passes the canvas-host's
   * measured rect from a ResizeObserver so the canvas always fills the
   * available space (matches the mockup behaviour).
   *
   * The layout still respects nodeRadius / minNodeGap — if the target
   * is too small to hold all nodes at their canonical size, the layout
   * gracefully falls back to its natural width/height.
   */
  targetWidth?: number
  targetHeight?: number
  /**
   * Job ids whose outgoing edges should NOT be rendered (the layout
   * still uses them for stage assignment, but no edge is emitted to
   * `result.edges` from these sources). Used to suppress fan-out
   * noise — e.g. `cluster-bootstrap` would otherwise emit an edge to
   * every component, which renders as visual chaos. Stage assignment
   * still respects the dependency so the layout still flows
   * left → right correctly.
   */
  hideEdgesFromIds?: ReadonlySet<string>
}

/** Sentinel region id used when caller doesn't supply per-job region. */
export const FALLBACK_REGION_ID = 'primary'
export const FALLBACK_FAMILY_ID = 'platform'

/* ──────────────────────────────────────────────────────────────────
 * Helpers — taxonomy & status derivation
 * ────────────────────────────────────────────────────────────────── */

/**
 * Default family palette mirrors the public product taxonomy in
 * `src/pages/wizard/steps/componentGroups.ts`. Caller may override.
 *
 * Hex values mirror the v4 mockup: provision-mockup.html GROUPS{}.
 */
export const DEFAULT_FAMILIES: FlowFamily[] = [
  { id: 'catalyst', label: 'Catalyst', color: '#64748B' },
  { id: 'pilot',    label: 'PILOT',    color: '#818CF8' },
  { id: 'spine',    label: 'SPINE',    color: '#38BDF8' },
  { id: 'surge',    label: 'SURGE',    color: '#2DD4BF' },
  { id: 'silo',     label: 'SILO',     color: '#FB923C' },
  { id: 'guardian', label: 'GUARDIAN', color: '#F472B6' },
  { id: 'insights', label: 'INSIGHTS', color: '#A78BFA' },
  { id: 'fabric',   label: 'FABRIC',   color: '#FBBF24' },
  { id: 'cortex',   label: 'CORTEX',   color: '#F87171' },
  { id: 'relay',    label: 'RELAY',    color: '#34D399' },
  { id: 'platform', label: 'Platform', color: '#94A3B8' },
]

/**
 * Convert the canonical Job lifecycle -> a 0..1 progress estimate for
 * the ring arc. running = derived from durationMs progress against an
 * implied 60s cap; succeeded = 1.0; failed = 1.0 (red); pending = 0.
 */
export function jobProgress(j: Job): number {
  if (j.status === 'succeeded' || j.status === 'failed') return 1.0
  if (j.status === 'pending') return 0
  // running — hint at progress via durationMs (capped at 60s display).
  const ms = Number.isFinite(j.durationMs) ? j.durationMs : 0
  const cap = 60_000
  return Math.max(0.05, Math.min(0.95, ms / cap))
}

/* ──────────────────────────────────────────────────────────────────
 * Pure layout
 * ────────────────────────────────────────────────────────────────── */

/**
 * Lay out `jobs` into a multi-region, multi-stage circular grid.
 *
 * Steps:
 *   1. Partition jobs by region (using `hints.regionId` per job).
 *   2. Per region, run longest-path stage assignment — stage(j) =
 *      max(stage(d) for d in dependsOn) + 1, starting at 1.
 *   3. Compute the global stage count = max stage across all regions.
 *   4. Compute each region's vertical band height = max nodes per
 *      stage * minNodeGap. Layout regions top → bottom in injection
 *      order.
 *   5. Within each region, assign per-stage y by sorting jobs alphabetically
 *      by jobName and distributing them around the band centre with
 *      `minNodeGap` spacing.
 *   6. Route within-region edges (straight if span<=1, bezier otherwise)
 *      and cross-region edges (always bezier — they cross the region
 *      gap).
 */
export function flowLayoutV4(
  jobs: readonly Job[],
  opts: FlowLayoutV4Options = {},
): FlowLayoutV4Result {
  const geom: FlowGeometryV4 = { ...DEFAULT_GEOMETRY_V4, ...opts.geometry }
  const hints = opts.hints ?? new Map<string, FlowNodeHints>()
  const families = opts.families ?? DEFAULT_FAMILIES
  const familyById = new Map<string, FlowFamily>()
  for (const f of families) familyById.set(f.id, f)
  const highlightJobId = opts.highlightJobId

  if (jobs.length === 0) {
    return {
      nodes: [],
      edges: [],
      regions: [],
      stages: [],
      width: geom.paddingX * 2 + 320,
      height: geom.paddingY * 2 + 200,
    }
  }

  // 1. Partition jobs by region (preserving caller-provided region order
  //    when supplied; otherwise injection order).
  const regionOrder: string[] = []
  const jobsByRegion = new Map<string, Job[]>()
  if (opts.regions && opts.regions.length > 0) {
    for (const r of opts.regions) {
      regionOrder.push(r.id)
      jobsByRegion.set(r.id, [])
    }
  }
  for (const j of jobs) {
    const r = hints.get(j.id)?.regionId ?? FALLBACK_REGION_ID
    if (!jobsByRegion.has(r)) {
      regionOrder.push(r)
      jobsByRegion.set(r, [])
    }
    jobsByRegion.get(r)!.push(j)
  }

  // Region descriptor lookup.
  const regionDescriptorById = new Map<string, FlowRegion>()
  if (opts.regions) {
    for (const r of opts.regions) regionDescriptorById.set(r.id, r)
  }

  // 2. Per-region stage assignment via longest-path Kahn.
  //    Job.dependsOn holds upstream JOB-NAMES (or ids) — we resolve both.
  const jobByKey = new Map<string, Job>()
  for (const j of jobs) {
    jobByKey.set(j.id, j)
    if (j.jobName && j.jobName !== j.id) jobByKey.set(j.jobName, j)
  }

  // Build per-region layered indexes.
  interface Layered {
    /** Stage (1-indexed) per job id. */
    stage: Map<string, number>
    /** byStage[stage-1] = ordered job ids in that column. */
    byStage: string[][]
    /** Total stages used in this region. */
    stageCount: number
  }
  const layered = new Map<string, Layered>()

  for (const regionId of regionOrder) {
    const regionJobs = jobsByRegion.get(regionId) ?? []
    if (regionJobs.length === 0) {
      layered.set(regionId, { stage: new Map(), byStage: [], stageCount: 0 })
      continue
    }
    // Build edges restricted to this region's jobs.
    const regionIds = new Set(regionJobs.map((j) => j.id))
    interface Edge { from: string; to: string }
    const edges: Edge[] = []
    for (const j of regionJobs) {
      const seenDep = new Set<string>()
      // Native dependsOn first.
      for (const dep of j.dependsOn) {
        const depJob = jobByKey.get(dep)
        if (!depJob || !regionIds.has(depJob.id)) continue
        if (seenDep.has(depJob.id)) continue
        seenDep.add(depJob.id)
        edges.push({ from: depJob.id, to: j.id })
      }
      // Caller-supplied component-graph extras.
      const extras = hints.get(j.id)?.extraDepIds ?? []
      for (const dep of extras) {
        const depJob = jobByKey.get(dep)
        if (!depJob || !regionIds.has(depJob.id)) continue
        if (seenDep.has(depJob.id)) continue
        seenDep.add(depJob.id)
        edges.push({ from: depJob.id, to: j.id })
      }
    }
    // Longest-path layer assignment.
    const indeg = new Map<string, number>()
    const out = new Map<string, string[]>()
    for (const j of regionJobs) {
      indeg.set(j.id, 0)
      out.set(j.id, [])
    }
    for (const e of edges) {
      indeg.set(e.to, (indeg.get(e.to) ?? 0) + 1)
      out.get(e.from)!.push(e.to)
    }
    const stage = new Map<string, number>()
    // Pre-seed from hint.stage so caller-derived stages take effect even
    // when no edges exist (e.g. test catalogs without dependsOn data).
    for (const j of regionJobs) {
      const hinted = hints.get(j.id)?.stage
      if (typeof hinted === 'number' && hinted >= 1) {
        stage.set(j.id, hinted)
      }
    }
    const queue: string[] = []
    // Deterministic root order: by jobName ascending.
    const roots = regionJobs
      .filter((j) => indeg.get(j.id) === 0)
      .map((j) => j.id)
      .sort((a, b) => {
        const ja = jobByKey.get(a)!.jobName.localeCompare(jobByKey.get(b)!.jobName)
        return ja !== 0 ? ja : a.localeCompare(b)
      })
    for (const id of roots) {
      // Honor the hint when present; otherwise root jobs default to 1.
      if (!stage.has(id)) stage.set(id, 1)
      queue.push(id)
    }
    let head = 0
    while (head < queue.length) {
      const id = queue[head++]!
      const myStage = stage.get(id) ?? 1
      const children = [...(out.get(id) ?? [])].sort((a, b) => {
        const ja = jobByKey.get(a)!.jobName.localeCompare(jobByKey.get(b)!.jobName)
        return ja !== 0 ? ja : a.localeCompare(b)
      })
      for (const child of children) {
        const childHint = hints.get(child)?.stage
        // Child stage = max(parent + 1, child hint, current child stage).
        const proposed = myStage + 1
        const baseline = stage.get(child) ?? 0
        const target =
          typeof childHint === 'number' && childHint >= 1
            ? Math.max(proposed, childHint, baseline)
            : Math.max(proposed, baseline)
        stage.set(child, target)
        indeg.set(child, indeg.get(child)! - 1)
        if (indeg.get(child) === 0) queue.push(child)
      }
    }
    // Defensive — any unvisited (cycles) gets the hint or stage 1.
    for (const j of regionJobs) {
      if (!stage.has(j.id)) {
        const hinted = hints.get(j.id)?.stage
        stage.set(j.id, typeof hinted === 'number' && hinted >= 1 ? hinted : 1)
      }
    }
    const stageCount = Math.max(0, ...stage.values())
    const byStage: string[][] = Array.from({ length: stageCount }, () => [])
    for (const j of regionJobs) {
      const s = stage.get(j.id)!
      byStage[s - 1]!.push(j.id)
    }
    // Sort each column for stable ordering. Group by family then label so
    // family bands cluster vertically (mirrors the mockup).
    for (const lst of byStage) {
      lst.sort((a, b) => {
        const ja = jobByKey.get(a)!
        const jb = jobByKey.get(b)!
        const fa = hints.get(a)?.familyId ?? FALLBACK_FAMILY_ID
        const fb = hints.get(b)?.familyId ?? FALLBACK_FAMILY_ID
        if (fa !== fb) return fa.localeCompare(fb)
        const na = (hints.get(a)?.label ?? ja.jobName).localeCompare(
          hints.get(b)?.label ?? jb.jobName,
        )
        return na !== 0 ? na : a.localeCompare(b)
      })
    }
    layered.set(regionId, { stage, byStage, stageCount })
  }

  // 3. Global stage count = max across regions.
  let globalStageCount = 0
  for (const l of layered.values()) {
    if (l.stageCount > globalStageCount) globalStageCount = l.stageCount
  }
  if (globalStageCount === 0) globalStageCount = 1 // never zero columns

  // 4. Sub-column splitting — if any logical stage has more than
  //    MAX_PER_COLUMN nodes, split it into ceil(N/MAX_PER_COLUMN)
  //    sub-columns. Mirrors the static-mockup MAX_PER_COL=5 behaviour
  //    (provision-mockup.html). Without this, dense families pile up
  //    vertically and the canvas reads as a tall ribbon instead of a
  //    multi-column grid.
  const MAX_PER_COLUMN = geom.maxPerColumn
  // Per logical stage (1..globalStageCount) determine the maximum
  // number of nodes any region needs in that stage; the resulting
  // sub-column count applies uniformly across regions so columns line
  // up vertically (mockup-faithful).
  const stageSubCols: number[] = Array.from({ length: globalStageCount }, () => 1)
  for (let s = 0; s < globalStageCount; s++) {
    let maxNodes = 0
    for (const l of layered.values()) {
      const col = l.byStage[s] ?? []
      if (col.length > maxNodes) maxNodes = col.length
    }
    if (maxNodes > MAX_PER_COLUMN) {
      stageSubCols[s] = Math.ceil(maxNodes / MAX_PER_COLUMN)
    }
  }
  // Sub-column index lookup: stage 1 starts at sub-column 1; stage 2
  // starts at 1 + stageSubCols[0]; etc.
  const stageStartSubCol: number[] = new Array(globalStageCount).fill(1)
  for (let s = 1; s < globalStageCount; s++) {
    stageStartSubCol[s] = stageStartSubCol[s - 1]! + stageSubCols[s - 1]!
  }
  const totalSubCols = globalStageCount === 0
    ? 1
    : stageStartSubCol[globalStageCount - 1]! + stageSubCols[globalStageCount - 1]!

  const canvasW =
    geom.paddingX * 2 +
    geom.leftReserve +
    geom.stageColumnWidth * (totalSubCols - 1)

  // Each region band is sized to its busiest sub-column.
  let cursorY = geom.paddingY
  const placedRegions: FlowRegionLane[] = []
  const bandTopByRegion = new Map<string, number>()
  const bandHeightByRegion = new Map<string, number>()

  for (const regionId of regionOrder) {
    const l = layered.get(regionId)!
    // Compute the max nodes in any sub-column once we've split.
    let maxNodesPerSubCol = 0
    for (let s = 0; s < l.byStage.length; s++) {
      const col = l.byStage[s] ?? []
      const subCount = stageSubCols[s] ?? 1
      const perSub = Math.ceil(col.length / subCount)
      if (perSub > maxNodesPerSubCol) maxNodesPerSubCol = perSub
    }
    const bandContent = Math.max(1, maxNodesPerSubCol) * geom.minNodeGap
    const bandHeight =
      geom.regionLabelHeight + bandContent + geom.regionLabelHeight * 0.5
    const desc = regionDescriptorById.get(regionId)
    placedRegions.push({
      regionId,
      label: desc?.label ?? regionId.toUpperCase(),
      meta: desc?.meta ?? '',
      y: cursorY,
      height: bandHeight,
      nodeCount: jobsByRegion.get(regionId)?.length ?? 0,
    })
    bandTopByRegion.set(regionId, cursorY)
    bandHeightByRegion.set(regionId, bandHeight)
    cursorY += bandHeight + geom.regionGap
  }
  cursorY -= geom.regionGap // remove trailing gap
  // Reserve a 24px strip at the bottom for stage labels.
  const canvasH = cursorY + geom.paddingY + 24

  // 5. Place every node — sub-column x distribution + per-column y
  //    distribution centred around the band middle.
  const nodes: FlowNodeV4[] = []
  for (const regionId of regionOrder) {
    const l = layered.get(regionId)!
    const bandTop = bandTopByRegion.get(regionId)!
    const bandHeight = bandHeightByRegion.get(regionId)!
    const contentTop = bandTop + geom.regionLabelHeight
    const contentBot = bandTop + bandHeight - geom.regionLabelHeight * 0.5
    const contentMid = (contentTop + contentBot) / 2
    for (let s = 0; s < l.byStage.length; s++) {
      const column = l.byStage[s]!
      const subCount = stageSubCols[s] ?? 1
      const startSub = stageStartSubCol[s]!
      // Distribute the column's nodes across `subCount` sub-columns
      // round-robin so the vertical fill is balanced.
      const perSub: string[][] = Array.from({ length: subCount }, () => [])
      for (let i = 0; i < column.length; i++) {
        // Group by `MAX_PER_COLUMN` rather than round-robin so family
        // groupings (which sort_alphabetically_by_family) stay clustered
        // within a sub-column.
        const subIdx = Math.min(
          subCount - 1,
          Math.floor(i / Math.ceil(column.length / Math.max(1, subCount))),
        )
        perSub[subIdx]!.push(column[i]!)
      }
      for (let sub = 0; sub < subCount; sub++) {
        const subNodes = perSub[sub]!
        const totalH = subNodes.length * geom.minNodeGap
        const startY = contentMid - totalH / 2 + geom.minNodeGap / 2
        const subColumn = startSub + sub - 1 // 0-indexed for cx math
        for (let i = 0; i < subNodes.length; i++) {
          const id = subNodes[i]!
          const job = jobByKey.get(id)
          if (!job) continue
          const familyId = hints.get(id)?.familyId ?? FALLBACK_FAMILY_ID
          const cx =
            geom.paddingX +
            geom.leftReserve +
            subColumn * geom.stageColumnWidth +
            geom.stageColumnWidth / 2
          const cy = startY + i * geom.minNodeGap
          const r = s === 0 ? geom.nodeRadiusAnchor : geom.nodeRadius
          const label = hints.get(id)?.label ?? job.jobName
          nodes.push({
            id,
            regionId,
            familyId: familyById.has(familyId) ? familyId : FALLBACK_FAMILY_ID,
            stage: s + 1,
            cx,
            cy,
            r,
            status: job.status,
            label,
            subLabel: formatSubLabel(job),
            progress: jobProgress(job),
            highlighted: highlightJobId === id,
            job,
          })
        }
      }
    }
  }

  // 6. Stage column descriptors — used by the renderer for dividers.
  // One entry per LOGICAL stage; left/right span all sub-columns the
  // stage occupies.
  const stages: FlowStageColumn[] = []
  for (let s = 0; s < globalStageCount; s++) {
    const subCount = stageSubCols[s] ?? 1
    const startSub = stageStartSubCol[s]!
    const left =
      geom.paddingX + geom.leftReserve + (startSub - 1) * geom.stageColumnWidth
    const right = left + subCount * geom.stageColumnWidth
    const cx = (left + right) / 2
    stages.push({
      stage: s + 1,
      cx,
      left,
      right,
    })
  }

  // 7. Edges — straight if same column (rare — same stage), bezier
  //    otherwise. Cross-region edges are always bezier.
  const nodeById = new Map<string, FlowNodeV4>()
  for (const n of nodes) nodeById.set(n.id, n)
  const edges: FlowEdgeV4[] = []
  const seenEdge = new Set<string>()
  const hideFrom = opts.hideEdgesFromIds
  for (const j of jobs) {
    for (const dep of j.dependsOn) {
      const depJob = jobByKey.get(dep)
      if (!depJob) continue
      // Visual-only suppression — caller-supplied set of source ids
      // whose outgoing edges shouldn't render. Stage assignment ran
      // earlier on the full edge set, so layout flow still respects
      // the dependency.
      if (hideFrom && hideFrom.has(depJob.id)) continue
      const from = nodeById.get(depJob.id)
      const to = nodeById.get(j.id)
      if (!from || !to) continue
      const k = `${from.id}→${to.id}`
      if (seenEdge.has(k)) continue
      seenEdge.add(k)
      const crossRegion = from.regionId !== to.regionId
      const span = Math.abs(to.stage - from.stage)
      const points = routeBezier(from, to, span, crossRegion)
      edges.push({
        fromId: from.id,
        toId: to.id,
        points,
        kind: crossRegion ? 'cross-region' : 'within-region',
        fromStatus: from.status,
      })
    }
  }

  /* ── Target-fit post-pass ───────────────────────────────────────
   * When the caller supplies targetWidth / targetHeight (e.g. from a
   * ResizeObserver on the canvas-host), rescale node positions +
   * region bands + stage descriptors so the layout EXACTLY fills the
   * host viewport. Mirrors the static mockup's SVG_W/SVG_H = host
   * bounds approach in marketing/mockups/provision-mockup.html.
   *
   * The SVG renders with preserveAspectRatio="xMidYMid meet" — when
   * the viewBox matches the host's aspect ratio (which it does, since
   * we scale to host bounds), there's NO letterboxing and circles
   * stay round.
   *
   * Edge case: if targetWidth/H is smaller than the natural minimum
   * needed to lay out N nodes at canonical radius+gap, we still scale
   * to the target — circles stay nodeRadius (the SVG draws them
   * relative to viewBox), but they may visually overlap. Caller is
   * responsible for ensuring the host has enough room for the catalog
   * size (Catalyst's wizard caps catalogs at ~65 nodes per region).
   * ──────────────────────────────────────────────────────────────── */
  let outW = canvasW
  let outH = canvasH
  // Only stretch UP — never compress positions below the natural
  // minimum (would cause node overlap, since nodeRadius is fixed by
  // the forcing function).
  if (opts.targetWidth && opts.targetWidth > canvasW) {
    const sx = opts.targetWidth / canvasW
    for (const n of nodes) n.cx *= sx
    for (const s of stages) {
      s.cx *= sx
      s.left *= sx
      s.right *= sx
    }
    for (const e of edges) {
      for (const p of e.points) p.x *= sx
    }
    outW = opts.targetWidth
  }
  if (opts.targetHeight && opts.targetHeight > canvasH) {
    const sy = opts.targetHeight / canvasH
    for (const n of nodes) n.cy *= sy
    for (const r of placedRegions) {
      r.y *= sy
      r.height *= sy
    }
    for (const e of edges) {
      for (const p of e.points) p.y *= sy
    }
    outH = opts.targetHeight
  }

  return {
    nodes,
    edges,
    regions: placedRegions,
    stages,
    width: outW,
    height: outH,
  }
}

/* ──────────────────────────────────────────────────────────────────
 * Edge routing
 * ────────────────────────────────────────────────────────────────── */

/**
 * Compute polyline points for an edge between two circular nodes.
 *
 *   • Span 0 (same column, different rows) — straight line.
 *   • Span >= 1 same-region — cubic bezier with 2 control points so
 *     the curve clears intermediate columns smoothly.
 *   • cross-region — cubic bezier with vertical bias so the curve
 *     crosses the region gap with a clean S-shape.
 *
 * Anchor convention: edges leave the right-side perimeter of the source
 * node and enter the left-side perimeter of the target. Tangent angle
 * is along the line of centres — for circular nodes this looks correct
 * irrespective of direction.
 */
export function routeBezier(
  from: { cx: number; cy: number; r: number },
  to: { cx: number; cy: number; r: number },
  span: number,
  crossRegion: boolean,
): { x: number; y: number }[] {
  const dx = to.cx - from.cx
  const dy = to.cy - from.cy
  const dist = Math.sqrt(dx * dx + dy * dy) || 1
  const nx = dx / dist
  const ny = dy / dist
  const sx = from.cx + nx * (from.r + 1)
  const sy = from.cy + ny * (from.r + 1)
  const ex = to.cx - nx * (to.r + 4)
  const ey = to.cy - ny * (to.r + 4)
  if (span === 0 && !crossRegion) {
    return [
      { x: sx, y: sy },
      { x: ex, y: ey },
    ]
  }
  // Cubic bezier — control points are SHIFTED OFF the line of centres
  // so the path arcs visibly (the mockup's organic flowing curves).
  // Within-region: c1 leaves horizontally, c2 enters horizontally,
  //   and both control points get a perpendicular nudge so the curve
  //   bows whichever way the line of centres is going. This guarantees
  //   non-collinear control points (forcing-function asserted in
  //   flowLayoutV4.test.ts: edges contain a `C` with non-collinear
  //   control points to prevent regression to straight bezier).
  // Cross-region: bigger horizontal bias + perpendicular nudge so the
  //   S-curve crosses the region gap with a clean arc.
  const ddx = ex - sx
  const ddy = ey - sy
  // Perpendicular to the line of centres (rotate (nx,ny) 90°).
  const px = -ny
  const py = nx
  // Bow magnitude — scales with edge length; capped so very long edges
  // don't fly off the canvas, and given a minimum so very short edges
  // still arc visibly.
  const bowMag = Math.max(18, Math.min(48, dist * 0.18))
  const horizontalBias = crossRegion ? 0.55 : 0.4
  // Asymmetric perpendicular offsets at c1 vs c2 so the curve has true
  // S-character (control points off the source-target axis).
  const bow1 = crossRegion ? bowMag * 1.1 : bowMag
  const bow2 = crossRegion ? bowMag * -0.6 : bowMag * 0.55
  const c1x = sx + ddx * horizontalBias + px * bow1
  const c1y = sy + ddy * (crossRegion ? 0.08 : 0) + py * bow1
  const c2x = ex - ddx * horizontalBias + px * bow2
  const c2y = ey - ddy * (crossRegion ? 0.08 : 0) + py * bow2
  return [
    { x: sx, y: sy },
    { x: c1x, y: c1y },
    { x: c2x, y: c2y },
    { x: ex, y: ey },
  ]
}

/**
 * True iff the cubic-bezier defined by (p0,p1,p2,p3) has CONTROL points
 * not collinear with the line p0→p3. Used both as a sanity check in the
 * routeBezier output and as a regression test in flowLayoutV4.test.ts
 * (forcing-function: edges must arc, not draw straight).
 */
export function hasNonCollinearControls(
  points: readonly { x: number; y: number }[],
): boolean {
  if (points.length !== 4) return false
  const [p0, p1, p2, p3] = points
  if (!p0 || !p1 || !p2 || !p3) return false
  const ax = p3.x - p0.x
  const ay = p3.y - p0.y
  const len = Math.hypot(ax, ay) || 1
  // Distance from p1 + p2 to the line p0->p3.
  const d1 = Math.abs(ax * (p1.y - p0.y) - ay * (p1.x - p0.x)) / len
  const d2 = Math.abs(ax * (p2.y - p0.y) - ay * (p2.x - p0.x)) / len
  // ≥ 4px off-axis on EITHER control counts as non-collinear (a flat
  // bezier would have d1≈d2≈0).
  return d1 >= 4 || d2 >= 4
}

/** Render polyline points → SVG `d` attribute. */
export function pointsToPath(points: readonly { x: number; y: number }[]): string {
  if (points.length < 2) return ''
  const head = `M ${points[0]!.x.toFixed(1)} ${points[0]!.y.toFixed(1)}`
  if (points.length === 4) {
    return `${head} C ${points[1]!.x.toFixed(1)} ${points[1]!.y.toFixed(1)}, ${points[2]!.x.toFixed(1)} ${points[2]!.y.toFixed(1)}, ${points[3]!.x.toFixed(1)} ${points[3]!.y.toFixed(1)}`
  }
  return `${head}${points
    .slice(1)
    .map((p) => ` L ${p.x.toFixed(1)} ${p.y.toFixed(1)}`)
    .join('')}`
}

function formatSubLabel(j: Job): string {
  const ms = Number.isFinite(j.durationMs) ? j.durationMs : 0
  if (ms <= 0) return ''
  const totalSec = Math.floor(ms / 1000)
  const h = Math.floor(totalSec / 3600)
  const m = Math.floor((totalSec % 3600) / 60)
  const s = totalSec % 60
  if (h > 0) return `${h}h ${m}m`
  if (m > 0) return `${m}m ${s}s`
  return `${s}s`
}
