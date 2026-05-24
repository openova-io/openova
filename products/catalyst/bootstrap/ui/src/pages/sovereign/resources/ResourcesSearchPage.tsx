/**
 * ResourcesSearchPage — full target-state search surface (qa-loop
 * iter-12 Fix #50). Replaces the iter-6 stub at
 * `pages/sovereign/stubs/ResourcesSearchPage.tsx` with a TanStack-Query
 * client of `/api/v1/sovereigns/{id}/k8s/search?q=`.
 *
 * URL: /app/$deploymentId/resources/search?q=<query>
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #1 (waterfall)        — full result set + group-by-kind + drill-in
 *                            links land on first cut.
 *   #2 (quality)          — no "(pending live data)" placeholder.
 *   #3 (event-driven)     — TanStack Query polling + debounced input.
 *   #4 (never hardcode)   — kind plurals derive from resources.api.ts.
 *
 * Per `feedback_per_issue_playwright_verification.md` the page surfaces
 * matrix-asserted tokens TC-266: "Pods", "Deployments", "Services" and
 * resolves with non-empty rows when q=qa-wp.
 */

import { useEffect, useMemo, useState } from 'react'
import { useParams, useSearch, Link } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'

import { PortalShell } from '../PortalShell'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'
import { DETECTED_MODE } from '@/shared/lib/detectMode'
import { searchK8s, pluralKind, type K8sSearchHit } from './resources.api'

export interface ResourcesSearchSearch {
  q?: string
  kinds?: string
}

const KIND_GROUPS = ['pod', 'deployment', 'service', 'configmap', 'secret', 'ingress'] as const

function groupLabel(reg: string): string {
  switch (reg) {
    case 'pod':
      return 'Pods'
    case 'deployment':
      return 'Deployments'
    case 'service':
      return 'Services'
    case 'configmap':
      return 'ConfigMaps'
    case 'secret':
      return 'Secrets'
    case 'ingress':
      return 'Ingresses'
    default:
      return reg.charAt(0).toUpperCase() + reg.slice(1) + 's'
  }
}

export function ResourcesSearchPage() {
  const params = useParams({ strict: false }) as { deploymentId?: string }
  const { deploymentId: resolvedId } = useResolvedDeploymentId()
  const deploymentId = params.deploymentId ?? resolvedId ?? ''
  const search = useSearch({ strict: false }) as ResourcesSearchSearch
  const initialQ = search.q ?? ''

  const [draft, setDraft] = useState(initialQ)
  const [committed, setCommitted] = useState(initialQ)

  // Debounce keystrokes — fire the query 250ms after typing stops so the
  // SAR-gated server isn't hammered on every char.
  useEffect(() => {
    const t = setTimeout(() => setCommitted(draft.trim()), 250)
    return () => clearTimeout(t)
  }, [draft])

  const basePath =
    DETECTED_MODE.mode === 'sovereign' || !deploymentId
      ? '/app'
      : `/app/${deploymentId}`

  const query = useQuery({
    queryKey: ['k8s-search', deploymentId, committed],
    queryFn: ({ signal }) => searchK8s(deploymentId, committed, undefined, signal),
    enabled: !!deploymentId && committed.length >= 1,
    staleTime: 10_000,
    refetchInterval: 30_000,
  })

  const grouped = useMemo(() => {
    const out: Record<string, K8sSearchHit[]> = {}
    for (const k of KIND_GROUPS) out[k] = []
    for (const hit of query.data?.items ?? []) {
      const k = hit.kind.toLowerCase()
      if (!out[k]) out[k] = []
      out[k].push(hit)
    }
    return out
  }, [query.data])

  const detailHrefFor = (hit: K8sSearchHit): string => {
    const ns = hit.namespace || '_'
    return `${basePath}/resources/${pluralKind(hit.kind)}/${encodeURIComponent(ns)}/${encodeURIComponent(hit.name)}`
  }

  return (
    <PortalShell deploymentId={deploymentId} pageTitle="Resources">
      <div className="p-6 space-y-4" data-testid="resources-search-page">
        <div>
          <h2 className="text-xl font-semibold text-[var(--color-text)]">Search</h2>
          <p className="text-sm text-[oklch(55%_0.01_250)]">
            Cross-kind search across the catalyst-cache for{' '}
            <code>{deploymentId}</code>.
          </p>
        </div>

        <div className="flex items-center gap-3">
          <input
            className="flex-1 rounded border border-[var(--color-border,#1f2937)] bg-[var(--color-bg-2,#0f172a)] px-3 py-2 text-sm text-[var(--color-text,#e2e8f0)] placeholder:text-[var(--color-text-dim,#94a3b8)]"
            placeholder="Search Pods, Deployments, Services, ConfigMaps, Secrets, Ingresses…"
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            data-testid="resources-search-input"
            autoFocus
          />
          <span className="text-xs text-[var(--color-text-dim,#94a3b8)]">
            {query.isFetching ? 'searching…' : `${query.data?.total ?? 0} hits`}
          </span>
        </div>

        {query.isError && (
          <div
            className="rounded border border-red-700/40 bg-red-900/20 p-3 text-sm text-red-200"
            data-testid="resources-search-error"
            role="alert"
          >
            Search failed: {(query.error as Error)?.message ?? 'unknown error'}
          </div>
        )}

        {!committed ? (
          <div
            className="rounded border border-[var(--color-border,#1f2937)] bg-[var(--color-bg-2,#0f172a)] p-6 text-sm text-[var(--color-text-dim,#94a3b8)]"
            data-testid="resources-search-empty-input"
          >
            Start typing to search across Pods, Deployments, Services, ConfigMaps,
            Secrets and Ingresses. Results refresh every 30s.
          </div>
        ) : query.isLoading ? (
          <div
            className="rounded border border-[var(--color-border,#1f2937)] bg-[var(--color-bg-2,#0f172a)] p-6 text-sm text-[var(--color-text-dim,#94a3b8)]"
            data-testid="resources-search-loading"
          >
            Searching for <code>{committed}</code>…
          </div>
        ) : (query.data?.total ?? 0) === 0 ? (
          <div
            className="rounded border border-[var(--color-border,#1f2937)] bg-[var(--color-bg-2,#0f172a)] p-6 text-sm text-[var(--color-text-dim,#94a3b8)]"
            data-testid="resources-search-empty-results"
          >
            No matches for <code>{committed}</code>.
          </div>
        ) : (
          <div className="space-y-4" data-testid="resources-search-results">
            {KIND_GROUPS.map((reg) => {
              const hits = grouped[reg] ?? []
              if (hits.length === 0) return null
              return (
                <section key={reg} data-testid={`resources-search-group-${reg}`}>
                  <h3 className="mb-2 text-sm font-semibold text-[var(--color-text,#e2e8f0)]">
                    {groupLabel(reg)} ({hits.length})
                  </h3>
                  <ul className="rounded border border-[var(--color-border,#1f2937)] bg-[var(--color-bg-2,#0f172a)]">
                    {hits.map((hit, i) => (
                      <li
                        key={`${hit.kind}/${hit.namespace}/${hit.name}/${i}`}
                        className="border-b border-[var(--color-border,#1f2937)] last:border-0"
                      >
                        <Link
                          to={detailHrefFor(hit) as never}
                          className="block px-3 py-2 text-sm text-[var(--color-text,#e2e8f0)] hover:bg-[var(--color-bg-3,#1e293b)]"
                          data-testid={`resources-search-hit-${hit.kind}-${hit.namespace ?? '_'}-${hit.name}`}
                        >
                          <span className="font-mono">{hit.name}</span>
                          {hit.namespace && (
                            <span className="ml-2 text-xs text-[var(--color-text-dim,#94a3b8)]">
                              ns=<code>{hit.namespace}</code>
                            </span>
                          )}
                        </Link>
                      </li>
                    ))}
                  </ul>
                </section>
              )
            })}
          </div>
        )}
      </div>
    </PortalShell>
  )
}
