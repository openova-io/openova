import { describe, expect, it } from 'vitest'
import { explorerHref, resourceHref, resourcesHref } from './links'
import { customerLens, lensFor } from './scope'

const operator = lensFor({ email: 'op@example.test', role: 'operator' })
const customer = lensFor({ email: 'c@example.test', role: 'customer-admin', customer_id: 'c-9' })
const pinned = customerLens('c-9')

describe('lens-aware links', () => {
  it('explorer: page for the operator and the customer, the detail tab when pinned', () => {
    expect(explorerHref(operator, 'preset=30d')).toBe('/explore?preset=30d')
    expect(explorerHref(customer, 'preset=30d')).toBe('/my/explore?preset=30d')
    expect(explorerHref(pinned, 'preset=30d')).toBe('/customers/c-9?tab=explore&preset=30d')
    expect(explorerHref(operator)).toBe('/explore')
    expect(explorerHref(pinned)).toBe('/customers/c-9?tab=explore')
  })
  it('accepts URLSearchParams', () => {
    expect(explorerHref(customer, new URLSearchParams({ group_by: 'sku', kind: 'ecs' }))).toBe('/my/explore?group_by=sku&kind=ecs')
  })
  it('resources list follows the same rule', () => {
    expect(resourcesHref(operator, 'q=abc')).toBe('/resources?q=abc')
    expect(resourcesHref(customer, 'q=abc')).toBe('/my/resources?q=abc')
    expect(resourcesHref(pinned, 'q=abc')).toBe('/customers/c-9?tab=resources&q=abc')
  })
  it('resource detail: every operator lens opens the operator page, a customer its own', () => {
    expect(resourceHref(operator, 's-1', 'r-1')).toBe('/resources/s-1/r-1')
    expect(resourceHref(pinned, 's-1', 'r-1')).toBe('/resources/s-1/r-1')
    expect(resourceHref(customer, 's-1', 'r-1')).toBe('/my/resources/s-1/r-1')
  })
  it('encodes ids that carry path characters', () => {
    expect(resourceHref(operator, 's-1', 'ns/pod-a')).toBe('/resources/s-1/ns%2Fpod-a')
  })
})
