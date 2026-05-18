templates/catalog-seed/ — baseline Blueprint CRs always rendered on every
Sovereign so /api/v1/catalog returns a non-empty items[] from the moment
catalyst-api comes up at handover-time. (WBS row TBD-E8, C4-015,
fix(catalog): seed published Blueprints on Sovereign handover.)

Background
──────────
catalyst-catalog reads Blueprints from THREE sources in priority order:

  (a) Public catalog mirror     (`catalog`           Gitea Org)
  (b) Sovereign-curated mirror  (`catalog-sovereign` Gitea Org)
  (c) Per-Org private repo      (`<org>/shared-blueprints` Gitea Org)

On a freshly-handed-over Sovereign NONE of those Gitea Orgs exist —
self-sovereign-cutover step-01 only mirrors `openova-io/openova` into
`openova/openova`, NOT the catalog Orgs (cf.
platform/self-sovereign-cutover/chart/templates/01-gitea-mirror-job.yaml).
The operator has to manually publish each Blueprint via the
`/blueprints/publish` admin endpoint or via the SovereignConsole's
PublishPage. Until they do that, every catalog read returns
`items: []` and the Apps grid is empty (test C4-015 FAIL on t22).

The chained catalog client (qa-loop iter-8 Fix #40,
products/catalyst/bootstrap/api/internal/handler/catalog_client_cluster_fallback.go)
already falls back to in-cluster `blueprints.catalyst.openova.io` CRs
when the upstream Gitea-backed sources return nothing. The qa-fixtures
chart leverages this — but qa-fixtures defaults OFF on production
Sovereigns, so its Blueprint CRs never render.

Fix
───
Always-render a small set of baseline Blueprint CRs pointing at REAL
charts that the blueprint-release.yaml workflow already publishes to
GHCR. The chained catalog client picks them up via the cluster fallback
path; UI grid is non-empty from t=0; operator can later override via
`/blueprints/publish` (org-private wins on collision per source.go's
priority order, so a curated override is non-destructive).

Per docs/INVIOLABLE-PRINCIPLES.md #1 (target-state, not MVP) every
Blueprint here references a REAL OCI artifact + a REAL configSchema
mirroring what the upstream `<chart>/blueprint.yaml` declares. No
placeholder charts, no fake configSchemas.

Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode) the entire seed
set is gated on `.Values.catalogSeed.enabled` (default true). An
operator who curates their own catalog from day-zero can disable the
seed in their per-Sovereign overlay.

Per docs/INVIOLABLE-PRINCIPLES.md #5 (no silent compromises) Blueprint
CRs are labeled with `catalyst.openova.io/managed-by: catalog-seed` so
the operator can distinguish chart-seeded entries from
Gitea-mirrored entries in `kubectl get blueprints` output.

Files
─────
  blueprint-bp-wordpress-tenant.yaml   — Canonical SME-tenant WordPress
                                          (mirrors platform/wordpress-tenant/blueprint.yaml)
  blueprint-bp-grafana.yaml             — Tenant observability dashboard
  blueprint-bp-prometheus.yaml          — Tenant metrics scraping
  blueprint-bp-keycloak.yaml            — Per-tenant Identity Provider
  blueprint-bp-cnpg.yaml                — CloudNative-PG Postgres operator
  blueprint-bp-redis.yaml               — In-memory key/value store
  blueprint-bp-clickhouse.yaml          — Column-oriented analytics DB
  blueprint-bp-opensearch.yaml          — Search + log analytics
  blueprint-bp-loki.yaml                — Log aggregation
  blueprint-bp-temporal.yaml            — Workflow orchestration
  blueprint-bp-n8n.yaml                 — Low-code automation
  blueprint-bp-langfuse.yaml            — LLM observability
  blueprint-bp-llm-gateway.yaml         — LLM routing + cost guard

Operator override
─────────────────
To replace seeded entries with curated Blueprints, push to the
`catalog-sovereign` Gitea Org. Per source/resolver.go priority order
(PRIVATE > SOVEREIGN > PUBLIC) the curated entry wins on name collision
and the in-cluster seeded CR is shadowed for catalog list/get calls.
The in-cluster CR remains in place as a fallback should the curated
entry be removed; the operator can delete it explicitly via
`kubectl delete blueprint bp-<name>` once their curated set is stable.
