/**
 * deps-graph.fixture.ts — shared mock Job graph for the dependency
 * visualization widget + Gantt timeline. Lives under `src/test/fixtures/`
 * per the contract in epic openova-io/openova#204; sibling agents
 * (JobsTable, JobDetail) may import from here too.
 *
 * The Job shape mirrors the *evolved* contract from the epic — the
 * existing src/pages/sovereign/jobs.ts type does not yet carry
 * `dependsOn`/`startedAt`/`finishedAt` fields, so these fixtures
 * intentionally use a richer shape via the local `Job` interface
 * exported below. Once the backend lands and src/pages/sovereign/jobs.ts
 * is updated, fixtures can re-export the canonical type.
 */
export type JobUiStatus = 'pending' | 'running' | 'succeeded' | 'failed'

export interface Job {
  id: string
  app: string
  title: string
  status: JobUiStatus
  /** ISO timestamp — when the job started executing (null if pending). */
  startedAt: string | null
  /** ISO timestamp — when the job finished (null if pending or running). */
  finishedAt: string | null
  /** Job IDs this job depends on. May reference IDs not in the same list. */
  dependsOn: string[]
}

/**
 * Five-job graph used by depsLayout.test.ts. Each job has 0–2 deps.
 * Topology:
 *
 *   tofu-init  ──►  tofu-plan  ──►  tofu-apply  ──►  flux-bootstrap
 *                                          ╲
 *                                           ──►  bp-cilium
 *
 * Layered layout expected:
 *   layer 0: tofu-init
 *   layer 1: tofu-plan
 *   layer 2: tofu-apply
 *   layer 3: flux-bootstrap, bp-cilium
 */
export const FIVE_JOB_GRAPH: Job[] = [
  {
    id: 'tofu-init',
    app: 'infrastructure',
    title: 'terraform init',
    status: 'succeeded',
    startedAt: '2026-04-29T10:00:00Z',
    finishedAt: '2026-04-29T10:00:30Z',
    dependsOn: [],
  },
  {
    id: 'tofu-plan',
    app: 'infrastructure',
    title: 'terraform plan',
    status: 'succeeded',
    startedAt: '2026-04-29T10:00:30Z',
    finishedAt: '2026-04-29T10:01:00Z',
    dependsOn: ['tofu-init'],
  },
  {
    id: 'tofu-apply',
    app: 'infrastructure',
    title: 'terraform apply',
    status: 'succeeded',
    startedAt: '2026-04-29T10:01:00Z',
    finishedAt: '2026-04-29T10:03:30Z',
    dependsOn: ['tofu-plan'],
  },
  {
    id: 'flux-bootstrap',
    app: 'cluster-bootstrap',
    title: 'Bootstrap Flux',
    status: 'running',
    startedAt: '2026-04-29T10:03:30Z',
    finishedAt: null,
    dependsOn: ['tofu-apply'],
  },
  {
    id: 'bp-cilium',
    app: 'bp-cilium',
    title: 'Install Cilium',
    status: 'pending',
    startedAt: null,
    finishedAt: null,
    dependsOn: ['tofu-apply'],
  },
]

/**
 * Three-node graph used by JobDependenciesGraph.test.tsx — a single chain
 * `a -> b -> c` so the widget renders 3 nodes + 2 edges.
 */
export const THREE_NODE_CHAIN: Job[] = [
  {
    id: 'a',
    app: 'infrastructure',
    title: 'A',
    status: 'succeeded',
    startedAt: '2026-04-29T10:00:00Z',
    finishedAt: '2026-04-29T10:00:30Z',
    dependsOn: [],
  },
  {
    id: 'b',
    app: 'infrastructure',
    title: 'B',
    status: 'running',
    startedAt: '2026-04-29T10:00:30Z',
    finishedAt: null,
    dependsOn: ['a'],
  },
  {
    id: 'c',
    app: 'bp-cilium',
    title: 'C',
    status: 'pending',
    startedAt: null,
    finishedAt: null,
    dependsOn: ['b'],
  },
]
