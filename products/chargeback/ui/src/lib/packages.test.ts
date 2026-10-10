import { describe, expect, it } from 'vitest'
import type { Feature, PackagesDoc } from '../api/types'
import { addonLines, cellOf, cellText, includedFeatures, includedFromText, includesLabel, includesRows, includesValue, matrixRows, optionalFeatures, planName, stateLabel, stateTone } from './packages'

/** The packages document as GET /public/packages answers it for the showcase book. */
export const doc: PackagesDoc = {
  currency: 'OMR',
  price_book: 'OpenOva plans',
  prices_as_of: '2026-09-11',
  packages: [
    { sku: 'plan.s', name: 'S', price_month: '5.000', includes: { vcpu: 2, memory_gb: 4, bandwidth_mbps: 50 } },
    { sku: 'plan.m', name: 'M', price_month: '9.000', includes: { vcpu: 4, memory_gb: 8, bandwidth_mbps: 100 } },
    { sku: 'plan.l', name: 'L', price_month: '16.000', includes: { vcpu: 8, memory_gb: 16, bandwidth_mbps: 250 } },
    { sku: 'plan.xl', name: 'XL', price_month: '30.000', includes: { vcpu: 16, memory_gb: 32, bandwidth_mbps: 1000 } },
  ],
  features: [
    {
      key: 'ssl',
      name: 'Unlimited free SSL',
      kind: 'boolean',
      cells: { 'plan.s': { state: 'included' }, 'plan.m': { state: 'included' }, 'plan.l': { state: 'included' }, 'plan.xl': { state: 'included' } },
    },
    {
      key: 'backup',
      name: 'Backup',
      blurb: 'Daily backups of your sites and databases, kept 30 days',
      kind: 'boolean',
      cells: {
        'plan.s': { state: 'optional', addon_sku: 'addon.backup', price_month: '1.500', included_from: 'plan.xl' },
        'plan.m': { state: 'optional', addon_sku: 'addon.backup', price_month: '1.500', included_from: 'plan.xl' },
        'plan.l': { state: 'optional', addon_sku: 'addon.backup', price_month: '1.500', included_from: 'plan.xl' },
        'plan.xl': { state: 'included' },
      },
    },
    {
      key: 'dedicated_ip',
      name: 'Dedicated IP address',
      kind: 'boolean',
      cells: {
        'plan.s': { state: 'optional', addon_sku: 'addon.dedicated_ip', price_month: '2.000' },
        'plan.m': { state: 'optional', addon_sku: 'addon.dedicated_ip', price_month: '2.000' },
        'plan.l': { state: 'not_offered' },
        'plan.xl': { state: 'optional', addon_sku: 'addon.dedicated_ip', price_month: '2.000' },
      },
    },
    {
      key: 'bandwidth',
      name: 'Bandwidth',
      kind: 'quantity',
      unit: 'Mbps',
      cells: { 'plan.s': { state: 'included', quantity: 50 }, 'plan.m': { state: 'included', quantity: 100 }, 'plan.l': { state: 'included', quantity: 250 }, 'plan.xl': { state: 'included', quantity: 1000 } },
    },
  ],
}

const features: Feature[] = [
  { id: 'f1', key: 'ssl', name: 'Unlimited free SSL', blurb: '', kind: 'boolean', sort_order: 1 },
  { id: 'f2', key: 'backup', name: 'Backup', blurb: 'Daily backups', kind: 'boolean', addon_sku: 'addon.backup', sort_order: 2 },
  { id: 'f3', key: 'bandwidth', name: 'Bandwidth', blurb: '', kind: 'quantity', unit: 'Mbps', addon_sku: 'eip.bandwidth_mbps', sort_order: 3 },
  // Not yet in the book: every cell reads not offered until the operator fills it.
  { id: 'f4', key: 'sso', name: 'SSO', blurb: '', kind: 'boolean', sort_order: 4 },
]

describe('states', () => {
  it('labels and tones the three states, and reads an unknown one as not offered', () => {
    expect(stateLabel('included')).toBe('Included')
    expect(stateLabel('optional')).toBe('Optional')
    expect(stateLabel('not_offered')).toBe('Not offered')
    expect(stateLabel(undefined)).toBe('Not offered')
    expect(stateTone('included')).toBe('ok')
    expect(stateTone('optional')).toBe('info')
    expect(stateTone('not_offered')).toBeUndefined()
  })
  it('names a plan from the document, else from its SKU', () => {
    expect(planName(doc, 'plan.xl')).toBe('XL')
    expect(planName(null, 'plan.m')).toBe('M')
  })
})

describe('cells', () => {
  it('reads a missing cell as not offered', () => {
    expect(cellOf(doc.features[2], 'plan.l').state).toBe('not_offered')
    expect(cellOf(undefined, 'plan.s')).toEqual({ state: 'not_offered' })
  })
  it('says which package includes an optional feature, or nothing', () => {
    expect(includedFromText(doc, cellOf(doc.features[1], 'plan.s'))).toBe('included from XL')
    expect(includedFromText(doc, cellOf(doc.features[2], 'plan.s'))).toBe('')
  })
  it('words a cell: included, the add-on price, the quantity, not offered', () => {
    expect(cellText({ kind: 'boolean' }, { state: 'included' }, 'OMR')).toBe('Included')
    expect(cellText({ kind: 'boolean' }, { state: 'optional', price_month: '1.500' }, 'OMR')).toBe('+ 1.500 OMR / month')
    expect(cellText({ kind: 'boolean' }, { state: 'optional' }, 'OMR')).toBe('Optional · unpriced')
    expect(cellText({ kind: 'quantity', unit: 'Mbps' }, { state: 'included', quantity: 50 }, 'OMR')).toBe('50 Mbps')
    expect(cellText({ kind: 'boolean' }, { state: 'not_offered' }, 'OMR')).toBe('Not offered')
  })
})

describe('what a package offers', () => {
  it('lists the optional features of a package with price and hint, in matrix order', () => {
    expect(optionalFeatures(doc, 'plan.m')).toEqual([
      { key: 'backup', name: 'Backup', blurb: 'Daily backups of your sites and databases, kept 30 days', addon_sku: 'addon.backup', price_month: '1.500', included_from: 'included from XL' },
      { key: 'dedicated_ip', name: 'Dedicated IP address', blurb: '', addon_sku: 'addon.dedicated_ip', price_month: '2.000', included_from: '' },
    ])
    // XL includes backup; L does not offer the dedicated IP.
    expect(optionalFeatures(doc, 'plan.xl').map((o) => o.key)).toEqual(['dedicated_ip'])
    expect(optionalFeatures(doc, 'plan.l').map((o) => o.key)).toEqual(['backup'])
    expect(optionalFeatures(null, 'plan.m')).toEqual([])
  })
  it('lists the included features of a package', () => {
    expect(includedFeatures(doc, 'plan.xl').map((f) => f.key)).toEqual(['ssl', 'backup', 'bandwidth'])
    expect(includedFeatures(doc, 'plan.s').map((f) => f.key)).toEqual(['ssl', 'bandwidth'])
  })
  it('turns a package choice with add-ons into add-on lines, dropping what is not optional there', () => {
    expect(addonLines(doc, 'm', ['backup', 'ssl', 'nope'])).toEqual([{ sku: 'addon.backup', key: 'backup', label: 'Backup add-on', price_month: '1.500' }])
    // On XL backup is included: ticking it adds nothing.
    expect(addonLines(doc, 'xl', ['backup'])).toEqual([])
    expect(addonLines(doc, 'plan.s', ['dedicated_ip', 'backup']).map((l) => l.sku)).toEqual(['addon.backup', 'addon.dedicated_ip'])
  })
})

describe('the comparison table', () => {
  it('orders the includes rows vCPU, memory, then the quantity features, labelled in words', () => {
    expect(includesRows(doc)).toEqual([
      { key: 'vcpu', label: 'vCPU' },
      { key: 'memory_gb', label: 'Memory (GB)' },
      { key: 'bandwidth_mbps', label: 'Bandwidth (Mbps)' },
    ])
    expect(includesLabel('storage_gb')).toBe('Storage (GB)')
    expect(includesLabel('mail_accounts')).toBe('Mail (accounts)')
    expect(includesValue(doc.packages[0], 'bandwidth_mbps')).toBe('50')
    expect(includesValue(doc.packages[0], 'storage_gb')).toBe('—')
  })
})

describe('the console matrix', () => {
  it('shows every feature, with the book’s cells and not offered where the book has none', () => {
    const rows = matrixRows(doc, features)
    expect(rows.map((r) => r.feature.key)).toEqual(['ssl', 'backup', 'bandwidth', 'sso'])
    expect(rows[1].cells['plan.s']).toEqual({ state: 'optional', addon_sku: 'addon.backup', price_month: '1.500', included_from: 'plan.xl' })
    expect(rows[1].cells['plan.xl'].state).toBe('included')
    expect(rows[1].inBook).toBe(true)
    expect(rows[3].inBook).toBe(false)
    expect(Object.values(rows[3].cells).map((c) => c.state)).toEqual(['not_offered', 'not_offered', 'not_offered', 'not_offered'])
  })
})
