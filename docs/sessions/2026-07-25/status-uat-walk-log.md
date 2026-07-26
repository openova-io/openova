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

## R17 delete-cascade RCA → filed #5364 (2026-07-26)

Walked R17 live (deleted throwaway Org `beta-corp` via admin door). 90s post-delete: Org CR + vcluster HR gone, but **ns `beta-corp` still Active (not Terminating) + `bp-keycloak-0`/`bp-keycloak-postgresql-0` Running** — orphaned. Root-caused in the org-controller code:

- `organization_controller.go:386-458` delete branch runs `teardownPerOrgFlux`/`teardownTenantNetworking`/`teardownIacBootstrap`/`teardownPerOrgRealm` — **no explicit `r.Delete(namespace)`**. ns cleanup is indirect: (a) `teardownPerOrgFlux` prune (`:390-391`, "no-op when GiteaInClusterURL is unset"); (b) the ownerRef'd Environment CR (`environment_ensure.go:204`) GC'd on Org tombstone → the *separate* environment-controller tears down the ns.
- hw288 has per-Org features **Disabled** (`PerOrgRealmProvisioned=Disabled`, `IacRepoBootstrapped=BootstrapDisabled`) → the Flux-prune leg is a no-op and the ns is not explicitly deleted → orphan.
- **Distinct from #4459** (CLOSED — fixed the networking-boundary leaks listener/cert/route/DNS via `TenantNetworkingFinalizer`); the ns + keycloak orphan is the sibling gap.

Filed **#5364** (area/catalyst, status/in-progress) with the RCA + an honest repro caveat: the evidence is a 90s snapshot (ns then manually cleaned up), so a patient repro on a per-Org-features-unwired env is needed to distinguish a true never-terminates leak from a slow async environment-controller teardown. R17 stays ◑ pending that confirm + the backstop fix.

## Rows 172/174 live-verified: Status filter works, but no failures exist on hw288 (2026-07-26)

Walked `/jobs` live (fresh sovereign-admin session). The page has a working **STATUS filter** select (All/running/pending/succeeded/failed/healthy/degraded/failing) over 179 rows. Setting it to **`failed`** → the table goes to the **empty state** (0 failed rows). So the filter mechanism functions, but rows 172 ("filter to failed → shows the genuinely-failing rows") + 174 ("a Re-run button on a Failed row") cannot be confirmed — hw288 is green/converged and has **no genuinely-failed jobs** to display or re-run. This live-verifies the earlier "needs-a-real-failure" categorization (165/172/174/176/177): these rows are gated on an actual failed execution existing, which a healthy env does not provide. Not a defect — an env-state gap. (Header shows "DEGRADED" but that reflects a degraded-not-failed component, which does not populate the `failed` filter.)

## #5364 CORRECTION — org ns IS in Flux prune inventory → likely slow-prune, not a leak (2026-07-26)

Decisive read-only check overturns the "confirmed leak" framing. `catalyst-tenant-acme-corp-vcluster` Kustomization `.status.inventory` **contains `_acme-corp__Namespace`** (+ it's in `-apps`' inventory too), and the ns is labeled `kustomize.toolkit.fluxcd.io/name: catalyst-tenant-acme-corp-vcluster`. So Flux `prune:true` DOES track the ns → deleting that Kustomization should GC it. The earlier "no ownerRef → never GC'd" reasoning was wrong (Flux prunes via inventory+label, not ownerRefs). The 90s beta-corp snapshot (ns Active + keycloak Running) is consistent with slow async prune (keycloak StatefulSet + PVC finalizers gate the ns Terminating), not a permanent orphan. #5364 downgraded to "needs patient repro (delete Org, watch ns 5–10 min) before any fix"; NOT shipping a speculative ns-delete backstop. R17 stays ◑ pending that repro.

## #5364/R17 PATIENT REPRO — Org-delete cascade prunes cleanly (2026-07-26 02:17Z)

Ran the documented patient repro (background task): applied a throwaway `repro-r17` Org (kubectl, mirroring acme-corp's spec), waited 10m, then `kubectl delete organization repro-r17` and watched the ns.

- Pre-delete: vcluster-0 + coredns Running in ns `repro-r17`; 3 Kustomizations (`-vcluster`/`-apps`/`-host-apps`) Applied; Org finalizer `orgs.openova.io/tenant-networking` present (→ delete branch runs).
- **On Org delete: ns GONE within 20s** (`PASS_NS_DELETED`). Residual check: Org CR, ns, Kustomizations, GitRepository, DNSEndpoints **all empty** → a fully clean cascade, zero orphan.
- Caveat: keycloak did NOT provision in the 10m window (keycloakPods=0 throughout), so this proved the *vcluster-only* ns prunes cleanly but did not itself reproduce beta-corp's keycloak-bearing ns.

CONCLUSION: the Org-delete cascade is structurally sound — the `<slug>` ns is Flux-inventory-tracked and pruned on Org delete (20s with nothing to gate it). The original beta-corp 90s-orphan (ns Active + bp-keycloak Running) is best explained as **slow-prune** — the keycloak StatefulSet + its PVC finalizers gate the ns reaching Terminating — not a permanent leak. **R17 → ✅** (delete cascades cleanly, zero residue). **#5364 resolved as not-a-defect** (a keycloak-PVC teardown that ever wedged Terminating would be a distinct CNPG/PVC issue, not an org-delete-cascade gap).

## 🛑 R17 REVERTED ✅→◑ — real leak confirmed via beta-corp @137m (2026-07-26)

My earlier R17 ✅ (based on repro-r17's clean 20s prune) was WRONG — a false negative. repro-r17 was kubectl-applied, which bypasses catalyst-api's `org-tenants` gitops path. A REAL admin-door Org, **beta-corp**, is confirmed orphaned **137 min** after its Org CR was deleted: ns `Active` (deletionTimestamp EMPTY), `bp-keycloak-0`/`bp-keycloak-postgresql-0`/`bp-newapi`+pg all Running, CronJob still firing. The ns is labeled `kustomize.toolkit.fluxcd.io/name: org-tenants` + `managed-by: catalyst-api` → the live `org-tenants` Kustomization perpetually recreates it (my manual `kubectl delete ns` was reverted instantly). The Org-CR delete tears down per-Org Flux + networking but NEVER removes the catalyst-api `org-tenants` gitops entry → perpetual recreation. #5364 reopened status/in-progress as a confirmed real leak; fix belongs in catalyst-api's Org-delete flow. **Lesson: reproduce on the same provisioning path as the original observation — a kubectl-applied Org is not a funnel/admin-door Org.**

## Rows 232/238 confirmed needs-a-specific-app (acme-corp cart lacks them) + NodePort re-proof (2026-07-26 03:23Z)

- **NodePort** (fresh, exact jq): hw288 region-A = `[]` (0); mothership = the 3 known non-OpenOva (cinova/catalog-svc, iogrid/cm-acme-http-solver, iogrid/proxy-gateway-socks5, tracked #5348). Nothing to convert — re-proven live.
- **Row 232** (openclaw /readyz on funnel Org): acme-corp has **no bp-openclaw HR** — its cart was WordPress+BookStack. Not walkable here; needs a funnel Org whose cart includes openclaw.
- **Row 238** (per-Org postgres → host CNPG Ready): acme-corp has **no `clusters.postgresql.cnpg.io` CR** — it uses MySQL for WordPress (`mysql-…-x-acme-corp-x-vcluster`, Guaranteed QoS). Not walkable here; needs a funnel Org that requested a per-Org CNPG Postgres.

Both confirmed "needs-a-specific-app" with live evidence (acme-corp's actual HR/pod set), not assumed. Would require provisioning a differently-carted funnel Org (a write) to walk.

## 🛑 catalyst-api OOM RECURRED — #5352 reopened (2026-07-26 03:38Z)

Live re-verify of the status/uat OOM issues found catalyst-api **still OOM-cycling** on hw288: pod `catalyst-api-5cd89b89f5-tn9qf` (2.5d old) at **restartCount=62**, `lastState.terminated.reason=OOMKilled` (exit 137, ~56-min growth cycle to the 4Gi limit), `request=96Mi` vs steady 855Mi→4Gi, readiness probe connection-refused during restarts. `/healthz=ok` only because probed early in the cycle. #5352 (closed at 28 restarts as fixed) **reopened** → status/uat with the evidence; #5285 (watch-informer flood, open) updated with the live OOM. Two faults: (1) the memory leak/growth to 4Gi (primary — likely the watch-informer flood, needs live profiling); (2) request=96Mi ~40× under actual (misconfig). Read-only; did NOT restart the north-star catalyst-api.

## status/uat re-walk + #5352 fix SHIPPED — 2026-07-26 04:26Z

Walked the 3 named status/uat issues live with fresh evidence:
- **#5345** (catalog chartless): `/catalog/bp-redis` renders **installable** on hw288 (v1.0.0, "Edit IaC", "In-memory key-value cache", singleton-per-Org) despite bp-redis being chartless in ghcr → the delivery-gap persists (fix #5346 unlisted the 5 in-repo, but hw288's frozen post-cutover seed still lists them). ◑ delivery-gated.
- **#5341** (gateway wildcard-SNI collision): BOTH `cilium-gateway` and `cilium-gateway-console` still carry `*.hw288.omani.works` listeners (6 wildcard listeners total) → collision persists; #5354 narrowed-listener fix not rolled (delivery-gated #5265). Live gateway config, stronger than a curl sample. ◑.
- **#5339** (region drift): region-A still runs the CronJob form (`*/5`, lastSchedule 04:25Z), no Deployment; #5340 not rolled (delivery-gated #5265). ◑.

Common gate: all three are delivery-gated on #5265 (hw288's frozen post-cutover seed predates the merged fixes) — a fresh prov clears them; not walkable-to-✅ on this env.

**#5352 OOM fix SHIPPED → PR #5365** (`fix/5352-k8scache-optional-gvr-existence-gate`, Refs #5352). Removes the dead `sandbox` kind; marks the 4 hcloud + cilium-esl GVRs `Optional`; adds a **bounded** (3s context-timeout, detached-goroutine, cached-per-GV) discovery existence-gate for Optional kinds only — served→register, absent/error/timeout→skip, non-optional kinds keep the synchronous-network-free fast path (does NOT reintroduce the boot-hang the old probe caused). +398/−22 across factory.go/kinds.go + a new 251-line test (12 cases). **Independently verified: `go build` EXIT=0, `go test ./internal/k8scache/...` ok.** Needs fresh-Huawei-prov verification (reflector "Failed to watch" gone, catalyst-api memory flat, 0 OOMKills over hours).
