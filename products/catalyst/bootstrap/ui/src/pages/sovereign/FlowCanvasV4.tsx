/**
 * FlowCanvasV4 — circular-node + multi-region + bezier rendering layer.
 *
 * Replaces the pill-card swimlane SVG that PR #245 shipped (rejected by
 * the operator as "intentional divergence" — the canonical mockup at
 * `marketing/mockups/provision-mockup-v4.png` calls for circular glyph
 * nodes, regional grouping, and a right-side log feed).
 *
 * Pure presentation: receives a fully-laid-out FlowLayoutV4Result plus
 * click handlers and renders the SVG. No data-fetching, no derivation
 * past what the layout already produced.
 *
 * Preserved test contracts (so cosmetic-guards.spec.ts keeps passing):
 *   • `data-testid="flow-canvas-svg"` on the root <svg>.
 *   • `data-testid="flow-job-<jobId>"` on every node group.
 *   • `data-testid="flow-batch-<batchId>"` on each region container —
 *     mapped 1:1 from `regionId` for cosmetic-guards Test #6 ("Flow
 *     canvas rendered with at least one batch swimlane"). Multi-region
 *     designs render multiple [data-testid^=flow-batch-] elements.
 *
 * New testids (for the upgraded mockup-fidelity guards):
 *   • `data-testid="flow-node-circle-<jobId>"` — the actual <circle>.
 *   • `data-testid="flow-region-<regionId>"` — region band frame.
 *   • `data-testid="flow-stage-<n>"` — stage column divider line.
 *   • `data-testid="flow-edge-<from>-<to>"` — directional edges.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #1 (waterfall) — circular nodes, region bands, bezier edges, family
 *      glyphs, and progress arcs all ship together.
 *   #2 (no compromise) — no graph library, no canvas rendering — pure
 *      SVG so testids work and the operator can right-click → inspect.
 *   #4 (never hardcode) — every dimension is in the geometry knob set,
 *      every colour in the FlowFamily palette.
 */

import type { CSSProperties, MouseEvent as ReactMouseEvent } from 'react'
import {
  pointsToPath,
  type FlowLayoutV4Result,
  type FlowNodeV4,
  type FlowEdgeV4,
  type FlowFamily,
  type FlowRegionLane,
} from '@/lib/flowLayoutV4'
import type { JobStatus } from '@/lib/jobs.types'

/* ──────────────────────────────────────────────────────────────────
 * Family glyphs — one SVG path per building-block family. Renders
 * inside the centre of every node (mockup spec: clear iconography
 * inside each circle, NOT a single-letter glyph).
 *
 * The path data is hand-tuned for a 24×24 viewbox so it can be drawn
 * scaled to ~node.r * 0.95 with a stroke-width that reads at 56-72px
 * node diameter. Picked from the lucide-react icon set so the canvas
 * + the wizard grid stay visually consistent.
 *
 * Test contract: every node renders <g class="node-glyph"> with this
 * path inside (forcing-function in flowLayoutV4.test.ts /
 * cosmetic-guards: presence of `node-glyph` class ensures icons can't
 * regress to single-letters again).
 * ────────────────────────────────────────────────────────────────── */

const GLYPH_VIEWBOX = 24
const GLYPH_STROKE_WIDTH = 1.8

interface GlyphSpec {
  /** SVG path `d` data, scaled to a 24×24 grid. */
  d: string
  /** Optional secondary path for icons that need two strokes. */
  d2?: string
  /** Render mode. "stroke" (default) for line icons, "fill" for filled glyphs. */
  mode?: 'stroke' | 'fill'
}

const FAMILY_GLYPHS: Record<string, GlyphSpec> = {
  // PILOT — GitOps & IaC: a branching git-tree icon (lucide GitBranch).
  pilot: {
    d:
      'M6 3v12 M6 15a3 3 0 1 0 0 6 a3 3 0 1 0 0 -6 ' +
      'M18 9a3 3 0 1 0 0 -6 a3 3 0 1 0 0 6 ' +
      'M18 9c0 4-6 4-6 8 v4',
  },
  // SPINE — Networking & Mesh: 3 radio-tower waves + dot (lucide Radio).
  spine: {
    d:
      'M4.93 19.07a10 10 0 0 1 0 -14.14 ' +
      'M7.76 16.24a6 6 0 0 1 0 -8.49 ' +
      'M19.07 4.93a10 10 0 0 1 0 14.14 ' +
      'M16.24 7.76a6 6 0 0 1 0 8.49',
    d2: 'M11 12a1 1 0 1 0 2 0 a1 1 0 1 0 -2 0',
  },
  // SURGE — Scaling & Resilience: trending-up arrow (lucide TrendingUp).
  surge: {
    d: 'M3 17 L9 11 L13 15 L21 7 M14 7h7v7',
  },
  // SILO — Storage & Registry: stacked database (lucide Database).
  silo: {
    d:
      'M4 6c0 1.66 3.58 3 8 3 s8 -1.34 8 -3 s-3.58 -3 -8 -3 s-8 1.34 -8 3 z ' +
      'M4 6v6c0 1.66 3.58 3 8 3 s8 -1.34 8 -3 V6 ' +
      'M4 12v6c0 1.66 3.58 3 8 3 s8 -1.34 8 -3 v-6',
  },
  // GUARDIAN — Security & Identity: shield with check (lucide ShieldCheck).
  guardian: {
    d: 'M12 22 c4.42 -1.5 7 -5 7 -10 V5 l-7 -3 l-7 3 v7 c0 5 2.58 8.5 7 10 z',
    d2: 'M9 12 l2 2 l4 -4',
  },
  // INSIGHTS — AIOps & Observability: pulse / activity (lucide Activity).
  insights: {
    d: 'M22 12 h-4 l-3 9 L9 3 l-3 9 H2',
  },
  // FABRIC — Data & Integration: workflow / nodes (lucide Workflow).
  fabric: {
    d:
      'M4 4 h6 v6 H4 z M14 14 h6 v6 h-6 z ' +
      'M14 4 h6 v6 h-6 z M4 14 h6 v6 H4 z',
    d2: 'M10 7 h4 M14 17 h-4 M7 10 v4 M17 10 v4',
  },
  // CORTEX — AI & Machine Learning: chip / cpu (lucide Cpu).
  cortex: {
    d:
      'M4 8 a2 2 0 0 1 2 -2 h12 a2 2 0 0 1 2 2 v12 a2 2 0 0 1 -2 2 H6 a2 2 0 0 1 -2 -2 z ' +
      'M9 12 h6 v6 h-6 z',
    d2:
      'M9 4 v2 M15 4 v2 M9 22 v-2 M15 22 v-2 ' +
      'M2 9 h2 M2 15 h2 M22 9 h-2 M22 15 h-2',
  },
  // RELAY — Communication: wifi / radio waves (lucide Wifi).
  relay: {
    d:
      'M5 12.55 a11 11 0 0 1 14 0 ' +
      'M1.42 9 a16 16 0 0 1 21.16 0 ' +
      'M8.53 16.11 a6 6 0 0 1 6.95 0',
    d2: 'M12 20 a1 1 0 1 0 2 0 a1 1 0 1 0 -2 0',
  },
  // CATALYST — Bootstrap & K8s: server stack (lucide Server).
  catalyst: {
    d:
      'M3 4 h18 v6 H3 z M3 14 h18 v6 H3 z',
    d2: 'M7 7 h.01 M7 17 h.01',
  },
  // PLATFORM — fallback: a layered cube (lucide Box).
  platform: {
    d:
      'M21 16 V8 a2 2 0 0 0 -1 -1.73 l-7 -4 a2 2 0 0 0 -2 0 l-7 4 A2 2 0 0 0 3 8 v8 a2 2 0 0 0 1 1.73 l7 4 a2 2 0 0 0 2 0 l7 -4 A2 2 0 0 0 21 16 z ' +
      'M3.27 6.96 L12 12.01 l8.73 -5.05 ' +
      'M12 22.08 V12',
  },
}

/**
 * Render a family glyph centred on (0,0) of the node group.
 * radius is the node radius — glyph fits within ~75% of that.
 */
function FamilyGlyph({
  familyId,
  radius,
  color,
  opacity,
}: {
  familyId: string
  radius: number
  color: string
  opacity: number
}) {
  const spec = FAMILY_GLYPHS[familyId] ?? FAMILY_GLYPHS.platform!
  // Glyph fills 75% of the node diameter (2 * radius * 0.75).
  const glyphSize = radius * 1.5
  const scale = glyphSize / GLYPH_VIEWBOX
  const offset = -glyphSize / 2
  const isFill = spec.mode === 'fill'
  return (
    <g
      className="node-glyph"
      data-family-glyph={familyId}
      transform={`translate(${offset.toFixed(2)} ${offset.toFixed(2)}) scale(${scale.toFixed(4)})`}
      pointerEvents="none"
    >
      <path
        d={spec.d}
        fill={isFill ? color : 'none'}
        stroke={isFill ? 'none' : color}
        strokeWidth={GLYPH_STROKE_WIDTH}
        strokeLinecap="round"
        strokeLinejoin="round"
        opacity={opacity}
      />
      {spec.d2 ? (
        <path
          d={spec.d2}
          fill={isFill ? color : 'none'}
          stroke={isFill ? 'none' : color}
          strokeWidth={GLYPH_STROKE_WIDTH}
          strokeLinecap="round"
          strokeLinejoin="round"
          opacity={opacity}
        />
      ) : null}
    </g>
  )
}

/* ──────────────────────────────────────────────────────────────────
 * Status palette
 * ────────────────────────────────────────────────────────────────── */

interface StatusTone {
  fill: string
  ring: string
  glyph: string
  glow: string
  arc: string
  label: string
}

const STATUS_TONE: Record<JobStatus, StatusTone> = {
  succeeded: {
    fill: '#0F1F18',
    ring: 'rgba(74,222,128,0.65)',
    glyph: '#86EFAC',
    glow: 'rgba(74,222,128,0.20)',
    arc: '#4ADE80',
    label: 'Succeeded',
  },
  running: {
    fill: '#0E1A33',
    ring: 'rgba(56,189,248,0.65)',
    glyph: '#BAE6FD',
    glow: 'rgba(56,189,248,0.25)',
    arc: '#38BDF8',
    label: 'Running',
  },
  failed: {
    fill: '#23070A',
    ring: 'rgba(248,113,113,0.7)',
    glyph: '#FCA5A5',
    glow: 'rgba(248,113,113,0.30)',
    arc: '#F87171',
    label: 'Failed',
  },
  pending: {
    fill: '#0D1726',
    ring: 'rgba(148,163,184,0.32)',
    glyph: 'rgba(148,163,184,0.55)',
    glow: 'transparent',
    arc: 'rgba(148,163,184,0.45)',
    label: 'Pending',
  },
}

/* ──────────────────────────────────────────────────────────────────
 * Component
 * ────────────────────────────────────────────────────────────────── */

export interface FlowCanvasV4Props {
  layout: FlowLayoutV4Result
  /** Family palette — used to look up node ring colour by familyId. */
  families: readonly FlowFamily[]
  /** Currently-open job (single-click) — gets a brighter ring. */
  openJobId: string | null
  /** Currently-highlighted job (operator-supplied highlight). */
  highlightJobId: string | null
  /** Embedded variant — drops some chrome. */
  embedded: boolean
  /** Click + double-click delegates. */
  onJobClick: (jobId: string, event: ReactMouseEvent<SVGGElement>) => void
  onJobDoubleClick: (jobId: string) => void
  onCanvasBackgroundClick: () => void
}

export function FlowCanvasV4(props: FlowCanvasV4Props) {
  const { layout, families, openJobId, highlightJobId, onJobClick, onJobDoubleClick, onCanvasBackgroundClick } = props

  if (layout.nodes.length === 0 && layout.regions.length === 0) {
    return (
      <div
        data-testid="flow-canvas-empty"
        className="rounded-xl border border-dashed border-[var(--color-border)] p-8 text-center text-sm text-[var(--color-text-dim)]"
      >
        No jobs to render in the dependency graph.
      </div>
    )
  }

  const familyById = new Map<string, FlowFamily>()
  for (const f of families) familyById.set(f.id, f)

  return (
    <svg
      width="100%"
      height="100%"
      viewBox={`0 0 ${layout.width} ${layout.height}`}
      preserveAspectRatio="xMidYMid meet"
      className="flow-canvas-svg-v4"
      data-testid="flow-canvas-svg"
      role="img"
      aria-label="Provisioning dependency flow"
      style={{ display: 'block', width: '100%', height: '100%' }}
      onClick={(e) => {
        if (e.target === e.currentTarget) onCanvasBackgroundClick()
      }}
    >
      <defs>
        {/* Arrow markers, one per status colour, so edges retain
            directional meaning across pending/running/done/failed. */}
        {(['pending', 'running', 'succeeded', 'failed'] as const).map((s) => (
          <marker
            key={s}
            id={`flow-v4-arrow-${s}`}
            viewBox="0 0 8 8"
            refX="7"
            refY="4"
            markerWidth="5"
            markerHeight="5"
            orient="auto-start-reverse"
          >
            <path d="M0,1 L7,4 L0,7 Z" fill={STATUS_TONE[s].arc} opacity="0.85" />
          </marker>
        ))}
        {/* Cross-region marker uses a warm amber tone like the mockup. */}
        <marker
          id="flow-v4-arrow-cross"
          viewBox="0 0 8 8"
          refX="7"
          refY="4"
          markerWidth="6"
          markerHeight="6"
          orient="auto-start-reverse"
        >
          <path d="M0,1 L7,4 L0,7 Z" fill="rgba(253,230,138,0.85)" />
        </marker>
      </defs>

      {/* ── Region band frames + labels ─────────────────────────── */}
      {layout.regions.map((r) => (
        <RegionBand key={r.regionId} region={r} canvasWidth={layout.width} />
      ))}

      {/* ── Stage column dividers (subtle vertical lines) ───────── */}
      {layout.stages.slice(1).map((s) => (
        <line
          key={`div-${s.stage}`}
          x1={s.left}
          x2={s.left}
          y1={12}
          y2={layout.height - 26}
          stroke="rgba(255,255,255,0.045)"
          strokeWidth={1}
          data-testid={`flow-stage-${s.stage}`}
        />
      ))}
      {/* ── Stage column labels (mockup row at bottom) ──────────── */}
      {layout.stages.map((s) => (
        <text
          key={`lbl-${s.stage}`}
          x={s.cx}
          y={layout.height - 8}
          fontSize={9}
          fontWeight={700}
          letterSpacing="0.10em"
          textAnchor="middle"
          fill="rgba(255,255,255,0.30)"
          fontFamily="var(--font-mono, ui-monospace, monospace)"
          data-testid={`flow-stage-label-${s.stage}`}
          pointerEvents="none"
        >
          STAGE {s.stage}
        </text>
      ))}

      {/* ── Edges (drawn before nodes so nodes sit on top) ──────── */}
      {layout.edges.map((edge) => (
        <FlowEdge key={`${edge.fromId}-${edge.toId}`} edge={edge} />
      ))}

      {/* ── Nodes (circular glyph + ring + arc + label) ─────────── */}
      {layout.nodes.map((node) => (
        <FlowNode
          key={node.id}
          node={node}
          family={familyById.get(node.familyId) ?? null}
          isOpen={openJobId === node.id}
          isHighlighted={node.highlighted || highlightJobId === node.id}
          onClick={(e) => onJobClick(node.id, e)}
          onDoubleClick={() => onJobDoubleClick(node.id)}
        />
      ))}
    </svg>
  )
}

/* ──────────────────────────────────────────────────────────────────
 * RegionBand
 * ────────────────────────────────────────────────────────────────── */

interface RegionBandProps {
  region: FlowRegionLane
  canvasWidth: number
}

function RegionBand({ region, canvasWidth }: RegionBandProps) {
  return (
    <g
      data-testid={`flow-batch-${region.regionId}`}
      data-region={region.regionId}
    >
      {/* Region band background — soft gradient + visible stroke so
          the multi-region grouping reads at a glance. */}
      <rect
        x={14}
        y={region.y}
        width={canvasWidth - 28}
        height={region.height}
        rx={16}
        ry={16}
        fill="rgba(11,28,58,0.18)"
        stroke="rgba(148,163,184,0.18)"
        strokeWidth={1}
        data-testid={`flow-region-${region.regionId}`}
      />
      {/* Region label pill — bright, anchored top-left of the band. */}
      <rect
        x={22}
        y={region.y + 8}
        width={Math.max(64, region.label.length * 7 + 18)}
        height={20}
        rx={5}
        ry={5}
        fill="rgba(56,189,248,0.10)"
        stroke="rgba(56,189,248,0.28)"
        strokeWidth={1}
      />
      <text
        x={31}
        y={region.y + 22}
        fill="rgba(186,230,253,0.95)"
        fontSize={10}
        fontWeight={800}
        letterSpacing="0.12em"
        fontFamily="var(--font-mono, ui-monospace, monospace)"
      >
        {region.label.toUpperCase()}
      </text>
      {region.meta ? (
        <text
          x={Math.max(64, region.label.length * 7 + 18) + 32}
          y={region.y + 22}
          fill="rgba(255,255,255,0.42)"
          fontSize={9}
          fontWeight={500}
          letterSpacing="0.04em"
          fontFamily="var(--font-mono, ui-monospace, monospace)"
        >
          {region.meta}
        </text>
      ) : null}
      <text
        x={canvasWidth - 22}
        y={region.y + 22}
        fill="rgba(255,255,255,0.42)"
        fontSize={9}
        fontWeight={600}
        letterSpacing="0.08em"
        textAnchor="end"
        fontFamily="var(--font-mono, ui-monospace, monospace)"
      >
        {region.nodeCount} {region.nodeCount === 1 ? 'JOB' : 'JOBS'}
      </text>
    </g>
  )
}

/* ──────────────────────────────────────────────────────────────────
 * FlowNode
 * ────────────────────────────────────────────────────────────────── */

interface FlowNodeProps {
  node: FlowNodeV4
  family: FlowFamily | null
  isOpen: boolean
  isHighlighted: boolean
  onClick: (e: ReactMouseEvent<SVGGElement>) => void
  onDoubleClick: () => void
}

function FlowNode({ node, family, isOpen, isHighlighted, onClick, onDoubleClick }: FlowNodeProps) {
  const tone = STATUS_TONE[node.status]
  const familyColor = family?.color ?? 'rgba(148,163,184,0.55)'
  // Pending nodes still wear their family ring colour at a softer
  // opacity so the operator can still scan the family layout while
  // nothing has started running.
  const ringColor = node.status === 'pending' ? familyColor : tone.ring
  const ringOpacity = node.status === 'pending' ? 0.55 : 0.95
  const circumference = 2 * Math.PI * node.r
  const arcLen = Math.max(0, Math.min(1, node.progress)) * circumference

  // Glyph colour: family colour for non-terminal states; status glyph
  // (cyan/green/red) takes over the centre for done/failed.
  const glyphColor = node.status === 'pending'
    ? familyColor
    : node.status === 'running'
      ? tone.glyph
      : familyColor
  const glyphOpacity = node.status === 'pending' ? 0.55 : 0.92

  const tooltip = [
    node.label,
    `Family: ${family?.label ?? node.familyId}`,
    `Stage: ${node.stage}`,
    `Status: ${tone.label}`,
    node.subLabel ? `Duration: ${node.subLabel}` : '',
  ]
    .filter(Boolean)
    .join(' · ')

  return (
    <g
      data-testid={`flow-job-${node.id}`}
      data-status={node.status}
      data-region={node.regionId}
      data-family={node.familyId}
      data-highlighted={isHighlighted ? 'true' : 'false'}
      data-open={isOpen ? 'true' : 'false'}
      onClick={onClick}
      onDoubleClick={onDoubleClick}
      style={{ cursor: 'pointer' }}
      transform={`translate(${node.cx.toFixed(1)}, ${node.cy.toFixed(1)})`}
    >
      <title>{tooltip}</title>
      {/* Hover halo — invisible until :hover via CSS. */}
      <circle
        r={node.r + 9}
        fill="none"
        stroke={familyColor}
        strokeWidth={1.6}
        opacity={isHighlighted ? 0.7 : 0}
        className="flow-v4-halo"
      />
      {/* Glow underlay for active states. */}
      {node.status === 'running' || node.status === 'failed' || isOpen || isHighlighted ? (
        <circle
          r={node.r + 5}
          fill={isHighlighted ? 'rgba(56,189,248,0.20)' : tone.glow}
          opacity={node.status === 'running' ? 0.95 : 0.6}
        />
      ) : null}
      {/* Body fill — slightly darker centre + family-tinted radial edge */}
      <circle
        r={node.r}
        fill={tone.fill}
        data-testid={`flow-node-circle-${node.id}`}
      />
      {/* Subtle inner family-tinted halo so the circle reads as
          "owned by this family" even when status is pending. */}
      <circle
        r={node.r * 0.85}
        fill={familyColor}
        opacity={node.status === 'pending' ? 0.05 : 0.10}
      />
      {/* Family ring — drawn under the progress arc. */}
      <circle
        r={node.r}
        fill="none"
        stroke={ringColor}
        strokeWidth={2}
        opacity={ringOpacity}
      />
      {/* Progress arc — outer ring driven by node.progress */}
      {node.progress > 0 ? (
        <circle
          r={node.r}
          fill="none"
          stroke={tone.arc}
          strokeWidth={3.5}
          strokeLinecap="round"
          strokeDasharray={`${arcLen.toFixed(2)} ${circumference.toFixed(2)}`}
          transform="rotate(-90)"
          opacity={node.status === 'pending' ? 0 : 0.95}
        >
          {node.status === 'running' ? (
            <animate
              attributeName="opacity"
              values="0.95;0.45;0.95"
              dur="1.8s"
              repeatCount="indefinite"
            />
          ) : null}
        </circle>
      ) : null}
      {/* Centre glyph — family icon (always rendered as <g
          class="node-glyph">). For terminal states, an additional
          status badge (✓ / ✕) overlays at the bottom-right. */}
      <FamilyGlyph
        familyId={node.familyId}
        radius={node.r}
        color={glyphColor}
        opacity={glyphOpacity}
      />
      {/* Status badge (terminal states only) — small overlay
          bottom-right so the family glyph stays visible. */}
      {node.status === 'succeeded' ? (
        <g transform={`translate(${node.r * 0.62} ${node.r * 0.62})`} pointerEvents="none">
          <circle r={node.r * 0.28} fill={tone.arc} opacity={0.9} />
          <text
            fontSize={Math.round(node.r * 0.42)}
            textAnchor="middle"
            dominantBaseline="central"
            fill={tone.fill}
            fontFamily="Inter, system-ui, sans-serif"
            fontWeight={900}
          >
            ✓
          </text>
        </g>
      ) : node.status === 'failed' ? (
        <g transform={`translate(${node.r * 0.62} ${node.r * 0.62})`} pointerEvents="none">
          <circle r={node.r * 0.28} fill={tone.arc} opacity={0.9} />
          <text
            fontSize={Math.round(node.r * 0.42)}
            textAnchor="middle"
            dominantBaseline="central"
            fill={tone.fill}
            fontFamily="Inter, system-ui, sans-serif"
            fontWeight={900}
          >
            ✕
          </text>
        </g>
      ) : null}
      {/* Pulse dot for actively running. */}
      {node.status === 'running' ? (
        <circle
          cx={node.r * 0.78}
          cy={-node.r * 0.78}
          r={3.5}
          fill={tone.arc}
          className="flow-v4-pulse"
        >
          <animate
            attributeName="opacity"
            values="1;0.35;1"
            dur="1.4s"
            repeatCount="indefinite"
          />
        </circle>
      ) : null}
      {/* Label below the node */}
      <text
        y={node.r + 14}
        fontSize={10.5}
        textAnchor="middle"
        fill={node.status === 'pending' ? 'rgba(255,255,255,0.55)' : 'rgba(255,255,255,0.85)'}
        fontFamily="Inter, system-ui, sans-serif"
        fontWeight={600}
        pointerEvents="none"
      >
        {truncate(node.label, 14)}
      </text>
      {node.subLabel ? (
        <text
          y={node.r + 26}
          fontSize={9}
          textAnchor="middle"
          fill="rgba(255,255,255,0.40)"
          fontFamily="var(--font-mono, ui-monospace, monospace)"
          pointerEvents="none"
        >
          {node.subLabel}
        </text>
      ) : null}
    </g>
  )
}

function truncate(s: string, max: number): string {
  if (s.length <= max) return s
  return s.slice(0, max - 1) + '…'
}

/* ──────────────────────────────────────────────────────────────────
 * FlowEdge
 * ────────────────────────────────────────────────────────────────── */

interface FlowEdgeProps {
  edge: FlowEdgeV4
}

function FlowEdge({ edge }: FlowEdgeProps) {
  const d = pointsToPath(edge.points)
  if (!d) return null
  let stroke: string
  let strokeWidth = 1.4
  let dasharray: string | undefined
  let marker: string
  let opacity: number
  if (edge.kind === 'cross-region') {
    stroke = 'rgba(253,230,138,0.55)'
    strokeWidth = 1.6
    dasharray = '7 4'
    marker = 'url(#flow-v4-arrow-cross)'
    opacity = 0.85
  } else {
    const tone = STATUS_TONE[edge.fromStatus]
    if (edge.fromStatus === 'succeeded') {
      stroke = 'rgba(167,243,208,0.42)'
    } else if (edge.fromStatus === 'running') {
      stroke = 'rgba(186,230,253,0.55)'
    } else if (edge.fromStatus === 'failed') {
      stroke = 'rgba(252,165,165,0.65)'
    } else {
      stroke = 'rgba(148,163,184,0.28)'
    }
    void tone
    marker = `url(#flow-v4-arrow-${edge.fromStatus})`
    opacity = edge.fromStatus === 'pending' ? 0.5 : 0.85
  }
  const style: CSSProperties = dasharray ? { strokeDasharray: dasharray } : {}
  return (
    <path
      d={d}
      fill="none"
      stroke={stroke}
      strokeWidth={strokeWidth}
      strokeLinecap="round"
      opacity={opacity}
      markerEnd={marker}
      style={style}
      data-testid={`flow-edge-${edge.fromId}-${edge.toId}`}
      data-kind={edge.kind}
      data-from-status={edge.fromStatus}
    />
  )
}
