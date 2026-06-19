/**
 * ReconciliationPage — the Convergence-Monitor Reconciliation surface
 * (#3925 surface B), served at:
 *   • /sovereign/provision/$deploymentId/reconciliation   (mothership)
 *   • /reconciliation                                      (chroot console)
 *
 * The permanent, LIVING Flux/GitOps dependency DAG — the convergence
 * spine. Nodes = the declared desired components (a bounded set:
 * HelmReleases + Kustomizations); edges = real Flux dependsOn; each
 * coloured by live reconcile state. It NEVER "finishes" — it holds desired
 * state. "Done provisioning" = the bounded set is all-Reconciled.
 *
 * Vocabulary (NOT Success/Failed — those imply a finite end):
 *   Reconciled · Reconciling · Drifted · Degraded · Suspended
 * A node goes Degraded ONLY when Flux reports Stalled; every other
 * Ready=False is Reconciling, which holds a spinner — never a red Failed.
 *
 * Scope — Flux-only to start (HelmReleases + Kustomizations). The deferred
 * continuous-reconciler classes are surfaced in the "not yet tracked"
 * footnote so the operator knows the scope.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), the data URL is
 * built from API_BASE inside reconciliation.api.ts, and every colour is a
 * named state token, not an inline hex literal scattered through JSX.
 */

import { useQuery } from '@tanstack/react-query'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'
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

  return (
    <PortalShell deploymentId={deploymentId} pageTitle="Reconciliation">
      <div className="mx-auto max-w-5xl" data-testid="reconciliation-page">
        <style>{RECON_CSS}</style>

        {/* N/M-Reconciled header */}
        <header className="mb-4 flex flex-wrap items-center justify-between gap-3">
          <div>
            <h2 className="text-lg font-semibold text-[var(--color-text-strong)]">
              Flux convergence spine
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

        {dag && dag.nodes.length > 0 ? (
          <ReconciliationDAGView dag={dag} />
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

/**
 * ReconciliationDAGView renders the bounded DAG. Nodes are grouped by kind
 * (Kustomization tier spine first, then HelmReleases); each row shows its
 * state badge + declared dependencies. The dependency arrows are rendered
 * as inline "depends on →" chips (a full force-directed layout is overkill
 * for the bounded set; the dependency text is the load-bearing signal the
 * operator reads — "where is it stuck and what's blocking it").
 */
function ReconciliationDAGView({ dag }: { dag: ReconciliationDAG }) {
  const kustomizations = dag.nodes.filter((n) => n.kind === 'Kustomization')
  const helmReleases = dag.nodes.filter((n) => n.kind === 'HelmRelease')
  return (
    <div data-testid="reconciliation-dag" className="flex flex-col gap-5">
      {kustomizations.length > 0 ? (
        <NodeGroup title="Kustomization tiers" nodes={kustomizations} />
      ) : null}
      {helmReleases.length > 0 ? (
        <NodeGroup title="HelmReleases" nodes={helmReleases} />
      ) : null}
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

const RECON_CSS = `
.recon-pulse { animation: recon-pulse 2s ease-in-out infinite; display: inline-block; }
@keyframes recon-pulse {
  0%, 100% { opacity: 1; transform: scale(1); }
  50%      { opacity: 0.4; transform: scale(0.7); }
}
`
