# OpenOva — Status Tracker

Regenerated every 15 min by `/home/openova/bin/refresh-dod-dashboard.sh`. Every `#NNNN` is a clickable GitHub link.

|  |  |
|---|---|
| Last refreshed | `2026-06-27` (hand-update, session 2026-06-27) |
| Live env | omantel.biz dep `91dc05917e44d1c1` — 2-region converged 62/62 + 56/56 |
| Open issues | 14 (12 gated tickets + #4486 session-resolved + #4499 just-merged via #4500) |
| Closed this session (audited 22/22 clean) | #4454 #4467 #4448 #4458 #4437 #4442 #4444 #4447 #4446 #4471 #4354 #4460 #4436 #4479 #4473 #4290 #4459 #4450 #4482 #4464 #4432 #4468 |
| Open TBD-* regressions | 0 |
| Keystone single-region GO-3EIP validation prov | FIRING on current main (kom4dc) — closes the roll-gated cluster |

**Legend:** <img alt="DONE" src="https://img.shields.io/badge/-DONE-2ea043?style=flat-square" /> done · <img alt="WAITING_PROV" src="https://img.shields.io/badge/-WAITING__PROV-bf8700?style=flat-square" /> fix shipped, awaits prov · <img alt="OPEN" src="https://img.shields.io/badge/-OPEN-cf222e?style=flat-square" /> open · <img alt="DEFERRED" src="https://img.shields.io/badge/-DEFERRED-6e7781?style=flat-square" /> deferred

---

## 1. Gating map — what unblocks the 14 open issues (session 2026-06-27)

The board dropped 35 → 12 gated this session. Every remaining open ticket sits behind ONE of three gates. The permanent omantel.biz env is pin-frozen, so merged fixes are inert on it — a fresh keystone prov on current main is the validation vehicle.

| Gate | Issues parked behind it | What clears it |
|---|---|---|
| **G1 — keystone single-region prov** (FIRING now on current main, kom4dc, GO-3EIP) | [#4488](https://github.com/openova-io/openova/issues/4488) crossplane provider-opentofu · [#4477](https://github.com/openova-io/openova/issues/4477) newapi-seed · [#4466](https://github.com/openova-io/openova/issues/4466) janitor log-only · [#4431](https://github.com/openova-io/openova/issues/4431) VPC-reclaim · [#4486](https://github.com/openova-io/openova/issues/4486) DR-self-fire · [#4499](https://github.com/openova-io/openova/issues/4499) seed-sync · [#4293](https://github.com/openova-io/openova/issues/4293) vcluster=Org · [#3969](https://github.com/openova-io/openova/issues/3969) placement-basics · [#4475](https://github.com/openova-io/openova/issues/4475) vcluster-CNP | The keystone prov reaching converged + zero-touch re-walk of these surfaces on current main. |
| **G2 — Omantel EIP quota bump 10 → ≥16** (the single decisive founder lever) | [#4275](https://github.com/openova-io/openova/issues/4275) region-kill failover · [#4212](https://github.com/openova-io/openova/issues/4212) DR seams · [#4293](https://github.com/openova-io/openova/issues/4293) multi-region | A 2-region prov needs 6 EIPs; kom4dc quota=10, free=3. No 2-region prov can fire until quota rises to ≥16. |
| **G3 — Anthropic credential** | [#4277](https://github.com/openova-io/openova/issues/4277) anthropic-seed-value · [#4111](https://github.com/openova-io/openova/issues/4111) agentic-run | A real Anthropic API token to seed into the per-Org OpenBao path; without it the chat→provision agentic journey cannot run end-to-end. |
| **Destructive-on-throwaway** | [#3379](https://github.com/openova-io/openova/issues/3379) cutover | Runs on the keystone env (deny-egress hold is destructive — never on the permanent env). |

**HARD CAPACITY FACT:** kom4dc EIP quota = 10, free = 3. A 2-region prov needs 6. The single founder lever that unblocks the most tickets at once is an **EIP quota bump 10 → ≥16** (clears G2's three tickets). The next-largest lever is the **Anthropic credential** (clears G3's two).

---

## 2. Open issues — all 14, with status label + gate (session 2026-06-27)

Live board snapshot 2026-06-27. Gate column maps each ticket to the gating map in §1 (G1 keystone prov · G2 EIP quota bump · G3 Anthropic credential · D destructive-on-throwaway). The two non-gated-bucket items — [#4486](https://github.com/openova-io/openova/issues/4486) (`status/completed`) and [#4499](https://github.com/openova-io/openova/issues/4499) (`status/uat`, just merged via PR #4500) — are session-resolved and awaiting their re-walk + close.

| # | Status label | Gate | Title |
|---|---|---|---|
| [#4488](https://github.com/openova-io/openova/issues/4488) | status/uat | G1 | fix(crossplane): provider-opentofu never INSTALLED on Huawei — xpkg.upbound.io pull routes through poisoned-EIP harbor → adoption seam inert |
| [#4477](https://github.com/openova-io/openova/issues/4477) | status/uat | G1 | SSO seeding faults on fresh prov: openbao external-group alias never binds (admin/ops 403) + newapi DB client_secret goes stale |
| [#4475](https://github.com/openova-io/openova/issues/4475) | status/uat | G1 | Org vcluster-tier convergence: CiliumNetworkPolicy can't apply inside vanilla vcluster + pool-DNS reflector startup-ordering |
| [#4466](https://github.com/openova-io/openova/issues/4466) | status/uat | G1 | Harden orphan-sweep janitor: protect-by-default + log-only-until-proven + active-dep do-not-touch allowlist + EVS-orphan sweep |
| [#4431](https://github.com/openova-io/openova/issues/4431) | status/in-progress | G1 | Fresh multi-region HCS prov false-fails on VPC quota — orphan VPCs leak quota with no reclaim sweep |
| [#4499](https://github.com/openova-io/openova/issues/4499) | status/uat | G1 | bp-plane-isolation: openbao default-deny OMITS catalyst-system → catalyst-api k8s-auth Cilium-dropped → secret/catalyst/* SecretSyncedError (merged via #4500) |
| [#4486](https://github.com/openova-io/openova/issues/4486) | status/completed | G1 | DR backbone self-fire: standby-absent on a converged primary must NOT latch the whole post-handover chain off |
| [#4293](https://github.com/openova-io/openova/issues/4293) | status/uat | G1 / G2 | EPIC: One vcluster = one Organization — collapse the 3 provisioning doors to the org-controller + de-vcluster the platform planes |
| [#3969](https://github.com/openova-io/openova/issues/3969) | status/uat | G1 | EPIC: Application-centric Placement (Primary/Standby · Hot/Cold, capability-gated multi-primary, default-cascade, recon-status) |
| [#4275](https://github.com/openova-io/openova/issues/4275) | (no status) | G2 | PILLAR-3 ACCEPTANCE: region-kill failover counter-test (D31) — kill region-a, prove cnpg-pair replica promotes RTO ≤ 30s, zero committed-tx loss |
| [#4212](https://github.com/openova-io/openova/issues/4212) | status/uat | G2 | EPIC: ONE object-model/DR backbone — 2 seams still unwired (spine Application-CR producer #3829, Crossplane adoption runtime-Observe #4018) |
| [#4277](https://github.com/openova-io/openova/issues/4277) | status/uat | G3 | FUNNEL/agenity: auto-seed the per-Org openbao anthropic/token at Org-create so the agentic journey is zero-touch on a FRESH Org |
| [#4111](https://github.com/openova-io/openova/issues/4111) | status/uat | G3 | bp-agenity: spawned claude-code agent cannot authenticate or run (no claude binary, no OAuth credentials.json, BareExec fallback) |
| [#3379](https://github.com/openova-io/openova/issues/3379) | status/uat | D | SOVEREIGNTY: cutover earns cutoverComplete=true only via a DURABLE reconcile-immune fact + a TRUE deny-egress proof (runs on the keystone env) |

---
## 3. Recently merged PRs

### Session 2026-06-27 — PRs merged

| PR | Issue | What it does |
|---|---|---|
| [#4493](https://github.com/openova-io/openova/pull/4493) | #4466 | janitor harden — log-only-until-proven + active-dep do-not-touch allowlist + protect-by-default |
| [#4492](https://github.com/openova-io/openova/pull/4492) | #4477 | seed newapi admin-token into OpenBao so the catalyst SSO seed no longer faults on a fresh prov |
| [#4495](https://github.com/openova-io/openova/pull/4495) | #4290 | org-controller renders the gateway/apiserver CNP host-side for the vcluster tier |
| [#4494](https://github.com/openova-io/openova/pull/4494) + [#4496](https://github.com/openova-io/openova/pull/4496) | #4111 | bp-agenity default imagePullSecrets=ghcr-pull + catalog-seed source.version lockstep (image-pull recovery) |
| [#4498](https://github.com/openova-io/openova/pull/4498) | #3969 | application-controller §13 — arm the Continuum placement lease-witness off the #3969 targets[] model |
| [#4497](https://github.com/openova-io/openova/pull/4497) | #4466 | UAT ledger consolidation — 22 closed + honest gate map |
| [#4500](https://github.com/openova-io/openova/pull/4500) | #4499 | bp-plane-isolation: add catalyst-system to the openbao allowIngressFrom — root cause behind the #4477/#4277 seed faults (catalyst-api k8s-auth to OpenBao was Cilium-dropped) |

### Last 36h (auto-table)

| Merged | PR | Issue closed | Title |
|---|---|---|---|
| 2026-06-26T19:41 | [#4498](https://github.com/openova-io/openova/pull/4498) | #3969 | feat(application-controller): #3969 §13 — arm the Continuum  |
| 2026-06-26T19:31 | [#4497](https://github.com/openova-io/openova/pull/4497) | #4466 | docs(uat): UAT ledger consolidation — 22 closed + honest gat |
| 2026-06-26T19:17 | [#4496](https://github.com/openova-io/openova/pull/4496) | #4494 | fix(catalog-seed #4111): bp-agenity seed source.version 0.5. |
| 2026-06-26T19:02 | [#4495](https://github.com/openova-io/openova/pull/4495) | #4290 | fix(org-controller): render gateway/apiserver CNP host-side  |
| 2026-06-26T18:50 | [#4494](https://github.com/openova-io/openova/pull/4494) | #4111 | fix(bp-agenity): default imagePullSecrets to ghcr-pull — kil |
| 2026-06-26T18:43 | [#4493](https://github.com/openova-io/openova/pull/4493) | #4466 | fix(janitor): log-only-until-proven + active-dep allowlist + |
| 2026-06-26T18:24 | [#4492](https://github.com/openova-io/openova/pull/4492) | #4477 | fix(catalyst): seed newapi admin-token into OpenBao so catal |
| 2026-06-26T17:54 | [#4491](https://github.com/openova-io/openova/pull/4491) | #4002 | fix(crossplane): route provider-opentofu xpkg pull off the p |
| 2026-06-26T17:48 | [#4490](https://github.com/openova-io/openova/pull/4490) | #4470 | fix(bp-plane-isolation): annotate smoke-render-mode=default- |
| 2026-06-26T17:47 | [#4489](https://github.com/openova-io/openova/pull/4489) | #4415 | docs(uat): restore 9 §A/§F walker-PASS rows + re-baseline #3 |
| 2026-06-26T17:45 | [#4487](https://github.com/openova-io/openova/pull/4487) | #3375 | fix(catalyst-api): DR backbone self-fires for a converged pr |
| 2026-06-26T17:12 | [#4485](https://github.com/openova-io/openova/pull/4485) | #4454 | docs: janitor self-reap post-mortem (VPC guard-chain) + sove |
| 2026-06-26T17:07 | [#4484](https://github.com/openova-io/openova/pull/4484) | #4279 | docs(uat): live fresh-prov 91dc0591 walk results — 57✅/16❌/1 |
| 2026-06-26T16:01 | [#4483](https://github.com/openova-io/openova/pull/4483) | #4325 | fix(bp-catalyst-platform #4482): reflect catalyst-gitea-toke |
| 2026-06-26T15:50 | [#4481](https://github.com/openova-io/openova/pull/4481) | #4477 | fix(bp-openbao,bp-newapi): two fresh-prov SSO seeding faults |
| 2026-06-26T16:12 | [#4480](https://github.com/openova-io/openova/pull/4480) | #4471 | fix(catalyst-api): console org-list/detail read from orgs.op |
| 2026-06-26T15:27 | [#4478](https://github.com/openova-io/openova/pull/4478) | #4474 | fix(org-controller): CRD clientSecretRef value-struct trap + |
| 2026-06-26T15:13 | [#4476](https://github.com/openova-io/openova/pull/4476) | #4293 | fix(provisioning): funnel plan selection propagates to the O |
| 2026-06-26T15:05 | [#4472](https://github.com/openova-io/openova/pull/4472) | #4462 | fix(org-controller #4471): ClusterRole gains update+patch on |
| 2026-06-26T17:35 | [#4470](https://github.com/openova-io/openova/pull/4470) | #4445 | fix(bp-plane-isolation #4468): extend the #4445 defer-if-abs |
| 2026-06-26T14:42 | [#4469](https://github.com/openova-io/openova/pull/4469) | #4467 | fix(bp-cilium): pin MTU=1370 for wireguard+vxlan datapath —  |
| 2026-06-26T12:54 | [#4465](https://github.com/openova-io/openova/pull/4465) | #2584 | fix(#4464): deploy-bump per-line cherry-pick stops whole-fil |
| 2026-06-26T12:28 | [#4463](https://github.com/openova-io/openova/pull/4463) | #4275 | fix(bp-postgres): decouple -mesh/-mesh-rw global Service ali |
| 2026-06-26T12:02 | [#4462](https://github.com/openova-io/openova/pull/4462) | #4250 | fix(org-controller): cascade-delete per-Org tenant-networkin |
| 2026-06-26T11:57 | [#4461](https://github.com/openova-io/openova/pull/4461) | #4448 | fix(bp-sso-bridge): CNP egress names openbao+keycloak+dns —  |
| 2026-06-26T12:03 | [#4457](https://github.com/openova-io/openova/pull/4457) | #4454 | fix(catalyst-api): janitor orphan-sweep no longer reaps its  |
| 2026-06-26T11:32 | [#4456](https://github.com/openova-io/openova/pull/4456) | #4452 | fix(bp-catalyst): provisioning-github-token-sync RBAC ordere |
| 2026-06-26T12:32 | [#4455](https://github.com/openova-io/openova/pull/4455) | #4290 | fix(org-controller): self-heal tenant-dns pool-PowerDNS key  |
| 2026-06-26T11:21 | [#4453](https://github.com/openova-io/openova/pull/4453) | #4114 | fix(catalyst-api #4450): preserve mothership-injected handov |
| 2026-06-26T11:14 | [#4452](https://github.com/openova-io/openova/pull/4452) | #4447 | fix(bp-catalyst): gitea-flux-auth-sync RBAC ordered before t |

---

## 4. Theater-incident log

Caught "fix shipped but actually broken" events + the validation principle that emerged:

| When | Broken PR | Caught by | Resolving PR | Principle codified |
|---|---|---|---|---|
| 2026-05-18 23:19Z | PR [#1875](https://github.com/openova-io/openova/pull/1875) — HR.dependsOn -> Kustomization silently ignored | t27 empirical walk | PR [#1879](https://github.com/openova-io/openova/pull/1879) | **#14** HR.dependsOn cross-kind |
| 2026-05-19 01:08Z | PR [#1892](https://github.com/openova-io/openova/pull/1892) — Python jsonencode != tofu HCL | t29 `tofu plan` failure | PR [#1894](https://github.com/openova-io/openova/pull/1894) | **#15** validate IaC with the IaC evaluator |
| 2026-05-19 02:00Z | PR [#1889](https://github.com/openova-io/openova/pull/1889) — 10 annotations on Gateway, CRD cap 8 | t30 SSA dry-run reject | PR [#1898](https://github.com/openova-io/openova/pull/1898) | (#15 applied) |

**Pattern**: every theater event was caught by an empirical fresh-prov walk, never by static review.

---

## 5. Resources

- [PRINCIPLES](https://github.com/openova-io/openova/blob/main/docs/PRINCIPLES.md) — 15 inviolable principles + anti-pattern catalog
- Manual refresh: `bash /home/openova/bin/refresh-dod-dashboard.sh`
- Cron: every 15 minutes
