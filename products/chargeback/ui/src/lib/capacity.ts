import type { CapacityFamilyDef, CapacityOverview, CapacityPoolStatus, CapacityPoolView, CapacityThresholds, CapacityZoneView } from '../api/types'
import { toNumber } from './num'

/**
 * Capacity readers (DESIGN.md §11). The server derives every figure; what
 * lives here is display arithmetic the page renders with — the threshold
 * colouring, the headroom rule re-stated for a cell the operator is editing
 * before the server has answered, and the form parsing — pinned by vitest
 * against the same figures the Go tests derive.
 */

/** The thresholds the server ships; used until the document arrives. */
export const DEFAULT_THRESHOLDS: CapacityThresholds = { warn_pct: 70, critical_pct: 85 }

/** The seven families in display order, for a page rendered before the document arrives. */
export const FAMILY_ORDER: ReadonlyArray<string> = ['vcpu', 'memory_gib', 'block_ssd_gib', 'block_hdd_gib', 'object_gib', 'eip_addresses', 'bandwidth_mbps']

const FALLBACK_LABELS: Record<string, CapacityFamilyDef> = {
  vcpu: { family: 'vcpu', label: 'vCPU', unit: 'vCPU' },
  memory_gib: { family: 'memory_gib', label: 'Memory', unit: 'GiB' },
  block_ssd_gib: { family: 'block_ssd_gib', label: 'Block SSD', unit: 'GiB' },
  block_hdd_gib: { family: 'block_hdd_gib', label: 'Block HDD', unit: 'GiB' },
  object_gib: { family: 'object_gib', label: 'Object storage', unit: 'GiB' },
  eip_addresses: { family: 'eip_addresses', label: 'Elastic IPs', unit: 'addresses' },
  bandwidth_mbps: { family: 'bandwidth_mbps', label: 'Bandwidth', unit: 'Mbps' },
}

/** The family definition by key: the server's list first, the built-in fallback for a key it did not send. */
export function familyDef(key: string, families?: CapacityFamilyDef[] | null): CapacityFamilyDef {
  return families?.find((f) => f.family === key) ?? FALLBACK_LABELS[key] ?? { family: key, label: key, unit: '' }
}

/**
 * utilisationStatus classifies a percentage: unset without a total (pct null),
 * warn at ≥ warn_pct, critical at ≥ critical_pct — the same rule as
 * capacity.Status on the server.
 */
export function utilisationStatus(pct: number | null | undefined, thresholds: CapacityThresholds = DEFAULT_THRESHOLDS): CapacityPoolStatus {
  if (pct === null || pct === undefined || !Number.isFinite(pct)) return 'unset'
  if (pct >= thresholds.critical_pct) return 'critical'
  if (pct >= thresholds.warn_pct) return 'warn'
  return 'ok'
}

/** The heatmap cell class for a status. */
export function heatClass(status: CapacityPoolStatus | string): string {
  switch (status) {
    case 'ok':
    case 'warn':
    case 'critical':
      return `heat ${status}`
    default:
      return 'heat unset'
  }
}

export interface Headroom {
  /** null when no family in the footprint has a total. */
  units: number | null
  /** The family that binds, "cap" when the direct cap does, "" when null. */
  binding: string
}

/**
 * headroom is min over the footprint's families of floor(available ÷
 * amount), over the families whose pool has a total (a family the operator
 * has not sized carries no information); a cap in SKU units bounds it as
 * floor(cap − consumed). Mirrors the server's rule so a cell being edited
 * previews the same number the next overview will report.
 */
export function headroom(
  footprint: Record<string, number | string>,
  available: Record<string, number | string | null | undefined>,
  hasTotal: Record<string, boolean>,
  cap?: { total: number | string; consumed: number | string } | null,
): Headroom {
  let best: number | null = null
  let binding = ''
  for (const fam of [...FAMILY_ORDER, ...Object.keys(footprint).filter((k) => !FAMILY_ORDER.includes(k))]) {
    if (!(fam in footprint) || !hasTotal[fam]) continue
    const amount = toNumber(footprint[fam])
    if (amount <= 0) continue
    const units = Math.floor(Math.max(0, toNumber(available[fam])) / amount)
    if (best === null || units < best) {
      best = units
      binding = fam
    }
  }
  if (cap) {
    const left = Math.floor(Math.max(0, toNumber(cap.total) - toNumber(cap.consumed)))
    if (best === null || left < best) {
      best = left
      binding = 'cap'
    }
  }
  return { units: best, binding }
}

/** "8 vCPU · 64 GiB" — a footprint in words, in family order. */
export function footprintSummary(footprint: Record<string, number | string> | null | undefined, families?: CapacityFamilyDef[] | null): string {
  if (!footprint) return ''
  const parts: string[] = []
  for (const fam of [...FAMILY_ORDER, ...Object.keys(footprint).filter((k) => !FAMILY_ORDER.includes(k))]) {
    if (!(fam in footprint)) continue
    const n = toNumber(footprint[fam])
    if (n <= 0) continue
    parts.push(`${formatAmount(n)} ${familyDef(fam, families).unit}`)
  }
  return parts.join(' · ')
}

/** Amounts are exact decimals; render up to 3 decimals, trailing zeros trimmed, en-US grouping. */
export function formatAmount(v: number | string | null | undefined, digits = 3): string {
  if (v === null || v === undefined || v === '') return '—'
  const n = toNumber(v)
  return n.toLocaleString('en-US', { maximumFractionDigits: digits })
}

/**
 * formatExhaustion says when a pool runs out at the present growth: "—" when
 * not growing or unknown, "< 1 day", "12 days", "3.5 months" (≥ 60 days),
 * "2.1 years" (≥ 730 days); 0 reads "now".
 */
export function formatExhaustion(days: number | null | undefined): string {
  if (days === null || days === undefined || !Number.isFinite(days)) return '—'
  if (days <= 0) return 'now'
  if (days < 1) return '< 1 day'
  if (days >= 730) return `${(days / 365).toFixed(1)} years`
  if (days >= 60) return `${(days / 30.4375).toFixed(1)} months`
  const d = Math.round(days)
  return `${d} day${d === 1 ? '' : 's'}`
}

/** "80.0 %" · "—" without a total. */
export function formatUtilisation(pct: number | null | undefined): string {
  if (pct === null || pct === undefined || !Number.isFinite(pct)) return '—'
  return `${pct.toLocaleString('en-US', { minimumFractionDigits: 1, maximumFractionDigits: 1 })} %`
}

export interface ParsedFootprint {
  families: Record<string, string>
  error: string
}

/**
 * parseFootprintForm turns the editor's per-family text inputs into the PUT
 * body: blanks and zeros are dropped (they remove the family), anything not
 * a non-negative number is an error naming the family. An empty result is
 * legal — it removes the footprint — the caller decides whether to warn.
 */
export function parseFootprintForm(values: Record<string, string>, families?: CapacityFamilyDef[] | null): ParsedFootprint {
  const out: Record<string, string> = {}
  for (const [fam, raw] of Object.entries(values)) {
    const s = raw.trim().replace(/,/g, '')
    if (s === '') continue
    if (!/^\d+(\.\d+)?$/.test(s)) return { families: {}, error: `${familyDef(fam, families).label}: enter a non-negative number` }
    if (Number(s) === 0) continue
    out[fam] = s
  }
  return { families: out, error: '' }
}

/** parseTotal validates a pool total typed into a cell: a non-negative number, commas allowed. */
export function parseTotal(raw: string): { total: string; error: string } {
  const s = raw.trim().replace(/,/g, '')
  if (s === '') return { total: '', error: 'enter the total' }
  if (!/^\d+(\.\d+)?$/.test(s)) return { total: '', error: 'the total must be a non-negative number' }
  return { total: s, error: '' }
}

/** The zones of the overview flattened with their region, in the server's order. */
export function zoneRows(ov: CapacityOverview | null | undefined, region?: string | 'all'): Array<{ region: string; regionName: string; zone: CapacityZoneView }> {
  const out: Array<{ region: string; regionName: string; zone: CapacityZoneView }> = []
  for (const r of ov?.regions ?? []) {
    if (region && region !== 'all' && r.code !== region) continue
    for (const z of r.zones) out.push({ region: r.code, regionName: r.name, zone: z })
  }
  return out
}

/** The pool of a zone for a family, if any. */
export function poolFor(zone: CapacityZoneView, family: string): CapacityPoolView | undefined {
  return zone.pools.find((p) => p.family === family)
}

/** Whether a pool has a total (0 reads as "not entered"). */
export function hasTotal(pool: CapacityPoolView | { total: number | string } | null | undefined): boolean {
  return Boolean(pool) && toNumber(pool!.total) > 0
}

/** "2026-09-09 09:00Z" for the as-of hour; "" when null. */
export function asOfLabel(asOf: string | null | undefined): string {
  if (!asOf) return ''
  const d = new Date(asOf)
  if (Number.isNaN(d.getTime())) return asOf
  return d.toISOString().replace('T', ' ').slice(0, 16) + 'Z'
}
