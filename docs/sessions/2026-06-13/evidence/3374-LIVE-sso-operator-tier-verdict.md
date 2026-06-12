# #3374 SSO — operator-tier LIVE walk (hw130, 2026-06-13, post-restore, fresh handover session)

The founder's north star 3: "NO login UI anywhere — URL → signed in as emrah.baysal as ADMIN, proof = surfing admin panels." Operator tier verified LIVE, fresh session, bare URLs:

| App | Bare URL → result | Zero-click |
|---|---|---|
| grafana | grafana.hw130/ → Home dashboard, signed in | ✅ |
| gitea | gitea.hw130/ → "emrah.baysal - Dashboard" | ✅ |
| harbor | registry.hw130/ → /harbor/projects | ✅ |
| openbao | bao.hw130/ui/ → /ui/vault/secrets (the founder-witnessed token-form failure, DEAD) | ✅ |
| pdns-admin | pdns-admin.hw130/ → /dashboard/ (was 1-click; callback-aware fix #3385 works) | ✅ |

**Admin-by-default**: harbor /harbor/users (admin-only surface) loaded — surfing the admin panel = proof.

**5/5 operator apps zero-click + admin.** The cookie-domain fix (#3385) is live (session cookie carries `Domain=hw130.omantel.biz`).

REMAINING #3374 scope (open): tenant-tier per-Org realm (funnel-gated #3376); the 4 harder apps (newapi/hubble/guacamole/openova-flow) + the generic OIDC gate — the #3374 agent's broader sweep.
