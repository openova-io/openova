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
 * GROUNDING RULE — the bug this file used to perpetrate, now fixed:
 *
 *   `deployment.status === "ready"` reflects ONLY Phase 0 (OpenTofu
 *   provision + cloud-init handoff) plus the catalyst-api's helmwatch
 *   loop terminating. It says NOTHING about whether each individual
 *   bp-* HelmRelease in the new cluster is actually `installed`. Only
 *   the per-component `phase: "component"` SSE events (or the durable
 *   `Result.componentStates` map the helmwatch persists when it ran
 *   successfully) carry that ground truth.
 *
 *   Therefore: a card NEVER flips to `installed` because of a coarse
 *   deployment-level signal. It only flips when:
 *     1. a `phase: "component"` event with `state: "installed"` arrives
 *        for that specific componentId, OR
 *     2. the durable `Result.componentStates` map (seeded via
 *        `seedComponentStates`) names that componentId as installed.
 *
 *   When neither signal is available — typically because the
 *   catalyst-api couldn't fetch the new cluster's kubeconfig and
 *   skipped helmwatch entirely — every card stays at `pending` and
 *   the AdminPage banner reads "Per-component install monitoring is
 *   unavailable for this deployment". That is the truthful status; an
 *   "all installed" green-pill rollup over silent helmwatch is a
 *   misleading fiction.
 *
 * Event vocabulary (from catalyst-api SSE):
 *   • `tofu-init` / `tofu-plan` / `tofu-apply` / `tofu-output` / `tofu`
 *     → drive the "Hetzner infra" phase banner.
 *   • `flux-bootstrap`
 *     → drives the "Cluster bootstrap" phase banner.
 *   • `component` (helmwatch.PhaseComponent)
 *     → drives a single Application's state. The event must carry
 *       `component: "<bp-id>"` and `state: "installing"|"installed"|
 *       "failed"|"degraded"|"pending"`. A `phase: "component"` event
 *       with `level: "warn"`/`"error"` and NO `component:` field is
 *       the helmwatch-skipped/failed-to-start/timeout signal — it
 *       sets the `phase1WatchSkipped` flag the AdminPage banner reads.
 *   • Anything else with a `component:` field whose value matches a
 *     known Application id falls back to the same component-state
 *     state-machine (installing on first sight, failed when level=error).
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
  /** Phase id — `tofu-*`, `flux-bootstrap`, `component`, etc. */
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
   * Per-component target state — set on `phase: component` events the API
   * emits when each Application's HelmRelease changes state.
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
  /**
   * Phase-1 HelmRelease watch availability. True when the catalyst-api
   * announced (via a `phase: "component"` warn/error event without a
   * `component:` field, OR via finalize-time when no componentStates
   * are available) that it could not observe per-component install
   * state on the new Sovereign cluster — typically because the
   * kubeconfig was not available on the catalyst-api side.
   *
   * The AdminPage reads this to render the "per-component install
   * monitoring is unavailable" banner instead of pretending the
   * bp-* HelmReleases are installed.
   */
  phase1WatchSkipped: boolean
  /**
   * The most-recent helmwatch warn/error message captured. Surfaced
   * verbatim in the banner detail line so the operator can see the
   * precise reason (skipped on missing kubeconfig vs. failed to start
   * vs. context cancelled) without having to crack the raw event log.
   */
  phase1WatchSkippedReason: string | null
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
    phase1WatchSkipped: false,
    phase1WatchSkippedReason: null,
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

  // ── Phase-1 helmwatch unavailability signal ───────────────────────
  // The catalyst-api emits a `phase: "component"` event with `level:
  // "warn"` (or "error") and NO `component:` field when it could not
  // observe per-component install state on the new Sovereign cluster.
  // Cases: (a) kubeconfig not available on the catalyst-api side, so
  // the watch was skipped entirely; (b) NewWatcher() failed to start;
  // (c) the watch loop terminated by context (timeout). All three
  // produce the same UI surface — the AdminPage banner — because all
  // three result in zero ground-truth per-component data for this
  // deployment. The message is captured verbatim so the operator sees
  // the actual reason, and `phase1WatchSkipped` flips to true.
  //
  // CLEAR-RULE (issue #232): the flag is NO LONGER monotonic. If we
  // subsequently receive a per-component event carrying real ground-
  // truth (state ≠ 'skipped' OR a non-empty `component:` field), then
  // helmwatch IS observing this deployment after all and the banner
  // must come down. The previous "stays set for lifetime" guarantee
  // produced a stale banner whenever a single early `state: skipped`
  // event in the SSE replay buffer contradicted a later stream of
  // healthy per-component events. Reducer is now the source of truth
  // for whether ground-truth data has actually arrived.
  if (
    ev.phase === 'component' &&
    (ev.level === 'warn' || ev.level === 'error') &&
    !ev.component
  ) {
    state.phase1WatchSkipped = true
    if (ev.message) state.phase1WatchSkippedReason = ev.message
    if (time) state.clusterBootstrap.lastEventTime = time
    pushEventToBucket(state, CLUSTER_BOOTSTRAP_KEY, ev)
    return
  }

  // ── Deployment-level status that contradicts a stale skipped flag ─
  // The catalyst-api also emits coarse `phase: "deployment"` events
  // when its overall status transitions. Status `phase1-watching` /
  // `installing` both prove helmwatch is alive and observing the new
  // cluster — clear any prior `phase1WatchSkipped` so the AdminPage
  // banner doesn't shadow live progress. (We do NOT clear on `ready`,
  // `failed`, or `provisioning` — those are terminal/early phases
  // where the skipped-flag may legitimately remain set.)
  if (ev.phase === 'deployment') {
    const status = (ev as { status?: string }).status
    if (status === 'phase1-watching' || status === 'installing') {
      state.phase1WatchSkipped = false
      state.phase1WatchSkippedReason = null
    }
    if (ev.message) state.clusterBootstrap.message = ev.message
    if (time) state.clusterBootstrap.lastEventTime = time
    pushEventToBucket(state, CLUSTER_BOOTSTRAP_KEY, ev)
    return
  }

  // ── Per-Application install events (target shape) ─────────────────
  // Either `phase: component` (helmwatch's PhaseComponent) or any other
  // phase that carries `component:` matching a known Application id.
  // This is the ONLY path that can flip a card to `installed`.
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
    // CLEAR-RULE (issue #232): a real per-component event with a known
    // state is ground truth from helmwatch. If a previous event flipped
    // `phase1WatchSkipped` to true (or this is a replay where the
    // skipped event preceded the live ones), unset it now so the
    // AdminPage banner reflects the FRESH stream rather than stale
    // history. We exclude the synthetic `'skipped'` state — that's the
    // explicit "no data" marker the API emits and should keep the flag
    // set if it arrives last.
    //
    // Note: `'skipped'` is NOT in the DeploymentEvent['state'] union
    // (mapApiState returns null for it), but the API may emit it as a
    // free-form string. The check below uses string comparison so we
    // don't exclude any legitimate state by accident.
    const evStateStr = ev.state as string | undefined
    if (apiState !== null && evStateStr !== 'skipped') {
      state.phase1WatchSkipped = false
      state.phase1WatchSkippedReason = null
    }
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
    phase1WatchSkipped: base.phase1WatchSkipped,
    phase1WatchSkippedReason: base.phase1WatchSkippedReason,
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
 * Seed the per-Application card states from the durable
 * `Result.componentStates` map the catalyst-api persists when its
 * Phase-1 helmwatch terminated successfully. Keys in the map are the
 * bare slug ("cilium", "catalyst-platform"); normaliseComponentId()
 * upgrades them to the canonical "bp-<slug>" form.
 *
 * This is the happy path: the operator opens the Admin page after the
 * deployment has finished, the GET /events replay has the full state
 * map on the snapshot, and every card reads its own ground-truth
 * status without waiting on any SSE. Cards whose id is NOT present in
 * the map keep their existing reducer state — that's the truthful
 * "the helmwatch never observed this component" outcome and the
 * AdminPage banner already covers the operator-facing explanation.
 *
 * Returns a NEW state object (immutable update).
 */
export function seedComponentStates(
  base: ReducerState,
  componentStates: Record<string, string> | null | undefined,
): ReducerState {
  const next = reduceEvents(base, [])
  if (!componentStates) return next
  for (const [rawId, rawState] of Object.entries(componentStates)) {
    const id = normaliseComponentId(rawId)
    if (!id || !next.apps[id]) continue
    const mapped = mapApiState(rawState as DeploymentEvent['state'])
    if (!mapped) continue
    next.apps[id] = { ...next.apps[id]!, status: mapped }
  }
  return next
}

/**
 * Finalize the deployment view when the catalyst-api reports
 * `deployment.status === "ready"` (Phase 0 + helmwatch loop terminated).
 *
 * GROUNDING RULE — this function is NOT a "pretend everything is
 * installed" hatch. `deployment.status === "ready"` is exclusively
 * a Phase-0/control-flow signal; it says nothing per-component.
 *
 * Behaviour:
 *   • Phase banners (Hetzner-infra, Cluster-bootstrap) flip to `done`
 *     unless they already terminated `failed`. These two banners ARE
 *     bound to deployment-level status; they're allowed to converge
 *     here.
 *   • Per-Application cards do NOT auto-flip to `installed`. They
 *     either:
 *       - retain whatever per-component event already drove them, OR
 *       - get seeded from the supplied `componentStates` map (the
 *         catalyst-api's durable Result.componentStates), OR
 *       - remain `pending` if neither signal is present, in which
 *         case `phase1WatchSkipped` flips to true so the AdminPage
 *         banner explains why.
 *
 * Returns a NEW state object (immutable update).
 */
export function markAllReady(
  base: ReducerState,
  componentStates?: Record<string, string> | null,
): ReducerState {
  // Seed component states first (no-op if undefined/null/empty).
  const seeded = seedComponentStates(base, componentStates ?? null)
  const next = reduceEvents(seeded, [])
  next.hetznerInfra.status = next.hetznerInfra.status === 'failed' ? 'failed' : 'done'
  next.clusterBootstrap.status = next.clusterBootstrap.status === 'failed' ? 'failed' : 'done'
  // If we never received any per-component ground truth — neither
  // streamed events nor a durable componentStates map — surface the
  // helmwatch-unavailable banner so the operator sees the truthful
  // "we don't know, go check the cluster directly" message rather
  // than a misleading green rollup. We do NOT promote any pending
  // card to `installed` here.
  const haveAnyGroundTruth =
    (componentStates && Object.keys(componentStates).length > 0) ||
    Object.values(next.apps).some((a) => a.status !== 'pending')
  if (!haveAnyGroundTruth) {
    next.phase1WatchSkipped = true
    if (!next.phase1WatchSkippedReason) {
      next.phase1WatchSkippedReason =
        'Phase-1 install state not available — the catalyst-api could not observe per-component install state on the new Sovereign cluster (typically because the kubeconfig was not available on the catalyst-api side). Check the new cluster directly with kubectl get helmrelease -n flux-system.'
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
