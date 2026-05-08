# EPICs 1–6: Unified Design

**Status**: Authoritative target spec for the Phase 0/1 roll-out tracked under #1094.
**Updated**: 2026-05-08
**Audience**: Every Architect, Implementer, Reviewer, Test-Plan Author, Test Executor, Fix Author working on any of #1094–#1101.

> **Read this before writing any code.** This document is the contract. It does not invent decisions — it stitches together what is already locked in `INVIOLABLE-PRINCIPLES.md`, `NAMING-CONVENTION.md`, `BLUEPRINT-AUTHORING.md`, `adr/0001-...`, `SRE.md`, and `MULTI-REGION-DNS.md` into a single low-level reference for the team.

---

## 0. Scope and reading order

Six EPICs are tracked from the umbrella #1094:

| # | EPIC | Issue |
|---|---|---|
| 0 | Foundation contracts (CRDs, controllers, label vocab, vCluster scaffold, MC substrate, Cilium hardening) | #1095 |
| 1 | Compliance (Kyverno + watcher extension + score aggregator + UI) | #1096 |
| 2 | Applications (Application/Blueprint CRDs, controllers, catalog-svc, install + topology editor) | #1097 |
| 3 | RBAC (useraccess-controller, Keycloak full-CRUD, claims, catalog tiers, multi-grant UI) | #1098 |
| 4 | Cloud Resources (k9s-on-web + Guacamole + projector) | #1099 |
| 5 | Networking (default-deny, Hubble, OTel Operator, ClusterMesh, DMZ vcluster, NetBird mesh) | #1100 |
| 6 | Multi-cluster + Continuum DR (3 regions, CNPG cluster-pair, Continuum CRD/controller, switchover UI) | #1101 |

**Reading order**: §1 (vocabulary) → §2 (architectural rules) → §3 (Phase 0 contracts) → §4..§9 (one §per EPIC) → §10 (dev-loop team shape) → §11 (resolved tensions / decisions log).

---

## 1. Vocabulary (the join key everywhere)

The label set in `NAMING-CONVENTION.md` §6 is the single join key across compliance, RBAC, billing, networking, and resource-browser scoping. Phase 0 makes it enforceable at admission via Kyverno.

### 1.1 Required labels on every Catalyst-managed resource

```yaml
metadata:
  labels:
    # Cluster scope (set by infrastructure)
    openova.io/provider:        hetzner|huawei|oci|aws|gcp|azure|contabo
    openova.io/region:          fsn|nbg|hel|...        # 3-char per §2.2
    openova.io/building-block:  rtz|dmz|mgt
    openova.io/env-type:        prod|stg|uat|dev|poc
    openova.io/sovereign:       <sovereign-fqdn>       # e.g. omantel.omani.works
    openova.io/host-cluster:    <prov>-<reg>-<bb>-<env_type>

    # Tenant scope (set by organization-controller / application-controller)
    openova.io/organization:    <org-slug>             # e.g. acme
    openova.io/environment:     <org>-<env_type>       # e.g. acme-prod
    openova.io/vcluster:        <org>                  # within host cluster (§4.7)
    openova.io/application:     <app-name>             # within Environment
    openova.io/blueprint:       <bp-name>              # e.g. bp-wordpress
    openova.io/blueprint-version: <semver>             # e.g. 1.3.0

    # Lifecycle
    openova.io/managed-by:      flux|crossplane|opentofu|manual
    app.kubernetes.io/managed-by: flux                 # mirrors when managed-by=flux
```

### 1.2 Reserved Organization slugs

Per `NAMING-CONVENTION.md` §2.5, refused at admission: `system`, `flux`, `crossplane`, `catalyst`, `gitea`, `kube-*`, anything matching a provider/region/bb/env_type code.

### 1.3 vCluster naming

- Within host cluster: `{org}` (no provider/region encoded — host cluster is the parent)
- Cross-cluster reference: `{prov}-{reg}-{bb}-{env_type}-{org}` (e.g. `hz-fsn-rtz-prod-acme`)
- Sibling vClusters named `acme` on `hz-fsn-rtz-prod` and `hz-hel-rtz-prod` are two physical realizations of the **same logical Catalyst Environment** `acme-prod`.

### 1.4 DNS

- Catalyst control-plane: `{component}.{location-code}.{sovereign-domain}` (e.g. `console.hfmp.openova.io`)
- Application: `{app}.{environment}.{sovereign-or-org-domain}` (e.g. `wordpress.acme-prod.omantel.openova.io`)

---

## 2. Architectural rules (non-negotiable)

These extend `INVIOLABLE-PRINCIPLES.md` and ADR-0001. Phase 1 EPIC bodies that violate any rule are rejected at review.

### 2.1 GitOps is the only deployment path

Flux-only. **No `kubectl apply` in production.** **No `helm install` in production.** **No `exec.Command("helm", …)` or equivalents anywhere.** Catalyst components observe via watch streams or write to Gitea repos that Flux reconciles.

### 2.2 Crossplane is cloud-only

Crossplane manages cloud-provider APIs (Hetzner Servers, OCI compute, S3 buckets, etc.). It does **not** do K8s-to-K8s composition. RoleBindings, Kustomizations, ConfigMaps from a higher-level intent CR are reconciled by Flux Kustomizations or thin in-cluster controllers — never a Crossplane Composition.

This is a change in posture from the existing repo state: the `XUserAccess` Composition uses `provider-kubernetes` to write RoleBindings. **Phase 0 migrates this to a Go in-cluster controller** (see §3.5). Future K8s-to-K8s reconciliation follows the new path.

### 2.3 Five backing stores. Period

Every component picks from this list; nothing else qualifies.

| Store | Tech | Use |
|---|---|---|
| SQL | CNPG | Transactional state — Keycloak, PowerDNS, billing |
| Document | FerretDB on CNPG | Marketplace catalog item specs, nested document shapes |
| KV / cache | Valkey | Sessions, rate-limit counters, idempotency keys, ephemeral pubsub |
| Messaging | NATS JetStream | Audit log, billing events, cross-replica fan-out, cross-region Mirror streams |
| Object | SeaweedFS | Bastion/pod session recordings, large blobs |

No new MongoDB. No new MySQL. No Redis (Valkey substitutes). No Redpanda. No Kafka in Catalyst itself. No MinIO (SeaweedFS substitutes).

### 2.4 K8s itself is the database for cluster state

No shadow store mirrors pods/deployments/services into a separate database. catalyst-api holds an in-process informer cache (`internal/k8scache.Factory`) that is rebuilt from the kube-apiserver on cold start.

### 2.5 Event-driven, never polling

State observed via K8s watch streams. UI updates via SSE. No `time.Tick` poll loops. No `setInterval` HTTP polls anywhere in the read path.

### 2.6 Tenancy is K8s-native

An `Organization` is `namespace + vCluster + Keycloak group + Organization CR`. Per-Org isolation lives in the vCluster layer. Resource names below the namespace **never** embed the Org slug — the namespace is the parent.

### 2.7 Identity is Keycloak

Per-Sovereign realm (corporate Sovereigns) or per-Org realm (SME Sovereigns). OIDC tokens flow end-to-end — the Sovereign's K8s api-server validates them via `--oidc-*` flags. Corporate Sovereigns federate Azure SSO via Keycloak Identity Provider broker.

### 2.8 Browser access is via Guacamole

Bastion sessions, pod consoles, RDP/VNC into VM workloads. One protocol, one audit log, one session-recording path — recordings on SeaweedFS.

### 2.9 Catalyst events flow on NATS JetStream

Audit log, user actions, billing, cross-replica fan-out, cross-region Mirror streams. Streams configured in Phase 0 (currently the chart has no `templates/` dir).

### 2.10 IaC always — every parameter is a variable

No region, replica count, TTL, weight, retention, or other knob is hardcoded. UI surfaces them; CRDs persist them in Gitea.

---

## 3. Phase 0 — Foundation contracts

Lands the cross-cutting primitives every later EPIC keys off. **Single team, serial.** Phase 1 EPICs depend on Phase 0 acceptance.

### 3.1 ADR-0001 ratification

- Promote `Status: Proposed` → `Status: Accepted`.
- Add §2.3 amendment paragraph: "Reconciling RoleBindings, Kustomizations, ConfigMaps, and other K8s-to-K8s objects is the responsibility of Flux Kustomizations or thin in-cluster controllers — not Crossplane Compositions. The `useraccess-controller` is the canonical example: it watches `UserAccess` CRs and reconciles RoleBindings/ClusterRoleBindings via the kubernetes Go clientset."

### 3.2 CRD set

All in `apps.openova.io/v1`, `orgs.openova.io/v1`, or `catalyst.openova.io/v1` per the type domain. Land in `products/catalyst/chart/crds/` (or per-product subdir). Validation hooks run as a Kyverno `ClusterPolicy`.

#### 3.2.1 `Organization` (`orgs.openova.io/v1`)

```yaml
apiVersion: orgs.openova.io/v1
kind: Organization
metadata:
  name: acme
spec:
  slug: acme                                # ^[a-z][a-z0-9-]{2,31}$
  displayName: ACME Corp
  kind: customer | internal                 # corporate-customer vs internal-department
  tier: sme | corporate                     # SME vs corporate Sovereign style
  billingMode: real | chargeback | showback
  sovereignRef: omantel.omani.works
  parentOrg: ""                             # for nested orgs (rare; allowed in corporate)
  defaultEnvironmentType: prod | dev | ...
  owners:
    - email: ceo@acme.com
      role: owner
  identity:
    federationProvider: ""                  # empty | azure-sso | okta | generic-oidc
    federationConfig:
      issuer: ""
      clientId: ""
      clientSecretRef: { name: "", key: "" }
status:
  vcluster: { name: "", phase: "" }
  keycloakGroup: { id: "", path: "" }
  giteaOrg: { name: "", repos: [] }
  conditions: []
```

#### 3.2.2 `Environment` (`catalyst.openova.io/v1`)

```yaml
apiVersion: catalyst.openova.io/v1
kind: Environment
metadata:
  name: acme-prod
spec:
  organizationRef: acme
  envType: prod                             # prod | stg | uat | dev | poc
  placement: single-region | multi-region
  regions:
    - { provider: hetzner, region: fsn, buildingBlock: rtz }
    - { provider: hetzner, region: hel, buildingBlock: rtz }
  policyRef: acme-prod-policy               # → EnvironmentPolicy CR
status:
  vclusters: [{ host: hz-fsn-rtz-prod, name: acme, phase: Ready }, ...]
  giteaRepoRef: { org: acme, branch: main }
  conditions: []
```

#### 3.2.3 `Application` (`apps.openova.io/v1`)

The most-referenced CRD. Schema mirrors `BLUEPRINT-AUTHORING.md` §3 (`configSchema` parameters validate against `Blueprint.spec.configSchema`).

```yaml
apiVersion: apps.openova.io/v1
kind: Application
metadata:
  name: marketing-site
  namespace: acme                           # = vCluster name on host cluster
spec:
  environmentRef: acme-prod
  blueprintRef:
    name: bp-wordpress
    version: 1.3.0
  placement: single-region | active-active | active-hotstandby
  regions:
    - hz-fsn-rtz-prod                       # primary first
    - hz-hel-rtz-prod                       # standby/replica
  parameters:                               # validated against Blueprint.spec.configSchema
    domain: marketing.acme-prod.omantel.openova.io
    adminEmail: ops@acme.com
    replicas: 2
    postgres:
      mode: external
      ref: shared-postgres                  # sibling Application name
  healthCheck:
    path: /health
    port: http
    intervalSeconds: 30
  owners:
    - { email: ops@acme.com, role: admin }
    - { email: dev@acme.com, role: developer }
  topology:
    autoFailover: true                      # Continuum-driven (EPIC-6) — only meaningful for active-hotstandby
    rto: 60s
    rpo: 5s
status:
  phase: Pending | Provisioning | Ready | Degraded | Failed
  primaryRegion: hz-fsn-rtz-prod
  regions:
    - { name: hz-fsn-rtz-prod, replicas: 2, ready: 2, role: primary }
    - { name: hz-hel-rtz-prod, replicas: 0, ready: 0, role: standby }
  giteaRepo: gitea.hfmp.omantel.openova.io/acme/marketing-site
  conditions: []
```

#### 3.2.4 `Blueprint` (`catalyst.openova.io/v1`)

Promoted from doc-contract to schema-validated CRD. Fields per `BLUEPRINT-AUTHORING.md` §3: `card`, `visibility`, `owner`, `configSchema`, `placementSchema`, `depends`, `manifests.source`, `overlays`, `upgrades`, `rotation`, `observability`. The 40 existing `platform/*/blueprint.yaml` and `products/*/blueprint.yaml` files validate against this CRD at admission.

#### 3.2.5 `EnvironmentPolicy` (`catalyst.openova.io/v1`)

Holds compliance config + promotion gating + placement defaults.

```yaml
apiVersion: catalyst.openova.io/v1
kind: EnvironmentPolicy
metadata:
  name: acme-prod-policy
spec:
  promotion:
    requiredApprovers: 2
    soakHours: 4
  compliance:
    weights:                                # extends sample list in §4 below
      multiReplica: 15
      pdb: 15
      topologySpread: 10
      probesPresent: 5
      resourceRequests: 8
      resourceLimits: 4
      pvcExpansion: { weight: 0, scope: stateful }   # N/A on stateless; weight=0 effectively
      hpaEffective: 8
      cilium-l7-mtls: 10
      flux-managed: 10
      harbor-proxy-pull: 5
      image-tag-pinned: 5
      prometheus-scrape: 5
      networkpolicy-present: 0              # default 0 = not in score; flip on per Org
      otel-injected: 0
      hubble-flows-seen: 0
      run-as-non-root: 0
      readonly-root-fs: 0
      cosign-verified: 0
      secret-not-in-env: 0
      backup-configured: 0
    modes:                                  # permissive vs enforcing per policy
      multiReplica: permissive
      pdb: permissive
      probesPresent: enforcing
      flux-managed: enforcing
      harbor-proxy-pull: enforcing
      image-tag-pinned: enforcing
      cilium-l7-mtls: enforcing
      otel-injected: permissive
      # ...
```

#### 3.2.6 `SecretPolicy` (`catalyst.openova.io/v1`)

```yaml
apiVersion: catalyst.openova.io/v1
kind: SecretPolicy
metadata:
  name: acme-prod-secrets
spec:
  rotation:
    - kind: oauth-client-secret
      labelSelector: { app.kubernetes.io/managed-by: catalyst }
      ttl: 90d
      action: rotate                       # rotate | warn | block
```

#### 3.2.7 `Runbook` (`catalyst.openova.io/v1`)

Hooks for auto-remediation; declarative form factored out of operator action loops. Phase-0 lands the schema; populated in later sprints by SRE Lead.

#### 3.2.8 `Continuum` (`dr.openova.io/v1`)

```yaml
apiVersion: dr.openova.io/v1
kind: Continuum
metadata:
  name: marketing-site-dr
  namespace: acme
spec:
  applicationRef: marketing-site
  primaryRegion: hz-fsn-rtz-prod
  hotStandbyRegions:
    - hz-hel-rtz-prod
  leaseClient:
    kind: cloudflare-kv | dns-quorum
    config:
      kvNamespaceId: ""                    # if cloudflare-kv
      resolvers: [8.8.8.8, 1.1.1.1, 9.9.9.9]   # if dns-quorum
      ttlSeconds: 30
      renewSeconds: 10
  luaRecord:
    selector: ifurlup | pickclosest | pickfirst | pickwhashed
    healthCheck:
      url: https://marketing.acme-prod.omantel.openova.io/health
      intervalSeconds: 5
      timeoutSeconds: 2
  rto: 60s
  rpo: 5s
status:
  primaryRegion: hz-fsn-rtz-prod
  leaseHolder: hz-fsn-rtz-prod
  leaseExpiresAt: "2026-05-08T19:50:30Z"
  replicationLag: { hz-hel-rtz-prod: 3.2s }
  lastSwitchover:
    at: ""
    from: ""
    to: ""
    reason: ""
    rtoObserved: ""
    rpoObserved: ""
  conditions: []
```

### 3.3 Controllers

All Go binaries. Live under `core/controllers/<name>/cmd/main.go`, driven by `controller-runtime` + `client-go`. Containers signed via cosign in CI; deployed via Flux HelmReleases on the management cluster (Phase 0) or per-data-plane cluster (some — see EPIC §s).

| Controller | Watches | Reconciles | Where it runs |
|---|---|---|---|
| `organization-controller` | `Organization` CR | vCluster + Keycloak group + Gitea Org + base RBAC | mgmt cluster |
| `environment-controller` | `Environment` CR | per-app Gitea repo branches + per-vCluster Flux GitRepository + JetStream subjects | mgmt cluster |
| `blueprint-controller` | `Blueprint` CR | catalog mirror (public → sovereign-curated → per-Org) | mgmt cluster |
| `application-controller` | `Application` CR | per-region Gitea manifest writes; honors Placement | mgmt cluster |
| `useraccess-controller` | `UserAccess` CR | RoleBinding + ClusterRoleBinding via kubernetes clientset | per data-plane cluster |
| `continuum-controller` | `Continuum` CR + `Application` CR | lease, replication health, switchover sequence, lua-record body via PDM | mgmt cluster |
| `compliance-aggregator` (extends k8scache) | `PolicyReport`, `ClusterPolicyReport`, custom evaluators | Score rollups → SSE + NATS `policy-rollup` KV | per data-plane cluster |

### 3.4 Keycloak full-CRUD

Extend `internal/keycloak/client.go` to cover realm, client, role, role-mapping, group hierarchy, and identity-provider CRUD. Add a higher-level `internal/keycloak/admin.go` for find-or-create-role and assign-tier-to-user-with-scope (Manara-style).

Add `groups[]` and `realm_access.roles[]` to the parsed claims in `auth/Claims` so that authorization context flows into request scope. Cache effective permissions in Valkey (8h TTL); invalidate via Keycloak `events.endpoints` webhook on group change.

### 3.5 useraccess-controller (replaces Crossplane Composition)

Today: `XUserAccess` Composition writes RoleBindings via `provider-kubernetes` — but provider-kubernetes is **not installed** on any cluster. Silent P0 bug.

Phase 0:

1. Author `useraccess-controller` Go binary. Logic mirrors Manara: AND-within-UserAccess scope, OR-across-UserAccess, label-based scope match.
2. Reconciles `UserAccess` CR → RoleBinding/ClusterRoleBinding using `client-go`.
3. Honors `enforced_scopes` per catalog tier (developer auto-gets `env-type=dev`).
4. **Delete** `XUserAccess` Composition + the orphaned `provider-kubernetes` Provider package reference.

### 3.6 Label-vocabulary enforcement

Two Kyverno `ClusterPolicy` resources land in `bp-kyverno`:

- `mutate-add-openova-labels` — at admission, derives missing labels from owning Organization/Environment/Application/Blueprint CR refs and adds them.
- `validate-require-openova-labels` — refuses resources without required labels (permissive in Phase 0; flipped to enforcing per Org via EnvironmentPolicy in EPIC-1).

### 3.7 vCluster scaffold

Phase-0 introduces a **thin in-cluster controller** (per §2.2 — no Crossplane K8s-to-K8s) that wraps `bp-vcluster` HelmRelease per Organization, plus a base set of resources (default-deny CCNP, Keycloak realm-link, Gitea repo).

The contabo-mkt `clusters/contabo-mkt/tenants/` pattern (already working as raw HelmReleases) is the model — the controller materializes those manifests from `Organization` CR fields.

### 3.8 Multi-cluster substrate

3 Hetzner regions provisioned by the existing OpenTofu module after one fix: today `infra/hetzner/main.tf` only wires `var.regions[0]` end-to-end. Phase 0 wires all entries.

| Cluster | Role | Topology | Notes |
|---|---|---|---|
| `hz-nbg-mgt-prod` | management | single-node merged-CP+worker | Catalyst control plane lives here |
| `hz-fsn-rtz-prod` | data plane | 1 CP + 3 workers | Per-Org vClusters live here |
| `hz-hel-rtz-prod` | data plane | 1 CP + 3 workers | Per-Org vClusters live here (multi-region pair) |

Cilium ClusterMesh enabled between `hz-fsn-rtz-prod` and `hz-hel-rtz-prod`. The mgmt cluster does **not** join ClusterMesh — it reaches data planes via NetBird mesh and direct K8s API calls.

### 3.9 Cleanup / bug-fixes (P0)

Bundled into Phase 0 because every later EPIC trips on at least one of these.

| # | Issue | Fix |
|---|---|---|
| 1 | Cilium subchart 1.16.5 vs values.yaml claiming 1.19.3 | Pin to one stable, align values, update Chart.lock |
| 2 | `omantel.omani.works/`, `otech.omani.works/` drifted from `_template/` | Reconcile to template; CI gate on diff |
| 3 | `provisioningstate.yaml` CRD = 0 bytes | Delete (catalyst-api uses PVC + flat-file event log per ADR-0001 §4.1) |
| 4 | NATS JetStream chart has no `templates/` | Add Stream + KV CRs |
| 5 | OTel Operator not deployed | Add HelmRelease |
| 6 | `local-path` StorageClass blocks multi-node CNPG primary/replica | Add `hcloud-volumes` CSI as default for stateful |
| 7 | Hubble relay+UI off | Turn on, expose behind Cilium Gateway with OIDC |
| 8 | No default-deny CCNP baseline | Add `default-deny-all` CCNP + per-namespace allow templates |
| 9 | `provider-kubernetes` referenced but not installed | Delete reference (per §3.5) |

### 3.10 Phase 0 acceptance gate

- All 8 CRDs present; `kubectl explain` works; schema validation rejects malformed inputs.
- All 7 controllers running and reconciling.
- Demo Organization bring-up via single API call: vCluster + Keycloak group + Gitea Org + base RBAC materialize within 60s.
- Demo Application install via single API call: Blueprint resolved, manifests in Gitea, Flux reconciles, Pod Ready in <3 min on `hz-fsn-rtz-prod`.
- Cilium ClusterMesh: a Service in vcluster-acme on fsn reachable via cross-cluster FQDN from a Pod in vcluster-acme on hel.
- 2× consecutive GREEN qa-loop on the foundation slice.

---

## 4. EPIC-1 Compliance (#1096)

### 4.1 Engine

- **Kyverno** is the only admission/audit engine. `validationFailureAction: Audit` for permissive policies, `Enforce` for enforcing — same policy YAML.
- The existing **k8scache watcher** (`internal/k8scache/`) extends to subscribe to `PolicyReport` and `ClusterPolicyReport` CRs.
- **Custom evaluators** for non-Kyverno checks live as small Go evaluators in the same watcher process. They emit synthetic `PolicyReport`-like rows so the aggregation path is uniform:
  - HPA-effective: HPA min replicas met by current Deployment.replicas.
  - OTel-sidecar-injected: Pod has `otel-collector` sidecar OR namespace has `Instrumentation` CR + auto-inject annotation.
  - Hubble-flows-seen: Cilium Hubble has observed at least one flow to/from this Pod in last 5 min.
  - Image-via-Harbor-proxy: container image refs `harbor.<sovereign-domain>/proxy-ghcr/...`.
  - Crossplane-managed-by-flux: Crossplane-managed-resource has `app.kubernetes.io/managed-by: flux` label.

### 4.2 Score aggregator

A new handler `internal/handler/compliance.go` joins:
1. PolicyReport rows (per-resource, per-policy)
2. Custom-evaluator rows
3. EnvironmentPolicy weights (per-Org override)

→ produces per-resource score.

Roll-ups:
- Per-Application: weighted average across Application-scoped resources.
- Per-Environment: weighted average across Applications.
- Per-Organization: weighted average across Environments.
- Per-Sovereign: weighted average across Organizations.

Output: SSE channel + NATS `policy-rollup` KV. Time-series retention in Mimir for trend dashboards.

### 4.3 Sample policy set

| Policy | Domain | Default weight | Default mode |
|---|---|---|---|
| Multi-replica (drainability gate) | resilience | 15% | permissive |
| PodDisruptionBudget present + permits eviction | resilience | 15% | permissive |
| Topology Spread across nodes | resilience | 10% | permissive |
| Liveness + Readiness probes | resilience | 5% | enforcing |
| Resource requests (CPU + memory) | resilience | 8% | enforcing |
| Resource limits | resilience | 4% | permissive |
| PVC volume expansion (stateful only — N/A drops from denominator) | resilience | configurable | permissive |
| Autoscaler (HPA/VPA) effective | resilience | 8% | permissive |
| Cilium ServiceMesh L7 mTLS (zero-trust) | security | 10% | enforcing |
| Flux-managed (GitOps) | governance | 10% | enforcing |
| Images via Harbor proxy | governance | 5% | enforcing |
| Image tag pinned (no `:latest`) | governance | 5% | enforcing |
| Prometheus scrape target | observability | 5% | permissive |
| NetworkPolicy present | security | configurable | permissive |
| OTel auto-instrumentation present | observability | configurable | permissive |
| Hubble flows observed (last 5m) | security | configurable | permissive |
| runAsNonRoot + readOnlyRootFilesystem | security | configurable | permissive |
| Image signature verified (cosign) | security | configurable | permissive |
| Secret not in env vars (prefer mounted file) | security | configurable | permissive |
| Backup configured (Velero schedule for stateful) | resilience | configurable | permissive |

`PVC volume expansion` is `N/A` for stateless workloads — the score normalizer drops `N/A` from the denominator.

### 4.4 UI

- **SRE Lead dashboard**: fleet view (Sovereigns × Organizations × Applications × score).
- **Security Lead dashboard**: same, sliced by security domain.
- **Org owner / App owner view**: this Application's score, drift panel, "what would I need to fix to reach 90%".
- **Per-policy drill-down**: every offending resource, the violating field, suggested fix.
- **Permissive/enforcing toggle** per policy, per Environment.

### 4.5 Acceptance

- Every Application gets a score within 60s of install.
- Score updates within 5s of a policy violation.
- Toggling permissive → enforcing actually blocks new violators at admission within 30s.
- 2× consecutive GREEN qa-loop on a synthetic Application matrix.

---

## 5. EPIC-2 Applications (#1097)

### 5.1 Application controller

- Reconciles `Application` CR → per-region Gitea repo manifest writes → Flux GitRepository + Kustomization → HelmRelease per Blueprint chart.
- `placement: active-active` writes to all `regions[]` simultaneously.
- `placement: active-hotstandby` writes to both regions but flips a `replica: 0` value in the standby region — Continuum (#1101) manages the failover flip.
- Validates `Application.spec.parameters` against `Blueprint.spec.configSchema` at admission.

### 5.2 Blueprint controller + catalog-svc

- `blueprint-controller` validates `Blueprint` CRs at admission.
- `catalog-svc` (new Go service in `core/services/catalog/` migrating SME catalog code) reads from:
  - Public catalog mirror (`catalog` Gitea Org on the Sovereign — auto-mirrored from public openova repo via CI).
  - Sovereign-curated `catalog-sovereign` Gitea Org.
  - Per-Org `<org>/shared-blueprints` Gitea repo.
- Exposes REST + GraphQL.
- CI wiring: `.github/workflows/blueprint-release.yaml` per `BLUEPRINT-AUTHORING.md` §2.

### 5.3 Live install flow UI

- Replace static `pages/sovereign/applicationCatalog.ts` with live data from catalog-svc.
- Auto-form generator: render `configSchema` into a form (`@rjsf/core` JSON-Schema → React form library).
- Install handler: POST → catalyst-api creates Application CR; UI polls/SSE for status.
- Topology editor on the Application page: `single-region | active-active | active-hotstandby` + regions[] picker.

### 5.4 Org owner self-service

Per-Application:
- Owners list with roles (admin, developer, viewer); edits flow to Keycloak via #1098 RBAC.
- Settings page: parameters editor (re-validates against configSchema), upgrade dialog, uninstall dialog.
- Detail tabs: Overview / Topology / Resources (drills into #1099 k9s view scoped to this app's namespaces) / Compliance (#1096 score) / Logs / Events / Settings.

### 5.5 Acceptance

- User installs `bp-wordpress` from catalog into a fresh Org in <60s, Ready in <3 min on `hz-fsn-rtz-prod`.
- Same user flips topology to `active-hotstandby` adding `hz-hel-rtz-prod`; replicas materialize in hel within 5 min.
- Org user pushes Blueprint to `<org>/shared-blueprints`, sovereign-admin curates it, appears in catalog.
- 2× consecutive GREEN qa-loop on Blueprint matrix × topology matrix.

---

## 6. EPIC-3 RBAC (#1098)

### 6.1 useraccess-controller (already specified in §3.5)

### 6.2 Catalog tier system

5 fixed tiers, each = a Keycloak realm role mapped to a ClusterRole:

| Tier | Level | Key actions | Auto-injected scope |
|---|---|---|---|
| viewer | 10 | `*.read` | — |
| developer | 20 | viewer + `workloads.exec`, `workloads.console`, `tickets.create/update`, `sessions.playback` | `env-type=dev` |
| operator | 30 | developer + `console.connect.admin`, `sam.manage`, `patches.manage`, `tickets.accept` | — |
| admin | 40 | operator + `compute.* (except delete)`, `credentials.*`, `applications.*`, `actions.*`, `accounts.*`, `networks.*`, `sessions.*` | — |
| owner | 50 | admin + `rbac.*`, `organization.*` | — |

Action sets baked into `catalog-tier.yaml` ConfigMap. ClusterRoles rendered from it by useraccess-controller at startup.

### 6.3 Scope = label-based

`UserAccess.spec.scopes: [{labelKey, labelValue}]`. AND within a UserAccess, OR across UserAccess. Wildcard scope `[{*: *}]` = global access.

Find-or-create-role pattern: `/rbac/assign` endpoint takes `{user, tier, scope}`, finds or creates the matching `UserAccess`, materializes Keycloak group attributes + RoleBinding via useraccess-controller.

### 6.4 Boundary between internal teams and customer orgs

Both are `Organization` CRs. Difference is `kind: internal | customer` + `billingMode`. Useraccess-controller refuses cross-Org grants from `internal` to `customer` and vice versa unless signed by the management Org owner — encoded as a Kyverno validating policy on `UserAccess` admission.

### 6.5 Corporate SSO federation

Per-Org Keycloak Identity Provider config. Sovereign-admin UI configures Azure SSO / Okta / generic OIDC. Per-Org SSO: corporate Orgs federate their own IdP into their Org's vCluster via realm-level federation.

### 6.6 UI

- Multi-grant editor (replaces single-grant).
- Keycloak user picker (search by email/name, federated when configured).
- Keycloak group browser; realm/client/role browser (sovereign-admin only).
- Per-Application "Members" tab; Per-Organization "Members" page.
- Access matrix view (Manara-style — users × applications × tier with warnings).
- Audit trail of role assignments.

### 6.7 Acceptance

- Sovereign-admin assigns "developer" tier scoped to `application=wordpress, env-type=dev` — RoleBinding materializes in <30s.
- Org owner adds sub-user with "admin" role to a single Application from the Application page; effective in <30s.
- Azure-SSO federation works for a demo corporate Org.
- 2× consecutive GREEN qa-loop against the access matrix.

---

## 7. EPIC-4 Cloud Resources (#1099)

### 7.1 Resource browser extension

`pages/sovereign/cloud-list/K8sListPage.tsx` extended to a full k9s-on-web:

- **Drill-down**: list row → detail page with tabs.
- **Resource tree** per detail: ownerReferences up + label selectors down (Deploy → RS → Pod, StatefulSet → PVC, Service → Endpoints → Pods).
- **YAML editor** with diff preview before apply (validates via dry-run; commits via Flux PR for `managed-by=flux`, direct apply for `managed-by=manual` with audit log).
- **Events panel** per resource (extend k8scache to include `Event` kind).
- **Metrics panel** per resource (kube-state-metrics + Prometheus → CPU/mem/disk surface).
- **Per-row actions**: scale, restart, delete, edit YAML (RBAC-gated).

### 7.2 Logs WebSocket

- catalyst-api `/api/v1/sovereigns/{id}/k8s/logs/{ns}/{pod}/{container}?follow=true&tailLines=100` WebSocket endpoint.
- Streams kubelet logs API directly (no aggregation in catalyst-api memory).
- xterm.js client; supports color, search, copy, persistent scrollback.

### 7.3 Guacamole

- New `platform/guacamole/chart/` per `BLUEPRINT-AUTHORING.md` §2.
- Helm templates: guacd Deployment, Guacamole webapp, k8s-ws-proxy DaemonSet, SeaweedFS PVC for recordings, Service, Ingress via Cilium Gateway, Keycloak OIDC client.
- Realm + client provisioned via the per-Sovereign `keycloak-config-cli` ConfigMap pattern.
- **One Guacamole per Sovereign** (not Manara fan-out — Sovereigns stay self-sufficient).

### 7.4 k8s-ws-proxy

- New Go binary `core/cmd/k8s-ws-proxy/`.
- HMAC-signed WebSocket proxy (`X-Catalyst-HMAC: SHA256({timestamp}:{path})`).
- Forwards to local kube-apiserver `/api/v1/.../pods/exec`.
- Echoes `Sec-WebSocket-Protocol: v4.channel.k8s.io`.
- Tmux-connect cascade for bastion shells.

### 7.5 Exec console UI

- Per-Pod "Open Shell" → catalyst-api creates Guacamole connection → returns embedded URL.
- xterm.js fallback for environments where iframe is blocked.
- Session list view: live + historical, with replay (RBAC: `sessions.playback`).

### 7.6 Projector (CQRS read-side)

- New Go binary `core/cmd/projector/`.
- Subscribes to NATS `catalyst.events`, projects into Valkey KV under `cluster:{cluster}:kind:{kind}:{namespace}/{name}`.
- catalyst-api SSE endpoint reads from Valkey KV (cross-replica fan-out).
- Replay window: NATS retention 24h; cold-start full reconcile from K8s LIST + replay.

### 7.7 Acceptance

- Operator browses Pods, drills into Deployment, sees Pod tree, opens logs, opens exec shell — all in <2s page transitions.
- Exec session is recorded; session list shows the recording; replay works from a different browser.
- YAML edit-and-apply works for `managed-by=manual` ConfigMap; YAML edit on `managed-by=flux` Service opens a Gitea PR.
- 2× consecutive GREEN qa-loop on resource-actions matrix.

---

## 8. EPIC-5 Networking (#1100)

### 8.1 Cilium hardening

- Pin subchart + values.yaml to one recent stable (1.16.6+ or 1.17.x).
- Default-deny `CiliumClusterwideNetworkPolicy` baseline.
- Per-namespace allow templates instantiated by organization-controller per Org vCluster.
- Application-controller adds per-Application egress rules from `Blueprint.spec.networking.egress`.

### 8.2 Hubble

- `hubble.relay.enabled = true`, `hubble.ui.enabled = true`.
- Hubble UI exposed behind Cilium Gateway with OIDC (Keycloak `hubble-ui` client).
- RBAC: `hubble.read` on viewer+ tier.

### 8.3 ClusterMesh

- Enable between `hz-fsn-rtz-prod` and `hz-hel-rtz-prod` (Phase 0 substrate already provisions both).
- WireGuard transparent encryption already on.
- Sample cross-cluster Service test: a `Service` in vcluster-acme on fsn reachable as `<svc>.<ns>.svc.acme.fsn.global` from a pod in vcluster-acme on hel.
- Document the FQDN pattern for Application authors.

### 8.4 OTel auto-instrumentation

- Install `OpenTelemetry Operator` HelmRelease.
- Default `Instrumentation` CR (Java/.NET/Node/Python) per Application namespace.
- Opt-in via Pod annotation `instrumentation.opentelemetry.io/inject-{lang}: "true"` (or auto by Org policy).
- Wire collector exporters: traces → Tempo, logs → Loki, metrics → Mimir.
- Propagate trace context via Cilium Envoy.
- Go: sidecarless eBPF auto-instrumentation is a follow-up — Phase 1 starts with operator-managed languages.

### 8.5 DMZ vCluster pattern

- `{org}-dmz` vCluster auto-created for `Organization.spec.kind: customer + tier: corporate`.
- DMZ blueprint set: NetBird endpoint, ingress controller, WAF (Coraza), Stalwart-relay.
- Cilium L7 policy enforcing dmz → workload egress on declared service ports only.
- SME-style Orgs (`tier: sme`) skip DMZ — direct internet via Cilium Gateway.

### 8.6 NetBird inter-Sovereign mesh

- Control plane: hosted on management cluster.
- Agents on every cluster (mgmt + data planes).
- Routes: catalyst-api → data-plane K8s APIs; Continuum (#1101) lease channels traverse the mesh.
- Security: mesh mTLS via SPIRE-issued certs (SPIRE bring-up follow-on after Phase 0).

### 8.7 Acceptance

- Pod in `acme` namespace can talk to another Pod in `acme` (intra-namespace) but not to `bankdhofar` Pod by default.
- Hubble UI shows live flows for the operator's Sovereign, RBAC-scoped.
- Trace from a `wordpress` request shows up in Tempo within 30s without app-side instrumentation.
- Service in vcluster-acme on fsn reachable from Pod in vcluster-acme on hel via ClusterMesh.
- Corporate Org bring-up creates `acme` + `acme-dmz` vClusters; ingress only via DMZ.
- 2× consecutive GREEN qa-loop.

---

## 9. EPIC-6 Multi-cluster + Continuum DR (#1101)

### 9.1 Phase-0 substrate handover

Phase 0 brings up the 3 regions and ClusterMesh; this EPIC builds on that.

### 9.2 CNPG cluster-pair (the proof-point)

- New `bp-cnpg-pair` Blueprint: primary CNPG Cluster in fsn, replica `externalCluster` in hel using WAL streaming.
- Replication traffic over Cilium ClusterMesh (no public exposure).
- Failover Pod readiness: replica becomes promotable when WAL lag < threshold.
- Acceptance: write 1M rows to primary, kill primary, replica promotes, no data loss within RPO.

### 9.3 Continuum controller

- `products/continuum/chart/` per `BLUEPRINT-AUTHORING.md` §2 layout.
- `continuum-controller` Go binary.
- Watches `Continuum` CRs + `Application` CRs with placement: `active-hotstandby`.
- Per-Continuum-CR: a goroutine maintains lease (10s renew, 30s TTL), watches replication metrics from CNPG.
- **Switchover sequence**:
  1. Validate lease holder is current primary (or assume control on lease loss + witness quorum).
  2. Cordon old primary writes (CNPG-level: set `cnpg.io/cluster.primary` annotation to standby; CNPG operator demotes primary, promotes standby).
  3. Drain in-flight HTTP traffic to old primary via flipping Cilium HTTPRoute weight to 0 over 10s.
  4. Flip lua-record probe target via PDM `/v1/commit` (low-TTL DNS — default 30s).
  5. Release old lease; acquire on new primary.
  6. Uncordon new primary writes; resume traffic.
  7. Audit event on NATS `catalyst.audit`.
- **Failback handler**: when old primary recovers, repair replication direction, schedule failback (manual approval gate).
- **Lua-record body synthesizer**: for the Application's HTTPRoute hostnames, write `{ifurlup, pickclosest}` lua bodies via PDM.
- **Lease witness = Cloudflare KV** (per `SRE.md` §2.4); fallback = 3-DNS-witness quorum (8.8.8.8 + 1.1.1.1 + 9.9.9.9, 2-of-3).

### 9.4 Application-page topology UI (extends #1097)

- Topology editor: `single-region` | `active-active` | `active-hotstandby`.
- Region picker.
- Switchover button (RBAC: owner tier; confirms with diff of "what's about to happen").
- Live status panel: replication lag, lease health, last switchover event, RPO/RTO observed vs target.
- Switchover history with audit trail.

### 9.5 Multi-Sovereign fleet view

- Replace mock-data `pages/dashboard/DashboardPage.tsx` with live multi-Sovereign aggregator.
- Per-Sovereign card: health, applications count, regions, alerts.
- Cross-Sovereign view: where each Application is running, topology, DR posture.

### 9.6 Acceptance

- 3-region cluster up: 1 mgmt + 2 data planes.
- Demo Application with `active-hotstandby` runs primary in fsn, hot-standby in hel; CNPG replication healthy.
- Switchover from Application page completes in <60s with <5s write disruption (bank-tier RTO/RPO).
- Resolver clients within 30-90s observe new primary (lua-record TTL window).
- 2× consecutive GREEN qa-loop on switchover matrix (planned, primary kill, partial partition, full region outage).
- Reverse failback works once original primary recovers.

---

## 10. Dev-loop team shape

Per EPIC, embedded across the whole journey (qa-loop is part of the team from day 1, not downstream):

| Role | Count | Parallelism |
|---|---|---|
| Architect | 1 | serial — single brief, no two hats |
| Implementer | 1–3 | parallel ONLY when Architect's brief declares scopes disjoint, each in own `git worktree` |
| Test-Plan Author | 1 | serial |
| Test-Plan Reviewer | 1 | serial — 4-eyes, mandatory; refuses sign-off until every requirement maps to ≥1 test row |
| Test Executor | 1 | serial |
| Fix Author | 1–5 | parallel, max 5 disjoint clusters, worktrees |
| Cross-EPIC Coordinator | 1 | serial; lives across all 6 EPICs; owns big-picture coherence |

**Anti-divergence rules** (baked into the loop):

1. Per-slice **2× consecutive GREEN** gate before merge — drift caught in hours, not at end of EPIC.
2. Implementer's first task: acknowledge reading (a) Phase-0 contracts (this doc + ADR-0001 + NAMING-CONVENTION + INVIOLABLE-PRINCIPLES), (b) Architect brief, (c) Test Plan. No code before that.
3. Worktree isolation for parallel Implementers + parallel Fix Authors.
4. Cross-EPIC Coordinator reconciles foundation-contract drift across all 6 EPIC teams every dev-day.

**EPIC sequencing**: Phase 0 (#1095) is serial. Phase 1 (#1096–#1101) runs **6 EPICs in parallel** after Phase 0 acceptance. Peak: 6 Architects (one per EPIC) + up to 18 Implementers + 6 Test-Plan Authors + 6 Reviewers + 6 Executors + up to 30 Fix Authors + 1 Coordinator. Resource budget gate: max 5 in-flight agents at any moment per `feedback_qa_loop_parallelization_rules.md`; the rest queue. Coordinator manages the queue.

---

## 11. Decisions log (resolved tensions)

| # | Tension | Decision | Authority |
|---|---|---|---|
| 1 | ADR-0001 = "Proposed" | Ratify → "Accepted" with §2.3 amendment for in-cluster controllers | Founder via standing INVIOLABLE-PRINCIPLES authority |
| 2 | UserAccess uses Crossplane Composition + `provider-kubernetes` (not installed) | Migrate to in-cluster `useraccess-controller` (per §2.2) | Coordinator |
| 3 | Cilium 1.16.5 vs values.yaml 1.19.3 | Pin to one stable, align values.yaml | Coordinator |
| 4 | `local-path` blocks multi-node CNPG | Add `hcloud-volumes` CSI as default for stateful | Coordinator |
| 5 | Two catalogs (bp-* OCI vs SME catalog) | Unify under `catalog-svc` per ADR-0001 §4.3 | Founder via ADR |
| 6 | omantel/otech bootstrap-kit drifted from `_template` | Reconcile + CI gate on diff | Coordinator |
| 7 | NATS chart has no `templates/` | Add Stream + KV CRs per §3.9 | Coordinator |
| 8 | `provisioningstate.yaml` = 0 bytes | Delete | Coordinator |
| 9 | Apps run in vCluster-per-Org (already in NAMING §1.5) | Confirmed locked — internal-dept + customer-SME both get vClusters; only billing differs | Founder explicit response |
| 10 | OTel Operator absent; only collector with all presets off | Add Operator + Instrumentation CRs (Java/.NET/Node/Python first; Go eBPF later) | Coordinator |
| 11 | Failover-controller is README-only | Replace with Continuum product (`products/continuum/`); CRD `dr.openova.io/v1` | Coordinator |
| 12 | One Guacamole per Sovereign or Manara fan-out? | One per Sovereign (Sovereigns stay self-sufficient per `INVIOLABLE-PRINCIPLES.md` topology rule) | Coordinator |

---

## 12. Where to start

1. Founder ratifies this document by closing #1094 with `status/in-progress` (already set) and merging the PR that adds this file.
2. Phase-0 Architect (#1095) reads this document, opens a series of file-level briefs under `openova-private/.claude/architect-briefs/epic-0/`, and queues Implementer slices.
3. Cross-EPIC Coordinator (this agent) drives the loop autonomously from there. Founder sees status updates only; no check-ins required.

---

*Authoritative until the team produces a follow-up amendment ADR. Cross-reference [`adr/0001-catalyst-control-plane-architecture.md`](adr/0001-catalyst-control-plane-architecture.md), [`NAMING-CONVENTION.md`](NAMING-CONVENTION.md), [`BLUEPRINT-AUTHORING.md`](BLUEPRINT-AUTHORING.md), [`SRE.md`](SRE.md), [`MULTI-REGION-DNS.md`](MULTI-REGION-DNS.md), [`INVIOLABLE-PRINCIPLES.md`](INVIOLABLE-PRINCIPLES.md).*
