# Stage 4 (W1c): Write path and events

Changes born in Git, webhook-triggered apply, screens by push.

47 tasks. One task is one pull request. Rows are in the order the tasks can run. "Waits for" lists the tasks that must be merged first; every task also waits for the first two tasks of the plan, `FND-021` and `FND-022`.

[Back to the overview](README.md)

## Areas in this stage

- [FND: Foundation gate and execution rules](#fnd-foundation-gate-and-execution-rules) — 5 tasks
- [NET: Network, zones and cluster classes](#net-network-zones-and-cluster-classes) — 2 tasks
- [SVC: Service model](#svc-service-model) — 2 tasks
- [CTL: Control plane](#ctl-control-plane) — 38 tasks

## FND Foundation gate and execution rules

### Group FND-200: Observability pipeline

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **FND-201**<br>lab test, 3 days | **Spike: what the telemetry pipeline delivers today, and which collector stays**<br>A result record that says what reaches the metric, log and trace stores of each region today, and names the one collector, the alert source and the kind of the objective object. | FND-120 | stated in its ticket |
| **FND-202**<br>decision, 3 days | **Decision record 0035: one telemetry pipeline, alert events, declared objectives**<br>One accepted record names the collector, the pipelines, the alert event with its fields, and the object that declares an objective. | FND-201, FND-106 | stated in its ticket |
| **FND-203**<br>new, 5 days | **One collector pipeline per cluster**<br>Each cluster runs one collector with a metrics, a logs and a traces pipeline, and the other collector's chart and slot are gone. | FND-202, FND-011 | stated in its ticket |
| **FND-204**<br>new, 5 days | **Alerts leave the cluster as events on the bus**<br>Every alert that fires or resolves is published once on the bus as a CloudEvent with the twelve agreed fields. | FND-203, FND-011 | stated in its ticket |
| **FND-205**<br>new, 5 days | **Declared objectives, and the observability gate check**<br>The platform's own objectives are objects in Git, and a gate check proves that telemetry arrives in every region and that an alert makes the round trip. | FND-204, FND-034 | Makes FND-G19 required. |

<details><summary>Files each task of this group changes</summary>

- **FND-201**: `docs/ledger/plan/spikes/FND-201-observability.md`
- **FND-202**: `docs/adr/0035-observability-pipeline.md`, `docs/adr/README.md`
- **FND-203**: `platform/opentelemetry/**`, `platform/alloy/**`, `clusters/_template/bootstrap-kit/20-opentelemetry.yaml`, `clusters/_template/bootstrap-kit/21-alloy.yaml`, `clusters/_template/bootstrap-kit/kustomization.yaml`, `scripts/expected-bootstrap-deps.yaml`, `products/catalyst/chart/templates/catalog-seed/blueprints.yaml`, `products/catalyst/bootstrap/api/internal/catalog/blueprints.json`, `products/catalyst/bootstrap/ui/src/shared/constants/catalog.generated.ts`, `docs/ledger/deleted-paths.txt`
- **FND-204**: `platform/mimir/chart/templates/**`, `platform/mimir/chart/values.yaml`, `platform/mimir/chart/tests/**`, `tests/acceptance/sovereign/internal/suite/fixture_alert*.go`
- **FND-205**: `tests/acceptance/sovereign/internal/suite/check_observability*.go`, `clusters/_template/sovereign-region/observability/**`, `docs/ledger/gates.yaml`

</details>

## NET Network, zones and cluster classes

### Group NET-220: Epic: placement contract without tiers

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **NET-222**<br>delete, 5 days | **Blueprint data without tiers**<br>Tier, non-host placement and tier prefixes removed by one codemod; seed regenerated; topology matrix deleted (after topology freeze lift and SVC's C1 codemod) | FND-120, NET-221, SVC-170, FND-011 | stated in its ticket |
| **NET-223**<br>delete, 5 days | **Application controller without the tier pivot**<br>VClusterPlacements map, tier resolution and pivot branch deleted; registry keeps the region half (before CTL-410) | NET-222 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **NET-222**: `products/catalyst/chart/templates/catalog-seed/blueprints.yaml`, `docs/topology-matrix.md`, `scripts/codemods/`
- **NET-223**: `core/controllers/application/cmd/main.go`, `core/controllers/application/internal/controller/application_controller.go`, `core/controllers/internal/clusterregistry/registry.go`

</details>

## SVC Service model

### Single tasks

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **SVC-170**<br>new, 5 days | **Contract C1: class and lifecycle on every Blueprint, with their readers**<br>class and lifecycle on all Blueprints from the fixed table, with these changes: bp-guacamole and bp-chargeback are platform-component (so unlisted); bp-model-gateway, bp-axon, bp-kagent, bp-agents-service-management are ai-service and bp-ops-records is platform-component; every row applies only if its folder exists; a Blueprint in the tree with no row stops the codemod. bp-newapi and bp-openclaw stay experimental. Classifiers deleted; chips, badges, 403 class-not-installable and 409 blueprint-deprecated; supported rule in the generator, fed with the newest counting gate record by the drift workflow. Regenerated files committed. | FND-031, SVC-010, SVC-160, FND-011 | Makes SVC-G01 required. |
| **SVC-200**<br>fix, 3 days | **Catalogue edits may change card fields only**<br>422 contract-field-read-only for any edit outside five card fields and visibility; schema check before commit. The package works before or after CTL-240; the two share a lock on the two handler files. | SVC-170 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **SVC-170**: `products/catalyst/chart/crds/blueprint.yaml`, `platform/_schemas/blueprint.schema.json`, `scripts/codemod-blueprint-c1.py`, `scripts/generate-catalog.py`, `products/catalyst/bootstrap/api/internal/catalog/catalog.go`, `products/catalyst/bootstrap/api/internal/handler/endpoint_handler.go`, `products/catalyst/bootstrap/ui/src/pages/sovereign/CatalogPage.tsx`, `​.github/workflows/blueprint-admission-validate.yml`, `​.github/workflows/preflight-blueprint-crd-cel.yaml`, `products/catalyst/bootstrap/api/internal/catalog/blueprints.json`, `products/catalyst/bootstrap/ui/src/shared/constants/catalog.generated.ts`, `products/catalyst/chart/templates/catalog-seed/blueprints.yaml` and 1 more
- **SVC-200**: `products/catalyst/bootstrap/api/internal/handler/catalog_edit_git.go`, `products/catalyst/bootstrap/api/internal/handler/blueprints.go`

</details>

## CTL Control plane

### Single tasks

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **CTL-300**<br>rework, 3 days | **The running stream is the declared one**<br>One Stream object CATALYST_ORG equal to the code's configuration; three disabled streams, streamsEnabled and the idempotency bucket deleted; ensureOrgStream binds to the declared stream and fails when it is absent; the bucket policy-rollup is not on the keep list; guard on stream creation in code. | CTL-044, FND-011 | stated in its ticket |
| **CTL-420**<br>rework, 3 days | **Watches in the organization and environment controllers**<br>Watches on Gateway and HelmRelease for Organizations; dependency wait and ready-state requeue moved to ten-minute safety nets | CTL-020, FND-011 | stated in its ticket |
| **CTL-240**<br>rework, 3 days | **Batch G: catalogue writers onto the library**<br>Catalogue edit, publish, curate and edit-pr commit through gitwrite; silent skip replaced by 503 | CTL-110 | stated in its ticket |
| **CTL-320**<br>fix, 5 days | **Durable audit**<br>CloudEvents helpers in natsbus; real audit publisher on catalyst.audit.recorded.v1; PublishStrict; replay at start; honest transport field | CTL-300, FND-032 | Makes CTL-G07 required. |
| **CTL-330**<br>delete, 3 days | **Delete the Kafka client**<br>franz-go removed from nine modules; REDPANDA_BROKERS removed; deprecated usage-stream helper deleted; the publish calls of auth and catalog that went to Kafka only are deleted. | BSS-010, CTL-300, FND-011 | stated in its ticket |
| **CTL-230**<br>rework, 3 days | **Batch F: resource browser**<br>Scale, edit, apply, delete refused for Git-owned objects; break-glass with reason for the rest; four operations audited before they run; fake pull-request redirect deleted. HandleK8sExecWebSocket and its authorisation helper stay in place for ACC-101 and ACC-123. | CTL-320 | stated in its ticket |
| **CTL-242**<br>fix, 3 days | **Catalogue edits are stored as the edited card fields only**<br>An edited or curated Blueprint is committed as a partial object: its name plus the card fields and visibility that SVC-200 allows. An upstream upgrade then lands on every other field. | CTL-240, SVC-200 | stated in its ticket |
| **CTL-355**<br>delete, 3 days | **Delete the second showback meter**<br>The route GET /api/v1/org/consumption, its handler and its panel are gone. The Organization page links to the billing engine's cost view; the ticket carries the link the billing area names. | CTL-035 | stated in its ticket |
| **CTL-345**<br>delete, 3 days | **One flow path: the flow server and the emitter leave the default kit**<br>Slots 56 and 57, the emitter, the proxy branch, the flow server and adapter removed; core and canvas kept | CTL-340 | stated in its ticket |
| **CTL-170**<br>lab test, 1 day | **Inspection: where chart-seeded Applications live and who reads them**<br>A yes/no answer whether any reader uses a follower region's chart-seeded Application, selecting variant A or B of CTL-174 | FND-120 | stated in its ticket |
| **CTL-172**<br>rework, 5 days | **Platform charts take placement from values**<br>Value bootstrapOwned.intent (placement, regions) in eleven charts, taking precedence over lookup; unset render unchanged | CTL-060, CTL-170, FND-011, FND-120 | stated in its ticket |
| **CTL-132**<br>rework, 1 day | **Ten-minute safety interval**<br>Source and Kustomization intervals at 10 minutes with 1-minute retry for the three selected source families | CTL-130, FND-120, FND-011 | Makes CTL-G01 required. |
| **CTL-126**<br>rework, 3 days | **One robot per Organization as committer**<br>gitwrite is constructed with a per-robot token source; the committer is <slug>-iac-bot or catalyst-fleet-bot. Robot tokens rest in OpenBao: the store in iacbootstrap logs in with Kubernetes auth and writes under the path SEC-190 defines; the reads of CATALYST_OPENBAO_ADDR and CATALYST_OPENBAO_TOKEN are removed. | CTL-162, SEC-190, SEC-192 | stated in its ticket |
| **CTL-174**<br>rework, 5 days | **Platform placement is committed, not patched**<br>ConfigMap platform-placement in Git with one key per slot, valuesFrom on the slots, export at start, update route commits placement | CTL-112, CTL-162, CTL-172, FND-120 | stated in its ticket |
| **CTL-180**<br>rework, 5 days | **Batch B1: Organization record from Git**<br>Organization create, patch and delete commit clusters/<fqdn>/intent/organizations/<slug>.yaml; adoption; journey J9 | CTL-120, CTL-162, FND-032 | Makes CTL-G09 required. |
| **CTL-210**<br>rework, 5 days | **Batch C: access grants from Git**<br>UserAccess create, update, delete commit clusters/<fqdn>/intent/access/<name>.yaml; adoption; journey J11 | CTL-120, CTL-130, CTL-162, FND-032 | Makes CTL-G11 required. |
| **CTL-220**<br>rework, 3 days | **Batch D1: the marketplace setting from Git**<br>Marketplace toggle commits a values ConfigMap consumed by valuesFrom; git command-line writer deleted | CTL-120, CTL-130, CTL-162 | stated in its ticket |
| **CTL-224**<br>rework, 3 days | **Batch D3: ad-hoc schedules from Git**<br>Schedule create, update, delete commit one CronJob manifest; adoption | CTL-120, CTL-130, CTL-162 | stated in its ticket |
| **CTL-250**<br>delete, 3 days | **Retire giteapr and the iac repository; guard for Git writers**<br>Package giteapr deleted; iac repository no longer created; guard G3 | CTL-162, CTL-230, CTL-240 | stated in its ticket |
| **CTL-176**<br>delete, 3 days | **Remove lookup from the platform Application templates**<br>No chart reads the live Application at render time | CTL-174, FND-120 | stated in its ticket |
| **CTL-190**<br>rework, 3 days | **Batch B2: suspend, resume and overage by commit**<br>ApplyEntitlementDecision committer with decision sequence idempotence; six routes use it; the four internal routes accept decisionId (string) and sequence (integer) in the request body; journey J10 as a check file. | CTL-180, FND-032 | Makes CTL-G06 required. |
| **CTL-212**<br>guard, 1 day | **Lock stage access to deny**<br>Stage access denied | CTL-210, FND-011 | stated in its ticket |
| **CTL-222**<br>rework, 1 day | **Batch D2: console sidebar overrides from Git**<br>Sidebar overrides committed as intent/settings/console-sidebar.yaml | CTL-220 | stated in its ticket |
| **CTL-260**<br>rework, 3 days | **Controllers' Git writes onto the library**<br>Every controller commits through gitwrite with multi-file commits; files byte-identical | CTL-060, CTL-250, FND-120 | stated in its ticket |
| **CTL-370**<br>rework, 3 days | **Second-rank polls: Application detail pages**<br>Listed files use useLiveQuery | CTL-360 | stated in its ticket |
| **CTL-372**<br>rework, 3 days | **Second-rank polls: resources pages**<br>Listed files use useLiveQuery | CTL-360 | stated in its ticket |
| **CTL-374**<br>rework, 3 days | **Second-rank polls: networking page**<br>Networking page uses useLiveQuery | CTL-360 | stated in its ticket |
| **CTL-376**<br>rework, 3 days | **Second-rank polls: architecture, readiness chip, cloud-list panes**<br>Listed files use useLiveQuery; only Sandbox and Falco polls remain, in allow with their owners | CTL-360 | stated in its ticket |
| **CTL-610**<br>test, 5 days | **Rebuild-from-Git drill**<br>Drill that restores every repository from the bundles of FND-184 into a fresh environment, seeds its Git server by that restore, and compares spec digests of all locked kinds. | CTL-162, CTL-180, CTL-210, FND-035, FND-130, FND-184, FND-185 | Makes CTL-G08 required. |
| **CTL-178**<br>guard, 1 day | **Lock stage applications to deny**<br>Stage applications denied; write verbs removed from non-Flux roles | CTL-162, CTL-176, FND-011 | Makes CTL-G04 required. |
| **CTL-410**<br>rework, 5 days | **Application controller on controller-runtime**<br>Existing Reconcile wrapped in a controller-runtime reconciler with a HelmRelease watch, work queue and leader election; re-list loop deleted | CTL-060, CTL-162, CTL-260, FND-120, NET-223, FND-011 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **CTL-300**: `platform/nats-jetstream/chart/templates/streams.yaml`, `kv-buckets.yaml`, `platform/nats-jetstream/chart/values.yaml`, `platform/nats-jetstream/chart/tests/**`, `scripts/check-event-subjects.py`, `scripts/baselines/streams.json`, `core/services/shared/events/bridge_subscriber.go`
- **CTL-420**: `core/controllers/organization/internal/controller/organization_controller.go`, `core/controllers/environment/internal/controller/environment_controller.go`, `products/catalyst/chart/templates/controllers/organization-controller-*.yaml`, `scripts/baselines/polling.json`
- **CTL-240**: `products/catalyst/bootstrap/api/internal/handler/catalog_edit_git.go`, `catalog_iac_edit.go`, `blueprints.go`
- **CTL-320**: `core/controllers/pkg/natsbus/cloudevent.go`, `products/catalyst/bootstrap/api/internal/audit/audit.go`, `products/catalyst/bootstrap/api/internal/natspub/audit_publisher.go`, `products/catalyst/bootstrap/api/cmd/api/main.go`, `products/catalyst/bootstrap/api/internal/handler/rbac_audit.go`, `docs/contracts/C3-event-subjects.yaml`, `tests/acceptance/sovereign/internal/suite/check_ctl_audit_survives_restart.go`, `tests/acceptance/sovereign/internal/suite/check_ctl_audit_survives_restart_test.go`, `docs/ledger/gates.yaml`
- **CTL-330**: `core/services/metering-sidecar/go.mod`, `core/services/shared/go.mod`, `products/catalyst/chart/templates/org-services/configmap.yaml`, `products/catalyst/chart/values.yaml`, `docs/ledger/deleted-paths.txt`
- **CTL-230**: `products/catalyst/bootstrap/api/internal/handler/k8s_resource_actions.go`, `k8s_resource_put_apply.go`, `reconcilers.go`, `jobs_retry.go`, `k8s_exec.go`, `products/catalyst/bootstrap/ui/src/pages/sovereign/resources/`, `docs/contracts/C9-mutating-routes.yaml`, `scripts/baselines/direct-cluster-writes.json`
- **CTL-242**: `products/catalyst/bootstrap/api/internal/handler/catalog_edit_git.go`, `products/catalyst/bootstrap/api/internal/handler/catalog_edit_git_test.go`, `products/catalyst/bootstrap/api/internal/handler/catalog_iac_edit.go`, `products/catalyst/bootstrap/api/internal/handler/blueprints.go`
- **CTL-355**: `products/catalyst/bootstrap/api/internal/handler/org_consumption.go`, `products/catalyst/bootstrap/api/internal/handler/org_consumption_test.go`, `products/catalyst/bootstrap/api/internal/handler/org_consumption_parent_slug_5819_test.go`, `products/catalyst/bootstrap/api/cmd/api/routes*.go`, `products/catalyst/bootstrap/ui/src/pages/sovereign/organizations/ShowbackPanel.tsx`, `products/catalyst/bootstrap/ui/src/pages/sovereign/organizations/OrganizationDetailPage.tsx`, `products/catalyst/bootstrap/ui/src/pages/sovereign/organizations/BillingModeGate.tsx`, `products/catalyst/bootstrap/ui/src/pages/sovereign/organizations/BillingModeGate.test.tsx`, `scripts/baselines/api-structure.json`, `docs/ledger/deleted-paths.txt`
- **CTL-345**: `clusters/_template/bootstrap-kit/56-bp-openova-flow-server.yaml`, `57-bp-openova-flow-emitter.yaml`, `clusters/_template/bootstrap-kit/kustomization.yaml`, `scripts/expected-bootstrap-deps.yaml`, `products/catalyst/bootstrap/api/internal/handler/flow_emitter.go`, `openova_flow_proxy.go`, `products/openova-flow/server/**`, `products/openova-flow/adapter-flux/**`, `scripts/baselines/polling.json`, `docs/ledger/deleted-paths.txt`
- **CTL-170**: `docs/ledger/plan/spikes/CTL-170.md`
- **CTL-132**: `core/controllers/pkg/fluxsource/fluxsource.go`, `core/controllers/organization/internal/controller/per_org_flux.go`, `clusters/_template/sovereign-region/base/gitrepository.yaml`, `clusters/_template/sovereign-region/base/region-kustomization.yaml`, `products/catalyst/chart/templates/catalog-sovereign-flux/catalog-sovereign-gitrepository.yaml`, `docs/ledger/gates.yaml`
- **CTL-126**: `products/catalyst/bootstrap/api/internal/intent/tokens.go`, `products/catalyst/bootstrap/api/cmd/api/main.go`, `core/controllers/organization/internal/iacbootstrap/bootstrap.go`, `core/controllers/organization/internal/iacbootstrap/openbao_store.go`, `core/controllers/organization/cmd/main.go`
- **CTL-174**: `products/catalyst/bootstrap/api/internal/intent/platform.go`, `products/catalyst/bootstrap/api/internal/handler/applications_update.go`, `docs/contracts/C4-git-layout-and-commit-library.md`
- **CTL-180**: `products/catalyst/bootstrap/api/internal/handler/organization_provisioning.go`, `org_tenant_org_cr.go`, `products/catalyst/bootstrap/api/internal/intent/organizations.go`, `docs/contracts/C9-mutating-routes.yaml`, `scripts/baselines/direct-cluster-writes.json`, `tests/acceptance/sovereign/internal/suite/check_ctl_organization_from_git.go`, `tests/acceptance/sovereign/internal/suite/check_ctl_organization_from_git_test.go`, `docs/ledger/gates.yaml`
- **CTL-210**: `products/catalyst/bootstrap/api/internal/handler/user_access.go`, `rbac_assign.go`, `user_access_owner_seed.go`, `products/catalyst/bootstrap/api/internal/intent/access.go`, `docs/contracts/C9-mutating-routes.yaml`, `scripts/baselines/*.json`, `tests/acceptance/sovereign/internal/suite/check_ctl_access_from_git.go`, `tests/acceptance/sovereign/internal/suite/check_ctl_access_from_git_test.go`, `docs/ledger/gates.yaml`
- **CTL-220**: `products/catalyst/bootstrap/api/internal/handler/marketplace_settings.go`, `products/catalyst/bootstrap/api/internal/intent/settings.go`, `clusters/_template/bootstrap-kit/13-bp-catalyst-platform.yaml`, `docs/contracts/C9-mutating-routes.yaml`, `scripts/baselines/*.json`
- **CTL-224**: `products/catalyst/bootstrap/api/internal/handler/schedules.go`, `products/catalyst/bootstrap/api/internal/intent/schedules.go`
- **CTL-250**: `products/catalyst/bootstrap/api/internal/giteapr/**`, `core/controllers/organization/internal/iacbootstrap/bootstrap.go`, `scripts/check-git-writers.py`, `scripts/baselines/git-writers.json`, `.github/workflows/check-git-writers.yaml`, `docs/ledger/deleted-paths.txt`
- **CTL-190**: `products/catalyst/bootstrap/api/internal/handler/org_suspend.go`, `org_suspend_internal.go`, `org_overage_stop_internal.go`, `products/catalyst/bootstrap/api/internal/intent/entitlement.go`, `docs/contracts/C9-mutating-routes.yaml`, `tests/acceptance/sovereign/internal/suite/check_ctl_decision_by_commit.go`, `tests/acceptance/sovereign/internal/suite/check_ctl_decision_by_commit_test.go`, `docs/ledger/gates.yaml`
- **CTL-212**: `products/catalyst/chart/values.yaml`, `scripts/baselines/lock-stages.json`
- **CTL-222**: `products/catalyst/bootstrap/api/internal/handler/console_ui.go`, `products/catalyst/bootstrap/api/internal/intent/settings.go`
- **CTL-260**: `core/controllers/organization/internal/controller/per_org_flux.go`, `organization_controller.go`, `core/controllers/environment/internal/controller/environment_controller.go`, `core/controllers/application/internal/controller/region_delivery.go`, `region_continuum.go`, `application_controller.go`, `core/controllers/continuum/internal/controller/store.go`, `scripts/baselines/git-writers.json`
- **CTL-370**: `products/catalyst/bootstrap/ui/src/pages/sovereign/AppDetail.tsx`, `pages/sovereign/AppDetail/useResolvedPlacement.ts`, `scripts/baselines/polling.json`
- **CTL-372**: `scripts/baselines/polling.json`
- **CTL-374**: `products/catalyst/bootstrap/ui/src/pages/sovereign/networking/NetworkingPage.tsx`, `scripts/baselines/polling.json`
- **CTL-376**: `products/catalyst/bootstrap/ui/src/pages/sovereign/Architecture.tsx`, `pages/sovereign/cloud-list/ControllerLogsPane.tsx`, `components/ReadinessChip.tsx`, `widgets/cloud-list/EventsPanel.tsx`, `scripts/baselines/polling.json`
- **CTL-610**: `scripts/rebuild-from-git-drill.sh`, `scripts/intent-snapshot.py`, `tests/acceptance/sovereign/**`, `docs/RUNBOOKS.md`, `tests/acceptance/sovereign/internal/suite/check_ctl_rebuild_from_git.go`, `tests/acceptance/sovereign/internal/suite/check_ctl_rebuild_from_git_test.go`, `docs/ledger/gates.yaml`
- **CTL-178**: `products/catalyst/chart/values.yaml`, `scripts/baselines/lock-stages.json`, `docs/ledger/gates.yaml`
- **CTL-410**: `core/controllers/application/cmd/main.go`, `core/controllers/application/internal/controller/application_controller.go`, `reconcile_serial.go`, `core/controllers/pkg/regionfile/`, `products/catalyst/chart/templates/controllers/**`, `scripts/baselines/polling.json`

</details>

### Group CTL-200: Epic: one Organization pipeline

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **CTL-202**<br>rework, 3 days | **DNS records as a step of the organization-controller**<br>DNS step in the controller with condition DNSReady; pipeline step deleted | CTL-180, CTL-201 | stated in its ticket |
| **CTL-203**<br>rework, 3 days | **Identity clients as a step of the organization-controller**<br>Identity-client step in the controller with condition IdentityReady | CTL-202 | stated in its ticket |
| **CTL-204**<br>rework, 3 days | **Console certificate as a step of the organization-controller**<br>TLS step in the controller with condition TLSReady; direct Secret writes and the five-minute reconciler deleted | CTL-203, SEC-095 | stated in its ticket |
| **CTL-205**<br>rework, 5 days | **Initial Applications and the storefront path through the API**<br>Initial Applications as Application files; internal route POST /api/v1/internal/organizations, which accepts planSlug and the commerce block and writes the block unchanged, with orderID, into the committed Organization file; the provisioning service calls the API and its raw cluster writes are deleted. | CTL-204 | stated in its ticket |
| **CTL-207**<br>delete, 5 days | **Delete the legacy overlay, the pipeline state and the second client**<br>Legacy overlay writer, pipeline state machine, tenant registry store, provisioning renderers and its Git client deleted | CTL-205 | stated in its ticket |
| **CTL-208**<br>delete, 3 days | **Delete the org-tenants Flux source**<br>The org-tenants GitRepository and Kustomization templates removed | CTL-207, SOV-240, FND-011 | stated in its ticket |
| **CTL-209**<br>guard, 1 day | **Lock stage organizations to deny**<br>Stage organizations denied | CTL-207, FND-011 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **CTL-202**: `core/controllers/organization/internal/controller/**`, `products/catalyst/bootstrap/api/internal/handler/organization_dns.go`, `organization_provisioning.go`
- **CTL-203**: `core/controllers/organization/internal/controller/**`, `products/catalyst/bootstrap/api/internal/handler/organization_keycloak.go`, `organization_provisioning.go`
- **CTL-204**: `core/controllers/organization/internal/controller/**`, `core/controllers/organization/internal/gitops/manifests.go`, `products/catalyst/bootstrap/api/internal/handler/org_console_tls.go`, `org_console_tls_reconcile.go`, `organization_provisioning.go`
- **CTL-205**: `products/catalyst/bootstrap/api/internal/handler/organization_provisioning.go`, `organization_funnel_apps.go`, `products/catalyst/bootstrap/api/internal/intent/organizations.go`, `core/services/provisioning/handlers/**`, `core/services/provisioning/main.go`, `docs/contracts/C9-mutating-routes.yaml`
- **CTL-207**: `products/catalyst/bootstrap/api/internal/handler/organization_gitops.go`, `organization_provisioning.go`, `tenant_registry_reconcile.go`, `products/catalyst/bootstrap/api/internal/store/organization_provisioning.go`, `store/tenant_registry.go`, `core/services/provisioning/gitops/**`, `core/services/provisioning/github/**`, `scripts/baselines/*.json`, `docs/ledger/deleted-paths.txt`
- **CTL-208**: `products/catalyst/chart/templates/org-services/org-repo-gitrepository.yaml`, `org-repo-kustomization.yaml`, `gitea-org-repo-bootstrap-job.yaml`, `products/catalyst/chart/values.yaml`, `docs/ledger/deleted-paths.txt`
- **CTL-209**: `products/catalyst/chart/values.yaml`, `scripts/baselines/lock-stages.json`

</details>

