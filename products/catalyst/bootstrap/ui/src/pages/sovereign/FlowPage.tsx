/**
 * FlowPage — restored natural (force-directed, bounded, palette-tuned)
 * canvas wired to the live openova-flow SSE stream.
 *
 * # 2026-05-11 restore
 *
 * Founder rejected the lane-layout / synthetic-phase scaffolding shipped
 * via PR #1399/#1400/#1407 (Agent #5/#6/#9). The natural canvas is
 * `FlowCanvasOrganic` — same component the rest of the operator UX has
 * been tuned against (Bug #481, #532, #669 etc.). This file is the
 * post-revert wrapper:
 *
 *   • Data path: useFlowStream → flowStreamToOrganic → flowLayoutOrganic
 *     → FlowCanvasOrganic. No `regionDescriptorsFromFlow` lane columns,
 *     no `defaultFoldedAtDepth` from @openova/flow-core, no
 *     `FoldControls` chrome strip.
 *   • Fold UX: per-bubble disclosure badge (⊕ K / ⊖) anchored on group
 *     bubbles, plus a top-right depth chip (◀ L<n>/<max> ▶) overlaid on
 *     the canvas. ?folded= + ?depth= preserved as the shareable-link
 *     state.
 *   • Right-click on a group opens a small "Fold subtree / Expand all
 *     under here / Open in new tab" menu.
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
import { FlowCanvasOrganic } from './FlowCanvasOrganic'
import type { FlowOrganicAction } from './FlowCanvasOrganic'
import { flowLayoutOrganic } from '@/lib/flowLayoutOrganic'
import { useFlowStream } from '@/lib/openflow-adapter-sse'
import { rollupFlowStatus } from '@/lib/flow-bridge'
import {
  defaultFoldedAtContainmentDepth,
  descendantCountByGroup,
  flowStreamToOrganic,
  maxContainmentDepth,
} from '@/lib/flowStreamToOrganic'
import { resolveDepth, type FoldDepth } from './FoldControls'
import {
  StatusStrip,
  type ProvisioningStatus,
} from '@/components/StatusStrip'

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
 * Depth normalisation — the natural canvas already speaks integer
 * depth + 'all'. Convert FoldDepth → number|'all'.
 * ────────────────────────────────────────────────────────────────── */

function depthToNumeric(d: FoldDepth): number | 'all' {
  return d === 'all' ? 'all' : d
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

  /* ── Translate SSE state → Job[] + hints ─────────────────────── */

  const adapter = useMemo(
    () =>
      flowStreamToOrganic({
        nodes: [...stream.nodes.values()],
        relationships: [...stream.relationships.values()],
        wizardRegions: store.regions,
      }),
    [stream.nodes, stream.relationships, store.regions],
  )

  /* ── URL state — depth + folded ──────────────────────────────── */

  const urlDepth = resolveDepth(search?.depth)
  const depth: FoldDepth = initialDepth ?? urlDepth
  const urlFoldedSet = useMemo(() => resolveFolded(search?.folded), [search?.folded])

  /* ── Compose the folded set: depth baseline + manual overrides ── */

  const foldedSet = useMemo(() => {
    const baseline = defaultFoldedAtContainmentDepth(adapter.jobs, depthToNumeric(depth))
    for (const id of urlFoldedSet) baseline.add(id)
    return baseline
  }, [adapter.jobs, depth, urlFoldedSet])

  /* ── Run the natural organic layout ──────────────────────────── */

  const layout = useMemo(
    () =>
      flowLayoutOrganic(adapter.jobs, {
        hints: adapter.hints,
        regions: adapter.regions,
        families: adapter.families,
        folded: foldedSet,
      }),
    [adapter, foldedSet],
  )

  /* ── Fold-disclosure helpers ─────────────────────────────────── */

  const badgeCounts = useMemo(
    () => descendantCountByGroup(adapter.jobs, foldedSet),
    [adapter.jobs, foldedSet],
  )

  const maxDepth = useMemo(
    () => maxContainmentDepth(adapter.jobs),
    [adapter.jobs],
  )

  const setSearchPatch = useCallback(
    (patch: { folded?: string | undefined; depth?: string | undefined }) => {
      // Why no `to:` field: on contabo the router's basepath is
      // `/sovereign` (see app/router.tsx), and a literal `to: '.'`
      // resolves through TanStack's path matcher in a way that
      // drops the basepath AND re-encodes path params — so a
      // depth-chip click on /sovereign/provision/<id>/jobs/<depId>:install-X
      // would push the browser to /provision/<id>/jobs/<depId>%3Ainstall-X
      // (no /sovereign, colon encoded as %3A → 404 at the BE since
      // jobs.Store keys by bare jobName). Omitting `to:` keeps the
      // navigate as a search-params-only mutation; TanStack preserves
      // the current pathname verbatim including the basepath. The
      // colon-prefixed jobId comes from older deep-links — the fix
      // for those URLs lives in handleNodeDoubleClick (strips the
      // prefix); here we just refuse to rewrite the path.
      navigate({
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

  const stepDepth = useCallback(
    (dir: 1 | -1) => {
      // Translate FoldDepth ↔ number for stepping; 'all' is just past
      // maxDepth so ▶ from L<max> moves to 'all', ◀ from 'all' moves
      // back to L<max>.
      const cur = depth === 'all' ? maxDepth + 1 : depth
      const next = Math.max(1, Math.min(maxDepth + 1, cur + dir))
      const nextDepth: FoldDepth =
        next > maxDepth ? 'all' : (next as FoldDepth)
      // Clear manual overrides on chip step — chip is the global truth.
      setSearchPatch({
        depth: nextDepth === 2 ? undefined : String(nextDepth),
        folded: undefined,
      })
    },
    [depth, maxDepth, setSearchPatch],
  )

  // Esc clears manual overrides — snap to chip depth.
  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if (e.key !== 'Escape') return
      if (urlFoldedSet.size === 0) return
      setSearchPatch({ folded: undefined })
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [urlFoldedSet.size, setSearchPatch])

  const toggleFold = useCallback(
    (nodeId: string) => {
      if (!adapter.groupIds.has(nodeId)) return
      const next = new Set(urlFoldedSet)
      const isFolded = foldedSet.has(nodeId)
      if (isFolded) next.delete(nodeId)
      else next.add(nodeId)
      const arr = [...next].filter(Boolean)
      setSearchPatch({ folded: arr.length > 0 ? arr.join(',') : undefined })
    },
    [adapter.groupIds, foldedSet, urlFoldedSet, setSearchPatch],
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

  const handleNodeClick = useCallback(
    (nodeId: string) => {
      setOpenJobId(nodeId)
    },
    [setOpenJobId],
  )

  const handleNodeDoubleClick = useCallback(
    (nodeId: string) => {
      // Drill-down id form: jobs.Store.GetJob keys by bare jobName
      // (e.g. "install-reflector"), NOT the full
      // "<deploymentId>:install-reflector" id form the canvas emits.
      // Mirror useJobLinkBuilder (JobsTable.tsx line 364): strip the
      // "<deploymentId>:" prefix and URL-encode the remainder. Without
      // this the backend returns 404 because the exact-match path
      // misses on the colon-prefixed id (Traefik also drops the URL
      // encoding of `:` in path segments — see PR #1414 history).
      const bare = nodeId.includes(':') ? nodeId.slice(nodeId.indexOf(':') + 1) : nodeId
      const encoded = encodeURIComponent(bare)
      // Double-click navigates to the job-detail surface. Chroot-aware:
      // on the mother's monitoring view the deploymentId is in the URL;
      // on the Sovereign's adult hostname the deploymentId is implicit
      // so the clean root form is correct.
      const target =
        deploymentId && DETECTED_MODE.mode !== 'sovereign'
          ? `/provision/${deploymentId}/jobs/${encoded}`
          : `/jobs/${encoded}`
      navigate({ to: target as never })
    },
    [navigate, deploymentId],
  )

  const handleCanvasBackgroundClick = useCallback(() => {
    setOpenJobId(hostJobId)
  }, [setOpenJobId, hostJobId])

  /* ── Right-click action list ─────────────────────────────────── */

  const flowActions = useMemo<FlowOrganicAction[]>(
    () => [
      {
        id: 'fold-subtree',
        label: 'Fold subtree',
      },
      {
        id: 'fold-to-level',
        label: 'Fold to level N',
      },
      {
        id: 'expand-all-under',
        label: 'Expand all under here',
      },
      {
        id: 'open-new-tab',
        label: 'Open in new tab',
      },
    ],
    [],
  )

  const handleNodeAction = useCallback(
    (nodeId: string, actionId: string) => {
      switch (actionId) {
        case 'fold-subtree': {
          if (adapter.groupIds.has(nodeId)) {
            const next = new Set(urlFoldedSet)
            next.add(nodeId)
            setSearchPatch({
              folded: next.size > 0 ? [...next].join(',') : undefined,
            })
          }
          return
        }
        case 'fold-to-level': {
          // Folds to one level deeper than the clicked node's own depth.
          // Best-effort: step the global chip up by one if there's room.
          stepDepth(-1)
          return
        }
        case 'expand-all-under': {
          if (adapter.groupIds.has(nodeId)) {
            const next = new Set(urlFoldedSet)
            next.delete(nodeId)
            // Also remove any descendants of nodeId that were manually
            // folded — best-effort using the live job graph.
            const byId = new Map(adapter.jobs.map((j) => [j.id, j]))
            const stack = [nodeId]
            const seen = new Set<string>()
            while (stack.length > 0) {
              const id = stack.pop()!
              if (seen.has(id)) continue
              seen.add(id)
              const j = byId.get(id)
              if (!j) continue
              for (const c of j.childIds ?? []) {
                next.delete(c)
                stack.push(c)
              }
            }
            setSearchPatch({
              folded: next.size > 0 ? [...next].join(',') : undefined,
            })
          }
          return
        }
        case 'open-new-tab': {
          // Same prefix-strip + encode logic as handleNodeDoubleClick —
          // jobs.Store.GetJob keys by bare jobName, so the
          // "<deploymentId>:install-X" form returns 404. On contabo the
          // browser is at /sovereign/<path>; window.open of an absolute
          // path /provision/... lands at the same origin so the basepath
          // is preserved automatically by the browser (it's the click
          // target href, not a router navigate).
          const bare = nodeId.includes(':') ? nodeId.slice(nodeId.indexOf(':') + 1) : nodeId
          const encoded = encodeURIComponent(bare)
          const target =
            deploymentId && DETECTED_MODE.mode !== 'sovereign'
              ? `/sovereign/provision/${deploymentId}/jobs/${encoded}`
              : `/jobs/${encoded}`
          if (typeof window !== 'undefined') {
            window.open(target, '_blank', 'noopener,noreferrer')
          }
          return
        }
      }
    },
    [adapter.groupIds, adapter.jobs, urlFoldedSet, setSearchPatch, stepDepth, deploymentId],
  )

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

  /* ── Render ──────────────────────────────────────────────────── */

  const hasGroups = adapter.groupIds.size > 0
  const depthLabel = depth === 'all' ? `L${maxDepth || 0}/${maxDepth || 0}` : `L${depth}/${maxDepth || depth}`
  const canvas = (
    <FlowCanvasOrganic
      layout={layout}
      openJobId={openJobId}
      hostJobId={hostJobId}
      embedded={embedded}
      onJobClick={(jobId) => handleNodeClick(jobId)}
      onJobDoubleClick={(jobId) => handleNodeDoubleClick(jobId)}
      onCanvasBackgroundClick={handleCanvasBackgroundClick}
      onFoldToggle={hasGroups ? toggleFold : undefined}
      badgeCounts={badgeCounts}
      nodeActions={hasGroups ? flowActions : undefined}
      onNodeAction={hasGroups ? handleNodeAction : undefined}
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

  const depthChip = hasGroups ? (
    <div
      className="flow-depth-chip"
      data-testid="flow-depth-chip"
      role="toolbar"
      aria-label="Canvas depth"
    >
      <button
        type="button"
        className="flow-depth-chip-btn"
        data-testid="flow-depth-chip-prev"
        aria-label="Decrease depth"
        title="Fold one level deeper"
        onClick={() => stepDepth(-1)}
      >
        ◀
      </button>
      <span className="flow-depth-chip-label" data-testid="flow-depth-chip-label">
        {depthLabel}
      </span>
      <button
        type="button"
        className="flow-depth-chip-btn"
        data-testid="flow-depth-chip-next"
        aria-label="Increase depth"
        title="Unfold one more level"
        onClick={() => stepDepth(1)}
      >
        ▶
      </button>
    </div>
  ) : null

  const flowSurface = (
    <div
      className={`flow-surface${canvasFullScreen ? ' is-fullscreen' : ''}`}
      data-testid="flow-surface"
      data-canvas-fullscreen={canvasFullScreen ? 'true' : 'false'}
    >
      <div className="flow-canvas-host" data-testid="flow-canvas-host">
        {canvas}
        {depthChip}
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

/* Depth chip — top-right gutter, just left of the full-screen
   toggle. Tucks above the canvas SVG; never moves the bubble
   layout. */
.flow-depth-chip {
  position: absolute;
  top: 12px;
  right: 52px;
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 2px 8px;
  border-radius: 999px;
  background: rgba(7, 10, 18, 0.78);
  border: 1px solid var(--color-border);
  color: var(--color-text-dim);
  font-family: var(--font-mono, ui-monospace, monospace);
  font-size: 11px;
  z-index: 50;
}
[data-theme="light"] .flow-depth-chip {
  background: rgba(241, 245, 249, 0.95);
  color: var(--color-text-dim);
}
.flow-depth-chip-btn {
  appearance: none;
  background: transparent;
  border: 0;
  padding: 2px 4px;
  color: var(--color-text-dim);
  cursor: pointer;
  font: inherit;
  border-radius: 4px;
  transition: color 0.12s ease, background-color 0.12s ease;
}
.flow-depth-chip-btn:hover {
  color: var(--color-text);
  background: rgba(148, 163, 184, 0.12);
}
.flow-depth-chip-label {
  min-width: 56px;
  text-align: center;
  font-variant-numeric: tabular-nums;
}
`
