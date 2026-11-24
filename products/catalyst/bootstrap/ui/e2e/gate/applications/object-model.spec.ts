// Gate check FND-G20 (FND-162): the Applications of the Sovereign's own control plane in the object model.
// One test per ledger row; the title is the row id and its clause.
import { test, expect } from '../fixtures.ts'
import { DR_CAPABLE_SPINE, arr, field, getObj, kube, list, str, type Obj } from './lib.ts'

/** The control-plane Application of a chart: platform-<chart>, the name #6849 gave the spine-<chart> of the clause. */
function spineApp(apps: Obj[], chart: string): Obj[] {
  return apps.filter((a) => a.metadata.name === `platform-${chart}` || a.metadata.name === `spine-${chart}`)
}

async function nodeRegions(): Promise<string[]> {
  const out: string[] = []
  for (const r of ['a', 'b'] as const) {
    const v = (await kube(r, ['get', 'nodes', '-o', 'jsonpath={.items[*].metadata.labels.openova\\.io/region}'])).trim()
    out.push(...v.split(/\s+/).filter((x) => x !== ''))
  }
  return [...new Set(out)]
}

test('G6 The spine producer has enrolled the spine into the object model: one spine Application in namespace catalyst per DR-capable spine HelmRelease present, each ADOPTING flux-system/bp-<chart> (spec.bootstrap true, spec.helmRelease) and carrying the real regions in spec.regions', async () => {
  const apps = (await list('a', 'applications.apps.openova.io', 'catalyst')).filter((a) => a.metadata.labels?.['apps.openova.io/bootstrap-owned'] !== 'true')
  expect(apps.length, 'Application CRs in namespace catalyst').toBeGreaterThan(0)
  const regions = await nodeRegions()
  let present = 0
  for (const chart of DR_CAPABLE_SPINE) {
    if ((await getObj('a', 'helmreleases.helm.toolkit.fluxcd.io', 'flux-system', `bp-${chart}`)) === null) continue
    present++
    const mine = spineApp(apps, chart)
    expect(mine, `one control-plane Application for flux-system/bp-${chart}`).toHaveLength(1)
    const a = mine[0]
    expect(field(a, 'spec.bootstrap'), `${a.metadata.name}: spec.bootstrap`).toBe(true)
    const hr = field(a, 'spec.helmRelease')
    expect(typeof hr === 'string' ? hr : str(a, 'spec.helmRelease.name'), `${a.metadata.name}: adopts the live HelmRelease`).toBe(`bp-${chart}`)
    const own = arr(a, 'spec.regions').map(String)
    expect(own.length, `${a.metadata.name}: spec.regions`).toBeGreaterThan(0)
    for (const r of own) expect(regions, `${a.metadata.name}: ${r} is a region of the deployment`).toContain(r)
  }
  expect(present, 'DR-capable spine HelmReleases on the Sovereign').toBeGreaterThan(0)
})

test('237 spine Application -> Continuum round-trip is Healthy and status.continuumRef is populated on the spine Application CR (openbao / keycloak / harbor / gitea)', async () => {
  const apps = await list('a', 'applications.apps.openova.io', 'catalyst')
  for (const chart of DR_CAPABLE_SPINE) {
    const mine = spineApp(apps, chart)
    expect(mine, `the control-plane Application of ${chart}`).toHaveLength(1)
    const a = mine[0]
    const ref = str(a, 'status.continuumRef')
    expect(ref, `${a.metadata.name}: status.continuumRef`).toMatch(/^[a-z0-9-]+\/[a-z0-9.-]+$/)
    const [ns, name] = ref.split('/')
    const c = await getObj('a', 'continuums.dr.openova.io', ns, name)
    expect(c, `${a.metadata.name}: the Continuum ${ref} exists`).not.toBeNull()
    expect(str(c, 'status.phase'), `${ref}: phase`).toBe('Healthy')
    expect(str(c, 'spec.applicationRef'), `${ref}: governs ${a.metadata.namespace}/${a.metadata.name} (the round trip)`).toBe(`${a.metadata.namespace}/${a.metadata.name}`)
  }
})
