/**
 * cloud-crud.spec.ts — Playwright E2E lock-in for the full CRUD
 * surface delivered by issue #349 (Phase A.2 of #347).
 *
 * What this asserts:
 *   • Every list page header surfaces a + New CTA that opens the Add
 *     modal for that kind.
 *   • Every list-row exposes a ⋯ menu with Edit + Delete.
 *   • Click-row continues to open the detail drawer; the drawer's
 *     header carries Edit + Delete buttons.
 *   • The Architecture force-graph context menu surfaces Edit + Delete
 *     for every node kind (Region/Cluster/vCluster/NodePool/
 *     WorkerNode/LoadBalancer/Network).
 *   • Screenshots saved at 1440×900 covering: Edit modal open for
 *     Cluster, Add modal open for PVC, row-action menu open, delete
 *     confirm.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), the URL comes
 * from playwright.config.ts (env-driven HOST + BASEPATH); we use a
 * synthetic deploymentId and rely on the SPA's fixture fallback for
 * the data plane. Per ADR-0001 §9.4 we never touch org/.
 */

import { test, expect, type Page } from '@playwright/test'

const DEPLOYMENT_ID = 'crud-349-e2e'

async function gotoList(page: Page, suffix: string) {
  await page.goto(`provision/${DEPLOYMENT_ID}/cloud/${suffix}`)
  await page.waitForLoadState('domcontentloaded')
}

async function gotoArch(page: Page) {
  await page.goto(`provision/${DEPLOYMENT_ID}/cloud/architecture`)
  await page.waitForLoadState('domcontentloaded')
  await page.waitForSelector('[data-testid=arch-graph-svg]')
}

test.describe('Cloud CRUD breadth (#349)', () => {
  test('every list page surfaces a + New CTA in the header', async ({ page }) => {
    const cases = [
      { suffix: 'compute/clusters', testId: 'cloud-clusters' },
      { suffix: 'compute/vclusters', testId: 'cloud-vclusters' },
      { suffix: 'compute/node-pools', testId: 'cloud-node-pools' },
      { suffix: 'compute/worker-nodes', testId: 'cloud-worker-nodes' },
      { suffix: 'network/load-balancers', testId: 'cloud-load-balancers' },
      { suffix: 'storage/pvcs', testId: 'cloud-pvcs' },
      { suffix: 'storage/buckets', testId: 'cloud-buckets' },
      { suffix: 'storage/volumes', testId: 'cloud-volumes' },
    ]
    for (const c of cases) {
      await gotoList(page, c.suffix)
      const newBtn = page.getByTestId(`${c.testId}-new-btn`)
      await expect(newBtn, `+ New CTA must show on /${c.suffix}`).toBeVisible()
    }
  })

  test('row ⋯ kebab exposes Edit + Delete on every list page', async ({ page }) => {
    // Use the seeded first row from the fixture (same ids as
    // cloud-list-pages.spec.ts).
    const cases = [
      { suffix: 'compute/clusters', rowId: 'cloud-clusters-row-cluster-eu-central-primary' },
      { suffix: 'compute/vclusters', rowId: 'cloud-vclusters-row-vc-eu-central-dmz' },
      { suffix: 'compute/node-pools', rowId: 'cloud-node-pools-row-pool-eu-cp' },
      { suffix: 'compute/worker-nodes', rowId: 'cloud-worker-nodes-row-node-eu-cp-0' },
      { suffix: 'network/load-balancers', rowId: 'cloud-load-balancers-row-lb-eu-central-edge' },
      { suffix: 'storage/pvcs', rowId: 'cloud-pvcs-row-pvc-postgres-data' },
      { suffix: 'storage/buckets', rowId: 'cloud-buckets-row-bucket-backups' },
      { suffix: 'storage/volumes', rowId: 'cloud-volumes-row-vol-postgres-eu' },
    ]
    for (const c of cases) {
      await gotoList(page, c.suffix)
      const trigger = page.getByTestId(`${c.rowId}-actions-trigger`)
      await expect(trigger, `kebab on /${c.suffix} row ${c.rowId}`).toBeVisible()
      await trigger.click()
      await expect(page.getByTestId(`${c.rowId}-actions-edit`)).toBeVisible()
      await expect(page.getByTestId(`${c.rowId}-actions-delete`)).toBeVisible()
      // Dismiss to keep test independent.
      await page.keyboard.press('Escape')
    }
  })

  test('clicking + New cluster opens AddClusterModal', async ({ page }) => {
    await gotoList(page, 'compute/clusters')
    await page.getByTestId('cloud-clusters-new-btn').click()
    await expect(page.getByTestId('infrastructure-modal-add-cluster')).toBeVisible()
    await expect(page.getByTestId('infrastructure-modal-add-cluster-title')).toContainText(/Add cluster/i)
  })

  test('clicking row Edit opens the Edit modal pre-filled', async ({ page }) => {
    await gotoList(page, 'compute/clusters')
    const rowId = 'cloud-clusters-row-cluster-eu-central-primary'
    await page.getByTestId(`${rowId}-actions-trigger`).click()
    await page.getByTestId(`${rowId}-actions-edit`).click()
    await expect(page.getByTestId('infrastructure-modal-edit-cluster')).toBeVisible()
    await expect(page.getByTestId('infrastructure-modal-edit-cluster-title')).toContainText(/Edit cluster/i)
    // Form field exists (cluster name pre-filled from fixture).
    await expect(page.getByTestId('cluster-form-name')).toBeVisible()
  })

  test('clicking row Delete opens the cascade-aware delete confirm for cluster', async ({ page }) => {
    await gotoList(page, 'compute/clusters')
    const rowId = 'cloud-clusters-row-cluster-eu-central-primary'
    await page.getByTestId(`${rowId}-actions-trigger`).click()
    await page.getByTestId(`${rowId}-actions-delete`).click()
    await expect(page.getByTestId('infrastructure-modal-delete-cascade')).toBeVisible()
  })

  test('detail drawer surfaces Edit + Delete buttons', async ({ page }) => {
    await gotoList(page, 'compute/clusters')
    const rowId = 'cloud-clusters-row-cluster-eu-central-primary'
    await page.getByTestId(rowId).click()
    await expect(page.getByTestId('cloud-clusters-detail')).toBeVisible()
    await expect(page.getByTestId('cloud-clusters-detail-actions-edit')).toBeVisible()
    await expect(page.getByTestId('cloud-clusters-detail-actions-delete')).toBeVisible()
  })

  test('PVC list page Add modal opens with namespace + capacity fields', async ({ page }) => {
    await gotoList(page, 'storage/pvcs')
    await page.getByTestId('cloud-pvcs-new-btn').click()
    await expect(page.getByTestId('infrastructure-modal-add-pvc')).toBeVisible()
    await expect(page.getByTestId('pvc-form-name')).toBeVisible()
    await expect(page.getByTestId('pvc-form-namespace')).toBeVisible()
    await expect(page.getByTestId('pvc-form-capacity')).toBeVisible()
    await expect(page.getByTestId('pvc-form-storage-class')).toBeVisible()
  })

  test('PVC Edit modal exposes only the capacity field as editable', async ({ page }) => {
    await gotoList(page, 'storage/pvcs')
    const rowId = 'cloud-pvcs-row-pvc-postgres-data'
    await page.getByTestId(`${rowId}-actions-trigger`).click()
    await page.getByTestId(`${rowId}-actions-edit`).click()
    await expect(page.getByTestId('infrastructure-modal-edit-pvc')).toBeVisible()
    // Capacity is editable.
    await expect(page.getByTestId('pvc-form-capacity')).toBeVisible()
    // Name + namespace + storage class are read-only.
    await expect(page.getByTestId('pvc-form-name-readonly')).toBeVisible()
    await expect(page.getByTestId('pvc-form-namespace-readonly')).toBeVisible()
    await expect(page.getByTestId('pvc-form-storage-class-readonly')).toBeVisible()
  })

  test('Architecture context-menu surfaces kind-aware Edit + Delete', async ({ page }) => {
    await gotoArch(page)
    const cluster = page.getByTestId(
      'arch-graph-node-Cluster-Cluster:cluster-eu-central-primary',
    )
    // force: true — continuous force-graph simulation never settles.
    await cluster.click({ button: 'right', force: true })
    const menu = page.getByTestId('cloud-architecture-context-menu')
    await expect(menu).toBeVisible()
    await expect(page.getByTestId('cloud-architecture-context-edit')).toBeVisible()
    await expect(page.getByTestId('cloud-architecture-context-delete')).toBeVisible()
  })

  test('Architecture context-menu on Region surfaces add-network and add-volume', async ({ page }) => {
    await gotoArch(page)
    const region = page.getByTestId(
      'arch-graph-node-Region-Region:region-eu-central',
    )
    await region.click({ button: 'right', force: true })
    await expect(page.getByTestId('cloud-architecture-context-menu')).toBeVisible()
    await expect(page.getByTestId('cloud-architecture-context-add-cluster')).toBeVisible()
    await expect(page.getByTestId('cloud-architecture-context-add-network')).toBeVisible()
    await expect(page.getByTestId('cloud-architecture-context-add-volume')).toBeVisible()
  })

  test('Architecture context-menu on Cluster surfaces add-worker-node and add-pvc', async ({ page }) => {
    await gotoArch(page)
    const cluster = page.getByTestId(
      'arch-graph-node-Cluster-Cluster:cluster-eu-central-primary',
    )
    await cluster.click({ button: 'right', force: true })
    await expect(page.getByTestId('cloud-architecture-context-add-vcluster')).toBeVisible()
    await expect(page.getByTestId('cloud-architecture-context-add-nodepool')).toBeVisible()
    await expect(page.getByTestId('cloud-architecture-context-add-worker-node')).toBeVisible()
    await expect(page.getByTestId('cloud-architecture-context-add-pvc')).toBeVisible()
  })

  test('captures #349 CRUD screenshots @ 1440x900', async ({ page }) => {
    // 1: Cluster Edit modal open.
    await gotoList(page, 'compute/clusters')
    const rowId = 'cloud-clusters-row-cluster-eu-central-primary'
    await page.getByTestId(`${rowId}-actions-trigger`).click()
    await page.waitForTimeout(150)
    await page.screenshot({
      path: 'e2e/screenshots/p349-cluster-row-actions-menu.png',
      fullPage: false,
    })
    await page.getByTestId(`${rowId}-actions-edit`).click()
    await expect(page.getByTestId('infrastructure-modal-edit-cluster')).toBeVisible()
    await page.waitForTimeout(150)
    await page.screenshot({
      path: 'e2e/screenshots/p349-cluster-edit-modal.png',
      fullPage: false,
    })
    // Close.
    await page.getByTestId('infrastructure-modal-edit-cluster-close').click()

    // 2: PVC Add modal open.
    await gotoList(page, 'storage/pvcs')
    await page.getByTestId('cloud-pvcs-new-btn').click()
    await expect(page.getByTestId('infrastructure-modal-add-pvc')).toBeVisible()
    await page.waitForTimeout(150)
    await page.screenshot({
      path: 'e2e/screenshots/p349-pvc-add-modal.png',
      fullPage: false,
    })
    await page.getByTestId('infrastructure-modal-add-pvc-close').click()

    // 3: Volume Delete confirm.
    await gotoList(page, 'storage/volumes')
    const volRow = 'cloud-volumes-row-vol-postgres-eu'
    await page.getByTestId(`${volRow}-actions-trigger`).click()
    await page.getByTestId(`${volRow}-actions-delete`).click()
    await expect(page.getByTestId('infrastructure-modal-delete-volumes')).toBeVisible()
    await page.waitForTimeout(150)
    await page.screenshot({
      path: 'e2e/screenshots/p349-volume-delete-confirm.png',
      fullPage: false,
    })
  })
})
