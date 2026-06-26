# Pre-flight checklist — fresh Sovereign provisioning (kom4dc / Huawei)

**Purpose:** a GO/NO-GO gate run BEFORE firing `POST /sovereign/api/v1/deployments`, so a fresh prov never hits the kom4dc VPC/EIP bottleneck and never lands on an unsafe substrate. This complements the *code-level* pre-flight already in `products/catalyst/bootstrap/api/internal/provisioner/provisioner.go:1841` (#4435, VPC-quota count) — this runbook is the operator's manual confirmation + the substrate-safety checks that code does not cover.

> Born from the 2026-06-26 P0 (`docs/sessions/2026-06-26/postmortem-janitor-self-reap.md`): a VPC-cleanup janitor reaped the live env. The checks below are the guard chain that incident defines.

## GO/NO-GO checklist

| # | Check | How | GO criteria |
|---|---|---|---|
| 1 | **VPC quota headroom** | Huawei API `GET /v1/{project}/vpcs` (me-east-215) — count `vpc`s | count < 5 (the hard project limit); after a clean wipe expect **1** (bastion) |
| 2 | **EIP pool headroom** | `GET /v1/{project}/publicips` — count allocated | free ≥ the prov's needs (apiserver ×2 + ELB ×2 + NAT ×1 per 2-region) |
| 3 | **No leaked orphans** | query VPCs / ELBs / detached `pvc-*` EVS / EIPs for `catalyst-…-<prefix>-*` names whose dep prefix matches NO live (non-wiped) deployment record | zero orphans (the wipe API's `tofuDestroyed` flag LIES — verify by direct API, see #4454 / a20803b8) |
| 4 | **Janitor is protect-by-default** | mothership `kubectl get deploy -n catalyst catalyst-api -o jsonpath='{...image}'` | image ≥ `fcb305f` (#4457 inversion) — confirm a `[JANITOR] … skipped (active deployment)` log on the new dep, NOT a reap (#4454) |
| 5 | **Chart delivers the fixes** | `git show origin/main:products/catalyst/chart/values.yaml \| grep -A2 organization-controller` then check the pinned SHA's source for the expected fix markers | org-ctrl image SHA contains the merged fixes (e.g. `resolvePoolPowerDNSAPIKey`, `TenantNetworkingFinalizer`) — guards the #4465 deploy-bump clobber class |
| 6 | **LE rate-limit headroom** | count distinct certs issued for the sovereign FQDN's registered domain in the last 7 days | < 50/week; else flip TLD `omantel.biz` ↔ `omani.works` (per L3 rotation) |

**NO-GO on any red** → remediate (canonical wipe of orphans via `POST /deployments/{id}/wipe`, EIP cooldown wait, roll the mothership, TLD flip) before firing.

## Wipe-confirmation evidence — 2026-06-26 (pre-re-prov, dep `b9f9590b558262b6` wiped)
Verified by direct Huawei AK/SK SigV3 query (NOT the lying wipe flag), both AZs me-east-215 a+b. After wiping the reaped dep + force-deleting 283 stranded detached `pvc-*` EVS volumes, the project held **only the bastion**:

| Resource | Count | Remaining |
|---|---|---|
| VPC | 1 | `bastion-openova-vpc` only |
| EIP/publicips | 1 | bastion `212.72.24.20` only (4 freed: `.14`/`.39`/`.30`/`.31`, in TTL cooldown) |
| ECS | 1 | `bastion-openova` only |
| EVS volumes | 2 | both bastion-attached |
| ELB loadbalancers | 0 | empty |
| NAT gateways | 0 | empty |
| VPC peerings | 0 | empty |

⇒ Checks 1-3 GREEN → the re-prov (`91dc05917e44d1c1`) fired with full VPC/EIP headroom.

## References
- Janitor self-reap P0: #4454 (fix #4457 protect-by-default inversion)
- Janitor hardening (log-only + active-dep allowlist + EVS-orphan sweep + cred-source): #4466
- Deploy-bump clobber (stale chart pins): #4465 (#4464)
- Post-mortem: `docs/sessions/2026-06-26/postmortem-janitor-self-reap.md`
