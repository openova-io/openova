import { describe, expect, it } from 'vitest'
import type { Me } from '../api/types'
import { navFor } from './Shell'

const A = '11111111-1111-1111-1111-111111111111'
const P = '99999999-9999-9999-9999-999999999999'

function labels(me: Me): Record<string, string[]> {
  const out: Record<string, string[]> = {}
  for (const [title, items] of navFor(me)) out[title] = items.map(([, label]) => label)
  return out
}

// The sidebar follows the permissions (DESIGN.md §10.9): the Sovereign lens
// for any Sovereign binding, Access only with settings.manage; the customer
// lens never shows the Sovereign's Bill / Configure pages, and Users only to
// an owner.
describe('Shell navigation per role', () => {
  it('sovereign-admin sees everything, Access included', () => {
    const me: Me = { email: 'ops@nc.example', role: 'operator', permissions: { sovereign: ['metering.read', 'settings.manage', 'billing.collect'] }, roles: [{ role: 'sovereign-admin', scope_kind: 'sovereign' }] }
    const nav = labels(me)
    expect(nav.Analyse).toContain('Overview')
    expect(nav.Bill).toContain('Collections')
    expect(nav.Configure).toContain('Access')
    expect(nav.Configure).toContain('Customers')
  })

  it('finance-viewer sees the Sovereign pages but not Access', () => {
    const me: Me = { email: 'fin@nc.example', role: 'finance-viewer', permissions: { sovereign: ['metering.read', 'audit.read'] }, roles: [{ role: 'finance-viewer', scope_kind: 'sovereign', source: 'group:finance' }] }
    const nav = labels(me)
    expect(nav.Analyse).toContain('Cost explorer')
    expect(nav.Bill).toContain('Statements')
    expect(nav.Configure).toContain('Price books')
    expect(nav.Configure).not.toContain('Access')
  })

  // Plan → Capacity (DESIGN.md §11) is a Sovereign page readable by every
  // Sovereign principal — the viewer reads, the admin edits inside — and
  // never part of the customer lens.
  it('Plan → Capacity is on the Sovereign lens for every Sovereign role, between Analyse and Bill', () => {
    const admin: Me = { email: 'ops@nc.example', role: 'operator', permissions: { sovereign: ['metering.read', 'capacity.manage'] }, roles: [{ role: 'sovereign-admin', scope_kind: 'sovereign' }] }
    const fin: Me = { email: 'fin@nc.example', role: 'finance-viewer', permissions: { sovereign: ['metering.read'] }, roles: [{ role: 'finance-viewer', scope_kind: 'sovereign' }] }
    for (const me of [admin, fin]) {
      expect(labels(me).Plan).toEqual(['Capacity'])
      expect(navFor(me).map(([title]) => title)).toEqual(['Analyse', 'Plan', 'Bill', 'Configure'])
    }
    const owner: Me = { email: 'owner@acme.example', role: 'customer-admin', customer_id: A, permissions: { [`customer:${A}`]: ['metering.read', 'customer.self.manage'] }, roles: [{ role: 'customer-owner', scope_kind: 'customer', customer_id: A }] }
    expect(labels(owner).Plan).toBeUndefined()
    for (const group of Object.values(labels(owner))) expect(group).not.toContain('Capacity')
  })

  it('customer-owner sees its own pages, Users and Account included, and none of the operator pages', () => {
    const me: Me = { email: 'owner@acme.example', role: 'customer-admin', customer_id: A, permissions: { [`customer:${A}`]: ['metering.read', 'account.topup', 'customer.self.manage'] }, roles: [{ role: 'customer-owner', scope_kind: 'customer', customer_id: A }], scopes: [`customer:${A}`] }
    const nav = labels(me)
    expect(nav.Analyse).toEqual(['Overview', 'Cost explorer', 'Resources'])
    expect(nav.Bill).toEqual(['Statements', 'Account', 'Budgets', 'Reports'])
    expect(nav.Configure).toEqual(['Cost sources', 'Discounts', 'Users'])
    for (const group of Object.values(nav)) {
      expect(group).not.toContain('Collections')
      expect(group).not.toContain('Customers')
      expect(group).not.toContain('Price books')
      expect(group).not.toContain('Access')
    }
  })

  it('customer-viewer has no Users page', () => {
    const me: Me = { email: 'v@acme.example', role: 'customer-viewer', customer_id: A, permissions: { [`customer:${A}`]: ['metering.read'] }, roles: [{ role: 'customer-viewer', scope_kind: 'customer', customer_id: A }], scopes: [`customer:${A}`] }
    const nav = labels(me)
    expect(nav.Configure).toEqual(['Cost sources', 'Discounts'])
    expect(nav.Bill).toContain('Account')
  })

  // The partner lens (DESIGN.md §11.5): its customers, its statements, its
  // account, its margin and its users — and none of the Sovereign's pages,
  // nor a customer's.
  it('partner-owner sees its own pages only', () => {
    const me: Me = {
      email: 'ap@resell.example',
      role: 'partner-owner',
      permissions: { [`partner:${P}`]: ['metering.read', 'account.topup', 'partner.self.manage'] },
      roles: [{ role: 'partner-owner', scope_kind: 'partner', partner_id: P, partner_name: 'Resell Co', customer_ids: [A] }],
      scopes: [`partner:${P}`],
    }
    const nav = labels(me)
    expect(nav.Analyse).toEqual(['My customers'])
    expect(nav.Bill).toEqual(['Statements', 'Account', 'Margin'])
    expect(nav.Configure).toEqual(['Retail prices', 'Users'])
    for (const group of Object.values(nav)) {
      expect(group).not.toContain('Partners')
      expect(group).not.toContain('Price books')
      expect(group).not.toContain('Customers')
      expect(group).not.toContain('Collections')
      expect(group).not.toContain('Access')
    }
  })

  it('partner-viewer reads and gets neither the retail rule nor the users', () => {
    const me: Me = {
      email: 'v@resell.example',
      role: 'partner-viewer',
      permissions: { [`partner:${P}`]: ['metering.read'] },
      roles: [{ role: 'partner-viewer', scope_kind: 'partner', partner_id: P, customer_ids: [A] }],
      scopes: [`partner:${P}`],
    }
    const nav = labels(me)
    expect(nav.Analyse).toEqual(['My customers'])
    expect(nav.Bill).toEqual(['Statements', 'Account', 'Margin'])
    expect(nav.Configure).toBeUndefined()
  })

  it('a sovereign-admin keeps Partners under Configure', () => {
    const me: Me = { email: 'ops@nc.example', role: 'operator', permissions: { sovereign: ['metering.read', 'partners.manage', 'settings.manage'] }, roles: [{ role: 'sovereign-admin', scope_kind: 'sovereign' }] }
    expect(labels(me).Configure).toContain('Partners')
  })

  it('a pre-binding /me document still lands on the right lens', () => {
    expect(labels({ email: 'ops@nc.example', role: 'operator' }).Configure).toContain('Access')
    expect(labels({ email: 'adm@acme.example', role: 'customer-admin', customer_id: A }).Configure).toContain('Users')
    expect(labels({ email: 'v@acme.example', role: 'customer-viewer', customer_id: A }).Configure).not.toContain('Users')
  })
})
