/**
 * EventsPanel.test.tsx — EPIC-4 Slice R4 (#1099) — severity-coloured
 * events with regarding-object filter.
 */

import { describe, it, expect, afterEach } from 'vitest'
import { render, screen, cleanup } from '@testing-library/react'

import { EventsPanel } from './EventsPanel'
import type { K8sObject } from '@/widgets/architecture-graph/useK8sCacheStream'

afterEach(() => cleanup())

function eventObj(opts: {
  uid: string
  type: 'Normal' | 'Warning'
  reason: string
  ns: string
  name: string
  kind?: string
  ts: string
}): K8sObject {
  return {
    apiVersion: 'events.k8s.io/v1',
    kind: 'Event',
    metadata: {
      uid: opts.uid,
      name: `evt-${opts.uid}`,
      namespace: opts.ns,
      creationTimestamp: opts.ts,
    },
    type: opts.type,
    reason: opts.reason,
    note: `note-${opts.reason}`,
    regarding: {
      kind: opts.kind ?? 'Pod',
      namespace: opts.ns,
      name: opts.name,
    },
  } as unknown as K8sObject
}

describe('EventsPanel', () => {
  it('renders empty state when no matching events', () => {
    render(<EventsPanel allEvents={[]} ns="default" name="wp-1" kindCanonical="pod" />)
    expect(screen.getByTestId('events-panel-empty')).toBeTruthy()
  })

  it('filters events by regarding.namespace + name + kind', () => {
    const matching = eventObj({
      uid: 'a',
      type: 'Normal',
      reason: 'Started',
      ns: 'default',
      name: 'wp-1',
      ts: '2026-05-09T10:00:00Z',
    })
    const otherPod = eventObj({
      uid: 'b',
      type: 'Warning',
      reason: 'Failed',
      ns: 'default',
      name: 'wp-2',
      ts: '2026-05-09T10:01:00Z',
    })
    const otherNs = eventObj({
      uid: 'c',
      type: 'Warning',
      reason: 'Failed',
      ns: 'kube-system',
      name: 'wp-1',
      ts: '2026-05-09T10:02:00Z',
    })
    render(
      <EventsPanel
        allEvents={[matching, otherPod, otherNs]}
        ns="default"
        name="wp-1"
        kindCanonical="pod"
      />,
    )
    expect(screen.getByTestId('events-panel-row-Started')).toBeTruthy()
    expect(screen.queryByTestId('events-panel-row-Failed')).toBeNull()
  })

  it('sorts events by timestamp descending', () => {
    const e1 = eventObj({ uid: 'a', type: 'Normal', reason: 'First', ns: 'd', name: 'p', ts: '2026-05-09T01:00:00Z' })
    const e2 = eventObj({ uid: 'b', type: 'Normal', reason: 'Latest', ns: 'd', name: 'p', ts: '2026-05-09T05:00:00Z' })
    const e3 = eventObj({ uid: 'c', type: 'Normal', reason: 'Mid', ns: 'd', name: 'p', ts: '2026-05-09T03:00:00Z' })
    render(<EventsPanel allEvents={[e1, e2, e3]} ns="d" name="p" kindCanonical="pod" />)
    const rows = screen.getAllByTestId(/^events-panel-row-/)
    expect(rows[0].dataset.testid).toBe('events-panel-row-Latest')
    expect(rows[1].dataset.testid).toBe('events-panel-row-Mid')
    expect(rows[2].dataset.testid).toBe('events-panel-row-First')
  })

  it('colours Warning events in amber', () => {
    const warn = eventObj({
      uid: 'w',
      type: 'Warning',
      reason: 'BackOff',
      ns: 'd',
      name: 'p',
      ts: '2026-05-09T01:00:00Z',
    })
    render(<EventsPanel allEvents={[warn]} ns="d" name="p" kindCanonical="pod" />)
    const cell = screen.getByTestId('events-panel-row-BackOff').firstChild as HTMLElement
    expect(cell.className).toContain('text-amber-300')
  })
})
