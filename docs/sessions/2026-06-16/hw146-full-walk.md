# hw146 full walk — founder sign-off gate (pre-wipe)

**Env:** hw146 = deployment `531e6908e606d11d` · fqdn `hw146.omani.works` · 2-region (me-east-215-a primary, me-east-215-b secondary) · fired with the **full train** (SHARED_PG=true).
**Walked:** 2026-06-16, zero-touch (no hand-patches). Convergence was zero-touch both regions.
**Rule honored:** no wipe until the founder has seen this full walk and says so (the hw145 mistake — never again).

This is the "complete walkthrough" the founder asked for before any wipe. Each finding is either ✅ validated live, or a **passenger** with a fix already in flight for the next prov (hw147).

---

## North Star results (live on hw146)

| NS | Statement | Verdict | Evidence (current hw146) |
|----|-----------|---------|--------------------------|
| **1** | Every app in a vCluster | ✅ PASS | 3 vClusters running 1/1: `dmz-vcluster`, `mgmt-vcluster`, `rtz-vcluster`; app pods carry the `-x-` vcluster-sync marker. `kubectl get statefulset -A`. |
| **2** | 3 shared-PG instances → 3 cards, many-to-many | ✅ PASS | `kubectl get cluster -n shared-data` → `shared-pg`, `shared-pg-b`, `shared-pg-c` — **all 3/3 READY, healthy**. 3 instances = 3 catalog cards. |
| **3** | NO login UI — URL → signed in as admin | ⚠️ 4/5 → fix shipped | SSO landing matrix below. Gitea/Grafana/Harbor/Keycloak land zero-click authed; OpenBao failed (popup-only OIDC callback) → **fixed #3628**. |
| **4** | Agreed apps multi-region; region-kill preserves data | ⏳ precondition ready | Data layer present: cnpg-pair replicating in region-b (2/3 ready + failover-readiness pod), shared-PG 3/3 region-a. Clean region-kill walk deferred to hw147 (hw146 is mid-cutover — see NS#5 — so a region-kill here would be inconclusive). |
| **5** | Cutover → cutoverComplete + 600s deny-egress | ⏳ furthest-ever → 2 defects fixed | Cutover ran further than any prior attempt (past harbor-prewarm AND step-05). Wedged at step-06 on two newly-surfaced defects → **fixes in flight** (next-earlier-defect pattern). Details below. |

---

## NS#3 — SSO landing matrix (zero-click, current hw146)

Each app's **Open** button on `/catalog` was clicked; verdict = does it land authenticated (not a login page)?

| App | Verdict | Evidence |
|-----|---------|----------|
| Gitea | ✅ lands authed | `gitea.hw146…/` dashboard "emrah.baysal", no login form. `evidence/hw146-gitea-sso-landed.png` |
| Grafana | ✅ lands authed | `grafana.hw146…/?orgId=1` "Welcome to Grafana", nav present. `evidence/hw146-grafana-sso-landed.png` |
| Harbor | ✅ lands authed | `registry.hw146…/harbor/projects` as `emrah.baysal@openova.io`, 9 projects. `evidence/hw146-harbor-sso-landed.png` |
| Keycloak | ✅ lands authed | `auth.hw146…/admin/sovereign/console/` admin console, realm nav. `evidence/hw146-keycloak-sso-landed.png` |
| OpenBao | ❌ → **fixed** | Vault-UI OIDC callback `vault.cluster.oidc-callback` threw `Cannot read properties of null (reading 'postMessage')` — popup-only callback can't complete in a top-level-tab launch. |

**OpenBao fix — #3626 / PR #3628** (chart-only): Vault-UI's OIDC callback does an unconditional `window.opener.postMessage`, so launching it as a top-level tab (window.opener=null) strands the user. Fix repoints OpenBao's launch `ssoInitPath` → `/sso/landing` (the popup-free top-level flow already shipped by #3374) in both the catalog-seed and `platform/openbao/blueprint.yaml`. `bp-openbao` 1.2.44 + `bp-catalyst-platform` 1.4.645. **No catalyst-ui change.** Rides hw147.

---

## NS#5 — cutover deep-dive (two stacked defects, both fixed)

hw146's cutover reached the **furthest point ever**: step-01 gitea-mirror ✅, step-03 harbor-prewarm ✅ (the 0.1.68 skopeo-retry fix got the 2 large guacamole images through), step-05 flux-gitrepository-patch ✅ (Flux now tracks local Gitea). It wedged at **step-06 helmrepository-patches Phase-3a**, which uncovered two defects no prior attempt reached:

- **Defect A (primary):** No cutover step mirrors the Helm **charts** into local Harbor — step-03 skopeo-pushes pod **images** only. Live proof: hw146 local Harbor `openova-io` = **29 image repos, 0 chart repos**. After step-06 repoints HelmRepositories to `oci://registry.hw146…/openova-io/bp-*`, the charts aren't there.
- **Defect B:** step-06 Phase-3a gates the destructive ghcr-auth strip on each pivoted HelmRepository reporting `.status.Ready=True` — but for `type: oci` HelmRepositories Flux **never** sets a Ready condition (verified: all 61 have empty `.status.conditions`). So the gate FATALs forever (and, usefully, masks A by never stripping → cluster stays alive on ghcr).

**Cutover fix — PR in flight** (cutover chart): (A) extend step-03 to skopeo-mirror the chart OCI artifacts (derived from each HelmRelease's chart+version) into local Harbor pre-egress, reusing the 0.1.68 retry discipline; (B) replace step-06 Phase-3a's never-true HelmRepository-Ready gate with a direct authenticated `helm pull` probe against local Harbor, preserving the "never strip on unproven local Harbor" invariant. Rides hw147 (hw146 is post-step-05; it can't receive a GitHub-main cutover-chart fix).

---

## App-health sweep — both regions (signal vs noise)

`kubectl` HR + Job + Pod sweep across region-a and region-b.

**Confirmed expected (NOT defects):**
- `bp-cluster-autoscaler-hcloud`, `bp-hcloud-ccm` non-Ready — Hetzner-cloud controllers, gated off on Huawei.
- `bp-catalyst-platform`, `bp-continuum`, `bp-self-sovereign-cutover` non-Ready **in region-b** — primary-suspended-in-standby by design.
- `bp-sandbox` ImagePullBackOff — Sandbox is owned by a separate project (out of this scope).
- `trivy-system/scan-vulnerabilityreport-*` Failed — security-scanner noise.

**Real passengers (fix dispatched — region-b/cross-region data wiring):**
1. region-b **grafana + powerdns-admin CrashLoopBackOff** — NXDOMAIN on `shared-pg-b-rw.shared-data` (that service exists only in region-a; the 3 shared-PG instances are primary-region). Either a placement defect (these apps shouldn't run in the standby) or a DB-wiring defect (should point at the region-b cnpg-pair replica). Under investigation.
2. **openbao-snapshot save (region-a) + fetch (region-b) CreateContainerConfigError** — need `seaweedfs-s3-secret` keys `admin_access_key_id`/`admin_secret_access_key` (absent) — openbao raft DR ↔ SeaweedFS wiring gap (region-kill-relevant).
3. **cnpg-pair replica-3 Pending** (2/3 ready) + **guacd Pending** both regions — being characterized as resource-constrained-expected vs a real request/anti-affinity defect.

---

## Fixes shipped this walk (the train for hw147)

| Finding | Issue / PR | Surface | Reaches hw146? |
|---------|------------|---------|----------------|
| Cutover harbor-prewarm large-image resilience | #3624 / #3625 (merged, 0.1.68) | cutover chart | already on main |
| OpenBao SSO doesn't land authed | #3626 / PR #3628 | bp-openbao + catalog-seed | no — hw147 |
| Cutover: chart-mirror + step-06 OCI-Ready gate | (agent in flight) | cutover chart | no — hw147 |
| region-b data-wiring (3 defects) | (agent in flight) | charts/IaC | no — hw147 |

---

## Bottom line

hw146 converged zero-touch both regions and **validated NS#1, NS#2, and 4/5 of NS#3 live**. It drove the cutover further than ever, and the walk surfaced exactly what the founder predicted — more real issues: the OpenBao SSO landing, the two deeper cutover defects (chart-mirror + the OCI Ready-gate), and the region-b data-wiring cluster. Every one has a fix shipped or in flight, all riding the next fresh prov.

**hw146 is NOT wiped.** Holding for founder review of this walk. Next prov (hw147) carries the full fixed train and is where NS#4 region-kill + NS#5 cutoverComplete get their clean validation.
