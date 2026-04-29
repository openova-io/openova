# Blueprint Authoring

**Status:** Authoritative target spec. **Updated:** 2026-04-27.
**Implementation:** The Blueprint CRD, `blueprint-controller`, and CI fan-out described below are design-stage. See [`IMPLEMENTATION-STATUS.md`](IMPLEMENTATION-STATUS.md). Today, `platform/<name>/` and `products/<name>/` folders contain only README.md.

How to author a **Blueprint** for Catalyst — the unified unit of installable software (replaces what was previously called "module" + "template"). Defer to [`GLOSSARY.md`](GLOSSARY.md) for terminology and [`ARCHITECTURE.md`](ARCHITECTURE.md) for the broader model.

---

## 1. What a Blueprint is

A Blueprint is:

- A **source location** (one of three Gitea-Org-scoped places, all using identical Blueprint shape):
  - **Public Blueprints**: a directory under `platform/<name>/` or `products/<name>/` in the [`github.com/openova-io/openova`](https://github.com/openova-io/openova) monorepo (this repository). Per-Blueprint isolation is provided by CI fan-out — each folder publishes its own signed OCI artifact. Visible to every Sovereign via the `catalog` Gitea Org mirror.
  - **Sovereign-curated private Blueprints**: a Gitea Repo under the `catalog-sovereign` Gitea Org on a Sovereign (e.g. `gitea.<location-code>.<sovereign-domain>/catalog-sovereign/bp-<name>/`). Authored by the Sovereign owner, visible to every Catalyst Organization on that Sovereign without being public upstream. Use case: an SME-marketplace operator (like `acme-telecom`) curates `bp-wordpress`, `bp-jitsi`, `bp-cal-com` for their tenants.
  - **Org-private Blueprints**: a directory inside `gitea.<location-code>.<sovereign-domain>/<org>/shared-blueprints/bp-<name>/` in that Organization's Gitea repo on its Sovereign (canonical Catalyst control-plane DNS form per [`NAMING-CONVENTION.md`](NAMING-CONVENTION.md) §5.1). Visible only within that Org.
- A **CRD manifest** (`blueprint.yaml`) declaring its identity, configSchema, placementSchema, dependencies, and pointers to its manifests.
- A **set of manifests** (Helm chart, Kustomize base + overlays, or raw YAML) that get applied when the Blueprint is installed as an Application.
- A **set of Crossplane Compositions** (optional) for any non-Kubernetes resources the Blueprint provisions.
- A **CI pipeline** that signs the artifact (cosign), generates an SBOM (Syft), publishes to OCI registry (`ghcr.io/openova-io/bp-<name>:<semver>`), and tags a release.

One Blueprint = one card in the marketplace (when `visibility: listed`).

> **Why monorepo for public Blueprints**: a single repository is simpler to govern, gives one consistent CI pipeline shape across all components, and avoids the per-repo overhead of permissions, settings, and dependabot config. Per-Blueprint isolation is provided at the **OCI artifact** layer, not the Git repo layer — `ghcr.io/openova-io/bp-<name>:<semver>` artifacts are independently versioned, signed, and consumed.

---

## 2. Folder layout

A Blueprint folder lives at `platform/<name>/` or `products/<name>/` in the [`github.com/openova-io/openova`](https://github.com/openova-io/openova) monorepo. The CI pipeline at the monorepo root detects changes per folder and publishes per-Blueprint OCI artifacts.

```
platform/<name>/                 ← OR products/<name>/ for composite Blueprints
├── blueprint.yaml               ← the Blueprint CRD manifest
├── README.md                    ← what it does, links to docs
├── chart/                       ← Helm chart (preferred for typical apps)
│   ├── Chart.yaml
│   ├── values.yaml
│   └── templates/
│   OR
├── manifests/                   ← Kustomize base
│   ├── base/
│   │   ├── kustomization.yaml
│   │   ├── deployment.yaml
│   │   ├── service.yaml
│   │   └── ingress.yaml
│   └── overlays/
│       ├── small/
│       ├── medium/
│       └── large/
├── compositions/                ← (optional) Crossplane Compositions
│   ├── postgres-database.yaml
│   └── object-storage-bucket.yaml
├── card/                        ← marketplace presentation
│   ├── icon.svg
│   ├── screenshots/
│   └── description.md
└── tests/                       ← acceptance tests
    ├── integration.yaml         ← Litmus probe / Catalyst test harness
    └── upgrade.yaml
```

The CI workflow lives **once** at the monorepo root (`.github/workflows/`) and uses path-based matrix builds — every `blueprint.yaml` triggers its own pipeline:

```yaml
# .github/workflows/blueprint-release.yaml (monorepo root, path-matrix)
on:
  push:
    tags: ['platform/*/v*', 'products/*/v*']    # tag form: platform/<name>/v1.2.3
  pull_request:
    paths: ['platform/**', 'products/**']
```

This shape is documented as the design contract; the workflow itself is not yet implemented (see [`IMPLEMENTATION-STATUS.md`](IMPLEMENTATION-STATUS.md)).

---

## 3. The Blueprint CRD

Annotated example for `bp-wordpress`:

```yaml
apiVersion: catalyst.openova.io/v1alpha1
kind: Blueprint
metadata:
  name: bp-wordpress
  version: 1.3.0
spec:

  card:                                # presentation in marketplace
    title: WordPress
    tagline: Self-hosted CMS
    category: cms
    tags: [cms, blog, php]
    icon: ./card/icon.svg
    screenshots:
      - ./card/screenshots/admin.png
      - ./card/screenshots/post-editor.png
    license: GPL-2.0
    documentation: https://wordpress.org/documentation

  visibility: listed                   # listed | unlisted | private

  owner:
    team: apps                         # team responsible for upkeep
    contact: apps@openova.io

  configSchema:                        # JSON Schema; drives console form
    type: object
    required: [domain, adminEmail]
    properties:
      domain:
        type: string
        format: hostname
        description: Public domain for the site
      adminEmail:
        type: string
        format: email
      title:
        type: string
        default: "My WordPress site"
      replicas:
        type: integer
        default: 2
        minimum: 1
        maximum: 20
      postgres:
        type: object
        oneOf:
          - properties:
              mode: { const: embedded }
          - properties:
              mode: { const: external }
              ref:
                type: string
                description: Name of an existing bp-postgres Application

  placementSchema:                     # supported placement modes
    modes: [single-region, active-active, active-hotstandby]
    minRegions: 1
    maxRegions: 5

  depends:                             # dependency declarations
    - blueprint: bp-postgres
      version: ^1.4
      alias: db
      when: "{{ .config.postgres.mode == 'embedded' }}"
      values:
        databases: ["{{ .application.name }}"]
        size: medium

  manifests:                           # how to materialize on install
    source:
      kind: HelmChart
      ref: oci://ghcr.io/openova-io/bp-wordpress:1.3.0
    overlays:                          # vendor sizing variants
      small:
        replicas: 1
        postgres: { mode: embedded, size: small }
        backups: { schedule: weekly }
      medium:
        replicas: 2
        postgres: { mode: embedded, size: medium }
        backups: { schedule: daily }
      large:
        replicas: 5
        postgres: { mode: external }
        backups: { schedule: daily }
        pdb: true
        hpa: true

  upgrades:                            # supported upgrade paths
    from:
      - 1.2.x                          # safe automatic
      - 1.1.x                          # requires data migration
    blocks:
      - 1.0.x                          # no path; recreate

  rotation:                            # secrets this Blueprint owns
    - kind: oauth-client-secret
      name: wp-keycloak-client
      ttl: 90d

  observability:                       # what this Blueprint emits
    metrics: prometheus
    logs: stdout
    traces: otlp
```

---

## 4. configSchema design

The console form is generated from `configSchema` — never hand-written. JSON Schema features supported:

- `type`, `format`, `default`, `enum`, `minimum`, `maximum`
- `oneOf` / `anyOf` for branching (e.g. embedded vs external Postgres)
- `properties.x.description` becomes form help text
- `dependencies` for conditional fields
- `x-catalyst-ui-hint` for non-trivial widgets:
  - `password` — masked input
  - `domain-picker` — autocomplete from existing Org domains
  - `application-ref` — picker over existing Apps in the Environment matching a Blueprint filter

Example with hint:

```yaml
postgres:
  type: object
  properties:
    ref:
      type: string
      x-catalyst-ui-hint: application-ref
      x-catalyst-ui-filter:
        blueprint: bp-postgres
        environment: current
```

The console renders this as a dropdown of existing postgres Applications in the current Environment.

---

## 5. Dependencies

### 5.1 Hard dependencies

```yaml
depends:
  - blueprint: bp-postgres
    version: ^1.4
    alias: db
```

Catalyst will install `bp-postgres` if not already present. The Blueprint may reference its dependency by alias in its manifests:

```yaml
# in chart/templates/deployment.yaml
env:
  - name: DATABASE_URL
    valueFrom:
      secretKeyRef:
        name: "{{ .Values.dependencies.db.connectionSecret }}"
        key: url
```

### 5.2 Conditional dependencies

```yaml
depends:
  - blueprint: bp-postgres
    when: "{{ .config.postgres.mode == 'embedded' }}"
    alias: db
```

Skipped at install time if the predicate is false. Useful when the user can choose "embedded backing service" vs "use existing".

### 5.3 Reference dependencies

The user can choose `external` mode and reference an existing Application:

```yaml
configSchema:
  postgres:
    oneOf:
      - properties:
          mode: { const: embedded }
      - properties:
          mode: { const: external }
          ref: { type: string }
```

When `mode: external`, the Blueprint's manifests resolve `ref` to a sibling Application in the same Environment, reads its connection details from the secret it exposes, and connects.

---

## 6. Placement and multi-region

`placementSchema` declares which Placement modes the Blueprint supports:

```yaml
placementSchema:
  modes: [single-region, active-active, active-hotstandby]
  minRegions: 1
  maxRegions: 5
```

For `active-active`, the Blueprint must be designed for it:
- Stateless services: trivial.
- Stateful: the Blueprint declares the replication strategy in its manifests (e.g. CNPG WAL streaming, SeaweedFS bucket replication, Valkey REPLICAOF).

Catalyst's projector uses the Placement spec to fan out manifests across the right vclusters at install time.

---

## 7. Manifests

Three accepted source types:

| `manifests.source.kind` | When to use |
|---|---|
| `HelmChart` | Most third-party apps with existing Helm charts. |
| `Kustomize` | Small custom apps; full control over patches and overlays. |
| `OAM` | (Future, not yet supported) — Open Application Model definitions. |

For Helm: `ref` points at an OCI artifact; Catalyst's Flux helm-controller fetches and renders.

For Kustomize: the Blueprint repo's `manifests/base/` is the base; each overlay in `manifests/overlays/<size>/` is a Kustomize component layered on top. Catalyst's Flux kustomize-controller renders.

---

## 8. Crossplane Compositions

If the Blueprint requires non-Kubernetes resources (cloud DBs, DNS records, S3 buckets, etc.), it includes Crossplane Compositions in `compositions/`.

```yaml
# compositions/postgres-database.yaml
apiVersion: apiextensions.crossplane.io/v1
kind: Composition
metadata:
  name: postgres-database.bp-wordpress
spec:
  compositeTypeRef:
    apiVersion: compose.openova.io/v1alpha1   # shared XRD group across Blueprints
    kind: PostgresDatabase
  resources:
    - name: hetzner-postgres-instance
      base:
        apiVersion: db.hcloud.crossplane.io/v1alpha1
        kind: PostgresInstance
        spec:
          forProvider:
            location: { from: spec.region }
            tier: { from: spec.tier }
```

Crossplane is **never user-facing**. End users see "needs a database" in the form, not Crossplane Compositions. Advanced users who write Compositions are typically:

- OpenOva engineers extending the public catalog.
- Sovereign-admins authoring private Compositions for their Sovereign.
- Corporate platform engineers contributing back upstream.

Compositions live in the Blueprint repo alongside the Helm chart / Kustomize manifests; CI signs and publishes them as part of the same OCI artifact.

---

## 9. Visibility

| Value | Where it appears | Who can install it |
|---|---|---|
| `listed` | Public marketplace card grid | Everyone in the Sovereign |
| `unlisted` | Not on cards; reachable by direct URL or search | Anyone who knows the Blueprint name |
| `private` | Visible only within the Org that owns the Blueprint repo | Only that Org's users |

Org-private Blueprints live in the Org's `shared-blueprints` Gitea repo, which only that Org's users have access to.

---

## 10. Versioning

- Semver (`MAJOR.MINOR.PATCH`).
- Each release publishes a signed OCI artifact at `ghcr.io/openova-io/bp-<name>:<version>` (where `<name>` is the folder name; the `bp-` prefix is added to the OCI artifact name to make it self-identifying as a Catalyst Blueprint).
- The Blueprint declares which prior versions are upgrade-compatible (`upgrades.from`).
- Customers pin to a version in their Application's `kustomization.yaml`. Upgrades are explicit (one-click in console, or a `git push` editing the version pin).

---

## 11. CI pipeline

Catalyst uses a **single monorepo CI** at the root of `github.com/openova-io/openova` (see §2 for the folder layout and path-matrix tag form). The same pipeline shape applies to every `platform/<name>/` and `products/<name>/` folder:

```yaml
# .github/workflows/blueprint-release.yaml (monorepo root)
on:
  pull_request:
    paths: ['platform/**', 'products/**']        # runs validate on PR
  push:
    tags:
      - 'platform/*/v*'                          # tag form: platform/<name>/v1.2.3
      - 'products/*/v*'                          #          products/<name>/v1.2.3

jobs:
  validate:                                      # runs on every PR touching a Blueprint folder
    - detect changed Blueprint folders (path-matrix)
    - for each: lint blueprint.yaml against the Blueprint CRD schema
                lint Helm chart / Kustomize base
                dry-run install in a kind cluster
                run tests/integration.yaml
                run tests/upgrade.yaml against the previous version

  build-and-sign:                                # runs only on tag push
    - parse the tag → identify which Blueprint folder + version
    - render that folder's Helm chart / Kustomize → OCI artifact
    - syft generate SBOM (per Blueprint)
    - cosign sign artifact + SBOM
    - push to ghcr.io/openova-io/bp-<folder-name>:<version>
    - publish blueprint.yaml as the OCI manifest's metadata layer
```

So tagging `platform/wordpress/v1.3.0` triggers a build of `platform/wordpress/`'s contents and publishes `ghcr.io/openova-io/bp-wordpress:1.3.0`. Other Blueprint folders are untouched. This is what "monorepo with per-Blueprint fan-out" means in practice.

Catalyst's `blueprint-controller` watches the GHCR catalog and registers new versions automatically — they appear in the marketplace within seconds of a successful release.

---

## 11.1 Umbrella shape (hard contract — CI-enforced)

**Every Blueprint chart at `platform/<name>/chart/` (and `products/<name>/chart/` for composite Blueprints) MUST be an *umbrella chart*: it MUST declare its upstream chart(s) under `dependencies:` in `Chart.yaml` so `helm dependency build` pulls the upstream payload into the published OCI artifact.**

Hollow charts — wrappers that carry only Catalyst overlay templates (NetworkPolicy, ClusterIssuer, ExternalSecret, ServiceMonitor) without an upstream subchart dependency — are **forbidden**. CI rejects them.

### Why

Earlier this cycle, `bp-cert-manager:1.0.0` shipped as a hollow chart: it carried only a `ClusterIssuer` template and **no upstream `cert-manager` subchart bytes**. Flux installed it on every Sovereign. Phase 1 broke on every Sovereign because cert-manager itself was never deployed — there was no controller, no CRDs, and the curated `ClusterIssuer` had nothing to register against. The artifact looked legitimate (right name, right version, signed, SBOM-attested) but the upstream payload was simply not there.

The fix is structural: the published OCI artifact's `<chart_name>/charts/` directory MUST contain the upstream chart at the version pinned by `Chart.yaml`'s `dependencies:` block.

### What CI enforces

`.github/workflows/blueprint-release.yaml` runs four hollow-chart guards on every publish:

| Stage | Guard | Failure mode caught |
|---|---|---|
| After `helm dependency build` | Working-tree `chart/charts/<dep>-<ver>.tgz` (or unpacked `chart/charts/<dep>/Chart.yaml`) exists for every `dependencies:` entry. | Missing/wrong repo URL, dependency-build silently skipped a dep. |
| After `helm package` | `tar -tzf` listing of the produced `.tgz` contains `<chart_name>/charts/<dep>-<ver>.tgz` (or unpacked) for every `dependencies:` entry. | `.helmignore` mishap, packaging-time stripping. |
| After `helm push` | `helm pull` round-trips the artifact from GHCR; the pulled `.tgz` listing again contains every declared subchart. | Registry-side path mangling, OCI manifest rewriting. |
| Always | `helm template` smoke render with default values produces non-trivial output; rendered manifests uploaded as workflow artifact for forensics. | Render-broken templates, schema violations, missing required values. |

**Any single guard failing fails the whole publish job.** A hollow Blueprint can never reach a Sovereign through the sanctioned CI path.

### Authoring rule

Every umbrella `Chart.yaml` declares the upstream chart(s) it wraps:

```yaml
# platform/cilium/chart/Chart.yaml
apiVersion: v2
name: bp-cilium
version: 1.1.0
type: application

# Upstream chart pulled in as a Helm subchart so `helm dependency build`
# bundles it into the OCI artifact. Pinned upstream version matches
# platform/cilium/blueprint.yaml + values.yaml's
# `catalystBlueprint.upstream.version`.
dependencies:
  - name: cilium
    version: "1.16.5"
    repository: "https://helm.cilium.io"
```

Catalyst-curated overlay templates (NetworkPolicy, ServiceMonitor, ClusterIssuer, ExternalSecret) live under `chart/templates/` alongside the dependency declaration. At install time Helm renders the upstream subchart **and** the Catalyst overlay — both ship from the same OCI artifact.

The version pinned in `dependencies:` MUST match the version recorded in `platform/<name>/blueprint.yaml` and the `catalystBlueprint.upstream.version` field in `values.yaml`. Operators bump all three together via PR + Blueprint release per Inviolable Principle #4 (no hardcoding).

Composite umbrellas (`products/catalyst/chart/`) follow the same rule: each leaf Blueprint they bundle is declared under `dependencies:`.

### Verifying an existing artifact

```bash
helm pull oci://ghcr.io/openova-io/bp-cilium --version 1.1.0
tar -tzf bp-cilium-1.1.0.tgz | grep '^bp-cilium/charts/cilium/' | head
```

A non-empty result proves the upstream subchart is inside the OCI artifact.

---

## 12. Authoring private Blueprints (in a customer Sovereign)

For corporate customers: the Org's platform team can author private Blueprints without involving OpenOva.

```
1. In the Catalyst console (Developer mode toggle on):
   Org context → Blueprint Studio → New Blueprint
2. Wizard offers two paths:
     a. Inherit from a public Blueprint (overlay path)
     b. Author from scratch (raw path)
3. Studio writes to gitea.<location-code>.<sovereign-domain>/<org>/shared-blueprints/bp-<name>.
4. On commit, CI runs (Gitea Actions inside the Sovereign).
5. blueprint-controller registers the new private Blueprint.
6. It appears in the Org's catalog as a private card.
```

Same flow works via direct git push to `shared-blueprints`. The console UI is convenience; Git is authoritative.

---

## 13. Contributing back to the public catalog

If an Org's private Blueprint would be useful to other customers, they can contribute it upstream:

```
1. Fork github.com/openova-io/openova
2. Add the Blueprint folder under platform/<name>/ or products/<name>/.
   Include blueprint.yaml + chart/ or manifests/ + (optional) compositions/ + tests/.
3. Open PR against main.
4. OpenOva engineers review for security, reusability, license, supply-chain (cosign,
   SBOM, dependency licenses, secret hygiene).
5. Merge → CI signs and publishes ghcr.io/openova-io/bp-<name>:<semver>.
6. blueprint-controller in every Sovereign's Catalyst picks it up on next mirror sync.
```

The contribution path applies equally to Crossplane Compositions, Helm charts, and full Blueprints. This is how the community grows the catalog.

---

## 14. Hard rules for Blueprint authors

| Rule | Why |
|---|---|
| All container images cosigned | Supply-chain security; Kyverno admission policy denies unsigned. |
| All artifacts SBOMed | Compliance (EU CRA, NIS2). |
| No plaintext secrets in chart values; use ExternalSecret references | See [`SECURITY.md`](SECURITY.md). |
| Workload identity via SPIFFE; no static service-account tokens | See [`SECURITY.md`](SECURITY.md) §2. |
| Health endpoints standardized: `/healthz` (liveness) + `/readyz` (readiness) | Catalyst observability assumes them. |
| Metrics on `/metrics` (Prometheus exposition) | Catalyst Grafana stack scrapes them. |
| Logs to stdout, structured JSON | Loki ingests them. |
| Traces via OTel | Tempo ingests them. |
| `app.kubernetes.io/*` labels set on every resource | Required for Catalyst projector to track. |
| Documentation in README.md, link from `card.documentation` | User clicks "Docs" on the card. |
| Acceptance tests in `tests/` | CI runs them on every PR. |
| Upgrade tests against previous version | Required to declare upgrade compatibility. |

---

*Cross-reference [`ARCHITECTURE.md`](ARCHITECTURE.md) for the runtime model and [`SECURITY.md`](SECURITY.md) for credential handling.*
