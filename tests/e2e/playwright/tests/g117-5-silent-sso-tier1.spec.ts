// G117.5 #2744 — Tier-1 silent-SSO end-to-end probe.
//
// For each of the 4 Tier-1 apps (Grafana, Gitea, Harbor, OpenBao) this
// spec walks the no-cookie 4-hop curl chain documented in
// feedback_g113_sso_idr_defaultprovider_fix.md and confirms:
//
//   HOP1: GET https://<app>.<sov-fqdn>/<sso-login-path>
//         → 302/303 to https://auth.<sov-fqdn>/realms/sovereign/protocol/
//           openid-connect/auth?... (client_id=<app>)
//   HOP2: GET HOP1 (with cookie jar)
//         → 303 to https://auth.<sov-fqdn>/realms/sovereign/broker/
//           catalyst-pin/login?session_code=...&client_id=...&tab_id=...
//           (IDR delegated silently to catalyst-pin)
//   HOP3: GET HOP2 (with jar)
//         → 303 to https://api.<sov-fqdn>/oidc/auth?scope=openid+email+
//           profile+groups&state=...
//   HOP4: GET HOP3 (with jar)
//         → 302 to https://console.<sov-fqdn>/login?next=...
//           (catalyst-api validating PIN session before issuing OIDC
//           code; no-cookie → bounce to console login)
//
// The chain proves the realm-config IDR `defaultProvider` binding is
// live AND each per-app OIDC client is registered in the realm with the
// right redirectUri.
//
// Auth: this spec runs ENTIRELY no-cookie. We're not testing the post-
// login app surface; we're testing that the SSO redirect chain reaches
// catalyst-api's /oidc/auth (no KC username/password form interrupts
// the chain). If HOP2 returns 200 (KC username/password form HTML),
// the IDR config didn't take and the test fails loudly.
//
// SKIPPED when SOV_FQDN env is not set — local dev runs and PR-time CI
// don't have a live Sovereign to probe. The chart-tests
// (g117-5-tier1-sso-clients.sh, g117-5-sso-tier1.sh per chart, g115-
// datasources-dashboards.sh) cover the chart-render side; this spec
// covers the live-cluster side once a Sovereign is up.

import { test, expect, type APIRequestContext } from '@playwright/test'

const SOV_FQDN = process.env.SOV_FQDN || ''

// Per-app SSO login entry paths (the URL the operator clicks "Sign in
// with SSO" → app starts the OIDC redirect chain).
const TIER1_APPS = [
  { app: 'grafana',  host: 'grafana',  loginPath: '/login/generic_oauth' },
  { app: 'gitea',    host: 'gitea',    loginPath: '/user/oauth2/openova-sso' },
  // Harbor exposes the registry at https://registry.<fqdn>/ (per
  // bp-harbor cluster overlay gateway.host). The OIDC login path is
  // /c/oidc/login (constant per Harbor 2.x — see OIDCLoginPath in
  // src/common/const.go).
  { app: 'harbor',   host: 'registry', loginPath: '/c/oidc/login' },
  // OpenBao's OIDC entry is POST /v1/auth/oidc/oidc/auth_url returning
  // a JSON body with `data.auth_url` — different shape than the 3
  // browser apps. Tested separately below.
]

async function chainHops(req: APIRequestContext, startURL: string) {
  // Walk a redirect chain manually so we can inspect each hop's
  // status + Location header without Playwright's automatic following.
  const hops: { status: number; location: string | null; url: string }[] = []
  let url = startURL
  for (let i = 0; i < 8; i++) {
    const res = await req.get(url, { maxRedirects: 0, ignoreHTTPSErrors: true, failOnStatusCode: false })
    const loc = res.headers()['location'] || null
    hops.push({ status: res.status(), location: loc, url })
    if (![301, 302, 303, 307, 308].includes(res.status())) break
    if (!loc) break
    url = loc.startsWith('http') ? loc : new URL(loc, url).toString()
  }
  return hops
}

test.describe('G117.5 #2744 Tier-1 silent-SSO 4-hop chain', () => {
  test.skip(!SOV_FQDN, 'SOV_FQDN env not set — skipping live-Sovereign SSO chain probe (chart-render tests cover the wiring side)')

  for (const { app, host, loginPath } of TIER1_APPS) {
    test(`${app}: SSO login chain reaches catalyst-api /oidc/auth without KC login form`, async ({ request }) => {
      const startURL = `https://${host}.${SOV_FQDN}${loginPath}`
      const hops = await chainHops(request, startURL)

      // The first redirect MUST land on KC's auth endpoint with our app's
      // client_id. If it doesn't redirect at all, the app didn't render
      // its SSO endpoint (chart misconfiguration).
      expect(hops.length, `${app} chain should have at least 2 hops, got ${hops.length}`).toBeGreaterThanOrEqual(2)
      const hop0 = hops[0]
      expect(
        [301, 302, 303, 307, 308].includes(hop0.status),
        `${app} HOP0 status=${hop0.status} should redirect to KC, got body instead (chart misconfigured?)`,
      ).toBeTruthy()
      expect(hop0.location, `${app} HOP0 location missing`).toContain(`auth.${SOV_FQDN}`)
      expect(hop0.location!, `${app} HOP0 client_id missing or wrong`).toContain(`client_id=${app}`)

      // Subsequent hops MUST chain through KC realm auth → IDR → catalyst-pin
      // broker → catalyst-api. CRITICAL: NO hop returns HTTP 200 with KC
      // login-form HTML — that's the "IDR config didn't take" failure mode.
      // KC's login form arrives as a 200 from the realm auth endpoint;
      // success means the chain SKIPS that page entirely.
      const realmAuthHop = hops.findIndex((h) => h.url.includes(`auth.${SOV_FQDN}`) && h.url.includes('/protocol/openid-connect/auth'))
      expect(realmAuthHop, `${app} chain did not include a KC realm auth URL: ${JSON.stringify(hops)}`).toBeGreaterThanOrEqual(0)

      // The KC auth hop MUST redirect (303), NOT return 200 with form HTML.
      const realmAuthHopRes = hops[realmAuthHop]
      expect(
        [301, 302, 303, 307, 308].includes(realmAuthHopRes.status),
        `${app} KC realm auth returned ${realmAuthHopRes.status} (NOT a redirect) — IDR defaultProvider config not active. ` +
          `Memory: feedback_g113_sso_idr_defaultprovider_fix.md.`,
      ).toBeTruthy()

      // The KC redirect's Location MUST point at /broker/catalyst-pin/
      // (silent delegation to the catalyst-pin IdP).
      expect(
        realmAuthHopRes.location,
        `${app} KC realm auth did not redirect to /broker/catalyst-pin/ — IDR not delegating`,
      ).toContain('/broker/catalyst-pin/')

      // Eventually the chain reaches catalyst-api's /oidc/auth which on
      // a no-cookie probe bounces back to console /login. That's the
      // expected terminal state for an unauthenticated walk.
      const oidcAuthHop = hops.findIndex((h) => h.url.includes(`api.${SOV_FQDN}`) && h.url.includes('/oidc/auth'))
      expect(oidcAuthHop, `${app} chain never reached catalyst-api /oidc/auth: ${JSON.stringify(hops)}`).toBeGreaterThanOrEqual(0)

      test.info().annotations.push({
        type: 'sso-chain',
        description: `${app}: ${hops.length} hops, terminal status=${hops[hops.length - 1].status}, terminal url=${hops[hops.length - 1].url}`,
      })
    })
  }

  test('openbao: POST /v1/auth/oidc/oidc/auth_url returns a KC-broker URL', async ({ request }) => {
    const res = await request.post(`https://bao.${SOV_FQDN}/v1/auth/oidc/oidc/auth_url`, {
      headers: { 'Content-Type': 'application/json' },
      data: {
        role: 'operator',
        redirect_uri: `https://bao.${SOV_FQDN}/ui/vault/auth/oidc/oidc/callback`,
      },
      ignoreHTTPSErrors: true,
      failOnStatusCode: false,
    })
    expect(res.status(), `OpenBao auth_url returned ${res.status()}; expected 200`).toBe(200)
    const body = await res.json()
    const url = body?.data?.auth_url
    expect(url, `OpenBao auth_url payload missing data.auth_url: ${JSON.stringify(body)}`).toContain(
      `auth.${SOV_FQDN}`,
    )
    expect(url, `OpenBao auth_url client_id mismatch: ${url}`).toContain('client_id=openbao')

    // OpenBao does NOT add kc_idp_hint to the URL (architectural
    // constraint documented in bp-openbao chart values.yaml — silent
    // SSO is delivered by the realm-config IDR `defaultProvider`
    // binding instead). Walk the chain manually starting from the
    // returned auth_url and verify KC still delegates to catalyst-pin.
    const hops = await chainHops(request, url)
    const realmHop = hops.find((h) => h.url.includes(`auth.${SOV_FQDN}`) && h.url.includes('/protocol/openid-connect/auth'))
    expect(realmHop, `OpenBao SSO chain never hit KC realm auth: ${JSON.stringify(hops)}`).toBeTruthy()
    expect(
      [301, 302, 303, 307, 308].includes(realmHop!.status),
      `OpenBao KC realm auth returned ${realmHop!.status} (NOT a redirect) — IDR defaultProvider not active`,
    ).toBeTruthy()
    expect(realmHop!.location, 'OpenBao KC realm auth did not delegate to /broker/catalyst-pin/').toContain(
      '/broker/catalyst-pin/',
    )
  })
})

test.describe('G115 #2744 grafana datasources auto-discovered', () => {
  test.skip(!SOV_FQDN, 'SOV_FQDN env not set — skipping live-Sovereign datasource probe')

  test('grafana /api/datasources returns the LGTM stack datasources', async ({ request }) => {
    // Grafana's /api/datasources is auth-gated. We use the basic-auth
    // admin path (GF_SECURITY_ADMIN_PASSWORD baked into bp-grafana's
    // admin secret) to probe — operators set GRAFANA_ADMIN_PASSWORD
    // for the test. SKIP if not provided.
    const pwd = process.env.GRAFANA_ADMIN_PASSWORD
    test.skip(!pwd, 'GRAFANA_ADMIN_PASSWORD env not set — skipping datasource enumeration')

    const res = await request.get(`https://grafana.${SOV_FQDN}/api/datasources`, {
      headers: {
        Authorization: 'Basic ' + Buffer.from(`admin:${pwd}`).toString('base64'),
      },
      ignoreHTTPSErrors: true,
      failOnStatusCode: false,
    })
    expect(res.status(), `grafana /api/datasources returned ${res.status()}`).toBe(200)
    const datasources = await res.json()
    const names = (datasources as { name: string }[]).map((d) => d.name)
    // Expect at least 3 starter datasources: Prometheus, Loki, Tempo.
    for (const want of ['Prometheus', 'Loki', 'Tempo']) {
      expect(names, `grafana datasource '${want}' missing — sidecar discovery failed`).toContain(want)
    }
  })
})
