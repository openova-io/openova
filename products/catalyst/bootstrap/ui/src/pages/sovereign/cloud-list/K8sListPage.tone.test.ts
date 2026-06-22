/**
 * K8sListPage.tone.test.ts — locks the k9s-style status COLOUR contract
 * for the Cloud list view (#4084).
 *
 * The per-kind list must read like k9s: an operator scans a column and
 * sees health by colour at a glance — green when ready==desired / Running /
 * Reconciled, amber when partial / Pending / Reconciling / restarts>0, red
 * when 0-ready / Failed / CrashLoop / Degraded, grey when Terminating /
 * Unknown / Completed. Colour is layered ON TOP of the existing text
 * (accessibility), so these tests assert BOTH the tone and the extracted
 * label.
 *
 * The classifiers + column builders are pure, so we exercise them directly
 * with synthetic K8s objects — no React/SSE harness required.
 */

import { describe, it, expect } from 'vitest'
import type { K8sObject } from '@/widgets/architecture-graph/useK8sCacheStream'
import {
  tonePhase,
  toneReconcile,
  toneReadyVsDesired,
  colReadyCount,
  colPodRestarts,
  colPodPhase,
  colReconcileStatus,
  colStatus,
} from './k8sColumns'

function obj(partial: Partial<K8sObject> & Record<string, unknown>): K8sObject {
  return { metadata: { name: 'x' }, ...partial } as K8sObject
}

describe('tonePhase — phase → colour', () => {
  it('healthy phases are green (ok)', () => {
    for (const p of ['Running', 'Ready', 'Active', 'Bound', 'Available'])
      expect(tonePhase(p)).toBe('ok')
  })
  it('in-progress phases are amber (warn)', () => {
    for (const p of ['Pending', 'ContainerCreating', 'Progressing'])
      expect(tonePhase(p)).toBe('warn')
  })
  it('failure phases are red (err)', () => {
    for (const p of [
      'CrashLoopBackOff',
      'Failed',
      'ImagePullBackOff',
      'ErrImagePull',
      'OOMKilled',
      'Evicted',
    ])
      expect(tonePhase(p)).toBe('err')
  })
  it('terminal/neutral phases are grey (muted)', () => {
    for (const p of ['Succeeded', 'Completed', 'Terminating', 'Unknown'])
      expect(tonePhase(p)).toBe('muted')
  })
  it('unrecognised / empty → undefined (plain text)', () => {
    expect(tonePhase('—')).toBeUndefined()
    expect(tonePhase('')).toBeUndefined()
    expect(tonePhase('SomethingCustom')).toBeUndefined()
  })
})

describe('toneReconcile — recon vocabulary → colour', () => {
  it('Reconciled green, Reconciling amber, Degraded red', () => {
    expect(toneReconcile('Reconciled')).toBe('ok')
    expect(toneReconcile('Reconciling')).toBe('warn')
    expect(toneReconcile('Degraded')).toBe('err')
  })
})

describe('toneReadyVsDesired — readiness → colour', () => {
  it('ready==desired (>0) is green', () => {
    expect(toneReadyVsDesired(3, 3)).toBe('ok')
  })
  it('partial readiness is amber', () => {
    expect(toneReadyVsDesired(1, 3)).toBe('warn')
  })
  it('zero ready while desired>0 is red', () => {
    expect(toneReadyVsDesired(0, 3)).toBe('err')
  })
  it('desired 0 is grey (nothing expected)', () => {
    expect(toneReadyVsDesired(0, 0)).toBe('muted')
  })
  it('unknown desired → undefined (plain text)', () => {
    expect(toneReadyVsDesired(2, undefined)).toBeUndefined()
  })
})

describe('colReadyCount — ready/desired column', () => {
  const col = colReadyCount('readyReplicas', 'replicas', { header: 'Ready' })

  it('renders "<ready>/<desired>" and colours by readiness', () => {
    const full = obj({ spec: { replicas: 3 }, status: { readyReplicas: 3 } })
    expect(col.extract(full)).toBe('3/3')
    expect(col.tone?.(full)).toBe('ok')

    const partial = obj({ spec: { replicas: 3 }, status: { readyReplicas: 1 } })
    expect(partial && col.extract(partial)).toBe('1/3')
    expect(col.tone?.(partial)).toBe('warn')

    const down = obj({ spec: { replicas: 3 }, status: {} })
    expect(col.extract(down)).toBe('0/3')
    expect(col.tone?.(down)).toBe('err')
  })

  it('supports desiredFrom:"status" for DaemonSets', () => {
    const ds = colReadyCount('numberReady', 'desiredNumberScheduled', {
      desiredFrom: 'status',
    })
    const o = obj({ status: { numberReady: 5, desiredNumberScheduled: 5 } })
    expect(ds.extract(o)).toBe('5/5')
    expect(ds.tone?.(o)).toBe('ok')
  })
})

describe('colPodRestarts — restart count → colour', () => {
  const col = colPodRestarts()
  it('0 restarts is grey "0"', () => {
    const o = obj({ status: { containerStatuses: [{ restartCount: 0 }] } })
    expect(col.extract(o)).toBe('0')
    expect(col.tone?.(o)).toBe('muted')
  })
  it('a few restarts are amber', () => {
    const o = obj({ status: { containerStatuses: [{ restartCount: 2 }] } })
    expect(col.extract(o)).toBe('2')
    expect(col.tone?.(o)).toBe('warn')
  })
  it('many restarts (>=5, summed across containers) are red', () => {
    const o = obj({
      status: { containerStatuses: [{ restartCount: 3 }, { restartCount: 4 }] },
    })
    expect(col.extract(o)).toBe('7')
    expect(col.tone?.(o)).toBe('err')
  })
})

describe('colPodPhase — surfaces a crash-looping container behind "Running"', () => {
  const col = colPodPhase()
  it('a Running pod with a CrashLoopBackOff container reads red', () => {
    const o = obj({
      status: {
        phase: 'Running',
        containerStatuses: [
          { state: { waiting: { reason: 'CrashLoopBackOff' } } },
        ],
      },
    })
    // The column shows the WAITING reason, not the misleading "Running".
    expect(col.extract(o)).toBe('CrashLoopBackOff')
    expect(col.tone?.(o)).toBe('err')
  })
  it('a genuinely Running pod reads green', () => {
    const o = obj({
      status: { phase: 'Running', containerStatuses: [{ ready: true, state: { running: {} } }] },
    })
    expect(col.extract(o)).toBe('Running')
    expect(col.tone?.(o)).toBe('ok')
  })
})

describe('colReconcileStatus / colStatus carry tone', () => {
  it('colReconcileStatus colours by recon state', () => {
    const col = colReconcileStatus('Status')
    const ready = obj({ status: { conditions: [{ type: 'Ready', status: 'True' }] } })
    expect(col.extract(ready)).toBe('Reconciled')
    expect(col.tone?.(ready)).toBe('ok')

    const stalled = obj({
      status: {
        conditions: [{ type: 'Ready', status: 'False', reason: 'RetriesExhausted' }],
      },
    })
    expect(col.extract(stalled)).toBe('Degraded')
    expect(col.tone?.(stalled)).toBe('err')
  })

  it('colStatus only tones when opts.tone is set', () => {
    const plain = colStatus('phase', 'Phase')
    const toned = colStatus('phase', 'Phase', { tone: true })
    const o = obj({ status: { phase: 'Running' } })
    expect(plain.tone).toBeUndefined()
    expect(toned.tone?.(o)).toBe('ok')
  })
})
