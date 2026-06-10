# hw124 — silent SSO cross-app browser walk (live, 2026-06-10)

Real OIDC flow walked via **Playwright headless browser** on hw124 — the founder's
acceptance standard ("land logged-in, zero prompt"), not a config audit. ONE
Keycloak session (catalyst-pin auto-delegation) → **all four apps landed
logged-in with zero prompts** = true cross-app single sign-on.

| App | Navigated to | Landed (logged-in, zero prompt) | Identity |
|---|---|---|---|
| **Harbor** | `/c/oidc/login` | `registry.hw124…/harbor/projects` — Projects portal, `library` project, New-Project/Logs nav | `qa-test-owner@openova.io` |
| **OpenBao** | `/ui/vault/auth?with=oidc` | `bao.hw124…/ui/vault/secrets` — Secrets Engines (cubbyhole/, secret/ kv), Browser CLI | session reused (no prompt) |
| **Guacamole** | `/guacamole/` | `/guacamole/#/` — Recent/All Connections home; id_token `aud:guacamole, groups:[sovereign-viewers]` | `qa-test-owner@openova.io` |
| **PowerDNS-Admin** | `/oidc/login` | `pdns-admin.hw124…/dashboard/` — Dashboard, Zone Management nav, **Zones table renders** (API reachable via #3207) | `qa-test-owner@openova.io` |

Key facts:
- **Zero prompt:** Harbor `/c/oidc/login` → KC `realms/sovereign` → `/c/oidc/callback?code=…` → portal, with NO login form and NO PIN. The catalyst-pin IdP auto-authenticated. Subsequent apps (openbao/guacamole/powerdns) reused the same KC session silently — single sign-on.
- **Harbor re-used-user conflict is stale test state, not a flow bug:** the first Harbor attempt hit `failed to create user record: … already exists`; deleting the stale Harbor user (`DELETE /api/v2.0/users/4` → 200) + re-walk onboarded cleanly and landed logged-in.
- **PowerDNS-Admin is functional, not just logged-in:** the Zones table renders (queries the PDNS API, empty list — no zones created yet) — confirming #3207 (PDNS_API_URL → real `powerdns.powerdns.svc:8081`).

Screenshots (this dir): `hw124-{harbor,openbao,guacamole,powerdns-admin}-silent-sso-LOGGED-IN-2026-06-10.png`
+ `hw124-harbor-silent-sso-zeroprompt-callback.png` (the pre-cleanup callback state).
