// Gate check FND-G23 (FND-165): the sovereignty cutover as the console shows it (Settings → Sovereignty,
// the Jobs page) against its durable record, the ConfigMap catalyst/self-sovereign-cutover-status
// (products/catalyst/bootstrap/api/internal/handler/cutover.go). Rows 159 and 160 describe a Sovereign that is
// still tethered; rows G11 and 166 one whose cutover is complete: on one candidate the rows of the other state
// fail with that state as the reason. One test per ledger row; the title is the row id and its clause.
import { test, expect, consoleURL } from '../fixtures.ts'
import type { Page } from '@playwright/test'
import { CUTOVER_STEPS, apiJSON, cutoverStatus, isAt, list, ownerContext, selfId, str } from './lib.ts'

/** One row of GET /api/v1/deployments/{id}/jobs (products/catalyst/bootstrap/api/internal/jobs/types.go). */
interface JobRow { id: string; jobName: string; type?: string; parentId?: string; status: string }

async function jobsFeed(page: Page): Promise<JobRow[]> {
  const id = await selfId(page)
  const body = await apiJSON<{ jobs?: unknown }>(page, `/api/v1/deployments/${encodeURIComponent(id)}/jobs`)
  expect(Array.isArray(body.jobs), 'the jobs feed answers a list jobs').toBe(true)
  return body.jobs as JobRow[]
}

/** The cutover group row and its step rows of the jobs feed; the eleven steps must all be there. */
async function cutoverJobs(page: Page): Promise<{ group: JobRow; steps: JobRow[] }> {
  const feed = await jobsFeed(page)
  const group = feed.find((j) => j.jobName === 'cutover')
  expect(group, 'the cutover group row of the jobs feed (jobName cutover)').toBeTruthy()
  const steps = feed.filter((j) => j.parentId === (group as JobRow).id)
  expect(steps.map((s) => s.jobName).sort(), 'the step rows of the cutover group').toEqual(CUTOVER_STEPS.map((s) => `cutover-step-${s}`).sort())
  return { group: group as JobRow, steps }
}

/** The job status the console derives from a step's durable result (jobs/activity_bridge.go activityStatusFromResult). */
function statusOf(result: string): string {
  const r = result.trim().toLowerCase()
  if (r === 'success' || r === 'skipped') return 'succeeded'
  if (r === 'failed' || r === 'running') return r
  return 'pending'
}

/** The Jobs page, narrowed to the cutover group's rows by the parent filter. */
async function openCutoverRows(page: Page, groupId: string): Promise<void> {
  await page.goto(consoleURL('/jobs'))
  await expect(page.getByTestId('jobs-table')).toBeVisible({ timeout: 60_000 })
  await page.getByTestId('jobs-filter-parent').selectOption(groupId)
}

test('G11 sovereignty cutover — the 11-step chain runs to completion and the 10-min deny-egress hold at step 08 (egress-block-test) holds green, so cutoverComplete=true is EARNED rather than asserted. ADR-0002 specified eight sequential Jobs; the shipped platform/self-sovereign-cutover/chart runs 11, which is what the live catalyst/self-sovereign-cutover-status ConfigMap enumerates and what rows 159 / 162 / 166 already assert.', async () => {
  const declared = (await list('a', 'configmaps', 'catalyst')).filter((c) => c.metadata.labels?.['app.kubernetes.io/component'] === 'cutover-step')
  expect(declared.map((c) => str(c, 'data.stepName')).sort(), 'the steps the chart declares (cutover-step ConfigMaps)').toEqual([...CUTOVER_STEPS].sort())
  const s = await cutoverStatus()
  expect(s.cutoverComplete, 'cutoverComplete').toBe('true')
  expect(s.failedStep ?? '', 'failedStep').toBe('')
  expect(CUTOVER_STEPS.filter((n) => !['success', 'skipped'].includes(s[`step.${n}.result`] ?? '')).map((n) => `${n}=${s[`step.${n}.result`] ?? ''}`), 'steps that did not complete').toEqual([])
  expect(s['step.egress-block-test.result'], 'step 08, the deny-egress hold, ran and held').toBe('success')
  const held = (Date.parse(s['step.egress-block-test.finishedAt'] ?? '') - Date.parse(s['step.egress-block-test.startedAt'] ?? '')) / 1000
  expect(held, 'seconds the deny-egress hold lasted').toBeGreaterThanOrEqual(600)
  const finished = Date.parse(s.cutoverFinishedAt ?? '')
  expect(Number.isFinite(finished), 'cutoverFinishedAt is a time').toBe(true)
  const lastStep = Math.max(...CUTOVER_STEPS.map((n) => Date.parse(s[`step.${n}.finishedAt`] ?? '')))
  expect(Number.isFinite(lastStep), 'every step has a finishedAt').toBe(true)
  expect(finished, 'cutoverComplete was set after the last step finished, not before').toBeGreaterThanOrEqual(lastStep)
})

test('159 Settings → Sovereignty section renders a "Cluster sovereignty" panel with a "TETHERED" badge + an "Achieve True Sovereignty" cutover CTA (runs the 11-step cutover + 10-min egress-block self-test).', async ({ page }) => {
  const s = await cutoverStatus()
  expect(s.cutoverComplete, 'the candidate is still tethered (the state this row describes)').toBe('false')
  expect(CUTOVER_STEPS.filter((n) => (s[`step.${n}.result`] ?? '') !== ''), 'cutover steps already started (the panel offers the CTA only before the first)').toEqual([])
  await page.goto(consoleURL('/settings'))
  const card = page.getByTestId('settings-sovereignty').getByTestId('sovereignty-card')
  await expect(card).toBeVisible({ timeout: 60_000 })
  await expect(card.getByRole('heading', { name: 'Cluster sovereignty' })).toBeVisible()
  await expect(card).toHaveAttribute('data-cutover-state', 'tethered')
  await expect(card.getByTestId('sovereignty-badge')).toHaveText(/^tethered$/i)
  await expect(card.getByTestId('cutover-start-button')).toHaveText('Achieve True Sovereignty')
  await expect(card.getByTestId('cutover-start-button')).toBeEnabled()
})

test('160 Console nav + Settings sidebar expose a dedicated "Sovereignty" anchor (#sovereignty) that scrolls to + highlights the Cluster-sovereignty panel — the cutover trigger is a first-class surface.', async ({ page }) => {
  await page.goto(consoleURL('/dashboard'))
  const nav = page.getByTestId('sov-console-nav-sovereignty')
  await expect(nav, 'the console nav entry').toHaveText('Sovereignty')
  await expect(nav).toHaveAttribute('href', '/settings#sovereignty')
  await nav.click()
  await expect.poll(() => new URL(page.url()).hash, { message: 'the nav entry lands on the anchor' }).toBe('#sovereignty')
  const panel = page.getByTestId('settings-sovereignty')
  await expect(panel, 'the panel is highlighted').toHaveAttribute('data-anchor-highlighted', 'true')
  await expect(panel).toHaveClass(/\bring-2\b/)
  await expect(panel, 'the panel is scrolled into view').toBeInViewport()
  await expect(panel.getByTestId('sovereignty-card')).toBeVisible()
  // The Settings sidebar's own entry, from the top of the page.
  await page.goto(consoleURL('/settings'))
  const toc = page.getByTestId('settings-toc-sovereignty')
  await expect(toc).toHaveAttribute('href', '#sovereignty')
  await toc.click()
  await expect(panel).toHaveAttribute('data-anchor-highlighted', 'true')
  await expect(panel).toBeInViewport()
})

test('161 Open /jobs (zero-login, signed in as owner) → the canvas table renders a populated activity list (not a spinner, empty state, or login redirect).', async ({ browser }) => {
  const { ctx, page } = await ownerContext(browser)
  try {
    // The feed the page itself reads (GET /api/v1/deployments/{id}/jobs) is what the table must render.
    const fed = page.waitForResponse((r) => r.request().method() === 'GET' && /\/api\/v1\/deployments\/[^/?]+\/jobs(\?|$)/.test(r.url()), { timeout: 60_000 })
    await page.goto(consoleURL('/jobs'))
    const res = await fed
    expect(res.status(), 'the jobs feed of the page').toBe(200)
    const jobs = ((await res.json()) as { jobs?: unknown }).jobs
    expect(Array.isArray(jobs), 'the jobs feed answers a list jobs').toBe(true)
    const feed = jobs as JobRow[]
    expect(feed.length, 'jobs in the feed of the owner').toBeGreaterThan(0)
    expect(isAt(page.url(), 'console', '/jobs'), `no redirect to a sign-in (${page.url()})`).toBe(true)
    await expect(page.getByTestId('jobs-table')).toBeVisible({ timeout: 60_000 })
    await expect(page.getByTestId('jobs-table-empty')).toHaveCount(0)
    await expect(page.getByTestId('jobs-result-count'), 'the table counts the jobs of the feed').toHaveText(new RegExp(`^\\d+/${feed.length}$`))
    const shown = await page.locator('[data-testid^="jobs-table-row-"]').evaluateAll((els) => els.map((e) => (e.getAttribute('data-testid') ?? '').slice('jobs-table-row-'.length)))
    expect(shown.length, 'rows the table renders').toBeGreaterThan(0)
    const ids = new Set(feed.map((j) => j.id))
    expect(shown.filter((id) => !ids.has(id)), 'rendered rows that are not jobs of the feed').toEqual([])
  } finally {
    await ctx.close()
  }
})

test('162 Find the cutover group row and expand it → it renders the 11 cutover-step-* rows (gitea-mirror, harbor-projects, harbor-prewarm, registry-pivot, … vcluster-registry-p… — the 11-step execution tree.', async ({ page }) => {
  // The group row is expanded with the table's parent filter (jobs-filter-parent), which narrows the table
  // to the children of one group.
  const { group, steps } = await cutoverJobs(page)
  await openCutoverRows(page, group.id)
  for (const s of steps) await expect(page.getByTestId(`jobs-table-row-${s.id}`), `the row of ${s.jobName}`).toBeVisible()
  const shown = await page.locator('[data-testid^="jobs-table-row-"]').evaluateAll((els) => els.map((e) => (e.getAttribute('data-testid') ?? '').slice('jobs-table-row-'.length)))
  expect(shown.filter((id) => id !== group.id).sort(), 'the rows under the cutover group').toEqual(steps.map((s) => s.id).sort())
})

test('163 Each cutover-step-* row reads an honest per-step status (Succeeded / Running / Failed / Pending), never a premature green.', async ({ page }) => {
  const { group, steps } = await cutoverJobs(page)
  const s = await cutoverStatus()
  await openCutoverRows(page, group.id)
  for (const n of CUTOVER_STEPS) {
    const row = steps.find((j) => j.jobName === `cutover-step-${n}`) as JobRow
    const want = statusOf(s[`step.${n}.result`] ?? '')
    expect(row.status, `the feed status of ${n} against its durable result`).toBe(want)
    await expect(page.getByTestId(`jobs-cell-status-${row.id}`), `the status the table shows for ${n}`).toHaveAttribute('data-status', want)
  }
})

test('164 The cutover group status reflects its real children (a group with a failed child reads failed, not a fake Succeeded).', async ({ page }) => {
  const { group, steps } = await cutoverJobs(page)
  const children = steps.map((j) => j.status)
  await openCutoverRows(page, group.id)
  const cell = page.getByTestId(`jobs-cell-status-${group.id}`)
  if (children.includes('failed')) {
    expect(group.status, 'a group with a failed child').toBe('failed')
    await expect(cell).toHaveAttribute('data-status', 'failed')
  } else if (children.every((c) => c === 'succeeded')) {
    expect(group.status, 'a group whose children all succeeded').toBe('succeeded')
    await expect(cell).toHaveAttribute('data-status', 'succeeded')
  } else {
    expect(group.status, `a group with unfinished children (${children.join(', ')}) is not green`).not.toBe('succeeded')
    await expect(cell).toHaveAttribute('data-status', group.status)
  }
})

test('165 Every cutover-step-* row on the Jobs page renders its actions cell PRESENT and deliberately EMPTY - each carries data-testid="jobs-cell-actions-empty-<id>" and none carries jobs-retry-<id> - proving the per-row Re-run control is GATED to Failed rather than missing from the table; the gate is pinned in both directions by JobsTable.cutoverRerun.test.tsx (present + labelled "Re-run" at status=failed, absent at succeeded/running/pending).', async ({ page }) => {
  // The gate in both directions: a step that is not failed has the empty actions cell and no Re-run; a step
  // that failed has the Re-run control.
  const { group, steps } = await cutoverJobs(page)
  await openCutoverRows(page, group.id)
  for (const s of steps) {
    if (s.status === 'failed') {
      await expect(page.getByTestId(`jobs-retry-${s.jobName}`), `the Re-run of failed ${s.jobName}`).toHaveText(/Re-run/)
      await expect(page.getByTestId(`jobs-cell-actions-empty-${s.id}`)).toHaveCount(0)
    } else {
      await expect(page.getByTestId(`jobs-cell-actions-empty-${s.id}`), `the empty actions cell of ${s.jobName} (${s.status})`).toHaveText('—')
      await expect(page.getByTestId(`jobs-retry-${s.jobName}`), `no Re-run on ${s.jobName} (${s.status})`).toHaveCount(0)
    }
  }
})

test('166 (After a COMPLETE cutover) the cutover group reads all-11-green on /jobs — every step Succeeded, including egress-block-test and registry-pivot.', async ({ page }) => {
  const s = await cutoverStatus()
  expect(s.cutoverComplete, 'the cutover of the candidate is complete (the state this row describes)').toBe('true')
  const { group, steps } = await cutoverJobs(page)
  expect(group.status, 'the cutover group').toBe('succeeded')
  expect(steps.filter((j) => j.status !== 'succeeded').map((j) => `${j.jobName}=${j.status}`), 'steps that are not Succeeded').toEqual([])
  await openCutoverRows(page, group.id)
  await expect(page.getByTestId(`jobs-cell-status-${group.id}`)).toHaveAttribute('data-status', 'succeeded')
  for (const j of steps) await expect(page.getByTestId(`jobs-cell-status-${j.id}`), j.jobName).toHaveAttribute('data-status', 'succeeded')
})
