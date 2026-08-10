/**
 * resolveSovereignFQDN — the AUTHORITATIVE Sovereign FQDN for auth URLs.
 *
 * #5895 / UAT row 29 (#3374). `detectMode()` derives the Sovereign FQDN by
 * stripping one leading `console.` label. That is correct for the Sovereign
 * console and WRONG for a per-Organization console, because a per-Org console
 * satisfies the `console.*` pattern without being a Sovereign:
 *
 *   console.hw293.omantel.biz    -> hw293.omantel.biz    (Sovereign, correct)
 *   console.walkone.omani.homes  -> walkone.omani.homes  (an ORG, wrong)
 *
 * The derived value feeds `buildOIDCEndpoints()` (oidc.ts:56) →
 * `https://auth.${sovereignFQDN}/realms/sovereign`, so on a per-Org console the
 * silent `prompt=none` re-auth navigates to `auth.<orgslug>.<parent>`. A walker
 * measured **404 on 10/10 parallel probes** there, with a VALID wildcard cert
 * and realm cookies preserved — which is precisely the signature of a routing
 * gap rather than a TLS or session one: the per-Org Gateway carries a listener
 * for `*.<slug>.<parent>` (org_console_tls.go:257) so TLS terminates against the
 * wildcard, but the only HTTPRoute minted for an Org is `console.<slug>.<parent>`
 * (tenant_route.go:147). Nothing serves `auth.<slug>.<parent>`, and nothing
 * should: there is no per-Org IdP by design. gitops.go:289-307 says so directly —
 * "the per-Org `keycloak.<slug>.<parent>` host is NXDOMAIN there ... The shared
 * realm at auth.<fqdn> is the SAME issuer the console + every other Sovereign
 * app resolves".
 *
 * The two host shapes are INDISTINGUISHABLE by inspection — `console.` plus
 * three labels either way — so the answer cannot be derived, it has to be asked
 * for. GET /api/v1/sovereign/self is the right source and the only one that
 * works on this path: it is unauthenticated by design (sovereign_self.go:24-28,
 * "carries no secrets, only public identifiers ... Bypassing the session gate
 * keeps the SovereignConsoleRedirect helper usable on the very first browser hit
 * before login") and it is on the Org-scoped allowlist (org_scope.go). Row 29 is
 * by definition an EXPIRED-session walk, so a session-gated source such as
 * /whoami — which also carries sovereignFQDN — cannot answer it.
 *
 * WHY THIS IS A SEPARATE MODULE RATHER THAN A CHANGE TO detectMode().
 * `DETECTED_MODE` is a synchronous module-level singleton resolved at import
 * time; this answer is necessarily async. detectMode() keeps deriving the MODE
 * from the hostname, which is correct — the per-Org console really is served by
 * the Sovereign bundle (tenant_route.go:127-133). What it cannot do is VERIFY
 * the FQDN, so every auth-URL caller resolves through here instead.
 */

import { VITE_SOVEREIGN_FQDN } from '@/shared/constants/env'
import { API_BASE } from '@/shared/config/urls'
import { DETECTED_MODE } from './detectMode'

/**
 * In-flight/settled promise, so concurrent callers share ONE answer.
 *
 * Caching is not an optimisation here, it is a correctness requirement: a page
 * load can enter the re-auth path from `rootBeforeLoad`, from
 * SovereignConsoleLayout's cookie-expiry branch (#5460) and from the OIDC
 * callback. If those legs resolved different hosts, the authorize request and
 * the token exchange would target different Keycloaks and the login would fail
 * in a NEW way rather than being fixed.
 */
let inFlight: Promise<string | null> | null = null

/**
 * resolveSovereignFQDN — the Sovereign FQDN to build auth URLs against.
 *
 * Returns null in catalyst-zero mode (there is no Sovereign to authenticate
 * against). Never rejects: on any failure it degrades to the hostname
 * derivation, which is exactly today's behaviour and is correct on the
 * Sovereign console itself.
 */
export function resolveSovereignFQDN(): Promise<string | null> {
  if (!inFlight) inFlight = resolveOnce()
  return inFlight
}

/** Test-only cache reset; production code resolves once per page load. */
export function resetSovereignFQDNCache(): void {
  inFlight = null
}

async function resolveOnce(): Promise<string | null> {
  // catalyst-zero has no Sovereign — asking would be a wrong question, not a
  // slow one.
  if (DETECTED_MODE.mode !== 'sovereign') return null

  // Build-time override wins and costs no request: dev/CI have no catalyst-api
  // to answer, and VITE_SOVEREIGN_FQDN exists precisely to stand in for one.
  const forced = (VITE_SOVEREIGN_FQDN ?? '').trim()
  if (forced) return forced

  // The hostname derivation is the FALLBACK, not the answer. It is right on the
  // Sovereign console and wrong on a per-Org console; using it only when the
  // authoritative source is unreachable keeps this module from making login
  // strictly more fragile than it was.
  const derived = DETECTED_MODE.sovereignFQDN

  try {
    const res = await fetch(`${API_BASE}/v1/sovereign/self`, {
      headers: { Accept: 'application/json' },
    })
    if (res.ok) {
      const body = (await res.json()) as { sovereignFQDN?: unknown } | null
      const fqdn =
        typeof body?.sovereignFQDN === 'string' ? body.sovereignFQDN.trim() : ''
      // Assert on the VALUE. An empty string is a present key with no answer,
      // and accepting it would build `https://auth./realms/sovereign` — a worse
      // failure than the one being fixed.
      if (fqdn) return fqdn
    }
  } catch {
    // Network failure / offline / jsdom without a fetch stub — fall through.
  }

  return derived
}
