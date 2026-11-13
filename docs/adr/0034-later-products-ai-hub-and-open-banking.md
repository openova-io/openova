# ADR-0034 — Later products: the AI hub and open banking

Status: Accepted
Date: 2026-11-13

Written by work package PRD-020. It records the boundaries and the main contracts of two products that are built later: the AI hub (`products/cortex`) and the open-banking product (`products/fingate`). Both are months away and the code beneath them will change, so this decision fixes what each product is, what it may use and what it never becomes. The steps of each work package are written when its ticket is cut.

Terms used here:

- **AI hub**: retrieval over an Organization's documents, ingestion, document processing, assistants, speech and the model servers that the platform serves on GPUs.
- **Open-banking product**: an API that third parties call to read accounts and make payments for a bank's customers, under a public open-banking standard.
- **Model gateway**: the one gateway in front of every model, with its aliases and its meter, as ADR-0030 decides.
- **Profile**: the data that names one version of an open-banking standard, its documents, its operations and its security rules.

## Context

Measured on `main` on 2026-11-13.

- `products/cortex` holds one file, `products/cortex/README.md` (297 lines, header "Status: Accepted"). It draws a model server, a vector database, a graph database, a chat interface, guardrails and model tracing, and sizes them at four A10 cards. There is no chart and no code. `docs/STATUS.md` lists the folder as README only.
- `products/fingate` holds one file, `products/fingate/README.md` (416 lines). It says that no open-banking implementation ships, plans version 3.1.10 of the UK standard and sets `OB_STANDARD` to `uk-ob-3.1` by default. The current version of the Read/Write API standard is 4.0.0.
- `bp-vllm` 1.0.1 (`platform/vllm/chart`) wraps vLLM 0.6.4. It runs on CPU unless `vllm.gpu.enabled` is set. It has no tool-calling flag and no API key. Its values download the model from the public model hub (`allowHuggingfaceEgress: true`) and ship the network policy switched off. Kit slot 39 (`clusters/_template/bootstrap-kit/39-bp-vllm.yaml`) installs it with `enabled: ${SOVEREIGN_VLLM_ENABLED:-false}`, and no code sets that variable.
- `bp-bge` 1.0.0 (`platform/bge`) serves the embedding model `BAAI/bge-small-en-v1.5` and the reranker `BAAI/bge-reranker-base` on CPU.
- `bp-librechat` 1.1.0 (`platform/librechat/chart`) wraps LibreChat 0.7.5. It renders an Ingress and reads nine values through `secretKeyRef` in its Deployment.
- `bp-kserve` (chart 1.0.0, upstream 0.16.0), `bp-knative`, `bp-langfuse` and `bp-nemo-guardrails` are wrapper charts. None of them, and neither `bp-bge` nor `bp-librechat`, has a kit slot file. `scripts/expected-bootstrap-deps.yaml` reserves slot 38 for `bp-kserve`, slot 42 for `bp-bge` and slot 48 for `bp-librechat`.
- The Blueprints for Milvus and Neo4j are a README and a `blueprint.yaml` each, with no chart.
- The platform has no GPU support. No Blueprint installs a GPU operator, and no file under `infra` names a GPU.
- ADR-0030 replaces the model gateways of today with one gateway, a simulation back end and a meter. Its contract, `docs/contracts/model-contract.md`, serves chat through aliases. The routes for embeddings, reranking and transcription are added by AIP-145.
- `platform/keycloak/README.md` claims a FAPI 2.0 module for the open-banking product. The chart under `platform/keycloak/chart` configures no FAPI client policy and no client-certificate authentication. It pins Keycloak 26.3.3. PRD-010 removes the claim.
- Cilium is 1.19.3 and the Gateway API definitions are 1.3.0. Whether a listener on these versions validates client certificates is not proven.
- The Blueprint definition (`products/catalyst/chart/crds/blueprint.yaml`) carries a free-form `configSchema`. The application controller (`core/controllers/application/internal/controller/application_controller.go`) marks an Application failed when its parameters do not match that schema.
- The application controller tells an Application its Organization's realm and issuer only for the Sovereign's own internal Organization. For any other Organization it renders nothing, and the chart keeps its defaults. CTL-415 delivers both values to every Organization.
- Application parameters are changed through `PUT /api/v1/sovereigns/{id}/applications/{name}` of catalyst-api. CTL-162 makes Application intent come from Git.
- `products/chargeback` is a first-party product with the layout `cmd/`, `internal/`, `chart/`, `ui/` and `blueprint.yaml`, an embedded user interface and embedded database migrations. `products/openova-mcp` is a first-party MCP server in Go with an `/mcp` route and 3,605 lines of tests.
- A per-Sovereign switch is a kit key of the upstream variables document (contract C5, SOV-110). The kit is one released artifact for every Sovereign, so only kit keys differ between Sovereigns.
- Neither product has an acceptance row or a function test. Their fourteen gate checks, PRD-G01 to PRD-G14, are listed in `docs/ledger/gates.yaml` and not built.
- The product guard `scripts/check-products.py` (PRD-030) holds the AI-hub charts at today's values: no route to a model server (G-P1), no model-provider host (G-P2), and a `secretKeyRef` count that may only fall (G-P3).

## Decision

The decisions are numbered P1 to P20.

| Decision | What is decided |
|---|---|
| P1 When product work may merge | Products are not held behind a wave. A product package merges when the gate checks of its dependencies are green. Specification work and spikes may run early. The open-banking specification kit (epic PRD-100) depends on no platform capability; its packages are taken only when no foundation package of their lane is ready. Nothing of either product enters the default bootstrap kit. |
| P2 Written fresh | Both products are written fresh in the platform's own stack. No code, chart, configuration, prompt, test corpus or fixture is copied from another implementation; another implementation is a reference for behaviour only. The only files taken unchanged are the publisher's public documents of the open-banking standard (PRD-101). |
| P3 Shape of the AI hub | The AI hub is a family of independent Blueprints with no umbrella chart. At Sovereign level: `bp-gpu-runtime`, `bp-bge` and `bp-speech`, with `bp-vllm` of the model plane. Each is a kit slot of ring `ai` (ADR-0020), switched by one kit key, and each model server is reached only through the model gateway. At Organization level: `bp-cortex` and a chat front end. `bp-cortex` holds retrieval, ingestion, document processing and assistants in one release, because they share one database and one access rule. |
| P4 How a model is reached | Every model call of an AI hub component goes to the model gateway, by alias, with a virtual key of the consumer Application (AIP-270). No AI hub chart publishes a model endpoint or holds a provider credential. A self-hosted model is a back end registered behind the same alias that the simulation back end serves today. |
| P5 No new kinds for the AI hub | A model server is a kit slot of the model plane, configured by the slot's values. Knowledge sources and assistants are list entries in `spec.parameters` of the `bp-cortex` Application, validated by its `configSchema` and written through the Application intent route. A GPU pool is a `NodePool` (CLD-175) with a GPU flavor, a label and a taint. |
| P6 Tenancy | Data is per Organization: stores, retrieval, ingestion and chat run inside the Organization's boundary. GPUs are shared per Sovereign: model servers are reached only through the gateway, which takes the Organization from the caller's verified credential. A model server for one Organization alone is the same Blueprint on a dedicated, tainted pool, offered later. |
| P7 Stores | One PostgreSQL database per `bp-cortex` Application, with the `vector` extension, holds chunks, vectors, access lists, definition status and the job table. Originals live in an object bucket. No Milvus, Neo4j, MongoDB, Redis or RabbitMQ. |
| P8 Access control in retrieval | Access is decided per document. It is enforced inside the vector query with bound parameters, and a second time by PostgreSQL row-level security. Identity is the Organization's Keycloak token with the audience checked. Readers are set per source. A manager may give one uploaded document its own, narrower reader list. Copying readers from an origin system waits for the connector that would write them. |
| P9 Ingestion | Fetching untrusted locations is split from indexing. A fetch Job holds only the connector credential and may dial only public addresses. The indexer holds the database credential and has no internet egress. Work is a job table woken by `LISTEN/NOTIFY`. Nothing inside the Organization connects to the shared event stream. |
| P10 Assistants and agents | An assistant is one retrieval and one generation, with no tool loop. It is offered in the OpenAI format for chat front ends only. Agents that use tools are the agents of the model plane (ADR-0030), which call `bp-cortex` through two MCP tools with their agent token. |
| P11 What is offered before GPUs | With zero GPUs: ingestion, document parsing, embeddings and reranking on CPU inside the Sovereign, knowledge search, batch speech on CPU, and cited answers through the gateway's chat alias. While the chat alias is served by the simulation back end, the catalogue states that answers leave the Sovereign, and each Organization must opt in. No credential supplied by one party serves another party (ADR-0030, decision M6): each Organization brings its own sign-in or key. This decision adds no model credential. With GPUs only: self-hosted chat, vision OCR and real-time speech. |
| P12 Form of the open-banking product | A specification-first build in Go in `products/fingate`. Handlers and validators are generated from the public OpenAPI documents. One modular binary serves the regulated path; four small binaries do the rest. |
| P13 Standards and versions | The standard is data: a profile. The first profile is the Read/Write API 4.0.0: six documents with 89 operations, of which the product serves 63, calls 1, and answers the profile's not-implemented response for the 25 international and file-payment operations. It goes with FAPI 1.0 Advanced Final, pushed authorisation requests, signed request objects, PS256, mutual-TLS or `private_key_jwt` client authentication, certificate-bound tokens, and detached signatures where the documents require them. FAPI 2.0 with rich authorisation requests is the recorded second profile. PRD-101 asserts the operation counts in a test. |
| P14 Customer authorisation | Only the authorisation server issues codes and tokens, and it is Keycloak. The product is installed in an Organization of its own, and that Organization's realm is the open-banking realm. No Blueprint field asks for a realm. The consent step is a Keycloak extension that calls the product's consent API. The resource server accepts one bearer type. |
| P15 Core-banking port | Core banking is reached only through a typed gRPC port of 14 operations, served by a separate adapter workload. Consent, authorisation, idempotency and the rule that a payment equals its consent live above the port. A conformance kit proves an adapter. The sandbox ledger is the first implementation. |
| P16 Where partner and product definitions live | Partners, their applications and the approved API products are list entries in `spec.parameters` of the `bp-fingate` Application. An approval is a commit. Consents, payments and deliveries are database records. |
| P17 Trust at the edge | Trust comes from a verified token bound to a verified client certificate. It never comes from network position or from an identity header that another service set. Where mutual TLS terminates is chosen by spike PRD-050 with a fixed rule. |
| P18 Language per service | Go for every first-party service. Java for the Keycloak extension only, because the extension interface is Java. TypeScript for the two user interfaces. No Python service: the model servers and the document converter are upstream images. |
| P19 What never becomes a product | The items listed under "Consequences" stay an installation's own Applications or stay outside the platform. |
| P20 The legacy-API broker | A separate component of `products/fingate`, built after the regulated slices and only for an installation that needs it. It is not part of the first slices, and no contract for it is fixed here. |

## Alternatives rejected

**P1.** (a) Wave gating, where nothing of the products starts until all earlier waves are green twice. Dependency gating replaces it, and the specification kit depends on nothing in the platform. (b) Letting the kit displace foundation work. Foundation work comes first, so the kit runs only when its lane is otherwise empty. (c) Starting product runtime code now. Its prerequisites (credential injection inside an Organization, the gateway alias, Application intent from Git) are not built, and work stacked on an unproven base is redone.

**P2.** Taking any existing asset unchanged. Each such asset is under a hundred lines to write fresh, and taking it would bring a foreign stack's habits with it.

**P3.** (a) One umbrella chart for the whole hub. A single release that mixes operators, definitions, data stores and workloads cannot be moved between installations. (b) Three Blueprints for retrieval, ingestion and document processing. They share one database, one bucket and one access rule, and three releases would need a shared-credential design that does not exist. (c) KServe as the serving layer now. It brings Knative or the inference extension, and one Deployment per model needs neither. KServe returns when one model needs a second replica; the alias hides the change. The wrapper charts `bp-kserve`, `bp-knative`, `bp-langfuse` and `bp-nemo-guardrails` are not part of the AI hub: no package of this area installs, edits or scans them, and the lifecycle rule of ADR-0020 decides whether they stay.

**P4.** (a) Letting `bp-cortex` call a model server directly inside the cluster. It would bypass metering, budgets and the residency rule, and the contract would become "call the Service with any key". (b) A second gateway inside the AI hub.

**P5.** (a) Seven custom resources: `ModelEndpoint`, `ModelRoute`, `GPUNodePool`, `KnowledgeSource`, `SourceType`, `RetrievalPipeline` and `Agent`. Each needs a definition, a controller and a status writer, and the intent path of an Organization admits Applications only (`docs/contracts/C4-git-layout-and-commit-library.md`). The application controller already validates parameters against `configSchema`. `GPUNodePool` would be a capability contract with one implementation, and `NodePool` already has flavor, labels and taints. Honest limit: one object per source with its own status is more Kubernetes-native. What would lift it: an Organization intent path that admits further kinds, and a second writer that needs status per object.

**P6.** (a) One shared hub per Sovereign with Organization filters. A filter mistake leaks documents across Organizations; a boundary does not. (b) GPUs per Organization by default. A 24 GB card costs too much to leave idle.

**P7.** (a) A dedicated vector database. The platform has no chart for one (Milvus is a README and a Blueprint file), it would be another stateful system to back up, and access trimming would need a second filter language. (b) A graph database. The licence of the community edition and the absence of a chart count against it; document relations such as "supersedes" are rows, added with their first reader. Honest limit: a dedicated vector store scales further. PostgreSQL with an HNSW index is the pattern up to tens of millions of chunks per Organization, and spike PRD-060 measures it. What would lift the limit: a measured corpus above that size, at which point the store interface takes a second implementation.

**P8.** (a) Access per knowledge source only. It cannot express a private document inside a shared source. (b) Filtering after the search. It leaks through result counts and scores and breaks recall. (c) Building filter strings by formatting request input.

**P9.** (a) One worker that fetches and indexes. A fetch steered by a user would then run in a pod that holds the database credential. (b) NATS inside the Organization. A subject of contract C3 (`docs/contracts/C3-event-subjects.yaml`) names no Organization, so subject permissions cannot separate Organizations on the one stream, and the screens need state columns anyway. Honest limit: a bus with a dead-letter stream is the stronger pattern. What would lift it: one NATS account per Organization.

**P10.** (a) An agent loop inside the retrieval service. The agent runtime belongs to the model plane. (b) No chat endpoint at all. A chat front end needs one, and the "agent as a model" form is allowed for chat front ends only.

**P11.** (a) Waiting for GPUs. Search, ingestion and parsing do not need them. (b) A small chat model on CPU. It cannot give a useful answer, and the gateway alias already has a back end.

**P12.** (a) Starting from an existing implementation and cleaning it in place. It contradicts P2 and starts a refactor loop. (b) Adopting an open-source open-banking stack. None was evaluated; spike PRD-040 is where another authorisation server would show up if Keycloak fails.

**P13.** (a) International and file payments in the first profile. They are 25 of the 41 payment operations, the ledger port would need three more payment types and a file transfer, and no installation is scheduled that offers them. The standard lets a provider leave an endpoint unimplemented. They come with a later package that adds the port fields together with their first reader. (b) FAPI 2.0 with rich authorisation requests as the first profile. The 4.0 standard requires FAPI 1.0 Advanced Final, and Keycloak's support for rich authorisation requests is not confirmed. It stays the second profile. What would lift the limit: a regulator profile that asks for it and a Keycloak version that passes its conformance plan. (c) Hard-coding one standard. Regional standards derive from this one and differ in enumerations, currency and identity; a profile directory costs one folder.

**P14.** (a) Any code or token issued by the product itself. (b) A second authorisation server product. (c) Installing the product in the Organization of the bank's staff. Third parties and bank customers must not share a realm with staff. (d) A Blueprint field that asks for a second realm inside one Organization. An Organization already has one realm, so the product gets an Organization of its own, and the platform's entities stay as they are.

**P15.** (a) An adapter class with one untyped method per API operation. A second adapter would re-implement product rules. (b) An in-process plug-in. A separate workload gives the adapter its own release cycle, language and network policy.

**P16.** (a) Custom resources for partners, applications and API products, for the reasons of P5. (b) A database table edited from a portal. It bypasses Git. The volume is tens to low hundreds of partners per installation, well inside one object.

**P17.** (a) Trusting a header that says which proxy forwarded the call. (b) Choosing the termination point without a test. The pinned Cilium and Gateway API versions may or may not validate client certificates on a listener.

**P18.** (a) Python for the API and consent services. They are written fresh anyway and share types with the Go port. (b) A Go version of the Keycloak extension through a side-car. The extension must run inside the authentication flow.

**P19.** Shipping any of the items under "Consequences" as a platform Blueprint. Each one belongs to one installation, and a platform Blueprint would carry it into every other.

**P20.** Building the broker first. It is a different product, a partner API gateway for legacy systems, and nothing else needs it yet.

## Consequences

Defects that the products must never contain. Each has a test that fails when the defect appears.

| # | Forbidden | Kept out by |
|---|---|---|
| X1 | A route of a product service that answers without a verified token (health and metrics excepted; those are never routed) | A router test that fails on any route without the token check (guard rule G-P4) |
| X2 | A token accepted without issuer, audience, expiry and algorithm checks; more than one bearer type | Negative token tests |
| X3 | Access to an assistant, or to its sources, not checked on the call that answers | Gate check PRD-G01; a unit test with two users |
| X4 | A user-supplied location fetched without validation, by a process that holds other credentials; a local path accepted | No route accepts a location; connector validation tests; gate check PRD-G02 |
| X5 | A query or filter built by formatting input into a string | Guard rule G-P5; bound parameters only |
| X6 | A password-only or development sign-in route in a deployable build | The closed route list of G-P4; no build tag adds routes |
| X7 | A credential, key or test account in the repository | The secret scan (FND-055); sandbox fixtures carry no credential |
| X8 | A credential as a Secret object or an environment variable; third-party tokens stored readable in a database | Guard rule G-P3; connector credentials live only in OpenBao |
| X9 | Identity taken from another application's cookie or database | Only tokens of the Organization's realm are accepted (X2) |
| X10 | Message or prompt text in application logs | A log test: a search and an answer leave no query text at any level |
| X11 | A code or token issued outside the authorisation server | The product has no token endpoint; the conformance plan |
| X12 | Product rules (ownership, payment equals consent, idempotency) inside an adapter | The port has no consent or subscription operation; the ledger conformance kit |
| X13 | Trust from network position or a forwarded identity header | Token and certificate binding checked on every call |
| X14 | A private key generated by the server and handed to a partner; revocation by a timer | The registrar accepts certificate requests or certificates only |
| X15 | Configuration in a database behind a screen; a cluster object written from a pod | Product screens write only through the Application route; guard rule G-P6 |
| X16 | Polling for status or configuration | Informers, `LISTEN/NOTIFY` and server-sent events |
| X17 | Paths that claim a specification version the payloads do not meet; unused model types | Every response is validated against its document in tests; guard rule G-P7 |
| X18 | A model endpoint published on a route; a model reached without the gateway | Guard rules G-P1 and G-P2; gate check PRD-G03 |
| X19 | A quality judge that is the model under test; a test harness that disables TLS verification | The model measurement of PRD-402 scores with a different alias; guard rule G-P5 |
| X20 | A mock bank inside a production service | The sandbox ledger is a separate Blueprint behind the port |

Rules G-P1 to G-P3 exist in `scripts/check-products.py`. Each later rule is added by the package that adds the first code it scans: G-P4 by PRD-203 and PRD-501, G-P5 by PRD-203, G-P6 by PRD-209 and PRD-507, G-P7 by PRD-501.

What stays an installation's own Application, or stays outside the platform (P19):

1. Assistant instructions, topic taxonomies, document models and gold question sets of one installation. They are data in that Organization's `bp-cortex` parameters and bucket.
2. One-off corpus preparation: enrichment, embedding and OCR batch scripts.
3. Voice-emotion models. Transcription is the product; analysis of the transcript is the installation's.
4. An Anthropic-protocol adapter for coding tools. If the model gateway serves that route, it is a gateway setting of the model plane; otherwise it is not built.
5. Banking products built on top of the open-banking product, such as lending or account opening, with their own partner kits and webhooks.
6. Programme or project trackers.
7. A wrapper around one vendor's proprietary library, such as an e-signature kit.
8. A country's digital-identity endpoints, attribute names and deep links. They are identity-provider configuration of the open-banking realm, set per installation.
9. Demonstration third-party and bank applications. The platform ships one reference third-party client inside the conformance tests and none as a product.
10. The adapter for one named core-banking system. It is a workload behind the ledger port.
11. Brand, currency, time zone and identifier prefixes. They are profile and parameter values.

Further consequences:

- `products/cortex/README.md` and `products/fingate/README.md` describe designs that this decision withdraws. The first package that adds code to each folder rewrites its README in place: PRD-202 for `products/cortex`, PRD-101 for `products/fingate`.
- Each product has a first slice that ends in gate checks on the release train. AI hub slice A1, knowledge search without GPUs, ends with PRD-G01, PRD-G03 and PRD-G04 required (PRD-210). Open-banking slice B1, sandbox account information, ends with PRD-G05 and PRD-G06 required (PRD-510). From then on a failing product check fails the release train, like any other required check. Rollback is one revert of PRD-210 or PRD-510.

## Shipped via

Work packages of area PRD in `docs/ledger/plan.yaml`: PRD-010, PRD-030, the spikes PRD-040, PRD-050, PRD-060, PRD-070, PRD-080 and PRD-090, the epics PRD-100 (open-banking specification kit), PRD-200 (AI hub slice A1), PRD-300 (fetch Job, connectors, tools, chat and speech), PRD-400 (GPU runtime and model choice), PRD-500 (open-banking slice B1), PRD-600 (payments, funds confirmation, recurring payments and events) and PRD-700 (portal and onboarding), and PRD-800 (the starting point for a core-banking adapter).
