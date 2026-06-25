/**
 * useProvisioningStream.test.ts — lock-in for issue #3914.
 *
 * The regression this guards: the JobsPage "Bootstrapping cluster" timeline
 * was wired (PR #4221) but `applyEvent` keyed purely on `ev.phase`. The
 * catalyst-api emits every Phase-1 (bootstrap-kit) transition as
 * `phase: "component"` with the REAL component id in `ev.component`
 * (`cilium`, `cert-manager`, … via ComponentIDFromHelmRelease) plus an
 * `ev.state` (`pending|installing|installed|degraded|failed`). The old hook
 * never read `component`/`state`, so all 11 components collapsed into the
 * `tofu-apply` fallback bucket — the bootstrap-kit rows (the ACTUAL ~30-min
 * void) stayed grey/pending forever.
 *
 * These tests drive the pure `applyEvent` reducer with the exact wire shape
 * the backend serialises (see provisioner.Event +
 * helmwatch.PhaseComponent) and assert each component's BootstrapPhase row
 * lights up independently.
 */

import { describe, it, expect } from 'vitest'
import {
  applyEvent,
  emptyPhaseMap,
  resolvePhaseId,
  type ProvisioningEvent,
} from './useProvisioningStream'

function ev(partial: Partial<ProvisioningEvent>): ProvisioningEvent {
  return {
    time: '2026-06-25T00:00:00Z',
    phase: 'component',
    level: 'info',
    message: '',
    ...partial,
  }
}

describe('resolvePhaseId — Phase-1 events route by component id (#3914)', () => {
  it('maps phase:"component" to the component id, not the literal "component"', () => {
    expect(resolvePhaseId(ev({ phase: 'component', component: 'cilium' }))).toBe('cilium')
  })

  it('aliases the umbrella component id to its bootstrap-kit phase id', () => {
    // ComponentIDFromHelmRelease("bp-catalyst-platform") → "catalyst-platform",
    // but the BootstrapPhase.id is "bp-catalyst-platform".
    expect(resolvePhaseId(ev({ phase: 'component', component: 'catalyst-platform' }))).toBe(
      'bp-catalyst-platform',
    )
  })

  it('passes Phase-0 OpenTofu phase ids through unchanged', () => {
    expect(resolvePhaseId(ev({ phase: 'tofu-apply', component: undefined }))).toBe('tofu-apply')
  })

  it('routes component-log lines by component id too', () => {
    expect(resolvePhaseId(ev({ phase: 'component-log', component: 'keycloak' }))).toBe('keycloak')
  })
})

describe('applyEvent — bootstrap-kit rows light up independently (#3914)', () => {
  it('flips the matching bootstrap-kit phase to running on installing, not tofu-apply', () => {
    let m = emptyPhaseMap()
    m = applyEvent(m, ev({ phase: 'component', component: 'cilium', state: 'installing' }))
    expect(m['cilium']!.status).toBe('running')
    // The regression symptom: cilium's event must NOT have landed on tofu-apply.
    expect(m['tofu-apply']!.status).toBe('pending')
    // Sibling components stay pending.
    expect(m['cert-manager']!.status).toBe('pending')
    expect(m['bp-catalyst-platform']!.status).toBe('pending')
  })

  it('flips a component to done on state:"installed"', () => {
    let m = emptyPhaseMap()
    m = applyEvent(m, ev({ phase: 'component', component: 'cert-manager', state: 'installing' }))
    expect(m['cert-manager']!.status).toBe('running')
    m = applyEvent(m, ev({ phase: 'component', component: 'cert-manager', state: 'installed' }))
    expect(m['cert-manager']!.status).toBe('done')
    expect(m['cert-manager']!.endedAt).toBe('2026-06-25T00:00:00Z')
  })

  it('flips a component to failed on state:"failed" or "degraded"', () => {
    let m = emptyPhaseMap()
    m = applyEvent(m, ev({ phase: 'component', component: 'openbao', state: 'failed' }))
    expect(m['openbao']!.status).toBe('failed')

    let n = emptyPhaseMap()
    n = applyEvent(n, ev({ phase: 'component', component: 'keycloak', state: 'degraded' }))
    expect(n['keycloak']!.status).toBe('failed')
  })

  it('drives the umbrella row via its catalyst-platform component id', () => {
    let m = emptyPhaseMap()
    m = applyEvent(
      m,
      ev({ phase: 'component', component: 'catalyst-platform', state: 'installing' }),
    )
    expect(m['bp-catalyst-platform']!.status).toBe('running')
    m = applyEvent(
      m,
      ev({ phase: 'component', component: 'catalyst-platform', state: 'installed' }),
    )
    expect(m['bp-catalyst-platform']!.status).toBe('done')
  })

  it('a component-log line keeps a running component running + updates the tail', () => {
    let m = emptyPhaseMap()
    m = applyEvent(m, ev({ phase: 'component', component: 'cilium', state: 'installing' }))
    m = applyEvent(
      m,
      ev({ phase: 'component-log', component: 'cilium', message: 'cilium pod pulling image' }),
    )
    expect(m['cilium']!.status).toBe('running')
    expect(m['cilium']!.lastEvent?.message).toBe('cilium pod pulling image')
  })

  it('a trailing component-log line does NOT revive a finished component', () => {
    let m = emptyPhaseMap()
    m = applyEvent(m, ev({ phase: 'component', component: 'flux', state: 'installed' }))
    expect(m['flux']!.status).toBe('done')
    m = applyEvent(
      m,
      ev({ phase: 'component-log', component: 'flux', message: 'late log line' }),
    )
    // Sticky: still done, not flipped back to running.
    expect(m['flux']!.status).toBe('done')
  })

  it('the backend CAN re-flip a failed HR back through installing→installed', () => {
    // helm-controller re-pin path: failed → installing → installed.
    let m = emptyPhaseMap()
    m = applyEvent(m, ev({ phase: 'component', component: 'gitea', state: 'failed' }))
    expect(m['gitea']!.status).toBe('failed')
    m = applyEvent(m, ev({ phase: 'component', component: 'gitea', state: 'installing' }))
    expect(m['gitea']!.status).toBe('running')
    m = applyEvent(m, ev({ phase: 'component', component: 'gitea', state: 'installed' }))
    expect(m['gitea']!.status).toBe('done')
  })

  it('Phase-0 OpenTofu events still drive their own rows (no regression)', () => {
    let m = emptyPhaseMap()
    m = applyEvent(m, ev({ phase: 'tofu-init', level: 'info', component: undefined, state: undefined }))
    expect(m['tofu-init']!.status).toBe('running')
    m = applyEvent(m, ev({ phase: 'tofu-apply', level: 'info', component: undefined, state: undefined }))
    // Phase boundary inference closes the earlier running tofu phase.
    expect(m['tofu-init']!.status).toBe('done')
    expect(m['tofu-apply']!.status).toBe('running')
    // A level:error on a Phase-0 event still fails that phase.
    m = applyEvent(m, ev({ phase: 'tofu-apply', level: 'error', message: 'boom' }))
    expect(m['tofu-apply']!.status).toBe('failed')
  })

  it('a full Phase-1 sweep converges every bootstrap-kit row to done', () => {
    const KIT = [
      'cilium', 'cert-manager', 'flux', 'crossplane', 'sealed-secrets',
      'spire', 'jetstream', 'openbao', 'keycloak', 'gitea',
    ]
    let m = emptyPhaseMap()
    for (const c of KIT) {
      m = applyEvent(m, ev({ phase: 'component', component: c, state: 'installing' }))
    }
    for (const c of KIT) {
      m = applyEvent(m, ev({ phase: 'component', component: c, state: 'installed' }))
    }
    m = applyEvent(m, ev({ phase: 'component', component: 'catalyst-platform', state: 'installed' }))
    for (const c of KIT) {
      expect(m[c]!.status).toBe('done')
    }
    expect(m['bp-catalyst-platform']!.status).toBe('done')
  })
})
