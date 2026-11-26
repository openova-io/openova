/**
 * e2e/gate/organizations/lib.ts — what the specifications of gate check FND-G22 share (FND-164).
 *
 * FND-G22 is the console regression of the Organizations pages, users and access, sign-on and
 * settings: one test per row of docs/ledger/UAT.md that the row map marks test:FND-G22, titled with
 * the row id and its clause. The helpers read the candidate the way the clause names it: the console
 * and the other hosts of the Sovereign in a browser, the console's own API through the signed-in
 * context, the clusters through regionKubectl, the identity provider through kcadm.sh in its own Pod.
 * A helper never turns a missing object into an empty answer: what a clause needs and cannot find
 * fails the test with the reason.
 *
 * The owner. A row that names "the owner" is walked signed in as the owner of the Sovereign (the
 * ConfigMap catalyst-system/sovereign-fqdn, key orgEmail): ownerContext mints the owner's console
 * session with the Sovereign's own signer, with the claims the gate workflow gives the run's
 * identities (tests/acceptance/sovereign/internal/session/mint.go). Every other row is walked as the
 * run's identity, the page of the test.
 */

import { createSign, randomUUID } from 'node:crypto'
import { promises as dns, Resolver } from 'node:dns'
import { expect, consoleURL, regionKubectl, type Region } from '../fixtures.ts'
import { fqdn, hostURL, scheme } from '../env.ts'
import type { Browser, BrowserContext, Page } from '@playwright/test'

export const REGIONS: readonly Region[] = ['a', 'b']

/** One Kubernetes object, as much of it as the clauses read. */
export interface Obj {
  apiVersion?: string
  kind?: string
  metadata: {
    name: string
    namespace?: string
    labels?: Record<string, string>
    annotations?: Record<string, string>
    ownerReferences?: Array<{ kind: string; name: string }>
    deletionTimestamp?: string
  }
  spec?: Record<string, unknown>
  status?: Record<string, unknown>
  data?: Record<string, string>
  rules?: Array<{ apiGroups?: string[]; resources?: string[]; verbs?: string[] }>
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

/** One object, or null when the cluster has none of that name (namespace '' for a cluster-scoped kind). */
export async function getObj(region: Region, kind: string, namespace: string, name: string): Promise<Obj | null> {
  const where = namespace ? ['-n', namespace] : []
  const r = await regionKubectl(region, ['get', kind, ...where, name, '-o', 'json'])
  if (r.code !== 0) {
    if (/NotFound|not found/.test(r.stderr)) return null
    throw new Error(`kubectl get ${kind} ${namespace}/${name} exited ${r.code}: ${r.stderr.trim()}`)
  }
  return JSON.parse(r.stdout) as Obj
}

/** A value of an object by a dotted path, for example field(o, 'spec.planSlug'). */
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

/** A value under exact keys, for keys that hold dots: at(o, 'matchLabels', 'app.kubernetes.io/name'). */
export function at(o: unknown, ...keys: string[]): unknown {
  let v: unknown = o
  for (const k of keys) v = v !== null && typeof v === 'object' ? (v as Record<string, unknown>)[k] : undefined
  return v
}

/** The status of a condition of an object: 'True', 'False', 'Unknown' or '' when it has none. */
export function cond(o: unknown, type: string): string {
  const c = arr(o, 'status.conditions').find((x) => str(x, 'type') === type)
  return c ? str(c, 'status') : ''
}

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
  const self = await apiJSON<{ deploymentId?: string }>(page, '/api/v1/sovereign/self')
  expect(self.deploymentId ?? '', 'the deployment id of GET /api/v1/sovereign/self').not.toBe('')
  return self.deploymentId as string
}

/** The signed-in identity as the console reads it (GET /api/v1/whoami). */
export async function whoami(page: Page): Promise<{ email: string; tier: string }> {
  const me = await apiJSON<{ email?: string; tier?: string }>(page, '/api/v1/whoami')
  return { email: me.email ?? '', tier: me.tier ?? '' }
}

/* ── The owner and the sessions ─────────────────────────────────────── */

/** The env of the catalyst-api container of region a, by name. */
async function catalystApiEnv(): Promise<Map<string, string>> {
  const deploy = JSON.parse(await kube('a', ['-n', 'catalyst-system', 'get', 'deploy', 'catalyst-api', '-o', 'json'])) as Obj
  return new Map(arr(deploy, 'spec.template.spec.containers').flatMap((c) => arr(c, 'env')).map((e) => [str(e, 'name'), str(e, 'value')]))
}

/** The owner of the Sovereign: the ConfigMap catalyst-system/sovereign-fqdn, key orgEmail. */
export async function ownerEmail(): Promise<string> {
  const cm = JSON.parse(await kube('a', ['-n', 'catalyst-system', 'get', 'configmap', 'sovereign-fqdn', '-o', 'json'])) as Obj
  const owner = str(cm, 'data.orgEmail').trim()
  expect(owner, 'the owner of the Sovereign (ConfigMap catalyst-system/sovereign-fqdn, key orgEmail)').toMatch(/^[^@\s]+@[^@\s]+$/)
  return owner
}

/**
 * A console session for an address, signed with the Sovereign's own signer, with the claims of the
 * PIN-verify session the gate workflow mints for the run's identities; extra claims replace those.
 */
export async function mintSession(email: string, extra: Record<string, unknown> = {}, ttlSeconds = 3_600): Promise<string> {
  const env = await catalystApiEnv()
  const issuer = env.get('CATALYST_PIN_ISSUER') || `https://console.${fqdn()}`
  const signer = env.get('CATALYST_HANDOVER_SIGNER_PATH') || '/var/lib/catalyst/handover-jwt-private.pem'
  const key = await kube('a', ['-n', 'catalyst-system', 'exec', 'deploy/catalyst-api', '-c', 'catalyst-api', '--', 'cat', signer])
  // A boolean, so that a key that does not match is never printed into the report or the trace.
  expect(/-----BEGIN [A-Z ]*PRIVATE KEY-----/.test(key), 'the Sovereign signer is a PEM key').toBe(true)
  const now = Math.floor(Date.now() / 1000)
  const part = (v: object) => Buffer.from(JSON.stringify(v)).toString('base64url')
  const signed = `${part({ alg: 'RS256', typ: 'JWT' })}.${part({
    iss: issuer, sub: email, email, email_verified: true, role: 'openova-user', tier: 'owner',
    realm_access: { roles: ['catalyst-owner', 'catalyst-admin', 'sovereign-admins'] },
    iat: now, exp: now + ttlSeconds, jti: randomUUID().replace(/-/g, ''), typ: 'session', ...extra,
  })}`
  return `${signed}.${createSign('RSA-SHA256').update(signed).sign(key, 'base64url')}`
}

/** A browser context that carries a console session as the catalyst_session cookie of the Sovereign's domain. */
export function sessionContext(browser: Browser, session: string): Promise<BrowserContext> {
  return browser.newContext({
    viewport: { width: 1440, height: 900 },
    storageState: {
      cookies: [{ name: 'catalyst_session', value: session, domain: `.${fqdn()}`, path: '/', expires: -1, httpOnly: true, secure: scheme() === 'https', sameSite: 'Lax' }],
      origins: [],
    },
  })
}

/** Runs fn with a page signed in as the owner, in a context of its own that is closed afterwards. */
export async function asOwner<T>(browser: Browser, fn: (page: Page, owner: string) => Promise<T>, ttlSeconds = 3_600): Promise<T> {
  const owner = await ownerEmail()
  const ctx = await sessionContext(browser, await mintSession(owner, {}, ttlSeconds))
  try {
    return await fn(await ctx.newPage(), owner)
  } finally {
    await ctx.close()
  }
}

/** The address of another host of the Sovereign, for example host('grafana', '/api/user'). */
export function host(sub: string, path = '/'): string {
  return hostURL(sub, path)
}

/** No sign-in form on a page: no password, PIN or token field. */
export async function expectNoLoginForm(page: Page, what: string): Promise<void> {
  await expect(page.locator('input[type="password"]'), `${what}: no password field`).toHaveCount(0)
  await expect(page.locator('input[autocomplete="one-time-code"], input[name="token"], input[name="otp"]'), `${what}: no PIN or token field`).toHaveCount(0)
}

/** The lines of the sso-bridge reconciler of region a ("<UTC time> | <text>", templates/configmap-reconciler.yaml). */
export async function ssoBridgeLog(since = '5m'): Promise<Array<{ at: string; text: string }>> {
  const out = await kube('a', ['-n', 'sso-bridge', 'logs', 'deploy/sso-bridge-reconciler', '-c', 'reconciler', `--since=${since}`], 180_000)
  const lines = out.split('\n').map((l) => /^(\d{4}-\d\d-\d\dT[\d:]+Z) \| (.*)$/.exec(l)).filter((m) => m !== null).map((m) => ({ at: m[1], text: m[2] }))
  expect(lines.length, `the sso-bridge reconciler logged in the last ${since}`).toBeGreaterThan(0)
  return lines
}

/** The ticks of the last minutes in which the reconciler re-registered an application's client (reconcile bp-<app> ...). */
export async function ticksOf(app: string, since = '5m'): Promise<string[]> {
  return [...new Set((await ssoBridgeLog(since)).filter((l) => l.text.startsWith(`reconcile bp-${app} `)).map((l) => l.at))]
}

/* ── The identity provider ─────────────────────────────────────────── */

/**
 * kcadm.sh in the identity provider's Pod of region a (keycloak/keycloak-0), signed in to the master
 * realm with the administrator credential the Pod holds, as the gate workflow's own kc() does. The
 * answer of a get, parsed.
 */
export async function kcadm<T>(args: string[]): Promise<T> {
  const script = 'c=/tmp/kcadm-gate.config; k=/opt/bitnami/keycloak/bin/kcadm.sh; ' +
    '"$k" config credentials --config "$c" --server http://localhost:8080 --realm master --user admin ' +
    '--password "$(cat /opt/bitnami/keycloak/secrets/admin-password)" >/dev/null || exit 1; exec "$k" "$@" --config "$c"'
  const out = await kube('a', ['-n', 'keycloak', 'exec', 'keycloak-0', '-c', 'keycloak', '--', 'sh', '-c', script, 'sh', ...args], 180_000)
  return JSON.parse(out) as T
}

export interface KcUser { id: string; username: string; email?: string; enabled: boolean }

/** The one user of the realm sovereign with an address; none or several fail the test. */
export async function realmUser(email: string): Promise<KcUser> {
  const users = (await kcadm<KcUser[]>(['get', 'users', '-r', 'sovereign', '-q', `email=${email}`, '-q', 'exact=true'])).filter((u) => (u.email ?? '').toLowerCase() === email.toLowerCase())
  expect(users.length, `the users of the realm sovereign with the address ${email}`).toBe(1)
  return users[0]
}

/* ── Organizations ─────────────────────────────────────────────────── */

/** One row of GET /api/v1/organizations (products/catalyst/bootstrap/api/internal/handler/organization_provisioning.go). */
export interface OrgItem {
  org_tenant_id: string
  state: string
  subdomain: string
  parent_domain?: string
  admin_email?: string
  company_name?: string
  console_host?: string
  tenant_namespace?: string
  kind?: string
  tier?: string
  billing_mode?: string
  isolation?: string
  plan_slug?: string
}

export async function orgItems(page: Page): Promise<OrgItem[]> {
  const body = await apiJSON<{ items?: OrgItem[] }>(page, '/api/v1/organizations')
  expect(Array.isArray(body.items), 'GET /api/v1/organizations answers a list items').toBe(true)
  return body.items as OrgItem[]
}

/** The Organization objects of region a (cluster-scoped organizations.orgs.openova.io). */
export function orgCRs(): Promise<Obj[]> {
  return listCluster('a', 'organizations.orgs.openova.io')
}

export const slugOf = (cr: Obj) => str(cr, 'spec.slug') || cr.metadata.name

/** The host namespaces of an Organization in a region: those labelled with its slug. */
export async function namespacesOf(region: Region, slug: string): Promise<Obj[]> {
  return (await listCluster(region, 'namespaces')).filter((n) => (n.metadata.labels?.['openova.io/organization'] ?? n.metadata.labels?.['catalyst.openova.io/organization']) === slug)
}

/** The vCluster StatefulSet `vcluster` of an Organization's namespace, or null. */
export async function vclusterSts(region: Region, namespace: string): Promise<Obj | null> {
  return getObj(region, 'statefulset', namespace, 'vcluster')
}

export const stsReady = (s: Obj | null) => s !== null && Number(field(s, 'status.readyReplicas') ?? 0) >= 1 && Number(field(s, 'status.readyReplicas')) === Number(field(s, 'spec.replicas') ?? 1)

/** The Organizations this folder creates and deletes; fixed names, so that every run reuses their rows. */
export interface Fixture { slug: string; company: string; kind: 'customer' | 'internal'; plan: 's' | 'm' }

export const FIXTURES: Record<'customer' | 'internal', Fixture> = {
  customer: { slug: 'gate-g22-ext', company: 'Gate G22 Customer', kind: 'customer', plan: 'm' },
  internal: { slug: 'gate-g22-int', company: 'Gate G22 Internal', kind: 'internal', plan: 's' },
}

/** The first parent domain of the Organization pool that is ready, as the Create-organization form picks it. */
export async function poolDomain(page: Page): Promise<string> {
  const body = await apiJSON<{ items?: Array<{ name: string; role: string; flipStatus?: string }> }>(page, '/api/v1/sovereign/parent-domains?role=org-pool')
  const ready = (body.items ?? []).filter((p) => p.role === 'org-pool' && (p.flipStatus === 'ready' || p.flipStatus === 'flipped'))
  expect(ready.length, 'a ready parent domain of the Organization pool').toBeGreaterThan(0)
  return ready[0].name
}

/** Deletes an Organization through the console API (DELETE /api/v1/organizations/{id}) for every live row of its slug. */
export async function deleteOrg(page: Page, slug: string): Promise<number> {
  const live = (await orgItems(page)).filter((o) => o.subdomain === slug && o.state !== 'deleted')
  for (const o of live) {
    const r = await page.request.delete(consoleURL(`/api/v1/organizations/${encodeURIComponent(o.org_tenant_id)}`), { timeout: 120_000 })
    expect(r.status(), `DELETE /api/v1/organizations/${o.org_tenant_id} (${slug})`).toBe(204)
  }
  return live.length
}

/** What is left of an Organization in both regions: its object, its namespaces, its vCluster StatefulSets. */
export async function leftOf(slug: string): Promise<string[]> {
  const left: string[] = []
  if ((await orgCRs()).some((o) => slugOf(o) === slug)) left.push(`Organization ${slug}`)
  for (const r of REGIONS) {
    for (const n of await namespacesOf(r, slug)) left.push(`namespace ${n.metadata.name} (region ${r})`)
    for (const s of (await list(r, 'statefulsets')).filter((s) => s.metadata.name === 'vcluster' && (s.metadata.namespace ?? '') === slug)) left.push(`StatefulSet ${s.metadata.namespace}/vcluster (region ${r})`)
  }
  return left
}

/** Waits until nothing of an Organization is left in either region. */
export async function waitGone(slug: string, timeoutMs = 600_000): Promise<void> {
  await until(`nothing of ${slug} is left`, async () => ((await leftOf(slug)).length === 0 ? true : null), timeoutMs, 10_000)
}

/**
 * The A records of a name at the authoritative servers of its zone (asked directly, so that no cache
 * answers); an empty list when they hold none.
 */
export async function authoritativeA(name: string, zone: string): Promise<string[]> {
  const ns = await dns.resolveNs(zone)
  expect(ns.length, `the name servers of ${zone}`).toBeGreaterThan(0)
  const ips = (await Promise.all(ns.map((n) => dns.resolve4(n).catch(() => [] as string[])))).flat()
  expect(ips.length, `the addresses of the name servers of ${zone}`).toBeGreaterThan(0)
  const resolver = new Resolver()
  resolver.setServers(ips)
  return new Promise((done, fail) => resolver.resolve4(name, (err, out) => {
    if (!err) done(out)
    else if (err.code === 'ENOTFOUND' || err.code === 'ENODATA') done([])
    else fail(new Error(`the A records of ${name} at ${ips.join(', ')}: ${err.code}`))
  }))
}

/* ── The Dashboard treemap ─────────────────────────────────────────── */

/** One cell of GET /api/v1/dashboard/treemap. */
export interface Cell {
  id?: string | null
  name: string
  count?: number
  children?: Cell[]
}

/** The treemap the Dashboard draws, for a layer stack (group_by), as the console asks for it. */
export async function treemap(page: Page, layers: string[]): Promise<Cell[]> {
  const id = await selfId(page)
  const q = new URLSearchParams({ group_by: layers.join(','), color_by: 'health', size_by: 'cpu_request', deployment_id: id })
  const body = await apiJSON<{ items?: Cell[] }>(page, `/api/v1/dashboard/treemap?${q.toString()}`)
  expect(Array.isArray(body.items) && (body.items as Cell[]).length > 0, `the treemap of ${layers.join(',')} has cells`).toBe(true)
  return body.items as Cell[]
}

/** The platform-overhead bucket of the organization layer (handler/fleet_treemap.go platformOrg). */
export const PLATFORM_ORG = '__platform__'

/**
 * The application of a Pod as the treemap names it (handler/dashboard.go applicationKey): its instance
 * label, its name label, else the workload that owns it. A Pod owned by a Job is one-shot activity and
 * has none (isEphemeralRow).
 */
export function applicationOf(p: Obj, rsOwner: Map<string, string>): string | null {
  const owners = p.metadata.ownerReferences ?? []
  if (owners.some((o) => o.kind === 'Job')) return null
  const l = p.metadata.labels ?? {}
  if (l['app.kubernetes.io/instance']) return l['app.kubernetes.io/instance']
  if (l['app.kubernetes.io/name']) return l['app.kubernetes.io/name']
  const o = owners.find((x) => x.name !== '')
  if (!o) return p.metadata.name
  if (o.kind !== 'ReplicaSet') return o.name
  return rsOwner.get(`${p.metadata.namespace ?? ''}/${o.name}`) || o.name
}

/**
 * The applications of the Pods of a set of namespaces in both regions: those of every Pod (what a
 * cell may name) and those of the running ones (what a cell must name).
 */
export async function applicationsIn(namespaces: (r: Region) => Promise<string[]>, regions: readonly Region[] = REGIONS): Promise<{ all: Set<string>; running: Set<string> }> {
  const all = new Set<string>()
  const running = new Set<string>()
  for (const r of regions) {
    for (const ns of await namespaces(r)) {
      const rs = new Map((await list(r, 'replicasets', ns)).map((x) => [`${ns}/${x.metadata.name}`, x.metadata.ownerReferences?.[0]?.name ?? '']))
      for (const p of await list(r, 'pods', ns)) {
        const a = applicationOf(p, rs)
        if (!a) continue
        all.add(a)
        if (str(p, 'status.phase') === 'Running') running.add(a)
      }
    }
  }
  return { all, running }
}

/** The applications of an Organization: the Pods of the namespaces labelled with its slug. */
export function applicationsOfOrg(slug: string): Promise<{ all: Set<string>; running: Set<string> }> {
  return applicationsIn(async (r) => (await namespacesOf(r, slug)).map((n) => n.metadata.name))
}

/** Words the Organizations model banned from the console (docs/GLOSSARY.md): tenant, SME. */
export const BANNED_WORDS = /\b(tenants?|SMEs?)\b/i
