# Glossary

> **Status:** Canonical. Single source of truth for OpenOva terminology.
> **Updated:** 2026-04-27.
> **Note:** Terms here describe the agreed model. For which terms map to currently-implemented code vs design-stage, see [`IMPLEMENTATION-STATUS.md`](IMPLEMENTATION-STATUS.md).

Every other document defers to this file. When a term in another doc looks contested, this file wins. New terminology is proposed here first, then propagated.

---

## Core nouns

| Term | Definition |
|---|---|
| **OpenOva** | The company. Authors and maintains Catalyst, Blueprints, and the openova.io services. Never used to name a product (use **Catalyst** for the platform). |
| **Catalyst** | The OpenOva platform itself. A self-sufficient Kubernetes-native control plane composed of console, marketplace, admin, projector, catalog-svc, workspace-controller, blueprint-controller, identity, secret, and event-spine components. Published as signed OCI Blueprints in this repository. |
| **Sovereign** | One **deployed** instance of Catalyst on Kubernetes infrastructure chosen by its owner. Self-contained; never depends at runtime on any other Sovereign. Examples: `openova` (run by us, hosts our SaaS Organizations), `omantel` (run by Omantel, hosts SME Organizations across Oman), `bankdhofar` (run by the bank, hosts internal Organizations). |
| **Organization** | The multi-tenancy unit inside a Sovereign. Has billing, users, Environments, private Blueprints. Ahmed's pharmacy is one Organization on the `omantel` Sovereign; `digital-channels` is one Organization on the `bankdhofar` Sovereign. |
| **Environment** | An env-typed scope (`{org}-prod`, `{org}-dev`, `{org}-uat`, `{org}-stg`, `{org}-poc`) where Applications run. **Logical** concept; can span multiple regions and building blocks via Placement. Backed by one Gitea repo and one or more vclusters. |
| **Application** | What a User installs into an Environment from a Blueprint. The user-facing object: an App Store-style card representing the WordPress, Postgres, RocketChat, etc. that is running. |
| **Blueprint** | The reusable, OCI-published, signed unit of installable software. Unifies what previously was split between "module" (primitive) and "template" (composition). A Blueprint can declare dependencies on other Blueprints, with arbitrary depth. Visibility: `listed` (catalog card) / `unlisted` / `private` (Org-scoped). |
| **User** | A person. Authenticates via Keycloak. Belongs to one Organization (or has cross-Org admin scope as `sovereign-admin`). |

---

## Roles

| Term | Definition |
|---|---|
| **`sovereign-admin`** | The role for Users who operate a Sovereign — runs Crossplane, configures Catalyst, onboards Organizations. Omantel's cloud team and Bank Dhofar's platform team are both `sovereign-admin` on their respective Sovereigns. There is no separate entity-noun (rejected: "operator", "tenant", "client"). |
| **`org-admin`** | Role within one Organization — creates Environments, manages Users, sets Org-level policies. |
| **`org-developer`** | Role with Application install/configure rights inside specific Environments. |
| **`org-viewer`** | Read-only role within an Organization. |
| **`security-officer`** | Role with audit/policy/secret-rotation gating rights. Optional Org-level role. |
| **`billing-admin`** | Role with billing/invoice/quota rights. Optional Org-level role. |
| **`sme-end-user`** | Persona, not a role: an SME owner (Ahmed) for whom Organization onboarding is automatic on first signup. |

---

## Infrastructure

| Term | Definition |
|---|---|
| **Cluster** | A physical Kubernetes cluster. Named per [`docs/NAMING-CONVENTION.md`](NAMING-CONVENTION.md): `{provider}-{region}-{building-block}-{env_type}` — e.g. `hz-fsn-rtz-prod`. Owned by `sovereign-admin`. Never user-facing. |
| **vcluster** | A virtual Kubernetes cluster (loft.sh's vcluster) running inside a parent Cluster. One vcluster per Organization per parent Cluster. Named `{org}` within the parent (qualified globally as `{provider}-{region}-{bb}-{env_type}-{org}`). Implementation detail of an Environment; not user-facing. |
| **Building Block** | A functional security zone — `rtz` (restricted trust), `dmz` (edge), `mgt` (management). Stable across failovers. See `NAMING-CONVENTION.md` §1.3. |
| **Region** | Geographic location, provider-scoped 3-char code (`fsn`, `nbg`, `hel`). |
| **Env Type** | Environment dimension value: `prod | stg | uat | dev | poc`. Was `{env}` in older naming; renamed to disambiguate from the Catalyst Environment object. |
| **Placement** | Per-Application metadata declaring which regions and building blocks realize that Application. Modes: `single-region`, `active-active`, `active-hotstandby`. |

---

## Catalyst components (the control plane)

| Component | Purpose |
|---|---|
| **console** | Primary user-facing UI. Three depths: form view (default for SME), advanced view (default for corporate), in-browser Monaco IaC editor (toggle). All commits to the Environment Gitea repo. |
| **marketplace** | Public-facing Blueprint card grid (the "App Store"). |
| **admin** | Sovereign-level admin UI. Where `sovereign-admin` configures the deployment. |
| **catalog-svc** | Reads Blueprint CRDs (sourced from public/private Gitea repos), serves catalog API to console + marketplace. |
| **projector** | CQRS read-side service. Subscribes to NATS JetStream, materializes per-Environment KV, serves SSE to console, handles Gitea webhooks (forces Flux reconcile on commit). |
| **workspace-controller** | Reconciles the **Environment CRD**: creates the vcluster(s), bootstraps Flux inside, creates the Gitea repo, wires the webhook, generates pull credentials. |
| **blueprint-controller** | Watches Blueprint repositories (public + Org-private), validates and registers Blueprint CRDs. |
| **identity** | Keycloak: per-Organization realm in SME-style Sovereigns; per-Sovereign realm in corporate-style. Plus SPIFFE/SPIRE for workload identity. |
| **secret** | OpenBao + External Secrets Operator (ESO). Independent Raft cluster per region (no stretched cluster); cross-region perf replication is async. |
| **event-spine** | NATS JetStream — pub/sub + Streams + KV bucket. Workload-identity-scoped accounts per Environment. Replaces what was previously specified as "Redpanda + Valkey" for the control plane. |
| **gitea** | Per-Sovereign Git server. Hosts the Blueprint mirror, Org-private Blueprints, and per-Environment workspace repos. |
| **billing** | Per-Org metering, invoicing. |
| **observability** | Per-Sovereign OpenTelemetry + Grafana stack for Catalyst's own telemetry. |

---

## Persona-facing surfaces

| Term | Definition |
|---|---|
| **UI** | The Catalyst console — full feature, three view depths. |
| **Git** | Direct push or pull-request to the Environment's Gitea repo, or to private Blueprint repos. |
| **API** | Catalyst REST + GraphQL. Used for portal integrations (a customer's Backstage / ServiceNow / JIRA). Not a primary IaC authoring surface. |
| **kubectl** | Inside a User's own vcluster, for **debugging only**. Not a configuration surface. |
| **Crossplane** | **Platform plumbing**, never a user surface. Used internally by Blueprints to manage non-K8s cloud resources. Advanced users may author their own Crossplane Compositions and contribute them upstream as Blueprints. |

---

## Banned terms (do not use in any docs / UI / API)

| Banned | Use instead | Reason |
|---|---|---|
| Tenant | Organization | Cloud-overloaded, ambiguous between Sovereign tenancy and Organization tenancy. |
| Operator (as entity / person) | `sovereign-admin` (the role) | Confused with the K8s Operator pattern. (K8s Operators in the controller-pattern sense are still called Operators.) |
| Client (in product UX sense) | User | Collides with Keycloak OIDC client. (OIDC clients in technical contexts remain "clients".) |
| Module | Blueprint | Catalyst unifies modules + templates. (Go module, Terraform module, Helm module are exempt — they're external technologies.) |
| Template | Blueprint | Same reason. (K8s template, prompt template, scaffold template are exempt — external technologies.) |
| Backstage | Catalyst console | Backstage was decided removed from the platform. |
| Synapse (as a product) | Axon (the OpenOva product) or Matrix/Synapse (the chat server) | Matrix's Synapse server is fine when context is the chat server. |
| Lifecycle Manager (separate product) | Catalyst | Lifecycle management is one of Catalyst's responsibilities, not a separate product. |
| Bootstrap wizard (separate product) | Catalyst bootstrap | Bootstrap is one phase of Catalyst provisioning. |
| "Workspace" (as Catalyst scope) | Environment | Renamed for industry alignment and to escape collision with VS Code / Slack / Backstage / Terraform workspaces. |
| "Instance" (as user-facing object) | Application | App Store metaphor. CRD remains internal. |

---

## Acronyms

| Acronym | Expansion |
|---|---|
| **OCI** | Open Container Initiative — registry artifact standard. Blueprints are published as OCI artifacts. |
| **CRD** | Custom Resource Definition — Kubernetes typed extension object. |
| **CQRS** | Command-Query Responsibility Segregation — write side (Git → Flux → K8s) vs read side (projector → JetStream KV → console). |
| **ESO** | External Secrets Operator. |
| **SPIFFE / SPIRE** | Workload identity standards. SVIDs are short-lived mTLS certs bound to K8s ServiceAccounts. |
| **GSLB** | Global Server Load Balancing — handled by k8gb across regions. |
| **PromotionPolicy** | Removed concept. Replaced by **EnvironmentPolicy** attached to a destination Environment, enforcing PR/approval/soak/change-window rules. |

---

## See also

- [`IMPLEMENTATION-STATUS.md`](IMPLEMENTATION-STATUS.md) — what's built today vs what's design-only.
- [`NAMING-CONVENTION.md`](NAMING-CONVENTION.md) — concrete naming patterns for every object type.
- [`ARCHITECTURE.md`](ARCHITECTURE.md) — how the components fit together.
- [`PERSONAS-AND-JOURNEYS.md`](PERSONAS-AND-JOURNEYS.md) — who uses each surface and what for.
- [`SECURITY.md`](SECURITY.md) — identity, secrets, rotation.
- [`SOVEREIGN-PROVISIONING.md`](SOVEREIGN-PROVISIONING.md) — how a Sovereign is brought online.
- [`BLUEPRINT-AUTHORING.md`](BLUEPRINT-AUTHORING.md) — how to write a Blueprint.
