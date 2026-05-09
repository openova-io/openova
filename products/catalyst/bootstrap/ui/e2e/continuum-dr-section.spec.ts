/**
 * continuum-dr-section.spec.ts — Playwright E2E for the EPIC-6 Slice
 * U-DR-1 (#1101) Continuum DR section embedded in the AppDetail
 * Topology tab.
 *
 * What this asserts (per `feedback_per_issue_playwright_verification.md`
 * — N issues = N snapshots, never collapse):
 *
 *  1. DR section renders + status panel populated from /continuums/{name}
 *  2. Switchover dialog opens + 7-step list visible + cancel
 *  3. Switchover dialog confirm → POST switchover endpoint hit
 *  4. SwitchingOver in-progress widget renders when status reports
 *  5. Switchover history table + detail modal
 *
 * Each test mounts mock catalyst-api responses so the page renders
 * deterministically without a live backend, then captures one
 * 1440x900 screenshot per assertion.
 */

import { test, expect, type Page, type Route } from '@playwright/test'

const DEPLOYMENT_ID = 'dr-1101'
const APP_NAME = 'wp-prod'
const NAMESPACE = 'acme'
const CONTINUUM_NAME = `dr-${APP_NAME}` // matches DRSection's default convention

const HOTSTANDBY_BP = {
  name: 'bp-wordpress',
  version: '1.2.3',
  card: { title: 'WordPress', summary: 'PHP CMS' },
  origin: 1,
  source: 'public',
  placementSchema: {
    modes: ['single-region', 'active-active', 'active-hotstandby'],
    default: 'active-hotstandby',
    minRegions: 2,
    maxRegions: 5,
  },
  raw: {
    spec: {
      version: '1.2.3',
      configSchema: { type: 'object', properties: {} },
      placementSchema: {
        modes: ['single-region', 'active-active', 'active-hotstandby'],
        default: 'active-hotstandby',
      },
    },
  },
}

const APP_STATUS = {
  name: APP_NAME,
  namespace: NAMESPACE,
  phase: 'Ready',
  spec: {
    blueprintRef: { name: 'bp-wordpress', version: '1.2.3' },
    parameters: { domain: 'shop.acme.com' },
    placement: 'active-hotstandby',
    regions: ['hz-fsn-rtz-prod', 'hz-hel-rtz-prod'],
    environmentRef: 'acme-prod',
  },
  status: {
    phase: 'Ready',
    regions: [
      { name: 'hz-fsn-rtz-prod', role: 'primary', replicas: 2, ready: 2, lastTransitionTime: '2026-05-08T12:00:00Z' },
      { name: 'hz-hel-rtz-prod', role: 'standby', replicas: 2, ready: 2, lastTransitionTime: '2026-05-08T12:00:00Z' },
    ],
  },
}

function continuumStatusBody(opts: { switchingOver?: boolean; switchoverStep?: string; lagSec?: number; lastResult?: string } = {}) {
  return {
    name: CONTINUUM_NAME,
    namespace: NAMESPACE,
    uid: 'uid-test',
    spec: {
      applicationRef: APP_NAME,
      primaryRegion: 'hz-fsn-rtz-prod',
      hotStandbyRegions: ['hz-hel-rtz-prod'],
      leaseClient: { kind: 'cloudflare-kv' },
    },
    status: {
      phase: opts.switchingOver ? 'SwitchingOver' : 'Healthy',
      primaryRegion: 'hz-fsn-rtz-prod',
      leaseHolder: 'hz-fsn-rtz-prod',
      replicationLagSeconds: opts.lagSec ?? 12,
      switchoverInProgress: !!opts.switchingOver,
      switchoverStep: opts.switchoverStep ?? '',
      lastSwitchover: opts.lastResult
        ? { result: opts.lastResult, from: 'hz-fsn-rtz-prod', to: 'hz-hel-rtz-prod', at: '2026-05-08T14:23:11Z' }
        : undefined,
    },
  }
}

function continuumAuditBody(extras: Array<{ auditType: string; ts: string; detail: string; actor?: string }> = []) {
  const base = [
    {
      auditType: 'continuum-switchover',
      ts: '2026-05-08T14:23:11Z',
      sovereignId: DEPLOYMENT_ID,
      actor: 'owner@acme.io',
      targetApp: APP_NAME,
      detail: 'switchover requested: hz-fsn-rtz-prod → hz-hel-rtz-prod (reason: drill)',
      result: 'ok',
    },
    {
      auditType: 'continuum-cnpg-promotable',
      ts: '2026-05-08T14:23:09Z',
      sovereignId: DEPLOYMENT_ID,
      targetApp: APP_NAME,
      detail: 'standby promotable; lag=4s',
    },
    {
      auditType: 'continuum-lease-acquired',
      ts: '2026-05-08T14:23:13Z',
      sovereignId: DEPLOYMENT_ID,
      targetApp: APP_NAME,
      detail: 'acquired lease for region hz-hel-rtz-prod',
    },
  ]
  return { items: [...extras, ...base], total: base.length + extras.length }
}

async function mockDRApi(page: Page, opts: { continuumStatus?: ReturnType<typeof continuumStatusBody>; switchoverPostHook?: () => void } = {}) {
  const continuumBody = opts.continuumStatus ?? continuumStatusBody()

  // Auth + self stubs.
  await page.route(/.*\/api\/v1\/sovereign\/self$/, (route: Route) => {
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ deploymentId: DEPLOYMENT_ID, sovereignFQDN: 'dr.example' }),
    })
  })
  await page.route(/.*\/api\/v1\/whoami$/, (route: Route) => {
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ sub: 'owner', email: 'owner@acme.io', tier: 'owner' }),
    })
  })
  await page.route(/.*\/api\/v1\/deployments\/[^/]+$/, (route: Route) => {
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ deploymentId: DEPLOYMENT_ID }),
    })
  })

  // Catalog endpoints.
  await page.route(/.*\/api\/v1\/catalog\/bp-wordpress$/, (route: Route) => {
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(HOTSTANDBY_BP) })
  })
  await page.route(/.*\/api\/v1\/catalog\/bp-wordpress\/versions$/, (route: Route) => {
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ name: 'bp-wordpress', versions: [{ version: '1.2.3', origin: 'public' }], upgradeMatrix: {} }),
    })
  })
  await page.route(/.*\/api\/v1\/catalog\/bp-wordpress\/versions\/[^/]+$/, (route: Route) => {
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(HOTSTANDBY_BP) })
  })

  // Application status — placement = active-hotstandby so DRSection renders.
  await page.route(/.*\/api\/v1\/sovereigns\/[^/]+\/applications\/wp-prod\/status.*$/, (route: Route) => {
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(APP_STATUS) })
  })

  // Continuum endpoints.
  await page.route(/.*\/api\/v1\/sovereigns\/[^/]+\/continuums\/dr-wp-prod(\?.*)?$/, (route: Route) => {
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(continuumBody) })
  })
  await page.route(/.*\/api\/v1\/sovereigns\/[^/]+\/continuums\/dr-wp-prod\/switchover(\?.*)?$/, (route: Route) => {
    if (opts.switchoverPostHook) opts.switchoverPostHook()
    route.fulfill({
      status: 202,
      contentType: 'application/json',
      body: JSON.stringify({
        name: CONTINUUM_NAME,
        namespace: NAMESPACE,
        targetRegion: 'hz-hel-rtz-prod',
        requestedAt: '2026-05-09T10:30:00Z',
        requestedBy: 'owner@acme.io',
        message: 'switchover requested',
      }),
    })
  })
  await page.route(/.*\/api\/v1\/sovereigns\/[^/]+\/audit\/continuum(\?.*)?$/, (route: Route) => {
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(continuumAuditBody()) })
  })

  // Compliance / RBAC stubs that the AppDetail page brings up.
  await page.route(/.*\/api\/v1\/sovereigns\/[^/]+\/compliance\/.*$/, (route: Route) => {
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ items: [], applications: [] }) })
  })
  await page.route(/.*\/api\/v1\/sovereigns\/[^/]+\/rbac\/access-matrix.*$/, (route: Route) => {
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ users: [], applications: [], tiers: ['viewer', 'developer', 'operator', 'admin', 'owner'] }),
    })
  })
  await page.route(/.*\/api\/v1\/sovereigns\/[^/]+\/clusters$/, (route: Route) => {
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ clusters: ['hz-fsn-rtz-prod', 'hz-hel-rtz-prod'] }) })
  })
}

test.describe('Continuum DR section (slice U-DR-1, #1101)', () => {
  test.use({ viewport: { width: 1440, height: 900 } })

  test('DR1: DR section renders + status panel populated', async ({ page }) => {
    await mockDRApi(page)
    await page.goto(`/provision/${DEPLOYMENT_ID}/app/${APP_NAME}`)
    await page.getByTestId('sov-app-tab-topology').click()
    await expect(page.getByTestId('continuum-dr-section')).toBeVisible()
    await expect(page.getByTestId('continuum-status-panel')).toBeVisible()
    await expect(page.getByTestId('continuum-status-phase-Healthy')).toBeVisible()
    await expect(page.getByTestId('continuum-status-lag-bucket-green')).toBeVisible()
    await page.screenshot({ path: 'test-results/continuum-dr-section/DR1-status-panel.png', fullPage: true })
  })

  test('DR2: Switchover dialog opens + 7 steps + cancel', async ({ page }) => {
    await mockDRApi(page)
    await page.goto(`/provision/${DEPLOYMENT_ID}/app/${APP_NAME}`)
    await page.getByTestId('sov-app-tab-topology').click()
    await page.getByTestId('continuum-dr-switchover-btn').click()
    await expect(page.getByTestId('continuum-switchover-dialog')).toBeVisible()
    for (let i = 1; i <= 7; i++) {
      await expect(page.getByTestId(`continuum-switchover-dialog-step-${i}`)).toBeVisible()
    }
    await page.screenshot({ path: 'test-results/continuum-dr-section/DR2-dialog-open.png', fullPage: true })
    await page.getByTestId('continuum-switchover-dialog-cancel').click()
    await expect(page.getByTestId('continuum-switchover-dialog')).toBeHidden()
  })

  test('DR3: Switchover confirm POSTs to /continuums/{name}/switchover', async ({ page }) => {
    let posted = false
    await mockDRApi(page, { switchoverPostHook: () => { posted = true } })
    await page.goto(`/provision/${DEPLOYMENT_ID}/app/${APP_NAME}`)
    await page.getByTestId('sov-app-tab-topology').click()
    await page.getByTestId('continuum-dr-switchover-btn').click()
    await page.getByTestId('continuum-switchover-dialog-reason').fill('e2e-test')
    await page.getByTestId('continuum-switchover-dialog-confirm').click()
    await expect(page.getByTestId('continuum-switchover-dialog')).toBeHidden({ timeout: 5000 })
    expect(posted).toBe(true)
    await page.screenshot({ path: 'test-results/continuum-dr-section/DR3-confirm-posted.png', fullPage: true })
  })

  test('DR4: live progress widget renders when status reports SwitchingOver', async ({ page }) => {
    await mockDRApi(page, {
      continuumStatus: continuumStatusBody({ switchingOver: true, switchoverStep: 'step 3/7 — drain-http' }),
    })
    await page.goto(`/provision/${DEPLOYMENT_ID}/app/${APP_NAME}`)
    await page.getByTestId('sov-app-tab-topology').click()
    await expect(page.getByTestId('continuum-status-switching-over')).toBeVisible()
    const stepText = await page.getByTestId('continuum-status-switching-over-step').textContent()
    expect(stepText).toContain('3/7')
    expect(stepText).toContain('drain-http')
    await page.screenshot({ path: 'test-results/continuum-dr-section/DR4-switching-over.png', fullPage: true })
  })

  test('DR5: Switchover history table + detail modal', async ({ page }) => {
    await mockDRApi(page)
    await page.goto(`/provision/${DEPLOYMENT_ID}/app/${APP_NAME}`)
    await page.getByTestId('sov-app-tab-topology').click()
    await expect(page.getByTestId('continuum-history')).toBeVisible()
    await expect(page.getByTestId('continuum-history-row-0')).toBeVisible()
    await page.getByTestId('continuum-history-row-0').click()
    await expect(page.getByTestId('continuum-history-modal')).toBeVisible()
    await expect(page.getByTestId('continuum-history-modal-step-0')).toBeVisible()
    await page.screenshot({ path: 'test-results/continuum-dr-section/DR5-history-modal.png', fullPage: true })
  })

  test('DR6: Failback panel surfaces when LastSwitchoverResult=Success', async ({ page }) => {
    await mockDRApi(page, {
      continuumStatus: continuumStatusBody({ lastResult: 'Success' }),
    })
    await page.goto(`/provision/${DEPLOYMENT_ID}/app/${APP_NAME}`)
    await page.getByTestId('sov-app-tab-topology').click()
    await expect(page.getByTestId('continuum-failback-panel')).toBeVisible()
    await expect(page.getByTestId('continuum-failback-request-btn')).toBeVisible()
    await page.screenshot({ path: 'test-results/continuum-dr-section/DR6-failback-panel.png', fullPage: true })
  })
})
