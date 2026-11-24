// Gate check FND-G21 (FND-163): the front doors of the candidate and the record that reports on them —
// the region-b kubeconfig the cut-over keeps, the split-horizon DNS of the tenant hosts, and, on the
// Sovereign that provisioned the candidate, its deployment record and its admission floor. One test per
// ledger row; the title is the row id and its clause. Rows 241 and 242 read the parent through the
// optional inputs GATE_PARENT_URL (the base its API paths hang off) and GATE_PARENT_SESSION (lib.ts).
import { promises as dns } from 'node:dns'
import { test, expect, consoleURL } from '../fixtures.ts'
import { RUN_TOKEN, arr, fqdnOf, getObj, isPrivateIPv4, list, listCluster, loadBalancersOf, parentContext, str, topology } from './lib.ts'

/** The record the parent keeps of the candidate (GET /api/v1/deployments, matched by its domain). */
interface DeploymentRecord {
  id: string
  status: string
  sovereignFQDN?: string
  startedAt?: string
  result?: { consoleDegraded?: boolean; consoleDegradedDetail?: string }
}

test('189 Region-b kubeconfig self-heals EIP→private-IP zero-touch on restart (topology survives a fresh prov).', async () => {
  const secret = await getObj('a', 'secrets', 'catalyst', 'cutover-secondary-kubeconfigs')
  expect(secret, 'the Secret catalyst/cutover-secondary-kubeconfigs').not.toBeNull()
  const keys = Object.keys(secret?.data ?? {}).filter((k) => k.endsWith('.yaml'))
  expect(keys.length, 'the kubeconfig of a further region').toBeGreaterThan(0)
  const cps = (await listCluster('b', 'nodes')).filter((n) => Object.keys(n.metadata.labels ?? {}).some((l) => /node-role\.kubernetes\.io\/(control-plane|master)/.test(l)))
  expect(cps.length, 'the control-plane nodes of region b').toBeGreaterThan(0)
  const internal = new Set(cps.flatMap((n) => arr(n, 'status.addresses').filter((a) => str(a, 'type') === 'InternalIP').map((a) => str(a, 'address'))))
  const servers = keys.map((k) => /server:\s*https:\/\/([0-9.]+):\d+/.exec(Buffer.from(secret?.data?.[k] ?? '', 'base64').toString('utf8'))?.[1] ?? '')
  expect(servers.filter((s) => s !== '').length, 'the server address of each further-region kubeconfig').toBe(keys.length)
  for (const s of servers) {
    expect(isPrivateIPv4(s), `the region-b kubeconfig dials ${s}, a private address, not the EIP`).toBe(true)
    expect([...internal], `${s} is the private address of a region-b control-plane node`).toContain(s)
  }
})

test('241 A ready deployment record\'s console health MATCHES the live front door: GET /sovereign/api/v1/deployments/{id} reports status: ready with consoleDegraded false/absent exactly while https://console.<fqdn>/ answers from the public internet, and consoleDegraded: true with a non-empty consoleDegradedDetail whenever it does not. FAILS in EITHER direction.', async ({ request }) => {
  const parent = await parentContext(request)
  const fqdn = fqdnOf()
  const listed = await parent.get('/api/v1/deployments')
  expect(listed.status(), `GET ${parent.url}/api/v1/deployments`).toBe(200)
  const records = (((await listed.json()) as { deployments?: DeploymentRecord[] }).deployments ?? []).filter((d) => d.sovereignFQDN === fqdn)
  expect(records.length, `the parent's record of ${fqdn}`).toBeGreaterThan(0)
  records.sort((x, y) => Date.parse(y.startedAt ?? '') - Date.parse(x.startedAt ?? ''))
  const one = await parent.get(`/api/v1/deployments/${encodeURIComponent(records[0].id)}`)
  expect(one.status(), `GET the record ${records[0].id}`).toBe(200)
  const rec = (await one.json()) as DeploymentRecord
  expect(rec.status, 'the record is ready').toBe('ready')
  const door = await request.get(consoleURL('/'), { maxRedirects: 0, timeout: 30_000, failOnStatusCode: false }).then((r) => r.status(), () => 0)
  const serving = door > 0 && door < 400
  if (serving) {
    expect(rec.result?.consoleDegraded ?? false, `the console answers ${door}: consoleDegraded false or absent`).toBe(false)
  } else {
    expect(rec.result?.consoleDegraded, `the console answers ${door}: the record surfaces it`).toBe(true)
    expect(rec.result?.consoleDegradedDetail ?? '', 'consoleDegradedDetail').not.toBe('')
  }
})

test('242 2-region BCP admission floor: a 1-region POST with no explicit bcpTopology is rejected at the LIVE production API.', async ({ request }) => {
  const parent = await parentContext(request)
  // One region, no bcpTopology, no provider and no credentials: should the floor not hold, the request
  // still cannot pass admission and nothing is provisioned.
  const body = { sovereignFQDN: `gate-bcp-floor-${RUN_TOKEN}.invalid`, regions: [{ cloudRegion: 'gate-floor' }] }
  const r = await parent.post('/api/v1/deployments', body)
  expect(r.status(), 'the 1-region request is rejected at admission').toBe(400)
  expect(await r.text(), 'the rejection is the BCP floor').toMatch(/no explicit bcpTopology/)
})

test('243 Tenant DNS split-horizon on a fresh prov: wildcard hosts → primary ELB EIP, console → console ELB EIP.', async ({ page }) => {
  const fqdn = fqdnOf()
  const t = await topology(page)
  const lbs = loadBalancersOf(t)
  expect(lbs.length, 'the load balancers of the topology').toBeGreaterThan(0)
  const byIp = new Map(lbs.map((l) => [l.publicIP, l]))
  const console4 = await dns.resolve4(`console.${fqdn}`)
  const wildcard4 = await dns.resolve4(`gate-${RUN_TOKEN}.${fqdn}`)
  const tenant = (await list('a', 'httproutes.gateway.networking.k8s.io')).flatMap((r) => arr(r, 'spec.hostnames').map(String)).find((h) => !h.startsWith('console.') && !h.startsWith('*') && h.endsWith(`.${fqdn}`))
  expect(tenant, 'a tenant host of the candidate').toBeTruthy()
  const tenant4 = await dns.resolve4(tenant as string)
  expect(console4.length && wildcard4.length, 'console and wildcard hosts resolve').toBeTruthy()
  expect(console4.filter((ip) => wildcard4.includes(ip)), 'the console and the wildcard hosts answer on different addresses').toEqual([])
  expect([...tenant4].sort(), `${tenant} follows the wildcard`).toEqual([...wildcard4].sort())
  for (const ip of [...console4, ...wildcard4]) expect(byIp.has(ip), `${ip} is the address of a load balancer of the candidate`).toBe(true)
  const first = t.regions[0]
  const primary = new Set([first?.providerRegion, first?.id, first?.name].filter((x) => !!x))
  for (const ip of wildcard4) expect(primary.has(byIp.get(ip)?.region ?? ''), `${ip}: the load balancer of the primary region`).toBe(true)
})
