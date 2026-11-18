/**
 * resource.api.test.ts — pure-function tests for the URL composers +
 * tab-validation helpers, the owner in Git and the break-glass writes.
 */

import { describe, it, expect, beforeEach, vi } from 'vitest'

import {
  ManagedByGitError,
  applyYAML,
  deleteResource,
  gitOwner,
  isValidResourceDetailTab,
  normaliseKindForRegistry,
  nsSegment,
  parseTabFromPath,
  resourceDetailHref,
  scaleResource,
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

describe('resource.api — kind normalisation', () => {
  it('normaliseKindForRegistry maps known plural kinds to singular', () => {
    expect(normaliseKindForRegistry('services')).toBe('service')
    expect(normaliseKindForRegistry('deployments')).toBe('deployment')
    expect(normaliseKindForRegistry('pods')).toBe('pod')
    expect(normaliseKindForRegistry('namespaces')).toBe('namespace')
    expect(normaliseKindForRegistry('endpointslices')).toBe('endpointslice')
  })

  it('normaliseKindForRegistry passes through canonical singular kinds', () => {
    expect(normaliseKindForRegistry('pod')).toBe('pod')
    expect(normaliseKindForRegistry('service')).toBe('service')
    expect(normaliseKindForRegistry('deployment')).toBe('deployment')
    expect(normaliseKindForRegistry('event')).toBe('event')
  })

  it('normaliseKindForRegistry lower-cases + trims input', () => {
    expect(normaliseKindForRegistry('  Services  ')).toBe('service')
    expect(normaliseKindForRegistry('POD')).toBe('pod')
  })

  it('normaliseKindForRegistry returns empty string for empty input', () => {
    expect(normaliseKindForRegistry('')).toBe('')
    expect(normaliseKindForRegistry('   ')).toBe('')
  })

  it('normaliseKindForRegistry leaves unknown kinds unchanged (lower-cased)', () => {
    // Caller is the API which already returns the `unknown-kind`
    // envelope with `availableKinds` for diagnostics.
    expect(normaliseKindForRegistry('madeupkind')).toBe('madeupkind')
    expect(normaliseKindForRegistry('SomeNewCRD')).toBe('somenewcrd')
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

describe('resource.api — owner in Git', () => {
  const meta = (metadata: Record<string, unknown>) => ({ metadata }) as unknown as Parameters<typeof gitOwner>[0]
  const helm = { 'helm.toolkit.fluxcd.io/name': 'wp' }
  const rs = { apiVersion: 'apps/v1', kind: 'ReplicaSet', name: 'wp-1' }

  it('gitOwner names the owner by the rules of the API, first match wins', () => {
    const kustomize = { 'kustomize.toolkit.fluxcd.io/name': 'apps', 'kustomize.toolkit.fluxcd.io/namespace': 'flux-system' }
    expect(gitOwner(meta({ labels: { ...kustomize, ...helm } }))).toBe('Kustomization flux-system/apps')
    expect(gitOwner(meta({ labels: { ...helm, 'helm.toolkit.fluxcd.io/namespace': 'shop' } }))).toBe('HelmRelease shop/wp')
    expect(gitOwner(meta({ labels: helm }))).toBe('HelmRelease wp')
    const release = { apiVersion: 'helm.toolkit.fluxcd.io/v2', kind: 'HelmRelease', name: 'wp' }
    expect(gitOwner(meta({ namespace: 'shop', ownerReferences: [rs, release] }))).toBe('HelmRelease shop/wp')
    expect(gitOwner(meta({ labels: { 'app.kubernetes.io/managed-by': 'Flux' } }))).toBe('Flux')
  })

  it('gitOwner is null when the detail payload names no owner in Git', () => {
    expect(gitOwner(null)).toBeNull()
    const labels = { 'app.kubernetes.io/managed-by': 'Helm', 'managed-by': 'flux' }
    const annotations = { 'catalyst.openova.io/managed-by': 'flux' }
    expect(gitOwner(meta({ labels, annotations, ownerReferences: [rs] }))).toBeNull()
  })
})

describe('resource.api — break-glass writes', () => {
  const answer = (status: number, body: unknown) => new Response(JSON.stringify(body), { status })
  beforeEach(() => {
    global.fetch = vi.fn().mockImplementation(() => Promise.resolve(answer(200, {})))
  })

  it('scale and apply send the body field reason; delete sends the query parameter reason', async () => {
    await scaleResource('dep', 'deployment', 'shop', 'wp', 2, 'load test of the shop')
    await applyYAML('dep', 'configmap', 'shop', 'banner', 'kind: ConfigMap\n', 'fix the shop banner')
    await deleteResource('dep', 'pod', 'shop', 'wp-1', 'stuck pod, see the incident')
    const calls = (global.fetch as ReturnType<typeof vi.fn>).mock.calls as [string, RequestInit][]
    expect(calls[0][0]).toContain('/v1/sovereigns/dep/k8s/deployment/shop/wp/scale')
    expect(JSON.parse(calls[0][1].body as string)).toEqual({ replicas: 2, reason: 'load test of the shop' })
    expect(calls[1][0]).toContain('/v1/sovereigns/dep/k8s/configmap/shop/banner/apply')
    expect(JSON.parse(calls[1][1].body as string)).toEqual({ yaml: 'kind: ConfigMap\n', reason: 'fix the shop banner' })
    expect(calls[2][0]).toContain('/v1/sovereigns/dep/k8s/pod/shop/wp-1?reason=stuck%20pod%2C%20see%20the%20incident')
    expect(calls[2][1].method).toBe('DELETE')
  })

  it('the answer 409 managed-by-git throws ManagedByGitError with the owner; any other refusal stays a plain error', async () => {
    global.fetch = vi.fn().mockResolvedValue(answer(409, { error: 'managed-by-git', code: '409', owner: 'Kustomization flux-system/apps' }))
    const err = await deleteResource('dep', 'pod', 'shop', 'wp-1', 'stuck pod, see the incident').catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ManagedByGitError)
    expect((err as ManagedByGitError).owner).toBe('Kustomization flux-system/apps')
    global.fetch = vi.fn().mockResolvedValue(answer(400, { error: 'break-glass-reason-required', code: '400' }))
    const other = await scaleResource('dep', 'deployment', 'shop', 'wp', 2, 'short').catch((e: unknown) => e)
    expect(other).not.toBeInstanceOf(ManagedByGitError)
    expect((other as Error).message).toContain('HTTP 400')
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
