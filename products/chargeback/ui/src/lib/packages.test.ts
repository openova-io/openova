import { describe, expect, it } from 'vitest'
import type { Feature } from '../api/types'
import { packages as doc } from '../panels/estimate/fixture'
import {
  addonLines,
  cellOf,
  cellSubText,
  cellText,
  floorItems,
  floorRows,
  groupedFeatures,
  groupedRows,
  includedFeatures,
  includedFromText,
  includesLabel,
  includesRows,
  includesValue,
  levelLabel,
  matrixRows,
  nextLevelLabel,
  optionalFeatures,
  planName,
  shapeGuaranteed,
  shapeHeadline,
  stateLabel,
  stateTone,
  stepUpHint,
  stepUpText,
  teaserText,
} from './packages'

/** Every feature of the fixture as GET /features lists it, plus one not yet in the book and one on the floor. */
const features: Feature[] = [
  { id: 'f1', key: 'bandwidth', name: 'Bandwidth', blurb: '', kind: 'quantity', group: 'capacity', unit: 'Mbps', addon_sku: 'eip.bandwidth_mbps', teaser: false, sort_order: 1 },
  { id: 'f2', key: 'disk', name: 'Disk', blurb: '', kind: 'quantity', group: 'capacity', unit: 'GB', addon_sku: 'k8s.pvc_gb', teaser: false, sort_order: 2 },
  { id: 'f3', key: 'ai_seo', name: 'AI SEO ready', blurb: '', kind: 'boolean', group: 'features', addon_sku: 'addon.ai_seo', teaser: false, sort_order: 3 },
  { id: 'f4', key: 'gitea_iac', name: 'Gitea + IaC', blurb: '', kind: 'access', group: 'access', teaser: false, sort_order: 4 },
  { id: 'f5', key: 'vuln_dashboard', name: 'Vulnerability dashboard', blurb: '', kind: 'boolean', group: 'ops', teaser: true, sort_order: 5 },
  { id: 'f6', key: 'dedicated_ip', name: 'Dedicated IP address', blurb: '', kind: 'boolean', group: 'scope', addon_sku: 'addon.dedicated_ip', teaser: false, sort_order: 6 },
  { id: 'f7', key: 'backup', name: 'Backup', blurb: 'Scheduled backups', kind: 'boolean', group: 'resilience', addon_sku: 'addon.backup', teaser: false, sort_order: 7 },
  { id: 'f8', key: 'dr_topology', name: 'DR topology', blurb: '', kind: 'level', group: 'resilience', levels: ['single region', 'active-passive'], addon_sku: 'addon.dr', teaser: false, sort_order: 8 },
  // Not yet in the book: every cell reads not offered until the operator fills it.
  { id: 'f9', key: 'sla', name: 'SLA', blurb: '', kind: 'level', group: 'service', levels: ['99.5 %', '99.9 %'], teaser: false, sort_order: 9 },
  // On the floor: never a row of the matrix.
  { id: 'f10', key: 'ssl', name: 'Unlimited free SSL', blurb: 'Certificates for every site', kind: 'boolean', group: 'floor', teaser: false, sort_order: 0 },
]

const backup = doc.features.find((f) => f.key === 'backup')!
const dr = doc.features.find((f) => f.key === 'dr_topology')!
const bw = doc.features.find((f) => f.key === 'bandwidth')!
const vuln = doc.features.find((f) => f.key === 'vuln_dashboard')!

describe('states', () => {
  it('labels and tones the three states, and reads an unknown one or a teaser as not offered', () => {
    expect(stateLabel('included')).toBe('Included')
    expect(stateLabel('optional')).toBe('Optional')
    expect(stateLabel('not_offered')).toBe('Not offered')
    expect(stateLabel('teaser')).toBe('Not offered')
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
    expect(cellOf({ cells: {} }, 'plan.l').state).toBe('not_offered')
    expect(cellOf(undefined, 'plan.s')).toEqual({ state: 'not_offered' })
  })
  it('says which package includes an optional feature, or nothing; a teaser says where it starts', () => {
    expect(includedFromText(doc, cellOf(backup, 'plan.s'))).toBe('included from XL')
    expect(includedFromText(doc, cellOf(doc.features.find((f) => f.key === 'dedicated_ip')!, 'plan.s'))).toBe('')
    expect(teaserText(doc, cellOf(vuln, 'plan.s'))).toBe('from M')
    expect(teaserText(doc, cellOf(vuln, 'plan.m'))).toBe('')
  })
  it('names a level and the next one', () => {
    expect(levelLabel(dr, cellOf(dr, 'plan.s'))).toBe('single region')
    expect(levelLabel(dr, cellOf(dr, 'plan.xl'))).toBe('active-passive')
    expect(nextLevelLabel(dr, cellOf(dr, 'plan.s'))).toBe('active-passive')
    expect(nextLevelLabel(dr, cellOf(dr, 'plan.m'))).toBe('')
    expect(levelLabel(backup, cellOf(backup, 'plan.s'))).toBe('')
  })
  it('words a cell by its kind: included, the add-on price, the quantity, a level, a teaser, not offered', () => {
    expect(cellText({ kind: 'boolean' }, { state: 'included' }, 'OMR')).toBe('Included')
    expect(cellText({ kind: 'boolean' }, { state: 'optional', price_month: '1.500' }, 'OMR')).toBe('+ 1.500 OMR / month')
    expect(cellText({ kind: 'boolean' }, { state: 'optional' }, 'OMR')).toBe('Optional · unpriced')
    expect(cellText({ kind: 'quantity', unit: 'Mbps' }, { state: 'included', quantity: 50, overage: 'hard_cap' }, 'OMR')).toBe('50 Mbps')
    expect(cellText({ kind: 'quantity', unit: 'GB' }, { state: 'included', overage: 'unlimited' }, 'OMR')).toBe('Unlimited')
    expect(cellText(dr, cellOf(dr, 'plan.s'), 'OMR')).toBe('single region')
    expect(cellText({ kind: 'access' }, { state: 'included' }, 'OMR')).toBe('Included')
    expect(cellText({ kind: 'boolean' }, { state: 'teaser', included_from: 'plan.m' }, 'OMR')).toBe('from M')
    expect(cellText({ kind: 'boolean' }, { state: 'not_offered' }, 'OMR')).toBe('Not offered')
  })
  it('puts the overage and the purchasable next level under the chip', () => {
    expect(cellSubText(bw, cellOf(bw, 'plan.s'), 'OMR')).toBe('hard cap')
    expect(cellSubText(bw, cellOf(bw, 'plan.l'), 'OMR')).toBe('metered')
    expect(cellSubText(dr, cellOf(dr, 'plan.s'), 'OMR')).toBe('active-passive + 8.000 OMR / month')
    expect(cellSubText(dr, cellOf(dr, 'plan.m'), 'OMR')).toBe('')
    expect(cellSubText(backup, cellOf(backup, 'plan.s'), 'OMR')).toBe('')
  })
})

describe('what a package offers', () => {
  it('lists the optional features of a package — boolean add-ons and purchasable next levels — in matrix order', () => {
    expect(optionalFeatures(doc, 'plan.m')).toEqual([
      { key: 'ai_seo', name: 'AI SEO ready', blurb: 'Search-engine readiness checked and tuned by AI', addon_sku: 'addon.ai_seo', price_month: '2.000', included_from: 'included from XL' },
      { key: 'dedicated_ip', name: 'Dedicated IP address', blurb: 'A public IPv4 reserved for your Organization', addon_sku: 'addon.dedicated_ip', price_month: '2.000', included_from: '' },
      { key: 'backup', name: 'Backup', blurb: 'Scheduled backups of your sites and databases', addon_sku: 'addon.backup', price_month: '1.500', included_from: 'included from XL' },
    ])
    // S also offers DR's next level; XL includes backup and AI SEO; L does not offer the dedicated IP.
    expect(optionalFeatures(doc, 'plan.s').map((o) => o.key)).toEqual(['ai_seo', 'dedicated_ip', 'backup', 'dr_topology'])
    expect(optionalFeatures(doc, 'plan.s').at(-1)).toEqual({ key: 'dr_topology', name: 'DR topology', blurb: 'Where your applications run, and where they fail over to', addon_sku: 'addon.dr', price_month: '8.000', included_from: 'included from M', next_level: 'active-passive' })
    expect(optionalFeatures(doc, 'plan.xl').map((o) => o.key)).toEqual(['dedicated_ip'])
    expect(optionalFeatures(doc, 'plan.l').map((o) => o.key)).toEqual(['ai_seo', 'backup'])
    expect(optionalFeatures(null, 'plan.m')).toEqual([])
  })
  it('lists the included features of a package, every kind alike', () => {
    expect(includedFeatures(doc, 'plan.xl').map((f) => f.key)).toEqual(['bandwidth', 'disk', 'ai_seo', 'gitea_iac', 'vuln_dashboard', 'backup', 'dr_topology'])
    expect(includedFeatures(doc, 'plan.s').map((f) => f.key)).toEqual(['bandwidth', 'disk', 'dr_topology'])
  })
  it('turns a package choice with add-ons into add-on lines, dropping what is not optional there', () => {
    expect(addonLines(doc, 'm', ['backup', 'gitea_iac', 'nope'])).toEqual([{ sku: 'addon.backup', key: 'backup', label: 'Backup add-on', price_month: '1.500' }])
    // On XL backup is included: ticking it adds nothing. On S the DR next level is a line named after the level.
    expect(addonLines(doc, 'xl', ['backup'])).toEqual([])
    expect(addonLines(doc, 'plan.s', ['dr_topology', 'backup']).map((l) => l.sku)).toEqual(['addon.backup', 'addon.dr'])
    expect(addonLines(doc, 's', ['dr_topology'])[0].label).toBe('DR topology — active-passive')
  })
})

describe('the comparison table', () => {
  it('orders the includes rows vCPU, memory, then the quantity features, labelled in words', () => {
    expect(includesRows(doc)).toEqual([
      { key: 'vcpu', label: 'vCPU' },
      { key: 'memory_gb', label: 'Memory (GB)' },
      { key: 'bandwidth_mbps', label: 'Bandwidth (Mbps)' },
      { key: 'disk_gb', label: 'Disk (GB)' },
    ])
    expect(includesLabel('storage_gb')).toBe('Storage (GB)')
    expect(includesLabel('mail_accounts')).toBe('Mail (accounts)')
    expect(includesValue(doc.packages[0], 'bandwidth_mbps')).toBe('50')
    expect(includesValue(doc.packages[0], 'storage_gb')).toBe('—')
  })
  it('words the shape: the headline with the disk, the guaranteed floors under it', () => {
    expect(shapeHeadline(doc.packages[1])).toBe('2 vCPU · 4 GB · 50 GB disk')
    expect(shapeGuaranteed(doc.packages[1])).toBe('0.33 vCPU · 1.33 GB guaranteed')
    expect(shapeHeadline({ includes: { vcpu: 2, memory_gb: 4 } })).toBe('2 vCPU · 4 GB')
    expect(shapeGuaranteed({})).toBe('')
  })
  it('groups the published features under the document’s groups, in order, skipping empty groups; the floor apart', () => {
    expect(groupedFeatures(doc).map((g) => [g.group.name, g.features.map((f) => f.key)])).toEqual([
      ['Capacity', ['bandwidth', 'disk']],
      ['Features', ['ai_seo']],
      ['Access', ['gitea_iac']],
      ['Managed operations', ['vuln_dashboard']],
      ['Scope', ['dedicated_ip']],
      ['Resilience', ['backup', 'dr_topology']],
    ])
    expect(floorItems(doc).map((f) => f.name)).toEqual(['Unlimited free SSL', 'Standard DDoS protection'])
    expect(groupedFeatures(null)).toEqual([])
  })
  it('says when the add-ons ticked under a package are worth the step to the next one', () => {
    // DR's next level on S (8.000) is bundled on M and worth more than the 4.000 step.
    expect(stepUpHint(doc, 'plan.s', ['dr_topology'])).toEqual({ next_sku: 'plan.m', next_name: 'M', gap_month: '4.000', sum: '8.000' })
    // Backup on S is not included on M: no hint, however much is ticked.
    expect(stepUpHint(doc, 'plan.s', ['dr_topology', 'backup'])).toBeNull()
    // On L, backup + AI SEO (3.500) are bundled on XL but far short of the 14.000 step.
    expect(stepUpHint(doc, 'plan.l', ['backup', 'ai_seo'])).toBeNull()
    expect(stepUpHint(doc, 'plan.xl', ['dedicated_ip'])).toBeNull()
    expect(stepUpHint(doc, 'plan.s', [])).toBeNull()
  })
})

describe('the console matrix', () => {
  it('shows every feature but the floor, with the book’s cells and not offered where the book has none', () => {
    const rows = matrixRows(doc, features)
    expect(rows.map((r) => r.feature.key)).toEqual(['bandwidth', 'disk', 'ai_seo', 'gitea_iac', 'vuln_dashboard', 'dedicated_ip', 'backup', 'dr_topology', 'sla'])
    expect(rows[6].cells['plan.s']).toEqual({ state: 'optional', addon_sku: 'addon.backup', price_month: '1.500', included_from: 'plan.xl' })
    expect(rows[6].cells['plan.xl'].state).toBe('included')
    expect(rows[6].inBook).toBe(true)
    expect(rows[8].inBook).toBe(false)
    expect(Object.values(rows[8].cells).map((c) => c.state)).toEqual(['not_offered', 'not_offered', 'not_offered', 'not_offered'])
  })
  it('groups the rows under their heading in the document’s order, a feature not yet in the book under its own group', () => {
    expect(groupedRows(doc, features).map((g) => [g.group.key, g.rows.map((r) => r.feature.key)])).toEqual([
      ['capacity', ['bandwidth', 'disk']],
      ['features', ['ai_seo']],
      ['access', ['gitea_iac']],
      ['ops', ['vuln_dashboard']],
      ['scope', ['dedicated_ip']],
      ['resilience', ['backup', 'dr_topology']],
      ['service', ['sla']],
    ])
    expect(floorRows(features).map((f) => f.key)).toEqual(['ssl'])
  })
  it('words the step-up check, green when it holds and red when it does not', () => {
    expect(stepUpText(doc.packages[0], 'OMR')).toEqual({ text: 'gap 4.000 · bundled add-ons 8.000 OMR ✓', ok: true })
    expect(stepUpText(doc.packages[1], 'OMR')).toEqual({ text: 'gap 7.000 · bundled add-ons 0.000 OMR ✓', ok: true })
    expect(stepUpText(doc.packages[2], 'OMR')).toEqual({ text: 'gap 14.000 · bundled add-ons 3.500 OMR ✗ raise add-on prices', ok: false })
    expect(stepUpText(doc.packages[3], 'OMR')).toBeNull()
  })
})
