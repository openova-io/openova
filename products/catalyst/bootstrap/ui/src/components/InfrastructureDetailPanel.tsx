/**
 * InfrastructureDetailPanel — right-side slide-in panel for the
 * Sovereign Infrastructure Topology canvas.
 *
 * Sections:
 *   • Properties — per-node provider data (read-only)
 *   • Status    — current healthy/degraded/failed badge + last update
 *   • Actions   — opens the appropriate CRUD modal for this node kind
 *
 * Per founder spec: "Click node → graph zooms in (NOT accordion).
 * Right-side detail panel slides in." This panel is the secondary UI
 * surface on top of the topology canvas — the canvas itself handles
 * the zoom transition.
 */

import type { ReactNode } from 'react'
import type { LayoutNode } from '@/lib/topologyLayout'
import type { TopologyStatus } from '@/lib/infrastructure.types'

const STATUS_COLOR: Record<TopologyStatus, string> = {
  healthy: 'var(--color-success)',
  degraded: 'var(--color-warn)',
  failed: 'var(--color-danger)',
  unknown: 'var(--color-text-dim)',
}

export interface DetailAction {
  key: string
  label: string
  onClick: () => void
  /** Optional dangerous flag — renders the action in danger-red. */
  danger?: boolean
}

export interface InfrastructureDetailPanelProps {
  node: LayoutNode | null
  onClose: () => void
  /** Called when the operator clicks the "Add child" button. The
   *  topology page wires this to the appropriate CRUD modal. */
  actions?: DetailAction[]
}

export function InfrastructureDetailPanel({
  node,
  onClose,
  actions = [],
}: InfrastructureDetailPanelProps) {
  if (!node) return null

  const properties = collectProperties(node)
  const lastUpdate = readLastUpdate(node)

  return (
    <aside
      role="dialog"
      aria-label={`${node.label} details`}
      data-testid="infrastructure-detail-panel"
      className="fixed right-0 top-14 z-30 flex h-[calc(100vh-3.5rem)] w-96 flex-col gap-3 border-l border-[var(--color-border)] bg-[var(--color-bg-2)] p-4 shadow-xl"
    >
      <header className="flex items-start justify-between gap-2">
        <div className="min-w-0">
          <p
            className="truncate text-base font-semibold text-[var(--color-text-strong)]"
            data-testid="infrastructure-detail-panel-name"
          >
            {node.label}
          </p>
          <p className="text-xs uppercase tracking-wide text-[var(--color-text-dim)]">
            {node.kind} · depth {node.depth}
          </p>
        </div>
        <button
          type="button"
          onClick={onClose}
          data-testid="infrastructure-detail-panel-close"
          className="rounded-md border border-[var(--color-border)] bg-transparent px-2 py-1 text-xs text-[var(--color-text-dim)] hover:text-[var(--color-text)]"
          aria-label="Close detail panel"
        >
          ×
        </button>
      </header>

      <Section title="Status" testId="infrastructure-detail-panel-status">
        <div className="flex items-center justify-between rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-3 py-2 text-xs">
          <span
            data-testid="infrastructure-detail-panel-status-pill"
            style={{
              color: STATUS_COLOR[node.status],
              fontWeight: 600,
              textTransform: 'uppercase',
              letterSpacing: '0.04em',
            }}
          >
            {node.status}
          </span>
          {lastUpdate && (
            <span className="text-[var(--color-text-dim)]">{lastUpdate}</span>
          )}
        </div>
      </Section>

      <Section title="Properties" testId="infrastructure-detail-panel-properties">
        {properties.length === 0 ? (
          <p className="text-xs text-[var(--color-text-dim)]">
            No additional properties for this node.
          </p>
        ) : (
          <dl className="grid grid-cols-3 gap-x-2 gap-y-1.5 text-xs">
            {properties.map(([k, v]) => (
              <div key={k} className="contents">
                <dt className="col-span-1 truncate text-[var(--color-text-dim)]">
                  {k}
                </dt>
                <dd
                  className="col-span-2 truncate font-mono text-[var(--color-text)]"
                  data-testid={`infrastructure-detail-panel-prop-${k}`}
                >
                  {v}
                </dd>
              </div>
            ))}
          </dl>
        )}
      </Section>

      <Section title="Actions" testId="infrastructure-detail-panel-actions">
        {actions.length === 0 ? (
          <p className="text-xs text-[var(--color-text-dim)]">
            No actions available for this node yet.
          </p>
        ) : (
          <div className="flex flex-col gap-1.5">
            {actions.map((a) => (
              <button
                key={a.key}
                type="button"
                onClick={a.onClick}
                data-testid={`infrastructure-detail-panel-action-${a.key}`}
                className={`rounded-md border px-3 py-1.5 text-left text-xs font-medium transition-colors ${
                  a.danger
                    ? 'border-[color-mix(in_srgb,var(--color-danger)_50%,var(--color-border))] text-[var(--color-danger)] hover:bg-[color-mix(in_srgb,var(--color-danger)_8%,transparent)]'
                    : 'border-[var(--color-border)] text-[var(--color-text)] hover:bg-[var(--color-bg)]'
                }`}
              >
                {a.label}
              </button>
            ))}
          </div>
        )}
      </Section>
    </aside>
  )
}

function Section({
  title,
  testId,
  children,
}: {
  title: string
  testId: string
  children: ReactNode
}) {
  return (
    <section data-testid={testId} className="flex flex-col gap-1.5">
      <h3 className="text-[0.7rem] font-semibold uppercase tracking-[0.08em] text-[var(--color-text-dim)]">
        {title}
      </h3>
      {children}
    </section>
  )
}

function collectProperties(node: LayoutNode): [string, string][] {
  const out: [string, string][] = []
  switch (node.ref.kind) {
    case 'cloud': {
      const c = node.ref.data
      out.push(['provider', c.provider])
      out.push(['regions', String(c.regionCount)])
      out.push(['quota', `${c.quotaUsed} / ${c.quotaLimit}`])
      break
    }
    case 'region': {
      const r = node.ref.data
      out.push(['provider', r.provider])
      out.push(['region', r.providerRegion])
      out.push(['cp sku', r.skuCp])
      out.push(['worker sku', r.skuWorker])
      out.push(['workers', String(r.workerCount)])
      out.push(['clusters', String(r.clusters.length)])
      break
    }
    case 'cluster': {
      const c = node.ref.data
      out.push(['version', c.version])
      out.push(['nodes', String(c.nodeCount)])
      out.push(['vclusters', String(c.vclusters.length)])
      out.push(['lbs', String(c.loadBalancers.length)])
      out.push(['pools', String(c.nodePools.length)])
      break
    }
    case 'vcluster': {
      const v = node.ref.data
      out.push(['isolation', v.isolationMode])
      break
    }
  }
  return out
}

function readLastUpdate(_node: LayoutNode): string | null {
  return null
}
