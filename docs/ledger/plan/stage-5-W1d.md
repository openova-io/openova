# Stage 5 (W1d): Contracts

Provider contract, service model, generated catalogues, second-provider proof.

51 tasks. One task is one pull request. Rows are in the order the tasks can run. "Waits for" lists the tasks that must be merged first; every task also waits for the first two tasks of the plan, `FND-021` and `FND-022`.

[Back to the overview](README.md)

## Areas in this stage

- [CLD: Cloud abstraction](#cld-cloud-abstraction) — 25 tasks
- [SOV: Sovereign lifecycle](#sov-sovereign-lifecycle) — 1 tasks
- [SEC: Secrets and root of trust](#sec-secrets-and-root-of-trust) — 1 tasks
- [NET: Network, zones and cluster classes](#net-network-zones-and-cluster-classes) — 6 tasks
- [SVC: Service model](#svc-service-model) — 16 tasks
- [INF: Infrastructure plane](#inf-infrastructure-plane) — 2 tasks

## CLD Cloud abstraction

### Single tasks

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **CLD-075**<br>document, 3 days | **Generator for the provider and profile tables**<br>scripts/generators/providers.py with --check. It reads every pack.yaml and profile.yaml and writes docs/generated/providers.md. It runs in FND's workflow check-generated-docs.yaml. The output file is added to generated-files.txt. | CLD-080, FND-102 | stated in its ticket |
| **CLD-076**<br>document, 3 days | **Documents link to the generated provider tables**<br>Each hand-written provider table the ticket lists is replaced by a link to docs/generated/providers.md. Only the files the ticket lists are changed; the touches above are the candidates. | CLD-075 | stated in its ticket |
| **CLD-092**<br>test, 3 days | **Seal hook conformance and the profile's seal section**<br>spec.seal.kind (static-node-file or cloud-key-service) required by the schema and set in every profile. TestProviderPacks asserts: the seal/ hook declares exactly the inputs and outputs of contract C8; the profile's kind equals the hook's kind; the module declares random_password.k3s_server_token and random_password.k3s_agent_token; hashicorp/random is in pack.yaml tofuProviders. | CLD-090, SEC-020, SEC-150 | stated in its ticket |
| **CLD-190**<br>delete, 5 days | **Retire Hetzner (only if CLD-182 fails or the founder chooses retirement)**<br>Hetzner pack and its charts, workflows and committed trees deleted. The value controller is removed from the schema. | CLD-182 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **CLD-075**: `scripts/generators/providers.py`, `docs/generated/providers.md`, `infra/providers/_contract/generated-files.txt`
- **CLD-076**: `docs/ARCHITECTURE.md`, `docs/STATUS.md`, `docs/RUNBOOKS.md`, `platform/opentofu/README.md`, `platform/crossplane/README.md`, `infra/providers/PROVIDER-INTERFACE.md`
- **CLD-092**: `infra/providers/_contract/cloudprofile.schema.json`, `infra/providers/*/profile.yaml`, `infra/providers/*/pack.yaml`, `products/catalyst/bootstrap/api/internal/cloudprofile/**`, `products/catalyst/bootstrap/api/internal/providers/conformance/conformance_test.go`
- **CLD-190**: `infra/providers/hetzner/**`, `products/catalyst/bootstrap/api/internal/providers/hetzner/**`, `products/catalyst/bootstrap/api/internal/hetzner/**`, `clusters/_template/providers/hetzner/**`, `infra/providers/_contract/cloudprofile.schema.json`, `docs/ledger/deleted-paths.txt`

</details>

### Group CLD-110: Epic: handlers and controllers stop naming providers

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **CLD-114**<br>rework, 3 days | **Adoption map, name lists and schema enums**<br>AdoptionMapper; providers.Names() replaces lists; Organization controller reads CLOUD_PROVIDER; provider enums become patterns | CLD-102, CLD-113 | stated in its ticket |
| **CLD-112**<br>rework, 3 days | **Credential check from the profile; one registry**<br>Fields validated against the profile; objectstorage package folded into ObjectStorageValidator | CLD-103 | stated in its ticket |
| **CLD-111**<br>rework, 5 days | **Wipe and janitor through capabilities**<br>OrphanSweeper and LivenessProber; type assertion, provider inference and fallback regions deleted | CLD-105 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **CLD-114**: `products/catalyst/bootstrap/api/internal/provisioner/adoption.go`, `products/catalyst/bootstrap/api/internal/jobs/types.go`, `core/controllers/organization/**`
- **CLD-112**: `products/catalyst/bootstrap/api/internal/handler/credentials.go`, `products/catalyst/bootstrap/api/internal/objectstorage/**`
- **CLD-111**: `products/catalyst/bootstrap/api/internal/providers/huawei/**`

</details>

### Group CLD-130: Epic: the shared template without provider tests

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **CLD-133**<br>fix, 1 day | **Secondary-region suspend keys are set for every provider**<br>SECONDARY_HR_SUSPEND and SECONDARY_EDGE_SUSPEND computed in the shared list | CLD-132 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **CLD-133**: `infra/providers/_shared/cloudinit-control-plane.tftpl`, `infra/providers/huawei/main.tf`, `infra/providers/_shared/tests/golden.auto.tfvars.json`

</details>

### Group CLD-140: Epic: provider overlays, one overlay path

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **CLD-145**<br>fix, 3 days | **Give live Sovereigns their provider-pack Kustomization**<br>Migration script with dry run, run by lane A on each live Sovereign following main | CLD-144 | stated in its ticket |
| **CLD-143**<br>rework, 3 days | **One Velero chart**<br>Checksum algorithm from CLOUD_OBJECT_CHECKSUM_ALGORITHM; fork chart and slot 34a deleted | CLD-145, FND-011 | stated in its ticket |
| **CLD-146**<br>rework, 3 days | **Hetzner values leave the shared network slot; storage driver name from the profile**<br>provider-values-cilium ConfigMap; per-overlay sovereign-tls kustomization; SeaweedFS provisioner from CLOUD_STORAGE_DRIVER | CLD-023, CLD-145, FND-011 | stated in its ticket |
| **CLD-147**<br>guard, 1 day | **Remove the prune protection**<br>Annotation removed from the four moved releases | CLD-145 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **CLD-145**: `scripts/migrate-sovereign-to-provider-pack.sh`
- **CLD-143**: `platform/velero/chart/values.yaml`, `platform/velero-hcs/**`, `scripts/expected-bootstrap-deps.yaml`, `scripts/sovereign-dod-verify.sh`
- **CLD-146**: `clusters/_template/providers/*/sovereign-tls/**`, `clusters/_template/providers/hetzner/values/**`, `clusters/_template/sovereign-tls-hetzner/**`, `platform/seaweedfs/chart/**`, `infra/providers/_shared/cloudinit-control-plane.tftpl`

</details>

### Group CLD-100: Epic: the engine drives the port

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **CLD-105**<br>rework, 3 days | **Hook variables become capability interfaces**<br>Preflighter, PostApplier, ApplyErrorClassifier on the Huawei adapter; three global hooks deleted | CLD-104 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **CLD-105**: `products/catalyst/bootstrap/api/internal/providers/huawei/**`

</details>

### Group CLD-120: Epic: providers route; the console reads the profile

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **CLD-122**<br>rework, 5 days | **The wizard renders providers, credentials, regions and flavors from the profile**<br>Wizard driven by the providers routes; TypeScript catalogues deleted | CLD-121 | stated in its ticket |
| **CLD-123**<br>rework, 3 days | **Wipe, decommission and cloud pages without provider names**<br>Credential inputs from the profile; generic purge report; no hetzner fall-backs | CLD-111, CLD-122 | stated in its ticket |
| **CLD-124**<br>delete, 1 day | **Remove the legacy credential aliases**<br>LegacyAliaser and Request.UnmarshalJSON deleted one train after the console change | CLD-123, FND-014 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **CLD-122**: `products/catalyst/bootstrap/ui/src/lib/providers.api.ts`, `products/catalyst/bootstrap/ui/src/entities/deployment/model.ts`, `products/catalyst/bootstrap/ui/src/shared/constants/providerSizes.ts`, `products/catalyst/bootstrap/ui/src/pages/wizard/steps/*.tsx`, `products/catalyst/bootstrap/ui/src/widgets/cloud-provider-card/**`
- **CLD-123**: `products/catalyst/bootstrap/ui/src/pages/sovereign/cloud-compute/*.tsx`, `products/catalyst/bootstrap/ui/src/pages/sovereign/cloud-list/kinds.ts`
- **CLD-124**: `products/catalyst/bootstrap/api/internal/provisioner/provisioner.go`, `products/catalyst/bootstrap/api/internal/handler/wipe.go`, `docs/ledger/deleted-paths.txt`

</details>

### Group CLD-150: Epic: Huawei cloud controller inside the Sovereign

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **CLD-151**<br>rework, 3 days | **Huawei plug-in chart; the route job leaves the shared network chart**<br>Chart bp-huawei-cloud-controller with the route job moved unchanged; Huawei wiring removed from the Cilium chart and slot | CLD-147, CLD-162, FND-011 | stated in its ticket |
| **CLD-152**<br>new, 5 days | **cloud-controller: load-balancer members and route runs follow Node events**<br>As the body. The controller writes ConfigMap cloud-controller-status after every reconcile. If go.mod has no controller-runtime requirement, add it at the version the ticket names; if it has one, change nothing (the same step as INF-110). | CLD-111, CLD-151, FND-032, FND-011 | Makes CLD-G08 required. |

<details><summary>Files each task of this group changes</summary>

- **CLD-151**: `platform/huawei-cloud-controller/**`, `clusters/_template/providers/huawei/**`, `platform/cilium/chart/templates/vpc-podcidr-route-reconciler.yaml`, `platform/cilium/chart/values.yaml`, `clusters/_template/bootstrap-kit/01-cilium.yaml`
- **CLD-152**: `products/catalyst/bootstrap/api/cmd/cloud-controller/**`, `products/catalyst/bootstrap/api/Containerfile`, `platform/huawei-cloud-controller/chart/templates/**`, `products/catalyst/bootstrap/api/go.mod`, `tests/acceptance/sovereign/internal/suite/check_*.go`

</details>

### Group CLD-160: Epic: installation parameters; installation literals leave the code

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **CLD-163**<br>rework, 3 days | **Installation literals leave shared charts and scripts**<br>Adoption Composition and four scripts take domain and region as required inputs. The billing engine is not touched. | CLD-162 | stated in its ticket |
| **CLD-165**<br>rework, 1 day | **Billing engine: the cloud endpoint is a required setting**<br>Billing engine: the cloud endpoint is a required setting. | CLD-162, CLD-163, FND-011 | stated in its ticket |
| **CLD-164**<br>guard, 3 days | **Installation-literal check**<br>Two-tier check: zero in code, falling baseline in documents and tests. It starts from the list SOV-540 reports and does not edit the cutover chart. | CLD-151, CLD-163, CLD-165, FND-031, SOV-540 | Makes CLD-G13 required. |

<details><summary>Files each task of this group changes</summary>

- **CLD-163**: `clusters/_template/infrastructure/base/adoption-claims.yaml`
- **CLD-165**: `products/chargeback/internal/config/config.go`, `products/chargeback/chart/values.yaml`, `clusters/_template/bootstrap-kit/13f-bp-chargeback.yaml`
- **CLD-164**: `scripts/check-installation-literals.sh`, `scripts/installation-literal-baseline.tsv`, `.github/workflows/check-provider-leaks.yaml`

</details>

### Group CLD-180: Epic: second-provider proof, Hetzner single region

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **CLD-181**<br>fix, 3 days | **Hetzner pack: inventory by name prefix**<br>Contract name prefix on Hetzner resources; ListServers and LivenessProber implemented | CLD-111, CLD-146 | stated in its ticket |
| **CLD-183**<br>fix, 3 days | **Second provider: dedicated seal volume on control-plane nodes**<br>One dedicated volume per control-plane node for the seal key and a non-empty volume_device in the pack's seal hook. Frame 10.3 item 27 makes this a condition of the re-proof. Holds lock provider-main. | CLD-181, SEC-150 | stated in its ticket |
| **CLD-182**<br>test, 5 days | **Re-prove Hetzner, single region**<br>Time-boxed walk (10 working days, three provisions); lifecycle proven on a pass | CLD-088, CLD-123, CLD-181, CLD-183 | Makes CLD-G14 required. |

<details><summary>Files each task of this group changes</summary>

- **CLD-181**: `products/catalyst/bootstrap/api/internal/providers/hetzner/**`
- **CLD-183**: `infra/providers/hetzner/main.tf`, `infra/providers/hetzner/seal/**`, `infra/providers/hetzner/tests/**`
- **CLD-182**: `infra/providers/hetzner/profile.yaml`

</details>

### Group CLD-210: Epic: ratchet the leak guard to zero

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **CLD-212**<br>guard, 3 days | **Hard-zero directories; retire the old guard**<br>Hard-zero list for API, console, shared template, shared kit, core, Cilium and claims charts; old guard deleted | CLD-114, CLD-123, CLD-146, CLD-152, CLD-164 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **CLD-212**: `scripts/check-provider-leaks.py`, `scripts/provider-leak-baseline.tsv`, `scripts/check-vendor-coupling.sh`, `.github/workflows/check-vendor-coupling.yaml`

</details>

## SOV Sovereign lifecycle

### Group SOV-700: EPIC: Day-2 with the parent switched off

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **SOV-765**<br>new, 5 days | **Ring switches by commit**<br>The kit's Flux Kustomization in the Sovereign's repository carries one delete patch per disabled ring, in the form spike SVC-210 records. The list is one contract key of C5, RINGS_DISABLED_JSON (default []), read by cloud-init at first boot and by seed-repo. Route PUT /api/v1/sovereign/rings changes the key and the rendered Kustomization in one commit. The create request accepts ringsDisabled. | CTL-035, CTL-110, FND-032, FND-120, SOV-130, SOV-170, SVC-220, NET-031 | Makes SOV-G18 required. |

<details><summary>Files each task of this group changes</summary>

- **SOV-765**: `infra/providers/_shared/cloudinit-control-plane.tftpl`, `infra/providers/_shared/tests/cloudinit_render.tftest.hcl`, `core/cmd/sovereign-ops/**`, `docs/contracts/c5-upstream-variables.schema.json`, `core/controllers/pkg/upstream/**`, `products/catalyst/bootstrap/api/internal/lifecycle/rings.go`, `products/catalyst/bootstrap/api/cmd/api/**`, `products/catalyst/bootstrap/api/internal/provisioner/provisioner.go`, `docs/contracts/C9-mutating-routes.yaml`, `tests/acceptance/sovereign/internal/suite/check_ring_switch.go`

</details>

## SEC Secrets and root of trust

### Single tasks

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **SEC-198**<br>rework, 3 days | **Remove the transitional cloud-credential Secrets**<br>The class T register entries of the cloud provisioning credential, the object-storage key of the state back end, the operator-supplied cloud credentials and the node join token are removed, with the Secret objects no consumer reads any more. CLD-106 and CLD-171 write the mount definitions; this package only removes. The step for the join token waits for the decision listed under unsettled. | CLD-106, CLD-171, CLD-173, SEC-095 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **SEC-198**: `scripts/secret-guard-baselines/secret-object-register.yaml`

</details>

## NET Network, zones and cluster classes

### Single tasks

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **NET-210**<br>rework, 3 days | **Availability zone per region and a separation output**<br>Optional availability_zone and cloud_region per region; output region_separation (none, zone, site) | CLD-090, NET-100 | stated in its ticket |
| **NET-212**<br>new, 3 days | **Show the separation; refuse the claim when it is none**<br>status.regionSeparation and condition; console notice; gate record line; disaster-recovery checks not applicable when none | NET-102, NET-210, FND-011 | Makes NET-G12 required. |

<details><summary>Files each task of this group changes</summary>

- **NET-210**: `infra/providers/huawei/variables.tf`, `infra/providers/huawei/main.tf`, `infra/providers/huawei/outputs.tf`, `infra/providers/huawei/tests/multi_region.tftest.hcl`, `infra/providers/hetzner/outputs.tf`
- **NET-212**: `products/catalyst/chart/crds/sovereign.yaml`, `products/catalyst/bootstrap/ui/src/pages/wizard/steps/StepProvider.tsx`, `tests/acceptance/sovereign/internal/suite/check_region_separation.go`, `scripts/acceptance-gate.py`

</details>

### Group NET-220: Epic: placement contract without tiers

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **NET-224**<br>delete, 3 days | **API without the tier gate**<br>Tier gate, CATALYST_PLACEMENT_VCLUSTER_TIERS, vClusterHostSyncNamespaces and the vcluster-role join deleted | NET-223 | stated in its ticket |
| **NET-226**<br>delete, 1 day | **Organization controller stops writing buildingBlock**<br>buildingBlock default and CATALYST_ENV_REGION_BUILDING_BLOCK deleted | NET-223 | stated in its ticket |
| **NET-227**<br>delete, 3 days | **Narrow the kinds**<br>Placement and tier enums narrowed to host and empty; identifier pattern without tier; buildingBlock deprecated | NET-222, NET-223, NET-224, NET-226, FND-011 | stated in its ticket |
| **NET-228**<br>delete, 3 days | **Cutover branches, generator branch and stale comments**<br>Plane branches of cutover step 10, the pivot branch of the placement renderer and stale zone comments deleted; carve-out renamed | NET-227, SOV-240, FND-011 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **NET-224**: `products/catalyst/bootstrap/api/internal/instances/create.go`, `products/catalyst/bootstrap/api/internal/handler/applications.go`, `products/catalyst/bootstrap/api/internal/handler/dashboard.go`
- **NET-226**: `core/controllers/organization/internal/controller/environment_ensure.go`, `core/controllers/organization/cmd/main.go`
- **NET-227**: `products/catalyst/chart/crds/blueprint.yaml`, `products/catalyst/chart/crds/application.yaml`, `products/catalyst/chart/crds/environment.yaml`, `products/catalyst/chart/crds/organization.yaml`, `products/catalyst/chart/crds/tests/`
- **NET-228**: `platform/self-sovereign-cutover/chart/templates/00-mgmt-isolation-carveout.yaml`, `platform/self-sovereign-cutover/chart/values.yaml`, `scripts/render-slot-placement.py`, `clusters/_template/bootstrap-kit/placement.yaml`, `clusters/_template/bootstrap-kit/*.yaml`, `infra/providers/huawei/main.tf`, `infra/providers/hetzner/README.md`

</details>

## SVC Service model

### Single tasks

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **SVC-315**<br>lab test, 3 days | **Spike: create a managed PostgreSQL instance through the API on the National Cloud**<br>Result record: yes or no on creating, reading and deleting one managed PostgreSQL instance through the cloud API with the Sovereign's cloud account, and what the operator must allow. Yes: epic SVC-310 starts and CLD-222 may be scheduled. No: the epic does not start and no postgres contract is written. | nothing else | stated in its ticket |
| **SVC-220**<br>rework, 3 days | **Ring labels on the kit**<br>Ring label on every slot file that exists, per the membership table plus these rows: 81, 82, 83, 42, 84, 85 are ai; slot 38 stays reserved. A slot file with no row stops the package. Ring field in the catalogue, regenerated files committed. The switch test computes its numbers from the tree. It stops if the pinned Flux version differs from the record of SVC-210. | SVC-150, SVC-210 | stated in its ticket |
| **SVC-162**<br>delete, 3 days | **Shelf deletion: README-only folders and scaffold Blueprint files**<br>Fifteen README-only folders and their nine scaffold blueprint.yaml files are deleted; one roadmap entry is written for each. products/cortex and products/fingate are not touched and get the two entries the later-products record gives. platform/stunner is not touched. Baselines of SVC-020 that count these files are lowered. | FND-091, FND-102, SVC-160, FND-011 | stated in its ticket |
| **SVC-164**<br>delete, 3 days | **Delete the OpenMeter Blueprint**<br>The folder platform/openmeter (10 files) and what lists it in the wizard, the logo copy step and the catalogue are gone; the regenerated catalogue files are committed. Comment lines in kit slot files and in other charts are left. Precondition: git grep -l -i openmeter outside docs and outside the folder prints 23 files at 2c1f5b0c0. Stop condition: a hit is Go code that calls OpenMeter at run time. | FND-091, SVC-160, FND-011 | stated in its ticket |
| **SVC-166**<br>rework, 1 day | **The umbrella chart's Blueprint file moves beside its chart**<br>platform/catalyst-platform/blueprint.yaml becomes products/catalyst/blueprint.yaml. spec.version is set once to the version of products/catalyst/chart/Chart.yaml at that commit (a version line in an added file is allowed); the release writer keeps it afterwards. The Blueprint stays seeded. The regenerated files are committed. | FND-091, SVC-160, FND-011 | stated in its ticket |
| **SVC-205**<br>guard, 1 day | **Lock stage blueprints to deny**<br>intentLock.stages.blueprints is deny; create, update, patch and delete on blueprints are removed from every non-Flux role of the chart; the stage is added to lock-stages.json. Precondition: in each of the two newest gate records the lock report shows an empty nonFluxSpecManagers and auditCount 0 or null for stage blueprints. | CTL-150, CTL-240, SVC-160, SVC-190, FND-011 | stated in its ticket |
| **SVC-180**<br>rework, 5 days | **Screens group by class; the dead branch goes**<br>Class grouping on Applications and Catalog; Platform page for platform components; isServiceApp deleted | SVC-170 | stated in its ticket |
| **SVC-230**<br>rework, 5 days | **The wizard shows the generated kit list**<br>Read-only ring list generated from the kit; hand list of 58 and product families deleted | FND-091, SVC-180, SVC-220 | stated in its ticket |
| **SVC-350**<br>fix, 1 day | **Promote proven Blueprints to supported**<br>lifecycle: supported on exactly the proven Blueprints | SVC-130, SVC-135, SVC-170, FND-011 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **SVC-315**: `docs/ledger/plan/spikes/SVC-315.md`
- **SVC-220**: `scripts/generate-catalog.py`, `scripts/check-service-model.py`, `scripts/tests/test_ring_switch.sh`, `products/catalyst/bootstrap/api/internal/catalog/blueprints.json`, `products/catalyst/bootstrap/ui/src/shared/constants/catalog.generated.ts`
- **SVC-162**: `platform/clickhouse/**`, `platform/debezium/**`, `platform/failover-controller/**`, `platform/ferretdb/**`, `platform/flink/**`, `platform/iceberg/**`, `platform/keda/**`, `platform/milvus/**`, `platform/neo4j/**`, `platform/opensearch/**`, `platform/opentofu/**`, `platform/stalwart/**` and 6 more
- **SVC-164**: `platform/openmeter/**`, `products/catalyst/bootstrap/ui/src/pages/wizard/steps/componentGroups.ts`, `products/catalyst/bootstrap/ui/src/pages/wizard/steps/componentFootprints.ts`, `products/catalyst/bootstrap/ui/src/pages/wizard/steps/componentLogos.tsx`, `products/catalyst/bootstrap/ui/src/pages/wizard/steps/logoTone.ts`, `products/catalyst/bootstrap/ui/src/pages/wizard/steps/StepComponents.test.tsx`, `products/catalyst/bootstrap/ui/src/entities/deployment/model.ts`, `products/catalyst/bootstrap/ui/src/pages/marketplace/marketplaceCopy.ts`, `products/catalyst/bootstrap/ui/src/pages/sovereign/AppDetail/InstancesSection.topology-gate.test.tsx`, `.github/workflows/catalyst-build.yaml`, `products/catalyst/bootstrap/api/internal/catalog/blueprints.json`, `products/catalyst/bootstrap/ui/src/shared/constants/catalog.generated.ts` and 3 more
- **SVC-166**: `platform/catalyst-platform/blueprint.yaml`, `products/catalyst/blueprint.yaml`, `products/catalyst/bootstrap/api/internal/catalog/blueprints.json`, `products/catalyst/bootstrap/ui/src/shared/constants/catalog.generated.ts`, `products/catalyst/chart/templates/catalog-seed/blueprints.yaml`, `docs/ledger/deleted-paths.txt`
- **SVC-205**: `products/catalyst/chart/values.yaml`, `scripts/baselines/lock-stages.json`
- **SVC-180**: `products/catalyst/bootstrap/ui/src/shared/constants/classes.ts`
- **SVC-230**: `scripts/generate-catalog.py`, `products/catalyst/bootstrap/ui/src/pages/wizard/steps/componentGroups.generated.ts`, `docs/ledger/deleted-paths.txt`
- **SVC-350**: `products/catalyst/bootstrap/api/internal/catalog/blueprints.json`, `products/catalyst/bootstrap/ui/src/shared/constants/catalog.generated.ts`, `products/catalyst/chart/templates/catalog-seed/blueprints.yaml`

</details>

### Group SVC-050: Epic: function tests for the foundation ring

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **SVC-110**<br>test, 1 day | **Function tests: topology charts (after the topology freeze)**<br>Existing pair test renamed to the convention; one Continuum test. Precondition: python3 scripts/plan.py state FND-120 --no-fetch prints merged. The pull request carries the trailer Decision: ADR-0020. | FND-120, SVC-010, SVC-060, FND-011 | stated in its ticket |
| **SVC-140**<br>test, 1 day | **Applications get the test stanza (after the topology freeze)**<br>Every HelmRelease rendered for an Application runs its function test. Precondition: python3 scripts/plan.py state FND-120 --no-fetch prints merged. Trailer Decision: ADR-0020. | FND-120, SVC-010, SVC-130 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **SVC-110**: `platform/cnpg-pair/chart/templates/tests/`, `products/continuum/chart/templates/tests/function-test.yaml`
- **SVC-140**: `core/controllers/application/internal/render/fanout.go`, `core/controllers/application/internal/render/fanout_test.go`

</details>

### Group SVC-240: Epic: first capability with two implementations, object storage

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **SVC-250**<br>lab test, 3 days | **Spike: composite resources in a release; bucket provisioning for an Organization**<br>Result record for (a) composite resource per Context under Flux and (b) bucket and scoped key on the kit SeaweedFS without an administrator credential in the Organization namespace | CLD-065, CLD-066 | stated in its ticket |
| **SVC-260**<br>new, 5 days | **provides, requires, the contract format, the registry and the install API**<br>Fields, capability schema, capability index output, registry package, BackingSelection by capability with canonical Context entry and spec.parameters.bindings | CLD-065, SEC-190, SEC-211, SVC-170, SVC-250, FND-011 | Adds the check SVC-G02. |
| **SVC-270**<br>new, 5 days | **In-cluster object storage and the capability probe**<br>object-storage contract v1; bp-object-bucket (bucket and scoped key per Context, keys written to the binding path); probe image and bp-capability-probe consumer with mounted binding | SEC-192, SVC-260, FND-011 | stated in its ticket |
| **SVC-275**<br>new, 3 days | **Platform buckets: one kit instance of bp-object-bucket**<br>Platform buckets: one kit instance of bp-object-bucket. | SVC-270 | stated in its ticket |
| **SVC-280**<br>new, 5 days | **The dialog offers the implementation; capabilities routes**<br>GET /api/v1/catalog/capabilities and /{name}; one dialog block per requires entry with implementation picker and create or reuse | SVC-270 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **SVC-260**: `products/catalyst/chart/crds/blueprint.yaml`, `platform/_schemas/blueprint.schema.json`, `platform/_schemas/capability.schema.json`, `scripts/generate-catalog.py`, `products/catalyst/bootstrap/api/internal/capability/`, `products/catalyst/bootstrap/api/internal/catalog/catalog.go`, `products/catalyst/bootstrap/api/internal/instances/create.go`, `products/catalyst/bootstrap/api/internal/handler/endpoint_handler.go`, `products/catalyst/bootstrap/api/internal/catalog/capabilities.json`, `products/catalyst/bootstrap/api/internal/catalog/blueprints.json`, `products/catalyst/bootstrap/ui/src/shared/constants/catalog.generated.ts`
- **SVC-270**: `platform/_capabilities/object-storage.yaml`, `platform/object-bucket/`, `core/cmd/capability-probe/`, `​.github/workflows/build-capability-probe.yaml`, `platform/capability-probe/`, `products/catalyst/bootstrap/api/internal/catalog/blueprints.json`, `products/catalyst/bootstrap/api/internal/catalog/capabilities.json`, `products/catalyst/bootstrap/ui/src/shared/constants/catalog.generated.ts`, `products/catalyst/chart/templates/catalog-seed/blueprints.yaml`
- **SVC-275**: `clusters/_template/bootstrap-kit/kustomization.yaml`, `clusters/_template/bootstrap-kit/placement.yaml`, `scripts/expected-bootstrap-deps.yaml`, `products/catalyst/bootstrap/api/internal/catalog/blueprints.json`, `products/catalyst/bootstrap/ui/src/shared/constants/catalog.generated.ts`
- **SVC-280**: `products/catalyst/bootstrap/api/internal/handler/catalog_capabilities.go`, `products/catalyst/bootstrap/ui/src/pages/sovereign/AppDetail/InstancesSection.tsx`

</details>

## INF Infrastructure plane

### Single tasks

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **INF-055**<br>fix, 1 day | **Leave the kured slot out of the local reduced kit**<br>One line 04b-bp-kured in the exclusion list of the local reduced kit, and the regenerated kit committed in the same pull request. | CLD-086, INF-050 | stated in its ticket |

### Group INF-200: Epic: virtual machines (INF-210, 220, 230, 240, 250, 260)

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **INF-210**<br>lab test, 3 days | **Spike: composite resource in an Organization's release, changed placement, moved Primary**<br>Four yes/no answers (A host cluster, B placement change refused, C keep leaves the resource, D Primary move leaves the resource) | CLD-087, SVC-250 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **INF-210**: `docs/ledger/plan/spikes/INF-210.md`

</details>

