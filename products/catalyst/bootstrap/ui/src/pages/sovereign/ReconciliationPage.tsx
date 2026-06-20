/**
 * ReconciliationPage — the Convergence-Monitor Reconciliation surface
 * (#3925 surface B), served at:
 *   • /sovereign/provision/$deploymentId/reconciliation   (mothership)
 *   • /reconciliation                                      (chroot console)
 *
 * The permanent, LIVING reconciler view — the convergence spine. Two tabs
 * over ONE dataset (operator design 2026-06-20):
 *   • DAG  — a real node-edge dependency GRAPH (force-free topological
 *            layout via the shared depsLayout, the same engine the Job
 *            dependencies graph uses), coloured by live reconcile state.
 *            Filtered to **Flux by default** because Flux Kustomizations/
 *            HelmReleases are the layer with real spec.dependsOn edges; the
 *            edgeless controller classes only render as standalone nodes
 *            when you explicitly widen the filter.
 *   • List — the SAME reconcilers as a scannable status table (all classes).
 *
 * Vocabulary (NOT Success/Failed — those imply a finite end):
 *   Reconciled · Reconciling · Drifted · Degraded · Suspended
 * A node goes Degraded ONLY when Flux reports Stalled; every other
 * Ready=False is Reconciling, which holds a spinner — never a red Failed.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), the data URL is
 * built from API_BASE inside reconciliation.api.ts, and every colour is a
 * named state token, not an inline hex literal scattered through JSX.
 */

import { useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'
import { depsLayout, type LayoutInput } from '@/shared/lib/depsLayout'
import { PortalShell } from './PortalShell'
import {
  fetchReconciliationDAG,
  type ReconState,
  type ReconciliationDAG,
  type ReconciliationNode,
} from '@/lib/reconciliation.api'

/** Live poll cadence — a reconcile loop is continuous; a few seconds keeps
 *  the DAG fresh through convergence without hammering the API. */
const RECON_POLL_MS = 4_000

/** Per-state visual tokens. The glyph + colour are the only state signal;
 *  the STRING is the vocabulary word verbatim (the FE never renders
 *  Success/Failed). */
const STATE_TONE: Record<ReconState, { glyph: string; fg: string; bg: string; border: string; pulse: boolean }> = {
  Reconciled: { glyph: '◇', fg: '#4ADE80', bg: 'rgba(74,222,128,0.10)', border: 'rgba(74,222,128,0.30)', pulse: false },
  Reconciling: { glyph: '↻', fg: '#38BDF8', bg: 'rgba(56,189,248,0.10)', border: 'rgba(56,189,248,0.30)', pulse: true },
  Drifted: { glyph: '⤳', fg: '#FBBF24', bg: 'rgba(251,191,36,0.10)', border: 'rgba(251,191,36,0.30)', pulse: false },
  Degraded: { glyph: '⚠', fg: '#F87171', bg: 'rgba(248,113,113,0.10)', border: 'rgba(248,113,113,0.30)', pulse: false },
  Suspended: { glyph: '⏸', fg: 'var(--color-text-dim)', bg: 'rgba(148,163,184,0.10)', border: 'rgba(148,163,184,0.30)', pulse: false },
}

/** The default DAG filter — Flux is the only layer with real dependsOn
 *  edges, so it's the graph that actually reads as a chain. */
const FLUX_CLASS = 'Flux'
const ALL_CLASS = 'All'

/** A node's filter CLASS. Flux = HelmRelease|Kustomization (the edge-bearing
 *  layer); every other kind is its own class (Crossplane, cert-manager, …). */
function nodeClass(kind: string): string {
  return kind === 'HelmRelease' || kind === 'Kustomization' ? FLUX_CLASS : kind
}

type ReconView = 'dag' | 'list'

export interface ReconciliationPageProps {
  /** Test seam — synthetic DAG bypassing the live fetch. */
  dataOverride?: ReconciliationDAG
  /** Test seam — disable the network poll (jsdom). */
  disablePoll?: boolean
}

export function ReconciliationPage({ dataOverride, disablePoll = false }: ReconciliationPageProps = {}) {
  const { deploymentId: resolved } = useResolvedDeploymentId()
  const deploymentId = resolved ?? ''

  const query = useQuery<ReconciliationDAG>({
    queryKey: ['reconciliation-dag', deploymentId],
    queryFn: () => fetchReconciliationDAG(deploymentId),
    enabled: !dataOverride && !disablePoll && !!deploymentId,
    refetchInterval: RECON_POLL_MS,
    staleTime: RECON_POLL_MS,
    placeholderData: (prev) => prev,
  })

  const dag = dataOverride ?? query.data ?? null

  const [view, setView] = useState<ReconView>('dag')
  const [filter, setFilter] = useState<string>(FLUX_CLASS)

  // The filter options present in the data: All + Flux + any other classes.
  const filterClasses = useMemo(() => {
    const present = new Set<string>()
    for (const n of dag?.nodes ?? []) present.add(nodeClass(n.kind))
    const ordered = [ALL_CLASS, FLUX_CLASS, ...[...present].filter((c) => c !== FLUX_CLASS).sort()]
    // de-dup while preserving order
    return [...new Set(ordered)]
  }, [dag])

  // DAG nodes after the class filter (default Flux). List shows ALL.
  const dagNodes = useMemo(() => {
    const all = dag?.nodes ?? []
    if (filter === ALL_CLASS) return all
    return all.filter((n) => nodeClass(n.kind) === filter)
  }, [dag, filter])

  const hasNodes = !!dag && dag.nodes.length > 0

  return (
    <PortalShell deploymentId={deploymentId} pageTitle="Reconciliation">
      <div className="mx-auto max-w-5xl" data-testid="reconciliation-page">
        <style>{RECON_CSS}</style>

        {/* N/M-Reconciled header */}
        <header className="mb-3 flex flex-wrap items-center justify-between gap-3">
          <div>
            <h2 className="text-lg font-semibold text-[var(--color-text-strong)]">
              Reconciliation spine
            </h2>
            <p className="mt-0.5 text-xs text-[var(--color-text-dim)]">
              The bounded declared set of continuous reconcilers. This view
              never finishes — it holds desired state.
            </p>
          </div>
          {dag ? (
            <div
              data-testid="reconciliation-header-count"
              className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)] px-4 py-2 text-right"
            >
              <div className="font-mono text-xl font-semibold text-[var(--color-text-strong)]">
                {dag.reconciled}/{dag.total}
              </div>
              <div className="text-[11px] uppercase tracking-wide text-[var(--color-text-dim)]">
                Reconciled
              </div>
            </div>
          ) : null}
        </header>

        {/* Tabs + (DAG-only) class filter */}
        {hasNodes ? (
          <div className="mb-3 flex flex-wrap items-center gap-2" data-testid="reconciliation-tabs">
            <div className="inline-flex overflow-hidden rounded-lg border border-[var(--color-border)]">
              {(['dag', 'list'] as ReconView[]).map((v) => (
                <button
                  key={v}
                  type="button"
                  data-testid={`reconciliation-tab-${v}`}
                  aria-pressed={view === v}
                  onClick={() => setView(v)}
                  className={
                    'px-3 py-1.5 text-xs font-semibold capitalize transition ' +
                    (view === v
                      ? 'bg-[var(--color-bg-3)] text-[var(--color-text-strong)]'
                      : 'bg-transparent text-[var(--color-text-dim)] hover:text-[var(--color-text)]')
                  }
                >
                  {v === 'dag' ? 'DAG' : 'List'}
                </button>
              ))}
            </div>
            {view === 'dag' ? (
              <label className="ml-1 flex items-center gap-1.5 text-[11px] text-[var(--color-text-dim)]">
                Filter
                <select
                  data-testid="reconciliation-dag-filter"
                  value={filter}
                  onChange={(e) => setFilter(e.target.value)}
                  className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] px-2 py-1 text-xs text-[var(--color-text)]"
                >
                  {filterClasses.map((c) => (
                    <option key={c} value={c}>
                      {c}
                    </option>
                  ))}
                </select>
              </label>
            ) : null}
          </div>
        ) : null}

        {query.isLoading && !dag ? (
          <div
            className="flex h-64 items-center justify-center text-sm text-[var(--color-text-dim)]"
            data-testid="reconciliation-loading"
          >
            Loading the reconciliation spine…
          </div>
        ) : null}

        {query.isError && !dag ? (
          <div
            className="rounded-md border border-[color:rgba(239,68,68,0.4)] bg-[color:rgba(239,68,68,0.08)] p-3 text-sm text-[#fca5a5]"
            data-testid="reconciliation-error"
          >
            Could not load the reconciliation spine. Retrying…
          </div>
        ) : null}

        {dag && dag.nodes.length === 0 && !query.isLoading ? (
          <div
            className="flex h-64 flex-col items-center justify-center gap-1 text-center text-sm text-[var(--color-text-dim)]"
            data-testid="reconciliation-empty"
          >
            <p className="font-medium text-[var(--color-text)]">No reconcilers observed yet.</p>
            <p>Once Flux begins reconciling the desired graph, the spine renders here.</p>
          </div>
        ) : null}

        {hasNodes ? (
          view === 'dag' ? (
            <ReconciliationGraph nodes={dagNodes} filter={filter} />
          ) : (
            <ReconciliationList nodes={dag!.nodes} />
          )
        ) : null}

        {/* Not-yet-tracked footnote (ticket §2 surface-A). */}
        {dag && dag.notYetTracked && dag.notYetTracked.length > 0 ? (
          <p
            className="mt-6 text-[11px] text-[var(--color-text-dimmer)]"
            data-testid="reconciliation-not-tracked"
          >
            Not yet tracked here (deferred continuous reconcilers):{' '}
            {dag.notYetTracked.join(' · ')}.
          </p>
        ) : null}
      </div>
    </PortalShell>
  )
}

/* ── DAG tab — the real node-edge graph ──────────────────────────────── */

const NODE_W = 184
const NODE_H = 52

/**
 * ReconciliationGraph renders the dependency DAG as a topological-layered
 * SVG graph — nodes coloured by reconcile state, edges = real Flux
 * dependsOn drawn as orthogonal step polylines with an arrow head. The
 * layout comes from the shared depsLayout engine (the same one the Job
 * dependencies graph uses) — deterministic, no graph library, SSR-safe.
 */
function ReconciliationGraph({ nodes, filter }: { nodes: ReconciliationNode[]; filter: string }) {
  const visibleIds = useMemo(() => new Set(nodes.map((n) => n.id)), [nodes])
  const layout = useMemo(() => {
    const inputs: LayoutInput[] = nodes.map((n) => ({
      id: n.id,
      // Drop dangling deps (to a node filtered out) so we never draw an
      // edge to a node that isn't on screen.
      dependsOn: (n.dependsOn ?? []).filter((d) => visibleIds.has(d)),
    }))
    return depsLayout(inputs, { nodeWidth: NODE_W, nodeHeight: NODE_H })
  }, [nodes, visibleIds])

  const byId = useMemo(() => {
    const m = new Map<string, ReconciliationNode>()
    for (const n of nodes) m.set(n.id, n)
    return m
  }, [nodes])

  if (nodes.length === 0) {
    return (
      <div
        data-testid="reconciliation-dag-empty"
        className="flex h-64 flex-col items-center justify-center gap-1 rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-2)] text-center text-sm text-[var(--color-text-dim)]"
      >
        <p className="font-medium text-[var(--color-text)]">
          No reconcilers match the “{filter}” filter.
        </p>
        <p>Widen the filter to All to see the other reconciler classes.</p>
      </div>
    )
  }

  return (
    <div
      data-testid="reconciliation-dag"
      className="relative w-full overflow-auto rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-2)]"
      style={{ maxHeight: 560 }}
    >
      <svg
        data-testid="reconciliation-dag-svg"
        width={layout.width}
        height={layout.height}
        viewBox={`0 0 ${layout.width} ${layout.height}`}
        role="img"
        aria-label="Reconciler dependency graph"
        style={{ display: 'block', minWidth: '100%' }}
      >
        <defs>
          <marker
            id="recon-dag-arrow"
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

        {/* Edges first so they sit beneath the nodes. */}
        <g data-testid="reconciliation-dag-edges">
          {layout.edges.map((e) => (
            <polyline
              key={`${e.from}->${e.to}`}
              data-testid={`reconciliation-dag-edge-${e.from}-${e.to}`}
              points={e.points.map((p) => `${p.x},${p.y}`).join(' ')}
              fill="none"
              stroke="var(--color-border-strong)"
              strokeWidth={1.5}
              markerEnd="url(#recon-dag-arrow)"
            />
          ))}
        </g>

        {/* Nodes. */}
        <g data-testid="reconciliation-dag-nodes">
          {layout.nodes.map((ln) => {
            const node = byId.get(ln.id)
            if (!node) return null
            const tone = STATE_TONE[node.state]
            return (
              <g
                key={ln.id}
                data-testid={`reconciliation-dag-node-${ln.id}`}
                data-node-id={ln.id}
                data-state={node.state}
                data-kind={node.kind}
                transform={`translate(${ln.x}, ${ln.y})`}
              >
                <rect
                  width={NODE_W}
                  height={NODE_H}
                  rx={10}
                  ry={10}
                  fill="var(--color-bg)"
                  stroke={tone.border}
                  strokeWidth={1.5}
                />
                {/* State pip. */}
                <circle cx={15} cy={NODE_H / 2} r={6} fill={tone.fg} className={tone.pulse ? 'recon-pulse' : undefined} />
                {/* Label. */}
                <text
                  x={30}
                  y={NODE_H / 2 - 5}
                  fill="var(--color-text-strong)"
                  fontSize={12}
                  fontWeight={600}
                  dominantBaseline="middle"
                  style={{ pointerEvents: 'none' }}
                >
                  {truncate(node.label, 22)}
                </text>
                {/* kind · state. */}
                <text
                  x={30}
                  y={NODE_H / 2 + 11}
                  fill="var(--color-text-dim)"
                  fontSize={10}
                  fontWeight={400}
                  dominantBaseline="middle"
                  style={{ pointerEvents: 'none' }}
                >
                  {node.kind} · {node.state}
                </text>
              </g>
            )
          })}
        </g>
      </svg>

      {/* Stats overlay. */}
      <div
        data-testid="reconciliation-dag-stats"
        className="pointer-events-none absolute bottom-2 left-2 flex gap-2"
      >
        <span className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg)]/80 px-2 py-0.5 text-[11px] text-[var(--color-text-dim)]">
          {layout.nodes.length} nodes
        </span>
        <span className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg)]/80 px-2 py-0.5 text-[11px] text-[var(--color-text-dim)]">
          {layout.edges.length} edges
        </span>
      </div>
    </div>
  )
}

/* ── List tab — the scannable status table (all classes) ─────────────── */

function ReconciliationList({ nodes }: { nodes: ReconciliationNode[] }) {
  const kustomizations = nodes.filter((n) => n.kind === 'Kustomization')
  const helmReleases = nodes.filter((n) => n.kind === 'HelmRelease')
  const others = nodes.filter((n) => n.kind !== 'Kustomization' && n.kind !== 'HelmRelease')
  return (
    <div data-testid="reconciliation-list" className="flex flex-col gap-5">
      {kustomizations.length > 0 ? <NodeGroup title="Kustomization tiers" nodes={kustomizations} /> : null}
      {helmReleases.length > 0 ? <NodeGroup title="HelmReleases" nodes={helmReleases} /> : null}
      {others.length > 0 ? <NodeGroup title="Other reconcilers" nodes={others} /> : null}
    </div>
  )
}

function NodeGroup({ title, nodes }: { title: string; nodes: ReconciliationNode[] }) {
  return (
    <section data-testid={`reconciliation-group-${title.split(' ')[0]?.toLowerCase()}`}>
      <h3 className="mb-2 text-xs font-semibold uppercase tracking-wide text-[var(--color-text-dim)]">
        {title}
      </h3>
      <ul className="flex flex-col gap-1.5">
        {nodes.map((n) => (
          <ReconciliationNodeRow key={n.id} node={n} />
        ))}
      </ul>
    </section>
  )
}

function ReconciliationNodeRow({ node }: { node: ReconciliationNode }) {
  const tone = STATE_TONE[node.state]
  return (
    <li
      data-testid="reconciliation-node"
      data-node-id={node.id}
      data-state={node.state}
      data-kind={node.kind}
      className="flex flex-wrap items-center gap-3 rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)] px-3 py-2"
    >
      <span
        data-testid="reconciliation-node-badge"
        className="inline-flex items-center gap-1.5 rounded-full border px-2 py-0.5 text-[11px] font-bold uppercase tracking-wide"
        style={{ background: tone.bg, borderColor: tone.border, color: tone.fg }}
      >
        <span className={tone.pulse ? 'recon-pulse' : undefined} aria-hidden>
          {tone.glyph}
        </span>
        {node.state}
      </span>
      <span className="font-mono text-sm text-[var(--color-text-strong)]">{node.label}</span>
      <span className="text-[11px] uppercase tracking-wide text-[var(--color-text-dimmer)]">{node.kind}</span>
      {node.dependsOn && node.dependsOn.length > 0 ? (
        <span
          data-testid="reconciliation-node-deps"
          className="ml-auto text-[11px] text-[var(--color-text-dim)]"
        >
          depends on{' '}
          <span className="font-mono text-[var(--color-text)]">
            {node.dependsOn.join(', ')}
          </span>
        </span>
      ) : null}
    </li>
  )
}

function truncate(s: string, max: number): string {
  if (s.length <= max) return s
  return s.slice(0, Math.max(0, max - 1)) + '…'
}

const RECON_CSS = `
.recon-pulse { animation: recon-pulse 2s ease-in-out infinite; transform-box: fill-box; transform-origin: center; }
@keyframes recon-pulse {
  0%, 100% { opacity: 1; transform: scale(1); }
  50%      { opacity: 0.4; transform: scale(0.7); }
}
`
