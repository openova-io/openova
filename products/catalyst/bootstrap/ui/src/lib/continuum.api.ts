/**
 * continuum.api.ts — typed REST client wrappers for the EPIC-6 Slice
 * U-DR-1 (#1101) Continuum DR endpoints on catalyst-api.
 *
 * Wire path:
 *
 *   browser ──/api/v1/sovereigns/{id}/continuums/...──▶ catalyst-api
 *
 * Switchover is DESIRED STATE (2026-11-07): there is no switchover
 * request. The operator sets the app's Primary in the placement editor;
 * the continuum-controller converges the writer onto it (7-step
 * Sequencer). This client only READS that convergence
 * (replication-status) and runs the read-only switchover PREVIEW so the
 * placement editor can say what a Save will do.
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
 * catalyst-api can see. Fleet roll-up of every Continuum.
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
  /** Desired-state projection — see ContinuumReplicationStatus. */
  desiredPrimaryRegion?: string
  converged?: boolean
  switchoverInProgress?: boolean
  step?: string
  lastSwitchoverDetail?: ContinuumLastSwitchover
  convergeReason?: string
  convergeMessage?: string
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
  /**
   * The Continuum CR's OWN reconciled status, relayed by the endpoint (UAT
   * row 62). `phase` is the controller's reconciled verdict (Healthy /
   * Degraded / FailedOver / …), `leaseHolder` is the region currently holding
   * the DNS-witness lease and `leaseExpiresAt` its rolling expiry — the three
   * facts that make the DR panel a live reading rather than a static badge.
   * All three are `omitempty` server-side: absent means the CR does not report
   * it, and the panel must render nothing rather than invent a value.
   */
  phase?: string
  leaseHolder?: string
  leaseExpiresAt?: string
  healthGates?: ContinuumHealthGate[]
  replicas?: ContinuumReplicaInfo[]
  observedAt?: string
  source: string // "live" | "pending"

  /* ── Desired-state switchover (2026-11-07) ──────────────────────────
   * There is NO imperative switchover request. The operator declares the
   * Primary region in the placement editor; the Application controller
   * projects it into the Continuum's spec.desiredPrimaryRegion and the
   * continuum-controller CONVERGES. `currentPrimary` above is the OBSERVED
   * writer (status.primaryRegion); these fields describe the declaration
   * and the convergence towards it. All optional — an older backend omits
   * them and the panel renders no convergence claim.
   */
  /** The Primary the operator declared (status echo, else spec). */
  desiredPrimaryRegion?: string
  /** status.primaryRegion verbatim (empty when not yet observed). */
  observedPrimaryRegion?: string
  /** observed === desired. */
  converged?: boolean
  /** The controller is running the 7-step switchover now. */
  switchoverInProgress?: boolean
  /** "step N/7 — <name>" while switchoverInProgress. */
  step?: string
  lastSwitchover?: ContinuumLastSwitchover
  /** The Converged condition's reason (Converged|Converging|ConvergeBlocked|ConvergeFailed). */
  convergeReason?: string
  /** Why it is not converged / what blocks it / when it retries. */
  convergeMessage?: string
}

/** status.lastSwitchover — the continuum-controller's last converge run. */
export interface ContinuumLastSwitchover {
  at?: string
  from?: string
  to?: string
  /** "Success" | "Failed" */
  result?: string
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
 * reach. A fleet view renders one row per item, linking
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
 * lagBucket — bucketizes lag seconds for the DR panel's progress
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
