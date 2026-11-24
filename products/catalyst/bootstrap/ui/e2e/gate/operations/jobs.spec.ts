// Gate check FND-G21 (FND-163): the jobs page (/jobs) and its feed. One test per ledger row; the title is
// the row id and its clause. The page is read signed in; its feed, GET /api/v1/deployments/{id}/jobs, is
// read through the same session; the objects behind the rows are read with regionKubectl.
//
// The rows about a Failed row (172, 174, 176, 177) need one: a converged candidate has none, so the
// serial block creates a standalone batch Job that cannot succeed (its command does not exist, no
// retry, a deadline) from the template of a CronJob of the candidate (an image the candidate already
// pulls), and deletes it after the block. Row 173 makes a status change the same way: its Job's pod is
// never scheduled, so the row runs in the open table until the deadline fails it.
//
// The two fixture Jobs have fixed names (gate-fail, gate-live), never a name per run: the catalyst-api keeps
// a task row after its Job is deleted, so a name per run would add two Failed rows to /jobs on every run.
// With a fixed name every run reuses the same two rows.
import { mkdtempSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test, expect, consoleURL } from '../fixtures.ts'
import type { Page } from '@playwright/test'
import { JOB_STATUSES, arr, cond, field, getObj, helmReleases, jobsFeed, kube, list, ready, reconcilers, until, type JobRow, type Obj } from './lib.ts'

/** Opens /jobs in its list view and waits for the table. */
async function openJobs(page: Page): Promise<void> {
  await page.goto(consoleURL('/jobs?view=list'))
  await expect(page.getByTestId('jobs-table'), 'the jobs table').toBeVisible({ timeout: 90_000 })
  await expect(page.getByTestId('jobs-loading')).toHaveCount(0, { timeout: 90_000 })
}

/** The row of the table for one row of the feed. */
const rowOf = (page: Page, j: JobRow) => page.getByTestId(`jobs-table-row-${j.id}`)

/** The id a HelmRelease gives its install row: install-<chart>, or install-<region>:<chart> for a further region. */
function installTarget(j: JobRow): string {
  const rest = j.jobName.replace(/^install-/, '')
  const at = rest.lastIndexOf(':')
  return at >= 0 ? rest.slice(at + 1) : rest
}

/**
 * Applies to region a a standalone batch Job that cannot succeed, built from the pod template of a CronJob
 * of the candidate (an image it already pulls): one container whose command does not exist, no retry, the
 * given deadline. extra is merged into the pod spec. A Job of the same name that an earlier run left is
 * deleted first (the template of a Job cannot change). The CronJobs of the scanner namespaces are never
 * copied: helmwatch folds every Job there onto the scanner's row, so no task row would appear. Returns the
 * namespace of the Job.
 */
async function applyFixtureJob(name: string, deadlineSeconds: number, extra: Record<string, unknown> = {}): Promise<string> {
  const crons = (await list('a', 'cronjobs.batch')).filter((c) => field(c, 'spec.suspend') !== true && !['kube-system', 'syft-grype', 'trivy-system'].includes(c.metadata.namespace ?? ''))
  expect(crons.length, 'a CronJob of the candidate whose template the fixture reuses').toBeGreaterThan(0)
  const cron = crons[0]
  const ns = cron.metadata.namespace ?? ''
  const template = JSON.parse(JSON.stringify(field(cron, 'spec.jobTemplate.spec.template') ?? {})) as Record<string, unknown>
  const spec = (template.spec ?? {}) as Record<string, unknown>
  delete spec.initContainers
  delete spec.volumes
  spec.restartPolicy = 'Never'
  spec.automountServiceAccountToken = false
  spec.containers = arr(spec, 'containers').slice(0, 1).map((c) => {
    const out = { ...(c as Record<string, unknown>) }
    delete out.args
    delete out.volumeMounts
    delete out.livenessProbe
    delete out.readinessProbe
    out.command = ['/gate-fixture-exits-nonzero']
    return out
  })
  Object.assign(spec, extra)
  const labels = { ...(cron.metadata.labels ?? {}), 'openova.io/gate-fixture': name }
  const manifest = {
    apiVersion: 'batch/v1',
    kind: 'Job',
    metadata: { name, namespace: ns, labels },
    spec: { backoffLimit: 0, activeDeadlineSeconds: deadlineSeconds, template: { metadata: { labels }, spec } },
  }
  const file = join(mkdtempSync(join(tmpdir(), 'gate-fixture-')), 'job.json')
  writeFileSync(file, JSON.stringify(manifest))
  await kube('a', ['delete', 'jobs.batch', '-n', ns, name, '--ignore-not-found', '--wait=true'], 180_000)
  await kube('a', ['apply', '-f', file])
  return ns
}

test('167 Open the console root in a fresh tab → land on the operator dashboard signed in as the sovereign-admin (no login form, no password field)', async ({ page }) => {
  await page.goto(consoleURL('/'))
  await expect(page).toHaveURL(/\/dashboard(\?|$)/, { timeout: 60_000 })
  await expect(page.getByTestId('dashboard-page'), 'the operator dashboard').toBeVisible({ timeout: 60_000 })
  await expect(page.locator('input[type="password"]'), 'no password field').toHaveCount(0)
  await expect(page).not.toHaveURL(/\/login/)
  const who = await page.request.get(consoleURL('/api/v1/whoami'), { headers: { Accept: 'application/json' } })
  expect(who.status(), 'GET /api/v1/whoami: the session is signed in').toBe(200)
  const me = (await who.json()) as { email?: string; tier?: string }
  expect(me.email ?? '', 'the signed-in identity').toMatch(/@/)
  expect(me.tier ?? '', 'the identity is a sovereign-admin (tier owner or admin)').toMatch(/^(owner|admin)$/)
})

test('168 Open /jobs → the canvas table renders a populated list of activity rows (not a spinner, empty state, or login redirect)', async ({ page }) => {
  const feed = await jobsFeed(page)
  expect(feed.length, 'the jobs feed of the candidate holds rows').toBeGreaterThan(0)
  await openJobs(page)
  await expect(page).not.toHaveURL(/\/login/)
  await expect(page.getByTestId('jobs-table-empty'), 'not the empty state').toHaveCount(0)
  const rows = page.locator('[data-testid^="jobs-table-row-"]')
  await expect.poll(() => rows.count(), { message: 'the table renders the rows of the feed', timeout: 60_000 }).toBeGreaterThan(0)
  const shown = (await page.getByTestId('jobs-result-count').innerText()).trim()
  const total = Number(shown.split('/')[1])
  expect(Math.abs(total - feed.length), `the table counts ${shown}; the feed holds ${feed.length} rows`).toBeLessThanOrEqual(Math.max(2, Math.ceil(feed.length * 0.05)))
})

test('169 The Kind column is present in the header and each row shows its kind; full header = Name·Kind·App·Deps·…', async ({ page }) => {
  const feed = await jobsFeed(page)
  await openJobs(page)
  const header = (await page.getByTestId('jobs-table').locator('thead th').allInnerTexts()).map((h) => h.trim())
  const want = ['Name', 'Kind', 'App', 'Deps', 'Parent', 'Status', 'Runs', 'Started', 'Duration', 'Actions']
  expect(header.filter((h) => h !== 'Region'), `the header: ${header.join(' · ')}`).toEqual(want)
  const kindOf = new Map(feed.map((j) => [j.id, j.kind ?? '']))
  const chips = await page.locator('[data-testid^="jobs-cell-kind-"]').evaluateAll((els) => els.map((e) => ({ id: (e.getAttribute('data-testid') ?? '').replace(/^jobs-cell-kind-/, ''), kind: e.getAttribute('data-kind') ?? '' })))
  const rows = await page.locator('[data-testid^="jobs-table-row-"]').count()
  expect(chips.length, 'one kind chip on every row').toBe(rows)
  expect(rows).toBeGreaterThan(0)
  for (const c of chips) {
    expect(c.kind, `row ${c.id}: its kind`).not.toBe('')
    if (kindOf.get(c.id)) expect(c.kind, `row ${c.id}: the kind of its feed row`).toBe(kindOf.get(c.id))
  }
})

test('170 Scroll/search to the install-openbao row → it renders green / Succeeded (the install is honestly green)', async ({ page }) => {
  const feed = await jobsFeed(page)
  const row = feed.find((j) => j.jobName === 'install-openbao')
  expect(row, 'the feed holds the row install-openbao').toBeTruthy()
  const hr = (await helmReleases('a')).find((h) => h.metadata.name === 'bp-openbao')
  expect(hr, 'the HelmRelease bp-openbao of region a').toBeTruthy()
  expect(ready(hr as Obj), 'the live HelmRelease bp-openbao is Ready').toBe('True')
  await openJobs(page)
  await page.getByTestId('jobs-search').fill('install-openbao')
  const badge = page.getByTestId(`jobs-cell-status-${(row as JobRow).id}`)
  await expect(badge).toBeVisible({ timeout: 30_000 })
  await expect(badge, 'the install renders Succeeded').toHaveAttribute('data-status', 'succeeded')
  await expect(badge).toHaveText(/Succeeded/)
})

test('171 Every rendered row maps to a real HelmRelease install / terraform stage (no placeholder, no synthetic/fabricated entry)', async ({ page }) => {
  const feed = await jobsFeed(page, { full: true })
  const hrs = [...(await helmReleases('a')), ...(await helmReleases('b'))]
  const names = new Set(hrs.flatMap((h) => [h.metadata.name, h.metadata.name.replace(/^bp-/, '')]))
  const installs = feed.filter((j) => (j.kind ?? '') === 'install')
  expect(installs.length, 'the feed holds install rows').toBeGreaterThan(0)
  const orphans = installs.filter((j) => !names.has(installTarget(j))).map((j) => j.jobName)
  expect(orphans, 'install rows without a HelmRelease in either region').toEqual([])
  for (const j of feed) {
    expect(j.jobName, `row ${j.id}: a name`).not.toBe('')
    expect(JOB_STATUSES, `row ${j.jobName}: a status of the vocabulary`).toContain(j.status)
    expect(j.kind ?? '', `row ${j.jobName}: a kind`).not.toBe('')
  }
  await openJobs(page)
  const finite = await jobsFeed(page)
  const rendered = await page.locator('[data-testid^="jobs-table-row-"]').evaluateAll((els) => els.map((e) => (e.getAttribute('data-testid') ?? '').replace(/^jobs-table-row-/, '')))
  const known = new Set(finite.map((j) => j.id))
  expect(rendered.filter((id) => !known.has(id)), 'rendered rows that the feed does not hold').toEqual([])
})

test.describe.serial('a failing task row', () => {
  test.describe.configure({ timeout: 300_000 })
  const name = 'gate-fail'
  const jobName = `task-${name}`
  let ns = ''
  let firstUid = ''

  test.beforeAll(async () => {
    ns = await applyFixtureJob(name, 60)
    await until(`the fixture Job ${ns}/${name} failed`, async () => {
      const j = await getObj('a', 'jobs.batch', ns, name)
      return j && cond(j, 'Failed') === 'True' ? true : null
    }, 180_000)
    firstUid = (await getObj('a', 'jobs.batch', ns, name))?.metadata.uid ?? ''
  })

  test.afterAll(async () => {
    if (ns) await kube('a', ['delete', 'jobs.batch', '-n', ns, '-l', `openova.io/gate-fixture=${name}`, '--ignore-not-found', '--wait=false'])
  })

  async function fixtureRow(page: Page): Promise<JobRow> {
    return until(`the feed shows ${jobName} failed`, async () => {
      const j = (await jobsFeed(page)).find((x) => x.jobName === jobName)
      return j && j.status === 'failed' ? j : null
    }, 180_000)
  }

  test('172 Set the Status filter to failed → the table shows the genuinely-failing rows, each with an honest failed status (the Status filter works)', async ({ page }) => {
    const mine = await fixtureRow(page)
    const failed = await jobsFeed(page, { status: 'failed' })
    expect(failed.map((j) => j.id), 'the failed rows of the feed hold the failing Job').toContain(mine.id)
    const hrs = new Map((await helmReleases('a')).map((h) => [h.metadata.name.replace(/^bp-/, ''), h]))
    for (const j of failed.filter((x) => x.kind === 'install' && !x.jobName.includes(':'))) {
      const hr = hrs.get(installTarget(j))
      expect(hr, `${j.jobName}: a failed install row has its HelmRelease in region a`).toBeTruthy()
      expect(ready(hr as Obj), `${j.jobName}: failed while its HelmRelease is not Ready`).not.toBe('True')
    }
    await openJobs(page)
    await page.getByTestId('jobs-filter-status').selectOption('failed')
    const rows = page.locator('[data-testid^="jobs-table-row-"]')
    await expect(rowOf(page, mine), 'the failing Job is listed').toBeVisible({ timeout: 60_000 })
    const statuses = await rows.evaluateAll((els) => els.map((e) => e.getAttribute('data-status') ?? ''))
    expect(statuses.length).toBeGreaterThan(0)
    expect(statuses.filter((s) => s !== 'failed'), 'rows of another status under the failed filter').toEqual([])
    await expect(page.getByTestId(`jobs-cell-status-${mine.id}`)).toHaveText(/Failed/)
  })

  test('174 On a Failed row (Status=failed), a Re-run / Retry-reconcile button is present on the row (visible on the row or on hover)', async ({ page }) => {
    const mine = await fixtureRow(page)
    await openJobs(page)
    const row = rowOf(page, mine)
    await expect(row).toBeVisible({ timeout: 60_000 })
    await row.hover()
    const btn = row.getByTestId(`jobs-retry-${jobName}`)
    await expect(btn, 'the Re-run button of the failed row').toBeVisible()
    await expect(btn).toBeEnabled()
    await expect(btn).toHaveText(/Re-run|Retry/)
    for (const id of await page.locator('[data-testid^="jobs-table-row-"][data-status="failed"]').evaluateAll((els) => els.map((e) => e.getAttribute('data-testid') ?? ''))) {
      await expect(page.getByTestId(id).locator('button.jobs-retry-btn'), `${id}: a Re-run button`).toHaveCount(1)
    }
  })

  test('175 On a Succeeded / healthy / Confirming row, NO Re-run button renders — the control is gated to Failed rows only', async ({ page }) => {
    await fixtureRow(page)
    await openJobs(page)
    const calm = page.locator('[data-testid^="jobs-table-row-"]').filter({ has: page.locator('[data-testid^="jobs-cell-status-"][data-status="succeeded"], [data-testid^="jobs-cell-status-"][data-status="healthy"], [data-testid^="jobs-cell-status-"][data-status="confirming"]') })
    const n = await calm.count()
    expect(n, 'Succeeded, healthy or Confirming rows on the page').toBeGreaterThan(0)
    await expect(calm.locator('button.jobs-retry-btn'), 'Re-run buttons on those rows').toHaveCount(0)
    await expect(page.locator('[data-testid^="jobs-table-row-"][data-status="failed"] button.jobs-retry-btn').first(), 'the failed row keeps its button').toBeVisible()
  })

  test('177 Use the same Re-run button on a Failed row — one remediation mechanism across rows, no per-kind UI (the single-table/single… shape)', async ({ page }) => {
    await fixtureRow(page)
    await openJobs(page)
    const retryable = page.locator('[data-testid^="jobs-table-row-"][data-status="failed"], [data-testid^="jobs-table-row-"][data-status="degraded"], [data-testid^="jobs-table-row-"][data-status="failing"]')
    const n = await retryable.count()
    expect(n, 'failed rows on the page').toBeGreaterThan(0)
    const shapes = new Set<string>()
    for (let i = 0; i < n; i++) {
      const cell = retryable.nth(i).locator('td.jobs-cell-actions')
      const controls = await cell.locator('button, a, select, input').evaluateAll((els) => els.map((e) => `${e.tagName}.${e.className}`))
      expect(controls, `row ${i}: exactly one control in its Actions cell`).toHaveLength(1)
      shapes.add(controls[0])
    }
    expect([...shapes], 'one control shape on every failed row, of any kind').toEqual(['BUTTON.jobs-retry-btn'])
    expect(await page.getByRole('table').count(), 'one table of jobs').toBe(1)
  })

  test('176 Click Re-run on a Failed row → a success toast/feedback appears and the button flips in place (e.g. Requesting…) — the browser triggered a re-reconcile, no terminal', async ({ page }) => {
    const mine = await fixtureRow(page)
    await openJobs(page)
    const row = rowOf(page, mine)
    await expect(row).toBeVisible({ timeout: 60_000 })
    const posted = page.waitForResponse((r) => r.request().method() === 'POST' && r.url().includes(`/jobs/${encodeURIComponent(jobName)}/retry`), { timeout: 60_000 })
    await row.getByTestId(`jobs-retry-${jobName}`).click()
    const answer = await posted
    expect(answer.status(), 'POST …/retry is accepted').toBeLessThan(300)
    await expect(row.getByTestId(`jobs-retry-done-${jobName}`), 'the button flips in place to the accepted state').toBeVisible({ timeout: 30_000 })
    await expect(row.getByTestId(`jobs-retry-done-${jobName}`)).toHaveText(/✓/)
    await until('the Job was run again (a new object)', async () => {
      const j = await getObj('a', 'jobs.batch', ns, name)
      return j && j.metadata.uid && j.metadata.uid !== firstUid ? true : null
    }, 120_000)
  })
})

test('173 Leave the table on screen ~30s → rows update live (tail) as reconciliation progresses; a status badge changes in place without a manual reload', async ({ page }) => {
  // A converged candidate may change no status for minutes, so the test makes one change of its own while the
  // table is open: a batch Job whose pod no node takes (a node selector no node carries) runs until its
  // deadline fails it. Its row must appear in the open table, its badge must turn from a running status to
  // Failed, the feed must hold the same status, and the document must never reload. The row of an earlier run
  // stays Failed until the new Job is seen, so the test waits for the running status first.
  test.setTimeout(480_000)
  const name = 'gate-live'
  const jobName = `task-${name}`
  await openJobs(page)
  const reads: string[] = []
  page.on('request', (r) => {
    if (/\/api\/v1\/deployments\/[^/]+\/jobs(\/stream)?(\?|$)/.test(r.url())) reads.push(r.url())
  })
  await page.evaluate(() => {
    ;(window as unknown as { __gateMark: string }).__gateMark = 'same-document'
  })
  const navigations = await page.evaluate(() => performance.getEntriesByType('navigation').length)
  let ns = ''
  try {
    ns = await applyFixtureJob(name, 150, { nodeSelector: { 'openova.io/gate-fixture-no-node': name } })
    const row = await until(`the feed shows ${jobName} pending or running`, async () => (await jobsFeed(page)).find((x) => x.jobName === jobName && ['pending', 'running'].includes(x.status)) ?? null, 90_000)
    await page.getByTestId('jobs-search').fill(jobName)
    const badge = page.getByTestId(`jobs-cell-status-${row.id}`)
    await expect(badge, 'the row of the new Job in the open table').toBeVisible({ timeout: 60_000 })
    await expect.poll(async () => (await badge.getAttribute('data-status')) ?? '', { message: 'the badge shows the new Job running before its deadline', timeout: 60_000 }).toMatch(/^(pending|running|confirming)$/)
    await expect(badge, 'the badge turns to Failed in place').toHaveAttribute('data-status', 'failed', { timeout: 300_000 })
    await expect(badge).toHaveText(/Failed/)
    const after = (await jobsFeed(page)).find((x) => x.id === row.id)
    expect(after?.status, 'the feed holds the status the badge shows').toBe('failed')
    const job = await getObj('a', 'jobs.batch', ns, name)
    expect(job && cond(job, 'Failed'), 'the Job failed in the cluster').toBe('True')
    expect(await page.evaluate(() => (window as unknown as { __gateMark?: string }).__gateMark), 'the same document: no reload').toBe('same-document')
    expect(await page.evaluate(() => performance.getEntriesByType('navigation').length)).toBe(navigations)
    expect(reads.length, 'the page read the jobs feed again by itself (poll or stream)').toBeGreaterThan(0)
  } finally {
    if (ns) await kube('a', ['delete', 'jobs.batch', '-n', ns, name, '--ignore-not-found', '--wait=false'])
  }
})

test('190 /jobs lists ONLY finite jobs (provision/cutover steps, batch Jobs, CronJob runs) — ZERO continuous-reconciler rows', async ({ page }) => {
  // The ledger qualified this clause on 2026-07-30: install rows are finite work (#5019) and are allowed.
  const full = await jobsFeed(page, { full: true })
  expect(full.filter((j) => j.kind === 'reconcile' || j.kind === 'reconciler').length, 'the candidate runs continuous reconcilers (the full inventory holds them)').toBeGreaterThan(0)
  const finite = await jobsFeed(page)
  expect(finite.filter((j) => j.kind === 'reconcile' || j.kind === 'reconciler').map((j) => j.jobName), 'continuous-reconciler rows on /jobs').toEqual([])
  await openJobs(page)
  await expect(page.locator('[data-testid^="jobs-cell-kind-"][data-kind="reconcile"], [data-testid^="jobs-cell-kind-"][data-kind="reconciler"]'), 'continuous-reconciler rows rendered').toHaveCount(0)
  for (const j of finite) expect(JOB_STATUSES, `${j.jobName}: a status`).toContain(j.status)
})

test('191 Continuous reconcilers (HelmRelease/Kustomization) do NOT appear in /jobs (they live in the recon lens)', async ({ page }) => {
  // The ledger qualified this clause on 2026-07-30: the HelmRelease half is obsolete (#5019); the Kustomization half stands.
  const ks = await list('a', 'kustomizations.kustomize.toolkit.fluxcd.io')
  expect(ks.length, 'the Kustomizations of region a').toBeGreaterThan(0)
  const finite = await jobsFeed(page)
  const names = new Set(finite.map((j) => j.jobName))
  expect(ks.map((k) => `reconcile-${k.metadata.name}`).filter((n) => names.has(n)), 'Kustomization rows on /jobs').toEqual([])
  const lens = await reconcilers(page)
  const inLens = new Set(lens.filter((r) => r.kind === 'Kustomization').map((r) => `${r.namespace}/${r.name}`))
  expect(ks.map((k) => `${k.metadata.namespace}/${k.metadata.name}`).filter((k) => !inLens.has(k)), 'Kustomizations missing from the recon lens').toEqual([])
})
