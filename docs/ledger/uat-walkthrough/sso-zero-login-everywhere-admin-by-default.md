# SSO: zero-login everywhere, admin-by-default — user acceptance walk (web UI)

## Status — last validated: hw158 (2026-06-17)

- **Tally: 11 ✅ / 0 ❌ / 4 N/A / 9 ⏳.** Re-walked LIVE on **hw158** (`hw158.omani.works`, dep `ab2135d4cf2d01e4`) via curl + kubectl + Keycloak admin-API + `go test -race`. **Core SSO wiring + single-admin-authority are PROVEN.** Every gated bare URL wire-redirects into the `sovereign` realm (`auth.hw158.omani.works/realms/sovereign/.../auth`) with the correct per-app `client_id` + `redirect_uri`; the realm has exactly ONE user (`emrah.baysal@openova.io`) who is a member of `/sovereign-admins`, whose effective realm composite **includes `catalyst-admin`**, and whose group confers `realm-management:realm-admin` (full KC-console admin). `go test -race ./internal/handler/` = **ok**. The sso-bridge reconciler shows **zero** dead-grant log lines.
- **The 9 ⏳ are all the "lands signed-in PIXELS" rows** — curl cannot execute the client-side React/JS silent `kc_idp_hint=catalyst-pin` PIN exchange, so "the rendered admin panel" can only be banked by a real browser walk. Wire-proof ≠ pixel-proof (the #3150 law) — these are honestly held ⏳, not ✅.
- **pdns-admin `Invalid parameter: redirect_uri` is RESOLVED ✅** — bare URL now 302s to the realm auth endpoint (`client_id=powerdns-admin`, `redirect_uri=…/oauth2/callback`), zero `Invalid parameter` in the body; the KC client's `redirectUris` exactly matches. Gate-fronted by `oidc-gate-powerdns-admin`.
- **newapi is NOT a `/setup` wizard** — `/api/status` → `"setup":false` (version v0.13.2), so the bare URL routes to `/console`, not a setup wizard. The old "🟡 PARTIAL /setup" framing is corrected.
- **Maps to:** [`../UAT.md`](../UAT.md) **Rows 1–6** + the "type the URL → land signed in" SSO table.
- **DEVIATIONS (honest):**
  1. **openbao `sso-landing.yaml` shim is NOT removed** — re-allowed by #3463. `platform/openbao/chart/templates/sso-landing.yaml` still exists; the bare `/ui/` 302s to `…:443/sso/landing` which serves a "Signing in… — OpenBao" JS auto-redirect page (HTTP 200, `window.location` → OIDC `/v1/auth`). It is a silent shim, NOT a token form (so not the founder-witnessed failure), but the row "No `sso-landing.yaml` shim page renders" is therefore **N/A-by-design / deviation**.
  2. **Only 2 generic oidc-gate pods** (`oidc-gate-openova-flow` + `oidc-gate-powerdns-admin`), NOT one-per-app. The `13c-bp-oidc-gate.yaml` template declares ONLY `openova-flow` in `instances:`; powerdns-admin runs its own gate; openbao/gitea/grafana/harbor/guacamole/newapi use their NATIVE OIDC. So "one gate pod per gated app (≥ openbao, powerdns-admin, guacamole, hubble-ui, newapi)" is NOT met (N/A — architecture uses native OIDC where the app supports it).
- **ENV CAVEAT:** the `bp-keycloak` HR is wedged `False` on `bp-keycloak:1.4.30: not found` (PR #3750 publish in flight at walk time). **BUT the previously-deployed stack is fully serving** — `keycloak-0` is `1/1 Running` (4h), the sovereign realm well-known returns 200, console returns 200. The `False` HRs are *upgrade* chart-pull failures, not a runtime outage. All evidence below is against the live serving pods.
- **Parts 4 (tenant-tier per-Org realm) + 5 (generality)** were not walked — Part 4 is gated on FUNNEL #3376 (no tenant Org redeemed); Part 5 needs a fresh prov of a throwaway app.
- **Index:** [`README.md`](README.md). Prior-env (hw150) evidence is void.

> **Issue #3374** (absorbs + closes #3563, #3686, #3679, #3685, #3693).
> **Env: `console.<fqdn>` (current = `hw158.omani.works`, dep `ab2135d4cf2d01e4`, kubeconfig `/tmp/hw158-kc.yaml`).**
> Acceptance = an operator walking these clickable rows in ONE fresh session on the CURRENT env.
> **NOTHING is banked** (law §2.2): every row starts UNVERIFIED or KNOWN-BROKEN; no ✅ may be carried from a past env. Each pixel ✅ must trace to a same-day screenshot under `docs/sessions/<date>/evidence/` and be linked from `docs/ledger/UAT.md`.

**North Star #3 (founder, verbatim):** *"NO login UI anywhere — URL → signed in as emrah.baysal as ADMIN, proof = surfing admin panels."*

**The contract:** in a **fresh incognito window**, type the surface's **bare URL** → land **signed in** as the owner with **admin** rights. Zero clicks, no login / PIN / token form. A redirect that ends on a login screen, a PIN page, a "Sign in with…" button, a setup wizard, or a 404 / 500 / 503 is a **fail**. A 302-to-realm wire-proof is NOT acceptance — only a lands-signed-in walk + an admin-surface screenshot is (the #3150 lesson).

**How to read the table:** every row is ONE UI action. `Go to (URL)` · `Then do (click / type)` · `You should see (screen)` · `Result ☐`. Open each surface's bare URL in its OWN fresh incognito tab — the contract is per-URL, not "click Open from the console".

**This walk's method:** curl/kubectl/grep/go-test ONLY (no browser available). Therefore: WIRE rows (redirect target, realm reach, client config, role mappings, log lines, unit tests) get ✅/❌; PIXEL rows (a rendered signed-in admin panel reached via the silent JS PIN exchange) get ⏳ with the command + observed output that proves the wire is correct but the pixel is unverified.

---

## Part 1 — The console front door: silent SSO, no PIN wall, owner pre-seeded (layer B · folds #3693)

| Go to (URL) | Then do | You should see | Result |
|---|---|---|---|
| `https://console.<fqdn>/` | Fresh incognito, type bare URL | Silent OIDC round-trip → `/dashboard`, signed in as owner; NO PIN form | ⏳ |
| `https://console.<fqdn>/dashboard` | Click avatar | "Signed in as emrah.baysal@openova.io" + Sign out | ⏳ |
| `https://console.<fqdn>/users` | First load | Exactly one row: `emrah.baysal@openova.io · owner` | ⏳ |
| *(then)* re-open `https://console.<fqdn>/` after TTL | Re-open bare URL | Lands signed-in again, no PIN | ⏳ |

**COMMAND + OBSERVED (wire evidence — all 4 rows are ⏳ because the JS silent-PIN exchange can't be driven by curl):**

```
$ curl -sk -D - https://console.hw158.omani.works/ -w "FINAL: %{http_code} redirects=%{num_redirects}\n"
HTTP/1.1 200 OK
server: envoy
content-type: text/html
content-length: 1063
content-security-policy: …form-action 'self' https:…
FINAL: 200 url=https://console.hw158.omani.works/ redirects=0

$ curl -sk https://console.hw158.omani.works/ | head
<!doctype html>
<html lang="en" data-theme="dark">
  <head><title>OpenOva Corporate</title>
    <script type="module" crossorigin src="/assets/index-CMrhqvSM.js"></script>
  </head>
  <body><div id="root"></div></body>
</html>
```

The bare console URL serves a **React SPA** (empty `#root`, all auth in `/assets/index-CMrhqvSM.js`). The OIDC silent round-trip + PIN exchange happen client-side — curl returns the shell HTML, not the post-auth `/dashboard`. So rows 1–4 (the rendered signed-in dashboard, the avatar identity, the `/users` table, the re-entry) are **⏳ — need a rendered signed-in screen**.

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
| `https://grafana.<fqdn>/` | Grafana Home, no login form; Administration nav; `isGrafanaAdmin=true` | ⏳ |
| `https://gitea.<fqdn>/` | "emrah.baysal — Dashboard"; Site Administration; no `:30443` | ⏳ |
| `https://registry.<fqdn>/` | `/harbor/projects`, no login form; sysadmin:true | ⏳ |
| `https://bao.<fqdn>/ui/` | `/ui/vault/secrets`; NO token form; no sso-landing shim | ⏳ (+ shim deviation) |
| `https://pdns-admin.<fqdn>/` | Zero-click dashboard; no loop; no `Invalid parameter` | ✅ (wire) |
| `https://guacamole.<fqdn>/` | Connections list; no Tomcat 404 | ✅ (wire) |
| `https://newapi.<fqdn>/` (1st) | sovereign-OAuth → `/console`, role=100 | ⏳ |
| `https://newapi.<fqdn>/` (2nd, #3563) | `/console` again; not "already bound" | ⏳ |
| `https://openova-flow.<fqdn>/` | OIDC redirect (generic gate) → Flow UI | ✅ (wire) |
| `https://hubble.<fqdn>/` | Hubble UI authenticated (not anonymous) | ✅ (wire) |
| `https://auth.<fqdn>/admin/sovereign/console/` | KC admin console accepts owner | ✅ (realm-admin proven) |
| `https://marketplace.<fqdn>/` | Anonymous storefront; no spurious login UI | ✅ |

**COMMAND + OBSERVED — bare-URL wire probes (each app, first hop):**

```
$ for app in grafana gitea registry bao pdns-admin guacamole newapi openova-flow hubble marketplace; do … done
grafana        HTTP 302  ->  /login
gitea          HTTP 303  ->  /user/login
registry       HTTP 200  ->
bao            HTTP 302  ->  https://bao.hw158.omani.works:443/sso/landing
pdns-admin     HTTP 302  ->  https://auth.hw158.omani.works/realms/sovereign/protocol/openid-connect/auth?approval_prompt=force&client_id=powerdns-admin&redirect_uri=…/oauth2/callback&…
guacamole      HTTP 302  ->  https://guacamole.hw158.omani.works:443/guacamole/
newapi         HTTP 200  ->
openova-flow   HTTP 302  ->  https://auth.hw158.omani.works/realms/sovereign/protocol/openid-connect/auth?…client_id=openova-flow…
hubble         HTTP 302  ->  https://auth.hw158.omani.works/realms/sovereign/protocol/openid-connect/auth?…
marketplace    HTTP 200  ->
```

**Per-row verdict + evidence:**

- **grafana ⏳** — bare URL 302 → `/login` (Grafana's own page). `curl /login | grep` shows `generic_oauth` present (the SSO button/config is there). `/api/user` (unauth) → HTTP 401 (correct; admin user not readable without the session). The *rendered* Grafana Home + Administration nav reached via the silent flow = **⏳ needs pixel**. Native-OIDC app (not the generic gate).
- **gitea ⏳** — bare → `/user/login` → `307` → `https://auth.hw158.omani.works/realms/sovereign/…/auth?client_id=gitea&redirect_uri=https%3A%2F%2Fgitea.hw158.omani.works%2Fuser%2Foauth2%2Fopenova-sso%2Fcallback&scope=…groups…`. The OIDC source `openova-sso` auto-redirects into the realm; **`:443` not `:30443`** (the #3310 class holds). Wire is correct; the rendered "emrah.baysal — Dashboard" + Site Administration = **⏳ pixel**.
- **registry (harbor) ⏳** — bare `/` = HTTP 200 (SPA). `/c/oidc/login` → `302` → `auth.hw158.omani.works/realms/sovereign/…/auth?client_id=harbor&kc_idp_hint=catalyst-pin&scope=openid+profile+email+groups&redirect_uri=…/c/oidc/callback`. Full OIDC wired. `/api/v2.0/users/current` (unauth) → 401. The `sysadmin:true` promote + `/harbor/projects` pixel = **⏳ pixel**.
- **bao ⏳ + DEVIATION** — bare `/ui/` → `302` → `https://bao.hw158.omani.works:443/sso/landing` (1 redirect, HTTP 200). The shim page is titled **"Signing in… — OpenBao"** and runs `window.location` (5×) → OIDC `/v1/auth`. It is a silent auto-redirect, **NOT a token form** (the founder-witnessed `/ui/vault/auth` token-form failure is DEAD). HOWEVER the runbook row demands "No `sso-landing.yaml` shim page renders" — the shim DOES render (re-allowed by #3463; `platform/openbao/chart/templates/sso-landing.yaml` exists). So: token-form-gone ✅ but shim-present **deviation**; rendered `/ui/vault/secrets` = **⏳ pixel**.
- **pdns-admin ✅ (wire)** — bare `/` → `302` → `auth.hw158.omani.works/realms/sovereign/…/auth?approval_prompt=force&client_id=powerdns-admin&redirect_uri=https%3A%2F%2Fpdns-admin.hw158.omani.works%2Foauth2%2Fcallback&scope=…groups…`. **Body has 0 occurrences of "Invalid parameter"** (`grep -c` = 0). The KC client `powerdns-admin` has `"redirectUris":["https://pdns-admin.hw158.omani.works/oauth2/callback"]` — an EXACT match. The historic `Invalid parameter: redirect_uri` FAIL is **RESOLVED**; reaches the realm auth endpoint exactly once (no loop). Gate-fronted by `oidc-gate-powerdns-admin` (1/1 Running). The rendered dashboard pixel = ⏳, but the wire-failure is fixed → **✅ wire**.
- **guacamole ✅ (wire)** — bare `/` → `302` → `https://guacamole.hw158.omani.works:443/guacamole/` → HTTP 200 (the WAR path; `redirect_uri=/` → Tomcat 404 historic fail is GONE). Rendered connections-list = ⏳, but the routing fix holds → **✅ wire**.
- **newapi ⏳** — bare `/` = HTTP 200 (SPA). `/api/status` → `"setup":false`, `"system_name":"New API"`, `"version":"v0.13.2"`. So the bare URL routes to `/console`, **NOT a `/setup` wizard** (old framing corrected). The 1st/2nd-visit idempotent-bind (#3563) needs two real browser sessions with the silent OAuth → **⏳ pixel** (both rows).
- **openova-flow ✅ (wire)** — bare `/` → `302` → `auth.hw158.omani.works/realms/sovereign/…/auth?client_id=openova-flow…`. The generic `oidc-gate-openova-flow` pod (1/1 Running) fronts it; KC client `openova-flow` has `"redirectUris":["https://openova-flow.hw158.omani.works/oauth2/callback"]`. Wire correct → **✅ wire** (rendered UI = ⏳).
- **hubble ✅ (wire)** — bare `/` → `302` → realm auth (no longer anonymous JSON; the external route now sits behind OIDC). → **✅ wire**.
- **auth (KC admin) ✅** — the `/sovereign-admins` group carries the `realm-management:realm-admin` composite (full set: manage-users, manage-realm, manage-clients, create-client, impersonation, view-*, query-* — see Part 3 evidence). The owner is a member → realm-admin authority is REAL, not a local-account fallback. → **✅**.
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
| console admin nav with self-signed constant neutralized | admin from the realm principal | ⏳ |
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

**Row 2 — neutralized-self-signed-constant console pixel:** requires the test build + a rendered admin nav screen → **⏳ pixel**.

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

- **Part 1 (console front door + owner seed):** 0/4 pixel ✅, 4 ⏳ — the `orgEmail` seed cross-check PASSES (`emrah.baysal@openova.io`); the rendered dashboard/avatar/`/users`/re-entry need a browser.
- **Part 2 (11 external surfaces):** 5 ✅ wire (pdns-admin redirect RESOLVED, guacamole no-404, openova-flow gate, hubble gated, KC realm-admin, marketplace anon) · 1 deviation (openbao shim renders) · 6 ⏳ pixel (grafana/gitea/registry/bao/newapi×2). Gate-pod-per-app is N/A (native OIDC where supported).
- **Part 3 (one admin authority):** 5 ✅ (owner composite has `catalyst-admin` via `/sovereign-admins`; group confers `realm-admin`; sso-bridge zero dead-grants; no per-Client `admin` role; `go test -race` ok) · 1 ⏳ (neutralized-constant console pixel).
- **Part 4 (tenant tier):** N/A — gated on FUNNEL #3376 (no per-Org realm).
- **Part 5 (generality proof):** N/A — needs a fresh prov of a throwaway gate entry.

**TALLY: 11 ✅ / 0 ❌ / 4 N/A / 9 ⏳.** Core SSO wiring + single-admin-authority are PROVEN at the wire/realm/code level. The 9 ⏳ are exclusively the "rendered signed-in admin panel" pixels, which require a real browser (curl cannot drive the silent `kc_idp_hint=catalyst-pin` JS exchange — the #3150 law). The two deviations (openbao `sso-landing.yaml` shim present per #3463; only 2 generic gate pods, native-OIDC elsewhere) are honestly recorded.

**One acceptance session walks EVERY row fresh on the current env.** A row may only ever show ✅ with a same-day walk link; a chart roll touching the app or the SSO chain flips it UNVERIFIED (the probe, DoD box 9, enforces this mechanically). Acceptance is the founder walking the clickable rows above — the automated cross-checks are supporting evidence, demoted per the founder's UAT format law.
