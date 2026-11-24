/**
 * e2e/gate/assurance/lib.ts — what the specifications of gate check FND-G23 share (FND-165).
 *
 * FND-G23 is the console regression of compliance, sovereignty and hand-over, and of the storefront: one
 * test per row of docs/ledger/UAT.md that the row map marks test:FND-G23, titled with the row id and its
 * clause. The helpers read the candidate the way a clause names it: the console and the storefront in a
 * browser, their APIs through a signed-in context, the clusters through regionKubectl, the gate mailbox
 * over IMAP. A helper never turns a missing object into an empty answer: what a clause needs and cannot
 * find fails the test with the reason. This folder keeps its own copies of the helpers of the other
 * folders, so that the folders change apart.
 */

import { spawn } from 'node:child_process'
import { createSign, randomUUID } from 'node:crypto'
import { connect } from 'node:tls'
import { expect, consoleURL, regionKubectl, type Region } from '../fixtures.ts'
import { fqdn, hostURL, kubeconfigOf } from '../env.ts'
import type { APIRequestContext, Browser, BrowserContext, Page } from '@playwright/test'

export const REGIONS: readonly Region[] = ['a', 'b']

/** One Kubernetes object, as much of it as the clauses read. */
export interface Obj {
  metadata: {
    name: string
    namespace?: string
    labels?: Record<string, string>
    resourceVersion?: string
    managedFields?: Array<{ manager?: string; operation?: string; time?: string }>
  }
  spec?: Record<string, unknown>
  status?: Record<string, unknown>
  data?: Record<string, string>
}

/** kubectl in a region; a non-zero exit fails the test with kubectl's own message. */
export async function kube(region: Region, args: string[], timeoutMs = 120_000): Promise<string> {
  const r = await regionKubectl(region, args, timeoutMs)
  if (r.code !== 0) throw new Error(`kubectl ${args.join(' ')} (region ${region}) exited ${r.code}: ${r.stderr.trim()}`)
  return r.stdout
}

function items(out: string, what: string): Obj[] {
  const parsed = JSON.parse(out) as { items?: unknown }
  if (!Array.isArray(parsed.items)) throw new Error(`kubectl get ${what} answered no list items`)
  return parsed.items as Obj[]
}

/** The objects of one kind, in every namespace or in one. */
export async function list(region: Region, kind: string, namespace?: string): Promise<Obj[]> {
  return items(await kube(region, ['get', kind, ...(namespace ? ['-n', namespace] : ['-A']), '-o', 'json']), kind)
}

/** The objects of one cluster-scoped kind. */
export async function listCluster(region: Region, kind: string): Promise<Obj[]> {
  return items(await kube(region, ['get', kind, '-o', 'json']), kind)
}

/** One object, or null when the cluster has none of that name. */
export async function getObj(region: Region, kind: string, namespace: string, name: string): Promise<Obj | null> {
  const r = await regionKubectl(region, ['get', kind, ...(namespace ? ['-n', namespace] : []), name, '-o', 'json'])
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
      last = null
    } catch (e) {
      last = e
    }
    if (Date.now() > end) throw new Error(`${what}: not within ${Math.round(timeoutMs / 1000)} s${last ? ` (last: ${String(last)})` : ''}`)
    await new Promise((r) => setTimeout(r, everyMs))
  }
}

/** GET of a console API path through a signed-in page; anything but 200 fails the test. */
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

/** The address of a page of the storefront, for example mkt('/redeem/?code=X'). */
export function mkt(path = '/'): string {
  return hostURL('marketplace', path)
}

/** True when a URL is the page path of a host of this Sovereign (path '' matches any path). */
export function isAt(url: string, sub: string, path = ''): boolean {
  const u = new URL(url)
  return u.host === new URL(hostURL(sub, '/')).host && (path === '' || u.pathname.replace(/\/+$/, '') === path.replace(/\/+$/, ''))
}

/** No sign-in form on a page: no password, PIN or token field. */
export async function expectNoLoginForm(page: Page, what: string): Promise<void> {
  await expect(page.locator('input[type="password"]'), `${what}: no password field`).toHaveCount(0)
  await expect(page.locator('input[autocomplete="one-time-code"], input[name="token"], input[name="otp"], input[maxlength="6"]'), `${what}: no PIN or token field`).toHaveCount(0)
}

/** A literal of the parent's hosts: console.openova.io, omantel.openova.io or a bare openova.io. */
export const MOTHERSHIP_HOST = /(^|[^a-z0-9.-])(console\.|omantel\.)?openova\.io(?![a-z0-9-])/i

/* ── The owner and the hand-over ───────────────────────────────────── */

/** The env of the catalyst-api container of region a, by name. */
async function catalystApiEnv(): Promise<Map<string, string>> {
  const deploy = JSON.parse(await kube('a', ['-n', 'catalyst-system', 'get', 'deploy', 'catalyst-api', '-o', 'json'])) as Obj
  return new Map(arr(deploy, 'spec.template.spec.containers').flatMap((c) => arr(c, 'env')).map((e) => [str(e, 'name'), str(e, 'value')]))
}

/** The owner of the Sovereign: the ConfigMap catalyst-system/sovereign-fqdn, key orgEmail. */
export async function sovereignFqdnConfig(): Promise<{ owner: string; selfDeploymentId: string }> {
  const cm = JSON.parse(await kube('a', ['-n', 'catalyst-system', 'get', 'configmap', 'sovereign-fqdn', '-o', 'json'])) as Obj
  const owner = str(cm, 'data.orgEmail').trim()
  expect(owner, 'the owner of the Sovereign (ConfigMap catalyst-system/sovereign-fqdn, key orgEmail)').toMatch(/^[^@\s]+@[^@\s]+$/)
  return { owner, selfDeploymentId: str(cm, 'data.selfDeploymentId') }
}

/**
 * A hand-over URL of this Sovereign for its owner, as FND-162 signs it: the claims of
 * handoverjwt.MintToken, signed with the Sovereign's own signer, which /auth/handover accepts besides the
 * parent's key (#4101). The issuer is the one catalyst-api stamps (CATALYST_HANDOVER_JWT_ISSUER).
 */
export async function ownerHandover(): Promise<{ url: string; owner: string }> {
  const env = await catalystApiEnv()
  const issuer = env.get('CATALYST_HANDOVER_JWT_ISSUER') ?? ''
  const signer = env.get('CATALYST_HANDOVER_SIGNER_PATH') ?? ''
  expect(issuer, 'catalyst-api names the issuer of its hand-over tokens (CATALYST_HANDOVER_JWT_ISSUER)').toMatch(/^https:\/\//)
  expect(signer, 'catalyst-api names its hand-over signer (CATALYST_HANDOVER_SIGNER_PATH)').toMatch(/^\//)
  const { owner, selfDeploymentId } = await sovereignFqdnConfig()
  const key = await kube('a', ['-n', 'catalyst-system', 'exec', 'deploy/catalyst-api', '-c', 'catalyst-api', '--', 'cat', signer])
  // A boolean, so that a key is never printed into the report or the trace.
  expect(/-----BEGIN [A-Z ]*PRIVATE KEY-----/.test(key), 'the Sovereign signer is a PEM key').toBe(true)
  const now = Math.floor(Date.now() / 1000)
  const part = (v: object) => Buffer.from(JSON.stringify(v)).toString('base64url')
  const signed = `${part({ alg: 'RS256', typ: 'JWT' })}.${part({
    iss: issuer, sub: owner, aud: [new URL(consoleURL('/')).origin], iat: now, exp: now + 300, jti: randomUUID(),
    email: owner, email_verified: true, sovereign_fqdn: fqdn(), deployment_id: selfDeploymentId, role: 'sovereign-admin',
  })}`
  const token = `${signed}.${createSign('RSA-SHA256').update(signed).sign(key, 'base64url')}`
  return { url: consoleURL(`/auth/handover?token=${encodeURIComponent(token)}`), owner }
}

/** A browser context signed in as the owner through the hand-over URL, landed on /dashboard. */
export async function ownerContext(browser: Browser): Promise<{ ctx: BrowserContext; page: Page; owner: string }> {
  const { url, owner } = await ownerHandover()
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } })
  const page = await ctx.newPage()
  await page.goto(url)
  await expect.poll(() => isAt(page.url(), 'console', '/dashboard'), { message: 'the hand-over lands on /dashboard', timeout: 60_000 }).toBe(true)
  return { ctx, page, owner }
}

/* ── The gate mailbox and the customer ─────────────────────────────── */

const CODE_SUBJECT = 'Your login code' // core/services/auth/handlers/handlers.go, sendCodeEmail

export interface Mailbox { region: Region; service: string; port: number; host: string; user: string; password: string; address: string }

/**
 * The gate mailbox of the Sovereign's own mail server (the Secret platform/stalwart-sovereign-conformance,
 * as check BSS-G01 and FND-164 read it). The customer is the mailbox's own address with a fixed tag, so
 * every run signs in the same customer.
 */
export async function mailbox(tag: string): Promise<Mailbox> {
  for (const r of REGIONS) {
    const s = await getObj(r, 'secret', 'platform', 'stalwart-sovereign-conformance')
    if (!s) continue
    const v = (k: string) => Buffer.from(String(field(s, `data.${k}`) ?? ''), 'base64').toString('utf8').trim()
    const [local, domain] = v('address').split('@')
    expect(Boolean(local && domain && v('user') && v('password') && v('imap-host') && v('imap-service') && Number(v('imap-port')) > 0), 'the gate mailbox Secret holds its keys').toBe(true)
    return { region: r, service: v('imap-service'), port: Number(v('imap-port')), host: v('imap-host'), user: v('user'), password: v('password'), address: `${local}+${tag}@${domain}` }
  }
  throw new Error('no region holds the Secret platform/stalwart-sovereign-conformance: the Sovereign has no gate mailbox to read the code from')
}

/** kubectl port-forward to the mailbox's Service; the local port and a stop. */
function forward(m: Mailbox): Promise<{ port: number; stop: () => void }> {
  const p = spawn('kubectl', ['--kubeconfig', kubeconfigOf(m.region), '-n', 'platform', 'port-forward', `service/${m.service}`, `:${m.port}`])
  return new Promise((done, fail) => {
    const timer = setTimeout(() => { p.kill(); fail(new Error('kubectl port-forward: no local port within 30 s')) }, 30_000)
    p.stdout.on('data', (d) => {
      const hit = /127\.0\.0\.1:(\d+)/.exec(String(d))
      if (hit) { clearTimeout(timer); done({ port: Number(hit[1]), stop: () => p.kill() }) }
    })
    p.on('exit', (c) => { clearTimeout(timer); fail(new Error(`kubectl port-forward exited ${c}`)) })
  })
}

/** An IMAP session over implicit TLS: one command at a time, the lines and literals it answered. */
function imap(port: number, servername: string): Promise<{ cmd: (c: string) => Promise<string>; close: () => void }> {
  return new Promise((ready, fail) => {
    const sock = connect({ host: '127.0.0.1', port, servername })
    sock.setEncoding('latin1')
    let buf = ''
    let greeted = false
    let n = 0
    let wait: { tag: string; verb: string; done: (s: string) => void; fail: (e: Error) => void } | null = null
    const cmd = (c: string) => new Promise<string>((done, f) => {
      const tag = `g${++n}`
      wait = { tag, verb: c.split(' ')[0], done, fail: f }
      sock.write(`${tag} ${c}\r\n`)
    })
    sock.on('data', (d: string) => {
      buf += d
      if (!greeted) {
        if (buf.includes('\r\n')) { greeted = true; buf = ''; ready({ cmd, close: () => sock.end() }) }
        return
      }
      const w = wait
      if (!w) return
      const end = new RegExp(`(^|\\r\\n)${w.tag} (OK|NO|BAD)[^\\r\\n]*\\r\\n`).exec(buf)
      if (!end) return
      const out = buf
      buf = ''
      wait = null
      if (end[2] === 'OK') w.done(out)
      else w.fail(new Error(`IMAP ${w.verb} answered ${end[2]}`)) // never the command: LOGIN holds the password
    })
    sock.on('error', (e) => (wait ? wait.fail(e) : fail(e)))
  })
}

/** The newest UID of the sign-in mails to the mailbox's address, and its code when it is above a floor. */
export async function codeMails(m: Mailbox, floor: number): Promise<{ newest: number; code: string }> {
  const fwd = await forward(m)
  try {
    const s = await imap(fwd.port, m.host)
    try {
      await s.cmd(`LOGIN ${JSON.stringify(m.user)} ${JSON.stringify(m.password)}`)
      await s.cmd('EXAMINE INBOX')
      const found = /\* SEARCH([\d ]*)/.exec(await s.cmd(`UID SEARCH TO ${JSON.stringify(m.address)} SUBJECT ${JSON.stringify(CODE_SUBJECT)}`))
      if (!found) throw new Error('IMAP UID SEARCH answered no SEARCH line')
      const uids = found[1].trim().split(/\s+/).filter((x) => x !== '').map(Number)
      const newest = Math.max(0, ...uids)
      if (newest <= floor) return { newest, code: '' }
      const body = (await s.cmd(`UID FETCH ${newest} BODY.PEEK[TEXT]`)).replace(/=\r\n/g, '')
      const code = /login code:\s*(\d{6})\b/i.exec(body)?.[1] ?? />\s*(\d{6})\s*</.exec(body)?.[1] ?? ''
      if (!code) throw new Error(`the sign-in mail ${newest} holds no 6-digit code`)
      return { newest, code }
    } finally {
      await s.cmd('LOGOUT').catch(() => undefined)
      s.close()
    }
  } finally {
    fwd.stop()
  }
}

/** Waits for the first sign-in code mailed above a floor (the newest UID before the code was asked for). */
export async function nextCode(m: Mailbox, floor: number): Promise<string> {
  const mail = await until('the sign-in code arrives in the gate mailbox', async () => { const x = await codeMails(m, floor); return x.code ? x : null }, 300_000, 10_000)
  return mail.code
}

/** The customer's storefront session through the storefront's own sign-in API (POST /api/auth/magic-link, /api/auth/verify). */
export async function customerToken(request: APIRequestContext, m: Mailbox): Promise<string> {
  const floor = (await codeMails(m, Number.MAX_SAFE_INTEGER)).newest
  const sent = await request.post(mkt('/api/auth/magic-link'), { data: { email: m.address } })
  expect(sent.status(), 'POST /api/auth/magic-link').toBe(200)
  const code = await nextCode(m, floor)
  const verified = await request.post(mkt('/api/auth/verify'), { data: { email: m.address, code } })
  expect(verified.status(), 'POST /api/auth/verify').toBe(200)
  const token = ((await verified.json()) as { token?: unknown }).token
  expect(typeof token === 'string' && token !== '', 'POST /api/auth/verify answers a session token').toBe(true)
  return token as string
}

/** One Organization of the customer on the storefront (GET /api/tenant/orgs). */
export interface Tenant { id: string; slug?: string; name?: string; status?: string; console_host?: string }

export async function customerOrgs(request: APIRequestContext, token: string): Promise<Tenant[]> {
  const r = await request.get(mkt('/api/tenant/orgs'), { headers: { Authorization: `Bearer ${token}` } })
  expect(r.status(), 'GET /api/tenant/orgs').toBe(200)
  const body = (await r.json()) as unknown
  expect(Array.isArray(body), 'GET /api/tenant/orgs answers a list').toBe(true)
  return body as Tenant[]
}

/** Whether the storefront would give a new Organization this slug (GET /api/tenant/check-slug/{slug}). */
export async function slugAvailable(request: APIRequestContext, token: string, slug: string): Promise<boolean> {
  const r = await request.get(mkt(`/api/tenant/check-slug/${encodeURIComponent(slug)}`), { headers: { Authorization: `Bearer ${token}` } })
  expect(r.status(), `GET /api/tenant/check-slug/${slug}`).toBe(200)
  const available = ((await r.json()) as { available?: unknown }).available
  expect(typeof available, `GET /api/tenant/check-slug/${slug} answers available`).toBe('boolean')
  return available as boolean
}

/* ── Organizations in the clusters ─────────────────────────────────── */

/** The Organization objects of region a (cluster-scoped organizations.orgs.openova.io). */
export function orgCRs(): Promise<Obj[]> {
  return listCluster('a', 'organizations.orgs.openova.io')
}

export const slugOf = (cr: Obj) => str(cr, 'spec.slug') || cr.metadata.name

/** The host namespaces of an Organization in a region: those labelled with its slug. */
export async function namespacesOf(region: Region, slug: string): Promise<Obj[]> {
  return (await listCluster(region, 'namespaces')).filter((n) => (n.metadata.labels?.['openova.io/organization'] ?? n.metadata.labels?.['catalyst.openova.io/organization']) === slug)
}

/** What is left of an Organization in both regions: its object and its namespaces. */
export async function leftOf(slug: string): Promise<string[]> {
  const left: string[] = []
  if ((await orgCRs()).some((o) => slugOf(o) === slug)) left.push(`Organization ${slug}`)
  for (const r of REGIONS) for (const n of await namespacesOf(r, slug)) left.push(`namespace ${n.metadata.name} (region ${r})`)
  return left
}

/** The first Ready=True condition of an object, as 'True', or what it holds. */
export const ready = (o: unknown) => cond(o, 'Ready')

/* ── Vouchers, through the console's BSS API ───────────────────────── */

/** One voucher of GET /api/v1/org/billing/vouchers/list (products/catalyst/bootstrap/ui/src/lib/bss.api.ts). */
export interface Voucher { code: string; credit_omr: number; description: string; plan_tier?: string; active: boolean; max_redemptions: number; times_redeemed: number; deleted_at?: string }

export async function vouchers(page: Page): Promise<Voucher[]> {
  const body = await apiJSON<unknown>(page, '/api/v1/org/billing/vouchers/list')
  const rows = Array.isArray(body) ? body : (body as { items?: unknown }).items
  expect(Array.isArray(rows), 'GET /api/v1/org/billing/vouchers/list answers a list').toBe(true)
  return rows as Voucher[]
}

/** Revokes every live voucher of a description (the fixtures' own); the store keeps a revoked voucher as a record. */
export async function revokeVouchers(page: Page, description: string): Promise<void> {
  for (const v of (await vouchers(page)).filter((x) => x.description === description && !x.deleted_at)) {
    const r = await page.request.delete(consoleURL(`/api/v1/org/billing/vouchers/revoke/${encodeURIComponent(v.code)}`))
    expect(r.status(), `DELETE /api/v1/org/billing/vouchers/revoke/${v.code}`).toBe(200)
  }
}

/** Issues a voucher through the console's BSS API, with the code left to the server. */
export async function issueVoucher(page: Page, req: { credit_omr: number; plan_tier: string; description: string; max_redemptions: number }): Promise<Voucher> {
  const r = await page.request.post(consoleURL('/api/v1/org/billing/vouchers/issue'), { data: { ...req, active: true } })
  expect(r.status(), 'POST /api/v1/org/billing/vouchers/issue').toBe(200)
  const v = (await r.json()) as Voucher
  expect(v.code, 'the server generated the voucher code').toMatch(/^VCH-[A-Z0-9]{12}$/)
  return v
}

/** The parent domains of the Organization pool that are ready, as the Create-organization form lists them. */
export async function poolDomains(page: Page): Promise<string[]> {
  const body = await apiJSON<{ items?: Array<{ name: string; role: string; flipStatus?: string }> }>(page, '/api/v1/sovereign/parent-domains?role=org-pool')
  expect(Array.isArray(body.items), 'GET /api/v1/sovereign/parent-domains answers a list items').toBe(true)
  return (body.items ?? []).filter((p) => p.role === 'org-pool' && (p.flipStatus === 'ready' || p.flipStatus === 'flipped')).map((p) => p.name)
}

/* ── The cutover ───────────────────────────────────────────────────── */

/**
 * The 11 steps of the shipped cutover chain, in order, as the clauses of rows G11 and 162 and the
 * repository's CLAUDE.md name them (platform/self-sovereign-cutover/chart).
 */
export const CUTOVER_STEPS = [
  'gitea-mirror', 'harbor-projects', 'harbor-prewarm', 'registry-pivot', 'flux-gitrepository-patch', 'helmrepository-patches',
  'catalyst-api-env-patch', 'egress-block-test', 'gitea-token-mint', 'vcluster-registry-pivot', 'crossplane-provider-pivot',
]

/** The cutover's status ConfigMap catalyst/self-sovereign-cutover-status; its absence fails the test. */
export async function cutoverStatus(): Promise<Record<string, string>> {
  const cm = await getObj('a', 'configmaps', 'catalyst', 'self-sovereign-cutover-status')
  expect(cm, 'the ConfigMap catalyst/self-sovereign-cutover-status').not.toBeNull()
  expect(cm?.data, 'the cutover status ConfigMap holds data').toBeTruthy()
  return cm?.data as Record<string, string>
}
