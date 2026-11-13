# Stage 6 (W2): First modules

Compliance screens, billing, the model gateway and agent runtime, access, machine provisioning.

147 tasks. One task is one pull request. Rows are in the order the tasks can run. "Waits for" lists the tasks that must be merged first; every task also waits for the first two tasks of the plan, `FND-021` and `FND-022`.

[Back to the overview](README.md)

## Areas in this stage

- [FND: Foundation gate and execution rules](#fnd-foundation-gate-and-execution-rules) — 7 tasks
- [CLD: Cloud abstraction](#cld-cloud-abstraction) — 2 tasks
- [SOV: Sovereign lifecycle](#sov-sovereign-lifecycle) — 1 tasks
- [SEC: Secrets and root of trust](#sec-secrets-and-root-of-trust) — 2 tasks
- [NET: Network, zones and cluster classes](#net-network-zones-and-cluster-classes) — 7 tasks
- [SVC: Service model](#svc-service-model) — 7 tasks
- [CTL: Control plane](#ctl-control-plane) — 9 tasks
- [CMP: Compliance and posture](#cmp-compliance-and-posture) — 30 tasks
- [BSS: Commerce and billing](#bss-commerce-and-billing) — 23 tasks
- [AIP: Agent plane](#aip-agent-plane) — 23 tasks
- [INF: Infrastructure plane](#inf-infrastructure-plane) — 16 tasks
- [ACC: Access plane](#acc-access-plane) — 17 tasks
- [PRD: Later products](#prd-later-products) — 3 tasks

## FND Foundation gate and execution rules

### Single tasks

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **FND-038**<br>test, 3 days | **Suite helper: a test Organization and a Blueprint install**<br>Any check can create a test Organization, install a named Blueprint in it and remove both, through the console API. | FND-032, FND-120 | stated in its ticket |
| **FND-170**<br>delete, 3 days | **Remove the outside-witness worker, if the region-loss record does not use it**<br>The outside-witness worker is gone, unless decision record 0027 keeps it as the write lease outside both regions. | FND-120 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **FND-038**: `tests/acceptance/sovereign/internal/suite/fixture_org.go`, `tests/acceptance/sovereign/internal/suite/fixture_org_test.go`
- **FND-170**: `products/continuum/cloudflare-worker/**`, `infra/cloudflare-worker-leases/**`, `.github/workflows/cloudflare-worker-leases-build.yaml`, `docs/ledger/deleted-paths.txt`

</details>

### Group FND-160: Console regression as code

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **FND-162**<br>test, 5 days | **Console regression: applications grid, application detail, catalogue**<br>The ledger rows about the applications grid, application detail and the catalogue are browser tests behind gate check FND-G20. | FND-161 | Makes FND-G20 required. |
| **FND-163**<br>test, 5 days | **Console regression: jobs, resource views, cloud and infrastructure read pages**<br>The ledger rows about jobs, resource views and the cloud and infrastructure read pages are browser tests behind gate check FND-G21. | FND-161, FND-162 | Makes FND-G21 required. |
| **FND-164**<br>test, 5 days | **Console regression: Organizations, users and access, settings**<br>The ledger rows about Organizations, users and access, and settings are browser tests behind gate check FND-G22. | FND-161, FND-163 | Makes FND-G22 required. |
| **FND-165**<br>test, 5 days | **Console regression: compliance, sovereignty and hand-over pages, storefront pages**<br>The ledger rows about compliance, sovereignty, hand-over and the storefront are browser tests behind gate check FND-G23. | FND-161, FND-164 | Makes FND-G23 required. |
| **FND-166**<br>delete, 3 days | **Retire the ledger tooling**<br>The hand-walk tooling of the ledger is gone, and the regression count is printed from the row map and the verdict. | FND-162, FND-163, FND-164, FND-165 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **FND-162**: `products/catalyst/bootstrap/ui/e2e/gate/applications/**`, `docs/ledger/UAT.md`, `docs/ledger/uat-row-map.csv`, `docs/ledger/gates.yaml`
- **FND-163**: `products/catalyst/bootstrap/ui/e2e/gate/operations/**`, `docs/ledger/UAT.md`, `docs/ledger/uat-row-map.csv`, `docs/ledger/gates.yaml`
- **FND-164**: `products/catalyst/bootstrap/ui/e2e/gate/organizations/**`, `docs/ledger/UAT.md`, `docs/ledger/uat-row-map.csv`, `docs/ledger/gates.yaml`
- **FND-165**: `products/catalyst/bootstrap/ui/e2e/gate/assurance/**`, `core/marketplace/e2e/gate/**`, `docs/ledger/UAT.md`, `docs/ledger/uat-row-map.csv`, `docs/ledger/gates.yaml`
- **FND-166**: `scripts/check-function-rows-cite-acceptance.py`, `scripts/check-uat-fix-sha-citations.py`, `scripts/check-uat-walkability.sh`, `scripts/check-walk-respects-scheduler.sh`, `scripts/classify-uat-delivery-state.py`, `scripts/gen-uat-final-report.py`, `scripts/reset-uat.py`, `scripts/test_uat_clause_identity.py`, `scripts/test_uat_sso_flip_evidence_floor.py`, `scripts/test_uat_table_shape.py`, `scripts/test_uat_tally.py`, `scripts/uat-3375-topology-probe.mjs` and 46 more

</details>

## CLD Cloud abstraction

### Group CLD-180: Epic: second-provider proof, Hetzner single region

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **CLD-184**<br>rework, 5 days | **Second provider: front doors declared by the module on host ports; the value controller leaves the schema**<br>Second provider: front doors declared by the module on host ports; the value controller leaves the schema. | CLD-182 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **CLD-184**: `infra/providers/hetzner/main.tf`, `infra/providers/hetzner/profile.yaml`, `infra/providers/hetzner/tests/**`, `clusters/_template/providers/hetzner/**`, `infra/providers/_contract/cloudprofile.schema.json`

</details>

### Single tasks

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **CLD-221**<br>new, 5 days | **Object storage, native side: unit module day2/objectbucket and Composition objectbucket-native**<br>For the Huawei and local packs: a unit module that creates one bucket and one scoped key and writes exactly the keys of SVC's object-storage contract version 1 to the OpenBao path it receives as binding_path, with the write-only producer identity. No sensitive output. A pipeline Composition objectbucket-native. day2.contracts gains objectbucket. The definition is SVC's (SVC-290). | CLD-175, FND-032, SVC-270 | Makes CLD-G18 required. |

<details><summary>Files each task of this group changes</summary>

- **CLD-221**: `tests/acceptance/sovereign/internal/suite/check_*.go`

</details>

## SOV Sovereign lifecycle

### Group SOV-900: EPIC: After the proof: decommission, mothership rebuild, orchestration move, region add

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **SOV-910**<br>new, 3 days | **Decommission by the owner, outside the cluster**<br>Subcommand decommission; runbook; gate teardown of released environments | SOV-435, SOV-440 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **SOV-910**: `core/cmd/sovereign-ops/internal/restore/decommission.go`, `docs/RUNBOOKS.md`, `tests/hermetic/handover/`, `scripts/sovereign-lifecycle.sh`

</details>

## SEC Secrets and root of trust

### Single tasks

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **SEC-310**<br>new, 3 days | **SSH certificate authority**<br>Mount ssh/, one authority per region, role node-admin. | SEC-161, SEC-190, SEC-250, FND-011 | stated in its ticket |
| **SEC-330**<br>new, 5 days | **TLS on the OpenBao listener**<br>Listener certificate from a bootstrap authority; every client on https. | SEC-162, SEC-211, FND-011 | Makes SEC-G16 required. |

<details><summary>Files each task of this group changes</summary>

- **SEC-310**: `platform/openbao/chart/values.yaml`, `platform/openbao/chart/templates/_config.tpl`, `platform/openbao/chart/templates/pki-bootstrap-job.yaml`
- **SEC-330**: `platform/openbao/chart/templates/listener-certificate.yaml`, `platform/openbao/chart/values.yaml`, `platform/external-secrets-stores/chart/templates/*.yaml`, `platform/secrets-lib/chart/templates/*.tpl`, `clusters/_template/bootstrap-kit/08-openbao.yaml`

</details>

## NET Network, zones and cluster classes

### Group NET-250: Epic: a web application firewall in the path

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **NET-251**<br>decision, 1 day | **Decision record: firewall mechanism and the package list of epic NET-250**<br>Decision record: firewall mechanism and the package list of epic NET-250. | NET-270 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **NET-251**: `docs/adr/README.md`

</details>

### Group NET-180: Epic: edge pool and a load balancer per region

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **NET-181**<br>new, 5 days | **Edge node pool, added beside the current entry**<br>Variable edge_pool; two tainted, labelled edge nodes per region without public addresses in their own security group, added as extra load-balancer members | CLD-090, NET-011, NET-100, NET-132, NET-166 | stated in its ticket |
| **NET-182**<br>fix, 3 days | **Gateway listeners bind on edge nodes only**<br>gatewayAPI.hostNetwork.nodes.matchLabels set to the edge label; tolerations for Envoy and agent | NET-181, FND-011 | stated in its ticket |
| **NET-183**<br>delete, 3 days | **Close the old entry**<br>Edge nodes are the only load-balancer members; web ports and ICMP no longer open on the general security group | NET-182 | Makes NET-G13 required. |
| **NET-184**<br>new, 5 days | **A load balancer per region**<br>Balancers, pools, listeners and members per region with moved blocks; secondary weight variable deleted; output edge_addresses | NET-183 | stated in its ticket |
| **NET-185**<br>fix, 5 days | **DNS front end on the edge pool**<br>dnsdist DaemonSet on edge nodes with host port 53 behind the regional balancers; LB-IPAM pool deleted | NET-184, SOV-310, FND-011 | stated in its ticket |
| **NET-200**<br>new, 5 days | **Entry failover by DNS**<br>ifportup records for the wildcard and apex in region order, written by the existing hook; suite step ENTRY | NET-184, FND-011 | Makes NET-G14 required. |

<details><summary>Files each task of this group changes</summary>

- **NET-181**: `infra/providers/huawei/main.tf`, `infra/providers/huawei/variables.tf`, `infra/providers/huawei/outputs.tf`, `infra/providers/huawei/cloudinit-worker.tftpl`, `infra/providers/huawei/tests/multi_region.tftest.hcl`
- **NET-182**: `clusters/_template/bootstrap-kit/01-cilium.yaml`, `platform/cilium/chart/tests/`
- **NET-183**: `infra/providers/huawei/main.tf`, `infra/providers/huawei/tests/multi_region.tftest.hcl`
- **NET-184**: `infra/providers/huawei/main.tf`, `infra/providers/huawei/variables.tf`, `infra/providers/huawei/outputs.tf`, `infra/providers/huawei/tests/multi_region.tftest.hcl`
- **NET-185**: `platform/powerdns/chart/templates/dnsdist.yaml`, `clusters/_template/bootstrap-kit/11-powerdns.yaml`, `infra/providers/huawei/main.tf`, `platform/cilium/chart/templates/sovereign-vip-lb-ipam-pool.yaml`, `clusters/_template/bootstrap-kit/01-cilium.yaml`, `infra/providers/_shared/cloudinit-control-plane.tftpl`
- **NET-200**: `platform/powerdns/chart/templates/geo-failover-configmap.yaml`, `platform/powerdns/chart/templates/zone-bootstrap-job.yaml`, `platform/powerdns/chart/values.yaml`, `clusters/_template/bootstrap-kit/11-powerdns.yaml`, `infra/providers/_shared/cloudinit-control-plane.tftpl`, `tests/acceptance/sovereign/internal/suite/entry_failover.go`

</details>

## SVC Service model

### Single tasks

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **SVC-340**<br>lab test, 3 days | **Spike: credential rotation on a native cache**<br>Result record: OpenBao plug-in, cloud API rotation, or neither; decides whether a cache capability epic may be written | nothing else | stated in its ticket |
| **SVC-365**<br>delete, 1 day | **Stop serving v1alpha1 of the Blueprint definition**<br>The v1alpha1 entry leaves the versions list of the Blueprint definition; v1 stays served and stored. Precondition: no tracked file outside docs carries apiVersion catalyst.openova.io/v1alpha1 with kind Blueprint, and SVC-040 has been on main for one release train. Stop condition: a writer in the API or a controller still emits v1alpha1 for this kind. | CTL-240, SVC-040, SVC-160, SVC-190, FND-011 | stated in its ticket |
| **SVC-185**<br>fix, 3 days | **Conditional: a Blueprint declaration that pins an Application to its region**<br>Written only if spike INF-210 answers B no or D no; otherwise it leaves the plan. One optional Blueprint field (name unsettled) with its three readers in the same pull request: the install route, the edit route and the Primary move refuse a change of region with 422 placement-immutable. First user: bp-virtual-machine. | FND-120, INF-210, SVC-170, FND-011 | stated in its ticket |
| **SVC-360**<br>delete, 3 days | **Delete the unused backing-service generator (after the topology freeze)**<br>backingservice package, BackingServiceSpec, ProducesInstancesSpec and producesInstances removed. Precondition: python3 scripts/plan.py state FND-120 --no-fetch prints merged. Trailer Decision: ADR-0020. | FND-091, FND-120, SVC-010, SVC-260, FND-011 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **SVC-365**: `products/catalyst/chart/crds/blueprint.yaml`, `.github/workflows/preflight-blueprint-crd-cel.yaml`, `docs/ledger/deleted-paths.txt`
- **SVC-185**: `products/catalyst/chart/crds/blueprint.yaml`, `platform/_schemas/blueprint.schema.json`, `scripts/generate-catalog.py`
- **SVC-360**: `core/controllers/pkg/backingservice/`, `core/controllers/application/internal/controller/placement_recon.go`, `core/controllers/application/internal/controller/application_controller.go`, `core/controllers/pkg/apis/blueprint/v1alpha1/topology_types.go`, `products/catalyst/chart/crds/blueprint.yaml`, `platform/cnpg/blueprint.yaml`, `platform/_schemas/blueprint.schema.json`, `scripts/generate-catalog.py`, `docs/ledger/deleted-paths.txt`

</details>

### Group SVC-240: Epic: first capability with two implementations, object storage

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **SVC-290**<br>new, 5 days | **Native object bucket and the swap test**<br>ObjectBucket definition with required binding.path; bp-object-bucket-managed rendering one resource per Context; no provider label and no selector; availability read from spec.services.objectbuckets of the cloud profile through cloudprofile.ForSovereign(); swap check SVC-G02 implemented and set to required. | CLD-080, CLD-113, CLD-221, FND-038, SVC-280, FND-011 | Makes SVC-G02 required. |
| **SVC-300**<br>new, 5 days | **Catalogue choices on the Sovereign kind and the admin screen**<br>spec.catalog.capabilities[] on the Sovereign definition (the item schema of section 4.5, unchanged); GET and PUT policy routes, where PUT commits under clusters/<fqdn>/intent/catalog/ through the commit library in library mode and never writes to the cluster; the registry reads the block from the Sovereign object; Capabilities tab. It also waits for the package that gives the Sovereign kind its writer, which has no identifier yet. | CTL-044, CTL-110, CTL-120, SVC-290, FND-011 | Makes SVC-G03 required. |

<details><summary>Files each task of this group changes</summary>

- **SVC-290**: `platform/crossplane-claims/chart/templates/xrds/objectbucket.yaml`, `platform/object-bucket-managed/`, `tests/acceptance/sovereign/internal/suite/check_svc_swap.go`, `docs/ledger/gates.yaml`
- **SVC-300**: `products/catalyst/bootstrap/api/internal/k8scache/kinds.go`, `products/catalyst/bootstrap/api/internal/handler/catalog_policy.go`, `products/catalyst/bootstrap/ui/src/pages/sovereign/PlatformPage.tsx`, `tests/acceptance/sovereign/internal/suite/check_svc_policy.go`, `docs/ledger/gates.yaml`, `products/catalyst/chart/crds/sovereign.yaml`

</details>

### Group SVC-310: Epic: PostgreSQL as a capability

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **SVC-320**<br>new, 5 days | **The postgres contract, in-cluster**<br>postgres contract v1; bp-postgres writes contract keys to the binding path for capability Contexts; probe postgres-v1; create-new and reuse journey check | FND-038, SVC-300, SVC-315, FND-011 | Makes SVC-G04 required. |

<details><summary>Files each task of this group changes</summary>

- **SVC-320**: `platform/_capabilities/postgres.yaml`, `platform/postgres/blueprint.yaml`, `platform/postgres/chart/templates/`, `core/cmd/capability-probe/`, `platform/capability-probe/`, `tests/acceptance/sovereign/internal/suite/check_svc_postgres.go`, `docs/ledger/gates.yaml`

</details>

## CTL Control plane

### Single tasks

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **CTL-700**<br>new, 3 days | **Pull-request mode of the commit library**<br>gitwrite Change.Mode pull-request and Result.PullRequestURL; intent.Submit answers 202 awaiting-approval for that mode; contract C4 updated. No console listing and no route uses it here: the first user, AIP-245, brings its own. | CMP-005, CTL-110, CTL-160 | stated in its ticket |
| **CTL-800**<br>decision, 3 days | **Decision record 0027: the region-loss contract**<br>Record 0027 from text the ticket carries; the architect writes it with area NET for the mesh part. It holds: recovery point and recovery time per data class as numbers, with recovery in minutes by operator-approved promotion through a Git commit; the follower's Git copy after the spike; the dr-leases bucket; whether the outside-witness worker is the write lease; lock stage topology; the record shape of per-Application DNS that follows the Primary; what remains of the mesh loop in the API; the package that implements promotion; one Unfreezes line for each topology entry that CTL-810, CTL-815 and CTL-820 touch. | CTL-055, FND-120 | stated in its ticket |
| **CTL-810**<br>delete, 3 days | **Delete the legacy promoter and failback templates**<br>The four template files (4,956 lines at 2c1f5b0c0) and the values only they read are gone (deletion list item 29). Precondition: record 0027 says nothing renders them any more, or the package it names for that has merged. | CTL-800, FND-011 | stated in its ticket |
| **CTL-815**<br>delete, 1 day | **One Continuum definition file**<br>One definition file is the source and the lockstep script is gone (deletion list item 42). Which file stays is stated in the ticket; it is unsettled here. Lock crds. | CTL-800, FND-011 | stated in its ticket |
| **CTL-820**<br>test, 5 days | **Region-loss gate check**<br>Check CTL-G10: after the loss of one region, recovery by operator-approved promotion through a Git commit stays within the numbers of decision record 0027 for each data class. It registers itself with RegisterCheck and is set to required. | CTL-800, FND-034 | Makes CTL-G10 required. |
| **CTL-195**<br>new, 3 days | **Entitlement decisions arrive as events**<br>Durable consumer catalyst-api-entitlement calling the committer | BSS-111, CTL-190, CTL-320 | stated in its ticket |
| **CTL-264**<br>guard, 1 day | **Lock stage flux to deny**<br>Stage flux is denied and write verbs on the five Flux kinds are removed from non-Flux roles, by the steps of CTL-178. Precondition: the two newest gate records show stage flux with no non-Flux spec manager and an audit count of 0 or null. | CTL-178, CTL-260, SOV-240, FND-011 | stated in its ticket |
| **CTL-415**<br>fix, 3 days | **Identity values reach every Application of a customer Organization**<br>Identity values reach every Application of a customer Organization. | CTL-410, FND-120 | stated in its ticket |
| **CTL-710**<br>rework, 5 days | **Push for the Organization console and the storefront**<br>Organization-scoped state stream through the gateway; four timers replaced by EventSource with disconnected-only fallback | BSS-010, CTL-205, CTL-360 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **CTL-700**: `core/controllers/pkg/gitwrite/**`, `products/catalyst/bootstrap/api/internal/intent/**`, `docs/contracts/C4-git-layout-and-commit-library.md`
- **CTL-800**: `docs/adr/0027-region-loss-contract.md`, `docs/adr/README.md`
- **CTL-810**: `platform/cnpg-pair/chart/templates/dr-promoter.yaml`, `platform/cnpg-pair/chart/templates/dr-failback.yaml`, `platform/postgres/chart/templates/dr-promoter.yaml`, `platform/postgres/chart/templates/dr-failback.yaml`, `platform/cnpg-pair/chart/values.yaml`, `platform/cnpg-pair/chart/tests/**`, `platform/postgres/chart/values.yaml`, `platform/postgres/chart/tests/**`, `scripts/baselines/polling.json`, `docs/ledger/deleted-paths.txt`
- **CTL-815**: `products/catalyst/chart/crds/continuum.yaml`, `products/continuum/chart/crds/continuum.yaml`, `scripts/check-continuum-crd-lockstep.sh`, `docs/ledger/deleted-paths.txt`
- **CTL-820**: `tests/acceptance/sovereign/internal/suite/check_ctl_region_loss.go`, `tests/acceptance/sovereign/internal/suite/check_ctl_region_loss_test.go`, `docs/ledger/gates.yaml`
- **CTL-195**: `products/catalyst/bootstrap/api/internal/intent/entitlement_consumer.go`, `products/catalyst/bootstrap/api/cmd/api/intent_wire.go`, `docs/contracts/C3-event-subjects.yaml`
- **CTL-264**: `products/catalyst/chart/values.yaml`, `products/catalyst/chart/templates/**`, `scripts/baselines/lock-stages.json`
- **CTL-415**: `core/controllers/application/internal/controller/application_controller.go`, `core/controllers/application/internal/controller/*_test.go`, `core/controllers/application/internal/controller/testdata/**`
- **CTL-710**: `core/console/src/components/AppsPage.svelte`, `core/console/src/components/JobsPage.svelte`, `core/marketplace/src/components/CheckoutStep.svelte`, `core/marketplace/src/components/LaunchingStep.svelte`

</details>

## CMP Compliance and posture

### Group CMP-070: Epic: enforcement mode owned in Git

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **CMP-073**<br>rework, 3 days | **The bootstrap window follows the hand-over variable**<br>Chart value sovereignConsume; window is bootstrapMode AND sovereignConsume not local; default render unchanged | CMP-014, FND-011, NET-030 | stated in its ticket |
| **CMP-072**<br>new, 5 days | **Mode change from the screen**<br>Route R7 commits the values document to every region; Policies screen with impact list; old route, resolver and EnvironmentPolicy.spec.compliance deleted | CMP-036, CMP-041, CMP-052, CMP-071, CMP-S6, CTL-045, CTL-160, FND-011 | Makes CMP-G08 required. |
| **CMP-076**<br>delete, 3 days | **Delete the parent's enforce patch**<br>Live HelmRelease patch and its calls removed; window proven closed by the hand-over variable in every region | CMP-036, CMP-073, SOV-150, SOV-240 | Makes CMP-G26 required. |
| **CMP-074**<br>fix, 1 day | **Lock stage policy to deny**<br>Intent lock stage policy set to deny; write verbs on this area's kinds removed from the API role | CMP-072, CMP-076, CMP-081, CMP-083, CTL-150 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **CMP-073**: `platform/kyverno-policies/chart/values.yaml`, `platform/kyverno-policies/chart/templates/_helpers.tpl`, `platform/kyverno-policies/chart/tests/bootstrap-window.sh`, `scripts/run-policy-chart-tests.sh`
- **CMP-072**: `products/catalyst/bootstrap/api/internal/compliance/mode.go`, `products/catalyst/bootstrap/api/internal/compliance/routes.go`, `products/catalyst/bootstrap/api/internal/handler/policy_mode.go`, `products/catalyst/bootstrap/api/internal/handler/install_refs.go`, `products/catalyst/bootstrap/api/internal/handler/keycloak_proxy.go`, `products/catalyst/bootstrap/api/internal/handler/compliance.go`, `products/catalyst/bootstrap/api/cmd/api/main.go`, `products/catalyst/chart/crds/environmentpolicy.yaml`, `docs/contracts/C9-mutating-routes.yaml`, `products/catalyst/bootstrap/ui/src/pages/sovereign/posture/PoliciesTab.tsx`, `products/catalyst/bootstrap/ui/src/pages/sovereign/posture/ModeDialog.tsx`, `products/catalyst/bootstrap/ui/src/app/router.tsx` and 2 more
- **CMP-076**: `products/catalyst/bootstrap/api/internal/handler/post_handover_policy_enforce.go`, `products/catalyst/bootstrap/api/internal/handler/post_handover_policy_enforce_test.go`, `products/catalyst/bootstrap/api/internal/handler/phase1_watch.go`, `products/catalyst/bootstrap/api/internal/handler/phase1_converged_late.go`, `tests/acceptance/sovereign/internal/suite/check_policy_window.go`, `docs/ledger/gates.yaml`, `docs/ledger/deleted-paths.txt`

</details>

### Single tasks

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **CMP-022**<br>delete, 3 days | **The console stops claiming what does not exist**<br>Security Lead page, framework chips, mode toggle and token-only text removed; policies table added; old browser spec trimmed | CMP-020 | stated in its ticket |
| **CMP-131**<br>new, 5 days | **Daily snapshot, history route and screen; the unread history bucket goes**<br>One immutable snapshot commit per day, routes R16 and R20, screen S13, key-value bucket and publisher deleted | CMP-045, CMP-052, CMP-071, CTL-045, FND-011 | Makes CMP-G22 required. |
| **CMP-154**<br>new, 3 days | **Secrets axis: the signals of the secrets area**<br>Secrets axis: the signals of the secrets area. | CMP-092, SEC-095, FND-011 | Makes CMP-G29 required. |
| **CMP-141**<br>new, 5 days | **Organization view**<br>Read routes and request routes under /api/v1/org/compliance with scope forced from the session; hub on the Organization host | CMP-082, CMP-085, CMP-093, CMP-101, CMP-113, CMP-121, CMP-131, FND-038 | Makes CMP-G23 required. |
| **CMP-152**<br>new, 3 days | **Break-glass uses on the Clusters view**<br>A consumer of catalyst.audit.recorded.v1 counts audit events of type break-glass per region; route R18 returns the count and the Clusters view shows it by push. Never scored. Direct cluster writes with the break-glass group are not counted (they are only in the API server's audit log). | CMP-150, CTL-230, CTL-320 | Makes CMP-G30 required. |

<details><summary>Files each task of this group changes</summary>

- **CMP-022**: `products/catalyst/bootstrap/ui/src/pages/admin/compliance/**`, `products/catalyst/bootstrap/ui/src/lib/compliance.api.ts`, `products/catalyst/bootstrap/ui/src/widgets/compliance/PolicyModeToggle.tsx`, `products/catalyst/bootstrap/ui/src/app/router.tsx`, `products/catalyst/bootstrap/ui/e2e/compliance-dashboards.spec.ts`, `docs/ledger/deleted-paths.txt`
- **CMP-131**: `products/catalyst/bootstrap/api/internal/compliance/snapshot.go`, `products/catalyst/bootstrap/api/internal/compliance/routes.go`, `products/catalyst/bootstrap/api/internal/natspub/compliance_rollup_publisher.go`, `products/catalyst/bootstrap/api/cmd/api/main.go`, `products/catalyst/bootstrap/api/internal/handler/compliance.go`, `platform/nats-jetstream/chart/templates/kv-buckets.yaml`, `platform/nats-jetstream/chart/values.yaml`, `products/catalyst/bootstrap/ui/src/pages/sovereign/posture/HistoryTab.tsx`, `products/catalyst/bootstrap/ui/src/app/router.tsx`, `tests/acceptance/sovereign/internal/suite/check_snapshot.go`, `docs/ledger/gates.yaml`, `docs/contracts/C3-event-subjects.yaml` and 1 more
- **CMP-154**: `platform/kyverno-policies/chart/model/checks.yaml`, `docs/ledger/gates.yaml`
- **CMP-141**: `products/catalyst/bootstrap/api/internal/compliance/orgroutes.go`, `products/catalyst/bootstrap/api/internal/compliance/orgroutes_test.go`, `products/catalyst/bootstrap/api/internal/handler/org_scope.go`, `products/catalyst/bootstrap/ui/src/pages/sovereign/posture/PostureLayout.tsx`, `products/catalyst/bootstrap/ui/src/pages/sovereign/SovereignSidebar.tsx`, `tests/acceptance/sovereign/internal/suite/check_org_posture.go`, `docs/ledger/gates.yaml`
- **CMP-152**: `products/catalyst/bootstrap/api/internal/compliance/routes.go`, `products/catalyst/bootstrap/ui/src/pages/sovereign/posture/ClustersTab.tsx`, `docs/ledger/gates.yaml`

</details>

### Group CMP-060: Epic: policy migration

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **CMP-058**<br>fix, 1 day | **Engine to the 1.19 line**<br>Engine and tool on the newest 1.19 patch release named in the ticket | CMP-012, CMP-014, CMP-020, FND-011 | stated in its ticket |
| **CMP-061**<br>rework, 3 days | **Engine values, informer kinds and mode adapter for the current policy kinds**<br>Two unused label policies deleted, exceptions switched on in one namespace, new policy kinds registered as optional | CMP-014, CMP-016, CMP-032, CMP-058, CMP-S1, FND-011 | stated in its ticket |
| **CMP-062**<br>rework, 5 days | **Validate policies to ValidatingPolicy, batch 1 (7 policies)**<br>Seven policies rewritten in place, each with a suite green before and after on the same resources | CMP-061, CMP-073, FND-011 | stated in its ticket |
| **CMP-068**<br>new, 3 days | **Pod security: enforce baseline on Organization namespaces**<br>Three pod-security labels on the Organization namespace template, the check namespace-pod-security-enforced, and proof that a Pod with a CSI volume and no service-account token is admitted | CMP-041, CMP-S5, FND-038, FND-011 | Makes CMP-G07 required. |
| **CMP-063**<br>rework, 5 days | **Validate policies to ValidatingPolicy, batch 2 (7 policies)**<br>Validate policies to ValidatingPolicy, batch 2 (7 policies). | CMP-062, FND-011 | stated in its ticket |
| **CMP-064**<br>rework, 5 days | **Validate policies to ValidatingPolicy, batch 3 (7 policies)**<br>Remaining seven validate policies, including the two with cluster lookups, rewritten; RBAC correspondence test rewritten | CMP-063, FND-011 | stated in its ticket |
| **CMP-065**<br>rework, 3 days | **Mutate policies to MutatingPolicy**<br>Three mutate policies rewritten with byte-identical patched outputs | CMP-064, FND-011 | stated in its ticket |
| **CMP-066**<br>delete, 3 days | **Delete what is left: stub, disabled image policy, test-only set; move the access policy home**<br>Zero legacy policy objects; access policy in the policy chart; engine gate check | CMP-065, FND-011 | Makes CMP-G06 required. |
| **CMP-069**<br>fix, 3 days | **Engine to the 1.20 line**<br>Engine on 1.20 after CMP-G06 is green twice; second report group read only if the ticket says the chart defaults to it | CMP-066, FND-151, FND-011 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **CMP-058**: `platform/kyverno/chart/Chart.yaml`, `platform/kyverno/chart/values.yaml`, `.github/workflows/policy-chart-tests.yaml`
- **CMP-061**: `platform/kyverno/chart/values.yaml`, `platform/kyverno/chart/templates/clusterpolicy-*.yaml`, `platform/kyverno-policies/chart/templates/namespace-exceptions.yaml`, `products/catalyst/bootstrap/api/internal/k8scache/kinds.go`, `products/catalyst/bootstrap/api/internal/compliance/adapters/policymode.go`, `scripts/baselines/kyverno-legacy.json`, `docs/ledger/deleted-paths.txt`
- **CMP-062**: `platform/kyverno-policies/chart/templates/_helpers.tpl`, `platform/kyverno-policies/chart/tests/**`, `scripts/baselines/kyverno-legacy.json`
- **CMP-068**: `core/controllers/organization/internal/gitops/manifests.go`, `core/controllers/organization/internal/gitops/testdata/**`, `products/catalyst/bootstrap/api/internal/compliance/adapters/podsecurity.go`, `platform/kyverno-policies/chart/model/checks.yaml`, `tests/acceptance/sovereign/internal/suite/check_pod_security.go`, `docs/ledger/gates.yaml`
- **CMP-063**: `platform/kyverno-policies/chart/tests/**`, `scripts/baselines/kyverno-legacy.json`
- **CMP-064**: `platform/kyverno-policies/chart/templates/rbac-*.yaml`, `platform/kyverno-policies/chart/tests/**`, `scripts/baselines/kyverno-legacy.json`
- **CMP-065**: `platform/kyverno-policies/chart/templates/baseline/21-app-label-mutate.yaml`, `platform/kyverno-policies/chart/templates/mutate/*.yaml`, `platform/kyverno-policies/chart/tests/**`, `scripts/baselines/kyverno-legacy.json`
- **CMP-066**: `platform/kyverno-policies/chart/templates/baseline/16-hubble-flows-seen.yaml`, `platform/kyverno-policies/chart/templates/baseline/18-cosign-verified.yaml`, `platform/kyverno-policies/chart/templates/baseline/25-useraccess-boundary.yaml`, `platform/crossplane-claims/chart/templates/policies/userAccess-boundary.yaml`, `products/catalyst/chart/templates/qa-fixtures/kyverno-policies-qa.yaml`, `scripts/check-kyverno-legacy-policies.sh`, `scripts/baselines/kyverno-legacy.json`, `tests/acceptance/sovereign/internal/suite/check_policy_engine.go`, `docs/ledger/gates.yaml`
- **CMP-069**: `platform/kyverno/chart/Chart.yaml`, `platform/kyverno/chart/values.yaml`, `.github/workflows/policy-chart-tests.yaml`, `products/catalyst/bootstrap/api/internal/k8scache/kinds.go`, `products/catalyst/bootstrap/api/internal/compliance/adapters/policyreport.go`

</details>

### Group CMP-090: Epic: scanner reports

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **CMP-088**<br>fix, 1 day | **Scanner to the current line**<br>Scanner operator on the release named in the ticket (before CMP-091 instead if spike CMP-S3 finds no publication date) | CMP-091, FND-011 | stated in its ticket |
| **CMP-092**<br>new, 3 days | **Configuration audit, secrets in images, risky roles, benchmark**<br>Four adapters and four checks; matched secret text never copied | CMP-091, FND-011 | Makes CMP-G14 required. |
| **CMP-093**<br>new, 5 days | **Security tab**<br>Screens S4, S6, S7; Pod vulnerability tab re-pointed | CMP-052, CMP-091, CMP-092, FND-161 | Makes CMP-G15 required. |

<details><summary>Files each task of this group changes</summary>

- **CMP-088**: `platform/trivy/chart/Chart.yaml`, `platform/trivy/chart/values.yaml`
- **CMP-092**: `products/catalyst/bootstrap/api/internal/compliance/adapters/configaudit.go`, `products/catalyst/bootstrap/api/internal/compliance/adapters/exposedsecret.go`, `products/catalyst/bootstrap/api/internal/compliance/adapters/rbac.go`, `products/catalyst/bootstrap/api/internal/compliance/adapters/benchmark.go`, `platform/kyverno-policies/chart/model/checks.yaml`, `tests/acceptance/sovereign/internal/suite/check_config_audit.go`, `docs/ledger/gates.yaml`
- **CMP-093**: `products/catalyst/bootstrap/ui/src/pages/sovereign/posture/security/**`, `products/catalyst/bootstrap/ui/src/pages/sovereign/cloud-list/SBOMTab.tsx`, `products/catalyst/bootstrap/ui/src/app/router.tsx`, `products/catalyst/bootstrap/ui/e2e/gate/posture/security.spec.ts`, `docs/ledger/gates.yaml`

</details>

### Group CMP-110: Epic: resilience

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **CMP-114**<br>new, 3 days | **Stability, certificate and delivery checks**<br>Checks stable-7d, certificate-valid, gitops-in-sync | CMP-111, FND-011 | Makes CMP-G19 required. |
| **CMP-113**<br>new, 5 days | **Continuity and the Resilience screen**<br>Rule C from Continuum status, screen S8, no write to any topology object | CMP-052, CMP-111, NET-212, FND-011 | Makes CMP-G18 required. |

<details><summary>Files each task of this group changes</summary>

- **CMP-114**: `products/catalyst/bootstrap/api/internal/k8scache/evaluators/stability.go`, `products/catalyst/bootstrap/api/internal/k8scache/evaluators/certificate.go`, `products/catalyst/bootstrap/api/internal/k8scache/evaluators/gitops.go`, `products/catalyst/bootstrap/api/cmd/api/evaluators_wire.go`, `platform/kyverno-policies/chart/model/checks.yaml`, `tests/acceptance/sovereign/internal/suite/check_stability.go`, `docs/ledger/gates.yaml`
- **CMP-113**: `products/catalyst/bootstrap/api/internal/k8scache/evaluators/continuity.go`, `products/catalyst/bootstrap/api/internal/compliance/routes.go`, `platform/kyverno-policies/chart/model/checks.yaml`, `products/catalyst/bootstrap/ui/src/pages/sovereign/posture/ResilienceTab.tsx`, `products/catalyst/bootstrap/ui/src/app/router.tsx`, `tests/acceptance/sovereign/internal/suite/check_continuity.go`, `docs/ledger/gates.yaml`

</details>

### Group CMP-080: Epic: decisions (waivers and remediation requests)

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **CMP-082**<br>new, 5 days | **Decisions screen and the request dialogs**<br>Screen S10 and the waiver request dialog; two-user flow by push | CMP-052, CMP-081, FND-161 | Makes CMP-G10 required. |
| **CMP-085**<br>new, 3 days | **Remediation requests**<br>Decision type remediate with dueAt and the states open, overdue, resolved | CMP-081, CMP-082, FND-011 | Makes CMP-G12 required. |
| **CMP-083**<br>new, 3 days | **Engine exception for a waiver on an enforcing policy**<br>PolicyException rendered into every region's folder for an active waiver, removed by commit at expiry | CMP-061, CMP-066, CMP-081, CMP-S1 | Makes CMP-G11 required. |

<details><summary>Files each task of this group changes</summary>

- **CMP-082**: `products/catalyst/bootstrap/ui/src/pages/sovereign/posture/DecisionsTab.tsx`, `products/catalyst/bootstrap/ui/src/pages/sovereign/posture/DecisionDrawer.tsx`, `products/catalyst/bootstrap/ui/src/pages/sovereign/posture/RequestWaiverDialog.tsx`, `products/catalyst/bootstrap/ui/src/app/router.tsx`, `products/catalyst/bootstrap/ui/e2e/gate/posture/decisions.spec.ts`, `docs/ledger/gates.yaml`
- **CMP-085**: `platform/kyverno-policies/chart/crds/compliancedecision.yaml`, `platform/kyverno-policies/chart/templates/decision-admission.yaml`, `products/catalyst/bootstrap/api/internal/compliance/decisions/**`, `products/catalyst/bootstrap/ui/src/pages/sovereign/posture/RequestFixDialog.tsx`, `tests/acceptance/sovereign/internal/suite/check_remediation.go`, `docs/ledger/gates.yaml`
- **CMP-083**: `products/catalyst/bootstrap/api/internal/compliance/decisions/render.go`, `products/catalyst/bootstrap/api/internal/compliance/decisions/render_test.go`, `products/catalyst/bootstrap/api/internal/compliance/decisions/routes.go`, `products/catalyst/bootstrap/api/internal/compliance/decisions/expiry.go`, `tests/acceptance/sovereign/internal/suite/check_decision_exception.go`, `docs/ledger/gates.yaml`

</details>

### Group CMP-120: Epic: frameworks

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **CMP-121**<br>new, 5 days | **Framework mappings, native benchmarks and per-framework scores**<br>Two native and two draft mapped frameworks, framework score and coverage, route R15 | CMP-092, CMP-111, FND-011 | Makes CMP-G20 required. |
| **CMP-123**<br>new, 3 days | **Frameworks screen**<br>Screen S9 on R15; drafts marked and hidden from Organization users | CMP-052, CMP-121, FND-161 | Makes CMP-G21 required. |

<details><summary>Files each task of this group changes</summary>

- **CMP-121**: `platform/kyverno-policies/chart/crds/compliancemodel.yaml`, `platform/kyverno-policies/chart/model/frameworks/*.yaml`, `platform/kyverno-policies/chart/model/schema/framework.schema.json`, `platform/kyverno-policies/chart/templates/compliance-model.yaml`, `scripts/check-compliance-model.py`, `platform/trivy/chart/values.yaml`, `products/catalyst/bootstrap/api/internal/compliance/frameworks.go`, `products/catalyst/bootstrap/api/internal/compliance/routes.go`, `tests/acceptance/sovereign/internal/suite/check_frameworks.go`, `docs/ledger/gates.yaml`
- **CMP-123**: `products/catalyst/bootstrap/ui/src/pages/sovereign/posture/FrameworksTab.tsx`, `products/catalyst/bootstrap/ui/src/pages/sovereign/posture/FrameworkDetail.tsx`, `products/catalyst/bootstrap/ui/src/app/router.tsx`, `products/catalyst/bootstrap/ui/e2e/gate/posture/frameworks.spec.ts`, `docs/ledger/gates.yaml`

</details>

### Group CMP-040: Epic: model and posture hub

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **CMP-053**<br>delete, 3 days | **Delete the old compliance pages and the old stream frames**<br>Old pages, widgets, helper files, stream hook, old spec and old score frames removed; redirects to /posture | CMP-052, CMP-072, CMP-093, CMP-101 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **CMP-053**: `products/catalyst/bootstrap/ui/src/pages/admin/compliance/**`, `products/catalyst/bootstrap/ui/src/widgets/compliance/**`, `products/catalyst/bootstrap/ui/src/lib/compliance.api.ts`, `products/catalyst/bootstrap/ui/src/lib/useComplianceStream.ts`, `products/catalyst/bootstrap/ui/src/app/router.tsx`, `products/catalyst/bootstrap/ui/e2e/compliance-dashboards.spec.ts`, `.github/workflows/playwright-e2e.yml`, `products/catalyst/bootstrap/api/internal/compliance/stream.go`, `tests/acceptance/sovereign/internal/suite/check_compliance_push.go`, `docs/ledger/deleted-paths.txt`

</details>

### Group CMP-100: Epic: runtime

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **CMP-098**<br>fix, 1 day | **Sensor to the current line**<br>Sensor on the release named in the ticket | CMP-101, FND-011 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **CMP-098**: `platform/falco/chart/Chart.yaml`, `platform/falco/chart/values.yaml`

</details>

## BSS Commerce and billing

### Group BSS-100: Epic: one plan source

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **BSS-101**<br>rework, 5 days | **Package offer, the default offer, and the controller's table built from it**<br>Schema, embedded default offer, parser with rules 1 to 7, quota table built from the default; golden files committed before the change | BSS-070, BSS-S1 | stated in its ticket |
| **BSS-102**<br>new, 5 days | **catalyst-api commits and serves the offer**<br>EnsureOffer (adopt default), ApplyOffer, GET /api/v1/commerce/offer with ETag, check BSS-G03 | BSS-101, CTL-035, CTL-110, CTL-120, CTL-160 | Makes BSS-G03 required. |
| **BSS-106**<br>new, 5 days | **The engine publishes the offer and catalyst-api commits it**<br>offer_versions, PublishIfChanged with completeness rule, one wrapper call site, subject registered, durable consumer catalyst-api-offer, check BSS-G06 | BSS-090, BSS-102, BSS-S1, CTL-320 | Makes BSS-G06 required. |
| **BSS-107**<br>rework, 5 days | **An engine without packages adopts the committed offer; publication outcome; no restated prices**<br>One-time import when package_settings is empty; committed_at tracking by ConfigMap watch with its Role; literal catalogPlans deleted; packageShapeOf reads settings only | BSS-106, FND-011 | stated in its ticket |
| **BSS-103**<br>rework, 5 days | **The organization-controller reads the committed offer**<br>Watch on the one ConfigMap, quota rule from the document, condition PlanKnown, planSlug enum replaced by a pattern, check BSS-G04 | BSS-107, FND-011 | Makes BSS-G04 required. |
| **BSS-104**<br>rework, 5 days | **Order capture prices from the committed offer**<br>Offer client with If-None-Match; catalog pricing deleted; an order without package_sku priced as plan.<slug>; topology price from the dr_topology cell; offer_version on orders (one column, no offer_hash); GET /billing/offer; check BSS-G05. | BSS-030, BSS-107, FND-011 | Makes BSS-G05 required. |
| **BSS-105**<br>rework, 5 days | **The storefront reads one offer**<br>Storefront loads /api/billing/offer only; plan deck and engine base-address resolution deleted; cart always carries package_sku | BSS-104 | stated in its ticket |
| **BSS-109**<br>rework, 3 days | **The Organization console and the back office read the offer**<br>Console reads packages from the offer; back-office plan and add-on editors deleted | BSS-104 | stated in its ticket |
| **BSS-108**<br>delete, 5 days | **Delete plans and add-ons from the catalog service**<br>Catalog plan and add-on routes, seeds, types and gateway routes deleted after two green trains | BSS-104, BSS-105, BSS-109 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **BSS-101**: `core/controllers/pkg/offer/**`, `docs/contracts/commerce-offer.schema.json`, `docs/contracts/README.md`, `scripts/check-commerce-offer.py`, `.github/workflows/bss-ratchets.yaml`, `core/controllers/organization/internal/gitops/manifests.go`, `core/controllers/organization/internal/gitops/manifests_test.go`, `core/controllers/organization/internal/gitops/testdata/plan-quota/**`, `scripts/baselines/bss.json`
- **BSS-102**: `products/catalyst/bootstrap/api/internal/intent/offer.go`, `products/catalyst/bootstrap/api/internal/intent/offer_test.go`, `products/catalyst/bootstrap/api/internal/handler/commerce_offer.go`, `products/catalyst/bootstrap/api/cmd/api/routes_commerce.go`, `products/catalyst/bootstrap/api/cmd/api/commerce_wire.go`, `products/catalyst/bootstrap/api/cmd/api/testdata/routes.golden`, `products/catalyst/bootstrap/api/internal/k8scache/kinds.go`, `tests/acceptance/sovereign/internal/suite/check_bss_offer_served.go`, `docs/ledger/gates.yaml`
- **BSS-106**: `products/chargeback/internal/offerpub/**`, `products/chargeback/internal/api/packages.go`, `products/chargeback/internal/api/server.go`, `products/chargeback/internal/store/events.go`, `products/chargeback/cmd/chargeback/main.go`, `products/catalyst/bootstrap/api/internal/intent/offer_consumer.go`, `products/catalyst/bootstrap/api/cmd/api/commerce_wire.go`, `docs/contracts/C3-event-subjects.yaml`, `tests/acceptance/sovereign/internal/suite/check_bss_offer_publication.go`, `tests/acceptance/sovereign/internal/engine/client.go`, `docs/ledger/gates.yaml`
- **BSS-107**: `products/chargeback/internal/offerpub/import.go`, `products/chargeback/internal/adapter/openova/offerseed.go`, `products/chargeback/internal/adapter/openova/adapter.go`, `products/chargeback/internal/adapter/openova/repository.go`, `products/chargeback/internal/store/planbook.go`, `products/chargeback/internal/api/packages.go`, `products/chargeback/chart/templates/rbac.yaml`, `scripts/baselines/bss.json`
- **BSS-103**: `core/controllers/organization/internal/gitops/manifests.go`, `core/controllers/organization/internal/controller/organization_controller.go`, `core/controllers/organization/internal/controller/offer_source.go`, `products/catalyst/chart/templates/controllers/organization-controller-offer-role.yaml`, `products/catalyst/chart/crds/organization.yaml`, `core/controllers/organization/internal/orgapi/types.go`, `tests/acceptance/sovereign/internal/suite/check_bss_quota_from_offer.go`, `docs/ledger/gates.yaml`
- **BSS-104**: `core/services/billing/packages/packages.go`, `core/services/billing/handlers/handlers.go`, `core/services/billing/handlers/routes.go`, `core/services/billing/store/store.go`, `core/services/billing/README.md`, `core/services/gateway/main.go`, `products/catalyst/chart/templates/org-services/billing.yaml`, `tests/acceptance/sovereign/internal/suite/check_bss_order_price.go`, `docs/ledger/gates.yaml`
- **BSS-105**: `core/marketplace/src/lib/packages.ts`, `core/marketplace/src/lib/packages.test.ts`, `core/marketplace/src/lib/config.ts`, `core/marketplace/src/lib/api.ts`, `core/marketplace/src/lib/cart.ts`, `core/marketplace/src/env.d.ts`, `core/marketplace/src/components/PlanStep.svelte`, `core/marketplace/src/components/PackageTable.svelte`, `core/marketplace/src/components/PackageLadder.svelte`, `core/marketplace/src/components/AddonsStep.svelte`, `core/marketplace/src/components/BCPStep.svelte`, `core/marketplace/src/components/ReviewStep.svelte` and 4 more
- **BSS-109**: `core/console/src/lib/api.ts`, `core/console/src/**`, `core/admin/src/lib/api.ts`, `core/admin/src/**`, `docs/ledger/deleted-paths.txt`
- **BSS-108**: `core/services/catalog/handlers/routes.go`, `core/services/catalog/handlers/handlers.go`, `core/services/catalog/handlers/seed.go`, `core/services/catalog/handlers/plans_filter_test.go`, `core/services/catalog/handlers/seed_plans_upsert_test.go`, `core/services/catalog/handlers/seed_addons_free_test.go`, `core/services/catalog/store/store.go`, `core/services/gateway/main.go`, `docs/contracts/C9-mutating-routes.yaml`, `docs/ledger/deleted-paths.txt`, `scripts/baselines/bss.json`

</details>

### Group BSS-120: Epic: one owner of each money entity

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **BSS-121**<br>fix, 3 days | **Checkout books exact amounts in the wallet**<br>Checkout debits the exact order total in amount_micro_omr; balance read exact; credit_micro on the balance route; no schema change | BSS-070 | stated in its ticket |
| **BSS-124**<br>fix, 5 days | **The debit port: found by order, no overdraft, engine only**<br>DebitWallet with row lock, order_id key, 404 unknown-order, 409 insufficient-funds, TokenReview caller check | BSS-121, FND-011 | stated in its ticket |
| **BSS-122**<br>new, 5 days | **On a Sovereign the engine settles invoices from the wallet**<br>Hook sends order_id from the Organization, projected token; WALLET_PROVIDER commerce with four rules; slot wires the hook; check BSS-G07 with a paid and an unpaid invoice | BSS-106, BSS-107, BSS-124, FND-011 | Makes BSS-G07 required. |
| **BSS-123**<br>delete, 5 days | **Delete the recurring card subscription and the second invoice system**<br>Provider checkout branch, webhook, portal, subscription and invoice code and the provider library removed; checkout answers 402 when credit does not cover; SetupMethod not available | BSS-122 | stated in its ticket |
| **BSS-125**<br>new, 5 days | **A credited wallet settles open invoices at once**<br>Outbox in order capture, catalyst.wallet.credited.v1 on voucher redemption, engine consumer that retries open invoices, check BSS-G14 | BSS-122, CTL-330 | Makes BSS-G14 required. |

<details><summary>Files each task of this group changes</summary>

- **BSS-121**: `core/services/billing/store/store.go`, `core/services/billing/handlers/handlers.go`, `core/services/billing/README.md`
- **BSS-124**: `core/services/billing/store/store.go`, `core/services/billing/handlers/metering.go`, `core/services/billing/handlers/metering_test.go`, `core/services/billing/handlers/tokenreview.go`, `core/services/billing/main.go`, `core/services/billing/README.md`, `products/catalyst/chart/templates/org-services/billing.yaml`, `products/catalyst/chart/templates/org-services/billing-tokenreview-rbac.yaml`, `docs/contracts/C9-mutating-routes.yaml`
- **BSS-122**: `products/chargeback/internal/adapter/openova/billinghook.go`, `products/chargeback/internal/settle/**`, `products/chargeback/internal/api/**`, `products/chargeback/internal/config/config.go`, `products/chargeback/cmd/chargeback/main.go`, `products/chargeback/chart/templates/deployment.yaml`, `products/chargeback/chart/values.yaml`, `clusters/_template/bootstrap-kit/13f-bp-chargeback.yaml`, `tests/acceptance/sovereign/internal/suite/check_bss_paid_statement.go`, `docs/ledger/gates.yaml`
- **BSS-123**: `core/services/billing/store/store.go`, `core/services/billing/handlers/handlers.go`, `core/services/billing/handlers/consumer.go`, `core/services/billing/handlers/routes.go`, `core/services/billing/go.mod`, `core/services/billing/go.sum`, `core/services/gateway/main.go`, `core/console/src/lib/api.ts`, `core/console/src/**`, `products/catalyst/bootstrap/ui/src/lib/bss.api.ts`, `products/chargeback/internal/adapter/openova/billinghook.go`, `docs/contracts/C9-mutating-routes.yaml` and 1 more
- **BSS-125**: `core/services/billing/store/store.go`, `core/services/billing/handlers/outbox_relay.go`, `core/services/billing/main.go`, `products/chargeback/internal/adapter/openova/walletcredit.go`, `products/chargeback/cmd/chargeback/main.go`, `docs/contracts/C3-event-subjects.yaml`, `tests/acceptance/sovereign/internal/suite/check_bss_wallet_credit.go`, `docs/ledger/gates.yaml`

</details>

### Single tasks

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **BSS-090**<br>rework, 5 days | **The engine's bus client and outbox**<br>Package bus, table event_outbox, relay; connected only when the adapter is on and NATS_URL is set | CTL-045, CTL-300, FND-051, NET-163, SEC-246, SEC-300, FND-011 | stated in its ticket |
| **BSS-150**<br>new, 5 days | **Provider modules follow a setting**<br>Six modules with a route guard, computed default, modules on /api/v1/me, navigation filter, check BSS-G10 | BSS-010, BSS-106 | Makes BSS-G10 required. |
| **BSS-170**<br>rework, 5 days | **The storefront's application list is generated**<br>Generator output 6 and its one reader; hand rows deleted | BSS-070, CTL-207, SVC-150, SVC-170 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **BSS-090**: `products/chargeback/internal/bus/**`, `products/chargeback/internal/store/events.go`, `products/chargeback/internal/config/config.go`, `products/chargeback/cmd/chargeback/main.go`, `products/chargeback/go.mod`, `products/chargeback/go.sum`, `products/chargeback/chart/templates/deployment.yaml`, `products/chargeback/chart/values.yaml`, `products/chargeback/chart/templates/cilium-egress-networkpolicy.yaml`, `products/chargeback/chart/tests/render-contract.sh`, `clusters/_template/bootstrap-kit/13f-bp-chargeback.yaml`
- **BSS-150**: `products/chargeback/internal/api/server.go`, `products/chargeback/internal/api/modules.go`, `products/chargeback/internal/api/**`, `products/chargeback/ui/src/layout/Shell.tsx`, `products/chargeback/ui/src/layout/Shell.test.ts`, `products/chargeback/ui/src/pages/**`, `tests/acceptance/sovereign/internal/suite/check_bss_modules.go`, `docs/ledger/gates.yaml`
- **BSS-170**: `scripts/generate-catalog.py`, `core/services/catalog/handlers/storefront.generated.json`, `core/services/catalog/handlers/seed.go`, `core/services/catalog/handlers/seed_test.go`, `core/services/catalog/handlers/retired_products_test.go`

</details>

### Group BSS-110: Epic: decisions as events

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **BSS-111**<br>rework, 5 days | **Decisions are recorded with an identifier and a sequence, and can be published**<br>entitlement_decisions, RecordDecision with publish flag, platform.Events, HTTP client sends decisionId and sequence, ENFORCEMENT_TRANSPORT default http, subject registered | BSS-090, CTL-190 | stated in its ticket |
| **BSS-113**<br>rework, 3 days | **Spend limit and budget are evaluated when the ledger is written**<br>OnUsageWritten hook, debounced evaluation worker, hourly loops become daily safety ticks | BSS-111 | stated in its ticket |
| **BSS-112**<br>delete, 5 days | **Enforcement runs on events; the HTTP doors are deleted**<br>Slot on events; HTTP client and the four internal routes deleted; applied_at tracking; check BSS-G08 | BSS-106, BSS-111, CTL-195, FND-011 | Makes BSS-G08 required. |

<details><summary>Files each task of this group changes</summary>

- **BSS-111**: `products/chargeback/internal/store/events.go`, `products/chargeback/internal/platform/platform.go`, `products/chargeback/internal/platform/events.go`, `products/chargeback/internal/collections/**`, `products/chargeback/internal/spendlimit/**`, `products/chargeback/internal/config/config.go`, `products/chargeback/cmd/chargeback/main.go`, `docs/contracts/C3-event-subjects.yaml`
- **BSS-113**: `products/chargeback/internal/spendlimit/**`, `products/chargeback/internal/budget/**`, `products/chargeback/internal/store/store.go`, `products/chargeback/internal/store/events.go`, `products/chargeback/cmd/chargeback/main.go`
- **BSS-112**: `clusters/_template/bootstrap-kit/13f-bp-chargeback.yaml`, `products/chargeback/chart/values.yaml`, `products/chargeback/chart/templates/deployment.yaml`, `products/chargeback/internal/platform/platform.go`, `products/chargeback/internal/config/config.go`, `products/chargeback/cmd/chargeback/main.go`, `products/chargeback/internal/adapter/openova/orgsync.go`, `products/catalyst/bootstrap/api/internal/handler/org_suspend_internal.go`, `products/catalyst/bootstrap/api/internal/handler/org_suspend_internal_test.go`, `products/catalyst/bootstrap/api/internal/handler/org_overage_stop_internal.go`, `products/catalyst/bootstrap/api/internal/handler/org_overage_stop_internal_test.go`, `products/catalyst/bootstrap/api/cmd/api/routes_*.go` and 5 more

</details>

### Group BSS-130: Epic: model usage

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **BSS-131**<br>new, 5 days | **The engine books model usage events**<br>The engine books model usage events. | AIP-111, AIP-130, AIP-140, BSS-090, BSS-106, BSS-113 | Makes BSS-G09 required. |
| **BSS-132**<br>delete, 3 days | **Delete the money path of model usage in order capture**<br>Legacy consumer, RecordUsage and the registry entry whose subject is exactly catalyst.usage.recorded (no version) deleted; the entry catalyst.usage.recorded.v1 stays. | AIP-111, BSS-131 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **BSS-131**: `products/chargeback/internal/adapter/openova/modelusage.go`, `products/chargeback/internal/store/events.go`, `products/chargeback/internal/store/models.go`, `products/chargeback/internal/api/server.go`, `products/chargeback/internal/api/usage_events.go`, `products/chargeback/cmd/chargeback/main.go`, `docs/contracts/C3-event-subjects.yaml`, `tests/acceptance/sovereign/internal/suite/check_bss_model_usage.go`, `docs/ledger/gates.yaml`, `tests/acceptance/sovereign/internal/engine/client.go`
- **BSS-132**: `core/services/billing/handlers/metering_consumer.go`, `core/services/billing/handlers/metering_consumer_test.go`, `core/services/billing/handlers/handlers.go`, `core/services/billing/main.go`, `core/services/billing/store/store.go`, `core/services/shared/events/nats.go`, `docs/contracts/C3-event-subjects.yaml`, `docs/ledger/deleted-paths.txt`

</details>

### Group BSS-140: Epic: secrets of this area

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **BSS-142**<br>delete, 3 days | **Payment-provider keys leave the database**<br>The two key columns dropped; settings route rejects the fields; form inputs removed | BSS-123 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **BSS-142**: `core/services/billing/store/store.go`, `core/services/billing/handlers/handlers.go`, `core/admin/src/**`

</details>

## AIP Agent plane

### Group AIP-100: EPIC: Axon as the simulation back end

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **AIP-101**<br>delete, 3 days | **Shrink Axon and give it a test rail**<br>vLLM provider, conversation store and Valkey removed; kit pinned to 0.3.220; unit test rail on pull requests | AIP-020, FND-011 | stated in its ticket |
| **AIP-102**<br>fix, 5 days | **Tool calls passed through; no shared sessions**<br>Turn chains with parked in-process tool handlers, transcript fallback, usage from raw stream events | AIP-101 | stated in its ticket |
| **AIP-103**<br>fix, 3 days | **Real streaming and real usage**<br>One-to-one stream mapping with tool-call argument deltas and a real usage chunk | AIP-102 | stated in its ticket |
| **AIP-104**<br>fix, 3 days | **Limits, queue, time-outs, admin listener**<br>Bounded active and parked runs, queue with 429, step time-out, admin port 3001 with /stats | AIP-103 | stated in its ticket |
| **AIP-105**<br>fix, 3 days | **One owner of the credential**<br>Single refresher with check-and-set write-back to OpenBao; children get the access token only | AIP-104 | stated in its ticket |
| **AIP-106**<br>new, 5 days | **bp-axon: chart, Blueprint file and kit slot 82**<br>Axon installed in the primary region with CSI-mounted credential, fenced ports 3000 and 3001, zero replicas elsewhere | AIP-105, FND-032, SEC-190, SEC-211, SVC-160, FND-011 | Makes AIP-G06 required. |

<details><summary>Files each task of this group changes</summary>

- **AIP-101**: `products/axon/src/**`, `products/axon/package.json`, `products/axon/package-lock.json`, `products/axon/chart/templates/valkey-*.yaml`, `products/axon/chart/templates/ingress.yaml`, `products/axon/chart/templates/token-refresh-cronjob.yaml`, `products/axon/chart/values.yaml`, `products/axon/scripts/e2e-test.sh`, `products/axon/scripts/pod-up.sh`, `products/axon/.env.example`, `products/axon/test/**`, `.github/workflows/axon-build.yaml` and 2 more
- **AIP-102**: `products/axon/src/upstream.ts`, `products/axon/src/turns.ts`, `products/axon/src/transcript.ts`, `products/axon/src/routes/**`, `products/axon/src/config.ts`, `products/axon/src/index.ts`, `products/axon/src/types/openai.ts`, `products/axon/src/providers/claude.ts`, `products/axon/src/providers/session-pool.ts`, `products/axon/src/middleware/auth.ts`, `products/axon/test/**`, `scripts/agent-plane-baseline.yaml`
- **AIP-103**: `products/axon/src/routes/chat-completions.ts`, `products/axon/src/turns.ts`, `products/axon/test/**`
- **AIP-104**: `products/axon/src/turns.ts`, `products/axon/src/config.ts`, `products/axon/src/index.ts`, `products/axon/test/**`
- **AIP-105**: `products/axon/src/credential.ts`, `products/axon/src/providers/token-refresh.ts`, `products/axon/src/upstream.ts`, `products/axon/src/index.ts`, `products/axon/src/config.ts`, `products/axon/test/**`, `scripts/agent-plane-baseline.yaml`
- **AIP-106**: `products/axon/chart/**`, `products/axon/blueprint.yaml`, `products/axon/Containerfile`, `products/axon/README.md`, `clusters/_template/bootstrap-kit/82-bp-axon.yaml`, `clusters/_template/bootstrap-kit/kustomization.yaml`, `clusters/_template/bootstrap-kit/placement.yaml`, `scripts/expected-bootstrap-deps.yaml`, `.github/workflows/axon-build.yaml`, `tests/acceptance/sovereign/internal/suite/check_aip_g06.go`, `docs/ledger/gates.yaml`

</details>

### Group AIP-110: EPIC: model-meter

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **AIP-111**<br>rework, 5 days | **Move the sidecar, take identity from the gateway, publish quantity**<br>Meter that fails closed without identity and publishes catalyst.usage.recorded.v1 with measures and no price | AIP-040, CTL-045, CTL-330 | stated in its ticket |
| **AIP-112**<br>fix, 3 days | **Streamed responses are metered**<br>Streaming parser that forwards unbuffered and reads the final usage chunk | AIP-111 | stated in its ticket |
| **AIP-113**<br>rework, 3 days | **Durable spool, admin routes, scripted backend**<br>serve, stub and version subcommands; admin listener with ring of 1,000 entries, metrics and log line | AIP-112 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **AIP-111**: `core/services/metering-sidecar/**`, `core/services/model-meter/**`, `core/services/shared/events/nats.go`, `.github/workflows/services-build.yaml`, `.github/workflows/test-ungated-trees.yaml`, `docs/contracts/C3-event-subjects.yaml`, `docs/ledger/deleted-paths.txt`, `scripts/agent-plane-baseline.yaml`
- **AIP-112**: `core/services/model-meter/meter/**`
- **AIP-113**: `core/services/model-meter/**`

</details>

### Single tasks

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **AIP-160**<br>new, 3 days | **Simulation credential routes**<br>PUT credential (to OpenBao only) and GET status routes for sovereign-admin | AIP-106, AIP-120 | Makes AIP-G13 required. |
| **AIP-130**<br>test, 3 days | **Model contract conformance client and checks**<br>Implementations of AIP-G02 to AIP-G05 set to expected-fail, one file per check; check_aip_model.go keeps the shared helpers. It exports ModelUsageProbe(ctx) (organization, alias, requestID string, err error), memoised, on conformance-echo, for BSS-G09 and AIP-G09. AIP-G02 also asserts that GET /v1/models lists conformance-echo with owned_by stub. | AIP-113, AIP-120, FND-032 | stated in its ticket |
| **AIP-156**<br>delete, 3 days | **Delete the long-lived bearer seed**<br>No one-year Organization administrator bearer is minted or read anywhere | AIP-155 | stated in its ticket |
| **AIP-140**<br>new, 5 days | **bp-model-gateway: chart and kit slot 81**<br>As before, plus: every object of GET /v1/models carries owned_by equal to the alias's back-end key. No package enables the Anthropic Messages route. | AIP-010, AIP-106, AIP-113, AIP-120, AIP-130, CTL-300, SVC-160, FND-011 | Makes AIP-G02, AIP-G03, AIP-G04 required. |
| **AIP-135**<br>test, 1 day | **Nightly record for the checks that need a real model**<br>Nightly record for the checks that need a real model. | AIP-130, AIP-140, AIP-160, FND-035 | stated in its ticket |
| **AIP-170**<br>fix, 1 day | **Model gateway and tool server resolve inside Organization vclusters**<br>Two replicated host Services added to the vcluster values | AIP-140 | stated in its ticket |
| **AIP-275**<br>fix, 3 days | **The meter refuses a stopped Organization**<br>Informer on Organization objects; 403 organization_stopped for suspended or overage-stopped Organizations | AIP-140, FND-011 | stated in its ticket |
| **AIP-285**<br>test, 3 days | **A usage record lands in the billing engine**<br>End-to-end check AIP-G09 using ModelUsageProbe of AIP-130 and the usage-event read method that BSS-131 adds to the suite's engine client. | AIP-140, BSS-131 | Makes AIP-G09 required. |
| **AIP-190**<br>delete, 5 days | **Delete credential seeding and refresh outside Axon**<br>Axon is the only reader and refresher of the simulation credential; no copy from a parent Sovereign | AIP-106, AIP-155, AIP-156, AIP-160, AIP-182, FND-011 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **AIP-160**: `products/catalyst/bootstrap/api/internal/modelsim/**`, `products/catalyst/bootstrap/api/cmd/api/main.go`, `products/catalyst/bootstrap/api/cmd/api/routes*.go`, `products/catalyst/bootstrap/api/cmd/api/testdata/routes.golden`, `docs/contracts/C9-mutating-routes.yaml`, `tests/acceptance/sovereign/internal/suite/check_aip_g13.go`, `docs/ledger/gates.yaml`, `scripts/agent-plane-baseline.yaml`
- **AIP-130**: `tests/acceptance/sovereign/internal/modelcontract/**`, `tests/acceptance/sovereign/internal/suite/check_aip_model.go`, `docs/ledger/gates.yaml`, `tests/acceptance/sovereign/internal/suite/check_aip_g02.go`, `tests/acceptance/sovereign/internal/suite/check_aip_g03.go`, `tests/acceptance/sovereign/internal/suite/check_aip_g04.go`, `tests/acceptance/sovereign/internal/suite/check_aip_g05.go`
- **AIP-156**: `products/catalyst/bootstrap/api/internal/handler/sovereign_mcp_bearer_seed.go`, `products/catalyst/bootstrap/api/internal/handler/sovereign_mcp_bearer_seed_test.go`, `products/catalyst/bootstrap/api/internal/handler/organization_provisioning.go`, `products/catalyst/bootstrap/api/internal/handler/sovereign_seed_reconciler.go`, `products/catalyst/bootstrap/api/internal/handler/organization_gitops.go`, `products/catalyst/bootstrap/api/internal/handler/application_parameters.go`, `products/openova-mcp/cmd/openova-mcp/main.go`, `scripts/agent-plane-baseline.yaml`, `scripts/baselines/api-structure.json`, `docs/ledger/deleted-paths.txt`
- **AIP-140**: `platform/model-gateway/**`, `clusters/_template/bootstrap-kit/81-bp-model-gateway.yaml`, `clusters/_template/bootstrap-kit/kustomization.yaml`, `clusters/_template/bootstrap-kit/placement.yaml`, `scripts/expected-bootstrap-deps.yaml`, `.github/workflows/build-bp-model-gateway.yaml`, `docs/ledger/gates.yaml`
- **AIP-135**: `.github/workflows/foundation-gate.yaml`
- **AIP-170**: `core/controllers/organization/internal/gitops/manifests.go`, `core/controllers/organization/internal/gitops/manifests_test.go`
- **AIP-275**: `core/services/model-meter/**`, `platform/model-gateway/chart/templates/**`, `tests/acceptance/sovereign/internal/suite/check_aip_g04.go`
- **AIP-285**: `tests/acceptance/sovereign/internal/suite/check_aip_g09.go`, `docs/ledger/gates.yaml`
- **AIP-190**: `products/catalyst/bootstrap/api/internal/handler/sovereign_anthropic_*.go`, `products/catalyst/bootstrap/api/internal/handler/anthropic_credential_class.go`, `products/catalyst/bootstrap/api/internal/handler/anthropic_credential_class_6250_test.go`, `products/catalyst/bootstrap/api/internal/handler/anthropic_seed_unhealed_4277_test.go`, `products/catalyst/bootstrap/api/internal/handler/sovereign_seed_reconciler.go`, `products/catalyst/bootstrap/api/internal/handler/sovereign_seed_reconciler_test.go`, `products/catalyst/bootstrap/api/internal/handler/kubeconfig.go`, `products/catalyst/bootstrap/api/internal/handler/organization_provisioning.go`, `products/catalyst/bootstrap/api/internal/handler/handler.go`, `products/catalyst/bootstrap/api/cmd/api/main.go`, `products/catalyst/chart/templates/api-deployment.yaml`, `products/catalyst/chart/templates/api-deployment-kustomize.yaml` and 7 more

</details>

### Group AIP-180: EPIC: Retire the old gateway and its satellites

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **AIP-181**<br>delete, 3 days | **Remove the per-Organization gateway and the proxy shell from the funnel**<br>As before, plus lifecycle deprecated on bp-newapi and bp-openclaw with the regenerated catalogue (lock blueprints). | AIP-140, CTL-207, SVC-170 | stated in its ticket |
| **AIP-182**<br>delete, 3 days | **Delete the gateway client, per-user key issuing and the administrator-token seed**<br>No code path issues or stores per-user gateway keys | AIP-181, FND-011 | stated in its ticket |
| **AIP-183**<br>delete, 3 days | **Delete kit slot 80 and what points at it**<br>Old gateway no longer installed on a Sovereign; its database and keyspace entries removed | AIP-182 | stated in its ticket |
| **AIP-184**<br>delete, 3 days | **Delete the charts, the bridge sidecar and their builds**<br>Charts and build workflows of the old gateway and proxy shell removed | AIP-183, FND-011 | stated in its ticket |
| **AIP-185**<br>delete, 1 day | **Remove the old gateway from the umbrella chart's policies**<br>No policy rule names namespace newapi | AIP-183, FND-011 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **AIP-181**: `products/catalyst/bootstrap/api/internal/handler/organization_funnel_apps.go`, `core/services/provisioning/gitops/gitops.go`, `core/services/provisioning/gitops/helmrelease_apps.go`, `core/controllers/organization/internal/gitops/platform_scope.go`, `core/controllers/organization/internal/gitops/manifests.go`, `core/controllers/pkg/render/manifests.go`, `platform/newapi/blueprint.yaml`, `platform/openclaw/blueprint.yaml`, `docs/ledger/deleted-paths.txt`
- **AIP-182**: `products/catalyst/bootstrap/api/internal/newapi/**`, `products/catalyst/bootstrap/api/internal/handler/sovereign_newapi_admin_token_seed.go`, `products/catalyst/bootstrap/api/internal/handler/sovereign_seed_reconciler.go`, `products/catalyst/bootstrap/api/internal/handler/org_users.go`, `products/catalyst/chart/templates/api-deployment.yaml`, `products/catalyst/chart/templates/api-deployment-kustomize.yaml`, `scripts/baselines/api-structure.json`, `docs/ledger/deleted-paths.txt`
- **AIP-183**: `clusters/_template/bootstrap-kit/80-newapi.yaml`, `clusters/_template/bootstrap-kit/kustomization.yaml`, `clusters/_template/bootstrap-kit/placement.yaml`, `clusters/_template/bootstrap-kit/16d-bp-postgres-shared-c.yaml`, `clusters/_template/bootstrap-kit/17-valkey.yaml`, `clusters/_template/bootstrap-kit/39-bp-vllm.yaml`, `scripts/expected-bootstrap-deps.yaml`, `docs/ledger/deleted-paths.txt`
- **AIP-184**: `platform/newapi/**`, `platform/openclaw/**`, `.github/workflows/build-bp-newapi.yaml`, `.github/workflows/build-bp-newapi-sandbox-bridge.yaml`, `.github/workflows/openclaw-controller.yaml`, `.github/workflows/openclaw-runtime.yaml`, `docs/ledger/deleted-paths.txt`, `scripts/agent-plane-baseline.yaml`
- **AIP-185**: `products/catalyst/chart/templates/network-policies/baseline-catalyst-system.yaml`, `products/catalyst/chart/templates/network-policies/keycloak-backchannel-ingress.yaml`, `products/catalyst/chart/templates/qa-fixtures/cilium-network-policies.yaml`

</details>

## INF Infrastructure plane

### Group INF-300: Epic: discovery and ownership (INF-310, 320, 330, 340, 350)

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **INF-350**<br>lab test, 3 days | **Spike: change events from the reference cloud**<br>Whether the audit-trail service on the operator-hosted stack can push server changes into the Sovereign, with measured delays | FND-070 | stated in its ticket |
| **INF-310**<br>new, 3 days | **Provider capability InventoryLister**<br>Capability types, reference and local adapters, conformance test with the read-only recording transport | CLD-088, CLD-102 | stated in its ticket |
| **INF-320**<br>new, 5 days | **Sources declared and cloud-account; remaining rules; check INF-G04**<br>Two more sources, rules 2, 7, 8, 10, 13, 15, 16 with tables, cloud credential as a CSI mount only, INF-G04 as expected-fail | CLD-171, CTL-020, INF-160, INF-230, INF-310, FND-011 | stated in its ticket |
| **INF-330**<br>new, 3 days | **Account source on by default, coverage rule; flip INF-G04**<br>Provider overlays switch the account source on; coverage rule 14 in the function test | INF-320, FND-011 | Makes INF-G04 required. |
| **INF-340**<br>new, 3 days | **Console: ownership and inventory health**<br>Sources panel, unmanaged/stale/quarantined filters, no override buttons | INF-170, INF-175, INF-320 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **INF-350**: `docs/ledger/plan/spikes/INF-350.md`
- **INF-310**: `products/catalyst/bootstrap/api/internal/providers/inventory.go`, `products/catalyst/bootstrap/api/internal/providers/huawei/inventory.go`, `products/catalyst/bootstrap/api/internal/providers/huawei/inventory_test.go`, `products/catalyst/bootstrap/api/internal/providers/local/inventory.go`, `products/catalyst/bootstrap/api/internal/providers/conformance/conformance_test.go`
- **INF-320**: `products/catalyst/bootstrap/api/internal/machines/source/declared.go`, `products/catalyst/bootstrap/api/internal/machines/source/cloudaccount.go`, `products/catalyst/bootstrap/api/internal/machines/inventory/rules.go`, `products/catalyst/bootstrap/api/internal/machines/inventory/rules_test.go`, `products/catalyst/bootstrap/api/internal/machines/inventory/reconciler.go`, `platform/machine-operations/chart/crds/machines.yaml`, `platform/machine-operations/chart/crds/machinesources.yaml`, `platform/machine-operations/chart/templates/rbac-inventory.yaml`, `platform/machine-operations/chart/templates/sources.yaml`, `platform/machine-operations/chart/templates/deployment-inventory.yaml`, `platform/machine-operations/chart/templates/secretproviderclass-cloud.yaml`, `platform/machine-operations/chart/values.yaml` and 2 more
- **INF-330**: `platform/machine-operations/chart/values.yaml`, `platform/machine-operations/chart/templates/sources.yaml`, `platform/machine-operations/chart/templates/tests/function-test.yaml`, `clusters/_template/providers/huawei/values/provider-values-bp-machine-operations.yaml`, `clusters/_template/providers/local/values/provider-values-bp-machine-operations.yaml`, `clusters/_template/bootstrap-kit/36-bp-machine-operations.yaml`, `docs/ledger/gates.yaml`
- **INF-340**: `products/catalyst/bootstrap/ui/src/pages/sovereign/cloud-list/MachinesPage.tsx`, `products/catalyst/bootstrap/ui/src/pages/sovereign/cloud-list/MachinesPage.test.tsx`, `products/catalyst/bootstrap/ui/src/pages/sovereign/cloud-list/MachineSourcesPanel.tsx`, `products/catalyst/bootstrap/ui/e2e/gate/machines/**`

</details>

### Group INF-100: Epic: machine operations on the platform's own nodes (INF-110, 112, 120, 130, 132, 135, 140, 160, 145, 170)

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **INF-140**<br>new, 5 days | **Vulnerability matching and daily re-matching**<br>Match task (trivy sbom), vulnerability report with database-age rule, daily rematch run by timer, check INF-G11 as expected-fail | INF-132, FND-011 | stated in its ticket |
| **INF-170**<br>new, 3 days | **Console: Machines**<br>Machines page from the live stream with facets, scores and the hold banner | CTL-360, INF-130 | stated in its ticket |
| **INF-160**<br>new, 3 days | **Install bp-machine-operations; flip INF-G02**<br>Kit slot 36 with crds CreateReplace, the evidence bucket as one entry of the platform bucket instance with its binding mounted by CSI, two OpenBao auth roles, default-deny allow file for machine-ops. | INF-132, INF-135, NET-150, SEC-190, SEC-211, SVC-275, FND-011, NET-030 | Makes INF-G02 required. |
| **INF-145**<br>test, 1 day | **Flip INF-G11 once the database is reachable after hand-over**<br>The slot's vulnerabilityDb.repository value and INF-G11 set to required. Not ready until SOV or CMP names the package for request N7 (c) | INF-140, INF-160 | Makes INF-G11 required. |
| **INF-175**<br>test, 3 days | **Browser specifications for the machine pages; check INF-G12**<br>The folder of browser specifications for this area's console pages with one specification for the Machines page, and check INF-G12 set to required. | FND-161, INF-160, INF-170 | Makes INF-G12 required. |

<details><summary>Files each task of this group changes</summary>

- **INF-140**: `products/catalyst/bootstrap/api/internal/machines/operations/match.go`, `products/catalyst/bootstrap/api/internal/machines/operations/report_vulnerability.go`, `products/catalyst/bootstrap/api/internal/machines/operations/rematch.go`, `products/catalyst/bootstrap/api/internal/machines/runner/match.go`, `platform/machine-operations/chart/crds/scanpolicies.yaml`, `platform/machine-operations/chart/crds/machineruns.yaml`, `platform/machine-operations/chart/values.yaml`, `platform/machine-operations/chart/templates/defaults.yaml`, `platform/machine-operations/chart/templates/deployment-operations.yaml`, `tests/acceptance/sovereign/internal/suite/check_inf_g11.go`, `docs/ledger/gates.yaml`
- **INF-170**: `products/catalyst/bootstrap/ui/src/pages/sovereign/cloud-list/kinds.ts`, `products/catalyst/bootstrap/ui/src/pages/sovereign/cloud-list/MachinesPage.tsx`, `products/catalyst/bootstrap/ui/src/pages/sovereign/cloud-list/MachinesPage.test.tsx`, `products/catalyst/bootstrap/api/internal/k8scache/kinds.go`
- **INF-160**: `clusters/_template/bootstrap-kit/36-bp-machine-operations.yaml`, `clusters/_template/bootstrap-kit/kustomization.yaml`, `scripts/expected-bootstrap-deps.yaml`, `platform/machine-operations/chart/templates/secretproviderclass.yaml`, `platform/machine-operations/chart/templates/tests/function-test.yaml`, `platform/openbao/chart/templates/_config.tpl`, `platform/openbao/chart/values.yaml`, `platform/network-policies/chart/templates/allow/machine-ops.yaml`, `platform/network-policies/chart/values.yaml`, `tests/acceptance/sovereign/policy-dials.yaml`, `docs/ledger/gates.yaml`
- **INF-145**: `clusters/_template/bootstrap-kit/36-bp-machine-operations.yaml`, `docs/ledger/gates.yaml`
- **INF-175**: `products/catalyst/bootstrap/ui/e2e/gate/machines/**`, `docs/ledger/gates.yaml`

</details>

### Group INF-200: Epic: virtual machines (INF-210, 220, 230, 240, 250, 260)

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **INF-220**<br>new, 5 days | **Unit module day2/virtualmachine for the reference and local packs**<br>Both modules to section 4.2 (behaviours 1-5 and 7), plan tests, check INF-G03 as expected-fail walking a plain unit Workspace. | CLD-173, CLD-174 | stated in its ticket |
| **INF-230**<br>new, 3 days | **VirtualMachine definition, two Compositions, catalogue format**<br>The definition with four validation rules, two Compositions, the catalogue schema, lock binding entry; the INF-G03 walk on a VirtualMachine manifest. | CLD-175, INF-220, FND-011 | stated in its ticket |
| **INF-250**<br>new, 3 days | **Image catalogue routes**<br>Read route and Git-first PUT/DELETE that write the catalogue into every region folder in one commit. Not ready until CTL names the package for scope FleetMachineOps and CLD the one for role virtual-machine | CTL-035, CTL-160, FND-120, INF-230 | stated in its ticket |
| **INF-240**<br>new, 3 days | **Blueprint bp-virtual-machine; flip INF-G03**<br>Chart and Blueprint as 4.4 without ring and exposure; INF-G03 mode application | CTL-162, INF-210, INF-230, SVC-170, SVC-290, FND-011 | Makes INF-G03 required. |
| **INF-260**<br>new, 5 days | **Console: Virtual machines**<br>List, create through the existing install dialog, detail tabs, stop/start/resize/remove through Application routes | CLD-080, INF-170, INF-175, INF-240, INF-250, SVC-180 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **INF-220**: `infra/providers/huawei/day2/virtualmachine/**`, `infra/providers/local/day2/virtualmachine/**`, `tests/acceptance/sovereign/internal/suite/check_inf_g03.go`, `docs/ledger/gates.yaml`
- **INF-230**: `platform/crossplane-claims/chart/templates/xrds/virtualmachine.yaml`, `platform/crossplane-claims/chart/values.yaml`, `platform/crossplane-claims/chart/tests/virtualmachine-rules.yaml`, `clusters/_template/providers/huawei/compositions/virtualmachine-native.yaml`, `clusters/_template/providers/local/compositions/virtualmachine-native.yaml`, `clusters/_template/providers/huawei/kustomization.yaml`, `clusters/_template/providers/local/kustomization.yaml`, `infra/providers/huawei/profile.yaml`, `infra/providers/local/profile.yaml`, `infra/providers/_contract/machine-images.schema.json`, `products/catalyst/chart/templates/intent-lock/bindings.yaml`, `tests/acceptance/sovereign/internal/suite/check_inf_g03.go`
- **INF-250**: `products/catalyst/bootstrap/api/internal/machines/routes/register.go`, `products/catalyst/bootstrap/api/internal/machines/routes/images.go`, `products/catalyst/bootstrap/api/internal/machines/routes/images_test.go`, `docs/contracts/C9-mutating-routes.yaml`
- **INF-240**: `platform/virtual-machine/chart/**`, `platform/virtual-machine/blueprint.yaml`, `platform/virtual-machine/README.md`, `tests/acceptance/sovereign/internal/suite/check_inf_g03.go`, `docs/ledger/gates.yaml`
- **INF-260**: `products/catalyst/bootstrap/ui/src/pages/sovereign/cloud-list/kinds.ts`, `products/catalyst/bootstrap/ui/src/pages/sovereign/cloud-list/VirtualMachinesPage.tsx`, `products/catalyst/bootstrap/ui/src/pages/sovereign/cloud-list/VirtualMachinesPage.test.tsx`, `products/catalyst/bootstrap/ui/src/pages/sovereign/cloud-list/virtual-machine/**`, `products/catalyst/bootstrap/api/internal/k8scache/kinds.go`, `infra/providers/_contract/cloudprofile.schema.json`, `products/catalyst/bootstrap/ui/e2e/gate/machines/**`

</details>

### Group INF-400: Epic: operations on machines over SSH (INF-410, 420, 430, 440, 450, 455, 460, 465, 470, 480, 490, 540)

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **INF-410**<br>new, 3 days | **OpenBao SSH role and sign policy for runners**<br>SSH role machine-ops and the sign line of policy machine-runner, merged in one pull request with SEC-310 | INF-160, SEC-190, SEC-310, FND-011 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **INF-410**: `platform/openbao/chart/values.yaml`, `platform/openbao/chart/templates/_config.tpl`

</details>

## ACC Access plane

### Group ACC-100: Epic: kinds, controller and the fenced pod session (order: ACC-101, ACC-102, ACC-103, ACC-105 and ACC-106 in parallel, ACC-104, ACC-108, ACC-107)

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **ACC-101**<br>delete, 5 days | **Delete the fabricated session surface in the API and the console**<br>synthesizeSession, GuacamoleClient, the in-memory session store, six route registrations and the three session pages are gone; a request with no claims is refused on the exec route; the console shell opens the WebSocket directly. | CTL-035, CTL-230 | stated in its ticket |
| **ACC-102**<br>delete, 5 days | **The web application, its database, the seed Job and the CronJob leave the chart**<br>Chart renders only the bootstrap-owned Application; values keep catalystBlueprint, global, crossRegion, bootstrapOwned and gain access.enabled; gate instance guacamole disabled until ACC-107; guard baselines GD1 to GD3 at 0. | ACC-020, ACC-060, ACC-101, FND-011 | stated in its ticket |
| **ACC-103**<br>new, 5 days | **The target and grant kinds, their controller and the grant life cycle**<br>AccessTarget (kind pod) and AccessGrant definitions with CEL rules; writer lock in Deny; controller with phase rules and RequeueAfter at window edges; grant_activated and grant_ended events; controller only in the primary region with mesh Service stub in the other; class platform-component and visibility unlisted. | ACC-102, CTL-045, CTL-300, CTL-320, SVC-170, FND-011 | Makes ACC-G13 required. |
| **ACC-105**<br>new, 3 days | **OpenBao roles for the access plane**<br>Policies access-session, access-session-platform, access-binder; three auth roles; PKI roles access-exec and access-exec-self; transit key access-recordings; no ssh lines. | ACC-030, ACC-103, SEC-190, SEC-211, SEC-250, FND-011 | Makes ACC-G14 required. |
| **ACC-106**<br>new, 5 days | **Session agent and session images**<br>Agent binary (config, handshake, relay, shipper; recording off when chunkBytes is 0) on a distroless image; guacd 1.6.0 mirrored and pinned by digest; image references exposed as controller environment variables. | ACC-020, ACC-103, FND-011 | Makes ACC-G15 required. |
| **ACC-104**<br>new, 5 days | **Session kind and reconciler, kind pod, and the exec certificate scoped to one pod**<br>Exec proxy accepts a certificate whose URI name scopes it to one namespace, pod and container; AccessSession definition; Kind interface and kind pod; session reconciler building policy, mount class and pod in that order and cleaning up; internal ready and closed routes on port 8081. | ACC-030, ACC-103, ACC-105, ACC-106, NET-163, SEC-211, SEC-250, FND-011 | Makes ACC-G03 required. |
| **ACC-108**<br>new, 3 days | **Admission and the session fence**<br>Admission policy (no parameter object; ephemeral containers may not mount volumes) that lets only the controller create session pods of the fixed shape; cluster-wide base network policy for session pods. | ACC-104, NET-150, NET-170, FND-011 | Makes ACC-G05 required. |
| **ACC-107**<br>new, 5 days | **Broker, tunnel and client page: a pod session from the browser**<br>Broker public routes (session open with write-ahead event, tunnel relay, end, break-glass, idle, reconnect), session_opened, session_closed and break_glass_used events, the client page with the Apache-released browser library, gate instance pointed at the controller. | ACC-020, ACC-104, ACC-108, CTL-320, FND-161, NET-163, FND-011 | Makes ACC-G16 required. |

<details><summary>Files each task of this group changes</summary>

- **ACC-101**: `products/catalyst/bootstrap/api/internal/handler/shells_issue.go`, `products/catalyst/bootstrap/api/internal/handler/shells_issue_test.go`, `products/catalyst/bootstrap/api/internal/handler/k8s_exec.go`, `products/catalyst/bootstrap/api/internal/handler/k8s_exec_test.go`, `products/catalyst/bootstrap/api/internal/handler/handler.go`, `products/catalyst/bootstrap/ui/src/pages/sovereign/sessions/`, `products/catalyst/bootstrap/ui/src/app/router.tsx`, `products/catalyst/bootstrap/ui/src/widgets/cloud-list/ExecPanel.tsx`, `products/catalyst/bootstrap/ui/src/pages/sovereign/cloud-list/resource.api.ts`, `docs/contracts/C9-mutating-routes.yaml`, `docs/ledger/deleted-paths.txt`
- **ACC-102**: `platform/guacamole/chart/files/postgresql/`, `platform/guacamole/chart/values.yaml`, `clusters/_template/bootstrap-kit/52-bp-guacamole.yaml`, `clusters/_template/bootstrap-kit/13c-bp-oidc-gate.yaml`, `tests/acceptance/sovereign/internal/suite/check_acc_g01.go`, `scripts/secret-guard-baselines/secret-object-register.yaml`, `scripts/baselines/access.json`, `docs/ledger/deleted-paths.txt`, `.github/workflows/build-bp-guacamole.yaml`
- **ACC-103**: `platform/guacamole/chart/crds/accesstarget.yaml`, `platform/guacamole/chart/crds/accessgrant.yaml`, `platform/guacamole/chart/templates/controller-deployment.yaml`, `platform/guacamole/chart/templates/controller-rbac.yaml`, `platform/guacamole/chart/templates/controller-service.yaml`, `platform/guacamole/chart/templates/admission-kinds.yaml`, `platform/guacamole/chart/templates/crossregion-netpol.yaml`, `platform/guacamole/chart/templates/_helpers.tpl`, `platform/guacamole/chart/tests/render.sh`, `platform/guacamole/chart/tests/crossregion-render.sh`, `platform/guacamole/chart/values.yaml`, `platform/guacamole/blueprint.yaml` and 12 more
- **ACC-105**: `platform/openbao/chart/templates/_config.tpl`, `platform/openbao/chart/values.yaml`, `platform/openbao/chart/tests/access-render.sh`, `tests/acceptance/sovereign/internal/suite/check_acc_g14.go`, `tests/acceptance/sovereign/fixtures/access/g14/`, `docs/ledger/gates.yaml`
- **ACC-106**: `core/cmd/access-session-agent/`, `.github/workflows/build-access-session-agent.yaml`, `.github/workflows/build-bp-guacamole.yaml`, `platform/guacamole/chart/values.yaml`, `platform/guacamole/chart/templates/controller-deployment.yaml`, `platform/guacamole/chart/tests/render.sh`, `tests/acceptance/sovereign/internal/suite/check_acc_g15.go`, `tests/acceptance/sovereign/fixtures/access/g15/pod.yaml`, `docs/ledger/gates.yaml`
- **ACC-104**: `core/cmd/k8s-ws-proxy/internal/auth/authorize.go`, `core/cmd/k8s-ws-proxy/internal/auth/accesscert.go`, `core/cmd/k8s-ws-proxy/internal/auth/accesscert_test.go`, `core/cmd/k8s-ws-proxy/internal/runtime/config.go`, `core/cmd/k8s-ws-proxy/internal/runtime/config_test.go`, `core/cmd/k8s-ws-proxy/main.go`, `core/cmd/k8s-ws-proxy/internal/proxy/exec.go`, `core/cmd/k8s-ws-proxy/internal/proxy/exec_test.go`, `platform/k8s-ws-proxy/chart/templates/certs.yaml`, `platform/k8s-ws-proxy/chart/templates/daemonset.yaml`, `platform/k8s-ws-proxy/chart/values.yaml`, `platform/k8s-ws-proxy/chart/tests/mtls-render.sh` and 16 more
- **ACC-108**: `platform/guacamole/chart/templates/admission-session-pods.yaml`, `platform/guacamole/chart/templates/policy-session-base.yaml`, `platform/guacamole/chart/values.yaml`, `clusters/_template/bootstrap-kit/52-bp-guacamole.yaml`, `platform/guacamole/chart/tests/render.sh`, `tests/acceptance/sovereign/internal/suite/check_acc_g05.go`, `docs/ledger/gates.yaml`
- **ACC-107**: `platform/guacamole/chart/crds/accesssession.yaml`, `platform/guacamole/chart/crds/accesstarget.yaml`, `core/controllers/access/api/v1alpha1/types.go`, `core/controllers/access/internal/broker/server.go`, `core/controllers/access/internal/broker/identity.go`, `core/controllers/access/internal/broker/sessions.go`, `core/controllers/access/internal/broker/tunnel.go`, `core/controllers/access/internal/broker/pages.go`, `core/controllers/access/internal/events/events.go`, `core/controllers/access/cmd/main.go`, `core/controllers/access/web/`, `core/controllers/access/Containerfile` and 9 more

</details>

### Group ACC-110: Epic: recording (order: ACC-111, ACC-112)

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **ACC-111**<br>new, 5 days | **Recording store: bucket, ingest, chain, manifest, signature**<br>Recordings bucket and its credential obtained through the object-storage capability binding and mounted, never a Secret; chunk ingest with order and digest rules; per-stream hash chain; manifest signed through transit; verify subcommand; recording_sealed event; sessions can no longer run unrecorded. | ACC-030, ACC-107, SVC-270, FND-011 | Makes ACC-G06 required. |
| **ACC-112**<br>new, 5 days | **Playback, the player page and the integrity checks**<br>Recordings routes with reviewer rule, own-session rule, write-ahead replay event and verification before the first byte; player page. | ACC-111 | Makes ACC-G02 required. |

<details><summary>Files each task of this group changes</summary>

- **ACC-111**: `platform/guacamole/chart/crds/accesssession.yaml`, `platform/guacamole/chart/crds/accesstarget.yaml`, `core/controllers/access/api/v1alpha1/types.go`, `core/controllers/access/internal/kinds/pod.go`, `core/controllers/access/internal/broker/sessions.go`, `core/controllers/access/internal/recording/`, `core/controllers/access/internal/broker/ingest.go`, `core/controllers/access/internal/openbao/client.go`, `core/controllers/access/internal/controller/session_controller.go`, `platform/guacamole/chart/templates/controller-deployment.yaml`, `platform/guacamole/chart/templates/recording-credential.yaml`, `platform/guacamole/chart/values.yaml` and 6 more
- **ACC-112**: `core/controllers/access/internal/broker/recordings.go`, `core/controllers/access/web/src/player.ts`, `core/controllers/access/web/replay.html`, `core/controllers/access/internal/broker/pages.go`, `docs/contracts/C3-event-subjects.yaml`, `products/catalyst/bootstrap/ui/e2e/gate/access/acc-g02.spec.ts`, `docs/ledger/gates.yaml`

</details>

### Group ACC-120: Epic: requests, approvals and the console (order: ACC-121, ACC-122, ACC-123)

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **ACC-121**<br>new, 5 days | **Intent routes for targets and grants**<br>Target, request, approval, reject, suspend and resume routes committing through intent.Submit with the writer-only scope the control-plane record names, under clusters/<fqdn>/intent/; tier rules; forced approval after suspension; grant-file housekeeping; the access files applied in the primary region only (decision A21). | ACC-107, CTL-120, CTL-130, CTL-160, FND-011 | Makes ACC-G04 required. |
| **ACC-122**<br>new, 5 days | **Console: targets, requests, approvals, sessions**<br>Four pages under one menu entry Access (targets, requests, my access, sessions) with row-level dialogs, live queries and no polling. | ACC-121, CTL-360 | Makes ACC-G17 required. |
| **ACC-123**<br>rework, 3 days | **The console pod shell opens an access session**<br>With the access kinds installed the shell button opens an access session or the request dialog; the direct route requires tier owner and a reason and is audited as break-glass; without the kinds it behaves as before. | ACC-112, ACC-122 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **ACC-121**: `platform/guacamole/chart/crds/accesstarget.yaml`, `core/controllers/access/api/v1alpha1/types.go`, `core/controllers/access/internal/controller/grant_controller.go`, `products/catalyst/bootstrap/api/internal/accessintent/`, `products/catalyst/bootstrap/api/internal/k8scache/`, `platform/guacamole/chart/templates/intent-kustomization.yaml`, `docs/contracts/C9-mutating-routes.yaml`, `products/catalyst/bootstrap/ui/e2e/gate/access/acc-g04.spec.ts`, `docs/ledger/gates.yaml`
- **ACC-122**: `products/catalyst/bootstrap/ui/src/pages/sovereign/access/`, `products/catalyst/bootstrap/ui/src/app/router.tsx`, `products/catalyst/bootstrap/ui/src/pages/sovereign/sovereignNav.ts`, `products/catalyst/bootstrap/ui/e2e/gate/access/acc-g17.spec.ts`, `docs/ledger/gates.yaml`
- **ACC-123**: `products/catalyst/bootstrap/ui/src/widgets/cloud-list/ExecPanel.tsx`, `products/catalyst/bootstrap/api/internal/handler/k8s_exec.go`, `products/catalyst/bootstrap/api/internal/handler/k8s_exec_ws.go`, `docs/contracts/C9-mutating-routes.yaml`

</details>

### Group ACC-130: Epic: SSH and database kinds, isolation proof (order: ACC-131, ACC-132, ACC-133)

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **ACC-131**<br>new, 5 days | **Kind ssh with signed certificates**<br>Target kind ssh; per-target OpenBao SSH role written and removed by the controller through a finalizer; certificates for one principal, 5 minutes, key identifier equal to the session UID, no forwarding. | ACC-020, ACC-030, ACC-112, SEC-310, FND-011 | Makes ACC-G07 required. |
| **ACC-132**<br>new, 5 days | **Kind database: PostgreSQL command-line workbench**<br>Target kind database (PostgreSQL on CloudNativePG, modes ro and rw, 55-minute cap); PgBouncer sidecar holding the dynamic user; psql tool image with no shell; db-proxy image with no shell. | ACC-131, SEC-290, FND-011 | Makes ACC-G08 required. |
| **ACC-133**<br>test, 3 days | **Organization isolation journey**<br>Check ACC-G09 on the two test Organizations of the network area's isolation journey. | ACC-132, NET-170 | Makes ACC-G09 required. |

<details><summary>Files each task of this group changes</summary>

- **ACC-131**: `platform/guacamole/chart/crds/accesstarget.yaml`, `core/controllers/access/api/v1alpha1/types.go`, `core/controllers/access/internal/kinds/ssh.go`, `core/controllers/access/internal/controller/target_controller.go`, `core/controllers/access/internal/openbao/client.go`, `platform/openbao/chart/templates/_config.tpl`, `platform/guacamole/chart/templates/controller-rbac.yaml`, `tests/acceptance/sovereign/fixtures/access/ssh-target.yaml`, `products/catalyst/bootstrap/ui/e2e/gate/access/acc-g07.spec.ts`, `docs/RUNBOOKS.md`, `docs/ledger/gates.yaml`
- **ACC-132**: `platform/guacamole/chart/crds/accesstarget.yaml`, `core/controllers/access/api/v1alpha1/types.go`, `core/controllers/access/internal/kinds/database.go`, `core/cmd/access-session-agent/internal/dbproxy/`, `core/cmd/access-session-agent/images/db-proxy/Containerfile`, `core/cmd/access-session-agent/images/tool-psql/Containerfile`, `.github/workflows/build-access-session-agent.yaml`, `platform/guacamole/chart/values.yaml`, `tests/acceptance/sovereign/fixtures/access/db-target.yaml`, `products/catalyst/bootstrap/ui/e2e/gate/access/acc-g08.spec.ts`, `docs/ledger/gates.yaml`
- **ACC-133**: `products/catalyst/bootstrap/ui/e2e/gate/access/acc-g09.spec.ts`, `tests/acceptance/sovereign/fixtures/access/g09/`, `docs/ledger/gates.yaml`

</details>

### Single tasks

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **ACC-140**<br>document, 1 day | **Framework mapping as data; runbook**<br>Mapping rows of section 3.12 each naming its gate check; runbook section Privileged access including the recordings step after a move of the Primary. | ACC-133, CMP-121 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **ACC-140**: `docs/RUNBOOKS.md`

</details>

## PRD Later products

### Single tasks

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **PRD-040**<br>lab test, 5 days | **Spike: authorisation server fitness, consent carrier and conformance run**<br>Result record with the conformance plan and variant names and outcome A to D. | nothing else | stated in its ticket |
| **PRD-060**<br>lab test, 3 days | **Spike: vector search with row-level trimming on the platform's PostgreSQL**<br>Extension version, recall at 10 and p95 latency on one million chunks with a one-percent reader. | nothing else | stated in its ticket |
| **PRD-050**<br>lab test, 3 days | **Spike: client certificates on the Organization listener**<br>Result record: gateway validation with forwarded certificate (A) or TLS passthrough (B). | NET-191 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **PRD-040**: `docs/ledger/plan/spikes/PRD-040-*.md`
- **PRD-060**: `docs/ledger/plan/spikes/PRD-060-*.md`
- **PRD-050**: `docs/ledger/plan/spikes/PRD-050-*.md`

</details>

