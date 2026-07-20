# hw282 — G12 CLEAN 6/6 + consolidation walk (2026-07-21)

**Env**: hw282.omantel.biz, dep `09696223c939cf29`, 2-region Huawei me-east-215-a/b, TLD omantel.biz (L3 rotation).
**Train**: #5303 (Harbor lowercase `disableredirect`) + #5292 (mothership-kustomize-compat) + full prior train. cutoverComplete=**true** (11/11 + deny-egress held).

## Headline — region-kill G12 CLEAN 6/6 (LAW #960 keystone)

First fully-autonomous 6/6 with the **failback face closed**:

| Face | Result | Evidence |
|---|---|---|
| 1. Kill region-a | ✅ | 4 ECS HARD os-stop, HTTP 200 ×4, bastion excluded |
| 2. #5267 fail-fast | ✅ | console/api → 503 no-healthy-upstream ×5 (not 404) |
| 3. Auto-promote | ✅ | region-b RW (`in_recovery=f` 20:22:10Z), **no force-arm** — #5239 arm-gate auto-armed |
| 4. RPO=0 | ✅ | pre-kill sentinel survived + post-kill write (`g12_hw282` rows=2) |
| 5. #5268 no split-brain | ✅ | region-a old-primary demoted to replica **before any write** (`in_recovery f→t` live) |
| 6. Failback re-clone | ✅ | region-a streaming replica of promoted region-b: `in_recovery=t \| wal_recv=1 \| g12_rows=2`; region-b stays RW |

**#5302/#5303 proven end-to-end**: post-restart cold chart-pulls succeeded (region-a HR 63/67 re-converged, Flux state intact) — the exact x509 cold-pull wedge that pinned hw281 region-a at 0/67 is gone with the fix baked in. Evidence comment: [#4275](https://github.com/openova-io/openova/issues/4275#issuecomment-5027486278).

## Post-G12 designed state (NOT defects)

- cnpg-pair roles swapped: region-b = RW primary, region-a `cnpg-pair-bp-cnpg-pair-primary` = streaming replica.
- Region-b HRs showing suspended = the #5219 promote latch (drift-immunity by design).
- Region-a not-Ready set = Hetzner-only slots (`bp-hcloud-ccm`, `bp-cluster-autoscaler-hcloud`, `bp-velero`) + `bp-catalyst-secondary-edge` (region-b's slot).
- shared-pg's own DR pair unaffected: a=primary, b=hot standby, lag 0.0 s.

## Consolidation walk (browser + 7-walker fleet)

Owner session minted via handover exchange (mothership-signed JWT → `/auth/handover` → `catalyst_session`; whoami: tier=owner). Playwright live again → the browser-gated render wall walked directly:

- **Cloud per-kind pages (199-204)**: Gateways 2 · HTTPRoutes live · NetworkPolicies live · CNPs live · LBs 2/2 HEALTHY · WorkerNodes 8/8 HEALTHY — all six render legs proven.
- **Topology/DR tab (51-58, 62, 64, 71)**: full DR surface live on shared-pg — active-hot-standby diagram, ● LIVE, replication lag **0.0 s**, armed "Switch over…" with RPO preflight; cilium honestly hides DR (active-active, no phantom Switchover).
- **Catalog edit round-trip (124-129)**: inline editor → Save → grid + hard-reload + server-side GET all carry `RECONCILE-PROOF-hw282-g12` (git-backed persist; #5113 commit-leg live). Note: proof marker intentionally left on bp-alloy summary (env is wiped next cycle; populated-tagline editor re-open affordance differs — cosmetic).
- **Jobs (159-168)**: populated activity, typed KIND filter, both regions, cutover group renders all step rows Succeeded.
- **Showback (22)**: single distinct `__platform__` overhead roll-up (20,746 units).
- **Settings (160)**: Sovereignty anchor present.

Fleet (workflow `wf_3e29544a-9a2`, 7 read-only walkers + funnel mutation walker): verdicts consolidated into `docs/ledger/UAT.md` on branch `uat/hw282-consolidation` (fresh off origin/main; env-recency per-row merge — 23 rows adopted from origin's hw281 evidence).

## Defects surfaced

- **R21 ❌ — catalog-seed version lag**: catalog-seed Blueprint CRs lag published charts for 10/52 blueprints (bp-catalyst-platform seed 1.4.539 vs deployed 1.4.1195, bp-guacamole 0.2.3 vs 0.2.29, bp-continuum 0.1.4 vs 0.1.12, …). Known resource-policy-keep drift class — durable fix to be batched at close-out; not a pillar blocker.
