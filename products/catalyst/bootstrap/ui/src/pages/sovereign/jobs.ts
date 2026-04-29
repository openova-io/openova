/**
 * jobs.ts — synthesises the Sovereign-provision Job model + reducer
 * adapter from the existing eventReducer.ts state.
 *
 * RATIONALE (per spec):
 *   • Phase 0 phases (`tofu-init`, `tofu-plan`, `tofu-apply`,
 *     `tofu-output`) → 1 Job each, app="infrastructure".
 *   • `flux-bootstrap` → 1 Job, app="cluster-bootstrap".
 *   • Each per-bp-* HelmRelease (= each per-Application card) → 1 Job,
 *     app=componentId (full bp- form).
 *
 * Each Job's expanded panel renders an ordered step list. For Phase 0
 * jobs, "steps" are the discrete `tofu` events captured against the
 * Hetzner-infra phase bucket — ordered chronologically — plus inferred
 * sub-steps (one per hcloud_* family seen). For per-component jobs,
 * "steps" are the chronological events captured against that
 * component's bucket in `eventsByTarget`.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), there is NO
 * hand-maintained list of jobs. Every Job is derived from the catalog
 * (`applications: ApplicationDescriptor[]`) and reducer state.
 *
 * Per #2 (never compromise), the reducer adapter doesn't lossy-collapse
 * data: the full event log is preserved on each Job's `steps` array so
 * the JobsPage expand-in-place panel can render the same order the
 * Hetzner / cluster-bootstrap / per-component bucket received.
 */

import type {
  ApplicationStatus,
  DeploymentEvent,
  PhaseStatus,
  ReducerState,
} from './eventReducer'
import {
  CLUSTER_BOOTSTRAP_BUCKET,
  HETZNER_INFRA_BUCKET,
} from './eventReducer'
import type { ApplicationDescriptor } from './applicationCatalog'

/** UI rendering bucket — same vocabulary as core/console JobsPage.svelte. */
export type JobUiStatus = 'pending' | 'running' | 'succeeded' | 'failed'

/** Ordered step inside a Job's expanded panel. */
export interface JobStep {
  /** Stable index inside the parent Job's `steps` array. */
  index: number
  /** Human-readable step title (event message or derived label). */
  name: string
  /** UI status — same vocabulary as the parent Job. */
  status: JobUiStatus
  /** ISO timestamp of the event that drove this step (or null). */
  startedAt: string | null
  /** Optional latency message (e.g. "12s") — pixel-ported from core. */
  message: string | null
}

/** Top-level Job — one row in the JobsPage vertical stack. */
export interface Job {
  /** Stable id — `infrastructure:<phase>` / `cluster-bootstrap` / `<bp-id>`. */
  id: string
  /**
   * Logical "app" attribution — drives the row's app-name link target.
   *   • `"infrastructure"` for the four tofu jobs (no app detail page).
   *   • `"cluster-bootstrap"` for the flux-bootstrap job (no app detail page).
   *   • `"bp-<slug>"` for per-component jobs — the row's app-name link
   *     navigates to that component's AppDetail page.
   */
  app: 'infrastructure' | 'cluster-bootstrap' | string
  /** Display name of the job — e.g. "Provision Hetzner network", "Install Cilium". */
  title: string
  /** UI rendering bucket. */
  status: JobUiStatus
  /** Most-recent event time across this job's events. */
  updatedAt: string | null
  /** Chronological event list — drives the expanded panel. */
  steps: JobStep[]
  /** True when the row is part of Phase 0 (no AppDetail navigation). */
  noAppLink: boolean
}

const TOFU_PHASE_LABELS: Record<string, string> = {
  'tofu-init':   'Provision Hetzner — terraform init',
  'tofu-plan':   'Provision Hetzner — terraform plan',
  'tofu-apply':  'Provision Hetzner — terraform apply',
  'tofu-output': 'Provision Hetzner — terraform output',
  'tofu':        'Provision Hetzner — runtime events',
}

/** Derive Job UI status from an Application status enum. */
function appStatusToUi(s: ApplicationStatus): JobUiStatus {
  switch (s) {
    case 'installed':  return 'succeeded'
    case 'installing': return 'running'
    case 'failed':     return 'failed'
    case 'degraded':   return 'failed'
    case 'pending':
    case 'unknown':
    default:           return 'pending'
  }
}

/** Derive Job UI status from a phase-banner state. */
function phaseStatusToUi(s: PhaseStatus): JobUiStatus {
  switch (s) {
    case 'done':    return 'succeeded'
    case 'running': return 'running'
    case 'failed':  return 'failed'
    case 'pending':
    default:        return 'pending'
  }
}

/** Build a JobStep from a DeploymentEvent at index `i`. */
function eventToStep(ev: DeploymentEvent, i: number): JobStep {
  const time = ev.time ?? null
  const status: JobUiStatus =
    ev.level === 'error' ? 'failed' :
    ev.state === 'installed' ? 'succeeded' :
    ev.state === 'failed' ? 'failed' :
    ev.state === 'pending' ? 'pending' :
    'running'
  const name = ev.message?.trim()
    ? ev.message
    : `${ev.phase}${ev.component ? ` · ${ev.component}` : ''}`
  return {
    index: i,
    name,
    status,
    startedAt: time,
    message: null,
  }
}

/**
 * Derive the full job list from the reducer state + the resolved
 * application descriptors.
 *
 *   • Job order: 4 tofu jobs (in declared phase order) →
 *     1 cluster-bootstrap job → 1 job per Application (in
 *     descriptor order — bootstrap-kit then user-selected).
 *   • Each Job's `steps` is the chronological event log for its bucket;
 *     when no events are captured yet, `steps` is empty and the row
 *     reads as `pending`.
 *
 * This function is a PURE derivation — no side effects, no caching.
 * Callers memoize on (state, applications) identity to avoid re-render.
 */
export function deriveJobs(
  state: ReducerState,
  applications: readonly ApplicationDescriptor[],
): Job[] {
  const out: Job[] = []

  // 1. Hetzner-infra Phase 0 jobs — split per declared tofu phase.
  // Filter the bucket once, partition by event.phase to surface a
  // per-phase row even if the API only emits the catch-all "tofu"
  // events for sub-steps.
  const tofuBucket = state.eventsByTarget[HETZNER_INFRA_BUCKET()] ?? []
  const tofuByPhase: Record<string, DeploymentEvent[]> = {}
  for (const ev of tofuBucket) {
    const key = ev.phase
    if (!tofuByPhase[key]) tofuByPhase[key] = []
    tofuByPhase[key]!.push(ev)
  }
  const TOFU_ORDER = ['tofu-init', 'tofu-plan', 'tofu-apply', 'tofu-output'] as const
  for (const phase of TOFU_ORDER) {
    const evs = tofuByPhase[phase] ?? []
    // Sub-steps for tofu-apply: synthesise one row per hcloud_* family
    // captured during the run so the operator can track which resource
    // family is currently being created. Synthesised steps come AFTER
    // the raw event log so chronology is preserved.
    const baseSteps = evs.map((ev, i) => eventToStep(ev, i))
    const synthSteps: JobStep[] = []
    if (phase === 'tofu-apply') {
      for (const family of state.hetznerInfra.seenResources) {
        synthSteps.push({
          index: baseSteps.length + synthSteps.length,
          name: `Create ${family}`,
          status: state.hetznerInfra.status === 'failed' ? 'failed' :
                  state.hetznerInfra.status === 'done' ? 'succeeded' : 'running',
          startedAt: state.hetznerInfra.lastEventTime,
          message: null,
        })
      }
    }
    const status: JobUiStatus =
      // Once the overall hetzner phase is done, every sub-phase is
      // implicitly done; if it's failed we cannot tell which sub-phase
      // failed without level=error in the bucket — keep `pending` for
      // sub-phases without their own events.
      state.hetznerInfra.status === 'failed' && evs.some((e) => e.level === 'error') ? 'failed' :
      state.hetznerInfra.status === 'done' ? 'succeeded' :
      evs.length > 0 ? 'running' :
      'pending'
    out.push({
      id: `infrastructure:${phase}`,
      app: 'infrastructure',
      title: TOFU_PHASE_LABELS[phase] ?? phase,
      status,
      updatedAt: state.hetznerInfra.lastEventTime,
      steps: [...baseSteps, ...synthSteps],
      noAppLink: true,
    })
  }

  // 2. Cluster bootstrap job.
  const bootstrapBucket = state.eventsByTarget[CLUSTER_BOOTSTRAP_BUCKET()] ?? []
  out.push({
    id: 'cluster-bootstrap',
    app: 'cluster-bootstrap',
    title: 'Bootstrap cluster (Flux + GitOps repo)',
    status: phaseStatusToUi(state.clusterBootstrap.status),
    updatedAt: state.clusterBootstrap.lastEventTime,
    steps: bootstrapBucket.map((ev, i) => eventToStep(ev, i)),
    noAppLink: true,
  })

  // 3. Per-Application jobs — one per descriptor, in catalog order.
  for (const app of applications) {
    const compState = state.apps[app.id]
    const compBucket = state.eventsByTarget[app.id] ?? []
    out.push({
      id: app.id,
      app: app.id,
      title: `Install ${app.title}`,
      status: compState ? appStatusToUi(compState.status) : 'pending',
      updatedAt: compState?.lastEventTime ?? null,
      steps: compBucket.map((ev, i) => eventToStep(ev, i)),
      noAppLink: false,
    })
  }

  return out
}

/**
 * Filter the global job list to those scoped to a single component.
 * Used by AppDetail's appended Jobs section — only the per-component
 * job is shown, not the Phase 0 / cluster-bootstrap rows.
 */
export function jobsForApplication(
  jobs: readonly Job[],
  applicationId: string,
): Job[] {
  return jobs.filter((j) => j.app === applicationId)
}

/**
 * Status-pill label/text mapping — pixel-ported from core/console
 * JobsPage.svelte's `statusBadge()`.
 */
export interface JobBadge {
  text: string
  classes: string
}
export function statusBadge(status: JobUiStatus): JobBadge {
  switch (status) {
    case 'succeeded': return { text: 'Succeeded', classes: 'bg-[var(--color-success)]/15 text-[var(--color-success)]' }
    case 'running':   return { text: 'Running',   classes: 'bg-[var(--color-accent)]/15 text-[var(--color-accent)]' }
    case 'failed':    return { text: 'Failed',    classes: 'bg-[var(--color-danger)]/15 text-[var(--color-danger)]' }
    case 'pending':
    default:          return { text: 'Pending',   classes: 'bg-[var(--color-warn)]/15 text-[var(--color-warn)]' }
  }
}

/** Format an ISO timestamp as HH:MM:SS — pixel-ported from JobsPage.svelte. */
export function fmtTime(ts: string | null | undefined): string {
  if (!ts) return ''
  if (ts.startsWith('0001-')) return ''
  const t = new Date(ts).getTime()
  if (!Number.isFinite(t) || t <= 0) return ''
  return new Date(t).toLocaleTimeString(undefined, {
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  })
}
