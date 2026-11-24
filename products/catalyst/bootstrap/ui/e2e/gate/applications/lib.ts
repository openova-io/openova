/**
 * e2e/gate/applications/lib.ts — what the specifications of gate check FND-G20 share (FND-162).
 *
 * FND-G20 is the console regression of the applications grid, the application detail page and the
 * catalogue: one test per row of docs/ledger/UAT.md that the row map marks test:FND-G20, titled with
 * the row id and its clause. The helpers read the candidate the way the clause names it: the console's
 * own API through the signed-in context, the cluster through regionKubectl. A helper never turns a
 * missing object into an empty answer: what a clause needs and cannot find fails the test with the
 * reason.
 */

import { expect, consoleURL, regionKubectl, type Region } from '../fixtures.ts'
import type { Page } from '@playwright/test'

/** The four placement modes, in the one vocabulary of the console (shared/lib/placement.ts). */
export const CANONICAL_MODES = ['singleton', 'active-passive', 'active-hot-standby', 'active-active']

/** The DR-capable control-plane components, each adopting flux-system/bp-<chart> (post_handover_spine_apps.go). */
export const DR_CAPABLE_SPINE = ['openbao', 'keycloak', 'harbor', 'gitea']

/** A unique token of this run, for values a test writes and then looks for. */
export const RUN_TOKEN = `${process.env.GITHUB_RUN_ID ?? 'local'}-${Date.now().toString(36)}`

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
  }
  spec?: Record<string, unknown>
  status?: Record<string, unknown>
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

/** One object, or null when the cluster has none of that name. */
export async function getObj(region: Region, kind: string, namespace: string, name: string): Promise<Obj | null> {
  const r = await regionKubectl(region, ['get', kind, '-n', namespace, name, '-o', 'json'])
  if (r.code !== 0) {
    if (/NotFound|not found/.test(r.stderr)) return null
    throw new Error(`kubectl get ${kind} -n ${namespace} ${name} exited ${r.code}: ${r.stderr.trim()}`)
  }
  return JSON.parse(r.stdout) as Obj
}

/** A value of an object by a dotted path, for example field(o, 'spec.placement'). */
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

/** The status of the Ready condition of an object: 'True', 'False', 'Unknown' or '' when it has none. */
export function ready(o: Obj): string {
  const c = arr(o, 'status.conditions').find((x) => str(x, 'type') === 'Ready')
  return c ? str(c, 'status') : ''
}

/** The Application CRs of the Sovereign (applications.apps.openova.io), in every namespace. */
export function applications(region: Region = 'a'): Promise<Obj[]> {
  return list(region, 'applications.apps.openova.io')
}

/** GET of a console API path through the signed-in context; anything but 200 fails the test. */
export async function apiJSON<T>(page: Page, path: string): Promise<T> {
  const r = await page.request.get(consoleURL(path), { headers: { Accept: 'application/json' } })
  expect(r.status(), `GET ${path}`).toBe(200)
  return (await r.json()) as T
}

/** One row of GET /api/v1/sovereign/apps (the Applications grid's source). */
export interface SovApp {
  id: string
  slug?: string
  title?: string
  blueprint?: string
  instance?: boolean
  topology?: string | { mode?: string }
  contextCount?: number
  bootstrapKit?: boolean
  org?: string
  status?: string
}

export async function sovereignApps(page: Page): Promise<SovApp[]> {
  const body = await apiJSON<{ apps?: SovApp[] }>(page, '/api/v1/sovereign/apps')
  expect(Array.isArray(body.apps), 'GET /api/v1/sovereign/apps answers a list apps').toBe(true)
  return body.apps as SovApp[]
}

export function topologyOf(a: SovApp): string {
  return typeof a.topology === 'string' ? a.topology : (a.topology?.mode ?? '')
}

/** The id the console gives one of the candidates' names, failing the test when it has none of them. */
export async function appIdOf(page: Page, ...names: string[]): Promise<string> {
  const apps = await sovereignApps(page)
  for (const n of names) {
    const a = apps.find((x) => x.id === n || x.slug === n)
    if (a) return a.id
  }
  throw new Error(`the Sovereign has no application named ${names.join(' or ')} (GET /api/v1/sovereign/apps)`)
}

/** The shared PostgreSQL instance (shared-pg, shared-pg-<x>): the one with the most contexts. */
export async function sharedPg(page: Page): Promise<SovApp> {
  const pg = (await sovereignApps(page))
    .filter((a) => a.instance === true && /^shared-pg(-|$)/.test(a.id))
    .sort((x, y) => (y.contextCount ?? 0) - (x.contextCount ?? 0))
  expect(pg.length, 'the Sovereign has a shared-pg instance').toBeGreaterThan(0)
  return pg[0]
}

/** Opens one application's page on a tab and waits for its tab strip. */
export async function openApp(page: Page, id: string, tab = 'overview'): Promise<void> {
  await page.goto(consoleURL(`/app/${encodeURIComponent(id)}/${tab}`))
  await expect(page.getByTestId('app-detail-tablist')).toBeVisible()
}

/** The labels of the tab strip of an application page, without their counts. */
export async function tabLabels(page: Page): Promise<string[]> {
  const tabs = page.getByTestId('app-detail-tablist').getByRole('tab')
  return tabs.evaluateAll((els) => els.map((e) => (e.firstChild?.textContent ?? '').trim()))
}

/** The placement target cards of the Topology tab: "<region> · <cluster> · <vcluster>" and the role line. */
export async function targetCards(page: Page): Promise<Array<{ where: string; role: string; sub: string }>> {
  const cards = page.getByTestId('topology-tab-target-cards').locator('[data-testid^="topology-tab-target-card-"][data-testid$="-role"]')
  await expect(page.getByTestId('topology-tab-placement-panel')).toBeVisible()
  const n = await cards.count()
  const out: Array<{ where: string; role: string; sub: string }> = []
  for (let i = 0; i < n; i++) {
    const card = page.getByTestId(`topology-tab-target-card-${i}`)
    const lines = await card.locator(':scope > div').allInnerTexts()
    out.push({ where: (lines[0] ?? '').trim(), role: (lines[1] ?? '').trim(), sub: (lines[2] ?? '').trim() })
  }
  return out
}

/** One item of GET /api/v1/catalog. */
export interface CatalogEntry {
  name: string
  version: string
  card?: { title?: string; summary?: string; tagline?: string; description?: string; iconLight?: string }
}

export async function catalogItems(page: Page): Promise<CatalogEntry[]> {
  const body = await apiJSON<{ items?: CatalogEntry[] }>(page, '/api/v1/catalog')
  expect(Array.isArray(body.items), 'GET /api/v1/catalog answers a list items').toBe(true)
  return body.items as CatalogEntry[]
}

/** The catalogue name of the Blueprint whose card title is the one given (Alloy, WordPress, PostgreSQL). */
export async function catalogNameOf(page: Page, title: string, ...names: string[]): Promise<string> {
  const items = await catalogItems(page)
  const hit = items.find((i) => names.includes(i.name)) ?? items.find((i) => (i.card?.title ?? '').toLowerCase() === title.toLowerCase())
  if (!hit) throw new Error(`the catalogue has no Blueprint ${title} (GET /api/v1/catalog)`)
  return hit.name
}

/** Opens a Blueprint's catalogue page and waits for its hero. */
export async function openCatalog(page: Page, name: string): Promise<void> {
  await page.goto(consoleURL(`/catalog/${encodeURIComponent(name)}`))
  await expect(page.getByTestId('catalog-hero')).toBeVisible()
}

/** The whole text of the YAML editor (CodeMirror renders only the lines in view): select all, copy, read. */
export async function editorYaml(page: Page): Promise<string> {
  await page.context().grantPermissions(['clipboard-read', 'clipboard-write'])
  const content = page.getByTestId('yaml-editor').locator('.cm-content')
  await expect(content).toBeVisible()
  await content.click()
  await page.keyboard.press('ControlOrMeta+a')
  await page.keyboard.press('ControlOrMeta+c')
  return page.evaluate(() => navigator.clipboard.readText())
}

/** Replaces the whole text of the YAML editor. */
export async function setEditorYaml(page: Page, yaml: string): Promise<void> {
  const content = page.getByTestId('yaml-editor').locator('.cm-content')
  await content.click()
  await page.keyboard.press('ControlOrMeta+a')
  await page.keyboard.insertText(yaml)
}

/** Runs a shell command in a container of a Pod. */
export function execIn(region: Region, namespace: string, pod: string, container: string, script: string, timeoutMs = 300_000): Promise<string> {
  return kube(region, ['exec', '-n', namespace, pod, '-c', container, '--', 'sh', '-c', script], timeoutMs)
}

/** Quotes a value for a POSIX shell. */
export function sq(s: string): string {
  return `'${s.replace(/'/g, `'"'"'`)}'`
}

/** Polls fn until it returns a value that is not null, or fails with what it last saw. */
export async function until<T>(what: string, fn: () => Promise<T | null>, timeoutMs: number, everyMs = 10_000): Promise<T> {
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
