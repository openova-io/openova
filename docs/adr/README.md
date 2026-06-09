# Architecture Decision Records (ADRs)

This directory holds **immutable** records of significant architectural decisions for the OpenOva platform.

Format: lightweight ADR (Status / Context / Decision / Consequences / Shipped-via). Once Accepted, an ADR is never edited in place — superseding decisions get a new ADR with `Status: Supersedes ADR-NNNN`.

## Index

| ADR | Title | Status |
|---|---|---|
| [0001](0001-catalyst-control-plane-architecture.md) | Catalyst control-plane architecture | Accepted |
| [0002](0002-post-handover-sovereignty-cutover.md) | Post-handover sovereignty cutover (8-tether pivot) | Accepted |
| [0003](0003-rbac-newapi-user-create-hook.md) | RBAC `newapi` user-create hook | Accepted |
| [0004](0004-cnpg-sync-replication.md) | CNPG Pillar 3 synchronous replication (remote_apply over ClusterMesh) | Accepted |
| [0009](0009-per-org-iac-repo-bootstrap.md) | Per-Organization IaC repo bootstrap on Org creation | Accepted |
| [0010](0010-reusable-shareable-backing-services.md) | Reusable, shareable backing-services model (declarative, no controller/Crossplane) | Accepted |

> **Numbering note:** slots 0005–0008 are intentionally reserved for in-flight EPIC ADRs (G92/G105/G108/G112 candidates). ADR-0009 picked its slot deliberately so it could land without ordering coupling — see the ADR-0009 header. The 0005–0008 gap is by design, not a missing-file anomaly.

## When to write an ADR

- A cross-Blueprint contract changes (e.g. event-spine schema, control-plane CRD versioning rule).
- A user-global Inviolable Principle gets a new clause.
- A Multi-Region DoD invariant is introduced or revised.
- A Blueprint-catalog default flips in a way that changes operator-visible behavior on fresh prov.

When unsure: file the ADR. Cheap to write, expensive to be wrong about.

## See also

- `docs/PRINCIPLES.md` — the 15 Inviolable Principles + anti-pattern catalog
- `docs/ARCHITECTURE.md` — current architecture (latest synthesis of all ADRs)
- `docs/DOD.md` — the 5-Pillar + Multi-Region DoD that ADRs preserve
