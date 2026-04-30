/**
 * useJobDetail — per-job fetch hook for the JobDetail page's log
 * viewer.
 *
 * WHY (issue #305):
 * The JobDetail page used to construct a synthetic execution id of the
 * form `${jobId}:latest` and pass it to <ExecutionLogs>. That synthetic
 * id is NEVER an actual execution id — real ones are 16-byte hex strings
 * allocated by the catalyst-api Bridge — so every log fetch returned 404
 * and the viewer rendered "Failed to load log page" / "No logs captured
 * for this log".
 *
 * The backend already exposes the data the viewer needs:
 *   GET /api/v1/deployments/{depId}/jobs/{jobId}
 *     → { job: Job, executions: Execution[] }
 *
 * `executions[]` is sorted started-at DESC server-side, so
 * `executions[0]?.id` is the real id of the most-recent attempt. This
 * hook polls that endpoint while the deployment is in flight, returning
 * the (job, executions, latest exec id) triple the JobDetail page wires
 * into <ExecutionLogs>.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), the URL is
 * built from `API_BASE`. Per #2 (never compromise) this is NOT a shim:
 * it follows the same React Query pattern as useLiveJobsBackfill.
 */

import { useQuery } from '@tanstack/react-query'
import { API_BASE } from '@/shared/config/urls'
import type { Job } from '@/lib/jobs.types'

/** Wire shape of an Execution row — kept local to this module since
 *  the JobsTable doesn't render Executions and the rest of the UI has
 *  no need for the type yet. Mirrors `internal/jobs.Execution` in the
 *  backend. */
export interface Execution {
  id: string
  jobId: string
  deploymentId: string
  status: 'running' | 'succeeded' | 'failed'
  startedAt: string
  finishedAt?: string | null
  lineCount: number
}

/** Wire shape of GET /api/v1/deployments/{id}/jobs/{jobId}. */
interface JobDetailResponse {
  job?: Job
  executions?: Execution[]
}

export interface UseJobDetailResult {
  job: Job | null
  executions: Execution[]
  /** id of `executions[0]` when present, else null. Stable for use as
   *  the executionId prop on <ExecutionLogs>. */
  latestExecutionId: string | null
  isLoading: boolean
  isError: boolean
  /** Distinguishes 404 (job genuinely doesn't exist on this deployment)
   *  from network/5xx errors. The JobDetail page renders the dedicated
   *  "Job not found" panel when notFound is true. */
  notFound: boolean
}

export interface UseJobDetailOptions {
  deploymentId: string
  jobId: string
  /** Test seam — disables the React Query refetch interval. */
  disablePolling?: boolean
  /** Test seam — inject a stub fetcher. */
  fetcher?: (deploymentId: string, jobId: string) => Promise<JobDetailResponse>
  /** When false the query is parked — used after the deployment reaches
   *  a terminal state to avoid wasteful polling. */
  enabled?: boolean
}

/** Founder-aligned poll cadence — matches useLiveJobsBackfill. */
const POLL_INTERVAL_MS = 5_000

/** Sentinel returned by the default fetcher on a 404. The hook
 *  translates this into `notFound: true` without surfacing it as an
 *  error to the operator. */
class JobNotFoundError extends Error {
  readonly deploymentId: string
  readonly jobId: string
  constructor(deploymentId: string, jobId: string) {
    super(`Job ${jobId} not found in deployment ${deploymentId}`)
    this.name = 'JobNotFoundError'
    this.deploymentId = deploymentId
    this.jobId = jobId
  }
}

async function defaultFetchJobDetail(
  deploymentId: string,
  jobId: string,
): Promise<JobDetailResponse> {
  // jobId is the canonical "<deploymentId>:<jobName>" string. The colon
  // is RFC 3986 path-safe, but encodeURIComponent turns it into %3A,
  // which chi's path matcher does NOT decode before route lookup —
  // every detail fetch returns 404 with the encoded form. Insert the
  // jobId raw; deploymentId is a 16-byte hex with no special chars so
  // encoding it is a no-op.
  const url =
    `${API_BASE}/v1/deployments/${encodeURIComponent(deploymentId)}` +
    `/jobs/${jobId}`
  const res = await fetch(url, { headers: { Accept: 'application/json' } })
  if (res.status === 404) {
    throw new JobNotFoundError(deploymentId, jobId)
  }
  if (!res.ok) {
    throw new Error(`Failed to fetch job detail: ${res.status}`)
  }
  return (await res.json()) as JobDetailResponse
}

export function useJobDetail(opts: UseJobDetailOptions): UseJobDetailResult {
  const {
    deploymentId,
    jobId,
    disablePolling = false,
    fetcher = defaultFetchJobDetail,
    enabled = true,
  } = opts

  const query = useQuery<JobDetailResponse, Error>({
    queryKey: ['job-detail', deploymentId, jobId],
    queryFn: () => fetcher(deploymentId, jobId),
    enabled: enabled && !!deploymentId && !!jobId,
    refetchInterval: () => {
      if (disablePolling) return false
      return POLL_INTERVAL_MS
    },
    placeholderData: (prev) => prev,
    retry: false,
  })

  const notFound = query.error instanceof JobNotFoundError
  const data = query.data
  const job = data?.job ?? null
  const executions = Array.isArray(data?.executions) ? data!.executions! : []
  // executions[] is sorted started-at DESC server-side. Take index 0
  // for the most-recent attempt.
  const latestExecutionId = executions[0]?.id ?? null

  return {
    job,
    executions,
    latestExecutionId,
    isLoading: query.isLoading,
    isError: !!query.error && !notFound,
    notFound,
  }
}
