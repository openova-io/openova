import { describe, expect, it } from 'vitest'
import type { CostCentreLine, CostCentreReport } from '../api/types'
import {
  CODE_RULE,
  UNASSIGNED_CODE,
  UNASSIGNED_NAME,
  breakdownAgrees,
  centreLabel,
  costCentreBody,
  costCentreRuleBody,
  isUnassigned,
  ruleMatchText,
  reportMetric,
  shareOf,
  sourceText,
  validateCostCentre,
  validateCostCentreRule,
} from './costcentres'

const line = (over: Partial<CostCentreLine> = {}): CostCentreLine => ({
  code: 'ENG',
  name: 'Engineering',
  usage: '10.000000',
  list: '11.000000',
  discount: '1.000000',
  net: '10.000000',
  tax: '0.500000',
  total: '10.500000',
  ...over,
})

describe('cost-centre codes', () => {
  it('accepts what the server accepts and refuses the rest', () => {
    for (const code of ['ENG', 'cc-1001', 'a', 'R.and.D', 'eng/platform', 'ops@hq']) {
      expect(CODE_RULE.test(code), code).toBe(true)
    }
    for (const code of ['', ' ENG', 'has space', '-leading', 'a#b', 'a'.repeat(65)]) {
      expect(CODE_RULE.test(code), code).toBe(false)
    }
  })

  it('can never be the unassigned bucket, so nothing collides with it', () => {
    expect(CODE_RULE.test(UNASSIGNED_CODE)).toBe(false)
    expect(isUnassigned(UNASSIGNED_CODE)).toBe(true)
    expect(isUnassigned('ENG')).toBe(false)
    expect(centreLabel({ code: UNASSIGNED_CODE })).toBe(UNASSIGNED_NAME)
    expect(centreLabel({ code: 'ENG' })).toBe('ENG')
    expect(centreLabel({ code: 'ENG', name: 'Engineering' })).toBe('Engineering')
  })

  it('refuses a form the server would refuse, naming the field', () => {
    expect(validateCostCentre({ code: '', name: '', active: true }).code).toMatch(/code is required/i)
    expect(validateCostCentre({ code: 'has space', name: '', active: true }).code).toBeTruthy()
    expect(validateCostCentre({ code: 'ENG', name: 'x'.repeat(201), active: true }).name).toBeTruthy()
    expect(validateCostCentre({ code: ' ENG ', name: ' Engineering ', active: true })).toEqual({})
    expect(costCentreBody({ code: ' ENG ', name: ' Engineering ', active: false })).toEqual({ code: 'ENG', name: 'Engineering', active: false })
  })
})

describe('cost-centre rules', () => {
  it('needs a centre, a valid tag key and a rank in range', () => {
    const base = { cost_centre_id: 'id', tag_key: 'team', tag_value: 'platform', priority: '100' }
    expect(validateCostCentreRule(base)).toEqual({})
    expect(validateCostCentreRule({ ...base, cost_centre_id: '' }).cost_centre_id).toBeTruthy()
    expect(validateCostCentreRule({ ...base, tag_key: '' }).tag_key).toBeTruthy()
    expect(validateCostCentreRule({ ...base, tag_key: 'has space' }).tag_key).toBeTruthy()
    expect(validateCostCentreRule({ ...base, priority: '10000' }).priority).toBeTruthy()
    expect(validateCostCentreRule({ ...base, priority: '1.5' }).priority).toBeTruthy()
    expect(validateCostCentreRule({ ...base, priority: '-1' }).priority).toBeTruthy()
    // An EMPTY tag value is legal: a tag present with no value is a real tag.
    expect(validateCostCentreRule({ ...base, tag_value: '' })).toEqual({})
  })

  it('shapes the body the API takes, with the rank as a number', () => {
    expect(costCentreRuleBody({ cost_centre_id: 'id', tag_key: ' team ', tag_value: ' platform ', priority: '10' })).toEqual({
      cost_centre_id: 'id',
      tag_key: 'team',
      tag_value: 'platform',
      priority: 10,
    })
  })

  it('reads a rule out loud, including the empty value', () => {
    expect(ruleMatchText({ tag_key: 'team', tag_value: 'platform' })).toBe('team = platform')
    expect(ruleMatchText({ tag_key: 'team', tag_value: '' })).toBe('team = (empty)')
  })
})

describe('the breakdown as the page reads it', () => {
  it('gives each row its share of the total, and nothing a share of zero', () => {
    expect(shareOf(line({ total: '25.000000' }), 100)).toBe(25)
    expect(shareOf(line({ total: '25.000000' }), 0)).toBe(0)
    expect(shareOf(line({ total: '0' }), 100)).toBe(0)
  })

  it('reports the server verdict on whether the rows tie back to the invoice', () => {
    const doc = (agrees: boolean): CostCentreReport => ({
      customer_id: 'c',
      period: '2026-09',
      currency: 'OMR',
      source: 'statement',
      status: 'issued',
      lines: [line()],
      totals: { usage: '10', list: '11', discount: '1', net: '10', tax: '0.5', total: '10.5' },
      invoice: { subtotal: '10', discount_total: '1', tax: '0.5', total: '10.5', agrees },
    })
    expect(breakdownAgrees(doc(true))).toBe(true)
    expect(breakdownAgrees(doc(false))).toBe(false)
    // No invoice to disagree with: a usage-only report is never "wrong".
    expect(breakdownAgrees(null)).toBe(true)
  })

  it('says which figures a reader is looking at', () => {
    const usageOnly: CostCentreReport = {
      customer_id: 'c',
      period: '2026-09',
      currency: 'OMR',
      source: 'usage',
      lines: [],
      totals: { usage: '0', list: '0', discount: '0', net: '0', tax: '0', total: '0' },
    }
    expect(sourceText(usageOnly)).toMatch(/has not been rated yet/)
    expect(sourceText({ ...usageOnly, source: 'statement', status: 'issued' })).toMatch(/issued invoice/)
    expect(sourceText({ ...usageOnly, source: 'statement', status: 'draft' })).toMatch(/draft statement/)
    expect(sourceText(null)).toBe('')
  })
})

// A period the rating run has not confirmed has 0 in every money column. Reading
// `total` there reports nothing, and the panel rendered that as "everything is
// attributed" while its own table showed 859.620 OMR of unassigned usage —
// measured on hw307, 2026-09. Absence of measurement must not render as a
// positive claim.
describe('an unrated period measures the figure its rows actually carry', () => {
  const unrated = {
    customer_id: '9692021e-ce13-4bbd-9429-292e5f9218cd',
    period: '2026-09',
    currency: 'OMR',
    source: 'usage' as const,
    lines: [
      { code: '(unassigned)', name: 'Unassigned', usage: '859.620', list: '0', discount: '0', net: '0', tax: '0', total: '0' },
      { code: 'ENG', name: 'Engineering', usage: '158.289', list: '0', discount: '0', net: '0', tax: '0', total: '0' },
      { code: 'OPS', name: 'Operations', usage: '79.141', list: '0', discount: '0', net: '0', tax: '0', total: '0' },
    ],
    totals: { usage: '1097.050', list: '0', discount: '0', net: '0', tax: '0', total: '0' },
  }
  const rated = {
    ...unrated,
    source: 'statement' as const,
    lines: [
      { code: '(unassigned)', name: 'Unassigned', usage: '1889.378', list: '1889.378', discount: '0', net: '1889.378', tax: '82.544', total: '1971.922' },
      { code: 'ENG', name: 'Engineering', usage: '446.159', list: '446.159', discount: '0', net: '446.159', tax: '19.492', total: '465.651' },
    ],
    totals: { usage: '2335.537', list: '2335.537', discount: '0', net: '2335.537', tax: '102.036', total: '2437.573' },
  }

  it('picks usage for an unrated period and total for a rated one', () => {
    expect(reportMetric(unrated)).toBe('usage')
    expect(reportMetric(rated)).toBe('total')
    expect(reportMetric(null)).toBe('usage')
  })

  it('reports the unassigned share of an unrated period instead of zero', () => {
    const m = reportMetric(unrated)
    const share = shareOf(unrated.lines[0], 1097.05, m)
    expect(Math.round(share)).toBe(78)
    // The defect: measuring `total` on this period says nothing is unassigned.
    expect(shareOf(unrated.lines[0], 1097.05, 'total')).toBe(0)
  })

  it('still measures money once an invoice has confirmed the period', () => {
    const m = reportMetric(rated)
    expect(Math.round(shareOf(rated.lines[0], 2437.573, m))).toBe(81)
  })
})
