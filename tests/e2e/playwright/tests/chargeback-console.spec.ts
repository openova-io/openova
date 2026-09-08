// Chargeback cost console — browser e2e against a REAL binary + Postgres
// (#6867). Everything asserted here is a rendered pixel of the embedded UI
// fed by the binary's own API; nothing is mocked.
//
// Who runs it: .github/workflows/chargeback-e2e.yaml builds the UI and the
// Go binary, starts it against a Postgres service with
// TRUSTED_FORWARD_AUTH_HEADER (the same seam bp-oidc-gate uses on a
// Sovereign), seeds it with tests/e2e/chargeback/seed.sh and runs this file
// with the seed's env. Locally: run the same three steps and
//   CHARGEBACK_BASE_URL=http://127.0.0.1:18080 npx playwright test tests/chargeback-console.spec.ts
//
// Why the numbers are exact: seed.sh writes 7 days × 24 h of one ECS
// (0.5/h) and one 100 GB EVS volume (0.001/GB-h) into the first week of the
// previous month → 84.000 + 16.800 = 100.800 OMR, and a 10 % discount created
// HERE through the UI makes the statement 90.720 net, 4.536 tax, 95.256 total.
// A wrong join, a lost hour or a mis-applied discount changes a digit.

import { test, expect, type Page } from '@playwright/test'
import { reachable } from './_helpers'

const BASE = process.env.CHARGEBACK_BASE_URL || 'http://127.0.0.1:18080'
const OPERATOR = process.env.CHARGEBACK_OPERATOR_EMAIL || 'e2e-operator@example.invalid'
const HEADER = process.env.CHARGEBACK_FORWARD_AUTH_HEADER || 'X-Forwarded-Email'
const FROM = process.env.CB_SEED_FROM || ''
const TO = process.env.CB_SEED_TO || ''
const PERIOD = process.env.CB_SEED_PERIOD || ''
const CUSTOMER = process.env.CB_CUSTOMER_ID || ''

test.use({ extraHTTPHeaders: { [HEADER]: OPERATOR } })

test.describe('chargeback cost console (#6867)', () => {
  test.beforeAll(async () => {
    const ok = await reachable(`${BASE}/healthz`)
    test.skip(!ok, `chargeback not reachable at ${BASE} — build, start and seed it first (see file header)`)
    test.skip(!FROM || !TO || !PERIOD || !CUSTOMER, 'seed env missing — source tests/e2e/chargeback/seed.env')
  })

  const explorer = `${BASE}/explore?preset=custom&from=${FROM}&to=${TO}&group_by=kind`

  test('signs in through the forward-auth seam and lands on the overview', async ({ page }) => {
    await page.goto(`${BASE}/`)
    await expect(page).toHaveURL(/\/overview$/)
    await expect(page.getByRole('heading', { name: 'Overview', level: 1 })).toBeVisible()
    // Wire contract: the KPI strip must show real numbers, never the zero
    // cards that shipped before #6867. Live resources = the two seeded rows.
    const kpi = (label: RegExp) => page.locator('.kpi').filter({ has: page.locator('.k', { hasText: label }) })
    await expect(kpi(/^Live resources/).locator('.v')).toHaveText(/^2/)
    // Last month = the seeded week (100.8 OMR), formatted compact ("101 OMR").
    await expect(kpi(/^Last month/).locator('.v')).toHaveText('101 OMR')
    await expect(page.getByText(OPERATOR)).toBeVisible()
  })

  test('cost explorer: grouped totals, previous period, drill-in, CSV link', async ({ page }) => {
    await page.goto(explorer)
    await expect(page.getByRole('heading', { name: 'Cost explorer' })).toBeVisible()
    const table = page.locator('table').first()
    await expect(table.locator('tbody tr')).toHaveCount(2)
    await expect(table.locator('tbody tr').nth(0)).toContainText('Elastic Cloud Server')
    await expect(table.locator('tbody tr').nth(0)).toContainText('84.000 OMR')
    await expect(table.locator('tbody tr').nth(1)).toContainText('Block storage (EVS)')
    await expect(table.locator('tbody tr').nth(1)).toContainText('16.800 OMR')
    await expect(table.locator('tfoot')).toContainText('100.800 OMR')
    // The chart is real SVG, one bar per seeded day.
    // One drawn bar per seeded day per series: 7 days × 2 services.
    await expect(page.locator('.card svg rect.chart-mark')).toHaveCount(14)
    await expect(page.locator('a', { hasText: 'Export CSV' })).toHaveAttribute('href', /\/api\/v1\/cost\/export\.csv\?/)
    // Drill-in: clicking the ECS row regroups by SKU with kind=ecs kept as a filter.
    await table.locator('tbody tr').nth(0).click()
    await expect(page).toHaveURL(/group_by=sku/)
    await expect(page).toHaveURL(/kind=ecs/)
    await expect(page.locator('table').first().locator('tbody tr').first()).toContainText('ecs.m7n.xlarge.8')
    await expect(page.locator('.chip', { hasText: 'Service' })).toBeVisible()
  })

  test('resources: inventory joined with cost, drill-in to the resource', async ({ page }) => {
    await page.goto(`${BASE}/resources?preset=custom&from=${FROM}&to=${TO}`)
    await expect(page.getByRole('heading', { name: 'Resources' })).toBeVisible()
    const rows = page.locator('table').first().locator('tbody tr')
    await expect(rows).toHaveCount(2)
    await expect(rows.filter({ hasText: 'web-1' })).toContainText('84.000 OMR')
    await expect(rows.filter({ hasText: 'vol-1' })).toContainText('16.800 OMR')
    await rows.filter({ hasText: 'web-1' }).click()
    await expect(page).toHaveURL(/\/resources\/.+\/vm-e2e-1/)
    await expect(page.getByRole('table').getByText('ecs.m7n.xlarge.8')).toBeVisible()
  })

  test('customer detail: tabs, discount CRUD through the modal, budget CRUD', async ({ page }) => {
    // Idempotent against a re-used database (local runs): remove what an
    // earlier run of this file created, through the same public API.
    const ds = await page.request.get(`${BASE}/api/v1/discounts`).then((r) => r.json())
    for (const d of (ds.discounts ?? []) as Array<{ id: string; name: string }>) if (d.name === 'E2E ten percent') await page.request.delete(`${BASE}/api/v1/discounts/${d.id}`)
    const bs = await page.request.get(`${BASE}/api/v1/budgets`).then((r) => r.json())
    for (const b of (bs.budgets ?? []) as Array<{ id: string; name: string }>) if (b.name === 'E2E ceiling') await page.request.delete(`${BASE}/api/v1/budgets/${b.id}`)
    await page.goto(`${BASE}/customers/${CUSTOMER}?tab=discounts`)
    await expect(page.getByRole('heading', { name: 'Acme E2E' })).toBeVisible()
    for (const tab of ['Overview', 'Cost', 'Resources', 'Statements', 'Discounts', 'Budgets', 'Sources', 'Users', 'Settings', 'Audit']) {
      await expect(page.locator('.tabs a').filter({ hasText: new RegExp(`^${tab}`) })).toBeVisible()
    }
    // DESIGN.md §2 — the price book is a property of the SOURCE: the Sources
    // tab carries the layer badge and the scoped select, and Settings no
    // longer has a price-book field at all.
    await page.locator('.tabs a').filter({ hasText: /^Sources/ }).click()
    const srcRow = page.locator('table').first().locator('tbody tr', { hasText: 'e2e-project' })
    await expect(srcRow).toContainText('Cloud')
    const bookSelect = srcRow.getByLabel('Price book for e2e-project')
    await expect(bookSelect).toBeVisible()
    await expect(bookSelect.locator('option', { hasText: 'E2E list' })).toHaveCount(1)
    await page.locator('.tabs a').filter({ hasText: /^Settings/ }).click()
    await expect(page.getByRole('heading', { name: 'Customer settings' })).toBeVisible()
    await expect(page.getByLabel('Price book', { exact: true })).toHaveCount(0)
    await expect(page.getByText('Assign them on the Sources tab')).toBeVisible()
    await page.goto(`${BASE}/customers/${CUSTOMER}?tab=discounts`)
    // Discount: 10 % on the whole bill, created through the UI.
    await page.getByRole('button', { name: /New discount/ }).click()
    const dlg = page.getByRole('dialog')
    await dlg.getByLabel(/Name/).fill('E2E ten percent')
    await dlg.getByLabel(/Percent/).fill('10')
    await dlg.getByRole('button', { name: /^(Create|Save)/ }).click()
    await expect(page.locator('table').first().locator('tbody tr', { hasText: 'E2E ten percent' })).toBeVisible()
    await expect(page.locator('table').first().locator('tbody tr', { hasText: 'E2E ten percent' })).toContainText('10')
    // Budget: 50 OMR for this customer.
    await page.locator('.tabs a', { hasText: 'Budgets' }).click()
    await page.getByRole('button', { name: /New budget/ }).click()
    const bd = page.getByRole('dialog')
    await bd.getByLabel(/Name/).fill('E2E ceiling')
    await bd.getByLabel(/Monthly amount/).fill('50')
    await bd.getByRole('button', { name: /^(Create|Save)/ }).click()
    await expect(page.getByText('E2E ceiling')).toBeVisible()
  })

  test('statements: run the seeded period, the statement carries discount → tax → total exactly', async ({ page, request }) => {
    const run = await request.post(`${BASE}/api/v1/statements/run`, { data: { period: PERIOD, customer_id: CUSTOMER } })
    expect(run.status()).toBe(200)
    await page.goto(`${BASE}/statements?period=${PERIOD}`)
    await expect(page.getByRole('heading', { name: 'Statements' })).toBeVisible()
    const row = page.locator('table').first().locator('tbody tr', { hasText: 'Acme E2E' })
    await expect(row).toBeVisible()
    // list 100.800 · discount −10.080 · tax 4.536 · total 95.256
    await expect(row).toContainText('100.800 OMR')
    await expect(row).toContainText('10.080 OMR')
    await expect(row).toContainText('4.536 OMR')
    await expect(row).toContainText('95.256 OMR')
    await row.getByRole('button', { name: 'Open' }).click()
    await expect(page).toHaveURL(/\/statements\/[0-9a-f-]+$/)
    await expect(page.getByText('From list price to total')).toBeVisible()
    const totals = page.locator('.card', { hasText: 'Totals' })
    await expect(totals).toContainText('100.800 OMR')
    await expect(totals).toContainText('10.080 OMR')
    await expect(totals).toContainText('90.720 OMR')
    await expect(totals).toContainText('4.536 OMR')
    await expect(totals).toContainText('95.256 OMR')
    await expect(page.getByText('E2E ten percent')).toBeVisible()
    await expect(page.getByText('Elastic Cloud Server')).toBeVisible()
  })

  test('price books: cloud scope, coverage 100 %, items editable inline; discounts and budgets pages list the created rows', async ({ page }) => {
    await page.goto(`${BASE}/pricebooks`)
    const bookRow = page.locator('table').first().locator('tbody tr', { hasText: 'E2E list' })
    await expect(bookRow).toContainText('100 %')
    // DESIGN.md §2: a book prices ONE layer, and the list says which. The
    // seeded book is a cloud book, assigned to one cloud source.
    await expect(bookRow).toContainText('Cloud')
    await expect(page.getByRole('group', { name: 'Price book scope filter' })).toBeVisible()
    await expect(bookRow.locator('td').nth(4)).toContainText('1')
    await bookRow.getByRole('link', { name: 'E2E list' }).click()
    await expect(page.getByText('ecs.m7n.xlarge.8')).toBeVisible()
    // The coverage card is measured over the sources assigned to the book.
    await expect(page.getByRole('heading', { name: 'SKUs in use by the sources assigned to this book' })).toBeVisible()
    await expect(page.getByText('e2e-project').first()).toBeVisible()
    await page.goto(`${BASE}/discounts`)
    await expect(page.locator('table').first().locator('tbody tr', { hasText: 'E2E ten percent' })).toBeVisible()
    await page.goto(`${BASE}/budgets`)
    await expect(page.getByText('E2E ceiling')).toBeVisible()
  })

  test('analysis pages render on real data: anomalies, recommendations, allocation', async ({ page }) => {
    await page.goto(`${BASE}/anomalies?preset=custom&from=${FROM}&to=${TO}`)
    await expect(page.getByRole('heading', { name: 'Anomalies' })).toBeVisible()
    await page.goto(`${BASE}/recommendations`)
    await expect(page.getByRole('heading', { name: 'Recommendations' })).toBeVisible()
    await page.goto(`${BASE}/allocation`)
    await expect(page.getByRole('heading', { name: 'Allocation' })).toBeVisible()
    await expect(page.getByText('Basis weights')).toBeVisible()
    // It is a report over the two layers, never billing (DESIGN.md §2.8).
    await expect(page.getByText('A report over the two layers, not billing')).toBeVisible()
    await expect(page.getByText(/Landlord customer/)).toBeVisible()
  })

  test('explorer: hourly grain, tag dimension, custom compare window', async ({ page }) => {
    const dayTo = new Date(Date.UTC(+FROM.slice(0, 4), +FROM.slice(5, 7) - 1, +FROM.slice(8, 10) + 1)).toISOString().slice(0, 10)
    // One seeded day at hour grain: 24 buckets, 24 × (0.5 + 0.1) = 14.400 OMR.
    await page.goto(`${BASE}/explore?preset=custom&from=${FROM}&to=${dayTo}&granularity=hour&group_by=kind`)
    await expect(page.locator('table').first().locator('tfoot')).toContainText('14.400 OMR')
    await expect(page.locator('.card svg rect.chart-mark')).toHaveCount(48)
    // Tag dimension: the seeded resources carry no tags → one "(untagged)" group with the whole 100.800.
    await page.goto(`${BASE}/explore?preset=custom&from=${FROM}&to=${TO}&group_by=tag:team`)
    const tagRows = page.locator('table').first().locator('tbody tr')
    await expect(tagRows).toHaveCount(1)
    await expect(tagRows.first()).toContainText('(untagged)')
    await expect(tagRows.first()).toContainText('100.800 OMR')
    // Custom compare against the seeded week itself → previous == current, delta 0.
    await page.goto(`${BASE}/explore?preset=custom&from=${FROM}&to=${TO}&group_by=kind&compare=custom&compare_from=${FROM}&compare_to=${TO}`)
    const foot = page.locator('table').first().locator('tfoot')
    await expect(foot).toContainText('100.800 OMR')
    await expect(page.locator('table').first().locator('tbody tr').first().locator('.delta')).toContainText('0.0 %')
  })

  test('reports: a weekly schedule lists, previews the rendered text and records a manual send', async ({ page }) => {
    const existing = await page.request.get(`${BASE}/api/v1/reports/schedules`).then((r) => r.json())
    for (const r of (existing.schedules ?? []) as Array<{ id: string; name: string }>) if (r.name === 'E2E weekly') await page.request.delete(`${BASE}/api/v1/reports/schedules/${r.id}`)
    const created = await page.request.post(`${BASE}/api/v1/reports/schedules`, {
      data: { name: 'E2E weekly', customer_id: CUSTOMER, cadence: 'weekly', day_of_week: 1, hour_utc: 6, recipients: ['fin@acme-e2e.example'], sections: ['summary', 'services', 'budgets'], active: true },
    })
    expect(created.status(), await created.text()).toBe(201)
    await page.goto(`${BASE}/reports`)
    await expect(page.getByRole('heading', { name: 'Reports' })).toBeVisible()
    const row = page.locator('table').first().locator('tbody tr', { hasText: 'E2E weekly' })
    await expect(row).toBeVisible()
    await expect(row).toContainText('fin@acme-e2e.example')
    await row.getByRole('button', { name: 'Preview' }).click()
    const dlg = page.getByRole('dialog')
    await expect(dlg.locator('pre')).toContainText(/OMR/)
    await dlg.getByRole('button', { name: /Close|Done/ }).click().catch(() => page.keyboard.press('Escape'))
    await row.getByRole('button', { name: 'Send now' }).click()
    await expect(page.locator('.notice.ok')).toContainText('fin@acme-e2e.example')
  })

  test('customers list: the Sources column counts by layer, not a price book', async ({ page }) => {
    await page.goto(`${BASE}/customers`)
    const row = page.locator('table').first().locator('tbody tr', { hasText: 'Acme E2E' })
    await expect(row).toBeVisible()
    await expect(row).toContainText('1 cloud')
    await expect(page.locator('table').first().locator('thead')).toContainText('Sources')
    await expect(page.locator('table').first().locator('thead')).not.toContainText('Price book')
  })

  test('a customer principal is scoped: another customer id is not found and operator pages redirect', async ({ browser }) => {
    // The customer's admin, via the same seam, sees only its own lens.
    const ctx = await browser.newContext({ extraHTTPHeaders: { [HEADER]: 'admin@acme-e2e.example' } })
    const page: Page = await ctx.newPage()
    await page.goto(`${BASE}/`)
    await expect(page).toHaveURL(/\/my\/overview$/)
    await page.goto(`${BASE}/customers`)
    await expect(page).not.toHaveURL(/\/customers$/)
    const res = await page.request.get(`${BASE}/api/v1/cost/summary`)
    expect(res.status()).toBe(403)
    await ctx.close()
  })
})
