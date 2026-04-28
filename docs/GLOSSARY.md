# Glossary

> **Status:** Canonical. Single source of truth for OpenOva terminology.
> **Updated:** 2026-04-27.
> **Note:** Terms here describe the agreed model. For which terms map to currently-implemented code vs design-stage, see [`IMPLEMENTATION-STATUS.md`](IMPLEMENTATION-STATUS.md).

Every other document defers to this file. When a term in another doc looks contested, this file wins. New terminology is proposed here first, then propagated.

---

## Core nouns

| Term | Definition |
|---|---|
| **OpenOva** | The company. Authors and maintains Catalyst, Blueprints, and the openova.io services. Used as a brand prefix in product names (e.g. "OpenOva Catalyst", "OpenOva Cortex"). When unqualified, "OpenOva" refers to the company; when referring to the platform itself, prefer **Catalyst**. |
| **Catalyst** | The OpenOva platform itself. A self-sufficient Kubernetes-native control plane. Composed of: console, marketplace, admin, catalog-svc, projector, provisioning, environment-controller, blueprint-controller, billing, identity, secret, event-spine, gitea, observability. See "Catalyst components" below. Published from this repository as signed OCI Blueprints. |
| **Sovereign** | One **deployed** instance of Catalyst on Kubernetes infrastructure chosen by its owner. Self-contained; never depends at runtime on any other Sovereign. Examples: `openova` (run by us, hosts our SaaS Organizations — formerly "Nova"), `omantel` (run by Omantel, hosts SME Organizations across Oman), `bankdhofar` (run by the bank, hosts internal Organizations). |
| **Organization** | The multi-tenancy unit inside a Sovereign. Has billing, Users, Environments, private Blueprints. Ahmed's pharmacy is one Organization on the `omantel` Sovereign; `digital-channels` is one Organization on the `bankdhofar` Sovereign. |
| **Environment** | An env-typed scope where Applications run. Named `{org}-{env_type}` where `env_type` is one of `prod | stg | uat | dev | poc` (per [`NAMING-CONVENTION.md`](NAMING-CONVENTION.md) §2.4). **Logical** concept; can span multiple regions and building blocks via Placement. Realized as a **branch** (`develop`/`staging`/`main`) inside each Application's Gitea repo, plus one or more vclusters per Placement spec. Examples: `acme-prod`, `acme-dev`, `bankdhofar-uat`. |
| **Application** | What a User installs into an Environment from a Blueprint. The user-facing object: an App Store-style card representing a running deployment (e.g. WordPress, Postgres, an internal microservice). **Each Application is realized as one Gitea repo** at `gitea.<location-code>.<sovereign-domain>/<org>/<app>` under its owning Organization. Branches `develop`/`staging`/`main` map to the `dev`/`stg`/`prod` Environments. The repo is the unit of CODEOWNERS, branch protection, webhook, and CI — giving every team self-sufficient ownership of their Apps. |
| **Blueprint** | The reusable, OCI-published, signed unit of installable software. Unifies what previously was split between "module" (primitive) and "template" (composition). A Blueprint can declare dependencies on other Blueprints, with arbitrary depth. Visibility: `listed` (catalog card) / `unlisted` / `private` (Org-scoped). Source layout: see [`BLUEPRINT-AUTHORING.md`](BLUEPRINT-AUTHORING.md) §2. |
| **User** | A person. Authenticates via Keycloak. Belongs to one Organization (or has cross-Org admin scope as `sovereign-admin`). |
| **Voucher** | A redeemable code that grants billing credit when applied at checkout. Issued by `sovereign-admin` (per-Sovereign campaigns) or `org-admin` (rare; intra-Org credit grants). The user-facing label for what the code calls `PromoCode` (see `core/services/billing/store/store.go`). Vouchers are the user-acquisition surface for franchised Sovereigns: a Franchisee mints codes, distributes them through their marketing channels, and a redeemer's first checkout converts the code into Organization credit. Lives as a row in the per-Sovereign billing Postgres database; soft-delete (`deleted_at`) preserves the audit trail of past redemptions. See [`FRANCHISE-MODEL.md`](FRANCHISE-MODEL.md). |

---

## Roles

| Term | Definition |
|---|---|
| **`sovereign-admin`** | The role for Users who operate a Sovereign — configures the Catalyst control plane, manages the underlying clusters via Crossplane (which is platform plumbing, not a user-facing surface), onboards Organizations, sets Sovereign-wide policies. Omantel's cloud team and Bank Dhofar's platform team are both `sovereign-admin` on their respective Sovereigns. There is no separate entity-noun (rejected: "operator", "tenant", "client"). |
| **`org-admin`** | Role within one Organization — creates Environments, manages Users, sets Org-level policies. |
| **`org-developer`** | Role with Application install/configure rights inside specific Environments. |
| **`org-viewer`** | Read-only role within an Organization. |
| **`security-officer`** | Role with audit/policy/secret-rotation gating rights. Optional Org-level role. |
| **`billing-admin`** | Role with billing/invoice/quota rights. Optional Org-level role. |
| **`sme-end-user`** | Persona, not a role: an SME owner (Ahmed) for whom Organization onboarding is automatic on first signup. |
| **`Franchisee`** | Persona, not a role: the legal entity (telco, ISP, hyperscaler reseller, regional cloud operator) that owns and operates a franchised Sovereign under license from OpenOva. Examples: Omantel running `omantel.omani.works`, a regional reseller running `cloud.acme.example`. The Franchisee's staff hold the `sovereign-admin` role on their Sovereign and use the existing admin app (per `core/admin/`) to issue Vouchers, curate the `catalog-sovereign` Gitea Org, set marketplace branding, and pick the per-tier pricing they pass through to their tenant Organizations. Revenue split with OpenOva is governed bilaterally by the franchise contract — not a per-Sovereign config field. See [`FRANCHISE-MODEL.md`](FRANCHISE-MODEL.md). |

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
| **console** | Primary user-facing UI. Three depths: form view (default for SME), advanced view (default for corporate), in-browser Monaco IaC editor (toggle). All commits go to the relevant Application's Gitea repo (one repo per App). |
| **marketplace** | Public-facing Blueprint card grid (the "App Store"). |
| **admin** | Sovereign-level admin UI. Where `sovereign-admin` configures the deployment. |
| **catalog-svc** | Reads Blueprint CRDs (sourced from the public catalog mirror + the Sovereign-curated `catalog-sovereign` Gitea Org + Org-private `shared-blueprints` repos), serves catalog API to console + marketplace. |
| **projector** | CQRS read-side service. Subscribes to NATS JetStream, materializes per-Environment KV, serves SSE to console, handles Gitea webhooks (forces Flux reconcile on commit). |
| **provisioning** | Validates Application install requests against Blueprint configSchema, composes manifests, **creates one Gitea repo per Application** under the Org's Gitea Org, commits initial branches (`develop`/`staging`/`main`). |
| **environment-controller** | Reconciles the **Environment CRD**: creates the vcluster(s), bootstraps Flux inside (watching the appropriate branch across the Org's Application repos), wires the webhook, generates pull credentials. |
| **blueprint-controller** | Watches Blueprint sources (this monorepo + per-Sovereign Gitea Org-private repos), validates and registers Blueprint CRDs. |
| **billing** | Per-Org metering, invoicing. |
| **identity** | Keycloak (per-Organization realm in SME-style Sovereigns; per-Sovereign realm in corporate-style) + SPIFFE/SPIRE for workload identity. |
| **secret** | OpenBao + External Secrets Operator (ESO). Independent Raft cluster per region (no stretched cluster); cross-region perf replication is async. |
| **event-spine** | NATS JetStream — pub/sub + Streams + KV bucket. Workload-identity-scoped Accounts per Organization. Replaces what was previously specified as "Redpanda + Valkey" for the control plane. |
| **gitea** | Per-Sovereign Git server. Hosts five top-level Gitea Orgs by convention: `catalog` (read-only mirror of the public Blueprint catalog), `catalog-sovereign` (optional — Sovereign-owner-curated private Blueprints visible to every Org on this Sovereign), one Gitea Org per Catalyst **Organization** (each holding the Org's `shared-blueprints` repo + one repo per **Application**), and `system` (sovereign-admin scope: CRs, policy bundles, runbooks). |
| **observability** | Per-Sovereign OpenTelemetry + Grafana stack (Alloy + Loki + Mimir + Tempo + Grafana) for Catalyst's own telemetry. |

---

## Gitea Orgs (top-level Gitea namespaces)

A Sovereign's Gitea instance hosts five conventional Gitea Orgs. The unified rule: **one Catalyst Organization = one Gitea Org**, **one Application = one Gitea Repo**, regardless of SME or corporate scale.

| Term | Definition |
|---|---|
| **`catalog` Gitea Org** | Read-only nightly mirror of `github.com/openova-io/openova` synced by `bp-catalyst-gitea`. Public Blueprints visible to every Org on the Sovereign. |
| **`catalog-sovereign` Gitea Org** | Optional. Sovereign-owner-curated private Blueprints (e.g. an SME marketplace operator authoring `bp-wordpress`, `bp-jitsi`, `bp-cal-com` for their tenants). Visible to every Org on this Sovereign without being public upstream. Distinct from per-Org `shared-blueprints` (Org-scoped only). |
| **`<org>` Gitea Org** | One per Catalyst **Organization**. Holds: `shared-blueprints` (Org-private Blueprint authoring) plus **one Gitea Repo per Application** owned by that Org (e.g. `acme-pharmacy/store-frontend`, `core-banking/billing-rail`). |
| **`system` Gitea Org** | Sovereign-admin scope. Holds: `catalyst-config` (Sovereign / Organization / Environment / EnvironmentPolicy CRs), `policy-bundle` (Kyverno ClusterPolicies, Falco rules, RE Scorecard CRDs), `runbooks` (auto-remediation Runbooks). Edit access restricted to `sovereign-admin`; per-Org delegation possible via Catalyst RBAC. |

---

## Persona-facing surfaces

| Term | Definition |
|---|---|
| **UI** | The Catalyst console — full feature, three view depths. |
| **Git** | Direct push or pull-request to an Application's Gitea repo (branches `develop`/`staging`/`main` for dev/stg/prod), or to a Blueprint repo (`shared-blueprints` per-Org or `catalog-sovereign` Sovereign-wide). |
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
| "Workspace" (as Catalyst scope or component name) | Environment / environment-controller | Renamed for industry alignment and to escape collision with VS Code / Slack / Backstage / Terraform workspaces. The controller previously named `workspace-controller` is now `environment-controller`. |
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
