import { describe, expect, it } from 'vitest'
import type { Me } from '../api/types'
import { customerHref, customerLens, lensFor, pageHref } from './scope'

describe('pageHref', () => {
  it('operator lens: pages are top-level routes', () => {
    const l = lensFor({ email: 'op@x', role: 'operator' })
    expect(pageHref(l, 'explore', 'preset=mtd')).toBe('/explore?preset=mtd')
    expect(pageHref(l, 'budgets')).toBe('/budgets')
  })
  it('customer (/my) lens: pages live under /my', () => {
    const l = lensFor({ email: 'c@x', role: 'customer-admin', customer_id: 'c-1' })
    expect(pageHref(l, 'explore', '?preset=mtd')).toBe('/my/explore?preset=mtd')
    expect(pageHref(l, 'statements')).toBe('/my/statements')
  })
  it('customer-pinned lens: pages are tabs of the detail page, explore is the cost tab', () => {
    const l = customerLens('c-1')
    expect(pageHref(l, 'explore', 'preset=mtd&group_by=kind')).toBe('/customers/c-1?tab=cost&preset=mtd&group_by=kind')
    expect(pageHref(l, 'budgets')).toBe('/customers/c-1?tab=budgets')
    expect(pageHref(l, 'resources', 'q=vm-1')).toBe('/customers/c-1?tab=resources&q=vm-1')
  })
  it('customer-pinned lens: the operator-only analyses go to the operator page filtered to the customer', () => {
    const l = customerLens('c 1')
    expect(pageHref(l, 'anomalies', 'day=2026-09-03')).toBe('/anomalies?customer=c%201&day=2026-09-03')
    expect(pageHref(l, 'recommendations')).toBe('/recommendations?customer=c%201')
  })
})

// The partner lens (DESIGN.md §13.5): the Sovereign-wide cost documents,
// which the server confines to the partner's own customers, under /partner.
describe('partner lens', () => {
  const partner: Me = {
    email: 'ap@resell.example',
    role: 'partner-owner',
    // A partner reports its PARTY as customer_id; the lens must not pin to it.
    customer_id: 'party-1',
    permissions: { 'partner:p-1': ['metering.read'] },
    roles: [{ role: 'partner-owner', scope_kind: 'partner', partner_id: 'p-1', customer_ids: ['c-1', 'c-2'] }],
    scopes: ['partner:p-1'],
  }

  it('reads the Sovereign-wide cost documents, not its party’s', () => {
    const l = lensFor(partner)
    expect(l.cost('explore')).toBe('/cost/explore')
    expect(l.cost('export.csv')).toBe('/cost/export.csv')
    expect(l.resources).toBe('/resources')
    expect(l.anomalies).toBe('/anomalies')
    expect(l.recommendations).toBe('/recommendations')
    expect(l.customerId).toBeNull()
  })

  it('spans customers without being the operator', () => {
    const l = lensFor(partner)
    // crossCustomer is what offers the `customer` dimension and the Customer
    // column; `operator` is what opens the provider's own configuration.
    expect(l.crossCustomer).toBe(true)
    expect(l.operator).toBe(false)
  })

  it('pages and customer links live under /partner', () => {
    const l = lensFor(partner)
    expect(pageHref(l, 'explore', 'preset=mtd&group_by=customer')).toBe('/partner/explore?preset=mtd&group_by=customer')
    expect(pageHref(l, 'resources')).toBe('/partner/resources')
    expect(customerHref(l, 'c-1')).toBe('/partner/customers/c-1')
  })

  it('a customer principal spans nothing and keeps its own paths', () => {
    const l = lensFor({ email: 'c@x', role: 'customer-admin', customer_id: 'c-1' })
    expect(l.crossCustomer).toBe(false)
    expect(l.cost('explore')).toBe('/customers/c-1/cost/explore')
  })

  it('the operator still spans every customer', () => {
    const l = lensFor({ email: 'op@x', role: 'operator' })
    expect(l.crossCustomer).toBe(true)
    expect(l.operator).toBe(true)
    expect(customerHref(l, 'c-1')).toBe('/customers/c-1')
  })
})
