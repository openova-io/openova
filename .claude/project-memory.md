# OpenOva Project Memory

> **Last Updated:** 2026-04-27 (Catalyst-unified rewrite)
> **Purpose:** Persistent context for Claude Code sessions about Catalyst platform strategy and architecture.

This file is now an **index** and **decision log**. The full architecture lives in [`docs/`](../docs/). When in doubt, the canonical docs win over this file.

---

## 1. Read these first

In strict order:

1. [`docs/GLOSSARY.md`](../docs/GLOSSARY.md) — terminology source of truth
2. [`docs/ARCHITECTURE.md`](../docs/ARCHITECTURE.md) — Catalyst architecture overview
3. [`docs/NAMING-CONVENTION.md`](../docs/NAMING-CONVENTION.md) — naming patterns
4. [`docs/PERSONAS-AND-JOURNEYS.md`](../docs/PERSONAS-AND-JOURNEYS.md) — who uses what
5. [`docs/SECURITY.md`](../docs/SECURITY.md) — identity, secrets, rotation
6. [`docs/SOVEREIGN-PROVISIONING.md`](../docs/SOVEREIGN-PROVISIONING.md) — bringing a Sovereign online
7. [`docs/BLUEPRINT-AUTHORING.md`](../docs/BLUEPRINT-AUTHORING.md) — writing Blueprints

If any older notes in this file contradict those docs, those docs win.

---

## 2. Core positioning (locked 2026-04-27)

- **OpenOva** = the company.
- **Catalyst** = the OpenOva platform (the control plane that turns a Kubernetes cluster into a self-sufficient deployment).
- **Sovereign** = a deployed instance of Catalyst.
- **Organization** = multi-tenancy unit inside a Sovereign.
- **Environment** = `{org}-{env_type}` scope where Applications run.
- **Application** = an installed **Blueprint**.
- **Blueprint** = the unified unit of installable software (replaces the older "module" + "template" split).

What was previously called **Nova** was just a Sovereign run by us hosting our SaaS Organizations. The "Nova" brand is retired in favor of "the openova Sovereign."

OpenOva's other products (**Cortex**, **Axon**, **Fingate**, **Fabric**, **Relay**, **Specter**, **Exodus**) are now positioned as composite Blueprints that run **on** Catalyst — not as parallel platform layers.

---

## 3. Stack decisions (locked 2026-04-27)

| Concern | Choice | Notes |
|---|---|---|
| **Event spine** | NATS JetStream | Apache 2.0 (no BSL risk); native KV; native multi-tenant Accounts. Replaces the older "Redpanda + Valkey" combo for the **control plane** only. Application-level event needs choose freely (Redpanda, Kafka, NATS, RabbitMQ). |
| **Secrets** | OpenBao + ESO | Apache 2.0 fork of Vault (LF-led, IBM-backed). Replaces Vault. |
| **Multi-region OpenBao** | Independent Raft per region + async perf replication | NOT a stretched cluster. Each region is its own failure domain. |
| **Workload identity** | SPIFFE/SPIRE | 5-min rotating SVIDs, mTLS everywhere. |
| **User identity** | Keycloak | Per-Org realm in SME-style Sovereigns; per-Sovereign realm in corporate-style. SME tier uses minimal single-replica Keycloak (no HA). |
| **GitOps** | Flux per vcluster | Lightweight (source + kustomize + helm controllers). One Flux per vcluster, watching its Environment Gitea repo. |
| **Git** | Gitea | Per-Sovereign. Hosts public Blueprint mirror, Org-private Blueprints, per-Environment workspace repos. |
| **IaC for non-K8s** | Crossplane | Only IaC. Never user-facing. Advanced users author Compositions as Blueprints. |
| **Bootstrap IaC** | OpenTofu | One-shot only. Archived after Phase 0. Crossplane takes over. |
| **Multi-tenancy** | vcluster (loft.sh) | One per Organization per host cluster. |
| **CNI / Service Mesh** | Cilium | eBPF mTLS, L7 policies, Gateway API. |
| **Bootstrap host** | catalyst-provisioner.openova.io | Permanent service. Each Sovereign is fully self-sufficient after Phase 0; provisioner stays online for the next Sovereign. |

---

## 4. User-facing surfaces (locked 2026-04-27)

Three first-class surfaces. **No fourth.**

- **UI** — Catalyst console. Form / Advanced / IaC editor depths. Default for all personas.
- **Git** — direct push or PR to the Environment Gitea repo (or Blueprint repos). Equal weight with UI.
- **API** — REST + GraphQL for portal integrations (Backstage, ServiceNow). Not a primary IaC surface.

`kubectl` is debug-only, scoped to one's own vcluster. No Terraform/Pulumi/CLI for production changes.

---

## 5. Banned terms

Replaced terms — never use in new docs, code, UI strings:

| Banned | Use instead |
|---|---|
| Tenant | Organization |
| Operator (entity / person) | `sovereign-admin` (role) |
| Client (UX sense) | User |
| Module / Template (Catalyst sense) | Blueprint |
| Backstage | Catalyst console |
| Synapse (the OpenOva product) | Axon |
| Lifecycle Manager (separate) | Catalyst |
| Bootstrap wizard (separate) | Catalyst bootstrap |
| Workspace (Catalyst scope) | Environment |
| Instance (user-facing object) | Application |

Full glossary: [`docs/GLOSSARY.md`](../docs/GLOSSARY.md).

---

## 6. Sovereign topology

```
catalyst-provisioner (always on)  ──Phase 0──►  Target cloud (Hetzner / AWS / etc.)
                                                       │
                                                       ▼
                                           Sovereign deployment:
                                           ─ Management cluster (mgt)
                                             - Catalyst control plane
                                             - Gitea, JetStream, OpenBao,
                                               Keycloak, projector, …
                                           ─ Workload clusters (rtz, dmz)
                                             - Per-Org vclusters
                                             - Each with lightweight Flux

After Phase 0: Sovereign is self-sufficient. Provisioner is no longer in the path.
```

See [`docs/SOVEREIGN-PROVISIONING.md`](../docs/SOVEREIGN-PROVISIONING.md) for full details.

---

## 7. Promotion model (no chain object)

There is no `ApplicationGroup` or `ChainPolicy` CRD. Promotion is the act of copying an Application's manifest from one Environment Gitea repo to another, gated by an `EnvironmentPolicy` attached to the destination Environment.

The Blueprint detail page in the console is the cross-Environment view: it shows every Application using a given Blueprint across all Environments in the Org, with version drift visible at a glance.

---

## 8. Multi-region semantics

- Clusters named by **building block, not failover role.** Same building blocks deployed in multiple regions; k8gb routes traffic. Section 1.3 of `docs/NAMING-CONVENTION.md`.
- Each region's OpenBao is an **independent** Raft cluster with async perf replication. No stretched clusters. See `docs/SECURITY.md` §5.
- Catalyst Environment is a **logical** scope realized by N vclusters across regions — Placement metadata on each Application controls fan-out.

---

## 9. Naming changes vs older docs

| Old | New |
|---|---|
| `{env}` dimension in NAMING-CONVENTION | `{env_type}` |
| "Workspace" (Catalyst scope) | "Environment" |
| "Tenant" (anywhere) | "Organization" |
| "Bootstrap mode" / "Manager mode" of `core/` app | Both fold under "Catalyst control plane" |
| Catalyst as a sub-product | Catalyst as the platform itself |
| Cortex / Fingate / etc. as products | Composite Blueprints running on Catalyst |
| OpenBao multi-region as stretched | OpenBao multi-region as independent Raft + async perf replication |
| Vault | OpenBao |
| Redpanda (control plane) | NATS JetStream |
| Valkey (control plane) | NATS JetStream KV (Valkey remains as Application Blueprint) |

---

## 10. Component count

The historical "52 components" framing is retained at the marketing level for continuity, but the platform's identity is now **Catalyst**, not "the 52 components." Components are Blueprints. The list is in [`docs/PLATFORM-TECH-STACK.md`](../docs/PLATFORM-TECH-STACK.md). Adding or removing components is a Blueprint addition or removal — does not require any platform-level rebrand.

---

## 11. Customer sync (unchanged in spirit)

Each Sovereign's Gitea mirrors the public Blueprint catalog from this repo. Pull cadence is Sovereign-local; air-gapped Sovereigns mirror offline. See [`docs/SOVEREIGN-PROVISIONING.md`](../docs/SOVEREIGN-PROVISIONING.md) §9.

---

## 12. Open follow-ups (post-rewrite)

- Per-Blueprint `README.md` audit — most are clean; remaining cleanup tracked in issue #37.
- `core/` directory may be reorganized to match the Catalyst component naming (no urgency; functional code unchanged).
- Specter and Exodus positioning: Specter is a composite Blueprint (`bp-specter`) installed by default in corporate-style Sovereigns; Exodus is a deliverable migration service (people + playbooks), not a Blueprint. Documented at length in `docs/BUSINESS-STRATEGY.md`.

---

## 13. Approved key phrases

- "Cloud-native is the foundation. Catalyst is how you operate it."
- "Catalyst — the OpenOva platform."
- "A Sovereign is a self-sufficient deployment of Catalyst."
- "Nova was just a Sovereign run by us. Now we say 'the openova Sovereign'."
- "Same code in every Sovereign — whether run by us, by Omantel, or by Bank Dhofar."

---

## 14. Phrases to avoid

- "Tenant" anywhere in product context.
- "Operator" as an entity (the role is "sovereign-admin").
- "Module" / "Template" in the Catalyst sense.
- "Backstage" — replaced.
- "Lifecycle Manager" or "Bootstrap wizard" as separate products.
- "Stretched cluster" in OpenBao context — we deliberately reject that pattern.
- "Workspace" as Catalyst scope — replaced by Environment.

---

*Older sections from earlier project-memory revisions removed during the 2026-04-27 unified rewrite. Historical decisions remain captured in git log of this repository if needed.*
