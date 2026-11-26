/**
 * e2e/gate/operations/lib.ts — what the specifications of gate check FND-G21 share (FND-163).
 *
 * FND-G21 is the console regression of the jobs pages, the resource views and the cloud and
 * infrastructure read pages: one test per row of docs/ledger/UAT.md that the row map marks
 * test:FND-G21, titled with the row id and its clause. The helpers read the candidate the way the
 * clause names it: the console's own API through the signed-in context, the clusters through
 * regionKubectl, the catalyst-api's own log. A helper never turns a missing object into an empty
 * answer: what a clause needs and cannot find fails the test with the reason.
 *
 * Optional inputs of this folder (the gate workflow does not set them yet, tracked by issue #8723; a test
 * that needs one fails with the reason):
 *   GATE_PARENT_URL      the console address of the Sovereign that provisioned the candidate
 *   GATE_PARENT_SESSION  a file holding a console session of that Sovereign's owner
 */

import { execFile } from 'node:child_process'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { expect, consoleURL, regionKubectl, type Region } from '../fixtures.ts'
import type { APIRequestContext, Page } from '@playwright/test'

/** The repository checkout the harness runs from (the candidate's commit, with its history). */
export const REPO_ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '../../../../../../..')

/** A unique token of this run, for objects a test creates and then looks for. */
export const RUN_TOKEN = `${process.env.GITHUB_RUN_ID ?? 'local'}-${Date.now().toString(36)}`.toLowerCase()

export const REGIONS: readonly Region[] = ['a', 'b']

/** One Kubernetes object, as much of it as the clauses read. */
export interface Obj {
  apiVersion?: string
  kind?: string
  metadata: {
    name: string
    namespace?: string
    uid?: string
    labels?: Record<string, string>
    annotations?: Record<string, string>
    ownerReferences?: Array<{ kind: string; name: string }>
    creationTimestamp?: string
  }
  spec?: Record<string, unknown>
  status?: Record<string, unknown>
  data?: Record<string, string>
  type?: string
}

/** kubectl in a region; a non-zero exit fails the test with kubectl's own message. */
export async function kube(region: Region, args: string[], timeoutMs = 120_000): Promise<string> {
  const r = await regionKubectl(region, args, timeoutMs)
  if (r.code !== 0) throw new Error(`kubectl ${args.join(' ')} (region ${region}) exited ${r.code}: ${r.stderr.trim()}`)
  return r.stdout
}

/** The objects of one kind, in every namespace or in one. */
export async function list(region: Region, kind: string, namespace?: string): Promise<Obj[]> {
  const where = namespace ? ['-n', namespace] : ['-A']
  const out = JSON.parse(await kube(region, ['get', kind, ...where, '-o', 'json'])) as { items?: Obj[] }
  return out.items ?? []
}

/** The objects of one cluster-scoped kind. */
export async function listCluster(region: Region, kind: string): Promise<Obj[]> {
  const out = JSON.parse(await kube(region, ['get', kind, '-o', 'json'])) as { items?: Obj[] }
  return out.items ?? []
}

/** One object, or null when the cluster has none of that name. */
export async function getObj(region: Region, kind: string, namespace: string, name: string): Promise<Obj | null> {
  const r = await regionKubectl(region, ['get', kind, '-n', namespace, name, '-o', 'json'])
  if (r.code !== 0) {
    if (/NotFound|not found/.test(r.stderr)) return null
    throw new Error(`kubectl get ${kind} -n ${namespace} ${name} exited ${r.code}: ${r.stderr.trim()}`)
  }
  return JSON.parse(r.stdout) as Obj
}

/** A value of an object by a dotted path, for example field(o, 'spec.suspend'). */
export function field(o: unknown, path: string): unknown {
  let v: unknown = o
  for (const k of path.split('.')) {
    if (v === null || typeof v !== 'object') return undefined
    v = (v as Record<string, unknown>)[k]
  }
  return v
}

export function str(o: unknown, path: string): string {
  const v = field(o, path)
  return typeof v === 'string' ? v : ''
}

export function arr(o: unknown, path: string): unknown[] {
  const v = field(o, path)
  return Array.isArray(v) ? v : []
}

export function num(o: unknown, path: string): number {
  const v = field(o, path)
  return typeof v === 'number' ? v : Number.NaN
}

/** The status of a condition of an object: 'True', 'False', 'Unknown' or '' when it has none. */
export function cond(o: unknown, type: string): string {
  const c = arr(o, 'status.conditions').find((x) => str(x, 'type') === type)
  return c ? str(c, 'status') : ''
}

export function ready(o: unknown): string {
  return cond(o, 'Ready')
}

export const nsName = (o: Obj) => `${o.metadata.namespace ?? ''}/${o.metadata.name}`

/** Polls fn until it returns a value that is not null, or fails with what it last saw. */
export async function until<T>(what: string, fn: () => Promise<T | null>, timeoutMs: number, everyMs = 5_000): Promise<T> {
  const end = Date.now() + timeoutMs
  let last: unknown = null
  for (;;) {
    try {
      const v = await fn()
      if (v !== null) return v
    } catch (e) {
      last = e
    }
    if (Date.now() > end) throw new Error(`${what}: not within ${Math.round(timeoutMs / 1000)} s${last ? ` (last: ${String(last)})` : ''}`)
    await new Promise((r) => setTimeout(r, everyMs))
  }
}

/** GET of a console API path through the signed-in context; anything but 200 fails the test. */
export async function apiJSON<T>(page: Page, path: string): Promise<T> {
  const r = await page.request.get(consoleURL(path), { headers: { Accept: 'application/json' }, timeout: 120_000 })
  expect(r.status(), `GET ${path}`).toBe(200)
  return (await r.json()) as T
}

/** The console-internal deployment id of the candidate (GET /api/v1/sovereign/self). */
export async function selfId(page: Page): Promise<string> {
  const self = await apiJSON<{ deploymentId?: string; sovereignFQDN?: string }>(page, '/api/v1/sovereign/self')
  expect(self.deploymentId ?? '', 'the deployment id of GET /api/v1/sovereign/self').not.toBe('')
  return self.deploymentId as string
}

/** One row of GET /api/v1/deployments/{id}/jobs (products/catalyst/bootstrap/api/internal/jobs/types.go). */
export interface JobRow {
  id: string
  jobName: string
  displayName?: string
  type?: string
  kind?: string
  appId?: string
  region?: string
  parentId?: string
  status: string
  reason?: string
  message?: string
  runCount?: number
}

/** The jobs feed: the finite list of /jobs by default, the full inventory with full=true. */
export async function jobsFeed(page: Page, opts: { full?: boolean; status?: string } = {}): Promise<JobRow[]> {
  const id = await selfId(page)
  const q = new URLSearchParams()
  if (opts.full) q.set('inventory', 'full')
  if (opts.status) q.set('status', opts.status)
  const qs = q.toString()
  const body = await apiJSON<{ jobs?: JobRow[] }>(page, `/api/v1/deployments/${encodeURIComponent(id)}/jobs${qs ? `?${qs}` : ''}`)
  expect(Array.isArray(body.jobs), 'the jobs feed answers a list jobs').toBe(true)
  return body.jobs as JobRow[]
}

/** The status vocabulary of a job row (products/catalyst/bootstrap/ui/src/lib/jobs.types.ts). */
export const JOB_STATUSES = ['pending', 'running', 'succeeded', 'failed', 'healthy', 'degraded', 'failing', 'dormant']

/** One cell of GET /api/v1/dashboard/treemap. */
export interface Cell {
  id?: string | null
  name: string
  count?: number
  percentage?: number | null
  children?: Cell[]
}

/** The treemap the Dashboard draws, for a layer stack (group_by), as the console asks for it. */
export async function treemap(page: Page, layers: string[], colorBy = 'health', sizeBy = 'cpu_request'): Promise<Cell[]> {
  const id = await selfId(page)
  const q = new URLSearchParams({ group_by: layers.join(','), color_by: colorBy, size_by: sizeBy, deployment_id: id })
  const body = await apiJSON<{ items?: Cell[] }>(page, `/api/v1/dashboard/treemap?${q.toString()}`)
  expect(Array.isArray(body.items), 'the treemap answers a list items').toBe(true)
  expect((body.items as Cell[]).length, `the treemap of ${layers.join(',')} has cells`).toBeGreaterThan(0)
  return body.items as Cell[]
}

/** Every cell of a treemap tree, depth first. */
export function cells(items: Cell[]): Cell[] {
  return items.flatMap((c) => [c, ...cells(c.children ?? [])])
}

/** The hierarchical topology the cloud pages read (GET /api/v1/deployments/{id}/infrastructure/topology). */
export interface Topology {
  topology: {
    pattern?: string
    regions: Array<{
      id: string
      name: string
      providerRegion: string
      status: string
      clusters: Array<{
        id: string
        name: string
        status: string
        nodeCount: number
        vclusters?: Array<{ id: string; name: string; namespace?: string }>
        loadBalancers?: Array<{ id: string; name: string; publicIP: string; region: string; status: string; listeners?: Array<{ port: number; protocol: string }>; frontDoorKind?: string }>
        nodes?: Array<{ id: string; name: string; role: string; status: string }>
      }>
    }>
  }
}

export async function topology(page: Page): Promise<Topology['topology']> {
  const id = await selfId(page)
  const body = await apiJSON<Topology>(page, `/api/v1/deployments/${encodeURIComponent(id)}/infrastructure/topology`)
  expect(Array.isArray(body.topology?.regions), 'the topology answers a list of regions').toBe(true)
  return body.topology
}

/** The load balancers of the topology. */
export function loadBalancersOf(t: Topology['topology']) {
  return t.regions.flatMap((r) => r.clusters.flatMap((c) => c.loadBalancers ?? []))
}

/** One row of GET /api/v1/deployments/{id}/reconcilers (internal/helmwatch/manage.go). */
export interface ManagedReconciler {
  kind: string
  name: string
  namespace: string
  state: string
  message?: string
  revision?: string
  suspended: boolean
  controller: string
}

export async function reconcilers(page: Page): Promise<ManagedReconciler[]> {
  const id = await selfId(page)
  const body = await apiJSON<{ reconcilers?: ManagedReconciler[]; total?: number }>(page, `/api/v1/deployments/${encodeURIComponent(id)}/reconcilers`)
  expect(Array.isArray(body.reconcilers), 'the reconcilers list answers a list reconcilers').toBe(true)
  return body.reconcilers as ManagedReconciler[]
}

/** Opens a page of the cloud view in its list form, for a kind of the cloud kinds (cloud-list/kinds.ts). */
export async function openCloudList(page: Page, kind: string): Promise<void> {
  await page.goto(consoleURL(`/cloud?view=list&kind=${encodeURIComponent(kind)}`))
  await expect(page.getByTestId(`cloud-list-view-active-${kind}`)).toBeVisible({ timeout: 60_000 })
}

/**
 * The rows a per-kind list page of the cloud view renders (cloud-list/K8sListPage.tsx), counted until
 * the count the clusters hold is reached: the live stream fills the page over a few seconds. The
 * expectation is the count of both regions: the stream of a Sovereign holds every region it registers
 * (handler/k8s.go deploymentScopedClusterIDs, #5571), so a page that shows one region's rows is short.
 */
export async function expectListMatches(page: Page, rowKind: string, countA: number, countB: number): Promise<void> {
  const rows = page.locator(`[data-testid^="cloud-${rowKind}-row-"]`)
  expect(countA, `the clusters of region a hold ${rowKind} objects`).toBeGreaterThan(0)
  const want = countA + countB
  await expect
    .poll(async () => (await rows.count()) - want, { message: `the rows of the ${rowKind} page against the ${want} objects of both regions`, timeout: 90_000, intervals: [2_000] })
    .toBe(0)
}

/** The log lines of the catalyst-api of region a that are JSON records, with their message. */
export interface LogRecord {
  msg: string
  [key: string]: unknown
}

export async function catalystApiLog(since = '3h'): Promise<LogRecord[]> {
  const out = await kube('a', ['-n', 'catalyst-system', 'logs', 'deploy/catalyst-api', '-c', 'catalyst-api', `--since=${since}`], 300_000)
  const recs: LogRecord[] = []
  for (const line of out.split('\n')) {
    const at = line.indexOf('{')
    if (at < 0) continue
    try {
      const r = JSON.parse(line.slice(at)) as LogRecord
      if (typeof r.msg === 'string') recs.push(r)
    } catch {
      // a line that is not a JSON record is not a log record of the catalyst-api
    }
  }
  expect(recs.length, 'the catalyst-api of region a has written JSON log records').toBeGreaterThan(0)
  return recs
}

/** The records of the janitor ([JANITOR] ...) in a log. */
export function janitorRecords(recs: LogRecord[]): LogRecord[] {
  return recs.filter((r) => r.msg.startsWith('[JANITOR]'))
}

/** The namespaces of the Organizations of a region: those labelled with an Organization. */
export async function orgNamespaces(region: Region): Promise<Obj[]> {
  return (await listCluster(region, 'namespaces')).filter((n) => orgOf(n) !== '')
}

export function orgOf(ns: Obj): string {
  return ns.metadata.labels?.['openova.io/organization'] ?? ns.metadata.labels?.['catalyst.openova.io/organization'] ?? ''
}

/** The host namespaces that run a per-Organization vCluster: its control-plane Pod carries app=vcluster. */
export async function vclusterNamespaces(region: Region): Promise<string[]> {
  const pods = await list(region, 'pods')
  return [...new Set(pods.filter((p) => p.metadata.labels?.app === 'vcluster').map((p) => p.metadata.namespace ?? ''))].filter((n) => n !== '').sort()
}

/** The Flux HelmReleases of a region. */
export function helmReleases(region: Region): Promise<Obj[]> {
  return list(region, 'helmreleases.helm.toolkit.fluxcd.io')
}

/** The CloudNativePG Clusters of a region, and whether each is healthy as CloudNativePG reports it. */
export function cnpgClusters(region: Region): Promise<Obj[]> {
  return list(region, 'clusters.postgresql.cnpg.io')
}

export function cnpgHealthy(c: Obj): boolean {
  return /healthy state/i.test(str(c, 'status.phase')) && num(c, 'status.readyInstances') === num(c, 'status.instances')
}

/** Runs a shell command in a container of a Pod. */
export function execIn(region: Region, namespace: string, pod: string, container: string, script: string, timeoutMs = 120_000): Promise<string> {
  return kube(region, ['exec', '-n', namespace, pod, '-c', container, '--', 'sh', '-c', script], timeoutMs)
}

/** git in the repository checkout; a non-zero exit fails the test with git's own message. */
export function git(args: string[]): Promise<string> {
  return new Promise((done, fail) => {
    execFile('git', args, { cwd: REPO_ROOT, maxBuffer: 64 << 20 }, (err, stdout, stderr) => {
      if (err) fail(new Error(`git ${args.join(' ')} failed: ${String(stderr).trim() || err.message}`))
      else done(String(stdout))
    })
  })
}

/** The Sovereign that provisioned the candidate, as the optional inputs of this folder name it. */
export async function parentContext(request: APIRequestContext): Promise<{ url: string; get: (path: string) => ReturnType<APIRequestContext['get']>; post: (path: string, body: unknown) => ReturnType<APIRequestContext['post']> }> {
  const url = (process.env.GATE_PARENT_URL ?? '').replace(/\/+$/, '')
  const file = process.env.GATE_PARENT_SESSION ?? ''
  if (!/^https:\/\/[a-z0-9.-]+$/.test(url) || file === '') {
    throw new Error('the harness holds no address and session of the Sovereign that provisioned the candidate (GATE_PARENT_URL, GATE_PARENT_SESSION, issue #8723): its deployment record is not readable from here')
  }
  const session = readFileSync(file, 'utf8').trim()
  if (session === '') throw new Error('GATE_PARENT_SESSION names an empty file')
  const headers = { Accept: 'application/json', Cookie: `catalyst_session=${session}` }
  return {
    url,
    get: (path: string) => request.get(`${url}${path}`, { headers, timeout: 60_000 }),
    post: (path: string, body: unknown) => request.post(`${url}${path}`, { headers: { ...headers, 'Content-Type': 'application/json' }, data: body, timeout: 60_000 }),
  }
}

/** The domain of the candidate (GATE_FQDN). */
export function fqdnOf(): string {
  const v = process.env.GATE_FQDN ?? ''
  expect(v, 'GATE_FQDN').toMatch(/^[a-z0-9.-]+$/)
  return v
}

/** True for an address of the private ranges of RFC 1918. */
export function isPrivateIPv4(ip: string): boolean {
  const m = /^(\d+)\.(\d+)\.(\d+)\.(\d+)$/.exec(ip)
  if (!m) return false
  const [a, b] = [Number(m[1]), Number(m[2])]
  return a === 10 || (a === 172 && b >= 16 && b <= 31) || (a === 192 && b === 168)
}
