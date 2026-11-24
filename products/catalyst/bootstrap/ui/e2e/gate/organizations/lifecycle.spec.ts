// Gate check FND-G22 (FND-164): an Organization through its whole life on the candidate. The block creates
// two Organizations through the console's Create-organization form, one of each kind (gate-g22-ext,
// customer, plan m; gate-g22-int, internal, plan s), reads them on the directory, the detail page, the
// Dashboard treemap and the clusters, and deletes them through the console (DELETE
// /api/v1/organizations/{id}), which is what rows R17 and 107 measure. The names are fixed, so a run
// reuses what an earlier one left: the store keeps one audit row per deleted Organization, shown in the
// directory as suspended, and the per-Org rows of the jobs list. Each Organization asks the CA for one
// certificate of its own names; no clause here waits for it. One test per ledger row; the title is the
// row id and its clause.
import { test, expect, consoleURL, stateOf } from '../fixtures.ts'
import type { Page } from '@playwright/test'
import {
  BANNED_WORDS, FIXTURES, PLATFORM_ORG, REGIONS, apiJSON, applicationsIn, applicationsOfOrg, arr, authoritativeA, deleteOrg, field, getObj, kube,
  leftOf, list, namespacesOf, orgCRs, orgItems, poolDomain, slugOf, str, stsReady, treemap, until, vclusterSts, waitGone,
  type Cell, type Fixture,
} from './lib.ts'
import { fqdn } from '../env.ts'

const EXT = FIXTURES.customer
const INT = FIXTURES.internal

test.describe.serial('two Organizations through the Create-organization form', () => {
  let parent = ''

  /** Where an Organization runs: the one region and namespace labelled with its slug. */
  async function home(slug: string): Promise<{ region: 'a' | 'b'; ns: string }> {
    const found: Array<{ region: 'a' | 'b'; ns: string }> = []
    for (const r of REGIONS) for (const n of await namespacesOf(r, slug)) found.push({ region: r, ns: n.metadata.name })
    expect(found.map((f) => `${f.region}/${f.ns}`), `the one namespace of ${slug}`).toHaveLength(1)
    return found[0]
  }

  async function createThroughForm(page: Page, f: Fixture): Promise<void> {
    await page.goto(consoleURL('/organizations/new'))
    await expect(page.getByTestId('org-create-form')).toBeVisible({ timeout: 60_000 })
    await page.getByTestId(`create-org-kind-${f.kind}`).click()
    await page.getByTestId('create-org-plan-select').selectOption(f.plan)
    await page.getByTestId('org-create-subdomain').fill(f.slug)
    await page.getByTestId('org-create-company').fill(f.company)
    await page.getByTestId('org-create-email').fill(`${f.slug}@${fqdn()}`)
    await page.getByTestId('org-create-parent-select').selectOption(parent)
    await page.getByTestId('org-create-submit').click()
    await expect(page.getByTestId('org-create-result').or(page.getByTestId('org-create-submit-error'))).toBeVisible({ timeout: 120_000 })
    await expect(page.getByTestId('org-create-submit-error'), `the form created ${f.slug}`).toHaveCount(0)
  }

  /** Ready: the row is done, the object's vCluster is Ready, the StatefulSet is 1/1, the console name is published. */
  async function readyOf(page: Page, f: Fixture): Promise<true | null> {
    const row = (await orgItems(page)).find((o) => o.subdomain === f.slug && o.state !== 'deleted')
    const cr = (await orgCRs()).find((o) => slugOf(o) === f.slug)
    if (!row || !['done', 'tenant_registered'].includes(row.state) || !cr || str(cr, 'status.vcluster.phase') !== 'Ready') return null
    const where = await home(f.slug)
    if (!stsReady(await vclusterSts(where.region, where.ns))) return null
    return (await authoritativeA(`console.${f.slug}.${parent}`, parent)).length > 0 ? true : null
  }

  async function cleanUp(page: Page): Promise<void> {
    for (const f of [EXT, INT]) {
      await deleteOrg(page, f.slug)
      // An object the console could not delete (its row already says deleted) is deleted directly; its finalizer cascades.
      for (const o of (await orgCRs()).filter((x) => slugOf(x) === f.slug)) await kube('a', ['delete', 'organizations.orgs.openova.io', o.metadata.name, '--wait=false'])
      await waitGone(f.slug)
    }
  }

  test.beforeAll(async ({ browser }) => {
    test.setTimeout(2_400_000)
    const ctx = await browser.newContext({ storageState: stateOf('run'), baseURL: consoleURL('/') })
    const page = await ctx.newPage()
    try {
      await cleanUp(page) // what an earlier run left
      parent = await poolDomain(page)
      for (const f of [EXT, INT]) await createThroughForm(page, f)
      for (const f of [EXT, INT]) await until(`${f.slug} is provisioned`, () => readyOf(page, f), 1_500_000, 15_000)
    } finally {
      await ctx.close()
    }
  })

  test.afterAll(async ({ browser }) => {
    test.setTimeout(1_200_000)
    const ctx = await browser.newContext({ storageState: stateOf('run'), baseURL: consoleURL('/') })
    try {
      await cleanUp(await ctx.newPage())
    } finally {
      await ctx.close()
    }
  })

  async function openDirectory(page: Page): Promise<void> {
    await page.goto(consoleURL('/organizations'))
    await expect(page.getByTestId('organizations-table')).toBeVisible({ timeout: 60_000 })
  }

  /** The directory row id of an Organization (the testid suffix of its name link). */
  async function rowId(page: Page, slug: string): Promise<string> {
    const link = page.locator(`[data-testid^="organizations-cell-name-"][href$="/organizations/${slug}"]`)
    await expect(link, `the directory row of ${slug}`).toHaveCount(1)
    return ((await link.getAttribute('data-testid')) ?? '').slice('organizations-cell-name-'.length)
  }

  async function directoryRow(page: Page, slug: string) {
    await openDirectory(page)
    const id = await rowId(page, slug)
    return {
      kind: page.getByTestId(`organizations-cell-kind-${id}`),
      tier: page.getByTestId(`organizations-cell-tier-${id}`),
      billing: page.getByTestId(`organizations-cell-billing-${id}`),
      isolation: page.getByTestId(`organizations-cell-isolation-${id}`),
      status: page.getByTestId(`organizations-cell-status-${id}`),
    }
  }

  async function openDetail(page: Page, slug: string): Promise<void> {
    await page.goto(consoleURL(`/organizations/${slug}`))
    await expect(page.getByTestId('org-detail-identity'), `the detail page of ${slug}`).toBeVisible({ timeout: 60_000 })
  }

  /** The organization layer of the Dashboard treemap with its applications. */
  async function orgCells(page: Page): Promise<Map<string, Cell>> {
    return new Map((await treemap(page, ['organization', 'application'])).map((c) => [c.id ?? '', c]))
  }

  /** An Organization's cell holds its own applications and nothing of the platform. */
  async function expectOwnApps(cell: Cell | undefined, slug: string, platform: Set<string>): Promise<void> {
    expect(cell, `the treemap cell of ${slug}`).toBeTruthy()
    const names = new Set((cell?.children ?? []).map((c) => c.name))
    const own = await applicationsOfOrg(slug)
    expect(own.running.size, `${slug} runs an application`).toBeGreaterThan(0)
    for (const a of own.running) expect(names.has(a), `${slug}: its application ${a} is in its cell`).toBe(true)
    for (const a of names) expect(own.all.has(a), `${slug}: ${a} is an application of its own namespaces`).toBe(true)
    for (const a of names) expect(platform.has(a), `${slug}: ${a} is not a platform workload`).toBe(false)
  }

  const platformApps = async () => (await applicationsIn(async () => ['catalyst-system', 'kube-system', 'flux-system'], ['a'])).running

  test('8 The Create-organization form renders fully (kind picker, slug, Company name, Admin email, parent-domain).', async ({ page }) => {
    await page.goto(consoleURL('/organizations/new'))
    await expect(page.getByTestId('org-create-form')).toBeVisible({ timeout: 60_000 })
    for (const k of ['internal', 'customer']) await expect(page.getByTestId(`create-org-kind-${k}`)).toBeVisible()
    for (const id of ['create-org-plan-select', 'org-create-subdomain', 'org-create-company', 'org-create-email', 'org-create-parent-select', 'org-create-submit']) await expect(page.getByTestId(id), id).toBeVisible()
    const pool = (await apiJSON<{ items?: Array<{ name: string; role: string }> }>(page, '/api/v1/sovereign/parent-domains?role=org-pool')).items ?? []
    const names = pool.filter((p) => p.role === 'org-pool').map((p) => p.name).sort()
    expect(names.length, 'the parent domains of the Organization pool').toBeGreaterThan(0)
    // Read again until the picker's own pool request has filled it.
    const options = () => page.getByTestId('org-create-parent-select').locator('option').evaluateAll((os) => os.map((o) => (o as HTMLOptionElement).value).filter((v) => v !== '').sort())
    await expect.poll(options, { message: 'the parent-domain picker offers the pool', timeout: 60_000 }).toEqual(names)
  })

  test('119 Create-organization flow: the form title, field labels, and submit button all say "Organization" (no "SME tenant slug" / "Onboard tenant" persona words).', async ({ page }) => {
    await page.goto(consoleURL('/organizations/new'))
    await expect(page.getByTestId('create-org-title')).toHaveText('Create organization', { timeout: 60_000 })
    const form = page.getByTestId('org-create-form')
    await expect(page.getByTestId('create-org-kind').locator('legend')).toHaveText('Organization kind')
    await expect(form.getByText('Organization slug', { exact: true })).toBeVisible()
    await expect(page.getByTestId('org-create-submit')).toHaveText(/Create organization/)
    expect(await page.getByTestId('org-create-page').innerText(), 'no banned word on the form').not.toMatch(BANNED_WORDS)
  })

  test('5 Organizations directory: the customer Org row shows KIND=customer, TIER=org (the #4292 isolation class - the purchased plan is carried separately as planSlug), BILLING=real, ISOLATION=vcluster, STATUS=active.', async ({ page }) => {
    const row = await directoryRow(page, EXT.slug)
    await expect(row.kind).toHaveText('customer')
    await expect(row.tier).toHaveText('org')
    await expect(row.billing).toHaveAttribute('data-mode', 'real')
    await expect(row.isolation).toHaveText('vcluster')
    await expect(row.status).toHaveAttribute('data-status', 'active')
    const cr = (await orgCRs()).find((o) => slugOf(o) === EXT.slug)
    expect([str(cr, 'spec.tier'), str(cr, 'spec.planSlug')], 'the object carries the tier and, apart, the plan').toEqual(['org', EXT.plan])
  })

  test('6 The customer Org is present as a real Organization in the operator directory (same record dashboard + showback read).', async ({ page }) => {
    const cr = (await orgCRs()).find((o) => slugOf(o) === EXT.slug)
    expect(cr, `the Organization object ${EXT.slug}`).toBeTruthy()
    expect(str(cr, 'spec.kind'), 'its kind').toBe('customer')
    await openDirectory(page)
    await rowId(page, EXT.slug)
    // The Dashboard and the per-Org attribution read one join key, the Organization label of the namespace.
    expect((await orgCells(page)).has(EXT.slug), 'the Dashboard attributes the same Organization').toBe(true)
  })

  test('7 Org detail renders canonical fields: slug acme (NOT sme-<uuid>), kind customer, tier org + planSlug m (the #4292 split: tier is the isolation class, the purchased plan is a separate field), billing real, isolation vcluster, owner, console URL.', async ({ page }) => {
    // The walk's Organization was acme; this one's slug is the one the form was given.
    await openDetail(page, EXT.slug)
    await expect(page.getByTestId('org-detail-slug')).toHaveText(EXT.slug)
    expect(await page.getByTestId('org-detail-slug').textContent()).not.toMatch(/^sme-[0-9a-f-]+$/)
    const want: Array<[string, string]> = [['kind', 'customer'], ['tier', 'org'], ['plan', 'm'], ['billing', 'real'], ['isolation', 'vcluster']]
    for (const [k, v] of want) await expect(page.getByTestId(`org-detail-${k}`), k).toHaveText(v)
    await expect(page.getByTestId('org-detail-owner')).toHaveText(`${EXT.slug}@${fqdn()}`)
    await expect(page.getByTestId('org-detail-console-link')).toHaveText(`console.${EXT.slug}.${parent}`)
  })

  test('9 Org detail Status = active, backed by a real vcluster isolation (the create→Provision loop produced a live backing, not a fake-green).', async ({ page }) => {
    await openDetail(page, EXT.slug)
    await expect(page.getByTestId('org-detail-status')).toHaveText('active')
    await expect(page.getByTestId('org-detail-isolation')).toHaveText('vcluster')
    const where = await home(EXT.slug)
    expect(stsReady(await vclusterSts(where.region, where.ns)), `the vCluster StatefulSet of ${where.ns} is Ready`).toBe(true)
    const pods = (await list(where.region, 'pods', where.ns)).filter((p) => p.metadata.labels?.app === 'vcluster' && str(p, 'status.phase') === 'Running')
    expect(pods.length, 'a running vCluster control plane').toBeGreaterThan(0)
  })

  test('10 The customer Org is present + ACTIVE backed by a real vcluster isolation (Lane B convergence read). [plan qualifier removed 2026-09-10] the #4292 tier gate is gone: every Organization on every plan, S and free included, is backed by a dedicated vCluster, so this row is walkable against any customer Org.', async ({ page }) => {
    const row = await directoryRow(page, EXT.slug)
    await expect(row.status).toHaveAttribute('data-status', 'active')
    await expect(row.isolation).toHaveText('vcluster')
    const cr = (await orgCRs()).find((o) => slugOf(o) === EXT.slug)
    expect(str(cr, 'status.vcluster.phase'), 'the object reports its vCluster Ready').toBe('Ready')
    const where = await home(EXT.slug)
    expect(stsReady(await vclusterSts(where.region, where.ns)), 'the StatefulSet is 1/1').toBe(true)
  })

  test('11 Org detail Status = active and the directory + detail consistently show vcluster isolation. [plan qualifier removed 2026-09-10] every Organization on every plan is vCluster-backed; an S-plan Org must report vcluster isolation like any other.', async ({ page }) => {
    for (const f of [EXT, INT]) {
      const row = await directoryRow(page, f.slug)
      await expect(row.isolation, `${f.slug} (plan ${f.plan}) in the directory`).toHaveText('vcluster')
      await expect(row.status).toHaveAttribute('data-status', 'active')
      await openDetail(page, f.slug)
      await expect(page.getByTestId('org-detail-isolation'), `${f.slug} on its detail page`).toHaveText('vcluster')
      await expect(page.getByTestId('org-detail-status')).toHaveText('active')
    }
  })

  test('12 On re-load the Org detail consistently reports active + vcluster (stable, no false flicker). [plan qualifier removed 2026-09-10] walkable against any Org on any plan. Note the stability clause itself is separable and was proven stable.', async ({ page }) => {
    for (const f of [EXT, INT]) {
      for (let i = 0; i < 3; i++) {
        await openDetail(page, f.slug)
        // The first values the page renders, read at once.
        expect([await page.getByTestId('org-detail-status').textContent(), await page.getByTestId('org-detail-isolation').textContent()], `${f.slug}, load ${i + 1}`).toEqual(['active', 'vcluster'])
      }
    }
  })

  test('99 An Organization on plan M or above is backed by a DEDICATED Org vCluster — kubectl -n <org> get sts vcluster reports 1/1 Ready.', async () => {
    const cr = (await orgCRs()).find((o) => slugOf(o) === EXT.slug)
    expect(str(cr, 'spec.planSlug'), 'the plan').toBe('m')
    const where = await home(EXT.slug)
    const sts = await vclusterSts(where.region, where.ns)
    expect([Number(field(sts, 'spec.replicas')), Number(field(sts, 'status.readyReplicas'))], `${where.ns}/vcluster: replicas and ready replicas`).toEqual([1, 1])
    const ns = await getObj(where.region, 'namespace', '', where.ns)
    expect(ns?.metadata.labels?.['openova.io/organization'], 'the namespace is the Organization\'s own').toBe(EXT.slug)
  })

  test('100 An Organization on plan S — the smallest plan and the default the funnel applies when no plan is chosen — is backed by a DEDICATED Org vCluster exactly like every other plan: kubectl -n <org> get sts vcluster reports 1/1 Ready and the Organization CR carries status.vcluster.phase=Ready; there is no plan whose Organizations share the bare host namespace (the #4292 tier gate was removed 2026-09-10).', async () => {
    const crs = await orgCRs()
    const cr = crs.find((o) => slugOf(o) === INT.slug)
    expect([str(cr, 'spec.planSlug'), str(cr, 'status.vcluster.phase')], `${INT.slug}: plan and vCluster phase`).toEqual(['s', 'Ready'])
    const where = await home(INT.slug)
    expect(stsReady(await vclusterSts(where.region, where.ns)), `${where.ns}/vcluster 1/1`).toBe(true)
    // Every other Organization of the candidate, whatever its plan; the Sovereign's own one is the parent, not an Organization on a plan.
    const own = fqdn().replace(/\./g, '-')
    for (const o of crs.filter((x) => slugOf(x) !== own && !x.metadata.deletionTimestamp)) {
      expect(str(o, 'status.vcluster.phase'), `${slugOf(o)} (plan ${str(o, 'spec.planSlug') || 'none'}) has its own vCluster`).toBe('Ready')
    }
  })

  test('101 The console Organization detail\'s isolation value is DERIVED from the observed backing, not inferred from kind — an Org whose backing is a host namespace must not report vcluster.', async ({ page }) => {
    const live = (await orgItems(page)).filter((o) => o.state !== 'deleted')
    const crs = new Set((await orgCRs()).map(slugOf))
    const measured = live.filter((o) => crs.has(o.subdomain))
    expect(measured.map((o) => o.subdomain), 'the Organizations that are objects').toEqual(expect.arrayContaining([EXT.slug, INT.slug]))
    for (const o of measured) {
      let observed = false
      for (const r of REGIONS) for (const n of await namespacesOf(r, o.subdomain)) if ((await vclusterSts(r, n.metadata.name)) !== null) observed = true
      await openDetail(page, o.subdomain)
      const shown = (await page.getByTestId('org-detail-isolation').textContent()) ?? ''
      if (observed) expect(shown, `${o.subdomain} (kind ${o.kind}) runs a vCluster`).toBe('vcluster')
      else expect(shown, `${o.subdomain} (kind ${o.kind}) runs no vCluster`).not.toBe('vcluster')
    }
  })

  test('117 Directory org cards / list rows: column headers read "Organization / Kind / Tier / Billing / Isolation / Status"; rows labeled "Organization", never "tenant"/"SME".', async ({ page }) => {
    await openDirectory(page)
    expect((await page.getByTestId('organizations-table').locator('thead th').allTextContents()).map((t) => t.trim())).toEqual(['Organization', 'Kind', 'Tier', 'Billing', 'Isolation', 'Status'])
    for (const f of [EXT, INT]) await expect(page.getByTestId(`organizations-cell-name-${await rowId(page, f.slug)}`), `the Organization column names ${f.slug}`).toContainText(f.company)
    expect(await page.getByTestId('organizations-table').innerText(), 'no banned word in the directory').not.toMatch(BANNED_WORDS)
  })

  test('120 Organization-detail view: heading "Acme Corp", breadcrumb "← Organizations", field labels Slug/Kind/Tier/Billing mode/Isolation/Stat… — no "tenant"/"SME".', async ({ page }) => {
    // The walk's Organization was Acme Corp; this one carries the company name the form was given.
    await openDetail(page, EXT.slug)
    await expect(page.getByTestId('portal-header-title')).toHaveText(EXT.company)
    await expect(page.getByTestId('org-detail-back-link')).toHaveText('← Organizations')
    const labels = (await page.getByTestId('org-detail-identity').locator('dt').allTextContents()).map((t) => t.trim())
    for (const l of ['Slug', 'Kind', 'Tier', 'Billing mode', 'Isolation', 'Status']) expect(labels, 'the field labels').toContain(l)
    expect(await page.getByTestId('organization-detail-page').innerText(), 'no banned word').not.toMatch(BANNED_WORDS)
  })

  test('215 CRD tier enum is [org, corporate] — a tier: org Organization is accepted.', async () => {
    const crd = await getObj('a', 'crd', '', 'organizations.orgs.openova.io')
    expect(crd, 'the CRD organizations.orgs.openova.io').not.toBeNull()
    const versions = arr(crd, 'spec.versions').filter((v) => (v as { served?: boolean }).served === true)
    expect(versions.length, 'a served version').toBeGreaterThan(0)
    for (const v of versions) expect(arr(v, 'schema.openAPIV3Schema.properties.spec.properties.tier.enum'), `the tier enum of ${str(v, 'name')}`).toEqual(['org', 'corporate'])
    const crs = await orgCRs()
    for (const f of [EXT, INT]) expect(str(crs.find((o) => slugOf(o) === f.slug), 'spec.tier'), `${f.slug} was accepted with tier org`).toBe('org')
  })

  test('G7 vcluster dual-door walk — both Org doors land a vcluster-isolation Org.', async ({ page }) => {
    // The form's two doors: customer, the shape the storefront creates, and internal, a department.
    for (const f of [EXT, INT]) {
      const cr = (await orgCRs()).find((o) => slugOf(o) === f.slug)
      expect(str(cr, 'spec.kind'), `${f.slug}: its door`).toBe(f.kind)
      const row = await directoryRow(page, f.slug)
      await expect(row.isolation, `${f.slug} (${f.kind})`).toHaveText('vcluster')
      const where = await home(f.slug)
      expect(stsReady(await vclusterSts(where.region, where.ns)), `${f.slug}: its vCluster is Ready`).toBe(true)
    }
  })

  // Rows 21, 22 and 23 name the Organizations Showback panel. Cost per Organization moved to the billing
  // engine (CTL-355); the attribution the panel showed is the organization layer of the Dashboard
  // treemap, which keys on the same Organization label of the namespace (handler/dashboard.go).
  test('21 Organizations Showback panel: the Application column for a selected Org lists only that Org\'s real apps (no cluster-wide infra/Job pods).', async ({ page }) => {
    await expectOwnApps((await orgCells(page)).get(EXT.slug), EXT.slug, await platformApps())
  })

  test('22 Showback shows a single visually-distinct Platform overhead roll-up line holding all control-plane/Job workloads.', async ({ page }) => {
    const items = await treemap(page, ['organization', 'application'])
    const platform = items.filter((c) => c.id === PLATFORM_ORG || c.name === 'Platform overhead')
    expect(platform.map((c) => [c.id, c.name]), 'one platform-overhead line').toEqual([[PLATFORM_ORG, 'Platform overhead']])
    const held = new Set((platform[0].children ?? []).map((c) => c.name))
    for (const a of await platformApps()) expect(held.has(a), `the control-plane workload ${a} is platform overhead`).toBe(true)
    // One-shot Job Pods are activity, not estate: the treemap drops them (isEphemeralRow), so no Organization holds one.
    const jobs = new Set<string>()
    for (const r of REGIONS) for (const j of await list(r, 'jobs')) jobs.add(j.metadata.name)
    for (const c of items.filter((x) => x.id !== PLATFORM_ORG)) for (const a of c.children ?? []) expect(jobs.has(a.name), `${c.id}: ${a.name} is not a Job`).toBe(false)
  })

  test('23 After a 2nd Org runs an app, the showback panel shows a SECOND Org row attributed only to its own app, distinct from Platform overhead.', async ({ page }) => {
    const cells = await orgCells(page)
    const platform = await platformApps()
    for (const f of [EXT, INT]) await expectOwnApps(cells.get(f.slug), f.slug, platform)
    expect([EXT.slug, INT.slug].filter((s) => cells.has(s)), 'two Organization rows apart from platform overhead').toEqual([EXT.slug, INT.slug])
  })

  test('25 One consistent model across surfaces: /organizations shows the Orgs that /apps, the dashboard and showback all agree on.', async ({ page }) => {
    const crs = new Set((await orgCRs()).filter((o) => !o.metadata.deletionTimestamp).map(slugOf))
    const live = new Set((await orgItems(page)).filter((o) => o.state !== 'deleted').map((o) => o.subdomain))
    expect([...live].sort(), 'the directory rows are the Organization objects').toEqual([...crs].sort())
    for (const s of [EXT.slug, INT.slug]) expect(live.has(s), `${s} in the directory`).toBe(true)
    for (const id of (await orgCells(page)).keys()) if (id !== PLATFORM_ORG) expect(crs.has(id), `the Dashboard's Organization ${id} is in the directory`).toBe(true)
    for (const a of await list('a', 'applications.apps.openova.io')) {
      const org = str(a, 'spec.organizationRef')
      if (org !== '') expect(crs.has(org), `the Application ${a.metadata.namespace}/${a.metadata.name} belongs to an Organization of the directory`).toBe(true)
    }
  })

  test('R17 deleting an Organization CR cascades cleanly — no orphaned ns / app / DNS leak.', async ({ page }) => {
    test.setTimeout(900_000)
    const name = `console.${EXT.slug}.${parent}`
    expect(await authoritativeA(name, parent), `${name} is published before the delete`).not.toEqual([])
    // The console's delete marks the object for deletion; the org-controller's finalizer runs the cascade.
    expect(await deleteOrg(page, EXT.slug), 'the console deleted the Organization').toBe(1)
    await waitGone(EXT.slug)
    for (const r of REGIONS) {
      const apps = (await list(r, 'applications.apps.openova.io')).filter((a) => str(a, 'spec.organizationRef') === EXT.slug)
      expect(apps.map((a) => a.metadata.name), `region ${r}: Applications of ${EXT.slug}`).toEqual([])
      const certs = (await list(r, 'certificates.cert-manager.io')).filter((c) => arr(c, 'spec.dnsNames').some((d) => String(d).includes(`${EXT.slug}.`)))
      expect(certs.map((c) => `${c.metadata.namespace}/${c.metadata.name}`), `region ${r}: certificates of ${EXT.slug}`).toEqual([])
    }
    await until(`${name} is withdrawn`, async () => ((await authoritativeA(name, parent)).length === 0 ? true : null), 300_000, 10_000)
    expect(await leftOf(EXT.slug), 'nothing is left').toEqual([])
  })

  test('107 Deleting an Organization removes its vCluster StatefulSet — no orphaned vCluster survives the delete cascade.', async ({ page }) => {
    test.setTimeout(900_000)
    const where = await home(INT.slug)
    expect(stsReady(await vclusterSts(where.region, where.ns)), 'the vCluster runs before the delete').toBe(true)
    expect(await deleteOrg(page, INT.slug), 'the console deleted the Organization').toBe(1)
    await until(`the vCluster of ${INT.slug} is gone`, async () => ((await vclusterSts(where.region, where.ns)) === null ? true : null), 600_000, 5_000)
    await waitGone(INT.slug)
    for (const r of REGIONS) {
      const pods = (await list(r, 'pods')).filter((p) => p.metadata.labels?.app === 'vcluster' && (p.metadata.namespace ?? '') === where.ns)
      expect(pods.map((p) => p.metadata.name), `region ${r}: vCluster Pods of ${where.ns}`).toEqual([])
    }
  })
})
