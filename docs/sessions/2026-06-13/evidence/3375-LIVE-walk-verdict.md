# #3375 TOPOLOGY / DR — live-walk verdict on hw130 (2026-06-13)

Env: `console.hw130.omantel.biz` · deployment `526540f27f4acfbc` · region-A
`/tmp/hw130-a.kubeconfig`, region-B `/tmp/hw130-b.kubeconfig`. kom4dc 2-VPC mimic
(me-east-215-a / -b). All measurements re-queried live this session.

## DoD box-by-box

| DoD box | State | Evidence |
|---|---|---|
| 1. Schema reconciliation — ONE canonical shape (`spec.topology`), orphan `placementSchema` flagged for removal, CRDs `CreateReplace` | **DONE** (merged PR #3395) | `docs/topology-matrix.md` §"Canonical declaration shape" names `spec.topology` canonical, `placementSchema` the orphan. CRD CEL in `products/catalyst/chart/crds/blueprint.yaml`. |
| 2. `docs/topology-matrix.md` — all 90 rows from the agreement table + convergence columns, zero blank classes, amendments flagged | **DONE** (merged #3395; +oidc-gate this PR) | The durable registry is on main; this PR adds the `bp-oidc-gate` row + amendment. |
| 3. Declarations converged + CI lint with red-proof + G116 admission gate | **DONE** (merged #3395; +oidc-gate this PR) | `scripts/test-topology-admission-gate.sh` → `ALL CASES PASS` (91 blueprints green + 4 red cases incl. G116-missing-topology). |
| 4. Cluster-ID registry — `mgmt-A/B rtz-A/B dmz-A/B` resolve to real per-region kubeconfig Secrets; `KubeConfigSecretFor` wired; unit test + live kubectl proof | **DONE** (merged #3395) | `core/controllers/internal/clusterregistry/registry.go` + `registry_test.go` (green). Wired at `application_controller.go:380 clusterResolver()`. Live: `vc-rtz`/`vc-mgmt`/`vc-dmz` Secrets present, `vc-rtz.config` decodes to a valid kubeconfig (`current-context: vcluster`). |
| 5. G115 grafana datasource+dashboard provisioning | DONE within #3395 scope (grafana row) | — |
| **6. cnpg-pair continuum-driven switchover walk; RTO/RPO measured** | **PARTIAL — continuum machinery walked LIVE; full region-kill DEFERRED to fresh-prov (#3307 VPC peering)** | see below |
| 7-9. gitea / openbao region-kill walks + rejoin | DEFERRED — same cross-VPC datapath gate | CLASS-B mechanisms; gated on #3307 |

## DoD-6 — continuum-driven walk, measured live

The continuum machinery was driven END-TO-END against the live `Continuum` CR
(`cnpg/cnpg-pair-bp-cnpg-pair-continuum`) via its own API (`POST
/v1/continuums/.../dry-run`, port 8082). This is the continuum-controller's OWN
read of switchover-readiness — not a hand assertion.

**Healthy (walked):**
- Both cnpg clusters **3/3 "Cluster in healthy state"** — region-A primary +
  region-B replica (the #3322/#3400 own-cluster netpol carve-out healed the
  replica's intra-cluster HA join). Evidence: `3375-LIVE-cnpg-pair-both-regions.txt`.
- The continuum dry-run produced the full **7-step plan**: step-1 validate-lease
  OK, step-5 swap-lease computes `Release(region-a) → Acquire(region-b, ttl=30s)`.
  Evidence: `3375-LIVE-continuum-driven-switchover-dryrun.json`.

**The honest gate (DEFERRED, measured — not faked):**
- The continuum sequencer's **step-2 BLOCKS**:
  `cannot resolve cluster-pair "cnpg-pair-bp-cnpg-pair" in ns "cnpg":
  pair incomplete (primary=true replica=false)`. The continuum does not see the
  region-B replica as a streaming member of the pair.
- Root cause, measured directly on the region-B replica:
  `pg_last_wal_receive_lsn() = 0/5000000` (the initial base backup only, frozen
  for 24h+) and `pg_stat_wal_receiver` count = **0** (no active cross-region
  receiver). Evidence: `3375-LIVE-cross-vpc-wal-receiver.txt`.
- The continuum-controller log shows the DNS-quorum lease cannot be held
  (`lease lost; new holder in charge — witness: lease held by another holder`,
  every 10s) — the cross-VPC datapath that the quorum needs is down.

**Why deferred, not faked:** promoting the region-B replica now would advance the
DB to a WAL position 24h stale (everything since the base backup lost) — a
fabricated PASS. The region-kill walk becomes valid the moment the cross-region
**VPC-peering datapath (#3307)** is established so the replica streams WAL. On the
kom4dc 2-VPC mimic that peering is bootstrap-time IaC — correctly validated on the
**fresh 2-region prov (Phase 3)**, where #3307 establishes peering at boot, NOT a
live patch on this env (zero-touch rule). This matches the founder's own
2026-06-12 root-cause comments on #3375.

## This-PR delta (residuals beyond #3395)
- `bp-oidc-gate`: completed its `perTopology` contract (it declared
  `supported: [active-passive, singleton]` but shipped no perTopology block — an
  HA-supported topology with no replication/switchover/placement). Now matches its
  stateless catalyst-cp peer `bp-sso-bridge` (mgmt-A active / mgmt-B passive,
  replication none, switchover bp-continuum). Matrix row + ROW-AMENDMENT added.
- G116 gate re-run green (91 blueprints) with the completed declaration.
