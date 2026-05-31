/**
 * scorecardToTreemapNodes — pure helper that converts a flat Score[]
 * (typically `ScorecardResponse.applications` after merge with the
 * SSE stream) into a 2-level treemap tree (Organization → Application).
 *
 * Lives in a sibling file so the consuming React component file stays
 * "components only" (react-refresh/only-export-components rule).
 *
 * Per the brief U1 scope: "Recharts squarified treemap, dimensions =
 * Sovereign × Organization × Application × score". The Sovereign-level
 * rollup is a page header (one value, not a treemap layer); the
 * treemap shows Organization × Application below it.
 */

import type { ComplianceTreemapNode } from './ComplianceTreemapNode'
import type { CategoryScore, Score } from '@/pages/admin/compliance/compliance.api'

export function scorecardToTreemapNodes(
  scores: Score[],
  policyDomainFilter?: ReadonlySet<string>,
): ComplianceTreemapNode[] {
  // Group apps by their organizationRef. Apps without an organization
  // land under "—" (parent unknown).
  const byOrg = new Map<string, ComplianceTreemapNode[]>()
  for (const s of scores) {
    if (s.scope !== 'application') continue
    if (policyDomainFilter && !applicationTouchesDomain(s, policyDomainFilter)) continue
    const orgKey = s.organizationRef || '—'
    const list = byOrg.get(orgKey) ?? []
    // Recharts requires non-zero size for cells to render. A zero
    // denominator (no policies applicable) still gets a tiny baseline
    // so the cell is visible (and colored grey via null total).
    const size = Math.max(1, s.denominator)
    list.push({
      name: s.applicationRef || s.id,
      total: s.total,
      size,
      score: s,
    })
    byOrg.set(orgKey, list)
  }
  // Top-level orgs sorted by total weight (largest first).
  const out: ComplianceTreemapNode[] = []
  for (const [org, apps] of byOrg.entries()) {
    apps.sort((a, b) => b.size - a.size)
    out.push({
      name: org,
      children: apps,
      size: apps.reduce((sum, a) => sum + a.size, 0),
    })
  }
  out.sort((a, b) => b.size - a.size)
  return out
}

/**
 * categoryScoresToTreemapNodes — G86b #2633 fallback synthesizer
 * (2026-06-01).
 *
 * Built for the case where the live scorecard reports a real Sovereign
 * Score (e.g. 50%) with populated `categoryScores` (baseline=77%,
 * 18 policies) BUT zero per-Application rollups, because workloads
 * lack the `catalyst.openova.io/application` label that
 * `enrichResourceState` needs to bucket per-app.
 *
 * Pre-G86b the treemap rendered the empty placeholder ("No data yet
 * for Compliance.") — hiding the real fact that 121 policy-weight
 * units exist and 61 pass. This synthesises one leaf per non-zero
 * category so operators see the actual compliance distribution.
 *
 * Returns one parent group ("Compliance categories") whose children
 * are per-category leaves keyed on the canonical domain vocabulary
 * (`security`, `sre`/`reliability`, `baseline`). The leaf's `total`
 * drives color via `scoreColor()`; `size` is the category's
 * denominator (so a 0-denominator category collapses to a 1-pixel
 * baseline cell with grey color, never silently dropped).
 *
 * Returns `[]` when `categoryScores` itself is empty or every
 * category has `denominator === 0`, so the existing empty-state
 * render path still fires on a truly cold-start sovereign.
 */
export function categoryScoresToTreemapNodes(
  categoryScores: Record<string, CategoryScore> | undefined,
): ComplianceTreemapNode[] {
  if (!categoryScores) return []
  // Render in canonical UI vocabulary order so the surface is stable
  // across reloads. `reliability` is the UI-facing alias for the
  // backend `sre` domain (see compliance.go ScorecardResponse alias).
  const order: Array<{ key: string; label: string }> = [
    { key: 'security', label: 'Security' },
    { key: 'sre', label: 'Reliability' },
    { key: 'baseline', label: 'Baseline' },
  ]
  const leaves: ComplianceTreemapNode[] = []
  for (const { key, label } of order) {
    const cs = categoryScores[key]
    if (!cs) continue
    const denom = Number(cs.denominator) || 0
    if (denom <= 0 && (Number(cs.policyCount) || 0) <= 0) continue
    const size = Math.max(1, denom > 0 ? denom : cs.policyCount)
    // total stays null when denominator is zero — `scoreColor(null)`
    // greys the cell instead of rendering it as a failing red.
    const total = denom > 0 ? Number(cs.score) : null
    leaves.push({
      name: `${label} (${cs.policyCount} ${cs.policyCount === 1 ? 'policy' : 'policies'})`,
      total,
      size,
      // Synthetic Score so the cell still feeds tooltip / onLeafClick
      // pathways. policyResults remains undefined — handleLeafClick
      // gracefully no-ops when no policy key is available.
      score: {
        scope: 'application',
        id: `category:${key}`,
        total,
        numerator: Number(cs.numerator) || 0,
        denominator: denom,
        updatedAt: new Date().toISOString(),
      },
    })
  }
  if (leaves.length === 0) return []
  return [
    {
      name: 'Compliance categories',
      children: leaves,
      size: leaves.reduce((sum, l) => sum + l.size, 0),
    },
  ]
}

function applicationTouchesDomain(
  s: Score,
  domain: ReadonlySet<string>,
): boolean {
  if (!s.policyResults) return true // can't filter rollups → keep
  for (const k of Object.keys(s.policyResults)) {
    if (domain.has(k)) return true
  }
  return false
}
