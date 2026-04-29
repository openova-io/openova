/**
 * eventReducer.test.ts — vitest coverage for the pure event-folding
 * reducer that drives the Sovereign Admin shell.
 *
 * Coverage:
 *   • initial state seeds every supplied Application id at `pending`.
 *   • `tofu-*` phases drive the Hetzner-infra banner state machine.
 *   • `flux-bootstrap` drives the Cluster-bootstrap banner.
 *   • Per-component `install` events with explicit `state:` flip the
 *     matching Application's status.
 *   • Bare-id (no `bp-` prefix) component fields are normalised.
 *   • markAllReady() forces every still-pending Application to
 *     installed but never demotes failures.
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

  it('markAllReady forces every pending Application to installed', () => {
    const s = buildInitialState(APPS)
    applyEvent(s, { phase: 'install', component: 'bp-cilium', state: 'failed' })
    const next = markAllReady(s)
    // Failed Applications stay failed.
    expect(next.apps['bp-cilium']?.status).toBe('failed')
    // Pending Applications flip to installed.
    expect(next.apps['bp-flux']?.status).toBe('installed')
    expect(next.apps['bp-cert-manager']?.status).toBe('installed')
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
    const next = markAllReady(reduceEvents(s, [
      { phase: 'tofu-output' },
      { phase: 'flux-bootstrap' },
    ]))
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
