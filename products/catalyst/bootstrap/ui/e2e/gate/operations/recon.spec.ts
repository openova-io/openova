// Gate check FND-G21 (FND-163): the reconciliation lens of the cloud view and the reconciler drill-in
// (/cloud/resource/<kind>/<ns>/<name>/<tab>). One test per ledger row; the title is the row id and its
// clause. The reconcile and suspend actions are clicked in the console and verified on the live object;
// the HelmRelease a test suspends is resumed after it.
import { test, expect, consoleURL } from '../fixtures.ts'
import type { Page } from '@playwright/test'
import { field, getObj, helmReleases, kube, list, ready, reconcilers, selfId, apiJSON, until, type Obj } from './lib.ts'

/** The states the management surface reports (internal/helmwatch/manage.go). */
const STATES = ['Reconciled', 'Reconciling', 'Degraded', 'Suspended']

/** A HelmRelease of flux-system in region a to drive: bp-alloy where it is installed, else a Ready leaf. */
async function drivableHelmRelease(): Promise<Obj> {
  const keep = new Set(['bp-catalyst-platform', 'bp-cilium', 'bp-flux', 'bp-self-sovereign-cutover', 'bp-cert-manager', 'bp-cnpg', 'bp-openbao', 'bp-keycloak', 'bp-gitea', 'bp-harbor'])
  const hrs = (await list('a', 'helmreleases.helm.toolkit.fluxcd.io', 'flux-system')).filter((h) => ready(h) === 'True' && field(h, 'spec.suspend') !== true)
  const hr = hrs.find((h) => h.metadata.name === 'bp-alloy') ?? hrs.find((h) => !keep.has(h.metadata.name))
  expect(hr, 'a Ready HelmRelease of flux-system to reconcile, suspend and resume').toBeTruthy()
  return hr as Obj
}

async function openReconcileTab(page: Page, kind: string, ns: string, name: string): Promise<void> {
  await page.goto(consoleURL(`/cloud/resource/${kind}/${ns}/${name}/reconcile`))
  await expect(page.getByTestId('reconcile-tab'), `the Reconcile tab of ${kind} ${ns}/${name}`).toBeVisible({ timeout: 60_000 })
}

test('192 The convergence Reconciliation link opens the cloud RECON lens (view=graph&lens=reconciliation), not the default cloud lens', async ({ page }) => {
  await page.goto(consoleURL('/dashboard'))
  const link = page.getByTestId('dashboard-link-reconciliation')
  await expect(link).toBeVisible({ timeout: 60_000 })
  await link.click()
  await expect(page).toHaveURL(/\/cloud\?/, { timeout: 30_000 })
  const url = new URL(page.url())
  expect(url.searchParams.get('view'), 'view').toBe('graph')
  expect(url.searchParams.get('lens'), 'lens').toBe('reconciliation')
  await expect(page.getByTestId('cloud-architecture-lens'), 'the lens selector shows the reconciliation lens').toHaveValue('reconciliation', { timeout: 30_000 })
})

test('193 Clicking a reconciler opens the ArgoCD-like management surface (drill-in)', async ({ page }) => {
  const ks = await list('a', 'kustomizations.kustomize.toolkit.fluxcd.io', 'flux-system')
  const k = ks.find((x) => x.metadata.name === 'bootstrap-kit') ?? ks[0]
  expect(k, 'a Kustomization of flux-system').toBeTruthy()
  const name = (k as Obj).metadata.name
  await page.goto(consoleURL('/cloud?view=list&kind=kustomizations'))
  const row = page.getByTestId(`cloud-kustomization-row-${name}`)
  await expect(row).toBeVisible({ timeout: 90_000 })
  await row.click()
  await expect(page).toHaveURL(new RegExp(`/cloud/resource/kustomization/flux-system/${name}/`), { timeout: 30_000 })
  await expect(page.getByTestId(`resource-detail-kustomization-${name}`)).toBeVisible({ timeout: 60_000 })
  for (const tab of ['overview', 'yaml', 'logs', 'events', 'reconcile']) {
    await expect(page.getByTestId(`resource-detail-tab-${tab}`), `the ${tab} tab of the drill-in`).toBeVisible()
  }
  await page.getByTestId('resource-detail-tab-reconcile').click()
  await expect(page.getByTestId('reconcile-tab-actions'), 'the management actions').toBeVisible({ timeout: 30_000 })
  await expect(page.getByTestId('reconcile-action-reconcile')).toBeVisible()
})

test('194 Recon surface lists Flux reconcilers with live status (Reconciled/Reconciling/Degraded/Suspended)', async ({ page }) => {
  const rows = await reconcilers(page)
  const live = [
    ...(await helmReleases('a')).map((o) => ({ kind: 'HelmRelease', o })),
    ...(await list('a', 'kustomizations.kustomize.toolkit.fluxcd.io')).map((o) => ({ kind: 'Kustomization', o })),
  ]
  expect(live.length, 'Flux reconcilers in region a').toBeGreaterThan(0)
  const byKey = new Map(rows.map((r) => [`${r.kind}/${r.namespace}/${r.name}`, r]))
  for (const { kind, o } of live) {
    const r = byKey.get(`${kind}/${o.metadata.namespace}/${o.metadata.name}`)
    expect(r, `${kind} ${o.metadata.namespace}/${o.metadata.name} is listed`).toBeTruthy()
    expect(STATES, `${kind} ${o.metadata.name}: a state of the surface`).toContain(r?.state)
    const suspended = field(o, 'spec.suspend') === true
    expect(r?.suspended, `${kind} ${o.metadata.name}: suspended as the live object`).toBe(suspended)
    if (suspended) expect(r?.state).toBe('Suspended')
    else if (ready(o) === 'True') expect(r?.state, `${kind} ${o.metadata.name}: Ready=True on the live object`).toBe('Reconciled')
    else expect(r?.state, `${kind} ${o.metadata.name}: not Ready on the live object`).not.toBe('Reconciled')
  }
  await page.goto(consoleURL('/cloud?view=graph&lens=reconciliation'))
  await expect(page.getByTestId('cloud-architecture-lens')).toHaveValue('reconciliation', { timeout: 60_000 })
  await expect(page.getByTestId('cloud-architecture-error')).toHaveCount(0)
  await expect(page.getByTestId('cloud-architecture-canvas-wrap')).toBeVisible()
})

test('195 Drill a reconciler → its controller logs render', async ({ page }) => {
  const ks = await list('a', 'kustomizations.kustomize.toolkit.fluxcd.io', 'flux-system')
  const k = ks.find((x) => x.metadata.name === 'bootstrap-kit') ?? ks[0]
  expect(k, 'a Kustomization of flux-system').toBeTruthy()
  const name = (k as Obj).metadata.name
  const id = await selfId(page)
  const logs = await apiJSON<{ controller?: string; lines?: Array<{ message: string }> }>(page, `/api/v1/deployments/${encodeURIComponent(id)}/reconcilers/Kustomization/flux-system/${name}/logs`)
  expect(logs.controller ?? '', 'the owning controller').toMatch(/kustomize-controller/)
  expect((logs.lines ?? []).length, `controller lines about ${name}`).toBeGreaterThan(0)
  await page.goto(consoleURL(`/cloud/resource/kustomization/flux-system/${name}/logs`))
  const pane = page.getByTestId('resource-detail-logs-controller')
  await expect(pane, 'the controller logs pane of the drill-in').toBeVisible({ timeout: 60_000 })
  const first = (logs.lines ?? [])[0]?.message ?? ''
  await expect(pane, 'the pane renders the controller output').toContainText(first.slice(0, 60), { timeout: 60_000 })
})

test('196 Reconcile action → reconcile.fluxcd.io/requestedAt lands on the live object', async ({ page }) => {
  const hr = await drivableHelmRelease()
  const ns = hr.metadata.namespace ?? ''
  const name = hr.metadata.name
  const before = hr.metadata.annotations?.['reconcile.fluxcd.io/requestedAt'] ?? ''
  const startedAt = Date.now()
  await openReconcileTab(page, 'helmrelease', ns, name)
  await page.getByTestId('reconcile-action-reconcile').click()
  await expect(page.getByTestId('reconcile-action-msg'), 'the console acknowledges the request').toContainText(/reconcile requested/i, { timeout: 30_000 })
  const at = await until('the annotation on the live object', async () => {
    const o = await getObj('a', 'helmreleases.helm.toolkit.fluxcd.io', ns, name)
    const v = o?.metadata.annotations?.['reconcile.fluxcd.io/requestedAt'] ?? ''
    return v !== '' && v !== before ? v : null
  }, 60_000, 2_000)
  expect(Date.parse(at), `reconcile.fluxcd.io/requestedAt=${at} is the time of this request`).toBeGreaterThanOrEqual(startedAt - 120_000)
})

test.describe.serial('suspend and resume', () => {
  let hr: Obj | null = null

  test.afterAll(async () => {
    if (!hr) return
    const o = await getObj('a', 'helmreleases.helm.toolkit.fluxcd.io', hr.metadata.namespace ?? '', hr.metadata.name)
    if (o && field(o, 'spec.suspend') === true) {
      await kube('a', ['patch', 'helmreleases.helm.toolkit.fluxcd.io', '-n', hr.metadata.namespace ?? '', hr.metadata.name, '--type=merge', '-p', '{"spec":{"suspend":false}}'])
    }
  })

  test('197 Suspend/Resume → spec.suspend flips on the live object', async ({ page }) => {
    hr = await drivableHelmRelease()
    const ns = hr.metadata.namespace ?? ''
    const name = hr.metadata.name
    const suspendOf = async () => field(await getObj('a', 'helmreleases.helm.toolkit.fluxcd.io', ns, name), 'spec.suspend') === true
    expect(await suspendOf(), 'the HelmRelease starts not suspended').toBe(false)
    await openReconcileTab(page, 'helmrelease', ns, name)
    await page.getByTestId('reconcile-action-suspend').click()
    await until('spec.suspend=true on the live object', async () => ((await suspendOf()) ? true : null), 60_000, 2_000)
    await expect(page.getByTestId('reconcile-kv-suspended'), 'the tab reads the new state').toHaveText(/Suspended\s*yes$/, { timeout: 30_000 })
    await page.getByTestId('reconcile-action-resume').click()
    await until('spec.suspend=false on the live object', async () => ((await suspendOf()) ? null : true), 60_000, 2_000)
    await expect(page.getByTestId('reconcile-kv-suspended')).toHaveText(/Suspended\s*no$/, { timeout: 30_000 })
  })
})
