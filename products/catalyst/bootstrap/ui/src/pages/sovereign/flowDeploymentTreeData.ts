/**
 * flowDeploymentTreeData.ts — pure helper that builds FlowGroupRow[]
 * from a flat job list + per-job hint lookup.
 *
 * Lives in its own module (not co-located with FlowDeploymentTree.tsx)
 * so the React component file only exports React components — keeps
 * Vite's HMR fast-refresh happy.
 */

import type { JobStatus } from '@/lib/jobs.types'
import type { FlowFamily } from '@/lib/flowLayoutV4'
import type { FlowGroupRow } from './FlowDeploymentTree'

export interface BuildFlowGroupRowsArgs {
  jobs: readonly { id: string; jobName: string; status: JobStatus; durationMs: number }[]
  hintByJob: ReadonlyMap<string, { regionId?: string; familyId?: string; label?: string }>
  regions: ReadonlyArray<{ id: string; label: string; meta: string }>
  families: readonly FlowFamily[]
  familyDescriptions?: Readonly<Record<string, string>>
}

export function buildFlowGroupRows(args: BuildFlowGroupRowsArgs): FlowGroupRow[] {
  const familyDesc = args.familyDescriptions ?? {}
  const familyById = new Map<string, FlowFamily>()
  for (const f of args.families) familyById.set(f.id, f)
  const fallbackRegion = { id: 'primary', label: 'Primary Region', meta: '' }
  const regionById = new Map<string, { id: string; label: string; meta: string }>()
  for (const r of args.regions) regionById.set(r.id, r)
  if (!regionById.has(fallbackRegion.id)) regionById.set(fallbackRegion.id, fallbackRegion)

  // Group jobs by (region, family).
  const buckets = new Map<string, FlowGroupRow>()
  // Track region order and per-region family order from the order jobs
  // arrive (deterministic).
  const regionOrder: string[] = []
  const familyOrderByRegion = new Map<string, string[]>()
  for (const j of args.jobs) {
    const hint = args.hintByJob.get(j.id) ?? {}
    const regionId = hint.regionId ?? fallbackRegion.id
    const familyId = hint.familyId ?? 'platform'
    if (!familyOrderByRegion.has(regionId)) {
      regionOrder.push(regionId)
      familyOrderByRegion.set(regionId, [])
    }
    if (!familyOrderByRegion.get(regionId)!.includes(familyId)) {
      familyOrderByRegion.get(regionId)!.push(familyId)
    }
    const key = `${regionId}:${familyId}`
    const existing = buckets.get(key)
    if (existing) {
      existing.rows.push({
        jobId: j.id,
        label: hint.label ?? j.jobName,
        status: j.status,
        durationLabel: formatDuration(j.durationMs),
      })
    } else {
      const region = regionById.get(regionId) ?? fallbackRegion
      const family = familyById.get(familyId)
      buckets.set(key, {
        id: key,
        regionId,
        regionLabel: region.label,
        regionMeta: region.meta,
        isRegionHeader: false, // set below after we know order
        familyId,
        familyLabel: family?.label ?? familyId.toUpperCase(),
        familyColor: family?.color ?? '#94A3B8',
        familyDesc: familyDesc[familyId] ?? '',
        rows: [
          {
            jobId: j.id,
            label: hint.label ?? j.jobName,
            status: j.status,
            durationLabel: formatDuration(j.durationMs),
          },
        ],
      })
    }
  }

  // Flatten in (regionOrder, familyOrder) order, stamping the first
  // entry of each region as the region header.
  const out: FlowGroupRow[] = []
  for (const regionId of regionOrder) {
    const families = familyOrderByRegion.get(regionId) ?? []
    let isFirst = true
    for (const familyId of families) {
      const g = buckets.get(`${regionId}:${familyId}`)
      if (!g) continue
      out.push({ ...g, isRegionHeader: isFirst })
      isFirst = false
    }
  }
  return out
}

function formatDuration(ms: number): string {
  if (!Number.isFinite(ms) || ms <= 0) return ''
  const totalSec = Math.floor(ms / 1000)
  const h = Math.floor(totalSec / 3600)
  const m = Math.floor((totalSec % 3600) / 60)
  const s = totalSec % 60
  if (h > 0) return `${h}h ${m}m`
  if (m > 0) return `${m}m ${s}s`
  return `${s}s`
}
