/**
 * jobsToOrganic — adapt the /jobs job-store `Job[]` into the side tables
 * (`hints` / `regions` / `families`) that the CANONICAL organic canvas
 * (`FlowCanvasOrganic` via `flowLayoutOrganic`) consumes.
 *
 * Rationale (Refs #6703): the /jobs Graph view must reuse the SAME
 * natural-view DAG the rest of the operator UX is tuned against
 * (`FlowCanvasOrganic` — the component the founder ratified after
 * rejecting the lane-layout scaffolding, see FlowPage.tsx header), NOT a
 * bespoke SVG renderer. `flowLayoutOrganic()` already accepts a `Job[]`
 * directly; the only missing piece is a `Job[] → {hints,regions,families}`
 * builder — `flowStreamToOrganic` builds those from the openova-flow SSE
 * `FlowNode[]`, but /jobs already has the flat `Job[]` (from the
 * deployment job stream), so list-view and graph-view stay on ONE data
 * source. This mirrors flowStreamToOrganic.ts:149-228 field-for-field:
 *
 *   • family  = the job's KIND (install / cron / step / …), labelled with
 *     the real engine name via JOB_ENGINE_LABELS so the canvas rings +
 *     legend read the same engine names as the /jobs chips.
 *   • region  = the job's `region` (blank → 'primary').
 *
 * The fold / depth helpers (defaultFoldedAtContainmentDepth,
 * descendantCountByGroup, maxContainmentDepth) already operate on `Job[]`
 * and are consumed unchanged by the graph view — this module only fills
 * the region/family gap flowStreamToOrganic filled for the stream path.
 */

import type { Job, JobKind } from './jobs.types'
import { jobKind, JOB_ENGINE_LABELS } from './jobs.types'
import type {
  OrganicFamily,
  OrganicNodeHints,
  OrganicRegion,
} from './flowLayoutOrganic'

/**
 * Per-kind ring colour for the organic bubbles. Distinct hue per engine
 * so the canvas is legible by kind at a glance; aligned with the /jobs
 * chip category tints. Any kind not listed falls back to slate.
 */
const KIND_FAMILY_COLOR: Record<JobKind, string> = {
  install: '#6366F1', // indigo — HelmRelease
  reconcile: '#0EA5E9', // sky — Kustomization
  reconciler: '#14B8A6', // teal — Deployment reconciler
  cron: '#A855F7', // violet — CronJob
  step: '#F59E0B', // amber — Job (step)
  task: '#F97316', // orange — Job (task)
  mutation: '#EC4899', // pink — Crossplane
  lifecycle: '#22C55E', // green — OpenTofu
  group: '#94A3B8', // slate — synthesised parent
}

export interface JobsOrganicInputs {
  hints: Map<string, OrganicNodeHints>
  regions: OrganicRegion[]
  families: OrganicFamily[]
  /** ids of the `type:'group'` parent rows (drives fold defaults). */
  groupIds: Set<string>
}

export interface RegionLabel {
  id: string
  label: string
  meta?: string
}

/**
 * Build the organic side tables from a flat `Job[]`.
 *
 * @param jobs         the job-store rows (the SAME list the /jobs table renders)
 * @param regionLabels optional pretty region labels (e.g. from the wizard
 *                     store) keyed by region id; falls back to UPPERCASE id.
 */
export function jobsToOrganicInputs(
  jobs: readonly Job[],
  regionLabels?: readonly RegionLabel[],
): JobsOrganicInputs {
  const hints = new Map<string, OrganicNodeHints>()
  const familyIds = new Set<JobKind>()
  const regionIds = new Set<string>()
  const groupIds = new Set<string>()

  for (const j of jobs) {
    if (j.type === 'group') groupIds.add(j.id)
    const kind = jobKind(j)
    const regionId = (j.region ?? '').trim()
    familyIds.add(kind)
    if (regionId.length > 0) regionIds.add(regionId)
    hints.set(j.id, {
      regionId: regionId.length > 0 ? regionId : 'primary',
      familyId: kind,
    })
  }

  // Families — one per seen kind, labelled with the real engine name.
  const families: OrganicFamily[] = [...familyIds].sort().map((id) => ({
    id,
    label: JOB_ENGINE_LABELS[id] ?? id,
    color: KIND_FAMILY_COLOR[id] ?? '#94A3B8',
  }))

  // Regions — from the jobs themselves, prettified via regionLabels when
  // available (mirrors flowStreamToOrganic's wizardRegions fallback).
  let regions: OrganicRegion[]
  if (regionIds.size > 0) {
    regions = [...regionIds].sort().map((id) => {
      const pretty = regionLabels?.find((r) => r.id === id)
      return pretty
        ? { id, label: pretty.label, meta: pretty.meta }
        : { id, label: id.toUpperCase() }
    })
  } else {
    regions = [{ id: 'primary', label: 'Primary Region' }]
  }

  return { hints, regions, families, groupIds }
}
