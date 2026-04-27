# Blueprint Authoring

**Status:** Authoritative | **Updated:** 2026-04-27

How to author a **Blueprint** for Catalyst — the unified unit of installable software (replaces what was previously called "module" + "template"). Defer to [`GLOSSARY.md`](GLOSSARY.md) for terminology and [`ARCHITECTURE.md`](ARCHITECTURE.md) for the broader model.

---

## 1. What a Blueprint is

A Blueprint is:

- A **Git repository** (`bp-<name>` under `github.com/openova` for public, or under `<sovereign>/<org>/shared-blueprints` for Org-private).
- A **CRD manifest** (`blueprint.yaml`) declaring its identity, configSchema, placementSchema, dependencies, and pointers to its manifests.
- A **set of manifests** (Helm chart, Kustomize base + overlays, or raw YAML) that get applied when the Blueprint is installed as an Application.
- A **set of Crossplane Compositions** (optional) for any non-Kubernetes resources the Blueprint provisions.
- A **CI pipeline** that signs the artifact (cosign), generates an SBOM (Syft), publishes to OCI registry (`ghcr.io/openova/<name>:<semver>`), and tags a release.

One Blueprint = one card in the marketplace (when `visibility: listed`).

---

## 2. Repository layout

```
bp-<name>/
├── blueprint.yaml             ← the Blueprint CRD manifest
├── README.md                  ← what it does, links to docs
├── chart/                     ← Helm chart (preferred for typical apps)
│   ├── Chart.yaml
│   ├── values.yaml
│   └── templates/
│   OR
├── manifests/                 ← Kustomize base
│   ├── base/
│   │   ├── kustomization.yaml
│   │   ├── deployment.yaml
│   │   ├── service.yaml
│   │   └── ingress.yaml
│   └── overlays/
│       ├── small/
│       ├── medium/
│       └── large/
├── compositions/              ← (optional) Crossplane Compositions
│   ├── postgres-database.yaml
│   └── object-storage-bucket.yaml
├── card/                      ← marketplace presentation
│   ├── icon.svg
│   ├── screenshots/
│   └── description.md
├── tests/                     ← acceptance tests
│   ├── integration.yaml       ← Litmus probe / Catalyst test harness
│   └── upgrade.yaml
└── .github/workflows/         ← CI
    ├── validate.yaml
    ├── test.yaml
    └── release.yaml
```

---

## 3. The Blueprint CRD

Annotated example for `bp-wordpress`:

```yaml
apiVersion: catalyst.openova.io/v1
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
      ref: oci://ghcr.io/openova/bp-wordpress:1.3.0
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
- Stateful: the Blueprint declares the replication strategy in its manifests (e.g. CNPG WAL streaming, MinIO bucket replication, Valkey REPLICAOF).

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
    apiVersion: bp-wordpress.openova.io/v1alpha1
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
- Each release publishes a signed OCI artifact at `ghcr.io/openova/<name>:<version>`.
- The Blueprint declares which prior versions are upgrade-compatible (`upgrades.from`).
- Customers pin to a version in their Application's `kustomization.yaml`. Upgrades are explicit (one-click in console, or a `git push` editing the version pin).

---

## 11. CI pipeline

Every Blueprint repo's CI does:

```yaml
on: push                           # branch: main
                                   # tags: vX.Y.Z

jobs:
  validate:
    - lint blueprint.yaml against the Blueprint CRD schema
    - lint Helm chart / Kustomize base
    - dry-run install in a kind cluster
    - run tests/integration.yaml
    - run tests/upgrade.yaml against the previous version

  build-and-sign:                   # only on tags
    - render Helm chart / Kustomize → OCI artifact
    - syft generate SBOM
    - cosign sign artifact + SBOM
    - push to ghcr.io/openova/<name>:<tag>
    - publish blueprint.yaml as the manifest
```

Catalyst's `blueprint-controller` watches the GHCR catalog and registers new versions automatically — they appear in the marketplace within seconds of a successful release.

---

## 12. Authoring private Blueprints (in a customer Sovereign)

For corporate customers: the Org's platform team can author private Blueprints without involving OpenOva.

```
1. In the Catalyst console (Developer mode toggle on):
   Org context → Blueprint Studio → New Blueprint
2. Wizard offers two paths:
     a. Inherit from a public Blueprint (overlay path)
     b. Author from scratch (raw path)
3. Studio writes to gitea.<sovereign-domain>/<org>/shared-blueprints/bp-<name>.
4. On commit, CI runs (Gitea Actions inside the Sovereign).
5. blueprint-controller registers the new private Blueprint.
6. It appears in the Org's catalog as a private card.
```

Same flow works via direct git push to `shared-blueprints`. The console UI is convenience; Git is authoritative.

---

## 13. Contributing back to the public catalog

If an Org's private Blueprint would be useful to other customers, they can contribute it upstream:

```
1. Fork github.com/openova/bp-<name>-template
2. Apply their Blueprint, signing key transferred to OpenOva or kept by the contributor.
3. Open PR.
4. OpenOva engineers review for security, reusability, license, supply-chain.
5. Merge → publish to ghcr.io/openova/<name>.
6. Now appears in every Sovereign's mirrored public catalog.
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
