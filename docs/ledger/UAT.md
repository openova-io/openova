# UAT — OpenOva Catalyst (unified test-case table)

> One flat table — one row per test step. **Test group · Tested page (click to open) · Test case (what you do) · What you must see · Result · Screenshot (click to view).** Newest fresh-prov walk (hw99, 2026-06-05, live 2-region) first, then the full matrix. Narrative, progress log + prior-prov detail: [`../sessions/2026-06-05/uat-status-log.md`](../sessions/2026-06-05/uat-status-log.md).
> Legend: ✅ pass · ◑ partial · ❌ fail · ⛔ blocked · ⏳ not-walked · ☐ pending.

| Test group | Tested page | Test case (what you do) | What you must see | Result | Screenshot |
|---|---|---|---|---|---|
| TC-01 Handover auto-r… | [console.hw99/…](https://console.hw99.omantel.biz/dashboard) | Mint handover token, open the handove… | Auto-lands on Sovereign /dashboard — no log… | ✅ (hw99 2026… | [img](../sessions/2026-06-05/evidence/hw99-tc01-handover-dashboard.png) |
| TC-02 Operator PIN si… | [console/sover…](https://console.openova.io/sovereign/login) | Enter operator email → receive 6-digi… | Authenticated, lands on the requested page | ✅ (hw99) — e… | [img](../sessions/2026-06-05/evidence/hw99-tc01-provisioning-apps-pending.png) |
| TC-03 First-touch (Se… | [console.hw99/…](https://console.hw99.omantel.biz/settings) | Open Settings | Real Sovereign values (FQDN/region/CP/deplo… | ✅ (hw99) | [img](../sessions/2026-06-05/evidence/hw99-tc03-settings-real.png) |
| TC-03 First-touch (Us… | [console.hw99/…](https://console.hw99.omantel.biz/users) | Open User Access | Operator listed at owner tier | ✅ (hw99) | [img](../sessions/2026-06-05/evidence/hw99-tc03-users-owner.png) |
| TC-04 Marketplace is … | [marketplace.h…](https://marketplace.hw99.omantel.biz/) | Open the marketplace storefront | Storefront 'Build Your Tenant' renders (Pil… | ✅ (hw99) | [img](../sessions/2026-06-05/evidence/hw99-pillar1-marketplace.png) |
| TC-05 Issue a voucher… | [console.hw99/…](https://console.hw99.omantel.biz/bss) | Open BSS → Vouchers | Voucher operations surface (Pillar 1) | ✅ (hw99) | [img](../sessions/2026-06-05/evidence/hw99-tc04-bss-voucher.png) |
| TC-09 BCP topology (P… | [console.hw99/…](https://console.hw99.omantel.biz/cloud) | Open Cloud view | 2-region topology rendered (me-east-215-a +… | ✅ (hw99) | [img](../sessions/2026-06-05/evidence/hw99-tc-pillar2-cloud-2region.png) |
| TC-00d Apps converged | [console.hw99/…](https://console.hw99.omantel.biz/apps) | Open Apps | All 49 apps INSTALLED (fully converged) | ✅ (hw99) | [img](../sessions/2026-06-05/evidence/hw99-apps-converged.png) |
| TC-17 Launch silent-S… | [console.hw99/…](https://console.hw99.omantel.biz/app/bp-grafana) | Open Grafana app detail | Launch button + endpoint present | ✅ (hw99) | [img](../sessions/2026-06-05/evidence/hw99-grafana-appdetail-launch.png) |
| TC-17 Launch silent-S… | [grafana.hw99/…](https://grafana.hw99.omantel.biz/login) | Grafana login page | Grafana serves login (not a localhost error) | ✅ (hw99) | [img](../sessions/2026-06-05/evidence/hw99-3061-grafana-login.png) |
| TC-17 Launch silent-S… | [grafana.hw99/…](https://grafana.hw99.omantel.biz/login/generic_oauth) | Click 'Sign in with OpenOva SSO' | OIDC redirect_uri = real host auth.hw99.oma… | ✅ (hw99) FIX… | [img](../sessions/2026-06-05/evidence/hw99-3061-sso-redirect-realhost.png) |
| TC-07 Pick plan | [mkt/plans](https://marketplace.hw99.omantel.biz/plans) | Open the plan-selection step | Plan cards render (Pillar 1 wizard step 1) | ✅ (hw99) | [img](../sessions/2026-06-05/evidence/hw99-tc07-plans.png) |
| TC-07 Pick apps | [mkt/apps](https://marketplace.hw99.omantel.biz/apps) | Open the app-picker step | App catalog renders for selection | ✅ (hw99) | [img](../sessions/2026-06-05/evidence/hw99-tc07-apps.png) |
| TC-09 BCP at signup | [mkt/bcp](https://marketplace.hw99.omantel.biz/bcp) | Open the BCP step | Business-continuity topology chosen at sign… | ✅ (hw99) | [img](../sessions/2026-06-05/evidence/hw99-tc09-bcp-topology.png) |
| TC-10 Checkout / crea… | [mkt/checkout](https://marketplace.hw99.omantel.biz/checkout) | Reach checkout (subdomain + create-Or… | Checkout + org-creation form renders | ◑ (hw99, rea… | [img](../sessions/2026-06-05/evidence/hw99-tc10-checkout-org.png) |
| TC-14 Jobs terminal | [jobs](https://console.hw99.omantel.biz/jobs) | Open Jobs | Jobs list terminal + region-filterable | ✅ (hw99) | [img](../sessions/2026-06-05/evidence/hw99-tc14-jobs.png) |
| TC-19 RBAC/Complianc… | [compliance](https://console.hw99.omantel.biz/sre/compliance) | Open Compliance | Compliance/RBAC surface renders | ✅ (hw99) | [img](../sessions/2026-06-05/evidence/hw99-compliance.png) |
| TC-24 Cutover trigger | [settings](https://console.hw99.omantel.biz/settings) | Open Settings → Sovereignty card | 'Achieve True Sovereignty' cutover card mounted (#3064) | ✅ (hw99) | [img](../sessions/2026-06-05/evidence/hw99-tc24-sovereignty-card.png) |
| TC-00a The deployment … | [console/sover…](https://console.openova.io/sovereign) | Sign in (operator PIN), open the depl… | Your in-flight deployment listed with `stat… | ✅ | — |
| TC-00a The deployment … | The deployment … | Open it (`/sovereign/provision/<dep-i… | Header shows the Sovereign FQDN + **the BCP… | ✅ | — |
| TC-00a The deployment … | The provision o… | Read the **regions** | **Exactly the regions ordered** — an active… | ✅ (executor-… | [img](../sessions/2026-06-04/evidence/hw93-mothership-cloud-2regions.png) |
| TC-00b The provisionin… | /sovereign/prov… | Read the jobs list | Jobs grouped/labelled **per region** — ever… | ❌ labeling d… | [img](../sessions/2026-06-04/evidence/hw93-jobs-both-regions-117of119.png) |
| TC-00b The provisionin… | A specific job … | Open it | Job detail/log streams; **both regions** re… | ⚠️ both regi… | — |
| TC-00b The provisionin… | The jobs view o… | Refresh every ~10 min | Jobs advance to success; no job stuck faile… | ✅ | — |
| TC-00c Convergence → h… | The provision v… | Wait through convergence | `status` advances `provisioning → … → ready… | ☐ | — |
| TC-00c Convergence → h… | On ready | Follow the handover hand-off | Auto-redirect into the Sovereign console (c… | ☐ | — |
| TC-00d Applications ta… | /sovereign/prov… | Read the **Deployments** + **Catalog*… | Deployments count + Catalog count; each app… | ✅ | [img](../sessions/2026-06-04/evidence/hw93-mothership-apps.png) |
| TC-00d Applications ta… | The app grid | Scan for failures | No app stuck FAILED on a healthy prov | ❌ finding | — |
| TC-00e User Access (RB… | /sovereign/prov… | Read the User Access surface | "Per-user access to Sovereigns × Applicatio… | ✅ | — |
| TC-00f Settings: deplo… | /sovereign/prov… | Read it | The **actual** org you ordered (Omantel) | ❌ DATA BUG | — |
| TC-00f Settings: deplo… | **Sovereign** s… | Read FQDN/Region/Capacity/Created/Sta… | The real values (`hw93.omantel.biz`, `me-ea… | ❌ DATA BUG | — |
| TC-00f Settings: deplo… | **Cloud credent… | Read provider + pool domain | `huawei` provider; pool `omantel.biz` | ❌ DATA BUG | — |
| TC-00f Settings: deplo… | API tokens / No… | Note state | — | ⚠️ scaffold | — |
| TC-00g Dashboard (tree… | /sovereign/prov… | Read the treemap | Resource allocation/utilisation once the cl… | ⏳ | — |
| TC-01 Handover auto-r… | console.openova… | Watch the provisioning progress page … | Per-region stage rows advance; **no stage s… | ✅ (hw96) | [img](../sessions/2026-06-04/evidence/hw96-part0-tc00b-jobs-bootstrap-succeeded.png) |
| TC-01 Handover auto-r… | Same page, at r… | Do nothing — wait | Browser **auto-redirects** to the Sovereign… | ✅ | [img](../sessions/2026-06-04/evidence/hw96-walk-tc01-tc03-operator-dashboard.png) |
| TC-02 Operator PIN si… | console.hw96.om… | Open the URL | Page loads over **publicly-trusted TLS** (n… | ✅ TLS | — |
| TC-02 Operator PIN si… | Sign in / PIN e… | email → Send code → 6-digit PIN | Advances to /dashboard, authenticated | ⏭️ bypassed | — |
| TC-02 Operator PIN si… | /dashboard | Navigate around | Still signed in — no re-PIN within session … | ✅ | — |
| TC-03 First-touch san… | /dashboard | Look at the URL bar after login | You are on **/dashboard** — NOT `/wizard` (… | ✅ | [img](../sessions/2026-06-04/evidence/hw96-walk-tc01-tc03-operator-dashboard.png) |
| TC-03 First-touch san… | Sidebar | Scan the sidebar entries | NO mothership-only views: no fleet dashboar… | ✅ | — |
| TC-03 First-touch san… | /users | Open **User Access** | The operator is listed with **tier=owner** … | ✅ | [img](../sessions/2026-06-04/evidence/hw96-walk-tc03-users-owner-tier.png) |
| TC-03 First-touch san… | /settings | Open **Settings** | Real values for Region, Capacity, Control-p… | ✅ (was ❌ on … | [img](../sessions/2026-06-04/evidence/hw96-walk-tc03-settings-real-values.png) |
| TC-04 Marketplace is … | marketplace.hw9… | Open the URL | Marketplace landing renders with a **non-em… | ✅ | [img](../sessions/2026-06-04/evidence/hw96-walk-tc04-marketplace-live.png) |
| TC-05 Issue a voucher… | Sidebar | Tap **BSS** | **"BSS — Business Support Systems"** landin… | ✅ | — |
| TC-05 Issue a voucher… | console.hw96.om… | Tap **+ Issue voucher** | Modal **"Issue voucher"** with fields: **Co… | ✅ | [img](../sessions/2026-06-04/evidence/hw96-walk-tc05-voucher-backend-timeout.png) |
| TC-05 Issue a voucher… | Issue voucher m… | Fill Code + Credit + the test recipie… | Button shows "Issuing…", modal closes, new … | ✅ | [img](../sessions/2026-06-04/evidence/hw96-walk-tc05-voucher-issued-active.png) |
| TC-05 Issue a voucher… | Recipient inbox | Open the voucher email | Email arrived via the **Sovereign's own SMT… | ❌ | — |
| TC-06 Redeem the vouc… | marketplace.hw9… | Open the redeem link from the email | **"Voucher valid"** card with the **OMR cre… | ✅ | [img](../sessions/2026-06-04/evidence/hw96-walk-tc06-voucher-valid.png) |
| TC-06 Redeem the vouc… | Redeem page (ne… | Edit the URL to a garbage code and re… | **"Voucher not valid"** state — clear messa… | ✅ | [img](../sessions/2026-06-04/evidence/hw96-walk-tc06-negative-not-valid.png) |
| TC-06 Redeem the vouc… | Back on the val… | Tap **Sign up to redeem** | Advances to **Pick a plan** (/plans); code … | ✅ | — |
| TC-07 Pick plan and a… | marketplace.hw9… | On **"Pick a plan"**, tap a plan card… | Advances to **"Build your stack"** (/apps) | ✅ | [img](../sessions/2026-06-04/evidence/hw96-walk-tc07-plans.png) |
| TC-07 Pick plan and a… | marketplace.hw9… | Select at least one **Postgres-backed… | Advances to **"Setup & extras"** (/addons) | ☐ | — |
| TC-08 Choose the free… | marketplace.hw9… | On **"Setup & extras"**, find **Your … | Subdomain field + a **pool picker** offerin… | ☐ | — |
| TC-08 Choose the free… | Your domain | Type a 2-character subdomain | Inline rejection ("at least 3 characters") … | ☐ | — |
| TC-08 Choose the free… | Your domain | Type a valid subdomain (e.g. `muscatp… | Subdomain accepted; advances to **Business … | ☐ | — |
| TC-09 Choose BCP topo… | marketplace.hw9… | Read the step | Heading **"Business continuity"** + *"Pick … | ✅ | [img](../sessions/2026-06-04/evidence/hw96-walk-tc09-bcp-pillar2-regions.png) |
| TC-09 Choose BCP topo… | Topology cards | Tap **Active-hot-standby** | **Primary region** and **Replica region** p… | ✅ | — |
| TC-09 Choose BCP topo… | Region pickers … | Pick the SAME region for both | Inline error **"Primary and replica must di… | ◑ | — |
| TC-09 Choose BCP topo… | Region pickers | Pick two different regions, Continue | Advances to **"Review & launch"** (/review) | ✅ | — |
| TC-10 Review, checkou… | marketplace.hw9… | On **"Review & launch"**, check **You… | All reflect the choices made in TC-07/08/09… | ◑ | — |
| TC-10 Review, checkou… | /review | Tap Checkout | Advances to **"Checkout"** | ✅ | [img](../sessions/2026-06-04/evidence/hw96-walk-tc10-checkout-pin-sent.png) |
| TC-10 Review, checkou… | marketplace.hw9… | Sign in: type the customer email, req… | PIN accepted; the voucher **credit is appli… | ⛔ | [img](../sessions/2026-06-04/evidence/hw96-walk-tc10-checkout-pin-sent.png) |
| TC-10 Review, checkou… | Checkout | Confirm | **"Setting up your tenant"** progress, then… | ⛔ | — |
| TC-11 The Organizatio… | "Your tenant is… | Follow the tenant link (or auto-redir… | Lands on `https://console.<orgslug>.<pool-t… | ⛔ | — |
| TC-11 The Organizatio… | Tenant console | PIN-login as the customer | **Dashboard renders** (Phase 2a) — not an e… | ⛔ | — |
| TC-11 The Organizatio… | Tenant apps view | Look at the app cards | The apps chosen in TC-07 appear as cards wi… | ⛔ | — |
| TC-11 The Organizatio… | A green app card | Tap **Open** | The app itself opens, **already signed in**… | ⛔ | — |
| TC-12 Operator sees t… | console.hw96.om… | Open **BSS → Tenants** | The new Organization appears with its chose… | ⛔ | — |
| TC-12 Operator sees t… | console.hw96.om… | Find the issued voucher row, expand it | Drawer shows **Redemptions** incremented ex… | ◑ | — |
| TC-13 Multi-region da… | console.hw96.om… | Set Layer-1 = Cluster | **One bubble per region** (2 on hw96) — not… | ◑ | — |
| TC-13 Multi-region da… | /dashboard | Set Layer-2 = Namespace | Namespace bubbles render **within** each cl… | ☐ | — |
| TC-13 Multi-region da… | console.hw96.om… | Read the kind chips | **All regions** present, no stuck spinners … | ✅ | [img](../sessions/2026-06-04/evidence/hw96-walk-tc13-cloud-2region-topology.png) |
| TC-13 Multi-region da… | /cloud, any res… | Click a leaf cell | Drill-down opens the resource detail — clic… | ☐ | — |
| TC-14 Jobs are termin… | [console.hw91/…](https://console.hw91.omantel.biz/jobs) | Open **Jobs** | **0 pending, 0 running** — every job in a t… | ☐ | — |
| TC-14 Jobs are termin… | /jobs | Read job rows | Per-region prefixes visible on a multi-regi… | ☐ | — |
| TC-15 Catalog: class … | console.hw96.om… | Open **Applications**, switch to the … | Blueprint **class** cards (one per Blueprin… | ✅ | [img](../sessions/2026-06-05/evidence/hw96-walk-tc15-applications-deployments.png) |
| TC-15 Catalog: class … | A class card (e… | Click it | **Catalog detail**: Blueprint header + **su… | ✅ | [img](../sessions/2026-06-05/evidence/hw96-walk-tc17-grafana-launch-sso-button.png) |
| TC-16 Three coexistin… | Catalog detail … | Tap **+ New instance**, complete the … | Each install accepted — no name-collision c… | ☐ | — |
| TC-16 Three coexistin… | Catalog detail … | Read the instance table | **3 rows**, each with its own name + status | ☐ | — |
| TC-16 Three coexistin… | Each instance r… | Open each instance's detail → its end… | **3 distinct URLs**, each serving its own G… | ☐ | — |
| TC-17 Launch silent-S… | App detail — **… | Tap **Launch →** | New tab opens **already signed in** (silent… | ❌ | [img](../sessions/2026-06-05/evidence/hw96-walk-tc17-grafana-launch-sso-button.png) [img](../sessions/2026-06-05/evidence/hw96-walk-tc17-grafana-FAIL-redirect-uri-localhost.png) |
| TC-17 Launch silent-S… | App detail — **… | Tap **Launch →** | Same: signed-in Gitea, no form | ☐ | — |
| TC-17 Launch silent-S… | App detail — **… | Tap **Launch →** | Same: signed-in Harbor, no form | ☐ | — |
| TC-17 Launch silent-S… | App detail — **… | Tap **Launch →** | OpenBao opens authenticated via OIDC (archi… | ☐ | — |
| TC-18 Endpoint edit →… | App detail → Co… | Rename an endpoint (e.g. `grafana` → … | UI confirms the change was submitted **as a… | ☐ | — |
| TC-18 Endpoint edit →… | Gitea (via TC-1… | Open the Org's `iac` repo → Pull requ… | The PR exists with **3 named checks**: `kyv… | ☐ | — |
| TC-18 Endpoint edit →… | The PR | Watch checks complete | All 3 green → PR **auto-merges** | ☐ | — |
| TC-18 Endpoint edit →… | Browser, ≤ 2 mi… | Open `https://metrics.<…>` (the NEW n… | New FQDN serves with **valid TLS** and **si… | ☐ | — |
| TC-19 RBAC surfaces | [console.hw91/…](https://console.hw91.omantel.biz/rbac/roles) | Open **Keycloak Roles** | Real role catalog renders (not empty/error) | ☐ | — |
| TC-19 RBAC surfaces | [console.hw91/…](https://console.hw91.omantel.biz/rbac/matrix) | Open **Access matrix** | Subjects × roles grid with the owner-tier o… | ☐ | — |
| TC-19 RBAC surfaces | [console.hw91/…](https://console.hw91.omantel.biz/users/new) | Create a second operator user (Name, … | Success toast → user appears in **User Acce… | ☐ | — |
| TC-21 Cross-Org realm… | Browser profile… | Sign in to Org-A's console | Org-A dashboard | ☐ | — |
| TC-21 Cross-Org realm… | Same profile A | Open Org-B's console URL | **A fresh sign-in is demanded** — Org-A's s… | ☐ | — |
| TC-21 Cross-Org realm… | Profile A, Org-… | Open both app URLs | Org-A's opens signed-in; Org-B's demands si… | ☐ | — |
| TC-22 Install a CNPG-… | Tenant marketpl… | Pick a CNPG-backed app (e.g. WordPres… | Install starts; app card progresses to green | ☐ | — |
| TC-22 Install a CNPG-… | App detail → To… | Read the placement | The app shows under **both regions** — prim… | ☐ | — |
| TC-22 Install a CNPG-… | The app's FQDN | Open it | App serves with trusted TLS | ☐ | — |
| TC-23 Region-kill: th… | The app's FQDN | Confirm it serves, note the time | HTTP 200, content loads | ☐ | — |
| TC-23 Region-kill: th… | (Dev team kills… | Keep refreshing the app URL | Service resumes within **≤ 30 s** — same FQ… | ☐ | — |
| TC-23 Region-kill: th… | Operator /dashb… | Read the region bubbles | Failed region shows unhealthy; surviving re… | ☐ | — |
| TC-23 Region-kill: th… | The app | Log in / use it | Data written before the kill is **all prese… | ☐ | — |
| TC-24 Trigger the cut… | The Sovereignty… | Read the card | **"Sovereignty status"** with badge **"Soft… | ☐ | — |
| TC-24 Trigger the cut… | The card | Tap **Achieve True Sovereignty** | An explanation modal opens first — the cuto… | ☐ | — |
| TC-24 Trigger the cut… | The modal | Confirm | Progress card appears; steps advance (e.g. … | ☐ | — |
| TC-25 Cutover complet… | Progress card | Wait through the final step | **"Egress test"** runs the 10-minute deny-e… | ☐ | — |
| TC-25 Cutover complet… | Sovereignty card | Read the badge | Flips to **"Independent"** — `cutoverComple… | ☐ | — |
| TC-26 Post-cutover re… | Fresh browser | PIN-login at console.hw91.omantel.biz | Works exactly as in TC-02 — the PIN/JWT iss… | ☐ | — |
| TC-26 Post-cutover re… | App detail — Gr… | Tap **Launch →** | Silent SSO still works — no login form, no … | ☐ | — |
| TC-26 Post-cutover re… | Tenant console | Customer PIN-login + open an app | Tenant flows unaffected | ☐ | — |
| TC-26 Post-cutover re… | Marketplace | Open /redeem with a fresh voucher | Voucher flow still works end-to-end post-cu… | ☐ | — |


