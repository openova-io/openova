// Gate check FND-G22 (FND-164): single sign-on into the applications of the Sovereign. Each application is
// opened on its own host (openHost) in the browser context of the owner (asOwner), so the identity
// provider brokers the owner's console session; what the application says about the signed-in user is
// read from its own API in the same context. One test per ledger row; the title is the row id and its
// clause. Nothing here writes to the Sovereign.
import { test, expect, openHost } from '../fixtures.ts'
import type { Page } from '@playwright/test'
import { asOwner, expectNoLoginForm, host, ticksOf, until } from './lib.ts'
import { fqdn } from '../env.ts'

const esc = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')

/** Opens an application on its host and waits until the page settles on that host. */
async function openApp(page: Page, sub: string, path = '/'): Promise<Page> {
  const p = await openHost(page, sub, path)
  await expect(p, `${sub} lands on its own host`).toHaveURL(new RegExp(`^https?://${esc(sub)}\\.${esc(fqdn())}(:443)?/`), { timeout: 120_000 })
  await p.waitForLoadState('networkidle', { timeout: 60_000 }).catch(() => undefined)
  return p
}

/**
 * GET of an application's API in the page's context, read once the sign-on round trip has set the
 * application's session; anything but 200 within 90 seconds fails the test with the last status.
 */
async function appJSON<T>(p: Page, sub: string, path: string): Promise<T> {
  let last = 0
  const body = await until(`GET ${sub}${path} answers 200`, async () => {
    const r = await p.request.get(host(sub, path), { headers: { Accept: 'application/json' } })
    last = r.status()
    return last === 200 ? ((await r.json()) as T) : null
  }, 90_000, 3_000).catch(() => null)
  expect(last, `GET ${sub}${path}`).toBe(200)
  return body as T
}

/** Every value of the page's localStorage, as one string. */
const storage = (p: Page) => p.evaluate(() => JSON.stringify(Object.fromEntries(Object.entries(window.localStorage))))

async function grafanaUser(p: Page): Promise<{ email: string; login: string; isGrafanaAdmin: boolean }> {
  return appJSON(p, 'grafana', '/api/user')
}

async function expectGrafanaHome(p: Page, owner: string): Promise<void> {
  await expectNoLoginForm(p, 'Grafana')
  await expect(p.getByText('Welcome to Grafana').first(), 'the Grafana Home dashboard').toBeVisible({ timeout: 60_000 })
  expect((await grafanaUser(p)).email, 'the Grafana user').toBe(owner)
}

async function giteaUser(p: Page): Promise<{ login: string; email: string; is_admin: boolean }> {
  return appJSON(p, 'gitea', '/api/v1/user')
}

async function harborUser(p: Page): Promise<{ username: string; email: string; sysadmin_flag: boolean }> {
  return appJSON(p, 'registry', '/api/v2.0/users/current')
}

async function openBao(page: Page): Promise<Page> {
  const p = await openApp(page, 'bao', '/ui/')
  // An in-transit "Signing in…" screen may show first; the final screen is the secrets list.
  await expect(p, 'the authenticated OpenBao session').toHaveURL(/\/ui\/vault\/secrets/, { timeout: 120_000 })
  await expect(p).not.toHaveURL(/\/ui\/vault\/auth/)
  await expectNoLoginForm(p, 'OpenBao')
  return p
}

async function newapiUser(p: Page): Promise<{ role?: number; username?: string }> {
  const raw = await p.evaluate(() => window.localStorage.getItem('user'))
  expect(raw, 'newapi holds the signed-in user').toBeTruthy()
  return JSON.parse(raw ?? '{}') as { role?: number; username?: string }
}

async function expectNewapiConsole(p: Page): Promise<void> {
  await expect(p, 'newapi lands on its console').toHaveURL(/\/console(\/|\?|$)/, { timeout: 120_000 })
  await expect(p).not.toHaveURL(/\/setup|\/login/)
  await expectNoLoginForm(p, 'newapi')
  const text = await p.locator('body').innerText()
  for (const bad of [/Unknown OAuth provider/i, /already bound/i, /upstream connect error/i]) expect(text, 'no error on the page').not.toMatch(bad)
}

test('30 Grafana bare URL → lands on Grafana Home, full UI, no login form; left nav shows Administration; user menu = the owner.', async ({ browser }) => {
  await asOwner(browser, async (page, owner) => {
    const g = await openApp(page, 'grafana')
    await expectGrafanaHome(g, owner)
    // Administration is shown to an administrator of the organization or of the server.
    const orgs = await appJSON<Array<{ orgId: number; role: string }>>(g, 'grafana', '/api/user/orgs')
    expect((await grafanaUser(g)).isGrafanaAdmin || orgs.some((o) => o.orgId === 1 && o.role === 'Admin'), 'the owner administers Grafana').toBe(true)
  })
})

test('31 Gitea bare URL → lands on the gitea dashboard titled "emrah.baysal — Dashboard", no login; profile menu exposes Site Administration; stays on :443.', async ({ browser }) => {
  // The walk's owner was emrah.baysal; the title names the Gitea login of this Sovereign's owner.
  await asOwner(browser, async (page, owner) => {
    const g = await openApp(page, 'gitea')
    const u = await giteaUser(g)
    expect([u.email, u.login], 'the Gitea user').toEqual([owner, owner.split('@')[0]])
    await expect(g).toHaveTitle(new RegExp(`^${esc(u.login)} - Dashboard`))
    await expectNoLoginForm(g, 'Gitea')
    expect(u.is_admin, 'Site Administration').toBe(true)
    expect(await g.locator('a[href$="/-/admin"], a[href$="/admin"]').count(), 'the profile menu links Site Administration').toBeGreaterThan(0)
    expect(new URL(g.url()).port, 'no explicit port').toBe('')
  })
})

test('32 Harbor bare URL → lands on /harbor/projects, no login form; user dropdown the owner with Administration menus.', async ({ browser }) => {
  await asOwner(browser, async (page, owner) => {
    const h = await openApp(page, 'registry')
    await expect(h).toHaveURL(/\/harbor\/projects/, { timeout: 120_000 })
    await expectNoLoginForm(h, 'Harbor')
    const u = await harborUser(h)
    expect(u.email, 'the Harbor user').toBe(owner)
    expect(u.sysadmin_flag, 'an administrator').toBe(true)
    await expect(h.getByText('Administration').first(), 'the Administration menus').toBeVisible()
  })
})

test('33 OpenBao bare UI → lands in an authenticated Vault session (Secrets engines / dashboard), NO token-entry form (an in-transit "Signing in…" shim is allowed).', async ({ browser }) => {
  await asOwner(browser, async (page) => {
    const b = await openBao(page)
    await expect(b.getByText(/Secrets engines/i).first(), 'the secrets engines').toBeVisible({ timeout: 60_000 })
  })
})

test('35 Guacamole bare URL → lands on the Guacamole connections list, signed-in; no Tomcat 404, no /guacamole/ login page.', async ({ browser }) => {
  await asOwner(browser, async (page, owner) => {
    const g = await openApp(page, 'guacamole')
    await expect(g.getByText(/all connections/i).first(), 'the connections list').toBeVisible({ timeout: 90_000 })
    expect(await g.locator('body').innerText(), 'no Tomcat error page').not.toMatch(/HTTP Status 404/)
    await expect(g).not.toHaveURL(/\/guacamole\/(#\/)?login/)
    await expectNoLoginForm(g, 'Guacamole')
    expect((await storage(g)).toLowerCase(), 'the Guacamole session is the owner').toContain(owner.toLowerCase())
  })
})

test('36 PowerDNS-Admin bare URL → lands on the dashboard signed-in; no redirect loop, no OAuth error, no Log In page.', async ({ browser }) => {
  await asOwner(browser, async (page) => {
    let hops = 0
    page.context().on('request', (r) => { if (r.isNavigationRequest() && r.url().includes('pdns-admin.')) hops++ })
    const p = await openApp(page, 'pdns-admin')
    await expect(p, 'the PowerDNS-Admin dashboard').toHaveURL(/\/dashboard/, { timeout: 120_000 })
    expect(hops, 'no redirect loop').toBeLessThan(20)
    await expectNoLoginForm(p, 'PowerDNS-Admin')
    expect(await p.locator('a[href$="/logout"]').count(), 'a signed-in session (Logout)').toBeGreaterThan(0)
    expect(await p.locator('body').innerText(), 'no OAuth error').not.toMatch(/oauth.*error|invalid_client|Log In/i)
  })
})

test('37 newapi bare URL (1st hit) → lands on /console signed-in as admin (role 100); no "Unknown OAuth provider", no login page.', async ({ browser }) => {
  await asOwner(browser, async (page) => {
    const n = await openApp(page, 'newapi')
    await expectNewapiConsole(n)
    expect((await newapiUser(n)).role, 'the newapi role').toBe(100)
  })
})

test('38 newapi bare URL (2nd hit, re-entry) → lands on /console again signed-in; NOT an "already bound" / re-link error / /setup wizard.', async ({ browser }) => {
  await asOwner(browser, async (page) => {
    const first = await openApp(page, 'newapi')
    await expectNewapiConsole(first)
    await first.close()
    const again = await openApp(page, 'newapi')
    await expectNewapiConsole(again)
    expect((await newapiUser(again)).role, 'still the admin').toBe(100)
  })
})

test('39 Hubble bare URL → lands on the Hubble UI, authenticated (not an anonymous/unauth view, no login page).', async ({ browser }) => {
  // Anonymously (a browser with no session) the access gate does not serve the UI: the visit ends elsewhere, at sign-in.
  const nobody = await browser.newContext()
  try {
    const p = await nobody.newPage()
    await p.goto(host('hubble', '/'))
    await p.waitForLoadState('networkidle', { timeout: 60_000 }).catch(() => undefined)
    expect(new URL(p.url()).hostname, 'an anonymous visit is sent away from the UI').not.toBe(`hubble.${fqdn()}`)
  } finally {
    await nobody.close()
  }
  await asOwner(browser, async (page) => {
    const h = await openApp(page, 'hubble')
    await expect(h).toHaveTitle(/Hubble/i, { timeout: 60_000 })
    expect(new URL(h.url()).hostname, 'the owner stays on the UI').toBe(`hubble.${fqdn()}`)
    await expectNoLoginForm(h, 'Hubble')
  })
})

test('40 Marketplace bare URL → renders the anonymous storefront (public, by design); confirm no spurious login UI is forced.', async ({ browser }) => {
  const ctx = await browser.newContext()
  try {
    const p = await ctx.newPage()
    const r = await p.goto(host('marketplace', '/'))
    expect(r?.status(), 'GET marketplace/').toBe(200)
    await expect(p).toHaveURL(new RegExp(`^https?://marketplace\\.${esc(fqdn())}/`))
    await expectNoLoginForm(p, 'the storefront')
    await expect(p.locator('header').first(), 'the storefront renders').toBeVisible({ timeout: 60_000 })
  } finally {
    await ctx.close()
  }
})

test('110 Gitea opens already signed in (avatar/menu shows the SSO user), repo list renders — no Gitea login form.', async ({ browser }) => {
  await asOwner(browser, async (page, owner) => {
    const g = await openApp(page, 'gitea')
    expect((await giteaUser(g)).email, 'the SSO user').toBe(owner)
    await expectNoLoginForm(g, 'Gitea')
    const repos = await appJSON<{ data?: Array<{ full_name: string }> }>(g, 'gitea', '/api/v1/repos/search?limit=5')
    expect((repos.data ?? []).length, 'repositories the owner sees').toBeGreaterThan(0)
    await g.goto(host('gitea', '/explore/repos'))
    for (const r of repos.data ?? []) await expect(g.getByText(r.full_name.split('/')[1], { exact: true }).first(), `the repo list shows ${r.full_name}`).toBeVisible({ timeout: 30_000 })
  })
})

test('111 Harbor opens signed in, the projects list renders — no Harbor login form, no gateway error page.', async ({ browser }) => {
  await asOwner(browser, async (page, owner) => {
    const h = await openApp(page, 'registry')
    expect((await harborUser(h)).email).toBe(owner)
    await expectNoLoginForm(h, 'Harbor')
    expect(await h.locator('body').innerText(), 'no gateway error').not.toMatch(/Bad Gateway|Service Unavailable|upstream connect error/i)
    const projects = await appJSON<Array<{ name: string }>>(h, 'registry', '/api/v2.0/projects?page_size=10')
    expect(projects.length, 'the projects').toBeGreaterThan(0)
    for (const p of projects) await expect(h.getByText(p.name, { exact: true }).first(), `the project ${p.name}`).toBeVisible({ timeout: 30_000 })
  })
})

test('112 Grafana opens signed in (no Grafana login), the home dashboard renders.', async ({ browser }) => {
  await asOwner(browser, async (page, owner) => {
    const g = await openApp(page, 'grafana')
    await expectGrafanaHome(g, owner)
    await appJSON(g, 'grafana', '/api/dashboards/home')
  })
})

test('113 The OpenBao UI renders signed in via OIDC — no manual token/unseal prompt blocking the landing.', async ({ browser }) => {
  await asOwner(browser, async (page) => {
    const b = await openBao(page)
    expect(await b.getByText(/unseal/i).count(), 'no unseal prompt').toBe(0)
    // The UI keeps its token under the name of the method that minted it (vault-<method>☃<n>).
    expect(Object.keys(JSON.parse(await storage(b)) as Record<string, string>).some((k) => k.startsWith('vault-oidc')), 'a token minted by OIDC').toBe(true)
  })
})

test('114 newapi opens signed in, its main console renders — no login form, no upstream-connect error.', async ({ browser }) => {
  await asOwner(browser, async (page) => {
    const n = await openApp(page, 'newapi')
    await expectNewapiConsole(n)
    expect((await newapiUser(n)).username ?? '', 'a signed-in newapi user').not.toBe('')
  })
})

test('180 Grafana bare URL → lands on Grafana Home ("Welcome to Grafana", full UI, Profile avatar), ?orgId=1, no login form (SSO landed signed-in).', async ({ browser }) => {
  await asOwner(browser, async (page, owner) => {
    const g = await openApp(page, 'grafana')
    await expect(g).toHaveURL(/[?&]orgId=1/, { timeout: 60_000 })
    await expectGrafanaHome(g, owner)
  })
})

test('181 Harbor (registry) bare URL → lands on /harbor/projects (projects, repos, Administration nav), no login form; user dropdown the owner.', async ({ browser }) => {
  await asOwner(browser, async (page, owner) => {
    const h = await openApp(page, 'registry')
    await expect(h).toHaveURL(/\/harbor\/projects/, { timeout: 120_000 })
    await expectNoLoginForm(h, 'Harbor')
    const u = await harborUser(h)
    expect([u.email, u.sysadmin_flag], 'the owner, an administrator').toEqual([owner, true])
    await expect(h.getByText('Administration').first()).toBeVisible()
  })
})

test('182 Gitea bare URL → lands on the gitea dashboard titled "emrah.baysal - Dashboard - Catalyst Gitea", logged in; URL stays on :443.', async ({ browser }) => {
  // The walk's owner was emrah.baysal; the title names the Gitea login of this Sovereign's owner.
  await asOwner(browser, async (page, owner) => {
    const g = await openApp(page, 'gitea')
    const u = await giteaUser(g)
    expect(u.email).toBe(owner)
    await expect(g).toHaveTitle(`${u.login} - Dashboard - Catalyst Gitea`)
    expect(new URL(g.url()).port, 'no explicit port').toBe('')
  })
})

test('183 OpenBao bare UI → final rendered screen is the authenticated Vault session (/ui/vault/secrets, Secrets Engines with cubbyhole/ + secret/ kv), NO /ui/vault/auth token form.', async ({ browser }) => {
  await asOwner(browser, async (page) => {
    const b = await openBao(page)
    for (const engine of ['cubbyhole/', 'secret/']) await expect(b.getByText(engine, { exact: true }).first(), engine).toBeVisible({ timeout: 60_000 })
  })
})

test('235 On a fresh prov, grafana SSO logs in 3/3 → 200 — the bp-sso-bridge per-tick credential refresh keeps the grafana OIDC client secret in lockstep with Keycloak, so the SSO round-trip completes. PASS = grafana /login/generic_oauth → Keycloak → back signed-in (200, user in the avatar) across 3 consecutive attempts; bp-sso-bridge refreshes the client cred each tick (no stale-secret 401).', async ({ browser }) => {
  test.setTimeout(600_000)
  for (let i = 1; i <= 3; i++) {
    await asOwner(browser, async (page, owner) => {
      const r = await page.goto(host('grafana', '/login/generic_oauth'))
      expect(r?.status(), `attempt ${i}: the round trip ends at 200`).toBe(200)
      await expect(page, `attempt ${i}: back on Grafana`).toHaveURL(new RegExp(`^https?://grafana\\.${esc(fqdn())}/`), { timeout: 120_000 })
      await expect(page).not.toHaveURL(/\/login/)
      expect((await grafanaUser(page)).email, `attempt ${i}: the user`).toBe(owner)
    })
  }
  const ticks = await until('two reconciler ticks for grafana', async () => { const t = await ticksOf('grafana', '5m'); return t.length >= 2 ? t : null }, 180_000, 30_000)
  expect(ticks.length, 'the reconciler re-registered grafana each tick').toBeGreaterThanOrEqual(2)
})
