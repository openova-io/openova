## #3370 CONTEXTS

**What #3370 delivers:** ONE generic platform-wide mechanism for multi-application reusability. Every Blueprint declares `shareable: true|false` + a `contextSchema` (`platform/postgres/blueprint.yaml:38-53`, `platform/valkey/blueprint.yaml:30-45`); each shareable instance exposes its **Contexts** — the child entity a consumer occupies (postgres: a `db`; valkey: a `keyspace`). The catalog card renders a shareable badge, the `/apps` tile shows `⛓ N contexts`, the instance page gains a **Contexts** tab, consumers show `Depends on: <instance> / <kind>:<ctx>`. Three shared-postgres instances self-register a bootstrap-owned `Application` CR the controller ADOPTS status-only (zero duplicate HRs). **The walk targets the operator console at `products/catalyst/bootstrap/ui/` (React/TanStack Router), NOT the SME tenant console at `core/console/`.**

| # | Go to (URL/route) | Do (click/type — EXACT label) | Expect (EXACT screen/text/count) | Source (file:line) | ☐ |
|---|---|---|---|---|---|
| **DoD #1 — shareable badge on catalog cards** | | | | | |
| 1 | `/apps` | Click the **Catalog** tab (testid `sov-tab-catalog`) | Card grid, one card per Blueprint CLASS | `AppsPage.tsx:612-621` | ☐ |
| 2 | `/apps` Catalog | Locate **PostgreSQL** card | Card carries `⛓ shareable` chip (`sov-catalog-shareable-bp-postgres`) | `AppsPage.tsx:940-948,859` | ☐ |
| 3 | `/apps` Catalog | Locate **Keycloak/Gitea** card | NO `⛓ shareable` chip | `AppsPage.tsx:859,940` | ☐ |
| 4 | `/catalog/bp-postgres` | Observe hero meta-chips | Green chip `⛓ shareable · db` (`badge-shareable`) | `CatalogDetail.tsx:229-237,155-159` | ☐ |
| 6 | terminal | `kubectl get blueprint bp-postgres -o jsonpath='{.spec.shareable}{" "}{.spec.contextSchema.kind}'` | `true db` | `platform/postgres/blueprint.yaml:38,40-46` | ☐ |
| **DoD #2 — one Application CR per instance; zero duplicate installs (adoption)** | | | | | |
| 7 | terminal | `kubectl get applications.apps.openova.io -A` | ≥3 rows: `shared-pg{,-b,-c}` in `shared-data` (was 0 pre-#3370) | `platform/postgres/chart/templates/application-cr.yaml:46-99` | ☐ |
| 8 | terminal | `kubectl get application shared-pg -n shared-data -o jsonpath='{.spec.bootstrap}{" "}{.metadata.labels.apps\.openova\.io/bootstrap-owned}'` | `true true` | `application-cr.yaml:54,57` | ☐ |
| 10 | terminal BEFORE→AFTER | Count `kubectl get hr -A`, reconcile the 3 bootstrap CRs, re-count | HR count UNCHANGED — adoption renders NO new HR | adoption guard `application_controller.go:611-613,1922-1925,1933-2011` | ☐ |
| 11 | terminal | `kubectl get application shared-pg -n shared-data -o jsonpath='{.status.phase}{" "}{.status.conditions[?(@.reason=="BootstrapAdopted")].reason}'` | `Ready BootstrapAdopted` | `application_controller.go:1970-1994,189` | ☐ |
| **DoD #3 — /apps shows 3 postgres instance cards with ⛓ N contexts** | | | | | |
| 13 | `/apps` Deployments | Locate **shared-pg** card | Topology chip + `⛓ 3 contexts` badge (`sov-app-contexts-shared-pg`) | `AppsPage.tsx:679-716,913-939` | ☐ |
| 14 | `/apps` Deployments | Locate **shared-pg-b**, **shared-pg-c** | Each with topology chip + `⛓ 3 contexts` | `AppsPage.tsx:679-716` | ☐ |
| 15 | `/apps` Deployments | Confirm no blueprint-level `bp-postgres` card | N instances ⇒ exactly N cards | `AppsPage.tsx:718-726` | ☐ |
| 16 | backend | `curl .../api/v1/sovereign/apps \| jq '.apps[] \| select(.instance==true)'` | 3 objects, each `contextCount: 3` | `sovereign.go:585,742-788,755-759` | ☐ |
| **DoD #4 — /catalog/bp-postgres: topologies + New instance + 3 instance lines; panel GONE** | | | | | |
| 18 | `/catalog/bp-postgres` | **Supported topologies** | `singleton` + `active-hot-standby`; singleton `default` chip | `CatalogDetail.tsx:370-404`; `blueprint.yaml:162-168` | ☐ |
| 19 | `/catalog/bp-postgres` | **Instances** header | `+ New instance` button (`btn-new-instance`) | `InstancesSection.tsx:84-104` | ☐ |
| 20 | `/catalog/bp-postgres` | Instances table (`sov-instances-table`) | EXACTLY 3 rows: shared-pg{,-b,-c} | `InstancesSection.tsx:154-235`; `endpoint_handler.go:1094-1161` | ☐ |
| 22 | `/catalog/bp-postgres` | Confirm NO "Data instances" panel | **GAP — DataInstances.svelte NOT deleted; only the SME console (`core/console`) carries it (`AppsPage.svelte:9,443-445`). Row FAILS strict DoD §2.8.** | operator console renders none | ☐ |
| **DoD #5 — /app/shared-pg Contexts tab + consumer Depends-on** | | | | | |
| 24 | `/app/shared-pg` | Observe tab strip | **Contexts** tab, count `3` (gated on shareable) | `AppDetail.tsx:650-658,222-231` | ☐ |
| 25 | `/app/shared-pg` | Click **Contexts** | Table `sov-contexts-table`: Context \| Occupied by \| Credential \| Status | `AppDetail.tsx:709-712`; `ContextsTab.tsx:42-55` | ☐ |
| 26 | `/app/shared-pg` Contexts | Read rows | `db/gitea·gitea→·gitea-database-secret·ready`; `db/registry·harbor→`; `db/keycloak·keycloak→` | `ContextsTab.tsx:57-104`; `16a-bp-postgres-shared.yaml:173-204` | ☐ |
| 29 | `/app/bp-gitea` Dependencies tab | Read line | `Depends on: shared-pg / db:gitea` (`sov-app-dependson-shared-pg`) | `AppDetail.tsx:783-799`; `applications.go:1280-1293` | ☐ |
| **DoD #6 — provisioning walk: DEFAULT auto-creates backing; ADVANCED reuse ⇒ new Context** | | | | | |
| 35 | New-instance dialog (DEFAULT) | Leave **Create new (default)**; **Create instance** | POST `backing:[{mode:'create'}]`; new consumer + new backing card both appear | `InstancesSection.tsx:482-511`; `endpoint_handler.go:976-1020` | ☐ |
| 38-39 | New-instance dialog (REUSE) | **Reuse existing** → pick a console-created pg → Create | Only ONE new card; a new Context row appears on the reused instance | `endpoint_handler.go:1022-1066`; `ContextsTab.tsx:57-104` | ☐ |
| 41 | safety | Attempt reuse of a **bootstrap-owned** instance via API | Rejected `backing-bootstrap-owned`. **GAP — reuse of shared-pg/-b/-c blocked in-console; demo must use a console-created instance.** | `endpoint_handler.go:1031-1037` | ☐ |
| **DoD #7 — generality: SAME mechanism on valkey (non-DB)** | | | | | |
| 42-44 | terminal + `/catalog/bp-valkey` | `kubectl get blueprint bp-valkey ...`; open Contexts on a valkey instance | `true keyspace`; SAME table, rows `keyspace/<name>` | `valkey/blueprint.yaml:30,33`; `ContextsTab.tsx:64-66` | ☐ |
| 46 | git diff review | ContextsTab / AppCard / NewInstanceDialog | NO `if blueprint==='postgres'` branching — declaration-driven | `endpoint_handler.go:1269-1284` (generic) | ☐ |
| **DoD #8 — ≥2 embedded DBs eliminated; remaining enumerated** | | | | | |
| 47-49 | terminal | pda → shared-pg-b, sme → shared-pg-c dependsOn edges | Both present (2 eliminations meet the bar) | `11a-bp-powerdns-admin.yaml:84-85`; `13-bp-catalyst-platform.yaml:105` | ☐ |
| 50-51 | terminal | Remaining embedded Clusters + shared-pg-c mapping | **GAP — pdns-pg (no gate), openova-flow-pg, newapi-pg remain; shared-pg-c declares only `sme_*`, NOT newapi/openova-flow.** | `powerdns/chart/templates/cnpg-cluster.yaml:33-37`; `16d-...-c.yaml:92-113` | ☐ |
| **DoD #9 — evidence** | | | | | |
| 52 | repo | Each box → screenshot under `docs/sessions/<date>/evidence/` + UAT.md row | All linked | DoD §10-11 | ☐ |

**Gaps:** (1) DataInstances.svelte+lib not deleted from SME console; (2) reuse of the 3 bootstrap instances blocked in-console; (3) shared-pg-c maps only sme, not newapi/openova-flow; (4) pdns-pg/openova-flow-pg/newapi-pg embedded DBs remain; (5) version skew bp-postgres 0.1.6 (UI seed) vs 0.1.8 (slot).
