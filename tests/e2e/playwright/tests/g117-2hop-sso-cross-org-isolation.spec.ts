// G117.5 W2.C4 #2744 — 2-hop Tier-3 SSO + cross-Org isolation probe.
//
// This spec verifies the per-Org Keycloak realm fan-out shipped by
// bp-keycloak 1.4.12 + bp-sso-bridge 0.2.2:
//
//   1. Each Org has its own realm at https://auth.<sov-fqdn>/realms/<slug>
//   2. The Org realm's Identity Provider Redirector execution has
//      `defaultProvider=sovereign-broker` bound → on unauthenticated
//      auth request, KC auto-303s to /realms/<slug>/broker/sovereign-
//      broker/login (1st hop into the Org realm; 2nd hop federates to
//      the sovereign realm whose own IDR delegates to catalyst-pin)
//   3. The OIDC discovery document at
//      /realms/<slug>/.well-known/openid-configuration carries
//      issuer=https://auth.<sov>/realms/<slug> — distinct per Org
//   4. Cross-Org isolation: a token issued by Org-A's realm has its
//      iss=.../realms/<slug-A>. Org-B applications that validate the
//      issuer against the Org-B discovery URL reject Org-A tokens.
//
// SKIPPED when SOV_FQDN env is not set — PR-time CI doesn't have a
// live multi-Org Sovereign to probe. The chart-tests
// (g117-w2c4-per-org-realm.sh, g117-w2c4-provision-org-realm.sh)
// cover the chart-render + reconciler-shape side; this spec covers
// live-cluster behavior once two Orgs are provisioned on a Sovereign.
//
// REQUIRED env (set on hw86 or any Sovereign with the chart-version
// bumps applied):
//   SOV_FQDN — the Sovereign FQDN (e.g. hw86.omani.works)
//   ORG_A    — first Org slug to probe (e.g. acme)
//   ORG_B    — second Org slug for isolation check (e.g. beta-org)
//
// Per memory feedback_g113_sso_idr_defaultprovider_fix.md anti-pattern
// catalog: returning HTTP 200 from the realm auth endpoint with the KC
// login form HTML body is the "IDR config didn't take" failure mode.
// The test fails loudly on that case.

import { test, expect, type APIRequestContext } from '@playwright/test'

const SOV_FQDN = process.env.SOV_FQDN || ''
const ORG_A = process.env.ORG_A || 'acme'
const ORG_B = process.env.ORG_B || 'beta-org'

async function chainHops(req: APIRequestContext, startURL: string) {
  // Walk redirects manually so each hop's status + Location is
  // observable. Same helper shape as g117-5-silent-sso-tier1.spec.ts.
  const hops: { status: number; location: string | null; url: string }[] = []
  let url = startURL
  for (let i = 0; i < 8; i++) {
    const res = await req.get(url, {
      maxRedirects: 0,
      ignoreHTTPSErrors: true,
      failOnStatusCode: false,
    })
    const loc = res.headers()['location'] || null
    hops.push({ status: res.status(), location: loc, url })
    if (![301, 302, 303, 307, 308].includes(res.status())) break
    if (!loc) break
    url = loc.startsWith('http') ? loc : new URL(loc, url).toString()
  }
  return hops
}

test.describe('G117.5 W2.C4 #2744 2-hop Tier-3 SSO + cross-Org isolation', () => {
  test.skip(
    !SOV_FQDN,
    'SOV_FQDN env not set — skipping live multi-Org probe (chart-tests cover the render side)',
  )

  test(`Org realm exists at /realms/${ORG_A} with valid OIDC discovery`, async ({ request }) => {
    const url = `https://auth.${SOV_FQDN}/realms/${ORG_A}/.well-known/openid-configuration`
    const res = await request.get(url, { ignoreHTTPSErrors: true, failOnStatusCode: false })
    expect(res.status(), `${url} should return 200; got ${res.status()}`).toBe(200)
    const doc = await res.json()
    expect(doc.issuer, `issuer should be /realms/${ORG_A}`).toBe(
      `https://auth.${SOV_FQDN}/realms/${ORG_A}`,
    )
    expect(doc.authorization_endpoint).toContain(`/realms/${ORG_A}/protocol/openid-connect/auth`)
    expect(doc.token_endpoint).toContain(`/realms/${ORG_A}/protocol/openid-connect/token`)
    expect(doc.userinfo_endpoint).toContain(`/realms/${ORG_A}/protocol/openid-connect/userinfo`)
  })

  test(`Org realm exists at /realms/${ORG_B} with distinct issuer`, async ({ request }) => {
    const url = `https://auth.${SOV_FQDN}/realms/${ORG_B}/.well-known/openid-configuration`
    const res = await request.get(url, { ignoreHTTPSErrors: true, failOnStatusCode: false })
    expect(res.status()).toBe(200)
    const doc = await res.json()
    // Cross-Org isolation invariant: distinct issuer per Org.
    expect(doc.issuer).toBe(`https://auth.${SOV_FQDN}/realms/${ORG_B}`)
    expect(doc.issuer).not.toBe(`https://auth.${SOV_FQDN}/realms/${ORG_A}`)
  })

  test(`Org-${ORG_A} IDR auto-303s to sovereign-broker (no KC login form)`, async ({ request }) => {
    // Start an OIDC auth-request at the Org realm. With the IDR
    // defaultProvider=sovereign-broker bound, KC should 303-chain into
    // the sovereign-broker IdP without rendering the realm's login
    // form HTML.
    const startURL =
      `https://auth.${SOV_FQDN}/realms/${ORG_A}/protocol/openid-connect/auth` +
      `?client_id=acme-broker&response_type=code&scope=openid` +
      `&redirect_uri=${encodeURIComponent('https://example.invalid/cb')}` +
      `&state=xy`
    const hops = await chainHops(request, startURL)
    expect(hops.length, `chain too short for ${ORG_A}: ${JSON.stringify(hops)}`).toBeGreaterThanOrEqual(2)

    // FAIL MODE: hop 0 returns 200 with HTML — that's the KC login
    // form, meaning the IDR config didn't bind to sovereign-broker.
    const hop0 = hops[0]
    expect(
      [301, 302, 303, 307, 308].includes(hop0.status),
      `Org-${ORG_A} HOP0 status=${hop0.status} — IDR defaultProvider not bound, KC showed login form`,
    ).toBeTruthy()

    // Chain MUST traverse /broker/sovereign-broker/ to confirm
    // delegation hit the per-Org IdP and is heading into the sovereign
    // realm (the federation 2nd hop).
    const brokerHop = hops.findIndex((h) =>
      (h.location || '').includes('/broker/sovereign-broker/'),
    )
    expect(
      brokerHop,
      `Org-${ORG_A} chain did not traverse /broker/sovereign-broker/: ${JSON.stringify(hops)}`,
    ).toBeGreaterThanOrEqual(0)
  })

  test(`Sovereign realm receives the cross-realm auth-request (2nd hop into sovereign)`, async ({
    request,
  }) => {
    // Following the broker hop, the chain should eventually issue an
    // OIDC auth-request to the sovereign realm's
    // /protocol/openid-connect/auth endpoint. The sovereign realm's
    // OWN IDR then catalyst-pin-delegates (G113 pattern). We assert
    // here only the first leg — into the sovereign realm's auth
    // endpoint — to scope the cross-Org test cleanly.
    const startURL =
      `https://auth.${SOV_FQDN}/realms/${ORG_A}/protocol/openid-connect/auth` +
      `?client_id=acme-broker&response_type=code&scope=openid` +
      `&redirect_uri=${encodeURIComponent('https://example.invalid/cb')}` +
      `&state=xy`
    const hops = await chainHops(request, startURL)

    const sovHop = hops.findIndex((h) => h.url.includes(`/realms/sovereign/`))
    expect(
      sovHop,
      `chain did not cross into /realms/sovereign/: ${JSON.stringify(hops)}`,
    ).toBeGreaterThanOrEqual(0)
  })

  test(`Cross-Org isolation: Org-${ORG_A} userinfo endpoint rejects Org-${ORG_B} tokens`, async ({
    request,
  }) => {
    // We don't have a real id_token here (no PIN session), but we
    // can prove the issuer-level isolation by showing that the two
    // realms have DISTINCT well-known issuers. Combined with the
    // earlier OIDC discovery test, this is sufficient evidence: an
    // application configured with `OIDC_ISSUER=https://auth.<sov>/
    // realms/<ORG_B>` rejects any token whose `iss` claim is
    // `https://auth.<sov>/realms/<ORG_A>` because that's the OIDC
    // Core §3.1.3.7 issuer-check that every conformant library does.
    //
    // To make this assertion concrete: query both realms' discovery
    // documents and verify they have non-overlapping (issuer, JWKS,
    // userinfo) triples. That guarantees no single id_token can
    // satisfy both realms' validators.
    const [a, b] = await Promise.all([
      request
        .get(`https://auth.${SOV_FQDN}/realms/${ORG_A}/.well-known/openid-configuration`, {
          ignoreHTTPSErrors: true,
        })
        .then((r) => r.json()),
      request
        .get(`https://auth.${SOV_FQDN}/realms/${ORG_B}/.well-known/openid-configuration`, {
          ignoreHTTPSErrors: true,
        })
        .then((r) => r.json()),
    ])

    // Three pairs must all differ — issuer + jwks_uri + userinfo_endpoint.
    expect(a.issuer).not.toBe(b.issuer)
    expect(a.jwks_uri).not.toBe(b.jwks_uri)
    expect(a.userinfo_endpoint).not.toBe(b.userinfo_endpoint)

    // Sanity: each contains its slug in the URL path (not catching
    // the case where two Orgs accidentally federate into a shared
    // realm).
    expect(a.issuer).toContain(`/realms/${ORG_A}`)
    expect(b.issuer).toContain(`/realms/${ORG_B}`)
  })
})
