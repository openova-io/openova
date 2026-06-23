# Fresh validation-prov EIP-headroom check — 2026-06-23

**Goal**: fire a SECOND coexisting Huawei (kom4dc HCS `me-east-215`) Sovereign to prove
the hardened `main` (8 fixes: #4156/#4157/#4159/#4161/#4162/#4165 + roots #4158/#4160/#4163)
converges zero-touch, WITHOUT touching the protected permanent omantel.biz (`4635277cae4ffed9`)
or the bastion (`bastion-openova` / `212.72.24.20`).

## The single gating question: EIP/floating-IP pool headroom (CHECKED, not assumed)

Queried the live HCS EIP quota + allocation via AK/SK SigV3 (SDK-HMAC-SHA256) against
`https://vpc.me-east-215.kom4dc.nationalcloud.om`, executed from the mothership
`catalyst-api` pod (only path with kom4dc egress). AK/SK from
`/var/lib/catalyst/tofu/4635277cae4ffed9/tofu.auto.tfvars.json`.

**EIP quota** (`GET /v1/<project>/quotas?type=publicIp`):
```
{"quotas":{"resources":[{"type":"publicIp","used":7,"quota":10,"min":0}]}}
```
→ **used=7, quota=10, 3 free.**

**All 7 allocated EIPs** (`GET /v1/<project>/publicips`), resolved by bandwidth-name tag:

| EIP | Owner | Protected? |
|---|---|---|
| 212.72.24.20 | bastion (`bastion-openova-bw`) | YES — never touch |
| 212.72.24.1  | omantel.biz me-east-215-a cp1 | YES — permanent env |
| 212.72.24.43 | omantel.biz me-east-215-b cp1 | YES |
| 212.72.24.28 | omantel.biz nat-preflight-1 | YES |
| 212.72.24.36 | omantel.biz nat-preflight-2 | YES |
| 212.72.24.31 | omantel.biz ELB | YES |
| 212.72.24.33 | omantel.biz ELB | YES |

**Zero orphans to reclaim.** All 7 are bastion (1) + omantel.biz (6). Stale `.nodeip`
files for prior wiped provs (a24f2c86, b40997687, ...) have NO matching kubeconfig — those
clusters are gone and their EIPs already released (hence the clean 7).

## EIP demand of a fresh prov (structural, unconditional — from the live IaC)

Four `huaweicloud_vpc_eip` resources in `main.tf`:
- `cp`           — `for_each = regions` → 1 per VPC
- `nat`          — `for_each = regions` → 1 per VPC
- `elb_primary`  — singleton → 1
- `elb_console`  — singleton → 1  (#4054 console-isolation dedicated EIP)

Workers take NO EIP (NAT egress, A2 design).

- **Canonical 2-VPC ("multi-region") prov = 6 EIPs.**
- Degenerate 1-VPC prov = 4 EIPs.

Both exceed the **3 free**.

## Verdict

A coexisting validation prov CANNOT fire: needs 6 (canonical) or 4 (single-VPC) EIPs,
only 3 free. The HCS `quota=10` is operator-set by Oman National Cloud (kom4dc) — not
tenant-self-raisable. This matches the prior `error allocating EIP: conflict` failure mode
(memory `feedback_wipe_old_sovereign_before_reprov_kom4dc_eip_pool`): EIP pool is the
scarce resource, not cores.

This is NOT "no Hetzner creds / no 2nd region" (the prior agent's wrong framing). The
topology IS single-HCS-region multi-VPC and the creds ARE present. The wall is purely the
**3-vs-6 EIP gap** while the protected omantel.biz legitimately holds 6.

## Options to actually run the zero-touch validation (none taken autonomously here — all
## either need founder input or would impact the protected env)

1. **Founder raises the kom4dc EIP quota** (10 → ≥16) — then a coexisting 6-EIP prov fits.
   This is the clean path; it leaves omantel.biz untouched.
2. **Wipe omantel.biz first, then fresh-fire on the freed pool** — frees 6 EIPs, gives a
   pristine zero-touch fire. But omantel.biz is the PROTECTED permanent env (explicitly
   off-limits this session), so NOT done.
3. Wait until omantel.biz is intentionally re-provisioned, and validate the hardened
   `main` on THAT fire (it pins the same 8 fixes).

## State of the thing-under-test (ready the moment EIPs free up)

All 8 fixes confirmed on `origin/main`: #4156 (d710a39e2), #4157 (93895c629),
#4159 (f1416fa52), #4161 (45dd31907), #4162 (77ddef2b2), #4165 (ea9cb8706).
bp-keycloak `1.4.38`; bp-wordpress-tenant pin `0.4.12`. Nothing in the platform blocks the
validation except pool capacity.
