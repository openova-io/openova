// Gate check FND-G21 (FND-163): the infrastructure the cloud pages read — Crossplane's observe-only
// adoption of what OpenTofu built, compared with the load balancers the topology of the console shows,
// and the storage classes the claims land on. Read from the clusters (regionKubectl, read-only). One test
// per ledger row; the title is the row id and its clause.
import { test, expect } from '../fixtures.ts'
import { REGIONS, arr, cond, field, kube, list, listCluster, loadBalancersOf, str, topology, type Obj } from './lib.ts'

const ADOPTIONS = 'cloudadoptions.compose.openova.io'

async function managed(): Promise<Obj[]> {
  const out = JSON.parse(await kube('a', ['get', 'managed', '-o', 'json'])) as { items?: Obj[] }
  return out.items ?? []
}

async function opentofuProvider(): Promise<Obj> {
  const p = (await listCluster('a', 'providers.pkg.crossplane.io')).find((x) => /opentofu/.test(x.metadata.name) || /opentofu/.test(str(x, 'spec.package')))
  expect(p, 'the Crossplane provider of OpenTofu').toBeTruthy()
  return p as Obj
}

const observeOnly = (o: Obj) => field(o, 'spec.parameters.manage') === false || JSON.stringify(field(o, 'spec.managementPolicies') ?? field(o, 'spec.parameters.managementPolicies') ?? []) === '["Observe"]'

/** The adoption of the primary load balancer, and its external name. */
async function elbAdoption(): Promise<Obj> {
  const all = await list('a', ADOPTIONS)
  const a = all.find((x) => /loadbalancer-primary/.test(x.metadata.name)) ?? all.find((x) => /loadbalancer|elb/.test(x.metadata.name))
  expect(a, 'the CloudAdoption of the load balancer').toBeTruthy()
  return a as Obj
}

test('G1 crossplane provider-opentofu Observe-only adoption populates on the fresh prov.', async () => {
  const p = await opentofuProvider()
  expect(cond(p, 'Installed'), 'provider-opentofu Installed').toBe('True')
  expect(cond(p, 'Healthy'), 'provider-opentofu Healthy').toBe('True')
  const ws = (await managed()).filter((m) => /opentofu/.test(m.apiVersion ?? '') && /^adopt-/.test(m.metadata.name))
  expect(ws.length, 'the adopt-* workspaces the observe-only adoption populated').toBeGreaterThan(0)
})

test('206 kubectl get managed is non-empty — Crossplane observes the OpenTofu-built infra.', async () => {
  const items = await managed()
  expect(items.length, 'kubectl get managed').toBeGreaterThan(0)
  expect(items.filter((m) => cond(m, 'Synced') !== 'True' || cond(m, 'Ready') !== 'True').map((m) => `${m.kind}/${m.metadata.name}`), 'managed resources not Synced and Ready').toEqual([])
})

test('207 A CloudAdoption for the real ELB reaches Synced+Ready (Observe), external-name = the real ELB id.', async ({ page }) => {
  const a = await elbAdoption()
  expect(cond(a, 'Synced'), 'Synced').toBe('True')
  expect(cond(a, 'Ready'), 'Ready').toBe('True')
  expect(observeOnly(a), 'Observe only').toBe(true)
  const ext = a.metadata.annotations?.['crossplane.io/external-name'] ?? ''
  expect(ext, 'its external-name').not.toBe('')
  const ids = loadBalancersOf(await topology(page)).map((l) => l.id)
  expect(ids, 'the external-name is the id of a load balancer the console shows').toContain(ext)
})

test('208 Adoption is Observe-only — the live ELB/nodes are untouched (no re-provision).', async ({ page }) => {
  const adoptions = await list('a', ADOPTIONS)
  expect(adoptions.length, 'the CloudAdoptions of the candidate').toBeGreaterThan(0)
  expect(adoptions.filter((a) => !observeOnly(a)).map((a) => a.metadata.name), 'adoptions that may manage (create, update, delete) the infrastructure').toEqual([])
  const ws = (await managed()).filter((m) => /^adopt-/.test(m.metadata.name))
  expect(ws.length, 'the adopt-* workspaces').toBeGreaterThan(0)
  for (const w of ws) {
    // A Workspace without managementPolicies takes Crossplane's default ["*"]: full management, not observe.
    const policies = arr(w, 'spec.managementPolicies')
    expect(JSON.stringify(policies), `${w.metadata.name}: observe only`).toBe('["Observe"]')
  }
  const a = await elbAdoption()
  const ids = loadBalancersOf(await topology(page)).map((l) => l.id)
  expect(ids, 'the adopted load balancer is still the live one (not re-provisioned)').toContain(a.metadata.annotations?.['crossplane.io/external-name'] ?? '')
  const oldest = Math.min(...adoptions.map((x) => Date.parse(x.metadata.creationTimestamp ?? '')))
  for (const r of REGIONS) {
    for (const n of await listCluster(r, 'nodes')) {
      expect(Date.parse(n.metadata.creationTimestamp ?? ''), `region ${r}: node ${n.metadata.name} predates the adoption (not re-created)`).toBeLessThan(oldest)
    }
  }
})

test('239 Crossplane adoption populates Observe-only on the fresh prov — providers.pkg.crossplane.io and cloudadoptions.compose.openova.io are non-empty and reach Synced+Ready, observing (NOT re-provisioning) the OpenTofu-built infra.', async ({ page }) => {
  const providers = await listCluster('a', 'providers.pkg.crossplane.io')
  expect(providers.filter((p) => cond(p, 'Healthy') === 'True').length, 'a Healthy Crossplane provider').toBeGreaterThan(0)
  const adoptions = await list('a', ADOPTIONS)
  expect(adoptions.length, 'kubectl get cloudadoptions.compose.openova.io -A').toBeGreaterThan(0)
  expect(adoptions.filter((a) => cond(a, 'Synced') !== 'True' || cond(a, 'Ready') !== 'True').map((a) => a.metadata.name), 'adoptions not Synced and Ready').toEqual([])
  expect(adoptions.filter((a) => !observeOnly(a)).map((a) => a.metadata.name), 'adoptions that are not observe-only').toEqual([])
  const a = await elbAdoption()
  expect(loadBalancersOf(await topology(page)).map((l) => l.id), 'external-name = the real ELB id').toContain(a.metadata.annotations?.['crossplane.io/external-name'] ?? '')
})

test('209 PVCs land on a real CSI storageclass (not k3s local-path).', async () => {
  for (const r of REGIONS) {
    const classes = new Map((await listCluster(r, 'storageclasses.storage.k8s.io')).map((s) => [s.metadata.name, str(s, 'provisioner')]))
    const pvcs = await list(r, 'persistentvolumeclaims')
    expect(pvcs.length, `region ${r}: PersistentVolumeClaims`).toBeGreaterThan(0)
    for (const p of pvcs) {
      const where = `region ${r}: ${p.metadata.namespace}/${p.metadata.name}`
      expect(str(p, 'status.phase'), `${where}: Bound`).toBe('Bound')
      const provisioner = classes.get(str(p, 'spec.storageClassName')) ?? ''
      expect(provisioner, `${where}: a provisioner of its class`).not.toBe('')
      expect(provisioner, `${where}: a CSI driver, not local-path`).not.toMatch(/local-path/)
      expect(provisioner, `${where}: a CSI driver`).toMatch(/csi/i)
    }
  }
})

test('210 local-path is FORBIDDEN (k3s --disable=local-storage).', async () => {
  for (const r of REGIONS) {
    const classes = await listCluster(r, 'storageclasses.storage.k8s.io')
    expect(classes.length, `region ${r}: StorageClasses`).toBeGreaterThan(0)
    expect(classes.filter((s) => /local-path/.test(s.metadata.name) || /local-path/.test(str(s, 'provisioner'))).map((s) => s.metadata.name), `region ${r}: local-path StorageClasses`).toEqual([])
    const deploys = await list(r, 'deployments.apps', 'kube-system')
    expect(deploys.filter((d) => /local-path-provisioner/.test(d.metadata.name)).map((d) => d.metadata.name), `region ${r}: the local-path provisioner`).toEqual([])
  }
})
