# Demo org-user agentic-RBAC walk — agenity + console scope (Refs #4110)

**Date:** 2026-06-24
**Sovereign:** omantel.biz (dep `4635277cae4ffed9`, me-east-215 region-B)
**Org:** demo (ns `org-7283eb4a-19e5-4e86-9066-d4aa26762064`, realm `org-demo`)
**Identity walked:** `demo@openova.io` — a DEMO org-admin session (NOT sovereign-admin, NOT the founder mailbox)

## What was validated

The North Star agentic-RBAC journey: a customer org-user lands on their own
agenity dashboard and their own console, fully **org-scoped**, with **no
global / sovereign-admin surface** reachable. The #4110 host-anchored
Org-scope (`tier=org-admin`, `orgScoped=true`) + the `OrgScopeGuard`
deny-by-default API gate were exercised live.

## How the session was minted (org-handover path)

1. Minted an HS256 member token signed with the Org mesh's
   `sme-secrets/JWT_SECRET` (sourced from
   `catalyst-system/org-services-secrets` key `JWT_SECRET`, 64 bytes), claims
   `{sub, email:"demo@openova.io", role:"member", typ:"session", iat, exp}`.
2. Hit `GET https://console.demo.omani.homes/auth/org-handover?token=…`.
   The handler (`org_handover.go`) resolved the Org scope from the **request
   host** (the trust anchor), validated the member token, and minted an RS256
   Org-scoped `catalyst_session`. Response: `302 → /jobs`,
   `Referrer-Policy: no-referrer`, `Set-Cookie: catalyst_session=…;
   Domain=demo.omani.homes; HttpOnly; Secure; SameSite=Lax`.

Minted session JWT (RBAC-relevant claims):

```
tier         : org-admin          (NOT a sovereign tier)
realm_access : { roles: [org-admin] }   (non-catalyst role → no sovereign nav)
role         : openova-user       (NOT superadmin/sovereign-admin)
org / org_id : demo
```

`whoami` confirmed live: `"orgScoped": true, "tier":"org-admin", "org":"demo"`.

## RBAC verdict — PASS (no global-admin surface, no leak)

### Sovereign-admin / cross-org API surfaces — ALL blocked

| Surface | Result |
|---|---|
| `GET /api/v1/deployments` | **403 org-scoped-forbidden** |
| `GET /api/v1/deployments/{id}` (deployment stream) | **403** |
| `GET /api/v1/organizations` (cross-org directory) | **403** |
| `POST /api/v1/deployments/{id}/wipe` (danger zone) | **403** |
| `DELETE /api/v1/deployments/{id}` | **403** |
| `GET /api/v1/parent-domains` | 404 (not routed) |
| `GET /api/v1/sovereigns/{id}/console-ui/sidebar-entries` | **403** |
| `list user-access` (Users page) | **403** |
| `list parent-domains` (Settings) | **403** |
| `cutover status` (Settings) | **403** |

### Org-OWN allowlist surfaces — reachable

`whoami`, `sovereign/self`, `sovereign/apps`, `catalog` all return 200.
`sovereign/apps` returns ONLY the demo Org's own app estate (agenity, cnpg,
keycloak, newapi, openclaw, stalwart, wordpress in env `omantel-biz-prod`) —
no secrets, no cross-org data.

### Console chrome — org-scoped

Sidebar nav shows ONLY: **Apps · Catalog · Sandbox · Users · Settings** —
every entry an Org-OWN surface. NO cloud view, NO provisioning wizard, NO
organizations directory, NO BSS/billing, NO deployment stream.

The `/settings` and `/users` SPA *routes* are reachable client-side (the SPA
renders the shell), BUT every sovereign-admin data fetch on those pages 403s
— FQDN/region/capacity/status render `—`, "API pending", or an inline
"HTTP 403". The "Danger zone" wipe/decommission buttons are inert against the
API (POST wipe → 403, DELETE → 403). This is the documented defense-in-depth
posture: the API 403s even if the SPA is bypassed. **No data or mutation
leaks through the SPA shell.**

`/dashboard` (a sovereign surface) bounces the org session back to `/apps`.

## agenity dashboard state — PASS

`https://agenity.demo.omani.homes/app/` renders the 0.9.6 (chepherd)
dashboard cleanly: Workers / Scrum Masters / Federation / A2A Inbox panels,
Team Transcript, "+ spawn agent". **0 console errors, 0 warnings.** The view
is the customer's OWN agentic surface (0 sessions, team "default") — no
platform-admin / cross-Org control.

## Warnings — cosmetic only

- The header **"Converging"** badge is the cosmetic env-state indicator (the
  demo Org is mid-convergence: newapi / openclaw / stalwart / wordpress still
  INSTALLING — out of scope per the brief). NOT a functional RBAC warning.
- The console's 4 "errors" in the JS console are the SPA probing
  `/api/v1/deployments/{id}` and the sidebar-entries endpoint and being
  **correctly denied 403** by OrgScopeGuard — these are the RBAC gate
  *working*, not failures.

## Enforcement wiring (code)

`products/catalyst/bootstrap/api/cmd/api/main.go:1158-1168` wires
`auth.RequireSession(...)` immediately followed by `h.OrgScopeGuard` on the
RequireSession group. `org_scope.go` defines the host-anchored scope
resolution + the deny-by-default `orgSafePathPrefixes` allowlist.

## Screenshots

- `00-BEFORE-4110-godmode-for-contrast.png` — pre-#4110 god-mode (historical).
- `01-agenity-dashboard-0.9.6-demo-org.png` — agenity dashboard, demo org-user.
- `02-demo-console-apps-org-scoped-nav.png` — console Apps, org-scoped 5-item nav.
- `03-demo-console-settings-all-403.png` — Settings shell, every sovereign field 403/—.
- `04-demo-console-users-403.png` — Users page, `list user-access: HTTP 403`.
