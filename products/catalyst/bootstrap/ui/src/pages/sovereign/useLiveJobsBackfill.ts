/**
 * useLiveJobsBackfill — bridge from the catalyst-api Jobs endpoint into
 * the wizard's reducer-derived JobsTable.
 *
 * WHY (issue #232):
 * The wizard's Jobs view is fed by `deriveJobs(reducerState, applications)`,
 * which folds the SSE event stream into the flat Job[] shape. That works
 * while the SSE channel is healthy, but two situations leave the table
 * frozen on `pending`:
 *
 *   1. helmwatch only fires on TRANSITIONS — its initial-list events are
 *      suppressed in some code paths, so a HelmRelease that's already
 *      Ready=True at watch-attach time emits no event the reducer can
 *      see.
 *   2. The SSE replay buffer carries old events that contradict the
 *      live state. When the reducer folds them in, fresh per-component
 *      installations get masked by stale `state: skipped` markers.
 *
 * Backend (#205) owns the canonical Jobs endpoint:
 *   GET /api/v1/deployments/{depId}/jobs → { jobs: Job[] }
 *
 * This hook polls that endpoint every 5s while the deployment is in
 * flight. When it returns ≥1 jobs, the JobsPage merges them into the
 * reducer-derived list (dedupe by job.id; live data wins on conflict).
 * When it returns [] or the endpoint is unreachable, the reducer-derived
 * list passes through unchanged.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), the URL is
 * built from `API_BASE` (which itself derives from Vite's `BASE_URL`),
 * never inlined. Per #2 (never compromise), this is NOT an MVP shim —
 * it's the same React Query patterns used everywhere else in the UI.
 *
 * Returns:
 *   • liveJobs    — Job[] from the live API. Empty array when the
 *                   endpoint hasn't responded yet or returned no rows.
 *   • isLoading   — true on the first fetch only.
 *   • isError     — true on a fetch failure (4xx/5xx/network). The
 *                   JobsPage falls back to reducer-derived data; no
 *                   banner is shown to the operator.
 *   • lastFetched — wall-clock ms of the most recent successful fetch
 *                   (null until a fetch succeeds). Surfaced for the
 *                   "live state stream re-attached" banner.
 */

import { useQuery } from '@tanstack/react-query'
import { API_BASE } from '@/shared/config/urls'
import type { Job } from '@/lib/jobs.types'

/** Wire shape of GET /api/v1/deployments/{id}/jobs. */
interface JobsResponse {
  jobs?: Job[]
}

/** Hook return shape — exposed so JobsPage stays typed. */
export interface UseLiveJobsBackfillResult {
  liveJobs: Job[]
  isLoading: boolean
  isError: boolean
  lastFetched: number | null
}

/**
 * Default fetcher — exposed via parameter so tests can inject a stub
 * without monkey-patching `globalThis.fetch`.
 */
async function defaultFetchJobs(deploymentId: string): Promise<Job[]> {
  const url = `${API_BASE}/v1/deployments/${encodeURIComponent(deploymentId)}/jobs`
  const res = await fetch(url, { headers: { Accept: 'application/json' } })
  if (!res.ok) {
    throw new Error(`Failed to fetch jobs: ${res.status}`)
  }
  const body = (await res.json()) as JobsResponse
  return Array.isArray(body.jobs) ? body.jobs : []
}

export interface UseLiveJobsBackfillOptions {
  /** Stable deployment id from the URL parameter. */
  deploymentId: string
  /** Test seam — disables the React Query refetch interval. */
  disablePolling?: boolean
  /** Test seam — inject a stub fetcher. */
  fetcher?: (deploymentId: string) => Promise<Job[]>
  /**
   * If true, the hook does NOT mount its query — used when the
   * deployment has reached a terminal state and further polling is
   * wasteful. Caller passes `streamStatus !== 'completed' && streamStatus !== 'failed'`.
   */
  enabled?: boolean
}

/** Founder-specified poll cadence — verbatim ("every 5s while the
 * deployment is in flight"). */
const POLL_INTERVAL_MS = 5_000

export function useLiveJobsBackfill(
  opts: UseLiveJobsBackfillOptions,
): UseLiveJobsBackfillResult {
  const { deploymentId, disablePolling = false, fetcher = defaultFetchJobs, enabled = true } = opts

  const query = useQuery<Job[]>({
    queryKey: ['live-jobs-backfill', deploymentId],
    queryFn: () => fetcher(deploymentId),
    enabled: enabled && !!deploymentId,
    refetchInterval: () => {
      if (disablePolling) return false
      return POLL_INTERVAL_MS
    },
    // Keep last data while a new fetch is in flight — avoids the
    // table flickering through an empty array between polls.
    placeholderData: (prev) => prev,
    // Fail silently — the JobsPage falls back to reducer-derived data
    // when this query errors. No retry storm; one attempt per interval
    // is enough.
    retry: false,
  })

  const liveJobs = Array.isArray(query.data) ? query.data : []
  const lastFetched =
    query.dataUpdatedAt > 0 ? query.dataUpdatedAt : null

  return {
    liveJobs,
    isLoading: query.isLoading,
    isError: query.isError,
    lastFetched,
  }
}

/**
 * Pure helper — merges reducer-derived jobs with live-API jobs.
 * Live data wins on conflict (same job.id). Reducer-derived rows that
 * don't appear in the live list pass through unchanged.
 *
 * Stable: re-running on identical inputs produces identical output.
 * No randomness, no cached state. Exported so unit tests can lock in
 * the contract.
 */
export function mergeJobs(
  reducerJobs: readonly Job[],
  liveJobs: readonly Job[],
): Job[] {
  // Backend is authoritative once it has any data. Reducer-derived
  // jobs use catalog ids ("bp-cilium") that don't match the backend's
  // "<deploymentId>:install-cilium" canonical id, so dedup-by-id
  // produces duplicates. Reducer-derived rows are also missing the
  // appId/batchId shape the backend uses, and clicking them navigates
  // to a route the backend can't resolve (→ 404 → "Failed to load
  // log page"). Switch to backend-only the moment it returns ≥1 row;
  // reducer-derived stays as the empty-state fallback.
  if (liveJobs.length > 0) return [...liveJobs]
  return [...reducerJobs]
}
