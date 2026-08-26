# ADR-0004 — CNPG Pillar 3 synchronous replication (remote_apply over ClusterMesh)

Status: Accepted
Date: 2026-05-20

> **Status (2026-08-26) — errata (ADR body is immutable; note only):** the §Consequences RTT example "~10 ms FSN↔HEL" cites the legacy Hetzner regions (Falkenstein↔Helsinki). The current substrate is **Huawei kom4dc** (regions `me-east-215-a` / `me-east-215-b`); FSN↔HEL stands only as a dated, provider-agnostic inter-region example.

## Context

Pillar 3 of the 5-Pillar DoD requires **zero transactions lost** on region kill. The original `bp-cnpg-pair` Blueprint was configured for asynchronous streaming replication (`blueprint.yaml:161` admitted "asynchronous-streaming"; `DESIGN.md:66` deferred sync to a future Blueprint). Asynchronous replication leaves a window of in-flight transactions unreplicated when the primary region dies — the canonical Pillar-3 claim is unachievable while replication is async.

## Decision

Configure CNPG primary cluster with:

- `synchronous_commit: "remote_apply"`
- `synchronous_standby_names: "FIRST 1 (<replica-cluster-name>)"`

Default `replication.mode: sync` in `bp-cnpg-pair` `values.yaml`. Replication flows over Cilium ClusterMesh on **WireGuard-over-public-IPs** DMZ (Multi-Region DoD A2 invariant — see `docs/DOD.md` §A2).

## Consequences

**Trade-off:** every commit pays inter-region RTT (~10 ms FSN↔HEL, ~100 ms continental-to-continental). If the replica is unreachable, the primary **BLOCKS new writes** — this is the by-design Pillar-3 contract: availability sacrificed for durability when the platform claims zero-tx-loss.

**Break-glass:** `ALTER SYSTEM SET synchronous_standby_names = ''` clears the sync requirement; documented in `bp-cnpg-pair/DESIGN.md` §7 (Break-Glass / DR runbook).

**Observability:** Continuum exposes `cnpg_sync_lag_bytes` and `cnpg_in_flight_commits` per primary; SLO-driven alerts fire if replica falls behind by >0 bytes for >30 s.

## Shipped via

- **PR #2071** — `bp-cnpg-pair` 0.1.2 + `bp-wordpress-tenant` 0.3.2 (default sync mode)
- **PR #2087** — `bp-cnpg-pair` pre-merge guard (CI enforces `replication.mode=sync` default cannot regress)
- **PR #2093** — `bp-cnpg-pair` smoke-render guard elevated to pre-merge

## Related

- **PR #2072** — `bp-continuum` bootstrap-kit slot (failover orchestrator consumes sync state)
- **PR #2073** — Generic install path for `bp-cnpg-pair` from Catalyst console
- **PR #2074** — Continuum CR per tenant (per-Org failover policy)
- **PR #2075** — D31 acceptance test harness (region-kill regression gate)

## See also

- `docs/DOD.md` §Pillar-3 (durability + zero-tx-loss)
- `docs/ARCHITECTURE.md` §7.1 (data-tier composition) + §10.7 (EPIC-2 acceptance)
- `platform/cnpg/DESIGN.md` + `products/catalyst/charts/bp-cnpg-pair/DESIGN.md`
