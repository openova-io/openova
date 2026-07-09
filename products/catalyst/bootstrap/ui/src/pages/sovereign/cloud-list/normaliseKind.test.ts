/**
 * normaliseKind.test.ts — route/nav regression lock for #4820.
 *
 * The Cloud list surface hijacked HTTPRoutes / NetworkPolicies /
 * CiliumNetworkPolicies (+ CiliumClusterwideNetworkPolicies) to the Services
 * list: a stale D17 alias `→ services` in the router's `kind`-normaliser
 * rewrote those params at `validateSearch` time even though #3998 gave them
 * first-class CloudListKind registrations + per-kind pages. The chip counts
 * populated (they read the live SSE snapshot directly) but every per-kind nav
 * landed on Services.
 *
 * These tests assert the two invariants that keep it fixed:
 *   1. EVERY `KIND_IDS` id round-trips through `normaliseCloudKind` to itself
 *      — a first-class kind can never be shadowed by an alias.
 *   2. The four #4820 kinds specifically do NOT normalise to `services`.
 * Plus the genuine alias behaviour (case-insensitive, singular→plural,
 * no-hyphen→hyphen) the normaliser is supposed to keep.
 */

import { describe, expect, it } from 'vitest'
import { KIND_IDS, isValidKind } from './kinds'
import { CLOUD_KIND_ALIASES, normaliseCloudKind } from './normaliseKind'

describe('normaliseCloudKind', () => {
  it('round-trips EVERY valid KIND_ID to itself (no alias shadows a real kind)', () => {
    for (const id of KIND_IDS) {
      expect(normaliseCloudKind(id)).toBe(id)
      // …and the result is itself a valid kind (so CloudListView renders the
      // per-kind page instead of falling back to DEFAULT_KIND).
      expect(isValidKind(normaliseCloudKind(id))).toBe(true)
    }
  })

  it('round-trips every KIND_ID regardless of URL casing', () => {
    for (const id of KIND_IDS) {
      expect(normaliseCloudKind(id.toUpperCase())).toBe(id)
    }
  })

  // The exact #4820 regression: these were rewritten to `services`.
  it.each([
    'httproutes',
    'networkpolicies',
    'ciliumnetworkpolicies',
    'ciliumclusterwidenetworkpolicies',
  ])('does NOT hijack %s to services (#4820)', (kind) => {
    expect(normaliseCloudKind(kind)).toBe(kind)
    expect(normaliseCloudKind(kind)).not.toBe('services')
  })

  it('maps kubectl-natural singular forms to the canonical plural', () => {
    expect(normaliseCloudKind('httproute')).toBe('httproutes')
    expect(normaliseCloudKind('networkpolicy')).toBe('networkpolicies')
    expect(normaliseCloudKind('ciliumnetworkpolicy')).toBe('ciliumnetworkpolicies')
    expect(normaliseCloudKind('ciliumclusterwidenetworkpolicy')).toBe(
      'ciliumclusterwidenetworkpolicies',
    )
    expect(normaliseCloudKind('service')).toBe('services')
    expect(normaliseCloudKind('pvc')).toBe('pvcs')
    expect(normaliseCloudKind('pv')).toBe('persistentvolumes')
  })

  it('maps no-hyphen kubectl forms to the hyphenated canonical id', () => {
    expect(normaliseCloudKind('loadbalancers')).toBe('load-balancers')
    expect(normaliseCloudKind('nodepools')).toBe('node-pools')
    expect(normaliseCloudKind('workernodes')).toBe('worker-nodes')
    expect(normaliseCloudKind('storageclasses')).toBe('storage-classes')
    expect(normaliseCloudKind('dnszones')).toBe('dns-zones')
  })

  it('every alias target is itself a valid KIND_ID (no alias points nowhere)', () => {
    for (const target of Object.values(CLOUD_KIND_ALIASES)) {
      expect(isValidKind(target)).toBe(true)
    }
  })

  it('no alias KEY collides with a first-class kind id (would be dead / shadowed)', () => {
    for (const key of Object.keys(CLOUD_KIND_ALIASES)) {
      expect(isValidKind(key)).toBe(false)
    }
  })

  it('leaves an unknown kind lowercased for CloudListView to reject → default', () => {
    expect(normaliseCloudKind('FooBar')).toBe('foobar')
    expect(isValidKind(normaliseCloudKind('FooBar'))).toBe(false)
  })
})
