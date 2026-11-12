# Stage 3 (W1b): Born-local Sovereign

Local from first boot, hand-over to Crossplane, in-place upgrade, child provisioning.

91 tasks. One task is one pull request. Rows are in the order the tasks can run. "Waits for" lists the tasks that must be merged first; every task also waits for the first two tasks of the plan, `FND-021` and `FND-022`.

[Back to the overview](README.md)

## Areas in this stage

- [FND: Foundation gate and execution rules](#fnd-foundation-gate-and-execution-rules) — 3 tasks
- [CLD: Cloud abstraction](#cld-cloud-abstraction) — 26 tasks
- [SOV: Sovereign lifecycle](#sov-sovereign-lifecycle) — 33 tasks
- [SEC: Secrets and root of trust](#sec-secrets-and-root-of-trust) — 2 tasks
- [NET: Network, zones and cluster classes](#net-network-zones-and-cluster-classes) — 4 tasks
- [CTL: Control plane](#ctl-control-plane) — 10 tasks
- [CMP: Compliance and posture](#cmp-compliance-and-posture) — 13 tasks

## FND Foundation gate and execution rules

### Group FND-160: Console regression as code

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **FND-161**<br>test, 5 days | **Row map and browser harness**<br>Every ledger row has one disposition, and a browser harness runs any check whose implementation is a folder of specifications. | FND-036, FND-120 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **FND-161**: `docs/ledger/uat-row-map.csv`, `products/catalyst/bootstrap/ui/e2e/gate/**`, `.github/workflows/foundation-gate.yaml`, `scripts/check-uat-row-map.py`

</details>

### Group FND-140: The release train as a workflow

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **FND-142**<br>test, 3 days | **The upgrade leg**<br>The second fire of every train installs the previous release and upgrades it in place to the candidate, and a check proves it. | FND-141, FND-144, SOV-510, SOV-520 | Makes FND-G14 required. |

<details><summary>Files each task of this group changes</summary>

- **FND-142**: `.github/workflows/release-train.yaml`, `tests/acceptance/sovereign/internal/suite/check_upgrade*.go`, `tests/acceptance/sovereign/internal/suite/result.go`, `tests/acceptance/sovereign/cmd/sovereign-acceptance/main.go`, `scripts/acceptance-gate.py`, `docs/ledger/gates.yaml`

</details>

### Group FND-150: Self-conformance in the Sovereign

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **FND-152**<br>new, 3 days | **Chart `bp-conformance` and its slot**<br>Every Sovereign runs the conformance suite on itself after install and after each release, and a gate check reads the report. | FND-151, SOV-510, CMP-150, CTL-038, FND-011 | Makes FND-G15 required. |

<details><summary>Files each task of this group changes</summary>

- **FND-152**: `platform/conformance/**`, `clusters/_template/bootstrap-kit/99-bp-conformance.yaml`, `clusters/_template/bootstrap-kit/kustomization.yaml`, `scripts/expected-bootstrap-deps.yaml`, `tests/acceptance/sovereign/internal/suite/check_self_conformance*.go`, `docs/ledger/gates.yaml`, `products/catalyst/bootstrap/api/internal/catalog/blueprints.json`, `products/catalyst/bootstrap/ui/src/shared/constants/catalog.generated.ts`

</details>

## CLD Cloud abstraction

### Group CLD-140: Epic: provider overlays, one overlay path

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **CLD-141**<br>guard, 1 day | **Protect provider releases from pruning before they move**<br>Prune-disabled annotation on four HelmReleases | CLD-055 | stated in its ticket |
| **CLD-144**<br>rework, 5 days | **Provider overlays replace provider-named flags**<br>As the body. Moved releases keep every label. If ConfigMap sovereign-variables exists in the template, the provider-pack Kustomization carries the same substituteFrom. The new CLOUD_* keys are appended to docs/contracts/c5-kit-keys.txt when that file exists. | CLD-080, CLD-132, CLD-141, CLD-142, FND-032 | Makes CLD-G01 required. |

<details><summary>Files each task of this group changes</summary>

- **CLD-144**: `clusters/_template/bootstrap-kit/**`, `infra/providers/_shared/cloudinit-control-plane.tftpl`, `infra/providers/_contract/contract.json`, `scripts/expected-bootstrap-deps.yaml`, `scripts/expected-cloudinit-kustomization-deps.yaml`, `products/catalyst/bootstrap/api/internal/provisioner/region_health.go`, `tests/acceptance/sovereign/internal/suite/check_*.go`, `docs/contracts/c5-kit-keys.txt`

</details>

### Group CLD-110: Epic: handlers and controllers stop naming providers

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **CLD-113**<br>rework, 3 days | **Mesh and topology read the profile**<br>As the body, except step 3: isNoCCMCloud is deleted and its callers always take the host-port proxy path on port 12379. No mesh field is read from the profile. If NET's package removed the function first, the step is skipped. | CLD-080, FND-011 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **CLD-113**: `products/catalyst/bootstrap/api/internal/handler/clustermesh.go`, `products/catalyst/bootstrap/api/internal/infrastructure/topology_loader.go`, `products/catalyst/bootstrap/api/internal/handler/infrastructure.go`, `products/catalyst/chart/templates/api-deployment*.yaml`, `products/catalyst/chart/values.yaml`, `clusters/_template/bootstrap-kit/13-bp-catalyst-platform.yaml`

</details>

### Group CLD-160: Epic: installation parameters; installation literals leave the code

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **CLD-161**<br>new, 3 days | **Installation document for the installation in use**<br>CloudProfile installation document applied to the mothership as a labelled ConfigMap | CLD-080 | stated in its ticket |
| **CLD-162**<br>rework, 5 days | **The Huawei module and adapter take installation values as inputs**<br>cloud_api_domain and related variables; defaults removed; adapter TfVars from the effective profile; neutral CLOUD_* substitutes | CLD-104, CLD-121, CLD-144, CLD-161, FND-011 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **CLD-162**: `infra/providers/huawei/**`, `products/catalyst/bootstrap/api/internal/providers/huawei/**`, `platform/huawei-evs-csi/chart/values.yaml`, `clusters/_template/providers/huawei/releases/*.yaml`, `infra/providers/_contract/contract.json`, `infra/providers/_shared/tests/**`, `docs/contracts/c5-kit-keys.txt`

</details>

### Single tasks

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **CLD-090**<br>rework, 5 days | **The three modules meet contract version 1; conformance tests**<br>As the body, and TestProviderPacks asserts the two module rules: boot data in lifecycle.ignore_changes and no node port. | CLD-040, CLD-080, CLD-087, FND-031 | Makes CLD-G03, CLD-G12 required. |
| **CLD-065**<br>fix, 1 day | **Crossplane 1.20 to 2.4**<br>Core at highest 2.4 patch | CLD-064, FND-011 | stated in its ticket |
| **CLD-066**<br>fix, 1 day | **provider-opentofu v1.1.11 by digest**<br>Provider package pinned by digest. The package keeps the registry prefix it finds in the line and changes only the version to the v1.1.11 digest. | CLD-050, CLD-065 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **CLD-090**: `products/catalyst/bootstrap/api/internal/providers/conformance/**`, `products/catalyst/bootstrap/api/internal/provisioner/provisioner.go`, `.github/workflows/catalyst-build.yaml`, `.github/workflows/cloud-init-size.yaml`
- **CLD-065**: `platform/crossplane/chart/Chart.yaml`, `platform/crossplane/chart/values.yaml`
- **CLD-066**: `clusters/_template/infrastructure/providers/base/provider-opentofu.yaml`

</details>

### Group CLD-100: Epic: the engine drives the port

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **CLD-101**<br>test, 3 days | **Golden files for the variables the engine writes**<br>TestTfvarsGolden for both clouds and TestLegacyRecordLoads | CLD-090 | stated in its ticket |
| **CLD-102**<br>rework, 3 days | **Port interface, capability interfaces, generated registry**<br>As the body. all.go is generated by go generate calling scripts/generators/provider-registry.py, checked by FND's drift workflow. The stale header comments in provider.go and all.go are fixed here. | CLD-080, CLD-101, FND-102 | stated in its ticket |
| **CLD-103**<br>rework, 5 days | **Credentials map, validation through the port, storage class from the profile**<br>Request.Credentials and Installation, legacy aliases, Normalize in adapters, provider switch and storage-class map deleted | CLD-102 | stated in its ticket |
| **CLD-104**<br>rework, 5 days | **The engine writes neutral variables; adapters write their own**<br>writeTfvars writes contract variables and engine extras only; TfVars in adapters; six provider-named Request fields deleted | CLD-103 | stated in its ticket |
| **CLD-106**<br>rework, 3 days | **Operator credentials from mounted files**<br>catalyst-api reads files cloud-credential.<provider>.<key> from the directory of flag --cloud-credentials-dir, default /var/run/secrets/openova. The files come from a CSI volume fed from OpenBao. No Secret volume and no environment variable. CATALYST_HUAWEI_* and the reference to Secret huawei-operator-creds are removed. | CLD-103, SEC-190, SEC-211, FND-011 | stated in its ticket |
| **CLD-107**<br>new, 5 days | **Command provider-identity and capability IdentityManager**<br>Interfaces IdentityManager and KeyProber and the binary provider-identity with verbs mint, rotate, revoke, probe, exactly as section 4.1 (JSON on standard input and output, exit codes 0, 1, 3, 4). Every adapter implements probe. The local adapter implements all verbs. The Huawei adapter implements the minting verbs only if the SOV-070 record answers minted. | CLD-088, CLD-102, FND-032, SOV-070 | Makes CLD-G17 required. |

<details><summary>Files each task of this group changes</summary>

- **CLD-101**: `products/catalyst/bootstrap/api/internal/provisioner/tfvars_golden_test.go`, `products/catalyst/bootstrap/api/internal/provisioner/testdata/**`, `products/catalyst/bootstrap/api/internal/store/legacy_record_test.go`
- **CLD-102**: `products/catalyst/bootstrap/api/internal/handler/janitor.go`, `scripts/generators/provider-registry.py`
- **CLD-103**: `products/catalyst/bootstrap/api/internal/store/store.go`, `products/catalyst/bootstrap/api/internal/handler/deployments.go`
- **CLD-104**: `products/catalyst/bootstrap/api/internal/provisioner/provisioner.go`, `products/catalyst/bootstrap/api/internal/provisioner/testdata/*.golden.json`
- **CLD-106**: `products/catalyst/bootstrap/api/internal/handler/deployments.go`, `products/catalyst/chart/templates/api-deployment.yaml`, `products/catalyst/chart/templates/api-deployment-kustomize.yaml`, `products/catalyst/chart/values.yaml`
- **CLD-107**: `products/catalyst/bootstrap/api/internal/providers/capabilities.go`, `products/catalyst/bootstrap/api/cmd/provider-identity/**`, `products/catalyst/bootstrap/api/Containerfile`, `tests/acceptance/sovereign/internal/suite/check_*.go`

</details>

### Group CLD-130: Epic: the shared template without provider tests

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **CLD-131**<br>rework, 3 days | **Provider substitutes move from the template into the modules (pure move)**<br>Input provider_substitutes_yaml; the two provider blocks become module locals | CLD-086, CLD-090 | stated in its ticket |
| **CLD-132**<br>rework, 3 days | **Remaining provider tests become injected inputs**<br>Inputs node_ipv4_only, fail_mark_cmd, gateway_host_network, render_region_kustomization; lint scripts iterate packs and read the size limit from the profile | CLD-131 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **CLD-131**: `infra/providers/_shared/cloudinit-control-plane.tftpl`, `infra/providers/_shared/tests/render.tf`
- **CLD-132**: `infra/providers/_shared/cloudinit-control-plane.tftpl`, `scripts/lint-cloudinit.sh`, `scripts/cloudinit-size-check.sh`, `products/catalyst/bootstrap/api/internal/providers/conformance/conformance_test.go`

</details>

### Group CLD-210: Epic: ratchet the leak guard to zero

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **CLD-211**<br>guard, 3 days | **Pack tokens and the additive check**<br>Scanner reads pack.yaml tokens and charts; additive check with self-test. The generator wrapper is not written; the generators run in FND's drift workflow. | CLD-080, CLD-102 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **CLD-211**: `scripts/check-provider-leaks.py`, `scripts/check-provider-pack-additive.sh`, `.github/workflows/check-provider-contract.yaml`

</details>

### Group CLD-120: Epic: providers route; the console reads the profile

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **CLD-121**<br>new, 3 days | **Providers routes**<br>The three GET routes of section 4.6 in a new package internal/providersapi, registered through the register function of CTL-035. Nothing is added to package handler. | CLD-103, CLD-113, CTL-035, FND-032 | Makes CLD-G07 required. |

<details><summary>Files each task of this group changes</summary>

- **CLD-121**: `products/catalyst/bootstrap/api/cmd/api/main.go`, `products/catalyst/bootstrap/api/internal/providersapi/**`, `tests/acceptance/sovereign/internal/suite/check_*.go`

</details>

### Group CLD-085: Epic: the local provider pack

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **CLD-088**<br>new, 5 days | **local pack: profile, adapter, overlay (first files-only proof)**<br>Complete local pack added by files only. The profile has no mesh section and sets seal.kind static-node-file when the schema asks for it. The adapter implements IdentityManager when the interface exists. | CLD-104, CLD-144, CLD-211, FND-031 | Makes CLD-G15 required. |

<details><summary>Files each task of this group changes</summary>

- **CLD-088**: `products/catalyst/bootstrap/api/internal/providers/local/**`, `clusters/_template/providers/local/**`, `products/catalyst/bootstrap/api/internal/providers/all/all.go`

</details>

### Group CLD-170: Epic: unit Workspaces and NodePool

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **CLD-171**<br>new, 3 days | **State in the bucket, encrypted; ProviderConfig cloud**<br>ClusterProviderConfig cloud with the s3 back end and the cloud credential as a mounted file (source Filesystem); the provider pod no longer reads Secret cloud-credentials. TF_ENCRYPTION in the runtime configuration with the OpenBao key provider and a token file re-minted every 15 minutes; the token generator and its ExternalSecret are rendered here in SEC's form. Three object-storage substitutes. Namespace catalyst-infra. The permanent canary Workspace catalyst-infra/state-canary. The template workspaces/base.yaml. | CLD-050, CLD-066, CLD-087, FND-032, SEC-190, SEC-211 | Makes CLD-G05 required. |
| **CLD-172**<br>new, 3 days | **OpenTofu provider binaries from the Sovereign's registry**<br>Mirror workflow from pack.yaml tofuProviders with versions from each module's lock file. CLI configuration for the provider pod and the API image that installs providers only from ${REGISTRY_HOST}/${REGISTRY_PATH_TOFU_PROVIDERS}. The stale Containerfile comments are fixed here. | CLD-050, CLD-171, SOV-130 | stated in its ticket |
| **CLD-173**<br>new, 3 days | **Facts for unit modules: day2_facts, the facts ConfigMap, the join token**<br>As the body, plus fact key region_code: the Sovereign region code of the cluster the ConfigMap is applied to. The join Secret holds the agent token of SEC-020 and is entered in SEC's exception register in the same pull request. | CLD-162, CLD-171, SEC-020, SEC-095 | stated in its ticket |
| **CLD-174**<br>new, 5 days | **Unit module nodepool for the local and Huawei packs, proven as plain unit Workspaces**<br>Two unit modules to section 4.8 with mock plan tests including the four Huawei behaviours. The module source is the local repository openova/provider-pack. The template workspaces/nodepool.yaml as a plain unit Workspace with no region field. Labels and taints are set at join. The walk is one suite check file that runs on the local pack and on the cloud. | CLD-088, CLD-172, CLD-173, FND-032, SOV-230 | Adds the check CLD-G04. |
| **CLD-176**<br>new, 5 days | **Bootstrap unit provisioner-access: the provisioner's API-port rule leaves the bootstrap module**<br>pack.yaml field spec.bootstrapUnits with its reader: the engine applies each named unit right after the bootstrap module, with its own state key tofu-state/<unit>/terraform.tfstate. Unit module provisioner-access holds the rule that lets provisioner_cidrs reach the API port; the rule leaves the Huawei and local bootstrap modules. The second provider keeps the rule in its bootstrap module. Holds lock provider-main. | CLD-086, CLD-104, CLD-173, NET-011 | stated in its ticket |
| **CLD-175**<br>new, 3 days | **NodePool definition with its two Compositions**<br>As the body. The manifest lives in the region's folder at the path contract C4 names. nodepools.compose.openova.io is added to the intent lock with the Crossplane exemption. The content of workspaces/nodepool.yaml becomes a NodePool object with the same placeholders. | CLD-174, CTL-045, CTL-150, FND-011 | Makes CLD-G04 required. |
| **CLD-177**<br>guard, 3 days | **Admission rule on the module address of a Workspace**<br>A ValidatingAdmissionPolicy and its binding for kind Workspace in group opentofu.m.upbound.io. Outside namespace catalyst-infra a Workspace is admitted only when source is Remote and the module address starts with git::${GITOPS_REPO_URL}//infra/providers/ and contains /day2/. Inside catalyst-infra the base module (sovereign-base) and the inline canary (state-canary) are also admitted. The cluster-scoped legacy Workspace kind is not matched. | CLD-174, SOV-130 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **CLD-171**: `clusters/_template/infrastructure/base/**`, `clusters/_template/infrastructure/providers/base/provider-opentofu.yaml`, `infra/providers/_shared/cloudinit-control-plane.tftpl`, `infra/providers/*/workspaces/base.yaml`, `tests/acceptance/sovereign/internal/suite/check_*.go`, `docs/contracts/c5-kit-keys.txt`
- **CLD-172**: `.github/workflows/mirror-tofu-providers.yaml`, `clusters/_template/infrastructure/providers/base/provider-opentofu.yaml`, `products/catalyst/bootstrap/api/Containerfile`
- **CLD-173**: `infra/providers/_contract/contract.json`, `infra/providers/_shared/cloudinit-control-plane.tftpl`, `products/catalyst/bootstrap/api/internal/handler/post_handover_adoption_apply.go`, `products/catalyst/bootstrap/api/internal/providers/conformance/conformance_test.go`
- **CLD-174**: `tests/acceptance/sovereign/internal/suite/check_*.go`
- **CLD-176**: `infra/providers/_contract/pack.schema.json`, `products/catalyst/bootstrap/api/internal/provisioner/provisioner.go`, `products/catalyst/bootstrap/api/internal/providers/conformance/conformance_test.go`
- **CLD-175**: `platform/crossplane-claims/chart/templates/xrds/nodepool.yaml`, `platform/crossplane-claims/chart/values.yaml`, `products/catalyst/chart/templates/intent-lock/bindings.yaml`, `tests/acceptance/sovereign/internal/suite/check_*.go`
- **CLD-177**: `clusters/_template/infrastructure/base/workspace-module-policy.yaml`, `clusters/_template/infrastructure/base/kustomization.yaml`

</details>

## SOV Sovereign lifecycle

### Group SOV-200: EPIC: Hand-over as a short ordered flip

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **SOV-220**<br>rework, 1 day | **The console's step list comes from the status response**<br>Card renders the steps array of the status response; unknown names render | nothing else | stated in its ticket |
| **SOV-260**<br>delete, 3 days | **Steps 1, 2 and 8 deleted; the sequencer loses the code of all eight**<br>Six-step chain; sequencer code for deleted steps removed; phase Independent written with cutoverComplete | SOV-160, SOV-240, FND-011 | stated in its ticket |
| **SOV-270**<br>rework, 5 days | **The proof exercises certificate, mail, Organization and inbound**<br>Four probes inside the hold; origin login removed from the pull Secret on success | FND-032, NET-153, SOV-250, SOV-310, SOV-320, SOV-330, FND-011 | Makes SOV-G07 required. |

<details><summary>Files each task of this group changes</summary>

- **SOV-220**: `products/catalyst/bootstrap/ui/src/shared/types/cutover.ts`
- **SOV-260**: `platform/self-sovereign-cutover/chart/values.yaml`, `products/catalyst/bootstrap/api/internal/handler/cutover*.go`, `scripts/check-cutover-version-floor.py`, `products/catalyst/bootstrap/api/internal/handler/cutover_chart_floor.go`, `docs/ledger/deleted-paths.txt`
- **SOV-270**: `platform/self-sovereign-cutover/chart/templates/08-egress-block-test-job.yaml`, `platform/self-sovereign-cutover/chart/values.yaml`, `tests/acceptance/sovereign/internal/suite/`, `tests/acceptance/sovereign/internal/suite/check_independence_proof.go`

</details>

### Group SOV-700: EPIC: Day-2 with the parent switched off

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **SOV-750**<br>lab test, 3 days | **Spike: an auto-scaling group worker pool on the reference cloud**<br>Three yes/no rows; decides whether mode autoscaled is offered there | nothing else | stated in its ticket |
| **SOV-760**<br>new, 3 days | **Kit keys are set by commit and at provisioning**<br>Route PUT /api/v1/sovereign/kit-keys/{key} with body {"value":"<string>","regions":["<region-id>"]} (regions optional, default all) commits the key to the named regions' variables.yaml through the commit library, write class git. The create request accepts an optional map kitKeys. Both accept only keys listed in docs/contracts/c5-kit-keys-settable.txt; the package that ships a key's reader adds its line there. | CTL-035, CTL-110, SOV-170, SOV-180 | stated in its ticket |
| **SOV-740**<br>new, 3 days | **Maintenance window on the base Workspace**<br>Open and close routes, guard G6, admission rule bounding a managed base Workspace to an annotated window of at most 8 hours | CTL-035, SOV-420 | stated in its ticket |
| **SOV-710**<br>new, 5 days | **Node scaling by Git commit**<br>Route that creates, resizes and removes an additional pool's unit Workspace file with a fixed count (the count input of the template of CLD-174; no scaling object, no mode). The file sits in that region's folder at the path and scope contract C4 names. Console control. | CTL-035, CTL-110, CTL-112, FND-032, SOV-435, CLD-175 | Makes SOV-G10 required. |
| **SOV-730**<br>new, 3 days | **Cloud credential rotation on schedule and on request**<br>CronJob and nonce-named Job running rotate-credential; rotate route; check SOV.13 | CTL-035, FND-032, SOV-435 | Makes SOV-G13 required. |

<details><summary>Files each task of this group changes</summary>

- **SOV-750**: `docs/ledger/plan/spikes/SOV-750-autoscaling-pool.md`
- **SOV-760**: `products/catalyst/bootstrap/api/internal/lifecycle/kitkeys.go`, `products/catalyst/bootstrap/api/internal/lifecycle/kitkeys_test.go`, `products/catalyst/bootstrap/api/cmd/api/**`, `products/catalyst/bootstrap/api/internal/provisioner/provisioner.go`, `docs/contracts/c5-kit-keys-settable.txt`, `docs/contracts/C9-mutating-routes.yaml`
- **SOV-740**: `scripts/check-base-workspace-observe.sh`, `products/catalyst/bootstrap/api/internal/lifecycle/window.go`
- **SOV-710**: `products/catalyst/bootstrap/ui/src/pages/sovereign/`, `products/catalyst/bootstrap/api/internal/lifecycle/pools.go`, `tests/acceptance/sovereign/internal/suite/check_scale_by_commit.go`
- **SOV-730**: `clusters/_template/infrastructure/base/credential-rotation.yaml`, `tests/acceptance/sovereign/internal/suite/lifecycle.go`, `products/catalyst/bootstrap/api/internal/lifecycle/rotate.go`, `tests/acceptance/sovereign/internal/suite/check_credential_rotation.go`

</details>

### Group SOV-100: EPIC: Upstream contract and born-local bootstrap

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **SOV-140**<br>rework, 5 days | **Substrate window, part 2: revocable bootstrap access, peer tokens, node artifacts from the registry**<br>parent-bootstrap and handover-peer service-account kubeconfigs in place of the administrator file; oci_fetch for node artifacts with fallback flag | SOV-130, NET-031 | stated in its ticket |
| **SOV-190**<br>rework, 1 day | **The console reads the mode from the API**<br>Host-name test deleted; mode from /api/v1/sovereign/self | SOV-180 | stated in its ticket |
| **SOV-175**<br>new, 3 days | **The Sovereign object gets its writer**<br>One Sovereign object per region cluster, written by sovereign-ops at bootstrap from the variables document and refreshed at hand-over and after a region is added. It fills the fields the kind has today. The network registry and region separation are copied from the module output into its status once NET-102 adds those fields with their reader. spec.catalog is left to the service-model area. The mechanism and the source of the registry are listed as unsettled; the ticket fixes them. | FND-032, FND-120, SOV-120, SOV-170 | Makes SOV-G17 required. |

<details><summary>Files each task of this group changes</summary>

- **SOV-140**: `infra/providers/hetzner/main.tf`, `infra/providers/huawei/main.tf`, `infra/providers/_shared/cloudinit-control-plane.tftpl`, `infra/providers/_shared/tests/cloudinit_render.tftest.hcl`, `products/catalyst/bootstrap/api/internal/handler/deployment_handover_export.go`, `scripts/secret-guard-baselines/secret-object-register.yaml`
- **SOV-190**: `products/catalyst/bootstrap/ui/src/shared/lib/detectMode.ts`, `products/catalyst/bootstrap/ui/src/app/router.tsx`
- **SOV-175**: `core/cmd/sovereign-ops/internal/sovereignobject/**`, `clusters/_template/sovereign-region/base/repo-seed-job.yaml`, `tests/acceptance/sovereign/internal/suite/check_sovereign_object.go`

</details>

### Group SOV-300: EPIC: Standing tethers cut at bootstrap

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **SOV-320**<br>rework, 5 days | **Sign-in mail without the parent's credential**<br>MAIL_MODE local or relay per child; seeding of the parent mailbox credential deleted | SOV-090, SOV-150, FND-011 | stated in its ticket |
| **SOV-335**<br>rework, 3 days | **The vulnerability database is served from the Sovereign's own registry**<br>One proxy project in each region's registry that proxies the parent's copy of the Trivy vulnerability database, created by the install Job of SOV-160. One contract key of C5, REGISTRY_PATH_VULN_DB, read by slot 30, which sets the scanner's database repository to the Sovereign's own registry. INF-145 sets the same value for the machine scanner. | SOV-110, SOV-150, SOV-160, FND-011 | stated in its ticket |
| **SOV-310**<br>rework, 5 days | **DNS zones delegated to the Sovereign**<br>NS delegation at provisioning; certificate validation against the Sovereign's own PowerDNS; central key deleted everywhere | CLD-086, CLD-090, SOV-090, SOV-150 | stated in its ticket |
| **SOV-330**<br>rework, 3 days | **The pool-domain manager is used only when a parent allocates a child**<br>Planted pool credential removed; Organization names written to the local PowerDNS; manager enabled only in mothership mode | CTL-201, SOV-310, FND-011 | stated in its ticket |
| **SOV-340**<br>fix, 3 days | **The parent's standing loops end at hand-over**<br>Mesh heal loop ends at HandoverCompletedAt; kubeconfig delivery loop deleted; standingLoops(id) | FND-032, NET-131, SOV-030, SOV-140 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **SOV-320**: `products/catalyst/bootstrap/api/internal/handler/sovereign_smtp_seed.go`, `clusters/_template/bootstrap-kit/kustomization.yaml`, `clusters/_template/bootstrap-kit/95-bp-stalwart-sovereign.yaml`, `platform/stalwart-sovereign/chart/**`, `products/catalyst/chart/values.yaml`, `scripts/secret-guard-baselines/secret-object-register.yaml`
- **SOV-335**: `docs/contracts/c5-upstream-variables.schema.json`, `core/controllers/pkg/upstream/**`, `platform/harbor/chart/templates/**`, `platform/trivy/chart/values.yaml`, `platform/trivy/chart/templates/**`, `clusters/_template/bootstrap-kit/30-trivy.yaml`, `tests/hermetic/handover/fixtures/**`
- **SOV-310**: `clusters/_template/sovereign-tls/powerdns-api-credentials.yaml`, `clusters/_template/bootstrap-kit/49-bp-cert-manager-powerdns-webhook.yaml`, `clusters/_template/bootstrap-kit/11-powerdns.yaml`, `infra/providers/_shared/cloudinit-control-plane.tftpl`, `products/catalyst/bootstrap/api/internal/provisioner/provisioner.go`, `scripts/secret-guard-baselines/secret-object-register.yaml`
- **SOV-330**: `infra/providers/_shared/cloudinit-control-plane.tftpl`, `products/catalyst/bootstrap/api/internal/provisioner/provisioner.go`, `products/catalyst/chart/values.yaml`
- **SOV-340**: `products/catalyst/bootstrap/api/internal/handler/phase1_watch.go`, `products/catalyst/bootstrap/api/internal/handler/deployment_handover_export.go`, `tests/acceptance/sovereign/internal/suite/`, `products/catalyst/bootstrap/api/internal/lifecycle/loops.go`, `tests/acceptance/sovereign/internal/suite/check_parent_silent.go`

</details>

### Group SOV-400: EPIC: Ownership pivot and parent release

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **SOV-405**<br>rework, 3 days | **The parent provisions with a per-child identity**<br>Mode minted: scoped identity minted before tofu apply and passed to the child in place of the account key; mode supplied unchanged | CLD-107, SOV-070 | stated in its ticket |
| **SOV-410**<br>new, 5 days | **ownership-pivot, part 1: own key, encrypted state push, plan gate**<br>As in the body. The Job logs in to OpenBao with its own service account and stores no token; the provider pod's token is rendered by CLD-171. | CLD-080, CLD-107, SEC-190, SOV-070, SOV-080, SOV-120 | stated in its ticket |
| **SOV-430**<br>new, 5 days | **Parent side of the release: release token, purge, custody scan and report**<br>Release token, FinaliseHandover purge with scanned report, custody route, wipe 409 for released children, cloudAccount on the record | CTL-035, SOV-030, SOV-140, SOV-340 | stated in its ticket |
| **SOV-420**<br>new, 3 days | **ownership-pivot, part 2: facts and base Workspace committed, step added to the chain**<br>infrastructure-facts, sovereign-base-inputs and observe-only Workspace sovereign-base committed; step ownership-pivot in the chain; check SOV.04 | CLD-171, CTL-110, CTL-112, FND-032, SEC-196, SOV-240, SOV-410, FND-011 | Makes SOV-G04 required. |
| **SOV-435**<br>new, 5 days | **parent-release: key revocation, access revocation, release call, custody proof**<br>As in the body. The rotation stage propagate waits until every consumer the secrets area lists carries the new key (mounted file in the provider pod and the API pod; a registered synced Secret for a driver chart). | CLD-107, FND-032, SOV-310, SOV-320, SOV-330, SOV-405, SOV-420, SOV-430, FND-011 | Makes SOV-G05, SOV-G06, SOV-G08 required. |
| **SOV-440**<br>test, 3 days | **The offline-key restore drill**<br>Subcommand restore-drill; check SOV.09 in scenario (OpenBao stopped) and gate | FND-032, SEC-190, SOV-080, SOV-420 | Makes SOV-G09 required. |
| **SOV-436**<br>fix, 3 days | **No static SSH key on a Sovereign's nodes**<br>After hand-over no node of a Sovereign accepts the per-Sovereign static SSH key, and POST /api/v1/sshkey/generate is no longer served without a session. Existing Sovereigns keep the key until their nodes are replaced. The mechanism (never plant the key, or remove it at hand-over) is listed as unsettled; the ticket fixes it. | SOV-140, SOV-435 | stated in its ticket |
| **SOV-437**<br>rework, 3 days | **The provisioner's access rule is moved and emptied at hand-over**<br>state-push moves the state of each bootstrap unit the pack lists (first: provisioner-access) together with the base state. The pivot commits that unit's Workspace as managed. access-revoke commits its address list empty, so the rule goes without a maintenance window on the base Workspace. | CLD-176, SOV-435 | stated in its ticket |
| **SOV-450**<br>delete, 3 days | **Read-only adoption and the parent's post-hand-over hooks are deleted**<br>CloudAdoption, its definition, ProviderConfig default, the adoption hook with its call at phase1_watch.go:1624 and the archive yes/no check are removed. Lines 1608 and 1651 are not touched. | SOV-435, FND-011 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **SOV-405**: `products/catalyst/bootstrap/api/internal/provisioner/provisioner.go`, `products/catalyst/bootstrap/api/internal/handler/deployments.go`
- **SOV-410**: `tests/hermetic/handover/fixtures/**`
- **SOV-430**: `products/catalyst/bootstrap/api/internal/handler/handover.go`, `products/catalyst/bootstrap/api/internal/handler/wipe.go`, `products/catalyst/bootstrap/api/internal/handler/deployment_handover_export.go`, `products/catalyst/bootstrap/api/internal/lifecycle/custody.go`
- **SOV-420**: `platform/self-sovereign-cutover/chart/templates/03x-ownership-pivot-job.yaml`, `platform/self-sovereign-cutover/chart/templates/rbac.yaml`, `tests/acceptance/sovereign/internal/suite/`, `tests/acceptance/sovereign/internal/suite/check_ownership.go`
- **SOV-435**: `core/cmd/sovereign-ops/internal/release/**`, `core/cmd/sovereign-ops/internal/rotation/**`, `platform/self-sovereign-cutover/chart/templates/07-parent-release-job.yaml`, `platform/self-sovereign-cutover/chart/templates/rbac.yaml`, `tests/acceptance/sovereign/internal/suite/`, `tests/acceptance/sovereign/internal/suite/check_parent_holds_nothing.go`, `tests/acceptance/sovereign/internal/suite/check_child_holds_no_parent_secret.go`
- **SOV-440**: `core/cmd/sovereign-ops/internal/restore/**`, `tests/hermetic/handover/assert/`, `tests/acceptance/sovereign/internal/suite/`, `docs/RUNBOOKS.md`, `tests/acceptance/sovereign/internal/suite/check_offline_restore.go`
- **SOV-436**: `infra/providers/huawei/main.tf`, `infra/providers/huawei/variables.tf`, `infra/providers/hetzner/main.tf`, `infra/providers/hetzner/variables.tf`, `products/catalyst/bootstrap/api/internal/provisioner/provisioner.go`, `products/catalyst/bootstrap/api/internal/store/store.go`, `products/catalyst/bootstrap/api/internal/handler/sshkey.go`, `products/catalyst/bootstrap/api/cmd/api/**`, `products/catalyst/bootstrap/ui/src/pages/wizard/**`, `tests/acceptance/sovereign/internal/suite/check_parent_holds_nothing.go`
- **SOV-437**: `core/cmd/sovereign-ops/internal/pivot/**`, `core/cmd/sovereign-ops/internal/release/**`, `tests/hermetic/handover/fixtures/**`, `tests/acceptance/sovereign/internal/suite/check_parent_holds_nothing.go`
- **SOV-450**: `platform/crossplane-claims/chart/templates/compositions/cloudadoption.yaml`, `platform/crossplane-claims/chart/templates/xrds/cloudadoption.yaml`, `clusters/_template/infrastructure/base/adoption-claims.yaml`, `clusters/_template/infrastructure/base/provider-config-opentofu.yaml`, `products/catalyst/bootstrap/api/internal/handler/post_handover_adoption_apply.go`, `products/catalyst/bootstrap/api/internal/provisioner/adoption.go`, `products/catalyst/bootstrap/api/internal/handler/phase1_watch.go`, `products/catalyst/bootstrap/api/internal/handler/cutover_internal.go`, `docs/ledger/deleted-paths.txt`

</details>

### Group SOV-800: EPIC: Mothership mode and the child walk

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **SOV-810**<br>new, 5 days | **Mothership mode complete**<br>As in the body. The provisioning inputs and the cloud credentials reach the API pod as mounted files delivered by the secrets area. | CLD-104, CLD-106, CLD-172, SEC-080, SEC-196, SOV-180, SOV-190, SOV-310, SOV-330, SOV-405, SOV-430, SOV-510, FND-011 | stated in its ticket |
| **SOV-820**<br>test, 5 days | **The child walk with company hosts blocked**<br>Journey L2 as one registered check with run-ins cloud and local. The local variant runs in the workflow conformance-local. | FND-032, SOV-610, SOV-710, SOV-810 | Makes SOV-G15 required. |

<details><summary>Files each task of this group changes</summary>

- **SOV-810**: `products/catalyst/chart/templates/api-deployment.yaml`, `products/catalyst/bootstrap/api/internal/provisioner/provisioner.go`, `products/catalyst/bootstrap/ui/src/pages/wizard/`
- **SOV-820**: `tests/acceptance/sovereign/internal/suite/check_child_walk.go`

</details>

### Group SOV-500: EPIC: Release channel and in-place upgrade

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **SOV-520**<br>new, 5 days | **release-sync: closure first, then the local tag**<br>CronJob that copies a new release before moving the local tag; version rule; route PUT /api/v1/sovereign/release | CTL-035, FND-032, SOV-230, SOV-240, SOV-510 | stated in its ticket |
| **SOV-530**<br>new, 3 days | **Offline bundle load**<br>Subcommand bundle-load; release-sync works with an empty origin | FND-032, SOV-520 | Makes SOV-G14 required. |
| **SOV-540**<br>delete, 3 days | **The refresh refusal and the day-two reconcilers are deleted**<br>As in the body, plus: the package report lists the provider literals, Secret manifests and random-value sites left in the cutover chart. | FND-142, SOV-520, FND-011 | Makes SOV-G12 required. |

<details><summary>Files each task of this group changes</summary>

- **SOV-520**: `core/cmd/sovereign-ops/internal/releasesync/**`, `clusters/_template/bootstrap-kit/06c-release-sync.yaml`, `clusters/_template/bootstrap-kit/kustomization.yaml`, `tests/acceptance/sovereign/internal/suite/`, `products/catalyst/bootstrap/api/internal/lifecycle/release.go`, `tests/acceptance/sovereign/internal/suite/check_platform_upgrade.go`
- **SOV-530**: `core/cmd/sovereign-ops/internal/bundle/**`, `docs/RUNBOOKS.md`, `tests/hermetic/handover/`, `tests/acceptance/sovereign/internal/suite/check_offline_bundle.go`
- **SOV-540**: `platform/self-sovereign-cutover/chart/templates/11-mirror-resync-cronjob.yaml`, `platform/self-sovereign-cutover/chart/templates/12-daytwo-harbor-pin-reconciler.yaml`, `platform/self-sovereign-cutover/chart/values.yaml`, `scripts/sovereign-daytwo-bootstrap.sh`, `docs/ledger/deleted-paths.txt`

</details>

### Group SOV-600: EPIC: Proof and freeze

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **SOV-610**<br>test, 5 days | **Lifecycle journey L1, zero touch, on the train**<br>Journey L1 and check SOV.02 | FND-032, FND-035, FND-141, SOV-270, SOV-340, SOV-435, SOV-440, SOV-510 | Makes SOV-G02 required. |
| **SOV-620**<br>decision, 3 days | **Freeze the proven lifecycle paths**<br>Decision record 0017; seven frozen items requested in the stability register, each with a soak that names the lifecycle checks. The frozen scenario item is the hand-over job of the workflow conformance-local. | SOV-610 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **SOV-610**: `scripts/sovereign-3x-zero-touch-cycle.sh`, `docs/ledger/gates.yaml`, `tests/acceptance/sovereign/internal/suite/check_zero_touch_handover.go`
- **SOV-620**: `docs/adr/0017-sovereign-lifecycle.md`, `docs/ledger/stability.yaml`, `docs/adr/0002-post-handover-sovereignty-cutover.md`, `docs/adr/0011-opentofu-crossplane-adoption-seam.md`

</details>

### Single tasks

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **SOV-265**<br>guard, 1 day | **Lock stage flux to deny**<br>Stage flux of the intent lock is set to deny, once no job of this area patches a Flux object. Same form as the control-plane area's stage switches. | CTL-150, SOV-260, SOV-540, FND-011 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **SOV-265**: `products/catalyst/chart/values.yaml`, `scripts/baselines/lock-stages.json`

</details>

## SEC Secrets and root of trust

### Single tasks

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **SEC-196**<br>new, 3 days | **Hand-over identity sovereign-ops and the hand-over inputs Secret**<br>Policy and auth role sovereign-ops of section 4.6. An ExternalSecret that fills Secret catalyst-infra/sovereign-base-secret-inputs from secret/data/catalyst/tofu/sovereign-base-inputs, with its class K register entry. The chart file that holds the ExternalSecret is named by the architect when the ticket is cut. | SEC-080, SEC-095, SEC-190, FND-011 | stated in its ticket |
| **SEC-238**<br>delete, 5 days | **Delete the cross-region Secret copies of the API**<br>The Secret copy functions between lines 1931 and 3031 of clustermesh.go are deleted. Shared values reach the peer region by peer push from OpenBao. Guard G5 baseline and the handler line count fall. Which of the five functions go is fixed by the decision listed under unsettled. | FND-120, NET-140, SEC-215, SEC-221, SEC-222, SEC-224, SEC-231 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **SEC-196**: `platform/openbao/chart/templates/_config.tpl`, `platform/openbao/chart/tests/config-desired-render.sh`, `tests/e2e/bp-openbao-config-live.sh`, `scripts/secret-guard-baselines/secret-object-register.yaml`
- **SEC-238**: `products/catalyst/bootstrap/api/internal/handler/clustermesh.go`, `products/catalyst/bootstrap/api/internal/handler/clustermesh_test.go`, `scripts/secret-guard-baselines/check-no-secret-writers.txt`

</details>

## NET Network, zones and cluster classes

### Single tasks

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **NET-102**<br>new, 3 days | **Registry in the Sovereign status, checked against the live cluster**<br>Network fields in the status of the Sovereign kind and a gate check comparing them with cilium-config | FND-032, NET-100, FND-011 | Makes NET-G08 required. |
| **NET-153**<br>fix, 3 days | **Allow-list of the hand-over proof's deny-egress object**<br>The proof's deny-egress object keeps its shape. Its intra-cluster allow ranges no longer come from chart defaults: the kit slot passes the routing range key that NET-110 adds and the service range. The cloud API ranges keep coming through PROVIDER_API_CIDRS_YAML. The object is a named exception of guard G2. Holds locks cutover and kit-slots. | NET-090, NET-110 | stated in its ticket |
| **NET-127**<br>fix, 3 days | **Mesh and encryption evidence without exec**<br>The mesh check and the transit-encryption script read their evidence from objects and metrics that the suite's service account can read. Neither runs a command inside an agent pod. A self run can then assert both. The exact source is unsettled and is fixed in the ticket from the census of NET-060. | NET-060, NET-131 | stated in its ticket |
| **NET-140**<br>delete, 3 days | **Delete mesh establishment from the API**<br>Thirteen establishment functions, the mode switch and the dial-port variable deleted; Secret copies left for SEC and CTL | NET-132, FND-011 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **NET-102**: `products/catalyst/chart/crds/sovereign.yaml`, `products/catalyst/chart/crds/tests/`, `tests/acceptance/sovereign/internal/suite/check_network_registry.go`
- **NET-153**: `clusters/_template/bootstrap-kit/06a-bp-self-sovereign-cutover.yaml`
- **NET-127**: `scripts/check-live-node-encryption.sh`
- **NET-140**: `products/catalyst/bootstrap/api/internal/handler/clustermesh.go`, `products/catalyst/bootstrap/api/internal/handler/clustermesh_test.go`, `products/catalyst/bootstrap/api/internal/handler/clustermesh_peer_names_test.go`, `products/catalyst/chart/values.yaml`, `products/catalyst/chart/templates/api-deployment.yaml`, `products/catalyst/chart/templates/api-deployment-kustomize.yaml`, `clusters/_template/bootstrap-kit/13-bp-catalyst-platform.yaml`, `infra/providers/_shared/cloudinit-control-plane.tftpl`, `infra/providers/huawei/main.tf`, `infra/providers/huawei/variables.tf`

</details>

## CTL Control plane

### Single tasks

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **CTL-160**<br>new, 5 days | **Package intent: validate, serialise, commit, wait**<br>intent.Validate, Serialize, Submit with no-op detection and informer sync-wait; cache adapter; rollback tool intent-detach.py | CTL-110 | stated in its ticket |
| **CTL-038**<br>new, 3 days | **The API accepts a Job's service-account token, confined to one Organization**<br>A projected token of service account catalyst-conformance in namespace catalyst-system, checked by TokenReview, may create, move and delete Applications in the Organization named conformance and nothing else. One middleware in a new package; AIP-120 extends it and adds no second one. | CTL-035 | stated in its ticket |
| **CTL-039**<br>fix, 3 days | **Read routes for the region registry and the Environment list**<br>GET /api/v1/sovereigns/{id}/regions and GET /api/v1/sovereigns/{id}/environments answer from the informer cache, in the shapes the suite already parses. The suite's two kubectl fallbacks are removed. | CTL-035 | stated in its ticket |
| **CTL-340**<br>rework, 5 days | **Jobs store subscribe; flow stream event-driven; jobs stream route**<br>jobs.Store.Subscribe, GET /api/v1/deployments/{depId}/jobs/stream, flow local stream without its 3-second ticker, no seed on every read | CTL-035 | stated in its ticket |
| **CTL-350**<br>rework, 3 days | **Applications views from the informer cache**<br>HandleSovereignApps reads the informer cache; two dead stream handlers and their helpers deleted | CTL-035 | stated in its ticket |
| **CTL-120**<br>new, 3 days | **Fleet intent path and its Kustomization**<br>Kustomization sovereign-intent in the writer region reading clusters/<fqdn>/intent with a placeholder file; absent in followers | CTL-060, CTL-110, FND-120 | stated in its ticket |
| **CTL-122**<br>new, 3 days | **Organization intent path, its Kustomization and its account**<br>Per-Organization Kustomization catalyst-tenant-<slug>-intent with impersonated account and Role, appended to the home-region file; existing five objects byte-identical | CTL-120, FND-120 | stated in its ticket |
| **CTL-130**<br>new, 3 days | **Receiver and system webhook**<br>Flux Receiver per region, one Gitea system webhook ensured by the organization-controller, ALLOWED_HOST_LIST, probe route and journey J6 | CTL-035, CTL-050, CTL-120, FND-032, FND-120, SEC-095, SEC-200, SEC-211, FND-011 | Adds the check CTL-G01. |
| **CTL-162**<br>rework, 5 days | **Batch A: Application intent from Git**<br>Five Application routes and the MCP tool commit Application files; adoption of existing Applications; iac copy deleted; journeys J7 and J8 as check files. One step added: before the commit, the create and update routes check spec.parameters against the Blueprint's configSchema and answer 422 schema-invalid. | CTL-122, CTL-130, CTL-160, FND-032, FND-130 | Makes CTL-G02, CTL-G03 required. |
| **CTL-360**<br>new, 5 days | **Live-query hook and removal of the first-rank polls**<br>useLiveQuery and shared stream; Applications, Catalog, console route, Jobs, job detail and logs pushed; pending chip and Git-apply banner | CTL-162, CTL-340, CTL-350, FND-161 | Makes CTL-G05 required. |

<details><summary>Files each task of this group changes</summary>

- **CTL-160**: `products/catalyst/bootstrap/api/internal/intent/**`, `products/catalyst/bootstrap/api/cmd/api/intent_wire.go`, `scripts/intent-detach.py`
- **CTL-038**: `products/catalyst/bootstrap/api/internal/jobauth/**`, `products/catalyst/bootstrap/api/cmd/api/routes*.go`
- **CTL-039**: `products/catalyst/bootstrap/api/internal/topologyread/**`, `products/catalyst/bootstrap/api/cmd/api/routes*.go`, `tests/acceptance/sovereign/internal/suite/discover.go`
- **CTL-340**: `products/catalyst/bootstrap/api/internal/jobs/store.go`, `products/catalyst/bootstrap/api/internal/jobs/stream.go`, `products/catalyst/bootstrap/api/cmd/api/routes_deployments.go`, `products/catalyst/bootstrap/api/internal/handler/flow_snapshot_local.go`, `jobs.go`, `scripts/baselines/polling.json`
- **CTL-350**: `products/catalyst/bootstrap/api/internal/handler/sovereign.go`, `applications.go`, `continuum_extras.go`, `products/catalyst/bootstrap/ui/src/lib/catalog.api.ts`, `products/catalyst/bootstrap/ui/src/lib/continuum.api.ts`, `scripts/baselines/*.json`
- **CTL-120**: `clusters/_template/sovereign-region/base/gitrepository.yaml`, `clusters/_template/sovereign-region/base/intent-kustomization.yaml`, `clusters/_template/sovereign-region/base/kustomization.yaml`, `clusters/_template/sovereign-region/multi-region-follower/kustomization.yaml`, `core/controllers/organization/cmd/main.go`, `core/controllers/organization/internal/intentroot/**`
- **CTL-122**: `core/controllers/organization/internal/controller/per_org_flux.go`, `core/controllers/organization/internal/gitops/manifests.go`
- **CTL-130**: `clusters/_template/sovereign-region/base/receiver.yaml`, `clusters/_template/sovereign-region/base/kustomization.yaml`, `core/controllers/pkg/gitea/client.go`, `core/controllers/organization/internal/hooks/**`, `core/controllers/organization/cmd/main.go`, `products/catalyst/chart/templates/controllers/organization-controller-*.yaml`, `platform/gitea/chart/values.yaml`, `products/catalyst/bootstrap/api/internal/intent/probe.go`, `products/catalyst/bootstrap/api/cmd/api/routes_sovereigns.go`, `docs/contracts/C9-mutating-routes.yaml`, `tests/acceptance/sovereign/internal/suite/check_ctl_push_apply.go`, `tests/acceptance/sovereign/internal/suite/check_ctl_push_apply_test.go` and 1 more
- **CTL-162**: `products/catalyst/bootstrap/api/internal/handler/handler.go`, `products/catalyst/bootstrap/api/internal/handler/applications.go`, `applications_update.go`, `endpoint_handler.go`, `org_applications.go`, `organization_funnel_apps.go`, `post_handover_spine_apps.go`, `products/catalyst/bootstrap/api/internal/intent/applications.go`, `products/openova-mcp/internal/tools/catalogue.go`, `products/openova-mcp/internal/tools/client.go`, `docs/contracts/C9-mutating-routes.yaml`, `scripts/baselines/*.json` and 4 more
- **CTL-360**: `products/catalyst/bootstrap/ui/src/lib/useLiveQuery.ts`, `src/lib/liveStream.ts`, `src/components/ExecutionLogs.tsx`, `scripts/baselines/polling.json`, `products/catalyst/bootstrap/ui/e2e/gate/live-views/**`, `docs/ledger/gates.yaml`

</details>

## CMP Compliance and posture

### Single tasks

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **CMP-030**<br>rework, 3 days | **Package internal/compliance: types and pure score functions**<br>Types of section 4.3 and rules 1 to 5 as pure functions with table and property tests | nothing else | stated in its ticket |
| **CMP-020**<br>fix, 3 days | **The API tells the truth: live mode, no fixture data, no invented list**<br>Mode read from the engine object; baseline-19 list, fixture app names and domain tiles deleted; server-side policy filter; live-read guard | CMP-010, FND-080, FND-011 | Makes CMP-G01, CMP-G02 required. |
| **CMP-032**<br>rework, 5 days | **The aggregator uses the package: subjects, one judge, seven results; duplicate evaluators deleted**<br>Read model keyed by source object, top-owner subjects, five evaluators deleted, golden before and after scorecards | CMP-020, CMP-030 | stated in its ticket |
| **CMP-150**<br>new, 5 days | **Regions and producers: the Clusters view**<br>Route R18, region filter on R1 to R5, screen S12, conformance results as their own group | CMP-091, CMP-101, CMP-111, CTL-150, FND-151 | Makes CMP-G24 required. |

<details><summary>Files each task of this group changes</summary>

- **CMP-030**: `products/catalyst/bootstrap/api/internal/compliance/types.go`, `products/catalyst/bootstrap/api/internal/compliance/model.go`, `products/catalyst/bootstrap/api/internal/compliance/score.go`, `products/catalyst/bootstrap/api/internal/compliance/score_test.go`, `products/catalyst/bootstrap/api/internal/compliance/testdata/**`
- **CMP-020**: `products/catalyst/bootstrap/api/internal/handler/compliance.go`, `products/catalyst/bootstrap/api/internal/handler/compliance_test.go`, `products/catalyst/bootstrap/api/internal/handler/compliance_runtime.go`, `products/catalyst/chart/templates/api-deployment.yaml`, `products/catalyst/chart/templates/api-deployment-kustomize.yaml`, `scripts/check-compliance-live-reads.sh`, `scripts/baselines/compliance-live-reads.json`, `docs/ledger/gates.yaml`
- **CMP-032**: `products/catalyst/bootstrap/api/internal/compliance/resolver.go`, `products/catalyst/bootstrap/api/internal/compliance/readmodel.go`, `products/catalyst/bootstrap/api/internal/compliance/adapters/**`, `products/catalyst/bootstrap/api/internal/handler/compliance.go`, `products/catalyst/bootstrap/api/internal/k8scache/evaluators/*.go`, `products/catalyst/bootstrap/api/cmd/api/evaluators_wire.go`, `docs/ledger/deleted-paths.txt`
- **CMP-150**: `products/catalyst/bootstrap/api/internal/compliance/producers.go`, `products/catalyst/bootstrap/api/internal/compliance/routes.go`, `products/catalyst/bootstrap/api/internal/compliance/adapters/policyreport.go`, `products/catalyst/bootstrap/ui/src/pages/sovereign/posture/ClustersTab.tsx`, `products/catalyst/bootstrap/ui/src/app/router.tsx`, `tests/acceptance/sovereign/internal/suite/check_regions.go`, `docs/ledger/gates.yaml`

</details>

### Group CMP-040: Epic: model and posture hub

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **CMP-036**<br>rework, 1 day | **Kit slot 27a: values document, hand-over variable, definitions on upgrade**<br>Slot gains valuesFrom (optional ConfigMap), values.compliancePolicies.sovereignConsume from ${SOVEREIGN_CONSUME:=origin}, crds: CreateReplace; wiring guard | nothing else | stated in its ticket |
| **CMP-041**<br>new, 5 days | **ComplianceModel: definition, data files, chart template, loader, exemptions**<br>Module kind with ten axes and 22 checks, 23 exemptions with reasons, per-cluster loader, catalogue and module-kind guards | CMP-016, CMP-032, CMP-036, CMP-014, FND-011 | Makes CMP-G03 required. |
| **CMP-045**<br>new, 5 days | **Posture routes and the change stream**<br>Routes R1 to R5 and R19 with change and resync frames and server-side paging | CMP-041, CTL-035 | Makes CMP-G04 required. |
| **CMP-052**<br>new, 5 days | **Posture hub: Overview, Workload, Readiness**<br>Screens S1 to S3 with all state in the address, on push | CMP-045, CTL-360, FND-161 | Makes CMP-G05 required. |

<details><summary>Files each task of this group changes</summary>

- **CMP-036**: `clusters/_template/bootstrap-kit/27a-kyverno-policies.yaml`, `scripts/check-policy-wiring.py`
- **CMP-041**: `platform/kyverno-policies/chart/crds/compliancemodel.yaml`, `platform/kyverno-policies/chart/model/**`, `platform/kyverno-policies/chart/templates/compliance-model.yaml`, `platform/kyverno-policies/chart/tests/model-render.sh`, `scripts/check-compliance-model.py`, `scripts/baselines/policy-suites.json`, `scripts/baselines/policy-exemptions.json`, `products/catalyst/bootstrap/api/internal/k8scache/kinds.go`, `products/catalyst/bootstrap/api/internal/compliance/model.go`, `products/catalyst/bootstrap/api/internal/compliance/readmodel.go`, `tests/acceptance/sovereign/internal/suite/check_compliance_model.go`, `docs/ledger/gates.yaml` and 1 more
- **CMP-045**: `products/catalyst/bootstrap/api/internal/compliance/routes.go`, `products/catalyst/bootstrap/api/internal/compliance/stream.go`, `products/catalyst/bootstrap/api/internal/compliance/rows.go`, `products/catalyst/bootstrap/api/internal/handler/compliance.go`, `tests/acceptance/sovereign/internal/suite/check_compliance_posture.go`, `docs/ledger/gates.yaml`
- **CMP-052**: `products/catalyst/bootstrap/ui/src/pages/sovereign/posture/**`, `products/catalyst/bootstrap/ui/src/lib/useLiveQuery.ts`, `products/catalyst/bootstrap/ui/src/app/router.tsx`, `products/catalyst/bootstrap/ui/src/pages/sovereign/sovereignNav.ts`, `products/catalyst/bootstrap/ui/src/pages/sovereign/AppDetail/ComplianceTab.tsx`, `products/catalyst/bootstrap/ui/src/pages/sovereign/cloud-list/ResourceComplianceTab.tsx`, `products/catalyst/bootstrap/ui/e2e/gate/posture/hub.spec.ts`, `docs/ledger/gates.yaml`

</details>

### Group CMP-070: Epic: enforcement mode owned in Git

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **CMP-071**<br>rework, 3 days | **Policy files in every region's folder: scopes, the alignment function, the empty document**<br>FleetPolicy and FleetEvidence scopes; Regions, WriteAll, Serialize and EnsurePolicyFiles; empty values document in every region | CMP-030, CMP-036, CTL-035, CTL-045, CTL-110, FND-120 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **CMP-071**: `core/controllers/pkg/gitwrite/**`, `docs/contracts/C4-git-layout-and-commit-library.md`, `products/catalyst/bootstrap/api/internal/compliance/policyfiles.go`, `products/catalyst/bootstrap/api/internal/compliance/policyfiles_test.go`, `products/catalyst/bootstrap/api/cmd/api/**`

</details>

### Group CMP-080: Epic: decisions (waivers and remediation requests)

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **CMP-081**<br>new, 5 days | **ComplianceDecision: kind, admission rule, routes, effect on findings**<br>Waiver kind, native admission rule, routes R8 to R11 writing every region's folder, derived state, expiry timer | CMP-016, CMP-045, CMP-071, CTL-160, FND-011 | Makes CMP-G09 required. |

<details><summary>Files each task of this group changes</summary>

- **CMP-081**: `platform/kyverno-policies/chart/crds/compliancedecision.yaml`, `platform/kyverno-policies/chart/templates/decision-admission.yaml`, `platform/kyverno-policies/chart/crds/compliancemodel.yaml`, `platform/kyverno-policies/chart/model/model.yaml`, `products/catalyst/bootstrap/api/internal/k8scache/kinds.go`, `products/catalyst/bootstrap/api/internal/compliance/decisions/**`, `products/catalyst/bootstrap/api/internal/compliance/readmodel.go`, `docs/contracts/C9-mutating-routes.yaml`, `tests/acceptance/sovereign/internal/suite/check_decisions.go`, `docs/ledger/gates.yaml`

</details>

### Group CMP-090: Epic: scanner reports

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **CMP-091**<br>new, 5 days | **Scanner kinds in the cache; vulnerabilities by workload**<br>Six scanner kinds as trimmed optional informer kinds, rule V and rule E, route R12, request-time routes deleted | CMP-016, CMP-045, CMP-S3, FND-011 | Makes CMP-G13 required. |

<details><summary>Files each task of this group changes</summary>

- **CMP-091**: `products/catalyst/bootstrap/api/internal/k8scache/kinds.go`, `products/catalyst/bootstrap/api/internal/k8scache/factory.go`, `products/catalyst/bootstrap/api/internal/compliance/adapters/vulnerability.go`, `products/catalyst/bootstrap/api/internal/compliance/resolver.go`, `products/catalyst/bootstrap/api/internal/compliance/routes.go`, `products/catalyst/bootstrap/api/internal/handler/compliance_runtime.go`, `platform/kyverno-policies/chart/model/**`, `platform/kyverno-policies/chart/crds/compliancemodel.yaml`, `tests/acceptance/sovereign/internal/suite/check_vulnerabilities.go`, `scripts/baselines/compliance-live-reads.json`, `docs/ledger/gates.yaml`

</details>

### Group CMP-110: Epic: resilience

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **CMP-111**<br>new, 5 days | **Derived checks on events and deadlines; backup recency**<br>Evaluator engine on events and next-change deadlines with no ticker; rule B; route R14; annotation policy deleted | CMP-016, CMP-045, FND-011, NET-030 | Makes CMP-G17 required. |

<details><summary>Files each task of this group changes</summary>

- **CMP-111**: `products/catalyst/bootstrap/api/internal/k8scache/evaluators/evaluators.go`, `products/catalyst/bootstrap/api/internal/k8scache/evaluators/backup.go`, `products/catalyst/bootstrap/api/cmd/api/evaluators_wire.go`, `products/catalyst/bootstrap/api/internal/k8scache/kinds.go`, `products/catalyst/bootstrap/api/internal/compliance/routes.go`, `platform/kyverno-policies/chart/templates/baseline/20-backup-configured.yaml`, `platform/kyverno-policies/chart/values.yaml`, `platform/kyverno-policies/chart/model/checks.yaml`, `tests/acceptance/sovereign/internal/suite/check_backup_recency.go`, `docs/ledger/gates.yaml`

</details>

### Group CMP-100: Epic: runtime

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **CMP-101**<br>new, 5 days | **Runtime detections by push: side-car to NATS, consumer, view**<br>Side-car on with NATS output, consumer and aggregate for the primary region, other regions listed as not delivered, screen S5, old panel and its poll deleted | CMP-045, CMP-052, CMP-081, CMP-S2, CTL-045, FND-011 | Makes CMP-G16 required. |

<details><summary>Files each task of this group changes</summary>

- **CMP-101**: `platform/falco/chart/values.yaml`, `docs/contracts/C3-event-subjects.yaml`, `products/catalyst/bootstrap/api/internal/compliance/adapters/runtime.go`, `products/catalyst/bootstrap/api/internal/compliance/adapters/runtime_event.schema.json`, `products/catalyst/bootstrap/api/internal/compliance/routes.go`, `products/catalyst/bootstrap/api/internal/handler/compliance_runtime.go`, `platform/kyverno-policies/chart/model/model.yaml`, `platform/kyverno-policies/chart/crds/compliancemodel.yaml`, `products/catalyst/bootstrap/ui/src/pages/sovereign/posture/security/RuntimeView.tsx`, `products/catalyst/bootstrap/ui/src/pages/admin/compliance/FalcoAlerts.tsx`, `products/catalyst/bootstrap/ui/src/pages/admin/compliance/RuntimeAlertsPage.tsx`, `products/catalyst/bootstrap/ui/src/app/router.tsx` and 4 more

</details>

