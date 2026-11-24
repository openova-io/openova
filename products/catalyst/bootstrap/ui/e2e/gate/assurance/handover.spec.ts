// Gate check FND-G23 (FND-165): the hand-over of the Sovereign to its owner. The owner is the ConfigMap
// catalyst-system/sovereign-fqdn's orgEmail; the hand-over URL is signed with the Sovereign's own signer, as
// FND-162 signs it. One test per ledger row; the title is the row id and its clause.
import { test, expect, consoleURL } from '../fixtures.ts'
import { fqdn } from '../env.ts'
import { expectNoLoginForm, isAt, kube, list, mkt, ownerContext, ownerHandover, type Obj } from './lib.ts'

test('R18 handover-key self-publish guard — the Sovereign does not re-publish its handover key on reconcile.', async () => {
  test.setTimeout(420_000)
  // Measured by write history, not by the absence of a log line: across a window of reconcile ticks the
  // Secret's resourceVersion does not move while a node Lease (renewed every few seconds) does, so the
  // window saw cluster writes and none of them landed on the Secret.
  // kubectl hides an object's write history unless it is asked for (--show-managed-fields).
  const read = async () => {
    const s = JSON.parse(await kube('a', ['-n', 'catalyst-system', 'get', 'secret', 'catalyst-handover-jwt-public', '-o', 'json', '--show-managed-fields'])) as Obj
    expect((s.metadata.managedFields ?? []).length, 'the write history of the Secret catalyst-system/catalyst-handover-jwt-public').toBeGreaterThan(0)
    return s
  }
  const leases = async () => {
    const all = await list('a', 'leases.coordination.k8s.io', 'kube-node-lease')
    expect(all.length, 'the node Leases of region a').toBeGreaterThan(0)
    return new Map(all.map((l) => [l.metadata.name, l.metadata.resourceVersion ?? '']))
  }
  const before = await read()
  const leasesBefore = await leases()
  const startedAt = new Date()
  // Three minutes: three ticks of catalyst-api's reconcile loop, which the hand walk measured at one a minute.
  await new Promise((r) => setTimeout(r, 180_000))
  const after = await read()
  const leasesAfter = await leases()
  expect([...leasesAfter].some(([n, v]) => leasesBefore.has(n) && leasesBefore.get(n) !== v), 'a node Lease was renewed in the window (the cluster was writing)').toBe(true)
  expect(after.metadata.resourceVersion, 'the resourceVersion of the hand-over key Secret').toBe(before.metadata.resourceVersion)
  const late = (after.metadata.managedFields ?? []).filter((f) => f.time && new Date(f.time) >= startedAt)
  expect(late.map((f) => `${f.manager} ${f.operation} ${f.time}`), 'writes to the hand-over key Secret in the window').toEqual([])
  const logs = await kube('a', ['-n', 'catalyst-system', 'logs', 'deploy/catalyst-api', '-c', 'catalyst-api', '--since=4m'], 180_000)
  expect(logs.split('\n').filter((l) => /publish/i.test(l) && /handover/i.test(l)), 'catalyst-api log lines that publish the hand-over key in the window').toEqual([])
})

test('3 The voucher-redeem URL opened in an AUTHED OWNER browser session bounces to /dashboard CLIENT-side and leaves NO redeem form rendered for the owner (the marketplace is a static build with client-side auth — the #4546 contract written for this row).', async ({ browser }) => {
  test.setTimeout(240_000)
  const { ctx, page, owner } = await ownerContext(browser)
  try {
    // Every redeem panel that becomes visible on the storefront is reported, so that a flash of the form
    // before the bounce is seen too (core/marketplace/src/pages/redeem.astro). The watcher reports itself
    // when it starts on a page, so that a watcher that never ran cannot read as "no panel".
    const shown: string[] = []
    const watched: string[] = []
    await page.exposeFunction('__gateRedeemPanel', (id: string) => { if (id.startsWith('watching ')) watched.push(id.slice(9)); else shown.push(id) })
    await ctx.addInitScript(() => {
      const ids = ['redeem-missing', 'redeem-not-valid', 'redeem-ended', 'redeem-throttled', 'redeem-valid']
      const report = (id: string) => (window as unknown as { __gateRedeemPanel: (id: string) => Promise<void> }).__gateRedeemPanel(id)
      const check = () => { for (const id of ids) { const el = document.getElementById(id); if (el && !el.classList.contains('hidden')) void report(id) } }
      new MutationObserver(check).observe(document, { subtree: true, attributes: true, attributeFilter: ['class'], childList: true })
      void report(`watching ${window.location.host}`)
    })
    const res = await page.goto(mkt('/redeem/?code=GATEG23OWNERBOUNCE'))
    expect(res?.status(), 'the storefront serves the redeem page itself (a static page, no server redirect)').toBe(200)
    expect(res?.request().redirectedFrom() ?? null, 'no server redirect before the page').toBeNull()
    await expect.poll(() => isAt(page.url(), 'console', '/dashboard'), { message: 'the owner is bounced to the console /dashboard', timeout: 60_000 }).toBe(true)
    await expect(page.getByTestId('profile-menu-avatar'), 'signed in as the owner').toHaveAttribute('title', owner)
    expect(watched, 'the panel watcher ran on the storefront page').toContain(new URL(mkt('/')).host)
    expect(shown, 'redeem panels rendered for the owner on the way').toEqual([])
  } finally {
    await ctx.close()
  }
})

test('96 Handover URL → lands on /dashboard already signed-in as the owner (avatar E), no login form.', async ({ browser }) => {
  test.setTimeout(180_000)
  // The walk's owner had the initial E; this Sovereign's owner has their own.
  const { ctx, page, owner } = await ownerContext(browser)
  try {
    await expectNoLoginForm(page, 'the dashboard after the hand-over')
    await expect(page.getByTestId('profile-menu-signin')).toHaveCount(0)
    await expect(page.getByTestId('profile-menu-avatar'), 'the avatar of the owner').toHaveText(owner.charAt(0).toUpperCase())
    await expect(page.getByTestId('profile-menu-avatar'), 'signed in as the owner').toHaveAttribute('title', owner)
  } finally {
    await ctx.close()
  }
})

test('178 The signed handover URL → lands directly on /dashboard signed-in (env switcher shows the live env, avatar E, no login form).', async ({ browser, request }) => {
  test.setTimeout(180_000)
  // Directly: the hand-over answers one redirect, to /dashboard. A token is single-use, so the redirect is
  // read with one token and the landing walked with a second.
  const first = await ownerHandover()
  const hop = await request.get(first.url, { maxRedirects: 0 })
  expect(hop.status(), 'GET /auth/handover').toBe(302)
  expect(new URL(hop.headers().location ?? '', consoleURL('/')).href, 'the redirect of the hand-over').toBe(consoleURL('/dashboard'))
  // The environment switcher of the walk is now the Sovereign label of the sidebar (sov-console-org-fqdn,
  // products/catalyst/bootstrap/ui/src/pages/sovereign/SovereignSidebar.tsx): the live environment is this
  // Sovereign's domain.
  const { ctx, page, owner } = await ownerContext(browser)
  try {
    await expect(page.getByTestId('sov-console-org-fqdn'), 'the live environment').toHaveText(fqdn())
    await expect(page.getByTestId('profile-menu-avatar'), 'the avatar of the owner').toHaveText(owner.charAt(0).toUpperCase())
    await expect(page.getByTestId('profile-menu-avatar'), 'signed in as the owner').toHaveAttribute('title', owner)
    await expectNoLoginForm(page, 'the dashboard after the hand-over')
    expect(isAt(page.url(), 'console', '/dashboard'), `still on /dashboard (${page.url()})`).toBe(true)
  } finally {
    await ctx.close()
  }
})
