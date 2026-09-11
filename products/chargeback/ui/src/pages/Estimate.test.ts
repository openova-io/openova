import { describe, expect, it } from 'vitest'
import type { PublicCatalog } from '../api/types'
import {
  addLine,
  byService,
  cartReady,
  estimateBody,
  EMBED_MESSAGE,
  heightMessage,
  isEmbed,
  lineError,
  lineForPlan,
  lineForSKU,
  priceableLines,
  removeLine,
  searchCatalog,
  shareLink,
  updateLine,
  type CartLine,
} from './Estimate'

const catalog: PublicCatalog = {
  price_book: { id: 'pb1', name: 'NC list 2026', updated_at: '2026-09-01T00:00:00Z' },
  currency: 'OMR',
  tax_rate: '0.0500',
  regions: ['me-east-215-a', 'me-east-215-b'],
  skus: [
    { sku: 'ecs.s6.large.2', service: 'ecs', unit: 'instance-hour', unit_price: '0.10000000', monthly: '73.000000', description: 'General computing 2 vCPU 4 GB' },
    { sku: 'ecs.c7.xlarge.2', service: 'ecs', unit: 'instance-hour', unit_price: '0.25000000', monthly: '182.500000' },
    { sku: 'evs.ssd.gb', service: 'evs', unit: 'gb-hour', unit_price: '0.00013699', monthly: '0.100003', description: 'SSD block storage per GB' },
  ],
  plans: [{ slug: 'm', name: 'M', sku: 'plan.m', unit: 'plan-hour', unit_price: '0.01232877', monthly: '9.000002', vcpu: 4, memory_gib: 8 }],
  payg: [{ sku: 'k8s.vcpu', unit: 'vcpu-hour', unit_price: '0.00273973', monthly: '2.000003' }],
  hours_per_month: 730,
  list_prices: true,
  notice: 'List prices.',
  generated_at: '2026-09-11T10:00:00Z',
}

const ecs = catalog.skus[0]
const evs = catalog.skus[2]
const planM = catalog.plans[0]

describe('the calculator cart', () => {
  it('adds a SKU with the default month, and a second add bumps the quantity instead of duplicating the row', () => {
    let lines = addLine([], lineForSKU(ecs))
    expect(lines).toHaveLength(1)
    expect(lines[0]).toMatchObject({ key: 'ecs.s6.large.2', kind: 'sku', sku: 'ecs.s6.large.2', quantity: '1', hours: '730', months: '1' })
    lines = addLine(lines, lineForSKU(ecs))
    expect(lines).toHaveLength(1)
    expect(lines[0].quantity).toBe('2')
    lines = addLine(lines, lineForSKU(evs))
    expect(lines.map((l) => l.key)).toEqual(['ecs.s6.large.2', 'evs.ssd.gb'])
  })

  it('adds a plan as a whole month, keyed apart from any SKU', () => {
    const lines = addLine(addLine([], lineForPlan(planM)), lineForSKU(ecs))
    expect(lines[0]).toMatchObject({ key: 'plan:m', kind: 'plan', plan: 'm', months: '1', quantity: '1' })
    expect(lines.map((l) => l.key)).toEqual(['plan:m', 'ecs.s6.large.2'])
  })

  it('updates and removes by key without touching its neighbours', () => {
    let lines = addLine(addLine([], lineForSKU(ecs)), lineForSKU(evs))
    lines = updateLine(lines, 'evs.ssd.gb', { quantity: '100' })
    expect(lines[0].quantity).toBe('1')
    expect(lines[1].quantity).toBe('100')
    expect(removeLine(lines, 'ecs.s6.large.2')).toHaveLength(1)
    expect(removeLine(lines, 'nothing')).toHaveLength(2)
  })

  it('names what is wrong with a row in the words the server would use, and never prices it meanwhile', () => {
    const line: CartLine = lineForSKU(ecs)
    expect(lineError(line)).toBe('')
    expect(lineError({ ...line, quantity: '' })).toMatch(/quantity must be more than 0/)
    expect(lineError({ ...line, quantity: '0' })).toMatch(/quantity must be more than 0/)
    expect(lineError({ ...line, quantity: '-3' })).toMatch(/quantity must be more than 0/)
    expect(lineError({ ...line, quantity: 'abc' })).toMatch(/quantity must be more than 0/)
    expect(lineError({ ...line, quantity: '1000000001' })).toMatch(/at most 1,000,000,000/)
    expect(lineError({ ...line, hours: '0' })).toMatch(/hours must be more than 0 and at most 744/)
    expect(lineError({ ...line, hours: '745' })).toMatch(/at most 744/)
    expect(lineError({ ...line, hours: '100.5' })).toBe('')
    const plan = lineForPlan(planM)
    expect(lineError(plan)).toBe('')
    expect(lineError({ ...plan, months: '0' })).toMatch(/months must be between 1 and 12/)
    expect(lineError({ ...plan, months: '13' })).toMatch(/months must be between 1 and 12/)
    expect(lineError({ ...plan, months: '1.5' })).toMatch(/months must be between 1 and 12/)
    // A half-typed row is simply not priced; the rest of the cart still is.
    const cart = updateLine(addLine(addLine([], lineForSKU(ecs)), lineForSKU(evs)), 'evs.ssd.gb', { quantity: '' })
    expect(priceableLines(cart).map((l) => l.key)).toEqual(['ecs.s6.large.2'])
    expect(cartReady(cart)).toBe(true)
    expect(cartReady([])).toBe(false)
    expect(cartReady([{ ...line, quantity: '0' }])).toBe(false)
  })

  it('builds the request the API expects: hours on an SKU line, months on a plan line, and nothing empty', () => {
    const lines = updateLine(addLine(addLine([], lineForPlan(planM)), lineForSKU(evs)), 'evs.ssd.gb', { quantity: '100', hours: '730' })
    const withMonths = updateLine(lines, 'plan:m', { months: '3' })
    expect(estimateBody(withMonths)).toEqual({
      lines: [
        { plan: 'm', quantity: '1', months: 3 },
        { sku: 'evs.ssd.gb', quantity: '100', hours_per_month: '730' },
      ],
    })
    // A plan line must never carry hours and an SKU line never months: the
    // API refuses both, so the body may not contain them.
    const body = estimateBody(withMonths, 'me-east-215-a', ' buyer@example.com ')
    expect(body.lines[0]).not.toHaveProperty('hours_per_month')
    expect(body.lines[1]).not.toHaveProperty('months')
    expect(body.region).toBe('me-east-215-a')
    expect(body.contact_email).toBe('buyer@example.com')
    // No region and no address means those keys are absent, not empty.
    expect(estimateBody(withMonths, '', '  ')).not.toHaveProperty('region')
    expect(estimateBody(withMonths, '', '  ')).not.toHaveProperty('contact_email')
    // Unpriceable rows are left out of the request entirely.
    expect(estimateBody(updateLine(withMonths, 'evs.ssd.gb', { hours: '999' })).lines).toHaveLength(1)
  })

  it('searches and groups the catalog', () => {
    expect(searchCatalog(catalog, '').map((s) => s.sku)).toHaveLength(3)
    expect(searchCatalog(catalog, 'evs').map((s) => s.sku)).toEqual(['evs.ssd.gb'])
    expect(searchCatalog(catalog, 'STORAGE').map((s) => s.sku)).toEqual(['evs.ssd.gb'])
    expect(searchCatalog(catalog, 'ecs').map((s) => s.sku)).toEqual(['ecs.s6.large.2', 'ecs.c7.xlarge.2'])
    expect(searchCatalog(catalog, 'nothing here')).toEqual([])
    expect(searchCatalog(null, 'ecs')).toEqual([])
    expect(byService(catalog.skus).map((g) => [g.service, g.skus.length])).toEqual([
      ['ecs', 2],
      ['evs', 1],
    ])
  })

  it('links a saved estimate by the URL the server gave, falling back to the id', () => {
    expect(shareLink({ id: 'e1', share_url: 'https://billing.t99.omani.works/estimate/e1' })).toBe('https://billing.t99.omani.works/estimate/e1')
    expect(shareLink({ id: 'e1' }, 'https://billing.t99.omani.works')).toBe('https://billing.t99.omani.works/estimate/e1')
  })
})

describe('embed mode', () => {
  it('is on only for embed=1 or embed=true', () => {
    expect(isEmbed('?embed=1')).toBe(true)
    expect(isEmbed('embed=1')).toBe(true)
    expect(isEmbed('?region=x&embed=true')).toBe(true)
    expect(isEmbed('?embed=0')).toBe(false)
    expect(isEmbed('?embed=')).toBe(false)
    expect(isEmbed('?embedded=1')).toBe(false)
    expect(isEmbed('')).toBe(false)
  })

  it('reports the height to the parent as a whole number of pixels', () => {
    expect(heightMessage(812.4)).toEqual({ type: EMBED_MESSAGE, height: 813 })
    expect(heightMessage(0)).toEqual({ type: EMBED_MESSAGE, height: 0 })
    expect(heightMessage(-5)).toEqual({ type: EMBED_MESSAGE, height: 0 })
    expect(EMBED_MESSAGE).toBe('openova-estimate-height')
  })
})
