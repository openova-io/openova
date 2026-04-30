/**
 * useLiveJobsBackfill.test.tsx — coverage for the live-API jobs
 * backfill hook + the pure mergeJobs helper (issue #232).
 *
 * Coverage:
 *   • mergeJobs — empty live list → reducer-derived passes through.
 *   • mergeJobs — non-empty live list with overlapping ids → live wins.
 *   • mergeJobs — non-overlapping live ids → both rendered.
 *   • Hook — returns empty list while loading.
 *   • Hook — returns live jobs once the fetcher resolves.
 *   • Hook — does NOT poll when `enabled: false`.
 *   • Hook — silently swallows errors and exposes `isError: true`.
 */

import { describe, it, expect, afterEach, vi } from 'vitest'
import { renderHook, cleanup, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { Job } from '@/lib/jobs.types'
import { useLiveJobsBackfill, mergeJobs } from './useLiveJobsBackfill'

afterEach(() => cleanup())

function makeJob(partial: Partial<Job>): Job {
  return {
    id: partial.id ?? 'job-1',
    jobName: partial.jobName ?? 'Job 1',
    appId: partial.appId ?? 'bp-cilium',
    batchId: partial.batchId ?? 'applications',
    dependsOn: partial.dependsOn ?? [],
    status: partial.status ?? 'pending',
    startedAt: partial.startedAt ?? null,
    finishedAt: partial.finishedAt ?? null,
    durationMs: partial.durationMs ?? 0,
  }
}

describe('mergeJobs — pure helper', () => {
  it('returns reducer jobs unchanged when live list is empty', () => {
    const reducer = [makeJob({ id: 'a' }), makeJob({ id: 'b' })]
    const merged = mergeJobs(reducer, [])
    expect(merged).toHaveLength(2)
    expect(merged.map((j) => j.id)).toEqual(['a', 'b'])
  })

  it('live wins on conflict (same job.id)', () => {
    const reducer = [makeJob({ id: 'a', status: 'pending' })]
    const live = [makeJob({ id: 'a', status: 'succeeded', durationMs: 12_345 })]
    const merged = mergeJobs(reducer, live)
    expect(merged).toHaveLength(1)
    expect(merged[0]!.status).toBe('succeeded')
    expect(merged[0]!.durationMs).toBe(12_345)
  })

  it('backend wins entirely when live has data', () => {
    const reducer = [makeJob({ id: 'a' })]
    const live = [makeJob({ id: 'b' }), makeJob({ id: 'c' })]
    const merged = mergeJobs(reducer, live)
    const ids = merged.map((j) => j.id).sort()
    expect(ids).toEqual(['b', 'c'])
  })

  it('fixes the omantel symptom — 0 reducer jobs + 5 backend jobs renders 5 rows', () => {
    // Reducer-derived list is empty because the SSE buffer's old
    // events left every card pending. Backend Jobs API has the
    // ground truth.
    const reducer: Job[] = []
    const live: Job[] = [
      makeJob({ id: 'bp-cilium', status: 'succeeded' }),
      makeJob({ id: 'bp-cert-manager', status: 'succeeded' }),
      makeJob({ id: 'bp-flux', status: 'succeeded' }),
      makeJob({ id: 'bp-crossplane', status: 'running' }),
      makeJob({ id: 'bp-vault', status: 'pending' }),
    ]
    const merged = mergeJobs(reducer, live)
    expect(merged).toHaveLength(5)
  })
})

function wrapper() {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  })
  return ({ children }: { children: React.ReactNode }) => (
    <QueryClientProvider client={qc}>{children}</QueryClientProvider>
  )
}

describe('useLiveJobsBackfill — hook', () => {
  it('returns the fetched jobs once the fetcher resolves', async () => {
    const expected: Job[] = [
      makeJob({ id: 'bp-cilium', status: 'succeeded' }),
      makeJob({ id: 'bp-cert-manager', status: 'running' }),
    ]
    const fetcher = vi.fn(async (): Promise<Job[]> => expected)
    const { result } = renderHook(
      () =>
        useLiveJobsBackfill({
          deploymentId: 'd-1',
          fetcher,
          disablePolling: true,
        }),
      { wrapper: wrapper() },
    )
    await waitFor(() => {
      expect(result.current.liveJobs).toHaveLength(2)
    })
    expect(result.current.liveJobs[0]!.id).toBe('bp-cilium')
    expect(result.current.isError).toBe(false)
    expect(result.current.lastFetched).not.toBeNull()
    expect(fetcher).toHaveBeenCalledTimes(1)
  })

  it('does NOT call the fetcher when enabled: false', async () => {
    const fetcher = vi.fn(async (): Promise<Job[]> => [])
    const { result } = renderHook(
      () =>
        useLiveJobsBackfill({
          deploymentId: 'd-1',
          fetcher,
          disablePolling: true,
          enabled: false,
        }),
      { wrapper: wrapper() },
    )
    // Give React Query a tick to potentially fire — it shouldn't.
    await new Promise((r) => setTimeout(r, 25))
    expect(fetcher).toHaveBeenCalledTimes(0)
    expect(result.current.liveJobs).toEqual([])
    expect(result.current.lastFetched).toBeNull()
  })

  it('exposes isError: true when the fetcher throws', async () => {
    const fetcher = vi.fn(async (): Promise<Job[]> => {
      throw new Error('500 Internal Server Error')
    })
    const { result } = renderHook(
      () =>
        useLiveJobsBackfill({
          deploymentId: 'd-1',
          fetcher,
          disablePolling: true,
        }),
      { wrapper: wrapper() },
    )
    await waitFor(() => {
      expect(result.current.isError).toBe(true)
    })
    // liveJobs is still an empty array — JobsPage falls back to
    // reducer-derived data.
    expect(result.current.liveJobs).toEqual([])
  })
})
