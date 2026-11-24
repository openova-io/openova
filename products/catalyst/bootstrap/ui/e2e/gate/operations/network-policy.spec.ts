// Gate check FND-G21 (FND-163): the network policies of the candidate as the cloud view's policy pages
// list them, read from the clusters (regionKubectl), and the traffic they must admit, proven by the
// object that uses it (a healthy CloudNativePG Cluster, a Ready GitRepository, an answering front door).
// One test per ledger row; the title is the row id and its clause.
import { test, expect, consoleURL } from '../fixtures.ts'
import { REGIONS, arr, cnpgClusters, cnpgHealthy, cond, field, helmReleases, list, orgNamespaces, ready, str, vclusterNamespaces, type Obj } from './lib.ts'

/** The namespaces an ingress rule of a NetworkPolicy admits ('*' for any) and its ports ('*' for any). */
function npIngress(np: Obj): Array<{ from: string[]; ports: string[] }> {
  return arr(np, 'spec.ingress').map((rule) => {
    const peers = arr(rule, 'from')
    const from = peers.length === 0 ? ['*'] : peers.flatMap((p) => {
      const sel = field(p, 'namespaceSelector')
      if (sel === undefined) return field(p, 'ipBlock') ? [] : [np.metadata.namespace ?? '']
      const labels = (field(sel, 'matchLabels') ?? {}) as Record<string, string>
      const exact = labels['kubernetes.io/metadata.name']
      if (exact) return [exact]
      return Object.keys(labels).length === 0 && arr(sel, 'matchExpressions').length === 0 ? ['*'] : [`selector:${JSON.stringify(sel)}`]
    })
    const ports = arr(rule, 'ports').map((p) => String(field(p, 'port') ?? '*'))
    return { from, ports: ports.length === 0 ? ['*'] : ports }
  })
}

function admits(nps: Obj[], ns: string, port: string): boolean {
  return nps.some((np) => npIngress(np).some((r) => (r.from.includes('*') || r.from.includes(ns)) && (r.ports.includes('*') || r.ports.includes(port))))
}

/** The namespaces a CiliumNetworkPolicy's ingress admits from ('*' for the cluster or the world). */
function cnpIngressFrom(cnp: Obj): string[] {
  const specs = [field(cnp, 'spec'), ...arr(cnp, 'specs')].filter((s) => s && typeof s === 'object')
  return specs.flatMap((s) => arr(s, 'ingress').flatMap((rule) => {
    const out: string[] = []
    for (const e of arr(rule, 'fromEntities')) if (e === 'cluster' || e === 'all' || e === 'world') out.push('*')
    for (const ep of arr(rule, 'fromEndpoints')) {
      const labels = (field(ep, 'matchLabels') ?? {}) as Record<string, string>
      const ns = labels['k8s:io.kubernetes.pod.namespace'] ?? labels['io.kubernetes.pod.namespace']
      if (ns) out.push(ns)
      else if (Object.keys(labels).length === 0 && arr(ep, 'matchExpressions').length === 0) out.push(cnp.metadata.namespace ?? '')
    }
    return out
  }))
}

const valid = (cnp: Obj) => cond(cnp, 'Valid') !== 'False'

test('R6 bp-postgres NetworkPolicy admits the declared consumers (CNPG operator probe + app dials).', async () => {
  const clusters = (await cnpgClusters('a')).filter((c) => /^shared-pg/.test(c.metadata.name))
  expect(clusters.length, 'the shared PostgreSQL Clusters of region a').toBeGreaterThan(0)
  for (const c of clusters) {
    const ns = c.metadata.namespace ?? ''
    const nps = await list('a', 'networkpolicies.networking.k8s.io', ns)
    expect(nps.length, `${ns}: the NetworkPolicies of bp-postgres`).toBeGreaterThan(0)
    expect(admits(nps, 'cnpg-system', '8000'), `${ns}: the operator status probe :8000 from cnpg-system`).toBe(true)
    expect(admits(nps, 'cnpg-system', '9187'), `${ns}: the operator metrics :9187 from cnpg-system`).toBe(true)
    expect(nps.some((np) => npIngress(np).some((r) => r.ports.includes('5432') && r.from.some((f) => f !== ns))), `${ns}: application dials :5432 from other namespaces`).toBe(true)
    expect(cnpgHealthy(c), `${ns}/${c.metadata.name}: healthy, so the operator probes and the replicas reach it`).toBe(true)
  }
})

test('R7 bp-plane-isolation admits gitea ingress for its declared consumers (org-services→gitea de-vcluster gap).', async () => {
  const hr = (await helmReleases('a')).find((h) => h.metadata.name === 'bp-plane-isolation')
  expect(hr && ready(hr), 'bp-plane-isolation is installed and Ready').toBe('True')
  const repos = (await list('a', 'gitrepositories.source.toolkit.fluxcd.io')).filter((g) => /gitea[^/]*\.svc/.test(str(g, 'spec.url')))
  expect(repos.length, 'GitRepositories that pull from the in-cluster gitea').toBeGreaterThan(0)
  for (const g of repos) expect(ready(g), `${g.metadata.namespace}/${g.metadata.name} pulls from gitea`).toBe('True')
  const consumers = new Set([...repos.map((g) => g.metadata.namespace ?? ''), ...(await orgNamespaces('a')).map((n) => n.metadata.name)])
  const cnps = await list('a', 'ciliumnetworkpolicies.cilium.io', 'gitea')
  expect(cnps.length, 'the CiliumNetworkPolicies of namespace gitea').toBeGreaterThan(0)
  const from = new Set(cnps.flatMap(cnpIngressFrom))
  const denied = [...consumers].filter((c) => !from.has('*') && !from.has(c))
  expect(denied, 'declared consumers of gitea that no ingress rule of namespace gitea admits').toEqual([])
})

test('R22 bp-plane-isolation admits apiserver-egress + gateway-ingress CNPs — CNPG admission + gateway hop succeed.', async () => {
  for (const r of REGIONS) {
    const hr = (await helmReleases(r)).find((h) => h.metadata.name === 'bp-plane-isolation')
    expect(hr && ready(hr), `region ${r}: bp-plane-isolation Ready`).toBe('True')
    const cnps = await list(r, 'ciliumnetworkpolicies.cilium.io')
    const egress = cnps.filter((c) => c.metadata.name.endsWith('-allow-apiserver-egress'))
    const ingress = cnps.filter((c) => c.metadata.name.endsWith('-allow-gateway-ingress'))
    expect(egress.length, `region ${r}: the apiserver-egress policies`).toBeGreaterThan(0)
    expect(ingress.length, `region ${r}: the gateway-ingress policies`).toBeGreaterThan(0)
    expect([...egress, ...ingress].filter((c) => !valid(c)).map((c) => `${c.metadata.namespace}/${c.metadata.name}`), `region ${r}: policies that are not valid`).toEqual([])
    const clusters = await cnpgClusters(r)
    expect(clusters.length, `region ${r}: CloudNativePG Clusters`).toBeGreaterThan(0)
    expect(clusters.filter((c) => !cnpgHealthy(c)).map((c) => `${c.metadata.namespace}/${c.metadata.name}`), `region ${r}: Clusters the operator cannot admit and reconcile`).toEqual([])
  }
  const routes = await list('a', 'httproutes.gateway.networking.k8s.io', 'keycloak')
  expect(routes.length, 'the keycloak HTTPRoute').toBeGreaterThan(0)
  for (const route of routes) {
    for (const p of arr(route, 'status.parents')) {
      expect(cond({ status: p }, 'Accepted'), `${route.metadata.name}: Accepted`).toBe('True')
      expect(cond({ status: p }, 'ResolvedRefs'), `${route.metadata.name}: ResolvedRefs`).toBe('True')
    }
    expect(arr(route, 'status.parents').length, `${route.metadata.name}: attached to a Gateway`).toBeGreaterThan(0)
  }
})

test('M3 vcluster-tier CNP applied host-side — the per-Org vcluster tier carries its split CNP.', async () => {
  let seen = 0
  for (const r of REGIONS) {
    for (const ns of await vclusterNamespaces(r)) {
      seen++
      const cnps = await list(r, 'ciliumnetworkpolicies.cilium.io', ns)
      const split = cnps.find((c) => c.metadata.name === 'allow-gateway-and-apiserver')
      expect(split, `region ${r}, ${ns}: the host-side split CNP allow-gateway-and-apiserver`).toBeTruthy()
      expect(valid(split as Obj), `region ${r}, ${ns}: the split CNP is valid`).toBe(true)
    }
  }
  expect(seen, 'per-Organization vClusters on the candidate').toBeGreaterThan(0)
})

test('229 bootstrap-kit Kustomization Ready=True + plane-isolation admits CNPG→kube-apiserver egress — on the fresh prov kubectl -n flux-system get kustomization bootstrap-kit is Ready=True, AND the plane-isolation NetworkPolicy permits the CNPG operator\'s egress to the kube-apiserver so Cluster admission/patch succeeds.', async () => {
  for (const r of REGIONS) {
    const ks = (await list(r, 'kustomizations.kustomize.toolkit.fluxcd.io', 'flux-system')).find((k) => k.metadata.name === 'bootstrap-kit')
    expect(ks, `region ${r}: the bootstrap-kit Kustomization`).toBeTruthy()
    expect(ready(ks as Obj), `region ${r}: bootstrap-kit Ready`).toBe('True')
    const operator = (await list(r, 'pods', 'cnpg-system')).filter((p) => str(p, 'status.phase') === 'Running')
    expect(operator.length, `region ${r}: the CNPG operator Running`).toBeGreaterThan(0)
    const clusters = await cnpgClusters(r)
    expect(clusters.length, `region ${r}: CloudNativePG Clusters`).toBeGreaterThan(0)
    expect(clusters.filter((c) => !cnpgHealthy(c)).map((c) => `${c.metadata.namespace}/${c.metadata.name}: ${str(c, 'status.phase')}`), `region ${r}: Clusters that do not reconcile`).toEqual([])
  }
})

test('231 bp-postgres singleton render carries the operator-probe NetworkPolicy — a singleton (non-HA) bp-postgres render still ships the NetworkPolicy that admits the CNPG operator\'s health/readiness probe, so a singleton Postgres reaches Ready (the probe is not blocked).', async () => {
  const apps = (await list('a', 'applications.apps.openova.io')).filter((a) => /(^|\/)bp-postgres$|^postgres$/.test(str(a, 'spec.blueprintRef.name') || str(a, 'spec.blueprint')))
  const singles = apps.filter((a) => (typeof field(a, 'spec.placement') === 'string' ? str(a, 'spec.placement') : str(a, 'spec.placement.mode')) === 'singleton')
  expect(singles.length, 'a singleton bp-postgres Application').toBeGreaterThan(0)
  for (const a of singles) {
    const ns = a.metadata.namespace ?? ''
    expect(ready(a), `${ns}/${a.metadata.name}: Ready`).toBe('True')
    const nps = (await list('a', 'networkpolicies.networking.k8s.io', ns)).filter((np) => np.metadata.name.endsWith('-allow-cnpg-operator-probe'))
    expect(nps.length, `${ns}: the operator-probe NetworkPolicy`).toBeGreaterThan(0)
    expect(admits(nps, 'cnpg-system', '8000') && admits(nps, 'cnpg-system', '9187'), `${ns}: it admits cnpg-system on :8000 and :9187`).toBe(true)
    const clusters = (await cnpgClusters('a')).filter((c) => c.metadata.namespace === ns)
    expect(clusters.length, `${ns}: its CloudNativePG Cluster`).toBeGreaterThan(0)
    for (const c of clusters) expect(cnpgHealthy(c), `${ns}/${c.metadata.name}: the operator probes it (healthy)`).toBe(true)
  }
})

test('240 No-nodePort gateway serves externally on a FRESH zero-touch prov: ELB → node:443 hostPort → hostNetwork envoy; gateway Service carries ZERO nodePorts (§854 literal).', async ({ request }) => {
  for (const r of REGIONS) {
    const svcs = await list(r, 'services')
    const withNodePort = svcs.filter((s) => str(s, 'spec.type') === 'NodePort' || arr(s, 'spec.ports').some((p) => Number(field(p, 'nodePort') ?? 0) > 0))
    expect(withNodePort.map((s) => `${s.metadata.namespace}/${s.metadata.name}`), `region ${r}: Services that carry a node port`).toEqual([])
    const gw = svcs.filter((s) => s.metadata.name.startsWith('cilium-gateway'))
    expect(gw.length, `region ${r}: the gateway Services`).toBeGreaterThan(0)
    const envoy = (await list(r, 'daemonsets.apps', 'kube-system')).find((d) => d.metadata.name === 'cilium-envoy')
    expect(field(envoy, 'spec.template.spec.hostNetwork'), `region ${r}: cilium-envoy runs on the host network`).toBe(true)
  }
  const door = await request.get(consoleURL('/'), { maxRedirects: 0, timeout: 30_000 })
  expect(door.status(), 'the console answers from the public internet').toBeLessThan(400)
})
