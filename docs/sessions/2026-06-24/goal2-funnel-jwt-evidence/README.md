# Goal 2 — marketplace org-creation → JWT → correct post-signup destination (live browser walk)

**Date:** 2026-06-24 · **Env:** permanent omantel.biz Sovereign (dep `4635277cae4ffed9`), post-recovery.
**Method:** Playwright (real Chromium) over public DNS + wildcard TLS. Reuses the funnel session minted by the #3376 walk (stranger `g4wpsso-stranger@omani.works`).

## The session JWT (decoded live from `localStorage['org-token']`)
```
header: { "alg": "HS256", "typ": "JWT" }
claims: {
  "sub":   "28e5ac70-4154-4b29-b397-0bd44ea79285",
  "email": "g4wpsso-stranger@omani.works",      // the funnel stranger
  "exp":   "2026-06-24T09:42:14Z"
}
localStorage:
  org-active-org-slug    = "g4wpsso"
  org-active-console-host = "console.g4wpsso.omani.works"   // POOL host, NOT omantel.biz
  org-active-org          = "aceba33a-413a-4981-a858-d50adc371943"
```
The JWT is carried as the `org-token` (Bearer / Authorization header), scoped to the funnel org.

## Screenshots
1. `goal2-01-marketplace-landing.png` — `https://marketplace.omantel.biz/` ("Build Your Organization — OpenOva"), the funnel entry, live.
2. `goal2-02-post-signup-console-g4wpsso.png` — the post-signup destination `https://console.g4wpsso.omani.works/apps`, **signed in as `g4wpsso-stranger@omani.works`** on org `g4wpsso.omani.works`, Applications rendering (vcluster INSTALLED; dev/staging/prod). The pool host, not omantel.biz — the #4179 fix.

## Verdict
Org-creation → org JWT (stranger email) → correct pool-host post-signup destination → signed-in console: **GREEN**, irrefutable browser evidence. Backs the closed #3376 / #4179.
