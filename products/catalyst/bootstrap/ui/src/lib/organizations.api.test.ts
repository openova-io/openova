/**
 * organizations.api.test.ts — pure-function coverage for the
 * Organizations directory composition (issue #3378 §5 empty-state law +
 * §2.3 kind-derived defaults). The fetch-driven listOrganizations() path
 * is exercised end-to-end by the directory page test; here we lock the
 * row-mapping invariants that the empty-state and the internal-door
 * defaults depend on.
 */

import { describe, expect, it } from 'vitest'
import {
  kindDefaults,
  parentRowFromSelf,
  subOrgRowFromTenant,
} from './organizations.api'
import type { Tenant } from './bss.api'

describe('kindDefaults', () => {
  it('internal → showback + namespace (the internal door, no voucher)', () => {
    expect(kindDefaults('internal')).toEqual({
      billingMode: 'showback',
      isolation: 'namespace',
    })
  })

  it('customer → real + vcluster (the marketplace funnel door)', () => {
    expect(kindDefaults('customer')).toEqual({
      billingMode: 'real',
      isolation: 'vcluster',
    })
  })
})

describe('parentRowFromSelf', () => {
  it('marks the row as the parent with showback billing (§5 day-one)', () => {
    const row = parentRowFromSelf({
      deploymentId: 'dep-123',
      sovereignFQDN: 'hw130.omantel.biz',
    })
    expect(row.isParent).toBe(true)
    expect(row.billingMode).toBe('showback')
    expect(row.kind).toBe('internal')
    expect(row.status).toBe('active')
    expect(row.displayName).toBe('hw130.omantel.biz')
    expect(row.consoleHost).toBe('console.hw130.omantel.biz')
    expect(row.id).toBe('dep-123')
  })

  it('never renders blank even when self-discovery fails (§5 never-blank)', () => {
    const row = parentRowFromSelf(null)
    expect(row.isParent).toBe(true)
    expect(row.id).toBe('__parent__')
    expect(row.displayName).toBe('Sovereign')
    expect(row.billingMode).toBe('showback')
  })
})

describe('subOrgRowFromTenant', () => {
  const tenant: Tenant = {
    id: 'tnt-1',
    orgName: 'ACME Corp',
    consoleHost: 'console.acme.omani.homes',
    subdomain: 'acme',
    parentDomain: 'omani.homes',
    plan: 'pro',
    status: 'active',
    region: 'me-east-215-a',
    ownerEmail: 'owner@acme.example',
    createdAt: '2026-06-13T00:00:00Z',
    updatedAt: '2026-06-13T00:00:00Z',
    lastError: '',
  }

  it('maps a customer tenant to a non-parent customer row', () => {
    const row = subOrgRowFromTenant(tenant)
    expect(row.isParent).toBe(false)
    expect(row.kind).toBe('customer')
    expect(row.billingMode).toBe('real')
    expect(row.isolation).toBe('vcluster')
    expect(row.displayName).toBe('ACME Corp')
    expect(row.slug).toBe('acme')
    expect(row.ownerEmail).toBe('owner@acme.example')
    expect(row.status).toBe('active')
  })
})
