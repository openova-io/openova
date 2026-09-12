import type { CostCentre, CostCentreLine, CostCentreReport } from '../api/types'
import type { Errors } from './forms'
import { toNumber } from './num'

/**
 * Cost-centre helpers (DESIGN.md §19) — pure, unit-tested in
 * costcentres.test.ts. The server owns the attribution and the
 * apportionment; these only shape a form, describe a rule in words, and say
 * whether a breakdown adds up to the invoice it claims to describe.
 */

/** The bucket usage no override and no rule names falls into. */
export const UNASSIGNED_CODE = '(unassigned)'
export const UNASSIGNED_NAME = 'Unassigned'

/** store.CostCentreCodeRule, mirrored so the form refuses before the POST. */
export const CODE_RULE = /^[A-Za-z0-9][A-Za-z0-9_.:/@-]{0,63}$/
/** store.TagKeyRule. */
export const TAG_KEY_RULE = /^[A-Za-z0-9_.:/@-]{1,128}$/

export const DEFAULT_PRIORITY = 100

export function isUnassigned(code: string): boolean {
  return code === UNASSIGNED_CODE
}

/** What a centre is called on a screen: its name, else its code. */
export function centreLabel(c: { code: string; name?: string | null }): string {
  const name = (c.name ?? '').trim()
  if (name) return name
  return c.code === UNASSIGNED_CODE ? UNASSIGNED_NAME : c.code
}

export interface CostCentreForm {
  code: string
  name: string
  active: boolean
}

export function emptyCostCentreForm(): CostCentreForm {
  return { code: '', name: '', active: true }
}

export function costCentreForm(c: CostCentre): CostCentreForm {
  return { code: c.code, name: c.name ?? '', active: c.active }
}

export function validateCostCentre(f: CostCentreForm): Errors<CostCentreForm> {
  const e: Errors<CostCentreForm> = {}
  const code = f.code.trim()
  if (!code) e.code = 'A code is required — it is what a tag value matches and what a report prints.'
  else if (!CODE_RULE.test(code)) e.code = 'Letters, digits and _ . : / @ - only, starting with a letter or digit, up to 64 characters.'
  if (f.name.trim().length > 200) e.name = 'At most 200 characters.'
  return e
}

export function costCentreBody(f: CostCentreForm) {
  return { code: f.code.trim(), name: f.name.trim(), active: f.active }
}

export interface CostCentreRuleForm {
  cost_centre_id: string
  tag_key: string
  tag_value: string
  priority: string
}

export function emptyCostCentreRuleForm(costCentreId = ''): CostCentreRuleForm {
  return { cost_centre_id: costCentreId, tag_key: '', tag_value: '', priority: String(DEFAULT_PRIORITY) }
}

export function validateCostCentreRule(f: CostCentreRuleForm): Errors<CostCentreRuleForm> {
  const e: Errors<CostCentreRuleForm> = {}
  if (!f.cost_centre_id) e.cost_centre_id = 'Choose the cost centre this tag value belongs to.'
  const key = f.tag_key.trim()
  if (!key) e.tag_key = 'The tag key the collector records, e.g. cost-centre or team.'
  else if (!TAG_KEY_RULE.test(key)) e.tag_key = 'Letters, digits and _ . : / @ - only, up to 128 characters.'
  if (f.tag_value.trim().length > 256) e.tag_value = 'At most 256 characters.'
  const p = Number(f.priority)
  if (!Number.isInteger(p) || p < 0 || p > 9999) e.priority = 'A whole number between 0 and 9999. Lower is matched first.'
  return e
}

export function costCentreRuleBody(f: CostCentreRuleForm) {
  return { cost_centre_id: f.cost_centre_id, tag_key: f.tag_key.trim(), tag_value: f.tag_value.trim(), priority: Number(f.priority) }
}

/** "team = platform", the rule as a person reads it. */
export function ruleMatchText(r: { tag_key: string; tag_value: string }): string {
  return `${r.tag_key} = ${r.tag_value === '' ? '(empty)' : r.tag_value}`
}

/** A row's share of the breakdown's total, 0-100; 0 when the total is 0. */
/**
 * Which figure a report is actually carrying. A period the rating run has not
 * confirmed has zero in every money column, so reading `total` there reports
 * nothing at all — and "nothing" is indistinguishable from "attributed". The
 * usage column is what those rows do carry, so that is what the page measures
 * until an invoice confirms them.
 */
export function reportMetric(doc: CostCentreReport | null | undefined): 'total' | 'usage' {
  return doc?.source === 'statement' ? 'total' : 'usage'
}

export function shareOf(line: CostCentreLine, totalValue: number, metric: 'total' | 'usage' = 'total'): number {
  if (!Number.isFinite(totalValue) || totalValue === 0) return 0
  return (toNumber(line[metric]) / totalValue) * 100
}

/**
 * Whether a breakdown adds up to the invoice it describes. The server
 * apportions by largest remainder, so this is an identity and not a
 * tolerance — a false here is a defect, and the page says so rather than
 * rounding it away.
 */
export function breakdownAgrees(doc: CostCentreReport | null | undefined): boolean {
  if (!doc?.invoice) return true
  return Boolean(doc.invoice.agrees)
}

/** What the report is: the invoice's own figures, or usage not yet rated. */
export function sourceText(doc: CostCentreReport | null | undefined): string {
  if (!doc) return ''
  if (doc.source === 'statement') {
    return `The ${doc.status === 'issued' ? 'issued invoice' : 'draft statement'} for ${doc.period}, read by cost centre. These are its own figures, frozen when it was rated.`
  }
  return `${doc.period} has not been rated yet, so these rows carry the period's usage alone — no invoice has confirmed them.`
}
