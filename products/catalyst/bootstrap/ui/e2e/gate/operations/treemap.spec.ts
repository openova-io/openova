// Gate check FND-G21 (FND-163): the Dashboard treemap (/dashboard) and its feed, GET
// /api/v1/dashboard/treemap, which the page asks for with the layer stack it shows. One test per ledger
// row; the title is the row id and its clause. A block's content is compared with the namespaces,
// Organizations and vClusters of the clusters (regionKubectl). Row 108 changes the declared placement of
// one Application for a moment and puts it back at once.
import { test, expect, consoleURL, type Region } from '../fixtures.ts'
import type { Page } from '@playwright/test'
import { REGIONS, apiJSON, cells, field, kube, list, listCluster, orgOf, ready, selfId, str, treemap, vclusterNamespaces, type Cell, type Obj } from './lib.ts'

const surface = (page: Page) => page.getByTestId('dashboard-treemap-surface')

async function openDashboard(page: Page): Promise<void> {
  await page.goto(consoleURL('/dashboard'))
  await expect(page.getByTestId('dashboard-page')).toBeVisible({ timeout: 60_000 })
  await expect(surface(page).locator('svg rect').first(), 'the treemap draws cells').toBeVisible({ timeout: 90_000 })
}

/** The namespaces of both regions, by name. */
async function namespaces(): Promise<Map<string, Obj>> {
  const out = new Map<string, Obj>()
  for (const r of REGIONS) for (const n of await listCluster(r, 'namespaces')) out.set(n.metadata.name, n)
  return out
}

/** The per-Organization vClusters of the candidate, as the block name the treemap gives each (the Organization). */
async function vclusterBlocks(): Promise<Map<string, string>> {
  const ns = await namespaces()
  const out = new Map<string, string>()
  for (const r of REGIONS) {
    for (const n of await vclusterNamespaces(r)) {
      const o = ns.get(n)
      out.set(n, (o && orgOf(o)) || n)
    }
  }
  return out
}

test('17 Dashboard treemap is a meaningful drill-down surface (Organization → vCluster → Application selectable & drillable, NOT a raw infra-pod utilisation treemap); default layer stack per founder verdict', async ({ page }) => {
  await openDashboard(page)
  const layer0 = page.getByTestId('treemap-layer-0-select')
  await expect(layer0, 'the default first layer is a grouping, not a pod list').toHaveValue(/^(organization|namespace|vcluster|application|progress)$/)
  const options = await layer0.locator('option').evaluateAll((els) => els.map((e) => (e as HTMLOptionElement).value))
  expect(options, 'Organization, vCluster and Application are selectable').toEqual(expect.arrayContaining(['organization', 'vcluster', 'application']))
  expect(options.filter((o) => /pod/i.test(o)), 'no raw pod layer').toEqual([])
  await layer0.selectOption('organization')
  const items = await treemap(page, ['organization', 'vcluster', 'application'])
  const parent = items.find((c) => (c.children ?? []).length > 0)
  expect(parent, 'an Organization block that holds vCluster blocks').toBeTruthy()
  await surface(page).locator('svg text', { hasText: (parent as Cell).name }).first().click()
  await expect(page.getByTestId('dashboard-breadcrumb-0'), 'the click drilled into the block').toHaveText((parent as Cell).name, { timeout: 30_000 })
})

test('18 Scan every treemap cell: NO ephemeral Job-pod cell appears (no cutover-*, scan-vulnerabilityr… *-snapshot-save-*)', async ({ page }) => {
  const all = cells(await treemap(page, ['namespace', 'application']))
  expect(all.length, 'cells of the default resource stack').toBeGreaterThan(1)
  const ephemeral = all.map((c) => c.name).filter((n) => /^cutover-|^scan-vulnerabilityreport|-snapshot-save-/.test(n) || /-(?=[a-z0-9]*[0-9])[a-z0-9]{8,10}-[a-z0-9]{5}$/.test(n) || /-\d{8}-[a-z0-9]{5}$/.test(n))
  expect(ephemeral, 'cells of ephemeral Job pods or pod-name shapes').toEqual([])
})

test('20 With a customer Org present, the customer estate is visually distinct from platform pods on the treemap', async ({ page }) => {
  const ns = await namespaces()
  const orgs = new Set([...ns.values()].map(orgOf).filter((o) => o !== ''))
  expect(orgs.size, 'a customer Organization on the candidate').toBeGreaterThan(0)
  const items = await treemap(page, ['organization', 'namespace'])
  const platform = items.find((c) => c.name === 'Platform overhead')
  expect(platform, 'the platform block').toBeTruthy()
  const customer = items.filter((c) => orgs.has(c.name))
  expect(customer.map((c) => c.name), 'a block of its own for a customer Organization').not.toEqual([])
  for (const c of customer) {
    for (const child of c.children ?? []) expect(orgOf(ns.get(child.name) ?? { metadata: { name: '' } }), `${c.name}: namespace ${child.name} is the Organization's`).toBe(c.name)
  }
  const leaked = (platform?.children ?? []).map((c) => c.name).filter((n) => orgs.has(orgOf(ns.get(n) ?? { metadata: { name: '' } })))
  expect(leaked, 'customer namespaces in the platform block').toEqual([])
  await openDashboard(page)
  await page.getByTestId('treemap-layer-0-select').selectOption('organization')
  await expect(surface(page).locator('svg text', { hasText: customer[0].name }).first(), 'the customer block is drawn and labelled').toBeVisible({ timeout: 60_000 })
})

test('97 Dashboard renders the cluster treemap and the LAYER 1 / LAYER 2 grouping comboboxes are visible', async ({ page }) => {
  await openDashboard(page)
  await expect(page.getByTestId('treemap-layer-0-select'), 'the Layer 1 combobox').toBeVisible()
  await expect(page.getByTestId('treemap-layer-1-select'), 'the Layer 2 combobox').toBeVisible()
  await expect(page.getByText('Layer 1', { exact: true })).toBeVisible()
  await expect(page.getByText('Layer 2', { exact: true })).toBeVisible()
  expect(await surface(page).locator('svg rect').count(), 'cells of the treemap').toBeGreaterThan(1)
})

test('98 LAYER 1 → vCluster renders a host block plus one block per per-Org vCluster (uatco, walk-stranger-two), each clickable. VACUITY GUARD: a lone host block is a FAIL, not an empty environment.', async ({ page }) => {
  const want = [...new Set((await vclusterBlocks()).values())].sort()
  expect(want.length, 'per-Organization vClusters on the candidate').toBeGreaterThan(0)
  const top = (await treemap(page, ['vcluster', 'application'])).map((c) => c.name)
  expect(top, 'a host block').toContain('host')
  expect(top.filter((n) => n !== 'host').sort(), 'one block per per-Org vCluster, not a lone host block').toEqual(want)
  await openDashboard(page)
  await page.getByTestId('treemap-layer-0-select').selectOption('vcluster')
  for (const name of want) {
    await surface(page).locator('svg text', { hasText: name }).first().click()
    await expect(page.getByTestId('dashboard-breadcrumb-0'), `the block of ${name} is clickable`).toHaveText(name, { timeout: 30_000 })
    await page.getByTestId('dashboard-breadcrumb-root').click()
  }
})

test('102 LAYER1=vCluster treemap: every per-Org vCluster renders as its own labelled block, one block per vCluster.', async ({ page }) => {
  const blocks = await vclusterBlocks()
  expect(blocks.size, 'per-Organization vClusters on the candidate').toBeGreaterThan(0)
  const top = (await treemap(page, ['vcluster', 'application'])).map((c) => c.name)
  for (const name of new Set(blocks.values())) expect(top.filter((n) => n === name), `exactly one block of ${name}`).toHaveLength(1)
  await openDashboard(page)
  await page.getByTestId('treemap-layer-0-select').selectOption('vcluster')
  for (const name of new Set(blocks.values())) {
    await expect(surface(page).locator('svg text', { hasText: name }).first(), `the label of ${name}`).toBeVisible({ timeout: 60_000 })
  }
})

test("103 A per-Org vCluster block contains ONLY that Organization's workloads — no cross-Org leakage into another Org's block", async ({ page }) => {
  const ns = await namespaces()
  const blocks = await vclusterBlocks()
  expect(blocks.size, 'per-Organization vClusters on the candidate').toBeGreaterThan(0)
  const owner = new Map([...blocks].map(([n, org]) => [org, n]))
  const items = await treemap(page, ['vcluster', 'namespace'])
  for (const b of items.filter((c) => c.name !== 'host')) {
    expect(owner.has(b.name), `the block ${b.name} is a per-Org vCluster`).toBe(true)
    const children = b.children ?? []
    expect(children.length, `${b.name}: workloads`).toBeGreaterThan(0)
    for (const c of children) {
      const o = ns.get(c.name)
      const org = o ? orgOf(o) : ''
      expect(org === b.name || c.name === owner.get(b.name), `${b.name}: namespace ${c.name} belongs to it`).toBe(true)
    }
  }
})

test('104 The seven bootstrap components (grafana/harbor/keycloak/gitea/openbao/newapi/guacamole) render under host — the post-#4325 canonical placement.', async ({ page }) => {
  const items = await treemap(page, ['vcluster', 'application'])
  const host = items.find((c) => c.name === 'host')
  expect(host, 'the host block').toBeTruthy()
  const under = (c: Cell | undefined, n: string) => (c?.children ?? []).some((x) => x.name === n || x.name === `bp-${n}` || x.name.startsWith(`${n}-`) || x.name.startsWith(`bp-${n}-`))
  for (const n of ['grafana', 'harbor', 'keycloak', 'gitea', 'openbao', 'newapi', 'guacamole']) {
    expect(under(host, n), `${n} under host`).toBe(true)
    for (const other of items.filter((c) => c.name !== 'host')) expect(under(other, n), `${n} not under ${other.name}`).toBe(false)
  }
})

test("108 Placement is read from RUNTIME (the observed pod/namespace), not from the Application CR's declared field — a declared value that disagrees with reality must lose.", async ({ page }) => {
  const apps = (await list('a', 'applications.apps.openova.io')).filter((a) => ready(a) === 'True' && str(a, 'spec.organizationRef') !== '')
  const modeOf = (a: Obj) => (typeof field(a, 'spec.placement') === 'string' ? str(a, 'spec.placement') : str(a, 'spec.placement.mode'))
  const app = apps.find((a) => modeOf(a) === 'singleton' && str(a, 'status.placement.mode') === 'singleton')
  expect(app, 'a Ready per-Org singleton Application whose runtime placement is singleton').toBeTruthy()
  const a = app as Obj
  const appNS = a.metadata.namespace ?? ''
  // The observed placement: the Pods of the release in both regions, in the Application's own namespaces (the
  // label the treemap names an application block by), and the vCluster block their namespace belongs to.
  const release = str(a, 'spec.releaseName') || str(a, 'spec.helmRelease.name') || a.metadata.name
  const homes = new Set([appNS, str(a, 'spec.targetNamespace')].filter((n) => n !== ''))
  const pods = new Map<Region, Obj[]>()
  for (const r of REGIONS) pods.set(r, (await list(r, 'pods')).filter((p) => homes.has(p.metadata.namespace ?? '') && p.metadata.labels?.['app.kubernetes.io/instance'] === release))
  const observed = REGIONS.filter((r) => (pods.get(r) ?? []).length > 0)
  expect(observed, `the Pods of ${release} run in one region, as the runtime placement singleton says`).toHaveLength(1)
  const blocks = await vclusterBlocks()
  const want = [...new Set((pods.get(observed[0]) ?? []).map((p) => `${blocks.get(p.metadata.namespace ?? '') ?? 'host'}/${p.metadata.namespace ?? ''}`))].sort()
  // The declared value is set for one read to one that disagrees with the Pods (active-passive, two regions).
  // The two console reads of placement are taken in the same moment and the declared value is put back at once:
  // the treemap, built from the Pods, and the runtime placement of the Topology tab, which also reads the
  // declared mode (GET /api/v1/sovereigns/{id}/applications/{name}/placement).
  const id = await selfId(page)
  const placementPath = `/api/v1/sovereigns/${encodeURIComponent(id)}/applications/${encodeURIComponent(a.metadata.name)}/placement?namespace=${encodeURIComponent(appNS)}`
  const shape = typeof field(a, 'spec.placement') === 'string' ? '"active-passive"' : '{"mode":"active-passive"}'
  const back = typeof field(a, 'spec.placement') === 'string' ? '"singleton"' : '{"mode":"singleton"}'
  const where = ['-n', appNS, a.metadata.name]
  let tree: Cell[] = []
  let placed: { targets?: Array<{ region?: string; role?: string }>; derivedFromRuntime?: boolean } = {}
  await kube('a', ['patch', 'applications.apps.openova.io', ...where, '--type=merge', '-p', `{"spec":{"placement":${shape}}}`])
  try {
    ;[tree, placed] = await Promise.all([treemap(page, ['region', 'vcluster', 'namespace', 'application']), apiJSON<typeof placed>(page, placementPath)])
  } finally {
    await kube('a', ['patch', 'applications.apps.openova.io', ...where, '--type=merge', '-p', `{"spec":{"placement":${back}}}`])
  }
  const restored = JSON.parse(await kube('a', ['get', 'applications.apps.openova.io', ...where, '-o', 'json'])) as Obj
  expect(modeOf(restored), 'the declared placement is put back').toBe('singleton')
  // The treemap: the release's block sits under the region and the vCluster and namespace of its Pods, and
  // under no second region as the declared value would have it.
  const shown: string[] = []
  for (const region of tree) {
    for (const block of region.children ?? []) {
      for (const ns of block.children ?? []) {
        if (homes.has(ns.name) && (ns.children ?? []).some((c) => c.name === release)) shown.push(`${region.name}|${block.name}/${ns.name}`)
      }
    }
  }
  expect(new Set(shown.map((s) => s.split('|')[0])).size, `the regions the treemap places ${release} in: ${shown.join(', ')}`).toBe(observed.length)
  expect([...new Set(shown.map((s) => s.split('|')[1]))].sort(), `the vCluster and namespace blocks of ${release}`).toEqual(want)
  // The runtime placement: observed from the Pods, one Primary in the one region, not the declared pair.
  expect(placed.derivedFromRuntime, `GET ${placementPath}: derived from the runtime`).toBe(true)
  expect((placed.targets ?? []).map((t) => t.role ?? ''), `GET ${placementPath}: the targets`).toEqual(observed.map(() => 'Primary'))
})
