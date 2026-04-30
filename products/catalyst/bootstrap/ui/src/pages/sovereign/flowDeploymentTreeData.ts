/**
 * flowDeploymentTreeData.ts — pure helper that builds FlowGroupRow[]
 * from a flat job list + per-job hint lookup.
 *
 * Lives in its own module (not co-located with FlowDeploymentTree.tsx)
 * so the React component file only exports React components — keeps
 * Vite's HMR fast-refresh happy.
 *
 * Also exports a small TWO-REGION test fixture (DEMO_TWO_REGION_FIXTURE)
 * used by FlowDeploymentTree.test.tsx + flowLayoutV4.test.ts to lock
 * the multi-region rendering contract:
 *
 *   • REGIONS: TON1 Falkenstein, NBG1 Nuremberg (matches the canonical
 *     mockup at marketing/mockups/provision-mockup-v4.png).
 *   • JOBS: 6 representative jobs across 3 families per region (=12).
 *   • HINTS: explicit regionId + familyId per job.
 */

import type { Job, JobStatus } from '@/lib/jobs.types'
import type {
  FlowFamily,
  FlowNodeHints,
  FlowRegion,
} from '@/lib/flowLayoutV4'
import type { FlowGroupRow } from './FlowDeploymentTree'

/* ──────────────────────────────────────────────────────────────────
 * Multi-region test fixture
 *
 * Exported so unit tests + Storybook stories don't have to re-derive
 * the wizard-store shape every time. Two regions, six jobs each, all
 * statuses represented (succeeded / running / pending / failed) so the
 * canvas's status-colour logic gets exercised.
 *
 * Region ids match the canonical mockup verbatim — touching them
 * breaks downstream test fixtures.
 * ────────────────────────────────────────────────────────────────── */

export const DEMO_TWO_REGIONS: FlowRegion[] = [
  { id: 'fsn1', label: 'FSN1 · Falkenstein', meta: 'Hetzner · Primary' },
  { id: 'nbg1', label: 'NBG1 · Nuremberg', meta: 'Hetzner · Secondary' },
]

interface DemoJob {
  id: string
  jobName: string
  appId: string
  family: string
  region: string
  stage: number
  status: JobStatus
  durationMs: number
  dependsOn: readonly string[]
}

const DEMO_JOBS_RAW: readonly DemoJob[] = [
  // ── FSN1 (primary region) ──
  { id: 'install-vms::fsn1',     jobName: 'Provision VMs',    appId: 'infrastructure',     family: 'catalyst', region: 'fsn1', stage: 1, status: 'succeeded', durationMs: 72_000, dependsOn: [] },
  { id: 'install-k3s::fsn1',     jobName: 'K3s Cluster',      appId: 'cluster-bootstrap',  family: 'catalyst', region: 'fsn1', stage: 2, status: 'succeeded', durationMs: 104_000, dependsOn: ['install-vms::fsn1'] },
  { id: 'install-cilium::fsn1',  jobName: 'Cilium',           appId: 'bp-cilium',          family: 'spine',    region: 'fsn1', stage: 3, status: 'running',   durationMs: 41_000, dependsOn: ['install-k3s::fsn1'] },
  { id: 'install-flux::fsn1',    jobName: 'Flux CD',          appId: 'bp-flux',            family: 'pilot',    region: 'fsn1', stage: 3, status: 'running',   durationMs: 32_000, dependsOn: ['install-k3s::fsn1'] },
  { id: 'install-certmgr::fsn1', jobName: 'cert-manager',     appId: 'bp-cert-manager',    family: 'guardian', region: 'fsn1', stage: 4, status: 'pending',   durationMs: 0,      dependsOn: ['install-flux::fsn1'] },
  { id: 'install-cnpg::fsn1',    jobName: 'CloudNativePG',    appId: 'bp-cnpg',            family: 'fabric',   region: 'fsn1', stage: 5, status: 'pending',   durationMs: 0,      dependsOn: ['install-certmgr::fsn1'] },
  // ── NBG1 (secondary region) ──
  { id: 'install-vms::nbg1',     jobName: 'Provision VMs',    appId: 'infrastructure',     family: 'catalyst', region: 'nbg1', stage: 1, status: 'succeeded', durationMs: 80_000, dependsOn: [] },
  { id: 'install-k3s::nbg1',     jobName: 'K3s Cluster',      appId: 'cluster-bootstrap',  family: 'catalyst', region: 'nbg1', stage: 2, status: 'running',   durationMs: 50_000, dependsOn: ['install-vms::nbg1'] },
  { id: 'install-cilium::nbg1',  jobName: 'Cilium',           appId: 'bp-cilium',          family: 'spine',    region: 'nbg1', stage: 3, status: 'pending',   durationMs: 0,      dependsOn: ['install-cilium::fsn1'] }, // cross-region!
  { id: 'install-netbird::nbg1', jobName: 'NetBird (mirror)', appId: 'bp-netbird',         family: 'spine',    region: 'nbg1', stage: 3, status: 'pending',   durationMs: 0,      dependsOn: ['install-k3s::nbg1'] },
  { id: 'install-keycloak::nbg1',jobName: 'Keycloak',         appId: 'bp-keycloak',        family: 'guardian', region: 'nbg1', stage: 4, status: 'pending',   durationMs: 0,      dependsOn: ['install-netbird::nbg1'] },
  { id: 'install-grafana::nbg1', jobName: 'Grafana',          appId: 'bp-grafana',         family: 'insights', region: 'nbg1', stage: 5, status: 'failed',    durationMs: 18_000, dependsOn: ['install-netbird::nbg1'] },
]

/** The Job[] portion of the fixture — fits the Job interface. */
export const DEMO_TWO_REGION_JOBS: readonly Job[] = DEMO_JOBS_RAW.map((j) => ({
  id: j.id,
  jobName: j.jobName,
  appId: j.appId,
  batchId: j.appId === 'infrastructure'
    ? 'infrastructure'
    : j.appId === 'cluster-bootstrap'
      ? 'bootstrap'
      : 'applications',
  dependsOn: j.dependsOn.slice(),
  status: j.status,
  startedAt: j.status === 'pending' ? null : new Date(Date.now() - j.durationMs).toISOString(),
  finishedAt: j.status === 'succeeded' || j.status === 'failed'
    ? new Date(Date.now()).toISOString()
    : null,
  durationMs: j.durationMs,
}))

/** The hints map portion of the fixture — fits FlowNodeHints. */
export const DEMO_TWO_REGION_HINTS: ReadonlyMap<string, FlowNodeHints> =
  new Map(DEMO_JOBS_RAW.map((j) => [
    j.id,
    {
      regionId: j.region,
      familyId: j.family,
      stage: j.stage,
      label: j.jobName,
    } satisfies FlowNodeHints,
  ]))

/** Convenience: bundles regions + jobs + hints. */
export const DEMO_TWO_REGION_FIXTURE = Object.freeze({
  regions: DEMO_TWO_REGIONS,
  jobs: DEMO_TWO_REGION_JOBS,
  hints: DEMO_TWO_REGION_HINTS,
})

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
