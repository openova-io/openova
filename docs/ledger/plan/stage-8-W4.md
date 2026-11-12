# Stage 8 (W4): Products

AI hub and open banking.

69 tasks. One task is one pull request. Rows are in the order the tasks can run. "Waits for" lists the tasks that must be merged first; every task also waits for the first two tasks of the plan, `FND-021` and `FND-022`.

[Back to the overview](README.md)

## Areas in this stage

- [NET: Network, zones and cluster classes](#net-network-zones-and-cluster-classes) — 3 tasks
- [BSS: Commerce and billing](#bss-commerce-and-billing) — 1 tasks
- [AIP: Agent plane](#aip-agent-plane) — 5 tasks
- [INF: Infrastructure plane](#inf-infrastructure-plane) — 6 tasks
- [PRD: Later products](#prd-later-products) — 54 tasks

## NET Network, zones and cluster classes

### Single tasks

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **NET-195**<br>new, 3 days | **Organization listener: client-certificate validation or TLS passthrough, by the outcome of PRD-050 (conditional, dropped until PRD-050 closes)**<br>Outcome A of PRD-050: opt-in client-certificate validation per route host on an Organization listener. Outcome B: a TLS passthrough route is allowed on an Organization listener for hosts the Organization owns. Carries R-NET-1. | NET-191, PRD-050 | stated in its ticket |
| **NET-330**<br>lab test, 3 days | **Spike: a bridged secondary network for virtual machines (conditional, dropped until INF-810 is scheduled)**<br>A result record that says how a second, bridged network sits beside the pod network on the reference class, whether a machine keeps its address on it, and what policy covers it. Outcome words are fixed in the ticket when the package is revived. | INF-020 | stated in its ticket |
| **NET-171**<br>new, 3 days | **Organization fence: two labelled exceptions for the open-banking product (conditional, dropped until epic PRD-500 is scheduled)**<br>(a) Ingress from namespace keycloak to port 8081 of pods labelled fingate.openova.io/listener: authz, with the matching egress rule in Keycloak's allow file. (b) Egress from pods labelled fingate.openova.io/signer: "true" to the OpenBao API port, only if the secrets record accepts transit signing from an Organization pod. The controller renders each exception only in an Organization that holds such pods. Carries R-NET-4 parts a and b. | NET-164, NET-170, FND-011 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **NET-171**: `platform/network-policies/chart/templates/allow/keycloak.yaml`, `tests/acceptance/sovereign/policy-dials.yaml`

</details>

## BSS Commerce and billing

### Group BSS-130: Epic: model usage

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **BSS-133**<br>rework, 3 days | **The engine books request counts and audio seconds**<br>Two more rows in the mapping of the usage consumer: measure llm.requests to SKU llm.<alias>.requests (unit request), and the audio-seconds measure to SKU llm.<alias>.audio_seconds (unit second). Priced by price items like every platform meter. Holds lock billing-engine. Also waits for the agent-plane package that meters the three task routes (not named yet); ticket_rev 0 until then. | BSS-131, AIP-111 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **BSS-133**: `products/chargeback/internal/adapter/openova/modelusage.go`, `products/chargeback/internal/adapter/openova/modelusage_test.go`, `products/chargeback/internal/store/events.go`, `products/chargeback/internal/store/models.go`

</details>

## AIP Agent plane

### Single tasks

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **AIP-145**<br>rework, 5 days | **Task routes: embeddings, rerank and transcription, with their metering**<br>POST /v1/embeddings, /v1/rerank and /v1/audio/transcriptions on the gateway address with the same credential check and the same removal of caller-set identity headers. Back-end keys embedding, rerank and transcription join the closed list of the alias document, each enabled false by default and settable by substitution in slot 81. An alias whose back end is one of the three and is disabled is not rendered (skipped, not failed). The meter publishes the one usage event for the three routes: llm.requests always, token measures where the back end reports them, and the new measure llm.audio.seconds for transcription. If the spike record says the gateway cannot route a path, the meter serves it behind the same address. | AIP-010, AIP-113, AIP-140, FND-011 | stated in its ticket |
| **AIP-171**<br>fix, 1 day | **The signer key set resolves inside Organization virtual clusters**<br>One more entry in the replicated-Service list of the Organization controller for the Service that serves GET /api/v1/auth/signers.jwks, one fence allow rule limited to that port, and one allowed and one denied dial row. First reader: PRD-301. | AIP-120, AIP-202 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **AIP-145**: `platform/model-gateway/chart/**`, `core/services/model-meter/**`, `docs/contracts/model-contract.md`
- **AIP-171**: `core/controllers/organization/internal/gitops/manifests.go`, `core/controllers/organization/internal/gitops/manifests_test.go`, `tests/acceptance/sovereign/policy-dials.yaml`

</details>

### Group AIP-290: EPIC: Switch-over to self-hosted serving (when GPUs exist)

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **AIP-291**<br>new, 5 days | **bp-vllm with tool-call parser, API key file and model cache on a GPU pool**<br>As before, built to the serving rules of the later-products record; lowers that record's vllm secretKeyRef baseline to 0. The accelerators flag of the cloud profile is added only if the ticket names its reader in this package. | AIP-140, CLD-175, PRD-405, FND-011 | Makes AIP-G21 required. |
| **AIP-292**<br>fix, 3 days | **Alias document repointed to selfhosted; evaluation sets re-run**<br>Callers unchanged; aliases served by self-hosted models | AIP-291, PRD-402 | stated in its ticket |
| **AIP-293**<br>delete, 3 days | **Delete Axon, slot 82, the credential routes and their guard entries**<br>Simulation back end removed | AIP-292, FND-011 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **AIP-291**: `platform/vllm/**`, `clusters/_template/bootstrap-kit/39-bp-vllm.yaml`
- **AIP-292**: `clusters/_template/bootstrap-kit/81-bp-model-gateway.yaml`
- **AIP-293**: `products/axon/**`, `clusters/_template/bootstrap-kit/82-bp-axon.yaml`, `products/catalyst/bootstrap/api/internal/modelsim/**`, `docs/ledger/deleted-paths.txt`, `scripts/agent-plane-baseline.yaml`

</details>

## INF Infrastructure plane

### Group INF-800: Epic: KubeVirt implementation (INF-810, 820, 830) — scheduled only if INF-020 says yes for a pack or a bare-metal pack exists

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **INF-810**<br>new, 5 days | **Blueprints bp-kubevirt and bp-cdi**<br>Two Blueprints with function tests | INF-020, SVC-060, FND-011 | stated in its ticket |
| **INF-820**<br>new, 5 days | **Composition virtualmachine-kubevirt, instance types, profile flag**<br>Field map of 4.20 as a Composition | CLD-080, INF-230, INF-810, FND-011 | stated in its ticket |
| **INF-830**<br>test, 3 days | **Check INF-G10**<br>The INF-G03 walk on the KubeVirt Composition | INF-820 | Makes INF-G10 required. |

<details><summary>Files each task of this group changes</summary>

- **INF-810**: `platform/kubevirt/**`, `platform/cdi/**`
- **INF-820**: `platform/kubevirt/chart/templates/instancetypes.yaml`
- **INF-830**: `tests/acceptance/sovereign/internal/suite/check_inf_g10.go`, `docs/ledger/gates.yaml`

</details>

### Group INF-900: Epic: Windows and hypervisor sources (INF-910, 920, 930) — outline; behind founder decision F4 and the migration area

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **INF-930**<br>new, 5 days | **Source type hypervisor**<br>Enum value hypervisor with its block and status.parent, sharing the migration area's listing code | INF-320, MIG-113, FND-011 | Makes INF-G15 required. |
| **INF-910**<br>new, 5 days | **Windows Server 2022 and later over SSH: facts, pending-update report, patch**<br>Windows facts, pending-update vulnerability report and patching by certificate | INF-455 | Makes INF-G13 required. |
| **INF-920**<br>new, 5 days | **Windows before 2022 over HTTPS remote management with a rotated password**<br>The one stored machine credential, in OpenBao only, replaced after every run | INF-910, SEC-190 | Makes INF-G14 required. |

<details><summary>Files each task of this group changes</summary>

- **INF-930**: `platform/machine-operations/chart/crds/machinesources.yaml`, `products/catalyst/bootstrap/api/internal/machines/source/hypervisor.go`
- **INF-910**: `products/machine-runner/content/playbooks/facts-windows.yml`, `products/machine-runner/content/playbooks/patch-windows.yml`, `products/catalyst/bootstrap/api/internal/machines/runner/windows.go`, `products/machine-runner/tools.lock.yaml`
- **INF-920**: `products/catalyst/bootstrap/api/internal/machines/runner/winrm.go`, `products/catalyst/bootstrap/api/internal/machines/operations/job.go`

</details>

## PRD Later products

### Group PRD-100: Epic: open-banking specification kit

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **PRD-101**<br>test, 3 days | **Vendor the six public documents and write the profile**<br>Vendored documents with hashes, profile.yaml as section 4.9, the not-implemented clause quoted, the old README replaced. | FND-023 | stated in its ticket |
| **PRD-104**<br>test, 3 days | **Consent package**<br>Next and Wire with property tests C4 for four families. | PRD-101 | stated in its ticket |
| **PRD-105**<br>test, 3 days | **Token package**<br>Rules T1 to T10 as one Check function with tests C3. | PRD-101 | stated in its ticket |
| **PRD-106**<br>test, 1 day | **CI workflow for the fingate module**<br>go vet, go test and generate drift check on every pull request touching the folder. | PRD-101 | stated in its ticket |
| **PRD-102**<br>test, 5 days | **Generated types and server interfaces (oapi-codegen, strict server)**<br>One generated package per document; no hand edits. | PRD-106 | stated in its ticket |
| **PRD-103**<br>test, 5 days | **Ledger port and conformance kit**<br>ledger.proto as section 4.14 with stubs, the kit (test C5) and an in-memory ledger. | PRD-106 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **PRD-101**: `products/fingate/go.mod`, `products/fingate/README.md`, `products/fingate/profiles/rw-4.0.0/*.yaml`, `products/fingate/profiles/rw-4.0.0/SOURCE.md`, `products/fingate/internal/profile/*.go`
- **PRD-104**: `products/fingate/internal/consent/**`
- **PRD-105**: `products/fingate/internal/token/**`
- **PRD-106**: `.github/workflows/fingate-ci.yaml`
- **PRD-102**: `products/fingate/internal/gen/**`, `products/fingate/tools/generate.go`, `products/fingate/tools/go.mod`, `products/fingate/tools/go.sum`, `products/fingate/tools/oapi/*.yaml`
- **PRD-103**: `products/fingate/api/ledger/v1/**`, `products/fingate/cmd/fingate-ledger-conformance/**`, `products/fingate/internal/ledgertest/**`

</details>

### Group PRD-200: Epic: AI hub slice A1, knowledge search without GPUs

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **PRD-202**<br>test, 5 days | **cortex store: migrations, roles, trimmed query**<br>Migration 0001, two roles, Store port, integration tests incl. per-document reader list and row-level security; old README replaced. | FND-023, PRD-060 | stated in its ticket |
| **PRD-203**<br>test, 5 days | **cortex query process: token check, gateway client, search, definitions loader**<br>Search and sources routes with realm-token check; TestEveryRouteAuthenticated; guard rules G-P4 and G-P5. | PRD-030, PRD-202 | stated in its ticket |
| **PRD-204**<br>test, 5 days | **cortex ingestion of uploads: job table, upload and document routes, index process, text parser, chunker, events stream**<br>Upload with optional reader fields, document routes, events stream, index process; no fetch Job. | PRD-203 | stated in its ticket |
| **PRD-205**<br>test, 3 days | **Assistants: models list, answers with citations, residency refusal**<br>GET /v1/models and POST /v1/chat/completions with tiers, citations and the external-models refusal. | PRD-204 | stated in its ticket |
| **PRD-211**<br>test, 3 days | **Document conversion parser**<br>Adapter to the converter container chosen by PRD-080, mapped to the parsed-document format. | PRD-080, PRD-204 | stated in its ticket |
| **PRD-206**<br>test, 5 days | **cortex screens**<br>Screens for sources, upload, documents, job state and assistants; every save goes through the Application update route with If-Match and unchanged bindings. | PRD-205 | stated in its ticket |
| **PRD-201**<br>rework, 5 days | **bp-bge chart to the serving rules**<br>Chart to rules 1 to 8 with home and enabled, CSI file in place of secretKeyRef, function test; baseline for bge 0. | PRD-030, PRD-080, SVC-170, FND-011 | stated in its ticket |
| **PRD-207**<br>test, 3 days | **Gate checks for slice A1**<br>Three registered checks that create their own test Organization. | FND-032, FND-038 | Adds the check PRD-G01, PRD-G03, PRD-G04. |
| **PRD-212**<br>fix, 1 day | **Allow file for namespace model-serving**<br>Ingress only from model-gateway, egress to DNS and the registry; one allowed and one denied dial row. | NET-150, NET-163, FND-011, NET-030 | stated in its ticket |
| **PRD-208**<br>new, 3 days | **Embeddings and rerank in the model plane: slot 42, kit key, aliases**<br>Slot for bp-bge switched by MODEL_PLANE_EMBEDDINGS; embedding and rerank back ends and two aliases in the gateway slot. | AIP-140, AIP-145, FND-011, FND-035, PRD-201, PRD-207, PRD-212, SEC-211, SOV-110, SOV-760 | Makes PRD-G03, PRD-G04 required. |
| **PRD-209**<br>new, 5 days | **Publish bp-cortex**<br>Blueprint with the upload-only schema, chart with CSI classes, public sign-on client registration, function test, signed image; guard rule G-P6. | AIP-170, AIP-202, AIP-270, CTL-160, CTL-162, CTL-415, NET-170, NET-190, NET-191, PRD-205, PRD-206, PRD-208, PRD-211, SEC-205, SEC-213, SVC-260, SVC-270, SVC-320, FND-011 | Makes PRD-G01 required. |
| **PRD-210**<br>test, 1 day | **The release train installs the AI hub and declares product-ai-hub**<br>Train provisions with MODEL_PLANE_EMBEDDINGS true and declares the flag; its revert is the rollback for every AI hub check. | FND-141, PRD-209 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **PRD-202**: `products/cortex/go.mod`, `products/cortex/README.md`, `products/cortex/internal/store/**`, `.github/workflows/cortex-ci.yaml`
- **PRD-203**: `products/cortex/cmd/cortex/**`, `scripts/check-products.py`, `scripts/baselines/products.json`
- **PRD-204**: `products/cortex/internal/httpapi/**`, `products/cortex/cmd/cortex/**`
- **PRD-205**: `products/cortex/internal/assistant/**`, `products/cortex/internal/httpapi/**`
- **PRD-211**: `products/cortex/internal/parse/documents/**`
- **PRD-206**: `products/cortex/ui/**`
- **PRD-201**: `platform/bge/**`, `scripts/baselines/products.json`
- **PRD-207**: `tests/acceptance/sovereign/internal/suite/check_prd_serving.go`, `tests/acceptance/sovereign/internal/suite/check_prd_cortex.go`, `docs/ledger/gates.yaml`
- **PRD-212**: `platform/network-policies/chart/templates/allow/model-serving.yaml`, `platform/network-policies/chart/values.yaml`, `tests/acceptance/sovereign/policy-dials.yaml`
- **PRD-208**: `clusters/_template/bootstrap-kit/42-bp-bge.yaml`, `clusters/_template/bootstrap-kit/kustomization.yaml`, `clusters/_template/bootstrap-kit/placement.yaml`, `scripts/expected-bootstrap-deps.yaml`, `clusters/_template/bootstrap-kit/81-bp-model-gateway.yaml`, `docs/contracts/c5-kit-keys.txt`, `docs/ledger/gates.yaml`, `docs/contracts/c5-kit-keys-settable.txt`
- **PRD-209**: `products/cortex/blueprint.yaml`, `products/cortex/chart/**`, `products/cortex/Containerfile`, `.github/workflows/build-bp-cortex.yaml`, `scripts/check-products.py`, `scripts/baselines/products.json`, `docs/ledger/gates.yaml`
- **PRD-210**: `.github/workflows/release-train.yaml`

</details>

### Group PRD-500: Epic: open-banking slice B1, sandbox account information

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **PRD-503**<br>test, 5 days | **Keycloak consent extension**<br>Java extension with exactly the three calls and the spike's consent carrier; signed image holding the provider archive. | PRD-040, PRD-101 | stated in its ticket |
| **PRD-501**<br>test, 5 days | **Resource server base: store, definitions loader, router with the token check, account-access consent resources, not-offered answers**<br>Migration 0001, informer on definitions, router with TestEveryRouteAuthenticated, three consent operations, notImplementedStatus answers; guard rules G-P4 and G-P7 at 3. | PRD-030, PRD-102, PRD-103, PRD-104, PRD-105 | stated in its ticket |
| **PRD-502**<br>test, 5 days | **Sandbox ledger**<br>The port over fixtures with at least three customers and five accounts. | PRD-103 | stated in its ticket |
| **PRD-508**<br>test, 5 days | **Account-information read operations and the ledger client**<br>The other 26 account-information operations with responses validated against the document. | PRD-103, PRD-501 | stated in its ticket |
| **PRD-509**<br>test, 3 days | **Internal authorisation API**<br>Second listener with the three routes of section 4.12. | PRD-508 | stated in its ticket |
| **PRD-512**<br>fix, 3 days | **OpenBao: partner client-certificate role and its policy line**<br>Policy line `pki/sign/NS.partner-client` (update) on policy workload, and role pki/roles/<ns>.partner-client with client-auth usage only and a maximum lifetime of 398 days. Holds the lock on the bp-openbao chart. Who creates the role per namespace is unsettled; no ticket before the secrets area confirms. | SEC-190, SEC-250, FND-011 | stated in its ticket |
| **PRD-504**<br>test, 5 days | **Registrar**<br>Keycloak clients for approved applications, certificate requests signed through the PKI role, issuance status; own client secret read from a mounted file. | PRD-501, PRD-512, SEC-250 | stated in its ticket |
| **PRD-506**<br>test, 3 days | **Gate checks for slice B1**<br>Two registered checks. | FND-032, FND-038 | Adds the check PRD-G05, PRD-G06. |
| **PRD-511**<br>rework, 3 days | **Keycloak chart option and its kit key**<br>Block fapi.enabled set from KEYCLOAK_OPEN_BANKING; init container copies the provider archive only when true. | PRD-503, SEC-221, SOV-110, SOV-760, FND-011 | stated in its ticket |
| **PRD-505**<br>test, 5 days | **Authorisation conformance workflow**<br>Workflow running the conformance plan recorded by PRD-040 against Keycloak, fingate-api and the sandbox ledger. | PRD-502, PRD-503, PRD-504, PRD-509, PRD-511 | stated in its ticket |
| **PRD-507**<br>new, 5 days | **Publish bp-fingate and bp-fingate-sandbox**<br>Charts with CSI classes, route or passthrough per PRD-050, function test, schema with products limited to accounts; guard rule G-P6. | CTL-160, CTL-162, CTL-415, FND-035, NET-170, NET-171, NET-190, NET-191, NET-195, PRD-050, PRD-502, PRD-504, PRD-505, PRD-506, SEC-205, SEC-211, SEC-290, SVC-320, FND-011 | Makes PRD-G05, PRD-G06 required. |
| **PRD-510**<br>test, 1 day | **The release train installs open banking and declares product-open-banking**<br>KEYCLOAK_OPEN_BANKING true and the capability in the train; its revert is the rollback for every open-banking check. | FND-141, PRD-507, PRD-511 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **PRD-503**: `platform/keycloak/extensions/fapi-consent/**`, `.github/workflows/build-keycloak-fapi.yaml`
- **PRD-501**: `products/fingate/cmd/fingate-api/**`, `scripts/check-products.py`, `scripts/baselines/products.json`
- **PRD-502**: `products/fingate/cmd/fingate-sandbox-ledger/**`, `products/fingate/internal/sandbox/**`
- **PRD-508**: `products/fingate/internal/api/ais_*.go`, `products/fingate/internal/ledgerclient/**`
- **PRD-509**: `products/fingate/internal/authz/**`, `products/fingate/cmd/fingate-api/**`
- **PRD-512**: `platform/openbao/chart/templates/_config.tpl`, `platform/openbao/chart/tests/policy-templates-render.sh`, `tests/e2e/bp-openbao-config-live.sh`
- **PRD-504**: `products/fingate/cmd/fingate-registrar/**`, `products/fingate/internal/registrar/**`
- **PRD-506**: `tests/acceptance/sovereign/internal/suite/check_prd_fingate.go`, `docs/ledger/gates.yaml`
- **PRD-511**: `platform/keycloak/chart/templates/fapi-*.yaml`, `platform/keycloak/chart/values.yaml`, `clusters/_template/bootstrap-kit/09-keycloak.yaml`, `docs/contracts/c5-kit-keys.txt`, `docs/contracts/c5-kit-keys-settable.txt`
- **PRD-505**: `.github/workflows/fingate-conformance.yaml`, `products/fingate/conformance/**`
- **PRD-507**: `products/fingate/blueprint.yaml`, `products/fingate/chart/**`, `products/fingate/Containerfile`, `products/fingate-sandbox/blueprint.yaml`, `products/fingate-sandbox/chart/**`, `.github/workflows/build-bp-fingate.yaml`, `scripts/check-products.py`, `docs/ledger/gates.yaml`
- **PRD-510**: `.github/workflows/release-train.yaml`

</details>

### Single tasks

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **PRD-800**<br>test, 3 days | **Adapter template for a second bank**<br>Go skeleton answering UNIMPLEMENTED for every operation, and a guide mapping each port operation to what a core-banking system must supply. | PRD-502 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **PRD-800**: `products/fingate/adapter-template/**`, `products/fingate/docs/writing-an-adapter.md`

</details>

### Group PRD-300: Epic: AI hub slice A2, fetch Job, connectors, tools, chat, speech

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **PRD-304**<br>test, 5 days | **bp-speech chart on CPU**<br>Chart to section 4.2 with the spike's image and model and a function test; added to guard scans. | PRD-030, PRD-080, SVC-170, FND-011 | stated in its ticket |
| **PRD-306**<br>new, 3 days | **Transcription in the model plane: slot 85, kit key, alias**<br>Slot switched by MODEL_PLANE_TRANSCRIPTION; transcription back end and alias transcribe-default. | AIP-145, FND-011, FND-038, PRD-208, PRD-212, PRD-304, SOV-110, SOV-760 | Makes PRD-G08 required. |
| **PRD-307**<br>test, 1 day | **The release train switches transcription on and declares model-transcription**<br>One kit key and one capability in the train. | PRD-306 | stated in its ticket |
| **PRD-301**<br>new, 3 days | **MCP tools for agents**<br>POST /mcp with knowledge_search and knowledge_get_document, agent tokens only. | AIP-120, AIP-171, AIP-202, PRD-210, FND-011 | Makes PRD-G09 required. |
| **PRD-303**<br>new, 5 days | **Chat front end with the user's identity**<br>Chat that forwards the user's token to cortex, with no credential of its own; librechat secretKeyRef baseline to 0 or the chart deleted. | PRD-070, PRD-210 | Makes PRD-G10 required. |
| **PRD-308**<br>new, 3 days | **Route that stores a user-supplied credential for an Application**<br>PUT /api/v1/organizations/{org}/applications/{app}/credentials/{name}: a signed-in manager of the Organization stores a value at mounted/data/<ns>/shared/user-<name>/e<epoch> through producer catalyst-api (create or replace). The value is never readable back and never reaches Git or a log. New code in a new package of the API, registered through a register function of CTL-035, with one entry in contract C9. Its reader is PRD-302. | CTL-035, CTL-045, PRD-210, SEC-192 | stated in its ticket |
| **PRD-302**<br>new, 5 days | **Sandboxed fetch Job and the Git connector**<br>Connector port, rules 1 to 9 each with a failing test, fetch binary, internal fetch-result and sync routes, Job template, fence policy, git and credential in the schema. | PRD-210, PRD-308, SEC-140, SEC-192 | Makes PRD-G02 required. |
| **PRD-305**<br>new, 3 days | **Web connector and scheduled sync**<br>Web connector, one CronJob per scheduled source, web and schedule in the schema. | PRD-302, FND-011 | Makes PRD-G11 required. |

<details><summary>Files each task of this group changes</summary>

- **PRD-304**: `platform/speech/**`, `scripts/check-products.py`, `scripts/baselines/products.json`
- **PRD-306**: `clusters/_template/bootstrap-kit/85-bp-speech.yaml`, `clusters/_template/bootstrap-kit/kustomization.yaml`, `clusters/_template/bootstrap-kit/placement.yaml`, `scripts/expected-bootstrap-deps.yaml`, `clusters/_template/bootstrap-kit/81-bp-model-gateway.yaml`, `docs/contracts/c5-kit-keys.txt`, `tests/acceptance/sovereign/internal/suite/check_prd_transcription.go`, `docs/ledger/gates.yaml`, `docs/contracts/c5-kit-keys-settable.txt`
- **PRD-307**: `.github/workflows/release-train.yaml`
- **PRD-301**: `products/cortex/internal/mcp/**`, `products/cortex/internal/auth/**`, `products/cortex/chart/templates/httproute.yaml`, `tests/acceptance/sovereign/internal/suite/check_prd_cortex_mcp.go`, `docs/ledger/gates.yaml`
- **PRD-303**: `scripts/baselines/products.json`, `docs/ledger/deleted-paths.txt`, `scripts/expected-bootstrap-deps.yaml`, `tests/acceptance/sovereign/internal/suite/check_prd_cortex_chat.go`, `docs/ledger/gates.yaml`
- **PRD-308**: `products/catalyst/bootstrap/api/internal/usercredential/**`, `products/catalyst/bootstrap/api/cmd/api/routes_usercredential.go`, `products/catalyst/bootstrap/api/cmd/api/testdata/routes.golden`, `docs/contracts/C9-mutating-routes.yaml`
- **PRD-302**: `products/cortex/cmd/cortex-fetch/**`, `products/cortex/internal/httpapi/**`, `products/cortex/blueprint.yaml`, `scripts/check-products.py`, `tests/acceptance/sovereign/internal/suite/check_prd_cortex_fetch.go`, `docs/ledger/gates.yaml`
- **PRD-305**: `products/cortex/internal/ingest/web/**`, `products/cortex/internal/httpapi/**`, `products/cortex/chart/templates/cronjob-sync.yaml`, `products/cortex/blueprint.yaml`, `tests/acceptance/sovereign/internal/suite/check_prd_cortex_sync.go`, `docs/ledger/gates.yaml`

</details>

### Group PRD-400: Epic: GPU runtime and model choice

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **PRD-401**<br>test, 5 days | **bp-gpu-runtime chart**<br>Chart wrapping the vendor GPU operator with enabled and driver.preinstalled, five alert rules, README with sizing tiers; no time slicing. | FND-202, PRD-030, PRD-090, SVC-170, FND-011 | stated in its ticket |
| **PRD-404**<br>fix, 1 day | **Allow file for the namespace of bp-gpu-runtime**<br>One allow file written from the network record's rule template for the namespace the chart of PRD-401 renders, one entry of enforcedNamespaces, one allowed and one denied dial row. The architect writes the namespace name in the ticket. | NET-150, PRD-401, FND-011, NET-030 | stated in its ticket |
| **PRD-403**<br>new, 3 days | **GPU runtime in the kit: slot 84, two kit keys, check**<br>Slot switched by GPU_RUNTIME and GPU_DRIVER_PREINSTALLED; check PRD-G07. | CLD-175, FND-011, FND-032, PRD-401, PRD-404, SOV-110, SOV-760 | Makes PRD-G07 required. |
| **PRD-405**<br>test, 3 days | **The release train provisions a GPU pool and declares gpu**<br>A GPU NodePool in the provision request, GPU_RUNTIME true, capability gpu. | PRD-403 | stated in its ticket |
| **PRD-402**<br>lab test, 3 days | **Choose the default self-hosted chat model by measurement**<br>Tool-call success, latency and licence for at most three candidates, scored by another alias; the choice and values for AIP-292. | AIP-291, PRD-209, PRD-405 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **PRD-401**: `platform/gpu-runtime/**`, `scripts/check-products.py`
- **PRD-404**: `platform/network-policies/chart/values.yaml`, `tests/acceptance/sovereign/policy-dials.yaml`
- **PRD-403**: `clusters/_template/bootstrap-kit/84-bp-gpu-runtime.yaml`, `clusters/_template/bootstrap-kit/kustomization.yaml`, `clusters/_template/bootstrap-kit/placement.yaml`, `scripts/expected-bootstrap-deps.yaml`, `docs/contracts/c5-kit-keys.txt`, `tests/acceptance/sovereign/internal/suite/check_prd_gpu.go`, `docs/ledger/gates.yaml`, `docs/contracts/c5-kit-keys-settable.txt`
- **PRD-405**: `.github/workflows/release-train.yaml`
- **PRD-402**: `docs/ledger/plan/spikes/PRD-402-model-choice.md`

</details>

### Group PRD-600: Epic: open-banking slice B2, domestic payments, funds confirmation, recurring payments, events

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **PRD-607**<br>fix, 3 days | **OpenBao: transit signing lines for an Application**<br>Policy lines `transit/sign/NS.SA.*` (update) and `transit/keys/NS.SA.*` (read, public key only) on policy workload; signing keys at transit/keys/<ns>.<sa>.<name> of type RSA-3072. Holds the lock on the bp-openbao chart. Who creates the key is unsettled; no ticket before the secrets area confirms. | PRD-510, SEC-190, FND-011 | stated in its ticket |
| **PRD-601**<br>test, 5 days | **Domestic payments with detached signatures and idempotency**<br>Six domestic payment operations; signatures through the transit key. | NET-171, PRD-510, PRD-607, SEC-190 | stated in its ticket |
| **PRD-602**<br>test, 5 days | **Domestic scheduled payments and standing orders**<br>Ten operations using the port's Recurrence message; no international or file operation. | PRD-601 | stated in its ticket |
| **PRD-603**<br>test, 5 days | **Funds confirmation and variable recurring payments**<br>Four funds-confirmation and nine recurring-payment operations. | PRD-601 | stated in its ticket |
| **PRD-605**<br>test, 5 days | **Outbox relay and the events operations**<br>Five event operations and the relay; its outbound request validated against the event-notification document. | PRD-601 | stated in its ticket |
| **PRD-604**<br>new, 3 days | **Release payments, funds confirmation and recurring payments**<br>Family names added to the schema enum; check PRD-G12. | PRD-602, PRD-603, FND-011 | Makes PRD-G12 required. |
| **PRD-606**<br>new, 3 days | **Release events**<br>Relay Deployment, events family in the schema, G-P7 baseline fixed at 63. | PRD-604, PRD-605, FND-011 | Makes PRD-G13 required. |

<details><summary>Files each task of this group changes</summary>

- **PRD-607**: `platform/openbao/chart/templates/_config.tpl`, `platform/openbao/chart/tests/policy-templates-render.sh`, `tests/e2e/bp-openbao-config-live.sh`
- **PRD-601**: `products/fingate/internal/api/pis_domestic*.go`
- **PRD-602**: `products/fingate/internal/api/pis_scheduled*.go`, `products/fingate/internal/api/pis_standing*.go`
- **PRD-605**: `products/fingate/cmd/fingate-relay/**`
- **PRD-604**: `products/fingate/blueprint.yaml`, `products/fingate/chart/**`, `tests/acceptance/sovereign/internal/suite/check_prd_fingate_payments.go`, `docs/ledger/gates.yaml`
- **PRD-606**: `products/fingate/blueprint.yaml`, `products/fingate/chart/**`, `tests/acceptance/sovereign/internal/suite/check_prd_fingate_events.go`, `docs/ledger/gates.yaml`, `scripts/baselines/products.json`

</details>

### Group PRD-700: Epic: open-banking slice B3, portal and onboarding

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **PRD-701**<br>test, 5 days | **Portal: catalogue from the documents, application form, status, sandbox console**<br>Developer screens; table onboarding_request (migration 0002); a registration is one request row. | PRD-510 | stated in its ticket |
| **PRD-702**<br>test, 3 days | **Bank-admin screens: approvals, pins, delivery replay**<br>Approval and pin changes as one call to the Application update route with the admin's token; replay of dead deliveries. | PRD-701 | stated in its ticket |
| **PRD-703**<br>new, 3 days | **Release the portal**<br>Portal in the chart; check PRD-G14. | PRD-702, FND-011 | Makes PRD-G14 required. |

<details><summary>Files each task of this group changes</summary>

- **PRD-701**: `products/fingate/ui/**`, `products/fingate/cmd/fingate-portal/**`, `products/fingate/internal/portal/**`
- **PRD-702**: `products/fingate/ui/src/admin/**`, `products/fingate/internal/portal/admin*.go`
- **PRD-703**: `products/fingate/chart/**`, `tests/acceptance/sovereign/internal/suite/check_prd_fingate_portal.go`, `docs/ledger/gates.yaml`

</details>

