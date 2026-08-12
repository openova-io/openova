# UAT — verdict and confidence, on one line per case

> **DERIVED — regenerate, never hand-edit.** `UAT.md` is the verdict of
> record; `uat-observations.csv` is the evidence of record. This document
> outranks neither. Rebuild with:
>
> ```
> python3 scripts/uat-confidence.py --snapshot --env <env>
> python3 scripts/uat-confidence-report.py --env <env>
> ```

Environment: **hw294** · denominator STONE at **286**

`conf` is the Beta-Bernoulli posterior over time-discounted evidence.
`every` is how many cycles pass between walks at this row's Leitner box.
`proofs` counts DISTINCT OTHER environments the row last passed on — that
is the number that survives a wipe, because it is evidence about the code
rather than about one machine.

**A ✅ at conf 0.11 and a ✅ at conf 0.80 are different claims.** The ledger
renders them identically; this table does not.

| verdict | rows |
|---|--:|
| ✅ | 196 |
| ❌ | 76 |
| ☐ | 14 |

| # | epic | case | verdict | conf | box | every | proofs | due | last env |
|---|---|---|:-:|--:|:-:|--:|--:|:-:|---|
| R1 | janitor | catalyst-api orphan-sweep no longer reaps a `ready` Sovereign — denylist inversion (`case "wi… | ✅ | 0.801 | 2 | 5 | 2 |  | hw294 |
| R2 | network | Cross-node pod TCP carries full-size data packets without DF-drop in BOTH regions — the Ciliu… | ✅ | 0.8018 | 2 | 5 | 2 |  | hw294 |
| R3 | sso | bp-plane-isolation openbao default-deny ADMITS sso-bridge ingress → sso-bridge reaches openba… | ✅ | 0.685 | 0 | 1 | 2 | yes | hw294 |
| R4 | sso | sso-bridge egress CNP permits openbao + keycloak — the reconciler's own egress is not blocked. | ✅ | 0.685 | 0 | 1 | 2 | yes | hw294 |
| R5 | sso | sso-bridge re-mints the grafana OIDC client secret each tick (KC_ADDR cache refreshed) — no s… | ✅ | 0.6871 | 0 | 1 | 2 | yes | hw294 |
| R6 | postgres | bp-postgres NetworkPolicy admits the declared consumers (CNPG operator probe + app dials). | ✅ | 0.801 | 2 | 5 | 2 |  | hw294 |
| R7 | plane-isolation | bp-plane-isolation admits gitea ingress for its declared consumers (org-services→gitea de-vcl… | ✅ | 0.6871 | 0 | 1 | 2 | yes | hw294 |
| R8 | gitea | gitea-flux-auth secrets are seeded — Flux pulls from gitea with valid credentials. | ✅ | 0.685 | 0 | 1 | 2 | yes | hw294 |
| R9 | sso | oidc-gate client_secret is seeded for powerdns-admin — full OIDC round-trip completes. | ✅ | 0.6871 | 0 | 1 | 2 | yes | hw294 |
| R10 | orgs | org-controller RBAC carries update/patch — per-Org provisioning completes (was create-only, n… | ✅ | 0.685 | 0 | 1 | 2 | yes | hw294 |
| R11 | gitea | gitea git-data survives a POD RESTART — the PVC rebinds and every bare repo retains its HEAD … | ✅ | 0.6837 | 0 | 1 | 2 | yes | hw294 |
| R12 | postgres | shared-pg `-mesh-rw` resolves in BOTH regions + CNPG streaming replication is live. | ✅ | 0.0896 | 0 | 1 | 1 |  | hw294 |
| R13 | convergence | region-B keycloak / gitea / harbor reach steady-state with NO CrashLoop. | ❌ | 0.0 | 0 | 1 | 1 |  | hw294 |
| R14 | model | console Organizations directory reads the live Organization CRs (not an empty in-memory store). | ✅ | 0.685 | 0 | 1 | 2 | yes | hw294 |
| R15 | funnel | funnel plan-slug propagates end-to-end — the chosen plan reaches the provisioned Org. | ✅ | 0.685 | 0 | 1 | 2 | yes | hw294 |
| R16 | funnel | the funnel + BSS Org doors are collapsed onto ONE path — `console.<slug>` lands 200. | ❌ | 0.0 | 0 | 1 | 0 | yes | hw294 |
| R17 | orgs | deleting an Organization CR cascades cleanly — no orphaned ns / app / DNS leak. | ✅ | 0.0411 | 0 | 1 | 1 | yes | hw294 |
| R18 | cutover | handover-key self-publish guard — the Sovereign does not re-publish its handover key on recon… | ✅ | 0.6871 | 0 | 1 | 2 | yes | hw294 |
| R19 | agenity | The per-Org **Agenity** workspace StatefulSet reaches Running with its Anthropic credential s… | ❌ | 0.0 | 0 | 1 | 1 | yes | hw294 |
| R20 | delivery | the deploy-bot bumps image pins per-line (not a blanket bump) — stale-render avoided. | ✅ | 0.8018 | 2 | 5 | 2 |  | hw294 |
| R21 | catalog | catalog-seed pins match the published ghcr chart versions — no inert lagging Blueprint CR. | ✅ | 0.3619 | 0 | 1 | 2 |  | hw294 |
| R22 | plane-isolation | bp-plane-isolation admits apiserver-egress + gateway-ingress CNPs — CNPG admission + gateway … | ✅ | 0.2881 | 0 | 1 | 1 |  | hw294 |
| W1 | wizard | Deployment wizard step 1 does NOT pre-fill a fabricated company into the Organisation identit… | ❌ | 0.0 | 0 | 1 | 0 | yes | hw294 |
| W2 | wizard | The wizard does NOT derive cloud provider/region from a fabricated value. | ❌ | 0.0 | 0 | 1 | 0 | yes | hw294 |
| W3 | wizard | Marketplace-mode storefront branding fields are optional and blank (hints only, no fabricated… | ☐ | 0.5011 | 0 | 1 | 1 | yes | hw294 |
| W4 | wizard | Every wizard step offers a Back control (footer navigation is consistent across the flow). | ✅ | 0.6871 | 0 | 1 | 2 | yes | hw294 |
| W5 | wizard | Component-selection step counts are self-consistent and every component id resolves to a real… | ❌ | 0.0 | 0 | 1 | 0 | yes | hw294 |
| M1 | janitor | janitor hardening — log-only/dry-run for a full live cycle before any destructive sweep. | ✅ | 0.8018 | 2 | 5 | 2 |  | hw294 |
| M2 | apps | newapi admin-token seeded into OpenBao at provision (was unseeded → admin-promote fault). | ✅ | 0.685 | 0 | 1 | 2 | yes | hw294 |
| M3 | network | vcluster-tier CNP applied host-side — the per-Org vcluster tier carries its split CNP. | ✅ | 0.685 | 0 | 1 | 2 | yes | hw294 |
| M4 | apps | agenity image-pull half — fresh-install ghcr-pull path. | ✅ | 0.6837 | 0 | 1 | 2 | yes | hw294 |
| G1 | adoption | crossplane provider-opentofu Observe-only adoption populates on the fresh prov. | ❌ | 0.0 | 0 | 1 | 1 |  | hw294 |
| G2 | apps | newapi ES-sync drives the admin token into a fresh per-Org newapi (seed merged in M2). | ❌ | 0.0 | 0 | 1 | 0 |  | hw294 |
| G3 | dr | On a two-region Sovereign every `continuums.dr.openova.io` CR declares a standby and every cn… | ❌ | 0.0 | 0 | 1 | 1 | yes | hw294 |
| G4 | adoption | A janitor pass with the destructive gate CLOSED reports its cloud sweeps under a `…WouldReap`… | ✅ | 0.8018 | 2 | 5 | 2 |  | hw294 |
| G5 | janitor | janitor log-only live proof (the dry-run full-cycle observation). | ✅ | 0.801 | 2 | 5 | 2 |  | hw294 |
| G6 | model | **The #4212 Seam-3 spine producer has enrolled the spine into the object model** — `kubectl g… | ✅ | 0.0643 | 0 | 1 | 2 | yes | hw294 |
| G7 | orgs | vcluster dual-door walk — both Org doors land a vcluster-isolation Org. | ❌ | 0.0 | 0 | 1 | 0 | yes | hw294 |
| G8 | apps | anthropic credential seeded into the agentic runtime — chat works end-to-end. | ❌ | 0.0 | 0 | 1 | 0 | yes | hw294 |
| G9 | apps | agentic-run half — the agenity solo agent chats + drives create_application. | ❌ | 0.0 | 0 | 1 | 0 | yes | hw294 |
| G10 | placement | Placement EPIC acceptance: EVERY Application CR carries a non-empty `spec.placement` drawn fr… | ✅ | 0.1772 | 0 | 1 | 2 | yes | hw294 |
| G11 | cutover | sovereignty cutover — the **11-step** chain runs to completion and the 10-min deny-egress hol… | ❌ | 0.0 | 0 | 1 | 1 | yes | hw294 |
| G12 | dr | region-kill (Pillar-3) — kill a region, prove failover + recovery. | ✅ | 0.595 | 0 | 1 | 2 | yes | hw294 |
| 1 | model | Console bare URL → lands `/dashboard` signed-in as the owner (no PIN/login form). | ✅ | 0.6866 | 0 | 1 | 2 | yes | hw294 |
| 2 | model | Full sidebar renders (Dashboard / Cloud / Apps / Catalog / Agenity / Jobs / Compliance / User… | ✅ | 0.6866 | 0 | 1 | 2 | yes | hw294 |
| 3 | model | The voucher-redeem URL opened in an AUTHED OWNER browser session bounces to `/dashboard` CLIE… | ✅ | 0.0731 | 0 | 1 | 1 | yes | hw294 |
| 4 | model | App page tab strip generality: open ≥2 archetypes (a DB `shared-pg`, a consumer `harbor`) → i… | ✅ | 0.0784 | 0 | 1 | 1 | yes | hw294 |
| 5 | model | Organizations directory: the customer Org row shows KIND=customer, TIER=org (the #4292 isolat… | ✅ | 0.6866 | 0 | 1 | 2 | yes | hw294 |
| 6 | model | The customer Org is present as a real Organization in the operator directory (same record das… | ✅ | 0.6866 | 0 | 1 | 2 | yes | hw294 |
| 7 | model | Org detail renders canonical fields: slug `acme` (NOT `sme-<uuid>`), kind customer, tier `org… | ✅ | 0.0233 | 0 | 1 | 0 | yes | hw294 |
| 8 | model | The Create-organization form renders fully (kind picker, slug, Company name, Admin email, par… | ✅ | 0.0359 | 0 | 1 | 1 | yes | hw294 |
| 9 | model | Org detail Status = active, backed by a real `vcluster` isolation (the create→Provision loop … | ✅ | 0.6866 | 0 | 1 | 2 | yes | hw294 |
| 10 | model | The customer Org is present + ACTIVE backed by a real `vcluster` isolation (Lane B convergenc… | ✅ | 0.6866 | 0 | 1 | 2 | yes | hw294 |
| 11 | model | Org detail Status = active and the directory + detail consistently show `vcluster` isolation.… | ✅ | 0.6866 | 0 | 1 | 2 | yes | hw294 |
| 12 | model | On re-load the Org detail consistently reports active + `vcluster` (stable, no false flicker)… | ✅ | 0.6866 | 0 | 1 | 2 | yes | hw294 |
| 13 | model | Catalog grid renders the Blueprint cards; `bp-postgres` detail has a **+ New instance** button. | ✅ | 0.6866 | 0 | 1 | 2 | yes | hw294 |
| 14 | model | The shared-PG reuse model is LIVE: the `shared-pg` app **Contexts** tab shows multiple consum… | ✅ | 0.6866 | 0 | 1 | 2 | yes | hw294 |
| 15 | model | Apps list renders one card per Application (all Platform/BOOTSTRAP-… a customer-launched app … | ✅ | 0.0222 | 0 | 1 | 0 | yes | hw294 |
| 16 | model | A customer app page (`/app/<name>`) → Settings/Topology → change topology → Save persists; th… | ❌ | 0.0 | 0 | 1 | 1 | yes | hw294 |
| 17 | model | Dashboard treemap is a meaningful drill-down surface (Organization → vCluster → Application s… | ✅ | 0.6866 | 0 | 1 | 2 | yes | hw294 |
| 18 | model | Scan every treemap cell: NO ephemeral Job-pod cell appears (no `cutover-*`, scan-vulnerabilit… | ✅ | 0.6866 | 0 | 1 | 2 | yes | hw294 |
| 19 | model | Count the ESTATE cards on /apps — `[data-card-kind="instance"]` inside `sov-apps-grid` (#6056… | ❌ | 0.0 | 0 | 1 | 0 | yes | hw294 |
| 20 | model | With a customer Org present, the customer estate is visually distinct from platform pods on t… | ✅ | 0.6866 | 0 | 1 | 2 | yes | hw294 |
| 21 | model | Organizations Showback panel: the **Application** column for a selected Org lists only that O… | ✅ | 0.6866 | 0 | 1 | 2 | yes | hw294 |
| 22 | model | Showback shows a single visually-distinct **Platform overhead** roll-up line holding all cont… | ✅ | 0.6866 | 0 | 1 | 2 | yes | hw294 |
| 23 | model | After a 2nd Org runs an app, the showback panel shows a SECOND Org row attributed only to its… | ✅ | 0.6866 | 0 | 1 | 2 | yes | hw294 |
| 24 | model | `shared-pg` renders the canonical tab strip Overview · Contexts · Topology · Dependencies (Co… | ✅ | 0.6866 | 0 | 1 | 2 | yes | hw294 |
| 25 | model | One consistent model across surfaces: `/organizations` shows the Orgs that `/apps`, the dashb… | ❌ | 0.0 | 0 | 1 | 0 | yes | hw294 |
| 26 | sso | Console bare URL → lands on the dashboard signed-in as the owner; no PIN/login/"Sign in with…… | ☐ | 0.6361 | 0 | 1 | 2 | yes | hw294 |
| 27 | sso | Avatar (top-right) menu reads "Signed in as the owner" with a Sign-out item. | ☐ | 0.6361 | 0 | 1 | 2 | yes | hw294 |
| 28 | sso | Users page renders the pre-seeded owner row the owner (tier=owner UserAccess CR), signed-in a… | ☐ | 0.6361 | 0 | 1 | 2 | yes | hw294 |
| 29 | sso | Re-open the bare console URL after the session TTL → lands signed-in again, no PIN re-prompt. | ❌ | 0.0 | 0 | 1 | 0 | yes | hw294 |
| 30 | sso | Grafana bare URL → lands on Grafana Home, full UI, no login form; left nav shows Administrati… | ❌ | 0.0 | 0 | 1 | 1 | yes | hw294 |
| 31 | sso | Gitea bare URL → lands on the gitea dashboard titled "emrah.baysal — Dashboard", no login; pr… | ❌ | 0.0 | 0 | 1 | 1 | yes | hw294 |
| 32 | sso | Harbor bare URL → lands on `/harbor/projects`, no login form; user dropdown the owner with Ad… | ❌ | 0.0 | 0 | 1 | 1 | yes | hw294 |
| 33 | sso | OpenBao bare UI → lands in an authenticated Vault session (Secrets engines / dashboard), NO t… | ❌ | 0.0 | 0 | 1 | 0 | yes | hw294 |
| 34 | sso | Keycloak admin console for the **sovereign** realm → lands inside the admin console (realm ov… | ❌ | 0.0 | 0 | 1 | 1 | yes | hw294 |
| 35 | sso | Guacamole bare URL → lands on the Guacamole connections list, signed-in; no Tomcat 404, no `/… | ❌ | 0.0 | 0 | 1 | 0 | yes | hw294 |
| 36 | sso | PowerDNS-Admin bare URL → lands on the dashboard signed-in; no redirect loop, no OAuth error,… | ❌ | 0.0 | 0 | 1 | 1 | yes | hw294 |
| 37 | sso | newapi bare URL (1st hit) → lands on `/console` signed-in as admin (role 100); no "Unknown OA… | ❌ | 0.0 | 0 | 1 | 0 | yes | hw294 |
| 38 | sso | newapi bare URL (2nd hit, re-entry) → lands on `/console` again signed-in; NOT an "already bo… | ❌ | 0.0 | 0 | 1 | 0 | yes | hw294 |
| 39 | sso | Hubble bare URL → lands on the Hubble UI, authenticated (not an anonymous/unauth view, no log… | ❌ | 0.0 | 0 | 1 | 1 | yes | hw294 |
| 40 | sso | Marketplace bare URL → renders the anonymous storefront (public, by design); confirm no spuri… | ☐ | 0.6361 | 0 | 1 | 2 | yes | hw294 |
| 41 | sso | Keycloak sovereign realm → Users lists the single owner principal the owner (enabled). | ☐ | 0.0729 | 0 | 1 | 1 | yes | hw294 |
| 42 | sso | Owner user → Groups tab shows membership in `/sovereign-admins` (alongside `/openova-users`) … | ☐ | 0.6361 | 0 | 1 | 2 | yes | hw294 |
| 43 | sso | Owner user → Role mapping tab: effective realm roles include `catalyst-admin` (not only defau… | ☐ | 0.6361 | 0 | 1 | 2 | yes | hw294 |
| 44 | sso | Groups → `/sovereign-admins` → Role mapping: group confers `catalyst-admin` (console) and rea… | ☐ | 0.6361 | 0 | 1 | 2 | yes | hw294 |
| 45 | sso | Console Users panel: owner row + the ability to view/manage users renders — proving console a… | ☐ | 0.6361 | 0 | 1 | 2 | yes | hw294 |
| 46 | topology | `bp-postgres` catalog detail renders; click **New instance** → the create dialog opens with a… | ✅ | 0.6866 | 0 | 1 | 2 | yes | hw294 |
| 47 | topology | Topology `<select>` options read exactly the ONE canonical vocabulary: `singleton`, `active-p… | ✅ | 0.6866 | 0 | 1 | 2 | yes | hw294 |
| 48 | topology | `active-passive` is a selectable option in the create `<select>` (not folded away). | ✅ | 0.0934 | 0 | 1 | 1 | yes | hw294 |
| 49 | topology | `singleton` is a separate selectable option (single-region single-instance), distinct from th… | ✅ | 0.6866 | 0 | 1 | 2 | yes | hw294 |
| 50 | topology | Pick `active-hot-standby`, name the instance, Provision → the create succeeds (toast/redirect… | ✅ | 0.6778 | 0 | 1 | 2 | yes | hw294 |
| 51 | topology | `shared-pg` Topology tab renders a per-region placement view listing region-a (active) and re… | ❌ | 0.0 | 0 | 1 | 1 |  | hw294 |
| 52 | topology | `shared-pg` Topology tab renders ROLE asymmetry across the pair: the region-a card reads `● P… | ❌ | 0.0 | 0 | 1 | 1 |  | hw294 |
| 53 | topology | Open a per-app Topology tab and read its placement view (singleton apps note no per-region/st… | ✅ | 0.6866 | 0 | 1 | 2 | yes | hw294 |
| 54 | topology | `shared-pg` Topology tab declared-topology strip renders the canonical mode in ONE vocabulary… | ✅ | 0.6866 | 0 | 1 | 2 | yes | hw294 |
| 55 | topology | `shared-pg` Topology tab renders EXACTLY ONE topology value, runtime-derived (`derivedFromRun… | ❌ | 0.0 | 0 | 1 | 0 |  | hw294 |
| 56 | topology | `shared-pg` Topology tab per-region placement + replication block: region-a primary, region-b… | ❌ | 0.0 | 0 | 1 | 1 | yes | hw294 |
| 57 | topology | `shared-pg` Topology tab Switchover button is present and armed because a live 2-region cnpg-… | ❌ | 0.0 | 0 | 1 | 0 | yes | hw294 |
| 58 | topology | A **singleton** app (cilium) Topology tab: the DR section / Switchover button does NOT render… | ✅ | 0.6869 | 0 | 1 | 2 | yes | hw294 |
| 59 | topology | Catalog New instance → pick `singleton` → Provision → that app's Topology tab shows single-re… | ✅ | 0.0784 | 0 | 1 | 1 | yes | hw294 |
| 60 | topology | Catalog New instance → pick `active-hot-standby` → Provision → that app's Topology tab shows … | ❌ | 0.0 | 0 | 1 | 0 | yes | hw294 |
| 61 | topology | Apps grid shows newly-provisioned postgres instances as their own cards, each carrying a topo… | ✅ | 0.6869 | 0 | 1 | 2 | yes | hw294 |
| 62 | topology | On an app WITH a live pair, the Topology DR section shows the live Continuum status (Ready / … | ❌ | 0.0 | 0 | 1 | 0 |  | hw294 |
| 63 | topology | An Application declaring **active-hot-standby** with NO Continuum backing renders an honest n… | ✅ | 0.0323 | 0 | 1 | 1 | yes | hw294 |
| 64 | topology | `shared-pg` Topology tab replication-lag field shows a live numeric seconds value (or explici… | ❌ | 0.0 | 0 | 1 | 1 |  | hw294 |
| 65 | topology | Cloud/regions view shows the true region count — a healthy 2-region prov reads `Cluster 2/2` … | ❌ | 0.0 | 0 | 1 | 1 |  | hw294 |
| 66 | topology | Cloud→Clusters renders 2/2 HEALTHY clusters, one per region (me-east-215-a + me-east-215-b), … | ❌ | 0.0 | 0 | 1 | 1 |  | hw294 |
| 67 | topology | grafana status/overview reports Healthy/Running in both regions — no "cannot resolve write ho… | ❌ | 0.0 | 0 | 1 | 0 |  | hw294 |
| 68 | topology | powerdns-admin status reports Healthy/Running — the CNPG-minted DB host resolved (no "could n… | ✅ | 0.6869 | 0 | 1 | 2 | yes | hw294 |
| 69 | topology | keycloak status reports Healthy/Running in both regions — JGroups DB-host resolves, no Unknow… | ❌ | 0.0 | 0 | 1 | 0 |  | hw294 |
| 70 | topology | guacamole status reports Healthy/Running in both regions — no missing-recordings-PVC error su… | ❌ | 0.0 | 0 | 1 | 1 |  | hw294 |
| 71 | topology | Region-kill baseline (before): `shared-pg` Topology tab shows live Continuum Ready, lease hel… | ❌ | 0.0 | 0 | 1 | 0 | yes | hw294 |
| 72 | funnel | Operator console BSS → Vouchers page renders the voucher issuance form (code, credit OMR, pla… | ✅ | 0.6869 | 0 | 1 | 2 | yes | hw294 |
| 73 | funnel | In the voucher form, type a weak code `1234` → Issue → the form rejects it inline ("voucher c… | ✅ | 0.6869 | 0 | 1 | 2 | yes | hw294 |
| 74 | funnel | Leave the code field empty → Issue → a new voucher row appears with a server-auto-generated h… | ✅ | 0.6869 | 0 | 1 | 2 | yes | hw294 |
| 75 | funnel | Stranger opens the redeem page with `?code=<CODE>` → sees "Voucher valid · 5000 OMR" with THI… | ✅ | 0.1546 | 0 | 1 | 1 | yes | hw294 |
| 76 | funnel | Open the redeem page with a junk code → a generic "voucher not valid" message (no tombstone /… | ✅ | 0.6869 | 0 | 1 | 2 | yes | hw294 |
| 77 | funnel | Redeem page source (DevTools): the HTML shows THIS Sovereign's brand and no `console.openova.… | ✅ | 0.6869 | 0 | 1 | 2 | yes | hw294 |
| 78 | funnel | Click "Sign up to redeem" → the browser lands on the plan picker grid (`/plans`). | ✅ | 0.6869 | 0 | 1 | 2 | yes | hw294 |
| 79 | funnel | Plans grid shows the tiers (S/M/L/XL/Flexi) with price/CPU/memory → pick plan M → advances to… | ✅ | 0.6869 | 0 | 1 | 2 | yes | hw294 |
| 80 | funnel | App catalog grid (served from THIS Sovereign's catalog) → pick WordPress → advances to add-on… | ✅ | 0.6869 | 0 | 1 | 2 | yes | hw294 |
| 81 | funnel | Add-ons step shows optional add-ons → leave defaults → Continue → advances to the BCP topolog… | ✅ | 0.6869 | 0 | 1 | 2 | yes | hw294 |
| 82 | funnel | BCP topology step shows BOTH Single-region and Active-hot-standby radios (the Pillar-2 BCP ch… | ✅ | 0.6869 | 0 | 1 | 2 | yes | hw294 |
| 83 | funnel | Review summary shows the chosen plan (M), app (WordPress), topology (Active-hot-standby), the… | ✅ | 0.6869 | 0 | 1 | 2 | yes | hw294 |
| 84 | funnel | On checkout, enter the stranger's email → Send code → type the emailed sign-in code (no passw… | ✅ | 0.1355 | 0 | 1 | 1 | yes | hw294 |
| 85 | funnel | Checkout summary: the voucher credit is applied — "Credit covers this order — 0 OMR due" (no … | ✅ | 0.1355 | 0 | 1 | 1 | yes | hw294 |
| 86 | funnel | Provisioning progress timeline advances to Done (Creating Org → Committing manifests → Provis… | ✅ | 0.1355 | 0 | 1 | 1 | yes | hw294 |
| 87 | funnel | After Launch, the marketplace redirects to the per-Org console — URL becomes the per-Org cons… | ❌ | 0.0 | 0 | 1 | 0 | yes | hw294 |
| 88 | funnel | Per-Org console landing: the stranger is signed in zero-click as the Org owner (their email i… | ✅ | 0.1355 | 0 | 1 | 1 | yes | hw294 |
| 89 | funnel | Per-Org console → Applications view shows the purchased WordPress app card Running/Healthy → … | ✅ | 0.6869 | 0 | 1 | 2 | yes | hw294 |
| 90 | funnel | Terminal acceptance: the purchased WordPress app SERVES at its own FQDN the app FQDN — the li… | ❌ | 0.0 | 0 | 1 | 0 | yes | hw294 |
| 91 | funnel | While signed in, re-open the marketplace root → the returning-user redirect sends the custome… | ✅ | 0.1355 | 0 | 1 | 1 | yes | hw294 |
| 92 | funnel | As the signed-in customer, rapidly re-submit checkout/redeem >5× in a few seconds → after ~5 … | ✅ | 0.1355 | 0 | 1 | 1 | yes | hw294 |
| 93 | funnel | Generality: mint a 2nd voucher and re-walk B.1→B.4 with a different slug (`walk-stranger-two`… | ✅ | 0.6869 | 0 | 1 | 2 | yes | hw294 |
| 94 | funnel | The 2nd Org's console lands signed-in on a different TLD (the 2nd-Org console) — identical ze… | ✅ | 0.1355 | 0 | 1 | 1 | yes | hw294 |
| 95 | funnel | The 2nd Org's purchased app serves at its own different-TLD FQDN (the 2nd-Org app) — two Orgs… | ❌ | 0.0 | 0 | 1 | 0 | yes | hw294 |
| 96 | placement | Handover URL → lands on `/dashboard` already signed-in as the owner (avatar E), no login form. | ✅ | 0.1354 | 0 | 1 | 1 | yes | hw294 |
| 97 | placement | Dashboard renders the cluster treemap and the LAYER 1 / LAYER 2 grouping comboboxes are visible. | ✅ | 0.6778 | 0 | 1 | 2 | yes | hw294 |
| 98 | placement | LAYER 1 → vCluster renders a `host` block **plus one block per per-Org vCluster** (`uatco`, `… | ✅ | 0.1276 | 0 | 1 | 1 | yes | hw294 |
| 99 | placement | An Organization on plan **M or above** is backed by a DEDICATED Org vCluster — `kubectl -n <o… | ✅ | 0.685 | 0 | 1 | 2 | yes | hw294 |
| 100 | placement | An Organization on plan **free or S** is backed by a HOST namespace and has NO vCluster — the… | ✅ | 0.0607 | 0 | 1 | 1 | yes | hw294 |
| 101 | placement | The console Organization detail's `isolation` value is DERIVED from the observed backing, not… | ❌ | 0.0 | 0 | 1 | 1 | yes | hw294 |
| 102 | placement | LAYER1=vCluster treemap: every per-Org vCluster renders as its own labelled block, one block … | ✅ | 0.1276 | 0 | 1 | 1 | yes | hw294 |
| 103 | placement | A per-Org vCluster block contains ONLY that Organization's workloads — no cross-Org leakage i… | ✅ | 0.1276 | 0 | 1 | 1 | yes | hw294 |
| 104 | placement | The seven bootstrap components (grafana/harbor/keycloak/gitea/openbao/newapi/guacamole) rende… | ✅ | 0.6778 | 0 | 1 | 2 | yes | hw294 |
| 105 | placement | A per-app placement detail matches the treemap block the app renders in — the two surfaces ca… | ✅ | 0.6778 | 0 | 1 | 2 | yes | hw294 |
| 106 | placement | Every Organization owns EXACTLY ONE host namespace labelled `openova.io/organization=<slug>` … | ✅ | 0.1276 | 0 | 1 | 1 | yes | hw294 |
| 107 | placement | Deleting an Organization removes its vCluster StatefulSet — no orphaned vCluster survives the… | ✅ | 0.2295 | 0 | 1 | 2 | yes | hw294 |
| 108 | placement | Placement is read from RUNTIME (the observed pod/namespace), not from the Application CR's de… | ✅ | 0.6778 | 0 | 1 | 2 | yes | hw294 |
| 109 | sso | (a) the console `/settings` profile renders the owner with no second login; (b) if the Keyclo… | ❌ | 0.0 | 0 | 1 | 0 | yes | hw294 |
| 110 | sso | Gitea opens already signed in (avatar/menu shows the SSO user), repo list renders — no Gitea … | ☐ | 0.6671 | 0 | 1 | 1 | yes | hw294 |
| 111 | sso | Harbor opens signed in, the projects list renders — no Harbor login form, no gateway error page. | ❌ | 0.0 | 0 | 1 | 1 |  | hw294 |
| 112 | sso | Grafana opens signed in (no Grafana login), the home dashboard renders. | ☐ | 0.6671 | 0 | 1 | 1 | yes | hw294 |
| 113 | sso | The OpenBao UI renders signed in via OIDC — no manual token/unseal prompt blocking the landing. | ☐ | 0.6671 | 0 | 1 | 1 | yes | hw294 |
| 114 | sso | newapi opens signed in, its main console renders — no login form, no upstream-connect error. | ☐ | 0.6671 | 0 | 1 | 1 | yes | hw294 |
| 115 | apps | The Guacamole connections list is NON-EMPTY for a signed-in sovereign-admin — `guacamole_conn… | ❌ | 0.0 | 0 | 1 | 0 | yes | hw294 |
| 116 | orgs | Organizations directory: the page title / heading reads "Organizations", never "Tenants"/"SME… | ✅ | 0.686 | 0 | 1 | 2 | yes | hw294 |
| 117 | orgs | Directory org cards / list rows: column headers read "Organization / Kind / Tier / Billing / … | ✅ | 0.686 | 0 | 1 | 2 | yes | hw294 |
| 118 | orgs | Left-nav sidebar label for this section reads "Organizations", not "Tenants"/"SME". | ✅ | 0.686 | 0 | 1 | 2 | yes | hw294 |
| 119 | orgs | Create-organization flow: the form title, field labels, and submit button all say "Organizati… | ✅ | 0.686 | 0 | 1 | 2 | yes | hw294 |
| 120 | orgs | Organization-detail view: heading "Acme Corp", breadcrumb "← Organizations", field labels Slu… | ✅ | 0.686 | 0 | 1 | 2 | yes | hw294 |
| 121 | orgs | BSS / billing screen: billing is framed as "This organization is in showback mode…", zero "te… | ✅ | 0.086 | 0 | 1 | 1 | yes | hw294 |
| 122 | orgs | Legacy `/bss/tenants` URL (PR #3390 alias) resolves and redirects to `/organizations`, render… | ✅ | 0.686 | 0 | 1 | 2 | yes | hw294 |
| 123 | catalog | Handover URL → lands on `/dashboard` already signed-in as the owner (avatar E), no login form… | ✅ | 0.0898 | 0 | 1 | 1 | yes | hw294 |
| 124 | catalog | Catalog grid renders Blueprint cards in a tile grid, each with an icon + summary; the Alloy c… | ✅ | 0.686 | 0 | 1 | 2 | yes | hw294 |
| 125 | catalog | Click the Alloy card → the detail page renders: a hero (icon + name + summary + Edit IaC), an… | ✅ | 0.686 | 0 | 1 | 2 | yes | hw294 |
| 126 | catalog | Click the admin Edit affordance in the hero → an edit form drops INLINE into the detail page … | ✅ | 0.686 | 0 | 1 | 2 | yes | hw294 |
| 127 | catalog | In the inline form, change Summary to `RECONCILE-PROOF-<ts>` → Save → the page refreshes in p… | ✅ | 0.686 | 0 | 1 | 2 | yes | hw294 |
| 128 | catalog | Back on the grid, the Alloy card summary now reads `RECONCILE-PROOF-<ts>` — the edit propagat… | ✅ | 0.686 | 0 | 1 | 2 | yes | hw294 |
| 129 | catalog | Hard-reload the detail page → the summary is still `RECONCILE-PROOF-<ts>` — the edit persiste… | ✅ | 0.686 | 0 | 1 | 2 | yes | hw294 |
| 130 | catalog | The non-card `version` field renders as a `v1.0.1` chip in the hero, and Edit IaC exposes the… | ✅ | 0.686 | 0 | 1 | 2 | yes | hw294 |
| 131 | catalog | Note the current hero logo (the Alloy glyph) as the baseline before an icon edit. | ✅ | 0.686 | 0 | 1 | 2 | yes | hw294 |
| 132 | catalog | Click Edit → in the Light-theme icon field paste a distinct image → Save → a "Saved to IaC ✓"… | ✅ | 0.686 | 0 | 1 | 2 | yes | hw294 |
| 133 | catalog | Observe the hero — it now shows the new logo; the render reads the edited `card.iconLight` fi… | ✅ | 0.686 | 0 | 1 | 2 | yes | hw294 |
| 134 | catalog | Return to the grid — the Alloy card icon is now the new logo (the grid tile resolves the same… | ✅ | 0.686 | 0 | 1 | 2 | yes | hw294 |
| 135 | catalog | Reload the detail page — the hero is still the new logo (render reads the persisted IaC icon … | ✅ | 0.686 | 0 | 1 | 2 | yes | hw294 |
| 136 | catalog | Click Edit again — the Light-theme icon field shows the current IaC value, falling back to th… | ✅ | 0.686 | 0 | 1 | 2 | yes | hw294 |
| 137 | catalog | Click Edit → click the icon picker (`iconpicker-*`) → a thumbnail grid of vendored `component… | ✅ | 0.686 | 0 | 1 | 2 | yes | hw294 |
| 138 | catalog | Click `cilium.svg` in the picker grid → the icon field + a live preview swatch update to the … | ✅ | 0.686 | 0 | 1 | 2 | yes | hw294 |
| 139 | catalog | Save → reload — the hero is now the Cilium logo (the picker selection persisted to IaC and re… | ✅ | 0.686 | 0 | 1 | 2 | yes | hw294 |
| 140 | catalog | On Save, the durable-commit verdict (git outcome) is surfaced in-UI (the Edit-IaC `managed-by… | ✅ | 0.686 | 0 | 1 | 2 | yes | hw294 |
| 141 | catalog | Hover the summary line in the hero → a pencil/edit affordance appears ON the field (`cif-summ… | ✅ | 0.686 | 0 | 1 | 2 | yes | hw294 |
| 142 | catalog | Click the summary → type a value → Save → only the summary updates in place; no full-form mod… | ✅ | 0.686 | 0 | 1 | 2 | yes | hw294 |
| 143 | catalog | Repeat the inline edit for the name field (`cif-name-edit` → `cif-name-input`) — it edits in … | ✅ | 0.686 | 0 | 1 | 2 | yes | hw294 |
| 144 | catalog | Click Edit IaC (`catalog-detail-ed… admin only) → the full `blueprint.yaml` opens in the YAML… | ✅ | 0.686 | 0 | 1 | 2 | yes | hw294 |
| 145 | catalog | Change a field in the editor → Commit → a Show-diff Current/Proposed side-by-side renders the… | ✅ | 0.686 | 0 | 1 | 2 | yes | hw294 |
| 146 | catalog | The editor subtitle states it directly: "Commit writes the IaC source of truth; Flux reconcil… | ✅ | 0.686 | 0 | 1 | 2 | yes | hw294 |
| 147 | catalog | Open the WordPress detail page → Edit → change Summary → Save → reload → the summary persists… | ✅ | 0.686 | 0 | 1 | 2 | yes | hw294 |
| 148 | catalog | WordPress: Edit IaC → edit `spec.manifests` → Commit → reload — the same YamlEditor edits a s… | ✅ | 0.686 | 0 | 1 | 2 | yes | hw294 |
| 149 | catalog | WordPress: Edit → Light-theme icon → distinct image → Save → reload — the WordPress hero icon… | ✅ | 0.686 | 0 | 1 | 2 | yes | hw294 |
| 150 | catalog | PostgreSQL detail renders the SAME edit surface (hero icon, Edit IaC, clickable cards) and Ed… | ✅ | 0.686 | 0 | 1 | 2 | yes | hw294 |
| 151 | catalog | Alloy + Postgres render the IDENTICAL edit chrome (same cif-icon-edit`/`cif… same Edit-IaC Ya… | ✅ | 0.686 | 0 | 1 | 2 | yes | hw294 |
| 152 | catalog | The catalog detail page renders (hero · About · Instances) and opens an INLINE Edit form (no … | ✅ | 0.686 | 0 | 1 | 2 | yes | hw294 |
| 153 | catalog | A summary edit Saves, updates the page AND the grid card, and persists across a reload — acce… | ✅ | 0.686 | 0 | 1 | 2 | yes | hw294 |
| 154 | catalog | A non-card field edit (`version`) persists — the whole CR is editable, not a 7-field overlay … | ✅ | 0.686 | 0 | 1 | 2 | yes | hw294 |
| 155 | catalog | The edited icon renders on hero + grid + survives reload; the form pre-fills the IaC icon; th… | ✅ | 0.686 | 0 | 1 | 2 | yes | hw294 |
| 156 | catalog | Save's verdict is backed by a REAL commit — resolvable in the Organization's Gitea IaC repo w… | ✅ | 0.686 | 0 | 1 | 2 | yes | hw294 |
| 157 | catalog | Per-field inline edit for cards (`cif-*`) + the full-CR YamlEditor (Edit IaC) for the rest, b… | ✅ | 0.686 | 0 | 1 | 2 | yes | hw294 |
| 158 | catalog | The identical edit mechanism works on a 2nd + 3rd blueprint — no per-blueprint UI (Alloy + Po… | ✅ | 0.686 | 0 | 1 | 2 | yes | hw294 |
| 159 | cutover | Settings → Sovereignty section renders a "Cluster sovereignty" panel with a "TETHERED" badge … | ✅ | 0.686 | 0 | 1 | 2 | yes | hw294 |
| 160 | cutover | Console nav + Settings sidebar expose a dedicated "Sovereignty" anchor (`#sovereignty`) that … | ✅ | 0.0996 | 0 | 1 | 2 | yes | hw294 |
| 161 | cutover | Open `/jobs` (zero-login, signed in as owner) → the canvas table renders a populated activity… | ✅ | 0.686 | 0 | 1 | 2 | yes | hw294 |
| 162 | cutover | Find the `cutover` group row and expand it → it renders the 11 `cutover-step-*` rows (gitea-m… | ❌ | 0.0 | 0 | 1 | 1 | yes | hw294 |
| 163 | cutover | Each `cutover-step-*` row reads an honest per-step status (Succeeded / Running / Failed / Pen… | ✅ | 0.0996 | 0 | 1 | 2 | yes | hw294 |
| 164 | cutover | The cutover group status reflects its real children (a group with a failed child reads failed… | ✅ | 0.0495 | 0 | 1 | 1 | yes | hw294 |
| 165 | cutover | Every `cutover-step-*` row on the Jobs page renders its actions cell PRESENT and deliberately… | ✅ | 0.3193 | 0 | 1 | 1 | yes | hw294 |
| 166 | cutover | (After a COMPLETE cutover) the `cutover` group reads all-11-green on `/jobs` — every step Suc… | ❌ | 0.0 | 0 | 1 | 1 | yes | hw294 |
| 167 | jobs | Open the console root in a fresh tab → land on the operator dashboard signed in as the sovere… | ✅ | 0.6863 | 0 | 1 | 2 | yes | hw294 |
| 168 | jobs | Open `/jobs` → the canvas table renders a populated list of activity rows (not a spinner, emp… | ✅ | 0.6863 | 0 | 1 | 2 | yes | hw294 |
| 169 | jobs | The Kind column is present in the header and each row shows its kind; full header = Name·Kind… | ✅ | 0.6863 | 0 | 1 | 2 | yes | hw294 |
| 170 | jobs | Scroll/search to the `install-openbao` row → it renders green / Succeeded (the install is hon… | ✅ | 0.6863 | 0 | 1 | 2 | yes | hw294 |
| 171 | jobs | Every rendered row maps to a real HelmRelease install / terraform stage (no placeholder, no s… | ✅ | 0.6863 | 0 | 1 | 2 | yes | hw294 |
| 172 | jobs | Set the Status filter to `failed` → the table shows the genuinely-failing rows, each with an … | ❌ | 0.0 | 0 | 1 | 1 | yes | hw294 |
| 173 | jobs | Leave the table on screen ~30s → rows update live (tail) as reconciliation progresses; a stat… | ✅ | 0.6863 | 0 | 1 | 2 | yes | hw294 |
| 174 | jobs | On a Failed row (Status=failed), a Re-run / Retry-reconcile button is present on the row (vis… | ✅ | 0.6124 | 0 | 1 | 2 | yes | hw294 |
| 175 | jobs | On a Succeeded / healthy / Confirming row, NO Re-run button renders — the control is gated to… | ✅ | 0.6863 | 0 | 1 | 2 | yes | hw294 |
| 176 | jobs | Click Re-run on a Failed row → a success toast/feedback appears and the button flips in place… | ❌ | 0.0 | 0 | 1 | 0 | yes | hw294 |
| 177 | jobs | Use the same Re-run button on a Failed row — one remediation mechanism across rows, no per-ki… | ✅ | 0.0784 | 0 | 1 | 1 | yes | hw294 |
| 178 | meta | The signed handover URL → lands directly on `/dashboard` signed-in (env switcher shows the li… | ✅ | 0.0898 | 0 | 1 | 1 | yes | hw294 |
| 179 | meta | Click the avatar (top-right) → menu reads "Signed in as the owner" with a Sign-out item — con… | ✅ | 0.6863 | 0 | 1 | 2 | yes | hw294 |
| 180 | meta | Grafana bare URL → lands on Grafana Home ("Welcome to Grafana", full UI, Profile avatar), `?o… | ✅ | 0.6863 | 0 | 1 | 2 | yes | hw294 |
| 181 | meta | Harbor (registry) bare URL → lands on `/harbor/projects` (projects, repos, Administration nav… | ✅ | 0.6863 | 0 | 1 | 2 | yes | hw294 |
| 182 | meta | Gitea bare URL → lands on the gitea dashboard titled "emrah.baysal - Dashboard - Catalyst Git… | ✅ | 0.6863 | 0 | 1 | 2 | yes | hw294 |
| 183 | meta | OpenBao bare UI → final rendered screen is the authenticated Vault session (`/ui/vault/secret… | ✅ | 0.0855 | 0 | 1 | 1 | yes | hw294 |
| 184 | meta | The frozen denominator is INTACT and no clause changes silently: `docs/ledger/UAT.md` holds e… | ✅ | 0.1297 | 0 | 1 | 1 |  | hw294 |
| 185 | meta | No ✅ row cites evidence from a wiped env — the ledger never presents a dead env's artifact as… | ✅ | 0.8015 | 2 | 5 | 2 |  | hw294 |
| 186 | mcp | **bp-openova-mcp** answers a JSON-RPC 2.0 `tools/list` over HTTPS with a NON-EMPTY tool set f… | ✅ | 0.6863 | 0 | 1 | 2 | yes | hw294 |
| 187 | topology | Per-app Topology for a multi-region app (grafana) shows **Pattern: active-active** with 2 PRI… | ❌ | 0.0 | 0 | 1 | 1 | yes | hw294 |
| 188 | topology | A genuine single-region app (catalyst-api) correctly shows **singleton** (no false multi-regi… | ❌ | 0.0 | 0 | 1 | 0 |  | hw294 |
| 189 | topology | Region-b kubeconfig self-heals EIP→private-IP **zero-touch** on restart (topology survives a … | ❌ | 0.0 | 0 | 1 | 1 |  | hw294 |
| 190 | jobs | `/jobs` lists **ONLY finite jobs** (provision/cutover steps, batch Jobs, CronJob runs) — ZERO… | ✅ | 0.6863 | 0 | 1 | 2 | yes | hw294 |
| 191 | jobs | Continuous reconcilers (HelmRelease/Kustomization) do **NOT** appear in `/jobs` (they live in… | ✅ | 0.6863 | 0 | 1 | 2 | yes | hw294 |
| 192 | recon | The convergence **Reconciliation** link opens the cloud **RECON lens** (`view=graph&lens=reco… | ✅ | 0.0934 | 0 | 1 | 1 | yes | hw294 |
| 193 | recon | Clicking a reconciler opens the **ArgoCD-like management surface** (drill-in). | ✅ | 0.6863 | 0 | 1 | 2 | yes | hw294 |
| 194 | recon | Recon surface lists Flux reconcilers with live status (Reconciled/Reconciling/Degraded/Suspen… | ✅ | 0.6863 | 0 | 1 | 2 | yes | hw294 |
| 195 | recon | Drill a reconciler → its controller **logs** render. | ✅ | 0.0934 | 0 | 1 | 1 | yes | hw294 |
| 196 | recon | **Reconcile** action → `reconcile.fluxcd.io/requestedAt` lands on the live object. | ✅ | 0.6778 | 0 | 1 | 2 | yes | hw294 |
| 197 | recon | **Suspend/Resume** → `spec.suspend` flips on the live object. | ❌ | 0.0 | 0 | 1 | 1 | yes | hw294 |
| 198 | cloud | `/cloud` per-kind **helmreleases** page shows the real count (~65), not 0. | ✅ | 0.6855 | 0 | 1 | 2 | yes | hw294 |
| 199 | cloud | Cloud **Gateway** page shows the live cilium Gateways (2: `cilium-gateway` + `cilium-gateway-… | ✅ | 0.6855 | 0 | 1 | 2 | yes | hw294 |
| 200 | cloud | Cloud **HTTPRoutes** page shows 15. **[expected count re-baselined 2026-07-30 by a prior env]… | ✅ | 0.6855 | 0 | 1 | 2 | yes | hw294 |
| 201 | cloud | Cloud **NetworkPolicies** page shows the live policies (30 live; expected ~10 stale). | ✅ | 0.6855 | 0 | 1 | 2 | yes | hw294 |
| 202 | cloud | Cloud **CiliumNetworkPolicies** page shows the live policies (42 live; expected 5 stale). | ✅ | 0.6855 | 0 | 1 | 2 | yes | hw294 |
| 203 | cloud | Cloud **Load Balancers** page shows the real LoadBalancer Svc (clustermesh-apiserver EXTERNAL… | ✅ | 0.6855 | 0 | 1 | 2 | yes | hw294 |
| 204 | cloud | Cloud **Worker Nodes** page shows the real nodes, not 0. | ✅ | 0.6855 | 0 | 1 | 2 | yes | hw294 |
| 205 | fleet | `/fleet/applications` returns the real app count (non-zero), not 0. | ✅ | 0.6855 | 0 | 1 | 2 | yes | hw294 |
| 206 | adoption | `kubectl get managed` is non-empty — Crossplane observes the OpenTofu-built infra. | ❌ | 0.0 | 0 | 1 | 1 |  | hw294 |
| 207 | adoption | A `CloudAdoption` for the real ELB reaches Synced+Ready (Observe), `external-name` = the real… | ❌ | 0.0 | 0 | 1 | 1 |  | hw294 |
| 208 | adoption | Adoption is **Observe-only** — the live ELB/nodes are untouched (no re-provision). | ❌ | 0.0 | 0 | 1 | 1 |  | hw294 |
| 209 | storage | PVCs land on a real **CSI storageclass** (not k3s local-path). | ✅ | 0.8012 | 2 | 5 | 2 |  | hw294 |
| 210 | storage | `local-path` is **FORBIDDEN** (k3s `--disable=local-storage`). | ✅ | 0.8012 | 2 | 5 | 2 |  | hw294 |
| 211 | mcp | MCP **sovereign-admin** token → `list_applications` returns all apps. | ✅ | 0.3011 | 0 | 1 | 2 | yes | hw294 |
| 212 | mcp | MCP **Org-scoped** token → `list_applications` returns ONLY that Org's apps (RBAC parity). | ✅ | 0.0468 | 0 | 1 | 1 | yes | hw294 |
| 213 | mcp | MCP cross-Org `get_application` is REFUSED as **not found** — `-32000`, and the message names… | ❌ | 0.0 | 0 | 1 | 0 | yes | hw294 |
| 214 | orgs | No `SME`/`tenant` banned-term leak in the console bundle (org-rename complete). | ✅ | 0.6855 | 0 | 1 | 2 | yes | hw294 |
| 215 | orgs | CRD `tier` enum is `[org, corporate]` — a `tier: org` Organization is accepted. | ✅ | 0.6855 | 0 | 1 | 2 | yes | hw294 |
| 216 | e2e-journey | A user with a **coupon/voucher code** redeems it → creates **his Organization**. Surface: `ht… | ✅ | 0.0597 | 0 | 1 | 1 | yes | hw294 |
| 217 | e2e-journey | The user logs in via the **passwordless SSO magic-email PIN** (any domain, e.g. `demo@openova… | ✅ | 0.0597 | 0 | 1 | 1 | yes | hw294 |
| 218 | e2e-journey | **chepherd is installable from the catalog as an Application** (`bp-agenity` **Helm chart**).… | ❌ | 0.0 | 0 | 1 | 0 | yes | hw294 |
| 219 | e2e-journey | The user **provisions chepherd** as an Application → it converges (Helm deploys, pods Ready, … | ❌ | 0.0 | 0 | 1 | 0 | yes | hw294 |
| 220 | e2e-journey | chepherd's **solo agent is pre-configured with Claude Opus 4.7** + a working token (the agent… | ❌ | 0.0 | 0 | 1 | 0 | yes | hw294 |
| 221 | e2e-journey | The user **chats with the chepherd solo agent** ("create a `<blueprint>` app in my org") → th… | ❌ | 0.0 | 0 | 1 | 0 | yes | hw294 |
| 222 | e2e-journey | The agent-created application **converges and appears in the user's Org** (chat-driven app cr… | ❌ | 0.0 | 0 | 1 | 0 | yes | hw294 |
| 223 | e2e-journey | The chepherd agent's actions are **RBAC-scoped to the user's Org** (UI-parity): the openova-m… | ❌ | 0.0 | 0 | 1 | 0 | yes | hw294 |
| 224 | convergence | Per-Org `bp-openclaw` controller reaches Ready (1/1) on a Sovereign — the idle-reaper can lis… | ✅ | 0.3011 | 0 | 1 | 2 | yes | hw294 |
| 225 | convergence | Per-Org `bp-newapi` HR reaches Ready on a fresh Org — the admin-promote post-install hook Com… | ✅ | 0.0752 | 0 | 1 | 1 | yes | hw294 |
| 226 | funnel | The customer's PURCHASED app actually RUNS — every funnel Org's `tenant-<slug>-apps` Kustomiz… | ✅ | 0.6855 | 0 | 1 | 2 | yes | hw294 |
| 227 | delivery | A POST-cutover Sovereign REFUSES a GitHub-side catalog bump: the Blueprint CR does NOT move w… | ❌ | 0.0 | 0 | 1 | 1 |  | hw294 |
| 228 | delivery | A **re-prov AFTER a wipe** does NOT false-fail on orphaned `catalyst-*` VPCs — the orphan-VPC… | ❌ | 0.0 | 0 | 1 | 0 | yes | hw294 |
| 229 | delivery | **bootstrap-kit Kustomization Ready=True + plane-isolation admits CNPG→kube-apiserver egress*… | ❌ | 0.0 | 0 | 1 | 2 |  | hw294 |
| 230 | delivery | **`ghcr-pull` reflects into a bare-slug per-Org namespace** — the emberstack reflector copies… | ✅ | 0.6855 | 0 | 1 | 2 | yes | hw294 |
| 231 | delivery | **bp-postgres singleton render carries the operator-probe NetworkPolicy** — a singleton (non-… | ✅ | 0.8012 | 2 | 5 | 2 |  | hw294 |
| 232 | apps | On a fresh funnel Org, **openclaw `/readyz` returns 200** — the 4-layer chain (controller ima… | ❌ | 0.0 | 0 | 1 | 1 | yes | hw294 |
| 233 | apps | On a fresh funnel Org, **WordPress serves HTTP 200** — the mysql DB pod is Guaranteed-QoS (re… | ✅ | 0.0702 | 0 | 1 | 1 | yes | hw294 |
| 234 | apps | On a funnel Org **that provisioned Stalwart mail** (cart includes `stalwart-mail`, or a mail-… | ❌ | 0.0 | 0 | 1 | 0 |  | hw294 |
| 235 | apps | On a fresh prov, **grafana SSO logs in 3/3 → 200** — the bp-sso-bridge per-tick credential re… | ❌ | 0.0 | 0 | 1 | 1 | yes | hw294 |
| 236 | apps | On a fresh multi-region prov, **region-B keycloak reaches Ready** — the cross-region shared-p… | ❌ | 0.0 | 0 | 1 | 1 |  | hw294 |
| 237 | spine | **spine Application → Continuum round-trip is Healthy + `status.continuumRef` is populated** … | ❌ | 0.0 | 0 | 1 | 1 | yes | hw294 |
| 238 | postgres | **Per-Org postgres-in-vCluster → host-side CNPG Ready** — a per-Org Postgres requested inside… | ✅ | 0.0572 | 0 | 1 | 1 | yes | hw294 |
| 239 | adoption | **Crossplane adoption populates Observe-only on the fresh prov** — `providers.pkg.crossplane.… | ❌ | 0.0 | 0 | 1 | 1 |  | hw294 |
| 240 | gateway-4706 | No-nodePort gateway serves externally on a FRESH zero-touch prov: ELB → node:443 hostPort → h… | ✅ | 0.8012 | 2 | 5 | 2 |  | hw294 |
| 241 | gateway-4706 | A `ready` deployment record's console health MATCHES the live front door: `GET /sovereign/api… | ❌ | 0.0 | 0 | 1 | 0 |  | hw294 |
| 242 | gateway-4706 | 2-region BCP admission floor: a 1-region POST with no explicit bcpTopology is rejected at the… | ✅ | 0.1963 | 0 | 1 | 1 | yes | hw294 |
| 243 | gateway-4706 | Tenant DNS split-horizon on a fresh prov: wildcard hosts → primary ELB EIP, console → console… | ✅ | 0.8012 | 2 | 5 | 2 |  | hw294 |
