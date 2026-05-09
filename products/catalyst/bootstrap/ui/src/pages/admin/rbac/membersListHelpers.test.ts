/**
 * membersListHelpers.test.ts — unit coverage for U5+U6 pure helpers.
 */

import { describe, expect, it } from 'vitest'

import {
  defaultScopesForScope,
  flattenForScope,
  grantForScope,
} from './membersListHelpers'
import type { AccessMatrixResponse } from './rbac.api'

const matrix: AccessMatrixResponse = {
  users: [
    {
      id: 'alice-uuid',
      email: 'alice@acme.io',
      source: 'keycloak',
      access: {
        wordpress: { tier: 'admin', userAccessRef: 'rbac-alice-wp', scopes: [] },
      },
    },
    {
      id: 'bob-uuid',
      email: 'bob@acme.io',
      source: 'azure_ad_federated',
      access: {
        '*': { tier: 'viewer', userAccessRef: 'rbac-bob-global' },
      },
    },
    {
      id: 'carol-uuid',
      source: 'keycloak',
      access: {
        billing: { tier: 'developer', userAccessRef: 'rbac-carol-billing' },
      },
    },
  ],
  applications: ['wordpress', 'billing', '*'],
  tiers: ['viewer', 'developer', 'operator', 'admin', 'owner'],
}

describe('flattenForScope (application)', () => {
  it('returns rows for users with direct app match OR global grant', () => {
    const rows = flattenForScope(matrix, { kind: 'application', value: 'wordpress' })
    // alice (wordpress=admin) + bob (global=viewer) = 2 rows
    expect(rows).toHaveLength(2)
    expect(rows.map((r) => r.user.id).sort()).toEqual(['alice-uuid', 'bob-uuid'])
  })
  it('skips users without a matching grant or wildcard', () => {
    const rows = flattenForScope(matrix, { kind: 'application', value: 'wordpress' })
    expect(rows.find((r) => r.user.id === 'carol-uuid')).toBeUndefined()
  })
  it('returns global grant when only wildcard exists', () => {
    const rows = flattenForScope(matrix, { kind: 'application', value: 'billing' })
    // carol (billing=developer) + bob (global=viewer)
    expect(rows.map((r) => r.user.id).sort()).toEqual(['bob-uuid', 'carol-uuid'])
  })
})

describe('flattenForScope (organization)', () => {
  it('returns every user (matrix is pre-filtered server-side)', () => {
    const rows = flattenForScope(matrix, { kind: 'organization', value: 'acme' })
    expect(rows).toHaveLength(3)
  })
})

describe('grantForScope', () => {
  it('prefers direct app match over global wildcard', () => {
    const grant = grantForScope(matrix.users[0], { kind: 'application', value: 'wordpress' })
    expect(grant?.tier).toBe('admin')
  })
  it('falls back to wildcard when no direct match', () => {
    const grant = grantForScope(matrix.users[1], { kind: 'application', value: 'wordpress' })
    expect(grant?.tier).toBe('viewer')
  })
  it('returns undefined when neither direct nor wildcard match', () => {
    const grant = grantForScope(matrix.users[2], { kind: 'application', value: 'wordpress' })
    expect(grant).toBeUndefined()
  })
})

describe('null-safety (qa-loop iter-4 cluster users-page-null-map-and-open-redirect)', () => {
  // The Go zero-value `[]User` slice serializes as `null`, not `[]`.
  // The matrix client mostly normalizes, but defense in depth here:
  // flattenForScope and grantForScope must not crash on a null users
  // array or a null per-user access map.
  it('flattenForScope tolerates users: null', () => {
    const broken = {
      users: null as unknown as never,
      applications: [],
      tiers: [],
    } as unknown as AccessMatrixResponse
    expect(flattenForScope(broken, { kind: 'application', value: 'wordpress' })).toEqual([])
  })

  it('grantForScope tolerates user.access: null', () => {
    const user = {
      id: 'x',
      source: 'keycloak',
      access: null as unknown as never,
    }
    expect(grantForScope(user as never, { kind: 'application', value: 'wordpress' })).toBeUndefined()
    expect(grantForScope(user as never, { kind: 'organization', value: 'acme' })).toBeUndefined()
  })
})

describe('defaultScopesForScope', () => {
  it('returns application scope for kind=application', () => {
    const out = defaultScopesForScope({ kind: 'application', value: 'wordpress' })
    expect(out).toEqual([{ key: 'openova.io/application', value: 'wordpress' }])
  })
  it('returns org scope for kind=organization', () => {
    const out = defaultScopesForScope({ kind: 'organization', value: 'acme' })
    expect(out).toEqual([{ key: 'openova.io/org', value: 'acme' }])
  })
})
