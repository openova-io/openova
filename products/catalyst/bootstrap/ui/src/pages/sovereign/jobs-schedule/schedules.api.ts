/**
 * schedules.api.ts — typed client for the ad-hoc CronJob schedule endpoints
 * (epic #6703 P4b, "New schedule").
 *
 *   POST   /api/v1/sovereigns/{id}/schedules                — create
 *   PATCH  /api/v1/sovereigns/{id}/schedules/{ns}/{name}    — edit schedule / zone / suspend
 *   DELETE /api/v1/sovereigns/{id}/schedules/{ns}/{name}    — delete
 *
 * An ad-hoc schedule is a REAL batch/v1 CronJob the catalyst-api clones from
 * an existing CronJob's jobTemplate (products/catalyst/bootstrap/api/internal/
 * handler/schedules.go). The console never authors a pod spec. Only
 * console-origin schedules (label catalyst.openova.io/schedule-origin=console)
 * may be edited or deleted — a chart's CronJob changes in Git and the server
 * answers 409 not-console-schedule.
 *
 * Every URL derives from API_BASE (INVIOLABLE-PRINCIPLES #4).
 */

import { API_BASE } from '@/shared/config/urls'
import { authedFetch } from '@/shared/lib/authedFetch'

export interface ScheduleCreateRequest {
  sourceNamespace: string
  sourceName: string
  name: string
  schedule: string
  timeZone?: string
  suspend?: boolean
}

export interface ScheduleUpdateRequest {
  schedule?: string
  timeZone?: string
  suspend?: boolean
  clearTimeZone?: boolean
}

export interface ScheduleResponse {
  namespace: string
  name: string
  schedule: string
  timeZone?: string
  suspend: boolean
  origin: 'console' | 'chart'
  sourceNamespace?: string
  sourceName?: string
  resourceVersion?: string
  uid?: string
}

export interface ScheduleDeleteResponse {
  deleted: boolean
  namespace: string
  name: string
  schedule: string
}

/** Server-side failure, carrying the wire `error` code + `detail`. */
export class ScheduleApiError extends Error {
  readonly status: number
  readonly code: string
  constructor(status: number, code: string, detail: string) {
    super(detail || code || `HTTP ${status}`)
    this.name = 'ScheduleApiError'
    this.status = status
    this.code = code
  }
}

function base(deploymentId: string): string {
  return `${API_BASE}/v1/sovereigns/${encodeURIComponent(deploymentId)}/schedules`
}

function itemURL(deploymentId: string, ns: string, name: string): string {
  return `${base(deploymentId)}/${encodeURIComponent(ns)}/${encodeURIComponent(name)}`
}

async function parse<T>(res: Response): Promise<T> {
  if (res.ok) return (await res.json()) as T
  let code = ''
  let detail = ''
  try {
    const body = (await res.json()) as { error?: string; detail?: string }
    code = body.error ?? ''
    detail = body.detail ?? ''
  } catch {
    /* non-JSON error body */
  }
  throw new ScheduleApiError(res.status, code, detail)
}

export async function createSchedule(
  deploymentId: string,
  body: ScheduleCreateRequest,
): Promise<ScheduleResponse> {
  const res = await authedFetch(base(deploymentId), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  return parse<ScheduleResponse>(res)
}

export async function updateSchedule(
  deploymentId: string,
  ns: string,
  name: string,
  body: ScheduleUpdateRequest,
): Promise<ScheduleResponse> {
  const res = await authedFetch(itemURL(deploymentId, ns, name), {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  return parse<ScheduleResponse>(res)
}

export async function deleteSchedule(
  deploymentId: string,
  ns: string,
  name: string,
): Promise<ScheduleDeleteResponse> {
  const res = await authedFetch(itemURL(deploymentId, ns, name), { method: 'DELETE' })
  return parse<ScheduleDeleteResponse>(res)
}
