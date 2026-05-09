/**
 * useCatalog — TanStack Query hook backed by catalyst-catalog REST.
 *
 * EPIC-2 Slice I (#1097) — replaces the static stub
 * `pages/sovereign/applicationCatalog.ts` for the install flow. Mirrors
 * the slice U `useComplianceStream` pattern (REST baseline, no Zustand
 * — TanStack Query owns the cache).
 *
 * Three hooks shipped here cover the three contract endpoints:
 *
 *   useCatalog({ org? })             → GET /api/v1/catalog (list)
 *   useCatalogItem(name)             → GET /api/v1/catalog/{name}
 *   useCatalogVersions(name)         → GET /api/v1/catalog/{name}/versions
 *   useCatalogItemVersion(n, v)      → GET /api/v1/catalog/{name}/versions/{v}
 *
 * The version-pinned `useCatalogItemVersion` is the one the install
 * flow drives — it is the only endpoint that returns the full Blueprint
 * `raw` map so the auto-form generator can read `spec.configSchema`.
 *
 * Resolution order (per slice L's contract): PRIVATE > SOVEREIGN >
 * PUBLIC. The handler does the priority dedup; the hook just renders
 * whatever the server returns.
 */

import { useQuery, type UseQueryResult } from '@tanstack/react-query'

import {
  listCatalog,
  getCatalogItem,
  getCatalogVersions,
  getCatalogItemVersion,
  type CatalogItem,
  type CatalogVersionsResponse,
} from './catalog.api'

/** Stable query keys so TanStack devtools + invalidation are coherent. */
export const catalogQueryKeys = {
  all: ['catalog'] as const,
  list: (org?: string) => ['catalog', 'list', org ?? ''] as const,
  item: (name: string) => ['catalog', 'item', name] as const,
  itemVersion: (name: string, version: string) =>
    ['catalog', 'item', name, version] as const,
  versions: (name: string) => ['catalog', 'versions', name] as const,
}

/**
 * Cache TTL for catalog reads. The catalog upstream caches 30s on its
 * side; layering 60s on the browser keeps the install page snappy
 * without going stale relative to the server. A "Refresh" affordance on
 * the install page can call `queryClient.invalidateQueries` if the
 * operator just published a Blueprint and wants to see it now.
 */
const CATALOG_STALE_MS = 60_000

/** UseCatalogOptions — slim wrapper so consumers can pin the org. */
export interface UseCatalogOptions {
  org?: string
  /** Disable the query (e.g. when sovereign id isn't resolved yet). */
  enabled?: boolean
}

/** useCatalog — list every Blueprint visible to the caller. */
export function useCatalog(opts: UseCatalogOptions = {}): UseQueryResult<CatalogItem[]> {
  return useQuery<CatalogItem[]>({
    queryKey: catalogQueryKeys.list(opts.org),
    queryFn: async () => {
      const r = await listCatalog({ org: opts.org })
      return r.items
    },
    enabled: opts.enabled !== false,
    staleTime: CATALOG_STALE_MS,
  })
}

/** useCatalogItem — fetch a single Blueprint at its latest version. */
export function useCatalogItem(name: string, enabled = true): UseQueryResult<CatalogItem> {
  return useQuery<CatalogItem>({
    queryKey: catalogQueryKeys.item(name),
    queryFn: () => getCatalogItem(name),
    enabled: enabled && !!name,
    staleTime: CATALOG_STALE_MS,
  })
}

/** useCatalogVersions — version matrix for a Blueprint. */
export function useCatalogVersions(
  name: string,
  enabled = true,
): UseQueryResult<CatalogVersionsResponse> {
  return useQuery<CatalogVersionsResponse>({
    queryKey: catalogQueryKeys.versions(name),
    queryFn: () => getCatalogVersions(name),
    enabled: enabled && !!name,
    staleTime: CATALOG_STALE_MS,
  })
}

/**
 * useCatalogItemVersion — fetch the FULL Blueprint at a pinned version.
 *
 * This is the endpoint that returns `raw` (the full parsed Blueprint
 * manifest). The install flow uses it to drive the auto-form generator
 * via `raw.spec.configSchema`. Caller must pass both `name` and
 * `version`; the hook is disabled until both are non-empty.
 */
export function useCatalogItemVersion(
  name: string,
  version: string,
  enabled = true,
): UseQueryResult<CatalogItem> {
  return useQuery<CatalogItem>({
    queryKey: catalogQueryKeys.itemVersion(name, version),
    queryFn: () => getCatalogItemVersion(name, version),
    enabled: enabled && !!name && !!version,
    staleTime: CATALOG_STALE_MS,
  })
}
