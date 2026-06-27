# Huawei National Cloud (Omantel) — Elastic IP quota-bump request

**Status:** ready to send · **Audience:** Omantel National Cloud account team → Huawei Cloud quota approval
**Region:** `me-east-215` (kom4dc) · **Project ID:** `f27698137bdc4b00ad509cf27f1e5547`
**Resource type:** `publicip` (Elastic IP / EIP) · **Requested by:** OpenOva platform engineering

---

## One-line ask

Raise the **`publicip` (EIP) quota** on project `f27698137bdc4b00ad509cf27f1e5547`, region `me-east-215`, from **10 → ≥ 16**.

---

## Why (business justification)

The permanent production Sovereign (`omantel.biz`) is a 2-region high-availability deployment on kom4dc. Its disaster-recovery / region-kill failover capability (the load-bearing Pillar-3 proof of the platform) has live, healthy plumbing — two independent CNPG clusters streaming RPO=0, a held witness lease, ClusterMesh 2/2 — but the **destructive validation drill** (kill region-a, prove region-b promotes with zero committed-transaction loss) has never been executed against a disposable environment.

We will not run a destructive failover drill against the permanent serving production environment except under a tightly-controlled low-traffic window with an operator present. The correct way to validate the DR/region-kill pillar repeatably is to stand up a **second, concurrent 2-region validation deployment** alongside the permanent one, run the drill there, capture the evidence, and tear it down — without ever touching production.

That second concurrent 2-region deployment needs **6 EIPs**. The current quota leaves only **3** free. So the validation is hard-capacity-gated on the EIP quota, not on engineering readiness. Raising the quota to **≥ 16** clears the gate and lets us validate region-kill, the DR backbone, and multi-region facets in one disposable prov, leaving the permanent `omantel.biz` env untouched.

---

## Current state (verified, read-only, 2026-06-27)

| Item | Value |
|---|---|
| Region | `me-east-215` (kom4dc) |
| Project ID | `f27698137bdc4b00ad509cf27f1e5547` |
| Current `publicip` quota | **10** |
| Currently used | **7** |
| Currently free | **3** |

### The 7 EIPs in use — itemized

| # | EIP role | Belongs to | Notes |
|---|---|---|---|
| 1 | **Bastion** EIP | Shared management infra (`bastion-openova`) | Permanent operations bastion — never wiped, not part of any Sovereign deployment. |
| 2 | region-a control-plane / apiserver EIP | Permanent `omantel.biz` Sovereign (region-a, `hw-me-east-215-a-rtz-prod`) | apiserver `212.72.24.12:6443`. |
| 3 | region-a NAT EIP | Permanent `omantel.biz` Sovereign (region-a) | Outbound egress for region-a workers. |
| 4 | region-b control-plane / apiserver EIP | Permanent `omantel.biz` Sovereign (region-b, `hw-me-east-215-b-rtz-prod`) | apiserver `212.72.24.26:6443`. |
| 5 | region-b NAT EIP | Permanent `omantel.biz` Sovereign (region-b) | Outbound egress for region-b workers. |
| 6 | primary ELB EIP | Permanent `omantel.biz` Sovereign | Customer/application traffic load balancer. |
| 7 | console ELB EIP | Permanent `omantel.biz` Sovereign | Dedicated console/api/marketplace load balancer (console-isolation). |

> Items 2–7 are the **6 EIPs** consumed by the single permanent 2-region production Sovereign; item 1 is the shared bastion. 10 − 7 = **3 free**.

---

## Why a 2-region validation prov needs 6 EIPs

A standard 2-region Catalyst deployment allocates EIPs as follows:

| EIP | Count | Per region |
|---|---|---|
| Control-plane / apiserver | 2 | 1 × region-a + 1 × region-b |
| NAT (egress) | 2 | 1 × region-a + 1 × region-b |
| Primary ELB (application traffic) | 1 | shared |
| Console ELB (console/api/marketplace) | 1 | shared |
| **Total** | **6** | |

Even with the console-isolation collapse (folding the console ELB into the primary ELB), a 2-region prov still needs **5** EIPs — which still exceeds the **3** currently free. So neither the full 6-EIP nor the collapsed 5-EIP form can fire today.

**3 free < 5 (collapsed) < 6 (standard)** ⇒ a second concurrent 2-region validation prov cannot be created at the current quota.

---

## The exact ask

| Field | Value |
|---|---|
| Quota name | `publicip` (Elastic IP) |
| Region | `me-east-215` |
| Project ID | `f27698137bdc4b00ad509cf27f1e5547` |
| Current limit | 10 |
| **Requested limit** | **≥ 16** |
| Headroom rationale | 7 (current usage) + 6 (one concurrent 2-region validation prov) = 13; request 16 to leave a small margin for transient EIP allocation during prov/teardown and for the console-isolation variant without re-requesting. |

A bump to **16** lets one full 2-region validation deployment run concurrently with the permanent production env, with margin. If Huawei caps the increase, **13** is the strict floor (7 + 6) that just barely admits one concurrent 2-region validation prov with zero headroom.

---

## What this unblocks once granted

A single quota approval clears the hard-capacity gate on three tickets at once, all of which are otherwise engineering-ready:

- **#4275** — Pillar-3 region-kill failover counter-test (kill region-a, prove region-b promotes RTO ≤ 30s, RPO = 0).
- **#4212** — DR object-model backbone (spine Application→Continuum producer + Crossplane Observe-first adoption runtime proof).
- **#4293 / #3969** — multi-region / application-centric placement facets that need a 2-region substrate to walk.

No code change is required to consume the new quota — the platform's standard 2-region prov path allocates the 6 EIPs automatically once the headroom exists.

---

## Submission notes (for the operator)

- This is a **quota increase request**, not a resource creation — it does not allocate any EIP by itself.
- Route via the Omantel National Cloud account team or the Huawei Cloud console **Quotas → publicip → request increase** for project `f27698137bdc4b00ad509cf27f1e5547`, region `me-east-215`.
- No production change occurs on approval; the new headroom simply makes a second concurrent 2-region validation prov possible. The permanent `omantel.biz` env is untouched throughout.
