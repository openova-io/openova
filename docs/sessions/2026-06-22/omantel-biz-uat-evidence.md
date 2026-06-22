# omantel.biz PERMANENT Sovereign — UAT live-walk evidence (2026-06-22)

**Env:** `omantel.biz` · dep `4635277cae4ffed9` · Huawei kom4dc · 2-region (me-east-215-a primary / -b standby) · status=ready
**Walked by:** orchestrator session 5c468708, live on the running Sovereign (Playwright UI + kubectl-via-mothership ground-truth).
**Rule:** every row below has a live artifact (HTTP code, CR, screenshot, or kubectl readback) — not a green-build inference.

## North Stars — all 4 demonstrated

| NS | Claim | Live evidence |
|---|---|---|
| **#1** every app in a vCluster | PASS | `mgmt`/`rtz`/`dmz` vCluster StatefulSets 1/1 Ready; 38/9/2 pods synced inside (x-<vc>-vcluster) |
| **#2** 3 shared-pg, many-to-many | PASS | CNPG clusters `shared-pg` + `shared-pg-b` + `shared-pg-c`, **3 instances each**, ns `shared-data` |
| **#3** passwordless admin | PASS | `https://console.omantel.biz` → 6-digit PIN (emailed emrah.baysal@openova.io, read read-only IMAP) → signed into `/dashboard` as admin; screenshot `omantel-biz-ns3-admin-dashboard.png` |
| **#4** multi-region a/p | PASS (topology) | `shared-pg-c` primary (region-A, currentPrimary shared-pg-c-2, 3/3) + `shared-pg-c-replica` (region-B, `replica.enabled=true`, source=shared-pg-c via `shared-pg-c-mesh`), both "Cluster in healthy state". Live region-kill failover proof = separate disruptive test (not run; would down the live console). |

## Datapath / Envoy / Cilium / TLS — GREEN

| Surface | Result |
|---|---|
| cilium-envoy (L7) | 3 pods 1/1 Running |
| Cilium Gateways | `cilium-gateway` + `cilium-gateway-console` both PROGRAMMED=True |
| HTTPRoutes | 15 |
| Wildcard TLS | `kube-system/sovereign-wildcard-tls-omantel-biz` ready=True (LE, SAN `*.omantel.biz`) |
| Cilium NetworkPolicy | 5 CNP enforced |
| **Front doors (real DNS)** | `console` / `marketplace` / `api`.omantel.biz → **HTTP 200, TLS verify=0** |

## Admin console surfaces (NS#3 deepening)

| Page | Result |
|---|---|
| Dashboard | Ready, 95 items, treemap renders (3 shared-pg + dmz/rtz/mgmt vClusters) |
| Cloud / Apps / Catalog | render; Catalog = 94 apps incl. **Agenity** (`bp-agenity`, #4058 rename live) + WordPress / WordPress-per-Org |
| Organizations | parent `omantel.biz` (isolation=vcluster, Active) + funnel-created **Demo Org** (customer, Active) |
| Users / RBAC | `emrah.baysal@openova.io` = **owner** tier; 5-tier model viewer/developer/operator/admin/owner |

## Agentic journey — funnel + install (real CRs)

- **demo@openova.io customer Org created** via console → Active, +7 HelmReleases (own vCluster), Org ID `7283eb4a-…`, console `console.demo.omani.homes`. Screenshot `omantel-biz-demo-org-provisioned.png`.
- **bp-agenity installed** into omantel.biz/rtz → real Application CR `omantel-biz/agenity` (singleton, me-east-215-a, rtz). Advanced **past EnvironmentMissing** after the #4077 fix → per-cluster HR fan-out.

## Blocker chain found + fixed this session (all merged to main)

| # | Blocker (surfaced live) | Fix |
|---|---|---|
| #4071 | console can't project app status (client-go WatchListClient informer) | #4072 merged |
| #4073 | no parent-domain pool → can't create customer Org | #4074 merged + live |
| #4075 | customer-Org console: stale pool DNS + wrong Enter-org host (+ per-pool wildcard cert residue) | #4076 merged |
| #4077 | app install → EnvironmentMissing (org onboarding never created the Environment CR) | #4078 merged |
| #4079 | per-cluster HR name invalid RFC-1123 (`agenity-rtz-A`) + missing sourceRef.kind | #4080 merged |

Plus this session: #4047 (EIP-cooldown), #4067 (warm-path coverage), #4068 (#4058 doc rename), #4070 (#4054 console-isolation DNS).

## Remaining for the clean re-fire
- The full agentic journey (agenity → OpenOva-MCP → WordPress; emrah@ → mgmt-vc agenity → shared-pg-c a/p) reaches **Running** on a **fresh zero-touch prov carrying #4076/#4078/#4080** (live Sovereign runs pinned controller images).
- #4075 residue: register the customer-Org pool's `*.<parent>` wildcard cert (the orphaned `omani.homes` pool was never a cert parent zone on this Sovereign) — or create customer Orgs on an omantel.biz-owned pool with a real wildcard cert.
- Live region-kill failover proof (NS#4) — disruptive; founder-gated.
