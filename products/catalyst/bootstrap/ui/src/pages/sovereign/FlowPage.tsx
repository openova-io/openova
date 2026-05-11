/**
 * FlowPage — multi-region OpenovaFlow canvas (Agent #5).
 *
 * The page drives the standalone {@link FlowCanvas} from
 * `@openova/flow-canvas` against the openova-flow SSE stream surfaced by
 * catalyst-api (`GET /api/v1/flows/{deploymentId}/stream`). Per-region
 * adapter-flux DaemonSets emit FlowMessage envelopes for each
 * HelmRelease they observe; the openova-flow-server merges those
 * emissions into a single contract-shaped stream keyed by
 * `flowId = deploymentId`. The canvas renders the merged graph — one
 * bubble per (region, HR) pair.
 *
 * Founder-locked design (2026-05-11):
 *   • Multi-region provisions produce N bubbles per HR (one per region).
 *     The adapter-flux tags every FlowNode with `region: '<location-code>'`;
 *     the canvas swimlane layout buckets by `region` so fsn1 and hel1
 *     each get their own column.
 *   • Single-region provisions look identical to the legacy single-cluster
 *     view because the canvas falls back to a single synthetic region
 *     descriptor.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #1 (waterfall) — full target shape: live SSE consumer, merged
 *     multi-region graph, host/selection rings, no MVP fallback.
 *   #2 (no compromise) — one update path. No "Job-shape adapter" or
 *     legacy `/logs` consumer remains on the FlowPage code path.
 *   #4 (never hardcode) — endpoint composed from `API_BASE`, region
 *     descriptors derived from live FlowNode tags, palette/families
 *     supplied by props.
 *
 * Embedded mode (`embedded` prop, used by JobDetail) drops the
 * PortalShell + StatusStrip chrome — JobDetail owns those.
 */

import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from 'react'
import { Link, useNavigate, useParams, useSearch } from '@tanstack/react-router'
import { useWizardStore } from '@/entities/deployment/store'
import { DETECTED_MODE } from '@/shared/lib/detectMode'
import { PortalShell } from './PortalShell'
import { FlowCanvas } from '@openova/flow-canvas'
import { defaultFoldedAtDepth } from '@openova/flow-core'
import type { FamilyDescriptor } from '@openova/flow-core'
import { useFlowStream } from '@/lib/openflow-adapter-sse'
import {
  CATALYST_STATUS_PALETTE,
  flowStateToArrays,
  regionDescriptorsFromFlow,
  rollupFlowStatus,
} from '@/lib/flow-bridge'
import { DEFAULT_FAMILIES } from '@/lib/flowFamilyPalette'
import {
  StatusStrip,
  type ProvisioningStatus,
} from '@/components/StatusStrip'
import { FoldControls, resolveDepth, type FoldDepth } from './FoldControls'
import { PRODUCTS } from '@/pages/wizard/steps/componentGroups'

/* ──────────────────────────────────────────────────────────────────
 * URL helpers
 * ────────────────────────────────────────────────────────────────── */

/** Parse `?folded=id1,id2,…` into a Set. */
export function resolveFolded(raw: unknown): Set<string> {
  if (typeof raw !== 'string' || raw.length === 0) return new Set()
  return new Set(
    raw
      .split(',')
      .map((s) => s.trim())
      .filter((s) => s.length > 0),
  )
}

/* ──────────────────────────────────────────────────────────────────
 * Family palette — catalog-merged DEFAULT_FAMILIES
 * ────────────────────────────────────────────────────────────────── */

function useFamilyPalette(): FamilyDescriptor[] {
  return useMemo(() => {
    const fromCatalog = PRODUCTS.map((p) => {
      const fallback = DEFAULT_FAMILIES.find((f) => f.id === p.id)
      return {
        id: p.id,
        label: p.name,
        color: fallback?.color ?? '#94A3B8',
      } satisfies FamilyDescriptor
    })
    const seen = new Set(fromCatalog.map((f) => f.id))
    for (const f of DEFAULT_FAMILIES) {
      if (!seen.has(f.id)) fromCatalog.push(f)
    }
    return fromCatalog
  }, [])
}

/* ──────────────────────────────────────────────────────────────────
 * Component
 * ────────────────────────────────────────────────────────────────── */

interface FlowPageProps {
  /** Test seam — disables the live SSE EventSource attach. */
  disableStream?: boolean
  /**
   * Test seam — accepted for backwards compatibility with the legacy
   * FlowPage signature; ignored under the OpenovaFlow contract (no
   * separate Jobs backfill path). Kept so existing call sites + tests
   * keep type-checking without churn.
   */
  disableJobsBackfill?: boolean
  /** Embedded variant: render without the PortalShell + StatusStrip chrome. */
  embedded?: boolean
  /** Override the deploymentId param. */
  deploymentIdOverride?: string
  /**
   * Node id that "owns" this page — typically the JobDetail route's
   * `$jobId`. The canvas paints a persistent teal ring on this node
   * regardless of which is currently single-clicked. The default
   * `openJobId` is set to this id on first paint so the LogPane shows
   * the host's logs immediately.
   */
  hostJobId?: string | null
  /**
   * Notifies the parent (JobDetail) every time the canvas's selected
   * node changes. The host stays put across single-click selections;
   * the parent uses this hook to keep its LogPane in sync with the
   * currently-clicked node.
   */
  onOpenJobChange?: (jobId: string | null) => void
  /** Override the global default fold depth (test seam). */
  initialDepth?: FoldDepth
}

export function FlowPage({
  disableStream = false,
  // eslint-disable-next-line @typescript-eslint/no-unused-vars
  disableJobsBackfill: _disableJobsBackfill = false,
  embedded = false,
  deploymentIdOverride,
  hostJobId = null,
  onOpenJobChange,
  initialDepth,
}: FlowPageProps = {}) {
  const looseParams = useParams({ strict: false }) as { deploymentId?: string }
  const deploymentId = deploymentIdOverride ?? looseParams.deploymentId ?? ''
  const store = useWizardStore()

  const search = useSearch({ strict: false }) as {
    folded?: unknown
    depth?: unknown
  }
  const navigate = useNavigate()

  /* ── Data: live SSE stream from openova-flow-server ──────────── */

  const stream = useFlowStream({ deploymentId, disableStream })
  const sovereignFQDN = useMemo<string | null>(() => {
    const fromStream = stream.flow?.meta?.['sovereignFQDN']
    return typeof fromStream === 'string' && fromStream.length > 0 ? fromStream : null
  }, [stream.flow])

  /* ── Region descriptors (multi-region support) ───────────────── */

  const regions = useMemo(
    () => regionDescriptorsFromFlow(stream.nodes, store.regions),
    [stream.nodes, store.regions],
  )

  /* ── Family palette ──────────────────────────────────────────── */

  const families = useFamilyPalette()

  /* ── Fold state — URL (depth + folded) + per-node manual ─────── */

  const urlDepth = resolveDepth(search?.depth)
  const depth: FoldDepth = initialDepth ?? urlDepth
  const urlFoldedSet = useMemo(() => resolveFolded(search?.folded), [search?.folded])

  // Materialise the live Map state into the readonly arrays the
  // canvas + layout expect. The Maps live in the hook so envelope
  // merges stay O(1); this is one O(N) materialisation per render.
  const { nodes, relationships } = useMemo(() => flowStateToArrays(stream), [stream])

  const foldedSet = useMemo(() => {
    const baseline =
      depth === 'all'
        ? new Set<string>()
        : defaultFoldedAtDepth(nodes, relationships, depth)
    // Manual per-node overrides: an id present in `?folded=` forces
    // folded (additive) — the UI's Expand-all sets `?folded=` to empty
    // so this composition reads cleanly.
    for (const id of urlFoldedSet) baseline.add(id)
    return baseline
  }, [nodes, relationships, depth, urlFoldedSet])

  const setSearchPatch = useCallback(
    (patch: { folded?: string | undefined; depth?: string | undefined }) => {
      navigate({
        to: '.',
        search: (prev) => {
          const next: Record<string, unknown> = { ...(prev ?? {}) }
          if ('folded' in patch) {
            if (patch.folded && patch.folded.length > 0) next.folded = patch.folded
            else delete next.folded
          }
          if ('depth' in patch) {
            if (patch.depth) next.depth = patch.depth
            else delete next.depth
          }
          return next
        },
      })
    },
    [navigate],
  )

  const onDepthChange = useCallback(
    (next: FoldDepth) => {
      setSearchPatch({
        depth: next === 2 ? undefined : String(next),
        // Clear manual overrides — depth is the new global truth.
        folded: undefined,
      })
    },
    [setSearchPatch],
  )

  // Group ids are the contains-edge `toId`s — a node is a group iff
  // at least one contains-edge points at it.
  const groupIds = useMemo(() => {
    const out = new Set<string>()
    for (const r of relationships) {
      if (r.type === 'contains') out.add(r.toId)
    }
    return out
  }, [relationships])
  const hasGroups = groupIds.size > 0

  const onCollapseAll = useCallback(() => {
    setSearchPatch({ depth: '1', folded: [...groupIds].join(',') })
  }, [groupIds, setSearchPatch])

  const onExpandAll = useCallback(() => {
    setSearchPatch({ depth: 'all', folded: undefined })
  }, [setSearchPatch])

  const toggleFold = useCallback(
    (nodeId: string) => {
      if (!groupIds.has(nodeId)) return
      const next = new Set(urlFoldedSet)
      const isFolded = foldedSet.has(nodeId)
      if (isFolded) next.delete(nodeId)
      else next.add(nodeId)
      const arr = [...next].filter(Boolean)
      setSearchPatch({ folded: arr.length > 0 ? arr.join(',') : undefined })
    },
    [groupIds, foldedSet, urlFoldedSet, setSearchPatch],
  )

  /* ── Selection / host node tracking ──────────────────────────── */

  const [openJobId, setOpenJobIdState] = useState<string | null>(hostJobId)
  const prevHostRef = useRef<string | null>(null)
  useEffect(() => {
    if (hostJobId !== prevHostRef.current) {
      prevHostRef.current = hostJobId
      setOpenJobIdState(hostJobId)
      onOpenJobChange?.(hostJobId)
    }
  }, [hostJobId, onOpenJobChange])
  const setOpenJobId = useCallback(
    (next: string | null) => {
      setOpenJobIdState(next)
      onOpenJobChange?.(next)
    },
    [onOpenJobChange],
  )

  // FlowCanvas handles single-vs-double-click debounce internally; the
  // page only supplies the callbacks.
  const handleNodeOpen = useCallback(
    (nodeId: string) => {
      setOpenJobId(nodeId)
    },
    [setOpenJobId],
  )

  const handleNodeNavigate = useCallback(
    (nodeId: string) => {
      // Chroot-aware target: on the mother's monitoring surface the
      // deploymentId is in the URL; on the Sovereign's adult hostname
      // the deploymentId is implicit so the clean root form is correct.
      const target =
        deploymentId && DETECTED_MODE.mode !== 'sovereign'
          ? `/provision/${deploymentId}/jobs/${nodeId}`
          : `/jobs/${nodeId}`
      navigate({ to: target as never })
    },
    [navigate, deploymentId],
  )

  const handleFoldToggle = useCallback(
    (nodeId: string) => {
      toggleFold(nodeId)
    },
    [toggleFold],
  )

  const handleCanvasBackgroundClick = useCallback(() => {
    setOpenJobId(hostJobId)
  }, [setOpenJobId, hostJobId])

  /* ── StatusStrip rollup ──────────────────────────────────────── */

  const rollup = useMemo(
    () => rollupFlowStatus({ nodes: stream.nodes, relationships: stream.relationships }),
    [stream.nodes, stream.relationships],
  )
  const provisioningStatus: ProvisioningStatus = rollup.status
  const finishedCount = rollup.finished
  const totalCount = rollup.total

  const earliestStarted = useMemo<number | null>(() => {
    if (rollup.earliestStartedMs !== null) return rollup.earliestStartedMs
    if (typeof stream.flow?.startedAt === 'number' && stream.flow.startedAt > 0) {
      return stream.flow.startedAt
    }
    return null
  }, [rollup.earliestStartedMs, stream.flow])

  const [now, setNow] = useState<number>(() => Date.now())
  useEffect(() => {
    if (provisioningStatus !== 'running') return
    const id = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(id)
  }, [provisioningStatus])
  const elapsedMs = earliestStarted === null ? 0 : Math.max(0, now - earliestStarted)

  /* ── Canvas full-screen ──────────────────────────────────────── */

  const [canvasFullScreen, setCanvasFullScreen] = useState<boolean>(false)
  const toggleCanvasFullScreen = useCallback(() => {
    setCanvasFullScreen((v) => !v)
  }, [])
  useEffect(() => {
    if (!canvasFullScreen) return
    function onKey(e: KeyboardEvent) {
      if (e.key === 'Escape') {
        e.stopPropagation()
        setCanvasFullScreen(false)
      }
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [canvasFullScreen])

  /* ── Flow envelope for canvas ────────────────────────────────── */

  const flowInstance = useMemo(() => {
    if (stream.flow) return stream.flow
    // Synthetic placeholder for the canvas on first paint — the canvas
    // tolerates this (it only reads flow.id for the SVG title).
    return {
      id: deploymentId || 'pending',
      status: 'pending',
      startedAt: 0,
    }
  }, [stream.flow, deploymentId])

  /* ── Render ──────────────────────────────────────────────────── */

  const canvas = (
    <FlowCanvas
      flow={flowInstance}
      nodes={nodes}
      relationships={relationships}
      folded={foldedSet}
      hostNodeId={hostJobId}
      selectedNodeId={openJobId}
      palette={CATALYST_STATUS_PALETTE}
      families={families}
      regions={regions}
      onNodeOpen={handleNodeOpen}
      onNodeNavigate={handleNodeNavigate}
      onFoldToggle={handleFoldToggle}
      onBackgroundClick={handleCanvasBackgroundClick}
    />
  )

  const fullScreenButton = (
    <button
      type="button"
      className="flow-fullscreen-btn"
      data-testid="flow-fullscreen-btn"
      aria-label={canvasFullScreen ? 'Exit canvas full-screen' : 'Canvas full-screen'}
      aria-pressed={canvasFullScreen}
      title={canvasFullScreen ? 'Exit full-screen (Esc)' : 'Full-screen canvas'}
      onClick={toggleCanvasFullScreen}
    >
      {canvasFullScreen ? (
        <svg width="14" height="14" viewBox="0 0 14 14" aria-hidden>
          <path d="M5 1 L5 5 L1 5 M9 1 L9 5 L13 5 M5 13 L5 9 L1 9 M9 13 L9 9 L13 9"
                stroke="currentColor" strokeWidth="1.4" fill="none" strokeLinecap="round" strokeLinejoin="round" />
        </svg>
      ) : (
        <svg width="14" height="14" viewBox="0 0 14 14" aria-hidden>
          <path d="M1 5 L1 1 L5 1 M9 1 L13 1 L13 5 M13 9 L13 13 L9 13 M5 13 L1 13 L1 9"
                stroke="currentColor" strokeWidth="1.4" fill="none" strokeLinecap="round" strokeLinejoin="round" />
        </svg>
      )}
    </button>
  )

  const flowSurface = (
    <div
      className={`flow-surface${canvasFullScreen ? ' is-fullscreen' : ''}`}
      data-testid="flow-surface"
      data-canvas-fullscreen={canvasFullScreen ? 'true' : 'false'}
    >
      <div className="flow-canvas-host" data-testid="flow-canvas-host">
        {canvas}
        {fullScreenButton}
      </div>
    </div>
  )

  if (embedded) {
    return (
      <div className="flow-page-embedded" data-testid="flow-page-embedded">
        <style>{FLOW_PAGE_CSS}</style>
        {flowSurface}
      </div>
    )
  }

  return (
    <PortalShell
      deploymentId={deploymentId}
      sovereignFQDN={sovereignFQDN}
      pageTitle="Flow"
      headerSlotLeft={
        <Link
          to={`/jobs` as never}
          className="text-[11px] text-[var(--color-text-dim)] hover:text-[var(--color-text)] no-underline"
          data-testid="flow-page-back-to-table"
        >
          ← Back to table
        </Link>
      }
    >
      <style>{FLOW_PAGE_CSS}</style>

      <div className="mt-2">
        <StatusStrip
          deploymentId={deploymentId}
          sovereignFQDN={sovereignFQDN}
          status={provisioningStatus}
          finished={finishedCount}
          total={totalCount}
          elapsedMs={elapsedMs}
          trailing={
            <FoldControls
              depth={depth}
              onDepthChange={onDepthChange}
              onCollapseAll={onCollapseAll}
              onExpandAll={onExpandAll}
              hasGroups={hasGroups}
            />
          }
        />
      </div>

      <div className="mt-4">{flowSurface}</div>
    </PortalShell>
  )
}

const FLOW_PAGE_CSS = `
.flow-page-embedded { width: 100%; height: 100%; }

.flow-surface {
  display: block;
  width: 100%;
  border: 1px solid var(--color-border);
  border-radius: 14px;
  background: var(--color-surface, rgba(7,10,18,0.55));
  padding: 12px;
  min-height: 540px;
  height: calc(100vh - 220px);
  max-height: 820px;
}

.flow-page-embedded .flow-surface {
  min-height: 0;
  padding: 0;
  border: 0;
  background: transparent;
  height: 100%;
  max-height: none;
}

/* Canvas full-screen mode — overlays the entire viewport above
   everything except the LogPane (which has z-index 60 by default
   and 80 in its own full-screen mode). The canvas occupies 100vw /
   100vh; the LogPane stays docked on top of it so the operator can
   still tail logs in the wider canvas. The Esc key exits — the
   FlowPage's keydown listener handles that. */
.flow-surface.is-fullscreen {
  position: fixed;
  top: 0;
  left: 0;
  right: 0;
  bottom: 0;
  width: 100vw;
  height: 100vh;
  max-height: none;
  z-index: 90;
  border-radius: 0;
  border: 0;
  padding: 0;
  background: rgba(2, 6, 15, 0.98);
}

.flow-canvas-host {
  position: relative;
  min-width: 0;
  height: 100%;
  background: var(--flow-canvas-bg, radial-gradient(ellipse at 20% 0%, rgba(11,28,58,0.85) 0%, rgba(7,10,18,0.85) 75%));
  border-radius: 12px;
  border: 1px solid var(--flow-canvas-border, rgba(255,255,255,0.04));
  overflow: auto;
  display: flex;
  align-items: center;
  justify-content: center;
}
[data-theme="light"] .flow-canvas-host {
  --flow-canvas-bg: radial-gradient(ellipse at 20% 0%, #f1f5f9 0%, #e2e8f0 75%);
  --flow-canvas-border: #cbd5e1;
}

.flow-surface.is-fullscreen .flow-canvas-host {
  border-radius: 0;
  border: 0;
}

.flow-page-embedded .flow-canvas-host {
  min-height: 0;
  height: 100%;
  max-height: none;
}

.flow-canvas-svg-organic {
  display: block;
  width: 100%;
  max-width: 100%;
  height: 100%;
  max-height: 100%;
}

/* Full-screen toggle button — sits in the top-right of the canvas
   host, mirroring the LogPane's full-screen toggle so the two
   surfaces feel symmetric. */
.flow-fullscreen-btn {
  position: absolute;
  top: 12px;
  right: 12px;
  width: 32px;
  height: 32px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  appearance: none;
  border: 1px solid var(--color-border);
  background: var(--color-surface);
  color: var(--color-text-dim);
  border-radius: 6px;
  cursor: pointer;
  z-index: 50;
  transition: color 0.12s ease, background-color 0.12s ease, border-color 0.12s ease;
}
.flow-fullscreen-btn:hover {
  color: var(--color-text-strong);
  border-color: var(--color-text-dim);
  background: rgba(148, 163, 184, 0.10);
}
.flow-fullscreen-btn[aria-pressed='true'] {
  color: var(--color-accent, #38BDF8);
  border-color: var(--color-accent, #38BDF8);
  background: rgba(56, 189, 248, 0.12);
}
`
