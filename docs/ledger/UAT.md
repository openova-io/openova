# UAT — OpenOva Catalyst (unified test-case table)

> One flat table — one row per test step. **Test group · Tested page (click to open) · Test case (what you do) · What you must see · Result · Screenshot (click to view).** Newest fresh-prov walk (hw99, 2026-06-05, live 2-region) first, then the full matrix. Narrative, progress log + prior-prov detail: [`../sessions/2026-06-05/uat-status-log.md`](../sessions/2026-06-05/uat-status-log.md).
> Legend: ✅ pass · ◑ partial · ❌ fail · ⛔ blocked · ⏳ not-walked · ☐ pending.

| Test group | Tested page | Test case (what you do) | What you must see | Result | Screenshot |
|---|---|---|---|---|---|
| TC-01 — Handover auto-redirect | [console.hw99/dashboard](https://console.hw99.omantel.biz/dashboard) | Mint handover token, open the handover URL | Auto-lands on Sovereign /dashboard — no login, no FQDN typed | ✅ (hw99 2026-06-05) | [img](../sessions/2026-06-05/evidence/hw99-tc01-handover-dashboard.png) |
| TC-02 — Operator PIN sign-in | [console/sovereign/login](https://console.openova.io/sovereign/login) | Enter operator email → receive 6-digit PIN → enter … | Authenticated, lands on the requested page | ✅ (hw99) — email-PIN walke… | [img](../sessions/2026-06-05/evidence/hw99-tc01-provisioning-apps-pending.png) |
| TC-03 — First-touch (Settings) | [console.hw99/settings](https://console.hw99.omantel.biz/settings) | Open Settings | Real Sovereign values (FQDN/region/CP/deployment id) | ✅ (hw99) | [img](../sessions/2026-06-05/evidence/hw99-tc03-settings-real.png) |
| TC-03 — First-touch (Users) | [console.hw99/users](https://console.hw99.omantel.biz/users) | Open User Access | Operator listed at owner tier | ✅ (hw99) | [img](../sessions/2026-06-05/evidence/hw99-tc03-users-owner.png) |
| TC-04 — Marketplace is live | [marketplace.hw99/](https://marketplace.hw99.omantel.biz/) | Open the marketplace storefront | Storefront 'Build Your Tenant' renders (Pillar 1) | ✅ (hw99) | [img](../sessions/2026-06-05/evidence/hw99-pillar1-marketplace.png) |
| TC-05 — Issue a voucher (BSS) | [console.hw99/bss](https://console.hw99.omantel.biz/bss) | Open BSS → Vouchers | Voucher operations surface (Pillar 1) | ✅ (hw99) | [img](../sessions/2026-06-05/evidence/hw99-tc04-bss-voucher.png) |
| TC-09 — BCP topology (Pillar 2) | [console.hw99/cloud](https://console.hw99.omantel.biz/cloud) | Open Cloud view | 2-region topology rendered (me-east-215-a + -b) | ✅ (hw99) | [img](../sessions/2026-06-05/evidence/hw99-tc-pillar2-cloud-2region.png) |
| TC-00d — Apps converged | [console.hw99/apps](https://console.hw99.omantel.biz/apps) | Open Apps | All 49 apps INSTALLED (fully converged) | ✅ (hw99) | [img](../sessions/2026-06-05/evidence/hw99-apps-converged.png) |
| TC-17 — Launch silent-SSO (#3061) | [console.hw99/app/bp-grafana](https://console.hw99.omantel.biz/app/bp-grafana) | Open Grafana app detail | Launch button + endpoint present | ✅ (hw99) | [img](../sessions/2026-06-05/evidence/hw99-grafana-appdetail-launch.png) |
| TC-17 — Launch silent-SSO (#3061) | [grafana.hw99/login](https://grafana.hw99.omantel.biz/login) | Grafana login page | Grafana serves login (not a localhost error) | ✅ (hw99) | [img](../sessions/2026-06-05/evidence/hw99-3061-grafana-login.png) |
| TC-17 — Launch silent-SSO (#3061) | [grafana.hw99/login/generic_o…](https://grafana.hw99.omantel.biz/login/generic_oauth) | Click 'Sign in with OpenOva SSO' | OIDC redirect_uri = real host auth.hw99.omantel.biz (NOT lo… | ✅ (hw99) FIXED | [img](../sessions/2026-06-05/evidence/hw99-3061-sso-redirect-realhost.png) |
| TC-00a — The deployment appears a… | [console/sovereign](https://console.openova.io/sovereign) | Sign in (operator PIN), open the deployments list | Your in-flight deployment listed with `status: provisioning` | ✅ | — |
| TC-00a — The deployment appears a… | The deployment row | Open it (`/sovereign/provision/<dep-id>`) | Header shows the Sovereign FQDN + **the BCP topology you or… | ✅ | — |
| TC-00a — The deployment appears a… | The provision overview | Read the **regions** | **Exactly the regions ordered** — an active-hot-standby Sov… | ✅ (executor-observed) | [img](../sessions/2026-06-04/evidence/hw93-mothership-cloud-2regions.png) |
| TC-00b — The provisioning jobs ru… | /sovereign/provision/<dep-id>/… | Read the jobs list | Jobs grouped/labelled **per region** — every install job pr… | ❌ labeling defect | [img](../sessions/2026-06-04/evidence/hw93-jobs-both-regions-117of119.png) |
| TC-00b — The provisioning jobs ru… | A specific job (e.g. jobs/inst… | Open it | Job detail/log streams; **both regions** represented (not j… | ⚠️ both regions' jobs exis… | — |
| TC-00b — The provisioning jobs ru… | The jobs view over ~the prov w… | Refresh every ~10 min | Jobs advance to success; no job stuck failed; the doc Statu… | ✅ | — |
| TC-00c — Convergence → handover h… | The provision view | Wait through convergence | `status` advances `provisioning → … → ready` only when the … | ☐ | — |
| TC-00c — Convergence → handover h… | On ready | Follow the handover hand-off | Auto-redirect into the Sovereign console (continues at **TC… | ☐ | — |
| TC-00d — Applications tab: per-ap… | /sovereign/provision/<dep> (Ap… | Read the **Deployments** + **Catalog** tabs | Deployments count + Catalog count; each app card shows a li… | ✅ | [img](../sessions/2026-06-04/evidence/hw93-mothership-apps.png) |
| TC-00d — Applications tab: per-ap… | The app grid | Scan for failures | No app stuck FAILED on a healthy prov | ❌ finding | — |
| TC-00e — User Access (RBAC) | /sovereign/provision/<dep>/users | Read the User Access surface | "Per-user access to Sovereigns × Applications × Namespaces … | ✅ | — |
| TC-00f — Settings: deployment con… | /sovereign/provision/<dep>/set… | Read it | The **actual** org you ordered (Omantel) | ❌ DATA BUG | — |
| TC-00f — Settings: deployment con… | **Sovereign** section | Read FQDN/Region/Capacity/Created/Status | The real values (`hw93.omantel.biz`, `me-east-215`, `m7n.la… | ❌ DATA BUG | — |
| TC-00f — Settings: deployment con… | **Cloud credentials** + **DNS** | Read provider + pool domain | `huawei` provider; pool `omantel.biz` | ❌ DATA BUG | — |
| TC-00f — Settings: deployment con… | API tokens / Notifications / W… | Note state | — | ⚠️ scaffold | — |
| TC-00g — Dashboard (treemap) | /sovereign/provision/<dep>/das… | Read the treemap | Resource allocation/utilisation once the cluster reports | ⏳ | — |
| TC-01 — Handover auto-redirect | console.openova.io/sovereign/p… | Watch the provisioning progress page (no clicks) | Per-region stage rows advance; **no stage stuck Pending aft… | ✅ (hw96) | [img](../sessions/2026-06-04/evidence/hw96-part0-tc00b-jobs-bootstrap-succeeded.png) |
| TC-01 — Handover auto-redirect | Same page, at ready | Do nothing — wait | Browser **auto-redirects** to the Sovereign Console (`/auth… | ✅ | [img](../sessions/2026-06-04/evidence/hw96-walk-tc01-tc03-operator-dashboard.png) |
| TC-02 — Operator PIN sign-in | console.hw96.omani.works | Open the URL | Page loads over **publicly-trusted TLS** (no browser warnin… | ✅ TLS | — |
| TC-02 — Operator PIN sign-in | Sign in / PIN entry | email → Send code → 6-digit PIN | Advances to /dashboard, authenticated | ⏭️ bypassed | — |
| TC-02 — Operator PIN sign-in | /dashboard | Navigate around | Still signed in — no re-PIN within session TTL (D14) | ✅ | — |
| TC-03 — First-touch sanity | /dashboard | Look at the URL bar after login | You are on **/dashboard** — NOT `/wizard` (D23) | ✅ | [img](../sessions/2026-06-04/evidence/hw96-walk-tc01-tc03-operator-dashboard.png) |
| TC-03 — First-touch sanity | Sidebar | Scan the sidebar entries | NO mothership-only views: no fleet dashboard, no "+ New dep… | ✅ | — |
| TC-03 — First-touch sanity | /users | Open **User Access** | The operator is listed with **tier=owner** and their email … | ✅ | [img](../sessions/2026-06-04/evidence/hw96-walk-tc03-users-owner-tier.png) |
| TC-03 — First-touch sanity | /settings | Open **Settings** | Real values for Region, Capacity, Control-plane size, Creat… | ✅ (was ❌ on hw93) | [img](../sessions/2026-06-04/evidence/hw96-walk-tc03-settings-real-values.png) |
| TC-04 — Marketplace is live | marketplace.hw96.omani.works | Open the URL | Marketplace landing renders with a **non-empty catalog** — … | ✅ | [img](../sessions/2026-06-04/evidence/hw96-walk-tc04-marketplace-live.png) |
| TC-05 — Issue a voucher from BSS | Sidebar | Tap **BSS** | **"BSS — Business Support Systems"** landing: KPI strip + s… | ✅ | — |
| TC-05 — Issue a voucher from BSS | console.hw96.omani.works/bss/v… | Tap **+ Issue voucher** | Modal **"Issue voucher"** with fields: **Code**, **Credit (… | ✅ | [img](../sessions/2026-06-04/evidence/hw96-walk-tc05-voucher-backend-timeout.png) |
| TC-05 — Issue a voucher from BSS | Issue voucher modal | Fill Code + Credit + the test recipient email, tap … | Button shows "Issuing…", modal closes, new row appears in t… | ✅ | [img](../sessions/2026-06-04/evidence/hw96-walk-tc05-voucher-issued-active.png) |
| TC-05 — Issue a voucher from BSS | Recipient inbox | Open the voucher email | Email arrived via the **Sovereign's own SMTP**; link is exa… | ❌ | — |
| TC-06 — Redeem the voucher | marketplace.hw96.omani.works/r… | Open the redeem link from the email | **"Voucher valid"** card with the **OMR credit** amount + t… | ✅ | [img](../sessions/2026-06-04/evidence/hw96-walk-tc06-voucher-valid.png) |
| TC-06 — Redeem the voucher | Redeem page (negative path) | Edit the URL to a garbage code and reload | **"Voucher not valid"** state — clear message, no crash, no… | ✅ | [img](../sessions/2026-06-04/evidence/hw96-walk-tc06-negative-not-valid.png) |
| TC-06 — Redeem the voucher | Back on the valid voucher card | Tap **Sign up to redeem** | Advances to **Pick a plan** (/plans); code carried to check… | ✅ | — |
| TC-07 — Pick plan and apps | marketplace.hw96.omani.works/p… | On **"Pick a plan"**, tap a plan card, tap Continue | Advances to **"Build your stack"** (/apps) | ✅ | [img](../sessions/2026-06-04/evidence/hw96-walk-tc07-plans.png) |
| TC-07 — Pick plan and apps | marketplace.hw96.omani.works/a… | Select at least one **Postgres-backed** app (the ca… | Advances to **"Setup & extras"** (/addons) | ☐ | — |
| TC-08 — Choose the free subdomain | marketplace.hw96.omani.works/a… | On **"Setup & extras"**, find **Your domain** | Subdomain field + a **pool picker** offering the operator-c… | ☐ | — |
| TC-08 — Choose the free subdomain | Your domain | Type a 2-character subdomain | Inline rejection ("at least 3 characters") — Continue stays… | ☐ | — |
| TC-08 — Choose the free subdomain | Your domain | Type a valid subdomain (e.g. `muscatpharmacy`), pic… | Subdomain accepted; advances to **Business continuity** (/b… | ☐ | — |
| TC-09 — Choose BCP topology at si… | marketplace.hw96.omani.works/bcp | Read the step | Heading **"Business continuity"** + *"Pick how your databas… | ✅ | [img](../sessions/2026-06-04/evidence/hw96-walk-tc09-bcp-pillar2-regions.png) |
| TC-09 — Choose BCP topology at si… | Topology cards | Tap **Active-hot-standby** | **Primary region** and **Replica region** pickers appear, e… | ✅ | — |
| TC-09 — Choose BCP topology at si… | Region pickers (negative path) | Pick the SAME region for both | Inline error **"Primary and replica must differ"** — Contin… | ◑ | — |
| TC-09 — Choose BCP topology at si… | Region pickers | Pick two different regions, Continue | Advances to **"Review & launch"** (/review) | ✅ | — |
| TC-10 — Review, checkout, create … | marketplace.hw96.omani.works/r… | On **"Review & launch"**, check **Your stack**, **P… | All reflect the choices made in TC-07/08/09, incl. the two … | ◑ | — |
| TC-10 — Review, checkout, create … | /review | Tap Checkout | Advances to **"Checkout"** | ✅ | [img](../sessions/2026-06-04/evidence/hw96-walk-tc10-checkout-pin-sent.png) |
| TC-10 — Review, checkout, create … | marketplace.hw96.omani.works/c… | Sign in: type the customer email, request the code,… | PIN accepted; the voucher **credit is applied** to the total | ⛔ | [img](../sessions/2026-06-04/evidence/hw96-walk-tc10-checkout-pin-sent.png) |
| TC-10 — Review, checkout, create … | Checkout | Confirm | **"Setting up your tenant"** progress, then **"Your tenant … | ⛔ | — |
| TC-11 — The Organization is online | "Your tenant is ready!" | Follow the tenant link (or auto-redirect) | Lands on `https://console.<orgslug>.<pool-tld>` with **publ… | ⛔ | — |
| TC-11 — The Organization is online | Tenant console | PIN-login as the customer | **Dashboard renders** (Phase 2a) — not an empty/error page | ⛔ | — |
| TC-11 — The Organization is online | Tenant apps view | Look at the app cards | The apps chosen in TC-07 appear as cards with status badges… | ⛔ | — |
| TC-11 — The Organization is online | A green app card | Tap **Open** | The app itself opens, **already signed in** via the Org's r… | ⛔ | — |
| TC-12 — Operator sees the new ten… | console.hw96.omani.works/bss/t… | Open **BSS → Tenants** | The new Organization appears with its chosen pool subdomain | ⛔ | — |
| TC-12 — Operator sees the new ten… | console.hw96.omani.works/bss/v… | Find the issued voucher row, expand it | Drawer shows **Redemptions** incremented exactly once (#200… | ◑ | — |
| TC-13 — Multi-region dashboard & … | console.hw96.omani.works/dashb… | Set Layer-1 = Cluster | **One bubble per region** (2 on hw96) — not a single merged… | ◑ | — |
| TC-13 — Multi-region dashboard & … | /dashboard | Set Layer-2 = Namespace | Namespace bubbles render **within** each cluster bubble | ☐ | — |
| TC-13 — Multi-region dashboard & … | console.hw96.omani.works/cloud… | Read the kind chips | **All regions** present, no stuck spinners (D5); no kind ch… | ✅ | [img](../sessions/2026-06-04/evidence/hw96-walk-tc13-cloud-2region-topology.png) |
| TC-13 — Multi-region dashboard & … | /cloud, any resource cell | Click a leaf cell | Drill-down opens the resource detail — clicks work on leave… | ☐ | — |
| TC-14 — Jobs are terminal and reg… | [console.hw91/jobs](https://console.hw91.omantel.biz/jobs) | Open **Jobs** | **0 pending, 0 running** — every job in a terminal state (D… | ☐ | — |
| TC-14 — Jobs are terminal and reg… | /jobs | Read job rows | Per-region prefixes visible on a multi-region Sovereign; th… | ☐ | — |
| TC-15 — Catalog: class page and i… | console.hw96.omani.works/apps | Open **Applications**, switch to the **Catalog** tab | Blueprint **class** cards (one per Blueprint, NOT one per i… | ✅ | [img](../sessions/2026-06-05/evidence/hw96-walk-tc15-applications-deployments.png) |
| TC-15 — Catalog: class page and i… | A class card (e.g. Grafana) | Click it | **Catalog detail**: Blueprint header + **supported-topology… | ✅ | [img](../sessions/2026-06-05/evidence/hw96-walk-tc17-grafana-launch-sso-button.png) |
| TC-16 — Three coexisting instance… | Catalog detail (Grafana) | Tap **+ New instance**, complete the install form, … | Each install accepted — no name-collision crash; 409 with a… | ☐ | — |
| TC-16 — Three coexisting instance… | Catalog detail (Grafana) | Read the instance table | **3 rows**, each with its own name + status | ☐ | — |
| TC-16 — Three coexisting instance… | Each instance row | Open each instance's detail → its endpoint | **3 distinct URLs**, each serving its own Grafana (change a… | ☐ | — |
| TC-17 — Launch silent-SSO: Tier-1… | App detail — **Grafana** | Tap **Launch →** | New tab opens **already signed in** (silent SSO `prompt=non… | ❌ | [img](../sessions/2026-06-05/evidence/hw96-walk-tc17-grafana-launch-sso-button.png) [img](../sessions/2026-06-05/evidence/hw96-walk-tc17-grafana-FAIL-redirect-uri-localhost.png) |
| TC-17 — Launch silent-SSO: Tier-1… | App detail — **Gitea** | Tap **Launch →** | Same: signed-in Gitea, no form | ☐ | — |
| TC-17 — Launch silent-SSO: Tier-1… | App detail — **Harbor** | Tap **Launch →** | Same: signed-in Harbor, no form | ☐ | — |
| TC-17 — Launch silent-SSO: Tier-1… | App detail — **OpenBao** | Tap **Launch →** | OpenBao opens authenticated via OIDC (architectural note: n… | ☐ | — |
| TC-18 — Endpoint edit → governed … | App detail → Connection | Rename an endpoint (e.g. `grafana` → `metrics`), sa… | UI confirms the change was submitted **as a Git PR** — not … | ☐ | — |
| TC-18 — Endpoint edit → governed … | Gitea (via TC-17 step 2 session) | Open the Org's `iac` repo → Pull requests | The PR exists with **3 named checks**: `kyverno-admission`,… | ☐ | — |
| TC-18 — Endpoint edit → governed … | The PR | Watch checks complete | All 3 green → PR **auto-merges** | ☐ | — |
| TC-18 — Endpoint edit → governed … | Browser, ≤ 2 min after merge | Open `https://metrics.<…>` (the NEW name) | New FQDN serves with **valid TLS** and **silent SSO still w… | ☐ | — |
| TC-19 — RBAC surfaces | [console.hw91/rbac/roles](https://console.hw91.omantel.biz/rbac/roles) | Open **Keycloak Roles** | Real role catalog renders (not empty/error) | ☐ | — |
| TC-19 — RBAC surfaces | [console.hw91/rbac/matrix](https://console.hw91.omantel.biz/rbac/matrix) | Open **Access matrix** | Subjects × roles grid with the owner-tier operator visible | ☐ | — |
| TC-19 — RBAC surfaces | [console.hw91/users/new](https://console.hw91.omantel.biz/users/new) | Create a second operator user (Name, Email, Roles),… | Success toast → user appears in **User Access** list | ☐ | — |
| TC-21 — Cross-Org realm isolation | Browser profile A | Sign in to Org-A's console | Org-A dashboard | ☐ | — |
| TC-21 — Cross-Org realm isolation | Same profile A | Open Org-B's console URL | **A fresh sign-in is demanded** — Org-A's session does NOT … | ☐ | — |
| TC-21 — Cross-Org realm isolation | Profile A, Org-A's Grafana vs … | Open both app URLs | Org-A's opens signed-in; Org-B's demands sign-in — per-Org … | ☐ | — |
| TC-22 — Install a CNPG-backed app… | Tenant marketplace/catalog | Pick a CNPG-backed app (e.g. WordPress/Ghost), choo… | Install starts; app card progresses to green | ☐ | — |
| TC-22 — Install a CNPG-backed app… | App detail → Topology | Read the placement | The app shows under **both regions** — primary + replica, t… | ☐ | — |
| TC-22 — Install a CNPG-backed app… | The app's FQDN | Open it | App serves with trusted TLS | ☐ | — |
| TC-23 — Region-kill: the app surv… | The app's FQDN | Confirm it serves, note the time | HTTP 200, content loads | ☐ | — |
| TC-23 — Region-kill: the app surv… | (Dev team kills the primary re… | Keep refreshing the app URL | Service resumes within **≤ 30 s** — same FQDN, no manual DN… | ☐ | — |
| TC-23 — Region-kill: the app surv… | Operator /dashboard | Read the region bubbles | Failed region shows unhealthy; surviving region carries the… | ☐ | — |
| TC-23 — Region-kill: the app surv… | The app | Log in / use it | Data written before the kill is **all present** (zero loss … | ☐ | — |
| TC-24 — Trigger the cutover | The Sovereignty surface (produ… | Read the card | **"Sovereignty status"** with badge **"Soft-tethered to mot… | ☐ | — |
| TC-24 — Trigger the cutover | The card | Tap **Achieve True Sovereignty** | An explanation modal opens first — the cutover is **irrever… | ☐ | — |
| TC-24 — Trigger the cutover | The modal | Confirm | Progress card appears; steps advance (e.g. **Mirrored commi… | ☐ | — |
| TC-25 — Cutover completes | Progress card | Wait through the final step | **"Egress test"** runs the 10-minute deny-egress hold and p… | ☐ | — |
| TC-25 — Cutover completes | Sovereignty card | Read the badge | Flips to **"Independent"** — `cutoverComplete=true` with th… | ☐ | — |
| TC-26 — Post-cutover regression: … | Fresh browser | PIN-login at console.hw91.omantel.biz | Works exactly as in TC-02 — the PIN/JWT issuer is now the S… | ☐ | — |
| TC-26 — Post-cutover regression: … | App detail — Grafana | Tap **Launch →** | Silent SSO still works — no login form, no redirect loop | ☐ | — |
| TC-26 — Post-cutover regression: … | Tenant console | Customer PIN-login + open an app | Tenant flows unaffected | ☐ | — |
| TC-26 — Post-cutover regression: … | Marketplace | Open /redeem with a fresh voucher | Voucher flow still works end-to-end post-cutover | ☐ | — |

