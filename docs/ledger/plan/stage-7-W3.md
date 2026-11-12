# Stage 7 (W3): Dependent modules

Machine operations, exemplar agents, migration, managed Kubernetes, more native services.

118 tasks. One task is one pull request. Rows are in the order the tasks can run. "Waits for" lists the tasks that must be merged first; every task also waits for the first two tasks of the plan, `FND-021` and `FND-022`.

[Back to the overview](README.md)

## Areas in this stage

- [CLD: Cloud abstraction](#cld-cloud-abstraction) — 6 tasks
- [SOV: Sovereign lifecycle](#sov-sovereign-lifecycle) — 6 tasks
- [NET: Network, zones and cluster classes](#net-network-zones-and-cluster-classes) — 4 tasks
- [SVC: Service model](#svc-service-model) — 1 tasks
- [CMP: Compliance and posture](#cmp-compliance-and-posture) — 4 tasks
- [BSS: Commerce and billing](#bss-commerce-and-billing) — 7 tasks
- [AIP: Agent plane](#aip-agent-plane) — 20 tasks
- [INF: Infrastructure plane](#inf-infrastructure-plane) — 24 tasks
- [MIG: Migration from VMware](#mig-migration-from-vmware) — 28 tasks
- [ACC: Access plane](#acc-access-plane) — 15 tasks
- [PRD: Later products](#prd-later-products) — 3 tasks

## CLD Cloud abstraction

### Single tasks

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **CLD-093**<br>test, 3 days | **Contract rule: adding a region plans only additions**<br>A script that reads a plan as JSON and fails on any action other than create or no-op. On the local pack: apply one region, plan two, run the script. For the Huawei module the gate lane runs the same script on request on a copy of a live working directory with one more region. If the module fails, the package stops and reports; it does not change main.tf. | CLD-087, CLD-090 | stated in its ticket |
| **CLD-222**<br>new, 5 days | **PostgreSQL, native side: unit module day2/postgresinstance and Composition postgresinstance-native**<br>PostgreSQL, native side: unit module day2/postgresinstance and Composition postgresinstance-native. | CLD-221, SVC-320 | Makes CLD-G19 required. |

<details><summary>Files each task of this group changes</summary>

- **CLD-093**: `infra/providers/local/tests/**`, `infra/providers/huawei/tests/**`, `scripts/check-plan-additive.py`, `.github/workflows/conformance-local.yaml`
- **CLD-222**: `infra/providers/huawei/day2/postgresinstance/**`, `clusters/_template/providers/huawei/compositions/postgresinstance-native.yaml`, `clusters/_template/providers/huawei/kustomization.yaml`, `infra/providers/huawei/profile.yaml`, `infra/providers/huawei/pack.yaml`, `tests/acceptance/sovereign/internal/suite/check_*.go`

</details>

### Group CLD-200: Epic: hyperscaler pack (default AWS), files-only proof on a real cloud

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **CLD-201**<br>new, 5 days | **AWS bootstrap module**<br>Module to contract version 1 with host-port load balancer targets, day2_facts and the pack's seal hook. | CLD-175, CLD-212 | stated in its ticket |
| **CLD-202**<br>new, 5 days | **AWS profile, adapter and overlay**<br>Profile, adapter with OrphanSweeper and LivenessProber, overlay releases | CLD-201 | stated in its ticket |
| **CLD-203**<br>new, 3 days | **AWS unit module nodepool and its Composition**<br>Third NodePool implementation as a unit Workspace | CLD-202 | stated in its ticket |
| **CLD-204**<br>test, 3 days | **AWS live walk**<br>Single-region Sovereign on AWS walked through the module gate; list of contract defects found | CLD-203 | Makes CLD-G16 required. |

<details><summary>Files each task of this group changes</summary>

- **CLD-201**: `infra/providers/aws/**`
- **CLD-202**: `products/catalyst/bootstrap/api/internal/providers/aws/**`, `clusters/_template/providers/aws/**`, `products/catalyst/bootstrap/api/internal/providers/all/all.go`
- **CLD-203**: `infra/providers/aws/day2/nodepool/**`, `clusters/_template/providers/aws/compositions/nodepool-native.yaml`, `infra/providers/aws/profile.yaml`
- **CLD-204**: `infra/providers/aws/profile.yaml`

</details>

## SOV Sovereign lifecycle

### Group SOV-900: EPIC: After the proof: decommission, mothership rebuild, orchestration move, region add

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **SOV-920**<br>rework, 5 days | **A replacement mothership is provisioned and proven**<br>Sovereign M in mothership mode, made by the standard path | SOV-820 | stated in its ticket |
| **SOV-950**<br>rework, 5 days | **The residual sequencer moves to a controller (conditional)**<br>Sequencer behind the unchanged status ConfigMap and routes, driven by a controller-runtime reconciler; not started unless CTL schedules multi-replica | SOV-620, FND-011 | stated in its ticket |
| **SOV-970**<br>new, 5 days | **Add a region by Git commit**<br>Route with body {"code","role","workerCount"} (no controlPlaneCount) that commits a base window with the new region, its unit Workspaces and its folder at the paths of contract C4. The Sovereign object is refreshed with the third region. The pull request carries Decision: ADR-0017. Journey L3. | CLD-093, CTL-035, FND-032, FND-120, NET-100, NET-131, SOV-175, SOV-620, SOV-740 | Makes SOV-G16 required. |
| **SOV-930**<br>rework, 3 days | **Child records and public names move to the replacement**<br>Records imported, public names moved, old API scaled to zero for 14 days | SOV-920 | stated in its ticket |
| **SOV-940**<br>delete, 1 day | **The Kustomize twins and the transitional keys are deleted**<br>Four twin files, legacyOwnVariables and ORIGIN_PROXY_ENDPOINTS_JSON removed | SOV-930, FND-011 | stated in its ticket |
| **SOV-960**<br>rework, 5 days | **The parent's provisioning watch moves to the same controller (conditional)**<br>Create route only records; reconciler runs provision, wait, hand-over; OpenTofu leaves the API image | SOV-940, SOV-950 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **SOV-920**: `docs/RUNBOOKS.md`
- **SOV-950**: `core/controllers/sovereign/**`, `products/catalyst/chart/templates/controllers/`, `products/catalyst/bootstrap/api/internal/handler/cutover*.go`, `products/catalyst/chart/templates/clusterrole-cutover-driver.yaml`
- **SOV-970**: `products/catalyst/bootstrap/api/internal/lifecycle/regions.go`, `tests/acceptance/sovereign/internal/suite/check_region_add.go`
- **SOV-930**: `docs/RUNBOOKS.md`
- **SOV-940**: `products/catalyst/chart/templates/*-kustomize.yaml`, `products/catalyst/chart/templates/kustomization.yaml`, `products/catalyst/chart/.helmignore`, `platform/mothership-ops/**`, `products/catalyst/bootstrap/api/internal/provisioner/provisioner.go`, `docs/contracts/c5-upstream-variables.schema.json`, `core/controllers/pkg/upstream`, `scripts/check-lifecycle-ratchets.sh`, `docs/ledger/deleted-paths.txt`
- **SOV-960**: `core/controllers/sovereign/internal/controller/provision.go`, `products/catalyst/bootstrap/api/internal/provisioner/**`, `products/catalyst/bootstrap/api/Containerfile`

</details>

## NET Network, zones and cluster classes

### Group NET-320: Epic: the first managed cluster class

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **NET-321**<br>decision, 3 days | **Decision record: managed cluster class on the reference cloud**<br>Record of the mechanism, vendor permissions and measured flags from the spike note | NET-290 | stated in its ticket |
| **NET-322**<br>new, 5 days | **Class file, schema and conformance probe as a suite profile**<br>Section 4.8 built: class files, schema, per-flag probe checks read by the gate's needs field, duplicate-pod-address check | NET-321 | Makes NET-G17 required. |

<details><summary>Files each task of this group changes</summary>

- **NET-321**: `docs/adr/`
- **NET-322**: `platform/cluster-classes/`, `tests/acceptance/sovereign/internal/suite/`

</details>

### Single tasks

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **NET-280**<br>lab test, 5 days | **Spike: overlay profile**<br>Outcome TUNNEL, NODE-OVERLAY or NONE; runs only when a multi-region Sovereign without a private link is scheduled | CLD-087 | stated in its ticket |
| **NET-310**<br>rework, 5 days | **Cilium 1.20**<br>Five upgrade-note items applied; mixed-version mesh proven | NET-132, NET-166, FND-011 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **NET-280**: `docs/sessions/`
- **NET-310**: `platform/cilium/chart/values.yaml`, `platform/gateway-api/chart/**`, `tests/hermetic/mesh/`

</details>

## SVC Service model

### Group SVC-310: Epic: PostgreSQL as a capability

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **SVC-330**<br>new, 5 days | **Native PostgreSQL**<br>PostgresInstance definition and bp-postgres-managed with stated limits; no provider label; spec.services.postgresinstances added with its reader, value from the result of SVC-315; bp-postgres stays the default. | CLD-222, SEC-290, SVC-320, FND-011 | Makes SVC-G05 required. |

<details><summary>Files each task of this group changes</summary>

- **SVC-330**: `platform/crossplane-claims/chart/templates/xrds/postgresinstance.yaml`, `platform/postgres-managed/`, `tests/acceptance/sovereign/internal/suite/check_svc_postgres.go`, `docs/ledger/gates.yaml`

</details>

## CMP Compliance and posture

### Group CMP-120: Epic: frameworks

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **CMP-125**<br>new, 5 days | **OSCAL export**<br>Route R17 with deterministic component-definition and assessment-results documents validated against pinned schemas | CMP-123, CMP-131 | Makes CMP-G25 required. |

<details><summary>Files each task of this group changes</summary>

- **CMP-125**: `products/catalyst/bootstrap/api/internal/compliance/oscal.go`, `products/catalyst/bootstrap/api/internal/compliance/testdata/oscal/*.json`, `products/catalyst/bootstrap/api/internal/compliance/routes.go`, `scripts/check-oscal-export.py`, `scripts/oscal/*.json`, `.github/workflows/policy-chart-tests.yaml`, `products/catalyst/bootstrap/ui/src/pages/sovereign/posture/FrameworkDetail.tsx`, `tests/acceptance/sovereign/internal/suite/check_oscal_export.go`, `docs/ledger/gates.yaml`

</details>

### Single tasks

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **CMP-170**<br>new, 3 days | **Efficiency measure**<br>Axis efficiency from hourly memory usage series and cached requests; null when no data | CMP-150, FND-203, FND-205, FND-011 | Makes CMP-G27 required. |
| **CMP-156**<br>new, 5 days | **Machine reports in the read model**<br>PolicyReports with scope kind Machine and sources machine-hardening, machine-vulnerability, machine-patch and machine-test become findings on subject kind Machine. The model's source list gains the four values and their checks. Machine vulnerabilities use the deadline table of rule V with publishedAt, fixedVersion and exposure. The package adds the property exposure to the machine report writer together with this reader. The machine controller is listed as a producer on the Clusters view. | CMP-091, CMP-150, INF-140, INF-160, INF-240, FND-011 | Makes CMP-G28 required. |
| **CMP-155**<br>new, 3 days | **Access signals on the posture hub**<br>Counts read by informer from the access kinds of group access.openova.io, registered as optional kinds: grants active now, targets with approval.required false, session findings without a disposition. Shown, not scored. Break-glass uses come with CMP-152. | ACC-103, ACC-204, CMP-016, CMP-150 | Makes CMP-G31 required. |

<details><summary>Files each task of this group changes</summary>

- **CMP-170**: `products/catalyst/bootstrap/api/internal/compliance/efficiency.go`, `platform/kyverno-policies/chart/model/model.yaml`, `platform/kyverno-policies/chart/crds/compliancemodel.yaml`, `docs/ledger/gates.yaml`
- **CMP-156**: `products/catalyst/bootstrap/api/internal/compliance/adapters/policyreport.go`, `products/catalyst/bootstrap/api/internal/compliance/producers.go`, `platform/kyverno-policies/chart/crds/compliancemodel.yaml`, `platform/kyverno-policies/chart/model/checks.yaml`, `docs/ledger/gates.yaml`
- **CMP-155**: `products/catalyst/bootstrap/api/internal/k8scache/kinds.go`, `products/catalyst/bootstrap/api/internal/compliance/routes.go`, `products/catalyst/bootstrap/ui/src/pages/sovereign/posture/ClustersTab.tsx`, `docs/ledger/gates.yaml`

</details>

## BSS Commerce and billing

### Group BSS-200: Epic: collector plug-in contract

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **BSS-201**<br>rework, 5 days | **Collector interface and registry; the existing collector behind it**<br>Collector, FileImporter, Purger, Recommender and registry; kind CHECK dropped; layer a plain column; source kind a pattern string | BSS-131, CLD-163, FND-011 | stated in its ticket |
| **BSS-202**<br>new, 5 days | **Bill file collector (FOCUS)**<br>Kind file with a FOCUS importer and POST /api/v1/sources/{id}/usage/import; check BSS-G11 | BSS-201 | Makes BSS-G11 required. |
| **BSS-203**<br>rework, 3 days | **Neutral meter class**<br>Column meter_class written at insert and backfilled; the stopped-resource predicate reads it | BSS-202 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **BSS-201**: `products/chargeback/internal/collector/collector.go`, `products/chargeback/internal/collector/huawei/plugin.go`, `products/chargeback/cmd/chargeback/main.go`, `products/chargeback/internal/api/server.go`, `products/chargeback/internal/api/sources.go`, `products/chargeback/internal/api/sources_purge.go`, `products/chargeback/internal/recommend/recommend.go`, `products/chargeback/internal/adapter/openova/repository.go`, `products/chargeback/internal/adapter/openova/orgsync.go`, `products/chargeback/internal/store/store.go`, `products/chargeback/internal/store/models.go`, `products/chargeback/internal/store/allocation.go` and 5 more
- **BSS-202**: `products/chargeback/internal/collector/focusfile/**`, `products/chargeback/cmd/chargeback/main.go`, `products/chargeback/internal/api/server.go`, `products/chargeback/internal/api/usage_import.go`, `products/chargeback/cmd/seed-history/apply.go`, `tests/acceptance/sovereign/internal/suite/check_bss_bill_file.go`, `docs/ledger/gates.yaml`
- **BSS-203**: `products/chargeback/internal/store/store.go`, `products/chargeback/internal/store/cost.go`, `products/chargeback/internal/collector/huawei/plugin.go`, `products/chargeback/internal/collector/focusfile/**`, `products/chargeback/internal/adapter/openova/collector.go`, `products/chargeback/internal/adapter/openova/modelusage.go`, `scripts/provider-leak-baseline.tsv`

</details>

### Single tasks

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **BSS-160**<br>rework, 5 days | **The platform collector meters a virtual machine Application**<br>The existing platform source (kind openova-org) tracks every Application of bp-virtual-machine: size from the parameters flavor, rootDiskGiB and dataDisks; compute hours only while status.powerState of the composite resource is Running; disk hours always. No new source kind. Holds lock billing-engine. ticket_rev 0 until the SKU shape is settled. | BSS-203, INF-240, FND-011 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **BSS-160**: `products/chargeback/internal/adapter/openova/collector.go`, `products/chargeback/internal/adapter/openova/collector_vm.go`, `products/chargeback/internal/adapter/openova/collector_vm_test.go`, `products/chargeback/internal/store/models.go`, `products/chargeback/chart/templates/rbac.yaml`

</details>

### Group BSS-300: Epic: instances that report to a provider system

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **BSS-301**<br>new, 5 days | **Instance identity and the usage statement intake (parent side)**<br>instance_identity, source kind bss-instance with link secret and link mode, signed push route with revision and period-closed rules; check BSS-G12 | BSS-201, BSS-203 | Makes BSS-G12 required. |
| **BSS-302**<br>new, 5 days | **An instance reports its usage (child side)**<br>uplink and uplink_statements, three uplink routes, daily build with revisions, https exporter through the existing outbox; check BSS-G13 | BSS-301 | Makes BSS-G13 required. |
| **BSS-303**<br>test, 3 days | **The standalone installation option is proven in CI**<br>Chart render test and integration test for option operator-central with the adapter off; README section | BSS-302, FND-011 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **BSS-301**: `products/chargeback/internal/federation/**`, `products/chargeback/internal/store/federation.go`, `products/chargeback/internal/store/models.go`, `products/chargeback/internal/api/server.go`, `products/chargeback/internal/api/sources.go`, `products/chargeback/internal/api/instance_statements.go`, `products/chargeback/ui/src/pages/**`, `tests/acceptance/sovereign/internal/suite/check_bss_statement_intake.go`, `docs/ledger/gates.yaml`
- **BSS-302**: `products/chargeback/internal/federation/uplink.go`, `products/chargeback/internal/federation/statement.go`, `products/chargeback/internal/store/federation.go`, `products/chargeback/internal/commercial/external/exporter.go`, `products/chargeback/internal/commercial/deliver.go`, `products/chargeback/internal/api/server.go`, `products/chargeback/internal/api/uplink.go`, `products/chargeback/ui/src/pages/**`, `products/chargeback/cmd/chargeback/main.go`, `tests/acceptance/sovereign/internal/suite/check_bss_uplink.go`, `docs/ledger/gates.yaml`
- **BSS-303**: `products/chargeback/chart/tests/standalone-profile.sh`, `products/chargeback/internal/federation/standalone_integration_test.go`, `.github/workflows/build-chargeback.yaml`, `products/chargeback/README.md`

</details>

## AIP Agent plane

### Group AIP-200: EPIC: bp-kagent

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **AIP-201**<br>fix, 3 days | **Node preparation for the sandbox class, exactly as the spike recorded**<br>Reference-class nodes can run the sandbox runtime class | AIP-030 | stated in its ticket |
| **AIP-202**<br>fix, 3 days | **Host-side fence: every namespace of an Organization reaches the model gateway and the tool server; callers of an agent runtime**<br>Allow rules that let every namespace of an Organization reach the replicated gateway and tool-server Services, the callers rule for an agent runtime install, and the dial rows. | AIP-170, NET-170 | stated in its ticket |
| **AIP-203**<br>new, 5 days | **Chart platform/kagent, Blueprint file, conformance agent and check**<br>As before; the Blueprint uses depends for PostgreSQL until SVC-320 has merged, then requires. | AIP-150, AIP-170, AIP-201, AIP-202, SEC-213, SVC-160, FND-011 | Makes AIP-G08 required. |

<details><summary>Files each task of this group changes</summary>

- **AIP-202**: `core/controllers/organization/internal/gitops/manifests.go`, `tests/acceptance/sovereign/policy-dials.yaml`
- **AIP-203**: `platform/kagent/**`, `.github/workflows/build-bp-kagent.yaml`, `tests/acceptance/sovereign/internal/suite/check_aip_g08.go`, `docs/ledger/gates.yaml`, `scripts/agent-plane-baseline.yaml`

</details>

### Group AIP-220: EPIC: ops-records

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **AIP-221**<br>rework, 5 days | **Service skeleton, schema with migrations, outbox table, health**<br>Go module with the tables of section 4.10.3 and a store interface | FND-011, FND-052 | stated in its ticket |
| **AIP-222**<br>rework, 5 days | **Signal intake, grouping, candidate scoring, rule results**<br>Rule path that opens and attaches incidents without any agent | AIP-221 | stated in its ticket |
| **AIP-224**<br>rework, 3 days | **Guard library agentguard and its adversarial corpus**<br>Fence, redaction, link allow-list and a turn budget that fails closed | AIP-221 | stated in its ticket |
| **AIP-223**<br>rework, 5 days | **Judge dispatcher: lanes, deadlines, agent tokens, A2A call, validation, late revision**<br>Bounded-judge contract of section 4.10.4 in code | AIP-120, AIP-222 | stated in its ticket |
| **AIP-225**<br>rework, 5 days | **MCP endpoint and console HTTP routes inside the service**<br>Incident tools and the routes of section 4.10.5, filtered by Organization | AIP-223, AIP-224 | stated in its ticket |
| **AIP-227**<br>fix, 3 days | **Console read path: reverse proxy /api/v1/ops/* in catalyst-api**<br>Session-verified proxy that asserts Organization, user and tier headers and refuses agent tokens | AIP-225 | stated in its ticket |
| **AIP-226**<br>new, 5 days | **bp-ops-records: chart, Blueprint file, slot 83, caller entry and check**<br>As before, but the shipped caller entry names the one Organization that hosts the platform's own agents (the slug is stated in the ticket), never the wildcard; a Sovereign widens it by a values commit. The Blueprint uses depends for PostgreSQL until SVC-320 has merged. | AIP-203, AIP-225, SEC-290, SVC-160, FND-011 | Makes AIP-G10 required. |
| **AIP-228**<br>new, 5 days | **Console pages: incident list, incident with timeline and actions, judgements**<br>Operator screens for incidents and judgements | AIP-226, AIP-227 | Makes AIP-G14 required. |

<details><summary>Files each task of this group changes</summary>

- **AIP-221**: `products/ops-records/**`, `.github/workflows/test-ungated-trees.yaml`
- **AIP-222**: `products/ops-records/internal/signals/**`, `products/ops-records/internal/correlate/**`
- **AIP-224**: `products/ops-records/internal/agentguard/**`
- **AIP-223**: `products/ops-records/internal/judge/**`
- **AIP-225**: `products/ops-records/internal/mcp/**`, `products/ops-records/internal/api/**`, `products/ops-records/cmd/**`
- **AIP-227**: `products/catalyst/bootstrap/api/internal/opsproxy/**`, `products/catalyst/bootstrap/api/cmd/api/main.go`, `products/catalyst/bootstrap/api/cmd/api/routes*.go`, `products/catalyst/bootstrap/api/cmd/api/testdata/routes.golden`, `docs/contracts/C9-mutating-routes.yaml`
- **AIP-226**: `products/ops-records/chart/**`, `products/ops-records/blueprint.yaml`, `products/ops-records/Containerfile`, `clusters/_template/bootstrap-kit/83-bp-ops-records.yaml`, `clusters/_template/bootstrap-kit/13-bp-catalyst-platform.yaml`, `clusters/_template/bootstrap-kit/kustomization.yaml`, `clusters/_template/bootstrap-kit/placement.yaml`, `scripts/expected-bootstrap-deps.yaml`, `tests/acceptance/sovereign/internal/suite/check_aip_g10.go`, `docs/ledger/gates.yaml`
- **AIP-228**: `products/catalyst/bootstrap/ui/src/pages/sovereign/ops/**`, `products/catalyst/bootstrap/ui/src/app/router.tsx`, `tests/acceptance/sovereign/internal/suite/check_aip_g14.go`, `docs/ledger/gates.yaml`

</details>

### Group AIP-100: EPIC: Axon as the simulation back end

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **AIP-107**<br>fix, 3 days | **Images and structured output in Axon**<br>data-URL image parts and json_schema response format through the kit | AIP-106 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **AIP-107**: `products/axon/src/upstream.ts`, `products/axon/src/turns.ts`, `products/axon/src/routes/chat-completions.ts`, `products/axon/test/**`

</details>

### Single tasks

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **AIP-270**<br>new, 5 days | **Virtual keys for Organization Applications (built on first need)**<br>As before; first consumer bp-cortex (PRD-209), key name cortex. | AIP-140, CTL-120, FND-011 | Makes AIP-G18 required. |
| **AIP-320**<br>new, 5 days | **Model access for a Sovereign that holds no login: backend kind upstream through the parent's gateway**<br>As before, but the upstream back end is off by default and is switched on per child by an explicit value. | AIP-270, FND-011 | Makes AIP-G05, AIP-G11 required. |
| **AIP-280**<br>fix, 3 days | **Traces from gateway and agent runtime to the cluster collector**<br>gen_ai.* trace attributes exported | AIP-203, FND-203, FND-011 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **AIP-270**: `products/catalyst/bootstrap/api/internal/modelkeys/**`, `platform/model-gateway/chart/**`, `tests/acceptance/sovereign/internal/suite/check_aip_g18.go`, `docs/ledger/gates.yaml`
- **AIP-320**: `platform/model-gateway/chart/**`, `clusters/_template/bootstrap-kit/81-bp-model-gateway.yaml`, `core/services/model-meter/**`
- **AIP-280**: `platform/model-gateway/chart/**`, `platform/kagent/chart/**`

</details>

### Group AIP-240: EPIC: bp-agents-service-management

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **AIP-241**<br>rework, 5 days | **Correlator: template, schema, prompt, evaluation set, and the evaluation runner**<br>First exemplar agent with at least 20 evaluation cases | AIP-226, FND-011 | stated in its ticket |
| **AIP-242**<br>rework, 3 days | **Investigator**<br>Second exemplar agent with its evaluation set | AIP-241, FND-011 | stated in its ticket |
| **AIP-243**<br>new, 3 days | **Scribe; the Blueprint file; the evaluation check**<br>Installable Blueprint of the three first agents; AIP-G11 implemented | AIP-242, SVC-160, FND-011 | Makes AIP-G22 required. |
| **AIP-244**<br>new, 5 days | **Coordinator and problem-manager, with their rule functions**<br>Major-incident grouping and problem records by rule and by judge | AIP-243, FND-011 | Makes AIP-G15 required. |
| **AIP-245**<br>new, 5 days | **Fixer; tool propose_change; change-proposal route; change_links**<br>An agent's fix is a pull request on branch change/<id> that a person merges | AIP-244, CTL-160, CTL-700, FND-011 | Makes AIP-G16 required. |

<details><summary>Files each task of this group changes</summary>

- **AIP-241**: `products/agents-service-management/**`, `scripts/agent-eval.py`
- **AIP-242**: `products/agents-service-management/**`
- **AIP-243**: `products/agents-service-management/**`, `tests/acceptance/sovereign/internal/suite/check_aip_g11.go`, `docs/ledger/gates.yaml`
- **AIP-244**: `products/ops-records/internal/judge/**`, `products/ops-records/internal/correlate/**`, `products/agents-service-management/**`, `tests/acceptance/sovereign/internal/suite/check_aip_g15.go`, `docs/ledger/gates.yaml`
- **AIP-245**: `products/catalyst/bootstrap/api/internal/changeproposal/**`, `products/catalyst/bootstrap/api/internal/agenttoken/**`, `products/catalyst/bootstrap/api/cmd/api/main.go`, `products/catalyst/bootstrap/api/cmd/api/routes*.go`, `products/catalyst/bootstrap/api/cmd/api/testdata/routes.golden`, `docs/contracts/C9-mutating-routes.yaml`, `products/openova-mcp/internal/tools/**`, `products/ops-records/**`, `products/agents-service-management/**`, `tests/acceptance/sovereign/internal/suite/check_aip_g16.go`, `docs/ledger/gates.yaml`

</details>

## INF Infrastructure plane

### Group INF-500: Epic: light testing (INF-510, 512, 515, 520, 530)

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **INF-512**<br>new, 3 days | **Runner test task and its tools**<br>Nuclei with signed templates and ZAP baseline in the image; exact argument lists per profile | INF-120 | stated in its ticket |
| **INF-510**<br>new, 5 days | **TestScope, scope rules, generated egress and kill handling**<br>The definition, fail-closed scope rules, egress policy limited to resolved targets and ports, kill handling | INF-420, INF-455, FND-011 | stated in its ticket |
| **INF-515**<br>new, 3 days | **Kill route; check INF-G07**<br>Audited kill route and the scope-escape check | INF-510, INF-512 | Makes INF-G07 required. |
| **INF-520**<br>new, 3 days | **Agent tool and approval screens**<br>Tool request_machine_test with the single argument scope; scopes screen with Approve and Kill. Needs AIP's tool-server package | INF-490, INF-515, FND-011 | stated in its ticket |
| **INF-530**<br>document, 1 day | **Rules of engagement and provider terms in the runbook**<br>One runbook section: scope contents, approvers, cloud terms, testing.enabled stays false until F6 is answered | INF-515 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **INF-512**: `products/catalyst/bootstrap/api/internal/machines/runner/test.go`, `products/catalyst/bootstrap/api/internal/machines/runner/test_test.go`, `products/machine-runner/Containerfile`, `products/machine-runner/tools.lock.yaml`, `products/machine-runner/LICENSES.md`
- **INF-510**: `platform/machine-operations/chart/crds/testscopes.yaml`, `platform/machine-operations/chart/crds/machineruns.yaml`, `platform/machine-operations/chart/values.yaml`, `products/catalyst/bootstrap/api/internal/machines/apis/v1alpha1/**`, `products/catalyst/bootstrap/api/internal/machines/operations/test.go`, `products/catalyst/bootstrap/api/internal/machines/operations/test_test.go`, `products/catalyst/bootstrap/api/internal/machines/operations/egress.go`
- **INF-515**: `products/catalyst/bootstrap/api/internal/machines/routes/kill.go`, `products/catalyst/bootstrap/api/internal/machines/routes/kill_test.go`, `products/catalyst/bootstrap/api/internal/machines/routes/register.go`, `docs/contracts/C9-mutating-routes.yaml`, `products/catalyst/bootstrap/api/internal/k8scache/kinds.go`, `tests/acceptance/sovereign/internal/suite/check_inf_g07.go`, `docs/ledger/gates.yaml`
- **INF-520**: `products/openova-mcp/**`, `products/catalyst/bootstrap/ui/src/pages/sovereign/machine-operations/TestScopes*.tsx`
- **INF-530**: `docs/RUNBOOKS.md`

</details>

### Group INF-700: Epic: native engine (INF-710, 720, 730) — outline; tickets written after the spike

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **INF-710**<br>lab test, 3 days | **Spike: the host security service on the operator-hosted stack**<br>Whether the service is deployed and callable, which families it protects, whether findings list by API, whether an agent is needed | FND-070 | stated in its ticket |
| **INF-720**<br>new, 5 days | **Capability HostSecurity; engine native for scans; check INF-G09**<br>Provider capability and ScanPolicy.engine with native | INF-430, INF-710, FND-011 | Makes INF-G09 required. |
| **INF-730**<br>new, 3 days | **Profile section and console display of engine availability**<br>spec.machines.native in the profile with the console as reader | CLD-080, INF-720 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **INF-710**: `docs/ledger/plan/spikes/INF-710.md`
- **INF-720**: `products/catalyst/bootstrap/api/internal/providers/hostsecurity.go`, `products/catalyst/bootstrap/api/internal/providers/huawei/**`, `products/catalyst/bootstrap/api/internal/machines/operations/native.go`, `platform/machine-operations/chart/crds/scanpolicies.yaml`, `tests/acceptance/sovereign/internal/suite/check_inf_g09.go`, `docs/ledger/gates.yaml`
- **INF-730**: `infra/providers/_contract/cloudprofile.schema.json`, `infra/providers/huawei/profile.yaml`, `products/catalyst/bootstrap/ui/src/pages/sovereign/machine-operations/**`

</details>

### Group INF-400: Epic: operations on machines over SSH (INF-410, 420, 430, 440, 450, 455, 460, 465, 470, 480, 490, 540)

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **INF-420**<br>new, 5 days | **Runner SSH mode, trust on declared machines, identity rules; check INF-G05**<br>Nine-step SSH sequence, facts task, per-Job egress policy, access ConfigMap with module variable and first-boot trust, rules 9 and 11, INF-G05 as expected-fail. Needs CLD's package for local machines on TCP 22 | CLD-086, INF-240, INF-320, INF-410, FND-011 | stated in its ticket |
| **INF-465**<br>new, 5 days | **Adoption of discovered machines**<br>Import block driven by adopt_cloud_id, observe-only Workspace, promote route gated on a zero-change plan | CLD-174, INF-240, INF-250, INF-330, FND-011 | stated in its ticket |
| **INF-430**<br>new, 5 days | **Scans over SSH; flip INF-G05**<br>Inventory and hardening playbooks for Debian and RHEL families, machine-created trigger, default policies for virtual machines | INF-420, FND-011 | Makes INF-G05 required. |
| **INF-440**<br>new, 5 days | **Runner patch task**<br>Patch playbooks with the never-a-false-success rules and health checks | INF-430 | stated in its ticket |
| **INF-450**<br>new, 5 days | **MaintenanceWindow, PatchPolicy and the roll-out logic**<br>Two definitions, window functions and timers, rings, serial groups, approval gate, soak, patch report, ring chart value | INF-430, FND-011 | stated in its ticket |
| **INF-455**<br>new, 5 days | **Run routes; check INF-G06**<br>Request, approve and cancel routes under the audit rule; deterministic patch check with a forced pending reboot. Needs CTL's package for operation machine-run in C9 | CTL-320, INF-440, INF-450 | Makes INF-G06 required. |
| **INF-460**<br>new, 5 days | **Enrolment of discovered machines**<br>Enrol route that stores a one-time credential in OpenBao only, the enrol run and playbook, deletion of the credential after the run. Needs SEC's write-only producer identity and founder decision F1 (b) | INF-330, INF-455, SEC-190, FND-011 | stated in its ticket |
| **INF-470**<br>new, 5 days | **VirtualMachineSnapshot and pre-patch snapshots**<br>Second composite kind with two Compositions and its reader, PatchPolicy.preSnapshot | CLD-080, INF-455, FND-011 | stated in its ticket |
| **INF-480**<br>new, 5 days | **Hardening remediation**<br>HardeningBaseline.mode with enforce, PatchPolicy.hardeningBaselineRef, remediation from the same content release | INF-430, INF-455, FND-011 | stated in its ticket |
| **INF-490**<br>new, 5 days | **Policy intent routes and screens**<br>Generic PUT/DELETE writing object files into every region folder; windows, policies, runs and source screens. Needs CTL's package for scope FleetMachineOps | FND-120, INF-135, INF-175, INF-455 | stated in its ticket |
| **INF-540**<br>guard, 1 day | **Lock stage machine-ops to deny**<br>Stage switched to deny after two trains at zero; write verbs removed from non-Flux roles | INF-135, INF-490, FND-011 | stated in its ticket |
| **INF-495**<br>new, 3 days | **Hold operations on a machine under migration**<br>The operations controller consumes catalyst.migration.vm_phase_changed.v1. From to: Landed until a terminal phase, the Machine whose provider identifier equals the event's server identifier gets no Job from any policy. | INF-320, INF-455, MIG-129, FND-011 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **INF-420**: `infra/providers/huawei/day2/virtualmachine/**`, `infra/providers/local/day2/virtualmachine/**`, `clusters/_template/providers/huawei/compositions/virtualmachine-native.yaml`, `clusters/_template/providers/local/compositions/virtualmachine-native.yaml`, `platform/machine-operations/chart/crds/machines.yaml`, `platform/machine-operations/chart/crds/machineruns.yaml`, `products/catalyst/bootstrap/api/internal/machines/runner/ssh.go`, `products/catalyst/bootstrap/api/internal/machines/runner/facts.go`, `products/catalyst/bootstrap/api/internal/machines/operations/job.go`, `products/catalyst/bootstrap/api/internal/machines/operations/egress.go`, `products/catalyst/bootstrap/api/internal/machines/operations/identity.go`, `products/catalyst/bootstrap/api/internal/machines/inventory/access_configmap.go` and 5 more
- **INF-465**: `infra/providers/huawei/day2/virtualmachine/**`, `infra/providers/local/day2/virtualmachine/**`, `clusters/_template/providers/huawei/compositions/virtualmachine-native.yaml`, `clusters/_template/providers/local/compositions/virtualmachine-native.yaml`, `platform/crossplane-claims/chart/templates/xrds/virtualmachine.yaml`, `platform/virtual-machine/chart/**`, `products/catalyst/bootstrap/api/internal/machines/routes/adopt.go`, `docs/contracts/C9-mutating-routes.yaml`, `tests/acceptance/sovereign/internal/suite/check_inf_g04.go`, `products/catalyst/bootstrap/api/internal/machines/adoption/**`, `products/catalyst/bootstrap/api/internal/machines/routes/register.go`
- **INF-430**: `products/machine-runner/content/playbooks/inventory.yml`, `products/machine-runner/content/playbooks/hardening.yml`, `products/machine-runner/content/tests/**`, `products/catalyst/bootstrap/api/internal/machines/runner/inventory_ssh.go`, `products/catalyst/bootstrap/api/internal/machines/runner/hardening_ssh.go`, `products/catalyst/bootstrap/api/internal/machines/operations/trigger.go`, `platform/machine-operations/chart/crds/scanpolicies.yaml`, `platform/machine-operations/chart/templates/defaults.yaml`, `tests/acceptance/sovereign/internal/suite/check_inf_g05.go`, `docs/ledger/gates.yaml`
- **INF-440**: `products/machine-runner/content/playbooks/patch-debian.yml`, `products/machine-runner/content/playbooks/patch-rhel.yml`, `products/machine-runner/content/tests/**`, `products/catalyst/bootstrap/api/internal/machines/runner/patch.go`, `products/catalyst/bootstrap/api/internal/machines/runner/patch_test.go`, `products/catalyst/bootstrap/api/internal/machines/runner/health.go`
- **INF-450**: `platform/machine-operations/chart/crds/maintenancewindows.yaml`, `platform/machine-operations/chart/crds/patchpolicies.yaml`, `platform/machine-operations/chart/crds/machines.yaml`, `platform/machine-operations/chart/crds/machineruns.yaml`, `platform/machine-operations/chart/crds/scanpolicies.yaml`, `platform/machine-operations/chart/templates/rbac-operations.yaml`, `products/catalyst/bootstrap/api/internal/machines/apis/v1alpha1/**`, `products/catalyst/bootstrap/api/internal/machines/operations/window.go`, `products/catalyst/bootstrap/api/internal/machines/operations/patch.go`, `products/catalyst/bootstrap/api/internal/machines/operations/report_patch.go`, `platform/virtual-machine/chart/values.yaml`, `platform/virtual-machine/chart/values.schema.json` and 1 more
- **INF-455**: `products/catalyst/bootstrap/api/internal/machines/routes/runs.go`, `products/catalyst/bootstrap/api/internal/machines/routes/runs_test.go`, `products/catalyst/bootstrap/api/internal/machines/routes/register.go`, `docs/contracts/C9-mutating-routes.yaml`, `products/catalyst/bootstrap/api/internal/k8scache/kinds.go`, `tests/acceptance/sovereign/internal/suite/check_inf_g06.go`, `docs/ledger/gates.yaml`
- **INF-460**: `products/catalyst/bootstrap/api/internal/machines/operations/enrol.go`, `products/machine-runner/content/playbooks/enrol.yml`, `products/machine-runner/content/tests/**`, `products/catalyst/bootstrap/api/internal/machines/routes/enrol.go`, `platform/machine-operations/chart/crds/machineruns.yaml`, `platform/openbao/chart/templates/_config.tpl`, `docs/contracts/C9-mutating-routes.yaml`, `tests/acceptance/sovereign/internal/suite/check_inf_g05.go`
- **INF-470**: `infra/providers/huawei/day2/virtualmachinesnapshot/**`, `infra/providers/local/day2/virtualmachinesnapshot/**`, `platform/crossplane-claims/chart/templates/xrds/virtualmachinesnapshot.yaml`, `clusters/_template/providers/huawei/compositions/virtualmachinesnapshot-native.yaml`, `clusters/_template/providers/local/compositions/virtualmachinesnapshot-native.yaml`, `platform/machine-operations/chart/crds/patchpolicies.yaml`, `products/catalyst/bootstrap/api/internal/machines/operations/patch.go`, `tests/acceptance/sovereign/internal/suite/check_inf_g06.go`, `infra/providers/_contract/cloudprofile.schema.json`
- **INF-480**: `platform/machine-operations/chart/crds/hardeningbaselines.yaml`, `platform/machine-operations/chart/crds/patchpolicies.yaml`, `products/machine-runner/content/playbooks/harden.yml`, `products/catalyst/bootstrap/api/internal/machines/operations/patch.go`, `products/catalyst/bootstrap/api/internal/machines/operations/scan.go`
- **INF-490**: `products/catalyst/bootstrap/api/internal/machines/routes/intent.go`, `products/catalyst/bootstrap/api/internal/machines/routes/intent_test.go`, `docs/contracts/C9-mutating-routes.yaml`, `products/catalyst/bootstrap/ui/src/pages/sovereign/machine-operations/**`, `products/catalyst/bootstrap/ui/src/pages/sovereign/sovereignNav.ts`, `products/catalyst/bootstrap/api/internal/k8scache/kinds.go`, `products/catalyst/bootstrap/ui/e2e/gate/machines/**`, `tests/acceptance/sovereign/internal/suite/check_inf_g06.go`
- **INF-540**: `products/catalyst/chart/values.yaml`, `scripts/baselines/lock-stages.json`
- **INF-495**: `products/catalyst/bootstrap/api/internal/machines/operations/migrationhold.go`, `products/catalyst/bootstrap/api/internal/machines/operations/migrationhold_test.go`, `products/catalyst/bootstrap/api/internal/machines/operations/job.go`, `platform/machine-operations/chart/templates/deployment-operations.yaml`, `platform/network-policies/chart/templates/allow/machine-ops.yaml`

</details>

### Group INF-600: Epic: golden images and rotation (INF-610, 620, 630, 640) — outline; tickets written after the spike

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **INF-610**<br>lab test, 3 days | **Spike: bake by capture on the reference cloud and the local pack**<br>Per pack: can a prepared, generalised machine be captured and a new one booted from it | INF-240 | stated in its ticket |
| **INF-620**<br>new, 5 days | **Kind MachineImage, capture module, catalogue commit**<br>Image build sequence of 4.20 ending in one catalogue commit | CLD-080, INF-250, INF-490, INF-610, FND-011 | stated in its ticket |
| **INF-630**<br>new, 3 days | **Node pools from a baked image**<br>NodePool.spec.image read by the nodepool module | CLD-175, INF-620 | stated in its ticket |
| **INF-640**<br>new, 5 days | **Engine image-rotation; check INF-G08**<br>PatchPolicy.engine with image-rotation, status.imageRef and output image_ref, controller commits the new image key to the owning Application | CTL-260, INF-455, INF-620, FND-011 | Makes INF-G08 required. |

<details><summary>Files each task of this group changes</summary>

- **INF-610**: `docs/ledger/plan/spikes/INF-610.md`
- **INF-620**: `platform/machine-operations/chart/crds/machineimages.yaml`, `products/catalyst/bootstrap/api/internal/machines/operations/image.go`, `infra/providers/huawei/day2/machineimage/**`, `infra/providers/local/day2/machineimage/**`, `products/machine-runner/content/playbooks/bake.yml`, `infra/providers/_contract/cloudprofile.schema.json`
- **INF-630**: `infra/providers/huawei/day2/nodepool/**`, `infra/providers/local/day2/nodepool/**`
- **INF-640**: `platform/machine-operations/chart/crds/patchpolicies.yaml`, `platform/crossplane-claims/chart/templates/xrds/virtualmachine.yaml`, `infra/providers/huawei/day2/virtualmachine/**`, `infra/providers/local/day2/virtualmachine/**`, `products/catalyst/bootstrap/api/internal/machines/operations/rotate.go`, `tests/acceptance/sovereign/internal/suite/check_inf_g08.go`, `docs/ledger/gates.yaml`

</details>

## MIG Migration from VMware

### Group MIG-100: Hermetic engine

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **MIG-101**<br>new, 5 days | **Module skeleton and API types**<br>The Go module of the migration product exists with the API types of five kinds and a test that every enum value has a named reader. | MIG-010, INF-230, MIG-020, FND-102 | stated in its ticket |
| **MIG-102**<br>new, 3 days | **Definitions, chart skeleton, Blueprint file**<br>The five definitions, the chart skeleton with its values and the Blueprint file exist, and the catalogue lists `bp-vm-migration` as unlisted and experimental. | MIG-101, SVC-170, SVC-040, FND-102, FND-011 | stated in its ticket |
| **MIG-111**<br>new, 5 days | **`vmcopy`: copy loop, result format, `file` reader, `fetch`**<br>The worker binary copies a disk and its changed areas through a network block device endpoint, proven byte for byte against files, and reports in the one result format. | MIG-101 | stated in its ticket |
| **MIG-112**<br>new, 3 days | **Worker image and conversion**<br>The public worker image exists with the pinned nbdkit and the conversion tools and without the vendor library, and a standing test proves that a converted disk boots. | MIG-111, MIG-060, MIG-050, FND-061 | stated in its ticket |
| **MIG-113**<br>new, 5 days | **vSphere control and change tracking**<br>The worker can describe, fence, unfence, power and snapshot source machines and ask vSphere for changed disk areas, proven against the SDK's simulator and recorded responses. | MIG-101, MIG-111, MIG-020 | stated in its ticket |
| **MIG-124**<br>new, 3 days | **Landing contract on the `local` pack**<br>The `local` pack's virtual-machine module accepts the six landing variables and returns the two landing outputs, and the pack overlay supplies the landing values of the migration chart. | MIG-102, INF-240, CLD-174, CLD-088, CLD-090 | stated in its ticket |
| **MIG-114**<br>new, 3 days | **`vddk` reader and the library init contract**<br>The worker reads source disks through the vendor disk library that an init container supplies at run time, and refuses a library version outside the tested range. | MIG-112, MIG-113, MIG-020 | stated in its ticket |
| **MIG-121**<br>new, 5 days | **Controller manager, `SourceProvider` reconciler, chart templates**<br>The controller runs with no credential, makes a source provider Ready through a helper Job that holds the source credential, and the chart ships the service accounts, policies and the admission rule for landing Workspaces. | MIG-102, MIG-113, SEC-190, SEC-211, NET-150, FND-061, FND-011 | stated in its ticket |
| **MIG-120**<br>new, 3 days | **Package `landing`: the landing Workspace in Go**<br>The controller can render a landing Workspace, read its outputs, move it to stage `server`, and end it by destroying or by observing and releasing. | MIG-124, MIG-121, CLD-010 | stated in its ticket |
| **MIG-128**<br>new, 3 days | **`NetworkMap` and `StorageMap` reconcilers**<br>Network maps and storage maps become Ready only when their source identifiers exist in vSphere and their storage classes exist in the cluster. | MIG-121 | stated in its ticket |
| **MIG-122**<br>new, 3 days | **Plan validation**<br>A plan is validated against the facts of its machines and carries, per machine, exactly the concerns of the closed list. | MIG-128, FND-011 | stated in its ticket |
| **MIG-123**<br>new, 5 days | **`Migration` reconciler: prepare, initial copy, synchronisation**<br>A migration prepares its port and volumes, runs the first copy and the repeated synchronisations under the declared limits, and reaches the state that waits for cutover. | MIG-122, MIG-120, MIG-111 | stated in its ticket |
| **MIG-129**<br>new, 3 days | **Hooks, events, and the helper sub-commands of cutover**<br>Hooks run as Jobs without any credential, the two migration events are registered and can be published, and the worker can fence, unfence and probe. | MIG-123, CTL-300, CTL-320, CTL-045 | stated in its ticket |
| **MIG-125**<br>new, 5 days | **Cutover and landing**<br>At the cutover time a group of machines is fenced, synchronised a last time, converted, landed through the landing Workspace and probed, in that order. | MIG-129, MIG-120, MIG-112 | stated in its ticket |
| **MIG-126**<br>new, 5 days | **Rollback, acceptance, cancel, rehearsal**<br>A landed migration can be rolled back to the fenced source, accepted without destroying anything, cancelled before cutover, or rehearsed without touching the source. | MIG-125 | stated in its ticket |
| **MIG-127**<br>test, 3 days | **Hermetic journeys on the local pack; function test**<br>On the local pack of every pull request, a disk is copied byte for byte and a machine is landed, rolled back and accepted, and both checks are required. | MIG-126, MIG-112, MIG-050, CLD-087, FND-130, FND-011 | Makes MIG-G01, MIG-G02 required. |

<details><summary>Files each task of this group changes</summary>

- **MIG-101**: `products/vm-migration/go.mod`, `products/vm-migration/go.sum`, `products/vm-migration/README.md`, `products/vm-migration/api/v1alpha1/**`, `.github/workflows/build-vm-migration.yaml`, `README.md`, `docs/generated/components.md`
- **MIG-102**: `products/vm-migration/chart/Chart.yaml`, `products/vm-migration/chart/values.yaml`, `products/vm-migration/chart/ci/test-values.yaml`, `products/vm-migration/chart/crds/*.yaml`, `products/vm-migration/chart/templates/_helpers.tpl`, `products/vm-migration/blueprint.yaml`, `products/vm-migration/api/v1alpha1/examples_test.go`, `products/vm-migration/api/v1alpha1/testdata/*.yaml`, `.github/workflows/build-vm-migration.yaml`, `products/catalyst/bootstrap/ui/src/shared/constants/catalog.generated.ts`, `products/catalyst/bootstrap/api/internal/catalog/blueprints.json`, `docs/generated/components.md` and 1 more
- **MIG-111**: `products/vm-migration/cmd/vmcopy/**`, `products/vm-migration/internal/reader/**`, `products/vm-migration/internal/copy/**`, `products/vm-migration/internal/result/**`, `products/vm-migration/go.mod`, `products/vm-migration/go.sum`, `.github/workflows/build-vm-migration.yaml`
- **MIG-112**: `products/vm-migration/Containerfile.worker`, `products/vm-migration/internal/convert/**`, `products/vm-migration/cmd/vmcopy/convert.go`, `.github/workflows/build-vm-migration.yaml`, `.github/workflows/check-no-vendor-disk-library.yaml`, `.github/rulesets/main.json`
- **MIG-113**: `products/vm-migration/internal/source/**`, `products/vm-migration/internal/vsphere/**`, `products/vm-migration/cmd/vmcopy/sync.go`, `products/vm-migration/go.mod`, `products/vm-migration/go.sum`
- **MIG-124**: `infra/providers/local/day2/virtualmachine/variables.tf`, `infra/providers/local/day2/virtualmachine/main.tf`, `infra/providers/local/day2/virtualmachine/outputs.tf`, `infra/providers/local/day2/virtualmachine/UNSUPPORTED`, `infra/providers/local/day2/virtualmachine/tests/landing.tftest.hcl`, `clusters/_template/providers/local/values/provider-values-vm-migration.yaml`, `clusters/_template/providers/local/kustomization.yaml`
- **MIG-114**: `products/vm-migration/internal/reader/vddk/**`, `products/vm-migration/cmd/vmcopy/sync.go`, `products/vm-migration/README.md`
- **MIG-121**: `products/vm-migration/cmd/controller/**`, `products/vm-migration/cmd/vmcopy/ping.go`, `products/vm-migration/cmd/vmcopy/describe_refs.go`, `products/vm-migration/internal/controller/provider/**`, `products/vm-migration/internal/jobs/**`, `products/vm-migration/Containerfile.controller`, `products/vm-migration/chart/templates/deployment.yaml`, `products/vm-migration/chart/templates/serviceaccounts.yaml`, `products/vm-migration/chart/templates/rbac.yaml`, `products/vm-migration/chart/templates/networkpolicy.yaml`, `products/vm-migration/chart/templates/service.yaml`, `products/vm-migration/chart/templates/admission-landing.yaml` and 3 more
- **MIG-120**: `products/vm-migration/internal/landing/**`, `products/vm-migration/go.mod`, `products/vm-migration/go.sum`
- **MIG-128**: `products/vm-migration/internal/controller/maps/**`, `products/vm-migration/cmd/controller/main.go`
- **MIG-122**: `products/vm-migration/internal/controller/plan/**`, `products/vm-migration/internal/validate/**`, `products/vm-migration/cmd/vmcopy/describe.go`, `products/vm-migration/cmd/vmcopy/list_machines.go`, `products/vm-migration/cmd/controller/**`, `products/vm-migration/chart/templates/rbac.yaml`
- **MIG-123**: `products/vm-migration/internal/controller/migration/**`, `products/vm-migration/cmd/vmcopy/remove_stale_snapshots.go`, `products/vm-migration/cmd/controller/main.go`
- **MIG-129**: `products/vm-migration/internal/hooks/**`, `products/vm-migration/internal/events/**`, `products/vm-migration/cmd/vmcopy/fence.go`, `products/vm-migration/cmd/vmcopy/unfence.go`, `products/vm-migration/cmd/vmcopy/probe.go`, `products/vm-migration/go.mod`, `products/vm-migration/go.sum`, `docs/contracts/C3-event-subjects.yaml`
- **MIG-125**: `products/vm-migration/internal/controller/migration/**`
- **MIG-126**: `products/vm-migration/internal/controller/migration/**`
- **MIG-127**: `tests/acceptance/sovereign/internal/suite/check_migration_copy.go`, `tests/acceptance/sovereign/internal/suite/check_migration_copy_test.go`, `tests/acceptance/sovereign/internal/suite/check_migration_land.go`, `tests/acceptance/sovereign/internal/suite/check_migration_land_test.go`, `tests/acceptance/sovereign/internal/suite/migration_fixture.go`, `products/vm-migration/deploy/helmrelease.yaml`, `products/vm-migration/chart/templates/tests/function-test.yaml`, `docs/ledger/gates.yaml`

</details>

### Group MIG-140: Network extension

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **MIG-141**<br>new, 5 days | **`l2relay` agent: data plane**<br>The relay agent keeps moved and unmoved machines of one split subnet reachable to each other over a tunnel, proven in network namespaces. | MIG-101, MIG-040 | stated in its ticket |
| **MIG-146**<br>new, 3 days | **`l2relay` control interface and image**<br>The relay takes its configuration over a mutually authenticated control interface on the target side and forwards it to the source side, and its public image exists. | MIG-141, MIG-060, FND-061 | stated in its ticket |
| **MIG-143**<br>new, 3 days | **Controller wiring for extensions**<br>The controller pushes the list of moved addresses to the relays, resolves map entries that name an extension, and probes for a duplicate address before a machine starts. | MIG-145, MIG-146, MIG-126, SEC-250, SEC-095, FND-011 | stated in its ticket |
| **MIG-144**<br>test, 3 days | **Extension gate checks**<br>The wiring from an extension to its relay is proven on the local pack of every pull request, a split subnet is proven on the reference cloud, and both checks are required. | MIG-143, MIG-147, MIG-127, MIG-133 | Makes MIG-G03, MIG-G07 required. |
| **MIG-154**<br>new, 3 days | **Console: Extensions page**<br>The sovereign-admin console has an Extensions page that shows tunnel state and lets a person create an extension, move its gateway and read the source relay's start arguments. | MIG-147, MIG-153 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **MIG-141**: `products/vm-migration/cmd/l2relay/**`, `products/vm-migration/internal/relay/**`, `products/vm-migration/go.mod`, `products/vm-migration/go.sum`, `.github/workflows/build-vm-migration.yaml`
- **MIG-146**: `products/vm-migration/internal/relay/control/**`, `products/vm-migration/cmd/l2relay/run.go`, `products/vm-migration/Containerfile.relay`, `.github/workflows/build-vm-migration.yaml`, `.github/workflows/check-no-vendor-disk-library.yaml`
- **MIG-143**: `products/vm-migration/internal/controller/extension/**`, `products/vm-migration/internal/controller/maps/**`, `products/vm-migration/internal/controller/migration/**`, `products/vm-migration/cmd/controller/main.go`, `products/vm-migration/chart/templates/certificate.yaml`, `products/vm-migration/chart/templates/deployment.yaml`, `scripts/secret-guard-baselines/secret-object-register.yaml`
- **MIG-144**: `tests/acceptance/sovereign/internal/suite/check_migration_extension_wiring.go`, `tests/acceptance/sovereign/internal/suite/check_migration_extension_wiring_test.go`, `tests/acceptance/sovereign/internal/suite/check_migration_split_subnet.go`, `tests/acceptance/sovereign/internal/suite/check_migration_split_subnet_test.go`, `docs/ledger/gates.yaml`
- **MIG-154**: `products/catalyst/bootstrap/ui/src/pages/sovereign/migration/extensions/**`, `products/catalyst/bootstrap/ui/src/app/router.tsx`

</details>

### Group MIG-150: Write path and screens

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **MIG-151**<br>new, 5 days | **Intent routes, credentials route, route-table entries**<br>Every migration object and every migration action is created by a route that validates and commits to Git, and the source credential goes to OpenBao without passing through Git. | MIG-126, CTL-110, CTL-120, CTL-160, CTL-035, SEC-190, CTL-045 | stated in its ticket |
| **MIG-152**<br>new, 5 days | **Console: Sources and Plans pages**<br>The sovereign-admin console has Sources and Plans pages: a person registers a source and its credential, builds a plan, and sees each machine's concerns. | MIG-151, CTL-360 | stated in its ticket |
| **MIG-156**<br>test, 3 days | **Intent round-trip check**<br>On the local pack of every pull request, migration objects created through the routes become files in Git and Ready objects, and the check is required. | MIG-151, MIG-127, CTL-120 | Makes MIG-G09 required. |
| **MIG-153**<br>new, 3 days | **Console: Migrations page**<br>The sovereign-admin console has a Migrations page: a person watches each machine's phase and schedules cutover, accepts, rolls back or cancels. | MIG-152 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **MIG-151**: `products/catalyst/bootstrap/api/internal/migration/**`, `products/catalyst/bootstrap/api/cmd/api/routes*.go`, `products/catalyst/bootstrap/api/cmd/api/testdata/routes.golden`, `docs/contracts/C9-mutating-routes.yaml`, `.github/workflows/build-vm-migration.yaml`
- **MIG-152**: `products/catalyst/bootstrap/ui/src/pages/sovereign/migration/sources/**`, `products/catalyst/bootstrap/ui/src/pages/sovereign/migration/plans/**`, `products/catalyst/bootstrap/ui/src/app/router.tsx`, `products/catalyst/bootstrap/ui/src/pages/sovereign/SovereignSidebar.tsx`, `products/catalyst/bootstrap/ui/src/pages/sovereign/SovereignSidebar.menu-mapping.test.tsx`
- **MIG-156**: `tests/acceptance/sovereign/internal/suite/check_migration_intent.go`, `tests/acceptance/sovereign/internal/suite/check_migration_intent_test.go`, `docs/ledger/gates.yaml`
- **MIG-153**: `products/catalyst/bootstrap/ui/src/pages/sovereign/migration/migrations/**`, `products/catalyst/bootstrap/ui/src/app/router.tsx`

</details>

### Group MIG-130: Landing on the reference cloud

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **MIG-133**<br>test, 3 days | **Live journeys on the reference cloud**<br>A release train that reaches the lab vSphere proves a warm Linux migration onto the reference cloud and its rollback, and both checks are required. | MIG-131, MIG-132, MIG-114, MIG-127, FND-141, INF-310 | Makes MIG-G04, MIG-G06 required. |

<details><summary>Files each task of this group changes</summary>

- **MIG-133**: `tests/acceptance/sovereign/internal/suite/check_migration_warm_linux.go`, `tests/acceptance/sovereign/internal/suite/check_migration_warm_linux_test.go`, `tests/acceptance/sovereign/internal/suite/check_migration_rollback.go`, `tests/acceptance/sovereign/internal/suite/check_migration_rollback_test.go`, `.github/workflows/foundation-gate.yaml`, `docs/ledger/gates.yaml`

</details>

### Single tasks

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **MIG-170**<br>new, 5 days | **`MigrationWave`**<br>Migrations can be grouped in a wave that fixes their window, their order and how many machines cut over at once, and a check on the local pack proves the order. | MIG-126, MIG-151, MIG-156, FND-011 | Makes MIG-G10 required. |
| **MIG-180**<br>document, 3 days | **Runbook and status**<br>The runbook tells a sovereign-admin how to migrate virtual machines step by step, and the status document says what the gate proves. | MIG-133, MIG-144 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **MIG-170**: `products/vm-migration/api/v1alpha1/migrationwave_types.go`, `products/vm-migration/api/v1alpha1/migration_types.go`, `products/vm-migration/api/v1alpha1/readers.go`, `products/vm-migration/api/v1alpha1/zz_generated.deepcopy.go`, `products/vm-migration/api/v1alpha1/examples_test.go`, `products/vm-migration/api/v1alpha1/testdata/migrationwave.yaml`, `products/vm-migration/chart/crds/*.yaml`, `products/vm-migration/chart/templates/rbac.yaml`, `products/vm-migration/internal/controller/wave/**`, `products/vm-migration/internal/controller/migration/**`, `products/vm-migration/cmd/controller/main.go`, `products/catalyst/bootstrap/api/internal/migration/**` and 5 more
- **MIG-180**: `docs/RUNBOOKS.md`, `docs/STATUS.md`

</details>

## ACC Access plane

### Group ACC-200: Epic: review (order: ACC-201, ACC-202, ACC-203, ACC-204, ACC-205)

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **ACC-201**<br>lab test, 3 days | **Spike: text for review, and the model's fit**<br>Results for typed-line reconstruction, typescript extraction, redaction, rule and model precision and recall on 20 scripted sessions, in-Sovereign alias, frame rendering cost. | ACC-112, AIP-140 | stated in its ticket |
| **ACC-202**<br>new, 5 days | **Rule file, matcher, findings**<br>Rule file written fresh, matcher on typed lines and terminal text, redaction, findings in session status with source rule and action none, session_finding event; no action taken. | ACC-133, ACC-201, FND-011 | Makes ACC-G10 required. |
| **ACC-203**<br>new, 5 days | **Tool server for the review agents**<br>Five read and report tools on port 8082 with token review for audience access-review; refuses every call unless the model alias is marked in-Sovereign. | ACC-202, FND-011 | Makes ACC-G19 required. |
| **ACC-204**<br>new, 5 days | **End and hold; dispositions; review screen**<br>Six-condition action bar as one pure function; end-session and hold (a commit that suspends grants); disposition route and event; review screen; sessions with open findings are not deleted. | ACC-122, ACC-203 | Makes ACC-G18 required. |
| **ACC-205**<br>new, 3 days | **Frame tool for the review agents**<br>Tool access_session_frame returning one PNG of a recorded screen at an offset; dispatched only if spike ACC-201 says 'frame: within limits' and a vision alias inside the Sovereign exists. | ACC-201, ACC-203 | Makes ACC-G20 required. |

<details><summary>Files each task of this group changes</summary>

- **ACC-201**: `docs/ledger/plan/spikes/ACC-201.md`
- **ACC-202**: `platform/guacamole/chart/files/review-rules.yaml`, `platform/guacamole/chart/templates/review-rules-configmap.yaml`, `platform/guacamole/chart/crds/accesstarget.yaml`, `platform/guacamole/chart/crds/accesssession.yaml`, `core/controllers/access/api/v1alpha1/types.go`, `core/controllers/access/internal/review/`, `core/controllers/access/internal/broker/tunnel.go`, `core/controllers/access/internal/broker/ingest.go`, `core/controllers/access/internal/events/events.go`, `docs/contracts/C3-event-subjects.yaml`, `products/catalyst/bootstrap/ui/e2e/gate/access/acc-g10.spec.ts`, `docs/ledger/gates.yaml`
- **ACC-203**: `core/controllers/access/internal/broker/mcp.go`, `platform/guacamole/chart/templates/controller-service.yaml`, `platform/guacamole/chart/values.yaml`, `tests/acceptance/sovereign/internal/suite/check_acc_g19.go`, `docs/ledger/gates.yaml`
- **ACC-204**: `core/controllers/access/internal/review/action.go`, `core/controllers/access/internal/broker/sessions.go`, `core/controllers/access/internal/broker/internal.go`, `products/catalyst/bootstrap/api/internal/accessintent/dispositions.go`, `products/catalyst/bootstrap/api/internal/accessintent/holds.go`, `products/catalyst/bootstrap/ui/src/pages/sovereign/access/SessionReviewPage.tsx`, `products/catalyst/bootstrap/ui/src/app/router.tsx`, `core/controllers/access/internal/events/events.go`, `docs/contracts/C3-event-subjects.yaml`, `docs/contracts/C9-mutating-routes.yaml`, `products/catalyst/bootstrap/ui/e2e/gate/access/acc-g18.spec.ts`, `docs/ledger/gates.yaml`
- **ACC-205**: `core/controllers/access/internal/review/frame.go`, `core/controllers/access/internal/broker/mcp.go`, `core/controllers/access/Containerfile`, `tests/acceptance/sovereign/internal/suite/check_acc_g20.go`, `docs/ledger/gates.yaml`

</details>

### Single tasks

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **ACC-230**<br>new, 3 days | **Live observation**<br>Stream route with follow=true pushing new chunks as stored; player live mode; Watch on active sessions; same reviewer and own-session rules; replay event carries live true. | ACC-112, ACC-122 | Makes ACC-G23 required. |
| **ACC-240**<br>new, 3 days | **Platform nodes as SSH targets, without the static key**<br>Nodes trust the SSH authority through a Plan; one AccessTarget per node in namespace guacamole with principal catalyst. Dropped from the plan under the default answer (a). | ACC-131, SOV-720, FND-011 | Makes ACC-G24 required. |
| **ACC-260**<br>delete, 3 days | **Remove the shared-secret leg of the exec proxy**<br>The shared-secret leg, its bootstrap Job, the old client certificate and the plain listener are gone; the access certificate is the only credential. Not before two release trains are green after ACC-123. | ACC-123, FND-011 | stated in its ticket |
| **ACC-245**<br>new, 5 days | **Machines as SSH targets**<br>An SSH target may name a Machine of its own namespace in the optional field ssh.machineRef; kind ssh reads the host address from that Machine and writes nothing to it (read verbs only on Machine). The virtual-machine unit module of both packs gains one variable that lists the logins an access session may use and writes their principal files at first boot. Locks: crds and the provider module. The Machine field that carries the address, the source of the variable's value and the capability flag of the check are written by the architect after INF-110 and INF-420 have merged. | ACC-131, INF-110, INF-420, FND-011 | Makes ACC-G26 required. |
| **ACC-250**<br>new, 5 days | **Regional brokers**<br>Regional brokers. | ACC-133, NET-184, FND-011 | Makes ACC-G25 required. |

<details><summary>Files each task of this group changes</summary>

- **ACC-230**: `core/controllers/access/internal/broker/recordings.go`, `core/controllers/access/web/src/player.ts`, `products/catalyst/bootstrap/ui/src/pages/sovereign/access/AccessSessionsPage.tsx`, `core/controllers/access/internal/events/events.go`, `docs/contracts/C3-event-subjects.yaml`, `products/catalyst/bootstrap/ui/e2e/gate/access/acc-g23.spec.ts`, `docs/ledger/gates.yaml`
- **ACC-240**: `platform/guacamole/chart/values.yaml`
- **ACC-260**: `core/cmd/k8s-ws-proxy/internal/auth/hmac.go`, `core/cmd/k8s-ws-proxy/internal/auth/hmac_test.go`, `core/cmd/k8s-ws-proxy/internal/auth/authorize.go`, `core/cmd/k8s-ws-proxy/internal/runtime/config.go`, `platform/k8s-ws-proxy/chart/templates/hmac-bootstrap-job.yaml`, `platform/k8s-ws-proxy/chart/templates/certs.yaml`, `platform/k8s-ws-proxy/chart/values.yaml`, `clusters/_template/bootstrap-kit/51-bp-k8s-ws-proxy.yaml`, `docs/ledger/deleted-paths.txt`
- **ACC-245**: `platform/guacamole/chart/crds/accesstarget.yaml`, `core/controllers/access/api/v1alpha1/types.go`, `core/controllers/access/internal/kinds/ssh.go`, `core/controllers/access/internal/controller/target_controller.go`, `platform/guacamole/chart/templates/controller-rbac.yaml`, `infra/providers/huawei/day2/virtualmachine/**`, `infra/providers/local/day2/virtualmachine/**`, `products/catalyst/bootstrap/ui/e2e/gate/access/acc-g26.spec.ts`, `docs/ledger/gates.yaml`
- **ACC-250**: `platform/guacamole/chart/values.yaml`, `platform/guacamole/chart/crds/accesstarget.yaml`, `core/controllers/access/internal/kinds/`, `core/controllers/access/web/src/client.ts`, `clusters/_template/bootstrap-kit/13c-bp-oidc-gate.yaml`

</details>

### Group ACC-210: Epic: web and graphical workbenches (order: ACC-211, ACC-212, ACC-213)

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **ACC-211**<br>new, 5 days | **Kind web: a recorded browser per session**<br>Target kind web (url, allowedHosts); Chromium in kiosk mode on a virtual display reached by guacd over VNC on loopback; egress limited to allowedHosts on 443. | ACC-020, ACC-133, FND-011 | Makes ACC-G11 required. |
| **ACC-212**<br>new, 3 days | **Sign-in fill for web targets**<br>The agent fills the sign-in form once through the browser's local debugging port from mounted files and closes the port; the browser container never mounts the credential. | ACC-211, FND-011 | Makes ACC-G21 required. |
| **ACC-213**<br>new, 3 days | **Graphical database tool**<br>Graphical database tool. | ACC-132, ACC-211, FND-011 | Makes ACC-G22 required. |

<details><summary>Files each task of this group changes</summary>

- **ACC-211**: `platform/guacamole/chart/crds/accesstarget.yaml`, `core/controllers/access/api/v1alpha1/types.go`, `core/controllers/access/internal/kinds/web.go`, `core/cmd/access-session-agent/images/browser/Containerfile`, `.github/workflows/build-access-session-agent.yaml`, `platform/guacamole/chart/values.yaml`, `tests/acceptance/sovereign/fixtures/access/web-target.yaml`, `products/catalyst/bootstrap/ui/e2e/gate/access/acc-g11.spec.ts`, `docs/ledger/gates.yaml`
- **ACC-212**: `platform/guacamole/chart/crds/accesstarget.yaml`, `core/controllers/access/api/v1alpha1/types.go`, `core/cmd/access-session-agent/internal/webfill/`, `core/controllers/access/internal/kinds/web.go`, `products/catalyst/bootstrap/ui/e2e/gate/access/acc-g21.spec.ts`, `docs/ledger/gates.yaml`
- **ACC-213**: `platform/guacamole/chart/crds/accesstarget.yaml`, `core/controllers/access/internal/kinds/database.go`, `core/cmd/access-session-agent/images/tool-db-desktop/Containerfile`, `.github/workflows/build-access-session-agent.yaml`, `platform/guacamole/chart/values.yaml`, `products/catalyst/bootstrap/ui/e2e/gate/access/acc-g22.spec.ts`, `docs/ledger/gates.yaml`

</details>

### Group ACC-220: Epic: Windows and RDP (order: ACC-221, ACC-222)

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **ACC-221**<br>lab test, 3 days | **Spike: RDP with a directory account checked out for the session**<br>PASS or FAIL per step: OpenBao LDAP engine with schema ad, check-out through a mount object, guacd rdp with network-level authentication, check-in and rotation, redirection off. | ACC-133 | stated in its ticket |
| **ACC-222**<br>new, 5 days | **Kind rdp**<br>Target kind rdp (host, port, domain, library); steps written by the architect from the result of ACC-221. | ACC-221, FND-011 | Makes ACC-G12 required. |

<details><summary>Files each task of this group changes</summary>

- **ACC-221**: `docs/ledger/plan/spikes/ACC-221.md`
- **ACC-222**: `platform/guacamole/chart/crds/accesstarget.yaml`, `core/controllers/access/api/v1alpha1/types.go`, `core/controllers/access/internal/kinds/rdp.go`, `platform/openbao/chart/templates/_config.tpl`, `tests/acceptance/sovereign/fixtures/access/rdp-target.yaml`, `products/catalyst/bootstrap/ui/e2e/gate/access/acc-g12.spec.ts`, `docs/ledger/gates.yaml`

</details>

## PRD Later products

### Single tasks

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **PRD-070**<br>lab test, 3 days | **Spike: chat front end forwards the user's token**<br>Whether the pinned chat interface forwards the signed-in user's access token (A) or not (B). | nothing else | stated in its ticket |
| **PRD-080**<br>lab test, 5 days | **Spike: CPU images for embeddings, rerank, document conversion and transcription**<br>Per task: pinned image and model with licence, digest, hardening and throughput, or 'not offered before GPUs'. | nothing else | stated in its ticket |
| **PRD-090**<br>lab test, 5 days | **Spike: GPU node on the National Cloud gate environment**<br>Driver route, policy exceptions and cold load time for the GPU operator on the platform's node image, with a tool-calling completion. | CLD-175 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **PRD-070**: `docs/ledger/plan/spikes/PRD-070-*.md`
- **PRD-080**: `docs/ledger/plan/spikes/PRD-080-*.md`
- **PRD-090**: `docs/ledger/plan/spikes/PRD-090-*.md`

</details>

