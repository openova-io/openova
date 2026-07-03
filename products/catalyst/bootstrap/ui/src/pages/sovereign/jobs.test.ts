/**
 * jobs.test.ts — coverage for `deriveJobs()` + helpers.
 *
 * What we lock in:
 *   • Phase 0: 4 tofu jobs derived from `tofu-init`/`tofu-plan`/
 *     `tofu-apply`/`tofu-output` events, each with `app="infrastructure"`
 *     and `noAppLink=true`.
 *   • `flux-bootstrap` → exactly 1 job, `app="cluster-bootstrap"`,
 *     `noAppLink=true`.
 *   • Per-Application: 1 job per descriptor, `app=<bp-id>`,
 *     `noAppLink=false` (so the AppDetail link renders in JobCard).
 *   • Step ordering matches the order events were applied.
 *   • `jobsForApplication()` filters to the single per-component row,
 *     excluding Phase 0 / cluster-bootstrap rows.
 *   • `statusBadge()` text + class mapping for every JobUiStatus.
 */

import { describe, it, expect } from 'vitest'
import {
  applyEvent,
  buildInitialState,
  markFailedTerminal,
  type DeploymentEvent,
} from './eventReducer'
import { deriveJobs, fmtTime, jobsForApplication, statusBadge } from './jobs'
import type { ApplicationDescriptor } from './applicationCatalog'

const APPS: ApplicationDescriptor[] = [
  {
    id: 'bp-cilium',
    bareId: 'cilium',
    title: 'Cilium',
    description: 'eBPF networking',
    familyId: 'spine',
    familyName: 'Spine',
    tier: 'mandatory',
    logoUrl: null,
    dependencies: [],
    bootstrapKit: true,
  },
  {
    id: 'bp-flux',
    bareId: 'flux',
    title: 'Flux',
    description: 'GitOps',
    familyId: 'spine',
    familyName: 'Spine',
    tier: 'mandatory',
    logoUrl: null,
    dependencies: [],
    bootstrapKit: true,
  },
]

function feed(events: DeploymentEvent[]) {
  const state = buildInitialState(APPS.map((a) => a.id))
  for (const ev of events) applyEvent(state, ev)
  return state
}

describe('jobs — deriveJobs', () => {
  it('derives 4 Phase 0 tofu jobs + 1 cluster-bootstrap job + N per-component jobs', () => {
    const state = feed([])
    const jobs = deriveJobs(state, APPS)
    // 4 tofu phases + 1 bootstrap + 2 components
    expect(jobs.length).toBe(4 + 1 + APPS.length)
  })

  it('marks Phase 0 jobs with app="infrastructure" and noAppLink=true', () => {
    const state = feed([])
    const jobs = deriveJobs(state, APPS)
    const tofuJobs = jobs.filter((j) => j.app === 'infrastructure')
    expect(tofuJobs.length).toBe(4)
    for (const j of tofuJobs) {
      expect(j.noAppLink).toBe(true)
      expect(j.id.startsWith('infrastructure:')).toBe(true)
    }
  })

  it('marks the bootstrap job with app="cluster-bootstrap" and noAppLink=true', () => {
    const state = feed([])
    const jobs = deriveJobs(state, APPS)
    const bootstrap = jobs.find((j) => j.app === 'cluster-bootstrap')
    expect(bootstrap).toBeDefined()
    expect(bootstrap!.noAppLink).toBe(true)
  })

  it('marks per-component jobs with app=<bp-id> and noAppLink=false', () => {
    const state = feed([])
    const jobs = deriveJobs(state, APPS)
    const cilium = jobs.find((j) => j.id === 'bp-cilium')
    expect(cilium).toBeDefined()
    expect(cilium!.app).toBe('bp-cilium')
    expect(cilium!.noAppLink).toBe(false)
  })

  it('per-component job flips to running when an installing event arrives', () => {
    const state = feed([
      { phase: 'component', component: 'bp-cilium', state: 'installing', time: '2026-04-29T10:00:00Z' },
    ])
    const jobs = deriveJobs(state, APPS)
    const cilium = jobs.find((j) => j.id === 'bp-cilium')!
    expect(cilium.status).toBe('running')
  })

  it('per-component job flips to succeeded when an installed event arrives', () => {
    const state = feed([
      { phase: 'component', component: 'bp-cilium', state: 'installed', time: '2026-04-29T10:01:00Z' },
    ])
    const jobs = deriveJobs(state, APPS)
    const cilium = jobs.find((j) => j.id === 'bp-cilium')!
    expect(cilium.status).toBe('succeeded')
  })

  it('per-component job flips to failed when level=error', () => {
    const state = feed([
      { phase: 'component', component: 'bp-cilium', level: 'error', message: 'helm install failed' },
    ])
    const jobs = deriveJobs(state, APPS)
    const cilium = jobs.find((j) => j.id === 'bp-cilium')!
    expect(cilium.status).toBe('failed')
  })

  it('hetzner phase running propagates to tofu-apply job status', () => {
    const state = feed([
      { phase: 'tofu-apply', message: 'creating hcloud_network', time: '2026-04-29T10:00:00Z' },
    ])
    const jobs = deriveJobs(state, APPS)
    const apply = jobs.find((j) => j.id === 'infrastructure:tofu-apply')!
    expect(apply.status).toBe('running')
    expect(apply.steps.length).toBeGreaterThanOrEqual(1)
    expect(apply.steps[0]!.name).toContain('hcloud_network')
  })

  it('synthesises a sub-step for each hcloud_* family seen during tofu-apply', () => {
    const state = feed([
      { phase: 'tofu-apply', message: 'hcloud_network.this: Creation complete', time: '2026-04-29T10:00:00Z' },
      { phase: 'tofu', message: 'hcloud_server.cp: Creating', time: '2026-04-29T10:01:00Z' },
      { phase: 'tofu-output', message: 'output ready', time: '2026-04-29T10:02:00Z' },
    ])
    const jobs = deriveJobs(state, APPS)
    const apply = jobs.find((j) => j.id === 'infrastructure:tofu-apply')!
    // Synth steps come AFTER raw events. With state=done they read as succeeded.
    const synthNames = apply.steps.map((s) => s.name).filter((n) => n.startsWith('Create '))
    expect(synthNames.length).toBeGreaterThanOrEqual(1)
  })

  it('cluster-bootstrap job carries flux-bootstrap events as steps', () => {
    const state = feed([
      { phase: 'flux-bootstrap', message: 'cloning repo', time: '2026-04-29T10:00:00Z' },
      { phase: 'flux-bootstrap', message: 'applying manifests', time: '2026-04-29T10:00:30Z' },
    ])
    const jobs = deriveJobs(state, APPS)
    const bootstrap = jobs.find((j) => j.app === 'cluster-bootstrap')!
    expect(bootstrap.steps.length).toBeGreaterThanOrEqual(2)
    expect(bootstrap.steps.map((s) => s.name)).toContain('cloning repo')
  })

  /**
   * Issue #519 regression — reproduces the otech17 deployment
   * 6b17518f12d529ea timeline at 2026-05-02 04:50 UTC:
   *
   *   • Tofu emits 200+ events in the first 10 seconds of tofu-apply,
   *     overflowing the catalyst-api producer channel (cap 256). The
   *     `tofu-output` and `flux-bootstrap` events are dropped.
   *   • Cloud-init on the new control plane finishes; PUTs kubeconfig
   *     back. catalyst-api's PutKubeconfig launches runPhase1Watch.
   *   • Phase-1 watch runs 60 minutes, sees zero HelmReleases, returns
   *     OutcomeFluxNotReconciling.
   *   • markPhase1Done writes Status="failed", phase1Outcome="flux-not-
   *     reconciling".
   *   • The wizard's `useDeploymentEvents` hook reads the GET /events
   *     terminal snapshot and (pre-fix) bailed early — never converging
   *     the Phase-0 banner. Result: "Phase 0 — Infrastructure" jobs
   *     stuck Running for 2h+ on a deployment that finished an hour ago.
   *
   * Post-fix: useDeploymentEvents calls markFailedTerminal on the
   * failed path, which uses the durable phase1Outcome ground truth to
   * converge the Hetzner-infra banner to done. The 4 tofu jobs then
   * derive to "succeeded" via jobs.ts.
   */
  it('issue #519: tofu-output dropped + Phase-1 timeout — markFailedTerminal flips Phase-0 jobs to succeeded', () => {
    // Produce events that LACK tofu-output (the regression scenario).
    const baseState = feed([
      { phase: 'tofu-init', time: '2026-05-02T02:08:14Z' },
      { phase: 'tofu-plan', time: '2026-05-02T02:08:15Z' },
      { phase: 'tofu-apply', time: '2026-05-02T02:08:18Z' },
      { phase: 'tofu', message: 'hcloud_network.main: Creating', time: '2026-05-02T02:08:19Z' },
      { phase: 'tofu', message: 'hcloud_load_balancer.main: Creating', time: '2026-05-02T02:08:19Z' },
      // … 230+ more tofu events here in production; we don't replay
      // them all because the regression is about the MISSING events,
      // not the captured ones. tofu-output / flux-bootstrap dropped.
    ])

    // Pre-fix: Phase-0 jobs are stuck in "running" / "pending".
    const preFix = deriveJobs(baseState, APPS)
    const preApplyJob = preFix.find((j) => j.id === 'infrastructure:tofu-apply')!
    const preOutputJob = preFix.find((j) => j.id === 'infrastructure:tofu-output')!
    expect(preApplyJob.status).toBe('running')
    expect(preOutputJob.status).toBe('pending')

    // Post-fix: markFailedTerminal converges Phase-0 banner using the
    // helmwatch outcome (any outcome != '' proves Phase 0 finished).
    const postState = markFailedTerminal(baseState, 'flux-not-reconciling', null)
    const postFix = deriveJobs(postState, APPS)
    for (const phase of ['tofu-init', 'tofu-plan', 'tofu-apply', 'tofu-output']) {
      const job = postFix.find((j) => j.id === `infrastructure:${phase}`)!
      expect(job.status).toBe('succeeded')
    }
    // cluster-bootstrap surfaces the Phase-1 failure so the operator's
    // eye lands on the actual failing phase.
    const bootstrap = postFix.find((j) => j.app === 'cluster-bootstrap')!
    expect(bootstrap.status).toBe('failed')
  })
})

describe('jobs — jobsForApplication', () => {
  it('filters to a single per-component row and excludes Phase 0 / bootstrap', () => {
    const state = feed([])
    const jobs = deriveJobs(state, APPS)
    const ciliumOnly = jobsForApplication(jobs, 'bp-cilium')
    expect(ciliumOnly.length).toBe(1)
    expect(ciliumOnly[0]!.id).toBe('bp-cilium')
  })

  it('returns empty for a component not in the descriptor list', () => {
    const state = feed([])
    const jobs = deriveJobs(state, APPS)
    expect(jobsForApplication(jobs, 'bp-nonexistent')).toEqual([])
  })
})

describe('jobs — statusBadge', () => {
  it('maps every JobUiStatus to a label + class', () => {
    expect(statusBadge('succeeded').text).toBe('Succeeded')
    expect(statusBadge('running').text).toBe('Running')
    expect(statusBadge('failed').text).toBe('Failed')
    expect(statusBadge('pending').text).toBe('Pending')
  })

  it('badge classes carry the canonical color tokens', () => {
    // #4704 follow-up — semantic contract: success=green, running=BLUE
    // (in-progress must be visibly distinct), failed=red, pending=GREY
    // (never amber — amber is reserved for genuine warning states).
    expect(statusBadge('succeeded').classes).toContain('var(--color-success)')
    expect(statusBadge('running').classes).toContain('var(--color-accent)')
    expect(statusBadge('failed').classes).toContain('var(--color-danger)')
    expect(statusBadge('pending').classes).toContain('var(--color-text-dim)')
    expect(statusBadge('pending').classes).not.toContain('var(--color-warn)')
  })
})

describe('jobs — fmtTime', () => {
  it('formats valid timestamps and rejects placeholder zero-value', () => {
    expect(fmtTime(null)).toBe('')
    expect(fmtTime(undefined)).toBe('')
    expect(fmtTime('0001-01-01T00:00:00Z')).toBe('')
    // A real timestamp produces a non-empty string in any locale.
    const fmt = fmtTime('2026-04-29T10:00:00Z')
    expect(fmt.length).toBeGreaterThan(0)
  })
})
