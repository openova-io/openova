# status/uat backlog — live-evidence walk log (2026-07-25, hw288 `027f07559af1f9f7`)

Each open status/uat issue walked live this session (curl / Playwright / kubectl on the real surface). Infra issues (#5285/#5328/#5265/#5339) have no numbered UAT row — their evidence lives here + on the issue.

| Issue | Live walk + evidence | Verdict |
|---|---|---|
| **#5352** OOM | catalyst-api `/healthz` 200, **0 restarts / 7h**, 625Mi stable (was 28 restarts→4Gi); fix image `e4b36a5` | **CLOSED** (runtime-verified) |
| **#5341** gateway flake | `mcp.hw288/mcp` ×12 → 7/12 `404`(envoy) / 5/12 `405`(app); openbao 404 hdr `server: envoy`; console 0/6 clean; browser openbao → `ERR_HTTP_RESPONSE_CODE_FAILURE` | fix #5354 merged, **delivery-gated #5265** |
| **#5345** catalog chartless | console `/catalog/bp-redis` renders installable (v1.0.0, "+ New Instance" ×2, topologies) but ghcr = NO-PACKAGE; screenshot captured | gap real; publish-or-hide the 5 |
| **#5265** delivery gap | On hw288 the mirror/prewarm ran once + no Day-2 reconciler is deployed → local Gitea/Harbor frozen (region-A bp-cilium 1.4.16 vs ghcr 1.4.17). **CORRECTED:** the Day-2 fix EXISTS — `12-daytwo-harbor-pin-reconciler` Deployment, merged `5daf918a4`/#5301 on 2026-07-23T01:26Z — but hw288's cutover pin (14:26Z) predates it, so hw288 never got it | **fix MERGED (#5301); validate on a fresh prov** (hw288 is a pre-fix env) |
| **#5339** region drift | region-A `vpc-podcidr-route-reconciler` = CronJob (`*/5`, 45h, pre-fix) vs region-B = Deployment (1/1, 33h, #5340) — stable split | root-caused → **#5359** |
| **#4901** Continuum | `continuums.dr.openova.io` shows 8 CRs incl cnpg-pair (Healthy); status surfaces `hotStandbyAbsent`/`standbyAvailable`/`replicationLagSeconds` + `StandbyAvailable=True` | gap **appears addressed** (self-corrected a short-name mis-read) |
| **#5285** watch-informer flood | catalyst-api `/healthz` 200, 0 restarts, 4 deps served, not starved; + 3 dedicated tests | code+test+runtime ✓, 1 failed-dep scenario left |
| **#5328** break-glass | script is in **open review-gated PR #5356** (not on main) — self-test can't run from main | review-gated |

**Filed this session:** #5358 (guacamole SSO nonce-race), #5359 (Pillar-5: cutover pivots only region-A; region-B Flux still on github/ghcr).

**Common blocker:** #5265 (frozen post-cutover catalog on hw288) gates #5341/#5358/#5339 delivery. The Day-2 fix is already MERGED (#5301, `12-daytwo-harbor-pin-reconciler`); hw288 predates it. Highest-leverage = a **fresh prov** whose bootstrap-kit pin includes #5301 — it carries the Day-2 reconciler (which then rolls the delivery-gated fixes) + must also cover region-B (#5359, still un-pivoted).

## Live surface reachability snapshot — 2026-07-25 (#5341 blast-radius)

Console-gateway = clean by design; shared-gateway = #5341 wildcard-SNI flake. 8 curls each:

| Surface | Gateway | envoy-404 |
|---|---|---|
| console/marketplace/auth | console | 0/0/0 of 8 (clean) |
| api (bare `/`) | console | 8/8 — but this is a **normal no-root-route 404** (catalyst-api serves `/api/v1/*`, not `/`); the real path `/api/v1/fleet/applications` returns **401** unauth (→200 in-browser, row 205). api is REACHABLE, NOT flaking. |
| grafana | shared | 0/8 |
| gitea | shared | 0/8 |
| hubble | shared | 0/8 |
| newapi | shared | 0/8 |
| harbor | shared | 4/8 |
| openbao | shared | 8/8 |
| powerdns-admin | shared | 8/8 |
| mcp /mcp | shared | 4/8 |

Confirms #5341 is non-deterministic per-subdomain (openbao/powerdns tend 8/8 down, grafana/gitea/hubble/newapi/harbor intermittent). Fix #5354 delivery-gated on #5265 (hw288 pre-fix env).

## Live re-hit — 2026-07-25 17:42Z (#5339 / #5285 / #5341)

- **#5339** vpc-podcidr-route-reconciler: present in both regions (region-A CronJob / region-B Deployment split stable — fix #5340 delivery-gated, hw288 predates).
- **#5285** catalyst-api `/healthz` → **HTTP/1.1 200 OK**, restarts=0 (quarantine fix holding; not starved).
- **#5341** `mcp.hw288/mcp` → **4/8 envoy-404** (shared-gateway flap persists; fix #5354 delivery-gated on #5265).

Neither #5339 nor #5285 has a numbered UAT row (infra/prov-lifecycle issues) — this log is their evidence home. #5341's UAT rows are 32 (✅ via harbor SSO) + 33/36 (☐, front doors down).

## MAJOR reclassification — customer-Org funnel is SELF-SERVICE, not founder-gated (2026-07-25)

Applied the 33/36 lesson (don't assume gated) to the ~24 "customer-Org founder-gated" rows and it broke open:
- Console **Billing → Vouchers → "+ Issue voucher"** is owner-self-service. Minted **VCH-W376HVPSMZYN (50 OMR, Active)** via the BSS UI.
- `marketplace.hw288.omani.works/redeem/?code=VCH-W376HVPSMZYN` → **"VOUCHER VALID — 50 OMR credit"** + the full funnel wizard (Plan → Stack → Add-ons → Topology → Review → Checkout).

So the customer-Org rows (5-12/16/20/23/84-90/226/R15/R17/G2/G7 + funnel apps) are **walkable by the owner** — no founder voucher/payment needed. This was an untested assumption carried all session; the marketplace voucher-redeem funnel creates a customer Org self-service. **In-progress:** walking the wizard → signup → provision → then the customer-Org row family. (The one genuine dependency to verify: the funnel signup PIN goes to the stranger email — needs a readable mailbox to complete checkout.)

## 🎉 CUSTOMER ORG CREATED via self-service funnel — 2026-07-25 19:44Z

Walked the full Pillar-1 funnel end-to-end (owner-self-service, NO founder action):
1. **Voucher issued** (VCH-W376HVPSMZYN, 50 OMR) via console BSS.
2. **Marketplace redeem** → wizard: Plans (S) → Apps (WordPress+BookStack) → Add-ons → Review → Checkout.
3. **Row 84** ✓ — checkout: entered `emrah.baysal+acme@openova.io` (plus-addressed "stranger") → "Send sign-in code" → read PIN from mailbox (subject "Your login code", plus-addressing delivered to owner mailbox) → verified (POST /api/auth/verify 200).
4. **Row 85** ✓ — voucher VCH-W376HVPSMZYN applied at checkout (50 OMR credit).
5. **Row 86** ✓ (launching) — Org "Acme Corp" (subdomain `acme-corp`) → "Launch my Organization" → `/launching/?host=https://console.acme-corp.omani.rest&next=/jobs&tenant=74fa694c-0552-4bd1-8e39-5ba92fd817df` "Setting up your console".

**Customer Org `acme-corp` (tenant 74fa694c-0552-4bd1-8e39-5ba92fd817df) is provisioning** at console.acme-corp.omani.rest. Screenshot: docs/sessions/2026-07-25/screenshots/funnel-acme-org-launching.png. This un-gates the customer-Org row family (5-12/16/20/23/87-90/226/R15/R17/G2/G7/120) — to walk once the vcluster + apps reach Ready. **This proves the ~24 "founder-gated" rows were self-service all along.**

## Row 16 (customer app topology) — blocked by acme-corp per-Org convergence lag (2026-07-25 22:11Z)

Re-attempted row 16 twice (21:41 + 22:11). console.acme-corp.omani.rest/app/wordpress → "App not found — the component wordpress is not part of this deployment", env CONVERGING, ~50m after Org create. The app IS running (wordpress+mysql pods 1/1) and shows INSTALLED in /apps, but the per-app **detail view** can't resolve it because the Org's convergence conditions are still False: `IdentityProviderConfigured=False, IdentityProviderClaimMappersConfigured=False, IacRepoBootstrapped=False, PerOrgRealmProvisioned=False` (only `Ready=True`). So row 16 (Settings/Topology change) + any per-Org-SSO rows are gated on the per-Org realm/IaC-repo bootstrap completing — a platform convergence step, not a walk I can force. The /apps-list-vs-/app-detail inconsistency (INSTALLED vs "not part of deployment") is itself worth a look — the two views read from different sources while IacRepoBootstrapped=False.

## CORRECTION — acme-corp per-Org realm/IaC are DISABLED (not lagging) — 2026-07-25 22:12Z

Root-caused the row-16 block (correcting the "convergence lag" note above). The Organization CR status shows the two gating conditions are **deliberately Disabled** in this controller deployment, not slow:
- `PerOrgRealmProvisioned` → `{"state":"Disabled","lastError":"per-Org Keycloak realm auto-wiring disabled in this controller deployment"}`
- `IacRepoBootstrapped` → `reason=BootstrapDisabled: iacbootstrap dependencies not wired`

So the per-Org Keycloak realm + the per-Org IaC repo bootstrap are **feature-flagged OFF** on this hw288 controller. Consequences: the per-app **detail view** (`/app/<name>`) can't resolve the running app (reads from the IaC deployment source), so **row 16** (Settings/Topology change) is unwalkable here; likewise any **per-Org-realm SSO** rows and **G2** (per-Org newapi ES-sync into a fresh per-Org realm). This is a controller-deployment config state, not a walk I can force — the customer Org's core (vcluster + apps + directory + detail-in-operator-console) is green (rows 5-12/20/87-90/120/226/233 ✅), but per-Org-realm-dependent rows need the flag enabled (or a controller that has it wired). Worth confirming with the founder whether this flag is intended-off on hw288 or a gap.
