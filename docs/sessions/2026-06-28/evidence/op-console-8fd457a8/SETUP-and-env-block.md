# Authed operator-console walk — 8fd457a8 (omantel.biz) — 2026-06-28

## Setup SUCCEEDED (genuine, keycloak-API-verified)
- Created throwaway operator user `uatwalk-op` (email uatwalk-op@omani.works) in the `sovereign` realm — **HTTP 201**.
- Granted `/sovereign-admins` group — **HTTP 204**; user groups confirmed `['sovereign-admins','sovereign-viewers']`.
- This proves the Sovereign realm's RBAC machinery on the fresh prov is intact: the admin/ops/viewer groups exist (`/sovereign-admins`, `/sovereign-ops`, `/sovereign-viewers`, `/openova-users`, plus the per-Org `/nstar`, `/omantel-biz`), and admin user+group management works against `auth.omantel.biz/realms/sovereign`.

## Walk BLOCKED — environment limitation, NOT a product fault
1. **Playwright browser backend has no public egress.** `browser_navigate` to `https://console.omantel.biz/` AND to `https://example.com/` both time out at 60s (domcontentloaded). The headless browser cannot reach any public host from this runtime.
2. **`catalyst-ui` OIDC client disallows direct-access grants** (`unauthorized_client: Client not allowed for direct access grants`) — it is a browser-only public client, so an API-layer token also requires the browser redirect flow (which is blocked by #1).

## Conclusion (honest, no theater)
The authed-UI numbered rows (30/31/32/33/34 SSO landings; 16/17/20/21/23 dashboard/treemap; Organizations directory; topology 46–65; Jobs lens) **cannot be render-verified in this environment** — exactly the documented prior-walk wall ("interactive UI renders stay honestly ⚠️/❌ where un-reproduced; the SPA login guard clears injected cookies"). **Zero rows flipped ✅** — flipping a render-row on a 200/302/redirect alone would be the 302-passing theater the founder forbids.

The operator user `uatwalk-op` is left in place (enabled) so the authed walk can be completed from any runtime that HAS browser egress; re-run the Playwright login as `uatwalk-op` / (password held by the walker) against console.omantel.biz.
