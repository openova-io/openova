# UAT — OpenOva Catalyst — fresh acceptance page (RESET 2026-06-11 — pending hw128)

> **Tests-first.** Fresh **2-region** prov **hw101** replaces wiped hw99. Every row is `☐` **pending** until walked LIVE on the **production React tree** (`products/catalyst/bootstrap/ui/`) on the real Sovereign — never a mock, never the dead Svelte console (`products/catalyst/console/`), never a CLOSED/merged status. A row earns its evidence link ONLY when it is actually walked. Authored 2026-06-08 after hw99 (1 functional cluster + falsified 2-region evidence) was wiped on founder order. **Sandbox is OUT of scope.**
>
> **This page is PER-APPLICATION.** Generic "works for grafana/gitea/harbor/openbao" rows are banned — every `ssoEnabled` app gets its **own** explicit single-click-SSO row (§2), and every app carrying a non-trivial topology gets its **own** explicit topology row (§3). The per-app data below is **extracted from `platform/*/blueprint.yaml`** (`spec.sso`, `spec.endpoints[].ssoEnabled`, `spec.topology.defaults.multi-region`, `spec.multiInstance.enabled`) and cross-checked against the topology audit + the #2744 SSO-fan-out tiers.
>
> Sources: 5-pillar DoD (`../DOD.md`); G117 EPIC #2737 (`#2740/#2741/#2742/#2743/#2744/#2745/#2674`); SSO fan-out tiers #2744; spire gap #3084; topology matrix `../sessions/2026-06-02-per-blueprint-topology-audit.md` (10 active-hot-standby / 14 active-active / 20 active-passive / 44 singleton).
>
> Legend: ✅ pass · ◑ partial · ❌ fail · ⛔ blocked · ☐ pending (default).

## ✅ Operator acceptance walk — 3 spot-checks (2026-06-09, hw124 `90a98a78f35846a3`)

Founder's streamlined acceptance (in lieu of all 121 canonical rows below). Walk on the **child**: `console.hw124.omani.works` / `marketplace.hw124.omani.works`. Results recorded by the operator.

| # | Check | What to do | Expected | Result |
|---|---|---|---|---|
| **A1** | Silent SSO — page 1 | console → Apps → an `ssoEnabled` app (e.g. grafana) → click **Open** | New tab lands **ALREADY signed in** — no login form, no 2nd click | ✅ **(operator-confirmed, 2026-06-09)** — grafana Open lands **already signed in, zero Keycloak UI**. Took a 5-layer fix: launch-url OIDC-init path + HR-app resolution (#3161), CRD `ssoInitPath` un-prune (#3163), createUser stop stamping UPDATE_PASSWORD (#3167), AND the **realm catalyst-pin IdP + auto-delegation + auto-link flow** which config-cli never imported on hw124 (applied via admin API; **durable follow-up: harden config-cli realm import / add a post-prov IdP-exists guard**). The catalyst-pin IdP is **realm-level → shared by all SSO apps**; fan-out in progress. |
| **A2** | Silent SSO — page 2 | console → Apps → a 2nd `ssoEnabled` app (e.g. gitea) → click **Open** | New tab lands **ALREADY signed in** | ✅ (2026-06-10, #3150) — gitea silent-SSO now lands the owner on the **authenticated Gitea Dashboard**, NO login page. See SSO-gitea row below for the logged-in walk + evidence. |
| **B** | Committed topologies met | console → Cloud + per-app **Topology** tab | Each app's region placement matches its committed topology (active-hot-standby / active-active / singleton) | ❌ (operator) — bp-openbao still **single-region**; verified: every app's pods in region -a only, `-b` is a separate independent cluster. **Multi-region app topology NOT wired.** |
| **C** | New catalog pages in place | console → Apps → **Catalog** tab (+ class page `/catalog/$bp`) | New catalog pages render | ◑ (2026-06-10) — **data-GET 404 FIXED + extended to the PostgreSQL App-card.** hw124 chart **1.4.552/1.4.553**: `GET /api/v1/catalog/bp-postgres` → **200** (was **404** — the gap flagged this session; #3191 seeded only `blueprints.json`, not the in-cluster catalog-seed the API serves), `kubectl get blueprints` → **78 CRs** incl `bp-postgres` (`visibility=listed title=PostgreSQL`). Control `bp-does-not-exist` → 404 proves the 200 is a real hit. Fixed #3199 (catalog-seed CR) + unwedged #3200/#3201. Evidence: `docs/sessions/2026-06-10/evidence/hw124-catalog-bp-postgres-200.md`. (Earlier: #3159/#3160 fixed bp-alloy → 200; CR count 14→35→**78**.) **Operator UI-walk (click Catalog → class page renders) still pending** a console session (headless agent hits the PIN-login wall). |
| **BONUS** | vcluster containment | non-foundational namespaces should live in rtz/dmz/mgmt vclusters | only foundational ns in host; everything else in a vcluster | ❌ (operator) — verified **172 pods in the HOST**, vclusters rtz/dmz/mgmt ~empty. Isolation architecture not working. |

> The full 121-row per-app canonical table is **preserved below** (it is mandated by this doc's "one row per ssoEnabled app" rule) — these 3 are the operator's fast acceptance pass, not a replacement.

## 0. Session results — hw124 (`90a98a78f35846a3`), walked 2026-06-09 — CLEAN ZERO-TOUCH 2-REGION

**Env state:** hw124 is the clean re-prov on the fully-fixed stack (cloud-agnostic `_shared` + **etcd** + **#3149** non-fatal kyverno gate). It **converged zero-touch past the #2989 wedge** (first prov to clear it), **both regions healthy** (region-b: 4 nodes Ready, 9 cilium pods, 49/56 HRs — the hw123 -b gap is GONE), and **serves HTTPS** (marketplace 200, console 200, valid Let's Encrypt wildcard cert). One operational nudge: force-reconciled bp-harbor past a transient external-secrets-webhook image-pull race (#3155 — Flux should self-heal that; needs a webhook-gate like kyverno's).

| Row | Result | Evidence |
|---|---|---|
| TC-01 Marketplace storefront | ✅ | `marketplace.hw124.omani.works/` — "Build Your Tenant" storefront, HTTPS valid cert (`docs/sessions/2026-06-09/evidence/hw124-marketplace-storefront.png`) |
| Operator console login (PIN) | ✅ | `console.hw124…/login` email→6-digit PIN (IMAP-retrieved) → dashboard. **NB (2026-06-10) — the headless-agent PIN-wall is the known route-around gate, NOT a console failure:** the console login WORKS for a real operator (this ✅ + the §0b walks used a real operator email whose mailbox is IMAP-readable). The headless browser CANNOT self-walk it — `POST /api/v1/auth/pin/issue` with a synthetic user returns **422 + catalyst-api log `pin/issue: SMTP rejected … smtpCode:550 "Mailbox does not exist"`** (the catalyst-pin SSO identity `qa-test-owner@openova.io` has no real inbox; hw124 runs no local mail server, PIN goes via the configured SMTP). So the ◑ UI-render rows (catalog class page, topology tabs, Open-button visual) need a **real operator mailbox the agent can read** — a founder-provided operator email or an authorized provisioned inbox — exactly the documented PIN gate. App-level SSO is NOT affected (catalyst-pin auto-auth needs no mailbox — proven by the 4 live logged-in app walks §2.1/§2.2). |
| Operator dashboard | ✅ | live cluster treemap, 94 items (seaweedfs/kyverno/harbor/keycloak/cnpg/openbao/powerdns/cilium/…) |
| **TC-12 Cloud view = 2 REAL regions** | ✅ | `/cloud?view=graph` — **Region 2/2, Cluster 2/2, 12 WorkerNodes** across `me-east-215-a` AND `-b`; kubectl-confirmed both regions Ready + cilium up (`hw124-cloud-2region-TC12.png`). NOT the empty-2nd-region shell that failed hw99/hw123. |

| TC-G1b Instance page ≠ class | ✅ | `/app/bp-grafana` — single-instance AppDetail, 7-tab strip, Ready, External URL, no "New instance" |
| TC-02 Operator issues voucher | ✅ | `/bss/vouchers` → +Issue voucher → code `UATHW124` + 50 OMR → submit → table row "UATHW124 · 50 OMR · **Active** · 6/9/2026" |
| TC-04 Redeem voucher | ✅ | `marketplace.hw124…/redeem?code=UATHW124` → "**Voucher valid** · 50 OMR credit · applied to your first Organization" |
| SSO-grafana (silent SSO) | ◑ | "Open" launches `grafana.hw124…` but lands on Grafana's **login form** ("Welcome to Grafana" + "Sign in with OpenOva SSO"), NOT auto-signed-in. #3150 (3-layer gap), confirmed live on the clean env. |

Remaining (continuing the live walk): **TC-05–09 checkout→Org** (heaviest — tenant sub-provision), TC-03 voucher email, the remaining per-app SSO rows (§2), TC-13/14 CNPG failover, TC-15–17 cutover.

## 0c. Session results — hw126 (`c986326a77d391d4`), walked 2026-06-11 — S-track SSO acceptance (LIVE BROWSER, READ-ONLY)

**Env state (honest):** hw126 (`hw126.omantel.biz`) is a live 2-region prov (region `me-east-215-a` cluster, treemap shows 104 items: cilium/keycloak/harbor/openbao/powerdns/gitea/guacamole/cnpg-pair/seaweedfs/mimir/falco/…). Console served 200 with a valid wildcard TLS cert; handover-JWT bypass landed on `/dashboard` as `tier=owner` (canonical claim shape: `role=sovereign-admin`, `aud=[https://console.hw126.omantel.biz]`, `iss=https://console.openova.io`). **MID-WALK the `catalyst-api` Pod went 503 "no healthy upstream"** (both `console.hw126…/auth/handover` backend AND `api.hw126…/oidc/auth` OIDC bridge) and stayed down for the remaining ~26 min of the window — this is the catalyst-api serving BOTH the console AppDetail pages AND the OIDC bridge every app's silent-SSO Open routes through. READ-ONLY mandate: not repaired; recorded honestly. S1 + S3 were walked conclusively before/around the outage; S2 + S4-via-console are blocked by it.

| Walk | App | Result | Evidence | Note |
|---|---|---|---|---|
| **S1** OpenBao zero-click (#3226/#3231) | bp-openbao | ❌ FAIL | `../sessions/2026-06-11/evidence/hw126-openbao-FAIL.png`, `hw126-openbao-FAIL-oidc-method.png`, `hw126-openbao-appdetail.png` | Console AppDetail → **Open OpenBao** → new tab `bao.hw126…/ui/` → settles on **`/ui/vault/auth?with=token`** = OpenBao's "**Sign in to OpenBao**" login form (Token/Username/LDAP/JWT/OIDC/RADIUS method picker, defaults to Token). The #3231 silent-SSO shim did **NOT** 302 through Keycloak. Even forcing `?with=oidc` lands on the OIDC method needing a manual **"Sign in with OIDC Provider"** click → not zero-click. (bao subdomain itself served 200 independently of the catalyst-api outage.) |
| **S3** pdns (#3225) | bp-powerdns-admin | ❌ FAIL | `../sessions/2026-06-11/evidence/hw126-pdns-FAIL-nxdomain.png` | `pdns.hw126.omantel.biz` resolves (212.72.24.30) + returns **302 → `https://pdns-admin.hw126.omantel.biz:30443/`** — but that redirect target is **NXDOMAIN** (`getent`/browser `DNS_PROBE_FINISHED_NXDOMAIN`, page "This site can't be reached"). The pdns-admin UI subdomain was never published in DNS and the `:30443` non-standard NodePort port was never fronted by the gateway. No UI loads → no SSO landing possible. |
| **S2** dead Open buttons (#3224) | bp-newapi, bp-openova-flow-server | ☐ not-walked | — | The negative-assertion (NO Open button on newapi/openova-flow-server; Open present+working on grafana/harbor) requires the console AppDetail pages, which 503'd when catalyst-api went down. Not walked — recorded honestly rather than asserted. |
| **S4** SSO matrix (#3150/#2743) | grafana, harbor, gitea, guacamole | ☐ not-walked (console-Open path blocked) | `../sessions/2026-06-11/evidence/hw126-openbao-FAIL.png` (openbao only) | The console **Open** click appends the silent-SSO params and routes through `api.hw126…/oidc/auth` (the catalyst-pin OIDC bridge) — verified live that grafana's "Sign in with OpenOva SSO" routes to `api.hw126…/oidc/auth?...client_id=catalyst-pin...` which **503'd** (catalyst-api down). So the whole SSO matrix is gated on catalyst-api health on hw126; only openbao (S1) was walked to a landed page (FAIL). grafana/gitea served 200 at their roots but direct-nav shows a login form (expected without the console-Open SSO params). |

**hw126 honest verdict:** OpenBao zero-click = **FAIL** (login form, no silent SSO); pdns = **FAIL** (302 → NXDOMAIN pdns-admin host). The remaining SSO-matrix + dead-button checks are **blocked by a live catalyst-api 503 outage** (serves both the console + the OIDC bridge) — not asserted as pass/fail. catalyst-api recovery + re-walk is the follow-up.

## 0d. Session results — hw128 (`5cc5f21df5f64ea7`), walked 2026-06-11 — CURRENT LIVE ENV, real email+PIN, FULL Playwright walk

> Supersedes the wiped hw124/hw126 rows. hw128 = first clean zero-touch 2-region Sovereign that produced **founder-visible PASSES** this session. 57/60 HRs Ready (3 N/A hetzner-only charts). Walked on the production React tree via real Stalwart-mailbox PIN — no bypass, no mock.

| TC | Surface | Result | Evidence |
|---|---|---|---|
| **SSO-into-app (#3272/#3271)** | grafana | ✅ **PASS** | Real PIN (840152) walk: SSO preserves the cross-host OAuth `next` → **lands INSIDE grafana** (`grafana.hw128…/?orgId=1`, "Home - Dashboards", authenticated), NOT console/dashboard. Contrast: hw126 landed console/dashboard = FAIL (#3270). `evidence/hw128-grafana-sso-PASS-landed-in-app.png` (PR #3287). #3271 CLOSED. |
| **jobs-both-regions (#3278)** | console /jobs | ✅ **PASS** | Region column + filter; me-east-215-a (16) + me-east-215-b-1 (690) install-job rows. `evidence/hw128-jobs-both-regions-PASS.png` (PR #3288). #3276 CLOSED. |
| **operator console** | /dashboard, /apps | ✅ working | Treemap 101 items; Apps catalog Deployments 49 / Catalog 63, all bootstrap apps INSTALLED w/ dependency graphs. `evidence/hw128-apps-page.png`, `hw128-console-surfaces-working.md` (PR #3289). |
| **region-kill (T2/T3, north-star row 1)** | cnpg-pair | ⛔ pending #3290 | Mesh orchestrator failed: primary kubeconfig empty-path (#3241 root-caused live) → cnpg-pair flip OFF. Fix #3290 merging → self-establishes the mesh on hw128 via mothership `restoreFromStore` (no re-prov). Then walkable. |
| **#3188 shared-PG card** | catalog data-instances | ⛔ pending dedicated prov | hw128 is own-cluster (SHARED_PG off). Full chain coded (#3274/#3284 merged, #3286 ready) → needs one `SHARED_PG=true` prov. |

**hw128 honest verdict:** SSO-into-app + jobs-both-regions = **PASS** (real-PIN, screenshotted, issues closed). region-kill + #3188-card = code-ready, pending #3290 mesh + a dedicated SHARED_PG prov respectively. This is the first env this session to produce founder-visible, clickable deltas vs the prior "converged-but-nothing-walks" state.

### 0c-bis. S2 + S4 re-walk attempt — 2026-06-11 (LIVE BROWSER, READ-ONLY) — catalyst-api STILL 503 from the gateway

**Env state (honest, re-probed live this session):** The S2/S4 re-walk was attempted on the premise that catalyst-api had recovered (Pod reported `1/1 Running`). **The live wire says otherwise.** The console's **static UI shell** serves 200 (`/`, `/login`, `/dashboard` all render the React app — email→PIN "Sign in" form), but **every catalyst-api-backed route returns `503 "no healthy upstream"`** — meaning the gateway has **no healthy backend endpoint** to route to (a Pod being `Running` ≠ passing its readiness probe / registered as a healthy upstream). Re-probed via `curl` over a sustained ~40s+ window (no transient roll window):

| Route (catalyst-api-backed) | HTTP | Note |
|---|---|---|
| `console.hw126…/auth/handover?token=<JWT>` | **503** | handover-JWT bypass cannot establish a session — page body literally `no healthy upstream` |
| `console.hw126…/catalyst/v1/apps` | **503** | the catalog/AppDetail data API S2 needs |
| `console.hw126…/api/catalyst/v1/apps` | **503** | same, alternate path prefix |
| `api.hw126…/oidc/auth?client_id=catalyst-pin&prompt=none` | **503** | the catalyst-pin OIDC bridge every Open routes through (S4) |
| `console.hw126…/login` · `/dashboard` · `/` | 200 | static UI shell only — renders, but `/dashboard` 302→`/login?next=/dashboard` (no session, since handover 503'd) |

Both auth paths into the console are gated on catalyst-api: (a) the **handover-JWT bypass** route 503s; (b) the **email→PIN** path needs catalyst-api to mint/verify the PIN (also 503). **No authenticated console session is reachable → the AppDetail pages (S2) and the console Open → OIDC-bridge silent-SSO chain (S4) cannot be exercised.** READ-ONLY mandate: not repaired; recorded honestly.

| Walk | App | Result | Evidence | Note |
|---|---|---|---|---|
| **S2** dead Open buttons (#3224) | bp-newapi, bp-openova-flow-server, grafana, harbor | ☐ not-walked (catalyst-api 503) | `../sessions/2026-06-11/evidence/hw126-console-login-catalyst-api-503.png`, `hw126-oidc-bridge-503.png` | The negative-assertion (NO Open button on newapi/openova-flow-server; Open present+works on grafana/harbor) requires the console AppDetail pages, which are served by catalyst-api (`/catalyst/v1/apps` → **503**). Console UI shell loads but `/dashboard` 302→`/login`; neither handover-JWT (503) nor PIN-login (needs catalyst-api) can establish a session. Not walked — recorded honestly, not asserted. |
| **S4** SSO matrix (#3150/#2743) | grafana, harbor, gitea, guacamole, pdns-admin | ☐ not-walked (console-Open + OIDC bridge both 503) | `../sessions/2026-06-11/evidence/hw126-oidc-bridge-503.png`, `hw126-console-login-catalyst-api-503.png` | The console **Open** click routes every app's silent-SSO through `api.hw126…/oidc/auth` (the catalyst-pin OIDC bridge) — confirmed live this session still **503 "no healthy upstream"** (screenshot). The whole SSO matrix is gated on catalyst-api health, which is down. Cannot reach an authenticated console to even click Open. Not walked — recorded honestly. (openbao S1 already walked **FAIL** in §0c above; not re-walked per scope.) |

**hw126 re-walk verdict (2026-06-11):** S2 + S4 **remain blocked** by the **same** catalyst-api `503 "no healthy upstream"` outage recorded in §0c / PR #3260 — the Pod reporting `1/1 Running` has **not** become a healthy gateway upstream (readiness-probe / endpoint-registration gap is the likely cause; READ-ONLY so not diagnosed in-cluster). The catalyst-api serves BOTH the console AppDetail data API AND the OIDC bridge, so its outage gates the entire S-track console-driven walk. Genuine catalyst-api recovery (a healthy registered upstream, not just a Running Pod) + re-walk is the follow-up — no PASS/FAIL asserted on any app surface that could not be reached.

### 0c-ter. S2 + S4 FINAL walk — 2026-06-11 (LIVE BROWSER, READ-ONLY) — catalyst-api RECOVERED, walks completed

**Env state (honest, re-probed live this session):** The catalyst-api `503` outage from §0c / §0c-bis is **FIXED** — a cilium-envoy resync restored every catalyst-api route. Re-probed live this session: `console.hw126…/` → **200**; `api.hw126…/oidc/auth` (no params) → **400** (param validation, not 503); `console.hw126…/auth/handover` (no token) → **401** (not 503). The handover-JWT bypass (fresh single-use RS256 token, canonical claims `role=sovereign-admin` / `aud=[https://console.hw126.omantel.biz]` / `iss=https://console.openova.io`) **landed authenticated on `/dashboard`** (`tier=owner`, 104-item treemap), and the **Apps page rendered all 49 deployments** from the (now healthy) catalyst-api. S2 + S4 were therefore walked to conclusion below.

| Walk | App | Result | Evidence | Note |
|---|---|---|---|---|
| **S2** dead Open buttons (#3224) — negative | bp-newapi | ❌ FAIL | `../sessions/2026-06-11/evidence/hw126-newapi-FAIL.png` | AppDetail `/app/bp-newapi` (Platform · BOOTSTRAP · `bp-newapi@1.4.63` · Ready) shows a hero **"Open newapi"** button **AND** an **"↗ Open"** button beside External URL `https://newapi.hw126.omantel.biz`, both captioned "single-click silent sign-in, no second login". The #3224 assertion is that this Platform component carries **NO** Open button — it does. FAIL. |
| **S2** dead Open buttons (#3224) — negative | bp-openova-flow-server | ❌ FAIL | `../sessions/2026-06-11/evidence/hw126-openova-flow-server-FAIL.png` | AppDetail `/app/bp-openova-flow-server` (Platform · BOOTSTRAP · `bp-openova-flow-server@0.2.3` · Ready) shows a hero **"Open openova-flow-server"** button **AND** an **"↗ Open"** beside External URL `https://openova-flow.hw126.omantel.biz`. Same dead-button leak as newapi. FAIL. |
| **S2** Open present + launches (#3224) — positive | grafana | ✅ PASS | `../sessions/2026-06-11/evidence/hw126-grafana-PASS.png` | AppDetail `/app/bp-grafana` shows **"Open Grafana"** + External URL `https://grafana.hw126.omantel.biz`. Click **launches** a new tab into the catalyst-pin OIDC silent-SSO chain (`api.hw126…/oidc/auth?…client_id=catalyst-pin…redirect_uri=…/broker/catalyst-pin/endpoint`). Open present AND launches → PASS for the S2 negative/positive contract. (Landed-authenticated is the S4 row below.) |
| **S2** Open present + launches (#3224) — positive | harbor | ✅ PASS | `../sessions/2026-06-11/evidence/hw126-harbor-PASS.png` | AppDetail `/app/bp-harbor` shows **"Open Harbor"**. Click **launches** a new tab into the same OIDC chain (`redirect_uri` → `registry.hw126…/c/oidc/callback`). Open present AND launches → PASS. |
| **S4** SSO landed-authenticated (#3150/#2743) | grafana | ❌ FAIL | `../sessions/2026-06-11/evidence/hw126-grafana-sso-FAIL-loginform.png` | console **Open Grafana** → silent-SSO chain bounces to the console **PIN login form** ("Sign in — Enter your email to receive a 6-digit PIN"), NOT a zero-prompt Grafana landing. Root cause proven live: the catalyst-pin OIDC bridge (`api.hw126…/oidc/auth`) — hit **with** the authenticated handover console session present — **302-redirects to `console.hw126…/login?next=…`** (the handover-JWT establishes the console app session but NOT the catalyst-pin Keycloak browser session the bridge consumes; only a real PIN-login mints that). Lands on a login form → FAIL per the walk rule. |
| **S4** SSO landed-authenticated (#3150/#2743) | harbor | ❌ FAIL | `../sessions/2026-06-11/evidence/hw126-harbor-sso-FAIL-loginform.png` | console **Open Harbor** → same bounce to the console **PIN login form** via the catalyst-pin OIDC-bridge gate. Lands on a login form → FAIL. |
| **S4** SSO landed-authenticated (#3150/#2743) | gitea | ❌ FAIL | `../sessions/2026-06-11/evidence/hw126-gitea-sso-FAIL-internal-kc-dns.png` | console **Open Gitea** → new tab redirects to **`http://keycloak.keycloak.svc.cluster.local/realms/sovereign/protocol/openid-connect/auth?client_id=gitea&redirect_uri=https://gitea.hw126.omantel.biz/user/oauth2/openova-sso/callback…`** — gitea's OIDC client points at the **internal cluster-DNS Keycloak issuer** (`http://keycloak.keycloak.svc.cluster.local`) instead of the public `https://auth.hw126.omantel.biz`, so the browser hits a `chrome-error` page (host unresolvable). Distinct from the PIN-gate — a genuine app-side OIDC issuer-URL misconfiguration. FAIL. |
| **S4** SSO landed-authenticated (#3150/#2743) | guacamole | ❌ FAIL | `../sessions/2026-06-11/evidence/hw126-guacamole-sso-FAIL-loginform.png` | console **Open guacamole** → same bounce to the console **PIN login form** via the catalyst-pin OIDC-bridge gate (`redirect_uri` ultimately → `guacamole.hw126…/guacamole/`). Lands on a login form → FAIL. |

**hw126 0c-ter verdict (2026-06-11):** catalyst-api **recovered** and both walks ran to conclusion. **S2:** bp-newapi + bp-openova-flow-server **FAIL** the dead-button assertion (both Platform/BOOTSTRAP components still render hero **Open** + External-URL **↗ Open**); grafana + harbor **PASS** (Open present AND launches). **S4:** all four console-Open walks land on a sign-in surface, none zero-prompt-authenticated — grafana/harbor/guacamole bounce to the console **PIN login** because the catalyst-pin OIDC bridge does not accept the handover-JWT console session (proven live with the authenticated session present); **gitea** is a distinct genuine misconfig — its OIDC client targets the **internal `keycloak.keycloak.svc.cluster.local` issuer** not the public `auth.hw126…` host, yielding a browser `chrome-error`. The bridge-vs-handover-session gate is the documented headless-agent PIN wall for grafana/harbor/guacamole; the gitea internal-issuer and the newapi/openova-flow-server dead-buttons are real product gaps. (openbao S1 + pdns S3 already walked **FAIL** in §0c; not re-walked per scope.)

## 0b. Session results — hw123 (`179551d5ce8039cf`) — SUPERSEDED (wiped for the hw124 clean re-prov)

**Env state (honest):** hw123 converged past the #2989 wedge (51/56) on the cloud-agnostic `_shared` bootstrap with **etcd** (`--cluster-init`) — first prov ever to clear it. Phase-1 watch then "failed" on the **recoverable** kyverno-hook timeout; the cluster stayed alive. Serving was unblocked by force-reconciling bp-kyverno (the real root cause, now permanently fixed in **#3149** — webhook-gate made non-fatal); `sovereign-tls` then applied the wildcard cert + LB-IPAM pool + gateway nodePort pin + coredns DNS-01 override **natively**. hw123 now serves HTTPS (marketplace + console 200, valid Let's Encrypt wildcard cert). **The pristine zero-touch proof is a fresh re-prov with #3149 merged (no nudges needed).**

| Row | Result | Evidence |
|---|---|---|
| TC-01 Marketplace storefront | ✅ | `marketplace.hw123.omani.works/` — "Build Your Tenant" storefront, HTTPS valid cert (`hw123-marketplace-https.png`) |
| TC-05 Pick plan | ✅ | `/plans` — S/M/L/XL/Flexi + 3 Sandbox tiers; M pre-selected; "Continue to Stack" |
| TC-06 Pick apps | ✅ | `/apps` — 14 apps, search, industry templates, category filters |
| Operator console login (PIN) | ✅ | `console.hw123…/login` email→6-digit PIN (retrieved via IMAP) → dashboard (`hw123-operator-console-dashboard.png`) |
| Operator dashboard + Apps | ✅ | live cluster treemap (95 items); Apps tab shows **49 INSTALLED** (full stack: grafana/gitea/harbor/keycloak/openbao/cnpg/powerdns/…) |
| TC-G1b Instance page ≠ class | ✅ | `/app/bp-grafana` — single-instance AppDetail, 7-tab strip, Ready, External URL, no "New instance" |
| TC-12 Cloud view = 2 REAL regions | ◑ | Region 2/2, Cluster 2/2, 12 WorkerNodes; region `-b` has **4 real nodes** (not an empty shell) but **NotReady** — secondary region didn't converge |
| SSO-grafana (silent SSO) | ◑ | "Open" launches `grafana.hw123…` in a new tab but lands on Grafana's **login form** — NOT auto-signed-in. Console catalyst-api PIN session ≠ the Keycloak browser session the apps' `prompt=none` SSO needs. |

Remaining (need a converged 2-region env / clean re-prov): TC-02/03/04 voucher, TC-07/08/09 checkout→Org, TC-10 tenant login, TC-11/13/14 multi-region+CNPG-failover (gated on region-b), TC-15/16/17 cutover, the remaining per-app SSO rows.

## 1. Pillars (marketplace → Org → multi-region → CNPG failover → cutover)

| # | Test group | Tested page | Test case (what you do) | What you must see | Result | Evidence |
|---|---|---|---|---|---|---|
| **Pillar 1 — Marketplace + voucher onboarding → Organization** |||||||
| TC-01 | Marketplace storefront | `marketplace.hw124.omani.works/` | Open the storefront | "Build your tenant" renders, non-empty, branded | ☐ | — |
| TC-02 | Operator issues voucher | `console.hw124.omani.works/bss/vouchers` | Operator: +Issue voucher → code+credit → submit | Voucher appears in the table, active | ☐ | — |
| TC-03 | Voucher email | recipient inbox | Open the voucher email | Delivered via the **Sovereign's own SMTP** with the redeem link | ☐ | — |
| TC-04 | Redeem voucher | `marketplace.hw124.omani.works/redeem?code=UATHW124` | Open the redeem link | "Voucher valid" + OMR credit; a garbage code → "not valid" | ☐ | — |
| TC-05 | Pick plan | `…/plans` | Pick a plan card | Advances to app picker | ☐ | — |
| TC-06 | Pick apps | `…/apps` | Select a Postgres-backed app | Advances to setup/extras | ☐ | — |
| TC-07 | Choose subdomain | `…/addons` | Type a valid subdomain | Pool picker offers a free domain; subdomain accepted | ☐ | — |
| TC-08 | Checkout (credit-only) | `…/checkout` | Sign in (email→PIN), confirm | Voucher **credit applied**, no card required; "Setting up your tenant" | ☐ | — |
| TC-09 | Organization created | "Your tenant is ready" | Follow the tenant link | Lands on `console.<orgslug>.<pool>` — real dashboard, not an error | ☐ | — |
| TC-10 | Tenant first login | tenant console | Customer PIN-login | Dashboard renders (Phase 2a) | ☐ | — |
| **Pillar 2 — Multi-region BCP topology chosen at signup** |||||||
| TC-11 | BCP at signup | `marketplace.hw124.omani.works/bcp` | Choose **active-hot-standby**, pick **two different** regions | Same-region rejected; two distinct regions accepted; provisions BOTH in one pass | ☐ | — |
| TC-12 | Cloud view = 2 REAL regions | `console.hw124.omani.works/cloud?view=graph` | Open the Cloud view | **2 regions, 2 clusters with REAL nodes in each** (not an empty 2nd-region VPC shell — the hw99 failure) | ☐ | — |
| **Pillar 3 — Two independent CNPG clusters + region-kill failover** |||||||
| TC-13 | CNPG pair across regions | `/app/$id` → Topology | Install a CNPG-backed app; read placement | One CNPG cluster **per region**, synchronous `ReplicaCluster` over ClusterMesh; both regions shown | ☐ | — |
| TC-14 | Region-kill failover | the app's FQDN | Dev kills the primary region; keep refreshing | Service resumes **≤30 s**, same FQDN; surviving region healthy; **0 transactions lost** | ☐ | — |
| **Pillar 5 — Sovereign independence (`bp-self-sovereign-cutover`)** |||||||
| TC-15 | Trigger cutover | `console.hw101.<dom>/settings` → Sovereignty | "Soft-tethered" → tap "Achieve True Sovereignty" → confirm | Progress card; 8 tether-pivot steps advance | ☐ | — |
| TC-16 | Egress-block proof | progress card | Wait through the final step | **10-min deny-egress** hold vs github.com/ghcr.io/harbor.openova.io; stays green → badge **"Independent"**, `cutoverComplete=true` | ☐ | — |
| TC-17 | Post-cutover resilience | tenant console + an app | PIN-login + tap **Open** | Both still work, now pulling exclusively from local Gitea/Harbor | ☐ | — |
| **G117 — Application lifecycle (EPIC #2737) — class ≠ instance** |||||||
| TC-G1a | Catalog class page | `console.hw124.omani.works/catalog/bp-postgres` | Open the CLASS page | **CLASS page** `/catalog/$bp` — instances-list + New-instance only, no single-instance tabs | ☐ | — |
| TC-G1b | Instance page ≠ class | `/apps` → Deployments tab → click an instance | Click an instance | **INSTANCE page** `/app/$id` — that one instance only; **NO "New instance"**; the two clicks NEVER open the same page | ☐ | — |
| TC-G2 | Multi-instance children | `/catalog/$bp` | "+ New instance" ×3, distinct names | All accepted, no collision; class page lists all N, each → its own `/app/$id` | ◑ | ☐ | — |
| TC-G4 | Endpoints tab editable | `/app/$id` → Endpoints | Add alias / edit / delete | EDITABLE; each mutation → Git-IaC PR (3 checks) → auto-merge → new FQDN serves TLS+SSO ≤2 min | ☐ | — |

## 2. PER-APP SSO single-click login — ONE ROW PER `ssoEnabled` app

> **The founder's #1 demand: an explicit single-click-login row for gitea, openbao, grafana, and every other ssoEnabled app — never a generic "works for grafana/gitea/harbor/openbao" row.**
>
> Extracted from `spec.endpoints[].ssoEnabled: true` + `spec.sso` (`realm`, `silentLogin: true`) in each `platform/<app>/blueprint.yaml`. **24 ssoEnabled blueprints, 26 ssoEnabled endpoints** (opensearch + catalyst-platform each expose two). The "Open" button replaces the raw endpoint URL on every `ssoEnabled` endpoint with `launchDefault: true`; the URL carries `prompt=none&kc_idp_hint=catalyst-pin` so the new tab lands **already signed in** — no login form, no second click. Realm is `sovereign` for Tier-1/Tier-2 control-plane apps and per-Org `{{.OrgSlug}}` for Tier-3 tenant apps (#2744). Tenant-app rows are walked from the **tenant** console `console.<orgslug>.<pool>`.

### 2.1 Tier-1 — sovereign-realm control-plane (4) — #2744 already-wired

| # | Tested page | Test case | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| SSO-grafana | `/app/bp-grafana` (instance) | Click **Open** | New tab lands in **Grafana** ALREADY signed in — silent OIDC (`prompt=none&kc_idp_hint=catalyst-pin`), **NO** login form, **NO** second click. Realm=`sovereign`. Roles mapped from KC groups (`grafana-editors`→editor, `grafana-viewers`→viewer, `sovereign-admins`→admin). | ☐ | — |
| SSO-gitea | `/app/bp-gitea` (instance) | Click **Open** | New tab lands in **Gitea** ALREADY signed in — silent OIDC, NO login form. Realm=`sovereign`. **Quirk:** silent delegation rides the sovereign-realm IDR `defaultProvider=catalyst-pin` (fires with OR without the hint — verified live). gitea's `openidConnect` provider 307s the browser straight to KC's authorize endpoint built from the **persisted discovery URL** (it IGNORES `--custom-auth-url` — the prior "custom-url-mapping carries the hint" note was wrong). | ☐ | — |
| SSO-harbor | `/app/bp-harbor` (instance, `ui` endpoint) | Click **Open** | New tab lands in **Harbor** ALREADY signed in — silent OIDC, NO login form. Realm=`sovereign`. **Quirk:** silent SSO rides Harbor's `oidc_extra_redirect_parms` config field set to `{"kc_idp_hint":"catalyst-pin"}` by the configure-oauth Job (`platform/harbor/chart`). (The 2nd `registry` endpoint is `ssoEnabled: false` — OCI clients use robot accounts, **no** Open button there.) | ☐ | — |
| SSO-openbao | `/app/bp-openbao` (instance, `api` endpoint) | Click **Open** | New tab lands in **OpenBao** ALREADY signed in via OIDC — Realm=`sovereign`, `default_role=operator`. **Quirk:** OpenBao's `api` endpoint is `visibility: private`; silent SSO does **NOT** ride a `prompt=none` URL param — the hashicorp/cap library (`builtin/credential/jwt/path_oidc.go createOIDCRequest`) builds the `auth_url` server-side and exposes **no** query-param knob, so silent login is delivered **only** by the KC realm IDR `defaultProvider=catalyst-pin` (NOT by URL hint). Must still land signed-in with no KC login form. | ☐ | — |

### 2.2 Tier-2 — sovereign-realm operator-facing (4) — #2744 federate-to-sovereign

| # | Tested page | Test case | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| SSO-guacamole | `/app/bp-guacamole` (instance) | Click **Open** | New tab lands in **Guacamole** ALREADY signed in — silent OIDC, NO login form. Realm=`sovereign` (`sovereign-admins`→admin). | ☐ | — |
| SSO-powerdns-admin | `/app/bp-powerdns-admin` (instance) | Click **Open** | New tab lands in **PowerDNS-Admin** ALREADY signed in — silent OIDC, NO login form. Realm=`sovereign`. Endpoint `visibility: private` (operator-only). | ☐ | — |
| SSO-netbird | `/app/bp-netbird` (instance) | Click **Open** | New tab lands in **NetBird** dashboard ALREADY signed in — silent OIDC, NO login form. Realm=`sovereign` (`sovereign-admins`→admin). | ☐ | — |
| SSO-spire | `/app/bp-spire` (instance) | Click **Open** | **⛔ #3084-BLOCKED:** `platform/spire` is a scaffold chart with **no** `spec.sso` block and **no** Keycloak OIDC client — there is currently NO ssoEnabled endpoint and NO Open button. Expectation once #3084 ships: silent OIDC into the SPIRE console at realm=`sovereign` (or explicit de-scope, since SPIRE is SPIFFE machine-identity, not human login). Until #3084: this row is **⛔ blocked**, not a pass. | ⛔ | #3084 |

### 2.3 Tier-3 — per-Org-realm tenant apps (realm=`{{.OrgSlug}}`) — #2744 2-hop broker

> Walked from the **tenant** console after Org onboarding. Each Org gets its own KC realm with a Keycloak-OIDC IdP federated to the `sovereign` realm broker (decision #6). `prompt=none&kc_idp_hint=catalyst-pin` on every Open.

| # | Tested page | Test case | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| SSO-opensearch-dashboards | `/app/bp-opensearch` (instance, `dashboards` endpoint) | Click **Open** | New tab lands in **OpenSearch Dashboards** ALREADY signed in — silent OIDC, NO login form. Realm=`{{.OrgSlug}}` (`opensearch-admins`→admin, `opensearch-users`→user). | ☐ | — |
| SSO-opensearch-api | `/app/bp-opensearch` (instance, `api` endpoint) | Inspect the `api` endpoint | `api` endpoint is `ssoEnabled: true` but `launchDefault: false` — **no** primary Open button (Dashboards is the launch target); API auth still OIDC-bearer at realm=`{{.OrgSlug}}`. | ☐ | — |
| SSO-matrix | `/app/bp-matrix` (instance) | Click **Open** | New tab lands in **Matrix** (Element) ALREADY signed in — silent OIDC, NO login form. Realm=`{{.OrgSlug}}`. | ☐ | — |
| SSO-langfuse | `/app/bp-langfuse` (instance) | Click **Open** | New tab lands in **Langfuse** ALREADY signed in — silent OIDC, NO login form. Realm=`{{.OrgSlug}}`. | ☐ | — |
| SSO-temporal | `/app/bp-temporal` (instance) | Click **Open** | New tab lands in **Temporal** Web UI ALREADY signed in — silent OIDC, NO login form. Realm=`{{.OrgSlug}}`. | ☐ | — |
| SSO-librechat | `/app/bp-librechat` (instance) | Click **Open** | New tab lands in **LibreChat** ALREADY signed in — silent OIDC, NO login form. Realm=`{{.OrgSlug}}`. | ☐ | — |
| SSO-openmeter | `/app/bp-openmeter` (instance) | Click **Open** | New tab lands in **OpenMeter** ALREADY signed in — silent OIDC, NO login form. Realm=`{{.OrgSlug}}`. | ☐ | — |
| SSO-wordpress-tenant | `/app/bp-wordpress-tenant` (instance) | Click **Open** | New tab lands in **WordPress** (wp-admin) ALREADY signed in — silent OIDC, NO login form. Realm=`{{.OrgSlug}}`. Hostname is per-instance `{{.AppName}}.{{.OrgSlug}}.<sov>`. | ☐ | — |
| SSO-vllm | `/app/bp-vllm` (instance) | Click **Open** | New tab lands in **vLLM** UI ALREADY signed in — silent OIDC, NO login form. Realm=`{{.OrgSlug}}`. | ☐ | — |
| SSO-kserve | `/app/bp-kserve` (instance) | Click **Open** | New tab lands in **KServe** UI ALREADY signed in — silent OIDC, NO login form. Realm=`{{.OrgSlug}}`. | ☐ | — |
| SSO-livekit | `/app/bp-livekit` (instance) | Click **Open** | New tab lands in **LiveKit** UI ALREADY signed in — silent OIDC, NO login form. Realm=`{{.OrgSlug}}`. | ☐ | — |
| SSO-llm-gateway | `/app/bp-llm-gateway` (instance) | Click **Open** | New tab lands in **LLM Gateway** UI ALREADY signed in — silent OIDC, NO login form. Realm=`{{.OrgSlug}}`. | ☐ | — |
| SSO-flink | `/app/bp-flink` (instance) | Click **Open** | New tab lands in **Flink** dashboard ALREADY signed in — silent OIDC, NO login form. Realm=`{{.OrgSlug}}`. | ☐ | — |
| SSO-neo4j | `/app/bp-neo4j` (instance, `browser` endpoint) | Click **Open** | New tab lands in **Neo4j Browser** ALREADY signed in — silent OIDC, NO login form. Realm=`{{.OrgSlug}}`. (The `bolt` endpoint is `ssoEnabled: false` — driver auth, no Open button.) | ☐ | — |
| SSO-litmus | `/app/bp-litmus` (instance) | Click **Open** | New tab lands in **LitmusChaos** ALREADY signed in — silent OIDC, NO login form. Realm=`{{.OrgSlug}}`. (Topology = singleton.) | ☐ | — |
| SSO-stalwart-tenant | `/app/bp-stalwart-tenant` (instance, `webmail` endpoint) | Click **Open** | New tab lands in **Stalwart webmail** ALREADY signed in — silent OIDC, NO login form. Realm=`{{.OrgSlug}}`. (The `smtp`/`imap` endpoints are `ssoEnabled: false` — protocol auth, no Open button.) | ☐ | — |

### 2.4 Non-SSO / no-Open endpoints — negative-assertion (must NOT show an Open button)

> Extracted apps whose front-door endpoint is `ssoEnabled: false` or that have **no** Keycloak login surface — the React console must **not** render an Open silent-SSO button for these.

| # | Tested page | Test case | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| SSO-neg-keycloak | `/app/bp-keycloak` → Endpoints | Inspect `ui`/`auth` endpoint | Keycloak's own `ui` endpoint is `ssoEnabled: false` (it IS the IdP) — **no** silent-SSO Open button; the KC admin console is a direct login. | ☐ | — |
| SSO-neg-hubble | `/app/bp-cilium` → Endpoints | Inspect `hubble` endpoint | Hubble UI endpoint exists (`hubble.<sov>`, `visibility: private`) but is **NOT** `ssoEnabled` in the blueprint → **no** Open silent-SSO button today. (Founder named hubble; current blueprint carries no `sso` block — documented gap, not a pass.) | ☐ | — |
| SSO-neg-wire | `/app/bp-{strimzi,valkey,ferretdb,clickhouse,milvus,iceberg,stunner}` → Endpoints | Inspect the wire/grpc/turn endpoint | These expose only protocol endpoints (`kafka`/`valkey`/`db`/`grpc`/`catalog`/`turn`) with `ssoEnabled: false` → **no** Open button; clients authenticate at the wire protocol, not via browser SSO. | ☐ | — |
| SSO-neg-registry | `/app/bp-harbor` → `registry` endpoint | Inspect `registry` endpoint | `registry.<sov>` is `ssoEnabled: false` — docker/OCI robot-account auth, **no** Open button (Harbor `ui` is the SSO surface — covered by SSO-harbor). | ☐ | — |

## 3. PER-APP topology — ONE ROW PER app carrying a non-trivial topology

> **The founder's #2 demand: an app-by-app topology view (openbao topology view named explicitly) — never a generic claim.**
>
> Extracted from `spec.topology.defaults.multi-region` in each `platform/<app>/blueprint.yaml`. Walked on a **2-region** Sovereign (hw101): install the app, open `/app/$id` → **Topology tab** (`products/catalyst/bootstrap/ui/src/pages/sovereign/AppDetail/TopologyTab.tsx`), read the placement + `perCluster[]` and confirm it matches the declared `spec.topology`. Counts match the audit: **10 active-hot-standby / 14 active-active / 20 active-passive / 44 singleton.**
>
> Topology → expectation mapping:
> - **active-active** → N active HRs, **one per region**, both serving live traffic (load-balanced).
> - **active-hot-standby** → 2 HRs: primary **active** + secondary **passive/warm** kept in sync, promotes on region-kill.
> - **active-passive** → primary **active** + secondary **cold/warm** standby (DR target; not serving until promoted).
> - **singleton** → **1** HR on the primary cluster only; **region-kill loses it** until restored (Velero).

### 3.1 active-hot-standby (10) — each individually

> **⛔ GAP ANALYSIS — all 10 active-hot-standby rows are gated on a 2-region prov (2026-06-10, live kubectl).** The "active-hot-standby" target = primary **active** + secondary **warm standby** kept in sync via a `bp-cnpg-pair` CNPG `ReplicaCluster` over Cilium ClusterMesh, with `bp-continuum` promote + PowerDNS flip on region-kill. **Current state on hw124:** every app runs its **single-region primary only** — `kubectl get clusters.postgresql.cnpg.io -A` shows the per-app primaries healthy (gitea→`gitea/gitea-pg`, harbor→`harbor/harbor-pg`, both 2-instance same-cluster; keycloak/grafana/guacamole/catalyst-platform back onto the catalyst-platform PG), but **0 clusters have `replica.enabled=true`** and **all nodes are in `me-east-215-a` (1 region)**. So the *standby half* of every active-hot-standby agreement is physically absent. **Remediation (Flux-native, no controller):** a fresh **2-region** prov with `SOVEREIGN_ENABLE_CNPG_PAIR=true` → bp-cnpg-pair renders the region-B `ReplicaCluster` (`spec.replica.enabled=true`, `primary:` → region-A source) synced over ClusterMesh; the blueprint side is verified-ready (#3189 B: 9/9 cnpg-pair refs, 0 drift; continuum-controller Running via #3204). **These rows cannot move past ☐/⛔ without that prov — it is physics (no region B → no warm standby), tracked on #3195.** The blueprint declarations themselves are confirmed correct; what's missing is the live 2-region substrate.

| # | Tested page | Test case | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| TOPO-catalyst-platform | `/app/bp-catalyst-platform` → Topology | Read placement | **active-hot-standby** → primary `mgmt-A` active + `mgmt-B` warm standby (Catalyst CRDs + PG via bp-cnpg-pair); `bp-continuum` flips PowerDNS for `console.<sov>`/`api.<sov>` on region-kill. `perCluster[]` shows 2 mgmt clusters. | ☐ | — |
| TOPO-keycloak | `/app/bp-keycloak` → Topology | Read placement | **active-hot-standby** → `mgmt-A` active + `mgmt-B` passive; PG via bp-cnpg-pair sync; DNS flip `auth.<sov>` ~30s on promote. | ☐ | — |
| TOPO-gitea | `/app/bp-gitea` → Topology | Read placement | **active-hot-standby** → primary HR active + secondary passive (sync); PG via bp-cnpg-pair, Git blobs on SeaweedFS S3; flip `gitea.<sov>`. | ☐ | — |
| TOPO-grafana | `/app/bp-grafana` → Topology | Read placement | **active-hot-standby** → 2 HRs: primary active + secondary passive (sync); Grafana DB on bp-cnpg-pair; flip `grafana.<sov>` ~30s. | ☐ | — |
| TOPO-harbor | `/app/bp-harbor` → Topology | Read placement | **active-hot-standby** → primary active + secondary passive; PG via bp-cnpg-pair, image blobs on object-storage replication; flip `harbor.<sov>`+`registry.<sov>`. | ☐ | — |
| TOPO-guacamole | `/app/bp-guacamole` → Topology | Read placement | **active-hot-standby** → primary active + secondary passive; PG via bp-cnpg-pair; flip `guac.<sov>` (in-progress remote-desktop sessions drop). | ☐ | — |
| TOPO-netbird | `/app/bp-netbird` → Topology | Read placement | **active-hot-standby** → primary active + secondary passive; mgmt state in PG via bp-cnpg-pair (candidate CP); flip `netbird.<sov>`. | ☐ | — |
| TOPO-spire | `/app/bp-spire` → Topology | Read placement | **active-hot-standby** declared → primary active + secondary passive; SPIRE Server datastore in PG. (SSO is #3084-blocked, but the topology declaration is present and walkable.) | ☐ | — |
| TOPO-cnpg-pair | `/app/bp-cnpg-pair` → Topology | Read placement | **active-hot-standby** → the canonical Pillar-3 pair: `rtz-A` active + `rtz-B` warm standby, **synchronous** streaming (`remote_apply`) over Cilium ClusterMesh, zero-tx-loss; `bp-continuum` lease + `cnpg promote` + PowerDNS lua-flip `db.<org>.<sov>`. **Ties TC-13/TC-14.** | ☐ | — |
| TOPO-sandbox | `/app/bp-sandbox` → Topology | (OUT of scope) | sandbox declares **active-hot-standby**, but **Sandbox is OUT of scope** for this UAT — row recorded for completeness only, not walked. | ☐ | — |

### 3.2 active-passive (20) — each individually (incl. openbao, founder-named)

| # | Tested page | Test case | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| TOPO-openbao | `/app/bp-openbao` → Topology | Read placement | **active-passive** → primary **active** (Raft 3-5 replicas) + secondary **warm** standby via async perf-replication; `bp-continuum` runs `vault operator raft transition-to-primary` on the standby; KV reads continue uninterrupted on the replica. `perCluster[]` shows primary + passive. **(Founder named this view explicitly.)** | ☐ | — |
| TOPO-stalwart-tenant | `/app/bp-stalwart-tenant` → Topology | Read placement | **active-passive** → primary active + secondary warm; PG via bp-cnpg-pair, mail blobs on tenant S3 replicated; flip `mail.<org>.<sov>`. | ☐ | — |
| TOPO-matrix | `/app/bp-matrix` → Topology | Read placement | **active-passive** → primary active + secondary warm; homeserver DB in PG via bp-cnpg-pair, media on S3; flip `matrix.<org>.<sov>`. | ☐ | — |
| TOPO-langfuse | `/app/bp-langfuse` → Topology | Read placement | **active-passive** → primary active + secondary warm; traces in PG (bp-cnpg-pair) + ClickHouse; flip `langfuse.<org>.<sov>`. | ☐ | — |
| TOPO-temporal | `/app/bp-temporal` → Topology | Read placement | **active-passive** → primary active + secondary warm; history in PG (bp-cnpg-pair) + visibility in OpenSearch; workflows resume from history on promote. | ☐ | — |
| TOPO-openmeter | `/app/bp-openmeter` → Topology | Read placement | **active-passive** → primary active + secondary warm; events in ClickHouse + state in PG (bp-cnpg-pair); flip `openmeter.<org>.<sov>`. | ☐ | — |
| TOPO-neo4j | `/app/bp-neo4j` → Topology | Read placement | **active-passive** → primary active + read-replica/standby; graph store on PVC, cross-region via backup-restore; promote read-replica or Velero restore. | ☐ | — |
| TOPO-wordpress-tenant | `/app/bp-wordpress-tenant` → Topology | Read placement | **active-passive** → primary active + secondary warm; PG via bp-cnpg-pair, media on tenant S3 replicated; flip `<site>.<org>.<sov>`. | ☐ | — |
| TOPO-ferretdb | `/app/bp-ferretdb` → Topology | Read placement | **active-passive** → primary active + secondary warm; Mongo-API over PG backend via bp-cnpg-pair (ferretdb itself stateless). | ☐ | — |
| TOPO-valkey | `/app/bp-valkey` → Topology | Read placement | **active-passive** → master + replica; Sentinel-pair across clusters via ClusterMesh; `bp-continuum` Sentinel failover (~5s reconnect). | ☐ | — |
| TOPO-milvus | `/app/bp-milvus` → Topology | Read placement | **active-passive** → primary active + standby queriers; vector index on S3 + metadata in etcd, both replicated. | ☐ | — |
| TOPO-flink | `/app/bp-flink` → Topology | Read placement | **active-passive** → jobmanager active + standby (ZK/k8s leader-election); checkpoints on S3 replicated; resumes from last checkpoint. | ☐ | — |
| TOPO-debezium | `/app/bp-debezium` → Topology | Read placement | **active-passive** → connector active on primary + restartable on standby reading latest Kafka offset (offsets in bp-strimzi, MM2-replicated). | ☐ | — |
| TOPO-loki | `/app/bp-loki` → Topology | Read placement | **active-passive** → primary active + standby querier; all chunks/index on object-storage (bucket-replicated); flip `loki.<sov>`. | ☐ | — |
| TOPO-mimir | `/app/bp-mimir` → Topology | Read placement | **active-passive** → primary active + standby querier; TSDB blocks on object-storage replicated. | ☐ | — |
| TOPO-tempo | `/app/bp-tempo` → Topology | Read placement | **active-passive** → primary active + standby querier; traces on object-storage replicated. | ☐ | — |
| TOPO-nats-jetstream | `/app/bp-nats-jetstream` → Topology | Read placement | **active-passive** → 3-node JetStream (Raft) active in mgmt + leaf-node tunnel to standby; in-flight msgs drop unless `stream.mirror` enabled. | ☐ | — |
| TOPO-newapi | `/app/bp-newapi` → Topology | Read placement | **active-passive** → stateless API active on primary + standby; DNS flip via bp-continuum. | ☐ | — |
| TOPO-sso-bridge | `/app/bp-sso-bridge` → Topology | Read placement | **active-passive** → stateless auth bridge active on primary + standby; DNS flip via bp-continuum. | ☐ | — |
| TOPO-k8s-ws-proxy | `/app/bp-k8s-ws-proxy` → Topology | Read placement | **active-passive** → stateless WebSocket proxy active + standby; new connections route to standby, in-flight WS drop. | ☐ | — |

### 3.3 active-active (14) — each individually

| # | Tested page | Test case | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| TOPO-opensearch | `/app/bp-opensearch` → Topology | Read placement | **active-active** → N active HRs, **one per region**, both ingesting; cross-cluster replication (CCR follower catches up from leader). `perCluster[]` shows ≥2 active. | ☐ | — |
| TOPO-clickhouse | `/app/bp-clickhouse` → Topology | Read placement | **active-active** → each region runs its own replica of the same shard (native replication via Keeper); both ingest. | ☐ | — |
| TOPO-strimzi | `/app/bp-strimzi` → Topology | Read placement | **active-active** → per-cluster Kafka, MirrorMaker2 replicates topics both ways; consumer offset translation via MM2. | ☐ | — |
| TOPO-iceberg | `/app/bp-iceberg` → Topology | Read placement | **active-active** → catalog (Hive/REST) active in both; data + table-metadata on S3, data plane reads same S3 from both regions. | ☐ | — |
| TOPO-livekit | `/app/bp-livekit` → Topology | Read placement | **active-active** → stateless SFU active in both regions; new rooms route to surviving region (rooms ephemeral). | ☐ | — |
| TOPO-stunner | `/app/bp-stunner` → Topology | Read placement | **active-active** → stateless TURN/STUN relay active in both; new ICE sessions route to surviving region. | ☐ | — |
| TOPO-vllm | `/app/bp-vllm` → Topology | Read placement | **active-active** → stateless GPU inference active in both; weights on shared PVC / baked image; new requests route to survivor. | ☐ | — |
| TOPO-kserve | `/app/bp-kserve` → Topology | Read placement | **active-active** → stateless model serving active in both; artifacts on S3. | ☐ | — |
| TOPO-knative | `/app/bp-knative` → Topology | Read placement | **active-active** → controller active in both; serverless Pods ephemeral; scale-to-zero unaffected. | ☐ | — |
| TOPO-librechat | `/app/bp-librechat` → Topology | Read placement | **active-active** → UI stateless active in both; chat history in PG via bp-cnpg-pair. | ☐ | — |
| TOPO-llm-gateway | `/app/bp-llm-gateway` → Topology | Read placement | **active-active** → stateless proxy active in both; secrets via External-Secrets. | ☐ | — |
| TOPO-anthropic-adapter | `/app/bp-anthropic-adapter` → Topology | Read placement | **active-active** → stateless adapter active in both; DNS load-balanced. | ☐ | — |
| TOPO-bge | `/app/bp-bge` → Topology | Read placement | **active-active** → stateless embedding service active in both; model in image. | ☐ | — |
| TOPO-nemo-guardrails | `/app/bp-nemo-guardrails` → Topology | Read placement | **active-active** → stateless policy enforcement active in both; policies from Git. | ☐ | — |

### 3.4 singleton (44) — representative set + coverage-sweep

> Singletons declare **1 HR on the primary cluster only**; region-kill loses the instance until Velero-restored. Representative explicit rows below, then one coverage-sweep row for the remainder.

| # | Tested page | Test case | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| TOPO-powerdns-admin | `/app/bp-powerdns-admin` → Topology | Read placement | **singleton** → 1 HR (admin UI state in PG); a cluster failure means edit via another cluster's instance; no cross-cluster sync. | ☐ | — |
| TOPO-litmus | `/app/bp-litmus` → Topology | Read placement | **singleton** → 1 HR; chaos experiments are intentionally cluster-scoped; region-kill loses experiment runs. | ☐ | — |
| TOPO-seaweedfs | `/app/bp-seaweedfs` → Topology | Read placement | **singleton** (per cluster) → 1 HR primary; within-cluster replicas + erasure coding; cross-cluster is async pull only — **region-kill-loses-it** warning if not replicated. | ☐ | — |
| TOPO-cnpg | `/app/bp-cnpg` → Topology | Read placement | **singleton** → 1 HR operator (stateless); the Clusters it manages get their own topology via bp-cnpg-pair. | ☐ | — |
| TOPO-cilium | `/app/bp-cilium` → Topology | Read placement | **singleton** (per cluster) → each cluster owns its identity space; ClusterMesh shares endpoint reachability only, not control state. | ☐ | — |
| TOPO-velero | `/app/bp-velero` → Topology | Read placement | **singleton** → 1 HR; backup catalog in PVC + blobs on replicated S3; restore is operator-initiated (it IS the DR tool). | ☐ | — |
| TOPO-singleton-sweep | each remaining singleton's `/app/$id` → Topology | Walk the rest | **Coverage sweep** for the remaining ~38 singletons (alloy, cert-manager(+webhooks), coraza, crossplane(+claims), external-dns, external-secrets(+stores), falco, flux, gateway-api, hcloud-*, kyverno(+policies), network-policies, opentelemetry(+operator), reflector, reloader, sealed-secrets, self-sovereign-cutover, sigstore, syft-grype, trivy, vpa, velero-hcs, qa-app, stalwart-sovereign, bp-*-vcluster, bp-vcluster-helmrepo, cluster-autoscaler-hcloud): each shows **1 HR on the primary cluster only**, no cross-cluster sync, region-kill warning where stateful. `bp-stalwart-sovereign` is **external** (mothership) — no Sovereign placement. `openclaw` is a scaffold with no topology declared yet. | ☐ | — |

## 4. PER-APP / per-zone vCluster containment

> Each App Blueprint must run **inside** its declared vCluster (mgmt / dmz / rtz), with **only** substrate prerequisites on the host cluster. No App Blueprint may schedule directly on a host node. Walked at `/app/$id` → placement (vCluster column). Grouped by zone; representative apps named per group.

| # | Zone / scope | Apps (representative) | What you must see | Result | Evidence |
|---|---|---|---|---|---|
| VC-mgmt | **mgmt** vCluster (control plane) | bp-catalyst-platform, bp-keycloak, bp-gitea, bp-harbor, bp-grafana, bp-openbao, bp-guacamole, bp-netbird, bp-powerdns-admin, bp-spire, bp-loki/mimir/tempo, bp-nats-jetstream, bp-sso-bridge | Each runs **inside** a `mgmt` vCluster (catalyst-platform `mgmt-A`/`mgmt-B`); none scheduled on the host node. `perCluster[].vcluster` = mgmt. | ☐ | — |
| VC-rtz | **rtz** vCluster (regulated tenant) | bp-cnpg-pair, bp-ferretdb, bp-valkey, bp-opensearch, bp-clickhouse, bp-matrix, bp-langfuse, bp-temporal, bp-openmeter, bp-neo4j, bp-stalwart-tenant, bp-wordpress-tenant, bp-milvus, bp-flink, bp-strimzi | Each runs **inside** the Org's `rtz` vCluster; tenant workloads never touch a host node. `perCluster[].vcluster` = rtz. | ☐ | — |
| VC-dmz | **dmz** vCluster (DMZ tenant) | tenant Apps placed in the DMZ tier per Org policy (bp-dmz-vcluster host) | DMZ-tier tenant Apps run **inside** the `dmz` vCluster; substrate-only on host. | ☐ | — |
| VC-host-substrate | **host** cluster (substrate only) | bp-cilium, bp-cert-manager(+webhooks), bp-flux, bp-gateway-api, bp-crossplane, bp-external-secrets, bp-external-dns, bp-kyverno, bp-sealed-secrets, bp-coraza, bp-falco, bp-vpa, bp-reloader, bp-reflector, bp-opentelemetry, bp-seaweedfs, bp-velero, bp-*-vcluster operators | These ARE the host-substrate prereqs — they run on the host cluster by design (singleton per cluster). **No App-tier Blueprint** (anything in §2/§3.1–§3.3 tenant/CP list) should appear on a host node. | ☐ | — |

---

## Coverage summary

- **Per-app SSO rows (§2):** 24 ssoEnabled blueprints, **26 ssoEnabled endpoints** → explicit rows. Tier-1 (4): grafana, gitea, harbor, openbao. Tier-2 (4): guacamole, powerdns-admin, netbird, spire(⛔ #3084). Tier-3 (16 endpoints): opensearch×2, matrix, langfuse, temporal, librechat, openmeter, wordpress-tenant, vllm, kserve, livekit, llm-gateway, flink, neo4j, litmus, stalwart-tenant. Plus negative-assertion rows for keycloak/hubble/wire-protocol/registry (no Open button). **catalyst-platform** (console/api) is the console itself — its SSO is exercised by Pillar-1 login, not an Open button.
- **Per-app topology rows (§3):** every active-hot-standby (10), active-passive (20), active-active (14) app individually; singletons (44) as a representative set + coverage-sweep. Matches the audit's 10 / 20 / 14 / 44.
- **gitea / openbao / grafana each have BOTH** an explicit SSO row (SSO-gitea, SSO-openbao, SSO-grafana) **and** an explicit topology row (TOPO-gitea, TOPO-openbao, TOPO-grafana). ✔
- **Gaps flagged (not passes):** spire SSO ⛔ #3084 (scaffold, no OIDC client); hubble has an endpoint but no `sso` block (no Open button today); sandbox topology recorded but OUT of scope.
