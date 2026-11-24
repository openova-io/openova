// Gate check FND-G21 (FND-163): the data plane the infrastructure pages report on — the shared
// PostgreSQL across the two regions, region b's Keycloak on it, the per-Organization PostgreSQL that the
// host's CloudNativePG operator runs, and the Continuum objects of the DR model. Read from the clusters
// (regionKubectl, read-only). One test per ledger row; the title is the row id and its clause.
import { test, expect } from '../fixtures.ts'
import { REGIONS, arr, cnpgClusters, cnpgHealthy, cond, execIn, field, helmReleases, kube, list, orgNamespaces, ready, str, vclusterNamespaces, type Obj } from './lib.ts'

/** A Running instance Pod of a CloudNativePG Cluster, the primary when asked for. */
async function instancePod(region: 'a' | 'b', c: Obj, primary: boolean): Promise<string> {
  const pods = (await list(region, 'pods', c.metadata.namespace)).filter((p) => p.metadata.labels?.['cnpg.io/cluster'] === c.metadata.name && str(p, 'status.phase') === 'Running')
  const want = primary ? pods.filter((p) => p.metadata.name === str(c, 'status.currentPrimary')) : pods
  expect(want.length, `region ${region}: a Running ${primary ? 'primary ' : ''}Pod of ${c.metadata.name}`).toBeGreaterThan(0)
  return want[0].metadata.name
}

test('R12 shared-pg -mesh-rw resolves in BOTH regions + CNPG streaming replication is live.', async () => {
  const primary = (await cnpgClusters('a')).find((c) => /^shared-pg/.test(c.metadata.name) && !/replica/.test(c.metadata.name))
  expect(primary, 'the shared PostgreSQL Cluster of region a').toBeTruthy()
  const ns = (primary as Obj).metadata.namespace ?? ''
  const mesh = (await list('a', 'services', ns)).find((s) => s.metadata.name.endsWith('-mesh-rw'))
  expect(mesh, `the -mesh-rw Service of ${ns}`).toBeTruthy()
  const host = `${(mesh as Obj).metadata.name}.${ns}.svc.cluster.local`
  const pA = await instancePod('a', primary as Obj, true)
  const replica = (await cnpgClusters('b')).find((c) => /^shared-pg/.test(c.metadata.name))
  expect(replica, 'the shared PostgreSQL Cluster of region b').toBeTruthy()
  const pB = await instancePod('b', replica as Obj, false)
  const nsB = (replica as Obj).metadata.namespace ?? ''
  for (const [region, pod, podNs] of [['a', pA, ns], ['b', pB, nsB]] as const) {
    const out = await execIn(region, podNs, pod, 'postgres', `getent hosts ${host}`)
    expect(out.trim(), `region ${region}: ${host} resolves`).toMatch(/^\d+\.\d+\.\d+\.\d+\s/)
  }
  const streaming = await execIn('a', ns, pA, 'postgres', "psql -tAc \"select count(*) from pg_stat_replication where state = 'streaming'\"")
  expect(Number(streaming.trim()), 'standbys streaming from the primary').toBeGreaterThan(0)
  const receiver = await execIn('b', nsB, pB, 'postgres', 'psql -tAc "select status from pg_stat_wal_receiver"')
  expect(receiver.trim(), 'region b receives the WAL stream').toBe('streaming')
})

test('236 On a fresh multi-region prov, region-B keycloak reaches Ready — the cross-region shared-pg write host is correct so region-B Keycloak connects to the primary Postgres and completes realm import.', async ({ request }) => {
  const hr = (await helmReleases('b')).find((h) => /^(bp-)?keycloak$/.test(h.metadata.name))
  expect(hr, 'the keycloak HelmRelease of region b').toBeTruthy()
  expect(ready(hr as Obj), 'region b: bp-keycloak Ready').toBe('True')
  const pods = (await list('b', 'pods', 'keycloak')).filter((p) => /^keycloak-\d+$/.test(p.metadata.name))
  expect(pods.length, 'region b: a keycloak Pod').toBeGreaterThan(0)
  const pod = pods[0]
  expect(str(pod, 'status.phase'), `region b: ${pod.metadata.name} Running`).toBe('Running')
  const env = arr(pod, 'spec.containers').flatMap((c) => arr(c, 'env'))
  const dbUrl = env.map((e) => (str(e, 'name') === 'KC_DB_URL' || str(e, 'name') === 'KC_DB_URL_HOST' ? str(e, 'value') : '')).find((v) => v !== '') ?? ''
  const dbTarget = dbUrl || (await kube('b', ['exec', '-n', 'keycloak', pod.metadata.name, '--', 'sh', '-c', 'printf %s "${KC_DB_URL:-$KC_DB_URL_HOST}"']))
  expect(dbTarget, 'region b: Keycloak writes to the cross-region primary (-mesh-rw)').toMatch(/-mesh-rw/)
  const status = await kube('b', ['exec', '-n', 'keycloak', pod.metadata.name, '--', 'bash', '-c', 'exec 3<>/dev/tcp/127.0.0.1/8080; printf "GET /realms/sovereign/.well-known/openid-configuration HTTP/1.0\\r\\nHost: localhost\\r\\n\\r\\n" >&3; head -n 1 <&3'])
  expect(status, 'region b: the realm sovereign is imported and served').toMatch(/ 200 /)
  const routes = await list('a', 'httproutes.gateway.networking.k8s.io', 'keycloak')
  const host = routes.flatMap((r) => arr(r, 'spec.hostnames').map(String))[0]
  expect(host, 'the keycloak host').toBeTruthy()
  const door = await request.get(`https://${host}/admin/`, { maxRedirects: 0, timeout: 30_000 })
  expect([301, 302, 303, 307, 308], `https://${host}/admin/ redirects into the sign-on`).toContain(door.status())
})

test('238 Per-Org postgres-in-vCluster → host-side CNPG Ready — a per-Org Postgres requested inside the Org\'s vCluster is reconciled by the host-side CNPG operator and reaches Ready.', async () => {
  const vc = new Set(await vclusterNamespaces('a'))
  const orgs = (await orgNamespaces('a')).map((n) => n.metadata.name).filter((n) => vc.has(n))
  expect(orgs.length, 'Organizations of the candidate that run a vCluster').toBeGreaterThan(0)
  const clusters = (await cnpgClusters('a')).filter((c) => orgs.includes(c.metadata.namespace ?? ''))
  expect(clusters.length, 'a per-Org PostgreSQL in a vCluster Organization, reconciled host-side').toBeGreaterThan(0)
  for (const c of clusters) {
    const where = `${c.metadata.namespace}/${c.metadata.name}`
    expect(cnpgHealthy(c), `${where}: healthy (${str(c, 'status.phase')})`).toBe(true)
    const pods = (await list('a', 'pods', c.metadata.namespace)).filter((p) => p.metadata.labels?.['cnpg.io/cluster'] === c.metadata.name)
    expect(pods.length, `${where}: its Pods`).toBeGreaterThan(0)
    for (const p of pods) {
      expect(str(p, 'status.phase'), `${p.metadata.name}: Running`).toBe('Running')
      expect(str(p, 'status.qosClass'), `${p.metadata.name}: Guaranteed QoS`).toBe('Guaranteed')
    }
    const hrs = (await helmReleases('a')).filter((h) => h.metadata.namespace === c.metadata.namespace)
    expect(hrs.length, `${c.metadata.namespace}: the per-Org releases`).toBeGreaterThan(0)
    expect(hrs.filter((h) => ready(h) !== 'True').map((h) => h.metadata.name), `${c.metadata.namespace}: releases not Ready (an app that cannot connect)`).toEqual([])
  }
})

test('G3 On a two-region Sovereign every continuums.dr.openova.io CR declares a standby and every cnpgPair-backed one proves it reachable: spec.hotStandbyRegions is non-empty on ALL of them, and StandbyAvailable=True on every Continuum that carries a spec.cnpgPair binding. VACUITY GUARD: ZERO Continuum CRs on the Sovereign is a FAIL, not a pass.', async () => {
  const all: Array<{ region: string; c: Obj }> = []
  for (const r of REGIONS) for (const c of await list(r, 'continuums.dr.openova.io')) all.push({ region: r, c })
  expect(all.length, 'Continuum CRs of the group dr.openova.io on the candidate').toBeGreaterThan(0)
  for (const { region, c } of all) {
    const where = `region ${region}: ${c.metadata.namespace}/${c.metadata.name}`
    expect(arr(c, 'spec.hotStandbyRegions').length, `${where}: spec.hotStandbyRegions`).toBeGreaterThan(0)
    if (field(c, 'spec.cnpgPair') !== undefined && field(c, 'spec.cnpgPair') !== null) {
      expect(cond(c, 'StandbyAvailable'), `${where}: StandbyAvailable`).toBe('True')
    }
  }
})
