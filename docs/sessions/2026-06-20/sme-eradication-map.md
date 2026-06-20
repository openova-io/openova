# SME → Organization eradication map (Refs #3985 + Refs #3383)

**Date:** 2026-06-20
**Branch:** `fix/eradicate-sme-to-organization`
**Mandate (founder, verbatim intent):** *"We have already ABANDONED the concept of SME. It was replaced with **Organization**. If any single line of config is still talking about SME we revamp all of them."*

`SME` is a DEAD legacy term. The canonical replacement is **Organization** and its already-canonical derivatives: the per-Org runtime namespace is `org-services`, the controller is `organization`, the RBAC tier is `organization`/`owner`/`sovereign-admin`, the HTTP surface is `/api/v1/org/*` + `/api/v1/organizations`. Issue **#3383** already migrated the namespace (`sme` → `org-services`), the primary Secret (`sme-secrets` → `org-services-secrets`, dual-rendered one release), and added the canonical `/api/v1/org/*` routes with `/api/v1/sme/*` as deprecated aliases. **#3985 finishes the job.**

## Scope at session start

- **378 files** (code/config/charts, excluding `docs/archive` + `.git`) carry `sme`/`SME`.
- **~3,833 raw matches.**
- Top areas: `products/catalyst/bootstrap` (117), `products/catalyst/chart` (53), `clusters/_template/bootstrap-kit` (17), `core/services/{provisioning,shared,billing,notification,tenant,domain}`, `core/controllers/organization`, `platform/{stalwart-tenant,wordpress-tenant,postgres,keycloak,...}` charts, `products/sandbox/mcp-server`, e2e tests.

## Canonical replacement convention

| Old form | New form | Notes |
|---|---|---|
| `sme` / `SME` (word) | `org` / `Org` / `Organization` | by altitude (display→Organization, identifier→Org/org) |
| `sme-<x>` (k8s/dns/secret) | `org-<x>` | wire-level — lockstep |
| `sme_<x>` (db/sql/go-field) | `org_<x>` | wire-level — lockstep |
| `CATALYST_SME_*` (env) | `CATALYST_ORG_*` | wire-level — lockstep setter+reader |
| `sme.<x>.events` (NATS/Kafka topic) | `org.<x>.events` | wire-level — lockstep producer+consumer |
| `/api/v1/sme/*` (HTTP) | `/api/v1/org/*` / `/api/v1/organizations` | canonical already exists (#3383); drop legacy alias + flip FE |
| `HandleSME*`, `smeXxx`, `SMEXxx` (Go ident) | `HandleOrg*`, `orgXxx`, `OrgXxx` | pure-semantic |

---

# Bucket (a) — SEMANTIC (rename freely)

These do not cross a process/wire boundary. Renaming them cannot break a running platform; both producer and consumer are the *same* code unit recompiled together. **Rename all to Organization/Org/org by altitude.**

### a.1 — Go source file names (`sme_*.go`)
Rename file + the symbols inside; all callers are in the same Go module recompiled together.
- `products/catalyst/bootstrap/api/internal/handler/sme_billing_purchase.go` → `org_billing_purchase.go`
- `…/sme_billing_revenue.go`, `…/sme_billing_vouchers.go`, `…/sme_bss_overview.go`(+`_test`), `…/sme_catalog_client.go`(+`_test`), `…/sme_commerce.go`(+`_test`), `…/sme_consumption.go`(+`_test`), `…/sme_enter_org.go`(+`_test`), `…/sme_orders.go`, `…/sme_tenant_org_cr.go`(+`_test`), `…/sme_users.go`(+`_test`) → `org_*.go`
- `core/services/shared/auth/mint_sme.go`(+`_test`) → `mint_org.go`

### a.2 — Go identifiers (functions, types, vars, struct fields)
Pure in-binary identifiers, no wire meaning. Rename `SME`→`Org` / `sme`→`org`:
`HandleCreateSMEUser`, `HandleListSMEUsers`, `HandleDeleteSMEUser`, `HandleGetSMEBssOverview`, `HandleListSMEOrders`, `HandleGetSMEBillingRevenue`, `HandleListSMEBillingVouchers`, `HandleIssueSMEBillingVoucher`, `HandleRevokeSMEBillingVoucher`, `HandleSMEBillingPurchase`, `HandleSMECommerceList/Create/Update/Delete`, `smeCatalogClient`, `smeCatalog`, `smeDeps`/`SMEDeps`, `SMERoleFor`, `SMEUserUUID`/`smeUserUUID`, `smeUserResponse`, `smeDomain`, `smeCatalogProbeBudget`, `SMEKeycloakRealmName`, `SMETokenTTL`, `smeTenantID`, `SMESecretApplier`, `SMEKeycloakClient`, `SMEKeycloakAdminURL`, `smeJWTSecret`, `SetSMEJWTSecret`, `smeVouchersClient`, `smeVouchersBudget`, `SMEPoolParentDomains`, `smePoolCount`, `smePlanBreakdown`, `smeOrder`, `SMEKeycloakDirectClient`, `smeGatewayURL`, `smeSecret`, `smeRevenueKpi`, `smeUserSteps`, `mintSMEBridgeToken`, etc. (full grep `\b(SME|sme)[A-Za-z]+` set).

### a.3 — FE (TS/TSX) directory, file & symbol names
- Dir `products/catalyst/bootstrap/ui/src/pages/sme/` → `…/org/` (and `sme.api.ts` → `org.api.ts`, `CreateTenantPage.tsx` symbols).
- FE component/var/const names containing `SME`/`sme` (display strings, `createSMETenant`, `getConsumption`, etc.) — rename. **EXCEPTION:** the actual request path constants `SME_USERS_PATH`/`SME_TENANTS_PATH` are wire-level → see (b.6).

### a.4 — Comments / doc-strings / log messages / display text
Every comment, `slog`/`log` message, Chart.yaml release-note line, README, and user-facing string that says "SME"/"per-SME"/"SME-tier"/"SME mesh"/"SME services" → Organization/per-Org/Organization-tier/etc.

### a.5 — mcp-server (`products/sandbox/mcp-server`)
All `sme`/`SME` there are **comments only** (e.g. `renderSMETenantOverlay defence`, `every catalyst sme-service uses`). Rename text → Organization. (No wire identifier defined here; it *consumes* `catalyst-tenant` repo — see c.1.)

### a.6 — e2e / playwright test specs & fixtures (semantic parts)
File renames + in-test symbol/display renames:
- `products/catalyst/bootstrap/ui/e2e/sme-demo.spec.ts`, `sme-tier-rbac.spec.ts`, `sme-tenant-multi-domain.spec.ts`, `lib/sme-fixtures.ts`
- `.github/workflows/sme-demo-e2e.yaml`
- `core/marketplace/playwright/customer-journey.spec.ts`
- Their literal request paths / topic strings that ARE wire-level move in lockstep with (b).

### a.7 — Test data, fixtures, sample names (`sme-acme`, `sme-alice`, `sme-t-acme`, `sme-demo`, `sme-example`, `qa-sme`)
Sample/fixture identifiers used only inside tests → `org-acme`, `org-alice`, etc. (No live consumer.)

---

# Bucket (b) — WIRE-LEVEL coordinated (lockstep only)

Each is a contract across a process/serialization boundary. Renamed **only** with every referencing site in the same coherent change, or it stays in (c). This is fresh-prov-forward: a fresh prov CREATEs the new name and every consumer reads the new name.

## b.1 — NATS/Kafka logical topic names `sme.<x>.events`
**Definition:** `core/services/shared/events/topics.go` (constants `TopicUserEvents=…`, etc.). These are the in-process MultiPublisher routing key **and** the Kafka/Redpanda topic on the legacy Catalyst-Zero/contabo path. (The actual NATS JetStream subjects are independently `catalyst.*` via `CanonicalSubject` — NOT affected.)

| OLD | NEW | Definition | Producers (literals) | Consumers (literals/logs) |
|---|---|---|---|---|
| `sme.user.events` | `org.user.events` | topics.go:13 | — | notification/main.go (fan-in const) |
| `sme.order.events` | `org.order.events` | topics.go:18 | billing/handlers/handlers.go:1174 | provisioning/main.go |
| `sme.billing.events` | `org.billing.events` | topics.go:24 | — | notification/main.go |
| `sme.provision.events` | `org.provision.events` | topics.go:30 | — | tenant/main.go:166; tenant/handlers/consumer.go:58 |
| `sme.tenant.events` | `org.tenant.events` | topics.go:36 | tenant/handlers/apps.go:198,325; tenant/handlers/handlers.go:275,309,464,729 | provisioning/main.go; billing/main.go:157; domain/main.go:106; tenant/main.go:207 |
| `sme.domain.events` | `org.domain.events` | topics.go:41 | — | notification/main.go |
| `sme.dlq` | `org.dlq` | topics.go:45 | DLQSubscriber | notification/handlers/consumer.go:15 |

Plus the durable consumer constant `ConsumerSMEBillingMetering` (`core/services/shared/events/nats.go:59`, value `sme-billing-metering`) → `org-billing-metering`, used in `billing/main.go:106,218` + `billing/handlers/metering_consumer.go`.
Plus YAML literal `sme.user.created` in `platform/stalwart-tenant/chart/values.yaml:363` → `org.user.created` (Stalwart webhook → NATS subscriber — lockstep with subscriber).

**Lockstep set:** `topics.go`, `nats.go`, `dlq_test.go`, `bridge.go`/`bridge_subscriber.go` (comments), `tenant/{main.go,handlers/{apps.go,handlers.go,consumer.go,members_consumer.go}}`, `provisioning/{main.go,handlers/consumer.go}`, `billing/{main.go,handlers/{handlers.go,metering_consumer.go,metering_consumer_test.go}}`, `domain/{main.go,handlers/consumer.go}`, `notification/{main.go,handlers/consumer.go}`, `shared/events/dlq_test.go`, `platform/stalwart-tenant/chart/values.yaml`.
The `LegacyTopics` fan-in mechanism (topics.go:48) is exactly the bridge for a publisher-side rename; on fresh-prov it is irrelevant (no old data). **Risk:** a durable NATS consumer rename orphans the old durable on a *live* cluster — fresh-prov-forward so new provs are clean; flagged for operator if a live env is upgraded in place.

## b.2 — Environment variables `CATALYST_SME_*`
Contract between chart `env:`/values setter and Go `os.Getenv` reader. Rename `CATALYST_SME_X`→`CATALYST_ORG_X`.

| OLD | NEW | Setter | Reader |
|---|---|---|---|
| `CATALYST_SME_JWT_SECRET` | `CATALYST_ORG_JWT_SECRET` | chart `api-deployment.yaml:793`, `api-deployment-kustomize.yaml:726` | `bootstrap/api/cmd/api/main.go:583` |
| `CATALYST_SME_POOL_DOMAINS` | `CATALYST_ORG_POOL_DOMAINS` | (no chart setter; tests `t.Setenv`) | `sovereign_parent_domains.go:56` + tests |
| `CATALYST_SME_KC_SA_TOKEN` | `CATALYST_ORG_KC_SA_TOKEN` | (external/secret) | `main.go:857,921` |
| `CATALYST_SME_GATEWAY_URL` | `CATALYST_ORG_GATEWAY_URL` | (code default) | `sme_billing_vouchers.go:96,113` |
| `CATALYST_SME_BP_{WORDPRESS,STALWART,OPENCLAW,NEWAPI,KEYCLOAK,CNPG}_VER` | `CATALYST_ORG_BP_*_VER` | (code default) | `main.go:898-903` |
| `CATALYST_SME_NEWAPI_BASE_URL_TEMPLATE` | `CATALYST_ORG_NEWAPI_BASE_URL_TEMPLATE` | (code default) | `main.go:828` |
| `CATALYST_SME_KC_DIRECT_PROVISION` | `CATALYST_ORG_KC_DIRECT_PROVISION` | (none — comment) | `organization_keycloak.go` |
| `CATALYST_SME_CATALOG_URL` | `CATALYST_ORG_CATALOG_URL` | configmap.yaml | `sme_catalog_client.go:32,98` |

**Lockstep set:** `products/catalyst/chart/templates/{api-deployment.yaml,api-deployment-kustomize.yaml}`, `products/catalyst/chart/templates/org-services/configmap.yaml`, `bootstrap/api/cmd/api/main.go`, `…/handler/{sovereign_parent_domains.go,sme_billing_vouchers.go,sme_catalog_client.go,organization_keycloak.go}`, related `_test.go` `t.Setenv` sites. (Variable *values* contain no embedded "sme" — safe.)

## b.3 — HTTP API routes `/api/v1/sme/*` (canonical already exists)
`#3383` already added canonical `/api/v1/org/*` + `/api/v1/organizations` and kept `/api/v1/sme/*` as `deprecatedAlias` routes (`Sunset: Wed, 01 Jul 2026`). The FE still calls the legacy paths.

**Lockstep move:**
1. FE `products/catalyst/bootstrap/ui/src/pages/sme/sme.api.ts:62,207` — flip `SME_USERS_PATH='/v1/sme/users'`→`'/v1/org/users'`, `SME_TENANTS_PATH='/v1/sme/tenants'`→`'/v1/organizations'`; audit `commerce.api.ts`, `organizations.api.ts` for any literal `/api/v1/sme/*`.
2. BE `bootstrap/api/cmd/api/main.go:1670-1831` — DROP the `/api/v1/sme/*` `deprecatedAlias` registrations once FE is flipped (the canonical `/api/v1/org/*` handlers stay). Handler func names → (a.2).

**Risk:** FE+BE must flip together; any external caller of `/api/v1/sme/*` (none in-repo) loses the alias — acceptable per the Sunset date.

## b.4 — k8s ConfigMap `sme-services-config`
Single ConfigMap consumed by 8 microservice Deployments via `envFrom.configMapRef`. Currently single-named (NOT yet aliased). Rename `sme-services-config`→`org-services-config`.
- **Creator:** `products/catalyst/chart/templates/org-services/configmap.yaml:151`
- **Consumers (47 refs):** `org-services/{catalog,gateway,auth,organization,billing,domain,notification,provisioning}.yaml` (`envFrom.configMapRef.name`).
**Risk:** half-rename → every org-service Pod CrashLoops on missing ConfigMap. Lockstep mandatory.

## b.5 — k8s Secret `org-services-secrets` (legacy `sme-secrets`) — #3383 already migrated
Canonical `org-services-secrets` already in use; legacy `sme-secrets` dual-rendered one release behind `.Values.compat.smeSecretsAlias` (default `true`). **Action for #3985:** flip the compat default to `false` (fresh provs stop emitting `sme-secrets`) and update the remaining `sme-secrets` comment/lookup text. Keep the *rendering branch code* for one-release in-place-upgrade safety (see c.2). Touch: `org-services/org-services-secrets.yaml`, `api-deployment{,-kustomize}.yaml`, `core/services/shared/auth/mint_*.go`, `billing/handlers/{handlers.go,vouchers.go}`.

## b.6 — Domain-pool role string `"sme-pool"`
Literal compared with `==`/`EqualFold`. Rename value `"sme-pool"`→`"org-pool"`.
- **Writers/constants:** `provisioner.go:145 (ParentDomainRoleSMEPool)`, `parent_domains.go:106 (RoleSMEPool)`, `sovereign_parent_domains.go:73-76,86`, `organization_provisioning.go:185,1092`, CRD `crds/sovereign.yaml:193 (enum)`.
- **Readers:** `sovereign_parent_domains.go:92`, `organization_provisioning.go:190,1068`, `external-dns`/`powerdns` chart docs, all `*_test.go` fixtures, UI `parent-domains/*.tsx,*.ts`.
**Risk:** CRD enum + producer + consumer + UI must move together or domain allocation silently fails validation.

## b.7 — GitOps repo path / Flux Kustomization `sme-tenants`
Git directory path `clusters/<fqdn>/sme-tenants/<orgID>/` (written by catalyst-api, read by Flux), Flux Kustomization/GitRepository default name, gitguard allow-list, cutover egress-block test. Rename `sme-tenants`→`org-tenants`.
- **Writers:** `organization_gitops.go:124,148,158,162,166,409,413,423-431`; `provisioning/github/client.go` (branch); `gitguard/gitguard.go:46` (allow-pattern).
- **Readers:** `org-services/org-repo-kustomization.yaml`, `org-repo-gitrepository.yaml:107` (branch default), `provisioning.yaml:368,490` (`CATALYST_GITOPS_BASE_PATH`/`_BRANCH`), `gitea-org-repo-bootstrap-job.yaml`, `self-sovereign-cutover/chart/{values.yaml:593,templates/08-egress-block-test-job.yaml:637}`, `values.yaml:1240` (`smeServices.gitOps.name`).
**Risk:** path/branch/Kustomization-name must all match or Flux never reconciles per-Org overlays. Lockstep mandatory. Also rename `.Values.smeServices.*` key → `.Values.orgServices.*` (chart-internal, all in this chart).

## b.8 — CNPG postgres cluster + DB + owner + generated secrets/services
Value-driven (`smePostgres.cluster.{name,database,owner,additionalDatabases}`). Fresh-prov CREATE-time names. Rename in lockstep:

| OLD | NEW | Creator | Consumers |
|---|---|---|---|
| Cluster `sme-pg` | `org-pg` | `org-services/cnpg-cluster.yaml:77` | values.yaml:1438, ferretdb.yaml:58, configmap.yaml:192 |
| Service DNS `sme-pg-rw` | `org-pg-rw` | CNPG-derived | ferretdb.yaml:58, configmap.yaml:191 |
| Secret `sme-pg-app` | `org-pg-app` | CNPG-derived | auth.yaml:36,41; billing.yaml; ferretdb.yaml:62 |
| DB `sme_auth` | `org_auth` | cnpg-cluster.yaml:100 | values.yaml:1449; auth.yaml:54 conn-str; 16d-bp-postgres-shared-c.yaml:126 |
| DB `sme_billing` | `org_billing` | cnpg-cluster.yaml:111 | values.yaml:1455; 16d:150 |
| DB `sme_documents` | `org_documents` | cnpg-cluster.yaml:111 | values.yaml:1591; ferretdb.yaml:60; 16d:155 |
| Owner role `sme` | `org` | cnpg-cluster.yaml:101 | values.yaml:1450; OWNER clauses:112; 16d:127,151,156 |

Plus the shared-pg-c path (`16d-bp-postgres-shared-c.yaml`) `sme-database-secret`→`org-database-secret`, `sme-valkey-auth`→`org-valkey-auth` (`valkey-cross-ns-secret.yaml` creator + `gateway.yaml:50`/`auth.yaml:71` readers), and Flux git-auth secret `openova-sme-tenants-git-auth`→`openova-org-tenants-git-auth` (`gitea-flux-auth-secrets-sync-job.yaml:72` + `values.yaml:682` + `org-repo-gitrepo-auth-secret.yaml:74`).
**Note:** chart comment at `values.yaml:1438` *currently* says "#3383 keeps the CNPG cluster + secret names" — that wire-stable decision is **superseded by #3985** (founder: revamp every line). Renamed lockstep, fresh-prov-forward; flagged for in-place upgrades (the old PVC/DB would orphan — fresh prov only).

## b.9 — Keycloak groups, realm pattern, tier string
- Groups `sme-admins`/`sme-users` (`platform/keycloak/chart/values.yaml:235-236`) → `org-admins`/`org-users`. JWT `groups` claim contract — must match any app-side `bound_claims`/role-map that references them.
- Per-tenant realm name pattern `sme-<sub>` (`keycloak/chart/values.yaml:215-236`, `stalwart-tenant/chart/Chart.yaml`, `wordpress-tenant/{blueprint.yaml,chart/values.yaml:277}`) → `org-<sub>` — realm is the OIDC issuer; consumer clients (wordpress/stalwart OIDC config) must move in lockstep.
- Org spec `tier = "sme"` (`sme_tenant_org_cr.go:146`, `organization_provisioning.go` comment, `organization_orgshape_test.go:20`) → `tier = "org"` — CRD spec field value; producer+any RBAC reader lockstep.
**Risk:** group/realm rename without app-side claim/issuer update → login succeeds but RBAC denies, or OIDC discovery 404. Lockstep mandatory; if any consumer cannot be located, leave that one identifier in (c).

## b.10 — `images.smeTag` chart value + deploy-bot
`.github/workflows/services-build.yaml` auto-bumps `images.smeTag` in `products/catalyst/chart/values.yaml`; the 8 `org-services/*.yaml` templates render `{{ .Values.images.smeTag }}`. Rename `smeTag`→`orgTag` (or `servicesTag`) in lockstep: values.yaml field + all 8 template refs + the workflow's `grep`/`sed` matcher at `services-build.yaml:146,150,190`.
**Risk:** half-rename breaks the deploy-bot auto-bump → org-service images never update. Lockstep mandatory.

---

# Bucket (c) — GENUINELY-MUST-NOT-TOUCH (small, defended)

| Identifier | Where | Why it stays |
|---|---|---|
| **c.1 — `catalyst-tenant` per-Org Gitea repo name** | `org-controller` (`per_org_flux.go`), `mcp-server/internal/tools/{sandbox_deploy.go,registry.go}`, per-Org Flux `GitRepository catalyst-tenant-<slug>` + Kustomization, chart docs | This is the `tenant`-sibling, NOT `sme`. It is a deeply-wired live Gitea repo name spanning org-controller (creator) + mcp-server (writer) + per-Org host Flux (consumer). Renaming is a **separate full-lockstep effort** that also touches the in-flight OpenOva-MCP work. The #3985 grep target is `sme`, not `tenant`; renaming `catalyst-tenant` here would be a half-finished `tenant` rename across two products → exactly the half-rename the issue forbids. **Pipelined as a follow-on `tenant`-eradication ticket.** Not matched by the #3985 `\bsme\b` grep anyway. |
| **c.2 — legacy `sme-secrets` dual-render *branch* (not default)** | `org-services/org-services-secrets.yaml` `compat.smeSecretsAlias` guard + `lookup "v1" "Secret" "sme" "sme-secrets"` | The #3383 one-release in-place-upgrade bridge. We flip the **default to false** (b.5) so fresh provs emit only `org-services-secrets`, but keep the guarded render branch + its `sme-secrets` string literal for one release so a *live* env upgraded in place doesn't lose the mirrored JWT secret mid-roll. Removing it entirely is a separate post-Sunset cleanup. The literal is behind a default-off flag → effectively dead on fresh provs. |
| **c.3 — `platform/{stalwart,wordpress}-tenant` chart dir + OCI name `bp-*-tenant`** | `Chart.yaml: name: bp-stalwart-tenant` / `bp-wordpress-tenant`, every Sovereign pin, catalog alias, deploy-bot | `tenant`-sibling (not `sme`). These are **published OCI artifact names** (`ghcr.io/openova-io/bp-wordpress-tenant`) pinned by every Sovereign + freshly seeded `0.4.1` (commit db664f13f) + catalog aliases. Renaming the chart name is a catalog-wide OCI migration with its own release train — out of #3985 `sme` scope, pipelined as a `tenant`-eradication ticket. Not matched by `\bsme\b`. |
| **c.4 — historical `docs/sessions/**` evidence + ADR text** | dated session dirs, ADR-0001/0003 prose | Immutable historical record. Editing rewrites history. Out of scope (the grep excludes `docs/archive`; live `docs/` prose referencing the *former* SME concept stays as historical narrative, not config). |

**(c) is intentionally tiny:** every item is either a `tenant`-sibling wire identifier that needs its OWN full-lockstep ticket (renaming it half-way inside #3985 would itself violate the no-half-rename rule), an in-place-upgrade safety bridge behind a default-off flag, or immutable history. **No `sme`-named *config/code* identifier is defended** — all `sme`/`SME` in `.go/.ts/.tsx/.yaml/.tf` is in (a) or (b).

---

# Execution order (to keep `go build`/`helm template` green at every commit)

1. **(a)** semantic bulk per package — comments, log strings, Go/TS identifiers, file renames. Build after each package.
2. **(b.1)** NATS topics (self-contained in `core/services`). `go build ./core/... && go test ./core/services/shared/events/...`.
3. **(b.2)** env vars (chart setter + Go reader together).
4. **(b.6)** `sme-pool` role (constant + CRD enum + consumers + tests together).
5. **(b.4)** `sme-services-config` ConfigMap (creator + 47 consumers together) → `helm template`.
6. **(b.7)** GitOps path + `smeServices.*` values key + Flux + gitguard + cutover test.
7. **(b.8)** CNPG cluster/DB/owner/secrets (value defaults + templates + 16d + conn-strings).
8. **(b.9)** Keycloak groups/realm/tier (KC chart + app-side OIDC consumers).
9. **(b.10)** `images.smeTag`→`orgTag` (values + 8 templates + workflow matcher).
10. **(b.3)** HTTP routes: flip FE path constants, drop BE `/api/v1/sme/*` deprecated aliases.
11. **(b.5)** flip `compat.smeSecretsAlias` default → false; tidy remaining `sme-secrets` text.

# Self-proof gates (run at the end)
- `cd core && go build ./... && go vet ./...` = 0; `cd products/catalyst/bootstrap/api && go build ./... && go vet ./...` = 0.
- `go test` on every touched package = green.
- `cd products/catalyst/bootstrap/ui && npx tsc -b && npm run build` = 0; same for `core/console`, `core/admin`, `core/marketplace` if touched.
- `helm template products/catalyst/chart` + each touched `platform/*/chart` = no error.
- `scripts/lint-cloudinit.sh both` if any cloud-init touched.
- `grep -riE '\bsme\b|sme[-_]|"sme"' --include=*.go --include=*.ts --include=*.tsx --include=*.yaml --include=*.tf` over code/config/charts (excl `docs/archive` + `.git`) = **0 except the (c) set** (`catalyst-tenant`, default-off `sme-secrets` bridge, `bp-*-tenant` — none of which match `\bsme\b` except the bridge literal).

**Final acceptance is the coordinator's LIVE UAT walk on a fresh prov** (funnel runs end-to-end; no "SME"/"Tenant" string in the console; per-Org console + RBAC intact). Build-green is NOT acceptance.
