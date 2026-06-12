# #3374 zero-click SSO — measured hw130 baseline + shipped mechanisms (2026-06-13)

Implementation session for [#3374](https://github.com/openova-io/openova/issues/3374).
Everything below is labeled **MEASURED-NOW** (walked live on hw130, read-only, this session)
or **PENDING-POST-MERGE-WALK** (mechanism shipped in the PR, not live-walkable until founder
review merges — zero-touch law, never hand-patch hw130).

## A. MEASURED-NOW — the TRUE state of all §3 rows (live walk + probe, fresh handover session)

Probe: [`scripts/sso-zero-click-probe.mjs`](../../../scripts/sso-zero-click-probe.mjs) →
[probe JSON](evidence/sso-probe-baseline-hw130.json). Screenshots in [evidence/](evidence/).

| # | Row | Fresh-session truth (probe) | With pre-existing PIN/KC session (browser walk) | Evidence |
|---|---|---|---|---|
| 1 | console | GREEN — handover → /dashboard signed in | same | [shot](evidence/row01-console-dashboard.png) |
| 2 | grafana | **RED — bounces to console PIN form** | GREEN zero-click + admin /admin/users | [landing](evidence/row02-grafana-landing.png), [admin](evidence/row02-grafana-admin-users.png) |
| 3 | gitea | **RED — same bounce** | GREEN zero-click + /admin walked | [landing](evidence/row03-gitea-landing.png), [admin](evidence/row03-gitea-admin.png) |
| 4 | harbor | **RED — same bounce** | GREEN zero-click + admin Users walked | [landing](evidence/row04-harbor-landing.png), [admin](evidence/row04-harbor-admin-users.png) |
| 5 | openbao | **RED — bare /ui/ = token form** (founder-witnessed) | RED — same; shim falls back to 1-click deep-link; OIDC click then lands signed in | [form](evidence/row05-openbao-BROKEN-token-form.png), [shim-degraded](evidence/row05-openbao-shim-DEGRADED-deeplink-fallback.png), [1-click-in](evidence/row05-openbao-oidc-1click-signedin.png) |
| 6 | pdns-admin | **RED — login form** | RED zero-click; 1-click /oidc/login → dashboard + full admin nav | [form](evidence/row06-pdns-admin-BROKEN-login-form.png), [1-click-in](evidence/row06-pdns-admin-oidc-dashboard-1click.png) |
| 7 | guacamole | **RED — bare / = Tomcat 404** | /guacamole/ zero-clicks signed-in BUT **NO admin** (#/settings bounces) | [404](evidence/row07-guacamole-BROKEN-root-404.png), [signed-in](evidence/row07-guacamole-warpath-signedin.png), [no-admin](evidence/row07-guacamole-settings-bounced-noadmin.png) |
| 8 | newapi | **RED — upstream connect timeout** (route dead) | same | [timeout](evidence/row08-newapi-BROKEN-upstream-timeout.png) |
| 9 | openova-flow | **RED — NO auth at all** (anonymous JSON API) | same | [anon-json](evidence/row09-openova-flow-UNAUTH-json.png) |
| 10 | hubble | **RED** — fresh: PIN bounce; with KC session: /oauth2/callback 500 `unauthorized_client` | RED | [500](evidence/row10-hubble-BROKEN-oauth2-callback-500.png) |
| 11 | keycloak admin | **RED — master-realm local form only** | same | [form](evidence/row11-keycloak-admin-BROKEN-local-form.png) |
| 12 | marketplace | GREEN — anonymous storefront, no login form (by design) | same | [storefront](evidence/row12-marketplace-anonymous-storefront.png) |

**Honest bottom line: 3/13 probe rows GREEN on a fresh session.** The earlier "grafana/gitea/harbor
work" belief was a shared-browser artifact (pre-existing KC session) — law §2.2 vindicated.

## B. Root causes found (each cited to code/live evidence)

1. **Handover session cookie host-only** — `auth_handover.go` set `catalyst_session` with no
   `Domain`, so `api.<fqdn>/oidc/auth` (catalyst-pin provider) never saw it → EVERY zero-click
   chain from a fresh handover bounced to the PIN form. The PIN-verify path was fixed on hw86
   (G113, auth.go); the handover path missed it. Diagnosed with a cookie dump:
   `COOKIE domain=console.hw130.omantel.biz name=catalyst_session`.
2. **openbao**: #3231 shim fires only from the console button; on hw130 it ALSO degrades to the
   deep-link because catalyst-api has no `CATALYST_OPENBAO_ADDR` (main.go required addr+token;
   chart never set either). Bare `/ui/` had no interception at all.
3. **pdns-admin**: the 0.1.10 `/login` redirect decapitated PDA's own OIDC session exchange —
   verified against the LIVE pda-legacy 0.4.2 source: `/oidc/authorized` stores the token then
   302s to `/login`, where `if 'oidc_token' in session:` mints the app session; authed `/login`
   goes STRAIGHT to `/dashboard/` (never revisits `/`). Root-only interception is loop-free.
4. **guacamole**: WAR at `/guacamole/`; bare root = Tomcat ROOT context 404 (TBD-G6 never shipped).
   Admin gap: chart ships the openid extension ONLY — `rolesFromGroup: sovereign-admins: admin`
   (blueprint.yaml:115) has no permission store to land in.
5. **newapi**: K8s NetworkPolicy admits only traefik/catalyst-system namespaces; Cilium Gateway
   traffic carries the reserved `ingress` ENTITY no K8s selector can express → timeout. Plus
   OIDC issuer pointed at the non-existent `ops` realm and `newapi-oidc` is a chart-local
   placeholder KC never learned.
6. **hubble**: bp-cilium helm-generates its own client-secret; the realm import derives a
   different one; sso-bridge never managed `hubble-ui` (no published bundle in flux-system,
   unlike the 5 Tier-1/2 apps) → `unauthorized_client` on every code exchange.
7. **keycloak admin**: KC root → /admin/master/console/ → master realm has no catalyst-pin
   broker → local form is the only path.
8. **openova-flow**: API router exposed with no auth of any kind (slot-56 intent: cross-cluster
   emitters POST over public HTTPS).

## C. PENDING-POST-MERGE-WALK — mechanisms shipped (this PR)

| Row | Mechanism | Where |
|---|---|---|
| ALL | handover cookie Domain=.&lt;fqdn&gt; (mirrors G113 PIN fix) + test | `products/catalyst/bootstrap/api/internal/handler/auth_handover.go` |
| 5 | HTTPRoute 302 Exact /, /ui, /ui/ → openbao-sso-init shim (loop-safe; port 443; hostname rewrite) + `CATALYST_OPENBAO_ADDR` env (Sovereign-only) + token-optional `OIDCAuthURL` (auth_url is unauthenticated) + oidc mount `listing_visibility=unauth` | bp-openbao 1.2.29, bp-catalyst-platform 1.4.596, catalyst-api |
| 6 | HTTPRoute 302 Exact / → /oidc/login ONLY (callback-aware; /login + /oidc/* never touched) | bp-powerdns-admin 0.1.12 |
| 7 | HTTPRoute 302 Exact / → /guacamole/ (admin-elevation gap documented, NOT closed — needs a JDBC permission store) | bp-guacamole 0.2.5 |
| 8 | CiliumNetworkPolicy `fromEntities: [ingress]` (Capabilities-guarded) + sso-bridge AppRegistration (`newapi-admin` in SOVEREIGN realm) + Merge-policy ExternalSecret onto `newapi-oidc` + slot-80 issuer → realms/sovereign | bp-newapi 1.4.64 |
| 9 | bp-oidc-gate (NEW, slot 13c): generic oauth2-proxy instance set; flow instance gates browser reads, skip-auth-routes the emitter write paths, --skip-jwt-bearer-tokens; flow's own route disabled in slot 56 | platform/oidc-gate 0.1.0 |
| 10 | oauth2Proxy.ssoBridgeSync: AppRegistration adopts the realm client + ExternalSecret feeds the LIVE KC secret to the proxy | bp-cilium 1.4.1 |
| 11 | realm import: /sovereign-admins → realm-management `realm-admin`; HTTPRoute 302 / → /admin/sovereign/console/ (master console untouched at full path) | bp-keycloak 1.4.27 |
| probe | `scripts/sso-zero-click-probe.mjs` + `.github/workflows/sso-probe.yaml` (dispatch + nightly) | scripts/ + CI |
| UAT law | §1 rebuilt (16 rows, markers); `scripts/uat-sso-flip.py` + `.github/workflows/sso-uat-flip.yaml` flips rows UNVERIFIED on every SSO-chain merge | docs/ledger/UAT.md + CI |

## D. Probe negative validation (DoD-6)

Run 2026-06-13 against live hw130:
`--expect-broken grafana,gitea,harbor,openbao,pdns-admin,guacamole,newapi,openova-flow-anon-denied,hubble,keycloak-admin`
→ `MATCH`, exit 0 — the probe reds on exactly the broken rows and greens on
console/openova-flow(authed)/marketplace.

## E. Known gaps carried (honest, not hidden)

- **guacamole admin** (DoD-1 row 7 half): needs the JDBC/postgres extension + ADMINISTER
  seeding — a feature, not a config flip. Tracked on #3374.
- **newapi zero-click entry**: reachability + real OIDC client shipped; whether new-api's UI
  auto-initiates OIDC (or needs a root redirect to its OIDC entry) is only measurable
  post-merge once the route is alive.
- **emitter tokens for openova-flow writes**: gate skip-auth-routes preserve today's
  machine-write surface; minting issuer JWTs for emitters is the follow-up.
- **DoD-7 tenant tier**: gated on FUNNEL #3376 — untouched, not faked.
- Pre-existing test failures on origin/main (NOT introduced here):
  `TestLoad_TenConcurrentDeploymentsAreIsolated`, `TestLoad_RejectsInvalidInputUnderConcurrency`.
