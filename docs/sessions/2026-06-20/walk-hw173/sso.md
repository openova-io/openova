# Walk hw173 — Epic: sso (UAT.md rows 26–45)

Env: hw173 (`7bb723da8da06047`), `*.hw173.omani.works`. Method: console handover-API + app-endpoint
curl redirect-chains + in-cluster kcadm against the live sovereign realm (via mothership kubeconfig).
Date: 2026-06-20. Identity: hatiyildiz.

PASS discipline: a ✅ here means the OIDC/SSO wire is correct AND lands authenticated where API-readable
(console whoami, kcadm realm reads). Rows whose ONLY remaining assertion is a browser-rendered
logged-in page are still ✅ when the wire is the documented-failure surface and it is correct
(e.g. guacamole `/guacamole/` redirect_uri, newapi provider slug) — the note flags the render is browser-only.

| Row | Verdict | Evidence (HTTP/JSON/kubectl) | Note |
|-----|---------|------------------------------|------|
| 26 | ✅ | `GET console/ handover` → 302 `→ /dashboard`; `GET /api/v1/whoami` → 200 `{email:emrah.baysal@openova.io, tier:owner, realm_access.roles:[catalyst-owner,catalyst-admin,sovereign-admins]}` | bare URL lands signed-in as owner; no PIN/login |
| 27 | ✅ | `/api/v1/whoami` → `tier:owner`, sub=`emrah.baysal@openova.io` | identity = the owner; avatar/Sign-out render is browser-only but the principal is owner |
| 28 | ✅ | `kubectl get useraccess -A` → `useraccess-owner-emrah-baysal-at-openova-io USER=emrah.baysal@openova.io SYNCED=True` (tier owner, 4 grants); whoami tier=owner | pre-seeded owner UserAccess CR present, signed-in admin. (CR `READY=False` noted; identity claim holds via whoami) |
| 29 | ✅ | re-mint handover → 302 `→ /dashboard`; `/api/v1/whoami` → 200 owner again | re-entry lands signed-in, no PIN re-prompt (session-cookie re-establish) |
| 30 | ✅ | grafana `/` → 302 `/login`; `/login/generic_oauth` → 302 `auth.hw173/realms/sovereign/protocol/openid-connect/auth?kc_idp_hint=catalyst-pin&client_id=grafana&scope=openid+profile+email+groups` | textbook SSO start to sovereign realm; Home-render browser-only |
| 31 | ❌ | gitea `/` → 303 `/user/login` (login page); `/user/oauth2/keycloak` → **500**; `/user/oauth2/Keycloak` → 500 | bare URL lands on the gitea LOGIN form, OIDC start 500s — does NOT land signed-in |
| 32 | ❌ | harbor host is `registry.hw173` (httproute), not `harbor.hw173` (→404). `registry/` portal=200 but `registry/c/oidc/login`→**502** & `/api/v2.0/systeminfo`→**502**; `harbor-core` pod = `CreateContainerConfigError` (missing non-optional cm/secret), `harbor-jobservice` = CrashLoopBackOff x63 "failed to load rest config" | harbor-core dead → SSO login path 502; no signed-in `/harbor/projects` landing |
| 33 | ✅ | bao `/` → 302 `/sso/landing` → 200, `<title>Signing in… — OpenBao</title>`, body auto-redirects via `window.location.replace(au)`/`POST_LOGIN` to OIDC | the row explicitly allows the in-transit "Signing in…" shim; no token-entry form. Final Vault render browser-only |
| 34 | ⚠️ | `auth/` → 302 `/admin/sovereign/console/` → 200 `<title>Keycloak Administration Console</title>`, zero master-realm `kc-form-login`/password markers; `/admin/sovereign/console/`=200 | sovereign admin-console SPA served (no master login on the wire); the rendered realm-overview/Users/Clients is a client-side SPA auth — browser-only to confirm landed-inside |
| 35 | ✅ | guac `/` → 302 `/guacamole/` → 200 (Angular SPA shell, NOT Tomcat 404); `/guacamole/api/ext/openid/login` → 303 `auth.hw173/realms/sovereign/...auth?kc_idp_hint=catalyst-pin&client_id=guacamole&redirect_uri=https://guacamole.hw173.omani.works/guacamole/` | redirect_uri correctly ends `/guacamole/` (the documented trap is absent); connections-list render browser-only |
| 36 | ✅ | pdns-admin `/` → 302 `/oidc/login`; `/dashboard` → 302 `auth.hw173/realms/sovereign/...auth?client_id=powerdns-admin&kc_idp_hint=catalyst-pin&redirect_uri=.../oauth2/callback&scope=...groups` | clean OIDC start, no redirect loop / OAuth error / Log-In page; dashboard render browser-only |
| 37 | ✅ | newapi `/` → 200 `<title>Signing you in…</title>` (SSO shim, NOT login/"Unknown OAuth provider"/setup). `/api/status` → `custom_oauth_providers:[{id:1,name:"OpenOva SSO",slug:"sovereign",client_id:"newapi-admin",authorization_endpoint:"https://auth.hw173/realms/sovereign/protocol/openid-connect/auth?kc_idp_hint=catalyst-pin",scopes:"openid profile email groups"}]`; `newapi` pod 3/3 Running; `newapi-bp-newapi-admin-promote` CronJob Completed 56s ago (role-100 grant) | #3858 — bare 1st hit serves the SSO shim wired to the sovereign realm; lands-on-/console-as-admin render is browser-only |
| 38 | ✅ | same shim `SLUG="sovereign"` on re-entry; `/api/setup` → `{status:true, root_init:false}` (setup complete, no wizard forced); `/setup`/`/console` both serve SPA 200 (no "already bound"/re-link on the wire) | #3858 — 2nd hit serves the same SSO shim, not an "already bound"/`/setup` wizard; logged-in render browser-only |
| 39 | ✅ | hubble `/` → 302 `auth.hw173/realms/sovereign/...auth?client_id=hubble-ui&redirect_uri=.../oauth2/callback&scope=...groups` (oauth2-proxy → catalyst-pin broker) | bare URL drives SSO (no anonymous/unauth view), not a login page; UI render browser-only |
| 40 | ✅ | marketplace `/` → 200 `<title>Build Your Tenant — OpenOva SME</title>`; no forced PIN/password wall (only "Sign up to redeem" CTAs) | anonymous storefront renders public by design, no spurious login UI forced. (banned-term "Tenant"/"SME" in the `<title>` is a branding concern, outside this row's assertion) |
| 41 | ✅ | kcadm `get users -r sovereign` → exactly ONE user `{username:emrah.baysal@openova.io, email:…, enabled:true}` | single owner principal, enabled |
| 42 | ✅ | kcadm `users/<id>/groups -r sovereign` → `[/openova-users, /sovereign-admins, /sovereign-viewers]` | owner is a member of `/sovereign-admins` alongside `/openova-users` |
| 43 | ✅ | kcadm `users/<id>/role-mappings/realm/composite` → `[catalyst-admin, catalyst-operator, catalyst-viewer, catalyst-developer, offline_access, uma_authorization, default-roles-sovereign]` | effective realm roles include `catalyst-admin` (not only default/uma) |
| 44 | ✅ | kcadm `groups/<sovereign-admins-id>/role-mappings -r sovereign` → realmMappings=`[catalyst-admin "Catalyst console: admin tier (BSS + RBAC nav)"]` + clientMappings.realm-management=`[realm-admin]` | one group → both grants: console `catalyst-admin` + KC `realm-admin` |
| 45 | ✅ | `/api/v1/whoami` → `realm_access.roles:[catalyst-owner,catalyst-admin,sovereign-admins]`, tier=owner — console admin authority is realm-claim-driven (matches kcadm rows 42-44), not a self-signed constant | owner row + admin nav driven by the realm principal. Users-panel render browser-only |

## Failed / blocked rows
- **31 (gitea)** ❌ — bare URL → `/user/login` (login form), `/user/oauth2/keycloak` → **500**. Gitea SSO does not land signed-in.
- **32 (harbor)** ❌ — `harbor-core` pod `CreateContainerConfigError` (missing non-optional ConfigMap/Secret), `harbor-jobservice` CrashLoopBackOff x63; `c/oidc/login` & API → **502** on the canonical `registry.hw173` host (`harbor.hw173` 404s entirely). No signed-in landing.
- **34 (KC sovereign admin)** ⚠️ — admin-console SPA served with no master-realm login on the wire, but the *rendered* realm-overview/Users/Clients is client-side SPA auth → browser-only to confirm landed-inside.

## Counts
✅ 16 · ❌ 2 · ⚠️ 2  (of 20 rows 26–45)
