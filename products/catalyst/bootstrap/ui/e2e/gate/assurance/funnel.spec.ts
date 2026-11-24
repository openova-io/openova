// Gate check FND-G23 (FND-165): the storefront funnel of a customer, walked twice on the candidate, and the
// vouchers it starts from.
//
// The walk. The run's user issues a voucher on the console's BSS Vouchers page (code left to the server,
// 5000 OMR, plan M, one redemption). A stranger's browser opens its redeem URL, signs up, picks package M,
// WordPress and the default add-ons, names the Organization gate-g23-one on the first ready pool domain of the
// Sovereign, chooses the active-hot-standby topology, signs in at checkout with the code mailed to the gate
// mailbox (the customer <gate mailbox>+fnd-g23@<domain>, the same on every run) and launches. The same customer
// then redeems a second voucher and launches gate-g23-two on the second ready pool domain. The walk waits for
// both Organizations, lands on their consoles and reads their applications.
//
// What the walk saw is recorded per row, and each test asserts its row's values: a wrong value fails that row
// alone; a step the walk cannot take stops that walk, and every later row of it fails with that reason; a read
// after the Organizations are up that fails, fails its own row. The record is kept in the runner's temporary
// folder for the run (it holds no session and no secret), so that a worker Playwright starts again after a
// failed test reads the record instead of walking again.
//
// What the walk leaves. The two Organizations are deleted by their customer through the storefront (DELETE
// /api/tenant/orgs/{id}) before the walk, when an earlier run left them, and once after it; the names are
// fixed, so a run reuses them once the teardown has given the slugs back. The vouchers are revoked; the
// billing store keeps a revoked voucher, and the order and credit lines of each checkout, as its own record.
// One test per ledger row; the title is the row id and its clause.
import { test, expect, consoleURL, stateOf } from '../fixtures.ts'
import type { APIRequestContext, BrowserContext, Page, Response } from '@playwright/test'
import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import {
  MOTHERSHIP_HOST, REGIONS, arr, codeMails, customerOrgs, customerToken, expectNoLoginForm, getObj, isAt, issueVoucher, kube, leftOf, list,
  mailbox, mkt, namespacesOf, nextCode, orgCRs, poolDomains, ready, revokeVouchers, slugAvailable, slugOf, str, until, vouchers,
  type Mailbox, type Voucher,
} from './lib.ts'

const TAG = 'fnd-g23'
const ONE = { slug: 'gate-g23-one', name: 'Gate G23 One', voucher: 'gate-g23 funnel one' }
const TWO = { slug: 'gate-g23-two', name: 'Gate G23 Two', voucher: 'gate-g23 funnel two' }
const CREDIT = 5000
const TIERS = ['S', 'M', 'L', 'XL', 'Flexi']
/** The stages of a funnel Organization with WordPress (core/services/provisioning/handlers/consumer.go). */
const STAGES = ['Creating Organization', 'Committing manifests to Git', 'Provisioning vCluster', 'Deploying WordPress', 'Configuring TLS certificates', 'Running health checks']
const VIEWPORT = { width: 1440, height: 900 }
const CACHE = join(tmpdir(), `fnd-g23-walk-${process.ppid}.json`)

/* ── The record of the walk ────────────────────────────────────────── */

interface PackageDoc { sku: string; name: string; price_month: string; includes?: { vcpu?: number; memory_gb?: number }; shape?: { vcpu?: number; memory_gb?: number } }
interface Landing { url: string; status: number; loginFields: number; avatarTitle: string; whoami: { email?: string; org?: string; orgScoped?: boolean }; sidebar: string }

interface Seen {
  '74': { added: Voucher[]; plan: string; credit: string; redeemed: string; status: string }
  '75': { host: string; credit: string; code: string; brand: string; mothershipRefs: string[] }
  '78': { url: string; pending: string }
  '79': { status: number; packages: PackageDoc[]; shown: Record<string, string>; chosen: string; planId: string; url: string }
  '80': { status: number; host: string; catalog: string[]; shown: string[]; selected: boolean; url: string }
  '81': { tiles: string[]; pressedBefore: string[]; pressedAfter: string[]; tlds: string[]; tld: string; url: string }
  '82': { single: boolean; hot: boolean; locked: string; switchedToGrow: boolean; hotChecked: boolean; url: string }
  '83': { plan: string; apps: string[]; topology: string; orgUrl: string; url: string }
  '84': { text: string; loginFields: number; signedIn: boolean }
  '92': { first: number; firstCredit: number; burst: number[]; panel: string; afterWindow: number }
  '85': { summary: string; covers: boolean; stripe: boolean; label: string; url: string }
  '86': { shown: string[]; shownStatus: string[]; shownRed: string[]; failedSeen: string[]; status: string; steps: Array<{ name: string; status: string }> }
  '87': { url: string; status: number; direct: number }
  '88': Landing
  '89': { cards: string[]; card: string; dataStatus: string; reasons: number; openedUrl: string; openedStatus: number }
  '90': { host: string; status: number; wordpress: boolean }
  '91': { url: string; consoles: string[] }
  R15: { planSlug: string; wanted: string }
  '216': { provisioned: string; directoryStatus: string; vcluster: string[] }
  '93': { tld: string; parentDomain: string; cert: string; url: string; provisioned: string }
  '94': Landing
  '95': { hosts: string[]; statuses: number[]; wordpress: boolean[] }
  '226': { kustomizations: Array<{ name: string; ready: string; running: number }> }
  R16: { consoles: Array<{ host: string; status: number }>; templates: Array<{ host: string; rules: string }> }
}

type Row = keyof Seen
type Chain = 'one' | 'two'
interface WalkRecord {
  done: boolean
  cleaned: boolean
  stopped: Record<Chain, string>
  failed: Partial<Record<Row, string>>
  seen: Partial<Seen>
  tld: Record<Chain, string>
}
let rec: WalkRecord = { done: false, cleaned: false, stopped: { one: '', two: '' }, failed: {}, seen: {}, tld: { one: '', two: '' } }
const save = () => writeFileSync(CACHE, JSON.stringify(rec), { mode: 0o600 })

/** The first line of an error, with any token or code of a URL taken out. */
function reason(e: unknown): string {
  return String((e as Error)?.message ?? e).split('\n')[0].replace(/(token|code)=[^&\s"']+/gi, '$1=…')
}

/** A URL without its query: the launch URLs carry the customer's session. */
const bare = (url: string) => { const u = new URL(url); return `${u.origin}${u.pathname}`.replace(/\/$/, '') }

/**
 * One step of a walk, run only while its chain has not stopped. What it returns is the record of its row
 * (none when row is null: the step only has to be taken). A step that throws stops its chain; with
 * stops=false (a read after the Organizations are up) it fails its own row only.
 */
async function at<K extends Row>(chain: Chain, row: K | null, fn: () => Promise<Seen[K]>, stops = true): Promise<void> {
  if (rec.stopped[chain]) return
  try {
    const v = await fn()
    if (row !== null) rec.seen[row] = v
  } catch (e) {
    const why = `${row === null ? 'a step' : `row ${row}`}: ${reason(e)}`
    if (stops || row === null) rec.stopped[chain] = why
    else rec.failed[row] = why
  } finally {
    save()
  }
}

/** What the walk saw at a row, or the failure of the test with the reason the walk has none. */
function saw<K extends Row>(row: K): Seen[K] {
  const v = rec.seen[row]
  if (v !== undefined) return v as Seen[K]
  if (rec.failed[row]) throw new Error(`the walk could not read row ${row}: ${rec.failed[row]}`)
  const why = [rec.stopped.one && `the first walk stopped at ${rec.stopped.one}`, rec.stopped.two && `the second walk stopped at ${rec.stopped.two}`].filter(Boolean).join('; ')
  throw new Error(`the walk did not reach row ${row}${why ? ` (${why})` : rec.done ? '' : ' (the walk did not finish)'}`)
}

/* ── Helpers of the walk ───────────────────────────────────────────── */

const consoleOf = (slug: string, tld: string) => `console.${slug}.${tld}`

/** The hosts of an Organization's routes in both regions that start with an application's name. */
async function appHosts(slug: string, app: string): Promise<string[]> {
  const out = new Set<string>()
  for (const r of REGIONS) {
    for (const ns of await namespacesOf(r, slug)) {
      for (const route of await list(r, 'httproutes.gateway.networking.k8s.io', ns.metadata.name)) {
        for (const h of arr(route, 'spec.hostnames').map(String)) if (h.startsWith(`${app}.`)) out.add(h)
      }
    }
  }
  if (out.size === 0) throw new Error(`no route of ${slug} serves a host ${app}.*`)
  return [...out]
}

/** Deletes the fixtures' Organizations as their customer and waits until the storefront gives the slugs back. */
async function cleanUp(adm: Page, request: APIRequestContext, m: Mailbox): Promise<void> {
  const token = await customerToken(request, m)
  const slugs = [ONE.slug, TWO.slug]
  for (const t of (await customerOrgs(request, token)).filter((o) => slugs.includes(o.slug ?? '') && o.status !== 'deleted')) {
    const r = await request.delete(mkt(`/api/tenant/orgs/${encodeURIComponent(t.id)}`), { headers: { Authorization: `Bearer ${token}` } })
    expect(r.status(), `DELETE /api/tenant/orgs/${t.id} (${t.slug})`).toBe(200)
  }
  for (const slug of slugs) {
    await until(`nothing of ${slug} is left and the storefront gives the slug back`, async () => {
      // An Organization object the storefront no longer holds is deleted directly; its finalizer cascades.
      if (!(await customerOrgs(request, token)).some((o) => o.slug === slug && o.status !== 'deleted')) {
        for (const o of (await orgCRs()).filter((x) => slugOf(x) === slug)) await kube('a', ['delete', 'organizations.orgs.openova.io', o.metadata.name, '--wait=false'])
      }
      return (await leftOf(slug)).length === 0 && (await slugAvailable(request, token, slug)) ? true : null
    }, 1_800_000, 15_000)
  }
  for (const f of [ONE, TWO]) await revokeVouchers(adm, f.voucher)
}

/** A response of the page whose URL path starts with a path, from the moment it is called; null when none came. */
function responseOf(page: Page, path: string, method = 'GET'): Promise<Response | null> {
  return page.waitForResponse((r) => r.request().method() === method && new URL(r.url()).pathname.startsWith(path), { timeout: 120_000 }).catch(() => null)
}

/** Waits until a page is on a path of the storefront. */
function onStorefront(page: Page, path: string): Promise<void> {
  return page.waitForURL((u) => isAt(u.href, 'marketplace', path), { timeout: 60_000 })
}

/**
 * Redeem → Plans → Apps → Add-ons → Topology → Review → Checkout of one Organization. The first walk records
 * rows 75 to 83; the second only has to take the steps (row 93 records what it reaches).
 */
async function walkOrg(page: Page, chain: Chain, slug: string, code: string, first: boolean): Promise<void> {
  const row = <K extends Row>(k: K): K | null => (first ? k : null)
  let pkgs: Promise<Response | null> = Promise.resolve(null)
  await at(chain, row('75'), async () => {
    await page.goto(mkt(`/redeem/?code=${encodeURIComponent(code)}`))
    await page.locator('#redeem-valid').waitFor({ state: 'visible', timeout: 60_000 })
    const credit = (await page.locator('#redeem-valid-credit').innerText()).trim()
    const shownCode = (await page.locator('#redeem-valid-code').innerText()).trim()
    const brand = await page.evaluate(() => document.documentElement.dataset.tenantBrand ?? '')
    const refs = await page.locator('[src], [href]').evaluateAll((els) => els.map((e) => e.getAttribute('src') ?? e.getAttribute('href') ?? ''))
    const host = new URL(page.url()).host
    pkgs = responseOf(page, '/api/v1/public/packages')
    await page.locator('#redeem-cta').click()
    return { host, credit, code: shownCode, brand, mothershipRefs: refs.filter((r) => /openova\.io/i.test(r)) }
  })
  await at(chain, row('78'), async () => {
    await onStorefront(page, '/plans')
    return { url: bare(page.url()), pending: (await page.evaluate(() => window.localStorage.getItem('org-pending-voucher'))) ?? '' }
  })
  let appsResp: Promise<Response | null> = Promise.resolve(null)
  await at(chain, row('79'), async () => {
    const res = await pkgs
    if (!res) throw new Error('the plans page did not ask for the packages (GET /api/v1/public/packages)')
    const doc = res.ok() ? ((await res.json()) as { packages?: PackageDoc[] }) : {}
    if (!Array.isArray(doc.packages) || doc.packages.length === 0) throw new Error(`GET /api/v1/public/packages answered ${res.status()} with no package`)
    await page.locator('[data-testid="package-table"], [data-testid="package-ladder"]').first().waitFor({ timeout: 60_000 })
    const shown: Record<string, string> = {}
    for (const p of doc.packages) {
      const head = await page.locator(`[data-testid="package-card-${p.sku}"], [data-testid="package-col-${p.sku}"]`).allInnerTexts()
      const cells = await page.locator(`[data-testid^="package-cell-"][data-testid$="-${p.sku}"]`).allInnerTexts()
      shown[p.sku] = [...head, ...cells].join(' | ')
    }
    const m = doc.packages.find((p) => p.name.trim().toUpperCase() === 'M') ?? doc.packages.find((p) => /(^|[-_.])m$/i.test(p.sku))
    if (!m) throw new Error(`the storefront offers no package M (${doc.packages.map((p) => p.name).join(', ')})`)
    appsResp = responseOf(page, '/api/catalog/apps')
    await page.getByTestId(`package-choose-${m.sku}`).click()
    await onStorefront(page, '/apps')
    const cart = JSON.parse((await page.evaluate(() => window.localStorage.getItem('org-cart'))) ?? '{}') as { plan?: string }
    return { status: res.status(), packages: doc.packages, shown, chosen: m.sku, planId: cart.plan ?? '', url: bare(page.url()) }
  })
  await at(chain, row('80'), async () => {
    const res = await appsResp
    if (!res) throw new Error('the apps page did not ask for the catalogue (GET /api/catalog/apps)')
    const catalog = res.ok() ? ((await res.json()) as unknown) : null
    const wordpress = page.locator('.app-name', { hasText: /^WordPress$/ })
    const wp = page.locator('a.app-card').filter({ has: wordpress }).first()
    await wp.waitFor({ timeout: 60_000 })
    await wp.locator('.app-add-btn').click()
    await page.locator('a.app-card.in-cart').filter({ has: wordpress }).first().waitFor({ timeout: 30_000 })
    const shown = await page.locator('a.app-card .app-name').allInnerTexts()
    await page.locator('a.float-cta').click()
    await onStorefront(page, '/addons')
    const names = Array.isArray(catalog) ? catalog.map((a) => String((a as { name?: unknown }).name ?? '')) : []
    return { status: res.status(), host: new URL(res.url()).host, catalog: names, shown, selected: true, url: bare(page.url()) }
  })
  await at(chain, row('81'), async () => {
    await page.locator('input.domain-input').waitFor({ timeout: 60_000 })
    const tiles = page.locator('[data-testid^="addon-tile-"]')
    const pressed = () => tiles.evaluateAll((els) => els.filter((e) => e.getAttribute('aria-pressed') === 'true').map((e) => e.getAttribute('data-testid') ?? ''))
    const ids = await tiles.evaluateAll((els) => els.map((e) => e.getAttribute('data-testid') ?? ''))
    const pressedBefore = await pressed()
    const tlds = await page.locator('select.domain-tld option').evaluateAll((els) => els.map((e) => (e as HTMLOptionElement).value))
    const tld = rec.tld[chain]
    if (!tld) throw new Error(`the Sovereign has no ready pool domain for this Organization (the storefront offers ${tlds.join(', ')})`)
    if (!tlds.includes(tld)) throw new Error(`the storefront does not offer the pool domain ${tld} (${tlds.join(', ')})`)
    await page.locator('select.domain-tld').selectOption(tld)
    await page.locator('input.domain-input').fill(slug)
    await page.locator('input.domain-input').press('Tab')
    await page.locator('.slug-status', { hasText: `${slug}.${tld} is available` }).waitFor({ timeout: 60_000 })
    const pressedAfter = await pressed()
    await page.locator('a.step-cta').click()
    await onStorefront(page, '/bcp')
    return { tiles: ids, pressedBefore, pressedAfter, tlds, tld, url: bare(page.url()) }
  })
  await at(chain, row('82'), async () => {
    const single = page.getByTestId('topology-card-single')
    const hot = page.getByTestId('topology-card-hot')
    await single.waitFor({ timeout: 60_000 })
    const out = { single: await single.isVisible(), hot: await hot.isVisible(), locked: (await hot.getAttribute('data-locked')) ?? '', switchedToGrow: false, hotChecked: false, url: '' }
    if (first) {
      // On a package that offers the standby only as usage, the step's own one-click switch to Grow is the way to it.
      if ((await hot.getAttribute('data-needs-grow')) === 'true') {
        await page.getByTestId('topology-switch-grow').click()
        out.switchedToGrow = true
      }
      const radio = page.locator('label[data-testid="topology-card-hot"] input[type="radio"]')
      if ((await radio.count()) === 1) {
        await radio.check()
        out.hotChecked = await radio.isChecked()
        if ((await page.locator('a.step-cta').getAttribute('aria-disabled')) === 'true') {
          await page.locator('#primary-region').selectOption({ index: 1 })
          await page.locator('#replica-region').selectOption({ index: 2 })
        }
      }
    }
    await page.locator('a.step-cta').click()
    await onStorefront(page, '/review')
    return { ...out, url: bare(page.url()) }
  })
  await at(chain, row('83'), async () => {
    await page.getByTestId('review-plan-row').waitFor({ timeout: 60_000 })
    // The plan option that is checked, the stack, the topology summary and the Organization's URL (ReviewStep.svelte).
    const plan = (await page.getByTestId('review-plan-row').locator('label:has(input:checked)').getAttribute('data-testid')) ?? ''
    const apps = await page.locator('a.stack-card').allInnerTexts()
    const topology = (await page.locator('.bcp-summary-head strong').innerText()).trim()
    const orgUrl = (await page.locator('.ws-preview').innerText()).trim()
    await page.locator('a.step-cta').click()
    await onStorefront(page, '/checkout')
    return { plan, apps, topology, orgUrl, url: bare(page.url()) }
  })
}

/** Launches the Organization from the checkout page: its name, the Launch button, the tenant the storefront created. */
async function launch(page: Page, name: string): Promise<{ tenantId: string; summary: string; covers: boolean; stripe: boolean; label: string }> {
  await page.locator('input[placeholder="My Company"]').fill(name)
  const button = page.getByRole('button', { name: /Launch my Organization|Purchase/ })
  await button.waitFor({ timeout: 120_000 })
  const label = (await button.innerText()).trim()
  const summary = await page.getByText('Order summary').locator('xpath=..').innerText()
  const covers = await page.getByText('✓ Credit covers this order — no card charge needed.').isVisible()
  const stripe = await page.getByText(/redirected to Stripe/).isVisible()
  if (!/Launch my Organization/.test(label)) throw new Error(`the checkout asks for a card (${label}): the voucher credit does not cover the order`)
  const created = responseOf(page, '/api/tenant/orgs', 'POST')
  await button.click()
  const res = await created
  if (!res) throw new Error('Launch created no Organization (no POST /api/tenant/orgs)')
  if (res.status() !== 201 && res.status() !== 200) throw new Error(`POST /api/tenant/orgs answered ${res.status()}`)
  const tenantId = String(((await res.json()) as { id?: unknown }).id ?? '')
  if (!tenantId) throw new Error('POST /api/tenant/orgs answered no id')
  await page.waitForURL((u) => !isAt(u.href, 'marketplace', '/checkout'), { timeout: 180_000 })
  return { tenantId, summary, covers, stripe, label }
}

/** The provisioning record of a tenant on the storefront (GET /api/provisioning/tenant/{id}, what the timeline renders). */
async function provision(request: APIRequestContext, tenantId: string): Promise<{ status: string; steps: Array<{ name: string; status: string }> }> {
  const r = await request.get(mkt(`/api/provisioning/tenant/${encodeURIComponent(tenantId)}`))
  if (r.status() !== 200) throw new Error(`GET /api/provisioning/tenant/${tenantId} answered ${r.status()}`)
  const body = (await r.json()) as { status?: unknown; steps?: unknown }
  if (typeof body.status !== 'string' || !Array.isArray(body.steps)) throw new Error(`GET /api/provisioning/tenant/${tenantId} answered no status and steps`)
  return { status: body.status, steps: (body.steps as Array<{ name?: unknown; status?: unknown }>).map((s) => ({ name: String(s.name ?? ''), status: String(s.status ?? '') })) }
}

/** Waits for the end of a provisioning, collecting every stage it saw go red. */
async function provisioned(request: APIRequestContext, tenantId: string, slug: string): Promise<{ status: string; steps: Array<{ name: string; status: string }>; failedSeen: string[] }> {
  const failedSeen = new Set<string>()
  const final = await until(`the provisioning of ${slug} ends`, async () => {
    const p = await provision(request, tenantId)
    for (const s of p.steps) if (s.status === 'failed') failedSeen.add(s.name)
    return p.status === 'completed' || p.status === 'failed' ? p : null
  }, 2_400_000, 15_000)
  return { ...final, failedSeen: [...failedSeen] }
}

/**
 * The brand this Sovereign configures for its storefront, read from outside the page: marketplace.brand.name of
 * its HelmRelease bp-catalyst-platform, the field Settings → Marketplace writes. A Sovereign that names no brand
 * has none of its own: its storefront can then only show the parent's default (layouts/Layout.astro), which rows
 * 75 and 77 forbid, so the rows fail with that reason instead of comparing the page with that default.
 */
async function sovereignBrand(): Promise<string> {
  const hr = await getObj('a', 'helmreleases.helm.toolkit.fluxcd.io', 'flux-system', 'bp-catalyst-platform')
  if (!hr) throw new Error('no HelmRelease flux-system/bp-catalyst-platform: the brand this Sovereign configures cannot be read')
  const name = str(hr, 'spec.values.marketplace.brand.name').trim()
  if (!name) throw new Error('this Sovereign configures no storefront brand (spec.values.marketplace.brand.name of the HelmRelease flux-system/bp-catalyst-platform is empty; Settings → Marketplace sets it): its storefront can only show the parent\'s default brand')
  return name
}

/** What the launch page rendered of its provisioning timeline, sampled from the launch until the page left the storefront. */
interface Rendered { labels: string[]; statuses: string[]; red: string[]; samples: number; error: string }

/**
 * Samples the provisioning timeline the launch page renders (ProvisioningTimeline.svelte: one
 * provisioning-stage per stage, its label and its data-status) every 3 s, until the page forwards to the
 * console, the timeline shows every stage done or one red, or the time is up (120 s while it shows none). It
 * runs beside the second walk and never throws: what went wrong is in error, and samples counts the reads that
 * found a timeline.
 */
async function renderedTimeline(page: Page, timeoutMs: number): Promise<Rendered> {
  const out: Rendered = { labels: [], statuses: [], red: [], samples: 0, error: '' }
  const red = new Set<string>()
  const start = Date.now()
  try {
    while (Date.now() < start + timeoutMs && isAt(page.url(), 'marketplace')) {
      if (out.samples === 0 && Date.now() - start > 120_000) { out.error = 'no provisioning-stage within 120 s of the launch'; break }
      const stages = await page.locator('[data-testid="provisioning-stage"]').evaluateAll((els) =>
        els.map((e) => ({ label: (e.querySelector('span')?.textContent ?? '').trim(), status: e.getAttribute('data-status') ?? '' })))
      if (stages.length > 0) {
        out.samples++
        out.labels = stages.map((s) => s.label)
        out.statuses = stages.map((s) => s.status)
        for (const s of stages) if (s.status === 'failed') red.add(s.label)
        if (red.size > 0 || stages.every((s) => s.status === 'completed')) break
      }
      await new Promise((r) => setTimeout(r, 3_000))
    }
  } catch (e) {
    // A read the page's own forward to the console cut short ends the sampling; any other is the reason.
    if (isAt(page.url(), 'marketplace')) out.error = reason(e)
  }
  out.red = [...red]
  return out
}

/** The status of the page's last navigation to a host, recorded from the moment it is called until stop(). */
interface Nav { status: () => number; stop: () => void }
function watchNavigation(page: Page, host: string): Nav {
  let status = 0
  const on = (r: Response) => { if (r.request().isNavigationRequest() && new URL(r.url()).host === host) status = r.status() }
  page.on('response', on)
  return { status: () => status, stop: () => { page.off('response', on) } }
}

/**
 * Waits for a page to land on an Organization's console, and what the signed-in customer sees there. The
 * storefront forwards to the console by itself as soon as the console answers (LaunchingStep.svelte), often
 * before the provisioning ends, so the status is the one nav recorded since before the launch.
 */
async function landing(page: Page, host: string, nav: Nav, timeoutMs: number): Promise<Landing> {
  try {
    await page.waitForURL((u) => u.host === host, { timeout: timeoutMs })
    await page.waitForLoadState('load')
  } finally {
    nav.stop()
  }
  const status = nav.status()
  await page.getByTestId('profile-menu-avatar').waitFor({ timeout: 120_000 })
  const loginFields = await page.locator('input[type="password"], input[autocomplete="one-time-code"], input[maxlength="6"]').count()
  const avatarTitle = (await page.getByTestId('profile-menu-avatar').getAttribute('title')) ?? ''
  const who = await page.request.get(`https://${host}/api/v1/whoami`)
  if (who.status() !== 200) throw new Error(`GET https://${host}/api/v1/whoami answered ${who.status()}`)
  const whoami = (await who.json()) as Landing['whoami']
  const sidebar = await page.getByTestId('sov-console-sidebar').innerText()
  return { url: bare(page.url()), status, loginFields, avatarTitle, whoami, sidebar }
}

async function walk(adm: BrowserContext, cus: BrowserContext, request: APIRequestContext): Promise<void> {
  const admPage = await adm.newPage()
  let mb: Mailbox | null = null
  try {
    mb = await mailbox(TAG)
    await cleanUp(admPage, request, mb)
    const pool = await poolDomains(admPage)
    rec.tld = { one: pool[0] ?? '', two: pool[1] ?? '' }
  } catch (e) {
    const why = `the clean-up before the walk: ${reason(e)}`
    rec.stopped = { one: why, two: why }
    return
  }
  const m = mb
  let code1 = ''
  await at('one', '74', async () => {
    await admPage.goto(consoleURL('/billing/vouchers'))
    await admPage.getByTestId('bss-vouchers-issue-cta').click()
    const before = new Set((await vouchers(admPage)).map((v) => v.code))
    await admPage.getByTestId('bss-issue-voucher-code').fill('')
    await admPage.getByTestId('bss-issue-voucher-credit').fill(String(CREDIT))
    await admPage.getByTestId('bss-issue-voucher-plan-tier').selectOption('m')
    await admPage.getByTestId('bss-issue-voucher-description').fill(ONE.voucher)
    await admPage.getByTestId('bss-issue-voucher-max-redemptions').fill('1')
    await admPage.getByTestId('bss-issue-voucher-submit').click()
    await admPage.getByTestId('bss-issue-voucher-modal').waitFor({ state: 'hidden', timeout: 60_000 })
    const added = (await vouchers(admPage)).filter((v) => !before.has(v.code))
    if (added.length !== 1) throw new Error(`the Issue form added ${added.length} vouchers`)
    code1 = added[0].code
    const row = admPage.getByTestId(`bss-voucher-row-${code1}`)
    await row.waitFor({ timeout: 60_000 })
    // The cells of a row: code, recipient, plan, credit, status, issued, expires, redemptions (VouchersPage.tsx).
    const cells = await row.locator('td').allInnerTexts()
    return {
      added, plan: (await admPage.getByTestId(`bss-voucher-plan-${code1}`).innerText()).trim(), credit: (cells[3] ?? '').trim(),
      redeemed: (cells[7] ?? '').trim(), status: (await admPage.getByTestId(`bss-voucher-status-${code1}`).innerText()).trim(),
    }
  })
  const one = await cus.newPage()
  await walkOrg(one, 'one', ONE.slug, code1, true)
  let token = ''
  await at('one', '84', async () => {
    await one.locator('input[type="email"]').fill(m.address)
    const floor = (await codeMails(m, Number.MAX_SAFE_INTEGER)).newest
    await one.getByRole('button', { name: 'Send sign-in code' }).click()
    const code = await nextCode(m, floor)
    await one.locator('input[maxlength="6"]').first().fill(code, { force: true })
    const verify = one.getByRole('button', { name: 'Verify & Continue' })
    if (await verify.isEnabled().catch(() => false)) await verify.click().catch(() => undefined)
    token = await until('the customer is signed in', async () => { const t = await one.evaluate(() => window.localStorage.getItem('org-token') ?? ''); return t ? t : null }, 60_000, 2_000)
    await one.locator('input[placeholder="My Company"]').waitFor({ timeout: 60_000 })
    return { text: await one.locator('main').innerText(), loginFields: await one.locator('input[type="password"], input[maxlength="6"]').count(), signedIn: true }
  })
  await at('one', '92', async () => {
    const preview = () => cus.request.post(mkt('/api/billing/vouchers/redeem-preview'), { data: { code: code1 } })
    const firstRes = await preview()
    const firstCredit = firstRes.status() === 200 ? Number(((await firstRes.json()) as { credit_omr?: unknown }).credit_omr) : NaN
    const burst: number[] = []
    let retryAfter = 0
    for (let i = 0; i < 10; i++) {
      const r = await preview()
      burst.push(r.status())
      if (r.status() === 429) retryAfter = Math.max(retryAfter, Number(r.headers()['retry-after'] ?? '0') || 0)
    }
    const p = await cus.newPage()
    await p.goto(mkt(`/redeem/?code=${encodeURIComponent(code1)}`))
    await p.locator('#redeem-throttled:visible, #redeem-valid:visible, #redeem-not-valid:visible').first().waitFor({ timeout: 60_000 })
    const panel = (await p.locator('#redeem-throttled').isVisible()) ? (await p.locator('#redeem-throttled').innerText()).trim() : ''
    await p.close()
    // The limiter's own window (Retry-After), then one preview again.
    await new Promise((r) => setTimeout(r, (Math.min(Math.max(retryAfter, 10), 600) + 2) * 1000))
    return { first: firstRes.status(), firstCredit, burst, panel, afterWindow: (await preview()).status() }
  })
  let tenant1 = ''
  // Row 87's status, recorded from the launch on: the storefront forwards to the console on its own, often
  // before the provisioning ends, so a listener attached at the landing would miss the navigation.
  const nav1 = watchNavigation(one, consoleOf(ONE.slug, rec.tld.one))
  await at('one', '85', async () => {
    const l = await launch(one, ONE.name)
    tenant1 = l.tenantId
    return { summary: l.summary, covers: l.covers, stripe: l.stripe, label: l.label, url: bare(one.url()) }
  })
  // The timeline the launch page renders, read while the second walk runs; row 86 compares it with the record.
  const timeline = rec.stopped.one ? null : renderedTimeline(one, 2_400_000)
  // The second Organization: the same customer, a second voucher and the second pool domain.
  if (!token && !rec.stopped.two) rec.stopped.two = `the first walk did not sign the customer in (${rec.stopped.one})`
  const two = await cus.newPage()
  const nav2 = watchNavigation(two, consoleOf(TWO.slug, rec.tld.two))
  let tenant2 = ''
  await at('two', null, async () => {
    const v2 = await issueVoucher(admPage, { credit_omr: CREDIT, plan_tier: 'm', description: TWO.voucher, max_redemptions: 1 })
    await walkOrg(two, 'two', TWO.slug, v2.code, false)
    if (!rec.stopped.two) tenant2 = (await launch(two, TWO.name)).tenantId
    return undefined as never
  })
  await at('one', '86', async () => {
    const p = await provisioned(request, tenant1, ONE.slug)
    const t = timeline ? await timeline : null
    if (!t || t.samples === 0) throw new Error(`the launch page rendered no provisioning timeline before it left the storefront${t?.error ? ` (${t.error})` : ''}`)
    if (t.error) throw new Error(`reading the provisioning timeline of the launch page: ${t.error}`)
    return { shown: t.labels, shownStatus: t.statuses, shownRed: t.red, failedSeen: p.failedSeen, status: p.status, steps: p.steps }
  }, false)
  await at('one', '87', async () => {
    const host = consoleOf(ONE.slug, rec.tld.one)
    const seen = await landing(one, host, nav1, 900_000)
    rec.seen['88'] = seen
    return { url: seen.url, status: seen.status, direct: (await request.get(`https://${host}/`)).status() }
  })
  nav1.stop()
  await at('one', '89', async () => {
    await one.goto(`https://${consoleOf(ONE.slug, rec.tld.one)}/apps`)
    await one.locator('[data-testid^="sov-app-card-"]').first().waitFor({ timeout: 120_000 })
    const cards = await one.locator('[data-testid^="sov-app-card-"]').evaluateAll((els) => els.map((e) => (e.getAttribute('data-testid') ?? '').slice('sov-app-card-'.length)))
    const card = cards.find((c) => /wordpress/i.test(c))
    if (!card) throw new Error(`no WordPress card among ${cards.join(', ')}`)
    const el = one.getByTestId(`sov-app-card-${card}`)
    const dataStatus = await until('the WordPress card reads installed', async () => { const s = (await el.getAttribute('data-status')) ?? ''; return s === 'installed' ? s : null }, 600_000, 10_000).catch(async () => (await el.getAttribute('data-status')) ?? '')
    const reasons = await one.getByTestId(`app-card-status-reason-${card}`).count()
    const popup = one.context().waitForEvent('page', { timeout: 180_000 })
    popup.catch(() => undefined) // awaited below; a click that fails must not leave it unhandled
    await one.getByTestId(`sov-app-open-${card}`).click({ timeout: 120_000 })
    const opened = await popup
    await opened.waitForLoadState('load')
    const res = await opened.request.get(opened.url())
    const openedUrl = bare(opened.url())
    await opened.close()
    return { cards, card, dataStatus, reasons, openedUrl, openedStatus: res.status() }
  }, false)
  await at('one', '90', async () => {
    const host = (await appHosts(ONE.slug, 'wordpress'))[0]
    const r = await request.get(`https://${host}/`)
    return { host, status: r.status(), wordpress: /WordPress/i.test(await r.text()) }
  }, false)
  await at('one', '91', async () => {
    await one.goto(mkt('/'))
    await one.waitForURL((u) => !isAt(u.href, 'marketplace'), { timeout: 600_000 })
    const consoles = (await customerOrgs(request, token)).filter((o) => o.status !== 'deleted').map((o) => o.console_host ?? '')
    return { url: bare(one.url()), consoles }
  }, false)
  await at('one', 'R15', async () => {
    const cr = (await orgCRs()).find((o) => slugOf(o) === ONE.slug)
    if (!cr) throw new Error(`no Organization object ${ONE.slug}`)
    const plans = await request.get(mkt('/api/catalog/plans?product=generic'))
    if (plans.status() !== 200) throw new Error(`GET /api/catalog/plans answered ${plans.status()}`)
    const all = (await plans.json()) as Array<{ id?: string; slug?: string; name?: string }>
    const plan = all.find((p) => p.id === saw('79').planId)
    return { planSlug: str(cr, 'spec.planSlug'), wanted: plan?.slug || plan?.name?.toLowerCase() || '' }
  }, false)
  await at('one', '216', async () => {
    await admPage.goto(consoleURL('/organizations'))
    const link = admPage.locator(`[data-testid^="organizations-cell-name-"][href$="/organizations/${ONE.slug}"]`).first()
    await link.waitFor({ timeout: 60_000 })
    const id = ((await link.getAttribute('data-testid')) ?? '').slice('organizations-cell-name-'.length)
    const directoryStatus = (await admPage.getByTestId(`organizations-cell-status-${id}`).innerText()).trim()
    const vcluster: string[] = []
    for (const r of REGIONS) for (const ns of await namespacesOf(r, ONE.slug)) {
      const sts = await getObj(r, 'statefulset', ns.metadata.name, 'vcluster')
      if (sts) vcluster.push(`${r}/${ns.metadata.name} ${String(sts.status?.readyReplicas ?? 0)}/${String(sts.spec?.replicas ?? 1)}`)
    }
    return { provisioned: rec.seen['86']?.status ?? '', directoryStatus, vcluster }
  }, false)
  await at('two', '93', async () => {
    const host = consoleOf(TWO.slug, rec.tld.two)
    const p = await provisioned(request, tenant2, TWO.slug)
    const seen = await landing(two, host, nav2, 900_000)
    rec.seen['94'] = seen
    const cr = (await orgCRs()).find((o) => slugOf(o) === TWO.slug)
    if (!cr) throw new Error(`no Organization object ${TWO.slug}`)
    const certName = `org-wildcard-tls-${TWO.slug}-${rec.tld.two.replace(/\./g, '-')}`
    const cert = (await list('a', 'certificates.cert-manager.io')).find((c) => c.metadata.name === certName)
    return { tld: rec.tld.two, parentDomain: str(cr, 'spec.tenantPublic.parentDomain'), cert: cert ? ready(cert) : `no Certificate ${certName}`, url: seen.url, provisioned: p.status }
  })
  nav2.stop()
  await at('two', '95', async () => {
    const hosts = [(await appHosts(ONE.slug, 'wordpress'))[0], (await appHosts(TWO.slug, 'wordpress'))[0]]
    const statuses: number[] = []
    const wordpress: boolean[] = []
    for (const h of hosts) {
      const r = await request.get(`https://${h}/`)
      statuses.push(r.status())
      wordpress.push(/WordPress/i.test(await r.text()))
    }
    return { hosts, statuses, wordpress }
  }, false)
  await at('one', '226', async () => {
    const ks = (await list('a', 'kustomizations.kustomize.toolkit.fluxcd.io')).filter((k) => /^catalyst-tenant-.+-apps$/.test(k.metadata.name))
    const out: Array<{ name: string; ready: string; running: number }> = []
    for (const k of ks) {
      const slug = k.metadata.name.replace(/^catalyst-tenant-/, '').replace(/-apps$/, '')
      let running = 0
      // The application's Pods, synced from the Organization's vCluster into its host namespaces.
      for (const r of REGIONS) for (const ns of await namespacesOf(r, slug)) {
        running += (await list(r, 'pods', ns.metadata.name)).filter((p) => str(p, 'status.phase') === 'Running' && /-x-.+-x-/.test(p.metadata.name)).length
      }
      out.push({ name: k.metadata.name, ready: ready(k), running })
    }
    return { kustomizations: out }
  }, false)
  await at('one', 'R16', async () => {
    const consoles: Array<{ host: string; status: number }> = []
    const templates: Array<{ host: string; rules: string }> = []
    const routes = await list('a', 'httproutes.gateway.networking.k8s.io')
    for (const cr of await orgCRs()) {
      const parent = str(cr, 'spec.tenantPublic.parentDomain')
      if (!parent) throw new Error(`the Organization ${slugOf(cr)} has no pool domain (spec.tenantPublic.parentDomain)`)
      const host = consoleOf(str(cr, 'spec.tenantPublic.subdomain').trim() || slugOf(cr), parent)
      consoles.push({ host, status: (await request.get(`https://${host}/`)).status() })
      const route = routes.find((r) => arr(r, 'spec.hostnames').map(String).includes(host))
      // A route's rules without its names: which paths go to which backend and port.
      const rules = arr(route, 'spec.rules').map((r) => JSON.stringify({ matches: arr(r, 'matches'), backends: arr(r, 'backendRefs').map((b) => `${str(b, 'name')}:${String((b as { port?: number }).port ?? '')}`) }))
      templates.push({ host, rules: rules.join('\n') })
    }
    return { consoles, templates }
  }, false)
}

test.describe('the storefront funnel of a customer, twice', () => {
  test.beforeAll(async ({ browser, playwright }) => {
    test.setTimeout(7_200_000)
    if (existsSync(CACHE)) {
      rec = JSON.parse(readFileSync(CACHE, 'utf8')) as WalkRecord
      return
    }
    save()
    const request = await playwright.request.newContext()
    const adm = await browser.newContext({ storageState: stateOf('run'), baseURL: consoleURL('/'), viewport: VIEWPORT })
    const cus = await browser.newContext({ viewport: VIEWPORT })
    try {
      await walk(adm, cus, request)
    } finally {
      rec.done = true
      save()
      await cus.close()
      await adm.close()
      await request.dispose()
    }
  })

  test.afterAll(async ({ browser, playwright }) => {
    test.setTimeout(2_400_000)
    if (rec.cleaned) return
    rec.cleaned = true
    save()
    const request = await playwright.request.newContext()
    const adm = await browser.newContext({ storageState: stateOf('run'), baseURL: consoleURL('/'), viewport: VIEWPORT })
    try {
      await cleanUp(await adm.newPage(), request, await mailbox(TAG))
    } finally {
      await adm.close()
      await request.dispose()
    }
  })

  test('72 Operator console BSS → Vouchers page renders the voucher issuance form (code, credit OMR, plan tier, description); no login wall.', async ({ page }) => {
    await page.goto(consoleURL('/billing/vouchers'))
    expect(isAt(page.url(), 'console', '/billing/vouchers'), `no sign-in wall (${page.url()})`).toBe(true)
    await expect(page.getByTestId('bss-vouchers-page')).toBeVisible({ timeout: 60_000 })
    await expectNoLoginForm(page, 'the Vouchers page')
    await page.getByTestId('bss-vouchers-issue-cta').click()
    const modal = page.getByTestId('bss-issue-voucher-modal')
    await expect(modal).toBeVisible()
    for (const f of ['code', 'credit', 'plan-tier', 'description']) await expect(modal.getByTestId(`bss-issue-voucher-${f}`), `the ${f} field`).toBeEditable()
    const options = await modal.getByTestId('bss-issue-voucher-plan-tier').locator('option').evaluateAll((els) => els.map((e) => (e as HTMLOptionElement).value))
    expect(options[0], 'the plan tier offers "any plan" first').toBe('')
    expect(options.filter((o) => o !== '').length, 'the plan tiers of the catalogue').toBeGreaterThan(0)
  })

  test('73 In the voucher form, type a weak code 1234 → Issue → the form rejects it inline ("voucher code must be at least 12 characters").', async ({ page }) => {
    await page.goto(consoleURL('/billing/vouchers'))
    await page.getByTestId('bss-vouchers-issue-cta').click()
    await page.getByTestId('bss-issue-voucher-code').fill('1234')
    await page.getByTestId('bss-issue-voucher-credit').fill('1')
    await page.getByTestId('bss-issue-voucher-submit').click()
    await expect(page.getByTestId('bss-issue-voucher-error')).toHaveText(/^Voucher code must be at least 12 characters/)
    await expect(page.getByTestId('bss-issue-voucher-modal'), 'the form stays open').toBeVisible()
    expect((await vouchers(page)).filter((v) => v.code === '1234'), 'a voucher 1234').toEqual([])
  })

  test('74 Leave the code field empty → Issue → a new voucher row appears with a server-auto-generated high-entropy code, credit, plan tier, unredeemed 0/1.', async () => {
    const s = saw('74')
    expect(s.added, 'the vouchers the form added').toHaveLength(1)
    const v = s.added[0]
    expect(v.code, 'a server-generated code (VCH- and 12 characters, about 60 bits)').toMatch(/^VCH-[A-Z0-9]{12}$/)
    expect([v.credit_omr, v.plan_tier, v.description, v.max_redemptions, v.times_redeemed], 'the stored voucher').toEqual([CREDIT, 'm', ONE.voucher, 1, 0])
    expect([s.plan.toLowerCase(), s.credit, s.redeemed, s.status], 'the new row: plan, credit, redemptions, status').toEqual(['m', `${CREDIT} OMR`, '0/1', 'Active'])
  })

  test('75 Stranger opens the redeem page with ?code=<CODE> → sees "Voucher valid · 5000 OMR" with THIS Sovereign\'s brand chrome (no openova.io / mothership logo).', async () => {
    const s = saw('75')
    expect(s.host, 'the storefront of this Sovereign').toBe(new URL(mkt('/')).host)
    expect(s.credit, 'the credit of the voucher').toBe(`${CREDIT} OMR credit`)
    expect(s.code, 'the code shown').toBe(saw('74').added[0].code)
    expect(s.brand, 'the brand of the page is the one this Sovereign configures for its storefront').toBe(await sovereignBrand())
    expect(s.mothershipRefs, 'links and images to openova.io').toEqual([])
  })

  test('76 Open the redeem page with a junk code → a generic "voucher not valid" message (no tombstone / no detail leak).', async ({ browser, request }) => {
    const junk = 'GATEG23JUNKCODE0'
    const r = await request.post(mkt('/api/billing/vouchers/redeem-preview'), { data: { code: junk } })
    expect(r.status(), 'POST /api/billing/vouchers/redeem-preview of a junk code').toBe(404)
    const body = (await r.json()) as Record<string, unknown>
    expect(Object.keys(body).filter((k) => ['credit_omr', 'description', 'code', 'active'].includes(k)), 'voucher details in the answer').toEqual([])
    const ctx = await browser.newContext()
    try {
      const p = await ctx.newPage()
      await p.goto(mkt(`/redeem/?code=${junk}`))
      await expect(p.locator('#redeem-not-valid')).toBeVisible({ timeout: 60_000 })
      await expect(p.locator('#redeem-not-valid h1')).toHaveText('Voucher not valid')
      await expect(p.locator('#redeem-not-valid-detail')).toHaveText('This code does not exist or has been retired.')
      for (const id of ['redeem-valid', 'redeem-ended']) await expect(p.locator(`#${id}`), `the ${id} panel`).toBeHidden()
    } finally {
      await ctx.close()
    }
  })

  test('77 Redeem page source (DevTools): the HTML shows THIS Sovereign\'s brand and no console.openova.io / omantel.openova.io / bare openova.io host literal.', async ({ browser, request }) => {
    const code = saw('74').added[0].code
    const src = await request.get(mkt(`/redeem/?code=${encodeURIComponent(code)}`))
    expect(src.status(), 'GET the redeem page').toBe(200)
    const html = await src.text()
    expect(MOTHERSHIP_HOST.exec(html)?.[0] ?? '', 'a host literal of the parent in the page source').toBe('')
    const ctx = await browser.newContext()
    try {
      const p = await ctx.newPage()
      await p.goto(mkt(`/redeem/?code=${encodeURIComponent(code)}`))
      await p.locator('#redeem-valid, #redeem-ended, #redeem-not-valid').first().waitFor({ timeout: 60_000 })
      const dom = await p.content()
      expect(MOTHERSHIP_HOST.exec(dom)?.[0] ?? '', 'a host literal of the parent in the rendered page').toBe('')
      expect(await p.evaluate(() => document.documentElement.dataset.tenantBrand ?? ''), 'the brand of the page').toBe(await sovereignBrand())
    } finally {
      await ctx.close()
    }
  })

  test('78 Click "Sign up to redeem" → the browser lands on the plan picker grid (/plans).', async () => {
    const s = saw('78')
    expect(s.url, 'where the button lands').toBe(mkt('/plans').replace(/\/$/, ''))
    expect(s.pending, 'the voucher the storefront keeps for checkout').toBe(saw('74').added[0].code)
  })

  test('79 Plans grid shows the tiers (S/M/L/XL/Flexi) with price/CPU/memory → pick plan M → advances to the app catalog (/apps).', async () => {
    // The tiers are the packages the Sovereign's BSS publishes (GET /api/v1/public/packages), which the page reads.
    const s = saw('79')
    expect(s.status, 'GET /api/v1/public/packages').toBe(200)
    expect(TIERS.filter((t) => !s.packages.some((p) => p.name.trim() === t)), 'tiers the storefront does not offer').toEqual([])
    for (const p of s.packages) {
      const cpu = p.shape?.vcpu ?? p.includes?.vcpu
      const mem = p.shape?.memory_gb ?? p.includes?.memory_gb
      expect([typeof cpu, typeof mem], `the CPU and memory ${p.name} publishes`).toEqual(['number', 'number'])
      const shown = s.shown[p.sku] ?? ''
      // The storefront's own wording of the two (PACKAGE_STRINGS, core/marketplace/src/lib/packages.ts).
      expect([shown.includes(p.name), p.price_month !== '' && shown.includes(p.price_month), shown.includes(`${cpu} vCPU`), shown.includes(`${mem} GB`)], `${p.name} shows its name, price, CPU and memory (${shown})`).toEqual([true, true, true, true])
    }
    expect(s.packages.find((p) => p.sku === s.chosen)?.name.trim(), 'the package picked').toBe('M')
    expect(s.planId, 'the plan the cart holds').not.toBe('')
    expect(s.url, 'where the pick lands').toBe(mkt('/apps').replace(/\/$/, ''))
  })

  test('80 App catalog grid (served from THIS Sovereign\'s catalog) → pick WordPress → advances to add-ons (/addons).', async () => {
    const s = saw('80')
    expect([s.status, s.host], 'GET /api/catalog/apps of this storefront').toEqual([200, new URL(mkt('/')).host])
    expect([...s.shown].sort(), 'the cards are the catalogue the page read').toEqual([...s.catalog].sort())
    expect(s.shown, 'the catalogue holds WordPress').toContain('WordPress')
    expect(s.selected, 'the WordPress card is selected').toBe(true)
    expect(s.url, 'where Continue lands').toBe(mkt('/addons').replace(/\/$/, ''))
  })

  test('81 Add-ons step shows optional add-ons → leave defaults → Continue → advances to the BCP topology step (/bcp).', async () => {
    const s = saw('81')
    expect(s.tiles.length, 'optional add-ons shown').toBeGreaterThan(0)
    expect(s.pressedAfter, 'the add-ons left at their defaults').toEqual(s.pressedBefore)
    expect(s.url, 'where Continue lands').toBe(mkt('/bcp').replace(/\/$/, ''))
  })

  test('82 BCP topology step shows BOTH Single-region and Active-hot-standby radios (the Pillar-2 BCP choice at signup) → select Active-hot-standby → Continue → review.', async () => {
    const s = saw('82')
    expect([s.single, s.hot], 'the Single-region and Active-hot-standby cards').toEqual([true, true])
    expect(s.hotChecked, `Active-hot-standby can be selected (locked: ${s.locked || 'no'}, switched to Grow: ${s.switchedToGrow})`).toBe(true)
    expect(s.url, 'where Continue lands').toBe(mkt('/review').replace(/\/$/, ''))
  })

  test('83 Review summary shows the chosen plan (M), app (WordPress), topology (Active-hot-standby), the Org slug, and the pool-TLD → Proceed to checkout.', async () => {
    const s = saw('83')
    expect(s.plan, 'the plan the review has checked').toBe(`review-plan-${saw('79').chosen}`)
    expect(s.apps.map((a) => a.trim()).filter((a) => /WordPress/.test(a)).length, `the stack holds WordPress (${s.apps.join(', ')})`).toBe(1)
    expect(s.topology, 'the topology').toBe('Active-hot-standby')
    expect(s.orgUrl, 'the Organization slug on its pool domain').toContain(`${ONE.slug}.${saw('81').tld}`)
    expect(s.url, 'where Checkout lands').toBe(mkt('/checkout').replace(/\/$/, ''))
  })

  test('84 On checkout, enter the stranger\'s email → Send code → type the emailed sign-in code (no password) → the page shows the stranger signed in + Org confirmed.', async () => {
    const s = saw('84')
    expect(s.signedIn, 'the storefront holds the customer session').toBe(true)
    expect(s.loginFields, 'password and code fields left on the page').toBe(0)
    expect(s.text, 'the page names the signed-in customer').toMatch(new RegExp(`\\+${TAG}@`))
    expect(s.text, 'the page confirms the Organization').toContain(`${ONE.slug}.${saw('81').tld}`)
  })

  test('92 As the signed-in customer, rapidly re-submit checkout/redeem >5× in a few seconds → after ~5 attempts a rate-limit notice appears; a single legitimate redeem is unaffected.', async () => {
    const s = saw('92')
    expect([s.first, s.firstCredit], 'the first redeem preview').toEqual([200, CREDIT])
    // Attempt 1 is the single preview, attempts 2 to 11 the burst; "after ~5 attempts": the first limited
    // one is the third to the seventh (the hand walk measured a limiter of 5 per 10 s).
    const firstLimited = s.burst.indexOf(429) + 2
    expect(firstLimited, `the burst is limited (${s.burst.join(' ')})`).toBeGreaterThanOrEqual(2)
    expect([firstLimited >= 3, firstLimited <= 7], `the attempt that is limited first: ${firstLimited}`).toEqual([true, true])
    expect(s.panel, 'the redeem page shows the rate-limit notice').toMatch(/^Too many attempts/)
    expect(s.afterWindow, 'a single redeem preview after the window').toBe(200)
  })

  test('85 Checkout summary: the voucher credit is applied — "Credit covers this order — 0 OMR due" (no Stripe card form) → click Launch / Place order.', async () => {
    const s = saw('85')
    expect(s.covers, 'the order summary says the credit covers the order').toBe(true)
    expect(s.summary, 'nothing is due').toMatch(/Due now\s*OMR 0\.000/)
    expect(s.stripe, 'the Stripe card note').toBe(false)
    expect(s.label, 'the button').toBe('Launch my Organization')
    expect(isAt(s.url, 'marketplace', '/checkout'), `the launch left the checkout (${s.url})`).toBe(false)
  })

  test('86 Provisioning progress timeline advances to Done (Creating Org → Committing manifests → Provisioning vCluster → Deploying WordPress → TLS → Health), no hang/red step.', async () => {
    const s = saw('86')
    const names = s.steps.map((x) => x.name)
    // The labels the page rendered are the stages of the provisioning record, in its order (they are laid out
    // up front, consumer.go), and the page rendered none of them red.
    expect(s.shown, `the stage labels the launch page rendered (last seen ${s.shownStatus.join(' ')}), against the provisioning record`).toEqual(names)
    expect(s.shownRed, 'stages the launch page rendered red').toEqual([])
    expect(s.failedSeen, 'stages that went red').toEqual([])
    expect(s.status, 'the provisioning').toBe('completed')
    expect(STAGES.filter((n) => !names.includes(n)), 'stages of the timeline that are missing').toEqual([])
    expect(STAGES.map((n) => names.indexOf(n)), 'the stages in order').toEqual(STAGES.map((n) => names.indexOf(n)).sort((a, b) => a - b))
    expect(s.steps.filter((x) => x.status !== 'completed').map((x) => `${x.name}=${x.status}`), 'stages not done').toEqual([])
  })

  test('87 After Launch, the marketplace redirects to the per-Org console — URL becomes the per-Org console and loads with publicly-trusted TLS (no cert error / connection-refused).', async () => {
    const s = saw('87')
    expect(new URL(s.url).host, 'where the launch lands').toBe(consoleOf(ONE.slug, saw('81').tld))
    expect(s.status, 'the console page').toBe(200)
    expect(s.direct, 'GET of the console over TLS the runner trusts').toBe(200)
  })

  test('88 Per-Org console landing: the stranger is signed in zero-click as the Org owner (their email in the avatar, their Org in the header) — no login/PIN form.', async () => {
    const s = saw('88')
    expect(s.loginFields, 'sign-in fields on the landing').toBe(0)
    expect(s.avatarTitle, 'the avatar names the customer').toMatch(new RegExp(`\\+${TAG}@`))
    expect([s.whoami.email, s.whoami.org, s.whoami.orgScoped], 'the console session').toEqual([s.avatarTitle, ONE.slug, true])
    expect(s.sidebar, 'the header names their Organization').toMatch(new RegExp(`${ONE.slug}|${ONE.name}`))
  })

  test('89 Per-Org console → Applications view shows the purchased WordPress app card Running/Healthy → click Open.', async () => {
    const s = saw('89')
    expect(s.card, `the WordPress card among ${s.cards.join(', ')}`).not.toBe('')
    expect(s.dataStatus, 'the card status').toBe('installed')
    expect(s.reasons, 'reasons the card shows for a status that is not Ready').toBe(0)
    expect(new URL(s.openedUrl).host, 'Open goes to the application').toMatch(new RegExp(`^wordpress\\.${ONE.slug}\\.`))
    expect(s.openedStatus, 'the page Open opened').toBe(200)
  })

  test('90 Terminal acceptance: the purchased WordPress app SERVES at its own FQDN the app FQDN — the live rendered WordPress site (not 404/502/cert error).', async () => {
    const s = saw('90')
    expect(s.host, 'the application host').toMatch(new RegExp(`^wordpress\\.${ONE.slug}\\.`))
    expect([s.status, s.wordpress], 'GET of the application: status and a WordPress page').toEqual([200, true])
  })

  test('91 While signed in, re-open the marketplace root → the returning-user redirect sends the customer to their own Org console, never to console.openova.io / the mothership.', async () => {
    const s = saw('91')
    const host = new URL(s.url).host
    expect(MOTHERSHIP_HOST.test(host), `the parent's console (${host})`).toBe(false)
    expect(s.consoles, `the consoles of the customer's Organizations hold ${host}`).toContain(host)
  })

  test('R15 funnel plan-slug propagates end-to-end — the chosen plan reaches the provisioned Org.', async () => {
    const s = saw('R15')
    expect(s.wanted, 'the catalogue plan of the cart').not.toBe('')
    expect(s.planSlug, `the plan of the Organization ${ONE.slug}`).toBe(s.wanted)
  })

  test('216 A user with a coupon/voucher code redeems it → creates his Organization. Surface: https://marketplace.<fqdn>/redeem/?code=<CODE> (no session) → plan/region → Launch. PASS = the provisioning timeline runs to Done AND /organizations shows the Org ACTIVE backed by live vcluster pods (not a fake-green status).', async () => {
    const s = saw('216')
    expect(s.provisioned, 'the provisioning timeline').toBe('completed')
    expect(s.directoryStatus, `the directory status of ${ONE.slug}`).toMatch(/^active$/i)
    // Active-hot-standby places the Organization in two regions: one vCluster per region it runs in.
    expect(REGIONS.filter((r) => !s.vcluster.some((v) => v.startsWith(`${r}/`))), 'regions without a vCluster of the Organization').toEqual([])
    expect(s.vcluster.filter((v) => !/ (\d+)\/\1$/.test(v) || / 0\/\d+$/.test(v)), 'vCluster StatefulSets that are not ready').toEqual([])
  })

  test('93 Generality: mint a 2nd voucher and re-walk B.1→B.4 with a different slug (walk-stranger-two) and a different pool-TLD (omani.rest) → a 2nd Organization provisions and lands signed-in.', async () => {
    const s = saw('93')
    expect(s.tld, 'the second pool domain').toMatch(/^[a-z0-9-]+(\.[a-z0-9-]+)+$/)
    expect(s.tld, 'a second pool domain, not the first Organization\'s').not.toBe(saw('81').tld)
    expect(s.provisioned, `the provisioning of ${TWO.slug}`).toBe('completed')
    expect(s.parentDomain, `the pool domain of ${TWO.slug}`).toBe(s.tld)
    expect(s.cert, `the wildcard certificate of ${TWO.slug}`).toBe('True')
    expect(new URL(s.url).host, 'where the second launch lands').toBe(consoleOf(TWO.slug, s.tld))
  })

  test('94 The 2nd Org\'s console lands signed-in on a different TLD (the 2nd-Org console) — identical zero-click contract, no special-casing.', async () => {
    const s = saw('94')
    expect(new URL(s.url).host, 'the second console').toBe(consoleOf(TWO.slug, saw('93').tld))
    expect(s.loginFields, 'sign-in fields on the landing').toBe(0)
    expect(s.avatarTitle, 'the avatar names the customer').toMatch(new RegExp(`\\+${TAG}@`))
    expect(s.avatarTitle, 'the same customer as on the first console').toBe(saw('88').avatarTitle)
    expect([s.whoami.email, s.whoami.org, s.whoami.orgScoped], 'the console session').toEqual([s.avatarTitle, TWO.slug, true])
  })

  test('95 The 2nd Org\'s purchased app serves at its own different-TLD FQDN (the 2nd-Org app) — two Orgs, two TLDs, two running apps, identical mechanism.', async () => {
    const s = saw('95')
    expect(s.hosts[0].endsWith(`.${saw('81').tld}`) && s.hosts[1].endsWith(`.${saw('93').tld}`), `the two application hosts on two pool domains (${s.hosts.join(', ')})`).toBe(true)
    expect(s.statuses, 'GET of the two applications').toEqual([200, 200])
    expect(s.wordpress, 'both serve a WordPress page').toEqual([true, true])
  })

  test('226 The customer\'s PURCHASED app actually RUNS — every funnel Org\'s tenant-<slug>-apps Kustomization reconciles into a running pod (the app is reachable, not 404).', async () => {
    const s = saw('226')
    expect(s.kustomizations.map((k) => k.name), 'the funnel Organization of this run').toContain(`catalyst-tenant-${ONE.slug}-apps`)
    expect(s.kustomizations.filter((k) => k.ready !== 'True' || k.running === 0).map((k) => `${k.name} Ready=${k.ready} running=${k.running}`), 'funnel Organizations whose applications do not run').toEqual([])
    expect(saw('90').status, 'the application of this run is reachable').toBe(200)
  })

  test('R16 the funnel + BSS Org doors are collapsed onto ONE path — console.<slug> lands 200.', async () => {
    // The funnel's door is this run's Organization; every other Organization of the Sovereign came through
    // another door. One path: every console host answers 200 and its route carries the same rules.
    const s = saw('R16')
    const own = s.templates.find((t) => t.host === consoleOf(ONE.slug, saw('81').tld))
    expect(own?.rules ?? '', 'the console route of the funnel Organization').not.toBe('')
    const others = s.templates.filter((t) => !/^console\.gate-g23-/.test(t.host))
    expect(others.length, 'an Organization that came through another door than the funnel').toBeGreaterThan(0)
    expect(s.consoles.filter((c) => c.status !== 200).map((c) => `${c.host} ${c.status}`), 'consoles that do not land 200').toEqual([])
    expect(s.templates.filter((t) => t.rules !== own?.rules).map((t) => t.host), 'console routes with other rules than the funnel Organization\'s').toEqual([])
  })
})
