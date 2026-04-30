/**
 * CloudPage — Sovereign-portal Cloud surface served at
 *   /sovereign/provision/$deploymentId/cloud/{architecture,compute,storage,network}
 *
 * Layout contract (issue #309):
 *   • Bare /cloud redirects to /cloud/architecture — the redirect is
 *     enforced by the router (see app/router.tsx); this component
 *     never renders standalone for the bare URL.
 *   • The shell renders a header (title + tagline + Sovereign
 *     switcher) and an <Outlet />. The sub-page navigation lives in
 *     the left sidebar as an accordion under "Cloud" — the previous
 *     in-page tab strip has been removed (see Sidebar.tsx).
 *   • The page owns ONE React Query for the hierarchical
 *     infrastructure tree. Sub-pages read from the shared query via
 *     `CloudContext` — no per-page fetches.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #1 (waterfall) — all four sub-pages ship at once.
 *   #4 (never hardcode) — every label / route is derived from the
 *      router's path constants; no inline "/cloud/foo" string outside
 *      the redirect target in the page-switcher.
 *
 * The data shape `HierarchicalInfrastructure` and the
 * `getHierarchicalInfrastructure` API call retain the legacy
 * "infrastructure" name because they are server-side contract
 * identifiers, not user-visible strings.
 */

import { createContext, useContext, useMemo, type ReactNode } from 'react'
import { Outlet, useNavigate, useParams, useRouterState } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { PortalShell } from './PortalShell'
import { useDeploymentEvents } from './useDeploymentEvents'
import {
  getHierarchicalInfrastructure,
  listDeployments,
  type CloudSpec,
  type DeploymentSummary,
  type HierarchicalInfrastructure,
  type TopologyTree,
} from '@/lib/infrastructure.types'
import { infrastructureTopologyFixture } from '@/test/fixtures/infrastructure-topology.fixture'

/**
 * Synthesise a `cloud` block from the regions list when the backend
 * doesn't return one. Every distinct provider becomes a single
 * cloud-tenant anchor.
 */
function inferCloudFromTopology(topology?: TopologyTree): CloudSpec[] {
  const regions = topology?.regions ?? []
  const byProvider = new Map<string, CloudSpec>()
  for (const r of regions) {
    const key = r.provider ?? 'unknown'
    const existing = byProvider.get(key)
    if (existing) {
      existing.regionCount += 1
    } else {
      byProvider.set(key, {
        id: `cloud-${key}`,
        name: key,
        provider: key,
        regionCount: 1,
        quotaUsed: 0,
        quotaLimit: 0,
      })
    }
  }
  return Array.from(byProvider.values())
}

/* ── Shared infrastructure query context ───────────────────────── */

export interface CloudContextValue {
  deploymentId: string
  data: HierarchicalInfrastructure | null
  isLoading: boolean
  isError: boolean
  refetch: () => void
}

const CloudContext = createContext<CloudContextValue | null>(null)

export function useCloud(): CloudContextValue {
  const ctx = useContext(CloudContext)
  if (!ctx) {
    throw new Error('useCloud must be used inside a CloudPage subtree')
  }
  return ctx
}

const STALE_MS = 30_000

/* ── Page shell ────────────────────────────────────────────────── */

export interface CloudPageProps {
  /** Test seam — disables the live SSE EventSource attach. */
  disableStream?: boolean
  /**
   * Test seam — render a content slot directly instead of using
   * <Outlet />. Allows component tests to mount the shell without
   * requiring a full TanStack-Router child tree.
   */
  contentOverride?: ReactNode
  /**
   * Test seam — bypass the React Query fetcher with synthetic data.
   * The data flows through CloudContext to children so every sub-page
   * sees the same response.
   */
  initialDataOverride?: HierarchicalInfrastructure
  /** Test seam — bypass the deployments-list fetch. */
  deploymentsOverride?: DeploymentSummary[]
}

export function CloudPage({
  disableStream = false,
  contentOverride,
  initialDataOverride,
  deploymentsOverride,
}: CloudPageProps = {}) {
  // tanstack-router resolves the matched route's params at runtime;
  // both the new `/cloud` parent and the legacy `/infrastructure`
  // parent expose the same `deploymentId` param, and the strict:false
  // option lets us share this component across both during the
  // rename window.
  const params = useParams({ strict: false }) as { deploymentId: string }
  const deploymentId = params.deploymentId
  const navigate = useNavigate()

  const pathname = useRouterState({ select: (s) => s.location.pathname })

  const { snapshot } = useDeploymentEvents({
    deploymentId,
    applicationIds: [],
    disableStream,
  })
  const sovereignFQDN = snapshot?.sovereignFQDN ?? snapshot?.result?.sovereignFQDN ?? null

  // Single hierarchical-topology fetch — every sub-page reads off this.
  const topologyQuery = useQuery<HierarchicalInfrastructure>({
    queryKey: ['infra-hierarchical', deploymentId],
    queryFn: () => getHierarchicalInfrastructure(deploymentId),
    staleTime: STALE_MS,
    enabled: !initialDataOverride,
    // The fixture serves as the local-dev fallback when the live
    // backend isn't deployed — the UI never fails closed in that
    // case. Per founder spec, the fixture-backed path is the
    // explicit pre-backend mode and must serve every sub-page off
    // the same shape.
    retry: 1,
  })

  // Deployments list — feeds the per-Sovereign header switcher.
  const deploymentsQuery = useQuery<DeploymentSummary[]>({
    queryKey: ['deployments-list'],
    queryFn: listDeployments,
    staleTime: 60_000,
    enabled: !deploymentsOverride,
    retry: 1,
  })

  const data = useMemo<HierarchicalInfrastructure | null>(() => {
    const raw =
      initialDataOverride ??
      topologyQuery.data ??
      (topologyQuery.isError ? infrastructureTopologyFixture : null)
    if (!raw) return null
    // Backend-tolerant normalisation — every collection field defaults
    // to an empty array so consumers can iterate freely. The current
    // backend ships a flat-ish response without `cloud` / `storage`
    // arrays; we synthesise them here so the topology tree always
    // has the expected shape.
    return {
      cloud: raw.cloud ?? inferCloudFromTopology(raw.topology),
      topology: {
        pattern: raw.topology?.pattern ?? 'solo',
        regions: (raw.topology?.regions ?? []).map((r) => ({
          ...r,
          clusters: (r.clusters ?? []).map((c) => ({
            ...c,
            vclusters: c.vclusters ?? [],
            loadBalancers: (c.loadBalancers ?? []).map((lb) => ({
              ...lb,
              // Older backend serialises `ports` as a CSV string; the
              // new shape uses an array of {port, protocol}. Coerce
              // either to the canonical array form.
              listeners:
                lb.listeners ??
                (typeof (lb as unknown as { ports?: string }).ports === 'string'
                  ? ((lb as unknown as { ports: string }).ports || '')
                      .split(',')
                      .map((p) => p.trim())
                      .filter(Boolean)
                      .map((p) => ({ port: parseInt(p, 10), protocol: 'tcp' }))
                  : []),
              targets: lb.targets ?? [],
            })),
            nodePools: c.nodePools ?? [],
            nodes: c.nodes ?? [],
          })),
          networks: (r.networks ?? []).map((n) => ({
            ...n,
            peerings: n.peerings ?? [],
            firewalls: n.firewalls ?? [],
          })),
        })),
      },
      storage: {
        pvcs: raw.storage?.pvcs ?? [],
        buckets: raw.storage?.buckets ?? [],
        volumes: raw.storage?.volumes ?? [],
      },
    }
  }, [initialDataOverride, topologyQuery.data, topologyQuery.isError])

  const ctx: CloudContextValue = useMemo(
    () => ({
      deploymentId,
      data,
      isLoading: !initialDataOverride && topologyQuery.isLoading && !data,
      isError: topologyQuery.isError && !initialDataOverride,
      refetch: () => topologyQuery.refetch(),
    }),
    [deploymentId, data, initialDataOverride, topologyQuery],
  )

  const deployments = deploymentsOverride ?? deploymentsQuery.data ?? []
  const switcherOptions: DeploymentSummary[] = useMemo(() => {
    const list = [...deployments]
    if (!list.find((d) => d.id === deploymentId)) {
      list.unshift({
        id: deploymentId,
        sovereignFQDN: sovereignFQDN ?? deploymentId,
        status: 'unknown',
      })
    }
    return list
  }, [deployments, deploymentId, sovereignFQDN])

  function handleSwitch(nextId: string) {
    if (nextId === deploymentId) return
    // Preserve the current sub-page when switching Sovereigns: if the
    // operator is on /cloud/compute, keep them on compute under the
    // new deployment. Falls back to /architecture otherwise.
    const suffixMatch = pathname.match(/\/(architecture|compute|storage|network|topology)$/)
    const suffix =
      suffixMatch && suffixMatch[1] !== 'topology' ? suffixMatch[1] : 'architecture'
    navigate({
      to: `/provision/$deploymentId/cloud/${suffix}` as never,
      params: { deploymentId: nextId } as never,
    })
  }

  return (
    <PortalShell deploymentId={deploymentId} sovereignFQDN={sovereignFQDN}>
      <style>{CLOUD_PAGE_CSS}</style>

      <div data-testid="cloud-page" className="mx-auto max-w-7xl">
        <header className="mb-3 flex items-start justify-between gap-4">
          <div>
            <h1
              className="text-2xl font-bold text-[var(--color-text-strong)]"
              data-testid="cloud-title"
            >
              Cloud
            </h1>
            <p className="mt-1 text-sm text-[var(--color-text-dim)]">
              Sovereign cloud — regions, clusters, and resources for{' '}
              {sovereignFQDN ?? `deployment ${deploymentId.slice(0, 8)}`}.
            </p>
          </div>
          <div className="flex flex-col items-end gap-1.5">
            <select
              data-testid="cloud-sovereign-switcher"
              value={deploymentId}
              onChange={(e) => handleSwitch(e.target.value)}
              className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] px-2 py-1 text-xs text-[var(--color-text)]"
            >
              {switcherOptions.map((d) => (
                <option key={d.id} value={d.id}>
                  {d.sovereignFQDN || d.id.slice(0, 8)}
                </option>
              ))}
            </select>
            <div className="text-right text-xs text-[var(--color-text-dim)]">
              <div className="font-mono">{deploymentId.slice(0, 8)}</div>
            </div>
          </div>
        </header>

        <CloudContext.Provider value={ctx}>
          <div className="mt-4" data-testid="cloud-content">
            {contentOverride ?? <Outlet />}
          </div>
        </CloudContext.Provider>
      </div>
    </PortalShell>
  )
}

/**
 * Page-scoped CSS — shared layout primitives (.infra-grid /
 * .infra-section / .infra-card / .infra-empty / .infra-bulk-actions)
 * are still consumed by the four sub-pages. The legacy `.tabs` /
 * `.tab` rules were removed when the in-page tab strip was replaced
 * by the sidebar accordion.
 */
const CLOUD_PAGE_CSS = `
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

/* Bulk action bar shared by Compute / Storage / Network. */
.infra-bulk-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 0.4rem;
  margin: 0.75rem 0;
  padding: 0.5rem 0.75rem;
  border-radius: 10px;
  background: var(--color-bg-2);
  border: 1px solid var(--color-border);
  align-items: center;
}
.infra-bulk-actions .label {
  font-size: 0.75rem;
  color: var(--color-text-dim);
  margin-right: 0.4rem;
}
.infra-bulk-actions button {
  border: 1px solid var(--color-border);
  background: var(--color-bg);
  color: var(--color-text);
  border-radius: 6px;
  padding: 0.3rem 0.7rem;
  font-size: 0.78rem;
  cursor: pointer;
}
.infra-bulk-actions button:hover {
  background: var(--color-surface);
}
.infra-bulk-actions button.primary {
  background: var(--color-accent);
  border-color: var(--color-accent);
  color: #fff;
  font-weight: 600;
}
`
