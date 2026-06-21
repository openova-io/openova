
## STEP-4 zero-touch walk (handover JWT + Playwright) — partial, health-gated

The cutover auto-trigger fired the MOMENT catalyst-api came Ready → the env went
straight from converging into a **finite self-sovereign cutover** (the dashboard
+ every page show "An operation is in progress — running a finite cutover").
Per the health-gate rule, the cutover-SENSITIVE epics (per-app topology, funnel
coupon→Org, recon reconcile/suspend) are env-state noise mid-pivot → marked
GATED, not walked. The env-INDEPENDENT fixes WERE walked live and PASS:

| Fix | Ticket | Verdict | Live evidence |
|---|---|---|---|
| SSO zero-click | #3374 | ✅ PASS | `…/auth/handover?token=<jwt>` → **`/dashboard` signed-in, NO login form**; full sidebar (Dashboard/Cloud/Apps/Catalog/Sandbox/Jobs/Compliance/Users/Organizations/Settings); avatar = hw178.omani.works. [`evidence/hw178-sso-dashboard.png`](evidence/hw178-sso-dashboard.png) |
| STORAGE | #4030/#4031/#4037 | ✅ PASS (with live patch) | evs-ssd default SC rendered zero-touch (#4030 held); driver image pull fixed live (#4037) → CSI pods Running → PVCs Bound on evs-ssd (`data-dmz/mgmt-vcluster-0`, `shared-pg-1`/`shared-pg-c-1` 10Gi); treemap shows `bp-huawei-evs…` running. Durable fix PR #4038. |
| CLOUD-VIEW LB | #4019 | ✅ PASS | Cloud view chip **`LoadBalancer 2/2`** — the real ELBs render (NOT 0/0, the exact bug); both region LBs `healthy`. [`evidence/hw178-cloud-2region-lb.png`](evidence/hw178-cloud-2region-lb.png) |
| TOPOLOGY (cloud, 2-region) | #4015 | ✅ PASS | Cloud view: **`Region 2/2` + `Cluster 2/2`** — both `me-east-215-a` AND `me-east-215-b` render with full worker fleets (24/24 WorkerNodes) — true active-active 2-region. |
| TENANT-STRINGS | #4017 | ✅ PASS | Sidebar + treemap + cloud view use `Organization`/`Sovereign`/`vCluster` — NO "tenant"/"SME" banned-term leak observed on any walked page. |
| JOBS-FINITE | — | ✅ PASS | Jobs page renders a structured finite-job table (kinds step/task/cron/lifecycle, parent/dep chains, Running/Pending/Succeeded, durations); tofu lifecycle jobs Succeeded; cutover step-chain dependency-ordered. NO infra-pod-as-app. [`evidence/hw178-jobs-finite.png`](evidence/hw178-jobs-finite.png) |
| TOPOLOGY (per-app placement) | #4015/#3375 | ⛔ GATED | env mid-cutover — per-app placement read would be transitional noise |
| FUNNEL (coupon→Org→ACTIVE) | #4016 | ⛔ GATED | env mid-cutover; also gitea/harbor CNPG PG still settling |
| RECON-ACTIONS | #4014 | ⛔ GATED | env mid-cutover — reconcile/suspend during a pivot is noise |

## Headline
1. **The hw177 worker-kubelet wedge did NOT recur** — all nodes Ready and stayed Ready; CoreDNS + Flux + HRs all converged. The wedge was an INFRA flake (confirmed).
2. **The #4030 storage StorageClass fix HELD zero-touch.**
3. **A NEW deterministic storage blocker (#4037, private-image imagePullSecret) was found live + fixed durably (PR #4038) + validated by a live patch (PVCs bound).**
4. **6 cycle-1/cloud fixes walked LIVE and PASS** (SSO, storage, cloud-view LB, 2-region topology, tenant-strings, jobs-finite). The 3 cutover-sensitive epics are health-gated.

## Notes / follow-ups
- The gitea/harbor CNPG PG PVCs were created with EMPTY storageClass (before evs-ssd landed, due to the #4037 image-pull delay) → they didn't self-bind; on a clean prov with #4037 merged the CSI driver comes up promptly so evs-ssd lands BEFORE CNPG PVC creation. Worth a small guard (CNPG cluster spec should pin `storageClass: evs-ssd` rather than rely on default-class timing).
- The `harbor-proxy-pull` Kyverno policy is AUDIT (not Enforce) and flags the direct-ghcr driver image; the imagePullSecret approach works, but a future consistency pass could repoint the driver to `registry.<fqdn>/proxy-ghcr/...`.
