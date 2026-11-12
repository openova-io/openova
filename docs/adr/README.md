# Architecture Decision Records (ADRs)

This directory holds **immutable** records of significant architectural decisions for the OpenOva platform.

Format: lightweight ADR (Status / Context / Decision / Consequences / Shipped-via). Once Accepted, an ADR is never edited in place. A later decision gets a new ADR. A correction to an accepted ADR is one header line added above its body; the body is not edited.

## Index

| ADR | Title | Status |
|---|---|---|
| [0001](0001-catalyst-control-plane-architecture.md) | Catalyst control-plane architecture | Accepted |
| [0002](0002-post-handover-sovereignty-cutover.md) | Post-handover sovereignty cutover (8-tether pivot) | Accepted |
| [0003](0003-rbac-newapi-user-create-hook.md) | RBAC `newapi` user-create hook | Accepted |
| [0004](0004-cnpg-sync-replication.md) | CNPG Pillar 3 synchronous replication (remote_apply over ClusterMesh) | Accepted |
| [0009](0009-per-org-iac-repo-bootstrap.md) | Per-Organization IaC repo bootstrap on Org creation | Accepted |
| [0010](0010-reusable-shareable-backing-services.md) | Reusable, shareable backing-services model (declarative, no controller/Crossplane) | Accepted |
| [0011](0011-opentofu-crossplane-adoption-seam.md) | OpenTofu→Crossplane adoption seam (provider-opentofu Workspace, Observe-first) | Accepted |
| [0012](0012-dragonfly-p2p-registry-cutover.md) | Dragonfly P2P registry distribution + cloud-agnostic cutover (kills the kom4dc hairpin + the bastion proxy-cache) | Accepted |
| [0013](0013-cross-org-denial-shape.md) | Cross-Organization denial shape: 403 on assertion, not-found on lookup (MCP `create_application` vs `get_application`) | Accepted |
| [0014](0014-chargeback-usage-ledger-and-split-deployment.md) | Chargeback: usage ledger as the measurement primitive (not Crossplane), `bp-chargeback` multi-tenant application with split deployment (Sovereign BSS · per-Org · central National Cloud instance) | Proposed; superseded by [0028](0028-commerce-and-billing.md) |
| [0015](0015-region-symmetric-gitops-and-native-topology-transitions.md) | Region-symmetric GitOps delivery, one topology contract, native topology transitions (replaces the imperative switchover sequencer) | Accepted |
| [0016](0016-cloud-abstraction-contract.md) | Cloud abstraction contract: provider port, module contract version 1, cloud profile, provider packs, Workspace hand-over | Accepted |
| [0019](0019-zones-are-namespaces-and-an-edge-pool.md) | Zones are namespaces and an edge pool: platform planes are host namespaces, a vCluster is the boundary of an Organization only, the zone trial and its withdrawal; closes the numbers 0005 to 0008 | Accepted |
| [0020](0020-service-model-classes-and-capability-contracts.md) | Service model: classes, lifecycle, capability contracts, one catalogue source, function tests and kit rings | Accepted |
| [0022](0022-foundation-gate-release-train-and-execution-protocol.md) | Foundation gate, release train, stability register and execution protocol (supersedes the gate wiring of ADR-0015 decision D9) | Accepted |
| [0023](0023-sandbox-withdrawn.md) | Sandbox withdrawn; the per-Organization agent workspace and the tool server replace it | Accepted |
| [0025](0025-organization-vcluster-tier-gate-added-and-removed.md) | The per-Organization vCluster tier gate, added 2026-06-25 and removed 2026-09-10 | Accepted |
| [0028](0028-commerce-and-billing.md) | Commerce and billing: one billing product with two installation options, one owner per money entity, one plan source | Accepted |
| [0029](0029-compliance-plane.md) | Compliance plane: one read model, one model object in Git, policy mode and waivers by commit | Accepted |
| [0033](0033-access-plane.md) | Access plane: one Blueprint with three kinds, a fenced pod per session, credentials through the mount, sealed recordings, review by rules | Accepted |

> **Numbering note:** numbers 0005–0008 were reserved for records that were never written (see the ADR-0009 header). They are never used. Record 0019, allocated below, closes them.

## Allocated numbers

No record picks its own number. This table is the allocation of the numbers 0016 to 0038: the number, what the record decides, and the work package of `docs/ledger/plan.yaml` that writes it. A record is in the index above once it exists.

| Number | Record | Written by |
|---|---|---|
| 0016 | Cloud abstraction contract | CLD-070 |
| 0017 | Sovereign lifecycle: born-local bootstrap, hand-over, upstream contract, release payload and upgrade | SOV-620 |
| 0018 | Secrets and trust: injection, seal, supply-chain verification | SEC-006 |
| 0019 | Network and zones: platform planes are host namespaces; a vCluster is the boundary of an Organization only; the zone trial and its withdrawal. Closes the reserved numbers 0005 to 0008 | NET-050 |
| 0020 | Service model: classes and capability contracts | SVC-010 |
| 0021 | Control plane: Git-first write path, admission lock, events | CTL-060 |
| 0022 | Foundation gate, release train, stability register and execution protocol | FND-106 |
| 0023 | Sandbox withdrawn | FND-106 |
| 0024 | The primary substrate moved on 2026-05-22; status of the earlier substrate | CLD-071 |
| 0025 | The per-Organization vCluster tier gate, added and removed | FND-106 |
| 0026 | NodePort ban and the gateway serving model | NET-052 |
| 0027 | Region-loss contract: recovery point and recovery time per data class | CTL-800 |
| 0028 | Commerce and billing | BSS-010 |
| 0029 | Compliance plane | CMP-005 |
| 0030 | Model plane | AIP-060 |
| 0031 | Infrastructure plane | INF-070 |
| 0032 | Migration is a platform product | MIG-010 |
| 0033 | Access plane | ACC-060 |
| 0034 | Later products | PRD-020 |
| 0035 | Observability: one telemetry pipeline, alert events, declared objectives | FND-202 |
| 0036 | Backup and restore | FND-181 |
| 0037 | Managed cluster class on the reference cloud | NET-321 |
| 0038 | Firewall mechanism | NET-251 |

A later number is allocated in the plan. The next free number is the field `adr_next` of `docs/ledger/plan.yaml`. A work package of type `decision` carries its number in the field `adr`; FND-106 carries 22 there and also writes 0023 and 0025.

## Header lines of records from 0016 on

A record from 0016 on begins, directly under its title, with plain lines of the form `Field: value`, one field on a line, so that a tool can read them.

- `Status:` is required. Its value is `Proposed` or `Accepted`.
- `Supersedes: ADR-NNNN` names a record that the new one replaces. When only a part is replaced, text after the number says which part.
- `Unfreezes: <entry id>` names an entry of the stability register (`docs/ledger/stability.yaml`, which package FND-040 adds) whose frozen paths the decision allows to change.
- `Retires-Check: <check id>` names a gate check of `docs/ledger/gates.yaml` that the decision removes.
- The last three are optional and may repeat, one on a line. Other fields, such as `Date:`, may follow.

A tool matches these lines exactly, at the start of a line. Records 0001 to 0015 are older than this form and are not changed to it. None of them carries an `Unfreezes:` or a `Retires-Check:` line, so none of them permits a change to a frozen path or the removal of a gate check.

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
