/**
 * jobsAdapter.ts — bridge between the legacy reducer-derived Job model
 * (./jobs.ts — used by per-component status pills, deep-link banners,
 * and the canonical core/console event vocabulary) and the recursive
 * Job tree the canvas / table consume (issue #351).
 *
 * Why a separate file?
 *   • ./jobs.ts owns RICH state (steps, app classification, noAppLink)
 *     that AdminPage / AppDetail still need for status pills and the
 *     dependencies viz.
 *   • lib/jobs.types.ts owns the recursive tree shape (parentId +
 *     childIds + type) the canvas + per-job detail page consume.
 *   • Mixing the two would couple the canvas render to the reducer
 *     internals; instead, this adapter is the ONLY place where the
 *     mapping lives. When the catalyst-api jobs endpoint takes over,
 *     the canvas swaps `adaptDerivedJobsToFlat()` for the API call and
 *     the canvas surface stays unchanged.
 *
 * Tree shape emitted:
 *
 *     phase-0-infra (group, "Phase 0 — Infrastructure")
 *         ├── install-tofu-init
 *         ├── install-tofu-plan
 *         ├── install-tofu-apply
 *         └── install-tofu-output
 *     cluster-bootstrap (group, "Cluster Bootstrap")
 *         └── install-flux-bootstrap
 *     applications (group, "Applications")
 *         ├── install-cilium
 *         ├── install-cert-manager
 *         └── …
 *
 * The three group slugs reflect the three rollups the operator already
 * sees on the AdminPage banners; the canvas + table group identically
 * so there's only one mental model.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), the group
 * slugs and labels live in {@link GROUPS}; consumers reading the slug
 * directly should reference that map.
 */

import type { Job as DerivedJob } from './jobs'
import type { Job, JobStatus, JobType } from '@/lib/jobs.types'

/* ──────────────────────────────────────────────────────────────────
 * Group taxonomy — single source of truth
 * ────────────────────────────────────────────────────────────────── */

export const GROUP_PHASE_0 = 'phase-0-infra'
// Slug for the synthesised "Cluster Bootstrap" group. Distinct from the
// per-leaf bootstrap job's id (which is the bare `cluster-bootstrap`)
// — the two MUST NOT collide because byId.set(j.id, j) is last-wins.
// Bug #476: when this slug equalled the leaf id, the leaf overwrote
// the group in the byId map, leaving the leaf with `parentId === its
// own id`. The layout's isVisible() walked that self-reference forever
// and the browser hung the moment the operator clicked any job link.
export const GROUP_CLUSTER_BOOTSTRAP = 'phase-1-bootstrap'
export const GROUP_APPLICATIONS = 'applications'

const GROUP_DISPLAY: Record<string, string> = {
  [GROUP_PHASE_0]: 'Phase 0 — Infrastructure',
  [GROUP_CLUSTER_BOOTSTRAP]: 'Cluster Bootstrap',
  [GROUP_APPLICATIONS]: 'Applications',
}

/** Map the legacy JobUiStatus vocabulary to JobStatus 1:1. */
function mapStatus(s: DerivedJob['status']): JobStatus {
  switch (s) {
    case 'running':
      return 'running'
    case 'succeeded':
      return 'succeeded'
    case 'failed':
      return 'failed'
    case 'pending':
    default:
      return 'pending'
  }
}

/** Pick the parent group slug from the legacy `app` classification. */
function groupOf(app: DerivedJob['app']): string {
  if (app === 'infrastructure') return GROUP_PHASE_0
  if (app === 'cluster-bootstrap') return GROUP_CLUSTER_BOOTSTRAP
  return GROUP_APPLICATIONS
}

/**
 * Compute the wall-clock duration for a job from its step list.
 *
 *   • succeeded / failed: time between the first event and the last.
 *     If the reducer captured both, this is the source-of-truth value.
 *   • running: now - first event time.
 *   • pending: 0 (the table renders "—" for zero durations).
 *
 * Conservative on missing data — returns 0 when no usable timestamps
 * are present.
 */
function durationOf(job: DerivedJob): number {
  if (job.steps.length === 0) return 0
  const times: number[] = []
  for (const s of job.steps) {
    if (!s.startedAt) continue
    const t = new Date(s.startedAt).getTime()
    if (Number.isFinite(t) && t > 0) times.push(t)
  }
  if (times.length === 0) return 0
  const start = Math.min(...times)
  if (job.status === 'succeeded' || job.status === 'failed') {
    const end = Math.max(...times)
    return Math.max(0, end - start)
  }
  return Math.max(0, Date.now() - start)
}

/** First step's timestamp — used as the table's startedAt column. */
function startedAtOf(job: DerivedJob): string | null {
  for (const s of job.steps) {
    if (s.startedAt) return s.startedAt
  }
  return null
}

/** Last step's timestamp on terminal jobs — used as finishedAt. */
function finishedAtOf(job: DerivedJob): string | null {
  if (job.status !== 'succeeded' && job.status !== 'failed') return null
  let last: string | null = null
  for (const s of job.steps) {
    if (s.startedAt) last = s.startedAt
  }
  return last
}

/* ──────────────────────────────────────────────────────────────────
 * Group rollup — mirrors the backend's deriveTreeView contract
 * ────────────────────────────────────────────────────────────────── */

interface Rollup {
  status: JobStatus
  startedAt: string | null
  finishedAt: string | null
  durationMs: number
  childIds: string[]
}

function rollupGroup(children: readonly Job[]): Rollup {
  if (children.length === 0) {
    return { status: 'pending', startedAt: null, finishedAt: null, durationMs: 0, childIds: [] }
  }
  const childIds = children.map((c) => c.id)
  let earliest: number | null = null
  let earliestIso: string | null = null
  let latest: number | null = null
  let latestIso: string | null = null
  let hasFailed = false
  let hasRunning = false
  let hasPending = false
  let allTerminal = true
  for (const c of children) {
    switch (c.status) {
      case 'failed':
        hasFailed = true
        break
      case 'running':
        hasRunning = true
        allTerminal = false
        break
      case 'pending':
        hasPending = true
        allTerminal = false
        break
    }
    if (c.startedAt) {
      const t = Date.parse(c.startedAt)
      if (Number.isFinite(t) && (earliest === null || t < earliest)) {
        earliest = t
        earliestIso = c.startedAt
      }
    }
    if (c.finishedAt) {
      const t = Date.parse(c.finishedAt)
      if (Number.isFinite(t) && (latest === null || t > latest)) {
        latest = t
        latestIso = c.finishedAt
      }
    }
  }
  let status: JobStatus = 'succeeded'
  if (hasFailed) status = 'failed'
  else if (hasRunning) status = 'running'
  else if (hasPending) status = 'pending'

  const finishedIso = allTerminal ? latestIso : null
  const durationMs =
    earliest !== null && finishedIso !== null && latest !== null
      ? Math.max(0, latest - earliest)
      : 0

  return {
    status,
    startedAt: earliestIso,
    finishedAt: finishedIso,
    durationMs,
    childIds,
  }
}

/* ──────────────────────────────────────────────────────────────────
 * Public adapter
 * ────────────────────────────────────────────────────────────────── */

/**
 * Adapt a legacy DerivedJob[] (from ./jobs.ts deriveJobs) into the
 * recursive Job tree the canvas + table consume.
 *
 * Output layout: every leaf job is followed by the synthesised parent
 * group row that owns it (one row per non-empty group). Stable: re-
 * running on the same input produces identical output. No randomness,
 * no cached state.
 *
 * Dep chaining: each per-component job depends on the cluster-
 * bootstrap leaf (and that on the last tofu leaf). Without this the
 * canvas would render isolated forests for the three groups.
 */
export function adaptDerivedJobsToFlat(derived: readonly DerivedJob[]): Job[] {
  const tofuOrder = derived.filter((j) => j.app === 'infrastructure').map((j) => j.id)
  const lastTofuId = tofuOrder.length > 0 ? tofuOrder[tofuOrder.length - 1] : null
  const bootstrap = derived.find((j) => j.app === 'cluster-bootstrap')

  // Build the leaves first — group parentId is computed below so the
  // ids are stable and self-referential isn't possible.
  const leaves: Job[] = derived.map((j) => {
    const groupSlug = groupOf(j.app)
    let dependsOn: string[] = []
    if (j.app === 'infrastructure') {
      const prev = tofuOrder.indexOf(j.id) - 1
      if (prev >= 0) dependsOn = [tofuOrder[prev]!]
    } else if (j.app === 'cluster-bootstrap') {
      if (lastTofuId) dependsOn = [lastTofuId]
    } else {
      if (bootstrap) dependsOn = [bootstrap.id]
    }
    return {
      id: j.id,
      jobName: j.title,
      type: 'install' as JobType,
      appId: j.app,
      parentId: groupSlug,
      dependsOn,
      childIds: [],
      status: mapStatus(j.status),
      startedAt: startedAtOf(j),
      finishedAt: finishedAtOf(j),
      durationMs: durationOf(j),
    }
  })

  // Materialise each non-empty parent group with its rollup.
  const byGroup = new Map<string, Job[]>()
  for (const leaf of leaves) {
    const arr = byGroup.get(leaf.parentId) ?? []
    arr.push(leaf)
    byGroup.set(leaf.parentId, arr)
  }
  const groups: Job[] = []
  for (const slug of [GROUP_PHASE_0, GROUP_CLUSTER_BOOTSTRAP, GROUP_APPLICATIONS]) {
    const kids = byGroup.get(slug)
    if (!kids || kids.length === 0) continue
    const rollup = rollupGroup(kids)
    groups.push({
      id: slug,
      jobName: slug,
      displayName: GROUP_DISPLAY[slug] ?? slug,
      type: 'group',
      appId: '',
      parentId: '',
      dependsOn: [],
      childIds: rollup.childIds,
      status: rollup.status,
      startedAt: rollup.startedAt,
      finishedAt: rollup.finishedAt,
      durationMs: rollup.durationMs,
    })
  }

  return [...groups, ...leaves]
}
