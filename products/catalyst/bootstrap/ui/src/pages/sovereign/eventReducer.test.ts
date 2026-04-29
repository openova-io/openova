/**
 * eventReducer.test.ts — vitest coverage for the pure event-folding
 * reducer that drives the Sovereign Admin shell.
 *
 * Coverage:
 *   • initial state seeds every supplied Application id at `pending`.
 *   • `tofu-*` phases drive the Hetzner-infra banner state machine.
 *   • `flux-bootstrap` drives the Cluster-bootstrap banner.
 *   • Per-component `install` / `component` events with explicit
 *     `state:` flip the matching Application's status; siblings stay
 *     `pending`.
 *   • Bare-id (no `bp-` prefix) component fields are normalised.
 *   • GROUNDING — markAllReady() does NOT promote pending Applications
 *     to installed when no componentStates map is supplied (the
 *     historical bug). It instead flips `phase1WatchSkipped=true` so
 *     the AdminPage banner renders. When componentStates IS supplied,
 *     every card seeds from it. Failed cards never get demoted.
 *   • A `phase: "component"` warn event WITHOUT a `component:` field
 *     flips `phase1WatchSkipped=true` (this is the helmwatch-skipped
 *     signal the catalyst-api emits when no kubeconfig is available).
 *   • seedComponentStates() seeds cards from a Result.componentStates
 *     map even without going through markAllReady.
 *   • computeOverallStatus aggregates correctly across the mix.
 */

import { describe, it, expect } from 'vitest'
import {
  applyEvent,
  buildInitialState,
  computeOverallStatus,
  markAllReady,
  normaliseComponentId,
  reduceEvents,
  seedComponentStates,
  type DeploymentEvent,
} from './eventReducer'

const APPS = ['bp-cilium', 'bp-cert-manager', 'bp-flux', 'bp-crossplane'] as const

describe('eventReducer — buildInitialState', () => {
  it('seeds every Application id at pending', () => {
    const s = buildInitialState(APPS)
    for (const id of APPS) {
      expect(s.apps[id]?.status).toBe('pending')
    }
    expect(s.hetznerInfra.status).toBe('pending')
    expect(s.clusterBootstrap.status).toBe('pending')
  })

  it('normalises bare ids to bp- prefix', () => {
    const s = buildInitialState(['cilium'])
    expect(s.apps['bp-cilium']).toBeTruthy()
  })
})

describe('eventReducer — tofu phases drive Hetzner-infra banner', () => {
  it('tofu-init flips banner to running', () => {
    const s = buildInitialState(APPS)
    applyEvent(s, { phase: 'tofu-init', message: 'init' })
    expect(s.hetznerInfra.status).toBe('running')
  })
  it('tofu-output flips banner to done', () => {
    const s = buildInitialState(APPS)
    applyEvent(s, { phase: 'tofu-init' })
    applyEvent(s, { phase: 'tofu-output' })
    expect(s.hetznerInfra.status).toBe('done')
  })
  it('error level flips banner to failed', () => {
    const s = buildInitialState(APPS)
    applyEvent(s, { phase: 'tofu-apply', level: 'error', message: 'apply failed' })
    expect(s.hetznerInfra.status).toBe('failed')
  })
  it('records hcloud_* resource families seen during tofu-apply', () => {
    const s = buildInitialState(APPS)
    applyEvent(s, { phase: 'tofu', message: 'hcloud_network.foo: complete' })
    applyEvent(s, { phase: 'tofu', message: 'hcloud_load_balancer.api: complete' })
    expect(s.hetznerInfra.seenResources.has('hcloud_network')).toBe(true)
    expect(s.hetznerInfra.seenResources.has('hcloud_load_balancer')).toBe(true)
    expect(s.hetznerInfra.seenResources.has('hcloud_firewall')).toBe(false)
  })
})

describe('eventReducer — flux-bootstrap drives Cluster-bootstrap banner', () => {
  it('first flux-bootstrap event flips banner to running', () => {
    const s = buildInitialState(APPS)
    applyEvent(s, { phase: 'flux-bootstrap', message: 'cloud-init bootstrap' })
    expect(s.clusterBootstrap.status).toBe('running')
    // Hetzner-infra converged to done because bootstrap can only fire after.
    expect(s.hetznerInfra.status).toBe('done')
  })
})

describe('eventReducer — per-component install events', () => {
  it('explicit state: installed flips the Application card', () => {
    const s = buildInitialState(APPS)
    applyEvent(s, { phase: 'install', component: 'bp-cilium', state: 'installed', message: 'ready' })
    expect(s.apps['bp-cilium']?.status).toBe('installed')
  })
  it('bare id is normalised', () => {
    const s = buildInitialState(APPS)
    applyEvent(s, { phase: 'install', component: 'cilium', state: 'installing' })
    expect(s.apps['bp-cilium']?.status).toBe('installing')
  })
  it('error level without explicit state flips to failed', () => {
    const s = buildInitialState(APPS)
    applyEvent(s, { phase: 'install', component: 'bp-flux', level: 'error', message: 'helm failed' })
    expect(s.apps['bp-flux']?.status).toBe('failed')
  })
  it('captures namespace + helmRelease + chartVersion when emitted', () => {
    const s = buildInitialState(APPS)
    applyEvent(s, {
      phase: 'install',
      component: 'bp-cilium',
      state: 'installing',
      namespace: 'kube-system',
      helmRelease: 'cilium',
      chartVersion: '1.17.6',
    })
    expect(s.apps['bp-cilium']?.namespace).toBe('kube-system')
    expect(s.apps['bp-cilium']?.helmRelease).toBe('cilium')
    expect(s.apps['bp-cilium']?.chartVersion).toBe('1.17.6')
  })
  it('routes events to per-component bucket', () => {
    const s = buildInitialState(APPS)
    applyEvent(s, { phase: 'install', component: 'bp-cilium', state: 'installing', message: 'a' })
    applyEvent(s, { phase: 'install', component: 'bp-cilium', state: 'installing', message: 'b' })
    expect(s.eventsByTarget['bp-cilium']?.length).toBe(2)
  })
})

describe('eventReducer — reduceEvents + markAllReady', () => {
  it('reduceEvents folds an array immutably (returns new state ref)', () => {
    const s = buildInitialState(APPS)
    const events: DeploymentEvent[] = [
      { phase: 'tofu-init' },
      { phase: 'tofu-output' },
      { phase: 'install', component: 'bp-cilium', state: 'installed' },
    ]
    const next = reduceEvents(s, events)
    expect(next).not.toBe(s)
    expect(next.hetznerInfra.status).toBe('done')
    expect(next.apps['bp-cilium']?.status).toBe('installed')
    // Original is untouched.
    expect(s.hetznerInfra.status).toBe('pending')
  })

  // GROUNDING RULE — `deployment.status === "ready"` is a Phase-0
  // signal only. markAllReady() must NOT promote pending Applications
  // to installed without a componentStates map. The previous
  // behaviour (this is the omantel bug) caused every card to flip
  // green even though every HelmRelease was actually 0/11 ready.
  it('markAllReady — components default to pending even when ready (no componentStates)', () => {
    const s = buildInitialState(APPS)
    const next = markAllReady(s)
    // Every card stays pending. None are promoted.
    for (const id of APPS) {
      expect(next.apps[id]?.status).toBe('pending')
    }
    // Phase banners ARE allowed to converge — they reflect Phase-0,
    // not per-component install state.
    expect(next.hetznerInfra.status).toBe('done')
    expect(next.clusterBootstrap.status).toBe('done')
    // The skipped banner flag is set so the AdminPage renders the
    // "per-component install monitoring is unavailable" prose.
    expect(next.phase1WatchSkipped).toBe(true)
    expect(next.phase1WatchSkippedReason).toBeTruthy()
  })

  // Per-component event with state: 'installed' flips that ONE card;
  // siblings stay pending. The `phase` value is "component" — this is
  // helmwatch.PhaseComponent, the wire string the catalyst-api emits.
  it('per-component event with state=installed flips that card; siblings stay pending', () => {
    const s = buildInitialState(APPS)
    applyEvent(s, { phase: 'component', component: 'bp-cilium', state: 'installed' })
    expect(s.apps['bp-cilium']?.status).toBe('installed')
    // Siblings are still pending — they got no event of their own.
    expect(s.apps['bp-cert-manager']?.status).toBe('pending')
    expect(s.apps['bp-flux']?.status).toBe('pending')
    expect(s.apps['bp-crossplane']?.status).toBe('pending')
  })

  // The kubeconfig-skipped warn event sets the banner flag. Format:
  // phase=='component' + level=='warn' + NO `component` field. This
  // exact shape is what phase1_watch.go emits when dep.Result.
  // Kubeconfig is empty.
  it('phase: "component" warn event without component flips phase1WatchSkipped', () => {
    const s = buildInitialState(APPS)
    applyEvent(s, {
      phase: 'component',
      level: 'warn',
      message:
        'Phase-1 watch skipped: no kubeconfig is available on the catalyst-api side. Operator must fetch the kubeconfig via SSH and re-run the deployment.',
    })
    expect(s.phase1WatchSkipped).toBe(true)
    expect(s.phase1WatchSkippedReason).toMatch(/Phase-1 watch skipped/)
    // Cards are NOT promoted by this event — they stay pending.
    for (const id of APPS) {
      expect(s.apps[id]?.status).toBe('pending')
    }
  })

  // The error variant (NewWatcher() failed to start) takes the same
  // path — same banner. The catalyst-api emits the same shape with
  // level=='error' instead of 'warn'.
  it('phase: "component" error event without component also flips phase1WatchSkipped', () => {
    const s = buildInitialState(APPS)
    applyEvent(s, {
      phase: 'component',
      level: 'error',
      message: 'Phase-1 watch could not start: build dynamic client: invalid kubeconfig',
    })
    expect(s.phase1WatchSkipped).toBe(true)
    expect(s.phase1WatchSkippedReason).toMatch(/Phase-1 watch could not start/)
  })

  // Once flipped, phase1WatchSkipped stays true even if subsequent
  // events arrive (this is the "stays set for the lifetime of the
  // deployment" guarantee — operator never sees the banner flicker
  // off after a stray event).
  it('phase1WatchSkipped is monotonic — once true, stays true', () => {
    const s = buildInitialState(APPS)
    applyEvent(s, { phase: 'component', level: 'warn', message: 'kubeconfig missing' })
    expect(s.phase1WatchSkipped).toBe(true)
    applyEvent(s, { phase: 'tofu-output' })
    applyEvent(s, { phase: 'flux-bootstrap' })
    expect(s.phase1WatchSkipped).toBe(true)
  })

  // When the durable Result.componentStates map IS populated (the
  // helmwatch happy path), seed every card directly from it.
  // Component-state values that are unknown are skipped.
  it('seedComponentStates seeds cards from a Result.componentStates map', () => {
    const s = buildInitialState(APPS)
    const next = seedComponentStates(s, {
      cilium: 'installed',
      'cert-manager': 'installing',
      flux: 'failed',
      crossplane: 'pending',
    })
    expect(next.apps['bp-cilium']?.status).toBe('installed')
    expect(next.apps['bp-cert-manager']?.status).toBe('installing')
    expect(next.apps['bp-flux']?.status).toBe('failed')
    expect(next.apps['bp-crossplane']?.status).toBe('pending')
  })

  it('seedComponentStates accepts already-prefixed bp- ids', () => {
    const s = buildInitialState(APPS)
    const next = seedComponentStates(s, { 'bp-cilium': 'installed' })
    expect(next.apps['bp-cilium']?.status).toBe('installed')
  })

  it('seedComponentStates ignores ids not in the application set', () => {
    const s = buildInitialState(APPS)
    const next = seedComponentStates(s, { 'unknown-bp': 'installed' })
    // No card was promoted; every original card stays pending.
    for (const id of APPS) {
      expect(next.apps[id]?.status).toBe('pending')
    }
  })

  // markAllReady + componentStates is the happy-path finalize call.
  // Every card seeds from the supplied map; banner does NOT show
  // (we have ground truth for at least one component).
  it('markAllReady seeds cards from componentStates and does NOT show the banner', () => {
    const s = buildInitialState(APPS)
    const next = markAllReady(s, {
      cilium: 'installed',
      'cert-manager': 'installed',
      flux: 'installed',
      crossplane: 'installed',
    })
    expect(next.apps['bp-cilium']?.status).toBe('installed')
    expect(next.apps['bp-cert-manager']?.status).toBe('installed')
    expect(next.apps['bp-flux']?.status).toBe('installed')
    expect(next.apps['bp-crossplane']?.status).toBe('installed')
    // Banner stays off because we have per-component data.
    expect(next.phase1WatchSkipped).toBe(false)
  })

  // markAllReady never demotes a `failed` card — even on the empty
  // componentStates path the failed card stays failed.
  it('markAllReady — failed cards never get demoted', () => {
    const s = buildInitialState(APPS)
    applyEvent(s, { phase: 'component', component: 'bp-cilium', state: 'failed' })
    const next = markAllReady(s)
    expect(next.apps['bp-cilium']?.status).toBe('failed')
    // Other cards stay pending (no banner-flip-to-installed fallback).
    expect(next.apps['bp-flux']?.status).toBe('pending')
    expect(next.apps['bp-cert-manager']?.status).toBe('pending')
  })
})

describe('eventReducer — computeOverallStatus', () => {
  it('returns failed when any Application failed', () => {
    const s = buildInitialState(APPS)
    applyEvent(s, { phase: 'install', component: 'bp-cilium', state: 'failed' })
    expect(computeOverallStatus(s)).toBe('failed')
  })
  it('returns installing when any Application is installing', () => {
    const s = buildInitialState(APPS)
    applyEvent(s, { phase: 'install', component: 'bp-cilium', state: 'installing' })
    expect(computeOverallStatus(s)).toBe('installing')
  })
  it('returns installed when every Application + banner are terminal', () => {
    const s = buildInitialState(APPS)
    // GROUNDING — the only way every card reaches `installed` is via
    // a real componentStates map (the helmwatch happy path) or one
    // component event per card. We use the durable map here.
    const next = markAllReady(reduceEvents(s, [
      { phase: 'tofu-output' },
      { phase: 'flux-bootstrap' },
    ]), {
      cilium: 'installed',
      'cert-manager': 'installed',
      flux: 'installed',
      crossplane: 'installed',
    })
    expect(computeOverallStatus(next)).toBe('installed')
  })
})

describe('eventReducer — normaliseComponentId', () => {
  it('handles undefined / empty / already-prefixed', () => {
    expect(normaliseComponentId(undefined)).toBeNull()
    expect(normaliseComponentId('')).toBeNull()
    expect(normaliseComponentId('bp-foo')).toBe('bp-foo')
    expect(normaliseComponentId('foo')).toBe('bp-foo')
  })
})
