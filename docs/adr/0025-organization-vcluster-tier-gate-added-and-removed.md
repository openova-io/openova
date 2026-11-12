# ADR-0025 — The per-Organization vCluster tier gate, added 2026-06-25 and removed 2026-09-10

Status: Accepted
Date: 2026-11-13

Written by work package FND-106. It records a rule that was added and removed in code, 77 days apart, with no decision record for either step.

## Context

**Added, 2026-06-25.** Commit `724668b8d` made the organization-controller the one producer of an Organization's boundary. In the same change it added a "tier gate": the plan of the Organization chose the kind of boundary.

- Plan free or S: the Organization shared the bare host namespace `<slug>`. It got no vCluster.
- Plan M and above: the Organization got a dedicated vCluster.

The gate was a predicate behind a constant, `allTiersVcluster = false`. Its comment promised that one line would switch every plan to a vCluster.

**Copied, 2026-06-25 to 2026-08-13.** The predicate was copied into three more places, each with its own copy of the constant: the provisioning service of the signup funnel (commit `dbaf79add`), the API (commit `5eccebd53`) and the console. Behind the gate a second set of rendered files, a second shape of pod names, a second readiness rule and a second status stamp grew. Two test files, both named `tier_gate_lockstep_4292_test.go`, existed only to keep the copies equal.

**Removed, 2026-09-10.** The founder's direction of that day: one customer Organization has one vCluster, on every plan, with no rule that depends on the plan. Three commits removed the gate from the organization-controller (`ccc09169a`), from the provisioning service (`322e8a52d`) and from the API and the console (`82082f0a3`). The gate was deleted, not switched: by then the one-line switch was not safe to take, because four copies and the code behind them would have had to change together.

## Decision

1. **Every Organization is backed by a dedicated vCluster.** This holds for every plan (s, m, l, xl, an empty or unknown value) and for both kinds of Organization (customer and internal).
2. **The plan sizes the boundary and never selects it.** The plan sets the ResourceQuota, the LimitRange and the quality-of-service shape. It is read for nothing else on this path.
3. **There is no switch.** No constant, flag or value chooses between a host namespace and a vCluster for an Organization. A request that declares the isolation `namespace` is refused with status 422.
4. **The vCluster is the boundary of an Organization only.** Whether a platform plane runs in a vCluster is another question. Record 0019 is allocated to it.

## What pins the decision in code

- `core/controllers/organization/internal/gitops/manifests.go`: the comment above the boundary constants states the single boundary. The tests `TestRender_EveryPlanSlugIsVclusterBacked_NoTierGate` and `TestRender_BoundaryFileSetIsPlanIndependent` fail if a plan changes the rendered file set.
- `core/services/provisioning/gitops/every_plan_vcluster_test.go`: the test `TestEveryPlanIsVclusterBacked_NoTierGate`.
- `products/catalyst/bootstrap/api/internal/handler/organization_provisioning.go`: the constant `orgIsolation = "vcluster"` and the function `declaredIsolationConflict`. The tests are in `org_isolation_every_plan_test.go` in the same folder.

## Consequences

- An Organization on the smallest plan costs the control plane of one vCluster. The host namespace quota is the purchased plan plus that overhead (commit `81b235914`).
- An Organization that was created before 2026-09-10 on plan free or S may still be observed as a host namespace. The API reports that as an observation of an older object, not as a rule of the plan.
- The ledger row that asserted "a free or S Organization has no vCluster" was retired and replaced (`docs/ledger/uat-retirements.csv`).

## Why this is written down

The gate lived for 77 days and changed what a customer Organization received. Neither step had a decision record. ADR-0025 closes that gap. It changes no code.
