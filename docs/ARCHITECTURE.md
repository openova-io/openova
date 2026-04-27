# Catalyst Architecture

**Status:** Authoritative target architecture. **Updated:** 2026-04-27.
**Implementation:** Most of what this document describes is **design-stage** — see [`IMPLEMENTATION-STATUS.md`](IMPLEMENTATION-STATUS.md) for what exists in code today vs what is design.

This document describes the architecture of **Catalyst** — the OpenOva platform. For terminology, defer to [`GLOSSARY.md`](GLOSSARY.md). For naming, defer to [`NAMING-CONVENTION.md`](NAMING-CONVENTION.md). For current code state, defer to [`IMPLEMENTATION-STATUS.md`](IMPLEMENTATION-STATUS.md).

---

## 1. The platform in one paragraph

Catalyst is a self-sufficient Kubernetes-native control plane published as signed OCI Blueprints. A single deployed Catalyst is called a **Sovereign**. Inside a Sovereign, **Organizations** are the multi-tenancy unit. An Organization has **Environments** (`{org}-prod`, `{org}-dev`, etc.) where users install **Applications** from **Blueprints**. Each Environment is backed by one Gitea repo and one or more vclusters running lightweight Flux. Every state change flows through NATS JetStream, projects into per-Environment KV via the **projector** service, and reaches the console via SSE — so every UI surface sees the same picture, derived from Git (write side) and Kubernetes (runtime side) without fragmenting. Crossplane handles all non-Kubernetes resources. OpenBao + ESO + SPIRE handles secrets and workload identity. Keycloak handles user identity. **Same code runs in every Sovereign — whether it's run by us, by Omantel, or by Bank Dhofar.**

---

## 2. Two scales, one architecture

The model serves two distinct customer shapes through the **same code**:

```
        ┌──────────────────────────────────────────────────────────────┐
        │ SME-style Sovereign (e.g. omantel)                           │
        │                                                               │
        │ Many small Organizations, mostly single-Environment           │
        │ Each Org gets its own minimal Keycloak (no HA)                │
        │ Self-service marketplace, next-next-next install              │
        │ Sovereign-admins are the SaaS provider's cloud team           │
        └──────────────────────────────────────────────────────────────┘

        ┌──────────────────────────────────────────────────────────────┐
        │ Corporate-style Sovereign (e.g. bankdhofar)                  │
        │                                                               │
        │ Few internal Organizations (core-banking, digital-channels…)  │
        │ One Sovereign-wide Keycloak (federates to corporate Azure AD) │
        │ Rich governance: EnvironmentPolicy, soak gates, approvers     │
        │ Sovereign-admins are the bank's platform team                 │
        │ Multi-region default; multi-Environment per Org default       │
        └──────────────────────────────────────────────────────────────┘
```

The **only** runtime configuration difference is set at provisioning time:

```yaml
keycloakTopology: per-organization      # SME default
# or
keycloakTopology: shared-sovereign      # Corporate default
```

Everything else is identical in code.

---

## 3. Topology

```
┌─────────────────────────────────────────────────────────────────────────┐
│ Sovereign: omantel                                                       │
│                                                                           │
│  Management host cluster: hz-nbg-mgt-prod                                 │
│  ┌────────────────────────────────────────────────────────────────────┐ │
│  │ Catalyst control plane (in catalyst-* namespaces)                   │ │
│  │   console   marketplace   admin   catalog-svc   projector           │ │
│  │   provisioning   environment-controller   blueprint-controller      │ │
│  │   billing                                                            │ │
│  │   gitea   nats-jetstream   openbao   keycloak   spire-server        │ │
│  │   observability (Grafana stack)                                      │ │
│  └────────────────────────────────────────────────────────────────────┘ │
│  Plus per-host-cluster infrastructure (Cilium, Flux, Crossplane,         │
│  cert-manager, External-Secrets, Kyverno, Harbor, Reloader, Trivy,       │
│  Falco, Sigstore, Syft+Grype, VPA, KEDA, External-DNS, k8gb, Coraza,     │
│  MinIO, Velero, failover-controller) — see PLATFORM-TECH-STACK §3.       │
│                                                                           │
│  Workload host clusters: hz-fsn-rtz-prod, hz-hel-rtz-prod                 │
│  ┌──────────────────────────────────────────────────────────────────┐   │
│  │ Per-Org vcluster (named {org}):                                  │   │
│  │   muscatpharmacy   acme-shop   blue-pharmacy   …                 │   │
│  │   each runs its own lightweight Flux pointed at the Environment  │   │
│  │   Gitea repo                                                     │   │
│  └──────────────────────────────────────────────────────────────────┘   │
│                                                                           │
│  DMZ host clusters: hz-fsn-dmz-prod, hz-hel-dmz-prod                      │
│   Cilium Gateway, WAF (Coraza), k8gb DNS, WireGuard endpoints             │
└─────────────────────────────────────────────────────────────────────────┘
                              ↕
                  Gitea (in management cluster)
                  ─────────────────────────────
                  catalog/                    ← public Blueprint mirror
                  organizations/muscatpharmacy/
                    ├── shared-blueprints/     ← Org-private Blueprints
                    └── muscatpharmacy-prod    ← Environment repo
                  organizations/acme-shop/
                    ├── shared-blueprints/
                    └── acme-shop-prod
                  ...
```

**Sovereign self-sufficiency**: once a Sovereign is provisioned, it has its own Gitea, its own JetStream, its own OpenBao, its own Keycloak, its own Crossplane. It does not depend on any other Sovereign at runtime. OpenOva's `openova` Sovereign is in the picture only as the publisher of public Blueprints — and even those are mirrored locally, so the Sovereign keeps working if openova.io disappears.

---

## 4. Write side: Git → Flux → Kubernetes (+ Crossplane)

```
                       Console UI                       REST/GraphQL API
                            │                                    │
                            │  (Git push from any of these       │
                            │   bypasses provisioning and goes   │
                            │   straight to the Gitea repo;      │
                            │   webhook + projector still fire)  │
                            ▼                                    ▼
              ┌──────────────────────────────────────────────────────────┐
              │  provisioning service                                    │
              │   - validates configSchema against Blueprint              │
              │   - resolves dependency graph                             │
              │   - composes manifests                                    │
              │   - commits to the Environment Gitea repo                 │
              └──────────────────────────────────────────────────────────┘
                                     │
                                     ▼
              ┌──────────────────────────────────────────────────────────┐
              │  Gitea: gitea.<sovereign-domain>/{org}/{org}-{env_type}    │
              │  ────────────────────────────────────────────────────────  │
              │  flux-system/        gotk-components.yaml + gotk-sync     │
              │  applications/                                             │
              │    marketing-site/    kustomization.yaml + values.yaml    │
              │    blog/              kustomization.yaml + values.yaml    │
              │    shared-postgres/   kustomization.yaml + values.yaml    │
              │  policies/                                                 │
              │    environment-policy.yaml   ← approvals, soak, windows   │
              └──────────────────────────────────────────────────────────┘
                                     │
                                     ▼ (Gitea webhook → projector → annotate)
              ┌──────────────────────────────────────────────────────────┐
              │  Flux in vcluster {org}                                  │
              │   - source-controller pulls commit                       │
              │   - kustomize-controller applies to per-App namespaces   │
              │   - helm-controller renders Helm-based Blueprints        │
              └──────────────────────────────────────────────────────────┘
                                     │
                  ┌──────────────────┴────────────────────┐
                  ▼                                        ▼
        K8s Application workloads                 Crossplane Claims
        (Deployments, Services,                  (Hetzner servers, DNS records,
         Pods, Secrets via ESO)                   S3 buckets, Cloudflare Workers)
                                                          │
                                                          ▼
                                              Crossplane Compositions
                                              fan out to provider APIs
```

**Crossplane is the only IaC.** Users never write Crossplane Compositions in their Application configs. Blueprint authors do — when a Blueprint declares "needs an external Postgres," that becomes a Crossplane Claim under the hood. Advanced users (corporate sovereign-admins, OpenOva engineers) can author and contribute Crossplane Compositions as Blueprints. End users see "needs a database, pick existing or new" in the UI.

---

## 5. Read side: CQRS via JetStream → projector → console

```
┌────────────────────┐     ┌────────────────────┐     ┌──────────────────┐
│ k8s informers      │     │ Flux events        │     │ Gitea webhooks   │
│ (one per vcluster) │     │ (per vcluster)     │     │ (per Sovereign)  │
└─────────┬──────────┘     └─────────┬──────────┘     └─────────┬────────┘
          │                          │                          │
          ▼                          ▼                          ▼
   ┌────────────────────────────────────────────────────────────────────┐
   │  NATS JetStream                                                    │
   │  Account isolation: one NATS Account per Organization.             │
   │  Subject prefix scoped per Environment (where <env> = {org}-{env_type}): │
   │     ws.<env>.k8s.<obj-kind>.<ns>.<name>                            │
   │     ws.<env>.flux.<kustomization>                                  │
   │     ws.<env>.git.<commit-hash>                                     │
   │     ws.<env>.crossplane.<resource>                                 │
   └────────────────────────────────────────────────────────────────────┘
                                  │
                                  ▼ durable consumer per env partition
   ┌────────────────────────────────────────────────────────────────────┐
   │  projector                                                         │
   │   - consumes events                                                │
   │   - rebuilds per-object state                                      │
   │   - writes to JetStream KV: ws-<env>-state/<kind>/<name>           │
   │   - fans out SSE to subscribed console clients                     │
   │   - authorizes by JWT claim {environment, org, role}               │
   │   - serves REST/GraphQL snapshot read API                          │
   └────────────────────────────────────────────────────────────────────┘
                                  │
                                  ▼
                       ┌────────────────────┐
                       │  Catalyst console  │
                       └────────────────────┘
```

**One spine (JetStream), one read model (JetStream KV), one consumer (projector), one stream (SSE).**

The console **never talks to k8s API or Git directly.** This is the architectural lock that prevents the "App says installed in one tab, failed in another tab" class of bug. Both tabs read the same JetStream KV snapshot served by the same projector replica.

JetStream replaces the older Redpanda + Valkey pairing in the control plane: NATS is Apache 2.0 (no BSL risk), has native KV (fewer moving parts), and native multi-tenant Accounts (cleaner per-Org isolation). Application-layer event needs (e.g. TalentMesh's voice pipeline) remain free to choose Redpanda, Kafka, NATS, or anything else — that's an Application-level decision, not a control-plane one.

---

## 6. Identity and secrets

Two separate identity systems for two separate purposes:

| Subject | System | Lifetime | Purpose |
|---|---|---|---|
| **Workloads** (every Pod) | SPIFFE/SPIRE → SVID (mTLS cert) | 5 min, auto-rotated | Pod-to-Pod auth, Pod-to-OpenBao auth, Pod-to-NATS auth |
| **Users** (every human) | Keycloak → JWT | 15 min access / 30 day refresh | UI auth, API auth |

**Secrets** flow:

```
            OpenBao (per-region, independent Raft cluster)
                  │
                  │ (workload requests via SPIFFE SVID)
                  ▼
            ESO ExternalSecret CR (in Git, references OpenBao path)
                  │
                  ▼
            K8s Secret (versioned, reloader watches for hash change)
                  │
                  ▼
            Pod (env var or mounted file)
```

**Multi-region**: each region runs its **own** 3-node Raft OpenBao cluster. **No stretched cluster.** Cross-region async perf replication for read availability and DR. A region failure does not require any other region to do anything.

**Keycloak** topology depends on Sovereign type:
- **SME-style** (`per-organization`): minimal single-replica Keycloak per Org, sized for hundreds of users. Embedded H2 or sqlite. Each Org's Keycloak is independent; failure does not affect other Orgs.
- **Corporate-style** (`shared-sovereign`): one HA Keycloak for the entire Sovereign, federating to the parent corporation's identity provider (Azure AD, Okta).

See [`SECURITY.md`](SECURITY.md) for full credential rotation and identity flow.

---

## 7. The user-facing surfaces

Three first-class surfaces. **No fourth.**

### 7.1 UI (the Catalyst console)

Default. Most users never leave it. Three depths the user can switch between:

- **Form view** — one Application page, fields driven by `configSchema`. Default for SME.
- **Advanced view** — same page with topology, secrets, observability, history, manifest tabs. Default for corporate.
- **IaC editor view** — in-browser Monaco editing the Environment Gitea repo with Blueprint-schema validation, live diff, commit-on-save. Toggle, not modal.

All three commit to the same Environment Gitea repo. The **Application card** is the user's primary handle — see [`PERSONAS-AND-JOURNEYS.md`](PERSONAS-AND-JOURNEYS.md).

### 7.2 Git

Direct push or pull-request to the Environment's Gitea repo (or to `shared-blueprints` for Org-private Blueprints, or to private Crossplane Composition repos for advanced users).

Identical write semantics as the UI. Both end up as a commit on the same repo. EnvironmentPolicy (PR approvals, soak, change windows) applies regardless of the surface.

### 7.3 API (REST + GraphQL)

For **integrations**, not for primary IaC authoring.

Use cases:
- A bank's existing Backstage portal queries Catalyst to show Environments and Applications.
- A change-management tool (ServiceNow, JIRA) triggers Application installs based on a ticket.
- A monitoring/auditing tool exports state for compliance reports.

The API exposes the same operations the console performs. It is **not** an IaC authoring layer in the Terraform-cloud sense. We do not ship a Terraform provider, a Pulumi SDK, or any other "declare desired state through us" surface — the Environment Gitea repo is that surface.

### 7.4 What's deliberately NOT a surface

- `kubectl` — useful for debugging inside one's own vcluster; never a configuration mechanism.
- A standalone CLI for production changes — Catalyst may expose a small read-only debug CLI in the future; not authoritative for installs/promotions.
- Terraform / Pulumi — Crossplane covers non-K8s; it is platform plumbing, not user-facing.

---

## 8. Promotion across Environments

Promotion is **not** a separate engine or a chain object. It is the simple act of copying an Application's manifest from one Environment's Gitea repo to another's, plus a policy on the destination.

```
Blueprint detail page in console:

  bp-wordpress @ available 1.4.0
  ─────────────────────────────────────────────────
  Applications using this Blueprint in your Org (4)

  Application       Environment        Version    Status
  ──────────────────────────────────────────────────────
  marketing-site    acme-dev           1.4.0      ● Running   [Open]
  marketing-site    acme-staging       1.3.0      ● Running   [Open]
  marketing-site    acme-prod          1.2.0      ● Running   [Open]
  blog              acme-prod          1.2.0      ● Running   [Open]

  [ + Install in another Environment ]
  [ Compare versions ]
```

From `marketing-site` in `acme-staging`, the user clicks "Copy to another Environment" → picks `acme-prod`. Catalyst opens a Gitea PR on `acme-prod`'s repo. The destination Environment's `EnvironmentPolicy` (approvers, soak, change window) applies to the PR. On merge, Flux reconciles. Done.

```yaml
apiVersion: catalyst.openova.io/v1alpha1
kind: EnvironmentPolicy
metadata:
  name: prod-default
  namespace: acme           # Org namespace on management cluster
spec:
  appliesTo:
    environments: [acme-prod]
  rules:
    - kind: pr-required
      approvers: [team-platform, team-security]
    - kind: soak
      sourceEnvironment: acme-staging
      duration: 72h
    - kind: change-window
      cron: "0 14 * * 2,4"  # Tue/Thu 14:00
      duration: 2h
```

The policy lives on the **destination** Environment, not on the App or on a chain. It applies uniformly to every Application installed there, regardless of who initiated the change (UI, Git, API).

---

## 9. Multi-Application linkage (the dependency tree)

A Blueprint can declare dependencies on other Blueprints:

```yaml
apiVersion: catalyst.openova.io/v1alpha1
kind: Blueprint
metadata:
  name: bp-wordpress
  version: 1.3.0
spec:
  configSchema: …
  depends:
    - blueprint: bp-postgres
      version: ^1.4
      alias: db
      when: "{{ .config.postgres.mode == 'embedded' }}"
      values:
        databases: ["{{ .application.name }}"]
```

When a User installs `marketing-site` from `bp-wordpress`:

1. **Catalog-svc** flattens the dependency tree.
2. **Console** asks: "WordPress requires Postgres. Use an existing Postgres Application or create a new dedicated one?" — querying projector for existing `bp-postgres` Applications in this Environment.
3. **Provisioning service** composes an InstallPlan: either one Application (`marketing-site`) referencing an existing postgres Application, or two Applications (`marketing-site` + `marketing-site-postgres`) with a Flux `dependsOn` edge.
4. **Gitea commit** writes one or two `applications/<name>/` directories.
5. **Flux** reconciles in dependency order.

Every Application is its own Flux Kustomization. The graph is materialized as `dependsOn` edges between Kustomizations, computed at install time from the Blueprint's `depends` declaration.

---

## 10. Provisioning a Sovereign

```
Phase 0  Bootstrap (one-shot, runs from catalyst-provisioner.openova.io)
─────────────────────────────────────────────────────────────────────
1. OpenTofu provisions: VPC, host nodes, load balancers, DNS records,
   object storage on the target cloud provider (Hetzner / AWS / etc.)
2. Bootstrap kit installs in order:
   a. Cilium (CNI + Gateway API)              ← network must come first
   b. cert-manager                            ← TLS for everything below
   c. Flux (host-level)                       ← GitOps engine
   d. Crossplane + provider config            ← cloud resource control plane
   e. Sealed Secrets (transient, only for bootstrap secrets)
   f. SPIRE server + agent                    ← workload identity
   g. NATS JetStream cluster (3 nodes)
   h. OpenBao cluster (3 nodes, region-local Raft)
   i. Keycloak (per `keycloakTopology` choice)
   j. Gitea (with public Blueprint mirror seeded)
   k. Catalyst control plane (umbrella Blueprint: bp-catalyst-platform)

Phase 1  Hand-off (~5 minutes after Phase 0 starts)
─────────────────────────────────────────────────────────────────────
Crossplane in the new Sovereign adopts management of further
infrastructure. OpenTofu state is archived. Bootstrap kit is no
longer in the runtime path.

Phase 2  Day-1 setup
─────────────────────────────────────────────────────────────────────
First sovereign-admin logs into the console; configures cert-manager
issuers, backup destinations, optional federation; onboards the first
Organization and creates its first Environment.

Phase 3  Steady-state operation
─────────────────────────────────────────────────────────────────────
Catalyst is fully autonomous. catalyst-provisioner.openova.io remains
online indefinitely as the entry point for future Sovereign
provisioning runs — but the existing Sovereign no longer depends on
it at runtime.
```

See [`SOVEREIGN-PROVISIONING.md`](SOVEREIGN-PROVISIONING.md) for the full procedure (this is the canonical reference for phase semantics).

---

## 11. Catalyst-on-Catalyst (dogfooding)

Every component in the Catalyst control plane is itself published as a Blueprint:

```
bp-catalyst-platform                 ← umbrella
├── depends: bp-catalyst-console
├── depends: bp-catalyst-marketplace
├── depends: bp-catalyst-admin
├── depends: bp-catalyst-catalog-svc
├── depends: bp-catalyst-projector
├── depends: bp-catalyst-provisioning
├── depends: bp-catalyst-environment-controller
├── depends: bp-catalyst-blueprint-controller
├── depends: bp-catalyst-billing
├── depends: bp-catalyst-gitea            ← per-Sovereign Git server
├── depends: bp-catalyst-nats-jetstream   ← event spine + KV
├── depends: bp-catalyst-openbao          ← secret backend
├── depends: bp-catalyst-keycloak         ← user identity
├── depends: bp-catalyst-spire            ← workload identity
└── depends: bp-catalyst-observability    ← OTel + Grafana stack
```

(Cilium, Flux, Crossplane, Cert-manager, Kyverno, Harbor, External-Secrets, Reloader, Falco, Sigstore, Syft+Grype are **per-host-cluster infrastructure**, not Catalyst control-plane components — see [`PLATFORM-TECH-STACK.md`](PLATFORM-TECH-STACK.md) §1. They get installed once per host cluster, before Catalyst itself.)

Installing `bp-catalyst-platform` once gives you a working Sovereign. Same Blueprint installed on Hetzner = the openova Sovereign. Same Blueprint installed on AWS for a bank = that bank's Sovereign. Same Blueprint installed on Hetzner for a telco = the omantel Sovereign. **One artifact. Zero divergence.**

OpenOva's own customer Applications (Cortex, Fingate, Fabric, Relay, Specter, Axon) are similarly composite Blueprints that run **on top of** Catalyst — they are Applications inside the `openova-public` Environment of the openova Sovereign.

---

## 12. State-of-the-art principles applied

| Pattern | Where it lives in this design |
|---|---|
| **CQRS** | Write side: Git → Flux → K8s. Read side: catalog-svc + projector. |
| **GitOps as truth** | Every state change is a commit. Rollback = `git revert`. Audit = `git log`. |
| **Event sourcing** | NATS JetStream is the durable event log. Projector replays for recovery. |
| **CRD-driven control plane** | Sovereign, Organization, Environment, Application, Blueprint, EnvironmentPolicy, SecretPolicy, Runbook — all CRDs. Controllers reconcile. |
| **Multi-tenancy at OS layer** | vcluster per Organization per host cluster — isolated K8s API + control plane per Org. |
| **Crossplane for non-K8s** | All cloud-side resources via Compositions. Users never see Crossplane. |
| **OCI artifacts for software** | Blueprints are signed OCI manifests, cosigned, SBOMed. |
| **CloudEvents-shaped envelopes** | Standard event format on JetStream subjects. |
| **OpenTelemetry first-class** | All Catalyst services emit traces; every Blueprint inherits OTel by default. |
| **Policy as code** | Kyverno policies in Catalyst block out-of-policy commits and out-of-policy K8s resources. |
| **Supply chain security** | cosign signing, SLSA-3 build provenance, Syft+Grype SBOM, Trivy scans, Falco runtime. |
| **JSON Schema for config** | Console form is generated from Blueprint configSchema. No hand-written forms. |
| **Pull-based updates** | Each Sovereign mirrors the public Blueprint catalog on its own schedule. Air-gap-ready by construction. |
| **Workload identity** | SPIFFE/SPIRE SVIDs replace static service-account credentials end-to-end. |
| **Independent failure domains** | OpenBao Raft per region. vcluster per Org. Keycloak per Org (SME) or per Sovereign (corporate). |

---

## 13. Open Application Model influence

The Blueprint shape is influenced by — but not identical to — OAM:

| OAM term | Catalyst equivalent |
|---|---|
| Application | Blueprint with `card.category=composite` |
| Component | Blueprint (single-purpose) |
| Trait | Blueprint overlay (e.g. `overlays/small`, `overlays/medium`, `overlays/large`) |
| Scope | Environment + Placement |

We are not a strict OAM implementation. We borrow the layered composition idea but use Kubernetes-native primitives (Kustomize, Helm) rather than OAM-specific machinery — because Flux, Crossplane, and the K8s ecosystem are the runtime, and inventing a new layer adds no value.

---

## 14. Read further

- [`GLOSSARY.md`](GLOSSARY.md) — every term defined.
- [`NAMING-CONVENTION.md`](NAMING-CONVENTION.md) — every name's pattern.
- [`PERSONAS-AND-JOURNEYS.md`](PERSONAS-AND-JOURNEYS.md) — who uses each surface and how.
- [`SECURITY.md`](SECURITY.md) — identity, secrets, rotation in detail.
- [`SOVEREIGN-PROVISIONING.md`](SOVEREIGN-PROVISIONING.md) — bringing a Sovereign online.
- [`BLUEPRINT-AUTHORING.md`](BLUEPRINT-AUTHORING.md) — writing Blueprints (including Crossplane Compositions for advanced users).
- [`PLATFORM-TECH-STACK.md`](PLATFORM-TECH-STACK.md) — every component's role in Catalyst.
- [`SRE.md`](SRE.md) — operating a Sovereign.
