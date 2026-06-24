# Marketplace org-creation funnel — JWT-in-cookie-not-URL live walk (Refs #4192)

Live Playwright walk of the `marketplace.omantel.biz` org-creation funnel on the
permanent omantel.biz Sovereign (dep `4635277cae4ffed9`, me-east-215), validating
the #4192 security property: the post-signup session must land via an **HttpOnly
`catalyst_session` cookie**, NOT a query-param token in the address bar.

## Walk parameters

- Plan: S · App: WordPress · Slug: `jwtwalk0624.omani.works` · Single-region
- Throwaway sign-in email: `jwtwalk0624@omani.works` (never a human mailbox)
- 6-digit PIN read from rtz Valkey key `magic:jwtwalk0624@omani.works`
- Org CR `jwtwalk0624` was created (`POST /api/tenant/orgs` → 201). Checkout did
  NOT complete (no valid voucher / no Stripe payment), so the slug + console host
  were never persisted to localStorage and provisioning never ran.

## Security verdict — PASS (design), live path gated by #4236

- **JWT in HttpOnly cookie, NOT URL** — confirmed correct by design + code.
  The marketplace hands off to `…/auth/org-handover?token=<HS256 member JWT>`
  (a short-lived bootstrap token, by design per #4182/#4186). The catalyst-api
  `AuthOrgHandover` handler validates it, mints an RS256 Org-scoped
  `catalyst_session` cookie with **`HttpOnly: true, Secure: true, SameSite: Lax`**,
  stamps `Referrer-Policy: no-referrer`, and **302s to a clean `/jobs`** with the
  token stripped from the URL. The original complaint shape
  (`…/jobs?token=eyJ…`) is gone — see
  `products/catalyst/bootstrap/api/internal/handler/org_handover.go`.
- **No regression found** — at no point did the funnel place a session JWT in the
  address bar at the FINAL landing. The only `?token=` is the documented
  bootstrap handover param that is burned into a cookie server-side.

## Live state actually observed (honest)

1. The "Console (my tenants)" button redirected to
   `https://console.omantel.biz/auth/org-handover?token=<HS256 member JWT>` —
   the slug-LESS **operator front-door** host, because checkout never completed
   so the slug/console-host were not persisted (`deriveConsoleURL` fell back to
   `console.<sov-fqdn>`).
2. On the front-door host `/auth/org-handover` is served as **STATIC SPA**
   (`server: envoy`, `etag`, `accept-ranges: bytes`, **no `Set-Cookie`**,
   `referrer-policy: strict-origin-when-cross-origin` = the static default, NOT
   the handler's `no-referrer`). The page renders **"Not Found"** — the
   server-side handler never ran. This is CORRECT: `resolveOrgScope` refuses to
   mint an Org session on the Sovereign front door ("invalid audience: not an
   org console host").
3. The correct per-Org pool host `console.jwtwalk0624.omani.works` is
   **NXDOMAIN** / `net::ERR_NAME_NOT_RESOLVED` — the **#4236 layer** (the pool
   A-record + Org console HTTPRoute are not auto-written by the funnel at the
   pre-payment stage; no HTTPRoute for the slug exists on the Sovereign).

So the live HttpOnly-cookie hop could not be exercised end-to-end on this walk —
it is gated behind (a) a completed checkout that persists the slug + Org console
host, and (b) the #4236 DNS/route writing for the pool host. The cookie-minting
behavior itself is proven by the registered route (`main.go` → `h.AuthOrgHandover`)
and the handler source.

## Evidence

- `01-org-handover-notfound-console-omantel-biz.png` — landing on the front-door
  handover URL (token in URL, page = "Not Found").
- `03-handover-frontdoor-notfound-no-setcookie.png` — re-navigation confirming the
  static "Not Found" serving with NO `Set-Cookie` (server-side handler did not run).
- Pool host `console.jwtwalk0624.omani.works` → `net::ERR_NAME_NOT_RESOLVED`
  (NXDOMAIN, #4236) — Chrome error page is not screenshot-able.

Constraints honored: no code changes, no wipes, no restarts, no bastion/mailbox.
