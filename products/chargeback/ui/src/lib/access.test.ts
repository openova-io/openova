import { describe, expect, it } from 'vitest'
import type { Me } from '../api/types'
import { can, canPartner, customerIds, customerScope, displayRole, homeFor, isPartner, isSovereign, partnerIds, partnerName, primaryPartnerId, roleLabel, scopeKindOf } from './access'

const A = '11111111-1111-1111-1111-111111111111'
const B = '22222222-2222-2222-2222-222222222222'

const sovereignAdmin: Me = {
  email: 'ops@nc.example',
  role: 'operator',
  roles: [{ role: 'sovereign-admin', scope_kind: 'sovereign', source: 'config' }],
  permissions: { sovereign: ['metering.read', 'rating.manage', 'customers.manage', 'billing.issue', 'billing.collect', 'account.topup', 'settings.manage', 'audit.read', 'customer.self.manage'] },
  scopes: ['sovereign'],
}
const financeViewer: Me = {
  email: 'fin@nc.example',
  role: 'finance-viewer',
  roles: [{ role: 'finance-viewer', scope_kind: 'sovereign', source: 'group:finance' }],
  permissions: { sovereign: ['audit.read', 'metering.read'] },
  scopes: ['sovereign'],
}
const owner: Me = {
  email: 'owner@acme.example',
  role: 'customer-admin',
  customer_id: A,
  roles: [{ role: 'customer-owner', scope_kind: 'customer', customer_id: A, source: 'binding' }],
  permissions: { [customerScope(A)]: ['account.topup', 'customer.self.manage', 'metering.read'] },
  scopes: [customerScope(A)],
}
const viewer: Me = {
  email: 'v@acme.example',
  role: 'customer-viewer',
  customer_id: A,
  roles: [{ role: 'customer-viewer', scope_kind: 'customer', customer_id: A, source: 'binding' }],
  permissions: { [customerScope(A)]: ['metering.read'] },
  scopes: [customerScope(A)],
}

describe('access — the console decides by permissions', () => {
  it('a Sovereign permission covers every customer', () => {
    expect(isSovereign(sovereignAdmin)).toBe(true)
    expect(can(sovereignAdmin, 'settings.manage')).toBe(true)
    expect(can(sovereignAdmin, 'billing.collect', A)).toBe(true)
    expect(can(sovereignAdmin, 'customer.self.manage', B)).toBe(true)
    expect(homeFor(sovereignAdmin)).toBe('/overview')
  })

  it('a finance-viewer reads and changes nothing', () => {
    expect(isSovereign(financeViewer)).toBe(true)
    expect(can(financeViewer, 'metering.read', A)).toBe(true)
    expect(can(financeViewer, 'billing.issue')).toBe(false)
    expect(can(financeViewer, 'settings.manage')).toBe(false)
    expect(can(financeViewer, 'rating.manage')).toBe(false)
    expect(displayRole(financeViewer)).toBe('finance-viewer')
    expect(roleLabel(displayRole(financeViewer))).toBe('Finance viewer')
  })

  it('a customer-owner holds its permissions on its own customer only', () => {
    expect(isSovereign(owner)).toBe(false)
    expect(can(owner, 'account.topup', A)).toBe(true)
    expect(can(owner, 'customer.self.manage', A)).toBe(true)
    expect(can(owner, 'metering.read', A)).toBe(true)
    expect(can(owner, 'account.topup', B)).toBe(false)
    expect(can(owner, 'account.topup')).toBe(false)
    expect(can(owner, 'billing.collect', A)).toBe(false)
    expect(can(owner, 'billing.issue', A)).toBe(false)
    expect(customerIds(owner)).toEqual([A])
    expect(homeFor(owner)).toBe('/my/overview')
    expect(roleLabel(displayRole(owner))).toBe('Owner')
  })

  it('a customer-viewer cannot top up or manage users', () => {
    expect(can(viewer, 'metering.read', A)).toBe(true)
    expect(can(viewer, 'account.topup', A)).toBe(false)
    expect(can(viewer, 'customer.self.manage', A)).toBe(false)
  })

  it('the most powerful binding names the principal', () => {
    const both: Me = {
      ...owner,
      roles: [
        { role: 'customer-owner', scope_kind: 'customer', customer_id: A },
        { role: 'finance-viewer', scope_kind: 'sovereign' },
      ],
      permissions: { ...owner.permissions, sovereign: ['metering.read', 'audit.read'] },
      scopes: ['sovereign', customerScope(A)],
    }
    expect(displayRole(both)).toBe('finance-viewer')
    expect(isSovereign(both)).toBe(true)
    expect(customerIds(both)).toEqual([A])
  })

  it('a /me document from before bindings still decides by the legacy role', () => {
    const oldOperator: Me = { email: 'ops@nc.example', role: 'operator' }
    const oldAdmin: Me = { email: 'adm@acme.example', role: 'customer-admin', customer_id: A }
    const oldViewer: Me = { email: 'v@acme.example', role: 'customer-viewer', customer_id: A }
    expect(isSovereign(oldOperator)).toBe(true)
    expect(can(oldOperator, 'settings.manage')).toBe(true)
    expect(can(oldAdmin, 'customer.self.manage', A)).toBe(true)
    expect(can(oldAdmin, 'billing.collect', A)).toBe(false)
    expect(can(oldAdmin, 'metering.read', B)).toBe(false)
    expect(can(oldViewer, 'account.topup', A)).toBe(false)
    expect(customerIds(oldAdmin)).toEqual([A])
    expect(homeFor(oldAdmin)).toBe('/my/overview')
  })

  it('nobody signed in holds nothing', () => {
    expect(can(null, 'metering.read')).toBe(false)
    expect(isSovereign(null)).toBe(false)
    expect(customerIds(null)).toEqual([])
    expect(homeFor(null)).toBe('/signin')
  })

  it('every role has one scope kind', () => {
    expect(scopeKindOf('sovereign-admin')).toBe('sovereign')
    expect(scopeKindOf('billing-operator')).toBe('sovereign')
    expect(scopeKindOf('finance-viewer')).toBe('sovereign')
    expect(scopeKindOf('partner-owner')).toBe('partner')
    expect(scopeKindOf('partner-viewer')).toBe('partner')
    expect(scopeKindOf('customer-owner')).toBe('customer')
    expect(scopeKindOf('customer-billing')).toBe('customer')
    expect(scopeKindOf('customer-viewer')).toBe('customer')
    expect(scopeKindOf('root')).toBe('')
  })
})

// The partner scope (DESIGN.md §11.5): a binding at partner:<id> covers the
// customers it expanded to, and its partner-scoped permissions are asked for
// at the partner, never at the Sovereign.
const P = '99999999-9999-9999-9999-999999999999'
const partnerOwner: Me = {
  email: 'ap@resell.example',
  role: 'partner-owner',
  customer_id: 'party-1',
  roles: [{ role: 'partner-owner', scope_kind: 'partner', partner_id: P, partner_name: 'Resell Co', customer_ids: ['party-1', A] }],
  permissions: { [`partner:${P}`]: ['metering.read', 'account.topup', 'partner.self.manage'] },
  scopes: [`partner:${P}`],
}

describe('the partner lens', () => {
  it('is a lens of its own, neither the Sovereign nor a customer', () => {
    expect(isPartner(partnerOwner)).toBe(true)
    expect(isSovereign(partnerOwner)).toBe(false)
    expect(isPartner(sovereignAdmin)).toBe(false)
    expect(homeFor(partnerOwner)).toBe('/partner/overview')
    expect(partnerIds(partnerOwner)).toEqual([P])
    expect(primaryPartnerId(partnerOwner)).toBe(P)
    expect(partnerName(partnerOwner, P)).toBe('Resell Co')
  })

  it('covers the customers it expanded to, and no others', () => {
    expect(can(partnerOwner, 'metering.read', A)).toBe(true)
    expect(can(partnerOwner, 'metering.read', B)).toBe(false)
    expect(customerIds(partnerOwner)).toContain(A)
    expect(customerIds(partnerOwner)).toContain('party-1')
    // A partner-scoped permission is not a Sovereign one.
    expect(can(partnerOwner, 'metering.read')).toBe(false)
    expect(can(partnerOwner, 'partners.manage', A)).toBe(false)
  })

  it('asks partner permissions at the partner', () => {
    expect(canPartner(partnerOwner, 'partner.self.manage', P)).toBe(true)
    expect(canPartner(partnerOwner, 'partner.self.manage', 'another')).toBe(false)
    expect(canPartner(partnerOwner, 'partners.manage', P)).toBe(false)
    // A Sovereign binding answers at every partner.
    expect(canPartner(sovereignAdmin, 'metering.read', P)).toBe(true)
    expect(canPartner(null, 'metering.read', P)).toBe(false)
  })

  it('labels the two partner roles', () => {
    expect(roleLabel('partner-owner')).toBe('Partner owner')
    expect(roleLabel('partner-viewer')).toBe('Partner viewer')
    expect(displayRole(partnerOwner)).toBe('partner-owner')
  })
})
