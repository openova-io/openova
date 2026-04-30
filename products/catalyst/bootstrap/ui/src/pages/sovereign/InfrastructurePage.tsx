/**
 * InfrastructurePage — Sovereign-portal Infrastructure surface served at
 *   /sovereign/provision/$deploymentId/infrastructure/{topology,compute,storage,network}
 *
 * Founder spec (verbatim):
 *   "The infrastructure page must be opened by default with the
 *    topology page, the same topology that we showed during the wizard.
 *    We should have the tabs of compute (clusters and worker nodes),
 *    storage (pvcs, buckets etc) and network (lbs, drgs, peerings etc)."
 *
 * Layout contract:
 *   • Bare /infrastructure redirects to /infrastructure/topology — the
 *     redirect is enforced by the router (see app/router.tsx); this
 *     component never renders standalone for the bare URL.
 *   • The shell renders a header (title + tagline) and a `<nav role=tablist>`
 *     with four tabs in the canonical AppsPage style (.tabs/.tab/.tab-count)
 *     so the visual rhythm matches the rest of the Sovereign portal.
 *   • Active tab is derived from the current pathname — clicking a tab
 *     navigates via TanStack Router's <Link>; back/forward keeps the
 *     active tab in sync.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #1 (waterfall) — all four tabs ship at once, not "topology now,
 *      compute later".
 *   #2 (no compromise) — tabs are TABS (role=tablist + role=tab),
 *      never accordions.
 *   #4 (never hardcode) — every label / route is derived from the TABS
 *      table; no inline "/infrastructure/foo" string outside this table.
 */

import type { ReactNode } from 'react'
import { Link, Outlet, useParams, useRouterState } from '@tanstack/react-router'
import { PortalShell } from './PortalShell'
import { useDeploymentEvents } from './useDeploymentEvents'

/* ── Tab table — single source of truth ────────────────────────── */

export type InfraTabKey = 'topology' | 'compute' | 'storage' | 'network'

interface InfraTab {
  key: InfraTabKey
  label: string
  /** Pathname suffix appended to /provision/$deploymentId/infrastructure. */
  suffix: 'topology' | 'compute' | 'storage' | 'network'
}

export const INFRA_TABS: readonly InfraTab[] = [
  { key: 'topology', label: 'Topology', suffix: 'topology' },
  { key: 'compute',  label: 'Compute',  suffix: 'compute'  },
  { key: 'storage',  label: 'Storage',  suffix: 'storage'  },
  { key: 'network',  label: 'Network',  suffix: 'network'  },
] as const

/** Resolve the active tab from the current pathname. Defaults to
 *  topology when no suffix matches (covers the redirect-in-flight
 *  paint and any lossy URL the user pastes). */
export function resolveActiveTab(pathname: string): InfraTabKey {
  for (const t of INFRA_TABS) {
    if (pathname.endsWith(`/infrastructure/${t.suffix}`)) return t.key
  }
  return 'topology'
}

/* ── Page shell ────────────────────────────────────────────────── */

export interface InfrastructurePageProps {
  /** Test seam — disables the live SSE EventSource attach. */
  disableStream?: boolean
  /**
   * Test seam — render a content slot directly instead of using
   * <Outlet />. Allows AppsPage-style component tests to mount the
   * shell without requiring a full TanStack-Router child tree.
   */
  contentOverride?: ReactNode
}

export function InfrastructurePage({
  disableStream = false,
  contentOverride,
}: InfrastructurePageProps = {}) {
  const params = useParams({
    from: '/provision/$deploymentId/infrastructure' as never,
  }) as { deploymentId: string }
  const deploymentId = params.deploymentId

  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const activeTab = resolveActiveTab(pathname)

  const { snapshot } = useDeploymentEvents({
    deploymentId,
    applicationIds: [],
    disableStream,
  })
  const sovereignFQDN = snapshot?.sovereignFQDN ?? snapshot?.result?.sovereignFQDN ?? null

  return (
    <PortalShell deploymentId={deploymentId} sovereignFQDN={sovereignFQDN}>
      <style>{INFRA_PAGE_CSS}</style>

      <div data-testid="infrastructure-page" className="mx-auto max-w-7xl">
        <header className="mb-3 flex items-start justify-between gap-4">
          <div>
            <h1
              className="text-2xl font-bold text-[var(--color-text-strong)]"
              data-testid="infrastructure-title"
            >
              Infrastructure
            </h1>
            <p className="mt-1 text-sm text-[var(--color-text-dim)]">
              Sovereign topology, compute, storage and network — pulled live from
              the deployment&rsquo;s cluster.
            </p>
          </div>
          <div className="text-right text-xs text-[var(--color-text-dim)]">
            <div>{sovereignFQDN || `deployment ${deploymentId.slice(0, 8)}`}</div>
            <div className="font-mono">{deploymentId.slice(0, 8)}</div>
          </div>
        </header>

        <nav
          className="tabs"
          role="tablist"
          aria-label="Infrastructure sections"
          data-testid="infrastructure-tabs"
        >
          {INFRA_TABS.map((tab) => {
            const isActive = tab.key === activeTab
            return (
              <Link
                key={tab.key}
                to={`/provision/$deploymentId/infrastructure/${tab.suffix}` as never}
                params={{ deploymentId } as never}
                role="tab"
                aria-selected={isActive}
                aria-current={isActive ? 'page' : undefined}
                className={`tab${isActive ? ' active' : ''}`}
                data-testid={`infra-tab-${tab.key}`}
              >
                {tab.label}
              </Link>
            )
          })}
        </nav>

        <div className="mt-4" data-testid="infrastructure-content">
          {contentOverride ?? <Outlet />}
        </div>
      </div>
    </PortalShell>
  )
}

/**
 * Pixel-aligned tab CSS — same selectors and values AppsPage exports
 * for its tab strip. Duplicated here so InfrastructurePage doesn't
 * depend on AppsPage's `<style>` block being mounted (every page in
 * the Sovereign portal owns its own scoped CSS payload).
 */
const INFRA_PAGE_CSS = `
.tabs {
  display: flex;
  gap: 0.25rem;
  margin: 1rem 0 0.5rem;
  border-bottom: 1px solid var(--color-border);
}
.tab {
  background: transparent;
  border: none;
  padding: 0.6rem 0.9rem;
  color: var(--color-text-dim);
  font: inherit;
  font-size: 0.88rem;
  font-weight: 500;
  cursor: pointer;
  border-bottom: 2px solid transparent;
  margin-bottom: -1px;
  display: inline-flex;
  align-items: center;
  gap: 0.4rem;
  text-decoration: none;
}
.tab:hover { color: var(--color-text); }
.tab.active {
  color: var(--color-text-strong);
  border-bottom-color: var(--color-accent);
  font-weight: 600;
}

.infra-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(320px, 1fr));
  gap: 0.75rem;
}
.infra-section {
  margin-top: 1.25rem;
}
.infra-section h2 {
  font-size: 0.95rem;
  font-weight: 600;
  color: var(--color-text-strong);
  margin: 0 0 0.5rem 0;
  display: flex;
  align-items: center;
  gap: 0.5rem;
}
.infra-section h2 .count {
  font-size: 0.7rem;
  font-weight: 600;
  padding: 0.08rem 0.4rem;
  border-radius: 999px;
  background: color-mix(in srgb, var(--color-border) 60%, transparent);
  color: var(--color-text-dim);
}
.infra-card {
  background: var(--color-surface);
  border: 1.5px solid var(--color-border);
  border-radius: 12px;
  padding: 0.85rem;
  display: flex;
  flex-direction: column;
  gap: 0.4rem;
  position: relative;
  overflow: hidden;
}
.infra-card[data-status="healthy"]  { border-color: color-mix(in srgb, var(--color-success) 45%, var(--color-border)); }
.infra-card[data-status="degraded"] { border-color: color-mix(in srgb, var(--color-warn) 55%, var(--color-border)); }
.infra-card[data-status="failed"]   { border-color: color-mix(in srgb, var(--color-danger) 55%, var(--color-border)); }
.infra-card-head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 0.5rem;
}
.infra-card-name {
  font-size: 0.92rem;
  font-weight: 600;
  color: var(--color-text-strong);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.infra-card-kind {
  font-size: 0.65rem;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  color: var(--color-text-dim);
  background: color-mix(in srgb, var(--color-border) 50%, transparent);
  padding: 0.1rem 0.4rem;
  border-radius: 3px;
}
.infra-card-row {
  display: flex;
  justify-content: space-between;
  font-size: 0.78rem;
  color: var(--color-text-dim);
}
.infra-card-row .v {
  color: var(--color-text);
  font-family: var(--font-mono, ui-monospace, SFMono-Regular, Menlo, monospace);
}
.infra-card-status {
  position: absolute;
  top: 0.55rem;
  right: 0.6rem;
  font-size: 0.62rem;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.04em;
  padding: 0.12rem 0.45rem;
  border-radius: 999px;
}
.infra-card-status[data-status="healthy"]  { background: color-mix(in srgb, var(--color-success) 16%, transparent); color: var(--color-success); }
.infra-card-status[data-status="degraded"] { background: color-mix(in srgb, var(--color-warn) 16%, transparent);    color: var(--color-warn); }
.infra-card-status[data-status="failed"]   { background: color-mix(in srgb, var(--color-danger) 16%, transparent);  color: var(--color-danger); }
.infra-card-status[data-status="unknown"]  { background: color-mix(in srgb, var(--color-text-dim) 16%, transparent); color: var(--color-text-dim); }

.infra-empty {
  margin-top: 2rem;
  text-align: center;
  color: var(--color-text-dim);
  padding: 2rem 1rem;
  border: 1px dashed var(--color-border);
  border-radius: 12px;
  background: var(--color-bg-2);
}
.infra-empty .title {
  font-size: 0.95rem;
  color: var(--color-text-strong);
  font-weight: 600;
  margin: 0 0 0.3rem;
}
.infra-empty .sub {
  font-size: 0.82rem;
  margin: 0;
}
`
