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
 * Status codes (#5731):
 *   202 — the request was PATCHED onto the Continuum CR (`status:
 *         "requested"`). The K-Cont-2 reconciler runs the 7-step
 *         sequence afterwards; this response is NOT a completion.
 *   200 — the live cnpg-pair path really performed the promotion
 *         (`status:"completed"`), or an authz/validation branch is
 *         carrying its semantic code in `httpStatus` + `error`.
 *   404 — the deployment is not registered with this catalyst-api.
 *   503 — the Sovereign cluster could not be reached.
 *
 * A 404/503 means NOTHING was attempted and the DR state is UNKNOWN. The
 * handler used to answer 200 `completed` on both, which reported a
 * finished failover for a cluster it could not reach.
 *
 * UI callers MUST still inspect `applied` (and `error`) — some branches
 * carry their outcome in the body (e.g. `error:"no-live-dr-pair"`,
 * `applied:false` when the app isn't placed active-hot-standby on a
 * 2-region Sovereign).
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
  /** "requested" (202, reconciler will run it) | "completed" (live pair promoted) | a semantic error code. */
  status?: string
  /** true when the request was applied to the cluster (patched or promoted). */
  applied?: boolean
  /** true ONLY when a promotion was observed — never set for a request that is merely queued. */
  completed?: boolean
  /** present on any failure/no-op outcome (e.g. "no-live-dr-pair", "sovereign-unreachable"). */
  error?: string
  /** the semantic HTTP status carried in-body. */
  httpStatus?: number
}

/**
 * ContinuumSwitchoverPreview — body of
 * POST /api/v1/sovereigns/{id}/continuum/{name}/switchover/preview
 * (continuum_extras.go::HandleContinuumSwitchoverPreview).
 *
 * The READ-ONLY RPO/health preflight the confirm dialog runs BEFORE it
 * arms the [ Confirm Switchover ] button. `promotable` is the gate: it is
 * true only when `blockingChecks` is empty (target is not already primary,
 * WAL lag is within 4× RTO, and the Continuum is not mid-switchover /
 * Failed). `currentLagSec` is the live replication lag the operator sees
 * before committing. Nothing is mutated by this call.
 */
export interface ContinuumSwitchoverPreview {
  continuum: string
  namespace: string
  targetRegion: string
  currentPrimary: string
  currentLagSec: number
  estimatedDurationSec: number
  estimatedDuration: string
  blockingChecks: string[]
  promotable: boolean
  message: string
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

/**
 * ContinuumFleetItem — one row of GET /api/v1/fleet/continuum. Mirrors
 * the catalyst-api `continuumFleetItem` shape (continuum_extras.go) — a
 * roll-up of every live Continuum CR across the Sovereigns this
 * catalyst-api can see. Drives the ContinuumPage `list` (fleet) mode.
 */
export interface ContinuumFleetItem {
  sovereign: string
  name: string
  namespace: string
  primaryRegion: string
  currentPrimary: string
  phase: string
  walLagSeconds: number
  leaseHolder?: string
  lastSwitchover?: string
  healthy: boolean
}

/** ContinuumFleetResponse — items envelope of GET /fleet/continuum.
 *
 * #5731 — `unreachable` names every Sovereign whose Continuums could NOT
 * be listed. The endpoint used to append a synthesized `Healthy: true`
 * row whenever `items` came back empty, so an unreadable estate rendered
 * as a healthy one. It now returns zero rows instead, which makes
 * `unreachable` the only way to distinguish "no DR is configured" from
 * "we could read nothing" — a renderer that ignores it will show the
 * same calm empty state for both.
 */
export interface ContinuumFleetResponse {
  items: ContinuumFleetItem[]
  total: number
  unreachable?: string[]
}

/**
 * ContinuumReplicaInfo — one per-region replica row of the replication
 * status. Mirrors the catalyst-api `continuumReplicaInfo` shape
 * (continuum_dr_extras.go). The fields are best-effort: a synthesized
 * fallback shape (source !== "live") may leave some empty.
 */
export interface ContinuumReplicaInfo {
  region?: string
  cluster?: string
  role?: string // "primary" | "replica"
  state?: string // streaming/connection state
  lagSeconds?: number
  lagBytes?: number
}

/**
 * ContinuumReplicationStatus — body of
 * GET /api/v1/sovereigns/{id}/continuum/{name}/replication-status
 * (continuum_dr_extras.go::HandleContinuumReplicationStatus).
 *
 * This is the LIVE DR telemetry the per-app Topology tab renders for a
 * cross-region (cnpg-pair / Continuum-backed) component: the current
 * primary, the WAL replication lag in seconds, and the streaming/sync
 * state. `source` is the honesty flag — "live" means it was read off the
 * real Continuum + CNPGPair CR status; "synthesized" means the in-cluster
 * client was bootstrapping or the CRs were missing and the backend
 * returned a realistic-but-not-live shape. The UI MUST surface this so a
 * fallback is never passed off as live.
 */
/**
 * ContinuumHealthGate — one row of the DERIVED health-gate list (#4923).
 * Never a hardcoded all-Pass wall: Pass requires positive evidence, Warn
 * means unverified/over-threshold, Fail means a verified fault.
 */
export interface ContinuumHealthGate {
  name: string
  status: string // "Pass" | "Warn" | "Fail"
  message?: string
  severity?: string // "info" | "warning" | "critical"
}

export interface ContinuumReplicationStatus {
  continuum: string
  namespace: string
  primaryRegion: string
  currentPrimary: string
  walLagSeconds: number
  walLagBytes?: number
  replicaPromotable?: boolean
  streamingState?: string
  syncState?: string
  lastHeartbeat?: string
  /**
   * Tri-state standby-leg verdict (#4923/#4901):
   * true — verified reachable+following off the live cnpg cluster-pair;
   * false — the required hot-standby is ABSENT (the explicit honest
   * condition the DR panel must surface, never masked as healthy);
   * undefined — unverifiable (no cnpg pair resolvable) — unknown, not green.
   */
  standbyAvailable?: boolean
  healthGates?: ContinuumHealthGate[]
  replicas?: ContinuumReplicaInfo[]
  observedAt?: string
  source: string // "live" | "pending"
}

/* ── Endpoint helpers ────────────────────────────────────────────── */

function continuumsBase(sovereignId: string): string {
  return `${API_BASE}/v1/sovereigns/${encodeURIComponent(sovereignId)}/continuums`
}

/**
 * continuumSingularBase — the SINGULAR `/continuum/` path the EPIC-6
 * iter-6 target-state endpoints live under (replication-status,
 * switchover/history, settings). Distinct from continuumsBase (plural
 * `/continuums/`, the older CRUD surface) — both are live for
 * back-compat; the replication-status route is singular only.
 */
function continuumSingularBase(sovereignId: string): string {
  return `${API_BASE}/v1/sovereigns/${encodeURIComponent(sovereignId)}/continuum`
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

/**
 * getContinuumReplicationStatus — fetch the live DR replication telemetry
 * for a Continuum CR (GET .../continuum/{name}/replication-status). The
 * per-app Topology tab calls this for cross-region components (continuum
 * name = `dr-<app>`) to render the standby region + live WAL lag. A 404
 * (no DR pair for this app) throws so the caller can render the calm
 * singleton/no-DR state — NOT a fabricated lag.
 */
export async function getContinuumReplicationStatus(
  sovereignId: string,
  name: string,
  opts: { namespace?: string } = {},
): Promise<ContinuumReplicationStatus> {
  const params = new URLSearchParams()
  if (opts.namespace) params.set('namespace', opts.namespace)
  const qs = params.toString()
  const url = `${continuumSingularBase(sovereignId)}/${encodeURIComponent(name)}/replication-status${qs ? '?' + qs : ''}`
  const res = await authedFetch(url, { headers: { Accept: 'application/json' } })
  if (!res.ok) {
    throw new Error(`continuum replication-status: HTTP ${res.status}`)
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

/**
 * getSwitchoverPreview — run the read-only RPO/health preflight for a
 * Continuum CR (POST .../continuum/{name}/switchover/preview). The confirm
 * dialog calls this before arming its [ Confirm Switchover ] button so the
 * operator sees the live lag + any blocking checks and can only proceed
 * when `promotable` is true. Read-only — mutates nothing on the cluster.
 *
 * The singular `/continuum/` path is used (the preview endpoint lives only
 * under the singular alias — see continuum_extras.go).
 */
export async function getSwitchoverPreview(
  sovereignId: string,
  name: string,
  body: { targetRegion?: string } = {},
  opts: { namespace?: string } = {},
): Promise<ContinuumSwitchoverPreview> {
  const params = new URLSearchParams()
  if (opts.namespace) params.set('namespace', opts.namespace)
  const qs = params.toString()
  const url = `${continuumSingularBase(sovereignId)}/${encodeURIComponent(name)}/switchover/preview${qs ? '?' + qs : ''}`
  const res = await authedFetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
    body: JSON.stringify(body),
  })
  if (!res.ok) {
    const detail = await res.text().catch(() => '')
    throw new Error(`switchover preview: HTTP ${res.status} ${detail}`)
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
 * listFleetContinuums — fleet-wide roll-up of every live Continuum CR
 * (GET /api/v1/fleet/continuum). The endpoint is NOT scoped to a single
 * Sovereign — catalyst-api aggregates across every Sovereign it can
 * reach. The ContinuumPage `list` mode renders one row per item, linking
 * to the per-continuum overview.
 *
 * Wire path:
 *
 *   browser ──/api/v1/fleet/continuum──▶ catalyst-api (continuum_extras.go)
 */
export async function listFleetContinuums(): Promise<ContinuumFleetResponse> {
  const url = `${API_BASE}/v1/fleet/continuum`
  const res = await authedFetch(url, { headers: { Accept: 'application/json' } })
  if (!res.ok) {
    throw new Error(`continuum fleet list: HTTP ${res.status}`)
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
