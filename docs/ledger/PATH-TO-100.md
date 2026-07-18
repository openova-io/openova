# PATH TO 100% — hw274-cycle fix map (refreshed 2026-07-19)

> **Source of truth:** [`UAT.md`](UAT.md) — reset 2026-07-19 for the **hw274** fresh prov (dep `d51a69f885872a81`, `hw274.omantel.biz`, 2-region me-east-215-a/b, fired 20:43Z on the 8-fix train).
> This file maps every non-green row family to its exact fix (issue + PR + code path) and its validation gate.
> Prior anchor (hw159, 2026-06-18) is superseded — its Bucket-A roots #3642/#3376 were closed in the June cycles.

---

## The hw274 train — 8 fixes merged 2026-07-18/19, ALL validate on this prov

| # | Defect (issue) | Fix PR / artifact | Rows it un-gates | Validation gate on hw274 |
|---|---|---|---|---|
| 1 | **#5210** catalyst-api informer one-shot token → post-cutover 401 flood → empty live-resource cache | **#5221** (`fdc903c85`) — chroot kubeconfig emits `user.tokenFile:` + refresh-wrap; `internal/handler/clustermesh_primary_kubeconfig.go` + `internal/k8scache` | ~26 render rows (cloud graph, Topology, treemap, per-kind 199-203, resource drill-in) | post-cutover console renders; reflector 401 count ≈ 0 |
| 2 | **#5220** dr-promoter false-positive promotion cemented by the #5219 latch (hw273: region-a alive TL1, region-b spuriously TL2) | **#5222** (`bc15f31b6`) — bp-cnpg-pair **0.2.16**: steady-state ARM gate (≥180s observed streaming before any promote) + TL-divergence surfaced on the HR (non-destructive; operator re-clone per RUNBOOKS §6.1) | G12 + DR family | region-kill G12: no convergence-time promote; clean promote+latch on the real kill; RPO=0 |
| 3 | **#5204** harbor-prewarm skipped Crossplane xpkg → provider-opentofu dead post-severance | **#5226** (`7c967fee4`) — cutover chart **0.1.142**: prewarm Phase A4 pull-through warm of every `spec.package` + step-11 verify-before-pivot (fail-loud); contract Case 65 | cutover rows 159-166 tail + crossplane G-rows | cc=true with `provider-opentofu Installed=True` under deny-egress |
| 4 | **#5124** Edit-IaC seed stale (read path fixed in #5164; write seed still bypassed the canonical serializer) | **#5227** (`36e2382e0`) — catalyst-api serves the seed through the #5115 canonical Blueprint CR serializer | Edit-IaC rows 148/154 family | console Edit → seed matches live CR → Commit 200 |
| 5 | **#5224** shared-pg role-password clobber (G12 flap ALTERed the shared primary's `harbor` role with region-b's divergent minted secret; CNPG blind — hw273 forensics) | **#5228** (`e786f9984`) — bp-postgres role-secrets single-sourced across the pair + Continuum/promote role-assert via CNPG managed-roles rv rotation (no direct ALTER) | R13 + region-b app steady-state family | zero `28P01` on shared roles through convergence AND through G12; region-b harbor steady |
| 6 | **#5223** console voucher-code inline policy + reconciler-node drill-in + card-save IaC toast | **#5229** (`0b01f9548`) — console SPA | catalog/BSS voucher rows + recon drill rows | authed console walk |
| 7 | **#5214** cutover gitea-admin-secret vanish (4th recurrence) | #5216, cutover 0.1.140 — **CLOSED**, fresh-prov-validated on hw273 | — (regression guard) | re-confirm zero-touch cc=true |
| 8 | **#5215** step-07 CSI-roll EVS wedge | #5217, cutover 0.1.141 — **CLOSED**, fresh-prov-validated on hw273 (step-07 in 12s vs 11-min wedge) | — (regression guard) | re-confirm zero-touch cc=true |

Also aboard, already fresh-prov-validated on hw273: **#5207/#5205** marketplace console-ready same-origin proxy (funnel row 87 leg).

## Remaining non-green map (fleet triage `wf_42f3cdef`, 2026-07-19)

| Class | Rows / family | Exact gate | Owner action |
|---|---|---|---|
| **Walk-only** (fix merged, needs the hw274 walk) | ~65 ⚠️ + the SSO 26-45 re-stamp family (durable SSO ≈ 90%, flushed by ledger discipline, NOT regressed) + rows 185/187/214/G10/G12/226/87 | hw274 cc=true + authed walk-fleet | walk-fleet workflow (staged) stamps them |
| **Founder-gated** | G8/G9/220/M4 (Agenity chat driver, MCP `create_application` E2E) | **#4277** — a real Anthropic credential from the founder | the ONLY non-engineering gap in the ledger |
| **Superseded premises** (annotate, don't walk) | R19 + sandbox rows 5/7/9-12 family | Sandbox concept removed by founder decree 2026-06-30 (Agenity + bp-openova-mcp supersede); #3985 SME→Organization; #4325 de-vcluster | reclassify ⛔-superseded at next consolidator pass (founder verdict precedent: rows 99-107) |
| **Residual filed** | #5225 `registry.<fqdn>` wildcard → region-a cp1 INTERNAL-IP (cross-VPC :443 refused) | partially addressed via #5228's Refs; verify on hw274 region-b pulls | if it recurs on hw274 → dedicated fix |
| **Non-blocking residue** | #5088 remainder (3 mothership NodePorts: cinova ⛔ / ephemeral solver / iogrid#844) | founder merge of iogrid#844 | consolidated record: #5088 comment 2026-07-19 |

## The 100% equation on hw274

`100% = walk-fleet stamps (walk-only bucket) + G12 clean (validates #5222/#5228) + superseded reclassification − #4277 (founder credential)`

Every engineering defect known as of this refresh has a **merged** fix aboard hw274. Anything new the walk surfaces gets the fleet treatment: forensics → triage → worktree fixers → next train.
