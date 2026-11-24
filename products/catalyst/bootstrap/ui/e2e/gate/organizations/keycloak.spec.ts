// Gate check FND-G22 (FND-164): the identity provider of the Sovereign, its realm sovereign and its admin
// console. The realm is read with kcadm.sh in the identity provider's own Pod (lib.ts kcadm), as the gate
// workflow reads it; the admin console is opened signed in as the owner. One test per ledger row; the title
// is the row id and its clause. Nothing here writes to the Sovereign.
import { test, expect } from '../fixtures.ts'
import { asOwner, expectNoLoginForm, host, kcadm, ownerEmail, realmUser, type KcUser } from './lib.ts'

/** The run's own identities and the sign-in check's user, which the gate workflow adds to the realm for the run. */
const RUN_IDENTITY = /^gate-\d+(-(run|second|viewer))?@/i

async function groupId(path: string): Promise<string> {
  const name = path.replace(/^\//, '')
  const found = (await kcadm<Array<{ id: string; path: string }>>(['get', 'groups', '-r', 'sovereign', '-q', `search=${name}`])).filter((g) => g.path === path)
  expect(found.map((g) => g.path), `the group ${path} of the realm sovereign`).toEqual([path])
  return found[0].id
}

test('34 Keycloak admin console for the sovereign realm → lands inside the admin console (realm overview / Users / Clients), no master-realm login.', async ({ browser }) => {
  test.setTimeout(240_000)
  await asOwner(browser, async (page) => {
    const visited: string[] = []
    page.on('framenavigated', (f) => { if (f === page.mainFrame()) visited.push(f.url()) })
    await page.goto(host('auth', '/admin/sovereign/console/'))
    await expect(page).toHaveURL(/\/admin\/sovereign\/console\//, { timeout: 90_000 })
    for (const name of ['Users', 'Clients']) await expect(page.getByRole('link', { name, exact: true }).first(), `the ${name} section`).toBeVisible({ timeout: 90_000 })
    await expectNoLoginForm(page, 'the admin console')
    expect(visited.filter((u) => u.includes('/realms/master/')), 'no master-realm login on the way').toEqual([])
  })
})

test('41 Keycloak sovereign realm → Users lists the single owner principal the owner (enabled).', async () => {
  const owner = await ownerEmail()
  const u = await realmUser(owner)
  expect(u.enabled, `${owner} is enabled`).toBe(true)
  // The one principal with the administrator group, besides the identities this gate run added itself.
  const members = await kcadm<KcUser[]>(['get', `groups/${await groupId('/sovereign-admins')}/members`, '-r', 'sovereign', '--limit', '1000'])
  const principals = members.map((m) => (m.email ?? m.username).toLowerCase()).filter((e) => !RUN_IDENTITY.test(e))
  expect(principals, 'the owner principals of the realm').toEqual([owner.toLowerCase()])
})

test('42 Owner user → Groups tab shows membership in /sovereign-admins (alongside /openova-users) — the source of admin authority.', async () => {
  const u = await realmUser(await ownerEmail())
  const groups = (await kcadm<Array<{ path: string }>>(['get', `users/${u.id}/groups`, '-r', 'sovereign'])).map((g) => g.path)
  expect(groups).toEqual(expect.arrayContaining(['/sovereign-admins', '/openova-users']))
})

test('43 Owner user → Role mapping tab: effective realm roles include catalyst-admin (not only default-roles/uma/o…', async () => {
  const u = await realmUser(await ownerEmail())
  const roles = (await kcadm<Array<{ name: string }>>(['get', `users/${u.id}/role-mappings/realm/composite`, '-r', 'sovereign'])).map((r) => r.name)
  expect(roles, 'the effective realm roles of the owner').toContain('catalyst-admin')
})

test('44 Groups → /sovereign-admins → Role mapping: group confers catalyst-admin (console) and realm-management realm-admin (KC console) — one source, both grants.', async () => {
  const m = await kcadm<{ realmMappings?: Array<{ name: string }>; clientMappings?: Record<string, { mappings?: Array<{ name: string }> }> }>(
    ['get', `groups/${await groupId('/sovereign-admins')}/role-mappings`, '-r', 'sovereign'])
  expect((m.realmMappings ?? []).map((r) => r.name), 'the realm roles of /sovereign-admins').toContain('catalyst-admin')
  expect((m.clientMappings?.['realm-management']?.mappings ?? []).map((r) => r.name), 'its realm-management roles').toContain('realm-admin')
})
