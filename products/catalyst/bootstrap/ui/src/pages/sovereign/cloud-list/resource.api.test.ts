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
