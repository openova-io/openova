# Goal #4 — demo-user RBAC walk (2026-06-24, console.demo.omani.homes)

Org-scoped browser walk as the demo customer (Org `org-7283eb4a-19e5-4e86-9066-d4aa26762064`).
Session: minted an HS256 member token (org-services JWT_SECRET) → `/auth/org-handover` (#4192) →
org-scoped `catalyst_session` cookie → landed clean on `/jobs` (**no token in URL**). Founder mailbox untouched.

## Both RBAC halves proven (screenshot `demo-org-signedin-7283.png`)
1. **Reaches own data**: Apps page lists the demo Org's OWN WordPress instances — demo-wp-blog, demo-wp-shop, test-shop — all in ns `org-7283eb4a-…-prod`, status CONVERGING, env devstagingprod. Signed in as `demo@openova.io` / `demo.omani.homes`.
2. **CANNOT access global-admin UI**: the org sidebar = **Apps / Catalog / Sandbox / Users / Settings ONLY**. The operator surfaces present on the sovereign-admin console (Dashboard / Cloud / Jobs / Compliance / Organizations — see `../postp0-signedin-walk/`) are ABSENT for the org user.
3. **API-layer denial confirmed**: demo-org token → `GET /api/v1/deployments` → **403 org-scoped-forbidden** (logged in this walk + the 2026-06-23 northstar-rbac-rewalk).

This is the north-star isolation: a customer reaches their own Org, never the Sovereign's global-admin.
