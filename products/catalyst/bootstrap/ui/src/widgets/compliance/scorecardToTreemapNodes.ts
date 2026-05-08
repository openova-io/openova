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
import type { Score } from '@/pages/admin/compliance/compliance.api'

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
