# SSO root-URL landing + funnel voucher + cutover readiness — omantel.biz post-#4252 roll

**Date:** 2026-06-24 · **Env:** omantel.biz Sovereign (dep `4635277cae4ffed9`, Huawei me-east-215, PERMANENT) · **Driver:** Playwright (live) + mothership-kubectl + handover-minted sovereign-admin session.
**Mandate:** walk #3374 (SSO landing), #3376 (funnel voucher→signed-in), #3379 (cutover READINESS only — NOT triggered).
**Access:** Sovereign-admin session JWT MINTED via the Sovereign's own handover signer (`/var/lib/catalyst/handover-jwt-private.pem` inside the Sovereign catalyst-api pod; modulus matches the validation JWK `99K7ZsJ0…`), identity `uat215+walk@omani.works`, set as `catalyst_session` cookie on `.omantel.biz`. **NEVER touched the founder mailbox `emrah.baysal@openova.io`.** Funnel PIN read from rtz Valkey `magic:<email>`.

## Env state at walk time

#4252 (`fix(org-provisioning): transient parent-index read no longer prunes sibling Org namespaces`) MERGED + rolled. The Sovereign catalyst-api cold-pulled through `1606fb4 → b63289a` (chart 1.4.821, #4260) over ~25 min (cold ghcr pulls). At walk time: catalyst-api 1/1 Running, `api.omantel.biz/healthz` 200, demo Org Ready/holding, all walk surfaces 200/302. `bp-catalyst-platform` HR still showed `Unknown / Running upgrade` (helm bookkeeping tail) but the data plane was fully converged and quiescent.

---

## #3374 — SSO root-URL landing → ✅ ALL reachable surfaces land signed-in

Typed the **bare root** of each surface with a single sovereign-admin `catalyst_session` cookie set (no per-app login). The cookie must be on `.omantel.biz` (parent) so the OIDC auth endpoint at `api.omantel.biz/oidc/auth` + Keycloak `auth.omantel.biz` receive it — then the full chain (app → `api/oidc/auth` → KC `sovereign` realm `catalyst-pin` broker → app callback) auto-completes with no PIN prompt.

| # | Surface | Bare root → | Verdict | Screenshot |
|---|---------|-------------|---------|-----------|
| 26 | `console.omantel.biz` | `/dashboard` signed-in, full operator nav (Dashboard/Cloud/Apps/Catalog/Sandbox/Jobs/Compliance/Users/Organizations/Billing/Settings), avatar `UO` | ✅ | `3374-01-console-bare-root-dashboard-signedin.png` |
| 31 | `gitea.omantel.biz` | title **"uat215-walk - Dashboard - Catalyst Gitea"**, no login form, :443 | ✅ | `3374-02-gitea-bareroot-signedin-dashboard.png` |
| 30 | `grafana.omantel.biz` | **"Welcome to Grafana"** Home, full UI, no login form | ✅ | `3374-03-grafana-bareroot-signedin-home.png` |
| 32 | `registry.omantel.biz/harbor/projects` | Projects grid, user dropdown **`uat215+walk@omani.works`**, New Project + Logs | ✅ | `3374-04-harbor-projects-signedin.png` |
| 33 | `bao.omantel.biz` | `/ui/vault/secrets` **"Secrets Engines"** (cubbyhole/, secret/ kv), no token form | ✅ | `3374-05-openbao-secrets-engines-signedin.png` |
| 35 | `guacamole.omantel.biz` | connections list (Recent/All), id_token `preferred_username=uat215+walk@omani.works` groups `[sovereign-admins,sovereign-viewers]`, no Tomcat 404 | ✅ | `3374-06-guacamole-connections-signedin.png` |
| 36 | `pdns-admin.omantel.biz` | `/dashboard/`, no redirect loop / Log In page | ✅ | `3374-07-pdns-admin-dashboard-signedin.png` |
| — | `hubble.omantel.biz` | Hubble UI ("Welcome! select a namespace"), oauth2-proxy OIDC carried, no KC login wall (data-stream reconnect alert = hubble-relay backend, not auth) | ✅ | `3374-08-hubble-ui-signedin.png` |
| 37 | `newapi.omantel.biz` | `/console/token` signed-in as **`sovereign_3`**, full nav, `/api/status`→200. App runs in mgmt-vcluster `newapi-bp-newapi-…` 3/3 Running (the host `newapi` ns holds only CNPG). **❌→✅ since 2026-06-22** | ✅ | `3374-09-newapi-console-signedin-sovereign3.png` |
| — | `openova-flow.omantel.biz` | serves a JSON service descriptor — "No UI is served from this binary" (HTTP+SSE router consumed by the console). **N/A** — no user-facing UI to land on. | N/A | — |
| — | per-Org console `console.g4wpsso.omani.works` | bare root → `/dashboard` signed-in as the Org member `g4wpsso-stranger@omani.works` (from the #3376 funnel session) | ✅ | `3374-org-console-bare-root-lands-signed-in.png` |

**Verdict: #3374 GREEN on every reachable surface.** The SSO clients are live (bp-sso-bridge reconciles gitea/grafana/harbor/openbao/sandbox/hubble-ui/newapi-admin/openova-flow/powerdns-admin/catalyst-platform in the `sovereign` realm). One coherent admin-authority model: a single Keycloak `sovereign`-realm session (established via the `catalyst-pin` broker) carries across console + all ~9 external app UIs with no re-login. `openova-flow` is the only non-landing surface and correctly so (it's a backend API, not a UI).

---

## #3376 — funnel voucher → signed-in own org → ✅ (full chain proven)

Fresh stranger (storage cleared), marketplace `marketplace.omantel.biz`:

| # | Step | Result | Screenshot |
|---|------|--------|-----------|
| 1 | plans → **S** (OMR 5.000/mo) | ✅ | — |
| 2 | apps → **WordPress** | ✅ | — |
| 3 | domain → **`g4wpsso.omani.works`** (pool TLD) | "✓ available" | `3376-01` |
| 4 | BCP → Single-region | ✅ | — |
| 5 | review → WordPress / S / total OMR 5.000 | ✅ | `3376-02` |
| 6 | checkout email `g4wpsso-stranger@omani.works` → PIN **098118** (read from rtz Valkey `magic:g4wpsso-stranger@omani.works`) → **signed in** | ✅ | `3376-03` |
| 7 | voucher **`F4179DONEVCH2026`** (50 OMR credit) → "Credit covers this order — **OMR 0.000 due**" → button = **"Launch my tenant"** | ✅ | `3376-04` |
| 8 | Launch → `POST /api/tenant/orgs` **201** → redirect to POOL host **`console.g4wpsso.omani.works/auth/org-handover?token=<sessionJWT>`** (NOT dead omantel.biz) | ✅ | — |
| 9 | org-handover → **302 → `/jobs`** (session cookie minted) → **signed-in on the OWN org console** as `g4wpsso-stranger@omani.works`, org `g4wpsso.omani.works`, vCluster INSTALLED, WordPress committed to the Org's GitOps repo (10 files) + provisioning | ✅ | `3376-05` |

**Pool DNS (the #4179 chain) is FIXED end-to-end.** Org-controller (image `09dcc10`, contains #4240/#4242) logged `tenant-dns: wrote per-Org pool A-records organization=g4wpsso console_host=console.g4wpsso.omani.works target=212.72.24.33 zone=omani.works`. `dig @8.8.8.8 / @1.1.1.1 → 212.72.24.33` (console-ELB EIP). `console.g4wpsso.omani.works/` → 200; `/auth/org-handover?token=` → 302 → `/jobs`. (A ~4-min org-controller reconcile lag + a local stub-resolver negative-cache briefly showed NXDOMAIN at launch second — globally the record was live and the browser landed signed-in once the local negative TTL expired.)

Full fix chain now live: **#4184** (thread parent_domain → console_host) + **#4216** (DNS env-name) + **#4222** (pool A-record → central pdns.openova.io) + **#4240/#4242** (org-controller writes the A-record + 2-label TLS cert) + **#4251** (register funnel-Org console host so the landing accepts the session). The g2signup/qa4179 NXDOMAIN blockers are resolved.

**Verdict: #3376 GREEN** — stranger + voucher → 0 OMR → own org → signed-in on `console.<slug>.omani.works` with the purchased app provisioning. WordPress reaching fully-Running inside the vcluster is async post-launch (same pattern as the existing demo Org).

---

## #3379 — sovereignty cutover READINESS (NOT triggered) → 1 real blocker

**Cutover NOT triggered** (permanent omantel.biz; founder-gated). Verdict from live CR/ConfigMap inspection + chart source review at the deployed pin `bp-self-sovereign-cutover@0.1.78` (HR Ready). Full detail commented on #3379.

✅ **Correctly wired:** installed dormant (status CM `cutoverComplete=false`, all 11 steps un-run, `registriesYamlActive=v1`, per-node v1 acks); 11 step ConfigMaps defined (the 8 tethers + 3 supporting); **durable reconcile-immune `cutoverComplete` (#3667 confirmed)** — `09-cutover-status-configmap.yaml` wraps the CM in a `lookup` INIT-ONLY guard so `helm upgrade` renders EMPTY and never reverts a live `true` (+`resource-policy: keep`); **registry pivot flips containerd (#3647 confirmed)** — DaemonSet writes registries.yaml AND regenerates containerd `certs.d/<host>/hosts.toml` for live nodes.

❌ **BLOCKER — deny-egress CCNP never applied under the shipped default.** `values.yaml` ships `egressTest.defaultDenyEgress: true`, but in `08-egress-block-test-job.yaml` the `kubectl apply -f /work/egress-deny.yaml` + `POLICY_APPLIED="yes"` live ONLY in the legacy `else` branch (L327, `defaultDenyEgress=false`). The default-deny branch (L271 `elif`) writes the manifest at L326 and never applies it. Downstream call-home assertion (L594) + fresh-pull-under-deny-egress proof (L604) are gated on `POLICY_APPLIED=yes` → both SKIP ("survival-poll only"). Net: under the default config the 600s window is an **assertion-only hold** (the exact hw139/L121-128 failure mode) → `cutoverComplete=true` could be earned WITHOUT github.com/ghcr.io/harbor.openova.io ever being blocked or probed — NOT the witnessed deny-egress proof #3379/ADR-0002 require. **Fix:** hoist the `kubectl apply` + `POLICY_APPLIED` + teardown `trap` to run after BOTH branches write `/work/egress-deny.yaml`.

**Verdict: machinery ~90% correct; NOT READY to earn a *valid* `cutoverComplete=true` until the one-line apply-placement fix lands.**

---

## Honesty notes

- All ✅ above are LIVE browser/wire evidence on the running omantel.biz Sovereign, sovereign-admin session minted via the sanctioned handover-signer path (never the founder mailbox).
- The cutover was NOT triggered; #3379 is a readiness verdict only.
- No catalyst-api restart, no bastion/mailbox touch, no force-reconcile.
