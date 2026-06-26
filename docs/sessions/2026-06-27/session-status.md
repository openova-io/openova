# Session status — 2026-06-27

Founder-facing summary. All verdicts are against the live `omantel.biz` deployment `91dc05917e44d1c1` (2-region, converged 62/62 + 56/56) unless noted.

## Headline

Board went **35 → 12 gated open**. 22 issues closed with live evidence and independently audited — **22/22 clean, zero false-closes**. Seven more fixes merged this session but are roll-gated (the permanent env is pin-frozen). A fresh single-region keystone prov is firing on current main to close that gate.

## Closed this session — 22, audited 22/22 clean

Each was closed against live evidence on `91dc0591`, then re-audited independently. Zero false-closes.

#4454 #4467 #4448 #4458 #4437 #4442 #4444 #4447 #4446 #4471 #4354 #4460 #4436 #4479 #4473 #4290 #4459 #4450 #4482 #4464 #4432 #4468

Themes: janitor self-reap P0 (#4454), Cilium MTU cross-node TCP (#4467), the de-vcluster SSO seeding chain (#4448/#4458/#4437/#4446), host shared-pg reachability (#4442/#4460/#4436), org-controller as single provisioning producer + cascade teardown (#4290/#4471/#4459/#4479/#4473), gitea git-data preservation (#4354), handover-JWT key (#4450), sandbox token reflection (#4482), deploy-bump concurrency (#4464), catalog-seed permanence (#4432), plane-isolation deadlock-guard (#4444/#4468).

## Merged this session, roll-gated (inert on the pin-frozen permanent env)

| PR | Issue | What |
|---|---|---|
| #4493 | #4466 | janitor harden — log-only-until-proven + active-dep allowlist + protect-by-default |
| #4492 | #4477 | seed newapi admin-token into OpenBao (fresh-prov SSO seed) |
| #4495 | #4290/#4475 | org-controller renders gateway/apiserver CNP host-side for the vcluster tier |
| #4494 + #4496 | #4111 | bp-agenity imagePullSecrets=ghcr-pull + catalog-seed source.version lockstep |
| #4498 | #3969 | application-controller §13 — Continuum placement lease-witness off targets[] |
| #4497 | ledger | UAT ledger consolidation — 22 closed + honest gate map |
| #4500 | #4499 | bp-plane-isolation: catalyst-system → openbao allowIngressFrom — the root cause behind the #4477/#4277 seed faults (catalyst-api k8s-auth to OpenBao was Cilium-dropped) |

## The 12 gated open + the 3 gates

The permanent env is pin-frozen, so merged fixes are inert on it. Every remaining open ticket sits behind ONE gate.

### G1 — keystone single-region prov (FIRING now, kom4dc, GO-3EIP, current main)
The validation vehicle for everything that merged but can't roll on the permanent env.
- **#4488** crossplane provider-opentofu install
- **#4477** newapi-seed
- **#4466** janitor log-only
- **#4431** VPC-reclaim
- **#4486** DR self-fire (already `status/completed`)
- **#4499** seed-sync (merged via #4500, awaiting re-walk)
- **#4293** one vcluster = one Organization
- **#3969** application-centric placement basics
- **#4475** vcluster-tier CNP

### G2 — Omantel EIP quota bump 10 → ≥16 (the single decisive founder lever)
A 2-region prov needs **6** EIPs. kom4dc quota = **10**, free = **3**. No 2-region prov can fire until the quota rises to **≥16**.
- **#4275** region-kill failover (Pillar-3)
- **#4212** DR backbone — 2 unwired seams
- **#4293** multi-region facet

### G3 — Anthropic credential
A real Anthropic API token to seed into the per-Org OpenBao path. Without it the chat→provision agentic journey can't run end-to-end.
- **#4277** anthropic-seed value
- **#4111** agentic run

### Destructive-on-throwaway
- **#3379** sovereignty cutover — the deny-egress hold is destructive, so it runs on the keystone env, never the permanent one.

## The one lever that unblocks the most

**EIP quota bump 10 → ≥16** on kom4dc. It clears all three G2 tickets at once (region-kill #4275, DR seams #4212, multi-region #4293) by letting a 2-region prov fire (needs 6 EIPs; only 3 free today). The next-largest single lever is the **Anthropic credential**, which clears G3 (#4277 + #4111).

## Keystone prov in flight

A fresh single-region GO-3EIP keystone prov is firing on kom4dc against current main. It is the vehicle that rolls every merged-but-roll-gated fix onto a clean substrate and re-walks the G1 surfaces. The permanent `omantel.biz` env stays untouched (pin-frozen, precious).

## Hard capacity fact

kom4dc EIP quota = 10, free = 3; a 2-region prov needs 6. This single number is what stands between the merged DR/region-kill code and a verified multi-region walk.
