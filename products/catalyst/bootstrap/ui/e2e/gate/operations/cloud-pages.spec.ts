// Gate check FND-G21 (FND-163): the read pages of the cloud view (/cloud?view=list&kind=<kind>). One test
// per ledger row; the title is the row id and its clause. Each page is compared with what it claims to
// show: the per-kind lists with the objects of the clusters (regionKubectl), the compute and network pages
// with the topology they render (GET /api/v1/deployments/{id}/infrastructure/topology) and the nodes and
// front doors of the candidate.
import { promises as dns } from 'node:dns'
import { test, expect } from '../fixtures.ts'
import { arr, expectListMatches, fqdnOf, list, listCluster, loadBalancersOf, openCloudList, topology } from './lib.ts'

/** The counts of one kind in the two regions. */
async function counts(kind: string): Promise<[number, number]> {
  return [(await list('a', kind)).length, (await list('b', kind)).length]
}

test('65 Cloud/regions view shows the true region count — a healthy 2-region prov reads Cluster 2/2 with no phantom region-B bubble', async ({ page }) => {
  const t = await topology(page)
  const nodes = [(await listCluster('a', 'nodes')).length, (await listCluster('b', 'nodes')).length]
  expect(nodes.filter((n) => n > 0).length, 'the candidate runs two regions').toBe(2)
  expect(t.regions.length, `the regions of the topology: ${t.regions.map((r) => r.providerRegion).join(', ')}`).toBe(2)
  expect(new Set(t.regions.map((r) => r.providerRegion)).size, 'two distinct regions, no phantom duplicate').toBe(2)
  for (const r of t.regions) expect(r.clusters.length, `region ${r.providerRegion}: one cluster`).toBe(1)
  await openCloudList(page, 'clusters')
  await expect(page.getByTestId('cloud-clusters-count'), 'the Clusters page reads 2').toHaveText('2', { timeout: 60_000 })
  const healthy = page.locator('[data-testid^="cloud-clusters-row-"] span.cloud-list-status[data-status="healthy"]')
  await expect(healthy, 'both clusters healthy: 2/2').toHaveCount(2, { timeout: 60_000 })
})

test('66 Cloud→Clusters renders 2/2 HEALTHY clusters, one per region (me-east-215-a + me-east-215-b), no phantom region', async ({ page }) => {
  const t = await topology(page)
  const want = [(await listCluster('a', 'nodes')).length, (await listCluster('b', 'nodes')).length].sort()
  await openCloudList(page, 'clusters')
  const rows = page.locator('[data-testid^="cloud-clusters-row-"]')
  await expect(rows, 'two cluster rows').toHaveCount(2, { timeout: 60_000 })
  const cells = await rows.evaluateAll((els) => els.map((e) => Array.from(e.querySelectorAll('td'), (td) => (td.textContent ?? '').trim())))
  const regions = cells.map((c) => c[1])
  expect(new Set(regions).size, `one cluster per region: ${regions.join(', ')}`).toBe(2)
  expect([...regions].sort(), 'the regions of the topology').toEqual(t.regions.map((r) => r.providerRegion).sort())
  expect(cells.map((c) => Number(c[5])).sort(), 'each row counts the nodes of one region').toEqual(want)
  await expect(rows.locator('span.cloud-list-status[data-status="healthy"]'), 'both HEALTHY').toHaveCount(2)
})

test('198 /cloud per-kind helmreleases page shows the real count (~65), not 0', async ({ page }) => {
  const [a, b] = await counts('helmreleases.helm.toolkit.fluxcd.io')
  await openCloudList(page, 'helmreleases')
  await expectListMatches(page, 'helmrelease', a, b)
})

test('199 Cloud Gateway page shows the live cilium Gateways (2: cilium-gateway + cilium-gateway-console)', async ({ page }) => {
  const gwA = await list('a', 'gateways.gateway.networking.k8s.io')
  const gwB = await list('b', 'gateways.gateway.networking.k8s.io')
  expect(gwA.map((g) => g.metadata.name), 'the Gateways of region a').toEqual(expect.arrayContaining(['cilium-gateway', 'cilium-gateway-console']))
  await openCloudList(page, 'gateways')
  await expectListMatches(page, 'gateway', gwA.length, gwB.length)
  for (const n of ['cilium-gateway', 'cilium-gateway-console']) {
    await expect(page.locator(`[data-testid="cloud-gateway-row-${n}"]`).first(), `the row of ${n}`).toBeVisible()
  }
})

test('200 Cloud HTTPRoutes page shows 15', async ({ page }) => {
  // The ledger re-baselined the literal 15 on 2026-07-30: the clause is "renders the live routes, non-zero and real".
  const [a, b] = await counts('httproutes.gateway.networking.k8s.io')
  await openCloudList(page, 'httproutes')
  await expectListMatches(page, 'httproute', a, b)
})

test('201 Cloud NetworkPolicies page shows the live policies (30 live; expected ~10 stale)', async ({ page }) => {
  const [a, b] = await counts('networkpolicies.networking.k8s.io')
  await openCloudList(page, 'networkpolicies')
  await expectListMatches(page, 'networkpolicy', a, b)
})

test('202 Cloud CiliumNetworkPolicies page shows the live policies (42 live; expected 5 stale)', async ({ page }) => {
  const [a, b] = await counts('ciliumnetworkpolicies.cilium.io')
  await openCloudList(page, 'ciliumnetworkpolicies')
  await expectListMatches(page, 'ciliumnetworkpolicy', a, b)
})

test('203 Cloud Load Balancers page shows the real LoadBalancer Svc (clustermesh-apiserver EXTERNAL-IP 212.72.24.31), not 0', async ({ page }) => {
  // The ledger re-baselined this clause on 2026-07-30 (#3998): the page renders the front door; score "the real front door, not 0".
  const lbs = loadBalancersOf(await topology(page))
  expect(lbs.length, 'the load balancers of the topology').toBeGreaterThan(0)
  // The front doors: the console host and the hosts the routes of region a serve.
  const hosts = new Set([`console.${fqdnOf()}`])
  for (const r of await list('a', 'httproutes.gateway.networking.k8s.io')) {
    for (const h of arr(r, 'spec.hostnames')) if (typeof h === 'string' && !h.startsWith('*') && hosts.size < 4) hosts.add(h)
  }
  const doors = new Set<string>()
  for (const h of hosts) for (const ip of await dns.resolve4(h)) doors.add(ip)
  expect(lbs.filter((l) => doors.has(l.publicIP)).map((l) => l.name), `a load balancer that is the front door (${[...doors].join(', ')})`).not.toEqual([])
  for (const l of lbs) {
    for (const p of l.listeners ?? []) expect(p.port < 30000 || p.port > 32767, `${l.name}: listener ${p.port} is not a node port`).toBe(true)
  }
  await openCloudList(page, 'load-balancers')
  await expect(page.locator('[data-testid^="cloud-load-balancers-row-"]'), 'one row per load balancer').toHaveCount(lbs.length, { timeout: 60_000 })
  for (const l of lbs) await expect(page.getByTestId(`cloud-load-balancers-row-${l.id}`)).toContainText(l.name)
})

test('204 Cloud Worker Nodes page shows the real nodes, not 0', async ({ page }) => {
  const names = [...(await listCluster('a', 'nodes')), ...(await listCluster('b', 'nodes'))].map((n) => n.metadata.name).sort()
  expect(names.length, 'the nodes of the two regions').toBeGreaterThan(0)
  await openCloudList(page, 'worker-nodes')
  await expect(page.getByTestId('cloud-worker-nodes-count'), 'the page counts every node').toHaveText(String(names.length), { timeout: 60_000 })
  const shown = await page.locator('[data-testid^="cloud-worker-nodes-row-"] td:first-child').allInnerTexts()
  expect(shown.map((s) => s.trim()).sort(), 'the rows are the nodes of the clusters').toEqual(names)
})
