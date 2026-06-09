/**
 * jobs.fixture.ts — shared sample of 8 leaf jobs across 2 parent
 * groups with a mix of statuses. Used by:
 *
 *   • src/pages/sovereign/JobsPage.tsx — when the catalyst-api jobs
 *     endpoint isn't responding yet
 *   • src/pages/sovereign/JobsTable.test.tsx — search / sort / filter
 *     coverage
 *   • src/pages/sovereign/AppDetail.test.tsx — Jobs tab filtering
 *   • the e2e cosmetic-guards spec — through a global fixture seeded
 *     into window for the JobsPage to pick up
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), the fixture
 * lives in one place only and every consumer imports it. Mutating
 * jobs here updates every test surface in lockstep.
 *
 * Tree shape (recursive Job model — issue #351):
 *
 *     phase-0-infra (group, "Phase 0 — Infrastructure")
 *         ├── job-tofu-init        succeeded
 *         ├── job-tofu-plan        succeeded
 *         ├── job-tofu-apply       succeeded
 *         ├── job-tofu-output      succeeded
 *         └── job-flux-bootstrap   running     (cluster-bootstrap leaf
 *                                              kept here for the
 *                                              fixture's 5-entry
 *                                              footprint)
 *     applications (group, "Applications")
 *         ├── job-install-cilium       succeeded
 *         ├── job-install-cert-manager pending
 *         └── job-install-flux         failed
 *
 * Status distribution covers every bucket so the canvas + table chrome
 * (badges, ring colours, search, sort) is exercised end-to-end.
 */

import type { Job } from '@/lib/jobs.types'
import {
  GROUP_PHASE_0,
  GROUP_APPLICATIONS,
} from '@/pages/sovereign/jobsAdapter'

/** Leaf install jobs only — the {@link FIXTURE_JOBS} export below adds
 *  the synthesised parent group rows.  */
const LEAVES: Job[] = [
  {
    id: 'job-tofu-init',
    jobName: 'Provision infrastructure — terraform init',
    type: 'install',
    appId: 'infrastructure',
    parentId: GROUP_PHASE_0,
    dependsOn: [],
    childIds: [],
    status: 'succeeded',
    startedAt: '2026-04-29T10:00:00Z',
    finishedAt: '2026-04-29T10:00:12Z',
    durationMs: 12_000,
  },
  {
    id: 'job-tofu-plan',
    jobName: 'Provision infrastructure — terraform plan',
    type: 'install',
    appId: 'infrastructure',
    parentId: GROUP_PHASE_0,
    dependsOn: ['job-tofu-init'],
    childIds: [],
    status: 'succeeded',
    startedAt: '2026-04-29T10:00:12Z',
    finishedAt: '2026-04-29T10:00:30Z',
    durationMs: 18_000,
  },
  {
    id: 'job-tofu-apply',
    jobName: 'Provision infrastructure — terraform apply',
    type: 'install',
    appId: 'infrastructure',
    parentId: GROUP_PHASE_0,
    dependsOn: ['job-tofu-plan'],
    childIds: [],
    status: 'succeeded',
    startedAt: '2026-04-29T10:00:30Z',
    finishedAt: '2026-04-29T10:02:14Z',
    durationMs: 104_000,
  },
  {
    id: 'job-tofu-output',
    jobName: 'Provision infrastructure — terraform output',
    type: 'install',
    appId: 'infrastructure',
    parentId: GROUP_PHASE_0,
    dependsOn: ['job-tofu-apply'],
    childIds: [],
    status: 'succeeded',
    startedAt: '2026-04-29T10:02:14Z',
    finishedAt: '2026-04-29T10:02:18Z',
    durationMs: 4_000,
  },
  {
    id: 'job-flux-bootstrap',
    jobName: 'Bootstrap Flux on cluster',
    type: 'install',
    appId: 'cluster-bootstrap',
    parentId: GROUP_PHASE_0,
    dependsOn: ['job-tofu-output'],
    childIds: [],
    status: 'running',
    startedAt: '2026-04-29T10:02:18Z',
    finishedAt: null,
    durationMs: 22_000,
  },
  {
    id: 'job-install-cilium',
    jobName: 'Install Cilium',
    type: 'install',
    appId: 'bp-cilium',
    parentId: GROUP_APPLICATIONS,
    dependsOn: ['job-flux-bootstrap'],
    childIds: [],
    status: 'succeeded',
    startedAt: '2026-04-29T10:02:40Z',
    finishedAt: '2026-04-29T10:03:25Z',
    durationMs: 45_000,
  },
  {
    id: 'job-install-cert-manager',
    jobName: 'Install cert-manager',
    type: 'install',
    appId: 'bp-cert-manager',
    parentId: GROUP_APPLICATIONS,
    dependsOn: ['job-install-cilium'],
    childIds: [],
    status: 'pending',
    startedAt: null,
    finishedAt: null,
    durationMs: 0,
  },
  {
    id: 'job-install-flux',
    jobName: 'Install Flux umbrella',
    type: 'install',
    appId: 'bp-flux',
    parentId: GROUP_APPLICATIONS,
    dependsOn: ['job-flux-bootstrap'],
    childIds: [],
    status: 'failed',
    startedAt: '2026-04-29T10:02:40Z',
    finishedAt: '2026-04-29T10:03:00Z',
    durationMs: 20_000,
  },
]

const PHASE_0_GROUP: Job = {
  id: GROUP_PHASE_0,
  jobName: GROUP_PHASE_0,
  displayName: 'Phase 0 — Infrastructure',
  type: 'group',
  appId: '',
  parentId: '',
  dependsOn: [],
  childIds: LEAVES.filter((j) => j.parentId === GROUP_PHASE_0).map((j) => j.id),
  status: 'running',
  startedAt: '2026-04-29T10:00:00Z',
  finishedAt: null,
  durationMs: 0,
}

const APPLICATIONS_GROUP: Job = {
  id: GROUP_APPLICATIONS,
  jobName: GROUP_APPLICATIONS,
  displayName: 'Applications',
  type: 'group',
  appId: '',
  parentId: '',
  dependsOn: [],
  childIds: LEAVES.filter((j) => j.parentId === GROUP_APPLICATIONS).map((j) => j.id),
  status: 'failed',
  startedAt: '2026-04-29T10:02:40Z',
  finishedAt: null,
  durationMs: 0,
}

/**
 * Fixture in canonical canvas order: parent groups first, leaves
 * after. Consumers that need leaves only can `.filter((j) => j.type
 * === 'install')`.
 */
export const FIXTURE_JOBS: Job[] = [PHASE_0_GROUP, APPLICATIONS_GROUP, ...LEAVES]
