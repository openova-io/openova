/**
 * resource.api.test.ts — pure-function tests for the URL composers +
 * tab-validation helpers.
 */

import { describe, it, expect } from 'vitest'

import {
  isValidResourceDetailTab,
  nsSegment,
  parseTabFromPath,
  resourceDetailHref,
  isFluxManaged,
  isManuallyManaged,
  logsWebSocketURL,
  execWebSocketURL,
} from './resource.api'

describe('resource.api — URL composers', () => {
  it('nsSegment returns "_" for empty / undefined ns', () => {
    expect(nsSegment(undefined)).toBe('_')
    expect(nsSegment('')).toBe('_')
    expect(nsSegment('  ')).toBe('_')
    expect(nsSegment('default')).toBe('default')
    expect(nsSegment('kube-system')).toBe('kube-system')
  })

  it('resourceDetailHref composes the canonical detail URL', () => {
    expect(resourceDetailHref('/cloud', 'pod', 'default', 'wp-1')).toBe(
      '/cloud/resource/pod/default/wp-1/overview',
    )
    expect(resourceDetailHref('/cloud/', 'node', undefined, 'node-a', 'metrics')).toBe(
      '/cloud/resource/node/_/node-a/metrics',
    )
  })
})

describe('resource.api — tab parser', () => {
  it('isValidResourceDetailTab accepts canonical names', () => {
    expect(isValidResourceDetailTab('overview')).toBe(true)
    expect(isValidResourceDetailTab('tree')).toBe(true)
    expect(isValidResourceDetailTab('madeup')).toBe(false)
    expect(isValidResourceDetailTab(7)).toBe(false)
  })

  it('parseTabFromPath defaults to overview on unknown', () => {
    expect(parseTabFromPath(undefined)).toBe('overview')
    expect(parseTabFromPath('')).toBe('overview')
    expect(parseTabFromPath('madeup')).toBe('overview')
    expect(parseTabFromPath('events')).toBe('events')
  })
})

describe('resource.api — managed-by detection', () => {
  it('isFluxManaged true for label = flux', () => {
    expect(isFluxManaged({ metadata: { labels: { 'app.kubernetes.io/managed-by': 'flux' } } } as unknown as Parameters<typeof isFluxManaged>[0])).toBe(true)
    expect(isFluxManaged({ metadata: { annotations: { 'catalyst.openova.io/managed-by': 'flux' } } } as unknown as Parameters<typeof isFluxManaged>[0])).toBe(true)
  })

  it('isFluxManaged false otherwise', () => {
    expect(isFluxManaged(null)).toBe(false)
    expect(isFluxManaged({} as unknown as Parameters<typeof isFluxManaged>[0])).toBe(false)
    expect(isFluxManaged({ metadata: { labels: { foo: 'bar' } } } as unknown as Parameters<typeof isFluxManaged>[0])).toBe(false)
  })

  it('isManuallyManaged is the negation when no flux marker', () => {
    expect(isManuallyManaged({ metadata: {} } as unknown as Parameters<typeof isFluxManaged>[0])).toBe(true)
    expect(isManuallyManaged({ metadata: { annotations: { 'catalyst.openova.io/managed-by': 'manual' } } } as unknown as Parameters<typeof isFluxManaged>[0])).toBe(true)
    expect(isManuallyManaged({ metadata: { labels: { 'app.kubernetes.io/managed-by': 'flux' } } } as unknown as Parameters<typeof isFluxManaged>[0])).toBe(false)
  })
})

describe('resource.api — logs/exec WebSocket URL composers', () => {
  it('logsWebSocketURL builds path with default options', () => {
    const url = logsWebSocketURL('dep', 'default', 'wp-1', 'web')
    expect(url).toContain('/v1/sovereigns/dep/k8s/logs/default/wp-1/web')
    expect(url).toContain('follow=true')
    expect(url).toContain('tailLines=100')
  })

  it('logsWebSocketURL adds since parameter when provided', () => {
    const url = logsWebSocketURL('dep', 'default', 'wp-1', 'web', {
      tailLines: 50,
      since: '2026-05-09T12:00:00Z',
    })
    expect(url).toContain('tailLines=50')
    expect(url).toContain('since=')
    expect(url).toContain('2026-05-09T12%3A00%3A00Z')
  })

  it('execWebSocketURL encodes the command query string', () => {
    const url = execWebSocketURL('dep', 'default', 'wp-1', 'web', '/bin/bash')
    expect(url).toContain('/v1/sovereigns/dep/k8s/exec/default/wp-1/web')
    expect(url).toContain('command=%2Fbin%2Fbash')
  })
})
