// Gate check FND-G21 (FND-163): the convergence of the platform's own applications as the resource views
// show them — the API gateway's admin token synchronised from OpenBao and its one consumer, and the
// per-Organization controllers and releases reaching Ready. Read from the clusters (regionKubectl). One
// test per ledger row; the title is the row id and its clause.
import { test, expect } from '../fixtures.ts'
import { arr, cond, field, getObj, helmReleases, kube, list, num, orgNamespaces, ready, str, type Obj } from './lib.ts'

const ES = 'externalsecrets.external-secrets.io'
const PUSH = 'pushsecrets.external-secrets.io'

/** The chart a HelmRelease installs. */
const chartOf = (h: Obj) => str(h, 'spec.chart.spec.chart') || str(h, 'spec.chartRef.name') || str(h, 'status.lastAttemptedRevision')

test('M2 newapi admin-token seeded into OpenBao at provision (was unseeded → admin-promote fault).', async () => {
  const es = await getObj('a', ES, 'newapi', 'catalyst-newapi-admin-token')
  expect(es, 'the ExternalSecret newapi/catalyst-newapi-admin-token').not.toBeNull()
  expect(cond(es, 'Ready'), 'it synchronises from OpenBao (Ready)').toBe('True')
  expect(str(arr(es, 'status.conditions').find((c) => str(c, 'type') === 'Ready'), 'reason'), 'SecretSynced').toBe('SecretSynced')
  const secret = await getObj('a', 'secrets', 'newapi', str(es, 'spec.target.name') || 'catalyst-newapi-admin-token')
  expect(secret, 'the Secret it materialises').not.toBeNull()
  expect(Object.keys(secret?.data ?? {}).length, 'the Secret holds the token').toBeGreaterThan(0)
})

test('G2 newapi admin-token ES-sync is Sovereign-scoped by design: the ExternalSecret + PushSecret pair reconciles green in ns newapi and the produced Secret is reflector-mirrored into catalyst-system for its one consumer, and any per-Organization bp-newapi release renders catalystIntegration.enabled: false so it can never clobber the Sovereign-wide bearer (#5987, #6074).', async () => {
  const es = await getObj('a', ES, 'newapi', 'catalyst-newapi-admin-token')
  expect(cond(es, 'Ready'), 'the ExternalSecret is green').toBe('True')
  const pushes = await list('a', PUSH, 'newapi')
  expect(pushes.length, 'the PushSecret of the pair').toBeGreaterThan(0)
  for (const p of pushes) expect(cond(p, 'Ready'), `PushSecret ${p.metadata.name} is green`).toBe('True')
  const source = await getObj('a', 'secrets', 'newapi', 'catalyst-newapi-admin-token')
  expect(source?.metadata.annotations?.['reflector.v1.k8s.emberstack.com/reflection-allowed-namespaces'] ?? '', 'the produced Secret may be mirrored into catalyst-system').toMatch(/(^|,)\s*catalyst-system\s*(,|$)/)
  const mirror = await getObj('a', 'secrets', 'catalyst-system', 'catalyst-newapi-admin-token')
  expect(mirror?.metadata.annotations?.['reflector.v1.k8s.emberstack.com/reflects'] ?? '', 'catalyst-system holds the mirror').toBe('newapi/catalyst-newapi-admin-token')
  for (const h of (await helmReleases('a')).filter((x) => /newapi/.test(chartOf(x)) && x.metadata.namespace !== 'newapi' && x.metadata.namespace !== 'flux-system')) {
    expect(field(h, 'spec.values.catalystIntegration.enabled'), `${h.metadata.namespace}/${h.metadata.name}: catalystIntegration.enabled`).toBe(false)
  }
})

test('224 Per-Org bp-openclaw controller reaches Ready (1/1) on a Sovereign — the idle-reaper can list its per-user pods and readyz passes.', async () => {
  const orgs = new Set((await orgNamespaces('a')).map((n) => n.metadata.name))
  const ctrls = (await list('a', 'deployments.apps')).filter((d) => orgs.has(d.metadata.namespace ?? '') && /openclaw-controller$/.test(d.metadata.name))
  expect(ctrls.length, 'a per-Org bp-openclaw controller on the candidate').toBeGreaterThan(0)
  for (const d of ctrls) {
    const where = `${d.metadata.namespace}/${d.metadata.name}`
    expect(num(d, 'status.readyReplicas'), `${where}: Ready`).toBe(num(d, 'spec.replicas'))
    expect(num(d, 'spec.replicas'), `${where}: replicas`).toBeGreaterThan(0)
    const log = await kube('a', ['logs', '-n', d.metadata.namespace ?? '', `deploy/${d.metadata.name}`, '--since=1h'])
    expect(log, `${where}: the idle-reaper may list its pods`).not.toMatch(/pods is forbidden|cannot list resource "pods"/)
  }
})

test('225 Per-Org bp-newapi HR reaches Ready on a fresh Org — the admin-promote post-install hook Completes (its ServiceAccount exists) so the chart finishes.', async () => {
  const orgs = new Set((await orgNamespaces('a')).map((n) => n.metadata.name))
  const hrs = (await helmReleases('a')).filter((h) => orgs.has(h.metadata.namespace ?? '') && /newapi/.test(chartOf(h)))
  expect(hrs.length, 'a per-Org bp-newapi release on the candidate').toBeGreaterThan(0)
  for (const h of hrs) {
    const ns = h.metadata.namespace ?? ''
    expect(ready(h), `${ns}/${h.metadata.name}: Ready`).toBe('True')
    const hooks = (await list('a', 'jobs.batch', ns)).filter((j) => /admin-promote/.test(j.metadata.name))
    for (const j of hooks) {
      expect(cond(j, 'Complete'), `${ns}/${j.metadata.name}: the hook Completes`).toBe('True')
      const sa = str(j, 'spec.template.spec.serviceAccountName') || 'default'
      expect(await getObj('a', 'serviceaccounts', ns, sa), `${ns}: the hook's ServiceAccount ${sa}`).not.toBeNull()
    }
  }
})
