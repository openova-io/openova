# Silent-SSO PATHFINDER — Grafana end-to-end + replication playbook

> **Tracking:** #3150 (Pillar 1/4 — single-click silent SSO from the console "Open" button).
> **Pathfinder app:** grafana on live hw124 (`*.hw124.omani.works`).
> **For:** the 6 follow-on agents (catalyst-platform/console, harbor, openbao, guacamole, powerdns-admin, netbird). Follow this verbatim.

---

## 0. The single acceptance bar

In the console, click **Open** on an app → a new tab lands you **inside the app already logged in** — no app login form, no "Sign in with OpenOva" button. That is the ONLY definition of done. A launch-url that returns the right path is necessary but not sufficient; the witnessed landed-in-app screenshot is the proof.

---

## 1. Root cause (confirmed live, 3 layers)

| Layer | Symptom | Cause |
|---|---|---|
| **1 — no app resolves** | `GET /catalyst/v1/apps/{id}/launch-url` 404s for grafana | `HandleGetLaunchURL` resolved `{id}` ONLY as an Application CR `metadata.uid`. Bootstrap-kit apps (grafana, harbor, openbao, gitea, keycloak, guacamole, powerdns-admin, netbird) install as bare **HelmReleases with NO Application CR** → no uid → 404 → console "Open" falls back to the plain `externalURL` → app shows its login form. (`kubectl get applications.catalyst.openova.io -A` on hw124 = **empty**.) |
| **2 — wrong URL shape** | Even when an endpoint resolves, the URL was `https://<host>/?prompt=none&kc_idp_hint=catalyst-pin` | Those are **Keycloak** params placed on the **app root**. Grafana (and Harbor, etc.) ignore unknown root-level query params and render their own login form. Silent SSO needs the app's **OIDC-init route** (Grafana `/login/generic_oauth`), which 302s to Keycloak itself. The endpoint struct had no field to express this. |
| **3 — direct-visit shows login form** | Visiting `https://grafana.<fqdn>` shows the Grafana login form | Grafana `grafana.ini` ships `auto_login=false` / `oauth_auto_login=false` **deliberately** (see §6). The OIDC half is otherwise correctly wired — clicking "OpenOva SSO" manually already logs in silently — so the gap is ONLY the console launch landing on the right route. |

**Confirmed-good half:** grafana's `envFrom: grafana-sso-oidc-credentials` ExternalSecret is `SecretSynced/Ready=True`, and the synced `GF_AUTH_GENERIC_OAUTH_AUTH_URL` already carries `?kc_idp_hint=catalyst-pin`. So we do NOT append `kc_idp_hint` to the launch URL — it's already baked into the app's own auth_url. The launch URL just needs to land the browser on the app's OIDC-init route.

---

## 2. The fix — every file changed and why

### 2a. Backend — `products/catalyst/bootstrap/api/internal/handler/endpoint_handler.go`

1. **New schema field `SSOInitPath`** on `endpointDecl` (the YAML/JSON projection of `Blueprint.spec.endpoints[]`) and on the wire-shape `resolvedEndpoint`. This is the app-local OIDC-init path (grafana `/login/generic_oauth`).

2. **`buildLaunchURL(hostname, tls, ssoInitPath)`** — added the third arg:
   - `ssoInitPath != ""` → returns `https://<host><ssoInitPath>` with **NO query string** (leading slash auto-normalised). The browser hits the app's OIDC-login route → app 302s to Keycloak with its pre-baked `kc_idp_hint` → silent PIN re-use → lands inside the app.
   - `ssoInitPath == ""` → legacy `https://<host>/?prompt=none&kc_idp_hint=catalyst-pin` (unchanged → back-compat).

3. **`HandleGetLaunchURL` resolves HR-backed apps.** When `findApplicationByUID(uid)` misses, the handler treats `{id}` as a blueprint/release name (`strings.TrimPrefix(uid, "bp-")`), resolves the Blueprint via the existing `fetchBlueprint` (which **already chains to the in-cluster Blueprint CR** via `catalog_client_cluster_fallback.go` → `getFromCluster`, trying both `grafana` and `bp-grafana`), picks the endpoint, and builds the launch URL. Org is empty for these Sovereign-singleton apps — correct, because their `hostnameTemplate` (`grafana.{{.SovereignFQDN}}`) doesn't reference an Org slug. App-not-found is still surfaced when the id names neither a CR nor a known blueprint.

4. **`resolveEndpoints`** (the `GET /apps/{id}/endpoints` list path) also threads `ep.SSOInitPath` into the resolved endpoint + its `LaunchURL`, so the Endpoints tab is consistent.

### 2b. Frontend — `products/catalyst/bootstrap/ui/src/pages/sovereign/AppDetail.tsx`

The launch button (`LaunchButton`) was keyed on `appUID`, which is empty for bootstrap apps → it short-circuited to the plain `externalURL`. Added:

```ts
const launchKey = appUID || (appIsBootstrap ? componentId : '')
```

`appIsBootstrap` comes from the backend GET-application response (`bootstrap:true`, set by `synthesiseAppFromHelmRelease` in `applications.go`). `componentId` is the blueprint/release name (e.g. `bp-grafana` — the BE strips the prefix). `launchKey` is threaded through `OverviewPanel` into `LaunchButton`. Non-bootstrap apps keep using the CR uid exactly as before. `getLaunchURL` already `encodeURIComponent`s the key.

### 2c. Blueprint — grafana endpoint declares `ssoInitPath`

Two files, BOTH required:

- **`platform/grafana/blueprint.yaml`** — the canonical source-of-truth blueprint. Added `ssoInitPath: "/login/generic_oauth"` to the `ui` endpoint.
- **`products/catalyst/chart/templates/catalog-seed/blueprints.yaml`** ⚠️ **(owned by the CATALOG agent — flag any edit)** — this is the **load-bearing** file at runtime: it seeds the in-cluster `bp-grafana` Blueprint CR that `fetchBlueprint`→`getFromCluster` reads at request time. Added the same `ssoInitPath` line to the `bp-grafana` `ui` endpoint. **Without this edit the live walk does NOT change**, because the API reads the seeded CR, not `platform/grafana/blueprint.yaml`.

> 🔑 **Replication gotcha #1:** the in-cluster Blueprint CR is what the launch-url reads. For apps seeded by `catalog-seed/blueprints.yaml` (grafana, keycloak, guacamole, …) you MUST edit that file (coordinate with the catalog agent). For apps whose CR comes from elsewhere (harbor, openbao, powerdns-admin, netbird are NOT in catalog-seed today — see §5), find where their Blueprint CR is sourced first (`kubectl get blueprints.catalyst.openova.io <name> -o yaml` shows `metadata.annotations` / `managedFields` pointing at the owner).

### 2c-bis. ⚠️ CRD schema — `ssoInitPath` MUST be in the Blueprint CRD (else it's pruned)

**The pathfinder MISSED this and it broke the live walk.** `Blueprint`'s endpoint schema in `products/catalyst/chart/crds/blueprint.yaml` is structural with NO `x-kubernetes-preserve-unknown-fields`. If `ssoInitPath` is not declared in the CRD, the API server **prunes** it on every apply — the seed ships it, Helm applies it, the **live CR silently loses it**, and `buildLaunchURL` falls back to the legacy app-root URL. The launch-url returns the OLD shape and the app shows its login form, with NO error anywhere.

**FIXED in #3163:** `ssoInitPath` added beside `ssoEnabled` in the shared (anchored) endpoint schema → covers BOTH `v1` and `v1alpha1`. **The 6 follow-on agents do NOT touch the CRD again — it's done once for all apps.**

**BUT — existing Sovereigns need a direct CRD apply.** Helm `crds/` are **install-only**; a chart upgrade does NOT update the CRD. So on hw124 (and any existing Sovereign) you MUST run, once:
```bash
kubectl --kubeconfig /tmp/hw124.kubeconfig apply -f products/catalyst/chart/crds/blueprint.yaml
```
Then the seed CR's `ssoInitPath` stops being pruned. **Verify it persisted** before trusting the launch-url:
```bash
kubectl get blueprints.catalyst.openova.io bp-<app> -o json | python3 -c "import sys,json;print([e.get('ssoInitPath') for e in json.load(sys.stdin)['spec']['endpoints'] if e.get('name')=='ui'])"
# must print your path, not [None]
```

### 2d. Contract — `docs/api/catalyst-api-openapi.yaml`

Added `ssoInitPath` to `EndpointDeclaration` and documented the launch-url's dual-id resolution (uid OR blueprint name) + the two URL shapes.

### 2e. Tests

- BE (`endpoint_handler_test.go`): `TestBuildLaunchURL_SSOInitPath` (path emission + leading-slash normalisation + empty-fallback), `TestGetLaunchURL_HRBacked_NoApplicationCR`, `TestGetLaunchURL_HRBacked_BpPrefixedID`, `TestGetLaunchURL_HRBacked_NoInitPathFallsBackLegacy`.
- FE (`AppDetail.test.tsx`): bootstrap app with no uid keys the launch-url on the blueprint name and opens the OIDC-init URL (not externalURL).

---

## 3. How to find each app's OIDC-init path

The launch URL must land on the route that **the app itself** treats as "start an OIDC login", which then redirects to Keycloak. This is app-specific. Derivation method:

1. **Read the app's OIDC/OAuth config** in `platform/<app>/chart/values.yaml` (or its `grafana.ini` / env block). Identify the OAuth provider plugin/module the app uses.
2. **The init path is the provider's login-initiation route**, NOT the callback/redirect-uri (the callback is where Keycloak sends the browser BACK; you want where the browser STARTS). Map by app framework:

| App | OIDC-init path | How derived |
|---|---|---|
| **grafana** | `/login/generic_oauth` | Grafana's generic OAuth provider login route (the "Sign in with <name>" button posts here). Callback is `/login/generic_oauth` too but a GET with no code starts the flow → 302 to Keycloak. |
| **harbor** | `/c/oidc/login` | Harbor core's OIDC onboarding route. The Harbor login page's "LOGIN VIA OIDC PROVIDER" button hits `/c/oidc/login`; callback is `/c/oidc/callback`. |
| **openbao** (Vault UI fork) | UI is SPA — OIDC is started from the UI, NOT a server route. **Use `auto_login` / a `?with=oidc` deep-link instead** (see §5 openbao note). Likely value: `/ui/vault/auth?with=oidc%2F` (the OIDC auth-method deep link). VERIFY against the running UI. |
| **guacamole** | `/` with the OpenID extension, OR `/#/?` — guacamole's OpenID extension auto-redirects from the root when `openid-authorization-endpoint` is set and no other auth succeeds. **Test whether bare-root already redirects** before adding a path; if it shows a form, the init route is the tomcat context root with the openid extension active. |
| **powerdns-admin** | `/oidc/login` (Flask-based; `authlib` blueprint registers `/<provider>/login` or `/oidc/login`). Confirm the exact registered route name from the running app's `url_map`. |
| **netbird** (dashboard) | SPA — OIDC started client-side via the dashboard's auth0/oidc-client config. Like openbao, prefer the dashboard's auto-redirect; the init "path" may just be `/` if the dashboard auto-starts the PKCE flow. VERIFY. |

3. **Verify the path empirically** before committing: `curl -sI "https://<app>.hw124.omani.works<candidate-path>"` — a correct init path returns **302** with a `Location:` header pointing at `https://auth.hw124.omani.works/realms/sovereign/protocol/openid-connect/auth?...`. If it returns 200 (HTML login form) the path is wrong.

> 🔑 **Replication gotcha #2 — SPA apps (openbao, netbird, guacamole).** Server-side apps (grafana, harbor, powerdns-admin) have a clean GET route that 302s. SPA apps start OIDC in JavaScript, so there may be NO server route to land on. For those, the launch strategy is either (a) a deep-link query the SPA recognises (`?with=oidc`), or (b) enabling the app's `auto_login`-equivalent so the SPA auto-starts the flow on load. Decide per-app and DOCUMENT which strategy you used. Do NOT assume `/login/generic_oauth` generalises.

---

## 4. Deploy + verify recipe (per app)

### Deploy

1. Edit the app's endpoint in the in-cluster-seeding file (catalog-seed or the app's blueprint source) to add `ssoInitPath: "<path>"`. **Coordinate with the catalog agent if it's `catalog-seed/blueprints.yaml`.**
2. If you changed any BE/handler code, bump `bp-catalyst-platform` `Chart.yaml`, commit signed + conventional, PR body `Refs #3150` (NEVER Closes/Fixes; avoid CI-banned words MVP/iterate/out-of-scope/blocker), squash-merge on green CI so CI builds the catalyst-api image + chart. **Verify the deploy-bot chart bump carries your new image.**
3. Roll hw124 (see §exact commands below): refresh the HelmRepository index, patch the HR `spec.chart.spec.version` to the newest published version that includes your merge (read the LIVE version first — Flux serializes reconciles; the higher cumulative version wins), annotate `reconcile.fluxcd.io/requestedAt`, poll until the **new** catalyst-api pod is Ready (a rollout serves OLD pods until the new one is Ready).

### Verify (witnessed — code-read is NOT acceptance)

1. Launch-url returns the OIDC-init path:
   ```bash
   curl -s -H "Authorization: Bearer <session-jwt>" \
     "https://console.hw124.omani.works/sovereign/api/v1/.../apps/bp-<app>/launch-url" | jq .url
   # expect: https://<app>.hw124.omani.works<ssoInitPath>
   ```
2. **Playwright walk:** log into `console.hw124.omani.works` (PIN-via-email), navigate to the app, click **Open**, confirm the new tab lands on the app's dashboard with **no login form**. Screenshot → `docs/sessions/2026-06-09/evidence/`.
3. Fallback proof if console-session is unobtainable: launch-url returns the correct OIDC-init path AND a `curl -L` following it WITH a Keycloak session cookie lands authenticated (302→302→200 app dashboard).

### Exact hw124 roll commands

```bash
export KUBECONFIG=/tmp/hw124.kubeconfig
# 1. read live chart version (do NOT assume)
kubectl -n flux-system get hr bp-catalyst-platform -o jsonpath='{.spec.chart.spec.version}'
# 2. refresh the index so the new version is discoverable
kubectl -n flux-system annotate helmrepository <repo> reconcile.fluxcd.io/requestedAt="$(date +%s)" --overwrite
# 3. bump to the newest published version that includes your merge
kubectl -n flux-system patch hr bp-catalyst-platform --type merge \
  -p '{"spec":{"chart":{"spec":{"version":"<NEW>"}}}}'
# 4. force reconcile
kubectl -n flux-system annotate hr bp-catalyst-platform reconcile.fluxcd.io/requestedAt="$(date +%s)" --overwrite
# 5. poll until the NEW catalyst-api pod is Ready
kubectl -n catalyst get pods -l app=catalyst-api -w
```

---

## 5. Per-app replication recipe (the 6 follow-on agents)

| App | launchKey (id) | ssoInitPath | Blueprint CR source | Strategy | Notes / gotchas |
|---|---|---|---|---|---|
| **catalyst-platform / console** | `bp-catalyst-platform` (or the console blueprint name) | the console is the Catalyst console itself — **it is the OIDC client the operator is ALREADY logged into**. Its "launch" is trivial/n/a; verify it doesn't need a separate silent-SSO (the operator session already covers it). Confirm whether a distinct grafana-shaped launch even applies. | catalog-seed? confirm | likely n/a | Probably the easiest "win" but also the most likely to be a no-op — verify the requirement is real before shipping a change. |
| **harbor** | `bp-harbor` | `/c/oidc/login` | `platform/harbor/blueprint.yaml` (NOT in catalog-seed — confirm the live CR source with `kubectl get blueprints.catalyst.openova.io bp-harbor -o yaml`) | server-route | Harbor's `ui` endpoint is `ssoEnabled:true`; the `registry` endpoint is `ssoEnabled:false` (OCI clients use robot/token auth, not silent SSO) — do NOT touch registry. |
| **openbao** | `bp-openbao` | SPA — likely `/ui/vault/auth?with=oidc%2F` OR enable an auto-redirect | confirm CR source | **SPA — derive empirically** | Vault-UI fork; OIDC starts client-side. The init path is a UI deep-link, not a server GET. Test with curl: a correct deep-link still returns the SPA shell (200) but the SPA then auto-starts OIDC — so the witnessed Playwright landing is MANDATORY here (curl can't confirm). |
| **guacamole** | `bp-guacamole` | test bare-root first; the openid extension may auto-redirect | `catalog-seed/blueprints.yaml` line ~1526 (⚠️ catalog agent) | server-redirect-on-root | `ui` endpoint hostname `guac.{{.SovereignFQDN}}`, `ssoEnabled:true`, `launchDefault:true`. If bare-root already 302s to Keycloak, `ssoInitPath` can stay empty + the legacy shape works; if it shows the guac form, find the openid extension's init route. |
| **powerdns-admin** | `bp-powerdns-admin` | `/oidc/login` (confirm from Flask url_map) | confirm CR source (not in catalog-seed) | server-route | Flask/authlib; the registered route name varies by config (`/oidc/login` vs `/<provider>/login`). Confirm before committing. |
| **netbird** | `bp-netbird` | SPA — likely `/` auto-start or a deep-link | confirm CR source | **SPA — derive empirically** | Dashboard is a React SPA using oidc-client PKCE. May auto-start on load (init path `/`), or need a query param. Witnessed Playwright landing MANDATORY. Note: bp-netbird has historically been DEFERRED (see memory `reference_dependency_graph_audit_red_netbird_gated`) — confirm it's actually installed on hw124 before walking. |

**keycloak** is intentionally `ssoEnabled:false` (`auth.{{.SovereignFQDN}}` endpoint) — Keycloak IS the IdP; you don't silent-SSO into the IdP itself. Do NOT add an init path there.

### The per-app loop each agent runs

1. `kubectl -n flux-system get hr bp-<app>` — confirm the app is installed on hw124.
2. `kubectl get blueprints.catalyst.openova.io bp-<app> -o jsonpath='{.spec.endpoints}'` — read its endpoints; find the `ssoEnabled:true launchDefault:true` UI endpoint.
3. Derive the OIDC-init path (§3) and **verify with `curl -sI`** that it 302s to Keycloak.
4. Add `ssoInitPath` to the endpoint in the **CR-seeding file** (find it — §2c gotcha #1). Coordinate with the catalog agent if it's `catalog-seed/blueprints.yaml`.
5. No BE change needed (the #3150 BE handles all apps generically) UNLESS your app is an SPA needing a different launch strategy — then justify + extend.
6. Roll hw124 to the chart version carrying the new seed (§4).
7. **Witnessed Playwright walk** → screenshot → `docs/sessions/2026-06-09/evidence/`. Code-read is NOT done.

---

## 6. Layer-3 decision (grafana) — do NOT flip `auto_login`

**Decision: rely SOLELY on the launch-url OIDC-init path. Keep grafana `auto_login=false` / `oauth_auto_login=false`. No bp-grafana chart change.**

Rationale:

1. **`auto_login=true` would force EVERY direct visit** to `grafana.<fqdn>` (bookmarks, health probes, the operator typing the bare URL) into an immediate OIDC redirect. The grafana chart **deliberately** leaves the OIDC `auth_url`/`token_url`/`api_url` blank by default (`platform/grafana/chart/values.yaml:157-162`: *"The chart leaves them blank by default so a half-configured signin attempt errors clearly rather than wedging the grafana login UI"*). With `auto_login=true`, a half-configured overlay would wedge the login UI into a redirect loop with **no escape hatch**. That is the exact failure the existing `false` default was chosen to prevent.
2. **The acceptance bar is "console Open → lands logged in"** — the launch-url OIDC-init path achieves precisely that, surgically (only on the operator's click), without changing default-visit behaviour.
3. **Keeping `disable_login_form:false` + `auto_login:false` preserves break-glass local-admin login** if Keycloak itself is down — operationally important.

So the grafana fix touches the **catalyst-platform chart only** (BE + blueprint CR seed). The bp-grafana chart is untouched.

> For SPA follow-on apps (openbao, netbird) the calculus differs — they may have no server-side init route, so an `auto_login`-equivalent might be the only option. Decide per-app and document the same trade-off (escape-hatch vs auto-redirect).

---

## 7. Anti-theater checklist (so the follow-on agents don't fake it)

- ❌ A launch-url that returns the right path but no witnessed landing = NOT done.
- ❌ `curl -I` returning 302 for an SPA app = NOT sufficient (SPA starts OIDC in JS; the 302 might be the SPA shell, not the OIDC bounce). SPA apps REQUIRE the Playwright screenshot.
- ❌ Editing `platform/<app>/blueprint.yaml` but NOT the in-cluster CR seed = the live walk won't change. The seed is load-bearing.
- ✅ Done = console Open → app dashboard, no login form, screenshot in `evidence/`.
