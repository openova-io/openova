// Gate check FND-G22 (FND-164): who the console says is signed in, its sidebar, its Users page and its
// Settings page. A row that names the owner is walked signed in as the owner (asOwner); the others as the
// run's identity. One test per ledger row; the title is the row id and its clause. Nothing here writes to
// the Sovereign.
import { test, expect, consoleURL } from '../fixtures.ts'
import type { Page } from '@playwright/test'
import { BANNED_WORDS, arr, asOwner, expectNoLoginForm, host, kcadm, list, ownerEmail, realmUser, str, until, whoami, type Obj } from './lib.ts'
import { fqdn } from '../env.ts'

/** Lands on the bare console address and waits for the Dashboard. */
async function landBare(page: Page): Promise<void> {
  await page.goto(consoleURL('/'))
  await expect(page).toHaveURL(/\/dashboard(\?|$)/, { timeout: 60_000 })
  await expect(page.getByTestId('dashboard-page')).toBeVisible({ timeout: 60_000 })
}

/** The console identity of a landed page: no sign-in control, the avatar names the address. */
async function expectSignedInAs(page: Page, email: string): Promise<void> {
  await expectNoLoginForm(page, page.url())
  await expect(page.getByTestId('profile-menu-signin'), 'no Sign in control').toHaveCount(0)
  await expect(page.getByTestId('profile-menu-avatar'), `signed in as ${email}`).toHaveAttribute('title', email, { timeout: 30_000 })
  expect((await whoami(page)).email, 'GET /api/v1/whoami').toBe(email)
}

async function openAccountMenu(page: Page): Promise<void> {
  await page.getByTestId('profile-menu-avatar').click()
  await expect(page.getByTestId('profile-menu-dropdown')).toBeVisible()
}

/** The UserAccess object the console seeded for the owner (annotation catalyst.openova.io/user-email). */
async function ownerAccess(owner: string): Promise<Obj> {
  const mine = (await list('a', 'useraccesses.access.openova.io')).filter((u) => u.metadata.annotations?.['catalyst.openova.io/user-email'] === owner)
  expect(mine.map((u) => u.metadata.name), `the UserAccess of the owner ${owner}`).toHaveLength(1)
  return mine[0]
}

async function openUsers(page: Page): Promise<void> {
  await page.goto(consoleURL('/users'))
  await expect(page.getByTestId('user-access-table'), 'the Users page lists access').toBeVisible({ timeout: 60_000 })
}

test('1 Console bare URL → lands /dashboard signed-in as the owner (no PIN/login form).', async ({ browser }) => {
  await asOwner(browser, async (page, owner) => {
    await landBare(page)
    await expectSignedInAs(page, owner)
  })
})

test('2 Full sidebar renders (Dashboard / Cloud / Apps / Catalog / Agenity / Jobs / Compliance / Users / Organizations / Settings).', async ({ page }) => {
  await landBare(page)
  const nav = page.getByTestId('sov-console-nav')
  // The Cloud entry is labelled Resources since the cloud view became the one Resources view.
  const want: Array<[string, string, string]> = [['dashboard', 'Dashboard', '/dashboard'], ['cloud', 'Resources', '/cloud'], ['apps', 'Apps', '/apps'],
    ['catalog', 'Catalog', '/catalog'], ['jobs', 'Jobs', '/jobs'], ['compliance', 'Compliance', '/sre/compliance'], ['users', 'Users', '/users'],
    ['organizations', 'Organizations', '/organizations'], ['settings', 'Settings', '/settings']]
  for (const [id, label, to] of want) {
    const item = nav.getByTestId(`sov-console-nav-${id}`)
    await expect(item, id).toBeVisible()
    await expect(item, id).toContainText(label)
    expect(new URL((await item.getAttribute('href')) ?? '', consoleURL('/')).pathname, `${id} opens ${to}`).toBe(to)
  }
  // Agenity is the per-Org workspace Blueprint; the sidebar carries it as the Blueprint's own entry.
  await expect(nav.locator('[data-testid^="sov-console-nav-bp-"]').filter({ hasText: 'Agenity' }), 'the Agenity entry').toHaveCount(1)
})

test('26 Console bare URL → lands on the dashboard signed-in as the owner; no PIN/login/"Sign in with…" button.', async ({ browser }) => {
  await asOwner(browser, async (page, owner) => {
    await landBare(page)
    await expectSignedInAs(page, owner)
    await expect(page.getByText(/Sign in with/i), 'no "Sign in with…" control').toHaveCount(0)
  })
})

test('27 Avatar (top-right) menu reads "Signed in as the owner" with a Sign-out item.', async ({ browser }) => {
  await asOwner(browser, async (page, owner) => {
    await landBare(page)
    await openAccountMenu(page)
    await expect(page.getByTestId('profile-menu-dropdown')).toContainText('Signed in as')
    await expect(page.getByTestId('profile-menu-email')).toHaveText(owner)
    await expect(page.getByTestId('profile-menu-signout')).toHaveText(/Sign out/)
  })
})

test('28 Users page renders the pre-seeded owner row the owner (tier=owner UserAccess CR), signed-in admin.', async ({ page }) => {
  const owner = await ownerEmail()
  const access = await ownerAccess(owner)
  expect(str(access, 'spec.user.keycloakSubject'), 'the subject of the owner access').toBe(owner)
  // The owner tier maps to the admin role on every application (handler/user_access_owner_seed.go).
  expect(arr(access, 'spec.applications').map((a) => `${str(a, 'app')}:${str(a, 'role')}`), 'its grant').toContain('*:admin')
  expect((await whoami(page)).tier, 'the signed-in identity is an admin').toMatch(/^(owner|admin)$/)
  await openUsers(page)
  await expect(page.getByTestId(`user-access-row-${access.metadata.name}`), 'the owner row').toContainText(owner)
})

test('29 Re-open the bare console URL after the session TTL → lands signed-in again, no PIN re-prompt.', async ({ browser }) => {
  test.setTimeout(300_000)
  const ttl = 90
  await asOwner(browser, async (page, owner) => {
    // The first sign-on through the identity provider (the Grafana door), as a User's first visit makes it.
    const g = await page.context().newPage()
    await g.goto(host('grafana', '/'))
    await until('Grafana signed in', async () => ((await g.request.get(host('grafana', '/api/user'))).status() === 200 ? true : null), 120_000, 3_000)
    const expires = Date.now() + ttl * 1000
    await new Promise((r) => setTimeout(r, Math.max(0, expires - Date.now()) + 10_000))
    expect((await page.request.get(consoleURL('/api/v1/whoami'))).status(), 'the console session has expired').toBe(401)
    await landBare(page)
    await expectSignedInAs(page, owner)
  }, ttl)
})

test('45 Console Users panel: owner row + the ability to view/manage users renders — proving console admin nav is driven by the realm principal, not a self-signed constant.', async ({ page }) => {
  const owner = await ownerEmail()
  const access = await ownerAccess(owner)
  // The row names the principal of the realm that holds the admin group.
  const principal = await realmUser(owner)
  const groups = (await kcadm<Array<{ path: string }>>(['get', `users/${principal.id}/groups`, '-r', 'sovereign'])).map((g) => g.path)
  expect(groups, `the realm principal ${owner}`).toContain('/sovereign-admins')
  await openUsers(page)
  expect(str(access, 'spec.user.keycloakSubject'), 'the owner row is that principal').toBe(principal.email ?? '')
  await expect(page.getByTestId(`user-access-row-${access.metadata.name}`)).toContainText(owner)
  await expect(page.getByTestId('user-access-new-cta'), 'the control that grants access').toBeVisible()
  await expect(page.getByTestId(`user-access-delete-${access.metadata.name}`), 'the control that revokes it').toBeVisible()
})

test('109 (a) the console /settings profile renders the owner with no second login; (b) reaching the Keycloak account console directly leaves the User on a working surface rather than stranded — both SPA entry paths 302 in ONE hop to /settings, which renders the owner\'s profile at 200, while the account REST surface keeps answering; and NO console navigation links a User to it.', async ({ browser, request }) => {
  await asOwner(browser, async (page, owner) => {
    const r = await page.goto(consoleURL('/settings'))
    expect(r?.status(), 'GET /settings').toBe(200)
    await expect(page).toHaveURL(/\/settings(\?|#|$)/, { timeout: 60_000 })
    await expect(page.getByTestId('settings-page')).toBeVisible({ timeout: 60_000 })
    await expectSignedInAs(page, owner)
    for (const path of ['/realms/sovereign/account', '/realms/sovereign/account/']) {
      const hop = await request.get(host('auth', path), { maxRedirects: 0 })
      expect(hop.status(), `GET auth${path}`).toBe(302)
      const to = new URL(hop.headers().location ?? '', host('auth', '/'))
      expect(`${to.hostname}${to.pathname}`, `auth${path} sends to`).toBe(`console.${fqdn()}/settings`)
      await page.goto(host('auth', path))
      await expect(page).toHaveURL(new RegExp(`^https?://console\\.${fqdn().replace(/\./g, '\\.')}(:443)?/settings`), { timeout: 60_000 })
      await expect(page.getByTestId('settings-page')).toBeVisible({ timeout: 60_000 })
    }
    // The account REST surface is Keycloak's account REST service: a JSON request below /realms/<realm>/account/
    // (AccountLoader) that carries no bearer is answered 401 "Bearer token required". The redirect would answer
    // 302, a removed surface 404, and the HTML account console a redirect to its sign-in.
    const rest = await request.get(host('auth', '/realms/sovereign/account/credentials'), { headers: { Accept: 'application/json' }, maxRedirects: 0 })
    expect(rest.status(), 'the account REST surface answers a request without a bearer').toBe(401)
    for (const path of ['/dashboard', '/settings']) {
      await page.goto(consoleURL(path))
      await expect(page.getByTestId('sov-console-nav')).toBeVisible({ timeout: 60_000 })
      await expect(page.locator('a[href*="/realms/"][href*="/account"]'), `${path}: no link to the account console`).toHaveCount(0)
    }
  })
})

test('118 Left-nav sidebar label for this section reads "Organizations", not "Tenants"/"SME".', async ({ page }) => {
  await landBare(page)
  const item = page.getByTestId('sov-console-nav-organizations')
  await expect(item).toHaveText(/^\s*Organizations\s*$/)
  expect(new URL((await item.getAttribute('href')) ?? '', consoleURL('/')).pathname).toBe('/organizations')
  expect(await page.getByTestId('sov-console-nav').innerText(), 'no banned word in the sidebar').not.toMatch(BANNED_WORDS)
})

test('179 Click the avatar (top-right) → menu reads "Signed in as the owner" with a Sign-out item — confirms the landed identity is the owner-admin.', async ({ browser }) => {
  await asOwner(browser, async (page, owner) => {
    await landBare(page)
    await openAccountMenu(page)
    await expect(page.getByTestId('profile-menu-dropdown')).toContainText('Signed in as')
    await expect(page.getByTestId('profile-menu-email')).toHaveText(owner)
    await expect(page.getByTestId('profile-menu-signout')).toBeVisible()
    // The owner-admin: the realm principal of that address holds the sovereign-admins group.
    const groups = (await kcadm<Array<{ path: string }>>(['get', `users/${(await realmUser(owner)).id}/groups`, '-r', 'sovereign'])).map((g) => g.path)
    expect(groups, `the groups of ${owner}`).toContain('/sovereign-admins')
  })
})
