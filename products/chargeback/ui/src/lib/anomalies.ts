import type { Anomaly, AnomalyDriver } from '../api/types'
import { addDays } from './dates'

/**
 * Anomalies page helpers (#6867, DESIGN.md §3.6). The server flags a day when
 * its cost for a (customer, kind) is ≥ 3σ above the trailing 14-day mean,
 * ≥ 1.3 × that mean, and at least one currency unit over it. Nothing here
 * re-derives that; the page only sorts, keys and links what it was given.
 */

/** The rule, stated for the empty state and the page footnote. */
export function anomalyRule(currency: string): string {
  return `No day exceeded 3σ of its 14-day baseline with ≥ 1 ${currency || 'unit'} impact`
}

/** Stable identity of one row — the same anomaly keeps its key across reloads so expansion survives. */
export function anomalyKey(a: Anomaly): string {
  return `${a.day}|${a.customer_id}|${a.dimension}|${a.key}`
}

/** Newest first, then by impact so the most expensive spike of a day leads. */
export function sortAnomalies(rows: Anomaly[]): Anomaly[] {
  return [...rows].sort((a, b) => b.day.localeCompare(a.day) || b.impact - a.impact || a.label.localeCompare(b.label))
}

export interface AnomalyKPIs {
  count: number
  totalImpact: number
  /** The single day with the largest summed impact, or null with no rows. */
  biggestDay: { day: string; impact: number } | null
}

export function anomalyKPIs(rows: Anomaly[]): AnomalyKPIs {
  const byDay = new Map<string, number>()
  let total = 0
  for (const a of rows) {
    total += a.impact
    byDay.set(a.day, (byDay.get(a.day) ?? 0) + a.impact)
  }
  let biggest: AnomalyKPIs['biggestDay'] = null
  for (const [day, impact] of byDay) if (!biggest || impact > biggest.impact) biggest = { day, impact }
  return { count: rows.length, totalImpact: total, biggestDay: biggest }
}

export interface DriverRow {
  key: string
  label: string
  value: number
}

/**
 * Drivers → RankedBars rows. Largest Δ first; SKU and resource drivers are
 * told apart in the label so "ecs.s6.large.2" and a server named the same
 * never collapse into one bar. Negative deltas (things that got cheaper on
 * the spike day) are kept — they explain why the impact is smaller than the
 * biggest driver — but ranked after every increase.
 */
export function driverRows(drivers: AnomalyDriver[] | null | undefined): DriverRow[] {
  const prefix = (k: string) => (k === 'sku' ? 'SKU' : k === 'resource' ? 'Resource' : k ? k[0].toUpperCase() + k.slice(1) : '')
  return (drivers ?? [])
    .filter((d) => d && Number.isFinite(d.delta))
    .map((d, i) => ({ key: `${d.kind}:${d.key || i}`, label: prefix(d.kind) ? `${prefix(d.kind)} · ${d.label || d.key}` : d.label || d.key, value: d.delta }))
    .sort((a, b) => b.value - a.value)
}

/** Explorer params for one anomaly: that one day, by SKU, scoped to the flagged dimension value. */
export function anomalyExplorerParams(a: Anomaly, operator: boolean): URLSearchParams {
  const p = new URLSearchParams({ preset: 'custom', from: a.day, to: addDays(a.day, 1), group_by: 'sku' })
  const dim = a.dimension || 'kind'
  if (a.key) p.set(dim, a.key)
  if (operator && a.customer_id) p.set('customer', a.customer_id)
  return p
}

/** Rows whose day the URL named (`?day=`), pre-expanded on load. */
export function keysForDay(rows: Anomaly[], day: string | null): string[] {
  if (!day) return []
  return rows.filter((a) => a.day === day).map(anomalyKey)
}
