/**
 * jobs.fixture.ts — shared sample of 8 jobs across 2 batches with a mix
 * of status values. Used by:
 *
 *   • src/pages/sovereign/JobsPage.tsx — when the catalyst-api jobs
 *     endpoint isn't responding yet (the backend on #205 is the data
 *     source; #204 ships the table without blocking on it).
 *   • src/pages/sovereign/JobsTable.test.tsx — search / sort / filter
 *     coverage.
 *   • src/pages/sovereign/AppDetail.test.tsx — Jobs tab filtering.
 *   • the e2e cosmetic-guards spec — through a global fixture seeded
 *     into window for the JobsPage to pick up.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), the fixture
 * lives in one place only and every consumer imports it. Mutating jobs
 * here updates every test surface in lockstep.
 */

import type { Batch, Job } from '@/lib/jobs.types'

/**
 * batch-1 — Phase 0 + cluster-bootstrap (5 jobs):
 *   • tofu-init            succeeded
 *   • tofu-plan            succeeded
 *   • tofu-apply           succeeded
 *   • tofu-output          succeeded
 *   • flux-bootstrap       running
 *
 * batch-2 — Per-Application installs (3 jobs):
 *   • install-cilium       succeeded
 *   • install-cert-manager pending
 *   • install-flux         failed
 *
 * Distribution covers every status bucket so the table chrome (badges,
 * progress bar colours, search, sort) is exercised end-to-end.
 */
export const FIXTURE_JOBS: Job[] = [
  {
    id: 'job-tofu-init',
    jobName: 'Provision Hetzner — terraform init',
    appId: 'infrastructure',
    batchId: 'batch-1',
    dependsOn: [],
    status: 'succeeded',
    startedAt: '2026-04-29T10:00:00Z',
    finishedAt: '2026-04-29T10:00:12Z',
    durationMs: 12_000,
  },
  {
    id: 'job-tofu-plan',
    jobName: 'Provision Hetzner — terraform plan',
    appId: 'infrastructure',
    batchId: 'batch-1',
    dependsOn: ['job-tofu-init'],
    status: 'succeeded',
    startedAt: '2026-04-29T10:00:12Z',
    finishedAt: '2026-04-29T10:00:30Z',
    durationMs: 18_000,
  },
  {
    id: 'job-tofu-apply',
    jobName: 'Provision Hetzner — terraform apply',
    appId: 'infrastructure',
    batchId: 'batch-1',
    dependsOn: ['job-tofu-plan'],
    status: 'succeeded',
    startedAt: '2026-04-29T10:00:30Z',
    finishedAt: '2026-04-29T10:02:14Z',
    durationMs: 104_000,
  },
  {
    id: 'job-tofu-output',
    jobName: 'Provision Hetzner — terraform output',
    appId: 'infrastructure',
    batchId: 'batch-1',
    dependsOn: ['job-tofu-apply'],
    status: 'succeeded',
    startedAt: '2026-04-29T10:02:14Z',
    finishedAt: '2026-04-29T10:02:18Z',
    durationMs: 4_000,
  },
  {
    id: 'job-flux-bootstrap',
    jobName: 'Bootstrap Flux on cluster',
    appId: 'cluster-bootstrap',
    batchId: 'batch-1',
    dependsOn: ['job-tofu-output'],
    status: 'running',
    startedAt: '2026-04-29T10:02:18Z',
    finishedAt: null,
    durationMs: 22_000,
  },
  {
    id: 'job-install-cilium',
    jobName: 'Install Cilium',
    appId: 'bp-cilium',
    batchId: 'batch-2',
    dependsOn: ['job-flux-bootstrap'],
    status: 'succeeded',
    startedAt: '2026-04-29T10:02:40Z',
    finishedAt: '2026-04-29T10:03:25Z',
    durationMs: 45_000,
  },
  {
    id: 'job-install-cert-manager',
    jobName: 'Install cert-manager',
    appId: 'bp-cert-manager',
    batchId: 'batch-2',
    dependsOn: ['job-install-cilium'],
    status: 'pending',
    startedAt: null,
    finishedAt: null,
    durationMs: 0,
  },
  {
    id: 'job-install-flux',
    jobName: 'Install Flux umbrella',
    appId: 'bp-flux',
    batchId: 'batch-2',
    dependsOn: ['job-flux-bootstrap'],
    status: 'failed',
    startedAt: '2026-04-29T10:02:40Z',
    finishedAt: '2026-04-29T10:03:00Z',
    durationMs: 20_000,
  },
]

/**
 * Pre-computed batch rollups derived from FIXTURE_JOBS. Backend (#205)
 * computes the same numbers server-side; the UI keeps a derivation
 * helper too so tests don't drift if the fixture grows.
 */
export const FIXTURE_BATCHES: Batch[] = [
  {
    batchId: 'batch-1',
    total: 5,
    finished: 4,
    failed: 0,
    running: 1,
    pending: 0,
  },
  {
    batchId: 'batch-2',
    total: 3,
    finished: 2,
    failed: 1,
    running: 0,
    pending: 1,
  },
]

/**
 * Pure derivation helper — re-computes Batch rollups from a Job list.
 * Used by the JobsPage when only `Job[]` is available (e.g. live SSE
 * stream replay) to render BatchProgress without a separate API call.
 */
export function deriveBatches(jobs: readonly Job[]): Batch[] {
  const map = new Map<string, Batch>()
  for (const j of jobs) {
    let b = map.get(j.batchId)
    if (!b) {
      b = { batchId: j.batchId, total: 0, finished: 0, failed: 0, running: 0, pending: 0 }
      map.set(j.batchId, b)
    }
    b.total += 1
    switch (j.status) {
      case 'succeeded': b.finished += 1; break
      case 'failed':    b.finished += 1; b.failed += 1; break
      case 'running':   b.running += 1; break
      case 'pending':   b.pending += 1; break
    }
  }
  // Stable order: batchId ascending so callers can render a deterministic
  // list across renders.
  return [...map.values()].sort((a, b) => a.batchId.localeCompare(b.batchId))
}
