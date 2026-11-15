// Marketplace — a remembered Organization the server no longer knows (#7922).
//
// hw307, 16 Nov 2026: the Sovereign was wiped and installed again under the
// same marketplace host. A browser that had used the previous installation
// still held that installation's Organization in localStorage
// (org-active-org / org-active-org-slug / org-active-console-host). After
// sign-in — the new installation's `catalyst_session_hint` cookie — the
// storefront jumped to `console.<old-org>.omani.rest`, an Organization that
// no longer existed.
//
// Renders the REAL built page (`npm run build && npm run preview`) in the
// same hermetic shape as the other specs: the one server answer that matters,
// `GET /api/tenant/check-slug/<slug>`, is routed; the old console host is
// routed too, so a navigation there is observable instead of a DNS error.
//
//   gone    — {"available":true}:  stays on the storefront, keys dropped
//   known   — {"available":false}: today's redirect to the remembered console
//   failed  — 503:                 stays on the storefront, keys kept
//
// READ-ONLY against clusters: no kubectl, no chart bumps, no Pod ops.

import { test, expect, type Page } from '@playwright/test'

const OLD_CONSOLE_ORIGIN = 'https://console.oldorg.omani.rest'
const OLD_ORG: Record<string, string> = {
  'org-active-org': 'b3f0c7d2-old-installation',
  'org-active-org-slug': 'oldorg',
  'org-active-console-host': 'console.oldorg.omani.rest',
}
const KEYS = Object.keys(OLD_ORG)

/**
 * The browser as the previous installation left it, plus the session hint the
 * new installation sets at sign-in (a Sovereign-admin session: `v=1`, no Org).
 */
async function browserFromPreviousInstallation(page: Page, baseURL: string): Promise<void> {
  await page.context().addCookies([
    { name: 'catalyst_session_hint', value: encodeURIComponent('v=1'), url: baseURL },
  ])
  // `?new=1` is exempt from the returning-visitor redirect, so the page
  // loads and the old installation's keys can be written.
  await page.goto('/?new=1')
  await page.evaluate((entries) => {
    for (const [k, v] of Object.entries(entries)) localStorage.setItem(k, v)
  }, OLD_ORG)
}

async function routeCheckSlug(page: Page, status: number, body: unknown): Promise<void> {
  await page.route('**/api/tenant/check-slug/oldorg', (route) =>
    route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) }),
  )
}

async function routeOldConsole(page: Page): Promise<void> {
  await page.route(`${OLD_CONSOLE_ORIGIN}/**`, (route) =>
    route.fulfill({ status: 200, contentType: 'text/html', body: '<h1>old installation console</h1>' }),
  )
}

async function storedKeys(page: Page): Promise<string[]> {
  return page.evaluate((keys) => keys.filter((k) => localStorage.getItem(k) !== null), KEYS)
}

/**
 * Load the storefront root and let the server check (if any) answer. Resolves
 * either way: a build that never asks (the defect) must fail on the URL
 * assertion that follows, not on a timeout.
 */
async function loadStorefront(page: Page): Promise<void> {
  const checked = page
    .waitForResponse('**/api/tenant/check-slug/oldorg', { timeout: 5_000 })
    .catch(() => null)
  await page.goto('/')
  await checked
}

test.describe('#7922 — a remembered Organization the server no longer knows', () => {
  test('gone: the visitor stays on the storefront and the old Organization is dropped', async ({ page, baseURL }) => {
    await routeCheckSlug(page, 200, { available: true })
    await routeOldConsole(page)
    await browserFromPreviousInstallation(page, baseURL!)

    await loadStorefront(page)

    // The URL first: on the old console's origin, localStorage reads empty
    // and the key assertion below would pass for the wrong reason.
    expect(page.url(), 'stayed on the storefront').not.toContain('console.oldorg')
    expect(new URL(page.url()).pathname).toBe('/')
    await expect.poll(() => storedKeys(page)).toEqual([])
    await expect(page.getByRole('heading', { name: /Build your cloud Organization/ })).toBeVisible()
  })

  test('CONTROL known: an Organization the server still has keeps today\'s redirect', async ({ page, baseURL }) => {
    await routeCheckSlug(page, 200, { available: false })
    await routeOldConsole(page)
    await browserFromPreviousInstallation(page, baseURL!)

    await page.goto('/')
    await page.waitForURL(`${OLD_CONSOLE_ORIGIN}/dashboard`)
    expect(page.url()).toBe(`${OLD_CONSOLE_ORIGIN}/dashboard`)
  })

  test('CONTROL failed: no answer means no redirect AND no wipe', async ({ page, baseURL }) => {
    await routeCheckSlug(page, 503, { error: 'unavailable' })
    await routeOldConsole(page)
    await browserFromPreviousInstallation(page, baseURL!)

    await loadStorefront(page)
    // Give a wrong branch the time it would need to navigate.
    await page.waitForTimeout(500)

    expect(page.url(), 'stayed on the storefront').not.toContain('console.oldorg')
    expect(new URL(page.url()).pathname).toBe('/')
    expect(await storedKeys(page)).toEqual(KEYS)
  })
})
