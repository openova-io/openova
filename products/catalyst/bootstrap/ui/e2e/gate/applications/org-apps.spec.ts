// Gate check FND-G20 (FND-162): the applications an Organization runs, reached on their own hosts, and the
// hand-over landing. One test per ledger row; the title is the row id and its clause.
import { test, expect, consoleURL, openHost } from '../fixtures.ts'
import { fqdn } from '../env.ts'
import { createSign, randomUUID } from 'node:crypto'
import { promises as dns } from 'node:dns'
import { arr, kube, list, ready, str, type Obj } from './lib.ts'

/**
 * A hand-over URL of this Sovereign for its owner. The token has the claims of handoverjwt.MintToken and is
 * signed with the Sovereign's own signer, which /auth/handover accepts besides the parent's key (#4101); the
 * gate workflow mints the run's sessions with the same signer. The issuer is the one catalyst-api stamps
 * (CATALYST_HANDOVER_JWT_ISSUER, set by the cutover), the owner the one it elevates (sovereign-fqdn orgEmail).
 */
async function ownerHandover(): Promise<{ url: string; owner: string }> {
  const deploy = JSON.parse(await kube('a', ['-n', 'catalyst-system', 'get', 'deploy', 'catalyst-api', '-o', 'json'])) as Obj
  const env = new Map(arr(deploy, 'spec.template.spec.containers').flatMap((c) => arr(c, 'env')).map((e) => [str(e, 'name'), str(e, 'value')]))
  const issuer = env.get('CATALYST_HANDOVER_JWT_ISSUER') ?? ''
  const signer = env.get('CATALYST_HANDOVER_SIGNER_PATH') ?? ''
  expect(issuer, 'catalyst-api names the issuer of its hand-over tokens (CATALYST_HANDOVER_JWT_ISSUER)').toMatch(/^https:\/\//)
  expect(signer, 'catalyst-api names its hand-over signer (CATALYST_HANDOVER_SIGNER_PATH)').toMatch(/^\//)
  const cm = JSON.parse(await kube('a', ['-n', 'catalyst-system', 'get', 'configmap', 'sovereign-fqdn', '-o', 'json'])) as Obj
  const owner = str(cm, 'data.orgEmail').trim()
  expect(owner, 'the owner of the Sovereign (ConfigMap catalyst-system/sovereign-fqdn, key orgEmail)').toMatch(/^[^@\s]+@[^@\s]+$/)
  const key = await kube('a', ['-n', 'catalyst-system', 'exec', 'deploy/catalyst-api', '--', 'cat', signer])
  const now = Math.floor(Date.now() / 1000)
  const part = (v: object) => Buffer.from(JSON.stringify(v)).toString('base64url')
  const signed = `${part({ alg: 'RS256', typ: 'JWT' })}.${part({
    iss: issuer, sub: owner, aud: [new URL(consoleURL('/')).origin], iat: now, exp: now + 300, jti: randomUUID(),
    email: owner, email_verified: true, sovereign_fqdn: fqdn(), deployment_id: str(cm, 'data.selfDeploymentId'), role: 'sovereign-admin',
  })}`
  const token = `${signed}.${createSign('RSA-SHA256').update(signed).sign(key, 'base64url')}`
  return { url: consoleURL(`/auth/handover?token=${encodeURIComponent(token)}`), owner }
}

/** The HelmReleases of Organizations whose name matches, with their namespace. */
async function orgReleases(match: RegExp): Promise<Obj[]> {
  return (await list('a', 'helmreleases.helm.toolkit.fluxcd.io')).filter((h) => match.test(h.metadata.name) && !['flux-system', 'catalyst', 'catalyst-system'].includes(h.metadata.namespace ?? ''))
}

async function hostsIn(ns: string, match: RegExp): Promise<{ host: string; route: Obj }[]> {
  return (await list('a', 'httproutes.gateway.networking.k8s.io', ns)).flatMap((r) => arr(r, 'spec.hostnames').map(String).filter((h) => match.test(h)).map((host) => ({ host, route: r })))
}

function podReady(p: Obj): boolean {
  return str(p, 'status.phase') === 'Running' && arr(p, 'status.containerStatuses').length > 0 && arr(p, 'status.containerStatuses').every((c) => (c as { ready?: boolean }).ready === true)
}

async function resolve(host: string): Promise<string[]> {
  return dns.resolve4(host).catch(() => [])
}

test('115 The Guacamole connections list is NON-EMPTY for a signed-in sovereign-admin: guacamole_connection holds at least one row and the ALL CONNECTIONS list renders it', async ({ page }) => {
  test.setTimeout(240_000)
  const pods = (await list('a', 'pods', 'guacamole')).filter((p) => p.metadata.labels?.['cnpg.io/instanceRole'] === 'primary' || p.metadata.labels?.role === 'primary')
  expect(pods.length, 'the primary of the Guacamole database').toBeGreaterThan(0)
  const count = await kube('a', ['exec', '-n', 'guacamole', pods[0].metadata.name, '-c', 'postgres', '--', 'sh', '-c',
    'for d in $(psql -U postgres -tAc "select datname from pg_database where not datistemplate"); do psql -U postgres -d "$d" -tAc "select count(*) from guacamole_connection" 2>/dev/null && exit 0; done; echo none'])
  expect(Number(count.trim().split('\n')[0]), 'rows of guacamole_connection (a connection, not the right to create one)').toBeGreaterThan(0)
  await page.goto('/')
  const guac = await openHost(page, 'guacamole', '/')
  await expect(guac.getByText(/all connections/i).first()).toBeVisible({ timeout: 60_000 })
  expect(await guac.locator('.all-connections .connection, .all-connections .connection-group .name').count(), 'connections in the list').toBeGreaterThan(0)
})

test('123 Handover URL lands on /dashboard already signed-in as the owner (avatar E), no login form', async ({ browser }) => {
  const { url, owner } = await ownerHandover()
  const ctx = await browser.newContext()
  try {
    const page = await ctx.newPage()
    await page.goto(url)
    await expect(page).toHaveURL(/\/dashboard(\?|$)/, { timeout: 60_000 })
    await expect(page.locator('input[type="password"], input[type="email"]')).toHaveCount(0)
    await expect(page.getByTestId('profile-menu-signin')).toHaveCount(0)
    await expect(page.getByTestId('profile-menu-avatar'), 'the avatar of the owner').toHaveText(owner.charAt(0).toUpperCase())
    await expect(page.getByTestId('profile-menu-avatar'), 'signed in as the owner').toHaveAttribute('title', owner)
  } finally {
    await ctx.close()
  }
})

test('232 On a fresh funnel Org, openclaw /readyz returns 200: bp-openclaw HR Ready=True, controller pod 1/1 Running, https://<openclaw-host>/readyz answers 200', async ({ request }) => {
  const hrs = await orgReleases(/openclaw/)
  expect(hrs.length, 'an Organization runs bp-openclaw').toBeGreaterThan(0)
  const hr = hrs[0]
  const ns = hr.metadata.namespace ?? ''
  expect(ready(hr), `${ns}/${hr.metadata.name} Ready`).toBe('True')
  const ctrl = (await list('a', 'pods', ns)).filter((p) => /openclaw/.test(p.metadata.name) && /controller/.test(p.metadata.name))
  expect(ctrl.length, 'the openclaw controller pod').toBeGreaterThan(0)
  expect(arr(ctrl[0], 'status.containerStatuses'), 'one container').toHaveLength(1)
  expect(podReady(ctrl[0]), 'the controller pod is 1/1 Running').toBe(true)
  const hosts = await hostsIn(ns, /openclaw/)
  expect(hosts.length, 'the openclaw host').toBeGreaterThan(0)
  expect((await request.get(`https://${hosts[0].host}/readyz`)).status()).toBe(200)
})

test('233 On a fresh funnel Org, WordPress serves HTTP 200: the mysql pod is Guaranteed-QoS and Running, the HTTPRoute parents the console gateway, the host resolves to the console-ELB EIP', async ({ request }) => {
  const hrs = await orgReleases(/wordpress/)
  expect(hrs.length, 'an Organization runs WordPress').toBeGreaterThan(0)
  const ns = hrs[0].metadata.namespace ?? ''
  const mysql = (await list('a', 'pods', ns)).filter((p) => /mysql|mariadb/.test(p.metadata.name))
  expect(mysql.length, 'the WordPress database pod').toBeGreaterThan(0)
  expect(str(mysql[0], 'status.qosClass'), 'QoS').toBe('Guaranteed')
  expect(podReady(mysql[0]), 'Running').toBe(true)
  const hosts = await hostsIn(ns, /^wordpress\./)
  expect(hosts.length, 'the WordPress HTTPRoute').toBeGreaterThan(0)
  const { host, route } = hosts[0]
  expect(arr(route, 'spec.parentRefs').map((p) => str(p, 'name')), 'the route parents the console gateway').toContain('cilium-gateway-console')
  const r = await request.get(`https://${host}/`)
  expect(r.status(), `https://${host}/`).toBe(200)
  const consoleIPs = await resolve(`console.${host.slice('wordpress.'.length)}`)
  const wpIPs = await resolve(host)
  expect(consoleIPs.length, 'the console host of the Org resolves').toBeGreaterThan(0)
  expect(wpIPs.filter((ip) => consoleIPs.includes(ip)), `${host} resolves to the console EIP ${consoleIPs.join(', ')}`).toEqual(wpIPs)
  expect(wpIPs.length).toBeGreaterThan(0)
})

test('234 On a funnel Org that provisioned Stalwart mail, mail.<slug>.<pool-tld>/ serves HTTP 200 (SnappyMail); the bp-stalwart-tenant pod Running; the webmail HTTPRoute present (no bare envoy 404)', async ({ request }) => {
  const hrs = await orgReleases(/stalwart/)
  expect(hrs.length, 'an Organization runs bp-stalwart-tenant').toBeGreaterThan(0)
  const ns = hrs[0].metadata.namespace ?? ''
  const pods = (await list('a', 'pods', ns)).filter((p) => /stalwart/.test(p.metadata.name))
  expect(pods.some(podReady), 'a Running bp-stalwart-tenant pod').toBe(true)
  const hosts = await hostsIn(ns, /^mail\./)
  expect(hosts.length, 'the webmail HTTPRoute').toBeGreaterThan(0)
  const r = await request.get(`https://${hosts[0].host}/`)
  expect(r.status(), `https://${hosts[0].host}/`).toBe(200)
  expect(await r.text(), 'the SnappyMail page').toMatch(/snappymail/i)
})
