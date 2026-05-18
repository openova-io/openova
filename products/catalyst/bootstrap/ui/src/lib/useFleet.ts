/**
 * useFleet — TanStack Query hook backed by the catalyst-api fleet
 * endpoints (EPIC-6 Slice U-Fleet, #1101).
 *
 * Mirrors the slice U `useComplianceStream` pattern (REST baseline, no
 * Zustand — TanStack Query owns the cache) and slice I `useCatalog` —
 * one hook per endpoint, stable query keys, conservative staleTime so
 * the dashboard isn't constantly re-fetching while the operator
 * scrolls through Sovereign cards.
 *
 * Three hooks shipped here cover the three brief endpoints:
 *
 *   useFleet({ page?, pageSize? })             → list Sovereigns
 *   useFleetSovereignSummary(id)               → per-Sov rollup
 *   useFleetApplications(filters)              → cross-Sov apps table
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #4 (never hardcode) — every URL flows through fleet.api.ts → API_BASE.
 *   #5 (least privilege) — server-side gate is the source of truth;
 *      hooks render whatever the server returns (filtered by tier).
 */

import { useQuery, type UseQueryResult } from '@tanstack/react-query'

import {
  listSovereigns,
  getSovereignSummary,
  listApplications,
  getFleetTreemap,
  type SovereignsResponse,
  type SovereignDetail,
  type ApplicationsResponse,
  type FleetApplicationsFilters,
  type FleetTreemapResponse,
  type FleetTreemapSizeBy,
  type FleetTreemapColorBy,
} from './fleet.api'

/** Stable query keys so TanStack devtools + invalidation are coherent. */
export const fleetQueryKeys = {
  all: ['fleet'] as const,
  sovereigns: (page: number, pageSize: number) =>
    ['fleet', 'sovereigns', page, pageSize] as const,
  sovereignSummary: (id: string) => ['fleet', 'sovereigns', 'summary', id] as const,
  applications: (filters: FleetApplicationsFilters) =>
    ['fleet', 'applications', filters.org ?? '', filters.topology ?? '', filters.drPosture ?? ''] as const,
  treemap: (sizeBy: string, colorBy: string) =>
    ['fleet', 'treemap', sizeBy, colorBy] as const,
}

/**
 * Cache TTL — the fleet endpoints fan out to per-Sovereign K8s reads
 * with a per-Sov 4s timeout, so a single fetch is cheap but not free.
 * 60s mirrors useCatalog and gives enough room for the dashboard to
 * feel snappy without becoming stale (auto-refetch on window-focus is
 * the normal trigger).
 */
const FLEET_STALE_MS = 60_000

/* ── useFleet — Sovereign list ─────────────────────────────────────── */

export interface UseFleetOptions {
  page?: number
  pageSize?: number
  /** Disable the query (e.g. while tier resolution is pending). */
  enabled?: boolean
}

export function useFleet(opts: UseFleetOptions = {}): UseQueryResult<SovereignsResponse> {
  const page = opts.page ?? 1
  const pageSize = opts.pageSize ?? 25
  return useQuery<SovereignsResponse>({
    queryKey: fleetQueryKeys.sovereigns(page, pageSize),
    queryFn: ({ signal }) => listSovereigns(page, pageSize, signal),
    enabled: opts.enabled !== false,
    staleTime: FLEET_STALE_MS,
  })
}

/* ── useFleetSovereignSummary — per-Sov rollup ────────────────────── */

export function useFleetSovereignSummary(
  id: string,
  enabled = true,
): UseQueryResult<SovereignDetail> {
  return useQuery<SovereignDetail>({
    queryKey: fleetQueryKeys.sovereignSummary(id),
    queryFn: ({ signal }) => getSovereignSummary(id, signal),
    enabled: enabled && !!id,
    staleTime: FLEET_STALE_MS,
  })
}

/* ── useFleetApplications — cross-Sov table ───────────────────────── */

export function useFleetApplications(
  filters: FleetApplicationsFilters = {},
  enabled = true,
): UseQueryResult<ApplicationsResponse> {
  return useQuery<ApplicationsResponse>({
    queryKey: fleetQueryKeys.applications(filters),
    queryFn: ({ signal }) => listApplications(filters, signal),
    enabled,
    staleTime: FLEET_STALE_MS,
  })
}

/* ── useFleetTreemap — single-layer fleet treemap (TBD-E14) ─────────
 *
 * One cell per Sovereign across the fleet. Backend skeleton at
 * /api/v1/fleet/treemap; deeper layers (Sov → Cluster → Application)
 * land in a follow-up slice that proxies each Sov's
 * /dashboard/treemap and unions the children.
 */

export interface UseFleetTreemapOptions {
  sizeBy?: FleetTreemapSizeBy
  colorBy?: FleetTreemapColorBy
  enabled?: boolean
}

export function useFleetTreemap(
  opts: UseFleetTreemapOptions = {},
): UseQueryResult<FleetTreemapResponse> {
  const sizeBy = opts.sizeBy ?? 'apps'
  const colorBy = opts.colorBy ?? 'health'
  return useQuery<FleetTreemapResponse>({
    queryKey: fleetQueryKeys.treemap(sizeBy, colorBy),
    queryFn: ({ signal }) => getFleetTreemap({ sizeBy, colorBy }, signal),
    enabled: opts.enabled !== false,
    staleTime: FLEET_STALE_MS,
  })
}
