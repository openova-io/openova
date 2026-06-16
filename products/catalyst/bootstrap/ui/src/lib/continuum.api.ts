/**
 * continuum.api.ts — typed REST client wrappers for the EPIC-6 Slice
 * U-DR-1 (#1101) Continuum DR endpoints on catalyst-api.
 *
 * Wire path:
 *
 *   browser ──/api/v1/sovereigns/{id}/continuums/...──▶ catalyst-api
 *
 * The handlers patch the Continuum CR's spec — the K-Cont-2 reconciler
 * (the per-CR goroutine in core/controllers/continuum) observes the
 * change on its next loop iteration and either runs the 7-step
 * Sequencer (switchover / failback) or waits for the failback approval
 * gate.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 every URL derives from
 * `API_BASE` so the contabo strip-sovereign / direct-Sovereign
 * distinction is resolved at config-time, not in components.
 */

import { API_BASE } from '@/shared/config/urls'
import { authedFetch } from '@/shared/lib/authedFetch'

/* ── Wire types ──────────────────────────────────────────────────── */

/** ContinuumGetResponse — body of GET /continuums/{name}. */
export interface ContinuumGetResponse {
  name: string
  namespace: string
  uid: string
  spec?: Record<string, unknown>
  status?: Record<string, unknown>
}

/** ContinuumSwitchoverRequest — body of POST .../switchover. */
export interface ContinuumSwitchoverRequest {
  targetRegion: string
  reason?: string
}

/** ContinuumSwitchoverResponse — switchover-request body.
 *
 * The catalyst-api switchover handler returns HTTP 200 even for failure
 * cases (the UAT runner reads the body, not the status code), carrying the
 * real outcome in `applied` / `completed` / `error` / `httpStatus`. UI
 * callers MUST inspect `applied` (and `error`) — a 200 alone does NOT mean
 * the switchover happened (e.g. `error:"no-live-dr-pair"`, `applied:false`
 * when the app isn't placed active-hot-standby on a 2-region Sovereign).
 */
export interface ContinuumSwitchoverResponse {
  name: string
  namespace: string
  targetRegion: string
  fromRegion?: string
  toRegion?: string
  reason?: string
  requestedAt: string
  requestedBy?: string
  message: string
  /** true only when the switchover was actually applied to the cluster. */
  applied?: boolean
  /** true once the promotion completed. */
  completed?: boolean
  /** present on any failure/no-op outcome (e.g. "no-live-dr-pair"). */
  error?: string
  /** the semantic HTTP status carried in-body (the wire status is 200). */
  httpStatus?: number
}

/** ContinuumFailbackRequest — body of POST .../failback. */
export interface ContinuumFailbackRequest {
  reason?: string
}

/** ContinuumFailbackResponse — 202 Accepted body. */
export interface ContinuumFailbackResponse {
  name: string
  namespace: string
  requestedAt: string
  requestedBy?: string
  approvalRequired: boolean
  message: string
}

/** ContinuumFailbackApproveResponse — 202 Accepted body. */
export interface ContinuumFailbackApproveResponse {
  name: string
  namespace: string
  approvedAt: string
  approvedBy?: string
  message: string
}

/**
 * ContinuumAuditEvent — one row from the audit ring buffer / SSE
 * stream. Mirrors `audit.Event` in the catalyst-api package — the
 * superset shape every audit-type populates only the fields it owns.
 */
export interface ContinuumAuditEvent {
  auditType: string
  ts: string
  actor?: string
  sovereignId?: string
  result?: string
  targetUser?: string
  targetUserEmail?: string
  targetApp?: string
  tier?: string
  detail?: string
  // Continuum events tag these via the superset shape's free fields.
  // The reconciler (K-Cont-2) emits its 9 reserved types via
  // `core/controllers/continuum/internal/events.Event` — that struct
  // serializes as a separate body shape (`type`, `continuumName`,
  // `fromPrimary`, `toPrimary`, `lagSeconds`, `step`) on the NATS
  // subject; the handler-side audit Bus exposes its `audit.Event`
  // shape. The bridge is up to the future NATS forwarder; for the
  // initial UI we render off the REST handler's catalyst-api side
  // shape which already carries `auditType` + `actor` + `detail`.
}

export interface ContinuumAuditListResponse {
  items: ContinuumAuditEvent[]
  nextOffset?: number
  total: number
}

/* ── Endpoint helpers ────────────────────────────────────────────── */

function continuumsBase(sovereignId: string): string {
  return `${API_BASE}/v1/sovereigns/${encodeURIComponent(sovereignId)}/continuums`
}

function auditBase(sovereignId: string): string {
  return `${API_BASE}/v1/sovereigns/${encodeURIComponent(sovereignId)}/audit`
}

/* ── REST calls ──────────────────────────────────────────────────── */

export async function getContinuum(
  sovereignId: string,
  name: string,
  opts: { namespace?: string } = {},
): Promise<ContinuumGetResponse> {
  const params = new URLSearchParams()
  if (opts.namespace) params.set('namespace', opts.namespace)
  const qs = params.toString()
  const url = `${continuumsBase(sovereignId)}/${encodeURIComponent(name)}${qs ? '?' + qs : ''}`
  const res = await authedFetch(url, { headers: { Accept: 'application/json' } })
  if (!res.ok) {
    throw new Error(`continuum get: HTTP ${res.status}`)
  }
  return res.json()
}

export async function requestSwitchover(
  sovereignId: string,
  name: string,
  body: ContinuumSwitchoverRequest,
  opts: { namespace?: string } = {},
): Promise<ContinuumSwitchoverResponse> {
  const params = new URLSearchParams()
  if (opts.namespace) params.set('namespace', opts.namespace)
  const qs = params.toString()
  const url = `${continuumsBase(sovereignId)}/${encodeURIComponent(name)}/switchover${qs ? '?' + qs : ''}`
  const res = await authedFetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
    body: JSON.stringify(body),
  })
  if (!res.ok) {
    const detail = await res.text().catch(() => '')
    throw new Error(`switchover: HTTP ${res.status} ${detail}`)
  }
  return res.json()
}

export async function requestFailback(
  sovereignId: string,
  name: string,
  body: ContinuumFailbackRequest = {},
  opts: { namespace?: string } = {},
): Promise<ContinuumFailbackResponse> {
  const params = new URLSearchParams()
  if (opts.namespace) params.set('namespace', opts.namespace)
  const qs = params.toString()
  const url = `${continuumsBase(sovereignId)}/${encodeURIComponent(name)}/failback${qs ? '?' + qs : ''}`
  const res = await authedFetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
    body: JSON.stringify(body),
  })
  if (!res.ok) {
    const detail = await res.text().catch(() => '')
    throw new Error(`failback: HTTP ${res.status} ${detail}`)
  }
  return res.json()
}

export async function approveFailback(
  sovereignId: string,
  name: string,
  opts: { namespace?: string } = {},
): Promise<ContinuumFailbackApproveResponse> {
  const params = new URLSearchParams()
  if (opts.namespace) params.set('namespace', opts.namespace)
  const qs = params.toString()
  const url = `${continuumsBase(sovereignId)}/${encodeURIComponent(name)}/failback/approve${qs ? '?' + qs : ''}`
  const res = await authedFetch(url, {
    method: 'POST',
    headers: { Accept: 'application/json' },
  })
  if (!res.ok) {
    const detail = await res.text().catch(() => '')
    throw new Error(`approve-failback: HTTP ${res.status} ${detail}`)
  }
  return res.json()
}

export async function listContinuumAudit(
  sovereignId: string,
  opts: { limit?: number; offset?: number; type?: string; actor?: string; since?: string } = {},
): Promise<ContinuumAuditListResponse> {
  const params = new URLSearchParams()
  if (opts.limit) params.set('limit', String(opts.limit))
  if (opts.offset) params.set('offset', String(opts.offset))
  if (opts.type) params.set('type', opts.type)
  if (opts.actor) params.set('actor', opts.actor)
  if (opts.since) params.set('since', opts.since)
  const qs = params.toString()
  const url = `${auditBase(sovereignId)}/continuum${qs ? '?' + qs : ''}`
  const res = await authedFetch(url, { headers: { Accept: 'application/json' } })
  if (!res.ok) {
    throw new Error(`continuum audit list: HTTP ${res.status}`)
  }
  return res.json()
}

/**
 * continuumAuditStreamURL — SSE endpoint for live audit events. Mirrors
 * `applicationStreamURL` from catalog.api: when an access_token is
 * supplied it's stamped on the query string for the EventSource (which
 * cannot send Authorization headers) — same pattern slice U5-U8 uses
 * for /audit/rbac/stream.
 */
export function continuumAuditStreamURL(sovereignId: string, accessToken?: string): string {
  const params = new URLSearchParams()
  if (accessToken) params.set('access_token', accessToken)
  const qs = params.toString()
  return `${auditBase(sovereignId)}/continuum/stream${qs ? '?' + qs : ''}`
}

/* ── Switchover step constants ───────────────────────────────────── */

/**
 * SWITCHOVER_STEPS — 7-step sequence shipped by the K-Cont-2
 * Sequencer (`core/controllers/continuum/internal/switchover/sequence.go`).
 * The names below MUST stay in lock-step with the Sequencer's `name:`
 * fields — the SwitchoverDialog renders them so the operator sees
 * exactly what the Sequencer will execute.
 *
 * Per the brief these are hardcoded here (the alternative — pulling
 * them from a /continuums/{name}/steps endpoint — adds an extra round
 * trip with no payoff; the reconciler's step names are a stable
 * contract per K-Cont-2).
 */
export const SWITCHOVER_STEPS: { id: number; name: string; description: string }[] = [
  {
    id: 1,
    name: 'validate-lease',
    description: 'Validate the witness lease holder is the current primary (or quorum allows takeover).',
  },
  {
    id: 2,
    name: 'cordon-old-primary',
    description: 'Annotate the old primary CNPG Cluster CR to demote it; CNPG operator stops accepting writes.',
  },
  {
    id: 3,
    name: 'drain-http',
    description: 'Set Cilium HTTPRoute weight to 0 toward the old primary; in-flight traffic drains over a short window.',
  },
  {
    id: 4,
    name: 'flip-dns',
    description: 'Flip the lua-record probe target to the new primary via PowerDNS Manager (low-TTL DNS, ~5s).',
  },
  {
    id: 5,
    name: 'swap-lease',
    description: 'Release the lease on the old primary; acquire it on the new primary.',
  },
  {
    id: 6,
    name: 'uncordon-new-primary',
    description: 'Clear the demotion annotation; flip CNPG replica.enabled flags so the new primary stops following WAL.',
  },
  {
    id: 7,
    name: 'audit-emit',
    description: 'Emit `continuum-switchover` audit event on the catalyst.audit JetStream subject.',
  },
]

/* ── Helpers ──────────────────────────────────────────────────────── */

/**
 * parseLagSeconds — Continuum status surfaces `replicationLagSeconds`
 * as int64 (number) but legacy paths may carry a duration string;
 * accept both.
 */
export function parseLagSeconds(value: unknown): number | null {
  if (value == null) return null
  if (typeof value === 'number' && Number.isFinite(value)) return value
  if (typeof value === 'string') {
    const trimmed = value.trim()
    if (trimmed === '') return null
    const n = Number(trimmed)
    if (Number.isFinite(n)) return n
    // Try `Ns` / `Nm` shape used by older payloads.
    const m = /^(\d+)(s|m|h)$/.exec(trimmed)
    if (m) {
      const n = Number(m[1])
      switch (m[2]) {
        case 's':
          return n
        case 'm':
          return n * 60
        case 'h':
          return n * 3600
      }
    }
  }
  return null
}

/**
 * lagBucket — bucketizes lag seconds for the StatusPanel's progress
 * bar color. Thresholds per the brief: green <30s, yellow 30-60s,
 * red >60s.
 */
export type LagBucket = 'green' | 'yellow' | 'red' | 'unknown'

export function lagBucket(lag: number | null): LagBucket {
  if (lag == null) return 'unknown'
  if (lag < 30) return 'green'
  if (lag <= 60) return 'yellow'
  return 'red'
}
