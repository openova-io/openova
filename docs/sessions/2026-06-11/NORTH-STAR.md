# North Star — founder-ratified 2026-06-11

> The business deliverables this track exists to ship, in priority order.
> Every item resolves to walked evidence in `docs/ledger/UAT.md` (screenshot +
> wire-capture), never a merged PR. Operating model: `docs/sessions/2026-06-10/STATE-OF-PLAY.md` §Execution-model.
> **Excluded by founder directive (repeated 5+): AI Sandbox / Pillar-4 — owned by another project.**

## Business deliverables

| # | Deliverable | What it lets you sell | Status |
|---|---|---|---|
| 1 | **Provable BCP/DR** — region-kill failover ≤60s, zero tx loss | Enterprise/bank differentiator: regulator-grade RPO/RTO | #3243 last fix; walk follows |
| 2 | **Working revenue funnel** — voucher → checkout → Org → tenant login | The Omantel→SME sales motion; no funnel = no billable customer | #3122 fix merged, unproven |
| 3 | **Enterprise isolation** — vCluster containment (DMZ/MGMT/RTZ + per-Org) | The multi-tenancy boundary every enterprise audits first | re-walk on hw126; weakest live claim |
| 4 | **Sovereign independence** — cutover + 10-min egress-block proof | The name promise: customer survives losing us | never executed; rides a hw126 handover |
| 5 | **"Any cloud, same product"** — Hetzner leg on the shared bootstrap | Market beyond kom4dc; provider de-risk | Huawei leg proven; Hetzner = one prov |

## TOPOLOGY track (active pipeline — hw126)

| # | Deliverable | Acceptance artifact |
|---|---|---|
| T1 | ClusterMesh A↔B fully wired | `verify-sovereign-convergence.sh` all-green |
| T2 | cnpg-pair across the 2 VPCs (#3236) | CNPG primary + replica `streaming` over the mesh |
| T3 | Region-kill failover walk (#3195/#3189) | kill region-A → promote ≤60s, 0 tx lost → failback; timed screenshots |
| T4 | vCluster containment | app pods inside rtz/dmz/mgmt; only substrate on host |
| T5 | Shared backing-services (#3188) | 1 shared PG engine + 2 consumers healthy |

## SSO track (walkable when hw126's console serves — fixes merged, zero walked)

| # | Deliverable | Acceptance artifact |
|---|---|---|
| S1 | OpenBao ZERO-click (#3226) | logged-in DOM screenshot, no click |
| S2 | No dead Open buttons (#3224) | bp-newapi/flow-server: none; grafana/harbor: works |
| S3 | pdns UI reachable + compliant (#3225) | `pdns.<fqdn>/` → pdns-admin UI + SSO lands |
| S4 | Full SSO matrix (#3150/#2743) | every ssoEnabled app lands logged-in — one UAT row per app |

## FUNNEL track

| F1 | voucher → checkout → Org → tenant login (#3122 e2e) | real tenant Org + customer-login screenshot; unlocks per-Org-realm SSO fan-out + ~85 per-app rows |
