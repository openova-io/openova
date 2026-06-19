# OpenOva Catalyst — UAT Master Walkthrough (single source of truth)

## How to read this
- Every row is a **web-UI-walkable** test case. The **Walk link** is a console URL the end user clicks to reproduce the result in the browser. **Evidence** is a UI screenshot.
- **Result** is one of: ✅ walked-pass · ❌ walked-fail · ☐ not-walked. Nothing else (no GAP, no "by-design" in this column).
- **One COMPLETE fresh walk per environment.** Any change to the environment voids ALL results and requires a full re-walk. Results never carry between environments.
- Backend-only checks (kubectl / HelmRelease / wire-level) are NOT test cases here — they are listed in the final "Excluded" appendix for traceability only.
- `<fqdn>` in every Walk link is the live Sovereign FQDN (e.g. `hw167.omantel.biz`), stamped at walk time. Per-Org (tenant) surfaces use the Org's own pool host `<orgslug>.omani.homes` (or `.omani.rest`/`.omani.trade`).

## Environment under test
`<fqdn>`  — (stamped at walk time; results below are ☐ until a complete fresh walk fills them)

## Summary
| Journey | Ticket | Test cases | ✅ | ❌ | ☐ |
|---|---|---:|---:|---:|---:|
| Object model | #3687 | 25 | 0 | 0 | 25 |
| SSO — zero-login everywhere, admin by default | #3374 | 20 | 0 | 0 | 20 |
| Topology / DR — one vocabulary + region-kill | #3375 | 26 | 0 | 0 | 26 |
| Funnel — voucher → running app | #3376 | 24 | 0 | 0 | 24 |
| Placement — 7 host apps into the mgmt vCluster (NS#1) | #3642 | 20 | 0 | 0 | 20 |
| Organizations — eradicate SME/tenant naming | #3383 | 7 | 0 | 0 | 7 |
| Catalog — single-source IaC edit (not overlay) | #3668 | 36 | 0 | 0 | 36 |
| Cutover — durable true deny-egress + faithful pivot (Pillar 5) | #3379 | 8 | 0 | 0 | 8 |
| Jobs — one honest canvas with remediation | #3646 | 11 | 0 | 0 | 11 |
| Regenerate-on-current-env (meta discipline) | #3581 | 9 | 0 | 0 | 9 |
| **TOTAL** | | **186** | **0** | **0** | **186** |

## 1. Object model — #3687
| ID | Test case (what the user sees) | Walk link | Result | Evidence |
|---|---|---|---|---|
| 3687-01 | Console bare URL → lands `/dashboard` signed-in as `emrah.baysal@openova.io` (no PIN/login form). | `https://console.<fqdn>/dashboard` | ☐ | |
| 3687-02 | Full sidebar renders (Dashboard / Cloud / Apps / Catalog / Sandbox / Jobs / Compliance / Users / Organizations / Settings). | `https://console.<fqdn>/dashboard` | ☐ | |
| 3687-03 | The voucher-redeem URL on an authed owner session server-redirects to `/dashboard` (no redeem form for the owner). | `https://console.<fqdn>/dashboard` | ☐ | |
| 3687-04 | App page tab strip generality: open ≥2 archetypes (a DB `shared-pg`, a consumer `harbor`) → identical tab strip; consumer Dependencies shows `Depends on: shared-pg / db:<ctx>`. | `https://console.<fqdn>/app/harbor` | ☐ | |
| 3687-05 | Organizations directory: the customer Org row shows KIND=customer, TIER=sme, BILLING=real, ISOLATION=vcluster, STATUS=active. | `https://console.<fqdn>/organizations` | ☐ | |
| 3687-09 | The customer Org is present as a real Organization in the operator directory (same record dashboard + showback read). | `https://console.<fqdn>/organizations` | ☐ | |
| 3687-10 | Org detail renders canonical fields: slug `acme` (NOT `sme-<uuid>`), kind customer, tier sme, billing real, isolation vcluster, owner, console URL. | `https://console.<fqdn>/organizations/acme` | ☐ | |
| 3687-11 | The Create-organization form renders fully (kind picker, slug, Company name, Admin email, parent-domain). | `https://console.<fqdn>/organizations/new` | ☐ | |
| 3687-12 | Org detail Status = active, backed by a real `vcluster` isolation (the create→Provision loop produced a live backing, not a fake-green). | `https://console.<fqdn>/organizations/acme` | ☐ | |
| 3687-15 | The customer Org is present + ACTIVE backed by a real `vcluster` isolation (Lane B convergence read). | `https://console.<fqdn>/organizations` | ☐ | |
| 3687-16 | Org detail Status = active and the directory + detail consistently show `vcluster` isolation. | `https://console.<fqdn>/organizations/acme` | ☐ | |
| 3687-17 | On re-load the Org detail consistently reports active + `vcluster` (stable, no false flicker). | `https://console.<fqdn>/organizations/acme` | ☐ | |
| 3687-20 | Catalog grid renders the Blueprint cards; `bp-postgres` detail has a **+ New instance** button. | `https://console.<fqdn>/catalog/bp-postgres` | ☐ | |
| 3687-21 | The shared-PG reuse model is LIVE: the `shared-pg` app **Contexts** tab shows multiple consumers sharing ONE PostgreSQL. | `https://console.<fqdn>/app/shared-pg` | ☐ | |
| 3687-22 | Apps list renders one card per Application (all Platform/BOOTSTRAP-owned); a customer-launched app card would appear under its Org. | `https://console.<fqdn>/apps` | ☐ | |
| 3687-23 | A customer app page (`/app/<name>`) → Settings/Topology → change topology → Save persists; the canonical tab strip includes a Topology tab. | `https://console.<fqdn>/app/blog` | ☐ | |
| 3687-26 | Dashboard treemap Layer-1 default = **Organization**, drillable to vCluster → Application (not a raw infra-pod utilisation treemap). | `https://console.<fqdn>/dashboard` | ☐ | |
| 3687-27 | Scan every treemap cell: NO ephemeral Job-pod cell appears (no `cutover-*`, `scan-vulnerabilityreport-*`, `*-snapshot-save-*`). | `https://console.<fqdn>/dashboard` | ☐ | |
| 3687-28 | Count Application cards: one card per `Application` (NOT one per HelmRelease/pod); bootstrap apps carry a Platform/Bootstrap badge. | `https://console.<fqdn>/apps` | ☐ | |
| 3687-29 | With a customer Org present, the customer estate is visually distinct from platform pods on the treemap. | `https://console.<fqdn>/dashboard` | ☐ | |
| 3687-30 | Organizations Showback panel: the **Application** column for a selected Org lists only that Org's real apps (no cluster-wide infra/Job pods). | `https://console.<fqdn>/organizations` | ☐ | |
| 3687-31 | Showback shows a single visually-distinct **Platform overhead** roll-up line holding all control-plane/Job workloads. | `https://console.<fqdn>/organizations` | ☐ | |
| 3687-32 | After a 2nd Org runs an app, the showback panel shows a SECOND Org row attributed only to its own app, distinct from Platform overhead. | `https://console.<fqdn>/organizations` | ☐ | |
| 3687-34 | `shared-pg` renders the canonical tab strip Overview · Contexts · Topology · Dependencies (Contexts tab present only for shareable blueprints). | `https://console.<fqdn>/app/shared-pg` | ☐ | |
| 3687-35 | One consistent model across surfaces: `/organizations` shows the Orgs that `/apps`, the dashboard and showback all agree on. | `https://console.<fqdn>/organizations` | ☐ | |

## 2. SSO — zero-login everywhere, admin by default — #3374
| ID | Test case (what the user sees) | Walk link | Result | Evidence |
|---|---|---|---|---|
| 3374-01 | Console bare URL → lands on the dashboard signed-in as the owner; no PIN/login/"Sign in with…" button. | `https://console.<fqdn>/dashboard` | ☐ | |
| 3374-02 | Avatar (top-right) menu reads "Signed in as emrah.baysal@openova.io" with a Sign-out item. | `https://console.<fqdn>/dashboard` | ☐ | |
| 3374-03 | Users page renders the pre-seeded owner row `emrah.baysal@openova.io` (tier=owner UserAccess CR), signed-in admin. | `https://console.<fqdn>/users` | ☐ | |
| 3374-04 | Re-open the bare console URL after the session TTL → lands signed-in again, no PIN re-prompt. | `https://console.<fqdn>/dashboard` | ☐ | |
| 3374-05 | Grafana bare URL → lands on Grafana Home, full UI, no login form; left nav shows Administration; user menu = `emrah.baysal@openova.io`. | `https://grafana.<fqdn>` | ☐ | |
| 3374-06 | Gitea bare URL → lands on the gitea dashboard titled "emrah.baysal — Dashboard", no login; profile menu exposes Site Administration; stays on :443. | `https://gitea.<fqdn>` | ☐ | |
| 3374-07 | Harbor bare URL → lands on `/harbor/projects`, no login form; user dropdown `emrah.baysal@openova.io` with Administration menus. | `https://harbor.<fqdn>` | ☐ | |
| 3374-08 | OpenBao bare UI → lands in an authenticated Vault session (Secrets engines / dashboard), NO token-entry form (an in-transit "Signing in…" shim is allowed). | `https://openbao.<fqdn>` | ☐ | |
| 3374-09 | Keycloak admin console for the **sovereign** realm → lands inside the admin console (realm overview / Users / Clients), no master-realm login. | `https://auth.<fqdn>` | ☐ | |
| 3374-10 | Guacamole bare URL → lands on the Guacamole connections list, signed-in; no Tomcat 404, no `/guacamole/` login page. | `https://guacamole.<fqdn>` | ☐ | |
| 3374-11 | PowerDNS-Admin bare URL → lands on the dashboard signed-in; no redirect loop, no OAuth error, no `Log In` page. | `https://pdns-admin.<fqdn>` | ☐ | |
| 3374-12 | newapi bare URL (1st hit) → lands on `/console` signed-in as admin (role 100); no "Unknown OAuth provider", no login page. | `https://newapi.<fqdn>` | ☐ | |
| 3374-13 | newapi bare URL (2nd hit, re-entry) → lands on `/console` again signed-in; NOT an "already bound" / re-link error / `/setup` wizard. | `https://newapi.<fqdn>` | ☐ | |
| 3374-15 | Hubble bare URL → lands on the Hubble UI, authenticated (not an anonymous/unauth view, no login page). | `https://hubble.<fqdn>` | ☐ | |
| 3374-16 | Marketplace bare URL → renders the anonymous storefront (public, by design); confirm no spurious login UI is forced. | `https://marketplace.<fqdn>` | ☐ | |
| 3374-17 | Keycloak sovereign realm → Users lists the single owner principal `emrah.baysal@openova.io` (enabled). | `https://auth.<fqdn>/admin/master/console/#/sovereign/users` | ☐ | |
| 3374-18 | Owner user → Groups tab shows membership in `/sovereign-admins` (alongside `/openova-users`) — the source of admin authority. | `https://auth.<fqdn>/admin/master/console/#/sovereign/users` | ☐ | |
| 3374-19 | Owner user → Role mapping tab: effective realm roles include `catalyst-admin` (not only default-roles/uma/offline). | `https://auth.<fqdn>/admin/master/console/#/sovereign/users` | ☐ | |
| 3374-20 | Groups → `/sovereign-admins` → Role mapping: group confers `catalyst-admin` (console) and realm-management `realm-admin` (KC console) — one source, both grants. | `https://auth.<fqdn>/admin/master/console/#/sovereign/groups` | ☐ | |
| 3374-21 | Console Users panel: owner row + the ability to view/manage users renders — proving console admin nav is driven by the realm principal, not a self-signed constant. | `https://console.<fqdn>/users` | ☐ | |

## 3. Topology / DR — one vocabulary + region-kill — #3375
| ID | Test case (what the user sees) | Walk link | Result | Evidence |
|---|---|---|---|---|
| 3375-01 | `bp-postgres` catalog detail renders; click **New instance** → the create dialog opens with a topology `<select>`. | `https://console.<fqdn>/catalog/bp-postgres` | ☐ | |
| 3375-02 | Topology `<select>` options read exactly the ONE canonical vocabulary: `singleton`, `active-passive`, `active-hot-standby`, `active-active` (no `single-region`/`active-hotstandby`). | `https://console.<fqdn>/catalog/bp-postgres` | ☐ | |
| 3375-03 | `active-passive` is a selectable option in the create `<select>` (not folded away). | `https://console.<fqdn>/catalog/bp-postgres` | ☐ | |
| 3375-04 | `singleton` is a separate selectable option (single-region single-instance), distinct from the multi-region modes. | `https://console.<fqdn>/catalog/bp-postgres` | ☐ | |
| 3375-05 | Pick `active-hot-standby`, name the instance, Provision → the create succeeds (toast/redirect to the new app card), NOT a red `topology not in supported [...]` error. | `https://console.<fqdn>/catalog/bp-postgres` | ☐ | |
| 3375-07 | `shared-pg` Topology tab renders a per-region placement view listing region-a (active) and region-b (standby) as ONE placement, not two separate instances. | `https://console.<fqdn>/app/shared-pg` | ☐ | |
| 3375-08 | `shared-pg` Topology tab: the standby (region-b) copy is shown scaled-down / passive while region-a shows the active replica count (not identical hot counts). | `https://console.<fqdn>/app/shared-pg` | ☐ | |
| 3375-09 | Open a per-app Topology tab and read its placement view (singleton apps note no per-region/standby surface). | `https://console.<fqdn>/app/cilium` | ☐ | |
| 3375-10 | `shared-pg` Topology tab declared-topology strip renders the canonical mode in ONE vocabulary — header dialect and picker dialect MATCH (no `singleton` header over an `active-hotstandby` chip). | `https://console.<fqdn>/app/shared-pg` | ☐ | |
| 3375-11 | `shared-pg` Topology tab shows the effective (live) topology next to the declared one — declared vs effective together, not a build-time constant. | `https://console.<fqdn>/app/shared-pg` | ☐ | |
| 3375-12 | `shared-pg` Topology tab per-region placement + replication block: region-a primary, region-b replica, and a live replication-lag in seconds (not a hardcoded `—`). | `https://console.<fqdn>/app/shared-pg` | ☐ | |
| 3375-13 | `shared-pg` Topology tab Switchover button is present and armed because a live 2-region cnpg-pair backs the app. | `https://console.<fqdn>/app/shared-pg` | ☐ | |
| 3375-14 | A **singleton** app (cilium) Topology tab: the DR section / Switchover button does NOT render (honestly hidden, not armed against a phantom region). | `https://console.<fqdn>/app/cilium` | ☐ | |
| 3375-15 | Catalog New instance → pick `singleton` → Provision → that app's Topology tab shows single-region placement (no region-b standby, no Switchover). | `https://console.<fqdn>/catalog/bp-postgres` | ☐ | |
| 3375-16 | Catalog New instance → pick `active-hot-standby` → Provision → that app's Topology tab shows a 2-region pair (region-a primary + region-b replica + armed Switchover). | `https://console.<fqdn>/catalog/bp-postgres` | ☐ | |
| 3375-17 | Apps grid shows newly-provisioned postgres instances as their own cards, each carrying a topology badge matching the mode picked at create time. | `https://console.<fqdn>/apps` | ☐ | |
| 3375-18 | On an app WITH a live pair, the Topology DR section shows the live Continuum status (Ready / lease holder / standby) from the live API, not a static badge. | `https://console.<fqdn>/app/shared-pg` | ☐ | |
| 3375-19 | grafana (declares hot-standby, no live DR backing) Topology DR section reads the honest "no live DR backing… Switchover unavailable" state with a disabled (not armed) Switchover button. | `https://console.<fqdn>/app/grafana` | ☐ | |
| 3375-20 | `shared-pg` Topology tab replication-lag field shows a live numeric seconds value (or explicit "no replica"), never a hardcoded `—`. | `https://console.<fqdn>/app/shared-pg` | ☐ | |
| 3375-21 | Cloud/regions view shows the true region count — a healthy 2-region prov reads `Cluster 2/2` with no phantom region-B bubble. | `https://console.<fqdn>/cloud` | ☐ | |
| 3375-24 | Cloud→Clusters renders 2/2 HEALTHY clusters, one per region (me-east-215-a + me-east-215-b), no phantom region. | `https://console.<fqdn>/cloud` | ☐ | |
| 3375-25 | grafana status/overview reports Healthy/Running in both regions — no "cannot resolve write host" crashloop in the app health panel. | `https://console.<fqdn>/app/grafana` | ☐ | |
| 3375-26 | powerdns-admin status reports Healthy/Running — the CNPG-minted DB host resolved (no "could not translate host"). | `https://console.<fqdn>/app/powerdns-admin` | ☐ | |
| 3375-27 | keycloak status reports Healthy/Running in both regions — JGroups DB-host resolves, no UnknownHostException in the health panel. | `https://console.<fqdn>/app/keycloak` | ☐ | |
| 3375-28 | guacamole status reports Healthy/Running in both regions — no missing-recordings-PVC error surfaced. | `https://console.<fqdn>/app/guacamole` | ☐ | |
| 3375-29 | Region-kill baseline (before): `shared-pg` Topology tab shows live Continuum Ready, lease held by region-a, region-b standby present, a live replication-lag number. | `https://console.<fqdn>/app/shared-pg` | ☐ | |

## 4. Funnel — voucher → running app — #3376
| ID | Test case (what the user sees) | Walk link | Result | Evidence |
|---|---|---|---|---|
| 3376-01 | Operator console BSS → Vouchers page renders the voucher issuance form (code, credit OMR, plan tier, description); no login wall. | `https://console.<fqdn>/bss/vouchers` | ☐ | |
| 3376-02 | In the voucher form, type a weak code `1234` → Issue → the form rejects it inline ("voucher code must be at least 12 characters"). | `https://console.<fqdn>/bss/vouchers` | ☐ | |
| 3376-03 | Leave the code field empty → Issue → a new voucher row appears with a server-auto-generated high-entropy code, credit, plan tier, `unredeemed 0/1`. | `https://console.<fqdn>/bss/vouchers` | ☐ | |
| 3376-04 | Stranger opens the redeem page with `?code=<CODE>` → sees "Voucher valid · 5000 OMR" with THIS Sovereign's brand chrome (no openova.io / mothership logo). | `https://marketplace.<fqdn>/redeem/?code=<CODE>` | ☐ | |
| 3376-05 | Open the redeem page with a junk code → a generic "voucher not valid" message (no tombstone / no detail leak). | `https://marketplace.<fqdn>/redeem/?code=JUNK` | ☐ | |
| 3376-06 | Redeem page source (DevTools): the HTML shows THIS Sovereign's brand and no `console.openova.io` / `omantel.openova.io` / bare `openova.io` host literal. | `https://marketplace.<fqdn>/redeem/?code=<CODE>` | ☐ | |
| 3376-07 | Click "Sign up to redeem" → the browser lands on the plan picker grid (`/plans`). | `https://marketplace.<fqdn>/plans` | ☐ | |
| 3376-08 | Plans grid shows the tiers (S/M/L/XL/Flexi) with price/CPU/memory → pick plan M → advances to the app catalog (`/apps`). | `https://marketplace.<fqdn>/plans` | ☐ | |
| 3376-09 | App catalog grid (served from THIS Sovereign's catalog) → pick WordPress → advances to add-ons (`/addons`). | `https://marketplace.<fqdn>/apps` | ☐ | |
| 3376-10 | Add-ons step shows optional add-ons → leave defaults → Continue → advances to the BCP topology step (`/bcp`). | `https://marketplace.<fqdn>/addons` | ☐ | |
| 3376-11 | BCP topology step shows BOTH Single-region and Active-hot-standby radios (the Pillar-2 BCP choice at signup) → select Active-hot-standby → Continue → review. | `https://marketplace.<fqdn>/bcp` | ☐ | |
| 3376-12 | Review summary shows the chosen plan (M), app (WordPress), topology (Active-hot-standby), the Org slug, and the pool-TLD → Proceed to checkout. | `https://marketplace.<fqdn>/review` | ☐ | |
| 3376-13 | On checkout, enter the stranger's email → Send code → type the emailed sign-in code (no password) → the page shows the stranger signed in + Org confirmed. | `https://marketplace.<fqdn>/checkout` | ☐ | |
| 3376-14 | Checkout summary: the voucher credit is applied — "Credit covers this order — 0 OMR due" (no Stripe card form) → click Launch / Place order. | `https://marketplace.<fqdn>/checkout` | ☐ | |
| 3376-15 | Provisioning progress timeline advances to Done (Creating Org → Committing manifests → Provisioning vCluster → Deploying WordPress → TLS → Health), no hang/red step. | `https://marketplace.<fqdn>/checkout` | ☐ | |
| 3376-16 | After Launch, the marketplace redirects to the per-Org console — URL becomes `https://console.<slug>.omani.homes` and loads with publicly-trusted TLS (no cert error / connection-refused). | `https://console.<orgslug>.omani.homes/` | ☐ | |
| 3376-17 | Per-Org console landing: the stranger is signed in zero-click as the Org owner (their email in the avatar, their Org in the header) — no login/PIN form. | `https://console.<orgslug>.omani.homes/dashboard` | ☐ | |
| 3376-18 | Per-Org console → Applications view shows the purchased WordPress app card Running/Healthy → click Open. | `https://console.<orgslug>.omani.homes/applications` | ☐ | |
| 3376-19 | Terminal acceptance: the purchased WordPress app SERVES at its own FQDN `https://wordpress.<slug>.omani.homes` — the live rendered WordPress site (not 404/502/cert error). | `https://wordpress.<orgslug>.omani.homes/` | ☐ | |
| 3376-20 | While signed in, re-open the marketplace root → the returning-user redirect sends the customer to their own Org console, never to `console.openova.io` / the mothership. | `https://marketplace.<fqdn>/` | ☐ | |
| 3376-21 | As the signed-in customer, rapidly re-submit checkout/redeem >5× in a few seconds → after ~5 attempts a rate-limit notice appears; a single legitimate redeem is unaffected. | `https://marketplace.<fqdn>/checkout` | ☐ | |
| 3376-22 | Generality: mint a 2nd voucher and re-walk B.1→B.4 with a different slug (`walk-stranger-two`) and a different pool-TLD (`omani.rest`) → a 2nd Organization provisions and lands signed-in. | `https://marketplace.<fqdn>/redeem/?code=<CODE2>` | ☐ | |
| 3376-23 | The 2nd Org's console lands signed-in on a different TLD (`console.walk-stranger-two.omani.rest`) — identical zero-click contract, no special-casing. | `https://console.<orgslug2>.omani.rest/dashboard` | ☐ | |
| 3376-24 | The 2nd Org's purchased app serves at its own different-TLD FQDN (`wordpress.walk-stranger-two.omani.rest`) — two Orgs, two TLDs, two running apps, identical mechanism. | `https://wordpress.<orgslug2>.omani.rest/` | ☐ | |

## 5. Placement — 7 host apps into the mgmt vCluster (NS#1) — #3642
| ID | Test case (what the user sees) | Walk link | Result | Evidence |
|---|---|---|---|---|
| 3642-01 | Handover URL → lands on `/dashboard` already signed-in as `emrah.baysal@openova.io` (avatar E), no login form. | `https://console.<fqdn>/dashboard` | ☐ | |
| 3642-02 | Dashboard renders the cluster treemap and the LAYER 1 / LAYER 2 grouping comboboxes are visible. | `https://console.<fqdn>/dashboard` | ☐ | |
| 3642-03 | Click LAYER 1 combobox → select `vCluster`; the treemap regroups into one labelled block per vCluster (`host` / `mgmt` / `rtz` / `dmz`), mgmt block visible + clickable. | `https://console.<fqdn>/dashboard` | ☐ | |
| 3642-04 | On the LAYER1=vCluster treemap, the **grafana** tile sits inside the **mgmt** block, not **host**. | `https://console.<fqdn>/dashboard` | ☐ | |
| 3642-05 | On the LAYER1=vCluster treemap, the **harbor** tile sits inside the **mgmt** block, not **host**. | `https://console.<fqdn>/dashboard` | ☐ | |
| 3642-06 | On the LAYER1=vCluster treemap, the **keycloak** tile sits inside the **mgmt** block, not **host**. | `https://console.<fqdn>/dashboard` | ☐ | |
| 3642-07 | On the LAYER1=vCluster treemap, the **gitea** tile sits inside the **mgmt** block, not **host**. | `https://console.<fqdn>/dashboard` | ☐ | |
| 3642-08 | On the LAYER1=vCluster treemap, the **openbao** tile sits inside the **mgmt** block, not **host**. | `https://console.<fqdn>/dashboard` | ☐ | |
| 3642-09 | On the LAYER1=vCluster treemap, the **newapi** tile sits inside the **mgmt** block, not **host**. | `https://console.<fqdn>/dashboard` | ☐ | |
| 3642-10 | On the LAYER1=vCluster treemap, the **guacamole** tile sits inside the **mgmt** block, not **host**. | `https://console.<fqdn>/dashboard` | ☐ | |
| 3642-11 | Drill into the **mgmt** block: its tiles include all 7 named apps (grafana/harbor/keycloak/gitea/openbao/newapi/guacamole) alongside loki/mimir/nats/tempo. | `https://console.<fqdn>/dashboard` | ☐ | |
| 3642-12 | Read the **host** block on the same treemap: none of the 7 named apps appear under `host`. | `https://console.<fqdn>/dashboard` | ☐ | |
| 3642-13 | Open the keycloak app card → its placement detail reads `mgmt` (the per-app placement mirrors the treemap block), not `host`. | `https://console.<fqdn>/app/keycloak` | ☐ | |
| 3642-14 | The sovereign-realm account console renders for `emrah.baysal@openova.io` (no second login, no error dialog). | `https://auth.<fqdn>/realms/sovereign/account` | ☐ | |
| 3642-15 | Gitea opens already signed in (avatar/menu shows the SSO user), repo list renders — no Gitea login form. | `https://gitea.<fqdn>` | ☐ | |
| 3642-16 | Harbor opens signed in, the projects list renders — no Harbor login form, no gateway error page. | `https://harbor.<fqdn>` | ☐ | |
| 3642-17 | Grafana opens signed in (no Grafana login), the home dashboard renders. | `https://grafana.<fqdn>` | ☐ | |
| 3642-18 | The OpenBao UI renders signed in via OIDC — no manual token/unseal prompt blocking the landing. | `https://openbao.<fqdn>` | ☐ | |
| 3642-19 | newapi opens signed in, its main console renders — no login form, no upstream-connect error. | `https://newapi.<fqdn>` | ☐ | |
| 3642-20 | Guacamole opens signed in, the connections list renders — no Guacamole login form. | `https://guacamole.<fqdn>` | ☐ | |

## 6. Organizations — eradicate SME/tenant naming — #3383
| ID | Test case (what the user sees) | Walk link | Result | Evidence |
|---|---|---|---|---|
| 3383-01 | Organizations directory: the page title / heading reads "Organizations", never "Tenants"/"SME Tenants". | `https://console.<fqdn>/organizations` | ☐ | |
| 3383-02 | Directory org cards / list rows: column headers read "Organization / Kind / Tier / Billing / Isolation / Status"; rows labeled "Organization", never "tenant"/"SME". | `https://console.<fqdn>/organizations` | ☐ | |
| 3383-03 | Left-nav sidebar label for this section reads "Organizations", not "Tenants"/"SME". | `https://console.<fqdn>/organizations` | ☐ | |
| 3383-04 | Create-organization flow: the form title, field labels, and submit button all say "Organization" (no "SME tenant slug" / "Onboard tenant" persona words). | `https://console.<fqdn>/organizations/new` | ☐ | |
| 3383-05 | Organization-detail view: heading "Acme Corp", breadcrumb "← Organizations", field labels Slug/Kind/Tier/Billing mode/Isolation/Status/Owner/Console — no "tenant"/"SME". | `https://console.<fqdn>/organizations/acme` | ☐ | |
| 3383-06 | BSS / billing screen: billing is framed as "This organization is in showback mode…", zero "tenant"/"SME" leaks. | `https://console.<fqdn>/organizations/billing` | ☐ | |
| 3383-07 | Legacy `/bss/tenants` URL (PR #3390 alias) resolves and redirects to `/organizations`, rendering the directory (H1 "Organizations") — not a 404, not a login redirect. | `https://console.<fqdn>/bss/tenants` | ☐ | |

## 7. Catalog — single-source IaC edit (not overlay) — #3668
| ID | Test case (what the user sees) | Walk link | Result | Evidence |
|---|---|---|---|---|
| 3668-01 | Handover URL → lands on `/dashboard` already signed-in as `emrah.baysal@openova.io` (avatar E), no login form (everything below is admin-gated). | `https://console.<fqdn>/dashboard` | ☐ | |
| 3668-02 | Catalog grid renders Blueprint cards in a tile grid, each with an icon + summary; the Alloy card is visible. | `https://console.<fqdn>/catalog` | ☐ | |
| 3668-03 | Click the Alloy card → the detail page renders: a hero (icon + name + summary + Edit IaC), an About section, and an Instances list; no login redirect. | `https://console.<fqdn>/catalog/bp-alloy` | ☐ | |
| 3668-04 | Click the admin Edit affordance in the hero → an edit form drops INLINE into the detail page (no modal overlay); fields appear in-place under the hero. | `https://console.<fqdn>/catalog/bp-alloy` | ☐ | |
| 3668-05 | In the inline form, change Summary to `RECONCILE-PROOF-<ts>` → Save → the page refreshes in place and the new summary shows in the hero. | `https://console.<fqdn>/catalog/bp-alloy` | ☐ | |
| 3668-06 | Back on the grid, the Alloy card summary now reads `RECONCILE-PROOF-<ts>` — the edit propagated to the card, not just the detail page. | `https://console.<fqdn>/catalog` | ☐ | |
| 3668-07 | Hard-reload the detail page → the summary is still `RECONCILE-PROOF-<ts>` — the edit persisted across a reload, not an in-memory overlay. | `https://console.<fqdn>/catalog/bp-alloy` | ☐ | |
| 3668-08 | The non-card `version` field renders as a `v1.0.1` chip in the hero, and Edit IaC exposes the full CR including `version` for editing. | `https://console.<fqdn>/catalog/bp-alloy` | ☐ | |
| 3668-10 | Note the current hero logo (the Alloy glyph) as the baseline before an icon edit. | `https://console.<fqdn>/catalog/bp-alloy` | ☐ | |
| 3668-11 | Click Edit → in the Light-theme icon field paste a distinct image → Save → a "Saved to IaC ✓" confirmation shows and the page refreshes. | `https://console.<fqdn>/catalog/bp-alloy` | ☐ | |
| 3668-12 | Observe the hero — it now shows the new logo; the render reads the edited `card.iconLight` first (IaC-first), not the bundled asset. | `https://console.<fqdn>/catalog/bp-alloy` | ☐ | |
| 3668-13 | Return to the grid — the Alloy card icon is now the new logo (the grid tile resolves the same edited `card.iconLight`). | `https://console.<fqdn>/catalog` | ☐ | |
| 3668-14 | Reload the detail page — the hero is still the new logo (render reads the persisted IaC icon on every load). | `https://console.<fqdn>/catalog/bp-alloy` | ☐ | |
| 3668-15 | Click Edit again — the Light-theme icon field shows the current IaC value, falling back to the bundled asset only when IaC carries none. | `https://console.<fqdn>/catalog/bp-alloy` | ☐ | |
| 3668-16 | Click Edit → click the icon picker (`iconpicker-*`) → a thumbnail grid of vendored `component-logos/*` assets opens (a role=listbox grid of logo tiles). | `https://console.<fqdn>/catalog/bp-alloy` | ☐ | |
| 3668-17 | Click `cilium.svg` in the picker grid → the icon field + a live preview swatch update to the Cilium logo. | `https://console.<fqdn>/catalog/bp-alloy` | ☐ | |
| 3668-18 | Save → reload — the hero is now the Cilium logo (the picker selection persisted to IaC and renders on hero + grid card). | `https://console.<fqdn>/catalog/bp-alloy` | ☐ | |
| 3668-19 | On Save, the durable-commit verdict (git outcome) is surfaced in-UI (the Edit-IaC `managed-by: manual • in sync` indicator + `{stored:true,committed:true}`), not a silent store success. | `https://console.<fqdn>/catalog/bp-alloy` | ☐ | |
| 3668-21 | Hover the summary line in the hero → a pencil/edit affordance appears ON the field (`cif-summary-edit` → `cif-summary-input`), inline, without opening the full form. | `https://console.<fqdn>/catalog/bp-alloy` | ☐ | |
| 3668-22 | Click the summary → type a value → Save → only the summary updates in place; no full-form modal opens. | `https://console.<fqdn>/catalog/bp-alloy` | ☐ | |
| 3668-23 | Repeat the inline edit for the name field (`cif-name-edit` → `cif-name-input`) — it edits in place and saves just that field. | `https://console.<fqdn>/catalog/bp-alloy` | ☐ | |
| 3668-24 | Click Edit IaC (`catalog-detail-edit-iac`, admin only) → the full `blueprint.yaml` opens in the YAML editor (the entire CR, not just 7 card fields). | `https://console.<fqdn>/catalog/bp-alloy` | ☐ | |
| 3668-25 | Change a field in the editor → Commit → a Show-diff Current/Proposed side-by-side renders the change and the commit succeeds (confirmation appears). | `https://console.<fqdn>/catalog/bp-alloy` | ☐ | |
| 3668-26 | The editor subtitle states it directly: "Commit writes the IaC source of truth; Flux reconciles it… Both this editor and the card form above write the same file." | `https://console.<fqdn>/catalog/bp-alloy` | ☐ | |
| 3668-27 | Open the WordPress detail page → Edit → change Summary → Save → reload → the summary persists, exactly as for Alloy. | `https://console.<fqdn>/catalog/bp-wordpress` | ☐ | |
| 3668-28 | WordPress: Edit IaC → edit `spec.manifests` → Commit → reload — the same YamlEditor edits a structurally-different blueprint's manifests in place. | `https://console.<fqdn>/catalog/bp-wordpress` | ☐ | |
| 3668-29 | WordPress: Edit → Light-theme icon → distinct image → Save → reload — the WordPress hero icon visibly changes. | `https://console.<fqdn>/catalog/bp-wordpress` | ☐ | |
| 3668-30 | PostgreSQL detail renders the SAME edit surface (hero icon, Edit IaC, clickable cards) and Edit IaC exposes `contextSchema` (a blueprint carrying `contextSchema`/`shareable`). | `https://console.<fqdn>/catalog/bp-postgres` | ☐ | |
| 3668-31 | Alloy + Postgres render the IDENTICAL edit chrome (same `cif-icon-edit`/`cif-name-edit`, same Edit-IaC YamlEditor) — no blueprint-specific UI. | `https://console.<fqdn>/catalog/bp-postgres` | ☐ | |
| 3668-32 | The catalog detail page renders (hero · About · Instances) and opens an INLINE Edit form (no modal) — acceptance headline 1. | `https://console.<fqdn>/catalog/bp-alloy` | ☐ | |
| 3668-33 | A summary edit Saves, updates the page AND the grid card, and persists across a reload — acceptance headline 2. | `https://console.<fqdn>/catalog/bp-alloy` | ☐ | |
| 3668-34 | A non-card field edit (`version`) persists — the whole CR is editable, not a 7-field overlay — acceptance headline 3. | `https://console.<fqdn>/catalog/bp-alloy` | ☐ | |
| 3668-35 | The edited icon renders on hero + grid + survives reload; the form pre-fills the IaC icon; the picker grid works — acceptance headline 4. | `https://console.<fqdn>/catalog/bp-alloy` | ☐ | |
| 3668-36 | Save surfaces the IaC-commit verdict (`committed:true` + `• in sync`), not a bare store success — acceptance headline 5. | `https://console.<fqdn>/catalog/bp-alloy` | ☐ | |
| 3668-37 | Per-field inline edit for cards (`cif-*`) + the full-CR YamlEditor (Edit IaC) for the rest, both writing the same IaC source — acceptance headline 6. | `https://console.<fqdn>/catalog/bp-alloy` | ☐ | |
| 3668-38 | The identical edit mechanism works on a 2nd + 3rd blueprint — no per-blueprint UI (Alloy + Postgres + a third) — acceptance headline 7. | `https://console.<fqdn>/catalog/bp-postgres` | ☐ | |

## 8. Cutover — durable true deny-egress + faithful pivot (Pillar 5) — #3379
| ID | Test case (what the user sees) | Walk link | Result | Evidence |
|---|---|---|---|---|
| 3379-01 | Settings → Sovereignty section renders a "Cluster sovereignty" panel with a "TETHERED" badge + an "Achieve True Sovereignty" cutover CTA (runs the 8-step cutover + 10-min egress-block self-test). | `https://console.<fqdn>/settings#sovereignty` | ☐ | |
| 3379-02 | Console nav + Settings sidebar expose a dedicated "Sovereignty" anchor (`#sovereignty`) that scrolls to + highlights the Cluster-sovereignty panel — the cutover trigger is a first-class surface. | `https://console.<fqdn>/settings#sovereignty` | ☐ | |
| 3379-05 | Open `/jobs` (zero-login, signed in as owner) → the canvas table renders a populated activity list (not a spinner, empty state, or login redirect). | `https://console.<fqdn>/jobs` | ☐ | |
| 3379-06 | Find the `cutover` group row and expand it → it renders the 11 `cutover-step-*` rows (gitea-mirror, harbor-projects, harbor-prewarm, registry-pivot, … vcluster-registry-pivot) — the 11-step execution tree. | `https://console.<fqdn>/jobs` | ☐ | |
| 3379-07 | Each `cutover-step-*` row reads an honest per-step status (Succeeded / Running / Failed / Pending), never a premature green. | `https://console.<fqdn>/jobs` | ☐ | |
| 3379-08 | The cutover group status reflects its real children (a group with a failed child reads failed, not a fake Succeeded). | `https://console.<fqdn>/jobs` | ☐ | |
| 3379-09 | On a failed `cutover-step-*` row, a Re-run button is present (per-row, gated to Failed) — the operator can re-drive a failed cutover step from the browser. | `https://console.<fqdn>/jobs` | ☐ | |
| 3379-10 | (After a COMPLETE cutover) the `cutover` group reads all-11-green on `/jobs` — every step Succeeded, including `egress-block-test` and `registry-pivot`. | `https://console.<fqdn>/jobs` | ☐ | |

## 9. Jobs — one honest canvas with remediation — #3646
| ID | Test case (what the user sees) | Walk link | Result | Evidence |
|---|---|---|---|---|
| 3646-01 | Open the console root in a fresh tab → land on the operator dashboard signed in as the sovereign-admin (no login form, no password field). | `https://console.<fqdn>/dashboard` | ☐ | |
| 3646-02 | Open `/jobs` → the canvas table renders a populated list of activity rows (not a spinner, empty state, or login redirect). | `https://console.<fqdn>/jobs` | ☐ | |
| 3646-03 | The Kind column is present in the header and each row shows its kind; full header = Name·Kind·App·Deps·Parent·Status·Started·Duration·Actions. | `https://console.<fqdn>/jobs` | ☐ | |
| 3646-04 | Scroll/search to the `install-openbao` row → it renders green / Succeeded (the install is honestly green). | `https://console.<fqdn>/jobs` | ☐ | |
| 3646-08 | Every rendered row maps to a real HelmRelease install / terraform stage (no placeholder, no synthetic/fabricated entry). | `https://console.<fqdn>/jobs` | ☐ | |
| 3646-10 | Set the Status filter to `failed` → the table shows the genuinely-failing rows, each with an honest failed status (the Status filter works). | `https://console.<fqdn>/jobs` | ☐ | |
| 3646-11 | Leave the table on screen ~30s → rows update live (tail) as reconciliation progresses; a status badge changes in place without a manual reload. | `https://console.<fqdn>/jobs` | ☐ | |
| 3646-12 | On a Failed row (Status=failed), a Re-run / Retry-reconcile button is present on the row (visible on the row or on hover). | `https://console.<fqdn>/jobs` | ☐ | |
| 3646-13 | On a Succeeded / healthy / Confirming row, NO Re-run button renders — the control is gated to Failed rows only. | `https://console.<fqdn>/jobs` | ☐ | |
| 3646-14 | Click Re-run on a Failed row → a success toast/feedback appears and the button flips in place (e.g. `Requesting…`) — the browser triggered a re-reconcile, no terminal. | `https://console.<fqdn>/jobs` | ☐ | |
| 3646-19 | Use the same Re-run button on a Failed row — one remediation mechanism across rows, no per-kind UI (the single-table/single-ingestion shape). | `https://console.<fqdn>/jobs` | ☐ | |

## 10. Regenerate-on-current-env (meta discipline) — #3581
| ID | Test case (what the user sees) | Walk link | Result | Evidence |
|---|---|---|---|---|
| 3581-01 | The signed handover URL → lands directly on `/dashboard` signed-in (env switcher shows the live env, avatar E, no login form). | `https://console.<fqdn>/dashboard` | ☐ | |
| 3581-02 | Click the avatar (top-right) → menu reads "Signed in as emrah.baysal@openova.io" with a Sign-out item — confirms the landed identity is the owner-admin. | `https://console.<fqdn>/dashboard` | ☐ | |
| 3581-03 | Grafana bare URL → lands on Grafana Home ("Welcome to Grafana", full UI, Profile avatar), `?orgId=1`, no login form (SSO landed signed-in). | `https://grafana.<fqdn>` | ☐ | |
| 3581-04 | Harbor (registry) bare URL → lands on `/harbor/projects` (projects, repos, Administration nav), no login form; user dropdown `emrah.baysal@openova.io`. | `https://registry.<fqdn>` | ☐ | |
| 3581-05 | Gitea bare URL → lands on the gitea dashboard titled "emrah.baysal - Dashboard - Catalyst Gitea", logged in; URL stays on :443. | `https://gitea.<fqdn>` | ☐ | |
| 3581-06 | OpenBao bare UI → final rendered screen is the authenticated Vault session (`/ui/vault/secrets`, Secrets Engines with cubbyhole/ + secret/ kv), NO `/ui/vault/auth` token form. | `https://openbao.<fqdn>` | ☐ | |
| 3581-07 | The rendered `UAT.md` (on GitHub) H1 + banner name only the live env — zero `hw150`/`hw144`/`hw128` predecessor mentions. | `https://github.com/openova-io/openova/blob/main/docs/ledger/UAT.md` | ☐ | |
| 3581-08 | The 🌟 North-Star table in the rendered `UAT.md` names only the live env (witnessed live in the browser on this fresh env). | `https://github.com/openova-io/openova/blob/main/docs/ledger/UAT.md` | ☐ | |
| 3581-09 | Every linked screenshot in the rendered `UAT.md` resolves under the current env's evidence dir — the wiped-predecessor evidence is fully flushed. | `https://github.com/openova-io/openova/blob/main/docs/ledger/UAT.md` | ☐ | |

## Excluded — not web-UI-walkable (backend-only, tracked outside this report)
> These rows assert backend / cluster / wire / CI invariants that have **no operator-console screen** to click. They are listed for traceability only and are NOT part of the UI-walkable test-case count above. (`missing-ui` = the parent surface renders but the specific sub-feature/affordance is unbuilt or not ingested on the current pin; `backend` = realised only as a CR field, secret, NetworkPolicy, pod/containerd state, code path, CI check, or a destructive operator-only RUN.)

| ID | Why it is not a UI test case | Tracked in |
|---|---|---|
| 3374-14 | openova-flow bare URL is fronted by the generic OIDC gate and resolves with the owner session, but the binary serves a JSON service descriptor — headless HTTP+SSE router, no web-UI by design. | #3374 (openova-flow headless router — backend, no UI) |
| 3374-22 | sso-bridge dead-grant absence: the `grant_operator_admin` / `skipping realm-role` paths are removed (not no-op'd) — verified by code/reconcile, no UI surface. | #3374 (sso-bridge code path — backend unit) |
| 3374-23 | auth.go `-race` assertion: a non-`/sovereign-admins` user must get no owner claim — covered by a handler unit test, not a page. | #3374 (auth.go handler unit test — backend) |
| 3374-24 | Tenant tier: per-Org console bare URL `console.<orgslug>.omani.homes` must land signed-in as the org owner, admin in THEIR realm. (Real surface; currently connection-refused — tracked as a serving defect, not UI-walkable until it serves.) | #3376 (per-Org external serving) — surface lives at `console.<orgslug>.omani.homes` |
| 3374-25 | Tenant tier: a purchased app bare URL `wordpress.<orgslug>.omani.homes` must land signed-in via the org realm. (Real surface; currently connection-refused — same root.) | #3376 (per-Org app serving) — surface lives at `wordpress.<orgslug>.omani.homes` |
| 3374-26 | Generality (#3370 bar): a brand-new throwaway app bare URL lands zero-click authenticated via the generic OIDC gate with no console/realm change beyond one gate entry. Needs a fresh prov of a throwaway gate entry (not yet provisioned). | #3374 (generality — needs fresh-prov gate entry) |
| 3375-06 | bp-grafana consumer topology-picker: a stateful consumer has no catalog New-instance create-time topology picker (singleton-per-Org). Surface renders; the create-picker sub-feature is unbuilt. | #3375 (consumer topology-picker — missing-ui) |
| 3375-22 | Settings topology/DR banner: a green active-hot-standby state (healthy) or a red "standby region not provisioned — DR INACTIVE" banner (capped). No topology-DR banner UI exists in Settings. | #3375 (Settings DR-banner — missing-ui) |
| 3375-23 | openbao cross-region snapshot save/fetch surfaced on `/jobs` (region-a save Complete + region-b fetch Complete). No `OpenBao Snapshot Save (cron)` row exists on the canvas. | #3375 (snapshot-cron on /jobs — missing-ui) |
| 3375-30 | Region-kill: capped (region-missing) case — the Switchover button is disabled with the region-missing reason. Gated on a deliberately-broken/capped env (no capped env available). | #3375 (capped-env case — gated on a broken env) |
| 3375-31 | Region-kill EXECUTION: operator severs region-a (instance destroy / node cordon / NetworkPolicy isolation) + runs a monotonic counter-writer across the kill. Destructive operator action, not a browser click. | #3375 / DoD D31 §6 (destructive region-kill RUN — operator-walk) |
| 3375-32 | Region-kill (after switchover): Topology tab shows primary now region-b, a switchover audit event, app reachable on its FQDN, switchover ≤30s (RTO) with RPO 0. Gated on the kill RUN. | #3375 (post-switchover read — gated on kill RUN) |
| 3375-33 | Region-kill (after rejoin): operator restores region-a → Topology tab shows recovery without split-brain (ONE primary, rejoined region a follower). Gated on the kill RUN. | #3375 (post-rejoin read — gated on kill RUN) |
| 3379-03 | A cutover progress card showing the live step ("Step 2 / 11 — harbor-prewarm", a percent bar). The Sovereignty section renders a state badge but no live per-step progress card. | #3379 (Step-N/11 progress card — missing-ui) |
| 3379-04 | A terminal-state indicator `cutoverComplete` → a "Sovereign — tethers severed" badge (CTA hidden once complete). No distinct cutoverComplete indicator beyond the Tethered/severed badge. | #3379 (cutoverComplete end-state — missing-ui) |
| 3379-11 | (After a COMPLETE cutover) Settings shows a steady "Sovereign — tethers severed" state with the CTA hidden. No such end-state indicator exists; post-cutover independence is a backend fact with no end-user screen. | #3379 (tethers-severed end-state — missing-ui) |
| 3379-12 | #3678 true deny-egress: the 600s hold is a default-deny-egress CCNP (`cutover-egress-block`) with a call-home assertion that FAILS if a mothership host is reachable. A NetworkPolicy + in-Job assertion, no console surface. | #3678 / #3379 (deny-egress CCNP — backend) |
| 3379-13 | #3671 faithful registry pivot: `registriesYamlActive=v2` flips node containerd to local Harbor; step-04 counts per-node v2 acks. A containerd `registries.yaml` rewrite per node, no console surface. | #3671 / #3379 (containerd registry pivot — backend) |
| 3379-14 | #3667 durable seal: `cutoverComplete=true` sealed in OpenBao (`secret/catalyst/cutover-complete`) so a chart upgrade cannot revert it. A sealed secret, no console surface. | #3667 / #3379 (durable OpenBao seal — backend) |
| 3379-15 | #3681 audit fidelity: `cutoverStartedAt` written once (true T0); resume advances a separate `cutoverLastAttemptStartedAt`. Status-ConfigMap fields, no console surface. | #3681 / #3379 (status-CM audit fields — backend) |
| 3379-16 | #3695 zero residual tether: every external-registry workload re-keyed to local Harbor; the `ghcr-pull` secret re-keyed off `ghcr.io`; no live pod references a mothership registry. Pod image refs + a secret key, no console surface. | #3695 / #3379 (workload registry re-key — backend) |
| 3383-08 | Kubernetes namespace `sme` → `org-services`. A namespace name is never displayed to a User; it appears only in cluster tooling. | #3383 (namespace rename — backend) |
| 3383-09 | A namespace name is never displayed to a User; appears only in cluster tooling — no browser screen renders it. | #3383 (namespace — backend) |
| 3383-10 | Chart template dir `products/catalyst/chart/templates/sme-services/` → `org-services/`. A chart directory path has no rendered UI. | #3383 (chart dir rename — backend) |
| 3383-11 | Secret `sme-secrets` → `org-services-secrets` (+ catalyst-api `CATALYST_SME_JWT_SECRET` env repoint). A Secret name and env-var key are never shown to a User. | #3383 (secret rename — backend) |
| 3383-12 | API route `POST /api/v1/sme/tenants` → `POST /api/v1/organizations` (+ deprecation alias). A raw API path is not a user-facing screen; the User sees the create-org FORM. | #3383 (API route rename — backend/wire) |
| 3383-13 | Go handler/store identifiers (`HandleCreateSMETenant`, `SMETenantProvisionStore`, …) → `Organization*`. Source-code symbols have no UI. | #3383 (Go symbols rename — backend) |
| 3383-14 | CI naming guard (`scripts/check-no-persona-machinery.sh` + `.github/workflows/naming-guard.yaml`). A CI workflow surfaces on a GitHub PR check, not on the console. | #3383 (CI naming-guard — CI/pipeline) |
| 3383-15 | Legitimate data-value survivor `TenantKindSME TenantKind = "sme"` (a Tier enum value) — intentionally retained, never displayed as a persona label, no UI. | #3383 (retained enum value — backend, by design) |
| 3642-21 | In-vCluster CRD registration inside vc-mgmt (httproutes / externalsecrets / cnpg clusters/poolers/scheduledbackups) registered INSIDE the mgmt vCluster. No console widget exposes the inner-vCluster CRD inventory. | #3642 (inner-vCluster CRD inventory — backend) |
| 3642-22 | Per-app pod-level syncer suffix (`-x-<innerNs>-x-mgmt-vcluster`) on each migrated pod. A host-cluster pod-name detail with no UI; PART B's mgmt-block placement is the browser-checkable equivalent. | #3642 (per-pod syncer suffix — backend) |
| 3642-23 | `cutoverComplete=true` survival of the 7 through the Pillar-5 600s deny-egress hold (no `admin.loft.sh` tether). Owned by the Pillar-5 cutover runbook's own surface, not duplicated here. | #3379 (Pillar-5 deny-egress — owned elsewhere) |
| 3646-05 | Set the Kind filter to `task` → the table re-filters to child-Job rows (`task-cnpg-pair-*`, `task-scan-vulnerabilityreport-*`, …). The Kind dropdown offers only `All`/`lifecycle` on this pin — no `task` ingestion. | #3646 / #3665 (task-kind ingestion — missing-ui on pin) |
| 3646-06 | Set the Kind filter to `cron` → a recurring `cron-openbao-snapshot-save` row renders. No `cron` kind / CronJob ingestion on this pin. | #3646 / #3665 (cron-kind ingestion — missing-ui on pin) |
| 3646-07 | Find the `reconciler-sso-bridge-reconciler` row (Kind=`reconciler`) → it renders a health status (`healthy`), not a one-shot Succeeded. No `reconciler` kind ingestion on this pin. | #3646 / #3665 (reconciler-kind ingestion — missing-ui on pin) |
| 3646-09 | Find a group/lifecycle row (the `cutover` group, or `reconcilers`) → its status reflects its real children (a failed child reads failed). No `group` kind / cutover roll-up row on this pin. | #3646 / #3674 (group-status roll-up — missing-ui on pin) |
| 3646-15 | After clicking Re-run, open that row's detail → the latest Execution's first line credits the operator (`requested by emrah.baysal@…`). The detail panel reads "No execution recorded yet" — no per-attempt Execution audit line on this pin. | #3646 / #3670 (Execution audit-line — missing-ui on pin) |
| 3646-16 | Find the `cutover` group row → it expands to 11 `cutover-step-*` rows (the 11-step execution tree). The cutover-execution projection (PR #3652) is not ingested on this pin. | #3646 / PR #3652 (cutover 11-step projection — missing-ui on pin) |
| 3646-17 | Read the cutover group status while steps are pending/failing → NOT premature-Succeeded; reads failed/running honestly from its step children. No cutover execution row exists to read on this pin. | #3646 (cutover group honest status — missing-ui on pin) |
| 3646-18 | Confirm a HelmRelease (`install-*`), a child Job (`task-*`), and a reconciler Deployment (`reconciler-*`) all render together via the one canvas — multi-kind co-render. Only the `lifecycle` kind renders on this pin. | #3646 (multi-kind co-render — missing-ui on pin) |
| 3668-09 | The edit is committed to the single Gitea IaC source (not a transient store overlay) and a chart upgrade does NOT revert it — a Git/Flux/CR backend fact with no operator-console surface to click. | #3668 (chart-upgrade-no-revert — backend Git/Flux fact) |
| 3668-20 | When the Gitea IaC source is down, a Save shows an amber "Saved (cache only) — IaC commit failed" banner, not a green save. Taking Gitea down is a destructive fault-injection with no operator-console toggle. | #3668 (amber-path fault-injection — destructive, no toggle) |
| 3668-39 | "Helm no longer co-owns the CR" + "chart upgrade does not revert" guarantees — Git/Flux/CR backend facts, not browser-observable. | #3668 (durable-IaC-vs-skin — backend) |
| 3687-06 | Store-row-is-a-projection: the FerretDB `store.Tenant` row carries the CR's UID/owner-ref (downstream projection). No console screen renders projection-vs-authority. | #3687 (object-model — backend data-ordering invariant) |
| 3687-07 | Delete-CR GC-cascade: deleting the Organization GCs its vCluster + realm + repos. No operator-facing delete-cascade surface is walked. | #3687 (finalizer GC — backend) |
| 3687-08 | One shared `TenantCreatedPayload` struct across publisher + consumer — a code-internal contract with no UI. | #3687 (payload-shape unity — code invariant) |
| 3687-13 | ONE Flux source reconciles both the funnel and console Orgs (one Git location) — a Flux-topology assertion with no console screen. | #3687 (per-Org gitops source — backend) |
| 3687-14 | The NATS bridge no longer ack-and-skips and the parallel SME store/writer is deleted — backend-convergence assertions, no UI. | #3687 (door-convergence internals — backend) |
| 3687-18 | `status.vcluster.phase` is readback-derived, not hardcoded `Provisioning` — a CR-field assertion; only the rendered badge has a screen. | #3687 (CR phase field — backend) |
| 3687-19 | A `catalyst-tenant` GitRepository + Kustomization reconciling the per-Org vCluster manifests exists — a Flux-object assertion, no screen. | #3687 (per-Org reconcile source — backend) |
| 3687-24 | The console create/update spine COMMITS the Application CR to Gitea (then Flux applies) and survives catalyst-api scaled to 0 — a write-path/storage assertion, no console screen. | #3687 (write-path Git-vs-etcd — backend) |
| 3687-25 | `kubectl get applications -A` is non-empty and equals the running-instance count; CNPG embedded clusters eliminated; watch loop healthy — cluster-state assertions. | #3687 (CR-vs-instance parity — backend) |
| 3687-33 | The consumption resolver keys purely on the `openova.io/organization` label + config-driven exclusion set, zero per-name special-casing — a backend-resolver assertion. | #3687 (resolver generality — backend) |
| 3687-36 | The funnel door AND the BSS/internal door yield an Organization through the SAME write path — a code-path assertion; only directory shape is visible. | #3687 (door write-path parity — backend) |
| 3687-37 | The SAME create→commit→fan-out→reconcile loop drives every blueprint/placement with zero blueprint-specific write code — a backend assertion. | #3687 (loop genericity — backend) |
| 3687-38 | `kubectl get applications -A` / `kubectl get organizations -A` both non-empty and console + cluster show the SAME sets on a fresh Sovereign — cluster-state parity. | #3687 (ground-truth parity — backend) |
| 3687-39 | The bootstrap Application-CR adoption guard ADOPTS already-installed instances (status-only, no duplicate fan-out) — a reconcile-behavior assertion. | #3687 (adoption-vs-duplication — backend) |

**Excluded total: 57 backend-only rows** (object-model 14 · SSO 6 · topology/DR 7 · placement 3 · organizations 8 · catalog 3 · cutover 8 · jobs 8 · funnel 0 · regenerate 0).

**Inventory reconciliation:** 186 UI-walkable + 57 excluded = 243 canonical rows — the full per-row UAT inventory (the 10 walkthrough runbooks under [`uat-walkthrough/`](uat-walkthrough/)).
