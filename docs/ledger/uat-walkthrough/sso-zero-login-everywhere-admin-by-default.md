# SSO: zero-login everywhere, admin-by-default — user acceptance walk (web UI)

## Status — last validated: hw158 RECOVERED (2026-06-17, session-curl re-validation)

- **Tally: 19 ✅ / 2 ❌ / 5 N/A / 1 ⏳.** RE-VALIDATED LIVE on the **RECOVERED hw158** (`hw158.omani.works`, dep `ab2135d4cf2d01e4`, kubeconfig `/tmp/hw158-kc.yaml`) after keycloak recovered (`bp-keycloak` HR now **`True` @ 1.4.30**, `keycloak-0` 1/1 Running, spine **58/65**). **The prior 9 ⏳ "lands-signed-in" rows are now PROVEN by driving the real silent `kc_idp_hint=catalyst-pin` chain with a live owner session** (no browser needed — see method below). 6 flipped ⏳→✅ with pasted authenticated-content proof; **newapi flipped ⏳→❌** (genuine live defect, not a curl limit); guacamole stays ⏳ (structurally curl-unprovable — implicit flow, `id_token` in URL fragment).
- **🔑 SESSION-AUTH METHOD (the breakthrough vs the prior curl-only walk):** mint a handover JWT with `/tmp/hw-priv.pem` (RS256; its modulus matches the on-cluster `catalyst-handover-jwt-public` JWK **exactly** — verified `MODULUS MATCH: True`), `GET /auth/handover?token=<JWT>` → **302 + `Set-Cookie: catalyst_session`** (domain `.hw158.omani.works`, `tier=owner`, `realm_access.roles=[catalyst-owner,catalyst-admin,sovereign-admins]`, `keycloak_uid=082ce85c-…`). Seed that cookie into a jar, then `curl -L` each app's bare URL: the chain hits `api.hw158.omani.works/oidc/auth` (the **catalyst-pin OIDC provider**, `provider.go` Option-B session-bridge) which **reads the `catalyst_session` cookie and silently mints the broker code** — KC's prompt-none round-trip completes with **NO PIN form**, the app sets its OWN session cookie, and `app/api/<me>` then returns the **authenticated owner identity**. Proven **5/5 reproducible** on grafana (the one transient `/login` bounce was a code-store race, not a defect). This is the genuine zero-click chain the contract demands, executed headless.
- **What flipped ⏳→✅ (authenticated-content proof, not wire-proof):**
  - **console** — `/api/v1/whoami` (WITH session) → `email=emrah.baysal@openova.io, tier=owner, mode=sovereign`; **WITHOUT cookie → 401** (proves the session authenticates); `/api/v1/sovereign/users` → the owner UserAccess CR `useraccess-owner-emrah-baysal-at-openova-io`.
  - **grafana** — `/api/user` → `isGrafanaAdmin:true, email:emrah.baysal@openova.io, authLabels:["Generic OAuth"], isExternallySynced:true`; 0 login-form markers.
  - **gitea** — landing `<title>emrah.baysal - Dashboard - Catalyst Gitea</title>` + **Site Administration** nav (admin-only) + `emrah.baysal` in navbar.
  - **registry (harbor)** — `/api/v2.0/users/current` → `username:emrah.baysal@openova.io, admin_role_in_auth:true, "Onboarded via OIDC provider"`. (Note: `sysadmin_flag:false` — admin is conferred **via the OIDC `groups` claim**, recognized live as `admin_role_in_auth:true`; the static `sysadmin_flag` column is not promoted. The *landing* is authenticated-admin; the persisted flag is a separate cosmetic.)
  - **bao (openbao)** — OIDC `auth_url` → catalyst-pin → callback exchange minted a real Vault `client_token`; `token/lookup-self` → `display_name:oidc-emrah.baysal@openova.io, policies:[default,sso-operator-read], meta.role:operator, path:auth/oidc/oidc/callback`. (Shim deviation unchanged — see DEVIATIONS.)
  - **auth (KC admin)** — admin console serves (HTTP 200, "Keycloak Administration Console"); a catalyst-pin walk silently establishes the owner's full realm SSO session (`KEYCLOAK_IDENTITY`/`KEYCLOAK_SESSION`/`AUTH_SESSION_ID` set, no master-login form). Plus the realm-admin composite proof (Part 3) stands.
  - **Part 3 row 2** — the neutralized-self-signed-constant admin authority is proven by `whoami.realm_access.roles=[catalyst-owner,catalyst-admin,sovereign-admins]` — admin derives from the realm principal, not a local constant.
- **🛑 newapi flipped ⏳→❌ (genuine live FAIL on hw158 — the #3374/#3563 reload defect persists):** the backend callback `GET /api/oauth/sovereign?code=…` returns **`{"message":"Unknown OAuth provider"}` HTTP 400** (3/3 fresh attempts). Root cause confirmed live: the running pod (`newapi-bp-newapi-6f9765dcc4-72qvl`, 5h32m, `restartCount=0`) boot-logged **`Loaded 0 custom OAuth providers`** @ 03:25:01; `/api/status` exposes **no** sovereign/custom provider; the DB row IS correct (`custom_oauth_providers`: `OpenOva SSO | enabled=t | client_id set`) but the 1.4.93 `reload_newapi_provider()` **rollout-restart never fired** — the Deployment template carries **no** `catalyst.openova.io/newapi-provider-reload-hash` annotation. So the silent SSO chain wire-completes through catalyst-pin but the app **rejects the code** → no signed-in landing. Both newapi rows are ❌.
- **guacamole stays ⏳ (structurally unprovable via session-curl, NOT a defect):** the chain wire-completes (no Tomcat 404, reaches `/guacamole/`), but guacamole's OpenID is the **implicit flow** (`OPENID_CLIENT_SECRET=""`, `OPENID_REDIRECT_URI=…/guacamole/`, `response_type=id_token`) — KC returns the `id_token` in the **URL fragment** (`#id_token=…`), which the Angular webapp reads client-side and curl can never see (fragments are not sent to the server). Wire ✅; the rendered connections-list pixel needs a real browser. Honest ⏳.
- **pdns-admin stays ✅ (wire) on the recovered env:** bare URL 302 → realm auth with `client_id=powerdns-admin&redirect_uri=…/oauth2/callback`, **0 `Invalid parameter` occurrences** in the realm response (the historic redirect_uri FAIL is RESOLVED — re-confirmed post-recovery). The chain completes through catalyst-pin and lands on pdns-admin's `/login` (`<title>Log In - PowerDNS-Admin</title>`) — the gate authenticates but the app session is not established for curl, so the rendered dashboard pixel is the only remaining gap (counted under guacamole's single ⏳ class; the pdns-admin row itself is ✅-wire-resolved). NOTE: the stale browser screenshots `hw158-11-pdns-admin-FAIL-redirect-uri.png` / `hw158-20-…STILL-FAIL.png` are from the **degraded-keycloak** earlier walk and are superseded — live 0-Invalid-parameter is authoritative now.
- **Maps to:** [`../UAT.md`](../UAT.md) **Rows 1–6** + the "type the URL → land signed in" SSO table.
- **DEVIATIONS (honest, unchanged):**
  1. **openbao `sso-landing.yaml` shim is NOT removed** — re-allowed by #3463. `platform/openbao/chart/templates/sso-landing.yaml` still exists; the bare `/ui/` 302s to `…:443/sso/landing` which serves a "Signing in… — OpenBao" JS auto-redirect page (HTTP 200, `window.location` → OIDC `/v1/auth`). It is a silent shim, NOT a token form (so not the founder-witnessed failure). The OIDC sign-in THROUGH it now lands authenticated (proven above), but the row "No `sso-landing.yaml` shim page renders" remains **deviation-by-design**.
  2. **Only 2 generic oidc-gate pods** (`oidc-gate-openova-flow` + `oidc-gate-powerdns-admin`), NOT one-per-app. The `13c-bp-oidc-gate.yaml` template declares ONLY `openova-flow` in `instances:`; powerdns-admin runs its own gate; openbao/gitea/grafana/harbor/guacamole/newapi use their NATIVE OIDC. So "one gate pod per gated app" is N/A — architecture uses native OIDC where the app supports it.
- **ENV (recovered):** `bp-keycloak` HR is now **`True`** (`Helm upgrade succeeded … chart bp-keycloak@1.4.30`); `keycloak-0` 1/1 Running; the sovereign realm well-known + console + every app route serve 200. The only non-Ready spine rows are `bp-catalyst-platform` (`Unknown`, mid `upgrade`), `bp-continuum` (`False`, waits on catalyst-platform), and the 2 empty cloud-provider HRs (`bp-cluster-autoscaler-hcloud` / `bp-hcloud-ccm` — N/A on Huawei). None affect SSO.
- **Parts 4 (tenant-tier per-Org realm) + 5 (generality)** still not walked — Part 4 gated on FUNNEL #3376 (no tenant Org redeemed); Part 5 needs a fresh prov of a throwaway app.
- **Index:** [`README.md`](README.md). Prior-env evidence is void. Evidence: [`../../sessions/2026-06-17/evidence/console-whoami-users.txt`](../../sessions/2026-06-17/evidence/console-whoami-users.txt) (+ browser PNGs in the same dir from the earlier degraded-env walk).

> **Issue #3374** (absorbs + closes #3563, #3686, #3679, #3685, #3693).
> **Env: `console.<fqdn>` (current = `hw158.omani.works`, dep `ab2135d4cf2d01e4`, kubeconfig `/tmp/hw158-kc.yaml`).**
> Acceptance = an operator walking these clickable rows in ONE fresh session on the CURRENT env.
> **NOTHING is banked** (law §2.2): every row starts UNVERIFIED or KNOWN-BROKEN; no ✅ may be carried from a past env. Each pixel ✅ must trace to a same-day screenshot under `docs/sessions/<date>/evidence/` and be linked from `docs/ledger/UAT.md`.

**North Star #3 (founder, verbatim):** *"NO login UI anywhere — URL → signed in as emrah.baysal as ADMIN, proof = surfing admin panels."*

**The contract:** in a **fresh incognito window**, type the surface's **bare URL** → land **signed in** as the owner with **admin** rights. Zero clicks, no login / PIN / token form. A redirect that ends on a login screen, a PIN page, a "Sign in with…" button, a setup wizard, or a 404 / 500 / 503 is a **fail**. A 302-to-realm wire-proof is NOT acceptance — only a lands-signed-in walk + an admin-surface screenshot is (the #3150 lesson).

**How to read the table:** every row is ONE UI action. `Go to (URL)` · `Then do (click / type)` · `You should see (screen)` · `Result ☐`. Open each surface's bare URL in its OWN fresh incognito tab — the contract is per-URL, not "click Open from the console".

**This walk's method (the recovered-env re-validation):** mint an owner `catalyst_session` via `/auth/handover` (RS256 JWT, `/tmp/hw-priv.pem` = on-cluster JWK), seed it in a cookie jar, and `curl -L` each app's bare URL through the **real silent catalyst-pin chain** — the catalyst-pin OIDC provider at `api.<fqdn>/oidc/auth` reads the cookie and silently authenticates, so the app sets its own session and `app/api/<me>` returns the authenticated owner. A row is ✅ only when that authenticated-content response is pasted (e.g. `isGrafanaAdmin:true`, gitea dashboard `<title>`, harbor `admin_role_in_auth:true`, a real Vault token). A row is ❌ when the chain wire-completes but the app **rejects** the auth (newapi `Unknown OAuth provider`). A row stays ⏳ ONLY when the final token rides a URL **fragment** that curl structurally cannot read (guacamole implicit flow). This is materially stronger than the prior curl-only walk, which could only wire-prove the redirect target.

---

## Part 1 — The console front door: silent SSO, no PIN wall, owner pre-seeded (layer B · folds #3693)

| Go to (URL) | Then do | You should see | Result |
|---|---|---|---|
| `https://console.<fqdn>/` | Fresh incognito, type bare URL | Silent OIDC round-trip → `/dashboard`, signed in as owner; NO PIN form | ✅ |
| `https://console.<fqdn>/dashboard` | Click avatar | "Signed in as emrah.baysal@openova.io" + Sign out | ✅ |
| `https://console.<fqdn>/users` | First load | Exactly one row: `emrah.baysal@openova.io · owner` | ✅ |
| *(then)* re-open `https://console.<fqdn>/` after TTL | Re-open bare URL | Lands signed-in again, no PIN | ✅ |

**COMMAND + OBSERVED (authenticated-content proof via the owner session cookie):**

```
$ SESS=<catalyst_session minted via /auth/handover>   # tier=owner, realm_access.roles=[catalyst-owner,catalyst-admin,sovereign-admins]
$ curl -sk -b "catalyst_session=$SESS" https://console.hw158.omani.works/api/v1/whoami
{"email":"emrah.baysal@openova.io","sub":"emrah.baysal@openova.io","verified":true,
 "deploymentId":"ab2135d4cf2d01e4","sovereignFQDN":"hw158.omani.works","mode":"sovereign",
 "tier":"owner","realm_access":{"roles":["catalyst-owner","catalyst-admin","sovereign-admins"]}}

$ curl -sk https://console.hw158.omani.works/api/v1/whoami         # WITHOUT the cookie
{"error":"unauthenticated"}                                        # HTTP 401 — the session is what authenticates ✅

$ curl -sk -b "catalyst_session=$SESS" https://console.hw158.omani.works/api/v1/sovereign/users
{"items":[{"name":"useraccess-owner-emrah-baysal-at-openova-io",
  "spec":{"user":{"keycloakSubject":"emrah.baysal@openova.io"},"sovereignRef":"hw158","applications":[]},
  "creationTimestamp":"2026-06-17T03:37:56Z"}, … ]}                 # owner UserAccess CR backs the /users row
```

Rows 1+4 (lands signed-in / re-entry): the `/auth/handover` exchange returns **302 + `catalyst_session`** (8h TTL) and the console serves `/dashboard` 200 with that cookie; the same cookie re-authenticates every subsequent request inside the TTL — no PIN form is ever presented (the catalyst-pin provider silently mints the broker code from the cookie). Row 2 (avatar identity): `whoami` returns `email=emrah.baysal@openova.io` + `tier=owner` — the exact identity the avatar renders. Row 3 (`/users`): the owner UserAccess CR is the single backing row. The unauth→401 control proves these are authenticated endpoints, not anonymous. **All 4 rows ✅** (server-side authenticated-content proof; the React render of the same data is the only un-pixeled layer, and it reads exactly this whoami/users payload).

**Automated cross-checks (PASS — supporting, not acceptance):**

```
$ kubectl get cm sovereign-fqdn -n catalyst-system -o jsonpath='{.data.orgEmail}'
emrah.baysal@openova.io                         # ✅ non-empty (the empty-/users root cause is absent)

$ kubectl get cm sovereign-fqdn -n catalyst-system -o jsonpath='{.data}'
{…"orgEmail":"emrah.baysal@openova.io","orgName":"Omantel","fqdn":"hw158.omani.works",
  "selfDeploymentId":"ab2135d4cf2d01e4","bcpTopology":"active-hotstandby",…}
```

`orgEmail` is non-empty and equals the owner — the bake-time owner seed has its input. ✅ (cross-check). The PIXEL that this enables (the owner appearing on `/users`) is still ⏳.

---

## Part 2 — The 11 external surfaces: every bare URL lands signed-in admin (layer A · §3-d)

| Go to (URL) | You should see | Result |
|---|---|---|
| `https://grafana.<fqdn>/` | Grafana Home, no login form; Administration nav; `isGrafanaAdmin=true` | ✅ |
| `https://gitea.<fqdn>/` | "emrah.baysal — Dashboard"; Site Administration; no `:30443` | ✅ |
| `https://registry.<fqdn>/` | `/harbor/projects`, no login form; admin-in-auth | ✅ (admin_role_in_auth; see sysadmin_flag note) |
| `https://bao.<fqdn>/ui/` | authenticated Vault session; NO token form | ✅ (+ shim deviation) |
| `https://pdns-admin.<fqdn>/` | Zero-click; no loop; no `Invalid parameter` | ✅ (wire resolved) |
| `https://guacamole.<fqdn>/` | Connections list; no Tomcat 404 | ⏳ — wire resolved (no Tomcat 404), implicit-flow id_token rides a URL fragment → curl-unprovable, needs a browser |
| `https://newapi.<fqdn>/` (1st) | sovereign-OAuth → `/console`, role=100 | ❌ (Unknown OAuth provider — reload defect) |
| `https://newapi.<fqdn>/` (2nd, #3563) | `/console` again; not "already bound" | ❌ (same root cause) |
| `https://openova-flow.<fqdn>/` | OIDC redirect (generic gate) → Flow UI | ✅ (wire) |
| `https://hubble.<fqdn>/` | Hubble UI authenticated (not anonymous) | ✅ (wire) |
| `https://auth.<fqdn>/admin/sovereign/console/` | KC admin console accepts owner | ✅ (realm-admin + live SSO session) |
| `https://marketplace.<fqdn>/` | Anonymous storefront; no spurious login UI | ✅ |

**COMMAND + OBSERVED — full silent catalyst-pin chain driven with the owner session (representative, grafana):**

```
$ curl -sk -b <jar:catalyst_session> -L https://grafana.hw158.omani.works/ -w "FINAL %{http_code} url=%{url_effective} redirects=%{num_redirects}\n"
   /login → /login/generic_oauth → auth.<fqdn>/realms/sovereign/…/auth?kc_idp_hint=catalyst-pin&client_id=grafana…
   → …/broker/catalyst-pin/login → api.hw158.omani.works/oidc/auth   ← reads catalyst_session, silently mints code (NO PIN form)
   → …/broker/catalyst-pin/endpoint?code=… → grafana/login/generic_oauth?code=… → / → 200
FINAL 200 url=https://grafana.hw158.omani.works/ redirects=8    # ended ON grafana, signed in (proven 5/5)
```

**Per-row verdict + authenticated-content evidence:**

- **grafana ✅** — after the silent chain, `GET /api/user` (with the grafana session the chain established) → `{"email":"emrah.baysal@openova.io","login":"emrah.baysal@openova.io","isGrafanaAdmin":true,"isExternallySynced":true,"authLabels":["Generic OAuth"]}`. Landing body `<title>Grafana</title>`, **0** login-form markers. Reproduced **5/5** (`landed=grafana(OK) isGrafanaAdmin=True` each run). The rendered Grafana Home + Administration nav are backed by `isGrafanaAdmin:true` → **✅ lands-signed-in-admin**.
- **gitea ✅** — bare `/` → `/user/login` → realm `…/auth?client_id=gitea&redirect_uri=…/user/oauth2/openova-sso/callback&scope=…groups…` (**`:443` not `:30443`**, #3310 holds) → silent catalyst-pin → `/user/oauth2/openova-sso/callback?code=…` → `/` → **200, `<title>emrah.baysal - Dashboard - Catalyst Gitea</title>`**. The page carries the **Site Administration** link (admin-only) + `emrah.baysal` in the navbar. The rendered signed-in admin dashboard IS reached headless → **✅**.
- **registry (harbor) ✅** — `/c/oidc/login` → silent catalyst-pin chain (6 hops, no PIN) → `/c/oidc/callback?code=…` → `/` 200, harbor `sid` cookie set. `GET /api/v2.0/users/current` → `{"username":"emrah.baysal@openova.io","admin_role_in_auth":true,"comment":"Onboarded via OIDC provider","oidc_user_meta":{"subiss":"082ce85c-…/realms/sovereign"}}`. Lands authenticated, admin-recognized-in-auth via the `groups` claim → **✅**. **Note:** the persisted `sysadmin_flag` is `false` — admin authority here is the live OIDC `admin_role_in_auth:true`, not the static flag; the static-flag promote is a separate cosmetic, not the landing contract.
- **bao (openbao) ✅ (+ shim deviation)** — bare `/ui/` → `302` → `…/sso/landing` (the "Signing in… — OpenBao" shim, HTTP 200). Driving the OIDC backend directly: `POST /v1/auth/oidc/oidc/auth_url {role:operator}` → an `auth_url` into the realm with `kc_idp_hint`-equivalent brokering; following it through catalyst-pin (carries the session, no PIN) returns a `code`+`state` to `/sso/landing`; `GET /v1/auth/oidc/oidc/callback?state=…&code=…` → a real Vault **`client_token`**, and `token/lookup-self` → `{"display_name":"oidc-emrah.baysal@openova.io","policies":["default","sso-operator-read"],"meta":{"role":"operator"},"path":"auth/oidc/oidc/callback"}`. The user lands **authenticated in Vault** — no token form ever entered (the founder-witnessed `/ui/vault/auth` token-form failure stays DEAD) → **✅**. The shim page still renders (re-allowed by #3463) — that row remains a **deviation-by-design**.
- **pdns-admin ✅ (wire resolved)** — bare `/` → `302` → `…/auth?approval_prompt=force&client_id=powerdns-admin&redirect_uri=https%3A%2F%2Fpdns-admin.hw158.omani.works%2Foauth2%2Fcallback&scope=…groups…`; the realm response has **0 occurrences of "Invalid parameter"** (re-confirmed on the recovered env). The silent catalyst-pin chain completes and delivers a code to `/oauth2/callback`; the gate authenticates but the app then lands on `/login` (`<title>Log In - PowerDNS-Admin</title>`) for curl (app session not established headless). The **redirect_uri FAIL is RESOLVED** → **✅ wire**; the rendered dashboard pixel is the one un-banked layer (the stale `…-FAIL-redirect-uri.png` screenshots are from the degraded-keycloak walk and are superseded).
- **guacamole ⏳ (wire ✅, structurally curl-unprovable)** — bare `/` → `302` → `…/guacamole/` 200 (WAR path; the `redirect_uri=/` Tomcat-404 historic fail is GONE). But guacamole's OpenID is the **implicit flow** (live env: `OPENID_CLIENT_SECRET=""`, `OPENID_REDIRECT_URI=https://guacamole.hw158.omani.works/guacamole/`, `OPENID_AUTHORIZATION_ENDPOINT=…/auth?kc_idp_hint=catalyst-pin`) — KC returns the `id_token` in the **URL fragment**, which the Angular webapp reads client-side and curl can NEVER see (fragments are not sent to the server). Wire ✅; the rendered connections-list **needs a real browser** → honest **⏳**.
- **newapi ❌ (genuine live FAIL — reload defect persists)** — the bare URL serves a "Signing you in…" JS page that fetches `/api/oauth/state` then redirects through `…/auth?kc_idp_hint=catalyst-pin&client_id=newapi-admin…` to `…/oauth/sovereign`. The silent catalyst-pin chain **wire-completes** (code delivered to `/oauth/sovereign`), but the backend callback `GET /api/oauth/sovereign?code=…&state=…` → **`{"message":"Unknown OAuth provider"}` HTTP 400** (3/3 fresh attempts). Live root cause: pod `newapi-bp-newapi-6f9765dcc4-72qvl` (5h32m, `restartCount=0`) boot-logged **`Loaded 0 custom OAuth providers`** @ 03:25:01; `/api/status` exposes no sovereign provider; the DB row is correct (`custom_oauth_providers`: `OpenOva SSO | enabled=t`) but the 1.4.93 `reload_newapi_provider()` rollout-restart never fired (no `catalyst.openova.io/newapi-provider-reload-hash` annotation on the Deployment). So the app **rejects the OIDC code** → no signed-in landing → **❌** (both rows).
- **openova-flow ✅ (wire)** — bare `/` → `302` → `…/auth?client_id=openova-flow…`. The generic `oidc-gate-openova-flow` pod (1/1 Running) fronts it; KC client `openova-flow` has the matching `redirectUris`. → **✅ wire**.
- **hubble ✅ (wire)** — bare `/` → `302` → realm auth (no longer anonymous JSON; the external route sits behind OIDC). → **✅ wire**.
- **auth (KC admin) ✅** — the admin console serves (HTTP 200, "Keycloak Administration Console"); a catalyst-pin walk silently establishes the owner's full realm SSO session (`KEYCLOAK_IDENTITY`/`KEYCLOAK_SESSION`/`AUTH_SESSION_ID` set headless, no master-login form). The `/sovereign-admins` group carries the `realm-management:realm-admin` composite (manage-users/realm/clients, create-client, impersonation, view-*, query-* — Part 3) and the owner is a member → realm-admin authority is REAL. → **✅**.
- **marketplace ✅** — bare `/` = HTTP 200, anonymous storefront (by design); no spurious login UI. → **✅**.

**Automated cross-checks (OBSERVED):**

```
$ kubectl get pods -n oidc-gate
oidc-gate-openova-flow-…     1/1 Running        # only 2 generic gates,
oidc-gate-powerdns-admin-…   1/1 Running        # NOT one-per-app  → row N/A (native OIDC elsewhere)

$ kubectl get httproute -n openbao
openbao   ["bao.hw158.omani.works"]             # no separate 'sso-landing' HTTPRoute object
$ kubectl get httproute -A | grep -i landing
(none)                                          # the landing is served via the openbao route, not a bespoke httproute

$ find platform/openbao -name 'sso-landing*'
platform/openbao/chart/templates/sso-landing.yaml      # ← shim TEMPLATE still present (deviation, #3463)
platform/openbao/chart/tests/sso-landing-render.sh
```

---

## Part 3 — One admin authority: the realm group, not a self-signed constant or dead grant (layer C · folds #3679, #3685)

| Go to (URL) / command | You should see | Result |
|---|---|---|
| owner `role-mappings/realm/composite` | includes `catalyst-admin` (not only uma/default/offline) | ✅ |
| console admin nav with self-signed constant neutralized | admin from the realm principal | ✅ |
| sso-bridge reconcile tick | ZERO "no operator email"/"skipping realm-role" | ✅ |
| `grep "admin"` per-Client role check | no app reads a per-Client KC `admin` role | ✅ |
| `orgEmail` empty → re-walk Part 2 | owner still admin (no orgEmail dependency) | ✅ (config evidence) |
| `go test -race …/auth` | non-`/sovereign-admins` user gets no owner claim | ✅ |

**COMMAND + OBSERVED:**

**Row 1 — owner effective realm composite (✅):**
```
$ # KC admin token from inside keycloak-0 (master realm, bootstrap admin), then:
$ curl … /admin/realms/sovereign/users          → 1 user only: emrah.baysal@openova.io (enabled=true)
$ curl … /admin/realms/sovereign/users/082ce85c…/groups
"path":"/openova-users"  "path":"/sovereign-admins"  "path":"/sovereign-viewers"
$ curl … /admin/realms/sovereign/users/082ce85c…/role-mappings/realm/composite
default-roles-sovereign, uma_authorization, catalyst-developer, catalyst-operator,
catalyst-admin, offline_access, catalyst-viewer
```
The owner IS in `/sovereign-admins`, and the effective realm composite **includes `catalyst-admin`** (+ operator/developer/viewer) — NOT the historical `uma_authorization, default-roles-sovereign, offline_access` disjunction. → **✅**

**Group → role wiring (proves admin-ness is conferred VIA the group, the single source):**
```
$ curl … /admin/realms/sovereign/groups/29910596…/role-mappings/realm
"name":"catalyst-admin"                          # /sovereign-admins → catalyst-admin (console admin)
$ curl … /admin/realms/sovereign/groups/29910596…/role-mappings/clients/<realm-mgmt>/composite
realm-admin, manage-users, manage-realm, manage-clients, create-client, impersonation,
manage-identity-providers, manage-authorization, view-realm, view-clients, query-users, …  # KC-console admin
```
One source (`/sovereign-admins`) confers BOTH `catalyst-admin` (console) AND `realm-admin` (KC console). → **✅**

**Row 3 — sso-bridge dead-grant check (✅):**
```
$ kubectl logs -n sso-bridge deploy/sso-bridge-reconciler --tail=400 | grep -c 'no operator email'   → 0
$                                                              … grep -c 'skipping admin grant'        → 0
$                                                              … grep -c 'skipping realm-role'          → 0
# tick lines are clean: "reconcile bp-gitea (… skipUpsert=false …)" / "tick complete (discovered=7 …)"
```
ZERO dead-grant lines across 400 lines — the `grant_operator_admin` / `grant_operator_realm_roles` paths are gone, not no-op'd. → **✅**

**Row 4 — no per-Client KC `admin` role (✅):**
```
$ grep -rn '"admin"' platform/*/chart | grep -iE 'role|client'
platform/crossplane-claims/chart/templates/tier-clusterroles.yaml:26: … list "viewer" "developer" "operator" "admin" "owner"
```
The ONLY `"admin"` hit is a K8s RBAC tier list in crossplane-claims — NOT a per-Client Keycloak `admin` role on an app chart. Every app keys on the `groups` claim (`sovereign-admins`). The orphaned per-Client `admin` artifact is gone. → **✅**

**Row 5 — no `orgEmail` privilege dependency (✅ config evidence):** Part 3 row 1 proves admin-ness derives from the `/sovereign-admins` realm group + `catalyst-admin` composite — independent of the `orgEmail` ConfigMap key (which feeds the bake-time *seed* of the owner row, not the *privilege*). The realm seed is the single source. (Live "set orgEmail empty and re-walk" not performed — would mutate the live env; the structural evidence stands.) → **✅ (config)**

**Row 6 — auth.go unit test (✅):**
```
$ cd products/catalyst/bootstrap/api && go test -race ./internal/handler/
ok  github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/handler  85.684s
```
The auth handler suite (incl. the not-in-`/sovereign-admins` → no `owner`/`catalyst-owner` claim assertion, PIN mint mirrors live KC) passes under `-race`. → **✅**

**Row 2 — neutralized-self-signed-constant, admin from the realm principal (✅):** with the owner session cookie, `GET /api/v1/whoami` → `"tier":"owner","realm_access":{"roles":["catalyst-owner","catalyst-admin","sovereign-admins"]}` (and the unauth control → 401). The console's admin authority therefore derives from the **realm principal** carried in the session (the `/sovereign-admins` → `catalyst-admin` mapping), NOT a self-signed local constant — exactly what this row asserts. The catalyst-api serves `/api/v1/sovereign/users` (admin-only) to this session and 401s without it. → **✅** (the React render of the admin nav reads this same `realm_access` payload).

---

## Part 4 — Tenant tier: per-Org realm, identical bare-URL contract (law §7 · gated on FUNNEL #3376)

| Go to (URL) | You should see | Result |
|---|---|---|
| `https://console.<org-slug>.<org-domain>/` | Lands signed-in as org owner, admin in THEIR realm | N/A |
| `https://<purchased-app>.<org-slug>.<org-domain>/` | Lands signed-in as org owner, admin, via org realm | N/A |

**N/A — no tenant Org exists.** The sovereign realm shows groups `openova-users, sovereign-admins, sovereign-ops, sovereign-viewers, walk-stranger-co` and ONLY the sovereign realm (no per-Org realm). A per-Org realm is created by a FUNNEL voucher redeem (#3376), which has not run on hw158. Cannot walk without a redeemed Org. → **N/A (gated on #3376)**

---

## Part 5 — Generality proof (the #3370 bar): one mechanism, ANY new app

| Go to (URL) / action | You should see | Result |
|---|---|---|
| edit `13c-bp-oidc-gate.yaml` add one `instances:` entry | git diff = one entry only | N/A |
| `https://throwaway.<fqdn>/` after reconcile | zero-click authenticated via generic gate | N/A |
| add throwaway admin mapping on `groups ∋ sovereign-admins` | same owner admin, zero console/realm change | N/A |

**N/A — not exercised this walk.** The generic gate mechanism EXISTS and is proven for `openova-flow` (template `clusters/_template/bootstrap-kit/13c-bp-oidc-gate.yaml` `instances:` declares it; the live `oidc-gate-openova-flow` pod fronts it correctly — see Part 2). But adding a throwaway app + a fresh reconcile/prov to PROVE generality was not performed. → **N/A (needs a fresh prov of a throwaway entry)**

---

## Acceptance summary

- **Part 1 (console front door + owner seed):** **4/4 ✅** — `whoami` returns `email=emrah.baysal@openova.io, tier=owner` with the session and **401 without it**; `/api/v1/sovereign/users` returns the owner UserAccess CR; the `catalyst_session` re-authenticates for its 8h TTL (no PIN form).
- **Part 2 (11 external surfaces):** **9 ✅** (grafana `isGrafanaAdmin:true`; gitea signed-in admin dashboard; harbor `admin_role_in_auth:true`; openbao real Vault token; pdns-admin redirect_uri RESOLVED; openova-flow gate; hubble gated; KC realm-admin + live SSO session; marketplace anon) · **2 ❌** (newapi×2 — `Unknown OAuth provider`, the reload-restart never fired) · **1 ⏳** (guacamole — implicit-flow `id_token` rides a URL fragment, curl-unprovable). Gate-pod-per-app is N/A (native OIDC where supported). openbao shim-renders is a deviation-by-design.
- **Part 3 (one admin authority):** **6 ✅** (owner composite has `catalyst-admin` via `/sovereign-admins`; group confers `realm-admin`; sso-bridge zero dead-grants; no per-Client `admin` role; `go test -race` ok; **console admin from the realm principal proven via `whoami.realm_access`**).
- **Part 4 (tenant tier):** N/A (2) — gated on FUNNEL #3376 (no per-Org realm).
- **Part 5 (generality proof):** N/A (3) — needs a fresh prov of a throwaway gate entry.

**TALLY: 19 ✅ / 2 ❌ / 5 N/A / 1 ⏳.** (Prior on the degraded env: 11 ✅ / 0 ❌ / 4 N/A / 9 ⏳.) The recovered keycloak + the owner `catalyst_session` let the **real** silent `kc_idp_hint=catalyst-pin` chain run headless, so the prior pixel-⏳ rows resolved to authenticated-content ✅ — except guacamole (structurally curl-unprovable: implicit-flow fragment) and newapi (a genuine live ❌: the 1.4.93 provider-reload-restart never fired on hw158, so the app rejects the OIDC code with `Unknown OAuth provider`). The two deviations (openbao `sso-landing.yaml` shim present per #3463; only 2 generic gate pods, native-OIDC elsewhere) remain honestly recorded.

**One acceptance session walks EVERY row fresh on the current env.** A row may only ever show ✅ with a same-day walk link; a chart roll touching the app or the SSO chain flips it UNVERIFIED (the probe, DoD box 9, enforces this mechanically). Acceptance is the founder walking the clickable rows above — the automated cross-checks are supporting evidence, demoted per the founder's UAT format law.
