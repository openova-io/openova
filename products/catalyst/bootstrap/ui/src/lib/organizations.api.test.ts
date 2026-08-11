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
  subOrgRowFromRecord,
} from './organizations.api'
import type { OrgRecord } from './bss.api'

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

describe('subOrgRowFromRecord', () => {
  const record: OrgRecord = {
    id: 'tnt-1',
    orgName: 'ACME Corp',
    consoleHost: 'console.acme.omani.homes',
    subdomain: 'acme',
    parentDomain: 'omani.homes',
    plan: 'pro',
    planSlug: 's',
    kind: 'customer',
    tier: 'org',
    billingMode: 'real',
    isolation: 'vcluster',
    status: 'active',
    region: 'me-east-215-a',
    ownerEmail: 'owner@acme.example',
    createdAt: '2026-06-13T00:00:00Z',
    updatedAt: '2026-06-13T00:00:00Z',
    lastError: '',
  }

  it('maps a customer org to a non-parent customer row', () => {
    const row = subOrgRowFromRecord(record)
    expect(row.isParent).toBe(false)
    expect(row.kind).toBe('customer')
    expect(row.tier).toBe('org')
    expect(row.billingMode).toBe('real')
    expect(row.isolation).toBe('vcluster')
    expect(row.displayName).toBe('ACME Corp')
    expect(row.slug).toBe('acme')
    expect(row.ownerEmail).toBe('owner@acme.example')
    expect(row.status).toBe('active')
  })

  // #3378 badge-fidelity regression: an Internal org must badge Internal
  // (showback + namespace), NOT the old hardcoded customer/real/vcluster.
  it('maps an internal org to an internal/showback/namespace row', () => {
    const row = subOrgRowFromRecord({
      ...record,
      id: 'tnt-internal',
      orgName: 'Finance',
      subdomain: 'finance',
      kind: 'internal',
      tier: 'corporate',
      billingMode: 'showback',
      isolation: 'namespace',
    })
    expect(row.kind).toBe('internal')
    expect(row.tier).toBe('corporate')
    expect(row.billingMode).toBe('showback')
    expect(row.isolation).toBe('namespace')
  })

  // An internal org with a chargeback override round-trips that override
  // (the advanced-view billing-mode pick), not the kind default.
  it('honors an explicit billingMode override on an internal org', () => {
    const row = subOrgRowFromRecord({
      ...record,
      kind: 'internal',
      billingMode: 'chargeback',
      isolation: 'namespace',
    })
    expect(row.kind).toBe('internal')
    expect(row.billingMode).toBe('chargeback')
  })

  // Legacy rows that predate the B1 spec fields (empty kind/tier/billing/
  // isolation) fall back to the kind-derived customer defaults so they
  // still badge sensibly rather than rendering blanks.
  //
  // #6145 (UAT row 101) — with ONE exception, asserted below: `isolation` no
  // longer falls back. This case used to expect 'vcluster' here.
  it('falls back to customer defaults when spec fields are empty (legacy row)', () => {
    const row = subOrgRowFromRecord({
      ...record,
      kind: '',
      tier: '',
      billingMode: '',
      isolation: '',
    })
    expect(row.kind).toBe('customer')
    expect(row.tier).toBe('org')
    expect(row.billingMode).toBe('real')
  })

  // #6145 (UAT row 101). kind/tier/billingMode degrade into a milder version of
  // themselves when the feed is silent; `isolation` would invent a dedicated
  // Kubernetes control plane. On hw293 the host-namespace-backed Organization
  // `g7freea` and the really-vcluster-backed `hw293walkone` both rendered
  // `Isolation: Vcluster`, and a mapper that substitutes 'vcluster' is the
  // second way that same wrong value reaches the card.
  it('reports NO isolation when the feed does not measure one', () => {
    const row = subOrgRowFromRecord({ ...record, isolation: '' })
    expect(row.isolation).not.toBe('vcluster')
    expect(row.isolation).toBe('')
  })

  // CONTROL for the same change: a feed that DOES report a boundary is passed
  // through untouched, in both directions. Without this the assertion above is
  // satisfied by a mapper that blanks the field for everyone.
  it('passes a measured isolation through verbatim', () => {
    expect(subOrgRowFromRecord({ ...record, isolation: 'vcluster' }).isolation).toBe('vcluster')
    expect(subOrgRowFromRecord({ ...record, isolation: 'namespace' }).isolation).toBe('namespace')
    // Unrecognized values are not measurements either.
    expect(subOrgRowFromRecord({ ...record, isolation: 'bogus' }).isolation).toBe('')
  })
})
