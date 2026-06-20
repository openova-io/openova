/**
 * ReconciliationPage — the Convergence-Monitor Reconciliation surface
 * (#3925 surface B), served at:
 *   • /sovereign/provision/$deploymentId/reconciliation   (mothership)
 *   • /reconciliation                                      (chroot console)
 *
 * The permanent, LIVING reconciler view — the convergence spine. Two tabs
 * over ONE dataset (operator design 2026-06-20):
 *   • DAG  — the SHARED force-directed bubble graph (GraphCanvas, the same
 *            widget the Architecture view uses — NOT a bespoke renderer),
 *            nodes coloured by live reconcile STATE via its nodeColorFn,
 *            edges = real Flux spec.dependsOn ('depends-on' relation).
 *            Filtered to **Flux by default** (the only layer with real
 *            dependsOn edges); widen to All / a class for the edgeless
 *            non-Flux reconcilers.
 *   • List — the SAME reconcilers (ALL classes, Flux + non-Flux) as a
 *            scientific filterable/sortable table (ReconciliationTable), on
 *            par with the Jobs view.
 *
 * BOTH views render every reconciler the backend emits — the Flux
 * HelmReleases + Kustomizations AND the declarative non-Flux reconcilers
 * (cert-manager / CNPG / External-Secrets / the Catalyst control-plane CRs).
 *
 * Vocabulary (NOT Success/Failed — those imply a finite end):
 *   Reconciled · Reconciling · Drifted · Degraded · Suspended
 */

import { useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'
import { GraphCanvas } from '@/widgets/architecture-graph/GraphCanvas'
import type { ArchNodeType, ArchStatus, GraphEdge, GraphNode } from '@/widgets/architecture-graph/types'
import { PortalShell } from './PortalShell'
import { ReconciliationTable } from './ReconciliationTable'
import { STATE_TONE, FLUX_CLASS, ALL_CLASS, nodeClass } from './reconciliation.shared'
import {
  fetchReconciliationDAG,
  type ReconState,
  type ReconciliationDAG,
  type ReconciliationNode,
} from '@/lib/reconciliation.api'

/** Live poll cadence — a reconcile loop is continuous; a few seconds keeps
 *  the DAG fresh through convergence without hammering the API. */
const RECON_POLL_MS = 4_000

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

  const filterClasses = useMemo(() => {
    const present = new Set<string>()
    for (const n of dag?.nodes ?? []) present.add(nodeClass(n.kind))
    const ordered = [ALL_CLASS, FLUX_CLASS, ...[...present].filter((c) => c !== FLUX_CLASS).sort()]
    return [...new Set(ordered)]
  }, [dag])

  const dagNodes = useMemo(() => {
    const all = dag?.nodes ?? []
    if (filter === ALL_CLASS) return all
    return all.filter((n) => nodeClass(n.kind) === filter)
  }, [dag, filter])

  const hasNodes = !!dag && dag.nodes.length > 0

  return (
    <PortalShell deploymentId={deploymentId} pageTitle="Reconciliation">
      <div className="mx-auto max-w-5xl" data-testid="reconciliation-page">
        <header className="mb-3 flex flex-wrap items-center justify-between gap-3">
          <div>
            <h2 className="text-lg font-semibold text-[var(--color-text-strong)]">Reconciliation spine</h2>
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
              <div className="text-[11px] uppercase tracking-wide text-[var(--color-text-dim)]">Reconciled</div>
            </div>
          ) : null}
        </header>

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
                    'px-3 py-1.5 text-xs font-semibold transition ' +
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
            <ReconciliationTable nodes={dag!.nodes} />
          )
        ) : null}

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

/* ── DAG tab — the SHARED force-directed bubble graph (GraphCanvas) ───── */

// Reconciler kind → an ArchNodeType (drives the bubble's icon); the ring
// colour is overridden per-node by reconcile STATE via nodeColorFn. Known
// kinds get a sensible glyph; any other kind falls back to 'Service'.
const KIND_ARCHTYPE: Record<string, ArchNodeType> = {
  HelmRelease: 'Service',
  Kustomization: 'Deployment',
  Certificate: 'Ingress',
  Cluster: 'Cluster',
  ExternalSecret: 'Namespace',
  Application: 'Service',
  Environment: 'vCluster',
  Organization: 'Region',
  Continuum: 'Cloud',
}
const STATE_ARCHSTATUS: Record<ReconState, ArchStatus> = {
  Reconciled: 'healthy',
  Reconciling: 'unknown',
  Drifted: 'degraded',
  Degraded: 'failed',
  Suspended: 'unknown',
}

function ReconciliationGraph({ nodes, filter }: { nodes: ReconciliationNode[]; filter: string }) {
  const byId = useMemo(() => {
    const m = new Map<string, ReconciliationNode>()
    for (const n of nodes) m.set(n.id, n)
    return m
  }, [nodes])

  const graphNodes = useMemo<GraphNode[]>(
    () =>
      nodes.map((n) => ({
        id: n.id,
        type: KIND_ARCHTYPE[n.kind] ?? 'Service',
        label: n.label,
        sublabel: `${n.kind} · ${n.state}`,
        status: STATE_ARCHSTATUS[n.state],
      })),
    [nodes],
  )

  const graphEdges = useMemo<GraphEdge[]>(() => {
    const ids = new Set(nodes.map((n) => n.id))
    const out: GraphEdge[] = []
    for (const n of nodes) {
      for (const dep of n.dependsOn ?? []) {
        if (ids.has(dep)) {
          out.push({ id: `${dep}->${n.id}`, source: dep, target: n.id, type: 'depends-on' })
        }
      }
    }
    return out
  }, [nodes])

  const colorByState = useMemo(
    () =>
      (g: GraphNode): string | undefined => {
        const rn = byId.get(g.id)
        return rn ? STATE_TONE[rn.state].fg : undefined
      },
    [byId],
  )

  if (nodes.length === 0) {
    return (
      <div
        data-testid="reconciliation-dag-empty"
        className="flex h-64 flex-col items-center justify-center gap-1 rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-2)] text-center text-sm text-[var(--color-text-dim)]"
      >
        <p className="font-medium text-[var(--color-text)]">No reconcilers match the “{filter}” filter.</p>
        <p>Widen the filter to All to see the other reconciler classes.</p>
      </div>
    )
  }

  return (
    <div data-testid="reconciliation-dag" className="relative w-full" style={{ height: 560 }}>
      <GraphCanvas
        nodes={graphNodes}
        edges={graphEdges}
        nodeColorFn={colorByState}
        testIdPrefix="reconciliation-dag"
      />
    </div>
  )
}
