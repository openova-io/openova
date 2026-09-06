import { describe, expect, it } from 'vitest'
import { customerLens, lensFor, pageHref } from './scope'

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
