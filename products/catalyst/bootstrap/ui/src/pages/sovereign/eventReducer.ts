/**
 * eventReducer.ts — pure reducer that folds the catalyst-api SSE/event
 * stream into a per-component install-state map for the Sovereign Admin
 * page.
 *
 * The previous DAG implementation (`pages/provision/ProvisionPage.tsx`)
 * mapped phases onto a synthetic supernode graph. That view has been
 * abandoned: the operator wants the Admin landing page to render every
 * Application as a card from the moment provisioning starts, with a per-
 * Application status pill and a click-through to a per-Application page
 * that owns its own logs / dependencies / status / overview tabs.
 *
 * Two synthetic phase nodes ("Hetzner infra" and "Cluster bootstrap")
 * sit ABOVE the application grid as compact status banners — they're
 * not Applications, they're the Phase 0 + cloud-init phases. This file
 * exposes their state too, but they are NOT rendered as cards.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), every Application
 * id this reducer recognises is computed at runtime from the catalog
 * (BOOTSTRAP_KIT + the wizard's selectedComponents). Adding or removing
 * a Blueprint never requires touching this file.
 *
 * Per #1 (waterfall is the contract), this reducer is the target shape:
 * it understands per-component `install` events the catalyst-api will
 * emit on the same SSE channel as today's `tofu-*` / `flux-bootstrap`
 * phases, and gracefully degrades to "unknown" when the backend hasn't
 * caught up. Both directions of the contract — events the API emits
 * today, events the API will emit — are encoded explicitly here so the
 * UI never has to special-case missing data.
 *
 * Event vocabulary (from catalyst-api SSE):
 *   • `tofu-init` / `tofu-plan` / `tofu-apply` / `tofu-output` / `tofu`
 *     → drive the "Hetzner infra" phase banner.
 *   • `flux-bootstrap`
 *     → drives the "Cluster bootstrap" phase banner.
 *   • `install` (target shape)
 *     → drives a single Application's state. The event must carry
 *       `component: "<bp-id>"` and `state: "installing"|"installed"|
 *       "failed"|"degraded"|"pending"`.
 *   • Anything else with a `component:` field whose value matches a
 *     known Application id falls back to the same component-state
 *     state-machine (running on first sight, failed when level=error,
 *     done when phase=done).
 */

export type ApplicationStatus =
  | 'pending'
  | 'installing'
  | 'installed'
  | 'failed'
  | 'degraded'
  | 'unknown'

export type PhaseStatus = 'pending' | 'running' | 'done' | 'failed'

/**
 * Raw event shape — superset of every event the catalyst-api emits on
 * the SSE channel. Matches `pages/provision/ProvisionPage` legacy
 * `ProvisionEvent` plus the per-component fields the new contract adds.
 */
export interface DeploymentEvent {
  /** RFC3339 timestamp from the API. */
  time?: string
  /** Phase id — `tofu-*`, `flux-bootstrap`, `install`, etc. */
  phase: string
  /** Optional log level. */
  level?: 'info' | 'warn' | 'error'
  /** Free-form log message. */
  message?: string
  /**
   * Per-component target shape — set when the event pertains to a single
   * Application. Either the full Blueprint id ("bp-cilium") or the bare
   * slug ("cilium"); normaliseComponentId() handles both.
   */
  component?: string
  /**
   * Per-component target state — set on `phase: install` events the API
   * will emit when each Application's Flux Kustomization changes state.
   */
  state?: 'pending' | 'installing' | 'installed' | 'failed' | 'degraded'
  /** Optional helm-release name (api may emit; fallback derived from id). */
  helmRelease?: string
  /** Optional namespace (api may emit; fallback "unknown"). */
  namespace?: string
  /** Optional chart version (api may emit). */
  chartVersion?: string
}

/**
 * Per-Application state derived by the reducer. Keys are the canonical
 * Blueprint id (always `bp-<slug>`).
 */
export interface ApplicationState {
  id: string
  status: ApplicationStatus
  message: string | null
  /** Timestamp of the most recent state-changing event (ISO string). */
  lastEventTime: string | null
  /** Helm release name surfaced from the event stream (or derived). */
  helmRelease: string | null
  /** Kubernetes namespace surfaced from the event stream. */
  namespace: string | null
  /** Chart semver surfaced from the event stream. */
  chartVersion: string | null
  /** Total event count attributed to this Application (for log replay UX). */
  eventCount: number
}

export interface ReducerState {
  /** Per-Application state map, keyed by full Blueprint id ("bp-<slug>"). */
  apps: Record<string, ApplicationState>
  /** Hetzner infra phase banner state. */
  hetznerInfra: {
    status: PhaseStatus
    message: string | null
    lastEventTime: string | null
    /** Subset of hcloud_* resource families seen during tofu-apply. */
    seenResources: Set<string>
  }
  /** Cluster bootstrap phase banner state. */
  clusterBootstrap: {
    status: PhaseStatus
    message: string | null
    lastEventTime: string | null
  }
  /** Event count routed to each per-Application bucket and the two banners. */
  eventsByTarget: Record<string, DeploymentEvent[]>
}

const HCLOUD_FAMILIES = ['hcloud_network', 'hcloud_firewall', 'hcloud_server', 'hcloud_load_balancer'] as const
const TOFU_PHASES = new Set(['tofu', 'tofu-init', 'tofu-plan', 'tofu-apply', 'tofu-output'])
const HETZNER_INFRA_KEY = '__hetzner-infra__'
const CLUSTER_BOOTSTRAP_KEY = '__cluster-bootstrap__'

export function HETZNER_INFRA_BUCKET(): string { return HETZNER_INFRA_KEY }
export function CLUSTER_BOOTSTRAP_BUCKET(): string { return CLUSTER_BOOTSTRAP_KEY }

/**
 * Normalise a component-id string to canonical `bp-<slug>` form. Accepts
 * both `bp-cilium` and `cilium` so the reducer is forgiving of legacy
 * event payloads that drop the prefix.
 */
export function normaliseComponentId(id: string | null | undefined): string | null {
  if (typeof id !== 'string' || id.length === 0) return null
  return id.startsWith('bp-') ? id : `bp-${id}`
}

/**
 * Build the initial state for a known Application id list — every id
 * starts at `pending` so the card grid renders the full set from the
 * first paint, before any events arrive.
 */
export function buildInitialState(applicationIds: readonly string[]): ReducerState {
  const apps: Record<string, ApplicationState> = {}
  for (const rawId of applicationIds) {
    const id = normaliseComponentId(rawId)
    if (!id) continue
    apps[id] = {
      id,
      status: 'pending',
      message: null,
      lastEventTime: null,
      helmRelease: null,
      namespace: null,
      chartVersion: null,
      eventCount: 0,
    }
  }
  return {
    apps,
    hetznerInfra: {
      status: 'pending',
      message: null,
      lastEventTime: null,
      seenResources: new Set(),
    },
    clusterBootstrap: {
      status: 'pending',
      message: null,
      lastEventTime: null,
    },
    eventsByTarget: {},
  }
}

/**
 * Map the API's per-component `state` value to the UI's `ApplicationStatus`
 * vocabulary. The two are aligned today, but we keep the indirection so a
 * future state vocabulary change in the API doesn't ripple through every
 * UI call site.
 */
function mapApiState(state: DeploymentEvent['state']): ApplicationStatus | null {
  if (!state) return null
  switch (state) {
    case 'pending': return 'pending'
    case 'installing': return 'installing'
    case 'installed': return 'installed'
    case 'failed': return 'failed'
    case 'degraded': return 'degraded'
    default: return null
  }
}

/**
 * Apply a single event — pure, in-place mutation on a CLONED state.
 * Callers MUST clone before calling (see `reduceEvents` below).
 */
export function applyEvent(state: ReducerState, ev: DeploymentEvent): void {
  const time = ev.time ?? null

  // ── Tofu / Hetzner-infra phase ────────────────────────────────────
  if (TOFU_PHASES.has(ev.phase)) {
    const banner = state.hetznerInfra
    if (banner.status === 'pending') banner.status = 'running'
    if (ev.level === 'error') banner.status = 'failed'
    if (ev.phase === 'tofu-output') banner.status = 'done'
    if (ev.phase === 'tofu' && typeof ev.message === 'string') {
      for (const f of HCLOUD_FAMILIES) {
        if (ev.message.indexOf(f) >= 0) banner.seenResources.add(f)
      }
    }
    if (ev.message) banner.message = ev.message
    if (time) banner.lastEventTime = time
    pushEventToBucket(state, HETZNER_INFRA_KEY, ev)
    return
  }

  // ── Cluster bootstrap phase ───────────────────────────────────────
  if (ev.phase === 'flux-bootstrap') {
    // Tofu must be done by the time bootstrap fires; converge state.
    if (state.hetznerInfra.status !== 'failed') state.hetznerInfra.status = 'done'
    const banner = state.clusterBootstrap
    if (banner.status === 'pending') banner.status = 'running'
    if (ev.level === 'error') banner.status = 'failed'
    if (ev.message) banner.message = ev.message
    if (time) banner.lastEventTime = time
    pushEventToBucket(state, CLUSTER_BOOTSTRAP_KEY, ev)
    return
  }

  // ── Per-Application install events (target shape) ─────────────────
  // Either `phase: install` with `component`, or any phase that carries
  // `component` matching a known Application id.
  const appId = normaliseComponentId(ev.component) ?? normaliseComponentId(ev.phase)
  if (appId && state.apps[appId]) {
    const app = state.apps[appId]
    const apiState = mapApiState(ev.state)
    if (apiState) {
      app.status = apiState
    } else if (ev.level === 'error') {
      app.status = 'failed'
    } else if (app.status === 'pending') {
      app.status = 'installing'
    }
    if (ev.message) app.message = ev.message
    if (ev.helmRelease) app.helmRelease = ev.helmRelease
    if (ev.namespace) app.namespace = ev.namespace
    if (ev.chartVersion) app.chartVersion = ev.chartVersion
    if (time) app.lastEventTime = time
    app.eventCount += 1
    pushEventToBucket(state, appId, ev)
    return
  }

  // ── Otherwise: route to the cluster-bootstrap bucket so nothing is lost ─
  pushEventToBucket(state, CLUSTER_BOOTSTRAP_KEY, ev)
}

/**
 * Append an event to the target bucket, preserving order.
 */
function pushEventToBucket(state: ReducerState, bucketKey: string, ev: DeploymentEvent): void {
  const bucket = state.eventsByTarget[bucketKey]
  if (bucket) bucket.push(ev)
  else state.eventsByTarget[bucketKey] = [ev]
}

/**
 * Fold an array of events into the supplied state. Returns a NEW state
 * object so React renders see a fresh reference (the caller passes a
 * cloned base; we mutate it and return). Used by both the GET /events
 * replay path and the live SSE stream — same reducer in both cases.
 */
export function reduceEvents(base: ReducerState, events: readonly DeploymentEvent[]): ReducerState {
  const next: ReducerState = {
    apps: { ...base.apps },
    hetznerInfra: {
      ...base.hetznerInfra,
      seenResources: new Set(base.hetznerInfra.seenResources),
    },
    clusterBootstrap: { ...base.clusterBootstrap },
    eventsByTarget: { ...base.eventsByTarget },
  }
  // Clone every nested record we might mutate.
  for (const id of Object.keys(next.apps)) {
    const a = next.apps[id]
    if (a) next.apps[id] = { ...a }
  }
  for (const k of Object.keys(next.eventsByTarget)) {
    const arr = next.eventsByTarget[k]
    if (arr) next.eventsByTarget[k] = [...arr]
  }
  for (const ev of events) applyEvent(next, ev)
  return next
}

/**
 * Force every still-`pending`/`installing` Application + every phase
 * banner into the terminal `done`/`installed` state. Called when the
 * SSE stream emits its terminal `done` event with `status: ready` —
 * any Application the API didn't emit a per-component install event
 * for (because the events backlog hasn't caught up) flips to installed
 * so the page reads as "everything is up" without having to wait for
 * a phantom event that may never arrive.
 */
export function markAllReady(base: ReducerState): ReducerState {
  const next = reduceEvents(base, [])
  next.hetznerInfra.status = next.hetznerInfra.status === 'failed' ? 'failed' : 'done'
  next.clusterBootstrap.status = next.clusterBootstrap.status === 'failed' ? 'failed' : 'done'
  for (const id of Object.keys(next.apps)) {
    const a = next.apps[id]
    if (!a) continue
    if (a.status !== 'failed' && a.status !== 'degraded') {
      next.apps[id] = { ...a, status: 'installed' }
    }
  }
  return next
}

/**
 * Compute an aggregate Sovereign-wide status from the per-component +
 * phase-banner mix. Used by the top-bar status pill.
 *
 *   • failed / degraded → fan up to top-bar
 *   • any installing/running → "installing"
 *   • everything done       → "ready"
 *   • everything pending    → "pending"
 */
export type SovereignStatus = 'pending' | 'installing' | 'installed' | 'failed' | 'degraded'

export function computeOverallStatus(state: ReducerState): SovereignStatus {
  const phaseFailed =
    state.hetznerInfra.status === 'failed' || state.clusterBootstrap.status === 'failed'
  const appFailed = Object.values(state.apps).some((a) => a.status === 'failed')
  if (phaseFailed || appFailed) return 'failed'
  const appDegraded = Object.values(state.apps).some((a) => a.status === 'degraded')
  if (appDegraded) return 'degraded'
  const phaseRunning =
    state.hetznerInfra.status === 'running' || state.clusterBootstrap.status === 'running'
  const appInstalling = Object.values(state.apps).some(
    (a) => a.status === 'installing',
  )
  if (phaseRunning || appInstalling) return 'installing'
  const allInstalled =
    state.hetznerInfra.status === 'done' &&
    state.clusterBootstrap.status === 'done' &&
    Object.values(state.apps).every((a) => a.status === 'installed')
  if (allInstalled) return 'installed'
  return 'pending'
}
